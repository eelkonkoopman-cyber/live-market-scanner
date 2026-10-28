package flagread

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/persist"
	"github.com/eelkonkoopman/marketscanner/internal/summarize"
)

const testAsOf = "2026-07-30" // the nightly classification date = the cache key

// --- fakes ---

type fakeReader struct {
	mu    sync.Mutex
	calls []core.SetupContext
	err   error // non-nil => NOTHING was billed (summarize's contract)
	// billedFail: the other failure shape — a valid Quality-0 read with Err
	// set and a NIL Go error, meaning a call WAS billed.
	billedFail bool
}

func (r *fakeReader) ReadSetup(_ context.Context, in core.SetupContext) (core.SetupRead, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, in)
	if r.err != nil {
		return core.SetupRead{}, r.err
	}
	if r.billedFail {
		return core.SetupRead{
			Sym: in.Sym, Ts: 7, AsOf: in.Flag.AsOf, Quality: 0, Verdict: "unrated",
			WhatsHappening: "geometry only", Model: "test-model",
			Err: "Claude request failed: boom",
		}, nil
	}
	return core.SetupRead{
		Sym: in.Sym, Ts: 7, AsOf: in.Flag.AsOf, Quality: 4, Verdict: "constructive",
		WhatsHappening: "READ:" + in.Sym, Model: "test-model",
	}, nil
}

func (r *fakeReader) Model() string { return "test-model" }

func (r *fakeReader) n() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.calls) }

func (r *fakeReader) first(t *testing.T) core.SetupContext {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		t.Fatal("reader was never called")
	}
	return r.calls[0]
}

type fakeArchive struct {
	mu    sync.Mutex
	rows  map[string]core.SetupRead
	saves int
	// getErr: the lookup FAILED (a locked/corrupt archive), which is a
	// different thing from a miss — see TestCacheLookupErrorSkipsTheReadAndRefunds.
	getErr error
}

func key(sym, asOf string) string { return sym + "|" + asOf }

func (a *fakeArchive) SaveSetupRead(_ context.Context, r core.SetupRead) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.saves++
	a.rows[key(r.Sym, r.AsOf)] = r
	return nil
}

func (a *fakeArchive) GetSetupRead(_ context.Context, sym, asOf string) (core.SetupRead, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.getErr != nil {
		return core.SetupRead{}, false, a.getErr
	}
	r, ok := a.rows[key(sym, asOf)]
	return r, ok, nil
}

func (a *fakeArchive) setGetErr(err error) { a.mu.Lock(); defer a.mu.Unlock(); a.getErr = err }

func (a *fakeArchive) RecentSetupReads(context.Context, string, int) ([]core.SetupRead, error) {
	return nil, nil
}

func (a *fakeArchive) saveCount() int { a.mu.Lock(); defer a.mu.Unlock(); return a.saves }

// --- harness ---

type harness struct {
	w *Worker

	reader *fakeReader
	arch   *fakeArchive

	mu        sync.Mutex
	now       time.Time
	rows      map[string]core.Row
	flags     map[string]core.FlagRef
	flagScans map[string]bool
	bars      []core.DailyBar
	barsErr   error
	barCalls  int
	// rowGate, when non-nil, parks every RowFor call until it is closed —
	// standing in for the Scanner ctrl round-trip stalling behind a busy tick.
	// See TestRunNeverBlocksTheEntriesChannel.
	rowGate chan struct{}
}

func textbookFlag() core.FlagRef {
	return core.FlagRef{
		Score: 82.5, State: 1, PoleGain: 45.0, RangePct: 6.2, Retrace: 18.4,
		VolDry: 0.62, Bars: 7, BaseHigh: 12.5, AsOf: testAsOf,
	}
}

// flagRow is a row as scan.overlayFlags leaves it: the seven flag numbers
// stamped on, no AsOf/BaseHigh (those live only on the FlagRef).
func flagRow(sym string, score, state float64) core.Row {
	f := int64(9_000_000)
	return core.Row{
		Sym: sym, Last: 12.6, ChgDay: 3.2, Rvol: 2.4, Float: &f,
		FlagScore: score, FlagState: state, FlagPole: 45.0, FlagRange: 6.2,
		FlagRetrace: 18.4, FlagVolDry: 0.62, FlagBars: 7,
	}
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		reader: &fakeReader{},
		arch:   &fakeArchive{rows: map[string]core.SetupRead{}},
		rows:   map[string]core.Row{},
		flags:  map[string]core.FlagRef{},
		// Two scans that both filter on the flag fields, plus one that doesn't:
		// a symbol can enter several at once on the same tick.
		flagScans: map[string]bool{"setups": true, "coils": true, "momentum": false},
		bars: []core.DailyBar{
			{Date: "2026-07-29", O: 11, H: 12, L: 10.8, C: 11.8, V: 1_000_000},
			{Date: testAsOf, O: 11.8, H: 12.5, L: 11.6, C: 12.2, V: 800_000},
		},
	}
	h.w = h.newWorker(h.arch, nil)
	// Anchor the clock in the worker's own location so the ET-midnight test
	// stays meaningful whether or not the tz database is linked into this
	// particular test binary.
	h.now = time.Date(2026, 7, 31, 10, 0, 0, 0, h.w.loc)
	h.add("TIGHT", 82.5, 1)
	return h
}

