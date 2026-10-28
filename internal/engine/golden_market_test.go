package engine

import (
	"math"
	"math/rand/v2"
	"sort"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dlevel"
)

// The golden test's (and the benchmarks') synthetic market: a deterministic
// per-second aggregate feed for N tickers plus every reference table the engine
// takes. All randomness is a PCG stream per ticker (plus one per scenario) with
// the derived draws written out by hand - no exp/log/trig - so the numbers are
// the same on every Go version and machine: the golden file pins the engine,
// never the maths library.
//
// Prices are generated a MINUTE at a time (the engine's closed-bar judges work
// on 1-minute bars): a plan gives each minute an OHLCV bar and the seconds that
// print, then lays the bar out as per-second aggregates whose open, close,
// extremes and volume add back up to the plan. Most minutes are random walks
// with bursts (pumps, dumps, volume climaxes) and slow waves; a few tickers
// follow scripted minute-by-minute patterns that trigger the judges (green
// dildo, power candle, climax -> bottom / fail / expiry, bouncy ball short,
// ignition) on purpose.

// goldRand is a PCG stream with the derived helpers written out by hand.
type goldRand struct{ p *rand.PCG }

func newGoldRand(seed, stream uint64) *goldRand {
	return &goldRand{p: rand.NewPCG(seed, stream^0x9e3779b97f4a7c15)}
}

func (r *goldRand) f() float64                     { return float64(r.p.Uint64()>>11) / (1 << 53) }
func (r *goldRand) intn(n int) int                 { return int(r.f() * float64(n)) }
func (r *goldRand) chance(p float64) bool          { return r.f() < p }
func (r *goldRand) between(lo, hi float64) float64 { return lo + (hi-lo)*r.f() }
func (r *goldRand) cube(lo, hi float64) float64    { u := r.f(); return lo + (hi-lo)*u*u*u }

// norm is a bounded bell (Irwin-Hall of four uniforms, unit variance).
func (r *goldRand) norm() float64 { return (r.f() + r.f() + r.f() + r.f() - 2) * 1.7320508075688772 }

// goldPx rounds to a tick: a cent from $1 up, a hundredth of a cent below.
func goldPx(x float64) float64 {
	if x >= 1 {
		return math.Round(x*100) / 100
	}
	return math.Round(x*10000) / 10000
}

func goldClamp(x, lo, hi float64) float64 { return math.Min(math.Max(x, lo), hi) }

// goldSym is a bijection from i to a distinct 4-letter symbol (5 with a W
// suffix for every seventh), scrambled so the order is unrelated to i.
func goldSym(i int) string {
	x := (uint64(i)*48271 + 7) % 456976 // 26^4; 48271 is coprime to 26
	var b [5]byte
	for k := 3; k >= 0; k-- {
		b[k] = byte('A' + x%26)
		x /= 26
	}
	if i%7 == 3 {
		b[4] = 'W'
		return string(b[:5])
	}
	return string(b[:4])
}

// goldDayKey is the ET trading-day number (04:00 ET to 04:00 ET, EDT) of an
// epoch second.
func goldDayKey(sec int64) int64 { return (sec - 8*3600) / 86400 }

// goldSecOfDay is the seconds since ET midnight of an epoch second (EDT).
func goldSecOfDay(sec int64) int64 { return ((sec-4*3600)%86400 + 86400) % 86400 }

// goldSession returns the activity and print-size factors of an epoch minute:
// thin pre- and post-market, the opening burst, the regular session.
func goldSession(min int64) (act, size float64) {
	m := goldSecOfDay(min*60) / 60
	switch {
	case m < 240:
		return 0.2, 0.5
	case m < 570:
		return 0.35, 0.6
	case m < 575:
		return 3.0, 1.6
	case m < 960:
		return 1.0, 1.0
	case m < 1200:
		return 0.3, 0.6
	}
	return 0, 0
}

// goldPrint is one planned per-second aggregate of the minute being delivered.
type goldPrint struct {
	ok               bool
	o, h, l, c, vw   float64
	v                int64
	acc              int64   // running day volume after this print (AccVol)
	dayOpen, dayVwap float64 // the official open (0 before 09:30) and the day VWAP (0 = the feed omits it)
	noOHL, noStart   bool    // the feed omitted open/high/low, or the window start
	late             int8    // delivered this many seconds late (0 = on time)
}

// goldBar is one minute's plan.
type goldBar struct {
	o, h, l, c float64
	v          int64
	secs       []int // the seconds of the minute that print, ascending
}

