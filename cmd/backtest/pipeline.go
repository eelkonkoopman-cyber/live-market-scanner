package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/backtest"
	"github.com/eelkonkoopman/marketscanner/internal/core"
)

const minHistDays = 5 // fewest earlier trading days a test day may have (the adv10 needs 5 of them)

type options struct {
	Days        int      // test days (60)
	HistDays    int      // earlier days for adv10 (10)
	End         string   // last test day, "2006-01-02" (2026-10-05)
	AsOf        string   // the date the feed's bars are adjusted to (the day they were fetched; "" = today, ET): splits executed up to it are undone
	SkipSplits  bool     // -splits=false: do not read the splits listing; nothing is un-adjusted
	Variants    []string // names; empty = all
	MaxCand     int      // prefilter cap per day (150)
	Resamples   int
	Seed        uint64
	GeneratedAt string // for the report header
	Command     string
}

// weekdaysBack lists n weekdays going back from end (inclusive when end is one),
// newest first. Holidays are not known here: the feed says (an empty grouped day).
func weekdaysBack(end time.Time, n int) []string {
	var out []string
	for d := end; len(out) < n; d = d.AddDate(0, 0, -1) {
		if wd := d.Weekday(); wd == time.Saturday || wd == time.Sunday {
			continue
		}
		out = append(out, d.Format("2006-01-02"))
	}
	return out
}

type dayData struct {
	date string
	bars map[string]core.DailyBar
}

