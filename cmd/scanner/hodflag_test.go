package main

import (
	"context"
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestSeedHodFlagOnceUnderOwnKey(t *testing.T) {
	h := newSeedHarness()
	seedScans(context.Background(), hodFlagSeedKey, hodFlagScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if strings.Join(h.saved, ",") != hodFlagID || hodFlagSeedKey != "seeded.hodflag.v1" || h.meta[hodFlagSeedKey] != "1" {
		t.Fatalf("saved=%v meta=%v", h.saved, h.meta)
	}
	h.saved, h.installed = nil, nil
	seedScans(context.Background(), hodFlagSeedKey, hodFlagScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 || len(h.installed) != 0 {
		t.Fatalf("re-seeded: %v %v", h.saved, h.installed)
	}
}

func TestHodFlagScanShape(t *testing.T) {
	d := hodFlagScans()[0]
	if d.ID != "hod-flag" || d.Name != "HOD Flag" || !d.Enabled {
		t.Fatalf("identity: %+v", d)
	}
	if len(d.Filter.Conds) != 2 || d.Filter.Conds[1].Field != core.FieldE9HodFlagAge || d.Filter.Conds[1].Cmp != core.CmpLTE {
		t.Fatalf("filter: %+v", d.Filter)
	}
	if !d.Alert.Enabled || !d.Alert.Speak || d.Alert.On != core.AlertOnEnter {
		t.Fatalf("alert must be on, spoken, on enter: %+v", d.Alert)
	}
}
