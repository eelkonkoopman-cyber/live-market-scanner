package core

import "encoding/json"

// Frame is the envelope for every WS message in both directions.
// Outbound types: scan.tick, alert, news, status, scan.list, error.
// Inbound types:  hello, scan.upsert, scan.delete, view.set, scan.resync,
//
//	news.view, settings.
type Frame struct {
	T  string          `json:"t"`
	ID int64           `json:"id,omitempty"`
	D  json.RawMessage `json:"d,omitempty"`
}

// Frame type tags.
const (
	// server -> client
	TScanTick  = "scan.tick"
	TAlert     = "alert"
	TTwsAuto   = "tws.auto" // outcome of a SendToTWS scan's alert: sent to the linked TWS window, or skipped (and why); broadcast to every window
	TNews      = "news"
	TStatus    = "status"
	TScanList  = "scan.list"
	TChartData = "chart.data"
	TError     = "error"

	// client -> server
	THello             = "hello"
	TScanUpsert        = "scan.upsert"
	TScanDelete        = "scan.delete"
	TViewSet           = "view.set"
	TScanResync        = "scan.resync"
	TNewsView          = "news.view"
	TNewsSummarize     = "news.summarize"
	TSettings          = "settings"
	TSetKey            = "settings.setKey"
	TChartGet          = "chart.get"
	TNewsFor           = "news.for"  // request a symbol's archived news history
	TTrendGet          = "trend.get" // request a symbol's daily-trend context
	TFundamentalsGet   = "fundamentals.get"
	TTapeWatch         = "tape.watch"
	TTapeRead          = "tape.read"
	TTapeClaudeSet     = "tape.claudeSet"     // toggle Claude regime reads for a symbol
	TTapeCommentarySet = "tape.commentarySet" // set the periodic live-commentary interval for a symbol
	TRowGet            = "row.get"            // on-demand row snapshot for a symbol (pop-out tape windows with no scan tiled)
	TChartWatch        = "chart.watch"        // replace this window's live-chart symbol SET (charts grid window)
	TWatchlistGet      = "watchlist.get"      // request the persisted watchlist
	TWatchlistSet      = "watchlist.set"      // replace the persisted watchlist (whole-list, ordered)
	TAlertTWSGet       = "alerttws.get"       // request the global "TWS follow" switch (alert-driven TWS Show on/off)
	TAlertTWSSet       = "alerttws.set"       // set the global "TWS follow" switch
	TDildoParamsGet    = "dildo.params.get"   // request the market-wide GREEN DILDO tuning
	TDildoParamsSet    = "dildo.params.set"   // replace it (validated, persisted, applied live, broadcast)
	TChartGridGet      = "chartgrid.get"      // request the persisted charts-grid layout blob
	TChartGridSet      = "chartgrid.set"      // replace the persisted charts-grid layout blob
	TReviewGet         = "review.get"         // request a persisted past-session snapshot (morning review)
	TDayLogGet         = "daylog.get"         // request one ET day of the Day Log ("" = today)
	TFlagEventsGet     = "flagevents.get"     // request one ET day of the 9EMA flag scan's signal activity ("" = today)

	// server -> client (past-session review)
	TReviewData = "review.data"

	// server -> client (9EMA flag scan signal activity)
	TFlagEventsData = "flagevents.data" // one day's events + the dates that have any (answer to flagevents.get)
	TFlagEventsAdd  = "flagevents.add"  // broadcast: events recorded just now (today only)

	// server -> client (Day Log)
	TDayLogData = "daylog.data" // the Day Log of one day: alerts, movers, dates, csv folder

	// server -> client (watchlist / charts-grid layout)
	TWatchlistData   = "watchlist.data"
	TChartGridData   = "chartgrid.data"
	TDildoParamsData = "dildo.params.data" // the tuning (a dildopb.Params-shaped object); answer to get, broadcast to ALL windows on set
	TAlertTWSData    = "alerttws.data"     // the "TWS follow" switch state; answer to get, broadcast to ALL windows on set

	// server -> client (news)
	TNewsSummary = "news.summary"

	// server -> client (fundamentals)
	TFundamentalsData = "fundamentals.data"

	// server -> client (on-demand row snapshot)
	TRowData = "row.data"

	// server -> client (daily trend)
	TTrendData = "trend.data"

	// server -> client (RTPR Impact Score, attaches to an article by id)
	TNewsScore = "news.score"

	// server -> client (tape)
	TTapeStat    = "tape.stat"
	TTapePrints  = "tape.prints" // batched raw time-and-sales prints, routed per watching window
	TTapeReadout = "tape.readout"
	TTapeRegime  = "tape.regime" // instant, free local-verdict alert on a regime shift
	TTapeCard    = "tape.card"   // completed tape-reading card (local or Claude)

	// server -> client (Claude's structured read of a bull-flag setup, fired
	// when a symbol enters a scan that filters on the flag fields)
	TSetupRead = "setup.read"

	// server -> client (live chart)
	TChartBar = "chart.bar"

	// server -> client (live IBKR borrow availability for the selected symbol)
	TShortLive = "short.live"

	// server -> client (activity log)
	TScanEnter = "scan.enter"

	// server -> client: today's symbol blacklist (internal/blacklist)
	TBlacklist = "blacklist"
	// client -> server
	TBlacklistAdd    = "blacklist.add"    // {sym}: silence sym's alerts until 00:00 ET
	TBlacklistRemove = "blacklist.remove" // {sym}: take sym off today's list
)

