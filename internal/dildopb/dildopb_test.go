package dildopb

import (
	"math"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// The candle gates at their round-number defaults (the TS suite pins the same
// boundaries; the golden classify vectors pin them to the last ulp).
func TestClassifyDefaults(t *testing.T) {
	p := DefaultParams()
	// body exactly 8 % of an open of 25, bodyX exactly 4, volX exactly 5, top exactly 20 %
	h, ok := Classify(25, 27.5, 25, 27, 100_000, 5, 200_000, 10, p)
	if !ok || h.BodyPct != 8 || h.BodyX != 4 || h.VolX != 5 || h.TopPct != 20 || h.DollarVol != 2_700_000 {
		t.Fatalf("the boundary candle: %+v ok %v", h, ok)
	}
	for _, tc := range []struct {
		name               string
		o, hi, l, c        float64
		v                  int64
		sumAbsBody, sumVol float64
		n                  int
		want               bool
	}{
		{"volX just under 5", 25, 27.5, 25, 27, 99_999, 5, 200_000, 10, false},
		{"bodyX just under 4", 25, 27.5, 25, 27, 100_000, 5.0001, 200_000, 10, false},
		{"top just over 20 %", 25, 27.5001, 25, 27, 100_000, 5, 200_000, 10, false},
		{"close 0.20 passes the price floor", 0.16, 0.2, 0.16, 0.2, 500_000, 0, 0, 3, true},
		{"close 0.1999 fails", 0.16, 0.1999, 0.16, 0.1999, 500_000, 0, 0, 3, false},
		{"$99,999.80 fails", 0.16, 0.2, 0.16, 0.2, 499_999, 0, 0, 3, false},
		{"2 priors", 1, 1.3, 0.99, 1.29, 500_000, 0.01, 1000, 2, false},
		{"3 priors", 1, 1.3, 0.99, 1.29, 500_000, 0.015, 1500, 3, true},
		{"red", 1, 1.1, 0.9, 0.95, 500_000, 0, 0, 10, false},
		{"doji", 1, 1.1, 0.9, 1, 500_000, 0, 0, 10, false},
		{"h == l", 1.2, 1.2, 1.2, 1.2, 500_000, 0, 0, 10, false},
		{"open 0", 0, 1.2, 0, 1.2, 500_000, 0, 0, 10, false},
	} {
		if _, ok := Classify(tc.o, tc.hi, tc.l, tc.c, tc.v, tc.sumAbsBody, tc.sumVol, tc.n, p); ok != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, ok, tc.want)
		}
	}
	// a prior mean of 0 passes with infinite multiples
	h, ok = Classify(1, 1.25, 0.98, 1.24, 120_000, 0, 0, 5, p)
	if !ok || !math.IsInf(h.BodyX, 1) || !math.IsInf(h.VolX, 1) {
		t.Errorf("mean 0: %+v ok %v", h, ok)
	}
}

// Classify sits on the engine's per-minute judge for ~11 000 tickers: it must not allocate.
func TestClassifyAllocatesNothing(t *testing.T) {
	p := DefaultParams()
	if n := testing.AllocsPerRun(1000, func() { Classify(25, 27.5, 25, 27, 100_000, 5, 200_000, 10, p) }); n != 0 {
		t.Fatalf("Classify allocates %v per call", n)
	}
}

// toFixed must be JS Number.prototype.toFixed: exact binary value, ties AWAY
// from zero (strconv rounds ties to even), '-' kept for negatives.
func TestToFixedIsJS(t *testing.T) {
	for _, tc := range []struct {
		x    float64
		d    int
		want string
	}{
		{0.25, 1, "0.3"}, {0.35, 1, "0.3"}, {2.5, 0, "3"}, {3.5, 0, "4"}, {0.125, 2, "0.13"},
		{1.005, 2, "1.00"}, {-0.04, 1, "-0.0"}, {math.Copysign(0, -1), 1, "0.0"}, {-1.25, 1, "-1.3"},
		{0.5782, 4, "0.5782"}, {7.93, 2, "7.93"}, {1.8691, 2, "1.87"}, {12345.678, 0, "12346"}, {0.00004, 4, "0.0000"},
		{33.67003367003372, 0, "34"}, {42.000000000000455, 1, "42.0"},
	} {
		if got := toFixed(tc.x, tc.d); got != tc.want {
			t.Errorf("toFixed(%v, %d) = %q, want %q", tc.x, tc.d, got, tc.want)
		}
	}
	if jsRound(0.49999999999999994) != 0 || jsRound(2.5) != 3 || jsRound(-2.5) != -2 || jsRound(716.518) != 717 {
		t.Error("jsRound is not Math.round")
	}
	if spct1(-0.04) != "+0.0%" || spct1(1.6) != "+1.6%" || spct1(-1.44) != "-1.4%" {
		t.Error("spct1")
	}
	if fmtDvol(716_518.08) != "$717k" || fmtDvol(4_303_495.7) != "$4.3M" {
		t.Error("fmtDvol")
	}
}

func bar(t0 int64, i int, o, h, l, c float64, v int64) core.Bar {
	return core.Bar{T: t0 + int64(i)*60_000, O: o, H: h, L: l, C: c, V: v}
}

