package core

// FloatRef is cached share-structure reference for one ticker. Sourced from the
// Ticker Details v3 endpoint and refreshed weekly. True free float is often
// unavailable from Massive/Polygon, so FloatShares may be nil and SharesOut is
// used as a labeled proxy (which overstates float / understates rotation).
type FloatRef struct {
	Ticker      string
	SharesOut   *int64 // nil = unknown
	FloatShares *int64 // nil = unknown (usually)
	AsOf        int64  // unix seconds when fetched
	Source      string // e.g. "details_v3" | "manual"
}

// ShortRef is IBKR short-sale availability for one ticker, parsed from the
// public shortable-stock file (refreshed every 15 minutes). Rates are annual
// percentages: FeeRate is what you PAY to borrow, RebateRate what you EARN on
// the short proceeds (it goes negative on hard-to-borrow names).
type ShortRef struct {
	Ticker     string
	Available  int64   // shortable shares; see Capped
	Capped     bool    // file reported ">10000000" — Available is a floor, not exact
	FeeRate    float64 // annual %, cost to borrow
	RebateRate float64 // annual %, earned on proceeds
	AsOf       int64   // unix seconds of the file's own timestamp
}

// TrendRef is one ticker's daily-chart context, recomputed nightly from
// grouped daily bars. DTrend uses the classic 20-SMA definition: up = close
// above a rising SMA, down = below a falling one, flat = everything else.
// The panel shows the supporting numbers; the scanner filters on DTrend.
type TrendRef struct {
	DTrend int     `json:"dtrend"` // 1 up / 0 flat / -1 down
	Chg20d float64 `json:"chg20d"` // % change over ~20 trading days
	VsSMA  float64 `json:"vsSma"`  // % distance of last close from the SMA
	Days   int     `json:"days"`   // closes the classification actually used
	AsOf   string  `json:"asOf"`   // trade date of the newest close
}

// FlagRef is one ticker's bull-flag / tight-consolidation setup, recomputed
// nightly from daily bars on the same cadence as TrendRef. The shape it
// measures is the classic pole → base: a sharp advance (PoleGain), then a
// TIGHT range (RangePct) that holds most of the gain (Retrace) on drying
// volume (VolDry). Retrace is the discriminator — a deep giveback means the
// advance failed, and it rejects most false positives. The panel shows the
// supporting numbers; the scanner filters on Score/State.
//
// Score and State are float64 (where TrendRef.DTrend is an int) so the scanner
// overlay copies them onto Row without a conversion — every numeric scan field
// resolves through fieldNum, which returns float64.
type FlagRef struct {
	Score float64 `json:"score"` // 0-100 composite setup quality
	// State is 0 = no pattern, 1 = coiling (base still intact). 2 = BREAKOUT
	// is never stored here: it is assigned LIVE by the scanner overlay when a
	// tick's Last clears BaseHigh, because a breakout happens intraday and a
	// nightly classification of it would always be a day late.
	State    float64 `json:"state"`
	PoleGain float64 `json:"poleGain"` // % advance of the pole
	RangePct float64 `json:"rangePct"` // base tightness: (hi-lo)/mid, as a %
	Retrace  float64 `json:"retrace"`  // % of the pole given back
	VolDry   float64 `json:"volDry"`   // base avg volume / pole avg volume (<1 = drying up)
	Bars     int     `json:"bars"`     // length of the base, in daily bars
	// BaseHigh is the base's high price. It is stored (rather than recomputed)
	// because the live overlay needs a level to compare Last against to flip
	// State 1 → 2 — the one field the nightly pass produces purely for
	// intraday use.
	BaseHigh float64 `json:"baseHigh"`
	AsOf     string  `json:"asOf"` // trade date the classification was computed for
}

