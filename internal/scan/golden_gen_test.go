package scan

import (
	"math"
	"math/rand/v2"
	"reflect"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// Deterministic market generator shared by the golden-equivalence test
// (golden_test.go) and the benchmarks (bench_process_test.go).
//
// It produces consecutive core.MetricSnapshot values shaped like the engine's:
// a dense Rows slice in an arbitrary (but seeded) order, Index filled sym ->
// position, float metrics Null (NaN) where the engine leaves them Null and
// ZERO where the engine leaves the scanner-side overlay fields zero, ages that
// tick up with the snapshot clock, rows that sit identical for many ticks,
// exact ties (thin names printing nothing, % changes rounded to a cent), signal
// groups that fire, expire and reset at the 04:00 / 09:30 / 16:00 boundaries.
//
// Everything is derived from one PCG stream and plain + - * / sqrt (no exp/log,
// and none of math/rand/v2's derived helpers), so the stream is bit-for-bit
// reproducible and the golden file does not depend on the Go version's maths.

var goldET = func() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return loc
}()

// goldAt is the epoch ms of h:m:s ET on the given day of October 2026 (the 20th
// is a Tuesday; DST is still in force until 1 November).
func goldAt(day, h, m, s int) int64 {
	return time.Date(2026, 10, day, h, m, s, 0, goldET).UnixMilli()
}

// goldSession maps an instant to the engine's session (ET clock).
func goldSession(asOf int64) core.Session {
	t := time.UnixMilli(asOf).In(goldET)
	switch m := t.Hour()*60 + t.Minute(); {
	case m >= 240 && m < 570:
		return core.SessionPre
	case m >= 570 && m < 960:
		return core.SessionRegular
	case m >= 960 && m < 1200:
		return core.SessionPost
	default:
		return core.SessionClosed
	}
}

// goldFrac is the share of the 04:00-20:00 session elapsed, the stand-in for
// the cumulative-volume baseline curve behind rvol.
func goldFrac(asOf int64, sess core.Session) float64 {
	if sess == core.SessionClosed {
		return 1
	}
	t := time.UnixMilli(asOf).In(goldET)
	return min(max(float64(t.Hour()*60+t.Minute()-240+5)/960, 0.01), 1)
}

// goldRand is a PCG stream with the derived helpers written out by hand.
type goldRand struct{ p *rand.PCG }

func newGoldRand(seed uint64) *goldRand {
	return &goldRand{p: rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)}
}

func (r *goldRand) f() float64                     { return float64(r.p.Uint64()>>11) / (1 << 53) }
func (r *goldRand) intn(n int) int                 { return int(r.f() * float64(n)) }
func (r *goldRand) chance(p float64) bool          { return r.f() < p }
func (r *goldRand) between(lo, hi float64) float64 { return lo + (hi-lo)*r.f() }
func (r *goldRand) cube(lo, hi float64) float64    { u := r.f(); return lo + (hi-lo)*u*u*u }

// norm is a bounded bell (Irwin-Hall of four uniforms, unit variance).
func (r *goldRand) norm() float64 { return (r.f() + r.f() + r.f() + r.f() - 2) * 1.7320508075688772 }

func goldRound(x float64, places int) float64 {
	p := 1.0
	for range places {
		p *= 10
	}
	return math.Round(x*p) / p
}

// goldPx rounds to a tick: a cent from $1 up, a hundredth of a cent below.
func goldPx(x float64) float64 {
	if x >= 1 {
		return goldRound(x, 2)
	}
	return goldRound(x, 4)
}

func goldPct(now, then float64) float64 { return (now/then - 1) * 100 }

func goldNaNOr(ok bool, v float64) float64 {
	if ok {
		return v
	}
	return core.Null
}

// goldSym is a bijection from i to a distinct 2-4 letter symbol, scrambled so
// the symbol order is unrelated to the generation order.
func goldSym(i int) string {
	x := (uint64(i)*48271 + 7) % 456976 // 26^4; 48271 is coprime to 26
	var b [4]byte
	for k := 3; k >= 0; k-- {
		b[k] = byte('A' + x%26)
		x /= 26
	}
	s := string(b[:])
	for len(s) > 2 && s[0] == 'A' {
		s = s[1:]
	}
	return s
}

