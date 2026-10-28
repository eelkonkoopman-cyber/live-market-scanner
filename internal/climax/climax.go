// Package climax is the Go port of the VOLUME CLIMAX -> BOTTOM CONFIRMED rule
// the Signal window runs in the browser (web/src/signals/climax.ts). Both of
// its alerts are engine-native: the scanner engine's shared closed-1-minute-bar
// judge (internal/engine) calls Classify on every closed traded minute of every
// ticker - with the 10 prior bars of the SAME ET day recomputed from its ring
// oldest -> newest and its own SMA-seeded 9 EMA - arms a per-ticker Latch on a
// hit (alert 1, "SYM: volume climax"), and Steps that latch on each later
// closed traded bar of the day (alert 2, "SYM: bottom confirmed"). No REST, no
// flagscan, no EmaFlagRef.
//
// THE RULE SET IS DEFINED IN TypeScript. This package is a line-by-line port of
// it, and the Latch here is the SAME code the engine runs, so the golden
// vectors (web/src/signals/fixtures/golden/climax-golden.json, written by
// climax.golden.test.ts with UPDATE_GOLDEN=1; golden_test.go here) test the
// engine's confirmation logic directly. Read climax.ts's header first: it
// carries the trader's request, the rule and the calibration caveat - an
// ALERT, a "look now" prompt, NOT a system (no edge after costs).
//
// What is deliberately NOT ported: the event notes (no Go consumer reads text).
//
// Pure: no clock reads (the caller passes nowMs); Classify, Latch.Arm and
// Latch.Step allocate nothing.
package climax

