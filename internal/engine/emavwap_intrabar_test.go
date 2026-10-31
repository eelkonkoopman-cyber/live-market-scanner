package engine

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// The live (intrabar) cross: the first print whose EMA-with-the-forming-candle
// crosses the VWAP fires, mid-minute (emavwap.go "INTRABAR firing").

// setVwapFirm pins the VWAP at v with so much weight that the prints of a test
// minute do not move it (applyAgg folds every print into the scope VWAP).
func setVwapFirm(ts *tickerState, v float64) { ts.scopeVwapNum, ts.scopeVwapDen = v*1e15, 1e15 }

// emvLive is a warm ticker (nine bars at c, VWAP vwap) with the live fire ON;
// next is the first minute after the warm-up.
func emvLive(c, vwap float64) (*tickerState, *signals, int64) {
	ts, sg := newEmvTS()
	next := warmEmv(ts, etMs(10, 0), c, vwap)
	sg.emvOff = false
	drainSignals(sg)
	return ts, sg, next
}

// print1 feeds one print of the minute starting at start, sec seconds in.
func print1(ts *tickerState, start int64, sec int, c float64, vol int64, vwap float64) {
	setVwapFirm(ts, vwap)
	ts.applyAgg(core.Agg{Sym: "X", Open: c, High: c, Low: c, Close: c, Vol: vol, StartMs: start + int64(sec)*1000, EndMs: start + int64(sec)*1000 + 1000})
}

// closeMin lets the 1 Hz clock judge the minute at start, the VWAP being vwap.
func closeMin(ts *tickerState, start int64, vwap float64) {
	setVwap(ts, vwap)
	ts.tickDildo(start+m1Ms+m1Grace, etOff(start))
}

func TestEmvIntrabarBullFiresMidMinuteOnceAndTheCloseAddsNothing(t *testing.T) {
	ts, sg, next := emvLive(99.9, 100)
	print1(ts, next, 2, 99.95, emvTestVol, 100) // liveD still < 0
	if got := drainSignals(sg); len(got) != 0 {
		t.Fatalf("fired below the VWAP: %v", got)
	}
	print1(ts, next, 5, 100.6, emvTestVol, 100) // EMA 100.04 > 100: the cross
	got := drainSignals(sg)
	if len(got) != 1 || got[0].Kind != core.KindEmaVwapBull || got[0].BarMs != next || got[0].Price != 100.6 {
		t.Fatalf("intrabar bull: %+v", got)
	}
	if e := &ts.d1.ev; e.side != 1 || e.bullMs != next+6000 || e.crossMs != next+6000 || e.intraMinute != int32(next/m1Ms) {
		t.Fatalf("state %+v", *e)
	}
	r := core.Row{EvBullAge: core.Null, EvBearAge: core.Null, EvEma: core.Null, EvVwap: core.Null, EvDist: core.Null}
	ts.emvRow(&r, next+6000, todayAnchorMs(next, etOff(next)))
	if r.EvBullAge != 0 || r.EvBarDvol != 100.6*float64(2*emvTestVol) {
		t.Fatalf("row %+v", r)
	}
	for sec := 10; sec < 50; sec += 10 { // more prints above: once per minute
		print1(ts, next, sec, 100.7, emvTestVol, 100)
	}
	if got := drainSignals(sg); len(got) != 0 {
		t.Fatalf("refired: %v", got)
	}
	closeMin(ts, next, 100) // closes above (d > 0): the side stays, nothing more
	if got := drainSignals(sg); len(got) != 0 {
		t.Fatalf("the close fired again: %v", got)
	}
	if e := &ts.d1.ev; e.side != 1 || e.intraMinute != 0 {
		t.Fatalf("state after close %+v", *e)
	}
	sg.emvOff = true                 // feedM's pre-print VWAP is not firm: closed-bar feed
	for i := int64(1); i <= 3; i++ { // the next bars, still above, stay silent too
		feedM(ts, next+i*m1Ms, 101, 100)
	}
	if got := drainSignals(sg); len(got) != 0 {
		t.Fatalf("later bars fired: %v", got)
	}
}

func TestEmvIntrabarUndoneCrossRestoresTheSideAndCanFireAgain(t *testing.T) {
	ts, sg, next := emvLive(99.9, 100)
	print1(ts, next, 5, 100.6, emvTestVol, 100)
	if got := drainSignals(sg); len(got) != 1 {
		t.Fatalf("first: %v", got)
	}
	print1(ts, next, 30, 99.0, emvTestVol, 100) // falls back: the minute closes at 99
	closeMin(ts, next, 100)
	if got := drainSignals(sg); len(got) != 0 {
		t.Fatalf("close fired: %v", got)
	}
	if e := &ts.d1.ev; e.side != -1 || e.intraMinute != 0 {
		t.Fatalf("side not restored: %+v", *e)
	}
	n2 := next + m1Ms
	print1(ts, n2, 20, 102, emvTestVol, 100) // a genuine second cross
	got := drainSignals(sg)
	if len(got) != 1 || got[0].Kind != core.KindEmaVwapBull || got[0].BarMs != n2 {
		t.Fatalf("second cross: %v", got)
	}
}

