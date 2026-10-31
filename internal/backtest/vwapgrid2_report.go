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

// Grid2Exits counts the sized trades by exit reason.
type Grid2Exits struct {
	Stop  int `json:"stop"`
	Trail int `json:"trail"` // R0 only
	Time  int `json:"time"`
	EOD   int `json:"eod"`
}

// Grid2Extra are the v2-only statistics of a variant.
type Grid2Extra struct {
	MeanNetPct float64    `json:"meanNetPctPerTrade"` // net P&L / (shares x entry), average
	MeanRetPct float64    `json:"meanGrossPctPerTrade"`
	Excess     ExcessStat `json:"excessVsBenchmark"`
	MaxDDR     float64    `json:"maxDrawdownR"`
	WorstDayR  float64    `json:"worstDayR"`
	WorstDay   string     `json:"worstDay"`
	DaysTraded int        `json:"daysTraded"`
}

// Grid2Variant is one variant's result.
type Grid2Variant struct {
	ID          string          `json:"id"`
	Desc        string          `json:"desc"`
	Signal      string          `json:"signal"`
	Stats       VwStats         `json:"stats"`
	Exits       Grid2Exits      `json:"exits"`
	Extra       Grid2Extra      `json:"extra"`
	SkippedSize int             `json:"skippedSize"`
	Counts      VwCounts        `json:"counts"`
	ByHour      []VwRow         `json:"byEntryHour"`
	Portfolio   *PortfolioSkips `json:"portfolio,omitempty"`
	Trades      []VwTrade       `json:"-"` // sized trades, date/cross order
}

// Grid2Report is the whole grid v2 result.
type Grid2Report struct {
	Version   string          `json:"gridVersion"`
	Meta      VwMeta          `json:"meta"`
	BenchSym  string          `json:"benchmark"`
	BenchDays int             `json:"benchmarkDaysWithBars"`
	BenchMiss []string        `json:"benchmarkDaysMissing"`
	Limits    PortfolioLimits `json:"portfolioLimits"`
	Variants  []Grid2Variant  `json:"variants"` // sorted by id, NOT by performance
}

// BuildGrid2Variant summarizes the trades (sized and not) of one rule set.
func BuildGrid2Variant(r Grid2Rules, trades []VwTrade, cnt VwCounts, o StatOpts) Grid2Variant {
	var sized []VwTrade
	for _, t := range trades {
		if t.Sized {
			sized = append(sized, t)
		}
	}
	sort.SliceStable(sized, func(i, j int) bool {
		a, b := sized[i], sized[j]
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		if a.CrossTs != b.CrossTs {
			return a.CrossTs < b.CrossTs
		}
		return a.Sym < b.Sym
	})
	v := Grid2Variant{ID: r.ID, Desc: r.Desc, Signal: r.Signal, Counts: cnt, Trades: sized}
	v.SkippedSize = len(trades) - len(sized)
	v.Counts.SkippedSize = v.SkippedSize
	v.Counts.Traded = len(sized)
	v.Stats = SummarizeVwap(sized, o)
	for _, t := range sized {
		switch t.Reason {
		case "stop":
			v.Exits.Stop++
		case "trail":
			v.Exits.Trail++
		case "time":
			v.Exits.Time++
		default:
			v.Exits.EOD++
		}
	}
	var net, gross float64
	for _, t := range sized {
		net += TradeNetPct(t)
		gross += t.RetPct
	}
	if n := float64(len(sized)); n > 0 {
		v.Extra.MeanNetPct, v.Extra.MeanRetPct = net/n, gross/n
	}
	v.Extra.Excess = TradeExcess(sized)
	v.Extra.MaxDDR = MaxDrawdownR(sized)
	v.Extra.WorstDayR, v.Extra.WorstDay, v.Extra.DaysTraded = WorstDayR(sized)
	v.ByHour = vwRows(sized, vwHourLabels, vwHour, false)
	return v
}

// JSON is grid2.json.
func (r Grid2Report) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

var grid2CSVHeader = []string{
	"variant", "signal", "trades", "win_rate_pct", "avg_r_gross", "avg_r_net", "median_r_net", "total_r_net", "net_usd_per_trade",
	"ci95_lo", "ci95_hi", "avg_bars_held", "exit_stop", "exit_trail", "exit_time", "exit_eod", "skipped_size",
	"mean_net_pct", "mean_gross_pct", "excess_n", "excess_missing", "mean_excess_pct", "excess_t", "mean_excess_net_pct", "mean_bench_pct",
	"max_dd_r", "worst_day_r", "worst_day", "days_traded", "skipped_max_positions", "skipped_daily_loss",
}

