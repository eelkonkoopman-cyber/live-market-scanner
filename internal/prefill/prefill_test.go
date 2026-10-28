package prefill

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/feed"
)

// --- fakes (the flagread harness style: fully injected, no network) ---

var testET = func() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return loc
}()

// et builds an ET wall-clock time on a fixed weekday (2026-08-06, a Thursday).
func et(h, m int) time.Time {
	return time.Date(2026, 8, 6, h, m, 0, 0, testET)
}

// barAt builds a 1-minute bar whose START is h:m ET on the test day.
func barAt(h, m int, o, hi, lo, c float64, v int64) core.Bar {
	return core.Bar{T: et(h, m).UnixMilli(), O: o, H: hi, L: lo, C: c, V: v}
}

type fakeSeeds struct {
	mu    sync.Mutex
	rows  []core.Seed
	calls int
	err   error
}

func (f *fakeSeeds) get(context.Context) ([]core.Seed, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.rows, f.err
}

func (f *fakeSeeds) n() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

type fakeBars struct {
	mu    sync.Mutex
	bars  map[string][]core.Bar
	errs  map[string]error // per-symbol error; also see all429
	calls []string
	// all429: every call fails with an HTTP 429 (the throttle-tripped case).
	all429 bool
}

func (f *fakeBars) get(_ context.Context, sym string) ([]core.Bar, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, sym)
	if f.all429 {
		return nil, &feed.HTTPError{Status: http.StatusTooManyRequests, Msg: "429"}
	}
	if err, ok := f.errs[sym]; ok {
		return nil, err
	}
	return f.bars[sym], nil
}

func (f *fakeBars) n() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

func (f *fakeBars) syms() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string(nil), f.calls...)
	sort.Strings(out)
	return out
}

type fakeApply struct {
	mu    sync.Mutex
	calls []struct {
		which core.Session
		stats map[string]core.SessionStats
	}
}

func (f *fakeApply) apply(which core.Session, stats map[string]core.SessionStats) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make(map[string]core.SessionStats, len(stats))
	for k, v := range stats {
		cp[k] = v
	}
	f.calls = append(f.calls, struct {
		which core.Session
		stats map[string]core.SessionStats
	}{which, cp})
}

func (f *fakeApply) n() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

func (f *fakeApply) get(t *testing.T, which core.Session) map[string]core.SessionStats {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.which == which {
			return c.stats
		}
	}
	return nil
}

// seed builds a snapshot seed row with just the fields rank() reads.
func seed(sym string, prevClose, close, dayOpen float64, vol int64) core.Seed {
	s := core.Seed{PrevClose: prevClose}
	s.Sym, s.Close, s.DayOpen, s.AccVol = sym, close, dayOpen, vol
	return s
}

// newTestWorker builds a Worker with the test clock and zero pace.
func newTestWorker(t *testing.T, cfg Config) *Worker {
	t.Helper()
	w := New(cfg)
	w.pace = 0 // no launch pacing in tests
	return w
}

// --- trigger matrix ---

func TestPlanTriggerMatrix(t *testing.T) {
	cases := []struct {
		name    string
		start   time.Time
		wantAt  time.Time // zero => never runs
		windows []core.Session
	}{
		{"before premarket 03:50 never", et(3, 50), time.Time{}, nil},
		{"midnight never", et(0, 30), time.Time{}, nil},
		{"mid-premarket 07:00 runs at 09:32 pre-only", et(7, 0), et(9, 32),
			[]core.Session{core.SessionPre}},
		{"just after open 09:31 runs +90s pre-only", et(9, 31).Add(30 * time.Second),
			et(9, 31).Add(30*time.Second + startupDelay), []core.Session{core.SessionPre}},
		{"midday 12:00 runs +90s pre-only", et(12, 0), et(12, 0).Add(startupDelay),
			[]core.Session{core.SessionPre}},
		{"evening 17:00 runs +90s pre+rth", et(17, 0), et(17, 0).Add(startupDelay),
			[]core.Session{core.SessionPre, core.SessionRegular}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorker(t, Config{
				Seeds: (&fakeSeeds{}).get, Bars: (&fakeBars{}).get,
				Apply: (&fakeApply{}).apply,
				Cap:   10, AppStart: tc.start, Now: func() time.Time { return tc.start },
			})
			at, windows := w.plan()
			if !at.Equal(tc.wantAt) {
				t.Fatalf("run at = %v, want %v", at, tc.wantAt)
			}
			if len(windows) != len(tc.windows) {
				t.Fatalf("windows = %v, want %v", windows, tc.windows)
			}
			for i := range windows {
				if windows[i] != tc.windows[i] {
					t.Fatalf("windows = %v, want %v", windows, tc.windows)
				}
			}
		})
	}
}

