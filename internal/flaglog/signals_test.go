package flaglog

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/climax"
	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
	"github.com/eelkonkoopman/marketscanner/internal/flagscan"
	"github.com/eelkonkoopman/marketscanner/internal/persist"
)

// The GREEN DILDO -> 9 EMA PULLBACK and VOLUME CLIMAX -> BOTTOM signals in the
// history: flagscan's dildo rule events (OnDildo) and the engine's alerts
// (OnSignal / RunSignals), recorded once per (sym, bar, kind) like the flag
// kinds, with a one-line note.

func dupd(sym string, nowMs int64, hot bool, evs ...dildopb.Event) flagscan.DildoUpdate {
	return flagscan.DildoUpdate{Sym: sym, NowMs: nowMs, HotOnly: hot, Result: dildopb.Result{Events: evs}}
}

func dev(ts int64, kind string, price float64) dildopb.Event {
	label := map[string]string{dildopb.KindDildo: dildopb.LabelDildo, dildopb.KindPb: dildopb.LabelPb, dildopb.KindCancel: dildopb.LabelCancel}[kind]
	return dildopb.Event{Ts: ts, Kind: kind, Label: label, Price: price, EmaDistPct: math.NaN()}
}

func kinds(evs []core.FlagEvent) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Sym + "/" + e.Kind
	}
	return out
}

func pubKinds(msgs []core.FlagEventsMsg) [][]string {
	var out [][]string
	for _, m := range msgs {
		out = append(out, kinds(m.Events))
	}
	return out
}

// mbDay is MB 2026-08-07 (the calibration's reference dildo: 16:51, its 9 EMA
// pullback 17:01) through dildopb.Evaluate at 17:05 ET - the rule's real
// events, notes included.
func mbDay(t *testing.T) (res dildopb.Result, dildoTs, pbTs, nowMs int64) {
	t.Helper()
	at := func(h, m int) int64 { return time.Date(2026, 8, 7, h, m, 0, 0, et).UnixMilli() }
	nowMs = at(17, 5)
	return dildopb.Evaluate(mbBars(t, nowMs), 60_000, nowMs, dildopb.DefaultParams()), at(16, 51), at(17, 1), nowMs
}

// mbBars is MB 2026-08-07's closed 1-minute bars before nowMs.
func mbBars(t *testing.T, nowMs int64) []core.Bar {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "signals", "fixtures", "dildo", "dildo-mb-2026-08-07.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Bars []core.Bar `json:"bars"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	var bars []core.Bar
	for _, b := range fx.Bars {
		if b.T < nowMs {
			bars = append(bars, b)
		}
	}
	return bars
}

func TestOnDildoRecordsTheRuleEventsOnceWithNotes(t *testing.T) {
	res, dildoTs, pbTs, nowMs := mbDay(t)
	r := New(newFakeRepo(), func() time.Time { return time.UnixMilli(nowMs) })
	pubs := collect(r)
	u := flagscan.DildoUpdate{Sym: "MB", NowMs: nowMs, HotOnly: true, Result: res}
	r.OnDildo(u)
	r.OnDildo(u) // the next evaluation repeats every event: nothing new
	msg, _ := r.Day(context.Background(), "")
	var dildo, pb *core.FlagEvent
	for i := range msg.Events {
		e := &msg.Events[i]
		switch {
		case e.Ts == dildoTs && e.Kind == dildopb.KindDildo:
			dildo = e
		case e.Ts == pbTs && e.Kind == dildopb.KindPb:
			pb = e
		}
	}
	if dildo == nil || pb == nil {
		t.Fatalf("want the 16:51 dildo and the 17:01 pullback: %v", kinds(msg.Events))
	}
	if dildo.Label != "GREEN DILDO" || dildo.Price == nil || dildo.Sym != "MB" || dildo.At != nowMs ||
		!strings.HasPrefix(dildo.Note, "+") || !strings.Contains(dildo.Note, "% body (") || !strings.Contains(dildo.Note, "; floor ") {
		t.Fatalf("dildo row %+v", *dildo)
	}
	if pb.Label != "9 EMA PULLBACK" || pb.Price == nil || math.Abs(*pb.Price-7.93) > 0.005 ||
		!strings.HasPrefix(pb.Note, "close ") || !strings.Contains(pb.Note, " vs EMA ") || !strings.HasSuffix(pb.Note, "10 min after the dildo") {
		t.Fatalf("pullback row %+v", *pb)
	}
	got := pubs()
	if len(got) != 1 || len(got[0].Events) != len(msg.Events) {
		t.Fatalf("one publish of every event, then none: %v", pubKinds(got))
	}
	// The note is the chart's own (dildopb.Event.Note) without the clock time
	// the row already shows.
	for _, ev := range res.Events {
		if ev.Ts == pbTs && ev.Kind == dildopb.KindPb && ev.Note() != "17:01 pullback to the 9 EMA held: "+pb.Note {
			t.Fatalf("pb note %q vs the chart's %q", pb.Note, ev.Note())
		}
	}
}

