// Command backtest replays the 9EMA-flag auto-trader over the last N completed
// US trading days (default 60) and writes the verdict: expected value per trade
// for five variants of the trader's rules, with confidence intervals, splits and
// the caveats.
//
//	backtest -days 60 -end 2026-10-05 -cache <dir> -out <dir> -concurrency 4
//
// It is a stand-alone tool and touches nothing live: no scanner, no broker, no
// TWS / IB Gateway, nothing under ~/.marketscanner (the API key is READ through
// internal/config, exactly as the scanner loads it, and is never printed or
// logged; both -cache and -out refuse to be inside ~/.marketscanner). The data
// comes from the Massive REST API through the scanner's own client
// (internal/feed): one grouped-daily call per trading day, then 1-minute bars
// (previous day + the day, extended hours) for each name that passes a documented
// prefilter, and one listing of the splits executed since the sample began (the
// feed's bars are split-adjusted as of the fetch, so names that split after a
// test day are put back on that day's price and share basis; -splits=false
// switches that off). Every response is cached under -cache keyed by symbol and date, so
// a rerun never refetches. -dry never opens a connection: it runs on whatever is
// already cached.
//
// The simulation is internal/backtest (pure, tested on fixtures); this package is
// the fetching, the cache and the files.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/backtest"
	"github.com/eelkonkoopman/marketscanner/internal/config"
	"github.com/eelkonkoopman/marketscanner/internal/feed"
)

func main() {
	os.Exit(realMain(os.Args[1:], os.Stdout, os.Stderr))
}

// underDir reports whether path is dir or inside it (compared case-insensitively
// on cleaned absolute paths: Windows).
func underDir(path, dir string) bool {
	a, err1 := filepath.Abs(path)
	b, err2 := filepath.Abs(dir)
	if err1 != nil || err2 != nil {
		return true // cannot tell: refuse
	}
	a, b = strings.ToLower(filepath.Clean(a)), strings.ToLower(filepath.Clean(b))
	rel, err := filepath.Rel(b, a)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func realMain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("backtest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	def := filepath.Join(os.TempDir(), "marketscanner-backtest")
	days := fs.Int("days", 60, "completed US trading days to test")
	hist := fs.Int("hist", 10, "earlier trading days fetched for the 10-day average volume")
	end := fs.String("end", "2026-10-05", "last test day (YYYY-MM-DD, a completed trading day)")
	cacheDir := fs.String("cache", filepath.Join(def, "cache"), "directory of cached API responses")
	outDir := fs.String("out", filepath.Join(def, "out"), "directory for report.md, trades.csv, summary.json")
	conc := fs.Int("concurrency", 4, "parallel requests (at most 4)")
	dry := fs.Bool("dry", false, "no network: run only on what is cached")
	variants := fs.String("variants", "all", "comma-separated variants V1..V5, or all")
	maxcand := fs.Int("maxcand", 150, "prefilter cap: names per day that get minute bars (the cap ranks by whole-day volume ratio, which also grows when a move succeeds, so when it binds the day's list can favour winners: rerun with a larger value, e.g. 100000 for no cap, to see how far the answer moves; the cache keeps what was fetched)")
	splits := fs.Bool("splits", true, "read the splits listing and put names that split after a test day back on that day's price and share basis (the feed's bars are split-adjusted as of the fetch); false replays the adjusted figures as they are, which understates the per-share costs of later reverse-splitters")
	asof := fs.String("asof", "", "the date the feed's bars are adjusted to, YYYY-MM-DD (default: today, ET): splits executed up to it are undone. Pin it to the original run's date when rerunning from the cache with -dry")
	resamples := fs.Int("resamples", 10000, "bootstrap resamples")
	seed := fs.Uint64("seed", 20261006, "bootstrap seed")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	usage := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "backtest: "+format+"\n", a...)
		return 2
	}
	if fs.NArg() > 0 {
		return usage("unexpected arguments %v", fs.Args())
	}
	if *days < 1 || *days > 250 {
		return usage("-days must be 1..250")
	}
	if *hist < 5 || *hist > 30 {
		return usage("-hist must be 5..30")
	}
	if *maxcand < 1 {
		return usage("-maxcand must be at least 1")
	}
	if *resamples < 100 {
		return usage("-resamples must be at least 100")
	}
	if _, err := time.Parse("2006-01-02", *end); err != nil {
		return usage("-end must be YYYY-MM-DD")
	}
	if *asof != "" {
		if _, err := time.Parse("2006-01-02", *asof); err != nil {
			return usage("-asof must be YYYY-MM-DD")
		}
		if *asof < *end {
			return usage("-asof %s is before -end %s: the bars were fetched after the last test day", *asof, *end)
		}
	}
	if *conc < 1 {
		return usage("-concurrency must be at least 1")
	}
	if *conc > 4 {
		fmt.Fprintf(stderr, "backtest: -concurrency %d lowered to 4 (the limit)\n", *conc)
		*conc = 4
	}
	var names []string
	if v := strings.TrimSpace(*variants); v != "" && !strings.EqualFold(v, "all") {
		names = strings.Split(v, ",")
	}
	if _, err := pickVariants(names); err != nil {
		return usage("%v", err)
	}
	for _, p := range []struct{ flag, path string }{{"-cache", *cacheDir}, {"-out", *outDir}} {
		if underDir(p.path, config.ConfigDir()) {
			return usage("%s %q is inside %s: the backtest never writes there", p.flag, p.path, config.ConfigDir())
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, abort := context.WithCancelCause(ctx)
	defer abort(nil)

	logf := func(format string, a ...any) {
		fmt.Fprintf(stderr, time.Now().Format("15:04:05")+" "+format+"\n", a...)
	}
	f := &fetcher{cache: cache{dir: *cacheDir}, conc: *conc, log: logf, sleep: realSleep, abort: abort}

	if !*dry {
		cfg, _ := config.Load() // the same key lookup as the scanner: MASSIVE_API_KEY, then ~/.marketscanner/apikey (read only)
		if cfg.MassiveKey == "" {
			fmt.Fprintln(stderr, "backtest: no Massive API key (set MASSIVE_API_KEY or save one in the scanner), or run with -dry on a filled cache")
			return 1
		}
		hc := &http.Client{Timeout: 90 * time.Second}
		f.src = liveSource{
			Client:    feed.New(feed.Config{APIKey: cfg.MassiveKey, RESTBase: cfg.MassiveBase, HTTP: hc}),
			splitList: newSplitClient(cfg.MassiveBase, cfg.MassiveKey, hc),
		}
	}

	cmdline := "backtest " + strings.Join(args, " ")
	rep, results, err := run(ctx, f, options{
		Days: *days, HistDays: *hist, End: *end, AsOf: *asof, SkipSplits: !*splits, Variants: names, MaxCand: *maxcand,
		Resamples: *resamples, Seed: *seed, GeneratedAt: time.Now().Format("2006-01-02 15:04 MST"), Command: cmdline,
	})
	if err != nil {
		fmt.Fprintf(stderr, "backtest: %v\n", err)
		return 1
	}
	paths, err := backtest.WriteFiles(*outDir, rep, results)
	if err != nil {
		fmt.Fprintf(stderr, "backtest: writing the report: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, rep.ConsoleSummary())
	fmt.Fprintln(stdout, "files:")
	for _, p := range paths {
		fmt.Fprintln(stdout, "  "+p)
	}
	return 0
}
