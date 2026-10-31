package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/backtest"
	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// vwOptions are the options of the -strategy vwapcross run.
type vwOptions struct {
	Days        int
	HistDays    int
	End         string
	From        string // "" = the last Days trading days ending End
	AsOf        string
	SkipSplits  bool
	Variants    []string // "intrabar", "close"; empty = both
	MaxCand     int
	Resamples   int
	Seed        uint64
	Plan        bool   // stop after the prefilter counts
	Grid        bool   // evaluate the pre-registered rule grid (backtest.VwapGrid) instead of the two signal variants
	Drift       bool   // diagnostic forward-return study (backtest.DriftDay) instead of the trades
	Drift2      bool   // -drift2: the drift study plus market-regime features and the per-month consistency report (drift2.md/csv)
	Grid2       bool   // evaluate the pre-registered grid v2 "VWAP reclaim" (backtest.Grid2)
	Grid3       bool   // evaluate the pre-registered grid v3 (backtest.Grid3)
	BenchOnly   bool   // fetch/read only the benchmark's minute bars (and the grouped days that name the test days), then stop
	Bench       string // benchmark symbol ("" = none)
	GeneratedAt string
	Command     string
}

func vwVariants(names []string) ([]string, error) {
	if len(names) == 0 {
		return []string{backtest.VwIntrabar, backtest.VwClose}, nil
	}
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n != backtest.VwIntrabar && n != backtest.VwClose {
			return nil, fmt.Errorf("unknown vwapcross variant %q (intrabar, close, or all)", n)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, nil
}

// weekdaysFrom counts the weekdays from..end inclusive.
func weekdaysFrom(from, end time.Time) int {
	n := 0
	for d := end; !d.Before(from); d = d.AddDate(0, 0, -1) {
		if wd := d.Weekday(); wd != time.Saturday && wd != time.Sunday {
			n++
		}
	}
	return n
}

// runVwapcross is the whole -strategy vwapcross run: days, splits, the
// prefilter, the minute bars, the simulation of both variants, the files. It
// returns the process exit code.
func runVwapcross(ctx context.Context, f *fetcher, o vwOptions, outDir string, stdout, stderr io.Writer) int {
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "backtest: "+format+"\n", a...)
		return 1
	}
	variants, err := vwVariants(o.Variants)
	if err != nil {
		return fail("%v", err)
	}
	var grid []backtest.VwapRules
	if o.Drift {
		variants = []string{"drift"} // one pass; the three signal types come out of DriftDay
	}
	if o.Grid {
		grid = backtest.VwapGrid()
		variants = nil
		for _, g := range grid {
			variants = append(variants, g.ID)
		}
	}
	var grid2 []backtest.Grid2Rules
	if o.Grid2 {
		grid2 = backtest.Grid2()
		variants = nil
		for _, g := range grid2 {
			variants = append(variants, g.ID)
		}
	}
	var grid3 []backtest.Grid2Rules
	if o.Grid3 {
		grid3 = backtest.Grid3()
		variants = nil
		for _, g := range grid3 {
			variants = append(variants, g.ID)
		}
	}
	end, err := time.Parse("2006-01-02", o.End)
	if err != nil {
		return fail("-end must be YYYY-MM-DD: %v", err)
	}
	if o.HistDays < minHistDays {
		o.HistDays = minHistDays
	}
	want := o.Days + o.HistDays
	if o.From != "" {
		from, _ := time.Parse("2006-01-02", o.From)
		want = weekdaysFrom(from, end) + o.HistDays
	}
	days, holidays, err := discoverDays(ctx, f, end, want)
	if err != nil {
		return fail("%v", err)
	}
	if len(days) == 0 {
		return fail("nothing cached: no grouped daily bars for the weekdays up to %s in the cache (-dry never fetches); run without -dry, or point -cache at a filled cache", o.End)
	}
	f.log("%d trading days found ending %s (holidays skipped: %s)", len(days), o.End, strings.Join(holidays, ", "))
	testStart := 0
	if o.From != "" {
		for testStart < len(days) && days[testStart].date < o.From {
			testStart++
		}
	} else {
		testStart = max(len(days)-o.Days, 0)
	}
	testStart = max(testStart, minHistDays)
	if testStart >= len(days) {
		return fail("only %d trading days available: not enough for the test days plus at least %d of history (a -dry run needs a filled cache)", len(days), minHistDays)
	}
	var hist []string
	for _, d := range days[:testStart] {
		hist = append(hist, d.date)
	}

	benchSym := strings.ToUpper(strings.TrimSpace(o.Bench))
	needBench := benchSym != "" && (o.BenchOnly || o.Grid2 || o.Grid3 || o.Drift)
	bench := backtest.NewBench(benchSym)
	var benchMissing []string
	// loadBench reads (dry) or fetches (live) the benchmark's bars of one test day, whatever the prefilter said.
	loadBench := func(i int) {
		day, prev := days[i].date, days[i-1].date
		var bars []core.Bar
		var ok bool
		if f.dry() {
			bars, ok = f.cache.loadBars(benchSym, prev, day) // no counters: a miss in -dry is reported below
		} else {
			bars, ok = f.minuteBars(ctx, benchSym, prev, day)
		}
		if ok {
			bench.Add(day, bars)
		}
		if !bench.Has(day) {
			benchMissing = append(benchMissing, day)
		}
	}
	if o.BenchOnly {
		if benchSym == "" {
			return fail("-benchonly needs a -bench symbol")
		}
		for i := testStart; i < len(days); i++ {
			if ctx.Err() != nil {
				return fail("%v", context.Cause(ctx))
			}
			loadBench(i)
		}
		st := f.stats()
		fmt.Fprintf(stdout, "benchonly %s: %d test days %s..%s, %d with bars, %d missing %v; minute-bar requests made %d (cache hits %d), dry=%v\n",
			benchSym, len(days)-testStart, days[testStart].date, days[len(days)-1].date, bench.NDays(), len(benchMissing), benchMissing, st.MinuteCalls, st.MinuteHits, f.dry())
		if st.FailureCount > 0 {
			fmt.Fprintf(stdout, "failures: %d (see the log)\n", st.FailureCount)
			return 1
		}
		return 0
	}

	asOf := o.AsOf
	if asOf == "" {
		asOf = backtest.ETDate(time.Now().UnixMilli())
	}
	if asOf < o.End {
		return fail("the as-of date %s is before the last test day %s", asOf, o.End)
	}
	splitFrom := days[0].date
	var sidx *backtest.SplitIndex
	var splitsNote string
	if o.SkipSplits {
		splitsNote = "switched off with -splits=false"
		f.log("WARNING: splits are NOT corrected (-splits=false)")
	} else {
		sp, known, err := f.splits(ctx, splitFrom, asOf)
		if err != nil {
			return fail("the splits listing %s..%s could not be fetched: %v (rerun: the cache keeps everything fetched so far; -splits=false runs knowingly without the correction)", splitFrom, asOf, err)
		}
		if !known {
			splitsNote = "the listing for as-of " + asOf + " is not in the cache (rerun with the -asof of the run that filled the cache, or without -dry)"
			f.log("WARNING: splits are NOT corrected: %s", splitsNote)
		} else {
			sidx = backtest.NewSplitIndex(sp)
			f.log("splits: %d usable executed %s..%s", sidx.Len(), splitFrom, asOf)
		}
	}

	params := backtest.DefaultVwapParams()
	simParams := params
	if o.Grid {
		simParams.MaxPosUSD = math.Inf(1) // the grid applies the cap afterwards and reports both
	}
	pf := backtest.DefaultVwPrefilter()
	pf.Cap = o.MaxCand
	meta := backtest.VwMeta{
		GeneratedAt: o.GeneratedAt, Command: o.Command, From: days[testStart].date, End: o.End,
		Params: params, Prefilter: pf, Resamples: o.Resamples, Seed: o.Seed,
	}
	allTrades := make([][]backtest.VwTrade, len(variants))
	counts := make([]backtest.VwCounts, len(variants))
	totalKept := 0
	var driftSigs []backtest.DriftSignal

	for i := testStart; i < len(days); i++ {
		if ctx.Err() != nil {
			return fail("%v", context.Cause(ctx))
		}
		day := days[i]
		prevDate := days[i-1].date
		var histMaps []map[string]core.DailyBar
		for _, d := range days[max(0, i-10):i] {
			histMaps = append(histMaps, d.bars)
		}
		pfd := pf
		if sidx != nil {
			pfd.SplitFactor = func(sym string) float64 { return sidx.Factor(sym, day.date) }
		}
		pcs, pst := backtest.VwapPrefilter(day.date, day.bars, histMaps, pfd)
		totalKept += pst.Kept
		meta.Days = append(meta.Days, day.date)
		if o.Plan {
			f.log("%s: %d tickers -> adv10/price/volume prefilter %d (no adv %d, price %d, adv %d, volume %d) -> %d minute-bar requests",
				day.date, pst.Universe, pst.Passed, pst.NoAdv, pst.LowPrice, pst.LowAdv, pst.LowVolume, pst.Kept)
			continue
		}

		if needBench {
			loadBench(i)
		}

		type slot struct {
			trades [][]backtest.VwTrade
			counts []backtest.VwCounts
			drift  []backtest.DriftSignal
			got    bool
		}
		slots := make([]slot, len(pcs))
		forEach(ctx, len(pcs), f.conc, func(k int) {
			pc := pcs[k]
			bars, ok := f.minuteBars(ctx, pc.Sym, prevDate, day.date)
			if !ok {
				return
			}
			bars = onlyDates(bars, prevDate, day.date)
			if len(bars) == 0 {
				return
			}
			bars = backtest.UnadjustBars(bars, pc.SplitFactor) // the cache holds the feed's response as it came
			s := slot{got: true, trades: make([][]backtest.VwTrade, len(variants)), counts: make([]backtest.VwCounts, len(variants))}
			if o.Drift {
				s.drift = backtest.DriftDay(pc.Sym, day.date, pc.Adv10, bars, params)
				slots[k] = s
				return
			}
			if o.Grid2 || o.Grid3 {
				if o.Grid3 {
					s.trades, s.counts = backtest.SimulateGrid2DayRegime(pc.Sym, day.date, pc.Adv10, bars, grid3, params, bench)
				} else {
					s.trades, s.counts = backtest.SimulateGrid2Day(pc.Sym, day.date, pc.Adv10, bars, grid2, params)
				}
				if pc.SplitFactor != 1 {
					for v := range s.trades {
						for j := range s.trades[v] {
							s.trades[v][j].SplitFact = pc.SplitFactor
						}
					}
				}
				slots[k] = s
				return
			}
			for v, name := range variants {
				var tr []backtest.VwTrade
				var cn backtest.VwCounts
				if o.Grid {
					tr, cn = backtest.SimulateVwapRulesDay(pc.Sym, day.date, pc.Adv10, bars, grid[v], simParams)
				} else {
					tr, cn = backtest.SimulateVwapCrossDay(pc.Sym, day.date, pc.Adv10, bars, name, params)
				}
				if pc.SplitFactor != 1 {
					for j := range tr {
						tr[j].SplitFact = pc.SplitFactor
					}
				}
				s.trades[v], s.counts[v] = tr, cn
			}
			slots[k] = s
		})
		if ctx.Err() != nil {
			return fail("%v", context.Cause(ctx))
		}
		have, adj := 0, 0
		dayTrades := make([]int, len(variants))
		for k, s := range slots {
			if !s.got {
				continue
			}
			have++
			if pcs[k].SplitFactor != 1 {
				adj++
			}
			driftSigs = append(driftSigs, s.drift...)
			for v := range variants {
				if o.Drift {
					break
				}
				allTrades[v] = append(allTrades[v], s.trades[v]...)
				counts[v].Add(s.counts[v])
				dayTrades[v] += s.counts[v].Traded
			}
		}
		meta.Prep = append(meta.Prep, backtest.VwDayPrep{Date: day.date, Prefilter: pst, Fetched: have, Adjusted: adj})
		f.log("%s: %d tickers -> prefilter %d -> bars %d (%d un-adjusted for a later split) -> trades %v (%s)",
			day.date, pst.Universe, pst.Kept, have, adj, dayTrades, strings.Join(variants, "/"))
	}

	if o.Plan {
		fmt.Fprintf(stdout, "plan: %d test days %s..%s, %d minute-bar requests in total (before the cache), about %d grouped-daily calls\n",
			len(meta.Days), meta.Days[0], o.End, totalKept, f.stats().GroupedCalls)
		return 0
	}

	meta.Data = f.stats()
	meta.Data.Holidays = holidays
	meta.Data.HistoryDays = hist
	meta.Data.SplitsKnown = sidx != nil
	meta.Data.SplitsNote = splitsNote
	if sidx != nil {
		meta.Data.SplitsFrom, meta.Data.SplitsAsOf, meta.Data.SplitsInWindow = splitFrom, asOf, sidx.Len()
	}
	so := backtest.StatOpts{Resamples: o.Resamples, Seed: o.Seed}
	if needBench {
		f.log("benchmark %s: %d test days with bars, %d without%s", benchSym, bench.NDays(), len(benchMissing), missingNote(benchMissing))
	}
	if o.Drift {
		if needBench {
			bench.AttachDrift(driftSigs)
			if o.Drift2 {
				bench.AttachRegime(driftSigs)
			}
		}
		header := fmt.Sprintf("Generated %s. Command: `%s`. Test days %s..%s (%d days, history %d days), splits: %s.",
			o.GeneratedAt, o.Command, meta.From, o.End, len(meta.Days), len(hist), map[bool]string{true: "corrected as of " + asOf, false: "NOT corrected (" + splitsNote + ")"}[sidx != nil])
		paths, err := backtest.WriteDriftFiles(outDir, driftSigs, params, header)
		if err != nil {
			return fail("writing the drift report: %v", err)
		}
		if o.Drift2 {
			p2, sum, err := backtest.WriteDrift2Files(outDir, driftSigs, params, header)
			if err != nil {
				return fail("writing the drift2 report: %v", err)
			}
			paths = append(paths, p2...)
			fmt.Fprintln(stdout, sum)
		}
		fmt.Fprintf(stdout, "drift: %d signals over %d days\nfiles:\n", len(driftSigs), len(meta.Days))
		for _, p := range paths {
			fmt.Fprintln(stdout, "  "+p)
		}
		return 0
	}
	if o.Grid3 {
		return writeGrid3(grid3, allTrades, counts, bench, benchMissing, meta, so, outDir, stdout, stderr)
	}
	if o.Grid2 {
		return writeGrid2(grid2, allTrades, counts, bench, benchMissing, meta, params, so, outDir, stdout, stderr)
	}
	if o.Grid {
		gr := backtest.GridReport{Version: backtest.GridVersion, Meta: meta}
		for v := range grid {
			gr.Variants = append(gr.Variants, backtest.BuildGridVariant(grid[v], allTrades[v], counts[v], params, so))
		}
		sort.SliceStable(gr.Variants, func(i, j int) bool { return gr.Variants[i].ID < gr.Variants[j].ID })
		paths, err := backtest.WriteGridFiles(outDir, gr)
		if err != nil {
			return fail("writing the grid report: %v", err)
		}
		for _, v := range gr.Variants {
			s := v.Stats
			fmt.Fprintf(stdout, "  %-4s trades %5d  win %5.1f%%  avgR gross %+.3f net %+.3f  net(no cap) %+.3f  skipped_size %d unfilled %d\n",
				v.ID, s.Trades, 100*s.WinRate, s.AvgRGross, s.AvgRNet, v.StatsNoCap.AvgRNet, v.SkippedSize, v.Unfilled)
		}
		fmt.Fprintln(stdout, "files:")
		for _, p := range paths {
			fmt.Fprintln(stdout, "  "+p)
		}
		return 0
	}
	rep := backtest.VwReport{Meta: meta}
	for v, name := range variants {
		rep.Variants = append(rep.Variants, backtest.BuildVwapReport(name, allTrades[v], counts[v], so))
	}
	paths, err := backtest.WriteVwapFiles(outDir, rep)
	if err != nil {
		return fail("writing the report: %v", err)
	}
	fmt.Fprintln(stdout, rep.ConsoleSummary())
	fmt.Fprintln(stdout, "files:")
	for _, p := range paths {
		fmt.Fprintln(stdout, "  "+p)
	}
	return 0
}

