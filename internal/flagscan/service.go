package flagscan

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
	"github.com/eelkonkoopman/marketscanner/internal/emaflag"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

// symState is everything the service knows about one candidate. It is touched
// only by the loop goroutine (the fetch goroutines work on copies and report
// back through service.results), so it carries no locks.
type symState struct {
	sym    string
	gen    uint64  // admission generation: a fetch result for an older admission is stale
	seq    uint64  // admission order, the tie-break for who fetches first
	pmVolX float64 // the multiple at the last candidate refresh (status/debug only)

	// bars is the 1-minute series, ascending. Before the first REST load it is
	// the live-only series the tap has folded so far; the load merges the two
	// (mergeREST). Never evaluated until loaded.
	bars       []core.Bar
	loaded     bool
	lastFoldAt time.Time // when the newest live aggregate changed bars (mergeREST's "is live newer than the request")

	// Evaluation bookkeeping: re-evaluate when the bars changed, or when the last
	// bar has closed since the last evaluation (a clock-only change).
	dirty      bool
	evalClosed bool // the last evaluation treated the last bar as closed
	evaluated  bool // ref is a current verdict (false = absent from the published map)
	broken     bool // the engine panicked on these bars: retried once when the last bar closes, then not until they change
	primed     bool // an evaluation of this admission has produced a verdict: the next one is EvalUpdate.Primed (the first replayed the REST history)
	ref        core.EmaFlagRef

	// Fetch scheduling: due is when the next REST call may start (admission,
	// retry back-off, the next resync); fails counts consecutive failures.
	fetching bool
	fails    int
	due      time.Time

	// GREEN DILDO -> 9 EMA PULLBACK (alert 2). hot: tracked ONLY because the
	// engine saw a green dildo on it (neither ranked under the cap nor pinned):
	// published HotOnly and never handed to OnEval. earlyDue: the early resync
	// that makes the dildo minute's REST bar final (zero = none pending); once
	// it has come it is dispatched ahead of ordinary resyncs. dildo: the rule's
	// verdict over these bars at the last evaluation. parityMs: an engine dildo
	// bar still to be checked against the chart rule (0 = none), parityOK once
	// the bars hold that minute final. tapFrom: when the tap began capturing
	// the symbol (its admission) - a minute that began earlier is only partly
	// in the live bars; restAt: when the last successful REST fetch started
	// (zero = none yet) - see holdsMinute.
	hot      bool
	earlyDue time.Time
	dildo    dildopb.Result
	parityMs int64
	parityOK bool
	tapFrom  time.Time
	restAt   time.Time
}

// hotEntry is one HOT name: the latest engine dildo bar (start, ms) and when
// its hold ends.
type hotEntry struct {
	barMs int64
	until time.Time
}

// earlyResyncSlack: a fetch that started at least EarlyResync - this after the
// dildo bar's end already returned the minute final (REST settles within ~15 s).
const earlyResyncSlack = 5 * time.Second

// fetchResult is what a fetch goroutine reports back to the loop.
type fetchResult struct {
	sym     string
	gen     uint64
	started time.Time // the loop's clock when the call was launched
	bars    []core.Bar
	err     error
}

// candResult is what the candidate goroutine reports back. ok is false when
// the Candidates call panicked: the loop then keeps the current set rather
// than reading "no candidates" into an engine hiccup.
type candResult struct {
	entries []core.PmVolEntry
	ok      bool
}

// service is the loop's state. See Run for the shape.
type service struct {
	cfg    Config
	tm     tuning
	ctx    context.Context
	aggs   <-chan core.Agg
	params emaflag.Params
	// eval is the engine call. A field only so a test can make it panic: the port
	// has no input anyone has found that does, and evalOne's recover (a bug in the
	// port must cost one symbol's verdict, never the live process) needs a test.
	eval func(bars []core.Bar, tfMs, nowMs int64, p emaflag.Params) emaflag.Result

	syms map[string]*symState
	seq  uint64
	gen  uint64

	results      chan fetchResult
	candCh       chan candResult
	candInflight bool
	inflight     int // fetch goroutines running (loop-owned counter)
	wg           sync.WaitGroup

	tap       []string // the symbol set last handed to SetTap (sorted); nil = the tap's initial, empty state
	nextCand  time.Time
	nextLog   time.Time
	mapDirty  bool // a verdict appeared, changed or vanished since the last publish
	pubNonNil bool // the last publish carried at least one symbol

	qualified  int   // candidates over the threshold at the last refresh (before the cap)
	beyondCap  int   // of which not evaluated because of the cap
	lastBeyond int   // beyondCap as last logged, so only a change is logged
	lastDrops  int64 // tap drops as of the last status line

	// Config.Pinned bookkeeping. lastEntries is the candidate list the last
	// applyCandidates reconciled with: a change in the pinned set re-applies it
	// at once (syncPins) instead of waiting for the next 30 s refresh. pins is
	// the (sorted, de-duplicated) pinned set that reconcile used.
	lastEntries []core.PmVolEntry
	pins        []string
	pinPanics   int64       // Config.Pinned panics so far (log throttling)
	evalPanics  int64       // Config.OnEval panics so far (log throttling)
	todo        []*symState // evaluate's scratch list, reused every tick

	// GREEN DILDO (Config.Hot). hot is the HOT set (symbol -> its latest dildo
	// and hold); the counters feed the status line: hot names evicted for
	// MaxHot, engine dildos the chart rule did not find on the final bars
	// (dildoParityMiss), published touch/pullback bars a resync moved after the
	// fact, and dildo-rule panics.
	hotCh           <-chan core.DildoHit
	dparams         dildopb.Params
	evalDildo       func(bars []core.Bar, tfMs, nowMs int64, p dildopb.Params, seen []int64, tolPct float64) dildopb.Result // a field for the same reason as eval
	engineDildo     map[string][]int64                                                                                      // symbol -> the latest bars (start ms) the engine called a dildo; judged with dildoConfirmTolerancePct
	hot             map[string]hotEntry
	hotEvictions    int64
	dildoParityMiss int64
	dildoRewrites   int64
	dildoPanics     int64
	dildoHookPanics int64 // Config.OnDildo panics so far (log throttling)

	// The tap's drop count as noteDrops last read it (the first read is the
	// baseline), and when it last saw the count rise: a minute that began
	// before that may be missing prints in every symbol's live bars.
	dropsSeen int64
	dropsRead bool
	dropAt    time.Time
}

