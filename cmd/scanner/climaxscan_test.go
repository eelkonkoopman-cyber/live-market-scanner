package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/engine"
	"github.com/eelkonkoopman/marketscanner/internal/scan"
)

// ─── seeding the VOLUME CLIMAX scans (same harness as seedflags_test.go) ─

// Every earlier batch has been consumed on an existing install, so the climax
// scans come under their own NEW key, seed once, and never come back after
// the user deletes them.
func TestSeedClimaxScansOnceUnderTheirOwnKey(t *testing.T) {
	earlier := []string{"seeded.flagscans.v1", "seeded.sessreview.v1", "seeded.gapfade.v1", "seeded.advscan.v1",
		e9FlagSeedKey, c1mSeedKey, e9SlamSeedKey, dildoSeedKey}
	for _, k := range earlier {
		if k == climaxSeedKey {
			t.Fatalf("the climax scans need their own seed key, %q is taken", k)
		}
	}
	h := newSeedHarness()
	for _, k := range earlier {
		h.meta[k] = "1"
	}
	seedScans(context.Background(), climaxSeedKey, climaxScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if got := strings.Join(h.saved, ","); got != "climax-1m,climax-confirm" || strings.Join(h.installed, ",") != got {
		t.Fatalf("saved %v installed %v", h.saved, h.installed)
	}
	if climaxSeedKey != "seeded.climax.v1" || h.meta[climaxSeedKey] != "1" {
		t.Fatalf("key %q meta %v", climaxSeedKey, h.meta)
	}

	// Deleted by the user, then a restart: nothing is resurrected.
	h.saved, h.installed = nil, nil
	seedScans(context.Background(), climaxSeedKey, climaxScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 || len(h.installed) != 0 {
		t.Fatalf("re-seeded after its key was set: %v %v", h.saved, h.installed)
	}
}

func TestSeedClimaxScansKeepsAUserTunedOneAndRetriesAFailedSave(t *testing.T) {
	h := newSeedHarness()
	seedScans(context.Background(), climaxSeedKey, climaxScans(), h.getMeta, h.setMeta,
		[]core.ScanDef{{ID: climax1mID, Name: "mine"}}, h.save, h.install)
	if strings.Join(h.saved, ",") != climaxConfirmID || h.meta[climaxSeedKey] != "1" {
		t.Fatalf("an existing ID is never overwritten, the other still seeds: saved %v meta %v", h.saved, h.meta)
	}

	h = newSeedHarness()
	h.saveErr[climaxConfirmID] = errors.New("disk full")
	seedScans(context.Background(), climaxSeedKey, climaxScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if h.meta[climaxSeedKey] != "" {
		t.Fatal("a failed save must leave the key unset so the next boot retries")
	}
	h.metaErr = errors.New("db locked")
	h.saved = nil
	seedScans(context.Background(), climaxSeedKey, climaxScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 {
		t.Fatalf("an unreadable meta store must not risk a double seed: %v", h.saved)
	}
}

// The defs' exact shape, every name a real field constant.
func TestClimaxScanDefShapes(t *testing.T) {
	defs := climaxScans()
	if len(defs) != 2 {
		t.Fatalf("%d defs", len(defs))
	}
	c1, cf := defs[0], defs[1]
	if c1.ID != "climax-1m" || c1.Name != "Volume Climax" || !c1.Enabled || c1.Limit != 50 {
		t.Fatalf("climax-1m %+v", c1)
	}
	if cf.ID != "climax-confirm" || cf.Name != "Bottom Confirmed" || !cf.Enabled || cf.Limit != 50 {
		t.Fatalf("climax-confirm %+v", cf)
	}
	want1 := core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldClimaxAge, Cmp: core.CmpLTE, Value: 240},
		{Field: core.FieldLast, Cmp: core.CmpGTE, Value: 0.50},
		{Field: core.FieldClimaxDvol, Cmp: core.CmpGTE, Value: 50000},
	}}
	if !reflect.DeepEqual(c1.Filter, want1) {
		t.Fatalf("climax-1m filter %+v", c1.Filter)
	}
	wantC := core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldClimaxState, Cmp: core.CmpEq, Value: 2},
		{Field: core.FieldClimaxConfAge, Cmp: core.CmpLTE, Value: 240},
		{Field: core.FieldClimaxAge, Cmp: core.CmpLTE, Value: 1200},
	}}
	if !reflect.DeepEqual(cf.Filter, wantC) {
		t.Fatalf("climax-confirm filter %+v", cf.Filter)
	}
	if !reflect.DeepEqual(c1.Sort, []core.SortKey{{Field: core.FieldClimaxAge, Dir: "asc"}}) ||
		!reflect.DeepEqual(cf.Sort, []core.SortKey{{Field: core.FieldClimaxConfAge, Dir: "asc"}}) {
		t.Fatalf("sorts %+v %+v", c1.Sort, cf.Sort)
	}
	if got := strings.Join(c1.Columns, ","); got != "last,climaxDropPct,climaxVolX,climaxDvol,climaxLow,climaxAge,chgDay,vol,news" {
		t.Fatalf("climax-1m columns %s", got)
	}
	if got := strings.Join(cf.Columns, ","); got != "last,climaxConfPx,climaxLow,climaxDropPct,climaxVolX,climaxConfAge,chgDay,news" {
		t.Fatalf("climax-confirm columns %s", got)
	}
	for _, c := range []struct {
		d     core.ScanDef
		sound string
	}{{c1, "climax"}, {cf, "bottom"}} {
		want := core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: c.sound, CooldownSec: 600, Speak: true}
		if c.d.Alert != want {
			t.Fatalf("%s alert %+v, want %+v", c.d.ID, c.d.Alert, want)
		}
		for _, f := range c.d.Filter.Conds {
			if f.Field == core.FieldPmVolX {
				t.Fatalf("%s: the climax is whole-market, no pmVolX term", c.d.ID)
			}
		}
	}
	for _, f := range []string{core.FieldClimaxAge, core.FieldClimaxDropPct, core.FieldClimaxVolX, core.FieldClimaxWickPct,
		core.FieldClimaxDvol, core.FieldClimaxLow, core.FieldClimaxState, core.FieldClimaxConfAge, core.FieldClimaxConfPx} {
		if !strings.HasPrefix(f, "climax") {
			t.Fatalf("field %q", f)
		}
	}
	// A fresh slice per call.
	defs[0].Columns[0] = "mutated"
	if climaxScans()[0].Columns[0] != "last" {
		t.Fatal("seeded defs must not alias one Columns slice")
	}
}

