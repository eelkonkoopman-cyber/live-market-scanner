package flagscan

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/emaflag"
)

// All of these drive the service by hand with a fake clock (env.step): no
// sleeps, no network, no engine. The Run loop itself (real tickers, goroutines,
// shutdown, races) is exercised in run_test.go.

func keysOf(m map[string]core.EmaFlagRef) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return equalStrings(a, b)
}

// recentBars is a loaded history that ends just before now, on today's ET date.
func recentBars(now time.Time) func(context.Context, string) ([]core.Bar, error) {
	return func(context.Context, string) ([]core.Bar, error) { return flatBars(now, 30, 10), nil }
}

func TestNewServiceRejectsAnUnusableConfig(t *testing.T) {
	full := newEnv(t).config()
	for name, mut := range map[string]func(*Config){
		"no Candidates": func(c *Config) { c.Candidates = nil },
		"no Bars":       func(c *Config) { c.Bars = nil },
		"no SetTap":     func(c *Config) { c.SetTap = nil },
		"no Publish":    func(c *Config) { c.Publish = nil },
	} {
		cfg := full
		mut(&cfg)
		if err := Run(context.Background(), cfg); err == nil {
			t.Errorf("%s: Run must refuse it", name)
		}
	}
	// Optional fields default sanely: no clock = time.Now, zero Params = the
	// Signal window's defaults.
	cfg := full
	cfg.Now, cfg.Params, cfg.Drops = nil, emaflag.Params{}, nil
	s, err := newService(cfg, defaultTuning())
	if err != nil {
		t.Fatal(err)
	}
	if s.params != emaflag.DefaultParams() || s.cfg.Now == nil {
		t.Fatalf("defaults not applied: %+v", s.params)
	}
}

func TestCandidatesAreAskedUncappedAndTheCapIsAppliedAndReported(t *testing.T) {
	e := newEnv(t)
	var gotMin float64
	var gotLimit = -1
	e.candFn = func(minX float64, limit int) []core.PmVolEntry {
		gotMin, gotLimit = minX, limit
		// deliberately unsorted and with a duplicate: the service ranks for itself
		return []core.PmVolEntry{{Sym: "D", PmVolX: 2}, {Sym: "A", PmVolX: 5}, {Sym: "F", PmVolX: 1.2},
			{Sym: "C", PmVolX: 3}, {Sym: "B", PmVolX: 4}, {Sym: "E", PmVolX: 1.5}, {Sym: "A", PmVolX: 4.9}}
	}
	e.setBars(recentBars(e.clock()))
	tm := defaultTuning()
	tm.maxCandidates = 3

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	s := e.service(tm)
	e.step(s, e.clock())

	if gotMin != MinPmVolX || gotLimit != 0 {
		t.Fatalf("Candidates(%v, %d): the service must ask for >= %v, uncapped (limit 0)", gotMin, gotLimit, MinPmVolX)
	}
	if got := keysOf(e.lastPub()); !sameStrings(got, []string{"A", "B", "C"}) {
		t.Fatalf("evaluated %v, want the three strongest A B C", got)
	}
	st := s.stats()
	if st.candidates != 3 || st.qualified != 6 || st.beyondCap != 3 {
		t.Fatalf("stats %+v: want 3 tracked, 6 qualified, 3 beyond the cap", st)
	}
	if !strings.Contains(buf.String(), "beyondCap=3") || !strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("the cap must not be silent; log was:\n%s", buf.String())
	}
	// The same shortfall is not re-logged every 30 s, only a change is.
	buf.Reset()
	e.step(s, e.clock().Add(31*time.Second))
	if strings.Contains(buf.String(), "than the cap") {
		t.Fatalf("an unchanged cap shortfall was logged again:\n%s", buf.String())
	}
}

func TestSetTapPrecedesTheFirstBackfill(t *testing.T) {
	e := newEnv(t)
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2}, core.PmVolEntry{Sym: "B", PmVolX: 1.5})
	e.setBars(recentBars(e.clock()))
	s := e.service(defaultTuning())
	e.step(s, e.clock())

	log := e.logCopy()
	if len(log) == 0 || log[0] != "tap:A,B" {
		t.Fatalf("the first call must be SetTap with the whole set, got %v", log)
	}
	for _, l := range log {
		if strings.HasPrefix(l, "bars:") {
			return // a REST call after the tap: ordering satisfied
		}
	}
	t.Fatalf("no backfill happened: %v", log)
}

