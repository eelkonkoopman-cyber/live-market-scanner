// Command scanner is the Live Market Scanner binary: it wires the feed, engine,
// scanner, news poller, and web server into one local process.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/eelkonkoopman/marketscanner/assets"
	"github.com/eelkonkoopman/marketscanner/internal/autosum"
	"github.com/eelkonkoopman/marketscanner/internal/blacklist"
	"github.com/eelkonkoopman/marketscanner/internal/bzstream"
	"github.com/eelkonkoopman/marketscanner/internal/config"
	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/daylog"
	"github.com/eelkonkoopman/marketscanner/internal/edgar"
	"github.com/eelkonkoopman/marketscanner/internal/engine"
	"github.com/eelkonkoopman/marketscanner/internal/feed"
	"github.com/eelkonkoopman/marketscanner/internal/flaglog"
	"github.com/eelkonkoopman/marketscanner/internal/flagread"
	"github.com/eelkonkoopman/marketscanner/internal/health"
	"github.com/eelkonkoopman/marketscanner/internal/ingest"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
	"github.com/eelkonkoopman/marketscanner/internal/news"
	"github.com/eelkonkoopman/marketscanner/internal/orderticket"
	"github.com/eelkonkoopman/marketscanner/internal/persist"
	"github.com/eelkonkoopman/marketscanner/internal/prefill"
	"github.com/eelkonkoopman/marketscanner/internal/prevah"
	"github.com/eelkonkoopman/marketscanner/internal/rtpr"
	"github.com/eelkonkoopman/marketscanner/internal/rvolfill"
	"github.com/eelkonkoopman/marketscanner/internal/scan"
	"github.com/eelkonkoopman/marketscanner/internal/schedule"
	"github.com/eelkonkoopman/marketscanner/internal/server"
	"github.com/eelkonkoopman/marketscanner/internal/sessions"
	"github.com/eelkonkoopman/marketscanner/internal/shortable"
	"github.com/eelkonkoopman/marketscanner/internal/summarize"
	"github.com/eelkonkoopman/marketscanner/internal/tape"
	"github.com/eelkonkoopman/marketscanner/internal/tray"
	"github.com/eelkonkoopman/marketscanner/internal/twslink"
	"github.com/eelkonkoopman/marketscanner/internal/universe"
)

// logPath is the resolved log file, recorded by run() so main() can point the
// user at it if startup fails.
var logPath string

