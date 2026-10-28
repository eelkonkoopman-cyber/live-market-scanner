package engine

import (
	"math"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// seedDay is a synthetic regular session 09:30..12:01 ET: two per-second
// aggregates per minute (at :00 and :30) with their own VWAPs, and the REST
// 1-minute bars the same prints make (vw = their volume-weighted price).
// Prices rally to ~90 by late morning and sag back to ~80 after 11:30, so a
// VWAP seen only from a late restart is far below the true one.
func seedDay() (aggs []core.Agg, bars []core.Bar) {
	for m := etMs(9, 30); m <= etMs(12, 1); m += m1Ms {
		i := float64((m - etMs(9, 30)) / m1Ms)
		p := 80 + 10*math.Sin(i/60*math.Pi/1.6) + 0.05*float64(int(i)%3)
		v1, v2 := int64(6000+int(i)%7*500), int64(4000+int(i)%5*300)
		a1 := core.Agg{Sym: "X", Open: p, High: p + 0.1, Low: p - 0.1, Close: p, VWAP: p - 0.02, Vol: v1, StartMs: m, EndMs: m + 1000}
		a2 := core.Agg{Sym: "X", Open: p, High: p + 0.2, Low: p - 0.05, Close: p + 0.1, VWAP: p + 0.07, Vol: v2, StartMs: m + 30_000, EndMs: m + 31_000}
		aggs = append(aggs, a1, a2)
		vw := (a1.VWAP*float64(v1) + a2.VWAP*float64(v2)) / float64(v1+v2)
		bars = append(bars, core.Bar{T: m, O: p, H: p + 0.2, L: p - 0.1, C: p + 0.1, V: v1 + v2, VW: vw})
	}
	return aggs, bars
}

// play applies the aggregates with StartMs in [from, to), letting the 1 Hz
// clock judge every minute that ends (plus the grace) before `to`.
func play(ts *tickerState, aggs []core.Agg, from, to int64) {
	for _, a := range aggs {
		if a.StartMs < from || a.StartMs >= to {
			continue
		}
		ts.applyAgg(a)
		if a.StartMs%m1Ms == 30_000 && a.StartMs+30_000+m1Grace < to {
			ts.tickDildo(a.StartMs+30_000+m1Grace, etOff(a.StartMs))
		}
	}
}

func vwapOf(ts *tickerState) float64 { return ts.scopeVwapNum / ts.scopeVwapDen }

func TestSeedScopeVwapAfterRestartMatchesWholeSession(t *testing.T) {
	aggs, bars := seedDay()
	now := etMs(12, 1) + 40_000 // 12:01:40, both 12:01 prints applied
	restart := etMs(11, 45) + 30_000

	for _, tc := range []struct {
		name    string
		restBar int64 // last REST bar's start: up to date, or lagging 3 minutes
	}{{"rest up to date", etMs(12, 1)}, {"rest lags", etMs(11, 57)}} {
		t.Run(tc.name, func(t *testing.T) {
			full, _ := newEmvTS()
			play(full, aggs, 0, now)
			re, sg := newEmvTS()
			sg.streamFrom(restart)
			play(re, aggs, restart, now)
			if d := math.Abs(vwapOf(re) - vwapOf(full)); d < 0.3 {
				t.Fatalf("restart VWAP %v already near the true %v: the fixture shows nothing", vwapOf(re), vwapOf(full))
			}
			drainSignals(sg)
			var rest []core.Bar
			for _, b := range bars {
				if b.T <= tc.restBar {
					rest = append(rest, b) // the in-progress 12:01 bar is included and must be ignored
				}
			}
			if !re.seedScopeVwap(rest, now, etOff(now), core.SessionRegular) {
				t.Fatal("not seeded")
			}
			if re.scopeVwapDen != full.scopeVwapDen || math.Abs(vwapOf(re)-vwapOf(full)) > 1e-9 {
				t.Fatalf("seeded VWAP %v (den %v) want %v (den %v)", vwapOf(re), re.scopeVwapDen, vwapOf(full), full.scopeVwapDen)
			}
			// the 1-minute EMA: rebuilt up to the cut, never refolds a seeded bar
			ev := &re.d1.ev
			if ev.prevOK || ev.side != 0 || ev.pendUp || ev.pendDn {
				t.Fatalf("cross state not restarted: %+v", *ev)
			}
			n := ev.emaN
			cutBar := tc.restBar - m1Ms // the last COMPLETED REST bar
			re.foldEmv(int32(cutBar/m1Ms), 99, 1, etOff(now), false)
			if ev.emaN != n {
				t.Fatalf("a seeded bar was folded again: emaN %d -> %d", n, ev.emaN)
			}
			// replay the minutes after the cut the way the live judge would have
			if tc.restBar != etMs(12, 1) {
				ev.seedEnd = 0
				for m := tc.restBar + m1Ms; m < etMs(12, 1); m += m1Ms {
					b := bars[(m-etMs(9, 30))/m1Ms]
					re.foldEmv(int32(m/m1Ms), b.C, b.V, etOff(m), false)
				}
			}
			if math.Abs(ev.ema-full.d1.ev.ema) > 1e-9 || ev.emaN != full.d1.ev.emaN {
				t.Fatalf("seeded EMA %v (n %d) want %v (n %d)", ev.ema, ev.emaN, full.d1.ev.ema, full.d1.ev.emaN)
			}
			// the first judged bar after a seed takes its side silently
			next := etMs(12, 2)
			for _, ts := range []*tickerState{full, re} {
				ts.applyAgg(core.Agg{Sym: "X", Open: 80, High: 80, Low: 80, Close: 80, VWAP: 80, Vol: 20000, StartMs: next, EndMs: next + 1000})
				ts.tickDildo(next+m1Ms+m1Grace, etOff(next))
			}
			if hs := drainSignals(sg); len(hs) != 0 {
				t.Fatalf("first judged bar after a seed fired %v", kindsOf(hs))
			}
			if !ev.prevOK || ev.lastMs != next+m1Ms {
				t.Fatalf("bar after the seed not judged: %+v", *ev)
			}
			if math.Abs(vwapOf(re)-vwapOf(full)) > 1e-9 {
				t.Fatalf("VWAP drifted after the seed: %v vs %v", vwapOf(re), vwapOf(full))
			}
		})
	}
}

func TestSeedScopeVwapIgnoresOtherScopes(t *testing.T) {
	aggs, bars := seedDay()
	now := etMs(12, 1) + 40_000
	ts, _ := newEmvTS()
	play(ts, aggs, etMs(11, 45), now)
	num, den := ts.scopeVwapNum, ts.scopeVwapDen
	// the engine is not in the regular session (a boundary between fetch and apply)
	if ts.seedScopeVwap(bars, now, etOff(now), core.SessionPost) {
		t.Fatal("seeded across a scope mismatch")
	}
	// pre-market bars never count toward the regular-hours VWAP
	pre := []core.Bar{{T: etMs(9, 0), C: 1, V: 1e9, VW: 1}}
	if ts.seedScopeVwap(pre, now, etOff(now), core.SessionRegular) {
		t.Fatal("seeded from pre-market bars only")
	}
	if ts.scopeVwapNum != num || ts.scopeVwapDen != den {
		t.Fatal("a refused seed changed the VWAP")
	}
	withPre := append([]core.Bar{{T: etMs(9, 0), C: 1, V: 1e9, VW: 1}}, bars...)
	if !ts.seedScopeVwap(withPre, now, etOff(now), core.SessionRegular) || vwapOf(ts) < 70 {
		t.Fatalf("pre-market bar leaked into the regular VWAP: %v", vwapOf(ts))
	}
}

// The live minute ring keeps the per-second hot path allocation-free.
func TestAccumScopeDoesNotAllocate(t *testing.T) {
	ts, _ := newEmvTS()
	a := core.Agg{Sym: "X", Open: 50, High: 50, Low: 50, Close: 50, VWAP: 50, Vol: 100, StartMs: etMs(10, 0)}
	if n := testing.AllocsPerRun(1000, func() {
		a.StartMs += 1000
		ts.accumScope(a)
	}); n != 0 {
		t.Fatalf("accumScope allocates %v", n)
	}
}
