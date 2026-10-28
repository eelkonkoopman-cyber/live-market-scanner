// Package flagread turns scan entries into Claude "setup reads": when a symbol
// enters a scan that FILTERS ON the nightly bull-flag fields, it pulls that
// symbol's row and daily bars, asks Claude to score and explain the setup, then
// caches, persists and broadcasts the read so the panel has it waiting.
//
// The deterministic pattern pass (internal/pattern) already did the
// whole-market filtering for free, so Claude only ever sees a shortlist. On top
// of that this worker layers autosum's cost controls, for the same reason —
// every call is real money on the user's account:
//
//   - an eligibility gate ordered CHEAPEST FIRST, so a symbol is never found
//     ineligible after money was spent on it;
//   - a per-symbol cooldown with a single check-and-mark, so concurrent entries
//     for one symbol can't double-run;
//   - per-symbol and global daily budgets that reset at ET midnight;
//   - a cache-first lookup on (sym, nightly AsOf) whose hits are refunded, and
//     which FAILS CLOSED: a lookup that errored is not a miss;
//   - a refund on every path that did NOT bill a call.
//
// Disable entirely with SCANNER_FLAGREAD=0.
package flagread

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
	"github.com/eelkonkoopman/marketscanner/internal/summarize"
)

// Reader abstracts summarize.Service for tests (same role as autosum.Summarizer).
type Reader interface {
	ReadSetup(ctx context.Context, in core.SetupContext) (core.SetupRead, error)
	Model() string
}

// Config wires the worker to the scanner, the daily-bar store, Claude and the
// read archive. Every callback and Cache is required; New disables the worker
// (with a log line) rather than panicking if one is missing.
type Config struct {
	// Entries is the scanner's entry stream — every symbol that newly enters
	// ANY scan. Most of them are dropped by UsesFlags.
	Entries <-chan core.ScanEntry

	// RowFor is scan.Scanner.Row: the live merged metrics for one symbol,
	// carrying the overlaid flag fields the gate reads (and the live
	// last/chgDay/rvol/float the read is grounded in).
	RowFor func(sym string) (core.Row, bool)
	// UsesFlags is scan.Scanner.ScanUsesFlags: does the ENTERING scan filter on
	// the flag fields? This is the first gate — user intent, not row shape.
	UsesFlags func(scanID string) bool
	// FlagFor is scan.Scanner.FlagFor: the whole nightly classification. The row
	// overlay carries only the seven filterable numbers; the read needs AsOf
	// (its cache key) and BaseHigh (the level to describe) too.
	FlagFor func(sym string) (core.FlagRef, bool)
	// BarsFor loads the recent daily bars behind the geometry (persist's
	// TrendRepo.BarsFor, bound to a lookback), oldest → newest.
	BarsFor func(ctx context.Context, sym string) ([]core.DailyBar, error)

	Reader Reader
	Cache  core.SetupArchive

	MaxPerSym int           // reads per symbol per day (default 2: one, plus one retry)
	MaxPerDay int           // global reads per day (default 40)
	Cooldown  time.Duration // min gap between runs for the same symbol (default 30m)
	MinScore  float64       // minimum FlagScore worth reading (default 60)
	Now       func() time.Time
}

type Worker struct {
	cfg   Config
	reads chan core.SetupReadMsg

	mu      sync.Mutex
	lastRun map[string]time.Time // sym -> last run attempt
	symDone map[string]int       // sym -> reads produced today
	dayDone int                  // global reads produced today
	day     string               // ET date the counters belong to

	loc     *time.Location
	enabled bool

	// off latches when Claude reports the structured-output backend is missing
	// (summarize.ErrSetupReadUnavailable). That is decided once, when the
	// service resolves its backend, so it cannot change mid-process: retrying
	// it on every entrant would be a guaranteed-useless round trip forever.
	// Atomic because it is set from a read goroutine and read by every other.
	off atomic.Bool
}

// runTimeout bounds one read attempt end to end: a bar query plus
// summarize.ReadSetup's own 60s timeout, with room for its concurrency
// semaphore. Nothing here is time-critical (a setup read lands in a panel, not
// an ear), but an attempt that never returns would hold its budget forever.
const runTimeout = 2 * time.Minute