func main() {
	// Pre-config fallback; run() switches to the log file once config is loaded.
	logx.Init(os.Getenv("SCANNER_LOG"))
	if err := run(); err != nil {
		logx.L().Error("fatal", "err", err)
		msg := "Live Market Scanner failed to start:\n\n" + err.Error()
		if logPath != "" {
			msg += "\n\nLog: " + logPath
		}
		fatalDialog(msg)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logPath = cfg.LogFile
	closer, lerr := logx.InitFile(cfg.LogLevel, cfg.LogFile)
	if lerr != nil {
		logx.Init(cfg.LogLevel)
		logx.L().Warn("log file unavailable; logging to stderr only", "path", cfg.LogFile, "err", lerr)
	} else {
		defer closer.Close()
	}

	// Single instance: if a scanner already answers on our address, just bring
	// its window forward and leave. Must run before the DB is opened.
	if scannerRunning(cfg.Listen) {
		logx.L().Info("already running; opening its window", "addr", cfg.Listen)
		if cfg.OpenBrowser {
			openUI(cfg)
		}
		return nil
	}

	sigCtx, stopSig := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSig()
	ctx, cancel := context.WithCancel(sigCtx)
	defer cancel()

	// Persistence.
	if dir := filepath.Dir(cfg.DBPath); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	db, err := persist.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	// Tape engine: order flow for the one selected symbol. Constructed before
	// the feed so its sink can be wired into the (single) WS connection.
	tp := tape.New()

	// Feed client + universe. Start permissive (track all) and narrow to regular
	// securities asynchronously, so a slow/failed reference fetch never blocks
	// startup — the UI is reachable immediately.
	//
	// SCANNER_FEED picks the market-data provider: "massive" (default, the
	// unchanged path) or "alpaca" (internal/alpaca, docs/alpaca-feed.md).
	feedName, err := feedKind()
	if err != nil {
		return err
	}
	var fc marketFeed
	if feedName == "alpaca" {
		ac, aerr := newAlpacaFeed(tp)
		if aerr != nil {
			return aerr
		}
		if !ac.HasKey() {
			logx.L().Warn("SCANNER_FEED=alpaca but no keys: create ~/.marketscanner/alpaca.json")
		}
		logx.L().Info("market data feed: alpaca", "feed", ac.Feed())
		fc = ac
	} else {
		fc = feed.New(feed.Config{
			APIKey: cfg.MassiveKey, WSURL: cfg.MassiveWSURL, RESTBase: cfg.MassiveBase,
			TapeSink:   tp.In(),
			TapeStatus: tp.SetError,
		})
	}
	uni := universe.NewAll()

	// Engine with persisted RVOL baseline + float reference injected.
	baseline, _ := db.BaselineRepo().LoadBaseline(ctx)
	floats, _ := db.FloatRepo().Floats(ctx)
	// The closed 1-minute bar judge of the GREEN DILDO and VOLUME CLIMAX alerts
	// (SCANNER_DILDO / SCANNER_CLIMAX, both on unless "0"). SignalLog: every
	// alert it calls also goes to the Signal window's history (flagLog below).
	// The GREEN DILDO tuning (Signal window panel) is the market-wide rule: load it
	// before the first bar is judged.
	dildoTune := dildoTuning{get: db.GetMeta, set: db.SetMeta}
	dildoTune.Load(ctx)
	eng := engine.New(engine.Options{TickHz: cfg.TickHz, Baseline: baseline, Floats: floats,
		Dildo: dildoEnabled(), Climax: climaxEnabled(), Bbs: bbsEnabled(), Ignition: ignitionEnabled(), EmaVwap: emaVwapEnabled(), VolBreak: volBreakEnabled(), SignalLog: true})

	// Narrow the universe to regular securities asynchronously; on success, prune
	// non-regular tickers that were seeded under the permissive startup set.
	go func() {
		if rerr := universe.Refresh(ctx, fc, uni); rerr != nil {
			logx.L().Warn("ticker reference fetch failed; tracking all symbols", "err", rerr)
			return
		}
		logx.L().Info("universe loaded", "tickers", uni.Len())
		eng.Retain(uni.Set())
		eng.SetSecTypes(uni.Types())
	}()

	// Scanner with persisted scan defs (seed a default on first run).
	scn := scan.New(eng)
	defs, _ := db.ScanRepo().List(ctx)
	if len(defs) == 0 {
		d := defaultScan()
		if err := db.ScanRepo().Save(ctx, d); err == nil {
			defs = []core.ScanDef{d}
			logx.L().Info("seeded default scan", "id", d.ID)
		}
	}
	scn.LoadAll(defs)
	// Pre-built scans: each batch seeded once per DB under its own meta key,
	// never resurrecting a deletion.
	seedScans(ctx, "seeded.flagscans.v1", flagScans(), db.GetMeta, db.SetMeta, defs, db.ScanRepo().Save, scn.Upsert)
	seedScans(ctx, "seeded.sessreview.v1", sessReviewScans(), db.GetMeta, db.SetMeta, defs, db.ScanRepo().Save, scn.Upsert)
	seedScans(ctx, "seeded.gapfade.v1", gapFadeScans(), db.GetMeta, db.SetMeta, defs, db.ScanRepo().Save, scn.Upsert)
	seedScans(ctx, "seeded.advscan.v1", premarketVolScans(), db.GetMeta, db.SetMeta, defs, db.ScanRepo().Save, scn.Upsert)
	seedScans(ctx, e9FlagSeedKey, emaFlagScans(), db.GetMeta, db.SetMeta, defs, db.ScanRepo().Save, scn.Upsert)
	seedScans(ctx, c1mSeedKey, c1mSpikeScans(), db.GetMeta, db.SetMeta, defs, db.ScanRepo().Save, scn.Upsert)
	seedScans(ctx, e9SlamSeedKey, emaFlagSlamScans(), db.GetMeta, db.SetMeta, defs, db.ScanRepo().Save, scn.Upsert)
	seedScans(ctx, hodFlagSeedKey, hodFlagScans(), db.GetMeta, db.SetMeta, defs, db.ScanRepo().Save, scn.Upsert)
	seedScans(ctx, dildoSeedKey, dildoScans(), db.GetMeta, db.SetMeta, defs, db.ScanRepo().Save, scn.Upsert)
	migrateDildoSendToTWS(ctx, db.GetMeta, db.SetMeta, db.ScanRepo().List, db.ScanRepo().Save, scn.Upsert)
	seedScans(ctx, climaxSeedKey, climaxScans(), db.GetMeta, db.SetMeta, defs, db.ScanRepo().Save, scn.Upsert)
	seedScans(ctx, bbsSeedKey, bbsScans(), db.GetMeta, db.SetMeta, defs, db.ScanRepo().Save, scn.Upsert)
	seedScans(ctx, bbsFormingSeedKey, bbsFormingScans(), db.GetMeta, db.SetMeta, defs, db.ScanRepo().Save, scn.Upsert)
	seedScans(ctx, powerCandleSeedKey, powerCandleScans(), db.GetMeta, db.SetMeta, defs, db.ScanRepo().Save, scn.Upsert)
	if q, ok := os.LookupEnv("SCANNER_PS_QUIET"); ok {
		if w, good := core.ParseQuietWindows(q); good {
			core.PsQuietWindows = w
		} else {
			logx.L().Warn("SCANNER_PS_QUIET ignored (want 05:00-07:00,16:00-20:00 or none)", "value", q)
		}
	}
	if v, err := strconv.ParseFloat(os.Getenv("SCANNER_PS_MIN_GAP"), 64); err == nil && v >= 0 {
		core.PsMinGapPct = v
	}
	seedScans(ctx, powerSetupSeedKey, powerSetupScans(), db.GetMeta, db.SetMeta, defs, db.ScanRepo().Save, scn.Upsert)
	seedScans(ctx, ignitionSeedKey, ignitionScans(), db.GetMeta, db.SetMeta, defs, db.ScanRepo().Save, scn.Upsert)
	seedScans(ctx, volBreakSeedKey, volBreakScans(), db.GetMeta, db.SetMeta, defs, db.ScanRepo().Save, scn.Upsert)
	seedScans(ctx, emaVwapBullSeedKey, emaVwapBullScans(), db.GetMeta, db.SetMeta, defs, db.ScanRepo().Save, scn.Upsert)
	seedScans(ctx, emaVwapBearSeedKey, emaVwapBearScans(), db.GetMeta, db.SetMeta, defs, db.ScanRepo().Save, scn.Upsert)
	seedScans(ctx, pmNewHighSeedKey, pmNewHighScans(), db.GetMeta, db.SetMeta, defs, db.ScanRepo().Save, scn.Upsert)
	seedScans(ctx, pdlBreakSeedKey, pdlBreakScans(), db.GetMeta, db.SetMeta, defs, db.ScanRepo().Save, scn.Upsert)
	seedScans(ctx, dailyBreakSeedKey, dailyBreakScans(), db.GetMeta, db.SetMeta, defs, db.ScanRepo().Save, scn.Upsert)
	seedScans(ctx, epWatchSeedKey, epWatchScans(), db.GetMeta, db.SetMeta, defs, db.ScanRepo().Save, scn.Upsert)
	seedScans(ctx, epBreakSeedKey, epBreakScans(), db.GetMeta, db.SetMeta, defs, db.ScanRepo().Save, scn.Upsert)
	migrateEmaVwapVolFloor(ctx, db.GetMeta, db.SetMeta, db.ScanRepo().List, db.ScanRepo().Save, scn.Upsert)
	migrateIgnitionPass(ctx, db.GetMeta, db.SetMeta, db.ScanRepo().List, db.ScanRepo().Save, scn.Upsert)
	migratePowerSetupAplus(ctx, db.GetMeta, db.SetMeta, db.ScanRepo().List, db.ScanRepo().Save, scn.Upsert)

	// Restore today's per-scan recurrence counters from the archived entries,
	// so the "hits" column survives restarts instead of resetting to 1s.
	go func() {
		et, lerr := time.LoadLocation("America/New_York")
		if lerr != nil {
			et = time.UTC
		}
		now := time.Now().In(et)
		dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, et)
		counts, cerr := db.NewsArchive().EntryHitsMax(ctx, dayStart.UnixMilli())
		if cerr != nil {
			logx.L().Warn("hits seed from archive failed", "err", cerr)
			return
		}
		if len(counts) > 0 {
			scn.SeedHits(dayStart.Format("2006-01-02"), counts)
		}
	}()

	// Frozen-slot backfill: when this process missed a session boundary (it
	// started mid-premarket or after 09:30), pull full-day minute bars for the
	// top ~300 snapshot-ranked movers (union the scans' active symbols) and
	// seed the frozen pre*/rth* review fields via eng.SeedFrozen — so the
	// "Pre-Market Movers" / "PM Gap & Fade" review scans work on a late start
	// too. Capped, paced, once per ET day; SCANNER_PREFILL_CAP=0 disables.
	pfw := prefill.New(prefill.Config{
		Seeds:    fc.Snapshot,
		Bars:     fc.Intraday,
		Active:   scn.ActiveSyms,
		Apply:    eng.SeedFrozen,
		SeedVwap: eng.SeedScopeVwap,
		Extra:    eng.EmvCandidates,
		AppStart: time.Now(),
	})

	sess := sessions.New(eng)

	// Previous trading day's after-hours (16:00-20:00 ET) highs for the
	// "broke yesterday's AH high" scans: fetched per symbol from Massive for
	// the ranked active names (prefill's REST-budget rules), topped up during
	// the day, re-seeded after each 04:00 reset. SCANNER_PREVAH_CAP=0 disables.
	pah := prevah.New(prevah.Config{
		Seeds:   fc.Snapshot,
		Active:  scn.ActiveSyms,
		Keep:    uni.Contains,
		BarsOn:  fc.IntradayOn,
		Apply:   eng.SetPrevAhHigh,
		Session: sess.Current,
	})
	ing := ingest.New(fc, eng, uni)
	ing.SetOnConnect(pfw.Reseed) // session VWAP + 1m EMA re-seed (vwapseed.go)
	np := news.New(fc, news.WithIntervals(cfg.NewsPoll, cfg.NewsRefPoll))
	sch := schedule.New(db.BaselineRepo(), db.FloatRepo(), db.CalendarRepo(), eng, fc, uni)

	// RVOL bootstrap: rebuild the baseline window's past days from Massive
	// minute bars for the scans' symbols and the snapshot movers, so RVOL
	// works without first recording 5 sessions live. Each (symbol, day) is
	// fetched once; SCANNER_RVOLFILL_CAP=0 disables.
	rvf := rvolfill.New(rvolfill.Config{
		Seeds:   fc.Snapshot,
		Active:  scn.ActiveSyms,
		Keep:    uni.Contains,
		Bars:    fc.IntradayBetween,
		Days:    sch.BaselineDays,
		Store:   db,
		Refresh: sch.RefreshBaselineFor,
	})

	// Daily-chart trend: nightly grouped-bars refresh -> classification ->
	// scan rows (dtrend/chg20d columns + filters) and the symbol panel chip.
	var trendMap atomic.Pointer[map[string]core.TrendRef]
	sch.EnableDailyTrend(db.TrendRepo(), fc, func(m map[string]core.TrendRef) {
		trendMap.Store(&m)
		scn.SetDailyTrend(m)
	})

	// Bull-flag / tight-consolidation setups: same nightly pass, same bars, no
	// extra fetch. No atomic.Pointer sibling to trendMap here — the scan rows
	// (flagScore/flagState/... columns + filters) are the only consumer, where
	// trend additionally backs the symbol panel's chip through TrendFor below.
	sch.EnableDailyFlags(func(m map[string]core.FlagRef) {
		scn.SetDailyFlags(m)
	})

	// IBKR short availability: one 15-minute file covers the whole US market,
	// so the scanner gets borrow data for every symbol without spending a
	// market-data line per ticker.
	shortStore := shortable.New(shortable.Config{}, func(m map[string]core.ShortRef) {
		scn.SetShortable(m)
	})

	// TWS link: pushes the selected ticker into Interactive Brokers via the
	// Display Groups API (the colored chain-link groups). Enabled by default;
	// harmlessly retries in the background when TWS isn't running.
	tws := twslink.New(twslink.Config{
		Enabled:  os.Getenv("SCANNER_TWS") != "0",
		Addr:     os.Getenv("SCANNER_TWS_ADDR"),
		ClientID: envInt("SCANNER_TWS_CLIENT_ID", 0),
		Group:    envInt("SCANNER_TWS_GROUP", 0),
		// Exact live borrow for the selected symbol. One market-data line out
		// of IBKR's 100 (shared with the TWS GUI), so it's opt-out.
		LiveShort: os.Getenv("SCANNER_TWS_LIVE_SHORT") != "0",
		// Real Level 2 depth for the selected symbol. Draws from the same
		// scarce market-data line pool as LiveShort, so it's single-target
		// the same way; opt-out.
		LiveDepth: os.Getenv("SCANNER_TWS_LIVE_DEPTH") != "0",
	})

	// Claude summarizer (auto-picks Anthropic API creds or the local claude CLI).
	// This is the manual "✨ Summarize"/"✨ Ask Claude" path and the tape/setup
	// readers — always Claude, unaffected by SCANNER_AUTOSUM_ENGINE below.
	smz := summarize.New()
	logx.L().Info("summarizer ready", "backend", smz.Backend(), "model", smz.Model(), "effort", smz.Effort())

	// Auto-summarizer engine: "local" (default) is a free extractive/
	// rule-based read that makes zero Claude API calls; "claude" keeps the
	// original Claude-backed auto-summaries. Mirrors the tape reader's
	// Local/Claude toggle pattern (internal/tape's always-on free local read
	// + the per-symbol Claude brain-button) — Claude is opt-in, not removed.
	// SCANNER_AUTOSUM_ENGINE=local|claude.
	autosumEngine := strings.ToLower(strings.TrimSpace(os.Getenv("SCANNER_AUTOSUM_ENGINE")))
	if autosumEngine == "" {
		autosumEngine = "local"
	}
	var autosumSum autosum.Summarizer
	// MaxPerSym/MaxPerDay/Cooldown exist ONLY to cap Claude API spend. The
	// local engine spends nothing, so those caps no longer apply for cost
	// reasons — but a generous, still-finite default stays as a basic
	// self-protection floor against a pathological entry-thrash loop, not a
	// literal unbounded allowance. SCANNER_AUTOSUM_MAX/_DAILY (below) can
	// still override either default explicitly.
	autosumMaxPerSym, autosumMaxPerDay, autosumCooldown := 50, 2000, time.Minute
	switch autosumEngine {
	case "claude":
		autosumSum = smz
		// Unchanged from before this build: real Claude API spend containment.
		autosumMaxPerSym, autosumMaxPerDay, autosumCooldown = 3, 60, 10*time.Minute
	default:
		autosumEngine = "local"
		autosumSum = summarize.NewLocal()
	}
	logx.L().Info("autosum engine selected", "engine", autosumEngine)

	// News archive: every accepted article (any source) and every scan entry
	// goes to SQLite through one async writer, so the feed paths never touch
	// disk. received_ms - published_ms per row is the feed-latency record;
	// joining scan_entries answers "did the headline beat the entry?".
	archive := db.NewsArchive()
	type articleBatch struct {
		items  []core.NewsItem
		bodies map[string]string
		via    string
		recvMs int64
	}
	archC := make(chan articleBatch, 256)
	entriesForDB := make(chan core.ScanEntry, 256)
	np.SetSink(func(items []core.NewsItem, bodies map[string]string, via string) {
		select {
		case archC <- articleBatch{items, bodies, via, time.Now().UnixMilli()}:
		default: // writer behind; losing an archive row beats stalling the feed
		}
	})
	go func() { // single archive writer (SQLite likes one writer anyway)
		for {
			select {
			case b := <-archC:
				if err := archive.SaveArticles(ctx, b.items, b.bodies, b.via, b.recvMs); err != nil {
					logx.L().Warn("news archive write failed", "err", err)
				}
			case e := <-entriesForDB:
				if err := archive.SaveEntry(ctx, e); err != nil {
					logx.L().Warn("scan entry archive write failed", "err", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { // retention sweep: on boot, then every 12h
		keep := 30 * 24 * time.Hour
		if d, err := strconv.Atoi(os.Getenv("SCANNER_NEWS_KEEP_DAYS")); err == nil && d > 0 {
			keep = time.Duration(d) * 24 * time.Hour
		}
		tk := time.NewTicker(12 * time.Hour)
		defer tk.Stop()
		for {
			if err := archive.PruneNews(ctx, time.Now().Add(-keep).UnixMilli()); err != nil {
				logx.L().Warn("news archive prune failed", "err", err)
			}
			select {
			case <-tk.C:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Auto-summarizer: scan entries -> day-news fetch -> Claude -> broadcast.
	entriesForSum := make(chan core.ScanEntry, 256)
	entriesForUI := make(chan core.ScanEntry, 256)
	entriesForFlag := make(chan core.ScanEntry, 256)
	go func() { // tee scanner entries to the two workers, the activity log, and the archive
		for e := range scn.Entries() {
			select {
			case entriesForSum <- e:
			default:
			}
			select {
			case entriesForUI <- e:
			default:
			}
			select {
			case entriesForDB <- e:
			default:
			}
			select {
			case entriesForFlag <- e:
			default:
			}
		}
	}()
	asw := autosum.New(autosum.Config{
		Entries: entriesForSum, Feed: fc, News: np, Sum: autosumSum, Cache: db.SummaryRepo(),
		MaxPerSym: envInt("SCANNER_AUTOSUM_MAX", autosumMaxPerSym),
		MaxPerDay: envInt("SCANNER_AUTOSUM_DAILY", autosumMaxPerDay),
		Cooldown:  autosumCooldown,
	})

	// Claude setup reads: a symbol entering a scan that FILTERS ON the nightly
	// bull-flag fields earns one scored, explained read of that setup — cached
	// on (sym, nightly asOf), persisted, and broadcast to every window. The
	// deterministic pattern pass already filtered the whole market for free, so
	// Claude only ever sees that shortlist; internal/flagread owns the gate,
	// the cooldown and the daily budgets. SCANNER_FLAGREAD=0 disables it.
	fread := flagread.New(flagread.Config{
		Entries:   entriesForFlag,
		RowFor:    scn.Row,
		UsesFlags: scn.ScanUsesFlags,
		FlagFor:   scn.FlagFor,
		BarsFor: func(ctx context.Context, sym string) ([]core.DailyBar, error) {
			since := time.Now().AddDate(0, 0, -flagBarLookbackDays).Format("2006-01-02")
			return db.TrendRepo().BarsFor(ctx, sym, since)
		},
		Reader: smz,
		Cache:  db.SetupArchive(),
	})

	// Merge news batches (poller + autosum per-symbol fetches): stamp News/
	// NewsAge onto scanner rows, then forward to the server for the panel.
	newsC := make(chan []core.NewsItem, 16)
	forward := func(items []core.NewsItem) {
		scn.MarkNews(items)
		select {
		case newsC <- items:
		default: // server behind; panel is cosmetic, drop
		}
	}
	go func() {
		for items := range np.Out() {
			forward(items)
		}
	}()
	go func() {
		for items := range asw.News() {
			forward(items)
		}
	}()

	// Source health: tracks each optional/critical data source's live
	// ok/failing/off state for the UI status bar, so a source silently
	// rejecting its saved token (observed: RTPR's "Invalid or inactive API
	// key" on every single poll) is visible there instead of invisible.
	// Every instrumented source below takes this as an optional
	// core.HealthReporter and guards every call with a nil check, so leaving
	// a source unwired (nil) is always a safe no-op.
	hl := health.New()

	// Benzinga's own push WebSocket (separate product from the Massive add-on;
	// needs a Benzinga-issued token). When enabled it beats the REST poll by
	// the whole index lag; the poll keeps running as dedupe-shared fallback.
	var bzs *bzstream.Stream
	if tok := config.BenzingaWSToken(); tok != "" {
		bzs = bzstream.New(bzstream.Config{Token: tok, Health: hl})
		go func() {
			for b := range bzs.Out() {
				// AddExternal shares the poller's dedupe/cache, so a story the
				// poll already saw is dropped here and vice versa.
				if fresh := np.AddExternal(b.Items, b.Bodies, "push"); len(fresh) > 0 {
					forward(fresh)
				}
			}
		}()
		logx.L().Info("benzinga push websocket enabled (token from env or ~/.marketscanner/bztoken)")
	} else {
		hl.Off("bzpush")
	}

	// RTPR: direct wire coverage (Business Wire / PR Newswire / GlobeNewswire /
	// AccessWire). Catches company press releases minutes before Benzinga's
	// own write-up of the same release (observed: GLMD, 9 minutes). Same
	// pattern as the push stream: shared dedupe via AddExternal, so whichever
	// source sees a story first wins.
	var rtp *rtpr.Poller
	var rtpAlerts *rtpr.Alerts
	newsScores := make(chan core.NewsScoreMsg, 64)
	if tok := config.RTPRToken(); tok != "" {
		// Priority: during a burst of simultaneous releases, fetch tickers
		// under any enabled scan's price ceiling first (e.g. a sub-$20 "speed
		// is key" scan) instead of pure publish order, using price data the
		// scanner already has live — no extra latency, no RTPR-side rule
		// needed (their rule DSL has no price field at all).
		rtp = rtpr.New(rtpr.Config{Token: tok, Priority: scn.IsPriority, Health: hl})
		go func() {
			for b := range rtp.Out() {
				if fresh := np.AddExternal(b.Items, b.Bodies, "rtpr"); len(fresh) > 0 {
					forward(fresh)
				}
			}
		}()
		logx.L().Info("rtpr wire poller enabled (token from env or ~/.marketscanner/rtprtoken)")

		// RTPR Impact Score (Beta): the account must separately enable "Alert
		// me on high-impact PRs" + WebSocket delivery in RTPR's Connect tab —
		// this consumer just stays quiet until then. Scores attach to an
		// article that's (almost always) already archived by the 3s ref poll;
		// the rare miss is fetched directly via the score's own signed link.
		rtpAlerts = rtpr.NewAlerts(rtpr.AlertsConfig{Token: tok, Health: hl})
		go func() {
			for ev := range rtpAlerts.Out() {
				ok, err := archive.SetArticleScore(ctx, ev.ArticleID, ev.Score, ev.Tier, ev.EventType, ev.Direction)
				if err != nil {
					logx.L().Warn("rtpr score attach failed", "err", err)
					continue
				}
				if !ok {
					// Not archived yet: fetch it via the score's own signed
					// permalink (authorized for exactly this during its hot
					// window), then forward it as a fresh article WITH the
					// score already attached, so it never appears unscored.
					b, fok := rtp.FetchOne(ctx, ev.URL)
					if !fok {
						continue
					}
					for i := range b.Items {
						if b.Items[i].ID != ev.ArticleID {
							continue
						}
						n := ev.Score
						b.Items[i].Score, b.Items[i].ScoreTier = &n, ev.Tier
						b.Items[i].EventType, b.Items[i].Direction = ev.EventType, ev.Direction
					}
					if fresh := np.AddExternal(b.Items, b.Bodies, "rtpr"); len(fresh) > 0 {
						forward(fresh)
					}
					continue
				}
				select {
				case newsScores <- core.NewsScoreMsg{
					ArticleID: ev.ArticleID, Sym: ev.Ticker, Score: ev.Score,
					Tier: ev.Tier, EventType: ev.EventType, Direction: ev.Direction,
				}:
				default: // server behind; a missed live update is cosmetic (the row is persisted)
				}
			}
		}()
	} else {
		hl.Off("rtpr")
		hl.Off("rtprAlerts")
	}

	// SEC EDGAR 13G/13G-A watcher: catches ownership-stake disclosures that
	// almost never get a wire press release (they're filed by the acquiring
	// investor, not the company), so Benzinga/RTPR never see them. Public
	// and free, no token required, so this always runs.
	e13g := edgar.New()
	// Always on, no token — reports under "edgar" for both this watcher and
	// eAll below (they share e13g's plumbing, see edgar.Watcher.SetHealth).
	e13g.SetHealth(hl)
	go func() {
		for b := range e13g.Out() {
			if fresh := np.AddExternal(b.Items, b.Bodies, "sec13g"); len(fresh) > 0 {
				forward(fresh)
			}
		}
	}()

	// SEC EDGAR all-filings watcher: the same current-events feed with NO
	// form-type filter (8-K, S-1/424B offerings, Form 4, delistings, ...),
	// gated to the symbols today's scans have actually surfaced — an 8-K on a
	// random large-cap is noise, the same 8-K on a ticker that just hit a
	// momentum scan is exactly the catalyst hunt. Shares the 13G watcher's
	// fair-access HTTP client and CIK→ticker map; SC 13G/13G-A stay with the
	// specialized watcher above (richer headline). SCANNER_EDGAR_ALL=0
	// disables it.
	eAll := edgar.NewAll(e13g, edgar.AllConfig{SymsFilter: scn.ActiveSyms})
	go func() {
		for b := range eAll.Out() {
			if fresh := np.AddExternal(b.Items, b.Bodies, "sec"); len(fresh) > 0 {
				forward(fresh)
			}
		}
	}()

	// tapeContext gathers the price levels + catalyst that make a tape read
	// specific ("sellers defending VWAP") instead of generic.
	tapeContext := func(ctx context.Context, sym string) summarize.TapeContext {
		tc := summarize.TapeContext{Sym: sym, Digest: tp.Digest(sym)}
		if row, ok := scn.Row(sym); ok {
			tc.Last, tc.VWAP, tc.ChgDay = row.Last, row.Vwap, row.ChgDay
			tc.HOD, tc.LOD = row.Hod, row.Lod
		}
		// Newest summarized article for this symbol today, if any.
		for _, it := range np.ItemsFor(sym, time.Now().Add(-24*time.Hour).UnixMilli()) {
			if sum, _, ok, _ := db.SummaryRepo().GetSummary(ctx, it.ID); ok {
				tc.NewsBrief = sum
				break
			}
		}
		return tc
	}
	tapeRead := func(ctx context.Context, sym string) (string, error) {
		return smz.ReadTape(ctx, tapeContext(ctx, sym))
	}

	// Tee tape stats: one copy drives the UI gauge, one drives the auto-reader.
	tapeStatsUI := make(chan core.TapeStat, 4)
	tapeReadouts := make(chan core.TapeReadoutMsg, 8)
	go runTapeAutoRead(ctx, tp.Stats(), tapeStatsUI, tapeReadouts, tapeRead)

	// Event-driven regime detection: every locally detected regime shift (and
	// periodic live-commentary heartbeat) becomes an instant free local-verdict
	// alert plus a (possibly Claude-upgraded) tape card, broadcast without
	// waiting on a manual "read tape" click. This supersedes runTapeAutoRead's
	// old role of producing tape cards; the manual tapeRead/TTapeRead flow above
	// is untouched.
	tapeRegimes := make(chan core.TapeRegimeMsg, 16)
	tapeCards := make(chan core.TapeCardMsg, 16)
	go runTapeRegimeOrchestrator(ctx, tp.RegimeEvents(), tapeRegimes, tapeCards,
		smz.ReadTapeControl,
		func(sym string) (vwap, hod, lod float64, ok bool) {
			row, ok := scn.Row(sym)
			if !ok {
				return 0, 0, 0, false
			}
			return row.Vwap, row.Hod, row.Lod, true
		},
		db.TapeArchive().SaveTapeRead,
		tp.NoteReadComplete,
	)

	// Live chart: per-second aggregates for the selected symbol, converted to
	// wire bars. Uses the A.* stream everyone already has, so the chart stays
	// live even on plans without the real-time trades needed for the tape.
	chartBars := make(chan core.ChartBarMsg, 64)

	// Order Ticket (cmd/scanner/orderticket.go): its tracked symbols ride the
	// chart tap alongside the windows' chart symbols.
	chartUnion := &chartSymUnion{set: ing.SetChartSyms}
	var oticket *orderticket.Ticket
	var psFlagLog atomic.Pointer[flaglog.Recorder] // set below once the Signal history exists
	if orderTicketEnabled() {
		t, oerr := orderticket.New(orderticket.Config{
			Dir:       orderTicketDir(cfg.DBPath),
			GetMeta:   db.GetMeta,
			SetMeta:   db.SetMeta,
			Group:     envInt("SCANNER_TWS_GROUP", 0),
			Seed:      fc.IntradayWithPrevDay,
			OnTracked: chartUnion.SetTicket,
			// the scanner's IBKR short-stock store (no fetch of its own)
			Shortable: func(sym string) (core.ShortRef, bool, int64) {
				r, ok := shortStore.Map()[sym]
				return r, ok, shortStore.AsOf()
			},
			// the A+ grade of the symbol's latest Power Setup (1.5x risk in the pane)
			PowerSetup: func(sym string) (bool, float64, int64, bool) {
				if fl := psFlagLog.Load(); fl != nil {
					return fl.LatestPowerSetup(sym)
				}
				return false, 0, 0, false
			},
		})
		if oerr != nil {
			logx.L().Warn("order ticket unavailable", "err", oerr)
		} else {
			oticket = t
		}
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case a := <-ing.ChartAggs():
				if oticket != nil {
					oticket.FeedAgg(a)
				}
				select {
				case chartBars <- core.ChartBarMsg{
					Sym: a.Sym, T: a.StartMs, O: a.Open, H: a.High, L: a.Low, C: a.Close,
					V: a.Vol, VWAP: a.DayVWAP,
				}:
				default:
				}
			}
		}
	}()

	// Real Level 2 depth (internal/twslink, single-target): a fresh book
	// snapshot sets it, a TWS-reported error/close clears it with the reason
	// -- SetDepth/SetDepthUnavailable are both no-ops once tp's depth target
	// has already moved on to a different symbol.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case d := <-tws.Depth():
				if d.Note != "" {
					tp.SetDepthUnavailable(d.Sym, d.Note)
				} else {
					tp.SetDepth(d.Sym, d.Bids, d.Asks)
				}
			}
		}
	}()

	status := func() core.StatusMsg {
		link := "off"
		if os.Getenv("SCANNER_TWS") != "0" {
			if up, _ := tws.Status(); up {
				link = "linked"
			} else {
				link = "waiting"
			}
		}
		// The Massive feed has no dedicated instrumentation call site (unlike
		// the sources above) — ing.State() already reports it once a second,
		// so this cheaply derives the same health shape from it here rather
		// than threading a HealthReporter through internal/ingest.
		if ing.State() == "live" {
			hl.SourceOK(feedName)
		} else {
			hl.SourceFail(feedName, ing.State())
		}
		return core.StatusMsg{
			Session:  sess.Current().String(),
			Feed:     ing.State(),
			Tracked:  eng.Tracked(),
			MsgRate:  ing.Rate(),
			Baseline: sch.BaselineStatus(),
			HasKey:   fc.HasKey(),
			TwsLink:  link,
			ClockMs:  time.Now().UnixMilli(),
			Counts:   scn.Counts(),
			Sources:  hl.Snapshot(),
		}
	}

	// setKey is invoked from the UI settings panel: persist the key, apply it to
	// the feed (which auto-recovers), and re-narrow the universe.
	setKey := func(_ context.Context, key string) error {
		key = strings.TrimSpace(key)
		if key == "" {
			return errors.New("empty API key")
		}
		if feedName == "alpaca" {
			// Never write the Alpaca pair into Massive's apikey file.
			return errors.New("the Alpaca feed reads its keys from ~/.marketscanner/alpaca.json; edit that file and restart")
		}
		if err := config.SaveKey(key); err != nil {
			return err
		}
		fc.SetKey(key)
		logx.L().Info("api key updated via UI")
		go func() {
			if err := universe.Refresh(context.Background(), fc, uni); err == nil {
				eng.Retain(uni.Set())
				eng.SetSecTypes(uni.Types())
				logx.L().Info("universe loaded", "tickers", uni.Len())
			}
		}()
		return nil
	}

	// Today's symbol blacklist (internal/blacklist): alerts of a listed symbol
	// are silenced in the Signal window and flagged in the Day Log; kept in
	// schema_meta for the ET day, cleared at 00:00 ET.
	blist := blacklist.New(blacklist.Config{GetMeta: db.GetMeta, SetMeta: db.SetMeta})

	// Signal activity log of the 9EMA flag scan (internal/flaglog): records every
	// signal per ET day, persists it and serves it back to the Signal window
	// (flagevents.get / flagevents.add). Built HERE, ahead of the server, because
	// server.Config.FlagEvents is read at server.New; it is wired into the scan
	// and the live push in the e9flags block below. No recorder when the scan is
	// off (SCANNER_E9FLAGS=0): FlagEvents stays nil and the window shows nothing.
	var flagLog *flaglog.Recorder
	if emaFlagsEnabled() {
		flagLog = flaglog.New(db.FlagEventRepo(), nil)
		psFlagLog.Store(flagLog)
	}

	// Day Log (internal/daylog): every scan alert, per ET day, with its outcome
	// filled in later from 1-minute bars, plus an end-of-day gainers / losers
	// snapshot; stored forever and mirrored to <db dir>/daylog/YYYY-MM-DD.csv.
	// SCANNER_DAYLOG=0 builds none of it.
	var dayLog *daylog.Recorder
	if dayLogEnabled() {
		dlcfg := daylog.Config{
			Repo: db.DayLogRepo(),
			Dir:  dayLogDir(cfg.DBPath),
			Row:  scn.Row,
			ScanNames: func() map[string]string {
				m := map[string]string{}
				for _, d := range scn.List() {
					m[d.ID] = d.Name
				}
				return m
			},
			News: func(ctx context.Context, sym string) ([]core.NewsItem, error) {
				return db.NewsArchive().ArticlesFor(ctx, sym, 50)
			},
			Note:      signalNoteFor(flagLog),
			Bars:      fc.IntradayWithPrevDay, // the REST call the chart window and the flag scan already make
			TopMovers: scn.TopMovers,
		}
		dlcfg.Blacklisted = blist.Has
		dayLog = daylog.New(dlcfg)
	}

	srvCfg := server.Config{
		Addr:         cfg.Listen,
		Scanner:      scn,
		Scans:        db.ScanRepo(),
		News:         newsC,
		Status:       status,
		SetKey:       setKey,
		Entries:      entriesForUI,
		Summaries:    asw.Summaries(),
		TapeStats:    tapeStatsUI,
		TapePrints:   tp.Prints(),
		TapeReadouts: tapeReadouts,
		ChartBars:    chartBars,
		ShortLive:    tws.ShortLive(),
		NewsScores:   newsScores,
		// TapeWatchSet reconciles BOTH watched symbol sets, each the UNION of
		// every open window's selection (computed server-side across all
		// clients in one recompute, so the tape and charts-grid sources can
		// never clobber each other). tapeSyms drives the tape engine and the
		// per-symbol trade/quote subscriptions; chartSyms (tapeSyms ∪ every
		// charts-grid window's set) drives only the ingest chart tap — the
		// A.* aggregate stream already carries those bars, so the grid costs
		// no extra subscriptions. lastSelected is whichever window most
		// recently changed its TAPE selection, and drives the single shared
		// TWS display + IBKR borrow-check line — only one real TWS instance
		// exists, so it stays "last window wins" rather than multi-target.
		// AlertToTWS: a scan with "Send to TWS when it alerts" on moves the linked
		// TWS window to the alerting symbol -- the same display-group Show as a
		// selection above, no order, no depth-target or window-selection change.
		// The hub applies the user-pick / min-gap guards before calling it.
		AlertToTWS: func(sym string) { tws.Show(sym) },
		// Global "TWS follow" switch for the above (Signal window button).
		AlertTWSLoad:   func(ctx context.Context) (string, error) { return db.GetMeta(ctx, "alerttws.enabled.v1") },
		AlertTWSSave:   func(ctx context.Context, v string) error { return db.SetMeta(ctx, "alerttws.enabled.v1", v) },
		DildoParamsGet: dildoTune.Get,
		DildoParamsSet: dildoTune.Set,
		TapeWatchSet: func(tapeSyms, chartSyms []string, lastSelected string) {
			tp.SetSymbols(tapeSyms)
			fc.WatchTapeSet(tapeSyms)
			chartUnion.SetCharts(chartSyms)
			tws.Show(lastSelected) // "" (last window closed) is a no-op
			if oticket != nil {
				oticket.SetFallbackSymbol(lastSelected)
			}
			tp.SetDepthTarget(lastSelected) // real Level 2 follows the same target
		},
		TapeRead: tapeRead,
		// Charts-grid window: persisted watchlist + opaque grid-layout blob.
		// The layout lives in schema_meta as a string the server never parses
		// — the client owns its shape (key versioned for that reason).
		WatchlistLoad: db.WatchlistRepo().List,
		WatchlistSave: db.WatchlistRepo().Replace,
		GridLoad: func(ctx context.Context) (string, error) {
			return db.GetMeta(ctx, "chartgrid.v1")
		},
		GridSave: func(ctx context.Context, layout string) error {
			return db.SetMeta(ctx, "chartgrid.v1", layout)
		},
		// Past-session review window: persisted engine boundary snapshots,
		// served raw — the client derives every percentage (the after-hours
		// gain vs the SAME day's regular close, day change vs prev close).
		ReviewLoad:  db.SessionStatsRepo().Load,
		ReviewDates: db.SessionStatsRepo().Dates,
		// Event-driven regime alerts/cards (see runTapeRegimeOrchestrator above)
		// and the two toggles that drive it, straight through to the engine.
		TapeRegimes:       tapeRegimes,
		TapeCards:         tapeCards,
		TapeClaudeSet:     tp.SetClaudeEnabled,
		TapeCommentarySet: tp.SetCommentaryInterval,
		// Finished bull-flag setup reads (see flagread.New above).
		SetupReads: fread.Reads(),
		// Click-to-load news history: the panel's live list only reaches back
		// to UI load; the archive holds 30 days per ticker.
		NewsFor: func(ctx context.Context, sym string) ([]core.NewsItem, error) {
			return db.NewsArchive().ArticlesFor(ctx, sym, 50)
		},
		TrendFor: func(sym string) (core.TrendRef, bool) {
			if p := trendMap.Load(); p != nil {
				ref, ok := (*p)[sym]
				return ref, ok
			}
			return core.TrendRef{}, false
		},
		// RowFor backs the row.get request a pop-out tape window uses to get
		// header stats for its own symbol when it has no scan tiled.
		RowFor: scn.RowJSON,
		// Summarize backs the manual "✨ Summarize"/"✨ Ask Claude" button — it
		// ALWAYS means "get me a real Claude read", never the free local one
		// autosum may have already produced automatically for this article.
		// See newsSummarizeHandler's doc comment for the silent-Claude-
		// short-circuit fix this closure depends on.
		Summarize: newsSummarizeHandler(db.SummaryRepo(), np.Get, archive.Article, smz),
		Chart: func(ctx context.Context, sym string) ([]core.Bar, float64, error) {
			// IntradayWithPrevDay, not Intraday: the monitor/watchlist charts
			// (MonitorChart.tsx) want the previous trading day's bars alongside
			// today's for morning gap continuity. internal/prefill's Bars wiring
			// stays on fc.Intraday (today-only) — see intraday.go's doc comments.
			bars, err := fc.IntradayWithPrevDay(ctx, sym)
			if err != nil {
				return nil, 0, err
			}
			prev, _ := fc.PrevClose(ctx, sym) // best-effort; 0 → no prior-close line
			return bars, prev, nil
		},
		Fundamentals: func(ctx context.Context, sym string) (core.Fundamentals, error) {
			// SEC filings only change ~quarterly: serve a same-day cache hit
			// without re-fetching. Same shape as the news-summary cache.
			if f, ok, _ := db.FundamentalsRepo().GetFundamentals(ctx, sym); ok && time.Now().Unix()-f.AsOf < 24*3600 {
				return f, nil
			}
			f, err := fc.Fundamentals(ctx, sym)
			if err != nil {
				// A transient fetch failure still serves a stale cache hit
				// rather than an empty panel (not-entitled fails the same way
				// every time, so this only helps genuinely transient errors).
				if cached, ok, _ := db.FundamentalsRepo().GetFundamentals(ctx, sym); ok {
					return cached, nil
				}
				return core.Fundamentals{}, err
			}
			if serr := db.FundamentalsRepo().SaveFundamentals(ctx, f); serr != nil {
				logx.L().Warn("fundamentals cache save failed", "err", serr)
			}
			return f, nil
		},
	}
	if flagLog != nil {
		srvCfg.FlagEvents = flagLog.Day
	}
	if dayLog != nil {
		srvCfg.AlertHook = dayLog.OnAlert
		srvCfg.DayLog = dayLog.Get
	}
	if oticket != nil {
		srvCfg.OrderTicket = oticket.Handler()
	}
	srvCfg.BlacklistView = blist.View
	srvCfg.BlacklistChanged = blist.Changed()
	srvCfg.BlacklistSet = func(sym string, add bool) error {
		if add {
			return blist.Add(sym)
		}
		return blist.Remove(sym)
	}
	srv := server.New(srvCfg)

	// Lifecycle: run all components; a fatal component error cancels everything.
	var wg sync.WaitGroup
	start := func(name string, fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := fn(ctx); e != nil {
				logx.L().Error("component exited", "name", name, "err", e)
				cancel()
			}
		}()
	}
	start("engine", eng.Run)
	if dayLog != nil {
		start("daylog", dayLog.Run)
	}
	if oticket != nil {
		start("orderticket", oticket.Run)
	}
	start("scanner", scn.Run)
	// Intraday sibling of EnableDailyFlags above: engine session bars -> same
	// pattern geometry under intraday params -> the iflagScore/... columns.
	start("iflags", func(ctx context.Context) error {
		return runIntradayFlags(ctx, intradayFlagInterval, eng.SessionBars, scn.SetIntradayFlags)
	})
	// 9EMA-flag MARKET scan (internal/flagscan): every stock whose pre-market
	// volume reached 1x its 10-day average gets the Signal window's 1-minute
	// 9 EMA flag state machine run on it all day -- engine candidates ->
	// Signal-window-identical REST bars + the ingest flag tap -> verdicts
	// overlaid by the scanner as e9State..e9Age (see emaFlagConfig). Same
	// lifetime/ctx style as the intraday sibling above; SCANNER_E9FLAGS=0 off.
	if flagLog != nil {
		// Load today's persisted signals BEFORE the scan starts, so a restart's
		// replay of the morning is recognised as known rather than pushed as new.
		// A load failure is logged and the recorder carries on (empty today).
		if err := flagLog.Load(ctx); err != nil {
			logx.L().Warn("flaglog: loading today's signals failed; continuing", "err", err)
		}
		flagLog.SetPublish(srv.PublishFlagEvents)
		start("flaglog", flagLog.Run)
		// The engine's GREEN DILDO / VOLUME CLIMAX / BOTTOM / CLIMAX FAILED
		// alerts (nil hand-off, so returns at once, when both judges are off);
		// alerts the engine dropped on a full hand-off are logged as they rise.
		start("flaglog-alerts", func(ctx context.Context) error {
			return flagLog.RunSignals(ctx, eng.SignalHits(), eng.SignalDrops)
		})
	}
	if emaFlagsEnabled() {
		start("e9flags", func(ctx context.Context) error {
			fcfg := emaFlagConfig(eng, fc, ing, scn)
			// Every evaluation and every dildo-rule call reaches the recorder
			// (cheap, never blocks): signallog.go.
			fcfg = wireSignalLog(fcfg, flagLog)
			return runEmaFlags(ctx, fcfg)
		})
	} else if dildoEnabled() {
		warnDildoAlert2Off()
	}
	start("sessions", sess.Run)
	start("ingest", ing.Run)
	start("news", np.Run)
	if bzs != nil {
		start("bzstream", bzs.Run)
	}
	if rtp != nil {
		start("rtpr", rtp.Run)
	}
	if rtpAlerts != nil {
		start("rtpr-alerts", rtpAlerts.Run)
	}
	start("edgar13g", e13g.Run)
	start("edgar-all", eAll.Run)
	start("autosum", asw.Run)
	start("flagread", fread.Run)
	start("tape", tp.Run)
	start("twslink", tws.Run)
	start("shortable", shortStore.Run)
	start("prefill", pfw.Run)
	start("prevah", pah.Run)
	start("rvolfill", rvf.Run)
	start("dlevels", func(ctx context.Context) error {
		tr := db.TrendRepo()
		return runDailyLevels(ctx, tr.MaxDate, tr.LoadBars, eng.SetDailyLevels, eng.SetPrevLows, time.Now)
	})
	if epEnabled() {
		start("episodic-pivot", func(ctx context.Context) error {
			return runEP(ctx, epSource{
				Candidates: scn.EPCandidates,
				Daily:      fc.DailyRange,
				StoredBars: db.TrendRepo().BarsFor,
				Articles:   db.NewsArchive().ArticlesFor,
				MarkNews:   scn.MarkNews,
				Publish:    scn.SetEP,
				Now:        time.Now,
			})
		})
	}
	start("adv10", func(ctx context.Context) error {
		return runAdv10(ctx, db.AvgDailyVolume, eng.SetAdv10, time.Now)
	})
	// Session-stats writer: the engine's once-per-boundary session snapshots
	// (09:30 pre, 16:00 regular, 20:00 post) land in SQLite for the morning
	// review window. The engine's handoff is non-blocking by contract (it
	// drops and counts if this writer wedges), so this goroutine owns ALL the
	// disk latency — and the drop logging the engine itself cannot do.
	start("sessionstats", func(ctx context.Context) error {
		return runSessionStatsWriter(ctx, eng.SessionSnapshots(), db.SessionStatsRepo(), eng.SessionSnapshotDrops)
	})
	start("schedule", sch.Run)
	start("server", srv.Run)

	if cfg.Tray {
		// A tray failure must never take the scanner down: log it and keep
		// running headless (start() would cancel everything on a non-nil error).
		start("tray", func(ctx context.Context) error {
			if terr := tray.Run(ctx, tray.Options{
				Tooltip:   "Live Market Scanner",
				Icon:      assets.Icon,
				OnDefault: func() { openUI(cfg) },
				Items: []tray.Item{
					{Label: "Open scanner", Default: true, OnClick: func() { openUI(cfg) }},
					{Label: "Charts window", OnClick: func() { openCharts(cfg) }},
					{Label: "Open log file", OnClick: func() { openLogFile(cfg.LogFile) }},
					{},
					{Label: "Quit Live Market Scanner", OnClick: cancel},
				},
				OnSessionEnd: cancel,
			}); terr != nil {
				logx.L().Error("tray failed; continuing without it", "err", terr)
			}
			return nil
		})
	}

	logx.L().Info("started", "addr", cfg.Listen, "db", cfg.DBPath, "log", cfg.LogFile)
	if cfg.OpenBrowser {
		go waitAndOpenUI(ctx, cfg)
	}

	<-ctx.Done()
	logx.L().Info("shutting down")
	if cfg.Tray || cfg.OpenBrowser {
		closeShellWindows()
	}
	wg.Wait()
	return nil
}

// claudeSummarizer is the minimal shape newsSummarizeHandler needs from the
// Claude service (satisfied by *summarize.Service). Declaring it as an
// interface here — rather than taking *summarize.Service directly — lets
// tests inject a fake that records whether it was called without dialing
// Claude, so the short-circuit logic below can be unit tested with no
// network access or API credentials.
type claudeSummarizer interface {
	Summarize(ctx context.Context, a summarize.Article) (string, error)
	Model() string
}

// newsSummarizeHandler builds the manual "✨ Summarize"/"✨ Ask Claude" button's
// handler. A click on this button ALWAYS means "get me a real Claude read",
// never the free local one autosum may already have produced automatically
// for this article.
//
// Silent-Claude-short-circuit fix: a cache hit only short-circuits (skips
// calling Claude) when the cached entry's model is a REAL Claude model — i.e.
// NOT summarize.LocalModel. Before this check existed, autosum writing local
// summaries into this same cache meant a click here on an article that
// already had an auto-generated LOCAL summary would silently return that
// local text and never call Claude, defeating "Claude stays opt-in"
// invisibly. A cached model=="local" entry instead falls through, calls
// Claude, and OVERWRITES the cache entry with the real result.
//
// getLive looks up a still-memory-cached article (np.Get's shape);
// getArchived falls back to the on-disk archive (archive.Article's shape)
// once an article has aged out of memory.
func newsSummarizeHandler(
	cache core.SummaryRepo,
	getLive func(id string) (core.NewsItem, string, bool),
	getArchived func(ctx context.Context, id string) (core.NewsItem, string, bool, error),
	claude claudeSummarizer,
) func(ctx context.Context, id string) (text, model string, err error) {
	return func(ctx context.Context, id string) (text, model string, err error) {
		if sum, cachedModel, ok, _ := cache.GetSummary(ctx, id); ok && cachedModel != "" && cachedModel != summarize.LocalModel {
			return sum, cachedModel, nil
		}
		item, body, ok := getLive(id)
		if !ok {
			// Evicted from the ~800-item memory cache (3-4h of market-wide
			// news) — the archive keeps articles for weeks.
			if item, body, ok, err = getArchived(ctx, id); err != nil || !ok {
				return "", "", errors.New("article is no longer cached — refresh the panel")
			}
		}
		sym := ""
		if len(item.Tickers) > 0 {
			sym = item.Tickers[0]
		}
		sum, err := claude.Summarize(ctx, summarize.Article{
			ID: item.ID, Sym: sym, Tickers: item.Tickers, Headline: item.Headline,
			Source: item.Source, URL: item.URL, Body: body, Teaser: item.Teaser, TS: item.TS,
		})
		if err != nil {
			return "", "", err
		}
		if serr := cache.SaveSummary(ctx, id, sym, claude.Model(), sum); serr != nil {
			logx.L().Warn("summary cache save failed", "err", serr)
		}
		return sum, claude.Model(), nil
	}
}

// runTapeAutoRead forwards tape digests to the UI and, once a newly selected
// symbol has accumulated enough flow to say something meaningful, asks Claude
// for one read automatically — so looking at a stock gives you the tape verdict
// without clicking. Per-symbol cooldown keeps re-selecting the same ticker from
// re-spending tokens; SCANNER_TAPE_AUTOREAD=0 disables it.
func runTapeAutoRead(
	ctx context.Context,
	in <-chan core.TapeStat,
	uiOut chan<- core.TapeStat,
	readouts chan<- core.TapeReadoutMsg,
	read func(context.Context, string) (string, error),
) {
	const (
		warmupSecs  = 45 // enough tape to be worth interpreting
		minTrades   = 40 // ...and enough prints, on a quiet name
		cooldownDur = 10 * time.Minute
	)
	enabled := os.Getenv("SCANNER_TAPE_AUTOREAD") != "0"
	lastRead := map[string]time.Time{}
	// done signals the in-flight read finished; owning it in the select loop
	// keeps every piece of state single-writer (no mutex, no race).
	done := make(chan struct{}, 1)
	busy := false

	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			busy = false
		case st := <-in:
			select {
			case uiOut <- st:
			default: // UI behind; next digest supersedes
			}
			if !enabled || busy || st.Sym == "" || st.Err != "" {
				continue
			}
			if st.WatchSecs < warmupSecs || st.TotalTrades < minTrades {
				continue
			}
			if time.Since(lastRead[st.Sym]) < cooldownDur {
				continue
			}
			lastRead[st.Sym] = time.Now()
			busy = true
			go func(sym string) {
				defer func() { done <- struct{}{} }()
				rctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
				defer cancel()
				text, err := read(rctx, sym)
				if err != nil {
					logx.L().Warn("auto tape read failed", "sym", sym, "err", err)
					return // silent: the manual button still reports errors
				}
				select {
				case readouts <- core.TapeReadoutMsg{Sym: sym, Text: text, At: time.Now().UnixMilli()}:
				default:
				}
			}(st.Sym)
		}
	}
}

