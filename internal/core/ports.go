package core

import (
	"context"
	"errors"
)

// ErrNotEntitled marks a feed error caused by the API key lacking the add-on
// subscription for an endpoint (e.g. Benzinga news), as opposed to a transient
// failure. Callers detect it with errors.Is.
var ErrNotEntitled = errors.New("not entitled")

// ErrRateLimited marks a feed error caused by the provider throttling us (HTTP
// 429), as opposed to an entitlement or transient failure. Pollers detect it
// with IsRateLimited and back off rather than retrying on their normal cadence.
var ErrRateLimited = errors.New("rate limited")

// IsRateLimited reports whether err was caused by provider throttling.
func IsRateLimited(err error) bool { return errors.Is(err, ErrRateLimited) }

// This file declares every cross-package contract. No package depends on a
// concrete implementation of another — they depend on these interfaces, all of
// which live in core (L0). Implementations:
//   FeedClient   -> internal/feed
//   Engine       -> internal/engine (+ internal/merge)
//   Scanner      -> internal/scan
//   *Repo        -> internal/persist
//   Server       -> internal/server

// FeedClient is the only thing the ingest/news layers know about the data source.
type FeedClient interface {
	// Snapshot returns the full-market REST snapshot as seed rows (each carrying
	// prev-close) used to seed day-state on startup and after every reconnect.
	Snapshot(ctx context.Context) ([]Seed, error)
	// Stream subscribes to per-second aggregates ("A.*") and delivers them on out
	// until ctx is done. It reconnects internally; each (re)connection invokes
	// onReset so the caller can re-seed day-state.
	Stream(ctx context.Context, out chan<- Agg, onReset func()) error
	// NewsSince polls the generic REST news endpoint for items published after
	// sinceMs (all plans; hourly recency; headline + description only).
	NewsSince(ctx context.Context, sinceMs int64) ([]NewsItem, error)
	// BenzingaNews polls the real-time Benzinga endpoint (paid expansion). The
	// bodies map (item ID -> full article HTML) stays server-side for the
	// summarizer. Un-entitled keys fail with an entitlement error.
	BenzingaNews(ctx context.Context, sinceMs int64) ([]NewsItem, map[string]string, error)
	// BenzingaNewsFor fetches one symbol's Benzinga articles since sinceMs —
	// used by the auto-summarizer to pull the day's news for a scanner entrant
	// (including articles published before the app started).
	BenzingaNewsFor(ctx context.Context, sym string, sinceMs int64) ([]NewsItem, map[string]string, error)
	// Reference lists tradable tickers (with security type) for universe building.
	Reference(ctx context.Context) ([]Ticker, error)
	// Fundamentals fetches SEC-filed quarterly revenue/cash/cash-flow for one
	// symbol (paid add-on; un-entitled keys fail with ErrNotEntitled).
	Fundamentals(ctx context.Context, sym string) (Fundamentals, error)
}

// Engine is the in-memory rolling-window store. The hot WRITE side (Apply) is
// driven by ingest; the READ side is the merged-snapshot channel consumed by the
// scanner. There is deliberately no synchronous Snapshot() accessor — snapshots
// flow out only via Snapshots(), built inside the engine's single-writer shards.
type Engine interface {
	Apply(a Agg) // hot write, lock-free per shard
	// Seed sets a ticker's day-state from a snapshot row (sessVol is SET, not
	// accumulated) — safe to call repeatedly, e.g. on every reconnect re-seed.
	Seed(s Seed)
	SeedClose(sym string, prevClose float64)
	// SeedFrozen backfills the frozen prior-session review slots (pre* for
	// SessionPre, rth* for SessionRegular; anything else is a no-op) from raw
	// minute-bar window stats, for a process that missed the live session
	// boundary. Provided symbols' slots are overwritten UNCONDITIONALLY —
	// call only when the live slot is known-incomplete. Untracked symbols
	// are silently skipped; the engine derives chg/gap/fade from its own
	// prevClose via the same derivation as the live boundary freeze.
	SeedFrozen(which Session, stats map[string]SessionStats)
	SetSession(s Session)            // driven by the session manager
	SetFloats(f map[string]FloatRef) // atomic hot-swap after weekly refresh
	SetSecTypes(m map[string]string) // atomic hot-swap after a universe refresh
	SetBaseline(b *BaselineTable)    // atomic hot-swap after nightly recompute
	// Retain evicts every tracked ticker NOT in members (used to prune the engine
	// to the regular-securities universe after the async reference load narrows it).
	Retain(members map[string]struct{})
	Snapshots() <-chan *MetricSnapshot // merged views, ~4 Hz
	// SessionBars returns each active ticker's session-so-far 5-minute OHLCV
	// bars, oldest→newest, one bar per bucket from the ticker's first OBSERVED
	// bucket through the current one — pre-observation buckets are excluded,
	// never fabricated. Date is the bucket's ET start ("2006-01-02 15:04").
	// Gated at read time: tickers with fewer than 20 observed buckets (the
	// pattern pass's own minimum history) or negligible session volume are
	// omitted, so the map covers only the active fraction of the market.
	SessionBars() map[string][]DailyBar
	Tracked() int                  // # tickers, for status
	Run(ctx context.Context) error // owns shards, 1 Hz clock, merger
}