// goldRowTemplate is a row as the engine's toRow leaves it: every float Null
// except the scanner-side overlay fields, which arrive as plain zeros.
var goldRowTemplate = func() core.Row {
	zero := map[string]bool{
		"DTrend": true, "Chg20d": true, "ShortFee": true,
		"FlagScore": true, "FlagState": true, "FlagPole": true, "FlagRange": true, "FlagRetrace": true, "FlagVolDry": true, "FlagBars": true,
		"IFlagScore": true, "IFlagState": true, "IFlagPole": true, "IFlagRange": true, "IFlagRetrace": true, "IFlagVolDry": true, "IFlagBars": true,
		"E9State": true, "E9Dist": true, "E9Trigger": true, "E9Stop": true, "E9FlagBars": true, "E9Age": true,
		"E9SlamAge": true, "E9SlamPct": true, "E9HodFlagAge": true,
	}
	var r core.Row
	v := reflect.ValueOf(&r).Elem()
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).Kind() == reflect.Float64 && !zero[v.Type().Field(i).Name] {
			v.Field(i).SetFloat(core.Null)
		}
	}
	r.NewsAge = -1
	return r
}()

type goldProf uint8

const (
	gpNormal goldProf = iota
	gpPenny
	gpETF
	gpThin
	gpADR
	gpHot
	gpGapper
)

// Signal groups; a ticker's ev[g] is the epoch ms its latest event began (0 =
// none today).
const (
	gsDildo = iota
	gsClimax
	gsBbs
	gsPc
	gsIgn
	gsEvBull
	gsEvBear
	gsDb
	gsPdl
	gsBrk5mLo
	gsBrk5mCl
	gsBrk5mClUp
	gsBrkPmh
	gsBrkAh
	gsBrkPmh5
	gsBrkAh5
	gsBrkEma
	gsN
)

type goldTicker struct {
	sym     string
	prof    goldProf
	secType string
	float   *int64
	shs     *int64
	short   *int64 // a ShortAvail the engine row already carries (the overlay leaves it alone when the sym is not in its map)

	sigma, drift float64 // % per print
	pAct         float64 // chance of printing in a step
	pSig         float64 // chance per print of a signal event
	volBase      float64 // shares per print
	grain        bool    // % changes rounded to 0.01, so equal values tie exactly
	lot          bool    // volumes in round lots
	hasBars      bool
	flick        bool // sometimes missing from a snapshot

	avg      float64 // average regular-session volume (volume scale; always valid)
	baseline bool    // AvgVol / Rvol known
	adv10    float64 // Null = unknown
	prevAh   float64 // previous after-hours high, 0 = none

	prevClose, dayOpen, last float64
	hod, lod                 float64
	hodSet                   bool
	vol, scopeVol            int64
	vwapNum, vwapDen         float64
	scopeNum, scopeDen       float64
	scopeOpen                float64
	scopeSet                 bool
	c1, c5, c15, ema         float64
	c1mChg, c1mRvol          float64
	p5lo, p5cl               float64
	prints                   int
	haltLeft                 int

	preDone bool
	preVol  int64
	pmh     float64

	ev                [gsN]int64
	noDbAge, noPdlAge bool
	fate              int   // climax: 0 runs out, 1 confirms, 2 fails
	confAfter         int64 // climax: ms after the bar that it confirms / fails
	bbsBreak          int64 // bouncy ball: epoch ms of the break bar, 0 = still forming
	listed            bool  // in g.sigIdx
}

type goldGenConfig struct {
	Rows       int
	Seed       uint64
	Active     float64 // scales every ticker's chance of printing in a step
	Start      int64   // epoch ms of the first snapshot; decides the session the market starts in
	EventSpan  int64   // signals present at the start began up to this many ms before it
	NoReorder  bool    // keep the row order fixed (an idle market)
	SignalRate float64 // scales every ticker's chance of a fresh signal (0 = 1)
	Flicker    float64 // share of tickers that drop out of some snapshots (a late-listed or re-added name); 0 = none
}

type goldGen struct {
	cfg    goldGenConfig
	rng    *goldRand
	tk     []goldTicker
	rows   []core.Row // current state, one per ticker
	order  []int      // snapshot row order: positions into rows
	sigIdx []int
	sess   core.Session
	frac   float64
	gen    uint64
}

func newGoldGen(cfg goldGenConfig) *goldGen {
	if cfg.EventSpan == 0 {
		cfg.EventSpan = 3 * 3600 * 1000
	}
	if cfg.SignalRate == 0 {
		cfg.SignalRate = 1
	}
	g := &goldGen{cfg: cfg, rng: newGoldRand(cfg.Seed)}
	g.tk = make([]goldTicker, cfg.Rows)
	g.rows = make([]core.Row, cfg.Rows)
	g.order = make([]int, cfg.Rows)
	for i := range g.tk {
		g.initTicker(i)
		g.order[i] = i
	}
	for i := len(g.order) - 1; i > 0; i-- {
		j := g.rng.intn(i + 1)
		g.order[i], g.order[j] = g.order[j], g.order[i]
	}
	g.seed(cfg.Start)
	return g
}