// goldRel is one scripted bar relative to the script's anchor price: prices are
// multiples of the anchor, v a multiple of the base minute volume.
type goldRel struct {
	o, h, l, c float64
	v          float64
	n          int // printing seconds
	first      bool
	dollars    float64 // first bar only: the base minute's dollar volume
}

type goldTk struct {
	idx      int
	sym      string
	rng      *goldRand
	p0       float64
	prev     float64 // previous close the day-1 seed carries (0 = unknown)
	vol      float64 // 1-minute return sd
	act      float64 // chance of a print per second
	size     float64 // shares per print
	dailyVol float64 // typical day volume (reference-data scale)
	class    int     // 0 hot .. 4 ultra-thin
	pBurst   float64

	waveAmp  float64 // per-minute drift of the slow wave (0 = none)
	waveHalf int64   // minutes per half wave
	waveOff  int64

	firstMin  int64 // first epoch minute it may trade (late appearers); 0 = from the start
	idle      [][2]int64
	deadFrom  int64 // no more prints from this epoch minute; 0 = alive
	evicted   bool  // dropped from the universe (kill) until revive
	noDayVwap bool
	otc       bool

	script  map[int64]goldRel
	anchor  float64
	scriptV float64 // shares per base minute

	px         float64 // last close (0 = never traded)
	burstLeft  int
	burstDrift float64
	burstVolX  float64
	burstActX  float64
	followDump bool
	curMin     int64
	prints     [60]goldPrint
	secBuf     [60]int
	dayKey     int64
	dayVol     int64
	dayNum     float64
	dayDen     float64
	dayOpen    float64
}

type goldMarketCfg struct {
	seed    uint64
	n       int
	minutes []int64 // the epoch minutes the scenario simulates (for late starts, idle spans, delistings); may be nil
}

type goldMarket struct {
	cfg  goldMarketCfg
	tk   []*goldTk
	late map[int64][]core.Agg
}

func newGoldMarket(cfg goldMarketCfg) *goldMarket {
	m := &goldMarket{cfg: cfg, late: map[int64][]core.Agg{}}
	pick := func(r *goldRand) int64 {
		if len(cfg.minutes) == 0 {
			return 0
		}
		return cfg.minutes[r.intn(len(cfg.minutes))]
	}
	for i := 0; i < cfg.n; i++ {
		r := newGoldRand(cfg.seed, uint64(i)+1)
		tk := &goldTk{idx: i, sym: goldSym(i), rng: r, script: map[int64]goldRel{}, burstVolX: 1, burstActX: 1}
		u := r.f()
		switch {
		case u < 0.08:
			tk.class, tk.act, tk.pBurst = 0, 0.85, 0.006
		case u < 0.30:
			tk.class, tk.act, tk.pBurst = 1, 0.35, 0.006
		case u < 0.60:
			tk.class, tk.act, tk.pBurst = 2, 0.10, 0.002
		case u < 0.85:
			tk.class, tk.act, tk.pBurst = 3, 0.02, 0.002
		default:
			tk.class, tk.act, tk.pBurst = 4, 0.004, 0.001
		}
		tk.act *= r.between(0.7, 1.3)
		v := r.f()
		switch {
		case v < 0.40:
			tk.p0 = goldPx(r.cube(0.15, 3.0))
			tk.vol = 0.012
		case v < 0.75:
			tk.p0 = goldPx(r.between(3, 25))
			tk.vol = 0.006
		case v < 0.95:
			tk.p0 = goldPx(r.between(25, 150))
			tk.vol = 0.004
		default:
			tk.p0 = goldPx(r.between(150, 500))
			tk.vol = 0.003
		}
		if tk.p0 < 0.1 {
			tk.p0 = 0.1
		}
		tk.vol *= r.between(0.6, 1.6)
		dollars := r.between(150, 4000)
		if tk.class == 0 {
			dollars *= 4
		}
		tk.size = math.Max(1, math.Round(dollars/tk.p0))
		tk.dailyVol = math.Min(math.Max(tk.size*tk.act*60*700, 20_000), 80_000_000)
		if !r.chance(0.08) {
			tk.prev = goldPx(tk.p0 * r.between(0.9, 1.1))
		}
		if r.chance(0.4) {
			tk.waveAmp = r.between(0.0015, 0.004)
			tk.waveHalf = int64(14 + r.intn(11))
			tk.waveOff = int64(r.intn(40))
		}
		tk.noDayVwap = r.chance(0.1)
		tk.otc = r.chance(0.05)
		if r.chance(0.12) {
			tk.firstMin = pick(r)
		}
		if r.chance(0.15) && len(cfg.minutes) > 0 {
			for s := 1 + r.intn(2); s > 0; s-- {
				a := r.intn(len(cfg.minutes))
				b := a + 8 + r.intn(23)
				if b >= len(cfg.minutes) {
					b = len(cfg.minutes) - 1
				}
				tk.idle = append(tk.idle, [2]int64{cfg.minutes[a], cfg.minutes[b]})
			}
		}
		if r.chance(0.04) {
			tk.deadFrom = pick(r)
		}
		m.tk = append(m.tk, tk)
	}
	return m
}