func missingNote(d []string) string {
	if len(d) == 0 {
		return ""
	}
	return ": " + strings.Join(d, " ")
}

// writeGrid2 attaches the benchmark, runs P1's portfolio pass over D5 and writes
// the grid v2 files.
func writeGrid2(grid2 []backtest.Grid2Rules, allTrades [][]backtest.VwTrade, counts []backtest.VwCounts, bench *backtest.Bench, benchMissing []string,
	meta backtest.VwMeta, params backtest.VwapParams, so backtest.StatOpts, outDir string, stdout, stderr io.Writer) int {
	for v := range allTrades {
		bench.AttachTrades(allTrades[v])
	}
	d5 := -1
	for i, g := range grid2 {
		if g.ID == "D5" {
			d5 = i
		}
	}
	lim := backtest.DefaultPortfolioLimits()
	gr := backtest.Grid2Report{Version: backtest.Grid2Version, Meta: meta, BenchSym: bench.Sym, BenchDays: bench.NDays(), BenchMiss: benchMissing, Limits: lim}
	if gr.BenchMiss == nil {
		gr.BenchMiss = []string{}
	}
	for v, g := range grid2 {
		trades, cn := allTrades[v], counts[v]
		var skips *backtest.PortfolioSkips
		if g.Derive != "" {
			if d5 < 0 || grid2[d5].ID != g.Derive {
				fmt.Fprintf(stderr, "backtest: grid2 variant %s derives from %s, which is not in the grid\n", g.ID, g.Derive)
				return 1
			}
			kept, sk := backtest.ApplyPortfolio(allTrades[d5], lim)
			for i := range kept {
				kept[i].Variant = g.ID
			}
			trades, cn, skips = kept, counts[d5], &sk
		}
		gv := backtest.BuildGrid2Variant(g, trades, cn, so)
		gv.Portfolio = skips
		gr.Variants = append(gr.Variants, gv)
	}
	sort.SliceStable(gr.Variants, func(i, j int) bool { return gr.Variants[i].ID < gr.Variants[j].ID })
	paths, err := backtest.WriteGrid2Files(outDir, gr)
	if err != nil {
		fmt.Fprintf(stderr, "backtest: writing the grid2 report: %v\n", err)
		return 1
	}
	for _, v := range gr.Variants {
		s, x := v.Stats, v.Extra
		fmt.Fprintf(stdout, "  %-3s trades %5d  win %5.1f%%  avgR gross %+.3f net %+.3f  net%%/trade %+.3f  excess %+.3f%% (t %.2f, n %d, missing %d)  maxDD %.1fR  worst day %+.1fR  days %d\n",
			v.ID, s.Trades, 100*s.WinRate, s.AvgRGross, s.AvgRNet, x.MeanNetPct, x.Excess.Mean, x.Excess.T, x.Excess.N, x.Excess.Missing, x.MaxDDR, x.WorstDayR, x.DaysTraded)
	}
	fmt.Fprintln(stdout, "files:")
	for _, p := range paths {
		fmt.Fprintln(stdout, "  "+p)
	}
	return 0
}