// --- once-per-day latch ---

func TestRunOncePerDayLatch(t *testing.T) {
	seeds := &fakeSeeds{rows: []core.Seed{seed("AAA", 10, 12, 0, 1_000_000)}}
	bars := &fakeBars{bars: map[string][]core.Bar{
		"AAA": {barAt(8, 0, 11, 12, 10.5, 11.5, 5000)},
	}}
	ap := &fakeApply{}
	now := et(12, 0)
	w := newTestWorker(t, Config{
		Seeds: seeds.get, Bars: bars.get, Apply: ap.apply,
		Cap: 10, AppStart: now, Now: func() time.Time { return now },
	})

	w.runOnce(context.Background(), []core.Session{core.SessionPre})
	w.runOnce(context.Background(), []core.Session{core.SessionPre}) // same ET day: no-op
	if seeds.n() != 1 {
		t.Fatalf("Seeds called %d times, want 1 (once-per-day latch)", seeds.n())
	}
	if bars.n() != 1 {
		t.Fatalf("Bars called %d times, want 1", bars.n())
	}
	if ap.n() != 1 {
		t.Fatalf("Apply called %d times, want 1", ap.n())
	}
}

// --- ranking + union + cap ---

func TestRankingPicksTopMoversAndUnionsActive(t *testing.T) {
	// Three big movers, a bunch of dead tickers, and one active symbol the
	// ranking would never pick on its own.
	rows := []core.Seed{
		seed("BIG1", 10, 15, 0, 2_000_000), // +50% and heavy volume
		seed("BIG2", 20, 26, 0, 1_000_000), // +30%
		seed("BIG3", 5, 6, 0, 500_000),     // +20%
		seed("MEH1", 100, 100.1, 0, 1000),
		seed("MEH2", 50, 50.05, 0, 800),
		seed("DEAD", 30, 0, 0, 0), // scores 0: excluded outright
	}
	seeds := &fakeSeeds{rows: rows}
	bars := &fakeBars{bars: map[string][]core.Bar{}}
	ap := &fakeApply{}
	now := et(12, 0)
	w := newTestWorker(t, Config{
		Seeds: seeds.get, Bars: bars.get, Apply: ap.apply,
		Active: func() map[string]struct{} { return map[string]struct{}{"ACTV": {}, "BIG1": {}} },
		Cap:    3, AppStart: now, Now: func() time.Time { return now },
	})
	w.runOnce(context.Background(), []core.Session{core.SessionPre})

	want := []string{"ACTV", "BIG1", "BIG2", "BIG3"} // top-3 ranked + active (BIG1 not doubled)
	got := bars.syms()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("fetched %v, want %v", got, want)
	}
}

// TestActiveUnionCappedAtCap pins the budget bound when a user scan matches a
// huge symbol set: the active-set union is itself capped at Cap, so one run
// fetches at most 2*Cap symbols (Cap ranked + Cap active extras) — Active()
// must never unbound the REST spend. The kept extras are the alphabetically
// first, deterministically.
func TestActiveUnionCappedAtCap(t *testing.T) {
	rows := []core.Seed{
		seed("BIG1", 10, 15, 0, 2_000_000),
		seed("BIG2", 20, 26, 0, 1_000_000),
		seed("BIG3", 5, 6, 0, 500_000),
	}
	// A scan gone wide: 40 active symbols the ranking would never pick.
	active := map[string]struct{}{}
	for i := 0; i < 40; i++ {
		active[fmt.Sprintf("ACT%02d", i)] = struct{}{}
	}
	seeds := &fakeSeeds{rows: rows}
	bars := &fakeBars{bars: map[string][]core.Bar{}}
	ap := &fakeApply{}
	now := et(12, 0)
	w := newTestWorker(t, Config{
		Seeds: seeds.get, Bars: bars.get, Apply: ap.apply,
		Active: func() map[string]struct{} { return active },
		Cap:    3, AppStart: now, Now: func() time.Time { return now },
	})
	w.runOnce(context.Background(), []core.Session{core.SessionPre})

	if n := bars.n(); n != 6 {
		t.Fatalf("Bars called %d times, want 6 (Cap=3 ranked + Cap=3 active extras; the union must be bounded by 2*Cap)", n)
	}
	want := []string{"ACT00", "ACT01", "ACT02", "BIG1", "BIG2", "BIG3"}
	if got := bars.syms(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("fetched %v, want %v (alphabetically-first actives kept, deterministically)", got, want)
	}
}

