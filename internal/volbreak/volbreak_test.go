package volbreak

import (
	"math"
	"testing"
)

const t0 = int64(1_700_000_000_000) / minMs * minMs

// feed steps a base: n bars from minute 0, each high/low from fn, volume v.
func feed(j *Judge, n int, v int64, fn func(i int) (h, l float64)) {
	for i := 0; i < n; i++ {
		h, l := fn(i)
		j.Step(Bar{Ms: t0 + int64(i)*minMs, H: h, L: l, C: (h + l) / 2, V: v}, math.NaN(), true)
	}
}

// base: 40 one-minute bars 9.6-10.0, the high tested at minutes 5 and 25.
func base(i int) (float64, float64) {
	if i == 5 || i == 25 {
		return 10.0, 9.7
	}
	return 9.9, 9.6
}

func TestRangeDetection(t *testing.T) {
	var j Judge
	feed(&j, 40, 1000, base)
	r, ok := j.FindRange()
	if !ok || r.High != 10.0 || r.Low != 9.6 || r.Touches != 2 || r.Bars != 40 || r.AvgVol != 1000 {
		t.Fatalf("range %+v ok=%v", r, ok)
	}
}

func TestRangeNeedsTwoTouches(t *testing.T) {
	var j Judge
	feed(&j, 40, 1000, func(i int) (float64, float64) {
		if i == 25 {
			return 10.0, 9.7
		}
		return 9.9, 9.6
	})
	if r, ok := j.FindRange(); ok {
		t.Fatalf("one touch made a range: %+v", r)
	}
	// two adjacent bars at the high are ONE visit
	var k Judge
	feed(&k, 40, 1000, func(i int) (float64, float64) {
		if i == 25 || i == 26 {
			return 10.0, 9.7
		}
		return 9.9, 9.6
	})
	if r, ok := k.FindRange(); ok || r.Touches != 1 {
		t.Fatalf("adjacent bars counted as two touches: %+v", r)
	}
	// two visits too close in time (2 minutes; TouchSepMin 3) do not count
	var m Judge
	feed(&m, 40, 1000, func(i int) (float64, float64) {
		if i == 25 || i == 27 {
			return 10.0, 9.7
		}
		return 9.9, 9.6
	})
	if _, ok := m.FindRange(); ok {
		t.Fatal("touches 2 minutes apart made a range")
	}
}

func TestRangeNeeds30Minutes(t *testing.T) {
	var j Judge
	feed(&j, 29, 1000, base)
	if _, ok := j.FindRange(); ok {
		t.Fatal("29 minutes made a range")
	}
	j.Step(Bar{Ms: t0 + 29*minMs, H: 9.9, L: 9.6, C: 9.8, V: 1000}, math.NaN(), true)
	if _, ok := j.FindRange(); !ok {
		t.Fatal("30 minutes did not make a range")
	}
}

func TestRangeTightness(t *testing.T) {
	var j Judge
	// a 16% wide base: no range of 30 minutes
	feed(&j, 40, 1000, func(i int) (float64, float64) {
		if i == 5 || i == 25 {
			return 11.5, 10.5
		}
		if i == 15 {
			return 10.2, 9.9
		}
		return 10.8, 10.4
	})
	if r, ok := j.FindRange(); ok {
		t.Fatalf("16%% range accepted: %+v", r)
	}
	// the range ends where it would get too wide: bars before a far low are out
	var k Judge
	feed(&k, 45, 1000, func(i int) (float64, float64) {
		if i < 5 {
			return 8.0, 7.5 // far below: not part of the base
		}
		return base(i)
	})
	if r, ok := k.FindRange(); !ok || r.Bars != 40 || r.Low != 9.6 {
		t.Fatalf("range %+v ok=%v", r, ok)
	}
}

