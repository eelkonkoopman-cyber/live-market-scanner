package backtest

import (
	"math"
	"sort"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// Pre-registered grid v2 ("VWAP reclaim") of the vwapcross strategy. Defined in
// code BEFORE any result was looked at; do not edit a variant after a run, add a
// new grid version instead. It was derived from the October drift study of round
// 2; SEPTEMBER is the reserved out-of-sample month.
//
// Signals come from the drift study's detectors (the INTRABAR 1-minute signal,
// or the 5m signal for D7), so the context features are exactly the ones the
// drift study measured. Differences from grid v1 worth knowing:
//   - a context gate (dayChg, belowMin, entry window) filters the RECORDED
//     signals: a gated-out signal still consumed the side (the drift study's
//     population), unlike grid v1's filters.
//   - stops are FIXED (no trailing) except the reference R0; exits are the stop,
//     a 60-minute time exit ("time": the close of the last bar starting at or
//     before entry+60 min) or the 15:59 close ("eod").
//   - one position per symbol (a signal during an open position is ignored but
//     still consumes the side); the $25k position cap and the cost model of v1.
//   - P1 is a second pass over D5's sized trades: all trades of a day sorted by
//     entry time (then symbol); a trade is skipped when the daily realised net R
//     is already <= -3 (trades whose exit bar has ended by its entry), else when
//     3 positions are open (exit bar not yet ended). The symbol's own
//     one-position rule was already applied in D5's pass, so a trade skipped by
//     P1 still blocked its symbol there: slightly conservative.
//   - R0 IS grid v1's B0 (SimulateVwapRulesDay): the reference.

// Grid2Version names the grid below.
const Grid2Version = "grid v2"

// Stop kinds of grid v2.
const (
	G2StopCrossLow = "cross-low"
	G2StopATR      = "entry-1.5*ATR14"
	G2StopDayLow   = "day-low-1tick"

	g2Tick      = 0.01
	g2TimeExit  = 60 // minutes
	g2DayChgMax = -2.0
	g2BelowMin  = 10
	g2WinFrom   = 10 * 60
	g2WinTo     = 12 * 60
)

// Grid2Rules is one variant.
type Grid2Rules struct {
	ID, Desc string
	Ref      bool   // R0: grid v1 B0 through SimulateVwapRulesDay
	Derive   string // P1: a portfolio pass over the sized trades of this variant
	Signal   string // VwIntrabar | Drift5m
	Stop     string
	HoldMin  int  // 0 = hold to the 15:59 close; N = time exit at entry+N min
	Trail    bool // trail to each completed bar's low (test only; no grid variant uses it)

	UseDayChg   bool
	DayChgBelow float64 // dayChg% at the cross must be strictly below this
	MinBelow    int     // belowMin >= this (0 = off)
	FromMin     int     // entry minute window [FromMin, ToMin) (ToMin 0 = off)
	ToMin       int

	// grid v3 gates (zero = off)
	FirstOnly bool    // only the symbol-day's first raw cross (DriftSignal.CrossN == 1)
	Trend5Yes bool    // DriftSignal.Trend5 == "yes"
	Avoid     bool    // skip when dayChg < -2% AND spyDay < -0.5% (both known)
	MinDist   float64 // DistPct (cross close vs VWAP, %) must be strictly above this; 0 = off
	Lim       PortfolioLimits
}

// PortfolioLimits are P1's limits.
type PortfolioLimits struct {
	MaxConcurrent int     // 3
	DayLossR      float64 // 3: no new entries once the realised net R of the day <= -DayLossR
}

// DefaultPortfolioLimits is P1's spec.
func DefaultPortfolioLimits() PortfolioLimits { return PortfolioLimits{MaxConcurrent: 3, DayLossR: 3} }

// Grid2 returns grid v2 sorted by id.
func Grid2() []Grid2Rules {
	h := func(id, desc, stop string, hold int) Grid2Rules {
		return Grid2Rules{ID: id, Desc: desc, Signal: VwIntrabar, Stop: stop, HoldMin: hold}
	}
	d := func(id, desc string, base Grid2Rules) Grid2Rules {
		base.ID, base.Desc = id, desc
		base.UseDayChg, base.DayChgBelow = true, g2DayChgMax
		return base
	}
	h1 := h("H1", "hold to EOD, fixed stop = cross low", G2StopCrossLow, 0)
	h2 := h("H2", "time exit at entry+60 min, fixed stop = cross low", G2StopCrossLow, g2TimeExit)
	h3 := h("H3", "hold to EOD, fixed stop = entry - 1.5 x ATR14 (1m)", G2StopATR, 0)
	h4 := h("H4", "hold to EOD, fixed stop = day's regular-hours low so far (at the cross bar) - 1 tick; skip if >= entry", G2StopDayLow, 0)
	d5 := d("D5", "VWAP reclaim: dayChg < -2% + belowMin >= 10 + entry 10:00-12:00 + H3", h3)
	d5.MinBelow, d5.FromMin, d5.ToMin = g2BelowMin, g2WinFrom, g2WinTo
	d6 := d5
	d6.ID, d6.Desc, d6.HoldMin = "D6", "D5 but time exit at entry+60 min", g2TimeExit
	d7 := d5
	d7.ID, d7.Desc, d7.Signal = "D7", "D5 on the 5m signal instead of the 1m intrabar signal", Drift5m
	p1 := d5
	p1.ID, p1.Desc, p1.Derive = "P1", "D5 with portfolio limits: max 3 concurrent positions, daily loss limit -3R", "D5"
	out := []Grid2Rules{
		{ID: "R0", Desc: "reference: grid v1 B0 (entry next open; stop cross low; trail prev-candle low)", Ref: true, Signal: VwIntrabar, Stop: G2StopCrossLow},
		h1, h2, h3, h4,
		d("D1", "dayChg < -2% + H1", h1), d("D2", "dayChg < -2% + H2", h2), d("D3", "dayChg < -2% + H3", h3), d("D4", "dayChg < -2% + H4", h4),
		d5, d6, d7, p1,
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Grid2Gate reports whether a recorded signal passes r's context gates.
func Grid2Gate(r Grid2Rules, s DriftSignal) bool {
	if r.UseDayChg && !(s.DayChgPct < r.DayChgBelow) { // NaN fails
		return false
	}
	if r.MinBelow > 0 && s.BelowMin < r.MinBelow {
		return false
	}
	if r.ToMin > 0 && (s.EntryMin < r.FromMin || s.EntryMin >= r.ToMin) {
		return false
	}
	if r.FirstOnly && s.CrossN != 1 {
		return false
	}
	if r.Trend5Yes && s.Trend5 != "yes" {
		return false
	}
	if r.MinDist > 0 && !(s.DistPct > r.MinDist) { // NaN fails
		return false
	}
	if r.Avoid && s.DayChgPct < g3AvoidDayChg && s.SpyDayPct < g3AvoidSpyDay { // NaN never avoids
		return false
	}
	return true
}

// dayLowSeries is the running low of the regular-hours bars of `date` through
// each bar (NaN before the first regular bar and for other days).
func dayLowSeries(bars []core.Bar, date string) []float64 {
	out := make([]float64, len(bars))
	lo := math.Inf(1)
	for i, b := range bars {
		out[i] = math.NaN()
		if ETDate(b.T) != date {
			continue
		}
		if m := ETMinute(b.T); m < vwSessionOpen || m >= vwSessionClose {
			continue
		}
		lo = math.Min(lo, b.L)
		out[i] = lo
	}
	return out
}

func g2BarIndex(bars []core.Bar, ts int64) int {
	i := sort.Search(len(bars), func(k int) bool { return bars[k].T >= ts })
	if i < len(bars) && bars[i].T == ts {
		return i
	}
	return -1
}

// SimulateGrid2Day runs every simulated rule on one symbol-day. Derived rules
// (P1) yield nil. bars are the previous day + the day, ascending, 1-minute.
// The returned slices are parallel to rules.
func SimulateGrid2Day(sym, date string, adv10 float64, bars []core.Bar, rules []Grid2Rules, p VwapParams) ([][]VwTrade, []VwCounts) {
	return SimulateGrid2DayRegime(sym, date, adv10, bars, rules, p, nil)
}

// SimulateGrid2DayRegime is SimulateGrid2Day with a benchmark: for a rule with
// Avoid set the recorded signals get their market-regime features (read at the
// cross minute) before the gates run. bench == nil leaves SpyDayPct NaN, so
// Avoid never skips.
func SimulateGrid2DayRegime(sym, date string, adv10 float64, bars []core.Bar, rules []Grid2Rules, p VwapParams, bench *Bench) ([][]VwTrade, []VwCounts) {
	trades := make([][]VwTrade, len(rules))
	counts := make([]VwCounts, len(rules))
	for i := range counts {
		counts[i] = VwCounts{SymbolDays: 1}
	}
	if len(bars) == 0 {
		return trades, counts
	}
	var d *driftDay
	sigs := map[string][]DriftSignal{}
	regimeSet := map[string]bool{}
	var atr, dayLow []float64
	for i, r := range rules {
		switch {
		case r.Derive != "":
			continue
		case r.Ref:
			trades[i], counts[i] = SimulateVwapRulesDay(sym, date, adv10, bars, VwapGrid()[0], p)
			continue
		}
		if d == nil {
			d = newDriftDay(bars, date, p)
		}
		if _, ok := sigs[r.Signal]; !ok {
			switch r.Signal {
			case Drift5m:
				sigs[r.Signal] = d.fiveSignals(sym, adv10, p)
			case VwClose:
				sigs[r.Signal] = d.minuteSignals(sym, adv10, VwClose, p)
			default:
				sigs[r.Signal] = d.minuteSignals(sym, adv10, VwIntrabar, p)
			}
			for k := range sigs[r.Signal] {
				sigs[r.Signal][k].SpyDayPct = math.NaN()
			}
		}
		if r.Avoid && bench != nil && !regimeSet[r.Signal] {
			regimeSet[r.Signal] = true
			bench.AttachRegime(sigs[r.Signal])
		}
		if r.Stop == G2StopATR && atr == nil {
			atr = atrSeries(bars, vwATRPeriod)
		}
		if r.Stop == G2StopDayLow && dayLow == nil {
			dayLow = dayLowSeries(bars, date)
		}
		trades[i], counts[i] = simulateGrid2Rule(sym, date, bars, r, sigs[r.Signal], atr, dayLow, p)
		counts[i].SymbolDays = 1
	}
	return trades, counts
}

func simulateGrid2Rule(sym, date string, bars []core.Bar, r Grid2Rules, sigs []DriftSignal, atr, dayLow []float64, p VwapParams) ([]VwTrade, VwCounts) {
	var cnt VwCounts
	var out []VwTrade
	openUntil := -1
	for _, s := range sigs {
		if !Grid2Gate(r, s) {
			continue
		}
		m := g2BarIndex(bars, s.CrossTs)
		if m < 0 {
			continue
		}
		cnt.Fired++
		if m <= openUntil {
			cnt.IgnoredOpen++
			continue
		}
		if tr, ok := g2Trade(bars, m, sym, date, r, s, atr, dayLow, p, &cnt); ok {
			openUntil = tr.exitBar
			out = append(out, tr)
		}
	}
	return out, cnt
}

// g2Trade resolves the entry (open of the bar after the cross), the fixed stop
// and the exit of one gated signal at bar m.
func g2Trade(bars []core.Bar, m int, sym, date string, r Grid2Rules, s DriftSignal, atr, dayLow []float64, p VwapParams, cnt *VwCounts) (VwTrade, bool) {
	e := m + 1
	if e >= len(bars) || ETDate(bars[e].T) != date || ETMinute(bars[e].T) >= p.LastEntryMin {
		cnt.NoEntryBar++
		return VwTrade{}, false
	}
	entry := bars[e].O
	var stop0 float64
	switch r.Stop {
	case G2StopATR:
		if m >= len(atr) || math.IsNaN(atr[m]) {
			cnt.NoEntryBar++
			return VwTrade{}, false
		}
		stop0 = entry - vwATRMult*atr[m]
	case G2StopDayLow:
		if m >= len(dayLow) || math.IsNaN(dayLow[m]) {
			cnt.NoEntryBar++
			return VwTrade{}, false
		}
		stop0 = dayLow[m] - g2Tick
	default:
		stop0 = s.CrossLow
	}
	if entry <= stop0 {
		cnt.SkippedGap++
		return VwTrade{}, false
	}
	risk := entry - stop0
	stop := stop0
	deadline := bars[e].T + int64(r.HoldMin)*minuteMs
	exitIdx, exitPx, reason := e, entry, "eod"
	for k := e; k < len(bars) && ETDate(bars[k].T) == date && ETMinute(bars[k].T) <= p.LastBarMin; k++ {
		bk := bars[k]
		if r.HoldMin > 0 && bk.T > deadline { // a gap in the minutes: the previous bar was the last one within the hold
			reason = "time"
			break
		}
		if bk.L <= stop {
			reason = "stop"
			if stop > stop0 {
				reason = "trail"
			}
			exitIdx, exitPx = k, math.Min(stop, bk.O)
			break
		}
		if r.Trail {
			stop = math.Max(stop, bk.L)
		}
		exitIdx, exitPx, reason = k, bk.C, "eod"
		if r.HoldMin > 0 && bk.T >= deadline {
			reason = "time"
			break
		}
	}
	tr := VwTrade{
		Variant: r.ID, Date: date, Sym: sym,
		CrossTs: bars[m].T, CrossMin: ETMinute(bars[m].T), EntryTs: bars[e].T, EntryMin: ETMinute(bars[e].T),
		Entry: entry, Stop0: stop0, Risk: risk, ExitTs: bars[exitIdx].T, Exit: exitPx, Reason: reason,
		BarsHeld: exitIdx - e + 1, R: (exitPx - entry) / risk, RetPct: 100 * (exitPx - entry) / entry,
		RiskPct: 100 * risk / entry, BarDvol: bars[m].C * float64(bars[m].V), Rvol: s.Rvol, exitBar: exitIdx,
		DayChg: s.DayChgPct, BelowMin: s.BelowMin,
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

// PortfolioSkips counts the trades a portfolio pass dropped.
type PortfolioSkips struct {
	MaxPos  int `json:"skippedMaxPositions"`
	DayLoss int `json:"skippedDailyLoss"`
}

// ApplyPortfolio is P1's second pass: per day, the sized trades sorted by entry
// time (then symbol). A position stays open until the end of its exit bar. The
// daily loss limit is tested first (realised net R of the trades closed by the
// entry), then the concurrency limit. Returns the kept trades ordered by
// (date, entry time, symbol).
func ApplyPortfolio(trades []VwTrade, lim PortfolioLimits) ([]VwTrade, PortfolioSkips) {
	var sized []VwTrade
	for _, t := range trades {
		if t.Sized {
			sized = append(sized, t)
		}
	}
	sort.SliceStable(sized, func(i, j int) bool {
		a, b := sized[i], sized[j]
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		if a.EntryTs != b.EntryTs {
			return a.EntryTs < b.EntryTs
		}
		return a.Sym < b.Sym
	})
	var kept []VwTrade
	var sk PortfolioSkips
	dayStart := 0 // index into kept of the first trade of the current day
	for i, t := range sized {
		if i == 0 || sized[i-1].Date != t.Date {
			dayStart = len(kept)
		}
		var realised float64
		open := 0
		for _, k := range kept[dayStart:] {
			if k.ExitTs+minuteMs <= t.EntryTs {
				realised += k.RNet
			} else {
				open++
			}
		}
		switch {
		case lim.DayLossR > 0 && realised <= -lim.DayLossR:
			sk.DayLoss++
		case lim.MaxConcurrent > 0 && open >= lim.MaxConcurrent:
			sk.MaxPos++
		default:
			kept = append(kept, t)
		}
	}
	return kept, sk
}
