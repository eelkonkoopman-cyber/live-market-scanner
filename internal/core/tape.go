package core

// This file holds the core types for the ported tape reader: the Go mirror of
// the standalone Python app's models.py (Trade/Quote already live in
// reference.go) plus the feature-bundle and verdict shapes engine.py computes
// (book_stats, local_verdict, claude_snapshot). See internal/tape (regime
// detection + verdict cascade) and internal/summarize (Claude reader) for the
// stages that build these; this file is types only.

// BookStats is the inside-size dynamics + depth-imbalance bundle: the Go
// mirror of engine.py's book_stats(). Most counts are windowed over the last
// ~30s of book updates; BidRefills/AskRefills/BidAbsorbedVol/AskAbsorbedVol
// are live (unwindowed) — they describe the CURRENT inside level since it was
// last established, and reset whenever that price itself changes.
type BookStats struct {
	// Windowed (~30s) event counts.
	BidAbsorb int `json:"bidAbsorb"` // bid hit by aggressive trades, then refreshed (the classic "refreshing bid")
	AskAbsorb int `json:"askAbsorb"`
	BidAdd    int `json:"bidAdd"` // size added to a quiet quote nobody is hitting — weak signal, not absorption
	AskAdd    int `json:"askAdd"`
	BidPull   int `json:"bidPull"` // size vanished with no trade — that side pulling away
	AskPull   int `json:"askPull"`
	BidUp     int `json:"bidUp"` // inside quote stepped to a new price, toward or away
	BidDown   int `json:"bidDown"`
	AskUp     int `json:"askUp"`
	AskDown   int `json:"askDown"`

	// Live (unwindowed) state of the current inside level.
	BidRefills     int     `json:"bidRefills"`
	AskRefills     int     `json:"askRefills"`
	BidAbsorbedVol float64 `json:"bidAbsorbedVol"` // volume sold into the current bid while it held
	AskAbsorbedVol float64 `json:"askAbsorbedVol"` // volume bought from the current offer while it held

	BidDepth float64 `json:"bidDepth"` // summed size across the visible L2 bid ladder
	AskDepth float64 `json:"askDepth"`
	// DepthImbalance is -1 (offer heavy) .. +1 (bid heavy) across the visible
	// L2 ladder; omitted/zero when there is no depth data (see
	// TapeStat.HasDepth) as well as when the book is genuinely balanced.
	DepthImbalance  float64 `json:"depthImbalance,omitempty"`
	InsideImbalance float64 `json:"insideImbalance"` // same, but from the L1 bid/ask sizes only
}

// TapeVerdict is the "who is in control" read — the Go mirror of engine.py's
// local_verdict() return dict (and the shape a structured Claude read is
// normalized into, when Source is "api" instead of "local").
type TapeVerdict struct {
	Control    string   `json:"control"` // "buyers" | "sellers" | "balanced"
	Confidence float64  `json:"confidence"`
	Text       string   `json:"text"`               // short, speakable summary
	Evidence   []string `json:"evidence,omitempty"` // supporting detail, shown on screen only
	Source     string   `json:"source"`             // "local" | "api"
}

// DepthLevel is one price/size rung of an L2 book ladder (best level first).
type DepthLevel struct {
	Price float64 `json:"price"`
	Size  float64 `json:"size"`
}

// DepthUpdate is a market-depth push from internal/twslink: either a fresh
// book snapshot for Sym (Bids/Asks non-empty, Note empty) or an explanation
// of why depth isn't available (Note set, Bids/Asks empty) -- e.g. a TWS
// error. No JSON tags: an internal handoff type, not a wire message.
type DepthUpdate struct {
	Sym  string
	Bids []DepthLevel
	Asks []DepthLevel
	Note string
}

// CVDPoint is one sample of the cumulative-volume-delta chart.
type CVDPoint struct {
	Ts    int64 `json:"ts"` // epoch ms
	Value int64 `json:"value"`
}

// TapeSnapshotPrint is one recent trade print inside a TapeSnapshot — price,
// size, aggressor side and a block flag. Unlike Print (the tape-panel feed
// shape), it carries no timestamp and no Notional, matching the shape
// engine.py's claude_snapshot() actually hands to Claude for recent_prints.
type TapeSnapshotPrint struct {
	Price float64 `json:"price"`
	Size  int64   `json:"size"`
	Side  string  `json:"side"` // buy | sell | neutral
	Block bool    `json:"block"`
}