func TestAggregatesThatArriveDuringTheBackfillAreFoldedAfterIt(t *testing.T) {
	e := newEnv(t)
	now := et(8, 50, 20) // mid-minute: the 08:50 bar is forming
	e.setNow(now)
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2})

	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	m := now.UnixMilli() / minuteMs * minuteMs // 08:50
	rest := append(flatBars(et(8, 50, 0), 10, 10), core.Bar{T: m, O: 10.00, H: 10.10, L: 9.95, C: 10.05, V: 500})
	e.setBars(func(ctx context.Context, sym string) ([]core.Bar, error) {
		started <- struct{}{}
		select {
		case <-gate:
			return rest, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	s := e.service(defaultTuning())
	s.tick(now)
	s.handleCandidates(<-s.candCh, now) // tap set, REST call launched
	<-started

	// Seconds arrive while REST is in flight: they fold into a live-only series.
	for i, a := range []core.Agg{
		sec("A", m/minuteMs, 22, 10.02, 10.30, 10.01, 10.20, 40), // 08:50:22: pushes the high past REST's
		sec("A", m/minuteMs, 25, 10.20, 10.22, 9.90, 10.15, 30),  // a lower low than REST's
	} {
		s.handleAgg(a, now.Add(time.Duration(i+1)*time.Second))
	}
	if st := s.syms["A"]; st.loaded || len(st.bars) != 1 {
		t.Fatalf("before the load the series is the live-only buffer: loaded=%v bars=%+v", st.loaded, st.bars)
	}
	if got := s.stats(); got.queued != 1 || got.evaluated != 0 {
		t.Fatalf("an unloaded symbol must be queued and unevaluated: %+v", got)
	}

	close(gate)
	later := now.Add(10 * time.Second)
	s.handleFetch(<-s.results, later)
	st := s.syms["A"]
	if !st.loaded {
		t.Fatal("not loaded after the fetch")
	}
	n := len(st.bars)
	if n != 11 {
		t.Fatalf("%d bars after the load, want 10 history + the 08:50 bar", n)
	}
	// REST's open, the widest range of the two views, REST's volume, and the
	// live close (an aggregate was folded after the request started, and the
	// 08:50 bar is still the newest one).
	want50 := core.Bar{T: m, O: 10.00, H: 10.30, L: 9.90, C: 10.15, V: 500}
	if st.bars[n-1] != want50 {
		t.Fatalf("overlap bar %+v, want %+v", st.bars[n-1], want50)
	}
	// The next minute then opens over the tap, on top of the merged series.
	s.handleAgg(sec("A", m/minuteMs+1, 0, 10.2, 10.4, 10.1, 10.35, 20), later.Add(time.Second))
	if len(st.bars) != 12 || st.bars[11] != (core.Bar{T: m + minuteMs, O: 10.2, H: 10.4, L: 10.1, C: 10.35, V: 20}) {
		t.Fatalf("a post-load aggregate must open the next bar: %+v", st.bars[len(st.bars)-1])
	}
	s.evaluate(later.Add(time.Second))
	if _, ok := e.lastPub()["A"]; !ok {
		t.Fatal("the loaded symbol was not published")
	}
}

func TestAnOldBuffersCloseDoesNotOverwriteALaterRESTClose(t *testing.T) {
	// Two symbols, ONE worker: B waits for A's fetch, so the seconds B sees
	// while queued are OLDER than its own REST request. REST (asked later) owns
	// the overlap bar's close.
	e := newEnv(t)
	now := et(8, 50, 20)
	e.setNow(now)
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 3}, core.PmVolEntry{Sym: "B", PmVolX: 2})
	m := now.UnixMilli() / minuteMs * minuteMs
	e.setBars(func(_ context.Context, sym string) ([]core.Bar, error) {
		return append(flatBars(et(8, 50, 0), 10, 10), core.Bar{T: m, O: 10, H: 10.2, L: 9.9, C: 10.18, V: 100}), nil
	})
	tm := defaultTuning()
	tm.workers = 1
	s := e.service(tm)
	s.tick(now)
	s.handleCandidates(<-s.candCh, now) // A is fetching, B is queued behind it
	if s.syms["B"].fetching {
		t.Fatal("B must be waiting for a free worker")
	}
	s.handleAgg(sec("B", m/minuteMs, 21, 10.1, 10.1, 10.1, 10.1, 5), now.Add(time.Second)) // before B's request starts
	later := now.Add(5 * time.Second)
	s.handleFetch(<-s.results, later) // A done; B's fetch starts now (dispatch), AFTER the agg above
	s.handleFetch(<-s.results, later.Add(time.Second))
	b := s.syms["B"].bars
	if got := b[len(b)-1].C; got != 10.18 {
		t.Fatalf("B's overlap close = %v, want REST's 10.18 (the buffered second predates the request)", got)
	}
}