func newServiceState(cfg Config, tm tuning) *service {
	if tm.workers < 1 {
		tm.workers = 1
	}
	if tm.hotHold <= 0 {
		tm.hotHold = HotHold
	}
	if tm.maxHot < 1 {
		tm.maxHot = MaxHot
	}
	if tm.earlyResync <= 0 {
		tm.earlyResync = EarlyResync
	}
	return &service{
		cfg: cfg, tm: tm, aggs: cfg.Aggs, params: cfg.Params, eval: emaflag.Evaluate,
		syms:        make(map[string]*symState),
		results:     make(chan fetchResult, tm.workers),
		candCh:      make(chan candResult, 1),
		hotCh:       cfg.Hot,
		dparams:     cfg.Dildo,
		evalDildo:   dildopb.EvaluateTolerant,
		hot:         make(map[string]hotEntry),
		engineDildo: make(map[string][]int64),
	}
}

// ─── The loop ────────────────────────────────────────────────────────────

// aggBatch bounds how many tap aggregates one wake-up folds before looking at
// the other channels again: enough to amortise the select under a burst, small
// enough that a flood cannot starve the tick.
const aggBatch = 256

func (s *service) run(ctx context.Context) error {
	s.ctx = ctx
	// A fetch goroutine ends as soon as its Bars call returns (the call honours
	// ctx, which is cancelled by the time we get here), so this wait is short and
	// is what lets a test - and process shutdown - assert nothing was leaked.
	defer s.wg.Wait()

	if ctx.Err() != nil {
		return nil // cancelled before it began: start no work at all
	}
	tk := time.NewTicker(s.tm.evalEvery)
	defer tk.Stop()
	s.tick(s.cfg.Now()) // the first candidate query starts at once, not a tick later
	for {
		select {
		case <-ctx.Done():
			return nil
		case a, ok := <-s.aggs:
			if !ok {
				s.aggs = nil // a closed tap would spin this select; a nil channel never fires
				continue
			}
			s.foldBatch(a)
		case r := <-s.results:
			s.handleFetch(r, s.cfg.Now())
		case c := <-s.candCh:
			s.handleCandidates(c, s.cfg.Now())
		case hit, ok := <-s.hotCh:
			if !ok {
				s.hotCh = nil // a closed hand-off would spin this select
				continue
			}
			s.handleHot(hit, s.cfg.Now())
		case <-tk.C:
			s.tick(s.cfg.Now())
		}
	}
}

// foldBatch folds the aggregate that woke the loop plus whatever else is
// already waiting on the tap (up to aggBatch), reading the clock once.
func (s *service) foldBatch(first core.Agg) {
	now := s.cfg.Now()
	s.handleAgg(first, now)
	for i := 1; i < aggBatch; i++ {
		select {
		case a, ok := <-s.aggs:
			if !ok {
				s.aggs = nil
				return
			}
			s.handleAgg(a, now)
		default:
			return
		}
	}
}

// tick is the once-per-EvalEvery work: start the candidate query when it is
// due, start any REST fetch that is due, evaluate what changed and publish,
// and write the status line. Every piece is cheap and none of them waits on
// the network.
func (s *service) tick(now time.Time) {
	if !s.candInflight && !now.Before(s.nextCand) {
		s.startCandidates(now)
	}
	s.noteDrops(now)
	s.expireHot(now)
	s.syncPins(now)
	s.dispatch(now)
	s.evaluate(now)
	if !now.Before(s.nextLog) {
		s.logStatus(now)
	}
}

// ─── Candidates ──────────────────────────────────────────────────────────

// startCandidates asks the engine for the pre-market volume leaders from a
// goroutine of its own: the answer is a fan-out into every engine shard and
// blocks until they all reply, which must never stall the aggregate tap (its
// buffer is a minute of slack, but a stalled loop would also hold up every
// verdict) - and which cannot be interrupted if the engine stops mid-query.
func (s *service) startCandidates(now time.Time) {
	s.candInflight = true
	s.nextCand = now.Add(s.tm.candidateEvery)
	go func() {
		var res candResult
		func() {
			defer func() {
				if r := recover(); r != nil {
					logx.L().Error("flagscan: candidate query panicked; keeping the current set", "panic", r)
				}
			}()
			// Uncapped on purpose: the cap is applied (and reported) in
			// handleCandidates, which needs the true count of what qualified.
			res.entries = s.cfg.Candidates(s.tm.minX, 0)
			res.ok = true
		}()
		select {
		case s.candCh <- res: // capacity 1, one query in flight: never blocks
		case <-s.ctx.Done():
		}
	}()
}

func (s *service) handleCandidates(res candResult, now time.Time) {
	s.candInflight = false
	if !res.ok {
		return
	}
	s.applyCandidates(res.entries, now)
	// The tap is already pointing at the new set, so the newcomers' backfill
	// can start right now rather than a tick from now.
	s.dispatch(now)
}

