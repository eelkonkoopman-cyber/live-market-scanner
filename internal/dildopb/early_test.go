package dildopb

import (
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func flatBase(n int, lo, hi float64) (hs, ls []float64) {
	for i := 0; i < n; i++ {
		hs, ls = append(hs, hi), append(ls, lo)
	}
	return
}

func TestJudgeEarly(t *testing.T) {
	p := DefaultEarlyParams()
	if p.BaseBars != 30 || p.MaxRangePct != 8 || !p.FirstOnly || !p.NewHighOfDay {
		t.Fatalf("defaults %+v", p)
	}
	hs, ls := flatBase(30, 1.00, 1.05) // 5% base
	if v := JudgeEarly(hs, ls, 1.20, 1.20, 0, false, p); !v.Early() || v.Bars != 30 || v.BaseHigh != 1.05 {
		t.Fatalf("quiet base breakout: %+v", v)
	}
	if v := JudgeEarly(hs[:9], ls[:9], 1.20, 1.20, 0, false, p); v.Reason != EarlyFewBars || v.Bars != 9 {
		t.Fatalf("9 bars: %+v", v)
	}
	if v := JudgeEarly(hs, ls, 1.04, 1.04, 0, false, p); v.Reason != EarlyNoBreakout {
		t.Fatalf("no breakout: %+v", v)
	}
	if v := JudgeEarly(hs, ls, 1.20, 1.20, 0, true, p); v.Reason != EarlyNotFirst {
		t.Fatalf("second dildo: %+v", v)
	}
	p2 := p
	p2.FirstOnly = false
	if v := JudgeEarly(hs, ls, 1.20, 1.20, 0, true, p2); !v.Early() {
		t.Fatalf("FirstOnly off: %+v", v)
	}
	wh, wl := flatBase(30, 1.00, 1.05)
	wh[0] = 1.20 // a wide bar 30 bars back: inside the window
	if v := JudgeEarly(wh, wl, 1.30, 1.30, 0, false, p); v.Reason != EarlyWideBase {
		t.Fatalf("wide base: %+v", v)
	}
	// ... and outside it once 31 bars are given: only the last 30 count.
	wh, wl = append([]float64{1.50}, hs...), append([]float64{1.00}, ls...)
	if v := JudgeEarly(wh, wl, 1.20, 1.20, 0, false, p); !v.Early() || v.Bars != 30 {
		t.Fatalf("trim to BaseBars: %+v", v)
	}
	zh, zl := flatBase(12, 0, 1)
	if v := JudgeEarly(zh, zl, 2, 2, 0, false, p); v.Reason != EarlyNoLow {
		t.Fatalf("no low: %+v", v)
	}
}

func TestEarlyFromBarsAndPriorDildoToday(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	t0 := time.Date(2026, 10, 13, 4, 0, 0, 0, ny).UnixMilli()
	var bars []core.Bar
	for i := 0; i < 40; i++ {
		bars = append(bars, core.Bar{T: t0 + int64(i)*60_000, O: 1, H: 1.02, L: 0.99, C: 1})
	}
	ts := t0 + 40*60_000
	bars = append(bars, core.Bar{T: ts, O: 1, H: 1.3, L: 1, C: 1.25}, core.Bar{T: ts + 60_000, H: 9, L: 0.1, C: 1})
	if v := EarlyFromBars(bars, ts, 0, false, DefaultEarlyParams()); !v.Early() || v.Bars != 30 {
		t.Fatalf("from bars: %+v", v)
	}
	evs := []Event{{Ts: ts - 86_400_000, Kind: KindDildo}}
	if PriorDildoToday(evs, ts) {
		t.Fatal("yesterday's dildo counted")
	}
	evs = append(evs, Event{Ts: ts - 600_000, Kind: KindPb}, Event{Ts: ts, Kind: KindDildo})
	if PriorDildoToday(evs, ts) {
		t.Fatal("a pullback or the dildo itself counted")
	}
	evs = append(evs, Event{Ts: ts - 600_000, Kind: KindDildo})
	if !PriorDildoToday(evs, ts) {
		t.Fatal("an earlier dildo today missed")
	}
}

func TestJudgeEarlyNewHighOfDay(t *testing.T) {
	p := DefaultEarlyParams()
	hs, ls := flatBase(30, 1.00, 1.05)
	if v := JudgeEarly(hs, ls, 1.20, 1.25, 1.10, false, p); !v.Early() || v.DayHigh != 1.10 {
		t.Fatalf("new HOD: %+v", v)
	}
	if v := JudgeEarly(hs, ls, 1.20, 1.25, 1.40, false, p); v.Reason != EarlyNotNewHigh || v.DayHigh != 1.40 {
		t.Fatalf("below the day high: %+v", v)
	}
	if v := JudgeEarly(hs, ls, 1.20, 1.40, 1.40, false, p); v.Reason != EarlyNotNewHigh {
		t.Fatalf("equal to the day high: %+v", v)
	}
	if v := JudgeEarly(hs, ls, 1.20, 1.25, 0, false, p); !v.Early() {
		t.Fatalf("first bar of the day: %+v", v)
	}
	p.NewHighOfDay = false
	if v := JudgeEarly(hs, ls, 1.20, 1.25, 1.40, false, p); !v.Early() {
		t.Fatalf("setting off: %+v", v)
	}
}

func TestEarlyFromBarsDayHigh(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	day := time.Date(2026, 10, 13, 4, 0, 0, 0, ny).UnixMilli()
	var bars []core.Bar
	// yesterday evening: a far higher bar that must NOT count
	bars = append(bars, core.Bar{T: day - 3_600_000, O: 1, H: 5, L: 1, C: 1})
	for i := 0; i < 40; i++ {
		bars = append(bars, core.Bar{T: day + int64(i)*60_000, O: 1, H: 1.02, L: 0.99, C: 1})
	}
	ts := day + 40*60_000
	bars = append(bars, core.Bar{T: ts, O: 1, H: 1.3, L: 1, C: 1.25})
	if hi := DayHighBefore(bars, ts); hi != 1.02 {
		t.Fatalf("DayHighBefore %v", hi)
	}
	if v := EarlyFromBars(bars, ts, 0, false, DefaultEarlyParams()); !v.Early() {
		t.Fatalf("prev-day high counted: %+v", v)
	}
	// a pre-market high above the dildo's high, outside the 30-bar base
	bars[1].H = 1.5
	if v := EarlyFromBars(bars, ts, 0, false, DefaultEarlyParams()); v.Reason != EarlyNotNewHigh || v.DayHigh != 1.5 {
		t.Fatalf("earlier day high: %+v", v)
	}
	if hi := DayHighBefore(bars, day); hi != 0 {
		t.Fatalf("first bar of the day: %v", hi)
	}
}