// A REST call that was asked inside a minute and answered after it ended: REST
// served a snapshot of the forming minute, the tap folded the rest of that
// minute plus the first second of the next while the call was in flight. The
// minute is CLOSED by the time the merge runs, and the closed-bar rules (stop by
// close, the EMA, flag extend / ready) read its close, so it must be the
// minute's last print - the live one - not REST's mid-minute snapshot, which
// nothing would repair until the next resync 2.5 to 5 minutes later.
func TestAMinuteThatEndsWhileTheRequestIsInFlightTakesTheLiveFinalClose(t *testing.T) {
	e := newEnv(t)
	now := et(8, 50, 20) // inside 08:50
	e.setNow(now)
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2})
	m := now.UnixMilli() / minuteMs // the 08:50 minute index

	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	rest := append(flatBars(et(8, 50, 0), 10, 10), core.Bar{T: m * minuteMs, O: 10.00, H: 10.10, L: 9.95, C: 10.05, V: 500})
	e.setBars(func(ctx context.Context, sym string) ([]core.Bar, error) {
		started <- struct{}{}
		select {
		case <-gate:
			return rest, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	s := e.service(defaultTuning())
	s.tick(now)
	s.handleCandidates(<-s.candCh, now) // tap set, REST call launched at 08:50:20
	<-started

	// While REST is in flight the minute ends: its last print at 08:50:59, then
	// the first second of 08:51 opens the next bar.
	s.handleAgg(sec("A", m, 59, 10.38, 10.40, 10.36, 10.40, 20), now.Add(39*time.Second))
	s.handleAgg(sec("A", m+1, 0, 10.40, 10.42, 10.40, 10.41, 10), now.Add(40*time.Second))

	close(gate)
	s.handleFetch(<-s.results, now.Add(41*time.Second))
	bars := s.syms["A"].bars
	n := len(bars)
	if n != 12 {
		t.Fatalf("%d bars after the load, want 10 history + 08:50 + 08:51", n)
	}
	// REST's open and volume, the widest range, and the close of the minute's
	// LAST print (08:50:59), which REST - asked at 08:50:20 - cannot have known.
	if want := (core.Bar{T: m * minuteMs, O: 10.00, H: 10.40, L: 9.95, C: 10.40, V: 500}); bars[n-2] != want {
		t.Fatalf("the minute that ended in flight: %+v, want %+v", bars[n-2], want)
	}
	if bars[n-1].T != (m+1)*minuteMs || bars[n-1].C != 10.41 {
		t.Fatalf("the next minute's live bar must be kept: %+v", bars[n-1])
	}
}

// The other side of the same rule: asked AFTER the minute ended, REST's copy of
// it is complete (and may hold a late print the tap missed), so REST keeps its
// close even though the live series has moved past that minute too.
func TestAMinuteThatEndedBeforeTheRequestKeepsRESTsClose(t *testing.T) {
	e := newEnv(t)
	now := et(8, 51, 10) // 08:50 is over
	e.setNow(now)
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2})
	m := et(8, 50, 0).UnixMilli() / minuteMs

	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	rest := append(flatBars(et(8, 50, 0), 10, 10), core.Bar{T: m * minuteMs, O: 10.00, H: 10.45, L: 9.95, C: 10.42, V: 500})
	e.setBars(func(ctx context.Context, sym string) ([]core.Bar, error) {
		started <- struct{}{}
		select {
		case <-gate:
			return rest, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	s := e.service(defaultTuning())
	s.tick(now)
	s.handleCandidates(<-s.candCh, now)
	<-started
	// The tap's own copy of 08:50 (it joined at :59 and missed REST's 10.42 print)
	// and the live 08:51 bar.
	s.handleAgg(sec("A", m, 59, 10.38, 10.40, 10.36, 10.40, 20), now.Add(-10*time.Second))
	s.handleAgg(sec("A", m+1, 0, 10.40, 10.42, 10.40, 10.41, 10), now.Add(-10*time.Second))

	close(gate)
	s.handleFetch(<-s.results, now.Add(time.Second))
	bars := s.syms["A"].bars
	n := len(bars)
	if want := (core.Bar{T: m * minuteMs, O: 10.00, H: 10.45, L: 9.95, C: 10.42, V: 500}); n != 12 || bars[n-2] != want {
		t.Fatalf("a minute REST was asked about after it ended must keep REST's close: %d bars, %+v, want %+v", n, bars[n-2], want)
	}
}

func TestBackfillFailureRetriesWithExponentialBackoffAndNeverBlocksOthers(t *testing.T) {
	e := newEnv(t)
	t0 := e.clock()
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2}, core.PmVolEntry{Sym: "B", PmVolX: 1.5})
	var mu sync.Mutex
	attempts := map[string]int{}
	e.setBars(func(_ context.Context, sym string) ([]core.Bar, error) {
		mu.Lock()
		attempts[sym]++
		n := attempts[sym]
		mu.Unlock()
		if sym == "A" && n < 4 {
			return nil, errors.New("503")
		}
		return flatBars(t0, 30, 10), nil
	})
	s := e.service(defaultTuning())

	e.step(s, t0)
	if _, ok := e.lastPub()["B"]; !ok {
		t.Fatal("B must load although A's REST call failed")
	}
	if _, ok := e.lastPub()["A"]; ok {
		t.Fatal("A has no history yet: it must not be published")
	}
	// Failure 1 at +0 -> retry at +5; failure 2 at +5 -> +10 -> retry at +15;
	// failure 3 at +15 -> +20 -> retry at +35 (success).
	expect := []struct {
		at       time.Duration
		attempts int
	}{
		{1 * time.Second, 1}, {4 * time.Second, 1}, {5 * time.Second, 2},
		{14 * time.Second, 2}, {15 * time.Second, 3},
		{34 * time.Second, 3}, {35 * time.Second, 4},
	}
	for _, x := range expect {
		e.step(s, t0.Add(x.at))
		mu.Lock()
		got := attempts["A"]
		mu.Unlock()
		if got != x.attempts {
			t.Fatalf("at +%s: %d attempts for A, want %d", x.at, got, x.attempts)
		}
	}
	if _, ok := e.lastPub()["A"]; !ok {
		t.Fatal("A must be published once a retry succeeds")
	}
	mu.Lock()
	if attempts["B"] != 1 {
		t.Fatalf("B was fetched %d times (a loaded symbol is not retried)", attempts["B"])
	}
	mu.Unlock()
	if s.syms["A"].fails != 0 {
		t.Fatal("a success resets the failure count")
	}
}

func TestBackoffDoublesFromFiveSecondsToTwoMinutes(t *testing.T) {
	s := newEnv(t).service(defaultTuning())
	var got []time.Duration
	for n := 1; n <= 9; n++ {
		got = append(got, s.backoff(n))
	}
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second,
		2 * time.Minute, 2 * time.Minute, 2 * time.Minute, 2 * time.Minute}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("backoff(%d) = %v, want %v (all: %v)", i+1, got[i], want[i], got)
		}
	}
	if s.backoff(500) != 2*time.Minute {
		t.Fatal("a huge failure count must not overflow")
	}
}

func TestAPanickingBarsCallIsAnErrorNotACrash(t *testing.T) {
	e := newEnv(t)
	t0 := e.clock()
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2})
	calls := 0
	e.setBars(func(context.Context, string) ([]core.Bar, error) {
		calls++
		if calls == 1 {
			panic("boom")
		}
		return flatBars(t0, 30, 10), nil
	})
	s := e.service(defaultTuning())
	e.step(s, t0)
	if s.syms["A"].fails != 1 || s.syms["A"].loaded {
		t.Fatalf("panic must count as a failed fetch: %+v", s.syms["A"])
	}
	e.step(s, t0.Add(5*time.Second))
	if !s.syms["A"].loaded {
		t.Fatal("the retry after the panic must load")
	}
}

