package climax

import (
	"math"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/emaflag"
)

func timeHM(ms int64, loc *time.Location) string {
	return time.UnixMilli(ms).In(loc).Format("15:04")
}

var testHit = Hit{DropPct: 10, VolX: 15, WickPct: 40, DollarVol: 1e6, EmaDistPct: -2, Mid: 9.275}

func TestLatchStrictTests(t *testing.T) {
	p := DefaultParams()
	var x Latch
	x.Arm(1000, 9.0, testHit)
	if x.State != StateWatching || x.Bars != 0 || x.EndMs != 0 {
		t.Fatalf("armed: %+v", x)
	}
	// a low EQUAL to the climax low does not fail; a close EQUAL to mid does not confirm
	if s := x.Step(2000, 9.0, 9.275, p); s != StateWatching || x.Bars != 1 {
		t.Fatalf("equal low / equal mid: %d bars %d", s, x.Bars)
	}
	if s := x.Step(3000, 9.0, math.Nextafter(9.275, 10), p); s != StateConfirmed || x.EndMs != 3000 || x.ConfPx != math.Nextafter(9.275, 10) {
		t.Fatalf("one ulp over mid must confirm: %+v", x)
	}
	// decided: never moves again
	if s := x.Step(4000, 1, 1, p); s != StateConfirmed || x.Bars != 2 {
		t.Fatalf("a decided latch moved: %+v", x)
	}
}

func TestLatchFailBeforeConfirm(t *testing.T) {
	var x Latch
	x.Arm(1000, 9.0, testHit)
	if s := x.Step(2000, math.Nextafter(9.0, 0), 9.5, DefaultParams()); s != StateFailed || x.EndMs != 2000 {
		t.Fatalf("an undercut that closes above mid must FAIL: %+v", x)
	}
}

func TestLatchExpiresAtTheFifthBar(t *testing.T) {
	p := DefaultParams()
	var x Latch
	x.Arm(0, 9.0, testHit)
	for k := 1; k <= 4; k++ {
		if s := x.Step(int64(k)*60_000, 9.1, 9.2, p); s != StateWatching {
			t.Fatalf("bar %d: %d", k, s)
		}
	}
	if s := x.Step(5*60_000, 9.1, 9.2, p); s != StateExpired || x.EndMs != 5*60_000 || x.Bars != 5 {
		t.Fatalf("5th bar: %+v", x)
	}
	// Arm replaces whatever the latch held (latest supersedes)
	x.Arm(9, 8, testHit)
	if x.State != StateWatching || x.Bars != 0 || x.BarMs != 9 || x.EndMs != 0 || x.ConfPx != 0 {
		t.Fatalf("re-arm: %+v", x)
	}
}

// The engine calls Classify, Arm and Step once per closed traded minute of
// every ticker: none of them may allocate.
func TestNoAllocations(t *testing.T) {
	p := DefaultParams()
	pr := Priors{N: 10, Hi: 50, Lo: 48, SumVol: 100_000, MaxVol: 10_000}
	var x Latch
	if n := testing.AllocsPerRun(1000, func() {
		if h, ok := Classify(48, 51, 47, 48.5, 80_000, pr, 49, 700, p); ok {
			x.Arm(1, 47, h)
			x.Step(2, 47.5, 48, p)
		}
	}); n != 0 {
		t.Fatalf("%v allocations per judge", n)
	}
	if _, ok := Classify(48, 51, 47, 48.5, 80_000, pr, 49, 700, p); !ok {
		t.Fatal("the alloc probe is not a climax: it measures nothing")
	}
}

// TestEmaSeriesPinnedToEmaflag pins the private emaSeries to emaflag's
// (emaflag.Evaluate's Result.Ema is the EMA at the last candle, so every prefix
// reads emaflag's series bar by bar). Bit-for-bit, NaN warm-up included.
func TestEmaSeriesPinnedToEmaflag(t *testing.T) {
	fx := loadFixtures(t)
	compared := 0
	for name, bars := range fx {
		if len(bars) > 240 {
			bars = bars[len(bars)-240:]
		}
		closes := make([]float64, len(bars))
		for i := range bars {
			closes[i] = bars[i].C
		}
		for _, period := range []int{9, 1, 14} {
			mine := emaSeries(closes, period)
			p := emaflag.DefaultParams()
			p.EmaPeriod = period
			for k := 1; k <= len(bars); k++ {
				theirs := emaflag.Evaluate(bars[:k], 60_000, bars[k-1].T+600_000, p).Ema
				got := mine[k-1]
				if math.IsNaN(theirs) != math.IsNaN(got) || (!math.IsNaN(got) && math.Float64bits(got) != math.Float64bits(theirs)) {
					t.Fatalf("%s period %d bar %d: emaSeries %v, emaflag %v", name, period, k-1, got, theirs)
				}
				compared++
			}
		}
	}
	t.Logf("compared %d EMA values", compared)
}

