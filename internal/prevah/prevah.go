// Package prevah seeds each symbol's PREVIOUS trading day's after-hours high
// (16:00-20:00 ET) into the engine, for the "broke yesterday's after-hours
// high" scan fields (brkPrevAhAge / vsPrevAh / prevAhHigh).
//
// Why a REST loader rather than the engine's own data: the engine only sees
// an after-hours session it was running for, and the persisted 20:00 session
// snapshot exists only on days the app was still open at 20:00 ET (01:00 UK) —
// rare in practice — and even then it can be PARTIAL (an app started at 18:00
// ET has no idea what traded at 16:30). A too-low level turns every trade
// above it into a fake breakout, so the level must come from a source that is
// complete by construction: Massive's 1-minute bars for that exact date.
// (AfterBell's market database was considered and rejected: by design it only
// keeps minute bars for symbols its own scans and charts touched, and it is
// PGlite running inside AfterBell, so it cannot be read from here safely.)
//
// There is no bulk endpoint — one REST call PER SYMBOL — so this follows
// prefill's REST-budget rules (see internal/prefill): a capped, ranked
// candidate list (whole-market snapshot movers + the scans' active symbols),
// paced launches under small bounded concurrency, and the first 429 stops all
// fetching for the rest of the ET day. Unlike prefill it TOPS UP during the
// day: names that become active later are fetched on the next pass, until the
// daily cap is spent. Disable with SCANNER_PREVAH_CAP=0.
//
// Date safety: bars are fetched for ONE explicit date and filtered on both the
// ET date and the minute of day, so a multi-day response can never leak today's
// bars into yesterday's after-hours high (cf. prefill's "date blindness").
package prevah

import (
	"context"
	"os"
	"strconv"
	"sync"
	"time"

	_ "time/tzdata" // America/New_York on Windows

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/feed"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
	"github.com/eelkonkoopman/marketscanner/internal/prefill"
)

const (
	ahStartMin = 16 * 60 // 16:00 ET
	ahEndMin   = 20 * 60 // 20:00 ET (exclusive)

	defaultCap   = 500              // symbols fetched per ET day
	defaultEvery = 10 * time.Minute // top-up cadence
	applyEvery   = time.Minute      // re-apply cadence (idempotent; catches newly created tickers)
	tickEvery    = 15 * time.Second
	// settle waits after the session becomes tradable so the engine's 04:00
	// day reset (which clears yesterday's levels) lands before we seed.
	settle = 10 * time.Second

	pace        = 250 * time.Millisecond // prefill's launch pace
	concurrency = 4                      // prefill's bound
)

// Config wires the worker to the rest of the app (injected by cmd/scanner).
type Config struct {
	Seeds  func(ctx context.Context) ([]core.Seed, error) // whole-market snapshot, for ranking
	Active func() map[string]struct{}                     // symbols the scans currently show
	// Keep limits candidates to the scanner's universe (universe.Contains):
	// the raw snapshot ranks warrants and rights as top movers, and every
	// fetch spent on a symbol the engine doesn't track is wasted budget.
	Keep    func(sym string) bool
	BarsOn  func(ctx context.Context, sym, date string) ([]core.Bar, error) // 1m bars for ONE ET date
	Apply   func(levels map[string]float64)                                 // engine.SetPrevAhHigh
	Session func() core.Session                                             // current trading session
	Cap     int                                                             // 0 = env/default; <0 disables
	Every   time.Duration                                                   // top-up cadence (0 = default)
	Now     func() time.Time
	// RankCap caps the ranked list per pass (defaults to Cap).
	RankCap int
	// Pace overrides the launch pace (tests).
	Pace time.Duration
}

// Worker owns one ET day's levels at a time.
type Worker struct {
	cfg Config
	loc *time.Location

	mu        sync.Mutex
	day       string             // ET date the state below belongs to
	prevDay   string             // that day's previous trading day
	levels    map[string]float64 // sym -> previous after-hours high
	tried     map[string]bool    // fetched (or failed) today; never refetched
	spent     int                // REST calls made today
	aborted   bool               // a 429 stopped fetching for the rest of the day
	sessSince time.Time          // when the session last became tradable
	nextRun   time.Time
	nextApply time.Time
}

func New(cfg Config) *Worker {
	if cfg.Cap == 0 {
		cfg.Cap = envInt("SCANNER_PREVAH_CAP", defaultCap)
	}
	if cfg.Every <= 0 {
		cfg.Every = defaultEvery
	}
	if cfg.RankCap <= 0 {
		cfg.RankCap = cfg.Cap
	}
	if cfg.Pace == 0 {
		cfg.Pace = pace
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.UTC
	}
	return &Worker{cfg: cfg, loc: loc}
}

// Run drives the worker until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	if w.cfg.Cap < 0 {
		logx.L().Info("prevah disabled (SCANNER_PREVAH_CAP<0)")
		return nil
	}
	tk := time.NewTicker(tickEvery)
	defer tk.Stop()
	for {
		w.tick(ctx, w.cfg.Now())
		select {
		case <-ctx.Done():
			return nil
		case <-tk.C:
		}
	}
}

