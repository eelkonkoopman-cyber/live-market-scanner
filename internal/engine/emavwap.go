package engine

import (
	"math"

	"github.com/eelkonkoopman/marketscanner/internal/climax"
	"github.com/eelkonkoopman/marketscanner/internal/core"
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
// Cost: foldEmv is O(1) once per traded 1-minute bar; nothing allocates
// (BenchmarkEmv).

const (
	emvBars = m1Ms      // a 1-minute bar, ms
	emvBand = 0.0005    // |EMA - VWAP| / VWAP needed beyond the cross, 0.05%
	emvWait = 10 * m1Ms // how long a cross waits for the universe, ms
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

	// the latest judged bar, for the row
	lastMs         int64 // END ms of the latest judged bar; 0 = none
	lastEma, lastV float64
	bullMs, bearMs int64 // END ms of the bar that fired the latest bull / bear alert; 0 = none
	// the volume behind the latest fired cross (either direction)
	crossMs   int64
	crossBv   int64
	crossBdv  float64
	crossSess int64
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
	if e.prevOK {
		if e.prevD <= 0 && d > 0 && e.side != 1 {
			e.pendUp = true
		} else if e.prevD >= 0 && d < 0 && e.side != -1 {
			e.pendDn = true
		}
	}
	e.prevD, e.prevOK = d, true
	barMs := endMs - emvBars
	switch {
	case (e.pendUp || e.waitUp > 0) && d >= emvBand:
		e.pendUp = false
		if !t.emvInUniverse(cl, endMs, off) {
			if e.waitUp == 0 {
				e.waitUp = endMs // keep the cross until the universe passes
			}
			return
		}
		e.waitUp, e.side, e.bullMs = 0, 1, endMs
		e.crossMs, e.crossBv, e.crossBdv, e.crossSess = endMs, e.bv, e.bdv, t.sessVol
		t.emitEmv(core.KindEmaVwapBull, barMs, cl, vwap, d)
	case (e.pendDn || e.waitDn > 0) && d <= -emvBand:
		e.pendDn = false
		if !t.emvInUniverse(cl, endMs, off) {
			if e.waitDn == 0 {
				e.waitDn = endMs
			}
			return
		}
		e.waitDn, e.side, e.bearMs = 0, -1, endMs
		e.crossMs, e.crossBv, e.crossBdv, e.crossSess = endMs, e.bv, e.bdv, t.sessVol
		t.emitEmv(core.KindEmaVwapBear, barMs, cl, vwap, d)
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

// emvInUniverse reports whether the bar just judged (ending endMs) passes the
// scan defaults, its own 1-minute $ volume included.
func (t *tickerState) emvInUniverse(c float64, endMs, off int64) bool {
	e := &t.d1.ev
	r := &t.ref.v
	if !t.ref.ok || !r.avgVolOK || !r.rvolOK || r.rvolBase <= 0 {
		return false
	}
	return c > emvMinPrice && r.avgVol >= emvMinAvgVol &&
		float64(t.sessVol)/r.rvolBase >= emvMinRvol &&
		e.bdv >= emvMinBarDvol && t.sessVol >= emvMinSessVol &&
		emvScope(endMs-1, off)%4 == 2
}

func (t *tickerState) emitEmv(kind string, barMs int64, c, vwap, d float64) {
	e := &t.d1.ev
	t.sig.emit(core.SignalHit{Sym: t.sym, Kind: kind, BarMs: barMs, Price: c, C: c, Ema: e.ema, Vwap: vwap, Dist: d * 100})
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
