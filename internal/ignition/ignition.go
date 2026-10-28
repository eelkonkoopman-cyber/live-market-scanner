// Package ignition is the Go port of the IGNITION rule the Signal window runs
// in the browser (web/src/signals/ignition.ts): the small first candle out of a
// quiet base right before a dildo. Example NCPL 2026-10-14 ~09:06 ET: a flat
// ~1.75-1.80 base, then a ~+5% green candle on a volume spike, then the big
// dildo the next minute. An ALERT, a "look now" prompt, not a system
// (uncalibrated).
//
// The alert is engine-native: the scanner engine's shared closed-1-minute-bar
// judge (internal/engine/ignition.go) calls Judge with its own bar rings;
// Evaluate here is the chart's whole-series form, and both are pinned to the TS
// rule by golden vectors (web/src/signals/fixtures/golden/ignition-golden.json;
// golden_test.go).
//
// THE RULE, per closed 1-minute bar (o, h, c, v):
//  1. Quiet base: the previous BaseBars bars (any day; at least
//     dildopb.EarlyMinBaseBars of them) span (max high - min low)/min low <=
//     MaxBasePct.
//  2. Volume >= MinVolX x the mean volume of the previous VolBars bars.
//  3. Green body (c-o)/o between MinBodyPct and MaxBodyPct (a full dildo is
//     not an ignition).
//  4. c > the base high AND h > the day high before it (04:00 ET on; the first
//     bar of the day passes). Rules 1 and 4 are dildopb.JudgeEarly's, reused.
//  5. Liquidity: c >= MinPrice and c x v >= MinDvol.
//
// One ignition per base: after one, the next fires only once a later bar's
// base fails rule 1 (the base was broken) or a new ET day began; and at most
// one per CooldownMin minutes per symbol.
//
// Pure: no clock reads; Judge allocates nothing.
package ignition

import (
	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
)

// Params mirrors IgnitionParams in ignition.ts field for field (percentages in %).
type Params struct {
	Enabled     bool    // true
	BaseBars    int     // 15
	MaxBasePct  float64 // 8
	VolBars     int     // 10
	MinVolX     float64 // 5
	MinBodyPct  float64 // 2
	MaxBodyPct  float64 // 8
	MinPrice    float64 // 0.20
	MinDvol     float64 // 50 000
	CooldownMin int     // 10
}

// DefaultParams is DEFAULT_IGNITION_PARAMS in ignition.ts.
func DefaultParams() Params {
	return Params{Enabled: true, BaseBars: 15, MaxBasePct: 8, VolBars: 10, MinVolX: 5, MinBodyPct: 2, MaxBodyPct: 8,
		MinPrice: 0.2, MinDvol: 50_000, CooldownMin: 10}
}

const (
	KindIgnition  = "ignition"
	LabelIgnition = "IGNITION"
)

// Reason is why a bar is not an ignition (OK = it is).
type Reason int

const (
	OK         Reason = iota
	LowPrice          // close under MinPrice
	LowDvol           // close x volume under MinDvol
	BodySmall         // not green enough
	BodyBig           // a dildo, not an ignition
	NoVolume          // volume under MinVolX x the prior mean
	FewBars           // fewer than dildopb.EarlyMinBaseBars base bars
	WideBase          // the base is not quiet (or has no positive low)
	NoBreakout        // the close is not above the base high
	NotNewHigh        // the high is not above the day high before it
)

// Verdict is Judge's answer and what it measured.
type Verdict struct {
	Reason   Reason
	BodyPct  float64
	VolX     float64 // +Inf with a prior mean of 0
	Dvol     float64
	RangePct float64 // the base's (0 when too few bars)
	BaseHigh float64
	DayHigh  float64
}

// Quiet reports whether the bars before the judged one formed a quiet base
// (rule 1 held), whatever else failed: the "one per base" latch re-arms on a
// bar where it is false.
func (v Verdict) Quiet() bool { return v.Reason != FewBars && v.Reason != WideBase }

func earlyParams(p Params) dildopb.EarlyParams {
	return dildopb.EarlyParams{BaseBars: p.BaseBars, MaxRangePct: p.MaxBasePct, FirstOnly: false, NewHighOfDay: true}
}

