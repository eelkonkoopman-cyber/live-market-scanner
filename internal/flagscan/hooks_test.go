package flagscan

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/emaflag"
)

// The two hooks the auto-trader reads the scan through: Config.OnEval (every
// evaluation, with the Primed rule) and Config.Pinned (symbols that stay
// candidates whatever the rank or the cap). Like the rest of the package these
// run against fakes: no network, no engine, no broker.

// ─── helpers ─────────────────────────────────────────────────────────────

// collector records OnEval updates the way the trader's channel would, but
// without ever dropping one.
type collector struct {
	mu   sync.Mutex
	list []EvalUpdate
}

func (c *collector) add(u EvalUpdate) {
	c.mu.Lock()
	c.list = append(c.list, u)
	c.mu.Unlock()
}

// take returns what was recorded since the last take.
func (c *collector) take() []EvalUpdate {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.list
	c.list = nil
	return out
}

// primedOf is the Primed flag of every update for sym, in order.
func primedOf(ups []EvalUpdate, sym string) []bool {
	var out []bool
	for _, u := range ups {
		if u.Sym == sym {
			out = append(out, u.Primed)
		}
	}
	return out
}

func symsOf(ups []EvalUpdate) []string {
	out := make([]string, 0, len(ups))
	for _, u := range ups {
		out = append(out, u.Sym)
	}
	return out
}

// pinSet is a Pinned callback whose answer the test changes between ticks, and
// which counts how often it is asked.
type pinSet struct {
	mu    sync.Mutex
	syms  []string
	calls int
}

func (p *pinSet) set(s ...string) {
	p.mu.Lock()
	p.syms = append([]string(nil), s...)
	p.mu.Unlock()
}

func (p *pinSet) get() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return append([]string(nil), p.syms...)
}

// serviceCfg is env.service with a Config the test has already edited (OnEval,
// Pinned, ...). The cleanup cancels and waits for every fetch goroutine.
func (e *env) serviceCfg(cfg Config, tm tuning) *service {
	e.t.Helper()
	s, err := newService(cfg, tm)
	if err != nil {
		e.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx
	e.t.Cleanup(func() {
		cancel()
		s.wg.Wait()
	})
	return s
}

// captureLog routes the package's log lines into a buffer for one test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// ─── OnEval: what an update is ───────────────────────────────────────────

func TestOnEvalIsSilentUntilTheBackfillLoadsThenTheFirstEvaluationIsTheUnprimedReplay(t *testing.T) {
	e := newEnv(t)
	now := et(8, 50, 20)
	e.setNow(now)
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2})
	m := now.UnixMilli() / minuteMs

	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	hist := flatBars(now, 30, 10) // 08:20 .. 08:49
	e.setBars(func(ctx context.Context, sym string) ([]core.Bar, error) {
		started <- struct{}{}
		select {
		case <-gate:
			return hist, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	var ups collector
	cfg := e.config()
	cfg.OnEval = ups.add
	s := e.serviceCfg(cfg, defaultTuning())

	s.tick(now)
	s.handleCandidates(<-s.candCh, now) // admitted, REST call parked
	<-started
	// Seconds that arrive while REST is in flight fold into the live-only series;
	// an unloaded symbol is never evaluated, so there is nothing to report.
	s.handleAgg(sec("A", m, 22, 10.00, 10.10, 9.95, 10.05, 40), now.Add(time.Second))
	s.evaluate(now.Add(2 * time.Second))
	if got := ups.take(); len(got) != 0 {
		t.Fatalf("OnEval was called %d times before the REST history loaded: %+v", len(got), got)
	}

	// The load: the first evaluation replays the whole history -> unprimed.
	close(gate)
	later := now.Add(10 * time.Second)
	s.handleFetch(<-s.results, later)
	s.evaluate(later)
	got := ups.take()
	if len(got) != 1 {
		t.Fatalf("%d updates after the load, want exactly one", len(got))
	}
	u := got[0]
	if u.Sym != "A" || u.Primed {
		t.Fatalf("first evaluation after the backfill: %+v, want sym A and Primed=false (it replays history)", u)
	}
	if u.NowMs != later.UnixMilli() {
		t.Fatalf("NowMs %d, want the service clock %d (not reduced by CloseGrace)", u.NowMs, later.UnixMilli())
	}
	if want := (core.Bar{T: m * minuteMs, O: 10.00, H: 10.10, L: 9.95, C: 10.05, V: 40}); u.Last != want {
		t.Fatalf("Last %+v, want the forming 08:50 bar %+v", u.Last, want)
	}
	if pub, ok := e.lastPub()["A"]; !ok || !sameRef(refOf(u.Result), pub) {
		t.Fatalf("the update's Result must be the very verdict that was published: %+v vs %+v", refOf(u.Result), pub)
	}

	// A live second: the next evaluation is primed, and Last follows the forming bar.
	s.handleAgg(sec("A", m, 25, 10.05, 10.30, 10.00, 10.20, 10), later.Add(time.Second))
	s.evaluate(later.Add(time.Second))
	got = ups.take()
	if len(got) != 1 || !got[0].Primed || got[0].Sym != "A" {
		t.Fatalf("after a live second: %+v, want one primed update", got)
	}
	if want := (core.Bar{T: m * minuteMs, O: 10.00, H: 10.30, L: 9.95, C: 10.20, V: 50}); got[0].Last != want {
		t.Fatalf("Last %+v, want the folded forming bar %+v", got[0].Last, want)
	}

	// Nothing changed and the bar is still forming: no evaluation, no update.
	s.evaluate(later.Add(2 * time.Second))
	if got := ups.take(); len(got) != 0 {
		t.Fatalf("an update with no evaluation behind it: %+v", got)
	}
	// The clock alone closes the bar (08:51:00 + CloseGrace): one more evaluation,
	// even though no data arrived and the verdict may not change. Same bar, closed.
	s.evaluate(et(8, 51, 0).Add(CloseGrace))
	got = ups.take()
	if len(got) != 1 || !got[0].Primed || got[0].Last.T != m*minuteMs || got[0].Last.C != 10.20 {
		t.Fatalf("the clock-only re-evaluation of the closed bar: %+v", got)
	}
	s.evaluate(et(8, 51, 30))
	if got := ups.take(); len(got) != 0 {
		t.Fatalf("a closed, unchanged bar must not be re-reported: %+v", got)
	}
}

func TestPrimingIsPerSymbolAndPerAdmission(t *testing.T) {
	e := newEnv(t)
	t0 := et(8, 50, 20) // mid-minute: the history's last bar (08:49) is already closed at the first evaluation
	e.setNow(t0)
	e.setBars(recentBars(t0))
	var ups collector
	cfg := e.config()
	cfg.OnEval = ups.add
	s := e.serviceCfg(cfg, defaultTuning())
	m := t0.UnixMilli() / minuteMs

	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2})
	e.step(s, t0)
	s.handleAgg(sec("A", m, 5, 10, 10.1, 9.9, 10.05, 10), t0.Add(5*time.Second))

	// B arrives while A is already running: B's first evaluation is its own
	// replay (unprimed) while A's next one is primed.
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2}, core.PmVolEntry{Sym: "B", PmVolX: 1.5})
	s.handleAgg(sec("A", m, 40, 10, 10.2, 9.9, 10.1, 10), t0.Add(31*time.Second))
	e.step(s, t0.Add(31*time.Second))
	got := ups.take()
	if a, b := primedOf(got, "A"), primedOf(got, "B"); !slices.Equal(a, []bool{false, true}) || !slices.Equal(b, []bool{false}) {
		t.Fatalf("A %v, B %v: want A [false true] (one replay, then live) and B [false] (its own replay)", a, b)
	}

	// A leaves the candidate set and comes back: a new admission, a new replay.
	e.setCands(core.PmVolEntry{Sym: "B", PmVolX: 1.5})
	e.step(s, t0.Add(62*time.Second))
	ups.take() // (A's 08:50 bar closed in that pass, before the refresh dropped it: one more primed update, not under test)
	if _, ok := s.syms["A"]; ok {
		t.Fatal("A must have been dropped")
	}
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2}, core.PmVolEntry{Sym: "B", PmVolX: 1.5})
	e.step(s, t0.Add(93*time.Second))
	got = ups.take()
	if a := primedOf(got, "A"); !slices.Equal(a, []bool{false}) {
		t.Fatalf("a re-admitted symbol must replay its history unprimed again, got %v", a)
	}
	if b := primedOf(got, "B"); len(b) != 0 {
		t.Fatalf("B did not change and must not be re-reported: %v", b)
	}
}

