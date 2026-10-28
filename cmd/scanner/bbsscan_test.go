package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/engine"
	"github.com/eelkonkoopman/marketscanner/internal/scan"
)

func TestSeedBbsScanOnceUnderItsOwnKey(t *testing.T) {
	for _, k := range []string{"seeded.flagscans.v1", "seeded.sessreview.v1", "seeded.gapfade.v1", "seeded.advscan.v1",
		e9FlagSeedKey, c1mSeedKey, e9SlamSeedKey, dildoSeedKey, climaxSeedKey} {
		if k == bbsSeedKey {
			t.Fatalf("seed key %q is taken", k)
		}
	}
	h := newSeedHarness()
	seedScans(context.Background(), bbsSeedKey, bbsScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if strings.Join(h.saved, ",") != "bouncy-short" || h.meta[bbsSeedKey] != "1" {
		t.Fatalf("saved %v meta %v", h.saved, h.meta)
	}
	h.saved = nil
	seedScans(context.Background(), bbsSeedKey, bbsScans(), h.getMeta, h.setMeta, nil, h.save, h.install)
	if len(h.saved) != 0 {
		t.Fatal("re-seeded")
	}
}

func TestBbsScanDefShape(t *testing.T) {
	d := bbsScans()[0]
	if d.Name != "Bouncy Ball Short" || !d.Alert.Enabled || d.Alert.On != core.AlertOnEnter || d.Alert.Sound != "bounce" ||
		d.Alert.CooldownSec != 600 || !d.Alert.Speak {
		t.Fatalf("%+v", d)
	}
}

// End to end: a real Engine (Bbs on) fed the synthetic AAOI day from the golden
// file raises exactly one "Bouncy Ball Short" enter for the 10:40 break.
func TestBbsEndToEndAAOI(t *testing.T) {
	raw, err := os.ReadFile("../../web/src/signals/fixtures/golden/bbs-golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Series []struct {
			Name string     `json:"name"`
			Bars []core.Bar `json:"bars"`
		} `json:"series"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	var bars []core.Bar
	for _, s := range g.Series {
		if s.Name == "aaoi" {
			bars = s.Bars
		}
	}
	// The engine's 1 Hz judge runs on the REAL wall clock: shift the day a week
	// back (2026-10-07, EDT too) so every bar is long closed.
	const week = 7 * 86_400_000
	for i := range bars {
		bars[i].T -= week
	}
	et, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tz database:", err)
	}
	breakTs := time.Date(2026, 10, 7, 10, 40, 0, 0, et).UnixMilli()
	var clock atomic.Int64
	clock.Store(bars[0].T - 60_000)
	eng := engine.New(engine.Options{TickHz: 20, Now: func() time.Time { return time.UnixMilli(clock.Load()) }, Bbs: true})
	clock.Store(breakTs + 63_000)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()
	scn := scan.New(eng)
	go func() { _ = scn.Run(ctx) }()
	h := newSeedHarness()
	seedScans(ctx, bbsSeedKey, bbsScans(), h.getMeta, h.setMeta, nil, h.save, scn.Upsert)
	scn.List()
	for _, b := range bars {
		if b.T > breakTs {
			break
		}
		eng.Apply(core.Agg{Sym: "AAOI", Open: b.O, High: b.H, Low: b.L, Close: b.C, Vol: b.V, StartMs: b.T, EndMs: b.T + 1000})
	}
	var got []core.AlertMsg
	deadline := time.After(2 * time.Second)
loop:
	for {
		select {
		case a := <-scn.Alerts():
			if a.Sym == "AAOI" {
				got = append(got, a)
			}
		case <-deadline:
			break loop
		}
	}
	if len(got) != 1 || got[0].ScanID != bbsScanID || got[0].Sound != "bounce" || got[0].Kind != "enter" {
		t.Fatalf("alerts %+v", got)
	}
	if v, ok := got[0].Row[core.FieldBbsSupport].(float64); !ok || v != 115.85 {
		t.Fatalf("row %+v", got[0].Row)
	}
}