func TestAPanickingCandidateQueryKeepsTheCurrentSet(t *testing.T) {
	e := newEnv(t)
	t0 := e.clock()
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2})
	e.setBars(recentBars(t0))
	s := e.service(defaultTuning())
	e.step(s, t0)
	e.candFn = func(float64, int) []core.PmVolEntry { panic("engine hiccup") }
	e.step(s, t0.Add(31*time.Second))
	if _, ok := s.syms["A"]; !ok {
		t.Fatal("a failed candidate query must not read as 'no candidates'")
	}
	if _, ok := e.lastPub()["A"]; !ok {
		t.Fatal("A's verdict must survive a failed candidate query")
	}
	// ... and the next refresh recovers.
	e.candFn = nil
	e.step(s, t0.Add(62*time.Second))
	if s.candInflight {
		t.Fatal("candidate query still marked in flight")
	}
}

func TestCandidateChurnAdmitsDropsAndRepublishes(t *testing.T) {
	e := newEnv(t)
	t0 := e.clock()
	e.setBars(func(context.Context, string) ([]core.Bar, error) { return flatBars(e.clock(), 30, 10), nil })
	s := e.service(defaultTuning())

	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2}, core.PmVolEntry{Sym: "B", PmVolX: 1.4})
	e.step(s, t0)
	if got := keysOf(e.lastPub()); !sameStrings(got, []string{"A", "B"}) {
		t.Fatalf("published %v", got)
	}

	e.setCands(core.PmVolEntry{Sym: "B", PmVolX: 1.6}, core.PmVolEntry{Sym: "C", PmVolX: 1.1})
	e.step(s, t0.Add(31*time.Second))
	if got := keysOf(e.lastPub()); !sameStrings(got, []string{"B", "C"}) {
		t.Fatalf("after churn published %v, want B C (A dropped, C admitted and loaded)", got)
	}
	if _, ok := s.syms["A"]; ok {
		t.Fatal("A's state must be dropped")
	}
	if s.syms["B"].gen == 0 || s.syms["B"].pmVolX != 1.6 {
		t.Fatalf("a surviving symbol keeps its state and takes the new multiple: %+v", s.syms["B"])
	}

	// The last candidate leaves: an EMPTY map is published (it is what clears
	// the verdict in the scanner), then nothing more is published.
	e.setCands()
	e.step(s, t0.Add(62*time.Second))
	if m := e.lastPub(); m == nil || len(m) != 0 {
		t.Fatalf("expected an empty publish to clear the last verdicts, got %v", m)
	}
	n := len(e.published())
	for i := 1; i <= 100; i++ {
		e.step(s, t0.Add(62*time.Second).Add(time.Duration(i)*time.Second))
	}
	if len(e.published()) != n {
		t.Fatalf("%d publishes while nothing changed", len(e.published())-n)
	}

	want := [][]string{{"A", "B"}, {"B", "C"}, {}}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.taps) != len(want) {
		t.Fatalf("tap updates %v, want exactly %v (SetTap only on a change)", e.taps, want)
	}
	for i := range want {
		if !sameStrings(e.taps[i], want[i]) {
			t.Fatalf("tap update %d = %v, want %v", i, e.taps[i], want[i])
		}
	}
}

func TestNothingIsPublishedBeforeThereIsSomethingToSay(t *testing.T) {
	e := newEnv(t)
	s := e.service(defaultTuning())
	for i := 0; i < 70; i++ {
		e.step(s, e.clock().Add(time.Second))
	}
	if len(e.published()) != 0 || e.countLog("tap:") != 0 {
		t.Fatalf("an idle service with no candidates must be silent: %v", e.logCopy())
	}
}

// parkedBars is a Bars function whose every call parks until the test releases
// it; calls announces each call (in order) with its release function.
type parkedBars struct {
	mu    sync.Mutex
	calls chan parkedCall
}

type parkedCall struct {
	sym     string
	release func(bars []core.Bar, err error)
}

func newParkedBars() *parkedBars { return &parkedBars{calls: make(chan parkedCall, 256)} }

func (p *parkedBars) fn(ctx context.Context, sym string) ([]core.Bar, error) {
	type out struct {
		bars []core.Bar
		err  error
	}
	ch := make(chan out, 1)
	p.calls <- parkedCall{sym: sym, release: func(b []core.Bar, e error) { ch <- out{b, e} }}
	select {
	case o := <-ch:
		return o.bars, o.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *parkedBars) next(t *testing.T) parkedCall {
	t.Helper()
	select {
	case c := <-p.calls:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("no Bars call was made")
		return parkedCall{}
	}
}

func TestAFetchResultForADepartedOrReadmittedSymbolIsDiscarded(t *testing.T) {
	e := newEnv(t)
	t0 := e.clock()
	pb := newParkedBars()
	e.setBars(pb.fn)
	s := e.service(defaultTuning())

	// A is admitted; its fetch parks.
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2})
	s.tick(t0)
	s.handleCandidates(<-s.candCh, t0)
	first := pb.next(t)
	gen1 := s.syms["A"].gen

	// A drops out while the call is in flight, then comes back: a new admission,
	// a new generation, a new fetch.
	e.setCands()
	s.tick(t0.Add(31 * time.Second))
	s.handleCandidates(<-s.candCh, t0.Add(31*time.Second))
	if len(s.syms) != 0 {
		t.Fatal("A must be dropped")
	}
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2})
	s.tick(t0.Add(62 * time.Second))
	s.handleCandidates(<-s.candCh, t0.Add(62*time.Second))
	second := pb.next(t)
	if s.syms["A"].gen == gen1 {
		t.Fatal("a re-admission must get a new generation")
	}

	// The OLD call finishes first, with a stale-looking history: ignored.
	first.release(flatBars(t0, 5, 99), nil)
	s.handleFetch(<-s.results, t0.Add(63*time.Second))
	if st := s.syms["A"]; st.loaded || len(st.bars) != 0 || !st.fetching {
		t.Fatalf("a result from the old admission must not load the new one: %+v", st)
	}
	// The new call then finishes and loads.
	second.release(flatBars(t0, 30, 10), nil)
	s.handleFetch(<-s.results, t0.Add(64*time.Second))
	if st := s.syms["A"]; !st.loaded || len(st.bars) != 30 || st.fetching {
		t.Fatalf("the current admission's result must load: %+v", st)
	}
	if s.inflight != 0 {
		t.Fatalf("in-flight accounting leaked: %d", s.inflight)
	}
}