func TestCapRespectedOnBarsCallCount(t *testing.T) {
	var rows []core.Seed
	for i := 0; i < 200; i++ {
		rows = append(rows, seed(fmt.Sprintf("S%03d", i), 10, 11+float64(i)*0.01, 0, 100_000))
	}
	seeds := &fakeSeeds{rows: rows}
	bars := &fakeBars{bars: map[string][]core.Bar{}}
	ap := &fakeApply{}
	now := et(12, 0)
	w := newTestWorker(t, Config{
		Seeds: seeds.get, Bars: bars.get, Apply: ap.apply,
		Cap: 50, AppStart: now, Now: func() time.Time { return now },
	})
	w.runOnce(context.Background(), []core.Session{core.SessionPre})
	if bars.n() != 50 {
		t.Fatalf("Bars called %d times, want exactly Cap=50", bars.n())
	}
}

// --- 429 abort ---

func TestFirst429AbortsWholeRun(t *testing.T) {
	var rows []core.Seed
	for i := 0; i < 200; i++ {
		rows = append(rows, seed(fmt.Sprintf("S%03d", i), 10, 12, 0, 100_000))
	}
	seeds := &fakeSeeds{rows: rows}
	bars := &fakeBars{all429: true}
	ap := &fakeApply{}
	now := et(12, 0)
	w := newTestWorker(t, Config{
		Seeds: seeds.get, Bars: bars.get, Apply: ap.apply,
		Cap: 200, AppStart: now, Now: func() time.Time { return now },
	})
	w.runOnce(context.Background(), []core.Session{core.SessionPre})

	st := w.stats()
	if !st.aborted {
		t.Fatal("run was not marked aborted after a 429")
	}
	// The first 429 cancels the run; at most the in-flight window (sem 4)
	// plus a couple of already-launched calls may land. Far below 200 is the
	// point: the budget was protected, not ground through.
	if n := bars.n(); n > 2*fetchConcurrency {
		t.Fatalf("Bars called %d times after a 429, want <= %d (abort must stop the run)",
			n, 2*fetchConcurrency)
	}
	if ap.n() != 0 {
		t.Fatalf("Apply called %d times on a fully-429 run, want 0", ap.n())
	}
	if st.fetched != 0 {
		t.Fatalf("fetched = %d, want 0", st.fetched)
	}
}

// --- per-symbol errors skip, not abort ---

func TestPerSymbolErrorSkipsWithoutAborting(t *testing.T) {
	preBar := barAt(8, 0, 11, 12, 10.5, 11.5, 5000)
	seeds := &fakeSeeds{rows: []core.Seed{
		seed("GOOD", 10, 12, 0, 1_000_000),
		seed("BAD", 10, 13, 0, 1_000_000),
		seed("ALSO", 10, 14, 0, 1_000_000),
	}}
	bars := &fakeBars{
		bars: map[string][]core.Bar{"GOOD": {preBar}, "ALSO": {preBar}},
		errs: map[string]error{"BAD": errors.New("boom")},
	}
	ap := &fakeApply{}
	now := et(12, 0)
	w := newTestWorker(t, Config{
		Seeds: seeds.get, Bars: bars.get, Apply: ap.apply,
		Cap: 10, AppStart: now, Now: func() time.Time { return now },
	})
	w.runOnce(context.Background(), []core.Session{core.SessionPre})

	st := w.stats()
	if st.aborted {
		t.Fatal("a generic per-symbol error must not abort the run")
	}
	if st.failed != 1 {
		t.Fatalf("failed = %d, want 1", st.failed)
	}
	if bars.n() != 3 {
		t.Fatalf("Bars called %d times, want 3 (the error must not stop the queue)", bars.n())
	}
	pre := ap.get(t, core.SessionPre)
	if pre == nil {
		t.Fatal("Apply(pre) never called")
	}
	if _, ok := pre["GOOD"]; !ok {
		t.Fatal("GOOD missing from applied pre stats")
	}
	if _, ok := pre["ALSO"]; !ok {
		t.Fatal("ALSO missing from applied pre stats")
	}
	if _, ok := pre["BAD"]; ok {
		t.Fatal("BAD must be skipped, not applied")
	}
}