// RowJSON is a null-aware wire row. Keys are metric field names; values are
// JSON-ready scalars (numbers/strings/bools) or nil for null. Changed-only ticks
// carry a subset of keys; full ticks carry every column of the scan. The scan
// layer builds these from core.Row, mapping the Null sentinel and nil pointers
// to JSON null.
type RowJSON = map[string]any

// ScanTick is the workhorse server->client message. Order is the authoritative
// sorted symbol list (<= Limit); Rows carries full bodies only for symbols that
// changed since the last tick, unless Full is true (snapshot / resync).
type ScanTick struct {
	ID    string             `json:"id"`
	Seq   uint64             `json:"seq"`
	AsOf  int64              `json:"asOf"`
	Full  bool               `json:"full"`
	Order []string           `json:"order"`
	Rows  map[string]RowJSON `json:"rows"`
}

// AlertMsg fires on scan entry or threshold cross; Row is the triggering
// snapshot so the UI can log why and play Sound.
type AlertMsg struct {
	ScanID string  `json:"scanId"`
	Sym    string  `json:"sym"`
	Kind   string  `json:"kind"` // "enter" | "cross"
	At     int64   `json:"at"`
	Sound  string  `json:"sound"`
	Row    RowJSON `json:"row"`
}

// TwsAutoMsg reports what the server did with one alert of a scan that has
// AlertConfig.SendToTWS on: moved the linked TWS window to Sym (Sent) or left it
// alone (Reason says why, e.g. "you picked ABC 8s ago"). Display only -- TWS is
// reached through the display-group link, never an order.
type TwsAutoMsg struct {
	Sym      string `json:"sym"`
	ScanID   string `json:"scanId"`
	ScanName string `json:"scanName"`
	Sent     bool   `json:"sent"`
	Reason   string `json:"reason,omitempty"` // why it was not sent (empty when Sent)
	At       int64  `json:"at"`               // unix ms
}

// NewsMsg pushes freshly polled headlines.
type NewsMsg struct {
	Items []NewsItem `json:"items"`
}

// SourceHealth is one data source's current state for the status bar.
type SourceHealth struct {
	State  string `json:"state"`            // ok | failing | off
	Msg    string `json:"msg,omitempty"`    // last failure message while failing
	Since  int64  `json:"since,omitempty"`  // epoch ms the current state began
	LastOK int64  `json:"lastOk,omitempty"` // epoch ms of the last success
}

// HealthReporter is what a data source calls to report its own state; a nil
// reporter is always a no-op for callers (guard with != nil).
type HealthReporter interface {
	SourceOK(name string)
	SourceFail(name, msg string)
}

