package engine

import (
	"math"
	"sort"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/climax"
	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
)

// The SIGNAL HISTORY hand-off (Engine.SignalHits): every alert the closed-bar
// judge calls - GREEN DILDO, VOLUME CLIMAX, BOTTOM CONFIRMED, CLIMAX FAILED -
// in the chart rule's event vocabulary, for internal/flaglog.

// withSignalLog wires the history channel into a hand-built judge (what
// Options.SignalLog does in New).
func withSignalLog(sg *signals, capacity int) *signals {
	sg.events = make(chan core.SignalHit, capacity)
	return sg
}

func drainSignals(sg *signals) []core.SignalHit {
	var out []core.SignalHit
	for {
		select {
		case h := <-sg.events:
			out = append(out, h)
		default:
			return out
		}
	}
}

func kindsOf(hs []core.SignalHit) []string {
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = etClock(h.BarMs) + " " + h.Kind
	}
	return out
}

func TestSignalLogOptionGatesTheChannel(t *testing.T) {
	for _, c := range []struct {
		name string
		opts Options
		want bool
	}{
		{"both signals off", Options{SignalLog: true}, false},
		{"no signal log", Options{Dildo: true, Climax: true}, false},
		{"dildo + log", Options{Dildo: true, SignalLog: true}, true},
		{"climax + log", Options{Climax: true, SignalLog: true}, true},
	} {
		e := New(c.opts)
		if got := e.SignalHits() != nil; got != c.want {
			t.Errorf("%s: SignalHits wired %v, want %v", c.name, got, c.want)
		}
		if e.SignalDrops() != 0 {
			t.Errorf("%s: drops %d", c.name, e.SignalDrops())
		}
	}
}

// A green dildo is handed to the history with the same bar and measures the
// flagscan hand-off carries, plus its open/close (the floor), price = close.
func TestSignalHitGreenDildo(t *testing.T) {
	ts, sg := newSigTS(true, true)
	withSignalLog(sg, 8)
	start := etMs(10, 0)
	for _, b := range flatBars1m(start, 10, 1.00) {
		feed1m(ts, b)
	}
	d := dildoBar(start + 10*m1Ms)
	feed1m(ts, d)
	hits := drainHits(sg)
	got := drainSignals(sg)
	if len(hits) != 1 || len(got) != 1 {
		t.Fatalf("hits %d, history %v: want one of each", len(hits), kindsOf(got))
	}
	h, s := hits[0], got[0]
	if s.Sym != "X" || s.Kind != dildopb.KindDildo || s.BarMs != d.T || s.Price != d.C || s.O != d.O || s.C != d.C ||
		s.BodyPct != h.BodyPct || s.BodyX != h.BodyX || s.VolX != h.VolX || s.DollarVol != h.DollarVol {
		t.Fatalf("history hit %+v, flagscan hit %+v", s, h)
	}
	// Quiet bars after it: nothing more.
	for _, b := range flatBars1m(start+11*m1Ms, 3, 1.09) {
		feed1m(ts, b)
	}
	if more := drainSignals(sg); len(more) != 0 {
		t.Fatalf("quiet bars called %v", kindsOf(more))
	}
}

