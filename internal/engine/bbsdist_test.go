package engine

import (
	"math"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/bbs"
	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// While forming, bbsDistPct = (last - support) / support * 100; Null otherwise.
func TestBbsRowDistPct(t *testing.T) {
	bars := loadBbsSeries(t)["aaoi"]
	ts, sg := newBbsTS()
	var breakMs int64
	seen := false
	for _, b := range bars {
		feed1m(ts, b)
		for _, h := range drainSignals(sg) {
			breakMs = h.BarMs
		}
		r := sigRow(ts, b.T+m1Ms+m1Grace)
		if breakMs == 0 && r.BbsState == bbs.StateForming {
			seen = true
			want := (r.Last - r.BbsSupport) / r.BbsSupport * 100
			if math.Abs(r.BbsDistPct-want) > 1e-9 {
				t.Fatalf("dist %v want %v", r.BbsDistPct, want)
			}
		} else if !core.IsNull(r.BbsDistPct) {
			t.Fatalf("non-forming dist %v", r.BbsDistPct)
		}
	}
	if !seen {
		t.Fatal("no forming row")
	}
}