func TestOnDildoNotesForEachKind(t *testing.T) {
	tue := etMs(2026, 10, 6, 10, 0)
	r := New(newFakeRepo(), func() time.Time { return nowT })
	cancel := dev(tue+3*60_000, dildopb.KindCancel, 0.95)
	cancel.Reason = dildopb.ReasonFaded
	r.OnDildo(dupd("AAA", tue+5*60_000, false, dev(tue, dildopb.KindDildo, 1.09), cancel))
	msg, _ := r.Day(context.Background(), "")
	if len(msg.Events) != 2 {
		t.Fatalf("events %v", kinds(msg.Events))
	}
	if n := msg.Events[1].Note; !strings.HasPrefix(n, "faded below the floor") || msg.Events[1].Label != "PB CANCELLED" {
		t.Fatalf("cancel note %q label %q", n, msg.Events[1].Label)
	}
}

// One pullback verdict per dildo: a resync that moves the 9 EMA PULLBACK to
// another bar, or turns it into a PB CANCELLED, adds no second row; the next
// dildo's verdict is its own.
func TestOnDildoOneVerdictPerDildo(t *testing.T) {
	m := func(k int64) int64 { return tue10 + k*60_000 }
	r := New(newFakeRepo(), func() time.Time { return nowT })
	pubs := collect(r)
	now := m(20)
	r.OnDildo(dupd("AAA", now, true, dev(m(0), dildopb.KindDildo, 1), dev(m(4), dildopb.KindPb, 1.05)))
	r.OnDildo(dupd("AAA", now, true, dev(m(0), dildopb.KindDildo, 1), dev(m(5), dildopb.KindPb, 1.04)))    // moved
	r.OnDildo(dupd("AAA", now, true, dev(m(0), dildopb.KindDildo, 1), dev(m(4), dildopb.KindCancel, 0.9))) // re-judged
	r.OnDildo(dupd("BBB", now, true, dev(m(0), dildopb.KindDildo, 1), dev(m(4), dildopb.KindPb, 1.05)))    // another symbol
	r.OnDildo(dupd("AAA", now, true, dev(m(0), dildopb.KindDildo, 1), dev(m(4), dildopb.KindPb, 1.05),
		dev(m(8), dildopb.KindDildo, 1.2), dev(m(12), dildopb.KindCancel, 1.1))) // a new dildo, its own verdict
	want := [][]string{
		{"AAA/dildo", "AAA/dildoPb"},
		{"BBB/dildo", "BBB/dildoPb"},
		{"AAA/dildo", "AAA/dildoCancel"},
	}
	if got := pubKinds(pubs()); !reflect.DeepEqual(got, want) {
		t.Fatalf("publishes %v, want %v", got, want)
	}
}