// kill stops ticker i printing from now on (the rest of this minute too), as
// a symbol leaving the universe; late prints already queued still arrive.
func (m *goldMarket) kill(i int) {
	tk := m.tk[i]
	tk.evicted = true
	tk.prints = [60]goldPrint{}
}

// revive lets a killed ticker print again from its next minute.
func (m *goldMarket) revive(i int) { m.tk[i].evicted = false }

// byIdx returns ticker i.
func (m *goldMarket) byIdx(i int) *goldTk { return m.tk[i] }

func (tk *goldTk) inIdle(min int64) bool {
	for _, s := range tk.idle {
		if min >= s[0] && min <= s[1] {
			return true
		}
	}
	return false
}

// second returns the aggregates that reach the engine during wall second sec:
// the prints of second sec-1 (EndMs == sec*1000, the clock-aligned window that
// just closed) plus any late ones that fall due now.
func (m *goldMarket) second(sec int64) []core.Agg {
	k := sec - 1
	min := k / 60
	var out []core.Agg
	for _, tk := range m.tk {
		if tk.curMin != min {
			tk.plan(min)
		}
		p := &tk.prints[k%60]
		if !p.ok {
			continue
		}
		a := tk.aggOf(k, p)
		if p.late > 0 {
			due := sec + int64(p.late)
			m.late[due] = append(m.late[due], a)
			continue
		}
		out = append(out, a)
	}
	if q := m.late[sec]; len(q) > 0 {
		out = append(out, q...)
		delete(m.late, sec)
	}
	return out
}

func (tk *goldTk) aggOf(k int64, p *goldPrint) core.Agg {
	a := core.Agg{
		Sym: tk.sym, Open: p.o, High: p.h, Low: p.l, Close: p.c, VWAP: p.vw, Vol: p.v,
		AccVol: p.acc, DayOpen: p.dayOpen, DayVWAP: p.dayVwap,
		StartMs: k * 1000, EndMs: k*1000 + 1000, OTC: tk.otc,
	}
	if p.noOHL {
		a.Open, a.High, a.Low = 0, 0, 0
	}
	if p.noStart {
		a.StartMs = 0
	}
	return a
}

// plan lays out epoch minute min.
func (tk *goldTk) plan(min int64) {
	tk.curMin = min
	tk.prints = [60]goldPrint{}
	if dk := goldDayKey(min * 60); dk != tk.dayKey {
		tk.dayKey = dk
		if tk.px > 0 { // the overnight gap
			tk.px = math.Max(goldPx(tk.px*(1+tk.rng.norm()*0.02)), 0.05)
		}
		tk.dayVol, tk.dayNum, tk.dayDen, tk.dayOpen = 0, 0, 0, 0
		tk.burstLeft = 0
	}
	if tk.deadFrom != 0 && min >= tk.deadFrom {
		return
	}
	if tk.evicted {
		return
	}
	if min < tk.firstMin || tk.inIdle(min) {
		return
	}
	if rel, ok := tk.script[min]; ok {
		tk.layout(min*60, tk.scriptBar(rel), false)
		return
	}
	if b, ok := tk.randomBar(min); ok {
		tk.layout(min*60, b, true)
	}
}