// StatusMsg drives the UI status bar.
type StatusMsg struct {
	Session  string `json:"session"`  // pre | regular | post | closed
	Feed     string `json:"feed"`     // live | reconnecting | down
	Tracked  int    `json:"tracked"`  // tickers in the engine
	MsgRate  int    `json:"msgRate"`  // ingest events/sec
	Baseline string `json:"baseline"` // ready | building
	HasKey   bool   `json:"hasKey"`   // is an API key configured?
	TwsLink  string `json:"twsLink"`  // off | linked | waiting
	ClockMs  int64  `json:"clockMs"`

	// Counts is the live match count per scan id, so the UI can badge every
	// tab — including the ones it is not currently streaming rows for.
	Counts map[string]int `json:"counts,omitempty"`

	// Sources is each tracked data source's current health (bzpush, rtpr,
	// rtprAlerts, edgar, massive), so a source silently rejecting its token
	// (observed: RTPR's "Invalid or inactive API key" on every poll) is
	// visible on the status bar instead of invisible.
	Sources map[string]SourceHealth `json:"sources,omitempty"`
}

// ErrorMsg reports a failed request; Ref echoes the offending request's id.
type ErrorMsg struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Ref  int64  `json:"ref,omitempty"`
	Sym  string `json:"sym,omitempty"` // symbol the failed request was for (chart.get / fundamentals.get)
}

// ScanListMsg is sent on hello with all persisted scans.
type ScanListMsg struct {
	Scans []ScanDef `json:"scans"`
}

// --- inbound payloads ---

type ViewSetMsg struct {
	ScanIDs []string `json:"scanIds"`
}

type ScanRefMsg struct {
	ID string `json:"id"`
}

type NewsViewMsg struct {
	Mode    string   `json:"mode"` // "all" | "tickers"
	Tickers []string `json:"tickers,omitempty"`
}

type SettingsMsg struct {
	TickHz int `json:"tickHz,omitempty"`
}

// SetKeyMsg carries the Massive API key pasted in the UI settings panel.
type SetKeyMsg struct {
	Key string `json:"key"`
}

// NewsSummarizeMsg asks the server to produce (or fetch the cached) Claude
// summary of one news article / SEC filing.
type NewsSummarizeMsg struct {
	ID string `json:"id"`
}

// NewsSummaryMsg returns the summary (or the failure) for a news article.
// Model names which engine produced Summary — the "local" sentinel
// (summarize.LocalModel) for the free extractive/rule-based read, or a real
// Claude model string — so the client can tag the summary LOCAL vs CLAUDE
// and knows whether a Claude upgrade is still on offer. Empty on a failure
// (Err set instead).
type NewsSummaryMsg struct {
	ID      string `json:"id"`
	Summary string `json:"summary,omitempty"`
	Model   string `json:"model,omitempty"`
	Err     string `json:"err,omitempty"`
}

// ChartGetMsg requests full intraday bars for a symbol (click-to-expand chart).
type ChartGetMsg struct {
	Sym string `json:"sym"`
}

// NewsForMsg requests a symbol's archived news history; the reply is a normal
// TNews frame (the client merges by article id), pushed to the requester only.
type NewsForMsg struct {
	Sym string `json:"sym"`
}

// TrendGetMsg requests one symbol's daily-trend context for the symbol panel.
type TrendGetMsg struct {
	Sym string `json:"sym"`
}

// TrendDataMsg answers trend.get. Ref nil = unknown (insufficient history).
type TrendDataMsg struct {
	Sym string    `json:"sym"`
	Ref *TrendRef `json:"ref"`
}

// NewsScoreMsg attaches an RTPR Impact Score to an article already broadcast
// (or archived) by id — the client patches its matching NewsItem in place.
type NewsScoreMsg struct {
	ArticleID string `json:"articleId"`
	Sym       string `json:"sym"`
	Score     int    `json:"score"`
	Tier      string `json:"tier"`
	EventType string `json:"eventType"`
	Direction string `json:"direction"`
}

// ChartDataMsg returns the intraday OHLC bars for the requested symbol, plus the
// prior session's official close (0 if unavailable) for the gap reference line.
type ChartDataMsg struct {
	Sym       string  `json:"sym"`
	Bars      []Bar   `json:"bars"`
	PrevClose float64 `json:"prevClose"`
}

// FundamentalsGetMsg requests SEC-filed fundamentals for a symbol (fetched on
// selection, like ChartGetMsg).
type FundamentalsGetMsg struct {
	Sym string `json:"sym"`
}

