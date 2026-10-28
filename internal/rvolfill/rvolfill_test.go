package rvolfill

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/feed"
	"github.com/eelkonkoopman/marketscanner/internal/persist"
)

var ny = func() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return loc
}()

// bar is a 1-minute bar starting at hh:mm ET on date.
func bar(date string, hh, mm int, v int64) core.Bar {
	d, _ := time.ParseInLocation("2006-01-02", date, ny)
	return core.Bar{T: d.Add(time.Duration(hh*60+mm) * time.Minute).UnixMilli(), V: v}
}

func fullDay(date string) core.MarketDay {
	return core.MarketDay{Date: date, IsTrading: true, SessOpenMin: 240, RegOpenMin: 570,
		RegCloseMin: 960, SessCloseMin: 1200, LastBucket: 191}
}

func TestBuildCurvesPlacesBarsByETDateAndMinute(t *testing.T) {
	bars := []core.Bar{
		bar("2026-10-01", 3, 59, 999),  // before 04:00: not part of the curve
		bar("2026-10-01", 4, 0, 10),    // bucket 0
		bar("2026-10-01", 4, 4, 5),     // bucket 0
		bar("2026-10-01", 4, 5, 7),     // bucket 1
		bar("2026-10-01", 9, 30, 100),  // bucket 66
		bar("2026-10-01", 20, 0, 999),  // 20:00: past the session
		bar("2026-10-02", 4, 0, 1),     // the next day must not leak into 10-01
		bar("2026-10-05", 10, 0, 5000), // a date that wasn't asked for
	}
	got := BuildCurves(bars, []core.MarketDay{fullDay("2026-10-01"), fullDay("2026-10-02")}, nil, ny)
	c := got["2026-10-01"]
	if len(c) != core.BucketCount {
		t.Fatalf("a full day has %d buckets, got %d", core.BucketCount, len(c))
	}
	for b, want := range map[int]int64{0: 15, 1: 22, 65: 22, 66: 122, 191: 122} {
		if c[b] != want {
			t.Errorf("10-01 bucket %d: want cumulative %d, got %d", b, want, c[b])
		}
	}
	if d := got["2026-10-02"]; d[0] != 1 || d[191] != 1 {
		t.Errorf("10-02 holds only its own bar: got %d..%d", d[0], d[191])
	}
	if _, ok := got["2026-10-05"]; ok {
		t.Error("a date that wasn't requested must not be built")
	}
}

// The daily total's excess (closing auction + late prints) lands at the close,
// never in the morning: scaling the whole day would inflate pre-market.
func TestBuildCurvesAddsTheDailyGapFromTheCloseBucket(t *testing.T) {
	bars := []core.Bar{bar("2026-10-01", 7, 0, 100), bar("2026-10-01", 15, 59, 900), bar("2026-10-01", 16, 30, 50)}
	c := BuildCurves(bars, []core.MarketDay{fullDay("2026-10-01")}, map[string]int64{"2026-10-01": 3050}, ny)["2026-10-01"]
	if c[143] != 1000 {
		t.Fatalf("before the close the curve is the minute bars only: bucket 143 = %d", c[143])
	}
	if c[144] != 1000+2000 {
		t.Fatalf("the 2,000-share gap is added at 16:00 (bucket 144): got %d", c[144])
	}
	if c[191] != 3050 {
		t.Fatalf("the day must end at the daily volume: got %d", c[191])
	}
	// Daily BELOW the minute total (rounding, a missing print): never subtract.
	c = BuildCurves(bars, []core.MarketDay{fullDay("2026-10-01")}, map[string]int64{"2026-10-01": 10}, ny)["2026-10-01"]
	if c[191] != 1050 {
		t.Fatalf("a lower daily must not shrink the curve: got %d", c[191])
	}
}

func TestBuildCurvesHalfDay(t *testing.T) {
	half := core.MarketDay{Date: "2026-11-27", IsTrading: true, IsHalfDay: true, SessOpenMin: 240,
		RegOpenMin: 570, RegCloseMin: 780, SessCloseMin: 1020, LastBucket: 107}
	bars := []core.Bar{bar("2026-11-27", 12, 0, 100)}
	c := BuildCurves(bars, []core.MarketDay{half}, map[string]int64{"2026-11-27": 300}, ny)["2026-11-27"]
	if len(c) != 108 {
		t.Fatalf("a half day ends at bucket 107: got %d buckets", len(c))
	}
	if c[107] != 100 {
		t.Fatalf("the curve ends AT the 13:00 close, before the closing cross: no gap, got %d", c[107])
	}
}

