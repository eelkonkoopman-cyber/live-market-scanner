package dlevel

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestLowTrackerBreakClearAndRebreak(t *testing.T) {
	var lt LowTracker
	lt.Set(50)
	// still above: nothing
	lt.Step(51, 50.5, 51, 1000)
	if _, _, ok := lt.Best(51, 52, true); ok {
		t.Fatal("above PDL must not count")
	}
	// cross below
	lt.Step(50.2, 49.8, 49.9, 2000)
	if p, ms, ok := lt.Best(49.9, 52, true); !ok || p != 50 || ms != 2000 {
		t.Fatalf("break: %v %v %v", p, ms, ok)
	}
	// deeper prints keep the first time
	lt.Step(49.9, 49.0, 49.1, 3000)
	if _, ms, _ := lt.Best(49.1, 52, true); ms != 2000 {
		t.Fatalf("time moved: %v", ms)
	}
	// close back above clears
	lt.Step(49.1, 49.1, 50.5, 4000)
	if _, _, ok := lt.Best(50.5, 52, true); ok {
		t.Fatal("back above must clear")
	}
	// re-break with a new time
	lt.Step(50.5, 49.5, 49.6, 5000)
	if _, ms, ok := lt.Best(49.6, 52, true); !ok || ms != 5000 {
		t.Fatalf("rebreak: %v %v", ms, ok)
	}
}

func TestLowTrackerGapDownAndAlreadyBelow(t *testing.T) {
	var lt LowTracker
	lt.Set(50)
	lt.Step(52, 48, 48.5, 1000) // first print, ref = prev close above the PDL
	if _, ms, ok := lt.Best(48.5, 52, true); !ok || ms != 1000 {
		t.Fatalf("gap down: %v %v", ms, ok)
	}
	var b LowTracker
	b.Set(50)
	b.Step(49, 48, 48.5, 1000) // never traded at/above the PDL: no break
	if _, _, ok := b.Best(48.5, 0, false); ok {
		t.Fatal("started below PDL must not count")
	}
	b.Step(0, 48, 48.5, 2000) // unknown ref
	if _, _, ok := b.Best(48.5, 0, false); ok {
		t.Fatal("unknown ref must not count")
	}
}

func TestLowTrackerRestartFallbackAndReset(t *testing.T) {
	var lt LowTracker
	lt.Set(50)
	if p, ms, ok := lt.Best(49, 51, true); !ok || p != 50 || ms != 0 {
		t.Fatalf("fallback: %v %v %v", p, ms, ok)
	}
	if _, _, ok := lt.Best(49, 49.5, true); ok {
		t.Fatal("high below PDL: no fallback")
	}
	lt.Step(50.5, 49, 49.2, 9)
	lt.Set(50) // same PDL keeps state
	if _, ms, _ := lt.Best(49.2, 0, false); ms != 9 {
		t.Fatal("Set(same) lost state")
	}
	lt.Reset()
	if _, _, ok := lt.Best(49.2, 0, false); ok {
		t.Fatal("Reset must clear")
	}
	lt.Set(48) // new PDL clears
}

func TestPrevLow(t *testing.T) {
	bars := []core.DailyBar{{Date: "2026-10-14", L: 40}, {Date: "2026-10-15", L: 42.5}, {Date: "2026-10-16", L: 1}}
	if PrevLow(bars, "2026-10-16", true) != 42.5 {
		t.Fatal("prev low")
	}
	if PrevLow(bars, "2026-10-16", false) != 0 {
		t.Fatal("stale must omit")
	}
}