func TestBreakNeeds3xVolume(t *testing.T) {
	var j Judge
	feed(&j, 40, 1000, base)
	if ev := j.Step(Bar{Ms: t0 + 40*minMs, H: 10.3, L: 9.9, C: 10.2, V: 2900}, 10.0, true); ev != EvNone {
		t.Fatalf("2.9x broke: %v", ev)
	}
	var k Judge
	feed(&k, 40, 1000, base)
	if ev := k.Step(Bar{Ms: t0 + 40*minMs, H: 10.3, L: 9.9, C: 10.2, V: 3200}, 10.0, true); ev != EvBreak {
		t.Fatalf("3.2x did not break: %v", ev)
	}
	if k.Level != 10.0 || math.Abs(k.VolX-3.2) > 1e-9 || k.State != StateWatching || k.BreakEma != 10.0 {
		t.Fatalf("latch %+v", k)
	}
	// a close AT the range high is not a break; nor is a stock not in play
	var m Judge
	feed(&m, 40, 1000, base)
	if ev := m.Step(Bar{Ms: t0 + 40*minMs, H: 10.3, L: 9.9, C: 10.0, V: 5000}, 10.0, true); ev != EvNone {
		t.Fatal("a close at the high broke")
	}
	var n Judge
	feed(&n, 40, 1000, base)
	if ev := n.Step(Bar{Ms: t0 + 40*minMs, H: 10.3, L: 9.9, C: 10.2, V: 5000}, 10.0, false); ev != EvNone {
		t.Fatal("broke out of play")
	}
}

func broken(t *testing.T) *Judge {
	t.Helper()
	j := &Judge{}
	feed(j, 40, 1000, base)
	if ev := j.Step(Bar{Ms: t0 + 40*minMs, H: 10.6, L: 9.95, C: 10.5, V: 5000}, 10.1, true); ev != EvBreak {
		t.Fatal("no break")
	}
	return j
}

func TestPullbackEntry(t *testing.T) {
	j := broken(t)
	// a bar far above the EMA: nothing
	if ev := j.Step(Bar{Ms: t0 + 41*minMs, H: 10.9, L: 10.6, C: 10.8, V: 2000}, 10.2, true); ev != EvNone {
		t.Fatal("entry far above the EMA")
	}
	// low within 0.3% of the EMA (10.33 vs 10.30) while closing over the level
	if ev := j.Step(Bar{Ms: t0 + 42*minMs, H: 10.7, L: 10.33, C: 10.5, V: 2000}, 10.3, true); ev != EvEntry {
		t.Fatalf("no entry: state %v", j.State)
	}
	if j.State != StateEntry || j.EntryPx != 10.3 || j.EntryMs != t0+42*minMs {
		t.Fatalf("latch %+v", j)
	}
	// only the FIRST pullback
	if ev := j.Step(Bar{Ms: t0 + 43*minMs, H: 10.5, L: 10.2, C: 10.4, V: 2000}, 10.3, true); ev != EvNone {
		t.Fatal("a second entry")
	}
}

func TestInvalidation(t *testing.T) {
	j := broken(t)
	// a close back under the range high fails it, even when the low touched the EMA
	if ev := j.Step(Bar{Ms: t0 + 41*minMs, H: 10.4, L: 9.9, C: 9.95, V: 2000}, 10.1, true); ev != EvNone || j.State != StateFailed {
		t.Fatalf("not failed: %v %v", ev, j.State)
	}
	if ev := j.Step(Bar{Ms: t0 + 42*minMs, H: 10.4, L: 10.1, C: 10.2, V: 2000}, 10.1, true); ev != EvNone {
		t.Fatal("entry after a failure")
	}
	// no pullback in 30 minutes expires it
	k := broken(t)
	for m := int64(41); m <= 70; m++ {
		if ev := k.Step(Bar{Ms: t0 + m*minMs, H: 11.5, L: 11.2, C: 11.4, V: 2000}, 10.5, true); ev != EvNone {
			t.Fatalf("event at minute %d", m)
		}
	}
	if k.State != StateWatching {
		t.Fatalf("expired early: %v", k.State)
	}
	if ev := k.Step(Bar{Ms: t0 + 71*minMs, H: 11.5, L: 10.5, C: 11.4, V: 2000}, 10.5, true); ev != EvNone || k.State != StateExpired {
		t.Fatalf("a pullback 31 minutes later: %v %v", ev, k.State)
	}
}

