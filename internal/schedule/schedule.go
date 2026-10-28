// Package schedule runs the nightly maintenance jobs: it flushes the engine's
// captured cumulative-volume curves to history, recomputes the RVOL baseline from
// the trailing window and hot-swaps it into the engine, prunes old curves, and
// refreshes the float reference weekly. ET-aware; fires shortly after the 20:00
// session close (with a startup catch-up if a run was missed).
package schedule

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	_ "time/tzdata"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/feed"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
	"github.com/eelkonkoopman/marketscanner/internal/pattern"
	"github.com/eelkonkoopman/marketscanner/internal/trend"
	"github.com/eelkonkoopman/marketscanner/internal/universe"
)

// Engine is the narrow slice of the engine the scheduler needs.
type Engine interface {
	Curves(lastBucket int) map[string][]int64
	SetBaseline(*core.BaselineTable)
	Baseline() *core.BaselineTable
	SetFloats(map[string]core.FloatRef)
}

// FloatSource fetches share-structure reference (satisfied by *feed.Client).
type FloatSource interface {
	Floats(ctx context.Context, syms []string) ([]core.FloatRef, error)
}

// DailySource fetches one date's grouped daily OHLCV bars for the whole market
// (satisfied by *feed.Client).
type DailySource interface {
	GroupedDaily(ctx context.Context, date string) (map[string]core.DailyBar, error)
	// HasKey reports whether a Massive API key is configured. The trend job
	// gates on this: an empty-key boot (an explicitly supported flow — the UI
	// can set the key later) would otherwise burn a whole backfill's worth of
	// requests as guaranteed 401s, on the un-paced error path, for nothing.
	HasKey() bool
}

type Scheduler struct {
	b    core.BaselineRepo
	f    core.FloatRepo
	c    core.CalendarRepo
	eng  Engine
	fsrc FloatSource
	uni  *universe.Universe

	loc        *time.Location
	now        func() time.Time
	window     int           // trailing sessions for the baseline mean
	floatEvery time.Duration // float considered stale after this
	floatCap   int           // max float fetches per run

	status atomic.Pointer[string] // current baseline status for the UI

	// bmu serializes baseline rebuilds: the scheduler's own loop and the
	// rvolfill worker (RefreshBaselineFor) both recompute and swap.
	bmu sync.Mutex

	// Incremental curve saves (flushToday): the ET day being saved and the
	// first bucket the next save must send (earlier ones are already stored).
	flushDay  string
	flushFrom int

	// Daily-trend job (opt-in via EnableDailyTrend; no-ops when unset).
	trepo   core.TrendRepo
	dsrc    DailySource
	onTrend func(map[string]core.TrendRef)
	// Bull-flag detection rides that same job (opt-in via EnableDailyFlags).
	onFlags func(map[string]core.FlagRef)
}

func New(b core.BaselineRepo, f core.FloatRepo, c core.CalendarRepo, eng Engine, fsrc FloatSource, uni *universe.Universe) *Scheduler {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.UTC
	}
	return &Scheduler{
		b: b, f: f, c: c, eng: eng, fsrc: fsrc, uni: uni,
		loc:        loc,
		now:        time.Now,
		window:     20,
		floatEvery: 7 * 24 * time.Hour,
		floatCap:   1000,
	}
}

// BaselineStatus returns the latest baseline status ("building" | "ready") for
// the status frame.
func (s *Scheduler) BaselineStatus() string {
	if p := s.status.Load(); p != nil {
		return *p
	}
	return "building"
}

func (s *Scheduler) setStatus(v string) { s.status.Store(&v) }

// EnableDailyTrend turns on the daily-chart trend job: backfill missing daily
// closes at startup, refresh nightly, classify, and hand the result to onSwap
// (which pushes it into the scanner and the symbol panel's lookup).
func (s *Scheduler) EnableDailyTrend(repo core.TrendRepo, src DailySource, onSwap func(map[string]core.TrendRef)) {
	s.trepo, s.dsrc, s.onTrend = repo, src, onSwap
}

