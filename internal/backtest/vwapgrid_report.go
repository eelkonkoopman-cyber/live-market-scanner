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

// GridExits counts the sized trades by exit reason.
type GridExits struct {
	Stop  int `json:"stop"`
	Trail int `json:"trail"`
	TP    int `json:"tp"`
	EOD   int `json:"eod"`
}

// GridHour is one hour bucket of a variant (net R, capped and uncapped).
type GridHour struct {
	Label        string  `json:"label"`
	Trades       int     `json:"trades"`
	AvgRNet      float64 `json:"avgRNet"`
	TotalRNet    float64 `json:"totalRNet"`
	TradesNoCap  int     `json:"tradesNoCap"`
	AvgRNetNoCap float64 `json:"avgRNetNoCap"`
}

// GridVariant is one variant's result: the headline with the $25k cap, and the
// same statistics with the cap removed.
type GridVariant struct {
	ID          string     `json:"id"`
	Desc        string     `json:"desc"`
	Signal      string     `json:"signal"`
	Stats       VwStats    `json:"stats"`
	StatsNoCap  VwStats    `json:"statsNoCap"`
	Exits       GridExits  `json:"exits"`
	SkippedSize int        `json:"skippedSize"`
	Unfilled    int        `json:"unfilled"`
	Counts      VwCounts   `json:"counts"`
	ByHour      []GridHour `json:"byEntryHour"`
}

// GridReport is the whole grid result.
type GridReport struct {
	Version  string        `json:"gridVersion"`
	Meta     VwMeta        `json:"meta"`
	Variants []GridVariant `json:"variants"` // sorted by id, NOT by performance
}

// WithinCap reports whether a (no-cap) simulated trade fits the position cap.
func WithinCap(t VwTrade, maxPos float64) bool {
	return t.Sized && float64(t.Shares)*t.Entry <= maxPos
}

// BuildGridVariant summarizes the trades of one rule set. trades come from a
// simulation run with the position cap removed (Sized = at least one share);
// the capped headline keeps those whose position is within p.MaxPosUSD.
func BuildGridVariant(r VwapRules, trades []VwTrade, cnt VwCounts, p VwapParams, o StatOpts) GridVariant {
	var nocap, capped []VwTrade
	for _, t := range trades {
		if !t.Sized {
			continue
		}
		nocap = append(nocap, t)
		if WithinCap(t, p.MaxPosUSD) {
			capped = append(capped, t)
		}
	}
	sortTrades := func(ts []VwTrade) {
		sort.SliceStable(ts, func(i, j int) bool {
			a, b := ts[i], ts[j]
			if a.Date != b.Date {
				return a.Date < b.Date
			}
			if a.CrossTs != b.CrossTs {
				return a.CrossTs < b.CrossTs
			}
			return a.Sym < b.Sym
		})
	}
	sortTrades(capped)
	sortTrades(nocap)
	v := GridVariant{ID: r.ID, Desc: r.Desc, Signal: r.Signal, Counts: cnt, Unfilled: cnt.Unfilled}
	v.Counts.Traded = len(capped)
	v.SkippedSize = len(trades) - len(capped)
	v.Counts.SkippedSize = v.SkippedSize
	v.Stats = SummarizeVwap(capped, o)
	v.StatsNoCap = SummarizeVwap(nocap, o)
	for _, t := range capped {
		switch t.Reason {
		case "stop":
			v.Exits.Stop++
		case "trail":
			v.Exits.Trail++
		case "tp":
			v.Exits.TP++
		default:
			v.Exits.EOD++
		}
	}
	hc := vwRows(capped, vwHourLabels, vwHour, false)
	hn := vwRows(nocap, vwHourLabels, vwHour, false)
	for i, row := range hc {
		v.ByHour = append(v.ByHour, GridHour{
			Label: row.Label, Trades: row.Trades, AvgRNet: row.AvgRNet, TotalRNet: row.AvgRNet * float64(row.Trades),
			TradesNoCap: hn[i].Trades, AvgRNetNoCap: hn[i].AvgRNet,
		})
	}
	return v
}

// JSON is grid.json.
func (r GridReport) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

var gridCSVHeader = []string{
	"variant", "signal", "trades", "win_rate_pct", "avg_r_gross", "avg_r_net", "median_r_net", "total_r_net", "net_usd_per_trade",
	"ci95_lo", "ci95_hi", "avg_bars_held", "exit_stop", "exit_trail", "exit_tp", "exit_eod", "skipped_size", "unfilled",
	"trades_no_cap", "avg_r_net_no_cap", "total_r_net_no_cap",
}

func gridCSVHeaderFull() []string {
	h := append([]string(nil), gridCSVHeader...)
	for _, l := range vwHourLabels {
		h = append(h, "hour_"+l+"_trades", "hour_"+l+"_avg_r_net")
	}
	return h
}