// applyCandidates reconciles the tracked set with a fresh candidate list: rank
// it, apply the cap (reporting what it cut), point the tap at the new set
// BEFORE anything is queued, forget the departed (their state, their verdict -
// the next publish drops them from the map) and queue the newcomers' backfill.
//
// Admission is by current rank, with no hysteresis: a name sitting on the cap
// boundary can swap in and out as the multiples shuffle. That costs a REST
// backfill per swap and nothing else - a re-admitted symbol's REST history
// reproduces its state exactly (same bars in, same rules) - and a hysteresis
// rule would make the cap mean "the 80 you saw first" instead of "the 80
// strongest".
//
// The one exception to "by rank" is Config.Pinned: a pinned symbol is tracked
// whatever its rank, whether the cap cut it or it is not even over the
// threshold, on top of the cap (the cap keeps meaning "80 by strength"; the
// handful of pins - a trader's open positions - are extra). The pinned set is
// read here, at apply time, so it is never older than the reconcile it feeds.
func (s *service) applyCandidates(entries []core.PmVolEntry, now time.Time) {
	s.lastEntries = entries
	best := make(map[string]float64, len(entries))
	for _, e := range entries {
		if e.Sym == "" || math.IsNaN(e.PmVolX) {
			continue
		}
		if x, ok := best[e.Sym]; !ok || e.PmVolX > x {
			best[e.Sym] = e.PmVolX
		}
	}
	ranked := make([]core.PmVolEntry, 0, len(best))
	for sym, x := range best {
		ranked = append(ranked, core.PmVolEntry{Sym: sym, PmVolX: x})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].PmVolX != ranked[j].PmVolX {
			return ranked[i].PmVolX > ranked[j].PmVolX
		}
		return ranked[i].Sym < ranked[j].Sym
	})
	s.qualified = len(ranked)
	var cut []core.PmVolEntry // what the cap leaves out, weakest last
	if s.tm.maxCandidates > 0 && len(ranked) > s.tm.maxCandidates {
		cut = ranked[s.tm.maxCandidates:]
		ranked = ranked[:s.tm.maxCandidates]
	}

	want := make(map[string]float64, len(ranked))
	for _, e := range ranked {
		want[e.Sym] = e.PmVolX
	}
	// The pins, on top of the cap. A pinned symbol that no candidate list
	// mentions carries a multiple of 0 (status/debug only; it sorts last).
	s.pins = s.readPins()
	var pinExtra []string // pinned, but not among the ranked: sorted, because s.pins is
	for _, p := range s.pins {
		if _, ok := want[p]; !ok {
			want[p] = best[p]
			pinExtra = append(pinExtra, p)
		}
	}
	// The cap only "does not evaluate" what no pin saved.
	s.beyondCap = 0
	for _, e := range cut {
		if _, kept := want[e.Sym]; !kept {
			s.beyondCap++
		}
	}
	if s.beyondCap != s.lastBeyond {
		if s.beyondCap > 0 {
			logx.L().Warn("flagscan: more symbols qualify than the cap; the weakest are not evaluated",
				"qualified", s.qualified, "cap", s.tm.maxCandidates, "beyondCap", s.beyondCap, "minPmVolX", s.tm.minX)
		} else {
			logx.L().Info("flagscan: candidates are back under the cap", "qualified", s.qualified, "cap", s.tm.maxCandidates)
		}
		s.lastBeyond = s.beyondCap
	}

	// The HOT names (a green dildo the engine just saw, Config.Hot), on top of
	// the cap and the pins and below the threshold. A symbol only they keep is
	// hot-only: published HotOnly (the scanner fills its dildo fields only) and
	// never handed to OnEval.
	var hotExtra []string
	for _, sym := range s.hotSyms() {
		if _, ok := want[sym]; !ok {
			want[sym] = best[sym]
			hotExtra = append(hotExtra, sym)
		}
	}

	// added keeps RANK order (strongest multiple first): admission order is the
	// fetch tie-break, so the best candidates get their history first; a pinned
	// symbol that is no candidate comes after them (it is already holding a
	// position, so its data is needed, but the name with a live setup is not
	// kept waiting for it); hot names come last.
	var added, dropped []string
	for _, e := range ranked {
		if _, ok := s.syms[e.Sym]; !ok {
			added = append(added, e.Sym)
		}
	}
	for _, p := range pinExtra {
		if _, ok := s.syms[p]; !ok {
			added = append(added, p)
		}
	}
	for _, h := range hotExtra {
		if _, ok := s.syms[h]; !ok {
			added = append(added, h)
		}
	}
	for sym := range s.syms {
		if _, ok := want[sym]; !ok {
			dropped = append(dropped, sym)
		}
	}
	sort.Strings(dropped)

	// The tap first, so every second from here on is captured; only then are
	// the newcomers made fetchable (the caller dispatches after this returns).
	if tapSyms := sortedKeys(want); !equalStrings(tapSyms, s.tap) {
		s.cfg.SetTap(tapSyms)
		s.tap = tapSyms
	}
	for _, sym := range dropped {
		if s.syms[sym].evaluated {
			s.mapDirty = true // its verdict must leave the published map
		}
		delete(s.syms, sym)
	}
	for _, sym := range added {
		s.gen++
		s.seq++
		s.syms[sym] = &symState{sym: sym, gen: s.gen, seq: s.seq, due: now, tapFrom: now}
	}
	for sym, x := range want {
		s.syms[sym].pmVolX = x
	}
	var hotOnly map[string]bool // nil (all false) when no hot name is extra
	if len(hotExtra) > 0 {
		hotOnly = make(map[string]bool, len(hotExtra))
		for _, h := range hotExtra {
			hotOnly[h] = true
		}
	}
	for sym, st := range s.syms {
		h := hotOnly[sym]
		if h == st.hot {
			continue
		}
		st.hot = h
		if h {
			// A candidate (or pin) that only the hot hold keeps now: OnEval stops
			// for it, so a later candidacy must start unprimed, exactly like a
			// fresh admission (its events since are history to the consumer).
			st.primed = false
		}
		if st.evaluated {
			st.dirty = true // HotOnly rides on the ref: re-evaluate and republish
		}
	}
	if len(added) > 0 || len(dropped) > 0 {
		logx.L().Info("flagscan: candidates changed",
			"candidates", len(s.syms), "added", len(added), "dropped", len(dropped), "qualified", s.qualified,
			"pinned", len(s.pins), "hotOnly", len(hotExtra))
		logx.L().Debug("flagscan: candidate delta", "added", added, "dropped", dropped)
	}
}

// ─── HOT names (GREEN DILDO -> 9 EMA PULLBACK) ───────────────────────────

// handleHot admits one engine green dildo's symbol as a HOT name for HotHold
// from the dildo bar's end (a newer dildo extends the hold), evicting the entry
// that expires first when the set outgrows MaxHot, and reconciles at once
// through the one path every admission takes (applyCandidates: the tap is
// pointed at the symbol BEFORE its backfill is queued; a symbol already
// tracked and loaded fetches nothing when its bars hold every print of the
// dildo minute - see holdsMinute). Runs on the loop goroutine; never calls
// into the engine.
func (s *service) handleHot(hit core.DildoHit, now time.Time) {
	if hit.Sym == "" || hit.BarMs <= 0 {
		return
	}
	s.notifyEngineDildo(hit, now)
	until := time.UnixMilli(hit.BarMs + minuteMs).Add(s.tm.hotHold)
	if !until.After(now) {
		return // a hit whose hold has already run out (a hand-off that lagged by half an hour): nothing to watch
	}
	s.noteEngineDildo(hit.Sym, hit.BarMs)
	he, had := s.hot[hit.Sym]
	if !had || until.After(he.until) {
		he.until = until
	}
	if hit.BarMs > he.barMs {
		he.barMs = hit.BarMs
	}
	s.hot[hit.Sym] = he
	if len(s.hot) > s.tm.maxHot {
		victim := ""
		var first time.Time
		for _, sym := range s.hotSyms() { // sorted: a tie evicts deterministically
			if e := s.hot[sym]; victim == "" || e.until.Before(first) {
				victim, first = sym, e.until
			}
		}
		delete(s.hot, victim)
		s.hotEvictions++
		logx.L().Warn("flagscan: more hot dildo names than MaxHot; the one expiring first is dropped",
			"evicted", victim, "hot", len(s.hot), "maxHot", s.tm.maxHot, "evictions", s.hotEvictions)
	}
	s.applyCandidates(s.lastEntries, now)
	if st := s.syms[hit.Sym]; st != nil {
		// The chart rule must find this very bar once the bars hold it final: at
		// once for a loaded symbol whose bars already hold the whole minute,
		// after the early resync for one still waiting for its REST history -
		// and for a LOADED one whose tap began inside the minute (admitted as a
		// candidate while the dildo printed: its bar is the tap's fragment, or a
		// REST snapshot of a minute still forming) or dropped prints since it
		// began. That one is resynced early too (a resync already in flight
		// schedules it when it lands, handleFetch); until then its fragment
		// would show no anchor and count a false parity miss.
		s.noteDrops(now)
		whole := st.loaded && s.holdsMinute(st, hit.BarMs)
		st.parityMs, st.parityOK = hit.BarMs, whole
		if st.loaded && !whole && !st.fetching {
			s.earlyResync(st, hit.BarMs, now)
		}
	}
	s.dispatch(now) // a newly admitted symbol's backfill starts now, not a tick later
}