// ChartBarMsg is one live per-second aggregate for the selected symbol; the
// client folds it into the trailing candle so the chart keeps moving after its
// initial REST snapshot. Vol is that second's volume (not cumulative).
type ChartBarMsg struct {
	Sym  string  `json:"sym"`
	T    int64   `json:"t"` // aggregate window start, epoch ms
	O    float64 `json:"o"`
	H    float64 `json:"h"`
	L    float64 `json:"l"`
	C    float64 `json:"c"`
	V    int64   `json:"v"`
	VWAP float64 `json:"vwap"` // day VWAP, for the panel's live stat
}

// TapeWatchMsg points the tape engine at a symbol (empty Sym = stop watching,
// which unsubscribes the trade/quote channels).
type TapeWatchMsg struct {
	Sym string `json:"sym"`
}

// ChartWatchMsg replaces the sending window's whole live-chart symbol set (the
// charts-grid window's counterpart of TapeWatchMsg). Empty/nil Syms clears it.
// Unlike tape.watch this drives ONLY the ingest chart tap (A.* aggregates the
// feed already carries) — no per-symbol trade/quote subscriptions, no TWS
// targeting.
type ChartWatchMsg struct {
	Syms []string `json:"syms"`
}

// FlagEventsGetMsg requests one ET day of the 9EMA flag scan's signal
// activity (internal/flaglog). Empty Date = today (ET); otherwise YYYY-MM-DD.
type FlagEventsGetMsg struct {
	Date string `json:"date"`
}

// FlagEvent is one signal of one symbol as recorded for the Signal window's
// activity list (internal/flaglog): a market-wide 9EMA flag scan event (one
// emaflag.Event), a GREEN DILDO / 9 EMA PULLBACK / PB CANCELLED (the engine's
// alert 1 or flagscan's dildopb events) or a VOLUME CLIMAX / BOTTOM CONFIRMED /
// CLIMAX FAILED (the engine's climax judge). Identity is (Sym, Ts, Kind).
type FlagEvent struct {
	Ts  int64  `json:"ts"` // the event's bar start (ms)
	At  int64  `json:"at"` // server ms when first recorded
	Sym string `json:"sym"`
	// Kind: the emaflag kinds impulse, flat, emaNear, entry, add, earlyBreak,
	// failed, exit, slam; the dildopb kinds dildo, dildoPb, dildoCancel; the
	// climax kinds climax, bottom, climaxFail.
	Kind  string   `json:"kind"`
	Label string   `json:"label"`
	Price *float64 `json:"price"`          // nil when the engine had no finite price
	Note  string   `json:"note,omitempty"` // one line of what the rule measured (dildo / climax kinds; "" for the flag kinds)
	// Judged is, on a dildoPb / dildoCancel row, the bar start (ms) of the
	// dildo that verdict judged (0 = unknown: a row recorded before it was
	// stored, or any other kind). Server-side only (flaglog rebuilds its
	// one-verdict-per-dildo guard from it after a restart): not sent to clients.
	Judged int64 `json:"-"`
}

// FlagEventsMsg carries flag-scan activity. As flagevents.data (the answer to
// flagevents.get): Date is the ET date served (YYYY-MM-DD), Dates the dates
// that have events (newest first, at most 30, always including today's ET date
// even when empty), Events that day's events oldest first (by Ts, then
// recording order). As flagevents.add (broadcast): Date is today and Events
// only the NEW ones; Dates is omitted.
type FlagEventsMsg struct {
	Date   string      `json:"date"`
	Dates  []string    `json:"dates"`
	Events []FlagEvent `json:"events"`
}

// WatchlistMsg carries the ordered watchlist, both directions: watchlist.set
// (client replaces the whole list) and watchlist.data (server's reply /
// broadcast after a change).
type WatchlistMsg struct {
	Syms []string `json:"syms"`
}

// AlertTWSMsg is the global "TWS follow" switch (alerttws.set in, alerttws.data
// out). Enabled=false stops alert-driven TWS Shows for every scan.
type AlertTWSMsg struct {
	Enabled bool `json:"enabled"`
}

// ChartGridMsg carries the charts-grid layout as an OPAQUE JSON blob, both
// directions (chartgrid.set in, chartgrid.data out). The client owns the
// shape; the server only stores and echoes it.
type ChartGridMsg struct {
	Layout json.RawMessage `json:"layout"`
}

