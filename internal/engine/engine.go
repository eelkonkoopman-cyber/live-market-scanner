// Package engine is the in-memory rolling-window store — the hot path. It owns
// N single-writer shards, a 1 Hz forward-fill clock, and a merger that emits an
// immutable MetricSnapshot every tick. It imports only core (and stdlib), plus
// the two pure 1-minute rule modules its closed-bar judge calls (dildopb and
// climax, which themselves import only core and stdlib); it never imports
// server, scan, feed, or persist.
package engine

import (
	"context"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dlevel"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

// Engine implements core.Engine.
type Engine struct {
	shards []*shard
	mask   uint32

	out    chan *core.MetricSnapshot
	tickHz time.Duration

	// sessSnaps carries the once-per-boundary whole-market session snapshots
	// (the persisted morning-review source). Handoff is ALWAYS non-blocking:
	// if the consumer is wedged the snapshot is dropped and counted — a lost
	// snapshot is a gap in history, a stalled engine is an outage.
	sessSnaps     chan core.SessionSnapshot
	sessSnapDrops atomic.Int64

	ix snapIndex // the merger's Index builder (merger goroutine only)

	// free holds Rows buffers of snapshots the consumer has finished with
	// (Recycle); the merger fills the next snapshot into one instead of
	// allocating ~14 MB per tick.
	free chan []core.Row

	baseline atomic.Pointer[core.BaselineTable]
	floats   atomic.Pointer[map[string]core.FloatRef]
	secTypes atomic.Pointer[map[string]string]
	session  atomic.Int32
	tracked  atomic.Int64

	loc *time.Location
	now func() time.Time // injectable for tests

	// startMs is when this engine began listening. A 5-minute candle that
	// opened earlier only saw part of its prints, so it is never used as the
	// "previous candle" for a break (see tickerState.accum5m).
	startMs int64

	// preFromOpen: the current pre-market scope began at the real 04:00 ET open
	// (the engine was already running), so the 09:30 freeze of it is complete.
	preFromOpen atomic.Bool

	// etOffMs is the current UTC->ET offset in ms (-4 h EDT, -5 h EST): set by
	// New, refreshed every second by runClock, read once per shard tick and
	// once per snapshot by the closed-bar judge (its ET day / minute) and the
	// signal rows ("today" starts at 04:00 ET).
	etOffMs atomic.Int64

	// sig is the closed 1-minute bar judge's wiring (GREEN DILDO / VOLUME
	// CLIMAX), shared by every ticker; nil when both are off.
	sig *signals
}

// Options configures a new Engine.
type Options struct {
	TickHz   int                      // snapshot cadence (default 4)
	Baseline *core.BaselineTable      // optional initial RVOL baseline
	Floats   map[string]core.FloatRef // optional initial float reference
	Now      func() time.Time         // test hook; defaults to time.Now

	// Dildo turns on the GREEN DILDO judge (alert 1 of the dildo -> 9 EMA
	// pullback signal; env SCANNER_DILDO, default on in cmd/scanner): the
	// dildoAge..dildoDvol row fields and the DildoHits hand-off to flagscan.
	// Climax turns on the VOLUME CLIMAX judge and its confirmation latch (env
	// SCANNER_CLIMAX, default on): the climaxAge..climaxConfPx row fields. With
	// either on, every closed traded 1-minute bar of every ticker is judged;
	// with both off (the zero value) nothing of it runs and those fields stay
	// Null.
	Dildo  bool
	Climax bool
	// Bbs turns on the BOUNCY BALL SHORT judge (env SCANNER_BBS, default on):
	// the bbsAge..bbsDvol row fields (bbs.go).
	Bbs bool
	// Ignition turns on the IGNITION judge (env SCANNER_IGNITION, default on):
	// the ignAge..ignVolX row fields (ignition.go).
	Ignition bool
	// EmaVwap turns on the 9EMA VWAP CROSS judge (env SCANNER_EMAVWAP, default
	// on): the evBullAge..evDist row fields (emavwap.go).
	EmaVwap bool
	// VolBreak turns on the VOLUME BREAK judge (env SCANNER_VOLBREAK, default
	// on): the vbAge..vbEntryPx row fields (volbreak.go).
	VolBreak bool

	// SignalLog also hands every alert the judge calls (GREEN DILDO, VOLUME
	// CLIMAX, BOTTOM CONFIRMED, CLIMAX FAILED - each only while its own switch
	// above is on) to SignalHits, for the Signal window's signal history
	// (internal/flaglog). Off, or with both judges off, SignalHits is nil.
	SignalLog bool
}

func (o Options) tickInterval() time.Duration {
	hz := o.TickHz
	if hz <= 0 {
		hz = 4
	}
	return time.Second / time.Duration(hz)
}

var _ core.Engine = (*Engine)(nil)

// New constructs an Engine with N = next-pow2(GOMAXPROCS) shards.
func New(opts Options) *Engine {
	n := roundPow2(runtime.GOMAXPROCS(0))
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.UTC // fall back; tests embed tzdata where needed
	}
	e := &Engine{
		mask: uint32(n - 1),
		out:  make(chan *core.MetricSnapshot, 1),
		free: make(chan []core.Row, 2),
		// Three boundaries a day: a small buffer absorbs a briefly-busy writer,
		// beyond that snapshots drop (see collectSessionSnapshot).
		sessSnaps: make(chan core.SessionSnapshot, 4),
		tickHz:    opts.tickInterval(),
		loc:       loc,
		now:       time.Now,
	}
	if opts.Now != nil {
		e.now = opts.Now
	}
	e.startMs = e.now().UnixMilli()
	e.session.Store(int32(core.SessionClosed))
	e.refreshEtOff()
	if opts.Dildo || opts.Climax || opts.Bbs || opts.Ignition || opts.EmaVwap || opts.VolBreak {
		e.sig = &signals{dildo: opts.Dildo, climax: opts.Climax, bbs: opts.Bbs, ign: opts.Ignition, emv: opts.EmaVwap, vb: opts.VolBreak, etOff: &e.etOffMs}
		if opts.Dildo {
			e.sig.hits = make(chan core.DildoHit, dildoHitsBuffer)
		}
		if opts.SignalLog {
			e.sig.events = make(chan core.SignalHit, signalHitsBuffer)
		}
	}

	e.shards = make([]*shard, n)
	for i := range e.shards {
		e.shards[i] = &shard{
			eng:   e,
			in:    make(chan core.Agg, 8192),
			tick:  make(chan int64, 1),
			snap:  make(chan snapReq, 1),
			ctrl:  make(chan func(*shard), 64),
			state: make(map[string]*tickerState),
		}
	}

	// Always have non-nil baseline/floats/secTypes so the hot path never nil-checks.
	e.baseline.Store(&core.BaselineTable{})
	empty := map[string]core.FloatRef{}
	e.floats.Store(&empty)
	emptyTypes := map[string]string{}
	e.secTypes.Store(&emptyTypes)
	if opts.Baseline != nil {
		e.baseline.Store(opts.Baseline)
	}
	if opts.Floats != nil {
		f := opts.Floats
		e.floats.Store(&f)
	}
	return e
}

