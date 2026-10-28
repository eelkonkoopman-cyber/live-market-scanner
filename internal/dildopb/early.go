package dildopb

import (
	"sort"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// THE EARLY GREEN DILDO (user, 2026-10-13): the first big candle out of a quiet
// base (BKYI 04:24, WORX 04:48, TCRT 06:58 ET), never a dildo in a name that has
// chopped all session (YJ 06:30 ET). One pure judge shared by the auto-trader's
// EarlyOnly filter (internal/autotrade) and the alert path (the engine's
// dildoEarly row field, flagscan's chart-side verdict, the SIGNALS history), so
// the trader and the alerts always apply the same rule. A dildo is early when
//   - the up to BaseBars 1-minute candles before it (any day; at least
//     EarlyMinBaseBars of them) span at most MaxRangePct: (max high - min low) /
//     min low,
//   - its close is above that base's highest high, and
//   - (FirstOnly) no earlier green dildo was seen in the symbol this ET day, and
//   - (NewHighOfDay, user 2026-10-14) its HIGH is above the highest high of every
//     earlier bar of the same ET day from 04:00 (pre-market counts, the previous
//     day's bars do not); the first bar of the day passes.

// EarlyMinBaseBars: fewer base candles than this and a dildo is not judged early.
const EarlyMinBaseBars = 10

// EarlyParams are the rule's knobs (the trader's settings carry their own copy).
type EarlyParams struct {
	BaseBars    int
	MaxRangePct float64
	FirstOnly   bool
	// NewHighOfDay: the dildo's high must be a new high of the ET day.
	NewHighOfDay bool
}

// DefaultEarlyParams: 30 bars, 8%, the first dildo of the day, a new high of the day.
func DefaultEarlyParams() EarlyParams {
	return EarlyParams{BaseBars: 30, MaxRangePct: 8, FirstOnly: true, NewHighOfDay: true}
}

// EarlyReason is why a dildo is not early (EarlyOK = it is).
type EarlyReason int

const (
	EarlyOK         EarlyReason = iota
	EarlyFewBars                // fewer than EarlyMinBaseBars base candles
	EarlyNoLow                  // the base has no positive low
	EarlyWideBase               // the base range is over MaxRangePct
	EarlyNoBreakout             // the close is not above the base high
	EarlyNotFirst               // an earlier dildo today (FirstOnly)
	EarlyNotNewHigh             // the high is not above the day high before it (NewHighOfDay)
)

// EarlyVerdict is the judge's answer and what it measured (RangePct / BaseHigh
// only once the base had enough bars and a low).
type EarlyVerdict struct {
	Reason   EarlyReason
	Bars     int
	RangePct float64
	BaseHigh float64
	DayHigh  float64 // the day high before the dildo (0 = no earlier bar today)
}

// Early reports whether the dildo is early.
func (v EarlyVerdict) Early() bool { return v.Reason == EarlyOK }

// JudgeEarly judges one dildo closing at closePx. highs/lows are the base
// candles before it, oldest first (only the last p.BaseBars are used);
// priorToday is whether an earlier green dildo was seen in the symbol this ET day;
// highPx is the dildo's high and dayHighBefore the highest high of the earlier
// bars of its ET day from 04:00 (<= 0: none, the first bar of the day).
func JudgeEarly(highs, lows []float64, closePx, highPx, dayHighBefore float64, priorToday bool, p EarlyParams) EarlyVerdict {
	n := min(len(highs), len(lows))
	if p.BaseBars > 0 && n > p.BaseBars {
		highs, lows = highs[len(highs)-p.BaseBars:], lows[len(lows)-p.BaseBars:]
		n = p.BaseBars
	} else {
		highs, lows = highs[len(highs)-n:], lows[len(lows)-n:]
	}
	v := EarlyVerdict{Bars: n, DayHigh: max(dayHighBefore, 0)}
	if n < EarlyMinBaseBars {
		v.Reason = EarlyFewBars
		return v
	}
	hi, lo := highs[0], lows[0]
	for i := 1; i < n; i++ {
		hi, lo = max(hi, highs[i]), min(lo, lows[i])
	}
	if !(lo > 0) {
		v.Reason = EarlyNoLow
		return v
	}
	v.RangePct, v.BaseHigh = (hi-lo)/lo*100, hi
	switch {
	case v.RangePct > p.MaxRangePct:
		v.Reason = EarlyWideBase
	case !(closePx > hi):
		v.Reason = EarlyNoBreakout
	case p.FirstOnly && priorToday:
		v.Reason = EarlyNotFirst
	case p.NewHighOfDay && dayHighBefore > 0 && !(highPx > dayHighBefore):
		v.Reason = EarlyNotNewHigh
	}
	return v
}

// EarlyFromBars judges the dildo whose bar starts at ts on a chart series
// (any order; bars at or after ts are ignored). closePx <= 0 takes the close of
// the bar at ts (its high is that bar's high; with no bar at ts, closePx).
func EarlyFromBars(bars []core.Bar, ts int64, closePx float64, priorToday bool, p EarlyParams) EarlyVerdict {
	base := make([]core.Bar, 0, len(bars))
	highPx := 0.0
	for _, b := range bars {
		if b.T > 0 && b.T < ts {
			base = append(base, b)
		} else if b.T == ts {
			highPx = max(b.H, b.C)
			if !(closePx > 0) {
				closePx = b.C
			}
		}
	}
	if !(highPx > 0) {
		highPx = closePx
	}
	dayHi := DayHighBefore(bars, ts)
	sort.Slice(base, func(i, j int) bool { return base[i].T < base[j].T })
	if p.BaseBars > 0 && len(base) > p.BaseBars {
		base = base[len(base)-p.BaseBars:]
	}
	hs, ls := make([]float64, len(base)), make([]float64, len(base))
	for i, b := range base {
		hs[i], ls[i] = b.H, b.L
	}
	return JudgeEarly(hs, ls, closePx, highPx, dayHi, priorToday, p)
}

// DayHighBefore is the highest high of the bars (any order) before ts on ts's
// ET day from 04:00 ET (pre-market counts, the previous day's bars do not);
// 0 = none.
func DayHighBefore(bars []core.Bar, ts int64) float64 {
	from := DayStartMs(ts)
	hi := 0.0
	for _, b := range bars {
		if b.T >= from && b.T < ts && b.H > hi {
			hi = b.H
		}
	}
	return hi
}

// DayStartMs is 04:00 ET of ts's ET day (the trading day's start).
func DayStartMs(ts int64) int64 {
	t := time.UnixMilli(ts).In(etLocation())
	return time.Date(t.Year(), t.Month(), t.Day(), 4, 0, 0, 0, etLocation()).UnixMilli()
}

// PriorDildoToday reports whether events hold a green dildo before ts on ts's ET day.
func PriorDildoToday(events []Event, ts int64) bool {
	day := etDayKey(ts)
	for i := range events {
		if e := &events[i]; e.Kind == KindDildo && e.Ts < ts && etDayKey(e.Ts) == day {
			return true
		}
	}
	return false
}