// runTapeRegimeOrchestrator consumes locally detected regime shifts (and
// periodic live-commentary "heartbeat" ticks) from the tape engine and turns
// each into: (1) an instant, free TapeRegimeMsg built straight from the
// event's local verdict — skipped for heartbeat events, which carry no
// headline news to alert on — and (2) a TapeCardMsg, either built directly
// from the local verdict (Claude off for this symbol) or produced by an
// asynchronous smz.ReadTapeControl call (Claude on), archived via saveRead and
// followed by noteReadComplete so the engine's heartbeat gate resets from read
// COMPLETION time. This is the event-driven successor to runTapeAutoRead's old
// role of producing tape cards; the separate manual "read tape" button
// (tapeRead / core.TTapeRead) is untouched and keeps working exactly as before.
//
// Per-symbol busy bookkeeping (not a single global flag, since multiple
// windows can watch different symbols concurrently) copies runTapeAutoRead's
// done-channel idiom: a symbol already awaiting a Claude read drops any
// further event for that same symbol — including a heartbeat that fired again
// before the in-flight read completed — rather than stacking concurrent calls.
func runTapeRegimeOrchestrator(
	ctx context.Context,
	events <-chan core.RegimeEvent,
	regimesOut chan<- core.TapeRegimeMsg,
	cardsOut chan<- core.TapeCardMsg,
	readControl func(ctx context.Context, snap core.TapeSnapshot, headline string) (summarize.ControlRead, error),
	rowVWAP func(sym string) (vwap, hod, lod float64, ok bool),
	saveRead func(ctx context.Context, event core.RegimeEvent, verdict core.TapeVerdict, whatsHappening, watchFor, model, errMsg string) error,
	noteReadComplete func(sym string, at time.Time),
) {
	busy := map[string]bool{}
	// done carries back the symbol whose in-flight read just finished; owning
	// it in the select loop keeps busy single-writer (no mutex, no race).
	done := make(chan string, 32)

	archive := func(ev core.RegimeEvent, verdict core.TapeVerdict, whatsHappening, watchFor, model, errMsg string) {
		if saveRead == nil {
			return
		}
		go func() {
			actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if err := saveRead(actx, ev, verdict, whatsHappening, watchFor, model, errMsg); err != nil {
				logx.L().Warn("tape read archive failed", "sym", ev.Sym, "err", err)
			}
		}()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case sym := <-done:
			busy[sym] = false
		case ev := <-events:
			if ev.Trigger != "heartbeat" {
				select {
				case regimesOut <- core.TapeRegimeMsg{
					Sym: ev.Sym, Ts: ev.Ts, Trigger: ev.Trigger, Headline: ev.Headline, Verdict: ev.Verdict,
				}:
				default: // UI behind; next event supersedes
				}
			}

			if !ev.ClaudeOn {
				select {
				case cardsOut <- core.TapeCardMsg{
					Sym: ev.Sym, Ts: ev.Ts, Trigger: ev.Trigger, Headline: ev.Headline,
					Model: "local", Control: ev.Verdict.Control, Confidence: ev.Verdict.Confidence,
					WhatsHappening: ev.Verdict.Text, Evidence: ev.Verdict.Evidence,
				}:
				default:
				}
				archive(ev, ev.Verdict, ev.Verdict.Text, "", "local", "")
				continue
			}

			if busy[ev.Sym] {
				continue // a read is already in flight for this symbol; drop rather than stack
			}
			busy[ev.Sym] = true
			go func(ev core.RegimeEvent) {
				defer func() {
					select {
					case done <- ev.Sym:
					default:
					}
				}()

				snap := ev.Snapshot
				if rowVWAP != nil {
					if vwap, hod, lod, ok := rowVWAP(ev.Sym); ok {
						snap.Vwap, snap.SessionHigh, snap.SessionLow = vwap, hod, lod
					}
				}

				rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 35*time.Second)
				defer cancel()
				read, err := readControl(rctx, snap, ev.Headline)

				model, whatsHappening, watchFor, errMsg := "local", ev.Verdict.Text, "", ""
				verdict := ev.Verdict
				if err != nil {
					// Backend-unavailable (no structured-output support, e.g. the
					// CLI backend) or a pick() failure: fall back to the local
					// verdict already computed for free, same as Claude-off,
					// rather than erroring the card out.
					if !errors.Is(err, summarize.ErrTapeControlUnavailable) {
						logx.L().Warn("tape control read unavailable; using local verdict", "sym", ev.Sym, "err", err)
					}
				} else {
					model, whatsHappening, watchFor, errMsg = read.Model, read.WhatsHappening, read.WatchFor, read.Err
					verdict = core.TapeVerdict{
						Control: read.Control, Confidence: read.Confidence,
						Text: read.WhatsHappening, Evidence: read.Evidence, Source: "api",
					}
				}

				select {
				case cardsOut <- core.TapeCardMsg{
					Sym: ev.Sym, Ts: ev.Ts, Trigger: ev.Trigger, Headline: ev.Headline,
					Model: model, Control: verdict.Control, Confidence: verdict.Confidence,
					WhatsHappening: whatsHappening, Evidence: verdict.Evidence, WatchFor: watchFor, Err: errMsg,
				}:
				default:
				}
				archive(ev, verdict, whatsHappening, watchFor, model, errMsg)
				if noteReadComplete != nil {
					noteReadComplete(ev.Sym, time.Now())
				}
			}(ev)
		}
	}
}