// After a restart Load rebuilds the guard from today's rows: the replay (the
// same events, or a resync's moved verdict) adds nothing.
func TestLoadRebuildsTheVerdictGuard(t *testing.T) {
	m := func(k int64) int64 { return tue10 + k*60_000 }
	repo := newFakeRepo()
	repo.rows["2026-10-06"] = []core.FlagEvent{
		{Ts: m(0), Sym: "AAA", Kind: dildopb.KindDildo, Label: "GREEN DILDO", Note: "engine"},
		{Ts: m(4), Sym: "AAA", Kind: dildopb.KindPb, Label: "9 EMA PULLBACK"},
		{Ts: m(4), Sym: "AAA", Kind: dildopb.KindDildo, Label: "GREEN DILDO"}, // the verdict bar is itself a new dildo
	}
	r := New(repo, func() time.Time { return nowT })
	pubs := collect(r)
	if err := r.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.OnDildo(dupd("AAA", m(20), true, dev(m(0), dildopb.KindDildo, 1), dev(m(4), dildopb.KindPb, 1.05), dev(m(4), dildopb.KindDildo, 1.1)))
	r.OnDildo(dupd("AAA", m(20), true, dev(m(0), dildopb.KindDildo, 1), dev(m(5), dildopb.KindCancel, 0.9))) // moved verdict of the 10:00 dildo
	if got := pubs(); len(got) != 0 {
		t.Fatalf("the restart's replay must add nothing: %v", pubKinds(got))
	}
	// The 10:04 dildo has no verdict yet: its pullback is new.
	r.OnDildo(dupd("AAA", m(20), true, dev(m(4), dildopb.KindDildo, 1.1), dev(m(9), dildopb.KindPb, 1.15)))
	if got := pubKinds(pubs()); !reflect.DeepEqual(got, [][]string{{"AAA/dildoPb"}}) {
		t.Fatalf("the 10:04 dildo's pullback: %v", got)
	}
	if msg, _ := r.Day(context.Background(), ""); msg.Events[0].Note != "engine" {
		t.Fatalf("loaded rows keep their note: %+v", msg.Events[0])
	}
}

// A PB CANCELLED the rule later turns into a 9 EMA PULLBACK (RV1-D1), with the
// real rule on MB 2026-08-07: the tap's 17:01 bar missed the minute's last
// prints (foldAgg drops a minute's late aggregates once the next minute has
// opened), so it read o 7.8899 h 7.93 l 7.69 c 7.70 and the rule called PB
// CANCELLED; the REST resync restored the 7.93 close - the same bar, only the
// close changed - and the rule's call became the 9 EMA PULLBACK (PbState 3:
// the dildo-pb alert, the chart's 9 EMA PB marker). The PB is alert 2 itself:
// it gets its row (the cancel's stays), and after it that dildo adds nothing.
func TestOnDildoPbSupersedesARecordedCancel(t *testing.T) {
	rest, dildoTs, pbTs, nowMs := mbDay(t)
	bars := mbBars(t, nowMs)
	for i := range bars {
		if bars[i].T == pbTs {
			bars[i].L, bars[i].C = 7.69, 7.70
		}
	}
	tap := dildopb.Evaluate(bars, 60_000, nowMs, dildopb.DefaultParams())
	has := func(res dildopb.Result, kind string) bool {
		for _, e := range res.Events {
			if e.Ts == pbTs && e.Kind == kind {
				return true
			}
		}
		return false
	}
	if !has(tap, dildopb.KindCancel) || has(tap, dildopb.KindPb) || !has(rest, dildopb.KindPb) || rest.PbState != dildopb.PbConfirmed {
		t.Fatalf("fixture: the tap's bar must cancel at 17:01 and the REST bar confirm (PbState %d)", rest.PbState)
	}

	r := New(newFakeRepo(), func() time.Time { return time.UnixMilli(nowMs) })
	pubs := collect(r)
	r.OnDildo(flagscan.DildoUpdate{Sym: "MB", NowMs: nowMs, Result: tap})
	r.OnDildo(flagscan.DildoUpdate{Sym: "MB", NowMs: nowMs + 1000, Result: rest})
	r.OnDildo(flagscan.DildoUpdate{Sym: "MB", NowMs: nowMs + 2000, Result: tap}) // flipped back: nothing
	got := pubs()
	if len(got) != 2 || !reflect.DeepEqual(kinds(got[1].Events), []string{"MB/dildoPb"}) {
		t.Fatalf("publishes %v, want the morning, then the 17:01 9 EMA PULLBACK alone", pubKinds(got))
	}
	msg, _ := r.Day(context.Background(), "")
	var rows []string
	for _, e := range msg.Events {
		if e.Ts >= dildoTs {
			rows = append(rows, time.UnixMilli(e.Ts).In(et).Format("15:04")+" "+e.Kind)
		}
	}
	if want := []string{"16:51 dildo", "17:01 dildoCancel", "17:01 dildoPb"}; !reflect.DeepEqual(rows, want) {
		t.Fatalf("history from the 16:51 dildo on %v, want %v", rows, want)
	}
}

