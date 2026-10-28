package engine

import (
	"context"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// c0 sits on a 5-minute boundary (1_700_000_100 % 300 == 0), so c0 + k*300
// opens candle k.
const c0 = int64(1_700_000_100)

// print5 applies a one-second aggregate covering [sec, sec+1) — the shape the
// A.* stream delivers: StartMs on the second, EndMs one second later.
func print5(ts *tickerState, sec int64, lo, close float64) {
	ts.applyAgg(core.Agg{
		Sym: ts.sym, Open: close, High: max(close, lo), Low: lo, Close: close, Vol: 100,
		StartMs: sec * 1000, EndMs: (sec + 1) * 1000,
	})
}

func newTS5() *tickerState {
	return &tickerState{sym: "X", curveBkt: -1, curveFirst: -1, barHead: -1}
}

func row5(ts *tickerState) core.Row { return ts.toRow(0, nil, nil, nil, core.SessionRegular) }

func near(a, b float64) bool { return abs(a-b) < 1e-4 }

func TestBrk5mBreaksPreviousLow(t *testing.T) {
	ts := newTS5()
	print5(ts, c0+5, 10.00, 10.05) // candle A
	print5(ts, c0+100, 9.80, 9.90) // A's low
	print5(ts, c0+200, 9.95, 10.00)

	// Candle B opens above A's low: the level is known, nothing broken yet.
	print5(ts, c0+300, 9.90, 9.92)
	r := row5(ts)
	if !near(r.Prev5mLow, 9.80) {
		t.Fatalf("prev5mLow want 9.80 got %v", r.Prev5mLow)
	}
	if !core.IsNull(r.Brk5mAge) {
		t.Fatalf("no break yet, got age %v", r.Brk5mAge)
	}
	if r.VsPrev5mLow <= 0 {
		t.Fatalf("above the previous low should read positive, got %v", r.VsPrev5mLow)
	}

	// Trades through 9.80: break, age 0 at the print.
	print5(ts, c0+310, 9.75, 9.76)
	r = row5(ts)
	if r.Brk5mAge != 0 {
		t.Fatalf("fresh break age want 0 got %v", r.Brk5mAge)
	}
	if want := pct(float32(9.76), float32(9.80)); abs(r.VsPrev5mLow-want) > 1e-6 {
		t.Fatalf("vsPrev5mLow want %v got %v", want, r.VsPrev5mLow)
	}

	// The clock moves on without prints: age counts up from the breaking second
	// (the agg ending at c0+311).
	ts.syncTo(c0 + 340)
	if r = row5(ts); r.Brk5mAge != 29 {
		t.Fatalf("age want 29 got %v", r.Brk5mAge)
	}

	// A bounce back above the level doesn't un-break the candle; vsPrev5mLow
	// shows where price is now.
	print5(ts, c0+345, 9.85, 9.90)
	r = row5(ts)
	if core.IsNull(r.Brk5mAge) {
		t.Fatal("the candle has broken the low; a bounce must not erase that")
	}
	if r.VsPrev5mLow <= 0 {
		t.Fatalf("back above the level should read positive, got %v", r.VsPrev5mLow)
	}
}

// TestBrk5mFinalSecondPrintStaysInItsCandle is the false-break guard. A stock
// selling into the bar close prints its low in the final second, [c0+299,
// c0+300). That agg's EndMs sits exactly ON the next candle's boundary. Keyed
// on EndMs (as the 15-second chart bars are) or on the snapshot clock (as the
// bkt* buckets are), the low would leak into the NEW candle, the old candle
// would read 10.00, and the new candle would "break" it on its first print.
func TestBrk5mFinalSecondPrintStaysInItsCandle(t *testing.T) {
	ts := newTS5()
	print5(ts, c0+10, 10.00, 10.00)
	print5(ts, c0+299, 9.50, 9.55) // final second of candle A: its real low
	print5(ts, c0+300, 9.60, 9.62) // candle B opens ABOVE 9.50

	r := row5(ts)
	if !near(r.Prev5mLow, 9.50) {
		t.Fatalf("the final-second low belongs to candle A: prev5mLow want 9.50 got %v", r.Prev5mLow)
	}
	if !core.IsNull(r.Brk5mAge) {
		t.Fatalf("B (9.60) is above A's true low (9.50): no break, got age %v", r.Brk5mAge)
	}
}

// Chart convention: the previous candle is the last one that TRADED.
func TestBrk5mPreviousCandleSkipsEmptyWindows(t *testing.T) {
	ts := newTS5()
	print5(ts, c0+50, 10.00, 10.00) // candle A, then a whole window with no prints
	ts.syncTo(c0 + 650)
	print5(ts, c0+650, 9.95, 9.95) // candle C

	r := row5(ts)
	if !near(r.Prev5mLow, 10.00) {
		t.Fatalf("previous traded candle is A: want 10.00 got %v", r.Prev5mLow)
	}
	if r.Brk5mAge != 0 {
		t.Fatalf("C broke A's low: want age 0 got %v", r.Brk5mAge)
	}
}

// Once the clock enters a new window, the last candle's break is history and
// that candle becomes the reference — even before the new candle's first print.
func TestBrk5mNoLiveBreakUntilCurrentCandleTrades(t *testing.T) {
	ts := newTS5()
	print5(ts, c0+50, 10.00, 10.00) // A
	print5(ts, c0+300, 9.90, 9.90)  // B breaks A
	if r := row5(ts); r.Brk5mAge != 0 {
		t.Fatalf("B should break A, got age %v", r.Brk5mAge)
	}

	ts.syncTo(c0 + 610) // candle C's window, no print yet
	r := row5(ts)
	if !core.IsNull(r.Brk5mAge) {
		t.Fatalf("B's break must not read as live in C's window, got age %v", r.Brk5mAge)
	}
	if !near(r.Prev5mLow, 9.90) {
		t.Fatalf("B has closed and is now the previous candle: want 9.90 got %v", r.Prev5mLow)
	}

	print5(ts, c0+615, 9.95, 9.95) // C holds above B
	if r = row5(ts); !core.IsNull(r.Brk5mAge) || !near(r.Prev5mLow, 9.90) {
		t.Fatalf("C above B: want no break vs 9.90, got age %v prev %v", r.Brk5mAge, r.Prev5mLow)
	}
	print5(ts, c0+620, 9.85, 9.85) // C breaks B
	if r = row5(ts); r.Brk5mAge != 0 {
		t.Fatalf("C broke B: want age 0 got %v", r.Brk5mAge)
	}
}

// A candle already in progress when the scanner started only saw part of its
// prints; its real low may be lower than what we saw, so using it would hand
// the next candle a fake break.
func TestBrk5mCandleInProgressAtStartupIsNeverTheReference(t *testing.T) {
	ts := newTS5()
	ts.m5ListenMs = (c0 + 120) * 1000 // started listening 2 minutes into candle A
	print5(ts, c0+130, 10.00, 10.00)
	print5(ts, c0+300, 9.95, 9.95) // B dips under what we SAW of A

	r := row5(ts)
	if !core.IsNull(r.Brk5mAge) || !core.IsNull(r.Prev5mLow) {
		t.Fatalf("partial A must not be a reference: age %v prev %v", r.Brk5mAge, r.Prev5mLow)
	}

	print5(ts, c0+600, 9.90, 9.90) // C vs a fully observed B
	if r = row5(ts); r.Brk5mAge != 0 || !near(r.Prev5mLow, 9.95) {
		t.Fatalf("C vs full B: want age 0 prev 9.95, got age %v prev %v", r.Brk5mAge, r.Prev5mLow)
	}
}

// A print for the previous candle that arrives after the next one opened
// belongs to the previous candle — and can show a "break" never happened.
func TestBrk5mLatePrintForPreviousCandleRevokesBreak(t *testing.T) {
	ts := newTS5()
	print5(ts, c0+100, 10.00, 10.00) // A as first seen
	print5(ts, c0+300, 9.95, 9.95)   // B "breaks" 10.00 ...
	if r := row5(ts); r.Brk5mAge != 0 {
		t.Fatalf("precondition: B breaks A as seen, got age %v", r.Brk5mAge)
	}

	print5(ts, c0+299, 9.90, 9.92) // ... but A's final second arrives late at 9.90
	r := row5(ts)
	if !core.IsNull(r.Brk5mAge) {
		t.Fatalf("A's true low was 9.90; B (9.95) never broke it, got age %v", r.Brk5mAge)
	}
	if !near(r.Prev5mLow, 9.90) {
		t.Fatalf("late print must revise A's low: want 9.90 got %v", r.Prev5mLow)
	}
}

func TestBrk5mTradingAtThePreviousLowIsNotABreak(t *testing.T) {
	ts := newTS5()
	print5(ts, c0+100, 10.00, 10.00)
	print5(ts, c0+300, 10.00, 10.02) // tests the low exactly
	if r := row5(ts); !core.IsNull(r.Brk5mAge) {
		t.Fatalf("touching the low is a test, not a break: got age %v", r.Brk5mAge)
	}
}

func TestBrk5mResetSessionDropsPreviousCandle(t *testing.T) {
	ts := newTS5()
	print5(ts, c0+100, 10.00, 10.00)
	print5(ts, c0+300, 9.90, 9.90)
	ts.resetSession()
	print5(ts, c0+3600, 9.00, 9.00) // first candle of the new day

	if r := row5(ts); !core.IsNull(r.Prev5mLow) || !core.IsNull(r.Brk5mAge) {
		t.Fatalf("the day's first candle has no previous: prev %v age %v", r.Prev5mLow, r.Brk5mAge)
	}
}

// TestEngineBrk5mThroughSnapshot drives the real engine: shard creation must
// stamp each ticker with the engine's start time (the partial-candle guard),
// and the break must reach merged snapshots. Prints are placed an hour in the
// FUTURE so the real 1 Hz wall clock can never push these tickers into a later
// window mid-test (syncTo ignores backward ticks).
func TestEngineBrk5mThroughSnapshot(t *testing.T) {
	base := (time.Now().Unix()/300 + 12) * 300
	agg := func(sym string, sec int64, px float64) core.Agg {
		return core.Agg{Sym: sym, Open: px, High: px, Low: px, Close: px, Vol: 100,
			StartMs: sec * 1000, EndMs: (sec + 1) * 1000}
	}
	run := func(t *testing.T, opts Options) *Engine {
		eng := New(opts)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go func() { _ = eng.Run(ctx) }()
		return eng
	}

	t.Run("full previous candle breaks", func(t *testing.T) {
		eng := run(t, Options{TickHz: 50})
		eng.Apply(agg("AAA", base+60, 10.00))
		eng.Apply(agg("AAA", base+300, 9.90))
		waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool {
			return r.Brk5mAge == 0 && near(r.Prev5mLow, 10.00) && r.VsPrev5mLow < 0
		})
	})

	t.Run("candle in progress at engine start is not a reference", func(t *testing.T) {
		eng := run(t, Options{TickHz: 50, Now: func() time.Time { return time.Unix(base+120, 0) }})
		eng.Apply(agg("BBB", base+130, 10.00))
		eng.Apply(agg("BBB", base+300, 9.90))
		// If shard.get forgot the engine start time, this row would show a break
		// vs 10.00 and the condition would never hold.
		waitForCond(t, eng, "BBB", 3*time.Second, func(r core.Row) bool {
			return near(r.Last, 9.90) && core.IsNull(r.Brk5mAge) && core.IsNull(r.Prev5mLow)
		})
		eng.Apply(agg("BBB", base+600, 9.80))
		waitForCond(t, eng, "BBB", 3*time.Second, func(r core.Row) bool {
			return r.Brk5mAge == 0 && near(r.Prev5mLow, 9.90)
		})
	})
}

