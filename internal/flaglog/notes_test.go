package flaglog

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
)

// A plain power candle's note ends with the power-setup filter tag; a power
// setup is its own P-SETUP row with the plan and the same tag.
func TestOnSignalPowerSetupAndFilterTag(t *testing.T) {
	c := etMs(2026, 10, 27, 7, 6)
	r := New(newFakeRepo(), func() time.Time { return time.UnixMilli(c + 5*60_000) })
	base := core.SignalHit{Sym: "ABCD", BarMs: c, Price: 1.38, O: 1.2, C: 1.38, BodyPct: 15, BodyX: 7.5, VolX: 200, TopPct: 70, DollarVol: 276_000,
		PsFromOpen: 15, PsSlope: 1.9, PsGap: 20, PsEntry: 1.2336, PsStop: 1.1596, PsTarget: 1.4556}
	pw := base
	pw.Kind = dildopb.KindPower
	r.OnSignal(pw)
	ps := base
	ps.Kind = core.KindPowerSetup
	ps.PsSlope = 2.8
	r.OnSignal(ps)
	msg, _ := r.Day(context.Background(), "")
	if len(msg.Events) != 2 {
		t.Fatalf("events %+v", msg.Events)
	}
	for _, e := range msg.Events {
		switch e.Kind {
		case "power":
			if !strings.HasSuffix(e.Note, "A✓ slope✗") {
				t.Fatalf("power note %q", e.Note)
			}
		case "powerSetup":
			if e.Label != "P-SETUP" || !strings.HasPrefix(e.Note, "entry 1.23 stop 1.16 target 1.46; A✓ slope✓") {
				t.Fatalf("setup %+v", e)
			}
		default:
			t.Fatalf("kind %q", e.Kind)
		}
	}
}

// An A+ setup (base range <= 5 %) is labelled "P-SETUP A+" with the base first in the note;
// a wider or unknown base keeps the plain P-SETUP row.
func TestOnSignalPowerSetupAplus(t *testing.T) {
	r := New(newFakeRepo(), nil)
	for _, c := range []struct {
		rng          float64
		aplus        bool
		label, start string
	}{{3.84, true, "P-SETUP A+", "A+ tight base 3.8%; entry 1.23"}, {5.2, false, "P-SETUP", "entry 1.23"}, {0, false, "P-SETUP", "entry 1.23"}} {
		h := core.SignalHit{Sym: "ABCD", Kind: core.KindPowerSetup, PsEntry: 1.2336, PsStop: 1.1596, PsTarget: 1.4556, PsBaseRange: c.rng, PsAplus: c.aplus}
		label, note, ok := r.signalText(h)
		if !ok || label != c.label || !strings.HasPrefix(note, c.start) {
			t.Fatalf("%v: %q %q", c.rng, label, note)
		}
	}
}

func TestLatestPowerSetup(t *testing.T) {
	c := etMs(2026, 10, 27, 7, 6)
	r := New(newFakeRepo(), func() time.Time { return time.UnixMilli(c + 5*60_000) })
	if _, _, _, ok := r.LatestPowerSetup("ABCD"); ok {
		t.Fatal("none yet")
	}
	h := core.SignalHit{Sym: "ABCD", Kind: core.KindPowerSetup, BarMs: c, Price: 1.3, PsEntry: 1.2, PsStop: 1.13, PsTarget: 1.4, PsBaseRange: 3.84, PsAplus: true}
	r.OnSignal(h)
	if a, rng, bar, ok := r.LatestPowerSetup("ABCD"); !ok || !a || rng != 3.8 || bar != c {
		t.Fatalf("A+ %v %v %v %v", a, rng, bar, ok)
	}
	h.BarMs, h.PsAplus, h.PsBaseRange = c+60_000, false, 7
	r.OnSignal(h)
	if a, rng, _, ok := r.LatestPowerSetup("ABCD"); !ok || a || rng != 0 {
		t.Fatalf("latest is plain: %v %v", a, rng)
	}
	if _, _, _, ok := r.LatestPowerSetup("XYZ"); ok {
		t.Fatal("other symbol")
	}
}

func TestLatestVwapBull(t *testing.T) {
	c := etMs(2026, 10, 29, 10, 1)
	r := New(newFakeRepo(), func() time.Time { return time.UnixMilli(c + 5*60_000) })
	if _, ok := r.LatestVwapBull("MGM"); ok {
		t.Fatal("none yet")
	}
	r.OnSignal(core.SignalHit{Sym: "MGM", Kind: core.KindEmaVwapBull, BarMs: c, Price: 27.99, Ema: 27.9, Vwap: 27.8, Dist: 0.3})
	r.OnSignal(core.SignalHit{Sym: "MGM", Kind: core.KindEmaVwapBull, BarMs: c + 120_000, Price: 28.1, Ema: 28.0, Vwap: 27.9, Dist: 0.3})
	r.OnSignal(core.SignalHit{Sym: "MGM", Kind: core.KindEmaVwapBear, BarMs: c + 180_000, Price: 27.0})
	if bar, ok := r.LatestVwapBull("MGM"); !ok || bar != c+120_000 {
		t.Fatalf("latest bull %v %v", bar, ok)
	}
	if _, ok := r.LatestVwapBull("XYZ"); ok {
		t.Fatal("other symbol")
	}
}

func TestLatestVwapBear(t *testing.T) {
	c := etMs(2026, 10, 29, 10, 1)
	r := New(newFakeRepo(), func() time.Time { return time.UnixMilli(c + 5*60_000) })
	if _, ok := r.LatestVwapBear("MGM"); ok {
		t.Fatal("none yet")
	}
	r.OnSignal(core.SignalHit{Sym: "MGM", Kind: core.KindEmaVwapBear, BarMs: c, Price: 27.0})
	r.OnSignal(core.SignalHit{Sym: "MGM", Kind: core.KindEmaVwapBear, BarMs: c + 60_000, Price: 26.9})
	r.OnSignal(core.SignalHit{Sym: "MGM", Kind: core.KindEmaVwapBull, BarMs: c + 180_000, Price: 27.99, Ema: 27.9, Vwap: 27.8, Dist: 0.3})
	if bar, ok := r.LatestVwapBear("MGM"); !ok || bar != c+60_000 {
		t.Fatalf("latest bear %v %v", bar, ok)
	}
	if bar, ok := r.LatestVwapBull("MGM"); !ok || bar != c+180_000 {
		t.Fatalf("bull must be unchanged %v %v", bar, ok)
	}
	if _, ok := r.LatestVwapBear("XYZ"); ok {
		t.Fatal("other symbol")
	}
}
