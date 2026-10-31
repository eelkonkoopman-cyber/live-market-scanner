package backtest

import (
	"math"
	"sort"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// DIAGNOSTIC "drift" mode of the vwapcross backtest: where, if anywhere, does the
// signal have follow-through? Every signal that passes the vwapcross universe is
// recorded with its forward returns from the entry (the open of the 1-minute bar
// after the cross) and a set of context features. No stops, no positions, no
// one-position-per-symbol rule: every signal counts. Pure: bars in, signals out.
//
// DEFINITIONS (repeated in drift.md):
//   - r5/r15/r30/r60 = close of the last 1-minute bar starting at or before
//     entry+N minutes (same day, up to the 15:59 bar), over the entry, minus 1,
//     in percent. rEOD = the 15:59 close (the last regular bar of the day). A
//     horizon beyond the day is therefore the EOD value.
//   - MFE/MAE = max high / min low over the bars starting in [entry, entry+30min)
//     over the entry, minus 1, in percent (MAE <= 0).
//   - stop units = return / ((entry - cross low) / entry); absent (NaN) when
//     entry <= cross low.
//   - crossNo counts RAW crosses of the symbol-day: geometry passed, with its own
//     armed state that universe failures do NOT leave unconsumed.
//   - belowMin = consecutive bars (5-minute candles x5 for the 5m signal)
//     immediately before the cross bar with EMA <= VWAP.
//   - the 5m signal: 9 EMA of the closes of clock-aligned 5-minute candles built
//     from regular-hours (09:30-16:00) 1-minute bars, continuing across days,
//     crossing above the regular-hours VWAP at a COMPLETED candle (evaluated at
//     the candle's last 1-minute bar, where the universe is also tested).
//     burst/rvol/dollar volume use that last 1-minute bar; the risk unit uses
//     the candle's low.
//   - trend5 for every signal is trend5mOK (candles5, extended-hours EMA9 of 5m
//     closes): for the 5m signal it tests the cross candle itself.

// Drift5m is the name of the 5-minute alternative signal.
const Drift5m = "5m"

// DriftHorizons are the forward horizons in minutes (the fifth is the day end).
var DriftHorizons = [4]int{5, 15, 30, 60}

// DriftSignal is one recorded signal.
type DriftSignal struct {
	Type, Date, Sym             string
	CrossTs, EntryTs            int64
	Entry, CrossLow, CrossClose float64
	First                       bool // the first signal of this type for the symbol-day

	Ret     [5]float64 // %: r5 r15 r30 r60 rEOD
	Units   [5]float64 // the same in stop units (NaN if no positive risk)
	MFE     float64    // %, next 30 minutes
	MAE     float64    // %, next 30 minutes
	RiskPct float64    // (entry - cross low) / entry * 100

	// numeric features (NaN = unknown)
	GapPct, DayChgPct, Rvol, Burst, DistPct float64
	CrossN, BelowMin, EntryMin              int

	// benchmark (set by Bench.AttachDrift; BenchOK false = none)
	BenchOK  bool
	BenchRet [5]float64 // %, the benchmark over the same horizons
	Excess   [5]float64 // Ret - BenchRet

	// market regime (set by Bench.AttachRegime, -drift2 only; RegimeOK false = none)
	RegimeOK                         bool
	SpyDayPct, Spy30Pct, RelStrPct   float64 // SPY at the cross minute vs its previous regular close / vs 30 minutes before; stock dayChg% - SPY day%
	SpyAboveVwap                     bool
	SpyDay, SpyVsVwap, Spy30, RelStr string // buckets ("n/a" when unknown)

	// buckets
	Tod, Gap, DayChg, RvolB, BurstB, CrossB, BelowB, DistB, Trend5, AboveOpen, PriceB string
}

// ─── buckets ────────────────────────────────────────────────────────────

func todBucket(min int) string {
	switch {
	case min < 10*60:
		return "09:30-10:00"
	case min < 10*60+30:
		return "10:00-10:30"
	case min < 11*60:
		return "10:30-11:00"
	case min < 12*60:
		return "11:00-12:00"
	case min < 14*60:
		return "12:00-14:00"
	}
	return "14:00-16:00"
}

// pctBucket is for gap% and dayChg%.
func pctBucket(v float64) string {
	switch {
	case math.IsNaN(v):
		return "n/a"
	case v < -2:
		return "<-2"
	case v < 0:
		return "-2..0"
	case v < 2:
		return "0..2"
	case v < 5:
		return "2..5"
	case v <= 10:
		return "5..10"
	}
	return ">10"
}

func rvolBucket(v float64) string {
	switch {
	case v < 2:
		return "1.5-2"
	case v < 3:
		return "2-3"
	case v <= 5:
		return "3-5"
	}
	return ">5"
}

func burstBucket(v float64) string {
	switch {
	case math.IsNaN(v):
		return "n/a"
	case v < 1:
		return "<1"
	case v < 2:
		return "1-2"
	case v <= 4:
		return "2-4"
	}
	return ">4"
}

func crossNoBucket(n int) string {
	switch {
	case n <= 1:
		return "1st"
	case n == 2:
		return "2nd"
	}
	return "3rd+"
}

func belowBucket(min int) string {
	switch {
	case min < 10:
		return "<10"
	case min < 30:
		return "10-30"
	case min <= 90:
		return "30-90"
	}
	return ">90"
}

func distBucket(v float64) string {
	switch {
	case math.IsNaN(v):
		return "n/a"
	case v < 0:
		return "<0"
	case v < 0.2:
		return "0-0.2"
	case v <= 0.5:
		return "0.2-0.5"
	}
	return ">0.5"
}

func priceBucket(px float64) string {
	switch {
	case px < 50:
		return "25-50"
	case px < 100:
		return "50-100"
	case px <= 300:
		return "100-300"
	}
	return ">300"
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// ─── forward returns ────────────────────────────────────────────────────

// DriftForward computes the forward returns of an entry at the open of bars[e]
// (the 15:59 bar being the last one of the day). crossLow is the risk reference.
// ok is false when bars[e] is not usable.
func DriftForward(bars []core.Bar, e int, crossLow float64) (ret, units [5]float64, mfe, mae, riskPct float64, ok bool) {
	for i := range ret {
		ret[i], units[i] = math.NaN(), math.NaN()
	}
	if e < 0 || e >= len(bars) {
		return ret, units, math.NaN(), math.NaN(), math.NaN(), false
	}
	date := ETDate(bars[e].T)
	entry := bars[e].O
	if !(entry > 0) || ETMinute(bars[e].T) > vwSessionClose-1 {
		return ret, units, math.NaN(), math.NaN(), math.NaN(), false
	}
	t0 := bars[e].T
	var last [4]float64
	for i := range last {
		last[i] = math.NaN()
	}
	eod := entry
	hi, lo := math.Inf(-1), math.Inf(1)
	for k := e; k < len(bars) && ETDate(bars[k].T) == date && ETMinute(bars[k].T) <= vwSessionClose-1; k++ {
		b := bars[k]
		for h, n := range DriftHorizons {
			if b.T <= t0+int64(n)*minuteMs {
				last[h] = b.C
			}
		}
		if b.T < t0+30*minuteMs {
			hi, lo = math.Max(hi, b.H), math.Min(lo, b.L)
		}
		eod = b.C
	}
	riskPct = math.NaN()
	risk := entry - crossLow
	if risk > 0 {
		riskPct = 100 * risk / entry
	}
	set := func(i int, px float64) {
		ret[i] = 100 * (px/entry - 1)
		if risk > 0 {
			units[i] = ret[i] / riskPct
		}
	}
	for h := range last {
		set(h, last[h])
	}
	set(4, eod)
	return ret, units, 100 * (hi/entry - 1), 100 * (lo/entry - 1), riskPct, true
}

// ─── day context and features ───────────────────────────────────────────

type driftDay struct {
	bars      []core.Bar
	date      string
	ser       VwapDaySeries
	prevClose float64 // last regular-hours close before the day; NaN if none
	dayOpen   float64 // the 09:30 open (first regular bar of the day); NaN if none
	cumVol    []float64
	cs        []candle5
	cema      []float64
}

func newDriftDay(bars []core.Bar, date string, p VwapParams) *driftDay {
	d := &driftDay{bars: bars, date: date, ser: VwapSeries(bars, date, p.EmaPeriod), prevClose: math.NaN(), dayOpen: math.NaN()}
	d.cumVol = make([]float64, len(bars))
	var cv float64
	for i, b := range bars {
		m := ETMinute(b.T)
		if ETDate(b.T) != date {
			if m >= vwSessionOpen && m < vwSessionClose {
				d.prevClose = b.C // ascending: ends as the last regular close before the day
			}
			continue
		}
		if m >= vwPreOpen {
			cv += float64(b.V)
		}
		d.cumVol[i] = cv
		if m >= vwSessionOpen && m < vwSessionClose && math.IsNaN(d.dayOpen) {
			d.dayOpen = b.O
		}
	}
	d.cs, d.cema = candles5(bars)
	return d
}

// burstAt is the volume of bar i over the mean of the 9 bars before it (NaN if unknown).
func burstAt(bars []core.Bar, i int) float64 {
	if i < 9 {
		return math.NaN()
	}
	var s float64
	for j := i - 9; j < i; j++ {
		s += float64(bars[j].V)
	}
	if !(s > 0) {
		return math.NaN()
	}
	return float64(bars[i].V) / (s / 9)
}

// belowRun counts the entries immediately before `from` with d <= 0, stopping at
// a missing d.
func belowRun(dd []float64, from int) int {
	n := 0
	for j := from - 1; j >= 0; j-- {
		if math.IsNaN(dd[j]) || !(dd[j] <= 0) {
			break
		}
		n++
	}
	return n
}

// build fills one signal evaluated at 1-minute bar i (the cross bar, or the
// candle's last 1-minute bar) with the cross candle's low / close. trend5Ms is
// the instant at which the last completed 5m candle is looked up.
func (d *driftDay) build(typ, sym string, i int, crossLow, crossClose, vwap, rvol float64, crossN, belowMin int, trend5Ms int64) (DriftSignal, bool) {
	e := i + 1
	if e >= len(d.bars) || ETDate(d.bars[e].T) != d.date || ETMinute(d.bars[e].T) >= 15*60+59 {
		return DriftSignal{}, false
	}
	ret, units, mfe, mae, riskPct, ok := DriftForward(d.bars, e, crossLow)
	if !ok {
		return DriftSignal{}, false
	}
	s := DriftSignal{
		Type: typ, Date: d.date, Sym: sym, CrossTs: d.bars[i].T, EntryTs: d.bars[e].T, Entry: d.bars[e].O,
		CrossLow: crossLow, CrossClose: crossClose, Ret: ret, Units: units, MFE: mfe, MAE: mae, RiskPct: riskPct,
		Rvol: rvol, Burst: burstAt(d.bars, i), CrossN: crossN, BelowMin: belowMin, EntryMin: ETMinute(d.bars[e].T),
		GapPct: math.NaN(), DayChgPct: math.NaN(), DistPct: math.NaN(),
	}
	if !math.IsNaN(d.prevClose) && d.prevClose > 0 {
		if !math.IsNaN(d.dayOpen) {
			s.GapPct = 100 * (d.dayOpen/d.prevClose - 1)
		}
		s.DayChgPct = 100 * (crossClose/d.prevClose - 1)
	}
	if vwap > 0 {
		s.DistPct = 100 * (crossClose - vwap) / vwap
	}
	s.Tod = todBucket(s.EntryMin)
	s.Gap, s.DayChg = pctBucket(s.GapPct), pctBucket(s.DayChgPct)
	s.RvolB, s.BurstB, s.CrossB = rvolBucket(rvol), burstBucket(s.Burst), crossNoBucket(crossN)
	s.BelowB, s.DistB, s.PriceB = belowBucket(belowMin), distBucket(s.DistPct), priceBucket(crossClose)
	s.Trend5 = yesNo(trend5mOK(d.cs, d.cema, trend5Ms))
	s.AboveOpen = yesNo(!math.IsNaN(d.dayOpen) && crossClose > d.dayOpen)
	return s, true
}

// DriftDay returns every drift signal of one symbol-day for the three signal
// types, ordered intrabar, close, 5m and by time within a type. bars are the
// previous day + the day, ascending, 1-minute, extended hours.
func DriftDay(sym, date string, adv10 float64, bars []core.Bar, p VwapParams) []DriftSignal {
	if len(bars) == 0 {
		return nil
	}
	d := newDriftDay(bars, date, p)
	var out []DriftSignal
	for _, v := range []string{VwIntrabar, VwClose} {
		out = append(out, d.minuteSignals(sym, adv10, v, p)...)
	}
	out = append(out, d.fiveSignals(sym, adv10, p)...)
	markFirst(out)
	return out
}

func markFirst(sigs []DriftSignal) {
	seen := map[string]bool{}
	for i := range sigs {
		if !seen[sigs[i].Type] {
			seen[sigs[i].Type] = true
			sigs[i].First = true
		}
	}
}

// dSeries is d = (ema-vwap)/vwap per bar (NaN where unjudged).
func (d *driftDay) dSeries() []float64 {
	dd := make([]float64, len(d.bars))
	for i, b := range d.bars {
		dd[i] = math.NaN()
		if ETDate(b.T) != d.date {
			continue
		}
		m := ETMinute(b.T)
		if m < vwSessionOpen || m >= vwSessionClose {
			continue
		}
		ema, vwap := d.ser.Ema[i], d.ser.Vwap[i]
		if math.IsNaN(ema) || math.IsNaN(vwap) || !(vwap > 0) {
			continue
		}
		dd[i] = (ema - vwap) / vwap
	}
	return dd
}

// minuteSignals mirrors SimulateVwapCrossDay's detection without positions.
func (d *driftDay) minuteSignals(sym string, adv10 float64, variant string, p VwapParams) []DriftSignal {
	dd := d.dSeries()
	var out []DriftSignal
	var (
		have     bool
		prevD    float64
		armed    = true
		firedIdx = -1
		rawArmed = true
		rawIdx   = -1
		rawN     int
	)
	for i, b := range d.bars {
		if math.IsNaN(dd[i]) {
			continue
		}
		min := ETMinute(b.T)
		cur := dd[i]
		if !have {
			have, prevD = true, cur
			continue
		}
		geom := false
		if prevD <= 0 && i > 0 && !math.IsNaN(d.ser.Ema[i-1]) {
			if variant == VwIntrabar {
				geom = b.H*p.K+d.ser.Ema[i-1]*(1-p.K) > d.ser.Vwap[i]
			} else {
				geom = cur > 0
			}
		}
		if rawArmed && geom {
			rawN++
			rawArmed, rawIdx = false, i
		}
		if armed && geom {
			rvol := 0.0
			if adv10 > 0 {
				rvol = d.cumVol[i] / (adv10 * CurveFrac(float64(min+1)))
			}
			if VwUniverseReject(p, b.C, adv10, b.C*float64(b.V), d.cumVol[i], rvol) == "" {
				armed, firedIdx = false, i
				sig := b.T // an intrabar poke happens inside the bar: only candles complete by its start count
				if variant == VwClose {
					sig += minuteMs
				}
				// rawN counts this bar's raw cross only when it was armed (rawN >= 1 here: a
				// signal bar that raw-disarmed earlier still reports the running count).
				cn := rawN
				if cn < 1 {
					cn = 1
				}
				if s, ok := d.build(variant, sym, i, b.L, b.C, d.ser.Vwap[i], rvol, cn, belowRun(dd, i), sig); ok {
					out = append(out, s)
				}
			}
		}
		if !armed && i > firedIdx && cur < 0 {
			armed = true
		}
		if !rawArmed && i > rawIdx && cur < 0 {
			rawArmed = true
		}
		prevD = cur
	}
	return out
}

// c5 is one regular-hours 5-minute candle of the 5m signal.
type c5 struct {
	date    string
	end     int64 // ms: start of the next 5-minute bucket
	lastIdx int   // index (into bars) of the candle's last 1-minute bar
	low     float64
	close   float64
}

// regular5m builds the clock-aligned regular-hours candles of the bars.
func regular5m(bars []core.Bar) []c5 {
	const w = 5 * minuteMs
	var cs []c5
	var key int64 = -1
	for i, b := range bars {
		m := ETMinute(b.T)
		if m < vwSessionOpen || m >= vwSessionClose {
			continue
		}
		if k := b.T / w; k == key {
			c := &cs[len(cs)-1]
			c.lastIdx, c.close, c.low = i, b.C, math.Min(c.low, b.L)
		} else {
			key = k
			cs = append(cs, c5{date: ETDate(b.T), end: (k + 1) * w, lastIdx: i, low: b.L, close: b.C})
		}
	}
	return cs
}

// fiveSignals: 9 EMA of the 5m closes crossing above the regular-hours VWAP at a
// completed candle. Nothing of the forming candle is read: every quantity of a
// candle is taken at or before its last 1-minute bar.
func (d *driftDay) fiveSignals(sym string, adv10 float64, p VwapParams) []DriftSignal {
	cs := regular5m(d.bars)
	cl := make([]float64, len(cs))
	for i, c := range cs {
		cl[i] = c.close
	}
	ema := emaSeries(cl, p.EmaPeriod)
	dd := make([]float64, len(cs))
	for i, c := range cs {
		dd[i] = math.NaN()
		if c.date != d.date || math.IsNaN(ema[i]) {
			continue
		}
		if vw := d.ser.Vwap[c.lastIdx]; vw > 0 && !math.IsNaN(vw) {
			dd[i] = (ema[i] - vw) / vw
		}
	}
	var out []DriftSignal
	var (
		have     bool
		prevD    float64
		armed    = true
		firedIdx = -1
		rawArmed = true
		rawIdx   = -1
		rawN     int
	)
	for i, c := range cs {
		cur := dd[i]
		if math.IsNaN(cur) {
			continue
		}
		if !have {
			have, prevD = true, cur
			continue
		}
		geom := prevD <= 0 && cur > 0
		if rawArmed && geom {
			rawN++
			rawArmed, rawIdx = false, i
		}
		if armed && geom {
			b := d.bars[c.lastIdx]
			min := ETMinute(b.T)
			rvol := 0.0
			if adv10 > 0 {
				rvol = d.cumVol[c.lastIdx] / (adv10 * CurveFrac(float64(min+1)))
			}
			if VwUniverseReject(p, b.C, adv10, b.C*float64(b.V), d.cumVol[c.lastIdx], rvol) == "" {
				armed, firedIdx = false, i
				below := 0
				for j := i - 1; j >= 0 && !math.IsNaN(dd[j]) && dd[j] <= 0; j-- {
					below += 5
				}
				cn := rawN
				if cn < 1 {
					cn = 1
				}
				if s, ok := d.build(Drift5m, sym, c.lastIdx, c.low, c.close, d.ser.Vwap[c.lastIdx], rvol, cn, below, c.end); ok {
					out = append(out, s)
				}
			}
		}
		if !armed && i > firedIdx && cur < 0 {
			armed = true
		}
		if !rawArmed && i > rawIdx && cur < 0 {
			rawArmed = true
		}
		prevD = cur
	}
	return out
}

// SortDriftSignals orders signals deterministically.
func SortDriftSignals(s []DriftSignal) {
	rank := map[string]int{VwIntrabar: 0, VwClose: 1, Drift5m: 2}
	sort.SliceStable(s, func(i, j int) bool {
		a, b := s[i], s[j]
		if rank[a.Type] != rank[b.Type] {
			return rank[a.Type] < rank[b.Type]
		}
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		if a.Sym != b.Sym {
			return a.Sym < b.Sym
		}
		return a.CrossTs < b.CrossTs
	})
}
