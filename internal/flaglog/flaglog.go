// Package flaglog records the signals the Signal window's SIGNALS history
// shows - the market-wide 9EMA flag scan's events (internal/flagscan OnEval),
// the GREEN DILDO / 9 EMA PULLBACK / PB CANCELLED of the dildo rule (flagscan
// OnDildo, every tracked symbol, hot-only names included) and the engine's own
// alerts (Engine.SignalHits: GREEN DILDO, VOLUME CLIMAX, BOTTOM CONFIRMED,
// CLIMAX FAILED) - so it can show an activity list of "stocks that made a
// signal" and view past days back. Every source shares one identity, (sym, bar
// start, kind) in the chart rule's vocabulary, so the engine's dildo and the
// chart rule's dildo on the same bar are one row. The dildo and climax rows
// carry a one-line note of what the rule measured (notes.go).
//
// Every flagscan evaluation hands over emaflag.Result.Events (dildopb's for
// OnDildo), which is CUMULATIVE (every event of every day in the bars, oldest
// first), so the same events come round on every evaluation. The Recorder
// keeps a seen-set keyed (sym, bar start, kind) for TODAY (ET) and records
// each key exactly once: the event goes into the in-memory day list, is queued
// for the writer goroutine (Run -> core.FlagEventRepo.Insert) and is published
// to the live clients as a flagevents.add frame. Events of earlier days that
// still sit in the bars are ignored: they were recorded on their own day.
//
// A scanner restart mid-day replays the morning's signals in the first
// (unprimed) evaluation of each symbol. Load() seeds the seen-set from the
// repo before the scan starts, so what was already persisted is recognised as
// known and not pushed as new; whatever was NOT persisted (a failed write) is
// recorded now, which heals the gap. The engine's alerts are never replayed
// (each bar is judged once, live), so a batch the full writer queue cannot
// take is held for the writer, not dropped, and a Load that fails is retried
// (when today is served, and by the writer) and merged, so the morning's rows
// are not hidden for the rest of the session.
//
// A dildo's pullback has ONE verdict: once a 9 EMA PULLBACK of a dildo is
// recorded, a later evaluation that puts that dildo's verdict on another bar
// or flips it (a REST resync rewrote a bar) adds no second row; a recorded PB
// CANCELLED blocks a second cancel the same way but NOT the 9 EMA PULLBACK the
// rule may call instead after a rewrite (a tap bar that missed the minute's
// last prints, a bad print REST removed): that PB is alert 2 itself and the
// chart draws it, so it gets its row next to the cancel's. Load rebuilds that
// guard from today's rows - each verdict row stores the dildo it judged
// (FlagEvent.Judged); rows from before that was stored are inferred from the
// row order - and a restart's replay of a recorded verdict names its dildo too.
//
// OnEval / OnDildo run on the flagscan loop goroutine, OnSignal on RunSignals'
// goroutine; none of them blocks: each takes one mutex (contended only by the
// others and by Day, which holds it for a slice copy), hands the new events to
// a buffered channel with a non-blocking send and calls the publish hook,
// which must also not block (server.PublishFlagEvents fans out through
// per-client non-blocking pushes).
package flaglog

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"

	_ "time/tzdata" // America/New_York on a Windows box with no OS zone files (repo convention)

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
	"github.com/eelkonkoopman/marketscanner/internal/flagscan"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

const (
	keepDays    = 30               // days of history kept (and the most dates offered)
	queueCap    = 256              // writer queue, in batches
	overflowCap = 4096             // batches held for the writer while the queue is full
	retryEvery  = 15 * time.Second // the writer's retry of a failed Load
	dayLayout   = "2006-01-02"
	finalFlush  = 2 * time.Second
	writeLimit  = 10 * time.Second
)

type key struct {
	sym  string
	ts   int64
	kind string
}

// batch is one OnEval's worth of new events, all of the ET day `day`.
type batch struct {
	day string
	evs []core.FlagEvent
}

// Recorder records the history's signals. Create with New; the zero value is
// not usable.
type Recorder struct {
	repo core.FlagEventRepo
	now  func() time.Time
	loc  *time.Location

	publish func(core.FlagEventsMsg) // set before Run/OnEval; nil = no live push
	queue   chan batch

	mu      sync.Mutex
	day     string // ET date the slice and seen-set belong to ("" until first use)
	startMs int64  // [startMs, endMs) is `day` in ms
	endMs   int64
	events  []core.FlagEvent // today's events in recording order
	seen    map[key]struct{}
	verdict map[key]string // {sym, dildo bar}: the strongest pullback verdict of that dildo recorded today (dildopb.KindCancel < KindPb)
	drops   int            // batches the full writer queue could not take (held in overflow)
	lost    int            // batches dropped for good (overflow full too)

	overflow []batch       // batches held for the writer while the queue was full
	wake     chan struct{} // cap 1: tells the writer overflow is waiting
	loadDay  string        // the ET day whose Load failed, to retry ("" = none)
}

