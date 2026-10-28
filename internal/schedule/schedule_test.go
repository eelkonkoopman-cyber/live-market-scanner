package schedule

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/persist"
	"github.com/eelkonkoopman/marketscanner/internal/universe"
)

type fakeEng struct {
	curves   map[string][]int64
	baseline *core.BaselineTable
	floats   map[string]core.FloatRef
}

func (f *fakeEng) Curves(int) map[string][]int64        { return f.curves }
func (f *fakeEng) SetBaseline(b *core.BaselineTable)    { f.baseline = b }
func (f *fakeEng) Baseline() *core.BaselineTable        { return f.baseline }
func (f *fakeEng) SetFloats(m map[string]core.FloatRef) { f.floats = m }

type fakeFloatSrc struct{}

func (fakeFloatSrc) Floats(context.Context, []string) ([]core.FloatRef, error) { return nil, nil }

func TestRunOnceRecomputesBaseline(t *testing.T) {
	ctx := context.Background()
	db, err := persist.Open(ctx, filepath.Join(t.TempDir(), "sched.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	uni := universe.NewAll()
	uni.Replace([]string{"AAA"}, nil)
	fe := &fakeEng{curves: map[string][]int64{"AAA": {0, 0, 500}}} // bucket 2 cumulative = 500

	s := New(db.BaselineRepo(), db.FloatRepo(), db.CalendarRepo(), fe, fakeFloatSrc{}, uni)
	monday := time.Date(2026, 6, 22, 21, 0, 0, 0, s.loc) // a Monday, after close
	s.now = func() time.Time { return monday }
	date := monday.Format("2006-01-02")

	if err := s.RunOnce(ctx, date); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if has, _ := db.BaselineRepo().HasCurveForDate(ctx, date); !has {
		t.Fatal("engine curves were not flushed to history")
	}
	if fe.baseline == nil {
		t.Fatal("recomputed baseline was not swapped into the engine")
	}
	bt, err := db.BaselineRepo().LoadBaseline(ctx)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	if v, ok := bt.Get("AAA", 2); !ok || v != 500 {
		t.Fatalf("baseline[AAA, bucket 2] want 500, got %v ok=%v", v, ok)
	}
	// One stored session: RVOL needs 5, so the honest status is "building".
	if s.BaselineStatus() != "building" {
		t.Fatalf("baseline status want building (1 of 5 sessions), got %q", s.BaselineStatus())
	}
}