func TestBackfillWorkersAreBounded(t *testing.T) {
	e := newEnv(t)
	t0 := e.clock()
	pb := newParkedBars()
	e.setBars(pb.fn)
	var cands []core.PmVolEntry
	for i := 0; i < 10; i++ {
		cands = append(cands, core.PmVolEntry{Sym: string(rune('A' + i)), PmVolX: float64(20 - i)})
	}
	e.setCands(cands...)
	s := e.service(defaultTuning())
	s.tick(t0)
	s.handleCandidates(<-s.candCh, t0)

	if s.inflight != BackfillWorkers {
		t.Fatalf("%d fetches in flight, want exactly BackfillWorkers = %d", s.inflight, BackfillWorkers)
	}
	fetching := func() string {
		var f []string
		for sym, st := range s.syms {
			if st.fetching {
				f = append(f, sym)
			}
		}
		sort.Strings(f)
		return strings.Join(f, "")
	}
	// Strongest multiples first: the first wave is the top four by rank.
	if got := fetching(); got != "ABCD" {
		t.Fatalf("first wave %q, want the four strongest ABCD", got)
	}
	parked := map[string]parkedCall{}
	for i := 0; i < BackfillWorkers; i++ { // the goroutines announce in any order
		c := pb.next(t)
		parked[c.sym] = c
	}
	// Finish one at a time, in rank order: each freed slot must start the next
	// queued symbol at once (not a tick later), strongest first, never more
	// than BackfillWorkers at a time.
	for i := 0; i < 10; i++ {
		sym := string(rune('A' + i))
		c, ok := parked[sym]
		if !ok {
			t.Fatalf("%s should be in flight by now; fetching %q", sym, fetching())
		}
		delete(parked, sym)
		c.release(flatBars(t0, 30, 10), nil)
		s.handleFetch(<-s.results, t0.Add(time.Duration(i+1)*time.Second))
		if s.inflight > BackfillWorkers {
			t.Fatalf("%d in flight after a result", s.inflight)
		}
		if i+BackfillWorkers < 10 {
			next := string(rune('A' + i + BackfillWorkers))
			if !s.syms[next].fetching {
				t.Fatalf("after %s finished the freed slot must start %s; fetching %q", sym, next, fetching())
			}
			nc := pb.next(t)
			parked[nc.sym] = nc
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.maxIn > BackfillWorkers {
		t.Fatalf("up to %d concurrent Bars calls", e.maxIn)
	}
	if got := s.stats(); got.queued != 0 {
		t.Fatalf("everything must be loaded: %+v", got)
	}
}

func TestBackfillsGoBeforeResyncs(t *testing.T) {
	e := newEnv(t)
	t0 := e.clock()
	e.setBars(recentBars(t0))
	tm := defaultTuning()
	tm.workers = 1
	s := e.service(tm)
	e.setCands(core.PmVolEntry{Sym: "OLD", PmVolX: 2})
	e.step(s, t0)

	// At the instant OLD's resync falls due a new symbol is admitted: both are
	// due, there is ONE free worker, and the brand-new symbol (no verdict at
	// all) must be fetched before the refresh of one that already has one.
	due := s.syms["OLD"].due
	pb := newParkedBars()
	e.setBars(pb.fn)
	s.applyCandidates([]core.PmVolEntry{{Sym: "OLD", PmVolX: 2}, {Sym: "NEW", PmVolX: 1.2}}, due)
	s.dispatch(due)
	if c := pb.next(t); c.sym != "NEW" {
		t.Fatalf("the first fetch was %s, want the unloaded NEW before OLD's resync", c.sym)
	}
	if s.syms["OLD"].fetching {
		t.Fatal("OLD's resync must wait for the single worker")
	}
}

func TestResyncReplacesHistoryUpToRESTsLastBarAndKeepsLiveBarsAfterIt(t *testing.T) {
	e := newEnv(t)
	t0 := et(8, 50, 0)
	e.setNow(t0)
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2})
	base := flatBars(t0, 30, 10) // 08:20 .. 08:49
	var mu sync.Mutex
	var restCalls int
	var resyncHist []core.Bar
	e.setBars(func(_ context.Context, sym string) ([]core.Bar, error) {
		mu.Lock()
		defer mu.Unlock()
		restCalls++
		if restCalls == 1 {
			return base, nil
		}
		return resyncHist, nil
	})
	s := e.service(defaultTuning())
	e.step(s, t0)
	if restCalls != 1 {
		t.Fatalf("%d REST calls after admission", restCalls)
	}

	// The first resync is phased into (2.5 min, 5 min]: nothing before, a call at.
	due := s.syms["A"].due
	if d := due.Sub(t0); d < ResyncEvery/2 || d >= ResyncEvery {
		t.Fatalf("first resync due in %s, want within [%s, %s)", d, ResyncEvery/2, ResyncEvery)
	}

	// Live aggregates fold minute after minute meanwhile (:00 and :30 of each).
	m0 := t0.UnixMilli() / minuteMs
	for now := t0; now.Before(due); now = now.Add(30 * time.Second) {
		e.setNow(now)
		minute := now.UnixMilli()/minuteMs - m0
		sc := int64(now.Second())
		p := 10 + float64(minute)*0.01
		s.handleAgg(sec("A", m0+minute, sc, p, p+0.02, p-0.02, p+0.01, 10), now)
	}
	// REST at the resync: the closed history incl. every minute before the
	// current one, with a LATE-PRINT correction to the 08:51 bar.
	dueMinute := due.UnixMilli() / minuteMs
	hist := append([]core.Bar(nil), base...)
	for m := m0; m < dueMinute; m++ {
		p := 10 + float64(m-m0)*0.01
		hist = append(hist, core.Bar{T: m * minuteMs, O: p, H: p + 0.02, L: p - 0.02, C: p + 0.01, V: 20})
	}
	for i := range hist {
		if hist[i].T == (m0+1)*minuteMs {
			hist[i].C, hist[i].H = 10.5, 10.6
		}
	}
	mu.Lock()
	resyncHist = hist
	mu.Unlock()

	// One second before it is due: no call. At the due time: a call.
	e.step(s, due.Add(-time.Second))
	if restCalls != 1 {
		t.Fatalf("resync fired early (%d calls)", restCalls)
	}
	// ... then a live second of the CURRENT minute arrives, before the resync lands.
	live := sec("A", dueMinute, 0, 11.1, 11.3, 11.0, 11.2, 7)
	s.handleAgg(live, due)
	e.step(s, due)
	if restCalls != 2 {
		t.Fatalf("resync did not fire at its due time (%d calls)", restCalls)
	}

	bars := s.syms["A"].bars
	var got51 *core.Bar
	for i := range bars {
		if bars[i].T == (m0+1)*minuteMs {
			got51 = &bars[i]
		}
	}
	if got51 == nil || got51.C != 10.5 || got51.H != 10.6 {
		t.Fatalf("REST's corrected 08:51 bar must replace the live-folded one: %+v", got51)
	}
	last := bars[len(bars)-1]
	if last.T != dueMinute*minuteMs || last.H != 11.3 || last.C != 11.2 {
		t.Fatalf("the live bar after REST's last bar must be kept (with the second that arrived before the resync landed): %+v", last)
	}
	if prev := bars[len(bars)-2]; prev.T != (dueMinute-1)*minuteMs || prev.C != 10+float64(dueMinute-1-m0)*0.01+0.01 {
		t.Fatalf("REST's last bar must be the one before the live bar: %+v", prev)
	}
	for i := 1; i < len(bars); i++ {
		if bars[i].T <= bars[i-1].T {
			t.Fatalf("merged series not strictly ascending at %d", i)
		}
	}
	if next := s.syms["A"].due.Sub(due); next != ResyncEvery {
		t.Fatalf("after a resync the next is due in %s, want %s", next, ResyncEvery)
	}
}