// tick is one scheduling step; Run calls it on a timer, tests call it directly.
func (w *Worker) tick(ctx context.Context, now time.Time) {
	now = now.In(w.loc)
	if w.cfg.Session() == core.SessionClosed {
		w.mu.Lock()
		w.sessSince = time.Time{}
		w.mu.Unlock()
		return
	}
	w.mu.Lock()
	if w.sessSince.IsZero() {
		w.sessSince = now
	}
	date := now.Format("2006-01-02")
	if date != w.day {
		// New ET day: yesterday's levels describe the wrong previous day.
		w.day, w.prevDay = date, PrevTradingDay(now).Format("2006-01-02")
		w.levels, w.tried = map[string]float64{}, map[string]bool{}
		w.spent, w.aborted = 0, false
		w.nextRun, w.nextApply = time.Time{}, time.Time{}
	}
	if now.Sub(w.sessSince) < settle {
		w.mu.Unlock()
		return
	}
	runDue := !w.aborted && w.spent < w.cfg.Cap && !now.Before(w.nextRun)
	applyDue := !now.Before(w.nextApply)
	w.mu.Unlock()

	if runDue {
		w.runOnce(ctx)
		w.mu.Lock()
		w.nextRun = now.Add(w.cfg.Every)
		w.mu.Unlock()
		applyDue = true
	}
	if applyDue {
		w.mu.Lock()
		snap := make(map[string]float64, len(w.levels))
		for k, v := range w.levels {
			snap[k] = v
		}
		w.nextApply = now.Add(applyEvery)
		w.mu.Unlock()
		if len(snap) > 0 {
			w.cfg.Apply(snap)
		}
	}
}

// runOnce fetches the previous after-hours high for ranked candidates not yet
// tried today, within the remaining daily budget.
func (w *Worker) runOnce(ctx context.Context) {
	seeds, err := w.cfg.Seeds(ctx)
	if err != nil {
		logx.L().Warn("prevah: snapshot for ranking failed; retrying next pass", "err", err)
		return
	}
	var active map[string]struct{}
	if w.cfg.Active != nil {
		active = w.cfg.Active()
	}
	if w.cfg.Keep != nil {
		kept := seeds[:0:0]
		for _, s := range seeds {
			if w.cfg.Keep(s.Sym) {
				kept = append(kept, s)
			}
		}
		seeds = kept
		if len(active) > 0 {
			ka := make(map[string]struct{}, len(active))
			for sym := range active {
				if w.cfg.Keep(sym) {
					ka[sym] = struct{}{}
				}
			}
			active = ka
		}
	}
	w.mu.Lock()
	day, prevDay := w.day, w.prevDay
	todo := make([]string, 0, 64)
	for _, sym := range prefill.Rank(seeds, w.cfg.RankCap, active) {
		if len(todo)+w.spent >= w.cfg.Cap {
			break
		}
		if !w.tried[sym] {
			todo = append(todo, sym)
			w.tried[sym] = true // claimed: never fetched twice today, even on failure
		}
	}
	w.spent += len(todo)
	w.mu.Unlock()
	if len(todo) == 0 {
		return
	}

	var (
		wg      sync.WaitGroup
		sem     = make(chan struct{}, concurrency)
		stop    = make(chan struct{})
		once    sync.Once
		mu      sync.Mutex
		got     = map[string]float64{}
		noAH    int
		failed  int
		limited bool
	)
	lim := time.NewTicker(w.cfg.Pace)
	defer lim.Stop()
launch:
	for _, sym := range todo {
		select {
		case <-ctx.Done():
			break launch
		case <-stop:
			break launch
		case <-lim.C:
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(sym string) {
			defer wg.Done()
			defer func() { <-sem }()
			bars, err := w.cfg.BarsOn(ctx, sym, prevDay)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil && feed.IsRateLimitErr(err):
				limited = true
				once.Do(func() { close(stop) })
			case err != nil:
				failed++
			default:
				if hi, ok := PostHigh(bars, prevDay, w.loc); ok {
					got[sym] = hi
				} else {
					noAH++ // didn't trade after hours: no level, nothing to break
				}
			}
		}(sym)
	}
	wg.Wait()

	w.mu.Lock()
	if w.day == day { // the ET day may have rolled while we fetched
		for k, v := range got {
			w.levels[k] = v
		}
		if limited {
			w.aborted = true
		}
	}
	total := len(w.levels)
	w.mu.Unlock()
	logx.L().Info("prevah pass complete", "day", day, "prevDay", prevDay, "fetched", len(todo),
		"levels", len(got), "noAfterHours", noAH, "failed", failed, "rateLimited", limited, "levelsToday", total)
}

// PostHigh returns the highest bar high in [16:00, 20:00) ET on date (ET,
// "2006-01-02"), checking each bar's ET DATE as well as its minute of day.
func PostHigh(bars []core.Bar, date string, loc *time.Location) (float64, bool) {
	var hi float64
	found := false
	for _, b := range bars {
		t := time.UnixMilli(b.T).In(loc)
		if t.Format("2006-01-02") != date {
			continue
		}
		if m := t.Hour()*60 + t.Minute(); m < ahStartMin || m >= ahEndMin {
			continue
		}
		if b.H > hi {
			hi, found = b.H, true
		}
	}
	return hi, found && hi > 0
}

// PrevTradingDay steps back from t's ET date over weekends. Like
// feed.prevTradingDay it has no holiday calendar: the day after a market
// holiday reports the holiday itself, which then has no after-hours bars, so
// no levels are seeded that day rather than wrong ones.
func PrevTradingDay(t time.Time) time.Time {
	d := t.AddDate(0, 0, -1)
	for wd := d.Weekday(); wd == time.Saturday || wd == time.Sunday; wd = d.Weekday() {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

// Levels returns a copy of today's levels (status / tests).
func (w *Worker) Levels() map[string]float64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]float64, len(w.levels))
	for k, v := range w.levels {
		out[k] = v
	}
	return out
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			if n == 0 {
				return -1 // 0 means disabled, as for SCANNER_PREFILL_CAP
			}
			return n
		}
	}
	return def
}