// holdsMinute reports whether a LOADED symbol's bars hold the 1-minute bar
// that starts at barMs complete: a REST fetch started late enough to return it
// final (EarlyResync - earlyResyncSlack after its end, handleFetch's rule), or
// a tap that was capturing the symbol before the minute began and has dropped
// nothing since.
func (s *service) holdsMinute(st *symState, barMs int64) bool {
	barStart := time.UnixMilli(barMs)
	if !st.restAt.IsZero() && !st.restAt.Before(barStart.Add(time.Minute+s.tm.earlyResync-earlyResyncSlack)) {
		return true
	}
	if st.tapFrom.After(barStart) {
		return false // admitted after the minute began: the tap saw only its tail
	}
	return s.dropAt.IsZero() || s.dropAt.Before(barStart)
}

// earlyResync makes a hot symbol's next REST call the early resync - at the
// dildo bar's end + EarlyResync, or a second from now if that has passed -
// unless one is due sooner anyway (which, landing before that, schedules this
// one itself: handleFetch).
func (s *service) earlyResync(st *symState, barMs int64, now time.Time) {
	d := time.UnixMilli(barMs + minuteMs).Add(s.tm.earlyResync)
	if soonest := now.Add(time.Second); d.Before(soonest) {
		d = soonest
	}
	if d.Before(st.due) {
		st.due, st.earlyDue = d, d
	}
}

// noteDrops reads the tap's drop count (Config.Drops, an atomic load) and
// records when it rose. Once a tick and on each hot hit, never per aggregate.
func (s *service) noteDrops(now time.Time) {
	if s.cfg.Drops == nil {
		return
	}
	d := s.cfg.Drops()
	if s.dropsRead && d > s.dropsSeen {
		s.dropAt = now
	}
	s.dropsSeen, s.dropsRead = d, true
}

// expireHot ends every hold that has run out and reconciles: a symbol only the
// hot set kept leaves (unless it is a candidate or pinned by now).
func (s *service) expireHot(now time.Time) {
	expired := false
	for sym, he := range s.hot {
		if !now.Before(he.until) {
			delete(s.hot, sym)
			expired = true
		}
	}
	if expired {
		s.applyCandidates(s.lastEntries, now)
	}
}

// hotSyms is the hot set's symbols, sorted.
func (s *service) hotSyms() []string {
	if len(s.hot) == 0 {
		return nil
	}
	out := make([]string, 0, len(s.hot))
	for sym := range s.hot {
		out = append(out, sym)
	}
	sort.Strings(out)
	return out
}

// readPins is Config.Pinned, normalised: blank names dropped, duplicates
// merged, sorted. A callback that panics must not cost the loop - or, worse,
// silently un-pin a symbol whose position is open - so the previous answer
// stays in force and the panic is logged (the first, then every 100th).
func (s *service) readPins() []string {
	if s.cfg.Pinned == nil {
		return nil
	}
	var raw []string
	ok := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				s.pinPanics++
				if s.pinPanics == 1 || s.pinPanics%100 == 0 {
					logx.L().Error("flagscan: Config.Pinned panicked; keeping the previous pinned set",
						"panic", r, "panics", s.pinPanics, "kept", len(s.pins))
				}
			}
		}()
		raw = s.cfg.Pinned()
		ok = true
	}()
	if !ok {
		return s.pins
	}
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		if p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	uniq := out[:0]
	for i, p := range out {
		if i == 0 || p != out[i-1] {
			uniq = append(uniq, p)
		}
	}
	return uniq
}

// syncPins applies a change in the pinned set without waiting for the next
// candidate refresh: a position adopted on a symbol that is not tracked gets
// its tap and backfill on this tick, and a released one that is no candidate
// leaves on this tick. Re-applying the last candidate list is exactly the
// reconcile a refresh would do with the same list, so there is no second code
// path to keep in step with the first.
func (s *service) syncPins(now time.Time) {
	if s.cfg.Pinned == nil {
		return
	}
	if equalStrings(s.readPins(), s.pins) {
		return
	}
	s.applyCandidates(s.lastEntries, now)
}

// ─── Live aggregates ─────────────────────────────────────────────────────

// handleAgg folds one per-second aggregate into its symbol's series. A symbol
// that is not (or no longer) a candidate is ignored: the tap is replaced
// atomically a few milliseconds after the set changes, so a straggler or two
// is normal. A symbol still waiting for its REST history folds into the
// live-only series - the buffer the first load merges with.
func (s *service) handleAgg(a core.Agg, now time.Time) {
	st := s.syms[a.Sym]
	if st == nil {
		return
	}
	bars, changed := foldAgg(st.bars, a)
	st.bars = bars
	if changed {
		st.dirty = true
		st.lastFoldAt = now
	}
}

// ─── REST backfill and resync ────────────────────────────────────────────

// dispatch starts every REST fetch that is due, up to the free worker slots:
// symbols still waiting for their first load before resyncs (a candidate with
// no verdict at all matters more than a refresh of one that has), then by due
// time, then admission order. Each fetch is a short-lived goroutine reporting
// through s.results, so a slow Massive response occupies a worker slot and
// nothing else.
func (s *service) dispatch(now time.Time) {
	free := s.tm.workers - s.inflight
	if free <= 0 {
		return
	}
	var due []*symState
	for _, st := range s.syms {
		if !st.fetching && !st.due.After(now) {
			due = append(due, st)
		}
	}
	if len(due) == 0 {
		return
	}
	sort.Slice(due, func(i, j int) bool {
		a, b := due[i], due[j]
		if a.loaded != b.loaded {
			return !a.loaded
		}
		// A hot symbol's early resync (the dildo minute's final REST bar, needed
		// before the first touch bar can close) goes before ordinary resyncs.
		if ae, be := a.earlyCame(now), b.earlyCame(now); ae != be {
			return ae
		}
		if !a.due.Equal(b.due) {
			return a.due.Before(b.due)
		}
		return a.seq < b.seq
	})
	if len(due) > free {
		due = due[:free]
	}
	for _, st := range due {
		st.fetching = true
		s.inflight++
		s.wg.Add(1)
		go s.fetch(st.sym, st.gen, now)
	}
}

