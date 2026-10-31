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

// VwDayPrep is one test day's funnel.
type VwDayPrep struct {
	Date      string      `json:"date"`
	Prefilter VwPrefStats `json:"prefilter"`
	Fetched   int         `json:"fetched"` // ticker-days whose minute bars were in hand
	Adjusted  int         `json:"splitAdjusted"`
}

// VwMeta is the run's header.
type VwMeta struct {
	GeneratedAt string      `json:"generatedAt"`
	Command     string      `json:"command"`
	From        string      `json:"from"`
	End         string      `json:"end"`
	Days        []string    `json:"days"`
	Params      VwapParams  `json:"params"`
	Prefilter   VwPrefOpts  `json:"prefilter"`
	Prep        []VwDayPrep `json:"prep"`
	Data        DataStats   `json:"data"`
	Resamples   int         `json:"bootstrapResamples"`
	Seed        uint64      `json:"bootstrapSeed"`
}

// VwRow is a bucket of a split or distribution.
type VwRow struct {
	Label    string  `json:"label"`
	Trades   int     `json:"trades"`
	WinRate  float64 `json:"winRate"` // net > 0
	AvgRGros float64 `json:"avgRGross"`
	AvgRNet  float64 `json:"avgRNet"`
	NetUSD   float64 `json:"totalNetUSD"`
}

// VwStats are the headline statistics of one variant's sized trades.
type VwStats struct {
	Trades      int     `json:"trades"`
	Wins        int     `json:"wins"` // net > 0
	WinRate     float64 `json:"winRate"`
	AvgRGross   float64 `json:"avgRGross"`
	AvgRNet     float64 `json:"avgRNet"`
	MedRGross   float64 `json:"medianRGross"`
	MedRNet     float64 `json:"medianRNet"`
	TotalRGross float64 `json:"totalRGross"`
	TotalRNet   float64 `json:"totalRNet"`
	GrossUSD    float64 `json:"totalGrossUSD"`
	NetUSD      float64 `json:"totalNetUSD"`
	CostUSD     float64 `json:"totalCostUSD"`
	ExpUSDNet   float64 `json:"expectancyNetUSDPerTrade"`
	CILo        float64 `json:"netRCI95Lo"` // 95% bootstrap CI of the mean net R
	CIHi        float64 `json:"netRCI95Hi"`
	AvgBars     float64 `json:"avgBarsHeld"`
	MedBars     float64 `json:"medianBarsHeld"`
	PctStop     float64 `json:"pctExitStop"`
	PctEOD      float64 `json:"pctExitEod"`
}

// VwVariantReport is one signal variant's whole result.
type VwVariantReport struct {
	Variant     string    `json:"variant"`
	Counts      VwCounts  `json:"counts"`
	Stats       VwStats   `json:"stats"`
	RBuckets    []VwRow   `json:"rBuckets"` // gross R distribution
	ByHour      []VwRow   `json:"byEntryHour"`
	ByRisk      []VwRow   `json:"byRiskPct"`
	Best        []VwTrade `json:"best10"`
	Worst       []VwTrade `json:"worst10"`
	UnsizedGrsR float64   `json:"skippedSizeAvgRGross"` // avg gross R of the trades dropped by the sizing rule
	Trades      []VwTrade `json:"-"`
}

// VwReport is the whole result.
type VwReport struct {
	Meta     VwMeta            `json:"meta"`
	Variants []VwVariantReport `json:"variants"`
}

var (
	vwRBucketLabels = []string{"<= -1R", "-1R..0", "0..1R", "1R..2R", "2R..5R", "> 5R"}
	vwHourLabels    = []string{"09:30-10:00", "10:00-11:00", "11:00-12:00", "12:00-14:00", "14:00-16:00"}
	vwRiskLabels    = []string{"< 0.2%", "0.2-0.5%", "0.5-1%", "> 1%"}
)

func vwRBucket(r float64) string {
	switch {
	case r <= -1:
		return vwRBucketLabels[0]
	case r <= 0:
		return vwRBucketLabels[1]
	case r <= 1:
		return vwRBucketLabels[2]
	case r <= 2:
		return vwRBucketLabels[3]
	case r <= 5:
		return vwRBucketLabels[4]
	}
	return vwRBucketLabels[5]
}

func vwHour(t VwTrade) string {
	m := t.EntryMin
	switch {
	case m < 10*60:
		return vwHourLabels[0]
	case m < 11*60:
		return vwHourLabels[1]
	case m < 12*60:
		return vwHourLabels[2]
	case m < 14*60:
		return vwHourLabels[3]
	}
	return vwHourLabels[4]
}

