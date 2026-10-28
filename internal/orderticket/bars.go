package orderticket

import (
	"math"
	"sort"
	"sync"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// EMAPeriod is the ticket's EMA: the 9 EMA on 1-minute bars.
const EMAPeriod = 9

// StaleAfter: an EMA whose newest closed bar ended longer ago than this is
// stale, and nothing is sent on it.
const StaleAfter = 2 * time.Minute

// emaSeries is the chart's EMA (web/src/signals/emaFlag.ts emaSeries, the
// vendored chart registry's emaArr): SMA of the first `period` closes as the
// seed, then the standard k = 2/(period+1) fold; NaN during warm-up. It runs
// continuously over previous day + today, exactly as the chart draws it.
func emaSeries(closes []float64, period int) []float64 {
	out := make([]float64, len(closes))
	k := 2 / float64(period+1)
	var prev, sum float64
	for i, c := range closes {
		switch {
		case i < period-1:
			sum += c
			out[i] = math.NaN()
		case i == period-1:
			sum += c
			prev = sum / float64(period)
			out[i] = prev
		default:
			prev = c*k + prev*(1-k)
			out[i] = prev
		}
	}
	return out
}

// Quote is what the ticket reads for one symbol.
type Quote struct {
	Sym        string  `json:"sym"`
	Price      float64 `json:"price"`      // last trade (newest bar close, forming or not)
	EMA        float64 `json:"ema"`        // 9 EMA over CLOSED 1m bars (0 = none)
	LastClosed int64   `json:"lastClosed"` // start ms of the newest closed bar (0 = none)
	Stale      bool    `json:"stale"`      // no closed bar ended within StaleAfter
	Bars       int     `json:"bars"`
	Live       bool    `json:"live"`   // EMA includes the forming bar (Set: "incl. live candle")
	AgeSec     int64   `json:"ageSec"` // seconds since the newest closed bar ENDED (EMA age)
	LastAt     int64   `json:"lastAt"` // start ms of the newest bar (forming or not) behind Price
}

// book holds 1-minute bars per tracked symbol: seeded from REST, then folded
// from the live per-second aggregates.
type book struct {
	mu   sync.Mutex
	bars map[string]map[int64]*core.Bar // sym -> minute start ms -> bar
	seen map[string]time.Time           // sym -> last seed time
	// readiness (see ready): newest REST bar per symbol, and the first
	// minute the live tap delivered since the symbol was tracked
	newest    map[string]int64
	firstLive map[string]int64
}

func newBook() *book {
	return &book{bars: map[string]map[int64]*core.Bar{}, seen: map[string]time.Time{},
		newest: map[string]int64{}, firstLive: map[string]int64{}}
}

// ensure starts keeping live aggregates for a tracked symbol BEFORE its REST
// seed lands (they used to be dropped until then, leaving a hole between the
// seed's newest bar and the first live bar kept).
func (b *book) ensure(sym string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.bars[sym] == nil {
		b.bars[sym] = map[int64]*core.Bar{}
	}
}

// ready: the bars are complete up to the live feed. False until the first
// seed; false while the live feed has a minute later than the minute after
// the seed's newest bar (a hole the REST seed has not covered yet), unless the
// seed was fetched >= 5 min after that live minute ended (then the hole is
// taken as minutes without trades: REST had time to include them). The
// ticket re-seeds every 10 s while not ready.
func (b *book) ready(sym string) (bool, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	at, ok := b.seen[sym]
	if !ok {
		return false, "loading 9EMA"
	}
	fl := b.firstLive[sym]
	nw := b.newest[sym]
	if fl == 0 || fl <= nw+60000 {
		return true, ""
	}
	if at.UnixMilli() >= fl+60000+5*60000 {
		return true, ""
	}
	return false, "9EMA loading (bars missing before the live feed)"
}

func minuteOf(ms int64) int64 { return ms - ((ms%60000)+60000)%60000 }

// seed merges REST 1-minute bars (they replace what is there for the same
// minute: the REST bar is the complete one).
func (b *book) seed(sym string, bars []core.Bar, at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	m := b.bars[sym]
	if m == nil {
		m = map[int64]*core.Bar{}
		b.bars[sym] = m
	}
	for _, x := range bars {
		if x.C <= 0 {
			continue
		}
		bb := x
		bb.T = minuteOf(x.T)
		m[bb.T] = &bb
		if bb.T > b.newest[sym] {
			b.newest[sym] = bb.T
		}
	}
	b.seen[sym] = at
}

func (b *book) lastSeed(sym string) (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.seen[sym]
	return t, ok
}

// feed folds one live aggregate into its minute bar. Unknown symbols are ignored.
func (b *book) feed(a core.Agg) {
	if a.Close <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	m := b.bars[a.Sym]
	if m == nil {
		return
	}
	t := minuteOf(a.StartMs)
	if b.firstLive[a.Sym] == 0 {
		b.firstLive[a.Sym] = t
	}
	x := m[t]
	if x == nil {
		m[t] = &core.Bar{T: t, O: a.Open, H: a.High, L: a.Low, C: a.Close, V: a.Vol}
		return
	}
	if a.High > x.H {
		x.H = a.High
	}
	if a.Low < x.L || x.L <= 0 {
		x.L = a.Low
	}
	x.C = a.Close
	x.V += a.Vol
}

// extremes: lowest low / highest high of the minute bars starting in
// [from, to) (ms), and how many there were.
func (b *book) extremes(sym string, from, to int64) (lo, hi float64, n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, x := range b.bars[sym] {
		if k < from || k >= to {
			continue
		}
		if n == 0 || x.L < lo {
			lo = x.L
		}
		if n == 0 || x.H > hi {
			hi = x.H
		}
		n++
	}
	return lo, hi, n
}

// drop forgets a symbol.
func (b *book) drop(sym string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.bars, sym)
	delete(b.seen, sym)
	delete(b.newest, sym)
	delete(b.firstLive, sym)
}

// quote reads the price and the 9 EMA over the bars that are CLOSED at now.
func (b *book) quote(sym string, now time.Time) Quote { return b.quoteMode(sym, now, false) }

// quoteMode: live=true also folds the forming (unclosed) bar's current close
// in as the last EMA input, like TWS's chart EMA; staleness/age still refer
// to the newest CLOSED bar.
func (b *book) quoteMode(sym string, now time.Time, live bool) Quote {
	q := Quote{Sym: sym, Stale: true, Live: live}
	b.mu.Lock()
	m := b.bars[sym]
	keys := make([]int64, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	nowMs := now.UnixMilli()
	closes := make([]float64, 0, len(keys))
	var lastClosed int64
	for _, k := range keys {
		if k+60000 <= nowMs {
			closes = append(closes, m[k].C)
			lastClosed = k
		}
	}
	forming := 0.0
	if len(keys) > 0 && keys[len(keys)-1]+60000 > nowMs {
		forming = m[keys[len(keys)-1]].C
	}
	if len(keys) > 0 {
		q.Price = m[keys[len(keys)-1]].C
		q.LastAt = keys[len(keys)-1]
	}
	b.mu.Unlock()
	q.Bars = len(closes)
	if len(closes) >= EMAPeriod {
		in := closes
		if live && forming > 0 {
			in = append(append([]float64(nil), closes...), forming)
		}
		e := emaSeries(in, EMAPeriod)
		q.EMA = e[len(e)-1]
		q.LastClosed = lastClosed
		q.Stale = nowMs-(lastClosed+60000) > StaleAfter.Milliseconds()
		q.AgeSec = (nowMs - (lastClosed + 60000)) / 1000
	}
	return q
}
