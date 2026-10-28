package tape

import (
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// fixed clock: all tests pivot around this instant.
var t0 = time.Date(2026, 7, 22, 9, 45, 0, 0, time.UTC)

func newTest(syms ...string) *Engine {
	e := New()
	e.now = func() time.Time { return t0 }
	if len(syms) == 0 {
		syms = []string{"ABCD"}
	}
	e.reconcile(syms)
	return e
}

func quote(e *Engine, sym string, bid, ask float64, atMs int64) {
	e.apply(core.TapeEvent{Quote: &core.Quote{Sym: sym, Bid: bid, Ask: ask, BidSz: 100, AskSz: 100, TS: atMs}})
}

func trade(e *Engine, sym string, price float64, size int64, atMs int64) {
	e.apply(core.TapeEvent{Trade: &core.Trade{Sym: sym, Price: price, Size: size, TS: atMs}})
}

// snap returns the current TapeStat for sym, "" TotalTrades/Sym zero value if
// sym isn't currently watched (matching e.syms[sym] being nil -> snapshot
// would nil-deref, so tests must only call this for a watched symbol).
func snap(e *Engine, sym string) core.TapeStat {
	return e.snapshot(e.syms[sym])
}

func TestQuoteRuleClassification(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quote(e, "ABCD", 4.50, 4.52, ms)

	trade(e, "ABCD", 4.52, 1000, ms) // at ask -> buy
	trade(e, "ABCD", 4.53, 500, ms)  // above ask -> buy
	trade(e, "ABCD", 4.50, 700, ms)  // at bid -> sell
	trade(e, "ABCD", 4.515, 200, ms) // above mid (4.51) -> buy

	st := snap(e, "ABCD")
	if !st.QuoteRule {
		t.Fatal("expected quote-rule mode")
	}
	if st.DeltaTotal != 1000+500+200-700 {
		t.Fatalf("delta: %d", st.DeltaTotal)
	}
	if st.TotalTrades != 4 {
		t.Fatalf("trades: %d", st.TotalTrades)
	}
}

func TestTickRuleFallbackWithoutQuotes(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	trade(e, "ABCD", 4.50, 100, ms) // first trade, no reference -> neutral
	trade(e, "ABCD", 4.51, 300, ms) // uptick -> buy
	trade(e, "ABCD", 4.49, 200, ms) // downtick -> sell

	st := snap(e, "ABCD")
	if st.QuoteRule {
		t.Fatal("no quotes -> tick-rule mode")
	}
	if st.DeltaTotal != 300-200 {
		t.Fatalf("delta: %d", st.DeltaTotal)
	}
}

func TestStaleQuoteFallsBackToTickRule(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quote(e, "ABCD", 4.50, 4.52, ms-quoteFreshMs-1000) // stale quote
	trade(e, "ABCD", 4.40, 100, ms)                    // would be "sell" by quote rule; tick rule -> neutral (first)
	trade(e, "ABCD", 4.45, 100, ms)                    // uptick -> buy

	st := snap(e, "ABCD")
	if st.DeltaTotal != 100 {
		t.Fatalf("stale quote must not classify; delta: %d", st.DeltaTotal)
	}
}

func TestWindowsSeparate1mAnd5m(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quote(e, "ABCD", 4.50, 4.52, ms)
	// 4 minutes ago: 2000 buys (in 5m window, outside 1m)
	trade(e, "ABCD", 4.52, 2000, ms-4*60_000)
	// 30s ago: 500 sells (in both windows)
	quote(e, "ABCD", 4.50, 4.52, ms-30_000)
	trade(e, "ABCD", 4.50, 500, ms-30_000)

	st := snap(e, "ABCD")
	if st.Delta1m != -500 {
		t.Fatalf("delta1m: %d", st.Delta1m)
	}
	if st.Delta5m != 2000-500 {
		t.Fatalf("delta5m: %d", st.Delta5m)
	}
	if st.BuyPct5m <= 50 || st.SellPct5m >= 50 {
		t.Fatalf("pcts: buy %.1f sell %.1f", st.BuyPct5m, st.SellPct5m)
	}
}

func TestBigPrintsCapturedAndCapped(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quote(e, "ABCD", 4.50, 4.52, ms)
	// establish a small average size first
	for i := 0; i < 50; i++ {
		trade(e, "ABCD", 4.52, 100, ms)
	}
	// 12 big prints (>20x avg) -> kept capped at printsKept, newest first
	for i := 0; i < 12; i++ {
		trade(e, "ABCD", 4.52, 50_000+int64(i), ms+int64(i))
	}
	st := snap(e, "ABCD")
	if len(st.Prints) != printsKept {
		t.Fatalf("prints kept: %d", len(st.Prints))
	}
	if st.Prints[0].Size != 50_011 {
		t.Fatalf("newest first: %+v", st.Prints[0])
	}
	if st.Prints[0].Side != "buy" {
		t.Fatalf("side: %s", st.Prints[0].Side)
	}
}

func TestNotionalThresholdCatchesLargeCapPrints(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quote(e, "ABCD", 500.00, 500.10, ms)
	trade(e, "ABCD", 500.10, 60, ms) // 60 shares but $30k notional -> notable
	st := snap(e, "ABCD")
	if len(st.Prints) != 1 {
		t.Fatalf("notional print missed: %+v", st.Prints)
	}
}

func TestControlScoreSign(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quote(e, "ABCD", 4.50, 4.52, ms)
	for i := 0; i < 20; i++ {
		trade(e, "ABCD", 4.52, 1000, ms) // heavy buying
	}
	trade(e, "ABCD", 4.50, 500, ms)
	st := snap(e, "ABCD")
	if st.Control <= 0.3 {
		t.Fatalf("heavy buying should read buyers-in-control, got %.2f", st.Control)
	}

	e2 := newTest()
	quote(e2, "ABCD", 4.50, 4.52, ms)
	for i := 0; i < 20; i++ {
		trade(e2, "ABCD", 4.50, 1000, ms)
	}
	if st2 := snap(e2, "ABCD"); st2.Control >= -0.3 {
		t.Fatalf("heavy selling should read sellers-in-control, got %.2f", st2.Control)
	}
}

func TestReconcileRemoveDropsSymbolAndIgnoresLateEvents(t *testing.T) {
	e := newTest("ABCD")
	ms := t0.UnixMilli()
	quote(e, "ABCD", 4.50, 4.52, ms)
	trade(e, "ABCD", 4.52, 5000, ms)

	e.reconcile([]string{"WXYZ"}) // switch entirely: drop ABCD, watch WXYZ

	if _, ok := e.syms["ABCD"]; ok {
		t.Fatal("ABCD should no longer be tracked")
	}
	stWxyz := snap(e, "WXYZ")
	if stWxyz.Sym != "WXYZ" || stWxyz.DeltaTotal != 0 || stWxyz.TotalTrades != 0 || len(stWxyz.Prints) != 0 {
		t.Fatalf("WXYZ should start fresh: %+v", stWxyz)
	}
	// events for the dropped symbol are silently ignored, not misapplied to WXYZ
	trade(e, "ABCD", 4.52, 1000, ms)
	if st := snap(e, "WXYZ"); st.TotalTrades != 0 {
		t.Fatalf("dropped-symbol trade must not leak into WXYZ: %+v", st)
	}
}

func TestReconcileAddPreservesExistingSymbolState(t *testing.T) {
	e := newTest("ABCD")
	ms := t0.UnixMilli()
	quote(e, "ABCD", 4.50, 4.52, ms)
	trade(e, "ABCD", 4.52, 5000, ms)
	before := snap(e, "ABCD")

	e.reconcile([]string{"ABCD", "WXYZ"}) // a second window starts watching WXYZ too

	after := snap(e, "ABCD")
	if after.DeltaTotal != before.DeltaTotal || after.TotalTrades != before.TotalTrades {
		t.Fatalf("adding a new symbol must not disturb ABCD's accumulated state: before=%+v after=%+v", before, after)
	}
	wxyz := snap(e, "WXYZ")
	if wxyz.TotalTrades != 0 {
		t.Fatalf("WXYZ should start fresh, got %+v", wxyz)
	}
}

func TestConcurrentSymbolsIsolated(t *testing.T) {
	e := newTest("AAAA", "BBBB")
	ms := t0.UnixMilli()
	quote(e, "AAAA", 4.50, 4.52, ms)
	quote(e, "BBBB", 10.00, 10.02, ms)

	// interleaved trades for both symbols
	trade(e, "AAAA", 4.52, 1000, ms)  // buy
	trade(e, "BBBB", 10.00, 2000, ms) // sell
	trade(e, "AAAA", 4.50, 300, ms)   // sell
	trade(e, "BBBB", 10.02, 500, ms)  // buy

	a := snap(e, "AAAA")
	b := snap(e, "BBBB")
	if a.DeltaTotal != 1000-300 {
		t.Fatalf("AAAA delta: %d", a.DeltaTotal)
	}
	if b.DeltaTotal != 500-2000 {
		t.Fatalf("BBBB delta: %d", b.DeltaTotal)
	}
	if a.TotalTrades != 2 || b.TotalTrades != 2 {
		t.Fatalf("trade counts: a=%d b=%d", a.TotalTrades, b.TotalTrades)
	}
}

func TestSetErrorAppliesToAllWatchedSymbols(t *testing.T) {
	e := newTest("AAAA", "BBBB")
	e.setErrorAll("trades not entitled")
	if a, b := snap(e, "AAAA").Err, snap(e, "BBBB").Err; a != "trades not entitled" || b != "trades not entitled" {
		t.Fatalf("SetError must apply to every watched symbol: AAAA=%q BBBB=%q", a, b)
	}
}

func TestDigestRendersTrailAndPrints(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quote(e, "ABCD", 4.50, 4.52, ms)
	trade(e, "ABCD", 4.52, 60_000, ms)
	e.publish()
	d := e.Digest("ABCD")
	if d == "" {
		t.Fatal("digest empty")
	}
	for _, want := range []string{"ABCD", "Notable prints", "BUY 60000"} {
		if !strings.Contains(d, want) {
			t.Fatalf("digest missing %q:\n%s", want, d)
		}
	}
}

func TestDigestRequiresExplicitSymbolArgument(t *testing.T) {
	e := newTest("ABCD")
	ms := t0.UnixMilli()
	quote(e, "ABCD", 4.50, 4.52, ms)
	trade(e, "ABCD", 4.52, 1000, ms)
	e.publish()
	if d := e.Digest("NOTWATCHED"); d != "" {
		t.Fatalf("digest for an unwatched symbol must be empty, got %q", d)
	}
	if d := e.Digest("ABCD"); d == "" {
		t.Fatal("digest for a watched symbol must not be empty")
	}
}

func TestBlockThresholdFloorsAtMinSizeWhenMedianIsSmall(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quote(e, "ABCD", 4.50, 4.52, ms)
	for i := 0; i < 10; i++ {
		trade(e, "ABCD", 4.52, 10, ms) // tiny median size -> floor should win
	}
	st := snap(e, "ABCD")
	if st.BlockThreshold != blockMinSize {
		t.Fatalf("blockThreshold should floor at %v, got %v", blockMinSize, st.BlockThreshold)
	}
}

func TestBlockThresholdScalesWithMedianSize(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quote(e, "ABCD", 4.50, 4.52, ms)
	for i := 0; i < 9; i++ {
		trade(e, "ABCD", 4.52, 100, ms) // odd-length sample, median == 100
	}
	st := snap(e, "ABCD")
	if st.MedianSize != 100 {
		t.Fatalf("medianSize: got %v want 100", st.MedianSize)
	}
	if want := blockMedianMult * 100.0; st.BlockThreshold != want {
		t.Fatalf("blockThreshold: got %v want %v", st.BlockThreshold, want)
	}
}

func TestMedianSizeAveragesEvenLengthSample(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quote(e, "ABCD", 4.50, 4.52, ms)
	trade(e, "ABCD", 4.52, 100, ms)
	trade(e, "ABCD", 4.52, 300, ms)
	st := snap(e, "ABCD")
	if st.MedianSize != 200 {
		t.Fatalf("medianSize even-length average: got %v want 200", st.MedianSize)
	}
}

func TestSizesRingCapsAt250ForMedian(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quote(e, "ABCD", 4.50, 4.52, ms)
	for i := 0; i < 260; i++ {
		trade(e, "ABCD", 4.52, 1, ms)
	}
	st := e.syms["ABCD"]
	if len(st.sizes) != sizesKept {
		t.Fatalf("sizes ring should cap at %d, got %d", sizesKept, len(st.sizes))
	}
}

func TestDelta10sWindowIsolatedFrom1mAnd5m(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quote(e, "ABCD", 4.50, 4.52, ms)
	// 30s ago: 500 buys (in the 1m/5m windows, outside the 10s window)
	trade(e, "ABCD", 4.52, 500, ms-30_000)
	// 5s ago: 200 sells (inside all three windows)
	quote(e, "ABCD", 4.50, 4.52, ms-5_000)
	trade(e, "ABCD", 4.50, 200, ms-5_000)

	st := snap(e, "ABCD")
	if st.Delta10s != -200 {
		t.Fatalf("delta10s: %d", st.Delta10s)
	}
	if st.Delta1m != 500-200 {
		t.Fatalf("delta1m: %d", st.Delta1m)
	}
}

func TestRatiosMatchDeltaOverVolumePerWindow(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quote(e, "ABCD", 4.50, 4.52, ms)
	trade(e, "ABCD", 4.52, 300, ms) // buy, within the 1s/10s/60s windows
	trade(e, "ABCD", 4.50, 100, ms) // sell, within the 1s/10s/60s windows

	st := snap(e, "ABCD")
	want := (300.0 - 100.0) / (300.0 + 100.0)
	if st.Ratio1s != want {
		t.Fatalf("ratio1s: got %v want %v", st.Ratio1s, want)
	}
	if st.Ratio10s != want {
		t.Fatalf("ratio10s: got %v want %v", st.Ratio10s, want)
	}
	if st.Ratio60s != want {
		t.Fatalf("ratio60s: got %v want %v", st.Ratio60s, want)
	}
}

func TestRatioZeroWhenWindowHasNoVolume(t *testing.T) {
	e := newTest()
	st := snap(e, "ABCD")
	if st.Ratio1s != 0 || st.Ratio10s != 0 || st.Ratio60s != 0 {
		t.Fatalf("ratios should be 0 with no trades: 1s=%v 10s=%v 60s=%v", st.Ratio1s, st.Ratio10s, st.Ratio60s)
	}
}

func TestPrintsPerSecAndBaseline(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quote(e, "ABCD", 4.50, 4.52, ms)
	// 3 trades in the last 1s
	for i := 0; i < 3; i++ {
		trade(e, "ABCD", 4.52, 100, ms)
	}
	// 57 more trades, one per second, spread through the rest of the trailing 60s window
	for i := 0; i < 57; i++ {
		trade(e, "ABCD", 4.52, 100, ms-int64(i+2)*1000)
	}
	st := snap(e, "ABCD")
	if st.PrintsPerSec != 3 {
		t.Fatalf("printsPerSec: got %d want 3", st.PrintsPerSec)
	}
	if st.PrintsPerSecBaseline != 1.0 { // 60 trades total over the trailing 60s
		t.Fatalf("printsPerSecBaseline: got %v want 1.0", st.PrintsPerSecBaseline)
	}
}

func TestCVDSeriesAccumulatesOncePerPublishTick(t *testing.T) {
	e := newTest()
	tick := t0
	e.now = func() time.Time { return tick }
	quote(e, "ABCD", 4.50, 4.52, tick.UnixMilli())
	trade(e, "ABCD", 4.52, 100, tick.UnixMilli()) // buyTotal=100, sellTotal=0

	e.publish()
	if st := snap(e, "ABCD"); len(st.CVDSeries) != 1 || st.CVDSeries[0].Value != 100 {
		t.Fatalf("expected a single cvd point of 100 after the first publish, got %+v", st.CVDSeries)
	}

	tick = tick.Add(time.Second)
	e.publish() // no new trades; still exactly one more point
	if st := snap(e, "ABCD"); len(st.CVDSeries) != 2 {
		t.Fatalf("expected 2 cvd points after the second publish, got %d", len(st.CVDSeries))
	}

	// snapshot() alone (bypassing publish) must NOT grow the series -- only
	// publish()'s 1Hz tick does.
	snap(e, "ABCD")
	snap(e, "ABCD")
	if st := snap(e, "ABCD"); len(st.CVDSeries) != 2 {
		t.Fatalf("snapshot() must not append CVD points on its own, got %d", len(st.CVDSeries))
	}
}

func TestCVDSeriesCapsAt300(t *testing.T) {
	e := newTest()
	tick := t0
	e.now = func() time.Time { return tick }
	for i := 0; i < 320; i++ {
		e.publish()
		tick = tick.Add(time.Second)
	}
	st := snap(e, "ABCD")
	if len(st.CVDSeries) != cvdPointsKept {
		t.Fatalf("cvd series should cap at %d, got %d", cvdPointsKept, len(st.CVDSeries))
	}
}