// The climax arms (price = its low, with its measures and midpoint); the
// latch's verdict follows: BOTTOM (the confirming close, the climax's low,
// midpoint, start and bar count) or CLIMAX FAILED (the undercutting low). An
// expiry is silent, as on the chart.
func TestSignalHitsClimaxBottomFailAndSilentExpiry(t *testing.T) {
	start := etMs(11, 0)

	ts, sg := newSigTS(false, true)
	withSignalLog(sg, 8)
	c := armClimax(t, ts, start)
	got := drainSignals(sg)
	if len(got) != 1 {
		t.Fatalf("arm: %v", kindsOf(got))
	}
	x := got[0]
	if x.Kind != climax.KindClimax || x.BarMs != c.T || x.Price != 9.00 || x.Low != 9.00 || x.Mid != 9.425 ||
		math.Abs(x.DropPct-(10.3-9)/10.3*100) > 1e-9 || math.Abs(x.VolX-20) > 1e-9 || math.Abs(x.WickPct-0.5/0.85*100) > 1e-6 || x.DollarVol != 190_000 {
		t.Fatalf("climax hit %+v", x)
	}
	feed1m(ts, confirmBar(c.T+m1Ms))
	got = drainSignals(sg)
	if len(got) != 1 {
		t.Fatalf("confirm: %v", kindsOf(got))
	}
	if b := got[0]; b.Kind != climax.KindBottom || b.BarMs != c.T+m1Ms || b.Price != 9.60 || b.Low != 9.00 || b.Mid != 9.425 ||
		b.ClimaxMs != c.T || b.Bars != 1 {
		t.Fatalf("bottom hit %+v", b)
	}

	ts, sg = newSigTS(false, true)
	withSignalLog(sg, 8)
	c = armClimax(t, ts, start)
	drainSignals(sg)
	feed1m(ts, failBar(c.T+3*m1Ms))
	got = drainSignals(sg)
	if len(got) != 1 {
		t.Fatalf("fail: %v", kindsOf(got))
	}
	if f := got[0]; f.Kind != climax.KindFail || f.BarMs != c.T+3*m1Ms || f.Price != 8.99 || f.Low != 9.00 || f.ClimaxMs != c.T || f.Bars != 1 {
		t.Fatalf("fail hit %+v", f)
	}

	ts, sg = newSigTS(false, true)
	withSignalLog(sg, 8)
	c = armClimax(t, ts, start)
	drainSignals(sg)
	for k := int64(1); k <= 5; k++ {
		feed1m(ts, core.Bar{T: c.T + k*m1Ms, O: 9.3, H: 9.4, L: 9.1, C: 9.3, V: 4000})
	}
	if ts.d1.cl.State != climax.StateExpired {
		t.Fatalf("latch %+v, want expired", ts.d1.cl)
	}
	if got := drainSignals(sg); len(got) != 0 {
		t.Fatalf("an expiry must be silent: %v", kindsOf(got))
	}
}

// The bar that confirms the old climax AND is a new one: BOTTOM (for the old
// climax) then CLIMAX, both on that bar - the chart's order.
func TestSignalHitsConfirmAndReArmOnOneBar(t *testing.T) {
	ts, sg := newSigTS(false, true)
	withSignalLog(sg, 8)
	start := etMs(11, 0)
	for _, b := range flatBars1m(start, 10, 10) {
		feed1m(ts, b)
	}
	c := climaxBar(start + 10*m1Ms)
	feed1m(ts, c)
	retest := core.Bar{T: c.T + m1Ms, O: 9.60, H: 9.70, L: 9.00, C: 9.50, V: 25_000}
	feed1m(ts, retest)
	got := drainSignals(sg)
	if k := kindsOf(got); len(k) != 3 || k[0] != "11:10 climax" || k[1] != "11:11 bottom" || k[2] != "11:11 climax" {
		t.Fatalf("history %v, want the 11:10 climax, then BOTTOM and CLIMAX on 11:11", k)
	}
	if got[1].ClimaxMs != c.T || got[1].Price != 9.50 || got[2].Low != 9.00 {
		t.Fatalf("bottom %+v / new climax %+v", got[1], got[2])
	}
}

