package main

import (
	"context"
	"errors"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// seedHarness fakes the meta store + repo + scanner install hook.
type seedHarness struct {
	meta      map[string]string
	metaErr   error
	saved     []string
	saveErr   map[string]error
	installed []string
}

func newSeedHarness() *seedHarness {
	return &seedHarness{meta: map[string]string{}, saveErr: map[string]error{}}
}

func (h *seedHarness) getMeta(_ context.Context, k string) (string, error) {
	return h.meta[k], h.metaErr
}
func (h *seedHarness) setMeta(_ context.Context, k, v string) error {
	h.meta[k] = v
	return nil
}
func (h *seedHarness) save(_ context.Context, d core.ScanDef) error {
	if err := h.saveErr[d.ID]; err != nil {
		return err
	}
	h.saved = append(h.saved, d.ID)
	return nil
}
func (h *seedHarness) install(d core.ScanDef) { h.installed = append(h.installed, d.ID) }

func TestSeedFlagScansSeedsBothOnceAndSetsMeta(t *testing.T) {
	h := newSeedHarness()
	seedScans(context.Background(), "seeded.flagscans.v1", flagScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 2 || h.saved[0] != "flags-intraday" || h.saved[1] != "flags-daily" {
		t.Fatalf("saved = %v, want both flag scans", h.saved)
	}
	if len(h.installed) != 2 {
		t.Fatalf("installed = %v", h.installed)
	}
	if h.meta["seeded.flagscans.v1"] != "1" {
		t.Fatal("meta flag not set after a successful seed")
	}

	// Second boot: meta set -> nothing happens, even with an empty def list
	// (this is what keeps a deliberate deletion deleted).
	h.saved, h.installed = nil, nil
	seedScans(context.Background(), "seeded.flagscans.v1", flagScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 || len(h.installed) != 0 {
		t.Fatalf("re-seeded after meta was set: saved=%v installed=%v", h.saved, h.installed)
	}
}

func TestSeedFlagScansSkipsExistingIDButSeedsTheOther(t *testing.T) {
	h := newSeedHarness()
	have := []core.ScanDef{{ID: "flags-intraday", Name: "user-tuned"}}
	seedScans(context.Background(), "seeded.flagscans.v1", flagScans(), h.getMeta, h.setMeta, have, h.save, h.install)
	if len(h.saved) != 1 || h.saved[0] != "flags-daily" {
		t.Fatalf("saved = %v, want only flags-daily (existing ID must not be overwritten)", h.saved)
	}
	if h.meta["seeded.flagscans.v1"] != "1" {
		t.Fatal("meta flag should still be set")
	}
}

func TestSeedFlagScansSaveFailureRetriesNextBoot(t *testing.T) {
	h := newSeedHarness()
	h.saveErr["flags-intraday"] = errors.New("disk full")
	seedScans(context.Background(), "seeded.flagscans.v1", flagScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if h.meta["seeded.flagscans.v1"] != "" {
		t.Fatal("meta flag must stay unset when a save fails, so the next boot retries")
	}
	if len(h.installed) != 0 {
		t.Fatalf("a failed save must not install: %v", h.installed)
	}
}

func TestSeedFlagScansMetaErrorDoesNotSeed(t *testing.T) {
	h := newSeedHarness()
	h.metaErr = errors.New("db locked")
	seedScans(context.Background(), "seeded.flagscans.v1", flagScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 {
		t.Fatalf("an unreadable meta store must not risk double-seeding: %v", h.saved)
	}
}

func TestSeedScansSessReviewSeedsOnceUnderOwnKey(t *testing.T) {
	h := newSeedHarness()
	seedScans(context.Background(), "seeded.sessreview.v1", sessReviewScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 1 || h.saved[0] != "premkt-movers" {
		t.Fatalf("saved = %v, want only premkt-movers", h.saved)
	}
	if len(h.installed) != 1 || h.installed[0] != "premkt-movers" {
		t.Fatalf("installed = %v", h.installed)
	}
	if h.meta["seeded.sessreview.v1"] != "1" {
		t.Fatal("sessreview meta flag not set after a successful seed")
	}
	if h.meta["seeded.flagscans.v1"] != "" {
		t.Fatal("seeding sessreview must not touch the flag-scans key")
	}

	// User deletes the scan, then restarts: meta stays set, so an empty def
	// list must NOT resurrect it.
	h.saved, h.installed = nil, nil
	seedScans(context.Background(), "seeded.sessreview.v1", sessReviewScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 || len(h.installed) != 0 {
		t.Fatalf("re-seeded after meta was set: saved=%v installed=%v", h.saved, h.installed)
	}
	if h.meta["seeded.sessreview.v1"] != "1" {
		t.Fatal("meta flag must stay set across the no-op boot")
	}
}

func TestSeedScansKeysAreIndependent(t *testing.T) {
	// An existing install that already ran the flag seed (key set) must still
	// pick up the newer session-review seed under its own key.
	h := newSeedHarness()
	h.meta["seeded.flagscans.v1"] = "1"
	seedScans(context.Background(), "seeded.flagscans.v1", flagScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 {
		t.Fatalf("flag key already set, flag batch must not seed: %v", h.saved)
	}
	seedScans(context.Background(), "seeded.sessreview.v1", sessReviewScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 1 || h.saved[0] != "premkt-movers" {
		t.Fatalf("saved = %v, want only premkt-movers", h.saved)
	}
	if h.meta["seeded.sessreview.v1"] != "1" {
		t.Fatal("sessreview meta flag not set")
	}
}

func TestSeedScansGapFadeSeedsOnceUnderOwnKey(t *testing.T) {
	h := newSeedHarness()
	// Existing install: both earlier batch keys already set — the gap-fade
	// batch must still seed under ITS OWN key (seed-key independence).
	h.meta["seeded.flagscans.v1"] = "1"
	h.meta["seeded.sessreview.v1"] = "1"
	seedScans(context.Background(), "seeded.gapfade.v1", gapFadeScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 1 || h.saved[0] != "pm-gap-fade" {
		t.Fatalf("saved = %v, want only pm-gap-fade", h.saved)
	}
	if len(h.installed) != 1 || h.installed[0] != "pm-gap-fade" {
		t.Fatalf("installed = %v", h.installed)
	}
	if h.meta["seeded.gapfade.v1"] != "1" {
		t.Fatal("gapfade meta flag not set after a successful seed")
	}

	// User deletes the scan, then restarts: meta stays set, so an empty def
	// list must NOT resurrect it.
	h.saved, h.installed = nil, nil
	seedScans(context.Background(), "seeded.gapfade.v1", gapFadeScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 || len(h.installed) != 0 {
		t.Fatalf("re-seeded after meta was set: saved=%v installed=%v", h.saved, h.installed)
	}
}

// TestGapFadeScanDefsUseRealFields pins the gap-fade def the same way the
// other seeded batches are pinned (a typo'd field would silently match
// nothing forever), and additionally pins the SHAPE of the price band: a
// between on last with the exact [25, 250] pair — the encoding
// internal/scan's toPair must accept (TestPredicateGapFadeSeededShape proves
// the same encoding compiles and matches over there).
func TestGapFadeScanDefsUseRealFields(t *testing.T) {
	valid := map[string]bool{
		core.FieldLast: true, core.FieldChgDay: true, core.FieldRvol: true, core.FieldNews: true,
		core.FieldPreGap: true, core.FieldPreFade: true,
		core.FieldPreChg: true, core.FieldPreVol: true, core.FieldPreHigh: true,
	}
	for _, d := range gapFadeScans() {
		if !d.Enabled {
			t.Fatalf("%s: must seed enabled", d.ID)
		}
		if d.Alert.Enabled {
			t.Fatalf("%s: a review list that fills at 09:30 must not fire enter alerts", d.ID)
		}
		if d.Alert.Sound == "" {
			t.Fatalf("%s: keep a sound value so the editor renders sanely", d.ID)
		}
		var sawBand, sawGap, sawFade bool
		for _, c := range d.Filter.Conds {
			if !valid[c.Field] {
				t.Fatalf("%s: filter references unexpected field %q", d.ID, c.Field)
			}
			switch c.Field {
			case core.FieldLast:
				if c.Cmp != core.CmpBetween {
					t.Fatalf("%s: price band must be a between, got %q", d.ID, c.Cmp)
				}
				pair, ok := c.Value.([]any)
				if !ok || len(pair) != 2 || pair[0] != 25 || pair[1] != 250 {
					t.Fatalf("%s: price band must be [25 250], got %v", d.ID, c.Value)
				}
				sawBand = true
			case core.FieldPreGap:
				if c.Cmp != core.CmpGTE {
					t.Fatalf("%s: preGap condition must be >=, got %q", d.ID, c.Cmp)
				}
				sawGap = true
			case core.FieldPreFade:
				if c.Cmp != core.CmpGTE {
					t.Fatalf("%s: preFade condition must be >=, got %q", d.ID, c.Cmp)
				}
				sawFade = true
			}
		}
		if !sawBand || !sawGap || !sawFade {
			t.Fatalf("%s: filter must include the price band, preGap and preFade (band=%v gap=%v fade=%v)",
				d.ID, sawBand, sawGap, sawFade)
		}
		for _, s := range d.Sort {
			if !valid[s.Field] {
				t.Fatalf("%s: sort references unexpected field %q", d.ID, s.Field)
			}
		}
		for _, col := range d.Columns {
			if !valid[col] {
				t.Fatalf("%s: column references unexpected field %q", d.ID, col)
			}
		}
	}
}

// Same trap as TestFlagScanDefsUseRealFields: a typo'd field name in the
// seeded review def would compile fine but silently match (or show) nothing
// forever — pin every filter, sort, and column name against the core consts.
func TestSessReviewScanDefsUseRealFields(t *testing.T) {
	valid := map[string]bool{
		core.FieldLast: true, core.FieldChgDay: true, core.FieldRvol: true, core.FieldNews: true,
		core.FieldPreChg: true, core.FieldPreVol: true, core.FieldPreHigh: true,
	}
	for _, d := range sessReviewScans() {
		if !d.Enabled {
			t.Fatalf("%s: must seed enabled", d.ID)
		}
		if d.Alert.Enabled {
			t.Fatalf("%s: a review list that fills at 09:30 must not fire enter alerts", d.ID)
		}
		if d.Alert.Sound == "" {
			t.Fatalf("%s: keep a sound value so the editor renders sanely", d.ID)
		}
		for _, c := range d.Filter.Conds {
			if !valid[c.Field] {
				t.Fatalf("%s: filter references unexpected field %q", d.ID, c.Field)
			}
		}
		for _, s := range d.Sort {
			if !valid[s.Field] {
				t.Fatalf("%s: sort references unexpected field %q", d.ID, s.Field)
			}
		}
		for _, col := range d.Columns {
			if !valid[col] {
				t.Fatalf("%s: column references unexpected field %q", d.ID, col)
			}
		}
	}
}

// The seeded defs must actually reference real, filterable fields — a typo'd
// field name here would compile fine but silently match nothing forever.
func TestFlagScanDefsUseRealFields(t *testing.T) {
	valid := map[string]bool{
		core.FieldIFlagState: true, core.FieldIFlagScore: true,
		core.FieldFlagState: true, core.FieldFlagScore: true,
	}
	for _, d := range flagScans() {
		if !d.Enabled || d.Alert.On != core.AlertOnCross || d.Alert.Trigger == nil {
			t.Fatalf("%s: enabled cross alert expected", d.ID)
		}
		for _, c := range d.Filter.Conds {
			if !valid[c.Field] {
				t.Fatalf("%s: filter references unexpected field %q", d.ID, c.Field)
			}
		}
		if !valid[d.Alert.Trigger.Field] {
			t.Fatalf("%s: alert trigger references unexpected field %q", d.ID, d.Alert.Trigger.Field)
		}
	}
}

// TestSeedScansPremarketVolSeedsOnceUnderOwnKey mirrors
// TestSeedScansGapFadeSeedsOnceUnderOwnKey: seed-key independence from every
// earlier batch, and a deleted scan must never be resurrected.
func TestSeedScansPremarketVolSeedsOnceUnderOwnKey(t *testing.T) {
	h := newSeedHarness()
	h.meta["seeded.flagscans.v1"] = "1"
	h.meta["seeded.sessreview.v1"] = "1"
	h.meta["seeded.gapfade.v1"] = "1"
	seedScans(context.Background(), "seeded.advscan.v1", premarketVolScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 1 || h.saved[0] != "premkt-vol-leaders" {
		t.Fatalf("saved = %v, want only premkt-vol-leaders", h.saved)
	}
	if len(h.installed) != 1 || h.installed[0] != "premkt-vol-leaders" {
		t.Fatalf("installed = %v", h.installed)
	}
	if h.meta["seeded.advscan.v1"] != "1" {
		t.Fatal("advscan meta flag not set after a successful seed")
	}

	h.saved, h.installed = nil, nil
	seedScans(context.Background(), "seeded.advscan.v1", premarketVolScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 || len(h.installed) != 0 {
		t.Fatalf("re-seeded after meta was set: saved=%v installed=%v", h.saved, h.installed)
	}
}

// TestPremarketVolScanDefsUseRealFields pins the field names the same way
// every other seeded batch is pinned (a typo'd field compiles fine and
// silently matches nothing forever), and pins the "gap % is chgDay, not
// preGap" decision documented on premarketVolScans — preGap is frozen at
// 09:30 and would sit Null for the entire pre-market session this scan
// exists to be watched during.
func TestPremarketVolScanDefsUseRealFields(t *testing.T) {
	valid := map[string]bool{
		core.FieldLast: true, core.FieldChgDay: true, core.FieldNews: true,
		core.FieldAvgVol: true, core.FieldPreVolPctAdv: true, core.FieldPreVol: true, core.FieldPreGap: true,
	}
	for _, d := range premarketVolScans() {
		if !d.Enabled {
			t.Fatalf("%s: must seed enabled", d.ID)
		}
		if d.Alert.Enabled {
			t.Fatalf("%s: a browsing list must not fire enter alerts", d.ID)
		}
		if d.Alert.Sound == "" {
			t.Fatalf("%s: keep a sound value so the editor renders sanely", d.ID)
		}
		var sawAvgVol, sawPctAdv bool
		for _, c := range d.Filter.Conds {
			if !valid[c.Field] {
				t.Fatalf("%s: filter references unexpected field %q", d.ID, c.Field)
			}
			switch c.Field {
			case core.FieldAvgVol:
				if c.Cmp != core.CmpGTE {
					t.Fatalf("%s: avgVol condition must be >=, got %q", d.ID, c.Cmp)
				}
				sawAvgVol = true
			case core.FieldPreVolPctAdv:
				if c.Cmp != core.CmpGTE {
					t.Fatalf("%s: preVolPctAdv condition must be >=, got %q", d.ID, c.Cmp)
				}
				sawPctAdv = true
			}
		}
		if !sawAvgVol || !sawPctAdv {
			t.Fatalf("%s: filter must include avgVol and preVolPctAdv (avgVol=%v pctAdv=%v)", d.ID, sawAvgVol, sawPctAdv)
		}
		for _, s := range d.Sort {
			if !valid[s.Field] {
				t.Fatalf("%s: sort references unexpected field %q", d.ID, s.Field)
			}
			if s.Field == core.FieldPreGap {
				t.Fatalf("%s: sort must not use preGap (Null until 09:30) as the live gap-strength signal", d.ID)
			}
		}
		for _, col := range d.Columns {
			if !valid[col] {
				t.Fatalf("%s: column references unexpected field %q", d.ID, col)
			}
		}
	}
}
