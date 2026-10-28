package dlevel

import (
	"fmt"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// bars builds consecutive daily bars from highs, dated 2026-09-01 onward (by index).
func bars(highs ...float64) []core.DailyBar {
	out := make([]core.DailyBar, len(highs))
	for i, h := range highs {
		out[i] = core.DailyBar{Date: fmt.Sprintf("2026-09-%02d", i+1), H: h, C: h - 1}
	}
	return out
}

func find(lv []Level, k Kind) (Level, bool) {
	for _, l := range lv {
		if l.Kind == k {
			return l, true
		}
	}
	return Level{}, false
}

func TestHigh14ExcludesTodayAndOlderBars(t *testing.T) {
	hs := make([]float64, 20)
	for i := range hs {
		hs[i] = 50 + float64(i%3)
	}
	hs[2] = 99 // older than 14 days: not the 14D high
	b := bars(hs...)
	b = append(b, core.DailyBar{Date: "2026-09-21", H: 120}) // today: ignored
	lv := Compute(b, "2026-09-21", true)
	l, ok := find(lv, Kind14D)
	if !ok || l.Price != 52 {
		t.Fatalf("14D high = %+v ok=%v, want 52 (levels %+v)", l, ok, lv)
	}
}

func TestPivotsAndPDH(t *testing.T) {
	// highs: 10 11 15 12 11 13 14 12 11 (today = day 10)
	lv := Compute(bars(10, 11, 15, 12, 11, 13, 14, 12, 11), "2026-09-10", true)
	// 15 is a pivot AND the 14D high (shows as 14D HIGH); 14 is a PIVOT; 11 is PDH.
	if l, ok := find(lv, Kind14D); !ok || l.Price != 15 {
		t.Fatalf("14D: %+v", lv)
	}
	if l, ok := find(lv, KindPivot); !ok || l.Price != 14 {
		t.Fatalf("pivot: %+v", lv)
	}
	if l, ok := find(lv, KindPDH); !ok || l.Price != 11 {
		t.Fatalf("pdh: %+v", lv)
	}
	if len(lv) != 3 {
		t.Fatalf("want exactly 3 levels (13 is not a pivot), got %+v", lv)
	}
	for i := 1; i < len(lv); i++ {
		if lv[i].Price <= lv[i-1].Price {
			t.Fatalf("not ascending: %+v", lv)
		}
	}
}

func TestPivotNeedsTwoBarsEachSideAndStrict(t *testing.T) {
	// the last two bars cannot be pivots; an equal neighbour high is not a pivot.
	lv := Compute(bars(10, 12, 12, 11, 10, 20, 19), "2026-09-08", false)
	if _, ok := find(lv, KindPivot); ok {
		t.Fatalf("no pivot expected, got %+v", lv)
	}
	if _, ok := find(lv, KindPDH); ok {
		t.Fatalf("withPDH=false must omit PDH: %+v", lv)
	}
}

func TestPDHDedupedIntoHigherKind(t *testing.T) {
	lv := Compute(bars(10, 11, 12, 13, 20), "2026-09-06", true)
	if len(lv) != 1 || lv[0].Kind != Kind14D || lv[0].Price != 20 {
		t.Fatalf("PDH == 14D high must collapse to 14D HIGH: %+v", lv)
	}
}

func TestCrossDetection(t *testing.T) {
	var tr Tracker
	tr.Set([]Level{{40, KindPDH}, {45, Kind14D}})
	tr.Step(38, 39, 39, 500)
	tr.Step(39, 41, 40.5, 1000)
	lv, ms, ok := tr.Best(40.5, 38.5, true)
	if !ok || lv.Price != 40 || ms != 1000 {
		t.Fatalf("best = %+v %d %v", lv, ms, ok)
	}
	// through 45 too: the HIGHEST level broken is reported.
	tr.Step(40.5, 46, 45.5, 2000)
	lv, ms, ok = tr.Best(45.5, 38.5, true)
	if !ok || lv.Price != 45 || lv.Kind != Kind14D || ms != 2000 {
		t.Fatalf("best = %+v %d %v", lv, ms, ok)
	}
	// back under 45 but above 40: the 45 break is gone, 40 remains.
	tr.Step(45.5, 45.6, 44, 3000)
	lv, _, ok = tr.Best(44, 38.5, true)
	if !ok || lv.Price != 40 {
		t.Fatalf("after pullback best = %+v %v", lv, ok)
	}
	// closes under 40: nothing left (even though the day's low was below it).
	tr.Step(44, 44, 39, 4000)
	if _, _, ok = tr.Best(39, 38.5, true); ok {
		t.Fatal("below every level: no breakout")
	}
	// a reclaim counts again, with the new time.
	tr.Step(39, 41, 41, 5000)
	if lv, ms, ok = tr.Best(41, 38.5, true); !ok || lv.Price != 40 || ms != 5000 {
		t.Fatalf("reclaim = %+v %d %v", lv, ms, ok)
	}
}

func TestGapThroughLevelAndAlreadyAbove(t *testing.T) {
	var tr Tracker
	tr.Set([]Level{{40, KindPDH}})
	// the first print of the day gaps from 38 (prev close) to 42: a cross.
	tr.Step(38, 42, 42, 700)
	if _, ms, ok := tr.Best(42, 41.9, true); !ok || ms != 700 {
		t.Fatalf("gap through level: ms=%d ok=%v", ms, ok)
	}
	// never below it today (low above the level), no tracked cross: not a breakout.
	var t2 Tracker
	t2.Set([]Level{{40, KindPDH}})
	t2.Step(43, 44, 44, 800)
	if _, _, ok := t2.Best(44, 43, true); ok {
		t.Fatal("always above the level: not a breakout")
	}
	// restart: the tracker missed the cross but today's low was under the level.
	if l, ms, ok := t2.Best(44, 39, true); !ok || l.Price != 40 || ms != 0 {
		t.Fatalf("lod fallback = %+v %d %v", l, ms, ok)
	}
}

func TestSetKeepsStateWhenSame(t *testing.T) {
	var tr Tracker
	tr.Set([]Level{{40, KindPDH}})
	tr.Step(39, 41, 41, 10)
	tr.Set([]Level{{40, KindPDH}})
	if _, ms, ok := tr.Best(41, 0, false); !ok || ms != 10 {
		t.Fatal("identical Set must keep crossing state")
	}
	tr.Reset()
	if _, _, ok := tr.Best(41, 0, false); ok {
		t.Fatal("Reset clears crosses")
	}
}
