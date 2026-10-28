package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/emaflag"
	"github.com/eelkonkoopman/marketscanner/internal/engine"
	"github.com/eelkonkoopman/marketscanner/internal/feed"
	"github.com/eelkonkoopman/marketscanner/internal/flagscan"
	"github.com/eelkonkoopman/marketscanner/internal/ingest"
	"github.com/eelkonkoopman/marketscanner/internal/scan"
	"github.com/eelkonkoopman/marketscanner/internal/universe"
)

// ─── seeding (same harness as seedflags_test.go) ─────────────────────────

var e9SeedIDs = []string{e9FlagSidewaysID, e9FlagReadyID, e9FlagBreakoutID}

func TestSeedEmaFlagScansSeedsAllThreeOnceUnderOwnKey(t *testing.T) {
	h := newSeedHarness()
	// An existing install: every earlier batch already ran. The 9EMA-flag batch
	// must still seed, under ITS OWN key (seed-key independence).
	for _, k := range []string{"seeded.flagscans.v1", "seeded.sessreview.v1", "seeded.gapfade.v1", "seeded.advscan.v1"} {
		h.meta[k] = "1"
	}
	seedScans(context.Background(), "seeded.e9flag.v1", emaFlagScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if got := strings.Join(h.saved, ","); got != strings.Join(e9SeedIDs, ",") {
		t.Fatalf("saved = %v, want %v", h.saved, e9SeedIDs)
	}
	if got := strings.Join(h.installed, ","); got != strings.Join(e9SeedIDs, ",") {
		t.Fatalf("installed = %v", h.installed)
	}
	if h.meta["seeded.e9flag.v1"] != "1" || e9FlagSeedKey != "seeded.e9flag.v1" {
		t.Fatalf("the batch's own meta flag (%q) must be set after a successful seed: %v", e9FlagSeedKey, h.meta)
	}

	// The user deletes one and restarts: the meta key stays set, so an empty def
	// list must NOT resurrect anything.
	h.saved, h.installed = nil, nil
	seedScans(context.Background(), "seeded.e9flag.v1", emaFlagScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 || len(h.installed) != 0 {
		t.Fatalf("re-seeded after meta was set: saved=%v installed=%v", h.saved, h.installed)
	}
}

func TestSeedEmaFlagScansSkipsExistingIDsAndSeedsTheRest(t *testing.T) {
	h := newSeedHarness()
	have := []core.ScanDef{{ID: e9FlagReadyID, Name: "user-tuned"}}
	seedScans(context.Background(), "seeded.e9flag.v1", emaFlagScans(), h.getMeta, h.setMeta, have, h.save, h.install)
	if got := strings.Join(h.saved, ","); got != e9FlagSidewaysID+","+e9FlagBreakoutID {
		t.Fatalf("saved = %v, want the other two (an existing ID must not be overwritten)", h.saved)
	}
	if h.meta["seeded.e9flag.v1"] != "1" {
		t.Fatal("meta flag should still be set")
	}
}

func TestSeedEmaFlagScansSaveFailureRetriesNextBoot(t *testing.T) {
	h := newSeedHarness()
	h.saveErr[e9FlagReadyID] = errors.New("disk full") // the SECOND of three fails
	seedScans(context.Background(), "seeded.e9flag.v1", emaFlagScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if h.meta["seeded.e9flag.v1"] != "" {
		t.Fatal("meta flag must stay unset when a save fails, so the next boot retries")
	}
	if got := strings.Join(h.installed, ","); got != e9FlagSidewaysID {
		t.Fatalf("only the scan saved before the failure may be installed, got %v", h.installed)
	}

	// Next boot: the first scan is now in the loaded defs; the failure is gone.
	delete(h.saveErr, e9FlagReadyID)
	h.saved, h.installed = nil, nil
	have := []core.ScanDef{{ID: e9FlagSidewaysID}}
	seedScans(context.Background(), "seeded.e9flag.v1", emaFlagScans(), h.getMeta, h.setMeta, have, h.save, h.install)
	if got := strings.Join(h.saved, ","); got != e9FlagReadyID+","+e9FlagBreakoutID {
		t.Fatalf("retry saved %v, want the two missing scans", h.saved)
	}
	if h.meta["seeded.e9flag.v1"] != "1" {
		t.Fatal("meta flag must be set once the retry completes")
	}
}

func TestSeedEmaFlagScansMetaErrorDoesNotSeed(t *testing.T) {
	h := newSeedHarness()
	h.metaErr = errors.New("db locked")
	seedScans(context.Background(), "seeded.e9flag.v1", emaFlagScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 {
		t.Fatalf("an unreadable meta store must not risk double-seeding: %v", h.saved)
	}
}

// A typo'd field name compiles fine and silently matches nothing forever: pin
// every filter, sort and column of the three seeded defs against the real field
// constants, and pin the SHAPE the spec asks for.
func TestEmaFlagScanDefsUseRealFieldsAndTheSpecifiedShape(t *testing.T) {
	validFilter := map[string]bool{core.FieldPmVolX: true, core.FieldE9State: true, core.FieldE9Age: true}
	validSort := map[string]bool{core.FieldE9State: true, core.FieldE9Dist: true, core.FieldE9Age: true}
	wantCols := []string{"last", "chgDay", "pmVolX", "e9State", "e9Dist", "e9Trigger", "e9FlagBars", "e9Age", "news"}
	validCol := map[string]bool{
		core.FieldLast: true, core.FieldChgDay: true, core.FieldPmVolX: true, core.FieldE9State: true, core.FieldE9Dist: true,
		core.FieldE9Trigger: true, core.FieldE9FlagBars: true, core.FieldE9Age: true, core.FieldNews: true,
	}
	type spec struct {
		name, sound string
		stage       []core.Filter // after the pmVolX gate
		sort        []core.SortKey
	}
	want := map[string]spec{
		e9FlagSidewaysID: {"9EMA Flag — sideways", "flat",
			[]core.Filter{{Field: core.FieldE9State, Cmp: core.CmpGTE, Value: 2}},
			[]core.SortKey{{Field: core.FieldE9State, Dir: "desc"}, {Field: core.FieldE9Dist, Dir: "asc"}}},
		e9FlagReadyID: {"9EMA Flag — ready", "ping",
			[]core.Filter{{Field: core.FieldE9State, Cmp: core.CmpEq, Value: 3}},
			[]core.SortKey{{Field: core.FieldE9Dist, Dir: "asc"}}},
		e9FlagBreakoutID: {"9EMA Flag — breakout", "long",
			[]core.Filter{{Field: core.FieldE9State, Cmp: core.CmpEq, Value: 4}, {Field: core.FieldE9Age, Cmp: core.CmpLTE, Value: 300}},
			[]core.SortKey{{Field: core.FieldE9Age, Dir: "asc"}}},
	}
	defs := emaFlagScans()
	if len(defs) != 3 {
		t.Fatalf("%d scans seeded, want 3", len(defs))
	}
	seen := map[string]bool{}
	for _, d := range defs {
		w, ok := want[d.ID]
		if !ok || seen[d.ID] {
			t.Fatalf("unexpected or duplicate scan ID %q", d.ID)
		}
		seen[d.ID] = true
		if d.Name != w.name || !d.Enabled || d.Limit != 50 {
			t.Errorf("%s: name %q enabled %v limit %d", d.ID, d.Name, d.Enabled, d.Limit)
		}
		if strings.Join(d.Columns, ",") != strings.Join(wantCols, ",") {
			t.Errorf("%s: columns %v, want %v", d.ID, d.Columns, wantCols)
		}
		for _, c := range d.Columns {
			if !validCol[c] {
				t.Errorf("%s: column %q is not a real field", d.ID, c)
			}
		}
		// pmVolX >= 1 first, then the stage conditions, all in ONE flat "and" the
		// scan editor can show (no nested groups).
		if d.Filter.Op != "and" || len(d.Filter.Conds) != 1+len(w.stage) {
			t.Fatalf("%s: filter %+v", d.ID, d.Filter)
		}
		gate := d.Filter.Conds[0]
		if gate.Field != core.FieldPmVolX || gate.Cmp != core.CmpGTE || gate.Value != 1 {
			t.Errorf("%s: the first condition must be pmVolX >= 1, got %+v", d.ID, gate)
		}
		for i, c := range d.Filter.Conds {
			if !c.IsLeaf() || !validFilter[c.Field] {
				t.Errorf("%s: filter condition %+v is not a leaf on a real field", d.ID, c)
			}
			if i > 0 && !reflect.DeepEqual(c, w.stage[i-1]) {
				t.Errorf("%s: stage condition %d = %+v, want %+v", d.ID, i, c, w.stage[i-1])
			}
		}
		if len(d.Sort) != len(w.sort) {
			t.Fatalf("%s: sort %+v", d.ID, d.Sort)
		}
		for i, s := range d.Sort {
			if !validSort[s.Field] || s != w.sort[i] {
				t.Errorf("%s: sort key %d = %+v, want %+v", d.ID, i, s, w.sort[i])
			}
		}
		a := d.Alert
		if !a.Enabled || a.On != core.AlertOnEnter || a.Trigger != nil || !a.Speak || a.CooldownSec != 300 || a.Sound != w.sound || a.RequireReentry {
			t.Errorf("%s: alert %+v, want enabled enter-alert, speak, 300 s cooldown, sound %q", d.ID, a, w.sound)
		}
	}
	// Defs must not share a Columns slice: the scanner and the UI may sort/edit it.
	defs[0].Columns[0] = "mutated"
	if emaFlagScans()[0].Columns[0] != "last" || defs[1].Columns[0] != "last" {
		t.Fatal("seeded defs must not alias one Columns slice")
	}
	// The sound ids must exist client-side. (The web registry is in
	// web/src/sounds; the editor falls back to a default for an unknown id, so
	// this only checks the ids are the three distinct ones the spec names.)
	sounds := map[string]bool{}
	for _, d := range emaFlagScans() {
		sounds[d.Alert.Sound] = true
	}
	if len(sounds) != 3 {
		t.Fatalf("each stage needs its own tone, got %v", sounds)
	}
}

// ─── the seeded defs against a real Scanner ──────────────────────────────

// snapEngine is the only part of core.Engine a Scanner touches: its snapshot
// stream. Any other method would hit the nil embedded interface and panic,
// which is the point - the scanner must not need more.
type snapEngine struct {
	core.Engine
	snaps chan *core.MetricSnapshot
}

func (e snapEngine) Snapshots() <-chan *core.MetricSnapshot { return e.snaps }

func pmRow(sym string, pmVolX float64) core.Row {
	return core.Row{
		Sym: sym, Last: 5, ChgDay: 2.5, PmVolX: pmVolX, NewsAge: -1, Sess: core.SessionPre,
		Chg1m: core.Null, Chg5m: core.Null, Chg15m: core.Null, Gap: core.Null, Rvol: core.Null,
	}
}

func nextTickFor(t *testing.T, scn *scan.Scanner, want map[string]bool) map[string]core.ScanTick {
	t.Helper()
	got := map[string]core.ScanTick{}
	deadline := time.After(3 * time.Second)
	for len(got) < len(want) {
		select {
		case tk := <-scn.Ticks():
			if want[tk.ID] {
				got[tk.ID] = tk
			}
		case <-deadline:
			t.Fatalf("timed out waiting for ticks of %v, got %d", want, len(got))
		}
	}
	return got
}

func TestSeededEmaFlagScansEndToEndThroughARealScanner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	snaps := make(chan *core.MetricSnapshot, 4)
	scn := scan.New(snapEngine{snaps: snaps})
	go scn.Run(ctx)

	// Seed exactly the way main() does: through seedScans, installing with Upsert.
	h := newSeedHarness()
	seedScans(ctx, "seeded.e9flag.v1", emaFlagScans(), h.getMeta, h.setMeta, nil, h.save, scn.Upsert)
	stored := map[string]core.ScanDef{}
	for _, d := range scn.List() { // List round-trips through the scanner's own goroutine: the upserts are in
		stored[d.ID] = d
	}
	for _, id := range e9SeedIDs {
		d, ok := stored[id]
		if !ok || !d.Alert.Speak {
			t.Fatalf("%s missing or lost its Speak flag in the live scanner: %+v", id, d)
		}
	}

	const asOf = int64(1_800_000_000_000)
	at := func(secAgo int64) int64 { return asOf - secAgo*1000 }
	scn.SetEmaFlags(map[string]core.EmaFlagRef{
		"FLAT1":   {State: 2, DistPct: 3.0, Trigger: 5.2, Stop: 4.8, FlagBars: 4, SinceMs: at(100)},
		"FLAT2":   {State: 2, DistPct: 1.0, Trigger: 5.2, Stop: 4.8, FlagBars: 3, SinceMs: at(100)},
		"RDY":     {State: 3, DistPct: 2.0, Trigger: 5.2, Stop: 4.8, FlagBars: 5, SinceMs: at(100)},
		"BRKNEW":  {State: 4, DistPct: 4.0, Trigger: math.NaN(), Stop: 4.9, FlagBars: 0, SinceMs: at(10)},
		"BRKOLD":  {State: 4, DistPct: 6.0, Trigger: math.NaN(), Stop: 4.9, FlagBars: 0, SinceMs: at(600)},
		"LOWVOL":  {State: 4, DistPct: 4.0, Trigger: math.NaN(), Stop: 4.9, SinceMs: at(5)},
		"IDLE":    {State: 0, DistPct: -3, Trigger: math.NaN(), Stop: math.NaN()},
		"IMPULSE": {State: 1, DistPct: 9, Trigger: math.NaN(), Stop: math.NaN(), SinceMs: at(30)},
	})
	scn.List() // sync: the overlay map is installed before the snapshot is processed

	snap := func(rows ...core.Row) *core.MetricSnapshot {
		s := &core.MetricSnapshot{AsOf: asOf, Sess: core.SessionPre, Rows: rows, Index: map[string]int{}}
		for i := range rows {
			s.Index[rows[i].Sym] = i
		}
		return s
	}
	snaps <- snap(pmRow("FLAT1", 2), pmRow("FLAT2", 1.0), pmRow("RDY", 3), pmRow("BRKNEW", 1.2), pmRow("BRKOLD", 5),
		pmRow("LOWVOL", 0.99), pmRow("IDLE", 4), pmRow("IMPULSE", 4), pmRow("UNKNOWN", 9))
	ticks := nextTickFor(t, scn, map[string]bool{e9FlagSidewaysID: true, e9FlagReadyID: true, e9FlagBreakoutID: true})
	eq := func(id string, want ...string) {
		t.Helper()
		if got := ticks[id].Order; strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: rows %v, want %v", id, got, want)
		}
	}
	// pmVolX >= 1 is inclusive (FLAT2 at exactly 1.0 is in, LOWVOL at 0.99 is out);
	// state desc then distance asc; idle, impulse and an unevaluated symbol are out.
	eq(e9FlagSidewaysID, "BRKNEW", "BRKOLD", "RDY", "FLAT2", "FLAT1")
	eq(e9FlagReadyID, "RDY")
	eq(e9FlagBreakoutID, "BRKNEW") // BRKOLD is 600 s old; LOWVOL fails the multiple

	// The streamed columns arrive in their wire shapes.
	row := ticks[e9FlagBreakoutID].Rows["BRKNEW"]
	if row["e9State"] != 4 || row["e9Age"] != 10.0 || row["e9Dist"] != 4.0 || row["pmVolX"] != 1.2 {
		t.Fatalf("breakout row on the wire: %+v", row)
	}
	if v, has := row["e9Trigger"]; has && v != nil {
		t.Fatalf("a NaN trigger must reach the wire as null, got %v", v)
	}
	if _, err := json.Marshal(ticks[e9FlagBreakoutID]); err != nil {
		t.Fatalf("a tick with a published NaN level must still marshal: %v", err)
	}

	// The scanner's enter alert fires for every symbol already matching when it
	// first sees the scan (only the activity-log "entry" is suppressed on the
	// baseline pass): one alert per matching symbol per scan, each with its
	// scan's own tone, and none for the names the filters exclude.
	first := map[string][]string{}
	for quiet := false; !quiet; {
		select {
		case a := <-scn.Alerts():
			first[a.ScanID+"/"+a.Sound] = append(first[a.ScanID+"/"+a.Sound], a.Sym)
		case <-time.After(150 * time.Millisecond):
			quiet = true
		}
	}
	for key, n := range map[string]int{
		e9FlagSidewaysID + "/flat": 5, e9FlagReadyID + "/ping": 1, e9FlagBreakoutID + "/long": 1,
	} {
		if len(first[key]) != n {
			t.Fatalf("first-sight alerts %s = %v, want %d symbol(s); all: %v", key, first[key], n, first)
		}
	}
	if len(first) != 3 {
		t.Fatalf("unexpected alert sources: %v", first)
	}

	// A fresh ENTER alert: RDY climbs to LONG on the next snapshot, entering the
	// breakout scan (and leaving ready). The alert carries the breakout tone.
	scn.SetEmaFlags(map[string]core.EmaFlagRef{
		"RDY": {State: 4, DistPct: 2.5, Trigger: math.NaN(), Stop: 4.8, SinceMs: asOf + 4000 - 2000},
	})
	scn.List()
	snaps <- &core.MetricSnapshot{AsOf: asOf + 4000, Sess: core.SessionPre, Rows: []core.Row{pmRow("RDY", 3)}, Index: map[string]int{"RDY": 0}}
	select {
	case a := <-scn.Alerts():
		if a.ScanID != e9FlagBreakoutID || a.Sym != "RDY" || a.Kind != "enter" || a.Sound != "long" {
			t.Fatalf("alert %+v, want an enter alert for RDY from the breakout scan with the long tone", a)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no alert when RDY entered the breakout stage")
	}
	// RDY was already in "sideways" (state >= 2) and merely changed stage there:
	// no second alert.
	select {
	case a := <-scn.Alerts():
		t.Fatalf("unexpected extra alert %+v", a)
	case <-time.After(150 * time.Millisecond):
	}
}

// ─── the adapter ─────────────────────────────────────────────────────────

func newAdapterParts(t *testing.T, restBase string) (*engine.Engine, *feed.Client, *ingest.Ingest, *scan.Scanner) {
	t.Helper()
	eng := engine.New(engine.Options{})
	// A dummy value for a local httptest server: the feed client always appends
	// an apiKey query parameter, whatever the server does with it.
	fc := feed.New(feed.Config{APIKey: "test-only", RESTBase: restBase})
	ing := ingest.New(fc, eng, universe.NewAll())
	scn := scan.New(eng)
	return eng, fc, ing, scn
}

func TestEmaFlagConfigWiresEverySeam(t *testing.T) {
	// A local REST server standing in for Massive: records the aggregates request
	// the Bars seam makes and answers with two 1-minute bars.
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"results":[{"t":1790938800000,"o":1,"h":2,"l":0.5,"c":1.5,"v":1000},{"t":1790938860000,"o":1.5,"h":1.6,"l":1.4,"c":1.55,"v":10}]}`))
	}))
	defer srv.Close()
	eng, fc, ing, scn := newAdapterParts(t, srv.URL)
	cfg := emaFlagConfig(eng, fc, ing, scn)

	if cfg.Candidates == nil || cfg.Bars == nil || cfg.SetTap == nil || cfg.Publish == nil || cfg.Now == nil || cfg.Drops == nil || cfg.Aggs == nil {
		t.Fatalf("every seam must be wired: %+v", cfg)
	}
	if cfg.Params != emaflag.DefaultParams() {
		t.Fatalf("the scan must run the Signal window's defaults, got %+v", cfg.Params)
	}
	if cfg.Aggs != ing.FlagAggs() {
		t.Fatal("Aggs must be the ingest flag tap's channel")
	}
	if cfg.Drops() != 0 {
		t.Fatal("a fresh tap has dropped nothing")
	}

	// Bars is the chart window's REST call: 1-minute aggregates over prev day..today.
	bars, err := cfg.Bars(context.Background(), "BEAT")
	if err != nil || len(bars) != 2 || bars[0].T != 1790938800000 || bars[1].C != 1.55 || bars[0].V != 1000 {
		t.Fatalf("Bars via the REST seam: %v %+v", err, bars)
	}
	if !strings.Contains(gotPath, "/v2/aggs/ticker/BEAT/range/1/minute/") {
		t.Fatalf("Bars must hit the 1-minute aggregates endpoint, requested %q", gotPath)
	}
	parts := strings.Split(strings.TrimPrefix(gotPath, "/v2/aggs/ticker/BEAT/range/1/minute/"), "/")
	if len(parts) != 2 || parts[0] >= parts[1] {
		t.Fatalf("the request must span the previous trading day through today (from < to), got %v", parts)
	}

	// Publish lands in the scanner (its overlay map is hot-swapped through ctrl:
	// run a scanner and watch a snapshot row pick the verdict up).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	snaps := make(chan *core.MetricSnapshot, 1)
	live := scan.New(snapEngine{snaps: snaps})
	go live.Run(ctx)
	cfg2 := emaFlagConfig(eng, fc, ing, live)
	cfg2.Publish(map[string]core.EmaFlagRef{"X": {State: 3, DistPct: 1.5, Trigger: 5.5, Stop: 5.1, FlagBars: 4, SinceMs: 1_000}})
	live.List() // sync with the scanner goroutine
	snaps <- &core.MetricSnapshot{AsOf: 11_000, Sess: core.SessionPre, Rows: []core.Row{pmRow("X", 2)}, Index: map[string]int{"X": 0}}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if r, ok := live.Row("X"); ok && r.E9State == 3 {
			if r.E9Age != 10 || r.E9Trigger != 5.5 {
				t.Fatalf("row %+v: age must be 10 s from the snapshot clock", r)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the published verdict never reached the scanner's row")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The real Run against the real engine and scanner, with nothing qualifying: it
// must complete its candidate query through the engine's shard fan-out (the one
// call that blocks on a running engine), publish nothing, and return nil when
// cancelled - never hang the shutdown.
func TestEmaFlagServiceRunsAgainstARealEngineAndStopsOnCancel(t *testing.T) {
	eng, fc, ing, scn := newAdapterParts(t, "http://127.0.0.1:1") // nothing listens: a stray REST call would fail loudly
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	go scn.Run(ctx)

	cfg := emaFlagConfig(eng, fc, ing, scn)
	var published int
	pub := cfg.Publish
	cfg.Publish = func(m map[string]core.EmaFlagRef) { published++; pub(m) }
	asked := make(chan struct{}, 1)
	cand := cfg.Candidates
	cfg.Candidates = func(minX float64, limit int) []core.PmVolEntry {
		out := cand(minX, limit)
		select {
		case asked <- struct{}{}:
		default:
		}
		return out
	}

	done := make(chan error, 1)
	go func() { done <- runEmaFlags(ctx, cfg) }()
	select {
	case <-asked: // the engine answered the candidate query
	case <-time.After(5 * time.Second):
		t.Fatal("the candidate query never completed against the real engine")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runEmaFlags returned %v on cancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the service did not stop on cancel")
	}
	if published != 0 {
		t.Fatalf("%d publishes with no candidates", published)
	}
}

// A Config the service refuses must not stop the scanner: run() cancels every
// component when one returns an error.
func TestRunEmaFlagsSwallowsAStartupFailure(t *testing.T) {
	if err := runEmaFlags(context.Background(), flagscan.Config{}); err != nil {
		t.Fatalf("a misconfigured side feature must never take the scanner down, got %v", err)
	}
}

func TestEmaFlagsKillSwitch(t *testing.T) {
	t.Setenv("SCANNER_E9FLAGS", "")
	if !emaFlagsEnabled() {
		t.Fatal("the market scan is on by default")
	}
	t.Setenv("SCANNER_E9FLAGS", "0")
	if emaFlagsEnabled() {
		t.Fatal("SCANNER_E9FLAGS=0 must turn it off")
	}
	t.Setenv("SCANNER_E9FLAGS", "1")
	if !emaFlagsEnabled() {
		t.Fatal("only 0 disables it")
	}
}