// ─── end to end: BIYA's 11:03 climax, from the aggregates to two alerts ──

// The production path: a real Engine (Climax on) fed BIYA 2026-10-07's bars as
// per-second aggregates judges the 11:03 climax and the 11:04 confirmation;
// its snapshots, through a real Scanner seeded the way main() seeds it, raise
// exactly two ENTER alerts for BIYA: "Volume Climax" (the climax tone) and then
// "Bottom Confirmed" (the bottom tone).
func TestClimaxEndToEndBIYA(t *testing.T) {
	raw, err := os.ReadFile("../../web/src/signals/fixtures/climax/climax-biya-2026-10-07.json")
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
	climaxTs := time.Date(2026, 10, 7, 11, 3, 0, 0, et).UnixMilli()

	// The engine's clock: it starts before the first bar (a bar from before the
	// engine started is part-observed and never judged), then reads the
	// snapshot times set below - 11:04:03 first, so a climax BIYA may have made
	// on 10-06 is yesterday's and never shows. The 1 Hz judge runs on the real
	// wall clock, far past 2026-10-07 11:05, so every bar is judged as soon as
	// it lands.
	var clock atomic.Int64
	clock.Store(fx.Bars[0].T - 60_000)
	now := func() time.Time { return time.UnixMilli(clock.Load()) }
	setNow := func(ms int64) { clock.Store(ms) }
	eng := engine.New(engine.Options{TickHz: 20, Now: now, Climax: true})
	if eng.DildoHits() != nil {
		t.Fatal("dildo off: no hits channel")
	}
	setNow(climaxTs + 63_000)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()
	scn := scan.New(eng)
	go func() { _ = scn.Run(ctx) }()
	h := newSeedHarness()
	seedScans(ctx, climaxSeedKey, climaxScans(), h.getMeta, h.setMeta, nil, h.save, scn.Upsert)
	scn.List() // the upserts are in

	feedThrough := func(lastT int64) {
		for len(fx.Bars) > 0 && fx.Bars[0].T <= lastT {
			b := fx.Bars[0]
			fx.Bars = fx.Bars[1:]
			eng.Apply(core.Agg{Sym: "BIYA", Open: b.O, High: b.H, Low: b.L, Close: b.C, Vol: b.V, StartMs: b.T, EndMs: b.T + 1000})
		}
	}
	alerts := func(until time.Duration) []core.AlertMsg {
		var out []core.AlertMsg
		deadline := time.After(until)
		for {
			select {
			case a := <-scn.Alerts():
				if a.Sym == "BIYA" {
					out = append(out, a)
				}
			case <-deadline:
				return out
			}
		}
	}

	// Through the 11:03 climax bar and one more print so the judge reaches it;
	// the scanner's clock reads 11:04:03 (the climax bar began 63 s ago).
	feedThrough(climaxTs)
	got := alerts(2 * time.Second)
	if len(got) != 1 || got[0].ScanID != climax1mID || got[0].Sound != "climax" || got[0].Kind != "enter" {
		t.Fatalf("after the 11:03 bar: alerts %+v, want one Volume Climax enter", got)
	}
	if v, ok := got[0].Row[core.FieldClimaxLow].(float64); !ok || v != 1.78 {
		t.Fatalf("alert row %+v, want climaxLow 1.78", got[0].Row)
	}
	if v, ok := got[0].Row[core.FieldClimaxAge].(float64); !ok || v < 63 || v > 64 {
		t.Fatalf("alert row climaxAge %v, want ~63 s", got[0].Row[core.FieldClimaxAge])
	}

	// Through the 11:04 confirming bar.
	feedThrough(climaxTs + 60_000)
	setNow(climaxTs + 123_000)
	got = alerts(2 * time.Second)
	if len(got) != 1 || got[0].ScanID != climaxConfirmID || got[0].Sound != "bottom" {
		t.Fatalf("after the 11:04 bar: alerts %+v, want exactly one Bottom Confirmed enter", got)
	}
	if v, ok := got[0].Row[core.FieldClimaxConfPx].(float64); !ok || v != 1.879 {
		t.Fatalf("confirm row %+v, want climaxConfPx 1.879", got[0].Row)
	}
	if r, ok := scn.Row("BIYA"); !ok || r.ClimaxState != 2 || r.ClimaxLow != 1.78 {
		t.Fatalf("scanner row %+v", r)
	}
}
