package persist

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

// TestWatchlistReplaceRoundTrip proves whole-list replace semantics: save,
// list back in pos order, replace with a different list (old rows gone), and
// clear with an empty list.
func TestWatchlistReplaceRoundTrip(t *testing.T) {
	ctx := context.Background()
	r := openTest(t).WatchlistRepo()

	if got, err := r.List(ctx); err != nil || len(got) != 0 {
		t.Fatalf("fresh watchlist should be empty: %v err=%v", got, err)
	}

	first := []string{"TSLA", "AAPL", "NVDA"} // deliberately not alphabetical: pos order must win
	if err := r.Replace(ctx, first); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got, err := r.List(ctx); err != nil || !reflect.DeepEqual(got, first) {
		t.Fatalf("list after replace: %v err=%v", got, err)
	}

	second := []string{"AMD", "NVDA"}
	if err := r.Replace(ctx, second); err != nil {
		t.Fatalf("replace2: %v", err)
	}
	if got, err := r.List(ctx); err != nil || !reflect.DeepEqual(got, second) {
		t.Fatalf("whole-list replace left stale rows: %v err=%v", got, err)
	}

	if err := r.Replace(ctx, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got, err := r.List(ctx); err != nil || len(got) != 0 {
		t.Fatalf("clear left rows: %v err=%v", got, err)
	}
}

// TestWatchlistSurvivesReopen proves the list — and its user ordering — comes
// back after a full Close/Open cycle on the same database file (a restart).
func TestWatchlistSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wl.db")

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	want := []string{"NVDA", "AMD", "TSLA", "AAPL"} // non-alphabetical: ordering is by pos
	if err := db.WatchlistRepo().Replace(ctx, want); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	got, err := db2.WatchlistRepo().List(ctx)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("watchlist did not survive reopen in order: %v err=%v", got, err)
	}
}
