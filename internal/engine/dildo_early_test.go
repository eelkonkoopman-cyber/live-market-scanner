package engine

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
)

func TestEarlyRingIsTheDefaultBase(t *testing.T) {
	if earlyRing != dildopb.DefaultEarlyParams().BaseBars {
		t.Fatalf("earlyRing %d != the early judge's base %d", earlyRing, dildopb.DefaultEarlyParams().BaseBars)
	}
}

func TestJudgeEarlyOnTheEngineRing(t *testing.T) {
	var d dildo1m
	d.day = 20000
	if got := d.judgeEarly(1.2, 1.2, 0); got != -1 {
		t.Fatalf("no bars: %d, want -1 (unknown)", got)
	}
	for i := 0; i < 9; i++ {
		d.push(1, 1.02, 0.99, 1, 100)
	}
	if got := d.judgeEarly(1.2, 1.2, 0); got != -1 {
		t.Fatalf("9 bars: %d, want -1", got)
	}
	d.push(1, 1.02, 0.99, 1, 100)
	if got := d.judgeEarly(1.2, 1.2, 0); got != 1 {
		t.Fatalf("10 quiet bars, breakout: %d, want 1", got)
	}
	if got := d.judgeEarly(1.01, 1.01, 0); got != 0 {
		t.Fatalf("no breakout: %d, want 0", got)
	}
	// A wide bar, then 30 quiet ones push it out of the ring.
	d.push(1, 1.5, 0.99, 1, 100)
	if got := d.judgeEarly(1.6, 1.6, 0); got != 0 {
		t.Fatalf("wide base: %d", got)
	}
	for i := 0; i < earlyRing; i++ {
		d.push(1, 1.02, 0.99, 1, 100)
	}
	if got := d.judgeEarly(1.2, 1.2, 0); got != 1 {
		t.Fatalf("wide bar out of the ring: %d", got)
	}
	// An earlier dildo today: not early; yesterday's does not count.
	d.latch.barMs, d.dildoDay = 1, d.day
	if got := d.judgeEarly(1.2, 1.2, 0); got != 0 {
		t.Fatalf("second dildo today: %d", got)
	}
	d.dildoDay = d.day - 1
	if got := d.judgeEarly(1.2, 1.2, 0); got != 1 {
		t.Fatalf("yesterday's dildo: %d", got)
	}
}

func TestEarlyField(t *testing.T) {
	if earlyField(1) != 1 || earlyField(0) != 0 || !core.IsNull(earlyField(-1)) {
		t.Fatal("earlyField")
	}
}

// The new-high-of-day rule (2026-10-14) on the engine's alert path, and the
// engine agreeing with the chart-side judge (dildopb.EarlyFromBars) the trader shares.
func TestEngineEarlyNewHighOfDay(t *testing.T) {
	run := func(pre []core.Bar, seedHod float64) (int8, bool) {
		ts, _ := newSigTS(true, false)
		if seedHod > 0 {
			ts.hod, ts.hodSet = float32(seedHod), true
		}
		bars := append([]core.Bar(nil), pre...)
		bars = append(bars, flatBars1m(etMs(5, 0), 30, 1.00)...)
		d := dildoBar(etMs(5, 30))
		bars = append(bars, d)
		for _, b := range bars {
			if b.T == etMs(5, 0) && len(pre) > 0 && pre[0].T < etMs(0, 0) {
				ts.resetSession() // the 04:00 session rollover (Engine's clock) clears yesterday's hod
			}
			feed1m(ts, b)
		}
		if ts.d1.latch.barMs != d.T {
			// not a HOD: nothing latched, nothing alerted (early would be 0 too)
			return 0, dildopb.EarlyFromBars(bars, d.T, 0, false, dildopb.DefaultEarlyParams()).Early()
		}
		return ts.d1.latch.early, dildopb.EarlyFromBars(bars, d.T, 0, false, dildopb.DefaultEarlyParams()).Early()
	}
	cases := []struct {
		name string
		pre  []core.Bar
		seed float64
		want int8
	}{
		{"new HOD", []core.Bar{{T: etMs(4, 30), O: 1, H: 1.05, L: 0.99, C: 1, V: 1000}}, 0, 1},
		{"below an earlier pre-market high", []core.Bar{{T: etMs(4, 30), O: 1, H: 1.20, L: 0.99, C: 1, V: 1000}}, 0, 0},
		{"previous day's high does not count", []core.Bar{{T: etMs(19, 0) - 86_400_000, O: 1, H: 5, L: 0.99, C: 1, V: 1000}}, 0, 1},
		{"first bars of the day", nil, 0, 1},
	}
	for _, c := range cases {
		got, chart := run(c.pre, c.seed)
		if got != c.want || chart != (c.want == 1) {
			t.Fatalf("%s: engine %d chart %v, want %d", c.name, got, chart, c.want)
		}
	}
	// the seeded hod (the day before the engine watched) counts too
	if got, _ := run(nil, 1.50); got != 0 {
		t.Fatalf("seeded day high 1.50: engine %d, want 0", got)
	}
}

func TestEngineDayHighBefore(t *testing.T) {
	var d dildo1m
	start := etMs(4, 0)
	d.foldDayHi(start-m1Ms, start, 9) // before 04:00: ignored
	if d.dayHi != 0 {
		t.Fatalf("pre-04:00 bar folded: %v", d.dayHi)
	}
	d.foldDayHi(start+5*m1Ms, start, 1.2)
	if got := d.dayHighBefore(start+10*m1Ms, start, 1.5); got != 1.5 {
		t.Fatalf("hod before: %v", got)
	}
	if got := d.dayHighBefore(start, start, 7); got != 1.2 {
		t.Fatalf("04:00 minute must not trust the hod: %v", got)
	}
	for i := 0; i < 10; i++ {
		d.push(1, 1.02, 0.99, 1, 100)
	}
	if got := d.judgeEarly(1.2, 1.25, 1.5); got != 0 {
		t.Fatalf("below the day high: %d", got)
	}
	if got := d.judgeEarly(1.2, 1.25, 1.1); got != 1 {
		t.Fatalf("new day high: %d", got)
	}
}

// HOD only: a dildo above every earlier high of the day alerts (hand-off, history,
// row); one under an earlier high alerts nothing, so flagscan never admits it as HOT
// and its 9 EMA pullback/touch alerts never fire.
func TestOnlyHodDildoAlerts(t *testing.T) {
	run := func(pre []core.Bar) (hits int, sigs int, rowNull bool) {
		ts, sg := newSigTS(true, false)
		withSignalLog(sg, 16)
		bars := append(append([]core.Bar(nil), pre...), flatBars1m(etMs(5, 0), 30, 1.00)...)
		d := dildoBar(etMs(5, 30))
		for _, b := range append(bars, d) {
			feed1m(ts, b)
		}
		r := sigRow(ts, d.T+2*m1Ms)
		return len(drainHits(sg)), len(drainSignals(sg)), core.IsNull(r.DildoAge)
	}
	if h, s, null := run(nil); h != 1 || s != 1 || null {
		t.Fatalf("HOD dildo: hits %d signals %d rowNull %v", h, s, null)
	}
	under := []core.Bar{{T: etMs(4, 30), O: 1, H: 1.20, L: 0.99, C: 1, V: 1000}}
	if h, s, null := run(under); h != 0 || s != 0 || !null {
		t.Fatalf("dildo under an earlier high: hits %d signals %d rowNull %v", h, s, null)
	}
}
