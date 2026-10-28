package backtest

import (
	"math"
	"sort"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// The prefilter exists only to decide WHICH (symbol, day) pairs are worth
// fetching 1-minute bars for: the whole market is ~12k tickers a day, and one
// minute-bar request per ticker-day is out of the question. It is a documented
// APPROXIMATION of "could the live trader have been watching this name", and the
// one rule it must obey is that no test may read the day's OUTCOME: a screen
// that keeps a name only because it later ran drops the identical setup that
// failed, and the expected value of what is left is biased upward. (The first
// version tested "day high >= yesterday's close * 1.05"; a gap-down name with
// huge pre-market volume that calls a losing LONG (FLAG) never trades 5% over
// yesterday's close, was never fetched, and its twin that ran was.) So every test
// below is a NECESSARY condition of a trade that the grouped daily bars can
// check without hindsight:
//
//   - price: the day's range must reach the trader's price filter [MinPrice,
//     MaxPrice]: high >= MinPrice and low <= MaxPrice, each with PriceMarginPct of
//     slack (the trigger sits 0.3% over the flag high, and the grouped bar may not
//     carry every extended-hours print). This replaces "yesterday's close in the
//     range", which dropped a $0.18 name that ran to $0.45 - and the trader
//     filters on the TRIGGER price, not yesterday's close;
//   - dayVol >= adv10: a NECESSARY condition of admission - the grouped daily
//     volume includes extended hours, so a name whose pre-market volume reached
//     1x its 10-day average has a whole-day volume of at least 1x too; dropping
//     the rest loses nothing;
//   - impulse bound: the flag needs an impulse CLOSE 5% over the 9 EMA. The EMA
//     is an SMA seed followed by convex combinations of closes, so it is never
//     under the lowest close of the two-day bar array, and an impulse close is at
//     most the day's high: high >= MinSpike * (lowest low of yesterday and today).
//     Unlike "5% over yesterday's close" this holds for every real candidate, gap
//     down or up. SpikeMarginPct shaves the threshold for what the grouped bar
//     can miss (extended-hours extremes, rounding). NOTE the margin is small by
//     design: with margin m the threshold is MinSpike*(1-m/100) times the low, and
//     since the day's high is never under the day's low a margin of 4.8% or more
//     (1.05*(1-0.048) < 1) would make the test vacuous;
//   - at most Cap names per day, strongest dayVol/adv10 first. The whole-day
//     volume ratio also grows when a move succeeds, so when the cap binds it can
//     still favour winners: the counts say on how many days it bound, and a run
//     with a larger -maxcand is the sensitivity check.
//
// The counts of every stage are returned, never swallowed.
//
// SPLITS. The feed's bars are split-adjusted as of today. SplitFactor, when set,
// gives each name's factor for the day (SplitIndex.Factor) and the prefilter
// works on the basis the trader saw: prices / factor, volumes * factor.

// PrefilterOpts are the prefilter's knobs.
type PrefilterOpts struct {
	MinPrice       float64 // 0.20
	MaxPrice       float64 // 50
	MinSpike       float64 // 1.05: day high >= this * the lowest low of yesterday and today (less the margin)
	SpikeMarginPct float64 // 2: slack on the impulse bound (see above)
	PriceMarginPct float64 // 1: slack on the price-range overlap
	Cap            int     // 150 names a day (<= 0: no cap)
	MinHist        int     // 5: days of volume needed for an adv10 (the average is over up to 10)

	// SplitFactor returns the factor by which adjusted history of sym was multiplied
	// by splits executed AFTER the day (1 = none); nil means no splits. Not part of
	// the report.
	SplitFactor func(sym string) float64 `json:"-"`
}

// DefaultPrefilter is the task spec's prefilter, with the outcome-free tests.
func DefaultPrefilter() PrefilterOpts {
	return PrefilterOpts{MinPrice: 0.20, MaxPrice: 50, MinSpike: 1.05, SpikeMarginPct: 2, PriceMarginPct: 1, Cap: 150, MinHist: 5}
}

// PrefCand is one name that passed the prefilter for one day. Prices and volumes
// are on the basis the trader saw (split-un-adjusted when a split factor is set).
type PrefCand struct {
	Sym         string
	Adv10       float64 // mean daily volume of the (up to) 10 trading days before the day
	PrevClose   float64
	DayVol      float64
	DayHigh     float64
	Ratio       float64 // DayVol / Adv10, the rank key
	SplitFactor float64 // 1, or the factor of splits executed after the day (see SplitIndex.Factor)
}

// PrefilterStats counts every stage of one day's prefilter.
type PrefilterStats struct {
	Date       string
	Universe   int // tickers with a daily bar that day
	NoPrev     int // no bar the previous trading day (new listing): no prevClose
	NoAdv      int // fewer than MinHist days of history (or no volume): no adv10
	OutOfPrice int // the day's range never reached [MinPrice, MaxPrice]
	LowVolume  int // dayVol < adv10
	NoSpike    int // high < MinSpike * the lowest low of yesterday and today (less the margin): no 5% impulse was possible
	Passed     int // passed every test (before the cap)
	Kept       int // after the cap
	Dropped    int // Passed - Kept: lost to the cap
}

// ADV10 is the mean daily volume of sym over hist (the up-to-10 trading days
// BEFORE the day in question, any order). ok is false when fewer than minDays of
// them carry the symbol, or the mean is not positive. The grouped daily volume
// includes extended hours - the same definition as the scanner's adv10.
func ADV10(sym string, hist []map[string]core.DailyBar, minDays int) (float64, bool) {
	var sum float64
	n := 0
	for _, day := range hist {
		if b, ok := day[sym]; ok {
			sum += float64(b.V)
			n++
		}
	}
	if n < minDays || n == 0 {
		return 0, false
	}
	avg := sum / float64(n)
	return avg, avg > 0
}

// Prefilter selects the day's names. today is the day's grouped daily bars,
// hist the up-to-10 PREVIOUS trading days' (ascending: the last one is the
// previous trading day, whose close is prevClose).
func Prefilter(date string, today map[string]core.DailyBar, hist []map[string]core.DailyBar, o PrefilterOpts) ([]PrefCand, PrefilterStats) {
	st := PrefilterStats{Date: date, Universe: len(today)}
	var prev map[string]core.DailyBar
	if len(hist) > 0 {
		prev = hist[len(hist)-1]
	}
	pm := o.PriceMarginPct / 100
	sm := o.SpikeMarginPct / 100
	var out []PrefCand
	for sym, b := range today {
		pb, ok := prev[sym]
		if !ok || !(pb.C > 0) {
			st.NoPrev++
			continue
		}
		adv, ok := ADV10(sym, hist, o.MinHist)
		if !ok {
			st.NoAdv++
			continue
		}
		// the basis the trader saw: undo splits executed after the day
		f := 1.0
		if o.SplitFactor != nil {
			if v := o.SplitFactor(sym); v > 0 && !math.IsInf(v, 0) {
				f = v
			}
		}
		prevC, prevL := pb.C/f, pb.L/f
		hi, lo, vol := b.H/f, b.L/f, float64(b.V)*f
		if !(hi*(1+pm) >= o.MinPrice && (!(lo > 0) || lo <= o.MaxPrice*(1+pm))) {
			st.OutOfPrice++
			continue
		}
		if float64(b.V) < adv { // a ratio: the same on either basis
			st.LowVolume++
			continue
		}
		// the lowest low of yesterday and today (yesterday's close when neither is usable)
		floor := 0.0
		for _, l := range []float64{prevL, lo} {
			if l > 0 && (floor == 0 || l < floor) {
				floor = l
			}
		}
		if floor == 0 {
			floor = prevC
		}
		if !(hi >= o.MinSpike*floor*(1-sm)) {
			st.NoSpike++
			continue
		}
		out = append(out, PrefCand{Sym: sym, Adv10: adv * f, PrevClose: prevC, DayVol: vol, DayHigh: hi, Ratio: float64(b.V) / adv, SplitFactor: f})
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
