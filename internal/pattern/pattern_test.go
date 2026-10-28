package pattern

import (
	"math"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// --- fixtures ---------------------------------------------------------------

// build turns parallel close/volume slices into ascending daily bars with
// sequential dates. wick is the ± fraction of the close used for the high and
// low; the fixtures pass 0 (H=L=C) wherever an exact range or retrace is being
// asserted, so the arithmetic in the comments is the arithmetic in the test.
func build(closes []float64, vols []int64, wick float64) []core.DailyBar {
	out := make([]core.DailyBar, len(closes))
	d := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	for i, c := range closes {
		out[i] = core.DailyBar{
			Date: d.AddDate(0, 0, i).Format("2006-01-02"),
			O:    c,
			H:    c * (1 + wick),
			L:    c * (1 - wick),
			C:    c,
			V:    vols[i],
		}
	}
	return out
}

// ramp returns n closes moving linearly from a to b (mirrors trend_test.ramp).
func ramp(a, b float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = a + (b-a)*float64(i)/float64(n-1)
	}
	return out
}

func rep(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func repV(v int64, n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// textbook is the canonical setup: 22 sessions ramping 10 → 20 on a million
// shares a day (the pole), then 8 sessions coiling between 19.20 and 20.20 on
// 40% of that volume (the base).
func textbook() []core.DailyBar {
	closes := append(ramp(10, 20, 22),
		19.60, 20.20, 19.40, 19.90, 19.20, 20.10, 19.70, 19.95)
	return build(closes, append(repV(1_000_000, 22), repV(400_000, 8)...), 0)
}

// flagWith builds a pole from 10.00 up to poleTop over 22 sessions, then an
// 8-bar base oscillating between baseHigh and baseLow (starting AT baseHigh, so
// the base-starts-at-its-high gate never interferes) on baseVol against the
// pole's million. It lets a test dial exactly one ingredient at a time.
func flagWith(poleTop, baseHigh, baseLow float64, baseVol int64) []core.DailyBar {
	mid := (baseHigh + baseLow) / 2
	closes := append(ramp(10, poleTop, 22),
		baseHigh, baseLow, baseHigh, baseLow, baseHigh, baseLow, baseHigh, mid)
	return build(closes, append(repV(1_000_000, 22), repV(baseVol, 8)...), 0)
}

// allFixtures is every constructed series in this file, for the invariants that
// must hold no matter what goes in.
func allFixtures() map[string][]core.DailyBar {
	return map[string][]core.DailyBar{
		"textbook":     textbook(),
		"deepRetrace":  deepRetrace(),
		"looseRange":   looseRange(),
		"volExpanding": volExpanding(),
		"shortBase":    shortBase(),
		"chop":         chop(),
		"fastAdvance":  fastAdvance(),
		"slowAdvance":  slowAdvance(),
		"downtrend":    build(ramp(20, 10, 40), repV(1_000_000, 40), 0.005),
		"tooFewBars":   build(ramp(10, 20, 19), repV(1_000_000, 19), 0),
		"allZero":      build(rep(0, 30), repV(1_000, 30), 0),
		"negative":     build(rep(-5, 30), repV(1_000, 30), 0),
		"nil":          nil,
	}
}

// deepRetrace: the same 100% pole, but the base sits ~55% of the way back down
// the advance. Its own range is tight (6.7%) and its volume dries up, so the
// retrace gate is the ONLY thing standing between this and a "setup".
func deepRetrace() []core.DailyBar {
	closes := append(ramp(10, 20, 22),
		15.0, 14.6, 15.4, 14.8, 15.2, 14.5, 15.5, 15.0)
	return build(closes, append(repV(1_000_000, 22), repV(400_000, 8)...), 0)
}

// looseRange: the same pole, but the base slams 18.00 ↔ 21.00 every session.
// It holds the advance easily (retrace ~27%) — it simply never coils.
func looseRange() []core.DailyBar {
	closes := append(ramp(10, 20, 22),
		21.0, 18.0, 21.0, 18.0, 21.0, 18.0, 21.0, 18.0)
	return build(closes, append(repV(1_000_000, 22), repV(400_000, 8)...), 0)
}

// volExpanding: textbook geometry, but volume TRIPLES in the base. That is
// distribution into the consolidation, not supply exhaustion.
func volExpanding() []core.DailyBar {
	closes := append(ramp(10, 20, 22),
		19.60, 20.20, 19.40, 19.90, 19.20, 20.10, 19.70, 19.95)
	return build(closes, append(repV(1_000_000, 22), repV(3_000_000, 8)...), 0)
}

// shortBase: a 33% one-day spike to 24.00, then exactly two quiet sessions.
// Two bars are not a base, and the shortest window that IS long enough still
// has to carry the spike bar.
func shortBase() []core.DailyBar {
	closes := append(ramp(10, 18, 27), 24.0, 20.0, 20.1)
	return build(closes, append(repV(1_000_000, 27), repV(400_000, 3)...), 0)
}

// fastAdvance and slowAdvance are UNINTERRUPTED climbs on realistic 2% wicks:
// 10.00 → 20.00 (~2.3%/session) and 10.00 → 12.50 (~0.8%/session) over 30
// sessions. Neither pauses anywhere, at any point, so neither contains a base —
// but every other gate is happy with their trailing bars, which span well under
// maxRangePct, start inside the base-start band, and have given back almost
// nothing of a qualifying advance.
//
// The slow one is the case that shows this is not a base-length problem: at
// ~0.8%/session even a 15-bar window fits inside baseStartTolPct, so no value
// of minBaseBars rejects it. Only a gate on the base's FAR end can.
func fastAdvance() []core.DailyBar {
	return build(ramp(10, 20, 30), repV(1_000_000, 30), 0.02)
}

func slowAdvance() []core.DailyBar {
	return build(ramp(10, 12.5, 30), repV(1_000_000, 30), 0.02)
}

// chop: a 10% oscillation with no net advance that happens to end quiet. This
// is the shape a naive "tight range" detector fires on all day long; there is
// no pole here, so there is no flag.
func chop() []core.DailyBar {
	var closes []float64
	for i := 0; i < 13; i++ {
		closes = append(closes, 10.0, 11.0)
	}
	closes = append(closes, 10.95, 11.00, 10.90, 10.98)
	return build(closes, repV(1_000_000, len(closes)), 0)
}

// --- the happy path ---------------------------------------------------------

func TestAnalyzeTextbookFlag(t *testing.T) {
	bars := textbook()
	ref, ok := Analyze(bars)
	if !ok {
		t.Fatal("the textbook pole-and-base must classify")
	}
	if ref.State != 1 {
		t.Fatalf("a valid base is State 1, got %v (%+v)", ref.State, ref)
	}
	if ref.Score < 90 {
		t.Fatalf("strong pole, 5%% base, 10%% retrace, half volume should score high: %+v", ref)
	}
	// The base is the last 10 bars: the 8 coiling sessions plus the two pole
	// bars that already traded within baseStartTolPct of the base high.
	if ref.Bars != 10 {
		t.Fatalf("base length: want 10, got %d (%+v)", ref.Bars, ref)
	}
	if ref.BaseHigh != 20.20 {
		t.Fatalf("BaseHigh must be the base's high 20.20 for the live breakout test, got %v", ref.BaseHigh)
	}
	// Ingredients, not just the score: pole 10 → 20.20, range 1.00/19.70,
	// retrace 1.00/10.20, volume (2×1M + 8×0.4M)/10 against the pole's 1M.
	if math.Abs(ref.PoleGain-102) > 0.5 {
		t.Fatalf("PoleGain: want ~102, got %v", ref.PoleGain)
	}
	if math.Abs(ref.RangePct-5.076) > 0.01 {
		t.Fatalf("RangePct: want ~5.076, got %v", ref.RangePct)
	}
	if math.Abs(ref.Retrace-9.804) > 0.01 {
		t.Fatalf("Retrace: want ~9.804, got %v", ref.Retrace)
	}
	if math.Abs(ref.VolDry-0.52) > 1e-9 {
		t.Fatalf("VolDry: want 0.52, got %v", ref.VolDry)
	}
	if ref.AsOf != bars[len(bars)-1].Date {
		t.Fatalf("AsOf must be the newest bar's own date %q, got %q", bars[len(bars)-1].Date, ref.AsOf)
	}
}

// --- rejections, each for its own reason ------------------------------------

func TestAnalyzeRejectsDeepRetrace(t *testing.T) {
	bars := deepRetrace()
	if ref, ok := Analyze(bars); ok {
		t.Fatalf("a base 55%% back down the pole is a failed move, not a flag: %+v", ref)
	}
	// Pin the reason: at the natural 8-bar base everything else is comfortably
	// inside its gate, so only the retrace can be doing the rejecting.
	ref, w := evaluate(bars, 8)
	if w != whyDeepRetrace {
		t.Fatalf("want %v, got %v (%+v)", whyDeepRetrace, w, ref)
	}
	if math.Abs(ref.Retrace-55) > 0.01 {
		t.Fatalf("Retrace: want ~55 (20.00 → 14.50 of a 10.00 advance), got %v", ref.Retrace)
	}
	if ref.Retrace <= maxRetracePct {
		t.Fatalf("Retrace %v should exceed maxRetracePct %v", ref.Retrace, maxRetracePct)
	}
	if ref.RangePct > maxRangePct {
		t.Fatalf("the base itself is tight (%.2f%%) — the range gate must NOT be what rejected it", ref.RangePct)
	}
	if ref.PoleGain < minPoleGainPct || ref.VolDry > maxVolDryUp {
		t.Fatalf("pole and volume must both be fine here: %+v", ref)
	}
}

func TestAnalyzeRejectsLooseRange(t *testing.T) {
	bars := looseRange()
	if ref, ok := Analyze(bars); ok {
		t.Fatalf("an 18-21 swing range is not a coil: %+v", ref)
	}
	ref, w := evaluate(bars, 8)
	if w != whyLooseRange {
		t.Fatalf("want %v, got %v (%+v)", whyLooseRange, w, ref)
	}
	if math.Abs(ref.RangePct-15.385) > 0.01 {
		t.Fatalf("RangePct: want ~15.385 (3.00/19.50), got %v", ref.RangePct)
	}
	if ref.RangePct <= maxRangePct {
		t.Fatalf("RangePct %v should exceed maxRangePct %v", ref.RangePct, maxRangePct)
	}
	if ref.Retrace > maxRetracePct {
		t.Fatalf("it holds the advance (%.2f%% retrace) — the retrace gate must NOT be what rejected it", ref.Retrace)
	}
}

func TestAnalyzeRejectsVolumeExpansion(t *testing.T) {
	bars := volExpanding()
	if ref, ok := Analyze(bars); ok {
		t.Fatalf("tripling volume inside the base is distribution: %+v", ref)
	}
	ref, w := evaluate(bars, 10)
	if w != whyVolExpanding {
		t.Fatalf("want %v, got %v (%+v)", whyVolExpanding, w, ref)
	}
	// (2×1M + 8×3M)/10 = 2.6M against the pole's 1M.
	if math.Abs(ref.VolDry-2.6) > 1e-9 {
		t.Fatalf("VolDry: want 2.6, got %v", ref.VolDry)
	}
	if ref.VolDry <= maxVolDryUp {
		t.Fatalf("VolDry %v should exceed maxVolDryUp %v", ref.VolDry, maxVolDryUp)
	}
	// Same bars as the textbook fixture: every other ingredient is textbook.
	if ref.Retrace > maxRetracePct || ref.RangePct > maxRangePct || ref.PoleGain < minPoleGainPct {
		t.Fatalf("only the volume should be wrong here: %+v", ref)
	}
}

func TestAnalyzeRejectsShortBase(t *testing.T) {
	// The gate itself: two quiet sessions are noise.
	bars := textbook()
	if ref, w := evaluate(bars, 2); w != whyBaseTooShort {
		t.Fatalf("a 2-bar base must be refused outright, got %v (%+v)", w, ref)
	}
	if ref, w := evaluate(bars, minBaseBars-1); w != whyBaseTooShort {
		t.Fatalf("minBaseBars-1 must be refused, got %v (%+v)", w, ref)
	}
	// End to end: a spike followed by exactly two quiet sessions. Analyze never
	// even considers the 2-bar window, and the 3-bar one still carries the
	// spike, so nothing here is a base.
	if ref, ok := Analyze(shortBase()); ok {
		t.Fatalf("spike + two quiet days is not a consolidation: %+v", ref)
	}
}

func TestAnalyzeRejectsNoPole(t *testing.T) {
	bars := chop()
	if ref, ok := Analyze(bars); ok {
		t.Fatalf("a 10%% chop that ends quiet has no pole to flag: %+v", ref)
	}
	ref, w := evaluate(bars, 4)
	if w != whyNoPole {
		t.Fatalf("want %v, got %v (%+v)", whyNoPole, w, ref)
	}
	if math.Abs(ref.PoleGain-10) > 0.01 {
		t.Fatalf("PoleGain: want ~10 (10.00 → 11.00), got %v", ref.PoleGain)
	}
	if ref.PoleGain >= minPoleGainPct {
		t.Fatalf("PoleGain %v should fall short of minPoleGainPct %v", ref.PoleGain, minPoleGainPct)
	}
	// The base part of the shape is immaculate — that is the whole point.
	if ref.RangePct > maxRangePct {
		t.Fatalf("the trailing 4 bars ARE tight (%.2f%%); only the pole is missing", ref.RangePct)
	}
}

func TestAnalyzeInsufficientHistory(t *testing.T) {
	// 19 bars of a textbook shape: refuse. Null beats a guess.
	short := build(append(ramp(10, 20, 14), 19.60, 20.20, 19.40, 19.90, 19.20),
		repV(1_000_000, 19), 0)
	if len(short) != minBars-1 {
		t.Fatalf("fixture should be exactly minBars-1 bars, got %d", len(short))
	}
	if ref, ok := Analyze(short); ok {
		t.Fatalf("%d bars must be unknown: %+v", len(short), ref)
	}
	if _, w := evaluate(short, 8); w != whyShortHistory {
		t.Fatalf("want %v, got %v", whyShortHistory, w)
	}
	if _, ok := Analyze(nil); ok {
		t.Fatal("nil bars must be unknown")
	}
	// One more bar and the same shape classifies — proving it was the history
	// length that refused, not the geometry.
	if _, ok := Analyze(textbook()); !ok {
		t.Fatal("the full-length fixture must classify")
	}
}

// A daily downtrend must never read as a coiling flag: its trailing bars are
// tight in isolation, which is exactly the trap.
func TestAnalyzeDowntrendNeverCoils(t *testing.T) {
	bars := build(ramp(20, 10, 40), repV(1_000_000, 40), 0.005)
	if ref, ok := Analyze(bars); ok {
		t.Fatalf("a 40-session slide must not produce a setup: %+v", ref)
	}
	for k := 1; k <= maxBaseBars+5; k++ {
		ref, w := evaluate(bars, k)
		if w == whyOK || ref.State == 1 {
			t.Fatalf("k=%d: a downtrend candidate must never be admissible (%v, %+v)", k, w, ref)
		}
	}
	// The trailing 3 bars really are tight — so the rejection has to come from
	// the missing pole, not from the range.
	ref, w := evaluate(bars, 3)
	if w != whyNoPole {
		t.Fatalf("want %v, got %v (%+v)", whyNoPole, w, ref)
	}
	if ref.RangePct > maxRangePct {
		t.Fatalf("the trap only exists if the trailing range IS tight, got %.2f%%", ref.RangePct)
	}
}

// A ticker that pushes up early and then stops trading entirely produces a
// long run of IDENTICAL bars: the engine's intraday forward-fill synthesizes
// flat hi=lo=close buckets with V=0 for every silent 5-minute window (and a
// halted shell's daily file does the same). That run is the PERFECT counterfeit
// of the setup this package hunts — zero range, zero retrace, and no volume at
// all riding the neutral-VolDry guard — but it is fabricated quiet, not a coil:
// nothing traded, so nothing was measured. An all-identical-price base must be
// rejected outright, under both presets.
func TestAnalyzeRejectsForwardFilledFlatBase(t *testing.T) {
	// 6 real buckets ramping 10.00 → 12.50 on a million shares (a 25% pole that
	// clears BOTH presets' pole gates), then 25 fabricated buckets: H=L=C=12.50,
	// V=0 — exactly what engine.SessionBars yields for a gap-up-then-dead name.
	closes := append(ramp(10, 12.5, 6), rep(12.5, 25)...)
	vols := append(repV(1_000_000, 6), repV(0, 25)...)
	bars := build(closes, vols, 0)

	if ref, ok := AnalyzeWith(IntradayParams(), bars); ok {
		t.Fatalf("an all-flat zero-volume base is fabricated quiet, not a coil: %+v", ref)
	}
	if ref, ok := Analyze(bars); ok {
		t.Fatalf("the daily preset must reject the same fabrication: %+v", ref)
	}
	// Pin the reason at a base length that is entirely forward-filled, and prove
	// the flat gate is the ONLY thing standing: every other gate admits it.
	ref, w := evaluateWith(IntradayParams(), bars, 8)
	if w != whyFlatBase {
		t.Fatalf("want %v, got %v (%+v)", whyFlatBase, w, ref)
	}
	if ref.BaseHigh != 12.5 {
		t.Fatalf("the rejected candidate should still report its base high: %+v", ref)
	}
	// And no candidate length sneaks through — the flat run is longer than
	// maxBaseBars, so every admissible window is fully fabricated.
	for k := minBaseBars; k <= maxBaseBars; k++ {
		if ref, w := evaluateWith(IntradayParams(), bars, k); w == whyOK {
			t.Fatalf("k=%d admitted a fully forward-filled base (%+v)", k, ref)
		}
	}
	// The counterfeit's signature is IDENTICAL prices, not missing volume: a
	// base with real price movement and no volume stays admissible (the
	// neutral-VolDry contract of TestVolDryTreatsAMissingReadingAsNeutral).
	moving := append(ramp(10, 20, 22),
		19.60, 20.20, 19.40, 19.90, 19.20, 20.10, 19.70, 19.95)
	mv := build(moving, append(repV(1_000_000, 20), repV(0, 10)...), 0)
	if _, ok := Analyze(mv); !ok {
		t.Fatal("a zero-volume base that MOVED must stay admissible — the flat gate keys on price, not volume")
	}
}

// --- the base-start band ----------------------------------------------------

// The tightness gate alone is not enough to find where a base begins: a slow
// advance fits inside maxRangePct too. This is the case that motivated
// baseStartTolPct, so it gets its own test.
func TestBaseStartBandRejectsPoleBars(t *testing.T) {
	bars := textbook()
	ref, w := evaluate(bars, 14)
	if w != whyBaseNotAtHigh {
		t.Fatalf("a 14-bar window that is 6 bars of pole must be refused, got %v (%+v)", w, ref)
	}
	if ref.RangePct > maxRangePct {
		t.Fatalf("the point of this test is that the range gate ALONE would have "+
			"admitted it (%.2f%% <= %.2f%%)", ref.RangePct, maxRangePct)
	}
	// And the length Analyze does pick starts within the band.
	got, ok := Analyze(bars)
	if !ok || got.Bars != 10 {
		t.Fatalf("Analyze should settle on the 10-bar base: ok=%v %+v", ok, got)
	}
	start := len(bars) - got.Bars
	if bars[start].H < got.BaseHigh*(1-baseStartTolPct/100) {
		t.Fatalf("chosen base starts %.2f%% under its own high, outside the %.1f%% band",
			(1-bars[start].H/got.BaseHigh)*100, baseStartTolPct)
	}
}

// A stock that is simply GOING UP is not consolidating, and it is the single
// biggest false positive available to this detector: an uninterrupted advance
// fits inside maxRangePct over its last few sessions exactly as well as a real
// coil does, gives back almost nothing (there has been no pause in which to
// give anything back), and clears the pole gate by a mile. Before the base's
// far end was gated, every one of these scored 57-87 and would have filled a
// `flagScore > 70` scan with the whole market's uptrends.
//
// The band alone cannot catch them: it is measured against the base's own high,
// which the advance is still busy making.
func TestAnalyzeUninterruptedAdvanceIsNotAFlag(t *testing.T) {
	cases := []struct {
		name string
		bars []core.DailyBar
		at   int // the base length that would otherwise have been admitted
	}{
		{"fast, ~2.3%/session", fastAdvance(), minBaseBars},
		{"slow, ~0.8%/session", slowAdvance(), 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if ref, ok := Analyze(tc.bars); ok {
				t.Fatalf("an advance that never pauses has no base at any length: %+v", ref)
			}
			// No candidate length is admissible — this is not a base-length
			// problem and cannot be tuned away with minBaseBars.
			for k := minBaseBars; k <= maxBaseBars; k++ {
				if ref, w := evaluate(tc.bars, k); w == whyOK {
					t.Fatalf("k=%d admitted an uninterrupted advance (%+v)", k, ref)
				}
			}
			// Pin the reason at the length that used to pass, and prove the
			// still-rising gate is the ONLY thing rejecting it: every other
			// measurement sits comfortably inside its own gate.
			ref, w := evaluate(tc.bars, tc.at)
			if w != whyBaseStillRising {
				t.Fatalf("k=%d: want %v, got %v (%+v)", tc.at, whyBaseStillRising, w, ref)
			}
			if ref.RangePct > maxRangePct {
				t.Fatalf("the trap only exists if the trailing range IS tight, got %.2f%%", ref.RangePct)
			}
			if ref.Retrace > maxRetracePct {
				t.Fatalf("an advance that never paused gives back nothing, got %.2f%%", ref.Retrace)
			}
			if ref.PoleGain < minPoleGainPct || ref.VolDry > maxVolDryUp {
				t.Fatalf("pole and volume must both be fine here: %+v", ref)
			}
			start := len(tc.bars) - tc.at
			if tc.bars[start].H < ref.BaseHigh*(1-baseStartTolPct/100) {
				t.Fatalf("the base-start band must ALSO have admitted it, or this "+
					"test is re-proving TestBaseStartBandRejectsPoleBars: %+v", ref)
			}
			// And the shape of the objection: the last bar IS the base's high.
			if tc.bars[len(tc.bars)-1].H != ref.BaseHigh {
				t.Fatalf("the final bar should be the one making the base high: %v vs %v",
					tc.bars[len(tc.bars)-1].H, ref.BaseHigh)
			}
		})
	}
	// The textbook base does the opposite — its high was made mid-base and the
	// last bar sits under it — so the gate must not touch it.
	tb := textbook()
	ref, ok := Analyze(tb)
	if !ok {
		t.Fatal("the textbook base must survive the still-rising gate")
	}
	if last := tb[len(tb)-1].H; !(last < ref.BaseHigh) {
		t.Fatalf("textbook's last bar %v should sit under its base high %v", last, ref.BaseHigh)
	}
}

// --- boundaries around each named threshold ---------------------------------

// The gates are all strict (>, <), so a value sitting exactly on a threshold is
// admitted. We probe just inside and just outside rather than exactly on the
// line: an exact-equality assertion would be testing IEEE-754 rounding of the
// fixture arithmetic, not the gate.
func TestThresholdBoundaries(t *testing.T) {
	cases := []struct {
		name string
		bars []core.DailyBar
		k    int
		want why
	}{
		// Retrace, against a 10.00 → 15.00 pole so a 40% giveback still leaves a
		// tight base (the two gates are genuinely independent here).
		{"retrace just inside", flagWith(15, 15, 13.05, 400_000), 8, whyOK},
		{"retrace just outside", flagWith(15, 15, 12.95, 400_000), 8, whyDeepRetrace},

		// Range: mid is 20.00, so the half-width is the percentage directly.
		{"range just inside", flagWith(21.5, 21.5, 18.6, 400_000), 8, whyOK},
		{"range just outside", flagWith(21.5, 21.5, 18.4, 400_000), 8, whyLooseRange},

		// Pole gain, 10.00 → poleTop.
		{"pole just inside", flagWith(12.5, 12.5, 12.0, 400_000), 8, whyOK},
		{"pole just outside", flagWith(11.8, 11.8, 11.4, 400_000), 8, whyNoPole},

		// Volume: base average against the pole's 1,000,000.
		{"voldry just inside", flagWith(20, 20, 19, 1_200_000), 8, whyOK},
		{"voldry just outside", flagWith(20, 20, 19, 1_300_000), 8, whyVolExpanding},

		// Base length.
		{"base length at the floor", textbook(), minBaseBars, whyOK},
		{"base length under the floor", textbook(), minBaseBars - 1, whyBaseTooShort},
		{"base length at the ceiling", textbook(), maxBaseBars, whyBaseNotAtHigh},
		{"base length over the ceiling", textbook(), maxBaseBars + 1, whyBaseTooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, w := evaluate(tc.bars, tc.k)
			if w != tc.want {
				t.Fatalf("want %v, got %v (%+v)", tc.want, w, ref)
			}
		})
	}
}