// ScanEntry fires when a symbol newly enters a scan's matched set (independent
// of the scan's alert config) — it drives the activity log and auto-summaries.
// A scan's first evaluation after (re)load is suppressed so startup and def
// edits don't fire a storm of pseudo-entries.
type ScanEntry struct {
	ScanID  string  `json:"scanId"`
	Sym     string  `json:"sym"`
	At      int64   `json:"at"`      // epoch ms
	Last    float64 `json:"last"`    // price at entry
	HasNews bool    `json:"hasNews"` // row's News flag at the moment of entry
	// Hits is the scan's base-criteria recurrence counter AT THIS ENTRY (see
	// core.Row.Hits) — archived so a restart can restore the true counter by
	// taking MAX(Hits) per scan/sym today, rather than re-deriving it from
	// entry COUNT (which undercounts once a hits filter is in play, since
	// entries into the DISPLAYED set lag entries into the base set).
	Hits int `json:"hits"`
}

// Scanner evaluates all enabled scans against engine snapshots and emits the WS
// ticks/alerts. It owns the per-scan seq and diff/alert state (single writer).
type Scanner interface {
	Upsert(def ScanDef)
	Delete(id string)
	List() []ScanDef
	Ticks() <-chan ScanTick
	Alerts() <-chan AlertMsg
	Entries() <-chan ScanEntry         // every scan-set entry (activity log / autosum)
	Resync(id string) (ScanTick, bool) // current state as a full=true tick
	Run(ctx context.Context) error
}

// Server serves the embedded React app + the /stream WebSocket.
type Server interface {
	Run(ctx context.Context) error
	Addr() string
}

// --- repositories (internal/persist) ---

type ScanRepo interface {
	List(ctx context.Context) ([]ScanDef, error)
	Save(ctx context.Context, d ScanDef) error
	Delete(ctx context.Context, id string) error
}

type FloatRepo interface {
	Floats(ctx context.Context) (map[string]FloatRef, error)
	UpsertFloats(ctx context.Context, refs []FloatRef) error
	// StaleTickers returns universe tickers whose AsOf is older than olderThanSec.
	StaleTickers(ctx context.Context, olderThanSec int64, universe []string) ([]string, error)
}

type BaselineRepo interface {
	LoadBaseline(ctx context.Context) (*BaselineTable, error)
	UpsertVolumeCurves(ctx context.Context, tradeDate string, curves map[string][]int64) error
	HasCurveForDate(ctx context.Context, tradeDate string) (bool, error)
	TrailingTradingDays(ctx context.Context, asOf string, n int) ([]string, error)
	RecomputeBaseline(ctx context.Context, asOf string, window []string) (rows int, err error)
	// RecomputeBaselineFor rebuilds only these tickers' rows (same mean, same
	// window) and returns them as a partial table.
	RecomputeBaselineFor(ctx context.Context, asOf string, window, tickers []string) (*BaselineTable, error)
	PruneVolumeCurves(ctx context.Context, beforeDate string) (deleted int, err error)
	SetBaselineStatus(ctx context.Context, status, computedFor string) error
	GetBaselineStatus(ctx context.Context) (status, computedFor string, err error)
}

type CalendarRepo interface {
	Sessions(ctx context.Context) ([]MarketDay, error)
	Upsert(ctx context.Context, days []MarketDay) error
}