// add registers a classified symbol: a row carrying the overlay numbers and the
// matching nightly reference.
func (h *harness) add(sym string, score, state float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rows[sym] = flagRow(sym, score, state)
	fr := textbookFlag()
	fr.Score = score
	h.flags[sym] = fr
}

func (h *harness) setRow(sym string, r core.Row) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rows[sym] = r
}

func (h *harness) nowFn() time.Time        { h.mu.Lock(); defer h.mu.Unlock(); return h.now }
func (h *harness) advance(d time.Duration) { h.mu.Lock(); defer h.mu.Unlock(); h.now = h.now.Add(d) }

func (h *harness) rowFor(sym string) (core.Row, bool) {
	h.mu.Lock()
	gate := h.rowGate
	r, ok := h.rows[sym]
	h.mu.Unlock()
	if gate != nil {
		<-gate // never while holding h.mu
	}
	return r, ok
}

func (h *harness) setRowGate(g chan struct{}) { h.mu.Lock(); defer h.mu.Unlock(); h.rowGate = g }

// newWorker rebuilds the worker with the same callbacks and cost controls but a
// different archive and/or entry channel — the seam the SQLite round-trip and
// the Run-loop tests need. Everything else is byte-identical to newHarness.
func (h *harness) newWorker(cache core.SetupArchive, entries <-chan core.ScanEntry) *Worker {
	return New(Config{
		Entries:   entries,
		RowFor:    h.rowFor,
		UsesFlags: h.usesFlags,
		FlagFor:   h.flagFor,
		BarsFor:   h.barsFor,
		Reader:    h.reader,
		Cache:     cache,
		MaxPerSym: 2, MaxPerDay: 3, Cooldown: time.Minute, MinScore: 60,
		Now: h.nowFn,
	})
}

func (h *harness) flagFor(sym string) (core.FlagRef, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f, ok := h.flags[sym]
	return f, ok
}

func (h *harness) usesFlags(scanID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.flagScans[scanID]
}

func (h *harness) barsFor(context.Context, string) ([]core.DailyBar, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.barCalls++
	return h.bars, h.barsErr
}

func (h *harness) barCallCount() int { h.mu.Lock(); defer h.mu.Unlock(); return h.barCalls }

// enter feeds one scan entry through the full worker path, synchronously.
func (h *harness) enter(sym string) { h.enterOn("setups", sym) }

func (h *harness) enterOn(scanID, sym string) {
	h.w.handle(context.Background(), core.ScanEntry{
		ScanID: scanID, Sym: sym, At: h.nowFn().UnixMilli(), Last: 12.6,
	})
}

func (h *harness) counters(sym string) (day, per int) {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	return h.w.dayDone, h.w.symDone[sym]
}

func (h *harness) drain() []core.SetupRead {
	var out []core.SetupRead
	for {
		select {
		case r := <-h.w.reads:
			out = append(out, r)
		default:
			return out
		}
	}
}

// --- the gate: nothing below may cost a single Claude call ---

func TestEntryFromNonFlagScanIsIgnored(t *testing.T) {
	h := newHarness(t)
	h.enterOn("momentum", "TIGHT") // perfect setup, but the scan never mentions the flag fields

	if h.reader.n() != 0 {
		t.Fatalf("a scan that doesn't filter on flags must not buy a read, got %d calls", h.reader.n())
	}
	if h.barCallCount() != 0 {
		t.Fatal("the cheapest gate must reject before any bar query")
	}
	if day, per := h.counters("TIGHT"); day != 0 || per != 0 {
		t.Fatalf("no budget may be touched: day=%d sym=%d", day, per)
	}
	if len(h.drain()) != 0 {
		t.Fatal("nothing should be broadcast")
	}
}

