package engine

import (
	"math"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// POWER SETUP (core/powersetup.go, docs/power-setup.md): evaluated once, at the
// close of every power candle that alerts. Three filters and a fixed plan, all
// plain float arithmetic: no allocation.
//
//	A      the close is < 21 % above the day's open (the first traded 1m bar
//	       from 04:00 ET; research feat.py fromopen)
//	slope  the 1m 9 EMA rose >= 2.1 % over the last 3 bars (ema[j]/ema[j-3],
//	       the EMA INCLUDING the power candle)
//	gap    the gap at the open vs the previous close is >= core.PsMinGapPct
//	       (OFF by default; env SCANNER_PS_MIN_GAP > 0 enables it): open = the first traded 1m
//	       bar from 04:00 ET, for pre-market AND regular-hours signals alike
//	       (the research used the 09:30 open: look-ahead for pre-market
//	       signals, deliberately not copied); previous close = the scanner's
//	       prevClose (REST snapshot prevDay close = the previous 16:00 regular close)
//
// Differences from the research EMA (feat.py): the engine's judge EMA is
// SMA-seeded over its first 9 closes and carries across days; feat.py seeds
// from the first close of the fetched window (previous day + today). The two
// converge within ~30 bars (0.8^30 = 0.1 %), so the slope agrees to well under
// a hundredth of a percentage point once a name has traded 40+ bars. Both fold
// every traded 1m bar (an empty minute is skipped), and both include the
// signal bar's own close.

// emaSlopeMinN is the number of folded closes the 3-bar slope needs: the EMA is
// valid from the 9th close, and ema[j-3] must be valid too.
const emaSlopeMinN = emaPeriod + 3

// rthStartOff is 09:30 ET as an offset from the 04:00 ET day start.
const rthStartOff = 5*3_600_000 + 30*60_000

// psEval is the verdict and numbers of one evaluation. The floats are
// core.Null when unknown.
type psEval struct {
	skip                                      string // the quiet window the filters passed inside (no setup)
	pass                                      bool
	fromOpen, slope, gap, entry, stop, target float64
	baseRange                                 float64 // the A+ grade's base range, % (core.PsBaseRangePct); Null = unknown
}

// baseRange is the A+ grade's base: (max high - min low) / min low over the
// last core.PsAplusBars judged traded bars BEFORE the bar being judged (the
// early ring; the power candle itself is pushed after the judges), the way
// research feat.py computes rng15 (bars[j-15:j], across the day boundary too).
// No allocation.
func (d *dildo1m) baseRange() float64 {
	n := min(d.eN, core.PsAplusBars)
	hi, lo := math.Inf(-1), math.Inf(1)
	for k, i := 0, d.eHead; k < n; k++ {
		if i--; i < 0 {
			i = earlyRing - 1
		}
		hi, lo = max(hi, d.eHi[i]), min(lo, d.eLo[i])
	}
	return core.PsBaseRangePct(hi, lo, n)
}

// foldOpen records the ET day's opens from the bar being folded (first traded
// bar from 04:00 / from 09:30). A part-observed first bar makes the 04:00 open
// untrustworthy (the engine joined mid-day), so the filters read it as unknown.
func (d *dildo1m) foldOpen(barMs, dayStart int64, o float64, partial bool) {
	if barMs < dayStart {
		return
	}
	if d.open == 0 && !d.openBad {
		if partial {
			d.openBad = true
		} else {
			d.open = o
		}
	}
	if d.rthOpen == 0 && !partial && barMs >= dayStart+rthStartOff {
		d.rthOpen = o
	}
}

// evalPowerSetup judges the three filters and the plan for the power candle
// closing at c, barMs. The judge EMA already includes this bar (judgeBar step 2).
func (t *tickerState) evalPowerSetup(barMs, dayStart int64, c float64) psEval {
	d := &t.d1
	ev := psEval{fromOpen: core.Null, slope: core.Null, gap: core.Null, entry: core.Null, stop: core.Null, target: core.Null, baseRange: d.baseRange()}
	if d.open > 0 && !d.openBad {
		ev.fromOpen = (c/d.open - 1) * 100
	}
	if d.emaN >= emaSlopeMinN && d.emaH[2] > 0 {
		ev.slope = (d.ema/d.emaH[2] - 1) * 100
	}
	if pc := float64(t.prevClose); pc > 0 {
		// always the 04:00 open (no look-ahead to the 09:30 open) vs the previous regular close
		if !d.openBad && d.open > 0 {
			ev.gap = (d.open/pc - 1) * 100
		}
	}
	if d.emaN >= emaPeriod && d.ema > 0 {
		ev.entry, ev.stop, ev.target = core.PowerSetupPlan(d.ema)
	}
	ev.pass = core.PsPassAll(ev.fromOpen, ev.slope, ev.gap) && !math.IsNaN(ev.entry)
	if ev.pass {
		if q := core.PsQuietName(int(240 + (barMs-dayStart)/60_000)); q != "" {
			ev.pass, ev.skip = false, q
		}
	}
	return ev
}

// stampRow writes the evaluation onto a row (age = the power candle's age).
func (e *psEval) stampRow(r *core.Row, age float64) {
	r.PsPass = 0 // 1 = a setup, 2 = the filters passed inside a quiet window (skipped)
	if e.pass {
		r.PsPass = 1
	} else if e.skip != "" {
		r.PsPass = 2
	}
	r.PsFromOpen, r.PsEmaSlope, r.PsGap = e.fromOpen, e.slope, e.gap
	r.PsEntry, r.PsStop, r.PsTarget = e.entry, e.stop, e.target
	r.PsAge = age
	r.PsBaseRange = e.baseRange
	r.PsAplus = 0
	if e.pass && core.PsIsAplus(e.baseRange) {
		r.PsAplus = 1
	}
}
