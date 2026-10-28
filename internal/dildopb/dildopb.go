// Package dildopb is the Go port of the GREEN DILDO -> 9 EMA PULLBACK rule the
// Signal window runs in the browser (web/src/signals/dildoPb.ts). It serves the
// market scan twice:
//
//   - Classify is alert 1's judge: the scanner engine (internal/engine) builds
//     1-minute OHLCV for every ticker from the per-second aggregates and calls
//     it on every closed minute, with the prior bars' sums recomputed from its
//     ring oldest -> newest (never a running add/subtract sum, which drifts by
//     ulps at the 4x / 5x boundaries).
//   - Evaluate is alert 2's: internal/flagscan runs it once a second over the
//     same REST + tap bars the chart draws (previous day + today), with the
//     chart's continuous 9 EMA, and publishes the pullback state.
//
// THE RULE SET IS DEFINED IN TypeScript. This package is a line-by-line port of
// it: same gates in the same order, the same SMA-seeded EMA, the same pending
// anchor ("latest supersedes"), the same forming-bar states. Read dildoPb.ts's
// header first - it carries the trader's request and the calibration caveat
// (an alert, a "look now" prompt, not a system: no edge after costs).
//
// Parity is pinned by web/src/signals/fixtures/golden/dildopb-golden.json
// (written by dildoPb.golden.test.ts with UPDATE_GOLDEN=1) and golden_test.go
// here: identical events (ts, kind, label, note, reason; price and emaDistPct
// as exact float64 bits) and identical Result fields. Event notes are
// formatted on demand (Event.Note), so Evaluate itself never builds a string.
//
// Pure: no clock reads, the caller passes nowMs; imports only core, math,
// math/big (notes), strconv/strings (notes) and time (the ET day and clock).
package dildopb

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "time/tzdata" // America/New_York on a Windows box with no OS zone files (repo convention)

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// ─── Parameters ──────────────────────────────────────────────────────────

// Floor values (Params.Floor).
const (
	FloorMid  = "mid"  // floor = (open + close)/2 of the dildo bar (the default)
	FloorOpen = "open" // floor = the dildo bar's open
)

// Params mirrors DildoParams in dildoPb.ts field for field.
type Params struct {
	Enabled      bool    // true   master switch (the engine's own gate is env SCANNER_DILDO)
	MinBodyPct   float64 // 8      bodyPct = (close - open)/open*100 >= this
	MinBodyX     float64 // 4      body >= this x the mean |close - open| of the prior bars (a mean of 0 passes)
	MaxTopPct    float64 // 20     (high - close)/(high - low)*100 <= this
	MinVolX      float64 // 5      volume >= this x the mean volume of the prior bars (a mean of 0 passes)
	PriorBars    int     // 10     the prior bars are the up to this many TRADED bars before the candle
	MinPriorBars int     // 3      no dildo is judged with fewer prior bars
	MinPrice     float64 // 0.20   close >= this
	MinDollarVol float64 // 100000 close * volume >= this
	PbWindowMin  int     // 20     the pullback is looked for on bars with ts <= dildo ts + this many minutes
	TouchPct     float64 // 1      touch level = EMA9[j-1] * (1 + this/100)
	HoldPct      float64 // 1      the touch bar must close >= EMA9[j] * (1 - this/100)
	Floor        string  // "mid"  FloorMid or FloorOpen
	EmaPeriod    int     // 9
}

// DefaultParams is DEFAULT_DILDO_PARAMS in dildoPb.ts (the calibrated defaults).
// The golden test asserts the "defaults" cases carry exactly these values.
func DefaultParams() Params {
	return Params{
		Enabled:      true,
		MinBodyPct:   8,
		MinBodyX:     4,
		MaxTopPct:    20,
		MinVolX:      5,
		PriorBars:    10,
		MinPriorBars: 3,
		MinPrice:     0.20,
		MinDollarVol: 100_000,
		PbWindowMin:  20,
		TouchPct:     1,
		HoldPct:      1,
		Floor:        FloorMid,
		EmaPeriod:    9,
	}
}

// ─── The candle ──────────────────────────────────────────────────────────

// Hit is what a dildo measured. BodyX / VolX are +Inf when the priors' mean
// body / volume was 0 (the gate passes); JSON row fields carry +Inf as null.
type Hit struct {
	BodyPct   float64
	BodyX     float64
	VolX      float64
	TopPct    float64
	DollarVol float64
}