func TestResyncFailureKeepsTheLiveVerdictAndRetries(t *testing.T) {
	e := newEnv(t)
	t0 := e.clock()
	fail := false
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2})
	e.setBars(func(context.Context, string) ([]core.Bar, error) {
		if fail {
			return nil, errors.New("429")
		}
		return flatBars(t0, 30, 10), nil
	})
	s := e.service(defaultTuning())
	e.step(s, t0)
	before := e.lastPub()["A"]
	n := len(e.published())

	fail = true
	due := s.syms["A"].due
	e.step(s, due)
	if s.syms["A"].fails != 1 || !s.syms["A"].loaded {
		t.Fatalf("a failed resync keeps the loaded series and counts the failure: %+v", s.syms["A"])
	}
	if !s.syms["A"].evaluated || !sameRef(e.lastPub()["A"], before) || len(e.published()) != n {
		t.Fatal("a failed resync must not disturb the published verdict")
	}
	if got := s.syms["A"].due.Sub(due); got != 5*time.Second {
		t.Fatalf("retry in %s, want the 5 s first back-off", got)
	}
	fail = false
	e.step(s, due.Add(5*time.Second))
	if s.syms["A"].fails != 0 {
		t.Fatal("the retry must succeed and reset the failure count")
	}
}

func TestFirstResyncsOfSymbolsAdmittedTogetherAreSpread(t *testing.T) {
	e := newEnv(t)
	t0 := e.clock()
	e.setBars(recentBars(t0))
	var cands []core.PmVolEntry
	for i := 0; i < MaxCandidates; i++ {
		cands = append(cands, core.PmVolEntry{Sym: "S" + string(rune('A'+i/26)) + string(rune('A'+i%26)), PmVolX: 2})
	}
	e.setCands(cands...)
	s := e.service(defaultTuning())
	// Load all 80: many dispatch waves, all at the same fake instant.
	e.step(s, t0)
	if got := s.stats(); got.queued != 0 || got.candidates != MaxCandidates {
		t.Fatalf("not all loaded: %+v", got)
	}
	distinct := map[time.Duration]bool{}
	for _, st := range s.syms {
		d := st.due.Sub(t0)
		if d < ResyncEvery/2 || d >= ResyncEvery {
			t.Fatalf("%s first resync in %s, outside [%s, %s)", st.sym, d, ResyncEvery/2, ResyncEvery)
		}
		distinct[d/time.Second] = true
	}
	if len(distinct) < 40 {
		t.Fatalf("only %d distinct resync seconds among %d symbols: the REST calls would bunch", len(distinct), MaxCandidates)
	}
}

