package persist

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func newSetupArchive(t *testing.T) core.SetupArchive {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db.SetupArchive()
}

func TestSetupArchiveRoundTrip(t *testing.T) {
	ctx := context.Background()
	r := newSetupArchive(t)

	want := core.SetupRead{
		Sym: "ACME", Ts: 1764000000000, Quality: 4, Verdict: "constructive",
		WhatsHappening: "Tight three-week base holding the upper third of a 45% pole.",
		Risks:          []string{"earnings in 4 days", "float is thin above 12.80"},
		Invalidation:   "loses 11.20 on volume",
		Model:          "claude-x", AsOf: "2026-07-30",
	}
	if err := r.SaveSetupRead(ctx, want); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, ok, err := r.GetSetupRead(ctx, "ACME", "2026-07-30")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.Sym != want.Sym || got.Ts != want.Ts || got.Quality != want.Quality ||
		got.Verdict != want.Verdict || got.WhatsHappening != want.WhatsHappening ||
		got.Invalidation != want.Invalidation || got.Model != want.Model ||
		got.AsOf != want.AsOf || got.Err != "" {
		t.Fatalf("scalar round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}
	// The Risks slice is the only JSON-encoded column — order and contents both.
	if len(got.Risks) != 2 || got.Risks[0] != "earnings in 4 days" || got.Risks[1] != "float is thin above 12.80" {
		t.Fatalf("risks json round-trip mismatch: %+v", got.Risks)
	}

	// A failed read (Quality 0, Err set, no risks) must survive too — it is
	// what the worker stores when Claude errors, and reading it back is how a
	// retry decision gets made without spending a second call to find out.
	fail := core.SetupRead{
		Sym: "BETA", Ts: 1764000001000, Quality: 0, Verdict: "unrated",
		WhatsHappening: "BETA coiling: score 82, pole +45.2%.",
		Model:          "claude-x", Err: "Claude returned unparseable output.", AsOf: "2026-07-30",
	}
	if err := r.SaveSetupRead(ctx, fail); err != nil {
		t.Fatalf("save failed-read: %v", err)
	}
	gotFail, ok, err := r.GetSetupRead(ctx, "BETA", "2026-07-30")
	if err != nil || !ok {
		t.Fatalf("get failed-read: ok=%v err=%v", ok, err)
	}
	if gotFail.Quality != 0 || gotFail.Err != "Claude returned unparseable output." || len(gotFail.Risks) != 0 {
		t.Fatalf("failed-read round-trip mismatch: %+v", gotFail)
	}
}

// TestSetupArchiveGetMissAndAsOfKey pins the cache semantics the whole cost
// control rests on: an unknown symbol misses, and — the one that actually
// matters — a DIFFERENT as_of for a symbol we already have a read for is ALSO a
// miss. If as_of were ignored, every fresh nightly classification would silently
// serve yesterday's read of geometry that no longer exists.
func TestSetupArchiveGetMissAndAsOfKey(t *testing.T) {
	ctx := context.Background()
	r := newSetupArchive(t)

	if _, ok, err := r.GetSetupRead(ctx, "NOPE", "2026-07-30"); err != nil || ok {
		t.Fatalf("unknown symbol must miss: ok=%v err=%v", ok, err)
	}

	if err := r.SaveSetupRead(ctx, core.SetupRead{
		Sym: "ACME", Ts: 1000, Quality: 4, Verdict: "constructive",
		WhatsHappening: "yesterday's base", Model: "claude-x", AsOf: "2026-07-30",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Same symbol, the NEXT night's classification: must be a miss.
	if _, ok, err := r.GetSetupRead(ctx, "ACME", "2026-07-31"); err != nil || ok {
		t.Fatalf("a new as_of for a known symbol must MISS (fresh geometry earns a fresh read): ok=%v err=%v", ok, err)
	}
	// Same as_of, a different symbol: also a miss.
	if _, ok, err := r.GetSetupRead(ctx, "BETA", "2026-07-30"); err != nil || ok {
		t.Fatalf("a different symbol on a known as_of must miss: ok=%v err=%v", ok, err)
	}
	// And the exact pair still hits, so the misses above aren't a broken query.
	got, ok, err := r.GetSetupRead(ctx, "ACME", "2026-07-30")
	if err != nil || !ok || got.WhatsHappening != "yesterday's base" {
		t.Fatalf("exact (sym, asOf) must hit: ok=%v err=%v got=%+v", ok, err, got)
	}
}

// TestSetupArchiveSaveTwiceUpserts: (sym, as_of) is the PRIMARY KEY, so a second
// save for the same nightly classification must REPLACE — not error on the
// constraint, and not leave two rows for one key that Recent would show twice
// and Get would pick between arbitrarily.
func TestSetupArchiveSaveTwiceUpserts(t *testing.T) {
	ctx := context.Background()
	r := newSetupArchive(t)

	first := core.SetupRead{
		Sym: "ACME", Ts: 1000, Quality: 0, Verdict: "unrated",
		WhatsHappening: "deterministic fallback line", Risks: []string{"stale risk"},
		Model: "claude-x", Err: "Claude request failed: timeout", AsOf: "2026-07-30",
	}
	if err := r.SaveSetupRead(ctx, first); err != nil {
		t.Fatalf("save first: %v", err)
	}
	second := core.SetupRead{
		Sym: "ACME", Ts: 2000, Quality: 5, Verdict: "textbook",
		WhatsHappening: "retried and got a real read", Risks: []string{"gap risk"},
		Invalidation: "loses 11.20", Model: "claude-x", AsOf: "2026-07-30",
	}
	if err := r.SaveSetupRead(ctx, second); err != nil {
		t.Fatalf("re-saving the same (sym, asOf) must upsert, not error: %v", err)
	}

	got, ok, err := r.GetSetupRead(ctx, "ACME", "2026-07-30")
	if err != nil || !ok {
		t.Fatalf("get after upsert: ok=%v err=%v", ok, err)
	}
	if got.Ts != 2000 || got.Quality != 5 || got.Verdict != "textbook" ||
		got.WhatsHappening != "retried and got a real read" || got.Invalidation != "loses 11.20" {
		t.Fatalf("upsert must overwrite every column: %+v", got)
	}
	// Every column, including the ones the newer read CLEARS.
	if got.Err != "" {
		t.Fatalf("upsert must clear the previous error, got %q", got.Err)
	}
	if len(got.Risks) != 1 || got.Risks[0] != "gap risk" {
		t.Fatalf("upsert must replace the risks slice, got %+v", got.Risks)
	}

	all, err := r.RecentSetupReads(ctx, "ACME", 10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("one (sym, asOf) must be exactly one row, got %d: %+v", len(all), all)
	}
}

// TestSetupArchiveRecentOrderLimitAndSymFilter covers what Recent must get right
// beyond field fidelity: newest-first ordering, per-symbol filtering, the limit,
// and the <=0 default.
func TestSetupArchiveRecentOrderLimitAndSymFilter(t *testing.T) {
	ctx := context.Background()
	r := newSetupArchive(t)

	mk := func(sym, asOf string, ts int64) core.SetupRead {
		return core.SetupRead{Sym: sym, AsOf: asOf, Ts: ts, Quality: 3, Verdict: "constructive", Model: "claude-x"}
	}
	// Inserted out of ts order so the ordering has to come from the query.
	for _, rd := range []core.SetupRead{
		mk("ACME", "2026-07-29", 1000),
		mk("ACME", "2026-07-31", 3000),
		mk("ACME", "2026-07-30", 2000),
		mk("BETA", "2026-07-30", 1500),
	} {
		if err := r.SaveSetupRead(ctx, rd); err != nil {
			t.Fatalf("save %s/%s: %v", rd.Sym, rd.AsOf, err)
		}
	}

	got, err := r.RecentSetupReads(ctx, "ACME", 10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	wantAsOf := []string{"2026-07-31", "2026-07-30", "2026-07-29"}
	if len(got) != len(wantAsOf) {
		t.Fatalf("want %d ACME reads (BETA must not leak), got %d: %+v", len(wantAsOf), len(got), got)
	}
	for i, w := range wantAsOf {
		if got[i].AsOf != w {
			t.Fatalf("reads must be newest-first: position %d is %s, want %s (full: %+v)", i, got[i].AsOf, w, got)
		}
	}

	betaGot, err := r.RecentSetupReads(ctx, "BETA", 10)
	if err != nil {
		t.Fatalf("recent beta: %v", err)
	}
	if len(betaGot) != 1 || betaGot[0].AsOf != "2026-07-30" {
		t.Fatalf("sym filter leaked across symbols: %+v", betaGot)
	}

	limited, err := r.RecentSetupReads(ctx, "ACME", 2)
	if err != nil {
		t.Fatalf("recent limited: %v", err)
	}
	if len(limited) != 2 || limited[0].AsOf != "2026-07-31" || limited[1].AsOf != "2026-07-30" {
		t.Fatalf("limit=2 should keep the two newest rows, got %+v", limited)
	}

	// limit <= 0 falls back to the default rather than returning nothing.
	dflt, err := r.RecentSetupReads(ctx, "ACME", 0)
	if err != nil {
		t.Fatalf("recent default limit: %v", err)
	}
	if len(dflt) != 3 {
		t.Fatalf("limit<=0 must apply the default limit, got %d rows", len(dflt))
	}
}
