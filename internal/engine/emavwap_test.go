package engine

import (
	"math"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

var emvTestVol int64 = 60000

func newEmvTS() (*tickerState, *signals) {
	ts, sg := newSigTS(false, false)
	sg.emv = true
	sg.events = make(chan core.SignalHit, 64)
	// inside the history universe: avg volume 5M, RVOL baseline 1000 shares
	ts.ref.ok, ts.ref.v = true, refVals{avgVol: 5e6, avgVolOK: true, rvolBase: 1000, rvolOK: true}
	return ts, sg
}

// setVwap makes the ticker's session VWAP exactly v.
func setVwap(ts *tickerState, v float64) { ts.scopeVwapNum, ts.scopeVwapDen = v, 1 }

// feedM feeds one closed 1-minute bar starting at start closing at c, the
// session VWAP being vwap when the bar is judged.
func feedM(ts *tickerState, start int64, c, vwap float64) {
	b := core.Bar{T: start, O: c - 1, H: c, L: c - 1, C: c, V: emvTestVol}
	ts.applyAgg(aggOf(b))
	setVwap(ts, vwap) // after the print: applyAgg folds its own price into the VWAP
	ts.tickDildo(b.T+m1Ms+m1Grace, etOff(b.T))
}

// refEma is the reference: SMA of the first 9, then k = 2/10.
func refEma(cl []float64) float64 {
	if len(cl) < 9 {
		return math.NaN()
	}
	e := 0.0
	for _, c := range cl[:9] {
		e += c
	}
	e /= 9
	for _, c := range cl[9:] {
		e = c*0.2 + e*0.8
	}
	return e
}

func TestEmvJudgesOneMinuteBarsAndMatchesTheReferenceEma(t *testing.T) {
	ts, _ := newEmvTS()
	start := etMs(10, 0)
	var cl []float64
	for i := 0; i < 14; i++ {
		c := 50 + float64((i*7)%5) + float64(i)*0.3
		cl = append(cl, c)
		feedM(ts, start+int64(i)*m1Ms, c, 45)
		if i < 8 {
			if ts.d1.ev.emaN != i+1 {
				t.Fatalf("bar %d: emaN %d", i, ts.d1.ev.emaN)
			}
			continue
		}
		if want := refEma(cl); math.Abs(ts.d1.ev.ema-want) > 1e-12 {
			t.Fatalf("bar %d: ema %v want %v", i, ts.d1.ev.ema, want)
		}
	}
}

func warmEmv(ts *tickerState, start int64, c, vwap float64) int64 {
	for i := 0; i < 9; i++ {
		feedM(ts, start+int64(i)*m1Ms, c, vwap)
	}
	return start + 9*m1Ms
}

func TestEmvBullAndBearCrossFireOncePerCross(t *testing.T) {
	ts, sg := newEmvTS()
	next := warmEmv(ts, etMs(10, 0), 99, 100) // EMA 99 under a VWAP of 100: silent, side known
	if got := drainSignals(sg); len(got) != 0 {
		t.Fatalf("warm-up fired %v", got)
	}
	var closes []float64
	for i := 0; i < 9; i++ {
		closes = append(closes, 99)
	}
	var kinds []string
	var bars []int64
	step := func(c float64) {
		closes = append(closes, c)
		feedM(ts, next, c, 100)
		for _, h := range drainSignals(sg) {
			kinds = append(kinds, h.Kind)
			bars = append(bars, h.BarMs)
			if h.BarMs != next || h.Price != c || h.Vwap != 100 {
				t.Fatalf("hit %+v at %d", h, next)
			}
			if want := (refEma(closes)/100 - 1) * 100; math.Abs(h.Dist-want) > 1e-9 || math.Abs(h.Ema-refEma(closes)) > 1e-12 {
				t.Fatalf("hit %+v want dist %v", h, want)
			}
		}
		next += m1Ms
	}
	for i := 0; i < 12; i++ { // the EMA climbs through 100 and stays above it
		step(103)
	}
	if len(kinds) != 1 || kinds[0] != core.KindEmaVwapBull {
		t.Fatalf("bull: %v", kinds)
	}
	first := -1 // the first bar whose reference EMA is 0.05 % above the VWAP
	for i := 9; i <= len(closes); i++ {
		if refEma(closes[:i])/100-1 >= emvBand {
			first = i
			break
		}
	}
	if want := etMs(10, 0) + int64(first-1)*m1Ms; bars[0] != want {
		t.Fatalf("fired on %s want %s", etClock(bars[0]), etClock(want))
	}
	for i := 0; i < 20; i++ { // and back down through it
		step(95)
	}
	if len(kinds) != 2 || kinds[1] != core.KindEmaVwapBear {
		t.Fatalf("bear: %v", kinds)
	}
	for i := 0; i < 20; i++ { // up again: re-armed by the opposite cross
		step(104)
	}
	if len(kinds) != 3 || kinds[2] != core.KindEmaVwapBull {
		t.Fatalf("second bull: %v", kinds)
	}
	r := core.Row{EvBullAge: core.Null, EvBearAge: core.Null, EvEma: core.Null, EvVwap: core.Null, EvDist: core.Null}
	now := next + 30_000
	ts.emvRow(&r, now, todayAnchorMs(now, etOff(now)))
	if r.EvBarVol != float64(emvTestVol) || r.EvBarDvol != float64(emvTestVol)*104 || r.EvSessVol < float64(emvTestVol) {
		t.Fatalf("cross volume row %+v", r)
	}
	if r.EvBullAge != float64((now-ts.d1.ev.bullMs)/1000) || r.EvBearAge <= r.EvBullAge || r.EvVwap != 100 || r.EvDist <= 0 {
		t.Fatalf("row %+v", r)
	}
}

func TestEmvHysteresisBandAndFlicker(t *testing.T) {
	ts, sg := newEmvTS()
	next := warmEmv(ts, etMs(10, 0), 100, 100.1) // EMA 100, VWAP 100.1: below by 0.1 %
	step := func(vwap float64) int {
		feedM(ts, next, 100, vwap) // the close 100 keeps the EMA at 100
		next += m1Ms
		return len(drainSignals(sg))
	}
	if n := step(99.99); n != 0 { // crossed (d = +0.01 %) inside the band: pending
		t.Fatal("fired inside the band")
	}
	if n := step(100.02); n != 0 { // back under before leaving the band: dropped
		t.Fatal("fired on flicker")
	}
	if n := step(99.99); n != 0 {
		t.Fatal("fired inside the band again")
	}
	if n := step(99.9); n != 1 { // d = +0.1 %: beyond the band, fires
		t.Fatalf("expected the bull on leaving the band, got %d", n)
	}
	if n := step(99.9); n != 0 {
		t.Fatal("fired twice")
	}
	// a dip under the VWAP that does not reach the band, then above again: no new bull
	if step(100.01) != 0 || step(99.9) != 0 {
		t.Fatal("re-fired without the opposite cross")
	}
	// and a real bear cross
	if n := step(100.2); n != 1 {
		t.Fatalf("bear: %d", n)
	}
}

func TestEmvNeedsNineBarsAndNeverJudgesABoundaryBar(t *testing.T) {
	ts, sg := newEmvTS()
	start := etMs(10, 0)
	for i := 0; i < 8; i++ { // under 9 bars: no EMA, no row, no cross whatever the VWAP does
		feedM(ts, start+int64(i)*m1Ms, 100, 100+float64(i%2)*5)
	}
	if ts.d1.ev.emaN != 8 || ts.d1.ev.lastMs != 0 || len(drainSignals(sg)) != 0 {
		t.Fatalf("%+v", ts.d1.ev)
	}
	// the bar that ENDS on 09:30 reads a VWAP that is being reset: not judged
	ts, sg = newEmvTS()
	next := warmEmv(ts, etMs(9, 21), 100, 101) // 9 bars: 09:21 .. 09:30
	if next != etMs(9, 30) {
		t.Fatal(next)
	}
	if ts.d1.ev.lastMs != 0 || ts.d1.ev.emaN != 9 {
		t.Fatalf("the 09:29 bar was judged: last %s", etClock(ts.d1.ev.lastMs))
	}
	// the first bar of the regular scope has no previous bar in it: it cannot cross
	feedM(ts, next, 200, 100)
	if len(drainSignals(sg)) != 0 || !ts.d1.ev.prevOK {
		t.Fatalf("%+v", ts.d1.ev)
	}
}

func TestEmvPartObservedBarsFoldButAreNeverJudged(t *testing.T) {
	ts, sg := newEmvTS()
	ts.m5ListenMs = etMs(10, 0) + 30*m1Ms // every bar below was begun before the engine listened
	warmEmv(ts, etMs(10, 0), 99, 100)
	if ts.d1.ev.emaN != 9 {
		t.Fatal("part-observed bars must still fold into the EMA")
	}
	if ts.d1.ev.lastMs != 0 || len(drainSignals(sg)) != 0 {
		t.Fatal("judged a part-observed bar")
	}
}

func TestEmvSwitchedOffRunsNothing(t *testing.T) {
	ts, sg := newSigTS(false, false) // emv off
	warmEmv(ts, etMs(10, 0), 99, 100)
	if ts.d1.ev.emaN != 0 || len(drainSignals(withSignalLog(sg, 4))) != 0 {
		t.Fatal("ran while off")
	}
}

func TestEmvJudgeAllocatesNothing(t *testing.T) {
	ts, sg := newEmvTS()
	sg.events = nil
	next := warmEmv(ts, etMs(10, 0), 99, 100)
	off := etOff(next)
	i := 0
	allocs := testing.AllocsPerRun(200, func() {
		m := int32(next/m1Ms) + int32(i)
		i++
		c := 99.0 + float64(i%7)
		s := d1Slot{m: m, o: c, h: c + 1, l: c - 1, c: c, v: 1000, oTs: int64(m) * m1Ms, cTs: int64(m) * m1Ms}
		setVwap(ts, 100)
		ts.judgeBar(&s, off)
	})
	if allocs != 0 {
		t.Fatalf("allocs %v", allocs)
	}
}

func BenchmarkEmv(b *testing.B) {
	ts, sg := newEmvTS()
	sg.events = nil
	next := warmEmv(ts, etMs(10, 0), 99, 100)
	off := etOff(next)
	setVwap(ts, 100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m := int32(next/m1Ms) + int32(i)
		c := 99.0 + float64(i%7)
		s := d1Slot{m: m, o: c, h: c + 1, l: c - 1, c: c, v: 1000, oTs: int64(m) * m1Ms, cTs: int64(m) * m1Ms}
		ts.judgeBar(&s, off)
	}
}

// runCross drives the standard bull cross and returns the hits written.
func runCross(ts *tickerState, sg *signals, c float64) int {
	next := warmEmv(ts, etMs(10, 0), 99, 100)
	n := 0
	for i := 0; i < 14; i++ {
		feedM(ts, next, c, 100)
		n += len(drainSignals(sg))
		next += m1Ms
	}
	return n
}

func TestEmvHistoryGatedByScanUniverse(t *testing.T) {
	ts, sg := newEmvTS()
	if n := runCross(ts, sg, 103); n != 1 {
		t.Fatalf("in-universe cross wrote %d rows", n)
	}
	// a cross outside the universe waits 10 minutes, then is dropped unconsumed:
	// no row stamp, no alert
	for name, mod := range map[string]func(*tickerState){
		"low price":  func(ts *tickerState) {},
		"low avgVol": func(ts *tickerState) { ts.ref.v.avgVol = 5e5 },
		"low rvol":   func(ts *tickerState) { ts.ref.v.rvolBase = 1e12 },
		"unresolved": func(ts *tickerState) { ts.ref.ok = false },
	} {
		ts, sg := newEmvTS()
		mod(ts)
		c := 103.0
		if name == "low price" {
			c = 20 // EMA/VWAP both rescaled below
		}
		if name == "low price" {
			next := warmEmv(ts, etMs(10, 0), 18, 19)
			n := 0
			for i := 0; i < 12; i++ {
				feedM(ts, next, 22, 19)
				n += len(drainSignals(sg))
				next += m1Ms
			}
			if n != 0 || ts.d1.ev.bullMs != 0 || ts.d1.ev.side == 1 {
				t.Fatalf("%s: rows %d bullMs %d", name, n, ts.d1.ev.bullMs)
			}
			continue
		}
		if n := runCross(ts, sg, c); n != 0 || ts.d1.ev.bullMs != 0 || ts.d1.ev.side == 1 {
			t.Fatalf("%s: rows %d bullMs %d", name, n, ts.d1.ev.bullMs)
		}
	}
	// thin bar: $ volume under 500k
	ts3, sg3 := newEmvTS()
	emvTestVol = 1000
	n := runCross(ts3, sg3, 103)
	emvTestVol = 60000
	if n != 0 {
		t.Fatalf("thin cross wrote %d rows", n)
	}
	// pre-market
	ts4, sg4 := newEmvTS()
	next := warmEmv(ts4, etMs(7, 0), 99, 100)
	n = 0
	for i := 0; i < 12; i++ {
		feedM(ts4, next, 103, 100)
		n += len(drainSignals(sg4))
		next += m1Ms
	}
	if n != 0 {
		t.Fatalf("pre-market cross wrote %d rows", n)
	}
}

// The ENPH 2026-10-28 case: the cross bar is too thin for the universe, a later
// bar is not. The cross waits and fires on that bar, with the row aged from it.
func TestEmvCrossWaitsForTheUniverse(t *testing.T) {
	ts, sg := newEmvTS()
	next := warmEmv(ts, etMs(10, 0), 99, 100)
	emvTestVol = 1000 // $100k bars: outside the universe
	feedM(ts, next, 110, 100) // EMA 101.2: crossed and beyond the band
	next += m1Ms
	if n := len(drainSignals(sg)); n != 0 || ts.d1.ev.bullMs != 0 || ts.d1.ev.waitUp == 0 {
		t.Fatalf("thin cross: %d hits, %+v", n, ts.d1.ev)
	}
	for i := 0; i < 3; i++ {
		feedM(ts, next, 110, 100)
		next += m1Ms
	}
	emvTestVol = 60000
	feedM(ts, next, 110, 100) // in the universe now, EMA still above the band
	hits := drainSignals(sg)
	if len(hits) != 1 || hits[0].Kind != core.KindEmaVwapBull || hits[0].BarMs != next {
		t.Fatalf("waited cross: %+v", hits)
	}
	if ts.d1.ev.bullMs != next+m1Ms || ts.d1.ev.side != 1 || ts.d1.ev.waitUp != 0 {
		t.Fatalf("%+v", ts.d1.ev)
	}
	r := core.Row{EvBullAge: core.Null}
	now := next + m1Ms + 5000
	ts.emvRow(&r, now, todayAnchorMs(now, etOff(now)))
	if r.EvBullAge != 5 || r.EvBarVol != float64(emvTestVol) {
		t.Fatalf("row %+v", r)
	}
	next += m1Ms
	feedM(ts, next, 110, 100)
	if len(drainSignals(sg)) != 0 {
		t.Fatal("fired twice")
	}
}

func TestEmvWaitingCrossDropsAfterTenMinutesUnconsumed(t *testing.T) {
	ts, sg := newEmvTS()
	next := warmEmv(ts, etMs(10, 0), 99, 100)
	emvTestVol = 1000
	defer func() { emvTestVol = 60000 }()
	feedM(ts, next, 110, 100)
	crossEnd := next + m1Ms
	next += m1Ms
	for next+m1Ms-crossEnd <= emvWait { // the whole wait, still thin
		feedM(ts, next, 110, 100)
		next += m1Ms
	}
	if ts.d1.ev.waitUp == 0 || len(drainSignals(sg)) != 0 {
		t.Fatalf("dropped early: %+v", ts.d1.ev)
	}
	emvTestVol = 60000
	feedM(ts, next, 110, 100) // in the universe just after the wait: too late
	if ts.d1.ev.waitUp != 0 || ts.d1.ev.bullMs != 0 || ts.d1.ev.side == 1 || len(drainSignals(sg)) != 0 {
		t.Fatalf("not dropped: %+v", ts.d1.ev)
	}
	// the side was not consumed (still -1 from the warm-up): the dip does not
	// re-fire a bear, the next bull cross does fire
	for i := 0; i < 40; i++ {
		next += m1Ms
		feedM(ts, next, 90, 100)
	}
	for i := 0; i < 40; i++ {
		next += m1Ms
		feedM(ts, next, 110, 100)
	}
	var kinds []string
	for _, h := range drainSignals(sg) {
		kinds = append(kinds, h.Kind)
	}
	if len(kinds) != 1 || kinds[0] != core.KindEmaVwapBull {
		t.Fatalf("after the drop: %v", kinds)
	}
}

func TestEmvWaitingCrossDroppedWhenItFallsBack(t *testing.T) {
	ts, sg := newEmvTS()
	next := warmEmv(ts, etMs(10, 0), 99, 100)
	emvTestVol = 1000
	feedM(ts, next, 110, 100)
	next += m1Ms
	emvTestVol = 60000
	feedM(ts, next, 110, 120) // in the universe, but the EMA is now under the VWAP
	if ts.d1.ev.waitUp != 0 || len(drainSignals(sg)) == 0 && ts.d1.ev.bullMs != 0 {
		t.Fatalf("%+v", ts.d1.ev)
	}
	for _, h := range drainSignals(sg) {
		if h.Kind == core.KindEmaVwapBull {
			t.Fatal("fired a bull after falling back")
		}
	}
}
