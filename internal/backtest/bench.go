package backtest

import (
	"math"
	"sort"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// Bench is the market benchmark (SPY by default): its 1-minute bars per test day.
// Every trade and drift signal is compared with the benchmark over the SAME
// minutes: entry = the open of the benchmark bar that starts at the entry
// timestamp, exit = the close of the last benchmark bar starting at or before the
// exit timestamp (the exit bar's close for a bar exit). A trade without benchmark
// bars on those minutes has no excess return and is counted as missing.
type Bench struct {
	Sym  string
	days map[string][]core.Bar
	prev map[string]float64   // date -> last regular-hours close before the day (absent if none)
	vwap map[string][]float64 // date -> regular-hours VWAP through each bar (NaN outside regular hours)
}

// NewBench returns an empty benchmark for sym.
func NewBench(sym string) *Bench {
	return &Bench{Sym: sym, days: map[string][]core.Bar{}, prev: map[string]float64{}, vwap: map[string][]float64{}}
}

// Add stores the regular-session bars of one day (only that day's bars are kept,
// ascending).
func (b *Bench) Add(date string, bars []core.Bar) {
	var day []core.Bar
	var prevT int64
	var prevC float64
	for _, x := range bars {
		if ETDate(x.T) == date {
			day = append(day, x)
		} else if m := ETMinute(x.T); ETDate(x.T) < date && m >= vwSessionOpen && m < vwSessionClose && x.T > prevT {
			prevT, prevC = x.T, x.C
		}
	}
	sort.SliceStable(day, func(i, j int) bool { return day[i].T < day[j].T })
	if len(day) > 0 {
		b.days[date] = day
		if prevC > 0 {
			b.prev[date] = prevC
		}
		vw := make([]float64, len(day))
		var pv, vv float64
		for i, x := range day {
			vw[i] = math.NaN()
			if m := ETMinute(x.T); m < vwSessionOpen || m >= vwSessionClose {
				continue
			}
			pv += (x.H + x.L + x.C) / 3 * float64(x.V)
			vv += float64(x.V)
			if vv > 0 {
				vw[i] = pv / vv
			}
		}
		b.vwap[date] = vw
	}
}

// Has reports whether the day has benchmark bars.
func (b *Bench) Has(date string) bool {
	if b == nil {
		return false
	}
	return len(b.days[date]) > 0
}

// NDays is the number of days with bars.
func (b *Bench) NDays() int {
	if b == nil {
		return 0
	}
	return len(b.days)
}

func (b *Bench) at(date string, ts int64) (bars []core.Bar, i int, ok bool) {
	if b == nil {
		return nil, 0, false
	}
	bars = b.days[date]
	i = sort.Search(len(bars), func(k int) bool { return bars[k].T >= ts })
	return bars, i, i < len(bars) && bars[i].T == ts
}

// TradeRet is the benchmark return in percent from the open of the bar starting
// at entryTs to the close of the last bar starting at or before exitTs.
func (b *Bench) TradeRet(date string, entryTs, exitTs int64) (float64, bool) {
	bars, i, ok := b.at(date, entryTs)
	if !ok || !(bars[i].O > 0) || exitTs < entryTs {
		return 0, false
	}
	j := sort.Search(len(bars), func(k int) bool { return bars[k].T > exitTs }) - 1
	if j < i {
		return 0, false
	}
	return 100 * (bars[j].C/bars[i].O - 1), true
}

// DriftRet is the benchmark's forward return at the five drift horizons from the
// bar starting at entryTs (the same rules as DriftForward).
func (b *Bench) DriftRet(date string, entryTs int64) ([5]float64, bool) {
	bars, i, ok := b.at(date, entryTs)
	if !ok {
		return [5]float64{}, false
	}
	ret, _, _, _, _, ok := DriftForward(bars, i, math.NaN())
	return ret, ok
}

// AttachTrades sets BenchOK, BenchRet and the excess returns of every trade.
func (b *Bench) AttachTrades(ts []VwTrade) {
	for i := range ts {
		t := &ts[i]
		t.BenchOK = false
		if br, ok := b.TradeRet(t.Date, t.EntryTs, t.ExitTs); ok {
			t.BenchOK, t.BenchRet = true, br
			t.Excess = t.RetPct - br
			t.ExcessNet = TradeNetPct(*t) - br
		}
	}
}

// AttachDrift sets the benchmark and excess returns of every drift signal.
func (b *Bench) AttachDrift(sigs []DriftSignal) {
	for i := range sigs {
		s := &sigs[i]
		if br, ok := b.DriftRet(s.Date, s.EntryTs); ok {
			s.BenchOK, s.BenchRet = true, br
			for h := range br {
				s.Excess[h] = s.Ret[h] - br[h] // NaN if either is NaN
			}
		}
	}
}

// TradeNetPct is the net P&L of a sized trade in percent of its entry notional.
func TradeNetPct(t VwTrade) float64 {
	if !t.Sized || t.Shares < 1 || !(t.Entry > 0) {
		return 0
	}
	return 100 * t.NetUSD / (float64(t.Shares) * t.Entry)
}

// ExcessStat is the mean excess return (percent) over the trades that have
// benchmark data, its t-statistic (mean/(sd/sqrt(n))) and how many were left out.
type ExcessStat struct {
	N       int     `json:"n"`
	Missing int     `json:"missing"`
	Mean    float64 `json:"meanExcessPct"`
	T       float64 `json:"t"`
	MeanNet float64 `json:"meanExcessNetPct"`
	MeanBen float64 `json:"meanBenchPct"`
}

// TradeExcess summarises the excess of the trades (BenchOK ones only).
func TradeExcess(ts []VwTrade) ExcessStat {
	var ex, exn, ben []float64
	var e ExcessStat
	for _, t := range ts {
		if !t.BenchOK {
			e.Missing++
			continue
		}
		ex, exn, ben = append(ex, t.Excess), append(exn, t.ExcessNet), append(ben, t.BenchRet)
	}
	st := DriftStat(ex)
	e.N, e.Mean, e.T = st.N, finite(st.Mean), finite(st.T)
	e.MeanNet, e.MeanBen = finite(DriftStat(exn).Mean), finite(DriftStat(ben).Mean)
	return e
}

// MaxDrawdownR is the largest peak-to-trough fall of the cumulative net-R curve
// (trades in exit-time order, the curve starting at 0 so a losing start counts).
func MaxDrawdownR(ts []VwTrade) float64 {
	o := append([]VwTrade(nil), ts...)
	sort.SliceStable(o, func(i, j int) bool {
		a, b := o[i], o[j]
		if a.ExitTs != b.ExitTs {
			return a.ExitTs < b.ExitTs
		}
		if a.EntryTs != b.EntryTs {
			return a.EntryTs < b.EntryTs
		}
		return a.Sym < b.Sym
	})
	var cum, peak, dd float64
	for _, t := range o {
		cum += t.RNet
		peak = math.Max(peak, cum)
		dd = math.Max(dd, peak-cum)
	}
	return dd
}

// WorstDayR is the lowest daily sum of net R and its date; days is the number of
// distinct days with a trade.
func WorstDayR(ts []VwTrade) (worst float64, date string, days int) {
	by := map[string]float64{}
	for _, t := range ts {
		by[t.Date] += t.RNet
	}
	first := true
	for d, v := range by {
		if first || v < worst || (v == worst && d < date) {
			worst, date, first = v, d, false
		}
	}
	return worst, date, len(by)
}
