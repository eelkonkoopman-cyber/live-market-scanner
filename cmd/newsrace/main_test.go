package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/newsfilter"
	"github.com/eelkonkoopman/marketscanner/internal/persist"
)

var now = time.Date(2026, 10, 30, 15, 0, 0, 0, time.UTC)

// at is a UTC time on the test day, in epoch ms.
func at(h, m, s int) int64 {
	return time.Date(2026, 10, 30, h, m, s, 0, time.UTC).UnixMilli()
}

// fixture builds a real scanner database (through internal/persist, so the
// stored shapes are the real ones) and the Newsfilter day file.
type fixture struct {
	db, dir string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	f := fixture{db: filepath.Join(root, "scanner.db"), dir: filepath.Join(root, "newsrace")}
	ctx := context.Background()

	d, err := persist.Open(ctx, f.db)
	if err != nil {
		t.Fatal(err)
	}
	arch := d.NewsArchive()
	save := func(via string, recv int64, it core.NewsItem) {
		t.Helper()
		if err := arch.SaveArticles(ctx, []core.NewsItem{it}, nil, via, recv); err != nil {
			t.Fatal(err)
		}
	}
	save("rtpr", at(10, 0, 5), core.NewsItem{ID: "rtprA", Headline: "Alpha Corp Announces Results", Source: "Business Wire", Tickers: []string{"LOW"}, TS: at(10, 0, 0)})
	save("rtpr", at(11, 0, 1), core.NewsItem{ID: "rtprB", Headline: "Beta Inc Declares Dividend", Source: "PR Newswire", Tickers: []string{"HIGH", "OTHER"}, TS: at(11, 0, 0)})
	save("rtpr", at(12, 0, 3), core.NewsItem{ID: "rtprC", Headline: "Gamma Only At RTPR", Source: "GlobeNewswire", Tickers: []string{"LOW"}, TS: at(12, 0, 0)})
	save("rtpr", at(12, 30, 1), core.NewsItem{ID: "rtprE", Headline: "Echo Newsfile Release", Source: "Newsfile Corp", Tickers: []string{"ECHO"}, TS: at(12, 30, 0)})
	save("poll", at(13, 0, 1), core.NewsItem{ID: "bz1", Headline: "Poll Only Story", Source: "Benzinga", Tickers: []string{"LOW"}, TS: at(13, 0, 0)})
	old := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC).UnixMilli()
	save("rtpr", old+5000, core.NewsItem{ID: "rtprOld", Headline: "Ancient History", Source: "Business Wire", Tickers: []string{"LOW"}, TS: old})
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	// Prices go in through a plain connection: LOW's latest close is 5 (not the
	// older 50), HIGH's is 80.
	h, err := sql.Open("sqlite", f.db)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		t, d string
		c    float64
	}{{"LOW", "2026-10-28", 50}, {"LOW", "2026-10-29", 5}, {"HIGH", "2026-10-29", 80}} {
		if _, err := h.Exec(`INSERT INTO daily_close_history (ticker, trade_date, close) VALUES (?, ?, ?)`, r.t, r.d, r.c); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}

	var lines []string
	add := func(v any) {
		b, _ := json.Marshal(v)
		lines = append(lines, string(b))
	}
	add(newsfilter.Event{Ev: newsfilter.EvConnected, Ms: at(9, 0, 0), URL: "wss"})
	add(newsfilter.Record{Ms: at(10, 0, 2), ID: "nfA", Pub: at(10, 0, 0), Src: "businesswire", Title: "Alpha Corp Announces Results", Syms: []string{"LOW"}})
	add(newsfilter.Record{Ms: at(11, 0, 2), ID: "nfB", Pub: at(11, 0, 0), Src: "prNewswire", Title: "Beta Inc Declares Dividend", Syms: []string{"HIGH"}})
	add(newsfilter.Record{Ms: at(13, 0, 0), ID: "nfD", Pub: at(13, 0, 0), Src: "accesswire", Title: "Delta Only At Newsfilter", Syms: []string{"LOW"}})
	add(newsfilter.Record{Ms: at(13, 5, 0), ID: "nfBz", Pub: at(13, 5, 0), Src: "benzinga", Title: "Benzinga Rewrite", Syms: []string{"LOW"}})
	add(newsfilter.Event{Ev: newsfilter.EvAlive, Ms: at(14, 59, 0)})
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "newsfilter-2026-10-30.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func runCmd(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errw bytes.Buffer
	err = run(args, &out, &errw, func() time.Time { return now })
	return out.String(), errw.String(), err
}