// The same supersede on the rule's other cancel-then-confirm rewrites (a bad
// print that broke the floor, later removed by REST: the PB lands on another
// bar). Only a recorded PB closes the dildo: a second cancel adds nothing, a
// PB after the PB adds nothing, a cancel after the PB adds nothing.
func TestOnDildoOnePbPerDildoAfterACancel(t *testing.T) {
	m := func(k int64) int64 { return tue10 + k*60_000 }
	r := New(newFakeRepo(), func() time.Time { return nowT })
	pubs := collect(r)
	now := m(20)
	r.OnDildo(dupd("AAA", now, true, dev(m(0), dildopb.KindDildo, 1), dev(m(3), dildopb.KindCancel, 0.9))) // the bad print
	r.OnDildo(dupd("AAA", now, true, dev(m(0), dildopb.KindDildo, 1), dev(m(4), dildopb.KindCancel, 0.9))) // moved cancel
	r.OnDildo(dupd("AAA", now, true, dev(m(0), dildopb.KindDildo, 1), dev(m(5), dildopb.KindPb, 1.05)))    // REST removed it
	r.OnDildo(dupd("AAA", now, true, dev(m(0), dildopb.KindDildo, 1), dev(m(6), dildopb.KindPb, 1.04)))    // moved PB
	r.OnDildo(dupd("AAA", now, true, dev(m(0), dildopb.KindDildo, 1), dev(m(7), dildopb.KindCancel, 0.9))) // flipped back
	want := [][]string{{"AAA/dildo", "AAA/dildoCancel"}, {"AAA/dildoPb"}}
	if got := pubKinds(pubs()); !reflect.DeepEqual(got, want) {
		t.Fatalf("publishes %v, want %v", got, want)
	}
}

// After a restart the rebuilt guard still tells a cancel from a PB: a PB
// CANCELLED row admits the 9 EMA PULLBACK the replay (over the REST bars)
// calls instead - the history heals - and a PB row admits nothing.
func TestLoadKeepsACancelledDildoOpenForItsPb(t *testing.T) {
	m := func(k int64) int64 { return tue10 + k*60_000 }
	load := func(t *testing.T, rows ...core.FlagEvent) (*Recorder, func() []core.FlagEventsMsg) {
		t.Helper()
		repo := newFakeRepo()
		repo.rows["2026-10-06"] = rows
		r := New(repo, func() time.Time { return nowT })
		pubs := collect(r)
		if err := r.Load(context.Background()); err != nil {
			t.Fatal(err)
		}
		return r, pubs
	}
	dildo := core.FlagEvent{Ts: m(0), Sym: "AAA", Kind: dildopb.KindDildo, Label: "GREEN DILDO"}
	cancel := core.FlagEvent{Ts: m(3), Sym: "AAA", Kind: dildopb.KindCancel, Label: "PB CANCELLED"}
	pb := core.FlagEvent{Ts: m(3), Sym: "AAA", Kind: dildopb.KindPb, Label: "9 EMA PULLBACK"}

	t.Run("cancel only: the replay's PB is recorded once", func(t *testing.T) {
		r, pubs := load(t, dildo, cancel)
		r.OnDildo(dupd("AAA", m(20), true, dev(m(0), dildopb.KindDildo, 1), dev(m(3), dildopb.KindPb, 1.05)))
		r.OnDildo(dupd("AAA", m(20), true, dev(m(0), dildopb.KindDildo, 1), dev(m(4), dildopb.KindPb, 1.04)))
		if got := pubKinds(pubs()); !reflect.DeepEqual(got, [][]string{{"AAA/dildoPb"}}) {
			t.Fatalf("publishes %v", got)
		}
	})
	t.Run("cancel then its PB: nothing more", func(t *testing.T) {
		r, pubs := load(t, dildo, cancel, pb)
		r.OnDildo(dupd("AAA", m(20), true, dev(m(0), dildopb.KindDildo, 1), dev(m(4), dildopb.KindPb, 1.04)))
		r.OnDildo(dupd("AAA", m(20), true, dev(m(0), dildopb.KindDildo, 1), dev(m(5), dildopb.KindCancel, 0.9)))
		if got := pubs(); len(got) != 0 {
			t.Fatalf("publishes %v", pubKinds(got))
		}
	})
}

