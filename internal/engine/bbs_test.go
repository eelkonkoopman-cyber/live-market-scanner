package engine

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/bbs"
	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func newBbsTS() (*tickerState, *signals) {
	ts, sg := newSigTS(false, false)
	sg.bbs = true
	sg.events = make(chan core.SignalHit, 64)
	return ts, sg
}

func loadBbsSeries(t testing.TB) map[string][]core.Bar {
	t.Helper()
	raw, err := os.ReadFile("../../web/src/signals/fixtures/golden/bbs-golden.json")
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

func TestBbsLookbackMatchesTheRule(t *testing.T) {
	if bbsLookback != bbs.DefaultParams().Lookback {
		t.Fatalf("bbsLookback %d != rule %d", bbsLookback, bbs.DefaultParams().Lookback)
	}
}

// Every golden series through the engine judge yields exactly bbs.Evaluate's
// events (defaults) - the chart's markers and the market alert agree.
func TestBbsEngineParityWithTheChartRule(t *testing.T) {
	series := loadBbsSeries(t)
	if len(series) < 5 {
		t.Fatal("too few series")
	}
	fired := 0
	for name, bars := range series {
		ts, sg := newBbsTS()
		var got []core.SignalHit
		for _, b := range bars {
			feed1m(ts, b)
			got = append(got, drainSignals(sg)...)
		}
		want := bbs.Evaluate(bars, 60_000, bars[len(bars)-1].T+600_000, bbs.DefaultParams()).Events
		if len(got) != len(want) {
			t.Fatalf("%s: engine %d events, chart %d", name, len(got), len(want))
		}
		for i, w := range want {
			g := got[i]
			if g.Kind != bbs.KindBbs || g.BarMs != w.Ts || g.Price != w.Price || g.Support != w.Support ||
				g.Touches != w.Touches || g.Highs != w.Highs || g.FirstHigh != w.FirstHigh || g.LastHigh != w.LastHigh {
				t.Fatalf("%s #%d: engine %+v chart %+v", name, i, g, w)
			}
		}
		fired += len(want)
	}
	if fired == 0 {
		t.Fatal("no series fired")
	}
}

func TestBbsRowFields(t *testing.T) {
	bars := loadBbsSeries(t)["aaoi"]
	ts, sg := newBbsTS()
	var breakMs int64
	var formingSeen bool
	for _, b := range bars {
		feed1m(ts, b)
		for _, h := range drainSignals(sg) {
			breakMs = h.BarMs
		}
		r := sigRow(ts, b.T+m1Ms+m1Grace)
		if breakMs == 0 && r.BbsState == bbs.StateForming {
			formingSeen = true
			if r.BbsSupport != 115.85 || !core.IsNull(r.BbsAge) {
				t.Fatalf("forming row %+v", r)
			}
		}
	}
	if !formingSeen || breakMs != etMs14(10, 40) {
		t.Fatalf("forming %v break %d", formingSeen, breakMs)
	}
	r := sigRow(ts, breakMs+120_000)
	if r.BbsState != bbs.StateBroken || r.BbsSupport != 115.85 || r.BbsAge != 120 || !(r.BbsDvol > 100_000) {
		t.Fatalf("broken row: state %v sup %v age %v dvol %v", r.BbsState, r.BbsSupport, r.BbsAge, r.BbsDvol)
	}
	// tomorrow: hidden
	if r := sigRow(ts, breakMs+86_400_000); !core.IsNull(r.BbsState) || !core.IsNull(r.BbsAge) {
		t.Fatalf("stale row %+v", r.BbsState)
	}
}

func etMs14(h, m int) int64 {
	return etMs(h, m) + 7*86_400_000 // 2026-10-07 -> 2026-10-14, both EDT
}

// One closed traded bar judged by the bouncy ball short with a full 90-bar
// window: O(90), no allocation.
func BenchmarkJudgeBbs(b *testing.B) {
	ts, sg := newBbsTS()
	sg.events = nil
	base := etMs(10, 0)
	for _, bar := range flatBars1m(base, 100, 1.00) {
		feed1m(ts, bar)
	}
	off := etOff(base)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m := int32(base/m1Ms) + 100 + int32(i)
		s := d1Slot{m: m, o: 1.00, h: 1.03, l: 0.99, c: 1.005, v: 1000, oTs: int64(m) * m1Ms, cTs: int64(m) * m1Ms}
		ts.judgeBar(&s, off)
	}
}