// New builds a recorder over repo. now is the clock (nil = time.Now); it is
// used for At stamps, Load, Day("") and the engine's alerts - evaluations
// carry their own clock (EvalUpdate.NowMs / DildoUpdate.NowMs).
func New(repo core.FlagEventRepo, now func() time.Time) *Recorder {
	return newRecorder(repo, now, queueCap)
}

func newRecorder(repo core.FlagEventRepo, now func() time.Time, qcap int) *Recorder {
	if now == nil {
		now = time.Now
	}
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.UTC // unreachable with time/tzdata embedded
	}
	return &Recorder{
		repo:    repo,
		now:     now,
		loc:     loc,
		queue:   make(chan batch, qcap),
		wake:    make(chan struct{}, 1),
		seen:    map[key]struct{}{},
		verdict: map[key]string{},
	}
}

// SetPublish sets the hook called with every batch of new events (Date =
// today, Events = only the new ones). It must not block. Call before Run /
// OnEval start; nil is allowed (no live push).
func (r *Recorder) SetPublish(fn func(core.FlagEventsMsg)) {
	r.mu.Lock()
	r.publish = fn
	r.mu.Unlock()
}

// dayBounds returns the ET date string and its [start, end) ms for the day
// containing ms.
func (r *Recorder) dayBounds(ms int64) (string, int64, int64) {
	t := time.UnixMilli(ms).In(r.loc)
	y, m, d := t.Date()
	start := time.Date(y, m, d, 0, 0, 0, 0, r.loc)
	end := time.Date(y, m, d+1, 0, 0, 0, 0, r.loc)
	return start.Format(dayLayout), start.UnixMilli(), end.UnixMilli()
}

func (r *Recorder) etDate(ms int64) string {
	d, _, _ := r.dayBounds(ms)
	return d
}

// rollTo switches the in-memory day when ms belongs to a different one. Caller
// holds r.mu.
func (r *Recorder) rollTo(ms int64) {
	if r.day != "" && ms >= r.startMs && ms < r.endMs {
		return
	}
	r.day, r.startMs, r.endMs = r.dayBounds(ms)
	r.events = nil
	r.seen = map[key]struct{}{}
	r.verdict = map[key]string{}
}

// Load prunes history to 30 days, then loads TODAY's persisted events into
// memory and the seen-set so a restart's replay of the morning is not
// re-pushed as new. Call it synchronously before the scan starts.
func (r *Recorder) Load(ctx context.Context) error {
	nowMs := r.now().UnixMilli()
	today := r.etDate(nowMs)
	if err := r.repo.Prune(ctx, keepDays, today); err != nil {
		logx.L().Warn("flaglog: prune failed; continuing", "err", err)
	}
	evs, err := r.repo.Day(ctx, today)
	if err != nil {
		r.mu.Lock()
		r.loadDay = today // retried by Day(today) and the writer (retryLoad)
		r.mu.Unlock()
		return err
	}
	r.mu.Lock()
	r.loadDay = ""
	r.rollTo(nowMs)
	r.events = append(r.events[:0], evs...)
	r.seen = make(map[key]struct{}, len(evs))
	for _, e := range evs {
		r.seen[key{e.Sym, e.Ts, e.Kind}] = struct{}{}
	}
	r.verdict = verdictsOf(evs)
	r.mu.Unlock()
	logx.L().Info("flaglog: loaded today's signals", "day", today, "events", len(evs))
	return nil
}