func TestOncePerLevel(t *testing.T) {
	j := broken(t)
	// fails back into the old range, which is then re-broken: no second alert
	j.Step(Bar{Ms: t0 + 41*minMs, H: 10.2, L: 9.7, C: 9.8, V: 1000}, 10.1, true)
	if j.State != StateFailed {
		t.Fatal("not failed")
	}
	for m := int64(42); m < 50; m++ {
		j.Step(Bar{Ms: t0 + m*minMs, H: 9.9, L: 9.6, C: 9.8, V: 1000}, 9.9, true)
	}
	if ev := j.Step(Bar{Ms: t0 + 50*minMs, H: 10.8, L: 9.9, C: 10.7, V: 9000}, 10.0, true); ev != EvNone {
		t.Fatal("the same level fired twice")
	}
	// a NEW range after the break (30+ minutes, two tests of 11.0) is still not
	// a new level: it may break again the same day (no once-per-day limit)
	var k Judge
	feed(&k, 40, 1000, base)
	k.Step(Bar{Ms: t0 + 40*minMs, H: 10.6, L: 9.95, C: 10.5, V: 5000}, 10.1, true)
	newRange := func(k *Judge, from int64) {
		for m := from; m < from+39; m++ {
			h, l := 10.9, 10.6
			if m == from+9 || m == from+29 {
				h = 11.0
			}
			k.Step(Bar{Ms: t0 + m*minMs, H: h, L: l, C: 10.8, V: 1000}, 10.75, true)
		}
	}
	newRange(&k, 41)
	if _, ok := k.FindRange(); !ok {
		t.Fatal("no new range")
	}
	if ev := k.Step(Bar{Ms: t0 + 80*minMs, H: 11.4, L: 10.9, C: 11.3, V: 6000}, 10.9, true); ev != EvBreak {
		t.Fatalf("a new level did not break the same day: %v", ev)
	}
	// the next ET day re-arms it
	k.NewDay()
	newRange(&k, 2000)
	if ev := k.Step(Bar{Ms: t0 + 2039*minMs, H: 11.4, L: 10.9, C: 11.3, V: 6000}, 10.9, true); ev != EvBreak || k.Level != 11.0 {
		t.Fatalf("no break of the next day's range: %v level %v", ev, k.Level)
	}
}

func TestNewDay(t *testing.T) {
	j := broken(t)
	j.NewDay()
	if j.State != StateExpired {
		t.Fatal("watching survived the day")
	}
	if _, ok := j.FindRange(); ok {
		t.Fatal("bars survived the day")
	}
}

func TestInPlay(t *testing.T) {
	if !InPlay(1.10, 1.0, 1_000_000) || InPlay(1.09, 1.0, 2_000_000) || InPlay(1.2, 1.0, 999_999) || InPlay(1.2, 0, 2_000_000) {
		t.Fatal("gate")
	}
}

func BenchmarkStep(b *testing.B) {
	var j Judge
	feed(&j, Ring, 1000, base)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		j.Step(Bar{Ms: t0 + int64(Ring+i)*minMs, H: 9.9, L: 9.6, C: 9.8, V: 1000}, 9.8, true)
	}
}

func TestStepNoAlloc(t *testing.T) {
	var j Judge
	feed(&j, 60, 1000, base)
	i := int64(60)
	if a := testing.AllocsPerRun(100, func() {
		j.Step(Bar{Ms: t0 + i*minMs, H: 9.9, L: 9.6, C: 9.8, V: 1000}, 9.8, true)
		i++
	}); a != 0 {
		t.Fatalf("%v allocs", a)
	}
}
