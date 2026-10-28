package main

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// The two seeded Vol Break scans: ids, the one-time seed key, alerts on with speech.
func TestVolBreakScans(t *testing.T) {
	if volBreakSeedKey != "seeded.volbreak.v1" {
		t.Fatal(volBreakSeedKey)
	}
	d := volBreakScans()
	if len(d) != 2 || d[0].ID != "vol-break" || d[0].Name != "Vol Break" || d[1].ID != "vol-break-9ema" || d[1].Name != "Vol Break 9EMA" {
		t.Fatalf("%+v", d)
	}
	for _, s := range d {
		if !s.Enabled || !s.Alert.Enabled || !s.Alert.Speak || s.Alert.On != core.AlertOnEnter {
			t.Fatalf("%s alert %+v", s.ID, s.Alert)
		}
	}
	if d[0].Alert.Sound != "volbreak" || d[0].Filter.Conds[0].Field != core.FieldVbAge {
		t.Fatalf("%+v", d[0])
	}
	if c := d[1].Filter.Conds; c[0].Field != core.FieldVbState || c[0].Value != 2 || c[1].Field != core.FieldVbEntryAge {
		t.Fatalf("%+v", c)
	}
}