// Apply routes a streaming aggregate to its shard. Called on the ingest
// goroutine. Bounded buffer; coalescing under burst is a future optimization.
func (e *Engine) Apply(a core.Agg) {
	if a.Sym == "" {
		return
	}
	e.shards[shardIdx(a.Sym, e.mask)].in <- a
}

// Seed sets a ticker's day-state from a snapshot row. sessVol is SET (not
// accumulated), so re-seeding on reconnect corrects the gap rather than
// double-counting.
//
// Ingest seeds right before its stream starts and on every reconnect, so a
// seed also marks where the closed-bar judge's whole minutes begin again: a
// minute that began before it was only part-observed (signals.streamMs).
func (e *Engine) Seed(s core.Seed) {
	if s.Sym == "" {
		return
	}
	if e.sig != nil {
		e.sig.streamFrom(e.now().UnixMilli())
	}
	e.shards[shardIdx(s.Sym, e.mask)].ctrl <- func(sh *shard) {
		sh.get(s.Sym).seedApply(s)
	}
}

// SeedClose sets the prior official close for a symbol (drives chgDay/gap).
func (e *Engine) SeedClose(sym string, prevClose float64) {
	if sym == "" {
		return
	}
	e.shards[shardIdx(sym, e.mask)].ctrl <- func(s *shard) {
		s.get(sym).prevClose = float32(prevClose)
	}
}

