package flagscan

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/emaflag"
)

// The service must hand the scanner exactly what the TypeScript engine says
// about the same bars. internal/emaflag already proves Evaluate == the TS
// output (its golden_test.go); these tests prove the rest of the chain - REST
// load, ET-day guard, clock handling, the ref mapping, the publish - adds
// nothing and loses nothing, by reading the SAME committed golden file
// (web/src/signals/fixtures/golden/emaflag-golden.json) and requiring the
// state, the levels, the flag length and the EMA distance the service
// publishes to match it, for every fixture x parameter set x clock and for every
// mid-setup truncation (replay) the golden file carries - the synthetic zz-tie-*
// fixtures, which sit exactly on rule boundaries, included. The expected
// numbers come from the file, never from this test.

const (
	fixturesDir = "../../web/src/signals/fixtures"
	goldenPath  = "../../web/src/signals/fixtures/golden/emaflag-golden.json"
)

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
	// The 9 EMA SLAM thresholds (every golden param set carries them).
	Slam             bool    `json:"slam"`
	SlamBelowPct     float64 `json:"slamBelowPct"`
	SlamOpenTolPct   float64 `json:"slamOpenTolPct"`
	SlamBodyAtr      float64 `json:"slamBodyAtr"`
	SlamMinBodyPct   float64 `json:"slamMinBodyPct"`
	SlamCloseInRange float64 `json:"slamCloseInRange"`
	SlamVolX         float64 `json:"slamVolX"`
}

func (g goldenParams) params() emaflag.Params {
	return emaflag.Params{
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

type goldenLevels struct {
	State      string   `json:"state"`
	Trigger    *float64 `json:"trigger"`
	Stop       *float64 `json:"stop"`
	FlagBars   *int     `json:"flagBars"`
	EmaDistPct *float64 `json:"emaDistPct"`
	SlamTs     *int64   `json:"slamTs"`  // the latest SLAM of the last ET day: the published SlamMs (e9SlamAge) ...
	SlamPct    *float64 `json:"slamPct"` // ... and its SlamPct (e9SlamPct)
}

type goldenCase struct {
	Fixture  string        `json:"fixture"`
	ParamSet string        `json:"paramSet"`
	Params   goldenParams  `json:"params"`
	Clock    string        `json:"clock"`
	NowMs    int64         `json:"nowMs"`
	Events   []goldenEvent `json:"events"`
	goldenLevels
}

type goldenReplay struct {
	Fixture  string `json:"fixture"`
	ParamSet string `json:"paramSet"`
	Clock    string `json:"clock"`
	Cut      int    `json:"cut"`
	NowMs    int64  `json:"nowMs"`
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
		t.Fatalf("cannot read the golden file %s: %v", goldenPath, err)
	}
	var g goldenFile
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("golden file does not parse: %v", err)
	}
	if g.TfMs != minuteMs {
		t.Fatalf("golden tfMs = %d, the service works at %d", g.TfMs, minuteMs)
	}
	return g
}

// loadFixtures reads every web/src/signals/fixtures/*.json, keyed by file name
// without .json (the same keys the golden file's "fixture" field uses).
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

// symOf is the ticker a fixture name stands for ("beat-2026-10-05" -> "BEAT").
func symOf(fixture string) string {
	return strings.ToUpper(fixture[:strings.IndexByte(fixture, '-')])
}

func near(a, b float64) bool {
	return a == b || math.Abs(a-b) <= 1e-9*math.Max(math.Abs(a), math.Abs(b))
}

func checkRef(t *testing.T, id string, got core.EmaFlagRef, want goldenLevels) {
	t.Helper()
	if s := emaflag.State(got.State).String(); s != want.State {
		t.Errorf("%s: state got %s, golden %s", id, s, want.State)
	}
	level := func(what string, got float64, want *float64) {
		switch {
		case want == nil && !math.IsNaN(got):
			t.Errorf("%s: %s got %v, golden null", id, what, got)
		case want != nil && (math.IsNaN(got) || !near(got, *want)):
			t.Errorf("%s: %s got %v, golden %v", id, what, got, *want)
		}
	}
	level("trigger", got.Trigger, want.Trigger)
	level("stop", got.Stop, want.Stop)
	level("emaDistPct", got.DistPct, want.EmaDistPct)
	wantBars := 0 // null flagBars = no flag = 0
	if want.FlagBars != nil {
		wantBars = *want.FlagBars
	}
	if got.FlagBars != wantBars {
		t.Errorf("%s: flagBars got %d, golden %d", id, got.FlagBars, wantBars)
	}
	// The latest 9 EMA SLAM: a null slamTs (no slam that day) is SlamMs 0, and
	// SlamPct is then not looked at (the overlay Nulls both on SlamMs 0).
	var wantSlam int64
	if want.SlamTs != nil {
		wantSlam = *want.SlamTs
	}
	if got.SlamMs != wantSlam {
		t.Errorf("%s: slamMs got %d, golden %d", id, got.SlamMs, wantSlam)
	}
	if wantSlam != 0 {
		level("slamPct", got.SlamPct, want.SlamPct)
	}
}