// TrendRepo stores per-ticker daily OHLCV bars (from nightly grouped bars) —
// the raw history the daily-trend classification and the flag-pattern pass are
// recomputed from, kept in the DB so a restart never refetches a month of
// grouped bars.
type TrendRepo interface {
	// UpsertDailyBars stores one date's bars keyed by ticker; tradeDate is the
	// row key for all of them (see the persist implementation).
	UpsertDailyBars(ctx context.Context, tradeDate string, bars map[string]DailyBar) error
	HasClosesForDate(ctx context.Context, tradeDate string) (bool, error)
	// LoadBars returns bars per ticker in trade-date ASCENDING order,
	// restricted to dates >= sinceDate. Callers that only need closes project
	// them out of the bars — there is deliberately no second closes-only query.
	LoadBars(ctx context.Context, sinceDate string) (map[string][]DailyBar, error)
	// BarsFor returns ONE ticker's bars, dates >= sinceDate, in trade-date
	// ASCENDING order — the same guarantee LoadBars makes, since the pattern
	// math and the setup reader's prompt both read a series oldest→newest.
	// It exists so a per-symbol consumer (the Claude setup reader, which needs
	// the actual bars behind a shortlisted symbol's flag geometry) doesn't have
	// to load the whole ~12k-ticker market to read one series. Nightly
	// whole-market work still uses LoadBars — this is not a replacement for it.
	// An unknown ticker is not an error: it returns a nil slice, nil error.
	BarsFor(ctx context.Context, sym, sinceDate string) ([]DailyBar, error)
	// MaxDate returns the newest trade_date stored ("" if the table is empty)
	// — the true data-recency stamp for TrendRef.AsOf, since wall-clock "today"
	// can be ahead of the newest close actually on hand (intraday boot,
	// weekend boot, a skipped holiday).
	MaxDate(ctx context.Context) (string, error)
	PruneDailyCloses(ctx context.Context, beforeDate string) (int, error)
}

// WatchlistRepo persists the user's ordered day-watchlist (the charts-grid
// window's symbol pool). One list, whole-list replace on save — the UI always
// edits the complete list, so there is no per-item CRUD.
type WatchlistRepo interface {
	// List returns the watchlist symbols ordered by position.
	List(ctx context.Context) ([]string, error)
	// Replace swaps the whole list in one transaction; positions are the slice
	// indices. An empty/nil slice clears the list.
	Replace(ctx context.Context, syms []string) error
}

// FlagEventRepo persists the Signal window's signal activity (internal/flaglog:
// the 9EMA flag scan's events, the green dildo / 9 EMA pullback and the volume
// climax / bottom alerts) so its activity list can be viewed back day by day.
// Note is stored with the row. Identity
// is (sym, ts, kind): an event is recorded once however many evaluations repeat
// it.
type FlagEventRepo interface {
	// Insert stores evs under the ET day in one transaction; rows whose
	// (sym, ts, kind) already exist are ignored (INSERT OR IGNORE).
	Insert(ctx context.Context, day string, evs []FlagEvent) error
	// Day returns one ET day's events ordered by ts, then insertion order.
	Day(ctx context.Context, day string) ([]FlagEvent, error)
	// Days returns the distinct days that have events, newest first, at most
	// limit of them.
	Days(ctx context.Context, limit int) ([]string, error)
	// Prune deletes the days older than today minus keepDays (today is
	// YYYY-MM-DD).
	Prune(ctx context.Context, keepDays int, today string) error
}

// SessionStatsRepo persists the engine's boundary session snapshots — one
// whole-market row set per (date, session) — so "what moved after hours
// yesterday" survives the overnight restart gap and is reviewable the next
// morning. Rows are RAW (prices/shares, never percentages); every derived
// number is computed at read time by the consumer.
type SessionStatsRepo interface {
	// SaveSnapshot replaces the (Date, Session) key's rows wholesale in one
	// transaction — a re-freeze of the same boundary overwrites cleanly
	// (idempotent), never accumulates stale symbols.
	SaveSnapshot(ctx context.Context, snap SessionSnapshot) error
	// Dates returns the distinct trading dates on hand, newest first.
	Dates(ctx context.Context) ([]string, error)
	// Load returns one (date, session)'s rows, ordered by symbol. An unknown
	// pair is not an error: nil slice, nil error.
	Load(ctx context.Context, date string, session Session) ([]SessionSnapshotRow, error)
	// PruneBefore keeps only the newest keepDates distinct dates (<= 0 uses
	// the default of 10) and reports how many rows were deleted.
	PruneBefore(ctx context.Context, keepDates int) (int, error)
}

// SummaryRepo caches news summaries (local extractive reads AND Claude reads
// alike) so a repeated click (or restart) never redoes the work — and, for
// Claude, never re-spends tokens on the same article. GetSummary returns the
// model/source string the summary was produced with (e.g. "local" or a real
// Claude model name) alongside the text: a caller deciding whether a cache
// hit is safe to serve as-is needs to know WHICH kind of read it is looking
// at (see cmd/scanner/main.go's manual "news.summarize" handler, which must
// NOT treat a cached local read as a substitute for an explicitly requested
// Claude read).
type SummaryRepo interface {
	GetSummary(ctx context.Context, id string) (summary, model string, ok bool, err error)
	SaveSummary(ctx context.Context, id, sym, model, summary string) error
}

