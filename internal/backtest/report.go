package backtest

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// TunedDay is the one day the engine's parameters were tuned on (2026-10-05).
// It is inside the sample, so every variant is reported with it, without it, and
// on its own.
const TunedDay = "2026-10-05"

// Failure is one data fetch that did not succeed.
type Failure struct {
	Key string `json:"key"` // "SYM 2026-09-14" or "grouped 2026-09-14"
	Err string `json:"err"` // already scrubbed of the API key by internal/feed
}

// DataStats is the fetch budget and what went wrong; cmd/backtest fills it.
type DataStats struct {
	Dry          bool      `json:"dry"`
	GroupedCalls int       `json:"groupedCalls"`
	GroupedHits  int       `json:"groupedCacheHits"`
	MinuteCalls  int       `json:"minuteCalls"`
	MinuteHits   int       `json:"minuteCacheHits"`
	RateLimited  int       `json:"rateLimited429"`
	MissingDry   int       `json:"missingFromCache"` // -dry: ticker-days that were not cached and so were not run
	Failures     []Failure `json:"failures"`
	FailureCount int       `json:"failureCount"`
	Holidays     []string  `json:"holidays"`              // weekdays whose grouped result was empty
	HistoryDays  []string  `json:"historyDays"`           // the earlier days used for adv10 only
	HistoryNote  string    `json:"historyNote,omitempty"` // set when a test day had fewer than 10 earlier days

	// Splits: the feed's bars are split-adjusted as of the fetch; the run un-adjusts
	// the names that split after each test day (splits.go).
	SplitsCalls    int    `json:"splitsCalls"`
	SplitsHits     int    `json:"splitsCacheHits"`
	SplitsKnown    bool   `json:"splitsKnown"`          // false: the listing was not available and NOTHING was un-adjusted
	SplitsFrom     string `json:"splitsFrom,omitempty"` // the listing covers execution dates from here ...
	SplitsAsOf     string `json:"splitsAsOf,omitempty"` // ... to here (the date the data was adjusted to)
	SplitsInWindow int    `json:"splitsInWindow"`       // usable splits in the listing
	SplitsNote     string `json:"splitsNote,omitempty"` // why they are not known
}

// DayPrep is one day's funnel, from the whole market to the signals.
type DayPrep struct {
	Date          string         `json:"date"`
	Prefilter     PrefilterStats `json:"prefilter"`
	Fetched       int            `json:"fetched"`       // candidate-days whose minute bars were in hand
	SplitAdjusted int            `json:"splitAdjusted"` // of those, names un-adjusted for a split executed after the day
	Admitted      int            `json:"admitted"`      // pmVolX >= 1 in the pre-market
	CapDropped    int            `json:"capDropped"`    // beyond the live cap of 80
	WithSignals   int            `json:"withSignals"`   // admitted names that had a LONG (FLAG) after admission
	Signals       int            `json:"signals"`
}

// RunMeta is everything about the run that is not a variant's result.
type RunMeta struct {
	GeneratedAt string        `json:"generatedAt"`
	EndDate     string        `json:"endDate"`
	Days        []string      `json:"days"`
	Command     string        `json:"command"`
	Data        DataStats     `json:"data"`
	Prefilter   PrefilterOpts `json:"prefilter"` // the screen this run used
	Prep        []DayPrep     `json:"prep"`
	Resamples   int           `json:"bootstrapResamples"`
	Seed        uint64        `json:"bootstrapSeed"`
}

// SplitImpact is what the names that split after their day contributed to one
// variant (their trades are on the day's basis, not the feed's adjusted one).
type SplitImpact struct {
	Trades       int     `json:"trades"`
	TotalNet     float64 `json:"totalNetUSD"`
	AvgNet       float64 `json:"avgNetUSD"`
	OthersAvgNet float64 `json:"othersAvgNetUSD"` // the mean net $ per trade of every other trade
	CostGap      float64 `json:"costGapUSD"`      // commission + fees on the real share counts minus what the adjusted prices would have charged
}

// VariantReport is one variant's numbers.
type VariantReport struct {
	Name     string         `json:"name"`
	Desc     string         `json:"desc"`
	Rules    Rules          `json:"rules"`
	All      Summary        `json:"all"`
	ExTuned  Summary        `json:"excludingTunedDay"`
	OnlyTune Summary        `json:"tunedDayOnly"`
	Splits   Splits         `json:"splits"`
	Skips    map[string]int `json:"skippedSignals"`
	Seen     int            `json:"signalsSeen"`
	Halted   int            `json:"haltedDays"`

	SplitImpact SplitImpact `json:"splitAfterDay"`
}

