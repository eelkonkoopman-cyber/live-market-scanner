package engine

import (
	"math"
	"sync/atomic"

	"github.com/eelkonkoopman/marketscanner/internal/climax"
	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
	"github.com/eelkonkoopman/marketscanner/internal/volbreak"
)

// The shared CLOSED 1-minute bar judge: the engine side of two market-wide
// 1-minute ALERTS, both "look now" prompts and not systems (neither showed an
// edge after costs in calibration):
//
//   - GREEN DILDO (internal/dildopb; alert 1 "SYM: green dildo"): a big green
//     candle closing near its high on a volume spike. A hit sets the ticker's
//     dildo latch (the row's dildoAge..dildoDvol) and is handed, non-blocking,
//     to the flagscan service (Engine.DildoHits), which judges alert 2 - the
//     pullback to the chart's 9 EMA - on the chart's own REST bars.
//   - VOLUME CLIMAX (internal/climax; "SYM: volume climax" and "SYM: bottom
//     confirmed"): a fresh low of the day's last 10 traded bars on the biggest
//     volume of them, a lower wick, under the 9 EMA; both alerts are judged
//     here (climax.go): the climax arms a per-ticker climax.Latch, and every
//     later closed traded bar of the same ET day steps it.
//
// Every alert either judge calls (dildo, climax, bottom, climaxFail) is also
// handed, non-blocking, to the Signal window's signal history (signals.emit ->
// Engine.SignalHits, Options.SignalLog -> internal/flaglog).
//
// Bars are CLOCK minutes keyed on each aggregate's OWN StartMs (like vol1m and
// the 1-minute EMA): open = the print with the EARLIEST StartMs, close = the
// LATEST, high/low the extremes, volume the sum. Two slots hold the newest
// minute and the one before it while it is still inside its grace, so a print
// that arrives up to m1Grace (3 s) after its minute ended still lands in its
// bar. The 1 Hz clock (tickDildo) judges each slot exactly once, at its minute
// end + m1Grace - the same grace as the engine's 1-minute EMA and flagscan's
// CloseGrace. A print for a minute already judged is dropped.
//
// What a judge needs is a 10-slot ring of the last JUDGED TRADED bars (empty
// clock minutes never enter it: the chart skips them too), a count of today's
// bars (todayN, the climax needs 10 of the SAME ET day) and the judge's own
// 1-minute 9 EMA. The ring's sums are RECOMPUTED oldest -> newest at judge time
// (never running add/subtract sums, which drift by ulps at the thresholds), in
// the exact order the chart rule (dildopb / climax Evaluate) sums its priors.
//
// Cost: accumDildo is O(1) per print, tickDildo O(1) per ticker per second,
// and a judge O(10) once per closed TRADED minute per ticker; none allocates
// (BenchmarkAccumDildo, BenchmarkJudge, BenchmarkSignals11k). Nothing here is
// reset at the 04:00 rollover: the ring and the EMA are about the latest
// minutes (the chart's EMA runs on from yesterday too), the ET day change
// inside the judge resets todayN, and the row's "today" rule (signalRow) hides
// a latch from before today's 04:00.

// judgeRing is the ring length: dildopb's PriorBars and climax's Lookback
// defaults (a test pins both to it).
const judgeRing = 10

// earlyRing is the early judge's base length: dildopb.DefaultEarlyParams().BaseBars
// (a test pins it). The alerts judge with the defaults, like the dildo itself.
const earlyRing = 30

// earlyParams: the shared early judge's defaults (the trader's shipped settings).
var earlyParams = dildopb.DefaultEarlyParams()

// The rule parameters. The climax rule is read ONCE (calibrated defaults); the
// green dildo's follow the Signal window's tuning panel (dildopb.Live, swapped
// atomically and read once per judged bar).
var climaxParams = climax.DefaultParams()

// dildoHitsBuffer is the hits channel's capacity: a whole-market burst at the
// 09:30 open is a few dozen names; beyond this a hit is dropped and counted.
const dildoHitsBuffer = 256

// signalHitsBuffer is the history channel's capacity (Engine.SignalHits): both
// alerts' bursts at once, with room; beyond it an alert is dropped and counted.
const signalHitsBuffer = 512

