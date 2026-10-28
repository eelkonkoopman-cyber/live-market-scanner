package health

import (
	"sync"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// newTestTracker builds a Tracker whose clock is a fake starting at start,
// plus the function tests use to advance it — so every transition below can
// assert exact Since/LastOK epoch-ms values instead of just relative order.
func newTestTracker(start time.Time) (tr *Tracker, advance func(time.Duration), at func() int64) {
	cur := start
	tr = New()
	tr.now = func() time.Time { return cur }
	advance = func(d time.Duration) { cur = cur.Add(d) }
	at = func() int64 { return cur.UnixMilli() }
	return tr, advance, at
}

// TestFirstTouch covers SourceOK/SourceFail/Off each landing on a name the
// Tracker has never seen before — the unknown -> {ok,failing,off} edges.
func TestFirstTouch(t *testing.T) {
	base := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		op   func(tr *Tracker)
		want core.SourceHealth
	}{
		{
			name: "SourceOK on unknown source",
			op:   func(tr *Tracker) { tr.SourceOK("s") },
			want: core.SourceHealth{State: "ok", Since: base.UnixMilli(), LastOK: base.UnixMilli()},
		},
		{
			name: "SourceFail on unknown source",
			op:   func(tr *Tracker) { tr.SourceFail("s", "boom") },
			want: core.SourceHealth{State: "failing", Since: base.UnixMilli(), Msg: "boom"},
		},
		{
			name: "Off on unknown source",
			op:   func(tr *Tracker) { tr.Off("s") },
			want: core.SourceHealth{State: "off", Since: base.UnixMilli()},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr, _, _ := newTestTracker(base)
			c.op(tr)
			got := tr.Snapshot()["s"]
			if got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

// TestTransitionSequence walks one source through every state transition in
// order, asserting the Since/LastOK/Msg semantics at each step: Since only
// moves on an actual state change, LastOK only moves on SourceOK, and Msg
// tracks (or clears with) the current state.
func TestTransitionSequence(t *testing.T) {
	base := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	tr, advance, at := newTestTracker(base)

	check := func(step string, want core.SourceHealth) {
		t.Helper()
		got := tr.Snapshot()["x"]
		if got != want {
			t.Fatalf("%s: got %+v, want %+v", step, got, want)
		}
	}

	// 1. Fail on an unknown source: failing, Since = now, Msg set.
	advance(time.Second)
	tFail1 := at()
	tr.SourceFail("x", "first failure")
	check("fail on unknown", core.SourceHealth{State: "failing", Since: tFail1, Msg: "first failure"})

	// 2. Fail again while already failing: Msg updates, Since UNCHANGED.
	advance(time.Second)
	tr.SourceFail("x", "second failure")
	check("fail while failing", core.SourceHealth{State: "failing", Since: tFail1, Msg: "second failure"})

	// 3. OK while failing: -> ok, Since resets to now, LastOK = now, Msg cleared.
	advance(time.Second)
	tOK1 := at()
	tr.SourceOK("x")
	check("ok while failing", core.SourceHealth{State: "ok", Since: tOK1, LastOK: tOK1})

	// 4. OK again while already ok: Since UNCHANGED, LastOK advances.
	advance(time.Second)
	tOK2 := at()
	tr.SourceOK("x")
	check("ok while ok", core.SourceHealth{State: "ok", Since: tOK1, LastOK: tOK2})

	// 5. Fail while ok: -> failing, Since resets to now, LastOK PRESERVED
	// (it still records the last real success, not zeroed by a later failure).
	advance(time.Second)
	tFail2 := at()
	tr.SourceFail("x", "dropped")
	check("fail while ok", core.SourceHealth{State: "failing", Since: tFail2, Msg: "dropped", LastOK: tOK2})

	// 6. Off: -> off, Since resets to now, Msg cleared, LastOK still preserved.
	advance(time.Second)
	tOff := at()
	tr.Off("x")
	check("off", core.SourceHealth{State: "off", Since: tOff, LastOK: tOK2})

	// 7. OK after Off: -> ok again, Since resets, LastOK advances, Msg cleared.
	advance(time.Second)
	tOK3 := at()
	tr.SourceOK("x")
	check("ok after off", core.SourceHealth{State: "ok", Since: tOK3, LastOK: tOK3})
}

// TestSnapshotIsIndependentCopy proves mutating a map returned by Snapshot
// can never corrupt the Tracker's own state, since Snapshot is read
// concurrently with writes from every source's own goroutine.
func TestSnapshotIsIndependentCopy(t *testing.T) {
	tr, _, _ := newTestTracker(time.Now())
	tr.SourceOK("a")

	snap := tr.Snapshot()
	snap["a"] = core.SourceHealth{State: "tampered"}
	snap["b"] = core.SourceHealth{State: "injected"}

	again := tr.Snapshot()
	if again["a"].State != "ok" {
		t.Fatalf("mutating a snapshot leaked into the tracker: %+v", again["a"])
	}
	if _, ok := again["b"]; ok {
		t.Fatalf("mutating a snapshot injected a new source into the tracker")
	}
}

// TestTrackerConcurrentAccess exercises the mutex under -race: many sources
// reporting OK/Fail concurrently, plus concurrent Snapshot reads, must never
// race or panic.
func TestTrackerConcurrentAccess(t *testing.T) {
	tr := New()
	var wg sync.WaitGroup
	names := []string{"bzpush", "rtpr", "rtprAlerts", "edgar", "massive"}
	for _, name := range names {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if i%2 == 0 {
					tr.SourceOK(name)
				} else {
					tr.SourceFail(name, "transient")
				}
			}
		}(name)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = tr.Snapshot()
		}
	}()
	wg.Wait()

	snap := tr.Snapshot()
	if len(snap) != len(names) {
		t.Fatalf("expected %d sources, got %d: %+v", len(names), len(snap), snap)
	}
}

// TestHealthReporterInterface documents that *Tracker is usable anywhere a
// core.HealthReporter is expected — the shape every instrumented source
// depends on.
func TestHealthReporterInterface(t *testing.T) {
	var r core.HealthReporter = New()
	r.SourceOK("x")        // must not panic
	r.SourceFail("x", "y") // must not panic
}
