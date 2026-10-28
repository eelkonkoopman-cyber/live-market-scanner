package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestSeedC1mSpikeScanOnceUnderOwnKey(t *testing.T) {
	h := newSeedHarness()
	// An existing install: every earlier batch already ran; this one must still seed.
	for _, k := range []string{"seeded.flagscans.v1", "seeded.sessreview.v1", "seeded.gapfade.v1", "seeded.advscan.v1", "seeded.e9flag.v1"} {
		h.meta[k] = "1"
	}
	seedScans(context.Background(), c1mSeedKey, c1mSpikeScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if got := strings.Join(h.saved, ","); got != c1mSpikeID {
		t.Fatalf("saved = %v, want [%s]", h.saved, c1mSpikeID)
	}
	if got := strings.Join(h.installed, ","); got != c1mSpikeID {
		t.Fatalf("installed = %v", h.installed)
	}
	if c1mSeedKey != "seeded.c1m.v1" || h.meta["seeded.c1m.v1"] != "1" {
		t.Fatalf("meta flag %q must be set after a successful seed: %v", c1mSeedKey, h.meta)
	}

	// The user deletes it and restarts: the meta key stays set, nothing comes back.
	h.saved, h.installed = nil, nil
	seedScans(context.Background(), c1mSeedKey, c1mSpikeScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 || len(h.installed) != 0 {
		t.Fatalf("re-seeded after meta was set: saved=%v installed=%v", h.saved, h.installed)
	}
}

func TestSeedC1mSpikeScanDoesNotOverwriteExistingID(t *testing.T) {
	h := newSeedHarness()
	have := []core.ScanDef{{ID: c1mSpikeID, Name: "user-tuned"}}
	seedScans(context.Background(), c1mSeedKey, c1mSpikeScans(), h.getMeta, h.setMeta, have, h.save, h.install)
	if len(h.saved) != 0 {
		t.Fatalf("an existing ID must not be overwritten: saved %v", h.saved)
	}
	h2 := newSeedHarness()
	h2.saveErr[c1mSpikeID] = errors.New("disk full")
	seedScans(context.Background(), c1mSeedKey, c1mSpikeScans(), h2.getMeta, h2.setMeta, nil, h2.save, h2.install)
	if h2.meta[c1mSeedKey] != "" {
		t.Fatal("meta flag must stay unset when the save fails, so the next boot retries")
	}
}

func TestC1mSpikeScanShape(t *testing.T) {
	defs := c1mSpikeScans()
	if len(defs) != 1 {
		t.Fatalf("%d defs, want 1", len(defs))
	}
	d := defs[0]
	if d.ID != "c1m-spike" || d.Name != "1m Candle Spike" || !d.Enabled || d.Limit != 50 {
		t.Fatalf("identity/limit: %+v", d)
	}
	wantFilter := core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldC1mChg, Cmp: core.CmpGTE, Value: 5},
		{Field: core.FieldC1mRvol, Cmp: core.CmpGTE, Value: 3},
	}}
	if !reflect.DeepEqual(d.Filter, wantFilter) {
		t.Fatalf("filter = %+v", d.Filter)
	}
	wantSort := []core.SortKey{{Field: core.FieldC1mChg, Dir: "desc"}, {Field: core.FieldC1mRvol, Dir: "desc"}}
	if !reflect.DeepEqual(d.Sort, wantSort) {
		t.Fatalf("sort = %+v", d.Sort)
	}
	wantCols := []string{"last", "c1mChg", "c1mRvol", "vol", "chgDay", "rvol", "news"}
	if !reflect.DeepEqual(d.Columns, wantCols) {
		t.Fatalf("columns = %v", d.Columns)
	}
	wantAlert := core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: "ping", CooldownSec: 120, RequireReentry: true}
	if d.Alert != wantAlert {
		t.Fatalf("alert = %+v", d.Alert)
	}
	if core.FieldC1mChg != "c1mChg" || core.FieldC1mRvol != "c1mRvol" {
		t.Fatal("wire keys must match the web registry")
	}
}
