package ignition

// Golden parity with web/src/signals/ignition.ts: replays every case of
// web/src/signals/fixtures/golden/ignition-golden.json through Evaluate.
// Regenerate: cd web && UPDATE_GOLDEN=1 npx vitest run src/signals/ignition.golden.test.ts

import (
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

const GoldenPath = "../../web/src/signals/fixtures/golden/ignition-golden.json"

type goldenParams struct {
	Enabled     bool    `json:"enabled"`
	BaseBars    int     `json:"baseBars"`
	MaxBasePct  float64 `json:"maxBasePct"`
	VolBars     int     `json:"volBars"`
	MinVolX     float64 `json:"minVolX"`
	MinBodyPct  float64 `json:"minBodyPct"`
	MaxBodyPct  float64 `json:"maxBodyPct"`
	MinPrice    float64 `json:"minPrice"`
	MinDvol     float64 `json:"minDvol"`
	CooldownMin int     `json:"cooldownMin"`
}

type goldenEvent struct {
	Ts       int64    `json:"ts"`
	Price    float64  `json:"price"`
	BodyPct  float64  `json:"bodyPct"`
	VolX     *float64 `json:"volX"`
	Dvol     float64  `json:"dvol"`
	RangePct float64  `json:"rangePct"`
	BaseHigh float64  `json:"baseHigh"`
	DayHigh  float64  `json:"dayHigh"`
}

type golden struct {
	ParamSets map[string]goldenParams `json:"paramSets"`
	Series    []struct {
		Name string     `json:"name"`
		Bars []core.Bar `json:"bars"`
	} `json:"series"`
	Cases []struct {
		Series string        `json:"series"`
		Params string        `json:"params"`
		Cut    int           `json:"cut"`
		NowMs  int64         `json:"nowMs"`
		Events []goldenEvent `json:"events"`
	} `json:"cases"`
}

func (g goldenParams) params() Params { return Params(g) }

func loadGolden(t testing.TB) golden {
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

func eventEq(e Event, w goldenEvent) bool {
	vx := w.VolX == nil && math.IsInf(e.VolX, 1) || w.VolX != nil && *w.VolX == e.VolX
	return vx && e.Ts == w.Ts && e.Price == w.Price && e.BodyPct == w.BodyPct && e.Dvol == w.Dvol &&
		e.RangePct == w.RangePct && e.BaseHigh == w.BaseHigh && e.DayHigh == w.DayHigh
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
	fired := 0
	for i, c := range g.Cases {
		got := Evaluate(series[c.Series][:c.Cut], 60_000, c.NowMs, g.ParamSets[c.Params].params())
		bad := len(got) != len(c.Events)
		for k := 0; !bad && k < len(got); k++ {
			bad = !eventEq(got[k], c.Events[k])
		}
		if bad {
			t.Fatalf("case %d (%s/%s cut %d): got %+v want %+v", i, c.Series, c.Params, c.Cut, got, c.Events)
		}
		fired += len(got)
	}
	if fired == 0 {
		t.Fatal("nothing fired")
	}
}