// retryLoad retries a failed Load: it reads the day again and merges its rows
// into memory, the seen-set and the verdict guard (a row recorded live since
// keeps its in-memory copy). A no-op when no Load failed or the day has
// rolled over since. Never call it holding r.mu (it reads the repo).
func (r *Recorder) retryLoad(ctx context.Context) {
	r.mu.Lock()
	day := r.loadDay
	r.mu.Unlock()
	if day == "" {
		return
	}
	evs, err := r.repo.Day(ctx, day)
	if err != nil {
		logx.L().Warn("flaglog: loading today's signals failed again; will retry", "day", day, "err", err)
		return
	}
	start, err := time.ParseInLocation(dayLayout, day, r.loc)
	if err != nil {
		return // unreachable: day is etDate's
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loadDay != day {
		return // another retry merged it
	}
	r.loadDay = ""
	if r.day != "" && r.day != day {
		return // the day rolled over: that day is served from the repo
	}
	r.rollTo(start.UnixMilli())
	n := 0
	for _, e := range evs {
		k := key{e.Sym, e.Ts, e.Kind}
		if _, ok := r.seen[k]; ok {
			continue
		}
		r.seen[k] = struct{}{}
		r.events = append(r.events, e)
		n++
	}
	for k, v := range verdictsOf(evs) {
		r.verdict[k] = stronger(r.verdict[k], v)
	}
	logx.L().Info("flaglog: loaded today's signals on retry", "day", day, "events", len(evs), "merged", n)
}

// verdictsOf rebuilds the one-verdict-per-dildo guard from a day's rows. A
// verdict row that stores its dildo (Judged) guards exactly that dildo. An
// older row does not say which dildo its verdict judged, and a dildo row may be the
// engine's alone (a bar the chart rule never called a dildo), so a 9 EMA
// PULLBACK / PB CANCELLED row guards EVERY dildo row of its symbol from the
// previous verdict's bar up to (not including) its own: the rule judges a
// dildo's pullback from the next bar on and judges its LATEST dildo, so the
// earlier ones in that span were superseded and guarding them costs nothing;
// a verdict bar may itself be a new dildo, judged by the next verdict. A
// verdict with no dildo row since the previous verdict re-judges that
// verdict's dildos (a PB CANCELLED a resync turned into a 9 EMA PULLBACK).
// The restart's replay adds the rule's own naming on top (OnDildo).
func verdictsOf(evs []core.FlagEvent) map[key]string {
	order := map[string]int{dildopb.KindCancel: 0, dildopb.KindPb: 1, dildopb.KindDildo: 2} // per bar: verdicts first, the cancel a PB superseded before it
	bySym := map[string][]core.FlagEvent{}
	for _, e := range evs {
		if _, ok := order[e.Kind]; ok {
			bySym[e.Sym] = append(bySym[e.Sym], e)
		}
	}
	done := map[key]string{}
	for sym, rows := range bySym {
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].Ts != rows[j].Ts {
				return rows[i].Ts < rows[j].Ts
			}
			return order[rows[i].Kind] < order[rows[j].Kind]
		})
		var pending, judged []int64 // dildo bars since the previous verdict / that verdict's
		for _, e := range rows {
			if e.Kind == dildopb.KindDildo {
				pending = append(pending, e.Ts)
				continue
			}
			if e.Judged != 0 { // exact; the inference below is for rows that predate Judged
				k := key{sym: sym, ts: e.Judged}
				done[k] = stronger(done[k], e.Kind)
				pending, judged = nil, nil
				continue
			}
			if len(pending) > 0 {
				judged, pending = pending, nil
			}
			for _, ts := range judged {
				k := key{sym: sym, ts: ts}
				done[k] = stronger(done[k], e.Kind)
			}
		}
	}
	return done
}

// stronger is the stronger of two recorded verdicts: a 9 EMA PULLBACK over a
// PB CANCELLED over none.
func stronger(a, b string) string {
	if a == dildopb.KindPb || b == "" {
		return a
	}
	return b
}

// claim reports whether (sym, ts, kind) is a signal of today not recorded
// yet, and marks it recorded. Caller holds r.mu (after rollTo).
func (r *Recorder) claim(sym string, ts int64, kind string) bool {
	if ts < r.startMs || ts >= r.endMs {
		return false // another day's event, recorded on its own day
	}
	k := key{sym, ts, kind}
	if _, ok := r.seen[k]; ok {
		return false
	}
	r.seen[k] = struct{}{}
	return true
}

