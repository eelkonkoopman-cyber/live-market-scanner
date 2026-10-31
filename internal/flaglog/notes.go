package flaglog

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/bbs"
	"github.com/eelkonkoopman/marketscanner/internal/climax"
	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
	"github.com/eelkonkoopman/marketscanner/internal/ignition"
	"github.com/eelkonkoopman/marketscanner/internal/volbreak"
)

// The one-line notes of the dildo and climax rows: what the rule measured, in
// the chart notes' own words and number formats (dildoPb.ts / climax.ts:
// fmtPx, JS toFixed rounding, '∞' for a mean-of-0 multiple), minus the clock
// time and the signal's name, which the row already shows (BOTTOM / CLIMAX
// FAILED lead with the measure instead of the price the row shows). Formatted once per
// NEW row, never per evaluation.

// dildoNote is the chart rule's own note (dildopb.Event.Note) without its
// "HH:MM " prefix and the event's name: "+9.0% body (4.5x prior), 200x volume,
// $218k; floor 1.04" / "close 7.93 vs EMA 7.80 (+1.6%), 10 min after the
// dildo" / "low 1.10 broke the floor 1.15".
func dildoNote(e *dildopb.Event) string {
	n := e.Note()
	if len(n) > 6 && n[2] == ':' && n[5] == ' ' {
		n = n[6:]
	}
	if e.Kind == dildopb.KindDildo {
		return strings.TrimPrefix(n, "green dildo ")
	}
	if i := strings.Index(n, ": "); i >= 0 { // "pullback to the 9 EMA held: " / "pullback cancelled: "
		n = n[i+2:]
	}
	return n
}

// EarlyNotePrefix leads the note of a GREEN DILDO row whose dildo was an EARLY
// one (dildopb.JudgeEarly, the auto-trader's rule): the Signal window's history
// shows it as an EARLY badge (web/src/dildoEarly.ts splitEarlyNote).
const EarlyNotePrefix = "early; "

// FreshNotePrefix leads the note of an IGNITION row whose ignition was FRESH
// (day change before it <= +3%): the history shows a FRESH badge
// (web/src/ignitionFresh.ts splitFreshNote).
const FreshNotePrefix = "fresh; "

// dildoFloorOpen: the engine judges with the calibrated defaults, so its
// dildo's floor (the level the pullback must hold) follows their Floor rule.
var dildoFloorOpen = dildopb.DefaultParams().Floor == dildopb.FloorOpen

// climaxLookback is the climax rule's prior window (the "10-bar high").
var climaxLookback = climax.DefaultParams().Lookback