// randomBar is an unscripted minute: a random walk with the ticker's slow wave,
// mean reversion to its reference price, and bursts (pump, then often a dump).
func (tk *goldTk) randomBar(min int64) (goldBar, bool) {
	r := tk.rng
	sf, sz := goldSession(min)
	if sf == 0 {
		return goldBar{}, false
	}
	if tk.burstLeft == 0 {
		tk.burstDrift, tk.burstVolX, tk.burstActX = 0, 1, 1
		switch {
		case tk.followDump:
			tk.followDump = false
			tk.burstLeft = 2 + r.intn(4)
			tk.burstDrift = -r.between(0.01, 0.025)
			tk.burstVolX, tk.burstActX = r.between(3, 12), r.between(1.5, 3)
		case r.chance(tk.pBurst * sf):
			tk.burstLeft = 3 + r.intn(7)
			d := r.between(0.008, 0.03)
			if r.chance(0.35) {
				d = -d
			} else {
				tk.followDump = r.chance(0.5)
			}
			tk.burstDrift = d
			tk.burstVolX, tk.burstActX = r.between(4, 25), r.between(1.5, 4)
		}
	}
	bursting := tk.burstLeft > 0
	if bursting {
		tk.burstLeft--
	}
	pAct := math.Min(0.98, tk.act*sf*tk.burstActX)
	n := 0
	for k := 0; k < 60; k++ {
		if r.f() < pAct {
			tk.secBuf[n] = k
			n++
		}
	}
	if n == 0 {
		return goldBar{}, false
	}
	if tk.px == 0 {
		tk.px = tk.p0
	}
	vol := tk.vol
	if bursting {
		vol *= 2.5
	}
	o := goldPx(tk.px * (1 + 0.15*vol*r.norm()))
	if o < 0.05 {
		o = 0.05
	}
	drift := tk.burstDrift - 0.01*(o/tk.p0-1)
	if tk.waveAmp != 0 {
		if ((min+tk.waveOff)/tk.waveHalf)%2 == 0 {
			drift += tk.waveAmp
		} else {
			drift -= tk.waveAmp
		}
	}
	c := goldPx(o * (1 + vol*r.norm() + drift))
	if c < 0.05 {
		c = 0.05
	}
	h := goldPx(math.Max(o, c) * (1 + math.Abs(r.norm())*vol*0.6))
	l := goldPx(math.Min(o, c) * (1 - math.Abs(r.norm())*vol*0.6))
	h = math.Max(h, math.Max(o, c))
	l = math.Min(l, math.Min(o, c))
	if l < 0.01 {
		l = 0.01
	}
	v := int64(float64(n) * tk.size * sz * tk.burstVolX * r.cube(0.3, 2.5))
	if v < int64(n) {
		v = int64(n)
	}
	return goldBar{o: o, h: h, l: l, c: c, v: v, secs: tk.secBuf[:n]}, true
}

// scriptBar resolves one scripted bar against the anchor price (the ticker's
// price when the script's first bar comes up).
func (tk *goldTk) scriptBar(rel goldRel) goldBar {
	if rel.first || tk.anchor == 0 {
		tk.anchor = tk.px
		if tk.anchor == 0 {
			tk.anchor = tk.p0
		}
		tk.scriptV = rel.dollars / tk.anchor
	}
	a := tk.anchor
	o, c := goldPx(a*rel.o), goldPx(a*rel.c)
	h := math.Max(goldPx(a*rel.h), math.Max(o, c))
	l := math.Min(goldPx(a*rel.l), math.Min(o, c))
	n := min(max(rel.n, 1), 60)
	// n distinct seconds, ascending: the first n of a hand-rolled shuffle
	var all [60]int
	for i := range all {
		all[i] = i
	}
	for i := 0; i < n; i++ {
		j := i + tk.rng.intn(60-i)
		all[i], all[j] = all[j], all[i]
	}
	copy(tk.secBuf[:n], all[:n])
	sort.Ints(tk.secBuf[:n])
	v := int64(rel.v * tk.scriptV)
	if v < int64(n) {
		v = int64(n)
	}
	return goldBar{o: o, h: h, l: l, c: c, v: v, secs: tk.secBuf[:n]}
}

