package dildopb

import "github.com/eelkonkoopman/marketscanner/internal/core"

// Tolerant returns p with every candle threshold loosened by pct percent:
// the minimums (MinBodyPct, MinBodyX, MinVolX, MinDollarVol, MinPrice) scaled
// by (1 - pct/100) and the close-in-top-range maximum (MaxTopPct) by
// (1 + pct/100). Prior-bar counts and the pullback settings are unchanged.
func Tolerant(p Params, pct float64) Params {
	lo, hi := 1-pct/100, 1+pct/100
	p.MinBodyPct *= lo
	p.MinBodyX *= lo
	p.MinVolX *= lo
	p.MinDollarVol *= lo
	p.MinPrice *= lo
	p.MaxTopPct *= hi
	return p
}

// EvaluateTolerant is Evaluate, except that a bar whose start is in seen (a
// bar the live engine already called a green dildo) is accepted when it meets
// every candle threshold within tolPct percent (Tolerant). All other bars keep
// p's exact thresholds. With no seen bars or tolPct <= 0 it is Evaluate.
func EvaluateTolerant(bars []core.Bar, tfMs, nowMs int64, p Params, seen []int64, tolPct float64) Result {
	if len(seen) == 0 || !(tolPct > 0) {
		return evaluate(bars, tfMs, nowMs, p, nil, nil)
	}
	loose := Tolerant(p, tolPct)
	return evaluate(bars, tfMs, nowMs, p, &loose, seen)
}

func containsTs(seen []int64, ts int64) bool {
	for _, s := range seen {
		if s == ts {
			return true
		}
	}
	return false
}