// signals is the engine-wide wiring of the judge, shared by every ticker of
// every shard through one pointer (tickerState.sig; nil = both signals off, so
// a ticker the engine built with neither on - and every hand-built test
// ticker - pays one nil check per print and per tick).
type signals struct {
	secTypes atomic.Pointer[map[string]string] // for the VWAP cross quality gate
	dildo  bool               // SCANNER_DILDO: classify green dildos (alert 1) and hand them on
	climax bool               // SCANNER_CLIMAX: classify climaxes and step their confirmation latch
	bbs    bool               // SCANNER_BBS: the bouncy ball short judge (bbs.go)
	ign    bool               // SCANNER_IGNITION: the ignition judge (ignition.go)
	emv    bool               // SCANNER_EMAVWAP: the 9EMA VWAP cross judge (emavwap.go)
	emvOff bool               // the judge's INTRABAR (live) fire off: closed-bar judging only (tests of the closed-bar path)
	vb     bool               // SCANNER_VOLBREAK: the volume break judge (volbreak.go)
	hits   chan core.DildoHit // the flagscan hand-off; nil when dildo is off
	drops  atomic.Int64       // hits dropped because the channel was full
	etOff  *atomic.Int64      // the engine's current UTC->ET offset, ms (Engine.etOffMs)

	events  chan core.SignalHit // the signal history hand-off (Options.SignalLog); nil = off
	evDrops atomic.Int64        // alerts dropped because events was full

	// emvBlocked counts, per universe leg (emvLegDv, emvLegRvol, emvLegOther;
	// index leg-1), the intrabar 9EMA VWAP crosses that were geometrically valid
	// but blocked by the universe - once per symbol-minute (emavwap.go).
	emvBlocked [emvLegsCount]atomic.Int64

	// streamMs is the wall clock (ms) of the latest Engine.Seed: ingest re-seeds
	// every ticker right before its stream starts and on every reconnect, and
	// the stream's subscription began before that seed, so a minute that began
	// at or after streamMs was observed whole and one that began before it
	// (the minute the stream started or came back in) was not (judgeBar step 3).
	// 0 until the first seed. Written by Seed, read once per judged bar.
	streamMs atomic.Int64
}

// emit hands one alert to the signal history (Engine.SignalHits) with a
// non-blocking send; a full channel drops it and counts the drop. No-op when
// the hand-off is off. Called once per alert (a bar is judged once), never per
// print: it allocates nothing.
// sigMinPrice: no signal for a stock under this (user, 2026-10-31).
var sigMinPrice = 0.50

func (sg *signals) emit(h core.SignalHit) {
	if sg.events == nil || (h.Price > 0 && h.Price < sigMinPrice) {
		return
	}
	select {
	case sg.events <- h:
	default:
		sg.evDrops.Add(1) // flaglog behind: the alert still stands on the row
	}
}

// streamFrom raises streamMs to ms (it never moves back).
func (sg *signals) streamFrom(ms int64) {
	for {
		cur := sg.streamMs.Load()
		if ms <= cur || sg.streamMs.CompareAndSwap(cur, ms) {
			return
		}
	}
}

// d1Slot is one 1-minute bar being built. m is its clock minute (StartMs /
// 60 000); 0 = empty. oTs / cTs are the StartMs of the prints that set the
// open / the close.
type d1Slot struct {
	m          int32
	o, h, l, c float64
	oTs, cTs   int64
	v          int64
	hodBefore  float64 // the ticker's day high (tickerState.hod) before the print that opened the slot; 0 = none
}

// d1Prior is one judged traded bar in the ring (32 B): what the dildo priors
// (|body|, volume) and the climax priors (high, low, volume) need.
type d1Prior struct {
	absBody, h, l float64
	vol           int64
}

// dildoLatch is the ticker's latest green dildo (alert 1). barMs 0 = none.
// bodyX / volX may be +Inf (a prior mean of 0).
type dildoLatch struct {
	barMs                      int64
	bodyPct, bodyX, volX, dvol float64
	early                      int8 // dildopb.JudgeEarly: 1 early, 0 not, -1 unknown
}