// Report is the whole result.
type Report struct {
	Meta     RunMeta         `json:"meta"`
	Variants []VariantReport `json:"variants"`
}

// BuildReport computes every statistic of results.
func BuildReport(meta RunMeta, results []VariantResult, o StatOpts) Report {
	meta.Resamples, meta.Seed = o.Resamples, o.Seed
	rep := Report{Meta: meta}
	days := meta.Days
	var exDays, onlyDays []string
	for _, d := range days {
		if d == TunedDay {
			onlyDays = append(onlyDays, d)
		} else {
			exDays = append(exDays, d)
		}
	}
	for _, vr := range results {
		var ex, only []Trade
		for _, t := range vr.Trades {
			if t.Date == TunedDay {
				only = append(only, t)
			} else {
				ex = append(ex, t)
			}
		}
		halted := 0
		for _, d := range vr.Days {
			if d.Halted {
				halted++
			}
		}
		var si SplitImpact
		var othersNet float64
		for _, t := range vr.Trades {
			if t.SplitFactor > 0 && t.SplitFactor != 1 {
				si.Trades++
				si.TotalNet += t.Net
				si.CostGap += t.SplitCostGap
			} else {
				othersNet += t.Net
			}
		}
		if si.Trades > 0 {
			si.AvgNet = si.TotalNet / float64(si.Trades)
		}
		if n := len(vr.Trades) - si.Trades; n > 0 {
			si.OthersAvgNet = othersNet / float64(n)
		}
		rep.Variants = append(rep.Variants, VariantReport{
			Name: vr.Rules.Name, Desc: vr.Rules.Desc, Rules: vr.Rules,
			All:      Summarize("all days", vr.Trades, days, o),
			ExTuned:  Summarize("excluding "+TunedDay, ex, exDays, o),
			OnlyTune: Summarize(TunedDay+" only", only, onlyDays, o),
			Splits:   SplitAll(vr.Trades),
			Skips:    vr.Skips, Seen: vr.Seen, Halted: halted,
			SplitImpact: si,
		})
	}
	return rep
}

// JSON is summary.json.
func (r Report) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

// ─── trades.csv ──────────────────────────────────────────────────────────

var csvHeader = []string{
	"variant", "date", "sym", "entry_et", "exit_et", "exit_kind", "shares", "trigger", "stop",
	"entry_raw", "entry_fill", "exit_raw", "exit_fill", "gross_usd", "commission_usd", "fees_usd",
	"slippage_usd", "net_usd", "initial_risk_usd", "r_gross", "r_net", "pmvolx", "adv10", "split_factor",
}

// splitCell is the split_factor column: 1 for no split (or a trade with no candidate).
func splitCell(f float64) string {
	if !(f > 0) {
		f = 1
	}
	return strconv.FormatFloat(finite(f), 'g', -1, 64)
}

// WriteTradesCSV writes every trade of every variant.
func WriteTradesCSV(w io.Writer, results []VariantResult) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(csvHeader); err != nil {
		return err
	}
	f := func(v float64, prec int) string { return strconv.FormatFloat(finite(v), 'f', prec, 64) }
	for _, vr := range results {
		for _, t := range vr.Trades {
			rec := []string{
				t.Variant, t.Date, t.Sym, ETClock(t.EntryTs), ETClock(t.ExitTs), t.ExitKind,
				strconv.FormatInt(t.Shares, 10), f(t.Trigger, 4), f(t.Stop, 4),
				f(t.EntryRaw, 4), f(t.EntryFill, 4), f(t.ExitRaw, 4), f(t.ExitFill, 4),
				f(t.Gross, 2), f(t.Commission, 2), f(t.Fees, 2), f(t.Slippage, 2), f(t.Net, 2),
				f(t.InitialRisk, 2), f(t.RGross, 3), f(t.RNet, 3), f(t.PmVolX, 2), f(t.Adv10, 0),
				splitCell(t.SplitFactor),
			}
			if err := cw.Write(rec); err != nil {
				return err
			}
		}
	}
	cw.Flush()
	return cw.Error()
}

