// Package prefill backfills the frozen prior-session review slots (the pre*
// and rth* scan fields) from Massive minute bars when the process MISSED the
// live session boundary: an app started mid-premarket has only a partial pre
// slot at 09:30, and one started after the open has none at all, which leaves
// the seeded "Pre-Market Movers" / "PM Gap & Fade" review scans empty for the
// whole day.
//
// There is no bulk endpoint for this — Intraday is one REST call PER SYMBOL —
// so the worker is deliberately frugal with the shared REST budget (the same
// per-key budget the news poller spends at a 1-second cadence; see the
// dailyCloseFetchPace house rules in internal/schedule/schedule.go):
//
//   - a CAPPED candidate list (~300 symbols) ranked from the whole-market
//     snapshot the app fetches anyway, unioned with the scans' active symbols;
//   - at most ONE run per ET day, and only when the boundary was missed;
//   - launches paced (the schedule.go 250ms precedent) under bounded
//     concurrency (sem 4 — the floats.go sem-8 precedent, halved because this
//     can run while the market is open and the ingest WS is busy);
//   - the FIRST 429 aborts the entire run — the news poller's own 429 backoff
//     runs up to 2 minutes, so a tripped throttle must stop spending requests
//     immediately, not grind through the remaining queue — and the run is NOT
//     retried until the next ET day (when the live freeze covers it anyway).
//
// Stats are handed to Engine.SeedFrozen, which derives chg/gap/fade from its
// own per-ticker prevClose through the SAME helper the live boundary freeze
// uses, so the two paths cannot drift. Disable with SCANNER_PREFILL_CAP=0.
package prefill

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	_ "time/tzdata" // embed tz DB so America/New_York resolves on Windows

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/feed"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

// Config wires the worker to the feed, the scanner and the engine. Seeds,
// Bars and Apply are required; New disables the worker (with a log line)
// rather than panicking if one is missing, same as flagread.
type Config struct {
	// Seeds is feed.Client.Snapshot: the whole-market snapshot (ONE REST
	// call) the candidate ranking is computed from.
	Seeds func(ctx context.Context) ([]core.Seed, error)
	// Bars is feed.Client.Intraday: today's full-day 1-minute bars for one
	// symbol, extended hours included (one REST call PER SYMBOL — the reason
	// everything here is capped).
	Bars func(ctx context.Context, sym string) ([]core.Bar, error)
	// Active is scan.Scanner.ActiveSyms: every symbol today's scans have
	// surfaced. Unioned with the ranked candidates so a symbol the user is
	// actually looking at is not left out by the cap — though the union
	// itself is capped at Cap extras (see Cap) to keep the REST spend bounded
	// even when a scan matches thousands of symbols.
	Active func() map[string]struct{}
	// Apply is engine.Engine.SeedFrozen (via core.Engine): overwrites the
	// frozen slot for every provided symbol, skips untracked ones.
	Apply func(which core.Session, stats map[string]core.SessionStats)

	// SeedVwap (optional) is engine.Engine.SeedScopeVwap: every symbol whose
	// bars are fetched (here or by the reconnect re-seed, see Reseed) has its
	// current-scope VWAP and 1-minute EMA seeded from them.
	SeedVwap func(sym string, bars []core.Bar)
	// Extra (optional) is engine.Engine.EmvCandidates: symbols that can reach
	// the 9EMA VWAP cross universe, appended (capped at Cap) to the fetch list
	// when SeedVwap is set, so they get a true session VWAP after a restart.
	Extra func() []string

	// Cap bounds the RANKED candidate list AND the active-set union: at most
	// Cap ranked symbols plus Cap active extras are fetched (worst case
	// 2*Cap Intraday calls + 1 snapshot per run), so a user scan matching
	// thousands of symbols can never unbound the REST spend. 0 reads
	// SCANNER_PREFILL_CAP (default 300); an explicit env value of 0 disables
	// the worker entirely.
	Cap int

	AppStart time.Time        // process boot time — decides IF and WHEN to run
	Now      func() time.Time // injectable clock (tests); defaults to time.Now
}

