package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/backtest"
	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/feed"
)

// source is the REST calls the backtest makes. liveSource is the real one (the
// scanner's own client for the bars, plus the splits listing the client has no
// method for); tests pass a fake, and -dry passes nothing.
type source interface {
	GroupedDaily(ctx context.Context, date string) (map[string]core.DailyBar, error)
	IntradayBetween(ctx context.Context, sym, from, to string) ([]core.Bar, error)
	// Splits lists the splits executed from..to (ISO dates, inclusive).
	Splits(ctx context.Context, from, to string) ([]backtest.StockSplit, error)
}

// liveSource is *feed.Client for the bars and a splitClient for the listing.
type liveSource struct {
	*feed.Client
	splitList *splitClient
}

func (s liveSource) Splits(ctx context.Context, from, to string) ([]backtest.StockSplit, error) {
	return s.splitList.Splits(ctx, from, to)
}

var _ source = liveSource{}

const (
	maxRateRetries   = 10               // HTTP 429s one call waits through before it gives up
	rateBackoffFirst = 2 * time.Second  // 2, 4, 8, ... capped
	rateBackoffMax   = 60 * time.Second //
	maxBadAttempts   = 3                // consecutive non-429 failures for one request before it is a data failure
	maxConsecFail    = 25               // consecutive failed requests across the run before the run itself stops (a dead feed, not a bad ticker)
	maxFailuresKept  = 200              // failures listed in the report (all are counted)
)

// ─── Cache ───────────────────────────────────────────────────────────────

// cache stores every response so a rerun never refetches. Grouped days are
// gzip-compressed JSON (~12k tickers each); minute bars are plain compact JSON
// keyed by symbol and date: <dir>/bars/<SYM>_<date>.json.
type cache struct{ dir string }

type groupedFile struct {
	Date string       `json:"date"`
	Bars []groupedRow `json:"bars"`
}

type groupedRow struct {
	S string  `json:"s"`
	O float64 `json:"o"`
	H float64 `json:"h"`
	L float64 `json:"l"`
	C float64 `json:"c"`
	V int64   `json:"v"`
}

type barsFile struct {
	Sym  string       `json:"sym"`
	From string       `json:"from"`
	To   string       `json:"to"`
	Bars [][6]float64 `json:"bars"` // t, o, h, l, c, v
}