// ---- close-based variant: the level is the previous candle's CLOSE ----

// printAt applies a one-second agg whose low and close differ.
func printAt(ts *tickerState, sec int64, lo, close float64) {
	ts.applyAgg(core.Agg{
		Sym: ts.sym, Open: close, High: max(close, lo), Low: lo, Close: close, Vol: 100,
		StartMs: sec * 1000, EndMs: (sec + 1) * 1000,
	})
}

// The case that separates the two variants: B trades under A's close but
// stays above A's low.
func TestBrk5mCloseBreaksPreviousClose(t *testing.T) {
	ts := newTS5()
	printAt(ts, c0+100, 9.80, 9.85) // A's low 9.80
	printAt(ts, c0+250, 9.88, 9.90) // A closes 9.90

	printAt(ts, c0+300, 9.92, 9.93) // B opens above A's close
	if r := row5(ts); !core.IsNull(r.Brk5mCloseAge) || !near(r.Prev5mClose, 9.90) || r.VsPrev5mClose <= 0 {
		t.Fatalf("above the close: want no break vs 9.90, got age %v prev %v vs %v", r.Brk5mCloseAge, r.Prev5mClose, r.VsPrev5mClose)
	}

	printAt(ts, c0+305, 9.85, 9.86) // under the close 9.90, still over the low 9.80
	r := row5(ts)
	if r.Brk5mCloseAge != 0 {
		t.Fatalf("B traded below A's close: want close-break age 0, got %v", r.Brk5mCloseAge)
	}
	if !core.IsNull(r.Brk5mAge) {
		t.Fatalf("B is still above A's low: the LOW variant must not fire, got %v", r.Brk5mAge)
	}
	if want := pct(float32(9.86), float32(9.90)); abs(r.VsPrev5mClose-want) > 1e-6 {
		t.Fatalf("vsPrev5mClose want %v got %v", want, r.VsPrev5mClose)
	}
	ts.syncTo(c0 + 330)
	if r = row5(ts); r.Brk5mCloseAge != 24 { // broke in the second ending at c0+306
		t.Fatalf("close-break age want 24 got %v", r.Brk5mCloseAge)
	}
}

