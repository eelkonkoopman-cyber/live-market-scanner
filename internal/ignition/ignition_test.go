package ignition

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func goldenSeries(t *testing.T, name string) []core.Bar {
	t.Helper()
	for _, s := range loadGolden(t).Series {
		if s.Name == name {
			return s.Bars
		}
	}
	t.Fatalf("no series %q", name)
	return nil
}

// The NCPL-shaped morning fires once at the small 09:06 candle, never on the
// dildo after it; the negative shapes never fire.
func TestNcplAndNegatives(t *testing.T) {
	p := DefaultParams()
	ev := Evaluate(goldenSeries(t, "ncpl"), 60_000, 1<<62, p)
	if len(ev) != 1 || ev[0].Price != 1.88 || ev[0].VolX != 15 || ev[0].BaseHigh != 1.8 {
		t.Fatalf("ncpl %+v", ev)
	}
	for _, name := range []string{"wideBase", "bigBody", "noVolume", "belowDayHigh"} {
		if ev := Evaluate(goldenSeries(t, name), 60_000, 1<<62, p); len(ev) != 0 {
			t.Fatalf("%s fired %+v", name, ev)
		}
	}
}

func TestJudgeReasons(t *testing.T) {
	p := DefaultParams()
	hs, ls := make([]float64, 15), make([]float64, 15)
	for i := range hs {
		hs[i], ls[i] = 1.8, 1.75
	}
	j := func(o, c float64, v int64, dh float64) Reason {
		return Judge(hs, ls, 200_000, 10, o, c+0.01, c, v, dh, p).Reason
	}
	for _, tc := range []struct {
		o, c float64
		v    int64
		dh   float64
		want Reason
	}{
		{1.79, 1.88, 300_000, 1.8, OK},
		{1.79, 1.81, 300_000, 1.8, BodySmall},
		{1.79, 1.98, 300_000, 1.8, BodyBig},
		{1.79, 1.88, 60_000, 1.8, NoVolume},
		{1.79, 1.88, 300_000, 2.1, NotNewHigh},
		{1.79, 1.88, 20_000, 1.8, LowDvol},
		{0.15, 0.158, 30_000_000, 0, LowPrice},
	} {
		if got := j(tc.o, tc.c, tc.v, tc.dh); got != tc.want {
			t.Errorf("%+v: got %v", tc, got)
		}
	}
	if r := Judge(hs[:9], ls[:9], 200_000, 9, 1.79, 1.89, 1.88, 300_000, 0, p).Reason; r != FewBars {
		t.Fatalf("few bars: %v", r)
	}
	if n := testing.AllocsPerRun(100, func() { Judge(hs, ls, 200_000, 10, 1.79, 1.89, 1.88, 300_000, 1.8, p) }); n != 0 {
		t.Fatalf("Judge allocates %v", n)
	}
}

func TestLatchOnePerBaseAndCooldown(t *testing.T) {
	p := DefaultParams()
	var l Latch
	ok := Verdict{Reason: OK}
	if !l.Step(60_000, ok, false, p) || l.Step(120_000, ok, false, p) {
		t.Fatal("one per base")
	}
	// the base breaks, but within the cooldown
	l.Step(180_000, Verdict{Reason: WideBase}, false, p)
	if l.Step(240_000, ok, false, p) {
		t.Fatal("cooldown")
	}
	if !l.Step(60_000+10*60_000, ok, false, p) {
		t.Fatal("re-armed after the cooldown")
	}
}