// EmaFlagRef is one ticker's live 9EMA-flag verdict: the result of the Signal
// window's 1-minute flag state machine (internal/emaflag, a port of
// web/src/signals/emaFlag.ts) over the symbol's bars, as published by the
// flagscan service for the scanner to overlay onto rows (scan fields e9State …
// e9Age, plus e9SlamAge / e9SlamPct). Unlike FlagRef it is a LIVE reference --
// republished about once a second while the symbol is a candidate -- so there
// is no AsOf: the freshness of a ref is the freshness of the last publish.
//
// A symbol present in the published map is "evaluated" (State 0 = evaluated,
// idle); a symbol absent from it is "not a candidate" and the overlay Nulls
// every e9* field for it. The levels follow the engine's NaN-means-null
// convention: Trigger/Stop/DistPct may be NaN (no trigger yet, EMA not seeded)
// and the overlay turns that into Null, so a NaN never reaches the wire.
// Carries no JSON tags: it is an in-process hand-off, never serialized.
type EmaFlagRef struct {
	State    int     // 0 idle / 1 impulse / 2 flat / 3 ready / 4 long
	DistPct  float64 // % of the latest close vs the 9 EMA (positive = above); NaN = unknown
	Trigger  float64 // entry trigger price; NaN = none
	Stop     float64 // protective stop price; NaN = none
	FlagBars int     // flag length in 1-minute bars
	// SinceMs is the epoch-ms timestamp of the event that put the symbol into
	// State (the flag's start for flat, the breakout bar for long); 0 = idle /
	// unknown, which the overlay reads as "no age".
	SinceMs int64
	// The latest 9 EMA SLAM of the symbol's current ET day (emaflag
	// Result.LastSlamTs / LastSlamPct; the service only publishes a symbol that
	// has a bar today, and the engine reports the last ET day, so it is always
	// today's). SlamMs is the slam bar's start, epoch ms; 0 = no slam today, and
	// then SlamPct means nothing (it may be 0 or NaN) -- the overlay Nulls both
	// e9SlamAge and e9SlamPct on SlamMs 0. SlamPct is that bar's close vs its own
	// 9 EMA, % (negative); NaN = unknown.
	SlamMs  int64
	SlamPct float64
	// HodFlagMs is the FLAT bar's start (epoch ms) of the symbol's latest flag
	// today whose run-up made a new high of the day (emaflag
	// Result.LastHodFlagTs); 0 = none, which the overlay reads as Null.
	HodFlagMs int64

	// GREEN DILDO -> 9 EMA PULLBACK (internal/dildopb Result over the same bars:
	// the symbol's last ET day). DildoMs is the latest dildo bar's start, epoch
	// ms; 0 = none today, and then the four measures mean nothing. DildoBodyX /
	// DildoVolX may be +Inf (a prior mean of 0) and every float may be NaN; the
	// overlay Nulls what is not finite.
	DildoMs    int64
	DildoBody  float64 // body, % of the open
	DildoBodyX float64 // body / the prior bars' mean body
	DildoVolX  float64 // volume / the prior bars' mean volume
	DildoDvol  float64 // close x volume, $
	// DildoEarly: the chart-side early verdict of that dildo (dildopb.JudgeEarly
	// on the chart's bars): 1 early, 0 not; meaningless when DildoMs is 0.
	DildoEarly int8
	// PbState is that dildo's pullback (dildopb.PbNone .. PbExpired: 0 none /
	// 1 watching / 2 touching / 3 confirmed / 4 cancelled / 5 expired). TouchMs
	// is the bar that touched the 9 EMA band (the forming bar while touching),
	// PbMs the confirming touch bar (state 3), both bar starts in epoch ms, 0 =
	// none; PbPx is the confirming bar's close (NaN = none).
	PbState int
	TouchMs int64
	PbMs    int64
	PbPx    float64
	// HotOnly: the symbol is tracked ONLY because the engine saw a green dildo
	// on it (flagscan's HOT admission): it is neither a pmVolX candidate nor
	// pinned. The overlay then leaves every e9* field Null and fills only the
	// dildo fields, so a user-built e9 scan never starts showing dildo names.
	HotOnly bool
}

