package engine

import (
	"math"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/climax"
	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// A textbook climax after flatBars1m(.., 10): a fresh low (9.00 under the
// priors' 9.90) 12.6 % under their 10.30 high, 20x their mean volume and the
// biggest bar, a 59 % lower wick, closing 9.50 under the ~10.1 EMA, $190k.
// Its midpoint is (9.85 + 9.00)/2 = 9.425.
func climaxBar(t int64) core.Bar {
	return core.Bar{T: t, O: 9.80, H: 9.85, L: 9.00, C: 9.50, V: 20_000}
}

// confirmBar holds the 9.00 low and closes 9.60 over the 9.425 midpoint.
func confirmBar(t int64) core.Bar {
	return core.Bar{T: t, O: 9.50, H: 9.70, L: 9.20, C: 9.60, V: 5_000}
}

// failBar undercuts the 9.00 low (and closes over the midpoint: it still FAILS).
func failBar(t int64) core.Bar { return core.Bar{T: t, O: 9.50, H: 9.70, L: 8.99, C: 9.60, V: 5_000} }

// armClimax feeds 10 flat bars from start and the climax bar after them.
func armClimax(t *testing.T, ts *tickerState, start int64) core.Bar {
	t.Helper()
	for _, b := range flatBars1m(start, 10, 10) {
		feed1m(ts, b)
	}
	c := climaxBar(start + 10*m1Ms)
	feed1m(ts, c)
	return c
}

// ─── today's bars ────────────────────────────────────────────────────────

// The climax priors are the last 10 traded bars of the SAME ET day: with 9
// bars today it is not judged, however many of yesterday's sit in the ring;
// with 10 it is. todayN restarts at the ET day change.
func TestClimaxNeedsTenBarsOfTodayNotYesterdays(t *testing.T) {
	yest := time.Date(2026, 10, 6, 19, 40, 0, 0, nyLoc).UnixMilli()
	today := etMs(4, 0)

	ts, _ := newSigTS(false, true)
	for _, b := range flatBars1m(yest, 12, 10) {
		feed1m(ts, b)
	}
	if ts.d1.todayN != 12 {
		t.Fatalf("todayN %d after 12 bars of 10-06", ts.d1.todayN)
	}
	for _, b := range flatBars1m(today, 9, 10) {
		feed1m(ts, b)
	}
	if ts.d1.todayN != 9 || ts.d1.n != judgeRing {
		t.Fatalf("todayN %d (want 9: reset at the ET day change), ring %d", ts.d1.todayN, ts.d1.n)
	}
	feed1m(ts, climaxBar(today+9*m1Ms))
	if ts.d1.cl.State != climax.StateNone {
		t.Fatalf("judged with 9 bars of today: %+v", ts.d1.cl)
	}

	// The control: a 10th bar of today first, then the same candle.
	ts2, _ := newSigTS(false, true)
	for _, b := range flatBars1m(yest, 12, 10) {
		feed1m(ts2, b)
	}
	c := armClimax(t, ts2, today)
	if ts2.d1.cl.State != climax.StateWatching || ts2.d1.cl.BarMs != c.T || ts2.d1.cl.Low != 9.00 {
		t.Fatalf("10 bars of today: latch %+v, want the 04:10 climax watching", ts2.d1.cl)
	}
}

// The part-observed first minute is never a climax prior: todayN restarts
// after it, so the climax waits for 10 FULL bars.
func TestClimaxPartialMinuteRestartsTodayN(t *testing.T) {
	start := etMs(10, 0)
	ts, _ := newSigTS(false, true)
	ts.m5ListenMs = start + 17_000 // listening began 17 s into 10:00
	feed1m(ts, core.Bar{T: start, O: 10, H: 10.3, L: 9.9, C: 10.2, V: 1000})
	if ts.d1.todayN != 0 || ts.d1.n != 1 || ts.d1.emaN != 1 {
		t.Fatalf("partial: todayN %d n %d emaN %d, want 0 / 1 (pushed) / 1 (folded)", ts.d1.todayN, ts.d1.n, ts.d1.emaN)
	}
	for _, b := range flatBars1m(start+m1Ms, 9, 10) {
		feed1m(ts, b)
	}
	feed1m(ts, climaxBar(start+10*m1Ms)) // 10 bars in the ring, but only 9 full ones
	if ts.d1.cl.State != climax.StateNone {
		t.Fatalf("the partial minute counted as a prior: %+v", ts.d1.cl)
	}
	if ts.d1.todayN != 10 {
		t.Fatalf("todayN %d", ts.d1.todayN)
	}
}

// A watching climax never confirms across the ET day: the next day's first
// bar expires it (EndMs 0, no Step).
func TestClimaxWatchingLatchExpiresAtTheDayChange(t *testing.T) {
	ts, _ := newSigTS(false, true)
	c := armClimax(t, ts, time.Date(2026, 10, 6, 19, 40, 0, 0, nyLoc).UnixMilli())
	if ts.d1.cl.State != climax.StateWatching {
		t.Fatalf("no climax armed at 19:50: %+v", ts.d1.cl)
	}
	feed1m(ts, confirmBar(etMs(4, 0))) // would confirm, but it is the next day
	x := ts.d1.cl
	if x.State != climax.StateExpired || x.EndMs != 0 || x.Bars != 0 || x.BarMs != c.T {
		t.Fatalf("latch %+v, want expired at the day change (EndMs 0, no bar stepped)", x)
	}
}

// The judge's own 9 EMA equals the engine's ticker.ema9 after every finalized
// minute on the normal path (prints in order, the 1 Hz clock running): same
// candles, same SMA seed, same arithmetic.
func TestClimaxJudgeEmaPinnedToTickerEma9(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	ts, _ := newSigTS(true, true)
	start := etMs(9, 0) / 1000
	p := 5.0
	compared := 0
	for sec := start; sec < start+400*60; sec++ {
		if rng.Intn(12) == 0 { // a print in about one second in twelve
			p *= 1 + (rng.Float64()-0.5)*0.01
			ts.applyAgg(core.Agg{Sym: "X", Open: p, High: p * 1.001, Low: p * 0.999, Close: p, Vol: 100,
				StartMs: sec * 1000, EndMs: (sec + 1) * 1000})
		}
		ts.syncTo(sec)
		ts.tick1m(sec)
		ts.tickDildo(sec*1000, etOff(sec*1000))
		if int(ts.d1.emaN) == ts.emaN && ts.emaN >= emaPeriod {
			if math.Float64bits(ts.d1.ema) != math.Float64bits(ts.ema9) {
				t.Fatalf("at %s after %d candles: judge EMA %v, ticker.ema9 %v", etClock(sec*1000), ts.emaN, ts.d1.ema, ts.ema9)
			}
			compared++
		}
	}
	if ts.emaN < 300 || compared < 300*55 {
		t.Fatalf("too little compared: %d candles, %d comparisons", ts.emaN, compared)
	}
}

// ─── the gates ───────────────────────────────────────────────────────────

// 09:30-09:34 ET is muted (the calibration's worst slice) under EDT AND EST
// offsets; 09:29 and 09:35 are not.
func TestClimaxMuteUnderEdtAndEst(t *testing.T) {
	for _, day := range []time.Time{
		time.Date(2026, 10, 7, 0, 0, 0, 0, nyLoc), // EDT, UTC-4
		time.Date(2026, 1, 7, 0, 0, 0, 0, nyLoc),  // EST, UTC-5
	} {
		for _, c := range []struct {
			h, m  int
			muted bool
		}{{9, 29, false}, {9, 30, true}, {9, 34, true}, {9, 35, false}} {
			at := day.Add(time.Duration(c.h)*time.Hour + time.Duration(c.m)*time.Minute).UnixMilli()
			ts, sg := newSigTS(false, true)
			sg.etOff.Store(etOff(at))
			armClimax(t, ts, at-10*m1Ms)
			if got := ts.d1.cl.State == climax.StateNone; got != c.muted {
				t.Errorf("%s %02d:%02d (offset %d h): muted %v, want %v", day.Format("2006-01-02"), c.h, c.m, etOff(at)/3_600_000, got, c.muted)
			}
		}
	}
}

// The calibrated floor is the engine's own: close >= $0.50 and candle dollar
// volume >= $50k, both inclusive.
func TestClimaxFloorIsApplied(t *testing.T) {
	start := etMs(11, 0)
	at := start + 10*m1Ms
	for _, c := range []struct {
		name string
		bar  core.Bar
		want bool
	}{
		{"close $0.50, exactly $50k", core.Bar{T: at, O: 0.51, H: 0.512, L: 0.47, C: 0.50, V: 100_000}, true},
		{"$49,999.50", core.Bar{T: at, O: 0.51, H: 0.512, L: 0.47, C: 0.50, V: 99_999}, false},
		{"close $0.49", core.Bar{T: at, O: 0.51, H: 0.512, L: 0.47, C: 0.49, V: 200_000}, false},
	} {
		ts, _ := newSigTS(false, true)
		for _, b := range flatBars1m(start, 10, 0.52) {
			feed1m(ts, b)
		}
		feed1m(ts, c.bar)
		if got := ts.d1.cl.State == climax.StateWatching; got != c.want {
			t.Errorf("%s: climax %v, want %v", c.name, got, c.want)
		}
	}
}

// ─── the confirmation latch ──────────────────────────────────────────────

func TestClimaxConfirmFailAndExpireInTheEngine(t *testing.T) {
	start := etMs(11, 0)

	ts, _ := newSigTS(false, true)
	c := armClimax(t, ts, start)
	feed1m(ts, confirmBar(c.T+m1Ms))
	if x := ts.d1.cl; x.State != climax.StateConfirmed || x.EndMs != c.T+m1Ms || x.ConfPx != 9.60 || x.Bars != 1 {
		t.Fatalf("confirm: %+v", x)
	}

	ts, _ = newSigTS(false, true)
	c = armClimax(t, ts, start)
	feed1m(ts, failBar(c.T+3*m1Ms)) // empty minutes in between do not count
	if x := ts.d1.cl; x.State != climax.StateFailed || x.EndMs != c.T+3*m1Ms || x.Bars != 1 {
		t.Fatalf("fail: %+v", x)
	}

	ts, _ = newSigTS(false, true)
	c = armClimax(t, ts, start)
	hold := func(t int64) core.Bar { return core.Bar{T: t, O: 9.3, H: 9.4, L: 9.1, C: 9.3, V: 4000} } // under the midpoint, over the low
	for k := int64(1); k <= 4; k++ {
		feed1m(ts, hold(c.T+k*m1Ms))
	}
	if ts.d1.cl.State != climax.StateWatching {
		t.Fatalf("4 bars: %+v", ts.d1.cl)
	}
	feed1m(ts, hold(c.T+5*m1Ms))
	if x := ts.d1.cl; x.State != climax.StateExpired || x.EndMs != c.T+5*m1Ms || x.Bars != 5 {
		t.Fatalf("expiry at the 5th bar: %+v", x)
	}
}

// ─── the row ─────────────────────────────────────────────────────────────

func TestClimaxRowFields(t *testing.T) {
	ts, _ := newSigTS(false, true)
	start := etMs(11, 0)
	c := armClimax(t, ts, start)

	r := sigRow(ts, c.T+m1Ms+m1Grace)
	if r.ClimaxAge != 63 || r.ClimaxState != 1 || r.ClimaxLow != 9.00 || r.ClimaxDvol != 190_000 {
		t.Fatalf("watching row: age %v state %v low %v dvol %v", r.ClimaxAge, r.ClimaxState, r.ClimaxLow, r.ClimaxDvol)
	}
	if math.Abs(r.ClimaxDropPct-(10.3-9)/10.3*100) > 1e-9 || math.Abs(r.ClimaxVolX-20) > 1e-9 || math.Abs(r.ClimaxWickPct-0.5/0.85*100) > 1e-6 {
		t.Fatalf("measures: drop %v volX %v wick %v", r.ClimaxDropPct, r.ClimaxVolX, r.ClimaxWickPct)
	}
	if !core.IsNull(r.ClimaxConfAge) || !core.IsNull(r.ClimaxConfPx) {
		t.Fatal("confirmation fields only in state 2")
	}

	cf := confirmBar(c.T + m1Ms)
	feed1m(ts, cf)
	r = sigRow(ts, cf.T+m1Ms+m1Grace)
	if r.ClimaxState != 2 || r.ClimaxAge != 123 || r.ClimaxConfAge != 63 || r.ClimaxConfPx != 9.60 {
		t.Fatalf("confirmed row: state %v age %v confAge %v confPx %v", r.ClimaxState, r.ClimaxAge, r.ClimaxConfAge, r.ClimaxConfPx)
	}
	// +Inf multiple: Null.
	ts.d1.cl.Hit.VolX = math.Inf(1)
	if r := sigRow(ts, cf.T+70_000); !core.IsNull(r.ClimaxVolX) || r.ClimaxState != 2 {
		t.Fatalf("+Inf volX: %v", r.ClimaxVolX)
	}
	// The next day from 04:00: every field Null.
	r = sigRow(ts, time.Date(2026, 10, 8, 4, 0, 0, 0, nyLoc).UnixMilli())
	for name, v := range map[string]float64{
		"climaxAge": r.ClimaxAge, "climaxDropPct": r.ClimaxDropPct, "climaxVolX": r.ClimaxVolX, "climaxWickPct": r.ClimaxWickPct,
		"climaxDvol": r.ClimaxDvol, "climaxLow": r.ClimaxLow, "climaxState": r.ClimaxState, "climaxConfAge": r.ClimaxConfAge, "climaxConfPx": r.ClimaxConfPx,
	} {
		if !core.IsNull(v) {
			t.Errorf("yesterday's climax after 04:00: %s = %v", name, v)
		}
	}

	ts, _ = newSigTS(false, true)
	c = armClimax(t, ts, start)
	feed1m(ts, failBar(c.T+m1Ms))
	if r := sigRow(ts, c.T+3*m1Ms); r.ClimaxState != 3 || !core.IsNull(r.ClimaxConfAge) || !core.IsNull(r.ClimaxConfPx) || r.ClimaxAge != 180 {
		t.Fatalf("failed row: state %v confAge %v confPx %v age %v", r.ClimaxState, r.ClimaxConfAge, r.ClimaxConfPx, r.ClimaxAge)
	}
}

// RV1-SRV-3: one bar that CONFIRMS the watching climax and is itself a new
// climax - a retest of the exact climax low (not under it: no fail; equal to
// the priors' low: fresh) on more volume than the climax bar, closing over the
// old midpoint and under the 9 EMA. The chart marks BOTTOM and CLIMAX on that
// bar (climax.Evaluate: 11:10 climax, 11:11 bottom, 11:11 climax) and the
// latch re-arms on the new climax, exactly as Evaluate does. The ROW must still
// show the bottom until the next bar is judged - climaxState 2 with the
// confirming bar's age and close - or "Bottom Confirmed" (climaxState == 2)
// never fires for the first climax. Its other fields are the new climax's.
// Once the next bar is judged the row is the new latch's again.
func TestClimaxConfirmAndReArmOnOneBarStillShowsTheBottom(t *testing.T) {
	ts, _ := newSigTS(false, true)
	start := etMs(11, 0)
	var bars []core.Bar
	for _, b := range flatBars1m(start, 10, 10) {
		feed1m(ts, b)
		bars = append(bars, b)
	}
	c := climaxBar(start + 10*m1Ms)
	feed1m(ts, c)
	retest := core.Bar{T: c.T + m1Ms, O: 9.60, H: 9.70, L: 9.00, C: 9.50, V: 25_000}
	feed1m(ts, retest)
	bars = append(bars, c, retest)

	var chart []string
	for _, ev := range climax.Evaluate(bars, 60_000, retest.T+2*m1Ms, climax.DefaultParams()).Events {
		chart = append(chart, etClock(ev.Ts)+" "+ev.Kind)
	}
	if got := chart; len(got) != 3 || got[0] != "11:10 climax" || got[1] != "11:11 bottom" || got[2] != "11:11 climax" {
		t.Fatalf("chart events %v, want the 11:10 climax, then BOTTOM and a new CLIMAX on 11:11", got)
	}
	if x := ts.d1.cl; x.State != climax.StateWatching || x.BarMs != retest.T || x.Low != 9.00 {
		t.Fatalf("latch %+v, want re-armed on the 11:11 climax (as Evaluate)", x)
	}

	r := sigRow(ts, retest.T+m1Ms+m1Grace) // 11:12:03, the next judge not yet run
	if r.ClimaxState != 2 || r.ClimaxConfAge != 63 || r.ClimaxConfPx != 9.50 {
		t.Fatalf("row after the confirm + re-arm bar: state %v confAge %v confPx %v, want 2 / 63 / 9.50 (BOTTOM shown)",
			r.ClimaxState, r.ClimaxConfAge, r.ClimaxConfPx)
	}
	if r.ClimaxAge != 63 || r.ClimaxLow != 9.00 || math.Abs(r.ClimaxVolX-25_000/2_900.0) > 1e-9 {
		t.Fatalf("row: age %v low %v volX %v, want the NEW climax's (63 s, 9.00, 25000 / 2900)", r.ClimaxAge, r.ClimaxLow, r.ClimaxVolX)
	}

	// The next bar is judged: the new latch takes it (holding, under its 9.35
	// midpoint) and the row is the new climax's, watching.
	hold := core.Bar{T: retest.T + m1Ms, O: 9.3, H: 9.34, L: 9.1, C: 9.3, V: 4000}
	feed1m(ts, hold)
	r = sigRow(ts, hold.T+m1Ms+m1Grace)
	if r.ClimaxState != 1 || !core.IsNull(r.ClimaxConfAge) || !core.IsNull(r.ClimaxConfPx) || r.ClimaxAge != 123 {
		t.Fatalf("row after the next bar: state %v confAge %v confPx %v age %v, want the new climax watching", r.ClimaxState, r.ClimaxConfAge, r.ClimaxConfPx, r.ClimaxAge)
	}
}

// ─── the env gates ───────────────────────────────────────────────────────

// SCANNER_CLIMAX off: no climax state and Null fields while the dildo still
// judges. SCANNER_DILDO off with the climax on: the climax still judges (the
// ring and the EMA run for either) and no dildo is handed on.
func TestClimaxAndDildoGatesAreIndependent(t *testing.T) {
	start := etMs(11, 0)
	climaxAt, dildoAt := start+10*m1Ms, start+21*m1Ms
	series := func(ts *tickerState) {
		armClimax(t, ts, start) // 11:10 climax; the $1 bars after it fail it at 11:11
		for _, b := range flatBars1m(start+11*m1Ms, 10, 1.00) {
			feed1m(ts, b)
		}
		clearDayHigh(ts)
		feed1m(ts, dildoBar(dildoAt))
	}
	rowAt := dildoAt + 2*m1Ms

	ts, sg := newSigTS(true, false)
	series(ts)
	if ts.d1.cl.State != climax.StateNone {
		t.Fatalf("climax off: latch %+v", ts.d1.cl)
	}
	if hits := drainHits(sg); len(hits) != 1 || ts.d1.latch.barMs != dildoAt {
		t.Fatalf("climax off: the dildo must still judge (hits %+v)", hits)
	}
	if r := sigRow(ts, rowAt); !core.IsNull(r.ClimaxAge) || !core.IsNull(r.ClimaxState) || core.IsNull(r.DildoAge) {
		t.Fatalf("climax off row: climaxAge %v state %v dildoAge %v", r.ClimaxAge, r.ClimaxState, r.DildoAge)
	}

	ts, sg = newSigTS(false, true)
	if sg.hits != nil {
		t.Fatal("dildo off: no hits channel")
	}
	series(ts)
	if ts.d1.latch.barMs != 0 || sg.drops.Load() != 0 {
		t.Fatalf("dildo off: latch %+v drops %d", ts.d1.latch, sg.drops.Load())
	}
	if x := ts.d1.cl; x.BarMs != climaxAt || x.State != climax.StateFailed || x.EndMs != climaxAt+m1Ms {
		t.Fatalf("dildo off: the climax must still judge (armed 11:10, failed 11:11): %+v", x)
	}
	if r := sigRow(ts, rowAt); !core.IsNull(r.DildoAge) || r.ClimaxState != 3 || r.ClimaxAge != float64((rowAt-climaxAt)/1000) {
		t.Fatalf("dildo off row: dildoAge %v climaxState %v climaxAge %v", r.DildoAge, r.ClimaxState, r.ClimaxAge)
	}
}

// ─── parity with the chart rule (climax.Evaluate) over the fixtures ──────

type climaxEv struct {
	ts    int64
	kind  string
	price float64
}

func (e climaxEv) String() string {
	return etClock(e.ts) + " " + e.kind
}

// engineClimaxEvents replays bars through a fresh judge and turns its latch
// transitions into Evaluate's event vocabulary: a newly armed latch is a
// climax (at its low); a watching latch that took this bar and confirmed is a
// bottom (at the confirming close), one that failed a climaxFail (at the
// undercutting low). When the bar both ended the old climax and armed a new
// one (XNDU 08:51), the old latch's verdict is the Step the judge ran on it
// before arming (Latch.Step is the shared, golden-pinned code).
func engineClimaxEvents(t *testing.T, bars []core.Bar) []climaxEv {
	t.Helper()
	ts, _ := newSigTS(false, true)
	var out []climaxEv
	for _, b := range bars {
		prev := ts.d1.cl
		feed1m(ts, b)
		cur := ts.d1.cl
		if prev.State == climax.StateWatching {
			v := cur
			if cur.BarMs != prev.BarMs {
				v = prev
				v.Step(b.T, b.L, b.C, climaxParams)
			}
			switch v.State {
			case climax.StateConfirmed:
				out = append(out, climaxEv{v.EndMs, climax.KindBottom, v.ConfPx})
			case climax.StateFailed:
				out = append(out, climaxEv{v.EndMs, climax.KindFail, b.L})
			}
		}
		if cur.State == climax.StateWatching && cur.BarMs != prev.BarMs {
			out = append(out, climaxEv{cur.BarMs, climax.KindClimax, cur.Low})
		}
	}
	return out
}

func chartClimaxEvents(bars []core.Bar, from int64) []climaxEv {
	res := climax.Evaluate(bars, 60_000, bars[len(bars)-1].T+2*m1Ms, climax.DefaultParams())
	var out []climaxEv
	for _, ev := range res.Events {
		if ev.Ts >= from {
			out = append(out, climaxEv{ev.Ts, ev.Kind, ev.Price})
		}
	}
	return out
}

func sameClimaxEvents(a, b []climaxEv) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ts != b[i].ts || a[i].kind != b[i].kind || math.Float64bits(a[i].price) != math.Float64bits(b[i].price) {
			return false
		}
	}
	return true
}

