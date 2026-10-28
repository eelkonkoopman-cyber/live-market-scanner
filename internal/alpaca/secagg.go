package alpaca

import (
	"sync"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// Trade is one decoded SIP trade.
type Trade struct {
	Sym   string
	Price float64
	Size  int64
	TS    int64 // epoch ms
	Conds []string
}

// Condition handling (CTA / UTP letter codes as Alpaca sends them), modelled on
// Massive/Polygon's aggregate rules: a trade whose condition says it does not
// reflect the current market price (average-price, cash, next-day, seller,
// prior-reference, contingent, derivatively priced, odd lot, official
// open/close reports, ...) does not move open/high/low/close. Odd lots still
// count toward volume (as Polygon's aggregates do); official open/close and
// corrected-close REPORTS add no volume (they restate earlier prints).
var (
	noPrice = set("B", "W", "4", "7", "9", "C", "G", "H", "I", "M", "N", "P", "Q", "R", "V", "Z")
	noVol   = set("M", "Q", "9")
)

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func eligible(conds []string) (price, vol bool) {
	price, vol = true, true
	for _, c := range conds {
		if noPrice[c] {
			price = false
		}
		if noVol[c] {
			vol = false
		}
	}
	return
}

type symAgg struct {
	sym string

	open        bool
	inList      bool // present in SecAgg.open
	sec         int64
	o, h, l, c  float64
	hasPx       bool
	vol         int64
	pvNum, pvDn float64
	carryVol    int64 // volume from seconds with no priced trade and no known price
	lastSec     int64 // last emitted second (0 = none)
	lastPx      float64

	day                  string
	dayVol               int64
	dayVwNum, dayVwDen   float64
	dayOpen              float64
	openAtMs, dayStartMs int64 // 09:30 ET / 00:00 ET of `day`, epoch ms
	dayEndMs             int64
}

// SecAgg turns a trade stream into Massive-style per-second aggregates. It is
// safe for concurrent use (the reader adds, a timer flushes).
type SecAgg struct {
	mu   sync.Mutex
	syms map[string]*symAgg
	open []*symAgg
}

func NewSecAgg() *SecAgg { return &SecAgg{syms: make(map[string]*symAgg, 12000)} }

func (a *SecAgg) state(sym string) *symAgg {
	s := a.syms[sym]
	if s == nil {
		s = &symAgg{sym: sym}
		a.syms[sym] = s
	}
	return s
}

func (s *symAgg) rollDay(ms int64) {
	if s.day != "" && ms >= s.dayStartMs && ms < s.dayEndMs {
		return
	}
	t := time.UnixMilli(ms).In(etLoc)
	d0 := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, etLoc)
	day := d0.Format("2006-01-02")
	if day == s.day {
		return
	}
	s.day = day
	s.dayStartMs = d0.UnixMilli()
	s.dayEndMs = d0.AddDate(0, 0, 1).UnixMilli()
	s.openAtMs = time.Date(t.Year(), t.Month(), t.Day(), 9, 30, 0, 0, etLoc).UnixMilli()
	s.dayVol, s.dayVwNum, s.dayVwDen, s.dayOpen = 0, 0, 0, 0
}

