// Package tape is the time-and-sales analytics engine: it classifies every
// print as buyer- or seller-initiated against the prevailing NBBO (quote
// rule, with the tick rule as fallback), maintains rolling order-flow metrics
// (volume delta, %at-ask, large prints) per watched symbol, and emits a ~1 Hz
// TapeStat digest per symbol that drives the UI's buyers/sellers-in-control
// gauge and Claude's on-demand tape reads.
//
// Tracks a small SET of symbols concurrently (one per open browser window),
// not the whole market: whole-market trades would be an order of magnitude
// more data than the per-second aggregates, and each window only needs to
// answer questions about the symbol its own trader is looking at. Per-event
// cost is O(1) regardless of how many symbols are in the set.
package tape

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

const (
	ringSecs     = 900 // 15 minutes of per-second buckets
	printsKept   = 10  // notable prints shown in the panel
	statHz       = time.Second
	quoteFreshMs = 30_000 // quotes older than this -> tick-rule mode

	// A print is "notable" if size >= bigSizeMult x rolling avg trade size, or
	// notional >= bigNotional — whichever fires first. Both thresholds exist
	// because sub-$1 runners hit share-count significance long before dollar
	// significance, and large caps the other way around.
	bigSizeMult = 20.0
	bigNotional = 25_000.0

	// sizesKept caps the trailing-print-size sample used for the median (and
	// therefore blockThreshold) — mirrors engine.py's `_sizes: Deque[float] =
	// deque(maxlen=250)`.
	sizesKept = 250

	// blockMinSize/blockMedianMult mirror engine.py's Settings defaults
	// (block_min_size=500, block_median_mult=8): a print is a "block" if its
	// size is at/above the greater of a fixed floor and a multiple of the
	// trailing median print size, so the bar self-scales to the symbol's own
	// typical clip size.
	blockMinSize    = 500.0
	blockMedianMult = 8.0

	// cvdPointsKept caps the CVD-chart sample series — mirrors engine.py's
	// `CVD_POINTS = 300`.
	cvdPointsKept = 300

	// heartbeatMinSecs/heartbeatMaxSecs bound the live-commentary interval —
	// mirrors server.py's HEARTBEAT_MIN/HEARTBEAT_MAX (0 stays off; anything
	// else is clamped into [15s, 600s]).
	heartbeatMinSecs = 15.0
	heartbeatMaxSecs = 600.0

	// Raw-print stream (Prints/flushPrints): pending prints flush every
	// printFlushEvery on the engine goroutine; one batch carries at most
	// printBatchCap prints (newest kept, overflow counted into Dropped), and
	// the output channel buffers printsOutBuf batches before backpressure
	// starts dropping whole batches (again counted, never blocking apply()).
	printFlushEvery = 250 * time.Millisecond
	printBatchCap   = 500
	printsOutBuf    = 64
)

type bucket struct {
	sec    int64 // unix second this bucket belongs to
	buyV   int64
	sellV  int64
	neutV  int64
	trades int64
}