// Classify judges one closed candle against its prior bars' sums (built
// oldest -> newest: sumAbsBody += |c-o|, sumVol += float64(v), over the up to
// PriorBars traded bars before it; n is their count). It allocates nothing.
// classifyDildo in dildoPb.ts, expression for expression.
func Classify(o, h, l, c float64, v int64, sumAbsBody, sumVol float64, n int, p Params) (Hit, bool) {
	if !(c > o) || !(o > 0) || !(h > l) {
		return Hit{}, false
	}
	bodyPct := (c - o) / o * 100
	if !(bodyPct >= p.MinBodyPct) {
		return Hit{}, false
	}
	if !(c >= p.MinPrice) {
		return Hit{}, false
	}
	dollarVol := c * float64(v)
	if !(dollarVol >= p.MinDollarVol) {
		return Hit{}, false
	}
	if !(n >= p.MinPriorBars) {
		return Hit{}, false
	}
	meanBody := sumAbsBody / float64(n)
	bodyX := math.Inf(1)
	if meanBody > 0 {
		bodyX = (c - o) / meanBody
	}
	if !(bodyX >= p.MinBodyX) {
		return Hit{}, false
	}
	meanVol := sumVol / float64(n)
	volX := math.Inf(1)
	if meanVol > 0 {
		volX = float64(v) / meanVol
	}
	if !(volX >= p.MinVolX) {
		return Hit{}, false
	}
	topPct := (h - c) / (h - l) * 100
	if !(topPct <= p.MaxTopPct) {
		return Hit{}, false
	}
	return Hit{BodyPct: bodyPct, BodyX: bodyX, VolX: volX, TopPct: topPct, DollarVol: dollarVol}, true
}

// Power candle (alert "SYM: power candle"): every green-dildo rule EXCEPT the
// close-near-high one - any top wick is allowed. KindPower / LabelPower name it
// in the signal history.
const (
	KindPower  = "power"
	LabelPower = "POWER"
)

// ClassifyPower is Classify without the MaxTopPct gate: Hit.TopPct is measured
// but not limited. A candle Classify accepts also passes here, so the caller
// excludes real dildos by trying Classify first. Allocates nothing.
func ClassifyPower(o, h, l, c float64, v int64, sumAbsBody, sumVol float64, n int, p Params) (Hit, bool) {
	q := p
	q.MaxTopPct = math.Inf(1)
	return Classify(o, h, l, c, v, sumAbsBody, sumVol, n, q)
}

// candleGates is the part of Classify that needs no prior bars, expression for
// expression: Evaluate runs it first and sums the priors only for a candle that
// passes, which keeps Evaluate's per-bar cost off the ~95 % of bars that are not
// even a big green candle (the result is identical: Classify re-runs the same
// gates).
func candleGates(o, h, l, c float64, v int64, p Params) bool {
	if !(c > o) || !(o > 0) || !(h > l) {
		return false
	}
	if !((c-o)/o*100 >= p.MinBodyPct) || !(c >= p.MinPrice) {
		return false
	}
	return c*float64(v) >= p.MinDollarVol
}

// priors sums bars[from:to] oldest -> newest (dildoPriors in dildoPb.ts).
func priors(bars []core.Bar, from, to int) (sumAbsBody, sumVol float64, n int) {
	for k := from; k < to; k++ {
		sumAbsBody += math.Abs(bars[k].C - bars[k].O)
		sumVol += float64(bars[k].V)
	}
	if to > from {
		n = to - from
	}
	return
}

// ─── Events and the result ───────────────────────────────────────────────

// Event kinds, labels and cancel reasons, identical to dildoPb.ts.
const (
	KindDildo  = "dildo"       // GREEN DILDO: alert 1's moment
	KindPb     = "dildoPb"     // 9 EMA PULLBACK: alert 2's moment
	KindCancel = "dildoCancel" // PB CANCELLED (list-only)

	LabelDildo  = "GREEN DILDO"
	LabelPb     = "9 EMA PULLBACK"
	LabelCancel = "PB CANCELLED"

	ReasonFloor = "floor" // the touch bar's low broke the floor
	ReasonEma   = "ema"   // the touch bar closed more than HoldPct under its EMA
	ReasonFaded = "faded" // the floor broke before any touch
)

// pbState values (Result.PbState, the scan's dildoPbState).
const (
	PbNone      = 0
	PbWatching  = 1
	PbTouching  = 2
	PbConfirmed = 3
	PbCancelled = 4
	PbExpired   = 5
)

