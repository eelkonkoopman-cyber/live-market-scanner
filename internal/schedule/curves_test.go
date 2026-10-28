package schedule

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/persist"
	"github.com/eelkonkoopman/marketscanner/internal/universe"
)

func newTestSched(t *testing.T, fe *fakeEng) (*Scheduler, *persist.DB) {
	t.Helper()
	db, err := persist.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db.BaselineRepo(), db.FloatRepo(), db.CalendarRepo(), fe, fakeFloatSrc{}, universe.NewAll()), db
}

// curve builds an n-bucket cumulative curve with the given bucket values.
func curve(n int, vals map[int]int64) []int64 {
	c := make([]int64, n)
	for b, v := range vals {
		c[b] = v
	}
	return c
}

// storedBucket reads AAA's stored value for one day and bucket (via a one-day
// baseline, the repo's only read path for curve history).
func storedBucket(t *testing.T, db *persist.DB, day string, bucket int) (float64, bool) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.BaselineRepo().RecomputeBaseline(ctx, day, []string{day}); err != nil {
		t.Fatal(err)
	}
	bt, err := db.BaselineRepo().LoadBaseline(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return bt.Get("AAA", bucket)
}

func TestFlushTodayOnlyInsideTheTradingDay(t *testing.T) {
	fe := &fakeEng{curves: map[string][]int64{"AAA": curve(20, map[int]int64{0: 999})}}
	s, db := newTestSched(t, fe)
	ctx := context.Background()
	for _, at := range []time.Time{
		time.Date(2026, 10, 5, 3, 50, 0, 0, s.loc),  // before the 04:00 reset: still yesterday's curve
		time.Date(2026, 10, 5, 4, 3, 0, 0, s.loc),   // inside the reset margin
		time.Date(2026, 10, 5, 20, 30, 0, 0, s.loc), // after the close
		time.Date(2026, 10, 4, 11, 0, 0, 0, s.loc),  // a Sunday
	} {
		s.now = func() time.Time { return at }
		s.flushToday(ctx)
	}
	for _, d := range []string{"2026-10-05", "2026-10-04"} {
		if has, _ := db.BaselineRepo().HasCurveForDate(ctx, d); has {
			t.Fatalf("nothing may be saved outside 04:05-20:00 ET on a trading day (%s)", d)
		}
	}
}

func TestFlushTodayIsIncremental(t *testing.T) {
	fe := &fakeEng{curves: map[string][]int64{"AAA": curve(13, map[int]int64{2: 100, 12: 500})}}
	s, db := newTestSched(t, fe)
	ctx := context.Background()
	s.now = func() time.Time { return time.Date(2026, 10, 5, 5, 0, 0, 0, s.loc) } // bucket 12
	s.flushToday(ctx)
	if v, ok := storedBucket(t, db, "2026-10-05", 2); !ok || v != 100 {
		t.Fatalf("first save stores what was observed: got %v %v", v, ok)
	}
	// The engine now reports a much larger early bucket; an incremental save
	// must not resend buckets before the last save's bucket (12).
	fe.curves = map[string][]int64{"AAA": curve(19, map[int]int64{2: 7777, 12: 600, 18: 900})}
	s.now = func() time.Time { return time.Date(2026, 10, 5, 5, 30, 0, 0, s.loc) } // bucket 18
	s.flushToday(ctx)
	if v, _ := storedBucket(t, db, "2026-10-05", 2); v != 100 {
		t.Fatalf("already-saved buckets must not be resent: bucket 2 = %v", v)
	}
	if v, _ := storedBucket(t, db, "2026-10-05", 12); v != 600 {
		t.Fatalf("the bucket in progress at the last save IS resent: bucket 12 = %v", v)
	}
	if v, _ := storedBucket(t, db, "2026-10-05", 18); v != 900 {
		t.Fatalf("new buckets are saved: bucket 18 = %v", v)
	}
}

func TestShutdownSavesTheDay(t *testing.T) {
	fe := &fakeEng{curves: map[string][]int64{"AAA": curve(73, map[int]int64{72: 4242})}}
	s, db := newTestSched(t, fe)
	s.now = func() time.Time { return time.Date(2026, 10, 5, 10, 0, 0, 0, s.loc) } // bucket 72
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = s.Run(ctx); close(done) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if has, _ := db.BaselineRepo().HasCurveForDate(context.Background(), "2026-10-05"); !has {
		t.Fatal("closing the app must save today's curve")
	}
}