// A day's rows do not say which dildo a verdict judged, and a dildo row may be
// the engine's alone (RV1-D2: a bar the chart rule never called a dildo - a
// parity miss, or the engine's short ring after a restart). After a restart a
// resync that moves the chart dildo's verdict must add no row.
func TestLoadGuardsTheVerdictPastAnEngineOnlyDildo(t *testing.T) {
	m := func(k int64) int64 { return tue10 + k*60_000 }
	repo := newFakeRepo()
	repo.rows["2026-10-06"] = []core.FlagEvent{
		{Ts: m(0), Sym: "AAA", Kind: dildopb.KindDildo, Label: "GREEN DILDO"},                 // the chart's (and the engine's)
		{Ts: m(2), Sym: "AAA", Kind: dildopb.KindDildo, Label: "GREEN DILDO", Note: "engine"}, // the engine's alone
		{Ts: m(5), Sym: "AAA", Kind: dildopb.KindPb, Label: "9 EMA PULLBACK"},                 // the 10:00 dildo's
	}
	r := New(repo, func() time.Time { return nowT })
	pubs := collect(r)
	if err := r.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.OnDildo(dupd("AAA", m(20), true, dev(m(0), dildopb.KindDildo, 1), dev(m(6), dildopb.KindPb, 1.05)))
	if got := pubs(); len(got) != 0 {
		t.Fatalf("a moved verdict after the restart added %v", pubKinds(got))
	}
}

// A verdict row whose dildo row never reached the DB (its write failed): the
// restart's replay records the dildo (healing the gap) and names it as that
// verdict's dildo, so a later move of the verdict adds no row.
func TestReplayAttributesARecordedVerdictToItsDildo(t *testing.T) {
	m := func(k int64) int64 { return tue10 + k*60_000 }
	repo := newFakeRepo()
	repo.rows["2026-10-06"] = []core.FlagEvent{{Ts: m(5), Sym: "AAA", Kind: dildopb.KindPb, Label: "9 EMA PULLBACK"}}
	r := New(repo, func() time.Time { return nowT })
	pubs := collect(r)
	if err := r.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.OnDildo(dupd("AAA", m(20), true, dev(m(0), dildopb.KindDildo, 1), dev(m(5), dildopb.KindPb, 1.05)))
	r.OnDildo(dupd("AAA", m(21), true, dev(m(0), dildopb.KindDildo, 1), dev(m(6), dildopb.KindPb, 1.04)))
	if got := pubKinds(pubs()); !reflect.DeepEqual(got, [][]string{{"AAA/dildo"}}) {
		t.Fatalf("publishes %v, want the healed dildo only", got)
	}
}