func (g *goldGen) initTicker(i int) {
	r := g.rng
	t := &g.tk[i]
	t.sym = goldSym(i)
	u := r.f()
	switch {
	case u < 0.52:
		t.prof = gpNormal
	case u < 0.64:
		t.prof = gpPenny
	case u < 0.72:
		t.prof = gpETF
	case u < 0.85:
		t.prof = gpThin
	case u < 0.90:
		t.prof = gpADR
	case u < 0.96:
		t.prof = gpHot
	case u < 0.975:
		t.prof = gpGapper
	default:
		t.prof = gpNormal // a new listing: no previous close
	}
	t.secType = "CS"
	switch t.prof {
	case gpNormal:
		t.prevClose = goldPx(r.cube(2, 300))
		t.sigma, t.pAct, t.pSig = r.between(0.05, 0.35), r.between(0.15, 0.6), 0.004
		t.avg = r.cube(8e4, 8e6)
	case gpPenny:
		t.prevClose = goldPx(r.between(0.15, 3.5))
		t.sigma, t.pAct, t.pSig = r.between(0.4, 2.0), r.between(0.2, 0.6), 0.01
		t.avg = r.cube(1e5, 4e6)
	case gpETF:
		t.prevClose = goldPx(r.between(15, 450))
		t.sigma, t.pAct, t.pSig = r.between(0.03, 0.12), r.between(0.3, 0.7), 0.001
		t.avg = r.cube(3e5, 3e7)
		t.secType = "ETF"
	case gpThin:
		t.prevClose = goldPx(r.cube(0.5, 60))
		t.sigma, t.pAct, t.pSig = r.between(0.5, 1.5), r.between(0.03, 0.12), 0
		t.avg = r.cube(3e3, 8e4)
		t.grain, t.lot = true, true
	case gpADR:
		t.prevClose = goldPx(r.cube(3, 120))
		t.sigma, t.pAct, t.pSig = r.between(0.1, 0.4), r.between(0.1, 0.4), 0.003
		t.avg = r.cube(5e4, 2e6)
		t.secType = "ADRC"
	case gpHot:
		t.prevClose = goldPx(r.cube(0.8, 40))
		t.sigma, t.pAct, t.pSig = r.between(0.8, 3), r.between(0.6, 0.95), 0.04
		t.drift = r.between(-0.15, 0.2)
		t.avg = r.cube(2e5, 6e6)
	case gpGapper:
		t.prevClose = goldPx(r.between(6, 45))
		t.sigma, t.pAct, t.pSig = r.between(0.5, 1.5), r.between(0.7, 0.95), 0.03
		t.drift = 0.06
		t.avg = r.cube(2e5, 3e6)
	}
	if r.chance(0.025) {
		t.prevClose = 0
	}
	if t.prof != gpThin && r.chance(0.3) {
		t.grain = true
	}
	if r.chance(0.01) {
		t.secType = ""
	}
	t.volBase = max(t.avg/1500, 1)
	t.hasBars = r.chance(0.12)
	t.flick = g.cfg.Flicker > 0 && r.chance(g.cfg.Flicker)
	t.baseline = r.chance(0.72)
	t.adv10 = core.Null
	if r.chance(0.8) {
		t.adv10 = math.Round(t.avg * r.between(0.7, 1.5))
	}
	if r.chance(0.6) {
		t.prevAh = goldPx(max(t.prevClose, 0.5) * r.between(1, 1.04))
	}
	// reference counts: nil = unknown
	if t.prof != gpETF && r.chance(0.85) {
		v := int64(r.cube(1e6, 6e8))
		t.float = &v
	}
	if r.chance(0.9) {
		v := int64(r.cube(2e6, 9e8))
		t.shs = &v
	}
	if r.chance(0.08) {
		v := int64(math.Round(r.cube(100, 3e6)/100) * 100)
		t.short = &v
	}
	t.last = t.prevClose
	if t.last == 0 {
		t.last = goldPx(r.cube(1, 80))
	}
	t.prints = int(r.cube(0, 40))
}

// baseRow is a fresh row for ticker i: the engine's all-Null shape plus the
// ticker's reference data.
func (g *goldGen) baseRow(i int) core.Row {
	t := &g.tk[i]
	r := goldRowTemplate
	r.Sym, r.SecType, r.Float, r.ShsOut, r.ShortAvail = t.sym, t.secType, t.float, t.shs, t.short
	if t.short != nil {
		r.ShortFee = 0.5 + float64(i%7)
	}
	return r
}