func TestEmvIntrabarUndoneThenClosedBarCrossStillFires(t *testing.T) {
	ts, sg, next := emvLive(99.9, 100)
	print1(ts, next, 5, 100.6, emvTestVol, 100)
	print1(ts, next, 30, 99.0, emvTestVol, 100)
	closeMin(ts, next, 100)
	drainSignals(sg)
	sg.emvOff = true // the later cross by the closed-bar path
	for i := int64(1); i <= 4; i++ {
		feedM(ts, next+i*m1Ms, 104, 100)
	}
	got := drainSignals(sg)
	if len(got) != 1 || got[0].Kind != core.KindEmaVwapBull {
		t.Fatalf("closed-bar fire after an undone intrabar cross: %v", got)
	}
}

func TestEmvIntrabarOutsideTheUniverseFallsBackToTheClosedBarPath(t *testing.T) {
	ts, sg, next := thinLive(99.9, 100, 1000) // thin warm-up bars ($100k): no liquid previous minute to lean on
	print1(ts, next, 5, 100.6, 100, 100) // a thin forming bar: $10k
	if got := drainSignals(sg); len(got) != 0 {
		t.Fatalf("thin bar fired: %v", got)
	}
	closeMin(ts, next, 100) // d +0.04 %: inside the band, pending
	if got := drainSignals(sg); len(got) != 0 {
		t.Fatalf("band: %v", got)
	}
	n2 := next + m1Ms
	print1(ts, n2, 5, 102, emvTestVol, 100) // already above: nothing to cross live
	if got := drainSignals(sg); len(got) != 0 {
		t.Fatalf("live: %v", got)
	}
	closeMin(ts, n2, 100)
	got := drainSignals(sg)
	if len(got) != 1 || got[0].Kind != core.KindEmaVwapBull || got[0].BarMs != next {
		t.Fatalf("closed-bar fallback (stamped with the cross bar): %v", got)
	}
}

func TestEmvIntrabarNeverFiresWarmingUpPartialOrBoundary(t *testing.T) {
	// warm-up: 8 bars only
	ts, sg := newEmvTS()
	sg.emvOff = false
	start := etMs(10, 0)
	for i := 0; i < 8; i++ {
		print1(ts, start+int64(i)*m1Ms, 5, 99.9, emvTestVol, 100)
		closeMin(ts, start+int64(i)*m1Ms, 100)
	}
	print1(ts, start+8*m1Ms, 5, 130, emvTestVol, 100)
	if got := drainSignals(sg); len(got) != 0 {
		t.Fatalf("warm-up fired: %v", got)
	}
	// partial minute: the stream (re)connected inside it
	ts, sg, next := emvLive(99.9, 100)
	sg.streamFrom(next + 30_000)
	print1(ts, next, 40, 100.6, emvTestVol, 100)
	if got := drainSignals(sg); len(got) != 0 {
		t.Fatalf("partial (stream) fired: %v", got)
	}
	ts, sg, next = emvLive(99.9, 100)
	ts.m5ListenMs = next + 1
	print1(ts, next, 40, 100.6, emvTestVol, 100)
	if got := drainSignals(sg); len(got) != 0 {
		t.Fatalf("partial (listen) fired: %v", got)
	}
	// the 09:29 bar ends on the open's scope boundary
	ts, sg = newEmvTS()
	next = warmEmv(ts, etMs(9, 20), 99.9, 100)
	sg.emvOff = false
	drainSignals(sg)
	if next != etMs(9, 29) {
		t.Fatalf("setup: %d", next)
	}
	print1(ts, next, 5, 100.6, emvTestVol, 100)
	if got := drainSignals(sg); len(got) != 0 {
		t.Fatalf("boundary fired: %v", got)
	}
}