func fileHash(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func TestRunReportsFromARealDatabaseAndDayFiles(t *testing.T) {
	f := newFixture(t)
	before := fileHash(t, f.db)

	out, errw, err := runCmd(t, "-db", f.db, "-dir", f.dir, "-days", "30", "-maxpx", "20")
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errw)
	}
	for _, want := range []string{
		"RTPR articles:        4  (wire 3; not on Newsfilter's list 1: Newsfile Corp 1)", // via='poll' and the 2026-09 row are not RTPR-in-window
		"Newsfilter records:   4  (wire 3; other sources 1: benzinga 1)",
		"Matched pairs:        2",
		"Newsfilter had 66.7% of RTPR's articles   (RTPR-only 1)",
		"RTPR had 66.7% of Newsfilter's ticker-bearing articles   (Newsfilter-only 1; 0 more without a ticker not counted)",
		"Newsfilter first on 50.0% of 2 pairs",
		"Lead, own stocks (latest close <= $20.00)",
		"Newsfilter first on 100.0% of 1 pairs",
		"+3.00s", // A: Newsfilter 3 s ahead
		"-1.00s", // B: RTPR 1 s ahead
		"Verdict: not enough data yet (2 matched pairs, need 200).",
		"0 reconnects",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	t.Logf("\n%s", out)

	if after := fileHash(t, f.db); after != before {
		t.Error("the scanner database changed: newsrace must never write it")
	}
}

func TestRunUsesDefaultsFromEnvironmentAndHome(t *testing.T) {
	f := newFixture(t)
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("SCANNER_DB", f.db)
	// The default recording dir is ~/.marketscanner/newsrace.
	if err := os.MkdirAll(filepath.Join(home, ".marketscanner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.dir, filepath.Join(home, ".marketscanner", "newsrace")); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCmd(t)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Matched pairs:        2") {
		t.Errorf("defaults not resolved:\n%s", out)
	}
	// Without the own-stocks cut that section disappears.
	out, _, err = runCmd(t, "-maxpx", "0")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "own stocks") {
		t.Errorf("-maxpx 0 should drop the own-stocks section:\n%s", out)
	}
}

func TestRunWindowAndErrors(t *testing.T) {
	f := newFixture(t)

	// -days 1 reaches back to 2026-10-29 15:00, which still holds today's articles.
	out, _, err := runCmd(t, "-db", f.db, "-dir", f.dir, "-days", "1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Matched pairs:        2") {
		t.Errorf("one day back from 15:00 still holds the 10:00-13:00 articles:\n%s", out)
	}

	if _, _, err := runCmd(t, "-db", f.db, "-dir", filepath.Join(t.TempDir(), "nope")); err == nil || !strings.Contains(err.Error(), "no Newsfilter recordings") {
		t.Errorf("missing recordings dir: %v", err)
	}
	if _, _, err := runCmd(t, "-db", filepath.Join(t.TempDir(), "absent.db"), "-dir", f.dir); err == nil || !strings.Contains(err.Error(), "scanner database") {
		t.Errorf("missing database: %v", err)
	}
	if _, _, err := runCmd(t, "-db", f.db, "-dir", f.dir, "-days", "0"); err == nil {
		t.Error("-days 0 must be refused")
	}
	if _, _, err := runCmd(t, "-nonsense"); err == nil {
		t.Error("an unknown flag must be refused")
	}

	// An empty recordings dir is not an error, but the user is told.
	empty := t.TempDir()
	out, errw, err := runCmd(t, "-db", f.db, "-dir", empty)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errw, "no Newsfilter recordings") || !strings.Contains(out, "Matched pairs:        0") {
		t.Errorf("empty dir: stderr %q out\n%s", errw, out)
	}
	// The three wire articles predate any recording: excluded, never misses.
	if !strings.Contains(out, "3 RTPR articles excluded") || !strings.Contains(out, "(RTPR-only 0)") {
		t.Errorf("with no recording at all every RTPR wire article is excluded, not counted as a miss:\n%s", out)
	}
}