func TestAnEngineFailureIsNoUpdateAndDoesNotUsePrimingUp(t *testing.T) {
	e := newEnv(t)
	t0 := et(8, 50, 20)
	e.setNow(t0)
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2})
	m := t0.UnixMilli() / minuteMs
	rest := append(flatBars(et(8, 50, 0), 30, 10), core.Bar{T: m * minuteMs, O: 10, H: 10.1, L: 9.9, C: 10.05, V: 50})
	e.setBars(func(context.Context, string) ([]core.Bar, error) { return rest, nil })
	var ups collector
	cfg := e.config()
	cfg.OnEval = ups.add
	s := e.serviceCfg(cfg, defaultTuning())
	real := s.eval
	fail := true
	s.eval = func(b []core.Bar, tf, now int64, p emaflag.Params) emaflag.Result {
		if fail {
			panic("boom")
		}
		return real(b, tf, now, p)
	}

	e.step(s, t0) // the first evaluation panics: no verdict, so no update either
	if got := ups.take(); len(got) != 0 {
		t.Fatalf("an update for an evaluation that produced no verdict: %+v", got)
	}
	fail = false
	s.handleAgg(sec("A", m, 30, 10.05, 10.2, 10.0, 10.1, 5), t0.Add(10*time.Second))
	e.step(s, t0.Add(11*time.Second))
	got := ups.take()
	if len(got) != 1 || got[0].Primed {
		t.Fatalf("the first evaluation that WORKS is still the history replay: %+v", got)
	}
}

