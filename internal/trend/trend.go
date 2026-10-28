// Package trend classifies a ticker's daily-chart context from its recent
// daily closes. Motivation (user observation, 2026-07-28): news-pop stocks in
// a daily uptrend or flat base tended to follow through, while pops in daily
// downtrends faded — so every scanner row carries this as a decision aid.
//
// Classification is the classic simple-moving-average definition, chosen for
// explainability over cleverness:
//
//	uptrend   = last close ABOVE the SMA and the SMA rising  (> +1% over ~5 sessions)
//	downtrend = last close BELOW the SMA and the SMA falling (< -1% over ~5 sessions)
//	flat      = everything else
//
// A 20-day SMA when at least 25 closes exist, falling back to a 10-day SMA at
// 15-24 closes (recent IPOs); fewer than 15 closes = unknown. The ±1% slope
// band is what makes "flat" a real bucket instead of a knife edge.
package trend

import (
	"github.com/eelkonkoopman/marketscanner/internal/core"
)

const (
	minCloses    = 15 // below this: unknown (no classification at all)
	fullWindow   = 20
	shortWindow  = 10
	slopeSpan    = 5   // sessions between the two SMA readings
	slopeBandPct = 1.0 // |SMA drift| must exceed this to leave "flat"
)

// Classify returns the trend for one ticker's daily closes (oldest→newest).
// ok=false when history is too short to say anything.
func Classify(closes []float64, asOf string) (core.TrendRef, bool) {
	n := len(closes)
	if n < minCloses {
		return core.TrendRef{}, false
	}
	w := fullWindow
	if n < fullWindow+slopeSpan {
		w = shortWindow
	}

	last := closes[n-1]
	smaNow := mean(closes[n-w:])
	smaPrev := mean(closes[n-w-slopeSpan : n-slopeSpan])
	if smaNow <= 0 || smaPrev <= 0 || last <= 0 {
		return core.TrendRef{}, false // defensive: garbage rows (halted shells etc.)
	}

	slopePct := (smaNow/smaPrev - 1) * 100
	vsSMA := (last/smaNow - 1) * 100

	dir := 0
	switch {
	case last > smaNow && slopePct > slopeBandPct:
		dir = 1
	case last < smaNow && slopePct < -slopeBandPct:
		dir = -1
	}

	chgIdx := n - (fullWindow + 1)
	if chgIdx < 0 {
		chgIdx = 0
	}
	chg20 := 0.0
	if closes[chgIdx] > 0 {
		chg20 = (last/closes[chgIdx] - 1) * 100
	}

	return core.TrendRef{DTrend: dir, Chg20d: chg20, VsSMA: vsSMA, Days: n, AsOf: asOf}, true
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}
