package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
	"github.com/eelkonkoopman/marketscanner/internal/engine"
	"github.com/eelkonkoopman/marketscanner/internal/feed"
	"github.com/eelkonkoopman/marketscanner/internal/flagscan"
	"github.com/eelkonkoopman/marketscanner/internal/ingest"
	"github.com/eelkonkoopman/marketscanner/internal/scan"
	"github.com/eelkonkoopman/marketscanner/internal/universe"
)

// ─── seeding the GREEN DILDO scans (same harness as seedflags_test.go) ───

func TestSeedDildoScansOnceUnderTheirOwnKey(t *testing.T) {
	earlier := []string{"seeded.flagscans.v1", "seeded.sessreview.v1", "seeded.gapfade.v1", "seeded.advscan.v1",
		e9FlagSeedKey, c1mSeedKey, e9SlamSeedKey}
	h := newSeedHarness()
	for _, k := range earlier {
		if k == dildoSeedKey {
			t.Fatalf("the dildo scans need their own seed key, %q is taken", k)
		}
		h.meta[k] = "1"
	}
	seedScans(context.Background(), dildoSeedKey, dildoScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if got := strings.Join(h.saved, ","); got != "dildo-1m,dildo-pb,dildo-touch" || strings.Join(h.installed, ",") != got {
		t.Fatalf("saved %v installed %v", h.saved, h.installed)
	}
	if dildoSeedKey != "seeded.dildo.v1" || h.meta[dildoSeedKey] != "1" {
		t.Fatalf("key %q meta %v", dildoSeedKey, h.meta)
	}

	// Deleted by the user, then a restart: nothing is resurrected.
	h.saved, h.installed = nil, nil
	seedScans(context.Background(), dildoSeedKey, dildoScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 || len(h.installed) != 0 {
		t.Fatalf("re-seeded after its key was set: %v %v", h.saved, h.installed)
	}
}

func TestSeedDildoScansKeepsAUserTunedOneAndRetriesAFailedSave(t *testing.T) {
	h := newSeedHarness()
	seedScans(context.Background(), dildoSeedKey, dildoScans(), h.getMeta, h.setMeta,
		[]core.ScanDef{{ID: dildoPbID, Name: "mine"}}, h.save, h.install)
	if strings.Join(h.saved, ",") != "dildo-1m,dildo-touch" || h.meta[dildoSeedKey] != "1" {
		t.Fatalf("an existing ID is never overwritten, the others still seed: saved %v meta %v", h.saved, h.meta)
	}

	h = newSeedHarness()
	h.saveErr[dildo1mID] = errors.New("disk full")
	seedScans(context.Background(), dildoSeedKey, dildoScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if h.meta[dildoSeedKey] != "" || len(h.installed) != 0 {
		t.Fatalf("a failed save must leave the key unset and install nothing: %v %v", h.meta, h.installed)
	}
	h.metaErr = errors.New("db locked")
	h.saved = nil
	seedScans(context.Background(), dildoSeedKey, dildoScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 {
		t.Fatalf("an unreadable meta store must not risk a double seed: %v", h.saved)
	}
}

// The defs' exact shape (spec table D), every name a real field constant.
func TestDildoScanDefShapes(t *testing.T) {
	defs := dildoScans()
	if len(defs) != 3 {
		t.Fatalf("%d defs", len(defs))
	}
	byID := map[string]core.ScanDef{}
	for _, d := range defs {
		byID[d.ID] = d
		if !d.Enabled || d.Limit != 50 {
			t.Fatalf("%s enabled %v limit %d", d.ID, d.Enabled, d.Limit)
		}
		for _, f := range d.Filter.Conds {
			if f.Field == core.FieldPmVolX {
				t.Fatalf("%s: the dildo is whole-market, no pmVolX term", d.ID)
			}
		}
	}
	for _, c := range []struct {
		id, name string
		filter   []core.Filter
		sort     string
		cols     string
		alert    core.AlertConfig
	}{
		{"dildo-1m", "Green Dildo",
			[]core.Filter{
				{Field: core.FieldDildoAge, Cmp: core.CmpLTE, Value: 240},
				{Field: core.FieldLast, Cmp: core.CmpGTE, Value: 0.20},
				{Field: core.FieldDildoDvol, Cmp: core.CmpGTE, Value: 100000},
			}, core.FieldDildoAge, "last,dildoBody,dildoVolX,dildoDvol,dildoAge,chgDay,vol,news",
			core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: "dildo", CooldownSec: 600, Speak: true, SendToTWS: true}},
		{"dildo-pb", "Dildo 9EMA Pullback",
			[]core.Filter{
				{Field: core.FieldDildoPbState, Cmp: core.CmpEq, Value: 3},
				{Field: core.FieldDildoPbAge, Cmp: core.CmpLTE, Value: 240},
			}, core.FieldDildoPbAge, "last,dildoPbPx,dildoBody,dildoPbAge,chgDay,vol,news",
			core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: "long", CooldownSec: 600, Speak: true}},
		{"dildo-touch", "Dildo 9EMA Touch",
			[]core.Filter{
				{Field: core.FieldDildoPbState, Cmp: core.CmpEq, Value: 2},
			}, core.FieldDildoTouchAge, "last,dildoBody,dildoTouchAge,chgDay,news",
			core.AlertConfig{Enabled: false, On: core.AlertOnEnter, Sound: "ping", CooldownSec: 600, Speak: false}},
	} {
		d, ok := byID[c.id]
		if !ok || d.Name != c.name {
			t.Fatalf("%s missing or misnamed: %+v", c.id, d)
		}
		if want := (core.Filter{Op: "and", Conds: c.filter}); !reflect.DeepEqual(d.Filter, want) {
			t.Fatalf("%s filter %+v, want %+v", c.id, d.Filter, want)
		}
		if !reflect.DeepEqual(d.Sort, []core.SortKey{{Field: c.sort, Dir: "asc"}}) {
			t.Fatalf("%s sort %+v", c.id, d.Sort)
		}
		if got := strings.Join(d.Columns, ","); got != c.cols {
			t.Fatalf("%s columns %s, want %s", c.id, got, c.cols)
		}
		if d.Alert != c.alert {
			t.Fatalf("%s alert %+v, want %+v", c.id, d.Alert, c.alert)
		}
	}
	// The dildo tone is its own among the seeded alert tones it could be
	// confused with.
	for _, f := range append(emaFlagScans(), emaFlagSlamScans()...) {
		if f.Alert.Sound == "dildo" {
			t.Fatalf("%s already uses the dildo tone", f.ID)
		}
	}
	defs[0].Columns[0] = "mutated"
	if dildoScans()[0].Columns[0] != "last" {
		t.Fatal("seeded defs must not alias one Columns slice")
	}
}

// The env gates: on unless "0".
func TestDildoAndClimaxEnvGates(t *testing.T) {
	for _, c := range []struct {
		env  string
		val  string
		gate func() bool
		want bool
	}{
		{"SCANNER_DILDO", "", dildoEnabled, true},
		{"SCANNER_DILDO", "1", dildoEnabled, true},
		{"SCANNER_DILDO", "0", dildoEnabled, false},
		{"SCANNER_CLIMAX", "", climaxEnabled, true},
		{"SCANNER_CLIMAX", "0", climaxEnabled, false},
	} {
		t.Setenv(c.env, c.val)
		if got := c.gate(); got != c.want {
			t.Errorf("%s=%q: %v, want %v", c.env, c.val, got, c.want)
		}
	}
}

// ─── the wiring ──────────────────────────────────────────────────────────

// emaFlagConfig hands flagscan the engine's dildo hand-off (nil, so off, when
// the engine was built without the judge), the calibrated rule and the drop
// counter. With the judge off the rule goes over DISABLED (RV1-SRV-1): a
// non-zero Params with Enabled false - the zero value would be read as "the
// defaults" - so flagscan judges no dildo on its candidates either, and
// SCANNER_DILDO=0 silences both alerts.
func TestEmaFlagConfigWiresTheDildoHandOff(t *testing.T) {
	for _, on := range []bool{true, false} {
		eng := engine.New(engine.Options{Dildo: on})
		fc := feed.New(feed.Config{APIKey: "test-only", RESTBase: "http://127.0.0.1:1"}) // never called here
		ing := ingest.New(fc, eng, universe.NewAll())
		cfg := emaFlagConfig(eng, fc, ing, scan.New(eng))
		if (cfg.Hot != nil) != on || cfg.Hot != eng.DildoHits() {
			t.Fatalf("dildo %v: Hot %v, want the engine's DildoHits (%v)", on, cfg.Hot, eng.DildoHits())
		}
		want := dildopb.DefaultParams()
		want.Enabled = on
		if cfg.Dildo != want || cfg.Dildo == (dildopb.Params{}) || cfg.DildoDrops == nil || cfg.DildoDrops() != 0 {
			t.Fatalf("dildo %v: params %+v (want %+v), drops wired %v", on, cfg.Dildo, want, cfg.DildoDrops != nil)
		}
	}
}

// SCANNER_DILDO=0 end to end (RV1-SRV-1): a pmVolX candidate whose chart bars
// hold a green dildo (MB 2026-08-07 16:51) raises "Green Dildo" through the
// overlay's chart-anchor union while the dildo is on - and with the judge off
// it raises nothing: its verdict arrives with every dildo field empty (the row's
// dildoAge Null) and no dildo alert fires. Production wiring (emaFlagConfig),
// fake REST / candidates / tap.
func TestDildoOffSilencesTheCandidatePath(t *testing.T) {
	raw, err := os.ReadFile("../../web/src/signals/fixtures/dildo/dildo-mb-2026-08-07.json")
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
	dildoTs := time.Date(2026, 8, 7, 16, 51, 0, 0, et).UnixMilli()
	for _, on := range []bool{true, false} {
		t.Run(map[bool]string{true: "on", false: "off"}[on], func(t *testing.T) {
			if !on {
				t.Setenv("SCANNER_DILDO", "0")
			}
			var clock atomic.Int64
			clock.Store(fx.Bars[0].T - 60_000)
			now := func() time.Time { return time.UnixMilli(clock.Load()) }
			eng := engine.New(engine.Options{TickHz: 20, Now: now, Dildo: dildoEnabled(), Climax: climaxEnabled()})
			if (eng.DildoHits() != nil) != on {
				t.Fatalf("precondition: the engine judge on=%v", on)
			}
			clock.Store(dildoTs + 63_000) // 16:52:03

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { _ = eng.Run(ctx) }()
			scn := scan.New(eng)
			go func() { _ = scn.Run(ctx) }()
			h := newSeedHarness()
			seedScans(ctx, dildoSeedKey, dildoScans(), h.getMeta, h.setMeta, nil, h.save, scn.Upsert)
			scn.List()

			fc := feed.New(feed.Config{APIKey: "test-only", RESTBase: "http://127.0.0.1:1"}) // replaced below, never called
			fcfg := emaFlagConfig(eng, fc, ingest.New(fc, eng, universe.NewAll()), scn)
			fcfg.Candidates = func(float64, int) []core.PmVolEntry { return []core.PmVolEntry{{Sym: "MB", PmVolX: 3}} }
			fcfg.Bars = func(context.Context, string) ([]core.Bar, error) {
				minute := clock.Load() / 60_000 * 60_000
				n := 0
				for n < len(fx.Bars) && fx.Bars[n].T < minute {
					n++
				}
				return fx.Bars[:n], nil
			}
			fcfg.Aggs, fcfg.SetTap, fcfg.Now = nil, func([]string) {}, now
			done := make(chan error, 1)
			go func() { done <- flagscan.Run(ctx, fcfg) }()
			defer func() {
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(5 * time.Second):
					t.Error("flagscan.Run did not stop")
				}
			}()
			for _, b := range fx.Bars {
				if b.T > dildoTs {
					break
				}
				eng.Apply(core.Agg{Sym: "MB", Open: b.O, High: b.H, Low: b.L, Close: b.C, Vol: b.V, StartMs: b.T, EndMs: b.T + 1000})
			}

			deadline := time.After(8 * time.Second)
			var settled <-chan time.Time // armed once MB's verdict is on its row
			for {
				select {
				case a := <-scn.Alerts():
					if a.Sym != "MB" {
						continue
					}
					if !on {
						t.Fatalf("SCANNER_DILDO=0 still alerts: scan %s sound %s dildoAge %v", a.ScanID, a.Sound, a.Row[core.FieldDildoAge])
					}
					if a.ScanID != dildo1mID || a.Sound != "dildo" {
						t.Fatalf("dildo on: alert %+v, want Green Dildo", a)
					}
					return
				case <-deadline:
					if on {
						t.Fatal("dildo on: no Green Dildo alert for the candidate (the control the off case is checked against)")
					}
					t.Fatal("no flagscan verdict for MB")
				case <-time.After(50 * time.Millisecond):
					if on {
						continue
					}
					if settled == nil {
						if r, ok := scn.Row("MB"); ok && !core.IsNull(r.E9State) {
							if !core.IsNull(r.DildoAge) || !core.IsNull(r.DildoPbAge) || !core.IsNull(r.DildoTouchAge) {
								t.Fatalf("SCANNER_DILDO=0: MB's row carries dildo fields: age %v pbAge %v touchAge %v", r.DildoAge, r.DildoPbAge, r.DildoTouchAge)
							}
							settled = time.After(1500 * time.Millisecond) // a few more snapshots for an alert to show up
						}
					}
				case <-settled:
					return // MB's verdict has been on its row for 1.5 s and no dildo alert came
				}
			}
		})
	}
}

// ─── end to end: MB 2026-08-07, both alerts on the production path ───────

// A real Engine (Dildo on) fed MB's bars as per-second aggregates judges the
// 16:51 ET green dildo and hands it to a real flagscan.Run (Hot = the engine's
// channel; REST = the fixture as chart.get would serve it at each moment;
// Publish = a real Scanner seeded the way main() seeds it). The scanner must
// raise "Green Dildo" (the dildo tone) at 16:52:03, and once the 17:01 bar has
// closed, "Dildo 9EMA Pullback" (the long tone) - two separate alerts for MB,
// with MB hot-only (every e9* field Null).
func TestDildoEndToEndMB(t *testing.T) {
	raw, err := os.ReadFile("../../web/src/signals/fixtures/dildo/dildo-mb-2026-08-07.json")
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
	dildoTs := time.Date(2026, 8, 7, 16, 51, 0, 0, et).UnixMilli()
	pbTs := time.Date(2026, 8, 7, 17, 1, 0, 0, et).UnixMilli()

	// One clock for the engine's snapshots and flagscan; the engine starts
	// listening before the first bar. The engine's 1 Hz judge runs on the real
	// wall clock, long past 2026-08-07, so each bar is judged as it lands.
	var clock atomic.Int64
	clock.Store(fx.Bars[0].T - 60_000)
	now := func() time.Time { return time.UnixMilli(clock.Load()) }
	eng := engine.New(engine.Options{TickHz: 20, Now: now, Dildo: true})
	clock.Store(dildoTs + 63_000) // 16:52:03

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()
	scn := scan.New(eng)
	go func() { _ = scn.Run(ctx) }()
	h := newSeedHarness()
	seedScans(ctx, dildoSeedKey, dildoScans(), h.getMeta, h.setMeta, nil, h.save, scn.Upsert)
	scn.List()

	fcfg := flagscan.Config{
		Candidates: func(float64, int) []core.PmVolEntry { return nil }, // MB is no pmVolX candidate: hot-only
		Bars: func(context.Context, string) ([]core.Bar, error) {
			minute := clock.Load() / 60_000 * 60_000
			n := 0
			for n < len(fx.Bars) && fx.Bars[n].T < minute {
				n++
			}
			return fx.Bars[:n], nil
		},
		SetTap:     func([]string) {},
		Publish:    scn.SetEmaFlags,
		Now:        now,
		Hot:        eng.DildoHits(),
		DildoDrops: eng.DildoDrops,
	}
	done := make(chan error, 1)
	go func() { done <- flagscan.Run(ctx, fcfg) }()

	// MB's earlier session (its earlier highs, up to 19.75, sit above the 16:51 candle) is not
	// fed to the engine: only HOD dildos alert, so the engine watches from 16:40,
	// where the 16:51 candle is the day's high it has seen. flagscan still gets the
	// whole fixture over REST.
	engineFrom := time.Date(2026, 8, 7, 16, 40, 0, 0, et).UnixMilli()
	for _, b := range fx.Bars {
		if b.T > dildoTs {
			break
		}
		if b.T < engineFrom {
			continue
		}
		eng.Apply(core.Agg{Sym: "MB", Open: b.O, High: b.H, Low: b.L, Close: b.C, Vol: b.V, StartMs: b.T, EndMs: b.T + 1000})
	}
	nextAlert := func(what string) core.AlertMsg {
		t.Helper()
		deadline := time.After(8 * time.Second)
		for {
			select {
			case a := <-scn.Alerts():
				if a.Sym == "MB" {
					return a
				}
			case <-deadline:
				t.Fatalf("no %s alert for MB", what)
			}
		}
	}
	a := nextAlert("Green Dildo")
	if a.ScanID != dildo1mID || a.Sound != "dildo" || a.Kind != "enter" {
		t.Fatalf("alert 1 %+v, want Green Dildo with the dildo tone", a)
	}
	if v, ok := a.Row[core.FieldDildoAge].(float64); !ok || v != 63 {
		t.Fatalf("alert 1 row dildoAge %v, want 63 s (16:51:00 -> 16:52:03)", a.Row[core.FieldDildoAge])
	}

	// flagscan must have taken in MB's admission fetch (the bars through the 16:51 dildo;
	// the early resync booked for 16:52:20) BEFORE the clock jumps. Alert 1 comes from the
	// engine's own path and says nothing about flagscan: under -race and load its loop can
	// still hold that fetch's result. Taken in after the jump, the fetch - started at
	// 16:52:03, too early to hold the dildo minute final - books the early resync for
	// "now + 1 s", 17:02:06, which this frozen clock never reaches: the 17:01 bar never
	// arrives and alert 2 never comes (the flake of the gate's -race run). Its first
	// publish, MB watching for the pullback, shows it was taken in.
	for deadline := time.Now().Add(8 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if r, ok := scn.Row("MB"); ok && r.DildoPbState == dildopb.PbWatching {
			break
		}
		if time.Now().After(deadline) {
			r, _ := scn.Row("MB")
			t.Fatalf("flagscan never published MB's first load (pbState %v, want %d: watching)", r.DildoPbState, dildopb.PbWatching)
		}
	}

	// The 17:01 bar closes: flagscan's early resync (due 16:52:20) runs now and
	// REST holds every bar through 17:01.
	clock.Store(pbTs + 65_000) // 17:02:05
	a = nextAlert("Dildo 9EMA Pullback")
	if a.ScanID != dildoPbID || a.Sound != "long" {
		t.Fatalf("alert 2 %+v, want Dildo 9EMA Pullback with the long tone", a)
	}
	if v, ok := a.Row[core.FieldDildoPbAge].(float64); !ok || v != 65 {
		t.Fatalf("alert 2 row dildoPbAge %v, want 65 s", a.Row[core.FieldDildoPbAge])
	}
	if v, ok := a.Row[core.FieldDildoPbPx].(float64); !ok || math.Abs(v-7.93) > 0.005 {
		t.Fatalf("alert 2 row dildoPbPx %v, want ~7.93", a.Row[core.FieldDildoPbPx])
	}
	r, ok := scn.Row("MB")
	if !ok || r.DildoPbState != 3 || !core.IsNull(r.E9State) || !core.IsNull(r.E9Dist) {
		t.Fatalf("scanner row: pbState %v e9State %v e9Dist %v (MB is hot-only: e9 Null)", r.DildoPbState, r.E9State, r.E9Dist)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("flagscan.Run did not stop")
	}
}