// DildoHit is one green dildo the scanner engine's closed-bar judge found
// (alert 1 of the GREEN DILDO -> 9 EMA PULLBACK signal), handed to the flagscan
// service on a buffered channel (Engine.DildoHits, a non-blocking send) so it
// can admit the symbol as a HOT name and judge the pullback to the chart's
// 9 EMA on the chart's own bars. BarMs is the candle's start (epoch ms); the
// OHLCV is the engine's own 1-minute bar built from the per-second aggregates;
// the measures are dildopb.Hit's (BodyX / VolX may be +Inf). An in-process
// hand-off, never serialized.
type DildoHit struct {
	Sym        string
	BarMs      int64
	O, H, L, C float64
	V          int64
	BodyPct    float64
	BodyX      float64
	VolX       float64
	DollarVol  float64
}

// SignalHit is one market-wide 1-minute ALERT the scanner engine's closed-bar
// judge called, handed to the Signal window's signal history (internal/flaglog)
// on a buffered channel (Engine.SignalHits, a non-blocking send): a GREEN DILDO
// (alert 1), a VOLUME CLIMAX, its BOTTOM CONFIRMED or its CLIMAX FAILED. Kind is
// the chart rule's own event kind (dildopb.KindDildo, climax.KindClimax /
// KindBottom / KindFail), so a history row and the chart's marker share one
// identity (Sym, BarMs, Kind). Only the fields of its kind are set (the rest
// are 0); a multiple may be +Inf (a prior mean of 0). An in-process hand-off,
// never serialized.
type SignalHit struct {
	Sym   string
	Kind  string
	BarMs int64   // the signal bar's START (epoch ms)
	Price float64 // dildo: the close; climax: the low; bottom: the confirming close; climaxFail: the undercutting low

	// GREEN DILDO: the bar's open and close (its floor) and dildopb.Hit's
	// measures. VolX and DollarVol are also the VOLUME CLIMAX's.
	O, C      float64
	BodyPct   float64
	BodyX     float64
	VolX      float64
	DollarVol float64
	// Early (GREEN DILDO): dildopb.JudgeEarly on the engine's own bars: 1 early,
	// 0 not early, -1 unknown (too few bars seen before it).
	Early int8

	// VOLUME CLIMAX: climax.Hit's drop and wick (%), and Mid, the level a later
	// close must beat (also set on a bottom / fail: the climax's midpoint).
	DropPct float64
	WickPct float64
	Mid     float64
	// BOTTOM CONFIRMED / CLIMAX FAILED: the climax low that had to hold, the
	// climax bar's start and the closed bars folded after it (this one included).
	Low      float64
	ClimaxMs int64
	Bars     int

	// BOUNCY BALL SHORT (Kind "bbs"; Price = the breaking close, DollarVol its
	// close x volume): the support, its touches, and the trailing lower-high
	// run (bounce highs, first and last).
	Support   float64
	Touches   int
	Highs     int
	FirstHigh float64
	LastHigh  float64

	// POWER CANDLE (Kind "power"; Price = the close, BodyPct / BodyX / VolX /
	// DollarVol its measures): the top wick, % of the range.
	TopPct float64

	// POWER SETUP (Kind core.KindPowerSetup, and the filter tag of a plain power
	// hit): the three filter values (Null = unknown) and the plan levels.
	PsFromOpen  float64
	PsSlope     float64
	PsGap       float64
	PsEntry     float64
	PsStop      float64
	PsTarget    float64
	PsBaseRange float64 // the base range (%) of the A+ grade (core.PsIsAplus); Null = unknown
	PsAplus     bool    // a power SETUP graded A+ (set by the engine; the zero value is never A+)
	PsSkip      string  // 'skipped: <quiet window>' note text when the filters passed inside a quiet window

	// IGNITION (Kind "ignition"; Price = the close, BodyPct / VolX / DollarVol
	// its measures): the quiet base's range (%) and high.
	BaseRangePct float64
	BaseHigh     float64
	// Fresh (IGNITION): the day change before it (the bar's open vs the
	// previous close) was at most +3%: 1 fresh, 0 not, -1 unknown.
	Fresh int8
	// Skip (IGNITION): why the ignition fails the skip rules (engine ignSkip),
	// e.g. "up 7.2% on the day"; "" = it passes and alerts.
	Skip string

	// 9EMA VWAP CROSS (Kinds "vwapBull" / "vwapBear"; Price = the 1-minute
	// close, BarMs the firing 1-minute bar's start): the 9 EMA of 1-minute closes and
	// the session VWAP at the cross, and the gap (%) between them.
	Ema  float64
	Vwap float64
	Dist float64

	// VOLUME BREAK (Kinds "volBreak" / "volBreakEntry"; Price = the break close /
	// the entry EMA): the range high that broke. Ema / VolX: the break's.
	Level float64
}

