package engine

import (
	"math"

	"github.com/eelkonkoopman/marketscanner/internal/climax"
	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// VOLUME CLIMAX -> BOTTOM CONFIRMED, the engine side (both alerts; the rule is
// internal/climax, a port of web/src/signals/climax.ts, golden-pinned). An
// ALERT, a "look now" prompt, not a system: calibration found no edge after
// costs (the confirmation's +15-minute median was -0.73 % net), only that a
// sell-off low on climactic volume holds twice as often as one without it.
//
// judgeClimax runs inside the shared closed-bar judge (dildo.go judgeBar, step
// 5), once per closed traded minute per ticker, after the ET day change, the
// judge-EMA fold and the partial-minute check:
//
//   - a watching latch first takes this bar (climax.Latch.Step - the SAME code
//     climax.Evaluate runs, so the golden vectors test this logic): a new low
//     under the climax low FAILS it, else a close above the climax bar's
//     midpoint CONFIRMS it (alert 2, "SYM: bottom confirmed"), else after the
//     5th bar it expires;
//   - then, with at least 10 bars of TODAY behind it, the bar itself is
//     classified against exactly the last 10 (Priors rebuilt from the ring
//     oldest -> newest, the chart's construction), the judge's own 9 EMA (NaN
//     until it has 9 closes) and the bar's ET minute (the 09:30-09:34 mute); a
//     hit arms the latch (alert 1, "SYM: volume climax"), replacing whatever it
//     held ("latest supersedes", as in Evaluate).
//
// One bar can do both: a retest of the EXACT climax low (not under it, so no
// fail; equal to the priors' low, so a fresh low) on at least the climax
// bar's volume that closes over the old midpoint confirms the old climax AND
// is a new one - the chart marks BOTTOM and CLIMAX on it. The latch re-arms,
// as Evaluate's does, but the confirmation it replaced is kept (clConf) so the
// row still shows the bottom (climaxState 2, its age and close) until the next
// bar is judged: otherwise "Bottom Confirmed" could never fire for the first
// climax.
//
// The judge EMA is used rather than ticker.ema9: that tracker can fold the NEXT
// minute before the judge sees this one (on the clock-skew path), and the judge
// must read the EMA of THIS bar. A test pins the two together on the normal
// path. Allocates nothing; O(10).
func (t *tickerState) judgeClimax(barMs int64, o, h, l, c float64, v int64, off int64) {
	d := &t.d1
	if d.cl.State == climax.StateWatching {
		// The verdict is handed to the signal history (Engine.SignalHits) in the
		// chart's vocabulary: BOTTOM at the confirming close, CLIMAX FAILED at the
		// undercutting low; an expiry is silent. Step leaves BarMs / Low / Hit.
		switch d.cl.Step(barMs, l, c, climaxParams) {
		case climax.StateConfirmed:
			t.sig.emit(core.SignalHit{Sym: t.sym, Kind: climax.KindBottom, BarMs: barMs, Price: c,
				Mid: d.cl.Hit.Mid, Low: d.cl.Low, ClimaxMs: d.cl.BarMs, Bars: d.cl.Bars})
		case climax.StateFailed:
			t.sig.emit(core.SignalHit{Sym: t.sym, Kind: climax.KindFail, BarMs: barMs, Price: l,
				Mid: d.cl.Hit.Mid, Low: d.cl.Low, ClimaxMs: d.cl.BarMs, Bars: d.cl.Bars})
		}
	}
	if int(d.todayN) < climaxParams.Lookback || d.n < climaxParams.Lookback {
		return
	}
	ema := math.NaN()
	if d.emaN >= emaPeriod {
		ema = d.ema
	}
	if hit, ok := climax.Classify(o, h, l, c, v, d.climaxPriors(climaxParams.Lookback), ema, climax.EtMinute(barMs, off), climaxParams); ok {
		if d.cl.State == climax.StateConfirmed && d.cl.EndMs == barMs {
			d.clConf = climaxConf{endMs: barMs, px: d.cl.ConfPx} // confirmed by THIS bar: keep it for the row
		}
		d.cl.Arm(barMs, l, hit)
		t.sig.emit(core.SignalHit{Sym: t.sym, Kind: climax.KindClimax, BarMs: barMs, Price: l,
			VolX: hit.VolX, DollarVol: hit.DollarVol, DropPct: hit.DropPct, WickPct: hit.WickPct, Mid: hit.Mid, Low: l})
	}
}

// climaxConf is a confirmation that a new climax on the SAME bar re-armed
// over: the confirming bar's start and close. climaxRow shows it while the new
// latch has taken no bar yet (cl.Bars == 0 and cl.BarMs == endMs); any other
// value is stale and ignored, so it is never cleared.
type climaxConf struct {
	endMs int64
	px    float64
}

// climaxPriors builds climax.Priors over the newest n ring bars (n <= the
// ring's count), oldest -> newest exactly as climax.PriorsOf does over the
// chart's bars: Hi = max high, Lo = min low, SumVol += float64(v), MaxVol =
// max v.
func (d *dildo1m) climaxPriors(n int) climax.Priors {
	pr := climax.Priors{N: n, Hi: math.Inf(-1), Lo: math.Inf(1)}
	i := d.head - n
	if i < 0 {
		i += judgeRing
	}
	for k := 0; k < n; k++ {
		p := &d.ring[i]
		if p.h > pr.Hi {
			pr.Hi = p.h
		}
		if p.l < pr.Lo {
			pr.Lo = p.l
		}
		pr.SumVol += float64(p.vol)
		if p.vol > pr.MaxVol {
			pr.MaxVol = p.vol
		}
		if i++; i == judgeRing {
			i = 0
		}
	}
	return pr
}

// climaxRow stamps the latest climax of TODAY (its bar started at or after
// anchor, today's 04:00 ET) onto a row: its age from the bar's START, its
// measures, the low that must hold, the latch state, and - only once the bottom
// is confirmed - the confirming bar's age and close. A +Inf volume multiple (a
// prior mean of 0) is Null.
func (t *tickerState) climaxRow(r *core.Row, nowMs, anchor int64) {
	x := &t.d1.cl
	if x.State == climax.StateNone || x.BarMs <= 0 || x.BarMs < anchor {
		return
	}
	r.ClimaxAge = float64(max(nowMs-x.BarMs, 0) / 1000)
	r.ClimaxDropPct = finiteOr(x.Hit.DropPct)
	r.ClimaxVolX = finiteOr(x.Hit.VolX)
	r.ClimaxWickPct = finiteOr(x.Hit.WickPct)
	r.ClimaxDvol = finiteOr(x.Hit.DollarVol)
	r.ClimaxLow = finiteOr(x.Low)
	r.ClimaxState = float64(x.State)
	switch {
	case x.State == climax.StateConfirmed:
		r.ClimaxConfAge = float64(max(nowMs-x.EndMs, 0) / 1000)
		r.ClimaxConfPx = finiteOr(x.ConfPx)
	case x.State == climax.StateWatching && x.Bars == 0 && t.d1.clConf.endMs == x.BarMs:
		// This bar confirmed the previous climax and is itself a new one (see
		// judgeClimax): the bottom shows until the next bar is judged.
		r.ClimaxState = float64(climax.StateConfirmed)
		r.ClimaxConfAge = float64(max(nowMs-x.BarMs, 0) / 1000)
		r.ClimaxConfPx = finiteOr(t.d1.clConf.px)
	}
}
