package flaglog

import (
	"context"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/bbs"
	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// The BOUNCY BALL SHORT alert lands in the history as kind "bbs" with its note.
func TestOnSignalRecordsBouncyBallShort(t *testing.T) {
	c := etMs(2026, 10, 14, 10, 40)
	r := New(newFakeRepo(), func() time.Time { return time.UnixMilli(c + 5*60_000) })
	r.OnSignal(core.SignalHit{Sym: "AAOI", Kind: bbs.KindBbs, BarMs: c, Price: 115.2, C: 115.2, DollarVol: 2_304_000,
		Support: 115.85, Touches: 4, Highs: 4, FirstHigh: 119.9, LastHigh: 116.8})
	msg, _ := r.Day(context.Background(), "")
	if len(msg.Events) != 1 {
		t.Fatalf("events %+v", msg.Events)
	}
	e := msg.Events[0]
	want := "support 115.85 (4 touches), lower highs 119.90 → 116.80 over 4 bounces, $2.3M"
	if e.Kind != "bbs" || e.Label != "BOUNCY BALL SHORT" || e.Note != want || e.Price == nil || *e.Price != 115.2 {
		t.Fatalf("got %+v note %q", e, e.Note)
	}
}