// PmVolEntry is one pre-market volume leader: a tracked symbol whose pre-market
// volume is at least a threshold multiple of its 10-day average daily volume.
// It is what Engine.PmVolLeaders hands the flagscan service as its candidate
// list; PmVolX is exactly the value the scan field pmVolX carries for the
// symbol (the engine derives both through one method).
type PmVolEntry struct {
	Sym    string
	PmVolX float64
}

// SetupContext is everything the structured Claude setup reader needs to read
// ONE bull-flag setup: the nightly geometry (Flag), the daily bars it was
// measured on, and the live row numbers worth grounding the read in so it can
// say where price sits versus Flag.BaseHigh right now. It is an internal
// handoff type — the reader renders it as a text prompt, never as JSON — so
// like RegimeEvent it carries no JSON tags.
//
// The live fields are copied straight off core.Row and keep ITS conventions:
// Last/ChgDay/Rvol use the Null sentinel (see IsNull) for "not available", and
// Float is a nil pointer when unknown (and may be a shares-out proxy).
type SetupContext struct {
	Sym  string
	Flag FlagRef
	Bars []DailyBar // recent daily bars, oldest → newest

	Last   float64
	ChgDay float64
	Rvol   float64
	Float  *int64
}

// ShortLive is real-time borrow availability for the SELECTED symbol, from the
// TWS API's generic tick 236. Unlike the 15-minute bulk file this is exact and
// current, but it costs one IBKR market-data line, so only one symbol is
// subscribed at a time.
type ShortLive struct {
	Sym string `json:"sym"`
	// Indicator is IB's "Shortable" tick: >2.5 = at least 1000 shares
	// available, >1.5 = available if shares can be located, <=1.5 = not
	// available. 0 = not received yet.
	Indicator float64 `json:"indicator"`
	// Shares is the exact count (TWS build 974+); -1 = not reported.
	Shares int64  `json:"shares"`
	At     int64  `json:"at"`
	Err    string `json:"err,omitempty"`
}

// ShortLiveLabel renders IB's indicator bands as the words IB documents.
func ShortLiveLabel(indicator float64) string {
	switch {
	case indicator > 2.5:
		return "available"
	case indicator > 1.5:
		return "locatable"
	case indicator > 0:
		return "not shortable"
	default:
		return ""
	}
}

// BucketCount is the number of 5-minute buckets in the full extended session
// (04:00–20:00 ET = 960 min / 5).
const BucketCount = 192

// BaselineTable is the RAM-resident RVOL baseline: per ticker, the trailing-mean
// CUMULATIVE session volume at each 5-minute bucket boundary, plus the sample
// count (sessions contributing) used for the low-confidence guard. Swapped
// atomically into the engine after each nightly recompute.
type BaselineTable struct {
	Cum   map[string][BucketCount]float64
	Count map[string][BucketCount]uint16
}