// seed puts the whole market in the state of a half-way-through day at `at`.
func (g *goldGen) seed(at int64) {
	r := g.rng
	g.sess = goldSession(at)
	g.frac = goldFrac(at, g.sess)
	for i := range g.tk {
		t := &g.tk[i]
		g.rows[i] = g.baseRow(i)
		row := &g.rows[i]
		if t.prof == gpGapper && g.sess != core.SessionClosed {
			t.last = goldPx(t.prevClose * r.between(1.11, 1.45))
		} else {
			t.last = goldPx(max(t.last*(1+r.norm()*t.sigma*4/100), 0.01))
		}
		volScale := r.between(0.3, 2.5)
		if t.prof == gpGapper {
			volScale = r.between(0.7, 3)
		}
		t.vol = int64(t.avg * g.frac * volScale)
		if t.lot {
			t.vol = t.vol / 100 * 100
			if r.chance(0.3) {
				t.vol = 0
			}
		}
		span := max(t.last*r.between(0.002, 0.03), 0.0001)
		t.hodSet = t.vol > 0
		t.hod, t.lod = goldPx(t.last+span*r.f()), goldPx(max(t.last-span*r.f(), 0.0001))
		vw := goldPx(t.last * (1 + r.norm()*0.005))
		t.vwapNum, t.vwapDen = vw*float64(t.vol), float64(t.vol)
		t.c1, t.c5, t.c15 = r.norm()*t.sigma, r.norm()*t.sigma*2, r.norm()*t.sigma*3
		t.ema = goldPx(t.last * (1 + r.norm()*0.002))
		t.p5lo = goldPx(t.last * (1 - r.between(0.001, 0.02)))
		t.p5cl = goldPx(t.last * (1 + (r.f()-0.5)*0.01))
		t.c1mChg = goldRound(r.norm()*t.sigma, 2)
		t.c1mRvol = goldNaNOr(r.chance(0.85), goldRound(r.cube(0.2, 14), 2))
		if g.sess != core.SessionPre && t.prevClose > 0 {
			t.dayOpen = goldPx(t.prevClose * (1 + r.norm()*0.01))
		}
		// session scope: what has accrued since the last boundary
		switch g.sess {
		case core.SessionPre:
			t.scopeVol = t.vol
		case core.SessionRegular:
			t.preVol = int64(float64(t.vol) * r.between(0.02, 0.2))
			t.scopeVol = t.vol - t.preVol
			t.preDone = true
		default:
			t.preVol = int64(float64(t.vol) * r.between(0.02, 0.2))
			t.scopeVol = int64(float64(t.vol) * r.between(0.005, 0.05))
			t.preDone = true
		}
		if t.scopeVol > 0 {
			t.scopeSet = true
			t.scopeOpen = goldPx(t.last * (1 + r.norm()*0.004))
			t.scopeNum, t.scopeDen = vw*float64(t.scopeVol), float64(t.scopeVol)
			row.SessHigh, row.SessLow = t.hod, t.lod
		}
		if t.preDone {
			t.pmh = goldPx(t.last * (1 + r.between(-0.02, 0.04)))
			g.freeze(i, true)
		}
		if g.sess == core.SessionPost || g.sess == core.SessionClosed {
			g.freeze(i, false)
		}
		if t.hasBars {
			row.Bars = g.makeBars(t, at)
		}
		pInit := map[goldProf]float64{gpNormal: 0.02, gpPenny: 0.06, gpETF: 0.005, gpThin: 0.01, gpADR: 0.02, gpHot: 0.25, gpGapper: 0.15}[t.prof] * g.cfg.SignalRate
		for grp := 0; grp < gsN; grp++ {
			if r.chance(pInit) {
				g.fire(i, grp, at-int64(r.cube(0, float64(g.cfg.EventSpan))))
			}
		}
		g.recompute(i)
	}
	g.stampAges(at)
}

// makeBars is a fresh 24-bar series ending at the ticker's last price.
func (g *goldGen) makeBars(t *goldTicker, asOf int64) []core.Bar {
	const n = 24
	bars := make([]core.Bar, n)
	m := asOf / 60000
	px := t.last
	for k := n - 1; k >= 0; k-- {
		o := goldPx(px * (1 + (g.rng.f()-0.5)*0.01))
		bars[k] = core.Bar{
			T: (m - int64(n-1-k)) * 60000, O: o, C: px,
			H: goldPx(max(o, px) * (1 + g.rng.f()*0.004)),
			L: goldPx(min(o, px) * (1 - g.rng.f()*0.004)),
			V: int64(g.rng.cube(0, t.volBase*8)),
		}
		px = o
	}
	return bars
}

