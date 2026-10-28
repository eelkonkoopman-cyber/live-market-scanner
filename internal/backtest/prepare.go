package backtest

import (
	"math"
	"sort"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/emaflag"
)

// Pre-market, in ET minutes of the day: pmVolX counts the volume traded from
// 04:00 up to (not including) the 09:30 open. The live engine's pmVolX is frozen
// at 09:30 at that complete pre-market value, so "never crossed 1x during the
// pre-market" is the same thing as "never a candidate".
const (
	pmStartMin = 4 * 60
	pmEndMin   = 9*60 + 30

	// MinPmVolX is the candidate threshold (flagscan.MinPmVolX, inclusive).
	MinPmVolX = 1.0
)

// Candidate is one (symbol, day) with the data the simulation needs.
type Candidate struct {
	Sym       string
	Date      string // ET trade date "2006-01-02"
	Adv10     float64
	PrevClose float64
	DayVol    float64
	Bars      []core.Bar // 1-minute, ascending: the previous trading day + the day (the chart's data shape)

	// SplitFactor is the factor by which splits executed AFTER the day multiplied
	// the feed's adjusted prices (1 or 0: none). Adv10, PrevClose, DayVol and Bars
	// are already back on the basis of the day (see splits.go); the factor is kept
	// to flag the trades on such names and to price what the adjusted figures
	// would have understated.
	SplitFactor float64

	// Filled by Admit / PrepareDay.
	AdmitTs int64   // bar start (ms) of the minute pmVolX first reached MinPmVolX
	PmVolX  float64 // the complete pre-market multiple (the value the live scan sorts by)
}

// Admit works out the admission of a name: the first 1-minute bar of the day
// (04:00 <= minute < 09:30 ET) at whose end the cumulative pre-market volume
// reaches MinPmVolX times adv10. admitTs is that bar's START (ms). finalX is the
// pre-market multiple at 09:30. ok is false when it never reached 1x (or adv10
// is not positive).
//
// A bar is counted whole: the live scan sees the crossing partway through the
// bar and admits within one candidate refresh (30 s), and the bar the crossing
// happened on is HISTORY to the trader (it primes on its first evaluation after
// admission), which is why Signals only takes events from the next minute on.
func Admit(bars []core.Bar, date string, adv10 float64) (admitTs int64, finalX float64, ok bool) {
	if !(adv10 > 0) {
		return 0, 0, false
	}
	var cum float64
	for i := range bars {
		b := &bars[i]
		m := ETMinute(b.T)
		if m < pmStartMin || m >= pmEndMin || ETDate(b.T) != date {
			continue
		}
		cum += float64(b.V)
		if !ok && cum/adv10 >= MinPmVolX {
			admitTs, ok = b.T, true
		}
	}
	return admitTs, cum / adv10, ok
}

// Signal is one LONG (FLAG) entry the engine called on a candidate's day that
// the trader could have seen (after admission). It is the trader's `pending`
// event plus the engine's stop at that moment.
type Signal struct {
	Cand    *Candidate
	Idx     int     // index of the entry bar in Cand.Bars
	Ts      int64   // the entry bar's start (ms)
	Trigger float64 // the event's price: the flag high * 1.003
	Stop    float64 // the engine's stop on that event (NaN: none)
	Rank    int     // 1 = strongest pmVolX of the day (the order the live scan evaluates in)
}

// Signals runs the engine over the candidate's whole 2-day bar array ONCE and
// returns TODAY's LONG (FLAG) entry events at least one minute after admission.
// emaflag.Evaluate is append-only (golden-tested against the TS engine), so the
// whole-day evaluation holds every event the live bar-by-bar evaluation would
// have emitted. nowMs is the end of the last bar, so every bar is CLOSED.
//
// The stop is read the way the trader reads it (tryEntry takes
// u.Result.Stop of the update that carries the event): the engine is run again
// over the bars up to and including the entry bar with that bar still FORMING,
// where the position the break opened is long and Result.Stop is its initial
// stop (max(flag low, EMA of the last closed bar) * (1 - 1%)).
func Signals(c *Candidate, p emaflag.Params) []Signal {
	bars := c.Bars
	n := len(bars)
	if n == 0 {
		return nil
	}
	full := emaflag.Evaluate(bars, minuteMs, bars[n-1].T+minuteMs, p)
	var out []Signal
	for _, ev := range full.Events {
		if ev.Kind != emaflag.KindEntry || ev.Label != emaflag.LabelEntry {
			continue
		}
		if ETDate(ev.Ts) != c.Date || ev.Ts < c.AdmitTs+minuteMs {
			continue
		}
		idx := barIndex(bars, ev.Ts)
		if idx < 0 {
			continue
		}
		stop := math.NaN()
		cut := emaflag.Evaluate(bars[:idx+1], minuteMs, bars[idx].T, p) // the entry bar is forming
		if cut.State == emaflag.StateLong && !math.IsNaN(cut.Stop) && math.Abs(cut.EntryPrice-ev.Price) <= 1e-9*math.Max(1, math.Abs(ev.Price)) {
			stop = cut.Stop
		}
		out = append(out, Signal{Cand: c, Idx: idx, Ts: ev.Ts, Trigger: ev.Price, Stop: stop})
	}
	return out
}

