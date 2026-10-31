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

// Drift2Summary is the short console text.
func Drift2Summary(r D2Result) string {
	return fmt.Sprintf("drift2: months %s; cells built %d, tested (n>=%d in each month) %d, CONSISTENT %d (positive %d, negative %d); expected by chance %.2f (t and sign rule, independent cells) / %.2f (brief's literal formula)",
		strings.Join(r.Months, ","), r.NCells, D2MinN, r.NTested, r.NFlagged, len(r.Consistent(1)), len(r.Consistent(-1)),
		D2ChanceExpected(r.NTested, D2MinT), D2ChanceLiteral(r.NTested))
}

func d2Stats(per []DStat) string {
	var parts []string
	for _, s := range per {
		parts = append(parts, fmt.Sprintf("%d | %s | %s", s.N, fnum(s.Mean, 3), fnum(s.T, 2)))
	}
	return strings.Join(parts, " | ")
}

// Drift2Markdown renders drift2.md.
func Drift2Markdown(r D2Result, header string) string {
	var sb strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&sb, format, a...) }
	w("# vwapcross DRIFT2 - regime features and per-month consistency\n\n%s\n\n", header)
	w("Subset: FIRST signal per symbol-day only. Horizons r60 and rEOD, each in raw %% and in %% excess over the benchmark. Months: %s. Features: all drift features plus spyDay%% (SPY at the cross minute vs its previous regular close), spyVsVwap (SPY above its own regular-hours VWAP at the cross minute), spy30%% (SPY change over the 30 minutes up to the cross minute; before 10:00 measured from the 09:30 open), relStr%% (stock dayChg%% - spyDay%%), aboveOpen (= stockVsOpen). Pairs (pre-registered, nothing else): dayChg%% x spyDay%%, dayChg%% x spyVsVwap, relStr%% x spyVsVwap, tod x spyVsVwap, belowMin x spyVsVwap, trend5 x spyVsVwap. Buckets with an unknown part (n/a) are skipped.\n\n", strings.Join(r.Months, ", "))
	w("A bucket is CONSISTENT when: n >= %d in EACH month, the mean has the same sign in each month, |t| >= %.1f in EACH month, and the pooled mean beats the cost hurdle in that direction (above +hurdle, or below -hurdle for the negative list). The hurdle is the type's average round-trip cost over the entry (about 0.044%%).\n\n", D2MinN, D2MinT)
	w("Cost hurdle per signal type (first per symbol-day): ")
	for i, t := range DriftTypes {
		if i > 0 {
			w("; ")
		}
		w("%s %s%%", t, fnum(r.Hurdles[t], 3))
	}
	w("\n\n")
	w("Cells built: %d. Cells tested (n >= %d in every month): %d. CONSISTENT: %d (positive %d, negative %d).\n\n", r.NCells, D2MinN, r.NTested, r.NFlagged, len(r.Consistent(1)), len(r.Consistent(-1)))
	w("Expected by chance among the tested cells: %.2f = cells x P(|t|>=%.1f)^2 x 1/2 with P = %.4f (same sign given both months significant; cells treated as independent, which they are not: raw/excess and r60/rEOD of one bucket are strongly correlated, and the pooled-hurdle condition is ignored, so this overstates the independent-cell count). The brief's literal formula cells x 0.134^2 x 0.5 x 2 gives %.2f.\n\n",
		D2ChanceExpected(r.NTested, D2MinT), D2MinT, math.Erfc(D2MinT/math.Sqrt2), D2ChanceLiteral(r.NTested))

	byKey := map[string]D2Cell{}
	for _, c := range r.Cells {
		byKey[c.key()] = c
	}
	list := func(title string, dir int) {
		cs := r.Consistent(dir)
		w("## %s (%d)\n\nSorted by min over months of |t|. \"other basis\" is the same bucket/horizon on the other basis (raw <-> excess).\n\n", title, len(cs))
		if len(cs) == 0 {
			w("None.\n\n")
			return
		}
		w("| signal | basis | horizon | feature | bucket |")
		for _, m := range r.Months {
			w(" %s n | %s mean | %s t |", m, m, m)
		}
		w(" pooled n | pooled mean | pooled t | hurdle | min abs t | other basis: per-month mean (t), pooled mean |\n|---|---|---|---|---|")
		for range r.Months {
			w("---:|---:|---:|")
		}
		w("---:|---:|---:|---:|---:|---|\n")
		for _, c := range cs {
			w("| %s | %s | %s | %s | %s |", c.Type, c.Basis, c.Horizon, c.Feature, c.Bucket)
			for _, s := range c.PerMonth {
				w(" %d | %s | %s |", s.N, fnum(s.Mean, 3), fnum(s.T, 2))
			}
			other := "-"
			if o, ok := byKey[strings.Replace(c.key(), "|"+c.Basis+"|", "|"+otherBasis(c.Basis)+"|", 1)]; ok {
				var ps []string
				for _, s := range o.PerMonth {
					ps = append(ps, fmt.Sprintf("%s (%s, n %d)", fnum(s.Mean, 3), fnum(s.T, 2), s.N))
				}
				other = strings.Join(ps, "; ") + "; pooled " + fnum(o.Pooled.Mean, 3)
			}
			w(" %d | %s | %s | %s | %s | %s |\n", c.Pooled.N, fnum(c.Pooled.Mean, 3), fnum(c.Pooled.T, 2), fnum(c.Hurdle, 3), fnum(c.MinAbsT(), 2), other)
		}
		w("\n")
	}
	list("CONSISTENT positive (long-side candidates)", 1)
	list("CONSISTENT negative (SHORT-side or avoid; the bear-cross equivalent is NOT computed here)", -1)

	w("## All cells\n\n")
	for _, typ := range DriftTypes {
		for _, basis := range D2Bases {
			for _, h := range D2Horizons {
				w("### %s - %s - %s\n\n| feature | bucket |", typ, basis, h.Name)
				for _, m := range r.Months {
					w(" %s n | %s mean | %s t |", m, m, m)
				}
				w(" pooled n | pooled mean | pooled t | tested | C |\n|---|---|")
				for range r.Months {
					w("---:|---:|---:|")
				}
				w("---:|---:|---:|:-:|:-:|\n")
				for _, c := range r.Cells {
					if c.Type != typ || c.Basis != basis || c.Horizon != h.Name {
						continue
					}
					if c.Pooled.N == 0 {
						continue
					}
					mark := ""
					if c.Consistent {
						mark = map[int]string{1: "+", -1: "-"}[c.Dir]
					}
					w("| %s | %s |", c.Feature, c.Bucket)
					for _, s := range c.PerMonth {
						w(" %d | %s | %s |", s.N, fnum(s.Mean, 3), fnum(s.T, 2))
					}
					w(" %d | %s | %s | %s | %s |\n", c.Pooled.N, fnum(c.Pooled.Mean, 3), fnum(c.Pooled.T, 2), yesNo(c.Eligible), mark)
				}
				w("\n")
			}
		}
	}
	return sb.String()
}