// freeze captures the outgoing sub-session into the Pre* (pre=true) or Rth*
// (pre=false) slots, the way the engine does at 09:30 and 16:00.
func (g *goldGen) freeze(i int, pre bool) {
	t, r := &g.tk[i], &g.rows[i]
	var chg, hi, lo, vwap, gap, fade float64 = core.Null, core.Null, core.Null, core.Null, core.Null, core.Null
	if t.scopeSet {
		hi, lo = goldPx(t.hod), goldPx(t.lod)
		if t.scopeDen > 0 {
			vwap = t.scopeNum / t.scopeDen
		}
		if t.prevClose > 0 {
			chg = goldPct(t.last, t.prevClose)
			gap = goldPct(hi, t.prevClose)
		}
		fade = max((hi-t.last)/hi*100, 0)
	}
	if pre {
		r.PreChg, r.PreHigh, r.PreLow, r.PreVwap, r.PreGap, r.PreFade, r.PreVol = chg, hi, lo, vwap, gap, fade, t.preVol
		if !t.scopeSet && t.preVol > 0 {
			r.PreHigh, r.PreLow = goldPx(t.last*1.01), goldPx(t.last*0.99)
		}
		return
	}
	r.RthChg, r.RthHigh, r.RthLow, r.RthVwap, r.RthGap, r.RthFade = chg, hi, lo, vwap, gap, fade
	r.RthVol = int64(float64(t.vol) * 0.8)
}

// newDay is the 04:00 ET reset: yesterday's close becomes the previous close,
// every day counter, signal and frozen slot clears.
func (g *goldGen) newDay(asOf int64) {
	for i := range g.tk {
		t := &g.tk[i]
		if t.last > 0 {
			t.prevClose = t.last
		}
		t.dayOpen, t.hod, t.lod, t.hodSet = 0, 0, 0, false
		t.vol, t.scopeVol, t.vwapNum, t.vwapDen = 0, 0, 0, 0
		t.preDone, t.preVol, t.pmh = false, 0, 0
		t.ev = [gsN]int64{}
		t.noDbAge, t.noPdlAge, t.bbsBreak = false, false, 0
		t.prevAh = 0
		if g.rng.chance(0.6) {
			t.prevAh = goldPx(t.last * g.rng.between(1, 1.04))
		}
		g.rows[i] = g.baseRow(i)
	}
}

// changeSession handles a sub-session boundary at asOf.
func (g *goldGen) changeSession(old, sess core.Session, asOf int64) {
	if old == core.SessionClosed && sess == core.SessionPre {
		g.newDay(asOf)
	}
	for i := range g.tk {
		t := &g.tk[i]
		switch sess {
		case core.SessionRegular:
			t.preDone, t.preVol, t.pmh = true, t.scopeVol, 0
			if t.scopeSet {
				t.pmh = t.hod
			}
			g.freeze(i, true)
			if t.last > 0 {
				t.dayOpen = t.last
			}
		case core.SessionPost:
			g.freeze(i, false)
		}
		t.scopeVol, t.scopeNum, t.scopeDen, t.scopeSet = 0, 0, 0, false
		r := &g.rows[i]
		r.SessHigh, r.SessLow, r.SessVwap, r.ChgSess = core.Null, core.Null, core.Null, core.Null
		r.SessVol = 0
	}
}

// Next advances the market to asOf and returns the snapshot, a fresh Rows slice
// and Index every time (the scanner mutates the rows it is handed).
func (g *goldGen) Next(asOf int64) *core.MetricSnapshot {
	sess := goldSession(asOf)
	if sess != g.sess {
		g.changeSession(g.sess, sess, asOf)
		g.sess = sess
		g.frac = goldFrac(asOf, sess)
		for i := range g.tk {
			g.recompute(i)
		}
	}
	g.frac = goldFrac(asOf, sess)
	for i := range g.tk {
		g.tick(i, asOf)
	}
	g.stampAges(asOf)
	if !g.cfg.NoReorder {
		n := len(g.order)
		for k := 0; k < n/20; k++ {
			a, b := g.rng.intn(n), g.rng.intn(n)
			g.order[a], g.order[b] = g.order[b], g.order[a]
		}
	}
	rows := make([]core.Row, 0, len(g.order))
	idx := make(map[string]int, len(g.order))
	for _, i := range g.order {
		if g.tk[i].flick && g.rng.chance(0.35) {
			continue
		}
		idx[g.rows[i].Sym] = len(rows)
		rows = append(rows, g.rows[i])
	}
	g.gen++
	return &core.MetricSnapshot{Gen: g.gen, AsOf: asOf, Sess: sess, Rows: rows, Index: idx}
}

