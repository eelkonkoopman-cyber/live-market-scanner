package emaflag

// Golden-vector parity with the TypeScript engine. web/src/signals/emaFlag.golden.test.ts
// runs evaluateFlag (the rules, emaFlag.ts) over every 1-minute fixture in
// web/src/signals/fixtures/*.json x seven parameter sets x two clocks (the real
// captures plus the synthetic zz-tie-* ones, which also run their own "ties:"
// parameter set) and commits the result as fixtures/golden/emaflag-golden.json;
// this test replays the SAME bars through Evaluate and requires the same state,
// the same event sequence (ts, kind, label, price within 1e-9 relative) and the
// same levels - the 9 EMA SLAM events and the latest SLAM of the day
// (Result.LastSlamTs / LastSlamClose / LastSlamPct) included; the seventh set,
// "slam loose", lowers every SLAM threshold so about half the fixtures fire one.
// It is the guarantee behind the market scan's promise that a symbol's scan
// state matches what the Signal window shows for it.
//
// Two lists in the file:
//   - cases: the full fixtures, every event compared;
//   - replays: the fixtures TRUNCATED at the moments the machine is mid-flag or
//     mid-position (see the TS test header for why the full runs alone are not
//     enough: they all end idle), compared on state, event count, last event
//     and the numeric levels (trigger, stop, flag bars, EMA, ...).
//
// The zz-tie-* fixtures put a bar exactly ON a rule boundary (a high on the
// trigger, a close on the stop, ...): no real capture ever does, so a `>` written
// as `>=` passed every real case. TestGoldenPinsTheExactTies holds the
// boundaries exact on this side and the outcome to the strict / inclusive rule.
//
// A rule changed in one language and not the other fails here (Go side) or in
// vitest (TS side) until the golden file is regenerated and the port follows.
// Regenerate: cd web && UPDATE_GOLDEN=1 npx vitest run src/signals/emaFlag.golden.test.ts

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

const (
	fixturesDir = "../../web/src/signals/fixtures"
	goldenPath  = "../../web/src/signals/fixtures/golden/emaflag-golden.json"
	regenerate  = "regenerate with: cd web && UPDATE_GOLDEN=1 npx vitest run src/signals/emaFlag.golden.test.ts, then re-run both suites"
)

// The golden file's shapes (camelCase JSON written by the TS test).

type goldenParams struct {
	EmaPeriod        int     `json:"emaPeriod"`
	AtrPeriod        int     `json:"atrPeriod"`
	MinExtPct        float64 `json:"minExtPct"`
	BreakoutLookback int     `json:"breakoutLookback"`
	ImpulseBaseBars  int     `json:"impulseBaseBars"`
	MinFlatBars      int     `json:"minFlatBars"`
	MaxFlatOfMovePct float64 `json:"maxFlatOfMovePct"`
	MaxFlatPct       float64 `json:"maxFlatPct"`
	MaxRetracePct    float64 `json:"maxRetracePct"`
	EmaBreakPct      float64 `json:"emaBreakPct"`
	ReadyDistPct     float64 `json:"readyDistPct"`
	EntryNeedsReady  bool    `json:"entryNeedsReady"`
	BreakBufferPct   float64 `json:"breakBufferPct"`
	StopBufferPct    float64 `json:"stopBufferPct"`
	TrailEma         bool    `json:"trailEma"`
	StopByClose      bool    `json:"stopByClose"`
	MaxAdds          int     `json:"maxAdds"`
	MaxFlagBars      int     `json:"maxFlagBars"`
	Slam             bool    `json:"slam"`
	SlamBelowPct     float64 `json:"slamBelowPct"`
	SlamOpenTolPct   float64 `json:"slamOpenTolPct"`
	SlamBodyAtr      float64 `json:"slamBodyAtr"`
	SlamMinBodyPct   float64 `json:"slamMinBodyPct"`
	SlamCloseInRange float64 `json:"slamCloseInRange"`
	SlamVolX         float64 `json:"slamVolX"`
}

