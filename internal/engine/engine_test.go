package engine

import (
	"context"
	"testing"
	"time"

	_ "time/tzdata" // embed IANA tz DB so America/New_York resolves on Windows

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestBucketOf(t *testing.T) {
	et, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load tz: %v", err)
	}
	cases := []struct {
		h, m, want int
	}{
		{4, 0, 0},     // 04:00 session open
		{4, 5, 1},     // first 5-min step
		{9, 30, 66},   // regular open
		{15, 55, 143}, // last regular bucket
		{19, 55, 191}, // last extended bucket
		{3, 0, 0},     // before open -> clamp low
		{20, 30, 191}, // after close -> clamp high
	}
	for _, c := range cases {
		got := bucketOf(time.Date(2026, 6, 23, c.h, c.m, 0, 0, et))
		if got != c.want {
			t.Errorf("%02d:%02d bucket=%d want %d", c.h, c.m, got, c.want)
		}
	}
}

func TestRoundPow2(t *testing.T) {
	for in, want := range map[int]int{1: 1, 2: 2, 3: 4, 5: 8, 8: 8, 9: 16, 0: 1} {
		if got := roundPow2(in); got != want {
			t.Errorf("roundPow2(%d)=%d want %d", in, got, want)
		}
	}
}

// TestEngineApplyAndSnapshot exercises the full pipeline: Apply -> shard ->
// merger -> Snapshots(), with a fixed clock for a deterministic bucket.
func TestEngineApplyAndSnapshot(t *testing.T) {
	fixed := time.Date(2026, 6, 23, 14, 0, 0, 0, time.UTC) // 10:00 ET
	eng := New(Options{TickHz: 20, Now: func() time.Time { return fixed }})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	eng.SeedClose("AAA", 9.0)
	eng.Apply(core.Agg{Sym: "AAA", Close: 10, High: 10, Low: 10, Vol: 100, EndMs: fixed.Unix() * 1000})
	eng.Apply(core.Agg{Sym: "BBB", Close: 5, High: 5, Low: 5, Vol: 50, EndMs: fixed.Unix() * 1000})

	snap := waitForSym(t, eng, "AAA", 3*time.Second)
	row := snap.Rows[snap.Index["AAA"]]
	if row.Last != 10 {
		t.Fatalf("AAA last want 10 got %v", row.Last)
	}
	if row.Vol != 100 {
		t.Fatalf("AAA vol want 100 got %v", row.Vol)
	}
	if eng.Tracked() < 2 {
		t.Fatalf("tracked want >=2 got %d", eng.Tracked())
	}
}

