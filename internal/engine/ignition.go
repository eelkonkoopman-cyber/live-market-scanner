package engine

import (
	"strconv"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/ignition"
)

// IGNITION, the engine side (the rule is internal/ignition, a port of
// web/src/signals/ignition.ts, golden-pinned): the small first candle out of a
// quiet base right before a dildo. An ALERT, a "look now" prompt, not a
// system (uncalibrated).
//
// judgeIgnition runs inside the shared closed-bar judge (dildo.go judgeBar,
// step 3b) once per closed, wholly observed traded minute per ticker, BEFORE
// the bar joins the rings: the base is the last ignBaseBars bars of the early
// ring (eHi/eLo), the volume mean the dildo's ring (dildoSums), the day high
// the dildo early judge's (dayHighBefore). The base is copied into stack
// arrays, so nothing allocates (BenchmarkJudgeIgnition). An ignition alerts
// "SYM: ignition" (signal history kind "ignition"); a FRESH one (freshMaxDayChgPct)
// "SYM: fresh ignition".

// ignBaseBars / ignVolBars are the rule's defaults the rings must cover (a test pins them).
const (
	ignBaseBars = 15
	ignVolBars  = judgeRing
)

var ignParams = ignition.DefaultParams()

// freshMaxDayChgPct: an ignition is FRESH when the day change before it - the
// ignition bar's open vs the previous trading day's close - is at most this.
const freshMaxDayChgPct = 3.0

// ignFresh is the fresh verdict of an ignition bar opening at o: 1 fresh,
// 0 not, -1 unknown (no previous close).
func ignFresh(o, prevClose float64) int8 {
	if !(prevClose > 0) || !(o > 0) {
		return -1
	}
	if (o/prevClose-1)*100 <= freshMaxDayChgPct+1e-9 { // epsilon: float noise at exactly +3%
		return 1
	}
	return 0
}

// The ignition SKIP rules (2026-10-14): an ignition hitting any of them is
// still recorded in the signal history (dimmed, "skip: <reason>") but does not
// alert - the seeded Ignition scan requires ignPass == 1.
const (
	ignSkipMaxDayChgPct = freshMaxDayChgPct // S1: day change before it above this (or unknown) skips
	ignSkipMaxDvol      = 250_000.0         // S2: the candle's $ volume (close x volume) above this skips
	ignSkipMinPrice     = 1.60              // S3: a close under this skips
	ignSkipMaxVolX      = 50.0              // S4: a volume multiple (vs the prior 10 bars) above this skips
)

// ignSkip is why an ignition bar fails the skip rules ("" = it passes): o / c
// its open and close, dvol close x volume, volX its volume multiple. The first
// rule hit (S1..S4 order) names the reason.
func ignSkip(o, c, dvol, volX, prevClose float64) string {
	switch {
	case ignFresh(o, prevClose) == -1:
		return "skip: day change unknown"
	case ignFresh(o, prevClose) == 0:
		return "skip: up " + strconv.FormatFloat((o/prevClose-1)*100, 'f', 1, 64) + "% on the day"
	case dvol > ignSkipMaxDvol:
		return "skip: $" + strconv.FormatFloat(dvol/1e3, 'f', 0, 64) + "k candle over $250k"
	case c < ignSkipMinPrice:
		return "skip: price " + strconv.FormatFloat(c, 'f', 2, 64) + " under $1.60"
	case volX > ignSkipMaxVolX:
		return "skip: " + strconv.FormatFloat(volX, 'f', 0, 64) + "x volume over 50x"
	}
	return ""
}

// ignState is a ticker's ignition judge state (dildo1m.ig).
type ignState struct {
	latch         ignition.Latch
	newDay        bool // the next judged bar is the first of a new ET day
	bodyPct, volX float64
	dvol          float64
	fresh         int8
	pass          int8 // 1 passes the skip rules, 0 skipped
}

func (t *tickerState) judgeIgnition(barMs int64, o, h, c float64, v int64, dayHi float64) {
	d := &t.d1
	g := &d.ig
	var hs, ls [ignBaseBars]float64
	n := min(d.eN, ignBaseBars)
	i := d.eHead - n
	if i < 0 {
		i += earlyRing
	}
	for k := 0; k < n; k++ {
		hs[k], ls[k] = d.eHi[i], d.eLo[i]
		if i++; i == earlyRing {
			i = 0
		}
	}
	_, sumVol := d.dildoSums()
	vd := ignition.Judge(hs[:n], ls[:n], sumVol, d.n, o, h, c, v, dayHi, ignParams)
	newDay := g.newDay
	g.newDay = false
	if !g.latch.Step(barMs, vd, newDay, ignParams) {
		return
	}
	fresh := ignFresh(o, float64(t.prevClose))
	skip := ignSkip(o, c, c*float64(v), vd.VolX, float64(t.prevClose))
	g.bodyPct, g.volX, g.dvol, g.fresh = vd.BodyPct, vd.VolX, vd.Dvol, fresh
	g.pass = 1
	if skip != "" {
		g.pass = 0
	}
	t.sig.emit(core.SignalHit{Sym: t.sym, Kind: ignition.KindIgnition, BarMs: barMs, Price: c, O: o, C: c,
		BodyPct: vd.BodyPct, VolX: vd.VolX, DollarVol: vd.Dvol, BaseRangePct: vd.RangePct, BaseHigh: vd.BaseHigh, Fresh: fresh, Skip: skip})
}

// ignRow stamps today's latest ignition onto a row.
func (t *tickerState) ignRow(r *core.Row, nowMs, anchor int64) {
	g := &t.d1.ig
	if ms := g.latch.LastMs; ms > 0 && ms >= anchor {
		r.IgnAge = float64(max(nowMs-ms, 0) / 1000)
		r.IgnBodyPct = finiteOr(g.bodyPct)
		r.IgnVolX = finiteOr(g.volX)
		r.IgnFresh = earlyField(g.fresh)
		r.IgnPass = float64(g.pass)
	}
}
