package engine

import (
	"context"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// waitForSessionSnapshot reads the boundary-snapshot stream until a snapshot
// for the wanted (completed) session arrives, failing on timeout. Earlier
// boundaries' snapshots are skipped, not lost — a test that needs them reads
// before advancing the session again.
func waitForSessionSnapshot(t *testing.T, eng *Engine, want core.Session, d time.Duration) core.SessionSnapshot {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case snap := <-eng.SessionSnapshots():
			if snap.Session == want {
				return snap
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a %v session snapshot", want)
			return core.SessionSnapshot{}
		}
	}
}

// findSnapshotRow returns sym's row in snap, or ok=false.
func findSnapshotRow(snap core.SessionSnapshot, sym string) (core.SessionSnapshotRow, bool) {
	for _, r := range snap.Rows {
		if r.Sym == sym {
			return r, true
		}
	}
	return core.SessionSnapshotRow{}, false
}

// TestSetSessionEmitsPreSnapshotAtRegularOpen proves the 09:30 boundary emits
// a whole-market SessionSnapshot of the completed pre-market: RAW values (the
// scope state the freeze reads, no percentages), the ticker's own prevClose
// riding along, the ET trading date of the boundary, and never-traded tickers
// skipped entirely.
func TestSetSessionEmitsPreSnapshotAtRegularOpen(t *testing.T) {
	fixed := time.Date(2026, 6, 23, 13, 0, 0, 0, time.UTC) // 09:00 ET, pre-market
	eng := New(Options{TickHz: 50, Now: func() time.Time { return fixed }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	preMarketMove(t, eng, fixed.Unix()) // AAA: prevClose 10, hi 15, lo 11, close 14, vol 300
	eng.SeedClose("BBB", 5)             // tracked (prevClose known) but NEVER traded

	eng.SetSession(core.SessionRegular) // 09:30 boundary

	snap := waitForSessionSnapshot(t, eng, core.SessionPre, 3*time.Second)
	if snap.Date != "2026-06-23" {
		t.Fatalf("snapshot date want 2026-06-23 (ET trading date at the boundary), got %q", snap.Date)
	}
	row, ok := findSnapshotRow(snap, "AAA")
	if !ok {
		t.Fatalf("AAA missing from the pre snapshot: %+v", snap.Rows)
	}
	wantVwap := (12.0*100 + 13.0*200) / 300
	if abs(row.Hi-15) > 1e-6 || abs(row.Lo-11) > 1e-6 || abs(row.Close-14) > 1e-6 ||
		abs(row.Vwap-wantVwap) > 1e-6 || row.Vol != 300 || abs(row.PrevClose-10) > 1e-6 {
		t.Fatalf("AAA raw values mismatch: %+v", row)
	}
	if _, ok := findSnapshotRow(snap, "BBB"); ok {
		t.Fatalf("never-traded BBB must be skipped, got %+v", snap.Rows)
	}
	if len(snap.Rows) != 1 {
		t.Fatalf("want exactly 1 row (AAA), got %d: %+v", len(snap.Rows), snap.Rows)
	}
}

// TestSetSessionPostToClosedEmitsPostSnapshot is the whole point of the
// feature: the 20:00 Post→Closed boundary — which previously did nothing at
// all — must emit the after-hours session snapshot. The numbers are chosen so
// the three candidate baselines all differ: prevClose 10 (day change), the
// SAME day's regular close 20 (the correct after-hours baseline, carried by
// the earlier regular snapshot), and the post session's own first print 22.
// Only the raw values are persisted; a consumer deriving the after-hours gain
// from these rows gets (25-20)/20 = +25% — not +150% (vs prevClose) and not
// +13.6% (vs the post's first print). The boundary must also leave the live
// scoped values frozen (no reset on entering Closed), as before.
func TestSetSessionPostToClosedEmitsPostSnapshot(t *testing.T) {
	fixed := time.Date(2026, 6, 23, 13, 0, 0, 0, time.UTC)
	eng := New(Options{TickHz: 50, Now: func() time.Time { return fixed }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	// Pre-market: AAA moves (prevClose 10); CCC trades ONLY here.
	preMarketMove(t, eng, fixed.Unix())
	eng.SeedClose("CCC", 2)
	eng.Apply(core.Agg{Sym: "CCC", Close: 3, High: 3, Low: 3, VWAP: 3, Vol: 10, EndMs: fixed.Unix() * 1000})
	waitForCond(t, eng, "CCC", 3*time.Second, func(r core.Row) bool { return r.SessVol == 10 })

	eng.SetSession(core.SessionRegular)
	snapPre := waitForSessionSnapshot(t, eng, core.SessionPre, 3*time.Second)
	if _, ok := findSnapshotRow(snapPre, "CCC"); !ok {
		t.Fatalf("CCC traded pre-market, must be in the pre snapshot: %+v", snapPre.Rows)
	}

	// Regular session: AAA closes at 20 — the after-hours gain's true baseline.
	eng.Apply(core.Agg{Sym: "AAA", Close: 20, High: 21, Low: 15, VWAP: 18, Vol: 600, EndMs: (fixed.Unix() + 2) * 1000})
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool { return r.SessVol == 600 })

	eng.SetSession(core.SessionPost)
	snapReg := waitForSessionSnapshot(t, eng, core.SessionRegular, 3*time.Second)
	regRow, ok := findSnapshotRow(snapReg, "AAA")
	if !ok || abs(regRow.Close-20) > 1e-6 {
		t.Fatalf("regular snapshot must carry AAA's 20 close (the AH baseline): %+v ok=%v", regRow, ok)
	}
	if _, ok := findSnapshotRow(snapReg, "CCC"); ok {
		t.Fatalf("CCC did not trade the regular session, must be skipped: %+v", snapReg.Rows)
	}

	// After hours: first print 22 (≠ rth close 20 ≠ prevClose 10), close 25.
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool { return r.SessVol == 0 })
	eng.Apply(core.Agg{Sym: "AAA", Close: 22, High: 22, Low: 17, VWAP: 20, Vol: 400, EndMs: (fixed.Unix() + 4) * 1000})
	eng.Apply(core.Agg{Sym: "AAA", Close: 25, High: 26, Low: 25, VWAP: 25, Vol: 100, EndMs: (fixed.Unix() + 5) * 1000})
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool { return r.SessVol == 500 })

	eng.SetSession(core.SessionClosed) // 20:00 — the previously-missing boundary

	snapPost := waitForSessionSnapshot(t, eng, core.SessionPost, 3*time.Second)
	if snapPost.Date != "2026-06-23" {
		t.Fatalf("post snapshot date want 2026-06-23, got %q", snapPost.Date)
	}
	row, ok := findSnapshotRow(snapPost, "AAA")
	if !ok {
		t.Fatalf("AAA missing from the post snapshot: %+v", snapPost.Rows)
	}
	wantVwap := (20.0*400 + 25.0*100) / 500
	if abs(row.Hi-26) > 1e-6 || abs(row.Lo-17) > 1e-6 || abs(row.Close-25) > 1e-6 ||
		abs(row.Vwap-wantVwap) > 1e-6 || row.Vol != 500 || abs(row.PrevClose-10) > 1e-6 {
		t.Fatalf("AAA post raw values mismatch: %+v", row)
	}
	// The three baselines really are three different numbers.
	if row.PrevClose == regRow.Close || regRow.Close == 22.0 || row.Close == row.PrevClose {
		t.Fatalf("test setup degenerate: baselines must differ (prevClose %v, rth close %v, first print 22)",
			row.PrevClose, regRow.Close)
	}
	if _, ok := findSnapshotRow(snapPost, "CCC"); ok {
		t.Fatalf("CCC did not trade after hours, must be skipped: %+v", snapPost.Rows)
	}

	// Entering Closed still leaves the live scoped values frozen — the
	// collection pass must not have smuggled in a reset.
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool {
		return r.SessVol == 500 && !core.IsNull(r.SessHigh) && abs(r.SessHigh-26) < 1e-3
	})
}