func grid2CSVHeaderFull() []string {
	h := append([]string(nil), grid2CSVHeader...)
	for _, l := range vwHourLabels {
		h = append(h, "hour_"+l+"_trades", "hour_"+l+"_avg_r_net")
	}
	return h
}

var grid2TradeHeader = []string{
	"variant", "date", "sym", "cross_et", "entry_et", "entry", "initial_stop", "risk_per_share", "risk_pct", "exit_et", "exit", "exit_reason",
	"bars_held", "shares", "r_gross", "r_net", "net_usd", "return_pct", "net_pct", "day_chg_pct", "below_min", "bench_ok", "bench_ret_pct", "excess_pct",
}

// WriteGrid2Files writes grid2.md, grid2.csv, grid2.json and the per-trade CSVs
// of D5 and P1 (grid2_trades_D5.csv, grid2_trades_P1.csv) into dir.
func WriteGrid2Files(dir string, r Grid2Report) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	r.Variants = append([]Grid2Variant(nil), r.Variants...)
	sort.SliceStable(r.Variants, func(i, j int) bool { return r.Variants[i].ID < r.Variants[j].ID })
	g := func(v float64, prec int) string { return strconv.FormatFloat(finite(v), 'f', prec, 64) }
	md := filepath.Join(dir, "grid2.md")
	if err := os.WriteFile(md, []byte(r.Markdown()), 0o644); err != nil {
		return nil, err
	}
	writeCSV := func(path string, rows [][]string) error {
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		cw := csv.NewWriter(f)
		if err := cw.WriteAll(rows); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}
	rows := [][]string{grid2CSVHeaderFull()}
	for _, v := range r.Variants {
		s, x := v.Stats, v.Extra
		var pm, pl int
		if v.Portfolio != nil {
			pm, pl = v.Portfolio.MaxPos, v.Portfolio.DayLoss
		}
		rec := []string{v.ID, v.Signal, strconv.Itoa(s.Trades), g(100*s.WinRate, 1), g(s.AvgRGross, 4), g(s.AvgRNet, 4), g(s.MedRNet, 4), g(s.TotalRNet, 2), g(s.ExpUSDNet, 2),
			g(s.CILo, 4), g(s.CIHi, 4), g(s.AvgBars, 2), strconv.Itoa(v.Exits.Stop), strconv.Itoa(v.Exits.Trail), strconv.Itoa(v.Exits.Time), strconv.Itoa(v.Exits.EOD), strconv.Itoa(v.SkippedSize),
			g(x.MeanNetPct, 4), g(x.MeanRetPct, 4), strconv.Itoa(x.Excess.N), strconv.Itoa(x.Excess.Missing), g(x.Excess.Mean, 4), g(x.Excess.T, 2), g(x.Excess.MeanNet, 4), g(x.Excess.MeanBen, 4),
			g(x.MaxDDR, 2), g(x.WorstDayR, 2), x.WorstDay, strconv.Itoa(x.DaysTraded), strconv.Itoa(pm), strconv.Itoa(pl)}
		for _, h := range v.ByHour {
			rec = append(rec, strconv.Itoa(h.Trades), g(h.AvgRNet, 4))
		}
		rows = append(rows, rec)
	}
	cp := filepath.Join(dir, "grid2.csv")
	if err := writeCSV(cp, rows); err != nil {
		return nil, err
	}
	js, err := r.JSON()
	if err != nil {
		return nil, err
	}
	jp := filepath.Join(dir, "grid2.json")
	if err := os.WriteFile(jp, js, 0o644); err != nil {
		return nil, err
	}
	paths := []string{md, cp, jp}
	for _, v := range r.Variants {
		if v.ID != "D5" && v.ID != "P1" {
			continue
		}
		tr := [][]string{grid2TradeHeader}
		for _, t := range v.Trades {
			tr = append(tr, []string{t.Variant, t.Date, t.Sym, ETClock(t.CrossTs), ETClock(t.EntryTs), g(t.Entry, 4), g(t.Stop0, 4), g(t.Risk, 4), g(t.RiskPct, 3),
				ETClock(t.ExitTs), g(t.Exit, 4), t.Reason, strconv.Itoa(t.BarsHeld), strconv.FormatInt(t.Shares, 10), g(t.R, 3), g(t.RNet, 3), g(t.NetUSD, 2),
				g(t.RetPct, 4), g(TradeNetPct(t), 4), g(t.DayChg, 2), strconv.Itoa(t.BelowMin), strconv.FormatBool(t.BenchOK), g(t.BenchRet, 4), g(t.Excess, 4)})
		}
		p := filepath.Join(dir, "grid2_trades_"+v.ID+".csv")
		if err := writeCSV(p, tr); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, nil
}

