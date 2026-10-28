// Package pattern measures the bull-flag / tight-consolidation setup on one
// ticker's daily bars: a sharp advance (the POLE), then a TIGHT sideways range
// (the BASE) that holds most of the advance on drying-up volume — the shape
// that precedes a breakout through the base's high.
//
// The five measurements, in the order they matter:
//
//	retrace  how much of the ADVANCE the pullback gave back (not how much of
//	         the price). THE discriminator: a 6% dip is nothing after a 100%
//	         run and everything after a 10% one, so only the ratio separates a
//	         stock that is resting from one whose move has already failed. It
//	         rejects more false positives than the other four combined.
//	range    (baseHigh-baseLow)/baseMid — how coiled the base is.
//	volDry   base average volume / pole average volume. Below 1.0 = supply
//	         drying up (nobody left who wants out); above 1.0 = distribution.
//	poleGain the size of the advance. A gate, not a virtue — see score.
//	bars     how long the base has held.
//
// WHERE THE BASE STARTS is the whole problem, and neither obvious answer works
// on its own:
//
//   - Scoring every candidate length and keeping the best collapses to the
//     minimum length every time, because ANY tail of a base is at least as
//     tight and at least as shallow as the whole thing. It would report a
//     3-bar range for a 12-bar base — flattering exactly the two numbers the
//     user filters on.
//   - Taking the longest length that still passes the tightness gate does the
//     opposite: a slow, steady advance fits inside a 15% range too, so the
//     "base" quietly swallows the top third of the pole.
//
// So we take the LONGEST admissible base and add TWO gates that together tell
// pole bars from base bars. A base has to be CAPPED at both ends:
//
//   - It must OPEN near its high: the base's first bar must have traded within
//     baseStartTolPct of the base's high. A bar 13% under the eventual high is
//     still climbing — that is pole, not base. The tolerance is a band rather
//     than a knife edge, in the same spirit as trend's ±1% slope band.
//   - It must not CLOSE on a new one: the base's highest high may not be its
//     own final bar. The opening band cannot catch a base that is still
//     advancing, because it is measured against the very high that base is
//     still making — an uninterrupted uptrend fits inside baseStartTolPct AND
//     maxRangePct over its last few sessions exactly as well as a real coil
//     does, at every base length, so no choice of minBaseBars separates them.
//     A consolidation HOLDS a high it has already made. The second gate is
//     also what makes BaseHigh mean something: if the base's high were today's
//     high, the live breakout overlay would degenerate into "printed a new
//     daily high" and fire every session on every name in an uptrend.
//
// Longest-then-gated-at-both-ends measures the whole consolidation honestly.
//
// State is 0 (no pattern) or 1 (coiling). This package NEVER returns 2:
// breakout is assigned live by the scanner overlay when a tick's price clears
// the stored BaseHigh, because a breakout happens intraday and a nightly
// classification of one would always be a day late.
//
// Thresholds are starting points for DAILY bars, all named in the one block
// below so they can be tuned against real output. Params carries the same
// gates as a value so the identical geometry can run over other bar sizes:
// DailyParams is the const block verbatim, IntradayParams retunes the one
// scale-sensitive knob for 5-minute session buckets. Pure functions, no I/O,
// no state, no dependency beyond core.
package pattern