// layout turns a minute plan into per-second prints. anomalies adds the feed's
// oddities (omitted open/high/low, omitted window start, zero volume, late
// delivery); scripted bars stay clean so their outcome is exact.
func (tk *goldTk) layout(sec0 int64, b goldBar, anomalies bool) {
	r := tk.rng
	n := len(b.secs)
	var ps [60]float64
	var w [60]float64
	span := b.h - b.l
	sum := 0.0
	for j := 0; j < n; j++ {
		fr := float64(j+1) / float64(n)
		p := b.o + (b.c-b.o)*fr + r.norm()*span*0.15
		ps[j] = goldClamp(goldPx(p), b.l, b.h)
		w[j] = 0.3 + r.f()
		sum += w[j]
	}
	ps[n-1] = b.c
	jh, jl := r.intn(n), r.intn(n)
	rem := b.v
	for j := 0; j < n; j++ {
		var v int64
		if j == n-1 {
			v = rem
		} else {
			v = min(int64(float64(b.v)*w[j]/sum), rem)
		}
		rem -= v
		open := b.o
		if j > 0 {
			open = ps[j-1]
		}
		cl := ps[j]
		hi, lo := math.Max(open, cl), math.Min(open, cl)
		if j == jh {
			hi = b.h
		}
		if j == jl {
			lo = b.l
		}
		p := goldPrint{ok: true, o: open, h: hi, l: lo, c: cl, v: v}
		p.vw = goldPx((open + hi + lo + cl) / 4)
		if anomalies {
			if r.chance(0.06) {
				p.vw = 0
			}
			p.noOHL = r.chance(0.03)
			p.noStart = r.chance(0.01)
			if r.chance(0.01) {
				p.late = int8(1 + r.intn(6))
			}
			if r.chance(0.004) {
				p.v = 0
			}
		}
		// the running day state, in time order
		s := sec0 + int64(b.secs[j])
		if tk.dayOpen == 0 && goldSecOfDay(s) >= 9*3600+30*60 {
			tk.dayOpen = open
		}
		tk.dayVol += p.v
		vw := p.vw
		if vw == 0 {
			vw = cl
		}
		tk.dayNum += vw * float64(p.v)
		tk.dayDen += float64(p.v)
		p.acc, p.dayOpen = tk.dayVol, tk.dayOpen
		if !tk.noDayVwap && tk.dayDen > 0 {
			p.dayVwap = math.Round(tk.dayNum/tk.dayDen*10000) / 10000
		}
		tk.prints[b.secs[j]] = p
	}
	tk.px = b.c
}

// ─── scripted minutes ───────────────────────────────────────────────────────

// goldChain appends scripted bars to a ticker: each bar opens where the last
// closed. dollars is the base minute's dollar volume (the volume unit v = 1).
type goldChain struct {
	tk    *goldTk
	min   int64
	lvl   float64
	first bool
	usd   float64
}

func newGoldChain(tk *goldTk, startMin int64, dollars float64) *goldChain {
	tk.firstMin, tk.idle, tk.deadFrom = 0, nil, 0 // a scripted ticker trades every minute of its script
	return &goldChain{tk: tk, min: startMin, lvl: 1, first: true, usd: dollars}
}

// add appends a bar: ret is close/open-1, top and bot the wicks beyond the
// body (fractions of the price), v the volume multiple, n the printing seconds.
func (c *goldChain) add(ret, top, bot, v float64, n int) {
	o := c.lvl
	cl := o * (1 + ret)
	rel := goldRel{o: o, c: cl, h: math.Max(o, cl) * (1 + top), l: math.Min(o, cl) * (1 - bot), v: v, n: n}
	if c.first {
		rel.first, rel.dollars, c.first = true, c.usd, false
	}
	c.tk.script[c.min] = rel
	c.min++
	c.lvl = cl
}

// addHL appends a bar closing at cl with the given high and low (widened to
// cover its open and close).
func (c *goldChain) addHL(cl, hi, lo, v float64, n int) {
	o := c.lvl
	rel := goldRel{o: o, c: cl, h: math.Max(hi, math.Max(o, cl)), l: math.Min(lo, math.Min(o, cl)), v: v, n: n}
	if c.first {
		rel.first, rel.dollars, c.first = true, c.usd, false
	}
	c.tk.script[c.min] = rel
	c.min++
	c.lvl = cl
}

// quiet appends k small-bodied bars (sd is the body's sd as a fraction).
func (c *goldChain) quiet(k int, sd float64) {
	r := c.tk.rng
	for i := 0; i < k; i++ {
		ret := goldClamp(r.norm()*sd, -3*sd, 3*sd)
		c.add(ret, math.Abs(r.norm())*0.0008+0.0004, math.Abs(r.norm())*0.0008+0.0004, 0.8+0.4*r.f(), 8+r.intn(18))
	}
}

// decline appends k bars falling by about step each, wicks small.
func (c *goldChain) decline(k int, step float64) {
	r := c.tk.rng
	for i := 0; i < k; i++ {
		c.add(-step+r.norm()*step*0.2, 0.001, 0.001, 0.9+0.2*r.f(), 10+r.intn(15))
	}
}