// WriteFiles writes report.md, trades.csv and summary.json into dir (created if
// needed) and returns the three paths.
func WriteFiles(dir string, rep Report, results []VariantResult) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	md := filepath.Join(dir, "report.md")
	if err := os.WriteFile(md, []byte(rep.Markdown()), 0o644); err != nil {
		return nil, err
	}
	csvPath := filepath.Join(dir, "trades.csv")
	cf, err := os.Create(csvPath)
	if err != nil {
		return nil, err
	}
	if err := WriteTradesCSV(cf, results); err != nil {
		cf.Close()
		return nil, err
	}
	if err := cf.Close(); err != nil {
		return nil, err
	}
	js, err := rep.JSON()
	if err != nil {
		return nil, err
	}
	jsPath := filepath.Join(dir, "summary.json")
	if err := os.WriteFile(jsPath, js, 0o644); err != nil {
		return nil, err
	}
	return []string{md, csvPath, jsPath}, nil
}

// ─── Markdown ────────────────────────────────────────────────────────────

func usd(v float64) string {
	v = finite(v)
	if v < 0 {
		return fmt.Sprintf("-$%.2f", -v)
	}
	return fmt.Sprintf("$%.2f", v)
}
func usdS(v float64) string { // signed
	v = finite(v)
	if v < 0 {
		return fmt.Sprintf("-$%.2f", -v)
	}
	return fmt.Sprintf("+$%.2f", v)
}
func pct(v float64) string { return fmt.Sprintf("%.1f%%", 100*finite(v)) }
func rr(v float64) string  { return fmt.Sprintf("%+.2fR", finite(v)) }
func pf(p *float64) string {
	if p == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.2f", *p)
}
func ci(lo, hi float64) string { return fmt.Sprintf("%s .. %s", usdS(lo), usdS(hi)) }

// verdict says, in words, where a mean's interval sits against zero.
func verdict(lo, hi float64, n int) string {
	switch {
	case n == 0:
		return "no trades"
	case lo > 0:
		return "interval above zero"
	case hi < 0:
		return "interval below zero"
	}
	return "interval spans zero"
}

func table(b *strings.Builder, head []string, rows [][]string) {
	b.WriteString("| " + strings.Join(head, " | ") + " |\n")
	b.WriteString("|" + strings.Repeat(" --- |", len(head)) + "\n")
	for _, r := range rows {
		b.WriteString("| " + strings.Join(r, " | ") + " |\n")
	}
	b.WriteString("\n")
}

// headline is the table of variants for one subset.
func (r Report) headline(b *strings.Builder, pick func(VariantReport) Summary) {
	head := []string{"Variant", "Trades", "Win rate", "Avg net $/trade", "95% CI of the mean", "Avg gross $/trade", "Total net", "Profit factor", "Max drawdown", "Reading"}
	var rows [][]string
	for _, v := range r.Variants {
		s := pick(v)
		rows = append(rows, []string{
			v.Name, strconv.Itoa(s.Trades), pct(s.WinRate), usdS(s.AvgNet), ci(s.NetCILo, s.NetCIHi),
			usdS(s.AvgGross), usdS(s.TotalNet), pf(s.ProfitFactor), usd(s.MaxDrawdown), verdict(s.NetCILo, s.NetCIHi, s.Trades),
		})
	}
	table(b, head, rows)
}

func summaryRows(vs VariantReport) (perTrade, perDay [][]string) {
	for _, s := range []Summary{vs.All, vs.ExTuned, vs.OnlyTune} {
		perTrade = append(perTrade, []string{
			s.Label, strconv.Itoa(s.Trades), pct(s.WinRate),
			usdS(s.AvgNet), usdS(s.MedNet), rr(s.AvgRNet), ci(s.NetCILo, s.NetCIHi),
			usdS(s.AvgGross), usdS(s.MedGross), rr(s.AvgRGros), ci(s.GrossCILo, s.GrossCIHi),
			usdS(s.TotalNet), usdS(s.TotalGross), usd(s.Commission), usd(s.Slippage), pf(s.ProfitFactor),
		})
		perDay = append(perDay, []string{
			s.Label, strconv.Itoa(s.Days), strconv.Itoa(s.DaysWithTrade), fmt.Sprintf("%.0f%%", s.PctDaysWithTrade),
			fmt.Sprintf("%.2f", s.AvgTradesPerDay), usdS(s.DayMeanNet), ci(s.DayCILo, s.DayCIHi),
			usd(s.MaxDrawdown), bestWorst(s.BestDay, s.BestDayDate, s.DaysWithTrade), bestWorst(s.WorstDay, s.WorstDayDt, s.DaysWithTrade),
		})
	}
	return
}