// symState is the accumulated order-flow state for one watched symbol.
type symState struct {
	sym          string
	watchStart   time.Time
	bid, ask     float64
	bidSz, askSz int64 // last quote's inside sizes -- for book-event classification (book.go)
	quoteAt      int64 // ms of last quote
	lastPrice    float64
	ring         [ringSecs]bucket
	buyTotal     int64
	sellTotal    int64
	trades       int64
	sumSize      int64
	sizes        []int64         // trailing print sizes, capped at sizesKept, for medianSize/blockThreshold
	prints       []core.Print    // newest first
	cvdSeries    []core.CVDPoint // CVD-chart samples, capped at cvdPointsKept, one appended per publish() tick
	errMsg       string

	// micro is a trailing-60s, count-capped (microKept) ring of individual
	// trades in chronological order (oldest first) -- deliberately separate
	// from the per-second bucket ring above. Buckets aggregate away
	// individual-print ordering and price, but buy_sweep/sell_sweep (regime.go)
	// need the last few prints IN ORDER to check same-side-and-price-progressing,
	// and the absorption detector's flat-price check needs the oldest/newest
	// trade price within its window -- neither is recoverable once folded into
	// a per-second volume total. Mirrors the trade-granularity slice of
	// engine.py's `self.trades` deque that _detect_sweep/_detect_absorption read
	// directly, capped for memory (Python's own deque there is unbounded within
	// its 60s prune window).
	micro []microTrade

	// --- Level-1 book-event tracking (book.go); see its doc comment for the
	// classification this state feeds. ---
	tradesAtQuote int64   // st.trades as of the last quote, for the pull-vs-just-traded gate
	bidLevelVol   float64 // volume sold into the current bid while it has held
	askLevelVol   float64 // volume bought from the current offer while it has held
	bidRefills    int     // times the current bid replenished after being hit
	askRefills    int
	bookEvents    []bookEvent // capped at bookEventsKept

	// --- Regime detection (regime.go) ---
	absorbFiredPx float64 // level (bid or ask price) a level-absorption regime event last fired at -- mirrors engine.py's single shared `_absorb_fired_px` exactly (not split per-side): a fire on the other side overwrites this too, so only a same-side refire at the same already-fired level is deduped, matching Python's actual behavior.
	lastRegimeTS  int64   // ms of the last fired regime event, for the shared cooldown gate
	lastSweptDir  string  // "buy" | "sell" | "" -- dedupes repeated sweep fires in the same direction until the direction flips
	claudeEnabled bool    // Claude regime reads on for this symbol; default false. Set via SetClaudeEnabled; fireRegime stamps RegimeEvent.ClaudeOn from it.

	// --- Live commentary / heartbeat (checkHeartbeats below) ---
	commentarySecs float64 // periodic live-commentary interval, seconds; 0 = off, else clamped to [heartbeatMinSecs, heartbeatMaxSecs] by clampCommentary -- mirrors server.py's commentary_secs.
	lastReadAt     int64   // ms of the last completed read (local or Claude) for this symbol, set via NoteReadComplete -- the heartbeat gate measures quiet time from HERE (read COMPLETION), not from detection time, mirroring server.py's `_last_read_wall`.

	// --- Raw-print stream (flushPrints): every classified trade pends here
	// until the next ~4 Hz flush, oldest first; pendingDropped counts prints
	// lost to backpressure since the last delivered batch. ---
	pendingPrints  []core.Print
	pendingDropped int

	// --- Real Level 2 depth from IBKR TWS (internal/twslink), single-target:
	// only Engine.depthTarget ever has hasDepth true here, see SetDepth/
	// SetDepthUnavailable/SetDepthTarget and snapshot() below. ---
	hasDepth  bool
	bookBids  []core.DepthLevel
	bookAsks  []core.DepthLevel
	depthNote string
}

// Engine consumes TapeEvents for whatever symbols are currently watched.
// Single writer: Run owns all state (the syms map included); SetSymbols
// routes through the events channel via a ctl func, same as every other
// mutation, so no locks are needed for the hot fields.
type Engine struct {
	in        chan core.TapeEvent
	ctl       chan func(*Engine)
	out       chan core.TapeStat
	printsOut chan core.TapePrintsMsg
	regimeOut chan core.RegimeEvent
	now       func() time.Time

	syms        map[string]*symState // Run goroutine only
	depthTarget string               // symbol currently holding the one TWS depth line, if any

	mu       sync.RWMutex
	lastStat map[string]core.TapeStat   // for Digest()
	history  map[string][]core.TapeStat // 1 snapshot/10s, last ~5 min, per symbol
}

func New() *Engine {
	return &Engine{
		in:        make(chan core.TapeEvent, 4096),
		ctl:       make(chan func(*Engine), 16),
		out:       make(chan core.TapeStat, 32), // up to len(syms) stats per 1Hz tick now
		printsOut: make(chan core.TapePrintsMsg, printsOutBuf),
		regimeOut: make(chan core.RegimeEvent, 32),
		now:       time.Now,
		syms:      make(map[string]*symState),
		lastStat:  make(map[string]core.TapeStat),
		history:   make(map[string][]core.TapeStat),
	}
}

