package backtest

import (
	"math"
	"sort"
	"strings"
)

// -drift2: market-regime features for the drift diagnostic and a per-calendar-
// month consistency check. All regime features are read at the CROSS minute (the
// stock's cross bar, whose close the stock features also use): no lookahead.
//
//   - spyDay%   = close of the benchmark bar starting at the cross minute over the
//     benchmark's previous regular close, minus 1.
//   - spyVsVwap = that close above the benchmark's regular-hours VWAP through the
//     same bar (typical price x volume, from 09:30).
//   - spy30%    = that close over the close of the last regular benchmark bar
//     starting at or before cross-30min, minus 1; before 10:00 (no such bar) the
//     reference is the day's 09:30 open.
//   - relStr%   = the stock's dayChg% minus spyDay%.

func spyDayBucket(v float64) string {
	switch {
	case math.IsNaN(v):
		return "n/a"
	case v < -0.5:
		return "<-0.5"
	case v < 0:
		return "-0.5..0"
	case v <= 0.5:
		return "0..0.5"
	}
	return ">0.5"
}

func spy30Bucket(v float64) string {
	switch {
	case math.IsNaN(v):
		return "n/a"
	case v < -0.2:
		return "<-0.2"
	case v < 0:
		return "-0.2..0"
	case v <= 0.2:
		return "0..0.2"
	}
	return ">0.2"
}

func relStrBucket(v float64) string {
	switch {
	case math.IsNaN(v):
		return "n/a"
	case v < -3:
		return "<-3"
	case v < -1:
		return "-3..-1"
	case v <= 1:
		return "-1..1"
	case v <= 3:
		return "1..3"
	}
	return ">3"
}

// Regime is the benchmark's state at one minute.
type Regime struct {
	DayPct, Pct30 float64
	AboveVwap     bool
}

// RegimeAt reads the benchmark regime at the bar starting at crossTs (which must
// exist and be a regular-hours bar).
func (b *Bench) RegimeAt(date string, crossTs int64) (Regime, bool) {
	bars, i, ok := b.at(date, crossTs)
	if !ok {
		return Regime{}, false
	}
	if m := ETMinute(bars[i].T); m < vwSessionOpen || m >= vwSessionClose {
		return Regime{}, false
	}
	vw := b.vwap[date]
	if len(vw) != len(bars) || math.IsNaN(vw[i]) || !(vw[i] > 0) {
		return Regime{}, false
	}
	px := bars[i].C
	r := Regime{DayPct: math.NaN(), Pct30: math.NaN(), AboveVwap: px > vw[i]}
	if pc, ok := b.prev[date]; ok && pc > 0 {
		r.DayPct = 100 * (px/pc - 1)
	}
	t0 := crossTs - 30*minuteMs
	ref := math.NaN()
	for k := 0; k <= i; k++ {
		m := ETMinute(bars[k].T)
		if m < vwSessionOpen || m >= vwSessionClose {
			continue
		}
		if math.IsNaN(ref) {
			ref = bars[k].O // the day's 09:30 open: the fallback when nothing starts at or before t0
		}
		if bars[k].T <= t0 {
			ref = bars[k].C
		} else {
			break
		}
	}
	if ref > 0 {
		r.Pct30 = 100 * (px/ref - 1)
	}
	return r, true
}

// AttachRegime sets the regime features and buckets of every signal.
func (b *Bench) AttachRegime(sigs []DriftSignal) {
	for i := range sigs {
		sigs[i].setRegime(b)
	}
}

func (s *DriftSignal) setRegime(b *Bench) {
	s.RegimeOK = false
	s.SpyDayPct, s.Spy30Pct, s.RelStrPct = math.NaN(), math.NaN(), math.NaN()
	s.SpyDay, s.SpyVsVwap, s.Spy30, s.RelStr = "n/a", "n/a", "n/a", "n/a"
	r, ok := b.RegimeAt(s.Date, s.CrossTs)
	if !ok {
		return
	}
	s.RegimeOK = true
	s.SpyDayPct, s.Spy30Pct, s.SpyAboveVwap = r.DayPct, r.Pct30, r.AboveVwap
	if !math.IsNaN(s.DayChgPct) && !math.IsNaN(r.DayPct) {
		s.RelStrPct = s.DayChgPct - r.DayPct
	}
	s.SpyDay, s.Spy30, s.RelStr = spyDayBucket(s.SpyDayPct), spy30Bucket(s.Spy30Pct), relStrBucket(s.RelStrPct)
	s.SpyVsVwap = yesNo(r.AboveVwap)
}

// ─── consistency check ──────────────────────────────────────────────────