// The print in [c0+299, c0+300) IS candle A's close. Keyed on EndMs it would
// land in B, A's close would read 10.00, and B would "break" it at once.
func TestBrk5mCloseFinalSecondPrintIsTheClose(t *testing.T) {
	ts := newTS5()
	printAt(ts, c0+10, 10.00, 10.00)
	printAt(ts, c0+299, 9.50, 9.50) // A's real close
	printAt(ts, c0+300, 9.60, 9.60) // B opens above it

	r := row5(ts)
	if !near(r.Prev5mClose, 9.50) {
		t.Fatalf("A closed at 9.50 in its final second: want prev close 9.50 got %v", r.Prev5mClose)
	}
	if !core.IsNull(r.Brk5mCloseAge) {
		t.Fatalf("B (9.60) is above A's close (9.50): no break, got age %v", r.Brk5mCloseAge)
	}
}

// The close is the LATEST print by timestamp, not the last one to arrive.
func TestBrk5mCloseOutOfOrderPrintDoesNotOverwriteClose(t *testing.T) {
	ts := newTS5()
	printAt(ts, c0+200, 9.90, 9.90)  // A's latest print
	printAt(ts, c0+100, 9.90, 10.20) // an EARLIER second arriving late
	printAt(ts, c0+300, 10.00, 10.00)

	r := row5(ts)
	if !near(r.Prev5mClose, 9.90) {
		t.Fatalf("an out-of-order earlier print must not become the close: want 9.90 got %v", r.Prev5mClose)
	}
	if !core.IsNull(r.Brk5mCloseAge) {
		t.Fatalf("B (10.00) is above A's real close (9.90): no break, got age %v", r.Brk5mCloseAge)
	}
}

