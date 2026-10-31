// Command newsrace answers "is Newsfilter as fast as RTPR, and does it miss
// anything?" from two records the scanner keeps on its own:
//
//   - the Newsfilter recorder's day files (~/.marketscanner/newsrace, written
//     by internal/newsfilter whenever a Newsfilter token is configured), and
//   - the RTPR rows in the scanner database (news_articles where via='rtpr').
//
// It pairs the same press release on both sides and prints coverage both ways,
// per wire, plus how far apart the two arrived (p10/p50/p90, and the same for
// the user's own low-priced stocks). The pairing and the numbers live in
// internal/newsfilter/race.go.
//
// It never writes the scanner database. It is opened with mode=ro, so scanner.db
// itself is not touched, but with the scanner stopped SQLite may still leave
// empty scanner.db-wal and scanner.db-shm files beside it (the scanner's next
// clean close removes them; with the scanner running they already exist).
// immutable=1 would avoid that and is unsafe against a live database. The
// recorder's files are only read, and it needs no key and makes no network
// calls. Run it from the repo; this never touches scanner.exe:
//
//	go run ./cmd/newsrace
//	go run ./cmd/newsrace -days 7 -maxpx 10
//
// received_ms in news_articles is stamped when the article reaches the news
// pipeline (the archive sink in cmd/scanner/main.go calls time.Now() at that
// moment, before the asynchronous database write), so it is an arrival time,
// not a write time. For RTPR that moment follows its 3 s ref poll and the
// permalink fetch, which is what the scanner actually experiences.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, registers "sqlite"; no cgo

	"github.com/eelkonkoopman/marketscanner/internal/config"
	"github.com/eelkonkoopman/marketscanner/internal/newsfilter"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr, time.Now); err != nil {
		fmt.Fprintln(os.Stderr, "newsrace:", err)
		os.Exit(1)
	}
}

func run(args []string, out, errw io.Writer, now func() time.Time) error {
	fs := flag.NewFlagSet("newsrace", flag.ContinueOnError)
	fs.SetOutput(errw)
	days := fs.Int("days", 30, "how many days back to compare")
	dbPath := fs.String("db", "", "scanner database (default: $SCANNER_DB or ~/.marketscanner/scanner.db)")
	dir := fs.String("dir", "", "Newsfilter recordings (default: ~/.marketscanner/newsrace)")
	maxPx := fs.Float64("maxpx", 20, "\"own stocks\" cut: latest daily close at or under this")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *days <= 0 {
		return fmt.Errorf("-days must be positive")
	}
	if *dbPath == "" {
		if *dbPath = os.Getenv("SCANNER_DB"); *dbPath == "" {
			*dbPath = filepath.Join(config.ConfigDir(), "scanner.db")
		}
	}
	if *dir == "" {
		*dir = filepath.Join(config.ConfigDir(), "newsrace")
	}

	asOf := now()
	toMs := asOf.UnixMilli()
	fromMs := asOf.AddDate(0, 0, -*days).UnixMilli()

	capt, err := newsfilter.LoadDir(*dir, fromMs, toMs)
	if err != nil {
		return err
	}
	if len(capt.Records) == 0 && len(capt.Events) == 0 {
		fmt.Fprintf(errw, "newsrace: no Newsfilter recordings in %s for this window; nothing to compare yet\n", *dir)
	}
	if capt.Bad > 0 {
		fmt.Fprintf(errw, "newsrace: skipped %d unreadable lines in the day files\n", capt.Bad)
	}
	if capt.Dupes > 0 {
		fmt.Fprintf(errw, "newsrace: dropped %d repeat sightings of an article (kept the earliest)\n", capt.Dupes)
	}

	db, err := openReadOnly(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	rtpr, err := readRTPR(ctx, db, fromMs, toMs)
	if err != nil {
		return fmt.Errorf("reading RTPR articles from %s: %w", *dbPath, err)
	}
	var prices map[string]float64
	if *maxPx > 0 {
		if prices, err = readPrices(ctx, db); err != nil {
			fmt.Fprintf(errw, "newsrace: no daily closes (%v); the own-stocks cut will be empty\n", err)
		}
	}

	cov := newsfilter.BuildCoverage(capt.Events, capt.Records, fromMs, toMs)
	rep := newsfilter.Analyze(rtpr, capt.Records, cov, newsfilter.Options{MaxPx: *maxPx, Prices: prices}, fromMs, toMs)
	rep.Write(out)
	return nil
}

// readOnlyDSN builds the file: URI that opens path read-only. The path goes
// through url.URL so a drive letter ("/C:/...") and spaces come out the way
// SQLite's URI parser expects.
func readOnlyDSN(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p, RawQuery: "mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)"}
	return u.String(), nil
}

// openReadOnly opens the scanner database for reading only: mode=ro makes
// SQLite refuse every write, and query_only is a second lock on the same door.
// WAL means it can read while the scanner is running.
func openReadOnly(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("scanner database: %w", err)
	}
	dsn, err := readOnlyDSN(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s read-only: %w", path, err)
	}
	return db, nil
}

// readRTPR loads the archived RTPR articles published in the window. tickers
// is one comma-joined column (persist/newsarchive.go).
func readRTPR(ctx context.Context, db *sql.DB, fromMs, toMs int64) ([]newsfilter.RTPRArticle, error) {
	rows, err := db.QueryContext(ctx, `
SELECT id, headline, COALESCE(source, ''), COALESCE(tickers, ''), published_ms, received_ms
FROM news_articles
WHERE via = 'rtpr' AND published_ms >= ? AND published_ms <= ?
ORDER BY published_ms`, fromMs, toMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []newsfilter.RTPRArticle
	for rows.Next() {
		var a newsfilter.RTPRArticle
		var tickers string
		if err := rows.Scan(&a.ID, &a.Title, &a.Source, &tickers, &a.PublishedMs, &a.ReceivedMs); err != nil {
			return nil, err
		}
		for _, t := range strings.Split(tickers, ",") {
			if t = strings.TrimSpace(t); t != "" {
				a.Tickers = append(a.Tickers, t)
			}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// readPrices loads each ticker's most recent daily close.
func readPrices(ctx context.Context, db *sql.DB) (map[string]float64, error) {
	rows, err := db.QueryContext(ctx, `
SELECT d.ticker, d.close
FROM daily_close_history d
JOIN (SELECT ticker, MAX(trade_date) AS md FROM daily_close_history GROUP BY ticker) m
  ON d.ticker = m.ticker AND d.trade_date = m.md`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]float64)
	for rows.Next() {
		var t string
		var px float64
		if err := rows.Scan(&t, &px); err != nil {
			return nil, err
		}
		out[strings.ToUpper(t)] = px
	}
	return out, rows.Err()
}
