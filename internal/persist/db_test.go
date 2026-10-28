package persist

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"

	_ "modernc.org/sqlite"
)

// TestMigrateAddsColumnToExistingTable is the exact scenario the user's live
// database was in: scan_entries already existed (from before has_news shipped)
// without the column. CREATE TABLE IF NOT EXISTS is a no-op against it, so
// ensureColumn's ALTER TABLE path is what has to actually add it — this
// reproduces that by hand-creating the pre-migration table first.
func TestMigrateAddsColumnToExistingTable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `
CREATE TABLE scan_entries (
  ts      INTEGER NOT NULL,
  sym     TEXT NOT NULL,
  scan_id TEXT NOT NULL,
  last    REAL
)`); err != nil {
		t.Fatalf("seed pre-migration table: %v", err)
	}
	if _, err := raw.ExecContext(ctx,
		`INSERT INTO scan_entries (ts, sym, scan_id, last) VALUES (1000, 'OLD', 's1', 5.0)`); err != nil {
		t.Fatalf("seed pre-migration row: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open should migrate an existing table, not fail: %v", err)
	}
	defer db.Close()

	// Pre-existing row must survive with the new column defaulted to false.
	var sym string
	var hasNews int
	if err := db.sql.QueryRowContext(ctx,
		`SELECT sym, has_news FROM scan_entries WHERE sym = 'OLD'`).Scan(&sym, &hasNews); err != nil {
		t.Fatalf("pre-existing row unreadable after migration: %v", err)
	}
	if sym != "OLD" || hasNews != 0 {
		t.Fatalf("pre-existing row: want (OLD, 0), got (%s, %d)", sym, hasNews)
	}

	// And the column must actually be writable through the real repo now.
	archive := db.NewsArchive()
	if err := archive.SaveEntry(ctx, core.ScanEntry{ScanID: "s1", Sym: "NEW", At: 2000, Last: 9, HasNews: true}); err != nil {
		t.Fatalf("SaveEntry after migration: %v", err)
	}
	if err := db.sql.QueryRowContext(ctx,
		`SELECT has_news FROM scan_entries WHERE sym = 'NEW'`).Scan(&hasNews); err != nil {
		t.Fatalf("read back new row: %v", err)
	}
	if hasNews != 1 {
		t.Fatalf("want has_news=1 for NEW, got %d", hasNews)
	}

	// Migrating an already-migrated table must stay a no-op (idempotent).
	if err := migrate(ctx, db.sql); err != nil {
		t.Fatalf("re-running migrate on an already-migrated db: %v", err)
	}
}

// TestMigrateAddsScoreColumnsToExistingArticlesTable is the same scenario for
// the Impact Score feature: news_articles existed (from the news-archive
// feature) before impact_score/impact_tier/event_type/impact_direction
// shipped, on the user's actual running database.
func TestMigrateAddsScoreColumnsToExistingArticlesTable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old2.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `
CREATE TABLE news_articles (
  id TEXT PRIMARY KEY, headline TEXT NOT NULL, source TEXT, url TEXT,
  teaser TEXT, body TEXT, tickers TEXT,
  published_ms INTEGER NOT NULL, received_ms INTEGER NOT NULL, via TEXT NOT NULL
)`); err != nil {
		t.Fatalf("seed pre-migration table: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `
INSERT INTO news_articles (id, headline, source, url, teaser, body, tickers, published_ms, received_ms, via)
VALUES ('old1', 'h', 's', 'u', '', '', 'BMRA', 1000, 1000, 'poll')`); err != nil {
		t.Fatalf("seed pre-migration row: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open should migrate an existing table, not fail: %v", err)
	}
	defer db.Close()

	archive := db.NewsArchive()
	got, _, ok, err := archive.Article(ctx, "old1")
	if err != nil || !ok {
		t.Fatalf("pre-existing row unreadable after migration: ok=%v err=%v", ok, err)
	}
	if got.Score != nil {
		t.Fatalf("pre-existing row should read as unscored, got %v", *got.Score)
	}
	if ok, err := archive.SetArticleScore(ctx, "old1", 77, "med", "contract_win", "bullish"); err != nil || !ok {
		t.Fatalf("SetArticleScore after migration: ok=%v err=%v", ok, err)
	}
}