// safeSym keeps a ticker usable as a file name (tickers carry '.', '-' and, on
// the odd listing, other characters).
func safeSym(sym string) string {
	var b strings.Builder
	for _, r := range sym {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

func (c cache) groupedPath(date string) string {
	return filepath.Join(c.dir, "grouped", date+".json.gz")
}

func (c cache) barsPath(sym, date string) string {
	return filepath.Join(c.dir, "bars", safeSym(sym)+"_"+date+".json")
}

// splitsPath is the splits listing for executions from..to. The "to" is the as-of
// date the bars were adjusted to, so a listing is only reused for the same pair.
func (c cache) splitsPath(from, to string) string {
	return filepath.Join(c.dir, "splits", from+"_"+to+".json")
}

type splitsFile struct {
	From   string                `json:"from"`
	To     string                `json:"to"`
	Splits []backtest.StockSplit `json:"splits"`
}

func (c cache) loadSplits(from, to string) ([]backtest.StockSplit, bool) {
	raw, err := os.ReadFile(c.splitsPath(from, to))
	if err != nil {
		return nil, false
	}
	var sf splitsFile
	if err := json.Unmarshal(raw, &sf); err != nil || sf.From != from || sf.To != to || sf.Splits == nil {
		return nil, false // corrupt, or for another range: a miss
	}
	return sf.Splits, true
}

func (c cache) saveSplits(from, to string, splits []backtest.StockSplit) error {
	if splits == nil {
		splits = []backtest.StockSplit{} // an empty listing is cached too (null would read as a miss)
	}
	sf := splitsFile{From: from, To: to, Splits: splits}
	return writeAtomic(c.splitsPath(from, to), func(w io.Writer) error { return json.NewEncoder(w).Encode(sf) })
}

// writeAtomic writes via a temp file in the same directory, so a killed run
// never leaves a half-written cache entry that a rerun would trust.
func writeAtomic(path string, write func(io.Writer) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err := write(tmp); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

func (c cache) loadGrouped(date string) (map[string]core.DailyBar, bool) {
	f, err := os.Open(c.groupedPath(date))
	if err != nil {
		return nil, false
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, false
	}
	defer zr.Close()
	var gf groupedFile
	if err := json.NewDecoder(zr).Decode(&gf); err != nil || gf.Date != date {
		return nil, false // corrupt or foreign: a miss, refetched
	}
	m := make(map[string]core.DailyBar, len(gf.Bars))
	for _, r := range gf.Bars {
		m[r.S] = core.DailyBar{Date: date, O: r.O, H: r.H, L: r.L, C: r.C, V: r.V}
	}
	return m, true
}

func (c cache) saveGrouped(date string, m map[string]core.DailyBar) error {
	gf := groupedFile{Date: date, Bars: make([]groupedRow, 0, len(m))}
	for sym, b := range m {
		gf.Bars = append(gf.Bars, groupedRow{S: sym, O: b.O, H: b.H, L: b.L, C: b.C, V: b.V})
	}
	return writeAtomic(c.groupedPath(date), func(w io.Writer) error {
		zw := gzip.NewWriter(w)
		if err := json.NewEncoder(zw).Encode(gf); err != nil {
			return err
		}
		return zw.Close()
	})
}

func (c cache) loadBars(sym, from, to string) ([]core.Bar, bool) {
	raw, err := os.ReadFile(c.barsPath(sym, to))
	if err != nil {
		return nil, false
	}
	var bf barsFile
	if err := json.Unmarshal(raw, &bf); err != nil || bf.Sym != sym || bf.To != to || bf.From != from {
		return nil, false // corrupt, or fetched for another range: a miss
	}
	out := make([]core.Bar, len(bf.Bars))
	for i, r := range bf.Bars {
		out[i] = core.Bar{T: int64(r[0]), O: r[1], H: r[2], L: r[3], C: r[4], V: int64(r[5])}
	}
	return out, true
}

func (c cache) saveBars(sym, from, to string, bars []core.Bar) error {
	bf := barsFile{Sym: sym, From: from, To: to, Bars: make([][6]float64, len(bars))}
	for i, b := range bars {
		bf.Bars[i] = [6]float64{float64(b.T), b.O, b.H, b.L, b.C, float64(b.V)}
	}
	return writeAtomic(c.barsPath(sym, to), func(w io.Writer) error { return json.NewEncoder(w).Encode(bf) })
}

// ─── Fetcher ─────────────────────────────────────────────────────────────

// fatalError is a failure no retry can fix (the key is refused): the run stops.
type fatalError struct{ err error }

func (e *fatalError) Error() string {
	return "the feed refuses the request, stopping: " + e.err.Error()
}
func (e *fatalError) Unwrap() error { return e.err }

func isAuthErr(err error) bool {
	var he *feed.HTTPError
	return errors.As(err, &he) && (he.Status == http.StatusUnauthorized || he.Status == http.StatusForbidden)
}

// fetcher is the cached, rate-limit-aware, bounded-concurrency way to the feed.
type fetcher struct {
	src   source // nil in -dry: nothing here touches the network
	cache cache
	conc  int
	log   func(format string, args ...any)
	sleep func(ctx context.Context, d time.Duration) error
	abort context.CancelCauseFunc // stops the run (a refused key, a dead feed)

	groupedCalls, groupedHits atomic.Int64
	minuteCalls, minuteHits   atomic.Int64
	splitsCalls, splitsHits   atomic.Int64
	rateLimited, missingDry   atomic.Int64
	consecFail                atomic.Int64

	mu        sync.Mutex
	failures  []backtest.Failure
	failCount int
}

func realSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (f *fetcher) dry() bool { return f.src == nil }

func (f *fetcher) fail(key string, err error) {
	f.mu.Lock()
	f.failCount++
	if len(f.failures) < maxFailuresKept {
		f.failures = append(f.failures, backtest.Failure{Key: key, Err: err.Error()})
	}
	f.mu.Unlock()
	f.log("FAILED %s: %v", key, err)
	if f.consecFail.Add(1) >= maxConsecFail && f.abort != nil {
		f.abort(fmt.Errorf("%d consecutive requests failed: the feed is down or refusing, stopping (last: %w)", maxConsecFail, err))
	}
}

// call runs one request with the retry policy: HTTP 429 waits (2 s, doubling to
// 60 s, at most maxRateRetries times) and does not count as a failure; any other
// error is retried until maxBadAttempts in a row, then it is a data failure for
// this request alone; a refused key (401/403) is fatal at once. calls counts
// every attempt on the wire.
func (f *fetcher) call(ctx context.Context, key string, calls *atomic.Int64, do func() error) error {
	rate, bad := 0, 0
	for {
		calls.Add(1)
		err := do()
		if err == nil {
			f.consecFail.Store(0)
			return nil
		}
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		switch {
		case feed.IsRateLimitErr(err):
			f.rateLimited.Add(1)
			rate++
			if rate > maxRateRetries {
				return fmt.Errorf("%s: still rate-limited after %d retries: %w", key, maxRateRetries, err)
			}
			d := rateBackoffFirst << (rate - 1)
			if d > rateBackoffMax || d <= 0 {
				d = rateBackoffMax
			}
			f.log("429 on %s: waiting %v (retry %d/%d)", key, d, rate, maxRateRetries)
			if e := f.sleep(ctx, d); e != nil {
				return context.Cause(ctx)
			}
		case isAuthErr(err):
			fe := &fatalError{err}
			if f.abort != nil {
				f.abort(fe)
			}
			return fe
		default:
			bad++
			if bad >= maxBadAttempts {
				return fmt.Errorf("%s: %d consecutive failures, giving up: %w", key, bad, err)
			}
			if e := f.sleep(ctx, time.Duration(bad)*500*time.Millisecond); e != nil {
				return context.Cause(ctx)
			}
		}
	}
}

// grouped returns one day's grouped bars. known is false only in -dry (not
// cached) or after a failure; an empty map is a holiday.
func (f *fetcher) grouped(ctx context.Context, date string) (m map[string]core.DailyBar, known bool, err error) {
	if m, ok := f.cache.loadGrouped(date); ok {
		f.groupedHits.Add(1)
		return m, true, nil
	}
	if f.dry() {
		f.missingDry.Add(1)
		return nil, false, nil
	}
	err = f.call(ctx, "grouped "+date, &f.groupedCalls, func() error {
		var e error
		m, e = f.src.GroupedDaily(ctx, date)
		return e
	})
	if err != nil {
		return nil, false, err
	}
	if e := f.cache.saveGrouped(date, m); e != nil {
		f.log("cache write for grouped %s failed (the run goes on): %v", date, e)
	}
	return m, true, nil
}

// splits returns the splits executed from..to (to = the as-of date the feed's
// bars were adjusted to). known is false only in -dry when this exact listing is
// not cached: nothing was asked and nothing is un-adjusted. An empty listing is a
// valid answer.
func (f *fetcher) splits(ctx context.Context, from, to string) (sp []backtest.StockSplit, known bool, err error) {
	if sp, ok := f.cache.loadSplits(from, to); ok {
		f.splitsHits.Add(1)
		return sp, true, nil
	}
	if f.dry() {
		return nil, false, nil
	}
	err = f.call(ctx, "splits "+from+".."+to, &f.splitsCalls, func() error {
		var e error
		sp, e = f.src.Splits(ctx, from, to)
		return e
	})
	if err != nil {
		return nil, false, err
	}
	if e := f.cache.saveSplits(from, to, sp); e != nil {
		f.log("cache write for the splits listing failed (the run goes on): %v", e)
	}
	return sp, true, nil
}

// minuteBars returns a ticker's 1-minute bars from..to. ok is false when the
// bars are not available (-dry and not cached, or a data failure, which is
// recorded).
func (f *fetcher) minuteBars(ctx context.Context, sym, from, to string) (bars []core.Bar, ok bool) {
	if b, hit := f.cache.loadBars(sym, from, to); hit {
		f.minuteHits.Add(1)
		return b, true
	}
	if f.dry() {
		f.missingDry.Add(1)
		return nil, false
	}
	key := sym + " " + to
	err := f.call(ctx, key, &f.minuteCalls, func() error {
		var e error
		bars, e = f.src.IntradayBetween(ctx, sym, from, to)
		return e
	})
	if err != nil {
		if ctx.Err() == nil {
			f.fail(key, err)
		}
		return nil, false
	}
	if e := f.cache.saveBars(sym, from, to, bars); e != nil {
		f.log("cache write for %s failed (the run goes on): %v", key, e)
	}
	return bars, true
}

// forEach runs fn(i) for i in [0, n) on at most conc goroutines, returning when
// all are done or ctx is cancelled.
func forEach(ctx context.Context, n, conc int, fn func(i int)) {
	if conc < 1 {
		conc = 1
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < conc; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					continue
				}
				fn(i)
			}
		}()
	}
loop:
	for i := 0; i < n; i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break loop
		}
	}
	close(jobs)
	wg.Wait()
}

// stats snapshots the budget for the report.
func (f *fetcher) stats() backtest.DataStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	return backtest.DataStats{
		Dry:          f.dry(),
		GroupedCalls: int(f.groupedCalls.Load()), GroupedHits: int(f.groupedHits.Load()),
		MinuteCalls: int(f.minuteCalls.Load()), MinuteHits: int(f.minuteHits.Load()),
		SplitsCalls: int(f.splitsCalls.Load()), SplitsHits: int(f.splitsHits.Load()),
		RateLimited: int(f.rateLimited.Load()), MissingDry: int(f.missingDry.Load()),
		Failures: append([]backtest.Failure(nil), f.failures...), FailureCount: f.failCount,
	}
}
