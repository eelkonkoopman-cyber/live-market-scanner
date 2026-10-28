package schedule

import (
	"context"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/feed"
	"github.com/eelkonkoopman/marketscanner/internal/persist"
	"github.com/eelkonkoopman/marketscanner/internal/universe"
)

// fakeDailySrc drives the trend job's GroupedDaily calls under test control:
// scripted per-date responses/errors, a call log, and a togglable key.
type fakeDailySrc struct {
	mu      sync.Mutex
	hasKey  bool
	calls   []string
	onCall  func(date string) (map[string]core.DailyBar, error) // nil = default: one bar per date
	defBase float64
}

func (f *fakeDailySrc) HasKey() bool { return f.hasKey }

func (f *fakeDailySrc) GroupedDaily(_ context.Context, date string) (map[string]core.DailyBar, error) {
	f.mu.Lock()
	f.calls = append(f.calls, date)
	f.mu.Unlock()
	if f.onCall != nil {
		return f.onCall(date)
	}
	return map[string]core.DailyBar{"AAA": {Date: date, C: f.defBase}}, nil
}

func (f *fakeDailySrc) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func newTestScheduler(t *testing.T) (*Scheduler, *persist.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := persist.Open(ctx, filepath.Join(t.TempDir(), "sched.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	uni := universe.NewAll()
	s := New(db.BaselineRepo(), db.FloatRepo(), db.CalendarRepo(), &fakeEng{}, fakeFloatSrc{}, uni)
	return s, db
}

var rateLimitErr = &feed.HTTPError{Status: http.StatusTooManyRequests, Msg: "429"}
var serverErr = &feed.HTTPError{Status: http.StatusInternalServerError, Msg: "boom"}

// refreshDailyCloses must abort the ENTIRE call on the first rate-limit
// response, not just skip that one date — the news poller shares the same
// per-key REST budget and hammering through a throttle only prolongs it.
func TestRefreshDailyClosesAbortsOnRateLimit(t *testing.T) {
	s, db := newTestScheduler(t)
	ctx := context.Background()
	trepo := db.TrendRepo()
	s.trepo = trepo

	dates := []string{"2026-07-20", "2026-07-21", "2026-07-22", "2026-07-23", "2026-07-24"}
	src := &fakeDailySrc{hasKey: true}
	src.onCall = func(d string) (map[string]core.DailyBar, error) {
		if d == "2026-07-22" {
			return nil, rateLimitErr
		}
		return map[string]core.DailyBar{"AAA": {Date: d, C: 10}}, nil
	}
	s.dsrc = src

	s.refreshDailyCloses(ctx, dates, false)

	if got := src.callCount(); got != 3 {
		t.Fatalf("must stop AT the rate-limited date (3 calls: 20,21,22), got %d calls: %v", got, src.calls)
	}
	for _, d := range []string{"2026-07-23", "2026-07-24"} {
		if has, _ := trepo.HasClosesForDate(ctx, d); has {
			t.Fatalf("date %s is AFTER the rate limit and must never have been fetched", d)
		}
	}
	for _, d := range []string{"2026-07-20", "2026-07-21"} {
		if has, _ := trepo.HasClosesForDate(ctx, d); !has {
			t.Fatalf("date %s was fetched successfully before the throttle and should be stored", d)
		}
	}
}

// The pacing sleep must apply BEFORE every attempt (success or failure), not
// only after a success — otherwise a run of failures fires back-to-back.
func TestRefreshDailyClosesPacesEveryAttempt(t *testing.T) {
	s, db := newTestScheduler(t)
	ctx := context.Background()
	s.trepo = db.TrendRepo()
	src := &fakeDailySrc{hasKey: true}
	src.onCall = func(string) (map[string]core.DailyBar, error) { return nil, serverErr }
	s.dsrc = src

	start := time.Now()
	s.refreshDailyCloses(ctx, []string{"2026-07-20", "2026-07-21", "2026-07-22"}, false)
	elapsed := time.Since(start)

	// 3 attempts, all erroring -> 2 inter-attempt gaps of dailyCloseFetchPace.
	want := 2 * dailyCloseFetchPace
	if elapsed < want {
		t.Fatalf("expected pacing between EVERY attempt (including failures): elapsed %v < want >= %v", elapsed, want)
	}
}

// Without a configured API key, the trend job must not spend a single
// request — an empty-key boot is an explicitly supported flow, not a chance
// to burn ~32 guaranteed-401 calls on the unpaced error path.
func TestDailyTrendSkippedWithoutKey(t *testing.T) {
	s, db := newTestScheduler(t)
	ctx := context.Background()
	trepo := db.TrendRepo()
	src := &fakeDailySrc{hasKey: false}
	var swapped bool
	s.EnableDailyTrend(trepo, src, func(m map[string]core.TrendRef) { swapped = true })

	tuesday := time.Date(2026, 6, 23, 21, 0, 0, 0, s.loc)
	s.now = func() time.Time { return tuesday }
	if err := s.RunOnce(ctx, tuesday.Format("2006-01-02")); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if n := src.callCount(); n != 0 {
		t.Fatalf("no key configured: want 0 GroupedDaily calls, got %d", n)
	}
	if swapped {
		t.Fatal("no key configured: trend map must not be published")
	}
}

// AsOf must reflect the actual newest data on hand, not wall-clock "today" —
// at an intraday boot the newest stored close is a prior session.
func TestRecomputeTrendAsOfReflectsActualDataNotWallClock(t *testing.T) {
	s, db := newTestScheduler(t)
	ctx := context.Background()
	trepo := db.TrendRepo()
	s.trepo = trepo
	s.dsrc = &fakeDailySrc{hasKey: true}

	// Seed 20 weekday sessions; "today" in the test is the Tuesday right
	// after, before tonight's refresh has added that date.
	d := time.Date(2026, 6, 29, 0, 0, 0, 0, s.loc) // a Monday
	stored := ""
	for i := 0; i < 20; i++ {
		for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			d = d.AddDate(0, 0, 1)
		}
		date := d.Format("2006-01-02")
		if err := trepo.UpsertDailyBars(ctx, date, map[string]core.DailyBar{
			"AAA": {Date: date, C: 10 + float64(i)},
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		stored = date
		d = d.AddDate(0, 0, 1)
	}

	tuesday := time.Date(2026, 7, 28, 21, 0, 0, 0, s.loc)
	s.now = func() time.Time { return tuesday }
	if err := s.ensureCalendar(ctx); err != nil {
		t.Fatalf("ensureCalendar: %v", err)
	}
	var got map[string]core.TrendRef
	s.onTrend = func(m map[string]core.TrendRef) { got = m }

	s.recomputeTrend(ctx)

	ref, ok := got["AAA"]
	if !ok {
		t.Fatal("AAA should have classified from the 20 seeded closes")
	}
	if ref.AsOf != stored {
		t.Fatalf("AsOf must be the newest STORED date (%s), not wall-clock today (2026-07-28); got %s", stored, ref.AsOf)
	}
	if ref.AsOf == tuesday.Format("2006-01-02") {
		t.Fatal("AsOf must not equal wall-clock today when today's close isn't stored yet")
	}
}

// --- bull-flag detection, riding the same nightly pass ----------------------

// flagSeries returns n closes/volumes shaped like the textbook bull flag
// internal/pattern is tuned for: an advance from 10 to 20 on a million shares a
// day (the pole), then an 8-session coil between 19.20 and 20.20 on 40% of that
// volume (the base). 20.20 is the base high the live overlay breaks out over.
func flagSeries(n int) ([]float64, []int64) {
	base := []float64{19.60, 20.20, 19.40, 19.90, 19.20, 20.10, 19.70, 19.95}
	pole := n - len(base)
	closes, vols := make([]float64, 0, n), make([]int64, 0, n)
	for i := 0; i < pole; i++ {
		closes = append(closes, 10+10*float64(i)/float64(pole-1))
		vols = append(vols, 1_000_000)
	}
	for _, c := range base {
		closes = append(closes, c)
		vols = append(vols, 400_000)
	}
	return closes, vols
}

// flatSeries is n sessions of an unchanged price: it classifies (flat) but has
// no advance whatsoever, so it can never be a flag. Paired with flagSeries it
// proves the flag map holds SETUPS, not just every symbol the job looked at.
func flatSeries(n int) ([]float64, []int64) {
	closes, vols := make([]float64, n), make([]int64, n)
	for i := range closes {
		closes[i], vols[i] = 10, 1_000_000
	}
	return closes, vols
}

// seedBarsNoClose stores bars whose highs and lows trace the given prices while
// the CLOSE column stays 0 — the shape a halted or malformed grouped-daily row
// takes. pattern.Analyze measures such a row perfectly well (it reads H/L/V);
// trend.Classify cannot (it reads closes alone). That split is what
// TestRecomputeTrendFlagsSurviveUnknownTrend needs.
func seedBarsNoClose(t *testing.T, repo core.TrendRepo, sym string, dates []string, prices []float64, vols []int64) {
	t.Helper()
	if len(prices) != len(dates) || len(vols) != len(dates) {
		t.Fatalf("fixture length mismatch: %d dates, %d prices, %d volumes", len(dates), len(prices), len(vols))
	}
	ctx := context.Background()
	for i, d := range dates {
		p := prices[i]
		if err := repo.UpsertDailyBars(ctx, d, map[string]core.DailyBar{
			sym: {Date: d, O: p, H: p, L: p, C: 0, V: vols[i]},
		}); err != nil {
			t.Fatalf("seed %s %s: %v", sym, d, err)
		}
	}
}

// seedBars stores one bar per date for sym with H=L=C, so the geometry the
// detector measures is exactly the closes written here.
func seedBars(t *testing.T, repo core.TrendRepo, sym string, dates []string, closes []float64, vols []int64) {
	t.Helper()
	if len(closes) != len(dates) || len(vols) != len(dates) {
		t.Fatalf("fixture length mismatch: %d dates, %d closes, %d volumes", len(dates), len(closes), len(vols))
	}
	ctx := context.Background()
	for i, d := range dates {
		c := closes[i]
		if err := repo.UpsertDailyBars(ctx, d, map[string]core.DailyBar{
			sym: {Date: d, O: c, H: c, L: c, C: c, V: vols[i]},
		}); err != nil {
			t.Fatalf("seed %s %s: %v", sym, d, err)
		}
	}
}

// The recompute publishes BOTH overlays off its single LoadBars, and the flag
// map carries only the symbols that actually have a setup.
func TestRecomputeTrendPublishesFlagsAlongsideTrend(t *testing.T) {
	s, db := newTestScheduler(t)
	ctx := context.Background()
	trepo := db.TrendRepo()
	s.trepo = trepo
	s.dsrc = &fakeDailySrc{hasKey: true}

	tuesday := time.Date(2026, 7, 28, 21, 0, 0, 0, s.loc)
	s.now = func() time.Time { return tuesday }
	if err := s.ensureCalendar(ctx); err != nil {
		t.Fatalf("ensureCalendar: %v", err)
	}
	dates := s.recentTradingDates(ctx, tuesday.Format("2006-01-02"), trendWindowDays)
	if len(dates) < 25 {
		t.Fatalf("fixture needs a full window of sessions, got %d", len(dates))
	}
	cl, vl := flagSeries(len(dates))
	seedBars(t, trepo, "COIL", dates, cl, vl)
	cl, vl = flatSeries(len(dates))
	seedBars(t, trepo, "DEAD", dates, cl, vl)

	var trends map[string]core.TrendRef
	var flags map[string]core.FlagRef
	s.onTrend = func(m map[string]core.TrendRef) { trends = m }
	s.onFlags = func(m map[string]core.FlagRef) { flags = m }

	s.recomputeTrend(ctx)

	// Trend is unaffected: both symbols still classify.
	for _, sym := range []string{"COIL", "DEAD"} {
		if _, ok := trends[sym]; !ok {
			t.Fatalf("%s should still be in the trend map: %v", sym, trends)
		}
	}
	if flags == nil {
		t.Fatal("the flag map was never published")
	}
	if _, ok := flags["DEAD"]; ok {
		t.Fatal("DEAD never advanced — a symbol with no setup must be ABSENT (the overlay reads absent as Null)")
	}
	if len(flags) != 1 {
		t.Fatalf("flag map must hold only the symbols with a setup, got %v", flags)
	}
	ref, ok := flags["COIL"]
	if !ok {
		t.Fatalf("COIL is a textbook flag and must be in the flag map, got %v", flags)
	}
	if ref.State != 1 {
		t.Fatalf("nightly State must be 1 (coiling); breakout is the live overlay's call. got %v", ref.State)
	}
	if ref.BaseHigh != 20.20 {
		t.Fatalf("BaseHigh must be the coil's own high 20.20, got %v", ref.BaseHigh)
	}
	if ref.Score <= 0 || ref.Score > 100 {
		t.Fatalf("Score out of range: %v", ref.Score)
	}
	if ref.Bars < 3 {
		t.Fatalf("base length must be a real base, got %d bars", ref.Bars)
	}
	// AsOf comes from the newest BAR's date (pattern.Analyze takes no asOf).
	if want := dates[len(dates)-1]; ref.AsOf != want {
		t.Fatalf("AsOf want newest stored date %s, got %s", want, ref.AsOf)
	}
}

// The two verdicts are independent, and the recompute measures the flag BEFORE
// the classifier's `continue` so that neither can suppress the other. This pins
// the direction a reordering would silently break: a symbol whose closes are
// unusable — trend unknown — but whose highs and lows still trace a clean base
// must STILL reach the flag map. Moving the flag block below the classifier's
// unknown-verdict `continue` passes every other test in this file and loses
// these setups, so the invariant needs a test of its own.
func TestRecomputeTrendFlagsSurviveUnknownTrend(t *testing.T) {
	s, db := newTestScheduler(t)
	ctx := context.Background()
	trepo := db.TrendRepo()
	s.trepo = trepo
	s.dsrc = &fakeDailySrc{hasKey: true}

	tuesday := time.Date(2026, 7, 28, 21, 0, 0, 0, s.loc)
	s.now = func() time.Time { return tuesday }
	if err := s.ensureCalendar(ctx); err != nil {
		t.Fatalf("ensureCalendar: %v", err)
	}
	dates := s.recentTradingDates(ctx, tuesday.Format("2006-01-02"), trendWindowDays)
	if len(dates) < 25 {
		t.Fatalf("fixture needs a full window of sessions, got %d", len(dates))
	}
	// Same textbook coil as the test above, but written with a zeroed close.
	cl, vl := flagSeries(len(dates))
	seedBarsNoClose(t, trepo, "HALTED", dates, cl, vl)

	var trends map[string]core.TrendRef
	var flags map[string]core.FlagRef
	s.onTrend = func(m map[string]core.TrendRef) { trends = m }
	s.onFlags = func(m map[string]core.FlagRef) { flags = m }

	s.recomputeTrend(ctx)

	// The premise: the classifier really does refuse this symbol.
	if _, ok := trends["HALTED"]; ok {
		t.Fatalf("premise broken — HALTED must be UNKNOWN to the classifier: %v", trends)
	}
	ref, ok := flags["HALTED"]
	if !ok {
		t.Fatalf("an unknown TREND must not suppress a measurable FLAG: %v", flags)
	}
	if ref.State != 1 || ref.BaseHigh != 20.20 {
		t.Fatalf("the setup must be measured in full, got State %v BaseHigh %v", ref.State, ref.BaseHigh)
	}
}

// A pass where nothing sets up must publish an EMPTY (non-nil) map: that is a
// real "no setups tonight" verdict and has to clear yesterday's, unlike the nil
// map SetDailyFlags ignores.
func TestRecomputeTrendPublishesEmptyFlagMapWhenNothingSetsUp(t *testing.T) {
	s, db := newTestScheduler(t)
	ctx := context.Background()
	trepo := db.TrendRepo()
	s.trepo = trepo
	s.dsrc = &fakeDailySrc{hasKey: true}

	tuesday := time.Date(2026, 7, 28, 21, 0, 0, 0, s.loc)
	s.now = func() time.Time { return tuesday }
	if err := s.ensureCalendar(ctx); err != nil {
		t.Fatalf("ensureCalendar: %v", err)
	}
	dates := s.recentTradingDates(ctx, tuesday.Format("2006-01-02"), trendWindowDays)
	cl, vl := flatSeries(len(dates))
	seedBars(t, trepo, "DEAD", dates, cl, vl)

	s.onTrend = func(map[string]core.TrendRef) {}
	published := false
	var flags map[string]core.FlagRef
	s.onFlags = func(m map[string]core.FlagRef) { published, flags = true, m }

	s.recomputeTrend(ctx)

	if !published {
		t.Fatal("a pass with no setups must still publish, to clear stale flags")
	}
	if flags == nil {
		t.Fatal("published map must be non-nil — SetDailyFlags treats nil as 'keep the last good one'")
	}
	if len(flags) != 0 {
		t.Fatalf("no symbol set up; want an empty map, got %v", flags)
	}
}

// The nightly job end to end: one refresh of the daily bars feeds both
// overlays, with no second fetch and no second load.
func TestRunOncePublishesTrendAndFlags(t *testing.T) {
	s, db := newTestScheduler(t)
	ctx := context.Background()
	trepo := db.TrendRepo()

	tuesday := time.Date(2026, 7, 28, 21, 0, 0, 0, s.loc)
	s.now = func() time.Time { return tuesday }
	td := tuesday.Format("2006-01-02")
	if err := s.ensureCalendar(ctx); err != nil {
		t.Fatalf("ensureCalendar: %v", err)
	}
	// Exactly the dates RunOnce refetches: the trailing window plus today.
	dates := append(s.recentTradingDates(ctx, td, trendWindowDays-1), td)
	closes, vols := flagSeries(len(dates))
	byDate := make(map[string]core.DailyBar, len(dates))
	for i, d := range dates {
		c := closes[i]
		byDate[d] = core.DailyBar{Date: d, O: c, H: c, L: c, C: c, V: vols[i]}
	}
	src := &fakeDailySrc{hasKey: true}
	src.onCall = func(d string) (map[string]core.DailyBar, error) {
		b, ok := byDate[d]
		if !ok {
			return nil, nil
		}
		return map[string]core.DailyBar{"COIL": b}, nil
	}

	var trends map[string]core.TrendRef
	var flags map[string]core.FlagRef
	s.EnableDailyTrend(trepo, src, func(m map[string]core.TrendRef) { trends = m })
	s.EnableDailyFlags(func(m map[string]core.FlagRef) { flags = m })

	if err := s.RunOnce(ctx, td); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if _, ok := trends["COIL"]; !ok {
		t.Fatalf("nightly pass must publish the trend map, got %v", trends)
	}
	ref, ok := flags["COIL"]
	if !ok {
		t.Fatalf("nightly pass must publish the flag map from the same bars, got %v", flags)
	}
	if ref.State != 1 || ref.BaseHigh != 20.20 {
		t.Fatalf("want the coil (State 1, BaseHigh 20.20), got %+v", ref)
	}
	// Today's bar is part of the fetch, so the setup is stamped as of tonight.
	if ref.AsOf != td {
		t.Fatalf("AsOf want tonight's trade date %s, got %s", td, ref.AsOf)
	}
	if n := src.callCount(); n != len(dates) {
		t.Fatalf("want exactly one grouped fetch per date (%d), got %d — flags must add none", len(dates), n)
	}
}

// The empty-key boot gates flags exactly as it gates trend: nothing fetched,
// nothing published.
func TestDailyFlagsSkippedWithoutKey(t *testing.T) {
	s, db := newTestScheduler(t)
	ctx := context.Background()
	src := &fakeDailySrc{hasKey: false}
	s.EnableDailyTrend(db.TrendRepo(), src, func(map[string]core.TrendRef) {})
	swapped := false
	s.EnableDailyFlags(func(map[string]core.FlagRef) { swapped = true })

	tuesday := time.Date(2026, 6, 23, 21, 0, 0, 0, s.loc)
	s.now = func() time.Time { return tuesday }
	if err := s.RunOnce(ctx, tuesday.Format("2006-01-02")); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if n := src.callCount(); n != 0 {
		t.Fatalf("no key configured: want 0 GroupedDaily calls, got %d", n)
	}
	if swapped {
		t.Fatal("no key configured: the flag map must not be published")
	}
}