// A Null flag field is a NaN, so EVERY comparison against it is false — the
// `score < MinScore` test alone would let it through. The explicit IsNull
// checks are what reject it.
func TestNullFlagDataIsIgnored(t *testing.T) {
	cases := []struct {
		name         string
		score, state float64
	}{
		{"null score, real state", core.Null, 1},
		{"real score, null state", 82.5, core.Null},
		{"unclassified symbol: both null", core.Null, core.Null},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.setRow("TIGHT", flagRow("TIGHT", tc.score, tc.state))
			h.enter("TIGHT")

			if h.reader.n() != 0 {
				t.Fatalf("null flag data must never be read, got %d calls", h.reader.n())
			}
			if day, _ := h.counters("TIGHT"); day != 0 {
				t.Fatalf("no budget may be spent, day=%d", day)
			}
		})
	}
}

func TestNoPatternAndWeakScoreAreIgnored(t *testing.T) {
	cases := []struct {
		name         string
		score, state float64
	}{
		{"state 0 = no pattern", 95, 0},
		{"score under MinScore", 59.9, 1},
		{"score under MinScore even while breaking out", 20, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.setRow("TIGHT", flagRow("TIGHT", tc.score, tc.state))
			h.enter("TIGHT")

			if h.reader.n() != 0 {
				t.Fatalf("ineligible geometry must not be read, got %d calls", h.reader.n())
			}
			if day, _ := h.counters("TIGHT"); day != 0 {
				t.Fatalf("no budget may be spent, day=%d", day)
			}
		})
	}
}

func TestUnknownSymbolIsIgnored(t *testing.T) {
	h := newHarness(t)
	h.enter("NOSUCH")
	if h.reader.n() != 0 || h.barCallCount() != 0 {
		t.Fatal("a symbol with no row must be dropped before any spend")
	}
	if day, _ := h.counters("NOSUCH"); day != 0 {
		t.Fatal("no budget may be spent on a symbol we have no row for")
	}
}

// A symbol whose row carries flag numbers but is missing from the nightly
// reference has no AsOf to key a read on — and it must cost nothing.
func TestMissingFlagReferenceSpendsNothing(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	delete(h.flags, "TIGHT")
	h.mu.Unlock()

	h.enter("TIGHT")
	if h.reader.n() != 0 {
		t.Fatal("no reference, no read")
	}
	if day, per := h.counters("TIGHT"); day != 0 || per != 0 {
		t.Fatalf("budget must be untouched: day=%d sym=%d", day, per)
	}
}

// --- cooldown ---

func TestCooldownBlocksSecondEntryThenAllows(t *testing.T) {
	h := newHarness(t)
	// A billed failure is deliberately NOT cached, so the only thing that can
	// block the second entry here is the cooldown itself.
	h.reader.billedFail = true

	h.enter("TIGHT")
	if h.reader.n() != 1 {
		t.Fatalf("first entry should read, got %d", h.reader.n())
	}
	h.advance(30 * time.Second) // still inside the 1m cooldown
	h.enter("TIGHT")
	if h.reader.n() != 1 {
		t.Fatalf("second entry inside the cooldown must not read, got %d", h.reader.n())
	}
	h.advance(31 * time.Second) // past it
	h.enter("TIGHT")
	if h.reader.n() != 2 {
		t.Fatalf("entry after the cooldown should read, got %d", h.reader.n())
	}
}

func TestCooldownIsPerSymbol(t *testing.T) {
	h := newHarness(t)
	if !h.w.shouldRun("TIGHT") {
		t.Fatal("first should run")
	}
	if h.w.shouldRun("TIGHT") {
		t.Fatal("second within cooldown should not run")
	}
	if !h.w.shouldRun("OTHER") {
		t.Fatal("a different symbol is unaffected")
	}
}

// --- budgets ---

func TestPerSymbolDailyBudgetCaps(t *testing.T) {
	h := newHarness(t) // MaxPerSym 2
	h.reader.billedFail = true

	for i := 0; i < 4; i++ {
		h.enter("TIGHT")
		h.advance(2 * time.Minute) // clear the cooldown so the BUDGET is what bites
	}
	if h.reader.n() != 2 {
		t.Fatalf("per-symbol budget is 2 reads/day, got %d", h.reader.n())
	}
	if day, per := h.counters("TIGHT"); day != 2 || per != 2 {
		t.Fatalf("counters want day=2 sym=2, got day=%d sym=%d", day, per)
	}
}

func TestGlobalDailyBudgetCaps(t *testing.T) {
	h := newHarness(t) // MaxPerDay 3
	h.reader.billedFail = true
	for _, sym := range []string{"AAA", "BBB", "CCC", "DDD"} {
		h.add(sym, 82.5, 1)
	}
	for _, sym := range []string{"AAA", "BBB", "CCC", "DDD"} {
		h.enter(sym) // each symbol has its own fresh cooldown
	}
	if h.reader.n() != 3 {
		t.Fatalf("global budget is 3 reads/day, got %d", h.reader.n())
	}
	if day, _ := h.counters("DDD"); day != 3 {
		t.Fatalf("dayDone want 3, got %d", day)
	}
}