// runSessionStatsWriter drains the engine's boundary session snapshots into
// the session_stats table, pruning to the newest N dates after each save. It
// is the single consumer of the snapshot channel; the engine's handoff to it
// never blocks (a lost snapshot is a gap in history, a stalled engine is an
// outage), so this loop also reads the engine's drop counter and logs any
// losses — the engine imports only core and cannot log them itself.
// WithoutCancel on the save so a boundary snapshot arriving just before
// shutdown still persists (the scan.upsert Save pattern).
func runSessionStatsWriter(
	ctx context.Context,
	snaps <-chan core.SessionSnapshot,
	repo core.SessionStatsRepo,
	drops func() int64,
) error {
	var seenDrops int64
	for {
		select {
		case <-ctx.Done():
			return nil
		case snap := <-snaps:
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			err := repo.SaveSnapshot(sctx, snap)
			if err == nil {
				logx.L().Info("session stats saved",
					"date", snap.Date, "session", snap.Session.String(), "rows", len(snap.Rows))
				if _, perr := repo.PruneBefore(sctx, 0); perr != nil { // 0 = repo default (10 dates)
					logx.L().Warn("session stats prune failed", "err", perr)
				}
			} else {
				logx.L().Warn("session stats save failed",
					"date", snap.Date, "session", snap.Session.String(), "err", err)
			}
			cancel()
			if d := drops(); d > seenDrops {
				logx.L().Warn("session snapshots dropped (writer was behind)", "count", d-seenDrops, "total", d)
				seenDrops = d
			}
		}
	}
}

