package engine

import (
	"math"

	"github.com/eelkonkoopman/marketscanner/internal/climax"
	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

// 9EMA VWAP CROSS, the engine side: the 9 EMA of 1-MINUTE closes crossing the
// session VWAP (the user reads it on a 1-minute chart). An ALERT, a "look now"
// prompt, not a system (uncalibrated). Two alerts: BULL ("SYM: VWAP bull cross")
// when the EMA closes above the VWAP after a bar at or under it, BEAR the reverse
// (signal history kinds "vwapBull" / "vwapBear", labels "VWAP+" / "VWAP−").
//
// Candles: every closed, traded 1-minute bar of the shared closed-bar judge
// (dildo.go judgeBar, step 2b) is judged once, when judgeBar judges it (on the
// next minute's first print, or by the 1 Hz clock at the minute's end +
// m1Grace). A minute with no trade does not exist (the chart skips empty
// candles too).
//
// EMA: SMA of the first 9 closes, then c*k + ema*(1-k), k = 2/10 (the chart's and
// the judge EMA's arithmetic). It is NOT reset at the 04:00 rollover: in a
// process that watched yesterday the first bars of the day continue yesterday's
// EMA; a freshly started engine needs 9 closed 1-minute bars before the EMA, the
// row's evEma and any cross exist, unless SeedScopeVwap (vwapseed.go) rebuilt
// the EMA and the scope's VWAP from today's REST 1-minute bars.
//
// VWAP: the ticker's session VWAP (row sessVwap, scopeVwapNum/Den) read when
// the bar is judged: the PRE-MARKET VWAP of 04:00-09:30, the REGULAR-HOURS VWAP
// from 09:30 (it restarts at the open), the post-market's from 16:00; the VWAP
// of an engine started mid-session covers only the prints it saw until
// SeedScopeVwap seeds it from REST bars (vwapseed.go). A bar that
// ENDS on a session boundary (the 09:29, 15:59 and 03:59 bars) is read while
// the scope is being reset, so it is not judged, and neither is a part-observed
// bar (it began before the engine or the latest stream (re)connect); either one
// also forgets the previous bar, so a cross needs two consecutive judged bars
// inside one VWAP scope.
//
// Cross + hysteresis (Schmitt trigger on d = (EMA-VWAP)/VWAP): a raw cross is
// the previous judged bar at or under the VWAP (d <= 0) and this one above
// (d > 0), or the reverse. It FIRES once d is beyond the 0.05% band on the
// cross side - on the cross bar, or on a later bar if the cross landed inside the
// band and d stayed on that side. A cross that falls back across the VWAP
// before leaving the band is dropped. After a fired cross the same direction is
// disarmed until the OPPOSITE cross has fired: a side that is only seen (no
// cross: the first judged bar of a scope) arms the opposite cross silently.
//
// Universe wait: a cross that leaves the band while the ticker is OUTSIDE the
// history universe (emvInUniverse: price, volume, regular hours) is not
// consumed. It waits and fires on the first later bar that passes the universe
// with d still beyond the band on that side, within emvWait of the bar that
// left the band. It is dropped WITHOUT taking the side (so the next cross can
// still fire) when the wait runs out, d falls back across the VWAP, or the
// scope changes. The row's bull/bear age is stamped from the bar that fires, so
// the scans and the alert agree.
//
// INTRABAR firing (user request 2026-10-29, "fire the moment it crosses"): the
// closed-bar judge above only alerts after the confirming bar closes, so MGM
// (d per closed bar 14:00 -0.058%, 14:01 +0.040% inside the band, 14:02
// +0.157%) alerted at 14:03:03, 2-3 minutes after the lines crossed on the live
// chart, whose EMA includes the FORMING candle. So every print (accumDildo ->
// intrabarEmv) also evaluates liveEma = forming close*k + ema*(1-k) against the
// live scope VWAP, and the FIRST print that takes d across 0 fires at once: no
// band, no debounce beyond once per minute per direction. It needs a warm EMA,
// the previous judged bar to be the latest closed traded bar of the same scope,
// a wholly observed, non-boundary forming minute and a valid VWAP; the
// universe is checked on the forming candle's own numbers (a cross that fails
// it does nothing here, and the closed-bar path with its band and universe
// wait remains the fallback). When the minute closes, finishEmv keeps the side
// if d is still across, or restores the side from before the fire if the cross
// un-did itself (the alert already went out: the accepted trade-off). The
// row's bull/bear age counts from the print that fired (bullMs = now, age 0),
// not from a bar end.
//
// QUALITY GATE (user, 2026-10-30: "most of it is junk"; emvQuality): a cross
// alerts only if the ticker is not an ETF, the time is 09:45 ET or later, and
// the lines did not cross within the previous 30 minutes (ILMN/PEP 10-30 weaved,
// NFLX/LNG crossed cleanly). No 5m trend leg: it blocked both good ones. A blocked cross takes the side and stamps nothing, so the scans stay
// quiet too.
//
// EARLY ALERT (user, 2026-10-30, LNG: in under VWAP before the cross): price
// has already crossed the VWAP, the live EMA has not yet but is within
// emvEarlyBand of it. Same universe and quality gate as the cross, at most once
// per emvQuiet per ticker. Kinds "vwapEarlyUp" / "vwapEarlyDn"; uncalibrated.
//
// Cost: foldEmv is O(1) once per traded 1-minute bar and intrabarEmv O(1) per
// print; nothing allocates (BenchmarkEmv, TestEmvJudgeAllocatesNothing).

const (
	emvBars = m1Ms      // a 1-minute bar, ms
	emvBand = 0.0005    // |EMA - VWAP| / VWAP needed beyond the cross, 0.05%
	emvWait = 10 * m1Ms // how long a cross waits for the universe, ms

	emvFirstMod  = 585       // no alert before 09:45 ET (minute of the day)
	emvQuiet     = 30 * m1Ms // no alert within this long of the previous cross
	emvEarlyBand = 0.002     // early alert: |EMA - VWAP| / VWAP at most 0.2%
)

var emvK = 2 / float64(emaPeriod+1)

// emvState is a ticker's judge state (dildo1m.ev).
type emvState struct {
	bv  int64   // the judged bar's shares
	bdv float64 // its dollar volume (close x volume)

	seedEnd int64 // END ms of the last REST bar SeedScopeVwap folded; bars ending by then are skipped

	emaN int     // closed bars folded into the EMA (never reset)
	sum  float64 // SMA seed accumulator for the first emaPeriod closes
	ema  float64 // the EMA after the latest closed bar (valid once emaN >= emaPeriod)

	scope  int64   // VWAP scope (ET day*4 + overnight/pre/regular/post) of the previous judged bar
	prevOK bool    // prevD is a judged bar of `scope`
	prevD  float64 // (EMA-VWAP)/VWAP of the previous judged bar
	side   int8    // last confirmed side: +1 EMA above, -1 below, 0 unknown
	pendUp bool    // a bull cross happened and has not left the band yet
	pendDn bool
	waitUp int64 // END ms of the bar where a bull cross left the band outside the universe; 0 = none
	waitDn int64
	// START ms of the bar where the pending / waiting bull (bear) cross actually
	// happened (the EMA crossed the VWAP). The alert fires later, once d leaves
	// the band (and the universe passes), but the signal is STAMPED with this
	// cross bar - otherwise a cross that landed inside the band showed one
	// minute late in the history and on the chart (user, 2026-10-29: "KLAC.
	// 12:59 signal. Cross at 12:58").
	crossUpBar, crossDnBar int64

	// the latest judged bar, for the row
	lastMs         int64 // END ms of the latest judged bar; 0 = none
	lastEma, lastV float64
	bullMs, bearMs int64 // END ms of the bar that fired the latest bull / bear alert (the firing print's ms for an intrabar fire); 0 = none
	// the volume behind the latest fired cross (either direction)
	crossMs   int64
	crossBv   int64
	crossBdv  float64
	crossSess int64

	// intrabar fire (intrabarEmv): the minute (epoch minute) and direction
	// (+1 bull, -1 bear) fired mid-candle, and the side before it fired, to be
	// restored if the closed bar falls back across the VWAP.
	intraMinute int32
	intraDir    int8
	sideBefore  int8

	// rvolOKScope is the VWAP scope (regular hours) in which sessVol/rvolBase
	// last reached emvMinRvol; the RVOL leg then stays passed for the rest of
	// that scope (see emvRvolPass). 0 = never; a new scope never matches.
	rvolOKScope int64
	// blockMinute is the epoch minute whose blocked intrabar cross was last
	// counted and logged (emvBlockNote): once per symbol-minute.
	blockMinute int32

	// the quality gate
	earlyMs                  int64 // the latest early alert
	lastCrossMs, prevCrossMs int64 // END ms of the latest closed-bar crosses of d through 0
}

// foldEmv judges one closed 1-minute bar (epoch minute m, close c, shares v).
// partial: the minute was not wholly observed.
func (t *tickerState) foldEmv(m int32, c float64, v int64, off int64, partial bool) {
	e := &t.d1.ev
	if (int64(m)+1)*emvBars <= e.seedEnd {
		return // already folded by SeedScopeVwap (vwapseed.go)
	}
	e.bv, e.bdv = v, c*float64(v)
	t.finishEmv(int64(m), c, partial, off)
}

// emvScope is the VWAP scope of the instant ms: ET day*4 + 0 overnight (before
// 04:00), 1 pre-market (04:00-09:30), 2 regular hours (09:30-16:00), 3 post-market.
func emvScope(ms, off int64) int64 {
	local := ms + off
	day := climax.EtDay(ms, off)
	mod := (local - day*86_400_000) / m1Ms
	switch {
	case mod < 240:
		return day * 4
	case mod < 570:
		return day*4 + 1
	case mod < 960:
		return day*4 + 2
	}
	return day*4 + 3
}

// finishEmv folds the bar's close into the EMA, reads the VWAP and judges the cross.
func (t *tickerState) finishEmv(m int64, cl float64, partial bool, off int64) {
	e := &t.d1.ev
	endMs := (m + 1) * emvBars
	intra := e.intraMinute != 0 && int64(e.intraMinute) == m
	intraDir := e.intraDir
	if intra {
		e.intraMinute, e.intraDir = 0, 0
	}
	e.emaN++
	switch {
	case e.emaN < emaPeriod:
		e.sum += cl
		return
	case e.emaN == emaPeriod:
		e.sum += cl
		e.ema = e.sum / float64(emaPeriod)
	default:
		e.ema = float64(cl*emvK) + float64(e.ema*(1-emvK))
	}
	// the VWAP: valid only inside one scope, never read while a boundary resets it
	vwap := 0.0
	if t.scopeVwapDen > 0 {
		vwap = t.scopeVwapNum / t.scopeVwapDen
	}
	sc := emvScope(endMs-1, off)
	if sc != e.scope {
		e.scope, e.prevOK, e.side, e.pendUp, e.pendDn = sc, false, 0, false, false
		e.waitUp, e.waitDn = 0, 0
	}
	if emvScope(endMs, off) != sc || partial || !(vwap > 0) || math.IsInf(vwap, 0) {
		e.prevOK, e.pendUp, e.pendDn = false, false, false // a boundary bar, a gap, no VWAP
		e.waitUp, e.waitDn = 0, 0
		return
	}
	d := (e.ema - vwap) / vwap
	e.lastMs, e.lastEma, e.lastV = endMs, e.ema, vwap
	t.emvNoteRvol(endMs, off)
	if intra && ((intraDir > 0 && d <= 0) || (intraDir < 0 && d >= 0)) {
		e.side = e.sideBefore // the intrabar cross un-did itself: a later one can fire again
	}

	if d >= 0 {
		e.pendDn, e.waitDn = false, 0
	}
	if d <= 0 {
		e.pendUp, e.waitUp = false, 0
	}
	if e.waitUp > 0 && endMs-e.waitUp > emvWait {
		e.waitUp = 0 // waited too long: dropped, the side stays unconsumed
	}
	if e.waitDn > 0 && endMs-e.waitDn > emvWait {
		e.waitDn = 0
	}
	if e.prevOK && e.prevD*d < 0 {
		e.prevCrossMs, e.lastCrossMs = e.lastCrossMs, endMs
	}
	if e.prevOK {
		if e.prevD <= 0 && d > 0 && e.side != 1 {
			e.pendUp, e.crossUpBar = true, endMs-emvBars
		} else if e.prevD >= 0 && d < 0 && e.side != -1 {
			e.pendDn, e.crossDnBar = true, endMs-emvBars
		}
	}
	e.prevD, e.prevOK = d, true
	barMs := endMs - emvBars
	switch {
	case (e.pendUp || e.waitUp > 0) && d >= emvBand:
		// Stamp: the actual cross bar when the cross was only waiting to leave
		// the band; the firing bar when it waited for the universe (that alert
		// is "it qualifies now", and back-dating it would make it look stale).
		upStamp := barMs
		if e.waitUp == 0 {
			upStamp = crossBarOr(e.crossUpBar, barMs)
		}
		e.pendUp = false
		if !t.emvInUniverse(cl, endMs, off) {
			if e.waitUp == 0 {
				e.waitUp = endMs // keep the cross until the universe passes
			}
			return
		}
		if !t.emvQuality(1, endMs, off) {
			e.waitUp, e.side = 0, 1
			return
		}
		e.waitUp, e.side, e.bullMs = 0, 1, endMs
		e.crossMs, e.crossBv, e.crossBdv, e.crossSess = endMs, e.bv, e.bdv, t.sessVol
		t.emitEmv(core.KindEmaVwapBull, upStamp, cl, vwap, d)
	case (e.pendDn || e.waitDn > 0) && d <= -emvBand:
		dnStamp := barMs // see upStamp
		if e.waitDn == 0 {
			dnStamp = crossBarOr(e.crossDnBar, barMs)
		}
		e.pendDn = false
		if !t.emvInUniverse(cl, endMs, off) {
			if e.waitDn == 0 {
				e.waitDn = endMs
			}
			return
		}
		if !t.emvQuality(-1, endMs, off) {
			e.waitDn, e.side = 0, -1
			return
		}
		e.waitDn, e.side, e.bearMs = 0, -1, endMs
		e.crossMs, e.crossBv, e.crossBdv, e.crossSess = endMs, e.bv, e.bdv, t.sessVol
		t.emitEmv(core.KindEmaVwapBear, dnStamp, cl, vwap, d)
	case e.side == 0 && !e.pendUp && !e.pendDn && e.waitUp == 0 && e.waitDn == 0:
		// a side seen without a cross (the first judged bar of a scope): the
		// opposite cross is armed, nothing fires
		if d >= emvBand {
			e.side = 1
		} else if d <= -emvBand {
			e.side = -1
		}
	}
}

// Signal universe: the same conditions as the seeded vwap-bull / vwap-bear
// scans' defaults (cmd/scanner emaVwapBaseConds + VolFloorConds). The engine
// cannot see the scans' live (user-edited) filters, so the defaults are applied
// here; the scans themselves still honour edits. A cross outside the universe
// waits (see "Universe wait") and stamps the row only when it fires.
const (
	emvMinPrice   = 25.0
	emvMinAvgVol  = 1_000_000
	emvMinRvol    = 1.5
	emvMinBarDvol = 500_000 // the 1-minute bar's $ volume
	emvMinSessVol = 500_000
)

// The universe legs, for the blocked-cross diagnostics (emvUniverseLeg).
const (
	emvPass      = 0 // in the universe
	emvLegDv     = 1 // the 1-minute bar's $ volume
	emvLegRvol   = 2 // sessVol / rvolBase
	emvLegOther  = 3 // price, average volume, session volume, regular hours, no reference data
	emvLegsCount = 3
)

// emvInUniverse reports whether the bar just judged (ending endMs) passes the
// scan defaults, its own 1-minute $ volume included.
func (t *tickerState) emvInUniverse(c float64, endMs, off int64) bool {
	return t.emvInUniverseDv(c, t.d1.ev.bdv, endMs, off)
}

// emvInUniverseDv is emvInUniverse with the bar's dollar volume given.
func (t *tickerState) emvInUniverseDv(c, barDvol float64, endMs, off int64) bool {
	return t.emvUniverseLeg(c, barDvol, endMs, off) == emvPass
}

// emvRvolPass is the RVOL leg, STICKY inside a regular-hours scope: once
// sessVol/rvolBase has reached emvMinRvol today (checked at every universe
// look and once per judged bar, emvNoteRvol) the ticker keeps passing it for
// the rest of that scope. The ratio sags as the baseline grows through the
// session while a stock that was in play stays in play (ECHO, 2026-10-29: the
// ratio sat on the threshold and a real cross waited for more volume). The
// next scope (tomorrow) is judged afresh. sc is the scope of the bar judged.
func (t *tickerState) emvRvolPass(base float64, sc int64) bool {
	e := &t.d1.ev
	if float64(t.sessVol)/base >= emvMinRvol {
		if sc%4 == 2 {
			e.rvolOKScope = sc
		}
		return true
	}
	return sc%4 == 2 && e.rvolOKScope == sc
}

// emvNoteRvol records the sticky RVOL pass for the bar ending endMs, so a dip
// below the threshold between two crosses does not forget an earlier pass.
func (t *tickerState) emvNoteRvol(endMs, off int64) {
	r := &t.ref.v
	if !t.ref.ok || !r.avgVolOK || !r.rvolOK || r.rvolBase <= 0 {
		return
	}
	t.emvRvolPass(r.rvolBase, emvScope(endMs-1, off))
}

// emvUniverseLeg names the leg that keeps the bar out of the universe (the
// first of: other, RVOL, $ volume), emvPass when it is in.
func (t *tickerState) emvUniverseLeg(c, barDvol float64, endMs, off int64) int {
	r := &t.ref.v
	if !t.ref.ok || !r.avgVolOK || !r.rvolOK || r.rvolBase <= 0 {
		return emvLegOther
	}
	sc := emvScope(endMs-1, off)
	if !(c > emvMinPrice && r.avgVol >= emvMinAvgVol && t.sessVol >= emvMinSessVol && sc%4 == 2) {
		return emvLegOther
	}
	if !t.emvRvolPass(r.rvolBase, sc) {
		return emvLegRvol
	}
	if !(barDvol >= emvMinBarDvol) {
		return emvLegDv
	}
	return emvPass
}

// emvBlockNote reports whether a blocked intrabar cross in epoch minute m is
// the first of its symbol-minute (and so is to be counted and logged).
func (e *emvState) emvBlockNote(m int32) bool {
	if e.blockMinute == m {
		return false
	}
	e.blockMinute = m
	return true
}

// emvBlocked counts and logs (Debug) a geometrically valid intrabar cross the
// universe blocked, once per symbol-minute; only the blocked branch pays.
func (t *tickerState) emvBlocked(leg int, s *d1Slot, dv float64) {
	if !t.d1.ev.emvBlockNote(s.m) {
		return
	}
	t.sig.emvBlocked[leg-1].Add(1)
	r := &t.ref.v
	logx.L().Debug("emavwap: intrabar cross blocked by the universe",
		"sym", t.sym, "leg", emvLegName[leg], "dv", dv, "sessVol", t.sessVol,
		"rvolBase", r.rvolBase, "avgVol", r.avgVol, "price", s.c)
}

var emvLegName = [emvLegsCount + 1]string{"", "dv", "rvol", "other"}

// emvIntrabarDv is the bar $ volume the intrabar universe judges: the larger
// of the forming candle's so far and the PREVIOUS minute's (the judged bar
// ending exactly where this one starts). Liquidity is judged from the recent
// minute, not from the first seconds of the forming one: a cross detected 3 s
// into the minute must not wait ~a minute for the candle to build $500k
// (MGA, 2026-10-29: $416k in the minute before, 54 s of waiting).
func (e *emvState) emvIntrabarDv(s *d1Slot, slotMs int64) float64 {
	dv := s.c * float64(s.v)
	if e.lastMs == slotMs && e.bdv > dv {
		dv = e.bdv
	}
	return dv
}

// intrabarEmv is the live cross check (see "INTRABAR firing" above), run on
// every print merged into the NEWEST 1-minute slot. nowMs is the print's EndMs.
// O(1), no allocation; returns without touching state unless it fires.
func (t *tickerState) intrabarEmv(s *d1Slot, nowMs, off int64) {
	e := &t.d1.ev
	if e.emaN < emaPeriod || !e.prevOK || t.sig.emvOff {
		return
	}
	d := &t.d1
	if o := &d.bar[0]; o.m != 0 && o.m != s.m {
		return // an older closed minute is still unjudged: the EMA lacks it
	}
	slotMs := int64(s.m) * m1Ms
	if e.lastMs > slotMs || slotMs < t.m5ListenMs || slotMs < t.sig.streamMs.Load() {
		return
	}
	if sc := emvScope(slotMs, off); sc != e.scope || emvScope(slotMs+m1Ms, off) != sc {
		return
	}
	if !(t.scopeVwapDen > 0) {
		return
	}
	vwap := t.scopeVwapNum / t.scopeVwapDen
	if !(vwap > 0) || math.IsInf(vwap, 0) {
		return
	}
	liveD := (float64(s.c*emvK) + float64(e.ema*(1-emvK)) - vwap) / vwap
	if dir := emvEarlyDir(e.prevD, liveD, float64(s.c), vwap); dir != 0 && !emvGateOff && (e.earlyMs == 0 || nowMs-e.earlyMs >= emvQuiet) &&
		t.emvQuality(dir, nowMs, off) && t.emvUniverseLeg(s.c, e.emvIntrabarDv(s, slotMs), slotMs+m1Ms, off) == emvPass {
		e.earlyMs = nowMs
		kind := core.KindEmaVwapEarlyUp
		if dir < 0 {
			kind = core.KindEmaVwapEarlyDn
		}
		t.emitEmv(kind, slotMs, s.c, vwap, liveD)
	}
	switch {
	case e.prevD <= 0 && liveD > 0 && e.side != 1:
		if e.intraMinute == s.m && e.intraDir == 1 {
			return
		}
		dv := s.c * float64(s.v)
		if leg := t.emvUniverseLeg(s.c, e.emvIntrabarDv(s, slotMs), slotMs+m1Ms, off); leg != emvPass {
			t.emvBlocked(leg, s, e.emvIntrabarDv(s, slotMs))
			return
		}
		if !t.emvQuality(1, nowMs, off) {
			return // the closed-bar path takes the side
		}
		e.sideBefore, e.side = e.side, 1
		e.pendUp, e.waitUp = false, 0
		e.bullMs = nowMs // age 0 at the alert (bullMs is a bar END elsewhere)
		e.crossMs, e.crossBv, e.crossBdv, e.crossSess = nowMs, s.v, dv, t.sessVol
		e.intraMinute, e.intraDir = s.m, 1
		t.emitEmv(core.KindEmaVwapBull, slotMs, s.c, vwap, liveD)
	case e.prevD >= 0 && liveD < 0 && e.side != -1:
		if e.intraMinute == s.m && e.intraDir == -1 {
			return
		}
		dv := s.c * float64(s.v)
		if leg := t.emvUniverseLeg(s.c, e.emvIntrabarDv(s, slotMs), slotMs+m1Ms, off); leg != emvPass {
			t.emvBlocked(leg, s, e.emvIntrabarDv(s, slotMs))
			return
		}
		if !t.emvQuality(-1, nowMs, off) {
			return
		}
		e.sideBefore, e.side = e.side, -1
		e.pendDn, e.waitDn = false, 0
		e.bearMs = nowMs
		e.crossMs, e.crossBv, e.crossBdv, e.crossSess = nowMs, s.v, dv, t.sessVol
		e.intraMinute, e.intraDir = s.m, -1
		t.emitEmv(core.KindEmaVwapBear, slotMs, s.c, vwap, liveD)
	}
}

// emvEarlyDir: +1 price above the VWAP with the EMA still just under it, -1 the
// reverse, 0 otherwise.
func emvEarlyDir(prevD, liveD, c, vwap float64) int8 {
	switch {
	case prevD < 0 && liveD < 0 && liveD >= -emvEarlyBand && c > vwap:
		return 1
	case prevD > 0 && liveD > 0 && liveD <= emvEarlyBand && c < vwap:
		return -1
	}
	return 0
}

// emvGateOff: tests of the raw cross logic and the golden run set it.
var emvGateOff bool

// emvIsETF: the ticker reference says ETF (unknown passes).
func (t *tickerState) emvIsETF() bool {
	m := t.sig.secTypes.Load()
	return m != nil && (*m)[t.sym] == "ETF"
}

// emvQuality is the quality gate (see the header).
func (t *tickerState) emvQuality(dir int8, ms, off int64) bool {
	e := &t.d1.ev
	if emvGateOff {
		return true
	}
	if t.emvIsETF() {
		return false
	}
	day := climax.EtDay(ms, off)
	if (ms+off-day*86_400_000)/m1Ms < emvFirstMod {
		return false
	}
	last := e.lastCrossMs
	if last == ms { // the closed bar being judged is this cross itself
		last = e.prevCrossMs
	}
	return last == 0 || ms-last >= emvQuiet
}

func (t *tickerState) emitEmv(kind string, barMs int64, c, vwap, d float64) {
	e := &t.d1.ev
	t.sig.emit(core.SignalHit{Sym: t.sym, Kind: kind, BarMs: barMs, Price: c, C: c, Ema: e.ema, Vwap: vwap, Dist: d * 100})
}

// crossBarOr is the bar a fired cross is stamped with: the bar where the EMA
// actually crossed the VWAP when it is known and not after the firing bar,
// else the firing bar itself.
func crossBarOr(cross, fire int64) int64 {
	if cross > 0 && cross <= fire {
		return cross
	}
	return fire
}

// emvRow stamps today's crosses and the latest judged bar onto a row.
func (t *tickerState) emvRow(r *core.Row, nowMs, anchor int64) {
	e := &t.d1.ev
	if ms := e.bullMs; ms > 0 && ms >= anchor {
		r.EvBullAge = float64(max(nowMs-ms, 0) / 1000)
	}
	if ms := e.bearMs; ms > 0 && ms >= anchor {
		r.EvBearAge = float64(max(nowMs-ms, 0) / 1000)
	}
	if ms := e.crossMs; ms > 0 && ms >= anchor {
		r.EvBarVol = float64(e.crossBv)
		r.EvBarDvol = e.crossBdv
		r.EvSessVol = float64(e.crossSess)
	}
	if e.lastMs > 0 && e.lastMs >= anchor {
		r.EvEma = e.lastEma
		r.EvVwap = e.lastV
		r.EvDist = (e.lastEma - e.lastV) / e.lastV * 100
	}
}
