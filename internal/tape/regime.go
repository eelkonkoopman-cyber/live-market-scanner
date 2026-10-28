// Regime-shift detection: after every classified trade, a fixed-precedence
// cascade of six detectors checks whether the tape just told a story worth a
// Claude read (a big player stepping in, absorption, a sweep, a CVD flip, a
// tape-speed spike). The first detector to fire wins and the whole cascade
// then cools down together -- this is the Go mirror of engine.py's
// _detect()/_fire() and the six _detect_* methods it calls, in the exact
// order and with the exact thresholds engine.py uses.
package tape

import (
	"fmt"
	"math"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

const (
	// warmupTrades/warm-up guard: no detector may fire until the symbol has
	// seen at least this many classified trades AND has a live two-sided
	// quote -- mirrors engine.py's `_detect()`: `total_trades < 20 or not
	// (bid and ask)`.
	warmupTrades = 20

	// claudeCooldownMs is the shared cooldown across ALL six detectors --
	// mirrors engine.py's Settings.claude_cooldown default (CLAUDE_COOLDOWN,
	// 20.0s): once any detector fires, the whole cascade goes quiet for this
	// long, regardless of which detector fired.
	claudeCooldownMs = 20_000

	// microKept/microWindowMs bound the trade-level ring (symState.micro):
	// at most this many trades, and never older than this many ms -- a
	// memory-bounded analogue of engine.py's `self.trades` deque, which is
	// unbounded in count but pruned to the same 60s window.
	microKept     = 64
	microWindowMs = 60_000

	// --- level absorption: engine.py's _detect_level_absorption() ---
	levelAbsorbRefills   = 3   // `self._bid_refills >= 3`
	levelAbsorbFloorMult = 5.0 // `floor = 5 * self._median_size()`

	// --- sweep: engine.py's _detect_sweep(), Settings.sweep_len default ---
	sweepLen       = 5     // `n = self.s.sweep_len`
	sweepMaxSpanMs = 3_000 // `recent[-1].ts - recent[0].ts > 3.0`

	// --- CVD flip: engine.py's _detect_cvd_flip() ---
	cvdFlipFastSecs  = 5  // `self._scan(5.0, now)`
	cvdFlipSlowSecs  = 10 // `self._scan(10.0, now)`
	cvdFlipNoiseMult = 3.0

	// --- absorption: engine.py's _detect_absorption() ---
	absorptionWindowSecs    = 10
	absorptionWindowMs      = absorptionWindowSecs * 1000
	absorptionMinPrints     = 15     // `w["count"] < 15`
	absorptionVolMedianMult = 10.0   // `w["vol"] < 10 * self._median_size()`
	absorptionBandPct       = 0.0005 // `max(0.0005 * price, 0.0)` -- ~5bps
	absorptionBuyShareHi    = 0.65
	absorptionBuyShareLo    = 0.35

	// --- speed spike: engine.py's _detect_speed(), Settings.speed_spike_mult ---
	speedBaselineWarmup = 30 * time.Second // `now - self._start < 30`
	speedSpikeMult      = 3.0
	speedSpikeMinPrints = 5 // `one >= 5`
)

// microTrade is one classified trade at trade granularity -- see symState's
// doc comment on the `micro` field for why this is kept separately from the
// per-second bucket ring.
type microTrade struct {
	ts    int64 // ms epoch (the trade's own TS)
	price float64
	side  string // "buy" | "sell" | "neutral"
	size  int64
}

// appendMicro appends a classified trade to st's trade-level ring, then
// prunes it to the trailing 60s (relative to the trade's own ts, matching
// engine.py's `_prune(t.ts)`) and caps it at microKept entries.
func appendMicro(st *symState, ts int64, price float64, side string, size int64) {
	st.micro = append(st.micro, microTrade{ts: ts, price: price, side: side, size: size})
	cutoff := ts - microWindowMs
	for len(st.micro) > 0 && st.micro[0].ts < cutoff {
		st.micro = st.micro[1:]
	}
	if len(st.micro) > microKept {
		st.micro = st.micro[len(st.micro)-microKept:]
	}
}

// microPriceRange returns the oldest ("first") and newest ("last") trade
// price within the trailing lookbackMs of st.micro (st.micro is oldest-first,
// so the first in-window entry found scanning forward is the oldest one, and
// the loop's final assignment is the newest). ok is false when no trade falls
// in the window. Mirrors the first/last fields of engine.py's `_scan()`.
func microPriceRange(st *symState, lookbackMs, nowMs int64) (first, last float64, ok bool) {
	cutoff := nowMs - lookbackMs
	for _, m := range st.micro {
		if m.ts < cutoff {
			continue
		}
		if !ok {
			first = m.price
			ok = true
		}
		last = m.price
	}
	return
}

// RegimeEvents is the stream of locally detected candidate regime shifts --
// the Go mirror of engine.py's regime_queue. Non-blocking send: a consumer
// that falls behind loses the oldest-unread events rather than stalling the
// hot trade path.
func (e *Engine) RegimeEvents() <-chan core.RegimeEvent { return e.regimeOut }

// detect runs the fixed-precedence regime cascade for one just-classified
// trade -- the Go mirror of engine.py's _detect(): warm-up guard, then the
// shared cooldown gate, then the six detectors in order with the first
// non-nil result winning. Must be called after classify()/accumulateLevelVol
// and after the trade has been folded into st's other counters (matching
// engine.py's on_trade() calling _detect(t) last).
func (e *Engine) detect(st *symState, t *core.Trade, side string) {
	if st.trades < warmupTrades || st.bid <= 0 || st.ask <= 0 {
		return
	}
	now := t.TS
	if now-st.lastRegimeTS < claudeCooldownMs {
		return // a read already fired recently; keep accumulating state
	}

	ev := e.detectLevelAbsorption(st)
	if ev == nil {
		ev = e.detectBlockPrint(st, t, side)
	}
	if ev == nil {
		ev = e.detectSweep(st)
	}
	if ev == nil {
		ev = e.detectCVDFlip(st)
	}
	if ev == nil {
		ev = e.detectAbsorption(st)
	}
	if ev == nil {
		ev = e.detectSpeedSpike(st)
	}
	if ev == nil {
		return
	}
	st.lastRegimeTS = now
	select {
	case e.regimeOut <- *ev:
	default: // consumer behind; drop rather than block the hot trade path
	}
}

// fireRegime builds the RegimeEvent for a fired detector -- the Go mirror of
// engine.py's _fire(): a fresh wall-clock ts (not the triggering trade's own
// ts) plus the full claudeSnapshot() feature bundle.
func (e *Engine) fireRegime(st *symState, trigger, headline string) *core.RegimeEvent {
	ev := core.RegimeEvent{
		Sym:      st.sym,
		Ts:       e.now().UnixMilli(),
		Trigger:  trigger,
		Headline: headline,
		Verdict:  e.localVerdict(st),
		ClaudeOn: st.claudeEnabled,
		Snapshot: e.claudeSnapshot(st, trigger, headline),
	}
	return &ev
}

// detectLevelAbsorption: a price level being hit repeatedly that keeps
// refreshing -- a large passive buyer/seller sitting there (SMB's
// "refreshing bid/offer"). Mirrors engine.py's _detect_level_absorption()
// exactly, including its single shared absorbFiredPx (see symState's doc
// comment on that field).
func (e *Engine) detectLevelAbsorption(st *symState) *core.RegimeEvent {
	floor := levelAbsorbFloorMult * e.medianSize(st)
	if st.bidRefills >= levelAbsorbRefills && st.bidLevelVol >= floor && st.bid != st.absorbFiredPx {
		st.absorbFiredPx = st.bid
		return e.fireRegime(st, "level_absorption",
			fmt.Sprintf("Bid holding %.4g — %.4g sold into it, refreshing", st.bid, st.bidLevelVol))
	}
	if st.askRefills >= levelAbsorbRefills && st.askLevelVol >= floor && st.ask != st.absorbFiredPx {
		st.absorbFiredPx = st.ask
		return e.fireRegime(st, "level_absorption",
			fmt.Sprintf("Offer capping %.4g — %.4g bought, refreshing", st.ask, st.askLevelVol))
	}
	return nil
}

// detectBlockPrint fires when a single print's own size is at/above
// blockThreshold -- mirrors engine.py's _detect_block().
func (e *Engine) detectBlockPrint(st *symState, t *core.Trade, side string) *core.RegimeEvent {
	if float64(t.Size) < e.blockThreshold(st) {
		return nil
	}
	who := "unclear"
	switch side {
	case "buy":
		who = "buyer"
	case "sell":
		who = "seller"
	}
	return e.fireRegime(st, "block_print",
		fmt.Sprintf("Block print %d @ %.4g — aggressive %s", t.Size, t.Price, who))
}

// detectSweep fires when the last sweepLen trades are all the same side,
// span at most sweepMaxSpanMs, and price net-progressed in that direction --
// deduped against st.lastSweptDir until the direction flips. Mirrors
// engine.py's _detect_sweep() exactly, reading the trade-level micro ring
// (see symState's doc comment on why the bucket ring can't supply this).
func (e *Engine) detectSweep(st *symState) *core.RegimeEvent {
	n := sweepLen
	if len(st.micro) < n {
		return nil
	}
	recent := st.micro[len(st.micro)-n:] // oldest-first, last n
	if recent[n-1].ts-recent[0].ts > sweepMaxSpanMs {
		return nil
	}
	dir := recent[0].side
	for _, m := range recent[1:] {
		if m.side != dir {
			return nil
		}
	}
	if dir != "buy" && dir != "sell" {
		return nil
	}
	moved := recent[n-1].price - recent[0].price
	if dir == "buy" && moved <= 0 {
		return nil
	}
	if dir == "sell" && moved >= 0 {
		return nil
	}
	if st.lastSweptDir == dir {
		return nil // already reported this run
	}
	st.lastSweptDir = dir
	var vol int64
	for _, m := range recent {
		vol += m.size
	}
	if dir == "buy" {
		return e.fireRegime(st, "buy_sweep", fmt.Sprintf("Buyers sweeping offers — %d prints, %d lifted", n, vol))
	}
	return e.fireRegime(st, "sell_sweep", fmt.Sprintf("Sellers sweeping bids — %d prints, %d hit", n, vol))
}

// detectCVDFlip fires when delta over the last cvdFlipFastSecs and the
// cvdFlipSlowSecs before that both clear a median-based noise floor and have
// opposite signs -- a genuine flip, not just a one-sided burst after a quiet
// stretch. Mirrors engine.py's _detect_cvd_flip().
func (e *Engine) detectCVDFlip(st *symState) *core.RegimeEvent {
	b5, s5, _, _ := e.window(st, cvdFlipFastSecs)
	b10, s10, _, _ := e.window(st, cvdFlipSlowSecs)
	cur := b5 - s5
	prev := (b10 - s10) - cur // the 5s before the last 5s
	noise := cvdFlipNoiseMult * e.medianSize(st)
	if math.Abs(float64(cur)) < noise || math.Abs(float64(prev)) < noise {
		return nil
	}
	if (cur > 0) == (prev > 0) {
		return nil // same sign, no flip
	}
	if cur > 0 {
		return e.fireRegime(st, "cvd_flip", "Delta flipped positive — buyers taking over")
	}
	return e.fireRegime(st, "cvd_flip", "Delta flipped negative — sellers taking over")
}

// detectAbsorption fires when a busy (>=15 prints, volume >=10x median),
// clearly one-sided (>=65% one way) window nonetheless left price flat
// (within ~5bps) -- one side pressing without moving price, the other side
// absorbing it. Mirrors engine.py's _detect_absorption().
func (e *Engine) detectAbsorption(st *symState) *core.RegimeEvent {
	b10, s10, _, t10 := e.window(st, absorptionWindowSecs)
	vol := b10 + s10
	if t10 < absorptionMinPrints || float64(vol) < absorptionVolMedianMult*e.medianSize(st) {
		return nil
	}
	price := st.lastPrice
	if price == 0 {
		price = 1
	}
	band := absorptionBandPct * price
	first, last, ok := microPriceRange(st, absorptionWindowMs, e.now().UnixMilli())
	if !ok {
		return nil
	}
	if math.Abs(last-first) > band {
		return nil
	}
	buyShare := 0.5
	if vol > 0 {
		buyShare = float64(b10) / float64(vol)
	}
	if buyShare >= absorptionBuyShareHi {
		return e.fireRegime(st, "absorption", "Heavy buying being absorbed — offers holding")
	}
	if buyShare <= absorptionBuyShareLo {
		return e.fireRegime(st, "absorption", "Heavy selling being absorbed — bids holding")
	}
	return nil
}

// detectSpeedSpike fires when the last-1s print rate is at/above
// speedSpikeMult times the trailing-60s baseline rate (and at least
// speedSpikeMinPrints in absolute terms), once the symbol has been watched
// long enough to have a meaningful baseline. Mirrors engine.py's
// _detect_speed().
func (e *Engine) detectSpeedSpike(st *symState) *core.RegimeEvent {
	if e.now().Sub(st.watchStart) < speedBaselineWarmup {
		return nil // need a baseline
	}
	_, _, _, one64 := e.window(st, 1)
	_, _, _, sixty64 := e.window(st, 60)
	one := int(one64)
	baseline := float64(sixty64) / 60.0
	if baseline <= 0 {
		return nil
	}
	if float64(one) >= speedSpikeMult*baseline && one >= speedSpikeMinPrints {
		b2, s2, _, _ := e.window(st, 2)
		lean := "two-sided"
		if d := b2 - s2; d > 0 {
			lean = "buy-side"
		} else if d < 0 {
			lean = "sell-side"
		}
		return e.fireRegime(st, "speed_spike",
			fmt.Sprintf("Tape accelerating (%d/s vs %.1f/s), %s", one, baseline, lean))
	}
	return nil
}

// claudeSnapshot builds the compact feature bundle handed to Claude on a
// regime shift -- the Go mirror of engine.py's claude_snapshot(). Vwap/
// SessionHigh/SessionLow are left zero-valued; a later stage populates them
// from the existing per-symbol row lookup (see core.TapeSnapshot's doc
// comment). BookBids/BookAsks are left empty: this stage is Level-1 only, no
// L2 depth ladder exists yet (see book.go).
func (e *Engine) claudeSnapshot(st *symState, trigger, headline string) core.TapeSnapshot {
	b10, s10, _, t10 := e.window(st, absorptionWindowSecs)
	b60, s60, _, t60 := e.window(st, 60)
	_, _, _, t1 := e.window(st, 1)

	var spread float64
	if st.bid > 0 && st.ask > 0 {
		spread = st.ask - st.bid
	}
	control := controlScore60(b60, s60)

	n := len(st.micro)
	if n > 20 {
		n = 20
	}
	recent := st.micro[len(st.micro)-n:]
	blockTh := e.blockThreshold(st)
	prints := make([]core.TapeSnapshotPrint, 0, n)
	for _, m := range recent {
		prints = append(prints, core.TapeSnapshotPrint{
			Price: m.price, Size: m.size, Side: m.side, Block: float64(m.size) >= blockTh,
		})
	}

	return core.TapeSnapshot{
		Symbol:   st.sym,
		Trigger:  trigger,
		Headline: headline,

		LastPrice: st.lastPrice,
		Bid:       st.bid,
		Ask:       st.ask,
		Spread:    spread,
		BidSize:   st.bidSz,
		AskSize:   st.askSz,

		CVDSession: st.buyTotal - st.sellTotal,
		Delta10s:   b10 - s10,
		Delta60s:   b60 - s60,
		BuyVol60s:  b60,
		SellVol60s: s60,

		Prints10s:            int(t10),
		PrintsPerSecNow:      int(t1),
		PrintsPerSecBaseline: float64(t60) / 60.0,

		BlockThreshold:  blockTh,
		MedianPrintSize: e.medianSize(st),
		ControlScore:    control,

		Book:         e.bookStats(st, bookLookback),
		RecentPrints: prints,
	}
}