// House numbers. All ET minutes-of-day.
const (
	// liveOkBeforeMin: an app already running before ~04:05 ET saw (almost)
	// the whole pre-market live — the tick-accurate live freeze is strictly
	// better than a bar-derived backfill, so the worker never runs.
	liveOkBeforeMin = 4*60 + 5 // 04:05
	// lateStartMin: past 09:31 the pre boundary is unambiguously missed.
	lateStartMin = 9*60 + 31 // 09:31
	// postStartMin: past 16:05 the rth boundary is missed too.
	postStartMin = 16*60 + 5 // 16:05

	preLoMin = 4 * 60 // pre window [04:00, 09:30) — bar START minute
	preHiMin = 9*60 + 30
	rthHiMin = 16 * 60 // rth window [09:30, 16:00); a bar stamped exactly
	// 09:30 belongs to rth (it aggregates trades from 09:30:00 onward).

	// concurrency / pacing: sem 4 in-flight (half of floats.go's 8 — this can
	// run during market hours) with launches paced at the schedule.go 250ms
	// precedent, so ~300 calls spread over ~75s instead of hammering the
	// budget the news poller polls every second.
	fetchConcurrency = 4
	defaultPace      = 250 * time.Millisecond

	// startupDelay: when the run is due "now" (mid-day or evening start),
	// wait for the startup snapshot/universe/scan restore to land first.
	startupDelay = 90 * time.Second
)

// Worker runs the capped, once-per-ET-day frozen-slot backfill.
type Worker struct {
	cfg     Config
	loc     *time.Location
	pace    time.Duration // between fetch launches; shortened in tests
	enabled bool

	mu   sync.Mutex
	day  string   // ET date of the last run attempt — the once-per-day latch
	last runStats // counters from the last run (tests assert on these)

	kick     chan struct{} // Reseed requests (buffer 1, coalesced)
	lastVwap time.Time     // start of the latest fetch that seeded VWAPs
}

// runStats are the counters of one run — asserted by tests directly (never
// via log text) and echoed in the summary log line.
type runStats struct {
	candidates int  // symbols selected for fetching
	fetched    int  // Bars calls that completed and were aggregated
	failed     int  // per-symbol non-429 failures (skipped, not fatal)
	aborted    bool // a 429 stopped the run early
	applied    int  // symbols that produced at least one window's stats
}

func New(cfg Config) *Worker {
	if cfg.Cap == 0 {
		cfg.Cap = envCap("SCANNER_PREFILL_CAP", 300)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.UTC
	}
	w := &Worker{cfg: cfg, loc: loc, pace: defaultPace, enabled: cfg.Cap > 0,
		kick: make(chan struct{}, 1)}
	if cfg.Seeds == nil || cfg.Bars == nil || cfg.Apply == nil {
		// A missing dependency is a wiring bug; stay disabled and say so
		// rather than panic in a goroutine at 09:32 (the flagread convention).
		logx.L().Warn("session prefill disabled: incomplete config")
		w.enabled = false
	}
	return w
}

// Run waits for the planned fire time (if any), performs the single backfill
// run, then parks until shutdown.
func (w *Worker) Run(ctx context.Context) error {
	if !w.enabled {
		logx.L().Info("session prefill disabled (SCANNER_PREFILL_CAP=0 or incomplete config)")
		<-ctx.Done()
		return nil
	}
	if w.cfg.SeedVwap != nil {
		go w.vwapLoop(ctx)
	}
	at, windows := w.plan()
	if at.IsZero() {
		logx.L().Info("session prefill not needed: app started before the pre-market; live freeze covers today")
		<-ctx.Done()
		return nil
	}
	logx.L().Info("session prefill scheduled", "at", at.Format("15:04:05 MST"), "windows", fmt.Sprint(windows))
	if d := at.Sub(w.cfg.Now()); d > 0 {
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
	}
	w.runOnce(ctx, windows)
	<-ctx.Done()
	return nil
}