// SetSession updates the current session and resets per-ticker state at session
// boundaries. Entering Pre from any other state begins a new extended day and
// fully resets rolling/cumulative state (nothing bleeds across days). Entering
// Regular or Post resets only the session-scoped metrics so chgSess/sessHigh/
// sessLow/sessVol/sessVwap measure each sub-session alone; the 04:00-anchored day
// state carries through. A move to Closed leaves the last session's values frozen.
//
// On the scope-reset path the OUTGOING session's scoped metrics are first
// frozen into the per-ticker review slots (freezeScope, keyed on old — a
// Closed→Regular mid-day start freezes nothing because no session completed
// under our watch). The full-reset path never freezes: entering Pre is a new
// day, and resetSession clears both frozen slots.
//
// Whenever the OUTGOING session is a market session (Pre/Regular/Post) the
// same once-per-boundary ctrl closure ALSO collects a raw SessionSnapshotRow
// per traded ticker — before any reset touches the scope — and the per-shard
// slices are fanned into one SessionSnapshot on SessionSnapshots() (the
// persisted morning-review source). This is what makes the 20:00 Post→Closed
// boundary a real ctrl pass now: it collects the after-hours snapshot but
// still neither freezes nor resets, so "a move to Closed leaves the last
// session's values frozen" keeps holding for the live row fields. Per-tick
// cost stays zero; the collection rides the boundary visit that already
// touches every ticker, and every handoff on the way out is non-blocking.
func (e *Engine) SetSession(s core.Session) {
	old := core.Session(e.session.Swap(int32(s)))
	if s == old {
		return
	}
	full := s == core.SessionPre && old != core.SessionPre
	if full {
		// A start or restart mid-premarket also resets here, but its pre scope
		// misses everything before now; only a reset within a minute of 04:00 ET
		// means the engine watched the whole session.
		now := e.now().In(e.loc)
		open := time.Date(now.Year(), now.Month(), now.Day(), 4, 0, 0, 0, e.loc)
		e.preFromOpen.Store(!now.Before(open) && now.Sub(open) <= time.Minute)
	}
	preComplete := e.preFromOpen.Load()
	scope := !full && (s == core.SessionRegular || s == core.SessionPost)
	collect := old == core.SessionPre || old == core.SessionRegular || old == core.SessionPost
	if !full && !scope && !collect {
		return
	}
	// The trading date the outgoing session belongs to, stamped at the
	// boundary moment: 09:30 closes that same date's pre-market, 16:00 and
	// 20:00 close the same date's regular/post sessions.
	var date string
	var reply chan []core.SessionSnapshotRow
	if collect {
		date = e.now().In(e.loc).Format("2006-01-02")
		reply = make(chan []core.SessionSnapshotRow, len(e.shards))
	}
	for _, sh := range e.shards {
		sh.ctrl <- func(x *shard) {
			var rows []core.SessionSnapshotRow
			for _, t := range x.state {
				if collect {
					// BEFORE any reset: the snapshot reads the live scope the
					// reset below is about to wipe.
					if r, ok := t.snapshotRow(); ok {
						rows = append(rows, r)
					}
				}
				if full {
					t.resetSession()
				} else if scope {
					t.freezeScope(old)
					if old == core.SessionPre {
						t.frozenPre.complete = preComplete
					}
					t.resetSessionScope()
				}
			}
			if collect {
				reply <- rows // buffered len(shards): never blocks the shard
			}
		}
	}
	if collect {
		go e.collectSessionSnapshot(date, old, reply)
	}
}

// collectSessionSnapshot fans the per-shard boundary row slices into one
// SessionSnapshot and hands it to the sessSnaps consumer. Runs on its own
// goroutine (SetSession must not wait on the shards); the final handoff is
// non-blocking — a wedged writer costs the snapshot, never the engine. The
// engine cannot log (it imports only core), so drops are counted on an atomic
// the writer side reads and reports via SessionSnapshotDrops. The timeout
// covers shutdown: a shard whose run loop exited mid-boundary never executes
// its ctrl closure, and an eternally-parked collector (or worse, a PARTIAL
// snapshot that would make every missing ticker look untraded) is not
// acceptable — after 30s the whole snapshot is abandoned and counted dropped.
func (e *Engine) collectSessionSnapshot(date string, old core.Session, reply <-chan []core.SessionSnapshotRow) {
	rows := make([]core.SessionSnapshotRow, 0, 4096)
	timeout := time.After(30 * time.Second)
	for range e.shards {
		select {
		case r := <-reply:
			rows = append(rows, r...)
		case <-timeout:
			e.sessSnapDrops.Add(1)
			return
		}
	}
	select {
	case e.sessSnaps <- core.SessionSnapshot{Date: date, Session: old, Rows: rows}:
	default:
		e.sessSnapDrops.Add(1) // consumer wedged: drop, count, move on
	}
}