// tick lets ticker i print (or not) in this step.
func (g *goldGen) tick(i int, asOf int64) {
	t, r := &g.tk[i], &g.rows[i]
	r.Sess = g.sess
	if t.haltLeft > 0 {
		t.haltLeft--
		r.Halted = t.haltLeft > 0
		return
	}
	if g.sess == core.SessionClosed || !g.rng.chance(t.pAct*g.cfg.Active) {
		return
	}
	if g.rng.chance(0.0006) {
		t.haltLeft, r.Halted = 3+g.rng.intn(25), true
		return
	}
	ret := t.sigma*g.rng.norm() + t.drift
	px := goldPx(t.last * (1 + ret/100))
	if px <= 0 {
		px = t.last
	}
	ret = goldPct(px, t.last)
	t.last = px

	dv := int64(t.volBase * (0.1 + 5*g.rng.cube(0, 1)))
	if t.lot {
		dv = max(dv/100*100, 100)
	}
	dv = max(dv, 1)
	t.vol += dv
	t.scopeVol += dv
	f := float64(dv)
	t.vwapNum, t.vwapDen = t.vwapNum+px*f, t.vwapDen+f
	t.scopeNum, t.scopeDen = t.scopeNum+px*f, t.scopeDen+f
	if !t.hodSet {
		t.hod, t.lod, t.hodSet = px, px, true
	} else {
		t.hod, t.lod = max(t.hod, px), min(t.lod, px)
	}
	if !t.scopeSet {
		t.scopeSet, t.scopeOpen = true, px
		r.SessHigh, r.SessLow = px, px
	} else {
		r.SessHigh, r.SessLow = max(r.SessHigh, px), min(r.SessLow, px)
	}
	t.c1, t.c5, t.c15 = t.c1*0.55+ret, t.c5*0.88+ret, t.c15*0.95+ret
	t.ema += 0.2 * (px - t.ema)
	t.prints++
	if t.prints >= 8 && g.rng.chance(0.15) {
		t.p5lo = goldPx(px * (1 - g.rng.between(0.001, 0.02)))
		t.p5cl = goldPx(px * (1 + (g.rng.f()-0.5)*0.01))
	}
	if g.rng.chance(0.85) {
		t.c1mChg = goldRound(ret*g.rng.between(0.5, 2.5), 2)
		t.c1mRvol = goldNaNOr(g.rng.chance(0.85), goldRound(g.rng.cube(0.2, 14), 2))
	}
	if g.rng.chance(t.pSig * g.cfg.SignalRate) {
		u := g.rng.f()
		grp := gsPdl
		for k, edge := range [...]float64{0.2, 0.35, 0.5, 0.65, 0.75, 0.82, 0.88, 0.94} {
			if u < edge {
				grp = k
				break
			}
		}
		g.fire(i, grp, asOf-int64(g.rng.intn(60000)))
	}
	if g.rng.chance(t.pSig * 2.5 * g.cfg.SignalRate) {
		g.fire(i, gsBrk5mLo+g.rng.intn(gsN-gsBrk5mLo), asOf-int64(g.rng.intn(30000)))
	}
	if t.hasBars && g.rng.chance(0.3) {
		r.Bars = g.makeBars(t, asOf)
	}
	g.recompute(i)
}

