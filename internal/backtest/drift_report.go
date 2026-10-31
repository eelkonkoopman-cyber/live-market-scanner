package backtest

import (
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DStat summarises a sample (NaNs skipped).
type DStat struct {
	N                 int
	Mean, Median, Pos float64 // Pos = share > 0, in percent
	T                 float64 // mean / (sd / sqrt(n)), sample sd; NaN if n < 2 or sd == 0
}

// DriftStat computes the DStat of xs.
func DriftStat(xs []float64) DStat {
	var v []float64
	for _, x := range xs {
		if !math.IsNaN(x) && !math.IsInf(x, 0) {
			v = append(v, x)
		}
	}
	s := DStat{N: len(v), Mean: math.NaN(), Median: math.NaN(), Pos: math.NaN(), T: math.NaN()}
	if len(v) == 0 {
		return s
	}
	var sum float64
	pos := 0
	for _, x := range v {
		sum += x
		if x > 0 {
			pos++
		}
	}
	n := float64(len(v))
	s.Mean, s.Median, s.Pos = sum/n, median(v), 100*float64(pos)/n
	if len(v) >= 2 {
		var ss float64
		for _, x := range v {
			ss += (x - s.Mean) * (x - s.Mean)
		}
		if sd := math.Sqrt(ss / (n - 1)); sd > 0 {
			s.T = s.Mean / (sd / math.Sqrt(n))
		}
	}
	return s
}

// DriftFeature is one context feature: its bucket accessor and the bucket order.
type DriftFeature struct {
	Name    string
	Of      func(DriftSignal) string
	Buckets []string
}

// DriftFeatures lists the features in report order.
func DriftFeatures() []DriftFeature {
	return []DriftFeature{
		{"tod (entry)", func(s DriftSignal) string { return s.Tod }, []string{"09:30-10:00", "10:00-10:30", "10:30-11:00", "11:00-12:00", "12:00-14:00", "14:00-16:00"}},
		{"gap%", func(s DriftSignal) string { return s.Gap }, []string{"<-2", "-2..0", "0..2", "2..5", "5..10", ">10", "n/a"}},
		{"dayChg%", func(s DriftSignal) string { return s.DayChg }, []string{"<-2", "-2..0", "0..2", "2..5", "5..10", ">10", "n/a"}},
		{"rvolApprox", func(s DriftSignal) string { return s.RvolB }, []string{"1.5-2", "2-3", "3-5", ">5"}},
		{"burst", func(s DriftSignal) string { return s.BurstB }, []string{"<1", "1-2", "2-4", ">4", "n/a"}},
		{"crossNo", func(s DriftSignal) string { return s.CrossB }, []string{"1st", "2nd", "3rd+"}},
		{"belowMin", func(s DriftSignal) string { return s.BelowB }, []string{"<10", "10-30", "30-90", ">90"}},
		{"distVwap%", func(s DriftSignal) string { return s.DistB }, []string{"<0", "0-0.2", "0.2-0.5", ">0.5", "n/a"}},
		{"trend5", func(s DriftSignal) string { return s.Trend5 }, []string{"yes", "no"}},
		{"aboveOpen", func(s DriftSignal) string { return s.AboveOpen }, []string{"yes", "no"}},
		{"price", func(s DriftSignal) string { return s.PriceB }, []string{"25-50", "50-100", "100-300", ">300"}},
	}
}

// DriftTypes are the signal types in report order.
var DriftTypes = []string{VwIntrabar, VwClose, Drift5m}

var driftHorizonNames = [5]string{"r5", "r15", "r30", "r60", "rEOD"}

// DriftFlagMinN and DriftFlagT are the flag thresholds.
const (
	DriftFlagMinN = 40
	DriftFlagT    = 2.0
)

// DriftHurdlePct is the average round-trip cost per share over the entry, in
// percent: 2 x slippage + 2 x commission per share (the $1 minimum per order is
// ignored: it only matters under 200 shares).
func DriftHurdlePct(sigs []DriftSignal, p VwapParams) float64 {
	if len(sigs) == 0 {
		return math.NaN()
	}
	perShare := 2*p.SlipPerShare + 2*p.CommPerShare
	var sum float64
	for _, s := range sigs {
		sum += perShare / s.Entry
	}
	return 100 * sum / float64(len(sigs))
}

// DriftSubset filters sigs by type and, if first, to the first signal per symbol-day.
func DriftSubset(sigs []DriftSignal, typ string, first bool) []DriftSignal {
	var out []DriftSignal
	for _, s := range sigs {
		if s.Type == typ && (!first || s.First) {
			out = append(out, s)
		}
	}
	return out
}

func fnum(v float64, prec int) string {
	if math.IsNaN(v) {
		return "-"
	}
	return strconv.FormatFloat(v, 'f', prec, 64)
}

func col(sigs []DriftSignal, f func(DriftSignal) float64) []float64 {
	xs := make([]float64, len(sigs))
	for i, s := range sigs {
		xs[i] = f(s)
	}
	return xs
}

// DriftFlag is one flagged bucket cell.
type DriftFlag struct {
	Type, Subset, Feature, Bucket, Horizon string
	Stat                                   DStat
	Hurdle                                 float64
}

// DriftFlags lists every (type, subset, feature, bucket, horizon in r15/r60/rEOD)
// with n >= DriftFlagMinN and |t| >= DriftFlagT, plus the number of cells tested.
func DriftFlags(sigs []DriftSignal, p VwapParams) ([]DriftFlag, int) {
	var out []DriftFlag
	tested := 0
	for _, typ := range DriftTypes {
		for _, first := range []bool{false, true} {
			sub := DriftSubset(sigs, typ, first)
			name := "all"
			if first {
				name = "first/sym-day"
			}
			hurdle := DriftHurdlePct(sub, p)
			for _, ft := range DriftFeatures() {
				for _, b := range ft.Buckets {
					var in []DriftSignal
					for _, s := range sub {
						if ft.Of(s) == b {
							in = append(in, s)
						}
					}
					for _, h := range []int{1, 3, 4} {
						st := DriftStat(col(in, func(s DriftSignal) float64 { return s.Ret[h] }))
						if st.N >= DriftFlagMinN {
							tested++
							if math.Abs(st.T) >= DriftFlagT {
								out = append(out, DriftFlag{typ, name, ft.Name, b, driftHorizonNames[h], st, hurdle})
							}
						}
					}
				}
			}
		}
	}
	return out, tested
}

// DriftMarkdown renders drift.md.
func DriftMarkdown(sigs []DriftSignal, p VwapParams, header string) string {
	var sb strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&sb, format, a...) }
	w("# vwapcross DRIFT diagnostic\n\n%s\n\n", header)
	w("Forward returns from the entry (open of the 1-minute bar after the cross bar), no stops, no positions: every signal counts. r5/r15/r30/r60 = close of the last 1-minute bar starting at or before entry+N minutes; rEOD = the 15:59 close; MFE/MAE over the bars starting within 30 minutes of the entry (MAE <= 0). \"stop units\" = return / ((entry - cross low)/entry). t = mean/(sd/sqrt(n)). A cell is flagged `*` when n >= %d and |t| >= %.0f (only r15, r60, rEOD are scanned for flags).\n\n", DriftFlagMinN, DriftFlagT)
	w("Definitions: crossNo = raw crosses of the symbol-day (universe failures do not consume it); belowMin = bars (5m: candles x5) just before the cross with EMA <= VWAP; burst = cross-bar volume / mean of the 9 bars before; trend5 = last completed 5m candle (extended-hours EMA9; for the `5m` signal the cross candle itself) closes above its 5m EMA9; gap%% = 09:30 open vs the previous regular close; dayChg%% = cross close vs the previous regular close; the `5m` signal = EMA9 of regular-hours 5m closes crossing above the regular-hours VWAP at a completed candle, universe tested at the candle's last 1-minute bar.\n\n")
	flags, tested := DriftFlags(sigs, p)
	w("Cost hurdle (2 x $0.01 slippage + 2 x $0.005 commission per share, over the entry; the $1 order minimum ignored):\n\n")
	w("| signal | subset | n | hurdle %% |\n|---|---|---:|---:|\n")
	for _, typ := range DriftTypes {
		for _, first := range []bool{false, true} {
			sub := DriftSubset(sigs, typ, first)
			w("| %s | %s | %d | %s |\n", typ, subsetName(first), len(sub), fnum(DriftHurdlePct(sub, p), 3))
		}
	}
	w("\nBucket cells scanned for flags (n >= %d): %d; flagged: %d. With that many cells, about %.0f%% of them would reach |t| >= 2 by chance alone.\n\n", DriftFlagMinN, tested, len(flags), 4.55)
	w("## Flagged buckets\n\n")
	if len(flags) == 0 {
		w("None.\n\n")
	} else {
		w("| signal | subset | feature | bucket | horizon | n | mean %% | median %% | %%pos | t | hurdle %% |\n|---|---|---|---|---|---:|---:|---:|---:|---:|---:|\n")
		for _, f := range flags {
			w("| %s | %s | %s | %s | %s | %d | %s | %s | %s | %s | %s |\n", f.Type, f.Subset, f.Feature, f.Bucket, f.Horizon, f.Stat.N,
				fnum(f.Stat.Mean, 3), fnum(f.Stat.Median, 3), fnum(f.Stat.Pos, 1), fnum(f.Stat.T, 2), fnum(f.Hurdle, 3))
		}
		w("\n")
	}
	for _, typ := range DriftTypes {
		for _, first := range []bool{false, true} {
			sub := DriftSubset(sigs, typ, first)
			w("## %s - %s (n=%d)\n\n", typ, subsetName(first), len(sub))
			w("### Overall\n\n| horizon | unit | n | mean | median | %%pos | t |\n|---|---|---:|---:|---:|---:|---:|\n")
			for h := 0; h < 5; h++ {
				h := h
				st := DriftStat(col(sub, func(s DriftSignal) float64 { return s.Ret[h] }))
				w("| %s | %% | %d | %s | %s | %s | %s |\n", driftHorizonNames[h], st.N, fnum(st.Mean, 3), fnum(st.Median, 3), fnum(st.Pos, 1), fnum(st.T, 2))
			}
			for h := 0; h < 5; h++ {
				h := h
				st := DriftStat(col(sub, func(s DriftSignal) float64 { return s.Units[h] }))
				w("| %s | stop units | %d | %s | %s | %s | %s |\n", driftHorizonNames[h], st.N, fnum(st.Mean, 3), fnum(st.Median, 3), fnum(st.Pos, 1), fnum(st.T, 2))
			}
			if hasDriftBench(sub) {
				for h := 0; h < 5; h++ {
					h := h
					st := DriftStat(col(sub, func(s DriftSignal) float64 { return s.Excess[h] }))
					w("| %s | excess %% vs benchmark | %d | %s | %s | %s | %s |\n", driftHorizonNames[h], st.N, fnum(st.Mean, 3), fnum(st.Median, 3), fnum(st.Pos, 1), fnum(st.T, 2))
				}
			}
			mf := DriftStat(col(sub, func(s DriftSignal) float64 { return s.MFE }))
			ma := DriftStat(col(sub, func(s DriftSignal) float64 { return s.MAE }))
			rk := DriftStat(col(sub, func(s DriftSignal) float64 { return s.RiskPct }))
			w("| MFE30 | %% | %d | %s | %s | | |\n| MAE30 | %% | %d | %s | %s | | |\n| risk (entry-crossLow) | %% | %d | %s | %s | | |\n\n",
				mf.N, fnum(mf.Mean, 3), fnum(mf.Median, 3), ma.N, fnum(ma.Mean, 3), fnum(ma.Median, 3), rk.N, fnum(rk.Mean, 3), fnum(rk.Median, 3))
			for _, ft := range DriftFeatures() {
				bench := hasDriftBench(sub)
				if bench {
					w("### %s\n\n| bucket | n | r15 mean | r15 t | r60 mean | r60 t | rEOD mean | rEOD t | ex n | ex r15 mean | ex r15 t | ex r60 mean | ex r60 t | ex rEOD mean | ex rEOD t |\n|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n", ft.Name)
				} else {
					w("### %s\n\n| bucket | n | r15 mean | r15 t | r60 mean | r60 t | rEOD mean | rEOD t |\n|---|---:|---:|---:|---:|---:|---:|---:|\n", ft.Name)
				}
				for _, b := range ft.Buckets {
					var in []DriftSignal
					for _, s := range sub {
						if ft.Of(s) == b {
							in = append(in, s)
						}
					}
					if len(in) == 0 {
						continue
					}
					cells := ""
					for _, h := range []int{1, 3, 4} {
						h := h
						st := DriftStat(col(in, func(s DriftSignal) float64 { return s.Ret[h] }))
						mark := ""
						if st.N >= DriftFlagMinN && math.Abs(st.T) >= DriftFlagT {
							mark = " *"
						}
						cells += fmt.Sprintf(" %s%s | %s |", fnum(st.Mean, 3), mark, fnum(st.T, 2))
					}
					if bench {
						cells += fmt.Sprintf(" %d |", DriftStat(col(in, func(s DriftSignal) float64 { return s.Excess[4] })).N)
						for _, h := range []int{1, 3, 4} {
							h := h
							st := DriftStat(col(in, func(s DriftSignal) float64 { return s.Excess[h] }))
							cells += fmt.Sprintf(" %s | %s |", fnum(st.Mean, 3), fnum(st.T, 2))
						}
					}
					w("| %s | %d |%s\n", b, len(in), cells)
				}
				w("\n")
			}
		}
	}
	return sb.String()
}