// The private ET helpers against EDT and EST, and the engine's shared minute /
// day formulas against the zone's own wall clock.
func TestEtHelpersEdtAndEst(t *testing.T) {
	loc := etLocation()
	for _, tc := range []struct {
		utc    time.Time
		offH   int64
		etMin  int
		etDate string
	}{
		{time.Date(2026, 10, 7, 13, 30, 0, 0, time.UTC), -4, 570, "2026-10-07"}, // 09:30 EDT
		{time.Date(2026, 12, 7, 14, 30, 0, 0, time.UTC), -5, 570, "2026-12-07"}, // 09:30 EST
		{time.Date(2026, 10, 8, 3, 59, 0, 0, time.UTC), -4, 1439, "2026-10-07"}, // 23:59 EDT, still the 7th
		{time.Date(2026, 10, 8, 4, 0, 0, 0, time.UTC), -4, 0, "2026-10-08"},     // midnight EDT
		{time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC), -4, 90, "2026-11-01"},   // 01:30 EDT, before the fall-back (06:00 UTC)
		{time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC), -5, 90, "2026-11-01"},   // 01:30 EST, after it
	} {
		ms := tc.utc.UnixMilli()
		off := etOffsetMs(ms)
		local := tc.utc.In(loc)
		_, zoneOff := local.Zone()
		if off != int64(zoneOff)*1000 {
			t.Errorf("%v: etOffsetMs %d, zone %d", tc.utc, off, zoneOff)
		}
		if got, want := EtMinute(ms, off), local.Hour()*60+local.Minute(); got != want {
			t.Errorf("%v: EtMinute %d, wall clock %d", tc.utc, got, want)
		}
		y, m, d := local.Date()
		if got := time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86_400; got != EtDay(ms, off) {
			t.Errorf("%v: EtDay %d, wall-clock date %d", tc.utc, EtDay(ms, off), got)
		}
		if off != tc.offH*3_600_000 || EtMinute(ms, off) != tc.etMin || local.Format("2006-01-02") != tc.etDate {
			t.Errorf("%v: off %d min %d date %s", tc.utc, off, EtMinute(ms, off), local.Format("2006-01-02"))
		}
	}
	if floorDiv(-1, 60_000) != -1 || floorMod(-1, 1440) != 1439 {
		t.Error("floorDiv / floorMod round toward -inf")
	}
}

// 09:30 ET is muted under both offsets; Evaluate reads the zone per UTC hour.
func TestMuteInEdtAndEst(t *testing.T) {
	for _, t0 := range []time.Time{time.Date(2026, 10, 7, 13, 18, 0, 0, time.UTC), time.Date(2026, 12, 7, 14, 18, 0, 0, time.UTC)} {
		var bars []core.Bar
		for i := 0; i < 12; i++ {
			c := 10 - 0.035*float64(i)
			bars = append(bars, core.Bar{T: t0.UnixMilli() + int64(i)*60_000, O: c + 0.02, H: c + 0.04, L: c - 0.04, C: c, V: 10_000})
		}
		bars = append(bars, core.Bar{T: t0.UnixMilli() + 12*60_000, O: 9.5, H: 9.55, L: 9.0, C: 9.3, V: 150_000})
		if hm := timeHM(bars[12].T, etLocation()); hm != "09:30" {
			t.Fatalf("setup: the candle starts %s", hm)
		}
		now := bars[12].T + 600_000
		if r := Evaluate(bars, 60_000, now, DefaultParams()); len(r.Events) != 0 {
			t.Errorf("%v: 09:30 must be muted, got %v", t0, r.Events)
		}
		p := DefaultParams()
		p.MuteStartMin, p.MuteEndMin = 0, 0
		if r := Evaluate(bars, 60_000, now, p); len(r.Events) != 1 {
			t.Errorf("%v: unmuted it is a climax, got %v", t0, r.Events)
		}
	}
}

func TestEvaluateEmptyCases(t *testing.T) {
	bars := []core.Bar{{T: 0, O: 1, H: 1, L: 1, C: 1, V: 1}}
	for _, r := range []Result{Evaluate(nil, 60_000, 0, DefaultParams()), Evaluate(bars, 300_000, 0, DefaultParams())} {
		if len(r.Events) != 0 || r.State != StateNone || !math.IsNaN(r.ClimaxLow) || r.LastClimaxTs != 0 {
			t.Errorf("empty: %+v", r)
		}
	}
	p := DefaultParams()
	p.Enabled = false
	if _, ok := Classify(48, 51, 47, 48.5, 80_000, Priors{N: 10, Hi: 50, Lo: 48, SumVol: 100_000, MaxVol: 10_000}, 49, 700, p); ok {
		t.Error("disabled must not classify")
	}
}

func BenchmarkClassify(b *testing.B) {
	p := DefaultParams()
	pr := Priors{N: 10, Hi: 50, Lo: 48, SumVol: 100_000, MaxVol: 10_000}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Classify(48, 51, 47, 48.5, 80_000, pr, 49, 700, p)
	}
}

// BenchmarkEvaluate on ~1 900 bars (the five fixtures laid end to end).
func BenchmarkEvaluate(b *testing.B) {
	fx := loadFixtures(b)
	var bars []core.Bar
	for _, name := range []string{"climax-biya-2026-10-07", "climax-clro-2026-08-06", "climax-xndu-2026-09-22"} {
		bars = append(bars, fx[name]...)
	}
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