// Two symbols that fall due in the same tick are delivered strongest pmVolX
// first, then by symbol - the same way every time, not in map order: a trader
// with one free position slot must give it to the same name for the same input.
func TestOnEvalDeliversTheSymbolsOfOneTickStrongestFirst(t *testing.T) {
	want := []string{"B", "D", "C", "A", "E"}
	for round := 0; round < 25; round++ {
		e := newEnv(t)
		t0 := e.clock()
		e.setCands(
			core.PmVolEntry{Sym: "A", PmVolX: 1.5}, core.PmVolEntry{Sym: "B", PmVolX: 3}, core.PmVolEntry{Sym: "C", PmVolX: 2},
			core.PmVolEntry{Sym: "D", PmVolX: 3}, core.PmVolEntry{Sym: "E", PmVolX: 1.1})
		e.setBars(recentBars(t0))
		var ups collector
		cfg := e.config()
		cfg.OnEval = ups.add
		s := e.serviceCfg(cfg, defaultTuning())
		e.step(s, t0) // every symbol loads and is evaluated in the same pass
		if got := symsOf(ups.take()); !slices.Equal(got, want) {
			t.Fatalf("round %d: delivery order %v, want %v", round, got, want)
		}
	}
}

func TestAPanickingOnEvalCostsOneUpdateNeverTheVerdictsOrTheOtherSymbols(t *testing.T) {
	buf := captureLog(t)
	e := newEnv(t)
	t0 := et(8, 50, 20)
	e.setNow(t0)
	m := t0.UnixMilli() / minuteMs
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 3}, core.PmVolEntry{Sym: "B", PmVolX: 2})
	e.setBars(recentBars(t0))
	var ups collector
	var calls int
	cfg := e.config()
	cfg.OnEval = func(u EvalUpdate) {
		calls++
		if u.Sym == "A" { // A is delivered first: B must still get its turn
			panic("consumer bug")
		}
		ups.add(u)
	}
	s := e.serviceCfg(cfg, defaultTuning())

	e.step(s, t0)
	if calls != 2 || !slices.Equal(symsOf(ups.take()), []string{"B"}) {
		t.Fatalf("%d callback calls; B must be delivered although A's callback panicked", calls)
	}
	if got := keysOf(e.lastPub()); !sameStrings(got, []string{"A", "B"}) {
		t.Fatalf("a panicking callback must not cost a verdict, published %v", got)
	}
	// ... and the loop is fine on the next change (the panic is logged once, not per call).
	s.handleAgg(sec("A", m, 30, 10, 10.1, 9.9, 10.05, 5), t0.Add(30*time.Second))
	e.step(s, t0.Add(31*time.Second))
	if calls != 3 || s.evalPanics != 2 {
		t.Fatalf("calls %d panics %d, want 3 and 2", calls, s.evalPanics)
	}
	if n := strings.Count(buf.String(), "Config.OnEval panicked"); n != 1 {
		t.Fatalf("the panic was logged %d times, want once (then every 100th):\n%s", n, buf.String())
	}
}

// ─── OnEval: through the live loop ───────────────────────────────────────

// The documented consumer keeps up with the loop, not the other way round: it
// hands each update to a buffered channel with a NON-BLOCKING send and counts
// what it drops. A consumer that never reads at all must cost the service
// nothing: the tap keeps draining (an unbuffered tap, every send a rendezvous),
// verdicts keep being evaluated and published, and the loop stops on cancel.
func TestOnEvalWithAConsumerThatNeverReadsNeverBlocksTheService(t *testing.T) {
	before := runtime.NumGoroutine()
	e := newEnv(t)
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 3})
	e.setBars(func(ctx context.Context, sym string) ([]core.Bar, error) { return flatBars(e.clock(), 30, 10), nil })

	trader := make(chan EvalUpdate, 3) // the trader's buffer; nobody drains it
	var drops, calls, pubs atomic.Int64
	aggs := make(chan core.Agg) // UNBUFFERED: every send is a hand-off to the loop
	cfg := e.config()
	cfg.Aggs = aggs
	cfg.Now = advancingClock(10 * time.Millisecond)
	cfg.OnEval = func(u EvalUpdate) {
		calls.Add(1)
		select {
		case trader <- u:
		default:
			drops.Add(1)
		}
	}
	cfg.Publish = func(m map[string]core.EmaFlagRef) {
		pubs.Add(1)
		e.publish(m)
	}
	s, err := newService(cfg, fastTuning())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := startRun(t, s, ctx)

	waitFor(t, "A's first update", func() bool { return calls.Load() >= 1 })
	m := e.clock().UnixMilli() / minuteMs
	for i := 0; drops.Load() < 25; i++ {
		if i > 20_000 {
			t.Fatalf("only %d drops after %d aggregates: the loop is not evaluating", drops.Load(), i)
		}
		p := 10 + float64(i%50)*0.01
		select {
		case aggs <- sec("A", m, int64(i%60), p, p+0.02, p-0.02, p+0.01, 1):
		case <-time.After(2 * time.Second):
			t.Fatalf("the loop stopped draining the tap after %d aggregates (a stalled consumer blocked it)", i)
		}
		time.Sleep(500 * time.Microsecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return although its consumer never read")
	}
	goroutinesSettle(t, before)

	// The loop has stopped: the books must balance exactly.
	if len(trader) != cap(trader) {
		t.Fatalf("the consumer's buffer holds %d, want it full (%d)", len(trader), cap(trader))
	}
	if got := calls.Load(); got != int64(len(trader))+drops.Load() {
		t.Fatalf("%d callbacks, %d buffered + %d dropped: every one must be one or the other", got, len(trader), drops.Load())
	}
	// The buffer kept the OLDEST updates: A's replay first, then its live ones.
	first := <-trader
	if first.Primed || first.Sym != "A" {
		t.Fatalf("the oldest buffered update: %+v, want A's unprimed replay", first)
	}
	if second := <-trader; !second.Primed {
		t.Fatalf("the next update after the replay must be primed: %+v", second)
	}
	if pubs.Load() == 0 {
		t.Fatal("nothing was published")
	}
}