// flagBarLookbackDays bounds the per-symbol daily-bar query behind one setup
// read, in CALENDAR days. internal/schedule classifies against trendWindowDays
// (32 TRADING days) and prunes stored closes at 45 calendar days — both of
// those constants are unexported there, so rather than declare a second window
// this asks for the store's full retained history, which is exactly the span
// the nightly geometry was measured over. summarize.ReadSetup then keeps the
// newest 40 bars (its own poleMaxBars + maxBaseBars cap), so a wider query
// costs a slightly longer key-range scan and not a single extra token.
const flagBarLookbackDays = 45

// envInt reads a positive integer env var, falling back to def.
func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// flagScans are the pre-built pattern scans seeded ONCE per database (guarded
// by a schema_meta flag, unlike defaultScan's empty-repo check) so they appear
// ready-made for existing installations too — but deleting one doesn't
// resurrect it on the next restart. State >= 1 keeps a symbol visible through
// the coiling -> breakout transition (breakout would LEAVE a state == 1 scan
// the moment it triggers, which is exactly when it matters most); the cross
// alert on state above 1.5 fires exactly once at that flip.
func flagScans() []core.ScanDef {
	return []core.ScanDef{
		{
			ID: "flags-intraday", Name: "Intraday Flags", Enabled: true,
			Filter: core.Filter{Op: "and", Conds: []core.Filter{
				{Field: core.FieldIFlagState, Cmp: core.CmpGTE, Value: 1},
				{Field: core.FieldIFlagScore, Cmp: core.CmpGTE, Value: 60},
			}},
			Sort: []core.SortKey{
				{Field: core.FieldIFlagState, Dir: "desc"}, // breakouts above coils
				{Field: core.FieldIFlagScore, Dir: "desc"},
			},
			Limit: 50,
			Columns: []string{
				"last", "chg5m", "chgDay", "vol", "rvol",
				"iflagScore", "iflagState", "iflagRetrace", "iflagVolDry", "news",
			},
			Alert: core.AlertConfig{
				Enabled: true, On: core.AlertOnCross,
				Trigger:     &core.Filter{Field: core.FieldIFlagState, Cmp: core.CmpCrossAbove, Value: 1.5},
				Sound:       "alert",
				CooldownSec: 120,
			},
		},
		{
			ID: "flags-daily", Name: "Daily Flags", Enabled: true,
			Filter: core.Filter{Op: "and", Conds: []core.Filter{
				{Field: core.FieldFlagState, Cmp: core.CmpGTE, Value: 1},
				{Field: core.FieldFlagScore, Cmp: core.CmpGTE, Value: 60},
			}},
			Sort: []core.SortKey{
				{Field: core.FieldFlagState, Dir: "desc"},
				{Field: core.FieldFlagScore, Dir: "desc"},
			},
			Limit: 50,
			Columns: []string{
				"last", "chgDay", "vol", "rvol", "dtrend",
				"flagScore", "flagState", "flagRetrace", "flagVolDry", "news",
			},
			Alert: core.AlertConfig{
				Enabled: true, On: core.AlertOnCross,
				Trigger:     &core.Filter{Field: core.FieldFlagState, Cmp: core.CmpCrossAbove, Value: 1.5},
				Sound:       "chime",
				CooldownSec: 120,
			},
		},
	}
}

// sessReviewScans is the pre-built past-session review scan, seeded once per
// database under its own schema_meta key (independent of the flag-scan seed,
// so each batch retries on its own). It fills at 09:30 when the frozen pre*
// fields are stamped and stays reviewable all day — until then preChg is Null
// and matches nothing, so the scan is simply empty. The alert is DISABLED on
// purpose: a review list that fills with ~50 rows at the open is not an alert
// source, and firing 50 enter-sounds at 09:30 would be pure noise. A sound
// value is still set so the editor's sound dropdown renders sanely.
func sessReviewScans() []core.ScanDef {
	return []core.ScanDef{
		{
			ID: "premkt-movers", Name: "Pre-Market Movers", Enabled: true,
			Filter: core.Filter{Op: "and", Conds: []core.Filter{
				{Field: core.FieldPreChg, Cmp: core.CmpGTE, Value: 5},
				{Field: core.FieldPreVol, Cmp: core.CmpGTE, Value: 100000},
			}},
			Sort: []core.SortKey{
				{Field: core.FieldPreChg, Dir: "desc"},
				{Field: core.FieldPreVol, Dir: "desc"},
			},
			Limit:   50,
			Columns: []string{"last", "chgDay", "preChg", "preVol", "preHigh", "rvol", "news"},
			Alert:   core.AlertConfig{Enabled: false, On: core.AlertOnEnter, Sound: "ping", CooldownSec: 120},
		},
	}
}

// gapFadeScans is the pre-built "gap up pre-market, then fade" review scan,
// seeded once per database under its own schema_meta key (independent of the
// flag-scan and sessreview batches, so existing installs that already ran
// those still pick this one up). It targets the classic gap-fade open setup
// in mid/large-priced names: $25-250, a pre-market PEAK gain (preGap = session
// HIGH vs prev close, catching overnight gaps and mid-premarket news jumps
// alike) of at least 5%, at least 30% of that move given back off the high by
// 09:30 (preFade), and enough pre-market volume to be tradeable. Like the
// other review scans it fills at 09:30 when the frozen pre* fields are
// stamped — until then preGap/preFade are Null and match nothing — and the
// alert stays DISABLED for the same reason: a list that fills all at once at
// the open is not an alert source.
func gapFadeScans() []core.ScanDef {
	return []core.ScanDef{
		{
			ID: "pm-gap-fade", Name: "PM Gap & Fade", Enabled: true,
			Filter: core.Filter{Op: "and", Conds: []core.Filter{
				{Field: core.FieldLast, Cmp: core.CmpBetween, Value: []any{25, 250}},
				{Field: core.FieldPreGap, Cmp: core.CmpGTE, Value: 5},
				{Field: core.FieldPreFade, Cmp: core.CmpGTE, Value: 30},
				{Field: core.FieldPreVol, Cmp: core.CmpGTE, Value: 100000},
			}},
			Sort: []core.SortKey{
				{Field: core.FieldPreFade, Dir: "desc"},
				{Field: core.FieldPreGap, Dir: "desc"},
			},
			Limit:   50,
			Columns: []string{"last", "chgDay", "preGap", "preFade", "preChg", "preHigh", "preVol", "rvol", "news"},
			Alert:   core.AlertConfig{Enabled: false, On: core.AlertOnEnter, Sound: "ping", CooldownSec: 120},
		},
	}
}

// premarketVolScans seeds "Premarket Volume Leaders" (user: "a stock scanner
// that scans the following; Average Daily volume, Percentage of that volume
// traded in the premarket and the gap %"): a liquidity floor on avgVol (the
// trailing regular-session-only ADV) so illiquid names don't dominate, a
// meaningful-participation floor on preVolPctAdv (10%+ of a typical day's
// volume already traded before the open is a real unusual-interest signal),
// sorted by that same field so the most unusual names lead. chgDay (live,
// continuously updating vs previous close) is the "gap %" column — NOT
// preGap, which is frozen at the 09:30 boundary and would sit Null for the
// entire pre-market session this scan is meant to be watched during. Like
// the other review-style scans the alert stays DISABLED: this is a list to
// browse, not a single-symbol trigger.
func premarketVolScans() []core.ScanDef {
	return []core.ScanDef{
		{
			ID: "premkt-vol-leaders", Name: "Premarket Volume Leaders", Enabled: true,
			Filter: core.Filter{Op: "and", Conds: []core.Filter{
				{Field: core.FieldAvgVol, Cmp: core.CmpGTE, Value: 500000},
				{Field: core.FieldPreVolPctAdv, Cmp: core.CmpGTE, Value: 10},
			}},
			Sort:    []core.SortKey{{Field: core.FieldPreVolPctAdv, Dir: "desc"}},
			Limit:   50,
			Columns: []string{"last", "chgDay", "preVolPctAdv", "avgVol", "preVol", "preGap", "news"},
			Alert:   core.AlertConfig{Enabled: false, On: core.AlertOnEnter, Sound: "ping", CooldownSec: 120},
		},
	}
}

// Seeded 9EMA-flag scan IDs and the schema_meta key their batch is guarded by.
const (
	e9FlagSeedKey      = "seeded.e9flag.v1"
	e9FlagSidewaysID   = "e9flag-sideways"
	e9FlagReadyID      = "e9flag-ready"
	e9FlagBreakoutID   = "e9flag-breakout"
	e9FlagCooldownSec  = 300 // one alert per symbol per scan per five minutes
	e9FlagBreakoutAgeS = 300 // a BREAKOUT stays on the breakout scan for five minutes
)

// emaFlagScans are the three seeded scans of the 9EMA-flag market scan (the
// stages the trader already knows from the Signal window), seeded once per
// database under their own schema_meta key like every other batch and never
// resurrected after a deletion. Each one is "pre-market volume at least 1x the
// 10-day average" (pmVolX >= 1: the candidate gate, restated in the filter so
// the editor shows the whole rule and a user can loosen it) AND a stage of the
// flag state machine (e9State: 0 idle, 1 impulse, 2 flat, 3 ready, 4 long):
//
//   - "sideways" (e9State >= 2): everything holding a flag, FLAT through LONG,
//     strongest stage first then closest to its 9 EMA. >= 2, not == 2: a name
//     that goes READY or LONG must stay visible on this list (the same reason
//     the Intraday Flags scan filters state >= 1), and the ENTER alert fires
//     once, when a symbol first reaches a flag - the earliest of the three;
//   - "ready" (e9State == 3): the 9 EMA has caught up (within 4%), closest
//     first - the stage the trader acts on;
//   - "breakout" (e9State == 4 and e9Age <= 300): the flag high broke while
//     ready, shown for five minutes after the entry bar, freshest first, so
//     the list is "what just went" and not every position of the day.
//
// The alert is on ENTER with a 300 s cooldown and Speak on: the tone says a
// stage was reached (a different sound per stage: flat, ping, long) and the
// voice says WHICH symbol, for a trader looking at another monitor. The
// columns are the same for all three: the usual price context, the multiple
// that made it a candidate, and the verdict.
func emaFlagScans() []core.ScanDef {
	cols := func() []string {
		return []string{
			"last", "chgDay", "pmVolX",
			"e9State", "e9Dist", "e9Trigger", "e9FlagBars", "e9Age",
			"news",
		}
	}
	mk := func(id, name, sound string, stage []core.Filter, sort ...core.SortKey) core.ScanDef {
		conds := append([]core.Filter{{Field: core.FieldPmVolX, Cmp: core.CmpGTE, Value: 1}}, stage...)
		return core.ScanDef{
			ID: id, Name: name, Enabled: true,
			Filter:  core.Filter{Op: "and", Conds: conds},
			Sort:    sort,
			Limit:   50,
			Columns: cols(),
			Alert: core.AlertConfig{
				Enabled: true, On: core.AlertOnEnter, Sound: sound,
				CooldownSec: e9FlagCooldownSec, Speak: true,
			},
		}
	}
	return []core.ScanDef{
		mk(e9FlagSidewaysID, "9EMA Flag — sideways", "flat",
			[]core.Filter{{Field: core.FieldE9State, Cmp: core.CmpGTE, Value: 2}},
			core.SortKey{Field: core.FieldE9State, Dir: "desc"},
			core.SortKey{Field: core.FieldE9Dist, Dir: "asc"}),
		mk(e9FlagReadyID, "9EMA Flag — ready", "ping",
			[]core.Filter{{Field: core.FieldE9State, Cmp: core.CmpEq, Value: 3}},
			core.SortKey{Field: core.FieldE9Dist, Dir: "asc"}),
		mk(e9FlagBreakoutID, "9EMA Flag — breakout", "long",
			[]core.Filter{
				{Field: core.FieldE9State, Cmp: core.CmpEq, Value: 4},
				{Field: core.FieldE9Age, Cmp: core.CmpLTE, Value: e9FlagBreakoutAgeS},
			},
			core.SortKey{Field: core.FieldE9Age, Dir: "asc"}),
	}
}