// finite is a price as the row stores it: nil for NaN / Inf.
func finite(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

// OnEval is the flagscan.Config.OnEval hook (see the package comment). It
// records the not-yet-seen events of today (the ET date of u.NowMs), including
// the unprimed history-replay evaluation, and never blocks.
func (r *Recorder) OnEval(u flagscan.EvalUpdate) {
	if len(u.Result.Events) == 0 {
		return
	}
	atMs := r.now().UnixMilli()

	r.mu.Lock()
	r.rollTo(u.NowMs)
	var fresh []core.FlagEvent
	for _, e := range u.Result.Events {
		if !r.claim(u.Sym, e.Ts, e.Kind) {
			continue
		}
		fresh = append(fresh, core.FlagEvent{Ts: e.Ts, At: atMs, Sym: u.Sym, Kind: e.Kind, Label: e.Label, Price: finite(e.Price)})
	}
	r.commit(fresh)
}

// OnDildo is the flagscan.Config.OnDildo hook: it records the not-yet-seen
// GREEN DILDO, 9 EMA PULLBACK and PB CANCELLED events of today (the ET date of
// u.NowMs) - one pullback verdict per dildo (see the package comment) - with
// the rule's own note minus the clock time the row already shows. Runs on the
// flagscan loop goroutine and never blocks; u.Result is only read.
func (r *Recorder) OnDildo(u flagscan.DildoUpdate) {
	if len(u.Result.Events) == 0 {
		return
	}
	atMs := r.now().UnixMilli()

	r.mu.Lock()
	r.rollTo(u.NowMs)
	var fresh []core.FlagEvent
	var anchor int64 // the dildo whose pullback the next verdict judges (the rule's latest)
	for i := range u.Result.Events {
		e := &u.Result.Events[i]
		var judged int64
		switch e.Kind {
		case dildopb.KindDildo:
			anchor = e.Ts
			if !r.claim(u.Sym, e.Ts, e.Kind) {
				continue
			}
		case dildopb.KindPb, dildopb.KindCancel:
			vk := key{sym: u.Sym, ts: anchor}
			had := r.verdict[vk]
			if had == dildopb.KindPb || (had != "" && e.Kind == dildopb.KindCancel) {
				continue // this dildo's verdict is recorded (a resync moved or flipped it)
			}
			if _, recorded := r.seen[key{u.Sym, e.Ts, e.Kind}]; recorded {
				r.verdict[vk] = e.Kind // a repeat, or a restart's replay of a loaded row: the rule names its dildo
				continue
			}
			if !r.claim(u.Sym, e.Ts, e.Kind) {
				continue
			}
			r.verdict[vk] = e.Kind // a PB here may supersede a recorded cancel: alert 2 gets its row
			judged = anchor        // stored, so a restart's Load rebuilds this guard exactly
		default:
			continue // a kind the history does not know
		}
		note := dildoNote(e)
		if e.Kind == dildopb.KindDildo && u.Early[e.Ts] {
			note = EarlyNotePrefix + note
		}
		fresh = append(fresh, core.FlagEvent{Ts: e.Ts, At: atMs, Sym: u.Sym, Kind: e.Kind, Label: e.Label,
			Price: finite(e.Price), Note: note, Judged: judged})
	}
	r.commit(fresh)
}

// OnSignal records one of the engine's alerts (Engine.SignalHits: GREEN DILDO,
// VOLUME CLIMAX, BOTTOM CONFIRMED, CLIMAX FAILED) if it is today's (by the
// recorder's clock) and not recorded yet, with its note. Never blocks.
func (r *Recorder) OnSignal(h core.SignalHit) {
	label, note, ok := r.signalText(h)
	if !ok {
		return
	}
	atMs := r.now().UnixMilli()
	r.mu.Lock()
	r.rollTo(atMs)
	var fresh []core.FlagEvent
	if r.claim(h.Sym, h.BarMs, h.Kind) {
		fresh = []core.FlagEvent{{Ts: h.BarMs, At: atMs, Sym: h.Sym, Kind: h.Kind, Label: label, Price: finite(h.Price), Note: note}}
	}
	r.commit(fresh)
}

// RunSignals records every alert arriving on hits (Engine.SignalHits) until
// ctx ends or hits is closed; a nil hits (the hand-off off) returns at once.
// drops (Engine.SignalDrops; nil = not tracked) counts the alerts the engine
// dropped because hits was full: rows missing from the history for good (the
// engine's alerts are never replayed), so a rise is logged. Checked after
// every alert, which is enough: a drop means a full buffer still to drain.
func (r *Recorder) RunSignals(ctx context.Context, hits <-chan core.SignalHit, drops func() int64) error {
	if hits == nil {
		return nil
	}
	var seenDrops int64
	for {
		select {
		case <-ctx.Done():
			return nil
		case h, ok := <-hits:
			if !ok {
				return nil
			}
			r.OnSignal(h)
			if drops == nil {
				continue
			}
			if d := drops(); d > seenDrops {
				logx.L().Warn("flaglog: engine alerts dropped (the history fell behind); missing from the Signals list",
					"count", d-seenDrops, "total", d)
				seenDrops = d
			}
		}
	}
}

// commit appends fresh (today's newly claimed events) to the day, queues them
// for the writer and publishes them. Caller holds r.mu; commit releases it
// (the publish runs unlocked).
func (r *Recorder) commit(fresh []core.FlagEvent) {
	if len(fresh) == 0 {
		r.mu.Unlock()
		return
	}
	r.events = append(r.events, fresh...)
	day := r.day
	pub := r.publish
	// Non-blocking hand-off to the writer. On a full queue (a long SQLite
	// write stall) the batch is held in overflow and the writer woken: it
	// writes it once it catches up - the engine's alerts are never replayed,
	// so a dropped batch would be gone at the next restart or midnight. Only
	// past overflowCap is a batch dropped for good (still shown today).
	b := batch{day: day, evs: append([]core.FlagEvent(nil), fresh...)}
	select {
	case r.queue <- b:
	default:
		r.drops++
		if len(r.overflow) < overflowCap {
			r.overflow = append(r.overflow, b)
			if r.drops == 1 || r.drops%100 == 0 {
				logx.L().Warn("flaglog: writer queue full; signals held for the writer", "held", len(r.overflow), "drops", r.drops)
			}
		} else {
			r.lost++
			if r.lost == 1 || r.lost%100 == 0 {
				logx.L().Warn("flaglog: writer far behind; signals NOT saved (shown today only, gone after a restart)", "lost", r.lost)
			}
		}
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
	r.mu.Unlock()

	if pub != nil {
		pub(core.FlagEventsMsg{Date: day, Events: append([]core.FlagEvent(nil), fresh...)})
	}
}

// Run is the writer loop: it drains the queue (and the overflow held while it
// was full) into the repo, batching whatever is waiting, until ctx ends, then
// makes a best-effort final drain. Write failures are logged, never fatal. It
// also retries a failed Load (after each write and every retryEvery).
func (r *Recorder) Run(ctx context.Context) error {
	tick := time.NewTicker(retryEvery)
	defer tick.Stop()
	for {
		var first *batch
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalFlush)
			r.flush(fctx, nil)
			cancel()
			return nil
		case b := <-r.queue:
			first = &b
		case <-r.wake:
		case <-tick.C:
			lctx, cancel := context.WithTimeout(ctx, writeLimit)
			r.retryLoad(lctx)
			cancel()
			continue
		}
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeLimit)
		r.flush(wctx, first)
		r.retryLoad(wctx)
		cancel()
	}
}