// plan decides from AppStart (ET) whether a backfill is owed at all, when to
// run it, and which windows to fill:
//
//	before 04:05           -> never (live freeze is complete and tick-accurate)
//	(04:05, 09:31)         -> at 09:32, PRE only (the live 09:30 freeze ran on
//	                          partial data; the backfill overwrites it whole)
//	(09:31, 16:05)         -> ~90s after start, PRE only (rth is still being
//	                          watched live and freezes normally at 16:00)
//	after 16:05            -> ~90s after start, PRE and RTH
//
// Weekends/holidays are NOT special-cased: internal/sessions has no holiday
// calendar (same honest gap as schedule.ensureCalendar), and on a closed day
// the run naturally finds nothing — the snapshot carries no fresh movement
// and Intraday returns no bars for "today", so every window comes up empty.
//
// HONEST BEHAVIOR, documented rather than engineered around:
//
//   - 09:30 vs 09:32 on a mid-premarket start: the live boundary freezes a
//     PARTIAL pre slot at 09:30, and the 09:32 backfill overwrites it whole.
//     In that ~2-minute window a USER-BUILT scan alerting on pre* fields can
//     fire on the partial values and fire again on the corrected ones (the
//     seeded review scans ship with alerts disabled, so they are unaffected).
//     We accept the possible double-fire: suppressing it would mean
//     second-guessing user alert semantics for a two-minute edge.
//   - Early-close (13:00) half days: the rth window keeps the fixed
//     [09:30, 16:00) slicing, so post-close half-day trading lands in rth* —
//     the SAME fixed-16:00 model internal/sessions uses for the live freeze
//     (there is no half-day calendar). Both paths share the limitation
//     identically; fixing it here alone would make them drift.
func (w *Worker) plan() (time.Time, []core.Session) {
	st := w.cfg.AppStart.In(w.loc)
	min := st.Hour()*60 + st.Minute()
	switch {
	case min < liveOkBeforeMin:
		return time.Time{}, nil
	case min < lateStartMin:
		return time.Date(st.Year(), st.Month(), st.Day(), 9, 32, 0, 0, w.loc),
			[]core.Session{core.SessionPre}
	case min < postStartMin:
		return w.cfg.AppStart.Add(startupDelay), []core.Session{core.SessionPre}
	default:
		return w.cfg.AppStart.Add(startupDelay),
			[]core.Session{core.SessionPre, core.SessionRegular}
	}
}

// runOnce performs one backfill run, at most once per ET day. The latch is
// taken BEFORE any fetching — an aborted (429) or failed run does NOT retry
// today: tomorrow the app will have been running across the boundary and the
// live freeze covers it for free.
func (w *Worker) runOnce(ctx context.Context, windows []core.Session) {
	if !w.enabled || len(windows) == 0 {
		return
	}
	day := w.cfg.Now().In(w.loc).Format("2006-01-02")
	w.mu.Lock()
	if w.day == day {
		w.mu.Unlock()
		return
	}
	w.day = day
	w.mu.Unlock()

	seeds, err := w.cfg.Seeds(ctx)
	if err != nil {
		logx.L().Warn("prefill: snapshot fetch failed; skipping today's backfill", "err", err)
		return
	}
	var active map[string]struct{}
	if w.cfg.Active != nil {
		active = w.cfg.Active()
	}
	syms := w.withExtra(rank(seeds, w.cfg.Cap, active))
	logx.L().Info("prefill candidates selected", "count", len(syms), "cap", w.cfg.Cap,
		"active", len(active), "windows", len(windows))
	if len(syms) == 0 {
		return
	}
	w.fetch(ctx, syms, windows)
}

// Rank is rank, exported for internal/prevah and internal/rvolfill, which
// spend the same REST budget on the same kind of candidates.
func Rank(seeds []core.Seed, limit int, active map[string]struct{}) []string {
	return rank(seeds, limit, active)
}