// The named thresholds bracket each other the way the package doc claims. A
// tuning edit that crosses one of these has broken an invariant, not a taste.
func TestThresholdsAreOrdered(t *testing.T) {
	if idealRetracePct >= maxRetracePct {
		t.Fatal("the retrace scoring anchor must sit inside its gate")
	}
	if idealRangePct >= maxRangePct {
		t.Fatal("the range scoring anchor must sit inside its gate")
	}
	if idealVolDry >= maxVolDryUp {
		t.Fatal("the volume scoring anchor must sit inside its gate")
	}
	if strongPoleGainPct <= minPoleGainPct {
		t.Fatal("the pole scoring anchor must sit above its gate")
	}
	if maxVolDryUp <= 1.0 {
		t.Fatal("the volume ceiling must sit ABOVE 1.0 — 1.0 exactly is a knife edge")
	}
	if minBaseBars >= maxBaseBars || minBars < poleMinBars+minBaseBars {
		t.Fatal("the base-length and history gates must leave a workable window")
	}
	if baseStartTolPct >= maxRangePct {
		t.Fatal("the base-start band must be tighter than the range gate or it does nothing")
	}
}

// --- scoring ----------------------------------------------------------------

func TestScoreWeighting(t *testing.T) {
	// The documented weighting is written out as LITERALS here on purpose. If
	// these referenced the consts they would be tautologies and any tuning edit
	// would sail straight through; spelling them out means changing the model
	// forces you to change the test that states it.
	const (
		wantRetrace = 40.0
		wantRange   = 25.0
		wantVolDry  = 20.0
		wantPole    = 15.0
	)
	for _, w := range []struct {
		name      string
		got, want float64
	}{
		{"retrace", wRetrace, wantRetrace},
		{"range", wRange, wantRange},
		{"volDry", wVolDry, wantVolDry},
		{"pole", wPole, wantPole},
	} {
		if w.got != w.want {
			t.Fatalf("%s weight: want %v, got %v — the package doc's weighting "+
				"is the model; change both or neither", w.name, w.want, w.got)
		}
	}
	if wantRetrace+wantRange+wantVolDry+wantPole != 100 {
		t.Fatal("weights must sum to 100 or the score is not 0-100")
	}
	// The documented ordering IS the model: retrace dominates, pole matters
	// least. Swapping two weights must fail here.
	if !(wRetrace > wRange && wRange > wVolDry && wVolDry > wPole) {
		t.Fatalf("weight ordering must stay retrace > range > volDry > pole, got %v %v %v %v",
			wRetrace, wRange, wVolDry, wPole)
	}

	ideal := core.FlagRef{
		Retrace:  idealRetracePct,
		RangePct: idealRangePct,
		VolDry:   idealVolDry,
		PoleGain: strongPoleGainPct,
	}
	if got := score(ideal); math.Abs(got-100) > 1e-9 {
		t.Fatalf("every ingredient at its anchor should score 100, got %v", got)
	}
	worst := core.FlagRef{
		Retrace:  maxRetracePct,
		RangePct: maxRangePct,
		VolDry:   maxVolDryUp,
		PoleGain: minPoleGainPct,
	}
	if got := score(worst); math.Abs(got) > 1e-9 {
		t.Fatalf("every ingredient at its gate should score 0, got %v", got)
	}

	// Degrading exactly one ingredient from anchor to gate must cost exactly
	// that ingredient's weight — this is what "the weighting is explicit" means.
	degrade := []struct {
		name string
		ref  core.FlagRef
		cost float64
	}{
		{"retrace", core.FlagRef{Retrace: maxRetracePct, RangePct: idealRangePct, VolDry: idealVolDry, PoleGain: strongPoleGainPct}, wantRetrace},
		{"range", core.FlagRef{Retrace: idealRetracePct, RangePct: maxRangePct, VolDry: idealVolDry, PoleGain: strongPoleGainPct}, wantRange},
		{"volDry", core.FlagRef{Retrace: idealRetracePct, RangePct: idealRangePct, VolDry: maxVolDryUp, PoleGain: strongPoleGainPct}, wantVolDry},
		{"pole", core.FlagRef{Retrace: idealRetracePct, RangePct: idealRangePct, VolDry: idealVolDry, PoleGain: minPoleGainPct}, wantPole},
	}
	for _, d := range degrade {
		if got := score(d.ref); math.Abs(got-(100-d.cost)) > 1e-9 {
			t.Fatalf("%s at its gate should cost exactly %v: want %v, got %v",
				d.name, d.cost, 100-d.cost, got)
		}
	}

	// Half-way on every ingredient lands half-way overall (the map is linear).
	half := core.FlagRef{
		Retrace:  (idealRetracePct + maxRetracePct) / 2,
		RangePct: (idealRangePct + maxRangePct) / 2,
		VolDry:   (idealVolDry + maxVolDryUp) / 2,
		PoleGain: (strongPoleGainPct + minPoleGainPct) / 2,
	}
	if got := score(half); math.Abs(got-50) > 1e-9 {
		t.Fatalf("midpoints should score 50, got %v", got)
	}
}

