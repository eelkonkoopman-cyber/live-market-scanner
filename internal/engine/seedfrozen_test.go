package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// frozenCase is one row of the anti-drift table: raw window facts driven
// through BOTH freeze paths — the live one (scope state + SetSession) and the
// backfill one (SeedFrozen) — which must yield identical frozen Row fields.
//
// Constraint baked into the table: vwap > 0 iff vol > 0, because the live
// path derives vwap validity from accumulated volume (with a VWAP→Close
// fallback) while the backfill derives it from the stat itself — the two
// representations only coincide under that constraint, which is also the only
// shape real data takes (volume without a price, or a VWAP without volume,
// does not occur). Values are chosen exactly representable in binary floating
// point so the cross-path comparison can demand EXACT equality.
type frozenCase struct {
	name      string
	prevClose float64 // 0 = unknown (never seeded)
	hi        float64 // 0 = no high observed
	lo        float64 // 0 = no low observed
	close     float64 // session-end price (always > 0: both paths require a trade)
	vwap      float64 // 0 = unknown (requires vol == 0, see above)
	vol       int64
}

// wantFrozen restates the frozen-metric formulas INDEPENDENTLY of
// newFrozenScope (plain float64 math, no shared code), so a mutation of the
// helper turns both paths' assertions red together — the decisive anti-drift
// property. Returns pre-slot expectations in emit order.
func wantFrozen(c frozenCase) (chg, hi, lo, vwap, gap, fade float64, vol int64) {
	chg, hi, lo, vwap, gap, fade = core.Null, core.Null, core.Null, core.Null, core.Null, core.Null
	vol = c.vol
	if c.prevClose > 0 {
		chg = (c.close/c.prevClose - 1) * 100
	}
	if c.hi > 0 {
		hi = c.hi
	}
	if c.lo > 0 {
		lo = c.lo
	}
	if c.vwap > 0 {
		vwap = c.vwap
	}
	if c.hi > 0 && c.prevClose > 0 {
		gap = (c.hi/c.prevClose - 1) * 100
	}
	if c.hi > 0 && c.close > 0 {
		fade = (c.hi - c.close) / c.hi * 100
		if fade < 0 {
			fade = 0
		}
	}
	return
}

// frozenFieldEq compares one frozen field against its independent expectation:
// Null must match Null exactly; real values match within the float32 tolerance
// the existing freeze tests use.
func frozenFieldEq(got, want float64) bool {
	if core.IsNull(want) {
		return core.IsNull(got)
	}
	return !core.IsNull(got) && abs(got-want) < 1e-3
}

func preMatchesWant(r core.Row, c frozenCase) bool {
	chg, hi, lo, vwap, gap, fade, vol := wantFrozen(c)
	return frozenFieldEq(r.PreChg, chg) && frozenFieldEq(r.PreHigh, hi) &&
		frozenFieldEq(r.PreLow, lo) && frozenFieldEq(r.PreVwap, vwap) &&
		frozenFieldEq(r.PreGap, gap) && frozenFieldEq(r.PreFade, fade) &&
		r.PreVol == vol
}

// waitPreMatch blocks until AAA's row matches the case's independently
// computed pre* expectations, returning the matching row; a timeout fails with
// the last row seen so a drifted formula is diagnosable.
func waitPreMatch(t *testing.T, eng *Engine, c frozenCase, via string) core.Row {
	t.Helper()
	deadline := time.After(3 * time.Second)
	var last core.Row
	seen := false
	for {
		select {
		case snap := <-eng.Snapshots():
			if i, ok := snap.Index["AAA"]; ok {
				last, seen = snap.Rows[i], true
				if preMatchesWant(last, c) {
					return last
				}
			}
		case <-deadline:
			chg, hi, lo, vwap, gap, fade, vol := wantFrozen(c)
			t.Fatalf("%s path never matched want chg=%v hi=%v lo=%v vwap=%v gap=%v fade=%v vol=%d; last row (seen=%v): chg=%v hi=%v lo=%v vwap=%v gap=%v fade=%v vol=%d",
				via, chg, hi, lo, vwap, gap, fade, vol, seen,
				last.PreChg, last.PreHigh, last.PreLow, last.PreVwap, last.PreGap, last.PreFade, last.PreVol)
			return core.Row{}
		}
	}
}