func TestBudgetsResetAtETMidnight(t *testing.T) {
	h := newHarness(t)
	h.reader.billedFail = true
	for _, sym := range []string{"AAA", "BBB", "CCC"} {
		h.add(sym, 82.5, 1)
		h.enter(sym)
	}
	h.add("DDD", 82.5, 1)
	h.enter("DDD")
	if h.reader.n() != 3 {
		t.Fatalf("the day's budget is spent, got %d reads", h.reader.n())
	}

	// 10:00 ET + 16h = 02:00 ET the next day: the ET date rolled over.
	h.advance(16 * time.Hour)
	h.enter("DDD")
	if h.reader.n() != 4 {
		t.Fatalf("a new ET day restores the budget, got %d reads", h.reader.n())
	}
	if day, per := h.counters("DDD"); day != 1 || per != 1 {
		t.Fatalf("counters should have reset then counted one: day=%d sym=%d", day, per)
	}
}

// --- cache ---

func TestCacheHitEmitsWithoutSpendingBudget(t *testing.T) {
	h := newHarness(t)
	cached := core.SetupRead{
		Sym: "TIGHT", Ts: 1, AsOf: testAsOf, Quality: 5, Verdict: "textbook",
		WhatsHappening: "CACHED", Model: "test-model",
	}
	if err := h.arch.SaveSetupRead(context.Background(), cached); err != nil {
		t.Fatal(err)
	}

	h.enter("TIGHT")

	if h.reader.n() != 0 {
		t.Fatalf("a cached read for this AsOf must not be re-bought, got %d calls", h.reader.n())
	}
	if h.barCallCount() != 0 {
		t.Fatal("a cache hit must not even load bars")
	}
	got := h.drain()
	if len(got) != 1 || got[0].WhatsHappening != "CACHED" {
		t.Fatalf("the cached read must still be broadcast, got %+v", got)
	}
	if day, per := h.counters("TIGHT"); day != 0 || per != 0 {
		t.Fatalf("cache hits are free -- counters want 0/0, got day=%d sym=%d", day, per)
	}
}

// A cache lookup that ERRORS is not a cache miss. It means we do not know
// whether this (sym, AsOf) was already paid for, and buying again is the one
// UNBOUNDED leak in the design: the same broken archive fails SaveSetupRead
// too, so the cache never populates, so every entrant re-buys every cooldown,
// every day, forever — and silently, because a swallowed error logs nothing.
// Fail closed instead, and recover on the next entry once the archive is well.
func TestCacheLookupErrorSkipsTheReadAndRefunds(t *testing.T) {
	h := newHarness(t)
	h.arch.setGetErr(errors.New("database is locked"))

	h.enter("TIGHT")

	if h.reader.n() != 0 {
		t.Fatalf("a FAILED cache lookup must not be spent through as a miss, got %d calls", h.reader.n())
	}
	if h.barCallCount() != 0 {
		t.Fatalf("nothing past the lookup should run, got %d bar loads", h.barCallCount())
	}
	if day, per := h.counters("TIGHT"); day != 0 || per != 0 {
		t.Fatalf("the reservation must be handed back: day=%d sym=%d", day, per)
	}
	if len(h.drain()) != 0 {
		t.Fatal("nothing was produced, so nothing may be broadcast")
	}

	// Not a latch: a transient archive failure costs one read, not the feature.
	h.arch.setGetErr(nil)
	h.advance(2 * time.Minute) // clear the cooldown the skipped attempt consumed
	h.enter("TIGHT")
	if h.reader.n() != 1 {
		t.Fatalf("a recovered archive must let the next entry read, got %d calls", h.reader.n())
	}
	if day, per := h.counters("TIGHT"); day != 1 || per != 1 {
		t.Fatalf("that one billed read should count 1/1, got day=%d sym=%d", day, per)
	}
}

// The cache key is (sym, nightly AsOf): tonight's fresh classification is a
// miss, which is exactly what earns a new read.
func TestNewAsOfIsACacheMiss(t *testing.T) {
	h := newHarness(t)
	if err := h.arch.SaveSetupRead(context.Background(), core.SetupRead{
		Sym: "TIGHT", AsOf: "2026-07-29", Quality: 5, WhatsHappening: "YESTERDAY",
	}); err != nil {
		t.Fatal(err)
	}
	h.enter("TIGHT")
	if h.reader.n() != 1 {
		t.Fatalf("a read for a PREVIOUS AsOf is not a hit for today's, got %d calls", h.reader.n())
	}
}

