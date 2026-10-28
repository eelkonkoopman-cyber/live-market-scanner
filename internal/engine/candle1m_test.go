package engine

import (
	"math"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// c0 (candle5m_test.go) sits on a minute boundary, so c0 + m*60 opens minute m.

// pm1 prints one trade in minute m, s seconds in, closing at c with volume v.
func pm1(ts *tickerState, m int, s int64, c float64, v int64) {
	sec := c0 + int64(m)*60 + s
	ts.applyAgg(core.Agg{Sym: ts.sym, Open: c, High: c, Low: c, Close: c, Vol: v,
		StartMs: sec * 1000, EndMs: (sec + 1) * 1000})
}

// clockAt advances the 1 Hz clock to second s of minute m (no print).
func clockAt(ts *tickerState, m int, s int64) { ts.syncTo(c0 + int64(m)*60 + s) }

// steady feeds minutes [0, n) with 100 shares each, closing at 10.00.
func steady(ts *tickerState, n int) {
	for m := 0; m < n; m++ {
		pm1(ts, m, 30, 10.00, 100)
	}
}

func c1m(ts *tickerState) (chg, rvol float64) {
	r := row5(ts)
	return r.C1mChg, r.C1mRvol
}

func isNull(v float64) bool { return core.IsNull(v) }

func wantC1m(t *testing.T, ts *tickerState, label string, wantChg, wantRvol float64) {
	t.Helper()
	chg, rvol := c1m(ts)
	ok := func(got, want float64) bool {
		if isNull(want) {
			return isNull(got)
		}
		return !isNull(got) && math.Abs(got-want) < 1e-9
	}
	if !ok(chg, wantChg) || !ok(rvol, wantRvol) {
		t.Fatalf("%s: chg %v rvol %v, want %v / %v", label, chg, rvol, wantChg, wantRvol)
	}
}

func TestC1mSteadyVolumeRvolIsOne(t *testing.T) {
	ts := newTS5()
	steady(ts, 12)
	wantC1m(t, ts, "steady", 0, 1)
}

func TestC1mFiveTimesVolumeAndChange(t *testing.T) {
	ts := newTS5()
	steady(ts, 10) // minutes 0..9
	pm1(ts, 10, 10, 10.20, 300)
	pm1(ts, 10, 40, 10.50, 200) // 500 = 5x the 100 average; close 10.50 vs previous close 10.00
	wantC1m(t, ts, "spike", 5, 5)
}

func TestC1mWarmUpNullBeforeNineObservedMinutes(t *testing.T) {
	ts := newTS5()
	for m := 0; m < 9; m++ { // minute 9 is the first with 9 observed minutes before it
		pm1(ts, m, 30, 10.00, 100)
		if _, rvol := c1m(ts); !isNull(rvol) {
			t.Fatalf("minute %d: rvol = %v, want Null in warm-up", m, rvol)
		}
	}
	pm1(ts, 9, 30, 10.10, 100) // +1%: F beats P, whose own baseline is still unobserved
	wantC1m(t, ts, "minute 9", 1, 1)

	// chg is available as soon as there is a previous close, warm-up or not.
	ts2 := newTS5()
	pm1(ts2, 0, 30, 10.00, 100)
	pm1(ts2, 1, 30, 10.10, 100)
	wantC1m(t, ts2, "warm-up chg", 1, core.Null)
}

func TestC1mPartialFirstMinuteIsNotCountedAsObserved(t *testing.T) {
	ts := newTS5()
	ts.m5ListenMs = (c0+20)*1000 + 1 // the engine started 20 s into minute 0
	steady(ts, 10)                   // minutes 0..9: minute 0 is partial, so minute 9 has only 8 observed before it
	wantC1m(t, ts, "partial first", 0, core.Null)
	pm1(ts, 10, 30, 10.10, 100) // +1%: F beats P, whose own baseline includes the partial minute
	wantC1m(t, ts, "minutes 1..9 heard", 1, 1)
}

func TestC1mEmptyMinutesCountAsZeroVolume(t *testing.T) {
	ts := newTS5()
	steady(ts, 6)              // minutes 0..5 traded (100 each); 6, 7, 8 are empty
	pm1(ts, 9, 30, 10.00, 300) // baseline minutes 0..8 = 600 / 9 = 66.67; 300 / 66.67 = 4.5; previous close is minute 5's 10.00
	wantC1m(t, ts, "empty minutes", 0, 4.5)
}

func TestC1mNullWhenBaselineIsAllEmpty(t *testing.T) {
	ts := newTS5()
	for m := 0; m < 9; m++ {
		pm1(ts, m, 30, 10.00, 0) // observed, but no volume at all
	}
	pm1(ts, 9, 30, 10.00, 100)
	wantC1m(t, ts, "zero baseline", 0, core.Null)
}

func TestC1mLatePrintIgnored(t *testing.T) {
	ts := newTS5()
	steady(ts, 10)
	pm1(ts, 10, 5, 10.40, 100)
	pm1(ts, 11, 5, 10.60, 100)  // finalizes minute 10
	pm1(ts, 10, 50, 12.00, 900) // late for minute 10: must change nothing
	pm1(ts, 11, 50, 10.70, 100)
	m10 := int32(c0/60) + 10
	if got := ts.c1m.ring[m10%c1mRing]; got.vol != 100 || got.min != m10 {
		t.Fatalf("minute 10 slot = %+v, a late print must not touch a finalized minute", got)
	}
	if ts.c1m.curVol != 200 || ts.c1m.curCl != 10.70 {
		t.Fatalf("forming candle vol %d close %v", ts.c1m.curVol, ts.c1m.curCl)
	}
	// An out-of-order print inside the forming minute: volume counts, an older print cannot set the close.
	pm1(ts, 11, 20, 9.00, 50)
	if ts.c1m.curVol != 250 || ts.c1m.curCl != 10.70 {
		t.Fatalf("out-of-order print: vol %d close %v, want 250 / 10.70", ts.c1m.curVol, ts.c1m.curCl)
	}
}

func TestC1mDisplayedCandleSelection(t *testing.T) {
	ts := newTS5()
	steady(ts, 10)
	// Minute 10: a +5% spike on 5x volume, peaking at :55.
	pm1(ts, 10, 10, 10.50, 500)
	pm1(ts, 10, 55, 10.50, 0)
	clockAt(ts, 10, 59)
	wantC1m(t, ts, ":59", 5, 5)

	// :00 of minute 11 with no print yet: the candle that just closed stays.
	clockAt(ts, 11, 0)
	wantC1m(t, ts, "new minute, no print", 5, 5)
	// The new candle opens weak (10.55 vs previous close 10.50 = +0.48%, tiny volume): P stays.
	pm1(ts, 11, 1, 10.55, 10)
	wantC1m(t, ts, "weak new candle", 5, 5)
	// It runs: 11.20 vs 10.50 = +6.67% beats P's +5%, so F is shown with ITS volume ratio.
	pm1(ts, 11, 30, 11.20, 140) // vol 150; baseline is minutes 2..10 = (800 + 500) / 9
	chgF := (11.20 - 10.50) / 10.50 * 100
	rvolF := 150 / (1300.0 / 9)
	wantC1m(t, ts, "stronger forming candle", chgF, rvolF)

	// Minute 12 has no prints yet: minute 11's candle is the one that just closed.
	clockAt(ts, 12, 30)
	wantC1m(t, ts, "minute 12 empty", chgF, rvolF)
	// Minute 13: minute 11 is no longer the previous minute, and 13 has no prints.
	clockAt(ts, 13, 5)
	wantC1m(t, ts, "minute 13", core.Null, core.Null)
}

func TestC1mFinalizedPreviousCandleReadsLikeUnfolded(t *testing.T) {
	// P already folded into the ring (the next minute has printed) must read the same as unfolded.
	ts := newTS5()
	steady(ts, 10)
	pm1(ts, 10, 10, 10.50, 500)
	pm1(ts, 11, 2, 10.40, 20) // folds minute 10; F = -0.95% vs 10.50, weaker than P
	wantC1m(t, ts, "P stronger than F", 5, 5)
}

func TestC1mTieGoesToPreviousCandle(t *testing.T) {
	// Binary-exact prices make an exact tie: P 8 -> 8.5 and F 8.5 -> 9.03125
	// are both +6.25%. The two have different volume, so the rvol shown says
	// which candle was chosen.
	ts := newTS5()
	for m := 0; m < 10; m++ {
		pm1(ts, m, 30, 8.0, 100)
	}
	pm1(ts, 10, 10, 8.5, 200)     // P: +6.25%, rvol 2
	pm1(ts, 11, 10, 9.03125, 500) // F: +6.25%, rvol 5 against minutes 2..10
	wantC1m(t, ts, "tie", 6.25, 2)
	pm1(ts, 11, 20, 9.04, 1) // F nudges ahead: now F wins
	chg, rvol := c1m(ts)
	if chg <= 6.25 || rvol < 4 {
		t.Fatalf("stronger F: chg %v rvol %v", chg, rvol)
	}
}

func TestC1mPrevCloseSkipsUntradedMinutes(t *testing.T) {
	ts := newTS5()
	steady(ts, 5)              // last trade: minute 4 at 10.00
	pm1(ts, 8, 30, 10.30, 100) // minutes 5, 6, 7 untraded
	wantC1m(t, ts, "forming", 3, core.Null)
	// Once it is the closed candle it is measured against the same close.
	clockAt(ts, 9, 1)
	wantC1m(t, ts, "closed", 3, core.Null)
	// ...also after minute 9 prints (candle 8 is folded into the ring and is still P).
	pm1(ts, 9, 5, 10.31, 1)
	wantC1m(t, ts, "folded", 3, core.Null)
}

func TestC1mGapUpFirstPrintMeasuredAgainstPreviousClose(t *testing.T) {
	ts := newTS5()
	steady(ts, 3)
	pm1(ts, 3, 1, 11.00, 10) // first print of the minute already +10%
	wantC1m(t, ts, "gap up", 10, core.Null)
}

func TestC1mFirstCandleHasNoPreviousClose(t *testing.T) {
	ts := newTS5()
	pm1(ts, 0, 5, 10.00, 100)
	wantC1m(t, ts, "first candle", core.Null, core.Null)
}

func TestC1mSurvivesDayReset(t *testing.T) {
	ts := newTS5()
	steady(ts, 10)
	pm1(ts, 10, 10, 10.50, 500)
	ts.resetSession()
	if ts.c1m.cur == 0 || ts.c1m.first == 0 {
		t.Fatal("the 1-minute volume tracker must survive resetSession")
	}
	clockAt(ts, 10, 40) // baseSec was cleared by the reset; the clock restarts
	wantC1m(t, ts, "after reset", 5, 5)
}
