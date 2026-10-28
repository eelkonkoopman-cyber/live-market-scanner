package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/backtest"
	"github.com/eelkonkoopman/marketscanner/internal/config"
	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/feed"
)

const fixture = "../../web/src/signals/fixtures/beat-2026-10-05.json"

func beatBars(t testing.TB) []core.Bar {
	t.Helper()
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Bars []core.Bar `json:"bars"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	return fx.Bars
}

// fake is an in-memory Massive: no network, ever. Every call is counted.
type fake struct {
	mu       sync.Mutex
	grouped  map[string]map[string]core.DailyBar // date -> bars; absent date = holiday (empty result)
	bars     map[string][]core.Bar               // "SYM" -> bars (returned for any range)
	groupedN atomic.Int64
	barsN    atomic.Int64
	perSym   map[string]int
	// errFor decides an error for the n-th (1-based) call of a symbol; nil = none.
	errFor func(sym string, n int) error
	// splits is what the reference listing returns (any range); splitsErr, when set, fails the call.
	splits     []backtest.StockSplit
	splitsErr  error
	splitsN    atomic.Int64
	splitsFrom string // the last range asked for
	splitsTo   string
}

func (f *fake) GroupedDaily(_ context.Context, date string) (map[string]core.DailyBar, error) {
	f.groupedN.Add(1)
	if m, ok := f.grouped[date]; ok {
		return m, nil
	}
	return map[string]core.DailyBar{}, nil
}

func (f *fake) IntradayBetween(_ context.Context, sym, from, to string) ([]core.Bar, error) {
	f.barsN.Add(1)
	f.mu.Lock()
	if f.perSym == nil {
		f.perSym = map[string]int{}
	}
	f.perSym[sym]++
	n := f.perSym[sym]
	f.mu.Unlock()
	if f.errFor != nil {
		if err := f.errFor(sym, n); err != nil {
			return nil, err
		}
	}
	return f.bars[sym], nil
}

func (f *fake) Splits(_ context.Context, from, to string) ([]backtest.StockSplit, error) {
	f.splitsN.Add(1)
	f.mu.Lock()
	f.splitsFrom, f.splitsTo = from, to
	f.mu.Unlock()
	if f.splitsErr != nil {
		return nil, f.splitsErr
	}
	return f.splits, nil
}

func db(c, h float64, v int64) core.DailyBar { return core.DailyBar{O: c, H: h, L: c, C: c, V: v} }

// world: trading days 2026-09-18 .. 2026-10-05 with 2026-09-24 a holiday (12 weekdays 09-18..10-05 minus it = 11
// trading days: the test day 2026-10-05 plus 10 of history). BEAT is a real candidate on 10-05 (volume 90M vs a 5M
// average), FAIL passes the prefilter too but its bars always fail, DUMB is over $50.
func world(t testing.TB) *fake {
	t.Helper()
	f := &fake{grouped: map[string]map[string]core.DailyBar{}, bars: map[string][]core.Bar{"BEAT": beatBars(t)}}
	for _, d := range []string{"2026-09-18", "2026-09-21", "2026-09-22", "2026-09-23", "2026-09-25", "2026-09-28", "2026-09-29", "2026-09-30", "2026-10-01", "2026-10-02"} {
		f.grouped[d] = map[string]core.DailyBar{
			"BEAT": db(0.43, 0.45, 5_000_000), "FAIL": db(2, 2.1, 1_000_000), "DUMB": db(120, 125, 1_000_000), "QUIET": db(3, 3.1, 1_000_000),
		}
	}
	f.grouped["2026-10-05"] = map[string]core.DailyBar{
		"BEAT": db(0.8, 0.95, 90_000_000), "FAIL": db(2.5, 2.6, 8_000_000), "DUMB": db(121, 130, 9_000_000), "QUIET": db(3, 3.05, 500_000),
	}
	return f
}

func newFetcher(src source, dir string) (*fetcher, *[]time.Duration) {
	var mu sync.Mutex
	var slept []time.Duration
	f := &fetcher{
		src: src, cache: cache{dir: dir}, conc: 4,
		log: func(string, ...any) {},
		sleep: func(ctx context.Context, d time.Duration) error {
			mu.Lock()
			slept = append(slept, d)
			mu.Unlock()
			return ctx.Err()
		},
	}
	return f, &slept
}

func opts() options {
	return options{Days: 1, HistDays: 10, End: "2026-10-05", AsOf: "2026-10-06", MaxCand: 150, Resamples: 200, Seed: 1, GeneratedAt: "test", Command: "backtest test"}
}

func TestEndToEndOnBEAT(t *testing.T) {
	fk := world(t)
	fk.errFor = func(sym string, n int) error {
		if sym == "FAIL" {
			return &feed.HTTPError{Status: 500, Msg: "boom"}
		}
		return nil
	}
	dir := t.TempDir()
	f, _ := newFetcher(fk, dir)
	rep, results, err := run(context.Background(), f, opts())
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Meta.Days; len(got) != 1 || got[0] != "2026-10-05" {
		t.Fatalf("test days %v", got)
	}
	if h := rep.Meta.Data.Holidays; len(h) != 1 || h[0] != "2026-09-24" {
		t.Errorf("holidays %v, want 2026-09-24 (an empty grouped result)", h)
	}
	if len(rep.Meta.Data.HistoryDays) != 10 {
		t.Errorf("history days %d, want 10", len(rep.Meta.Data.HistoryDays))
	}
	p := rep.Meta.Prep[0]
	if p.Prefilter.Passed != 2 || p.Prefilter.OutOfPrice != 1 || p.Prefilter.LowVolume != 1 || p.Fetched != 1 || p.Admitted != 1 || p.Signals != 1 {
		t.Errorf("funnel %+v", p)
	}
	d := rep.Meta.Data
	if d.FailureCount != 1 || len(d.Failures) != 1 || !strings.HasPrefix(d.Failures[0].Key, "FAIL 2026-10-05") {
		t.Errorf("failures %+v", d.Failures)
	}
	if fk.perSym["FAIL"] != 3 || fk.perSym["BEAT"] != 1 {
		t.Errorf("calls per symbol %v: FAIL must be tried exactly 3 times, BEAT once", fk.perSym)
	}
	if d.GroupedCalls != int(fk.groupedN.Load()) || d.MinuteCalls != int(fk.barsN.Load()) || d.GroupedHits != 0 || d.MinuteHits != 0 {
		t.Errorf("budget %+v vs fake %d/%d", d, fk.groupedN.Load(), fk.barsN.Load())
	}
	if len(results) != 5 || len(rep.Variants) != 5 {
		t.Fatalf("%d variants", len(rep.Variants))
	}
	v1 := rep.Variants[0]
	if v1.All.Trades != 1 || v1.All.TotalNet < 100 {
		t.Errorf("V1: %d trades, net %v: want the one BEAT trade (about +$166)", v1.All.Trades, v1.All.TotalNet)
	}
	if tr := results[0].Trades[0]; tr.Sym != "BEAT" || backtest.ETClock(tr.EntryTs) != "09:00" || backtest.ETClock(tr.ExitTs) != "09:14" {
		t.Errorf("trade %+v", tr)
	}
	if v3 := results[2].Trades[0]; backtest.ETClock(v3.ExitTs) != "09:15" || v3.ExitKind != backtest.ExitClose {
		t.Errorf("V3 trade %+v", v3)
	}

	// the files
	paths, err := backtest.WriteFiles(filepath.Join(dir, "out"), rep, results)
	if err != nil || len(paths) != 3 {
		t.Fatalf("write: %v %v", paths, err)
	}
	md, _ := os.ReadFile(paths[0])
	if !strings.Contains(string(md), "## V5:") || !strings.Contains(string(md), "FAIL 2026-10-05") {
		t.Errorf("report.md incomplete")
	}
}

// A second run on the same cache makes no calls at all and gives the same numbers.
func TestCacheMakesARerunFree(t *testing.T) {
	dir := t.TempDir()
	fk := world(t)
	f1, _ := newFetcher(fk, dir)
	rep1, _, err := run(context.Background(), f1, opts())
	if err != nil {
		t.Fatal(err)
	}
	callsBefore := fk.groupedN.Load() + fk.barsN.Load()

	f2, _ := newFetcher(fk, dir)
	rep2, _, err := run(context.Background(), f2, opts())
	if err != nil {
		t.Fatal(err)
	}
	if got := fk.groupedN.Load() + fk.barsN.Load(); got != callsBefore {
		t.Errorf("the rerun made %d new calls, want 0", got-callsBefore)
	}
	d := rep2.Meta.Data
	if d.GroupedCalls != 0 || d.MinuteCalls != 0 || d.GroupedHits == 0 || d.MinuteHits == 0 {
		t.Errorf("rerun budget %+v", d)
	}
	if rep1.Variants[0].All.TotalNet != rep2.Variants[0].All.TotalNet || rep1.Variants[2].All.TotalNet != rep2.Variants[2].All.TotalNet {
		t.Errorf("rerun gave other numbers")
	}
	a, _ := json.Marshal(rep1.Variants)
	b, _ := json.Marshal(rep2.Variants)
	if !bytes.Equal(a, b) {
		t.Errorf("the variants' numbers differ between a fresh run and a cached one")
	}

	// -dry: no source at all (a call would be a nil dereference), the same answer
	f3, _ := newFetcher(nil, dir)
	rep3, _, err := run(context.Background(), f3, opts())
	if err != nil {
		t.Fatal(err)
	}
	if !rep3.Meta.Data.Dry || rep3.Variants[0].All.TotalNet != rep1.Variants[0].All.TotalNet {
		t.Errorf("dry run: %+v", rep3.Meta.Data)
	}
	// the failed ticker was never cached: in a dry run it is simply not there
	if rep3.Meta.Data.MissingDry < 0 {
		t.Errorf("missing %d", rep3.Meta.Data.MissingDry)
	}
}

func TestDryOnAnEmptyCacheExplains(t *testing.T) {
	f, _ := newFetcher(nil, t.TempDir())
	_, _, err := run(context.Background(), f, opts())
	if err == nil || !strings.Contains(err.Error(), "not enough") {
		t.Errorf("err = %v, want a not-enough-days error", err)
	}
}

func TestRateLimitBackoffThenSuccess(t *testing.T) {
	fk := world(t)
	fk.errFor = func(sym string, n int) error {
		if sym == "BEAT" && n <= 3 {
			return &feed.HTTPError{Status: 429, Msg: "slow down"}
		}
		return nil
	}
	f, slept := newFetcher(fk, t.TempDir())
	rep, _, err := run(context.Background(), f, opts())
	if err != nil {
		t.Fatal(err)
	}
	// three 429s, waited 2 s, 4 s, 8 s, then the bars came: not a failure
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}
	var got []time.Duration
	for _, d := range *slept {
		if d >= time.Second {
			got = append(got, d)
		}
	}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("waits %v, want %v", got, want)
	}
	if rep.Meta.Data.RateLimited != 3 || rep.Meta.Data.FailureCount != 0 || rep.Variants[0].All.Trades != 1 {
		t.Errorf("rate limited %d, failures %d, trades %d", rep.Meta.Data.RateLimited, rep.Meta.Data.FailureCount, rep.Variants[0].All.Trades)
	}
}

func TestRateLimitCapsOutButIsOnlyThatTickersFailure(t *testing.T) {
	fk := world(t)
	fk.errFor = func(sym string, n int) error {
		if sym == "FAIL" {
			return &feed.HTTPError{Status: 429, Msg: "slow down"}
		}
		return nil
	}
	f, slept := newFetcher(fk, t.TempDir())
	rep, _, err := run(context.Background(), f, opts())
	if err != nil {
		t.Fatal(err)
	}
	if fk.perSym["FAIL"] != maxRateRetries+1 || rep.Meta.Data.FailureCount != 1 {
		t.Errorf("FAIL tried %d times, failures %d", fk.perSym["FAIL"], rep.Meta.Data.FailureCount)
	}
	for _, d := range *slept {
		if d > rateBackoffMax {
			t.Errorf("a wait of %v is over the cap", d)
		}
	}
	if rep.Variants[0].All.Trades != 1 {
		t.Errorf("BEAT must still trade")
	}
}

func TestThreeConsecutiveFailuresThenOneThenSuccess(t *testing.T) {
	// two failures then success: retried, not a data failure
	fk := world(t)
	fk.errFor = func(sym string, n int) error {
		if sym == "BEAT" && n <= 2 {
			return errors.New("connection reset")
		}
		return nil
	}
	f, _ := newFetcher(fk, t.TempDir())
	rep, _, err := run(context.Background(), f, opts())
	if err != nil || rep.Meta.Data.FailureCount != 0 || fk.perSym["BEAT"] != 3 || rep.Variants[0].All.Trades != 1 {
		t.Errorf("err %v failures %d calls %d", err, rep.Meta.Data.FailureCount, fk.perSym["BEAT"])
	}
}

func TestRefusedKeyStopsTheRun(t *testing.T) {
	fk := world(t)
	fk.errFor = func(sym string, n int) error { return &feed.HTTPError{Status: 403, Msg: "NOT_AUTHORIZED"} }
	ctx, abort := context.WithCancelCause(context.Background())
	f, _ := newFetcher(fk, t.TempDir())
	f.abort = abort
	_, _, err := run(ctx, f, opts())
	var fe *fatalError
	if err == nil || !errors.As(err, &fe) {
		t.Fatalf("err = %v, want the fatal refused-key error", err)
	}
	if fk.perSym["BEAT"] > 1 || fk.perSym["FAIL"] > 1 {
		t.Errorf("a refused key was retried: %v", fk.perSym)
	}
}

func TestADeadFeedStopsTheRun(t *testing.T) {
	fk := world(t)
	// many candidates, every one failing: after maxConsecFail the run stops itself
	for i := 0; i < 60; i++ {
		sym := "T" + string(rune('A'+i/26)) + string(rune('A'+i%26))
		for d, m := range fk.grouped {
			if d == "2026-10-05" {
				m[sym] = db(2.5, 2.6, 8_000_000)
			} else {
				m[sym] = db(2, 2.1, 1_000_000)
			}
		}
	}
	fk.errFor = func(sym string, n int) error { return &feed.HTTPError{Status: 500, Msg: "boom"} }
	ctx, abort := context.WithCancelCause(context.Background())
	f, _ := newFetcher(fk, t.TempDir())
	f.abort = abort
	f.conc = 1
	_, _, err := run(ctx, f, opts())
	if err == nil || !strings.Contains(err.Error(), "consecutive requests failed") {
		t.Fatalf("err = %v", err)
	}
	if n := fk.barsN.Load(); n > int64(maxConsecFail*maxBadAttempts+maxBadAttempts) {
		t.Errorf("%d minute calls: the run did not stop", n)
	}
}

func TestHolidaysAreFoundByEmptyResultsAndSkipped(t *testing.T) {
	fk := world(t)
	f, _ := newFetcher(fk, t.TempDir())
	days, holidays, err := discoverDays(context.Background(), f, time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), 11)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 11 || days[0].date != "2026-09-18" || days[10].date != "2026-10-05" {
		t.Errorf("days %d: first %s last %s", len(days), days[0].date, days[len(days)-1].date)
	}
	for _, d := range days {
		if d.date == "2026-09-24" {
			t.Errorf("the holiday is among the trading days")
		}
		if wd := mustDate(t, d.date).Weekday(); wd == time.Saturday || wd == time.Sunday {
			t.Errorf("%s is a weekend", d.date)
		}
	}
	if len(holidays) != 1 || holidays[0] != "2026-09-24" {
		t.Errorf("holidays %v", holidays)
	}
}

func mustDate(t testing.TB, s string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCorruptCacheEntriesAreRefetched(t *testing.T) {
	dir := t.TempDir()
	fk := world(t)
	f, _ := newFetcher(fk, dir)
	if _, _, err := run(context.Background(), f, opts()); err != nil {
		t.Fatal(err)
	}
	// truncate a bars file and a grouped file
	c := cache{dir: dir}
	if err := os.WriteFile(c.barsPath("BEAT", "2026-10-05"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.groupedPath("2026-10-02"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	f2, _ := newFetcher(fk, dir)
	rep, _, err := run(context.Background(), f2, opts())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Meta.Data.MinuteCalls != 1 || rep.Meta.Data.GroupedCalls != 1 || rep.Variants[0].All.Trades != 1 {
		t.Errorf("budget %+v", rep.Meta.Data)
	}
}

func TestCacheKeysAndRoundTrip(t *testing.T) {
	c := cache{dir: t.TempDir()}
	if got := filepath.Base(c.barsPath("BRK.A", "2026-10-05")); got != "BRK.A_2026-10-05.json" {
		t.Errorf("bars file %q", got)
	}
	if got := filepath.Base(c.barsPath("A/B:C", "2026-10-05")); strings.ContainsAny(got, `/:\`) {
		t.Errorf("unsafe file name %q", got)
	}
	if got := filepath.Base(c.barsPath("CON", "2026-10-05")); got == "CON.json" {
		t.Errorf("a bare Windows device name: %q", got)
	}
	bars := []core.Bar{{T: 1790928000000, O: 0.4295, H: 0.43, L: 0.412, C: 0.43, V: 531}, {T: 1790929980000, O: 1.5, H: 1.5, L: 1.5, C: 1.5, V: 12345678901}}
	if err := c.saveBars("BEAT", "2026-10-02", "2026-10-05", bars); err != nil {
		t.Fatal(err)
	}
	got, ok := c.loadBars("BEAT", "2026-10-02", "2026-10-05")
	if !ok || len(got) != 2 || got[0] != bars[0] || got[1] != bars[1] {
		t.Errorf("round trip: %v %v", got, ok)
	}
	if _, ok := c.loadBars("BEAT", "2026-10-01", "2026-10-05"); ok {
		t.Errorf("a different from-date must be a miss")
	}
	if err := c.saveBars("EMPTY", "a", "2026-10-05", nil); err != nil {
		t.Fatal(err)
	}
	if got, ok := c.loadBars("EMPTY", "a", "2026-10-05"); !ok || len(got) != 0 {
		t.Errorf("an empty response is cached too: %v %v", got, ok)
	}
	m := map[string]core.DailyBar{"A": {O: 1, H: 2, L: 0.5, C: 1.5, V: 99}}
	if err := c.saveGrouped("2026-10-05", m); err != nil {
		t.Fatal(err)
	}
	gm, ok := c.loadGrouped("2026-10-05")
	if !ok || gm["A"].V != 99 || gm["A"].H != 2 || gm["A"].Date != "2026-10-05" {
		t.Errorf("grouped round trip: %v %v", gm, ok)
	}
	if err := c.saveGrouped("2026-09-07", map[string]core.DailyBar{}); err != nil {
		t.Fatal(err)
	}
	if hm, ok := c.loadGrouped("2026-09-07"); !ok || len(hm) != 0 {
		t.Errorf("a holiday is cached as an empty day: %v %v", hm, ok)
	}
}

func TestConcurrencyNeverExceedsTheLimit(t *testing.T) {
	var cur, peak atomic.Int64
	forEach(context.Background(), 50, 4, func(i int) {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		cur.Add(-1)
	})
	if p := peak.Load(); p > 4 || p < 2 {
		t.Errorf("peak concurrency %d, want 2..4", p)
	}
}

func TestNeverWritesUnderTheConfigDir(t *testing.T) {
	cfg := config.ConfigDir()
	for _, p := range []string{cfg, filepath.Join(cfg, "cache"), filepath.Join(cfg, "x", "y"), strings.ToUpper(cfg)} {
		if !underDir(p, cfg) {
			t.Errorf("%s must count as inside %s", p, cfg)
		}
	}
	for _, p := range []string{filepath.Dir(cfg), cfg + "-backtest", filepath.Join(t.TempDir(), "cache")} {
		if underDir(p, cfg) {
			t.Errorf("%s must not count as inside %s", p, cfg)
		}
	}
	// the CLI refuses them before it does anything (no network is reachable from here: it must
	// fail on the flag, not on the key)
	var out, errb bytes.Buffer
	if code := realMain([]string{"-dry", "-cache", filepath.Join(cfg, "cache")}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "never writes there") {
		t.Errorf("-cache inside the config dir: code %d, %q", code, errb.String())
	}
	if code := realMain([]string{"-dry", "-out", cfg}, &out, &errb); code != 2 {
		t.Errorf("-out inside the config dir: code %d", code)
	}
}

func TestFlagValidation(t *testing.T) {
	for _, args := range [][]string{
		{"-days", "0"}, {"-days", "999"}, {"-end", "yesterday"}, {"-concurrency", "0"}, {"-variants", "V9"},
		{"-resamples", "5"}, {"-hist", "2"}, {"stray"}, {"-maxcand", "0"},
	} {
		var out, errb bytes.Buffer
		if code := realMain(append([]string{"-dry"}, args...), &out, &errb); code != 2 {
			t.Errorf("%v: exit %d, want 2 (stderr %q)", args, code, errb.String())
		}
	}
	// a dry run on an empty cache is a clean error, exit 1, no network
	var out, errb bytes.Buffer
	dir := t.TempDir()
	if code := realMain([]string{"-dry", "-days", "3", "-cache", filepath.Join(dir, "c"), "-out", filepath.Join(dir, "o")}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "not enough") {
		t.Errorf("dry on an empty cache: exit %d, %q", code, errb.String())
	}
}

func TestPickVariants(t *testing.T) {
	all, err := pickVariants(nil)
	if err != nil || len(all) != 5 {
		t.Fatalf("all: %d %v", len(all), err)
	}
	two, err := pickVariants([]string{"v1", " V3 ", "V1"})
	if err != nil || len(two) != 2 || two[0].Name != "V1" || two[1].Name != "V3" {
		t.Errorf("subset: %+v %v", two, err)
	}
	if _, err := pickVariants([]string{"V0"}); err == nil {
		t.Errorf("V0 must be refused")
	}
}

func TestWeekdaysBack(t *testing.T) {
	got := weekdaysBack(time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), 7) // a Monday
	want := []string{"2026-10-05", "2026-10-02", "2026-10-01", "2026-09-30", "2026-09-29", "2026-09-28", "2026-09-25"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v want %v", got, want)
	}
	// a Saturday end starts at the Friday
	if got := weekdaysBack(time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC), 1); got[0] != "2026-10-02" {
		t.Errorf("Saturday end -> %v", got)
	}
}
