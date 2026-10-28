package dildopb

import (
	"math"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/emaflag"
)

// TestEmaSeriesPinnedToEmaflag pins this package's private emaSeries to
// emaflag's (unexported, and emaflag.go must not be touched): emaflag.Evaluate
// reports the EMA at the last candle (Result.Ema), so feeding it every prefix
// of the same bars reads emaflag's series bar by bar. Bit-for-bit equality,
// NaN during the warm-up included, over every real fixture and a period-1 and
// a period-20 run.
func TestEmaSeriesPinnedToEmaflag(t *testing.T) {
	fx := loadFixtures(t)
	compared := 0
	for name, bars := range fx {
		if len(bars) > 260 {
			bars = bars[len(bars)-260:] // the arithmetic is the same at every index; keep the O(n^2) prefix loop fast
		}
		for _, period := range []int{9, 1, 20} {
			closes := make([]float64, len(bars))
			for i := range bars {
				closes[i] = bars[i].C
			}
			mine := emaSeries(closes, period)
			p := emaflag.DefaultParams()
			p.EmaPeriod = period
			for k := 1; k <= len(bars); k++ {
				theirs := emaflag.Evaluate(bars[:k], 60_000, bars[k-1].T+600_000, p).Ema
				got := mine[k-1]
				if math.IsNaN(theirs) != math.IsNaN(got) || (!math.IsNaN(got) && math.Float64bits(got) != math.Float64bits(theirs)) {
					t.Fatalf("%s period %d bar %d: emaSeries %v, emaflag %v", name, period, k-1, got, theirs)
				}
				compared++
			}
		}
	}
	if compared == 0 {
		t.Fatal("nothing compared")
	}
	t.Logf("compared %d EMA values", compared)
}

// The pin must not be vacuous: a different period gives different numbers.
func TestEmaSeriesPinIsNotVacuous(t *testing.T) {
	bars := []core.Bar{}
	for i := 0; i < 30; i++ {
		c := 1 + 0.01*float64(i%7)
		bars = append(bars, core.Bar{T: int64(i) * 60_000, O: c, H: c, L: c, C: c, V: 1})
	}
	closes := make([]float64, len(bars))
	for i := range bars {
		closes[i] = bars[i].C
	}
	a, b := emaSeries(closes, 9), emaSeries(closes, 10)
	if a[29] == b[29] {
		t.Fatal("period 9 and 10 agree: the pin would prove nothing")
	}
}
