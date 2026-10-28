package bbs

// Golden parity with web/src/signals/bouncyShort.ts: replays every case of
// web/src/signals/fixtures/golden/bbs-golden.json through Evaluate.
// Regenerate: cd web && UPDATE_GOLDEN=1 npx vitest run src/signals/bouncyShort.golden.test.ts

import (
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

const GoldenPath = "../../web/src/signals/fixtures/golden/bbs-golden.json"

type goldenParams struct {
	Enabled        bool    `json:"enabled"`
	Lookback       int     `json:"lookback"`
	SupportBandPct float64 `json:"supportBandPct"`
	MinTouches     int     `json:"minTouches"`
	MinSepBars     int     `json:"minSepBars"`
	MinBouncePct   float64 `json:"minBouncePct"`
	MinLowerHighs  int     `json:"minLowerHighs"`
	LowerHighPct   float64 `json:"lowerHighPct"`
	BreakPct       float64 `json:"breakPct"`
	FormingNearPct float64 `json:"formingNearPct"`
}

type goldenEvent struct {
	Ts        int64   `json:"ts"`
	Price     float64 `json:"price"`
	Support   float64 `json:"support"`
	Touches   int     `json:"touches"`
	Highs     int     `json:"highs"`
	FirstHigh float64 `json:"firstHigh"`
	LastHigh  float64 `json:"lastHigh"`
}

type golden struct {
	ParamSets map[string]goldenParams `json:"paramSets"`
	Series    []struct {
		Name string     `json:"name"`
		Bars []core.Bar `json:"bars"`
	} `json:"series"`
	Cases []struct {
		Series string `json:"series"`
		Params string `json:"params"`
		Cut    int    `json:"cut"`
		NowMs  int64  `json:"nowMs"`
		Result struct {
			Events  []goldenEvent `json:"events"`
			State   int           `json:"state"`
			Support *float64      `json:"support"`
			LastTs  *int64        `json:"lastTs"`
		} `json:"result"`
	} `json:"cases"`
}

func (g goldenParams) params() Params { return Params(g) }

func loadGolden(t *testing.T) golden {
	t.Helper()
	raw, err := os.ReadFile(GoldenPath)
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestDefaultsMatchGolden(t *testing.T) {
	if got := loadGolden(t).ParamSets["defaults"].params(); got != DefaultParams() {
		t.Fatalf("defaults %+v != DefaultParams %+v", got, DefaultParams())
	}
}

func TestGoldenParity(t *testing.T) {
	g := loadGolden(t)
	series := map[string][]core.Bar{}
	for _, s := range g.Series {
		series[s.Name] = s.Bars
	}
	if len(g.Cases) < 100 {
		t.Fatalf("only %d cases", len(g.Cases))
	}
	for i, c := range g.Cases {
		bars := series[c.Series][:c.Cut]
		r := Evaluate(bars, 60_000, c.NowMs, g.ParamSets[c.Params].params())
		want := c.Result
		bad := len(r.Events) != len(want.Events) || r.State != want.State
		if !bad {
			for k, e := range r.Events {
				w := want.Events[k]
				if e.Ts != w.Ts || e.Price != w.Price || e.Support != w.Support || e.Touches != w.Touches ||
					e.Highs != w.Highs || e.FirstHigh != w.FirstHigh || e.LastHigh != w.LastHigh {
					bad = true
				}
			}
		}
		if want.Support == nil {
			bad = bad || !math.IsNaN(r.Support)
		} else {
			bad = bad || r.Support != *want.Support
		}
		if want.LastTs == nil {
			bad = bad || r.LastTs != 0
		} else {
			bad = bad || r.LastTs != *want.LastTs
		}
		if bad {
			t.Fatalf("case %d (%s/%s cut %d): got %+v want %+v", i, c.Series, c.Params, c.Cut, r, want)
		}
	}
}
