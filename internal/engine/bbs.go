package engine

import (
	"github.com/eelkonkoopman/marketscanner/internal/bbs"
	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// BOUNCY BALL SHORT, the engine side (the rule is internal/bbs, a port of
// web/src/signals/bouncyShort.ts, golden-pinned). An ALERT, a "look now"
// prompt, not a system (uncalibrated).
//
// judgeBbs runs inside the shared closed-bar judge (dildo.go judgeBar, step
// 5b) once per closed traded minute per ticker. It keeps today's last
// bbsLookback judged traded bars' highs and lows (after the day's latest
// break) in a shift buffer, so bbs.Analyze reads them as plain slices - the
// same windows bbs.Evaluate builds:
//   - break: Analyze(the window before this bar) OK and close < S*(1-0.3 %)
//     -> alert ("SYM: bouncy ball short"), signal history kind "bbs"; the
//     window clears (the next base starts after the break);
//   - else the bar joins the window and the row is FORMING when
//     Analyze(window incl. it) is OK and the close is within 1 % of S.
// The ET day change and a part-observed minute clear the window. The buffer is
// allocated once per ticker on its first judged bar; nothing allocates after.
// Cost O(90) per closed bar (BenchmarkJudgeBbs).

// bbsLookback is bbs.DefaultParams().Lookback (a test pins it).
const bbsLookback = 90

var bbsParams = bbs.DefaultParams()

type bbsWin struct {
	hs, ls [bbsLookback]float64
	n      int
}

// bbsState is a ticker's bouncy ball short judge state (dildo1m.bb).
type bbsState struct {
	w       *bbsWin
	breakMs int64   // today's latest break bar start; 0 = none
	sup     float64 // its support
	dvol    float64 // its close x volume
	formMs  int64   // the bar that was judged FORMING (0 = the latest bar was not)
	formSup float64
}

// dayReset clears the window (a new ET day, or a gap in what was seen). The
// break latch stays: the row's "today" anchor hides yesterday's.
func (b *bbsState) dayReset() {
	if b.w != nil {
		b.w.n = 0
	}
	b.formMs = 0
}

func (t *tickerState) judgeBbs(barMs int64, h, l, c float64, v int64) {
	b := &t.d1.bb
	if b.w == nil {
		b.w = new(bbsWin)
	}
	w := b.w
	if n := w.n; n > 0 && c < w.ls[n-1]*(1-bbsParams.BreakPct/100) { // exact necessary: S <= the newest low
		if pt := bbs.Analyze(w.hs[:n], w.ls[:n], bbsParams); bbs.Breaks(pt, c, bbsParams) {
			b.breakMs, b.sup, b.dvol, b.formMs = barMs, pt.Support, c*float64(v), 0
			w.n = 0
			t.sig.emit(core.SignalHit{Sym: t.sym, Kind: bbs.KindBbs, BarMs: barMs, Price: c, C: c, DollarVol: c * float64(v),
				Support: pt.Support, Touches: pt.Touches, Highs: pt.Highs, FirstHigh: pt.FirstHigh, LastHigh: pt.LastHigh})
			return
		}
	}
	if w.n == bbsLookback {
		copy(w.hs[:], w.hs[1:])
		copy(w.ls[:], w.ls[1:])
		w.n--
	}
	w.hs[w.n], w.ls[w.n] = h, l
	w.n++
	b.formMs = 0
	if pt := bbs.Analyze(w.hs[:w.n], w.ls[:w.n], bbsParams); bbs.Forming(pt, c, bbsParams) {
		b.formMs, b.formSup = barMs, pt.Support
	}
}

// bbsRow stamps today's bouncy ball short onto a row (state 1 forming wins
// over 2 broken, as in bbs.Evaluate).
func (t *tickerState) bbsRow(r *core.Row, nowMs, anchor int64) {
	b := &t.d1.bb
	broke := b.breakMs > 0 && b.breakMs >= anchor
	if broke {
		r.BbsAge = float64(max(nowMs-b.breakMs, 0) / 1000)
		r.BbsDvol = finiteOr(b.dvol)
	}
	switch {
	case b.formMs > 0 && b.formMs >= anchor:
		r.BbsState, r.BbsSupport = bbs.StateForming, finiteOr(b.formSup)
		if s := r.BbsSupport; !core.IsNull(s) && s > 0 && r.Last > 0 {
			r.BbsDistPct = finiteOr((r.Last - s) / s * 100)
		}
	case broke:
		r.BbsState, r.BbsSupport = bbs.StateBroken, finiteOr(b.sup)
	}
}
