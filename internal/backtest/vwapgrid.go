package backtest

import (
	"math"
	"sort"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// The pre-registered rule grid for the vwapcross strategy. GridVersion and the
// list in VwapGrid are fixed BEFORE the results are looked at: do not edit a
// variant after a run, add a new grid version instead.
//
// The simulation is parametric: a VwapRules value says which signal, which
// filters, how the entry/stop are set and how the exit works. The baseline B0
// reproduces SimulateVwapCrossDay exactly (pinned by a test).
//
// APPROXIMATIONS specific to the grid (also printed in grid.md):
//   - a filter that fails (F1..F4) behaves like a failed universe test: it does
//     not consume the side, so a later bar can still fire.
//   - fixed take-profit fills AT the TP price (a gap through it is not credited
//     with the better open); a bar that touches both stop and TP is a stop.
//   - the fill bar of a limit entry may also hit the stop: assumed filled first,
//     then stopped (conservative).
//   - exit reasons: "stop" = the initial stop was hit, "trail" = a trailed
//     (raised) stop was hit, "tp", "eod".

// GridVersion names the grid below.
const GridVersion = "grid v1"

// Entry kinds.
const (
	EntryNextOpen = "next-open"
	EntryLimit    = "limit-at-cross-close"
)

// Stop kinds.
const (
	StopCrossLow = "cross-low"
	StopWide     = "min(cross-low, vwap-0.3%)"
	StopATR      = "entry-1.5*ATR14"
	StopLimitLow = "cross-low-0.01"

	vwWideFrac    = 0.003
	vwATRMult     = 1.5
	vwATRPeriod   = 14
	vwEmaTrailGap = 0.0005
)

// Exit kinds.
const (
	ExitTrailPrev  = "trail-prev-low"
	ExitTrail2     = "trail-2-candle-low"
	ExitTrailEMA   = "trail-ema9-0.05%"
	ExitTPPct      = "tp-pct-fixed-stop"
	ExitTPR        = "tp-R-fixed-stop"
	ExitTrailAfter = "trail-prev-low-after-1R"
)

// VwapRules is one variant of the grid.
type VwapRules struct {
	ID     string
	Desc   string
	Signal string // VwIntrabar | VwClose
	Entry  string
	Stop   string
	Exit   string
	TPPct  float64 // ExitTPPct: 0.01 = +1% of the fill
	TPR    float64 // ExitTPR: multiple of the initial risk

	LimitBars int // EntryLimit: bars after the cross bar in which the limit may fill

	FromMin    int     // F1: the entry bar must start at or after this ET minute (0 = off)
	MinVol1m   float64 // F2: cross-bar volume >= this x the mean volume of the 9 bars before it (0 = off)
	VwapRising bool    // F3
	Trend5m    bool    // F4
}

// VwapGrid returns grid v1 in the fixed order of the registration.
func VwapGrid() []VwapRules {
	b := func(id, desc string) VwapRules {
		return VwapRules{ID: id, Desc: desc, Signal: VwIntrabar, Entry: EntryNextOpen, Stop: StopCrossLow, Exit: ExitTrailPrev}
	}
	with := func(r VwapRules, f func(*VwapRules)) VwapRules { f(&r); return r }
	u := func(id, desc, sig string) VwapRules {
		return VwapRules{ID: id, Desc: desc, Signal: sig, Entry: EntryLimit, Stop: StopLimitLow, Exit: ExitTPPct, TPPct: 0.01, LimitBars: 3}
	}
	return []VwapRules{
		b("B0", "baseline: entry next open; stop cross low; trail prev-candle low"),
		with(b("X1", "exit: trail to the lower of the last 2 candle lows"), func(r *VwapRules) { r.Exit = ExitTrail2 }),
		with(b("X2", "exit: trail to EMA9(close) - 0.05%"), func(r *VwapRules) { r.Exit = ExitTrailEMA }),
		with(b("X3", "exit: fixed +1% take-profit, fixed stop"), func(r *VwapRules) { r.Exit, r.TPPct = ExitTPPct, 0.01 }),
		with(b("X4", "exit: fixed 1.5R take-profit, fixed stop"), func(r *VwapRules) { r.Exit, r.TPR = ExitTPR, 1.5 }),
		with(b("X5", "exit: fixed 2R take-profit, fixed stop"), func(r *VwapRules) { r.Exit, r.TPR = ExitTPR, 2 }),
		with(b("X6", "exit: fixed stop until +1R reached, then trail prev-candle low"), func(r *VwapRules) { r.Exit = ExitTrailAfter }),
		with(b("S1", "stop: min(cross low, VWAP at cross x 0.997)"), func(r *VwapRules) { r.Stop = StopWide }),
		with(b("S2", "stop: entry - 1.5 x ATR14 (1-minute, Wilder)"), func(r *VwapRules) { r.Stop = StopATR }),
		with(b("S3", "S1 stop + X2 EMA trail"), func(r *VwapRules) { r.Stop, r.Exit = StopWide, ExitTrailEMA }),
		with(b("S4", "S2 stop + X4 1.5R take-profit"), func(r *VwapRules) { r.Stop, r.Exit, r.TPR = StopATR, ExitTPR, 1.5 }),
		with(b("F1", "filter: entries from 10:00 only"), func(r *VwapRules) { r.FromMin = 10 * 60 }),
		with(b("F2", "filter: cross-bar volume >= 2x mean of the 9 bars before"), func(r *VwapRules) { r.MinVol1m = 2 }),
		with(b("F3", "filter: VWAP rising (VWAP > VWAP 10 bars earlier)"), func(r *VwapRules) { r.VwapRising = true }),
		with(b("F4", "filter: last completed 5m candle closes above its 5m EMA9"), func(r *VwapRules) { r.Trend5m = true }),
		with(b("F5", "F1 + F2 + S1 stop + X2 trail"), func(r *VwapRules) {
			r.FromMin, r.MinVol1m, r.Stop, r.Exit = 10*60, 2, StopWide, ExitTrailEMA
		}),
		u("U1", "VWAP X order: limit at the cross close (close signal), 3-bar window, stop cross low-0.01, TP +1%", VwClose),
		u("U1i", "U1 rules on the intrabar signal", VwIntrabar),
		with(u("U2", "U1 + F1 + F2", VwClose), func(r *VwapRules) { r.FromMin, r.MinVol1m = 10*60, 2 }),
	}
}

// atrSeries is the Wilder ATR of the 1-minute bars (NaN until `period` bars).
func atrSeries(bars []core.Bar, period int) []float64 {
	out := make([]float64, len(bars))
	for i := range out {
		out[i] = math.NaN()
	}
	var prev, sum float64
	for i, b := range bars {
		tr := b.H - b.L
		if i > 0 {
			pc := bars[i-1].C
			tr = math.Max(tr, math.Max(math.Abs(b.H-pc), math.Abs(b.L-pc)))
		}
		switch {
		case i < period-1:
			sum += tr
		case i == period-1:
			sum += tr
			prev = sum / float64(period)
			out[i] = prev
		default:
			prev = (prev*float64(period-1) + tr) / float64(period)
			out[i] = prev
		}
	}
	return out
}

type candle5 struct {
	end   int64 // ms: start of the next bucket
	close float64
}

// candles5 builds clock-aligned 5-minute closes from the 1-minute bars plus the
// 9 EMA of those closes.
func candles5(bars []core.Bar) ([]candle5, []float64) {
	const w = 5 * 60_000
	var cs []candle5
	for _, b := range bars {
		bk := b.T / w
		if n := len(cs); n > 0 && cs[n-1].end == (bk+1)*w {
			cs[n-1].close = b.C
			continue
		}
		cs = append(cs, candle5{end: (bk + 1) * w, close: b.C})
	}
	cl := make([]float64, len(cs))
	for i, c := range cs {
		cl[i] = c.close
	}
	return cs, emaSeries(cl, 9)
}

// trend5mOK: the last 5m candle completed at signalMs closes above its 5m EMA9.
func trend5mOK(cs []candle5, ema []float64, signalMs int64) bool {
	i := sort.Search(len(cs), func(i int) bool { return cs[i].end > signalMs }) - 1
	if i < 0 || math.IsNaN(ema[i]) {
		return false
	}
	return cs[i].close > ema[i]
}

// SimulateVwapRulesDay is SimulateVwapCrossDay with the rules of r. bars are the
// previous day + the day, ascending, 1-minute, extended hours.
func SimulateVwapRulesDay(sym, date string, adv10 float64, bars []core.Bar, r VwapRules, p VwapParams) ([]VwTrade, VwCounts) {
	cnt := VwCounts{SymbolDays: 1}
	if len(bars) == 0 {
		return nil, cnt
	}
	ser := VwapSeries(bars, date, p.EmaPeriod)
	var atr []float64
	if r.Stop == StopATR {
		atr = atrSeries(bars, vwATRPeriod)
	}
	var cs []candle5
	var cema []float64
	if r.Trend5m {
		cs, cema = candles5(bars)
	}
	reject := func(why string) {
		if cnt.UniverseReject == nil {
			cnt.UniverseReject = map[string]int{}
		}
		cnt.UniverseReject[why]++
	}
	var out []VwTrade
	var (
		have      bool
		prevD     float64
		armed     = true
		firedIdx  = -1
		cumVol    float64
		openUntil = -1
	)
	for i, b := range bars {
		if ETDate(b.T) != date {
			continue
		}
		min := ETMinute(b.T)
		if min >= vwPreOpen {
			cumVol += float64(b.V)
		}
		if min < vwSessionOpen || min >= vwSessionClose {
			continue
		}
		ema, vwap := ser.Ema[i], ser.Vwap[i]
		if math.IsNaN(ema) || math.IsNaN(vwap) || !(vwap > 0) {
			continue
		}
		d := (ema - vwap) / vwap
		if !have {
			have, prevD = true, d
			continue
		}
		geom := false
		if armed && prevD <= 0 && i > 0 && !math.IsNaN(ser.Ema[i-1]) {
			switch r.Signal {
			case VwIntrabar:
				geom = b.H*p.K+ser.Ema[i-1]*(1-p.K) > vwap
			default:
				geom = d > 0
			}
		}
		if geom {
			barDvol := b.C * float64(b.V)
			rvol := 0.0
			if adv10 > 0 {
				rvol = cumVol / (adv10 * CurveFrac(float64(min+1)))
			}
			why := VwUniverseReject(p, b.C, adv10, barDvol, cumVol, rvol)
			if why == "" {
				why = vwFilterReject(r, bars, ser, i, min, cs, cema)
			}
			if why != "" {
				reject(why)
			} else {
				cnt.Fired++
				armed, firedIdx = false, i
				if i <= openUntil {
					cnt.IgnoredOpen++
				} else if tr, ok := vwapRulesTrade(bars, i, sym, date, r, d, barDvol, rvol, p, ser, atr, &cnt); ok {
					openUntil = tr.exitBar
					out = append(out, tr)
				}
			}
		}
		if !armed && i > firedIdx && d < 0 {
			armed = true
		}
		prevD = d
	}
	return out, cnt
}

// vwFilterReject names the first failing filter ("" = pass).
func vwFilterReject(r VwapRules, bars []core.Bar, ser VwapDaySeries, i, min int, cs []candle5, cema []float64) string {
	if r.FromMin > 0 && min+1 < r.FromMin { // the entry bar (next minute) must start at or after FromMin
		return "f1_time"
	}
	if r.MinVol1m > 0 {
		if i < 9 {
			return "f2_volume"
		}
		var s float64
		for j := i - 9; j < i; j++ {
			s += float64(bars[j].V)
		}
		if !(float64(bars[i].V) >= r.MinVol1m*s/9) {
			return "f2_volume"
		}
	}
	if r.VwapRising {
		if i < 10 || math.IsNaN(ser.Vwap[i-10]) || !(ser.Vwap[i] > ser.Vwap[i-10]) {
			return "f3_vwapRising"
		}
	}
	if r.Trend5m {
		sig := bars[i].T // an intrabar poke happens inside the bar: only candles complete by its start count
		if r.Signal == VwClose {
			sig += 60_000
		}
		if !trend5mOK(cs, cema, sig) {
			return "f4_trend5m"
		}
	}
	return ""
}

// vwapRulesTrade resolves the entry, stop and exit of one fired signal at bar m.
func vwapRulesTrade(bars []core.Bar, m int, sym, date string, r VwapRules, d, barDvol, rvol float64, p VwapParams, ser VwapDaySeries, atr []float64, cnt *VwCounts) (VwTrade, bool) {
	var e int
	var entry float64
	if r.Entry == EntryLimit {
		limit := bars[m].C
		e = -1
		for k := m + 1; k <= m+r.LimitBars && k < len(bars) && ETDate(bars[k].T) == date && ETMinute(bars[k].T) < p.LastEntryMin; k++ {
			if bars[k].L <= limit {
				e, entry = k, math.Min(limit, bars[k].O)
				break
			}
		}
		if e < 0 {
			cnt.Unfilled++
			return VwTrade{}, false
		}
	} else {
		e = m + 1
		if e >= len(bars) || ETDate(bars[e].T) != date || ETMinute(bars[e].T) >= p.LastEntryMin {
			cnt.NoEntryBar++
			return VwTrade{}, false
		}
		entry = bars[e].O
	}
	var stop0 float64
	switch r.Stop {
	case StopWide:
		stop0 = math.Min(bars[m].L, ser.Vwap[m]*(1-vwWideFrac))
	case StopATR:
		if m >= len(atr) || math.IsNaN(atr[m]) {
			cnt.NoEntryBar++ // no 14 bars of ATR history: counted with the no-entry cases
			return VwTrade{}, false
		}
		stop0 = entry - vwATRMult*atr[m]
	case StopLimitLow:
		stop0 = bars[m].L - 0.01
	default:
		stop0 = bars[m].L
	}
	if entry <= stop0 {
		cnt.SkippedGap++
		return VwTrade{}, false
	}
	risk := entry - stop0
	tp := 0.0
	switch r.Exit {
	case ExitTPPct:
		tp = entry * (1 + r.TPPct)
	case ExitTPR:
		tp = entry + r.TPR*risk
	}
	stop := stop0
	reached1R := false
	exitIdx, exitPx, reason := e, entry, "eod"
	for k := e; k < len(bars) && ETDate(bars[k].T) == date && ETMinute(bars[k].T) <= p.LastBarMin; k++ {
		bk := bars[k]
		if bk.L <= stop { // the stop is checked first: a bar touching both stop and TP is a stop
			reason = "stop"
			if stop > stop0 {
				reason = "trail"
			}
			exitIdx, exitPx = k, math.Min(stop, bk.O)
			break
		}
		if tp > 0 && bk.H >= tp {
			exitIdx, exitPx, reason = k, tp, "tp"
			break
		}
		switch r.Exit {
		case ExitTrailPrev:
			stop = math.Max(stop, bk.L)
		case ExitTrail2:
			lo := bk.L
			if k > 0 {
				lo = math.Min(lo, bars[k-1].L)
			}
			stop = math.Max(stop, lo)
		case ExitTrailEMA:
			if v := ser.Ema[k]; !math.IsNaN(v) {
				stop = math.Max(stop, v*(1-vwEmaTrailGap))
			}
		case ExitTrailAfter:
			if bk.H >= entry+risk {
				reached1R = true
			}
			if reached1R {
				stop = math.Max(stop, bk.L)
			}
		}
		exitIdx, exitPx, reason = k, bk.C, "eod"
	}
	tr := VwTrade{
		Variant: r.ID, Date: date, Sym: sym,
		CrossTs: bars[m].T, CrossMin: ETMinute(bars[m].T), EntryTs: bars[e].T, EntryMin: ETMinute(bars[e].T),
		Entry: entry, Stop0: stop0, Risk: risk, ExitTs: bars[exitIdx].T, Exit: exitPx, Reason: reason,
		BarsHeld: exitIdx - e + 1, R: (exitPx - entry) / risk, RetPct: 100 * (exitPx - entry) / entry,
		RiskPct: 100 * risk / entry, CrossD: d, BarDvol: barDvol, Rvol: rvol, exitBar: exitIdx,
	}
	shares := int64(math.Floor(p.RiskUSD / risk))
	if shares < 1 || float64(shares)*entry > p.MaxPosUSD {
		cnt.SkippedSize++
		return tr, true
	}
	tr.Sized, tr.Shares = true, shares
	n := float64(shares)
	tr.GrossUSD = n * (exitPx - entry)
	tr.CostUSD = 2*math.Max(p.CommPerShare*n, p.CommMinPerOrd) + 2*p.SlipPerShare*n
	tr.NetUSD = tr.GrossUSD - tr.CostUSD
	tr.RNet = tr.NetUSD / (n * risk)
	cnt.Traded++
	return tr, true
}
