// Package flagscan is the 9EMA-flag MARKET scan: it watches every stock whose
// pre-market volume has reached at least 1x its 10-day average daily volume
// (the scan field pmVolX) and runs, on each of them, the very same 1-minute
// 9 EMA flag state machine the Signal window runs - idle -> impulse -> flat ->
// ready -> long - all day, with no chart window open. The scanner overlays the
// published verdicts onto its rows as the e9State ... e9Age columns, and three
// seeded scans (sideways / ready / breakout) turn the stages the trader already
// knows from the Signal window into alerts. The same evaluation carries the
// engine's one bearish alert, the 9 EMA SLAM (a flag or position slammed
// through the 9 EMA by one big closed candle): the day's latest slam rides on
// the ref (SlamMs / SlamPct) into the e9SlamAge / e9SlamPct columns and the
// seeded "9EMA Slam" scan.
//
// WHY it is a service of its own. The rule set is geometry, and project law is
// that geometry never runs in the engine's hot path; a whole-market 1-minute
// OHLCV ring (~12k tickers x a day of bars) would buy a setup that only matters
// on a few dozen names; and evaluating in the browser would be capped at the 16
// chart.watch symbols per window and only work while a window is open. So the
// engine only names the candidates (Engine.PmVolLeaders), the ingest only tees
// their per-second aggregates (Ingest.SetFlagSyms / FlagAggs), and this loop
// owns the bars and the geometry - the shape internal/flagscan shares with
// cmd/scanner's intraday-flag pass (iflags.go), which also keeps its geometry
// out of the engine and publishes a map for the scanner to overlay.
//
// THE DATA PATH IS THE SIGNAL WINDOW'S, ON PURPOSE. For each candidate: the
// REST 1-minute bars of the previous trading day plus today (the chart.get
// source, feed.Client.IntradayWithPrevDay) on admission; then the live
// per-second aggregates folded into the forming minute with the chart's own
// rule (store.ts foldMinute, see foldAgg); then a REST resync every five
// minutes, which heals what a reconnect gap or a dropped aggregate lost. The
// same bars into the same rules (internal/emaflag, golden-tested against the
// TypeScript engine) mean the scan's state for a symbol is the state the Signal
// window shows for it - which is what lets a trader trust an alert enough to
// open the chart and find the same flag there.
//
// Shape of the loop (Run): one goroutine owns every symbol's state, so nothing
// here is locked. It selects over the aggregate tap, the results of the two
// kinds of background work (the candidate query and the REST fetches, each in
// its own short-lived goroutine so a slow engine fan-out or a slow REST call
// can never stop it draining the tap) and a one-second tick. The tick starts
// due work, evaluates every symbol whose bars changed (or whose last bar just
// closed), and publishes the whole verdict map when any verdict changed. A
// symbol is published only once its REST history has loaded and it has a bar
// today: a half-loaded series would put the EMA warm-up and the "five prior
// bars" impulse gate on the wrong bars, and a verdict computed from
// yesterday's bars would be yesterday's alert.
//
// THE TWO HOOKS FOR A CONSUMER THAT ACTS (internal/autotrade). Config.OnEval
// hands each fresh evaluation - the full engine Result, the latest bar and a
// Primed flag - to a callback that runs on the loop goroutine, so the consumer
// sees every symbol's evaluations in order with no locking on this side; it
// must return at once (a non-blocking send into a channel it owns, counting
// what it drops). Config.Pinned names the symbols that must stay candidates
// whatever their pmVolX rank or the cap (a trader's open positions and working
// orders), so the data an open position depends on never stops. Both are
// optional and, when nil, change nothing: the scan is still only a decision
// aid, and this package itself still places and manages nothing.
//
// What it does NOT do: place or manage anything, look at symbols below the
// pmVolX threshold (a name only becomes a candidate once its pre-market volume
// reaches it, so a thin 04:00 market has none; pinned symbols and HOT names
// are the exceptions), or evaluate more than MaxCandidates at once (the cap is
// applied after ranking by pmVolX and the number qualifying beyond it is
// logged, never dropped silently; pinned and hot symbols are kept on top of the
// cap).
//
// GREEN DILDO -> 9 EMA PULLBACK (alert 2). The engine judges every closed
// 1-minute candle of the whole market for a green dildo (alert 1) and hands
// each one over on Config.Hot. The service then tracks that symbol as a HOT
// name for HotHold (30 min) on top of the candidates, through the very same
// path - tap first, REST backfill, plus one early resync EarlyResync after the
// dildo bar so its REST bar is final - and runs internal/dildopb over the
// chart's bars (the chart's continuous 9 EMA across yesterday and today) for
// EVERY tracked symbol, publishing the pullback on the ref (DildoMs .. PbPx).
// A symbol tracked only because it is hot is published with HotOnly set: the
// scanner then fills only its dildo fields (never e9*), and Config.OnEval never
// sees it (the consumer contract stays "pmVolX candidates + pinned"); Config.OnHot
// does (the auto-trader's green dildo strategy). Every update - OnEval's and
// OnHot's - carries the dildo rule's call (EvalUpdate.Dildo), the dildo bar and the
// latest bars.
package flagscan