// EnableDailyFlags turns on bull-flag / tight-consolidation detection. It RIDES
// the daily-trend job — same nightly (and startup-backfill) pass, same bars
// already loaded for the classifier — so it needs no repo, no source, and adds
// neither a query nor an API call; it does nothing until EnableDailyTrend has
// been called too.
//
// onSwap receives one entry per symbol that HAS a setup; symbols without one
// are simply absent, which the scanner overlay reads as Null. A pass where
// nothing sets up publishes an EMPTY (non-nil) map on purpose: that is a real
// "no setups tonight" verdict and must clear yesterday's, unlike the nil map
// SetDailyFlags ignores (which is how a failed load leaves the last good one).
func (s *Scheduler) EnableDailyFlags(onSwap func(map[string]core.FlagRef)) {
	s.onFlags = onSwap
}

func (s *Scheduler) Run(ctx context.Context) error {
	if err := s.ensureCalendar(ctx); err != nil {
		logx.L().Warn("calendar init failed", "err", err)
	}
	// Initialize status from whatever's already persisted.
	if st, _, err := s.b.GetBaselineStatus(ctx); err == nil && st != "" {
		s.setStatus(st)
	}
	// maybeCatchUp runs FIRST and may itself perform the full daily-trend
	// refresh (RunOnce step 5, when booting post-20:05 ET with today's curve
	// missing). Launching the backfill goroutine only after it returns means
	// initDailyTrend's HasClosesForDate checks see that work already done and
	// skip it (force=false), instead of two goroutines racing the same
	// GroupedDaily calls against the same per-key REST budget.
	s.maybeCatchUp(ctx)
	if s.trepo != nil && s.dsrc.HasKey() {
		go s.initDailyTrend(ctx)
	}
	// The nightly job only runs if the app is open at 20:05 ET; recompute here
	// when the baseline doesn't yet include the previous session.
	s.ensureBaselineFresh(ctx)

	flushTk := time.NewTicker(curveFlushEvery)
	defer flushTk.Stop()
	for {
		now := s.now()
		d := s.nextFire(now).Sub(now)
		if d < time.Second {
			d = time.Second
		}
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			// Last save on the way out: the user closes the app well before the
			// 20:05 ET nightly job. Incremental, so it is small and quick.
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			s.flushToday(fctx)
			cancel()
			return nil
		case <-flushTk.C:
			t.Stop()
			s.flushToday(ctx)
			s.ensureBaselineFresh(ctx) // picks up a day rollover while running
		case <-t.C:
			if err := s.RunOnce(ctx, s.tradeDate(s.now())); err != nil {
				logx.L().Warn("nightly run failed", "err", err)
			}
		}
	}
}

// RunOnce performs the full nightly job for a trade date.
func (s *Scheduler) RunOnce(ctx context.Context, tradeDate string) error {
	_ = s.ensureCalendar(ctx)

	lastBucket, trading := core.BucketCount-1, true
	if days, err := s.c.Sessions(ctx); err == nil {
		for _, d := range days {
			if d.Date == tradeDate {
				lastBucket, trading = d.LastBucket, d.IsTrading
				break
			}
		}
	}
	if !trading {
		return nil
	}

	// 1. Flush the engine's captured cumulative curves into history.
	if curves := s.eng.Curves(lastBucket); len(curves) > 0 {
		if err := s.b.UpsertVolumeCurves(ctx, tradeDate, curves); err != nil {
			return fmt.Errorf("flush curves: %w", err)
		}
	}

	// 2. Recompute the baseline from the trailing window and hot-swap it in.
	if err := s.recomputeThrough(ctx, tradeDate); err != nil {
		return err
	}

	// 3. Prune old curves (~45 calendar days ≈ 30 trading days of headroom).
	if del, perr := s.b.PruneVolumeCurves(ctx, isoMinusDays(tradeDate, 45)); perr == nil && del > 0 {
		logx.L().Info("pruned old volume curves", "deleted", del)
	}

	// 4. Weekly float refresh (self-limited to stale tickers, capped per run).
	s.refreshFloats(ctx)

	// 5. Daily-trend refresh. The nightly pass refetches the ENTIRE window,
	// not just today: adjusted=true bars are adjusted as of the FETCH date,
	// so incrementally-appended history drifts after a split (a 10:1 split
	// would leave old unadjusted rows next to new adjusted ones — a fake 90%
	// cliff the classifier would read as a downtrend). ~30 grouped calls once
	// per night keeps the whole window split-consistent.
	if s.trepo != nil && s.dsrc.HasKey() {
		dates := append(s.recentTradingDates(ctx, tradeDate, trendWindowDays-1), tradeDate)
		s.refreshDailyCloses(ctx, dates, true)
		if del, err := s.trepo.PruneDailyCloses(ctx, isoMinusDays(tradeDate, 45)); err == nil && del > 0 {
			logx.L().Info("pruned old daily closes", "deleted", del)
		}
		s.recomputeTrend(ctx)
	}
	return nil
}