// ─── BEAT through the whole chain: history is never traded ───────────────

type beatRun struct {
	start      time.Time
	bars       []core.Bar // the fixture
	ups        []EvalUpdate
	pubs       int
	aggSecs    map[int64]bool // fake seconds in which aggregates were delivered
	resyncSecs map[int64]bool // fake seconds in which REST was called
}

// replayBeat admits BEAT at start (a whole minute) and runs the real service
// second by second to two minutes past the fixture's last bar, exactly like
// TestReplayBeatSecondBySecondMatchesGolden, collecting every OnEval update.
func replayBeat(t *testing.T, start time.Time) beatRun {
	t.Helper()
	if start.Second() != 0 || start.Nanosecond() != 0 {
		t.Fatalf("start %v is not a whole minute: the aggregates before it would be lost", start)
	}
	const sym = "BEAT"
	bars := loadFixtures(t)["beat-2026-10-05"]
	e := newEnv(t)
	e.setCands(core.PmVolEntry{Sym: sym, PmVolX: 1.7})
	e.setBars(func(context.Context, string) ([]core.Bar, error) {
		minute := e.clock().UnixMilli() / minuteMs * minuteMs
		n := 0
		for n < len(bars) && bars[n].T < minute {
			n++
		}
		return bars[:n], nil
	})
	var ups collector
	cfg := e.config()
	cfg.OnEval = ups.add
	s := e.serviceCfg(cfg, defaultTuning())

	admMinute := start.UnixMilli() / minuteMs * minuteMs
	due := map[int64][]core.Agg{}
	for _, b := range bars {
		if b.T < admMinute {
			continue
		}
		for _, a := range subAggs(sym, b) {
			due[a.StartMs/1000] = append(due[a.StartMs/1000], a)
		}
	}
	run := beatRun{start: start, bars: bars, aggSecs: map[int64]bool{}, resyncSecs: map[int64]bool{}}
	end := time.UnixMilli(bars[len(bars)-1].T).Add(2 * time.Minute)
	for now := start; !now.After(end); now = now.Add(time.Second) {
		e.setNow(now)
		restBefore := e.countLog("bars:")
		s.tick(now)
		e.settle(s, now)
		if e.countLog("bars:") != restBefore {
			run.resyncSecs[now.Unix()] = true
		}
		for _, a := range due[now.Unix()] {
			s.handleAgg(a, now)
			run.aggSecs[now.Unix()] = true
		}
		s.evaluate(now)
	}
	run.ups = ups.take()
	run.pubs = len(e.published())
	return run
}

// consumer is the rule the EvalUpdate doc gives a trader: ignore !Primed, mark
// the events of the first primed update as seen without acting, then act on
// each NEW entry within two minutes. naive does none of the first two - the
// consumer the Primed flag exists to keep from buying history.
type consumer struct {
	naive      bool
	primedOnce bool
	seen       map[string]bool
	acted      []int64 // ts of the entry events acted on
	actedAt    []int64 // NowMs of the update it acted on
}

func evKey(ev emaflag.Event) string { return fmt.Sprintf("%d|%s|%s", ev.Ts, ev.Kind, ev.Label) }

func (c *consumer) feed(u EvalUpdate) {
	if c.seen == nil {
		c.seen = map[string]bool{}
	}
	if !c.naive {
		if !u.Primed {
			return
		}
		if !c.primedOnce {
			c.primedOnce = true
			for _, ev := range u.Result.Events {
				c.seen[evKey(ev)] = true
			}
			return
		}
	}
	for _, ev := range u.Result.Events {
		k := evKey(ev)
		if c.seen[k] {
			continue
		}
		c.seen[k] = true
		if ev.Kind == emaflag.KindEntry && ev.Label == emaflag.LabelEntry && ev.Ts >= u.NowMs-120_000 {
			c.acted = append(c.acted, ev.Ts)
			c.actedAt = append(c.actedAt, u.NowMs)
		}
	}
}

func hasEntry(r emaflag.Result, ts int64) bool {
	for _, ev := range r.Events {
		if ev.Kind == emaflag.KindEntry && ev.Label == emaflag.LabelEntry && ev.Ts == ts {
			return true
		}
	}
	return false
}

func countFalse(ups []EvalUpdate) int {
	n := 0
	for _, u := range ups {
		if !u.Primed {
			n++
		}
	}
	return n
}