func TestDatabaseIsOpenedReadOnly(t *testing.T) {
	f := newFixture(t)
	before := fileHash(t, f.db)
	db, err := openReadOnly(f.db)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM news_articles`).Scan(&n); err != nil || n != 6 {
		t.Fatalf("read: n=%d err=%v", n, err)
	}
	for _, stmt := range []string{
		`INSERT INTO daily_close_history (ticker, trade_date, close) VALUES ('X', '2026-10-30', 1)`,
		`DELETE FROM news_articles`,
		`UPDATE news_articles SET headline = 'x'`,
		`CREATE TABLE sneaky (a)`,
		`PRAGMA journal_mode = DELETE`,
	} {
		if _, err := db.Exec(stmt); err == nil && !strings.HasPrefix(stmt, "PRAGMA") {
			t.Errorf("a write went through the read-only handle: %s", stmt)
		}
	}
	db.Close()
	if fileHash(t, f.db) != before {
		t.Error("database file changed")
	}
	if _, err := openReadOnly(filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Error("a missing database must not be created by opening it")
	}
}

// The command never writes scanner.db. With the scanner stopped, SQLite may
// still leave empty -wal and -shm files beside it, which is what the usage
// comment in main.go says; nothing else may appear next to the database.
func TestRunLeavesOnlyTheDatabaseAndItsEmptySideFiles(t *testing.T) {
	f := newFixture(t)
	before := fileHash(t, f.db)
	if _, _, err := runCmd(t, "-db", f.db, "-dir", f.dir); err != nil {
		t.Fatal(err)
	}
	if fileHash(t, f.db) != before {
		t.Error("scanner.db changed")
	}
	ents, err := os.ReadDir(filepath.Dir(f.db))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.IsDir() { // the recordings directory of the fixture
			continue
		}
		switch e.Name() {
		case "scanner.db", "scanner.db-shm":
		case "scanner.db-wal":
			if info, err := e.Info(); err != nil || info.Size() != 0 {
				t.Errorf("scanner.db-wal has content (%v, %v): something wrote to the database", info, err)
			}
		default:
			t.Errorf("unexpected file %s next to the database", e.Name())
		}
	}
}

func TestReadOnlyDSN(t *testing.T) {
	for _, c := range []struct{ in, wantPrefix string }{
		{`C:\Users\someone\.marketscanner\scanner.db`, "file:///C:/Users/someone/.marketscanner/scanner.db?mode=ro"},
		{`C:\Users\some one\scanner.db`, "file:///C:/Users/some%20one/scanner.db?mode=ro"},
		{`/home/x/scanner.db`, "file:///home/x/scanner.db?mode=ro"},
	} {
		got, err := readOnlyDSN(c.in)
		if err != nil {
			t.Fatal(err)
		}
		// filepath.Abs on a foreign-style path is platform dependent; only
		// check the shape that matters on the platform running the test.
		if filepath.IsAbs(c.in) && !strings.HasPrefix(got, c.wantPrefix) {
			t.Errorf("readOnlyDSN(%q) = %q, want prefix %q", c.in, got, c.wantPrefix)
		}
		if !strings.Contains(got, "mode=ro") || !strings.Contains(got, "query_only(1)") {
			t.Errorf("dsn lacks the read-only switches: %s", got)
		}
	}
}
