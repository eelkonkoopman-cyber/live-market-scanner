package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// withPMH gives the ticker a final pre-market high, complete or not.
func withPMH(ts *tickerState, hi float32, complete bool) {
	ts.frozenPre = frozenScope{set: true, hiSet: true, hi: hi, complete: complete}
}

func TestLevelPmhBreak(t *testing.T) {
	ts := newTS5()
	print5(ts, c0+1, 9.80, 9.80) // last before the level is known: below it
	withPMH(ts, 10.00, true)

	print5(ts, c0+2, 9.90, 9.95) // still below: armed, no break
	if r := row5(ts); !core.IsNull(r.BrkPmhAge) || r.VsPmh >= 0 {
		t.Fatalf("below the PMH: want no break and vsPmh < 0, got age %v vs %v", r.BrkPmhAge, r.VsPmh)
	}
	printHL(ts, c0+3, 10.04, 9.97, 10.02) // trades through 10.00
	r := row5(ts)
	if r.BrkPmhAge != 0 || r.VsPmh <= 0 {
		t.Fatalf("broke the PMH: want age 0 and vsPmh > 0, got age %v vs %v", r.BrkPmhAge, r.VsPmh)
	}
	ts.syncTo(c0 + 30)
	if r = row5(ts); r.BrkPmhAge != 26 { // broke in the second ending at c0+4
		t.Fatalf("age want 26 got %v", r.BrkPmhAge)
	}
}

// The core false-breakout guard: a pre-market high from an engine that started
// mid-premarket can be too low, so it must not be used as a level at all.
func TestLevelPmhIncompleteIsNoLevel(t *testing.T) {
	ts := newTS5()
	print5(ts, c0+1, 9.80, 9.80)
	withPMH(ts, 10.00, false)
	printHL(ts, c0+2, 10.50, 10.40, 10.45)
	if r := row5(ts); !core.IsNull(r.BrkPmhAge) || !core.IsNull(r.VsPmh) {
		t.Fatalf("incomplete PMH must yield no level: age %v vs %v", r.BrkPmhAge, r.VsPmh)
	}
}

// A stock already trading above a level when the level arrives broke it at
// some unknown earlier time: not a fresh break. It re-arms on a close back at
// or below the level, and the next push through IS a break.
func TestLevelAlreadyAboveIsNotFresh(t *testing.T) {
	ts := newTS5()
	print5(ts, c0+1, 10.30, 10.30)
	withPMH(ts, 10.00, true) // e.g. a restart after the real break

	printHL(ts, c0+2, 10.40, 10.25, 10.35)
	if r := row5(ts); !core.IsNull(r.BrkPmhAge) {
		t.Fatalf("already above when the level arrived: not a fresh break, got %v", r.BrkPmhAge)
	}
	printHL(ts, c0+3, 10.10, 9.95, 9.98) // closes back below: re-armed
	if r := row5(ts); !core.IsNull(r.BrkPmhAge) {
		t.Fatalf("a close below is not a break, got %v", r.BrkPmhAge)
	}
	printHL(ts, c0+4, 10.06, 9.99, 10.05) // reclaims
	if r := row5(ts); r.BrkPmhAge != 0 {
		t.Fatalf("the reclaim is a fresh break: want 0 got %v", r.BrkPmhAge)
	}
}

func TestLevelTouchIsNotABreak(t *testing.T) {
	ts := newTS5()
	print5(ts, c0+1, 9.90, 9.90)
	withPMH(ts, 10.00, true)
	printHL(ts, c0+2, 10.00, 9.95, 9.99) // high exactly at the level
	if r := row5(ts); !core.IsNull(r.BrkPmhAge) {
		t.Fatalf("touching the level is a test, not a break: got %v", r.BrkPmhAge)
	}
}

// Holding above the level doesn't re-fire; only a close back below re-arms.
func TestLevelNoRefireWhileHoldingAbove(t *testing.T) {
	ts := newTS5()
	print5(ts, c0+1, 9.90, 9.90)
	withPMH(ts, 10.00, true)
	printHL(ts, c0+2, 10.05, 9.99, 10.04) // break
	printHL(ts, c0+3, 10.20, 10.10, 10.15)
	printHL(ts, c0+4, 10.30, 10.20, 10.25)
	if r := row5(ts); r.BrkPmhAge != 2 { // still the c0+2 break (ended c0+3), now c0+5
		t.Fatalf("holding above must not re-fire: want age 2 got %v", r.BrkPmhAge)
	}
}

func TestLevelPrevAhBreakAndReset(t *testing.T) {
	ts := newTS5()
	print5(ts, c0+1, 5.00, 5.00)
	ts.prevAhHi = 5.20
	printHL(ts, c0+2, 5.25, 5.10, 5.22)
	r := row5(ts)
	if r.BrkPrevAhAge != 0 || !near(r.PrevAhHigh, 5.20) || r.VsPrevAh <= 0 {
		t.Fatalf("broke the prev AH high: age %v level %v vs %v", r.BrkPrevAhAge, r.PrevAhHigh, r.VsPrevAh)
	}
	ts.resetSession()
	print5(ts, c0+3600, 5.30, 5.30)
	if r = row5(ts); !core.IsNull(r.PrevAhHigh) || !core.IsNull(r.BrkPrevAhAge) {
		t.Fatalf("the day reset must clear yesterday's level: level %v age %v", r.PrevAhHigh, r.BrkPrevAhAge)
	}
}