// SessionSnapshots returns the once-per-boundary session snapshot stream (the
// morning-review persistence source), mirroring Snapshots().
func (e *Engine) SessionSnapshots() <-chan core.SessionSnapshot { return e.sessSnaps }

// SessionSnapshotDrops reports how many boundary snapshots have been dropped
// because the consumer was wedged (or a shutdown interrupted the collection).
// The writer goroutine reads it to log the loss the engine itself cannot.
func (e *Engine) SessionSnapshotDrops() int64 { return e.sessSnapDrops.Load() }

// SeedFrozen backfills the frozen prior-session review slots — pre* when
// which == SessionPre, rth* when which == SessionRegular; any other which is
// a no-op — from raw minute-bar window stats derived outside the engine. It
// is the restart-recovery path for a process that missed (or only partially
// observed) the live 09:30/16:00 boundary. Percentages (chg/gap/fade) are
// derived HERE, per ticker, from the engine's own authoritative prevClose
// through newFrozenScope — the exact helper the live boundary freeze uses —
// so the two paths cannot drift; the caller supplies only raw Hi/Lo/Vwap/
// Close/Vol.
//
// OVERWRITE CONTRACT: a provided symbol's slot is overwritten
// UNCONDITIONALLY. The caller must invoke SeedFrozen only when the live slot
// is known-incomplete (the app started after the boundary, or mid-session so
// the boundary froze a partial window) — never as a routine refresh on top of
// a cleanly live-frozen day. Symbols absent from stats keep whatever they
// had; symbols the engine doesn't track are silently skipped (seeding never
// creates tickers). Runs as a ctrl-closure fan-out (the Curves/SessionBars
// precedent): every write happens on the owning shard's goroutine, nothing
// new on the per-tick hot path. stats is read-only and shared across shards.
// SetPrevAhHigh seeds the previous trading day's after-hours (16:00-20:00 ET)
// high per symbol, for brkPrevAhAge / vsPrevAh / prevAhHigh. Symbols absent
// from levels keep what they had; untracked symbols are skipped (seeding never
// creates tickers); non-positive levels are ignored. Re-seeding the same value
// is a no-op for the break tracker, so callers may re-apply freely. The day
// rollover (resetSession) clears every level. Ctrl fan-out: all writes happen
// on the owning shard's goroutine. levels is read-only and shared.
// SetAdv10 seeds each symbol's 10-day average daily volume (pmVolX / adv10).
// Same contract as SetPrevAhHigh: absent symbols keep theirs, untracked ones
// are skipped, non-positive values ignored, re-applying is harmless. Unlike
// the levels it is NOT cleared at the day reset — the loader replaces it.
func (e *Engine) SetAdv10(adv map[string]float64) {
	if len(adv) == 0 {
		return
	}
	for _, sh := range e.shards {
		sh.ctrl <- func(s *shard) {
			for sym, v := range adv {
				if t, ok := s.state[sym]; ok && float32(v) > 0 {
					t.adv10 = float32(v)
				}
			}
		}
	}
}

func (e *Engine) SetPrevAhHigh(levels map[string]float64) {
	if len(levels) == 0 {
		return
	}
	for _, sh := range e.shards {
		sh.ctrl <- func(s *shard) {
			for sym, v := range levels {
				if t, ok := s.state[sym]; ok && float32(v) > 0 {
					t.prevAhHi = float32(v)
				}
			}
		}
	}
}