func vwRisk(t VwTrade) string {
	switch {
	case t.RiskPct < 0.2:
		return vwRiskLabels[0]
	case t.RiskPct < 0.5:
		return vwRiskLabels[1]
	case t.RiskPct < 1:
		return vwRiskLabels[2]
	}
	return vwRiskLabels[3]
}

func vwRows(trades []VwTrade, labels []string, key func(VwTrade) string, byGross bool) []VwRow {
	rows := make([]VwRow, len(labels))
	idx := map[string]int{}
	for i, l := range labels {
		rows[i].Label = l
		idx[l] = i
	}
	wins := make([]int, len(labels))
	for _, t := range trades {
		var i int
		if byGross {
			i = idx[vwRBucket(t.R)]
		} else {
			i = idx[key(t)]
		}
		rows[i].Trades++
		rows[i].AvgRGros += t.R
		rows[i].AvgRNet += t.RNet
		rows[i].NetUSD += t.NetUSD
		if t.NetUSD > 0 {
			wins[i]++
		}
	}
	for i := range rows {
		if n := float64(rows[i].Trades); n > 0 {
			rows[i].WinRate = float64(wins[i]) / n
			rows[i].AvgRGros /= n
			rows[i].AvgRNet /= n
		}
	}
	return rows
}

// SummarizeVwap computes the statistics of the SIZED trades (all passed in).
func SummarizeVwap(trades []VwTrade, o StatOpts) VwStats {
	var s VwStats
	s.Trades = len(trades)
	if s.Trades == 0 {
		return s
	}
	rg, rn, bars := make([]float64, 0, s.Trades), make([]float64, 0, s.Trades), make([]float64, 0, s.Trades)
	stops := 0
	for _, t := range trades {
		rg, rn, bars = append(rg, t.R), append(rn, t.RNet), append(bars, float64(t.BarsHeld))
		s.TotalRGross += t.R
		s.TotalRNet += t.RNet
		s.GrossUSD += t.GrossUSD
		s.NetUSD += t.NetUSD
		s.CostUSD += t.CostUSD
		if t.NetUSD > 0 {
			s.Wins++
		}
		if t.Reason == "stop" {
			stops++
		}
	}
	n := float64(s.Trades)
	s.WinRate = float64(s.Wins) / n
	s.AvgRGross, s.AvgRNet = s.TotalRGross/n, s.TotalRNet/n
	s.MedRGross, s.MedRNet = median(rg), median(rn)
	s.ExpUSDNet = s.NetUSD / n
	s.CILo, s.CIHi = BootstrapMeanCI(rn, o.Resamples, o.Seed)
	var bs float64
	for _, b := range bars {
		bs += b
	}
	s.AvgBars, s.MedBars = bs/n, median(bars)
	s.PctStop = 100 * float64(stops) / n
	s.PctEOD = 100 - s.PctStop
	return s
}

// BuildVwapReport assembles a variant's report from its trades (sized and not)
// and counters.
func BuildVwapReport(variant string, all []VwTrade, cnt VwCounts, o StatOpts) VwVariantReport {
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		if a.CrossTs != b.CrossTs {
			return a.CrossTs < b.CrossTs
		}
		return a.Sym < b.Sym
	})
	var sized []VwTrade
	var uns, unsR float64
	for _, t := range all {
		if t.Sized {
			sized = append(sized, t)
		} else {
			uns++
			unsR += t.R
		}
	}
	v := VwVariantReport{Variant: variant, Counts: cnt, Trades: all}
	if uns > 0 {
		v.UnsizedGrsR = unsR / uns
	}
	v.Stats = SummarizeVwap(sized, o)
	v.RBuckets = vwRows(sized, vwRBucketLabels, nil, true)
	v.ByHour = vwRows(sized, vwHourLabels, vwHour, false)
	v.ByRisk = vwRows(sized, vwRiskLabels, vwRisk, false)
	ranked := append([]VwTrade(nil), sized...)
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].NetUSD > ranked[j].NetUSD })
	for i := 0; i < len(ranked) && i < 10; i++ {
		v.Best = append(v.Best, ranked[i])
	}
	for i := len(ranked) - 1; i >= 0 && len(v.Worst) < 10; i-- {
		v.Worst = append(v.Worst, ranked[i])
	}
	return v
}

// JSON is summary.json.
func (r VwReport) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