// The unit-test base series of dildoPb.test.ts: a dildo at bar 12, a touch at bar 16.
func TestEvaluatePaths(t *testing.T) {
	const t0 = int64(1791406800000) // 2026-10-07 09:00 ET
	base := func() []core.Bar {
		var b []core.Bar
		for i := 0; i < 12; i++ {
			b = append(b, bar(t0, i, 1, 1.005, 0.995, 1, 1000))
		}
		return append(b, bar(t0, 12, 1, 1.205, 0.995, 1.2, 100_000), bar(t0, 13, 1.2, 1.22, 1.19, 1.21, 5000),
			bar(t0, 14, 1.21, 1.22, 1.19, 1.2, 5000), bar(t0, 15, 1.2, 1.22, 1.19, 1.2, 5000))
	}
	run := func(b []core.Bar, nowMs int64) Result {
		if nowMs == 0 {
			nowMs = b[len(b)-1].T + 600_000
		}
		return Evaluate(b, 60_000, nowMs, DefaultParams())
	}
	kinds := func(r Result) string {
		s := ""
		for _, e := range r.Events {
			s += e.Kind + "/" + e.Reason + " "
		}
		return s
	}
	if r := run(append(base(), bar(t0, 16, 1.2, 1.2, 1.12, 1.18, 8000)), 0); kinds(r) != "dildo/ dildoPb/ " || r.PbState != PbConfirmed || r.PbPx != 1.18 {
		t.Errorf("confirm: %s %d %v", kinds(r), r.PbState, r.PbPx)
	}
	if r := run(append(base(), bar(t0, 16, 1.2, 1.2, 1.09, 1.18, 8000)), 0); kinds(r) != "dildo/ dildoCancel/floor " || r.PbState != PbCancelled {
		t.Errorf("floor: %s", kinds(r))
	}
	if r := run(append(base(), bar(t0, 16, 1.2, 1.2, 1.101, 1.105, 8000)), 0); kinds(r) != "dildo/ dildoCancel/ema " {
		t.Errorf("ema: %s", kinds(r))
	}
	if r := run(append(base()[:13], bar(t0, 13, 1.2, 1.2, 1.08, 1.15, 5000)), 0); kinds(r) != "dildo/ dildoCancel/faded " {
		t.Errorf("faded: %s", kinds(r))
	}
	// forming: touching, then a floor break; never an event
	touch := bar(t0, 16, 1.2, 1.2, 1.12, 1.18, 8000)
	if r := run(append(base(), touch), touch.T+30_000); kinds(r) != "dildo/ " || r.PbState != PbTouching || r.TouchTs != touch.T {
		t.Errorf("forming touch: %s %d", kinds(r), r.PbState)
	}
	touch.L = 1.09
	if r := run(append(base(), touch), touch.T+30_000); r.PbState != PbCancelled || r.TouchTs != 0 {
		t.Errorf("forming floor: %d", r.PbState)
	}
	// a quiet name: watching until the last eligible bar has closed
	b := base()
	if r := run(b, t0+12*60_000+20*60_000+59_999); r.PbState != PbWatching {
		t.Errorf("quiet, before deadline+60s: %d", r.PbState)
	}
	if r := run(b, t0+12*60_000+21*60_000); r.PbState != PbExpired {
		t.Errorf("quiet, at deadline+60s: %d", r.PbState)
	}
	// tf != 1m / disabled -> empty
	if r := Evaluate(b, 300_000, b[len(b)-1].T+600_000, DefaultParams()); len(r.Events) != 0 || r.PbState != PbNone || !math.IsNaN(r.Floor) {
		t.Error("5m must be empty")
	}
	p := DefaultParams()
	p.Enabled = false
	if r := Evaluate(b, 60_000, b[len(b)-1].T+600_000, p); len(r.Events) != 0 {
		t.Error("disabled must be empty")
	}
}

// BenchmarkEvaluate: flagscan runs Evaluate once a second per tracked symbol
// over ~1 900 bars (previous day + today). Target: under 50 µs.
func BenchmarkEvaluate(b *testing.B) {
	fx := loadFixtures(&testing.T{})
	var bars []core.Bar
	for _, name := range []string{"dildo-wvvip-2026-08-26", "dildo-mb-2026-08-07", "dildo-pdsb-2026-09-08"} {
		bars = append(bars, fx[name]...)
	}
	// one ascending series of ~1 900 bars (the three fixtures laid end to end, re-timed)
	if len(bars) > 1900 {
		bars = bars[:1900]
	}
	series := make([]core.Bar, len(bars))
	t0 := int64(1791316800000)
	for i := range bars {
		series[i] = bars[i]
		series[i].T = t0 + int64(i)*60_000
	}
	p := DefaultParams()
	now := series[len(series)-1].T + 30_000
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Evaluate(series, 60_000, now, p)
	}
}

func BenchmarkClassify(b *testing.B) {
	p := DefaultParams()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Classify(25, 27.5, 25, 27, 100_000, 5, 200_000, 10, p)
	}
}