func TestASymbolWithNoBarTodayHasNoVerdictAndMidnightRetiresOne(t *testing.T) {
	e := newEnv(t)
	t0 := et(8, 50, 0)
	e.setNow(t0)
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2})
	// REST has only yesterday's bars (the name has not printed today as far as
	// REST knows yet): the engine would report YESTERDAY's final state.
	yesterday := et(15, 0, 0).AddDate(0, 0, -1)
	e.setBars(func(context.Context, string) ([]core.Bar, error) { return flatBars(yesterday, 40, 10), nil })
	s := e.service(defaultTuning())
	e.step(s, t0)
	if len(e.published()) != 0 {
		t.Fatalf("a candidate with no bar today must not be published: %v", e.lastPub())
	}
	if s.stats().evaluated != 0 {
		t.Fatal("evaluated count must be 0")
	}

	// Its first live second today: now it has a verdict.
	m := t0.UnixMilli() / minuteMs
	s.handleAgg(sec("A", m, 5, 10, 10.1, 9.9, 10.05, 100), t0.Add(5*time.Second))
	e.step(s, t0.Add(6*time.Second))
	if _, ok := e.lastPub()["A"]; !ok {
		t.Fatal("A must be published once it has a bar today")
	}

	// Midnight passes with no further bar: the verdict is retired (the engine
	// would otherwise keep reporting a day that is over, with a growing age).
	midnight := time.Date(2026, 10, 6, 0, 0, 30, 0, etLocation())
	e.step(s, midnight)
	if m := e.lastPub(); m == nil || len(m) != 0 {
		t.Fatalf("after midnight the stale verdict must leave the map, got %v", m)
	}
}

func TestALastBarThatClosesWithNoNewDataIsReEvaluatedOnTheClock(t *testing.T) {
	// BEAT cut at 243 bars (the last bar is 08:59): the golden file says the
	// TS engine calls it IMPULSE while that bar forms and READY once it has
	// closed (the FLAT bar opened inside the 4% band). No second ever arrives
	// for 09:00, so only the clock can flip it - exactly the "last bar just
	// closed" re-evaluation.
	g := loadGolden(t)
	fx := loadFixtures(t)
	var forming, closed *goldenReplay
	for i := range g.Replays {
		r := &g.Replays[i]
		if r.Fixture == "beat-2026-10-05" && r.ParamSet == "defaults" && r.Cut == 243 {
			if r.Clock == "forming" {
				forming = r
			} else {
				closed = r
			}
		}
	}
	if forming == nil || closed == nil || forming.State != "impulse" || closed.State != "ready" {
		t.Fatalf("golden replays for BEAT cut 243: forming %+v closed %+v", forming, closed)
	}
	bars := fx["beat-2026-10-05"][:243]
	last := time.UnixMilli(bars[len(bars)-1].T)

	e := newEnv(t)
	e.setNow(last.Add(30 * time.Second))
	e.setCands(core.PmVolEntry{Sym: "BEAT", PmVolX: 3})
	e.setBars(func(context.Context, string) ([]core.Bar, error) { return bars, nil })
	s := e.service(defaultTuning())

	// 30 s into the minute after... the bar is still forming (it began 30 s ago).
	e.step(s, last.Add(30*time.Second+CloseGrace))
	checkRef(t, "forming", e.lastPub()["BEAT"], forming.goldenLevels)
	n := len(e.published())

	// One minute after the bar began, but inside the grace: still forming, nothing to say.
	e.step(s, last.Add(time.Minute+CloseGrace-time.Second))
	if len(e.published()) != n {
		t.Fatal("published before the bar's grace period ended")
	}
	// Grace over: the bar is closed, the verdict flips - with no new data at all.
	e.step(s, last.Add(time.Minute+CloseGrace))
	checkRef(t, "closed", e.lastPub()["BEAT"], closed.goldenLevels)
	if len(e.published()) != n+1 {
		t.Fatalf("expected exactly one publish for the close, got %d", len(e.published())-n)
	}
	// And then it is stable: unchanged bars, unchanged verdict, no more publishes.
	for i := 1; i <= 30; i++ {
		e.step(s, last.Add(time.Minute+CloseGrace).Add(time.Duration(i)*time.Second))
	}
	if len(e.published()) != n+1 {
		t.Fatalf("%d redundant publishes", len(e.published())-n-1)
	}
}

func TestStatusLineCarriesTheCountsAndTapDrops(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)

	e := newEnv(t)
	t0 := e.clock()
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2}, core.PmVolEntry{Sym: "B", PmVolX: 1.5})
	pb := newParkedBars()
	e.setBars(func(ctx context.Context, sym string) ([]core.Bar, error) {
		if sym == "B" { // B stays in the backfill queue
			return pb.fn(ctx, sym)
		}
		return flatBars(t0, 30, 10), nil
	})
	s := e.service(defaultTuning())
	s.tick(t0) // the first status line (nothing loaded yet)
	s.handleCandidates(<-s.candCh, t0)
	r := <-s.results // A's result (B is parked)
	s.handleFetch(r, t0)
	s.evaluate(t0)

	buf.Reset()
	e.drops.Store(7)
	s.nextLog = time.Time{}
	s.tick(t0.Add(time.Second))
	line := buf.String()
	for _, want := range []string{"msg=flagscan ", "candidates=2", "evaluated=1", "idle=1", "backfillQueue=1", "inflight=1", "tapDrops=7", "tapDropsSinceLast=7"} {
		if !strings.Contains(line, want) {
			t.Fatalf("status line lacks %q:\n%s", want, line)
		}
	}
	if !strings.Contains(line, "level=WARN") {
		t.Fatalf("a tap that dropped aggregates must be a warning:\n%s", line)
	}
	// A minute later with two more drops: the delta is reported; with none, no warning.
	buf.Reset()
	e.drops.Store(9)
	s.tick(t0.Add(70 * time.Second))
	if !strings.Contains(buf.String(), "tapDropsSinceLast=2") {
		t.Fatalf("delta missing:\n%s", buf.String())
	}
	buf.Reset()
	s.tick(t0.Add(140 * time.Second))
	if strings.Contains(buf.String(), "level=WARN") || !strings.Contains(buf.String(), "tapDropsSinceLast=0") {
		t.Fatalf("no new drops, no warning:\n%s", buf.String())
	}
	// Exactly one status line per minute: ticks inside the minute add none.
	buf.Reset()
	for i := 1; i <= 50; i++ {
		s.tick(t0.Add(140 * time.Second).Add(time.Duration(i) * time.Second))
	}
	if n := strings.Count(buf.String(), "msg=flagscan "); n != 0 {
		t.Fatalf("%d status lines inside one minute", n)
	}
}

