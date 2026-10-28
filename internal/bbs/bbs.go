// Package bbs is the Go port of the BOUNCY BALL SHORT rule the Signal window
// runs in the browser (web/src/signals/bouncyShort.ts). The alert is
// engine-native: the scanner engine's shared closed-1-minute-bar judge
// (internal/engine/bbs.go) keeps the day's last Lookback traded bars per ticker
// and calls Analyze on them; Evaluate here is the chart's whole-series form,
// and both are pinned to the TS rule by golden vectors
// (web/src/signals/fixtures/golden/bbs-golden.json; golden_test.go).
//
// The trader (2026-10-14): "It has a solid support and is making lower highs
// on each bounce of that support and after that will sell off sharply."
// Example AAOI 2026-10-14: support ~115.85 touched ~09:50, 10:05, 10:20, 10:35
// ET, bounce highs 119.9 -> 117.8 -> 117.4 -> 116.8, broke ~10:40 and sold to
// 110. An ALERT, a "look now" prompt, not a system (uncalibrated).
//
// THE STRUCTURE (Analyze over closed bars of ONE ET day, oldest -> newest):
//  1. Support. Walk back from the newest bar keeping S = the running minimum
//     low. Each bar whose low is under S*(1-band) (a materially lower low)
//     closes a candidate base: S over the bars after it. The first candidate
//     (highest support) whose touches and highs pass wins; else the walk goes
//     on with the lower low, and the last candidate is the whole window. S is
//     the minimum low of its base, so no close in it is under S*(1-band).
//  2. Touches. Forward over the base: a bar whose low <= S*(1+band) touches.
//     A touch starts a NEW touch only if it is >= MinSepBars after the previous
//     touch bar AND some bar since that touch reached S*(1+MinBouncePct)
//     (a real bounce); otherwise it extends the current touch.
//  3. Bounce highs. The highest high from a touch's (last) bar until the next
//     touch - or, for the last touch, until the newest bar - counted only once
//     it reached the bounce level. Lower highs: the trailing run of bounce highs
//     each <= the previous * (1-LowerHighPct).
//     OK = touches >= MinTouches and the trailing run has >= MinLowerHighs lower
//     highs (MinLowerHighs+1 highs).
//
// THE ALERT (per closed bar j of the day): with the window = the last Lookback
// bars before j that are after the day's latest break, if Analyze(window) is OK
// and close[j] < S*(1-BreakPct) -> BOUNCY BALL SHORT at the close. A break
// clears the window (the next base starts after it). Otherwise bar j joins the
// window and the row is FORMING when Analyze(window incl. j) is OK and
// close[j] <= S*(1+FormingNearPct).
//
// Pure: no clock reads; Analyze allocates nothing.
package bbs