// The engine's alerts (core.SignalHit) as rows: labels, prices, notes.
func TestOnSignalRecordsTheEngineAlerts(t *testing.T) {
	c := etMs(2026, 10, 6, 11, 10)
	r := New(newFakeRepo(), func() time.Time { return time.UnixMilli(c + 10*60_000) })
	pubs := collect(r)
	r.OnSignal(core.SignalHit{Sym: "SBFM", Kind: dildopb.KindDildo, BarMs: c - 60_000, Price: 1.09, O: 1.00, C: 1.09,
		BodyPct: 9, BodyX: 4.5, VolX: 200, DollarVol: 218_000})
	r.OnSignal(core.SignalHit{Sym: "BIYA", Kind: climax.KindClimax, BarMs: c, Price: 9.00, Low: 9.00,
		DropPct: 12.6, VolX: math.Inf(1), WickPct: 58.8, DollarVol: 1_250_000, Mid: 9.4})
	r.OnSignal(core.SignalHit{Sym: "BIYA", Kind: climax.KindBottom, BarMs: c + 60_000, Price: 9.60, Mid: 9.4, Low: 9.00, ClimaxMs: c, Bars: 1})
	r.OnSignal(core.SignalHit{Sym: "XNDU", Kind: climax.KindFail, BarMs: c + 3*60_000, Price: 8.99, Mid: 9.4, Low: 9.00, ClimaxMs: c, Bars: 2})
	msg, _ := r.Day(context.Background(), "")
	type row struct{ kind, label, note string }
	var got []row
	for _, e := range msg.Events {
		got = append(got, row{e.Kind, e.Label, e.Note})
	}
	want := []row{
		{"dildo", "GREEN DILDO", "+9.0% body (4.5x prior), 200x volume, $218k; floor 1.04"},
		{"climax", "VOLUME CLIMAX", "12.6% under the 10-bar high, ∞x volume, 59% lower wick, $1.3M; bottom on a close above 9.40"},
		{"bottom", "BOTTOM CONFIRMED", "climax low 9.00 is 6.2% under, 1 bar after the 11:10 climax; midpoint 9.40"},
		{"climaxFail", "CLIMAX FAILED", "undercut the 11:10 climax low 9.00"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows\n got %+v\nwant %+v", got, want)
	}
	if p := msg.Events[0].Price; p == nil || *p != 1.09 {
		t.Fatalf("dildo price %v", p)
	}
	if p := msg.Events[1].Price; p == nil || *p != 9.00 {
		t.Fatalf("climax price (its low) %v", p)
	}
	if len(pubs()) != 4 {
		t.Fatalf("one publish per alert: %v", pubKinds(pubs()))
	}

	// The same alert again, or the chart rule's dildo on the same bar: no row.
	r.OnSignal(core.SignalHit{Sym: "SBFM", Kind: dildopb.KindDildo, BarMs: c - 60_000, Price: 1.09})
	r.OnDildo(dupd("SBFM", c+10*60_000, true, dev(c-60_000, dildopb.KindDildo, 1.09)))
	if msg, _ := r.Day(context.Background(), ""); len(msg.Events) != 4 || len(pubs()) != 4 {
		t.Fatalf("a repeat added a row: %v", kinds(msg.Events))
	}
	if msg.Events[0].Note != want[0].note {
		t.Fatal("the first recording's note wins")
	}
}

func TestOnSignalIgnoresOtherDaysAndUnknownKinds(t *testing.T) {
	r := New(newFakeRepo(), func() time.Time { return nowT })
	pubs := collect(r)
	r.OnSignal(core.SignalHit{Sym: "OLD", Kind: dildopb.KindDildo, BarMs: etMs(2026, 10, 5, 19, 59), Price: 1})
	r.OnSignal(core.SignalHit{Sym: "AAA", Kind: "mystery", BarMs: tue10, Price: 1})
	r.OnSignal(core.SignalHit{Sym: "AAA", Kind: climax.KindClimax, BarMs: tue10, Price: math.NaN()})
	msg, _ := r.Day(context.Background(), "")
	if len(msg.Events) != 1 || msg.Events[0].Sym != "AAA" || msg.Events[0].Price != nil || len(pubs()) != 1 {
		t.Fatalf("events %+v", msg.Events)
	}
}

func TestRunSignalsRecordsUntilCancelled(t *testing.T) {
	r := New(newFakeRepo(), func() time.Time { return nowT })
	ch := make(chan core.SignalHit, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.RunSignals(ctx, ch, nil) }()
	ch <- core.SignalHit{Sym: "AAA", Kind: climax.KindClimax, BarMs: tue10, Price: 2}
	deadline := time.After(2 * time.Second)
	for {
		if msg, _ := r.Day(context.Background(), ""); len(msg.Events) == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the hit never reached the history")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunSignals did not stop")
	}
	if err := r.RunSignals(context.Background(), nil, nil); err != nil { // off: returns at once
		t.Fatal(err)
	}
}