// signalText is the label and note of one engine alert; ok is false for a
// kind the history does not know.
func (r *Recorder) signalText(h core.SignalHit) (label, note string, ok bool) {
	switch h.Kind {
	case dildopb.KindDildo:
		floor := (h.O + h.C) / 2
		if dildoFloorOpen {
			floor = h.O
		}
		early := ""
		if h.Early == 1 {
			early = EarlyNotePrefix
		}
		return dildopb.LabelDildo, early + "+" + toFixed(h.BodyPct, 1) + "% body (" + toFixed(h.BodyX, 1) + "x prior), " +
			toFixed(h.VolX, 0) + "x volume, " + fmtDvol(h.DollarVol) + "; floor " + fmtPx(floor), true
	case dildopb.KindPower:
		return dildopb.LabelPower, "+" + toFixed(h.BodyPct, 1) + "% body (" + toFixed(h.BodyX, 1) + "x prior), " + toFixed(h.TopPct, 0) +
			"% top wick, " + toFixed(h.VolX, 0) + "x volume, " + fmtDvol(h.DollarVol) + "; " + core.PowerFilterTag(h.PsFromOpen, h.PsSlope, h.PsGap) + skipNote(h.PsSkip), true
	case core.KindPowerSetup:
		label, grade := core.LabelPowerSetup, ""
		if h.PsAplus {
			label, grade = core.LabelPowerSetup+" "+core.GradeAplus, core.GradeAplus+" tight base "+toFixed(h.PsBaseRange, 1)+"%; "
		}
		return label, grade + "entry " + fmtPx(h.PsEntry) + " stop " + fmtPx(h.PsStop) + " target " + fmtPx(h.PsTarget) + "; " +
			core.PowerFilterTag(h.PsFromOpen, h.PsSlope, h.PsGap) + " (" + core.PowerFilterValues(h.PsFromOpen, h.PsSlope, h.PsGap) + "), " +
			toFixed(h.BodyPct, 1) + "% body, " + fmtDvol(h.DollarVol), true
	case climax.KindClimax:
		return climax.LabelClimax, toFixed(h.DropPct, 1) + "% under the " + strconv.Itoa(climaxLookback) + "-bar high, " +
			toFixed(h.VolX, 1) + "x volume, " + toFixed(h.WickPct, 0) + "% lower wick, " + fmtDvol(h.DollarVol) +
			"; bottom on a close above " + fmtPx(h.Mid), true
	case climax.KindBottom:
		risk := (h.Price - h.Low) / h.Price * 100
		// The measure first (the row's price column already shows the close,
		// and the list cuts the note to its first ~40 characters).
		return climax.LabelBottom, "climax low " + fmtPx(h.Low) + " is " + toFixed(risk, 1) + "% under, " +
			plural(h.Bars, "bar") + " after the " + r.clock(h.ClimaxMs) + " climax; midpoint " + fmtPx(h.Mid), true
	case climax.KindFail:
		return climax.LabelFail, "undercut the " + r.clock(h.ClimaxMs) + " climax low " + fmtPx(h.Low), true // the row's price is the low
	case bbs.KindBbs:
		return bbs.LabelBbs, "support " + fmtPx(h.Support) + " (" + strconv.Itoa(h.Touches) + " touches), lower highs " +
			fmtPx(h.FirstHigh) + " → " + fmtPx(h.LastHigh) + " over " + plural(h.Highs, "bounce") + ", " + fmtDvol(h.DollarVol), true
	case ignition.KindIgnition:
		fresh := ""
		if h.Skip != "" {
			fresh = h.Skip + "; " // a skipped ignition (engine ignSkip): dimmed, no alert
		} else if h.Fresh == 1 {
			fresh = FreshNotePrefix
		}
		return ignition.LabelIgnition, fresh + "+" + toFixed(h.BodyPct, 1) + "% candle on " + toFixed(h.VolX, 1) + "x volume out of a " +
			toFixed(h.BaseRangePct, 1) + "% base, " + fmtDvol(h.DollarVol) + "; base high " + fmtPx(h.BaseHigh), true
	case volbreak.KindBreak:
		return volbreak.LabelBreak, "volume break " + fmtPx(h.Level) + " (" + toFixed(h.VolX, 1) + "x vol), 9EMA " + fmtPxOrQ(h.Ema) + ", " + fmtDvol(h.DollarVol), true
	case volbreak.KindEntry:
		return volbreak.LabelEntry, "vol break 9EMA entry " + fmtPx(h.Price) + ", level " + fmtPx(h.Level) + "; broke at " + r.clock(h.ClimaxMs), true
	case core.KindEmaVwapEarlyUp, core.KindEmaVwapEarlyDn:
		label = core.LabelEmaVwapEarlyUp
		dir := "above"
		if h.Kind == core.KindEmaVwapEarlyDn {
			label, dir = core.LabelEmaVwapEarlyDn, "under"
		}
		return label, "price " + fmtPx(h.Price) + " " + dir + " the VWAP " + fmtPx(h.Vwap) + ", 9 EMA " + toFixed(math.Abs(h.Dist), 2) + "% from crossing", true
	case core.KindEmaVwapBull, core.KindEmaVwapBear:
		label = core.LabelEmaVwapBull
		dir := "above"
		if h.Kind == core.KindEmaVwapBear {
			label, dir = core.LabelEmaVwapBear, "under"
		}
		return label, "1m 9 EMA " + fmtPx(h.Ema) + " crossed " + dir + " the VWAP " + fmtPx(h.Vwap) + " (" + toFixed(math.Abs(h.Dist), 2) + "% gap)", true
	}
	return "", "", false
}

