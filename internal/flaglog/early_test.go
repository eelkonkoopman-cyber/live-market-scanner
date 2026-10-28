package flaglog

import (
	"context"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
)

func TestEarlyDildoRowsLeadTheirNoteWithEarly(t *testing.T) {
	c := etMs(2026, 10, 6, 11, 10)
	r := New(newFakeRepo(), func() time.Time { return time.UnixMilli(c + 10*60_000) })
	r.OnSignal(core.SignalHit{Sym: "EARL", Kind: dildopb.KindDildo, BarMs: c, Price: 1.09, O: 1, C: 1.09,
		BodyPct: 9, BodyX: 4.5, VolX: 200, DollarVol: 218_000, Early: 1})
	r.OnSignal(core.SignalHit{Sym: "LATE", Kind: dildopb.KindDildo, BarMs: c, Price: 1.09, O: 1, C: 1.09,
		BodyPct: 9, BodyX: 4.5, VolX: 200, DollarVol: 218_000, Early: 0})
	r.OnSignal(core.SignalHit{Sym: "UNKN", Kind: dildopb.KindDildo, BarMs: c, Price: 1.09, O: 1, C: 1.09,
		BodyPct: 9, BodyX: 4.5, VolX: 200, DollarVol: 218_000, Early: -1})
	u := dupd("CHRT", c+5*60_000, true, dev(c+60_000, dildopb.KindDildo, 1.2))
	u.Early = map[int64]bool{c + 60_000: true}
	r.OnDildo(u)
	u = dupd("CHNO", c+5*60_000, true, dev(c+60_000, dildopb.KindDildo, 1.2))
	u.Early = map[int64]bool{c + 60_000: false}
	r.OnDildo(u)
	msg, _ := r.Day(context.Background(), "")
	early := map[string]bool{}
	for _, e := range msg.Events {
		early[e.Sym] = len(e.Note) >= len(EarlyNotePrefix) && e.Note[:len(EarlyNotePrefix)] == EarlyNotePrefix
	}
	want := map[string]bool{"EARL": true, "LATE": false, "UNKN": false, "CHRT": true, "CHNO": false}
	for s, w := range want {
		if got, ok := early[s]; !ok || got != w {
			t.Errorf("%s: early note %v (recorded %v), want %v", s, got, ok, w)
		}
	}
}