func subsetName(first bool) string {
	if first {
		return "first per symbol-day"
	}
	return "all signals"
}

// DriftCSV renders drift.csv (every signal, every feature, every return).
func DriftCSV(sigs []DriftSignal) ([][]string, error) {
	hdr := []string{"type", "date", "sym", "crossTs", "entryTs", "entry", "crossLow", "crossClose", "firstOfSymDay", "riskPct",
		"r5", "r15", "r30", "r60", "rEOD", "u5", "u15", "u30", "u60", "uEOD", "mfe30", "mae30",
		"entryMin", "tod", "gapPct", "gap", "dayChgPct", "dayChg", "rvol", "rvolB", "burst", "burstB", "crossNo", "crossB",
		"belowMin", "belowB", "distVwapPct", "distB", "trend5", "aboveOpen", "price", "priceB"}
	bench := hasDriftBench(sigs)
	if bench {
		hdr = append(hdr, "benchOK", "b5", "b15", "b30", "b60", "bEOD", "x5", "x15", "x30", "x60", "xEOD")
	}
	rows := [][]string{hdr}
	f := func(v float64) string {
		if math.IsNaN(v) {
			return ""
		}
		return strconv.FormatFloat(v, 'f', 5, 64)
	}
	i := strconv.Itoa
	for _, s := range sigs {
		row := []string{s.Type, s.Date, s.Sym, strconv.FormatInt(s.CrossTs, 10), strconv.FormatInt(s.EntryTs, 10), f(s.Entry), f(s.CrossLow), f(s.CrossClose),
			strconv.FormatBool(s.First), f(s.RiskPct)}
		for h := 0; h < 5; h++ {
			row = append(row, f(s.Ret[h]))
		}
		for h := 0; h < 5; h++ {
			row = append(row, f(s.Units[h]))
		}
		row = append(row, f(s.MFE), f(s.MAE), i(s.EntryMin), s.Tod, f(s.GapPct), s.Gap, f(s.DayChgPct), s.DayChg, f(s.Rvol), s.RvolB,
			f(s.Burst), s.BurstB, i(s.CrossN), s.CrossB, i(s.BelowMin), s.BelowB, f(s.DistPct), s.DistB, s.Trend5, s.AboveOpen, f(s.CrossClose), s.PriceB)
		if bench {
			row = append(row, strconv.FormatBool(s.BenchOK))
			for h := 0; h < 5; h++ {
				row = append(row, f(s.BenchRet[h]))
			}
			for h := 0; h < 5; h++ {
				row = append(row, f(s.Excess[h]))
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// WriteDriftFiles writes drift.md and drift.csv into dir.
func WriteDriftFiles(dir string, sigs []DriftSignal, p VwapParams, header string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	SortDriftSignals(sigs)
	md := filepath.Join(dir, "drift.md")
	if err := os.WriteFile(md, []byte(DriftMarkdown(sigs, p, header)), 0o644); err != nil {
		return nil, err
	}
	rows, _ := DriftCSV(sigs)
	cp := filepath.Join(dir, "drift.csv")
	fh, err := os.Create(cp)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	cw := csv.NewWriter(fh)
	if err := cw.WriteAll(rows); err != nil {
		return nil, err
	}
	return []string{md, cp}, nil
}

// hasDriftBench reports whether any signal carries benchmark data; the excess
// columns appear only then, so a run without benchmark bars is unchanged.
func hasDriftBench(sigs []DriftSignal) bool {
	for _, s := range sigs {
		if s.BenchOK {
			return true
		}
	}
	return false
}