// flush writes first (if any) plus everything currently queued, one Insert per
// ET day.
func (r *Recorder) flush(ctx context.Context, first *batch) {
	byDay := map[string][]core.FlagEvent{}
	var order []string
	add := func(b batch) {
		if _, ok := byDay[b.day]; !ok {
			order = append(order, b.day)
		}
		byDay[b.day] = append(byDay[b.day], b.evs...)
	}
	if first != nil {
		add(*first)
	}
	for drained := false; !drained; {
		select {
		case b := <-r.queue:
			add(b)
		default:
			drained = true
		}
	}
	r.mu.Lock()
	held := r.overflow
	r.overflow = nil
	r.mu.Unlock()
	for _, b := range held {
		add(b)
	}
	for _, d := range order {
		if err := r.repo.Insert(ctx, d, byDay[d]); err != nil {
			logx.L().Warn("flaglog: saving flag signals failed", "day", d, "events", len(byDay[d]), "err", err)
		}
	}
}

// Day serves one ET day (YYYY-MM-DD; "" = today) for flagevents.get: today from
// memory, other days from the repo, plus the dates that have events (newest
// first, at most 30, always including today).
func (r *Recorder) Day(ctx context.Context, date string) (core.FlagEventsMsg, error) {
	today := r.etDate(r.now().UnixMilli())
	if date == "" {
		date = today
	}
	if date == today {
		r.retryLoad(ctx) // a failed Load: merge the morning before serving today
	}

	var evs []core.FlagEvent
	r.mu.Lock()
	inMem := date == r.day
	if inMem {
		evs = append(evs, r.events...)
	}
	r.mu.Unlock()
	if inMem {
		sort.SliceStable(evs, func(i, j int) bool { return evs[i].Ts < evs[j].Ts })
	} else {
		var err error
		if evs, err = r.repo.Day(ctx, date); err != nil {
			return core.FlagEventsMsg{}, err
		}
	}
	if evs == nil {
		evs = []core.FlagEvent{}
	}

	days, err := r.repo.Days(ctx, keepDays)
	if err != nil {
		return core.FlagEventsMsg{}, err
	}
	days = append(days, today)
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	out := make([]string, 0, len(days))
	for _, d := range days {
		if n := len(out); n > 0 && out[n-1] == d {
			continue
		}
		out = append(out, d)
	}
	if len(out) > keepDays {
		out = out[:keepDays]
	}
	return core.FlagEventsMsg{Date: date, Dates: out, Events: evs}, nil
}