// The seeded 9 EMA SLAM scan: its ID and the schema_meta key it is guarded by.
// A key of its OWN, not a fourth def in emaFlagScans: every existing database
// already consumed seeded.e9flag.v1, so a scan added to that batch would never
// reach them.
const (
	e9SlamSeedKey = "seeded.e9slam.v1"
	e9SlamID      = "e9flag-slam"
	e9SlamAgeS    = 300 // a SLAM stays on the scan for five minutes after its bar began
)

// emaFlagSlamScans is the one seeded scan of the 9 EMA SLAM (user, 2026-10-07:
// "A candle after a stock has been up and trades more or less sideways SLAMS
// through the 9EMA."): the flagscan service's engine flags a closed 1-minute
// candle that opens at/above the 9 EMA out of a flag (or a position) and closes
// well under it on a big, high-volume body; the scanner overlays the day's
// latest one as e9SlamAge / e9SlamPct. The scan is the candidate gate restated
// (pmVolX >= 1, as on the three flag scans) AND a slam in the last five
// minutes, freshest first. e9SlamAge counts from the slam bar's START, and the
// slam is only known once that bar has closed, so a slam shows from about a
// minute old to five minutes old. Alert on ENTER, its own falling 'slam' tone,
// Speak on ("SPAI: nine E M A slam"), 300 s cooldown like the flag scans. An
// alert only.
// Seeded once per database and never resurrected after a deletion.
func emaFlagSlamScans() []core.ScanDef {
	return []core.ScanDef{{
		ID: e9SlamID, Name: "9EMA Slam", Enabled: true,
		Filter: core.Filter{Op: "and", Conds: []core.Filter{
			{Field: core.FieldPmVolX, Cmp: core.CmpGTE, Value: 1},
			{Field: core.FieldE9SlamAge, Cmp: core.CmpLTE, Value: e9SlamAgeS},
		}},
		Sort:  []core.SortKey{{Field: core.FieldE9SlamAge, Dir: "asc"}},
		Limit: 50,
		Columns: []string{
			"last", "chgDay", "pmVolX",
			"e9SlamPct", "e9SlamAge", "e9Dist",
			"news",
		},
		Alert: core.AlertConfig{
			Enabled: true, On: core.AlertOnEnter, Sound: "slam",
			CooldownSec: e9FlagCooldownSec, Speak: true,
		},
	}}
}

// Seeded "HOD Flag" scan: its ID and its own schema_meta key.
const (
	hodFlagSeedKey = "seeded.hodflag.v1"
	hodFlagID      = "hod-flag"
	hodFlagAgeS    = 300 // a HOD flag stays on the scan for five minutes after its FLAT bar began
)

// hodFlagScans is the one seeded scan of the HOD FLAG (user, 2026-10-16: "an
// alert on the flag forming after a HOD break"): the 9EMA flag engine's FLAT
// event (sideways consolidation above/at the 9 EMA after the run-up) on a flag
// whose run-up top is a NEW HIGH OF THE DAY, pre-market included
// (emaflag Result.LastHodFlagTs, overlaid as e9HodFlagAge). Same candidate gate
// as the other flag scans (pmVolX >= 1). The age is decided once when the flag
// opens, so it alerts once per flag; ON by default, Speak says "SYM: HOD flag"
// (web e9.ts spokenScanName). An alert only. Seeded once, never resurrected.
func hodFlagScans() []core.ScanDef {
	return []core.ScanDef{{
		ID: hodFlagID, Name: "HOD Flag", Enabled: true,
		Filter: core.Filter{Op: "and", Conds: []core.Filter{
			{Field: core.FieldPmVolX, Cmp: core.CmpGTE, Value: 1},
			{Field: core.FieldE9HodFlagAge, Cmp: core.CmpLTE, Value: hodFlagAgeS},
		}},
		Sort:  []core.SortKey{{Field: core.FieldE9HodFlagAge, Dir: "asc"}},
		Limit: 50,
		Columns: []string{
			"last", "chgDay", "pmVolX",
			"e9HodFlagAge", "e9State", "e9Dist",
			"news",
		},
		Alert: core.AlertConfig{
			Enabled: true, On: core.AlertOnEnter, Sound: "ping",
			CooldownSec: e9FlagCooldownSec, RequireReentry: true, Speak: true,
		},
	}}
}

// dildoEnabled / climaxEnabled are the env gates of the two closed-1-minute-bar
// alerts (engine.Options.Dildo / .Climax): on unless SCANNER_DILDO=0 /
// SCANNER_CLIMAX=0. Off, the engine never judges for that signal, its row
// fields stay Null and its seeded scans simply stay empty (they are seeded
// regardless, like the 9EMA flag scans under SCANNER_E9FLAGS=0). For the
// dildo that includes flagscan's own pass over its candidates (emaFlagConfig
// hands it the rule disabled); only dildoPbState reads 0, "none", on the
// symbols the 9EMA flag scan tracks. The dildo's alert 2 (the 9 EMA pullback)
// also needs the 9EMA flag scan (SCANNER_E9FLAGS), which judges it on the
// chart's bars.
func dildoEnabled() bool  { return os.Getenv("SCANNER_DILDO") != "0" }
func climaxEnabled() bool { return os.Getenv("SCANNER_CLIMAX") != "0" }

// bbsEnabled gates the BOUNCY BALL SHORT judge (env SCANNER_BBS, default on).
func bbsEnabled() bool { return os.Getenv("SCANNER_BBS") != "0" }

// ignitionEnabled gates the IGNITION judge (env SCANNER_IGNITION, default on).
func ignitionEnabled() bool { return os.Getenv("SCANNER_IGNITION") != "0" }

// emaVwapEnabled gates the 9EMA VWAP CROSS judge (env SCANNER_EMAVWAP, default on).
func emaVwapEnabled() bool { return os.Getenv("SCANNER_EMAVWAP") != "0" }

// volBreakEnabled gates the VOLUME BREAK judge (env SCANNER_VOLBREAK, default on).
func volBreakEnabled() bool { return os.Getenv("SCANNER_VOLBREAK") != "0" }

// The seeded GREEN DILDO -> 9 EMA PULLBACK scans: their IDs and the
// schema_meta key the batch is guarded by (its own: every database already
// consumed the earlier keys).
const (
	dildoSeedKey     = "seeded.dildo.v1"
	dildoTWSKey      = "seeded.dildo.tws.v1" // one-time: switch "Send to TWS" on for an existing Green Dildo scan
	dildo1mID        = "dildo-1m"
	dildoPbID        = "dildo-pb"
	dildoTouchID     = "dildo-touch"
	dildoCooldownSec = 600 // one alert per symbol per scan per ten minutes (the calibration's alert cooldown)
	dildoFreshS      = 240 // an alert's row stays on its scan for four minutes after the bar began
)

// dildoScans are the three seeded scans of the GREEN DILDO -> 9 EMA PULLBACK
// signal (user, 2026-10-07: "a lot of times when there is a green dildo candle
// i can enter on the 9EMA for a scalp ... Can we have a signal scanner for the
// green dildo candles on the 1 minute as well?"). Two sequential alerts, on
// the WHOLE market (no pmVolX gate) above a liquidity floor:
//
//   - "Green Dildo" (dildo-1m): the engine judged a closed 1-minute candle a
//     green dildo in the last four minutes; the alert fires when the row
//     ENTERS, ~3-4 s after the candle closed. Speak says "SYM: green dildo".
//     The `last >= 0.20` and `dildoDvol >= 100000` terms RESTATE the engine's
//     own floor so the user can RAISE it in the editor: lowering them below
//     $0.20 / $100k changes nothing, the engine never latches a dildo under it.
//   - "Dildo 9EMA Pullback" (dildo-pb): flagscan saw price come back to the
//     chart's 9 EMA while the move held (a touch bar closing near the EMA and
//     above the dildo's mid floor) - the scalp entry; the existing rising
//     `long` tone, Speak "SYM: nine E M A pullback".
//   - "Dildo 9EMA Touch" (dildo-touch): the intrabar early cue (a forming bar
//     touching the EMA band), its alert OFF by default; one click enables it.
//
// The two age fields (dildoAge, dildoPbAge) are what make the two alerts fire
// in sequence for one symbol. An ALERT only - a "look now" prompt, not a
// system: calibration found no edge after costs. ScanDef has no description
// field, so that caveat lives here and in the README. Seeded once per
// database, never resurrected after a deletion.
func dildoScans() []core.ScanDef {
	alert := func(sound string, on, speak bool) core.AlertConfig {
		return core.AlertConfig{Enabled: on, On: core.AlertOnEnter, Sound: sound, CooldownSec: dildoCooldownSec, Speak: speak}
	}
	return []core.ScanDef{
		{
			ID: dildo1mID, Name: "Green Dildo", Enabled: true,
			Filter: core.Filter{Op: "and", Conds: []core.Filter{
				{Field: core.FieldDildoAge, Cmp: core.CmpLTE, Value: dildoFreshS},
				{Field: core.FieldLast, Cmp: core.CmpGTE, Value: 0.20},        // the engine floor, restated: raise it here
				{Field: core.FieldDildoDvol, Cmp: core.CmpGTE, Value: 100000}, // the engine floor, restated: raise it here
			}},
			Sort:    []core.SortKey{{Field: core.FieldDildoAge, Dir: "asc"}},
			Limit:   50,
			Columns: []string{"last", "dildoBody", "dildoVolX", "dildoDvol", "dildoAge", "chgDay", "vol", "news"},
			Alert:   withSendToTWS(alert("dildo", true, true)), // "straight to TWS": the linked window follows the alert (guarded, display link only)
		},
		{
			ID: dildoPbID, Name: "Dildo 9EMA Pullback", Enabled: true,
			Filter: core.Filter{Op: "and", Conds: []core.Filter{
				{Field: core.FieldDildoPbState, Cmp: core.CmpEq, Value: 3},
				{Field: core.FieldDildoPbAge, Cmp: core.CmpLTE, Value: dildoFreshS},
			}},
			Sort:    []core.SortKey{{Field: core.FieldDildoPbAge, Dir: "asc"}},
			Limit:   50,
			Columns: []string{"last", "dildoPbPx", "dildoBody", "dildoPbAge", "chgDay", "vol", "news"},
			Alert:   alert("long", true, true),
		},
		{
			ID: dildoTouchID, Name: "Dildo 9EMA Touch", Enabled: true,
			Filter: core.Filter{Op: "and", Conds: []core.Filter{
				{Field: core.FieldDildoPbState, Cmp: core.CmpEq, Value: 2},
			}},
			Sort:    []core.SortKey{{Field: core.FieldDildoTouchAge, Dir: "asc"}},
			Limit:   50,
			Columns: []string{"last", "dildoBody", "dildoTouchAge", "chgDay", "news"},
			Alert:   alert("ping", false, false),
		},
	}
}

func withSendToTWS(a core.AlertConfig) core.AlertConfig {
	a.SendToTWS = true
	return a
}

// migrateDildoSendToTWS switches "Send to TWS when it alerts" on for the
// Green Dildo scan (dildo-1m) of a database that was seeded before that option
// existed (user, 2026-10-08: "If there is a Green Dildo alert send it straight
// to TWS"). Runs once, under its own schema_meta key, AFTER the seeding above
// (a fresh install already got the flag from dildoScans). Only that one field
// changes: whatever the user did to the scan since (filter, sound, cooldown,
// even switching Speak off) stays exactly as it is. A deleted scan is not
// resurrected, the key is simply set. A failed save leaves the key unset so
// the next boot retries; an unreadable meta store or scan list does nothing.
func migrateDildoSendToTWS(
	ctx context.Context,
	getMeta func(context.Context, string) (string, error),
	setMeta func(context.Context, string, string) error,
	list func(context.Context) ([]core.ScanDef, error),
	save func(context.Context, core.ScanDef) error,
	install func(core.ScanDef),
) {
	if v, err := getMeta(ctx, dildoTWSKey); err != nil || v != "" {
		return // already ran (or meta unreadable)
	}
	defs, err := list(ctx)
	if err != nil {
		logx.L().Warn("dildo send-to-TWS migration: listing scans failed", "err", err)
		return
	}
	for _, d := range defs {
		if d.ID != dildo1mID {
			continue
		}
		if !d.Alert.SendToTWS {
			d.Alert.SendToTWS = true
			if err := save(ctx, d); err != nil {
				logx.L().Warn("dildo send-to-TWS migration: save failed", "id", d.ID, "err", err)
				return // retry next boot; key stays unset
			}
			install(d)
			logx.L().Info("Green Dildo now sends its alerts to TWS (Send to TWS switched on once; untick it in the scan editor to stop)", "id", d.ID)
		}
		break
	}
	_ = setMeta(ctx, dildoTWSKey, "1")
}

// The seeded VOLUME CLIMAX -> BOTTOM CONFIRMED scans: their IDs and their
// own schema_meta key.
const (
	climaxSeedKey      = "seeded.climax.v1"
	climax1mID         = "climax-1m"
	climaxConfirmID    = "climax-confirm"
	climaxCooldownSec  = 600  // one alert per symbol per scan per ten minutes
	climaxFreshS       = 240  // a row stays on its scan for four minutes after its bar began
	climaxConfirmMaxS  = 1200 // Decision C: a confirmation more than 20 min after its climax is not "bottom confirmed"
	climaxFloorLast    = 0.50
	climaxFloorDvolUSD = 50000
)

// climaxScans are the two seeded scans of the VOLUME CLIMAX -> BOTTOM
// CONFIRMED signal (user, 2026-10-07: "I want a fourth signal. Climatic Volume
// on a bottom or sell off move."). Both alerts are engine-native, on the WHOLE
// market (no pmVolX gate) above a liquidity floor:
//
//   - "Volume Climax" (climax-1m): a closed 1-minute candle made a fresh low
//     of the day's last 10 bars, at least 6 % under their high, on 8x their
//     mean volume and the biggest of them, with a 25 % lower wick, closing
//     under the 9 EMA (09:30-09:34 ET muted); the alert fires when the row
//     ENTERS, ~3-4 s after the candle closed, with its own low held `climax`
//     tone and Speak "SYM: volume climax". `last >= 0.50` and
//     `climaxDvol >= 50000` RESTATE the engine floor so the user can RAISE it;
//     lowering them below $0.50 / $50k changes nothing.
//   - "Bottom Confirmed" (climax-confirm): within the next 5 traded bars a
//     close above the climax candle's midpoint before any new low (state 2),
//     confirmed in the last four minutes, and at most 20 minutes after the
//     climax (Decision C: the rule counts traded bars, so on a thin name the
//     5 bars can span hours; this term hides those); the `bottom` octave leap,
//     Speak "SYM: bottom confirmed".
//
// The two age fields (climaxAge, climaxConfAge) make the alerts fire in
// sequence. Alert only: a "look now" prompt, not a system. No edge after costs
// in calibration (ScanDef has no description field, so that caveat lives here
// and in the README). Seeded once per database, never resurrected.
func climaxScans() []core.ScanDef {
	alert := func(sound string) core.AlertConfig {
		return core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: sound, CooldownSec: climaxCooldownSec, Speak: true}
	}
	return []core.ScanDef{
		{
			ID: climax1mID, Name: "Volume Climax", Enabled: true,
			Filter: core.Filter{Op: "and", Conds: []core.Filter{
				{Field: core.FieldClimaxAge, Cmp: core.CmpLTE, Value: climaxFreshS},
				{Field: core.FieldLast, Cmp: core.CmpGTE, Value: climaxFloorLast},          // the engine floor, restated: raise it here
				{Field: core.FieldClimaxDvol, Cmp: core.CmpGTE, Value: climaxFloorDvolUSD}, // the engine floor, restated: raise it here
			}},
			Sort:    []core.SortKey{{Field: core.FieldClimaxAge, Dir: "asc"}},
			Limit:   50,
			Columns: []string{"last", "climaxDropPct", "climaxVolX", "climaxDvol", "climaxLow", "climaxAge", "chgDay", "vol", "news"},
			Alert:   alert("climax"),
		},
		{
			ID: climaxConfirmID, Name: "Bottom Confirmed", Enabled: true,
			Filter: core.Filter{Op: "and", Conds: []core.Filter{
				{Field: core.FieldClimaxState, Cmp: core.CmpEq, Value: 2},
				{Field: core.FieldClimaxConfAge, Cmp: core.CmpLTE, Value: climaxFreshS},
				{Field: core.FieldClimaxAge, Cmp: core.CmpLTE, Value: climaxConfirmMaxS},
			}},
			Sort:    []core.SortKey{{Field: core.FieldClimaxConfAge, Dir: "asc"}},
			Limit:   50,
			Columns: []string{"last", "climaxConfPx", "climaxLow", "climaxDropPct", "climaxVolX", "climaxConfAge", "chgDay", "news"},
			Alert:   alert("bottom"),
		},
	}
}

