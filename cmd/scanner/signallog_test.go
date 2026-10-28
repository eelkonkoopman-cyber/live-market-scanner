package main

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/climax"
	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
	"github.com/eelkonkoopman/marketscanner/internal/emaflag"
	"github.com/eelkonkoopman/marketscanner/internal/engine"
	"github.com/eelkonkoopman/marketscanner/internal/flaglog"
	"github.com/eelkonkoopman/marketscanner/internal/flagscan"
	"github.com/eelkonkoopman/marketscanner/internal/persist"
)

// The SIGNALS history wiring: which sources build the recorder, the hooks it
// is chained into, and the engine's alerts reaching it end to end.

func TestSignalLogWantedWhenAnySourceRuns(t *testing.T) {
	ch := make(chan core.SignalHit)
	for _, c := range []struct {
		name string
		e9   bool
		hits <-chan core.SignalHit
		want bool
	}{
		{"everything off", false, nil, false},
		{"flag scan only", true, nil, true},
		{"engine alerts only (SCANNER_E9FLAGS=0)", false, ch, true},
		{"both", true, ch, true},
	} {
		if got := signalLogWanted(c.e9, c.hits); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// SCANNER_DILDO / SCANNER_CLIMAX reach the history through the engine: with
// both off main's engine has no alert hand-off (and, with the flag scan off
// too, no recorder); either one on wires it.
func TestSignalHitsFollowTheKillSwitches(t *testing.T) {
	build := func() *engine.Engine {
		return engine.New(engine.Options{Dildo: dildoEnabled(), Climax: climaxEnabled(), SignalLog: true})
	}
	t.Setenv("SCANNER_DILDO", "0")
	t.Setenv("SCANNER_CLIMAX", "0")
	if build().SignalHits() != nil {
		t.Fatal("both switches off: no alert hand-off")
	}
	t.Setenv("SCANNER_CLIMAX", "")
	if build().SignalHits() == nil {
		t.Fatal("the climax on: the hand-off must be wired")
	}
	t.Setenv("SCANNER_CLIMAX", "0")
	t.Setenv("SCANNER_DILDO", "1")
	if build().SignalHits() == nil {
		t.Fatal("the dildo on: the hand-off must be wired")
	}
}

// wireSignalLog chains the recorder in front of what is already wired (the
// auto-trader's OnEval) and adds OnDildo; a nil recorder changes nothing.
func TestWireSignalLogChainsBothHooks(t *testing.T) {
	ctx := context.Background()
	db, err := persist.Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	rec := flaglog.New(db.FlagEventRepo(), func() time.Time { return now })

	var evals, dildos int
	cfg := flagscan.Config{
		OnEval:  func(flagscan.EvalUpdate) { evals++ },
		OnDildo: func(flagscan.DildoUpdate) { dildos++ },
	}
	if got := wireSignalLog(cfg, nil); got.OnDildo == nil || got.OnEval == nil {
		t.Fatal("a nil recorder must leave the hooks as they are")
	}
	w := wireSignalLog(cfg, rec)
	ms := now.UnixMilli() / 60_000 * 60_000
	w.OnEval(flagscan.EvalUpdate{Sym: "BEAT", NowMs: now.UnixMilli(), Result: emaflag.Result{
		Events: []emaflag.Event{{Ts: ms, Kind: emaflag.KindEntry, Label: "LONG (FLAG)", Price: 0.65}}}})
	w.OnDildo(flagscan.DildoUpdate{Sym: "SBFM", NowMs: now.UnixMilli(), HotOnly: true, Result: dildopb.Result{
		Events: []dildopb.Event{{Ts: ms, Kind: dildopb.KindDildo, Label: dildopb.LabelDildo, Price: 1.09}}}})
	if evals != 1 || dildos != 1 {
		t.Fatalf("the hooks already wired ran %d / %d times, want 1 / 1", evals, dildos)
	}
	msg, err := rec.Day(ctx, "")
	if err != nil || len(msg.Events) != 2 {
		t.Fatalf("recorded %+v err=%v", msg.Events, err)
	}

	// Nothing wired before: the recorder alone.
	only := wireSignalLog(flagscan.Config{}, rec)
	if only.OnEval == nil || only.OnDildo == nil {
		t.Fatal("the recorder's hooks must be wired")
	}
	only.OnDildo(flagscan.DildoUpdate{Sym: "MB", NowMs: now.UnixMilli(), Result: dildopb.Result{
		Events: []dildopb.Event{{Ts: ms, Kind: dildopb.KindDildo, Label: dildopb.LabelDildo, Price: 7}}}})
	if msg, _ := rec.Day(ctx, ""); len(msg.Events) != 3 {
		t.Fatalf("recorded %d, want 3", len(msg.Events))
	}
}

// The production path for the engine's alerts: a real Engine (Climax on,
// SignalLog on) fed BIYA 2026-10-07's bars as aggregates hands its 11:03
// VOLUME CLIMAX and 11:04 BOTTOM CONFIRMED to a recorder over a real database
// through RunSignals - each exactly once, priced and noted - and the writer
// persists them.
func TestEngineAlertsReachTheHistoryBIYA(t *testing.T) {
	raw, err := os.ReadFile("../../web/src/signals/fixtures/climax/climax-biya-2026-10-07.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Bars []core.Bar `json:"bars"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	et, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tz database:", err)
	}
	climaxTs := time.Date(2026, 10, 7, 11, 3, 0, 0, et).UnixMilli()

	var clock atomic.Int64
	clock.Store(fx.Bars[0].T - 60_000)
	now := func() time.Time { return time.UnixMilli(clock.Load()) }
	eng := engine.New(engine.Options{TickHz: 20, Now: now, Climax: true, SignalLog: true})
	clock.Store(climaxTs + 3*60_000)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()
	db, err := persist.Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rec := flaglog.New(db.FlagEventRepo(), now)
	if err := rec.Load(ctx); err != nil {
		t.Fatal(err)
	}
	var pubs atomic.Int64
	rec.SetPublish(func(core.FlagEventsMsg) { pubs.Add(1) })
	wctx, stopWriter := context.WithCancel(ctx)
	writerDone := make(chan error, 1)
	go func() { writerDone <- rec.Run(wctx) }()
	go func() { _ = rec.RunSignals(ctx, eng.SignalHits(), eng.SignalDrops) }()

	for _, b := range fx.Bars {
		if b.T > climaxTs+60_000 {
			break
		}
		eng.Apply(core.Agg{Sym: "BIYA", Open: b.O, High: b.H, Low: b.L, Close: b.C, Vol: b.V, StartMs: b.T, EndMs: b.T + 1000})
	}
	var climaxRow, bottomRow *core.FlagEvent
	deadline := time.After(5 * time.Second)
	for climaxRow == nil || bottomRow == nil {
		msg, err := rec.Day(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		climaxRow, bottomRow = nil, nil
		for i := range msg.Events {
			e := &msg.Events[i]
			switch {
			case e.Sym == "BIYA" && e.Kind == climax.KindClimax && e.Ts == climaxTs:
				climaxRow = e
			case e.Sym == "BIYA" && e.Kind == climax.KindBottom && e.Ts == climaxTs+60_000:
				bottomRow = e
			}
		}
		if climaxRow != nil && bottomRow != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("the 11:03 climax and the 11:04 bottom never reached the history: %+v", msg.Events)
		case <-time.After(20 * time.Millisecond):
		}
	}
	if climaxRow.Label != "VOLUME CLIMAX" || climaxRow.Price == nil || *climaxRow.Price != 1.78 ||
		!strings.Contains(climaxRow.Note, "under the 10-bar high") {
		t.Fatalf("climax row %+v", *climaxRow)
	}
	if bottomRow.Label != "BOTTOM CONFIRMED" || bottomRow.Price == nil || math.Abs(*bottomRow.Price-1.879) > 1e-9 ||
		!strings.Contains(bottomRow.Note, "after the 11:03 climax") {
		t.Fatalf("bottom row %+v", *bottomRow)
	}
	stopWriter()
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	saved, err := db.FlagEventRepo().Day(ctx, "2026-10-07")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range saved {
		if e.Sym == "BIYA" && (e.Kind == climax.KindClimax || e.Kind == climax.KindBottom) && e.Ts >= climaxTs {
			n++
			if e.Note == "" {
				t.Fatalf("persisted without its note: %+v", e)
			}
		}
	}
	if n != 2 {
		t.Fatalf("persisted %d of the two alerts: %+v", n, saved)
	}
	if eng.SignalDrops() != 0 {
		t.Fatalf("drops %d", eng.SignalDrops())
	}
}