// Consistency thresholds (pre-registered).
const (
	D2MinN = 30
	D2MinT = 1.5
)

// D2Feature is a single feature or a pre-registered pair.
type D2Feature struct {
	Name    string
	Of      func(DriftSignal) string
	Buckets []string
}

func d2Base() map[string]DriftFeature {
	m := map[string]DriftFeature{}
	for _, f := range DriftFeatures() {
		m[f.Name] = f
	}
	for _, f := range []DriftFeature{
		{"spyDay%", func(s DriftSignal) string { return s.SpyDay }, []string{"<-0.5", "-0.5..0", "0..0.5", ">0.5", "n/a"}},
		{"spyVsVwap", func(s DriftSignal) string { return s.SpyVsVwap }, []string{"yes", "no", "n/a"}},
		{"spy30%", func(s DriftSignal) string { return s.Spy30 }, []string{"<-0.2", "-0.2..0", "0..0.2", ">0.2", "n/a"}},
		{"relStr%", func(s DriftSignal) string { return s.RelStr }, []string{"<-3", "-3..-1", "-1..1", "1..3", ">3", "n/a"}},
	} {
		m[f.Name] = f
	}
	return m
}

// D2Pairs are the pre-registered 2-feature combinations (nothing else).
var D2Pairs = [][2]string{
	{"dayChg%", "spyDay%"}, {"dayChg%", "spyVsVwap"}, {"relStr%", "spyVsVwap"},
	{"tod (entry)", "spyVsVwap"}, {"belowMin", "spyVsVwap"}, {"trend5", "spyVsVwap"},
}

// D2Features lists the singles (every drift feature, aboveOpen = stockVsOpen,
// plus the four regime ones) followed by the pairs.
func D2Features() []D2Feature {
	base := d2Base()
	var out []D2Feature
	for _, f := range DriftFeatures() {
		out = append(out, D2Feature{f.Name, f.Of, f.Buckets})
	}
	for _, n := range []string{"spyDay%", "spyVsVwap", "spy30%", "relStr%"} {
		f := base[n]
		out = append(out, D2Feature{f.Name, f.Of, f.Buckets})
	}
	for _, p := range D2Pairs {
		out = append(out, PairFeature(base[p[0]], base[p[1]]))
	}
	return out
}

// PairFeature crosses two features: bucket "a | b", a-major order.
func PairFeature(a, b DriftFeature) D2Feature {
	var bk []string
	for _, x := range a.Buckets {
		for _, y := range b.Buckets {
			bk = append(bk, x+" | "+y)
		}
	}
	return D2Feature{a.Name + " x " + b.Name, func(s DriftSignal) string { return a.Of(s) + " | " + b.Of(s) }, bk}
}

// Months lists the distinct YYYY-MM of the signals, ascending.
func Months(sigs []DriftSignal) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range sigs {
		if len(s.Date) >= 7 && !seen[s.Date[:7]] {
			seen[s.Date[:7]] = true
			out = append(out, s.Date[:7])
		}
	}
	sort.Strings(out)
	return out
}

// D2Cell is one (type, basis, horizon, feature, bucket) with its per-month and
// pooled statistics.
type D2Cell struct {
	Type, Basis, Horizon, Feature, Bucket string
	PerMonth                              []DStat
	Pooled                                DStat
	Hurdle                                float64
	Eligible                              bool // n >= D2MinN in EVERY month (the cell counts as tested)
	Consistent                            bool
	Dir                                   int // +1 / -1 when Consistent
}

// MinAbsT is min over the months of |t| (0 when any is NaN).
func (c D2Cell) MinAbsT() float64 {
	m := math.Inf(1)
	for _, s := range c.PerMonth {
		if math.IsNaN(s.T) {
			return 0
		}
		m = math.Min(m, math.Abs(s.T))
	}
	if math.IsInf(m, 1) {
		return 0
	}
	return m
}

// D2Eligible: at least two months and n >= minN in each.
func D2Eligible(per []DStat, minN int) bool {
	if len(per) < 2 {
		return false
	}
	for _, s := range per {
		if s.N < minN {
			return false
		}
	}
	return true
}