// A candle in progress at startup has an unknowable LOW but an exact CLOSE —
// we were listening when it ended — so it is a valid close reference.
func TestBrk5mClosePartialCandleIsAValidReference(t *testing.T) {
	ts := newTS5()
	ts.m5ListenMs = (c0 + 120) * 1000
	printAt(ts, c0+130, 10.00, 10.05)
	printAt(ts, c0+290, 10.00, 10.00) // A closes 10.00
	printAt(ts, c0+300, 9.95, 9.95)

	r := row5(ts)
	if r.Brk5mCloseAge != 0 || !near(r.Prev5mClose, 10.00) {
		t.Fatalf("partial A's close is exact: want close-break age 0 vs 10.00, got age %v prev %v", r.Brk5mCloseAge, r.Prev5mClose)
	}
	if !core.IsNull(r.Prev5mLow) || !core.IsNull(r.Brk5mAge) {
		t.Fatalf("partial A's LOW must still be withheld: prev %v age %v", r.Prev5mLow, r.Brk5mAge)
	}
}

// A late print for the previous candle that is LATER than what we took as its
// close is the real close; an earlier late print is not.
func TestBrk5mCloseLatePrintRevisesClose(t *testing.T) {
	ts := newTS5()
	printAt(ts, c0+100, 10.00, 10.00) // A's close as first seen
	printAt(ts, c0+300, 9.95, 9.95)   // B "breaks" 10.00
	if r := row5(ts); r.Brk5mCloseAge != 0 {
		t.Fatalf("precondition: B breaks A's close as seen, got %v", r.Brk5mCloseAge)
	}

	printAt(ts, c0+50, 9.99, 10.30) // late but EARLIER than the close print: ignored
	if r := row5(ts); !near(r.Prev5mClose, 10.00) || r.Brk5mCloseAge != 0 {
		t.Fatalf("an earlier late print must not move the close: prev %v age %v", r.Prev5mClose, r.Brk5mCloseAge)
	}

	printAt(ts, c0+299, 9.92, 9.92) // late and LATER: A really closed at 9.92
	r := row5(ts)
	if !near(r.Prev5mClose, 9.92) {
		t.Fatalf("A's real close is 9.92: got %v", r.Prev5mClose)
	}
	if !core.IsNull(r.Brk5mCloseAge) {
		t.Fatalf("B (9.95) never traded below 9.92: break must be revoked, got %v", r.Brk5mCloseAge)
	}
}

