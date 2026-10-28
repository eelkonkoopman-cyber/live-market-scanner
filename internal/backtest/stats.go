package backtest

import (
	"math"
	"math/rand/v2"
	"sort"
)

// StatOpts are the statistics' knobs.
type StatOpts struct {
	Resamples int    // bootstrap resamples (10,000)
	Seed      uint64 // the bootstrap's seed: the same trades give the same interval
}

// DefaultStatOpts is the task spec's bootstrap.
func DefaultStatOpts() StatOpts { return StatOpts{Resamples: 10_000, Seed: 20261006} }

// Summary is the headline statistics of a set of trades over a set of days.
// An undefined number (no trades, no losing trade for a profit factor) is zero,
// or nil for the profit factor: never NaN, which JSON cannot carry.
type Summary struct {
	Label  string `json:"label"`
	Days   int    `json:"days"`   // trading days in the set (days with and without a trade)
	Trades int    `json:"trades"` // trades in the set
	Wins   int    `json:"wins"`   // net > 0

	WinRate  float64 `json:"winRate"`
	AvgNet   float64 `json:"avgNetUSD"`   // $ per trade, after costs
	AvgGross float64 `json:"avgGrossUSD"` // $ per trade, before costs
	MedNet   float64 `json:"medianNetUSD"`
	MedGross float64 `json:"medianGrossUSD"`
	AvgRNet  float64 `json:"avgRNet"` // R = P&L / (shares * (trigger - stop))
	AvgRGros float64 `json:"avgRGross"`

	TotalNet   float64 `json:"totalNetUSD"`
	TotalGross float64 `json:"totalGrossUSD"`
	Commission float64 `json:"commissionUSD"` // commissions + fees, both sides
	Slippage   float64 `json:"slippageUSD"`

	// 95% bootstrap interval of the MEAN $ per trade (resampling trades).
	NetCILo   float64 `json:"netCI95Lo"`
	NetCIHi   float64 `json:"netCI95Hi"`
	GrossCILo float64 `json:"grossCI95Lo"`
	GrossCIHi float64 `json:"grossCI95Hi"`

	ProfitFactor *float64 `json:"profitFactor"` // net: gross wins / gross losses; nil with no losing trade

	MaxDrawdown float64 `json:"maxDrawdownUSD"` // of the daily net equity curve, peak to trough (>= 0)
	BestDay     float64 `json:"bestDayUSD"`     // among days WITH a trade
	BestDayDate string  `json:"bestDay"`
	WorstDay    float64 `json:"worstDayUSD"`
	WorstDayDt  string  `json:"worstDay"`

	DaysWithTrade    int     `json:"daysWithTrade"`
	PctDaysWithTrade float64 `json:"pctDaysWithTrade"` // 0..100, of all days in the set
	AvgTradesPerDay  float64 `json:"avgTradesPerDay"`  // over all days in the set

	// The same question asked of DAYS (trades inside a day are not independent):
	// the mean net $ per trading day (zero for a day with no trade) and its 95%
	// bootstrap interval, resampling days.
	DayMeanNet float64 `json:"dayMeanNetUSD"`
	DayCILo    float64 `json:"dayNetCI95Lo"`
	DayCIHi    float64 `json:"dayNetCI95Hi"`
}

// Summarize computes the statistics of trades over days (every trading day of
// the set, ascending, trades or not). Trades outside days are ignored by the
// day statistics but still counted: pass the matching sets.
func Summarize(label string, trades []Trade, days []string, o StatOpts) Summary {
	s := Summary{Label: label, Days: len(days), Trades: len(trades)}
	net := make([]float64, 0, len(trades))
	gross := make([]float64, 0, len(trades))
	var winSum, lossSum, rNet, rGross float64
	perDay := map[string]float64{}
	perDayN := map[string]int{}
	for _, t := range trades {
		net = append(net, t.Net)
		gross = append(gross, t.Gross)
		s.TotalNet += t.Net
		s.TotalGross += t.Gross
		s.Commission += t.Commission + t.Fees
		s.Slippage += t.Slippage
		rNet += t.RNet
		rGross += t.RGross
		if t.Net > 0 {
			s.Wins++
			winSum += t.Net
		} else if t.Net < 0 {
			lossSum += -t.Net
		}
		perDay[t.Date] += t.Net
		perDayN[t.Date]++
	}
	if n := len(trades); n > 0 {
		fn := float64(n)
		s.WinRate = float64(s.Wins) / fn
		s.AvgNet, s.AvgGross = s.TotalNet/fn, s.TotalGross/fn
		s.MedNet, s.MedGross = median(net), median(gross)
		s.AvgRNet, s.AvgRGros = rNet/fn, rGross/fn
		s.NetCILo, s.NetCIHi = BootstrapMeanCI(net, o.Resamples, o.Seed)
		s.GrossCILo, s.GrossCIHi = BootstrapMeanCI(gross, o.Resamples, o.Seed)
		if lossSum > 0 {
			pf := winSum / lossSum
			s.ProfitFactor = &pf
		}
	}

	// Daily equity (every day of the set, in order) and the day-level numbers.
	daily := make([]float64, len(days))
	var eq, peak float64
	first := true
	for i, d := range days {
		daily[i] = perDay[d]
		eq += daily[i]
		if eq > peak {
			peak = eq
		}
		if dd := peak - eq; dd > s.MaxDrawdown {
			s.MaxDrawdown = dd
		}
		if perDayN[d] > 0 {
			s.DaysWithTrade++
			if first || daily[i] > s.BestDay {
				s.BestDay, s.BestDayDate = daily[i], d
			}
			if first || daily[i] < s.WorstDay {
				s.WorstDay, s.WorstDayDt = daily[i], d
			}
			first = false
		}
	}
	if len(days) > 0 {
		s.PctDaysWithTrade = 100 * float64(s.DaysWithTrade) / float64(len(days))
		s.AvgTradesPerDay = float64(len(trades)) / float64(len(days))
		var tot float64
		for _, v := range daily {
			tot += v
		}
		s.DayMeanNet = tot / float64(len(days))
		s.DayCILo, s.DayCIHi = BootstrapMeanCI(daily, o.Resamples, o.Seed)
	}
	return s
}