// verdictViaService runs one symbol through the service's real path - admit,
// REST load, evaluate, publish - with the clock set so that the engine sees
// exactly nowMs, and returns what was published for it.
func verdictViaService(t *testing.T, ctx context.Context, p emaflag.Params, sym string, bars []core.Bar, nowMs int64) (core.EmaFlagRef, bool) {
	t.Helper()
	var pub map[string]core.EmaFlagRef
	cfg := Config{
		Candidates: func(float64, int) []core.PmVolEntry { return []core.PmVolEntry{{Sym: sym, PmVolX: 2}} },
		Bars:       func(context.Context, string) ([]core.Bar, error) { return bars, nil }, // the shared fixture slice, on purpose
		SetTap:     func([]string) {},
		Publish:    func(m map[string]core.EmaFlagRef) { pub = m },
		Params:     p,
	}
	s, err := newService(cfg, defaultTuning())
	if err != nil {
		t.Fatal(err)
	}
	s.ctx = ctx
	now := time.UnixMilli(nowMs).Add(CloseGrace) // the engine is handed now - CloseGrace == nowMs
	s.applyCandidates(cfg.Candidates(MinPmVolX, 0), now)
	s.dispatch(now)
	s.handleFetch(<-s.results, now)
	s.evaluate(now)
	ref, ok := pub[sym]
	return ref, ok
}