// In is the event sink the feed layer writes into.
func (e *Engine) In() chan<- core.TapeEvent { return e.in }

// Stats is the ~1 Hz digest stream the server broadcasts (one TapeStat per
// currently-watched symbol per tick).
func (e *Engine) Stats() <-chan core.TapeStat { return e.out }

// Prints is the batched raw time-and-sales stream for the pop-out tape window
// (~4 Hz, one TapePrintsMsg per symbol that had prints since the last flush).
// Backpressure never blocks the engine: a full channel drops the whole batch
// and surfaces its size in the next delivered batch's Dropped.
func (e *Engine) Prints() <-chan core.TapePrintsMsg { return e.printsOut }

// SetSymbols updates which symbols this engine tracks concurrently. Symbols
// no longer wanted are dropped (their state discarded); genuinely new symbols
// start fresh; symbols already being watched are left completely untouched —
// this is the property that matters, since a second window subscribing to a
// different symbol must never reset a first window's already-accumulated
// tape history. Empty/nil syms stops watching everything.
func (e *Engine) SetSymbols(syms []string) {
	cp := append([]string(nil), syms...)
	e.ctl <- func(en *Engine) { en.reconcile(cp) }
}

// SetError surfaces a feed-level tape problem (e.g. trades not entitled) onto
// every currently-watched symbol — Massive's subscription-rejection frames
// are connection-wide with no per-topic culprit, so there is no finer symbol
// to blame it on.
func (e *Engine) SetError(msg string) {
	e.ctl <- func(en *Engine) { en.setErrorAll(msg) }
}

func (e *Engine) setErrorAll(msg string) {
	for _, st := range e.syms {
		st.errMsg = msg
	}
}

// SetClaudeEnabled turns Claude regime reads on/off for sym -- routed through
// ctl like SetSymbols, so no locks are needed for the hot fields. Resets the
// heartbeat timer so re-enabling never fires commentary the instant it comes
// back on, mirroring server.py's set_claude(). A no-op if sym isn't currently
// watched.
func (e *Engine) SetClaudeEnabled(sym string, on bool) {
	e.ctl <- func(en *Engine) {
		st, ok := en.syms[sym]
		if !ok {
			return
		}
		st.claudeEnabled = on
		st.lastReadAt = en.now().UnixMilli()
	}
}

// SetCommentaryInterval sets sym's periodic live-commentary interval in
// seconds (0 = off, else clamped to [heartbeatMinSecs, heartbeatMaxSecs]) --
// mirrors server.py's set_commentary()/clamp_commentary(). Resets the
// heartbeat timer so a change never fires commentary instantly. A no-op if
// sym isn't currently watched.
func (e *Engine) SetCommentaryInterval(sym string, secs float64) {
	e.ctl <- func(en *Engine) {
		st, ok := en.syms[sym]
		if !ok {
			return
		}
		st.commentarySecs = clampCommentary(secs)
		st.lastReadAt = en.now().UnixMilli()
	}
}

// SetDepthTarget names the one symbol that should hold the real IBKR Level 2
// subscription -- mirrors the existing single-target pattern already used
// for TWS's display group / live borrow line (only one real TWS instance,
// and depth requests draw from the same scarce market-data-line pool). If
// the target is actually changing, the previous target's depth fields are
// cleared first: its TWS subscription is being cancelled by the caller
// (internal/twslink) around the same time, so its book is about to go stale.
func (e *Engine) SetDepthTarget(sym string) {
	e.ctl <- func(en *Engine) {
		if en.depthTarget == sym {
			return
		}
		if st, ok := en.syms[en.depthTarget]; ok {
			st.hasDepth, st.bookBids, st.bookAsks, st.depthNote = false, nil, nil, ""
		}
		en.depthTarget = sym
	}
}

