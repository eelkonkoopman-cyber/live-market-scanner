package climax

// Golden-vector parity with the TypeScript rule. web/src/signals/climax.golden.test.ts
// runs evaluateClimax / classifyClimax (the rule, climax.ts) over the five real
// fixtures in web/src/signals/fixtures/climax/ plus the synthetic series written
// into the golden file itself, under five parameter sets x two clocks, and
// commits the output as fixtures/golden/climax-golden.json. This test replays
// the SAME inputs through Evaluate / Classify and requires identical events
// (ts, kind, label, reason; price as exact float64 bits through JSON),
// identical Result fields and identical Classify vectors. The Latch the engine
// runs is the one Evaluate runs, so these vectors test the engine's
// confirmation logic directly. Go ports no note text (no Go consumer reads it).
//
// Regenerate: cd web && UPDATE_GOLDEN=1 npx vitest run src/signals/climax.golden.test.ts

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

const (
	fixturesDir = "../../web/src/signals/fixtures/climax"
	goldenPath  = "../../web/src/signals/fixtures/golden/climax-golden.json"
	regenerate  = "regenerate with: cd web && UPDATE_GOLDEN=1 npx vitest run src/signals/climax.golden.test.ts, then re-run both suites"
)

// gnum is a golden number: null (none), a JSON number, or the strings
// "Infinity" / "-Infinity" / "NaN" the TS test writes for non-finite values.
type gnum struct {
	v    float64
	null bool
}

func (g *gnum) UnmarshalJSON(b []byte) error {
	switch s := string(b); s {
	case "null":
		*g = gnum{v: math.NaN(), null: true}
	case `"Infinity"`:
		*g = gnum{v: math.Inf(1)}
	case `"-Infinity"`:
		*g = gnum{v: math.Inf(-1)}
	case `"NaN"`:
		*g = gnum{v: math.NaN()}
	default:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("golden number %s: %w", s, err)
		}
		*g = gnum{v: f}
	}
	return nil
}

func same(got, want float64) bool {
	if math.IsNaN(want) {
		return math.IsNaN(got)
	}
	return got == want // JSON writes -0 as 0
}

type goldenParams struct {
	Enabled         bool    `json:"enabled"`
	Lookback        int     `json:"lookback"`
	MinDropPct      float64 `json:"minDropPct"`
	MinVolX         float64 `json:"minVolX"`
	RequireMaxVol   bool    `json:"requireMaxVol"`
	MinWickPct      float64 `json:"minWickPct"`
	RequireBelowEma bool    `json:"requireBelowEma"`
	EmaPeriod       int     `json:"emaPeriod"`
	MinPrice        float64 `json:"minPrice"`
	MinDollarVol    float64 `json:"minDollarVol"`
	MuteStartMin    int     `json:"muteStartMin"`
	MuteEndMin      int     `json:"muteEndMin"`
	CoreHoursOnly   bool    `json:"coreHoursOnly"`
	ConfirmBars     int     `json:"confirmBars"`
}

func (g goldenParams) params() Params {
	return Params{
		Enabled: g.Enabled, Lookback: g.Lookback, MinDropPct: g.MinDropPct, MinVolX: g.MinVolX, RequireMaxVol: g.RequireMaxVol,
		MinWickPct: g.MinWickPct, RequireBelowEma: g.RequireBelowEma, EmaPeriod: g.EmaPeriod, MinPrice: g.MinPrice,
		MinDollarVol: g.MinDollarVol, MuteStartMin: g.MuteStartMin, MuteEndMin: g.MuteEndMin, CoreHoursOnly: g.CoreHoursOnly,
		ConfirmBars: g.ConfirmBars,
	}
}

type goldenEvent struct {
	Ts     int64   `json:"ts"`
	Kind   string  `json:"kind"`
	Label  string  `json:"label"`
	Price  gnum    `json:"price"`
	Reason *string `json:"reason"`
}

type goldenResult struct {
	LastClimaxTs gnum `json:"lastClimaxTs"`
	ClimaxLow    gnum `json:"climaxLow"`
	ClimaxMid    gnum `json:"climaxMid"`
	DropPct      gnum `json:"dropPct"`
	VolX         gnum `json:"volX"`
	WickPct      gnum `json:"wickPct"`
	DollarVol    gnum `json:"dollarVol"`
	State        int  `json:"state"`
	Bars         int  `json:"bars"`
	ConfTs       gnum `json:"confTs"`
	ConfPx       gnum `json:"confPx"`
	FailTs       gnum `json:"failTs"`
	EndTs        gnum `json:"endTs"`
}