// VolDry is a RATIO, and a ratio with a missing side is not a reading. The
// pole side has always been guarded; the base side is the one that bites,
// because 0/poleVol is a perfectly finite 0.0 — the best possible value — so a
// base that printed no volume at all (a halted shell, a delisting, a gap in the
// grouped-daily feed) collected full marks for "supply exhausted" purely by not
// having traded. Both sides must read 1.0: unknown, neutral.
func TestVolDryTreatsAMissingReadingAsNeutral(t *testing.T) {
	// Textbook geometry, but every bar the base can reach carries no volume.
	// The base Analyze settles on is 10 bars, so the zeroes have to start at 20
	// or baseVol stays positive and nothing here is being tested.
	closes := append(ramp(10, 20, 22),
		19.60, 20.20, 19.40, 19.90, 19.20, 20.10, 19.70, 19.95)

	for _, tc := range []struct {
		name string
		vol  int64
	}{
		{"no volume at all", 0},
		{"garbage negative volume", -5_000_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bars := build(closes, append(repV(1_000_000, 20), repV(tc.vol, 10)...), 0)
			ref, ok := Analyze(bars)
			if !ok {
				t.Fatalf("the geometry is textbook — only the volume is missing: %+v", ref)
			}
			if ref.VolDry != 1.0 {
				t.Fatalf("a missing volume reading must be neutral 1.0, got %v", ref.VolDry)
			}
			// The point of the guard: it must not be scored as the BEST reading.
			perfect := ref
			perfect.VolDry = 0
			if score(ref) >= score(perfect) {
				t.Fatalf("a base that never traded scored %v, as good as a base that "+
					"traded on a tenth of the pole's volume (%v)", score(ref), score(perfect))
			}
			// And it must still be strictly worse than the real textbook read.
			tb, _ := Analyze(textbook())
			if !(ref.Score < tb.Score) {
				t.Fatalf("unknown volume (%v) must score under the textbook's measured "+
					"0.52 (%v)", ref.Score, tb.Score)
			}
		})
	}
}