func (s *service) fetch(sym string, gen uint64, started time.Time) {
	defer s.wg.Done()
	ctx, cancel := context.WithTimeout(s.ctx, s.tm.fetchTimeout)
	defer cancel()
	res := fetchResult{sym: sym, gen: gen, started: started}
	func() {
		defer func() {
			if r := recover(); r != nil {
				res.err = fmt.Errorf("bars fetch panicked: %v", r)
			}
		}()
		res.bars, res.err = s.cfg.Bars(ctx, sym)
	}()
	select {
	case s.results <- res: // capacity == workers >= in flight: never blocks
	case <-s.ctx.Done():
	}
}

// backoff is the retry delay after the n-th consecutive failure: 5 s doubling
// to 2 min.
func (s *service) backoff(fails int) time.Duration {
	d := s.tm.retryMin
	for i := 1; i < fails && d < s.tm.retryMax; i++ {
		d *= 2
	}
	return min(d, s.tm.retryMax)
}

// resyncOffset spreads the first resyncs of symbols admitted together (the
// 04:00 rush admits dozens in one candidate cycle) across the second half of
// the interval, deterministically per symbol, so the REST calls do not all
// land on the same second every five minutes.
func (s *service) resyncOffset(sym string) time.Duration {
	half := s.tm.resyncEvery / 2
	if half <= 0 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(sym))
	return time.Duration(h.Sum64() % uint64(half))
}

func (s *service) handleFetch(r fetchResult, now time.Time) {
	s.inflight--
	// Whatever the outcome, a worker slot is free: start the next due fetch at
	// once instead of waiting for the tick (a 04:00 rush of admissions drains in
	// workers-wide waves, not one wave per second).
	defer s.dispatch(now)
	st := s.syms[r.sym]
	if st == nil || st.gen != r.gen {
		return // the symbol left (or left and came back) while the call was in flight
	}
	st.fetching = false
	if r.err != nil {
		st.fails++
		st.due = now.Add(s.backoff(st.fails))
		// A flapping REST endpoint would otherwise write a line per symbol per
		// retry: the first failure and every fifth after it are Warn, the rest Debug.
		if st.fails == 1 || st.fails%5 == 0 {
			logx.L().Warn("flagscan: bars fetch failed; will retry",
				"sym", st.sym, "loaded", st.loaded, "fails", st.fails, "retryIn", s.backoff(st.fails).String(), "err", r.err)
		} else {
			logx.L().Debug("flagscan: bars fetch failed", "sym", st.sym, "fails", st.fails, "err", r.err)
		}
		return
	}
	first := !st.loaded
	liveFresh := !st.lastFoldAt.IsZero() && !st.lastFoldAt.Before(r.started)
	st.bars = mergeREST(cleanREST(r.bars), st.bars, liveFresh, r.started.UnixMilli())
	st.loaded = true
	st.restAt = r.started
	st.dirty = true
	st.fails = 0
	st.due = now.Add(s.tm.resyncEvery)
	if first {
		// Phase the first resync somewhere in [resync/2, resync): see resyncOffset.
		st.due = now.Add(s.tm.resyncEvery/2 + s.resyncOffset(st.sym))
	}
	st.earlyDue = time.Time{}
	if he, ok := s.hot[st.sym]; ok {
		// A hot symbol: was this fetch late enough to hold the dildo minute
		// final? If so the parity check may run; if not and this was its first
		// load - or the minute is still waiting to be made final (a loaded
		// symbol whose tap began inside it, handleHot) - one EARLY resync at
		// the dildo bar's end + EarlyResync makes it final before the first
		// possible touch bar closes (a minute later at the earliest). After
		// that, the ordinary ResyncEvery applies.
		barEnd := time.UnixMilli(he.barMs + minuteMs)
		if !r.started.Before(barEnd.Add(s.tm.earlyResync - earlyResyncSlack)) {
			if st.parityMs > 0 {
				st.parityOK = true
			}
		} else if first || (st.parityMs > 0 && !st.parityOK) {
			s.earlyResync(st, he.barMs, now)
		}
	}
}

// earlyCame reports whether the symbol's early resync is pending and due.
func (st *symState) earlyCame(now time.Time) bool {
	return !st.earlyDue.IsZero() && !st.earlyDue.After(now)
}

// ─── Evaluate and publish ────────────────────────────────────────────────

// refOf is the scanner's view of an engine result. The levels keep NaN-means-
// null (the scan overlay Nulls them); State/FlagBars/SinceMs are copied as is,
// and so is the day's latest 9 EMA SLAM (LastSlamTs 0 = none today; the
// overlay then Nulls e9SlamAge and e9SlamPct whatever LastSlamPct holds).
func refOf(r emaflag.Result) core.EmaFlagRef {
	return core.EmaFlagRef{
		State:    int(r.State),
		DistPct:  r.EmaDistPct,
		Trigger:  r.Trigger,
		Stop:     r.Stop,
		FlagBars: r.FlagBars,
		SinceMs:  r.StateSinceMs,
		SlamMs:   r.LastSlamTs,
		SlamPct:  r.LastSlamPct,

		HodFlagMs: r.LastHodFlagTs,
	}
}

// withDildo adds the dildo rule's verdict and the hot-only flag to a ref. A
// symbol with no dildo today (LastDildoTs 0) keeps every dildo field zero (the
// overlay reads DildoMs 0 as "none" and PbState 0 as "no pullback"), so its
// ref is exactly the one refOf alone would give.
func withDildo(ref core.EmaFlagRef, d dildopb.Result, hotOnly bool) core.EmaFlagRef {
	ref.HotOnly = hotOnly
	if d.LastDildoTs == 0 {
		return ref
	}
	ref.DildoMs = d.LastDildoTs
	ref.DildoBody, ref.DildoBodyX, ref.DildoVolX, ref.DildoDvol = d.LastDildoBodyPct, d.LastDildoBodyX, d.LastDildoVolX, d.LastDildoDvol
	ref.PbState = d.PbState
	ref.TouchMs, ref.PbMs, ref.PbPx = d.TouchTs, d.PbTs, d.PbPx
	return ref
}

func sameFloat(a, b float64) bool { return a == b || (math.IsNaN(a) && math.IsNaN(b)) }

