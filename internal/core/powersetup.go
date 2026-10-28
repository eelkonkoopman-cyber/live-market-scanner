package core

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// POWER SETUP: a power candle (engine, dildo.go) that also passes three
// filters evaluated at its close, with a fixed trade plan. See
// docs/power-setup.md for the rulebook and what the backtest showed.
//
// The numbers below are the researched system; they are deliberately
// constants (not tunables) so the alert always means the same thing.
const (
	KindPowerSetup  = "powerSetup" // SIGNALS history kind
	LabelPowerSetup = "P-SETUP"
	GradeAplus      = "A+" // the A+ grade: SIGNALS label "P-SETUP A+", note prefix "A+ tight base 3.8%; "

	PsMaxFromOpenPct = 21.0 // filter A: the close is LESS than this % above the day's open
	PsMinSlopePct    = 2.1  // the 1-minute 9 EMA rose at least this % over the last 3 bars
	PsStopFrac       = 0.06 // stop = entry x (1 - 6%)
	PsTargetR        = 3.0  // target = entry + 3R, R = entry - stop
	PsEntryValidSec  = 900  // the buy limit is valid ~15 minutes

	// A+ GRADE (2026-10-28; docs/power-setup.md): a setup whose base, the
	// PsAplusBars 1-minute bars before the power candle, spans at most
	// PsAplusMaxRangePct ((max high - min low) / min low, research feat.py
	// rng15). Fewer than PsAplusMinBars prior bars = unknown = not A+. The
	// order ticket sizes an A+ setup at PsAplusRiskMult x the selected risk.
	PsAplusBars        = 15
	PsAplusMinBars     = 3
	PsAplusMaxRangePct = 5.0
	PsAplusRiskMult    = 1.5
)

// PsIsAplus: the base range (%) qualifies for A+ (a Null range never does).
func PsIsAplus(baseRange float64) bool { return baseRange <= PsAplusMaxRangePct } // NaN compares false

// PsBaseRangePct is (max high - min low) / min low x 100 over the prior bars'
// highs and lows (the same length); Null when fewer than PsAplusMinBars or a
// low is not positive. No allocation.
func PsBaseRangePct(hi, lo float64, n int) float64 {
	if n < PsAplusMinBars || !(lo > 0) {
		return Null
	}
	return (hi - lo) / lo * 100
}

// PowerSetupPlan is the trade plan off the 1-minute 9 EMA at the signal: buy
// limit at the EMA, stop 6 % under it, target entry + 3R.
func PowerSetupPlan(ema float64) (entry, stop, target float64) {
	entry = ema
	stop = ema * (1 - PsStopFrac)
	target = entry + PsTargetR*(entry-stop)
	return
}

// PsVerdict names which of the three filters hold: every filter whose value is
// finite is judged; a Null (unknown) value fails.
func PsPassA(fromOpen float64) bool  { return fromOpen < PsMaxFromOpenPct } // NaN compares false
func PsPassSlope(slope float64) bool { return slope >= PsMinSlopePct }
func PsPassGap(gap float64) bool     { return PsMinGapPct <= 0 || gap >= PsMinGapPct }

// PsMinGapPct is the gap filter's threshold, %. 0 (the default) = the filter is
// OFF: the honest re-test showed a gap >= 10% rule only looked good through
// look-ahead. The gap is still computed and shown. Set once at start-up
// (cmd/scanner, env SCANNER_PS_MIN_GAP) before the engine runs.
var PsMinGapPct = 0.0

func PsPassAll(fromOpen, slope, gap float64) bool {
	return PsPassA(fromOpen) && PsPassSlope(slope) && PsPassGap(gap)
}

// PowerFilterTag is the learning tag of a power candle, e.g. "A✓ slope✗ gap✓".
// An unknown value (Null) prints "?" in place of the mark.
func PowerFilterTag(fromOpen, slope, gap float64) string {
	tag := "A" + psMark(fromOpen, PsPassA(fromOpen)) + " slope" + psMark(slope, PsPassSlope(slope))
	if PsMinGapPct > 0 { // the gap filter is off by default: no mark for a rule that is not applied
		tag += " gap" + psMark(gap, PsPassGap(gap))
	}
	return tag
}

func psMark(v float64, pass bool) string {
	switch {
	case math.IsNaN(v) || math.IsInf(v, 0):
		return "?"
	case pass:
		return "✓"
	}
	return "✗"
}

// PowerFilterValues renders the measured values for a note: "+12.3% from open,
// EMA slope +3.4%, gap +25.0%".
func PowerFilterValues(fromOpen, slope, gap float64) string {
	return psPct(fromOpen) + " from open, EMA slope " + psPct(slope) + ", gap " + psPct(gap)
}

func psPct(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "?"
	}
	s := strconv.FormatFloat(v, 'f', 1, 64)
	if v >= 0 {
		s = "+" + s
	}
	return s + "%"
}

// QUIET WINDOWS: Power Setup does not fire inside them (the user does not trade
// then, and the backtest was negative 05:00-07:00 ET). The power candle still
// alerts and its note/row say "skipped: <window>". Defaults: 05:00-07:00 ET and
// 16:00-20:00 ET (after hours), so it fires 04:00-05:00 and 07:00-16:00 ET.
// Set once at start-up (cmd/scanner, env SCANNER_PS_QUIET, "none" clears) before
// the engine runs; never changed while it runs.
type QuietWindow struct {
	StartMin, EndMin int // ET minute of day, [start, end)
	Name             string
}

var PsQuietWindows = []QuietWindow{
	{5 * 60, 7 * 60, "05–07 ET window"},
	{16 * 60, 20 * 60, "16–20 ET window"},
}

// PsQuietName names the quiet window containing the ET minute of day, or "".
// No allocation.
func PsQuietName(minuteOfDay int) string {
	for i := range PsQuietWindows {
		if w := &PsQuietWindows[i]; minuteOfDay >= w.StartMin && minuteOfDay < w.EndMin {
			return w.Name
		}
	}
	return ""
}

// ParseQuietWindows parses "05:00-07:00,11:00-20:00" ("none" or "" = no windows).
func ParseQuietWindows(s string) ([]QuietWindow, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "none") {
		return nil, true
	}
	var out []QuietWindow
	for _, part := range strings.Split(s, ",") {
		var h1, m1, h2, m2 int
		if _, err := fmt.Sscanf(strings.TrimSpace(part), "%d:%d-%d:%d", &h1, &m1, &h2, &m2); err != nil {
			return nil, false
		}
		a, b := h1*60+m1, h2*60+m2
		if a < 0 || b > 24*60 || a >= b {
			return nil, false
		}
		out = append(out, QuietWindow{a, b, fmt.Sprintf("%02d–%02d ET window", h1, h2)})
	}
	return out, true
}
