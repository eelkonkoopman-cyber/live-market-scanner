// Package emaflag is the Go port of the 9 EMA FLAG engine the Signal window
// runs in the browser (web/src/signals/emaFlag.ts, evaluateFlag). It exists so
// the market scan (internal/flagscan) can evaluate the SAME long-only setup on
// every pre-market-volume candidate server-side, all day, without a chart
// window being open: idle -> impulse -> flat -> ready -> long.
//
// THE RULE SET IS DEFINED IN TypeScript. This package is a line-by-line port of
// it, not a reinterpretation: same step order inside a closed bar (the
// intrabar rules first - flag break by high and, when the stop is a hard
// order, the stop by low - then the close-based stop/trail, the FAIL gate, the
// impulse, the flag open/extend), the same forming-bar pass on the last bar,
// the same per-ET-day state machine, the same CONTINUOUS EMA across the day
// boundary, the same flag-span, retrace and F-1 "top bar is the thrust, not the
// flag" rules, and the same event kinds and labels. Read emaFlag.ts's header
// first: it carries the trader's description of the setup and the WHY of every
// gate, and none of that is repeated here.
//
// Parity is a guarantee, not a hope: web/src/signals/emaFlag.golden.test.ts
// runs the TS engine over every real fixture x five parameter sets x two clocks
// (all closed / last bar forming) and commits the output as
// web/src/signals/fixtures/golden/emaflag-golden.json; golden_test.go in this
// package replays the same bars through Evaluate and fails on any difference
// in state, event sequence, prices or levels. Change a rule in one language and
// one of the two tests goes red until the other follows (and the golden file is
// regenerated: UPDATE_GOLDEN=1, see the TS test header).
//
// FLAG EXPIRY (Params.MaxFlagBars, 30; see FLAG EXPIRY in emaFlag.ts for the
// why - PTC sat in one "active" flag for 373 bars, READY the whole time). A flag
// whose box, counted in closed bars from its TOP bar inclusive, spans more than
// MaxFlagBars without breaking ends silently (the TS box outcome 'stale', which
// this port does not draw) and the state falls back to impulse. The same test -
// "the top is more than MaxFlagBars bars old" - also refuses to open a flag
// under such a top (it would be born stale), which is what stops the very next
// bar re-opening a flag and printing FLAT again: only a higher high moves the
// top. Decided on a CLOSED bar after that bar's intrabar break, and before its
// READY test, exactly as in TS. 0 switches it off.
//
// 9 EMA SLAM (Params.Slam and the six Slam* thresholds; see 9 EMA SLAM in
// emaFlag.ts for the trader's words, the SPAI 2026-10-07 example and the
// calibration). The bearish alert of the same engine: on a CLOSED bar i whose
// day's state at the end of bar i-1 was flat, ready or long, a candle that
// opened at the 9 EMA (no more than SlamOpenTolPct under bar i-1's; the
// previous close held it) and closed SlamBelowPct under it with a big body
// (SlamBodyAtr x ATR of bar i-1, SlamMinBodyPct of the open), near its low
// (SlamCloseInRange) on volume (SlamVolX x the mean of the up-to-10 earlier
// bars of the day). At most one per flag / position episode (the position and
// the flag open at the end of bar i-1 are each an episode of their own; one
// slam marks both), emitted LAST on its bar (after whatever the bar's long side
// reported - EXIT / FLAG FAILED, or EARLY BREAK when the bar first broke a flag
// that was not yet READY: alone with no position, EARLY BREAK then EXIT while
// long), never on a forming bar. It changes no state, level or long-side
// event; the auto-trader, which acts on KindEntry / KindExit only, never sees a
// trade in it. Result.LastSlamTs / LastSlamClose / LastSlamPct report the latest
// one of the evaluated day for the market scan. This is the one rule that reads
// the ATR, so the port computes ATR(AtrPeriod) - the TS atrSeries, operation
// for operation - when Slam is on.
//
// What is deliberately NOT ported, because the scan never reads it and no rule
// gates on it: the human text (event notes, headline, detail lines), the
// FlagBox drawing geometry and outcomes, the event side (every event is the
// long side's except KindSlam, the one 'short' event), ATR as a level (the TS
// engine exposes it for display), and the evidence numbers other than the ones
// the scan columns need (flag bars, EMA distance, the latest SLAM).
//
// Pure: imports only core (for core.Bar), math and time. No clock reads - the
// caller passes nowMs, exactly like the TS engine - so a replay is
// deterministic and the whole package is testable with fixture bars.
package emaflag