func (e *Engine) SeedFrozen(which core.Session, stats map[string]core.SessionStats) {
	if which != core.SessionPre && which != core.SessionRegular {
		return
	}
	if len(stats) == 0 {
		return
	}
	for _, sh := range e.shards {
		sh.ctrl <- func(s *shard) {
			for sym, st := range stats {
				t, ok := s.state[sym]
				if !ok {
					continue // another shard's symbol, or not tracked at all
				}
				var vwap float64
				vwapSet := st.Vwap > 0
				if vwapSet {
					vwap = st.Vwap
				}
				// Narrow FIRST, gate on the narrowed value — the exact order the
				// live path uses (accumScope gates on float32(a.High) > 0), so
				// the two paths' float64→float32 conversion points are identical.
				hi, lo, close := float32(st.Hi), float32(st.Lo), float32(st.Close)
				fs := newFrozenScope(t.prevClose, hi, lo, close,
					hi > 0, lo > 0, vwap, vwapSet, st.Vol)
				if which == core.SessionPre {
					fs.complete = true // prefill fetched the whole session's bars
					t.frozenPre = fs
				} else {
					t.frozenRth = fs
				}
			}
		}
	}
}

// Retain evicts tracked tickers not in members. Each shard prunes its own state
// (single-writer) and the tracked counter is decremented per eviction. members is
// read-only and shared across shards. A nil/empty members set is ignored to avoid
// wiping everything when the universe is still permissive.
func (e *Engine) Retain(members map[string]struct{}) {
	if len(members) == 0 {
		return
	}
	for _, sh := range e.shards {
		sh.ctrl <- func(s *shard) {
			kept := s.list[:0] // s.list holds exactly s.state's tickers
			for _, t := range s.list {
				if _, ok := members[t.sym]; ok {
					kept = append(kept, t)
					continue
				}
				delete(s.state, t.sym)
				e.tracked.Add(-1)
			}
			clear(s.list[len(kept):])
			s.list = kept
		}
	}
}

// SetFloats / SetSecTypes / SetBaseline hot-swap the reference data via
// atomic pointers; the merger picks up the new pointer on its next tick.
func (e *Engine) SetFloats(f map[string]core.FloatRef) { e.floats.Store(&f) }
func (e *Engine) SetSecTypes(m map[string]string) {
	e.secTypes.Store(&m)
	if e.sig != nil {
		e.sig.secTypes.Store(&m)
	}
}
func (e *Engine) SetBaseline(b *core.BaselineTable) {
	if b != nil {
		e.baseline.Store(b)
	}
}

// Baseline returns the table RVOL currently reads (nil before the first load).
// Callers must treat it as read-only and swap a modified copy in.
func (e *Engine) Baseline() *core.BaselineTable { return e.baseline.Load() }

// Curves collects each tracked ticker's captured cumulative-volume curve up to
// lastBucket (inclusive), for the nightly RVOL flush. The read runs inside each
// shard's single-writer goroutine, so it never races live updates.
func (e *Engine) Curves(lastBucket int) map[string][]int64 {
	if lastBucket < 0 {
		lastBucket = 0
	}
	if lastBucket >= core.BucketCount {
		lastBucket = core.BucketCount - 1
	}
	reply := make(chan map[string][]int64, len(e.shards))
	for _, sh := range e.shards {
		lb := lastBucket
		sh.ctrl <- func(s *shard) {
			m := make(map[string][]int64, len(s.state))
			for sym, t := range s.state {
				cv := make([]int64, lb+1)
				copy(cv, t.curve[:lb+1])
				m[sym] = cv
			}
			reply <- m
		}
	}
	out := make(map[string][]int64)
	for range e.shards {
		for k, v := range <-reply {
			out[k] = v
		}
	}
	return out
}

