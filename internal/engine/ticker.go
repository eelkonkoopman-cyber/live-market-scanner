package engine

import (
	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dlevel"
)

// ringLen is the per-ticker rolling-price buffer length in seconds. It must be
// strictly greater than the longest lookback (900s for chg15m) so priceAgo(900)
// addresses a slot distinct from head — a 900-length ring would read "now vs
// now". 960 (16 min) gives headroom.
const ringLen = 960

// minBaselineSessions gates RVOL: a baseline built from too few sessions yields
// absurd ratios, so below this we emit null.
const minBaselineSessions = 5

// regularOpenBucket/regularCloseBucket are the fixed 5-minute-bucket indices
// for 09:30 and 16:00 ET, from bucketOf's own (minutesOfDay-240)/5 formula
// (240 = 04:00 ET, bucket 0): (570-240)/5=66, (960-240)/5=144. Used to slice
// AvgVol (regular-session-only) out of the same per-bucket cumulative
// baseline RVOL reads — these are fixed wall-clock times, not "now", so
// compile-time constants are correct and avoid constructing a time.Time.
const (
	regularOpenBucket  = 66
	regularCloseBucket = 144
)

// tickerState is the per-symbol hot state. It is owned by exactly one shard
// goroutine; no field is ever read or written from another goroutine.
type tickerState struct {
	sym string

	// rolling per-second price ring (forward-filled for sparse tickers)
	ring     [ringLen]float32
	valid    [ringLen]bool
	head     int   // index of "now"
	baseSec  int64 // epoch second represented by ring[head]
	firstSec int64 // first second seen this session (0 = none)

	// day/session state
	last      float32
	prevClose float32 // seeded from REST snapshot
	dayOpen   float32 // official day open (op)
	sessVol   int64   // engine-accumulated session volume (04:00-anchored; see applyAgg)
	dayVWAP   float32 // session VWAP to date (a)
	hod, lod  float32
	hodSet    bool
	lodSet    bool

	// session-scoped state (current pre/regular/post sub-session only). Reset at
	// every session boundary by resetSessionScope, independent of the 04:00-anchored
	// day state above. scopeOpen is the first price seen this sub-session; the
	// regular session prefers the official dayOpen (see toRow).
	scopeOpen    float32
	scopeOpenSet bool
	scopeHi      float32
	scopeLo      float32
	scopeHiSet   bool
	scopeLoSet   bool
	scopeVol     int64   // volume accumulated this sub-session
	scopeVwapNum float64 // Σ(barVWAP × barVol) this sub-session
	scopeVwapDen float64 // Σ(barVol) this sub-session
	// vwMin: the live VWAP sums of the latest few minutes (slot = minute & 7),
	// so SeedScopeVwap can join REST minute bars with the prints that came
	// after the last seeded bar without counting any print twice.
	vwMin [vwMinSlots]vwMinSum

	// Frozen prior-session slots: the outgoing sub-session's scoped metrics,
	// captured by freezeScope at the 09:30 (frozenPre) and 16:00 (frozenRth)
	// boundaries — the same ctrl closure that wipes the scope, never the hot
	// path. Memory cost is trivial (two small structs per ticker); the slots
	// are memory-only, so a restart after the boundary loses them — a
	// documented limitation.
	frozenPre frozenScope
	frozenRth frozenScope

	halted bool
	lastTS int64 // EndMs of last real agg

	// captured cumulative session volume at each 5-min bucket boundary, for the
	// nightly RVOL baseline (self-built from live data). curveBkt is the highest
	// bucket recorded so far this session (-1 = none).
	curve    [core.BucketCount]int64
	curveBkt int

	// per-bucket session OHLC at the same 5-min resolution, for the intraday
	// flag pass (read via Engine.SessionBars; the geometry itself never runs in
	// the engine). hi/lo are TRUE intra-bucket extremes accumulated from agg
	// High/Low into pendHi/pendLo by applyAgg (not 10 Hz samples of last);
	// close is last, stamped by recordCurve. curveFirst is the first observed
	// bucket (-1 = none): earlier buckets stay zero and are excluded by the
	// reader, mirroring curve's no-back-fill rule. Per-bucket volume needs no
	// array — it is the delta of adjacent curve entries. Cost: 3 arrays ×
	// 192 × 4 B = 2,304 B per ticker ≈ 23 MB at 10k tickers, alongside the
	// curve's ~15 MB.
	bktHi      [core.BucketCount]float32
	bktLo      [core.BucketCount]float32
	bktCl      [core.BucketCount]float32
	pendHi     float32 // max agg High since the current bucket opened (0 = none yet)
	pendLo     float32 // min agg Low since the current bucket opened (0 = none yet)
	curveFirst int

	// inline candlestick chart: ring of recent OHLC bars + the bar being built.
	barRing   [barCount]core.Bar
	barHead   int // index of newest completed bar (-1 = none)
	barFilled int
	curBar    core.Bar
	curBarTS  int64 // start ms of curBar (0 = none)
	// barsCache is the slice bars() last built; barsValid says it still
	// matches the ring. Every snapshot (10 Hz, every tracked ticker) asks for
	// the bars, but a quiet ticker's ring changes rarely, so the projection is
	// rebuilt only after a mutation (see invalidateBars) instead of allocating
	// 11k slices per tick. The cached slice is never written again once
	// handed out — a change replaces it — so sharing it across snapshots is safe.
	barsCache []core.Bar
	barsValid bool

	// 5-minute candle break tracker (brk5mAge / vsPrev5mLow / prev5mLow).
	// Candles are keyed on each agg's OWN StartMs, not the snapshot clock that
	// stamps the bkt* buckets above: their <=100 ms boundary slop is harmless
	// for flag geometry, but here it would fake a break the instant a candle
	// opens whenever the outgoing candle's final-second low leaked into it —
	// i.e. exactly when a stock is selling into the bar close. EndMs is no
	// better: a per-second agg covering [09:34:59, 09:35:00) ends ON the next
	// candle's boundary.
	m5Start     int64   // start ms of the forming candle (0 = none)
	m5Lo        float32 // forming candle's low so far
	m5Partial   bool    // forming candle opened before the engine was listening
	m5PrevStart int64   // start ms of the previous completed candle (0 = none)
	m5PrevLo    float32 // previous completed candle's low (0 = none usable)
	m5BrkMs     int64   // EndMs of the agg that first traded below m5PrevLo this candle (0 = not broken)
	m5ListenMs  int64   // engine start: a candle opening before this saw only part of its prints

	// Close-based variant (brk5mCloseAge / vsPrev5mClose / prev5mClose). A
	// candle's close is its LATEST print by timestamp, so an out-of-order agg
	// can never overwrite it with an earlier price. Unlike the low, a candle
	// already in progress at startup has an exact close (we were listening
	// when it ended), so partial candles ARE valid close references.
	m5Cl       float32 // forming candle's close so far
	m5ClTs     int64   // StartMs of the print that set m5Cl
	m5PrevCl   float32 // previous candle's close (0 = none)
	m5PrevClTs int64   // StartMs of the print that set m5PrevCl
	m5BrkClMs  int64   // EndMs of the agg that first traded below m5PrevCl this candle (0 = not broken)

	// Long side: the forming candle's running HIGH (from agg High, not just its
	// close) and the first moment it traded strictly ABOVE m5PrevCl.
	m5Hi        float32 // forming candle's high so far
	m5BrkClUpMs int64   // EndMs of the agg that first traded above m5PrevCl this candle (0 = not broken)

	// Level breaks: today's pre-market high (from the COMPLETE frozen pre slot,
	// so only from 09:30) and the previous trading day's after-hours high
	// (seeded by the prevah loader). See levelBreak.
	pmhBrk   levelBreak
	prevAhHi float32           // previous trading day's 16:00-20:00 ET high; 0 = unknown
	pdl      dlevel.LowTracker // previous-day-low break (SetPrevLows)
	dl       dlevel.Tracker    // daily levels (SetDailyLevels) and today's crosses; levels survive the day reset, crosses do not
	ahBrk    levelBreak

	// The same two levels on the 5-minute timeframe: a break is a 5-minute
	// candle CLOSING above the level while armed (a wick above that closes back
	// under doesn't count); a 5-minute close at or below re-arms. Evaluated once
	// per candle, when the next candle's first print arrives or the 1 Hz clock
	// sees the window end (tick5m), whichever comes first.
	adv10    float32 // 10-day average daily volume (SetAdv10); 0 = unknown. Survives the day reset: the loader replaces it
	pmh5     levelClose
	ah5      levelClose
	lvl5Done int64   // start ms of the latest 5-minute candle evaluated for level closes
	lvl5Cl   float32 // that candle's close (the "previous close" for arming)

	// 1-minute 9 EMA (brkEma9Age / vsEma9 / ema9): the SAME EMA the charts
	// window draws on its 1m cells (engine registry.ts emaArr: SMA seed over the
	// first 9 closes, then k = 2/10), over COMPLETED 1-minute candles that
	// traded (the chart skips empty minutes too). Candles are keyed on each
	// agg's own StartMs, like the 5m tracker. Unlike day state the EMA is NOT
	// reset at 04:00: the chart's EMA runs on through yesterday's bars, so a
	// reset would make the two disagree every morning.
	m1Start  int64   // start ms of the forming 1-minute candle (0 = none)
	m1Cl     float64 // its close so far (latest print by StartMs)
	m1ClTs   int64
	m1Done   int64   // start ms of the latest FINALIZED candle; prints at or before it are late and ignored
	emaN     int     // completed candles folded since the engine started watching this ticker
	emaSum   float64 // SMA seed accumulator for the first emaPeriod closes
	ema9     float64 // the EMA after the latest completed candle (valid once emaN >= emaPeriod)
	emaAbove bool    // the latest completed candle closed at or above its EMA
	emaXMs   int64   // END ms of the candle that last closed below the EMA right after one at/above (0 = none)

	// 1-minute candle volume tracker (c1mChg / c1mRvol), see candle1m.go. Not
	// reset at the 04:00 rollover, like the EMA above.
	c1m vol1m

	// The shared closed 1-minute bar judge of the GREEN DILDO and VOLUME CLIMAX
	// alerts (dildo.go / climax.go): its own bar slots, ring, judge EMA and the
	// two latches. sig is the engine-wide wiring (nil = both signals off: the
	// judge is skipped entirely). Not reset at the 04:00 rollover either.
	sig *signals
	d1  dildo1m

	// Snapshot row cache (shard.snapshot): row is buildRow's output (before
	// signalRow, which always runs against the snapshot clock) for the shard's
	// current rowKey; dirty marks a state change since it was built. ref holds
	// the reference-table lookups the row was built from.
	row   core.Row
	dirty bool
	ref   refCache
}