func bestWorst(v float64, date string, n int) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprintf("%s (%s)", usdS(v), date)
}

func splitTable(b *strings.Builder, title string, rows []SplitRow) {
	fmt.Fprintf(b, "**%s**\n\n", title)
	var out [][]string
	for _, r := range rows {
		out = append(out, []string{r.Label, strconv.Itoa(r.Trades), pct(r.WinRate), usdS(r.AvgNet), usdS(r.AvgGross), rr(r.AvgRNet), usdS(r.TotalNet)})
	}
	table(b, []string{"Bucket", "Trades", "Win rate", "Avg net $", "Avg gross $", "Avg R (net)", "Total net"}, out)
}

// Markdown renders report.md.
func (r Report) Markdown() string {
	var b strings.Builder
	m := r.Meta
	first, last := "", ""
	if len(m.Days) > 0 {
		first, last = m.Days[0], m.Days[len(m.Days)-1]
	}
	fmt.Fprintf(&b, "# 9EMA flag auto-trader: backtest over %d trading days\n\n", len(m.Days))
	fmt.Fprintf(&b, "Sample: %s to %s (%d US trading days). Generated %s.\n\n", first, last, len(m.Days), m.GeneratedAt)
	if m.Data.Dry {
		b.WriteString("**DRY RUN: this used only what was already in the cache; ticker-days that were not cached were not run.**\n\n")
	}
	b.WriteString("The question: what is the expected value of this bot? Every variant replays the trader's actual rules ")
	b.WriteString("(internal/autotrade defaults) over the signals the 9EMA flag engine (internal/emaflag, the golden-tested port) would have called on every stock that qualified as a candidate that day. ")
	b.WriteString("All money figures are per trade unless said otherwise. \"Net\" is after commissions, fees and slippage; \"gross\" is before all three.\n\n")

	b.WriteString("## Expected value per trade, all days\n\n")
	r.headline(&b, func(v VariantReport) Summary { return v.All })
	fmt.Fprintf(&b, "## Expected value per trade, excluding %s\n\n", TunedDay)
	fmt.Fprintf(&b, "The engine's parameters were tuned on %s, which is inside the sample, so that day flatters the result. This is the cleaner number.\n\n", TunedDay)
	r.headline(&b, func(v VariantReport) Summary { return v.ExTuned })
	b.WriteString("How to read the interval: it is a 95% bootstrap interval of the mean net $ per trade (")
	fmt.Fprintf(&b, "%d resamples, seed %d). If it spans zero the sample cannot tell this bot from a coin flip with costs. ", m.Resamples, m.Seed)
	b.WriteString("Trades on the same day are not independent (the same hot tape feeds all of them), so each variant section also gives the interval of the mean net $ per DAY, which is the more honest one.\n\n")

	b.WriteString("## Data and method\n\n")
	r.dataSection(&b)

	for _, v := range r.Variants {
		fmt.Fprintf(&b, "## %s: %s\n\n", v.Name, v.Desc)
		pt, pd := summaryRows(v)
		b.WriteString("**Per trade**\n\n")
		table(&b, []string{"Set", "Trades", "Win rate", "Avg net", "Median net", "Avg R net", "95% CI net", "Avg gross", "Median gross", "Avg R gross", "95% CI gross", "Total net", "Total gross", "Commissions+fees", "Slippage cost", "Profit factor"}, pt)
		b.WriteString("**Per day**\n\n")
		table(&b, []string{"Set", "Days", "Days with a trade", "% of days", "Trades/day", "Mean net $/day", "95% CI $/day", "Max drawdown", "Best day", "Worst day"}, pd)
		fmt.Fprintf(&b, "Signals the engine called on admitted candidates: %d. Days the daily-loss halt tripped: %d.\n\n", v.Seen, v.Halted)
		if m.Data.SplitsKnown {
			si := v.SplitImpact
			if si.Trades == 0 {
				b.WriteString("Trades on names that split after the day: none.\n\n")
			} else {
				fmt.Fprintf(&b, "Trades on names that split after the day (prices, shares and costs restated to that day's basis, not the feed's adjusted one): %d, total net %s, average %s per trade; every other trade averages %s. Commissions and fees on the real share counts exceed what the adjusted prices would have charged by %s over these trades.\n\n",
					si.Trades, usdS(si.TotalNet), usdS(si.AvgNet), usdS(si.OthersAvgNet), usd(si.CostGap))
			}
		}
		b.WriteString("**Signals not traded, by reason**\n\n")
		var rows [][]string
		traded := v.All.Trades
		rows = append(rows, []string{"traded", strconv.Itoa(traded)})
		for _, why := range SkipReasons {
			if n := v.Skips[why]; n > 0 {
				rows = append(rows, []string{why, strconv.Itoa(n)})
			}
		}
		table(&b, []string{"Outcome", "Signals"}, rows)
		splitTable(&b, "By price band (trigger)", v.Splits.Price)
		splitTable(&b, "By entry time (ET)", v.Splits.Time)
		splitTable(&b, "By month", v.Splits.Month)
	}

	b.WriteString("## Caveats, in plain language\n\n")
	for _, c := range caveats() {
		b.WriteString("- " + c + "\n")
	}
	b.WriteString("\n")

	b.WriteString("## Appendix: the funnel, day by day\n\n")
	var rows [][]string
	for _, p := range m.Prep {
		rows = append(rows, []string{p.Date, strconv.Itoa(p.Prefilter.Universe), strconv.Itoa(p.Prefilter.Passed), strconv.Itoa(p.Prefilter.Dropped),
			strconv.Itoa(p.Fetched), strconv.Itoa(p.SplitAdjusted), strconv.Itoa(p.Admitted), strconv.Itoa(p.CapDropped), strconv.Itoa(p.WithSignals), strconv.Itoa(p.Signals)})
	}
	table(&b, []string{"Day", "Tickers", "Passed prefilter", "Lost to the prefilter cap", "Minute bars fetched", "Un-adjusted for a later split", "Admitted (pmVolX >= 1)", "Lost to the 80 cap", "Names with a signal", "Signals"}, rows)
	if n := len(m.Data.Failures); n > 0 {
		fmt.Fprintf(&b, "**Data failures (%d total, first %d shown)**\n\n", m.Data.FailureCount, n)
		for _, f := range m.Data.Failures {
			fmt.Fprintf(&b, "- %s: %s\n", f.Key, f.Err)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (r Report) dataSection(b *strings.Builder) {
	m := r.Meta
	var uni, passed, dropped, fetched, adm, capd, withSig, sigs, capDays, splitAdj int
	var noPrev, noAdv, outPrice, lowVol, noSpike int
	for _, p := range m.Prep {
		uni += p.Prefilter.Universe
		noPrev += p.Prefilter.NoPrev
		noAdv += p.Prefilter.NoAdv
		outPrice += p.Prefilter.OutOfPrice
		lowVol += p.Prefilter.LowVolume
		noSpike += p.Prefilter.NoSpike
		passed += p.Prefilter.Passed
		dropped += p.Prefilter.Dropped
		if p.Prefilter.Dropped > 0 {
			capDays++
		}
		fetched += p.Fetched
		splitAdj += p.SplitAdjusted
		adm += p.Admitted
		capd += p.CapDropped
		withSig += p.WithSignals
		sigs += p.Signals
	}
	pf := m.Prefilter
	if pf.MaxPrice == 0 { // a report built without the screen's settings: the defaults
		pf = DefaultPrefilter()
	}
	capTxt := fmt.Sprintf("the %d with the strongest volume ratio", pf.Cap)
	if pf.Cap <= 0 {
		capTxt = "all of them (no cap)"
	}
	d := m.Data
	fmt.Fprintf(b, "- Source: Massive REST through the scanner's own client (internal/feed). Grouped daily bars for every US stock on every day (%d calls, %d from cache), plus %d earlier trading days for the 10-day average volume. Holidays detected as an empty grouped result: %s.\n",
		d.GroupedCalls, d.GroupedHits, len(d.HistoryDays), listOrNone(d.Holidays))
	fmt.Fprintf(b, "- adv10 = mean daily volume over the 10 trading days before the day (at least 5 present). Prefilter, per day, on the grouped daily bars and without hindsight (no test reads how the day ended): the day's price range reaches the trader's $%.2f-$%.0f filter (high at least the minimum, low at most the maximum, each with %.0f%% slack); whole-day volume at least adv10; the day's high at least %.0f%% over the lowest low of yesterday and today, less %.0f%% slack (the engine needs an impulse close %.0f%% over its 9 EMA and the EMA is never under the lowest close, so a name without that room cannot have called a flag); then %s.\n",
		pf.MinPrice, pf.MaxPrice, pf.PriceMarginPct, 100*(pf.MinSpike-1), pf.SpikeMarginPct, 100*(pf.MinSpike-1), capTxt)
	fmt.Fprintf(b, "- The prefilter across the sample: %d ticker-days; dropped for no previous-day bar %d, for under %d days of volume history %d, for a price range outside the filter %d, for whole-day volume under adv10 %d, for no room for an impulse %d; %d passed; %d lost to the cap (it bound on %d of %d days); %d fetched.\n",
		uni, noPrev, pf.MinHist, noAdv, outPrice, lowVol, noSpike, passed, dropped, capDays, len(m.Prep), fetched)
	fmt.Fprintf(b, "- Minute bars (previous day + the day, 1-minute, extended hours): %d calls, %d from cache, %d rate-limited (HTTP 429, retried with backoff), %d ticker-days failed for good.\n", d.MinuteCalls, d.MinuteHits, d.RateLimited, d.FailureCount)
	if d.SplitsKnown {
		fmt.Fprintf(b, "- Splits: the feed's bars are split-adjusted as of %s. The reference listing of splits executed from %s to %s (%d usable, %d calls, %d from cache) was used to put every name that split AFTER a test day back on that day's basis before the prefilter's price test, admission, sizing, costs and the price filter: prices divided and volumes multiplied by the product of the split ratios, on the minute bars and on the daily history alike. %d candidate-days were un-adjusted.\n",
			listOrNone([]string{d.SplitsAsOf}), d.SplitsFrom, d.SplitsAsOf, d.SplitsInWindow, d.SplitsCalls, d.SplitsHits, splitAdj)
	} else {
		note := d.SplitsNote
		if note == "" {
			note = "the splits listing was not available"
		}
		fmt.Fprintf(b, "- **Splits: NOT CORRECTED (%s).** The bars are split-adjusted as of the fetch, so names that reverse-split after their day are replayed at an inflated price with fewer shares: their per-share costs are understated and the price filter misfires. Read every figure below with that bias in the trader's favour.\n", note)
	}
	if d.Dry {
		fmt.Fprintf(b, "- Dry run: %d ticker-days were not in the cache and were left out.\n", d.MissingDry)
	}
	if d.HistoryNote != "" {
		fmt.Fprintf(b, "- Note: %s.\n", d.HistoryNote)
	}
	fmt.Fprintf(b, "- Candidate-days (pmVolX reached 1.0 before 09:30 ET): %d of %d fetched; %d lost to the live cap of 80 per day (rank by the final pre-market multiple). Names with at least one LONG (FLAG) signal after admission: %d; signals seen: %d.\n", adm, fetched, capd, withSig, sigs)
	b.WriteString("- Signals: the engine is run once over the previous day + the day with every bar closed; entries from the minute AFTER admission count (the trader primes on its first evaluation after a name is admitted, so a signal on the admission minute is history). The stop on each entry is the engine's own initial stop at that moment.\n")
	b.WriteString("- Trader rules replayed in time order across all candidates of a day: entry window [start, end) ET, price filter [$0.20, $50] on the trigger, shares = the lowest of risk $50 / (trigger - stop), $2000 / trigger and 2% of the volume of the five minutes before the entry bar (only minutes since the name was admitted count, as the live trader only knows volume it has observed); at most 3 open positions, the variant's trades per day, 2 entries per symbol per day; the daily-loss halt on realized plus marked open P&L, checked every minute until the last position has left; entry limit = trigger + 1%, no fill when the entry bar opened above it, else fill at the larger of trigger and open; flatten at 15:50 at that bar's open.\n")
	b.WriteString("- Costs: commission $0.0035/share each side (minimum $0.35 per order, at most 1% of the trade value) plus $0.002/share of regulatory and exchange fees each side; slippage 0.25% worse on entries, 0.5% worse on stop exits, 0.1% worse on flatten, close and end-of-data exits. The gross figures have none of these.\n\n")
}

func listOrNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}

func caveats() []string {
	return []string{
		"This is a model of fills, not fills. A minute bar says where price traded, not whether this order, at this size, in a stock that moves cents in a second, would have been filled there. The slippage allowances (0.25% in, 0.5% out on stops) are a guess for fast penny stocks; if the real figure is double, the net result worsens by roughly the slippage line in each table again.",
		"Inside a minute bar the order of events is unknown. Where it matters the model takes the cautious reading: a stop is checked before anything else in the same bar (including the entry bar itself, so a break bar that also dipped under the stop is a stopped-out trade), and a position that exits during a bar still counts as open for another signal in that bar.",
		"The entry assumes the order fills at the trigger (or at the open when the bar opened above it) unless the bar opened more than 1% above the trigger. Real fills of a limit order placed after the break can be worse, can be partial, or can miss on a spike that reverses inside the bar. Partial fills and queue position are not modelled.",
		"The candidate list is an approximation. Fetching minute bars for the whole market is not possible, so each day keeps only the names that pass a screen on the day's grouped daily bars and, of those, the ones with the strongest whole-day volume ratio. The first version of the screen kept a name only if its day high reached 5% over yesterday's close and only if yesterday's close was inside the price filter. Both tests read the day's outcome: a gap-down name that called a losing flag never traded 5% over yesterday and was never fetched, while an identical setup that ran was, so the expected value of what was left was biased upward (and a $0.18 name that ran to $0.45 was lost). This run uses a different screen, described in the data section: the day's price range overlaps the price filter; the whole-day volume is at least adv10, which cannot lose a true candidate (pre-market volume is part of the day's volume); and the day's high is at least 5% over the lowest low of yesterday and today, less a small margin, which every name that called a flag must satisfy because the EMA is never under the lowest close and the impulse needs a close 5% over it. What can still be lost: the grouped bar's high and low may omit some extended-hours prints, so the margin is not a proof, and the cap ranks by whole-day volume ratio, which also grows when a move succeeds, so on days the cap binds it can still favour winners. The data section says on how many days it bound; a rerun with a larger -maxcand shows how far the answer moves.",
		"The live scan caps candidates at 80 at once by their pre-market multiple at that moment. Here the cap uses each name's final pre-market multiple. The funnel table says whether it ever bound.",
		"Prices and volumes arrive split-adjusted as of the fetch (the API's adjusted=true), so a stock that reverse-split after the day being replayed shows a higher price and fewer shares than the live bot saw. Costs are per share, so they shrink with the share count: a $2000 position costs about $67 in commissions and fees round trip at $0.30 and about $3.70 at an adjusted $6.00, more than a typical trade's whole expected value; the $0.20-$50 price filter and the tick size also misfire (a real $0.15 trigger looks like $3.00). Names that later reverse-split are mostly decliners, so the error leans in the trader's favour. This run reads the splits reference data and puts each such name back on the basis of the day before anything else is computed (the data section gives the dates, the count and whether it worked; each variant section gives the trades affected and the dollars of cost the adjusted figures would have hidden). Not recovered: a split the listing lacks or dates differently, spin-offs, and any rounding in the adjusted prices (an un-adjusted price is only as fine as the adjusted one it came from). Daily and minute data are adjusted the same way, so the volume ratios stayed consistent either way. If the data section says splits were NOT corrected, none of this was done.",
		"The liquidity cap (2% of the five minutes before the entry bar) counts only minutes since the name was admitted, as the live trader does: it only knows volume it has observed. The live trader also counts the still-forming minute's partial volume, which a minute bar cannot reproduce, so for an entry within four minutes of admission the replayed size is at or below the live size.",
		fmt.Sprintf("The engine's parameters were tuned on %s, which is inside the sample. That day is reported separately and every headline is repeated without it. Treat the without-it number as the one to believe; even it is not a clean out-of-sample test, because the setup itself was shaped by the last few weeks of tape.", TunedDay),
		"Sixty days is one market regime and a few hundred trades at most. Five variants were run and the best of five will look better than it is; the variants differ in exits and limits, not in the entries, so their results are strongly related and are not five independent confirmations.",
		"The interval by trade assumes the trades are independent. They are not (one hot day produces many trades with the same sign), which is why the per-day interval is shown too. If that one spans zero, so does the honest answer.",
		"Stops and trails are not rounded to the tick, the entry limit is. The trader's own engine EXIT (a close under its trail) is ignored in the stop-mode variants: a close under the trail always implies the touch happened first, so it never changes the result. Short selling, borrowing, halts (LULD pauses, which can gap a stop), and after-hours news gaps beyond what the bars show are not modelled. The previous trading day for the bar history is holiday-aware here; the live chart fetch skips weekends only, so on the day after a market holiday the live engine had one day less of bars (a slower EMA warm-up) than this replay.",
		"Nothing here placed or touched an order: the scanner, the broker link and the paper account were not used.",
	}
}

// ConsoleSummary is what the CLI prints: the headline tables and the budget.
func (r Report) ConsoleSummary() string {
	var b strings.Builder
	m := r.Meta
	first, last := "", ""
	if len(m.Days) > 0 {
		first, last = m.Days[0], m.Days[len(m.Days)-1]
	}
	fmt.Fprintf(&b, "BACKTEST %s .. %s (%d days)\n", first, last, len(m.Days))
	d := m.Data
	fmt.Fprintf(&b, "budget: grouped %d calls (%d cached), minute %d calls (%d cached), %d x429, %d failures", d.GroupedCalls, d.GroupedHits, d.MinuteCalls, d.MinuteHits, d.RateLimited, d.FailureCount)
	if d.Dry {
		fmt.Fprintf(&b, ", DRY (%d not cached)", d.MissingDry)
	}
	b.WriteString("\n")
	var fetched, adm, sigs, pfDrop, capDrop, capDays, splitAdj int
	for _, p := range m.Prep {
		fetched += p.Fetched
		adm += p.Admitted
		sigs += p.Signals
		pfDrop += p.Prefilter.Dropped
		capDrop += p.CapDropped
		splitAdj += p.SplitAdjusted
		if p.Prefilter.Dropped > 0 {
			capDays++
		}
	}
	fmt.Fprintf(&b, "funnel: %d ticker-days fetched (%d more lost to the prefilter cap, which bound on %d of %d days) -> %d admitted (pmVolX>=1; %d lost to the 80 cap) -> %d signals\n", fetched, pfDrop, capDays, len(m.Prep), adm, capDrop, sigs)
	if d.SplitsKnown {
		fmt.Fprintf(&b, "splits: %d in %s..%s (%d calls, %d cached); %d candidate-days un-adjusted for a later split\n", d.SplitsInWindow, d.SplitsFrom, d.SplitsAsOf, d.SplitsCalls, d.SplitsHits, splitAdj)
	} else {
		note := d.SplitsNote
		if note == "" {
			note = "listing not available"
		}
		fmt.Fprintf(&b, "splits: NOT CORRECTED (%s): per-share costs and the price filter of later reverse-splitters are off\n", note)
	}
	for _, set := range []struct {
		name string
		pick func(VariantReport) Summary
	}{
		{"ALL DAYS", func(v VariantReport) Summary { return v.All }},
		{"EXCLUDING " + TunedDay, func(v VariantReport) Summary { return v.ExTuned }},
	} {
		fmt.Fprintf(&b, "\n%s\n", set.name)
		fmt.Fprintf(&b, "%-4s %7s %6s %10s %24s %10s %10s %6s %9s  %s\n", "var", "trades", "win%", "net$/trd", "95% CI (net mean)", "gross$/trd", "total net", "PF", "maxDD", "$/day CI")
		for _, v := range r.Variants {
			s := set.pick(v)
			fmt.Fprintf(&b, "%-4s %7d %5.1f%% %10s %24s %10s %10s %6s %9s  %s\n", v.Name, s.Trades, 100*s.WinRate, usdS(s.AvgNet), ci(s.NetCILo, s.NetCIHi),
				usdS(s.AvgGross), usdS(s.TotalNet), pf(s.ProfitFactor), usd(s.MaxDrawdown), ci(s.DayCILo, s.DayCIHi))
		}
	}
	// skip reasons of the first variant, the shipped one
	if len(r.Variants) > 0 {
		v := r.Variants[0]
		fmt.Fprintf(&b, "\n%s signals %d, traded %d; not traded:", v.Name, v.Seen, v.All.Trades)
		keys := make([]string, 0, len(v.Skips))
		for k := range v.Skips {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, " [%s %d]", k, v.Skips[k])
		}
		b.WriteString("\n")
	}
	if d.SplitsKnown {
		for _, v := range r.Variants {
			si := v.SplitImpact
			fmt.Fprintf(&b, "%s trades on names that split after the day: %d (net %s total, %s each; others %s each; costs the adjusted prices would have hidden %s)\n",
				v.Name, si.Trades, usdS(si.TotalNet), usdS(si.AvgNet), usdS(si.OthersAvgNet), usd(si.CostGap))
		}
	}
	return b.String()
}