import (
	"context"
	"errors"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
	"github.com/eelkonkoopman/marketscanner/internal/emaflag"
)

// The thresholds and cadences, in one block.
const (
	// MinPmVolX is the candidate threshold: pre-market volume at least 1x the
	// 10-day average daily volume - the trader's "traded at least 1x their
	// 10-day average volume", inclusive.
	MinPmVolX = 1.0
	// MaxCandidates caps the symbols evaluated at once. 80 keeps the REST budget
	// (one backfill each on admission, one resync per five minutes) around 80
	// backfills plus 16 resyncs a minute at the very worst, far inside Massive's
	// limits, and is several times the number of names that normally qualify.
	MaxCandidates = 80
	// CandidateEvery is how often the candidate list is re-read from the engine.
	// pmVolX only grows during pre-market and freezes at 09:30, so 30 s is
	// prompt enough to admit a name within a bar of it crossing 1x.
	CandidateEvery = 30 * time.Second
	// ResyncEvery is how often each candidate's bars are replaced from REST.
	ResyncEvery = 5 * time.Minute
	// EvalEvery is the evaluation / publish cadence: a verdict reaches the
	// scanner at most about a second after the bar that caused it changed.
	EvalEvery = time.Second
	// CloseGrace is how long past its minute a bar is still treated as forming
	// when no newer bar exists: the last seconds of a minute arrive a little
	// after it ends, and a premature "closed" verdict (a flag break judged on a
	// bar that is about to widen) would flip back a second later. Latency from a
	// bar's close to its verdict is therefore about one to three seconds.
	CloseGrace = 3 * time.Second
	// BackfillWorkers bounds the concurrent REST fetches (admission backfills
	// and resyncs share the pool, backfills first).
	BackfillWorkers = 4

	// HotHold is how long a green dildo keeps its symbol tracked as a HOT name
	// (from the end of the dildo bar; a newer dildo extends it): the pullback is
	// looked for over 20 minutes after the dildo, so 30 covers the window and
	// its close with room for a late REST load.
	HotHold = 30 * time.Minute
	// MaxHot caps the hot set (on top of MaxCandidates). Beyond it the entry
	// with the earliest expiry is evicted (and logged): a burst of more than 40
	// dildos inside 30 minutes is far above the calibrated ~18-29 a DAY.
	MaxHot = 40
	// EarlyResync is when, after the dildo bar's end, a hot symbol whose first
	// REST load could not yet have the dildo minute final gets one extra
	// resync: the REST bar of that minute is then final before the first
	// possible touch bar closes (at least a minute later).
	EarlyResync = 20 * time.Second
)

// Retry and housekeeping constants that no caller tunes.
const (
	retryMin     = 5 * time.Second  // first retry after a failed fetch ...
	retryMax     = 2 * time.Minute  // ... doubling up to here
	fetchTimeout = 45 * time.Second // one REST call; a hung request must not hold a worker
	logEvery     = time.Minute      // the single status line
)

