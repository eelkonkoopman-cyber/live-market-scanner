package backtest

import (
	"math"
	"sort"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// SPLITS. The feed hands over bars with adjusted=true: every price and volume of
// a name is restated as of TODAY for the splits executed since. A penny stock
// that reverse-split after the day being replayed therefore shows up at a
// multiple of the price the live trader saw, with a fraction of the shares - and
// everything that depends on the price level goes wrong with it: per-share
// commissions and fees shrink in proportion (a $2000 position costs ~$67 round
// trip at $0.30 and ~$3.7 at the adjusted $6.00), the $0.20-$50 price filter
// admits a real $0.15 trigger as $3.00, and the tick size changes across $1.
// Names that later reverse-split are mostly decliners, so the bias is one-sided.
//
// So the pipeline puts every name back on the basis of the day being replayed
// (the "as-of-day" basis: splits executed up to and including the day are in the
// data, later ones are undone) BEFORE the prefilter's price test, admission,
// sizing, costs and the price filter. One factor per (name, day) does it for the
// whole candidate: the minute bars of both days and the grouped daily rows used
// for adv10 and the previous close.

// StockSplit is one corporate split from the reference data.
type StockSplit struct {
	Sym  string  `json:"sym"`
	Date string  `json:"date"` // execution date, "2006-01-02": the first day trading on the new basis
	From float64 `json:"from"` // split_from: 20 in a 1-for-20 reverse split, 1 in a 2-for-1 forward split
	To   float64 `json:"to"`   // split_to: 1 in a 1-for-20 reverse split, 2 in a 2-for-1 forward split
}

func (s StockSplit) valid() bool {
	fin := func(v float64) bool { return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }
	return s.Sym != "" && s.Date != "" && fin(s.From) && fin(s.To)
}

// SplitIndex answers "by what factor did splits after this day multiply the
// adjusted history of this name".
type SplitIndex struct {
	bySym map[string][]StockSplit // ascending by date
	n     int
}

// NewSplitIndex indexes splits; rows that are not usable (no ticker or date, a
// ratio that is not positive and finite) are dropped.
func NewSplitIndex(splits []StockSplit) *SplitIndex {
	x := &SplitIndex{bySym: map[string][]StockSplit{}}
	for _, s := range splits {
		if !s.valid() {
			continue
		}
		x.bySym[s.Sym] = append(x.bySym[s.Sym], s)
		x.n++
	}
	for _, l := range x.bySym {
		sort.SliceStable(l, func(i, j int) bool { return l[i].Date < l[j].Date })
	}
	return x
}

// Len is the number of usable splits.
func (x *SplitIndex) Len() int {
	if x == nil {
		return 0
	}
	return x.n
}

// Factor is the product of From/To over the splits of sym executed AFTER date
// (strictly: a split executed on the day itself is already in that day's data).
// It is the factor the adjusted feed multiplied the day's prices by, and divided
// its volumes by: a 1-for-20 reverse split (from 20, to 1) after the day gives
// 20, so the real price is the adjusted price / 20 and the real volume the
// adjusted volume * 20; a 2-for-1 forward split gives 0.5. 1 when there is none.
func (x *SplitIndex) Factor(sym, date string) float64 {
	if x == nil {
		return 1
	}
	f := 1.0
	for _, s := range x.bySym[sym] {
		if s.Date > date { // ISO dates order as strings
			f *= s.From / s.To
		}
	}
	return f
}

// UnadjustBars returns bars on the basis before splits that multiplied the
// adjusted prices by factor: prices / factor, volumes * factor (rounded to a
// whole share). The input is not modified. A factor of 1, or one that is not
// positive and finite, returns the bars as they are.
func UnadjustBars(bars []core.Bar, factor float64) []core.Bar {
	if factor == 1 || !(factor > 0) || math.IsInf(factor, 0) || len(bars) == 0 {
		return bars
	}
	out := make([]core.Bar, len(bars))
	for i, b := range bars {
		out[i] = core.Bar{
			T: b.T, O: b.O / factor, H: b.H / factor, L: b.L / factor, C: b.C / factor,
			V: int64(math.Round(float64(b.V) * factor)),
		}
	}
	return out
}

// splitFactor is the candidate's factor, 1 when it carries none.
func (c *Candidate) splitFactor() float64 {
	if f := c.SplitFactor; f > 0 && !math.IsInf(f, 0) {
		return f
	}
	return 1
}