// SessionBars collects each ACTIVE ticker's session-so-far 5-minute OHLCV bars
// for the intraday flag pass, mirroring Curves' ctrl-closure fan-out (the read
// runs inside each shard's single-writer goroutine, so it never races live
// updates). The engine owns the bucket math: the series ends at the bucket of
// e.now(). Read-time gating (capture stays ungated): tickers with fewer than
// minSessionBarsBuckets observed buckets or session volume under
// minSessionBarsVol are omitted, bounding the map to the active fraction of
// the market. Bars are oldest→newest; Date is the bucket's ET start time
// ("2006-01-02 15:04"), so the last bar's Date feeds FlagRef.AsOf.
func (e *Engine) SessionBars() map[string][]core.DailyBar {
	now := e.now().In(e.loc)
	cur := bucketOf(now)
	// Bucket labels are identical for every ticker; format each once here, not
	// once per ticker inside the shards.
	labels := make([]string, cur+1)
	base := time.Date(now.Year(), now.Month(), now.Day(), 4, 0, 0, 0, e.loc)
	for b := range labels {
		labels[b] = base.Add(time.Duration(b) * 5 * time.Minute).Format("2006-01-02 15:04")
	}
	reply := make(chan map[string][]core.DailyBar, len(e.shards))
	for _, sh := range e.shards {
		sh.ctrl <- func(s *shard) {
			m := make(map[string][]core.DailyBar)
			for sym, t := range s.state {
				if bars := t.sessionBars(cur, labels); bars != nil {
					m[sym] = bars
				}
			}
			reply <- m
		}
	}
	out := make(map[string][]core.DailyBar)
	for range e.shards {
		for k, v := range <-reply {
			out[k] = v
		}
	}
	return out
}

// PmVolLeaders returns the tracked tickers whose pre-market volume is at least
// minX times their 10-day average daily volume -- the candidate list of the
// 9EMA-flag market scan -- sorted by that multiple descending (ties by symbol,
// so the order is deterministic) and capped at limit entries (limit <= 0 =
// uncapped, the same convention as a scan's row Limit; a caller that wants to
// REPORT how many qualified beyond its cap asks uncapped and trims itself).
//
// It is data only and mirrors Curves/SessionBars: a ctrl-closure fan-out, so
// every read of a ticker's state happens on its owning shard goroutine and
// nothing is added to the per-tick hot path. Like them it blocks until every
// shard has answered, so it must only be called while Run is live. The
// multiple comes from tickerState.pmVolX -- the very method toRow uses for the
// pmVolX scan field -- so a symbol is a candidate exactly when its pmVolX
// column reads >= minX: live during pre-market, the frozen COMPLETE
// pre-market value afterwards, and a symbol with an unknown adv10 or only a
// partial frozen pre-market is absent (unknown is not "enough"). The session
// is read once up front so every shard evaluates against the same value.
//
// The result is a fresh slice the caller owns. A NaN minX matches nothing.
func (e *Engine) PmVolLeaders(minX float64, limit int) []core.PmVolEntry {
	sess := core.Session(e.session.Load())
	reply := make(chan []core.PmVolEntry, len(e.shards))
	for _, sh := range e.shards {
		sh.ctrl <- func(s *shard) {
			var m []core.PmVolEntry
			for sym, t := range s.state {
				if x, ok := t.pmVolX(sess); ok && x >= minX {
					m = append(m, core.PmVolEntry{Sym: sym, PmVolX: x})
				}
			}
			reply <- m
		}
	}
	var out []core.PmVolEntry
	for range e.shards {
		out = append(out, (<-reply)...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PmVolX != out[j].PmVolX {
			return out[i].PmVolX > out[j].PmVolX
		}
		return out[i].Sym < out[j].Sym
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Snapshots returns the merged-snapshot stream consumed by the scanner.
func (e *Engine) Snapshots() <-chan *core.MetricSnapshot { return e.out }

// Recycle hands back a snapshot from Snapshots() that its consumer is done
// with: no goroutine reads or writes it, its rows or anything taken from them
// by pointer after the call. The merger reuses its Rows buffer for a later
// snapshot. Optional: a snapshot never recycled is garbage collected.
func (e *Engine) Recycle(snap *core.MetricSnapshot) {
	if snap == nil || cap(snap.Rows) == 0 {
		return
	}
	select {
	case e.free <- snap.Rows:
	default: // enough spares already
	}
}

// DildoHits is the GREEN DILDO hand-off: every dildo the closed-bar judge finds
// (alert 1), for the flagscan service to admit the symbol as a HOT name and
// judge alert 2 (the 9 EMA pullback) on the chart's bars. The engine's send is
// non-blocking (a full buffer drops the hit and counts it, DildoDrops), so a
// slow or absent reader never stalls a shard. nil when Options.Dildo is off - a
// nil channel never delivers, which is "off" to a select.
func (e *Engine) DildoHits() <-chan core.DildoHit {
	if e.sig == nil || e.sig.hits == nil {
		return nil
	}
	return e.sig.hits
}

// DildoDrops reports how many dildo hits were dropped because the DildoHits
// buffer was full (for the flagscan status line). 0 when the judge is off.
func (e *Engine) DildoDrops() int64 {
	if e.sig == nil {
		return 0
	}
	return e.sig.drops.Load()
}

// SignalHits is the SIGNAL HISTORY hand-off: every alert the closed-bar judge
// calls - a GREEN DILDO, a VOLUME CLIMAX, its BOTTOM CONFIRMED or CLIMAX FAILED
// (an expiry is silent, as on the chart) - in the chart rule's event
// vocabulary, for internal/flaglog to record. Each judged bar is judged once,
// so each alert is handed over once. The send is non-blocking (a full buffer
// drops the alert and counts it, SignalDrops). nil unless Options.SignalLog and
// at least one of the judges is on.
func (e *Engine) SignalHits() <-chan core.SignalHit {
	if e.sig == nil || e.sig.events == nil {
		return nil
	}
	return e.sig.events
}

// SignalDrops reports how many alerts were dropped because the SignalHits
// buffer was full. 0 when the hand-off is off.
func (e *Engine) SignalDrops() int64 {
	if e.sig == nil {
		return 0
	}
	return e.sig.evDrops.Load()
}

// refreshEtOff stores the UTC->ET offset in force now (the zone of e.loc).
func (e *Engine) refreshEtOff() {
	_, off := e.now().In(e.loc).Zone()
	e.etOffMs.Store(int64(off) * 1000)
}

// Tracked returns the number of distinct tickers seen.
func (e *Engine) Tracked() int { return int(e.tracked.Load()) }

// Run starts the shards, the 1 Hz clock, and the merger, and blocks until ctx is
// cancelled.
func (e *Engine) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for _, s := range e.shards {
		s := s
		wg.Add(1)
		go func() { defer wg.Done(); s.run(ctx) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); e.runClock(ctx) }()
	wg.Add(1)
	go func() { defer wg.Done(); e.runMerger(ctx) }()

	<-ctx.Done()
	wg.Wait()
	return nil
}

// runClock fans a wall-clock second to every shard each second (non-blocking;
// shards self-catch-up via syncTo's gap fill if a tick is dropped).
func (e *Engine) runClock(ctx context.Context) {
	tk := time.NewTicker(time.Second)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tk.C:
			sec := now.Unix()
			if sec%300 == 0 {
				e.logEmvBlocked()
			}
			e.refreshEtOff() // a DST change lands within a second; the shards read it once per tick
			for _, s := range e.shards {
				select {
				case s.tick <- sec:
				default:
				}
			}
		}
	}
}