// Event is one thing the rule called on a CLOSED bar. Price is the dildo's
// close, the touch bar's close (KindPb), or for a cancel the bar's low
// (floor / faded) or close (ema). EmaDistPct is set on KindPb only (NaN
// otherwise): the touch bar's close vs its own EMA, %.
type Event struct {
	Ts         int64
	Kind       string
	Label      string
	Price      float64
	Reason     string // KindCancel only
	EmaDistPct float64

	// what Note() formats from; not part of the contract
	ema       float64 // KindPb / ReasonEma: the touch bar's EMA
	floor     float64
	anchorTs  int64
	hit       Hit
	emaPeriod int
}

// Result is the rule's call for the LAST ET day in the bars: its latest dildo
// and that anchor's pullback. Float fields are NaN and timestamps 0 where the
// TS result says null.
type Result struct {
	Events []Event // every ET day in the bars, oldest first

	LastDildoTs      int64   // the latest dildo bar's start; 0 = none today
	LastDildoBodyPct float64 // its Hit (NaN = none)
	LastDildoBodyX   float64 // may be +Inf
	LastDildoVolX    float64 // may be +Inf
	LastDildoDvol    float64

	PbState    int     // PbNone .. PbExpired
	TouchTs    int64   // the touch bar (a closed touch bar of any verdict, or the forming bar while touching); 0 = none
	PbTs       int64   // the confirming touch bar (PbConfirmed); 0 = none
	PbPx       float64 // its close (PbConfirmed); NaN = none
	Floor      float64 // the anchor's floor; NaN = none
	DeadlineTs int64   // dildo ts + PbWindowMin; 0 = none
}

func emptyResult() Result {
	nan := math.NaN()
	return Result{LastDildoBodyPct: nan, LastDildoBodyX: nan, LastDildoVolX: nan, LastDildoDvol: nan, PbPx: nan, Floor: nan}
}

// ─── EMA (private copy of emaflag.emaSeries; ema_pin_test.go pins it) ────

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

// emaSeries is the fold over a whole close series (the form ema_pin_test.go
// pins to emaflag's).
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

// etLocation is America/New_York (a private copy of emaflag's; the fixed UTC-5
// fallback can't happen with time/tzdata linked in).
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

func etDayKey(ms int64) int {
	y, m, d := time.UnixMilli(ms).In(etLocation()).Date()
	return y*10000 + int(m)*100 + d
}

// ─── Evaluate ────────────────────────────────────────────────────────────

type anchor struct {
	ts       int64
	hit      Hit
	floor    float64
	deadline int64
	verdict  int // 0 pending, else PbConfirmed / PbCancelled / PbExpired
	touchTs  int64
	pbTs     int64
	pbPx     float64
}

// Evaluate runs the dildo + pullback rule over bars (ascending by T; may span
// the previous trading day + today) and returns every event plus the call for
// the last ET day. tfMs must be 60_000 (the rule is 1-minute only; anything
// else, or p.Enabled false, returns an empty result). The last bar is FORMING
// iff nowMs < last.T + tfMs: it never emits an event, it only moves PbState
// (touching / a floor break) - see evaluateDildo in dildoPb.ts.
func Evaluate(bars []core.Bar, tfMs, nowMs int64, p Params) Result {
	return evaluate(bars, tfMs, nowMs, p, nil, nil)
}