// clock is the ET wall clock of ms as HH:MM.
func (r *Recorder) clock(ms int64) string { return time.UnixMilli(ms).In(r.loc).Format("15:04") }

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// fmtPx is the chart notes' price: 4 decimals under $1, else 2.
func fmtPx(v float64) string {
	if math.Abs(v) < 1 {
		return toFixed(v, 4)
	}
	return toFixed(v, 2)
}

// fmtPxOrQ is fmtPx, or "?" for an unknown (non-finite or non-positive) price.
func fmtPxOrQ(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return "?"
	}
	return fmtPx(v)
}

// fmtDvol is '$717k' under $1M, '$1.2M' from there.
func fmtDvol(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "$?"
	}
	if v < 1e6 {
		return "$" + toFixed(v/1e3, 0) + "k"
	}
	return "$" + toFixed(v/1e6, 1) + "M"
}

// toFixed is JS Number.prototype.toFixed(d) for a finite x - the exact binary
// value rounded half AWAY from zero (strconv rounds an exact tie to even:
// 6.25 -> "6.2", JS "6.3") - and '∞' for a non-finite one (a mean-of-0
// multiple).
func toFixed(x float64, d int) string {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return "∞"
	}
	neg := x < 0
	if neg {
		x = -x
	}
	q := new(big.Rat).SetFloat64(x)
	q.Mul(q, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(d)), nil)))
	q.Add(q, big.NewRat(1, 2))
	s := new(big.Int).Quo(q.Num(), q.Denom()).String() // floor: both positive
	if d > 0 {
		if len(s) <= d {
			s = strings.Repeat("0", d-len(s)+1) + s
		}
		s = s[:len(s)-d] + "." + s[len(s)-d:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

// skipNote is "; skipped: <window>" for a power candle whose setup filters passed inside a quiet window.
func skipNote(w string) string {
	if w == "" {
		return ""
	}
	return "; skipped: " + w
}

// LatestPowerSetup reads sym's latest P-SETUP recorded today: whether it was
// graded A+ ("P-SETUP A+"), its base range % (parsed from the note's "A+ tight
// base 3.8%" prefix; 0 when not A+) and its bar time. ok = a setup today.
// The order ticket sizes an A+ setup at core.PsAplusRiskMult x the risk.
func (r *Recorder) LatestPowerSetup(sym string) (aplus bool, baseRange float64, barMs int64, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rollTo(r.now().UnixMilli())
	for i := len(r.events) - 1; i >= 0; i-- {
		e := &r.events[i]
		if e.Sym != sym || e.Kind != core.KindPowerSetup {
			continue
		}
		if e.Label == core.LabelPowerSetup+" "+core.GradeAplus {
			aplus = true
			if s, found := strings.CutPrefix(e.Note, core.GradeAplus+" tight base "); found {
				if j := strings.IndexByte(s, '%'); j > 0 {
					baseRange, _ = strconv.ParseFloat(s[:j], 64)
				}
			}
		}
		return aplus, baseRange, e.Ts, true
	}
	return false, 0, 0, false
}

// LatestVwapBull reads sym's newest 9EMA VWAP bull cross recorded today: the
// firing 1-minute bar's START ms (FlagEvent.Ts = SignalHit.BarMs). Read-only;
// the order ticket's "VWAP X" preset uses it. ok = a bull cross today.
func (r *Recorder) LatestVwapBull(sym string) (barMs int64, ok bool) {
	return r.latestVwapCross(sym, core.KindEmaVwapBull)
}

// LatestVwapBear is LatestVwapBull for the bear (short) cross: the order
// ticket's "VWAP X down" preset.
func (r *Recorder) LatestVwapBear(sym string) (barMs int64, ok bool) {
	return r.latestVwapCross(sym, core.KindEmaVwapBear)
}

func (r *Recorder) latestVwapCross(sym, kind string) (barMs int64, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rollTo(r.now().UnixMilli())
	for i := range r.events {
		e := &r.events[i]
		if e.Sym != sym || e.Kind != kind {
			continue
		}
		if !ok || e.Ts > barMs {
			barMs, ok = e.Ts, true
		}
	}
	return barMs, ok
}
