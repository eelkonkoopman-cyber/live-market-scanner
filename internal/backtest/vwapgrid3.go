package backtest

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Pre-registered grid v3 of the vwapcross strategy, defined in code BEFORE any
// result was looked at (do not edit a variant after a run; add grid v4). It is
// built from the round-4 drift2 findings (September + October, first signal per
// symbol-day). August 2026 is the reserved hold-out and is never run for it.
//
// All variants: entry = open of the bar after the cross, $50 risk sizing, the
// grid v1/v2 cost model, one position per symbol, flat at the 15:59 close, SPY
// excess reported. Signal = the INTRABAR 1-minute signal unless stated.
//
// DEFINITIONS
//   - "first" = DriftSignal.CrossN == 1. CrossN is the drift study's RAW cross
//     count of the symbol-day: a cross consumes the raw state whether or not the
//     universe passed, so a universe-failed earlier cross DOES count: only a
//     signal that is both the day's first raw cross and universe-passing is
//     "first". (The drift study's own First flag, the first universe-passing
//     signal, is not used.)
//   - trend5 = DriftSignal.Trend5 == "yes" (extended-hours 9 EMA of completed 5m
//     closes rising, exactly as the drift study computed it at the cross).
//   - AVOID = skip when dayChg% < -2 AND spyDay% < -0.5, both read at the cross
//     minute. If SPY is unknown for the signal (no bench bars), the signal is
//     NOT avoided (nothing can be judged); the excess columns exclude it anyway.
//   - V5 uses the CLOSE signal (cross judged at the bar's close; entry still at
//     the next bar's open) and DistPct = (cross close - VWAP)/VWAP*100 > 0.5.
//   - V6 = V4's sized trades through ApplyPortfolio with max 5 concurrent and a
//     -3R daily realised loss limit.
//   - V0 = grid v2 H3 unchanged (all signals, 1.5 x ATR14 stop, EOD).

// Grid3Version names the grid below.
const Grid3Version = "grid v3"

const (
	g3AvoidDayChg = -2.0
	g3AvoidSpyDay = -0.5
	g3MinDist     = 0.5
)

// Grid3Limits are V6's limits.
func Grid3Limits() PortfolioLimits { return PortfolioLimits{MaxConcurrent: 5, DayLossR: 3} }

