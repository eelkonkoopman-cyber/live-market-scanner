package engine

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/volbreak"
)

// vbBase: 40 one-minute bars from 10:00 ET in a 1.16-1.20 range (the high
// tested at 10:05 and 10:25), 40k shares each, on a stock whose previous close
// was 1.00 (up ~18%, 1.84M shares by the break).
func vbBase(ts *tickerState) {
	for i := 0; i < 40; i++ {
		h, l := 1.19, 1.16
		if i == 5 || i == 25 {
			h, l = 1.20, 1.17
		}
		feed1m(ts, core.Bar{T: etMs(10, i), O: l + 0.01, H: h, L: l, C: h - 0.01, V: 40_000})
	}
}

func TestVolBreakEngine(t *testing.T) {
	ts, sg := newSigTS(false, false)
	sg.vb = true
	sg.events = make(chan core.SignalHit, 64)
	ts.prevClose = 1.0
	ts.sessVol = 400_000 // 1M+ by the break (each bar adds its volume)
	vbBase(ts)
	if hs := drainSignals(sg); len(hs) != 0 {
		t.Fatalf("signals in the base: %+v", hs)
	}
	// 10:40 breaks 1.20 on 6x volume
	feed1m(ts, core.Bar{T: etMs(10, 40), O: 1.19, H: 1.26, L: 1.19, C: 1.25, V: 240_000})
	hs := drainSignals(sg)
	if len(hs) != 1 || hs[0].Kind != volbreak.KindBreak || hs[0].Level != 1.20 || hs[0].VolX < 5.99 || hs[0].VolX > 6.01 || !(hs[0].Ema > 0) {
		t.Fatalf("break %+v", hs)
	}
	r := vbNullRow()
	ts.signalRow(&r, etMs(10, 42), etOff(etMs(10, 42)))
	if r.VbAge != 120 || r.VbLevel != 1.20 || r.VbState != 1 || !core.IsNull(r.VbEntryAge) {
		t.Fatalf("row %+v", r)
	}
	// 10:41 pulls back into the 9 EMA, closing over 1.20
	ema := ts.d1.ema
	feed1m(ts, core.Bar{T: etMs(10, 41), O: 1.24, H: 1.25, L: ema * 0.99, C: 1.22, V: 50_000})
	hs = drainSignals(sg)
	if len(hs) != 1 || hs[0].Kind != volbreak.KindEntry || hs[0].Level != 1.20 || hs[0].Price != ts.d1.ema {
		t.Fatalf("entry %+v", hs)
	}
	r = vbNullRow()
	ts.signalRow(&r, etMs(10, 43), etOff(etMs(10, 43)))
	if r.VbState != 2 || r.VbEntryAge != 120 || r.VbEntryPx != hs[0].Price {
		t.Fatalf("row %+v", r)
	}
}

// A stock not in play (up < 10%) never allocates the judge or alerts.
func TestVolBreakNotInPlay(t *testing.T) {
	ts, sg := newSigTS(false, false)
	sg.vb = true
	sg.events = make(chan core.SignalHit, 64)
	ts.prevClose = 1.15
	vbBase(ts)
	feed1m(ts, core.Bar{T: etMs(10, 40), O: 1.19, H: 1.26, L: 1.19, C: 1.25, V: 160_000})
	if hs := drainSignals(sg); len(hs) != 0 || ts.d1.vb != nil {
		t.Fatalf("not in play: %+v %v", hs, ts.d1.vb != nil)
	}
}

func vbNullRow() core.Row {
	n := core.Null
	return core.Row{VbAge: n, VbLevel: n, VbVolX: n, VbEma: n, VbState: n, VbEntryAge: n, VbEntryPx: n}
}
