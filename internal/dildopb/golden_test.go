package dildopb

// Golden-vector parity with the TypeScript rule. web/src/signals/dildoPb.golden.test.ts
// runs evaluateDildo / classifyDildo (the rule, dildoPb.ts) over the five real
// fixtures in web/src/signals/fixtures/dildo/ plus the synthetic series written
// into the golden file itself, under five parameter sets x two clocks, and
// commits the output as fixtures/golden/dildopb-golden.json. This test replays
// the SAME inputs through Evaluate / Classify and requires identical events
// (ts, kind, label, note, reason; price and emaDistPct as exact float64 bits
// through JSON) and identical Result fields - so the market scan's alert 1
// (the engine's Classify) and alert 2 (flagscan's Evaluate) say exactly what
// the Signal window's chart markers say.
//
// Regenerate: cd web && UPDATE_GOLDEN=1 npx vitest run src/signals/dildoPb.golden.test.ts

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
	fixturesDir = "../../web/src/signals/fixtures/dildo"
	goldenPath  = "../../web/src/signals/fixtures/golden/dildopb-golden.json"
	regenerate  = "regenerate with: cd web && UPDATE_GOLDEN=1 npx vitest run src/signals/dildoPb.golden.test.ts, then re-run both suites"
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

// same is "the same float64" (NaN equals NaN; JSON writes -0 as 0, so 0 == -0).
func same(got, want float64) bool {
	if math.IsNaN(want) {
		return math.IsNaN(got)
	}
	return got == want
}

type goldenParams struct {
	Enabled      bool    `json:"enabled"`
	MinBodyPct   float64 `json:"minBodyPct"`
	MinBodyX     float64 `json:"minBodyX"`
	MaxTopPct    float64 `json:"maxTopPct"`
	MinVolX      float64 `json:"minVolX"`
	PriorBars    int     `json:"priorBars"`
	MinPriorBars int     `json:"minPriorBars"`
	MinPrice     float64 `json:"minPrice"`
	MinDollarVol float64 `json:"minDollarVol"`
	PbWindowMin  int     `json:"pbWindowMin"`
	TouchPct     float64 `json:"touchPct"`
	HoldPct      float64 `json:"holdPct"`
	Floor        string  `json:"floor"`
	EmaPeriod    int     `json:"emaPeriod"`
}

func (g goldenParams) params() Params {
	return Params{
		Enabled: g.Enabled, MinBodyPct: g.MinBodyPct, MinBodyX: g.MinBodyX, MaxTopPct: g.MaxTopPct, MinVolX: g.MinVolX,
		PriorBars: g.PriorBars, MinPriorBars: g.MinPriorBars, MinPrice: g.MinPrice, MinDollarVol: g.MinDollarVol,
		PbWindowMin: g.PbWindowMin, TouchPct: g.TouchPct, HoldPct: g.HoldPct, Floor: g.Floor, EmaPeriod: g.EmaPeriod,
	}
}

type goldenEvent struct {
	Ts         int64   `json:"ts"`
	Kind       string  `json:"kind"`
	Label      string  `json:"label"`
	Price      gnum    `json:"price"`
	Note       string  `json:"note"`
	Reason     *string `json:"reason"`
	EmaDistPct gnum    `json:"emaDistPct"`
}