// TestSetSessionFromClosedEmitsNothing proves the snapshot is keyed on the
// OUTGOING session: a mid-day start flips Closed→Regular, no market session
// completed under our watch, and no snapshot may be emitted — even though the
// seeded ticker has live scope data.
func TestSetSessionFromClosedEmitsNothing(t *testing.T) {
	fixed := time.Date(2026, 6, 23, 15, 0, 0, 0, time.UTC) // 11:00 ET mid-day start
	eng := New(Options{TickHz: 50, Now: func() time.Time { return fixed }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	eng.Seed(core.Seed{Agg: core.Agg{Sym: "AAA", Close: 14, High: 15, Low: 11, Vol: 300, EndMs: fixed.Unix() * 1000}, PrevClose: 10})
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool { return !core.IsNull(r.SessHigh) })

	eng.SetSession(core.SessionRegular) // old == Closed: wipe scope, emit nothing
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool { return core.IsNull(r.SessHigh) })

	select {
	case snap := <-eng.SessionSnapshots():
		t.Fatalf("Closed→Regular must emit no session snapshot, got %+v", snap)
	case <-time.After(300 * time.Millisecond):
	}
	if d := eng.SessionSnapshotDrops(); d != 0 {
		t.Fatalf("no snapshot was attempted, drops must be 0, got %d", d)
	}
}