// sameRef reports whether two verdicts would publish the same thing. A new
// SLAM changes nothing else on the ref (it is an alert, not a state), so its
// fields must count here or the slam would wait for the next unrelated change;
// the same goes for EVERY dildo field (a touch, a pullback, a new dildo, the
// hot-only flag): each one compared, none left to ride on another change.
func sameRef(a, b core.EmaFlagRef) bool {
	return a.State == b.State && a.FlagBars == b.FlagBars && a.SinceMs == b.SinceMs &&
		sameFloat(a.DistPct, b.DistPct) && sameFloat(a.Trigger, b.Trigger) && sameFloat(a.Stop, b.Stop) &&
		a.SlamMs == b.SlamMs && sameFloat(a.SlamPct, b.SlamPct) && a.HodFlagMs == b.HodFlagMs &&
		a.DildoMs == b.DildoMs && sameFloat(a.DildoBody, b.DildoBody) && sameFloat(a.DildoBodyX, b.DildoBodyX) &&
		sameFloat(a.DildoVolX, b.DildoVolX) && sameFloat(a.DildoDvol, b.DildoDvol) &&
		a.PbState == b.PbState && a.TouchMs == b.TouchMs && a.PbMs == b.PbMs && sameFloat(a.PbPx, b.PbPx) &&
		a.HotOnly == b.HotOnly && a.DildoEarly == b.DildoEarly
}

// chartEarly is the early verdict (dildopb.JudgeEarly with the defaults, the
// trader's rule) of the dildo at ts on the chart's own bars: the trader judges
// on these same bars (EvalUpdate.Recent), so the two agree. Too few bars before
// it is "not early", as the trader has it.
func chartEarly(bars []core.Bar, events []dildopb.Event, ts int64) bool {
	return dildopb.EarlyFromBars(bars, ts, 0, dildopb.PriorDildoToday(events, ts), dildopb.DefaultEarlyParams()).Early()
}

// dildoEarlyOf is the chart-side early verdict of every green dildo in events
// (for the SIGNALS history's EARLY flag, DildoUpdate.Early).
func dildoEarlyOf(bars []core.Bar, events []dildopb.Event) map[int64]bool {
	var out map[int64]bool
	for i := range events {
		if e := &events[i]; e.Kind == dildopb.KindDildo {
			if out == nil {
				out = map[int64]bool{}
			}
			out[e.Ts] = chartEarly(bars, events, e.Ts)
		}
	}
	return out
}

// evalDildoOne runs the dildo rule over one symbol's bars, with evalOne's
// guard: a panic in the port costs that symbol's pullback (an empty result,
// logged the first time and every 100th), never the live process.
func (s *service) evalDildoOne(st *symState, nowMs int64) (res dildopb.Result) {
	defer func() {
		if r := recover(); r != nil {
			s.dildoPanics++
			if s.dildoPanics == 1 || s.dildoPanics%100 == 0 {
				logx.L().Error("flagscan: dildo evaluation panicked; no pullback for this symbol",
					"sym", st.sym, "panic", r, "panics", s.dildoPanics)
			}
			res = dildopb.Result{}
		}
	}()
	return s.evalDildo(st.bars, minuteMs, nowMs, s.dildoParams(), s.engineDildo[st.sym], dildoConfirmTolerancePct)
}

// dildoParams is the rule's parameters for this evaluation: Config.DildoLive
// (the Signal window's tuning panel, swapped atomically) when wired, else the
// fixed Config.Dildo. Read once per symbol evaluation.
func (s *service) dildoParams() dildopb.Params {
	if s.cfg.DildoLive != nil {
		return s.cfg.DildoLive()
	}
	return s.dparams
}

// dildoConfirmTolerancePct: a bar the live engine already called a green dildo
// is confirmed on the chart's bars when it meets every threshold within this
// many percent (minimums x0.95, the top-range maximum x1.05) - the engine's
// per-second aggregates and the REST minute bars can differ by a tick. Bars the
// engine did not flag keep the exact thresholds.
const dildoConfirmTolerancePct = 5

// maxEngineDildos bounds the engine-seen dildo bars kept per symbol.
const maxEngineDildos = 16

// noteEngineDildo records a bar the engine called a dildo (deduplicated,
// newest kept, bounded).
func (s *service) noteEngineDildo(sym string, barMs int64) {
	seen := s.engineDildo[sym]
	for _, ms := range seen {
		if ms == barMs {
			return
		}
	}
	seen = append(seen, barMs)
	if len(seen) > maxEngineDildos {
		seen = append([]int64(nil), seen[len(seen)-maxEngineDildos:]...)
	}
	s.engineDildo[sym] = seen
}

// dildoAfterTheFact logs (and counts) a published touch or pullback bar that a
// later evaluation of the SAME dildo moved: only a resync rewriting a bar can
// do that (the rule is append-only on unchanged bars). One change is the
// rule's own: a forming bar inside the EMA band publishes TOUCH with TouchMs =
// that bar, and when the same bar then trades through the floor the rule's
// forming-bar branch cancels with no touch bar (TouchMs 0; the closed bar
// later gives the same floor cancel on that bar) - that is not a rewrite.
func (s *service) dildoAfterTheFact(st *symState, old, cur core.EmaFlagRef) {
	if old.DildoMs == 0 || old.DildoMs != cur.DildoMs {
		return
	}
	formingFloorBreak := old.PbState == dildopb.PbTouching && cur.PbState == dildopb.PbCancelled && cur.TouchMs == 0
	if (old.TouchMs != 0 && cur.TouchMs != old.TouchMs && !formingFloorBreak) || (old.PbMs != 0 && cur.PbMs != old.PbMs) {
		s.dildoRewrites++
		logx.L().Info("flagscan: a resync moved a published dildo touch/pullback",
			"sym", st.sym, "dildoMs", cur.DildoMs, "touchMs", old.TouchMs, "nowTouchMs", cur.TouchMs,
			"pbMs", old.PbMs, "nowPbMs", cur.PbMs, "rewrites", s.dildoRewrites)
	}
}

// dildoParity checks, once the bars hold the engine's dildo minute final,
// that the chart rule finds a dildo on that very bar (dildoParityMiss counts
// the times it does not: the engine's per-second aggregates and the REST
// minute bars can differ by a tick; counted, not fixed).
func (s *service) dildoParity(st *symState, nowMs int64) {
	if st.parityMs == 0 || !st.parityOK || nowMs < st.parityMs+minuteMs {
		return
	}
	found := false
	for i := len(st.dildo.Events) - 1; i >= 0; i-- {
		ev := &st.dildo.Events[i]
		if ev.Ts < st.parityMs {
			break
		}
		if ev.Ts == st.parityMs && ev.Kind == dildopb.KindDildo {
			found = true
			break
		}
	}
	if !found {
		s.dildoParityMiss++
		logx.L().Info("flagscan: the chart's bars show no green dildo where the engine saw one",
			"sym", st.sym, "barMs", st.parityMs, "misses", s.dildoParityMiss)
	}
	st.parityMs, st.parityOK = 0, false
}