func otherBasis(b string) string {
	if b == "raw" {
		return "excess"
	}
	return "raw"
}

// Drift2CSV renders drift2.csv: one row per cell.
func Drift2CSV(r D2Result) [][]string {
	hdr := []string{"type", "basis", "horizon", "feature", "bucket"}
	for _, m := range r.Months {
		hdr = append(hdr, m+"_n", m+"_mean", m+"_t")
	}
	hdr = append(hdr, "pooled_n", "pooled_mean", "pooled_t", "hurdle", "tested", "consistent", "direction", "minAbsT")
	rows := [][]string{hdr}
	f := func(v float64) string {
		if math.IsNaN(v) {
			return ""
		}
		return strconv.FormatFloat(v, 'f', 5, 64)
	}
	for _, c := range r.Cells {
		row := []string{c.Type, c.Basis, c.Horizon, c.Feature, c.Bucket}
		for _, s := range c.PerMonth {
			row = append(row, strconv.Itoa(s.N), f(s.Mean), f(s.T))
		}
		dir := ""
		if c.Consistent {
			dir = strconv.Itoa(c.Dir)
		}
		row = append(row, strconv.Itoa(c.Pooled.N), f(c.Pooled.Mean), f(c.Pooled.T), f(c.Hurdle),
			strconv.FormatBool(c.Eligible), strconv.FormatBool(c.Consistent), dir, f(c.MinAbsT()))
		rows = append(rows, row)
	}
	return rows
}

// WriteDrift2Files writes drift2.md and drift2.csv into dir and returns the paths
// and a console summary.
func WriteDrift2Files(dir string, sigs []DriftSignal, p VwapParams, header string) ([]string, string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", err
	}
	res := Drift2Analyze(sigs, p)
	md := filepath.Join(dir, "drift2.md")
	if err := os.WriteFile(md, []byte(Drift2Markdown(res, header)), 0o644); err != nil {
		return nil, "", err
	}
	cp := filepath.Join(dir, "drift2.csv")
	fh, err := os.Create(cp)
	if err != nil {
		return nil, "", err
	}
	defer fh.Close()
	if err := csv.NewWriter(fh).WriteAll(Drift2CSV(res)); err != nil {
		return nil, "", err
	}
	return []string{md, cp}, Drift2Summary(res), nil
}