import (
	"math"
	"sync"
	"time"

	_ "time/tzdata" // America/New_York on a Windows box with no OS zone files (repo convention)

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// ─── Parameters ──────────────────────────────────────────────────────────

// Params are the engine's thresholds, in ONE named block, mirroring FlagParams
// in emaFlag.ts field for field (the comments there say what each gate is for).
type Params struct {
	EmaPeriod        int     // 9
	AtrPeriod        int     // 14 Wilder ATR length; only the SLAM's body test reads it
	MinExtPct        float64 // 5    impulse: a CLOSE at least this % above the EMA ...
	BreakoutLookback int     // 30   ... on a bar whose HIGH beats the highest high of the prior N bars of the same ET day (needs >= 5 prior bars that day)
	ImpulseBaseBars  int     // 10   the impulse's base low = min low of the N bars before the impulse bar (and the bar itself)
	MinFlatBars      int     // 3    closed bars, all at/after the top bar, that must qualify as flat
	MaxFlatOfMovePct float64 // 35   flat = the window's CLOSES span <= this % of the move (top - base low)
	MaxFlatPct       float64 // 10   ... and <= this % of price (sanity cap for enormous moves)
	MaxRetracePct    float64 // 50   the flag's lowest LOW must hold above base + (1 - this/100) * move
	EmaBreakPct      float64 // 1    a CLOSE more than this % under the EMA ends the setup (flag FAILED if one was active)
	ReadyDistPct     float64 // 4    READY: the flag's latest close is within this % above the EMA
	EntryNeedsReady  bool    // true a flag-high break counts as an entry only once READY; before that it is an EARLY BREAK
	BreakBufferPct   float64 // 0.3  trigger = flag high * (1 + this/100)
	StopBufferPct    float64 // 1.0  initial stop = max(flag low, EMA of the last CLOSED bar) * (1 - this/100); trailing uses the same buffer
	TrailEma         bool    // true while long, each closed bar raises the stop to EMA * (1 - buffer) (never lowers it)
	StopByClose      bool    // true exit on a CLOSE below the stop; false = any LOW at/below it (also on the forming bar)
	MaxAdds          int     // 2    later flag breakouts while long are ADDs, at most this many per position
	MaxFlagBars      int     // 30   a flag whose box (top bar included) spans more than this many closed bars without breaking expires (silent); no new flag until a new top. 0 = no limit

	// 9 EMA SLAM (an alert only: it never changes the state, the levels or the long side's events).
	Slam             bool    // true master switch for KindSlam
	SlamBelowPct     float64 // 3    the slam bar CLOSES at least this % under its own EMA ...
	SlamOpenTolPct   float64 // 0.5  ... having OPENED no more than this % under the previous bar's EMA (whose close was at/above it)
	SlamBodyAtr      float64 // 1    its body (open - close) is at least this many ATRs (Wilder, AtrPeriod, previous bar); 0 = no ATR test
	SlamMinBodyPct   float64 // 2    ... and at least this % of its open
	SlamCloseInRange float64 // 0.35 it closes in the lowest this fraction of its range: close - low <= this * (high - low)
	SlamVolX         float64 // 1.3  its volume is at least this x the mean of the up-to-10 earlier bars of the day (no test with fewer than 3)
}

// DefaultParams is DEFAULT_FLAG_PARAMS in emaFlag.ts, field for field. The
// golden test asserts the "defaults" cases carry exactly these values.
func DefaultParams() Params {
	return Params{
		EmaPeriod:        9,
		AtrPeriod:        14,
		MinExtPct:        5,
		BreakoutLookback: 30,
		ImpulseBaseBars:  10,
		MinFlatBars:      3,
		MaxFlatOfMovePct: 35,
		MaxFlatPct:       10,
		MaxRetracePct:    50,
		EmaBreakPct:      1,
		ReadyDistPct:     4,
		EntryNeedsReady:  true,
		BreakBufferPct:   0.3,
		StopBufferPct:    1.0,
		TrailEma:         true,
		StopByClose:      true,
		MaxAdds:          2,
		MaxFlagBars:      30,
		Slam:             true,
		SlamBelowPct:     3,
		SlamOpenTolPct:   0.5,
		SlamBodyAtr:      1,
		SlamMinBodyPct:   2,
		SlamCloseInRange: 0.35,
		SlamVolX:         1.3,
	}
}

// minPriorBars: an impulse needs this many earlier bars the same ET day before
// "a new N-bar high" means anything (MIN_PRIOR_BARS in emaFlag.ts).
const minPriorBars = 5

// The SLAM's volume yardstick: the mean volume of up to slamVolBars bars before
// the slam bar, the same ET day, and no volume test with fewer than
// slamMinVolBars of them (SLAM_VOL_BARS / SLAM_MIN_VOL_BARS in emaFlag.ts).
const (
	slamVolBars    = 10
	slamMinVolBars = 3
)

// ─── Result types ────────────────────────────────────────────────────────

// State is the machine's state after the last candle. The numeric values are
// the order of FlagState in emaFlag.ts and are what the e9State scan field
// carries, so they are part of the wire contract: do not reorder.
type State int

const (
	StateIdle    State = 0 // nothing to trade; waiting for an impulse
	StateImpulse State = 1 // BREAKOUT: armed, wait for it to go sideways
	StateFlat    State = 2 // FLAT: sideways above the 9 EMA
	StateReady   State = 3 // READY: the 9 EMA has caught up (within ReadyDistPct)
	StateLong    State = 4 // LONG: the flag high broke while ready; the position is open
)

// String is the TS FlagState spelling ('idle' ... 'long'); the golden file uses it.
func (s State) String() string {
	switch s {
	case StateImpulse:
		return "impulse"
	case StateFlat:
		return "flat"
	case StateReady:
		return "ready"
	case StateLong:
		return "long"
	default:
		return "idle"
	}
}

// Event kinds and labels, identical to FlagEventKind and the labels emaFlag.ts
// emits. Note '9 EMA NEAR' and 'SLAM (9 EMA)' are fixed labels even when
// EmaPeriod is not 9, as in the TS engine. KindSlam is the one bearish event
// (side 'short' in TS): an alert, never a trade.
const (
	KindImpulse    = "impulse"
	KindFlat       = "flat"
	KindEmaNear    = "emaNear"
	KindEntry      = "entry"
	KindAdd        = "add"
	KindEarlyBreak = "earlyBreak"
	KindFailed     = "failed"
	KindExit       = "exit"
	KindSlam       = "slam"

	LabelImpulse    = "BREAKOUT"
	LabelFlat       = "FLAT"
	LabelEmaNear    = "9 EMA NEAR"
	LabelEntry      = "LONG (FLAG)"
	LabelAdd        = "ADD (FLAG)"
	LabelEarlyBreak = "EARLY BREAK"
	LabelFailed     = "FLAG FAILED"
	LabelExit       = "EXIT"
	LabelSlam       = "SLAM (9 EMA)"
)

// Event is one thing the engine called on a bar. Price is "the level the event
// is about": the high for BREAKOUT, the trigger for FLAT / 9 EMA NEAR / EARLY
// BREAK / LONG / ADD, the close for FLAG FAILED and SLAM, the exit price for EXIT.
type Event struct {
	Ts    int64 // the bar's start (ms) the event belongs to
	Kind  string
	Label string
	Price float64
}

// Result is the engine's call for the LAST ET day in the bars. Float levels are
// NaN where the TS engine reports null (no flag, no position, EMA still warming
// up, ...): use math.IsNaN, never == 0.
type Result struct {
	State  State
	Events []Event // ALL days, oldest first, in EMISSION order (the TS order)

	Ema float64 // the 9 EMA at the last candle (what the chart line shows right now)
	// EmaClosed is the 9 EMA of the last CLOSED bar of the last ET day - the number
	// the engine's own stop trails (max(stop, EmaClosed * (1 - StopBufferPct/100)))
	// and the auto-trader's trailing stop order is hung from. It differs from Ema
	// while the last bar is forming (Ema then includes that bar's close). NaN while
	// no bar of the day has closed or the EMA is still warming up. EmaClosedTs is
	// the start (ms) of the bar it belongs to (0 with it), so a consumer can act
	// once per closed bar. Not part of the TS golden output: the TS summary uses
	// the same number internally (emaLast) and never exposes it.
	EmaClosed   float64
	EmaClosedTs int64
	Trigger     float64 // flag high * (1 + buffer) while a flag is active
	Stop        float64 // the open position's stop; else the stop a break of the active flag would get
	FlagHigh    float64 // the active flag's cap
	FlagLow     float64 // the active flag's lowest low since the top
	EntryPrice  float64 // the FIRST entry of the open position (an add never moves it)

	FlagBars   int     // bars the active flag has held, top bar included (0 = no flag / no closed bar yet)
	EmaDistPct float64 // the last candle's close vs the EMA, % (positive = above); NaN while the EMA warms up

	// StateSinceMs is the ts (bar start, ms) of the event that put the last day
	// into State: the LONG entry bar for long, the bar the EMA caught up on for
	// ready (the FLAT bar when the flag opened already inside the band, else the
	// 9 EMA NEAR bar), the FLAT bar for flat. For impulse it is the bar the
	// machine last ENTERED the impulse state: the BREAKOUT bar, or the bar a
	// faded/broken flag or a closed position handed it back to the live impulse.
	// 0 when idle. The scan's e9Age is "seconds since this".
	StateSinceMs int64

	// The latest 9 EMA SLAM (KindSlam) of the LAST ET day in the bars - the TS
	// evidence lastSlamTs / lastSlamClose / lastSlamPct, golden-pinned. A slam is
	// decided on a CLOSED bar only, so these never come from a forming bar.
	//   LastSlamTs    the slam bar's start (ms); 0 = no slam that day (the scan's
	//                 e9SlamAge is "seconds since this", Null when 0)
	//   LastSlamClose the slam bar's close (== the event's Price); NaN with none
	//   LastSlamPct   that close vs the slam bar's own 9 EMA, % - NEGATIVE (under
	//                 it, by at least SlamBelowPct); NaN with none (e9SlamPct)
	LastSlamTs    int64
	LastSlamClose float64
	LastSlamPct   float64

	// HOD FLAG (an alert only, no state or event change; hodFlag in emaFlag.ts):
	// the start (ms) of the FLAT bar of the LAST ET day's latest flag whose
	// run-up top was a NEW HIGH OF THE DAY - the top bar's high is above every
	// earlier bar's high of the day (pre-market included). 0 = none today.
	// Decided once, when the flag opens, so it fires once per flag.
	LastHodFlagTs int64
}

// ─── EMA ─────────────────────────────────────────────────────────────────

// emaSeries is the EMA of closes, aligned to the input, NaN during warm-up. The
// arithmetic is emaSeries in emaFlag.ts (itself the vendored chart engine's
// emaArr, operation for operation): SMA seed of the first `period` closes, then
// k = 2/(period+1). The explicit float64(...) around each product forbids the
// compiler from fusing multiply-add into an FMA on architectures that would
// (arm64 and friends), which would change the last bit versus JS; on amd64 it
// is a no-op.
func emaSeries(closes []float64, period int) []float64 {
	out := make([]float64, len(closes))
	for i := range out {
		out[i] = math.NaN()
	}
	k := 2 / float64(period+1)
	prev := 0.0
	sum := 0.0
	for i := 0; i < len(closes); i++ {
		if i < period-1 {
			sum += closes[i]
			continue
		}
		if i == period-1 {
			sum += closes[i]
			prev = sum / float64(period)
		} else {
			prev = float64(closes[i]*k) + float64(prev*(1-k))
		}
		out[i] = prev
	}
	return out
}

// atrSeries is the Wilder ATR of bars, aligned to the input, NaN during
// warm-up: atrSeries in emaFlag.ts operation for operation (an SMA of the first
// `period` true ranges, then (prev*(period-1) + tr)/period; bar 0's true range
// uses its own close as the previous close). The explicit float64(...) around
// the product forbids an FMA, as in emaSeries.
func atrSeries(bars []core.Bar, period int) []float64 {
	out := make([]float64, len(bars))
	for i := range out {
		out[i] = math.NaN()
	}
	acc := 0.0
	prev := 0.0
	for i := range bars {
		b := &bars[i]
		pc := b.C
		if i > 0 {
			pc = bars[i-1].C
		}
		tr := math.Max(math.Max(b.H-b.L, math.Abs(b.H-pc)), math.Abs(b.L-pc))
		if i < period {
			acc += tr
			if i == period-1 {
				prev = acc / float64(period)
				out[i] = prev
			}
			continue
		}
		prev = (float64(prev*float64(period-1)) + tr) / float64(period)
		out[i] = prev
	}
	return out
}

// ─── ET days ─────────────────────────────────────────────────────────────

var (
	etOnce sync.Once
	etLoc  *time.Location
)

// etLocation is America/New_York. If the zone cannot be loaded (it can't fail
// with time/tzdata linked in) it falls back to a fixed UTC-5: the only thing
// the zone is used for is the ET calendar-date change, which lands at 00:00
// local, and the market has no bars between 20:00 and 04:00 ET, so being an
// hour off in summer moves a day boundary inside an empty stretch of the clock.
func etLocation() *time.Location {
	etOnce.Do(func() {
		loc, err := time.LoadLocation("America/New_York")
		if err != nil {
			loc = time.FixedZone("ET", -5*3600)
		}
		etLoc = loc
	})
	return etLoc
}

// etDayKey is the ET calendar date of a ms timestamp as one comparable int
// (yyyymmdd) - the grouping the chart's VWAP resets on and the TS engine's
// etDayKey buckets by.
func etDayKey(ms int64, loc *time.Location) int {
	y, m, d := time.UnixMilli(ms).In(loc).Date()
	return y*10000 + int(m)*100 + d
}

// ─── The machine ─────────────────────────────────────────────────────────

// activeFlag is the TS ActiveFlag plus the two timestamps StateSinceMs needs;
// the box the TS engine draws is not kept.
type activeFlag struct {
	high     float64 // the flag high (the top it is holding under)
	trigger  float64
	endIdx   int // last index the flag held
	minClose float64
	maxClose float64
	ready    bool
	openTs   int64 // the FLAT bar
	readyTs  int64 // the bar the EMA caught up on (== openTs when it opened ready)
	slammed  bool  // this flag's episode already had its SLAM (at most one per episode)
}

type position struct {
	entryPrice float64
	entryTs    int64
	stop       float64
	adds       int
	slammed    bool // this position's episode already had its SLAM (at most one per episode)
}

// shared is what every day's machine reads (and the events it appends to).
type shared struct {
	p        Params
	bars     []core.Bar
	ema      []float64 // NaN during warm-up (the first period-1 bars of the WHOLE array)
	atr      []float64 // Wilder ATR(AtrPeriod), NaN during warm-up; nil when Slam is off (nothing else reads it)
	extFloor float64   // a close under ema * extFloor ends the setup
	minFlat  int
	events   []Event
}

// day is one ET day's mutable state (DayState in emaFlag.ts) with its rules as
// methods. A new one is created per ET day, so an impulse or a position never
// leaks across midnight, while ema (in shared) runs on continuously.
type day struct {
	*shared
	s           int // first index of this ET day
	impulse     bool
	baseLow     float64
	top         float64
	topIdx      int
	lowSinceTop float64 // lowest low of the closed bars AFTER the top bar (+Inf until one exists: the top bar is the thrust, not part of its own flag)
	flag        *activeFlag
	pos         *position

	prev     State // the state after the previous bar, to date impulse
	impSince int64

	slamIdx int     // the day's latest SLAM bar (-1: none yet)
	slamPct float64 // its close vs its EMA, % (negative)

	hodFlagTs int64 // FLAT bar of the day's latest flag whose top was a new high of the day (0: none)
}

func (d *day) emit(i int, kind string, price float64, label string) {
	d.events = append(d.events, Event{Ts: d.bars[i].T, Kind: kind, Label: label, Price: price})
}

func (d *day) state() State {
	switch {
	case d.pos != nil:
		return StateLong
	case d.flag != nil:
		if d.flag.ready {
			return StateReady
		}
		return StateFlat
	case d.impulse:
		return StateImpulse
	}
	return StateIdle
}

// track dates the impulse state: called after every bar (closed or the forming
// pass), it remembers the bar on which the machine last entered StateImpulse.
// The other states are dated from their own events (summarise).
func (d *day) track(i int) {
	cur := d.state()
	if cur != d.prev {
		if cur == StateImpulse {
			d.impSince = d.bars[i].T
		}
		d.prev = cur
	}
}

// exitPos closes the position; byClose exits at the close, otherwise at the stop.
func (d *day) exitPos(i int, byClose bool) {
	price := d.pos.stop
	if byClose {
		price = d.bars[i].C
	}
	d.emit(i, KindExit, price, LabelExit)
	d.pos = nil
}

// breakFlag is the break of the active flag's trigger - judged on the HIGH, so
// it also runs on the forming bar. It uses state exactly as it stood after the
// last CLOSED bar (the flag, ready, lowSinceTop, the EMA of bar i-1).
func (d *day) breakFlag(i int, f *activeFlag) {
	emaLast := d.ema[i]
	if i-1 >= 0 && !math.IsNaN(d.ema[i-1]) {
		emaLast = d.ema[i-1]
	}
	ready := f.ready
	d.flag = nil
	if ready || !d.p.EntryNeedsReady {
		if d.pos == nil {
			stop := math.Max(d.lowSinceTop, emaLast) * (1 - d.p.StopBufferPct/100)
			d.pos = &position{entryPrice: f.trigger, entryTs: d.bars[i].T, stop: stop}
			d.emit(i, KindEntry, f.trigger, LabelEntry)
		} else if d.pos.adds < d.p.MaxAdds {
			d.pos.adds++
			d.emit(i, KindAdd, f.trigger, LabelAdd)
		}
		// else: already at the add cap - the flag is spent and nothing prints.
	} else {
		d.emit(i, KindEarlyBreak, f.trigger, LabelEarlyBreak)
	}
}

// intrabar holds the rules that run on the forming bar as well as the closed
// one: the flag break by high and - only when the stop is a hard order - the
// stop by low. The closed loop runs it FIRST for every bar, which is what makes
// the closed-bar run reproduce every forming-bar event (the append-only
// contract the TS F9 test pins).
func (d *day) intrabar(i int) {
	b := &d.bars[i]
	if f := d.flag; d.impulse && f != nil && b.H > f.trigger {
		d.breakFlag(i, f)
	}
	if !d.p.StopByClose && d.pos != nil && b.L <= d.pos.stop {
		d.exitPos(i, false)
	}
}

// topExpired: the current top is older than a flag under it may be - more than
// MaxFlagBars bars from the top bar (inclusive) to bar i; 0 = no limit. The one
// definition of the flag expiry (topExpired in emaFlag.ts).
func (d *day) topExpired(i int) bool {
	return d.p.MaxFlagBars > 0 && i-d.topIdx+1 > d.p.MaxFlagBars
}

func (d *day) openFlag(i int, emaI float64) {
	p := &d.p
	bars := d.bars
	// A top already older than a flag may be opens nothing: the flag would be born
	// stale, and a dead flag gets no FLAT alert. It is also what makes an expiry
	// final: the count that expires a flag refuses the next one under the same top.
	if d.topExpired(i) {
		return
	}
	wStart := i - d.minFlat + 1
	if wStart < d.topIdx {
		return
	}
	move := d.top - d.baseLow
	if !(move > 0) {
		return
	}
	// The window may start ON the top bar (it counts toward MinFlatBars, and its
	// close must still hold the EMA floor and sit inside the opening span), but
	// the top bar is the thrust the flag hangs from, not part of the flag: the
	// flag's retrace low and its running close range start on the bar AFTER the
	// top - m - except with MinFlatBars 1, where the window is the one bar and
	// may be the top bar itself.
	m := wStart
	if d.minFlat > 1 {
		m = max(wStart, d.topIdx+1)
	}
	lo := math.Inf(1)
	hi := math.Inf(-1)
	minLow := math.Inf(1)
	fLo := math.Inf(1)
	fHi := math.Inf(-1)
	for k := wStart; k <= i; k++ {
		ek := d.ema[k]
		if math.IsNaN(ek) || !(bars[k].C >= ek*d.extFloor) {
			return
		}
		if bars[k].C < lo {
			lo = bars[k].C
		}
		if bars[k].C > hi {
			hi = bars[k].C
		}
		if k >= m {
			if bars[k].L < minLow {
				minLow = bars[k].L
			}
			if bars[k].C < fLo {
				fLo = bars[k].C
			}
			if bars[k].C > fHi {
				fHi = bars[k].C
			}
		}
	}
	span := hi - lo
	if span > (p.MaxFlatOfMovePct/100)*move || span > (p.MaxFlatPct/100)*bars[i].C {
		return
	}
	if minLow < d.baseLow+float64((1-p.MaxRetracePct/100)*move) {
		return
	}
	// lowSinceTop starts empty on a top bar, so a flag opened on the top bar
	// itself (MinFlatBars 1) takes its low from that bar.
	if math.IsInf(d.lowSinceTop, 0) || math.IsNaN(d.lowSinceTop) {
		d.lowSinceTop = minLow
	}

	trigger := d.top * (1 + p.BreakBufferPct/100)
	f := &activeFlag{high: d.top, trigger: trigger, endIdx: i, minClose: fLo, maxClose: fHi, openTs: bars[i].T}
	d.flag = f
	d.emit(i, KindFlat, trigger, LabelFlat)
	// HOD FLAG: the run-up's top is above every earlier high of this ET day.
	if d.topIdx > d.s {
		hod := math.Inf(-1)
		for k := d.s; k < d.topIdx; k++ {
			if bars[k].H > hod {
				hod = bars[k].H
			}
		}
		if d.top > hod {
			d.hodFlagTs = bars[i].T
		}
	}
	if bars[i].C <= emaI*(1+p.ReadyDistPct/100) {
		f.ready = true
		f.readyTs = bars[i].T
	}
}

func (d *day) extendFlag(i int, emaI float64, f *activeFlag) {
	p := &d.p
	c := d.bars[i].C
	if c < f.minClose {
		f.minClose = c
	}
	if c > f.maxClose {
		f.maxClose = c
	}
	move := d.top - d.baseLow
	span := f.maxClose - f.minClose
	if span > (p.MaxFlatOfMovePct/100)*move || span > (p.MaxFlatPct/100)*c {
		d.flag = nil // faded, silently
		return
	}
	f.endIdx = i
	// EXPIRY: the box now spans more than MaxFlagBars bars from the top bar. Silent,
	// and BEFORE the READY test below, so an expiring flag emits no 9 EMA NEAR.
	if d.topExpired(i) {
		d.flag = nil
		return
	}
	if !f.ready && c <= emaI*(1+p.ReadyDistPct/100) {
		f.ready = true
		f.readyTs = d.bars[i].T
		d.emit(i, KindEmaNear, f.trigger, LabelEmaNear)
	}
}

func (d *day) tryStartImpulse(i int, emaI float64) {
	p := &d.p
	bars := d.bars
	if i-d.s < minPriorBars {
		return
	}
	if !(bars[i].C >= emaI*(1+p.MinExtPct/100)) {
		return
	}
	priorHigh := math.Inf(-1)
	for k := max(d.s, i-p.BreakoutLookback); k < i; k++ {
		if bars[k].H > priorHigh {
			priorHigh = bars[k].H
		}
	}
	if !(bars[i].H > priorHigh) {
		return
	}
	bl := bars[i].L
	for k := max(d.s, i-p.ImpulseBaseBars); k < i; k++ {
		if bars[k].L < bl {
			bl = bars[k].L
		}
	}
	d.impulse = true
	d.baseLow = bl
	d.top = bars[i].H
	d.topIdx = i
	d.lowSinceTop = math.Inf(1)
	d.emit(i, KindImpulse, bars[i].H, LabelImpulse)
}

// closedBar is one closed bar through the rules, in the TS order: the long
// side's rules (closedRules), then the SLAM. SLAM rule 1 - the position and the
// flag open at the END of bar i-1 (flat / ready / long), each an episode of its
// own (a later flag can open next to a position that survived its slam under
// edited params) - is taken before this bar's rules can close them. A bar may
// slam while either has not slammed yet; one that already slammed is not
// slammed twice.
func (d *day) closedBar(i int) {
	emaI := d.ema[i]
	if math.IsNaN(emaI) {
		return // EMA warm-up (the first period-1 bars of the whole array)
	}
	var slamPos *position
	var slamFlag *activeFlag
	if d.p.Slam {
		slamPos, slamFlag = d.pos, d.flag
	}
	d.closedRules(i, emaI)
	if (slamPos != nil && !slamPos.slammed) || (slamFlag != nil && !slamFlag.slammed) {
		d.trySlam(i, emaI, slamPos, slamFlag)
	}
}

// trySlam is SLAM rules 2-6 on CLOSED bar i, run after the bar's own rules (so
// it is emitted after whatever that bar's long side reported: EXIT, FLAG
// FAILED, or EARLY BREAK); trySlam in emaFlag.ts, comparison for comparison. It
// marks the episodes (pos and flag as they stood at the end of bar i-1, either
// may be nil: BOTH, since the one bar slammed through both) and records the
// day's latest slam - nothing else: a SLAM changes no state, level or long-side
// event.
func (d *day) trySlam(i int, emaI float64, pos *position, flag *activeFlag) {
	p := &d.p
	if i-1 < d.s {
		return // rule 1 is the same ET day
	}
	emaPrev := d.ema[i-1]
	if math.IsNaN(emaPrev) {
		return
	}
	b := &d.bars[i]
	o, c := b.O, b.C
	// 2. from above: the previous close held the EMA, and this bar opened at/near it
	if !(d.bars[i-1].C >= emaPrev) {
		return
	}
	if !(o >= emaPrev*(1-p.SlamOpenTolPct/100)) {
		return
	}
	// 3. through: closed well under its own EMA
	if !(c <= emaI*(1-p.SlamBelowPct/100)) {
		return
	}
	// 4. a hard candle: a big body, in ATRs of the bar before and in % of the open
	body := o - c
	if !(body > 0) {
		return
	}
	if p.SlamBodyAtr > 0 {
		atrPrev := math.NaN()
		if d.atr != nil {
			atrPrev = d.atr[i-1]
		}
		if math.IsNaN(atrPrev) || !(body >= p.SlamBodyAtr*atrPrev) {
			return
		}
	}
	if !(body/o >= p.SlamMinBodyPct/100) {
		return
	}
	// 5. closed weak: in the lower part of its range
	if !(c-b.L <= p.SlamCloseInRange*(b.H-b.L)) {
		return
	}
	// 6. on volume: vs the mean of the up-to-10 earlier bars of the day (no test with fewer than 3)
	volSum := 0.0
	nVol := 0
	for k := i - 1; k >= d.s && nVol < slamVolBars; k-- {
		volSum += float64(d.bars[k].V)
		nVol++
	}
	if nVol >= slamMinVolBars && !(float64(b.V) >= p.SlamVolX*(volSum/float64(nVol))) {
		return
	}

	if pos != nil {
		pos.slammed = true
	}
	if flag != nil {
		flag.slammed = true
	}
	d.slamIdx = i
	d.slamPct = (c/emaI - 1) * 100
	d.emit(i, KindSlam, c, LabelSlam)
}

// closedRules is the long side's rules on one closed bar, in the TS order
// (closedBar in emaFlag.ts).
func (d *day) closedRules(i int, emaI float64) {
	p := &d.p
	b := &d.bars[i]

	// 1. the intrabar rules - the very step the forming pass runs.
	d.intrabar(i)

	// 2. a position: the close-based stop, then the EMA trail.
	if pos := d.pos; pos != nil {
		if p.StopByClose && b.C < pos.stop {
			d.exitPos(i, true)
		} else if p.TrailEma {
			pos.stop = math.Max(pos.stop, emaI*(1-p.StopBufferPct/100))
		}
	}

	// 3. FAIL: a close well under the EMA ends the setup (and a long is left to its own stop).
	if b.C < emaI*d.extFloor {
		if d.flag != nil {
			d.flag = nil
			d.emit(i, KindFailed, b.C, LabelFailed)
		}
		d.impulse = false
		return
	}

	// 4. the impulse: start one, or move its top and keep a flag honest.
	if !d.impulse {
		d.tryStartImpulse(i, emaI)
		return
	}
	if b.H > d.top {
		// A new top. A flag still open here was poked (high above its cap but
		// not through the trigger - a real break was handled in step 1): it can
		// no longer be "under the high", so it closes quietly.
		d.flag = nil
		d.top = b.H
		d.topIdx = i
		d.lowSinceTop = math.Inf(1)
	} else if b.L < d.lowSinceTop {
		d.lowSinceTop = b.L
	}
	if f := d.flag; f != nil {
		d.extendFlag(i, emaI, f)
	} else {
		d.openFlag(i, emaI)
	}
}

// ─── Top-level entry point ───────────────────────────────────────────────

// Evaluate runs the 9 EMA flag engine over bars (ascending by T; may span the
// previous trading day + today) at candle width tfMs (60_000 for 1-minute) and
// returns the call for the LAST ET day. The last bar is the FORMING bar iff
// nowMs < last.T + tfMs - the same contract as evaluateFlag's nowMs - in which
// case only the intrabar rules run on it.
//
// Bars must be finite and in ascending order; a NaN close poisons the EMA from
// that bar on, which reads as "warming up" (idle), never as a signal.
func Evaluate(bars []core.Bar, tfMs, nowMs int64, p Params) Result {
	n := len(bars)
	res := Result{
		Ema: math.NaN(), EmaClosed: math.NaN(), Trigger: math.NaN(), Stop: math.NaN(), FlagHigh: math.NaN(),
		FlagLow: math.NaN(), EntryPrice: math.NaN(), EmaDistPct: math.NaN(),
		LastSlamClose: math.NaN(), LastSlamPct: math.NaN(),
	}
	if n == 0 {
		return res
	}
	// A bad config must not index out of range: a period under 1 is 1.
	if p.EmaPeriod < 1 {
		p.EmaPeriod = 1
	}

	closes := make([]float64, n)
	for i := range bars {
		closes[i] = bars[i].C
	}
	sh := &shared{
		p: p, bars: bars, ema: emaSeries(closes, p.EmaPeriod),
		extFloor: 1 - p.EmaBreakPct/100,
		minFlat:  max(1, p.MinFlatBars),
	}
	if p.Slam {
		sh.atr = atrSeries(bars, p.AtrPeriod)
	}

	// Day ranges (bars are ts-ascending, so a day is one contiguous run). The ET
	// calendar date can only change on a UTC HOUR boundary (America/New_York's
	// offsets are whole hours and its DST switches happen on the hour), so one
	// lookup per UTC hour gives the same key for every bar in it - the same trick
	// as the TS engine, and it keeps this off the per-bar hot path.
	loc := etLocation()
	var dayStarts []int
	lastHour := int64(math.MinInt64)
	key, prevKey := 0, 0
	for i := range bars {
		hour := floorDiv(bars[i].T, 3_600_000)
		if hour != lastHour {
			lastHour = hour
			key = etDayKey(bars[i].T, loc)
		}
		if i == 0 || key != prevKey {
			dayStarts = append(dayStarts, i)
		}
		prevKey = key
	}

	var last *day
	var lastForming bool
	var lastE int
	for di, s := range dayStarts {
		e := n
		if di+1 < len(dayStarts) {
			e = dayStarts[di+1]
		}
		isLastDay := di == len(dayStarts)-1
		forming := isLastDay && nowMs < bars[n-1].T+tfMs
		d := &day{shared: sh, s: s, lowSinceTop: 0, topIdx: -1, slamIdx: -1}
		lastClosed := e - 1
		if forming {
			lastClosed = e - 2
		}
		for i := s; i <= lastClosed; i++ {
			d.closedBar(i)
			d.track(i)
		}
		// the forming bar
		if forming {
			d.intrabar(e - 1)
			d.track(e - 1)
		}
		if isLastDay {
			last, lastForming, lastE = d, forming, e
		}
	}
	d := last
	res.Events = sh.events

	// ── summary of the last day (summarise in emaFlag.ts) ──
	lastIdx := lastE - 1
	lastClosed := lastIdx
	if lastForming {
		lastClosed = lastIdx - 1
	}
	emaNow := sh.ema[lastIdx]
	emaLast := math.NaN()
	if lastClosed >= d.s {
		emaLast = sh.ema[lastClosed]
	}
	res.Ema = emaNow
	if !math.IsNaN(emaLast) {
		res.EmaClosed, res.EmaClosedTs = emaLast, bars[lastClosed].T
	}

	f, pos := d.flag, d.pos
	prospectiveStop := math.NaN()
	if f != nil {
		res.FlagHigh = f.high
		res.FlagLow = d.lowSinceTop
		res.Trigger = f.trigger
		if !math.IsNaN(emaLast) {
			prospectiveStop = math.Max(d.lowSinceTop, emaLast) * (1 - p.StopBufferPct/100)
		}
	}
	if pos != nil {
		res.Stop = pos.stop
		res.EntryPrice = pos.entryPrice
	} else {
		res.Stop = prospectiveStop
	}
	if f != nil && lastClosed >= d.s {
		res.FlagBars = f.endIdx - d.topIdx + 1
	}
	if !math.IsNaN(emaNow) && emaNow > 0 {
		res.EmaDistPct = (bars[lastIdx].C/emaNow - 1) * 100
	}
	if d.slamIdx >= 0 {
		res.LastSlamTs, res.LastSlamClose, res.LastSlamPct = bars[d.slamIdx].T, bars[d.slamIdx].C, d.slamPct
	}
	res.LastHodFlagTs = d.hodFlagTs

	res.State = d.state()
	switch res.State {
	case StateLong:
		res.StateSinceMs = pos.entryTs
	case StateReady:
		res.StateSinceMs = f.readyTs
	case StateFlat:
		res.StateSinceMs = f.openTs
	case StateImpulse:
		res.StateSinceMs = d.impSince
	}
	return res
}

// floorDiv is a/b rounded toward -inf (JS Math.floor(a/b)); b > 0.
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && a < 0 {
		q--
	}
	return q
}