// rank scores every snapshot seed and returns the top `limit` symbols,
// unioned with the scanner's active set. The active extras are capped at
// `limit` too, so the result — and therefore the run's Intraday call count —
// is bounded by 2*limit no matter how many symbols today's scans surfaced.
//
// The score is built only from what a snapshot Seed actually carries
// (verified against feed/snapshot.go's toSeed):
//
//   - |change %|: Seed.Close vs Seed.PrevClose. Close is the LAST TRADE —
//     during pre-market the snapshot's day block is empty and Close falls
//     back to the latest minute bar's close, so this term is extended-hours
//     aware and carries the ranking before the open.
//   - |gap %|: Seed.DayOpen vs Seed.PrevClose. DayOpen is the snapshot day
//     block's official REGULAR-session open (0 until 09:30 — no fallback),
//     so this term only contributes after the open, where it is a true
//     open-vs-prevClose gap.
//   - volume term: log10(1+AccVol) — today's accumulated volume (day volume,
//     falling back to the minute block's running total, which includes
//     pre-market). The log keeps it commensurate with the percentage terms:
//     1M shares adds ~6 "points", same order as a 6% move, so a mover needs
//     either real movement or real liquidity, and dead tickers score ~0.
func rank(seeds []core.Seed, limit int, active map[string]struct{}) []string {
	type cand struct {
		sym   string
		score float64
	}
	cands := make([]cand, 0, len(seeds))
	for _, s := range seeds {
		if s.Sym == "" {
			continue
		}
		sc := 0.0
		if s.PrevClose > 0 && s.Close > 0 {
			sc += math.Abs((s.Close - s.PrevClose) / s.PrevClose * 100)
		}
		if s.PrevClose > 0 && s.DayOpen > 0 {
			sc += math.Abs((s.DayOpen - s.PrevClose) / s.PrevClose * 100)
		}
		if s.AccVol > 0 {
			sc += math.Log10(1 + float64(s.AccVol))
		}
		if sc <= 0 {
			continue // no movement, no volume — nothing to review
		}
		cands = append(cands, cand{s.Sym, sc})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].sym < cands[j].sym // deterministic across runs
	})
	if len(cands) > limit {
		cands = cands[:limit]
	}
	out := make([]string, 0, len(cands)+len(active))
	seen := make(map[string]struct{}, len(cands))
	for _, c := range cands {
		out = append(out, c.sym)
		seen[c.sym] = struct{}{}
	}
	extra := make([]string, 0, len(active))
	for sym := range active {
		if _, ok := seen[sym]; !ok {
			extra = append(extra, sym)
		}
	}
	sort.Strings(extra) // map order is random; keep runs reproducible
	if len(extra) > limit {
		// A user scan gone wide (thousands of active symbols) must not unbound
		// the REST spend: the active union is capped at limit too, so one run
		// fetches at most 2*limit symbols. Alphabetical truncation keeps the
		// choice deterministic; the dropped tail loses nothing permanent — the
		// live freeze covers every symbol tomorrow.
		extra = extra[:limit]
	}
	return append(out, extra...)
}

