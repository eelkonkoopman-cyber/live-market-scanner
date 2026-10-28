package dildopb

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// tolBars: ten quiet prior bars (body 0.10, volume 1000) then one big green
// candle whose body is bodyX x the prior mean (BKYI 2026-10-13 04:24 ET: 3.9x).
func tolBars(bodyX float64) ([]core.Bar, int64) {
	t0 := int64(1_760_000_000_000) / 60_000 * 60_000
	var bars []core.Bar
	for i := 0; i < 10; i++ {
		bars = append(bars, core.Bar{T: t0 + int64(i)*60_000, O: 1.0, H: 1.1, L: 1.0, C: 1.1, V: 1000})
	}
	c := 1.0 + 0.1*bodyX
	ts := t0 + 10*60_000
	bars = append(bars, core.Bar{T: ts, O: 1.0, H: c + 0.01, L: 1.0, C: c, V: 100_000})
	return bars, ts
}

func hasDildo(r Result, ts int64) bool {
	for _, e := range r.Events {
		if e.Ts == ts && e.Kind == KindDildo {
			return true
		}
	}
	return false
}

func TestEvaluateTolerant(t *testing.T) {
	p := DefaultParams()
	for _, c := range []struct {
		name  string
		bodyX float64
		seen  bool
		want  bool
	}{
		{"3.9x engine saw it", 3.9, true, true},
		{"3.9x engine did not", 3.9, false, false},
		{"3.7x engine saw it", 3.7, true, false},
		{"4.1x engine did not", 4.1, false, true},
	} {
		bars, ts := tolBars(c.bodyX)
		now := ts + 10*60_000
		var seen []int64
		if c.seen {
			seen = []int64{ts}
		}
		r := EvaluateTolerant(bars, 60_000, now, p, seen, 5)
		if got := hasDildo(r, ts); got != c.want {
			t.Errorf("%s: dildo %v, want %v", c.name, got, c.want)
		}
		if c.want && r.LastDildoTs != ts {
			t.Errorf("%s: LastDildoTs %d, want %d", c.name, r.LastDildoTs, ts)
		}
		if !c.seen && hasDildo(Evaluate(bars, 60_000, now, p), ts) != c.want {
			t.Errorf("%s: Evaluate disagrees", c.name)
		}
	}
}

func TestTolerantScales(t *testing.T) {
	q := Tolerant(DefaultParams(), 5)
	if q.MinBodyX != 4*0.95 || q.MinBodyPct != 8*0.95 || q.MinVolX != 5*0.95 || q.MinDollarVol != 100_000*0.95 || q.MinPrice != 0.20*0.95 || q.MaxTopPct != 20*1.05 {
		t.Fatalf("Tolerant: %+v", q)
	}
	if q.PriorBars != 10 || q.MinPriorBars != 3 || q.PbWindowMin != 20 {
		t.Fatalf("Tolerant changed non-threshold params: %+v", q)
	}
}