func TestPrimingKeepsAFlagThatAlreadyBrokenFromBeingTradedAndLetsALiveEntryThrough(t *testing.T) {
	g := loadGolden(t)
	_, entryTs, exitTs, _ := beatAnchors(t, g)
	if entryTs == 0 || exitTs == 0 {
		t.Fatalf("golden BEAT has no entry/exit: %d %d", entryTs, exitTs)
	}
	entryAt := time.UnixMilli(entryTs)

	t.Run("admitted while the flag is still forming: the live entry gets through", func(t *testing.T) {
		run := replayBeat(t, et(8, 50, 0))
		if len(run.ups) == 0 || run.ups[0].Primed || countFalse(run.ups) != 1 {
			t.Fatalf("want exactly one unprimed update, the first; got %d unprimed of %d", countFalse(run.ups), len(run.ups))
		}
		var c consumer
		for _, u := range run.ups {
			c.feed(u)
		}
		if !slices.Equal(c.acted, []int64{entryTs}) {
			t.Fatalf("the consumer acted on entries %v, want exactly BEAT's live 09:00 entry %d", c.acted, entryTs)
		}
		// ... during the entry bar's own minute: the break is judged on the HIGH.
		if at := time.UnixMilli(c.actedAt[0]); at.Before(entryAt) || !at.Before(entryAt.Add(time.Minute)) {
			t.Fatalf("acted at %s, want inside the entry minute %s", at.In(etLocation()).Format("15:04:05"), entryAt.In(etLocation()).Format("15:04"))
		}
	})

	t.Run("admitted a minute after the entry: the replayed entry is history", func(t *testing.T) {
		start := entryAt.Add(time.Minute)
		if !start.Before(time.UnixMilli(exitTs)) {
			t.Fatalf("test setup: %s is not before the exit", start)
		}
		run := replayBeat(t, start)
		first := run.ups[0]
		if first.Primed || countFalse(run.ups) != 1 {
			t.Fatalf("want exactly one unprimed update, the first; got %d unprimed", countFalse(run.ups))
		}
		if !hasEntry(first.Result, entryTs) || first.Result.State != emaflag.StateLong {
			t.Fatalf("the replay must carry the entry the flag already made: state %v, events %+v", first.Result.State, first.Result.Events)
		}
		// Within the consumer's own two-minute recency window - the dangerous case.
		if first.NowMs-entryTs > 120_000 {
			t.Fatalf("test setup: the entry is %d ms old at admission, outside the recency window", first.NowMs-entryTs)
		}
		var contract, naive consumer
		naive.naive = true
		cursed := false
		for _, u := range run.ups {
			contract.feed(u)
			naive.feed(u)
			if u.Primed && hasEntry(u.Result, entryTs) {
				cursed = true
			}
		}
		if len(naive.acted) != 1 {
			t.Fatalf("the naive consumer acted %d times; this setup must be one that WOULD buy history (else the test proves nothing)", len(naive.acted))
		}
		if len(contract.acted) != 0 {
			t.Fatalf("the Primed-respecting consumer acted on %v: it traded history", contract.acted)
		}
		if !cursed {
			t.Fatal("the entry must stay in the cumulative Result.Events of later primed updates (the doc promises it; the seen-set exists for it)")
		}
	})
}

// OnEval is an evaluation hook, not a heartbeat: BEAT's replay evaluates when
// aggregates arrive, when the last bar closes and when a REST resync lands -
// and at no other second. A bar closing or a resync can leave the published
// verdict exactly as it was; those evaluations are reported all the same.
func TestOnEvalFiresOnEvaluationsNotOnEveryTick(t *testing.T) {
	run := replayBeat(t, et(8, 50, 0))
	allowed := map[int64]bool{run.start.Unix(): true}
	for at := range run.aggSecs {
		allowed[at] = true
	}
	for at := range run.resyncSecs {
		allowed[at] = true
	}
	for _, b := range run.bars {
		allowed[b.T/1000+60+int64(CloseGrace/time.Second)] = true // the bar closes
	}
	perSecond := map[int64]int{}
	for _, u := range run.ups {
		at := u.NowMs / 1000
		if !allowed[at] {
			t.Fatalf("an update at %s: no aggregate, bar close or resync happened then", time.Unix(at, 0).In(etLocation()).Format("15:04:05"))
		}
		if perSecond[at]++; perSecond[at] > 2 { // the tick's own pass, then the one after the aggregates
			t.Fatalf("%d updates in one second", perSecond[at])
		}
	}
	if len(run.ups) <= run.pubs {
		t.Fatalf("%d updates for %d publishes: OnEval reports evaluations whose verdict did not change, publish only changes", len(run.ups), run.pubs)
	}
	t.Logf("%d updates, %d publishes over %d aggregate seconds", len(run.ups), run.pubs, len(run.aggSecs))
}

// ─── Pinned ──────────────────────────────────────────────────────────────