func TestBuildCurvesSkipsDaysWithoutTrades(t *testing.T) {
	got := BuildCurves(nil, []core.MarketDay{fullDay("2026-10-01")}, map[string]int64{"2026-10-01": 500}, ny)
	if len(got) != 0 {
		t.Fatalf("no bars, no curve (a daily volume alone has no shape): %v", got)
	}
}

// ---- worker ----

type harness struct {
	t     *testing.T
	db    *persist.DB
	w     *Worker
	mu    sync.Mutex
	calls []string // "SYM from..to"
	limit string   // this symbol answers 429
	fail  string   // this symbol fails with a plain error
	seeds []core.Seed
	act   map[string]struct{}
	days  []core.MarketDay
	refr  int
	// refreshed collects the tickers passed to Refresh.
	refreshed []string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := persist.Open(context.Background(), filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h := &harness{t: t, db: db, act: map[string]struct{}{}}
	// Baseline window, most recent first, as Scheduler.BaselineDays returns it.
	for _, d := range []string{"2026-10-02", "2026-10-01", "2026-09-30"} {
		h.days = append(h.days, fullDay(d))
	}
	h.w = New(Config{
		Seeds:  func(context.Context) ([]core.Seed, error) { return h.seeds, nil },
		Active: func() map[string]struct{} { return h.act },
		Keep:   func(sym string) bool { return sym != "JUNKW" },
		Bars: func(_ context.Context, sym, from, to string) ([]core.Bar, error) {
			h.mu.Lock()
			h.calls = append(h.calls, sym+" "+from+".."+to)
			h.mu.Unlock()
			switch sym {
			case h.limit:
				return nil, &feed.HTTPError{Status: 429, Msg: "HTTP 429"}
			case h.fail:
				return nil, errors.New("boom")
			}
			var out []core.Bar
			for _, d := range []string{"2026-09-30", "2026-10-01", "2026-10-02"} {
				if d >= from && d <= to {
					out = append(out, bar(d, 10, 0, 1000))
				}
			}
			return out, nil
		},
		Days:    func(context.Context) ([]core.MarketDay, error) { return h.days, nil },
		Store:   db,
		Refresh: func(_ context.Context, syms []string) { h.refr++; h.refreshed = append(h.refreshed, syms...) },
		Cap:     100,
		Pace:    time.Millisecond,
		Now:     func() time.Time { return time.Date(2026, 10, 5, 7, 0, 0, 0, ny) },
	})
	return h
}

func (h *harness) seed(syms ...string) {
	for i, s := range syms {
		h.seeds = append(h.seeds, core.Seed{Agg: core.Agg{Sym: s, AccVol: int64(1e6 * (len(syms) - i))}})
	}
}

func (h *harness) run() {
	h.w.runOnce(context.Background(), h.w.cfg.Now().In(ny))
}

func (h *harness) takeCalls() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := append([]string(nil), h.calls...)
	h.calls = nil
	sort.Strings(c)
	return c
}

// storedAt reads one stored curve value through a one-day baseline (the
// repo's only read path for curve history); 0 = nothing stored.
func storedAt(t *testing.T, db *persist.DB, sym, date string, bucket int) int64 {
	t.Helper()
	ctx := context.Background()
	if _, err := db.BaselineRepo().RecomputeBaseline(ctx, date, []string{date}); err != nil {
		t.Fatal(err)
	}
	bt, err := db.BaselineRepo().LoadBaseline(ctx)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := bt.Get(sym, bucket)
	return int64(v)
}

