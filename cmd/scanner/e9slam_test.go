package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/flagscan"
	"github.com/eelkonkoopman/marketscanner/internal/scan"
)

// ─── seeding the "9EMA Slam" scan (same harness as seedflags_test.go) ────

// An existing install has already consumed every earlier batch - the 9EMA flag
// batch included - so the slam scan must come under a NEW key of its own, seed
// exactly once, and never come back after the user deletes it.
func TestSeedEmaFlagSlamScanOnceUnderItsOwnKey(t *testing.T) {
	if e9SlamSeedKey == e9FlagSeedKey {
		t.Fatalf("the slam scan needs its own seed key: databases already consumed %q", e9FlagSeedKey)
	}
	h := newSeedHarness()
	for _, k := range []string{"seeded.flagscans.v1", "seeded.sessreview.v1", "seeded.gapfade.v1", "seeded.advscan.v1", e9FlagSeedKey, c1mSeedKey} {
		h.meta[k] = "1"
	}
	have := []core.ScanDef{{ID: e9FlagSidewaysID}, {ID: e9FlagReadyID}, {ID: e9FlagBreakoutID}}
	seedScans(context.Background(), e9SlamSeedKey, emaFlagSlamScans(), h.getMeta, h.setMeta, have, h.save, h.install)
	if strings.Join(h.saved, ",") != e9SlamID || strings.Join(h.installed, ",") != e9SlamID {
		t.Fatalf("saved %v installed %v, want just %s", h.saved, h.installed, e9SlamID)
	}
	if e9SlamID != "e9flag-slam" || e9SlamSeedKey != "seeded.e9slam.v1" || h.meta[e9SlamSeedKey] != "1" {
		t.Fatalf("id %q key %q meta %v", e9SlamID, e9SlamSeedKey, h.meta)
	}

	// Deleted by the user, then a restart: the key is set, nothing is resurrected.
	h.saved, h.installed = nil, nil
	seedScans(context.Background(), e9SlamSeedKey, emaFlagSlamScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 || len(h.installed) != 0 {
		t.Fatalf("re-seeded after its meta key was set: saved=%v installed=%v", h.saved, h.installed)
	}

	// The 9EMA flag batch is untouched: still its three scans, no slam among them
	// (a fresh database gets the slam scan from its own batch only).
	if n := len(emaFlagScans()); n != 3 {
		t.Fatalf("emaFlagScans has %d scans, want the original 3", n)
	}
	for _, d := range emaFlagScans() {
		if d.ID == e9SlamID {
			t.Fatal("the slam scan must not ride in the already-consumed e9flag batch")
		}
	}
}

func TestSeedEmaFlagSlamScanKeepsAUserTunedOneAndRetriesAFailedSave(t *testing.T) {
	// A scan with the same ID already exists (the user made one): never overwritten.
	h := newSeedHarness()
	seedScans(context.Background(), e9SlamSeedKey, emaFlagSlamScans(), h.getMeta, h.setMeta,
		[]core.ScanDef{{ID: e9SlamID, Name: "mine"}}, h.save, h.install)
	if len(h.saved) != 0 || h.meta[e9SlamSeedKey] != "1" {
		t.Fatalf("an existing ID must not be overwritten (saved %v) and the key still set (%v)", h.saved, h.meta)
	}

	// The save fails: the key stays unset so the next boot retries, which works.
	h = newSeedHarness()
	h.saveErr[e9SlamID] = errors.New("disk full")
	seedScans(context.Background(), e9SlamSeedKey, emaFlagSlamScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if h.meta[e9SlamSeedKey] != "" || len(h.installed) != 0 {
		t.Fatalf("a failed save must leave the key unset and install nothing: %v %v", h.meta, h.installed)
	}
	delete(h.saveErr, e9SlamID)
	seedScans(context.Background(), e9SlamSeedKey, emaFlagSlamScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if strings.Join(h.saved, ",") != e9SlamID || h.meta[e9SlamSeedKey] != "1" {
		t.Fatalf("retry: saved %v meta %v", h.saved, h.meta)
	}

	// An unreadable meta store must not risk a double seed.
	h = newSeedHarness()
	h.metaErr = errors.New("db locked")
	seedScans(context.Background(), e9SlamSeedKey, emaFlagSlamScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 {
		t.Fatalf("meta error: saved %v", h.saved)
	}
}

// The def's exact shape, every name pinned to a real field constant (a typo'd
// field compiles and silently matches nothing forever).
func TestEmaFlagSlamScanDefShape(t *testing.T) {
	defs := emaFlagSlamScans()
	if len(defs) != 1 {
		t.Fatalf("%d defs, want 1", len(defs))
	}
	d := defs[0]
	if d.ID != e9SlamID || d.Name != "9EMA Slam" || !d.Enabled || d.Limit != 50 {
		t.Fatalf("def %+v", d)
	}
	wantFilter := core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldPmVolX, Cmp: core.CmpGTE, Value: 1},
		{Field: core.FieldE9SlamAge, Cmp: core.CmpLTE, Value: 300},
	}}
	if !reflect.DeepEqual(d.Filter, wantFilter) {
		t.Fatalf("filter %+v, want %+v", d.Filter, wantFilter)
	}
	if want := []core.SortKey{{Field: core.FieldE9SlamAge, Dir: "asc"}}; !reflect.DeepEqual(d.Sort, want) {
		t.Fatalf("sort %+v, want %+v", d.Sort, want)
	}
	wantCols := []string{
		core.FieldLast, core.FieldChgDay, core.FieldPmVolX, core.FieldE9SlamPct, core.FieldE9SlamAge, core.FieldE9Dist, core.FieldNews,
	}
	if strings.Join(d.Columns, ",") != "last,chgDay,pmVolX,e9SlamPct,e9SlamAge,e9Dist,news" ||
		strings.Join(d.Columns, ",") != strings.Join(wantCols, ",") {
		t.Fatalf("columns %v", d.Columns)
	}
	a := d.Alert
	if !a.Enabled || a.On != core.AlertOnEnter || a.Trigger != nil || a.Sound != "slam" || !a.Speak ||
		a.CooldownSec != 300 || a.RequireReentry {
		t.Fatalf("alert %+v, want enabled enter-alert, sound slam, speak, 300 s cooldown", a)
	}
	// Its tone is its own: none of the 9EMA flag stages uses it.
	for _, f := range emaFlagScans() {
		if f.Alert.Sound == a.Sound {
			t.Fatalf("%s already uses the %q tone", f.ID, a.Sound)
		}
	}
	// A fresh slice per call: the scanner and the UI may edit it.
	defs[0].Columns[0] = "mutated"
	if emaFlagSlamScans()[0].Columns[0] != "last" {
		t.Fatal("seeded defs must not alias one Columns slice")
	}
}

// ─── end to end: the SPAI slam, from the bars to the scan row ────────────

// The whole server path over the real SPAI 2026-10-07 bars: flagscan.Run (the
// production loop, fed by a fake REST that serves the fixture through 08:17 ET
// and a clock at 08:17:05) evaluates the engine and publishes into a real
// Scanner seeded the way main() seeds it; a snapshot at 08:17:05 must put SPAI
// on the "9EMA Slam" scan with e9SlamAge = 125 s (from the 08:15 bar's start)
// and e9SlamPct = -4.33, and raise its ENTER alert with the slam tone.
func TestEmaFlagSlamEndToEndSPAI(t *testing.T) {
	raw, err := os.ReadFile("../../web/src/signals/fixtures/spai-2026-10-07.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Bars []core.Bar `json:"bars"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	et, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tz database:", err)
	}
	slamTs := time.Date(2026, 10, 7, 8, 15, 0, 0, et).UnixMilli()
	now := time.Date(2026, 10, 7, 8, 17, 5, 0, et)
	var served []core.Bar // REST at 08:17:05: every bar through the forming 08:17 one
	for _, b := range fx.Bars {
		if b.T <= now.UnixMilli() {
			served = append(served, b)
		}
	}
	if len(served) == 0 || served[len(served)-1].T != slamTs+2*60_000 {
		t.Fatalf("fixture does not reach 08:17 ET (%d bars)", len(served))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	snaps := make(chan *core.MetricSnapshot, 2)
	scn := scan.New(snapEngine{snaps: snaps})
	go scn.Run(ctx)
	h := newSeedHarness()
	seedScans(ctx, e9SlamSeedKey, emaFlagSlamScans(), h.getMeta, h.setMeta, nil, h.save, scn.Upsert)

	published := make(chan map[string]core.EmaFlagRef, 16)
	cfg := flagscan.Config{
		Candidates: func(float64, int) []core.PmVolEntry { return []core.PmVolEntry{{Sym: "SPAI", PmVolX: 2.4}} },
		Bars:       func(context.Context, string) ([]core.Bar, error) { return served, nil },
		SetTap:     func([]string) {},
		Publish: func(m map[string]core.EmaFlagRef) {
			scn.SetEmaFlags(m)
			select {
			case published <- m:
			default:
			}
		},
		Now: func() time.Time { return now },
	}
	done := make(chan error, 1)
	go func() { done <- flagscan.Run(ctx, cfg) }()

	var ref core.EmaFlagRef
	select {
	case m := <-published:
		var ok bool
		if ref, ok = m["SPAI"]; !ok {
			t.Fatalf("first publish lacks SPAI: %v", m)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("flagscan never published a verdict for SPAI")
	}
	if ref.SlamMs != slamTs || math.Abs(ref.SlamPct-(-4.3252149810545255)) > 1e-9 {
		t.Fatalf("published slam %d / %v, want the 08:15 ET bar %d / -4.3252", ref.SlamMs, ref.SlamPct, slamTs)
	}
	scn.List() // the overlay map is installed before the snapshot is processed

	row := pmRow("SPAI", 2.4)
	row.Last, row.ChgDay = 4.3, 18
	snaps <- &core.MetricSnapshot{AsOf: now.UnixMilli(), Sess: core.SessionPre, Rows: []core.Row{row}, Index: map[string]int{"SPAI": 0}}
	tk := nextTickFor(t, scn, map[string]bool{e9SlamID: true})[e9SlamID]
	if len(tk.Order) != 1 || tk.Order[0] != "SPAI" {
		t.Fatalf("slam scan rows %v, want [SPAI]", tk.Order)
	}
	got := tk.Rows["SPAI"]
	if got[core.FieldE9SlamAge] != 125.0 {
		t.Fatalf("e9SlamAge %v, want 125 (08:15:00 -> 08:17:05)", got[core.FieldE9SlamAge])
	}
	if pct, ok := got[core.FieldE9SlamPct].(float64); !ok || math.Abs(pct-(-4.3252149810545255)) > 1e-9 {
		t.Fatalf("e9SlamPct %v, want -4.3252", got[core.FieldE9SlamPct])
	}
	if _, ok := got[core.FieldE9Dist].(float64); !ok || got[core.FieldPmVolX] != 2.4 {
		t.Fatalf("slam row on the wire: %+v", got)
	}
	select {
	case a := <-scn.Alerts():
		if a.ScanID != e9SlamID || a.Sym != "SPAI" || a.Kind != "enter" || a.Sound != "slam" {
			t.Fatalf("alert %+v, want an enter alert for SPAI from the slam scan with the slam tone", a)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no alert for the SPAI slam")
	}
	if r, ok := scn.Row("SPAI"); !ok || r.E9SlamAge != 125 {
		t.Fatalf("scanner row %+v", r)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("flagscan.Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("flagscan.Run did not stop on cancel")
	}
}