// Grid3 returns grid v3 sorted by id.
func Grid3() []Grid2Rules {
	mk := func(id, desc, stop string) Grid2Rules {
		return Grid2Rules{ID: id, Desc: desc, Signal: VwIntrabar, Stop: stop, FirstOnly: true}
	}
	v1 := mk("V1", "first cross + trend5 yes; fixed stop = cross low; hold to EOD", G2StopCrossLow)
	v1.Trend5Yes = true
	v2 := mk("V2", "first cross + trend5 yes; fixed stop = entry - 1.5 x ATR14; hold to EOD", G2StopATR)
	v2.Trend5Yes = true
	v3 := mk("V3", "first cross + trend5 yes; fixed stop = day's regular-hours low so far - 1 tick (skip if >= entry); hold to EOD", G2StopDayLow)
	v3.Trend5Yes = true
	v4 := v2
	v4.ID, v4.Desc, v4.Avoid = "V4", "V2 + AVOID (skip when dayChg < -2% AND spyDay < -0.5% at the cross)", true
	v5 := mk("V5", "CLOSE signal + first cross + distVwap > 0.5%; stop 1.5 x ATR14; EOD", G2StopATR)
	v5.Signal, v5.MinDist = VwClose, g3MinDist
	v6 := v4
	v6.ID, v6.Desc, v6.Derive, v6.Lim = "V6", "V4 with portfolio limits: max 5 concurrent positions, daily loss limit -3R", "V4", Grid3Limits()
	v7 := mk("V7", "first cross only (no trend5); stop 1.5 x ATR14; EOD (reference for trend5)", G2StopATR)
	v0 := g3H3()
	v0.ID, v0.Desc = "V0", "reference: grid v2 H3 (all signals, 1.5 x ATR14 stop, EOD)"
	out := []Grid2Rules{v0, v1, v2, v3, v4, v5, v6, v7}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func g3H3() Grid2Rules {
	for _, r := range Grid2() {
		if r.ID == "H3" {
			return r
		}
	}
	panic("grid v2 H3 missing")
}

// Grid3Month is the label of a run (YYYY-MM of its first test day).
func Grid3Month(r Grid2Report) string {
	if len(r.Meta.From) >= 7 {
		return r.Meta.From[:7]
	}
	return "?"
}

// ─── files ──────────────────────────────────────────────────────────────

var grid3CSVHeader = []string{
	"variant", "signal", "trades", "win_rate_pct", "avg_r_gross", "avg_r_net", "median_r_net", "ci95_lo", "ci95_hi", "mean_net_pct",
	"excess_n", "excess_missing", "mean_excess_pct", "excess_t", "max_dd_r", "worst_day_r", "worst_day", "days_traded",
	"exit_stop", "exit_time", "exit_eod", "skipped_size", "skipped_max_positions", "skipped_daily_loss",
}

func grid3Row(v Grid2Variant) []string {
	s, x := v.Stats, v.Extra
	g := func(f float64, prec int) string { return strconv.FormatFloat(finite(f), 'f', prec, 64) }
	var pm, pl int
	if v.Portfolio != nil {
		pm, pl = v.Portfolio.MaxPos, v.Portfolio.DayLoss
	}
	return []string{v.ID, v.Signal, strconv.Itoa(s.Trades), g(100*s.WinRate, 1), g(s.AvgRGross, 4), g(s.AvgRNet, 4), g(s.MedRNet, 4), g(s.CILo, 4), g(s.CIHi, 4), g(x.MeanNetPct, 4),
		strconv.Itoa(x.Excess.N), strconv.Itoa(x.Excess.Missing), g(x.Excess.Mean, 4), g(x.Excess.T, 2), g(x.MaxDDR, 2), g(x.WorstDayR, 2), x.WorstDay, strconv.Itoa(x.DaysTraded),
		strconv.Itoa(v.Exits.Stop + v.Exits.Trail), strconv.Itoa(v.Exits.Time), strconv.Itoa(v.Exits.EOD), strconv.Itoa(v.SkippedSize), strconv.Itoa(pm), strconv.Itoa(pl)}
}

// grid3Cells are the display columns of a variant row (after the id).
func grid3Cells(v Grid2Variant) []string {
	s, x := v.Stats, v.Extra
	return []string{strconv.Itoa(s.Trades), pct(s.WinRate), rr(s.AvgRGross), rr(s.AvgRNet), rr(s.MedRNet), fmt.Sprintf("%+.3f..%+.3f", s.CILo, s.CIHi),
		fmt.Sprintf("%+.3f%%", x.MeanNetPct), fmt.Sprintf("%+.3f%% (t %.2f, n %d)", x.Excess.Mean, x.Excess.T, x.Excess.N),
		fmt.Sprintf("%.1fR", x.MaxDDR), fmt.Sprintf("%+.1fR %s", x.WorstDayR, x.WorstDay), strconv.Itoa(x.DaysTraded),
		fmt.Sprintf("%d/%d/%d", v.Exits.Stop+v.Exits.Trail, v.Exits.Time, v.Exits.EOD), strconv.Itoa(v.SkippedSize)}
}

var grid3Head = []string{"Trades", "Win", "Avg R gross", "Avg R net", "Median R net", "95% CI mean net R", "Mean net %/trade", "Mean excess % (t)", "Max DD", "Worst day", "Days", "Exits stop/time/eod", "skipped_size"}

// WriteGrid3Files writes grid3.md, grid3.csv and grid3.json into dir.
func WriteGrid3Files(dir string, r Grid2Report) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	r.Variants = append([]Grid2Variant(nil), r.Variants...)
	sort.SliceStable(r.Variants, func(i, j int) bool { return r.Variants[i].ID < r.Variants[j].ID })
	rows := [][]string{grid3CSVHeader}
	for _, v := range r.Variants {
		rows = append(rows, grid3Row(v))
	}
	cp := filepath.Join(dir, "grid3.csv")
	f, err := os.Create(cp)
	if err != nil {
		return nil, err
	}
	cw := csv.NewWriter(f)
	if err := cw.WriteAll(rows); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	js, err := r.JSON()
	if err != nil {
		return nil, err
	}
	jp := filepath.Join(dir, "grid3.json")
	if err := os.WriteFile(jp, js, 0o644); err != nil {
		return nil, err
	}
	mp := filepath.Join(dir, "grid3.md")
	if err := os.WriteFile(mp, []byte(r.Grid3Markdown()), 0o644); err != nil {
		return nil, err
	}
	return []string{mp, cp, jp}, nil
}