// --- the read itself ---

func TestSuccessfulReadIsPersistedAndBroadcast(t *testing.T) {
	h := newHarness(t)
	h.setRow("TIGHT", func() core.Row { // price cleared the base intraday
		r := flagRow("TIGHT", 82.5, 2)
		r.Last = 12.9
		return r
	}())

	h.enter("TIGHT")

	in := h.reader.first(t)
	if in.Sym != "TIGHT" || in.Last != 12.9 || in.ChgDay != 3.2 || in.Rvol != 2.4 {
		t.Fatalf("live row numbers not carried into the read: %+v", in)
	}
	if in.Float == nil || *in.Float != 9_000_000 {
		t.Fatalf("float not carried into the read: %v", in.Float)
	}
	if len(in.Bars) != 2 || in.Bars[0].Date != "2026-07-29" {
		t.Fatalf("daily bars not carried oldest-first: %+v", in.Bars)
	}
	// Geometry comes from the nightly reference (AsOf/BaseHigh live only there)
	// but STATE comes from the row: the breakout happened after last night.
	if in.Flag.AsOf != testAsOf || in.Flag.BaseHigh != 12.5 {
		t.Fatalf("nightly reference not carried: %+v", in.Flag)
	}
	if in.Flag.State != 2 {
		t.Fatalf("live breakout state must override the stored 1, got %v", in.Flag.State)
	}

	got := h.drain()
	if len(got) != 1 || got[0].Quality != 4 || got[0].WhatsHappening != "READ:TIGHT" {
		t.Fatalf("read not broadcast: %+v", got)
	}
	if h.arch.saveCount() != 1 {
		t.Fatalf("a successful read must be persisted once, got %d saves", h.arch.saveCount())
	}
	if r, ok, _ := h.arch.GetSetupRead(context.Background(), "TIGHT", testAsOf); !ok || r.Quality != 4 {
		t.Fatalf("stored read wrong: %+v ok=%v", r, ok)
	}
	if day, per := h.counters("TIGHT"); day != 1 || per != 1 {
		t.Fatalf("one billed read want 1/1, got day=%d sym=%d", day, per)
	}
}

// summarize's contract: a NON-NIL error means no call was billed, so the
// reservation must be handed back.
func TestReaderErrorRefundsBudget(t *testing.T) {
	h := newHarness(t)
	h.reader.err = errors.New("not logged in")

	h.enter("TIGHT")

	if h.reader.n() != 1 {
		t.Fatalf("want one attempt, got %d", h.reader.n())
	}
	if day, per := h.counters("TIGHT"); day != 0 || per != 0 {
		t.Fatalf("a failed call bills nothing and must refund: day=%d sym=%d", day, per)
	}
	if len(h.drain()) != 0 {
		t.Fatal("nothing to broadcast when no read was produced")
	}
	if h.arch.saveCount() != 0 {
		t.Fatal("nothing to persist either")
	}
}

// The other half of the contract: a NIL error with Err set means a call WAS
// billed. That budget must stay spent, or a backend failing on every call could
// burn money without limit.
func TestBilledFailedReadKeepsBudgetSpentAndIsNotCached(t *testing.T) {
	h := newHarness(t)
	h.reader.billedFail = true

	h.enter("TIGHT")

	if day, per := h.counters("TIGHT"); day != 1 || per != 1 {
		t.Fatalf("a billed failure must NOT be refunded: day=%d sym=%d", day, per)
	}
	got := h.drain()
	if len(got) != 1 || got[0].Quality != 0 || got[0].Err == "" {
		t.Fatalf("the failed read is still broadcast: %+v", got)
	}
	if h.arch.saveCount() != 0 {
		t.Fatal("a failed read must not be cached as this AsOf's answer")
	}
	if _, ok, _ := h.arch.GetSetupRead(context.Background(), "TIGHT", testAsOf); ok {
		t.Fatal("a later entry must be able to retry, not hit a stored failure")
	}
}