func TestPinnedSymbolsSurviveRankAndCapChurnAndStayInTheTap(t *testing.T) {
	e := newEnv(t)
	t0 := et(8, 50, 20)
	e.setNow(t0)
	m := t0.UnixMilli() / minuteMs
	e.setBars(recentBars(t0))
	var (
		listMu sync.Mutex
		list   = []core.PmVolEntry{{Sym: "A", PmVolX: 5}, {Sym: "B", PmVolX: 4}, {Sym: "C", PmVolX: 3}, {Sym: "D", PmVolX: 2}, {Sym: "E", PmVolX: 1.5}}
		cands  atomic.Int32
	)
	setList := func(l ...core.PmVolEntry) { listMu.Lock(); list = l; listMu.Unlock() }
	e.candFn = func(minX float64, limit int) []core.PmVolEntry {
		cands.Add(1)
		listMu.Lock()
		defer listMu.Unlock()
		return append([]core.PmVolEntry(nil), list...)
	}
	pins := &pinSet{}
	pins.set("E") // the weakest of five, and the cap is 3: the cap alone would cut it
	var ups collector
	cfg := e.config()
	cfg.Pinned = pins.get
	cfg.OnEval = ups.add
	tm := defaultTuning()
	tm.maxCandidates = 3
	s := e.serviceCfg(cfg, tm)

	// 1. A pin on top of the cap: A B C by rank + E by pin; D is the one the cap cuts.
	e.step(s, t0)
	if got := keysOf(e.lastPub()); !sameStrings(got, []string{"A", "B", "C", "E"}) {
		t.Fatalf("evaluated %v, want the cap's A B C plus the pinned E", got)
	}
	if st := s.stats(); st.candidates != 4 || st.qualified != 5 || st.beyondCap != 1 || st.pinned != 1 {
		t.Fatalf("stats %+v: want 4 tracked, 5 qualified, 1 beyond the cap (D; E was saved by its pin), 1 pinned", st)
	}
	genE := s.syms["E"].gen
	if got := primedOf(ups.take(), "E"); !slices.Equal(got, []bool{false}) {
		t.Fatalf("E's first evaluation must be its unprimed replay, got %v", got)
	}

	// 2. Churn: E is no longer over the threshold at all (absent from the list)
	// and the ranking reshuffles. E stays - same state, no new backfill, still
	// primed - while C falls to the cap.
	setList(core.PmVolEntry{Sym: "D", PmVolX: 6}, core.PmVolEntry{Sym: "B", PmVolX: 5}, core.PmVolEntry{Sym: "A", PmVolX: 4}, core.PmVolEntry{Sym: "C", PmVolX: 3})
	e.step(s, t0.Add(31*time.Second))
	if got := keysOf(e.lastPub()); !sameStrings(got, []string{"A", "B", "D", "E"}) {
		t.Fatalf("after churn evaluated %v, want A B D (rank) + E (pin); C is cut by the cap", got)
	}
	if s.syms["E"].gen != genE || e.countLog("bars:E") != 1 {
		t.Fatalf("E was re-admitted (gen %d -> %d, %d REST calls): a pinned symbol must keep its state through churn", genE, s.syms["E"].gen, e.countLog("bars:E"))
	}
	if st := s.stats(); st.qualified != 4 || st.beyondCap != 1 {
		t.Fatalf("stats %+v: want 4 qualified, C beyond the cap", st)
	}
	s.handleAgg(sec("E", m, 10, 10, 10.1, 9.9, 10.05, 5), t0.Add(32*time.Second))
	e.step(s, t0.Add(32*time.Second))
	if got := primedOf(ups.take(), "E"); !slices.Equal(got, []bool{true}) {
		t.Fatalf("E's data must keep flowing through the churn as PRIMED updates, got %v", got)
	}

	// 3. Unpinned: it leaves on the very next tick (no wait for the 30 s
	// refresh), because it is no candidate either - and no candidate query ran.
	asked := cands.Load()
	pins.set()
	e.step(s, t0.Add(33*time.Second))
	if _, ok := e.lastPub()["E"]; ok || s.syms["E"] != nil {
		t.Fatalf("an unpinned non-candidate must be dropped at once: published %v", keysOf(e.lastPub()))
	}
	if cands.Load() != asked {
		t.Fatal("releasing a pin must not cost an engine query")
	}

	// 4. A pin on a symbol nobody ever listed (a position adopted at reconnect):
	// admitted on the next tick, tap first, its own backfill, no refresh needed.
	pins.set("Z")
	e.step(s, t0.Add(34*time.Second))
	if _, ok := e.lastPub()["Z"]; !ok {
		t.Fatalf("a pinned non-candidate must be admitted and evaluated, published %v", keysOf(e.lastPub()))
	}
	if x := s.syms["Z"].pmVolX; x != 0 {
		t.Fatalf("a pinned symbol that is no candidate has multiple %v, want 0", x)
	}
	if cands.Load() != asked {
		t.Fatal("admitting a pin must not wait for (or cost) a candidate refresh")
	}
	log := e.logCopy()
	tapAt, barsAt := -1, -1
	for i, l := range log {
		if strings.HasPrefix(l, "tap:") && strings.Contains(l, "Z") && tapAt < 0 {
			tapAt = i
		}
		if l == "bars:Z" && barsAt < 0 {
			barsAt = i
		}
	}
	if tapAt < 0 || barsAt < 0 || tapAt > barsAt {
		t.Fatalf("Z's tap (%d) must precede its backfill (%d): %v", tapAt, barsAt, log)
	}
	if got := primedOf(ups.take(), "Z"); !slices.Equal(got, []bool{false}) {
		t.Fatalf("Z's first evaluation is a replay, got %v", got)
	}

	// 5. Pinned again after being released = a new admission (fresh replay).
	pins.set("Z", "E")
	e.step(s, t0.Add(35*time.Second))
	if got := primedOf(ups.take(), "E"); !slices.Equal(got, []bool{false}) || e.countLog("bars:E") != 2 {
		t.Fatalf("a re-pinned symbol is a new admission: primed %v, %d REST calls (want [false], 2)", got, e.countLog("bars:E"))
	}

	// Throughout, the tap never lost a pinned symbol while it was pinned.
	e.mu.Lock()
	defer e.mu.Unlock()
	want := [][]string{
		{"E"},                // the pin alone, before the first candidate answer
		{"A", "B", "C", "E"}, // + the cap's three
		{"A", "B", "D", "E"}, // churn
		{"A", "B", "D"},      // E released
		{"A", "B", "D", "Z"}, // Z pinned
		{"A", "B", "D", "E", "Z"},
	}
	if len(e.taps) != len(want) {
		t.Fatalf("tap updates %v, want %v", e.taps, want)
	}
	for i := range want {
		if !slices.Equal(e.taps[i], want[i]) {
			t.Fatalf("tap update %d = %v, want %v (all: %v)", i, e.taps[i], want[i], e.taps)
		}
	}
}