// Grid3Markdown is grid3.md of one run.
func (r Grid2Report) Grid3Markdown() string {
	var b strings.Builder
	m := r.Meta
	fmt.Fprintf(&b, "# Bull VWAP Cross: grid v3 (%s), %s\n\n", r.Version, Grid3Month(r))
	fmt.Fprintf(&b, "Generated %s. Test days %s .. %s (%d trading days). Command: `%s`\n\n", m.GeneratedAt, m.From, m.End, len(m.Days), m.Command)
	b.WriteString("Registered in code (Grid3 in internal/backtest/vwapgrid3.go) before any result was seen. Rows are sorted by variant id, NOT by performance; CIs understate the uncertainty (same-day trades are correlated; variants share trades).\n\n")
	fmt.Fprintf(&b, "Benchmark: %s, %d test days with bars; days without: %d %s.\n\n", r.BenchSym, r.BenchDays, len(r.BenchMiss), listDays(r.BenchMiss))
	var vr [][]string
	for _, v := range r.Variants {
		vr = append(vr, []string{v.ID, v.Signal, v.Desc})
	}
	table(&b, []string{"Id", "Signal", "Rules"}, vr)
	var rows [][]string
	for _, v := range r.Variants {
		rows = append(rows, append([]string{v.ID}, grid3Cells(v)...))
	}
	table(&b, append([]string{"Id"}, grid3Head...), rows)
	var fr [][]string
	for _, v := range r.Variants {
		c := v.Counts
		pm, pl := "-", "-"
		if v.Portfolio != nil {
			pm, pl = strconv.Itoa(v.Portfolio.MaxPos), strconv.Itoa(v.Portfolio.DayLoss)
		}
		fr = append(fr, []string{v.ID, strconv.Itoa(c.Fired), strconv.Itoa(c.IgnoredOpen), strconv.Itoa(c.NoEntryBar), strconv.Itoa(c.SkippedGap), strconv.Itoa(v.SkippedSize), pm, pl})
	}
	table(&b, []string{"Id", "Signals after gates", "Ignored (position open)", "No entry bar / ATR", "skipped_gap", "skipped_size", "V6 skipped: max 5 open", "V6 skipped: day loss -3R"}, fr)
	fmt.Fprintf(&b, "V6 limits: max %d concurrent positions, realised net R of the day <= -%.0fR stops new entries. Excess %% = gross trade return minus %s over the same minutes. Max DD = peak-to-trough of cumulative net R. R is measured against each variant's own stop (not like-for-like across stop rules).\n\n", r.Limits.MaxConcurrent, r.Limits.DayLossR, r.BenchSym)
	return b.String()
}

// ReadGrid3JSON loads a grid3.json (or any Grid2Report JSON).
func ReadGrid3JSON(path string) (Grid2Report, error) {
	var r Grid2Report
	raw, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(raw, &r)
}

func grid3Find(r Grid2Report, id string) (Grid2Variant, bool) {
	for _, v := range r.Variants {
		if v.ID == id {
			return v, true
		}
	}
	return Grid2Variant{}, false
}

// Grid3Min picks, per variant, the report index of the worse month (lower mean
// net R; ties: the earlier month).
func Grid3Min(reps []Grid2Report) map[string]int {
	out := map[string]int{}
	for i, r := range reps {
		for _, v := range r.Variants {
			j, seen := out[v.ID]
			if !seen {
				out[v.ID] = i
				continue
			}
			if cur, _ := grid3Find(reps[j], v.ID); v.Stats.AvgRNet < cur.Stats.AvgRNet {
				out[v.ID] = i
			}
		}
	}
	return out
}

// Grid3CombinedMarkdown is grid3_both.md: per variant one row per month and a
// "min" row, the month with the LOWER mean net R (every column of that month).
func Grid3CombinedMarkdown(reps []Grid2Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Bull VWAP Cross: grid v3 (%s), months combined\n\n", Grid3Version)
	b.WriteString("Per variant: one row per month and a `min` row = the month with the lower mean net R (all columns of that month). The selection criterion is the min row.\n\n")
	seen := map[string]bool{}
	var order []string
	for _, r := range reps {
		for _, v := range r.Variants {
			if !seen[v.ID] {
				seen[v.ID] = true
				order = append(order, v.ID)
			}
		}
	}
	sort.Strings(order)
	minIdx := Grid3Min(reps)
	var rows [][]string
	for _, id := range order {
		for _, r := range reps {
			if v, ok := grid3Find(r, id); ok {
				rows = append(rows, append([]string{id, Grid3Month(r)}, grid3Cells(v)...))
			}
		}
		mr := reps[minIdx[id]]
		if v, ok := grid3Find(mr, id); ok {
			rows = append(rows, append([]string{id, "min (" + Grid3Month(mr) + ")"}, grid3Cells(v)...))
		}
	}
	table(&b, append([]string{"Id", "Month"}, grid3Head...), rows)
	return b.String()
}

// WriteGrid3Both reads the grid3.json of each dir (in the order given) and
// writes grid3_both.md into outDir.
func WriteGrid3Both(outDir string, dirs []string) (string, error) {
	var reps []Grid2Report
	for _, d := range dirs {
		r, err := ReadGrid3JSON(filepath.Join(d, "grid3.json"))
		if err != nil {
			return "", err
		}
		reps = append(reps, r)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(outDir, "grid3_both.md")
	return p, os.WriteFile(p, []byte(Grid3CombinedMarkdown(reps)), 0o644)
}