func startEngine(t *testing.T) *Engine {
	t.Helper()
	fixed := time.Date(2026, 6, 23, 13, 0, 0, 0, time.UTC) // 09:00 ET, pre-market
	eng := New(Options{TickHz: 50, Now: func() time.Time { return fixed }})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = eng.Run(ctx) }()
	return eng
}

// driveLive produces the case's frozen pre* slot through the LIVE path: scope
// state built from one Apply'd aggregate, then the 09:30 SetSession boundary.
func driveLive(t *testing.T, c frozenCase) core.Row {
	t.Helper()
	eng := startEngine(t)
	eng.SetSession(core.SessionPre)
	waitForSession(t, eng, core.SessionPre, 3*time.Second)
	sec := time.Date(2026, 6, 23, 13, 0, 0, 0, time.UTC).Unix()
	// One agg carries the whole window: High/Low build scopeHi/scopeLo (0 =
	// never set), Close is the session-end last (a Close above High leaves
	// scopeHi at High — the odd-data fade-clamp shape), VWAP×Vol land in the
	// scoped VWAP accumulators.
	eng.Apply(core.Agg{Sym: "AAA", Close: c.close, High: c.hi, Low: c.lo,
		VWAP: c.vwap, Vol: c.vol, EndMs: sec * 1000})
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool { return r.Last == float64(float32(c.close)) })
	if c.prevClose > 0 {
		eng.SeedClose("AAA", c.prevClose) // ctrl: FIFO-ordered before the boundary below
	}
	eng.SetSession(core.SessionRegular) // 09:30 boundary: freeze, then wipe
	return waitPreMatch(t, eng, c, "live")
}

// driveSeed produces the same slot through the BACKFILL path: a tracked
// ticker, the same prevClose, then SeedFrozen with the raw window stats.
func driveSeed(t *testing.T, c frozenCase) core.Row {
	t.Helper()
	eng := startEngine(t)
	sec := time.Date(2026, 6, 23, 13, 0, 0, 0, time.UTC).Unix()
	// Track AAA with a neutral print; frozen fields never read live state.
	eng.Apply(core.Agg{Sym: "AAA", Close: 1, Vol: 1, EndMs: sec * 1000})
	waitForSym(t, eng, "AAA", 3*time.Second)
	if c.prevClose > 0 {
		eng.SeedClose("AAA", c.prevClose) // ctrl: FIFO-ordered before SeedFrozen
	}
	eng.SeedFrozen(core.SessionPre, map[string]core.SessionStats{
		"AAA": {Hi: c.hi, Lo: c.lo, Vwap: c.vwap, Close: c.close, Vol: c.vol},
	})
	return waitPreMatch(t, eng, c, "seed")
}

// bitEq is the cross-path identity check: both Null, or the SAME float64 —
// exact, no tolerance, because both paths widen the same float32 arithmetic.
func bitEq(a, b float64) bool {
	if core.IsNull(a) || core.IsNull(b) {
		return core.IsNull(a) && core.IsNull(b)
	}
	return a == b
}

