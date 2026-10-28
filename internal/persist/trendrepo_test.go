package persist

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// bar is a terse close-only DailyBar for the cases that only exercise ordering
// / replacement semantics (the OHLCV values themselves are pinned separately by
// TestTrendRepoOHLCVRoundTrip).
func bar(date string, c float64) core.DailyBar { return core.DailyBar{Date: date, C: c} }

func TestTrendRepoRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	r := db.TrendRepo()

	if has, _ := r.HasClosesForDate(ctx, "2026-07-25"); has {
		t.Fatal("empty table must report no closes")
	}
	// Two dates; the batched upsert path must survive an odd map too.
	if err := r.UpsertDailyBars(ctx, "2026-07-25", map[string]core.DailyBar{
		"AAA": bar("2026-07-25", 10), "BBB": bar("2026-07-25", 5),
	}); err != nil {
		t.Fatalf("upsert d1: %v", err)
	}
	if err := r.UpsertDailyBars(ctx, "2026-07-26", map[string]core.DailyBar{
		"AAA": bar("2026-07-26", 11), "BBB": bar("2026-07-26", 4.5), "CCC": bar("2026-07-26", 1),
	}); err != nil {
		t.Fatalf("upsert d2: %v", err)
	}
	// Re-upsert same date replaces, not duplicates.
	if err := r.UpsertDailyBars(ctx, "2026-07-26", map[string]core.DailyBar{
		"AAA": bar("2026-07-26", 11.5),
	}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	if has, _ := r.HasClosesForDate(ctx, "2026-07-25"); !has {
		t.Fatal("date 1 should exist")
	}
	byT, err := r.LoadBars(ctx, "2026-07-25")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// AAA: [10, 11.5] date-ascending (11 replaced by 11.5).
	if got := byT["AAA"]; len(got) != 2 || got[0].C != 10 || got[1].C != 11.5 {
		t.Fatalf("AAA closes want [10 11.5], got %v", got)
	}
	if got := byT["CCC"]; len(got) != 1 || got[0].C != 1 {
		t.Fatalf("CCC closes want [1], got %v", got)
	}
	// Since-bound excludes earlier dates.
	byT2, _ := r.LoadBars(ctx, "2026-07-26")
	if got := byT2["AAA"]; len(got) != 1 || got[0].C != 11.5 {
		t.Fatalf("since-bound: AAA want [11.5], got %v", got)
	}

	if n, err := r.PruneDailyCloses(ctx, "2026-07-26"); err != nil || n != 2 {
		t.Fatalf("prune want 2 deleted (AAA+BBB @25), got n=%d err=%v", n, err)
	}
}

// The full OHLCV round-trip: every widened column must survive the write and
// come back on the right field. Column ORDER is the trap here — the insert
// lists close before open/high/low (close came first historically) while the
// select lists them o,h,l,c, so a mismatched Scan would silently swap values
// that all look like plausible prices.
func TestTrendRepoOHLCVRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	r := db.TrendRepo()

	// Deliberately all-distinct values so a transposed field can't pass.
	want := core.DailyBar{Date: "2026-07-27", O: 76.32, H: 79.25, L: 75.11, C: 77.85, V: 868021}
	if err := r.UpsertDailyBars(ctx, "2026-07-27", map[string]core.DailyBar{"BFAM": want}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	byT, err := r.LoadBars(ctx, "2026-07-27")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := byT["BFAM"]
	if len(got) != 1 {
		t.Fatalf("want 1 bar for BFAM, got %v", got)
	}
	if got[0] != want {
		t.Fatalf("OHLCV round-trip mismatch:\n got %+v\nwant %+v", got[0], want)
	}
}

// LoadBars documents trade-date ASCENDING order, and the pattern math depends
// on it completely (a pole is only a pole if it comes BEFORE the base). Insert
// out of order to prove the ordering comes from the query, not from luck.
func TestTrendRepoLoadBarsAscendingOrder(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	r := db.TrendRepo()

	dates := []string{"2026-07-24", "2026-07-20", "2026-07-27", "2026-07-22", "2026-07-21"}
	for i, d := range dates {
		if err := r.UpsertDailyBars(ctx, d, map[string]core.DailyBar{
			"AAA": bar(d, float64(10+i)), // close deliberately unordered too
		}); err != nil {
			t.Fatalf("upsert %s: %v", d, err)
		}
	}

	byT, err := r.LoadBars(ctx, "2026-07-01")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := byT["AAA"]
	wantDates := []string{"2026-07-20", "2026-07-21", "2026-07-22", "2026-07-24", "2026-07-27"}
	if len(got) != len(wantDates) {
		t.Fatalf("want %d bars, got %d: %+v", len(wantDates), len(got), got)
	}
	for i, wd := range wantDates {
		if got[i].Date != wd {
			t.Fatalf("bars must be trade-date ASCENDING: position %d is %s, want %s (full: %+v)",
				i, got[i].Date, wd, got)
		}
	}
}