// --- window slicing ---

func TestWindowSlicingBoundary(t *testing.T) {
	// One symbol trading across the 09:30 boundary: the 09:29 bar is PRE, the
	// 09:30 bar is RTH (it aggregates trades from 09:30:00 onward).
	sym := "EDGE"
	seeds := &fakeSeeds{rows: []core.Seed{seed(sym, 10, 12, 11, 1_000_000)}}
	bars := &fakeBars{bars: map[string][]core.Bar{sym: {
		barAt(4, 0, 10, 10.5, 9.8, 10.2, 1000),      // pre
		barAt(9, 29, 10.2, 11.0, 10.1, 10.8, 3000),  // pre — last pre bar
		barAt(9, 30, 10.8, 11.5, 10.7, 11.2, 4000),  // rth — first rth bar
		barAt(15, 59, 11.2, 12.0, 11.1, 11.9, 2000), // rth — last rth bar
		barAt(16, 0, 11.9, 12.1, 11.8, 12.0, 500),   // post: in NEITHER window
	}}}
	ap := &fakeApply{}
	now := et(17, 0)
	w := newTestWorker(t, Config{
		Seeds: seeds.get, Bars: bars.get, Apply: ap.apply,
		Cap: 10, AppStart: now, Now: func() time.Time { return now },
	})
	w.runOnce(context.Background(), []core.Session{core.SessionPre, core.SessionRegular})

	pre := ap.get(t, core.SessionPre)
	rth := ap.get(t, core.SessionRegular)
	if pre == nil || rth == nil {
		t.Fatalf("Apply not called for both windows: pre=%v rth=%v", pre != nil, rth != nil)
	}
	p, ok := pre[sym]
	if !ok {
		t.Fatal("no pre stats for EDGE")
	}
	// Pre = the 04:00 and 09:29 bars only.
	if p.Hi != 11.0 || p.Lo != 9.8 || p.Vol != 4000 || p.Close != 10.8 {
		t.Fatalf("pre stats wrong: %+v", p)
	}
	wantPreVwap := (10.2*1000 + 10.8*3000) / 4000
	if diff := p.Vwap - wantPreVwap; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("pre vwap = %v, want %v (close-weighted)", p.Vwap, wantPreVwap)
	}
	r, ok := rth[sym]
	if !ok {
		t.Fatal("no rth stats for EDGE")
	}
	// Rth = the 09:30 and 15:59 bars only (16:00 is post).
	if r.Hi != 12.0 || r.Lo != 10.7 || r.Vol != 6000 || r.Close != 11.9 {
		t.Fatalf("rth stats wrong: %+v", r)
	}
	wantRthVwap := (11.2*4000 + 11.9*2000) / 6000
	if diff := r.Vwap - wantRthVwap; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("rth vwap = %v, want %v (close-weighted)", r.Vwap, wantRthVwap)
	}
}

func TestEmptyWindowSkipsSymbol(t *testing.T) {
	// The symbol only traded in the regular session; a PRE-only run must not
	// apply anything for it (its pre slot stays untouched — "never traded").
	sym := "RTHONLY"
	seeds := &fakeSeeds{rows: []core.Seed{seed(sym, 10, 12, 11, 1_000_000)}}
	bars := &fakeBars{bars: map[string][]core.Bar{sym: {
		barAt(10, 0, 10.8, 11.5, 10.7, 11.2, 4000),
	}}}
	ap := &fakeApply{}
	now := et(12, 0)
	w := newTestWorker(t, Config{
		Seeds: seeds.get, Bars: bars.get, Apply: ap.apply,
		Cap: 10, AppStart: now, Now: func() time.Time { return now },
	})
	w.runOnce(context.Background(), []core.Session{core.SessionPre})
	if ap.n() != 0 {
		t.Fatalf("Apply called %d times for a symbol with no pre bars, want 0", ap.n())
	}
}

// --- disabled ---