func TestPinnedNamesAreNormalisedAndAPinThatIsAlreadyACandidateChangesNothing(t *testing.T) {
	e := newEnv(t)
	t0 := e.clock()
	e.setBars(recentBars(t0))
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 3}, core.PmVolEntry{Sym: "B", PmVolX: 2})
	pins := &pinSet{}
	pins.set("", "B", "B", "A", "") // blanks and duplicates, as a careless caller might return
	cfg := e.config()
	cfg.Pinned = pins.get
	s := e.serviceCfg(cfg, defaultTuning())
	e.step(s, t0)
	if !slices.Equal(s.pins, []string{"A", "B"}) {
		t.Fatalf("pins %v, want [A B]", s.pins)
	}
	if got := keysOf(e.lastPub()); !sameStrings(got, []string{"A", "B"}) {
		t.Fatalf("published %v", got)
	}
	if st := s.stats(); st.candidates != 2 || st.pinned != 2 {
		t.Fatalf("%+v: a pin on a candidate must not add a tracked symbol", st)
	}
	if pins.calls < 2 { // asked at least by the tick and by the refresh
		t.Fatalf("Pinned was asked %d times", pins.calls)
	}
}

func TestAPanickingPinnedCallbackKeepsThePreviousPinsAndNeverUnpins(t *testing.T) {
	buf := captureLog(t)
	e := newEnv(t)
	t0 := e.clock()
	e.setBars(recentBars(t0))
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 3})
	pins := &pinSet{}
	pins.set("E")
	var broken atomic.Bool
	cfg := e.config()
	cfg.Pinned = func() []string {
		if broken.Load() {
			panic("trader bug")
		}
		return pins.get()
	}
	s := e.serviceCfg(cfg, defaultTuning())
	e.step(s, t0)
	if _, ok := e.lastPub()["E"]; !ok {
		t.Fatal("E must be pinned and published before the callback breaks")
	}
	broken.Store(true)
	// Many ticks, and a candidate refresh (which re-reads the pins) in the middle.
	for i := 1; i <= 70; i++ {
		e.step(s, t0.Add(time.Duration(i)*time.Second))
	}
	if !slices.Equal(s.pins, []string{"E"}) || s.syms["E"] == nil {
		t.Fatalf("a panicking Pinned must keep the previous answer, not un-pin: pins %v", s.pins)
	}
	if _, ok := e.lastPub()["E"]; !ok {
		t.Fatal("E's verdict was lost")
	}
	if n := strings.Count(buf.String(), "Config.Pinned panicked"); n != 1 {
		t.Fatalf("logged %d times for %d panics, want once (then every 100th)", n, s.pinPanics)
	}
	// It recovers by itself.
	broken.Store(false)
	pins.set()
	e.step(s, t0.Add(71*time.Second))
	if s.syms["E"] != nil {
		t.Fatal("once the callback answers again (now: nothing pinned), E must be released")
	}
}

// ─── The hooks under the real loop ───────────────────────────────────────