// recomputeThrough is RunOnce's baseline step: recompute over the window ending
// at tradeDate (inclusive) and hot-swap it in, holding bmu only for that.
func (s *Scheduler) recomputeThrough(ctx context.Context, tradeDate string) error {
	s.bmu.Lock()
	defer s.bmu.Unlock()
	s.setStatus("building")
	_ = s.b.SetBaselineStatus(ctx, "building", tradeDate)
	window, err := s.b.TrailingTradingDays(ctx, tradeDate, s.window)
	if err != nil {
		return fmt.Errorf("trailing days: %w", err)
	}
	rows, err := s.b.RecomputeBaseline(ctx, tradeDate, window)
	if err != nil {
		return fmt.Errorf("recompute baseline: %w", err)
	}
	if bt, err := s.b.LoadBaseline(ctx); err == nil {
		s.eng.SetBaseline(bt)
	}
	stored := s.storedSessions(ctx, window)
	status := baselineStatusFor(stored)
	_ = s.b.SetBaselineStatus(ctx, status, tradeDate)
	s.setStatus(status)
	logx.L().Info("rvol baseline recomputed", "date", tradeDate, "rows", rows, "window", len(window), "storedSessions", stored, "status", status)
	return nil
}

// dailyCloseFetchPace throttles GroupedDaily calls so a ~25-32 call backfill
// or nightly refresh never competes with the news poller's 1-second Benzinga
// cadence for the same per-key REST budget — both go through the same
// *feed.Client. Applied BEFORE every attempt (success or failure), not just
// after a success: an error path that skipped it would let a stretch of
// failures (e.g. an active rate limit) fire back-to-back at network-RTT
// speed, compounding the exact throttle it should be easing off of.
const dailyCloseFetchPace = 250 * time.Millisecond

// refreshDailyCloses fetches grouped closes for the given dates. force=false
// skips dates already stored (startup backfill); force=true refetches
// everything (nightly split-consistency pass). Aborts the whole call — not
// just the current date — on the first rate-limit response: the news poller
// shares this REST budget and its own 429 backoff runs up to 2 minutes, so a
// throttle tripped here should stop spending requests immediately rather than
// grind through the remaining queue.
func (s *Scheduler) refreshDailyCloses(ctx context.Context, dates []string, force bool) {
	fetched := 0
	for i, d := range dates {
		if i > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(dailyCloseFetchPace):
			}
		}
		if ctx.Err() != nil {
			return
		}
		if !force {
			if has, err := s.trepo.HasClosesForDate(ctx, d); err != nil || has {
				continue
			}
		}
		bars, err := s.dsrc.GroupedDaily(ctx, d)
		if err != nil {
			if feed.IsRateLimitErr(err) {
				logx.L().Warn("grouped daily rate-limited; stopping this run early to protect the news feed's shared budget",
					"date", d, "remaining", len(dates)-i-1)
				return
			}
			logx.L().Warn("grouped daily fetch failed", "date", d, "err", err)
			continue
		}
		if len(bars) == 0 {
			continue // holiday mislabeled as trading in the synthetic calendar
		}
		if err := s.trepo.UpsertDailyBars(ctx, d, bars); err != nil {
			logx.L().Warn("daily bars upsert failed", "date", d, "err", err)
			continue
		}
		fetched++
	}
	if fetched > 0 {
		logx.L().Info("daily close history refreshed", "days", fetched)
	}
}

// initDailyTrend backfills any missing recent trading days of closes (~25
// grouped calls on a fresh database, zero once warm) and publishes the first
// classification — trend AND flags, since both come out of recomputeTrend, so a
// fresh boot has the columns populated without waiting for 20:05 ET. Runs async
// off Run so a slow backfill never delays startup.
func (s *Scheduler) initDailyTrend(ctx context.Context) {
	today := s.now().In(s.loc).Format("2006-01-02")
	s.refreshDailyCloses(ctx, s.recentTradingDates(ctx, today, trendWindowDays), false)
	s.recomputeTrend(ctx)
}

// trendWindowDays: trading days of history to keep classifying against.
// 32 > the classifier's 25-close full window, with slack for data gaps.
const trendWindowDays = 32