// evalOne runs the engine over one symbol's bars. A panic in the port (a bug,
// or a shape of data nobody has fed it yet) must cost one symbol's verdict,
// never the scanner process the trader is using - the loop runs inside the
// live binary - so it is recovered, logged, and the symbol simply has no
// verdict until its bars next change (plus one more try when its last bar
// closes: the closed-bar rules are a different path from the forming-bar pass).
func (s *service) evalOne(st *symState, nowMs int64) (res emaflag.Result, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			logx.L().Error("flagscan: evaluation panicked; symbol skipped", "sym", st.sym, "panic", r)
			res, ok = emaflag.Result{}, false
		}
	}()
	return s.eval(st.bars, minuteMs, nowMs, s.params), true
}

// notify hands one evaluation to Config.OnEval (if set) and records that the
// symbol's first evaluation of this admission - the one that replayed the REST
// history - is behind it, so every later one is Primed. The callback is the
// consumer's code on the loop goroutine: a panic in it is recovered and logged
// (the first, then every 100th) and costs that one update, never the loop, the
// verdict map or the other symbols.
func (s *service) notify(st *symState, res emaflag.Result, now time.Time) {
	primed := st.primed
	st.primed = true
	if s.cfg.OnEval == nil {
		return
	}
	defer s.recoverHook("OnEval", st.sym)
	u := s.update(st, res, now)
	u.Primed = primed
	s.cfg.OnEval(u)
}

// notifyHot hands one evaluation of a HOT-ONLY symbol to Config.OnHot. It never
// touches st.primed: a hot name that later becomes a candidate (or a pin) starts
// unprimed for OnEval, exactly as it would without this hook.
func (s *service) notifyHot(st *symState, res emaflag.Result, now time.Time) {
	defer s.recoverHook("OnHot", st.sym)
	u := s.update(st, res, now)
	u.HotOnly = true
	s.cfg.OnHot(u)
}

// notifyEngineDildo hands one engine green dildo to Config.OnEngineDildo.
func (s *service) notifyEngineDildo(hit core.DildoHit, now time.Time) {
	if s.cfg.OnEngineDildo == nil {
		return
	}
	defer s.recoverHook("OnEngineDildo", hit.Sym)
	s.cfg.OnEngineDildo(EngineDildoUpdate(hit, now.UnixMilli()))
}

// recoverHook is the guard around a consumer's callback: a panic costs that one
// update (logged the first time and every 100th), never the loop.
func (s *service) recoverHook(hook, sym string) {
	if r := recover(); r != nil {
		s.evalPanics++
		if s.evalPanics == 1 || s.evalPanics%100 == 0 {
			logx.L().Error("flagscan: Config."+hook+" panicked; update dropped",
				"sym", sym, "panic", r, "panics", s.evalPanics)
		}
	}
}

// updateRecentBars is how many of the latest bars an update carries (EvalUpdate.Recent):
// 40 minutes (the auto-trader's early-dildo filter reads the 30 before a dildo), so a consumer that missed updates for a while (a full queue, a stall)
// still sees every bar of its green dildo setup's wait.
const updateRecentBars = 40

// update builds the EvalUpdate of one evaluation: the engine's result, the latest bar,
// the dildo rule's call (no events: the consumer reads its summary) with its bar, and a
// copy of the latest bars.
func (s *service) update(st *symState, res emaflag.Result, now time.Time) EvalUpdate {
	n := len(st.bars)
	d := st.dildo
	d.Events = nil
	var bar core.Bar
	if d.LastDildoTs > 0 {
		for i := n - 1; i >= 0 && st.bars[i].T >= d.LastDildoTs; i-- {
			if st.bars[i].T == d.LastDildoTs {
				bar = st.bars[i]
				break
			}
		}
	}
	return EvalUpdate{
		Sym:      st.sym,
		NowMs:    now.UnixMilli(),
		Result:   res,
		Last:     st.bars[n-1],
		Dildo:    d,
		DildoBar: bar,
		Recent:   append([]core.Bar(nil), st.bars[max(0, n-updateRecentBars):]...),
	}
}

// notifyDildo hands the dildo rule's latest call over one symbol to
// Config.OnDildo (if set and the call holds any event), for hot-only names as
// well as candidates. A panic in the callback is recovered and logged (the
// first, then every 100th) and costs that one update only.
func (s *service) notifyDildo(st *symState, now time.Time) {
	if s.cfg.OnDildo == nil || len(st.dildo.Events) == 0 {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			s.dildoHookPanics++
			if s.dildoHookPanics == 1 || s.dildoHookPanics%100 == 0 {
				logx.L().Error("flagscan: Config.OnDildo panicked; update dropped",
					"sym", st.sym, "panic", r, "panics", s.dildoHookPanics)
			}
		}
	}()
	s.cfg.OnDildo(DildoUpdate{Sym: st.sym, NowMs: now.UnixMilli(), HotOnly: st.hot, Result: st.dildo,
		Early: dildoEarlyOf(st.bars, st.dildo.Events)})
}

