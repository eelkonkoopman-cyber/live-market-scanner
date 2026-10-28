package main

import (
	"context"
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestSeedBbsFormingOnceUnderItsOwnKey(t *testing.T) {
	for _, k := range []string{"seeded.flagscans.v1", "seeded.sessreview.v1", "seeded.gapfade.v1", "seeded.advscan.v1",
		e9FlagSeedKey, c1mSeedKey, e9SlamSeedKey, dildoSeedKey, climaxSeedKey, bbsSeedKey} {
		if k == bbsFormingSeedKey {
			t.Fatalf("seed key %q is taken", k)
		}
	}
	h := newSeedHarness()
	h.meta[bbsSeedKey] = "1" // an existing database that already has the short scan
	seedScans(context.Background(), bbsFormingSeedKey, bbsFormingScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if strings.Join(h.saved, ",") != "bouncy-forming" || h.meta[bbsFormingSeedKey] != "1" {
		t.Fatalf("saved %v meta %v", h.saved, h.meta)
	}
	h.saved = nil
	seedScans(context.Background(), bbsFormingSeedKey, bbsFormingScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 {
		t.Fatal("re-seeded")
	}
}

func TestBbsFormingScanDefShape(t *testing.T) {
	d := bbsFormingScans()[0]
	if d.ID != "bouncy-forming" || d.Name != "Bouncy Ball Forming" || !d.Alert.Enabled || d.Alert.On != core.AlertOnEnter ||
		d.Alert.Sound != "bounceSoft" || d.Alert.CooldownSec != 900 || !d.Alert.Speak {
		t.Fatalf("%+v", d)
	}
	if len(d.Sort) != 1 || d.Sort[0].Field != core.FieldBbsDistPct || d.Sort[0].Dir != "asc" {
		t.Fatalf("sort %+v", d.Sort)
	}
	if strings.Join(d.Columns, ",") != "last,chgDay,bbsSupport,bbsDistPct,news" {
		t.Fatalf("cols %v", d.Columns)
	}
	has := map[string]bool{}
	for _, f := range d.Filter.Conds {
		has[f.Field] = true
	}
	if !has[core.FieldBbsState] || !has[core.FieldLast] || !has[core.FieldVol] {
		t.Fatalf("filters %+v", d.Filter)
	}
}