func New(cfg Config) *Worker {
	if cfg.MaxPerSym <= 0 {
		cfg.MaxPerSym = envInt("SCANNER_FLAGREAD_MAX", 2)
	}
	if cfg.MaxPerDay <= 0 {
		cfg.MaxPerDay = envInt("SCANNER_FLAGREAD_DAILY", 40)
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 30 * time.Minute
	}
	if cfg.MinScore <= 0 {
		cfg.MinScore = envFloat("SCANNER_FLAGREAD_MIN_SCORE", 60)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.UTC
	}
	w := &Worker{
		cfg:     cfg,
		reads:   make(chan core.SetupReadMsg, 64),
		lastRun: map[string]time.Time{},
		symDone: map[string]int{},
		loc:     loc,
		enabled: os.Getenv("SCANNER_FLAGREAD") != "0",
	}
	// A missing dependency is a wiring bug, and the failure mode we refuse is
	// "panic in a read goroutine at the open". Stay disabled and say so.
	if cfg.RowFor == nil || cfg.UsesFlags == nil || cfg.FlagFor == nil ||
		cfg.BarsFor == nil || cfg.Reader == nil || cfg.Cache == nil {
		logx.L().Warn("flag setup reads disabled: incomplete config")
		w.enabled = false
	}
	return w
}

// Reads carries finished setup reads (the server broadcasts them as setup.read).
func (w *Worker) Reads() <-chan core.SetupReadMsg { return w.reads }

func (w *Worker) Run(ctx context.Context) error {
	if !w.enabled {
		logx.L().Info("flag setup reads disabled (SCANNER_FLAGREAD=0)")
		<-ctx.Done()
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-w.cfg.Entries:
			// handle runs off-loop because its first two gate steps are Scanner
			// ctrl round-trips: blocking this loop on the scanner's goroutine
			// would let a busy scanner back the entry channel up. Concurrency is
			// safe by construction — shouldRun's check-and-mark is what decides
			// whether any work happens.
			go w.handle(ctx, e)
		}
	}
}

// handle is the whole per-entry path: eligibility gate, then the read.
func (w *Worker) handle(ctx context.Context, e core.ScanEntry) {
	row, ok := w.gate(e)
	if !ok {
		return
	}
	if !w.shouldRun(e.Sym) {
		return
	}
	w.runSym(ctx, e.Sym, row)
}

// gate answers "is this entrant worth spending on", cheapest check first, so
// budget is never spent to discover a symbol was ineligible. Nothing here costs
// money or touches the counters.
func (w *Worker) gate(e core.ScanEntry) (core.Row, bool) {
	if w.off.Load() {
		return core.Row{}, false // no backend for structured reads; see runSym
	}
	// The scan the symbol entered must be one the user built AROUND the flag
	// geometry. A momentum scan that happens to catch a coiling ticker is not
	// an instruction to spend money on it.
	if !w.cfg.UsesFlags(e.ScanID) {
		return core.Row{}, false
	}
	row, ok := w.cfg.RowFor(e.Sym)
	if !ok {
		return core.Row{}, false
	}
	// Null is a NaN, so `row.FlagScore < w.cfg.MinScore` below is ALREADY false
	// for an unclassified symbol — every comparison against NaN is false, which
	// would make the `<` test pass it through. The explicit IsNull checks are
	// what actually reject it, and they are also the honest statement of the
	// rule: no measurement means nothing to read, however the comparisons below
	// are later rewritten.
	if core.IsNull(row.FlagScore) || core.IsNull(row.FlagState) {
		return core.Row{}, false
	}
	if row.FlagState < 1 || row.FlagScore < w.cfg.MinScore {
		return core.Row{}, false
	}
	return row, true
}

// shouldRun applies the per-symbol cooldown + daily budgets (single check-and-
// mark so concurrent entries for one symbol can't double-run).
func (w *Worker) shouldRun(sym string) bool {
	now := w.cfg.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	if d := now.In(w.loc).Format("2006-01-02"); d != w.day {
		w.day = d
		w.symDone = map[string]int{}
		w.dayDone = 0
	}
	if w.dayDone >= w.cfg.MaxPerDay || w.symDone[sym] >= w.cfg.MaxPerSym {
		return false
	}
	if now.Sub(w.lastRun[sym]) < w.cfg.Cooldown {
		return false
	}
	w.lastRun[sym] = now
	return true
}