var vwCSVHeader = []string{
	"variant", "date", "sym", "cross_et", "entry_et", "entry", "initial_stop", "risk_per_share", "risk_pct",
	"exit_et", "exit", "exit_reason", "bars_held", "r_gross", "return_pct", "cross_d", "bar_dollar_vol", "rvol_approx",
	"sized", "shares", "gross_usd", "cost_usd", "net_usd", "r_net", "split_factor",
}

// WriteVwapCSV writes every trade (sized or not) of every variant.
func WriteVwapCSV(path string, r VwReport) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	cw := csv.NewWriter(f)
	if err := cw.Write(vwCSVHeader); err != nil {
		f.Close()
		return err
	}
	g := func(v float64, prec int) string { return strconv.FormatFloat(finite(v), 'f', prec, 64) }
	for _, vr := range r.Variants {
		for _, t := range vr.Trades {
			rec := []string{
				t.Variant, t.Date, t.Sym, ETClock(t.CrossTs), ETClock(t.EntryTs), g(t.Entry, 4), g(t.Stop0, 4), g(t.Risk, 4), g(t.RiskPct, 3),
				ETClock(t.ExitTs), g(t.Exit, 4), t.Reason, strconv.Itoa(t.BarsHeld), g(t.R, 3), g(t.RetPct, 3), g(t.CrossD*100, 3), g(t.BarDvol, 0), g(t.Rvol, 2),
				strconv.FormatBool(t.Sized), strconv.FormatInt(t.Shares, 10), g(t.GrossUSD, 2), g(t.CostUSD, 2), g(t.NetUSD, 2), g(t.RNet, 3), g(t.SplitFact, 4),
			}
			if err := cw.Write(rec); err != nil {
				f.Close()
				return err
			}
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// WriteVwapFiles writes report.md, trades.csv and summary.json into dir.
func WriteVwapFiles(dir string, r VwReport) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	md := filepath.Join(dir, "report.md")
	if err := os.WriteFile(md, []byte(r.Markdown()), 0o644); err != nil {
		return nil, err
	}
	cp := filepath.Join(dir, "trades.csv")
	if err := WriteVwapCSV(cp, r); err != nil {
		return nil, err
	}
	js, err := r.JSON()
	if err != nil {
		return nil, err
	}
	jp := filepath.Join(dir, "summary.json")
	if err := os.WriteFile(jp, js, 0o644); err != nil {
		return nil, err
	}
	return []string{md, cp, jp}, nil
}

func vwRowTable(b *strings.Builder, title string, rows []VwRow) {
	b.WriteString("**" + title + "**\n\n")
	var out [][]string
	for _, r := range rows {
		out = append(out, []string{r.Label, strconv.Itoa(r.Trades), pct(r.WinRate), rr(r.AvgRGros), rr(r.AvgRNet), usdS(r.NetUSD)})
	}
	table(b, []string{"Bucket", "Trades", "Win rate (net)", "Avg R gross", "Avg R net", "Total net $"}, out)
}

func vwTradeTable(b *strings.Builder, title string, ts []VwTrade) {
	b.WriteString("**" + title + "**\n\n")
	var out [][]string
	for _, t := range ts {
		out = append(out, []string{t.Date, t.Sym, ETClock(t.CrossTs), ETClock(t.EntryTs), fmt.Sprintf("%.2f", t.Entry), fmt.Sprintf("%.2f", t.Stop0),
			ETClock(t.ExitTs) + " " + fmt.Sprintf("%.2f", t.Exit) + " " + t.Reason, rr(t.R), rr(t.RNet), usdS(t.NetUSD)})
	}
	table(b, []string{"Date", "Sym", "Cross", "Entry", "Entry px", "Stop", "Exit", "R gross", "R net", "Net $"}, out)
}

// Markdown is report.md.
func (r VwReport) Markdown() string {
	var b strings.Builder
	m := r.Meta
	b.WriteString("# Bull VWAP Cross backtest\n\n")
	fmt.Fprintf(&b, "Generated %s. Test days %s .. %s (%d trading days). Command: `%s`\n\n", m.GeneratedAt, m.From, m.End, len(m.Days), m.Command)
	b.WriteString("Rules: buy the OPEN of the candle after the cross; initial stop = the cross candle's low; the stop trails to each completed candle's low (never down); flat at the 15:59 close. One position per symbol; bull only; regular hours only.\n\n")
	p := m.Params
	fmt.Fprintf(&b, "Universe at the cross bar: close > $%.0f, ADV10 >= %.0f, bar $vol >= %.0f, shares since 04:00 >= %.0f, RVOL (approx.) >= %.1f. Sizing: $%.0f risk per trade (shares = floor(risk$ / risk per share)), trades with 0 shares or a position over $%.0f are skipped. Costs: $%.2f slippage per share each side + $%.3f/share commission each side (min $%.2f/order).\n\n",
		p.MinPrice, p.MinAdv10, p.MinBarDvol, p.MinSessVol, p.MinRvol, p.RiskUSD, p.MaxPosUSD, p.SlipPerShare, p.CommPerShare, p.CommMinPerOrd)

	b.WriteString("## Headline\n\n")
	var head [][]string
	for _, v := range r.Variants {
		s := v.Stats
		head = append(head, []string{v.Variant, strconv.Itoa(s.Trades), pct(s.WinRate), rr(s.AvgRGross), rr(s.AvgRNet), rr(s.MedRNet), rr(s.TotalRNet),
			usdS(s.ExpUSDNet), fmt.Sprintf("%+.3fR .. %+.3fR", s.CILo, s.CIHi), usdS(s.NetUSD)})
	}
	table(&b, []string{"Variant", "Trades", "Win rate", "Avg R gross", "Avg R net", "Median R net", "Total R net", "Expectancy net $/trade", "95% CI mean net R", "Total net $"}, head)
	b.WriteString("The intrabar variant is the primary (the alert as it now fires live). Win = net P&L > 0. Median R gross is in the per-variant tables. The CI resamples trades (" + strconv.Itoa(m.Resamples) + " resamples, seed " + strconv.FormatUint(m.Seed, 10) + "); trades on the same day are correlated, so treat it as optimistic.\n\n")

	for _, v := range r.Variants {
		s, c := v.Stats, v.Counts
		fmt.Fprintf(&b, "## Variant: %s\n\n", v.Variant)
		table(&b, []string{"Measure", "Value"}, [][]string{
			{"Trades (sized)", strconv.Itoa(s.Trades)},
			{"Win rate (net > 0)", pct(s.WinRate)},
			{"Avg R gross / net", rr(s.AvgRGross) + " / " + rr(s.AvgRNet)},
			{"Median R gross / net", rr(s.MedRGross) + " / " + rr(s.MedRNet)},
			{"Total R gross / net", rr(s.TotalRGross) + " / " + rr(s.TotalRNet)},
			{"Total gross / costs / net $", usdS(s.GrossUSD) + " / " + usd(s.CostUSD) + " / " + usdS(s.NetUSD)},
			{"Expectancy net $/trade", usdS(s.ExpUSDNet)},
			{"95% bootstrap CI of mean net R", fmt.Sprintf("%+.3fR .. %+.3fR", s.CILo, s.CIHi)},
			{"Bars held avg / median", fmt.Sprintf("%.1f / %.1f", s.AvgBars, s.MedBars)},
			{"Exits by stop / eod", fmt.Sprintf("%.1f%% / %.1f%%", s.PctStop, s.PctEOD)},
		})
		rej := ""
		var keys []string
		for k := range c.UniverseReject {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			rej += fmt.Sprintf("%s %d; ", k, c.UniverseReject[k])
		}
		fmt.Fprintf(&b, "Funnel: %d symbol-days simulated; %d signals fired; universe rejects after the geometry fired (first failing test): %s; ignored because a position in the symbol was open: %d; no entry bar (none or starting at/after 15:59): %d; skipped_gap (entry open <= stop): %d; skipped_size (shares < 1 or position > cap): %d (their avg gross R %+.2f, left out of every statistic); traded: %d.\n\n",
			c.SymbolDays, c.Fired, strings.TrimSuffix(rej, "; "), c.IgnoredOpen, c.NoEntryBar, c.SkippedGap, c.SkippedSize, v.UnsizedGrsR, c.Traded)
		vwRowTable(&b, "Distribution of gross R", v.RBuckets)
		vwRowTable(&b, "By hour of entry (ET)", v.ByHour)
		vwRowTable(&b, "By risk as % of price", v.ByRisk)
		vwTradeTable(&b, "Top 10 trades (net $)", v.Best)
		vwTradeTable(&b, "Bottom 10 trades (net $)", v.Worst)
	}

	b.WriteString("## Data and funnel\n\n")
	var rows [][]string
	var tu, tp, tk, tf int
	for _, d := range m.Prep {
		rows = append(rows, []string{d.Date, strconv.Itoa(d.Prefilter.Universe), strconv.Itoa(d.Prefilter.Passed), strconv.Itoa(d.Prefilter.Kept), strconv.Itoa(d.Fetched), strconv.Itoa(d.Adjusted)})
		tu += d.Prefilter.Universe
		tp += d.Prefilter.Passed
		tk += d.Prefilter.Kept
		tf += d.Fetched
	}
	rows = append(rows, []string{"total", strconv.Itoa(tu), strconv.Itoa(tp), strconv.Itoa(tk), strconv.Itoa(tf), ""})
	table(&b, []string{"Day", "Tickers", "Prefilter passed", "Kept (cap)", "Minute bars in hand", "Un-adjusted for a later split"}, rows)
	d := m.Data
	fmt.Fprintf(&b, "API: %d grouped-daily calls (%d from cache), %d minute-bar calls (%d from cache), %d splits-listing calls, %d rate-limited (429) retries, %d failures. Splits known: %v. Holidays: %s.\n\n",
		d.GroupedCalls, d.GroupedHits, d.MinuteCalls, d.MinuteHits, d.SplitsCalls, d.RateLimited, d.FailureCount, d.SplitsKnown, listOrNone(d.Holidays))

	b.WriteString("## Approximations and caveats\n\n")
	for _, c := range VwCaveats() {
		b.WriteString("- " + c + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

// VwCaveats lists the approximations, for the report.
func VwCaveats() []string {
	return []string{
		"VWAP is rebuilt from 1-minute bars as the cumulative sum of typical price (high+low+close)/3 times volume over volume, restarting at 09:30; the live VWAP uses every print. The feed's minute bars carry no per-bar VWAP through the bar type used here.",
		"The EMA is the live arithmetic (SMA seed of the first 9 closes, k = 0.2) over every 1-minute close of the previous day and the day, not reset at 04:00. Minutes without a trade have no bar and are skipped, as on the chart.",
		"intrabar: bar m fires when d(m-1) <= 0 and high(m)*0.2 + EMA(m-1)*0.8 > VWAP at the END of bar m. Assuming the poke happens at the bar's high and VWAP at the bar end is an approximation; the live check uses each print's forming close and the live VWAP. No 0.05% band, no universe wait.",
		"close: bar m fires when d(m-1) <= 0 and d(m) > 0 at the close.",
		"After a fire the bull is re-armed only after a LATER bar closes with d < 0. A cross that fails the universe does nothing and does not consume the side.",
		"RVOL is approximated as shares since 04:00 / (ADV10 x a fixed typical intraday cumulative-volume fraction at the end of the bar). The table (about 3% by 09:30, 15% by 10:00, 30% by 11:00, 45% by 12:00, 55% by 13:00, 65% by 14:00, 78% by 15:00, 100% by 16:00, linear in between) is one symbol-independent U-shape: names with a different shape are mis-measured. ADV10 is the mean of the previous 10 grouped daily volumes (extended hours included), the scanner's definition.",
		"Prefilter (which ticker-days get minute bars): day high > $25 (necessary for a cross-bar close above $25; the spec's day-close test would drop names that closed under $25 after crossing above it), ADV10 >= 1,000,000, whole-day volume >= 0.8 x ADV10 (a loose superset of RVOL >= 1.5 at some minute: whole-day volume reads the afternoon, but the test is loose enough that a qualifying early-morning surge is kept in practice; a name that spiked and then died on very low volume could be missed). No cap by default.",
		"Fills: entry exactly at the next candle's open, a stop hit fills at the stop (or the open if the bar gaps through it), plus $0.01/share slippage and IBKR fixed commission each side; there is no queueing, partial fill, halt, locate or short-sale rule. Small caps and fast movers will fill worse than this; a stop at the previous candle's low on a 1-minute chart is hit by noise.",
		"R is measured against the initial stop (entry - cross candle low). Net R = net $ / (shares x risk per share). Trades whose size would be 0 shares or over $25,000 are counted and listed in trades.csv (sized=false) but left out of every statistic.",
		"A signal arriving while a position in the same symbol is open is ignored (counted). Positions are not carried overnight.",
		"The sample is one month; every trade in a day shares the market's mood, so the bootstrap interval (resampling trades) understates the uncertainty.",
	}
}

// ConsoleSummary is the short stdout summary.
func (r VwReport) ConsoleSummary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "vwapcross %s..%s (%d days)\n", r.Meta.From, r.Meta.End, len(r.Meta.Days))
	for _, v := range r.Variants {
		s := v.Stats
		fmt.Fprintf(&b, "  %-9s trades %d  win %s  avgR gross %+.3f net %+.3f  95%% CI %+.3f..%+.3f  net %s\n",
			v.Variant, s.Trades, pct(s.WinRate), s.AvgRGross, s.AvgRNet, s.CILo, s.CIHi, usdS(s.NetUSD))
	}
	return b.String()
}
