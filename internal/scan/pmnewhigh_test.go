package scan

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// The seeded "PM New High" filter (cmd/scanner pmNewHighScans; kept equal by
// TestPmNewHighScanShape there): pre-market, rvol >= 2, last at the day high.
func TestPredicatePmNewHigh(t *testing.T) {
	pred, err := compile(core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldSession, Cmp: core.CmpEq, Value: "pre"},
		{Field: core.FieldRvol, Cmp: core.CmpGTE, Value: 2},
		{Field: core.FieldVsHod, Cmp: core.CmpGTE, Value: 0},
	}})
	if err != nil {
		t.Fatal(err)
	}
	r := mkRow("A", 5, 0)
	r.Sess, r.Rvol, r.VsHod = core.SessionPre, 2.5, 0
	if !pred(&r) {
		t.Fatal("pre, rvol 2.5, at the high must match")
	}
	r.VsHod = -0.4
	if pred(&r) {
		t.Fatal("below the high must not match")
	}
	r.VsHod = core.Null
	if pred(&r) {
		t.Fatal("unknown high must not match")
	}
	r.VsHod, r.Rvol = 0, 1.9
	if pred(&r) {
		t.Fatal("rvol < 2 must not match")
	}
	r.Rvol, r.Sess = 3, core.SessionRegular
	if pred(&r) {
		t.Fatal("regular session must not match")
	}
}