// The seeded BOUNCY BALL SHORT scan: its ID and its own schema_meta key.
const (
	bbsSeedKey      = "seeded.bbs.v1"
	bbsScanID       = "bouncy-short"
	bbsCooldownSec  = 600
	bbsFreshS       = 240
	bbsFloorLast    = 1.0
	bbsFloorDvolUSD = 100000
)

// bbsScans is the seeded BOUNCY BALL SHORT scan (user, 2026-10-14: "It has a
// solid support and is making lower highs on each bounce of that support and
// after that will sell off sharply."): engine-native on the WHOLE market, a
// closed 1-minute bar breaking a support touched >= 3 times with lower highs
// on each bounce (internal/bbs). Price >= $1 and the break bar's dollar volume
// >= $100k; alert on enter with the falling `bounce` sound, Speak "SYM: bouncy
// ball short", 10-minute cooldown. Alert only (uncalibrated). Seeded once.
func bbsScans() []core.ScanDef {
	return []core.ScanDef{{
		ID: bbsScanID, Name: "Bouncy Ball Short", Enabled: true,
		Filter: core.Filter{Op: "and", Conds: []core.Filter{
			{Field: core.FieldBbsAge, Cmp: core.CmpLTE, Value: bbsFreshS},
			{Field: core.FieldLast, Cmp: core.CmpGTE, Value: bbsFloorLast},
			{Field: core.FieldBbsDvol, Cmp: core.CmpGTE, Value: bbsFloorDvolUSD},
		}},
		Sort:    []core.SortKey{{Field: core.FieldBbsAge, Dir: "asc"}},
		Limit:   50,
		Columns: []string{"last", "bbsSupport", "bbsDvol", "bbsAge", "chgDay", "vol", "news"},
		Alert:   core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: "bounce", CooldownSec: bbsCooldownSec, Speak: true},
	}}
}

// The seeded BOUNCY BALL FORMING scan (2026-10-14): its own key so databases
// that already have "bouncy-short" get it once.
const (
	bbsFormingSeedKey     = "seeded.bbs.forming.v1"
	bbsFormingScanID      = "bouncy-forming"
	bbsFormingCooldownSec = 900
	// Liquidity floor: bbsDvol is the BREAK bar's dollar volume, Null while a
	// base is only forming, so the floor is day volume instead (>= 200k shares,
	// i.e. >= $200k at the $1 price floor).
	bbsFormingFloorVol = 200000
)

// bbsFormingScans lists the bases that are FORMING (bbsState 1: support with
// 3+ touches, 2+ lower highs, last close within 1% of it), nearest to the
// support first. Alert on enter with the soft `bounceSoft` tone, Speak
// "SYM: bouncy ball forming", 15-minute cooldown. The web Signal window's
// "Bouncy forming" switch defaults OFF.
func bbsFormingScans() []core.ScanDef {
	return []core.ScanDef{{
		ID: bbsFormingScanID, Name: "Bouncy Ball Forming", Enabled: true,
		Filter: core.Filter{Op: "and", Conds: []core.Filter{
			{Field: core.FieldBbsState, Cmp: core.CmpEq, Value: 1},
			{Field: core.FieldLast, Cmp: core.CmpGTE, Value: bbsFloorLast},
			{Field: core.FieldVol, Cmp: core.CmpGTE, Value: bbsFormingFloorVol},
		}},
		Sort:    []core.SortKey{{Field: core.FieldBbsDistPct, Dir: "asc"}},
		Limit:   50,
		Columns: []string{"last", "chgDay", "bbsSupport", "bbsDistPct", "news"},
		Alert:   core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: "bounceSoft", CooldownSec: bbsFormingCooldownSec, Speak: true},
	}}
}

// Seeded 1-minute candle spike scan: its ID and the schema_meta key it is
// guarded by.
const (
	c1mSeedKey = "seeded.c1m.v1"
	c1mSpikeID = "c1m-spike"
)

// c1mSpikeScans is the one seeded scan of the 1-minute candle fields: a live
// 1-minute candle up 5% or more on at least 3x the volume of the nine minutes
// before it (c1mChg / c1mRvol describe the SAME candle: the forming one, or the
// one that just closed while it is the stronger). Seeded once per database like
// every other batch and never resurrected after a deletion; the editor shows
// the whole rule so the thresholds can be tuned.
func c1mSpikeScans() []core.ScanDef {
	return []core.ScanDef{{
		ID: c1mSpikeID, Name: "1m Candle Spike", Enabled: true,
		Filter: core.Filter{Op: "and", Conds: []core.Filter{
			{Field: core.FieldC1mChg, Cmp: core.CmpGTE, Value: 5},
			{Field: core.FieldC1mRvol, Cmp: core.CmpGTE, Value: 3},
		}},
		Sort: []core.SortKey{
			{Field: core.FieldC1mChg, Dir: "desc"},
			{Field: core.FieldC1mRvol, Dir: "desc"},
		},
		Limit:   50,
		Columns: []string{"last", "c1mChg", "c1mRvol", "vol", "chgDay", "rvol", "news"},
		Alert:   core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: "ping", CooldownSec: 120, RequireReentry: true},
	}}
}

// Seeded "PM New High" scan: its ID and its own schema_meta key.
const (
	pmNewHighSeedKey = "seeded.pmnewhigh.v1"
	pmNewHighID      = "pm-new-high"
)

// pmNewHighScans: in the pre-market, a stock on 2x+ relative volume trading at
// a new high of the pre-market. vsHod is the right field here: the engine's
// high of day resets at the day roll and is built from bar highs, so during the
// pre-market it IS the pre-market high, and vsHod = % of last vs it (0 = at the
// high). vsPmh is not usable: it stays Null until the pre-market has completed.
// sessHigh cannot be compared to last in a filter (leaf conditions are
// field-vs-constant). Alert on enter with RequireReentry: a pullback drops the
// row, the next fresh high re-enters and alerts again (120 s cooldown); Speak
// says "SYM: pre-market high" (web e9.ts spokenScanName). Seeded once, never
// resurrected after a deletion.
func pmNewHighScans() []core.ScanDef {
	return []core.ScanDef{{
		ID: pmNewHighID, Name: "PM New High", Enabled: true,
		Filter: core.Filter{Op: "and", Conds: []core.Filter{
			{Field: core.FieldSession, Cmp: core.CmpEq, Value: "pre"},
			{Field: core.FieldRvol, Cmp: core.CmpGTE, Value: 2},
			{Field: core.FieldVsHod, Cmp: core.CmpGTE, Value: 0},
		}},
		Sort:    []core.SortKey{{Field: core.FieldRvol, Dir: "desc"}},
		Limit:   50,
		Columns: []string{"last", "chgDay", "rvol", "vol", "news"},
		Alert:   core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: "ping", CooldownSec: 120, RequireReentry: true, Speak: true},
	}}
}

// seedScans installs a batch of pre-built scans exactly once per database,
// guarded by its own schema_meta key so independent batches (flag scans, the
// session-review scan, ...) seed and retry independently. have is the
// already-loaded def list (an existing ID is never overwritten — the user may
// have tuned it); save persists, install adds to the live scanner. Injected
// funcs keep it unit-testable without a real DB or scanner.
func seedScans(
	ctx context.Context,
	metaKey string,
	defs []core.ScanDef,
	getMeta func(context.Context, string) (string, error),
	setMeta func(context.Context, string, string) error,
	have []core.ScanDef,
	save func(context.Context, core.ScanDef) error,
	install func(core.ScanDef),
) {
	if v, err := getMeta(ctx, metaKey); err != nil || v != "" {
		return // already ran (or meta unreadable — don't risk double-seeding)
	}
	existing := make(map[string]bool, len(have))
	for _, d := range have {
		existing[d.ID] = true
	}
	for _, d := range defs {
		if existing[d.ID] {
			continue
		}
		if err := save(ctx, d); err != nil {
			logx.L().Warn("scan seed failed", "key", metaKey, "id", d.ID, "err", err)
			return // retry next boot; meta stays unset
		}
		install(d)
		logx.L().Info("seeded scan", "key", metaKey, "id", d.ID)
	}
	_ = setMeta(ctx, metaKey, "1")
}

// defaultScan is the 5-min momentum scan seeded on first run.
func defaultScan() core.ScanDef {
	return core.ScanDef{
		ID: "mom5", Name: "5-min Momentum", Enabled: true,
		Filter: core.Filter{Op: "and", Conds: []core.Filter{
			{Field: core.FieldLast, Cmp: core.CmpBetween, Value: []any{1, 50}},
			{Field: core.FieldChg5m, Cmp: core.CmpGT, Value: 2},
			{Field: core.FieldVol, Cmp: core.CmpGT, Value: 50000},
		}},
		Sort:    []core.SortKey{{Field: core.FieldChg5m, Dir: "desc"}},
		Limit:   50,
		Columns: []string{"last", "chg5m", "chg1m", "chgDay", "vol", "rvol", "vwap", "news"},
		Alert:   core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: "ping", CooldownSec: 120, RequireReentry: true},
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// The seeded IGNITION scan: its ID and its own schema_meta key.
const (
	ignitionSeedKey     = "seeded.ignition.v1"
	ignitionScanID      = "ignition"
	ignitionCooldownSec = 600
	ignitionFreshS      = 240
	ignitionPassKey     = "migrated.ignition.pass.v1"
)

// ignitionPassCond: the Ignition scan alerts only on ignitions passing the
// engine's four skip rules (engine ignSkip; user 2026-10-14).
func ignitionPassCond() core.Filter {
	return core.Filter{Field: core.FieldIgnPass, Cmp: core.CmpEq, Value: 1}
}

// migrateIgnitionPass adds ignitionPassCond to the seeded Ignition scan of a
// database seeded before the skip rules existed - once (its own meta key), and
// only when that scan is still exactly the original seed (filter, sort, limit,
// columns, alert, name, enabled); an edited or deleted scan is left alone. A
// failed save leaves the key unset so the next boot retries.
func migrateIgnitionPass(
	ctx context.Context,
	getMeta func(context.Context, string) (string, error),
	setMeta func(context.Context, string, string) error,
	list func(context.Context) ([]core.ScanDef, error),
	save func(context.Context, core.ScanDef) error,
	install func(core.ScanDef),
) {
	if v, err := getMeta(ctx, ignitionPassKey); err != nil || v != "" {
		return
	}
	defs, err := list(ctx)
	if err != nil {
		logx.L().Warn("ignition pass migration: listing scans failed", "err", err)
		return
	}
	for _, d := range defs {
		if d.ID != ignitionScanID {
			continue
		}
		if ignitionScanUnedited(d) {
			d.Filter.Conds = append(d.Filter.Conds, ignitionPassCond())
			if err := save(ctx, d); err != nil {
				logx.L().Warn("ignition pass migration: save failed", "err", err)
				return
			}
			install(d)
			logx.L().Info("Ignition scan now alerts only on ignitions passing the skip rules (ignPass = 1)")
		}
		break
	}
	_ = setMeta(ctx, ignitionPassKey, "1")
}

// ignitionScanUnedited: d is still the pre-skip-rules seed of the Ignition scan.
func ignitionScanUnedited(d core.ScanDef) bool {
	orig := ignitionScans()[0]
	orig.Filter.Conds = orig.Filter.Conds[:len(orig.Filter.Conds)-1] // the seed before ignitionPassCond
	a, err1 := json.Marshal(orig)
	b, err2 := json.Marshal(d)
	return err1 == nil && err2 == nil && string(a) == string(b)
}

// ignitionScans is the seeded IGNITION scan: engine-native on the WHOLE market,
// the small first candle out of a quiet base right before a dildo
// (internal/ignition; the rule carries its own liquidity floor: close >= $0.20,
// candle >= $50k). Alert on enter with the short sharp `ignite` sound, Speak
// "SYM: ignition", 10-minute cooldown. Alert only (uncalibrated). Seeded once.
func ignitionScans() []core.ScanDef {
	return []core.ScanDef{{
		ID: ignitionScanID, Name: "Ignition", Enabled: true,
		Filter: core.Filter{Op: "and", Conds: []core.Filter{
			{Field: core.FieldIgnAge, Cmp: core.CmpLTE, Value: ignitionFreshS},
			ignitionPassCond(),
		}},
		Sort:    []core.SortKey{{Field: core.FieldIgnAge, Dir: "asc"}},
		Limit:   50,
		Columns: []string{"last", "ignBodyPct", "ignVolX", "ignAge", "chgDay", "vol", "news"},
		Alert:   core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: "ignite", CooldownSec: ignitionCooldownSec, Speak: true},
	}}
}

// The seeded VOLUME BREAK scans (docs/volume-break.md): their IDs and their
// own schema_meta key.
const (
	volBreakSeedKey     = "seeded.volbreak.v1"
	volBreakScanID      = "vol-break"
	volBreakEntryScanID = "vol-break-9ema"
	volBreakFreshS      = 240
	volBreakCooldownSec = 600
)

