package flagscan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// env is the fake world around a service: a controllable clock, a candidate
// list, a Bars function, a recording tap and a recording Publish. Nothing here
// touches the network or an engine. Its event log keeps the ORDER of tap
// updates and REST calls, which is what the tap-before-backfill test asserts.
type env struct {
	t *testing.T

	mu    sync.Mutex
	now   time.Time
	cands []core.PmVolEntry
	// candFn, when set, replaces the cands list (the churn and panic tests).
	candFn func(minX float64, limit int) []core.PmVolEntry
	bars   func(ctx context.Context, sym string) ([]core.Bar, error)
	log    []string
	taps   [][]string
	pubs   []map[string]core.EmaFlagRef
	maxIn  int // max concurrent Bars calls seen
	in     int

	drops atomic.Int64
	aggs  chan core.Agg
}

func newEnv(t *testing.T) *env {
	t.Helper()
	return &env{
		t:    t,
		now:  time.Date(2026, 10, 5, 8, 50, 0, 0, etLocation()),
		aggs: make(chan core.Agg, 1024),
		bars: func(context.Context, string) ([]core.Bar, error) { return nil, errors.New("no bars wired") },
	}
}

func (e *env) clock() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *env) setNow(t time.Time) {
	e.mu.Lock()
	e.now = t
	e.mu.Unlock()
}

func (e *env) setCands(c ...core.PmVolEntry) {
	e.mu.Lock()
	e.cands = c
	e.mu.Unlock()
}

func (e *env) setBars(f func(ctx context.Context, sym string) ([]core.Bar, error)) {
	e.mu.Lock()
	e.bars = f
	e.mu.Unlock()
}

func (e *env) candidates(minX float64, limit int) []core.PmVolEntry {
	e.mu.Lock()
	fn, c := e.candFn, append([]core.PmVolEntry(nil), e.cands...)
	e.mu.Unlock()
	if fn != nil {
		return fn(minX, limit)
	}
	// The real accessor is inclusive at minX and honours limit; mirror both so
	// a service that asked for the wrong thing would show.
	var out []core.PmVolEntry
	for _, x := range c {
		if x.PmVolX >= minX {
			out = append(out, x)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (e *env) barsCall(ctx context.Context, sym string) ([]core.Bar, error) {
	e.mu.Lock()
	f := e.bars
	e.log = append(e.log, "bars:"+sym)
	e.in++
	e.maxIn = max(e.maxIn, e.in)
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.in--
		e.mu.Unlock()
	}()
	return f(ctx, sym)
}

func (e *env) setTap(syms []string) {
	e.mu.Lock()
	e.log = append(e.log, "tap:"+strings.Join(syms, ","))
	e.taps = append(e.taps, append([]string(nil), syms...))
	e.mu.Unlock()
}

func (e *env) publish(m map[string]core.EmaFlagRef) {
	e.mu.Lock()
	e.log = append(e.log, fmt.Sprintf("publish:%d", len(m)))
	e.pubs = append(e.pubs, m)
	e.mu.Unlock()
}

func (e *env) config() Config {
	return Config{
		Candidates: e.candidates,
		Bars:       e.barsCall,
		Aggs:       e.aggs,
		SetTap:     e.setTap,
		Publish:    e.publish,
		Now:        e.clock,
		Drops:      e.drops.Load,
	}
}

// service builds a service driven by hand (no Run): the test calls tick,
// handleAgg and settle itself, with whatever fake time it chooses. Cleanup
// cancels and waits for every fetch goroutine, so a leaked one fails the test
// binary's -race run instead of hiding.
func (e *env) service(tm tuning) *service {
	e.t.Helper()
	s, err := newService(e.config(), tm)
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

// settle drains the background work the last tick started (the candidate query
// and the REST fetches) and applies the results, the way Run's select would. A
// fetch that is parked on a gate makes settle time out: release gates first.
func (e *env) settle(s *service, now time.Time) {
	e.t.Helper()
	for s.candInflight || s.inflight > 0 {
		select {
		case c := <-s.candCh:
			s.handleCandidates(c, now)
		case r := <-s.results:
			s.handleFetch(r, now)
		case <-time.After(5 * time.Second):
			e.t.Fatalf("settle timed out (candInflight=%v inflight=%d)", s.candInflight, s.inflight)
		}
	}
}

// step advances the fake clock to now, runs one tick and everything it kicked
// off, then evaluates once more so a symbol loaded during the settle gets its
// verdict in the same fake second (Run would publish it on the next real tick).
func (e *env) step(s *service, now time.Time) {
	e.t.Helper()
	e.setNow(now)
	s.tick(now)
	e.settle(s, now)
	s.evaluate(now)
}

func (e *env) logCopy() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.log...)
}

func (e *env) published() []map[string]core.EmaFlagRef {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]map[string]core.EmaFlagRef(nil), e.pubs...)
}

// lastPub is the most recent published map (nil if none yet).
func (e *env) lastPub() map[string]core.EmaFlagRef {
	p := e.published()
	if len(p) == 0 {
		return nil
	}
	return p[len(p)-1]
}

func (e *env) countLog(prefix string) int {
	n := 0
	for _, l := range e.logCopy() {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// et builds a time on 2026-10-05 (a Monday, EDT) at the given ET clock time.
func et(h, m, s int) time.Time {
	return time.Date(2026, 10, 5, h, m, s, 0, etLocation())
}

// flatBars makes n 1-minute bars ending at the minute before end, flat at
// price p with a small wobble, for tests that need a loaded history but no
// setup.
func flatBars(end time.Time, n int, p float64) []core.Bar {
	out := make([]core.Bar, 0, n)
	last := end.Truncate(time.Minute).UnixMilli() - minuteMs
	for i := n - 1; i >= 0; i-- {
		out = append(out, core.Bar{T: last - int64(i)*minuteMs, O: p, H: p * 1.001, L: p * 0.999, C: p, V: 100})
	}
	return out
}

// TestMain silences the service's own log lines (the status line fires every
// fake minute in the replay); the tests that assert on logging install their
// own handler and restore this one.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}
