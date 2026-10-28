package engine

import (
	"math"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/volbreak"
)

// VOLUME BREAK, the engine side (the rule is internal/volbreak;
// docs/volume-break.md): a volume break of a consolidation high on a stock in
// play, then the first pullback to the 1-minute 9 EMA. Two ALERTS, nothing is
// ordered.
//
// judgeVolBreak runs inside the shared closed-bar judge (dildo.go judgeBar,
// step 5c) once per closed, wholly observed traded minute per ticker, after the
// judge EMA folded this bar. The rule's ring of bars (~4 KB) is allocated the
// first time a bar closes 10%+ over the previous close (the volume half of
// volbreak.InPlay is only required at the break) and reused from then on, so
// the thousands of quiet tickers pay nothing and a judged bar allocates
// nothing. Its range therefore forms from the bars after that first bar.
func (t *tickerState) judgeVolBreak(barMs int64, h, l, c float64, v int64) {
	d := &t.d1
	pc := float64(t.prevClose)
	inPlay := volbreak.InPlay(c, pc, t.sessVol)
	if d.vb == nil {
		if !volbreak.InPlay(c, pc, volbreak.MinSessVol) { // up 10%+: start watching before the volume arrives
			return
		}
		d.vb = new(volbreak.Judge)
	}
	ema := math.NaN()
	if d.emaN >= emaPeriod {
		ema = d.ema
	}
	j := d.vb
	switch j.Step(volbreak.Bar{Ms: barMs, H: h, L: l, C: c, V: v}, ema, inPlay) {
	case volbreak.EvBreak:
		t.sig.emit(core.SignalHit{Sym: t.sym, Kind: volbreak.KindBreak, BarMs: barMs, Price: c, C: c,
			Level: j.Level, VolX: j.VolX, Ema: j.BreakEma, DollarVol: c * float64(v)})
	case volbreak.EvEntry:
		t.sig.emit(core.SignalHit{Sym: t.sym, Kind: volbreak.KindEntry, BarMs: barMs, Price: j.EntryPx, C: c,
			Level: j.Level, VolX: j.VolX, Ema: j.EntryPx, ClimaxMs: j.BreakMs})
	}
}

// vbRow stamps today's latest volume break onto a row.
func (t *tickerState) vbRow(r *core.Row, nowMs, anchor int64) {
	j := t.d1.vb
	if j == nil || j.State == volbreak.StateNone || j.BreakMs < anchor {
		return
	}
	r.VbAge = float64(max(nowMs-j.BreakMs, 0) / 1000)
	r.VbLevel = finiteOr(j.Level)
	r.VbVolX = finiteOr(j.VolX)
	r.VbEma = finiteOr(j.BreakEma)
	r.VbState = float64(j.State)
	if j.State == volbreak.StateEntry {
		r.VbEntryAge = float64(max(nowMs-j.EntryMs, 0) / 1000)
		r.VbEntryPx = finiteOr(j.EntryPx)
	}
}