func TestEnsureBaselineFreshExcludesTodayAndNeedsFive(t *testing.T) {
	fe := &fakeEng{}
	s, db := newTestSched(t, fe)
	ctx := context.Background()
	s.now = func() time.Time { return time.Date(2026, 10, 5, 8, 0, 0, 0, s.loc) }
	if err := s.ensureCalendar(ctx); err != nil {
		t.Fatal(err)
	}
	br := db.BaselineRepo()
	prior := []string{"2026-09-28", "2026-09-29", "2026-09-30", "2026-10-01", "2026-10-02"}
	for i, d := range prior {
		if err := br.UpsertVolumeCurves(ctx, d, map[string][]int64{"AAA": {0, int64(100 * (i + 1))}}); err != nil {
			t.Fatal(err)
		}
	}
	// Today's partial curve, saved earlier this morning: must NOT feed today's baseline.
	if err := br.UpsertVolumeCurves(ctx, "2026-10-05", map[string][]int64{"AAA": {0, 1_000_000}}); err != nil {
		t.Fatal(err)
	}
	s.ensureBaselineFresh(ctx)
	if fe.baseline == nil {
		t.Fatal("a stale baseline must be recomputed and swapped in at startup")
	}
	if v, ok := fe.baseline.Get("AAA", 1); !ok || v != 300 { // mean(100..500)
		t.Fatalf("baseline must cover the 5 prior sessions only, not today's partial: want 300 got %v", v)
	}
	if s.BaselineStatus() != "ready" {
		t.Fatalf("5 stored sessions: want ready, got %q", s.BaselineStatus())
	}
	fe.baseline = nil // already fresh: a second call must not recompute
	s.ensureBaselineFresh(ctx)
	if fe.baseline != nil {
		t.Fatal("must not recompute when the baseline already includes the previous session")
	}
}

func TestEnsureBaselineFreshBuildingBelowFive(t *testing.T) {
	fe := &fakeEng{}
	s, db := newTestSched(t, fe)
	ctx := context.Background()
	s.now = func() time.Time { return time.Date(2026, 10, 5, 8, 0, 0, 0, s.loc) }
	if err := s.ensureCalendar(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.BaselineRepo().UpsertVolumeCurves(ctx, "2026-10-02", map[string][]int64{"AAA": {0, 100}}); err != nil {
		t.Fatal(err)
	}
	s.ensureBaselineFresh(ctx)
	if s.BaselineStatus() != "building" {
		t.Fatalf("1 stored session: RVOL is still null everywhere, want building, got %q", s.BaselineStatus())
	}
}

// The backfill writes past days for a handful of tickers: only theirs are
// recomputed, every other ticker's baseline stays exactly as it was.
func TestRefreshBaselineForReplacesOnlyThoseTickers(t *testing.T) {
	fe := &fakeEng{}
	s, db := newTestSched(t, fe)
	ctx := context.Background()
	s.now = func() time.Time { return time.Date(2026, 10, 5, 8, 0, 0, 0, s.loc) }
	if err := s.ensureCalendar(ctx); err != nil {
		t.Fatal(err)
	}
	br := db.BaselineRepo()
	days := []string{"2026-09-28", "2026-09-29", "2026-09-30", "2026-10-01", "2026-10-02"}
	for _, d := range days {
		if err := br.UpsertVolumeCurves(ctx, d, map[string][]int64{"KEEP": {0, 100}}); err != nil {
			t.Fatal(err)
		}
	}
	s.ensureBaselineFresh(ctx) // KEEP: 100 at bucket 1
	// Rebuilt history lands for NEW (and, in the DB only, a change to KEEP that
	// this refresh must NOT pick up: it wasn't in the list).
	for _, d := range days {
		if err := br.UpsertVolumeCurves(ctx, d, map[string][]int64{"NEW": {0, 400}, "KEEP": {0, 900}}); err != nil {
			t.Fatal(err)
		}
	}
	before := fe.baseline
	s.RefreshBaselineFor(ctx, []string{"NEW"})
	if v, ok := fe.baseline.Get("NEW", 1); !ok || v != 400 || fe.baseline.CountAt("NEW", 1) != 5 {
		t.Fatalf("the rebuilt ticker's baseline is swapped in: got %v %v", v, ok)
	}
	if v, _ := fe.baseline.Get("KEEP", 1); v != 100 {
		t.Fatalf("tickers outside the list keep their baseline: KEEP = %v", v)
	}
	if _, ok := before.Get("NEW", 1); ok {
		t.Fatal("the table the engine was reading must not be modified in place")
	}
	if s.BaselineStatus() != "ready" {
		t.Fatalf("want ready, got %q", s.BaselineStatus())
	}
}

func TestBaselineDaysIsTheWindowBeforeToday(t *testing.T) {
	s, _ := newTestSched(t, &fakeEng{})
	ctx := context.Background()
	s.now = func() time.Time { return time.Date(2026, 10, 5, 8, 0, 0, 0, s.loc) }
	if err := s.ensureCalendar(ctx); err != nil {
		t.Fatal(err)
	}
	days, err := s.BaselineDays(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != s.window {
		t.Fatalf("want the %d-day window, got %d", s.window, len(days))
	}
	if days[0].Date != "2026-10-02" || days[1].Date != "2026-10-01" {
		t.Fatalf("most recent first, ending at the previous session (never today): %s, %s", days[0].Date, days[1].Date)
	}
	if days[0].RegCloseMin != 960 || days[0].LastBucket != 191 {
		t.Fatalf("calendar rows ride along: %+v", days[0])
	}
}