// recentTradingDates returns the last n trading dates strictly BEFORE today
// (today's grouped bar is only final after the close; the nightly job adds it).
func (s *Scheduler) recentTradingDates(ctx context.Context, today string, n int) []string {
	days, err := s.c.Sessions(ctx)
	if err != nil {
		return nil
	}
	var dates []string
	for _, d := range days {
		if d.IsTrading && d.Date < today {
			dates = append(dates, d.Date)
		}
	}
	if len(dates) > n {
		dates = dates[len(dates)-n:]
	}
	return dates
}

// recomputeTrend classifies every stored symbol from ONE load of the daily bar
// history and publishes the results to the scanner. Both consumers ride that
// single load: the trend classifier (which needs closes only) and, when
// enabled, the flag detector (which needs the full OHLCV). Nightly cadence and
// the startup backfill both land here — geometry is never computed on the tick
// path, only injected into it via the overlay maps published below.
func (s *Scheduler) recomputeTrend(ctx context.Context) {
	if s.trepo == nil || s.onTrend == nil {
		return
	}
	today := s.now().In(s.loc).Format("2006-01-02")
	dates := s.recentTradingDates(ctx, today, trendWindowDays)
	if len(dates) == 0 {
		return
	}
	byTicker, err := s.trepo.LoadBars(ctx, dates[0])
	if err != nil {
		logx.L().Warn("trend recompute: load bars failed", "err", err)
		return
	}
	// AsOf must reflect DATA recency, not wall-clock "today": at an intraday,
	// weekend, or post-holiday-skip boot, the newest close on hand can be a
	// prior session even though "today" has already turned over. The panel
	// chip states this date directly ("N sessions to {asOf}").
	asOf, err := s.trepo.MaxDate(ctx)
	if err != nil || asOf == "" {
		asOf = today // no stored data at all yet (e.g. first backfill call failed) — best effort
	}
	out := make(map[string]core.TrendRef, len(byTicker))
	// Most symbols are not setting up, so this map stays small — no presize.
	flags := make(map[string]core.FlagRef)
	up, flat, down := 0, 0, 0
	for sym, bars := range byTicker {
		// Flag geometry, on the bars ALREADY loaded above. Deliberately not
		// behind the classifier's !ok below: the two gate on different things
		// (Classify wants 15 closes and positive prices, Analyze 20 bars and an
		// admissible base), so neither verdict may suppress the other. Analyze
		// takes no asOf — it stamps the newest bar's own date.
		if s.onFlags != nil {
			if fref, ok := pattern.Analyze(bars); ok {
				flags[sym] = fref
			}
		}
		// The classifier takes closes only; the loader returns full bars (one
		// query serves both consumers), so project them here.
		closes := make([]float64, len(bars))
		for i, b := range bars {
			closes[i] = b.C
		}
		ref, ok := trend.Classify(closes, asOf)
		if !ok {
			continue
		}
		out[sym] = ref
		switch ref.DTrend {
		case 1:
			up++
		case -1:
			down++
		default:
			flat++
		}
	}
	s.onTrend(out)
	logx.L().Info("daily trend classified", "up", up, "flat", flat, "down", down,
		"unknown", len(byTicker)-len(out))
	if s.onFlags != nil {
		s.onFlags(flags)
		logx.L().Info("daily flags detected", "setups", len(flags), "scanned", len(byTicker))
	}
}

func (s *Scheduler) refreshFloats(ctx context.Context) {
	syms := s.universeSyms()
	if len(syms) == 0 {
		return
	}
	cutoff := s.now().Add(-s.floatEvery).Unix()
	stale, err := s.f.StaleTickers(ctx, cutoff, syms)
	if err != nil || len(stale) == 0 {
		return
	}
	if len(stale) > s.floatCap {
		stale = stale[:s.floatCap]
	}
	refs, err := s.fsrc.Floats(ctx, stale)
	if err != nil && len(refs) == 0 {
		logx.L().Warn("float refresh failed", "err", err)
		return
	}
	if len(refs) == 0 {
		return
	}
	if err := s.f.UpsertFloats(ctx, refs); err != nil {
		logx.L().Warn("float upsert failed", "err", err)
		return
	}
	if floats, err := s.f.Floats(ctx); err == nil {
		s.eng.SetFloats(floats)
	}
	logx.L().Info("float reference refreshed", "updated", len(refs))
}

