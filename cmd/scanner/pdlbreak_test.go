package main

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestSeedPDLBreakOnceUnderOwnKey(t *testing.T) {
	h := newSeedHarness()
	seedScans(context.Background(), pdlBreakSeedKey, pdlBreakScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if strings.Join(h.saved, ",") != pdlBreakID || strings.Join(h.installed, ",") != pdlBreakID {
		t.Fatalf("saved=%v installed=%v", h.saved, h.installed)
	}
	if pdlBreakSeedKey != "seeded.pdlbreak.v1" || h.meta[pdlBreakSeedKey] != "1" {
		t.Fatalf("meta not set: %v", h.meta)
	}
	h.saved, h.installed = nil, nil
	seedScans(context.Background(), pdlBreakSeedKey, pdlBreakScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 || len(h.installed) != 0 {
		t.Fatalf("re-seeded: %v %v", h.saved, h.installed)
	}
}

func TestPDLBreakScanShape(t *testing.T) {
	d := pdlBreakScans()[0]
	if d.ID != "pdl-break" || d.Name != "PDL Break" || !d.Enabled {
		t.Fatalf("identity: %+v", d)
	}
	want := core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldLast, Cmp: core.CmpGT, Value: 25},
		{Field: core.FieldAvgVol, Cmp: core.CmpGTE, Value: 500000},
		{Field: core.FieldPdlPct, Cmp: core.CmpGT, Value: 0},
	}}
	if !reflect.DeepEqual(d.Filter, want) {
		t.Fatalf("filter = %+v", d.Filter)
	}
	if !reflect.DeepEqual(d.Sort, []core.SortKey{{Field: core.FieldPdlPct, Dir: "asc"}}) {
		t.Fatalf("sort = %+v", d.Sort)
	}
	if !d.Alert.Enabled || !d.Alert.Speak {
		t.Fatalf("alert must be on and spoken: %+v", d.Alert)
	}
}

func TestComputePrevLows(t *testing.T) {
	mk := func(last string, c float64) []core.DailyBar {
		return []core.DailyBar{{Date: "2026-10-13", L: c - 3, C: c}, {Date: last, L: c - 2, C: c}}
	}
	out := computePrevLows(map[string][]core.DailyBar{
		"HI": mk("2026-10-15", 50), "LO": mk("2026-10-15", 10), "STALE": mk("2026-10-14", 50),
	}, "2026-10-16")
	if len(out) != 1 || out["HI"] != 48 {
		t.Fatalf("got %v", out)
	}
}
