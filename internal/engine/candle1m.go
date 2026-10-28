package engine

import "github.com/eelkonkoopman/marketscanner/internal/core"

// 1-minute candle volume tracker (scan fields c1mChg / c1mRvol): "5% up on a
// 1-minute candle with 3x the volume of the 9 candles before it".
//
// Candles are CLOCK minutes keyed on each agg's OWN StartMs (minute =
// StartMs / 60000), like the 1-minute EMA and the 5-minute tracker. A candle's
// close is its LATEST print by StartMs and its volume is the sum of agg.Vol.
//
// Finalization is LAZY: the forming candle is folded into the ring only when a
// print for a later minute arrives, and every read (toRow) works out for itself
// which minute is "now" (baseSec, advanced by the 1 Hz clock for every tracked
// ticker) and treats a forming candle whose minute has passed as completed.
// So no clock hook is needed, a candle "closes" on time even for a stock that
// went quiet, and a late per-second agg for the minute that just ended is still
// accepted until the next minute's first print arrives (no grace window to tune).
//
// This tracker is NOT reset at the 04:00 day rollover (nor at session
// boundaries): like the 9 EMA it is about the most recent minutes, and a reset
// would only add a nine-minute blind spot to every ticker each morning.
//
// Memory per ticker: ~210 B (ring 10 x 16 B + ~50 B of scalars), ~2.3 MB for
// 11k tickers.

const (
	c1mRing  = 10 // the displayed candle + the 9 before it
	c1mBasis = 9  // candles in the volume baseline
)

type vol1mSlot struct {
	vol int64
	min int32 // the clock minute this slot holds (0 = empty); a slot is only valid when it equals the minute asked for
}

type vol1m struct {
	ring [c1mRing]vol1mSlot // FINALIZED traded minutes, slot = minute % 10; an untraded minute simply has no slot (volume 0)

	cur     int32   // forming (latest) minute with prints; 0 = none yet
	curVol  int64   // its volume so far
	curCl   float64 // its close so far (latest print by StartMs)
	curClTs int64   // StartMs of the print that set curCl

	done    int32   // latest FINALIZED minute; prints for a minute at or before it are late and ignored
	lastCl  float64 // close of the latest finalized traded candle (= done's close)
	pPrevCl float64 // the previous-close of that candle: the close of the traded candle before it (0 = none)

	first int32 // first minute whose volume is fully known (0 = nothing seen yet); earlier minutes are UNKNOWN, not zero
}

// accumC1m folds one aggregate into the 1-minute volume tracker.
func (t *tickerState) accumC1m(a core.Agg) {
	ts := a.StartMs
	if ts == 0 {
		ts = a.EndMs - 1
	}
	cl := a.Close
	if ts <= 0 || cl <= 0 {
		return
	}
	v := &t.c1m
	m := int32(ts / m1Ms)
	vol := a.Vol
	if vol < 0 {
		vol = 0
	}
	switch {
	case m <= v.done:
		return // that minute is already finalized: a late print
	case m > v.cur:
		if v.cur != 0 {
			v.finalize()
		} else if v.first == 0 {
			// First print this tracker has seen. A minute that opened before
			// the engine was listening is partial (its volume understates), so
			// the first fully observed minute is the next one.
			v.first = m
			if int64(m)*m1Ms < t.m5ListenMs {
				v.first = m + 1
			}
		}
		v.cur, v.curVol, v.curCl, v.curClTs = m, vol, cl, ts
	case m == v.cur:
		v.curVol += vol
		if ts >= v.curClTs {
			v.curCl, v.curClTs = cl, ts // the close is the LATEST print, not the last to arrive
		}
		// otherwise: older than the forming minute but not finalized: dropped, as the 1-minute EMA tracker does
	}
}

// finalize folds the forming candle into the ring.
func (v *vol1m) finalize() {
	s := &v.ring[v.cur%c1mRing]
	s.vol, s.min = v.curVol, v.cur
	v.pPrevCl = v.lastCl
	v.lastCl = v.curCl
	v.done = v.cur
}

// rvol is candle x's volume over the mean volume of the 9 clock minutes before
// it. Null unless all nine were observed (x-9 >= first: earlier minutes are
// unknown, not zero) and the mean is positive. Untraded minutes inside the
// window count as zero volume.
func (v *vol1m) rvol(x int32, vol int64) float64 {
	if v.first == 0 || x-c1mBasis < v.first {
		return core.Null
	}
	var sum int64
	for k := x - c1mBasis; k < x; k++ {
		if s := &v.ring[k%c1mRing]; s.min == k {
			sum += s.vol
		}
	}
	if sum <= 0 {
		return core.Null
	}
	return float64(vol) / (float64(sum) / c1mBasis)
}

// c1mCand is the pair of metrics for one candle.
type c1mCand struct{ chg, rvol float64 }

// cand measures candle x: its close against prev (the latest traded candle's
// close before x; <= 0 = none) and its volume against the nine minutes before.
func (v *vol1m) cand(x int32, vol int64, cl, prev float64) c1mCand {
	c := c1mCand{chg: core.Null, rvol: v.rvol(x, vol)}
	if prev > 0 {
		c.chg = (cl - prev) / prev * 100
	}
	return c
}

// view returns the two scan fields for the DISPLAYED candle, given the current
// clock second. F is the forming candle (the current clock minute, if it has
// printed); P is the candle that completed most recently, but only while it is
// the minute immediately before now (it "just closed"). With no P, F is shown.
// With both, whichever has the higher chg is shown (tie -> P; F's Null chg ->
// P). Why: a spike that peaks at :55 must not vanish from a ">= 5%" scan at :00
// when the new candle opens with almost no volume, and c1mChg and c1mRvol must
// always describe the SAME candle so an AND filter over them is coherent.
func (v *vol1m) view(nowSec int64) (chg, rvol float64) {
	if v.cur == 0 || nowSec <= 0 {
		return core.Null, core.Null
	}
	now := int32(nowSec / 60)
	if now < v.cur {
		now = v.cur // never let a lagging clock push the forming candle into the future
	}
	var f, p c1mCand
	hasF, hasP := false, false
	if v.cur == now {
		f, hasF = v.cand(v.cur, v.curVol, v.curCl, v.lastCl), true
		if v.done == now-1 { // finalized, immediately before now
			s := &v.ring[v.done%c1mRing]
			p, hasP = v.cand(v.done, s.vol, v.lastCl, v.pPrevCl), true
		}
	} else if v.cur == now-1 { // completed by the clock, not yet folded into the ring
		p, hasP = v.cand(v.cur, v.curVol, v.curCl, v.lastCl), true
	}
	switch {
	case hasF && hasP:
		if !core.IsNull(f.chg) && (core.IsNull(p.chg) || f.chg > p.chg) {
			return f.chg, f.rvol
		}
		return p.chg, p.rvol
	case hasF:
		return f.chg, f.rvol
	case hasP:
		return p.chg, p.rvol
	}
	return core.Null, core.Null
}