// A missing API backend is permanent for the process: refund, latch off, and
// never attempt again — not even for a different symbol.
func TestBackendUnavailableRefundsAndDisablesTheProcess(t *testing.T) {
	h := newHarness(t)
	h.reader.err = fmt.Errorf("wrapped: %w", summarize.ErrSetupReadUnavailable)
	h.add("OTHER", 90, 1)

	h.enter("TIGHT")
	if day, per := h.counters("TIGHT"); day != 0 || per != 0 {
		t.Fatalf("the unavailable sentinel bills nothing: day=%d sym=%d", day, per)
	}
	if !h.w.off.Load() {
		t.Fatal("the worker must latch off")
	}

	h.enter("OTHER")
	h.advance(2 * time.Minute)
	h.enter("OTHER")
	if h.reader.n() != 1 {
		t.Fatalf("no retries after the backend is known missing, got %d attempts", h.reader.n())
	}
	if h.barCallCount() != 1 {
		t.Fatalf("later entries must be dropped by the gate, before bars: %d bar loads", h.barCallCount())
	}
}

func TestNoDailyBarsRefundsWithoutReading(t *testing.T) {
	cases := []struct {
		name string
		bars []core.DailyBar
		err  error
	}{
		{"query failed", nil, errors.New("db closed")},
		{"no history stored", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.mu.Lock()
			h.bars, h.barsErr = tc.bars, tc.err
			h.mu.Unlock()

			h.enter("TIGHT")

			if h.reader.n() != 0 {
				t.Fatalf("no chart to send means no read worth paying for, got %d calls", h.reader.n())
			}
			if day, per := h.counters("TIGHT"); day != 0 || per != 0 {
				t.Fatalf("the reservation must be refunded: day=%d sym=%d", day, per)
			}
		})
	}
}

// shouldRun's single check-and-mark is what makes this true: a burst of
// entries for one symbol (several scans matching it on the same tick) must
// produce exactly ONE paid read.
func TestConcurrentEntriesForOneSymbolReadOnce(t *testing.T) {
	h := newHarness(t)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.enter("TIGHT")
		}()
	}
	wg.Wait()

	if h.reader.n() != 1 {
		t.Fatalf("8 concurrent entries for one symbol must buy exactly 1 read, got %d", h.reader.n())
	}
	if got := h.drain(); len(got) != 1 {
		t.Fatalf("exactly one read should be broadcast, got %d", len(got))
	}
	if day, per := h.counters("TIGHT"); day != 1 || per != 1 {
		t.Fatalf("counters want 1/1, got day=%d sym=%d", day, per)
	}
}

// Two scans that BOTH filter on the flag fields, both matching the same symbol
// on the same tick. This is the ordinary way a trader runs the feature (a
// "coils" scan and a "setups" scan side by side), and it must buy ONE read, not
// one per scan — shouldRun's single check-and-mark is per SYMBOL, deliberately
// not per (symbol, scan).
func TestTwoFlagFilteringScansInOneTickBuyOneRead(t *testing.T) {
	h := newHarness(t)

	var wg sync.WaitGroup
	for _, id := range []string{"setups", "coils"} {
		wg.Add(1)
		go func(scanID string) { defer wg.Done(); h.enterOn(scanID, "TIGHT") }(id)
	}
	wg.Wait()

	if h.reader.n() != 1 {
		t.Fatalf("one symbol entering two flag scans at once must buy 1 read, got %d", h.reader.n())
	}
	if h.barCallCount() != 1 {
		t.Fatalf("and load bars once, got %d", h.barCallCount())
	}
	if day, per := h.counters("TIGHT"); day != 1 || per != 1 {
		t.Fatalf("counters want 1/1, got day=%d sym=%d", day, per)
	}
	if got := h.drain(); len(got) != 1 {
		t.Fatalf("exactly one read should be broadcast, got %d", len(got))
	}
}

