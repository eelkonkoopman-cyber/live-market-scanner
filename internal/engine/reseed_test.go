package engine

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// A reconnect re-seed races the live stream: the REST snapshot is older than
// aggregates that may have already applied. The stale snapshot must never win.
func TestStaleReseedDoesNotRegressLiveState(t *testing.T) {
	ts := &tickerState{sym: "X"}

	// Live agg first: price 10 at t0+1.
	ts.applyAgg(core.Agg{Sym: "X", Close: 10, High: 10.5, Low: 9.9, Vol: 50, EndMs: (t0 + 1) * 1000})

	// Snapshot taken at t0 (older) lands second — the lost race.
	ts.seedApply(core.Seed{
		Agg:       core.Agg{Sym: "X", Close: 9, High: 9.6, Low: 8.5, Vol: 1000, EndMs: t0 * 1000},
		PrevClose: 8,
	})

	if ts.last != 10 {
		t.Fatalf("stale seed must not regress last: want 10 got %v", ts.last)
	}
	if ts.lastTS != (t0+1)*1000 {
		t.Fatalf("stale seed must not regress lastTS: got %d", ts.lastTS)
	}
	// Day extremes are the UNION of snapshot and live: hod from the live spike,
	// lod from the snapshot's earlier low.
	if ts.hod != 10.5 {
		t.Fatalf("hod: want live 10.5, got %v", ts.hod)
	}
	if ts.lod != 8.5 {
		t.Fatalf("lod: want snapshot 8.5, got %v", ts.lod)
	}
	// Snapshot day-volume still SETS (documented double-count guard)...
	if ts.sessVol != 1000 {
		t.Fatalf("sessVol: want snapshot 1000, got %d", ts.sessVol)
	}
	// ...and day-constants still apply.
	if ts.prevClose != 8 {
		t.Fatalf("prevClose: want 8, got %v", ts.prevClose)
	}
}

// The normal path — seed first, then live aggs — must be unchanged.
func TestFreshSeedStillApplies(t *testing.T) {
	ts := &tickerState{sym: "X"}
	ts.seedApply(core.Seed{
		Agg:       core.Agg{Sym: "X", Close: 9, High: 9.6, Low: 8.5, Vol: 1000, EndMs: t0 * 1000},
		PrevClose: 8,
	})
	if ts.last != 9 || ts.sessVol != 1000 || ts.hod != 9.6 || ts.lod != 8.5 {
		t.Fatalf("fresh seed should apply fully: last=%v vol=%d hod=%v lod=%v",
			ts.last, ts.sessVol, ts.hod, ts.lod)
	}
	ts.applyAgg(core.Agg{Sym: "X", Close: 10, High: 10, Low: 10, Vol: 50, EndMs: (t0 + 1) * 1000})
	if ts.last != 10 {
		t.Fatalf("live agg after seed: want 10 got %v", ts.last)
	}
}
