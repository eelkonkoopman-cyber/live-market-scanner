package core

// Agg is one per-second aggregate bar for a symbol, decoded from the Massive
// WebSocket "A" event and pushed by the ingest layer into the engine.
//
// It carries every field the engine needs downstream, per the integration
// review: bar OHLC/VWAP/volume PLUS the day-level accumulators (AccVol, DayVWAP,
// DayOpen) and the bar window timestamps used to place the value in the ring.
//
// NOTE on AccVol (the "av" field): whether it is anchored at 04:00 (extended
// session) or 09:30 (regular open) is verified by the Phase-0 spike. If "av"
// excludes pre-market, the engine must accumulate its own session volume by
// summing per-bar Vol from the first bar at/after 04:00 ET instead of trusting
// AccVol — RVOL correctness depends on the live accumulator and the flat-file
// baseline sharing the 04:00 anchor.
type Agg struct {
	Sym string

	Open  float64 // bar open  (o)
	High  float64 // bar high  (h)
	Low   float64 // bar low   (l)
	Close float64 // bar close == last trade in window (c)
	VWAP  float64 // bar VWAP  (vw)
	Vol   int64   // bar volume (v)

	AccVol  int64   // accumulated day volume (av) — anchor under test
	DayOpen float64 // official day open (op)
	DayVWAP float64 // day VWAP to date (a)

	StartMs int64 // window start, epoch ms (s)
	EndMs   int64 // window end,   epoch ms (e)

	OTC bool // OTC-listed (otc); present only for OTC names
}

// Seed is a startup/reconnect seed row: a snapshot Agg plus the prior official
// close (which the streaming feed doesn't carry but chgDay/gap need). Produced by
// FeedClient.Snapshot; the caller Apply()s the Agg and SeedClose()s PrevClose.
type Seed struct {
	Agg
	PrevClose float64
}

// SessionStats is one symbol's RAW stats over a completed sub-session window
// (pre-market or regular), aggregated from historical minute bars by the
// frozen-slot backfill path and handed to Engine.SeedFrozen when the process
// missed the live session boundary. Deliberately raw — Hi/Lo/Vwap/Close are
// prices and Vol shares, never percentages — because the engine derives
// chg/gap/fade from its own authoritative per-ticker prevClose, through the
// same helper the live boundary freeze uses, so the two derivations cannot
// drift. Zero/negative values mean "unknown": Hi/Lo/Vwap <= 0 leave the
// corresponding frozen fields Null.
type SessionStats struct {
	Hi, Lo, Vwap, Close float64
	Vol                 int64
}

// SessionSnapshotRow is one symbol's RAW stats over a completed sub-session,
// captured by the engine at the live session boundary (the same scope state the
// frozen-slot freeze reads) and persisted for next-morning review. Like
// SessionStats it is deliberately raw — prices and shares, never percentages:
// the after-hours gain, the day change, and every other derived number are
// computed at READ time from these values (and the sibling sessions' rows), so
// there is exactly one derivation and it can never drift from what was stored.
// Zero means "unknown" (a ticker with no observed high stores Hi 0), mirroring
// SessionStats' validity convention. JSON tags because the rows travel to the
// review window verbatim on the wire.
type SessionSnapshotRow struct {
	Sym       string  `json:"sym"`
	Hi        float64 `json:"hi"`
	Lo        float64 `json:"lo"`
	Close     float64 `json:"close"`
	Vwap      float64 `json:"vwap"`
	Vol       int64   `json:"vol"`
	PrevClose float64 `json:"prevClose"`
}

// SessionSnapshot is one completed sub-session's whole-market snapshot: every
// ticker that actually traded in that scope, captured once at the session
// boundary. Date is the ET trading date ("2006-01-02") the session belongs to
// — the 16:00 and 20:00 boundaries stamp the same date as the session they
// close, and the 09:30 boundary stamps the pre-market's own date.
type SessionSnapshot struct {
	Date    string // ET trading date, "2006-01-02"
	Session Session
	Rows    []SessionSnapshotRow
}