import (
	"math"
	"sync"
	"time"

	_ "time/tzdata" // America/New_York on a Windows box with no OS zone files (repo convention)

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// ─── Parameters ──────────────────────────────────────────────────────────

// Params mirrors ClimaxParams in climax.ts field for field.
type Params struct {
	Enabled         bool    // true  master switch (the engine's own gate is env SCANNER_CLIMAX)
	Lookback        int     // 10    the priors are EXACTLY the last N traded bars of the SAME ET day (fewer: no climax)
	MinDropPct      float64 // 6     (priors' highest high - low)/that high*100 >= this
	MinVolX         float64 // 8     volume >= this x the priors' mean volume (a mean of 0 passes, +Inf)
	RequireMaxVol   bool    // true  volume >= the biggest single prior bar's volume (inclusive)
	MinWickPct      float64 // 25    lower wick (min(open, close) - low)/(high - low)*100 >= this
	RequireBelowEma bool    // true  close < EMA9 at this bar (strict; EMA unknown -> no climax)
	EmaPeriod       int     // 9     SMA-seeded, continuous across days
	MinPrice        float64 // 0.50  close >= this
	MinDollarVol    float64 // 50000 close * volume >= this
	MuteStartMin    int     // 570   ET minute-of-day: bars starting 09:30 ...
	MuteEndMin      int     // 575   ... up to but EXCLUDING 09:35 are never a climax (start >= end disables the mute)
	CoreHoursOnly   bool    // false true: only bars starting 10:00-15:59 ET (etMin in [600, 960))
	ConfirmBars     int     // 5     the confirmation looks at the next N closed traded bars of the same ET day
}

// DefaultParams is DEFAULT_CLIMAX_PARAMS in climax.ts (the calibrated
// defaults). The golden test asserts the "defaults" cases carry exactly these.
func DefaultParams() Params {
	return Params{
		Enabled:         true,
		Lookback:        10,
		MinDropPct:      6,
		MinVolX:         8,
		RequireMaxVol:   true,
		MinWickPct:      25,
		RequireBelowEma: true,
		EmaPeriod:       9,
		MinPrice:        0.50,
		MinDollarVol:    50_000,
		MuteStartMin:    570,
		MuteEndMin:      575,
		CoreHoursOnly:   false,
		ConfirmBars:     5,
	}
}

// ─── The candle ──────────────────────────────────────────────────────────

// Priors are the prior bars, built oldest -> newest: Hi = max high, Lo = min
// low, SumVol += float64(v), MaxVol = max v (climaxPriors in climax.ts). With
// none: Hi -Inf, Lo +Inf.
type Priors struct {
	N      int
	Hi, Lo float64
	SumVol float64
	MaxVol int64
}

// Hit is what a climax measured. VolX is +Inf when the priors' mean volume was
// 0; EmaDistPct is NaN when the EMA was unknown. Mid = (high + low)/2 is the
// level a later close must beat to confirm.
type Hit struct {
	DropPct    float64
	VolX       float64
	WickPct    float64
	DollarVol  float64
	EmaDistPct float64
	Mid        float64
}

// Classify judges one closed candle. ema is the bar's own EMA (NaN or Inf =
// unknown), etMin its START's ET minute-of-day. It allocates nothing;
// classifyClimax in climax.ts expression for expression.
func Classify(o, h, l, c float64, v int64, pr Priors, ema float64, etMin int, p Params) (Hit, bool) {
	if !p.Enabled || !(pr.N >= p.Lookback) || !(h > l) || !(pr.Hi > 0) {
		return Hit{}, false
	}
	if !(l <= pr.Lo) {
		return Hit{}, false
	}
	dropPct := (pr.Hi - l) / pr.Hi * 100
	if !(dropPct >= p.MinDropPct) {
		return Hit{}, false
	}
	meanVol := pr.SumVol / float64(pr.N)
	volX := math.Inf(1)
	if meanVol > 0 {
		volX = float64(v) / meanVol
	}
	if !(volX >= p.MinVolX) {
		return Hit{}, false
	}
	if p.RequireMaxVol && !(v >= pr.MaxVol) {
		return Hit{}, false
	}
	wickPct := (math.Min(o, c) - l) / (h - l) * 100
	if !(wickPct >= p.MinWickPct) {
		return Hit{}, false
	}
	if !(c >= p.MinPrice) {
		return Hit{}, false
	}
	dollarVol := c * float64(v)
	if !(dollarVol >= p.MinDollarVol) {
		return Hit{}, false
	}
	emaKnown := !math.IsNaN(ema) && !math.IsInf(ema, 0)
	if p.RequireBelowEma && !(emaKnown && c < ema) {
		return Hit{}, false
	}
	if p.MuteStartMin < p.MuteEndMin && etMin >= p.MuteStartMin && etMin < p.MuteEndMin {
		return Hit{}, false
	}
	if p.CoreHoursOnly && !(etMin >= 600 && etMin < 960) {
		return Hit{}, false
	}
	emaDist := math.NaN()
	if emaKnown {
		emaDist = (c - ema) / ema * 100
	}
	return Hit{DropPct: dropPct, VolX: volX, WickPct: wickPct, DollarVol: dollarVol, EmaDistPct: emaDist, Mid: (h + l) / 2}, true
}

// candleGates is the part of Classify that needs no prior bars (the same
// expressions, cheapest first); Evaluate runs it, and two exact necessary
// conditions on the bar before (see Evaluate), and builds the priors only for a
// candle that passes. The result is identical: Classify re-runs every gate.
func candleGates(o, h, l, c float64, v int64, ema float64, etMin int, p Params) bool {
	if !p.Enabled || !(h > l) {
		return false
	}
	if p.RequireBelowEma && !(!math.IsNaN(ema) && !math.IsInf(ema, 0) && c < ema) {
		return false
	}
	if p.MuteStartMin < p.MuteEndMin && etMin >= p.MuteStartMin && etMin < p.MuteEndMin {
		return false
	}
	if p.CoreHoursOnly && !(etMin >= 600 && etMin < 960) {
		return false
	}
	if !(c >= p.MinPrice) || !(c*float64(v) >= p.MinDollarVol) {
		return false
	}
	return (math.Min(o, c)-l)/(h-l)*100 >= p.MinWickPct
}

// PriorsOf builds Priors over bars[from:to], oldest -> newest (the chart's
// construction; the engine builds the same from its ring).
func PriorsOf(bars []core.Bar, from, to int) Priors {
	pr := Priors{Hi: math.Inf(-1), Lo: math.Inf(1)}
	for k := from; k < to; k++ {
		b := &bars[k]
		if b.H > pr.Hi {
			pr.Hi = b.H
		}
		if b.L < pr.Lo {
			pr.Lo = b.L
		}
		pr.SumVol += float64(b.V)
		if b.V > pr.MaxVol {
			pr.MaxVol = b.V
		}
	}
	if to > from {
		pr.N = to - from
	}
	return pr
}

// ─── The latch (the confirmation; shared by the engine and Evaluate) ─────

// State is a climax latch's state, and the scan's climaxState.
type State int8

const (
	StateNone      State = 0
	StateWatching  State = 1 // armed: waiting for a close above Mid within ConfirmBars bars
	StateConfirmed State = 2 // BOTTOM CONFIRMED (alert 2)
	StateFailed    State = 3 // a bar undercut the climax low first
	StateExpired   State = 4 // ConfirmBars bars passed with neither (or the ET day ended)
)

// Latch is one climax and its confirmation. BarMs is the climax bar's START;
// Low the level that must hold; Bars the closed traded bars folded after the
// climax; EndMs the START of the verdict bar (0 while watching); ConfPx the
// confirming close (StateConfirmed only).
type Latch struct {
	BarMs  int64
	Low    float64
	Hit    Hit
	Bars   int
	State  State
	EndMs  int64
	ConfPx float64
}

// Arm starts watching a new climax, replacing whatever the latch held.
func (x *Latch) Arm(barMs int64, low float64, hit Hit) {
	*x = Latch{BarMs: barMs, Low: low, Hit: hit, State: StateWatching}
}

// Step folds one CLOSED traded bar after the climax, of the SAME ET day (the
// caller expires a watching latch at the day change). Only a watching latch
// moves. The undercut is checked FIRST (a bar that undercuts and closes above
// the midpoint FAILS); both tests are strict.
func (x *Latch) Step(barMs int64, l, c float64, p Params) State {
	if x.State != StateWatching {
		return x.State
	}
	x.Bars++
	switch {
	case l < x.Low:
		x.State, x.EndMs = StateFailed, barMs
	case c > x.Hit.Mid:
		x.State, x.EndMs, x.ConfPx = StateConfirmed, barMs, c
	case x.Bars >= p.ConfirmBars:
		x.State, x.EndMs = StateExpired, barMs
	}
	return x.State
}

// ─── Events and the result ───────────────────────────────────────────────

// Event kinds, labels and the fail reason, identical to climax.ts.
const (
	KindClimax = "climax"     // VOLUME CLIMAX: alert 1's moment; Price = the climax LOW
	KindBottom = "bottom"     // BOTTOM CONFIRMED: alert 2's moment; Price = the confirming close
	KindFail   = "climaxFail" // CLIMAX FAILED (list-only); Price = the undercutting low

	LabelClimax = "VOLUME CLIMAX"
	LabelBottom = "BOTTOM CONFIRMED"
	LabelFail   = "CLIMAX FAILED"

	ReasonLow = "low"
)

// Event is one thing the rule called on a CLOSED bar.
type Event struct {
	Ts     int64
	Kind   string
	Label  string
	Price  float64
	Reason string // KindFail only
}

// Result is the rule's call: every event, plus the LAST ET day's latest climax.
// Floats are NaN and timestamps 0 where the TS result says null.
type Result struct {
	Events []Event // every ET day in the bars, oldest first

	LastClimaxTs int64 // the climax bar's start; 0 = none today
	ClimaxLow    float64
	ClimaxMid    float64
	DropPct      float64
	VolX         float64 // may be +Inf
	WickPct      float64
	DollarVol    float64
	State        State // as Latch.State, plus StateFailed for a FORMING bar that already undercut the low
	Bars         int
	ConfTs       int64
	ConfPx       float64
	FailTs       int64 // the undercutting bar (the forming bar's start for a forming undercut)
	EndTs        int64
}

func emptyResult() Result {
	nan := math.NaN()
	return Result{ClimaxLow: nan, ClimaxMid: nan, DropPct: nan, VolX: nan, WickPct: nan, DollarVol: nan, ConfPx: nan}
}

// ─── EMA (private copy of emaflag.emaSeries; climax_test.go pins it) ─────

// emaFold is emaflag's emaSeries as a running fold, so Evaluate needs no
// per-bar arrays: SMA seed of the first `period` closes, then
// k = 2/(period+1), operation for operation (while warming up, prev holds the
// running sum). The explicit float64(...) around each product forbids an FMA,
// which would change the last bit versus JS. Four fields, so the compiler can
// keep it in registers.
type emaFold struct {
	k      float64
	prev   float64
	left   int // closes still needed before the SMA seed
	period int
}

func newEmaFold(period int) emaFold {
	return emaFold{k: 2 / float64(period+1), left: period, period: period}
}

// next folds one close and returns the EMA including it (NaN during warm-up).
func (e *emaFold) next(c float64) float64 {
	switch {
	case e.left > 1:
		e.left--
		e.prev += c
		return math.NaN()
	case e.left == 1:
		e.left = 0
		e.prev += c
		e.prev = e.prev / float64(e.period)
	default:
		e.prev = float64(c*e.k) + float64(e.prev*(1-e.k))
	}
	return e.prev
}

// emaSeries is the fold over a whole close series (the pinned form).
func emaSeries(closes []float64, period int) []float64 {
	f := newEmaFold(period)
	out := make([]float64, len(closes))
	for i, c := range closes {
		out[i] = f.next(c)
	}
	return out
}

// ─── ET ──────────────────────────────────────────────────────────────────

var (
	etOnce sync.Once
	etLoc  *time.Location
)

// etLocation is America/New_York (a private copy of emaflag's).
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

// etOffsetMs is the UTC->ET offset in force at ms (negative: -4 h EDT, -5 h EST).
func etOffsetMs(ms int64) int64 {
	_, off := time.UnixMilli(ms).In(etLocation()).Zone()
	return int64(off) * 1000
}

// EtMinute is the ET minute-of-day of ms under the offset offMs (the engine's
// one shared formula: floorMod(floorDiv(ms+off, 60 000), 1440)).
func EtMinute(ms, offMs int64) int {
	return int(floorMod(floorDiv(ms+offMs, 60_000), 1440))
}

// EtDay is the ET day number of ms under offMs (floorDiv(ms+off, 86 400 000)).
func EtDay(ms, offMs int64) int64 {
	return floorDiv(ms+offMs, 86_400_000)
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func floorMod(a, b int64) int64 {
	return a - floorDiv(a, b)*b
}

// ─── Evaluate ────────────────────────────────────────────────────────────

// Evaluate runs the climax rule over bars (ascending by T; may span the
// previous trading day + today) and returns every event plus the last ET day's
// latest climax. tfMs must be 60_000 (1-minute only; anything else, or
// p.Enabled false, returns an empty result). The last bar is FORMING iff
// nowMs < last.T + tfMs: it never emits; a watching latch it already undercut
// reads StateFailed (final). evaluateClimax in climax.ts.
func Evaluate(bars []core.Bar, tfMs, nowMs int64, p Params) Result {
	res := emptyResult()
	n := len(bars)
	if tfMs != 60_000 || !p.Enabled || n == 0 {
		return res
	}
	period := p.EmaPeriod
	if period < 1 {
		period = 1
	}
	lookback := p.Lookback
	if lookback < 0 {
		lookback = 0
	}
	forming := nowMs < bars[n-1].T+tfMs
	lastClosed := n - 1
	if forming {
		lastClosed = n - 2
	}

	// One pass over the closed bars: the EMA folds on, and the ET minute and day
	// come from the UTC->ET offset, looked up once per UTC hour (it can only
	// change on an hour boundary: the TS engine's trick). A new ET day starts a
	// fresh latch: a confirmation never crosses it.
	fold := newEmaFold(period)
	var hourStart, hourEnd, off, day int64 // the current UTC hour [hourStart, hourEnd), its offset and ET day
	s := 0                                 // the first index of the current ET day
	var events []Event
	var x Latch
	for j := 0; j <= lastClosed; j++ {
		b := &bars[j]
		if j == 0 || b.T < hourStart || b.T >= hourEnd {
			hourStart = floorDiv(b.T, 3_600_000) * 3_600_000
			hourEnd = hourStart + 3_600_000
			off = etOffsetMs(hourStart)
			if d := EtDay(hourStart, off); j == 0 || d != day {
				day, s = d, j
				x = Latch{}
			}
		}
		ema := fold.next(b.C)
		// a) the watching latch
		if x.State == StateWatching {
			switch x.Step(b.T, b.L, b.C, p) {
			case StateConfirmed:
				events = append(events, Event{Ts: b.T, Kind: KindBottom, Label: LabelBottom, Price: b.C})
			case StateFailed:
				events = append(events, Event{Ts: b.T, Kind: KindFail, Label: LabelFail, Price: b.L, Reason: ReasonLow})
			}
		}
		// b) is bar j a climax? (exactly `lookback` priors of this day)
		// The bar before is one of the priors (lookback >= 1), so a fresh low can't be over its low and,
		// with RequireMaxVol, the volume can't be under its volume: exact necessary conditions that skip
		// building the priors for most bars (Classify still decides).
		if j-s < lookback || lookback < 1 || bars[j-1].L < b.L || (p.RequireMaxVol && bars[j-1].V > b.V) {
			continue
		}
		etMin := EtMinute(b.T, off)
		if !candleGates(b.O, b.H, b.L, b.C, b.V, ema, etMin, p) {
			continue
		}
		if hit, ok := Classify(b.O, b.H, b.L, b.C, b.V, PriorsOf(bars, j-lookback, j), ema, etMin, p); ok {
			x.Arm(b.T, b.L, hit) // latest supersedes
			events = append(events, Event{Ts: b.T, Kind: KindClimax, Label: LabelClimax, Price: b.L})
		}
	}
	// The result is the LAST ET day's latch: a forming bar that opens a new ET day
	// (or a lone forming bar) has none; a watching latch the forming bar already
	// undercut has FAILED (final - a low only falls).
	formingFail := false
	if forming {
		f := &bars[n-1]
		if lastClosed < 0 || EtDay(f.T, etOffsetMs(floorDiv(f.T, 3_600_000)*3_600_000)) != day {
			x = Latch{}
		} else if x.State == StateWatching && f.L < x.Low {
			formingFail = true
		}
	}
	res.Events = events

	// ── the Result: the last ET day's latch ──
	if x.State == StateNone {
		return res
	}
	res.LastClimaxTs = x.BarMs
	res.ClimaxLow, res.ClimaxMid = x.Low, x.Hit.Mid
	res.DropPct, res.VolX, res.WickPct, res.DollarVol = x.Hit.DropPct, x.Hit.VolX, x.Hit.WickPct, x.Hit.DollarVol
	res.Bars = x.Bars
	res.State = x.State
	if x.State == StateConfirmed {
		res.ConfTs, res.ConfPx = x.EndMs, x.ConfPx
	}
	if x.State == StateFailed {
		res.FailTs = x.EndMs
	}
	if x.State != StateWatching {
		res.EndTs = x.EndMs
	}
	if formingFail {
		fts := bars[n-1].T
		res.State, res.FailTs, res.EndTs = StateFailed, fts, fts
	}
	return res
}
