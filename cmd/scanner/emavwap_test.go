package main

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestSeedEmaVwapScansOnceEachUnderTheirOwnKey(t *testing.T) {
	if emaVwapBullSeedKey != "seeded.emavwap.bull.v1" || emaVwapBearSeedKey != "seeded.emavwap.bear.v1" {
		t.Fatal(emaVwapBullSeedKey, emaVwapBearSeedKey)
	}
	h := newSeedHarness()
	seedScans(context.Background(), emaVwapBullSeedKey, emaVwapBullScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	seedScans(context.Background(), emaVwapBearSeedKey, emaVwapBearScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if strings.Join(h.saved, ",") != "vwap-bull,vwap-bear" || h.meta[emaVwapBullSeedKey] != "1" || h.meta[emaVwapBearSeedKey] != "1" {
		t.Fatalf("saved %v meta %v", h.saved, h.meta)
	}
	// deleted by the user: the keys keep them from coming back
	h.saved = nil
	seedScans(context.Background(), emaVwapBullSeedKey, emaVwapBullScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	seedScans(context.Background(), emaVwapBearSeedKey, emaVwapBearScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 {
		t.Fatalf("re-seeded %v", h.saved)
	}
}

func TestEmaVwapScanShape(t *testing.T) {
	for _, c := range []struct {
		d         core.ScanDef
		name, age string
	}{
		{emaVwapBullScans()[0], "VWAP Bull Cross", core.FieldEvBullAge},
		{emaVwapBearScans()[0], "VWAP Bear Cross", core.FieldEvBearAge},
	} {
		d := c.d
		if d.Name != c.name || !d.Enabled || !d.Alert.Enabled || !d.Alert.Speak || d.Alert.On != core.AlertOnEnter {
			t.Fatalf("%+v", d)
		}
		// the universe: last > $25, avgVol >= 1M, RVOL >= 1.5, then the fresh cross
		want := core.Filter{Op: "and", Conds: []core.Filter{
			{Field: core.FieldLast, Cmp: core.CmpGT, Value: 25},
			{Field: core.FieldAvgVol, Cmp: core.CmpGTE, Value: 1000000},
			{Field: core.FieldRvol, Cmp: core.CmpGTE, Value: 1.5},
			{Field: c.age, Cmp: core.CmpLTE, Value: emaVwapFreshS},
			{Field: core.FieldEvBarDvol, Cmp: core.CmpGTE, Value: 500000},
			{Field: core.FieldEvSessVol, Cmp: core.CmpGTE, Value: 500000},
			{Field: core.FieldSession, Cmp: core.CmpEq, Value: "regular"},
		}}
		if !reflect.DeepEqual(d.Filter, want) {
			t.Fatalf("filter %+v", d.Filter)
		}
		cols := []string{"last", "evEma", "evVwap", "evDist", "rvol", "avgVol", "evBarDvol", "evSessVol", c.age, "chgDay"}
		if !reflect.DeepEqual(d.Columns, cols) {
			t.Fatalf("columns %v", d.Columns)
		}
	}
}

func TestEmaVwapVolFloorSeedAndMigration(t *testing.T) {
	ctx := context.Background()
	for _, id := range []string{emaVwapBullID, emaVwapBearID} {
		var d core.ScanDef
		if id == emaVwapBullID {
			d = emaVwapBullScans()[0]
		} else {
			d = emaVwapBearScans()[0]
		}
		c := d.Filter.Conds
		n := len(c)
		if n != 7 || c[4].Field != core.FieldEvBarDvol || c[4].Cmp != core.CmpGTE || c[4].Value != 500000 ||
			c[5].Field != core.FieldEvSessVol || c[5].Value != 500000 ||
			c[6].Field != core.FieldSession || c[6].Cmp != core.CmpEq || c[6].Value != "regular" {
			t.Fatalf("%s seed conds %+v", id, c)
		}
		if !slices.Contains(d.Columns, "evBarDvol") || !slices.Contains(d.Columns, "evSessVol") {
			t.Fatalf("columns %v", d.Columns)
		}
	}
	// the pre-floor seed, as the store returns it (JSON round trip)
	old := func(id, age string) core.ScanDef {
		d := emaVwapScan(id, id, "chime", age)
		d.Filter.Conds = d.Filter.Conds[:4]
		b, _ := json.Marshal(d)
		var r core.ScanDef
		_ = json.Unmarshal(b, &r)
		return r
	}
	run := func(h *seedHarness, have []core.ScanDef) {
		migrateEmaVwapVolFloor(ctx, h.getMeta, h.setMeta, func(context.Context) ([]core.ScanDef, error) { return have, nil }, h.save, h.install)
	}
	h := newSeedHarness()
	run(h, []core.ScanDef{old(emaVwapBullID, core.FieldEvBullAge), old(emaVwapBearID, core.FieldEvBearAge)})
	if strings.Join(h.saved, ",") != "vwap-bull,vwap-bear" || h.meta[emaVwapVolFloorKey] != "1" {
		t.Fatalf("saved %v meta %v", h.saved, h.meta)
	}
	h.saved = nil
	run(h, []core.ScanDef{old(emaVwapBullID, core.FieldEvBullAge)})
	if len(h.saved) != 0 {
		t.Fatal("ran twice")
	}
	// bull edited (rvol 3): skipped; bear unedited: migrated; key set
	h = newSeedHarness()
	ed := old(emaVwapBullID, core.FieldEvBullAge)
	ed.Filter.Conds[2].Value = 3.0
	run(h, []core.ScanDef{ed, old(emaVwapBearID, core.FieldEvBearAge)})
	if strings.Join(h.saved, ",") != "vwap-bear" || h.meta[emaVwapVolFloorKey] != "1" {
		t.Fatalf("saved %v meta %v", h.saved, h.meta)
	}
}