// powerLatch is the ticker's latest power candle. barMs 0 = none.
type powerLatch struct {
	barMs                              int64
	bodyPct, topPct, bodyX, volX, dvol float64
	ps                                 psEval // the power-setup filters and plan at its close (power_setup.go)
}

// dildo1m is the per-ticker judge state (embedded in tickerState as d1).
// About 600 B per ticker, ~7 MB for 11k tickers.
type dildo1m struct {
	bar        [2]d1Slot // bar[0] = the previous minute still inside its grace, bar[1] = the newest minute
	lastJudged int32     // minute of the last judged bar; prints at or before it are dropped

	ring    [judgeRing]d1Prior // the last judgeRing judged traded bars; head = the next write
	n, head int

	day    int64   // ET day number (climax.EtDay) of the last judged bar
	todayN int32   // bars of `day` pushed since the last day change or partial minute
	emaN   int32   // closes folded into the judge EMA (partial minute included); never reset
	emaSum float64 // SMA seed accumulator for the first EmaPeriod closes
	ema    float64 // the judge's 9 EMA after the latest judged bar (valid once emaN >= 9)

	// the early judge's base: the high/low of the last earlyRing judged traded
	// bars (the same bars as ring, a longer window); eHead = the next write.
	eHi, eLo  [earlyRing]float64
	eN, eHead int
	dildoDay  int64 // ET day (climax.EtDay) of the latest green dildo; 0 = none

	// the new-high-of-day rule: dayHi is the highest high of the judged bars of
	// d.day from 04:00 ET; preHod is the ticker's hod before the print being
	// folded (set by applyAgg, copied into a slot when that print opens it).
	dayHi  float64
	preHod float64

	// POWER SETUP inputs (power_setup.go): the ET day's first traded 1m bar from
	// 04:00 ET and from 09:30 ET (their opens; 0 = none), whether the 04:00 one
	// is untrustworthy (the first bar the engine folded was part-observed), and
	// the judge EMA after the three bars before the newest (h[0] newest).
	open, rthOpen float64
	openBad       bool
	emaH          [3]float64

	latch  dildoLatch   // the latest green dildo
	pc     powerLatch   // the latest power candle (a dildo-rule candle with any top wick)
	cl     climax.Latch // the latest volume climax and its confirmation
	clConf climaxConf   // a confirmation the same bar's new climax re-armed over (climax.go)

	bb bbsState        // the bouncy ball short judge (bbs.go)
	ig ignState        // the ignition judge (ignition.go)
	ev emvState        // the 9EMA VWAP cross judge (emavwap.go)
	vb *volbreak.Judge // the volume break judge (volbreak.go); nil until the ticker is first in play
}

// barPrices is an aggregate's OHLC with the missing pieces filled from its
// close (the feed may omit open/high/low on a thin second; startBar's rule).
// ok is false for a print with no usable close.
func barPrices(a core.Agg) (o, h, l, c float64, ok bool) {
	c = a.Close
	if !(c > 0) || math.IsInf(c, 0) {
		return 0, 0, 0, 0, false
	}
	o, h, l = a.Open, a.High, a.Low
	if !(o > 0) {
		o = c
	}
	if !(h > 0) {
		h = c
	}
	if !(l > 0) {
		l = c
	}
	return o, max(h, o, c), min(l, o, c), c, true
}