// Markdown is grid2.md.
func (r Grid2Report) Markdown() string {
	var b strings.Builder
	m := r.Meta
	fmt.Fprintf(&b, "# Bull VWAP Cross: VWAP reclaim grid (%s)\n\n", r.Version)
	fmt.Fprintf(&b, "Generated %s. Test days %s .. %s (%d trading days). Command: `%s`\n\n", m.GeneratedAt, m.From, m.End, len(m.Days), m.Command)
	b.WriteString("Registered in code (Grid2 in internal/backtest/vwapgrid2.go) before any result was seen; derived from the October drift study, so **October is in-sample for this grid and September is the out-of-sample test**. Rows are sorted by variant id, NOT by performance; the confidence intervals understate the uncertainty (same-day trades are correlated, and the variants share trades).\n\n")
	p := m.Params
	fmt.Fprintf(&b, "Universe, sizing and costs as in grid v1: close > $%.0f, ADV10 >= %.0f, bar $vol >= %.0f, session volume >= %.0f, approx. RVOL >= %.1f; $%.0f risk per trade, $%.2f slippage + $%.3f commission per share each side (min $%.2f/order), $%.0f position cap (trades over it are skipped_size). Entry = open of the bar after the cross.\n\n",
		p.MinPrice, p.MinAdv10, p.MinBarDvol, p.MinSessVol, p.MinRvol, p.RiskUSD, p.SlipPerShare, p.CommPerShare, p.CommMinPerOrd, p.MaxPosUSD)
	fmt.Fprintf(&b, "Benchmark: %s, %d test days with bars; **days without benchmark bars: %d** %s. Trades on those days are left out of the excess columns (counted in `ex n/missing`).\n\n", r.BenchSym, r.BenchDays, len(r.BenchMiss), listDays(r.BenchMiss))

	b.WriteString("## Variants\n\n")
	var vr [][]string
	for _, v := range r.Variants {
		vr = append(vr, []string{v.ID, v.Signal, v.Desc})
	}
	table(&b, []string{"Id", "Signal", "Rules"}, vr)

	b.WriteString("## Headline\n\n")
	var rows [][]string
	for _, v := range r.Variants {
		s, x := v.Stats, v.Extra
		rows = append(rows, []string{v.ID, strconv.Itoa(s.Trades), pct(s.WinRate), rr(s.AvgRGross), rr(s.AvgRNet), rr(s.MedRNet), rr(s.TotalRNet),
			fmt.Sprintf("%+.3f..%+.3f", s.CILo, s.CIHi), usdS(s.ExpUSDNet), fmt.Sprintf("%+.3f%%", x.MeanNetPct),
			fmt.Sprintf("%+.3f%% (t %.2f)", x.Excess.Mean, x.Excess.T), fmt.Sprintf("%d/%d", x.Excess.N, x.Excess.Missing),
			fmt.Sprintf("%.1fR", x.MaxDDR), fmt.Sprintf("%+.1fR %s", x.WorstDayR, x.WorstDay), strconv.Itoa(x.DaysTraded),
			fmt.Sprintf("%.1f", s.AvgBars), fmt.Sprintf("%d/%d/%d/%d", v.Exits.Stop, v.Exits.Trail, v.Exits.Time, v.Exits.EOD), strconv.Itoa(v.SkippedSize)})
	}
	table(&b, []string{"Id", "Trades", "Win", "Avg R gross", "Avg R net", "Median R net", "Total R net", "95% CI mean net R", "Net $/trade", "Mean net %/trade",
		"Mean excess % vs " + r.BenchSym, "ex n/missing", "Max DD (R)", "Worst day", "Days traded", "Bars held", "Exits stop/trail/time/eod", "skipped_size"}, rows)
	b.WriteString("Excess % = the trade's gross return % (exit vs entry price) minus the benchmark's return over the same minutes (open of the entry minute to the close of the exit minute); t = mean/(sd/sqrt(n)) treating trades as independent (they are not). Max DD = the largest peak-to-trough fall of the cumulative net-R curve with trades in exit-time order, starting at 0. Win = net P&L > 0.\n\n")

	b.WriteString("## Net R by entry hour (ET), average (trades)\n\n")
	head := []string{"Id"}
	head = append(head, vwHourLabels...)
	var hr [][]string
	for _, v := range r.Variants {
		row := []string{v.ID}
		for _, h := range v.ByHour {
			row = append(row, fmt.Sprintf("%s (%d)", rr(h.AvgRNet), h.Trades))
		}
		hr = append(hr, row)
	}
	table(&b, head, hr)

	b.WriteString("## Funnel and portfolio\n\n")
	var fr [][]string
	for _, v := range r.Variants {
		c := v.Counts
		pm, pl := "-", "-"
		if v.Portfolio != nil {
			pm, pl = strconv.Itoa(v.Portfolio.MaxPos), strconv.Itoa(v.Portfolio.DayLoss)
		}
		fr = append(fr, []string{v.ID, strconv.Itoa(c.Fired), strconv.Itoa(c.IgnoredOpen), strconv.Itoa(c.NoEntryBar), strconv.Itoa(c.SkippedGap), strconv.Itoa(v.SkippedSize), pm, pl})
	}
	table(&b, []string{"Id", "Signals after gates", "Ignored (position open)", "No entry bar / ATR", "skipped_gap", "skipped_size", "P1 skipped: max 3 open", "P1 skipped: day loss -3R"}, fr)
	fmt.Fprintf(&b, "P1 limits: max %d concurrent positions across all symbols, daily realised net R <= -%.0fR stops new entries.\n\n", r.Limits.MaxConcurrent, r.Limits.DayLossR)

	b.WriteString("## Approximations and caveats\n\n")
	for _, c := range VwGrid2Caveats() {
		b.WriteString("- " + c + "\n")
	}
	b.WriteString("\nBase-rule caveats of the single-rule backtest also apply:\n\n")
	for _, c := range VwCaveats() {
		b.WriteString("- " + c + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

func listDays(d []string) string {
	if len(d) == 0 {
		return ""
	}
	return "(" + strings.Join(d, ", ") + ")"
}

// VwGrid2Caveats are the grid v2 specific approximations.
func VwGrid2Caveats() []string {
	return []string{
		"Signals are the drift study's detectors (intrabar 1-minute; D7: the 5m EMA9-over-VWAP candle cross). Context gates (dayChg, belowMin, entry window) filter these recorded signals, so a gated-out signal still consumes the side, as in the drift study; grid v1's filters do not. The funnel therefore has no universe-reject breakdown and does not count signals dropped for lack of an entry bar.",
		"dayChg = the cross bar's close (5m: the candle's close) over the previous regular-hours close, as in the drift study (not the live price at entry). belowMin = consecutive 1-minute bars (5m: candles x5) before the cross with EMA <= VWAP. Entry window = the entry bar's start in [10:00, 12:00).",
		"H1/H2 stop = the cross bar's low (5m: the candle's low), fixed, never trailed. H3 stop = entry - 1.5 x ATR14 (1m Wilder, as of the cross bar). H4 stop = the lowest low of the regular-hours bars from 09:30 through the cross bar, minus $0.01; skipped (skipped_gap) if >= entry. A stop fills at the stop, or at the open if the bar gaps through it. The stop is tested first on every bar including the entry bar.",
		"Time exit (H2, D2, D6): the close of the last bar starting at or before entry + 60 minutes (exit reason `time`); a stop hit earlier wins; the day ending first is `eod`.",
		"R is measured against each variant's own stop, so R of different stop rules is not like-for-like; every trade risks the same $50 before the cap, so $ and total R are comparable. A wide ATR stop (H3) makes small positions: the percent columns are per share and ignore the size.",
		"Excess returns compare the gross price return of the trade with the benchmark over the same minutes; costs are only in the net columns. Benchmark minute bars come from the same cache/fetcher; a trade whose entry minute has no benchmark bar is excluded from the excess columns.",
		"P1 is a second pass over D5's sized trades (see the file header of vwapgrid2.go): per day by entry time then symbol; a position is open until the end of its exit bar; the daily loss limit counts the realised net R of trades already closed at the entry; the loss limit is tested before the concurrency limit. Trades dropped by P1 still blocked their symbol in D5's own pass.",
		"R0 is grid v1's B0 (trailing previous-candle-low stop) unchanged: it is the reference and must equal the B0 row of a grid v1 run on the same days.",
	}
}