// TapeSnapshot is the full feature bundle handed to Claude on a regime shift —
// the Go mirror of engine.py's claude_snapshot(). Unlike every other type in
// this package, its JSON tags match the Python dict's own keys (snake_case,
// e.g. "cvd_session", "delta_10s") rather than this file's usual camelCase:
// the structured Claude-reader stage's ported system prompt names these exact
// fields, and it must line up with the JSON actually sent to the model.
//
// Vwap/SessionHigh/SessionLow are left zero-valued by the engine's snapshot
// builder — a later stage fills them in from the existing per-symbol row
// lookup, which already tracks VWAP/session-high/session-low.
type TapeSnapshot struct {
	Symbol   string `json:"symbol"`
	Trigger  string `json:"trigger"`
	Headline string `json:"headline"`

	LastPrice float64 `json:"last_price"`
	Bid       float64 `json:"bid"`
	Ask       float64 `json:"ask"`
	Spread    float64 `json:"spread"`
	BidSize   int64   `json:"bid_size"`
	AskSize   int64   `json:"ask_size"`

	Vwap        float64 `json:"vwap"`         // placeholder; populated later from the row lookup
	SessionHigh float64 `json:"session_high"` // placeholder
	SessionLow  float64 `json:"session_low"`  // placeholder

	CVDSession int64 `json:"cvd_session"`
	Delta10s   int64 `json:"delta_10s"`
	Delta60s   int64 `json:"delta_60s"`
	BuyVol60s  int64 `json:"buy_vol_60s"`
	SellVol60s int64 `json:"sell_vol_60s"`

	Prints10s            int     `json:"prints_10s"`
	PrintsPerSecNow      int     `json:"prints_per_sec_now"`
	PrintsPerSecBaseline float64 `json:"prints_per_sec_baseline"`

	BlockThreshold  float64 `json:"block_threshold"`
	MedianPrintSize float64 `json:"median_print_size"`
	ControlScore    float64 `json:"control_score"` // 60s buy/sell imbalance, -1 (sellers) .. +1 (buyers)

	Book         BookStats           `json:"book"`
	BookBids     []DepthLevel        `json:"book_bids"` // top-N L2 bids, best first (empty when not entitled)
	BookAsks     []DepthLevel        `json:"book_asks"`
	RecentPrints []TapeSnapshotPrint `json:"recent_prints"`
}

// RegimeEvent is a candidate regime shift detected locally — the internal
// engine-to-orchestrator handoff type, not itself a wire message. It mirrors
// models.py's RegimeEvent, plus ClaudeOn (whether a Claude read should be
// attempted for it) and a starter Verdict (local_verdict(), always available
// immediately, before/instead of any Claude call completes).
type RegimeEvent struct {
	Sym      string
	Ts       int64 // unix millis
	Trigger  string
	Headline string
	Verdict  TapeVerdict
	ClaudeOn bool
	Snapshot TapeSnapshot
}

// TapeReadRecord is one archived tape-reading row — the full round-trip shape
// returned by persist.TapeArchive.Recent. It combines a regime event's
// identity (Sym/Ts/Trigger/Headline/Snapshot) with the completed read's
// verdict (Control/Confidence/Evidence, from whichever TapeVerdict was final —
// the local starter or Claude's normalized read) plus the remaining
// Claude/local-read fields that have no home on TapeVerdict or RegimeEvent.
// The Go mirror of what recorder.py's record_read(event, read) writes to its
// per-symbol "reads" table.
type TapeReadRecord struct {
	Sym            string
	Ts             int64
	Trigger        string
	Headline       string
	Control        string
	Confidence     float64
	WhatsHappening string
	Evidence       []string
	WatchFor       string
	Model          string
	Err            string
	Snapshot       TapeSnapshot
}

// TapeCardMsg broadcasts a completed tape-reading card for a regime event —
// either the instant local verdict standing in alone, or Claude's structured
// read once it returns (Model distinguishes which). Control carries the same
// "buyers" | "sellers" | "balanced" verdict the card's own read produced
// (ev.Verdict.Control for a local card, the normalized Claude read's Control
// otherwise — see runTapeRegimeOrchestrator), matching the Python source's
// card, which spreads the read's control alongside it. Err carries a failed
// Claude call, so the UI knows the local verdict already sent via
// TapeRegimeMsg remains the authoritative read.
type TapeCardMsg struct {
	Sym            string   `json:"sym"`
	Ts             int64    `json:"ts"`
	Trigger        string   `json:"trigger"`
	Headline       string   `json:"headline"`
	Model          string   `json:"model"`   // "local" | the Claude model id
	Control        string   `json:"control"` // "buyers" | "sellers" | "balanced"
	Confidence     float64  `json:"confidence"`
	WhatsHappening string   `json:"whatsHappening"`
	Evidence       []string `json:"evidence,omitempty"`
	WatchFor       string   `json:"watchFor,omitempty"`
	Err            string   `json:"err,omitempty"`
}

// TapeRegimeMsg is the instant, free (local-verdict) alert fired the moment a
// regime shift is detected, before any Claude read completes — see
// TapeCardMsg for the (possibly upgraded) follow-up.
type TapeRegimeMsg struct {
	Sym      string      `json:"sym"`
	Ts       int64       `json:"ts"`
	Trigger  string      `json:"trigger"`
	Headline string      `json:"headline"`
	Verdict  TapeVerdict `json:"verdict"`
}

// TapePrintsMsg is one flushed batch (~4 Hz) of raw time-and-sales prints for
// a tape-watched symbol — the per-trade feed behind the pop-out tape window's
// scrolling tape, distinct from TapeStat.Prints (notable prints only). Prints
// are oldest first within the batch, each carrying the same Lee-Ready
// aggressor side the CVD/verdict pipeline computed for it. Dropped counts
// prints lost to backpressure since the last delivered batch (batch-cap
// overflow, or a whole batch discarded because the engine's output channel
// was full), so the UI can render an honest gap marker instead of silently
// missing tape.
type TapePrintsMsg struct {
	Sym     string  `json:"sym"`
	Prints  []Print `json:"prints"` // oldest first
	Dropped int     `json:"dropped,omitempty"`
}

// TapeClaudeSetMsg toggles Claude regime reads for a symbol (client -> server).
type TapeClaudeSetMsg struct {
	Sym     string `json:"sym"`
	Enabled bool   `json:"enabled"`
}

// TapeCommentarySetMsg sets the periodic live-commentary interval for a
// symbol, in seconds (0 = off) (client -> server).
type TapeCommentarySetMsg struct {
	Sym     string  `json:"sym"`
	Seconds float64 `json:"seconds"`
}