// runSym produces one setup read for sym: cache first, Claude only on a miss.
func (w *Worker) runSym(ctx context.Context, sym string, row core.Row) {
	rctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()

	// The full nightly classification. Its AsOf is the cache key, so a symbol
	// earns exactly one read per nightly classification and every re-entry that
	// day is free.
	flag, ok := w.cfg.FlagFor(sym)
	if !ok {
		return // classified on the row but not in the reference: nothing to read
	}
	// The live overlay promotes state 1 → 2 the moment price clears BaseHigh
	// (scan.overlayFlags). Carry THAT state into the read: the stored reference
	// still says "coiling" because the breakout happened after last night.
	flag.State = row.FlagState

	if !w.budgetOne(sym) {
		return
	}
	cached, hit, cerr := w.cfg.Cache.GetSetupRead(rctx, sym, flag.AsOf)
	switch {
	case cerr != nil:
		// FAIL CLOSED. A failed lookup is NOT a miss: it means we do not know
		// whether this (sym, AsOf) has already been paid for, and the honest
		// answer to "did we already buy this" being unknown is don't buy it
		// again. Treating it as a miss is the one unbounded leak in the design
		// — a broken archive fails SaveSetupRead too, so the cache never
		// populates, so EVERY entrant re-buys every cooldown, every day,
		// forever, with nothing in the log to explain the bill. Skipping costs
		// one read; the next entry past the cooldown retries the lookup free.
		logx.L().Warn("setup read skipped: cache lookup failed", "sym", sym, "err", cerr)
		w.unbudgetOne(sym)
		return
	case hit:
		w.emit(cached)
		w.unbudgetOne(sym) // cache hits are free — don't count them
		return
	}

	bars, err := w.cfg.BarsFor(rctx, sym)
	if err != nil || len(bars) == 0 {
		// No chart to send means no read worth paying for. Nothing was billed.
		logx.L().Warn("setup read skipped: no daily bars", "sym", sym, "err", err)
		w.unbudgetOne(sym)
		return
	}

	read, rerr := w.cfg.Reader.ReadSetup(rctx, core.SetupContext{
		Sym: sym, Flag: flag, Bars: bars,
		Last: row.Last, ChgDay: row.ChgDay, Rvol: row.Rvol, Float: row.Float,
	})
	switch {
	case errors.Is(rerr, summarize.ErrSetupReadUnavailable):
		// Permanent for this process: summarize resolves its backend once, so
		// no later entrant can do better. Refund (nothing was billed), latch the
		// worker off so the gate drops every future entry, and log exactly once.
		w.unbudgetOne(sym)
		if w.off.CompareAndSwap(false, true) {
			logx.L().Warn("flag setup reads unavailable; disabled for this process", "err", rerr)
		}
		return
	case rerr != nil:
		// summarize.ReadSetup's contract: a non-nil error means NO call was
		// billed (backend pick failed, or the context gave up on the semaphore).
		logx.L().Warn("setup read failed", "sym", sym, "err", rerr)
		w.unbudgetOne(sym)
		return
	}

	// From here a call WAS billed, even when read.Err is set (Quality 0), so the
	// budget stays spent: a backend erroring on every call must cost at most
	// MaxPerDay failures, not an unbounded stream of them. A failed read is
	// still broadcast (the panel shows why) but NOT persisted — storing it would
	// freeze the failure in as this AsOf's cached answer, and the per-symbol
	// budget is what limits the retries instead.
	if read.Err == "" {
		if err := w.cfg.Cache.SaveSetupRead(rctx, read); err != nil {
			logx.L().Warn("setup read save failed", "sym", sym, "err", err)
		}
	}
	w.emit(read)
}

// budgetOne reserves one read slot for sym; false when budgets are spent.
func (w *Worker) budgetOne(sym string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dayDone >= w.cfg.MaxPerDay || w.symDone[sym] >= w.cfg.MaxPerSym {
		return false
	}
	w.dayDone++
	w.symDone[sym]++
	return true
}

func (w *Worker) unbudgetOne(sym string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Floor at zero: a refund can land after shouldRun's ET-midnight counter
	// reset (the reservation was taken on the OLD day), and a negative counter
	// would grant the new day extra budget.
	if w.dayDone > 0 {
		w.dayDone--
	}
	if w.symDone[sym] > 0 {
		w.symDone[sym]--
	}
}

func (w *Worker) emit(m core.SetupReadMsg) {
	select {
	case w.reads <- m:
	default: // consumer behind; reads are advisory, drop
	}
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func envFloat(name string, def float64) float64 {
	if v := os.Getenv(name); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	return def
}