func TestGoldenThroughService(t *testing.T) {
	g := loadGolden(t)
	fx := loadFixtures(t)
	pristine := loadFixtures(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nCases, nReplays, nSlams := 0, 0, 0 // nSlams: compared verdicts that carry a SLAM (vacuity guard)
	for _, c := range g.Cases {
		id := c.Fixture + " | " + c.ParamSet + " | " + c.Clock
		ref, ok := verdictViaService(t, ctx, c.Params.params(), symOf(c.Fixture), fx[c.Fixture], c.NowMs)
		if !ok {
			t.Errorf("%s: the service published no verdict", id)
			continue
		}
		checkRef(t, id, ref, c.goldenLevels)
		nCases++
		if c.SlamTs != nil {
			nSlams++
		}
	}
	for _, r := range g.Replays {
		id := r.Fixture + " | " + r.ParamSet + " | " + r.Clock + " | cut " + strconv.Itoa(r.Cut)
		bars := fx[r.Fixture][:r.Cut]
		ref, ok := verdictViaService(t, ctx, g.ParamSets[r.ParamSet].params(), symOf(r.Fixture), bars, r.NowMs)
		if !ok {
			t.Errorf("%s: the service published no verdict", id)
			continue
		}
		checkRef(t, id, ref, r.goldenLevels)
		nReplays++
		if r.SlamTs != nil {
			nSlams++
		}
	}
	if nCases == 0 || nReplays == 0 || nSlams == 0 {
		t.Fatalf("compared %d cases, %d replays, %d with a SLAM", nCases, nReplays, nSlams)
	}
	// The slices went to the service as the fixture's own memory: it must have
	// copied before folding or merging.
	if !reflect.DeepEqual(fx, pristine) {
		t.Fatal("the service modified the slice Config.Bars returned")
	}
	t.Logf("service verdict == golden TS output for %d full cases and %d replays (%d with a SLAM)", nCases, nReplays, nSlams)
}

// ─── BEAT, second by second ──────────────────────────────────────────────

// beatAnchors reads the BEAT / defaults / all-closed golden events: the
// minutes the TS engine says the setup flatted, entered and exited, which the
// stepwise replay below must reproduce through the live path.
func beatAnchors(t *testing.T, g goldenFile) (flatTs, entryTs, exitTs int64, final goldenLevels) {
	t.Helper()
	for _, c := range g.Cases {
		if c.Fixture != "beat-2026-10-05" || c.ParamSet != "defaults" || c.Clock != "closed" {
			continue
		}
		var lastFlat int64
		for _, e := range c.Events {
			switch e.Kind {
			case emaflag.KindFlat:
				lastFlat = e.Ts
			case emaflag.KindEntry:
				if entryTs == 0 {
					entryTs, flatTs = e.Ts, lastFlat
				}
			case emaflag.KindExit:
				if exitTs == 0 {
					exitTs = e.Ts
				}
			}
		}
		return flatTs, entryTs, exitTs, c.goldenLevels
	}
	t.Fatal("golden file has no BEAT / defaults / closed case")
	return
}

// subAggs splits one 1-minute bar into four per-second aggregates at :00 :15
// :30 :45, one print each, in o, h, l, c order. Folding them reproduces the bar
// exactly (open = o, high = h, low = l, close = c, volume = v).
func subAggs(sym string, b core.Bar) []core.Agg {
	prices := [4]float64{b.O, b.H, b.L, b.C}
	out := make([]core.Agg, 4)
	for i, p := range prices {
		v := b.V / 4
		if i == 3 {
			v = b.V - 3*(b.V/4)
		}
		start := b.T + int64(i)*15_000
		out[i] = core.Agg{Sym: sym, Open: p, High: p, Low: p, Close: p, Vol: v, StartMs: start, EndMs: start + 1000}
	}
	return out
}

// expectedBars is what the service SHOULD hold at simMs, built without any of
// the service's own code: the fixture's bars before the admission minute (what
// the fake REST served) plus the delivered per-second aggregates grouped by
// minute with plain min/max.
func expectedBars(fixture []core.Bar, delivered []core.Agg, admMinute int64) []core.Bar {
	var out []core.Bar
	for _, b := range fixture {
		if b.T < admMinute {
			out = append(out, b)
		}
	}
	var cur *core.Bar
	for _, a := range delivered {
		m := a.StartMs / minuteMs * minuteMs
		if cur == nil || cur.T != m {
			out = append(out, core.Bar{T: m, O: a.Open, H: a.High, L: a.Low, C: a.Close, V: a.Vol})
			cur = &out[len(out)-1]
			continue
		}
		cur.H = math.Max(cur.H, a.High)
		cur.L = math.Min(cur.L, a.Low)
		cur.C = a.Close
		cur.V += a.Vol
	}
	return out
}

func TestReplayBeatSecondBySecondMatchesGolden(t *testing.T) {
	g := loadGolden(t)
	fx := loadFixtures(t)
	bars := fx["beat-2026-10-05"]
	flatTs, entryTs, exitTs, final := beatAnchors(t, g)
	if flatTs == 0 || entryTs == 0 || exitTs == 0 {
		t.Fatalf("golden BEAT events lack flat/entry/exit: %d %d %d", flatTs, entryTs, exitTs)
	}

	const sym = "BEAT"
	e := newEnv(t)
	e.setCands(core.PmVolEntry{Sym: sym, PmVolX: 1.7})
	// REST serves what a chart.get at that moment would: every fixture bar before
	// the current minute (a closed bar; the forming one arrives over the tap).
	// It hands back the fixture's own slice - the service must copy it.
	e.setBars(func(context.Context, string) ([]core.Bar, error) {
		minute := e.clock().UnixMilli() / minuteMs * minuteMs
		n := 0
		for n < len(bars) && bars[n].T < minute {
			n++
		}
		return bars[:n], nil
	})
	s := e.service(defaultTuning())

	start := et(8, 50, 0)
	end := time.UnixMilli(bars[len(bars)-1].T).Add(2 * time.Minute)
	admMinute := start.UnixMilli() / minuteMs * minuteMs

	// The schedule: every aggregate of every fixture bar from the admission
	// minute on, keyed by the fake second it arrives in.
	due := map[int64][]core.Agg{}
	for _, b := range bars {
		if b.T < admMinute {
			continue
		}
		for _, a := range subAggs(sym, b) {
			due[a.StartMs/1000] = append(due[a.StartMs/1000], a)
		}
	}

	type point struct {
		at  time.Time
		ref core.EmaFlagRef
	}
	var timeline []point
	var delivered []core.Agg
	pubsSeen := 0
	steps := 0
	for now := start; !now.After(end); now = now.Add(time.Second) {
		e.setNow(now)
		s.tick(now)
		e.settle(s, now)
		for _, a := range due[now.Unix()] {
			s.handleAgg(a, now)
			delivered = append(delivered, a)
		}
		s.evaluate(now)
		steps++

		// Whatever the service last published must equal the engine run over the
		// independently built bars at this very second.
		wantBars := expectedBars(bars, delivered, admMinute)
		want := emaflag.Evaluate(wantBars, minuteMs, now.Add(-CloseGrace).UnixMilli(), emaflag.DefaultParams())
		got, ok := e.lastPub()[sym]
		if !ok {
			t.Fatalf("%s: nothing published for BEAT yet", now.Format("15:04:05"))
		}
		if !sameRef(got, refOf(want)) {
			t.Fatalf("%s: published %+v, engine over the same bars says %+v", now.Format("15:04:05"), got, refOf(want))
		}

		pubs := e.published()
		if len(pubs) > pubsSeen {
			if len(pubs) > pubsSeen+1 {
				t.Fatalf("%s: %d publishes in one second (limit 1)", now.Format("15:04:05"), len(pubs)-pubsSeen)
			}
			if pubsSeen > 0 && sameRef(pubs[pubsSeen][sym], pubs[pubsSeen-1][sym]) {
				t.Fatalf("%s: a publish that changed nothing", now.Format("15:04:05"))
			}
			timeline = append(timeline, point{now, pubs[pubsSeen][sym]})
			pubsSeen = len(pubs)
		}
	}
	if steps < 1500 {
		t.Fatalf("only %d fake seconds replayed", steps)
	}
	for _, p := range timeline {
		t.Logf("%s  state %d  since %s  dist %.2f%%  trigger %.4f  stop %.4f  flagBars %d",
			p.at.In(etLocation()).Format("15:04:05"), p.ref.State,
			time.UnixMilli(p.ref.SinceMs).In(etLocation()).Format("15:04"), p.ref.DistPct, p.ref.Trigger, p.ref.Stop, p.ref.FlagBars)
	}

	// ── anchors from the golden TS output ──
	firstState := func(state int, from int) *point {
		for i := from; i < len(timeline); i++ {
			if timeline[i].ref.State == state {
				return &timeline[i]
			}
		}
		return nil
	}
	flatMs := func(ms int64) time.Time { return time.UnixMilli(ms) }

	// FLAT/READY on the 08:59 bar: that bar's FLAT event opened the flag already
	// inside the 4% band, so the machine goes straight to READY (3) - and only
	// once the bar is closed, i.e. no earlier than a minute after the bar began.
	ready := firstState(int(emaflag.StateReady), 0)
	if ready == nil {
		t.Fatalf("the service never published READY; timeline %v", timeline)
	}
	if ready.at.Before(flatMs(flatTs).Add(time.Minute)) || ready.ref.SinceMs != flatTs {
		t.Errorf("READY first published at %s with SinceMs %d; golden FLAT bar is %s (%d)",
			ready.at.In(etLocation()).Format("15:04:05"), ready.ref.SinceMs, flatMs(flatTs).In(etLocation()).Format("15:04"), flatTs)
	}
	// LONG during the entry bar's own minute (the break is judged on the HIGH, so
	// it shows while the bar forms), dated to the entry bar.
	long := firstState(int(emaflag.StateLong), 0)
	if long == nil {
		t.Fatalf("the service never published LONG; timeline %v", timeline)
	}
	if long.at.Before(flatMs(entryTs)) || !long.at.Before(flatMs(entryTs).Add(time.Minute)) || long.ref.SinceMs != entryTs {
		t.Errorf("LONG first published at %s with SinceMs %d; golden entry bar is %s (%d)",
			long.at.In(etLocation()).Format("15:04:05"), long.ref.SinceMs, flatMs(entryTs).In(etLocation()).Format("15:04"), entryTs)
	}
	// Back to idle after the EXIT bar closes, and the run ends in the golden
	// final state (idle, no levels).
	var idleAfter *point
	for i := range timeline {
		if timeline[i].at.After(long.at) && timeline[i].ref.State == int(emaflag.StateIdle) {
			idleAfter = &timeline[i]
			break
		}
	}
	if idleAfter == nil {
		t.Fatalf("the service never went back to idle after the position; timeline %v", timeline)
	}
	if idleAfter.at.Before(flatMs(exitTs).Add(time.Minute)) || idleAfter.ref.SinceMs != 0 {
		t.Errorf("idle first published at %s (since %d); golden EXIT bar is %s",
			idleAfter.at.In(etLocation()).Format("15:04:05"), idleAfter.ref.SinceMs, flatMs(exitTs).In(etLocation()).Format("15:04"))
	}
	last := timeline[len(timeline)-1].ref
	checkRef(t, "BEAT final", last, final)

	// The position stays LONG from entry until the exit bar closes (the golden
	// full run has an ADD and two more flags inside it; none may flip the state).
	for _, p := range timeline {
		if p.at.After(long.at) && p.at.Before(idleAfter.at) && p.ref.State != int(emaflag.StateLong) {
			t.Errorf("state %d published at %s between the entry and the exit", p.ref.State, p.at.In(etLocation()).Format("15:04:05"))
		}
	}
}