// accumDildo folds one aggregate into the judge's bar slots. Called from
// applyAgg right after accumC1m, on the hot path: O(1), no allocation. A print
// for the newest or the previous minute merges into its slot; a print for a
// LATER minute opens a new newest slot (judging the previous slot first if it
// is somehow still waiting: the clock-skew path only, the 1 Hz clock normally
// judged it); anything older - a minute already judged, or one older than both
// slots - is dropped.
func (t *tickerState) accumDildo(a core.Agg) {
	sg := t.sig
	if sg == nil {
		return
	}
	ts := a.StartMs
	if ts == 0 {
		ts = a.EndMs - 1
	}
	if ts <= 0 {
		return
	}
	o, h, l, c, ok := barPrices(a)
	if !ok {
		return
	}
	v := a.Vol
	if v < 0 {
		v = 0
	}
	d := &t.d1
	m := int32(ts / m1Ms)
	switch {
	case m <= d.lastJudged:
		return // its bar was judged already: a late print
	case m == d.bar[1].m:
		d.bar[1].merge(ts, o, h, l, c, v)
		if sg.emv {
			t.intrabarEmv(&d.bar[1], a.EndMs, sg.etOff.Load()) // live cross (emavwap.go)
		}
	case m == d.bar[0].m:
		d.bar[0].merge(ts, o, h, l, c, v)
	case m > d.bar[1].m:
		if d.bar[0].m != 0 {
			t.judgeBar(&d.bar[0], sg.etOff.Load()) // clock skew: a third minute before the 1 Hz clock judged the first
		}
		d.bar[0] = d.bar[1]
		d.bar[1] = d1Slot{m: m, o: o, h: h, l: l, c: c, oTs: ts, cTs: ts, v: v, hodBefore: d.preHod}
		if sg.emv {
			t.intrabarEmv(&d.bar[1], a.EndMs, sg.etOff.Load())
		}
	default:
		// Older than the newest slot and not the previous one: a first print for
		// a minute the slots have already moved past. Dropped.
	}
}

// merge folds one print into a slot: the open is the EARLIEST print by StartMs,
// the close the LATEST (ties: the last to arrive, like vol1m), high/low the
// extremes, volume the sum.
func (s *d1Slot) merge(ts int64, o, h, l, c float64, v int64) {
	if ts < s.oTs {
		s.o, s.oTs = o, ts
	}
	if ts >= s.cTs {
		s.c, s.cTs = c, ts
	}
	if h > s.h {
		s.h = h
	}
	if l < s.l {
		s.l = l
	}
	s.v += v
}

// tickDildo judges every slot whose minute ended at least m1Grace ago: the
// previous minute first, then the newest. Called by the shard's 1 Hz clock
// with the wall clock in ms and the engine's UTC->ET offset (loaded once per
// tick by the shard). O(1), no allocation; each slot is judged exactly once.
func (t *tickerState) tickDildo(nowMs, off int64) {
	if t.sig == nil {
		return
	}
	d := &t.d1
	if s := &d.bar[0]; s.m != 0 && nowMs >= (int64(s.m)+1)*m1Ms+m1Grace {
		t.judgeBar(s, off)
	}
	if s := &d.bar[1]; s.m != 0 && nowMs >= (int64(s.m)+1)*m1Ms+m1Grace {
		t.judgeBar(s, off)
	}
}

