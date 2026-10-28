// Package rvolfill rebuilds past days' cumulative volume curves from Massive
// 1-minute bars, so RVOL works for a symbol without first recording five of
// its sessions live (the engine only records the hours the app is open, and
// RVOL stays null below five sessions).
//
// What a rebuilt curve must match is the engine's LIVE curve: the snapshot's
// cumulative day volume per 5-minute bucket. Checked against the raw trade
// tape (2026-09-28 / 10-01 / 10-02, small and large caps): before 16:00 a
// finalized day's minute bars equal the tape within ~1-5%, but the daily total
// also counts the closing-auction print and late "prior reference price"
// prints that minute bars leave out, worth up to ~40% of the day on large
// caps (ABBV) and ~0% on small caps. So a rebuilt curve is the minute bars'
// running total, plus that gap (daily_close_history volume minus the minute
// total) from the closing bucket on, where the closing cross lands. Scaling
// the whole day up instead would inflate the morning baseline by the same
// ~40% on those names. (TODAY's minute bars run well short of the tape in
// pre-market, since late TRF prints get folded in later, so only finished
// past days are rebuilt.)
//
// One request per symbol (no bulk endpoint), so this follows prevah's
// REST-budget rules: ranked candidates (the scans' current symbols first,
// then whole-market snapshot movers), paced launches, bounded concurrency, a
// daily cap, and the first 429 stops fetching for the rest of the ET day.
// Each (symbol, date) is fetched once ever (persisted markers); rebuilt data
// MERGES with live-recorded days (the larger value per bucket wins), so it
// only fills what the app didn't see. After a pass writes anything the
// rebuilt symbols' baseline is recomputed and swapped in. Disable with
// SCANNER_RVOLFILL_CAP=0.
package rvolfill

import (
	"context"
	"os"
	"sort"
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
	defaultCap     = 2000             // symbols fetched per ET day
	defaultPassCap = 150              // symbols per pass (each pass ends in a baseline swap)
	defaultEvery   = time.Minute      // pass cadence
	defaultFill    = 10               // most recent baseline days rebuilt per symbol
	rankEvery      = 10 * time.Minute // whole-market snapshot re-rank cadence
	rankCap        = 500              // snapshot movers considered per ranking
	startDelay     = 20 * time.Second // let the scheduler build the calendar first
	tickEvery      = 5 * time.Second

	pace        = 250 * time.Millisecond // prefill's launch pace
	concurrency = 4                      // prefill's bound
)

// Store is the persistence the worker needs (satisfied by *persist.DB).
type Store interface {
	FilledCurveDates(ctx context.Context, dates []string) (map[string]map[string]bool, error)
	DailyVolumes(ctx context.Context, ticker string, dates []string) (map[string]int64, error)
	SaveFilledCurves(ctx context.Context, ticker string, curves map[string][]int64, dates []string) error
}

// Config wires the worker to the rest of the app (injected by cmd/scanner).
type Config struct {
	Seeds  func(ctx context.Context) ([]core.Seed, error) // whole-market snapshot, for ranking
	Active func() map[string]struct{}                     // symbols the scans currently show
	Keep   func(sym string) bool                          // the scanner's universe
	// Bars fetches 1m bars for the ET dates from..to inclusive in one request
	// (feed.Client.IntradayBetween).
	Bars func(ctx context.Context, sym, from, to string) ([]core.Bar, error)
	// Days returns the baseline's current window, most recent first
	// (schedule.Scheduler.BaselineDays).
	Days  func(ctx context.Context) ([]core.MarketDay, error)
	Store Store
	// Refresh recomputes and swaps in these tickers' baseline
	// (Scheduler.RefreshBaselineFor).
	Refresh func(ctx context.Context, tickers []string)

	Cap     int           // symbols per ET day; 0 = env/default; <0 disables
	PassCap int           // symbols per pass (0 = default)
	Every   time.Duration // pass cadence (0 = default)
	Fill    int           // days rebuilt per symbol (0 = default)
	Now     func() time.Time
	Pace    time.Duration // launch pace override (tests)
}

// Worker runs the passes; state is per ET day.
type Worker struct {
	cfg Config
	loc *time.Location

	mu       sync.Mutex
	started  time.Time
	day      string          // ET date the state below belongs to
	tried    map[string]bool // fetched (or failed) today; never refetched today
	spent    int
	aborted  bool // a 429 stopped fetching for the rest of the day
	nextRun  time.Time
	ranked   []string // snapshot movers, refreshed every rankEvery
	rankedAt time.Time
}