func (g goldenParams) params() Params {
	return Params{
		EmaPeriod: g.EmaPeriod, AtrPeriod: g.AtrPeriod, MinExtPct: g.MinExtPct, BreakoutLookback: g.BreakoutLookback,
		ImpulseBaseBars: g.ImpulseBaseBars, MinFlatBars: g.MinFlatBars, MaxFlatOfMovePct: g.MaxFlatOfMovePct,
		MaxFlatPct: g.MaxFlatPct, MaxRetracePct: g.MaxRetracePct, EmaBreakPct: g.EmaBreakPct, ReadyDistPct: g.ReadyDistPct,
		EntryNeedsReady: g.EntryNeedsReady, BreakBufferPct: g.BreakBufferPct, StopBufferPct: g.StopBufferPct,
		TrailEma: g.TrailEma, StopByClose: g.StopByClose, MaxAdds: g.MaxAdds, MaxFlagBars: g.MaxFlagBars,
		Slam: g.Slam, SlamBelowPct: g.SlamBelowPct, SlamOpenTolPct: g.SlamOpenTolPct, SlamBodyAtr: g.SlamBodyAtr,
		SlamMinBodyPct: g.SlamMinBodyPct, SlamCloseInRange: g.SlamCloseInRange, SlamVolX: g.SlamVolX,
	}
}

type goldenEvent struct {
	Ts    int64   `json:"ts"`
	Kind  string  `json:"kind"`
	Label string  `json:"label"`
	Price float64 `json:"price"`
}

// goldenLevels: a null in the file is a nil pointer here (NaN in Result).
type goldenLevels struct {
	State      string   `json:"state"`
	Ema        *float64 `json:"ema"`
	Trigger    *float64 `json:"trigger"`
	Stop       *float64 `json:"stop"`
	FlagHigh   *float64 `json:"flagHigh"`
	FlagLow    *float64 `json:"flagLow"`
	EntryPrice *float64 `json:"entryPrice"`
	FlagBars   *int     `json:"flagBars"`
	EmaDistPct *float64 `json:"emaDistPct"`
	SlamTs     *int64   `json:"slamTs"` // the latest SLAM of the last ET day (Result.LastSlam*)
	SlamClose  *float64 `json:"slamClose"`
	SlamPct    *float64 `json:"slamPct"`
	HodFlagTs  *int64   `json:"hodFlagTs"` // the latest HOD FLAT bar of the last ET day (Result.LastHodFlagTs)
}

type goldenCase struct {
	Fixture  string        `json:"fixture"`
	ParamSet string        `json:"paramSet"`
	Params   goldenParams  `json:"params"`
	Clock    string        `json:"clock"`
	NowMs    int64         `json:"nowMs"`
	Bars     int           `json:"bars"`
	Events   []goldenEvent `json:"events"`
	goldenLevels
}

type goldenReplay struct {
	Fixture   string       `json:"fixture"`
	ParamSet  string       `json:"paramSet"`
	Clock     string       `json:"clock"`
	Cut       int          `json:"cut"`
	NowMs     int64        `json:"nowMs"`
	NEvents   int          `json:"nEvents"`
	LastEvent *goldenEvent `json:"lastEvent"`
	goldenLevels
}