// Over all five climax fixtures, the history the engine hands over for today
// is EXACTLY climax.Evaluate's event list (kind, bar, price bit for bit) - the
// same identities the chart draws.
func TestSignalHitsClimaxParityWithTheChartRule(t *testing.T) {
	fixtures := loadSigFixtures(t, "climax")
	syms := make([]string, 0, len(fixtures))
	for s := range fixtures {
		syms = append(syms, s)
	}
	sort.Strings(syms)
	for _, sym := range syms {
		fx := fixtures[sym]
		today := fx.todayStart(t)
		want := chartClimaxEvents(fx.Bars, today)
		ts, sg := newSigTS(false, true)
		withSignalLog(sg, 1024)
		for _, b := range fx.Bars {
			feed1m(ts, b)
		}
		var got []climaxEv
		for _, h := range drainSignals(sg) {
			if h.BarMs >= today {
				got = append(got, climaxEv{h.BarMs, h.Kind, h.Price})
			}
		}
		if !sameClimaxEvents(got, want) {
			t.Errorf("%s: history %v, chart %v", sym, got, want)
		}
	}
}

// Over all five dildo fixtures, the history's dildos are exactly the engine's
// alert-1 hits (the parity with the chart rule is TestDildoEngineParityWithTheChartRule).
func TestSignalHitsDildoMatchTheAlertOneHits(t *testing.T) {
	for sym, fx := range loadSigFixtures(t, "dildo") {
		ts, sg := newSigTS(true, false)
		withSignalLog(sg, 1024)
		var hits, hist []int64
		for _, b := range fx.Bars {
			feed1m(ts, b)
			for _, h := range drainHits(sg) {
				hits = append(hits, h.BarMs)
			}
			for _, h := range drainSignals(sg) {
				if h.Kind != dildopb.KindDildo {
					t.Fatalf("%s: climax off, yet %s", sym, h.Kind)
				}
				hist = append(hist, h.BarMs)
			}
		}
		if !equalInt64s(hist, hits) {
			t.Errorf("%s: history %v, alert-1 hits %v", sym, clocks(hist), clocks(hits))
		}
	}
}

// SCANNER_DILDO / SCANNER_CLIMAX: each signal's switch gates its history too.
func TestSignalHitsRespectEachKillSwitch(t *testing.T) {
	start := etMs(11, 0)
	series := func(ts *tickerState) { // TestClimaxAndDildoGatesAreIndependent's
		armClimax(t, ts, start) // 11:10 climax; the $1 bars after it fail it at 11:11
		for _, b := range flatBars1m(start+11*m1Ms, 10, 1.00) {
			feed1m(ts, b)
		}
		clearDayHigh(ts)
		feed1m(ts, dildoBar(start+21*m1Ms))
	}
	count := func(hs []core.SignalHit) (dildo, clim int) {
		for _, h := range hs {
			if h.Kind == dildopb.KindDildo {
				dildo++
			} else {
				clim++
			}
		}
		return
	}
	ts, sg := newSigTS(true, false)
	withSignalLog(sg, 64)
	series(ts)
	if d, c := count(drainSignals(sg)); d != 1 || c != 0 {
		t.Fatalf("climax off: %d dildo, %d climax kinds", d, c)
	}
	ts, sg = newSigTS(false, true)
	withSignalLog(sg, 64)
	series(ts)
	if d, c := count(drainSignals(sg)); d != 0 || c != 2 {
		t.Fatalf("dildo off: %d dildo, %d climax kinds (want the climax and its fail)", d, c)
	}
}

// A full history channel drops (counted) and never blocks the judge; the
// flagscan hand-off is unaffected.
func TestSignalHitsFullChannelDropsWithoutBlocking(t *testing.T) {
	ts, sg := newSigTS(true, true)
	withSignalLog(sg, 1)
	start := etMs(11, 0)
	done := make(chan struct{})
	go func() {
		armClimax(t, ts, start)
		feed1m(ts, failBar(start+11*m1Ms))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a full history channel blocked the judge")
	}
	if sg.evDrops.Load() != 1 || len(drainSignals(sg)) != 1 {
		t.Fatalf("drops %d, want 1 (the first hit still queued)", sg.evDrops.Load())
	}
	if ts.d1.cl.State != climax.StateFailed {
		t.Fatalf("latch %+v: the judge must run on regardless", ts.d1.cl)
	}
}