import (
	"math"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

const (
	// --- history / geometry gates -------------------------------------------

	// minBars: below this we refuse outright. 20 sessions is the least that can
	// show a pole, a base, and enough context to tell them apart, and a wrong
	// "setup" is worse than no answer (trend.minCloses takes the same line).
	minBars = 20
	// minBaseBars: two quiet sessions are noise, not a consolidation.
	minBaseBars = 3
	// maxBaseBars: past ~3 weeks the shape stops being a flag and becomes a
	// trading range of its own kind, with different follow-through behaviour.
	maxBaseBars = 15
	// poleMinBars: sessions that must exist BEFORE the base for a swing low to
	// mean anything at all.
	poleMinBars = 8
	// poleMaxBars: how far back we hunt for the swing low that launched the
	// advance. ~5 weeks; a pole older than that is not what is holding price up.
	poleMaxBars = 25

	// --- admissibility gates: failing ANY of these means "not a flag" --------

	// minPoleGainPct: no advance, no flag. 20% inside poleMaxBars sessions is a
	// real move on a daily chart without being so rare it only catches squeezes.
	minPoleGainPct = 20.0
	// intradayMinPoleGainPct: the ONE knob IntradayParams turns, because it is
	// the one absolute-percentage gate — see IntradayParams for why the other
	// nine thresholds carry over to 5-minute buckets unchanged.
	intradayMinPoleGainPct = 10.0
	// maxRetracePct: THE discriminator — percent OF THE POLE given back. Past
	// ~40% the advance has failed and the "base" is really a downtrend pausing;
	// the classic 38.2% Fibonacci line sits just under this on purpose.
	maxRetracePct = 40.0
	// maxRangePct: base tightness ceiling, (high-low)/mid. Wider than ~15% on a
	// daily chart is not a coil, it is a swing range.
	maxRangePct = 15.0
	// maxVolDryUp: base avg volume / pole avg volume. Below 1.0 is drying up.
	// The ceiling sits ABOVE 1.0 deliberately: 1.0 exactly would be a knife
	// edge, and a base printing roughly the pole's volume is merely
	// uninformative, not bearish. Past 1.25 the base is absorbing real supply.
	maxVolDryUp = 1.25
	// baseStartTolPct: the base's first bar must have traded within this much of
	// the base's high. This is half of the gate that separates a base bar from a
	// pole bar — see the package doc. Without it a slow advance fits inside
	// maxRangePct and gets mislabelled as the consolidation it is still
	// building toward. The other half (the base may not make its high on its own
	// final bar) needs no threshold at all.
	baseStartTolPct = 5.0

	// --- scoring anchors: the "full marks" end of each ingredient ------------

	idealRetracePct   = 10.0 // a flag that barely gives anything back
	idealRangePct     = 5.0  // a genuinely coiled fortnight
	idealVolDry       = 0.55 // base trading at ~half the pole's volume
	strongPoleGainPct = 60.0 // beyond this a bigger pole earns no extra credit

	// --- score weights, summing to 100 --------------------------------------

	wRetrace = 40.0
	wRange   = 25.0
	wVolDry  = 20.0
	wPole    = 15.0
)

// Params carries every threshold gate from the const block above as a value,
// so the same geometry can run over bars of any resolution. The const block
// stays the single home of each threshold's rationale — DailyParams returns it
// verbatim — and ALL ten gates are carried, because a knob missing here would
// be silently frozen at its daily value for every caller. The scoring anchors
// and weights are deliberately NOT carried: they state what a good flag looks
// like relative to these gates, and score derives its "worst" anchors from the
// gates in this struct, so they follow whatever Params says.
type Params struct {
	// --- history / geometry gates ---
	MinBars     int // refuse outright below this many bars
	MinBaseBars int // shortest admissible base
	MaxBaseBars int // longest admissible base
	PoleMinBars int // bars that must exist before the base
	PoleMaxBars int // how far back to hunt for the pole's swing low

	// --- admissibility gates ---
	MinPoleGainPct  float64 // % advance: no advance, no flag
	MaxRetracePct   float64 // % OF THE POLE given back — the discriminator
	MaxRangePct     float64 // base tightness ceiling, (high-low)/mid as a %
	MaxVolDryUp     float64 // ceiling on base avg volume / pole avg volume
	BaseStartTolPct float64 // base's first bar must trade within this % of its high
}

// DailyParams is the const block as a value: the thresholds for daily bars,
// unchanged. Analyze(bars) and AnalyzeWith(DailyParams(), bars) are the same
// call by construction.
func DailyParams() Params {
	return Params{
		MinBars:         minBars,
		MinBaseBars:     minBaseBars,
		MaxBaseBars:     maxBaseBars,
		PoleMinBars:     poleMinBars,
		PoleMaxBars:     poleMaxBars,
		MinPoleGainPct:  minPoleGainPct,
		MaxRetracePct:   maxRetracePct,
		MaxRangePct:     maxRangePct,
		MaxVolDryUp:     maxVolDryUp,
		BaseStartTolPct: baseStartTolPct,
	}
}

// IntradayParams is DailyParams with ONE knob turned: MinPoleGainPct drops
// from 20 to 10 for 5-minute session buckets.
//
// Every other threshold is scale-free by construction — retrace is a percent
// OF THE POLE, range is a percent of the base's mid, volume dry-up is a ratio
// — and the bar-count windows map directly onto the intraday shape this preset
// hunts: 20 bars minimum ≈ 100 minutes of session before we will say anything,
// a base of 3-15 buckets ≈ a 15-75 minute coil, and a pole hunt of up to 25
// buckets ≈ two hours of advance. Only the absolute %-advance gate is
// scale-sensitive: 20% is a real move on a daily chart, but intraday a 10%
// push inside two hours is already a serious move — and the pole stays a
// filterable field, so a user who wants bigger intraday poles demands more at
// scan time instead of here.
func IntradayParams() Params {
	p := DailyParams()
	p.MinPoleGainPct = intradayMinPoleGainPct
	return p
}

// Analyze measures the bull-flag setup on one ticker's daily bars
// (oldest→newest) and reports ok=false when the history is too short or no
// admissible base exists — mirroring trend.Classify, where Null beats a guess.
//
// AsOf comes from the newest bar's own Date, so a symbol that stopped printing
// carries the date of ITS last bar rather than the recompute's wall clock.
func Analyze(bars []core.DailyBar) (core.FlagRef, bool) {
	return AnalyzeWith(DailyParams(), bars)
}

// AnalyzeWith is Analyze under an explicit threshold set: the identical
// geometry, gated by p instead of the daily consts. The intraday worker runs
// it with IntradayParams over 5-minute session buckets carried as
// core.DailyBar — everything Analyze says about its bars and its result holds
// here too, per bar of whatever size p was tuned for.
func AnalyzeWith(p Params, bars []core.DailyBar) (core.FlagRef, bool) {
	n := len(bars)
	if n < p.MinBars {
		return core.FlagRef{}, false
	}
	// Longest admissible base wins (see the package doc), so walk the candidate
	// lengths DOWNWARD and take the first that clears every gate. The scan is
	// at most MaxBaseBars candidates of at most PoleMaxBars+MaxBaseBars bars —
	// a few hundred float ops per symbol per pass, free at a nightly cadence
	// and still cheap at the intraday worker's once-a-minute one.
	maxK := p.MaxBaseBars
	if lim := n - p.PoleMinBars; lim < maxK {
		maxK = lim
	}
	for k := maxK; k >= p.MinBaseBars; k-- {
		if ref, w := evaluateWith(p, bars, k); w == whyOK {
			return ref, true
		}
	}
	return core.FlagRef{}, false
}

// why records which gate rejected a candidate base. It exists because
// "rejected" is useless feedback when tuning thresholds against real output —
// "rejected because the base gave back 61% of the pole" is not — and because
// it lets the tests assert the REASON, not just the verdict.
type why int

const (
	whyOK              why = iota
	whyShortHistory        // not enough daily bars to say anything
	whyBaseTooShort        // fewer than minBaseBars trailing bars
	whyBaseTooLong         // more than maxBaseBars trailing bars
	whyBadData             // non-positive or non-finite prices
	whyFlatBase            // every base bar printed one identical price: nothing traded
	whyNoPole              // the advance into the base is too small (or absent)
	whyBaseNotAtHigh       // the "base" starts well under its own high: still pole
	whyBaseStillRising     // the "base" makes its high on its last bar: still advancing
	whyDeepRetrace         // gave back too much of the advance
	whyLooseRange          // the base is a swing range, not a coil
	whyVolExpanding        // volume grew in the base: distribution, not exhaustion
)

func (w why) String() string {
	switch w {
	case whyOK:
		return "ok"
	case whyShortHistory:
		return "short history"
	case whyBaseTooShort:
		return "base too short"
	case whyBaseTooLong:
		return "base too long"
	case whyBadData:
		return "bad data"
	case whyFlatBase:
		return "base never traded (all bars one identical price)"
	case whyNoPole:
		return "no pole"
	case whyBaseNotAtHigh:
		return "base does not start at its high"
	case whyBaseStillRising:
		return "base makes its high on its last bar"
	case whyDeepRetrace:
		return "retrace too deep"
	case whyLooseRange:
		return "range too loose"
	case whyVolExpanding:
		return "volume expanding"
	}
	return "unknown"
}

// evaluate is evaluateWith under DailyParams — the daily-preset view of one
// candidate, mirroring the Analyze/AnalyzeWith pair.
func evaluate(bars []core.DailyBar, k int) (core.FlagRef, why) {
	return evaluateWith(DailyParams(), bars, k)
}

// evaluateWith measures the single candidate whose base is the trailing k bars
// and returns the reason it is not a flag (whyOK if it is). The returned
// FlagRef is populated as far as the geometry got, rejection or not, so a
// caller — today only the tests — can see WHICH ingredient failed and by how
// much.
//
// Structural checks run first because they decide whether the geometry is even
// defined. Everything else is measured up front and then run through one gate
// cascade, ordered by how fundamental the objection is: no pole at all beats
// "that is still the pole", which beats a failed advance (retrace), which beats
// an uncoiled one (range), which beats a volume read (the noisiest ingredient).
// The two "that is still the pole" objections sit together: the base opening
// under its high and the base making its high on its last bar are the same
// complaint about opposite ends of the window.
func evaluateWith(p Params, bars []core.DailyBar, k int) (core.FlagRef, why) {
	n := len(bars)
	ref := core.FlagRef{Bars: k}
	if n > 0 {
		ref.AsOf = bars[n-1].Date
	}

	if n < p.MinBars {
		return ref, whyShortHistory
	}
	if k < p.MinBaseBars {
		return ref, whyBaseTooShort
	}
	if k > p.MaxBaseBars {
		return ref, whyBaseTooLong
	}
	baseStart := n - k
	if baseStart < p.PoleMinBars {
		return ref, whyShortHistory
	}

	// --- the base: the trailing k bars ---
	baseHigh, baseLow, baseVol := stats(bars[baseStart:])
	ref.BaseHigh = baseHigh // the level the live overlay compares Last against
	if !(baseLow > 0 && baseHigh >= baseLow) {
		return ref, whyBadData // halted shells, missing rows, NaN
	}
	if baseHigh == baseLow {
		// Every bar in the base printed one identical price: nothing traded.
		// On intraday session bars this is the engine's forward-fill
		// synthesizing flat hi=lo=close buckets for a ticker that went dead
		// (a halted daily shell looks the same), and it is the perfect
		// counterfeit of the setup — zero range, zero retrace, zero volume
		// riding the neutral-VolDry guard below. Fabricated quiet is not a
		// coil; a real one always has SOME wiggle. Rejecting on identical
		// PRICE (not missing volume) is deliberate: a base that moved on
		// unreported volume stays admissible per the VolDry neutral contract.
		return ref, whyFlatBase
	}
	// The same high with the final bar withheld, for the still-rising gate below.
	// k >= minBaseBars, so this slice is never empty.
	priorHigh, _, _ := stats(bars[baseStart : n-1])
	ref.RangePct = (baseHigh - baseLow) / ((baseHigh + baseLow) / 2) * 100

	// --- the pole: the advance immediately before the base ---
	// The swing low that launched it is the lowest low in the PoleMaxBars
	// bars preceding the base.
	from := baseStart - p.PoleMaxBars
	if from < 0 {
		from = 0
	}
	poleLow, poleLowIdx := 0.0, from
	for i := from; i < baseStart; i++ {
		if l := bars[i].L; l > 0 && (poleLow <= 0 || l < poleLow) {
			poleLow, poleLowIdx = l, i
		}
	}
	if poleLow <= 0 {
		return ref, whyBadData
	}
	// The run's high is the highest high AFTER that low, base included: when the
	// base ticks to a marginal new high before pulling back, THAT is the top the
	// pullback has to be measured from, not a stale pole tip. MARGINAL is the
	// operative word, and the still-rising gate below is what enforces it: an
	// advance that is still being made inside the "base" ends on the base's own
	// high, and neither the tightness gate nor the base-start band can see that
	// — a slow enough climb sits inside both of them at every base length.
	peak, peakIdx := bars[poleLowIdx].H, poleLowIdx
	for i := poleLowIdx; i < n; i++ {
		if h := bars[i].H; h > peak {
			peak, peakIdx = h, i
		}
	}
	if !(peak > poleLow) {
		return ref, whyNoPole // no advance at all: nothing to flag
	}
	ref.PoleGain = (peak/poleLow - 1) * 100

	// Retrace: the deepest giveback since the high, as a share of THE ADVANCE
	// rather than of price. Measuring it from the run high to the lowest low
	// since — instead of to the base's low — keeps it independent of where we
	// chose to draw the base boundary, so a shakeout bar left just outside the
	// base cannot flatter the number.
	pullbackLow := peak
	for i := peakIdx; i < n; i++ {
		if l := bars[i].L; l > 0 && l < pullbackLow {
			pullbackLow = l
		}
	}
	ref.Retrace = (peak - pullbackLow) / (peak - poleLow) * 100

	// Volume: base average against the pole's average, measured over the pole
	// proper (swing low → base start). EITHER side coming out non-positive is a
	// data gap, not a reading, and 1.0 = "unknown, neutral" is a fairer read of
	// that than throwing away an otherwise clean setup. The base side needs the
	// guard as much as the pole side does and for the same reason: a base that
	// printed no volume at all is a halted shell, and letting 0/poleVol through
	// would hand it a PERFECT supply-exhaustion score for not having traded.
	_, _, poleVol := stats(bars[poleLowIdx:baseStart])
	ref.VolDry = 1.0
	if poleVol > 0 && baseVol > 0 {
		ref.VolDry = baseVol / poleVol
	}

	switch {
	case ref.PoleGain < p.MinPoleGainPct:
		return ref, whyNoPole
	case bars[baseStart].H < baseHigh*(1-p.BaseStartTolPct/100):
		return ref, whyBaseNotAtHigh
	case bars[n-1].H > priorHigh:
		return ref, whyBaseStillRising
	case ref.Retrace > p.MaxRetracePct:
		return ref, whyDeepRetrace
	case ref.RangePct > p.MaxRangePct:
		return ref, whyLooseRange
	case ref.VolDry > p.MaxVolDryUp:
		return ref, whyVolExpanding
	}

	sc := scoreWith(p, ref)
	if math.IsNaN(sc) || math.IsInf(sc, 0) {
		return ref, whyBadData // a NaN slipped past the comparisons above
	}
	ref.Score = sc
	ref.State = 1 // never 2 — breakout is the live overlay's call, see the package doc
	return ref, whyOK
}

// score composes the four ingredients into 0-100. The weights are deliberately
// lopsided, and stated here rather than buried in the arithmetic:
//
//	retrace  40  the discriminator. A base that gave back little of the advance
//	             is a rest; one that gave back a lot is a failed move wearing a
//	             base's clothes, and nothing else in the set separates the two.
//	range    25  tightness is the second-order form of the same idea — a coiled
//	             range means neither side is forcing the issue.
//	volDry   20  supply exhaustion. Corroborating rather than decisive: volume
//	             is the noisiest ingredient (splits, halts, thin tape).
//	pole     15  least weight ON PURPOSE. A huge pole is what makes the chart
//	             look exciting and is the ingredient with the least predictive
//	             value — plenty of 200% poles fail. It is a precondition (the
//	             minPoleGainPct gate), not a quality.
//
// Base length is scored at zero weight on purpose too: it is a gate, not a
// virtue. A 4-bar and a 12-bar tight base are both valid setups and we have no
// evidence yet for preferring either, so expressing a preference would be
// inventing information.
func score(r core.FlagRef) float64 {
	return scoreWith(DailyParams(), r)
}

// scoreWith anchors the "zero marks" end of each ingredient at the gate in p
// rather than at the daily const: an ingredient sitting exactly on the gate
// that ADMITTED it scores zero, whichever preset that gate came from. The
// "full marks" anchors and the weights stay the shared consts — what a good
// flag looks like relative to its gates does not change with the bar size.
func scoreWith(p Params, r core.FlagRef) float64 {
	return wRetrace*quality(r.Retrace, idealRetracePct, p.MaxRetracePct) +
		wRange*quality(r.RangePct, idealRangePct, p.MaxRangePct) +
		wVolDry*quality(r.VolDry, idealVolDry, p.MaxVolDryUp) +
		wPole*quality(r.PoleGain, strongPoleGainPct, p.MinPoleGainPct)
}

// quality maps a raw measurement onto 0..1: 1.0 at (or past) best, 0.0 at (or
// past) worst, linear in between. best may sit above or below worst, so the one
// helper serves the "lower is better" ingredients (retrace, range, volume) and
// the "higher is better" one (pole gain) alike.
func quality(v, best, worst float64) float64 {
	if best == worst {
		return 0
	}
	q := (v - worst) / (best - worst)
	if q < 0 {
		return 0
	}
	if q > 1 {
		return 1
	}
	return q // NaN falls through both comparisons; evaluate catches it
}

// stats returns the highest high, lowest low and mean volume of a bar run.
func stats(bs []core.DailyBar) (high, low, avgVol float64) {
	if len(bs) == 0 {
		return 0, 0, 0
	}
	high, low = bs[0].H, bs[0].L
	vol := 0.0
	for _, b := range bs {
		if b.H > high {
			high = b.H
		}
		if b.L < low {
			low = b.L
		}
		vol += float64(b.V)
	}
	return high, low, vol / float64(len(bs))
}