func TestBrk5mCloseTradingAtTheCloseIsNotABreak(t *testing.T) {
	ts := newTS5()
	printAt(ts, c0+100, 9.90, 10.00)
	printAt(ts, c0+300, 10.00, 10.01) // touches A's close exactly
	if r := row5(ts); !core.IsNull(r.Brk5mCloseAge) {
		t.Fatalf("touching the close is a test, not a break: got %v", r.Brk5mCloseAge)
	}
}

func TestBrk5mCloseNoLiveBreakUntilCurrentCandleTrades(t *testing.T) {
	ts := newTS5()
	printAt(ts, c0+100, 10.00, 10.00) // A closes 10.00
	printAt(ts, c0+300, 9.90, 9.95)   // B breaks it, closes 9.95
	if r := row5(ts); r.Brk5mCloseAge != 0 {
		t.Fatalf("B broke A's close: got %v", r.Brk5mCloseAge)
	}
	ts.syncTo(c0 + 610) // C's window, no print yet
	r := row5(ts)
	if !core.IsNull(r.Brk5mCloseAge) {
		t.Fatalf("B's break must not read as live in C's window, got %v", r.Brk5mCloseAge)
	}
	if !near(r.Prev5mClose, 9.95) {
		t.Fatalf("B has closed and is the previous candle: want 9.95 got %v", r.Prev5mClose)
	}
}

func TestBrk5mCloseResetSessionDropsPreviousClose(t *testing.T) {
	ts := newTS5()
	printAt(ts, c0+100, 10.00, 10.00)
	printAt(ts, c0+300, 9.90, 9.90)
	ts.resetSession()
	printAt(ts, c0+3600, 9.00, 9.00)
	if r := row5(ts); !core.IsNull(r.Prev5mClose) || !core.IsNull(r.Brk5mCloseAge) {
		t.Fatalf("the day's first candle has no previous close: prev %v age %v", r.Prev5mClose, r.Brk5mCloseAge)
	}
}

