package engine

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// newBarState mirrors shard.get's initialization (curveBkt/curveFirst/barHead
// all -1) so white-box tests exercise the same starting state as production.
func newBarState(sym string) *tickerState {
	return &tickerState{sym: sym, curveBkt: -1, curveFirst: -1, barHead: -1}
}

// testLabels builds a full set of synthetic bucket labels; white-box tests only
// assert Date pass-through, the real ET formatting is asserted at engine level.
func testLabels() []string {
	l := make([]string, core.BucketCount)
	for i := range l {
		l[i] = fmt.Sprintf("b%03d", i)
	}
	return l
}

// TestRecordCurveCommitUsesAggExtremes proves a bucket's hi/lo come from TRUE
// agg extremes (pendHi/pendLo folded by applyAgg), not 10 Hz samples of last:
// the fed high (15) exceeds every close ever printed, and it must survive both
// the mid-bucket in-place stamp and the rollover commit.
func TestRecordCurveCommitUsesAggExtremes(t *testing.T) {
	ts := newBarState("X")
	feedBar(ts, t0, 10, 15, 8, 10, 10, 100) // high 15 / low 8 never equal a close
	ts.recordCurve(10)
	if ts.bktHi[10] != 15 || ts.bktLo[10] != 8 || ts.bktCl[10] != 10 {
		t.Fatalf("in-place stamp want 15/8/10 got %v/%v/%v", ts.bktHi[10], ts.bktLo[10], ts.bktCl[10])
	}
	if ts.curveFirst != 10 {
		t.Fatalf("curveFirst want 10 got %d", ts.curveFirst)
	}

	// A second agg in the SAME bucket: hi/lo keep the wider extremes, close
	// tracks last — and a mid-bucket read is never stale.
	feedBar(ts, t0+1, 11, 12, 10.5, 11, 11, 100)
	ts.recordCurve(10)
	if ts.bktHi[10] != 15 || ts.bktLo[10] != 8 || ts.bktCl[10] != 11 {
		t.Fatalf("mid-bucket update want 15/8/11 got %v/%v/%v", ts.bktHi[10], ts.bktLo[10], ts.bktCl[10])
	}

	// Rollover: the outgoing bucket keeps the committed extremes; the new bucket
	// opens flat at last with a fresh pending window.
	ts.recordCurve(11)
	if ts.bktHi[10] != 15 || ts.bktLo[10] != 8 || ts.bktCl[10] != 11 {
		t.Fatalf("committed bucket want 15/8/11 got %v/%v/%v", ts.bktHi[10], ts.bktLo[10], ts.bktCl[10])
	}
	if ts.bktHi[11] != 11 || ts.bktLo[11] != 11 || ts.bktCl[11] != 11 {
		t.Fatalf("fresh bucket should open flat at last=11, got %v/%v/%v", ts.bktHi[11], ts.bktLo[11], ts.bktCl[11])
	}
	if ts.pendHi != 0 || ts.pendLo != 0 {
		t.Fatalf("pending window not reset on rollover: %v/%v", ts.pendHi, ts.pendLo)
	}
}

// TestRecordCurveForwardFillFlat proves skipped buckets fill as flat
// hi=lo=close=last bars with unchanged cumulative volume, and that buckets
// before the first observed one are never back-filled.
func TestRecordCurveForwardFillFlat(t *testing.T) {
	ts := newBarState("X")
	feedBar(ts, t0, 20, 21, 19, 20, 20, 100)
	ts.recordCurve(5)
	ts.recordCurve(9) // skips 6,7,8

	if ts.bktHi[5] != 21 || ts.bktLo[5] != 19 || ts.bktCl[5] != 20 {
		t.Fatalf("committed bucket 5 want 21/19/20 got %v/%v/%v", ts.bktHi[5], ts.bktLo[5], ts.bktCl[5])
	}
	for b := 6; b <= 9; b++ {
		if ts.bktHi[b] != 20 || ts.bktLo[b] != 20 || ts.bktCl[b] != 20 {
			t.Fatalf("gap bucket %d should be flat 20, got %v/%v/%v", b, ts.bktHi[b], ts.bktLo[b], ts.bktCl[b])
		}
		if ts.curve[b] != 100 {
			t.Fatalf("gap bucket %d cumulative want 100 got %d", b, ts.curve[b])
		}
	}
	for b := 0; b < 5; b++ { // pre-first buckets stay zero (no back-fill)
		if ts.bktHi[b] != 0 || ts.bktLo[b] != 0 || ts.bktCl[b] != 0 || ts.curve[b] != 0 {
			t.Fatalf("pre-first bucket %d should stay zero", b)
		}
	}
}