// dildo appends a green dildo: a big green body closing near its high on a
// volume spike. top is the top wick as a fraction of the price.
func (c *goldChain) dildo(body, top, volX float64) {
	c.add(body, top, 0.002, volX, 40)
}

// climax appends the volume climax bar: a hammer under the base on a volume
// spike (a big drop with a long lower wick).
func (c *goldChain) climax(volX float64) {
	c.add(-0.06, 0.003, 0.0266, volX, 40)
}

// bbs appends a bouncy ball short base: four touches of a support with lower
// highs on each bounce, then (when brk) the closed bar breaking it.
func (c *goldChain) bbs(brk bool) {
	hs := [4]float64{1.030, 1.024, 1.018, 1.012}
	c.lvl = 1.02
	for i, h := range hs {
		c.addHL(1.008, c.lvl, 1.0005, 1, 14) // the touch: a dip to the support that closes back up
		c.addHL(h-0.004, h, 1.006, 1, 14)
		c.addHL(h-0.008, h-0.003, 1.006, 1, 14)
		c.addHL(1.010, h-0.006, 1.007, 1, 14)
		_ = i
	}
	if brk {
		c.addHL(0.994, c.lvl, 0.990, 1.4, 20) // closes under the support: BOUNCY BALL SHORT
		c.addHL(0.985, 0.995, 0.982, 1.1, 14)
	}
}

// scriptDildo schedules a green-dildo pattern ending at the dildo bar and
// returns the bar's epoch minute. wide makes the base too wide to be early.
func (m *goldMarket) scriptDildo(idx int, startMin int64, base int, wide bool, body, top, volX float64) int64 {
	c := newGoldChain(m.tk[idx], startMin, 40_000)
	if wide {
		c.quiet(base, 0.012)
	} else {
		c.quiet(base, 0.0015)
	}
	at := c.min
	c.dildo(body, top, volX)
	c.quiet(3, 0.002)
	return at
}

// scriptIgnition schedules a quiet base and the small first candle out of it.
// follow adds the dildo right after, as the real setup has.
func (m *goldMarket) scriptIgnition(idx int, startMin int64, base int, dollars, volX float64, follow bool) int64 {
	c := newGoldChain(m.tk[idx], startMin, dollars)
	c.quiet(base, 0.0015)
	at := c.min
	c.add(0.04, 0.002, 0.001, volX, 30)
	if follow {
		c.dildo(0.12, 0.004, 9)
	}
	c.quiet(3, 0.002)
	return at
}

// scriptClimax schedules a decline, the climax bar and one of three outcomes
// (0 bottom confirmed, 1 failed, 2 expires after five quiet bars, 3 a retest of
// the exact low on a bigger spike that confirms the old climax and is a new one).
func (m *goldMarket) scriptClimax(idx int, startMin int64, down int, outcome int) int64 {
	c := newGoldChain(m.tk[idx], startMin, 40_000)
	c.decline(down, 0.0035)
	at := c.min
	c.climax(15)
	switch outcome {
	case 0:
		c.add(0.035, 0.002, 0.02, 1.2, 20) // closes over the climax midpoint without undercutting
	case 1:
		c.add(-0.01, 0.002, 0.04, 1.2, 20) // a new low under the climax low
	case 2:
		for i := 0; i < 6; i++ {
			c.add(0.002, 0.001, 0.002, 1, 12)
		}
	case 3:
		c.add(0.035, 0.002, 0.02, 1.2, 20)
		c.climax(30)
	}
	c.quiet(3, 0.002)
	return at
}

// scriptBbs schedules a bouncy ball short base after some quiet bars.
func (m *goldMarket) scriptBbs(idx int, startMin int64, lead int, brk bool) int64 {
	c := newGoldChain(m.tk[idx], startMin, 40_000)
	c.quiet(lead, 0.002)
	at := c.min
	c.bbs(brk)
	c.quiet(2, 0.002)
	return at
}

// scriptVwapCross schedules a heavy regular-hours ticker whose 1-minute 9 EMA
// moves away from the session VWAP one way and then crosses it the other way
// (dir +1: falls, then a bull cross; -1: rises, then a bear cross), on enough
// volume for the 9EMA VWAP cross universe. It returns the turning minute.
func (m *goldMarket) scriptVwapCross(idx int, startMin int64, dollars float64, dir float64) int64 {
	c := newGoldChain(m.tk[idx], startMin, dollars)
	c.decline(6, 0.006*dir)
	at := c.min
	for i := 0; i < 8; i++ {
		c.add(0.008*dir, 0.001, 0.001, 2, 30)
	}
	c.quiet(2, 0.001)
	return at
}

