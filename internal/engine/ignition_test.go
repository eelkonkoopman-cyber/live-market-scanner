package engine

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
	"github.com/eelkonkoopman/marketscanner/internal/ignition"
)

func newIgnTS() (*tickerState, *signals) {
	ts, sg := newSigTS(false, false)
	sg.ign = true
	sg.events = make(chan core.SignalHit, 64)
	return ts, sg
}

func loadIgnSeries(t testing.TB) map[string][]core.Bar {
	t.Helper()
	raw, err := os.ReadFile("../../web/src/signals/fixtures/golden/ignition-golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Series []struct {
			Name string     `json:"name"`
			Bars []core.Bar `json:"bars"`
		} `json:"series"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	out := map[string][]core.Bar{}
	for _, s := range g.Series {
		out[s.Name] = s.Bars
	}
	return out
}

func TestIgnitionRingsCoverTheRule(t *testing.T) {
	p := ignition.DefaultParams()
	if ignBaseBars != p.BaseBars || ignVolBars != p.VolBars || earlyRing < ignBaseBars || dildopb.EarlyMinBaseBars > ignBaseBars {
		t.Fatalf("rings %d/%d/%d vs rule %+v", ignBaseBars, ignVolBars, earlyRing, p)
	}
}

// Every golden series through the engine judge yields exactly ignition.Evaluate's
// events (defaults) - the chart's markers and the market alert agree.
func TestIgnitionEngineParityWithTheChartRule(t *testing.T) {
	series := loadIgnSeries(t)
	fired := 0
	for name, bars := range series {
		ts, sg := newIgnTS()
		var got []core.SignalHit
		for i, b := range bars {
			if i > 0 && dildopb.DayStartMs(b.T) != dildopb.DayStartMs(bars[i-1].T) {
				ts.resetSession() // the 04:00 session rollover (Engine's clock) clears yesterday's hod
			}
			feed1m(ts, b)
			got = append(got, drainSignals(sg)...)
		}
		want := ignition.Evaluate(bars, 60_000, bars[len(bars)-1].T+600_000, ignition.DefaultParams())
		if len(got) != len(want) {
			t.Fatalf("%s: engine %d events, chart %d (%+v vs %+v)", name, len(got), len(want), got, want)
		}
		for i, w := range want {
			g := got[i]
			if g.Kind != ignition.KindIgnition || g.BarMs != w.Ts || g.Price != w.Price || g.BodyPct != w.BodyPct ||
				g.VolX != w.VolX || g.DollarVol != w.Dvol || g.BaseRangePct != w.RangePct || g.BaseHigh != w.BaseHigh {
				t.Fatalf("%s #%d: engine %+v chart %+v", name, i, g, w)
			}
		}
		fired += len(want)
	}
	if fired == 0 {
		t.Fatal("no series fired")
	}
}

func TestIgnitionRowFields(t *testing.T) {
	ts, sg := newIgnTS()
	var ign int64
	for _, b := range loadIgnSeries(t)["ncpl"] {
		feed1m(ts, b)
		for _, h := range drainSignals(sg) {
			ign = h.BarMs
		}
	}
	if ign != etMs14(9, 6) {
		t.Fatalf("ignition at %d", ign)
	}
	r := sigRow(ts, ign+120_000)
	if r.IgnAge != 120 || !(r.IgnBodyPct > 5 && r.IgnBodyPct < 5.1) || r.IgnVolX != 15 {
		t.Fatalf("row age %v body %v volX %v", r.IgnAge, r.IgnBodyPct, r.IgnVolX)
	}
	if r := sigRow(ts, ign+86_400_000); !core.IsNull(r.IgnAge) {
		t.Fatalf("stale row %v", r.IgnAge)
	}
}

// One closed traded bar judged by the ignition (quiet base, so the full
// rule runs): O(25), no allocation.
func BenchmarkJudgeIgnition(b *testing.B) {
	ts, sg := newIgnTS()
	sg.events = nil
	base := etMs(10, 0)
	for _, bar := range flatBars1m(base, 40, 1.00) {
		feed1m(ts, bar)
	}
	off := etOff(base)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m := int32(base/m1Ms) + 40 + int32(i)
		s := d1Slot{m: m, o: 1.00, h: 1.03, l: 0.99, c: 1.005, v: 1000, oTs: int64(m) * m1Ms, cTs: int64(m) * m1Ms}
		ts.judgeBar(&s, off)
	}
}

func TestIgnFreshVerdict(t *testing.T) {
	for _, c := range []struct {
		o, pc float64
		want  int8
	}{{1.03, 1, 1}, {1.0, 1, 1}, {0.9, 1, 1}, {1.031, 1, 0}, {1.5, 1, 0}, {1.0, 0, -1}} {
		if got := ignFresh(c.o, c.pc); got != c.want {
			t.Fatalf("ignFresh(%v,%v)=%d want %d", c.o, c.pc, got, c.want)
		}
	}
	if freshMaxDayChgPct != 3 {
		t.Fatal(freshMaxDayChgPct)
	}
}

// The fresh verdict rides on the hit and the row: fresh / not fresh / unknown.
func TestIgnitionFreshHitAndRow(t *testing.T) {
	bars := loadIgnSeries(t)["ncpl"]
	var open float64
	for _, b := range bars {
		if b.T == etMs14(9, 6) {
			open = b.O
		}
	}
	if open == 0 {
		t.Fatal("no ignition bar")
	}
	for _, c := range []struct {
		pc   float64
		want int8
		row  float64
	}{{open / 1.02, 1, 1}, {open / 1.10, 0, 0}, {0, -1, core.Null}} {
		ts, sg := newIgnTS()
		ts.prevClose = float32(c.pc)
		var hit core.SignalHit
		for _, b := range bars {
			feed1m(ts, b)
			for _, h := range drainSignals(sg) {
				hit = h
			}
		}
		if hit.Fresh != c.want {
			t.Fatalf("pc %v: fresh %d want %d", c.pc, hit.Fresh, c.want)
		}
		r := sigRow(ts, hit.BarMs+60_000)
		if (core.IsNull(c.row) && !core.IsNull(r.IgnFresh)) || (!core.IsNull(c.row) && r.IgnFresh != c.row) {
			t.Fatalf("pc %v: row ignFresh %v", c.pc, r.IgnFresh)
		}
	}
}

// Each skip rule skips on its own; a bar passing all four has no reason.
func TestIgnSkipRules(t *testing.T) {
	// passing: open 1.02 vs pc 1 (+2%), close 2, dvol 100k, 20x volume
	if r := ignSkip(1.02, 2, 100_000, 20, 1); r != "" {
		t.Fatalf("pass: %q", r)
	}
	for _, c := range []struct {
		name              string
		o, cl, dvol, volX float64
		pc                float64
		want              string
	}{
		{"S1", 1.072, 2, 100_000, 20, 1, "skip: up 7.2% on the day"},
		{"S1 unknown", 1.02, 2, 100_000, 20, 0, "skip: day change unknown"},
		{"S2", 1.02, 2, 300_000, 20, 1, "skip: $300k candle over $250k"},
		{"S3", 1.02, 1.59, 100_000, 20, 1, "skip: price 1.59 under $1.60"},
		{"S4", 1.02, 2, 100_000, 51, 1, "skip: 51x volume over 50x"},
	} {
		if got := ignSkip(c.o, c.cl, c.dvol, c.volX, c.pc); got != c.want {
			t.Fatalf("%s: %q want %q", c.name, got, c.want)
		}
	}
	// boundaries pass: exactly +3%, $250k, $1.60, 50x
	if r := ignSkip(1.03, 1.60, 250_000, 50, 1); r != "" {
		t.Fatalf("boundary: %q", r)
	}
	if ignSkipMaxDayChgPct != 3 || ignSkipMaxDvol != 250_000 || ignSkipMinPrice != 1.60 || ignSkipMaxVolX != 50 {
		t.Fatal("thresholds")
	}
}

// The skip verdict rides on the hit (Skip) and the row (ignPass).
func TestIgnitionSkipHitAndRow(t *testing.T) {
	bars := loadIgnSeries(t)["ncpl"]
	ts, sg := newIgnTS()
	ts.prevClose = float32(1.0) // NCPL opens far above: S1 (and its $564k candle: S2)
	var hit core.SignalHit
	for _, b := range bars {
		feed1m(ts, b)
		for _, h := range drainSignals(sg) {
			hit = h
		}
	}
	if hit.Kind == "" || !strings.HasPrefix(hit.Skip, "skip: up ") {
		t.Fatalf("hit %+v", hit)
	}
	if r := sigRow(ts, hit.BarMs+60_000); r.IgnPass != 0 {
		t.Fatalf("row ignPass %v", r.IgnPass)
	}
	ts.d1.ig.pass = 1
	if r := sigRow(ts, hit.BarMs+60_000); r.IgnPass != 1 {
		t.Fatalf("row ignPass %v", r.IgnPass)
	}
}