// EmvBlocked reports how many intrabar 9EMA VWAP crosses the universe blocked
// (geometrically valid, counted once per symbol-minute), per leg: the bar's $
// volume, RVOL, and everything else.
func (e *Engine) EmvBlocked() (dv, rvol, other int64) {
	if e.sig == nil {
		return 0, 0, 0
	}
	b := &e.sig.emvBlocked
	return b[emvLegDv-1].Load(), b[emvLegRvol-1].Load(), b[emvLegOther-1].Load()
}

// logEmvBlocked writes the blocked-cross totals (every 5 minutes, only when
// the 9EMA VWAP judge is on).
func (e *Engine) logEmvBlocked() {
	if e.sig == nil || !e.sig.emv {
		return
	}
	dv, rvol, other := e.EmvBlocked()
	logx.L().Info("emavwap: intrabar crosses blocked by the universe", "dv", dv, "rvol", rvol, "other", other)
}

// runMerger requests per-shard rows every tick, assembles a fresh immutable
// snapshot (no buffer reuse), and publishes it (dropping if the scanner lags).
func (e *Engine) runMerger(ctx context.Context) {
	tk := time.NewTicker(e.tickHz)
	defer tk.Stop()
	reply := make(chan []core.Row, len(e.shards))
	var gen uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			gen++
			now := e.now()
			req := e.newSnapReq(now, reply)
			for _, s := range e.shards {
				s.snap <- req
			}
			var rows []core.Row
			select {
			case rows = <-e.free:
				rows = rows[:0]
			default:
				rows = make([]core.Row, 0, e.tracked.Load())
			}
			for range e.shards {
				rows = append(rows, (<-reply)...)
			}
			snap := e.assembleSnapshot(gen, now.UnixMilli(), req.sess, rows)
			select {
			case e.out <- snap:
			default: // scanner is behind; drop, next tick supersedes
			}
		}
	}
}

