// Package volbreak is the VOLUME BREAK rule (docs/volume-break.md): a
// continuation scalp on stocks in play. An ALERT, not a system: nothing is
// ever ordered.
//
//  1. a consolidation RANGE on closed 1-minute bars: the longest run of bars,
//     ending at the bar before the one judged, whose (high - low) / low stays
//     within MaxRangePct; it must span at least MinRangeMin minutes and its high
//     must have been tested at least MinTouches times (bars whose high came
//     within TouchPct of it, separate visits at least TouchSepMin minutes apart);
//  2. the BREAK: a bar closes above the range high on at least MinVolX x the
//     range's mean 1-minute volume ("VOL BREAK");
//  3. the ENTRY: within EntryWindowMin minutes, the first later bar whose low
//     touches or comes within EmaNearPct of the 1-minute 9 EMA while it still
//     closes above the range high ("VOL BREAK 9EMA"). A close back under the
//     range high first invalidates the break (Failed); no pullback in time
//     expires it (Expired);
//  4. ONCE PER LEVEL: a range must start after the latest break bar, so the
//     same level never fires twice and a new range has to form first;
//  5. ONCE PER DAY: only the first VOL BREAK of the ET day per symbol (its
//     9 EMA entry may still follow); NewDay re-arms it.
//
// Judge is a fixed-size value: Step allocates nothing (BenchmarkStep).
package volbreak

import "math"

// The rule's tunables (constants so the alert always means the same thing).
const (
	MinDayChgPct   = 10.0      // universe: the bar's close is at least this % over the previous close
	MinSessVol     = 1_000_000 // universe: session volume at least this many shares
	MinRangeMin    = 30        // the range spans at least this many minutes
	MaxRangePct    = 12.0      // the range is tight: (high - low) / low <= this %
	TouchPct       = 0.5       // a bar whose high is within this % of the range high tests it
	MinTouches     = 2         // tests of the range high
	TouchSepMin    = 5         // the first and the last test at least this many minutes apart
	MinVolX        = 3.0       // the breaking bar's volume vs the range's mean 1-minute volume
	EmaNearPct     = 0.3       // the pullback's low within this % of the 9 EMA (or through it)
	EntryWindowMin = 30        // the pullback must come within this many minutes of the break
	Ring           = 120       // bars kept: a longer range is measured over its newest 2 hours
)

// Signal history kinds and labels.
const (
	KindBreak  = "volBreak"
	KindEntry  = "volBreakEntry"
	LabelBreak = "VOL BREAK"
	LabelEntry = "VOL BREAK 9EMA"
)

const minMs = 60_000

// Bar is one closed, traded 1-minute bar; Ms is its START.
type Bar struct {
	Ms   int64
	H, L float64
	C    float64
	V    int64
}

// State is the latest break's state (the scan's vbState).
type State int8

const (
	StateNone     State = 0
	StateWatching State = 1 // broke; waiting for the 9 EMA pullback
	StateEntry    State = 2 // the pullback came: the entry alert fired
	StateFailed   State = 3 // a close back under the range high first
	StateExpired  State = 4 // no pullback within EntryWindowMin (or the ET day ended)
)

// Event is what one Step called.
type Event int8

const (
	EvNone Event = iota
	EvBreak
	EvEntry
)

// Range is a consolidation found by FindRange.
type Range struct {
	High, Low float64
	StartMs   int64 // the first bar's start
	Bars      int
	Touches   int
	AvgVol    float64
}

// Judge is one ticker's rule state.
type Judge struct {
	ring    [Ring]Bar
	n, head int
	afterMs int64 // a range must start AFTER this bar (the latest break): once per level
	broke   bool  // a VOL BREAK fired this ET day: once per day

	State    State
	BreakMs  int64   // the break bar's start
	Level    float64 // the range high that broke
	VolX     float64 // the break bar's volume / the range's mean
	BreakEma float64 // the 9 EMA at the break close (NaN = not seeded)
	EntryMs  int64   // the entry bar's start (StateEntry)
	EntryPx  float64 // the 9 EMA at the entry bar (the limit to work)
}

