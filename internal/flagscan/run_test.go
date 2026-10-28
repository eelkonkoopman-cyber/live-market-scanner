package flagscan

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// The tests in this file run the real loop (Run's select, real tickers, real
// goroutines) with short tick intervals, to prove the properties that only a
// running loop has: it keeps draining the tap while REST or the engine is slow,
// it stops on cancel and leaves nothing behind, and it is race-free under
// concurrent aggregates, churn and publishes (run these with -race).

// fastTuning shrinks the real-time cadences; the thresholds stay production's.
func fastTuning() tuning {
	tm := defaultTuning()
	tm.evalEvery = 5 * time.Millisecond
	tm.logEvery = 20 * time.Millisecond
	return tm
}

// advancingClock moves fake time forward `step` on every read, so a loop that
// is polling it sees candidate refreshes, resyncs and status lines come due
// without the test waiting for them in real time. It starts at 09:00 ET.
func advancingClock(step time.Duration) func() time.Time {
	var n atomic.Int64
	base := et(9, 0, 0)
	return func() time.Time { return base.Add(time.Duration(n.Add(1)) * step) }
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// goroutinesSettle waits for the goroutine count to fall back to `before`.
func goroutinesSettle(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var n int
	for time.Now().Before(deadline) {
		if n = runtime.NumGoroutine(); n <= before {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	buf := make([]byte, 1<<16)
	buf = buf[:runtime.Stack(buf, true)]
	t.Fatalf("goroutine leak: %d before, %d after\n%s", before, n, buf)
}

func startRun(t *testing.T, s *service, ctx context.Context) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- s.run(ctx) }()
	return done
}

func TestRunReturnsOnCancelAndLeaksNothingEvenMidFetch(t *testing.T) {
	before := runtime.NumGoroutine()

	e := newEnv(t)
	e.cands = []core.PmVolEntry{{Sym: "A", PmVolX: 3}, {Sym: "B", PmVolX: 2}, {Sym: "C", PmVolX: 1.5}}
	e.setBars(func(ctx context.Context, sym string) ([]core.Bar, error) {
		<-ctx.Done() // a REST call that never answers: only cancellation ends it
		return nil, ctx.Err()
	})
	cfg := e.config()
	cfg.Now = advancingClock(10 * time.Millisecond)
	s, err := newService(cfg, fastTuning())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := startRun(t, s, ctx)
	waitFor(t, "three parked backfills", func() bool { return e.countLog("bars:") == 3 })

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v on cancel, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancel (a fetch holds it?)")
	}
	goroutinesSettle(t, before)
}