// writeGrid3 attaches the benchmark, derives V6 from V4 with the grid v3
// portfolio limits and writes the grid v3 files.
func writeGrid3(grid3 []backtest.Grid2Rules, allTrades [][]backtest.VwTrade, counts []backtest.VwCounts, bench *backtest.Bench, benchMissing []string,
	meta backtest.VwMeta, so backtest.StatOpts, outDir string, stdout, stderr io.Writer) int {
	for v := range allTrades {
		bench.AttachTrades(allTrades[v])
	}
	idx := map[string]int{}
	for i, g := range grid3 {
		idx[g.ID] = i
	}
	gr := backtest.Grid2Report{Version: backtest.Grid3Version, Meta: meta, BenchSym: bench.Sym, BenchDays: bench.NDays(), BenchMiss: benchMissing, Limits: backtest.Grid3Limits()}
	if gr.BenchMiss == nil {
		gr.BenchMiss = []string{}
	}
	for v, g := range grid3 {
		trades, cn := allTrades[v], counts[v]
		var skips *backtest.PortfolioSkips
		if g.Derive != "" {
			src, ok := idx[g.Derive]
			if !ok {
				fmt.Fprintf(stderr, "backtest: grid3 variant %s derives from %s, which is not in the grid\n", g.ID, g.Derive)
				return 1
			}
			kept, sk := backtest.ApplyPortfolio(allTrades[src], g.Lim)
			for i := range kept {
				kept[i].Variant = g.ID
			}
			trades, cn, skips = kept, counts[src], &sk
		}
		gv := backtest.BuildGrid2Variant(g, trades, cn, so)
		gv.Portfolio = skips
		gr.Variants = append(gr.Variants, gv)
	}
	sort.SliceStable(gr.Variants, func(i, j int) bool { return gr.Variants[i].ID < gr.Variants[j].ID })
	paths, err := backtest.WriteGrid3Files(outDir, gr)
	if err != nil {
		fmt.Fprintf(stderr, "backtest: writing the grid3 report: %v\n", err)
		return 1
	}
	for _, v := range gr.Variants {
		s, x := v.Stats, v.Extra
		fmt.Fprintf(stdout, "  %-3s trades %5d  win %5.1f%%  avgR gross %+.3f net %+.3f  excess %+.3f%% (t %.2f)  maxDD %.1fR  worst day %+.1fR  days %d\n",
			v.ID, s.Trades, 100*s.WinRate, s.AvgRGross, s.AvgRNet, x.Excess.Mean, x.Excess.T, x.MaxDDR, x.WorstDayR, x.DaysTraded)
	}
	fmt.Fprintln(stdout, "files:")
	for _, p := range paths {
		fmt.Fprintln(stdout, "  "+p)
	}
	return 0
}