func TestQualityClampsAndReverses(t *testing.T) {
	cases := []struct {
		name           string
		v, best, worst float64
		want           float64
		wantNaN        bool
	}{
		{name: "at best", v: 5, best: 5, worst: 15, want: 1},
		{name: "at worst", v: 15, best: 5, worst: 15, want: 0},
		{name: "midpoint", v: 10, best: 5, worst: 15, want: 0.5},
		{name: "past best clamps to 1", v: -3, best: 5, worst: 15, want: 1},
		{name: "past worst clamps to 0", v: 99, best: 5, worst: 15, want: 0},
		{name: "higher-is-better at best", v: 60, best: 60, worst: 20, want: 1},
		{name: "higher-is-better midpoint", v: 40, best: 60, worst: 20, want: 0.5},
		{name: "higher-is-better past worst", v: 0, best: 60, worst: 20, want: 0},
		{name: "degenerate anchors", v: 7, best: 5, worst: 5, want: 0},
		{name: "NaN in, NaN out", v: math.NaN(), best: 5, worst: 15, wantNaN: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := quality(tc.v, tc.best, tc.worst)
			if tc.wantNaN {
				if !math.IsNaN(got) {
					t.Fatalf("want NaN (evaluate catches it downstream), got %v", got)
				}
				return
			}
			if math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
		})
	}
}

