package pattern

import (
	"math"
	"reflect"
	"testing"
)

// DailyParams IS the const block. Field-by-field so that a knob wired to the
// wrong const fails on its own line, then two reflection nets underneath: the
// field count catches Params growing without this test growing with it, and
// the no-zero-field sweep catches a knob that was added to the struct but
// never wired into the preset — a zero gate is an unwired gate, and an unwired
// gate silently freezes that threshold for every caller of AnalyzeWith.
func TestDailyParamsMatchConsts(t *testing.T) {
	p := DailyParams()
	if p.MinBars != minBars {
		t.Fatalf("MinBars: want %v, got %v", minBars, p.MinBars)
	}
	if p.MinBaseBars != minBaseBars {
		t.Fatalf("MinBaseBars: want %v, got %v", minBaseBars, p.MinBaseBars)
	}
	if p.MaxBaseBars != maxBaseBars {
		t.Fatalf("MaxBaseBars: want %v, got %v", maxBaseBars, p.MaxBaseBars)
	}
	if p.PoleMinBars != poleMinBars {
		t.Fatalf("PoleMinBars: want %v, got %v", poleMinBars, p.PoleMinBars)
	}
	if p.PoleMaxBars != poleMaxBars {
		t.Fatalf("PoleMaxBars: want %v, got %v", poleMaxBars, p.PoleMaxBars)
	}
	if p.MinPoleGainPct != minPoleGainPct {
		t.Fatalf("MinPoleGainPct: want %v, got %v", minPoleGainPct, p.MinPoleGainPct)
	}
	if p.MaxRetracePct != maxRetracePct {
		t.Fatalf("MaxRetracePct: want %v, got %v", maxRetracePct, p.MaxRetracePct)
	}
	if p.MaxRangePct != maxRangePct {
		t.Fatalf("MaxRangePct: want %v, got %v", maxRangePct, p.MaxRangePct)
	}
	if p.MaxVolDryUp != maxVolDryUp {
		t.Fatalf("MaxVolDryUp: want %v, got %v", maxVolDryUp, p.MaxVolDryUp)
	}
	if p.BaseStartTolPct != baseStartTolPct {
		t.Fatalf("BaseStartTolPct: want %v, got %v", baseStartTolPct, p.BaseStartTolPct)
	}

	typ := reflect.TypeOf(p)
	if typ.NumField() != 10 {
		t.Fatalf("Params has %d fields but this test pins 10 — wire the new knob "+
			"into DailyParams and assert it above", typ.NumField())
	}
	v := reflect.ValueOf(p)
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).IsZero() {
			t.Fatalf("DailyParams().%s is zero — an unwired knob, not a tuning", typ.Field(i).Name)
		}
	}
}

// A 12% pole sits between the two presets' MinPoleGainPct gates (10 < 12 < 20),
// so the SAME bars must flip verdict purely on the Params argument — proving
// the plumbing is real and AnalyzeWith is not quietly re-reading the consts.
func TestAnalyzeWithParamsChangesBehavior(t *testing.T) {
	// Pole 10.00 → 11.20 over 22 bars, then an 8-bar coil 10.90-11.20 on 40%
	// of the pole's volume: textbook in every ingredient EXCEPT the pole.
	bars := flagWith(11.2, 11.2, 10.9, 400_000)

	if ref, ok := AnalyzeWith(DailyParams(), bars); ok {
		t.Fatalf("a 12%% pole must fall short of the daily 20%% gate: %+v", ref)
	}
	ref, ok := AnalyzeWith(IntradayParams(), bars)
	if !ok {
		t.Fatal("a 12% pole must clear the intraday 10% gate")
	}
	if ref.State != 1 {
		t.Fatalf("an admitted intraday setup is State 1, got %v (%+v)", ref.State, ref)
	}
	if math.Abs(ref.PoleGain-12) > 0.01 {
		t.Fatalf("PoleGain: want ~12, got %v", ref.PoleGain)
	}
	if ref.BaseHigh != 11.2 {
		t.Fatalf("BaseHigh must be the base's high 11.20 for the live breakout test, got %v", ref.BaseHigh)
	}
	if ref.Score <= 0 || ref.Score > 100 || math.IsNaN(ref.Score) {
		t.Fatalf("score must stay a usable 0-100 number under intraday params: %v", ref.Score)
	}

	// Pin WHY daily rejected it: the pole gate alone, at a base length where
	// every other ingredient sits comfortably inside its own gate.
	if r, w := evaluateWith(DailyParams(), bars, 8); w != whyNoPole {
		t.Fatalf("daily: want %v, got %v (%+v)", whyNoPole, w, r)
	}
	if r, w := evaluateWith(IntradayParams(), bars, 8); w != whyOK {
		t.Fatalf("intraday: want %v, got %v (%+v)", whyOK, w, r)
	}

	// And Analyze is AnalyzeWith(DailyParams(), ...) by definition — same
	// verdict AND same measurements, on both an accepted and a rejected shape.
	tb := textbook()
	aRef, aOK := Analyze(tb)
	wRef, wOK := AnalyzeWith(DailyParams(), tb)
	if aOK != wOK || aRef != wRef {
		t.Fatalf("Analyze must equal AnalyzeWith(DailyParams()): %v/%+v vs %v/%+v", aOK, aRef, wOK, wRef)
	}
	if _, ok := Analyze(bars); ok {
		t.Fatal("Analyze must reject the 12% pole exactly as AnalyzeWith(DailyParams()) does")
	}
}

// IntradayParams turns exactly one knob. Reflection walks every field so a
// future edit that quietly forks a second threshold — the bug this preset's
// doc comment promises away — fails here by name.
func TestIntradayParamsDiffersOnlyInMinPoleGain(t *testing.T) {
	d, i := DailyParams(), IntradayParams()
	dv, iv := reflect.ValueOf(d), reflect.ValueOf(i)
	typ := reflect.TypeOf(d)
	seen := false
	for f := 0; f < typ.NumField(); f++ {
		name := typ.Field(f).Name
		if name == "MinPoleGainPct" {
			seen = true
			continue
		}
		if !reflect.DeepEqual(dv.Field(f).Interface(), iv.Field(f).Interface()) {
			t.Fatalf("IntradayParams forks %s (%v vs %v); only MinPoleGainPct may differ",
				name, dv.Field(f).Interface(), iv.Field(f).Interface())
		}
	}
	if !seen {
		t.Fatal("Params no longer has a MinPoleGainPct field — update this test and the presets together")
	}
	// The one divergence, as literals on purpose (TestScoreWeighting's line):
	// changing either preset's pole gate must force a change here too.
	if d.MinPoleGainPct != 20.0 {
		t.Fatalf("daily MinPoleGainPct: want 20.0, got %v", d.MinPoleGainPct)
	}
	if i.MinPoleGainPct != 10.0 {
		t.Fatalf("intraday MinPoleGainPct: want 10.0, got %v", i.MinPoleGainPct)
	}
}
