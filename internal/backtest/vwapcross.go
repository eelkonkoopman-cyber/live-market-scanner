package backtest

import (
	"math"
	"sort"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// The "vwapcross" strategy: the live 9EMA VWAP CROSS bull alert (internal/engine/
// emavwap.go) traded mechanically. Buy the open of the candle after the cross,
// stop at the low of the cross candle, trail the stop to each completed candle's
// low, flat at the 15:59 close. Pure: bars in, trades out; no I/O.
//
// APPROXIMATIONS (all repeated in the report):
//   - VWAP: the regular-hours session VWAP restarting 09:30 is rebuilt from the
//     1-minute bars as sum(typical*v)/sum(v), typical = (h+l+c)/3. core.Bar from
//     the REST feed carries no per-bar vwap here, and the live VWAP is built from
//     every print.
//   - intrabar: the live EMA includes the forming candle; the firing test is
//     high(m)*k + EMA(m-1)*(1-k) > VWAP, with VWAP taken at the END of bar m (the
//     live VWAP at the instant of the high is not known). The poke is assumed to
//     be at the bar's high.
//   - no 0.05% hysteresis band and no "universe wait" of the closed-bar path.
//   - RVOL: cumVol(04:00..m) / (ADV10 * CurveFrac(end of m)) with a fixed typical
//     U-shaped curve (vwapCurve), not the symbol's own curve.
//   - a signal that fails the universe does nothing and does not consume the
//     side (the live intrabar path behaves the same); a fired signal disarms the
//     bull until a LATER bar closes with d < 0.
//   - fills: entry at the next candle's open and exits at the stop with only the
//     flat $0.01/share slippage + commission of the cost model; no queueing, no
//     halts, no partial fills.

// Variants of the signal.
const (
	VwIntrabar = "intrabar"
	VwClose    = "close"
)

// VwapParams are the rules; DefaultVwapParams is the spec.
type VwapParams struct {
	MinPrice     float64 // cross bar close > 25
	MinAdv10     float64 // 1,000,000
	MinBarDvol   float64 // cross bar close*volume >= 500,000
	MinSessVol   float64 // shares since 04:00 through the cross bar >= 500,000
	MinRvol      float64 // 1.5
	K            float64 // EMA smoothing 2/(9+1)
	EmaPeriod    int     // 9
	LastEntryMin int     // a next bar starting at or after 15:59 is not entered
	LastBarMin   int     // 15:59: the last bar a position is held through

	// costs and sizing
	RiskUSD       float64 // 50: dollars risked per trade
	MaxPosUSD     float64 // 25,000
	SlipPerShare  float64 // 0.01 on entry and on exit
	CommPerShare  float64 // 0.005
	CommMinPerOrd float64 // 1.00
}

// DefaultVwapParams is the architect's spec.
func DefaultVwapParams() VwapParams {
	return VwapParams{
		MinPrice: 25, MinAdv10: 1_000_000, MinBarDvol: 500_000, MinSessVol: 500_000, MinRvol: 1.5,
		K: 0.2, EmaPeriod: 9, LastEntryMin: 15*60 + 59, LastBarMin: 15*60 + 59,
		RiskUSD: 50, MaxPosUSD: 25_000, SlipPerShare: 0.01, CommPerShare: 0.005, CommMinPerOrd: 1,
	}
}

const (
	vwSessionOpen  = 9*60 + 30
	vwSessionClose = 16 * 60
	vwPreOpen      = 4 * 60
)

// vwapCurve is the typical cumulative share of a US equity's daily volume traded
// by a minute of the ET day (04:00 included, extended hours counted in the whole
// day), piecewise linear between the points; before 04:00 it is 0 and after 16:00
// 1. It is deliberately one table: change it here. Approximate by design.
var vwapCurve = []struct {
	Min  int
	Frac float64
}{
	{vwPreOpen, 0}, {9*60 + 30, 0.03}, {10 * 60, 0.15}, {11 * 60, 0.30}, {12 * 60, 0.45},
	{13 * 60, 0.55}, {14 * 60, 0.65}, {15 * 60, 0.78}, {16 * 60, 1.0},
}

// CurveFrac is the typical cumulative volume fraction at the ET minute of day.
func CurveFrac(min float64) float64 {
	c := vwapCurve
	if min <= float64(c[0].Min) {
		return c[0].Frac
	}
	for i := 1; i < len(c); i++ {
		if min <= float64(c[i].Min) {
			a, b := c[i-1], c[i]
			return a.Frac + (b.Frac-a.Frac)*(min-float64(a.Min))/float64(b.Min-a.Min)
		}
	}
	return c[len(c)-1].Frac
}

// VwUniverseReject names the first universe test the cross bar fails ("" = in).
func VwUniverseReject(p VwapParams, closePx, adv10, barDvol, sessVol, rvol float64) string {
	switch {
	case !(closePx > p.MinPrice):
		return "price"
	case !(adv10 >= p.MinAdv10):
		return "adv10"
	case !(barDvol >= p.MinBarDvol):
		return "barDollarVol"
	case !(sessVol >= p.MinSessVol):
		return "sessionVol"
	case !(rvol >= p.MinRvol):
		return "rvol"
	}
	return ""
}

// VwTrade is one simulated trade.
type VwTrade struct {
	Variant string `json:"variant"`
	Date    string `json:"date"`
	Sym     string `json:"sym"`

	CrossTs   int64   `json:"crossTs"`
	CrossMin  int     `json:"crossMin"` // ET minute of day of the cross bar start
	EntryTs   int64   `json:"entryTs"`
	EntryMin  int     `json:"entryMin"`
	Entry     float64 `json:"entry"`
	Stop0     float64 `json:"initialStop"`
	Risk      float64 `json:"riskPerShare"` // entry - initial stop
	ExitTs    int64   `json:"exitTs"`
	Exit      float64 `json:"exit"`
	Reason    string  `json:"exitReason"` // stop | eod
	BarsHeld  int     `json:"barsHeld"`   // bars from the entry bar to the exit bar, both counted
	R         float64 `json:"rGross"`     // (exit - entry) / risk
	RetPct    float64 `json:"returnPct"`  // gross, of the entry price
	RiskPct   float64 `json:"riskPct"`    // risk / entry * 100
	CrossD    float64 `json:"crossD"`     // d at the cross bar's close
	BarDvol   float64 `json:"barDollarVol"`
	Rvol      float64 `json:"rvolApprox"`
	SplitFact float64 `json:"splitFactor,omitempty"`

	// Sizing: shares = floor(RiskUSD/risk). Sized false = the trade is skipped by
	// the sizing rule (shares < 1 or position over MaxPosUSD): kept for the CSV,
	// left out of every statistic.
	Sized    bool    `json:"sized"`
	Shares   int64   `json:"shares"`
	GrossUSD float64 `json:"grossUSD"`
	CostUSD  float64 `json:"costUSD"` // slippage + commissions, both sides
	NetUSD   float64 `json:"netUSD"`
	RNet     float64 `json:"rNet"` // NetUSD / (shares * risk)

	exitBar int // index of the exit bar in the day's bars (simulation only)

	// grid v2 / benchmark context (not in any JSON or v1 output)
	BenchOK   bool    `json:"-"` // benchmark bars were available for the trade's minutes
	BenchRet  float64 `json:"-"` // benchmark return % over the trade's minutes
	Excess    float64 `json:"-"` // RetPct - BenchRet
	ExcessNet float64 `json:"-"` // net % - BenchRet
	DayChg    float64 `json:"-"` // day change % at the cross (NaN = unknown); grid v2 only
	BelowMin  int     `json:"-"` // belowMin at the cross; grid v2 only
}

// VwCounts are the funnel counters of the simulation (additive).
type VwCounts struct {
	SymbolDays     int            `json:"symbolDays"`
	Fired          int            `json:"signalsFired"`    // geometry + universe passed
	UniverseReject map[string]int `json:"universeRejects"` // geometry passed, universe failed, by first failing test
	IgnoredOpen    int            `json:"ignoredPositionOpen"`
	NoEntryBar     int            `json:"noEntryBar"`         // no next bar, or it starts at/after 15:59
	SkippedGap     int            `json:"skippedGap"`         // entry open <= stop
	SkippedSize    int            `json:"skippedSize"`        // shares < 1 or position > cap
	Traded         int            `json:"traded"`             // trades in the statistics
	Unfilled       int            `json:"unfilled,omitempty"` // limit entries that never filled (grid rules only)
}

// Add folds o into c.
func (c *VwCounts) Add(o VwCounts) {
	c.SymbolDays += o.SymbolDays
	c.Fired += o.Fired
	c.IgnoredOpen += o.IgnoredOpen
	c.NoEntryBar += o.NoEntryBar
	c.SkippedGap += o.SkippedGap
	c.SkippedSize += o.SkippedSize
	c.Traded += o.Traded
	c.Unfilled += o.Unfilled
	if len(o.UniverseReject) > 0 && c.UniverseReject == nil {
		c.UniverseReject = map[string]int{}
	}
	for k, v := range o.UniverseReject {
		c.UniverseReject[k] += v
	}
}

// VwapDaySeries holds, per bar, the quantities the signals are built from; it
// is exported so tests can check the arithmetic against a reference.
type VwapDaySeries struct {
	Ema  []float64 // NaN until the 9th close
	Vwap []float64 // regular-hours VWAP at the END of the bar; NaN outside 09:30-16:00
}

// VwapSeries computes the EMA over every close (previous day + the day, NOT
// reset at 04:00) and the regular-hours VWAP of the day `date`.
func VwapSeries(bars []core.Bar, date string, period int) VwapDaySeries {
	closes := make([]float64, len(bars))
	for i, b := range bars {
		closes[i] = b.C
	}
	s := VwapDaySeries{Ema: emaSeries(closes, period), Vwap: make([]float64, len(bars))}
	var pv, vv float64
	for i, b := range bars {
		s.Vwap[i] = math.NaN()
		if ETDate(b.T) != date {
			continue
		}
		m := ETMinute(b.T)
		if m < vwSessionOpen || m >= vwSessionClose {
			continue
		}
		v := float64(b.V)
		pv += (b.H + b.L + b.C) / 3 * v
		vv += v
		if vv > 0 {
			s.Vwap[i] = pv / vv
		}
	}
	return s
}

// SimulateVwapCrossDay runs one symbol-day for one signal variant. bars are the
// previous day + the day, ascending, 1-minute, extended hours.
func SimulateVwapCrossDay(sym, date string, adv10 float64, bars []core.Bar, variant string, p VwapParams) ([]VwTrade, VwCounts) {
	cnt := VwCounts{SymbolDays: 1}
	if len(bars) == 0 {
		return nil, cnt
	}
	ser := VwapSeries(bars, date, p.EmaPeriod)
	var out []VwTrade

	var (
		have      bool
		prevD     float64
		armed     = true
		firedIdx  = -1
		cumVol    float64
		openUntil = -1 // bar index of the open position's exit bar
	)
	for i, b := range bars {
		if ETDate(b.T) != date {
			continue
		}
		min := ETMinute(b.T)
		if min >= vwPreOpen {
			cumVol += float64(b.V)
		}
		if min < vwSessionOpen || min >= vwSessionClose { // fully inside 09:30-16:00 (1-minute bars)
			continue
		}
		ema, vwap := ser.Ema[i], ser.Vwap[i]
		if math.IsNaN(ema) || math.IsNaN(vwap) || !(vwap > 0) {
			continue
		}
		d := (ema - vwap) / vwap
		if !have { // the first judged bar only sets the side
			have, prevD = true, d
			continue
		}
		geom := false
		if armed && prevD <= 0 && i > 0 && !math.IsNaN(ser.Ema[i-1]) {
			switch variant {
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
			if why := VwUniverseReject(p, b.C, adv10, barDvol, cumVol, rvol); why != "" {
				if cnt.UniverseReject == nil {
					cnt.UniverseReject = map[string]int{}
				}
				cnt.UniverseReject[why]++
			} else {
				cnt.Fired++
				armed, firedIdx = false, i
				if i <= openUntil {
					cnt.IgnoredOpen++
				} else if tr, ok := vwapTrade(bars, i, sym, date, variant, d, barDvol, rvol, p, &cnt); ok {
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

// vwapTrade enters at the open of bar m+1 and walks the stop. ok false = no trade
// (counted in cnt). A trade skipped only by the sizing rule is returned (Sized
// false) so its holding period still blocks a second entry and it reaches the CSV.
func vwapTrade(bars []core.Bar, m int, sym, date, variant string, d, barDvol, rvol float64, p VwapParams, cnt *VwCounts) (VwTrade, bool) {
	e := m + 1
	if e >= len(bars) || ETDate(bars[e].T) != date || ETMinute(bars[e].T) >= p.LastEntryMin {
		cnt.NoEntryBar++
		return VwTrade{}, false
	}
	entry, stop0 := bars[e].O, bars[m].L
	if entry <= stop0 {
		cnt.SkippedGap++
		return VwTrade{}, false
	}
	stop := stop0
	exitIdx, exitPx, reason := e, entry, "eod"
	for k := e; k < len(bars) && ETDate(bars[k].T) == date && ETMinute(bars[k].T) <= p.LastBarMin; k++ {
		bk := bars[k]
		if bk.L <= stop {
			exitIdx, exitPx, reason = k, math.Min(stop, bk.O), "stop"
			break
		}
		stop = math.Max(stop, bk.L) // trail to the completed candle's low, never down
		exitIdx, exitPx, reason = k, bk.C, "eod"
	}
	risk := entry - stop0
	tr := VwTrade{
		Variant: variant, Date: date, Sym: sym,
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
	comm := 2 * math.Max(p.CommPerShare*n, p.CommMinPerOrd)
	slip := 2 * p.SlipPerShare * n
	tr.GrossUSD = n * (exitPx - entry)
	tr.CostUSD = comm + slip
	tr.NetUSD = tr.GrossUSD - tr.CostUSD
	tr.RNet = tr.NetUSD / (n * risk)
	cnt.Traded++
	return tr, true
}

// ─── prefilter ──────────────────────────────────────────────────────────

// VwPrefOpts are the vwapcross prefilter's knobs.
type VwPrefOpts struct {
	MinPrice    float64                  // 25: the day's HIGH must exceed it (a necessary condition of a cross bar close > 25)
	MinAdv10    float64                  // 1,000,000
	MinVolRatio float64                  // 0.8: whole-day volume (incl. extended hours) >= this * ADV10
	Cap         int                      // 100000: effectively none
	MinHist     int                      // 5
	SplitFactor func(sym string) float64 `json:"-"`
}

// DefaultVwPrefilter is the spec.
func DefaultVwPrefilter() VwPrefOpts {
	return VwPrefOpts{MinPrice: 25, MinAdv10: 1_000_000, MinVolRatio: 0.8, Cap: 100000, MinHist: 5}
}

// VwPrefStats counts the stages of one day.
type VwPrefStats struct {
	Date      string `json:"date"`
	Universe  int    `json:"universe"`
	NoAdv     int    `json:"noAdv"`
	LowPrice  int    `json:"lowPrice"`
	LowAdv    int    `json:"lowAdv"`
	LowVolume int    `json:"lowVolume"`
	Passed    int    `json:"passed"`
	Kept      int    `json:"kept"`
	Dropped   int    `json:"dropped"`
}

// VwapPrefilter selects the (symbol, day) pairs that get minute bars. Every test
// is a necessary condition of a universe-passing cross bar that the grouped
// daily bar can check, or (the volume test) a documented loose superset of
// RVOL >= 1.5 at some minute; none reads what happened after the cross.
// Prices/volumes are on the basis of the day (split-un-adjusted if set).
func VwapPrefilter(date string, today map[string]core.DailyBar, hist []map[string]core.DailyBar, o VwPrefOpts) ([]PrefCand, VwPrefStats) {
	st := VwPrefStats{Date: date, Universe: len(today)}
	var out []PrefCand
	for sym, b := range today {
		adv, ok := ADV10(sym, hist, o.MinHist)
		if !ok {
			st.NoAdv++
			continue
		}
		f := 1.0
		if o.SplitFactor != nil {
			if v := o.SplitFactor(sym); v > 0 && !math.IsInf(v, 0) {
				f = v
			}
		}
		if !(b.H/f > o.MinPrice) {
			st.LowPrice++
			continue
		}
		if !(adv*f >= o.MinAdv10) {
			st.LowAdv++
			continue
		}
		if float64(b.V) < o.MinVolRatio*adv {
			st.LowVolume++
			continue
		}
		prevC := 0.0
		if len(hist) > 0 {
			if pb, ok := hist[len(hist)-1][sym]; ok {
				prevC = pb.C / f
			}
		}
		out = append(out, PrefCand{Sym: sym, Adv10: adv * f, PrevClose: prevC, DayVol: float64(b.V) * f, DayHigh: b.H / f, Ratio: float64(b.V) / adv, SplitFactor: f})
	}
	st.Passed = len(out)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ratio != out[j].Ratio {
			return out[i].Ratio > out[j].Ratio
		}
		return out[i].Sym < out[j].Sym
	})
	if o.Cap > 0 && len(out) > o.Cap {
		out = out[:o.Cap]
	}
	st.Kept = len(out)
	st.Dropped = st.Passed - st.Kept
	return out, st
}