func TestEmvIntrabarBearMirrors(t *testing.T) {
	ts, sg, next := emvLive(100.1, 100)
	print1(ts, next, 5, 99.4, emvTestVol, 100) // EMA 99.96 < 100
	got := drainSignals(sg)
	if len(got) != 1 || got[0].Kind != core.KindEmaVwapBear || got[0].BarMs != next || got[0].Dist >= 0 {
		t.Fatalf("intrabar bear: %+v", got)
	}
	print1(ts, next, 20, 99.3, emvTestVol, 100)
	closeMin(ts, next, 100)
	if got := drainSignals(sg); len(got) != 0 {
		t.Fatalf("refired: %v", got)
	}
	if ts.d1.ev.side != -1 {
		t.Fatalf("side %d", ts.d1.ev.side)
	}
	// un-done bear: the minute closes back above
	ts2, sg2, nx := emvLive(100.1, 100)
	print1(ts2, nx, 5, 99.4, emvTestVol, 100)
	print1(ts2, nx, 30, 101, emvTestVol, 100)
	closeMin(ts2, nx, 100)
	drainSignals(sg2)
	if ts2.d1.ev.side != 1 {
		t.Fatalf("bear side not restored: %d", ts2.d1.ev.side)
	}
}

// A cross in the first seconds of a minute, while the previous minute is still
// inside its 3 s grace (unjudged, so the EMA lacks its close), waits for the
// judge; the next print after it fires.
func TestEmvIntrabarWaitsForThePreviousMinuteToBeJudged(t *testing.T) {
	ts, sg, next := emvLive(99.9, 100)
	print1(ts, next, 10, 99.9, emvTestVol, 100) // minute `next`, still below
	n2 := next + m1Ms
	print1(ts, n2, 0, 130, emvTestVol, 100) // first print of the next minute: `next` unjudged
	if got := drainSignals(sg); len(got) != 0 {
		t.Fatalf("fired with an unjudged older minute: %v", got)
	}
	closeMin(ts, next, 100) // the 1 Hz clock judges it (n2 is not due yet)
	print1(ts, n2, 4, 130, emvTestVol, 100)
	got := drainSignals(sg)
	if len(got) != 1 || got[0].BarMs != n2 {
		t.Fatalf("after the judge: %v", got)
	}
}

func TestEmvIntrabarAllocatesNothing(t *testing.T) {
	ts, sg, next := emvLive(99.9, 100)
	sg.events = nil
	off := etOff(next)
	i := int64(0)
	allocs := testing.AllocsPerRun(200, func() {
		st := next + i*m1Ms
		i++
		print1(ts, st, 5, 102, emvTestVol, 100)   // crosses (fires)
		print1(ts, st, 20, 99.0, emvTestVol, 100) // falls back
		setVwap(ts, 100)
		ts.tickDildo(st+m1Ms+m1Grace, off)
	})
	if allocs != 0 {
		t.Fatalf("allocs %v", allocs)
	}
	if ts.d1.ev.bullMs == 0 {
		t.Fatal("never fired in the alloc run")
	}
}

func BenchmarkEmvIntrabarPrint(b *testing.B) {
	ts, sg, next := emvLive(99.9, 100)
	sg.events = nil
	setVwapFirm(ts, 100)
	a := core.Agg{Sym: "X", Open: 99.9, High: 99.9, Low: 99.9, Close: 99.9, Vol: 100, StartMs: next, EndMs: next + 1000}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ts.applyAgg(a)
	}
}

// thinLive is emvLive whose warm-up minutes are thin ($100k each); the last
// warm minute is the one before `next`.
func thinLive(c, vwap float64, warmVol int64) (*tickerState, *signals, int64) {
	old := emvTestVol
	emvTestVol = warmVol
	defer func() { emvTestVol = old }()
	ts, sg, next := emvLive(c, vwap)
	ts.sessVol += 1_000_000 // the day's volume is ample: only the bar's $ is thin
	return ts, sg, next
}

func TestEmvIntrabarUsesThePreviousMinutesDollarVolume(t *testing.T) {
	// previous minute $6M, forming candle $10k: fires at the first crossing print
	ts, sg, next := emvLive(99.9, 100)
	print1(ts, next, 3, 100.6, 100, 100)
	got := drainSignals(sg)
	if len(got) != 1 || got[0].Kind != core.KindEmaVwapBull {
		t.Fatalf("liquid previous minute, thin forming bar: %v", got)
	}
	if blocked(sg) != [3]int64{} {
		t.Fatalf("blocked %v", blocked(sg))
	}
	// previous minute thin ($100k): waits for the forming candle's own $500k
	ts, sg, next = thinLive(99.9, 100, 1000)
	print1(ts, next, 3, 100.6, 100, 100)
	if got := drainSignals(sg); len(got) != 0 {
		t.Fatalf("thin previous minute fired: %v", got)
	}
	print1(ts, next, 54, 100.6, 5000, 100) // forming $ now ~ $510k
	got = drainSignals(sg)
	if len(got) != 1 || got[0].Kind != core.KindEmaVwapBull {
		t.Fatalf("forming candle reaching $500k: %v", got)
	}
	// a gap: the previous judged bar is not the immediately preceding minute
	ts, sg, next = emvLive(99.9, 100)
	n2 := next + 2*m1Ms
	print1(ts, n2, 3, 100.6, 100, 100)
	if got := drainSignals(sg); len(got) != 0 {
		t.Fatalf("stale previous bar used: %v", got)
	}
}