// Config wires the service to the rest of the scanner. Everything is injected
// so the whole service runs against fakes in tests (no network, no engine); the
// production adapter is cmd/scanner/e9flags.go.
type Config struct {
	// Candidates returns the tracked symbols whose pre-market volume is at least
	// minX times their 10-day average daily volume, ranked by that multiple,
	// capped at limit (limit <= 0 = uncapped). Engine.PmVolLeaders. The service
	// always asks uncapped and applies MaxCandidates itself, because only then
	// can it log how many qualified beyond the cap.
	Candidates func(minX float64, limit int) []core.PmVolEntry

	// Bars returns a symbol's 1-minute bars for the previous trading day plus
	// today, ascending. feed.Client.IntradayWithPrevDay. It must honour ctx
	// (the service cancels it on shutdown and after fetchTimeout) and may
	// return a slice it keeps: the service copies it before touching it.
	Bars func(ctx context.Context, sym string) ([]core.Bar, error)

	// Aggs is the per-second aggregate tap (Ingest.FlagAggs): every aggregate of
	// the symbols currently set with SetTap, in arrival order.
	Aggs <-chan core.Agg

	// SetTap replaces the tapped symbol set (Ingest.SetFlagSyms). The service
	// calls it BEFORE it queues the backfill of a newly admitted symbol, so no
	// second between the backfill's cut-off and the first live print is lost.
	SetTap func(syms []string)

	// Publish hands the scanner the complete verdict map (Scanner.SetEmaFlags).
	// The map is fresh per call and never touched again by the service; a
	// symbol absent from it is "not a candidate / not evaluated" and the scan
	// overlay Nulls its e9* fields. An empty map is a real publish: it is what
	// clears the last candidate's verdict when it drops out.
	Publish func(map[string]core.EmaFlagRef)

	// Now is the clock (time.Now in production; a fake in the replay tests).
	Now func() time.Time

	// Params are the engine's thresholds; the zero value means
	// emaflag.DefaultParams() (the Signal window's defaults).
	Params emaflag.Params

	// Drops, optional, reports the tap's running discard count
	// (Ingest.FlagDrops) for the once-a-minute status line; nil omits it.
	Drops func() int64

	// OnEval, optional (nil = off), is called with the outcome of every
	// candidate evaluation: each time the engine ran over a symbol's bars and
	// produced a verdict (bars changed, or the last bar closed since the last
	// run), whether or not the verdict differs from the published one. It is
	// NOT called for a symbol that has no verdict (REST history not loaded yet,
	// no bar today, an engine panic).
	//
	// Contract: it runs on the service goroutine, right after the evaluation,
	// before that tick's Publish, one symbol at a time (strongest pmVolX first,
	// then by symbol, so two symbols that evaluate in the same tick are always
	// delivered in the same order). It MUST NOT block: a slow or blocked
	// callback stalls the aggregate tap, every verdict and the publish. The
	// intended consumer hands the update to a buffered channel it owns with a
	// non-blocking send and counts what it drops. A panic in it is recovered
	// and logged (the evaluation, the verdict and the other symbols are
	// unaffected). The update, including Result.Events, belongs to the callee:
	// the service never touches it again.
	OnEval func(EvalUpdate)

	// OnHot, optional (nil = off), is OnEval for the symbols tracked ONLY because of a
	// green dildo (neither a pmVolX candidate nor pinned): each of their evaluations,
	// with HotOnly set and Primed false (a hot-only symbol is never primed: a later
	// candidacy starts unprimed for OnEval, exactly as without this hook). The same
	// rules as OnEval: the service goroutine, it MUST NOT block, a panic is recovered
	// and counted, the update belongs to the callee. The auto-trader's green dildo
	// strategy is the consumer: a dildo name it trades is pinned from its first order on,
	// and from then on reaches it through OnEval.
	OnHot func(EvalUpdate)

	// Pinned, optional (nil = off), returns the symbols that must stay
	// candidates whatever their pmVolX rank or the MaxCandidates cap - the
	// trader's open positions and working orders - so their bars, tap and
	// verdicts never stop while one is open. Pinned symbols are tracked on top
	// of the cap (the cap still applies to everything else), are admitted even
	// when they are not (or no longer) over the pmVolX threshold, and are
	// released as soon as they leave the returned list (a symbol that is then
	// also not a candidate is dropped like any other).
	//
	// It runs on the service goroutine, at every candidate refresh and once a
	// tick (a change takes effect on the next tick without waiting for the next
	// refresh), so it must be cheap, must not block and must be safe to call
	// concurrently with whatever updates the list - the same rules as OnEval. A
	// panic is recovered and logged, and the previous answer stays in force.
	Pinned func() []string

	// Hot, optional (nil = off), is the engine's GREEN DILDO hand-off
	// (Engine.DildoHits): every closed 1-minute candle the engine judged a green
	// dildo, market-wide. The service reads it in its loop (never calling into
	// the engine), admits the symbol as a HOT name for HotHold on top of the cap
	// and below the pmVolX threshold, and judges the pullback to the chart's
	// 9 EMA on its bars (alert 2). The engine's send is non-blocking.
	Hot <-chan core.DildoHit

	// OnEngineDildo, optional (nil = off), is handed every engine green dildo read
	// from Hot (EngineDildoUpdate) before the chart's bars judge it: the same signal
	// that raises the audible alert, so the auto-trader acts on what the user hears
	// even when the chart's REST bars later disagree. Same contract as OnEval: on
	// the service goroutine, must not block; a panic is recovered.
	OnEngineDildo func(EvalUpdate)

	// Dildo are the dildo rule's thresholds; the zero value means
	// dildopb.DefaultParams() (the calibrated defaults, which the engine's
	// alert-1 judge also uses).
	Dildo dildopb.Params
	// DildoLive, when set, supplies the rule's parameters afresh for every
	// evaluation (the Signal window's tuning panel, shared with the engine
	// judge) and replaces Dildo. It must be cheap and safe for concurrent use.
	DildoLive func() dildopb.Params

	// DildoDrops, optional, reports how many hits the engine dropped because
	// Hot was full (Engine.DildoDrops), for the status line; nil omits it.
	DildoDrops func() int64

	// OnDildo, optional (nil = off), is called with the dildo rule's call over a
	// tracked symbol's bars whenever an evaluation ran it and it holds at least
	// one event - for EVERY tracked symbol, hot-only names included (OnEval
	// never sees those): the GREEN DILDOs, 9 EMA PULLBACKs and PB CANCELLEDs the
	// Signal window's history records (internal/flaglog). Result.Events is
	// cumulative like OnEval's (every day in the bars, oldest first), so a
	// consumer must remember what it has recorded. Same contract as OnEval: on
	// the service goroutine, right after that symbol's evaluation, must not
	// block, a panic is recovered and logged. The update SHARES the service's
	// Result: read it during the call, never modify it or keep it.
	OnDildo func(DildoUpdate)
}

