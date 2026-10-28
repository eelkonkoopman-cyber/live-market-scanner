package flagscan

import (
	"math"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/emaflag"
)

// wavyBars is two full extended sessions (04:00-20:00 ET, 960 bars each) of a
// wandering price: the worst-case series length a candidate's history reaches
// (the REST window is the previous trading day plus today).
func wavyBars(end time.Time, n int) []core.Bar {
	out := make([]core.Bar, n)
	last := end.Truncate(time.Minute).UnixMilli() - minuteMs
	for i := 0; i < n; i++ {
		p := 10 + 2*math.Sin(float64(i)/37) + 0.5*math.Sin(float64(i)/5)
		out[i] = core.Bar{T: last - int64(n-1-i)*minuteMs, O: p, H: p + 0.05, L: p - 0.05, C: p + 0.01, V: 100}
	}
	return out
}

// BenchmarkEvaluateAllCandidates is the loop's worst second: all 80 candidates
// have a changed bar and each is re-evaluated over ~1900 bars. It documents
// that evaluating every candidate every second is a small fraction of a core
// (see the ns/op: one op is one full pass over the cap).
func BenchmarkEvaluateAllCandidates(b *testing.B) {
	bars := wavyBars(time.Date(2026, 10, 5, 12, 0, 0, 0, etLocation()), 1900)
	p := emaflag.DefaultParams()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for s := 0; s < MaxCandidates; s++ {
			emaflag.Evaluate(bars, minuteMs, bars[len(bars)-1].T+minuteMs, p)
		}
	}
}