// ensureCalendar fills a weekday trading calendar around "now" (holidays/half-days
// are a future refinement via a market-calendar feed).
func (s *Scheduler) ensureCalendar(ctx context.Context) error {
	now := s.now().In(s.loc)
	start := now.AddDate(0, 0, -40)
	end := now.AddDate(0, 0, 90)
	var days []core.MarketDay
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		wd := d.Weekday()
		days = append(days, core.MarketDay{
			Date:        d.Format("2006-01-02"),
			IsTrading:   wd != time.Saturday && wd != time.Sunday,
			SessOpenMin: 240, RegOpenMin: 570, RegCloseMin: 960, SessCloseMin: 1200,
			LastBucket: core.BucketCount - 1,
		})
	}
	return s.c.Upsert(ctx, days)
}

// maybeCatchUp runs the nightly job at startup if it's already past the fire time
// today and today's curve hasn't been captured yet.
func (s *Scheduler) maybeCatchUp(ctx context.Context) {
	n := s.now().In(s.loc)
	if n.Hour()*60+n.Minute() < 20*60+5 {
		return
	}
	if wd := n.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return
	}
	td := n.Format("2006-01-02")
	if has, _ := s.b.HasCurveForDate(ctx, td); has {
		return
	}
	logx.L().Info("nightly catch-up", "date", td)
	if err := s.RunOnce(ctx, td); err != nil {
		logx.L().Warn("catch-up failed", "err", err)
	}
}

// curveFlushEvery is how often today's curve is saved while the app runs.
const curveFlushEvery = 10 * time.Minute

// baselineReadyMin mirrors the engine's minBaselineSessions: below this many
// stored sessions RVOL is null everywhere, so the UI must say "building".
const baselineReadyMin = 5

func baselineStatusFor(storedSessions int) string {
	if storedSessions >= baselineReadyMin {
		return "ready"
	}
	return "building"
}

// storedSessions counts the window dates that actually have curves saved.
func (s *Scheduler) storedSessions(ctx context.Context, window []string) int {
	n := 0
	for _, d := range window {
		if has, err := s.b.HasCurveForDate(ctx, d); err == nil && has {
			n++
		}
	}
	return n
}

// isTradingDay consults the market calendar, falling back to weekdays.
func (s *Scheduler) isTradingDay(ctx context.Context, day time.Time) bool {
	date := day.Format("2006-01-02")
	if days, err := s.c.Sessions(ctx); err == nil {
		for _, d := range days {
			if d.Date == date {
				return d.IsTrading
			}
		}
	}
	wd := day.Weekday()
	return wd != time.Saturday && wd != time.Sunday
}

// flushToday saves today's curve buckets observed since the last save, so a
// day survives the app closing before the 20:05 ET nightly job (the user's
// normal pattern: until this existed, almost no day was ever saved and RVOL
// never got its 5 sessions). Incremental, so the shutdown save finishes well
// inside run.bat's force-kill grace. Only 04:05-20:00 ET on trading days:
// before the engine's 04:00 reset its curve still holds yesterday (or a stale
// pre-open snapshot seed) and must not be stamped with today's date. The repo
// merges with MAX, so re-sending the bucket in progress is harmless.
func (s *Scheduler) flushToday(ctx context.Context) {
	now := s.now().In(s.loc)
	m := now.Hour()*60 + now.Minute()
	if m < 4*60+5 || m >= 20*60 || !s.isTradingDay(ctx, now) {
		return
	}
	day := now.Format("2006-01-02")
	if day != s.flushDay {
		s.flushDay, s.flushFrom = day, 0
	}
	bucket := min((m-4*60)/5, core.BucketCount-1)
	curves := s.eng.Curves(bucket)
	for _, cv := range curves { // Curves returns copies: safe to blank what's already saved
		for i := 0; i < s.flushFrom && i < len(cv); i++ {
			cv[i] = 0
		}
	}
	if err := s.b.UpsertVolumeCurves(ctx, day, curves); err != nil {
		logx.L().Warn("curve save failed; will resend next time", "err", err)
		return
	}
	s.flushFrom = bucket // re-send this one next time: it was still filling
}