// The end-to-end cost claim of the whole feature, against the REAL SQLite
// archive rather than the map fake: a symbol earns exactly ONE paid read per
// nightly classification, and every later entry that day — including after a
// mid-day process restart — reads back what that call bought.
//
// Running it through persist proves the two things the fake cannot: that the
// key the worker SAVES under is the key it LOOKS UP under once it has been
// through SQL, and that flag.AsOf is actually populated (an empty AsOf would
// still round-trip in the fake's string key, and would silently collapse every
// classification into one row).
func TestSQLiteCacheAbsorbsReEntryAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	db, err := persist.Open(ctx, filepath.Join(t.TempDir(), "setupreads.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	arch := db.SetupArchive()

	h := newHarness(t)
	h.w = h.newWorker(arch, nil)

	h.enter("TIGHT") // the one paid read
	if h.reader.n() != 1 {
		t.Fatalf("the first entry should buy exactly one read, got %d", h.reader.n())
	}
	if day, per := h.counters("TIGHT"); day != 1 || per != 1 {
		t.Fatalf("that read costs 1/1, got day=%d sym=%d", day, per)
	}

	// The key really is (sym, the nightly AsOf) — not (sym, "").
	if _, ok, err := arch.GetSetupRead(ctx, "TIGHT", testAsOf); err != nil || !ok {
		t.Fatalf("the read must be stored under the nightly AsOf %q: ok=%v err=%v", testAsOf, ok, err)
	}
	if _, ok, _ := arch.GetSetupRead(ctx, "TIGHT", ""); ok {
		t.Fatal("stored under an EMPTY as_of: flag.AsOf never reached the archive")
	}

	// Re-entry past the cooldown, same nightly classification: free.
	h.advance(2 * time.Minute)
	h.enter("TIGHT")
	if h.reader.n() != 1 {
		t.Fatalf("re-entry on an unchanged classification must hit the cache, got %d calls", h.reader.n())
	}
	if h.barCallCount() != 1 {
		t.Fatalf("a cache hit must not even load bars, got %d loads", h.barCallCount())
	}
	if day, per := h.counters("TIGHT"); day != 1 || per != 1 {
		t.Fatalf("a cache hit is refunded, so counters stay 1/1: day=%d sym=%d", day, per)
	}

	// Restart mid-day: brand-new worker (empty cooldown map, zeroed counters,
	// no in-memory state at all), same database on disk. Still free.
	h.drain()
	h.w = h.newWorker(arch, nil)
	h.enter("TIGHT")
	if h.reader.n() != 1 {
		t.Fatalf("a restart must not re-buy what the database already holds, got %d calls", h.reader.n())
	}
	if day, per := h.counters("TIGHT"); day != 0 || per != 0 {
		t.Fatalf("the restarted worker spent budget on a cached read: day=%d sym=%d", day, per)
	}
	got := h.drain()
	if len(got) != 1 || got[0].WhatsHappening != "READ:TIGHT" || got[0].AsOf != testAsOf {
		t.Fatalf("the restarted worker must broadcast the STORED read, got %+v", got)
	}

	// And a fresh nightly classification is still a miss — the cache absorbs
	// re-entries, it does not freeze a symbol's read in forever.
	h.mu.Lock()
	fr := h.flags["TIGHT"]
	fr.AsOf = "2026-07-31"
	h.flags["TIGHT"] = fr
	h.mu.Unlock()
	h.advance(2 * time.Minute)
	h.enter("TIGHT")
	if h.reader.n() != 2 {
		t.Fatalf("a new nightly AsOf must earn a fresh read, got %d calls", h.reader.n())
	}
}

// Run must never do per-entry work ON its own loop. The gate's first two steps
// are synchronous ctrl round-trips into the Scanner and a read attempt can run
// for runTimeout; either one executed inline would back the scanner's entry
// channel up behind Claude, which is precisely why autosum spawns per symbol.
func TestRunNeverBlocksTheEntriesChannel(t *testing.T) {
	h := newHarness(t)
	stuck := make(chan struct{})
	h.setRowGate(stuck) // every gate call parks in RowFor

	entries := make(chan core.ScanEntry) // UNBUFFERED: a send lands only if Run received it
	h.w = h.newWorker(h.arch, entries)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.w.Run(ctx) }()

	// Every one of these wedges in the gate. The loop must keep draining anyway.
	for i := 0; i < 20; i++ {
		select {
		case entries <- core.ScanEntry{
			ScanID: "setups", Sym: fmt.Sprintf("S%02d", i), At: h.nowFn().UnixMilli(),
		}:
		case <-time.After(2 * time.Second):
			t.Fatalf("Run stopped draining Entries after %d entries: handle is running ON the loop", i)
		}
	}

	close(stuck) // let the parked gates finish (all unknown symbols: no spend)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run should exit cleanly on ctx cancel, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
	if h.reader.n() != 0 {
		t.Fatalf("none of those symbols has a row, so none may be read: %d calls", h.reader.n())
	}
}

// Reads() is an advisory broadcast, so emit is a non-blocking send: no UI, a
// disconnected UI, or a UI slower than the worker must not park a read
// goroutine — which, holding a budget reservation, would leak it until
// runTimeout and could pile goroutines up behind a wedged consumer.
func TestFullReadsChannelDropsInsteadOfWedgingTheWorker(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < cap(h.w.reads); i++ { // nobody is consuming
		h.w.reads <- core.SetupRead{Sym: "FILLER"}
	}

	done := make(chan struct{})
	go func() { defer close(done); h.enter("TIGHT") }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("a full Reads() channel wedged the worker: emit must be a non-blocking send")
	}

	// The read still happened in full — only the broadcast was dropped.
	if h.reader.n() != 1 {
		t.Fatalf("want one read, got %d", h.reader.n())
	}
	if h.arch.saveCount() != 1 {
		t.Fatalf("the read must still be persisted, got %d saves", h.arch.saveCount())
	}
	if day, per := h.counters("TIGHT"); day != 1 || per != 1 {
		t.Fatalf("and still be counted: day=%d sym=%d", day, per)
	}
}