func TestRunWithAlreadyCancelledContextReturnsAtOnce(t *testing.T) {
	before := runtime.NumGoroutine()
	e := newEnv(t)
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2})
	e.setBars(func(ctx context.Context, sym string) ([]core.Bar, error) { return flatBars(e.clock(), 30, 10), nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, e.config()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	goroutinesSettle(t, before)
}

func TestRunKeepsDrainingTheTapWhileBackfillIsParkedAndTheEngineIsSlow(t *testing.T) {
	e := newEnv(t)
	list := []core.PmVolEntry{{Sym: "A", PmVolX: 3}}
	block := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(block) }) }
	e.setBars(func(ctx context.Context, sym string) ([]core.Bar, error) {
		select {
		case <-block:
			return flatBars(e.clock(), 30, 10), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	// An engine fan-out that stalls: the candidate query is parked from its
	// SECOND call on (the first admits A), the way an engine mid-reset would.
	var calls atomic.Int32
	e.candFn = func(minX float64, limit int) []core.PmVolEntry {
		if calls.Add(1) > 1 {
			<-block
		}
		return list
	}
	aggs := make(chan core.Agg) // UNBUFFERED: every send is a hand-off to the loop
	cfg := e.config()
	cfg.Aggs = aggs
	cfg.Now = advancingClock(10 * time.Second) // candidate refreshes come due constantly
	s, err := newService(cfg, fastTuning())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := startRun(t, s, ctx)
	defer func() {
		release() // unpark the engine query first: it ignores ctx, like the real fan-out
		cancel()
		<-done
	}()
	waitFor(t, "A's backfill to park", func() bool { return e.countLog("bars:A") == 1 })
	waitFor(t, "the second candidate query to park", func() bool { return calls.Load() >= 2 })

	// 300 aggregates, each a rendezvous with the loop: a loop stuck behind the
	// parked REST call or the parked engine query would time out on the first.
	m := e.clock().UnixMilli() / minuteMs
	for i := 0; i < 300; i++ {
		select {
		case aggs <- sec("A", m, int64(i%60), 10, 10.1, 9.9, 10.05, 1):
		case <-time.After(2 * time.Second):
			t.Fatalf("the loop stopped draining the tap after %d aggregates", i)
		}
	}
	release()
}

func TestRunClosedTapDoesNotSpinOrStop(t *testing.T) {
	e := newEnv(t)
	aggs := make(chan core.Agg, 4)
	cfg := e.config()
	cfg.Aggs = aggs
	cfg.Now = advancingClock(time.Millisecond)
	s, err := newService(cfg, fastTuning())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := startRun(t, s, ctx)
	close(aggs)
	time.Sleep(30 * time.Millisecond) // many ticks happen with the tap closed
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestFoldBatchDrainsWhatIsQueuedAndRetiresAClosedTap(t *testing.T) {
	e := newEnv(t)
	s := e.service(defaultTuning())
	m := et(9, 0, 0).UnixMilli() / minuteMs
	s.syms["A"] = &symState{sym: "A", gen: 1}
	ch := make(chan core.Agg, 8)
	for i := 0; i < 5; i++ {
		ch <- sec("A", m, int64(i+1), 10, 10+float64(i), 10, 10, 1)
	}
	s.aggs = ch
	s.foldBatch(sec("A", m, 0, 10, 10, 10, 10, 1))
	if got := s.syms["A"].bars; len(got) != 1 || got[0].V != 6 || got[0].H != 14 {
		t.Fatalf("six aggregates must fold into one bar: %+v", got)
	}
	close(ch)
	s.foldBatch(sec("A", m, 9, 10, 10, 10, 10, 1))
	if s.aggs != nil {
		t.Fatal("a closed tap must be retired (a closed channel is always ready: it would spin the select)")
	}
}

// ─── race stress ─────────────────────────────────────────────────────────

func TestRunUnderConcurrentAggregatesChurnAndPublishes(t *testing.T) {
	before := runtime.NumGoroutine()
	e := newEnv(t)
	syms := []string{"AA", "BB", "CC", "DD", "EE", "FF", "GG", "HH"}

	// The candidate list rotates through the symbols as the clock advances, so
	// admissions, drops, re-admissions and cap trimming all happen mid-run.
	var round atomic.Int64
	e.candFn = func(minX float64, limit int) []core.PmVolEntry {
		r := int(round.Add(1))
		var out []core.PmVolEntry
		for i, s := range syms {
			if (i+r)%3 != 0 { // two thirds qualify, a different two thirds each refresh
				out = append(out, core.PmVolEntry{Sym: s, PmVolX: float64(10 - i)})
			}
		}
		return out
	}
	var fetches atomic.Int64
	e.setBars(func(ctx context.Context, sym string) ([]core.Bar, error) {
		n := fetches.Add(1)
		if n%7 == 0 {
			return nil, fmt.Errorf("transient %d", n)
		}
		time.Sleep(time.Duration(n%3) * time.Millisecond) // overlap in-flight fetches with the loop
		return flatBars(e.clock(), 40, 10), nil
	})
	aggs := make(chan core.Agg, 256)
	var pubMu sync.Mutex
	var pubCount int
	cfg := e.config()
	cfg.Aggs = aggs
	cfg.Now = advancingClock(150 * time.Millisecond)
	cfg.Publish = func(m map[string]core.EmaFlagRef) {
		pubMu.Lock()
		pubCount++
		pubMu.Unlock()
		for sym, ref := range m {
			if ref.State < 0 || ref.State > 4 || sym == "" {
				t.Errorf("bad published ref %q %+v", sym, ref)
			}
		}
		e.publish(m)
	}
	tm := fastTuning()
	tm.maxCandidates = 5
	s, err := newService(cfg, tm)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := startRun(t, s, ctx)

	// A producer hammering the tap (including for symbols that are not
	// candidates right now) from its own goroutine, like ingest's pump.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		base := et(9, 0, 0).UnixMilli() / minuteMs
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			p := 10 + float64(i%20)*0.01
			select {
			case aggs <- sec(syms[i%len(syms)], base+int64(i/400), int64(i%60), p, p+0.02, p-0.02, p+0.01, 5):
			case <-stop:
				return
			}
		}
	}()

	waitFor(t, "publishes and fetches to happen", func() bool {
		pubMu.Lock()
		defer pubMu.Unlock()
		return pubCount >= 5 && fetches.Load() >= 15
	})
	time.Sleep(150 * time.Millisecond)
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
}