// volBreakScans are the two seeded VOLUME BREAK scans, engine-native on the
// whole market (internal/volbreak; the rule carries its own universe: up 10%+
// on the day on 1M+ shares). Alerts only, nothing is ordered. Seeded once,
// never resurrected after a deletion.
//
//   - "Vol Break" (vol-break): a 1m close over a 30+ minute consolidation high
//     (tested twice, <= 12% wide) on 3x+ the range's mean volume. The
//     `volbreak` tone, Speak "SYM: volume break 3.43 (4.2x vol), 9EMA x.xx".
//   - "Vol Break 9EMA" (vol-break-9ema): the first pullback to the 1m 9 EMA
//     within 30 minutes, still closing over the level (vbState 2). The rising
//     `long` entry phrase, Speak "SYM: vol break 9EMA entry x.xx, level 3.43".
func volBreakScans() []core.ScanDef {
	alert := func(sound string) core.AlertConfig {
		return core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: sound, CooldownSec: volBreakCooldownSec, Speak: true}
	}
	return []core.ScanDef{
		{
			ID: volBreakScanID, Name: "Vol Break", Enabled: true,
			Filter: core.Filter{Op: "and", Conds: []core.Filter{
				{Field: core.FieldVbAge, Cmp: core.CmpLTE, Value: volBreakFreshS},
			}},
			Sort:    []core.SortKey{{Field: core.FieldVbAge, Dir: "asc"}},
			Limit:   50,
			Columns: []string{"last", "vbLevel", "vbVolX", "vbEma", "vbState", "vbAge", "chgDay", "vol", "news"},
			Alert:   alert("volbreak"),
		},
		{
			ID: volBreakEntryScanID, Name: "Vol Break 9EMA", Enabled: true,
			Filter: core.Filter{Op: "and", Conds: []core.Filter{
				{Field: core.FieldVbState, Cmp: core.CmpEq, Value: 2},
				{Field: core.FieldVbEntryAge, Cmp: core.CmpLTE, Value: volBreakFreshS},
			}},
			Sort:    []core.SortKey{{Field: core.FieldVbEntryAge, Dir: "asc"}},
			Limit:   50,
			Columns: []string{"last", "vbEntryPx", "vbLevel", "vbVolX", "vbEntryAge", "vbAge", "chgDay", "vol", "news"},
			Alert:   alert("long"),
		},
	}
}

// The seeded POWER CANDLE scan: its ID and its own schema_meta key.
const (
	powerCandleSeedKey = "seeded.powercandle.v1"
	powerCandleScanID  = "power-candle"
)

// powerCandleScans is the seeded POWER CANDLE scan: engine-native on the whole
// market, a 1-minute candle passing every green-dildo rule (the live tuned
// params, HOD only) EXCEPT the close-near-high one - any top wick - and not
// itself a dildo. Alert on enter with the `bell` tone, Speak "SYM: power
// candle". Alert only; no HOT admission or pullback follow-ups. Seeded once.
func powerCandleScans() []core.ScanDef {
	return []core.ScanDef{{
		ID: powerCandleScanID, Name: "Power Candle", Enabled: true,
		Filter: core.Filter{Op: "and", Conds: []core.Filter{
			{Field: core.FieldPcAge, Cmp: core.CmpLTE, Value: dildoFreshS},
		}},
		Sort:    []core.SortKey{{Field: core.FieldPcAge, Dir: "asc"}},
		Limit:   50,
		Columns: []string{"last", "pcBodyPct", "pcTopPct", "pcBodyX", "pcVolX", "pcDvol", "pcAge", "psFromOpen", "psEmaSlope", "psGap", "chgDay", "vol", "news"},
		Alert:   core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: "bell", CooldownSec: dildoCooldownSec, Speak: true},
	}}
}

// The seeded POWER SETUP scan: its ID and its own schema_meta key.
const (
	powerSetupSeedKey = "seeded.powersetup.v1"
	powerSetupScanID  = "power-setup"
)

// powerSetupScans is the seeded POWER SETUP scan (docs/power-setup.md): a power
// candle that passed the three filters (psPass == 1: close < 21% over the day's
// open, 1m 9EMA up >= 2.1% over 3 bars; the gap filter is off by default), listed while the buy limit
// is valid (~15 minutes). Alert on enter with the distinct `psetup` tone, Speak
// "SYM: power setup, entry X, stop Y". Alerts only: nothing is ever ordered.
// Seeded once, never resurrected after a deletion.
func powerSetupScans() []core.ScanDef {
	return []core.ScanDef{{
		ID: powerSetupScanID, Name: "Power Setup", Enabled: true,
		Filter: core.Filter{Op: "and", Conds: []core.Filter{
			{Field: core.FieldPsPass, Cmp: core.CmpEq, Value: 1},
			{Field: core.FieldPsAge, Cmp: core.CmpLTE, Value: core.PsEntryValidSec},
		}},
		Sort:    []core.SortKey{{Field: core.FieldPsAge, Dir: "asc"}},
		Limit:   50,
		Columns: []string{"last", "psAplus", "psBaseRange", "psEntry", "psStop", "psTarget", "psFromOpen", "psEmaSlope", "psGap", "psAge", "chgDay", "vol", "news"},
		Alert:   core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: "psetup", CooldownSec: dildoCooldownSec, Speak: true},
	}}
}

// powerSetupAplusKey: the one-off migration that adds the A+ grade columns
// (psAplus, psBaseRange) to an already-seeded Power Setup scan, so the web sees
// the grade (alert text, P-SETUP A+ badge, the order ticket's 1.5x risk).
const powerSetupAplusKey = "migrated.powersetup.aplus.v1"

// migratePowerSetupAplus appends the missing A+ columns to the power-setup scan
// once (own meta key); the filter is never touched. A failed save retries next boot.
func migratePowerSetupAplus(
	ctx context.Context,
	getMeta func(context.Context, string) (string, error),
	setMeta func(context.Context, string, string) error,
	list func(context.Context) ([]core.ScanDef, error),
	save func(context.Context, core.ScanDef) error,
	install func(core.ScanDef),
) {
	if v, err := getMeta(ctx, powerSetupAplusKey); err != nil || v != "" {
		return
	}
	defs, err := list(ctx)
	if err != nil {
		logx.L().Warn("power setup A+ migration: listing scans failed", "err", err)
		return
	}
	for _, d := range defs {
		if d.ID != powerSetupScanID {
			continue
		}
		added := false
		for _, c := range []string{core.FieldPsAplus, core.FieldPsBaseRange} {
			if !slices.Contains(d.Columns, c) {
				d.Columns, added = append(d.Columns, c), true
			}
		}
		if !added {
			continue
		}
		if err := save(ctx, d); err != nil {
			logx.L().Warn("power setup A+ migration: save failed", "err", err)
			return
		}
		install(d)
		logx.L().Info("Power Setup scan now carries the A+ grade columns")
	}
	_ = setMeta(ctx, powerSetupAplusKey, "1")
}

// Seeded "Daily Breakout" scan: its ID and its own schema_meta key.
const (
	dailyBreakSeedKey = "seeded.dailybreak.v1"
	dailyBreakID      = "daily-breakout"
)

// dailyBreakScans: stocks over $30 that crossed a DAILY level today (the
// 14-day high, a swing-high pivot, or the previous day's high; internal/dlevel,
// from daily_close_history) and are still above it. dbPct > 0 only matches a
// symbol with a broken level (Null fails numerics); last > 30 is the price
// floor; avgVol >= 500000 is the same liquidity floor as the premarket volume
// scan. Sort: 14D HIGH first (dbType rank 1), then the smallest % above the
// level (the freshest break). The alert is OFF by default (the Signal window's
// switch is off too); Speak says "SYM: daily breakout". Seeded once, never
// resurrected after a deletion.
func dailyBreakScans() []core.ScanDef {
	return []core.ScanDef{{
		ID: dailyBreakID, Name: "Daily Breakout", Enabled: true,
		Filter: core.Filter{Op: "and", Conds: []core.Filter{
			{Field: core.FieldLast, Cmp: core.CmpGT, Value: 30},
			{Field: core.FieldAvgVol, Cmp: core.CmpGTE, Value: 500000},
			{Field: core.FieldDbPct, Cmp: core.CmpGT, Value: 0},
		}},
		Sort: []core.SortKey{
			{Field: core.FieldDbType, Dir: "asc"},
			{Field: core.FieldDbPct, Dir: "asc"},
		},
		Limit:   50,
		Columns: []string{"last", "dbLevel", "dbType", "dbPct", "dbAge", "chgDay", "rvol", "vol", "news"},
		Alert:   core.AlertConfig{Enabled: false, On: core.AlertOnEnter, Sound: "ping", CooldownSec: 120, RequireReentry: true, Speak: true},
	}}
}

// Seeded "PDL Break" scan: its ID and its own schema_meta key.
const (
	pdlBreakSeedKey = "seeded.pdlbreak.v1"
	pdlBreakID      = "pdl-break"
)

// pdlBreakScans: stocks over $25 that traded at/above the previous day's low
// and broke below it today (pre-market included) and are still below
// (internal/dlevel LowTracker, PDL from daily_close_history). pdlPct > 0 only
// matches a symbol with a live break (Null fails numerics). Sort: smallest %
// below the PDL first. The alert is ON by default (the user asked for it):
// enter + re-entry, so one alert per break per symbol; Speak says "SYM: PDL
// break" (web e9.ts spokenScanName). Seeded once, never resurrected.
func pdlBreakScans() []core.ScanDef {
	return []core.ScanDef{{
		ID: pdlBreakID, Name: "PDL Break", Enabled: true,
		Filter: core.Filter{Op: "and", Conds: []core.Filter{
			{Field: core.FieldLast, Cmp: core.CmpGT, Value: 25},
			{Field: core.FieldAvgVol, Cmp: core.CmpGTE, Value: 500000},
			{Field: core.FieldPdlPct, Cmp: core.CmpGT, Value: 0},
		}},
		Sort:    []core.SortKey{{Field: core.FieldPdlPct, Dir: "asc"}},
		Limit:   50,
		Columns: []string{"last", "pdlLevel", "pdlPct", "pdlAge", "chgDay", "rvol", "vol", "news"},
		Alert:   core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: "ping", CooldownSec: 30, RequireReentry: true, Speak: true},
	}}
}

// The seeded 9EMA VWAP CROSS scans: their IDs and their OWN schema_meta keys
// (one per scan, so deleting one never brings it back and never touches the other).
const (
	emaVwapBullSeedKey = "seeded.emavwap.bull.v1"
	emaVwapBearSeedKey = "seeded.emavwap.bear.v1"
	emaVwapBullID      = "vwap-bull"
	emaVwapBearID      = "vwap-bear"
	emaVwapFreshS      = 240 // a cross's row stays on its scan for four minutes after the 1-minute bar that fired closed
	emaVwapCooldownSec = 120
	emaVwapVolFloorKey = "migrated.emavwap.volfloor.v1"
	emaVwapMinBarDvol  = 500000 // the cross 1-minute bar's $ volume
	emaVwapMinSessVol  = 500000 // today's cumulative shares
)

// emaVwapBaseConds: the original (pre-volume-floor) seeded filter.
func emaVwapBaseConds(ageField string) []core.Filter {
	return []core.Filter{
		{Field: core.FieldLast, Cmp: core.CmpGT, Value: 25},
		{Field: core.FieldAvgVol, Cmp: core.CmpGTE, Value: 1000000},
		{Field: core.FieldRvol, Cmp: core.CmpGTE, Value: 1.5},
		{Field: ageField, Cmp: core.CmpLTE, Value: emaVwapFreshS},
	}
}

// emaVwapVolFloorConds: RVOL is easy to exceed in the thin early pre-market, so the
// cross must also carry real volume: bar $ volume, cumulative shares, and regular hours.
func emaVwapVolFloorConds() []core.Filter {
	return []core.Filter{
		{Field: core.FieldEvBarDvol, Cmp: core.CmpGTE, Value: emaVwapMinBarDvol},
		{Field: core.FieldEvSessVol, Cmp: core.CmpGTE, Value: emaVwapMinSessVol},
		{Field: core.FieldSession, Cmp: core.CmpEq, Value: "regular"},
	}
}

// migrateEmaVwapVolFloor adds the volume floor to the seeded vwap-bull / vwap-bear
// scans once (own meta key), only where the filter is still exactly the original
// seed; an edited scan is left alone and logged. A failed save retries next boot.
func migrateEmaVwapVolFloor(
	ctx context.Context,
	getMeta func(context.Context, string) (string, error),
	setMeta func(context.Context, string, string) error,
	list func(context.Context) ([]core.ScanDef, error),
	save func(context.Context, core.ScanDef) error,
	install func(core.ScanDef),
) {
	if v, err := getMeta(ctx, emaVwapVolFloorKey); err != nil || v != "" {
		return
	}
	defs, err := list(ctx)
	if err != nil {
		logx.L().Warn("vwap volume floor migration: listing scans failed", "err", err)
		return
	}
	for _, d := range defs {
		var age string
		switch d.ID {
		case emaVwapBullID:
			age = core.FieldEvBullAge
		case emaVwapBearID:
			age = core.FieldEvBearAge
		default:
			continue
		}
		orig := core.Filter{Op: "and", Conds: emaVwapBaseConds(age)}
		a, err1 := json.Marshal(orig)
		b, err2 := json.Marshal(d.Filter)
		if err1 != nil || err2 != nil || string(a) != string(b) {
			logx.L().Info("vwap volume floor migration: scan filter was edited, left as is", "id", d.ID)
			continue
		}
		d.Filter.Conds = append(d.Filter.Conds, emaVwapVolFloorConds()...)
		for _, c := range []string{core.FieldEvBarDvol, core.FieldEvSessVol} {
			if !slices.Contains(d.Columns, c) {
				d.Columns = append(d.Columns, c)
			}
		}
		if err := save(ctx, d); err != nil {
			logx.L().Warn("vwap volume floor migration: save failed", "id", d.ID, "err", err)
			return
		}
		install(d)
		logx.L().Info("VWAP cross scan now requires cross bar $ volume, day volume and regular hours", "id", d.ID)
	}
	_ = setMeta(ctx, emaVwapVolFloorKey, "1")
}

// emaVwapScan: large caps (last > $25, average volume >= 1M, RVOL >= 1.5) whose
// 9 EMA of 1-minute closes crossed the session VWAP in the last four minutes
// (engine emavwap.go). Alert on enter, Speak "SYM: VWAP bull|bear cross" (web
// e9.ts spokenScanName). A cross fires once, so a row enters once per cross.
func emaVwapScan(id, name, sound, ageField string) core.ScanDef {
	return core.ScanDef{
		ID: id, Name: name, Enabled: true,
		Filter:  core.Filter{Op: "and", Conds: append(emaVwapBaseConds(ageField), emaVwapVolFloorConds()...)},
		Sort:    []core.SortKey{{Field: ageField, Dir: "asc"}},
		Limit:   50,
		Columns: []string{"last", core.FieldEvEma, core.FieldEvVwap, core.FieldEvDist, "rvol", "avgVol", core.FieldEvBarDvol, core.FieldEvSessVol, ageField, "chgDay"},
		Alert:   core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: sound, CooldownSec: emaVwapCooldownSec, Speak: true},
	}
}

func emaVwapBullScans() []core.ScanDef {
	return []core.ScanDef{emaVwapScan(emaVwapBullID, "VWAP Bull Cross", "chime", core.FieldEvBullAge)}
}

func emaVwapBearScans() []core.ScanDef {
	return []core.ScanDef{emaVwapScan(emaVwapBearID, "VWAP Bear Cross", "alert", core.FieldEvBearAge)}
}