import (
	"math"
	"sync"
	"time"

	_ "time/tzdata"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// Params mirrors BbsParams in bouncyShort.ts field for field (percentages in %).
type Params struct {
	Enabled        bool    // true
	Lookback       int     // 90   the base lives in the last N closed traded bars of the same ET day
	SupportBandPct float64 // 0.4  a touch: low <= S*(1+band); the base ends at a low < S*(1-band)
	MinTouches     int     // 3
	MinSepBars     int     // 3    a new touch is >= N bars after the previous touch bar
	MinBouncePct   float64 // 0.5  a bounce must reach S*(1+this)
	MinLowerHighs  int     // 2    trailing lower highs (MinLowerHighs+1 bounce highs)
	LowerHighPct   float64 // 0.2  each lower high <= the previous*(1-this)
	BreakPct       float64 // 0.3  the break: close < S*(1-this)
	FormingNearPct float64 // 1.0  forming: close <= S*(1+this)
}

// DefaultParams is DEFAULT_BBS_PARAMS in bouncyShort.ts.
func DefaultParams() Params {
	return Params{Enabled: true, Lookback: 90, SupportBandPct: 0.4, MinTouches: 3, MinSepBars: 3,
		MinBouncePct: 0.5, MinLowerHighs: 2, LowerHighPct: 0.2, BreakPct: 0.3, FormingNearPct: 1.0}
}

// Pattern is what Analyze found. Highs is the trailing lower-high run's length
// (bounce highs), FirstHigh / LastHigh its first and last high.
type Pattern struct {
	OK        bool
	Support   float64
	Touches   int
	Highs     int
	FirstHigh float64
	LastHigh  float64
	Start     int // the base's first index into the slices
}

// Analyze judges the base ending at the newest bar of hs/ls (oldest -> newest,
// one ET day, closed bars). Allocates nothing; analyzeBbs in bouncyShort.ts
// expression for expression.
func Analyze(hs, ls []float64, p Params) Pattern {
	n := len(ls)
	if n == 0 {
		return Pattern{}
	}
	band := p.SupportBandPct / 100
	s := math.Inf(1)
	for i := n - 1; i >= 0; i-- {
		if !math.IsInf(s, 1) && ls[i] < s*(1-band) {
			if pt := base(hs, ls, i+1, s, p); pt.OK {
				return pt
			}
		}
		if ls[i] < s {
			s = ls[i]
		}
	}
	return base(hs, ls, 0, s, p)
}

// base runs the touch / bounce pass over hs/ls[b:] against the support s.
func base(hs, ls []float64, b int, s float64, p Params) Pattern {
	n := len(ls)
	var pt Pattern
	band := p.SupportBandPct / 100
	thr := s * (1 + band)
	bounceLvl := s * (1 + p.MinBouncePct/100)
	lhK := 1 - p.LowerHighPct/100
	touches, lastT := 0, -1
	curHi := math.Inf(-1)
	bounced := false
	nH, run := 0, 0
	prevH, firstH := 0.0, 0.0
	for k := b; k <= n; k++ {
		// k == n is the end-of-base flush of the last bounce
		closeBounce := false
		if k == n {
			closeBounce = touches > 0 && bounced
		} else if ls[k] <= thr {
			if touches == 0 {
				touches = 1
			} else if k-lastT >= p.MinSepBars && bounced {
				closeBounce = true
				touches++
			}
		}
		if closeBounce {
			if nH > 0 && curHi <= prevH*lhK {
				run++
			} else {
				run, firstH = 1, curHi
			}
			prevH = curHi
			nH++
		}
		if k == n {
			break
		}
		if ls[k] <= thr {
			lastT = k
			curHi = hs[k]
			bounced = hs[k] >= bounceLvl
			continue
		}
		if touches > 0 {
			if hs[k] > curHi {
				curHi = hs[k]
			}
			if hs[k] >= bounceLvl {
				bounced = true
			}
		}
	}
	pt.Support, pt.Touches, pt.Start = s, touches, b
	if nH > 0 {
		pt.Highs, pt.FirstHigh, pt.LastHigh = run, firstH, prevH
	}
	pt.OK = touches >= p.MinTouches && run-1 >= p.MinLowerHighs
	return pt
}

// Breaks reports whether a close breaks an OK pattern's support.
func Breaks(pt Pattern, c float64, p Params) bool {
	return pt.OK && c < pt.Support*(1-p.BreakPct/100)
}

// Forming reports whether an OK pattern (including the bar closing at c) is
// near enough to its support to be watched.
func Forming(pt Pattern, c float64, p Params) bool {
	return pt.OK && c <= pt.Support*(1+p.FormingNearPct/100)
}

// Row / Result states.
const (
	StateNone    = 0
	StateForming = 1
	StateBroken  = 2
)

const (
	KindBbs  = "bbs"
	LabelBbs = "BOUNCY BALL SHORT"
)

// Event is one break.
type Event struct {
	Ts        int64
	Price     float64
	Support   float64
	Touches   int
	Highs     int
	FirstHigh float64
	LastHigh  float64
}

// Result is every break plus the last ET day's state. Support is the forming
// base's when State is forming, else the latest break's (NaN = none).
type Result struct {
	Events  []Event
	State   int
	Support float64
	LastTs  int64 // the latest break of the last ET day; 0 = none
}

var (
	etOnce sync.Once
	etLoc  *time.Location
)

func etDay(ms int64) int64 {
	etOnce.Do(func() {
		loc, err := time.LoadLocation("America/New_York")
		if err != nil {
			loc = time.FixedZone("ET", -5*3600)
		}
		etLoc = loc
	})
	t := time.UnixMilli(ms).In(etLoc)
	y, m, d := t.Date()
	return int64(y)*10000 + int64(m)*100 + int64(d)
}

// Evaluate runs the rule over bars (ascending, may span days). tfMs must be
// 60 000. The last bar is FORMING iff nowMs < last.T + tfMs and is ignored.
// evaluateBbs in bouncyShort.ts.
func Evaluate(bars []core.Bar, tfMs, nowMs int64, p Params) Result {
	res := Result{Support: math.NaN()}
	n := len(bars)
	if tfMs != 60_000 || !p.Enabled || n == 0 {
		return res
	}
	last := n - 1
	if nowMs < bars[n-1].T+tfMs {
		last = n - 2
	}
	look := p.Lookback
	if look < 1 {
		look = 1
	}
	hs := make([]float64, 0, n)
	ls := make([]float64, 0, n)
	ws := 0 // window start into hs/ls (hs/ls hold today's bars after the latest break)
	var day int64
	state := StateNone
	sup := math.NaN()
	var lastTs int64
	for j := 0; j <= last; j++ {
		b := &bars[j]
		if d := etDay(b.T); j == 0 || d != day {
			day = d
			hs, ls, ws = hs[:0], ls[:0], 0
			state, sup, lastTs = StateNone, math.NaN(), 0
		}
		if m := len(ls); m-ws > look {
			ws = m - look
		}
		pt := Analyze(hs[ws:], ls[ws:], p)
		if Breaks(pt, b.C, p) {
			res.Events = append(res.Events, Event{Ts: b.T, Price: b.C, Support: pt.Support, Touches: pt.Touches,
				Highs: pt.Highs, FirstHigh: pt.FirstHigh, LastHigh: pt.LastHigh})
			hs, ls, ws = hs[:0], ls[:0], 0
			state, sup, lastTs = StateBroken, pt.Support, b.T
			continue
		}
		hs, ls = append(hs, b.H), append(ls, b.L)
		if m := len(ls); m-ws > look {
			ws = m - look
		}
		if pf := Analyze(hs[ws:], ls[ws:], p); Forming(pf, b.C, p) {
			state, sup = StateForming, pf.Support
		} else if lastTs != 0 {
			state = StateBroken
			sup = latestSupport(res.Events)
		} else {
			state, sup = StateNone, math.NaN()
		}
	}
	res.State, res.Support, res.LastTs = state, sup, lastTs
	return res
}

func latestSupport(ev []Event) float64 {
	if len(ev) == 0 {
		return math.NaN()
	}
	return ev[len(ev)-1].Support
}