type goldenCase struct {
	Source   string        `json:"source"`
	Name     string        `json:"name"`
	ParamSet string        `json:"paramSet"`
	Clock    string        `json:"clock"`
	NowMs    int64         `json:"nowMs"`
	Bars     int           `json:"bars"`
	Events   []goldenEvent `json:"events"`
	Result   goldenResult  `json:"result"`
}

type goldenReplay struct {
	Source    string       `json:"source"`
	Name      string       `json:"name"`
	ParamSet  string       `json:"paramSet"`
	Clock     string       `json:"clock"`
	Cut       int          `json:"cut"`
	NowMs     int64        `json:"nowMs"`
	NEvents   int          `json:"nEvents"`
	LastEvent *goldenEvent `json:"lastEvent"`
	Result    goldenResult `json:"result"`
}

type goldenClassify struct {
	O, H, L, C float64
	V          int64
	Pr         struct {
		N      int     `json:"n"`
		Hi     gnum    `json:"hi"`
		Lo     gnum    `json:"lo"`
		SumVol float64 `json:"sumVol"`
		MaxVol int64   `json:"maxVol"`
	} `json:"pr"`
	Ema      gnum   `json:"ema"`
	EtMin    int    `json:"etMin"`
	ParamSet string `json:"paramSet"`
	Hit      *struct {
		DropPct    gnum `json:"dropPct"`
		VolX       gnum `json:"volX"`
		WickPct    gnum `json:"wickPct"`
		DollarVol  gnum `json:"dollarVol"`
		EmaDistPct gnum `json:"emaDistPct"`
		Mid        gnum `json:"mid"`
	} `json:"hit"`
}

type goldenFile struct {
	TfMs      int64                   `json:"tfMs"`
	ParamSets map[string]goldenParams `json:"paramSets"`
	Series    map[string][]core.Bar   `json:"series"`
	Cases     []goldenCase            `json:"cases"`
	Replays   []goldenReplay          `json:"replays"`
	Classify  []goldenClassify        `json:"classify"`
}

func loadGolden(t testing.TB) goldenFile {
	t.Helper()
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("cannot read the golden file %s: %v\n%s", goldenPath, err, regenerate)
	}
	var g goldenFile
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("golden file does not parse: %v", err)
	}
	return g
}

// loadFixtures reads web/src/signals/fixtures/climax/*.json, keyed by file name without .json.
func loadFixtures(t testing.TB) map[string][]core.Bar {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(fixturesDir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures under %s (err %v)", fixturesDir, err)
	}
	out := make(map[string][]core.Bar, len(files))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var fx struct {
			Bars []core.Bar `json:"bars"`
		}
		if err := json.Unmarshal(raw, &fx); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		out[strings.TrimSuffix(filepath.Base(f), ".json")] = fx.Bars
	}
	return out
}

func inputBars(t *testing.T, g goldenFile, fx map[string][]core.Bar, source, name string) []core.Bar {
	t.Helper()
	var b []core.Bar
	var ok bool
	if source == "fixture" {
		b, ok = fx[name]
	} else {
		b, ok = g.Series[name]
	}
	if !ok {
		t.Fatalf("%s %s: no such input - %s", source, name, regenerate)
	}
	return b
}

func checkTs(t *testing.T, id, what string, got int64, want gnum) {
	t.Helper()
	w := int64(0)
	if !want.null {
		w = int64(want.v)
	}
	if got != w {
		t.Errorf("%s %s: got %d, golden %d", id, what, got, w)
	}
}

func checkNum(t *testing.T, id, what string, got float64, want gnum) {
	t.Helper()
	if !same(got, want.v) {
		t.Errorf("%s %s: got %v (%016x), golden %v (%016x)", id, what, got, math.Float64bits(got), want.v, math.Float64bits(want.v))
	}
}

func checkEvent(t *testing.T, id string, got Event, want goldenEvent) {
	t.Helper()
	reason := ""
	if want.Reason != nil {
		reason = *want.Reason
	}
	if got.Ts != want.Ts || got.Kind != want.Kind || got.Label != want.Label || got.Reason != reason {
		t.Errorf("%s: event got {%d %s %q %q}, golden {%d %s %q %q}", id, got.Ts, got.Kind, got.Label, got.Reason, want.Ts, want.Kind, want.Label, reason)
		return
	}
	checkNum(t, id, "price", got.Price, want.Price)
}