func New(cfg Config) *Worker {
	if cfg.Cap == 0 {
		cfg.Cap = envInt("SCANNER_RVOLFILL_CAP", defaultCap)
	}
	if cfg.PassCap <= 0 {
		cfg.PassCap = defaultPassCap
	}
	if cfg.Every <= 0 {
		cfg.Every = defaultEvery
	}
	if cfg.Fill <= 0 {
		cfg.Fill = defaultFill
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
	return &Worker{cfg: cfg, loc: loc, tried: map[string]bool{}}
}

// Run drives the worker until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	if w.cfg.Cap < 0 {
		logx.L().Info("rvolfill disabled (SCANNER_RVOLFILL_CAP=0)")
		return nil
	}
	w.mu.Lock()
	w.started = w.cfg.Now()
	w.mu.Unlock()
	tk := time.NewTicker(tickEvery)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tk.C:
		}
		w.tick(ctx, w.cfg.Now())
	}
}

// tick is one scheduling step; Run calls it on a timer, tests call it directly.
func (w *Worker) tick(ctx context.Context, now time.Time) {
	now = now.In(w.loc)
	w.mu.Lock()
	if now.Sub(w.started) < startDelay {
		w.mu.Unlock()
		return
	}
	if date := now.Format("2006-01-02"); date != w.day {
		w.day, w.tried, w.spent, w.aborted, w.nextRun = date, map[string]bool{}, 0, false, time.Time{}
	}
	due := !w.aborted && w.spent < w.cfg.Cap && !now.Before(w.nextRun)
	w.mu.Unlock()
	if !due {
		return
	}
	w.runOnce(ctx, now)
	w.mu.Lock()
	w.nextRun = now.Add(w.cfg.Every)
	w.mu.Unlock()
}

// job is one symbol's fetch: the baseline days it still lacks.
type job struct {
	sym  string
	days []core.MarketDay // ascending
}

func (w *Worker) runOnce(ctx context.Context, now time.Time) {
	days, err := w.cfg.Days(ctx)
	if err != nil || len(days) == 0 {
		return // calendar not built yet; next pass
	}
	if len(days) > w.cfg.Fill {
		days = days[:w.cfg.Fill]
	}
	dates := make([]string, len(days))
	for i, d := range days {
		dates[i] = d.Date
	}
	filled, err := w.cfg.Store.FilledCurveDates(ctx, dates)
	if err != nil {
		logx.L().Warn("rvolfill: reading markers failed", "err", err)
		return
	}

	cands := w.candidates(ctx, now)
	w.mu.Lock()
	day := w.day
	var jobs []job
	for _, sym := range cands {
		if len(jobs) >= w.cfg.PassCap || w.spent+len(jobs) >= w.cfg.Cap {
			break
		}
		if w.tried[sym] {
			continue
		}
		var need []core.MarketDay
		for _, d := range days {
			if !filled[sym][d.Date] {
				need = append(need, d)
			}
		}
		if len(need) == 0 {
			continue
		}
		sort.Slice(need, func(i, j int) bool { return need[i].Date < need[j].Date })
		jobs = append(jobs, job{sym, need})
		w.tried[sym] = true // claimed: never fetched twice today, even on failure
	}
	w.spent += len(jobs)
	w.mu.Unlock()
	if len(jobs) == 0 {
		return
	}

	var (
		wg      sync.WaitGroup
		sem     = make(chan struct{}, concurrency)
		stop    = make(chan struct{})
		once    sync.Once
		mu      sync.Mutex
		wrote   []string
		empty   int
		failed  int
		limited bool
	)
	lim := time.NewTicker(w.cfg.Pace)
	defer lim.Stop()
launch:
	for _, j := range jobs {
		select {
		case <-ctx.Done():
			break launch
		case <-stop:
			break launch
		case <-lim.C:
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			defer func() { <-sem }()
			n, err := w.fill(ctx, j)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil && feed.IsRateLimitErr(err):
				limited = true
				once.Do(func() { close(stop) })
			case err != nil:
				failed++
				logx.L().Debug("rvolfill: symbol failed", "sym", j.sym, "err", err)
			case n == 0:
				empty++ // didn't trade on any of those days
			default:
				wrote = append(wrote, j.sym)
			}
		}(j)
	}
	wg.Wait()

	if limited {
		w.mu.Lock()
		if w.day == day {
			w.aborted = true
		}
		w.mu.Unlock()
	}
	if len(wrote) > 0 && w.cfg.Refresh != nil {
		w.cfg.Refresh(ctx, wrote)
	}
	logx.L().Info("rvolfill pass complete", "symbols", len(jobs), "rebuilt", len(wrote), "noTrades", empty,
		"failed", failed, "rateLimited", limited, "through", dates[0])
}

