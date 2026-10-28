package flaglog

import (
	"context"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/ignition"
)

// The IGNITION alert lands in the history as kind "ignition" with its note.
func TestOnSignalRecordsIgnition(t *testing.T) {
	c := etMs(2026, 10, 14, 9, 6)
	r := New(newFakeRepo(), func() time.Time { return time.UnixMilli(c + 5*60_000) })
	r.OnSignal(core.SignalHit{Sym: "NCPL", Kind: ignition.KindIgnition, BarMs: c, Price: 1.88, O: 1.79, C: 1.88,
		BodyPct: 5.03, VolX: 15, DollarVol: 564_000, BaseRangePct: 2.86, BaseHigh: 1.8})
	msg, _ := r.Day(context.Background(), "")
	if len(msg.Events) != 1 {
		t.Fatalf("events %+v", msg.Events)
	}
	e := msg.Events[0]
	want := "+5.0% candle on 15.0x volume out of a 2.9% base, $564k; base high 1.80"
	if e.Kind != "ignition" || e.Label != "IGNITION" || e.Note != want || e.Price == nil || *e.Price != 1.88 {
		t.Fatalf("got %+v note %q", e, e.Note)
	}
}

// A FRESH ignition's note leads with the fresh marker; unknown / not fresh do not.
func TestOnSignalMarksFreshIgnition(t *testing.T) {
	c := etMs(2026, 10, 14, 9, 6)
	for _, f := range []int8{1, 0, -1} {
		r := New(newFakeRepo(), func() time.Time { return time.UnixMilli(c + 5*60_000) })
		r.OnSignal(core.SignalHit{Sym: "NCPL", Kind: ignition.KindIgnition, BarMs: c, Price: 1.88, O: 1.79, C: 1.88,
			BodyPct: 5.03, VolX: 15, DollarVol: 564_000, BaseRangePct: 2.86, BaseHigh: 1.8, Fresh: f})
		msg, _ := r.Day(context.Background(), "")
		want := "+5.0% candle on 15.0x volume out of a 2.9% base, $564k; base high 1.80"
		if f == 1 {
			want = FreshNotePrefix + want
		}
		if len(msg.Events) != 1 || msg.Events[0].Note != want {
			t.Fatalf("fresh %d: %+v", f, msg.Events)
		}
	}
}

// A SKIPPED ignition is still recorded, its note led by the skip reason (no fresh marker).
func TestOnSignalMarksSkippedIgnition(t *testing.T) {
	c := etMs(2026, 10, 14, 9, 6)
	r := New(newFakeRepo(), func() time.Time { return time.UnixMilli(c + 5*60_000) })
	r.OnSignal(core.SignalHit{Sym: "NCPL", Kind: ignition.KindIgnition, BarMs: c, Price: 1.88, O: 1.79, C: 1.88,
		BodyPct: 5.03, VolX: 15, DollarVol: 564_000, BaseRangePct: 2.86, BaseHigh: 1.8, Fresh: 0, Skip: "skip: up 7.2% on the day"})
	msg, _ := r.Day(context.Background(), "")
	want := "skip: up 7.2% on the day; +5.0% candle on 15.0x volume out of a 2.9% base, $564k; base high 1.80"
	if len(msg.Events) != 1 || msg.Events[0].Kind != "ignition" || msg.Events[0].Note != want {
		t.Fatalf("%+v", msg.Events)
	}
}