// discoverDays finds the `want` most recent trading days ending at end: every
// weekday's grouped bars are fetched (or read from the cache) and an EMPTY result
// is a holiday. Ascending. In -dry a weekday that is not cached is unknown and
// skipped, so fewer days may come back.
func discoverDays(ctx context.Context, f *fetcher, end time.Time, want int) (days []dayData, holidays []string, err error) {
	known := map[string]*dayData{} // date -> data (nil bars map = holiday)
	isHoliday := map[string]bool{}
	tried := map[string]bool{}
	count := want + 6 // ~70 trading days span a handful of holidays
	for round := 0; round < 8; round++ {
		dates := weekdaysBack(end, count)
		var todo []string
		for _, d := range dates {
			if !tried[d] {
				todo = append(todo, d)
				tried[d] = true
			}
		}
		results := make([]*dayData, len(todo))
		errs := make([]error, len(todo))
		forEach(ctx, len(todo), f.conc, func(i int) {
			m, ok, e := f.grouped(ctx, todo[i])
			if e != nil {
				errs[i] = e
				return
			}
			if ok {
				results[i] = &dayData{date: todo[i], bars: m}
			}
		})
		if ctx.Err() != nil {
			return nil, nil, context.Cause(ctx)
		}
		for i, e := range errs {
			if e != nil {
				return nil, nil, fmt.Errorf("grouped daily bars for %s could not be fetched, and a missing day would corrupt the previous-day and volume history: %w (rerun: the cache keeps everything fetched so far)", todo[i], e)
			}
		}
		for _, r := range results {
			if r == nil {
				continue
			}
			if len(r.bars) == 0 {
				isHoliday[r.date] = true
				continue
			}
			known[r.date] = r
		}
		if len(known) >= want || f.dry() {
			break
		}
		count += want - len(known) + 4
	}
	for d := range isHoliday {
		holidays = append(holidays, d)
	}
	sort.Strings(holidays)
	var dates []string
	for d := range known {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	if len(dates) > want {
		dates = dates[len(dates)-want:]
	}
	for _, d := range dates {
		days = append(days, *known[d])
	}
	// holidays older than the oldest day kept are of no interest
	if len(dates) > 0 {
		var kept []string
		for _, h := range holidays {
			if h > dates[0] {
				kept = append(kept, h)
			}
		}
		holidays = kept
	}
	return days, holidays, nil
}

func pickVariants(names []string) ([]backtest.Rules, error) {
	if len(names) == 0 {
		return backtest.Variants(), nil
	}
	var out []backtest.Rules
	seen := map[string]bool{}
	for _, n := range names {
		v, ok := backtest.VariantByName(strings.TrimSpace(n))
		if !ok {
			return nil, fmt.Errorf("unknown variant %q (V1..V5, or all)", n)
		}
		if !seen[v.Name] {
			seen[v.Name] = true
			out = append(out, v)
		}
	}
	return out, nil
}

// run is the whole backtest: days, prefilter, minute bars, admission, signals,
// every variant, the report. Nothing here writes files (the caller does) and
// everything outside goes through f.
func run(ctx context.Context, f *fetcher, o options) (backtest.Report, []backtest.VariantResult, error) {
	var zero backtest.Report
	end, err := time.Parse("2006-01-02", o.End)
	if err != nil {
		return zero, nil, fmt.Errorf("-end must be YYYY-MM-DD: %w", err)
	}
	rules, err := pickVariants(o.Variants)
	if err != nil {
		return zero, nil, err
	}
	if o.HistDays < minHistDays {
		o.HistDays = minHistDays
	}

	days, holidays, err := discoverDays(ctx, f, end, o.Days+o.HistDays)
	if err != nil {
		return zero, nil, err
	}
	f.log("%d trading days found ending %s (holidays skipped: %s)", len(days), o.End, strings.Join(holidays, ", "))
	testStart := max(len(days)-o.Days, minHistDays)
	if testStart >= len(days) {
		return zero, nil, fmt.Errorf("only %d trading days available (%d cached): not enough for %d test days plus at least %d of history", len(days), len(days), o.Days, minHistDays)
	}

	var hist []string
	for _, d := range days[:testStart] {
		hist = append(hist, d.date)
	}

	// The splits listing: the feed's bars are adjusted as of the fetch, so a name
	// that split after a test day has to be put back on that day's basis (splits.go).
	// Executions from the first history day to the as-of date; a split before a
	// test day never matters, one after it does. Without the listing the run does
	// not guess: it says so in the report (and a failed fetch stops the run).
	asOf := o.AsOf
	if asOf == "" {
		asOf = backtest.ETDate(time.Now().UnixMilli())
	}
	if asOf < o.End {
		return zero, nil, fmt.Errorf("the as-of date %s is before the last test day %s", asOf, o.End)
	}
	splitFrom := days[0].date
	var sidx *backtest.SplitIndex
	var splitsNote string
	switch {
	case o.SkipSplits:
		splitsNote = "switched off with -splits=false"
		f.log("WARNING: splits are NOT corrected (-splits=false): names that reverse-split after their day are replayed at the feed's adjusted price")
	default:
		sp, known, err := f.splits(ctx, splitFrom, asOf)
		if err != nil {
			return zero, nil, fmt.Errorf("the splits listing %s..%s could not be fetched, and without it names that reverse-split later are replayed at the wrong price and share count: %w (rerun: the cache keeps everything fetched so far; -splits=false runs knowingly without the correction)", splitFrom, asOf, err)
		}
		if !known {
			splitsNote = "the listing for as-of " + asOf + " is not in the cache (rerun with the -asof of the run that filled the cache, or without -dry)"
			f.log("WARNING: splits are NOT corrected: %s", splitsNote)
		} else {
			sidx = backtest.NewSplitIndex(sp)
			f.log("splits: %d usable executed %s..%s", sidx.Len(), splitFrom, asOf)
		}
	}

	meta := backtest.RunMeta{GeneratedAt: o.GeneratedAt, EndDate: o.End, Command: o.Command}
	var prepared []backtest.PreparedDay

	pf := backtest.DefaultPrefilter()
	pf.Cap = o.MaxCand
	meta.Prefilter = pf
	rule0 := rules[0]
	for i := testStart; i < len(days); i++ {
		if ctx.Err() != nil {
			return zero, nil, context.Cause(ctx)
		}
		day := days[i]
		prevDate := days[i-1].date
		var histMaps []map[string]core.DailyBar
		for _, d := range days[max(0, i-10):i] {
			histMaps = append(histMaps, d.bars)
		}
		pfd := pf // this day's screen: names back on this day's basis
		if sidx != nil {
			pfd.SplitFactor = func(sym string) float64 { return sidx.Factor(sym, day.date) }
		}
		pcs, pst := backtest.Prefilter(day.date, day.bars, histMaps, pfd)

		cands := make([]backtest.Candidate, len(pcs))
		got := make([]bool, len(pcs))
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
			// the cache holds the feed's response as it came; the basis of the day is applied here
			bars = backtest.UnadjustBars(bars, pc.SplitFactor)
			cands[k] = backtest.Candidate{Sym: pc.Sym, Date: day.date, Adv10: pc.Adv10, PrevClose: pc.PrevClose, DayVol: pc.DayVol, Bars: bars, SplitFactor: pc.SplitFactor}
			got[k] = true
		})
		if ctx.Err() != nil {
			return zero, nil, context.Cause(ctx)
		}
		var have []backtest.Candidate
		splitAdj := 0
		for k := range cands {
			if got[k] {
				have = append(have, cands[k])
				if cands[k].SplitFactor != 1 {
					splitAdj++
				}
			}
		}
		pd := backtest.PrepareDay(day.date, have, rule0.Flag, rule0.MaxCandidates)
		prepared = append(prepared, pd)
		meta.Days = append(meta.Days, day.date)
		meta.Prep = append(meta.Prep, backtest.DayPrep{
			Date: day.date, Prefilter: pst, Fetched: len(have), SplitAdjusted: splitAdj, Admitted: pd.Admitted, CapDropped: pd.CapDropped,
			WithSignals: len(pd.Kept), Signals: len(pd.Signals),
		})
		f.log("%s: %d tickers -> prefilter %d (cap dropped %d) -> bars %d (%d un-adjusted for a later split) -> admitted %d (cap dropped %d) -> %d signals",
			day.date, pst.Universe, pst.Kept, pst.Dropped, len(have), splitAdj, pd.Admitted, pd.CapDropped, len(pd.Signals))
	}

	var results []backtest.VariantResult
	for _, r := range rules {
		results = append(results, backtest.RunVariant(prepared, r))
	}

	meta.Data = f.stats()
	meta.Data.Holidays = holidays
	meta.Data.HistoryDays = hist
	meta.Data.SplitsKnown = sidx != nil
	meta.Data.SplitsNote = splitsNote
	if sidx != nil {
		meta.Data.SplitsFrom, meta.Data.SplitsAsOf, meta.Data.SplitsInWindow = splitFrom, asOf, sidx.Len()
	}
	if testStart < 10 {
		meta.Data.HistoryNote = fmt.Sprintf("the earliest test day has only %d earlier trading days for its 10-day average volume (fewer than 10)", testStart)
	}
	rep := backtest.BuildReport(meta, results, backtest.StatOpts{Resamples: o.Resamples, Seed: o.Seed})
	return rep, results, nil
}

// onlyDates keeps the bars whose ET date is one of dates: the request spans the
// previous trading day and the day, and the chart's data shape is exactly those two.
func onlyDates(bars []core.Bar, dates ...string) []core.Bar {
	out := bars[:0:0]
	for _, b := range bars {
		d := backtest.ETDate(b.T)
		for _, want := range dates {
			if d == want {
				out = append(out, b)
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out
}