// Get returns the baseline cumulative volume for a ticker at a bucket, and false
// if absent, out of range, or non-positive (treated as null RVOL).
func (b *BaselineTable) Get(ticker string, bucket int) (float64, bool) {
	if b == nil || bucket < 0 || bucket >= BucketCount {
		return 0, false
	}
	// Index the map value in place: "cum, ok := b.Cum[ticker]" would copy the
	// whole [BucketCount]float64 (1.5 KB) on every call.
	if v := b.Cum[ticker][bucket]; v > 0 {
		return v, true
	}
	return 0, false
}

// With returns a copy of b in which each listed ticker's rows are replaced by
// part's (or dropped when part has none). b itself is never modified: the
// engine's merger reads it concurrently. A nil b counts as empty.
func (b *BaselineTable) With(part *BaselineTable, tickers []string) *BaselineTable {
	out := &BaselineTable{Cum: map[string][BucketCount]float64{}, Count: map[string][BucketCount]uint16{}}
	if b != nil {
		out.Cum = make(map[string][BucketCount]float64, len(b.Cum)+len(tickers))
		out.Count = make(map[string][BucketCount]uint16, len(b.Count)+len(tickers))
		for k, v := range b.Cum {
			out.Cum[k] = v
		}
		for k, v := range b.Count {
			out.Count[k] = v
		}
	}
	for _, t := range tickers {
		delete(out.Cum, t)
		delete(out.Count, t)
		if part == nil {
			continue
		}
		if v, ok := part.Cum[t]; ok {
			out.Cum[t] = v
		}
		if v, ok := part.Count[t]; ok {
			out.Count[t] = v
		}
	}
	return out
}

// CountAt returns how many historical sessions contributed to a ticker/bucket
// baseline. The RVOL guard treats counts below a minimum as null.
func (b *BaselineTable) CountAt(ticker string, bucket int) int {
	if b == nil || bucket < 0 || bucket >= BucketCount {
		return 0
	}
	return int(b.Count[ticker][bucket]) // in place, as in Get; absent reads 0
}

// MarketDay is one row of the trading calendar (ET). Session boundaries are
// minutes since ET midnight; LastBucket is the highest valid 5-min bucket index
// for the date (191 on a full day, ~107 on a 13:00 half-day).
type MarketDay struct {
	Date         string // "YYYY-MM-DD" ET
	IsTrading    bool
	IsHalfDay    bool
	SessOpenMin  int // 240  (04:00)
	RegOpenMin   int // 570  (09:30)
	RegCloseMin  int // 960  (16:00), 780 on half days
	SessCloseMin int // 1200 (20:00), ~1020 on half days
	LastBucket   int // 191 full, ~107 half
}

// NewsItem is one headline from the REST news poller, indexed per ticker and
// joined into scan rows (News / NewsAge) and pushed in the news panel.
type NewsItem struct {
	ID        string   `json:"id"`
	Tickers   []string `json:"tickers"`
	Headline  string   `json:"headline"`
	Source    string   `json:"source"`
	URL       string   `json:"url"`
	TS        int64    `json:"ts"`                // published, epoch ms
	Sentiment string   `json:"sentiment"`         // positive | neutral | negative | ""
	Teaser    string   `json:"teaser,omitempty"`  // short description/teaser, if the source provides one
	HasBody   bool     `json:"hasBody,omitempty"` // full article body cached server-side (summarizable without fetching)

	// RTPR Impact Score (Beta) — arrives ~45s after publication on their alerts
	// WebSocket, so it attaches to an article we typically already have. Score
	// nil = not scored (the vast majority of articles: RTPR only scores wire
	// items, and only ones clearing the account's Connect-tab threshold).
	Score     *int   `json:"score,omitempty"`
	ScoreTier string `json:"scoreTier,omitempty"` // e.g. "high"
	EventType string `json:"eventType,omitempty"` // e.g. "fda_approval"
	Direction string `json:"direction,omitempty"` // bullish | bearish | neutral
}