func TestEvaluationOnlyRunsForSymbolsWhoseBarsChanged(t *testing.T) {
	e := newEnv(t)
	t0 := et(9, 0, 10)
	e.setNow(t0)
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2})
	e.setBars(recentBars(t0))
	s := e.service(defaultTuning())
	e.step(s, t0)
	if !s.syms["A"].evaluated || s.syms["A"].dirty {
		t.Fatalf("loaded and evaluated: %+v", s.syms["A"])
	}
	n := len(e.published())

	// A refused or stale second changes nothing and so evaluates nothing.
	s.handleAgg(sec("A", t0.UnixMilli()/minuteMs-5, 0, 10, 10, 10, 10, 1), t0) // an old minute
	s.handleAgg(core.Agg{Sym: "A", Open: 0, High: 1, Low: 1, Close: 1, StartMs: t0.UnixMilli()}, t0)
	s.handleAgg(sec("ZZZ", t0.UnixMilli()/minuteMs, 0, 10, 10, 10, 10, 1), t0) // not a candidate
	if s.syms["A"].dirty {
		t.Fatal("an ignored aggregate must not mark the symbol dirty")
	}
	e.step(s, t0.Add(time.Second))
	if len(e.published()) != n {
		t.Fatal("republished without a change")
	}
	// A real one does.
	s.handleAgg(sec("A", t0.UnixMilli()/minuteMs, 11, 10.5, 10.6, 10.4, 10.55, 10), t0.Add(2*time.Second))
	if !s.syms["A"].dirty {
		t.Fatal("a folded aggregate must mark the symbol dirty")
	}
}

// A symbol the engine panics on has no verdict "until its bars next change"
// (evalOne's contract) - and the clock is not a change. Once its last bar
// closes it gets ONE more try (the closed-bar rules are a different code path
// from the forming-bar pass that just failed), after which it must stay quiet:
// without that, a bad symbol would re-run the engine and write an Error line
// every second for as long as its bars stand still.
func TestASymbolWhoseEvaluationPanickedIsNotRetriedEverySecondOnceItsBarCloses(t *testing.T) {
	e := newEnv(t)
	t0 := et(8, 50, 20)
	e.setNow(t0)
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2})
	m := t0.UnixMilli() / minuteMs
	// REST serves a snapshot of the forming 08:50 minute: that is the last bar.
	rest := append(flatBars(et(8, 50, 0), 30, 10), core.Bar{T: m * minuteMs, O: 10, H: 10.1, L: 9.9, C: 10.05, V: 50})
	e.setBars(func(context.Context, string) ([]core.Bar, error) { return rest, nil })
	s := e.service(defaultTuning())
	calls := 0
	s.eval = func([]core.Bar, int64, int64, emaflag.Params) emaflag.Result {
		calls++
		panic("boom")
	}

	e.step(s, t0) // admitted, loaded, evaluated while 08:50 is still forming: the first panic
	if calls != 1 || !s.syms["A"].broken || s.syms["A"].evaluated {
		t.Fatalf("calls %d, state %+v: want one failed evaluation and a broken symbol", calls, s.syms["A"])
	}
	// The rest of the minute (the engine sees now - CloseGrace): the bars did not
	// change and the bar is still forming, so nothing runs.
	for i := 1; i <= 36; i++ {
		e.step(s, t0.Add(time.Duration(i)*time.Second))
	}
	if calls != 1 {
		t.Fatalf("%d engine calls while the bars stood still and the bar was forming, want 1", calls)
	}

	// 08:51:00 + CloseGrace: the bar has closed - the one retry the close earns.
	closeAt := et(8, 51, 0).Add(CloseGrace)
	e.step(s, closeAt)
	if calls != 2 {
		t.Fatalf("%d engine calls when the bar closed, want 2 (one retry)", calls)
	}
	for i := 1; i <= 20; i++ {
		e.step(s, closeAt.Add(time.Duration(i)*time.Second))
	}
	if calls != 2 {
		t.Fatalf("%d engine calls after the bar closed with the bars unchanged, want 2: a broken symbol must not be re-run every second", calls)
	}
	if _, ok := e.lastPub()["A"]; ok || len(e.published()) != 0 {
		t.Fatalf("a symbol the engine panics on must publish nothing: %v", e.published())
	}

	// New bars are a change: it is tried again (and fails again, quietly).
	s.handleAgg(sec("A", m+1, 5, 10.1, 10.2, 10.0, 10.15, 5), closeAt.Add(21*time.Second))
	e.step(s, closeAt.Add(22*time.Second))
	if calls != 3 {
		t.Fatalf("%d engine calls after the bars changed, want 3", calls)
	}
}