func blocked(sg *signals) [3]int64 {
	return [3]int64{sg.emvBlocked[0].Load(), sg.emvBlocked[1].Load(), sg.emvBlocked[2].Load()}
}

func TestEmvBlockedCountersPerLegAndOncePerSymbolMinute(t *testing.T) {
	// dv leg
	ts, sg, next := thinLive(99.9, 100, 1000)
	for i := 0; i < 5; i++ { // several prints of one minute: one count
		print1(ts, next, 3+i, 100.6, 10, 100)
	}
	if b := blocked(sg); b != [3]int64{1, 0, 0} {
		t.Fatalf("dv leg %v", b)
	}
	// rvol leg: sessVol / rvolBase below 1.5 while price/avgVol/sessVol pass
	ts, sg = newEmvTS()
	ts.ref.v.rvolBase = 1e9 // never passed: not sticky either
	next = warmEmv(ts, etMs(10, 0), 99.9, 100)
	sg.emvOff = false
	drainSignals(sg)
	print1(ts, next, 3, 100.6, 100, 100)
	if b := blocked(sg); b != [3]int64{0, 1, 0} {
		t.Fatalf("rvol leg %v", b)
	}
	// other leg: price under the floor / avg volume
	ts, sg, next = emvLive(99.9, 100)
	ts.ref.v.avgVol = 1000
	print1(ts, next, 3, 100.6, 100, 100)
	if b := blocked(sg); b != [3]int64{0, 0, 1} {
		t.Fatalf("other leg %v", b)
	}
}

func TestEmvBlockNoteLimiter(t *testing.T) {
	var e emvState
	if !e.emvBlockNote(5) || e.emvBlockNote(5) || e.emvBlockNote(5) {
		t.Fatal("first call of a minute passes, repeats do not")
	}
	if !e.emvBlockNote(6) || e.emvBlockNote(6) || !e.emvBlockNote(5) {
		t.Fatal("a new minute passes again")
	}
}

func TestEmvRvolIsStickyInsideARegularScope(t *testing.T) {
	ts, _, next := emvLive(99.9, 100)
	off := etOff(next)
	sc := emvScope(next, off)
	end := next + m1Ms
	ts.ref.v.rvolBase = 1000 // sessVol is far above 1.5 x 1000
	if !ts.emvInUniverseDv(100, 1e6, end, off) {
		t.Fatal("passes at a high RVOL")
	}
	if ts.d1.ev.rvolOKScope != sc {
		t.Fatalf("scope not recorded %d %d", ts.d1.ev.rvolOKScope, sc)
	}
	ts.ref.v.rvolBase = 1e9 // RVOL collapses later in the scope
	if !ts.emvInUniverseDv(100, 1e6, end, off) {
		t.Fatal("still passes after the dip, same scope")
	}
	// next day, same time: re-evaluated
	if ts.emvInUniverseDv(100, 1e6, end+24*3600_000, off) {
		t.Fatal("next scope must be judged afresh")
	}
	// a pass is also noted by the judged-bar fold: the dip before a cross does not forget
	ts2, sg2, nx := emvLive(99.9, 100) // warm-up folds at a good RVOL (base 1000)
	ts2.ref.v.rvolBase = 1e9
	print1(ts2, nx, 3, 100.6, 100, 100)
	got := drainSignals(sg2)
	if len(got) != 1 {
		t.Fatalf("intrabar cross after the dip: %v", got)
	}
}

func TestEmvClosedBarPathUsesStickyRvol(t *testing.T) {
	ts, sg := newEmvTS() // closed-bar only
	next := warmEmv(ts, etMs(10, 0), 99, 100)
	drainSignals(sg)
	ts.ref.v.rvolBase = 1e9 // dips, but the warm-up bars noted the pass
	for i := 0; i < 4; i++ {
		feedM(ts, next, 110, 100)
		next += m1Ms
	}
	got := drainSignals(sg)
	if len(got) != 1 || got[0].Kind != core.KindEmaVwapBull {
		t.Fatalf("closed bar with RVOL dipped: %v", got)
	}
	// a fresh ticker that never passed stays out
	ts2, sg2 := newEmvTS()
	ts2.ref.v.rvolBase = 1e9
	n2 := warmEmv(ts2, etMs(10, 0), 99, 100)
	for i := 0; i < 4; i++ {
		feedM(ts2, n2, 110, 100)
		n2 += m1Ms
	}
	if got := drainSignals(sg2); len(got) != 0 {
		t.Fatalf("never-passed ticker fired: %v", got)
	}
}