// WriteGridFiles writes grid.md, grid.csv and grid.json into dir.
func WriteGridFiles(dir string, r GridReport) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	r.Variants = append([]GridVariant(nil), r.Variants...)
	sort.SliceStable(r.Variants, func(i, j int) bool { return r.Variants[i].ID < r.Variants[j].ID })
	md := filepath.Join(dir, "grid.md")
	if err := os.WriteFile(md, []byte(r.Markdown()), 0o644); err != nil {
		return nil, err
	}
	cp := filepath.Join(dir, "grid.csv")
	f, err := os.Create(cp)
	if err != nil {
		return nil, err
	}
	cw := csv.NewWriter(f)
	g := func(v float64, prec int) string { return strconv.FormatFloat(finite(v), 'f', prec, 64) }
	_ = cw.Write(gridCSVHeaderFull())
	for _, v := range r.Variants {
		s, n := v.Stats, v.StatsNoCap
		rec := []string{v.ID, v.Signal, strconv.Itoa(s.Trades), g(100*s.WinRate, 1), g(s.AvgRGross, 4), g(s.AvgRNet, 4), g(s.MedRNet, 4), g(s.TotalRNet, 2), g(s.ExpUSDNet, 2),
			g(s.CILo, 4), g(s.CIHi, 4), g(s.AvgBars, 2), strconv.Itoa(v.Exits.Stop), strconv.Itoa(v.Exits.Trail), strconv.Itoa(v.Exits.TP), strconv.Itoa(v.Exits.EOD),
			strconv.Itoa(v.SkippedSize), strconv.Itoa(v.Unfilled), strconv.Itoa(n.Trades), g(n.AvgRNet, 4), g(n.TotalRNet, 2)}
		for _, h := range v.ByHour {
			rec = append(rec, strconv.Itoa(h.Trades), g(h.AvgRNet, 4))
		}
		_ = cw.Write(rec)
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
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
	jp := filepath.Join(dir, "grid.json")
	if err := os.WriteFile(jp, js, 0o644); err != nil {
		return nil, err
	}
	return []string{md, cp, jp}, nil
}

