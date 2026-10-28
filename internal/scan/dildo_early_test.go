package scan

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestDildoEarlyRidesWithTheDildoScansOnly(t *testing.T) {
	r := core.Row{DildoAge: 5, DildoEarly: 1}
	if got := rowJSON(&r, []string{"last", core.FieldDildoAge})[core.FieldDildoEarly]; got != 1 {
		t.Fatalf("dildo scan: dildoEarly %v, want 1", got)
	}
	if _, ok := rowJSON(&r, []string{"last"})[core.FieldDildoEarly]; ok {
		t.Fatal("a scan without dildoAge carries dildoEarly")
	}
	r.DildoEarly = 0
	if got := rowJSON(&r, []string{core.FieldDildoAge})[core.FieldDildoEarly]; got != 0 {
		t.Fatalf("not early: %v", got)
	}
	r.DildoEarly = core.Null
	if _, ok := rowJSON(&r, []string{core.FieldDildoAge})[core.FieldDildoEarly]; ok {
		t.Fatal("unknown must be absent")
	}
	if fieldNum(&core.Row{DildoEarly: 1}, core.FieldDildoEarly) != 1 {
		t.Fatal("fieldNum")
	}
}

func TestOverlayDildoEarly(t *testing.T) {
	const asOf = int64(1_000_000)
	// the same dildo on both sides: the chart's verdict wins (the trader's bars)
	r := core.Row{DildoAge: 100, DildoEarly: core.Null}
	overlayDildo(&r, core.EmaFlagRef{DildoMs: asOf - 100_000, DildoEarly: 1}, true, asOf)
	if r.DildoEarly != 1 {
		t.Fatalf("same dildo: %v", r.DildoEarly)
	}
	r = core.Row{DildoAge: 100, DildoEarly: 1}
	overlayDildo(&r, core.EmaFlagRef{DildoMs: asOf - 100_000, DildoEarly: 0}, true, asOf)
	if r.DildoEarly != 0 {
		t.Fatalf("same dildo, chart says not early: %v", r.DildoEarly)
	}
	// a newer chart dildo replaces the engine's, verdict included
	r = core.Row{DildoAge: 500, DildoEarly: 0}
	overlayDildo(&r, core.EmaFlagRef{DildoMs: asOf - 60_000, DildoEarly: 1}, true, asOf)
	if r.DildoAge != 60 || r.DildoEarly != 1 {
		t.Fatalf("newer chart dildo: age %v early %v", r.DildoAge, r.DildoEarly)
	}
	// an older chart dildo leaves the engine's alone
	r = core.Row{DildoAge: 30, DildoEarly: 1}
	overlayDildo(&r, core.EmaFlagRef{DildoMs: asOf - 600_000, DildoEarly: 0}, true, asOf)
	if r.DildoEarly != 1 {
		t.Fatalf("older chart dildo: %v", r.DildoEarly)
	}
}