// Candidate churn, a pin set that rotates, a permanent pin on a name that is
// never a candidate, aggregates from a hammering producer - and both callbacks
// asserting they are never running at the same time (they belong to the one
// loop goroutine). Run with -race.
func TestRunWithOnEvalAndPinnedUnderChurnIsSerialAndRaceFree(t *testing.T) {
	before := runtime.NumGoroutine()
	e := newEnv(t)
	names := []string{"AA", "BB", "CC", "DD", "EE", "FF", "GG", "HH"}
	var round atomic.Int64
	e.candFn = func(minX float64, limit int) []core.PmVolEntry {
		r := int(round.Add(1))
		var out []core.PmVolEntry
		for i, s := range names {
			if (i+r)%3 != 0 {
				out = append(out, core.PmVolEntry{Sym: s, PmVolX: float64(10 - i)})
			}
		}
		return out
	}
	e.setBars(func(ctx context.Context, sym string) ([]core.Bar, error) {
		time.Sleep(time.Duration(len(sym)%2) * time.Millisecond)
		return flatBars(e.clock(), 40, 10), nil
	})
	var inside atomic.Int32
	guard := func(what string) func() {
		if n := inside.Add(1); n != 1 {
			t.Errorf("%s ran while another callback was running (%d inside): callbacks must be serial", what, n)
		}
		return func() { inside.Add(-1) }
	}
	var pinTurn atomic.Int64
	var pinCalls, evalCalls atomic.Int64
	var mu sync.Mutex
	seen := map[string]int{}
	aggs := make(chan core.Agg, 256)
	cfg := e.config()
	cfg.Aggs = aggs
	cfg.Now = advancingClock(150 * time.Millisecond)
	cfg.Pinned = func() []string {
		defer guard("Pinned")()
		pinCalls.Add(1)
		// ZZ is never a candidate; one of the names is pinned in turn.
		return []string{"ZZ", names[int(pinTurn.Load())%len(names)]}
	}
	cfg.OnEval = func(u EvalUpdate) {
		defer guard("OnEval")()
		evalCalls.Add(1)
		if u.Sym == "" || u.Last.T == 0 {
			t.Errorf("bad update %+v", u)
		}
		mu.Lock()
		seen[u.Sym]++
		mu.Unlock()
	}
	tm := fastTuning()
	tm.maxCandidates = 4
	s, err := newService(cfg, tm)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := startRun(t, s, ctx)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(2)
	go func() { // the pin set rotates under the loop's feet
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(7 * time.Millisecond):
				pinTurn.Add(1)
			}
		}
	}()
	go func() { // the tap producer, including for symbols that are not candidates now
		defer wg.Done()
		base := et(9, 0, 0).UnixMilli() / minuteMs
		all := append([]string{"ZZ"}, names...)
		for i := 0; ; i++ {
			p := 10 + float64(i%20)*0.01
			select {
			case aggs <- sec(all[i%len(all)], base+int64(i/400), int64(i%60), p, p+0.02, p-0.02, p+0.01, 5):
			case <-stop:
				return
			}
		}
	}()
	waitFor(t, "updates for ZZ, the permanent pin", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return seen["ZZ"] >= 3 && evalCalls.Load() >= 40 && pinCalls.Load() >= 20
	})
	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}
	goroutinesSettle(t, before)

	// ZZ is no candidate and was pinned throughout: it must be in EVERY tap set.
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.taps) == 0 {
		t.Fatal("the tap was never set")
	}
	for i, tap := range e.taps {
		if !slices.Contains(tap, "ZZ") {
			t.Fatalf("tap update %d = %v lost the permanently pinned ZZ", i, tap)
		}
	}
}

// ─── the closed-bar EMA the trader's trailing stop hangs from ────────────

// Every update carries the engine's EmaClosed / EmaClosedTs - the 9 EMA of the last
// CLOSED bar and that bar's start - and they are what the TRADER needs to raise a
// trailing stop once per closed bar: while the last bar is forming they name the
// bar before it, the moment it closes (the clock alone, CloseGrace later) they name
// it, and the number is the fixture's own EMA at that bar, not the forming bar's.
func TestEvalUpdatesCarryTheEmaOfTheLastClosedBar(t *testing.T) {
	run := replayBeat(t, et(8, 50, 0))
	closes := make([]float64, len(run.bars))
	idx := map[int64]int{}
	for i, b := range run.bars {
		closes[i] = b.C
		idx[b.T] = i
	}
	// the 9 EMA the engine publishes: the SMA of the first 9 closes, then k = 2/10
	ema := make([]float64, len(closes))
	sum, prev := 0.0, 0.0
	for i, c := range closes {
		switch {
		case i < 8:
			sum += c
			ema[i] = math.NaN()
		case i == 8:
			sum += c
			prev = sum / 9
			ema[i] = prev
		default:
			prev = c*0.2 + prev*0.8
			ema[i] = prev
		}
	}
	forming, closedNow, distinct := 0, 0, map[int64]bool{}
	for _, u := range run.ups {
		r := u.Result
		if r.EmaClosedTs == 0 {
			if !math.IsNaN(r.EmaClosed) {
				t.Fatalf("EmaClosed %v with no bar (EmaClosedTs 0)", r.EmaClosed)
			}
			continue
		}
		i, ok := idx[r.EmaClosedTs]
		if !ok {
			t.Fatalf("EmaClosedTs %d is not a bar of the fixture", r.EmaClosedTs)
		}
		if math.Abs(r.EmaClosed-ema[i]) > 1e-9 {
			t.Fatalf("EmaClosed at %d = %v, the fixture's EMA there is %v", r.EmaClosedTs, r.EmaClosed, ema[i])
		}
		distinct[r.EmaClosedTs] = true
		if u.NowMs-CloseGrace.Milliseconds() >= u.Last.T+minuteMs {
			closedNow++
			if r.EmaClosedTs != u.Last.T {
				t.Fatalf("the last bar %d has closed (now %d): EmaClosedTs = %d, want that bar", u.Last.T, u.NowMs, r.EmaClosedTs)
			}
		} else {
			forming++
			if r.EmaClosedTs >= u.Last.T {
				t.Fatalf("the last bar %d is still forming: EmaClosedTs = %d must be an earlier bar", u.Last.T, r.EmaClosedTs)
			}
			if r.Ema == r.EmaClosed && u.Last.C != run.bars[idx[u.Last.T]-1].C {
				// a forming bar that closes elsewhere moves the EMA; the two being equal would mean EmaClosed leaked the forming bar
				t.Fatalf("forming bar %d: Ema == EmaClosed %v although the bar's close differs from the previous one", u.Last.T, r.Ema)
			}
		}
	}
	if forming == 0 || closedNow == 0 || len(distinct) < 10 {
		t.Fatalf("the replay must show both phases and many bars: %d forming, %d closed, %d distinct closed bars", forming, closedNow, len(distinct))
	}
}