// evaluate runs the engine on every symbol whose bars changed since its last
// verdict, or whose last bar has closed since (the clock alone changes the
// answer then: the forming-bar pass gives way to the closed-bar rules), and
// publishes the whole map when any verdict appeared, changed or vanished.
//
// The engine sees nowMs = now - CloseGrace. That is what keeps a bar "forming"
// for CloseGrace after its minute ends when no newer bar exists, so the last
// seconds of the minute (which arrive a little late) land before the closed-
// bar rules judge it; the moment a newer bar opens, the older one is closed
// regardless (the engine only treats the LAST bar as forming).
//
// A symbol with no bar on today's ET date has no verdict: the engine reports
// the LAST ET day it is given, so a candidate that has not printed today (a
// frozen post-session candidate list read after midnight, a name REST has not
// caught up on) would otherwise publish yesterday's final state - and its age -
// as if it were live.
//
// Each successful evaluation is also handed to Config.OnEval (see notify). The
// symbols due this tick are evaluated strongest pmVolX first, then by symbol,
// not in map order: a consumer with a limited number of slots (a trader's open
// positions) must get the same answer for the same input, and the strongest
// setup is the one that should meet a free slot first.
func (s *service) evaluate(now time.Time) {
	nowMs := now.Add(-s.tm.closeGrace).UnixMilli()
	today := etDay(now.UnixMilli())
	todo := s.todo[:0]
	for _, st := range s.syms {
		if !st.loaded {
			continue
		}
		n := len(st.bars)
		if n == 0 || etDay(st.bars[n-1].T) != today {
			if st.evaluated {
				st.evaluated = false
				s.mapDirty = true
			}
			continue
		}
		closed := nowMs >= st.bars[n-1].T+minuteMs
		// A dildo still "watching" on a name gone quiet must turn "expired" once
		// its window's last bar has closed: the clock alone changes that answer.
		expireDue := !st.broken && st.dildo.PbState == dildopb.PbWatching && st.dildo.DeadlineTs > 0 &&
			nowMs >= st.dildo.DeadlineTs+minuteMs
		// So does an engine dildo whose parity check is ready (a quiet symbol
		// may have no bar change to trigger it).
		parityDue := !st.broken && st.parityMs > 0 && st.parityOK && nowMs >= st.parityMs+minuteMs
		if (st.evaluated || st.broken) && !st.dirty && (st.evalClosed || !closed) && !expireDue && !parityDue {
			continue
		}
		todo = append(todo, st)
	}
	if len(todo) > 1 {
		sort.Slice(todo, func(i, j int) bool {
			a, b := todo[i], todo[j]
			if a.pmVolX != b.pmVolX {
				return a.pmVolX > b.pmVolX
			}
			return a.sym < b.sym
		})
	}
	for _, st := range todo {
		closed := nowMs >= st.bars[len(st.bars)-1].T+minuteMs
		st.dirty = false
		res, ok := s.evalOne(st, nowMs)
		st.broken = !ok
		if !ok {
			// Record the clock state on a failure too, or the skip guard above never
			// holds for a symbol that failed while its bar was forming: once the bar
			// closes it would be re-run (and log an Error) every tick until its bars
			// change. A failure earns ONE retry when the bar closes, then silence.
			st.evalClosed = closed
			if st.evaluated {
				st.evaluated = false
				s.mapDirty = true
			}
			continue
		}
		// The dildo rule over the same bars, for every tracked symbol (a pmVolX
		// candidate gets its dildo and pullback too), on the same clock.
		st.dildo = s.evalDildoOne(st, nowMs)
		s.notifyDildo(st, now)
		ref := withDildo(refOf(res), st.dildo, st.hot)
		if ref.DildoMs > 0 && chartEarly(st.bars, st.dildo.Events, ref.DildoMs) {
			ref.DildoEarly = 1
		}
		if st.evaluated {
			s.dildoAfterTheFact(st, st.ref, ref)
		}
		s.dildoParity(st, nowMs)
		st.evalClosed = closed
		if !st.evaluated || !sameRef(st.ref, ref) {
			s.mapDirty = true
		}
		st.ref, st.evaluated = ref, true
		if !st.hot {
			// Never for a hot-only symbol - not even the priming bookkeeping: the
			// consumer's contract is "pmVolX candidates + pinned", and a hot name
			// that later becomes a candidate must start unprimed (its events so
			// far are history), like any fresh admission.
			s.notify(st, res, now)
		} else if s.cfg.OnHot != nil {
			// A hot-only symbol goes to OnHot instead (the green dildo strategy's door),
			// still without the priming bookkeeping.
			s.notifyHot(st, res, now)
		}
	}
	clear(todo) // the scratch list must not keep a dropped symbol's bars alive
	s.todo = todo[:0]
	if !s.mapDirty {
		return
	}
	s.mapDirty = false
	m := make(map[string]core.EmaFlagRef, len(s.syms))
	for sym, st := range s.syms {
		if st.evaluated {
			m[sym] = st.ref
		}
	}
	if len(m) == 0 && !s.pubNonNil {
		return // nothing to say and nothing to clear
	}
	s.pubNonNil = len(m) > 0
	s.cfg.Publish(m)
}

// ─── Status ──────────────────────────────────────────────────────────────

// stats is a point-in-time summary: the status line, and what the tests assert.
type stats struct {
	candidates int // symbols being tracked
	qualified  int // over the threshold at the last refresh, before the cap
	beyondCap  int // qualified but not tracked because of the cap
	evaluated  int // with a current verdict
	byState    [5]int
	queued     int // waiting for the first REST load (queued, in back-off or in flight)
	inflight   int // REST calls running
	drops      int64
	pinned     int // symbols held by Config.Pinned (tracked on top of the cap)

	// GREEN DILDO: the hot set, how many of the tracked are hot-only, hot names
	// evicted for MaxHot, hits the engine dropped (Config.DildoDrops), engine
	// dildos the chart rule did not confirm, and published touch/pullback bars
	// a resync moved.
	hot             int
	hotOnly         int
	hotEvictions    int64
	dildoDrops      int64
	dildoParityMiss int64
	dildoRewrites   int64
}

func (s *service) stats() stats {
	st := stats{
		candidates: len(s.syms), qualified: s.qualified, beyondCap: s.beyondCap, inflight: s.inflight,
		pinned: len(s.pins),
		hot:    len(s.hot), hotEvictions: s.hotEvictions, dildoParityMiss: s.dildoParityMiss, dildoRewrites: s.dildoRewrites,
	}
	if s.cfg.DildoDrops != nil {
		st.dildoDrops = s.cfg.DildoDrops()
	}
	for _, sy := range s.syms {
		if !sy.loaded {
			st.queued++
		}
		if sy.hot {
			st.hotOnly++
		}
		if sy.evaluated {
			st.evaluated++
			if sy.ref.State >= 0 && sy.ref.State < len(st.byState) {
				st.byState[sy.ref.State]++
			}
		}
	}
	if s.cfg.Drops != nil {
		st.drops = s.cfg.Drops()
	}
	return st
}

// logStatus writes the one line a minute: how many candidates, how many have a
// verdict and in which state, how many still wait for their REST history, and
// the tap's discard count (a rising one means the loop is not keeping up).
func (s *service) logStatus(now time.Time) {
	s.nextLog = now.Add(s.tm.logEvery)
	st := s.stats()
	logx.L().Info("flagscan",
		"candidates", st.candidates, "qualified", st.qualified, "beyondCap", st.beyondCap, "pinned", st.pinned,
		"evaluated", st.evaluated,
		"idle", st.byState[0], "impulse", st.byState[1], "flat", st.byState[2], "ready", st.byState[3], "long", st.byState[4],
		"backfillQueue", st.queued, "inflight", st.inflight,
		"tapDrops", st.drops, "tapDropsSinceLast", st.drops-s.lastDrops,
		"hot", st.hot, "hotOnly", st.hotOnly, "hotEvictions", st.hotEvictions,
		"dildoDrops", st.dildoDrops, "dildoParityMiss", st.dildoParityMiss, "dildoRewrites", st.dildoRewrites)
	if st.drops > s.lastDrops {
		logx.L().Warn("flagscan: the aggregate tap dropped aggregates (loop behind); the REST resync will heal the bars",
			"dropped", st.drops-s.lastDrops, "total", st.drops)
	}
	s.lastDrops = st.drops
}

// ─── small helpers ───────────────────────────────────────────────────────

func sortedKeys(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