// syncTo advances the ring head to sec, forward-filling intermediate seconds
// with the last known price so a sparse ticker still has a defined price_k_ago.
// Idempotent when already at sec.
func (t *tickerState) syncTo(sec int64) {
	if t.baseSec == 0 { // first touch this session
		t.baseSec = sec
		t.head = 0
		t.firstSec = sec // FIX: set firstSec here, not lazily on first agg, so the
		// 1 Hz clock advancing a ticker before its first trade can't bypass the
		// cold-start guard.
		return
	}
	gap := sec - t.baseSec
	if gap <= 0 {
		return // same second or out-of-order; writer applies last-value-wins
	}
	if gap >= ringLen {
		// Gap larger than the window (overnight / long halt): wipe and reseed.
		for i := range t.valid {
			t.valid[i] = false
		}
		t.head = 0
		t.baseSec = sec
		if t.last != 0 {
			t.ring[0] = t.last
			t.valid[0] = true
		}
		return
	}
	for i := int64(0); i < gap; i++ {
		t.head = (t.head + 1) % ringLen
		t.ring[t.head] = t.last
		t.valid[t.head] = t.last != 0
	}
	t.baseSec = sec
}

// applyAgg folds one per-second aggregate into the ticker state.
func (t *tickerState) applyAgg(a core.Agg) {
	prevLast := t.last
	sec := a.EndMs / 1000
	t.syncTo(sec)

	t.last = float32(a.Close)
	t.ring[t.head] = t.last
	t.valid[t.head] = true
	t.lastTS = a.EndMs

	// Session volume is engine-accumulated from per-bar volume. The provider's
	// "av" (accumulated daily volume) anchor (04:00 vs 09:30) is unverified, but
	// per-bar v is always the trades in THIS window, so summing it yields a
	// 04:00-anchored cumulative that matches the flat-file RVOL baseline fold.
	if a.Vol > 0 {
		t.sessVol += a.Vol
	}
	if a.DayOpen != 0 {
		t.dayOpen = float32(a.DayOpen)
	}
	if a.DayVWAP != 0 {
		t.dayVWAP = float32(a.DayVWAP)
	}
	// HOD/LOD from bar extremes (not last price), with explicit set flags. The
	// same true extremes fold into pendHi/pendLo — the in-progress 5-min
	// bucket's running high/low, committed by recordCurve at snapshot time.
	// Hot-path cost: one extra compare per branch inside the existing >0
	// guards; no allocation, no time math.
	// the day high BEFORE this print, for the early judge's new-high-of-day rule
	// (a minute's slot keeps the value seen when its first print opened it).
	if t.hodSet {
		t.d1.preHod = float64(t.hod)
	} else {
		t.d1.preHod = 0
	}
	if h := float32(a.High); h > 0 {
		if !t.hodSet || h > t.hod {
			t.hod = h
			t.hodSet = true
		}
		if h > t.pendHi {
			t.pendHi = h
		}
	}
	if l := float32(a.Low); l > 0 {
		if !t.lodSet || l < t.lod {
			t.lod = l
			t.lodSet = true
		}
		if t.pendLo == 0 || l < t.pendLo {
			t.pendLo = l
		}
	}
	t.accumScope(a)
	t.accum5m(a)
	t.accum1m(a)
	t.accumC1m(a)
	t.accumDildo(a)
	t.stepLevels(a, prevLast)
	t.updateBar(a)
}

// accumScope folds one aggregate into the current sub-session's scoped state:
// the session-open reference, session high/low, session volume, and the
// volume-weighted VWAP accumulators. Called from applyAgg only.
func (t *tickerState) accumScope(a core.Agg) {
	if !t.scopeOpenSet && a.Close != 0 {
		t.scopeOpen = float32(a.Close)
		t.scopeOpenSet = true
	}
	if h := float32(a.High); h > 0 && (!t.scopeHiSet || h > t.scopeHi) {
		t.scopeHi = h
		t.scopeHiSet = true
	}
	if l := float32(a.Low); l > 0 && (!t.scopeLoSet || l < t.scopeLo) {
		t.scopeLo = l
		t.scopeLoSet = true
	}
	if a.Vol > 0 {
		t.scopeVol += a.Vol
		vw := a.VWAP
		if vw == 0 {
			vw = a.Close // fall back to bar close when the feed omits VWAP
		}
		t.scopeVwapNum += vw * float64(a.Vol)
		t.scopeVwapDen += float64(a.Vol)
		m := a.StartMs / m1Ms
		sl := &t.vwMin[m&(vwMinSlots-1)]
		if sl.m != m {
			*sl = vwMinSum{m: m}
		}
		sl.num += vw * float64(a.Vol)
		sl.den += float64(a.Vol)
	}
}

// seedApply sets day-state from a snapshot Seed. Unlike applyAgg it SETS sessVol
// (to the snapshot's accumulated volume) rather than accumulating, so calling it
// again on a reconnect re-seed corrects the missed gap instead of double-counting.
func (t *tickerState) seedApply(s core.Seed) {
	sec := s.EndMs / 1000
	t.syncTo(sec)

	// A (re-)seed races the live stream: on reconnect the REST snapshot is
	// fetched while newer aggregates may already be applying, and losing that
	// race must not regress last/VWAP to the snapshot's older view — for a
	// thinly-traded symbol the next correcting agg could be minutes away.
	if s.EndMs >= t.lastTS {
		t.last = float32(s.Close)
		t.ring[t.head] = t.last
		t.valid[t.head] = true
		t.lastTS = s.EndMs
		if s.DayVWAP != 0 {
			t.dayVWAP = float32(s.DayVWAP)
		}
	}

	t.sessVol = s.Vol // SET, not +=
	if s.DayOpen != 0 {
		t.dayOpen = float32(s.DayOpen)
	}
	// Merge, don't overwrite: the snapshot covers the day up to when it was
	// taken, live aggs cover what happened since — the true day extremes are
	// the union of both.
	if h := float32(s.High); h > 0 && (!t.hodSet || h > t.hod) {
		t.hod, t.hodSet = h, true
	}
	if l := float32(s.Low); l > 0 && (!t.lodSet || l < t.lod) {
		t.lod, t.lodSet = l, true
	}
	if s.PrevClose != 0 {
		t.prevClose = float32(s.PrevClose)
	}

	// Session scope: seed the open/high/low references so a fresh mid-session start
	// has a defined chgSess/sessHigh/sessLow, but DON'T touch the volume/VWAP
	// accumulators — a reconnect re-seed must not zero scoped volume (we lack the
	// snapshot's intra-session split; it self-corrects at the next session reset).
	if !t.scopeOpenSet && s.Close != 0 {
		t.scopeOpen, t.scopeOpenSet = float32(s.Close), true
	}
	if h := float32(s.High); h > 0 && (!t.scopeHiSet || h > t.scopeHi) {
		t.scopeHi, t.scopeHiSet = h, true
	}
	if l := float32(s.Low); l > 0 && (!t.scopeLoSet || l < t.scopeLo) {
		t.scopeLo, t.scopeLoSet = l, true
	}
}