// barIndex is the index of the bar that starts at ts, or -1.
func barIndex(bars []core.Bar, ts int64) int {
	i := sort.Search(len(bars), func(i int) bool { return bars[i].T >= ts })
	if i < len(bars) && bars[i].T == ts {
		return i
	}
	return -1
}

// PreparedDay is one day's admitted candidates and their signals - everything
// the variants share, computed once.
type PreparedDay struct {
	Date       string
	Fetched    int          // candidates handed in (bars in hand)
	Admitted   int          // reached pmVolX >= 1 during the pre-market
	CapDropped int          // admitted names beyond MaxCandidates (the live cap, by pmVolX rank)
	Kept       []*Candidate // admitted, within the cap, with at least one signal (the only ones the simulation needs)
	Signals    []Signal     // all signals of the kept candidates, in the order the day replays them
}

// PrepareDay admits the day's candidates, applies the live cap and collects the
// signals. maxCand <= 0 means no cap.
//
// The live cap is "the 80 strongest pmVolX AT THAT MOMENT"; here the rank is by
// each name's FINAL pre-market multiple (an approximation, rarely binding: the
// count is reported either way).
func PrepareDay(date string, cands []Candidate, p emaflag.Params, maxCand int) PreparedDay {
	pd := PreparedDay{Date: date, Fetched: len(cands)}
	var adm []*Candidate
	for i := range cands {
		c := &cands[i]
		ts, x, ok := Admit(c.Bars, date, c.Adv10)
		if !ok {
			continue
		}
		c.AdmitTs, c.PmVolX = ts, x
		adm = append(adm, c)
	}
	pd.Admitted = len(adm)
	sort.Slice(adm, func(i, j int) bool {
		if adm[i].PmVolX != adm[j].PmVolX {
			return adm[i].PmVolX > adm[j].PmVolX
		}
		return adm[i].Sym < adm[j].Sym
	})
	if maxCand > 0 && len(adm) > maxCand {
		pd.CapDropped = len(adm) - maxCand
		adm = adm[:maxCand]
	}
	for rank, c0 := range adm {
		// A copy, so the kept candidate does not pin the whole day's slice (every
		// candidate's bars) in memory.
		c := new(Candidate)
		*c = *c0
		sigs := Signals(c, p)
		if len(sigs) == 0 {
			continue
		}
		for i := range sigs {
			sigs[i].Rank = rank + 1
		}
		pd.Kept = append(pd.Kept, c)
		pd.Signals = append(pd.Signals, sigs...)
	}
	sortSignals(pd.Signals)
	return pd
}

// sortSignals orders signals the way the day replays them: by time, then the
// stronger pmVolX first (the live scan evaluates strongest first), then symbol.
func sortSignals(s []Signal) {
	sort.SliceStable(s, func(i, j int) bool {
		if s[i].Ts != s[j].Ts {
			return s[i].Ts < s[j].Ts
		}
		if s[i].Rank != s[j].Rank {
			return s[i].Rank < s[j].Rank
		}
		return s[i].Cand.Sym < s[j].Cand.Sym
	})
}

// emaSeries is the 9 EMA of closes, aligned to the input, NaN during warm-up:
// emaflag.emaSeries operation for operation (SMA seed of the first `period`
// closes, then k = 2/(period+1); the explicit float64() conversions forbid
// fused multiply-add). It is duplicated because the engine does not export its
// series, and pinned to the engine by TestEMAMatchesEngine.
func emaSeries(closes []float64, period int) []float64 {
	out := make([]float64, len(closes))
	for i := range out {
		out[i] = math.NaN()
	}
	if period < 1 {
		period = 1
	}
	k := 2 / float64(period+1)
	prev, sum := 0.0, 0.0
	for i := 0; i < len(closes); i++ {
		if i < period-1 {
			sum += closes[i]
			continue
		}
		if i == period-1 {
			sum += closes[i]
			prev = sum / float64(period)
		} else {
			prev = float64(closes[i]*k) + float64(prev*(1-k))
		}
		out[i] = prev
	}
	return out
}
