package tape

import (
	"math"
	"testing"
)

// verdictConf mirrors engine.py's `round(min(conf, 0.95), 2)` -- the exact
// post-processing every tier's raw confidence formula goes through -- so
// tests can assert against the same rounded/capped value localVerdict itself
// produces, rather than the raw pre-rounding number.
func verdictConf(raw float64) float64 {
	if raw > verdictConfidenceCap {
		raw = verdictConfidenceCap
	}
	return math.Round(raw*100) / 100
}

func TestVerdictTier1BidAbsorptionBeatsLowerTiers(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	warmUp(e, "ABCD", ms) // median size = 1, so the absorption floor stays tiny
	st := e.syms["ABCD"]

	quoteSz(e, "ABCD", 99.99, 1000, 100.01, 1000, ms) // establish the inside quote
	floor := levelAbsorbFloorMult * e.medianSize(st)
	per := int64(floor)/3 + 1 // 3 refills together must clear the floor
	for i := 0; i < levelAbsorbRefills; i++ {
		trade(e, "ABCD", 99.99, per, ms) // sellers hit the bid
		quoteSz(e, "ABCD", 99.99, 300, 100.01, 1000, ms)
		quoteSz(e, "ABCD", 99.99, 1500, 100.01, 1000, ms) // refreshes at the same price
	}

	v := e.localVerdict(st)
	if v.Control != "buyers" {
		t.Fatalf("tier1 bid absorption should read buyers in control, got %+v", v)
	}
	if want := verdictConf(0.55 + 0.05*float64(st.bidRefills)); v.Confidence != want {
		t.Fatalf("tier1 confidence: got %v want %v (bidRefills=%d)", v.Confidence, want, st.bidRefills)
	}

	// Sanity: this setup is nothing but one-sided selling (all trades hit the
	// bid) -- tier5's own fallback formula, computed off the same 60s window,
	// would have read "sellers". Tier1 pre-empting it is the whole point of
	// the cascade: a level that keeps absorbing the selling is a buyers signal
	// even though the raw aggressor flow was 100% sell-side.
	b60, s60, _, _ := e.window(st, 60)
	if score := controlScore60(b60, s60); score >= -verdictFallbackScoreThresh {
		t.Fatalf("test setup should read heavy one-sided selling on its own; score=%v", score)
	}
}

func TestVerdictTier1AskAbsorptionBeatsLowerTiers(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	warmUp(e, "ABCD", ms)
	st := e.syms["ABCD"]

	quoteSz(e, "ABCD", 99.99, 1000, 100.01, 1000, ms)
	floor := levelAbsorbFloorMult * e.medianSize(st)
	per := int64(floor)/3 + 1
	for i := 0; i < levelAbsorbRefills; i++ {
		trade(e, "ABCD", 100.01, per, ms) // buyers lift the offer
		quoteSz(e, "ABCD", 99.99, 1000, 100.01, 300, ms)
		quoteSz(e, "ABCD", 99.99, 1000, 100.01, 1500, ms) // refreshes at the same price
	}

	v := e.localVerdict(st)
	if v.Control != "sellers" {
		t.Fatalf("tier1 ask absorption should read sellers in control, got %+v", v)
	}
	if want := verdictConf(0.55 + 0.05*float64(st.askRefills)); v.Confidence != want {
		t.Fatalf("tier1 confidence: got %v want %v (askRefills=%d)", v.Confidence, want, st.askRefills)
	}

	b60, s60, _, _ := e.window(st, 60)
	if score := controlScore60(b60, s60); score <= verdictFallbackScoreThresh {
		t.Fatalf("test setup should read heavy one-sided buying on its own; score=%v", score)
	}
}

func TestVerdictTier2BidPullBeatsFallback(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quoteSz(e, "ABCD", 1.82, 5000, 1.83, 2000, ms)
	for _, sz := range []int64{3800, 2600, 1500, 700, 300} { // size vanishing, no trades in between
		quoteSz(e, "ABCD", 1.82, sz, 1.83, 2000, ms)
	}
	st := e.syms["ABCD"]

	v := e.localVerdict(st)
	if v.Control != "sellers" {
		t.Fatalf("tier2 bid pull should read sellers in control, got %+v", v)
	}
	if v.Confidence != 0.5 {
		t.Fatalf("tier2 confidence should be exactly 0.5, got %v", v.Confidence)
	}

	// Sanity: no trades at all happened in this setup, so tier5's fallback
	// alone would have read "balanced" -- tier2 is what turns it into a
	// directional "sellers" read (support thinning under price).
	b60, s60, _, _ := e.window(st, 60)
	if score := controlScore60(b60, s60); score != 0 {
		t.Fatalf("test setup should have zero classified volume; score=%v", score)
	}
}