// FundamentalsRepo caches on-demand fundamentals per ticker (SEC filings only
// change ~quarterly, so a repeated click on the same symbol shouldn't re-hit
// the API every time).
type FundamentalsRepo interface {
	GetFundamentals(ctx context.Context, sym string) (f Fundamentals, ok bool, err error)
	SaveFundamentals(ctx context.Context, f Fundamentals) error
}

// NewsArchive persists every article on arrival (the in-memory poller cache
// only holds ~800 items ≈ 3-4h) plus each scan entry, making two things
// possible: summaries of cache-evicted articles, and per-session latency
// analysis — received_ms - published_ms is feed lag, and joining entries to
// articles answers "was the headline here before the stock hit the scanner?".
type NewsArchive interface {
	// SaveArticles stores first sightings only: a re-arrival of a stored id is
	// ignored so received_ms/via always describe the FIRST time we saw it.
	SaveArticles(ctx context.Context, items []NewsItem, bodies map[string]string, via string, receivedMs int64) error
	// Article returns a stored article and body (the summarizer's fallback
	// when the in-memory cache has evicted it).
	Article(ctx context.Context, id string) (item NewsItem, body string, ok bool, err error)
	// ArticlesFor returns a ticker's stored articles newest-first — the news
	// panel's history source when a symbol is selected (the in-memory
	// broadcast list only reaches back to UI load).
	ArticlesFor(ctx context.Context, sym string, limit int) ([]NewsItem, error)
	// SetArticleScore attaches an RTPR Impact Score to an already-archived
	// article. ok=false means the article isn't archived yet — the caller
	// fetches it via the score event's signed permalink first, then retries.
	SetArticleScore(ctx context.Context, id string, score int, tier, eventType, direction string) (ok bool, err error)
	SaveEntry(ctx context.Context, e ScanEntry) error
	// EntryHitsMax returns, per scan per symbol, the HIGHEST ScanEntry.Hits
	// recorded since sinceMs — seeds the scanner's recurring-hits counters
	// across restarts. MAX, not COUNT(*): a scan gated by its own hits filter
	// only archives entries once the threshold is met, so counting archived
	// entries systematically undercounts base-criteria recurrence. Hits only
	// increases within a day, so the max archived value IS the last known
	// counter value.
	EntryHitsMax(ctx context.Context, sinceMs int64) (map[string]map[string]int, error)
	// PruneNews deletes articles/tickers/entries older than beforeMs.
	PruneNews(ctx context.Context, beforeMs int64) error
}

// TapeArchive persists tape-reading regime events + reads (local verdicts and
// Claude structured reads alike) — the Go mirror of the standalone Python
// tape-reader app's recorder.py, but one shared table across every symbol
// (distinguished by the sym column) instead of recorder.py's
// one-SQLite-file-per-symbol layout.
type TapeArchive interface {
	// SaveTapeRead archives one completed read for a regime event. event
	// supplies the detection-time identity (Sym/Ts/Trigger/Headline) and
	// feature snapshot; verdict supplies the "who is in control" call
	// (Control/Confidence/Evidence) — this is whichever TapeVerdict actually
	// completed for this read, the local starter or Claude's normalized
	// upgrade, not necessarily event.Verdict. The remaining four are
	// Claude/local-read fields with no home on either existing type.
	SaveTapeRead(ctx context.Context, event RegimeEvent, verdict TapeVerdict, whatsHappening, watchFor, model, errMsg string) error
	// Recent returns a symbol's archived reads, newest first.
	Recent(ctx context.Context, sym string, limit int) ([]TapeReadRecord, error)
}

// SetupArchive persists Claude's structured bull-flag setup reads AND is the
// cache that keeps them cheap. Unlike TapeArchive — an append-only log of
// intraday regime events, where the same symbol legitimately produces many
// reads a session — a setup read describes ONE nightly classification, so
// (Sym, AsOf) is its identity and its primary key: a symbol re-entering a
// flag-filtered scan later the same day is a cache hit costing nothing, and a
// fresh nightly AsOf is a miss that earns a fresh read.
//
// Failed reads (Quality 0, Err set) are stored like any other. Whether a
// stored failure counts as a cache hit or is worth one retry is the calling
// worker's budget decision, not this port's.
type SetupArchive interface {
	// SaveSetupRead upserts one read on (Sym, AsOf) — saving twice for the
	// same pair replaces, it never duplicates or errors.
	SaveSetupRead(ctx context.Context, r SetupRead) error
	// GetSetupRead is the pre-spend cache lookup: ok=false means no read
	// exists for exactly this (sym, asOf) and a Claude call is warranted.
	GetSetupRead(ctx context.Context, sym, asOf string) (r SetupRead, ok bool, err error)
	// RecentSetupReads returns a symbol's archived reads, newest first.
	RecentSetupReads(ctx context.Context, sym string, limit int) ([]SetupRead, error)
}