// recordCurve captures the cumulative session volume AND the per-bucket
// session OHLC at the current 5-min bucket. Recording begins at the first
// observed bucket (earlier buckets stay 0 = unknown, yielding null RVOL there
// and exclusion from sessionBars) so a mid-session start doesn't fabricate
// pre-start state. Runs in the shard's snapshot handler (10 Hz per ticker),
// so it stays O(1) and allocation-free: on rollover the outgoing bucket takes
// one final fold of the pending extremes, skipped buckets forward-fill as
// flat hi=lo=close=last bars with unchanged cumulative volume (one traversal,
// shared with the curve fill), and the current bucket is (re)stamped in place
// every call so a mid-bucket read is never stale.
func (t *tickerState) recordCurve(bucket int) {
	if bucket < 0 || bucket >= core.BucketCount {
		return
	}
	if t.curveBkt < 0 {
		t.curveBkt = bucket - 1 // start here; don't back-fill unknown earlier buckets
		t.curveFirst = bucket
	} else if bucket > t.curveBkt {
		// Rollover: commit the outgoing bucket with the extremes seen since the
		// previous snapshot, then open the new bucket's pending window fresh.
		// Aggs landing in the ≤100 ms between the boundary and this snapshot are
		// attributed to the outgoing bucket — the alternative is per-agg time
		// math in applyAgg, which the hot path forbids.
		t.stampBkt(t.curveBkt)
		t.pendHi, t.pendLo = 0, 0
	}
	for b := t.curveBkt + 1; b <= bucket; b++ {
		t.curve[b] = t.sessVol                                      // forward-fill across any skipped buckets
		t.bktHi[b], t.bktLo[b], t.bktCl[b] = t.last, t.last, t.last // flat gap bars
	}
	t.curve[bucket] = t.sessVol
	t.stampBkt(bucket) // in-place update: a mid-bucket read is never stale
	if bucket > t.curveBkt {
		t.curveBkt = bucket
	}
}

// stampBkt writes bucket b's hi/lo/close from the pending extremes, folding in
// last (so close can never sit outside [lo, hi]) and falling back to last
// alone when no agg extreme has arrived this bucket.
func (t *tickerState) stampBkt(b int) {
	hi, lo := t.pendHi, t.pendLo
	if t.last > 0 {
		if hi == 0 || t.last > hi {
			hi = t.last
		}
		if lo == 0 || t.last < lo {
			lo = t.last
		}
	}
	t.bktHi[b], t.bktLo[b], t.bktCl[b] = hi, lo, t.last
}

// Intraday session-bar read gates, applied in sessionBars at READ time —
// capture in recordCurve stays ungated and branch-free.
const (
	// minSessionBarsBuckets mirrors internal/pattern's minBars = 20: below it
	// Analyze refuses outright, so building the slice is pure waste. (The
	// engine imports only core, so the value is restated here, not imported.)
	minSessionBarsBuckets = 20
	// minSessionBarsVol is the session-volume floor: a name that has traded
	// under 50k shares all day has no tradeable intraday pattern. Together the
	// two gates bound SessionBars' map to the active fraction of the market.
	minSessionBarsVol = 50_000
)

// sessionBars builds this ticker's session-so-far 5-minute OHLCV series for
// the intraday flag pass, or nil when gated out. Bars run oldest→newest from
// the first observed bucket through cur (clamped to what recordCurve has
// stamped); pre-first buckets are excluded, never fabricated. Per-bucket
// volume is the delta of adjacent cumulative-curve entries — the first bar's
// V is curve[0] when observation began at the session's first bucket and 0
// otherwise (a mid-session first observation's cumulative includes the whole
// pre-observation day, which is not a bucket volume). O chains from the prior
// bar's close (first bar: its own close) — pattern.Analyze never reads O;
// the chain keeps the type honest. labels[b] is bucket b's ET start time,
// preformatted by the caller. Runs on the shard goroutine (ctrl closure).
func (t *tickerState) sessionBars(cur int, labels []string) []core.DailyBar {
	first := t.curveFirst
	if first < 0 {
		return nil
	}
	end := t.curveBkt
	if end > cur {
		end = cur
	}
	if end-first+1 < minSessionBarsBuckets {
		return nil
	}
	if t.sessVol < minSessionBarsVol {
		return nil
	}
	out := make([]core.DailyBar, 0, end-first+1)
	for b := first; b <= end; b++ {
		var v int64
		if b > first {
			v = t.curve[b] - t.curve[b-1]
		} else if b == 0 {
			v = t.curve[0]
		}
		if v < 0 {
			// A reconnect re-seed SETS sessVol from the REST snapshot, which
			// can come in LOWER than the engine-accumulated sum (that is its
			// correction contract), stepping the cumulative curve down
			// mid-session and turning the adjacent-entry delta negative. A
			// negative bucket volume is fabricated data — clamp to 0, the
			// honest "unknown". (Covers the curve[0] path too, against a
			// garbage negative seed volume.)
			v = 0
		}
		o := float64(t.bktCl[b])
		if len(out) > 0 {
			o = out[len(out)-1].C
		}
		out = append(out, core.DailyBar{
			Date: labels[b],
			O:    o,
			H:    float64(t.bktHi[b]),
			L:    float64(t.bktLo[b]),
			C:    float64(t.bktCl[b]),
			V:    v,
		})
	}
	return out
}

// priceAgo returns the price k seconds before now, and false if there isn't yet
// k seconds of session history (cold start) or that slot was never priced.
func (t *tickerState) priceAgo(k int) (float32, bool) {
	if t.firstSec == 0 || t.baseSec-t.firstSec < int64(k) {
		return 0, false // cold start -> null, never a fake %
	}
	idx := (t.head - k + ringLen) % ringLen
	if !t.valid[idx] {
		return 0, false
	}
	return t.ring[idx], true
}

// Inline candlestick chart: rolling ring of fixed-interval OHLC bars.
const (
	barIntervalMs = 15000 // 15-second candles
	barCount      = 24    // ~6 minutes of history
)

// updateBar folds a per-second aggregate into the current bar, rolling to a new
// bar at each interval boundary. Built purely from live aggregates.
func (t *tickerState) updateBar(a core.Agg) {
	if a.EndMs == 0 {
		return
	}
	start := (a.EndMs / barIntervalMs) * barIntervalMs
	if t.curBarTS == 0 {
		t.startBar(start, a)
		return
	}
	if start != t.curBarTS {
		t.commitBar()
		t.startBar(start, a)
		return
	}
	if a.High > t.curBar.H {
		t.curBar.H = a.High
	}
	if a.Low > 0 && (a.Low < t.curBar.L || t.curBar.L == 0) {
		t.curBar.L = a.Low
	}
	t.curBar.C = a.Close
	t.curBar.V += a.Vol
	t.invalidateBars()
}

// invalidateBars drops the memoized bars() projection; every mutation of the
// ring or the in-progress bar must call it.
func (t *tickerState) invalidateBars() {
	t.barsValid = false
	t.barsCache = nil
}

func (t *tickerState) startBar(start int64, a core.Agg) {
	o := a.Open
	if o == 0 {
		o = a.Close
	}
	h, l := a.High, a.Low
	if h == 0 {
		h = a.Close
	}
	if l == 0 {
		l = a.Close
	}
	t.curBarTS = start
	t.curBar = core.Bar{T: start, O: o, H: h, L: l, C: a.Close, V: a.Vol}
	t.invalidateBars()
}