func checkResult(t *testing.T, id string, r Result, w goldenResult) {
	t.Helper()
	checkTs(t, id, "lastClimaxTs", r.LastClimaxTs, w.LastClimaxTs)
	checkNum(t, id, "climaxLow", r.ClimaxLow, w.ClimaxLow)
	checkNum(t, id, "climaxMid", r.ClimaxMid, w.ClimaxMid)
	checkNum(t, id, "dropPct", r.DropPct, w.DropPct)
	checkNum(t, id, "volX", r.VolX, w.VolX)
	checkNum(t, id, "wickPct", r.WickPct, w.WickPct)
	checkNum(t, id, "dollarVol", r.DollarVol, w.DollarVol)
	if int(r.State) != w.State || r.Bars != w.Bars {
		t.Errorf("%s: state/bars got %d/%d, golden %d/%d", id, r.State, r.Bars, w.State, w.Bars)
	}
	checkTs(t, id, "confTs", r.ConfTs, w.ConfTs)
	checkNum(t, id, "confPx", r.ConfPx, w.ConfPx)
	checkTs(t, id, "failTs", r.FailTs, w.FailTs)
	checkTs(t, id, "endTs", r.EndTs, w.EndTs)
}

func TestGoldenCases(t *testing.T) {
	g := loadGolden(t)
	fx := loadFixtures(t)
	if g.TfMs != 60_000 {
		t.Fatalf("golden tfMs = %d", g.TfMs)
	}
	if gp, ok := g.ParamSets["defaults"]; !ok || gp.params() != DefaultParams() {
		t.Fatalf("golden DEFAULT_CLIMAX_PARAMS %+v != DefaultParams() %+v - %s", gp.params(), DefaultParams(), regenerate)
	}
	inGolden := map[string]bool{}
	for _, c := range g.Cases {
		if c.Source == "fixture" {
			inGolden[c.Name] = true
		}
	}
	var missing []string
	for name := range fx {
		if !inGolden[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 || len(inGolden) != len(fx) {
		t.Fatalf("fixtures and golden file disagree (fixtures without cases: %v; %d fixtures, %d in golden) - %s", missing, len(fx), len(inGolden), regenerate)
	}
	events := 0
	for _, c := range g.Cases {
		id := c.Name + " | " + c.ParamSet + " | " + c.Clock
		bars := inputBars(t, g, fx, c.Source, c.Name)
		if len(bars) != c.Bars {
			t.Errorf("%s: %d bars, golden was generated from %d - %s", id, len(bars), c.Bars, regenerate)
			continue
		}
		gp, ok := g.ParamSets[c.ParamSet]
		if !ok {
			t.Fatalf("%s: unknown param set", id)
		}
		r := Evaluate(bars, g.TfMs, c.NowMs, gp.params())
		if len(r.Events) != len(c.Events) {
			t.Errorf("%s: %d events, golden %d", id, len(r.Events), len(c.Events))
		}
		for i := 0; i < len(r.Events) && i < len(c.Events); i++ {
			checkEvent(t, id+" event "+strconv.Itoa(i), r.Events[i], c.Events[i])
			events++
		}
		checkResult(t, id, r, c.Result)
	}
	if len(g.Cases) == 0 || events == 0 {
		t.Fatal("no golden cases / events compared")
	}
	t.Logf("compared %d cases, %d events", len(g.Cases), events)
}

func TestGoldenReplays(t *testing.T) {
	g := loadGolden(t)
	fx := loadFixtures(t)
	if len(g.Replays) == 0 {
		t.Fatalf("no replays - %s", regenerate)
	}
	states := map[State]int{}
	for _, rp := range g.Replays {
		id := rp.Name + " | " + rp.ParamSet + " | " + rp.Clock + " | cut " + strconv.Itoa(rp.Cut)
		bars := inputBars(t, g, fx, rp.Source, rp.Name)
		if rp.Cut < 1 || rp.Cut > len(bars) {
			t.Errorf("%s: cut out of range - %s", id, regenerate)
			continue
		}
		r := Evaluate(bars[:rp.Cut], g.TfMs, rp.NowMs, g.ParamSets[rp.ParamSet].params())
		if len(r.Events) != rp.NEvents {
			t.Errorf("%s: %d events, golden %d", id, len(r.Events), rp.NEvents)
		} else if rp.LastEvent != nil {
			checkEvent(t, id+" last event", r.Events[len(r.Events)-1], *rp.LastEvent)
		}
		checkResult(t, id, r, rp.Result)
		states[r.State]++
	}
	for s := StateWatching; s <= StateExpired; s++ {
		if states[s] == 0 {
			t.Errorf("no replay ends in state %d", s)
		}
	}
	t.Logf("compared %d replays; final states %v", len(g.Replays), states)
}

func TestGoldenClassify(t *testing.T) {
	g := loadGolden(t)
	if len(g.Classify) < 150 {
		t.Fatalf("only %d classify vectors - %s", len(g.Classify), regenerate)
	}
	hits := 0
	for i, v := range g.Classify {
		id := "classify " + strconv.Itoa(i)
		gp, ok := g.ParamSets[v.ParamSet]
		if !ok {
			t.Fatalf("%s: unknown param set %q", id, v.ParamSet)
		}
		pr := Priors{N: v.Pr.N, Hi: v.Pr.Hi.v, Lo: v.Pr.Lo.v, SumVol: v.Pr.SumVol, MaxVol: v.Pr.MaxVol}
		h, ok := Classify(v.O, v.H, v.L, v.C, v.V, pr, v.Ema.v, v.EtMin, gp.params())
		if ok != (v.Hit != nil) {
			t.Errorf("%s: hit %v, golden %v (%+v)", id, ok, v.Hit != nil, v)
			continue
		}
		// the prefilter Evaluate runs first must never reject a candle Classify accepts
		if ok && !candleGates(v.O, v.H, v.L, v.C, v.V, v.Ema.v, v.EtMin, gp.params()) {
			t.Errorf("%s: candleGates rejects a climax", id)
		}
		if !ok {
			continue
		}
		hits++
		checkNum(t, id, "dropPct", h.DropPct, v.Hit.DropPct)
		checkNum(t, id, "volX", h.VolX, v.Hit.VolX)
		checkNum(t, id, "wickPct", h.WickPct, v.Hit.WickPct)
		checkNum(t, id, "dollarVol", h.DollarVol, v.Hit.DollarVol)
		checkNum(t, id, "emaDistPct", h.EmaDistPct, v.Hit.EmaDistPct)
		checkNum(t, id, "mid", h.Mid, v.Hit.Mid)
	}
	if hits < 30 || len(g.Classify)-hits < 30 {
		t.Errorf("classify vectors are lopsided: %d of %d hit", hits, len(g.Classify))
	}
	t.Logf("compared %d classify vectors (%d hits)", len(g.Classify), hits)
}

// TestGoldenFixtureExpectations pins the five real fixtures' events in Go too
// (the spec's table; the TS unit tests pin the same on the other side).
func TestGoldenFixtureExpectations(t *testing.T) {
	fx := loadFixtures(t)
	loc := etLocation()
	clock := func(ms int64) string { return timeHM(ms, loc) }
	want := map[string]string{
		"climax-biya-2026-10-07": "11:03 climax,11:04 bottom",
		"climax-lhsw-2026-09-02": "10:12 climax,10:13 bottom,15:17 climax,15:18 climaxFail",
		"climax-clro-2026-08-06": "12:26 climax",
		"climax-armp-2026-09-14": "13:51 climax,13:52 bottom,14:17 climax,14:18 climaxFail",
		"climax-xndu-2026-09-22": "08:50 climax,08:51 climaxFail,08:51 climax,08:53 climaxFail",
	}
	for name, w := range want {
		b := fx[name]
		r := Evaluate(b, 60_000, b[len(b)-1].T+600_000, DefaultParams())
		var got []string
		for _, e := range r.Events {
			got = append(got, clock(e.Ts)+" "+e.Kind)
		}
		if strings.Join(got, ",") != w {
			t.Errorf("%s:\n got  %s\n want %s", name, strings.Join(got, ","), w)
		}
	}
	// CLRO: never confirmed, expired at 12:31 with no event
	b := fx["climax-clro-2026-08-06"]
	if r := Evaluate(b, 60_000, b[len(b)-1].T+600_000, DefaultParams()); r.State != StateExpired || clock(r.EndTs) != "12:31" || r.Bars != 5 {
		t.Errorf("CLRO: state %d end %s bars %d, want expired at 12:31 after 5 bars", r.State, clock(r.EndTs), r.Bars)
	}
	// BIYA: the acceptance trace
	b = fx["climax-biya-2026-10-07"]
	r := Evaluate(b, 60_000, b[len(b)-1].T+600_000, DefaultParams())
	if r.State != StateConfirmed || r.ConfPx != 1.879 || r.ClimaxLow != 1.78 || math.Abs(r.ClimaxMid-1.8691) > 1e-12 ||
		strconv.FormatFloat(r.DropPct, 'f', 2, 64) != "13.59" || strconv.FormatFloat(r.VolX, 'f', 2, 64) != "9.60" {
		t.Errorf("BIYA: %+v", r)
	}
}