func TestCapZeroDisablesEntirely(t *testing.T) {
	t.Setenv("SCANNER_PREFILL_CAP", "0")
	seeds := &fakeSeeds{rows: []core.Seed{seed("AAA", 10, 12, 0, 1_000_000)}}
	bars := &fakeBars{}
	ap := &fakeApply{}
	now := et(12, 0)
	w := newTestWorker(t, Config{
		Seeds: seeds.get, Bars: bars.get, Apply: ap.apply,
		AppStart: now, Now: func() time.Time { return now }, // Cap 0 -> env -> 0
	})
	if w.enabled {
		t.Fatal("worker enabled with SCANNER_PREFILL_CAP=0")
	}
	w.runOnce(context.Background(), []core.Session{core.SessionPre})
	if seeds.n() != 0 || bars.n() != 0 || ap.n() != 0 {
		t.Fatalf("disabled worker did work: seeds=%d bars=%d apply=%d",
			seeds.n(), bars.n(), ap.n())
	}
}

func TestCapDefaultsTo300(t *testing.T) {
	// Explicitly clear the env for this test (t.Setenv restores it after).
	t.Setenv("SCANNER_PREFILL_CAP", "")
	w := New(Config{
		Seeds: (&fakeSeeds{}).get, Bars: (&fakeBars{}).get, Apply: (&fakeApply{}).apply,
		AppStart: et(12, 0),
	})
	if w.cfg.Cap != 300 {
		t.Fatalf("default Cap = %d, want 300", w.cfg.Cap)
	}
	if !w.enabled {
		t.Fatal("worker should be enabled with a complete config")
	}
}

// --- VWAP seeding (Engine.SeedScopeVwap) ---

type fakeVwap struct {
	mu   sync.Mutex
	syms []string
}

func (f *fakeVwap) seed(sym string, _ []core.Bar) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.syms = append(f.syms, sym)
}

func TestFrozenRunSeedsVwapsAndExtraCandidates(t *testing.T) {
	seeds := &fakeSeeds{rows: []core.Seed{seed("AAA", 10, 12, 0, 1_000_000)}}
	bars := &fakeBars{bars: map[string][]core.Bar{"AAA": {barAt(10, 0, 11, 12, 10.5, 11.5, 5000)}}}
	vw := &fakeVwap{}
	now := et(12, 0)
	w := newTestWorker(t, Config{
		Seeds: seeds.get, Bars: bars.get, Apply: (&fakeApply{}).apply,
		SeedVwap: vw.seed, Extra: func() []string { return []string{"AAA", "BBB", "CCC"} },
		Cap: 2, AppStart: now, Now: func() time.Time { return now },
	})
	w.runOnce(context.Background(), []core.Session{core.SessionPre})
	if got := bars.syms(); len(got) != 3 || got[0] != "AAA" || got[1] != "BBB" || got[2] != "CCC" {
		t.Fatalf("fetched %v, want AAA + the extras BBB, CCC", got)
	}
	sort.Strings(vw.syms)
	if len(vw.syms) != 3 {
		t.Fatalf("seeded %v", vw.syms)
	}
	// a reconnect right after is covered by that run
	if w.vwapDue(false) {
		t.Fatal("re-seed due within vwapMinGap of the frozen run")
	}
}

func TestVwapDue(t *testing.T) {
	cases := []struct {
		name       string
		start, now time.Time
		first      bool
		want       bool
	}{
		{"mid-session start", et(11, 45), et(11, 47), true, true},
		{"started before the scope", et(9, 0), et(9, 32), true, false}, // saw 09:30 live
		{"reconnect later", et(9, 0), et(11, 0), false, true},
		{"overnight", et(3, 30), et(3, 32), true, false},
		{"after 20:00", et(19, 0), et(20, 5), false, false},
		{"pre-market start", et(6, 0), et(6, 2), true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := tc.now
			w := newTestWorker(t, Config{
				Seeds: (&fakeSeeds{}).get, Bars: (&fakeBars{}).get, Apply: (&fakeApply{}).apply,
				SeedVwap: (&fakeVwap{}).seed, Cap: 5, AppStart: tc.start, Now: func() time.Time { return now },
			})
			if got := w.vwapDue(tc.first); got != tc.want {
				t.Fatalf("vwapDue = %v want %v", got, tc.want)
			}
		})
	}
}