// judgeBar judges one closed slot and clears it. The order is the spec's:
//  1. a new ET day resets todayN and expires a watching climax (a confirmation
//     never crosses the ET day);
//  2. the close folds into the judge EMA (partial minute included);
//  3. a part-observed minute - one that began before the engine was built
//     (m5ListenMs) or before the latest (re)connect seed (streamMs: the
//     minute the stream started or came back in) - is pushed into the ring (a
//     dildo prior, as vol1m.first treats it) but never judged, and todayN
//     restarts (it is never a climax prior);
//  4. the dildo classify (SCANNER_DILDO);
//  5. the climax: step a watching latch, then classify (SCANNER_CLIMAX);
//  6. push into the ring; todayN++.
func (t *tickerState) judgeBar(s *d1Slot, off int64) {
	sg := t.sig
	d := &t.d1
	m := s.m
	o, h, l, c, v := s.o, s.h, s.l, s.c, s.v
	hodBefore := s.hodBefore
	*s = d1Slot{}
	d.lastJudged = m
	barMs := int64(m) * m1Ms

	// 1. the ET day
	if day := climax.EtDay(barMs, off); day != d.day {
		d.day, d.todayN, d.dayHi = day, 0, 0
		d.open, d.rthOpen, d.openBad = 0, 0, false
		if d.cl.State == climax.StateWatching {
			d.cl.State, d.cl.EndMs = climax.StateExpired, 0
		}
		d.bb.dayReset()
		d.ig.newDay = true
		if d.vb != nil {
			d.vb.NewDay()
		}
	}
	// 2. the judge EMA: SMA seed over the first 9 closes, then c*k + ema*(1-k),
	// the explicit conversions forbidding an FMA (climax/dildopb's emaFold, the
	// chart's arithmetic; on amd64 it is also ticker.ema9's, pinned by a test).
	d.emaN++
	switch {
	case d.emaN < emaPeriod:
		d.emaSum += c
	case d.emaN == emaPeriod:
		d.emaSum += c
		d.ema = d.emaSum / float64(emaPeriod)
	default:
		d.emaH[2], d.emaH[1], d.emaH[0] = d.emaH[1], d.emaH[0], d.ema
		d.ema = float64(c*judgeEmaK) + float64(d.ema*(1-judgeEmaK))
	}
	// 2b. the 9EMA VWAP cross (emavwap.go) judges every 1-minute bar; a
	// part-observed one folds into the EMA but is never judged.
	if sg.emv {
		t.foldEmv(m, c, v, off, barMs < t.m5ListenMs || barMs < sg.streamMs.Load())
	}
	// 3. a part-observed minute
	dayStart := d.day*86_400_000 - off + 4*3_600_000 // 04:00 ET of the bar's ET day
	partial := barMs < t.m5ListenMs || barMs < sg.streamMs.Load()
	d.foldOpen(barMs, dayStart, o, partial)
	if partial {
		d.foldDayHi(barMs, dayStart, h)
		d.push(o, h, l, c, v)
		d.todayN = 0
		d.bb.dayReset() // a gap in what was seen: the base restarts
		if d.vb != nil {
			d.vb.Gap()
		}
		return
	}
	// 3b. ignition (ignition.go): judged on the bars BEFORE this one, like the dildo
	if sg.ign {
		t.judgeIgnition(barMs, o, h, c, v, d.dayHighBefore(barMs, dayStart, hodBefore))
	}
	// 4. green dildo
	if sg.dildo {
		dp := dildopb.Live()
		k := d.n
		if dp.PriorBars < k { // the ring holds judgeRing bars; a shorter yardstick uses the latest ones
			k = dp.PriorBars
		}
		sumAbsBody, sumVol := d.dildoSumsLast(k)
		if hit, ok := dildopb.Classify(o, h, l, c, v, sumAbsBody, sumVol, k, dp); ok && dp.Enabled {
			dayHi := d.dayHighBefore(barMs, dayStart, hodBefore)
			early := d.judgeEarly(c, h, dayHi)
			d.dildoDay = d.day // every dildo counts as a prior for the early rule
			// HOD only: a dildo whose high is not strictly above every earlier high of the
			// ET day (pre-market included; the auto-trader's rule) alerts nothing.
			if !(dayHi > 0 && !(h > dayHi)) {
				d.latch = dildoLatch{barMs: barMs, bodyPct: hit.BodyPct, bodyX: hit.BodyX, volX: hit.VolX, dvol: hit.DollarVol, early: early}
				select {
				case sg.hits <- core.DildoHit{
					Sym: t.sym, BarMs: barMs, O: o, H: h, L: l, C: c, V: v,
					BodyPct: hit.BodyPct, BodyX: hit.BodyX, VolX: hit.VolX, DollarVol: hit.DollarVol,
				}:
				default:
					sg.drops.Add(1) // flagscan behind (or not running): alert 1 still stands on the row
				}
				sg.emit(core.SignalHit{
					Sym: t.sym, Kind: dildopb.KindDildo, BarMs: barMs, Price: c, O: o, C: c,
					BodyPct: hit.BodyPct, BodyX: hit.BodyX, VolX: hit.VolX, DollarVol: hit.DollarVol, Early: early,
				})
			}
		} else if ph, pok := dildopb.ClassifyPower(o, h, l, c, v, sumAbsBody, sumVol, k, dp); pok && dp.Enabled {
			// POWER CANDLE: every dildo rule but the close-near-high one, so any top
			// wick; Classify refused it, so it is never also a dildo. Same HOD-only rule.
			dayHi := d.dayHighBefore(barMs, dayStart, hodBefore)
			if !(dayHi > 0 && !(h > dayHi)) {
				ps := t.evalPowerSetup(barMs, dayStart, c)
				d.pc = powerLatch{barMs: barMs, bodyPct: ph.BodyPct, topPct: ph.TopPct, bodyX: ph.BodyX, volX: ph.VolX, dvol: ph.DollarVol, ps: ps}
				sg.emit(core.SignalHit{
					Sym: t.sym, Kind: dildopb.KindPower, BarMs: barMs, Price: c, O: o, C: c,
					BodyPct: ph.BodyPct, BodyX: ph.BodyX, VolX: ph.VolX, TopPct: ph.TopPct, DollarVol: ph.DollarVol,
					PsFromOpen: ps.fromOpen, PsSlope: ps.slope, PsGap: ps.gap, PsEntry: ps.entry, PsStop: ps.stop, PsTarget: ps.target, PsBaseRange: ps.baseRange, PsSkip: ps.skip,
				})
				if ps.pass {
					sg.emit(core.SignalHit{
						Sym: t.sym, Kind: core.KindPowerSetup, BarMs: barMs, Price: c, O: o, C: c,
						BodyPct: ph.BodyPct, BodyX: ph.BodyX, VolX: ph.VolX, TopPct: ph.TopPct, DollarVol: ph.DollarVol,
						PsFromOpen: ps.fromOpen, PsSlope: ps.slope, PsGap: ps.gap, PsEntry: ps.entry, PsStop: ps.stop, PsTarget: ps.target, PsBaseRange: ps.baseRange, PsAplus: core.PsIsAplus(ps.baseRange),
					})
				}
			}
		}
	}
	// 5. volume climax (climax.go)
	if sg.climax {
		t.judgeClimax(barMs, o, h, l, c, v, off)
	}
	// 5b. bouncy ball short (bbs.go)
	if sg.bbs {
		t.judgeBbs(barMs, h, l, c, v)
	}
	// 5c. volume break (volbreak.go)
	if sg.vb {
		t.judgeVolBreak(barMs, h, l, c, v)
	}
	// 6.
	d.foldDayHi(barMs, dayStart, h)
	d.push(o, h, l, c, v)
	d.todayN++
}