// newSnapReq builds the snapshot request every shard answers for the instant
// now: the 5-minute bucket, the reference tables and the session as they stand,
// and the clock the signal fields age to.
func (e *Engine) newSnapReq(now time.Time, reply chan []core.Row) snapReq {
	floats, secTypes := e.floats.Load(), e.secTypes.Load()
	return snapReq{
		bucket:      bucketOf(now.In(e.loc)),
		baseline:    e.baseline.Load(),
		floats:      *floats,
		secTypes:    *secTypes,
		floatsRef:   floats,
		secTypesRef: secTypes,
		sess:        core.Session(e.session.Load()),
		nowMs:       now.UnixMilli(), // == the snapshot's AsOf
		etOff:       e.etOffMs.Load(),
		reply:       reply,
	}
}

// assembleSnapshot wraps the merged shard rows into the immutable snapshot the
// scanner consumes, with its sym -> position index. Only the merger (or a test
// driving the engine synchronously) calls it.
func (e *Engine) assembleSnapshot(gen uint64, asOfMs int64, sess core.Session, rows []core.Row) *core.MetricSnapshot {
	idx, idxGen := e.ix.index(rows)
	return &core.MetricSnapshot{
		Gen:      gen,
		AsOf:     asOfMs,
		Sess:     sess,
		Rows:     rows,
		Index:    idx,
		IndexGen: idxGen,
	}
}

// snapIndex builds the snapshots' sym -> position Index. The shards list their
// tickers in a stable order, so consecutive snapshots almost always carry the
// same symbols in the same order: then the previous map, read-only once
// published, is shared instead of rebuilt.
type snapIndex struct {
	syms []string // the row order idx was built for
	idx  map[string]int
	gen  uint64
}

func (x *snapIndex) index(rows []core.Row) (map[string]int, uint64) {
	if x.idx != nil && len(rows) == len(x.syms) {
		same := true
		for i := range rows {
			if rows[i].Sym != x.syms[i] {
				same = false
				break
			}
		}
		if same {
			return x.idx, x.gen
		}
	}
	idx := make(map[string]int, len(rows))
	x.syms = x.syms[:0]
	for i := range rows {
		idx[rows[i].Sym] = i
		x.syms = append(x.syms, rows[i].Sym)
	}
	x.idx = idx
	x.gen++
	return idx, x.gen
}

// bucketOf maps an ET timestamp to its 5-minute bucket index (0..191), clamped.
func bucketOf(t time.Time) int {
	mins := t.Hour()*60 + t.Minute()
	b := (mins - 240) / 5 // 240 = 04:00 ET
	if b < 0 {
		return 0
	}
	if b >= core.BucketCount {
		return core.BucketCount - 1
	}
	return b
}

func roundPow2(n int) int {
	if n < 1 {
		n = 1
	}
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}

// SetPrevLows installs each symbol's previous-day low (internal/dlevel
// LowTracker) for the PDL Break fields. Same contract as SetDailyLevels: an
// unchanged low keeps its crossing state; absent/untracked symbols are skipped.
func (e *Engine) SetPrevLows(lows map[string]float32) {
	if len(lows) == 0 {
		return
	}
	for _, sh := range e.shards {
		sh.ctrl <- func(s *shard) {
			for sym, lo := range lows {
				if t, ok := s.state[sym]; ok {
					t.pdl.Set(lo)
				}
			}
		}
	}
}

// SetDailyLevels installs each symbol's daily levels (internal/dlevel) for the
// Daily Breakout fields. A symbol's list is replaced only when it differs (the
// crossing state is kept otherwise, so callers may re-apply freely); symbols
// absent from levels keep theirs; untracked ones are skipped. The day reset
// clears the crosses but not the levels — the loader recomputes them when the
// ET date or the newest stored daily bar changes. levels is read-only/shared.
func (e *Engine) SetDailyLevels(levels map[string][]dlevel.Level) {
	if len(levels) == 0 {
		return
	}
	for _, sh := range e.shards {
		sh.ctrl <- func(s *shard) {
			for sym, lv := range levels {
				if t, ok := s.state[sym]; ok {
					t.dl.Set(lv)
				}
			}
		}
	}
}
