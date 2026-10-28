package scan

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// bbsDistPct is filterable/sortable and Null -> excluded.
func TestPredicateBbsDistPct(t *testing.T) {
	r := sigRowOf("AAOI", 116.2)
	r.BbsState, r.BbsSupport, r.BbsDistPct = 1, 115.85, 0.3
	if v := fieldNum(&r, core.FieldBbsDistPct); v != 0.3 {
		t.Fatalf("fieldNum = %v", v)
	}
	p, err := compile(core.Filter{Field: core.FieldBbsDistPct, Cmp: core.CmpLTE, Value: 1})
	if err != nil || !p(&r) {
		t.Fatalf("must match (err %v)", err)
	}
	r.BbsDistPct = core.Null
	if p(&r) {
		t.Fatal("Null must not match")
	}
}