// SetDepth records a fresh Level 2 book snapshot for sym. A no-op unless sym
// is the current depth target -- guards a late/in-flight update arriving
// just after SetDepthTarget switched to a different symbol.
func (e *Engine) SetDepth(sym string, bids, asks []core.DepthLevel) {
	e.ctl <- func(en *Engine) {
		if sym != en.depthTarget {
			return
		}
		st, ok := en.syms[sym]
		if !ok {
			return
		}
		st.hasDepth, st.bookBids, st.bookAsks, st.depthNote = true, bids, asks, ""
	}
}

// SetDepthUnavailable records why sym's depth subscription failed or closed
// (a real TWS-reported reason, e.g. "halted, please re-subscribe"). A no-op
// unless sym is the current depth target, same guard as SetDepth.
func (e *Engine) SetDepthUnavailable(sym string, note string) {
	e.ctl <- func(en *Engine) {
		if sym != en.depthTarget {
			return
		}
		st, ok := en.syms[sym]
		if !ok {
			return
		}
		st.hasDepth, st.bookBids, st.bookAsks, st.depthNote = false, nil, nil, note
	}
}

// clampCommentary normalizes a live-commentary interval -- the Go mirror of
// server.py's clamp_commentary(): 0, negative, or NaN means off, otherwise
// clamped to [heartbeatMinSecs, heartbeatMaxSecs].
func clampCommentary(secs float64) float64 {
	if secs <= 0 || math.IsNaN(secs) {
		return 0
	}
	if secs < heartbeatMinSecs {
		return heartbeatMinSecs
	}
	if secs > heartbeatMaxSecs {
		return heartbeatMaxSecs
	}
	return secs
}

// NoteReadComplete records that a tape read (local or Claude) just finished
// for sym -- checkHeartbeats measures quiet time from THIS timestamp, not
// from when a regime shift was detected, mirroring server.py's
// `_last_read_wall = time.time()` reset in _consume_regime's finally block: a
// slow read (or a genuine regime read firing mid-wait) both push the next
// commentary tick out, so the two never double up. A no-op if sym isn't
// currently watched (e.g. the window closed while the read was in flight).
func (e *Engine) NoteReadComplete(sym string, at time.Time) {
	e.ctl <- func(en *Engine) {
		if st, ok := en.syms[sym]; ok {
			st.lastReadAt = at.UnixMilli()
		}
	}
}

// checkHeartbeats fires a synthetic "heartbeat" RegimeEvent for every watched
// symbol whose live commentary is enabled and due -- the Go mirror of
// server.py's _heartbeat_loop(), folded into the engine's existing 1Hz tick
// (Run) rather than a second timer goroutine. Gates on the same warm-up
// (>=warmupTrades trades, live two-sided quote) the six regime detectors use
// in detect(), plus time since the last completed read (NoteReadComplete)
// clearing commentarySecs. Whether a read is already in flight for a symbol
// is orchestrator state, not engine state (see cmd/scanner's per-symbol busy
// bookkeeping) -- unlike server.py's single-hub `_reading`/queue-empty guard
// (there is only ever one symbol there), this only gates on the conditions
// above and relies on the orchestrator to drop a heartbeat that would double
// up with a read already running for that symbol.
func (e *Engine) checkHeartbeats() {
	nowMs := e.now().UnixMilli()
	for _, st := range e.syms {
		if !st.claudeEnabled || st.commentarySecs <= 0 {
			continue
		}
		if st.trades < warmupTrades || st.bid <= 0 || st.ask <= 0 {
			continue
		}
		if float64(nowMs-st.lastReadAt) < st.commentarySecs*1000 {
			continue
		}
		ev := e.fireRegime(st, "heartbeat", "Live commentary")
		select {
		case e.regimeOut <- *ev:
		default: // consumer behind; drop rather than block the tick
		}
	}
}