// TestSessionSnapshotHandoffNeverBlocks wedges the consumer (nobody reads
// SessionSnapshots) and drives more boundaries than the channel buffers. Every
// SetSession must complete immediately, the shards must keep serving, and the
// overflow must be counted as drops — a lost snapshot is a gap in history, a
// stalled engine is an outage.
func TestSessionSnapshotHandoffNeverBlocks(t *testing.T) {
	fixed := time.Date(2026, 6, 23, 13, 0, 0, 0, time.UTC)
	eng := New(Options{TickHz: 50, Now: func() time.Time { return fixed }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	eng.SetSession(core.SessionPre)
	waitForSession(t, eng, core.SessionPre, 3*time.Second)
	eng.SeedClose("AAA", 10)
	eng.Apply(core.Agg{Sym: "AAA", Close: 12, High: 12, Low: 12, VWAP: 12, Vol: 100, EndMs: fixed.Unix() * 1000})
	waitForCond(t, eng, "AAA", 3*time.Second, func(r core.Row) bool { return r.SessVol == 100 })

	// 7 boundaries whose OUTGOING session is a market session; the channel
	// buffers 4, so exactly 3 must drop. The consumer is deliberately wedged.
	boundaries := []core.Session{
		core.SessionRegular, core.SessionPost, core.SessionClosed, // day 1: 3 collects
		core.SessionPre,                                           // Closed→Pre: no collect (full reset)
		core.SessionRegular, core.SessionPost, core.SessionClosed, // day 2: 3 more
		core.SessionPre,     // no collect
		core.SessionRegular, // 7th collect
	}
	for _, s := range boundaries {
		done := make(chan struct{})
		go func(s core.Session) { eng.SetSession(s); close(done) }(s)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("SetSession(%v) blocked with a wedged snapshot consumer", s)
		}
	}

	// The engine itself must still be alive and serving snapshots.
	waitForSym(t, eng, "AAA", 3*time.Second)

	deadline := time.Now().Add(3 * time.Second)
	for {
		if d := eng.SessionSnapshotDrops(); d == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("want exactly 3 counted drops (7 boundary snapshots, buffer 4), got %d", eng.SessionSnapshotDrops())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The 4 buffered snapshots are still deliverable once the consumer wakes.
	for i := 0; i < 4; i++ {
		select {
		case <-eng.SessionSnapshots():
		case <-time.After(time.Second):
			t.Fatalf("buffered snapshot %d never arrived after the consumer woke", i)
		}
	}
}