// Ticker is a row of the ticker reference, used to build the regular-securities
// universe (filter by Type: CS, ADRC, ETF).
type Ticker struct {
	Symbol string
	Type   string // CS | ADRC | ETF | WARRANT | UNIT | ...
	Name   string
	Active bool
}

// FinQuarter is one reporting period's SEC-filed figures — merged from the
// income statement (Revenue), balance sheet (Cash), and cash flow statement
// (OperatingCF), keyed by PeriodEnd. Any field may be nil if that statement
// didn't report it for this period.
type FinQuarter struct {
	FiscalYear    int      `json:"fy"`
	FiscalQuarter int      `json:"fq"`
	PeriodEnd     string   `json:"periodEnd"`  // YYYY-MM-DD
	FilingDate    string   `json:"filingDate"` // YYYY-MM-DD
	Revenue       *float64 `json:"revenue,omitempty"`
	Cash          *float64 `json:"cash,omitempty"`        // cash_and_equivalents, balance sheet
	OperatingCF   *float64 `json:"operatingCF,omitempty"` // net cash from operating activities; negative = burning cash
}

// TapeEvent is one time-and-sales event for the tape-watched symbol: exactly
// one of Trade/Quote is set. Delivered on a single channel so trade/quote
// ordering is preserved (aggressor classification needs the quote that
// prevailed at trade time).
type TapeEvent struct {
	Trade *Trade
	Quote *Quote
}

// Trade is one print from the T.<sym> WebSocket channel.
type Trade struct {
	Sym   string
	Price float64
	Size  int64
	TS    int64   // SIP timestamp, epoch ms
	Conds []int32 // condition codes (reserved for future filtering)
}

// Quote is one NBBO update from the Q.<sym> WebSocket channel. Sizes are in
// SHARES (the wire format sends round lots; the feed layer normalizes).
type Quote struct {
	Sym   string
	Bid   float64
	BidSz int64
	Ask   float64
	AskSz int64
	TS    int64 // epoch ms
}

// Print is a notable (large) trade kept for the tape panel.
type Print struct {
	TS       int64   `json:"ts"`
	Price    float64 `json:"price"`
	Size     int64   `json:"size"`
	Side     string  `json:"side"` // buy | sell | neutral
	Notional float64 `json:"notional"`
}

