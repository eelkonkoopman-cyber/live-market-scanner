// Package dlevel computes a symbol's DAILY price levels from daily bars and
// tracks which of them price has crossed today, for the "Daily Breakout" scan
// (scan fields dbLevel / dbType / dbPct / dbAge).
//
// Levels (all from completed daily bars, today excluded):
//   - 14D HIGH: the highest high of the last 14 trading days;
//   - PIVOT:    a swing-high, a day whose high is above the highs of the 2 days
//     on each side, within the last 60 trading days;
//   - PDH:      the previous trading day's high.
//
// Two levels at the same price collapse into the more significant kind
// (14D HIGH over PIVOT over PDH). The package depends on core only; the engine
// owns one Tracker per ticker and steps it per aggregate.
package dlevel

import (
	"sort"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// Kind ranks a level's significance; the value is also the scan sort rank
// (14D HIGH sorts first).
type Kind uint8

const (
	Kind14D   Kind = 1
	KindPivot Kind = 2
	KindPDH   Kind = 3
)

func (k Kind) String() string {
	switch k {
	case Kind14D:
		return "14D HIGH"
	case KindPivot:
		return "PIVOT"
	case KindPDH:
		return "PDH"
	}
	return ""
}

const (
	HighDays   = 14 // trading days in the 14D HIGH
	PivotDays  = 60 // trading days searched for swing highs
	pivotSides = 2  // days on each side a pivot must exceed
)

// Level is one daily level.
type Level struct {
	Price float32
	Kind  Kind
}

// Compute returns the levels (ascending by price) from bars (oldest first,
// Date "2006-01-02"). Bars dated today or later are ignored. withPDH false
// omits the previous-day high (the caller knows the newest stored bar is not
// the previous trading day, so its high would be mislabelled).
func Compute(bars []core.DailyBar, today string, withPDH bool) []Level {
	n := 0
	for n < len(bars) && bars[n].Date < today {
		n++
	}
	bars = bars[:n]
	if n == 0 {
		return nil
	}
	var lv []Level
	// 14D HIGH
	start := max(n-HighDays, 0)
	hi := 0.0
	for _, b := range bars[start:] {
		if b.H > hi {
			hi = b.H
		}
	}
	if hi > 0 {
		lv = append(lv, Level{float32(hi), Kind14D})
	}
	// Pivots: bars[i] with i in [n-60, n-1-2], needing 2 bars on each side.
	ps := max(n-PivotDays, pivotSides)
	for i := ps; i+pivotSides < n; i++ {
		h := bars[i].H
		if h <= 0 {
			continue
		}
		ok := true
		for d := 1; d <= pivotSides; d++ {
			if bars[i-d].H >= h || bars[i+d].H >= h {
				ok = false
				break
			}
		}
		if ok {
			lv = append(lv, Level{float32(h), KindPivot})
		}
	}
	if pdh := bars[n-1].H; withPDH && pdh > 0 {
		lv = append(lv, Level{float32(pdh), KindPDH})
	}
	// Collapse equal prices into the most significant kind.
	sort.Slice(lv, func(i, j int) bool {
		if lv[i].Price != lv[j].Price {
			return lv[i].Price < lv[j].Price
		}
		return lv[i].Kind < lv[j].Kind
	})
	out := lv[:0]
	for _, l := range lv {
		if len(out) > 0 && out[len(out)-1].Price == l.Price {
			continue
		}
		out = append(out, l)
	}
	return out
}

// Same reports whether two level lists are identical.
func Same(a, b []Level) bool {
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

// Tracker follows today's crosses of one symbol's levels.
type Tracker struct {
	levels  []Level // ascending by price
	crossed []bool
	ts      []int64 // EndMs of the aggregate that made the cross
}

// Set installs the day's levels. Identical levels keep the crossing state.
func (t *Tracker) Set(lv []Level) {
	if Same(t.levels, lv) {
		return
	}
	t.levels = append(t.levels[:0], lv...)
	t.crossed = make([]bool, len(lv))
	t.ts = make([]int64, len(lv))
}

// Reset forgets today's crosses (day rollover); the levels stay until the
// loader replaces them.
func (t *Tracker) Reset() {
	for i := range t.crossed {
		t.crossed[i] = false
		t.ts[i] = 0
	}
}

// Len is the number of levels.
func (t *Tracker) Len() int { return len(t.levels) }

// Step folds one aggregate: ref is the price before it (previous last, or the
// previous close for the day's first print; 0 = unknown), hi/cl its high and
// close. A level is crossed when ref was at or below it and the aggregate
// traded strictly above it; a close at or below the level un-crosses it.
func (t *Tracker) Step(ref, hi, cl float32, endMs int64) {
	if cl <= 0 {
		return
	}
	if hi < cl {
		hi = cl
	}
	for i, l := range t.levels {
		if ref > 0 && ref <= l.Price && hi > l.Price && !t.crossed[i] {
			t.crossed[i] = true
			t.ts[i] = endMs
		}
		if cl <= l.Price {
			t.crossed[i] = false
			t.ts[i] = 0
		}
	}
}

// Best returns the highest level that today's trading crossed and last is
// still above. A level counts as crossed when the tracker saw it, or when the
// day's low (lod) was below it and last is above it now (a restart lost the
// moment: ms is 0). ms is the cross time (EndMs) or 0 when unknown.
func (t *Tracker) Best(last, lod float32, lodSet bool) (l Level, ms int64, ok bool) {
	if last <= 0 {
		return l, 0, false
	}
	for i := len(t.levels) - 1; i >= 0; i-- {
		lv := t.levels[i]
		if last <= lv.Price {
			continue
		}
		if t.crossed[i] {
			return lv, t.ts[i], true
		}
		if lodSet && lod > 0 && lod < lv.Price {
			return lv, 0, true
		}
	}
	return l, 0, false
}

// PrevLow returns the low of the newest bar dated before today, or 0 when
// there is none, it is not positive, or (withPrev false) the newest stored bar
// is not the previous trading day (its low would be mislabelled).
func PrevLow(bars []core.DailyBar, today string, withPrev bool) float32 {
	if !withPrev {
		return 0
	}
	for i := len(bars) - 1; i >= 0; i-- {
		if bars[i].Date < today {
			if bars[i].L > 0 {
				return float32(bars[i].L)
			}
			return 0
		}
	}
	return 0
}

// LowTracker follows today's break BELOW one symbol's previous-day low (PDL).
type LowTracker struct {
	pdl    float32
	broken bool
	ts     int64
}

// Set installs the PDL; the same value keeps the break state.
func (t *LowTracker) Set(pdl float32) {
	if t.pdl == pdl {
		return
	}
	t.pdl, t.broken, t.ts = pdl, false, 0
}

// Reset forgets today's break (day rollover).
func (t *LowTracker) Reset() { t.broken, t.ts = false, 0 }

// Level is the installed PDL (0 = none).
func (t *LowTracker) Level() float32 { return t.pdl }

// Step folds one aggregate: ref is the price before it (previous last, or the
// previous close for the day's first print; 0 = unknown), lo/cl its low and
// close. A break is ref at or above the PDL and the aggregate trading strictly
// below it; a close at or above the PDL clears it, so a later re-break counts
// again with a new time.
func (t *LowTracker) Step(ref, lo, cl float32, endMs int64) {
	if t.pdl <= 0 || cl <= 0 {
		return
	}
	if lo > cl {
		lo = cl
	}
	if ref > 0 && ref >= t.pdl && lo < t.pdl && !t.broken {
		t.broken = true
		t.ts = endMs
	}
	if cl >= t.pdl {
		t.broken = false
		t.ts = 0
	}
}

// Best reports the PDL when today's trading broke it and last is still below.
// Restart fallback: the day's high (hod) at or above the PDL with last below
// counts, with ms 0 (age unknown). ms is the break time (EndMs) or 0.
func (t *LowTracker) Best(last, hod float32, hodSet bool) (pdl float32, ms int64, ok bool) {
	if t.pdl <= 0 || last <= 0 || last >= t.pdl {
		return 0, 0, false
	}
	if t.broken {
		return t.pdl, t.ts, true
	}
	if hodSet && hod >= t.pdl {
		return t.pdl, 0, true
	}
	return 0, 0, false
}
