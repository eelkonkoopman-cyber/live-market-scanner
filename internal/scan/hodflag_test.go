package scan

import (
	"math"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// e9HodFlagAge: seconds since the FLAT bar of today's latest HOD flag, Null
// without one (a candidate with HodFlagMs 0, a non-candidate) - never a 0 that
// `e9HodFlagAge <= 300` would read as a fresh flag.
func TestScannerE9HodFlagAgeOverlay(t *testing.T) {
	has := core.EmaFlagRef{State: 2, DistPct: 1, Trigger: math.NaN(), Stop: math.NaN(), HodFlagMs: 1_000_000}
	none := has
	none.HodFlagMs = 0
	snap := snapOf(1_000_000+125_999, mkRow("HOD", 5, 1), mkRow("NONE", 5, 1), mkRow("OUT", 5, 1))
	e9Overlay(t, map[string]core.EmaFlagRef{"HOD": has, "NONE": none}, snap)
	row := func(sym string) core.Row { return snap.Rows[snap.Index[sym]] }

	if r := row("HOD"); r.E9HodFlagAge != 125 || fieldNum(&r, core.FieldE9HodFlagAge) != 125 {
		t.Errorf("HOD age want 125 got %v", r.E9HodFlagAge)
	}
	for _, sym := range []string{"NONE", "OUT"} {
		r := row(sym)
		if !core.IsNull(r.E9HodFlagAge) {
			t.Errorf("%s: e9HodFlagAge want Null, got %v", sym, r.E9HodFlagAge)
		}
	}
}
