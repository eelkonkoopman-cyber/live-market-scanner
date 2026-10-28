package engine

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
)

func newPowerTS() (*tickerState, *signals) {
	ts, sg := newSigTS(true, false)
	sg.events = make(chan core.SignalHit, 64)
	return ts, sg
}

func kinds(hs []core.SignalHit) (dildo, power int) {
	for _, h := range hs {
		switch h.Kind {
		case dildopb.KindDildo:
			dildo++
		case dildopb.KindPower:
			power++
		}
	}
	return
}

// A +10 % body with a 67 % top wick on 200x volume.
func powerBar(t int64) core.Bar {
	return core.Bar{T: t, O: 1.00, H: 1.30, L: 1.00, C: 1.10, V: 200_000}
}

func runBar(b core.Bar, mut func([]core.Bar)) (dildo, power int, ts *tickerState) {
	ts, sg := newPowerTS()
	start := etMs(10, 0)
	pri := flatBars1m(start, 10, 1.00)
	if mut != nil {
		mut(pri)
	}
	for _, p := range pri {
		feed1m(ts, p)
	}
	b.T = start + 10*m1Ms
	feed1m(ts, b)
	d, p := kinds(drainSignals(sg))
	return d, p, ts
}

func TestPowerCandleFiresNotDildo(t *testing.T) {
	d, p, ts := runBar(powerBar(0), nil)
	if d != 0 || p != 1 {
		t.Fatalf("dildo %d power %d", d, p)
	}
	r := sigRow(ts, etMs(10, 11)+5_000)
	if r.PcAge != 65 || r.PcTopPct < 66 || r.PcTopPct > 67 || r.PcBodyPct < 9.99 || !(r.PcDvol > 200_000) || !core.IsNull(r.DildoAge) {
		t.Fatalf("row %+v", r)
	}
}

func TestRealDildoFiresDildoNotPower(t *testing.T) {
	d, p, ts := runBar(dildoBar(0), nil)
	if d != 1 || p != 0 {
		t.Fatalf("dildo %d power %d", d, p)
	}
	if r := sigRow(ts, etMs(10, 11)+5_000); !core.IsNull(r.PcAge) {
		t.Fatal("pc latch set by a dildo")
	}
}

func TestPowerCandleWHLR(t *testing.T) {
	ts, sg := newPowerTS()
	start := etMs(4, 0)
	// quiet prior traded bars (the previous day, flat)
	for i := 0; i < 4; i++ {
		feed1m(ts, core.Bar{T: start - 86_400_000 + int64(i)*m1Ms, O: 0.4288, H: 0.4288, L: 0.4288, C: 0.4288, V: 100})
	}
	feed1m(ts, core.Bar{T: start, O: 0.4288, H: 0.6484, L: 0.4288, C: 0.5695, V: 445906})
	if d, p := kinds(drainSignals(sg)); d != 0 || p != 1 {
		t.Fatalf("dildo %d power %d", d, p)
	}
}

func TestPowerCandleNeedsHOD(t *testing.T) {
	d, p, _ := runBar(powerBar(0), func(b []core.Bar) { b[2].H = 1.40 }) // an earlier high above the wick candle's
	if d != 0 || p != 0 {
		t.Fatalf("dildo %d power %d", d, p)
	}
}

func TestPowerCandleFollowsLiveParams(t *testing.T) {
	t.Cleanup(dildopb.ResetLive)
	p := dildopb.DefaultParams()
	p.MinBodyPct = 12 // the candle is +10 %
	dildopb.SetLive(p)
	if d, pw, _ := runBar(powerBar(0), nil); d != 0 || pw != 0 {
		t.Fatalf("minBodyPct 12: dildo %d power %d", d, pw)
	}
	p = dildopb.DefaultParams()
	p.Enabled = false
	dildopb.SetLive(p)
	if d, pw, _ := runBar(powerBar(0), nil); d != 0 || pw != 0 {
		t.Fatal("disabled must silence power")
	}
	p = dildopb.DefaultParams()
	p.MaxTopPct = 90 // wick 67 % is now within a dildo's limit: a dildo, not power
	dildopb.SetLive(p)
	if d, pw, _ := runBar(powerBar(0), nil); d != 1 || pw != 0 {
		t.Fatalf("maxTopPct 90: dildo %d power %d", d, pw)
	}
}
