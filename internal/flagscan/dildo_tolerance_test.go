package flagscan

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
)

// A BKYI-shaped bar (body 3.9x the prior mean, under the 4x minimum) is
// confirmed on the chart's bars only when the engine already saw a dildo on it.
func TestDildoConfirmTolerance(t *testing.T) {
	mk := func(bodyX float64) ([]core.Bar, int64) {
		t0 := int64(1_760_000_000_000) / minuteMs * minuteMs
		var bars []core.Bar
		for i := 0; i < 10; i++ {
			bars = append(bars, core.Bar{T: t0 + int64(i)*minuteMs, O: 1, H: 1.1, L: 1, C: 1.1, V: 1000})
		}
		c := 1 + 0.1*bodyX
		ts := t0 + 10*minuteMs
		return append(bars, core.Bar{T: ts, O: 1, H: c + 0.01, L: 1, C: c, V: 100_000}), ts
	}
	for _, c := range []struct {
		name  string
		bodyX float64
		seen  bool
		want  bool
	}{
		{"BKYI 3.9x, engine saw it", 3.9, true, true},
		{"BKYI 3.9x, engine did not", 3.9, false, false},
		{"3.7x, engine saw it", 3.7, true, false},
	} {
		s := &service{dparams: dildopb.DefaultParams(), evalDildo: dildopb.EvaluateTolerant, engineDildo: map[string][]int64{}}
		bars, ts := mk(c.bodyX)
		if c.seen {
			s.noteEngineDildo("BKYI", ts)
		}
		st := &symState{sym: "BKYI", bars: bars}
		res := s.evalDildoOne(st, ts+5*minuteMs)
		if got := res.LastDildoTs == ts; got != c.want {
			t.Errorf("%s: confirmed %v, want %v", c.name, got, c.want)
		}
		if c.want && withDildo(core.EmaFlagRef{}, res, false).DildoMs != ts {
			t.Errorf("%s: the ref does not carry the dildo", c.name)
		}
	}
}