// daily_close_history shipped close-only. An existing database is migrated with
// ensureColumn, NOT recreated, so its rows must still load — as bars whose OHLV
// is zero — rather than vanishing with the old table.
func TestTrendRepoMigrationPreservesCloseOnlyRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")

	// Recreate the pre-OHLCV table exactly as it shipped, and put a row in it.
	h, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy handle: %v", err)
	}
	if _, err := h.ExecContext(ctx, `CREATE TABLE daily_close_history (
  ticker     TEXT NOT NULL,
  trade_date TEXT NOT NULL,
  close      REAL NOT NULL,
  PRIMARY KEY (ticker, trade_date)
) WITHOUT ROWID`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if _, err := h.ExecContext(ctx,
		`INSERT INTO daily_close_history (ticker, trade_date, close) VALUES ('OLD','2026-07-20',12.5)`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("close legacy handle: %v", err)
	}

	db, err := Open(ctx, path) // runs migrate -> ensureColumn adds open/high/low/volume
	if err != nil {
		t.Fatalf("open must migrate a close-only database in place: %v", err)
	}
	defer db.Close()

	byT, err := db.TrendRepo().LoadBars(ctx, "2026-07-01")
	if err != nil {
		t.Fatalf("load after migration: %v", err)
	}
	got := byT["OLD"]
	if len(got) != 1 {
		t.Fatalf("the pre-existing close-only row must survive the migration, got %+v", byT)
	}
	if want := (core.DailyBar{Date: "2026-07-20", C: 12.5}); got[0] != want {
		t.Fatalf("migrated row must keep its close and read back zero OHLV:\n got %+v\nwant %+v", got[0], want)
	}
}

// TestTrendRepoLargeBatch drives the multi-row insert batching across the
// batch boundary (400) — the real nightly write is ~12k rows.
func TestTrendRepoLargeBatch(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	r := db.TrendRepo()

	bars := make(map[string]core.DailyBar, 1001)
	for i := 0; i < 1001; i++ {
		bars[fmtSym(i)] = bar("2026-07-27", float64(i)+0.5)
	}
	if err := r.UpsertDailyBars(ctx, "2026-07-27", bars); err != nil {
		t.Fatalf("bulk upsert: %v", err)
	}
	byT, err := r.LoadBars(ctx, "2026-07-27")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(byT) != 1001 {
		t.Fatalf("want 1001 tickers, got %d", len(byT))
	}
	if got := byT[fmtSym(777)]; len(got) != 1 || got[0].C != 777.5 {
		t.Fatalf("spot check sym 777: %v", got)
	}
}

// BarsFor is the per-symbol read path behind a Claude setup read. It must make
// the SAME trade-date ASCENDING guarantee LoadBars does (the prompt renders the
// series oldest→newest, and a reversed one would describe the pole and base
// backwards), and it must return ONLY the asked-for ticker — the whole point is
// not paying for the other ~12k.
func TestTrendRepoBarsForSymbolAndAscendingOrder(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	r := db.TrendRepo()

	// Inserted out of date order, with a second ticker interleaved on every
	// date, so neither the ordering nor the filter can pass by luck.
	dates := []string{"2026-07-24", "2026-07-20", "2026-07-27", "2026-07-22", "2026-07-21"}
	for i, d := range dates {
		if err := r.UpsertDailyBars(ctx, d, map[string]core.DailyBar{
			"AAA": bar(d, float64(10+i)),
			"ZZZ": bar(d, float64(90+i)),
		}); err != nil {
			t.Fatalf("upsert %s: %v", d, err)
		}
	}

	got, err := r.BarsFor(ctx, "AAA", "2026-07-01")
	if err != nil {
		t.Fatalf("barsfor: %v", err)
	}
	wantDates := []string{"2026-07-20", "2026-07-21", "2026-07-22", "2026-07-24", "2026-07-27"}
	if len(got) != len(wantDates) {
		t.Fatalf("want %d bars for AAA only, got %d: %+v", len(wantDates), len(got), got)
	}
	for i, wd := range wantDates {
		if got[i].Date != wd {
			t.Fatalf("bars must be trade-date ASCENDING: position %d is %s, want %s (full: %+v)",
				i, got[i].Date, wd, got)
		}
	}
	// AAA's closes were 10..14 by insertion order, ZZZ's 90..94 — any leaked
	// row from the other ticker shows up as a close in the 90s.
	for _, b := range got {
		if b.C >= 90 {
			t.Fatalf("BarsFor leaked another ticker's bar: %+v (full: %+v)", b, got)
		}
	}

	// sinceDate bounds the series the same way LoadBars does.
	bounded, err := r.BarsFor(ctx, "AAA", "2026-07-22")
	if err != nil {
		t.Fatalf("barsfor bounded: %v", err)
	}
	if len(bounded) != 3 || bounded[0].Date != "2026-07-22" || bounded[2].Date != "2026-07-27" {
		t.Fatalf("since-bound: want [22 24 27], got %+v", bounded)
	}

	// An unknown ticker is empty, not an error — the caller just has no bars.
	none, err := r.BarsFor(ctx, "NOPE", "2026-07-01")
	if err != nil {
		t.Fatalf("barsfor unknown ticker must not error: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("unknown ticker must return no bars, got %+v", none)
	}
}

// The full OHLCV fidelity check for the per-symbol path: BarsFor has its own
// column list and Scan, so a transposed field here would not be caught by
// TestTrendRepoOHLCVRoundTrip (which only exercises LoadBars).
func TestTrendRepoBarsForOHLCVRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	r := db.TrendRepo()

	want := core.DailyBar{Date: "2026-07-27", O: 76.32, H: 79.25, L: 75.11, C: 77.85, V: 868021}
	if err := r.UpsertDailyBars(ctx, "2026-07-27", map[string]core.DailyBar{"BFAM": want}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := r.BarsFor(ctx, "BFAM", "2026-07-01")
	if err != nil {
		t.Fatalf("barsfor: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 bar for BFAM, got %+v", got)
	}
	if got[0] != want {
		t.Fatalf("OHLCV round-trip mismatch:\n got %+v\nwant %+v", got[0], want)
	}
}

func fmtSym(i int) string {
	// A0000-style synthetic tickers.
	const digits = "0123456789"
	return "A" + string([]byte{digits[i/1000%10], digits[i/100%10], digits[i/10%10], digits[i%10]})
}