func (t *tickerState) commitBar() {
	if t.curBarTS == 0 {
		return
	}
	t.barHead = (t.barHead + 1) % barCount
	t.barRing[t.barHead] = t.curBar
	if t.barFilled < barCount {
		t.barFilled++
	}
	t.invalidateBars()
}

// bars returns the completed bars (oldest→newest) plus the in-progress bar.
// Memoized: the same slice comes back until the ring changes (callers must
// treat it as read-only — snapshots already are).
func (t *tickerState) bars() []core.Bar {
	if t.barsValid {
		return t.barsCache
	}
	t.barsCache = t.buildBars()
	t.barsValid = true
	return t.barsCache
}

func (t *tickerState) buildBars() []core.Bar {
	n := t.barFilled
	if n == 0 && t.curBarTS == 0 {
		return nil
	}
	out := make([]core.Bar, 0, n+1)
	for i := 0; i < n; i++ {
		idx := ((t.barHead-(n-1-i))%barCount + barCount) % barCount
		out = append(out, t.barRing[idx])
	}
	if t.curBarTS != 0 {
		out = append(out, t.curBar)
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// frozenScope is a snapshot of one COMPLETED sub-session's scoped metrics,
// captured by freezeScope at the session boundary just before resetSessionScope
// wipes the live values. set means the boundary fired and the ticker actually
// traded that session. Each metric carries its own validity flag rather than a
// float32 NaN sentinel: widening a stored NaN back to float64 would fabricate a
// core.Null and mix sentinel semantics across the float32/float64 divide, so a
// value is either (flag=true, real number) or (flag=false, emit core.Null).
type frozenScope struct {
	set     bool // boundary fired and the ticker traded that session
	chgSet  bool // chg valid (prevClose was known at freeze time)
	hiSet   bool
	loSet   bool
	vwapSet bool
	gapSet  bool    // gap valid (session high and prevClose both known)
	fadeSet bool    // fade valid (session high and session-end price both known)
	chg     float32 // % vs prev close at the moment the session ended
	hi      float32
	lo      float32
	vwap    float32
	gap     float32 // % of the session HIGH vs prev close — the peak gain
	fade    float32 // % given back from the session high to session end (>= 0)
	vol     int64
	// complete: the slot covers the WHOLE session. False when the engine only
	// started watching partway through (a late start or restart), where the
	// high can be too low. Set by the live freeze when the engine saw the
	// session open, and by SeedFrozen (prefill's full-session REST bars).
	// Only the level-break trackers gate on it; the review fields don't.
	complete bool
}

// emit widens a frozen slot into the row's chg/hi/lo/vwap/gap/fade/vol targets
// through the per-field validity flags — never a bare float32 NaN cast. The
// float targets must already hold core.Null and the vol target 0 (toRow's
// defaults), so an unset slot or unset field is a no-op.
func (f *frozenScope) emit(chg, hi, lo, vwap, gap, fade *float64, vol *int64) {
	if !f.set {
		return
	}
	*vol = f.vol
	if f.chgSet {
		*chg = float64(f.chg)
	}
	if f.hiSet {
		*hi = float64(f.hi)
	}
	if f.loSet {
		*lo = float64(f.lo)
	}
	if f.vwapSet {
		*vwap = float64(f.vwap)
	}
	if f.gapSet {
		*gap = float64(f.gap)
	}
	if f.fadeSet {
		*fade = float64(f.fade)
	}
}

// newFrozenScope is the SINGLE derivation of a completed sub-session's frozen
// review metrics from raw window facts — shared by the live boundary freeze
// (freezeScope) and the restart backfill (Engine.SeedFrozen) so the two paths
// cannot drift. It always returns a set slot; "never traded" gating is the
// caller's job (freezeScope requires scopeOpenSet; SeedFrozen only feeds
// symbols the backfill actually found bars for).
//
// chg is pct(close, prevClose) — the chgDay value at the moment the session
// ended ("up 40% in the pre-market" means vs the previous close, not vs the
// 04:00 first print), gated on prevClose > 0; when prevClose is unknown the
// slot still carries hi/lo/vol/vwap with chgSet=false so chg emits Null.
//
// gap is pct(hi, prevClose) — the session HIGH vs the previous close, i.e.
// the PEAK gain (catching both overnight gaps and mid-session news spikes),
// NOT the close-vs-prevClose number (that is chg) and not vs the session's
// first print; gated on hiSet && prevClose > 0. fade is (hi-close)/hi*100 —
// the % given back from the session high to the session-end price, off the
// HIGH, not off the open — gated on hiSet && hi > 0 && close > 0 and clamped
// at >= 0 (a close above the recorded high, possible only via odd data,
// yields 0, never a negative fade). The two gate independently: an unknown
// prevClose nulls gap but leaves fade computable. vwap carries its own set
// flag because its validity source differs per caller (live: accumulated
// volume > 0; backfill: a positive VWAP in the fetched bars).
func newFrozenScope(prevClose, hi, lo, close float32, hiSet, loSet bool, vwap float64, vwapSet bool, vol int64) frozenScope {
	fs := frozenScope{set: true, vol: vol}
	if prevClose > 0 {
		fs.chg = float32(pct(close, prevClose))
		fs.chgSet = true
	}
	if hiSet {
		fs.hi, fs.hiSet = hi, true
	}
	if loSet {
		fs.lo, fs.loSet = lo, true
	}
	if vwapSet {
		fs.vwap = float32(vwap)
		fs.vwapSet = true
	}
	if hiSet && prevClose > 0 {
		fs.gap = float32(pct(hi, prevClose))
		fs.gapSet = true
	}
	if hiSet && hi > 0 && close > 0 {
		fade := (hi - close) / hi * 100
		if fade < 0 {
			fade = 0 // close above the recorded high (odd data): no fade, never negative
		}
		fs.fade = fade
		fs.fadeSet = true
	}
	return fs
}

// freezeScope captures the OUTGOING sub-session's scoped metrics into the
// matching frozen slot: pre-market into frozenPre at the 09:30 boundary,
// regular into frozenRth at 16:00. Any other outgoing session (Closed, Post)
// is a no-op — there is nothing to review from it. Runs only inside the
// session-boundary ctrl closure (once per boundary, never the hot path), and
// MUST run before resetSessionScope or it would freeze zeros.
//
// The metric definitions (chg vs prevClose, gap off the HIGH, fade off the
// HIGH clamped >= 0, and their independent gating) live in newFrozenScope —
// this is a thin mapping of live scope state into that shared derivation.
func (t *tickerState) freezeScope(old core.Session) {
	var dst *frozenScope
	switch old {
	case core.SessionPre:
		dst = &t.frozenPre
	case core.SessionRegular:
		dst = &t.frozenRth
	default:
		return
	}
	if !t.scopeOpenSet {
		return // never traded this session; leave the slot unset (Null/0)
	}
	var vwap float64
	vwapSet := t.scopeVwapDen > 0
	if vwapSet {
		vwap = t.scopeVwapNum / t.scopeVwapDen
	}
	*dst = newFrozenScope(t.prevClose, t.scopeHi, t.scopeLo, t.last,
		t.scopeHiSet, t.scopeLoSet, vwap, vwapSet, t.scopeVol)
}

// snapshotRow projects the current sub-session's scope into a RAW persistable
// SessionSnapshotRow — the exact scope state freezeScope reads, but without any
// percentage derivation (the row is stored raw; chg/gap/fade and the
// after-hours gain are derived at read time by the review consumer). ok=false
// when the ticker never traded this sub-session (the same scopeOpenSet gate
// freezeScope uses), so never-traded names cost no storage. Zero = unknown,
// per SessionSnapshotRow's validity convention. Runs only inside the
// session-boundary ctrl closure, never the hot path, and MUST run before
// resetSessionScope or it would snapshot zeros.
func (t *tickerState) snapshotRow() (core.SessionSnapshotRow, bool) {
	if !t.scopeOpenSet {
		return core.SessionSnapshotRow{}, false
	}
	r := core.SessionSnapshotRow{Sym: t.sym, Vol: t.scopeVol}
	if t.scopeHiSet {
		r.Hi = float64(t.scopeHi)
	}
	if t.scopeLoSet {
		r.Lo = float64(t.scopeLo)
	}
	if t.last > 0 {
		r.Close = float64(t.last)
	}
	if t.scopeVwapDen > 0 {
		r.Vwap = t.scopeVwapNum / t.scopeVwapDen
	}
	if t.prevClose > 0 {
		r.PrevClose = float64(t.prevClose)
	}
	return r, true
}

// resetSessionScope clears only the session-scoped metrics, called at every
// sub-session boundary (pre → regular → post) so chgSess/sessHigh/sessLow/
// sessVol/sessVwap measure the current session alone. The 04:00-anchored day
// state is left intact.
// levelBreak tracks breaks ABOVE a fixed price level (today's pre-market high,
// yesterday's after-hours high). A break is the first trade above the level
// while ARMED; a 1-second bar that closes at or below the level re-arms it, so
// a reclaim after a failed breakout counts as a fresh break. When a level is
// first seen (or changes) it is armed only if the price before this bar was at
// or below it: a stock already trading above when the level arrived (a restart,
// a late REST seed) broke it at an unknown earlier time and must not be
// reported as a fresh break now.
type levelBreak struct {
	lvl   float32 // the level the state below was built against
	armed bool
	brkMs int64 // EndMs of the bar that made the latest break (0 = none)
}

func (b *levelBreak) step(level, hi, close, prevLast float32, endMs int64) {
	if level <= 0 {
		return
	}
	if b.lvl != level {
		*b = levelBreak{lvl: level, armed: prevLast > 0 && prevLast <= level}
	}
	if b.armed && hi > level { // strictly above: touching the level is not a break
		b.brkMs = endMs
		b.armed = false
	}
	if close <= level {
		b.armed = true
	}
}

// pmhLevel is today's pre-market high once it is final (frozen at 09:30) AND
// complete; 0 otherwise.
func (t *tickerState) pmhLevel() float32 {
	if f := t.frozenPre; f.set && f.hiSet && f.complete && f.hi > 0 {
		return f.hi
	}
	return 0
}

// stepLevels folds one aggregate into both level-break trackers.
func (t *tickerState) stepLevels(a core.Agg, prevLast float32) {
	cl := float32(a.Close)
	if cl <= 0 {
		return
	}
	hi := float32(a.High)
	if hi <= 0 {
		hi = cl
	}
	t.pmhBrk.step(t.pmhLevel(), hi, cl, prevLast, a.EndMs)
	t.ahBrk.step(t.prevAhHi, hi, cl, prevLast, a.EndMs)
	if t.dl.Len() > 0 {
		ref := prevLast
		if ref <= 0 {
			ref = t.prevClose // the day's first print: a gap through a level is a cross
		}
		t.dl.Step(ref, hi, cl, a.EndMs)
	}
	if t.pdl.Level() > 0 {
		ref := prevLast
		if ref <= 0 {
			ref = t.prevClose // the day's first print: a gap down through the PDL is a break
		}
		lo := float32(a.Low)
		if lo <= 0 || lo > cl {
			lo = cl
		}
		t.pdl.Step(ref, lo, cl, a.EndMs)
	}
}

// levelClose is levelBreak on candle CLOSES: a break is a candle closing
// above the level while armed; a close at or below re-arms. As with
// levelBreak, a level that arrives while the previous candle already closed
// above it is not armed (the break happened at an unknown earlier time).
type levelClose struct {
	lvl   float32
	armed bool
	brkMs int64 // END ms of the candle that made the latest close-break (0 = none)
}

func (b *levelClose) close(level, c, prevC float32, endMs int64) {
	if level <= 0 || c <= 0 {
		return
	}
	if b.lvl != level {
		*b = levelClose{lvl: level, armed: prevC > 0 && prevC <= level}
	}
	if c > level {
		if b.armed {
			b.brkMs = endMs
			b.armed = false
		}
		return
	}
	b.armed = true
}

// m5Grace: how long after a 5-minute window ends the clock waits for late
// per-second aggregates before treating its candle as closed.
const m5Grace = 3 * 1000

// closeLevels5m evaluates one completed 5-minute candle against both levels,
// once per candle (both completion paths call it; the second is a no-op).
func (t *tickerState) closeLevels5m(start int64, c float32) {
	if start <= t.lvl5Done || c <= 0 {
		return
	}
	end := start + m5Ms
	t.pmh5.close(t.pmhLevel(), c, t.lvl5Cl, end)
	t.ah5.close(t.prevAhHi, c, t.lvl5Cl, end)
	t.lvl5Done, t.lvl5Cl = start, c
}

// tick5m closes the forming 5-minute candle for the level checks once its
// window has ended, so a stock that goes quiet still gets evaluated on time.
// Called by the shard's 1 Hz clock. It does not roll the 5m tracker itself.
func (t *tickerState) tick5m(sec int64) {
	if t.m5Start != 0 && sec*1000 >= t.m5Start+m5Ms+m5Grace {
		t.closeLevels5m(t.m5Start, t.m5Cl)
	}
}

// 1-minute 9 EMA constants. emaWarm: the scanner's EMA starts from its own
// first 9 candles, while the chart's runs on from yesterday; the seed's
// influence decays by (1-k) per candle, 0.8^21 ~ 0.9% after 30 candles, so the
// cross signal (and the ema9 / vsEma9 fields) only appear after emaWarm
// completed candles, where both EMAs agree on which side of it price closed.
const (
	m1Ms      = 60 * 1000
	m1Grace   = 3 * 1000 // finalize a candle this long after its minute ends (late per-second aggs)
	emaPeriod = 9
	emaK      = 2.0 / (emaPeriod + 1)
	emaWarm   = 30
)

// accum1m folds one aggregate into the forming 1-minute candle; a print for a
// later minute completes the forming candle first.
func (t *tickerState) accum1m(a core.Agg) {
	ts := a.StartMs
	if ts == 0 {
		ts = a.EndMs - 1
	}
	cl := a.Close
	if ts <= 0 || cl <= 0 {
		return
	}
	start := ts - ts%m1Ms
	switch {
	case start <= t.m1Done:
		return // its candle is already folded into the EMA: a late print
	case start > t.m1Start:
		if t.m1Start != 0 {
			t.finalize1m()
		}
		t.m1Start, t.m1Cl, t.m1ClTs = start, cl, ts
	case start == t.m1Start && ts >= t.m1ClTs:
		t.m1Cl, t.m1ClTs = cl, ts // the close is the LATEST print, not the last to arrive
	}
}

// tick1m completes the forming candle once its minute has ended (plus grace),
// so a stock that stops trading still gets its candle closed on time. Called by
// the shard's 1 Hz clock.
func (t *tickerState) tick1m(sec int64) {
	if t.m1Start != 0 && sec*1000 >= t.m1Start+m1Ms+m1Grace {
		t.finalize1m()
	}
}

// finalize1m folds the forming candle's close into the EMA exactly the way the
// chart engine's emaArr does, and records a cross: this candle closed BELOW its
// EMA while the previous completed candle closed at or above.
func (t *tickerState) finalize1m() {
	c := t.m1Cl
	end := t.m1Start + m1Ms
	t.m1Done, t.m1Start = t.m1Start, 0
	t.emaN++
	switch {
	case t.emaN < emaPeriod:
		t.emaSum += c
		return
	case t.emaN == emaPeriod:
		t.emaSum += c
		t.ema9 = t.emaSum / emaPeriod
	default:
		t.ema9 = c*emaK + t.ema9*(1-emaK)
	}
	below := c < t.ema9 // strictly: closing AT the EMA is not closing under it
	if below && t.emaAbove && t.emaN > emaWarm {
		t.emaXMs = end
	}
	t.emaAbove = !below
}

// m5Ms is the candle width for the 5-minute break tracker.
const m5Ms = 5 * 60 * 1000

// accum5m folds one aggregate into the 5-minute candle break tracker. Runs on
// the hot path: one modulo, a few compares, no allocation.
func (t *tickerState) accum5m(a core.Agg) {
	ts := a.StartMs
	if ts == 0 {
		ts = a.EndMs - 1 // window start missing: step inside the window, never onto its end boundary
	}
	if ts <= 0 {
		return
	}
	cl := float32(a.Close)
	lo := float32(a.Low)
	if lo <= 0 {
		lo = cl
	}
	hi := float32(a.High)
	if hi <= 0 {
		hi = cl
	}
	if lo <= 0 {
		return
	}
	start := ts - ts%m5Ms
	switch {
	case start == t.m5Start:
		// same candle
	case start > t.m5Start:
		// A new candle opens and the outgoing one becomes "previous" — unless it
		// opened before we were listening: its true low is unknowable, and a
		// too-high low would hand the next candle a fake break.
		if t.m5Start != 0 {
			t.closeLevels5m(t.m5Start, t.m5Cl)
			t.m5PrevStart, t.m5PrevLo = t.m5Start, t.m5Lo
			if t.m5Partial {
				t.m5PrevLo = 0 // low unknowable; the close below is still exact
			}
			t.m5PrevCl, t.m5PrevClTs = t.m5Cl, t.m5ClTs
		}
		t.m5Start, t.m5Lo, t.m5BrkMs = start, 0, 0
		t.m5Cl, t.m5ClTs, t.m5BrkClMs = 0, 0, 0
		t.m5Hi, t.m5BrkClUpMs = 0, 0
		t.m5Partial = start < t.m5ListenMs
	case start == t.m5PrevStart:
		// A late print for the previous candle, arriving after the next one
		// opened. It belongs to that candle's low; if it undercuts what the
		// forming candle reached, the "break" only existed because this print
		// was missing — revoke it.
		if t.m5PrevLo > 0 && lo < t.m5PrevLo {
			t.m5PrevLo = lo
			if t.m5Lo >= t.m5PrevLo {
				t.m5BrkMs = 0
			}
		}
		// A LATER print than the one taken as the close IS the close.
		if cl > 0 && ts >= t.m5PrevClTs {
			t.m5PrevCl, t.m5PrevClTs = cl, ts
			if t.m5Lo >= t.m5PrevCl {
				t.m5BrkClMs = 0
			}
			if t.m5Hi <= t.m5PrevCl {
				t.m5BrkClUpMs = 0
			}
		}
		return
	default:
		return // older than the previous candle: that history is closed
	}
	if t.m5Lo == 0 || lo < t.m5Lo {
		t.m5Lo = lo
	}
	if hi > t.m5Hi {
		t.m5Hi = hi
	}
	if cl > 0 && ts >= t.m5ClTs {
		t.m5Cl, t.m5ClTs = cl, ts
	}
	brkAt := a.EndMs
	if brkAt <= 0 {
		brkAt = ts + 1000
	}
	// Strictly below: trading AT the level is a test of it, not a break.
	if t.m5BrkMs == 0 && t.m5PrevLo > 0 && t.m5Lo < t.m5PrevLo {
		t.m5BrkMs = brkAt
	}
	if t.m5BrkClMs == 0 && t.m5PrevCl > 0 && t.m5Lo < t.m5PrevCl {
		t.m5BrkClMs = brkAt
	}
	// Long side, strictly above: trading AT the close is a test, not a break.
	if t.m5BrkClUpMs == 0 && t.m5PrevCl > 0 && t.m5Hi > t.m5PrevCl {
		t.m5BrkClUpMs = brkAt
	}
}

// m5View resolves the break against the CURRENT 5-minute window. If the ticker
// hasn't traded in the current window yet, there is no current candle — so no
// live break — and the candle we hold has in fact closed, which makes IT the
// previous candle a fresh one will be measured against.
func (t *tickerState) m5View(nowSec int64) (v m5Levels) {
	if t.m5Start == 0 {
		return v
	}
	nowMs := nowSec * 1000
	if t.m5Start >= nowMs-nowMs%m5Ms {
		return m5Levels{prevLo: t.m5PrevLo, prevCl: t.m5PrevCl, brkLoMs: t.m5BrkMs, brkClMs: t.m5BrkClMs, brkClUpMs: t.m5BrkClUpMs}
	}
	v.prevCl = t.m5Cl
	if !t.m5Partial {
		v.prevLo = t.m5Lo
	}
	return v
}

// m5Levels is the previous candle's low/close and when the current candle first
// traded below each (0 = not broken, or no level).
type m5Levels struct {
	prevLo, prevCl              float32
	brkLoMs, brkClMs, brkClUpMs int64 // brkClUpMs: first trade ABOVE prevCl
}

func (t *tickerState) resetSessionScope() {
	t.scopeOpen, t.scopeOpenSet = 0, false
	t.scopeHi, t.scopeHiSet = 0, false
	t.scopeLo, t.scopeLoSet = 0, false
	t.scopeVol = 0
	t.scopeVwapNum, t.scopeVwapDen = 0, 0
}

// resetSession clears rolling + cumulative state at the start of a new extended
// session (day rollover). prevClose is preserved; the caller re-seeds it.
func (t *tickerState) resetSession() {
	for i := range t.valid {
		t.valid[i] = false
	}
	t.head = 0
	t.baseSec = 0
	t.firstSec = 0
	t.sessVol = 0
	t.dayOpen = 0
	t.hod, t.lod = 0, 0
	t.hodSet, t.lodSet = false, false
	t.curve = [core.BucketCount]int64{}
	t.curveBkt = -1
	t.bktHi = [core.BucketCount]float32{}
	t.bktLo = [core.BucketCount]float32{}
	t.bktCl = [core.BucketCount]float32{}
	t.pendHi, t.pendLo = 0, 0
	t.curveFirst = -1
	t.barRing = [barCount]core.Bar{}
	t.barHead = -1
	t.barFilled = 0
	t.curBar = core.Bar{}
	t.curBarTS = 0
	t.invalidateBars()
	// A new extended day: yesterday's frozen pre/rth review slots must not
	// survive into today (the full-reset path never freezes).
	t.frozenPre = frozenScope{}
	t.frozenRth = frozenScope{}
	// The first candle of a new day has no previous candle: yesterday's 19:55
	// post-market bar is not a level anyone trades the 04:00 open against.
	t.m5Start, t.m5Lo, t.m5Partial = 0, 0, false
	t.m5PrevStart, t.m5PrevLo, t.m5BrkMs = 0, 0, 0
	t.m5Cl, t.m5ClTs, t.m5PrevCl, t.m5PrevClTs, t.m5BrkClMs = 0, 0, 0, 0, 0
	t.m5Hi, t.m5BrkClUpMs = 0, 0
	// Yesterday's levels are meaningless today; the prevah loader re-seeds the
	// after-hours high for the NEW previous day once the reset has landed.
	t.pmhBrk, t.ahBrk, t.prevAhHi = levelBreak{}, levelBreak{}, 0
	t.dl.Reset()
	t.pdl.Reset()
	t.pmh5, t.ah5, t.lvl5Done, t.lvl5Cl = levelClose{}, levelClose{}, 0, 0
	t.emaXMs = 0 // yesterday's 9 EMA loss is not today's signal; the EMA itself runs on
	// t.c1m is deliberately NOT reset either: it is about the latest minutes
	// and a reset would only add a nine-minute blind spot each morning.
	t.resetSessionScope()
}

// pmVolX is today's pre-market volume as a multiple of the 10-day average daily
// volume (adv10), with ok=false when it is not known. It is the SINGLE
// definition of the scan field pmVolX: toRow stamps it onto the row and
// Engine.PmVolLeaders ranks the flag-scan candidates by it, so the candidate
// list and the column a trader sees can never disagree (there is no second copy
// of the formula to drift).
//
// Pre-market volume so far. During pre-market the day volume IS the pre-market
// volume, and the periodic snapshot re-seed keeps it whole across a restart
// (scopeVol would restart from zero). After 09:30 use the frozen pre-market
// volume, but only when that freeze saw the whole session (or prefill seeded
// it): a partial one would understate it, so it reports unknown (ok=false)
// rather than a misleadingly low multiple. No adv10 -> unknown as well.
//
// sess is the engine's session at the moment of the read (the snapshot's for
// toRow, the live one for PmVolLeaders). Reads only the ticker's own state, so
// like toRow it must run on the owning shard goroutine.
func (t *tickerState) pmVolX(sess core.Session) (float64, bool) {
	if t.adv10 > 0 {
		switch {
		case sess == core.SessionPre:
			return float64(t.sessVol) / float64(t.adv10), true
		case t.frozenPre.set && t.frozenPre.complete:
			return float64(t.frozenPre.vol) / float64(t.adv10), true
		}
	}
	return 0, false
}

// refVals is everything a row reads from the three reference tables (RVOL
// baseline, floats, security types) for one ticker. Resolving it costs several
// map lookups, so the snapshot path keeps it per ticker (refs) until a table is
// swapped or the 5-minute bucket rolls.
type refVals struct {
	rvolBase float64
	rvolOK   bool
	avgVol   float64
	avgVolOK bool
	shsOut   *int64 // owned copies, shared read-only by the rows built from them
	float    *int64
	secType  string
}

// refCache is a ticker's resolved refVals and the tables and bucket it was
// resolved for. The tables are never modified after they are swapped in (each
// Set* stores a freshly built one), so their pointers identify their contents.
type refCache struct {
	baseline *core.BaselineTable
	floats   *map[string]core.FloatRef
	secTypes *map[string]string
	bucket   int
	ok       bool
	v        refVals
}

// refs returns the ticker's reference values for req, re-resolving only what a
// table swap or a bucket roll changed.
func (t *tickerState) refs(req *snapReq) *refVals {
	c := &t.ref
	switch {
	case !c.ok || c.baseline != req.baseline || c.floats != req.floatsRef || c.secTypes != req.secTypesRef:
		c.v = resolveRefs(t.sym, req.bucket, req.baseline, req.floats, req.secTypes)
		c.baseline, c.floats, c.secTypes, c.bucket, c.ok = req.baseline, req.floatsRef, req.secTypesRef, req.bucket, true
	case c.bucket != req.bucket:
		c.v.rvolBase, c.v.rvolOK = resolveRvol(t.sym, req.bucket, req.baseline)
		c.bucket = req.bucket
	}
	return &c.v
}

// resolveRvol is the RVOL baseline at bucket: today's cumulative volume is
// divided by it when enough sessions contributed.
func resolveRvol(sym string, bucket int, baseline *core.BaselineTable) (float64, bool) {
	if baseline != nil {
		if base, ok := baseline.Get(sym, bucket); ok && baseline.CountAt(sym, bucket) >= minBaselineSessions {
			return base, true
		}
	}
	return 0, false
}

// resolveRefs performs every reference-table lookup a row needs.
func resolveRefs(sym string, bucket int, baseline *core.BaselineTable, floats map[string]core.FloatRef, secTypes map[string]string) (v refVals) {
	v.rvolBase, v.rvolOK = resolveRvol(sym, bucket, baseline)

	// AvgVol: trailing-mean REGULAR-SESSION volume — see FieldAvgVol's doc for
	// why this slices [09:30,16:00) out of the same baseline RVOL uses
	// rather than using the full extended-hours cumulative total.
	if baseline != nil {
		if hi, ok1 := baseline.Get(sym, regularCloseBucket); ok1 {
			if lo, ok2 := baseline.Get(sym, regularOpenBucket); ok2 && baseline.CountAt(sym, regularCloseBucket) >= minBaselineSessions {
				if adv := hi - lo; adv > 0 {
					v.avgVol, v.avgVolOK = adv, true
				}
			}
		}
	}

	if fr, ok := floats[sym]; ok {
		if fr.SharesOut != nil {
			so := *fr.SharesOut
			v.shsOut = &so
		}
		if eff := effectiveFloat(fr); eff != nil {
			f := *eff
			v.float = &f
		}
	}
	if ty, ok := secTypes[sym]; ok {
		v.secType = ty
	}
	return v
}

// toRow projects the state into the canonical metric Row for the given
// reference tables. Runs inside the shard goroutine, so reads of t are
// race-free. baseline/floats are immutable snapshots passed in by the merger.
// The snapshot path calls buildRow with the ticker's cached refs instead.
func (t *tickerState) toRow(bucket int, baseline *core.BaselineTable, floats map[string]core.FloatRef, secTypes map[string]string, sess core.Session) core.Row {
	v := resolveRefs(t.sym, bucket, baseline, floats, secTypes)
	return t.buildRow(sess, &v)
}

// buildRow is toRow with the reference lookups already resolved.
func (t *tickerState) buildRow(sess core.Session, ref *refVals) core.Row {
	r := core.Row{
		Sym:             t.sym,
		Last:            float64(t.last),
		Vol:             t.sessVol,
		Vwap:            float64(t.dayVWAP),
		Hod:             float64(t.hod),
		Lod:             float64(t.lod),
		Halted:          t.halted,
		Sess:            sess,
		Chg1m:           core.Null,
		Chg5m:           core.Null,
		Chg15m:          core.Null,
		ChgDay:          core.Null,
		Gap:             core.Null,
		VsVwap:          core.Null,
		VsHod:           core.Null,
		Rvol:            core.Null,
		AvgVol:          core.Null,
		Rot:             core.Null,
		ChgSess:         core.Null,
		SessHigh:        core.Null,
		SessLow:         core.Null,
		SessVol:         t.scopeVol,
		SessVwap:        core.Null,
		PreChg:          core.Null,
		PreHigh:         core.Null,
		PreLow:          core.Null,
		PreVwap:         core.Null,
		PreGap:          core.Null,
		PreFade:         core.Null,
		PreVolPctAdv:    core.Null,
		RthChg:          core.Null,
		RthHigh:         core.Null,
		RthLow:          core.Null,
		RthVwap:         core.Null,
		RthGap:          core.Null,
		RthFade:         core.Null,
		Brk5mAge:        core.Null,
		VsPrev5mLow:     core.Null,
		Prev5mLow:       core.Null,
		Brk5mCloseAge:   core.Null,
		VsPrev5mClose:   core.Null,
		Prev5mClose:     core.Null,
		Brk5mCloseUpAge: core.Null,
		BrkPmhAge:       core.Null,
		VsPmh:           core.Null,
		PrevAhHigh:      core.Null,
		BrkPrevAhAge:    core.Null,
		VsPrevAh:        core.Null,
		BrkEma9Age:      core.Null,
		VsEma9:          core.Null,
		Ema9:            core.Null,
		C1mChg:          core.Null,
		C1mRvol:         core.Null,
		BrkPmh5mAge:     core.Null,
		Adv10:           core.Null,
		PmVolX:          core.Null,
		BrkPrevAh5mAge:  core.Null,
		DbLevel:         core.Null,
		DbType:          core.Null,
		DbPct:           core.Null,
		DbAge:           core.Null,
		PdlLevel:        core.Null,
		PdlPct:          core.Null,
		PdlAge:          core.Null,
		EpPass:          core.Null,
		EpGap:           core.Null,
		EpVolX:          core.Null,
		EpEarnAge:       core.Null,
		Ep3m:            core.Null,
		EpAdr:           core.Null,
		EpAdrDays:       core.Null,
		EpStop:          core.Null,
		EpOrh:           core.Null,
		EpBrkAge:        core.Null,
		EpRiskPct:       core.Null,
		// The signal fields: the shard stamps the judge's latches over these
		// (signalRow, with the snapshot clock); the four dildoPb* ones are the
		// scanner's overlay. Null, never 0: `dildoAge <= 240` must not read an
		// absent dildo as a fresh one.
		DildoAge:      core.Null,
		DildoBody:     core.Null,
		DildoEarly:    core.Null,
		DildoVolX:     core.Null,
		DildoDvol:     core.Null,
		DildoPbAge:    core.Null,
		DildoPbPx:     core.Null,
		DildoTouchAge: core.Null,
		DildoPbState:  core.Null,
		ClimaxAge:     core.Null,
		ClimaxDropPct: core.Null,
		ClimaxVolX:    core.Null,
		ClimaxWickPct: core.Null,
		ClimaxDvol:    core.Null,
		ClimaxLow:     core.Null,
		ClimaxState:   core.Null,
		ClimaxConfAge: core.Null,
		ClimaxConfPx:  core.Null,
		BbsAge:        core.Null,
		BbsSupport:    core.Null,
		BbsState:      core.Null,
		BbsDvol:       core.Null,
		BbsDistPct:    core.Null,
		PcAge:         core.Null,
		PcBodyPct:     core.Null,
		PcTopPct:      core.Null,
		PcBodyX:       core.Null,
		PcVolX:        core.Null,
		PcDvol:        core.Null,
		PsPass:        core.Null,
		PsFromOpen:    core.Null,
		PsEmaSlope:    core.Null,
		PsGap:         core.Null,
		PsEntry:       core.Null,
		PsStop:        core.Null,
		PsTarget:      core.Null,
		PsAge:         core.Null,
		PsAplus:       core.Null,
		PsBaseRange:   core.Null,
		IgnAge:        core.Null,
		IgnBodyPct:    core.Null,
		IgnVolX:       core.Null,
		IgnFresh:      core.Null,
		IgnPass:       core.Null,
		VbAge:         core.Null,
		VbLevel:       core.Null,
		VbVolX:        core.Null,
		VbEma:         core.Null,
		VbState:       core.Null,
		VbEntryAge:    core.Null,
		VbEntryPx:     core.Null,
		EvBullAge:     core.Null,
		EvBearAge:     core.Null,
		EvEma:         core.Null,
		EvVwap:        core.Null,
		EvDist:        core.Null,
		EvBarVol:      core.Null,
		EvBarDvol:     core.Null,
		EvSessVol:     core.Null,
		NewsAge:       -1, // news overlay (merger/scan) sets this when applicable
		Bars:          t.bars(),
	}

	// Frozen prior-session slots (plain field copies; the freeze itself only
	// ever runs in the session-boundary ctrl closure).
	t.frozenPre.emit(&r.PreChg, &r.PreHigh, &r.PreLow, &r.PreVwap, &r.PreGap, &r.PreFade, &r.PreVol)
	t.frozenRth.emit(&r.RthChg, &r.RthHigh, &r.RthLow, &r.RthVwap, &r.RthGap, &r.RthFade, &r.RthVol)

	if p, ok := t.priceAgo(60); ok && p != 0 {
		r.Chg1m = pct(t.last, p)
	}
	if p, ok := t.priceAgo(300); ok && p != 0 {
		r.Chg5m = pct(t.last, p)
	}
	if p, ok := t.priceAgo(900); ok && p != 0 {
		r.Chg15m = pct(t.last, p)
	}
	if t.prevClose != 0 {
		r.ChgDay = pct(t.last, t.prevClose)
	}
	if t.dayOpen != 0 && t.prevClose != 0 {
		r.Gap = pct(t.dayOpen, t.prevClose)
	}
	if t.dayVWAP != 0 {
		r.VsVwap = pct(t.last, t.dayVWAP)
	}
	if t.hodSet && t.hod != 0 {
		r.VsHod = pct(t.last, t.hod) // <= 0: distance below the high
	}
	// 5-minute candle break. baseSec is "now" to the second: the engine's 1 Hz
	// clock advances every tracked ticker's ring, traded or not.
	m5 := t.m5View(t.baseSec)
	if m5.prevLo > 0 {
		r.Prev5mLow = float64(m5.prevLo)
		if t.last != 0 {
			r.VsPrev5mLow = pct(t.last, m5.prevLo)
		}
		if m5.brkLoMs > 0 {
			r.Brk5mAge = float64(max(t.baseSec-m5.brkLoMs/1000, 0))
		}
	}
	if m5.prevCl > 0 {
		r.Prev5mClose = float64(m5.prevCl)
		if t.last != 0 {
			r.VsPrev5mClose = pct(t.last, m5.prevCl)
		}
		if m5.brkClMs > 0 {
			r.Brk5mCloseAge = float64(max(t.baseSec-m5.brkClMs/1000, 0))
		}
		if m5.brkClUpMs > 0 {
			r.Brk5mCloseUpAge = float64(max(t.baseSec-m5.brkClUpMs/1000, 0))
		}
	}
	if lvl := t.pmhLevel(); lvl > 0 {
		if t.last != 0 {
			r.VsPmh = pct(t.last, lvl)
		}
		if t.pmhBrk.brkMs > 0 && t.pmhBrk.lvl == lvl {
			r.BrkPmhAge = float64(max(t.baseSec-t.pmhBrk.brkMs/1000, 0))
		}
	}
	if t.emaN > emaWarm {
		r.Ema9 = t.ema9
		if t.last != 0 {
			r.VsEma9 = (float64(t.last)/t.ema9 - 1) * 100
		}
		if t.emaXMs > 0 {
			r.BrkEma9Age = float64(max(t.baseSec-t.emaXMs/1000, 0))
		}
	}
	r.C1mChg, r.C1mRvol = t.c1m.view(t.baseSec)
	if t.adv10 > 0 {
		r.Adv10 = float64(t.adv10)
	}
	if x, ok := t.pmVolX(sess); ok {
		r.PmVolX = x
	}
	if lvl := t.pmhLevel(); lvl > 0 && t.pmh5.brkMs > 0 && t.pmh5.lvl == lvl {
		r.BrkPmh5mAge = float64(max(t.baseSec-t.pmh5.brkMs/1000, 0))
	}
	if t.dl.Len() > 0 {
		if lv, ms, ok := t.dl.Best(t.last, t.lod, t.lodSet); ok {
			r.DbLevel = float64(lv.Price)
			r.DbType = float64(lv.Kind)
			r.DbPct = pct(t.last, lv.Price)
			if ms > 0 {
				r.DbAge = float64(max(t.baseSec-ms/1000, 0))
			}
		}
	}
	if t.pdl.Level() > 0 {
		if lv, ms, ok := t.pdl.Best(t.last, t.hod, t.hodSet); ok {
			r.PdlLevel = float64(lv)
			r.PdlPct = pct(lv, t.last)
			if ms > 0 {
				r.PdlAge = float64(max(t.baseSec-ms/1000, 0))
			}
		}
	}
	if t.prevAhHi > 0 && t.ah5.brkMs > 0 && t.ah5.lvl == t.prevAhHi {
		r.BrkPrevAh5mAge = float64(max(t.baseSec-t.ah5.brkMs/1000, 0))
	}
	if t.prevAhHi > 0 {
		r.PrevAhHigh = float64(t.prevAhHi)
		if t.last != 0 {
			r.VsPrevAh = pct(t.last, t.prevAhHi)
		}
		if t.ahBrk.brkMs > 0 && t.ahBrk.lvl == t.prevAhHi {
			r.BrkPrevAhAge = float64(max(t.baseSec-t.ahBrk.brkMs/1000, 0))
		}
	}

	// Session-scoped change: the regular session anchors to the official day open;
	// pre/post anchor to the first price seen after the session reset.
	sessBase := float32(0)
	if sess == core.SessionRegular && t.dayOpen != 0 {
		sessBase = t.dayOpen
	} else if t.scopeOpenSet {
		sessBase = t.scopeOpen
	}
	if sessBase != 0 {
		r.ChgSess = pct(t.last, sessBase)
	}
	if t.scopeHiSet {
		r.SessHigh = float64(t.scopeHi)
	}
	if t.scopeLoSet {
		r.SessLow = float64(t.scopeLo)
	}
	if t.scopeVwapDen > 0 {
		r.SessVwap = t.scopeVwapNum / t.scopeVwapDen
	}

	// RVOL: today's cumulative vs the trailing-mean baseline at this bucket.
	if ref.rvolOK {
		r.Rvol = float64(t.sessVol) / ref.rvolBase
	}

	// AvgVol (resolveRefs): trailing-mean REGULAR-SESSION volume.
	var avgVol float64
	var avgVolOK bool
	if ref.avgVolOK {
		r.AvgVol = ref.avgVol
		avgVol, avgVolOK = ref.avgVol, true
	}

	// PreVolPctAdv: pre-market volume so far / AvgVol — LIVE (t.scopeVol)
	// while pre-market is still running (frozenPre hasn't fired yet; without
	// this branch the field would be null for the entire session it exists
	// for), FROZEN (t.frozenPre.vol) once the 09:30 boundary has passed.
	if avgVolOK {
		var preVolNow int64
		var preVolNowOK bool
		switch {
		case sess == core.SessionPre:
			preVolNow, preVolNowOK = t.scopeVol, true
		case t.frozenPre.set:
			preVolNow, preVolNowOK = t.frozenPre.vol, true
		}
		if preVolNowOK {
			r.PreVolPctAdv = float64(preVolNow) / avgVol * 100
		}
	}

	// Float / shares-out / rotation. Degrades to null when float is unknown.
	r.ShsOut = ref.shsOut
	if ref.float != nil {
		r.Float = ref.float
		if f := *ref.float; f > 0 {
			r.Rot = float64(t.sessVol) / float64(f)
		}
	}

	r.SecType = ref.secType
	return r
}

// effectiveFloat prefers true free float, falling back to shares outstanding as
// a labeled proxy.
func effectiveFloat(fr core.FloatRef) *int64 {
	if fr.FloatShares != nil {
		return fr.FloatShares
	}
	return fr.SharesOut
}

// pct returns the percent change from then to now (e.g. 2.5 for +2.5%).
func pct(now, then float32) float64 {
	return (float64(now)/float64(then) - 1) * 100
}