// judgeEmaK is k = 2/(9+1), computed the way climax/dildopb compute it.
var judgeEmaK = 2 / float64(emaPeriod+1)

// push appends one judged traded bar to the ring.
func (d *dildo1m) push(o, h, l, c float64, v int64) {
	d.ring[d.head] = d1Prior{absBody: math.Abs(c - o), h: h, l: l, vol: v}
	if d.head++; d.head == judgeRing {
		d.head = 0
	}
	if d.n < judgeRing {
		d.n++
	}
	d.eHi[d.eHead], d.eLo[d.eHead] = h, l
	if d.eHead++; d.eHead == earlyRing {
		d.eHead = 0
	}
	if d.eN < earlyRing {
		d.eN++
	}
}

// foldDayHi folds a judged bar's high into the day high (bars from 04:00 ET only).
func (d *dildo1m) foldDayHi(barMs, dayStart int64, h float64) {
	if barMs >= dayStart && h > d.dayHi {
		d.dayHi = h
	}
}

// dayHighBefore is the day high before the bar at barMs: the judged bars' dayHi,
// raised by the ticker's hod as it stood when the bar's first print came (it
// covers the day before the engine started watching: the seed). The hod of a
// bar in the 04:00 minute is not trusted: the session reset may not have
// cleared yesterday's yet. 0 = none (the first bar of the day).
func (d *dildo1m) dayHighBefore(barMs, dayStart int64, hodBefore float64) float64 {
	hi := d.dayHi
	if barMs >= dayStart+m1Ms && hodBefore > hi {
		hi = hodBefore
	}
	return hi
}

// judgeEarly is the early verdict (dildopb.JudgeEarly, the trader's rule) of a
// green dildo closing at c with the high h (dayHi: the day high before it) on the current ET day (d.day), judged on the bars
// before it: 1 early, 0 not, -1 unknown (fewer than dildopb.EarlyMinBaseBars
// bars seen: a ticker the engine has only just started watching). Called once
// per dildo, before the bar is pushed; allocates its two small slices only then.
func (d *dildo1m) judgeEarly(c, h, dayHi float64) int8 {
	if d.eN < dildopb.EarlyMinBaseBars {
		return -1
	}
	hs, ls := make([]float64, d.eN), make([]float64, d.eN)
	i := d.eHead - d.eN
	if i < 0 {
		i += earlyRing
	}
	for k := 0; k < d.eN; k++ {
		hs[k], ls[k] = d.eHi[i], d.eLo[i]
		if i++; i == earlyRing {
			i = 0
		}
	}
	prior := d.dildoDay != 0 && d.dildoDay == d.day
	if dildopb.JudgeEarly(hs, ls, c, h, dayHi, prior, earlyParams).Early() {
		return 1
	}
	return 0
}