func (e *Engine) Run(ctx context.Context) error {
	tk := time.NewTicker(statHz)
	defer tk.Stop()
	ptk := time.NewTicker(printFlushEvery) // faster cadence for the raw-print stream
	defer ptk.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case fn := <-e.ctl:
			fn(e)
		case ev := <-e.in:
			e.apply(ev)
		case <-ptk.C:
			e.flushPrints()
		case <-tk.C:
			e.publish()
			e.checkHeartbeats()
		}
	}
}

func (e *Engine) reconcile(syms []string) {
	want := make(map[string]struct{}, len(syms))
	for _, s := range syms {
		if s == "" {
			continue
		}
		want[s] = struct{}{}
	}
	for sym := range e.syms {
		if _, ok := want[sym]; ok {
			continue
		}
		delete(e.syms, sym)
		e.mu.Lock()
		delete(e.lastStat, sym)
		delete(e.history, sym)
		e.mu.Unlock()
	}
	for sym := range want {
		if _, ok := e.syms[sym]; ok {
			continue // already watched -- leave its accumulated state alone
		}
		e.syms[sym] = &symState{sym: sym, watchStart: e.now()}
	}
}

func (e *Engine) apply(ev core.TapeEvent) {
	if q := ev.Quote; q != nil {
		st, ok := e.syms[q.Sym]
		if !ok {
			return
		}
		if q.Bid > 0 && q.Ask > 0 && q.Ask >= q.Bid {
			// Classify what changed at the inside BEFORE overwriting it -- the
			// classification compares the incoming quote against the quote
			// that just prevailed (book.go).
			classifyQuote(st, q)
			st.bid, st.ask, st.bidSz, st.askSz, st.quoteAt = q.Bid, q.Ask, q.BidSz, q.AskSz, q.TS
		}
		return
	}
	t := ev.Trade
	if t == nil || t.Size <= 0 || t.Price <= 0 {
		return
	}
	st, ok := e.syms[t.Sym]
	if !ok {
		return
	}

	side := e.classify(st, t)
	// Volume traded AT the inside -- how much this level is soaking up. Must
	// run right after classify(), before the trade is folded into the
	// buckets/session totals below, matching engine.py's on_trade() order.
	accumulateLevelVol(st, side, t.Price, t.Size)

	b := e.bucketFor(st, t.TS/1000)
	switch side {
	case "buy":
		b.buyV += t.Size
		st.buyTotal += t.Size
	case "sell":
		b.sellV += t.Size
		st.sellTotal += t.Size
	default:
		b.neutV += t.Size
	}
	b.trades++
	st.trades++
	st.sumSize += t.Size
	st.lastPrice = t.Price
	st.sizes = append(st.sizes, t.Size)
	if len(st.sizes) > sizesKept {
		st.sizes = st.sizes[1:]
	}

	// Notable print? (threshold vs the rolling average so it self-scales.)
	avg := e.avgSize(st)
	notional := t.Price * float64(t.Size)
	if (avg > 0 && float64(t.Size) >= bigSizeMult*avg) || notional >= bigNotional {
		p := core.Print{TS: t.TS, Price: t.Price, Size: t.Size, Side: side, Notional: notional}
		st.prints = append([]core.Print{p}, st.prints...)
		if len(st.prints) > printsKept {
			st.prints = st.prints[:printsKept]
		}
	}

	// Every classified trade also joins the raw-print stream (flushPrints):
	// unlike the notable-print chips above, the pop-out tape shows ALL prints.
	// Capped per pending batch — keep the newest, count the loss honestly.
	st.pendingPrints = append(st.pendingPrints, core.Print{TS: t.TS, Price: t.Price, Size: t.Size, Side: side, Notional: notional})
	if len(st.pendingPrints) > printBatchCap {
		st.pendingPrints = st.pendingPrints[1:]
		st.pendingDropped++
	}

	appendMicro(st, t.TS, t.Price, side, t.Size)
	e.detect(st, t, side)
}