// recompute rebuilds every price/volume-derived field of row i from the
// ticker's state (the signal ages are stamped separately, per step).
func (g *goldGen) recompute(i int) {
	t, r := &g.tk[i], &g.rows[i]
	q := func(x float64) float64 {
		if t.grain {
			return goldRound(x, 2)
		}
		return x
	}
	r.Last = t.last
	r.Vol = t.vol
	r.Vwap, r.VsVwap = 0, core.Null
	if t.vwapDen > 0 {
		r.Vwap = t.vwapNum / t.vwapDen
		r.VsVwap = goldPct(t.last, r.Vwap)
	}
	r.Hod, r.Lod, r.VsHod = t.hod, t.lod, core.Null
	if t.hodSet && t.hod != 0 {
		r.VsHod = goldPct(t.last, t.hod)
	}
	r.Chg1m, r.Chg5m, r.Chg15m = core.Null, core.Null, core.Null
	if t.prints >= 3 {
		r.Chg1m = q(t.c1)
	}
	if t.prints >= 8 {
		r.Chg5m = q(t.c5)
	}
	if t.prints >= 15 {
		r.Chg15m = q(t.c15)
	}
	r.ChgDay, r.Gap = core.Null, core.Null
	if t.prevClose != 0 {
		r.ChgDay = q(goldPct(t.last, t.prevClose))
		if t.dayOpen != 0 {
			r.Gap = goldPct(t.dayOpen, t.prevClose)
		}
	}
	r.Rvol, r.AvgVol = core.Null, core.Null
	if t.baseline {
		r.Rvol = float64(t.vol) / (t.avg * g.frac)
		r.AvgVol = math.Round(t.avg)
	}
	r.SessVol = t.scopeVol
	if t.scopeSet {
		base := t.scopeOpen
		if g.sess == core.SessionRegular && t.dayOpen != 0 {
			base = t.dayOpen
		}
		r.ChgSess = goldPct(t.last, base)
	}
	if t.scopeDen > 0 {
		r.SessVwap = t.scopeNum / t.scopeDen
	}
	r.Rot = core.Null
	if t.float != nil && *t.float > 0 {
		r.Rot = float64(t.vol) / float64(*t.float)
	}
	r.Adv10, r.PmVolX, r.PreVolPctAdv = t.adv10, core.Null, core.Null
	pre, preOK := t.preVol, t.preDone
	if g.sess == core.SessionPre {
		pre, preOK = t.scopeVol, true
	}
	if preOK {
		if t.adv10 > 0 {
			r.PmVolX = float64(pre) / t.adv10
		}
		if t.baseline {
			r.PreVolPctAdv = float64(pre) / t.avg * 100
		}
	}
	r.Ema9, r.VsEma9 = core.Null, core.Null
	if t.prints >= 12 {
		r.Ema9 = t.ema
		r.VsEma9 = (t.last/t.ema - 1) * 100
	}
	r.Prev5mLow, r.VsPrev5mLow, r.Prev5mClose, r.VsPrev5mClose = core.Null, core.Null, core.Null, core.Null
	if t.prints >= 8 {
		r.Prev5mLow, r.VsPrev5mLow = t.p5lo, goldPct(t.last, t.p5lo)
		r.Prev5mClose, r.VsPrev5mClose = t.p5cl, goldPct(t.last, t.p5cl)
	}
	r.C1mChg, r.C1mRvol = t.c1mChg, t.c1mRvol
	r.PrevAhHigh, r.VsPrevAh = core.Null, core.Null
	if t.prevAh > 0 {
		r.PrevAhHigh, r.VsPrevAh = t.prevAh, goldPct(t.last, t.prevAh)
	}
	r.VsPmh = core.Null
	if t.pmh > 0 && g.sess != core.SessionPre {
		r.VsPmh = goldPct(t.last, t.pmh)
	}
	if !core.IsNull(r.DbLevel) {
		r.DbPct = goldPct(t.last, r.DbLevel)
	}
	if !core.IsNull(r.PdlLevel) {
		r.PdlPct = goldPct(r.PdlLevel, t.last)
	}
	if !core.IsNull(r.BbsSupport) {
		r.BbsDistPct = goldPct(t.last, r.BbsSupport)
	}
}