// lockedBuf is a log sink safe to read while another goroutine logs.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// An alert the engine dropped (its SignalHits hand-off was full: RV1-D3) is
// a row missing from the history for good - the engine's alerts are never
// replayed - so RunSignals logs whenever Engine.SignalDrops rises.
func TestRunSignalsLogsDroppedAlerts(t *testing.T) {
	var buf lockedBuf
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	r := New(newFakeRepo(), func() time.Time { return nowT })
	ch := make(chan core.SignalHit)
	var drops atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.RunSignals(ctx, ch, drops.Load) }()
	lines := func() []string {
		var out []string
		for _, l := range strings.Split(buf.String(), "\n") {
			if strings.Contains(l, "dropped") {
				out = append(out, l)
			}
		}
		return out
	}
	waitLines := func(n int) {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for len(lines()) < n {
			select {
			case <-deadline:
				t.Fatalf("want %d drop lines, log:\n%s", n, buf.String())
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	hit := func(k int64) core.SignalHit {
		return core.SignalHit{Sym: "AAA", Kind: climax.KindClimax, BarMs: tue10 + k*60_000, Price: 2}
	}

	ch <- hit(0) // no drops: no line
	drops.Store(3)
	ch <- hit(1)
	waitLines(1)
	ch <- hit(2) // no new drops: no line
	drops.Store(5)
	ch <- hit(3)
	waitLines(2)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := lines()
	if len(got) != 2 || !strings.Contains(got[0], "WARN") || !strings.Contains(got[0], "count=3 total=3") ||
		!strings.Contains(got[1], "count=2 total=5") {
		t.Fatalf("drop lines %q", got)
	}
}

// End to end over the real database: every kind is persisted with its note
// by the writer, and after a restart Load serves today's history - as today -
// from the DB while the replay adds nothing.
func TestPersistAndRestartLoadEveryKind(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "scanner.db")
	db, err := persist.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	m := func(k int64) int64 { return tue10 + k*60_000 }
	clock := func() time.Time { return time.UnixMilli(m(30)) }

	flag := upd("BEAT", m(30), ev(m(1), "entry", 0.65))
	dildos := dupd("SBFM", m(30), true, dev(m(2), dildopb.KindDildo, 1.09), dev(m(6), dildopb.KindPb, 1.12))
	hits := []core.SignalHit{
		{Sym: "SBFM", Kind: dildopb.KindDildo, BarMs: m(2), Price: 1.09, O: 1, C: 1.09, BodyPct: 9, BodyX: 4.5, VolX: 200, DollarVol: 218_000},
		{Sym: "BIYA", Kind: climax.KindClimax, BarMs: m(3), Price: 9, Low: 9, DropPct: 12.6, VolX: 20, WickPct: 58.8, DollarVol: 190_000, Mid: 9.4},
		{Sym: "BIYA", Kind: climax.KindBottom, BarMs: m(4), Price: 9.6, Mid: 9.4, Low: 9, ClimaxMs: m(3), Bars: 1},
	}

	r1 := New(db.FlagEventRepo(), clock)
	if err := r1.Load(ctx); err != nil {
		t.Fatal(err)
	}
	rctx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- r1.Run(rctx) }()
	for _, h := range hits {
		r1.OnSignal(h)
	}
	r1.OnEval(flag)
	r1.OnDildo(dildos)
	before, _ := r1.Day(ctx, "")
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db2, err := persist.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	r2 := New(db2.FlagEventRepo(), clock)
	pubs := collect(r2)
	if err := r2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := r2.Day(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if after.Date != "2026-10-06" || after.Dates[0] != "2026-10-06" {
		t.Fatalf("restart: served %q dates %v, want today", after.Date, after.Dates)
	}
	if !reflect.DeepEqual(after.Events, before.Events) {
		t.Fatalf("restart lost or changed rows:\n got %+v\nwant %+v", after.Events, before.Events)
	}
	if got := kinds(after.Events); !reflect.DeepEqual(got, []string{"BEAT/entry", "SBFM/dildo", "BIYA/climax", "BIYA/bottom", "SBFM/dildoPb"}) {
		t.Fatalf("kinds %v", got)
	}
	for _, e := range after.Events {
		if (e.Kind == "entry") != (e.Note == "") {
			t.Fatalf("%s/%s note %q", e.Sym, e.Kind, e.Note)
		}
	}
	// The restart's replay: nothing is new.
	for _, h := range hits {
		r2.OnSignal(h)
	}
	r2.OnEval(flag)
	r2.OnDildo(dildos)
	if got := pubs(); len(got) != 0 {
		t.Fatalf("the replay published %v", pubKinds(got))
	}
}
