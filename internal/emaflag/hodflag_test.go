package emaflag

// HOD FLAG: Result.LastHodFlagTs is the FLAT bar of a flag whose run-up top is
// a new high of the ET day. Same synthetic bars as hodFlag in
// web/src/signals/emaFlag.test.ts; the golden vectors carry the real-bar parity.

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// hodRun: n quiet bars at 1.00 (bar 0 has high spike), the thrust (top 1.20),
// then flag bars 6 closes 1.180 .. 1.192 and `more` extra drifting bars.
func hodRun(n int, spike float64, more int) []core.Bar {
	var bars []core.Bar
	for i := 0; i < n; i++ {
		h := 1.002
		if i == 0 {
			h = spike
		}
		bars = append(bars, xbar(i, 1.0, h, 0.998, 1.0, 1000))
	}
	bars = append(bars, xbar(n, 1.0, 1.2, 0.999, 1.18, 9000))
	closes := []float64{1.18, 1.184, 1.188, 1.19, 1.192, 1.192}
	for k := 0; k < len(closes)+more; k++ {
		c := closes[min(k, len(closes)-1)]
		bars = append(bars, xbar(n+1+k, c-0.001, c+0.004, c-0.004, c, 1500))
	}
	return bars
}

func flatTs(r Result) int64 {
	for _, e := range r.Events {
		if e.Kind == KindFlat {
			return e.Ts
		}
	}
	return 0
}

func TestHodFlag_NewHighOfDayLegFiresOnTheFlatBar(t *testing.T) {
	r := evalClosed(hodRun(40, 1.002, 0), DefaultParams())
	if r.State != StateReady && r.State != StateFlat {
		t.Fatalf("state %s, want a flag", r.State)
	}
	if ft := flatTs(r); ft == 0 || r.LastHodFlagTs != ft {
		t.Fatalf("LastHodFlagTs %d, want the FLAT bar %d", r.LastHodFlagTs, ft)
	}
}

func TestHodFlag_LegUnderAnEarlierHighDoesNotFire(t *testing.T) {
	// bar 0 (>30 bars before the thrust, so the impulse still fires) printed 1.50
	r := evalClosed(hodRun(40, 1.5, 0), DefaultParams())
	if flatTs(r) == 0 {
		t.Fatalf("no flag formed: %v", stamps(r))
	}
	if r.LastHodFlagTs != 0 {
		t.Fatalf("LastHodFlagTs %d, want 0 (leg top 1.20 under the earlier 1.50)", r.LastHodFlagTs)
	}
}

func TestHodFlag_OncePerFlag(t *testing.T) {
	first := evalClosed(hodRun(40, 1.002, 0), DefaultParams()).LastHodFlagTs
	if first == 0 {
		t.Fatal("expected a HOD flag")
	}
	for _, more := range []int{1, 3, 8} {
		if got := evalClosed(hodRun(40, 1.002, more), DefaultParams()).LastHodFlagTs; got != first {
			t.Fatalf("more=%d: LastHodFlagTs moved %d -> %d", more, first, got)
		}
	}
}