// flushPrints emits one TapePrintsMsg per symbol with pending prints — called
// on the engine goroutine (Run's ~4 Hz print ticker), so it reads and resets
// symState without locks, same as publish(). The send NEVER blocks: a full
// printsOut drops the whole batch and rolls its size (plus any Dropped it was
// already carrying) into pendingDropped, so the loss surfaces as Dropped on
// the next batch that does get through — a dropped UI print is cosmetic, a
// stalled engine is not. Symbols with nothing pending emit nothing; a bare
// carried-over Dropped count waits for the next real prints.
func (e *Engine) flushPrints() {
	for _, st := range e.syms {
		if len(st.pendingPrints) == 0 {
			continue
		}
		msg := core.TapePrintsMsg{Sym: st.sym, Prints: st.pendingPrints, Dropped: st.pendingDropped}
		st.pendingPrints, st.pendingDropped = nil, 0
		select {
		case e.printsOut <- msg:
		default: // consumer behind; drop the batch rather than block the engine
			st.pendingDropped = len(msg.Prints) + msg.Dropped
		}
	}
}

// classify marks a trade buyer/seller-initiated: quote rule when a fresh NBBO
// exists (at/above ask = buy, at/below bid = sell, midpoint -> tick rule),
// tick rule otherwise. This is the standard Lee-Ready approach.
func (e *Engine) classify(st *symState, t *core.Trade) string {
	if e.quoteFresh(st, t.TS) {
		switch {
		case t.Price >= st.ask:
			return "buy"
		case t.Price <= st.bid:
			return "sell"
		default:
			mid := (st.bid + st.ask) / 2
			if t.Price > mid {
				return "buy"
			}
			if t.Price < mid {
				return "sell"
			}
		}
	}
	// tick rule: up from last trade = buy, down = sell
	if st.lastPrice > 0 {
		if t.Price > st.lastPrice {
			return "buy"
		}
		if t.Price < st.lastPrice {
			return "sell"
		}
	}
	return "neutral"
}

func (e *Engine) quoteFresh(st *symState, nowMs int64) bool {
	return st.quoteAt > 0 && nowMs-st.quoteAt < quoteFreshMs
}

func (e *Engine) bucketFor(st *symState, sec int64) *bucket {
	b := &st.ring[sec%ringSecs]
	if b.sec != sec {
		*b = bucket{sec: sec}
	}
	return b
}

// window sums buckets in (now-secs, now].
func (e *Engine) window(st *symState, secs int) (buyV, sellV, neutV, trades int64) {
	nowSec := e.now().Unix()
	for s := nowSec - int64(secs) + 1; s <= nowSec; s++ {
		b := &st.ring[s%ringSecs]
		if b.sec == s {
			buyV += b.buyV
			sellV += b.sellV
			neutV += b.neutV
			trades += b.trades
		}
	}
	return
}

func (e *Engine) avgSize(st *symState) float64 {
	if st.trades == 0 {
		return 0
	}
	return float64(st.sumSize) / float64(st.trades)
}