// A changed level (a re-seed) rebuilds the tracker: a break against the old
// level must not be reported against the new one.
func TestLevelChangeDropsOldBreak(t *testing.T) {
	ts := newTS5()
	print5(ts, c0+1, 5.00, 5.00)
	ts.prevAhHi = 5.20
	printHL(ts, c0+2, 5.25, 5.10, 5.22) // breaks 5.20
	ts.prevAhHi = 5.50                  // corrected upward
	if r := row5(ts); !core.IsNull(r.BrkPrevAhAge) {
		t.Fatalf("the 5.20 break is not a break of 5.50: got %v", r.BrkPrevAhAge)
	}
	printHL(ts, c0+3, 5.40, 5.20, 5.30) // below 5.50, armed (prev last 5.22 <= 5.50)
	printHL(ts, c0+4, 5.55, 5.35, 5.52)
	if r := row5(ts); r.BrkPrevAhAge != 0 {
		t.Fatalf("break of the corrected level: want 0 got %v", r.BrkPrevAhAge)
	}
}

// Through the real engine: the 09:30 freeze marks the pre-market high complete
// only if the pre scope started at the real 04:00 open; SetPrevAhHigh seeds.
func TestEngineLevelsThroughSnapshot(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	base := (time.Now().Unix()/300 + 12) * 300 // future prints: wall ticks can't move baseSec
	agg := func(sym string, sec int64, hi, lo, cl float64) core.Agg {
		return core.Agg{Sym: sym, Open: cl, High: hi, Low: lo, Close: cl, Vol: 100, StartMs: sec * 1000, EndMs: (sec + 1) * 1000}
	}
	drain := func(e *Engine) { _ = e.Curves(0) } // ctrl round-trip: earlier ctrl ops have landed

	run := func(t *testing.T, preStart time.Time) *Engine {
		var clock atomic.Pointer[time.Time]
		clock.Store(&preStart)
		e := New(Options{TickHz: 50, Now: func() time.Time { return *clock.Load() }})
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go func() { _ = e.Run(ctx) }()
		e.SetSession(core.SessionPre)
		drain(e)
		e.Apply(agg("PMH", base+10, 10.00, 9.80, 9.90)) // pre-market high 10.00
		e.Apply(agg("PMH", base+20, 9.95, 9.85, 9.90))
		open := time.Date(preStart.Year(), preStart.Month(), preStart.Day(), 9, 30, 0, 0, ny)
		clock.Store(&open)
		e.SetSession(core.SessionRegular) // freeze
		drain(e)
		e.Apply(agg("PMH", base+30, 10.05, 9.95, 10.03)) // trades through 10.00
		return e
	}

	t.Run("watched since 04:00: complete, break reported", func(t *testing.T) {
		e := run(t, time.Date(2026, 10, 5, 4, 0, 5, 0, ny))
		waitForCond(t, e, "PMH", 3*time.Second, func(r core.Row) bool {
			return r.BrkPmhAge == 0 && r.VsPmh > 0
		})
	})
	t.Run("started mid-premarket: incomplete, no level", func(t *testing.T) {
		e := run(t, time.Date(2026, 10, 5, 7, 43, 0, 0, ny))
		waitForCond(t, e, "PMH", 3*time.Second, func(r core.Row) bool {
			return near(r.Last, 10.03) && core.IsNull(r.BrkPmhAge) && core.IsNull(r.VsPmh)
		})
	})
	t.Run("SetPrevAhHigh seeds a breakable level", func(t *testing.T) {
		e := New(Options{TickHz: 50})
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go func() { _ = e.Run(ctx) }()
		e.Apply(agg("AHH", base+10, 5.00, 4.95, 5.00))
		waitForSym(t, e, "AHH", 3*time.Second)
		e.SetPrevAhHigh(map[string]float64{"AHH": 5.20, "NOPE": 1})
		drain(e)
		e.Apply(agg("AHH", base+20, 5.25, 5.10, 5.22))
		waitForCond(t, e, "AHH", 3*time.Second, func(r core.Row) bool {
			return r.BrkPrevAhAge == 0 && near(r.PrevAhHigh, 5.20)
		})
	})
}

// SeedFrozen is prefill's full-session REST data: complete by construction.
func TestSeedFrozenMarksPreComplete(t *testing.T) {
	e := New(Options{TickHz: 50})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = e.Run(ctx) }()
	base := (time.Now().Unix()/300 + 12) * 300
	e.Apply(core.Agg{Sym: "SFZ", High: 9.9, Low: 9.8, Close: 9.9, Vol: 1, StartMs: base * 1000, EndMs: (base + 1) * 1000})
	waitForSym(t, e, "SFZ", 3*time.Second)
	e.SeedFrozen(core.SessionPre, map[string]core.SessionStats{"SFZ": {Hi: 10, Lo: 9, Close: 9.9, Vol: 1000}})
	_ = e.Curves(0)
	e.Apply(core.Agg{Sym: "SFZ", High: 10.1, Low: 9.95, Close: 10.05, Vol: 1, StartMs: (base + 1) * 1000, EndMs: (base + 2) * 1000})
	waitForCond(t, e, "SFZ", 3*time.Second, func(r core.Row) bool { return r.BrkPmhAge == 0 })
}