// --- invariants that must hold for every input ------------------------------

// State 2 = breakout is the live scanner overlay's call, comparing a tick's
// price against BaseHigh. Nothing in this package may ever produce it.
func TestNeverReturnsBreakoutState(t *testing.T) {
	for name, bars := range allFixtures() {
		if ref, _ := Analyze(bars); ref.State != 0 && ref.State != 1 {
			t.Fatalf("%s: Analyze produced State %v; only 0 and 1 may come from this package", name, ref.State)
		}
		for k := 0; k <= maxBaseBars+5; k++ {
			ref, w := evaluate(bars, k)
			if ref.State != 0 && ref.State != 1 {
				t.Fatalf("%s k=%d: evaluate produced State %v (%v)", name, k, ref.State, w)
			}
			if w != whyOK && ref.State != 0 {
				t.Fatalf("%s k=%d: a rejected candidate must stay State 0, got %v (%v)", name, k, ref.State, w)
			}
		}
	}
}

func TestAnalyzeGarbageDefensive(t *testing.T) {
	for _, name := range []string{"allZero", "negative"} {
		bars := allFixtures()[name]
		if ref, ok := Analyze(bars); ok {
			t.Fatalf("%s prices must be rejected, not divided by: %+v", name, ref)
		}
		if _, w := evaluate(bars, 8); w != whyBadData {
			t.Fatalf("%s: want %v, got %v", name, whyBadData, w)
		}
	}
	// A NaN bar must not leak a NaN score or an infinity into a "setup".
	nan := textbook()
	nan[25].H, nan[25].L, nan[25].C = math.NaN(), math.NaN(), math.NaN()
	if ref, ok := Analyze(nan); ok {
		for field, v := range map[string]float64{
			"Score": ref.Score, "PoleGain": ref.PoleGain, "RangePct": ref.RangePct,
			"Retrace": ref.Retrace, "VolDry": ref.VolDry, "BaseHigh": ref.BaseHigh,
		} {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Fatalf("%s came out %v — no infinities or NaNs may escape: %+v", field, v, ref)
			}
		}
	}
	// An all-NaN series must simply be unknown.
	allNaN := textbook()
	for i := range allNaN {
		allNaN[i].H, allNaN[i].L, allNaN[i].C = math.NaN(), math.NaN(), math.NaN()
	}
	if ref, ok := Analyze(allNaN); ok {
		t.Fatalf("an all-NaN series must be unknown: %+v", ref)
	}
}

// Whatever the geometry, a returned score has to be a usable 0-100 number: the
// scan layer sorts and filters on it.
func TestScoreStaysInRange(t *testing.T) {
	for name, bars := range allFixtures() {
		ref, ok := Analyze(bars)
		if !ok {
			continue
		}
		if ref.Score < 0 || ref.Score > 100 || math.IsNaN(ref.Score) {
			t.Fatalf("%s: score %v outside 0-100", name, ref.Score)
		}
		if ref.Bars < minBaseBars || ref.Bars > maxBaseBars {
			t.Fatalf("%s: reported base length %d outside [%d,%d]", name, ref.Bars, minBaseBars, maxBaseBars)
		}
		if ref.BaseHigh <= 0 || ref.AsOf == "" {
			t.Fatalf("%s: an admitted setup needs a usable BaseHigh and AsOf: %+v", name, ref)
		}
	}
}