// TestResetSessionClearsSessionBars proves a day rollover wipes the per-bucket
// OHLC arrays, the pending extremes, and the first-observed marker.
func TestResetSessionClearsSessionBars(t *testing.T) {
	ts := newBarState("X")
	feedBar(ts, t0, 10, 15, 8, 10, 10, 100_000)
	ts.recordCurve(0)
	ts.recordCurve(30)

	ts.resetSession()
	if ts.bktHi != [core.BucketCount]float32{} || ts.bktLo != [core.BucketCount]float32{} || ts.bktCl != [core.BucketCount]float32{} {
		t.Fatal("resetSession must zero the per-bucket OHLC arrays")
	}
	if ts.pendHi != 0 || ts.pendLo != 0 {
		t.Fatalf("resetSession must clear pending extremes, got %v/%v", ts.pendHi, ts.pendLo)
	}
	if ts.curveFirst != -1 || ts.curveBkt != -1 {
		t.Fatalf("resetSession must clear curveFirst/curveBkt, got %d/%d", ts.curveFirst, ts.curveBkt)
	}
	if ts.sessionBars(core.BucketCount-1, testLabels()) != nil {
		t.Fatal("sessionBars after reset should be nil")
	}
}

// TestSessionBarsSeries proves the built series excludes pre-first buckets,
// computes V as cumulative deltas (0 across forward-filled gaps, 0 for a
// mid-session first bar), and O-chains from the prior close.
func TestSessionBarsSeries(t *testing.T) {
	labels := testLabels()

	ts := newBarState("X")
	feedBar(ts, t0, 10, 11, 9, 10, 10, 60_000)
	ts.recordCurve(3)  // first observed bucket = 3
	ts.recordCurve(24) // forward-fill 4..24
	feedBar(ts, t0+1, 12, 13, 11.5, 12, 12, 1_000)
	ts.recordCurve(24) // in-place: current bucket picks up the new agg

	bars := ts.sessionBars(24, labels)
	if len(bars) != 22 {
		t.Fatalf("want 22 bars (buckets 3..24), got %d", len(bars))
	}
	if bars[0].Date != labels[3] || bars[len(bars)-1].Date != labels[24] {
		t.Fatalf("date range want %s..%s got %s..%s", labels[3], labels[24], bars[0].Date, bars[len(bars)-1].Date)
	}
	// First bar: mid-session first observation -> V=0 (its cumulative includes
	// the whole pre-observation day), extremes from the agg, O = its own C.
	if bars[0].V != 0 {
		t.Fatalf("mid-session first bar V want 0 got %d", bars[0].V)
	}
	if bars[0].H != 11 || bars[0].L != 9 || bars[0].C != 10 || bars[0].O != 10 {
		t.Fatalf("first bar OHLC want 10/11/9/10 got %v/%v/%v/%v", bars[0].O, bars[0].H, bars[0].L, bars[0].C)
	}
	// Gap buckets 4..23: flat at 10, V=0, O chained from the prior close.
	for i := 1; i < len(bars)-1; i++ {
		b := bars[i]
		if b.H != 10 || b.L != 10 || b.C != 10 || b.O != 10 || b.V != 0 {
			t.Fatalf("gap bar %d should be flat 10 with V=0: %+v", i, b)
		}
	}
	// Last bar: the second agg's volume as a delta, extremes folded, O = prior C.
	last := bars[len(bars)-1]
	if last.V != 1_000 {
		t.Fatalf("last bar V want 1000 got %d", last.V)
	}
	if last.H != 13 || last.C != 12 || last.O != 10 {
		t.Fatalf("last bar want H=13 C=12 O=10 got H=%v C=%v O=%v", last.H, last.C, last.O)
	}
}

