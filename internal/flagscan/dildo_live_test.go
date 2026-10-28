package flagscan

import (
	"sync/atomic"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
)

// Config.DildoLive (the Signal window's tuning panel) is read on every
// evaluation: swapping it changes the next verdict, and a race with readers is
// clean under -race.
func TestDildoLiveParamsReachTheEvaluation(t *testing.T) {
	t0 := int64(1_760_000_000_000) / minuteMs * minuteMs
	var bars []core.Bar
	for i := 0; i < 10; i++ {
		bars = append(bars, core.Bar{T: t0 + int64(i)*minuteMs, O: 1, H: 1.1, L: 1, C: 1.1, V: 1000})
	}
	ts := t0 + 10*minuteMs
	bars = append(bars, core.Bar{T: ts, O: 1, H: 1.51, L: 1, C: 1.5, V: 100_000}) // +50 % body, 5x
	var cur atomic.Pointer[dildopb.Params]
	d := dildopb.DefaultParams()
	cur.Store(&d)
	s := &service{dparams: dildopb.DefaultParams(), evalDildo: dildopb.EvaluateTolerant, engineDildo: map[string][]int64{}}
	s.cfg.DildoLive = func() dildopb.Params { return *cur.Load() }
	st := &symState{sym: "X", bars: bars}
	if res := s.evalDildoOne(st, ts+5*minuteMs); res.LastDildoTs != ts {
		t.Fatalf("defaults: dildo not found (%d)", res.LastDildoTs)
	}
	strict := dildopb.DefaultParams()
	strict.MinBodyPct = 80
	cur.Store(&strict)
	if res := s.evalDildoOne(st, ts+5*minuteMs); res.LastDildoTs == ts {
		t.Fatal("live minBodyPct 80 must reject the +50 % body")
	}
	// race: a writer swapping while the service evaluates
	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			p := dildopb.DefaultParams()
			p.MinBodyPct = float64(i % 90)
			cur.Store(&p)
		}
		close(done)
	}()
	for i := 0; i < 200; i++ {
		s.evalDildoOne(st, ts+5*minuteMs)
	}
	<-done
}