// BootstrapMeanCI is the 95% percentile-bootstrap interval of the mean of xs:
// `resamples` resamples with replacement, each the size of xs, drawn from a PCG
// seeded with seed - so the same data and seed always give the same interval,
// whatever else ran before. (0, 0) for no data.
func BootstrapMeanCI(xs []float64, resamples int, seed uint64) (lo, hi float64) {
	n := len(xs)
	if n == 0 || resamples <= 0 {
		return 0, 0
	}
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	means := make([]float64, resamples)
	fn := float64(n)
	for r := 0; r < resamples; r++ {
		var sum float64
		for i := 0; i < n; i++ {
			sum += xs[rng.IntN(n)]
		}
		means[r] = sum / fn
	}
	sort.Float64s(means)
	return means[int(0.025*float64(resamples))], means[min(resamples-1, int(0.975*float64(resamples)))]
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	c := append([]float64(nil), xs...)
	sort.Float64s(c)
	n := len(c)
	if n%2 == 1 {
		return c[n/2]
	}
	return (c[n/2-1] + c[n/2]) / 2
}

// ─── Splits ──────────────────────────────────────────────────────────────

// SplitRow is one bucket of a split.
type SplitRow struct {
	Label    string  `json:"label"`
	Trades   int     `json:"trades"`
	WinRate  float64 `json:"winRate"`
	AvgNet   float64 `json:"avgNetUSD"`
	AvgGross float64 `json:"avgGrossUSD"`
	TotalNet float64 `json:"totalNetUSD"`
	AvgRNet  float64 `json:"avgRNet"`
}

// Splits is the three breakdowns of the task.
type Splits struct {
	Price []SplitRow `json:"byPrice"`
	Time  []SplitRow `json:"byEntryTime"`
	Month []SplitRow `json:"byMonth"`
}

// Price bands (by the trigger) and entry-time buckets (by the entry bar's ET
// minute), in display order.
var (
	PriceBands  = []string{"under $1", "$1 - $5", "$5 - $50"}
	TimeBuckets = []string{"04:00-07:00", "07:00-09:30", "09:30-11:00", "11:00-15:45"}
)

// PriceBand is the band of a trade's trigger price.
func PriceBand(t Trade) string {
	switch {
	case t.Trigger < 1:
		return PriceBands[0]
	case t.Trigger < 5:
		return PriceBands[1]
	}
	return PriceBands[2]
}

// TimeBucket is the entry-time bucket of a trade (the last one is open-ended:
// the window may reach the flatten time).
func TimeBucket(t Trade) string {
	switch {
	case t.EntryMin < 7*60:
		return TimeBuckets[0]
	case t.EntryMin < 9*60+30:
		return TimeBuckets[1]
	case t.EntryMin < 11*60:
		return TimeBuckets[2]
	}
	return TimeBuckets[3]
}

// MonthKey is the trade's month ("2026-09").
func MonthKey(t Trade) string {
	if len(t.Date) >= 7 {
		return t.Date[:7]
	}
	return t.Date
}

// Split buckets trades by key. labels fixes the rows (and their order); nil
// derives them, sorted, from the trades.
func Split(trades []Trade, labels []string, key func(Trade) string) []SplitRow {
	if labels == nil {
		seen := map[string]bool{}
		for _, t := range trades {
			if k := key(t); !seen[k] {
				seen[k] = true
				labels = append(labels, k)
			}
		}
		sort.Strings(labels)
	}
	rows := make([]SplitRow, len(labels))
	idx := map[string]int{}
	for i, l := range labels {
		rows[i].Label = l
		idx[l] = i
	}
	wins := make([]int, len(labels))
	var rsum = make([]float64, len(labels))
	var gsum = make([]float64, len(labels))
	for _, t := range trades {
		i, ok := idx[key(t)]
		if !ok {
			continue
		}
		rows[i].Trades++
		rows[i].TotalNet += t.Net
		gsum[i] += t.Gross
		rsum[i] += t.RNet
		if t.Net > 0 {
			wins[i]++
		}
	}
	for i := range rows {
		if n := rows[i].Trades; n > 0 {
			fn := float64(n)
			rows[i].WinRate = float64(wins[i]) / fn
			rows[i].AvgNet = rows[i].TotalNet / fn
			rows[i].AvgGross = gsum[i] / fn
			rows[i].AvgRNet = rsum[i] / fn
		}
	}
	return rows
}

// SplitAll computes the three splits of trades.
func SplitAll(trades []Trade) Splits {
	return Splits{
		Price: Split(trades, PriceBands, PriceBand),
		Time:  Split(trades, TimeBuckets, TimeBucket),
		Month: Split(trades, nil, MonthKey),
	}
}

// finite guards a number going into JSON.
func finite(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}
