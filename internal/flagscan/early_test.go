package flagscan

import (
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
)

func TestChartEarlyIsTheSharedJudge(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	t0 := time.Date(2026, 10, 13, 4, 0, 0, 0, ny).UnixMilli()
	var bars []core.Bar
	for i := 0; i < 30; i++ {
		bars = append(bars, core.Bar{T: t0 + int64(i)*60_000, O: 1, H: 1.02, L: 0.99, C: 1})
	}
	ts := t0 + 30*60_000
	bars = append(bars, core.Bar{T: ts, O: 1, H: 1.3, L: 1, C: 1.25})
	evs := []dildopb.Event{{Ts: ts, Kind: dildopb.KindDildo}}
	if !chartEarly(bars, evs, ts) {
		t.Fatal("quiet base breakout not early")
	}
	evs2 := append([]dildopb.Event{{Ts: ts - 20*60_000, Kind: dildopb.KindDildo}}, evs...)
	if chartEarly(bars, evs2, ts) {
		t.Fatal("the second dildo today was early")
	}
	if got := dildoEarlyOf(bars, evs2); got[ts] || len(got) != 2 {
		t.Fatalf("dildoEarlyOf %v", got)
	}
	if chartEarly(bars[25:], evs, ts) {
		t.Fatal("too few chart bars is not early (the trader's rule)")
	}
}