// medianSize is the median of st.sizes' trailing print sizes -- matching
// Python's statistics.median (an even-length sample averages its two middle
// values) -- or 1 when no trades have been seen yet, the same fallback
// engine.py's _median_size() uses so blockThreshold has a sane floor before
// any prints arrive.
func (e *Engine) medianSize(st *symState) float64 {
	n := len(st.sizes)
	if n == 0 {
		return 1
	}
	cp := append([]int64(nil), st.sizes...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	mid := n / 2
	if n%2 == 1 {
		return float64(cp[mid])
	}
	return float64(cp[mid-1]+cp[mid]) / 2
}

// blockThreshold is the print size at/above which a trade is flagged as a
// block -- the Go mirror of engine.py's _block_threshold().
func (e *Engine) blockThreshold(st *symState) float64 {
	if v := blockMedianMult * e.medianSize(st); v > blockMinSize {
		return v
	}
	return blockMinSize
}

// recordCVD appends the current cumulative-volume-delta sample to st's
// capped chart series. Called once per publish() tick (1Hz) rather than from
// snapshot() itself, so that calling snapshot() directly (as the tests do)
// never grows the series on its own -- engine.py's snapshot() appends on
// every call because it's polled directly at ~4Hz; this engine instead ticks
// publish() at a fixed 1Hz, so gating the append there gives the same "one
// sample per wall-clock tick" cadence.
func (e *Engine) recordCVD(st *symState) {
	st.cvdSeries = append(st.cvdSeries, core.CVDPoint{
		Ts:    e.now().UnixMilli(),
		Value: st.buyTotal - st.sellTotal,
	})
	if len(st.cvdSeries) > cvdPointsKept {
		st.cvdSeries = st.cvdSeries[1:]
	}
}

func (e *Engine) publish() {
	for sym, st := range e.syms {
		e.recordCVD(st)
		stat := e.snapshot(st)
		e.mu.Lock()
		e.lastStat[sym] = stat
		hist := e.history[sym]
		if len(hist) == 0 || stat.AsOf-hist[len(hist)-1].AsOf >= 10_000 {
			hist = append(hist, stat)
			if len(hist) > 30 {
				hist = hist[1:]
			}
			e.history[sym] = hist
		}
		e.mu.Unlock()
		select {
		case e.out <- stat:
		default: // consumer behind; next tick supersedes
		}
	}
}

func (e *Engine) snapshot(st *symState) core.TapeStat {
	now := e.now()
	out := core.TapeStat{
		Sym: st.sym, AsOf: now.UnixMilli(), Watching: true, Err: st.errMsg,
		Bid: st.bid, Ask: st.ask, TotalTrades: st.trades,
		DeltaTotal: st.buyTotal - st.sellTotal,
		QuoteRule:  e.quoteFresh(st, now.UnixMilli()),
		AvgSize5m:  0,
		WatchSecs:  int(now.Sub(st.watchStart) / time.Second),
	}

	b1, s1, _, t1 := e.window(st, 60) // 1-MINUTE window (feeds Delta1m/Rate1m/Ratio60s)
	b5, s5, n5, t5 := e.window(st, 300)
	b10, s10, _, _ := e.window(st, 10)  // 10-SECOND window (feeds Delta10s/Ratio10s)
	b1s, s1s, _, t1s := e.window(st, 1) // 1-SECOND window (feeds Ratio1s/PrintsPerSec)
	out.Delta1m = b1 - s1
	out.Delta5m = b5 - s5
	out.Delta10s = b10 - s10
	out.Rate1m = float64(t1) / 60.0
	if t5 > 0 {
		out.AvgSize5m = float64(b5+s5+n5) / float64(t5)
	}
	if cls := b5 + s5; cls > 0 {
		out.BuyPct5m = 100 * float64(b5) / float64(cls)
		out.SellPct5m = 100 * float64(s5) / float64(cls)
	}
	out.Prints = append([]core.Print(nil), st.prints...)

	// Delta as a share of that window's own volume (-1..+1), matching
	// engine.py's ratio_1s/10s/60s exactly: vol there is buy+sell only
	// (neutral-classified volume is excluded), so a small delta on thin
	// volume reads as strongly one-sided while a big delta on huge volume
	// reads mild.
	if v := b1s + s1s; v > 0 {
		out.Ratio1s = float64(b1s-s1s) / float64(v)
	}
	if v := b10 + s10; v > 0 {
		out.Ratio10s = float64(b10-s10) / float64(v)
	}
	if v := b1 + s1; v > 0 {
		out.Ratio60s = float64(b1-s1) / float64(v)
	}
	out.PrintsPerSec = int(t1s)
	out.PrintsPerSecBaseline = float64(t1) / 60.0
	out.BlockThreshold = e.blockThreshold(st)
	out.MedianSize = e.medianSize(st)
	out.CVDSeries = append([]core.CVDPoint(nil), st.cvdSeries...)
	out.Book = e.bookStats(st, bookLookback)
	out.Verdict = e.localVerdict(st)

	// Real Level 2 (internal/twslink) is single-target: only the symbol most
	// recently selected across every window ever holds the one TWS depth
	// line, same as the display group / live borrow line. Every other
	// watched symbol gets an explanatory note (once a target actually
	// exists) rather than the frontend's generic "needs a subscription"
	// fallback, so it reads as a deliberate resource choice, not a problem.
	if st.sym == e.depthTarget {
		out.HasDepth, out.BookBids, out.BookAsks, out.DepthNote = st.hasDepth, st.bookBids, st.bookAsks, st.depthNote
	} else if e.depthTarget != "" {
		out.DepthNote = "Level 2 follows " + e.depthTarget + ", your most recently selected symbol"
	}

	// Control score: mostly the 5-minute aggressor balance, tilted by the
	// 1-minute delta (normalized) so a fresh flip shows up before the 5m
	// average catches up. Clamped to [-1, 1].
	if cls := b5 + s5; cls > 0 {
		balance := 2*float64(b5)/float64(cls) - 1 // -1..1
		var tilt float64
		if v1 := b1 + s1; v1 > 0 {
			tilt = float64(out.Delta1m) / float64(v1) // -1..1
		}
		out.Control = clamp(0.65*balance+0.35*tilt, -1, 1)
	}
	return out
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// controlScore60 is the 60s buy/sell imbalance, -1 (sellers) .. +1 (buyers)
// -- the Go mirror of engine.py's _control_score(): delta over the window's
// own buy+sell volume (neutral excluded), clamped, zero when the window has
// no classified volume yet. A pure function of an already-computed 60s
// window's totals (rather than a method that re-scans the ring itself) so
// claudeSnapshot (regime.go) and localVerdict (verdict.go) -- both of which
// need their own 60s window for other fields too -- read the exact same
// formula off the one window scan instead of drifting apart or re-scanning.
func controlScore60(buyV, sellV int64) float64 {
	vol := buyV + sellV
	if vol <= 0 {
		return 0
	}
	return clamp(float64(buyV-sellV)/float64(vol), -1, 1)
}

// Digest renders the current tape state for sym as text for the Claude tape
// read: the metric trail (10s cadence), current values, and notable prints.
// Empty string if sym isn't currently watched.
func (e *Engine) Digest(sym string) string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	st, ok := e.lastStat[sym]
	if !ok {
		return ""
	}
	hist := e.history[sym]
	var b strings.Builder
	fmt.Fprintf(&b, "Symbol: %s (watched %ds, %s classification)\n",
		st.Sym, st.WatchSecs, map[bool]string{true: "NBBO quote-rule", false: "tick-rule"}[st.QuoteRule])
	fmt.Fprintf(&b, "Now: bid %.4g ask %.4g | delta 1m %+d, 5m %+d, total %+d | at-ask %.0f%% vs at-bid %.0f%% (5m) | %.1f trades/s | avg size %.0f\n",
		st.Bid, st.Ask, st.Delta1m, st.Delta5m, st.DeltaTotal, st.BuyPct5m, st.SellPct5m, st.Rate1m, st.AvgSize5m)
	if len(hist) > 1 {
		b.WriteString("Trail (10s steps, oldest first): delta_total / at-ask%:\n")
		for _, h := range hist {
			fmt.Fprintf(&b, "  %+d/%.0f%%", h.DeltaTotal, h.BuyPct5m)
		}
		b.WriteString("\n")
	}
	if len(st.Prints) > 0 {
		b.WriteString("Notable prints (newest first):\n")
		for _, p := range st.Prints {
			fmt.Fprintf(&b, "  %s %s %d @ %.4g ($%.0fk)\n",
				time.UnixMilli(p.TS).Format("15:04:05"), strings.ToUpper(p.Side), p.Size, p.Price, p.Notional/1000)
		}
	}
	return b.String()
}
