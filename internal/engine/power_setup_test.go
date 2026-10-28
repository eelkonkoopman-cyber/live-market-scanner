package engine

import (
	"math"
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// psRun feeds quiet bars from `from` (ET minute-of-day start) at price 1.20,
// then the candle one bar later; prevClose 1.00 gives a 20 % gap off the 1.20
// open. Returns the ticker, the hits and the candle's bar start.
func psRun(from int64, nQuiet int, candle core.Bar, prevClose float32) (*tickerState, []core.SignalHit, int64) {
	ts, sg := newPowerTS()
	ts.prevClose = prevClose
	for _, b := range flatBars1m(from, nQuiet, 1.20) {
		feed1m(ts, b)
	}
	candle.T = from + int64(nQuiet)*m1Ms
	feed1m(ts, candle)
	return ts, drainSignals(sg), candle.T
}

// a +15 % body, 70 % top wick, 200x volume candle off the 1.20 open (pass).
func psCandle() core.Bar { return core.Bar{O: 1.20, H: 1.80, L: 1.20, C: 1.38, V: 200_000} }

func hitOf(hs []core.SignalHit, kind string) (core.SignalHit, bool) {
	for _, h := range hs {
		if h.Kind == kind {
			return h, true
		}
	}
	return core.SignalHit{}, false
}

func TestPowerSetupPreMarketPasses(t *testing.T) {
	ts, hs, at := psRun(etMs(4, 0), 12, psCandle(), 1.00)
	ps, ok := hitOf(hs, core.KindPowerSetup)
	if !ok {
		t.Fatalf("no power setup: %v", kindsOf(hs))
	}
	if pw, _ := hitOf(hs, "power"); pw.PsGap == 0 { // the plain power hit carries the tag values too
		t.Fatalf("plain power hit lacks the filter values: %+v", pw)
	}
	if math.Abs(ps.PsFromOpen-15) > 0.01 || math.Abs(ps.PsGap-20) > 0.01 || ps.PsSlope < 2.1 {
		t.Fatalf("values %+v", ps)
	}
	// plan: entry = the 1m 9EMA at the close, stop x0.94, target +3R
	if ps.PsEntry != ts.d1.ema || math.Abs(ps.PsStop-ps.PsEntry*0.94) > 1e-9 || math.Abs(ps.PsTarget-ps.PsEntry*1.18) > 1e-9 {
		t.Fatalf("plan %+v ema %v", ps, ts.d1.ema)
	}
	r := sigRow(ts, at+m1Ms+5_000)
	if r.PsPass != 1 || r.PsAge != 65 || r.PsEntry != ps.PsEntry || math.Abs(r.PsGap-20) > 0.01 || core.IsNull(r.PsEmaSlope) {
		t.Fatalf("row %+v", r)
	}
}

func TestPowerSetupRegularHoursPasses(t *testing.T) {
	// the stock opens the regular session at 1.20 (gap +20 % vs 1.00), 09:30 bar onward
	ts, hs, _ := psRun(etMs(9, 30), 12, psCandle(), 1.00)
	if _, ok := hitOf(hs, core.KindPowerSetup); !ok {
		t.Fatalf("no power setup: %v", kindsOf(hs))
	}
	if ts.d1.rthOpen != 1.20 || ts.d1.open != 1.20 {
		t.Fatalf("opens %v %v", ts.d1.open, ts.d1.rthOpen)
	}
}

func TestPowerSetupFilterFailuresStillStampValues(t *testing.T) {
	core.PsMinGapPct = 10 // enable the optional gap filter for this test
	t.Cleanup(func() { core.PsMinGapPct = 0 })
	// A: +23 % over the open (and a bigger slope) fails
	c := psCandle()
	c.C, c.H = 1.48, 2.00
	ts, hs, at := psRun(etMs(4, 0), 12, c, 1.00)
	if _, ok := hitOf(hs, core.KindPowerSetup); ok {
		t.Fatal("A should fail")
	}
	r := sigRow(ts, at+m1Ms+5_000)
	if r.PsPass != 0 || r.PsFromOpen < 23 || core.IsNull(r.PsEntry) || core.IsNull(r.PsGap) {
		t.Fatalf("row %+v", r)
	}
	// gap: previous close 1.15 -> +4.3 % fails
	_, hs, _ = psRun(etMs(4, 0), 12, psCandle(), 1.15)
	pw, _ := hitOf(hs, "power")
	if _, ok := hitOf(hs, core.KindPowerSetup); ok || pw.PsGap > 5 {
		t.Fatalf("gap should fail: %+v", pw)
	}
	// slope: an 11 % body moves the EMA 1.98 % over 3 bars (< 2.1)
	c = psCandle()
	c.C, c.H = 1.332, 1.60
	_, hs, _ = psRun(etMs(4, 0), 12, c, 1.00)
	pw, ok := hitOf(hs, "power")
	if !ok {
		t.Fatalf("no power candle: %v", kindsOf(hs))
	}
	if _, ok := hitOf(hs, core.KindPowerSetup); ok || !(pw.PsSlope < 2.1) {
		t.Fatalf("slope should fail: %+v", pw)
	}
	if tag := core.PowerFilterTag(pw.PsFromOpen, pw.PsSlope, pw.PsGap); tag != "A✓ slope✗ gap✓" {
		t.Fatalf("tag %q", tag)
	}
}

func TestPowerSetupUnknownInputsFail(t *testing.T) {
	core.PsMinGapPct = 10
	t.Cleanup(func() { core.PsMinGapPct = 0 })
	// no previous close: the gap is unknown, so no setup, but the other values are stamped
	ts, hs, at := psRun(etMs(4, 0), 12, psCandle(), 0)
	if _, ok := hitOf(hs, core.KindPowerSetup); ok {
		t.Fatal("unknown gap must fail")
	}
	r := sigRow(ts, at+m1Ms+5_000)
	if r.PsPass != 0 || !core.IsNull(r.PsGap) || core.IsNull(r.PsFromOpen) {
		t.Fatalf("row %+v", r)
	}
	// the engine joined mid-day: the first folded bar was part-observed, so the open is unknown
	ts2, sg2 := newPowerTS()
	ts2.prevClose = 1.00
	ts2.m5ListenMs = etMs(4, 5)
	for _, b := range flatBars1m(etMs(4, 0), 12, 1.20) {
		feed1m(ts2, b)
	}
	cd := psCandle()
	cd.T = etMs(4, 12)
	feed1m(ts2, cd)
	if _, ok := hitOf(drainSignals(sg2), core.KindPowerSetup); ok || !ts2.d1.openBad {
		t.Fatal("part-observed open must not pass")
	}
}

func TestPowerSetupNoteTag(t *testing.T) {
	_, hs, _ := psRun(etMs(4, 0), 12, psCandle(), 1.00)
	ps, _ := hitOf(hs, core.KindPowerSetup)
	if tag := core.PowerFilterTag(ps.PsFromOpen, ps.PsSlope, ps.PsGap); !strings.HasPrefix(tag, "A✓ slope✓") {
		t.Fatal(tag)
	}
}

func TestPowerSetupZeroAlloc(t *testing.T) {
	ts, _ := newPowerTS()
	ts.prevClose = 1
	ts.d1.open, ts.d1.emaN, ts.d1.ema = 1.2, 40, 1.3
	ts.d1.emaH = [3]float64{1.29, 1.28, 1.27}
	if n := testing.AllocsPerRun(100, func() { _ = ts.evalPowerSetup(etMs(5, 0), etMs(4, 0), 1.35) }); n != 0 {
		t.Fatalf("allocs %v", n)
	}
}

// Gap filter off (default): a small gap or no previous close still passes, the gap is still stamped.
func TestPowerSetupGapOffByDefault(t *testing.T) {
	ts, hs, at := psRun(etMs(4, 0), 12, psCandle(), 1.15) // gap +4.3 %
	if _, ok := hitOf(hs, core.KindPowerSetup); !ok {
		t.Fatal("gap must not filter by default")
	}
	if r := sigRow(ts, at+m1Ms+5_000); r.PsPass != 1 || r.PsGap > 5 || core.IsNull(r.PsGap) {
		t.Fatalf("row %+v", r)
	}
	_, hs, _ = psRun(etMs(4, 0), 12, psCandle(), 0)
	if _, ok := hitOf(hs, core.KindPowerSetup); !ok {
		t.Fatal("unknown gap must pass while the filter is off")
	}
}

// Inside a quiet window the filters are stamped but nothing fires; the plain power hit says why.
func TestPowerSetupQuietWindowSkips(t *testing.T) {
	ts, hs, at := psRun(etMs(6, 0), 12, psCandle(), 1.00) // 06:12 ET
	if _, ok := hitOf(hs, core.KindPowerSetup); ok {
		t.Fatal("fired inside 05-07 ET")
	}
	pw, _ := hitOf(hs, "power")
	if pw.PsSkip != "05–07 ET window" {
		t.Fatalf("skip %q", pw.PsSkip)
	}
	if r := sigRow(ts, at+m1Ms+5_000); r.PsPass != 2 || core.IsNull(r.PsEntry) {
		t.Fatalf("row %+v", r)
	}
	if _, hs, _ = psRun(etMs(17, 0), 12, psCandle(), 1.00); func() bool { _, ok := hitOf(hs, core.KindPowerSetup); return ok }() {
		t.Fatal("fired inside 16-20 ET")
	}
	core.PsQuietWindows = nil
	t.Cleanup(func() { core.PsQuietWindows, _ = core.ParseQuietWindows("05:00-07:00,16:00-20:00") })
	if _, hs, _ = psRun(etMs(6, 0), 12, psCandle(), 1.00); func() bool { _, ok := hitOf(hs, core.KindPowerSetup); return !ok }() {
		t.Fatal("cleared windows must fire")
	}
}

// A+ grade: the 15 bars before the power candle span <= 5 % (research feat.py rng15).
func TestPowerSetupAplusFlatBase(t *testing.T) {
	ts, hs, at := psRun(etMs(4, 0), 20, psCandle(), 1.00)
	ps, ok := hitOf(hs, core.KindPowerSetup)
	if !ok || core.IsNull(ps.PsBaseRange) || !ps.PsAplus {
		t.Fatalf("flat base must be A+: %+v", ps)
	}
	if r := sigRow(ts, at+m1Ms+5_000); r.PsAplus != 1 || r.PsBaseRange != ps.PsBaseRange {
		t.Fatalf("row %+v", r)
	}
}

func TestPowerSetupBaseRangeWindow(t *testing.T) {
	var d dildo1m
	if !core.IsNull(d.baseRange()) {
		t.Fatal("no bars must be unknown")
	}
	d.push(1, 1.01, 1, 1, 1)
	d.push(1, 1.01, 1, 1, 1)
	if !core.IsNull(d.baseRange()) {
		t.Fatal("2 bars must be unknown (not A+)")
	}
	// an old wide bar, then 15 tight bars: the window is the last 15 only
	d.push(1, 2, 0.5, 1, 1)
	for range 14 {
		d.push(1, 1.02, 1, 1, 1)
	}
	if r := d.baseRange(); math.Abs(r-300) > 1e-9 { // the wide bar is still the 15th
		t.Fatalf("range %v", r)
	}
	d.push(1, 1.04, 1, 1, 1)
	if r := d.baseRange(); math.Abs(r-4) > 1e-9 || !core.PsIsAplus(r) {
		t.Fatalf("range %v", r)
	}
	d.push(1, 1.0501, 1, 1, 1)
	if r := d.baseRange(); core.PsIsAplus(r) {
		t.Fatalf("range %v must not be A+", r)
	}
	ev := psEval{pass: false, baseRange: 1}
	var row core.Row
	ev.stampRow(&row, 0)
	if row.PsAplus != 0 {
		t.Fatal("a failed setup is never A+")
	}
}