// TestSessionBarsFirstBucketVolume proves a ticker observed from the session's
// very first bucket reports curve[0] as the first bar's volume (nothing
// preceded it, so the cumulative IS the bucket volume).
func TestSessionBarsFirstBucketVolume(t *testing.T) {
	ts := newBarState("X")
	feedBar(ts, t0, 10, 11, 9, 10, 10, 60_000)
	ts.recordCurve(0)
	ts.recordCurve(25)

	bars := ts.sessionBars(25, testLabels())
	if len(bars) != 26 {
		t.Fatalf("want 26 bars, got %d", len(bars))
	}
	if bars[0].V != 60_000 {
		t.Fatalf("session-start first bar V want 60000 (curve[0]) got %d", bars[0].V)
	}
}

// TestSessionBarsReseedNeverYieldsNegativeVolume: seedApply SETS sessVol from
// the REST snapshot (the reconnect-correction contract), so the snapshot coming
// in LOWER than the engine-accumulated sum steps the cumulative curve DOWN
// mid-session. The naive adjacent-entry delta would then emit a NEGATIVE bucket
// volume into a core.DailyBar — fabricated data the pattern pass would fold
// into its volume averages. sessionBars must clamp the delta at 0.
func TestSessionBarsReseedNeverYieldsNegativeVolume(t *testing.T) {
	ts := newBarState("X")
	feedBar(ts, t0, 10, 11, 9, 10, 10, 60_000)
	ts.recordCurve(3)
	ts.recordCurve(10) // buckets 3..10 committed at cumulative 60k

	// Reconnect re-seed: the snapshot's accumulated volume is LOWER (55k).
	ts.seedApply(core.Seed{Agg: core.Agg{
		Sym: "X", Close: 10, High: 11, Low: 9, Vol: 55_000, EndMs: (t0 + 1) * 1000,
	}})
	ts.recordCurve(25) // buckets 11..25 stamp the stepped-down cumulative

	bars := ts.sessionBars(25, testLabels())
	if bars == nil {
		t.Fatal("23 observed buckets on 55k session volume must pass both gates")
	}
	for i, b := range bars {
		if b.V < 0 {
			t.Fatalf("bar %d carries negative volume %d — a re-seed stepping the "+
				"cumulative down must clamp to 0, never fabricate negative volume", i, b.V)
		}
	}
}

// TestSessionBarsGates proves both read-time gates: fewer than
// minSessionBarsBuckets observed buckets -> nil, and session volume under
// minSessionBarsVol -> nil (capture itself stays ungated).
func TestSessionBarsGates(t *testing.T) {
	labels := testLabels()

	// Bucket gate: 22 buckets recorded, but cur clamps the readable span to 19.
	ts := newBarState("X")
	feedBar(ts, t0, 10, 11, 9, 10, 10, 60_000)
	ts.recordCurve(3)
	ts.recordCurve(24)
	if got := ts.sessionBars(21, labels); got != nil { // 3..21 = 19 < 20
		t.Fatalf("19 observed buckets should gate out, got %d bars", len(got))
	}
	if got := ts.sessionBars(22, labels); got == nil { // 3..22 = 20 passes
		t.Fatal("20 observed buckets should pass the gate")
	}

	// Volume gate: plenty of buckets, one share short of the floor.
	lo := newBarState("Y")
	feedBar(lo, t0, 10, 11, 9, 10, 10, minSessionBarsVol-1)
	lo.recordCurve(0)
	lo.recordCurve(40)
	if got := lo.sessionBars(40, labels); got != nil {
		t.Fatalf("sessVol below floor should gate out, got %d bars", len(got))
	}
	feedBar(lo, t0+1, 10, 10, 10, 10, 10, 1) // one more share crosses the floor
	lo.recordCurve(40)
	if got := lo.sessionBars(40, labels); got == nil {
		t.Fatal("sessVol at floor should pass the gate")
	}
}