// ensureBaselineFresh recomputes the RVOL baseline when it doesn't yet include
// the previous trading session. The window ends at that session, never today:
// today's partial curve must not feed today's own baseline.
func (s *Scheduler) ensureBaselineFresh(ctx context.Context) {
	s.bmu.Lock()
	defer s.bmu.Unlock()
	window, err := s.baselineWindow(ctx)
	if err != nil || len(window) == 0 {
		if err != nil {
			logx.L().Warn("baseline refresh: trailing days", "err", err)
		}
		return
	}
	prev := window[0]
	if _, forDate, err := s.b.GetBaselineStatus(ctx); err == nil && forDate >= prev {
		return // already includes the previous session (nightly job or an earlier start)
	}
	rows, err := s.b.RecomputeBaseline(ctx, prev, window)
	if err != nil {
		logx.L().Warn("baseline refresh failed", "err", err)
		return
	}
	if bt, err := s.b.LoadBaseline(ctx); err == nil {
		s.eng.SetBaseline(bt)
	}
	stored := s.storedSessions(ctx, window)
	status := baselineStatusFor(stored)
	_ = s.b.SetBaselineStatus(ctx, status, prev)
	s.setStatus(status)
	logx.L().Info("rvol baseline refreshed at startup", "through", prev, "rows", rows, "storedSessions", stored, "status", status)
}

// RefreshBaselineFor recomputes just these tickers' baseline over the current
// window and swaps in a copy of the live table with their rows replaced:
// internal/rvolfill calls it after writing rebuilt curves for past days. Only
// the touched tickers are recomputed because a whole-market recompute over a
// full window (~11k tickers x 20 days) takes long and holds the database.
func (s *Scheduler) RefreshBaselineFor(ctx context.Context, tickers []string) {
	if len(tickers) == 0 {
		return
	}
	s.bmu.Lock()
	defer s.bmu.Unlock()
	window, err := s.baselineWindow(ctx)
	if err != nil || len(window) == 0 {
		return
	}
	part, err := s.b.RecomputeBaselineFor(ctx, window[0], window, tickers)
	if err != nil {
		logx.L().Warn("baseline refresh for rebuilt tickers failed", "err", err)
		return
	}
	s.eng.SetBaseline(s.eng.Baseline().With(part, tickers))
	stored := s.storedSessions(ctx, window)
	status := baselineStatusFor(stored)
	if _, forDate, err := s.b.GetBaselineStatus(ctx); err == nil {
		_ = s.b.SetBaselineStatus(ctx, status, forDate)
	}
	s.setStatus(status)
}

// BaselineDays returns the trading days the baseline averages right now (the
// trailing window ending at the previous session, never today), most recent
// first, with their calendar rows.
func (s *Scheduler) BaselineDays(ctx context.Context) ([]core.MarketDay, error) {
	window, err := s.baselineWindow(ctx)
	if err != nil || len(window) == 0 {
		return nil, err
	}
	byDate := map[string]core.MarketDay{}
	if days, err := s.c.Sessions(ctx); err == nil {
		for _, d := range days {
			byDate[d.Date] = d
		}
	}
	out := make([]core.MarketDay, 0, len(window))
	for _, d := range window {
		md, ok := byDate[d]
		if !ok { // calendar row missing: assume a full day
			md = core.MarketDay{Date: d, IsTrading: true, SessOpenMin: 240, RegOpenMin: 570,
				RegCloseMin: 960, SessCloseMin: 1200, LastBucket: core.BucketCount - 1}
		}
		out = append(out, md)
	}
	return out, nil
}

// baselineWindow is the trailing window ending at the previous trading session.
func (s *Scheduler) baselineWindow(ctx context.Context) ([]string, error) {
	today := s.tradeDate(s.now())
	prevs, err := s.b.TrailingTradingDays(ctx, isoMinusDays(today, 1), 1)
	if err != nil || len(prevs) == 0 {
		return nil, err
	}
	return s.b.TrailingTradingDays(ctx, prevs[0], s.window)
}

func (s *Scheduler) nextFire(now time.Time) time.Time {
	n := now.In(s.loc)
	fire := time.Date(n.Year(), n.Month(), n.Day(), 20, 5, 0, 0, s.loc)
	if !fire.After(n) {
		fire = fire.AddDate(0, 0, 1)
	}
	return fire
}

func (s *Scheduler) tradeDate(now time.Time) string { return now.In(s.loc).Format("2006-01-02") }

func (s *Scheduler) universeSyms() []string {
	set := s.uni.Set()
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

func isoMinusDays(date string, n int) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return t.AddDate(0, 0, -n).Format("2006-01-02")
}