// fetch pulls each candidate's full-day minute bars (paced launches, sem-4
// in-flight), slices them into the requested session windows, and applies the
// aggregated stats per window. The first 429 aborts the ENTIRE run — see the
// package comment and schedule.go's refreshDailyCloses for the house rule.
func (w *Worker) fetch(ctx context.Context, syms []string, windows []core.Session) {
	wantPre, wantRth := false, false
	for _, s := range windows {
		switch s {
		case core.SessionPre:
			wantPre = true
		case core.SessionRegular:
			wantRth = true
		}
	}

	fctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu      sync.Mutex
		pre     = make(map[string]core.SessionStats)
		rth     = make(map[string]core.SessionStats)
		fetched int
		failed  int
		aborted bool
	)
	sem := make(chan struct{}, fetchConcurrency)
	var wg sync.WaitGroup
	if w.cfg.SeedVwap != nil {
		w.mu.Lock()
		w.lastVwap = w.cfg.Now()
		w.mu.Unlock()
	}

	for i, sym := range syms {
		if fctx.Err() != nil {
			break
		}
		// Pace BEFORE every launch, not just after successes — the same rule
		// dailyCloseFetchPace documents: an unpaced error path would let a
		// stretch of failures fire at network-RTT speed.
		if i > 0 && w.pace > 0 {
			select {
			case <-fctx.Done():
			case <-time.After(w.pace):
			}
			if fctx.Err() != nil {
				break
			}
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(sym string) {
			defer wg.Done()
			defer func() { <-sem }()
			bars, err := w.cfg.Bars(fctx, sym)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if feed.IsRateLimitErr(err) {
					// First 429 aborts the whole run: the news poller shares
					// this REST budget and its own backoff runs minutes, so a
					// tripped throttle must stop spending immediately.
					if !aborted {
						aborted = true
						logx.L().Warn("prefill aborted on rate limit",
							"done", fetched, "total", len(syms), "failed", failed)
						cancel()
					}
					return
				}
				if fctx.Err() != nil {
					return // run already aborted/shut down; don't count the wash
				}
				failed++ // per-symbol failure: skip it, keep going
				return
			}
			fetched++
			if w.cfg.SeedVwap != nil {
				w.cfg.SeedVwap(sym, bars)
			}
			if wantPre {
				if st, ok := windowStats(bars, preLoMin, preHiMin, w.loc); ok {
					pre[sym] = st
				}
			}
			if wantRth {
				if st, ok := windowStats(bars, preHiMin, rthHiMin, w.loc); ok {
					rth[sym] = st
				}
			}
		}(sym)
	}
	wg.Wait()

	applied := 0
	if wantPre && len(pre) > 0 {
		w.cfg.Apply(core.SessionPre, pre)
		applied = len(pre)
	}
	if wantRth && len(rth) > 0 {
		w.cfg.Apply(core.SessionRegular, rth)
		if len(rth) > applied {
			applied = len(rth)
		}
	}
	w.mu.Lock()
	w.last = runStats{candidates: len(syms), fetched: fetched, failed: failed,
		aborted: aborted, applied: applied}
	w.mu.Unlock()
	logx.L().Info("prefill complete", "backfilled", applied, "candidates", len(syms),
		"fetched", fetched, "failed", failed, "aborted", aborted,
		"pre", len(pre), "rth", len(rth))
}

// windowStats aggregates the bars whose ET bar-START minute falls in
// [loMin, hiMin) into one SessionStats: hi = max(H), lo = min(L) over
// positive lows, vol = sum(V), close = last in-window C, and vwap =
// sum(C*V)/sum(V) — a CLOSE-weighted approximation, documented as such
// because core.Bar carries no per-bar VWAP; at 1-minute resolution it tracks
// the true VWAP closely. Returns ok=false when no bars land in the window
// (never traded there — the symbol is skipped, leaving its slot untouched).
func windowStats(bars []core.Bar, loMin, hiMin int, loc *time.Location) (core.SessionStats, bool) {
	var st core.SessionStats
	var num float64
	n := 0
	for _, b := range bars {
		t := time.UnixMilli(b.T).In(loc)
		m := t.Hour()*60 + t.Minute()
		if m < loMin || m >= hiMin {
			continue
		}
		n++
		if b.H > st.Hi {
			st.Hi = b.H
		}
		if b.L > 0 && (st.Lo == 0 || b.L < st.Lo) {
			st.Lo = b.L
		}
		st.Vol += b.V
		num += b.C * float64(b.V)
		if b.C > 0 {
			st.Close = b.C
		}
	}
	if n == 0 {
		return core.SessionStats{}, false
	}
	if st.Vol > 0 {
		st.Vwap = num / float64(st.Vol)
	}
	return st, true
}