// DildoUpdate is one dildo-rule evaluation, as handed to Config.OnDildo.
type DildoUpdate struct {
	Sym string
	// NowMs is the service clock at the evaluation (wall time, like
	// EvalUpdate.NowMs); the rule judged the bars as of NowMs - CloseGrace.
	NowMs int64
	// HotOnly: the symbol is tracked only because the engine saw a green dildo
	// on it (neither a pmVolX candidate nor pinned).
	HotOnly bool
	// Result is the rule's call over the symbol's bars (dildopb.Evaluate):
	// every event of every day in them, oldest first.
	Result dildopb.Result
	// Early is the early verdict (dildopb.JudgeEarly on these bars, the
	// trader's rule) of every KindDildo event in Result, keyed on its Ts.
	Early map[int64]bool
}

// EvalUpdate is one evaluation, as handed to Config.OnEval.
type EvalUpdate struct {
	Sym string

	// NowMs is the service clock (Config.Now) at the evaluation, in ms: wall
	// time, NOT reduced by CloseGrace. The engine itself judged the bars as of
	// NowMs - CloseGrace (so a minute that ended less than CloseGrace ago was
	// still forming for it).
	NowMs int64

	// Result is the engine's complete call over the symbol's bars: the state,
	// the levels and every event of every day in the bars, oldest first.
	// Because Events is cumulative, an update always repeats events a previous
	// one carried; a consumer that acts on events must remember the ones it has
	// handled (and must treat the first Primed update's events as history).
	// Events include the bearish KindSlam alerts and Result.LastSlam* the day's
	// latest slam (what the published ref's SlamMs / SlamPct carry): a long-only
	// consumer must act on the kinds it knows and ignore the rest.
	Result emaflag.Result

	// Last is the latest bar in the series the engine saw, FORMING or closed
	// (Last.T + 60 s > NowMs - CloseGrace means it was still forming).
	Last core.Bar

	// Primed is false for exactly one evaluation per admission of a symbol: the
	// first one, which runs over the REST backfill and so replays the symbol's
	// whole history (every event in Result.Events is old news, whatever its
	// timestamp). It is true for every later evaluation of that admission,
	// which only ever add what the live aggregates (or a resync) changed. A
	// symbol that leaves the candidate set and comes back is a new admission:
	// its first evaluation is unprimed again. A consumer must never act on a
	// !Primed update, and should record the events of the first Primed update
	// as seen without acting on them too - the engine reports every event of
	// the day on every update.
	Primed bool

	// HotOnly: tracked only because of a green dildo (neither a pmVolX candidate nor
	// pinned). Such updates come through Config.OnHot, never through OnEval, and are
	// never Primed.
	HotOnly bool
	// Dildo is the dildo rule's call over the same bars at the same clock
	// (dildopb.Evaluate, what the published ref's Dildo* fields come from). Events is
	// nil here: a consumer reads the summary fields (LastDildoTs, Floor, PbState,
	// DeadlineTs). Zero value when the rule is off.
	Dildo dildopb.Result
	// DildoBar is the 1-minute bar of Dildo.LastDildoTs (zero when none, or no longer
	// in the bars).
	DildoBar core.Bar
	// Recent is a COPY of the last up to 40 bars, oldest first; Last is its final
	// element. The consumer may keep it.
	Recent []core.Bar

	// Engine marks an update that is not an evaluation but the engine's own
	// live-candle GREEN DILDO (the hit behind the audible alert), handed on through
	// Config.OnEngineDildo the moment it arrives: Dildo.LastDildoTs and DildoBar are
	// the engine's candle, NowMs the service clock, everything else zero.
	Engine bool
}