// Markdown is grid.md.
func (r GridReport) Markdown() string {
	var b strings.Builder
	m := r.Meta
	fmt.Fprintf(&b, "# Bull VWAP Cross: rule grid (%s)\n\n", r.Version)
	fmt.Fprintf(&b, "Generated %s. Test days %s .. %s (%d trading days). Command: `%s`\n\n", m.GeneratedAt, m.From, m.End, len(m.Days), m.Command)
	b.WriteString("The variants were registered in code before any result was seen (VwapGrid in internal/backtest/vwapgrid.go). **Rows are sorted by variant id, NOT by performance**: with 19 variants some will look good by chance, so read the confidence intervals (which understate the uncertainty: trades on one day are correlated) and the multiple-comparison problem before picking one.\n\n")
	p := m.Params
	fmt.Fprintf(&b, "Every variant uses the existing universe (close > $%.0f, ADV10 >= %.0f, bar $vol >= %.0f, session volume >= %.0f, approx. RVOL >= %.1f), $%.0f risk per trade and the cost model ($%.2f slippage + $%.3f commission per share each side, min $%.2f/order). The headline columns apply the $%.0f position cap (trades over it are counted in skipped_size and left out); the `no cap` columns remove it (position flow is identical, only the sizing filter differs).\n\n",
		p.MinPrice, p.MinAdv10, p.MinBarDvol, p.MinSessVol, p.MinRvol, p.RiskUSD, p.SlipPerShare, p.CommPerShare, p.CommMinPerOrd, p.MaxPosUSD)

	b.WriteString("## Variants\n\n")
	var vr [][]string
	for _, v := range r.Variants {
		vr = append(vr, []string{v.ID, v.Signal, v.Desc})
	}
	table(&b, []string{"Id", "Signal", "Rules"}, vr)

	b.WriteString("## Headline (with the position cap)\n\n")
	var rows [][]string
	for _, v := range r.Variants {
		s, n := v.Stats, v.StatsNoCap
		rows = append(rows, []string{v.ID, strconv.Itoa(s.Trades), pct(s.WinRate), rr(s.AvgRGross), rr(s.AvgRNet), rr(s.MedRNet), rr(s.TotalRNet),
			usdS(s.ExpUSDNet), fmt.Sprintf("%+.3f..%+.3f", s.CILo, s.CIHi), fmt.Sprintf("%.1f", s.AvgBars),
			fmt.Sprintf("%d/%d/%d/%d", v.Exits.Stop, v.Exits.Trail, v.Exits.TP, v.Exits.EOD), strconv.Itoa(v.SkippedSize), strconv.Itoa(v.Unfilled),
			strconv.Itoa(n.Trades), rr(n.AvgRNet)})
	}
	table(&b, []string{"Id", "Trades", "Win", "Avg R gross", "Avg R net", "Median R net", "Total R net", "Net $/trade", "95% CI mean net R", "Bars held", "Exits stop/trail/tp/eod", "skipped_size", "unfilled", "Trades (no cap)", "Net R (no cap)"}, rows)
	b.WriteString("Exit reasons: stop = the initial stop was hit; trail = a raised stop was hit; tp = a take-profit filled; eod = flat at the 15:59 close. Win = net P&L > 0.\n\n")

	b.WriteString("## Net R by entry hour (ET), average (trades), with the cap | no cap\n\n")
	head := []string{"Id"}
	head = append(head, vwHourLabels...)
	var hr [][]string
	for _, v := range r.Variants {
		row := []string{v.ID}
		for _, h := range v.ByHour {
			row = append(row, fmt.Sprintf("%s (%d) | %s (%d)", rr(h.AvgRNet), h.Trades, rr(h.AvgRNetNoCap), h.TradesNoCap))
		}
		hr = append(hr, row)
	}
	table(&b, head, hr)

	b.WriteString("## Funnel per variant\n\n")
	var fr [][]string
	for _, v := range r.Variants {
		c := v.Counts
		var keys []string
		for k := range c.UniverseReject {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		rej := ""
		for _, k := range keys {
			rej += fmt.Sprintf("%s %d; ", k, c.UniverseReject[k])
		}
		fr = append(fr, []string{v.ID, strconv.Itoa(c.Fired), strings.TrimSuffix(rej, "; "), strconv.Itoa(c.IgnoredOpen), strconv.Itoa(c.NoEntryBar), strconv.Itoa(c.SkippedGap), strconv.Itoa(v.SkippedSize), strconv.Itoa(v.Unfilled)})
	}
	table(&b, []string{"Id", "Fired", "Rejects (first failing test)", "Ignored (position open)", "No entry bar", "skipped_gap", "skipped_size", "unfilled"}, fr)

	b.WriteString("## Approximations and caveats\n\n")
	for _, c := range VwGridCaveats() {
		b.WriteString("- " + c + "\n")
	}
	b.WriteString("\nBase-rule caveats of the single-rule backtest also apply:\n\n")
	for _, c := range VwCaveats() {
		b.WriteString("- " + c + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

// VwGridCaveats are the grid-specific approximations.
func VwGridCaveats() []string {
	return []string{
		"Filters F1..F4 behave like universe tests: a signal they reject does nothing and does not consume the side, so a later bar can still fire. F1: the entry bar (the minute after the cross bar) must start at or after 10:00; under U2 the gate is on the same cross-bar minute.",
		"F2: cross-bar volume >= 2x the mean volume of the 9 bars before it in the series (the bars present, premarket and the previous day included; minutes without a trade have no bar). F3: session VWAP at the end of the cross bar > the VWAP 10 bars (series positions) earlier, both inside 09:30-16:00.",
		"F4: 5-minute candles are clock-aligned buckets built from the 1-minute bars (previous day included); the EMA9 is over 5-minute closes (SMA seed). The candle used is the last one COMPLETE at the signal time (its bucket ended at or before the start of the cross bar for the intrabar signal, at the end of the cross bar for the close signal): no lookahead, the forming candle is not used.",
		"X1: after a bar closes the stop is raised to the lower of that bar's low and the previous bar's low (the previous-candle-low trail is the 1-candle version). X2: after each closed bar the stop is raised to EMA9(close, 1-minute, the live series) x (1 - 0.05%). X6: the trail starts after a bar's high has touched entry + 1R (R = the initial risk); that bar's own low is already trailed to when it closes.",
		"Fixed take-profits (X3, X4, X5, S4, U*) fill at exactly the TP price, even if a bar gaps over it; a bar whose range reaches both the stop and the TP counts as a STOP (conservative). The stop is evaluated first on every bar, including the entry/fill bar.",
		"S2/S4: ATR14 is the 1-minute Wilder ATR (SMA seed of the first 14 true ranges) over all bars up to and including the cross bar (extended hours and the previous day included). Signals with fewer than 14 bars of history are counted under 'no entry bar'.",
		"S1: the VWAP used is the cross bar's end-of-bar session VWAP, as for the signal itself.",
		"U1/U2 use the CLOSE signal (the limit price is the cross candle's close, so the candle must be complete), U1i the intrabar signal with the same order rules. The limit sits at the cross candle's close; it fills on the first of the next 3 bars whose low <= the limit, at the limit (at the bar's open if it opens below). Unfilled orders are counted (unfilled) and are not trades; the signal is consumed anyway. The stop is cross low - $0.01 and a real stop (fills at the stop, or the open if gapped through); the fill bar itself may stop the position out (assumed filled first). Risk and size derive from fill - stop. A fill at or below the stop is a skipped_gap.",
		"R is measured against each variant's own initial stop; R and net R of different stop rules are therefore not like-for-like in dollars of risk, but every trade risks the same $50 before the cap, so $ and total R are comparable.",
		"Costs are the flat per-share model of the base backtest; limit orders are assumed to fill with the same $0.01 slippage as market orders (no price improvement, no queue position, no partial fills): optimistic for U*, since a touch of the limit is not a fill in a real book.",
	}
}