type goldenResult struct {
	LastDildoTs      gnum `json:"lastDildoTs"`
	LastDildoBodyPct gnum `json:"lastDildoBodyPct"`
	LastDildoBodyX   gnum `json:"lastDildoBodyX"`
	LastDildoVolX    gnum `json:"lastDildoVolX"`
	LastDildoDvol    gnum `json:"lastDildoDvol"`
	PbState          int  `json:"pbState"`
	TouchTs          gnum `json:"touchTs"`
	PbTs             gnum `json:"pbTs"`
	PbPx             gnum `json:"pbPx"`
	Floor            gnum `json:"floor"`
	DeadlineTs       gnum `json:"deadlineTs"`
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
	SumAbsBody float64 `json:"sumAbsBody"`
	SumVol     float64 `json:"sumVol"`
	N          int     `json:"n"`
	ParamSet   string  `json:"paramSet"`
	Hit        *struct {
		BodyPct   gnum `json:"bodyPct"`
		BodyX     gnum `json:"bodyX"`
		VolX      gnum `json:"volX"`
		TopPct    gnum `json:"topPct"`
		DollarVol gnum `json:"dollarVol"`
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

func loadGolden(t *testing.T) goldenFile {
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

// loadFixtures reads web/src/signals/fixtures/dildo/*.json, keyed by file name without .json.
func loadFixtures(t *testing.T) map[string][]core.Bar {
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
	checkNum(t, id, "emaDistPct", got.EmaDistPct, want.EmaDistPct)
	if n := got.Note(); n != want.Note {
		t.Errorf("%s: note\n got    %q\n golden %q", id, n, want.Note)
	}
}

func checkResult(t *testing.T, id string, r Result, w goldenResult) {
	t.Helper()
	checkTs(t, id, "lastDildoTs", r.LastDildoTs, w.LastDildoTs)
	checkNum(t, id, "lastDildoBodyPct", r.LastDildoBodyPct, w.LastDildoBodyPct)
	checkNum(t, id, "lastDildoBodyX", r.LastDildoBodyX, w.LastDildoBodyX)
	checkNum(t, id, "lastDildoVolX", r.LastDildoVolX, w.LastDildoVolX)
	checkNum(t, id, "lastDildoDvol", r.LastDildoDvol, w.LastDildoDvol)
	if r.PbState != w.PbState {
		t.Errorf("%s pbState: got %d, golden %d", id, r.PbState, w.PbState)
	}
	checkTs(t, id, "touchTs", r.TouchTs, w.TouchTs)
	checkTs(t, id, "pbTs", r.PbTs, w.PbTs)
	checkNum(t, id, "pbPx", r.PbPx, w.PbPx)
	checkNum(t, id, "floor", r.Floor, w.Floor)
	checkTs(t, id, "deadlineTs", r.DeadlineTs, w.DeadlineTs)
}

func TestGoldenCases(t *testing.T) {
	g := loadGolden(t)
	fx := loadFixtures(t)
	if g.TfMs != 60_000 {
		t.Fatalf("golden tfMs = %d", g.TfMs)
	}
	if gp, ok := g.ParamSets["defaults"]; !ok || gp.params() != DefaultParams() {
		t.Fatalf("golden DEFAULT_DILDO_PARAMS %+v != DefaultParams() %+v - %s", gp.params(), DefaultParams(), regenerate)
	}
	// The golden file must cover exactly the fixtures that exist.
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
	states := map[int]int{}
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
		states[r.PbState]++
	}
	for s := PbWatching; s <= PbExpired; s++ {
		if states[s] == 0 {
			t.Errorf("no replay ends in pbState %d", s)
		}
	}
	t.Logf("compared %d replays; final pbStates %v", len(g.Replays), states)
}

func TestGoldenClassify(t *testing.T) {
	g := loadGolden(t)
	hits := 0
	for i, v := range g.Classify {
		id := "classify " + strconv.Itoa(i)
		gp, ok := g.ParamSets[v.ParamSet]
		if !ok {
			t.Fatalf("%s: unknown param set %q", id, v.ParamSet)
		}
		h, ok := Classify(v.O, v.H, v.L, v.C, v.V, v.SumAbsBody, v.SumVol, v.N, gp.params())
		if ok != (v.Hit != nil) {
			t.Errorf("%s: hit %v, golden %v (%+v)", id, ok, v.Hit != nil, v)
			continue
		}
		if !ok {
			continue
		}
		hits++
		checkNum(t, id, "bodyPct", h.BodyPct, v.Hit.BodyPct)
		checkNum(t, id, "bodyX", h.BodyX, v.Hit.BodyX)
		checkNum(t, id, "volX", h.VolX, v.Hit.VolX)
		checkNum(t, id, "topPct", h.TopPct, v.Hit.TopPct)
		checkNum(t, id, "dollarVol", h.DollarVol, v.Hit.DollarVol)
	}
	if hits == 0 || hits == len(g.Classify) {
		t.Errorf("classify vectors are vacuous: %d of %d hit", hits, len(g.Classify))
	}
}

// TestGoldenFixtureExpectations pins the real fixtures' headline events in Go
// too (the TS unit tests pin them on the other side).
func TestGoldenFixtureExpectations(t *testing.T) {
	fx := loadFixtures(t)
	run := func(name string) []string {
		b := fx[name]
		r := Evaluate(b, 60_000, b[len(b)-1].T+600_000, DefaultParams())
		day := etDayKey(b[len(b)-1].T)
		var out []string
		for _, e := range r.Events {
			if etDayKey(e.Ts) == day {
				s := fmtEt(e.Ts) + " " + e.Kind
				if e.Reason != "" {
					s += "/" + e.Reason
				}
				out = append(out, s)
			}
		}
		return out
	}
	want := map[string]string{
		"dildo-mb-2026-08-07":    "05:03 dildo,05:09 dildoCancel/faded,15:08 dildo,15:17 dildoPb,16:51 dildo,17:01 dildoPb",
		"dildo-pdsb-2026-09-08":  "08:30 dildo,08:31 dildo,08:42 dildoCancel/ema,09:30 dildo,09:36 dildoPb",
		"dildo-crmt-2026-07-14":  "09:30 dildo,09:39 dildoPb",
		"dildo-vbio-2026-08-12":  "09:34 dildo,09:45 dildo,09:56 dildo,10:01 dildoCancel/faded",
		"dildo-wvvip-2026-08-26": "04:01 dildo,04:02 dildo,04:09 dildoCancel/floor",
	}
	for name, w := range want {
		if got := strings.Join(run(name), ","); got != w {
			t.Errorf("%s:\n got  %s\n want %s", name, got, w)
		}
	}
}