// D2Consistent applies the pre-registered rule: n >= minN in EACH month, the mean
// has the same (non-zero) sign in each month, |t| >= minT in EACH month, and the
// pooled mean exceeds the cost hurdle in that direction (above +hurdle for a
// positive bucket, below -hurdle for a negative one). dir is +1 / -1.
func D2Consistent(per []DStat, pooled DStat, hurdle float64, minN int, minT float64) (ok bool, dir int) {
	if !D2Eligible(per, minN) {
		return false, 0
	}
	for i, s := range per {
		if math.IsNaN(s.Mean) || s.Mean == 0 || math.IsNaN(s.T) || math.Abs(s.T) < minT {
			return false, 0
		}
		d := 1
		if s.Mean < 0 {
			d = -1
		}
		if i == 0 {
			dir = d
		} else if d != dir {
			return false, 0
		}
	}
	if math.IsNaN(pooled.Mean) || math.IsNaN(hurdle) {
		return false, 0
	}
	if dir > 0 && !(pooled.Mean > hurdle) {
		return false, 0
	}
	if dir < 0 && !(pooled.Mean < -hurdle) {
		return false, 0
	}
	return true, dir
}

// D2ChanceExpected is the number of cells expected to pass the per-month t and
// sign conditions by chance alone: cells x P(|t|>=minT)^2 x 1/2 (same sign given
// both significant), t treated as standard normal (two months).
func D2ChanceExpected(cells int, minT float64) float64 {
	p := math.Erfc(minT / math.Sqrt2)
	return float64(cells) * p * p * 0.5
}

// D2ChanceLiteral is the brief's literal approximation cells x 0.134^2 x 0.5 x 2.
func D2ChanceLiteral(cells int) float64 { return float64(cells) * 0.134 * 0.134 * 0.5 * 2 }

// D2Result is the whole analysis.
type D2Result struct {
	Months   []string
	Cells    []D2Cell
	Hurdles  map[string]float64 // per signal type
	NCells   int                // all cells built
	NTested  int                // eligible cells
	NFlagged int
}

// D2Horizons are the horizons of the check: r60 and rEOD.
var D2Horizons = []struct {
	Name string
	Idx  int
}{{"r60", 3}, {"rEOD", 4}}

// D2Bases are the return bases.
var D2Bases = []string{"raw", "excess"}

// Drift2Analyze evaluates every cell (first signal per symbol-day only).
func Drift2Analyze(sigs []DriftSignal, p VwapParams) D2Result {
	res := D2Result{Months: Months(sigs), Hurdles: map[string]float64{}}
	feats := D2Features()
	for _, typ := range DriftTypes {
		sub := DriftSubset(sigs, typ, true)
		hurdle := DriftHurdlePct(sub, p)
		res.Hurdles[typ] = hurdle
		for _, basis := range D2Bases {
			use := sub
			if basis == "excess" {
				use = nil
				for _, s := range sub {
					if s.BenchOK {
						use = append(use, s)
					}
				}
			}
			for _, h := range D2Horizons {
				idx := h.Idx
				val := func(s DriftSignal) float64 {
					if basis == "excess" {
						return s.Excess[idx]
					}
					return s.Ret[idx]
				}
				for _, ft := range feats {
					groups := map[string][]DriftSignal{}
					for _, s := range use {
						k := ft.Of(s)
						groups[k] = append(groups[k], s)
					}
					for _, b := range ft.Buckets {
						if strings.Contains(b, "n/a") {
							continue // unknown: not an actionable bucket
						}
						in := groups[b]
						c := D2Cell{Type: typ, Basis: basis, Horizon: h.Name, Feature: ft.Name, Bucket: b, Hurdle: hurdle}
						for _, m := range res.Months {
							var xs []float64
							for _, s := range in {
								if s.Date[:7] == m {
									xs = append(xs, val(s))
								}
							}
							c.PerMonth = append(c.PerMonth, DriftStat(xs))
						}
						c.Pooled = DriftStat(col(in, val))
						c.Eligible = D2Eligible(c.PerMonth, D2MinN)
						c.Consistent, c.Dir = D2Consistent(c.PerMonth, c.Pooled, hurdle, D2MinN, D2MinT)
						res.NCells++
						if c.Eligible {
							res.NTested++
						}
						if c.Consistent {
							res.NFlagged++
						}
						res.Cells = append(res.Cells, c)
					}
				}
			}
		}
	}
	return res
}

// Consistent returns the consistent cells of one direction, sorted by MinAbsT
// descending (ties: type, basis, horizon, feature, bucket).
func (r D2Result) Consistent(dir int) []D2Cell {
	var out []D2Cell
	for _, c := range r.Cells {
		if c.Consistent && c.Dir == dir {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.MinAbsT() != b.MinAbsT() {
			return a.MinAbsT() > b.MinAbsT()
		}
		return a.key() < b.key()
	})
	return out
}

func (c D2Cell) key() string {
	return c.Type + "|" + c.Basis + "|" + c.Horizon + "|" + c.Feature + "|" + c.Bucket
}