// evaluate is Evaluate; when loose is non-nil, a bar whose start is in seen
// (bars the engine already called a dildo) is judged with *loose instead of p
// (see EvaluateTolerant). Everything else - the pullback, the floor, the EMA -
// always uses p.
func evaluate(bars []core.Bar, tfMs, nowMs int64, p Params, loose *Params, seen []int64) Result {
	res := emptyResult()
	n := len(bars)
	if tfMs != 60_000 || !p.Enabled || n == 0 {
		return res
	}
	period := p.EmaPeriod
	if period < 1 {
		period = 1
	}
	fold := newEmaFold(period) // the chart's EMA, folded bar by bar (no per-bar arrays)
	ema := math.NaN()          // ema[j] after the fold of bar j; ema[j-1] before it
	forming := nowMs < bars[n-1].T+tfMs
	lastClosed := n - 1
	if forming {
		lastClosed = n - 2
	}
	windowMs := int64(p.PbWindowMin) * 60_000
	touchK := 1 + p.TouchPct/100
	holdK := 1 - p.HoldPct/100

	var pending, latest *anchor
	var events []Event
	emit := func(ev Event) { events = append(events, ev) }

	for j := 0; j <= lastClosed; j++ {
		b := &bars[j]
		emaPrev := ema // ema[j-1] (NaN for j == 0 and during the warm-up)
		ema = fold.next(b.C)
		// a) the pending anchor's pullback
		if a := pending; a != nil {
			if b.T <= a.deadline {
				if !math.IsNaN(emaPrev) {
					level := emaPrev * touchK
					if b.L <= level {
						a.touchTs = b.T
						ej := ema
						switch {
						case b.L <= a.floor:
							a.verdict = PbCancelled
							emit(Event{Ts: b.T, Kind: KindCancel, Label: LabelCancel, Price: b.L, Reason: ReasonFloor, EmaDistPct: math.NaN(), floor: a.floor, emaPeriod: p.EmaPeriod})
						case b.C < ej*holdK:
							a.verdict = PbCancelled
							emit(Event{Ts: b.T, Kind: KindCancel, Label: LabelCancel, Price: b.C, Reason: ReasonEma, EmaDistPct: math.NaN(), ema: ej, floor: a.floor, emaPeriod: p.EmaPeriod})
						default:
							a.verdict = PbConfirmed
							a.pbTs, a.pbPx = b.T, b.C
							emit(Event{Ts: b.T, Kind: KindPb, Label: LabelPb, Price: b.C, EmaDistPct: (b.C - ej) / ej * 100, ema: ej, floor: a.floor, anchorTs: a.ts, emaPeriod: p.EmaPeriod})
						}
						pending = nil
					} else if b.L <= a.floor {
						a.verdict = PbCancelled
						emit(Event{Ts: b.T, Kind: KindCancel, Label: LabelCancel, Price: b.L, Reason: ReasonFaded, EmaDistPct: math.NaN(), floor: a.floor, emaPeriod: p.EmaPeriod})
						pending = nil
					}
				}
			} else {
				a.verdict = PbExpired // silent
				pending = nil
			}
		}
		// b) is bar j itself a dildo? (latest supersedes)
		from := j - p.PriorBars
		if from < 0 {
			from = 0
		}
		cp := &p
		if loose != nil && containsTs(seen, b.T) {
			cp = loose
		}
		if !candleGates(b.O, b.H, b.L, b.C, b.V, *cp) {
			continue
		}
		sumAbsBody, sumVol, cnt := priors(bars, from, j)
		if hit, ok := Classify(b.O, b.H, b.L, b.C, b.V, sumAbsBody, sumVol, cnt, *cp); ok {
			floor := (b.O + b.C) / 2
			if p.Floor == FloorOpen {
				floor = b.O
			}
			a := &anchor{ts: b.T, hit: hit, floor: floor, deadline: b.T + windowMs, pbPx: math.NaN()}
			pending, latest = a, a
			emit(Event{Ts: b.T, Kind: KindDildo, Label: LabelDildo, Price: b.C, EmaDistPct: math.NaN(), floor: floor, hit: hit, emaPeriod: p.EmaPeriod})
		}
	}
	res.Events = events

	// ── the Result: the last ET day's latest dildo ──
	L := latest
	if L == nil || etDayKey(L.ts) != etDayKey(bars[n-1].T) {
		return res
	}
	res.LastDildoTs = L.ts
	res.LastDildoBodyPct, res.LastDildoBodyX, res.LastDildoVolX, res.LastDildoDvol = L.hit.BodyPct, L.hit.BodyX, L.hit.VolX, L.hit.DollarVol
	res.Floor, res.DeadlineTs = L.floor, L.deadline
	res.TouchTs, res.PbTs, res.PbPx = L.touchTs, L.pbTs, L.pbPx
	if L.verdict != 0 {
		res.PbState = L.verdict
		return res
	}
	st := 0
	if forming && n >= 2 {
		f := &bars[n-1]
		if e := ema; f.T <= L.deadline && !math.IsNaN(e) { // ema[n-2]: the last closed bar's
			if f.L <= L.floor {
				st = PbCancelled
			} else if f.L <= e*touchK {
				st = PbTouching
				res.TouchTs = f.T
			}
		}
	}
	if st == 0 {
		st = PbWatching
		if nowMs >= L.deadline+tfMs {
			st = PbExpired
		}
	}
	res.PbState = st
	return res
}