// TapeStat is the ~1 Hz digest of order flow for the watched symbol — the
// buyers/sellers-in-control read, computed from aggressor-classified prints.
type TapeStat struct {
	Sym         string  `json:"sym"`
	AsOf        int64   `json:"asOf"`
	Watching    bool    `json:"watching"`
	WatchSecs   int     `json:"watchSecs"`
	QuoteRule   bool    `json:"quoteRule"` // true = NBBO classification; false = tick-rule fallback
	Bid         float64 `json:"bid"`
	Ask         float64 `json:"ask"`
	Delta1m     int64   `json:"delta1m"` // buy vol - sell vol
	Delta5m     int64   `json:"delta5m"`
	DeltaTotal  int64   `json:"deltaTotal"` // since watch start
	BuyPct5m    float64 `json:"buyPct5m"`   // % of classified volume at/above ask
	SellPct5m   float64 `json:"sellPct5m"`
	AvgSize5m   float64 `json:"avgSize5m"`
	Rate1m      float64 `json:"rate1m"` // trades/sec over the last minute
	TotalTrades int64   `json:"totalTrades"`
	Prints      []Print `json:"prints,omitempty"` // newest first
	Control     float64 `json:"control"`          // -1 (sellers) .. +1 (buyers)
	Err         string  `json:"err,omitempty"`    // e.g. trades channel not entitled

	// --- tape-reader bundle: regime detection, verdicts, depth (ported from
	// the standalone Python app's engine.py) ---
	Delta10s             int64        `json:"delta10s"` // buy vol - sell vol, last 10s
	Ratio1s              float64      `json:"ratio1s"`  // delta as share of that window's volume, -1..+1
	Ratio10s             float64      `json:"ratio10s"`
	Ratio60s             float64      `json:"ratio60s"`
	PrintsPerSec         int          `json:"printsPerSec"`         // trades/sec, last 1s
	PrintsPerSecBaseline float64      `json:"printsPerSecBaseline"` // trades/sec, trailing 60s
	BlockThreshold       float64      `json:"blockThreshold"`       // size at/above which a print is flagged as a block
	MedianSize           float64      `json:"medianSize"`           // median print size, trailing prints
	CVDSeries            []CVDPoint   `json:"cvdSeries,omitempty"`
	Book                 BookStats    `json:"book"`
	Verdict              TapeVerdict  `json:"verdict"`        // rules-based (or Claude) "who is in control" read
	ClaudeEnabled        bool         `json:"claudeEnabled"`  // Claude regime reads on for this symbol
	CommentarySecs       float64      `json:"commentarySecs"` // periodic Claude commentary interval; 0 = off
	HasDepth             bool         `json:"hasDepth"`
	BookBids             []DepthLevel `json:"bookBids,omitempty"` // top-of-book L2 ladder, best first
	BookAsks             []DepthLevel `json:"bookAsks,omitempty"`
	DepthNote            string       `json:"depthNote,omitempty"` // why depth is unavailable, straight from IBKR
}

// Fundamentals is on-demand SEC-filed financial data for one symbol — fetched
// when the user selects it (not tracked for the whole universe: filings only
// change ~quarterly and most tracked symbols are never inspected in detail).
// BurnQuarterly/RunwayMonths are derived from the newest quarter's operating
// cash flow, deliberately excluding financing activity (e.g. an offering)
// so a dilutive raise can't mechanically shrink the reported burn.
type Fundamentals struct {
	Sym           string       `json:"sym"`
	Quarters      []FinQuarter `json:"quarters"`                // newest first, up to 4
	BurnQuarterly *float64     `json:"burnQuarterly,omitempty"` // $ burned last quarter; nil if cash-flow positive
	RunwayMonths  *float64     `json:"runwayMonths,omitempty"`  // Cash / (Burn/3); nil if not burning or cash unknown
	AsOf          int64        `json:"asOf"`                    // unix seconds when fetched
	Source        string       `json:"source,omitempty"`        // "massive_financials_v1"
}

// InPlayEntry is one entry of the engine's live IN-PLAY ranking (Engine.InPlayLeaders): a
// tracked symbol ranked by its day volume so far (04:00-anchored) over its 10-day average
// daily volume. It is what the auto-trader's PMH break takes its universe from; Pmh and
// Open ride along so it needs no second read of the engine.
type InPlayEntry struct {
	Sym  string
	X    float64 // day volume so far / adv10
	Last float64 // the last price (0 = none yet)
	Pmh  float64 // today's pre-market high once final (frozen at 09:30) and complete; 0 otherwise
	Open float64 // the official day open; 0 = not known
}

// EPRef is one gapper's daily-chart context for the Episodic Pivot scans,
// fetched once per day per candidate: Chg3m is the % change of the previous
// close over ~63 sessions, AdrPct the ADR20 in % (of AdrDays bars). Null = unknown.
type EPRef struct {
	Chg3m   float64
	AdrPct  float64
	AdrDays int
}

// The 9EMA VWAP CROSS alerts' signal history kinds and labels (the bear label
// uses U+2212, the minus sign).
const (
	KindEmaVwapBull  = "vwapBull"
	KindEmaVwapBear  = "vwapBear"
	LabelEmaVwapBull = "VWAP+"
	LabelEmaVwapBear = "VWAP−"
)