// Through the real engine, started mid-candle: the close variant must arm on
// the very next candle while the low variant correctly waits one more.
func TestEngineBrk5mCloseThroughSnapshot(t *testing.T) {
	base := (time.Now().Unix()/300 + 12) * 300
	eng := New(Options{TickHz: 50, Now: func() time.Time { return time.Unix(base+120, 0) }})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = eng.Run(ctx) }()
	agg := func(sec int64, px float64) core.Agg {
		return core.Agg{Sym: "CCC", Open: px, High: px, Low: px, Close: px, Vol: 100, StartMs: sec * 1000, EndMs: (sec + 1) * 1000}
	}
	eng.Apply(agg(base+130, 10.05))
	eng.Apply(agg(base+290, 10.00)) // partial A closes 10.00
	eng.Apply(agg(base+300, 9.95))
	waitForCond(t, eng, "CCC", 3*time.Second, func(r core.Row) bool {
		return r.Brk5mCloseAge == 0 && near(r.Prev5mClose, 10.00) && r.VsPrev5mClose < 0 &&
			core.IsNull(r.Brk5mAge) && core.IsNull(r.Prev5mLow)
	})
}

// ---- long side: the current candle trades ABOVE the previous close ----

// printHL applies a one-second agg with an explicit high and low.
func printHL(ts *tickerState, sec int64, hi, lo, close float64) {
	ts.applyAgg(core.Agg{
		Sym: ts.sym, Open: close, High: hi, Low: lo, Close: close, Vol: 100,
		StartMs: sec * 1000, EndMs: (sec + 1) * 1000,
	})
}

func TestBrk5mCloseUpBreaksAbovePreviousClose(t *testing.T) {
	ts := newTS5()
	printHL(ts, c0+100, 10.10, 9.95, 10.00) // A closes 10.00 (high 10.10)
	printHL(ts, c0+300, 9.99, 9.97, 9.98)   // B opens under the close
	if r := row5(ts); !core.IsNull(r.Brk5mCloseUpAge) {
		t.Fatalf("below the close: no long break yet, got %v", r.Brk5mCloseUpAge)
	}
	// The bar's HIGH pokes above 10.00 even though it closes back under it:
	// trading above counts, intra-bar.
	printHL(ts, c0+305, 10.02, 9.97, 9.99)
	r := row5(ts)
	if r.Brk5mCloseUpAge != 0 {
		t.Fatalf("B's high traded above A's close: want long-break age 0, got %v", r.Brk5mCloseUpAge)
	}
	ts.syncTo(c0 + 320)
	if r = row5(ts); r.Brk5mCloseUpAge != 14 { // broke in the second ending at c0+306
		t.Fatalf("long-break age want 14 got %v", r.Brk5mCloseUpAge)
	}
}

// A volatile candle can trade through the previous close in BOTH directions;
// it then belongs in both the long and the short scan.
func TestBrk5mCloseCandleCanBreakBothWays(t *testing.T) {
	ts := newTS5()
	printHL(ts, c0+100, 10.00, 10.00, 10.00) // A closes 10.00
	printHL(ts, c0+300, 9.95, 9.90, 9.92)    // B dips under ...
	printHL(ts, c0+310, 10.08, 10.02, 10.06) // ... then rips over
	r := row5(ts)
	// The dip's agg ends at c0+301, the rip's at c0+311: short age 10, long age 0.
	if r.Brk5mCloseAge != 10 || r.Brk5mCloseUpAge != 0 {
		t.Fatalf("both breaks expected: short age %v (want 10), long age %v (want 0)", r.Brk5mCloseAge, r.Brk5mCloseUpAge)
	}
}