type goldenFile struct {
	TfMs      int64                   `json:"tfMs"`
	ParamSets map[string]goldenParams `json:"paramSets"`
	Cases     []goldenCase            `json:"cases"`
	Replays   []goldenReplay          `json:"replays"`
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

// loadFixtures reads every web/src/signals/fixtures/*.json (non-recursive: the
// golden/ subdirectory is not a fixture), keyed by file name without .json.
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

// near is "within 1e-9 relative"; exact equality (including both zero) passes.
func near(a, b float64) bool {
	if a == b {
		return true
	}
	return math.Abs(a-b) <= 1e-9*math.Max(math.Abs(a), math.Abs(b))
}

func checkFloat(t *testing.T, id, what string, got float64, want *float64) {
	t.Helper()
	if want == nil {
		if !math.IsNaN(got) {
			t.Errorf("%s %s: got %v, golden has null", id, what, got)
		}
		return
	}
	if math.IsNaN(got) || !near(got, *want) {
		t.Errorf("%s %s: got %v, golden %v", id, what, got, *want)
	}
}

func checkEvent(t *testing.T, id string, got Event, want goldenEvent) {
	t.Helper()
	if got.Ts != want.Ts || got.Kind != want.Kind || got.Label != want.Label || !near(got.Price, want.Price) {
		t.Errorf("%s: event got {%d %s %q %v}, golden {%d %s %q %v}", id, got.Ts, got.Kind, got.Label, got.Price, want.Ts, want.Kind, want.Label, want.Price)
	}
}

func checkLevels(t *testing.T, id string, got Result, want goldenLevels) {
	t.Helper()
	if got.State.String() != want.State {
		t.Errorf("%s: state got %s, golden %s", id, got.State, want.State)
	}
	checkFloat(t, id, "ema", got.Ema, want.Ema)
	checkFloat(t, id, "trigger", got.Trigger, want.Trigger)
	checkFloat(t, id, "stop", got.Stop, want.Stop)
	checkFloat(t, id, "flagHigh", got.FlagHigh, want.FlagHigh)
	checkFloat(t, id, "flagLow", got.FlagLow, want.FlagLow)
	checkFloat(t, id, "entryPrice", got.EntryPrice, want.EntryPrice)
	checkFloat(t, id, "emaDistPct", got.EmaDistPct, want.EmaDistPct)
	wantBars := 0 // null flagBars = no flag = 0
	if want.FlagBars != nil {
		wantBars = *want.FlagBars
	}
	if got.FlagBars != wantBars {
		t.Errorf("%s: flagBars got %d, golden %d", id, got.FlagBars, wantBars)
	}
	var wantSlamTs int64 // null slamTs = no slam that day = 0
	if want.SlamTs != nil {
		wantSlamTs = *want.SlamTs
	}
	if got.LastSlamTs != wantSlamTs {
		t.Errorf("%s: LastSlamTs got %d, golden %d", id, got.LastSlamTs, wantSlamTs)
	}
	checkFloat(t, id, "slamClose", got.LastSlamClose, want.SlamClose)
	checkFloat(t, id, "slamPct", got.LastSlamPct, want.SlamPct)
	var wantHod int64 // null hodFlagTs = no HOD flag that day = 0
	if want.HodFlagTs != nil {
		wantHod = *want.HodFlagTs
	}
	if got.LastHodFlagTs != wantHod {
		t.Errorf("%s: LastHodFlagTs got %d, golden %d", id, got.LastHodFlagTs, wantHod)
	}
}

// checkStateSince verifies StateSinceMs against the result's own events - the
// definition on Result: long = the position's entry bar, ready = the bar the
// EMA caught up on (the latest FLAT or 9 EMA NEAR event), flat = the FLAT bar,
// impulse = a bar of the last day at or after the live impulse's BREAKOUT bar,
// idle = 0. The TS engine has no such field, so this is derived, not golden.
func checkStateSince(t *testing.T, id string, bars []core.Bar, r Result) {
	t.Helper()
	lastOf := func(kinds ...string) (int64, bool) {
		for i := len(r.Events) - 1; i >= 0; i-- {
			for _, k := range kinds {
				if r.Events[i].Kind == k {
					return r.Events[i].Ts, true
				}
			}
		}
		return 0, false
	}
	var want int64
	switch r.State {
	case StateIdle:
		want = 0
	case StateLong:
		w, ok := lastOf(KindEntry)
		if !ok {
			t.Errorf("%s: long but no entry event", id)
			return
		}
		want = w
	case StateReady:
		w, ok := lastOf(KindFlat, KindEmaNear)
		if !ok {
			t.Errorf("%s: ready but no flat/emaNear event", id)
			return
		}
		want = w
	case StateFlat:
		w, ok := lastOf(KindFlat)
		if !ok {
			t.Errorf("%s: flat but no flat event", id)
			return
		}
		want = w
	case StateImpulse:
		imp, ok := lastOf(KindImpulse)
		if !ok {
			t.Errorf("%s: impulse but no impulse event", id)
			return
		}
		isBar := false
		for i := range bars {
			if bars[i].T == r.StateSinceMs {
				isBar = true
				break
			}
		}
		if !isBar || r.StateSinceMs < imp {
			t.Errorf("%s: impulse StateSinceMs %d must be a bar at/after the BREAKOUT bar %d", id, r.StateSinceMs, imp)
		}
		return
	}
	if r.StateSinceMs != want {
		t.Errorf("%s: StateSinceMs got %d, want %d (state %s)", id, r.StateSinceMs, want, r.State)
	}
}

func TestGoldenFullCases(t *testing.T) {
	g := loadGolden(t)
	fx := loadFixtures(t)
	if g.TfMs != 60_000 {
		t.Fatalf("golden tfMs = %d, the port is tested at 60000", g.TfMs)
	}

	// The golden file must cover exactly the fixtures that exist: a fixture added
	// without regenerating would otherwise be silently unchecked here.
	inGolden := map[string]int{}
	for _, c := range g.Cases {
		inGolden[c.Fixture]++
	}
	var missing, stale []string
	for name := range fx {
		if inGolden[name] == 0 {
			missing = append(missing, name)
		}
	}
	for name := range inGolden {
		if _, ok := fx[name]; !ok {
			stale = append(stale, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 || len(stale) > 0 {
		t.Fatalf("fixtures and golden file disagree (fixtures without golden cases: %v; golden cases without a fixture: %v) - %s", missing, stale, regenerate)
	}

	compared, events := 0, 0
	for _, c := range g.Cases {
		id := c.Fixture + " | " + c.ParamSet + " | " + c.Clock
		bars := fx[c.Fixture]
		if len(bars) != c.Bars {
			t.Errorf("%s: fixture has %d bars, golden was generated from %d - %s", id, len(bars), c.Bars, regenerate)
			continue
		}
		if c.ParamSet == "defaults" && c.Params.params() != DefaultParams() {
			t.Errorf("%s: golden DEFAULT_FLAG_PARAMS %+v != DefaultParams() %+v", id, c.Params.params(), DefaultParams())
		}
		r := Evaluate(bars, g.TfMs, c.NowMs, c.Params.params())
		compared++
		if len(r.Events) != len(c.Events) {
			t.Errorf("%s: %d events, golden %d", id, len(r.Events), len(c.Events))
		}
		for i := 0; i < len(r.Events) && i < len(c.Events); i++ {
			checkEvent(t, id+" event "+strconv.Itoa(i), r.Events[i], c.Events[i])
			events++
		}
		checkLevels(t, id, r, c.goldenLevels)
		checkStateSince(t, id, bars, r)
	}
	if compared == 0 {
		t.Fatal("no golden cases compared")
	}
	t.Logf("compared %d full cases (%d fixtures x param sets x clocks), %d events", compared, len(fx), events)
}

func TestGoldenReplays(t *testing.T) {
	g := loadGolden(t)
	fx := loadFixtures(t)
	if len(g.Replays) == 0 {
		t.Fatalf("golden file has no replays - %s", regenerate)
	}
	byState := map[string]int{}
	for _, rp := range g.Replays {
		id := rp.Fixture + " | " + rp.ParamSet + " | " + rp.Clock + " | cut " + strconv.Itoa(rp.Cut)
		bars := fx[rp.Fixture]
		if rp.Cut < 1 || rp.Cut > len(bars) {
			t.Errorf("%s: cut out of range for a fixture of %d bars - %s", id, len(bars), regenerate)
			continue
		}
		gp, ok := g.ParamSets[rp.ParamSet]
		if !ok {
			t.Fatalf("%s: unknown param set", id)
		}
		slice := bars[:rp.Cut]
		r := Evaluate(slice, g.TfMs, rp.NowMs, gp.params())
		if len(r.Events) != rp.NEvents {
			t.Errorf("%s: %d events, golden %d", id, len(r.Events), rp.NEvents)
		} else if rp.LastEvent != nil {
			checkEvent(t, id+" last event", r.Events[len(r.Events)-1], *rp.LastEvent)
		}
		checkLevels(t, id, r, rp.goldenLevels)
		checkStateSince(t, id, slice, r)
		byState[r.State.String()]++
	}
	// A replay list that never leaves idle would pass vacuously.
	for _, s := range []string{"impulse", "flat", "ready", "long"} {
		if byState[s] == 0 {
			t.Errorf("no replay ends in state %s", s)
		}
	}
	t.Logf("compared %d replays; final states %v", len(g.Replays), byState)
}

// TestGoldenSlam is the vacuity guard for the 9 EMA SLAM: parity over cases
// that never slam would prove nothing. The golden file must carry SPAI
// 2026-10-07's reference slam under the defaults - exactly one, the 08:15 ET
// bar, at its close 4.566, after that bar's FLAG FAILED - and this port must
// report it on Result.LastSlam*; the "slam loose" set must fire on most of the
// corpus.
func TestGoldenSlam(t *testing.T) {
	g := loadGolden(t)
	fx := loadFixtures(t)
	slam0815 := time.Date(2026, 10, 7, 8, 15, 0, 0, etLocation()).UnixMilli()
	for _, clock := range []string{"closed", "forming"} {
		var c *goldenCase
		for i := range g.Cases {
			if g.Cases[i].Fixture == "spai-2026-10-07" && g.Cases[i].ParamSet == "defaults" && g.Cases[i].Clock == clock {
				c = &g.Cases[i]
			}
		}
		if c == nil {
			t.Fatalf("golden file has no SPAI / defaults / %s case - %s", clock, regenerate)
		}
		var slams []goldenEvent
		for k, e := range c.Events {
			if e.Kind == KindSlam {
				slams = append(slams, e)
				if k == 0 || c.Events[k-1].Kind != KindFailed || c.Events[k-1].Ts != e.Ts {
					t.Errorf("SPAI %s: the SLAM must follow the same bar's FLAG FAILED", clock)
				}
			}
		}
		if len(slams) != 1 || slams[0].Ts != slam0815 || slams[0].Label != LabelSlam || slams[0].Price != 4.566 {
			t.Errorf("SPAI %s: golden slams %+v, want exactly one at 08:15 ET (%d) at 4.566", clock, slams, slam0815)
		}
		r := Evaluate(fx["spai-2026-10-07"], g.TfMs, c.NowMs, c.Params.params())
		if r.LastSlamTs != slam0815 || r.LastSlamClose != 4.566 || !(r.LastSlamPct < -3) {
			t.Errorf("SPAI %s: LastSlam %d %v %v, want 08:15 ET, 4.566, under -3%%", clock, r.LastSlamTs, r.LastSlamClose, r.LastSlamPct)
		}
	}
	firing := map[string]bool{}
	for _, c := range g.Cases {
		for _, e := range c.Events {
			if c.ParamSet == "slam loose" && e.Kind == KindSlam {
				firing[c.Fixture] = true
			}
		}
	}
	if len(firing) < 8 {
		t.Errorf("the slam loose set fires on only %d fixtures - %s", len(firing), regenerate)
	}
}

// TestGoldenPinsTheExactTies is the Go half of the tie fixtures' guard (the TS
// half is 'exact ties' in emaFlag.golden.test.ts). The zz-tie-* fixtures put a
// bar exactly ON a rule boundary - a high on the trigger, a close and low on the
// stop, a close on ema * (1 + readyDistPct/100), with an EMA of period 1 every
// EMA comparison - so a `>` written as `>=` (or `<=` as `<`) in either language
// changes the answer. Two things are pinned here and nowhere else:
//   - the boundary is still exact on THIS side: the trigger / stop / EMA band the
//     Go port computes itself equals the fixture's bar to the last bit (the
//     golden comparison alone would pass over a fixture that stopped being a tie
//     on both sides at once);
//   - the golden file records the strict / inclusive outcome the rules call for.
func TestGoldenPinsTheExactTies(t *testing.T) {
	g := loadGolden(t)
	fx := loadFixtures(t)
	const tf = int64(60_000)
	closedAt := func(bars []core.Bar) int64 { return bars[len(bars)-1].T + 10*tf }
	caseOf := func(fixture, set, clock string) goldenCase {
		t.Helper()
		for _, c := range g.Cases {
			if c.Fixture == fixture && c.ParamSet == set && c.Clock == clock {
				return c
			}
		}
		t.Fatalf("golden file has no case %s | %s | %s - %s", fixture, set, clock, regenerate)
		return goldenCase{}
	}
	eventAt := func(c goldenCase, kind string, ts int64) *goldenEvent {
		for i := range c.Events {
			if c.Events[i].Kind == kind && c.Events[i].Ts == ts {
				return &c.Events[i]
			}
		}
		return nil
	}
	stale := func(what string) string {
		return what + " - a rule or default moved the boundary: rebuild the fixture (see its \"source\") and regenerate"
	}
	bars := func(name string) []core.Bar {
		t.Helper()
		b, ok := fx[name]
		if !ok || len(b) < 3 {
			t.Fatalf("fixture %s missing - %s", name, regenerate)
		}
		return b
	}

	// A high exactly on the trigger is not a break; one ulp above it is.
	for _, tc := range []struct {
		name string
		ulp  bool
	}{{"zz-tie-trigger-equal", false}, {"zz-tie-trigger-ulp", true}} {
		b := bars(tc.name)
		tie := b[len(b)-1]
		armed := Evaluate(b[:len(b)-1], tf, closedAt(b[:len(b)-1]), DefaultParams())
		want, what := armed.Trigger, "the prefix is no longer READY or the last bar's high is not the trigger"
		if tc.ulp {
			want, what = math.Nextafter(want, math.Inf(1)), what+" + 1 ulp"
		}
		if armed.State != StateReady || tie.H != want {
			t.Fatalf("%s: %s", tc.name, stale(what))
		}
		for _, clock := range []string{"forming", "closed"} {
			c := caseOf(tc.name, "defaults", clock)
			entry := eventAt(c, KindEntry, tie.T)
			switch {
			case tc.ulp && (entry == nil || entry.Price != armed.Trigger || c.State != "long"):
				t.Errorf("%s %s: one ulp over the trigger must enter at it and be long (entry %v, state %s)", tc.name, clock, entry, c.State)
			case !tc.ulp && entry != nil:
				t.Errorf("%s %s: a high EQUAL to the trigger is not a break, but the golden file enters", tc.name, clock)
			case !tc.ulp && clock == "forming" && c.State != "ready":
				t.Errorf("%s forming: the flag must still be armed (ready), golden says %s", tc.name, c.State)
			}
		}
	}

	// A bar whose low and close are exactly the stop: no exit by close; the
	// low <= stop rule (stopByClose=false) exits, ties included.
	for _, tc := range []struct {
		name string
		ulp  bool
	}{{"zz-tie-stop-equal", false}, {"zz-tie-stop-ulp", true}} {
		b := bars(tc.name)
		tie := b[len(b)-2] // one more bar follows: the tie bar is CLOSED on both clocks
		inPos := Evaluate(b[:len(b)-2], tf, closedAt(b[:len(b)-2]), DefaultParams())
		edge, what := inPos.Stop, "the prefix is no longer LONG or the tie bar's low/close is not the stop"
		if tc.ulp {
			edge, what = math.Nextafter(edge, math.Inf(-1)), what+" - 1 ulp"
		}
		if inPos.State != StateLong || tie.C != edge || tie.L != edge {
			t.Fatalf("%s: %s", tc.name, stale(what))
		}
		for _, clock := range []string{"forming", "closed"} {
			byClose := eventAt(caseOf(tc.name, "defaults", clock), KindExit, tie.T)
			byLow := eventAt(caseOf(tc.name, "stopByClose=false", clock), KindExit, tie.T)
			switch {
			case tc.ulp && (byClose == nil || byClose.Price != edge):
				t.Errorf("%s %s: a close one ulp under the stop must exit at the close", tc.name, clock)
			case !tc.ulp && byClose != nil:
				t.Errorf("%s %s: a close EQUAL to the stop must not exit", tc.name, clock)
			}
			if byLow == nil || byLow.Price != inPos.Stop {
				t.Errorf("%s %s stopByClose=false: low <= stop must exit at the stop, ties included (got %v, stop %v)", tc.name, clock, byLow, inPos.Stop)
			}
		}
	}

	// EMA period 1: the EMA is the close; every flag opens READY by a tie, so no
	// 9 EMA NEAR event, yet entries exist (they need READY).
	const ema1 = "ties: emaPeriod=1, zero bands"
	b := bars("zz-tie-ema-period-1")
	p1, ok := g.ParamSets[ema1]
	if !ok {
		t.Fatalf("golden file has no param set %q - %s", ema1, regenerate)
	}
	if r := Evaluate(b, tf, closedAt(b), p1.params()); r.Ema != b[len(b)-1].C {
		t.Errorf("%s", stale("the EMA of period 1 is not the last close"))
	}
	kinds := map[string]bool{}
	for _, e := range caseOf("zz-tie-ema-period-1", ema1, "closed").Events {
		kinds[e.Kind] = true
	}
	for _, k := range []string{KindImpulse, KindFlat, KindEntry, KindExit} {
		if !kinds[k] {
			t.Errorf("zz-tie-ema-period-1 under %q: no %q event", ema1, k)
		}
	}
	if kinds[KindEmaNear] {
		t.Errorf("zz-tie-ema-period-1 under %q: a flag that opens READY has no 9 EMA NEAR event", ema1)
	}

	// 9 EMA NEAR on a close exactly at ema * (1 + readyDistPct/100).
	const near = "ties: readyDistPct on the 04:29 ET close"
	b = bars("zz-tie-ready-extend")
	tie := b[len(b)-2]
	pr, ok := g.ParamSets[near]
	if !ok {
		t.Fatalf("golden file has no param set %q - %s", near, regenerate)
	}
	at := Evaluate(b[:len(b)-1], tf, closedAt(b[:len(b)-1]), pr.params())
	if tie.C != at.Ema*(1+pr.ReadyDistPct/100) {
		t.Fatalf("%s", stale("the 04:29 ET bar is not exactly on the READY band"))
	}
	for _, clock := range []string{"forming", "closed"} {
		if eventAt(caseOf("zz-tie-ready-extend", near, clock), KindEmaNear, tie.T) == nil {
			t.Errorf("zz-tie-ready-extend %s: 9 EMA NEAR must fire on the tie bar", clock)
		}
	}
	under := pr.params()
	under.ReadyDistPct = math.Nextafter(pr.ReadyDistPct, math.Inf(-1))
	for _, e := range Evaluate(b, tf, closedAt(b), under).Events {
		if e.Kind == KindEmaNear && e.Ts == tie.T {
			t.Errorf("a band one ulp under must not fire 9 EMA NEAR on the tie bar")
		}
	}
	if s := caseOf("zz-tie-ready-extend", near, "closed").State; s != "ready" {
		t.Errorf("zz-tie-ready-extend: the flag must be READY after the tie bar, golden says %s", s)
	}
}