// TestFrozenLiveAndSeedIdentical is the anti-drift table: every case runs
// through BOTH the live boundary freeze and SeedFrozen, each asserted against
// formulas restated independently in this file, then compared to each other
// field-for-field with EXACT equality (Null-for-Null included). If the shared
// helper's derivation is mutated, the live and seed subtests go red together;
// if only one goes red, the paths have drifted and the refactor has failed.
func TestFrozenLiveAndSeedIdentical(t *testing.T) {
	cases := []frozenCase{
		// The canonical pre-market move: chg +40, gap +50 (off the HIGH, not
		// chg), fade off the high — three different numbers.
		{name: "typical move", prevClose: 10, hi: 15, lo: 11, close: 14, vwap: 13, vol: 300},
		// prevClose unknown: chg and gap Null, hi/lo/vwap/vol/fade still real.
		{name: "prevClose unknown", prevClose: 0, hi: 12, lo: 9, close: 11, vwap: 10.5, vol: 100},
		// No high observed: hi, gap AND fade Null; chg/lo/vwap unaffected.
		{name: "no high", prevClose: 10, hi: 0, lo: 9, close: 11, vwap: 10.5, vol: 100},
		// No low observed: only lo Null.
		{name: "no low", prevClose: 10, hi: 12, lo: 0, close: 11, vwap: 10.5, vol: 100},
		// Session ends AT its high: fade exactly 0 — set, not Null.
		{name: "ends at high", prevClose: 10, hi: 12, lo: 10, close: 12, vwap: 11, vol: 100},
		// Close above the recorded high (odd data): fade clamps to 0.
		{name: "fade clamps negative", prevClose: 10, hi: 12, lo: 11, close: 13, vwap: 12, vol: 100},
		// No volume traded across the window: vwap Null, vol 0, prices real.
		{name: "no volume no vwap", prevClose: 10, hi: 12, lo: 9, close: 11, vwap: 0, vol: 0},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			var liveRow, seedRow core.Row
			var liveOK, seedOK bool
			t.Run("live", func(t *testing.T) { liveRow = driveLive(t, c); liveOK = true })
			t.Run("seed", func(t *testing.T) { seedRow = driveSeed(t, c); seedOK = true })
			if !liveOK || !seedOK {
				return // the failing subtest already reported; no identity to check
			}
			if !bitEq(liveRow.PreChg, seedRow.PreChg) || !bitEq(liveRow.PreHigh, seedRow.PreHigh) ||
				!bitEq(liveRow.PreLow, seedRow.PreLow) || !bitEq(liveRow.PreVwap, seedRow.PreVwap) ||
				!bitEq(liveRow.PreGap, seedRow.PreGap) || !bitEq(liveRow.PreFade, seedRow.PreFade) ||
				liveRow.PreVol != seedRow.PreVol {
				t.Fatalf("live and seed paths drifted:\nlive: chg=%v hi=%v lo=%v vwap=%v gap=%v fade=%v vol=%d\nseed: chg=%v hi=%v lo=%v vwap=%v gap=%v fade=%v vol=%d",
					liveRow.PreChg, liveRow.PreHigh, liveRow.PreLow, liveRow.PreVwap, liveRow.PreGap, liveRow.PreFade, liveRow.PreVol,
					seedRow.PreChg, seedRow.PreHigh, seedRow.PreLow, seedRow.PreVwap, seedRow.PreGap, seedRow.PreFade, seedRow.PreVol)
			}
		})
	}
}

// TestSeedFrozenUnknownPrevClose pins the backfill's representation of
// "bars found, but prevClose never seeded": chg and gap Null via their own
// validity flags, hi/lo/vwap/vol/fade all real.
func TestSeedFrozenUnknownPrevClose(t *testing.T) {
	eng := startEngine(t)
	eng.Apply(core.Agg{Sym: "AAA", Close: 1, Vol: 1, EndMs: time.Now().Unix() * 1000})
	waitForSym(t, eng, "AAA", 3*time.Second)

	eng.SeedFrozen(core.SessionPre, map[string]core.SessionStats{
		"AAA": {Hi: 12, Lo: 9, Vwap: 10.5, Close: 11, Vol: 100},
	})
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool {
		return r.PreVol == 100 &&
			core.IsNull(r.PreChg) && core.IsNull(r.PreGap) &&
			!core.IsNull(r.PreHigh) && abs(r.PreHigh-12) < 1e-3 &&
			!core.IsNull(r.PreLow) && abs(r.PreLow-9) < 1e-3 &&
			!core.IsNull(r.PreVwap) && abs(r.PreVwap-10.5) < 1e-3 &&
			!core.IsNull(r.PreFade) && abs(r.PreFade-(12.0-11.0)/12.0*100) < 1e-3
	})
}

// TestSeedFrozenSkipsUnknownSymbols proves a stats entry for a symbol the
// engine doesn't track is silently dropped: no ticker is created, no panic,
// and the tracked symbol still gets its slot.
func TestSeedFrozenSkipsUnknownSymbols(t *testing.T) {
	eng := startEngine(t)
	eng.Apply(core.Agg{Sym: "AAA", Close: 1, Vol: 1, EndMs: time.Now().Unix() * 1000})
	waitForSym(t, eng, "AAA", 3*time.Second)

	eng.SeedFrozen(core.SessionPre, map[string]core.SessionStats{
		"AAA": {Hi: 12, Lo: 9, Vwap: 10.5, Close: 11, Vol: 100},
		"ZZZ": {Hi: 50, Lo: 40, Vwap: 45, Close: 44, Vol: 999},
	})
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool { return r.PreVol == 100 })

	snap := waitForSym(t, eng, "AAA", 3*time.Second)
	if _, ok := snap.Index["ZZZ"]; ok {
		t.Fatal("SeedFrozen must never create a ticker for an untracked symbol")
	}
	if got := eng.Tracked(); got != 1 {
		t.Fatalf("tracked want 1 (AAA only), got %d", got)
	}
}

