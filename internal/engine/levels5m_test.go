package engine

import (
	"context"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// candle5 prints 5-minute candle k (from c0): an early print reaching `hi`,
// then a late print closing at `c`.
func candle5(ts *tickerState, k int, hi, c float64) {
	s := c0 + int64(k)*300
	ts.applyAgg(core.Agg{Sym: ts.sym, Open: c, High: hi, Low: min(c, hi), Close: min(c, hi), Vol: 100,
		StartMs: (s + 10) * 1000, EndMs: (s + 11) * 1000})
	ts.applyAgg(core.Agg{Sym: ts.sym, Open: c, High: c, Low: c, Close: c, Vol: 100,
		StartMs: (s + 290) * 1000, EndMs: (s + 291) * 1000})
}

// end5 closes candle k via the 1 Hz clock (no next-candle print).
func end5(ts *tickerState, k int) {
	sec := c0 + int64(k+1)*300 + 3
	ts.syncTo(sec)
	ts.tick5m(sec)
}

func TestLevel5mWickIsNotACloseBreak(t *testing.T) {
	ts := newTS5()
	candle5(ts, 0, 9.90, 9.90)
	end5(ts, 0)
	withPMH(ts, 10.00, true)

	candle5(ts, 1, 10.20, 9.95) // wicks through 10.00, closes back under
	end5(ts, 1)
	r := row5(ts)
	if core.IsNull(r.BrkPmhAge) {
		t.Fatal("precondition: the trade-level field DOES see the wick")
	}
	if !core.IsNull(r.BrkPmh5mAge) {
		t.Fatalf("a wick that closes back under is not a 5-minute break, got %v", r.BrkPmh5mAge)
	}

	candle5(ts, 2, 10.15, 10.10) // closes above
	end5(ts, 2)
	end := (c0 + 3*300) * 1000
	if ts.pmh5.brkMs != end {
		t.Fatalf("the close above must break at the candle's END: %d want %d", ts.pmh5.brkMs, end)
	}
	if r = row5(ts); r.BrkPmh5mAge != 3 { // clock is 3 s past the candle end
		t.Fatalf("age from the candle close want 3 got %v", r.BrkPmh5mAge)
	}
}

func TestLevel5mNextPrintClosesTheCandle(t *testing.T) {
	ts := newTS5()
	candle5(ts, 0, 9.90, 9.90)
	withPMH(ts, 10.00, true)
	candle5(ts, 1, 10.10, 10.05) // closes above; no clock tick
	if ts.pmh5.brkMs != 0 {
		t.Fatal("not closed yet: the window hasn't ended and nothing has printed after it")
	}
	candle5(ts, 2, 10.06, 10.06) // the next candle's first print closes candle 1
	if want := (c0 + 2*300) * 1000; ts.pmh5.brkMs != want {
		t.Fatalf("the next candle's first print must close candle 1: %d want %d", ts.pmh5.brkMs, want)
	}
}

func TestLevel5mClockWaitsOutTheGrace(t *testing.T) {
	ts := newTS5()
	candle5(ts, 0, 9.90, 9.90)
	end5(ts, 0)
	withPMH(ts, 10.00, true)
	candle5(ts, 1, 10.10, 10.05)
	sec := c0 + 2*300 + 2 // 2 s after the window: inside the grace
	ts.syncTo(sec)
	ts.tick5m(sec)
	if ts.pmh5.brkMs != 0 {
		t.Fatal("must wait out the grace for late per-second aggregates")
	}
	ts.syncTo(sec + 1)
	ts.tick5m(sec + 1)
	if ts.pmh5.brkMs == 0 {
		t.Fatal("after the grace the clock must close the candle")
	}
}

func TestLevel5mCloseAtLevelIsNotAbove(t *testing.T) {
	ts := newTS5()
	candle5(ts, 0, 9.90, 9.90)
	end5(ts, 0)
	withPMH(ts, 10.00, true)
	candle5(ts, 1, 10.00, 10.00) // closes exactly at the level
	end5(ts, 1)
	if ts.pmh5.brkMs != 0 || !ts.pmh5.armed {
		t.Fatalf("closing AT the level is not above it (and re-arms): brk %d armed %v", ts.pmh5.brkMs, ts.pmh5.armed)
	}
}

func TestLevel5mAlreadyAboveWhenLevelArrives(t *testing.T) {
	ts := newTS5()
	candle5(ts, 0, 10.40, 10.30) // closed above before the level was known
	end5(ts, 0)
	withPMH(ts, 10.00, true)
	candle5(ts, 1, 10.50, 10.45)
	end5(ts, 1)
	if ts.pmh5.brkMs != 0 {
		t.Fatal("already above when the level arrived: not a fresh break")
	}
	candle5(ts, 2, 10.10, 9.95) // closes back under: re-armed
	end5(ts, 2)
	candle5(ts, 3, 10.20, 10.15) // closes above again: a break
	end5(ts, 3)
	if want := (c0 + 4*300) * 1000; ts.pmh5.brkMs != want {
		t.Fatalf("the reclaim close is a break: %d want %d", ts.pmh5.brkMs, want)
	}
}

func TestLevel5mOneBreakWhileClosingAbove(t *testing.T) {
	ts := newTS5()
	candle5(ts, 0, 9.90, 9.90)
	end5(ts, 0)
	withPMH(ts, 10.00, true)
	for k := 1; k <= 4; k++ {
		candle5(ts, k, 10.2, 10.1+0.01*float64(k))
		end5(ts, k)
	}
	if want := (c0 + 2*300) * 1000; ts.pmh5.brkMs != want {
		t.Fatalf("consecutive closes above are one break (the first): %d want %d", ts.pmh5.brkMs, want)
	}
}

func TestLevel5mPrevAhAndReset(t *testing.T) {
	ts := newTS5()
	candle5(ts, 0, 5.00, 5.00)
	end5(ts, 0)
	ts.prevAhHi = 5.20
	candle5(ts, 1, 5.30, 5.25)
	end5(ts, 1)
	if r := row5(ts); core.IsNull(r.BrkPrevAh5mAge) {
		t.Fatal("a 5-minute close above the previous after-hours high must register")
	}
	ts.resetSession()
	if ts.ah5.brkMs != 0 || ts.lvl5Done != 0 || ts.lvl5Cl != 0 {
		t.Fatal("the day reset must clear the 5-minute level state")
	}
}

// Through the real engine with past timestamps: the breaking candle is the
// LAST one, so only the shard clock (tick5m) can close it.
func TestEngineLevel5mClockClosesTheBreakCandle(t *testing.T) {
	e := New(Options{TickHz: 50})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = e.Run(ctx) }()
	s0 := (time.Now().Unix()/300 - 6) * 300 // 30 minutes ago, 5-min aligned
	agg := func(sec int64, px float64) core.Agg {
		return core.Agg{Sym: "L5M", Open: px, High: px, Low: px, Close: px, Vol: 100, StartMs: sec * 1000, EndMs: (sec + 1) * 1000}
	}
	e.Apply(agg(s0+10, 5.00))
	waitForSym(t, e, "L5M", 3*time.Second)
	e.SetPrevAhHigh(map[string]float64{"L5M": 5.20})
	_ = e.Curves(0)
	e.Apply(agg(s0+290, 5.00))     // candle 0 closes under
	e.Apply(agg(s0+300+290, 5.30)) // candle 1 closes above; nothing prints after it
	waitForCond(t, e, "L5M", 5*time.Second, func(r core.Row) bool {
		return !core.IsNull(r.BrkPrevAh5mAge)
	})
}