// NewDay starts an ET day: the bars are forgotten, a watching break expires.
func (j *Judge) NewDay() {
	j.Gap()
	j.afterMs = 0
	j.broke = false
	if j.State == StateWatching {
		j.State = StateExpired
	}
}

// Gap forgets the bars (a part-observed minute: what was seen has a hole).
func (j *Judge) Gap() { j.n, j.head = 0, 0 }

// Step judges one closed bar: ema is the 1-minute 9 EMA INCLUDING this bar
// (NaN = not seeded), inPlay the universe gate at this bar. The bar then joins
// the ring.
func (j *Judge) Step(b Bar, ema float64, inPlay bool) Event {
	ev := EvNone
	if j.State == StateWatching {
		switch {
		case b.C < j.Level:
			j.State = StateFailed
		case b.Ms-j.BreakMs > EntryWindowMin*minMs:
			j.State = StateExpired
		case ema > 0 && b.L <= ema*(1+EmaNearPct/100):
			j.State, j.EntryMs, j.EntryPx = StateEntry, b.Ms, ema
			ev = EvEntry
		}
	}
	if ev == EvNone && j.State != StateWatching && inPlay {
		if r, ok := j.FindRange(); ok && b.C > r.High && r.AvgVol > 0 && float64(b.V) >= MinVolX*r.AvgVol {
			j.State, j.BreakMs, j.Level, j.VolX, j.BreakEma = StateWatching, b.Ms, r.High, float64(b.V)/r.AvgVol, ema
			j.EntryMs, j.EntryPx = 0, math.NaN()
			j.afterMs, j.broke = b.Ms, true
			ev = EvBreak
		}
	}
	j.push(b)
	return ev
}

func (j *Judge) push(b Bar) {
	j.ring[j.head] = b
	if j.head++; j.head == Ring {
		j.head = 0
	}
	if j.n < Ring {
		j.n++
	}
}

// FindRange measures the consolidation ending at the newest bar in the ring:
// the longest run back from it (bars after the latest break only) whose
// (high - low) / low stays within MaxRangePct. ok when it spans MinRangeMin
// minutes and its high was tested MinTouches times. No allocation.
func (j *Judge) FindRange() (r Range, ok bool) {
	if j.n == 0 {
		return r, false
	}
	newest := j.head - 1
	if newest < 0 {
		newest = Ring - 1
	}
	endMs := j.ring[newest].Ms + minMs
	hi, lo := math.Inf(-1), math.Inf(1)
	var sumV float64
	i, k := newest, 0
	for ; k < j.n; k++ {
		b := &j.ring[i]
		if b.Ms <= j.afterMs {
			break
		}
		nh, nl := max(hi, b.H), min(lo, b.L)
		if !(nl > 0) || (nh-nl)/nl*100 > MaxRangePct {
			break
		}
		hi, lo = nh, nl
		sumV += float64(b.V)
		r.StartMs = b.Ms
		if i--; i < 0 {
			i = Ring - 1
		}
	}
	if k == 0 || endMs-r.StartMs < MinRangeMin*minMs {
		return r, false
	}
	r.High, r.Low, r.Bars, r.AvgVol = hi, lo, k, sumV/float64(k)
	// the tests of the high: separate visits (a bar out of the zone between),
	// oldest -> newest, the first and the last TouchSepMin minutes apart
	zone := hi * (1 - TouchPct/100)
	var firstMs, lastMs int64
	in := false
	for m := 0; m < k; m++ {
		i++
		if i == Ring {
			i = 0
		}
		b := &j.ring[i]
		if b.H >= zone {
			if !in {
				if r.Touches == 0 {
					firstMs = b.Ms
				}
				lastMs = b.Ms
				r.Touches++
			}
			in = true
		} else {
			in = false
		}
	}
	ok = r.Touches >= MinTouches && lastMs-firstMs >= TouchSepMin*minMs
	return r, ok
}

// InPlay is the universe gate: up MinDayChgPct on the day (close vs the
// previous close) on at least MinSessVol shares.
func InPlay(c, prevClose float64, sessVol int64) bool {
	return prevClose > 0 && (c/prevClose-1)*100 >= MinDayChgPct && sessVol >= MinSessVol
}