// candidates lists the scans' current symbols first (what the user is looking
// at), then the cached snapshot ranking. Only runOnce's goroutine touches
// ranked/rankedAt.
func (w *Worker) candidates(ctx context.Context, now time.Time) []string {
	keep := func(sym string) bool { return w.cfg.Keep == nil || w.cfg.Keep(sym) }
	var out []string
	seen := map[string]bool{}
	if w.cfg.Active != nil {
		var act []string
		for sym := range w.cfg.Active() {
			if keep(sym) {
				act = append(act, sym)
			}
		}
		sort.Strings(act) // map order is random; keep passes reproducible
		for _, sym := range act {
			out, seen[sym] = append(out, sym), true
		}
	}
	if w.cfg.Seeds != nil && (w.ranked == nil || now.Sub(w.rankedAt) >= rankEvery) {
		if seeds, err := w.cfg.Seeds(ctx); err == nil {
			kept := seeds[:0:0]
			for _, s := range seeds {
				if keep(s.Sym) {
					kept = append(kept, s)
				}
			}
			w.ranked, w.rankedAt = prefill.Rank(kept, rankCap, nil), now
		} else {
			logx.L().Warn("rvolfill: snapshot for ranking failed; using the scans' symbols", "err", err)
		}
	}
	for _, sym := range w.ranked {
		if !seen[sym] {
			out, seen[sym] = append(out, sym), true
		}
	}
	return out
}

// fill fetches one symbol's missing days and stores the rebuilt curves,
// returning how many days had trades.
func (w *Worker) fill(ctx context.Context, j job) (int, error) {
	bars, err := w.cfg.Bars(ctx, j.sym, j.days[0].Date, j.days[len(j.days)-1].Date)
	if err != nil {
		return 0, err
	}
	dates := make([]string, len(j.days))
	for i, d := range j.days {
		dates[i] = d.Date
	}
	daily, err := w.cfg.Store.DailyVolumes(ctx, j.sym, dates)
	if err != nil {
		return 0, err
	}
	curves := BuildCurves(bars, j.days, daily, w.loc)
	if err := w.cfg.Store.SaveFilledCurves(ctx, j.sym, curves, dates); err != nil {
		return 0, err
	}
	return len(curves), nil
}

// BuildCurves turns 1-minute bars into one cumulative 5-minute curve per day
// (bucket 0 = 04:00 ET, the engine's layout), for the given days only: each
// bar is placed by its own ET date and minute, so a multi-day response can
// never leak one day into another. The daily total's excess over the minute
// bars (closing auction + late prints, see the package doc) is added from the
// day's regular-close bucket on (when the day's curve reaches it). Days
// without a single bar are absent.
func BuildCurves(bars []core.Bar, days []core.MarketDay, daily map[string]int64, loc *time.Location) map[string][]int64 {
	idx := make(map[string]int, len(days))
	for i, d := range days {
		idx[d.Date] = i
	}
	per := make([][core.BucketCount]int64, len(days))
	seen := make([]bool, len(days))
	for _, b := range bars {
		t := time.UnixMilli(b.T).In(loc)
		i, ok := idx[t.Format("2006-01-02")]
		if !ok {
			continue
		}
		m := t.Hour()*60 + t.Minute() - 240
		if m < 0 || m >= core.BucketCount*5 || b.V <= 0 {
			continue
		}
		per[i][m/5] += b.V
		seen[i] = true
	}
	out := make(map[string][]int64, len(days))
	for i, d := range days {
		if !seen[i] {
			continue
		}
		last := d.LastBucket
		if last <= 0 || last >= core.BucketCount {
			last = core.BucketCount - 1
		}
		closeMin := d.RegCloseMin
		if closeMin <= 240 {
			closeMin = 960 // no calendar row: 16:00 ET
		}
		closeB := (closeMin - 240) / 5
		cum := make([]int64, last+1)
		var s int64
		for k := 0; k <= last; k++ {
			s += per[i][k]
			cum[k] = s
		}
		// On a half day the stored curve ends at the 13:00 close itself (bucket
		// 107), before the closing cross prints: there is no bucket for the gap.
		if gap := daily[d.Date] - s; gap > 0 && closeB <= last {
			for k := closeB; k <= last; k++ {
				cum[k] += gap
			}
		}
		out[d.Date] = cum
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