// TestSeedFrozenPostAndClosedNoOp proves which values without a review slot
// (Post, Closed) seed nothing — while which == SessionRegular routes to the
// rth* slot, ordered after the no-ops so their absence is deterministic.
func TestSeedFrozenPostAndClosedNoOp(t *testing.T) {
	eng := startEngine(t)
	eng.SeedClose("AAA", 10)
	eng.Apply(core.Agg{Sym: "AAA", Close: 1, Vol: 1, EndMs: time.Now().Unix() * 1000})
	waitForSym(t, eng, "AAA", 3*time.Second)

	stats := map[string]core.SessionStats{"AAA": {Hi: 12, Lo: 9, Vwap: 10.5, Close: 11, Vol: 100}}
	eng.SeedFrozen(core.SessionPost, stats)   // no-op: no review slot
	eng.SeedFrozen(core.SessionClosed, stats) // no-op: no review slot
	eng.SeedFrozen(core.SessionRegular, stats)

	// The Regular seed landing proves the shard processed everything queued
	// before it; the pre* slot must still be untouched.
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool { return r.RthVol == 100 })
	snap := waitForSym(t, eng, "AAA", 3*time.Second)
	r := snap.Rows[snap.Index["AAA"]]
	if core.IsNull(r.RthChg) || abs(r.RthChg-10) > 1e-3 { // pct(11, 10) via engine's prevClose
		t.Fatalf("rthChg want +10 got %v", r.RthChg)
	}
	if !core.IsNull(r.PreChg) || !core.IsNull(r.PreHigh) || !core.IsNull(r.PreLow) ||
		!core.IsNull(r.PreVwap) || !core.IsNull(r.PreGap) || !core.IsNull(r.PreFade) || r.PreVol != 0 {
		t.Fatalf("Post/Closed seeds must be no-ops, got pre=(%v %v %v %v %v %v %d)",
			r.PreChg, r.PreHigh, r.PreLow, r.PreVwap, r.PreGap, r.PreFade, r.PreVol)
	}
}

// TestSeedFrozenOverwritesLiveSlot pins the overwrite contract: a slot the
// live boundary already froze is replaced unconditionally by a later
// SeedFrozen (the CALLER gates when backfill is appropriate, not the engine).
func TestSeedFrozenOverwritesLiveSlot(t *testing.T) {
	eng := startEngine(t)
	preMarketMove(t, eng, time.Date(2026, 6, 23, 13, 0, 0, 0, time.UTC).Unix())
	eng.SetSession(core.SessionRegular) // live freeze: PreVol 300, PreHigh 15
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool { return r.PreVol == 300 })

	eng.SeedFrozen(core.SessionPre, map[string]core.SessionStats{
		"AAA": {Hi: 30, Lo: 20, Vwap: 25, Close: 28, Vol: 999},
	})
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool {
		return r.PreVol == 999 &&
			!core.IsNull(r.PreHigh) && abs(r.PreHigh-30) < 1e-3 &&
			!core.IsNull(r.PreChg) && abs(r.PreChg-180) < 1e-3 && // pct(28, 10)
			!core.IsNull(r.PreGap) && abs(r.PreGap-200) < 1e-3 // pct(30, 10)
	})
}

// TestSeedFrozenRaceWithApply hammers SeedFrozen from the test goroutine while
// aggregates stream in — under -race this proves the ctrl-closure fan-out
// never touches shard state outside the shard goroutine (the
// TestSessionBarsRaceWithApply precedent).
func TestSeedFrozenRaceWithApply(t *testing.T) {
	eng := startEngine(t)
	start := time.Date(2026, 6, 23, 13, 0, 0, 0, time.UTC).Unix()

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
				VWAP: 10, Vol: 5_000, EndMs: (start + int64(i)) * 1000,
			})
		}
	}()
	stats := map[string]core.SessionStats{
		"AAA": {Hi: 12, Lo: 9, Vwap: 10.5, Close: 11, Vol: 100},
		"BBB": {Hi: 6, Lo: 4, Vwap: 5, Close: 5, Vol: 200},
		"ZZZ": {Hi: 50, Lo: 40, Vwap: 45, Close: 44, Vol: 999}, // never tracked
	}
	for i := 0; i < 50; i++ {
		eng.SeedFrozen(core.SessionPre, stats)
		eng.SeedFrozen(core.SessionRegular, stats)
	}
	close(stop)
	wg.Wait()
}