// Add folds one trade in and returns any aggregate it completed (the symbol's
// previous second, when this trade opens a newer one).
func (a *SecAgg) Add(t Trade) []core.Agg {
	if t.Sym == "" || t.TS <= 0 {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.state(t.Sym)
	var out []core.Agg
	sec := t.TS / 1000
	if s.open && sec > s.sec {
		if ag, ok := s.emit(); ok {
			out = append(out, ag)
		}
	}
	if !s.open {
		if s.lastSec != 0 && sec <= s.lastSec {
			sec = s.lastSec + 1 // late print after its second was emitted: fold forward
		}
		s.open, s.sec = true, sec
		if !s.inList {
			s.inList = true
			a.open = append(a.open, s)
		}
	}
	// (sec < s.sec while open: a slightly late print folds into the open second.)
	s.rollDay(t.TS)
	px, vo := eligible(t.Conds)
	if vo && t.Size > 0 {
		s.vol += t.Size
		s.dayVol += t.Size
		if px {
			s.dayVwNum += t.Price * float64(t.Size)
			s.dayVwDen += float64(t.Size)
		}
	}
	if px && t.Price > 0 {
		if !s.hasPx {
			s.o, s.h, s.l = t.Price, t.Price, t.Price
			s.hasPx = true
		}
		if t.Price > s.h {
			s.h = t.Price
		}
		if t.Price < s.l {
			s.l = t.Price
		}
		s.c = t.Price
		s.lastPx = t.Price
		if vo && t.Size > 0 {
			s.pvNum += t.Price * float64(t.Size)
			s.pvDn += float64(t.Size)
		}
		if s.dayOpen == 0 && t.TS >= s.openAtMs {
			s.dayOpen = t.Price
		}
	}
	return out
}

// emit closes the open bucket. ok=false when it carries nothing to report.
func (s *symAgg) emit() (core.Agg, bool) {
	s.open = false
	vol := s.vol + s.carryVol
	hasPx := s.hasPx
	o, h, l, c := s.o, s.h, s.l, s.c
	vw := 0.0
	if s.pvDn > 0 {
		vw = s.pvNum / s.pvDn
	}
	sec := s.sec
	s.vol, s.hasPx, s.pvNum, s.pvDn = 0, false, 0, 0
	if !hasPx {
		if s.lastPx == 0 || vol == 0 {
			s.carryVol = vol
			return core.Agg{}, false
		}
		o, h, l, c = s.lastPx, s.lastPx, s.lastPx, s.lastPx
	}
	if vw == 0 {
		vw = c
	}
	s.carryVol = 0
	s.lastSec = sec
	dvw := 0.0
	if s.dayVwDen > 0 {
		dvw = s.dayVwNum / s.dayVwDen
	}
	return core.Agg{
		Sym: s.sym, Open: o, High: h, Low: l, Close: c, VWAP: vw, Vol: vol,
		AccVol: s.dayVol, DayOpen: s.dayOpen, DayVWAP: dvw,
		StartMs: sec * 1000, EndMs: sec*1000 + 1000,
	}, true
}

// FlushBefore emits every open second that ended at or before cutMs.
func (a *SecAgg) FlushBefore(cutMs int64) []core.Agg {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []core.Agg
	keep := a.open[:0]
	for _, s := range a.open {
		if !s.open {
			s.inList = false
			continue // already emitted by Add
		}
		if (s.sec+1)*1000 <= cutMs {
			if ag, ok := s.emit(); ok {
				out = append(out, ag)
			}
			s.inList = false
			continue
		}
		keep = append(keep, s)
	}
	for i := len(keep); i < len(a.open); i++ {
		a.open[i] = nil
	}
	a.open = keep
	return out
}

// Seed primes day accumulators from a REST snapshot (reconnect / startup), so
// DayVWAP / AccVol / DayOpen carry the part of the day the stream missed. It
// never LOWERS volume already counted from trades.
func (a *SecAgg) Seed(seeds []core.Seed) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, sd := range seeds {
		if sd.Sym == "" || sd.EndMs <= 0 {
			continue
		}
		s := a.state(sd.Sym)
		s.rollDay(sd.EndMs)
		if sd.Vol >= s.dayVol && sd.Vol > 0 {
			s.dayVol = sd.Vol
			if sd.DayVWAP > 0 {
				s.dayVwNum, s.dayVwDen = sd.DayVWAP*float64(sd.Vol), float64(sd.Vol)
			}
		}
		if sd.DayOpen > 0 {
			s.dayOpen = sd.DayOpen
		}
		if s.lastPx == 0 && sd.Close > 0 {
			s.lastPx = sd.Close
		}
	}
}