// dildoSums are the dildo priors' sums over all n ring bars (up to 10 traded
// bars, which may reach back into yesterday as the chart's do), RECOMPUTED
// oldest -> newest: sumAbsBody += |c-o|, sumVol += float64(v) - dildopb's
// priors(), so the bits are the chart's.
func (d *dildo1m) dildoSums() (sumAbsBody, sumVol float64) { return d.dildoSumsLast(d.n) }

// dildoSumsLast is dildoSums over only the latest n ring bars (n <= d.n).
func (d *dildo1m) dildoSumsLast(n int) (sumAbsBody, sumVol float64) {
	i := d.head - n
	if i < 0 {
		i += judgeRing
	}
	for k := 0; k < n; k++ {
		p := &d.ring[i]
		sumAbsBody += p.absBody
		sumVol += float64(p.vol)
		if i++; i == judgeRing {
			i = 0
		}
	}
	return
}

// todayAnchorMs is the latest 04:00 ET at or before nowMs (under offMs): the
// trading day's start, the moment the engine's day state rolls over. A latch
// whose bar started before it belongs to an earlier day and is not shown.
// (Before 04:00 the previous day's anchor is taken 24 h back; on a DST night
// that is an hour off inside 00:00-04:00, where nothing trades.)
func todayAnchorMs(nowMs, offMs int64) int64 {
	a := climax.EtDay(nowMs, offMs)*86_400_000 - offMs + 4*3_600_000
	if nowMs < a {
		a -= 86_400_000
	}
	return a
}

// earlyField is an early verdict as the row's dildoEarly: 1, 0 or Null.
func earlyField(e int8) float64 {
	switch e {
	case 1:
		return 1
	case 0:
		return 0
	}
	return core.Null
}

// finiteOr maps a non-finite measure (a +Inf multiple: a prior mean of 0) to
// Null, so it reaches the wire as null and never as an encoding error.
func finiteOr(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return core.Null
	}
	return v
}

// signalRow stamps the judge's latches onto a row toRow built (toRow leaves
// every signal field Null): the latest green dildo and the latest volume
// climax of TODAY. nowMs is the snapshot's clock (its AsOf), so the ages are
// the same ones the scanner computes for every other overlay age; offMs the
// engine's UTC->ET offset at that snapshot. Runs on the shard goroutine.
func (t *tickerState) signalRow(r *core.Row, nowMs, offMs int64) {
	if t.sig == nil {
		return
	}
	d := &t.d1
	anchor := todayAnchorMs(nowMs, offMs)
	if x := &d.latch; x.barMs > 0 && x.barMs >= anchor {
		r.DildoAge = float64(max(nowMs-x.barMs, 0) / 1000)
		r.DildoBody = finiteOr(x.bodyPct)
		r.DildoVolX = finiteOr(x.volX)
		r.DildoDvol = finiteOr(x.dvol)
		r.DildoEarly = earlyField(x.early)
	}
	if x := &d.pc; x.barMs > 0 && x.barMs >= anchor {
		r.PcAge = float64(max(nowMs-x.barMs, 0) / 1000)
		r.PcBodyPct = finiteOr(x.bodyPct)
		r.PcTopPct = finiteOr(x.topPct)
		r.PcBodyX = finiteOr(x.bodyX)
		r.PcVolX = finiteOr(x.volX)
		r.PcDvol = finiteOr(x.dvol)
		x.ps.stampRow(r, r.PcAge)
	}
	t.climaxRow(r, nowMs, anchor)
	t.bbsRow(r, nowMs, anchor)
	t.ignRow(r, nowMs, anchor)
	t.emvRow(r, nowMs, anchor)
	t.vbRow(r, nowMs, anchor)
}