// EngineDildoUpdate is the Engine update for one engine green dildo hit.
func EngineDildoUpdate(hit core.DildoHit, nowMs int64) EvalUpdate {
	return EvalUpdate{Sym: hit.Sym, NowMs: nowMs, Engine: true,
		Dildo:    dildopb.Result{LastDildoTs: hit.BarMs},
		DildoBar: core.Bar{T: hit.BarMs, O: hit.O, H: hit.H, L: hit.L, C: hit.C, V: hit.V}}
}

// tuning is the service's timing and limits. Run uses the exported constants;
// the tests shrink them (a one-second tick and a five-minute resync would make
// a faithful replay take real minutes).
type tuning struct {
	minX           float64
	maxCandidates  int
	candidateEvery time.Duration
	resyncEvery    time.Duration
	evalEvery      time.Duration
	closeGrace     time.Duration
	workers        int
	retryMin       time.Duration
	retryMax       time.Duration
	fetchTimeout   time.Duration
	logEvery       time.Duration
	hotHold        time.Duration
	maxHot         int
	earlyResync    time.Duration
}

func defaultTuning() tuning {
	return tuning{
		minX:           MinPmVolX,
		maxCandidates:  MaxCandidates,
		candidateEvery: CandidateEvery,
		resyncEvery:    ResyncEvery,
		evalEvery:      EvalEvery,
		closeGrace:     CloseGrace,
		workers:        BackfillWorkers,
		retryMin:       retryMin,
		retryMax:       retryMax,
		fetchTimeout:   fetchTimeout,
		logEvery:       logEvery,
		hotHold:        HotHold,
		maxHot:         MaxHot,
		earlyResync:    EarlyResync,
	}
}

// Run starts the service and blocks until ctx is cancelled (returning nil), or
// returns an error at once for an unusable Config. It must be started next to
// the engine, ingest and scanner it reads and feeds (cmd/scanner/main.go): the
// candidate query is a ctrl-closure fan-out into a RUNNING engine.
//
// On return no goroutine of the service is left behind, except that an
// engine-side Candidates call which never returns (an engine that stopped
// mid-query) cannot be interrupted - the call is made from its own goroutine
// precisely so that Run itself never waits for it.
func Run(ctx context.Context, cfg Config) error {
	s, err := newService(cfg, defaultTuning())
	if err != nil {
		return err
	}
	return s.run(ctx)
}

func newService(cfg Config, tm tuning) (*service, error) {
	switch {
	case cfg.Candidates == nil:
		return nil, errors.New("flagscan: Config.Candidates is required")
	case cfg.Bars == nil:
		return nil, errors.New("flagscan: Config.Bars is required")
	case cfg.SetTap == nil:
		return nil, errors.New("flagscan: Config.SetTap is required")
	case cfg.Publish == nil:
		return nil, errors.New("flagscan: Config.Publish is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Params == (emaflag.Params{}) {
		cfg.Params = emaflag.DefaultParams()
	}
	if cfg.Dildo == (dildopb.Params{}) {
		cfg.Dildo = dildopb.DefaultParams()
	}
	return newServiceState(cfg, tm), nil
}