// ─── reference data ─────────────────────────────────────────────────────────

// goldBaseline builds an RVOL baseline: most tickers with a front-loaded
// cumulative curve and 20 sessions behind it; some with too few sessions, some
// with no counts, some absent, some with zero early buckets or a flat regular
// session (no AvgVol). variant shifts the numbers for a later swap.
func (m *goldMarket) baseline(seed uint64, variant int) *core.BaselineTable {
	bt := &core.BaselineTable{Cum: map[string][core.BucketCount]float64{}, Count: map[string][core.BucketCount]uint16{}}
	scale := 1 + 0.15*float64(variant)
	for i, tk := range m.tk {
		r := newGoldRand(seed, uint64(i)+1)
		u := r.f()
		if u < 0.06 {
			continue
		}
		total := tk.dailyVol * r.between(0.5, 2) * scale
		var cum [core.BucketCount]float64
		var cnt [core.BucketCount]uint16
		for b := range cum {
			var s float64
			switch {
			case b < 66:
				s = 0.15 * float64(b+1) / 66
			case b < 144:
				s = 0.15 + 0.75*float64(b-65)/78
			default:
				s = 0.9 + 0.1*float64(b-143)/48
			}
			cum[b] = math.Round(total * s)
			cnt[b] = 20
		}
		switch {
		case u < 0.10: // counts missing: Cum alone is not enough
			bt.Cum[tk.sym] = cum
			continue
		case u < 0.20: // too few sessions to trust
			for b := range cnt {
				cnt[b] = 4
			}
		case u < 0.23: // exactly the minimum
			for b := range cnt {
				cnt[b] = minBaselineSessions
			}
		case u < 0.30: // a few thin buckets
			for k := 0; k < 12; k++ {
				cnt[r.intn(core.BucketCount)] = 3
			}
		}
		switch r.intn(20) {
		case 0: // no volume in the early buckets (non-positive: treated as null)
			for b := 0; b < 30; b++ {
				cum[b] = 0
			}
		case 1: // flat regular session: AvgVol non-positive
			for b := 66; b < core.BucketCount; b++ {
				cum[b] = cum[66]
			}
		}
		bt.Cum[tk.sym], bt.Count[tk.sym] = cum, cnt
	}
	return bt
}

// goldFloats builds the float reference: absent, float + shares out, shares out
// only (the proxy), both unknown, a zero float, float only.
func (m *goldMarket) floats(seed uint64, variant int) map[string]core.FloatRef {
	out := map[string]core.FloatRef{}
	for i, tk := range m.tk {
		r := newGoldRand(seed, uint64(i)+1)
		u := r.f()
		if variant > 0 && r.chance(0.15) {
			u = r.f() // a different category after the swap
		}
		so := int64(tk.dailyVol * r.between(5, 200))
		fl := int64(float64(so) * r.between(0.3, 0.9))
		fr := core.FloatRef{Ticker: tk.sym, AsOf: 1_790_000_000, Source: "details_v3"}
		switch {
		case u < 0.20:
			continue
		case u < 0.55:
			fr.SharesOut, fr.FloatShares = &so, &fl
		case u < 0.80:
			fr.SharesOut = &so
		case u < 0.90:
			// both unknown
		case u < 0.93:
			zero := int64(0)
			fr.SharesOut, fr.FloatShares = &so, &zero
		default:
			fr.FloatShares = &fl
		}
		out[tk.sym] = fr
	}
	return out
}

// goldTypes builds the security-type reference (about 85 % of tickers).
func (m *goldMarket) types(seed uint64, variant int) map[string]string {
	out := map[string]string{}
	for i, tk := range m.tk {
		r := newGoldRand(seed, uint64(i)+1)
		u, v := r.f(), r.f()
		if variant > 0 && r.chance(0.2) {
			v = r.f()
		}
		if u < 0.15 {
			continue
		}
		switch {
		case v < 0.7:
			out[tk.sym] = "CS"
		case v < 0.82:
			out[tk.sym] = "ETF"
		default:
			out[tk.sym] = "ADRC"
		}
	}
	return out
}