// ReviewGetMsg requests one persisted past-session snapshot for the morning
// review window. Empty Date = the newest date on hand; empty Session defaults
// to "post" (the after-hours session is what the morning watchlist is built
// from). Session is the wire label ("pre" | "regular" | "post").
type ReviewGetMsg struct {
	Date    string `json:"date,omitempty"`
	Session string `json:"session,omitempty"`
}

// ReviewDataMsg answers review.get. Date/Session echo what was actually
// resolved (the newest date when the request left Date empty); Dates is always
// the full picker list, newest first, so the client can offer other days.
// Rows carries RAW per-symbol values — every percentage (after-hours gain =
// (post.close - rth.close) / rth.close of the SAME day, day change vs
// prevClose, ...) is derived client-side from these. Empty Rows with a
// resolved Date means the scanner genuinely observed no trades for that
// session; an empty Date means no snapshot history exists at all — both are
// honest "no data", never fabricated.
type ReviewDataMsg struct {
	Date    string               `json:"date"`
	Session string               `json:"session"`
	Dates   []string             `json:"dates"`
	Rows    []SessionSnapshotRow `json:"rows"`
}

// TapeReadMsg asks Claude to interpret the current order flow for a symbol.
type TapeReadMsg struct {
	Sym string `json:"sym"`
}

// TapeReadoutMsg carries Claude's tape interpretation (or the failure).
type TapeReadoutMsg struct {
	Sym  string `json:"sym"`
	Text string `json:"text,omitempty"`
	Err  string `json:"err,omitempty"`
	At   int64  `json:"at"`
}

// RowGetMsg requests a one-off row snapshot for a symbol — used by a pop-out
// tape window, which has no scan tiled and so no other way to get header
// stats (Last/Chg5m/Vol/Rvol/Vwap) for the symbol it's watching.
type RowGetMsg struct {
	Sym     string   `json:"sym"`
	Columns []string `json:"columns"`
}

// RowDataMsg answers row.get. Row is nil if the symbol has no current row
// (e.g. outside the tracked universe).
type RowDataMsg struct {
	Sym string  `json:"sym"`
	Row RowJSON `json:"row"`
}

// SetupRead is Claude's structured read of ONE bull-flag / tight-consolidation
// setup — produced when a symbol enters a scan that filters on the flag fields
// (FieldFlagScore … FieldFlagBars), so the deterministic nightly pattern pass
// has already done the whole-market filtering and Claude only ever sees the
// shortlist.
//
// Quality is 1-5 on a successful read and 0 ONLY on a failed one (Err set):
// like the tape reader's ControlRead, every failure path still returns a valid,
// populated SetupRead rather than a zero struct, so the UI never breaks.
//
// AsOf is the FlagRef.AsOf the read was measured against, NOT when the read
// ran (that is Ts, unix millis, this repo's convention). It travels with the
// read because it is the date component of the read's cache key: a read is
// reusable exactly as long as the nightly geometry it described is still the
// current one.
type SetupRead struct {
	Sym            string   `json:"sym"`
	Ts             int64    `json:"ts"`      // when the read ran, unix millis
	Quality        int      `json:"quality"` // 1 (broken) .. 5 (textbook); 0 = failed read
	Verdict        string   `json:"verdict"`
	WhatsHappening string   `json:"whatsHappening"`
	Risks          []string `json:"risks,omitempty"`
	Invalidation   string   `json:"invalidation,omitempty"`
	Model          string   `json:"model"`
	Err            string   `json:"err,omitempty"` // non-empty if the read failed / was refused
	AsOf           string   `json:"asOf"`          // the FlagRef.AsOf this read describes
}

// SetupReadMsg is the setup.read wire message. It is an ALIAS of SetupRead, not
// a second struct: the tape reader needs two shapes (summarize.ControlRead is
// an untagged internal handoff, TapeCardMsg adds the Sym/Trigger/Headline the
// read itself doesn't carry) but a SetupRead already carries every field the
// client needs, so duplicating it would only create two things to keep in sync.
type SetupReadMsg = SetupRead

// ─── Today's symbol blacklist (internal/blacklist) ──────────────────────────

// BlacklistMsg is the blacklist frame: today's blacklisted symbols (sorted,
// never null), sent on hello and after every change.
type BlacklistMsg struct {
	Syms []string `json:"syms"`
}

// BlacklistSymMsg is the blacklist.add / blacklist.remove request.
type BlacklistSymMsg struct {
	Sym string `json:"sym"`
}