// stats returns the last run's counters (tests assert on these, never on log
// text).
func (w *Worker) stats() runStats {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last
}

// envCap reads a NON-NEGATIVE integer env var (0 is meaningful: disabled),
// falling back to def when unset or invalid.
func envCap(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return def
}

// VWAP re-seed house numbers.
const (
	// vwapDelay: a (re)connect kick waits this long before fetching, so the
	// startup snapshot/universe/reference (avgVol for Extra) have landed and a
	// mid-day start's frozen-slot run (startupDelay, which seeds VWAPs too)
	// goes first and makes this one redundant.
	vwapDelay = 2 * time.Minute
	// vwapMinGap: at most one VWAP-seeding fetch per this window, whatever the
	// reconnect rate (each run is up to 2*Cap REST calls).
	vwapMinGap = 5 * time.Minute
)

// withExtra appends Extra's symbols not already in syms, capped at Cap.
func (w *Worker) withExtra(syms []string) []string {
	if w.cfg.SeedVwap == nil || w.cfg.Extra == nil {
		return syms
	}
	seen := make(map[string]struct{}, len(syms))
	for _, s := range syms {
		seen[s] = struct{}{}
	}
	n := 0
	for _, s := range w.cfg.Extra() {
		if n >= w.cfg.Cap {
			break
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		syms = append(syms, s)
		n++
	}
	return syms
}

// Reseed asks for a VWAP re-seed: ingest calls it on every stream (re)connect.
// Non-blocking and coalesced; a no-op when SeedVwap is not wired.
func (w *Worker) Reseed() {
	if w == nil || w.kick == nil {
		return
	}
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// scopeStart is the ET start of the VWAP scope t is in (pre 04:00, regular
// 09:30, post 16:00); ok=false outside 04:00-20:00.
func (w *Worker) scopeStart(t time.Time) (time.Time, bool) {
	et := t.In(w.loc)
	min := et.Hour()*60 + et.Minute()
	var sm int
	switch {
	case min < preLoMin:
		return time.Time{}, false
	case min < preHiMin:
		sm = preLoMin
	case min < rthHiMin:
		sm = preHiMin
	case min < 20*60:
		sm = rthHiMin
	default:
		return time.Time{}, false
	}
	return time.Date(et.Year(), et.Month(), et.Day(), sm/60, sm%60, 0, 0, w.loc), true
}

// vwapLoop serves Reseed kicks: after vwapDelay it fetches today's bars for the
// ranked candidates (+ Extra) and seeds their VWAPs, unless the scope started
// after the process did (the first connect saw the whole scope live) or a
// seeding fetch ran within vwapMinGap.
func (w *Worker) vwapLoop(ctx context.Context) {
	first := true
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.kick:
		}
		t := time.NewTimer(vwapDelay)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		select { // kicks during the wait are covered by this run
		case <-w.kick:
		default:
		}
		wasFirst := first
		first = false
		if !w.vwapDue(wasFirst) {
			continue
		}
		seeds, err := w.cfg.Seeds(ctx)
		if err != nil {
			logx.L().Warn("vwap seed: snapshot fetch failed", "err", err)
			continue
		}
		var active map[string]struct{}
		if w.cfg.Active != nil {
			active = w.cfg.Active()
		}
		syms := w.withExtra(rank(seeds, w.cfg.Cap, active))
		logx.L().Info("vwap seed: fetching today's minute bars", "count", len(syms))
		w.fetch(ctx, syms, nil)
	}
}

// vwapDue decides whether a kick's run is owed now.
func (w *Worker) vwapDue(firstConnect bool) bool {
	now := w.cfg.Now()
	st, ok := w.scopeStart(now)
	if !ok {
		return false
	}
	if firstConnect && !w.cfg.AppStart.After(st.Add(time.Minute)) {
		return false // the process saw the whole scope live
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastVwap.IsZero() || now.Sub(w.lastVwap) >= vwapMinGap
}