// goldLevels builds daily levels (14D HIGH / PIVOT / PDH) around each price for
// ~45 % of the tickers, ascending by price; variant moves a share of them.
func (m *goldMarket) levels(seed uint64, variant int) map[string][]dlevel.Level {
	out := map[string][]dlevel.Level{}
	for i, tk := range m.tk {
		r := newGoldRand(seed, uint64(i)+1)
		if !r.chance(0.45) {
			continue
		}
		k := 1 + r.intn(4)
		var lv []dlevel.Level
		for j := 0; j < k; j++ {
			p := tk.p0 * r.between(0.92, 1.3)
			if variant > 0 {
				p *= r.between(0.97, 1.03)
			}
			lv = append(lv, dlevel.Level{Price: float32(goldPx(p)), Kind: dlevel.Kind(1 + r.intn(3))})
		}
		sort.Slice(lv, func(a, b int) bool { return lv[a].Price < lv[b].Price })
		out[tk.sym] = lv
	}
	return out
}

// goldPrevLows builds previous-day lows for ~45 % of the tickers.
func (m *goldMarket) prevLows(seed uint64, variant int) map[string]float32 {
	out := map[string]float32{}
	for i, tk := range m.tk {
		r := newGoldRand(seed, uint64(i)+1)
		if !r.chance(0.45) {
			continue
		}
		p := tk.p0 * r.between(0.88, 1.03)
		if variant > 0 {
			p *= r.between(0.98, 1.02)
		}
		out[tk.sym] = float32(goldPx(p))
	}
	return out
}

// goldAhHighs builds previous-day after-hours highs for ~40 % of the tickers.
func (m *goldMarket) ahHighs(seed uint64, variant int) map[string]float64 {
	out := map[string]float64{}
	for i, tk := range m.tk {
		r := newGoldRand(seed, uint64(i)+1)
		if !r.chance(0.4) {
			continue
		}
		p := tk.p0 * r.between(0.97, 1.12)
		if variant > 0 {
			p *= r.between(0.98, 1.04)
		}
		out[tk.sym] = goldPx(p)
	}
	return out
}

// goldAdv10 builds 10-day average daily volumes for ~75 % of the tickers (a few
// non-positive ones, which the engine ignores).
func (m *goldMarket) adv10(seed uint64, variant int) map[string]float64 {
	out := map[string]float64{}
	for i, tk := range m.tk {
		r := newGoldRand(seed, uint64(i)+1)
		if !r.chance(0.75) {
			continue
		}
		a := math.Round(tk.dailyVol * r.between(0.05, 1.5) * (1 + 0.2*float64(variant)))
		if r.chance(0.02) {
			a = -a
		}
		out[tk.sym] = a
	}
	return out
}

// goldFrozen builds the prefill's raw session stats for ~25 % of the tickers.
func (m *goldMarket) frozen(seed uint64) map[string]core.SessionStats {
	out := map[string]core.SessionStats{}
	for i, tk := range m.tk {
		r := newGoldRand(seed, uint64(i)+1)
		if !r.chance(0.25) {
			continue
		}
		hi := goldPx(tk.p0 * r.between(1.0, 1.15))
		lo := goldPx(tk.p0 * r.between(0.88, 1.0))
		st := core.SessionStats{Hi: hi, Lo: lo, Close: goldPx(tk.p0 * r.between(0.95, 1.08)), Vol: int64(tk.dailyVol * r.between(0.01, 0.3))}
		if r.chance(0.8) {
			st.Vwap = goldPx((hi + lo) / 2)
		}
		if r.chance(0.05) {
			st.Hi = 0 // unknown high: the slot carries no hi/gap/fade
		}
		out[tk.sym] = st
	}
	return out
}

// goldSeed builds the snapshot seed the ingest layer hands the engine for a
// ticker at nowMs: the last known state, the previous close and the day volume
// so far (distort scales it, to exercise the SET-not-add contract).
func (tk *goldTk) seed(nowMs int64, distort float64) core.Seed {
	last := tk.px
	if last == 0 {
		last = tk.prev
	}
	if last == 0 {
		last = tk.p0
	}
	s := core.Seed{
		Agg: core.Agg{
			Sym: tk.sym, Open: last, High: last, Low: last, Close: last, VWAP: last,
			Vol: int64(float64(tk.dayVol) * distort), AccVol: tk.dayVol,
			DayOpen: tk.dayOpen, DayVWAP: last, StartMs: nowMs - 1000, EndMs: nowMs,
		},
		PrevClose: tk.prev,
	}
	if tk.px != 0 && tk.dayVol > 0 {
		s.High, s.Low = goldPx(last*1.01), goldPx(last*0.99)
	}
	return s
}