// TestEngineSessionBars exercises the full fan-out with a movable fixed clock:
// a ticker observed at 10:00 ET, then the clock jumps to 11:35 ET (20 buckets),
// and SessionBars must return its series with real ET date labels, agg-extreme
// H/L on the first bar, flat forward-filled bars after it, and V deltas — while
// a low-volume ticker stays gated out.
func TestEngineSessionBars(t *testing.T) {
	start := time.Date(2026, 6, 23, 14, 0, 0, 0, time.UTC) // 10:00 ET -> bucket 72
	var mu sync.Mutex
	now := start
	eng := New(Options{
		TickHz: 50,
		Now: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return now
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	eng.Apply(core.Agg{Sym: "AAA", Close: 10, High: 12, Low: 9, Vol: 60_000, EndMs: start.Unix() * 1000})
	eng.Apply(core.Agg{Sym: "BBB", Close: 5, High: 5, Low: 5, Vol: 10, EndMs: start.Unix() * 1000})
	waitForSym(t, eng, "AAA", 3*time.Second)
	waitForSym(t, eng, "BBB", 3*time.Second)

	// Before the clock advances only one bucket is observed: everyone gated out.
	if got := eng.SessionBars(); len(got) != 0 {
		t.Fatalf("single observed bucket should gate everything out, got %d entries", len(got))
	}

	mu.Lock()
	now = time.Date(2026, 6, 23, 15, 35, 0, 0, time.UTC) // 11:35 ET -> bucket 91
	mu.Unlock()

	deadline := time.Now().Add(3 * time.Second)
	var bars []core.DailyBar
	for {
		m := eng.SessionBars()
		if _, ok := m["BBB"]; ok {
			t.Fatal("BBB (10 shares) must be gated out by the volume floor")
		}
		if b, ok := m["AAA"]; ok {
			bars = b
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for AAA in SessionBars")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if len(bars) != 20 { // buckets 72..91
		t.Fatalf("want 20 bars, got %d", len(bars))
	}
	if bars[0].Date != "2026-06-23 10:00" || bars[19].Date != "2026-06-23 11:35" {
		t.Fatalf("date range want 10:00..11:35, got %s..%s", bars[0].Date, bars[19].Date)
	}
	if bars[0].H != 12 || bars[0].L != 9 || bars[0].C != 10 || bars[0].O != 10 || bars[0].V != 0 {
		t.Fatalf("first bar want O10 H12 L9 C10 V0, got %+v", bars[0])
	}
	for i := 1; i < len(bars); i++ {
		b := bars[i]
		if b.H != 10 || b.L != 10 || b.C != 10 || b.V != 0 {
			t.Fatalf("forward-filled bar %d should be flat 10 with V=0: %+v", i, b)
		}
		if b.O != bars[i-1].C {
			t.Fatalf("bar %d O must chain from prior C: O=%v prior C=%v", i, b.O, bars[i-1].C)
		}
	}
}

// TestSessionBarsRaceWithApply hammers SessionBars from the test goroutine
// while aggregates stream in — under -race this proves the ctrl-closure
// fan-out never touches shard state outside the shard goroutine. (Curves has
// no dedicated race test; SessionBars gets one.)
func TestSessionBarsRaceWithApply(t *testing.T) {
	start := time.Date(2026, 6, 23, 15, 35, 0, 0, time.UTC) // 11:35 ET
	eng := New(Options{TickHz: 50, Now: func() time.Time { return start }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		syms := []string{"AAA", "BBB", "CCC", "DDD"}
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			eng.Apply(core.Agg{
				Sym: syms[i%len(syms)], Close: 10, High: 11, Low: 9,
				Vol: 5_000, EndMs: (start.Unix() + int64(i)) * 1000,
			})
		}
	}()
	for i := 0; i < 50; i++ {
		_ = eng.SessionBars()
		_ = eng.Curves(core.BucketCount - 1)
	}
	close(stop)
	wg.Wait()
}