// A refund can land after shouldRun's ET-midnight reset, i.e. against counters
// that belong to a DIFFERENT day than the reservation did. It must floor at
// zero: a negative counter would silently hand the new day extra budget.
func TestRefundFloorsAtZeroSoItCannotGrantExtraBudget(t *testing.T) {
	h := newHarness(t)
	h.w.unbudgetOne("TIGHT") // stale refund from a reservation taken on the old day
	if day, per := h.counters("TIGHT"); day != 0 || per != 0 {
		t.Fatalf("counters must floor at zero, got day=%d sym=%d", day, per)
	}

	h.reader.billedFail = true
	for _, sym := range []string{"AAA", "BBB", "CCC", "DDD"} {
		h.add(sym, 82.5, 1)
		h.enter(sym)
	}
	if h.reader.n() != 3 {
		t.Fatalf("the day's budget is still 3 after a stale refund, got %d reads", h.reader.n())
	}
}

func TestIncompleteConfigDisablesWorker(t *testing.T) {
	w := New(Config{}) // no callbacks, no reader, no cache
	if w.enabled {
		t.Fatal("a worker missing its dependencies must stay disabled, not panic later")
	}
}

// The kill switch has to stay safe for the SERVER, not just for the wallet.
// main.go wires server.Config.SetupReads = fread.Reads() unconditionally, and
// the broadcast loop selects on that channel for the life of the process. So a
// disabled worker must still hand back a channel that is
//
//   - non-nil, because Reads() is called before anything checks enabled, and
//   - never closed, which is the actual wedge: a receive on a CLOSED channel
//     succeeds instantly and forever, so the broadcast select would spin at
//     full tilt and flood every client with zero-value setup.read frames.
//
// An open, empty channel is inert (its select case simply never fires), which
// is exactly what "disabled" should look like from the server's side.
func TestDisabledWorkerKeepsAnOpenIdleReadsChannel(t *testing.T) {
	t.Setenv("SCANNER_FLAGREAD", "0")
	h := newHarness(t) // constructed under the kill switch

	entries := make(chan core.ScanEntry, 1)
	w := New(Config{
		Entries: entries,
		RowFor:  h.rowFor, UsesFlags: h.usesFlags, FlagFor: h.flagFor,
		BarsFor: h.barsFor, Reader: h.reader, Cache: h.arch,
		Now: h.nowFn,
	})
	if w.enabled {
		t.Fatal("SCANNER_FLAGREAD=0 must disable the worker")
	}
	if w.Reads() == nil {
		t.Fatal("a disabled worker still owes the server a non-nil Reads() channel")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	// An entry that would otherwise be a textbook paid read.
	entries <- core.ScanEntry{ScanID: "setups", Sym: "TIGHT", At: h.nowFn().UnixMilli(), Last: 12.6}

	select {
	case v, ok := <-w.Reads():
		if !ok {
			t.Fatal("disabled worker closed Reads(): the broadcast select would spin on it forever")
		}
		t.Fatalf("disabled worker emitted a read: %+v", v)
	case <-time.After(150 * time.Millisecond):
		// Open, empty and silent — the inert shape the broadcast loop needs.
	}
	if h.reader.n() != 0 || h.arch.saveCount() != 0 {
		t.Fatalf("disabled worker spent money: %d reads, %d saves", h.reader.n(), h.arch.saveCount())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("disabled Run should exit cleanly on ctx cancel, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("disabled Run did not return after ctx cancel")
	}
	// Still open after Run returned: shutdown must not close it either.
	select {
	case _, ok := <-w.Reads():
		if !ok {
			t.Fatal("Run closed Reads() on the way out; the server may still be selecting on it")
		}
	default:
	}
}

func TestDefaultsAreApplied(t *testing.T) {
	h := newHarness(t)
	w := New(Config{
		RowFor: h.rowFor, UsesFlags: h.usesFlags, FlagFor: h.flagFor,
		BarsFor: h.barsFor, Reader: h.reader, Cache: h.arch,
	})
	if w.cfg.MaxPerSym <= 0 || w.cfg.MaxPerDay <= 0 || w.cfg.Cooldown <= 0 ||
		w.cfg.MinScore <= 0 || w.cfg.Now == nil {
		t.Fatalf("every cost control must have a default: %+v", w.cfg)
	}
	if !w.enabled {
		t.Fatal("a fully wired worker is enabled unless SCANNER_FLAGREAD=0")
	}
}
