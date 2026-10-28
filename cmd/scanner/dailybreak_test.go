package main

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestSeedDailyBreakOnceUnderOwnKey(t *testing.T) {
	h := newSeedHarness()
	seedScans(context.Background(), dailyBreakSeedKey, dailyBreakScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if strings.Join(h.saved, ",") != dailyBreakID || strings.Join(h.installed, ",") != dailyBreakID {
		t.Fatalf("saved=%v installed=%v", h.saved, h.installed)
	}
	if dailyBreakSeedKey != "seeded.dailybreak.v1" || h.meta[dailyBreakSeedKey] != "1" {
		t.Fatalf("meta not set: %v", h.meta)
	}
	h.saved, h.installed = nil, nil
	seedScans(context.Background(), dailyBreakSeedKey, dailyBreakScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 || len(h.installed) != 0 {
		t.Fatalf("re-seeded: %v %v", h.saved, h.installed)
	}
}

func TestDailyBreakScanShape(t *testing.T) {
	d := dailyBreakScans()[0]
	if d.ID != "daily-breakout" || d.Name != "Daily Breakout" || !d.Enabled {
		t.Fatalf("identity: %+v", d)
	}
	want := core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldLast, Cmp: core.CmpGT, Value: 30},
		{Field: core.FieldAvgVol, Cmp: core.CmpGTE, Value: 500000},
		{Field: core.FieldDbPct, Cmp: core.CmpGT, Value: 0},
	}}
	if !reflect.DeepEqual(d.Filter, want) {
		t.Fatalf("filter = %+v", d.Filter)
	}
	if !reflect.DeepEqual(d.Sort, []core.SortKey{{Field: core.FieldDbType, Dir: "asc"}, {Field: core.FieldDbPct, Dir: "asc"}}) {
		t.Fatalf("sort = %+v", d.Sort)
	}
	if d.Alert.Enabled || !d.Alert.Speak {
		t.Fatalf("alert must be off by default and spoken: %+v", d.Alert)
	}
}

func TestComputeDailyLevelsPriceFloorAndPDHGuard(t *testing.T) {
	mk := func(last string, c float64) []core.DailyBar {
		return []core.DailyBar{
			{Date: "2026-10-12", H: c + 1, C: c}, {Date: "2026-10-13", H: c + 2, C: c},
			{Date: last, H: c + 3, C: c},
		}
	}
	all := map[string][]core.DailyBar{
		"HI":    mk("2026-10-15", 50),
		"LO":    mk("2026-10-15", 10), // closed under the floor: no levels
		"STALE": mk("2026-10-14", 50), // newest bar is not the previous weekday: no PDH level
	}
	out := computeDailyLevels(all, "2026-10-16")
	if _, ok := out["LO"]; ok {
		t.Fatal("sub-$25 name must have no levels")
	}
	if lv := out["HI"]; len(lv) != 1 || lv[0].Price != 53 { // PDH == 14D high collapses
		t.Fatalf("HI = %+v", out["HI"])
	}
	if lv := out["STALE"]; len(lv) != 1 || lv[0].Price != 53 {
		t.Fatalf("STALE = %+v", out["STALE"])
	}
	if prevWeekday("2026-10-12") != "2026-10-09" { // Monday -> Friday
		t.Fatal("prevWeekday over a weekend")
	}
}
