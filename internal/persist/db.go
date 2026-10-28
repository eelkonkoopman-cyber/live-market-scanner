// Package persist is the only package that touches SQL. It owns the single
// *sql.DB handle and exposes the core repository interfaces. Other packages
// depend on those interfaces (in core), never on this package's concrete types
// or on database/sql.
package persist

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/eelkonkoopman/marketscanner/internal/core"

	_ "modernc.org/sqlite" // pure-Go driver, registers "sqlite"; no cgo
)

// DB owns the process-wide SQLite handle.
type DB struct{ sql *sql.DB }

// Open opens (creating if needed) the SQLite database at path, applies WAL +
// busy-timeout pragmas, and runs migrations.
func Open(ctx context.Context, path string) (*DB, error) {
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	h, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// WAL permits concurrent readers; only writes serialize. A small pool lets the
	// nightly baseline recompute write while startup loads / scan saves read,
	// rather than blocking everything behind a single connection.
	h.SetMaxOpenConns(4)
	h.SetMaxIdleConns(4)
	if err := h.PingContext(ctx); err != nil {
		_ = h.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := migrate(ctx, h); err != nil {
		_ = h.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &DB{sql: h}, nil
}

// Close closes the underlying handle.
func (d *DB) Close() error { return d.sql.Close() }

// Repo accessors — each returns a thin struct over the shared handle that
// satisfies the corresponding core interface.
func (d *DB) ScanRepo() core.ScanRepo                 { return &scanRepo{d.sql} }
func (d *DB) FloatRepo() core.FloatRepo               { return &floatRepo{d.sql} }
func (d *DB) BaselineRepo() core.BaselineRepo         { return &baselineRepo{d.sql} }
func (d *DB) CalendarRepo() core.CalendarRepo         { return &calendarRepo{d.sql} }
func (d *DB) SummaryRepo() core.SummaryRepo           { return &summaryRepo{d.sql} }
func (d *DB) FundamentalsRepo() core.FundamentalsRepo { return &fundamentalsRepo{d.sql} }
func (d *DB) NewsArchive() core.NewsArchive           { return &newsArchive{d.sql} }
func (d *DB) TrendRepo() core.TrendRepo               { return &trendRepo{d.sql} }
func (d *DB) TapeArchive() core.TapeArchive           { return &tapeArchive{d.sql} }
func (d *DB) SetupArchive() core.SetupArchive         { return &setupArchive{d.sql} }
func (d *DB) WatchlistRepo() core.WatchlistRepo       { return &watchlistRepo{d.sql} }
func (d *DB) FlagEventRepo() core.FlagEventRepo       { return &flagEventRepo{d.sql} }
func (d *DB) SessionStatsRepo() core.SessionStatsRepo { return &sessionStatsRepo{d.sql} }
func (d *DB) DayLogRepo() core.DayLogRepo             { return &dayLogRepo{d.sql} }

// Compile-time assertions that the repos satisfy the core ports.
var (
	_ core.ScanRepo         = (*scanRepo)(nil)
	_ core.FloatRepo        = (*floatRepo)(nil)
	_ core.BaselineRepo     = (*baselineRepo)(nil)
	_ core.CalendarRepo     = (*calendarRepo)(nil)
	_ core.SummaryRepo      = (*summaryRepo)(nil)
	_ core.FundamentalsRepo = (*fundamentalsRepo)(nil)
	_ core.NewsArchive      = (*newsArchive)(nil)
	_ core.TrendRepo        = (*trendRepo)(nil)
	_ core.TapeArchive      = (*tapeArchive)(nil)
	_ core.SetupArchive     = (*setupArchive)(nil)
	_ core.WatchlistRepo    = (*watchlistRepo)(nil)
	_ core.SessionStatsRepo = (*sessionStatsRepo)(nil)
	_ core.FlagEventRepo    = (*flagEventRepo)(nil)
	_ core.DayLogRepo       = (*dayLogRepo)(nil)
)

const schema = `
CREATE TABLE IF NOT EXISTS schema_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS scan_defs (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  enabled    INTEGER NOT NULL DEFAULT 1,
  sort_field TEXT NOT NULL DEFAULT 'chg5m',
  sort_dir   TEXT NOT NULL DEFAULT 'desc',
  top_n      INTEGER NOT NULL DEFAULT 50,
  def_json   TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  rev        INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS idx_scan_defs_enabled ON scan_defs(enabled);

CREATE TABLE IF NOT EXISTS market_calendar (
  trade_date     TEXT PRIMARY KEY,
  is_trading     INTEGER NOT NULL,
  is_half_day    INTEGER NOT NULL DEFAULT 0,
  sess_open_min  INTEGER NOT NULL DEFAULT 240,
  reg_open_min   INTEGER NOT NULL DEFAULT 570,
  reg_close_min  INTEGER NOT NULL DEFAULT 960,
  sess_close_min INTEGER NOT NULL DEFAULT 1200,
  last_bucket    INTEGER NOT NULL DEFAULT 191,
  source         TEXT,
  updated_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_calendar_trading ON market_calendar(is_trading);

CREATE TABLE IF NOT EXISTS float_ref (
  ticker       TEXT PRIMARY KEY,
  shares_out   INTEGER,
  float_shares INTEGER,
  as_of        INTEGER NOT NULL,
  source       TEXT NOT NULL,
  raw_json     TEXT
);
CREATE INDEX IF NOT EXISTS idx_float_asof ON float_ref(as_of);

CREATE TABLE IF NOT EXISTS volume_curve_history (
  ticker     TEXT NOT NULL,
  trade_date TEXT NOT NULL,
  bucket     INTEGER NOT NULL,
  cum_vol    INTEGER NOT NULL,
  PRIMARY KEY (ticker, trade_date, bucket)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_vch_date ON volume_curve_history(trade_date);

-- (ticker, trade_date) pairs whose curve was rebuilt from minute bars by
-- internal/rvolfill, including dates the ticker didn't trade: each pair is
-- fetched once, never again.
CREATE TABLE IF NOT EXISTS curve_filled (
  ticker     TEXT NOT NULL,
  trade_date TEXT NOT NULL,
  PRIMARY KEY (ticker, trade_date)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS rvol_baseline (
  ticker       TEXT NOT NULL,
  bucket       INTEGER NOT NULL,
  baseline_cum REAL NOT NULL,
  sample_count INTEGER NOT NULL,
  computed_for TEXT NOT NULL,
  updated_at   INTEGER NOT NULL,
  PRIMARY KEY (ticker, bucket)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS fundamentals_cache (
  ticker    TEXT PRIMARY KEY,
  as_of     INTEGER NOT NULL,
  data_json TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_fundamentals_asof ON fundamentals_cache(as_of);

CREATE TABLE IF NOT EXISTS news_summary (
  id         TEXT PRIMARY KEY,
  sym        TEXT,
  model      TEXT,
  summary    TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS news_articles (
  id           TEXT PRIMARY KEY,
  headline     TEXT NOT NULL,
  source       TEXT,
  url          TEXT,
  teaser       TEXT,
  body         TEXT,
  tickers      TEXT,
  published_ms INTEGER NOT NULL,
  received_ms  INTEGER NOT NULL,
  via          TEXT NOT NULL,
  impact_score     INTEGER, -- RTPR Impact Score (Beta) 0-100, NULL = unscored
  impact_tier      TEXT,
  event_type       TEXT,
  impact_direction TEXT
);
CREATE INDEX IF NOT EXISTS idx_news_articles_pub ON news_articles(published_ms);

CREATE TABLE IF NOT EXISTS news_article_tickers (
  ticker       TEXT NOT NULL,
  id           TEXT NOT NULL,
  published_ms INTEGER NOT NULL,
  PRIMARY KEY (ticker, id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_nat_ticker_pub ON news_article_tickers(ticker, published_ms);

-- Named daily_close_history from when it stored only the close, but it carries
-- the full daily OHLCV bar now (the pattern pass needs the range and the
-- volume, not just the close). Renaming it would strand every existing
-- database, so the four newer columns are added by ensureColumn below instead.
-- NB no semicolons in this comment, migrate splits the schema on them.
CREATE TABLE IF NOT EXISTS daily_close_history (
  ticker     TEXT NOT NULL,
  trade_date TEXT NOT NULL,
  close      REAL NOT NULL,
  open       REAL NOT NULL DEFAULT 0,
  high       REAL NOT NULL DEFAULT 0,
  low        REAL NOT NULL DEFAULT 0,
  volume     INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (ticker, trade_date)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_dch_date ON daily_close_history(trade_date);

CREATE TABLE IF NOT EXISTS scan_entries (
  ts       INTEGER NOT NULL,
  sym      TEXT NOT NULL,
  scan_id  TEXT NOT NULL,
  last     REAL,
  has_news INTEGER NOT NULL DEFAULT 0,
  hits     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_scan_entries_ts ON scan_entries(ts);
CREATE INDEX IF NOT EXISTS idx_scan_entries_sym ON scan_entries(sym, ts);

CREATE TABLE IF NOT EXISTS tape_reads (
  ts              INTEGER NOT NULL,
  sym             TEXT NOT NULL,
  trigger         TEXT NOT NULL,
  headline        TEXT,
  control         TEXT,
  confidence      REAL,
  whats_happening TEXT,
  evidence_json   TEXT,
  watch_for       TEXT,
  model           TEXT,
  error           TEXT,
  snapshot_json   TEXT
);
CREATE INDEX IF NOT EXISTS idx_tape_reads_sym ON tape_reads(sym, ts);

-- Keyed, not appended (unlike tape_reads): one Claude setup read per symbol per
-- nightly classification, so (sym, as_of) is the primary key and the cache key
-- the flag-read worker checks before spending a call. A re-entry the same day
-- upserts. NB no semicolons in this comment, migrate splits the schema on them.
CREATE TABLE IF NOT EXISTS setup_reads (
  sym             TEXT NOT NULL,
  as_of           TEXT NOT NULL,
  ts              INTEGER NOT NULL,
  quality         INTEGER NOT NULL DEFAULT 0,
  verdict         TEXT NOT NULL DEFAULT '',
  whats_happening TEXT NOT NULL DEFAULT '',
  risks_json      TEXT NOT NULL DEFAULT '',
  invalidation    TEXT NOT NULL DEFAULT '',
  model           TEXT NOT NULL DEFAULT '',
  error           TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (sym, as_of)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_setup_reads_sym ON setup_reads(sym, ts);

-- The charts-grid window's day-watchlist. pos is the user's ordering, whole
-- list replaced in one transaction on every save (see watchlistRepo.Replace).
CREATE TABLE IF NOT EXISTS watchlist (
  sym TEXT PRIMARY KEY,
  pos INTEGER NOT NULL
);

-- The Signal window's signal activity (internal/flaglog: the 9EMA flag scan,
-- the green dildo and volume climax alerts): one row per signal (sym, bar
-- start ts, kind), recorded once whatever repeats it. day is the ET date of
-- ts, kept to the newest ~30 by flagEventRepo.Prune. at is the server ms it
-- was first recorded, price NULL when the engine had none, note the one-line
-- measure of a dildo / climax signal ('' for the flag kinds - added after the
-- table shipped, see migrate), judged the bar start of the dildo a dildoPb /
-- dildoCancel row judged (0 = unknown or another kind - added later still).
CREATE TABLE IF NOT EXISTS flag_events (
  day   TEXT NOT NULL,
  sym   TEXT NOT NULL,
  ts    INTEGER NOT NULL,
  kind  TEXT NOT NULL,
  label TEXT NOT NULL,
  price REAL,
  at    INTEGER NOT NULL,
  note  TEXT NOT NULL DEFAULT '',
  judged INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (sym, ts, kind)
);
CREATE INDEX IF NOT EXISTS flag_events_day ON flag_events(day, ts);

-- Engine boundary session snapshots (the morning-review source): one RAW row
-- per traded symbol per completed sub-session, written whole-set per
-- (date, session) at the live 09:30 / 16:00 / 20:00 boundaries. Values are
-- prices and shares, never percentages -- the after-hours gain and every other
-- derived number are computed at read time. Zero means unknown, mirroring the
-- in-memory validity flags. Kept to the newest ~10 dates by
-- sessionStatsRepo.PruneBefore. NB no semicolons in this comment, migrate
-- splits the schema on them.
CREATE TABLE IF NOT EXISTS session_stats (
  date       TEXT NOT NULL,
  session    TEXT NOT NULL,
  sym        TEXT NOT NULL,
  hi         REAL NOT NULL DEFAULT 0,
  lo         REAL NOT NULL DEFAULT 0,
  close      REAL NOT NULL DEFAULT 0,
  vwap       REAL NOT NULL DEFAULT 0,
  vol        INTEGER NOT NULL DEFAULT 0,
  prev_close REAL NOT NULL DEFAULT 0,
  PRIMARY KEY (date, session, sym)
) WITHOUT ROWID;

-- The Day Log (internal/daylog): every scan alert that fired, one row each,
-- kept forever. day is the ET trading date of at. Optional numbers are NULL
-- when unknown. out5 / out15 / out30 are the % price change that many minutes
-- after the alert, max_up / max_down the best and worst % inside 30 minutes,
-- all filled by a background pass from 1-minute bars (out_done = 1 once that
-- pass has finished with the alert). blacklisted flags names on the
-- day's symbol blacklist (internal/blacklist).
CREATE TABLE IF NOT EXISTS day_alerts (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  day         TEXT NOT NULL,
  at          INTEGER NOT NULL,
  sym         TEXT NOT NULL,
  scan_id     TEXT NOT NULL,
  scan_name   TEXT NOT NULL DEFAULT '',
  kind        TEXT NOT NULL DEFAULT '',
  price       REAL,
  chg_day     REAL,
  vol         INTEGER,
  rvol        REAL,
  news_hl     TEXT NOT NULL DEFAULT '',
  news_age    INTEGER,
  note        TEXT NOT NULL DEFAULT '',
  blacklisted INTEGER NOT NULL DEFAULT 0,
  out5        REAL,
  out15       REAL,
  out30       REAL,
  max_up      REAL,
  max_down    REAL,
  out_done    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS day_alerts_day ON day_alerts(day, at);

-- The Day Log end-of-day top gainers and losers (side gain or loss, rank 1 is
-- the biggest), one snapshot per day replaced whole by dayLogRepo.ReplaceMovers.
CREATE TABLE IF NOT EXISTS day_movers (
  day      TEXT NOT NULL,
  side     TEXT NOT NULL,
  rank     INTEGER NOT NULL,
  sym      TEXT NOT NULL,
  price    REAL,
  chg_day  REAL,
  vol      INTEGER,
  headline TEXT NOT NULL DEFAULT '',
  taken_at INTEGER NOT NULL,
  PRIMARY KEY (day, side, rank)
);
`

// migrate executes the schema statement-by-statement (driver-agnostic; no
// reliance on multi-statement Exec) and stamps the schema version.
func migrate(ctx context.Context, h *sql.DB) error {
	for _, stmt := range strings.Split(schema, ";") {
		s := strings.TrimSpace(stmt)
		if s == "" {
			continue
		}
		if _, err := h.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("exec schema stmt: %w", err)
		}
	}
	// Columns added to a table after it first shipped: CREATE TABLE IF NOT
	// EXISTS above is a no-op against a database that already has the table
	// from an older schema version, so those columns need an explicit add.
	if err := ensureColumn(ctx, h, "scan_entries", "has_news", "has_news INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("migrate scan_entries.has_news: %w", err)
	}
	if err := ensureColumn(ctx, h, "scan_entries", "hits", "hits INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("migrate scan_entries.hits: %w", err)
	}
	if err := ensureColumn(ctx, h, "flag_events", "note", "note TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate flag_events.note: %w", err)
	}
	if err := ensureColumn(ctx, h, "flag_events", "judged", "judged INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("migrate flag_events.judged: %w", err)
	}
	for col, ddl := range map[string]string{
		"impact_score":     "impact_score INTEGER",
		"impact_tier":      "impact_tier TEXT",
		"event_type":       "event_type TEXT",
		"impact_direction": "impact_direction TEXT",
	} {
		if err := ensureColumn(ctx, h, "news_articles", col, ddl); err != nil {
			return fmt.Errorf("migrate news_articles.%s: %w", col, err)
		}
	}
	// daily_close_history shipped close-only. NOT NULL DEFAULT 0 (rather than
	// nullable) so the rows already in an existing database keep loading —
	// as bars whose OHLV is zero — instead of disappearing or scanning into a
	// NULL. They fill in with real values on the next nightly refresh, which
	// refetches the whole window anyway for split consistency.
	for col, ddl := range map[string]string{
		"open":   "open REAL NOT NULL DEFAULT 0",
		"high":   "high REAL NOT NULL DEFAULT 0",
		"low":    "low REAL NOT NULL DEFAULT 0",
		"volume": "volume INTEGER NOT NULL DEFAULT 0",
	} {
		if err := ensureColumn(ctx, h, "daily_close_history", col, ddl); err != nil {
			return fmt.Errorf("migrate daily_close_history.%s: %w", col, err)
		}
	}
	_, err := h.ExecContext(ctx,
		`INSERT INTO schema_meta(key,value) VALUES('schema_version','1')
		 ON CONFLICT(key) DO NOTHING`)
	return err
}

// GetMeta / SetMeta read and write one schema_meta key — small app-level
// flags like "this one-time seed already ran" (same table and upsert shape
// baselineRepo's own meta helpers use). GetMeta returns "" for an unset key.
func (d *DB) GetMeta(ctx context.Context, key string) (string, error) {
	var v string
	err := d.sql.QueryRowContext(ctx, `SELECT value FROM schema_meta WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (d *DB) SetMeta(ctx context.Context, key, value string) error {
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO schema_meta(key,value) VALUES(?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// ensureColumn adds a column to an existing table if it isn't already there.
// SQLite has no ADD COLUMN IF NOT EXISTS, so this checks pragma table_info
// first (querying an unknown table name is impossible here — table is always
// one of ours from the schema above, never user input).
func ensureColumn(ctx context.Context, h *sql.DB, table, column, addColumnDDL string) error {
	rows, err := h.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil // already present
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = h.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+addColumnDDL)
	return err
}