// ─── Notes (formatted on demand; the TS engine's text, byte for byte) ────

// Note is the event's one-line note exactly as dildoPb.ts writes it (ET clock
// times, fmtPx prices, JS toFixed rounding). No Go consumer reads it; the
// golden test pins it.
func (e Event) Note() string {
	P := strconv.Itoa(e.emaPeriod)
	at := fmtEt(e.Ts)
	switch e.Kind {
	case KindDildo:
		return at + " green dildo +" + toFixed(e.hit.BodyPct, 1) + "% body (" + xf(e.hit.BodyX, 1) + "x prior), " + xf(e.hit.VolX, 0) +
			"x volume, " + fmtDvol(e.hit.DollarVol) + "; floor " + fmtPx(e.floor)
	case KindPb:
		mins := jsRound(float64(e.Ts-e.anchorTs) / 60_000)
		return at + " pullback to the " + P + " EMA held: close " + fmtPx(e.Price) + " vs EMA " + fmtPx(e.ema) + " (" + spct1(e.EmaDistPct) + "), " +
			strconv.FormatFloat(mins, 'f', -1, 64) + " min after the dildo"
	case KindCancel:
		var why string
		switch e.Reason {
		case ReasonFloor:
			why = "low " + fmtPx(e.Price) + " broke the floor " + fmtPx(e.floor)
		case ReasonEma:
			why = "closed " + toFixed((e.ema-e.Price)/e.ema*100, 1) + "% under the " + P + " EMA"
		default:
			why = "faded below the floor " + fmtPx(e.floor) + " before reaching the " + P + " EMA"
		}
		return at + " pullback cancelled: " + why
	}
	return ""
}

// toFixed is JS Number.prototype.toFixed(d) for a finite x: the exact binary
// value rounded half AWAY from zero at d decimals ("pick the larger n"), with a
// '-' for x < 0 (so -0.04 -> "-0.0", -0 -> "0.0"). strconv rounds ties to even,
// which differs on exact ties (0.25 -> "0.2" vs JS "0.3"), hence math/big.
func toFixed(x float64, d int) string {
	switch {
	case math.IsNaN(x):
		return "NaN"
	case math.IsInf(x, 1):
		return "Infinity"
	case math.IsInf(x, -1):
		return "-Infinity"
	}
	neg := x < 0
	if neg {
		x = -x
	}
	r := new(big.Rat).SetFloat64(x)
	r.Mul(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(d)), nil)))
	r.Add(r, big.NewRat(1, 2))
	s := new(big.Int).Quo(r.Num(), r.Denom()).String() // floor: both are positive
	if d > 0 {
		if len(s) <= d {
			s = strings.Repeat("0", d-len(s)+1) + s
		}
		s = s[:len(s)-d] + "." + s[len(s)-d:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

// jsRound is JS Math.round: the nearest integer, ties toward +Inf. x - floor(x)
// is exact for every finite double, so the tie test is exact.
func jsRound(x float64) float64 {
	f := math.Floor(x)
	if x-f >= 0.5 {
		return f + 1
	}
	return f
}

// fmtPx is shortFade.ts's fmtPx: 4 decimals under $1, else 2.
func fmtPx(v float64) string {
	if math.Abs(v) < 1 {
		return toFixed(v, 4)
	}
	return toFixed(v, 2)
}

// fmtEt is shortFade.ts's fmtEt: the ET wall clock as HH:MM.
func fmtEt(ms int64) string {
	return time.UnixMilli(ms).In(etLocation()).Format("15:04")
}

// xf is a multiple for a note: toFixed, or '∞' for a +Inf (mean-of-0) multiple.
func xf(v float64, d int) string {
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return "∞"
	}
	return toFixed(v, d)
}

// fmtDvol is dildoPb.ts's fmtDvol: '$717k' under $1M, '$1.2M' from there.
func fmtDvol(v float64) string {
	if v < 1e6 {
		return "$" + strconv.FormatFloat(jsRound(v/1e3), 'f', -1, 64) + "k"
	}
	return "$" + toFixed(v/1e6, 1) + "M"
}

// spct1 is the signed one-decimal percent ('+1.6%'), never '-0.0%'.
func spct1(v float64) string {
	s := toFixed(v, 1)
	if s == "-0.0" {
		s = "0.0"
	}
	if !strings.HasPrefix(s, "-") {
		s = "+" + s
	}
	return s + "%"
}