// TestSetSecTypesAppearsInSnapshot proves SetSecTypes hot-swaps and a
// subsequent snapshot row carries the right SecType, while a symbol absent
// from the map stays "" rather than crashing (nil-map-safe, mirroring
// SetFloats's own "always non-nil" hot path guarantee).
func TestSetSecTypesAppearsInSnapshot(t *testing.T) {
	fixed := time.Date(2026, 6, 23, 14, 0, 0, 0, time.UTC)
	eng := New(Options{TickHz: 20, Now: func() time.Time { return fixed }})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	eng.Apply(core.Agg{Sym: "AAA", Close: 10, High: 10, Low: 10, Vol: 100, EndMs: fixed.Unix() * 1000})
	eng.Apply(core.Agg{Sym: "BBB", Close: 5, High: 5, Low: 5, Vol: 50, EndMs: fixed.Unix() * 1000})

	// Before SetSecTypes: neither symbol has a type yet.
	snap := waitForSym(t, eng, "AAA", 3*time.Second)
	if got := snap.Rows[snap.Index["AAA"]].SecType; got != "" {
		t.Fatalf("AAA SecType before SetSecTypes = %q, want empty", got)
	}

	eng.SetSecTypes(map[string]string{"AAA": "ETF"})

	deadline := time.After(3 * time.Second)
	for {
		select {
		case snap := <-eng.Snapshots():
			aaa := snap.Rows[snap.Index["AAA"]]
			bbb := snap.Rows[snap.Index["BBB"]]
			if aaa.SecType == "ETF" {
				if bbb.SecType != "" {
					t.Fatalf("BBB SecType = %q, want empty (absent from the map)", bbb.SecType)
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for AAA's SecType to update")
		}
	}
}

// TestCurveCaptureAndCollect proves the engine records cumulative volume at the
// current 5-min bucket and Curves() collects it (the self-built RVOL source).
func TestCurveCaptureAndCollect(t *testing.T) {
	fixed := time.Date(2026, 6, 23, 14, 0, 0, 0, time.UTC) // 10:00 ET -> bucket 72
	eng := New(Options{TickHz: 20, Now: func() time.Time { return fixed }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	eng.Seed(core.Seed{Agg: core.Agg{Sym: "AAA", Close: 10, High: 10, Low: 10, Vol: 100, EndMs: fixed.Unix() * 1000}})
	waitForSym(t, eng, "AAA", 3*time.Second) // ensures a snapshot ran -> recordCurve fired

	curves := eng.Curves(core.BucketCount - 1)
	cv, ok := curves["AAA"]
	if !ok || len(cv) != core.BucketCount {
		t.Fatalf("curve missing or wrong length: ok=%v len=%d", ok, len(cv))
	}
	if cv[72] != 100 {
		t.Fatalf("curve[72] (10:00 ET) want 100, got %d", cv[72])
	}
	if cv[0] != 0 {
		t.Fatalf("curve[0] (pre-start, unknown) want 0, got %d", cv[0])
	}
}

// TestRetainEvicts proves Retain removes tickers outside the kept set and
// decrements the tracked count (the warrant-leak fix).
func TestRetainEvicts(t *testing.T) {
	eng := New(Options{TickHz: 20, Now: func() time.Time { return time.Unix(1_700_000_000, 0) }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	eng.Seed(core.Seed{Agg: core.Agg{Sym: "AAA", Close: 10, High: 10, Low: 10, Vol: 1, EndMs: 1_700_000_000_000}})
	eng.Seed(core.Seed{Agg: core.Agg{Sym: "WARR", Close: 1, High: 1, Low: 1, Vol: 1, EndMs: 1_700_000_000_000}})
	waitForSym(t, eng, "WARR", 3*time.Second)

	eng.Retain(map[string]struct{}{"AAA": {}})

	deadline := time.After(3 * time.Second)
	for {
		select {
		case snap := <-eng.Snapshots():
			_, hasA := snap.Index["AAA"]
			_, hasW := snap.Index["WARR"]
			if hasA && !hasW {
				if got := eng.Tracked(); got != 1 {
					t.Fatalf("tracked after Retain want 1, got %d", got)
				}
				return
			}
		case <-deadline:
			t.Fatal("WARR was not evicted by Retain")
		}
	}
}

// TestSetSessionResetsScopeNotDay proves a sub-session boundary (regular -> post)
// resets session-scoped volume while the 04:00-anchored day volume carries through.
func TestSetSessionResetsScopeNotDay(t *testing.T) {
	fixed := time.Date(2026, 6, 23, 14, 0, 0, 0, time.UTC) // 10:00 ET
	eng := New(Options{TickHz: 50, Now: func() time.Time { return fixed }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	// SetSession and Apply travel on different channels (ctrl vs ingest), so the
	// aggregate can overtake the session change — and entering Regular resets
	// session scope, which would zero the very volume asserted below. Wait for
	// the session to actually land first so the ordering is deterministic.
	eng.SetSession(core.SessionRegular)
	waitForSession(t, eng, core.SessionRegular, 3*time.Second)

	eng.Apply(core.Agg{Sym: "AAA", Close: 10, High: 10, Low: 10, Vol: 100, EndMs: fixed.Unix() * 1000})

	// Regular session: scoped vol == day vol == 100.
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool { return r.SessVol == 100 && r.Vol == 100 })

	// Cross into post-market: scope resets, day volume holds. Waiting for this also
	// guarantees the reset is processed before the next Apply below (no race).
	eng.SetSession(core.SessionPost)
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool { return r.SessVol == 0 && r.Vol == 100 })

	// New post-session prints accrue only to the scoped volume; day vol keeps summing.
	eng.Apply(core.Agg{Sym: "AAA", Close: 11, High: 11, Low: 11, Vol: 30, EndMs: (fixed.Unix() + 1) * 1000})
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool { return r.SessVol == 30 && r.Vol == 130 })
}

// preMarketMove drives AAA through a pre-market move on a running engine:
// prevClose 10, first print 12, last 14 (high 15, low 11, vol 300). The first
// print deliberately differs from prevClose so the frozen preChg (+40 vs
// prevClose) and a wrong vs-first-print change (+16.7) are DIFFERENT numbers —
// asserting +40 is asserting the spec.
func preMarketMove(t *testing.T, eng *Engine, sec int64) {
	t.Helper()
	eng.SetSession(core.SessionPre)
	waitForSession(t, eng, core.SessionPre, 3*time.Second)
	eng.SeedClose("AAA", 10)
	eng.Apply(core.Agg{Sym: "AAA", Close: 12, High: 12, Low: 12, VWAP: 12, Vol: 100, EndMs: sec * 1000})
	eng.Apply(core.Agg{Sym: "AAA", Close: 14, High: 15, Low: 11, VWAP: 13, Vol: 200, EndMs: (sec + 1) * 1000})
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool { return r.SessVol == 300 })
}

// TestSetSessionFreezesPreAtRegularOpen proves the 09:30 boundary captures the
// outgoing pre-market scope into the pre* review fields BEFORE wiping it, and
// that preChg is the chgDay snapshot at the boundary (vs prevClose), not a
// change vs the session's own first print.
func TestSetSessionFreezesPreAtRegularOpen(t *testing.T) {
	fixed := time.Date(2026, 6, 23, 13, 0, 0, 0, time.UTC) // 09:00 ET, pre-market
	eng := New(Options{TickHz: 50, Now: func() time.Time { return fixed }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	preMarketMove(t, eng, fixed.Unix())

	eng.SetSession(core.SessionRegular) // 09:30 boundary: freeze, then wipe
	wantVwap := (12.0*100 + 13.0*200) / 300
	wantFade := (15.0 - 14.0) / 15.0 * 100 // off the HIGH 15, not off the 12 first print
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool {
		return r.SessVol == 0 && // live scope wiped for the new sub-session
			r.PreVol == 300 &&
			!core.IsNull(r.PreChg) && abs(r.PreChg-40) < 1e-3 && // vs prevClose 10, NOT +16.7 vs the 12 first print
			!core.IsNull(r.PreHigh) && abs(r.PreHigh-15) < 1e-3 &&
			!core.IsNull(r.PreLow) && abs(r.PreLow-11) < 1e-3 &&
			!core.IsNull(r.PreVwap) && abs(r.PreVwap-wantVwap) < 1e-3 &&
			!core.IsNull(r.PreGap) && abs(r.PreGap-50) < 1e-3 && // high 15 vs prevClose 10, NOT chg's +40
			!core.IsNull(r.PreFade) && abs(r.PreFade-wantFade) < 1e-3 &&
			core.IsNull(r.RthChg) && core.IsNull(r.RthGap) && core.IsNull(r.RthFade) &&
			r.RthVol == 0 // regular hasn't completed yet
	})
}

// TestSetSessionFreezesRthAndKeepsPre proves the 16:00 boundary freezes the
// regular session into rth* without disturbing the already-frozen pre* slot —
// both populated, with different values.
func TestSetSessionFreezesRthAndKeepsPre(t *testing.T) {
	fixed := time.Date(2026, 6, 23, 13, 0, 0, 0, time.UTC)
	eng := New(Options{TickHz: 50, Now: func() time.Time { return fixed }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	preMarketMove(t, eng, fixed.Unix())
	eng.SetSession(core.SessionRegular)
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool { return r.PreVol == 300 && r.SessVol == 0 })

	// Regular-session move: last 20 vs prevClose 10 -> rthChg +100, every
	// value distinct from the pre-market's.
	eng.Apply(core.Agg{Sym: "AAA", Close: 18, High: 21, Low: 17, VWAP: 19, Vol: 500, EndMs: (fixed.Unix() + 2) * 1000})
	eng.Apply(core.Agg{Sym: "AAA", Close: 20, High: 20, Low: 20, VWAP: 20, Vol: 100, EndMs: (fixed.Unix() + 3) * 1000})
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool { return r.SessVol == 600 })

	eng.SetSession(core.SessionPost) // 16:00 boundary
	wantRthVwap := (19.0*500 + 20.0*100) / 600
	wantRthFade := (21.0 - 20.0) / 21.0 * 100 // off the regular-session high 21
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool {
		return r.SessVol == 0 &&
			r.RthVol == 600 &&
			!core.IsNull(r.RthChg) && abs(r.RthChg-100) < 1e-3 &&
			!core.IsNull(r.RthHigh) && abs(r.RthHigh-21) < 1e-3 &&
			!core.IsNull(r.RthLow) && abs(r.RthLow-17) < 1e-3 &&
			!core.IsNull(r.RthVwap) && abs(r.RthVwap-wantRthVwap) < 1e-3 &&
			!core.IsNull(r.RthGap) && abs(r.RthGap-110) < 1e-3 && // high 21 vs prevClose 10
			!core.IsNull(r.RthFade) && abs(r.RthFade-wantRthFade) < 1e-3 &&
			// the pre-market slot is untouched by the 16:00 boundary
			r.PreVol == 300 && !core.IsNull(r.PreChg) && abs(r.PreChg-40) < 1e-3 &&
			!core.IsNull(r.PreGap) && abs(r.PreGap-50) < 1e-3 &&
			!core.IsNull(r.PreFade) && abs(r.PreFade-(15.0-14.0)/15.0*100) < 1e-3
	})
}

// TestSetSessionDayRolloverClearsFrozen proves entering Pre (a new extended
// day) performs the FULL reset and wipes the frozen review slots — yesterday's
// pre-market must not survive into today.
func TestSetSessionDayRolloverClearsFrozen(t *testing.T) {
	fixed := time.Date(2026, 6, 23, 13, 0, 0, 0, time.UTC)
	eng := New(Options{TickHz: 50, Now: func() time.Time { return fixed }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	preMarketMove(t, eng, fixed.Unix())
	eng.SetSession(core.SessionRegular)
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool { return r.PreVol == 300 })

	eng.SetSession(core.SessionPre) // next day, 04:00: full reset
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool {
		return r.PreVol == 0 && core.IsNull(r.PreChg) && core.IsNull(r.PreHigh) &&
			core.IsNull(r.PreLow) && core.IsNull(r.PreVwap) &&
			core.IsNull(r.PreGap) && core.IsNull(r.PreFade) &&
			r.RthVol == 0 && core.IsNull(r.RthChg) &&
			core.IsNull(r.RthGap) && core.IsNull(r.RthFade)
	})
}

// TestSetSessionClosedToRegularFreezesNothing proves the freeze is keyed on
// the OUTGOING session: an app started mid-day flips Closed -> Regular, and no
// session completed under our watch, so nothing may be frozen — even though
// the seeded ticker has live scope data the wipe is about to clear.
func TestSetSessionClosedToRegularFreezesNothing(t *testing.T) {
	fixed := time.Date(2026, 6, 23, 15, 0, 0, 0, time.UTC) // 11:00 ET, mid-day start
	eng := New(Options{TickHz: 50, Now: func() time.Time { return fixed }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	// Seeded while the engine still reads Closed (its construction default).
	eng.Seed(core.Seed{Agg: core.Agg{Sym: "AAA", Close: 14, High: 15, Low: 11, Vol: 300, EndMs: fixed.Unix() * 1000}, PrevClose: 10})
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool { return !core.IsNull(r.SessHigh) })

	eng.SetSession(core.SessionRegular) // old == Closed: scope wipes, nothing freezes
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool { return core.IsNull(r.SessHigh) })

	snap := waitForSym(t, eng, "AAA", 3*time.Second)
	r := snap.Rows[snap.Index["AAA"]]
	if !core.IsNull(r.PreChg) || !core.IsNull(r.PreHigh) || !core.IsNull(r.PreLow) ||
		!core.IsNull(r.PreVwap) || !core.IsNull(r.PreGap) || !core.IsNull(r.PreFade) || r.PreVol != 0 ||
		!core.IsNull(r.RthChg) || !core.IsNull(r.RthHigh) || !core.IsNull(r.RthLow) ||
		!core.IsNull(r.RthVwap) || !core.IsNull(r.RthGap) || !core.IsNull(r.RthFade) || r.RthVol != 0 {
		t.Fatalf("Closed->Regular must freeze nothing, got pre=(%v %v %v %v %v %v %d) rth=(%v %v %v %v %v %v %d)",
			r.PreChg, r.PreHigh, r.PreLow, r.PreVwap, r.PreGap, r.PreFade, r.PreVol,
			r.RthChg, r.RthHigh, r.RthLow, r.RthVwap, r.RthGap, r.RthFade, r.RthVol)
	}
}

// waitForSession blocks until the engine publishes a snapshot stamped with the
// given session, so tests can order a SetSession against later Apply calls.
func waitForSession(t *testing.T, eng *Engine, want core.Session, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case snap := <-eng.Snapshots():
			if snap.Sess == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for session %v", want)
			return
		}
	}
}

// waitForCond blocks until a snapshot's row for sym satisfies cond, failing on
// timeout.
func waitForCond(t *testing.T, eng *Engine, sym string, d time.Duration, cond func(core.Row) bool) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case snap := <-eng.Snapshots():
			if i, ok := snap.Index[sym]; ok && cond(snap.Rows[i]) {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s to satisfy condition", sym)
			return
		}
	}
}

func waitForSym(t *testing.T, eng *Engine, sym string, d time.Duration) *core.MetricSnapshot {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case snap := <-eng.Snapshots():
			if _, ok := snap.Index[sym]; ok {
				return snap
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s in a snapshot", sym)
			return nil
		}
	}
}
