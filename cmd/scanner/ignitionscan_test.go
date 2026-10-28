package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/engine"
	"github.com/eelkonkoopman/marketscanner/internal/scan"
)

func TestSeedIgnitionScanOnceUnderItsOwnKey(t *testing.T) {
	for _, k := range []string{"seeded.flagscans.v1", "seeded.sessreview.v1", "seeded.gapfade.v1", "seeded.advscan.v1",
		e9FlagSeedKey, c1mSeedKey, e9SlamSeedKey, dildoSeedKey, climaxSeedKey, bbsSeedKey, bbsFormingSeedKey, hodFlagSeedKey} {
		if k == ignitionSeedKey {
			t.Fatalf("seed key %q is taken", k)
		}
	}
	h := newSeedHarness()
	seedScans(context.Background(), ignitionSeedKey, ignitionScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if strings.Join(h.saved, ",") != "ignition" || h.meta[ignitionSeedKey] != "1" {
		t.Fatalf("saved %v meta %v", h.saved, h.meta)
	}
	h.saved = nil
	seedScans(context.Background(), ignitionSeedKey, ignitionScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 {
		t.Fatal("re-seeded")
	}
}

func TestIgnitionScanDefShape(t *testing.T) {
	d := ignitionScans()[0]
	if d.Name != "Ignition" || !d.Alert.Enabled || d.Alert.On != core.AlertOnEnter || d.Alert.Sound != "ignite" ||
		d.Alert.CooldownSec != 600 || !d.Alert.Speak {
		t.Fatalf("%+v", d)
	}
}

// End to end: a real Engine (Ignition on) fed the synthetic NCPL morning from
// the golden file raises exactly one "Ignition" enter for the 09:06 candle.
func TestIgnitionEndToEndNCPL(t *testing.T) {
	old := ignitionScans()
	old[0].Filter.Conds = old[0].Filter.Conds[:1] // the pre-skip-rules seed: alerts on every ignition
	if got := runIgnitionNCPL(t, old); len(got) != 1 || got[0].ScanID != ignitionScanID || got[0].Sound != "ignite" || got[0].Kind != "enter" {
		t.Fatalf("alerts %+v", got)
	} else if v, ok := got[0].Row[core.FieldIgnVolX].(float64); !ok || v != 15 {
		t.Fatalf("row %+v", got[0].Row)
	}
	// The current seed (ignPass == 1): NCPL's ignition is skipped (unknown day
	// change, $564k candle), so it does not alert.
	if got := runIgnitionNCPL(t, ignitionScans()); len(got) != 0 {
		t.Fatalf("skipped ignition alerted: %+v", got)
	}
}

func runIgnitionNCPL(t *testing.T, defs []core.ScanDef) []core.AlertMsg {
	t.Helper()
	raw, err := os.ReadFile("../../web/src/signals/fixtures/golden/ignition-golden.json")
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
	var bars []core.Bar
	for _, s := range g.Series {
		if s.Name == "ncpl" {
			bars = s.Bars
		}
	}
	const week = 7 * 86_400_000
	for i := range bars {
		bars[i].T -= week
	}
	et, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tz database:", err)
	}
	ignTs := time.Date(2026, 10, 7, 9, 6, 0, 0, et).UnixMilli()
	var clock atomic.Int64
	clock.Store(bars[0].T - 60_000)
	eng := engine.New(engine.Options{TickHz: 20, Now: func() time.Time { return time.UnixMilli(clock.Load()) }, Ignition: true})
	clock.Store(ignTs + 63_000)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()
	scn := scan.New(eng)
	go func() { _ = scn.Run(ctx) }()
	h := newSeedHarness()
	seedScans(ctx, ignitionSeedKey, defs, h.getMeta, h.setMeta, nil, h.save, scn.Upsert)
	scn.List()
	for _, b := range bars {
		if b.T > ignTs {
			break
		}
		eng.Apply(core.Agg{Sym: "NCPL", Open: b.O, High: b.H, Low: b.L, Close: b.C, Vol: b.V, StartMs: b.T, EndMs: b.T + 1000})
	}
	var got []core.AlertMsg
	deadline := time.After(2 * time.Second)
loop:
	for {
		select {
		case a := <-scn.Alerts():
			if a.Sym == "NCPL" {
				got = append(got, a)
			}
		case <-deadline:
			break loop
		}
	}
	return got
}

// A new database gets the ignPass condition in the seed.
func TestIgnitionSeedRequiresPass(t *testing.T) {
	c := ignitionScans()[0].Filter.Conds
	if len(c) != 2 || c[1].Field != core.FieldIgnPass || c[1].Cmp != core.CmpEq || c[1].Value != 1 {
		t.Fatalf("%+v", c)
	}
}

// The migration adds ignPass == 1 once, and only to an unedited seeded scan.
func TestMigrateIgnitionPass(t *testing.T) {
	ctx := context.Background()
	oldSeed := func() core.ScanDef {
		d := ignitionScans()[0]
		d.Filter.Conds = d.Filter.Conds[:1]
		// round-trip through JSON like the store (numbers come back float64)
		b, _ := json.Marshal(d)
		var r core.ScanDef
		_ = json.Unmarshal(b, &r)
		return r
	}
	run := func(h *seedHarness, have []core.ScanDef) {
		migrateIgnitionPass(ctx, h.getMeta, h.setMeta, func(context.Context) ([]core.ScanDef, error) { return have, nil }, h.save, h.install)
	}
	// unedited: gets it, once
	h := newSeedHarness()
	run(h, []core.ScanDef{oldSeed()})
	if strings.Join(h.saved, ",") != "ignition" || h.meta[ignitionPassKey] != "1" {
		t.Fatalf("saved %v meta %v", h.saved, h.meta)
	}
	h.saved = nil
	run(h, []core.ScanDef{oldSeed()})
	if len(h.saved) != 0 {
		t.Fatal("ran twice")
	}
	// edited (cooldown changed): untouched, key still set
	h = newSeedHarness()
	ed := oldSeed()
	ed.Alert.CooldownSec = 300
	run(h, []core.ScanDef{ed})
	if len(h.saved) != 0 || h.meta[ignitionPassKey] != "1" {
		t.Fatalf("edited scan migrated: %v", h.saved)
	}
	// edited filter: untouched
	h = newSeedHarness()
	ed = oldSeed()
	ed.Filter.Conds[0].Value = 120.0
	run(h, []core.ScanDef{ed})
	if len(h.saved) != 0 {
		t.Fatal("edited filter migrated")
	}
	// deleted: nothing saved
	h = newSeedHarness()
	run(h, nil)
	if len(h.saved) != 0 || h.meta[ignitionPassKey] != "1" {
		t.Fatal("deleted")
	}
}

func TestSeedPowerCandleScanOnceUnderItsOwnKey(t *testing.T) {
	if powerCandleSeedKey != "seeded.powercandle.v1" {
		t.Fatal(powerCandleSeedKey)
	}
	h := newSeedHarness()
	seedScans(context.Background(), powerCandleSeedKey, powerCandleScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if strings.Join(h.saved, ",") != "power-candle" || h.meta[powerCandleSeedKey] != "1" {
		t.Fatalf("saved %v meta %v", h.saved, h.meta)
	}
	h.saved = nil
	seedScans(context.Background(), powerCandleSeedKey, powerCandleScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 {
		t.Fatal("re-seeded")
	}
	d := powerCandleScans()[0]
	if d.Name != "Power Candle" || !d.Alert.Enabled || d.Alert.Sound != "bell" || !d.Alert.Speak {
		t.Fatalf("%+v", d)
	}
}

func TestSeedPowerSetupScanOnceNeverResurrected(t *testing.T) {
	if powerSetupSeedKey != "seeded.powersetup.v1" {
		t.Fatal(powerSetupSeedKey)
	}
	h := newSeedHarness()
	seedScans(context.Background(), powerSetupSeedKey, powerSetupScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if strings.Join(h.saved, ",") != "power-setup" || h.meta[powerSetupSeedKey] != "1" {
		t.Fatalf("saved %v meta %v", h.saved, h.meta)
	}
	// the user deletes it: the key stays set, a restart brings nothing back
	h.saved = nil
	seedScans(context.Background(), powerSetupSeedKey, powerSetupScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 {
		t.Fatal("re-seeded / resurrected")
	}
	d := powerSetupScans()[0]
	if d.Name != "Power Setup" || !d.Enabled || !d.Alert.Enabled || d.Alert.Sound != "psetup" || !d.Alert.Speak || d.Alert.On != core.AlertOnEnter {
		t.Fatalf("%+v", d)
	}
	f := d.Filter.Conds
	if len(f) != 2 || f[0].Field != core.FieldPsPass || f[0].Value != 1 || f[1].Field != core.FieldPsAge || f[1].Value != core.PsEntryValidSec {
		t.Fatalf("filter %+v", f)
	}
}

// The A+ migration appends psAplus / psBaseRange to an existing Power Setup scan once.
func TestMigratePowerSetupAplus(t *testing.T) {
	ctx := context.Background()
	old := powerSetupScans()[0]
	old.Columns = []string{"last", "psEntry", "psAge"}
	other := ignitionScans()[0]
	var got core.ScanDef
	h := newSeedHarness()
	list := func(context.Context) ([]core.ScanDef, error) { return []core.ScanDef{other, old}, nil }
	migratePowerSetupAplus(ctx, h.getMeta, h.setMeta, list, h.save, func(d core.ScanDef) { got = d })
	if strings.Join(h.saved, ",") != "power-setup" || h.meta[powerSetupAplusKey] != "1" || strings.Join(got.Columns, ",") != "last,psEntry,psAge,psAplus,psBaseRange" {
		t.Fatalf("saved %v meta %v cols %v", h.saved, h.meta, got.Columns)
	}
	h.saved = nil
	migratePowerSetupAplus(ctx, h.getMeta, h.setMeta, list, h.save, func(core.ScanDef) {})
	if len(h.saved) != 0 {
		t.Fatal("ran twice")
	}
}
