package flaglog

import (
	"context"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// The 9EMA VWAP cross lands in the history as VWAP+ / VWAP- rows.
func TestOnSignalRecordsVwapCrosses(t *testing.T) {
	c := etMs(2026, 10, 14, 10, 6)
	r := New(newFakeRepo(), func() time.Time { return time.UnixMilli(c + 5*60_000) })
	r.OnSignal(core.SignalHit{Sym: "AAPL", Kind: core.KindEmaVwapBull, BarMs: c, Price: 101.5, Ema: 100.12, Vwap: 100.05, Dist: 0.07})
	r.OnSignal(core.SignalHit{Sym: "AAPL", Kind: core.KindEmaVwapBear, BarMs: c + 600_000, Price: 99, Ema: 99.9, Vwap: 100, Dist: -0.1})
	msg, _ := r.Day(context.Background(), "")
	if len(msg.Events) != 2 {
		t.Fatalf("events %+v", msg.Events)
	}
	for _, e := range msg.Events {
		switch e.Kind {
		case "vwapBull":
			if e.Label != "VWAP+" || e.Note != "1m 9 EMA 100.12 crossed above the VWAP 100.05 (0.07% gap)" {
				t.Fatalf("%+v", e)
			}
		case "vwapBear":
			if e.Label != "VWAP\u2212" || e.Note != "1m 9 EMA 99.90 crossed under the VWAP 100.00 (0.10% gap)" {
				t.Fatalf("%+v", e)
			}
		default:
			t.Fatalf("%+v", e)
		}
	}
}