func TestPassFetchesEachMissingDayOnceAndRefreshes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// AAA already had 09-30 rebuilt on an earlier run.
	if err := h.db.SaveFilledCurves(ctx, "AAA", nil, []string{"2026-09-30"}); err != nil {
		t.Fatal(err)
	}
	h.act["BBB"] = struct{}{}
	h.act["JUNKW"] = struct{}{} // outside the universe
	h.seed("AAA", "CCC")
	h.run()
	want := []string{"AAA 2026-10-01..2026-10-02", "BBB 2026-09-30..2026-10-02", "CCC 2026-09-30..2026-10-02"}
	if got := h.takeCalls(); !equal(got, want) {
		t.Fatalf("one request per symbol spanning only its missing days:\n got %v\nwant %v", got, want)
	}
	if h.refr != 1 {
		t.Fatalf("a pass that wrote curves must swap the baseline in once, got %d", h.refr)
	}
	sort.Strings(h.refreshed)
	if !equal(h.refreshed, []string{"AAA", "BBB", "CCC"}) {
		t.Fatalf("exactly the rebuilt tickers are recomputed: %v", h.refreshed)
	}
	if v := storedAt(t, h.db, "BBB", "2026-10-01", 191); v != 1000 {
		t.Fatalf("rebuilt curve stored: BBB 10-01 bucket 191 = %d", v)
	}
	// A restarted process (fresh tried-set) must not refetch: the markers persist.
	h.w = New(h.w.cfg)
	h.run()
	if got := h.takeCalls(); len(got) != 0 {
		t.Fatalf("every (symbol, day) is fetched once ever, got %v", got)
	}
	if h.refr != 1 {
		t.Fatal("nothing written, nothing to recompute")
	}
}

func TestScansSymbolsGoFirstAndPassCapHolds(t *testing.T) {
	h := newHarness(t)
	h.w.cfg.PassCap = 2
	h.seed("M1", "M2", "M3")
	h.act["ZZZ"] = struct{}{}
	h.run()
	got := h.takeCalls()
	if len(got) != 2 || got[1][:3] != "ZZZ" || got[0][:2] != "M1" {
		t.Fatalf("the scans' symbol first, then the top mover, 2 per pass: %v", got)
	}
	h.run()
	if got := h.takeCalls(); len(got) != 2 {
		t.Fatalf("the next pass takes the next two: %v", got)
	}
}

func TestLiveDaysMergeNotOverwrite(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// The app recorded AAA live on 10-01 only from 12:20 ET (bucket 100), with
	// more volume than the minute bars show.
	live := make([]int64, 192)
	for b := 100; b < 192; b++ {
		live[b] = 5000
	}
	if err := h.db.BaselineRepo().UpsertVolumeCurves(ctx, "2026-10-01", map[string][]int64{"AAA": live}); err != nil {
		t.Fatal(err)
	}
	h.seed("AAA")
	h.run()
	if v := storedAt(t, h.db, "AAA", "2026-10-01", 120); v != 5000 {
		t.Fatalf("a live-recorded bucket keeps the larger live value: got %d", v)
	}
	if v := storedAt(t, h.db, "AAA", "2026-10-01", 80); v != 1000 {
		t.Fatalf("a bucket the app didn't see is filled from the minute bars: got %d", v)
	}
}

func TestRateLimitStopsForTheDay(t *testing.T) {
	h := newHarness(t)
	h.w.cfg.Pace = 20 * time.Millisecond
	h.w.cfg.Every = time.Nanosecond
	h.limit = "AAA"
	h.seed("AAA", "B1", "B2", "B3", "B4", "B5", "B6", "B7", "B8", "B9")
	h.w.started = time.Date(2026, 10, 5, 6, 0, 0, 0, ny)
	h.w.tick(context.Background(), time.Date(2026, 10, 5, 7, 0, 0, 0, ny))
	first := len(h.takeCalls())
	if first >= 10 {
		t.Fatalf("a 429 must stop launching the rest of the pass: %d calls", first)
	}
	h.seed("NEW1", "NEW2")                                                  // movers that appear later the same day
	h.w.tick(context.Background(), time.Date(2026, 10, 5, 7, 15, 0, 0, ny)) // past the re-rank interval
	if n := len(h.takeCalls()); n != 0 {
		t.Fatalf("nothing more is fetched today after a 429, not even new movers: %d calls", n)
	}
	h.w.tick(context.Background(), time.Date(2026, 10, 6, 7, 0, 0, 0, ny))
	if n := len(h.takeCalls()); n == 0 {
		t.Fatal("a new ET day resumes")
	}
}

func TestFailedSymbolIsNotMarked(t *testing.T) {
	h := newHarness(t)
	h.fail = "AAA"
	h.seed("AAA")
	h.run()
	h.takeCalls()
	f, err := h.db.FilledCurveDates(context.Background(), []string{"2026-09-30", "2026-10-01", "2026-10-02"})
	if err != nil {
		t.Fatal(err)
	}
	if len(f["AAA"]) != 0 {
		t.Fatalf("a failed fetch must leave every day unmarked, so a later run retries it: %v", f["AAA"])
	}
	if h.refr != 0 {
		t.Fatal("nothing written, no recompute")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
