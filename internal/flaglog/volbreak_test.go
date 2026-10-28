package flaglog

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/volbreak"
)

func TestOnSignalVolBreak(t *testing.T) {
	c := etMs(2026, 10, 28, 10, 40)
	r := New(newFakeRepo(), func() time.Time { return time.UnixMilli(c + 5*60_000) })
	r.OnSignal(core.SignalHit{Sym: "ABCD", Kind: volbreak.KindBreak, BarMs: c, Price: 3.5, C: 3.5, Level: 3.43, VolX: 4.2, Ema: 3.31, DollarVol: 560_000})
	r.OnSignal(core.SignalHit{Sym: "ABCD", Kind: volbreak.KindEntry, BarMs: c + 120_000, Price: 3.452, C: 3.48, Level: 3.43, Ema: 3.452, ClimaxMs: c})
	r.OnSignal(core.SignalHit{Sym: "EFGH", Kind: volbreak.KindBreak, BarMs: c, Price: 2, Level: 1.9, VolX: 3, Ema: math.NaN(), DollarVol: 1e6})
	msg, _ := r.Day(context.Background(), "")
	want := map[string]string{
		"ABCD " + volbreak.LabelBreak: "volume break 3.43 (4.2x vol), 9EMA 3.31, $560k",
		"ABCD " + volbreak.LabelEntry: "vol break 9EMA entry 3.45, level 3.43; broke at 10:40",
		"EFGH " + volbreak.LabelBreak: "volume break 1.90 (3.0x vol), 9EMA ?, $1.0M",
	}
	if len(msg.Events) != 3 {
		t.Fatalf("events %+v", msg.Events)
	}
	for _, e := range msg.Events {
		if w, ok := want[e.Sym+" "+e.Label]; !ok || e.Note != w {
			t.Fatalf("%s %s: note %q, want %q", e.Sym, e.Label, e.Note, w)
		}
	}
}
