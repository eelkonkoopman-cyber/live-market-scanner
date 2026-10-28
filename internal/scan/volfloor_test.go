package scan

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// The VWAP cross volume floor (cmd/scanner emaVwapVolFloorConds): a cross on a
// thin bar / thin day / outside regular hours does not match.
func TestVwapVolFloorFilter(t *testing.T) {
	p, err := compile(core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldEvBarDvol, Cmp: core.CmpGTE, Value: 500000},
		{Field: core.FieldEvSessVol, Cmp: core.CmpGTE, Value: 500000},
		{Field: core.FieldSession, Cmp: core.CmpEq, Value: "regular"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	row := func(dv, sv float64, s core.Session) *core.Row {
		return &core.Row{EvBarDvol: dv, EvSessVol: sv, Sess: s}
	}
	if !p(row(600000, 900000, core.SessionRegular)) {
		t.Fatal("liquid regular-hours cross should match")
	}
	for name, r := range map[string]*core.Row{
		"thin bar":   row(40000, 900000, core.SessionRegular),
		"thin day":   row(600000, 22716, core.SessionRegular),
		"pre-market": row(600000, 900000, core.SessionPre),
		"no cross":   row(core.Null, core.Null, core.SessionRegular),
	} {
		if p(r) {
			t.Fatalf("%s matched", name)
		}
	}
}
