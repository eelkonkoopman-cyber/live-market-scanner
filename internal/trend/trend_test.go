package trend

import (
	"math"
	"testing"
)

// ramp builds n closes moving linearly from a to b.
func ramp(a, b float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = a + (b-a)*float64(i)/float64(n-1)
	}
	return out
}

func flat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestClassifyUptrend(t *testing.T) {
	// Steady climb 10 -> 15 over 30 sessions: above a rising SMA.
	r, ok := Classify(ramp(10, 15, 30), "2026-07-28")
	if !ok || r.DTrend != 1 {
		t.Fatalf("steady climb should classify up: ok=%v ref=%+v", ok, r)
	}
	if r.Chg20d <= 0 || r.VsSMA <= 0 {
		t.Fatalf("uptrend stats should be positive: %+v", r)
	}
	if r.AsOf != "2026-07-28" || r.Days != 30 {
		t.Fatalf("metadata: %+v", r)
	}
}

func TestClassifyDowntrend(t *testing.T) {
	r, ok := Classify(ramp(15, 10, 30), "2026-07-28")
	if !ok || r.DTrend != -1 {
		t.Fatalf("steady slide should classify down: ok=%v ref=%+v", ok, r)
	}
	if r.Chg20d >= 0 || r.VsSMA >= 0 {
		t.Fatalf("downtrend stats should be negative: %+v", r)
	}
}

func TestClassifyFlat(t *testing.T) {
	// Dead flat: SMA drift 0, inside the ±1% band.
	if r, ok := Classify(flat(10, 30), "d"); !ok || r.DTrend != 0 {
		t.Fatalf("flat series should classify flat: ok=%v ref=%+v", ok, r)
	}
	// Slow drift below the band (~+0.5% over the whole month) stays flat —
	// the band is what makes flat a real bucket instead of a knife edge.
	if r, ok := Classify(ramp(10, 10.05, 30), "d"); !ok || r.DTrend != 0 {
		t.Fatalf("sub-band drift should classify flat: ok=%v ref=%+v", ok, r)
	}
}

// A pop ABOVE a still-falling SMA is not yet an uptrend — that's exactly the
// "dead-cat bounce in a downtrend" case the user wants flagged, so it must
// NOT be classified up. With the close above the SMA it isn't "down" by the
// definition either: it lands in flat (transition state).
func TestClassifyBounceInDowntrendIsNotUp(t *testing.T) {
	closes := ramp(20, 10, 29)
	closes = append(closes, 13) // one-day +30% bounce over a falling SMA
	r, ok := Classify(closes, "d")
	if !ok {
		t.Fatal("should classify")
	}
	if r.DTrend == 1 {
		t.Fatalf("a one-day bounce over a falling SMA must not read as uptrend: %+v", r)
	}
}

func TestClassifyShortHistoryFallsBackThenRefuses(t *testing.T) {
	// 15-24 closes: the 10-day window applies (recent IPO).
	if r, ok := Classify(ramp(10, 14, 16), "d"); !ok || r.DTrend != 1 {
		t.Fatalf("16-close climb should classify up via the short window: ok=%v ref=%+v", ok, r)
	}
	// Fewer than 15: refuse — Null beats a guess.
	if _, ok := Classify(ramp(10, 14, 14), "d"); ok {
		t.Fatal("14 closes must be unknown")
	}
	if _, ok := Classify(nil, "d"); ok {
		t.Fatal("nil closes must be unknown")
	}
}

func TestClassifyGarbageDefensive(t *testing.T) {
	if _, ok := Classify(flat(0, 30), "d"); ok {
		t.Fatal("all-zero closes must be rejected, not divided by")
	}
	closes := flat(10, 30)
	closes[29] = math.SmallestNonzeroFloat64 // absurd but nonzero
	if r, ok := Classify(closes, "d"); ok && math.IsInf(r.VsSMA, 0) {
		t.Fatalf("no infinities allowed out: %+v", r)
	}
}