func TestVerdictTier3SweepBeatsDivergenceAndFallback(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quote(e, "ABCD", 10.00, 10.02, ms)
	// Heavy one-sided buying, pinned at the ask (flat price) -- enough volume
	// and flatness to ALSO satisfy tier4's divergence condition, proving tier3
	// wins even though tier4's own condition holds too.
	for i := 0; i < 20; i++ {
		trade(e, "ABCD", 10.02, 100, ms+int64(i))
	}
	st := e.syms["ABCD"]
	st.lastSweptDir = "buy" // as if detectSweep had just fired a buy_sweep

	v := e.localVerdict(st)
	if v.Control != "buyers" {
		t.Fatalf("tier3 sweep should read buyers in control, got %+v", v)
	}
	b60, s60, _, _ := e.window(st, 60)
	score := controlScore60(b60, s60)
	if want := verdictConf(0.5 + math.Abs(score)/2); v.Confidence != want {
		t.Fatalf("tier3 confidence: got %v want %v (score=%v)", v.Confidence, want, score)
	}

	// Concrete proof tier3 was pre-empting tier4: clear lastSweptDir (as if no
	// sweep had fired) and the SAME underlying flat/one-sided state now falls
	// through to tier4, which reads the OPPOSITE way (buying absorbed, price
	// stalling -> sellers).
	st.lastSweptDir = ""
	v2 := e.localVerdict(st)
	if v2.Control != "sellers" {
		t.Fatalf("tier4 divergence should take over and read sellers once tier3 is out of the way, got %+v", v2)
	}
	if v2.Confidence != 0.5 {
		t.Fatalf("tier4 confidence should be exactly 0.5, got %v", v2.Confidence)
	}
}

func TestVerdictTier4DivergenceBeatsFallback(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quote(e, "ABCD", 10.00, 10.02, ms)
	// Heavy one-sided buying pinned at the ask: price never actually moves --
	// the offer is absorbing the buying pressure.
	for i := 0; i < 20; i++ {
		trade(e, "ABCD", 10.02, 100, ms+int64(i))
	}
	st := e.syms["ABCD"]

	v := e.localVerdict(st)
	if v.Control != "sellers" {
		t.Fatalf("tier4 divergence (buying absorbed, price stalling) should read sellers, got %+v", v)
	}
	if v.Confidence != 0.5 {
		t.Fatalf("tier4 confidence should be exactly 0.5, got %v", v.Confidence)
	}

	// Sanity: tier5's own fallback formula, off the same score, would have
	// said "buyers" -- proving tier4 pre-empted it.
	b60, s60, _, _ := e.window(st, 60)
	if score := controlScore60(b60, s60); score <= verdictFallbackScoreThresh {
		t.Fatalf("test setup should read a strong buy score so tier5 would disagree; score=%v", score)
	}
}

func TestVerdictTier5FallbackBalancedWhenFlowIsTwoSided(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quote(e, "ABCD", 10.00, 10.02, ms)
	trade(e, "ABCD", 10.02, 100, ms) // buy
	trade(e, "ABCD", 10.00, 100, ms) // sell -- perfectly balanced
	st := e.syms["ABCD"]

	v := e.localVerdict(st)
	if v.Control != "balanced" {
		t.Fatalf("perfectly balanced flow should read balanced, got %+v", v)
	}
	if v.Confidence != 0.4 {
		t.Fatalf("balanced confidence should be exactly 0.4, got %v", v.Confidence)
	}
}

func TestVerdictTier5FallbackLeansWithScore(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quote(e, "ABCD", 10.00, 10.02, ms)
	trade(e, "ABCD", 10.02, 130, ms) // buy
	trade(e, "ABCD", 10.00, 100, ms) // sell -- mild imbalance, above the 0.12 lean threshold
	st := e.syms["ABCD"]

	v := e.localVerdict(st)
	if v.Control != "buyers" {
		t.Fatalf("mild buy imbalance should lean buyers, got %+v", v)
	}
	b60, s60, _, _ := e.window(st, 60)
	score := controlScore60(b60, s60)
	if want := verdictConf(0.35 + math.Abs(score)/2); v.Confidence != want {
		t.Fatalf("tier5 lean confidence: got %v want %v (score=%v)", v.Confidence, want, score)
	}
}

func TestVerdictConfidenceCapsAt095(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	warmUp(e, "ABCD", ms)
	st := e.syms["ABCD"]

	quoteSz(e, "ABCD", 99.99, 1000, 100.01, 1000, ms)
	floor := levelAbsorbFloorMult * e.medianSize(st)
	per := int64(floor)/3 + 1
	// Far more than levelAbsorbRefills -- 0.55 + 0.05*refills would exceed
	// 0.95 uncapped.
	for i := 0; i < 10; i++ {
		trade(e, "ABCD", 99.99, per, ms)
		quoteSz(e, "ABCD", 99.99, 300, 100.01, 1000, ms)
		quoteSz(e, "ABCD", 99.99, 1500, 100.01, 1000, ms)
	}

	v := e.localVerdict(st)
	if v.Control != "buyers" {
		t.Fatalf("expected level absorption to still read buyers, got %+v", v)
	}
	if v.Confidence != 0.95 {
		t.Fatalf("confidence should cap at 0.95 once 0.55+0.05*refills exceeds it (refills=%d), got %v", st.bidRefills, v.Confidence)
	}
}