// Mirror of the final-second guard: A closes HIGH in its last second; keyed on
// EndMs that print would leak into B, A's close would read 10.00, and B opening
// at 10.40 would be a fake long break.
func TestBrk5mCloseUpFinalSecondPrintIsTheClose(t *testing.T) {
	ts := newTS5()
	printHL(ts, c0+10, 10.00, 10.00, 10.00)
	printHL(ts, c0+299, 10.50, 10.50, 10.50) // A's real close
	printHL(ts, c0+300, 10.40, 10.40, 10.40) // B opens under it
	if r := row5(ts); !core.IsNull(r.Brk5mCloseUpAge) || !near(r.Prev5mClose, 10.50) {
		t.Fatalf("B (10.40) is below A's close (10.50): no long break, got age %v prev %v", r.Brk5mCloseUpAge, r.Prev5mClose)
	}
}

func TestBrk5mCloseUpTradingAtTheCloseIsNotABreak(t *testing.T) {
	ts := newTS5()
	printHL(ts, c0+100, 10.00, 10.00, 10.00)
	printHL(ts, c0+300, 10.00, 9.98, 9.99) // high touches the close exactly
	if r := row5(ts); !core.IsNull(r.Brk5mCloseUpAge) {
		t.Fatalf("touching the close is a test, not a break: got %v", r.Brk5mCloseUpAge)
	}
}

func TestBrk5mCloseUpLatePrintRevisesClose(t *testing.T) {
	ts := newTS5()
	printHL(ts, c0+100, 10.00, 10.00, 10.00) // A's close as first seen
	printHL(ts, c0+300, 10.05, 10.03, 10.04) // B "breaks above" 10.00
	if r := row5(ts); r.Brk5mCloseUpAge != 0 {
		t.Fatalf("precondition: B breaks above A's close as seen, got %v", r.Brk5mCloseUpAge)
	}
	printHL(ts, c0+299, 10.08, 10.08, 10.08) // A really closed at 10.08
	r := row5(ts)
	if !near(r.Prev5mClose, 10.08) || !core.IsNull(r.Brk5mCloseUpAge) {
		t.Fatalf("B's high 10.05 never cleared the real close 10.08: prev %v age %v", r.Prev5mClose, r.Brk5mCloseUpAge)
	}
}

func TestBrk5mCloseUpNoLiveBreakUntilCurrentCandleTrades(t *testing.T) {
	ts := newTS5()
	printHL(ts, c0+100, 10.00, 10.00, 10.00)
	printHL(ts, c0+300, 10.10, 10.05, 10.08) // B breaks above
	ts.syncTo(c0 + 610)                      // C's window, no print yet
	if r := row5(ts); !core.IsNull(r.Brk5mCloseUpAge) {
		t.Fatalf("B's long break must not read as live in C's window, got %v", r.Brk5mCloseUpAge)
	}
}

func TestBrk5mCloseUpResetSession(t *testing.T) {
	ts := newTS5()
	printHL(ts, c0+100, 10.00, 10.00, 10.00)
	printHL(ts, c0+300, 10.10, 10.05, 10.08)
	ts.resetSession()
	printHL(ts, c0+3600, 11.00, 11.00, 11.00)
	if r := row5(ts); !core.IsNull(r.Brk5mCloseUpAge) {
		t.Fatalf("first candle of the day has no previous close: got %v", r.Brk5mCloseUpAge)
	}
}

func TestEngineBrk5mCloseUpThroughSnapshot(t *testing.T) {
	base := (time.Now().Unix()/300 + 12) * 300
	eng := New(Options{TickHz: 50})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = eng.Run(ctx) }()
	agg := func(sec int64, px float64) core.Agg {
		return core.Agg{Sym: "UPP", Open: px, High: px, Low: px, Close: px, Vol: 100, StartMs: sec * 1000, EndMs: (sec + 1) * 1000}
	}
	eng.Apply(agg(base+60, 10.00))
	eng.Apply(agg(base+300, 10.10))
	waitForCond(t, eng, "UPP", 3*time.Second, func(r core.Row) bool {
		return r.Brk5mCloseUpAge == 0 && near(r.Prev5mClose, 10.00) && r.VsPrev5mClose > 0 && core.IsNull(r.Brk5mCloseAge)
	})
}
