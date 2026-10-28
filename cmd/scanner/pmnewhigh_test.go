package main

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestSeedPmNewHighOnceUnderOwnKey(t *testing.T) {
	h := newSeedHarness()
	for _, k := range []string{"seeded.flagscans.v1", "seeded.c1m.v1", "seeded.ignition.v1"} {
		h.meta[k] = "1"
	}
	seedScans(context.Background(), pmNewHighSeedKey, pmNewHighScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if strings.Join(h.saved, ",") != pmNewHighID || strings.Join(h.installed, ",") != pmNewHighID {
		t.Fatalf("saved=%v installed=%v", h.saved, h.installed)
	}
	if pmNewHighSeedKey != "seeded.pmnewhigh.v1" || h.meta[pmNewHighSeedKey] != "1" {
		t.Fatalf("meta not set: %v", h.meta)
	}
	// Deleted by the user, restarted: never resurrected.
	h.saved, h.installed = nil, nil
	seedScans(context.Background(), pmNewHighSeedKey, pmNewHighScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 || len(h.installed) != 0 {
		t.Fatalf("re-seeded: %v %v", h.saved, h.installed)
	}
}

func TestPmNewHighScanShape(t *testing.T) {
	defs := pmNewHighScans()
	if len(defs) != 1 {
		t.Fatalf("%d defs", len(defs))
	}
	d := defs[0]
	if d.ID != "pm-new-high" || d.Name != "PM New High" || !d.Enabled {
		t.Fatalf("identity: %+v", d)
	}
	wantFilter := core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldSession, Cmp: core.CmpEq, Value: "pre"},
		{Field: core.FieldRvol, Cmp: core.CmpGTE, Value: 2},
		{Field: core.FieldVsHod, Cmp: core.CmpGTE, Value: 0},
	}}
	if !reflect.DeepEqual(d.Filter, wantFilter) {
		t.Fatalf("filter = %+v", d.Filter)
	}
	if !reflect.DeepEqual(d.Sort, []core.SortKey{{Field: core.FieldRvol, Dir: "desc"}}) {
		t.Fatalf("sort = %+v", d.Sort)
	}
	if !reflect.DeepEqual(d.Columns, []string{"last", "chgDay", "rvol", "vol", "news"}) {
		t.Fatalf("columns = %v", d.Columns)
	}
	want := core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: "ping", CooldownSec: 120, RequireReentry: true, Speak: true}
	if d.Alert != want {
		t.Fatalf("alert = %+v", d.Alert)
	}
}