// fire starts a signal event of group grp on ticker i at epoch ms `ms`, drawing
// the measures that are fixed for the bar.
func (g *goldGen) fire(i, grp int, ms int64) {
	t, r, rng := &g.tk[i], &g.rows[i], g.rng
	t.ev[grp] = ms
	if !t.listed {
		t.listed = true
		g.sigIdx = append(g.sigIdx, i)
	}
	px := t.last
	switch grp {
	case gsDildo:
		r.DildoBody = goldRound(rng.between(1.5, 15), 2)
		r.DildoVolX = goldNaNOr(!rng.chance(0.05), goldRound(rng.cube(1.2, 60), 1))
		r.DildoDvol = math.Round(rng.cube(2e4, 3e6))
		r.DildoEarly = [...]float64{0, 1, core.Null}[rng.intn(3)]
	case gsClimax:
		r.ClimaxDropPct = goldRound(rng.between(3, 30), 2)
		r.ClimaxVolX = goldNaNOr(!rng.chance(0.03), goldRound(rng.cube(2, 40), 1))
		r.ClimaxWickPct = goldRound(rng.between(5, 90), 1)
		r.ClimaxDvol = math.Round(rng.cube(1e4, 4e6))
		r.ClimaxLow = goldPx(px * (1 - rng.between(0.005, 0.08)))
		t.fate = rng.intn(3)
		t.confAfter = int64(rng.between(60, 400)) * 1000
	case gsBbs:
		r.BbsSupport = goldPx(px * (1 - rng.between(0.003, 0.02)))
		r.BbsDvol = math.Round(rng.cube(1e4, 2e6))
		t.bbsBreak = 0
		if rng.chance(0.5) {
			t.bbsBreak = ms + int64(rng.between(60, 600))*1000
		}
	case gsPc:
		r.PcBodyPct = goldRound(rng.between(1, 12), 2)
		r.PcTopPct = goldRound(rng.between(0.5, 40), 1)
		r.PcBodyX = goldNaNOr(!rng.chance(0.1), goldRound(rng.cube(1.5, 20), 1))
		r.PcVolX = goldNaNOr(!rng.chance(0.1), goldRound(rng.cube(1.5, 40), 1))
		r.PcDvol = math.Round(rng.cube(1e4, 3e6))
	case gsIgn:
		r.IgnBodyPct = goldRound(rng.between(0.2, 3), 2)
		r.IgnVolX = goldRound(rng.cube(1.5, 30), 1)
		r.IgnFresh = [...]float64{0, 1, core.Null}[rng.intn(3)]
		r.IgnPass = float64(rng.intn(2))
	case gsEvBull, gsEvBear:
		vw := px
		if !core.IsNull(r.SessVwap) {
			vw = r.SessVwap
		}
		r.EvVwap = vw
		r.EvEma = goldPx(vw * (1 + rng.between(-0.01, 0.01)))
		r.EvDist = goldPct(r.EvEma, r.EvVwap)
	case gsDb:
		r.DbLevel = goldPx(px * (1 - rng.between(0.005, 0.12)))
		r.DbType = float64(1 + rng.intn(3))
		t.noDbAge = rng.chance(0.2)
	case gsPdl:
		r.PdlLevel = goldPx(px * (1 + rng.between(0.005, 0.1)))
		t.noPdlAge = rng.chance(0.2)
	}
}

// stampAges writes every signal's age (whole seconds from the event to the
// snapshot clock) and runs the climax / bouncy-ball state machines. Rows with
// no event today keep the template's Nulls.
func (g *goldGen) stampAges(asOf int64) {
	age := func(ms int64) float64 {
		if ms <= 0 {
			return core.Null
		}
		return float64(max(asOf-ms, 0) / 1000)
	}
	for _, i := range g.sigIdx {
		t, r := &g.tk[i], &g.rows[i]
		r.DildoAge = age(t.ev[gsDildo])
		r.PcAge = age(t.ev[gsPc])
		r.IgnAge = age(t.ev[gsIgn])
		r.EvBullAge, r.EvBearAge = age(t.ev[gsEvBull]), age(t.ev[gsEvBear])
		r.DbAge, r.PdlAge = core.Null, core.Null
		if !t.noDbAge {
			r.DbAge = age(t.ev[gsDb])
		}
		if !t.noPdlAge {
			r.PdlAge = age(t.ev[gsPdl])
		}
		r.Brk5mAge, r.Brk5mCloseAge, r.Brk5mCloseUpAge = age(t.ev[gsBrk5mLo]), age(t.ev[gsBrk5mCl]), age(t.ev[gsBrk5mClUp])
		r.BrkPmhAge, r.BrkPrevAhAge = age(t.ev[gsBrkPmh]), age(t.ev[gsBrkAh])
		r.BrkPmh5mAge, r.BrkPrevAh5mAge, r.BrkEma9Age = age(t.ev[gsBrkPmh5]), age(t.ev[gsBrkAh5]), age(t.ev[gsBrkEma])
		if ms := t.ev[gsClimax]; ms > 0 {
			r.ClimaxAge = age(ms)
			since := asOf - ms
			r.ClimaxState, r.ClimaxConfAge, r.ClimaxConfPx = 1, core.Null, core.Null
			switch {
			case since >= 1800_000:
				r.ClimaxState = 4
			case t.fate == 1 && since >= t.confAfter:
				r.ClimaxState = 2
				r.ClimaxConfAge = age(ms + t.confAfter)
				r.ClimaxConfPx = goldPx(r.ClimaxLow * 1.004)
			case t.fate == 2 && since >= t.confAfter:
				r.ClimaxState = 3
			}
		}
		if ms := t.ev[gsBbs]; ms > 0 {
			if t.bbsBreak > 0 && asOf >= t.bbsBreak {
				r.BbsState, r.BbsAge, r.BbsDistPct = 2, age(t.bbsBreak), core.Null
			} else {
				r.BbsState, r.BbsAge = 1, core.Null
				r.BbsDistPct = goldPct(t.last, r.BbsSupport)
			}
		}
	}
}