// Judge judges one closed bar. hs/ls are the base bars before it, oldest
// first (only the last BaseBars are used); sumVol/nVol the sum (oldest ->
// newest, float64 each) and count of the previous up-to-VolBars bars' volumes;
// dayHighBefore the highest high of the earlier bars of its ET day from 04:00
// (<= 0: none). Allocates nothing.
func Judge(hs, ls []float64, sumVol float64, nVol int, o, h, c float64, v int64, dayHighBefore float64, p Params) Verdict {
	ev := dildopb.JudgeEarly(hs, ls, c, h, dayHighBefore, false, earlyParams(p))
	vd := Verdict{RangePct: ev.RangePct, BaseHigh: ev.BaseHigh, DayHigh: ev.DayHigh}
	vd.Dvol = c * float64(v)
	if o > 0 {
		vd.BodyPct = (c - o) / o * 100
	}
	if nVol > 0 {
		vd.VolX = float64(v) / (sumVol / float64(nVol))
	}
	switch ev.Reason {
	case dildopb.EarlyFewBars:
		vd.Reason = FewBars
	case dildopb.EarlyNoLow, dildopb.EarlyWideBase:
		vd.Reason = WideBase
	}
	if nVol == 0 && vd.Reason == OK {
		vd.Reason = FewBars
	}
	if vd.Reason != OK {
		return vd
	}
	switch {
	case !(c >= p.MinPrice):
		vd.Reason = LowPrice
	case !(vd.Dvol >= p.MinDvol):
		vd.Reason = LowDvol
	case !(vd.BodyPct >= p.MinBodyPct):
		vd.Reason = BodySmall
	case !(vd.BodyPct <= p.MaxBodyPct):
		vd.Reason = BodyBig
	case !(vd.VolX >= p.MinVolX):
		vd.Reason = NoVolume
	case ev.Reason == dildopb.EarlyNoBreakout:
		vd.Reason = NoBreakout
	case ev.Reason == dildopb.EarlyNotNewHigh:
		vd.Reason = NotNewHigh
	}
	return vd
}

// Latch is the per-symbol "one per base, one per cooldown" state.
type Latch struct {
	Spent  bool  // an ignition fired on the current base
	LastMs int64 // the latest ignition bar start; 0 = none
}

// Step folds one judged bar (starting at barMs, its verdict vd; newDay: the
// first bar of a new ET day) into the latch and reports whether it fires.
func (l *Latch) Step(barMs int64, vd Verdict, newDay bool, p Params) bool {
	if newDay || !vd.Quiet() {
		l.Spent = false
	}
	if vd.Reason != OK || l.Spent || (l.LastMs != 0 && barMs-l.LastMs < int64(p.CooldownMin)*60_000) {
		return false
	}
	l.Spent, l.LastMs = true, barMs
	return true
}

// Event is one ignition.
type Event struct {
	Ts       int64
	Price    float64 // the close
	BodyPct  float64
	VolX     float64
	Dvol     float64
	RangePct float64
	BaseHigh float64
	DayHigh  float64
}

// Evaluate runs the rule over bars (ascending, may span days). tfMs must be
// 60 000. The last bar is FORMING iff nowMs < last.T + tfMs and is ignored.
// evaluateIgnition in ignition.ts.
func Evaluate(bars []core.Bar, tfMs, nowMs int64, p Params) []Event {
	n := len(bars)
	if tfMs != 60_000 || !p.Enabled || n == 0 {
		return nil
	}
	last := n - 1
	if nowMs < bars[n-1].T+tfMs {
		last = n - 2
	}
	hs, ls := make([]float64, n), make([]float64, n)
	for i := range bars {
		hs[i], ls[i] = bars[i].H, bars[i].L
	}
	var out []Event
	var l Latch
	var day int64
	dayHi := 0.0
	for j := 0; j <= last; j++ {
		b := &bars[j]
		ds := dildopb.DayStartMs(b.T)
		newDay := j == 0 || ds != day
		if newDay {
			day, dayHi = ds, 0
		}
		lo := max(0, j-p.BaseBars)
		vlo := max(0, j-p.VolBars)
		sumVol := 0.0
		for k := vlo; k < j; k++ {
			sumVol += float64(bars[k].V)
		}
		vd := Judge(hs[lo:j], ls[lo:j], sumVol, j-vlo, b.O, b.H, b.C, b.V, dayHi, p)
		if l.Step(b.T, vd, newDay, p) {
			out = append(out, Event{Ts: b.T, Price: b.C, BodyPct: vd.BodyPct, VolX: vd.VolX, Dvol: vd.Dvol,
				RangePct: vd.RangePct, BaseHigh: vd.BaseHigh, DayHigh: vd.DayHigh})
		}
		if b.T >= ds && b.H > dayHi {
			dayHi = b.H
		}
	}
	return out
}