// For all five climax fixtures, the engine's latch transitions for today are
// EXACTLY climax.Evaluate's events over the chart's bars (previous day +
// today) - fed the previous day and today, AND fed today only (the 03:30 cold
// start, whose EMA is seeded from today's first 9 bars: Decision A).
func TestClimaxEngineParityWithTheChartRule(t *testing.T) {
	fixtures := loadSigFixtures(t, "climax")
	if len(fixtures) != 5 {
		t.Fatalf("%d climax fixtures, want 5", len(fixtures))
	}
	syms := make([]string, 0, len(fixtures))
	for s := range fixtures {
		syms = append(syms, s)
	}
	sort.Strings(syms)
	for _, sym := range syms {
		fx := fixtures[sym]
		today := fx.todayStart(t)
		want := chartClimaxEvents(fx.Bars, today)
		if len(want) == 0 {
			t.Fatalf("%s: the chart finds no climax today", sym)
		}
		var got []climaxEv
		for _, ev := range engineClimaxEvents(t, fx.Bars) {
			if ev.ts >= today {
				got = append(got, ev)
			}
		}
		if !sameClimaxEvents(got, want) {
			t.Errorf("%s prev day + today: engine %v, chart %v", sym, got, want)
		}
		var todays []core.Bar
		for _, b := range fx.Bars {
			if b.T >= today {
				todays = append(todays, b)
			}
		}
		cold := engineClimaxEvents(t, todays)
		if !sameClimaxEvents(cold, want) {
			t.Errorf("%s today only (cold start): engine %v, chart %v", sym, cold, want)
		}
		// The engine's final latch is the chart Result's latest climax of the day,
		// expiry included (CLRO: no event, state 4 at 12:31).
		res := climax.Evaluate(fx.Bars, 60_000, fx.Bars[len(fx.Bars)-1].T+2*m1Ms, climax.DefaultParams())
		ts, _ := newSigTS(false, true)
		for _, b := range todays {
			feed1m(ts, b)
		}
		x := ts.d1.cl
		endMs := x.EndMs
		if x.State == climax.StateWatching {
			endMs = 0
		}
		if x.BarMs != res.LastClimaxTs || x.State != res.State || endMs != res.EndTs || x.Bars != res.Bars ||
			x.Low != res.ClimaxLow || math.Float64bits(x.Hit.DropPct) != math.Float64bits(res.DropPct) {
			t.Errorf("%s final latch %+v, chart result state %d end %s climax %s bars %d", sym, x, res.State, etClock(res.EndTs), etClock(res.LastClimaxTs), res.Bars)
		}
		if sym == "CLRO" && (x.State != climax.StateExpired || etClock(x.EndMs) != "12:31") {
			t.Errorf("CLRO: latch %+v, want expired at 12:31", x)
		}
		t.Logf("%-4s chart %v | engine %v | cold %v", sym, want, got, cold)
	}
}
