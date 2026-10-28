package emaflag

// The 9 EMA SLAM on the Go side: the same synthetic bars as F18 in
// web/src/signals/emaFlag.test.ts (bars 0..26 are F3's READY flag, bar 27 the
// slam), each rule flipped on its own, the episode guard, the forming-bar rule,
// the switch, and Result.LastSlam*. The golden vectors (golden_test.go,
// TestGoldenSlam) carry the parity with the TS engine on real bars, SPAI
// 2026-10-07 08:15 included.

import (
	"math"
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// slamReady is F18's READY: 20 quiet bars at 1.00, the thrust (bar 20, top
// 1.20), the flag 21..26 (closes 1.180 .. 1.192; o = c - 0.001, h = c + 0.004,
// l = c - 0.004, v 1500). The state after bar 26 is ready.
func slamReady() []core.Bar {
	var bars []core.Bar
	for i := 0; i < 20; i++ {
		bars = append(bars, xbar(i, 1.0, 1.002, 0.998, 1.0, 1000))
	}
	bars = append(bars, xbar(20, 1.0, 1.2, 0.999, 1.18, 9000))
	for k, c := range []float64{1.18, 1.184, 1.188, 1.19, 1.192, 1.192} {
		bars = append(bars, xbar(21+k, c-0.001, c+0.004, c-0.004, c, 1500))
	}
	return bars
}

// slamBar is F18's SLAM: opens 1.19 above EMA@26, closes 1.08 = 4.9% under
// EMA@27, body 7.6 ATR / 9.2% of the open, close 8% up its range, 2.9x volume.
func slamBar() core.Bar { return xbar(27, 1.19, 1.192, 1.07, 1.08, 6000) }

func with(bars []core.Bar, more ...core.Bar) []core.Bar {
	return append(append([]core.Bar(nil), bars...), more...)
}

func slamsOf(r Result) []Event {
	var out []Event
	for _, e := range r.Events {
		if e.Kind == KindSlam {
			out = append(out, e)
		}
	}
	return out
}

func withoutSlams(evs []Event) []Event {
	out := []Event{}
	for _, e := range evs {
		if e.Kind != KindSlam {
			out = append(out, e)
		}
	}
	return out
}

func sameEvents(a, b []Event) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSlam_SyntheticFiresLastOnItsBar(t *testing.T) {
	p := DefaultParams()
	if r := evalClosed(slamReady(), p); r.State != StateReady {
		t.Fatalf("bars 0..26: state %s, want ready", r.State)
	}
	bars := with(slamReady(), slamBar())
	r := evalClosed(bars, p)
	wantStamps(t, "synthetic slam", r, "20 impulse", "22 flat", "26 emaNear", "27 failed", "27 slam")
	s := slamsOf(r)
	if len(s) != 1 || s[0].Label != LabelSlam || s[0].Price != 1.08 || s[0].Ts != bars[27].T {
		t.Fatalf("slam events %+v", s)
	}
	closes := make([]float64, len(bars))
	for i := range bars {
		closes[i] = bars[i].C
	}
	ema27 := emaSeries(closes, 9)[27]
	if r.LastSlamTs != bars[27].T || r.LastSlamClose != 1.08 || r.LastSlamPct != (1.08/ema27-1)*100 || !(r.LastSlamPct < -3) {
		t.Errorf("LastSlam = %d %v %v, want %d 1.08 %v", r.LastSlamTs, r.LastSlamClose, r.LastSlamPct, bars[27].T, (1.08/ema27-1)*100)
	}
	// The switch: off is the same run minus the event, every level identical.
	p.Slam = false
	off := evalClosed(bars, p)
	if !sameEvents(off.Events, withoutSlams(r.Events)) {
		t.Errorf("slam off: events %v, want %v", stamps(off), withoutSlams(r.Events))
	}
	if off.LastSlamTs != 0 || !math.IsNaN(off.LastSlamClose) || !math.IsNaN(off.LastSlamPct) {
		t.Errorf("slam off: LastSlam %d %v %v, want 0 NaN NaN", off.LastSlamTs, off.LastSlamClose, off.LastSlamPct)
	}
	off.Events, r.Events = nil, nil
	off.LastSlamTs, off.LastSlamClose, off.LastSlamPct = r.LastSlamTs, r.LastSlamClose, r.LastSlamPct
	if !sameResult(off, r) {
		t.Errorf("slam off changed the result beyond the slam:\n on  %+v\n off %+v", r, off)
	}
}

// sameResult compares two Results field by field, NaN == NaN.
func sameResult(a, b Result) bool {
	f := func(x, y float64) bool { return x == y || (math.IsNaN(x) && math.IsNaN(y)) }
	return a.State == b.State && sameEvents(a.Events, b.Events) && f(a.Ema, b.Ema) && f(a.EmaClosed, b.EmaClosed) &&
		a.EmaClosedTs == b.EmaClosedTs && f(a.Trigger, b.Trigger) && f(a.Stop, b.Stop) && f(a.FlagHigh, b.FlagHigh) &&
		f(a.FlagLow, b.FlagLow) && f(a.EntryPrice, b.EntryPrice) && a.FlagBars == b.FlagBars && f(a.EmaDistPct, b.EmaDistPct) &&
		a.StateSinceMs == b.StateSinceMs && a.LastSlamTs == b.LastSlamTs && f(a.LastSlamClose, b.LastSlamClose) &&
		f(a.LastSlamPct, b.LastSlamPct)
}

// Each rule on its own (F18's rule tests, the same bars): the bars / params that
// break only that rule give no slam, the params that restore it give one.
func TestSlam_EachRuleFlipsTheSlam(t *testing.T) {
	d := DefaultParams()
	set := func(f func(*Params)) Params { p := d; f(&p); return p }
	with26 := func(c26 float64) []core.Bar {
		b := slamReady()[:26]
		return append(b, xbar(26, c26-0.001, c26+0.004, c26-0.004, c26, 1500))
	}
	s27b := xbar(27, 1.135, 1.137, 1.05, 1.06, 6000)
	thin := slamBar()
	thin.V = 2700 // 1.29x the 2100 mean
	exact := slamBar()
	exact.V = 2730 // exactly 1.3x: inclusive
	cases := []struct {
		name string
		bars []core.Bar
		p    Params
		want int
	}{
		{"defaults", with(slamReady(), slamBar()), d, 1},
		{"rule 1: no flag (minFlatBars 10)", with(slamReady(), slamBar()), set(func(p *Params) { p.MinFlatBars = 10 }), 0},
		{"rule 1: idle", with(slamReady()[:20], xbar(20, 1.0, 1.002, 0.9, 0.91, 9000)), d, 0},
		{"rule 2a: previous close 1.13 under its EMA", with(with26(1.13), s27b), d, 0},
		{"rule 2a: previous close 1.14 above its EMA", with(with26(1.14), s27b), d, 1},
		{"rule 2b: opens 0.8% under the EMA", with(slamReady(), xbar(27, 1.14, 1.142, 1.07, 1.08, 6000)), d, 0},
		{"rule 2b: tolerance 1%", with(slamReady(), xbar(27, 1.14, 1.142, 1.07, 1.08, 6000)), set(func(p *Params) { p.SlamOpenTolPct = 1 }), 1},
		{"rule 3: 2.7% under", with(slamReady(), xbar(27, 1.19, 1.192, 1.1, 1.11, 6000)), d, 0},
		{"rule 3: slamBelowPct 2", with(slamReady(), xbar(27, 1.19, 1.192, 1.1, 1.11, 6000)), set(func(p *Params) { p.SlamBelowPct = 2 }), 1},
		{"rule 4a: 8 ATR", with(slamReady(), slamBar()), set(func(p *Params) { p.SlamBodyAtr = 8 }), 0},
		{"rule 4a: 7.5 ATR", with(slamReady(), slamBar()), set(func(p *Params) { p.SlamBodyAtr = 7.5 }), 1},
		{"rule 4a: 0 = off", with(slamReady(), slamBar()), set(func(p *Params) { p.SlamBodyAtr = 0 }), 1},
		{"rule 4b: 10% body", with(slamReady(), slamBar()), set(func(p *Params) { p.SlamMinBodyPct = 10 }), 0},
		{"rule 4b: 9% body", with(slamReady(), slamBar()), set(func(p *Params) { p.SlamMinBodyPct = 9 }), 1},
		{"rule 5: close 42% up its range", with(slamReady(), xbar(27, 1.19, 1.192, 1.0, 1.08, 6000)), d, 0},
		{"rule 5: closeInRange 0.45", with(slamReady(), xbar(27, 1.19, 1.192, 1.0, 1.08, 6000)), set(func(p *Params) { p.SlamCloseInRange = 0.45 }), 1},
		{"rule 6: 1.29x volume", with(slamReady(), thin), d, 0},
		{"rule 6: exactly 1.3x", with(slamReady(), exact), d, 1},
		{"rule 6: volX 1.2", with(slamReady(), thin), set(func(p *Params) { p.SlamVolX = 1.2 }), 1},
		{"switch off", with(slamReady(), slamBar()), set(func(p *Params) { p.Slam = false }), 0},
	}
	for _, tc := range cases {
		if got := len(slamsOf(evalClosed(tc.bars, tc.p))); got != tc.want {
			t.Errorf("%s: %d slams, want %d", tc.name, got, tc.want)
		}
	}
	// the rule-2a pair really differs only in that rule: both READY after bar 26
	for _, c26 := range []float64{1.13, 1.14} {
		if r := evalClosed(with26(c26), d); r.State != StateReady {
			t.Errorf("c26 %v: state %s, want ready", c26, r.State)
		}
	}
}

func TestSlam_OutOfALongAfterTheExit(t *testing.T) {
	brk := xbar(27, 1.19, 1.21, 1.19, 1.205, 4000)
	b28 := xbar(28, 1.205, 1.215, 1.2, 1.21, 1000)
	if r := evalClosed(with(slamReady(), brk, b28), DefaultParams()); r.State != StateLong {
		t.Fatalf("through bar 28: state %s, want long", r.State)
	}
	r := evalClosed(with(slamReady(), brk, b28, xbar(29, 1.21, 1.212, 1.1, 1.11, 9000)), DefaultParams())
	got := stamps(r)
	if len(got) < 2 || got[len(got)-2] != "29 exit" || got[len(got)-1] != "29 slam" || r.State != StateIdle {
		t.Errorf("events %v state %s, want ... 29 exit, 29 slam / idle", got, r.State)
	}
}

func TestSlam_NeverOnAFormingBar(t *testing.T) {
	bars := with(slamReady(), slamBar())
	forming := Evaluate(bars, 60_000, bars[27].T+30_000, DefaultParams())
	if len(slamsOf(forming)) != 0 || forming.LastSlamTs != 0 || !math.IsNaN(forming.LastSlamPct) {
		t.Errorf("forming bar 27: %v, LastSlamTs %d", stamps(forming), forming.LastSlamTs)
	}
	closed := evalClosed(bars, DefaultParams())
	if len(slamsOf(closed)) != 1 || !sameEvents(closed.Events[:len(forming.Events)], forming.Events) {
		t.Errorf("closed bar 27: %v must extend the forming run %v with the slam", stamps(closed), stamps(forming))
	}
}

func TestSlam_AtMostOnePerEpisode(t *testing.T) {
	p := DefaultParams()
	p.EmaBreakPct, p.MaxFlatOfMovePct, p.MaxFlatPct = 20, 100, 100 // the flag survives its slam
	b28 := xbar(28, 1.08, 1.19, 1.08, 1.19, 2000)
	b29 := xbar(29, 1.19, 1.192, 1.07, 1.08, 9000) // a second candidate out of the same flag
	r := evalClosed(with(slamReady(), slamBar(), b28, b29), p)
	if s := slamsOf(r); len(s) != 1 || s[0].Ts != xbar(27, 0, 0, 0, 0, 0).T || r.State != StateReady {
		t.Errorf("one episode: slams %+v state %s, want only bar 27 / ready", s, r.State)
	}
	thin := slamBar()
	thin.V = 1000 // bar 27 is no slam: the same bar 29 is the episode's one
	r2 := evalClosed(with(slamReady(), thin, b28, b29), p)
	if s := slamsOf(r2); len(s) != 1 || s[0].Ts != b29.T || r2.LastSlamTs != b29.T {
		t.Errorf("without the first: slams %+v LastSlamTs %d, want bar 29", s, r2.LastSlamTs)
	}
}

// slamTwoEpisodes is F18's "a later flag is its own episode": READY, the break
// (LONG on 27), bar 28, a slam-shaped bar 29 (volume v29), a rally to a new top
// 1.315 (30..34) and a new flag (35..38), then bar 39 out of that flag: from
// above the EMA to 5.5% under it.
func slamTwoEpisodes(v29 int64) []core.Bar {
	bars := with(slamReady(),
		xbar(27, 1.19, 1.21, 1.19, 1.205, 4000),
		xbar(28, 1.205, 1.215, 1.2, 1.21, 1000),
		xbar(29, 1.21, 1.212, 1.1, 1.11, v29))
	for k, c := range []float64{1.13, 1.17, 1.22, 1.27, 1.31} {
		bars = append(bars, xbar(30+k, c-0.03, c+0.005, c-0.035, c, 6000))
	}
	for k, c := range []float64{1.3, 1.305, 1.3, 1.302} {
		bars = append(bars, xbar(35+k, c-0.002, c+0.004, c-0.004, c, 2500))
	}
	return bars
}

func slamBars(r Result) []int64 {
	out := []int64{}
	for _, e := range slamsOf(r) {
		out = append(out, (e.Ts-expiryAnchor)/60_000)
	}
	return out
}

func sameInts(a []int64, b ...int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A position that survives its slam (TrailEma off, an 8% stop - both editable
// in the Signal panel) does not swallow the slam of a LATER flag: that flag is
// its own episode. emaFlag.test.ts F18 pins the same bars.
func TestSlam_ALaterFlagIsItsOwnEpisode(t *testing.T) {
	wide := DefaultParams()
	wide.TrailEma, wide.StopBufferPct = false, 8
	s39 := xbar(39, 1.3, 1.302, 1.17, 1.18, 12000)

	r38 := evalClosed(slamTwoEpisodes(9000), wide)
	if !sameInts(slamBars(r38), 29) || r38.State != StateLong || r38.FlagHigh != 1.315 {
		t.Fatalf("through bar 38: slams %v state %s flagHigh %v, want [29] / long / 1.315", slamBars(r38), r38.State, r38.FlagHigh)
	}
	r := evalClosed(with(slamTwoEpisodes(9000), s39), wide)
	got := stamps(r)
	if !sameInts(slamBars(r), 29, 39) || r.LastSlamTs != s39.T || got[len(got)-2] != "39 failed" || got[len(got)-1] != "39 slam" {
		t.Errorf("bar 39 out of the new flag: slams %v events %v LastSlamTs %d, want [29 39] ending 39 failed, 39 slam", slamBars(r), got, r.LastSlamTs)
	}
	if s := slamBars(evalClosed(with(slamTwoEpisodes(1000), s39), wide)); !sameInts(s, 39) {
		t.Errorf("bar 29 on thin volume: slams %v, want [39]", s)
	}
	if s := slamBars(evalClosed(with(slamTwoEpisodes(9000), s39), DefaultParams())); !sameInts(s, 29, 39) {
		t.Errorf("defaults: slams %v, want [29 39]", s)
	}
}

// One slam marks every episode open at the end of the previous bar: bar 39
// slams out of the old position AND the new flag, so a later slam-shaped bar
// out of the still-open position (no flag left) is no second slam.
func TestSlam_OneSlamMarksEveryEpisodeItWentThrough(t *testing.T) {
	wide := DefaultParams()
	wide.TrailEma, wide.StopBufferPct = false, 8
	again := func(v39 int64) []core.Bar {
		return with(slamTwoEpisodes(1000),
			xbar(39, 1.3, 1.302, 1.17, 1.18, v39),
			xbar(40, 1.18, 1.3, 1.18, 1.29, 3000),
			xbar(41, 1.29, 1.292, 1.16, 1.17, 20000))
	}
	if r := evalClosed(again(12000), wide); !sameInts(slamBars(r), 39) || r.State != StateLong {
		t.Errorf("slam 39 through both: slams %v state %s, want [39] / long", slamBars(r), r.State)
	}
	if s := slamBars(evalClosed(again(1000), wide)); !sameInts(s, 41) {
		t.Errorf("no slam on 39: slams %v, want [41] (the position's own)", s)
	}
}

// A slam bar that first breaks a FLAT flag that is not yet READY follows its
// EARLY BREAK alone: the break closed the flag before the fail gate, so there
// is no FLAG FAILED, and no position, so no EXIT (emaFlag.test.ts F18 pins the
// same bars).
func TestSlam_AfterAnEarlyBreakAlone(t *testing.T) {
	var bars []core.Bar
	for i := 0; i < 15; i++ {
		c := 9.99
		if i%2 == 1 {
			c = 10.01
		}
		bars = append(bars, xbar(i, 10, 10.02, 9.98, c, 1000))
	}
	bars = append(bars, xbar(15, 10, 11.1, 9.99, 11, 3000), xbar(16, 11, 11.05, 10.9, 10.95, 1200), xbar(17, 10.95, 11.05, 10.9, 11, 1100))
	if r := evalClosed(bars, DefaultParams()); r.State != StateFlat {
		t.Fatalf("through bar 17: state %s, want flat", r.State)
	}
	r := evalClosed(with(bars, xbar(18, 11, 11.2, 9.78, 9.8, 6000)), DefaultParams())
	wantStamps(t, "slam after an early break", r, "15 impulse", "17 flat", "18 earlyBreak", "18 slam")
}

// ...but while LONG the same kind of bar also EXITs: LONG on 27, a second thrust
// to 1.374 (28..30) and a tight ADD flag far above the EMA (31..33), FLAT but
// not READY. Bar 34 pokes its trigger (EARLY BREAK, emitted with the position
// open), closes 8.8% under the EMA (the stop-by-close EXIT) and slams last.
// emaFlag.test.ts F18 pins the same bars.
func TestSlam_AfterAnEarlyBreakWhileLong(t *testing.T) {
	bars := with(slamReady(), xbar(27, 1.19, 1.21, 1.19, 1.205, 4000))
	for k, c := range []float64{1.25, 1.31, 1.37} {
		bars = append(bars, xbar(28+k, c-0.04, c+0.004, c-0.045, c, 6000))
	}
	for k, c := range []float64{1.365, 1.368, 1.366} {
		bars = append(bars, xbar(31+k, c-0.001, c+0.003, c-0.003, c, 2500))
	}
	r33 := evalClosed(bars, DefaultParams())
	if got := stamps(r33); r33.State != StateLong || math.Abs(r33.Trigger-1.378122) > 1e-9 || got[len(got)-1] != "32 flat" {
		t.Fatalf("through bar 33: state %s trigger %v events %v, want long / 1.378122 / ending 32 flat", r33.State, r33.Trigger, got)
	}
	r := evalClosed(with(bars, xbar(34, 1.366, r33.Trigger+0.01, 1.15, 1.16, 20000)), DefaultParams())
	got := stamps(r)
	if len(got) < 3 || strings.Join(got[len(got)-3:], ",") != "34 earlyBreak,34 exit,34 slam" || r.State != StateIdle {
		t.Errorf("bar 34: events %v state %s, want ending 34 earlyBreak, 34 exit, 34 slam / idle", got, r.State)
	}
}

// On every real fixture, both clocks: Slam off is the same run minus its slam
// events, and the result is otherwise identical.
func TestSlam_OffIsTheEngineMinusTheSlams(t *testing.T) {
	fired := 0
	for name, bars := range loadFixtures(t) {
		for _, now := range []int64{bars[len(bars)-1].T + 10*60_000, bars[len(bars)-1].T + 30_000} {
			on := Evaluate(bars, 60_000, now, DefaultParams())
			p := DefaultParams()
			p.Slam = false
			off := Evaluate(bars, 60_000, now, p)
			fired += len(slamsOf(on))
			if !sameEvents(off.Events, withoutSlams(on.Events)) {
				t.Errorf("%s: slam off events differ beyond the slams", name)
			}
			on.Events, off.Events = nil, nil
			off.LastSlamTs, off.LastSlamClose, off.LastSlamPct = on.LastSlamTs, on.LastSlamClose, on.LastSlamPct
			if !sameResult(on, off) {
				t.Errorf("%s: slam off changed the result beyond the slam", name)
			}
		}
	}
	if fired < 8 {
		t.Errorf("only %d slams across the fixtures: the test proves little", fired)
	}
}

func TestAtrSeries_WilderArithmetic(t *testing.T) {
	bars := []core.Bar{
		{H: 2, L: 1, C: 1.5},      // tr = 1 (bar 0 uses its own close)
		{H: 2.5, L: 1.8, C: 2},    // max(0.7, 1.0, 0.3) = 1.0
		{H: 2.1, L: 1.2, C: 1.3},  // max(0.9, 0.1, 0.8) = 0.9
		{H: 1.4, L: 1.35, C: 1.4}, // max(0.05, 0.1, 0.05) = 0.1
	}
	got := atrSeries(bars, 2)
	// the true ranges with float64 (run-time) arithmetic - a Go constant expression would be exact
	b := bars
	tr := func(i int) float64 {
		return math.Max(math.Max(b[i].H-b[i].L, math.Abs(b[i].H-b[i-1].C)), math.Abs(b[i].L-b[i-1].C))
	}
	seed := ((b[0].H - b[0].L) + tr(1)) / 2 // the SMA of the first two
	a2 := (seed*1 + tr(2)) / 2              // then (prev*(period-1) + tr)/period
	a3 := (a2*1 + tr(3)) / 2
	if tr(1) != 1 || math.Abs(tr(2)-0.9) > 1e-12 || math.Abs(tr(3)-0.1) > 1e-12 {
		t.Fatalf("true ranges %v %v %v", tr(1), tr(2), tr(3))
	}
	if !math.IsNaN(got[0]) || got[1] != seed || got[2] != a2 || got[3] != a3 {
		t.Errorf("atr = %v, want [NaN %v %v %v]", got, seed, a2, a3)
	}
}
