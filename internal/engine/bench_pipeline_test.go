package engine

// End-to-end pipeline benchmark: one 100 ms scanner tick, from per-second
// aggregates to the scan.tick / alert / entry messages the hub would encode.
//
//	go test ./internal/engine -run XXX -bench PipelineTick -benchmem -count 6
//
// What one iteration does (the same work the live process does, minus the
// socket-facing hub and the feed):
//
//  1. the shards, each on its own goroutine like shard.run, fold their share of
//     the aggregates into the ticker state (onAgg), run the 1 Hz forward-fill
//     clock when the second rolled over (onTick), and build their rows for the
//     snapshot request (snapshot: recordCurve + toRow + signalRow);
//  2. the benchmark goroutine plays the merger: it appends the shards' rows and
//     assembles the snapshot exactly like runMerger (assembleSnapshot);
//  3. the snapshot goes to a REAL scan.Scanner whose Run goroutine evaluates the
//     32 live scans (internal/scan/testdata/live_scan_defs.json): overlays, every
//     scan's predicate, sort, limit, diff, alerts;
//  4. the Ticks / Alerts / Entries channels are drained in the loop.
//
// Sub-benchmarks:
//
//	active         one second boundary: clock tick + ~2,500 aggregates + snapshot + scan
//	idle           the same snapshot with no aggregates and no clock change (the quiet
//	               slices; the live process burns about a core on these when idle)
//	active_engine  active without the scanner (shards + merge only)
//	idle_engine    idle without the scanner
//
// The scanner's cost is active minus active_engine (idle minus idle_engine). A
// real second is one active slice and nine idle ones, not ten active: the A.*
// feed delivers a second's aggregates together, at the second boundary.
//
// Reported beyond ns/op and allocs: cpu-ms/op is the whole process's user+kernel
// CPU per iteration (the number that matters for "cores burned": the shards run in
// parallel, so ns/op alone understates it), and ticks/op, tickrows/op, alerts/op,
// entries/op are what the scanner emitted - a code change that keeps them equal
// did the same work.
//
// The scanner is driven through its public surface: Scanner.process is not
// exported, so the snapshot is handed to Scanner.Run over the engine's snapshot
// channel (made unbuffered here, so the send returns only once Run has the
// snapshot) and Scanner.ScanUsesFlags - a ctrl-channel round trip that is a map
// miss for an unknown id - serves as the barrier: Run handles ctrl ops only
// between snapshots, so when it answers, process has finished. A direct
// synchronous hook would remove those two goroutine hops (a few microseconds per
// tick); it would have to be a non-test exported method on Scanner.
//
// The workload is synthetic but shaped like a US morning (10:00 ET, regular
// session): 11,000 tickers with a fat-tailed day change, log-skewed prices and
// volumes, an RVOL baseline for 90% of them, floats for 85%, daily levels / PDL /
// previous after-hours highs, a handful of news, daily-trend, flag, 9EMA and
// shortable overlays, and random 60-second bursts that make the closed-bar
// judges (dildo, climax, ...) fire. Everything derives from per-ticker splitmix64
// streams and + - * / sqrt only, so a run is bit-reproducible and independent of
// the number of shards (see TestPipelineDigest).
//
// Identifiers here carry the pipe prefix: other test files share this package.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"hash"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dlevel"
	"github.com/eelkonkoopman/marketscanner/internal/scan"
)

var pipeUpdate = flag.Bool("pipe-update", false, "rewrite testdata/pipe_digest.txt from the current pipeline")

// ─── configuration ───────────────────────────────────────────────────────────

type pipeCfg struct {
	tickers    int // distinct symbols in the engine
	warmSecs   int // simulated seconds of bulk warm-up (aggregates + 1 Hz clock, no snapshots)
	aggsPerSec int // expected aggregates in a second (the burst slice's size)
	settle     int // full pipeline ticks (scanner included) run before anything is measured
}

var (
	// pipeFullCfg is the live-sized fixture: the whole US market, ten simulated
	// minutes of activity, ~2,500 aggregates in a burst slice.
	pipeFullCfg = pipeCfg{tickers: 11000, warmSecs: 600, aggsPerSec: 2500, settle: 20}
	// pipeSmallCfg is the same shape at a size a unit test can afford.
	pipeSmallCfg = pipeCfg{tickers: 1200, warmSecs: 120, aggsPerSec: 270, settle: 6}
)

const (
	pipeDefsFile  = "../scan/testdata/live_scan_defs.json"
	pipeBarrierID = "\x00pipe-barrier" // a scan id that never exists: ScanUsesFlags answers false at once
	pipeSnapOffMs = 50                 // a snapshot is taken this long after its second boundary
)

var pipeET = func() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return loc
}()

// pipeStartMs is where the simulated clock starts and where the engine "begins
// listening": 09:50:00 ET on a Wednesday of the EDT period, regular session.
var pipeStartMs = time.Date(2026, 10, 21, 9, 50, 0, 0, pipeET).UnixMilli()

// ─── deterministic generator ─────────────────────────────────────────────────

// pipeRand is a splitmix64 stream.
type pipeRand struct{ s uint64 }

func (r *pipeRand) next() uint64 {
	r.s += 0x9e3779b97f4a7c15
	z := r.s
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// f is uniform in [0,1).
func (r *pipeRand) f() float64 { return float64(r.next()>>11) / (1 << 53) }

// norm is an approximately standard normal (Irwin-Hall of four uniforms): no
// log/cos, so the stream is the same on every machine.
func (r *pipeRand) norm() float64 {
	return (r.f() + r.f() + r.f() + r.f() - 2) * 1.7320508075688772
}

func (r *pipeRand) sign() float64 {
	if r.next()&1 == 0 {
		return -1
	}
	return 1
}

// pipePx rounds a price to the tick the market uses: cents from $1, 1/100 cent below.
func pipePx(p float64) float64 {
	if p >= 1 {
		return math.Round(p*100) / 100
	}
	return math.Round(p*10000) / 10000
}

// pipeSym is a unique 4-letter symbol per index (7919 is coprime to 26^4).
func pipeSym(i int) string {
	n := (uint64(i)*7919 + 13) % (26 * 26 * 26 * 26)
	var b [4]byte
	for k := 3; k >= 0; k-- {
		b[k] = byte('A' + n%26)
		n /= 26
	}
	return string(b[:])
}

// pipeShape[b] is the share of a day's volume traded by the end of 5-minute
// bucket b (04:00-20:00 ET): a thin pre-market ramp, a decaying open surge, a
// quiet middle with a closing lift, a thin after-hours tail.
var pipeShape = func() (cum [core.BucketCount]float64) {
	var w [core.BucketCount]float64
	for b := range w {
		switch {
		case b < 66:
			w[b] = 0.05 + 0.45*float64(b)/66
		case b < 144:
			x := float64(b - 66)
			w[b] = 0.55 + 2.2/(1+x/2.5) + 0.7*(x/78)*(x/78)
		default:
			w[b] = 0.25 / (1 + float64(b-144)/6)
		}
	}
	var total float64
	for _, v := range w {
		total += v
	}
	var run float64
	for b, v := range w {
		run += v
		cum[b] = run / total
	}
	return cum
}()

// pipeProps is everything about one ticker that does not change while it runs.
type pipeProps struct {
	sym                           string
	prevClose                     float64
	open0, hi0, lo0, last0, vwap0 float64 // the REST snapshot the engine is seeded from at 09:50
	chg                           float64 // day change at the seed, %
	dayVol                        float64
	seedVol                       int64   // session volume at the seed
	sigma                         float64 // return volatility per sqrt(second)
	act                           float64 // relative chance to print in a given second
	aggVol                        float64 // mean shares per printing second

	adv10  float32
	levels []dlevel.Level
	pdl    float32
	prevAh float32

	hasBase   bool
	baseTotal float64
	baseCount uint16
	shsOut    int64
	floatSh   int64
	secType   string

	hasTrend bool
	trend    core.TrendRef
	hasShort bool
	short    core.ShortRef
}

func pipeMakeProps(i int) pipeProps {
	r := pipeRand{s: uint64(i)*0x2545f4914f6cdd1d + 0x1234567}
	r.next()
	r.next()
	p := pipeProps{sym: pipeSym(i)}

	u := r.f()
	u2 := u * u
	p.prevClose = pipePx(0.2 + 400*u2*u2)

	switch m := r.f(); {
	case m < 0.70:
		p.chg = 0.8 * r.norm()
	case m < 0.95:
		p.chg = 2.2 * r.norm()
	default:
		p.chg = (4 + 36*r.f()*r.f()) * r.sign()
	}
	p.last0 = pipePx(p.prevClose * (1 + p.chg/100))
	if p.last0 <= 0 {
		p.last0 = p.prevClose
	}
	p.open0 = pipePx(p.prevClose * (1 + p.chg*0.6/100))
	if p.open0 <= 0 {
		p.open0 = p.prevClose
	}
	p.hi0 = pipePx(math.Max(p.last0, p.open0) * (1 + 0.012*r.f()))
	p.lo0 = pipePx(math.Min(p.last0, p.open0) * (1 - 0.012*r.f()))
	p.vwap0 = pipePx((p.open0 + p.last0 + p.hi0 + p.lo0) / 4)

	liq := r.f()
	liq3 := liq * liq * liq
	absChg := math.Abs(p.chg)
	p.dayVol = (2000 + 20e6*liq3*liq3) * (1 + absChg/3)
	p.act = 0.03 + 5*liq3*liq3 + 0.25*absChg
	p.sigma = 0.0003 + 0.002*r.f()*r.f()
	if p.last0 < 5 {
		p.sigma *= 1.8
	}

	// Today's pace against the baseline: mostly near 1x, a tail of 5x+ names,
	// movers busier than the rest.
	v := r.f()
	v2 := v * v
	pace := (0.25 + 4*v2*v2*v2*v2) * (1 + absChg/5)
	p.seedVol = int64(p.dayVol * pipeShape[70] * pace)
	p.aggVol = 4e-5*p.dayVol*pace*(0.3+1.4*r.f()) + 1

	if r.f() < 0.9 {
		p.adv10 = float32(p.dayVol * (0.8 + 0.5*r.f()))
	}
	if r.f() < 0.6 {
		n := 1 + int(r.f()*3.99)
		step := 0.0
		for k := 0; k < n; k++ {
			step += 0.01 * (1 + r.f())
			p.levels = append(p.levels, dlevel.Level{Price: float32(pipePx(p.prevClose * (1 + step))), Kind: dlevel.Kind(1 + int(r.f()*2.99))})
		}
	}
	if r.f() < 0.6 {
		p.pdl = float32(pipePx(p.prevClose * (1 - 0.08*r.f())))
	}
	if r.f() < 0.3 {
		p.prevAh = float32(pipePx(p.prevClose * (0.98 + 0.06*r.f())))
	}

	if r.f() < 0.9 {
		p.hasBase = true
		p.baseTotal = p.dayVol * (0.7 + 0.8*r.f())
		p.baseCount = 20
		if r.f() < 0.08 {
			p.baseCount = 3 // too few sessions: RVOL stays null
		}
	}
	if r.f() < 0.85 {
		w := r.f()
		p.shsOut = int64(2e6 + 400e6*w*w*w)
		if r.f() < 0.6 {
			p.floatSh = int64(float64(p.shsOut) * 0.7)
		}
	}
	switch m := r.f(); {
	case m < 0.85:
		p.secType = "CS"
	case m < 0.95:
		p.secType = "ETF"
	default:
		p.secType = "ADRC"
	}
	if r.f() < 0.9 {
		p.hasTrend = true
		p.trend = core.TrendRef{DTrend: int(r.f()*2.99) - 1, Chg20d: (r.f() - 0.5) * 40, VsSMA: (r.f() - 0.5) * 10, Days: 20, AsOf: "2026-10-20"}
	}
	if r.f() < 0.65 {
		p.hasShort = true
		p.short = core.ShortRef{Ticker: p.sym, Available: int64(r.f() * 5e6), Capped: r.f() < 0.1, FeeRate: r.f() * 30, RebateRate: r.f() * 4, AsOf: 1_791_000_000}
	}
	return p
}

// pipeReference builds the engine's reference tables from the tickers' props.
func pipeReference(props []pipeProps) (*core.BaselineTable, map[string]core.FloatRef, map[string]string) {
	base := &core.BaselineTable{Cum: make(map[string][core.BucketCount]float64, len(props)), Count: make(map[string][core.BucketCount]uint16, len(props))}
	floats := make(map[string]core.FloatRef, len(props))
	types := make(map[string]string, len(props))
	for i := range props {
		p := &props[i]
		if p.hasBase {
			var cum [core.BucketCount]float64
			var cnt [core.BucketCount]uint16
			for b := range cum {
				cum[b] = p.baseTotal * pipeShape[b]
				cnt[b] = p.baseCount
			}
			base.Cum[p.sym], base.Count[p.sym] = cum, cnt
		}
		if p.shsOut > 0 {
			so := p.shsOut
			fr := core.FloatRef{Ticker: p.sym, SharesOut: &so, AsOf: 1_790_000_000, Source: "details_v3"}
			if p.floatSh > 0 {
				fl := p.floatSh
				fr.FloatShares = &fl
			}
			floats[p.sym] = fr
		}
		types[p.sym] = p.secType
	}
	return base, floats, types
}

// pipeScale finds k so that the expected number of printing tickers per second,
// the sum of min(1, act*k), is target.
func pipeScale(props []pipeProps, target int) float64 {
	lo, hi := 0.0, 1.0
	sum := func(k float64) (s float64) {
		for i := range props {
			s += math.Min(1, props[i].act*k)
		}
		return s
	}
	for sum(hi) < float64(target) && hi < 1e6 {
		hi *= 2
	}
	for it := 0; it < 60; it++ {
		mid := (lo + hi) / 2
		if sum(mid) < float64(target) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return hi
}

// ─── shard workers ───────────────────────────────────────────────────────────

// pipeSim is one ticker's running price process.
type pipeSim struct {
	p       *pipeProps
	rng     pipeRand
	prob    float64 // chance of printing in a given second
	px      float64
	lastSec int64
	accVol  int64
	vwNum   float64
	vwDen   float64
	burstTo int64 // second the current burst ends (0 = none)
	burstUp bool
}

// pipeBurstPerSec is the chance per second that a printing ticker starts a
// 60-second burst (a runner, or a flush and rebound): the closed-bar judges
// need some real signals to work on.
const pipeBurstPerSec = 0.004 / 60

// pipeWorker owns one shard the way shard.run does: every touch of the shard's
// state happens on its goroutine.
type pipeWorker struct {
	idx  int
	s    *shard
	sim  []pipeSim
	jobs chan pipeJob
}

type pipeJob struct {
	sec  int64 // epoch second of the 1 Hz clock / the aggregates
	warm int   // > 0: warm up this many seconds from sec (clock tick + aggregates each, no snapshot)
	tick bool  // run the 1 Hz clock for sec
	aggs bool  // apply this second's aggregates
	snap bool  // build the snapshot rows for req
	req  snapReq
	out  chan<- pipeRes
}

type pipeRes struct {
	idx  int
	rows []core.Row
}

func (w *pipeWorker) run() {
	for j := range w.jobs {
		if j.warm > 0 {
			for k := 0; k < j.warm; k++ {
				sec := j.sec + int64(k)
				w.s.onTick(sec)
				w.aggSecond(sec)
			}
			j.out <- pipeRes{idx: w.idx}
			continue
		}
		if j.tick {
			w.s.onTick(j.sec)
		}
		if j.aggs {
			w.aggSecond(j.sec)
		}
		var rows []core.Row
		if j.snap {
			rows = w.s.snapshot(j.req)
		}
		j.out <- pipeRes{idx: w.idx, rows: rows}
	}
}

// aggSecond feeds the aggregates of second sec: each ticker draws once from its
// own stream whether it printed.
func (w *pipeWorker) aggSecond(sec int64) {
	endMs := sec * 1000
	for i := range w.sim {
		sd := &w.sim[i]
		if sd.rng.f() >= sd.prob {
			continue
		}
		w.s.onAgg(sd.agg(sec, endMs))
	}
}

// agg draws the ticker's next per-second aggregate (its window ends at endMs).
func (sd *pipeSim) agg(sec, endMs int64) core.Agg {
	p, r := sd.p, &sd.rng
	dt := float64(min(max(sec-sd.lastSec, 1), 30))
	sd.lastSec = sec

	if sd.burstTo == 0 && r.f() < pipeBurstPerSec*dt {
		sd.burstTo = sec + 60
		sd.burstUp = r.f() < 0.7
	}
	ret := p.sigma * math.Sqrt(dt) * r.norm()
	volMul := 1.0
	if sd.burstTo != 0 {
		left := sd.burstTo - sec
		switch {
		case left <= 0:
			sd.burstTo = 0
		case sd.burstUp:
			ret += 0.0016 * dt
			volMul = 8
		case left > 15:
			ret -= 0.0022 * dt
			volMul = 8
		default:
			ret += 0.004 * dt // the rebound that leaves a lower wick
			volMul = 10
		}
	}

	o := sd.px
	c := math.Max(pipePx(o*(1+ret)), 0.01)
	spread := 0.5 * p.sigma * math.Sqrt(dt)
	hi := math.Max(pipePx(math.Max(o, c)*(1+spread*r.f())), math.Max(o, c))
	lo := math.Max(math.Min(pipePx(math.Min(o, c)*(1-spread*r.f())), math.Min(o, c)), 0.01)
	vol := int64(p.aggVol*(0.3+1.4*r.f())*volMul) + 1
	vwap := pipePx((o + c + hi + lo) / 4)

	sd.px = c
	sd.accVol += vol
	sd.vwNum += vwap * float64(vol)
	sd.vwDen += float64(vol)
	return core.Agg{
		Sym: p.sym, Open: o, High: hi, Low: lo, Close: c, VWAP: vwap, Vol: vol,
		AccVol:  p.seedVol + sd.accVol,
		DayOpen: p.open0,
		DayVWAP: (p.vwap0*float64(p.seedVol) + sd.vwNum) / (float64(p.seedVol) + sd.vwDen),
		StartMs: endMs - 1000, EndMs: endMs,
	}
}

// ─── the fixture ─────────────────────────────────────────────────────────────

type pipeFixture struct {
	cfg     pipeCfg
	eng     *Engine
	scn     *scan.Scanner
	workers []*pipeWorker
	res     chan pipeRes
	parts   [][]core.Row
	clock   atomic.Int64 // the simulated "now" (ms) the engine's Now option returns
	sec     int64        // epoch second the engine's 1 Hz clock last ticked to
	gen     uint64
	last    *core.MetricSnapshot
	cancel  context.CancelFunc
}

// pipeStats counts what the scanner emitted; with h set, every message is also
// folded into the hash in the order it was drained.
type pipeStats struct {
	ticks, tickRows, alerts, entries int64
	h                                hash.Hash
	msgs                             *[]string // when set, every message's JSON too (to find where two runs part)
}

func (st *pipeStats) fold(tag byte, v any) {
	if st.h == nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		b = []byte("ERR " + err.Error())
	}
	st.h.Write([]byte{tag})
	st.h.Write(b)
	st.h.Write([]byte{'\n'})
	if st.msgs != nil {
		*st.msgs = append(*st.msgs, string(tag)+string(b))
	}
}

func pipeLoadDefs(tb testing.TB) []core.ScanDef {
	tb.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(pipeDefsFile))
	if err != nil {
		tb.Fatalf("the live scan definitions: %v", err)
	}
	var defs []core.ScanDef
	if err := json.Unmarshal(raw, &defs); err != nil {
		tb.Fatalf("%s: %v", pipeDefsFile, err)
	}
	return defs
}

// pipeBuild makes an engine of cfg.tickers tickers warmed with cfg.warmSecs of
// simulated activity and a scanner holding the live scans, and settles them with
// cfg.settle full ticks. Nothing here is timed.
func pipeBuild(tb testing.TB, cfg pipeCfg) *pipeFixture {
	tb.Helper()
	f := &pipeFixture{cfg: cfg, res: make(chan pipeRes)}
	f.clock.Store(pipeStartMs)

	props := make([]pipeProps, cfg.tickers)
	for i := range props {
		props[i] = pipeMakeProps(i)
	}
	base, floats, types := pipeReference(props)

	// The engine exactly as cmd/scanner builds it: every judge on, signal log on.
	f.eng = New(Options{
		TickHz: 10, Baseline: base, Floats: floats,
		Now:   func() time.Time { return time.UnixMilli(f.clock.Load()) },
		Dildo: true, Climax: true, Bbs: true, Ignition: true, EmaVwap: true, SignalLog: true,
	})
	f.eng.SetSecTypes(types)
	f.eng.session.Store(int32(core.SessionRegular))
	f.eng.sig.streamFrom(pipeStartMs) // what ingest's seed does: whole minutes from here on
	// Unbuffered, so the hand-over to the scanner returns once Run holds the
	// snapshot (see the header); the live channel holds one.
	f.eng.out = make(chan *core.MetricSnapshot)

	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	hits, events := f.eng.DildoHits(), f.eng.SignalHits()
	go func() { // flagscan / flaglog: take the judges' hand-offs so they never fill
		for {
			select {
			case <-ctx.Done():
				return
			case <-hits:
			case <-events:
			}
		}
	}()

	// Tickers: created and seeded here (before the workers start), the way the
	// ingest layer seeds from the REST snapshot, then handed to their shard's worker.
	scale := pipeScale(props, cfg.aggsPerSec)
	f.workers = make([]*pipeWorker, len(f.eng.shards))
	for k, sh := range f.eng.shards {
		f.workers[k] = &pipeWorker{idx: k, s: sh, jobs: make(chan pipeJob)}
	}
	for i := range props {
		p := &props[i]
		w := f.workers[shardIdx(p.sym, f.eng.mask)]
		t := w.s.get(p.sym)
		t.seedApply(core.Seed{
			Agg: core.Agg{Sym: p.sym, Open: p.open0, High: p.hi0, Low: p.lo0, Close: p.last0, VWAP: p.vwap0,
				Vol: p.seedVol, AccVol: p.seedVol, DayOpen: p.open0, DayVWAP: p.vwap0,
				StartMs: pipeStartMs - 1000, EndMs: pipeStartMs},
			PrevClose: p.prevClose,
		})
		t.adv10 = p.adv10
		if len(p.levels) > 0 {
			t.dl.Set(p.levels)
		}
		if p.pdl > 0 {
			t.pdl.Set(p.pdl)
		}
		t.prevAhHi = p.prevAh
		w.sim = append(w.sim, pipeSim{p: p, rng: pipeRand{s: uint64(i)*0x9e3779b97f4a7c15 + 77}, prob: math.Min(1, p.act*scale), px: p.last0, lastSec: pipeStartMs / 1000})
	}
	for _, w := range f.workers {
		go w.run()
	}
	f.parts = make([][]core.Row, len(f.workers))

	// Ten simulated minutes (cfg.warmSecs), every shard on its own.
	f.sec = pipeStartMs / 1000
	for _, w := range f.workers {
		w.jobs <- pipeJob{sec: f.sec + 1, warm: cfg.warmSecs, out: f.res}
	}
	for range f.workers {
		<-f.res
	}
	f.sec += int64(cfg.warmSecs)
	f.clock.Store(f.sec*1000 + pipeSnapOffMs)

	// The scanner with the live definitions and the overlays its Set* hooks feed.
	f.scn = scan.New(f.eng)
	go func() { _ = f.scn.Run(ctx) }()
	f.scn.LoadAll(pipeLoadDefs(tb))
	pipeOverlays(f.scn, props, f.sec*1000)
	f.scn.ScanUsesFlags(pipeBarrierID) // every queued control op has run

	for i := 0; i < cfg.settle; i++ {
		f.step(true, true, nil)
	}
	f.step(false, true, nil)
	f.step(false, true, nil)
	runtime.GC()
	return f
}

// close stops the goroutines of a fixture a test built (the benchmark's lives as
// long as the process).
func (f *pipeFixture) close() {
	f.cancel()
	for _, w := range f.workers {
		close(w.jobs)
	}
}

// step runs one 100 ms tick. active: the clock moves to the next second boundary,
// the shards run the 1 Hz clock and apply that second's aggregates; otherwise the
// snapshot is of unchanged state at an unchanged clock. withScan hands the
// snapshot to the scanner and drains what it emitted into st (nil = discard).
func (f *pipeFixture) step(active, withScan bool, st *pipeStats) {
	if active {
		f.sec++
		f.clock.Store(f.sec*1000 + pipeSnapOffMs)
	}
	nowMs := f.sec*1000 + pipeSnapOffMs
	req := f.eng.newSnapReq(time.UnixMilli(nowMs), nil)
	for _, w := range f.workers {
		w.jobs <- pipeJob{sec: f.sec, tick: active, aggs: active, snap: true, req: req, out: f.res}
	}
	for range f.workers {
		r := <-f.res
		f.parts[r.idx] = r.rows
	}
	// The merger: rows in shard order (the live merger takes them as the shards
	// finish; a fixed order keeps the benchmark reproducible).
	rows := make([]core.Row, 0, f.eng.tracked.Load())
	for _, p := range f.parts {
		rows = append(rows, p...)
	}
	f.gen++
	snap := f.eng.assembleSnapshot(f.gen, nowMs, req.sess, rows)
	f.last = snap
	if !withScan {
		return
	}
	f.eng.out <- snap
	f.scn.ScanUsesFlags(pipeBarrierID)
	if st == nil {
		st = &pipeStats{}
	}
	f.drain(st)
}

// drain empties the scanner's three output channels into st. The scanner walks
// its scans in map order, so the order BETWEEN scans is random from one tick to
// the next (each scan's own messages stay in order); when hashing, a tick's
// messages are put in scan-id order first so the digest sees one order.
func (f *pipeFixture) drain(st *pipeStats) {
	var ticks []core.ScanTick
	var alerts []core.AlertMsg
	var entries []core.ScanEntry
	hashing := st.h != nil
	for done := false; !done; {
		select {
		case t := <-f.scn.Ticks():
			st.ticks++
			st.tickRows += int64(len(t.Rows))
			if hashing {
				ticks = append(ticks, t)
			}
		default:
			done = true
		}
	}
	for done := false; !done; {
		select {
		case a := <-f.scn.Alerts():
			st.alerts++
			if hashing {
				alerts = append(alerts, a)
			}
		default:
			done = true
		}
	}
	for done := false; !done; {
		select {
		case e := <-f.scn.Entries():
			st.entries++
			if hashing {
				entries = append(entries, e)
			}
		default:
			done = true
		}
	}
	if !hashing {
		return
	}
	slices.SortStableFunc(ticks, func(a, b core.ScanTick) int { return strings.Compare(a.ID, b.ID) })
	slices.SortStableFunc(alerts, func(a, b core.AlertMsg) int { return strings.Compare(a.ScanID, b.ScanID) })
	slices.SortStableFunc(entries, func(a, b core.ScanEntry) int { return strings.Compare(a.ScanID, b.ScanID) })
	for _, t := range ticks {
		st.fold('t', t)
	}
	for _, a := range alerts {
		st.fold('a', a)
	}
	for _, e := range entries {
		st.fold('e', e)
	}
}

// pipeOverlays hands the scanner the reference data it overlays onto rows: the
// nightly trend and flag sets, a few intraday-flag and 9EMA refs, the shortable
// file and some fresh news.
func pipeOverlays(s *scan.Scanner, props []pipeProps, nowMs int64) {
	r := pipeRand{s: 0xfeed}
	trend := make(map[string]core.TrendRef, len(props))
	short := make(map[string]core.ShortRef, len(props))
	dflags := map[string]core.FlagRef{}
	iflags := map[string]core.FlagRef{}
	ema := map[string]core.EmaFlagRef{}
	var news []core.NewsItem
	for i := range props {
		p := &props[i]
		if p.hasTrend {
			trend[p.sym] = p.trend
		}
		if p.hasShort {
			short[p.sym] = p.short
		}
		if r.f() < 0.025 {
			dflags[p.sym] = core.FlagRef{Score: 40 + 60*r.f(), State: float64(int(r.f() * 1.99)), PoleGain: 20 + 80*r.f(), RangePct: 2 + 6*r.f(),
				Retrace: 40 * r.f(), VolDry: 0.3 + 0.6*r.f(), Bars: 5 + int(r.f()*10), BaseHigh: p.last0 * (0.995 + 0.02*r.f()), AsOf: "2026-10-20"}
		}
		if r.f() < 0.006 {
			iflags[p.sym] = core.FlagRef{Score: 50 + 50*r.f(), State: float64(int(r.f() * 1.99)), PoleGain: 5 + 20*r.f(), RangePct: 1 + 3*r.f(),
				Retrace: 40 * r.f(), VolDry: 0.3 + 0.6*r.f(), Bars: 4 + int(r.f()*8), BaseHigh: p.last0 * (0.995 + 0.02*r.f()), AsOf: "2026-10-21 09:45"}
		}
		if r.f() < 0.004 {
			ref := core.EmaFlagRef{State: int(r.f() * 4.99), DistPct: r.norm(), Trigger: p.last0 * 1.01, Stop: p.last0 * 0.98, FlagBars: 3 + int(r.f()*10),
				SinceMs: nowMs - int64(r.f()*600_000), HotOnly: r.f() < 0.2}
			if r.f() < 0.3 {
				ref.SlamMs, ref.SlamPct = nowMs-int64(r.f()*900_000), -2-r.f()*4
			}
			if r.f() < 0.3 {
				ref.DildoMs, ref.DildoBody, ref.DildoVolX, ref.DildoDvol = nowMs-int64(r.f()*900_000), 8+r.f()*6, 5+r.f()*10, 2e5+r.f()*1e6
				ref.PbState = int(r.f() * 5.99)
			}
			ema[p.sym] = ref
		}
		if r.f() < 0.004+0.02*math.Min(p.act/5, 1) {
			news = append(news, core.NewsItem{ID: fmt.Sprintf("pipe-%d", i), Tickers: []string{p.sym}, Headline: "Pipeline headline " + p.sym,
				Source: "Benzinga", TS: nowMs - int64(r.f()*7_200_000)})
		}
	}
	s.SetDailyTrend(trend)
	s.SetShortable(short)
	s.SetDailyFlags(dflags)
	s.SetIntradayFlags(iflags)
	s.SetEmaFlags(ema)
	s.MarkNews(news)
}

// ─── process CPU time ────────────────────────────────────────────────────────
// pipeCPUSeconds (bench_pipeline_cpu_*_test.go): user+kernel CPU seconds of this
// process; 0 where it is not available.

// ─── the benchmark ───────────────────────────────────────────────────────────

var pipeShared struct {
	once sync.Once
	f    *pipeFixture
}

// pipeSharedFixture builds the live-sized fixture once per test binary: every
// sub-benchmark, and every -count repetition, continues the same simulated
// session, so a build (a few seconds) is not repeated for each b.N the testing
// package tries.
func pipeSharedFixture(b *testing.B) *pipeFixture {
	b.Helper()
	pipeShared.once.Do(func() {
		t0 := time.Now()
		f := pipeBuild(b, pipeFullCfg)
		var dildo, climax, pc int
		for i := range f.last.Rows {
			r := &f.last.Rows[i]
			if !core.IsNull(r.DildoAge) {
				dildo++
			}
			if !core.IsNull(r.ClimaxAge) {
				climax++
			}
			if !core.IsNull(r.PcAge) {
				pc++
			}
		}
		b.Logf("pipeline fixture: %d tickers, %d s warm-up, built in %v; rows with a dildo/climax/power candle today: %d/%d/%d; scan matches: %v",
			f.eng.Tracked(), pipeFullCfg.warmSecs, time.Since(t0).Round(time.Millisecond), dildo, climax, pc, f.scn.Counts())
		pipeShared.f = f
	})
	return pipeShared.f
}

func BenchmarkPipelineTick(b *testing.B) {
	f := pipeSharedFixture(b)
	for _, c := range []struct {
		name         string
		active, scan bool
	}{
		{"active", true, true},
		{"idle", false, true},
		{"active_engine", true, false},
		{"idle_engine", false, false},
	} {
		b.Run(c.name, func(b *testing.B) {
			var st pipeStats
			runtime.GC()
			b.ReportAllocs()
			cpu0 := pipeCPUSeconds()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				f.step(c.active, c.scan, &st)
			}
			b.StopTimer()
			if cpu := pipeCPUSeconds() - cpu0; cpu > 0 {
				b.ReportMetric(cpu*1000/float64(b.N), "cpu-ms/op")
			}
			if c.scan {
				n := float64(b.N)
				b.ReportMetric(float64(st.ticks)/n, "ticks/op")
				b.ReportMetric(float64(st.tickRows)/n, "tickrows/op")
				b.ReportMetric(float64(st.alerts)/n, "alerts/op")
				b.ReportMetric(float64(st.entries)/n, "entries/op")
			}
		})
	}
}

// ─── the end-to-end digest ───────────────────────────────────────────────────

// pipeDigestRun builds the small fixture and runs a fixed mix of active and idle
// ticks, hashing every scan.tick / alert / scan.enter the scanner emits in the
// order it emits them (rows are sorted with a symbol tiebreak, so the hash does not
// depend on the engine's row order).
func pipeDigestRun(t *testing.T, msgs *[]string) string {
	t.Helper()
	f := pipeBuild(t, pipeSmallCfg)
	defer f.close()
	st := pipeStats{h: sha256.New(), msgs: msgs}
	for i := 0; i < 36; i++ {
		f.step(i%4 != 3, true, &st) // three active ticks, then an idle one
	}
	if st.ticks == 0 || st.alerts == 0 || st.entries == 0 {
		t.Fatalf("the digest run exercised too little: %d ticks, %d alerts, %d entries", st.ticks, st.alerts, st.entries)
	}
	return fmt.Sprintf("ticks %d\ntickrows %d\nalerts %d\nentries %d\nsha256 %s\n",
		st.ticks, st.tickRows, st.alerts, st.entries, hex.EncodeToString(st.h.Sum(nil)))
}

const pipeDigestFile = "testdata/pipe_digest.txt"

// TestPipelineDigest pins the scanner output of a deterministic small pipeline
// (1,200 tickers, the 32 live scans, 36 ticks): engine rows -> overlays ->
// predicates -> sort -> diff -> alerts, end to end. A behaviour-preserving change
// to any stage leaves the digest alone. The run also repeats itself, so a flaky
// harness shows up as a mismatch between the two runs rather than as a bad golden.
//
//	go test ./internal/engine -run TestPipelineDigest                 (check)
//	go test ./internal/engine -run TestPipelineDigest -pipe-update    (regenerate)
//
// The generator uses only + - * / and sqrt, so the digest holds on any amd64
// build; a platform that fuses multiply-adds (arm64) would need its own.
func TestPipelineDigest(t *testing.T) {
	var m1, m2 []string
	got := pipeDigestRun(t, &m1)
	if again := pipeDigestRun(t, &m2); again != got {
		for i := 0; i < min(len(m1), len(m2)); i++ {
			if m1[i] != m2[i] {
				t.Errorf("first differing message is #%d:\n  run 1: %.600s\n  run 2: %.600s", i, m1[i], m2[i])
				break
			}
		}
		t.Fatalf("the pipeline is not deterministic:\nfirst run:\n%ssecond run:\n%s", got, again)
	}
	path := filepath.FromSlash(pipeDigestFile)
	if *pipeUpdate {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		hdr := "# End-to-end digest of TestPipelineDigest (bench_pipeline_test.go): what the scanner emits for a deterministic 1,200-ticker pipeline.\n" +
			"# Regenerate (only for an INTENDED change of scan output): go test ./internal/engine -run TestPipelineDigest -pipe-update\n"
		if err := os.WriteFile(path, []byte(hdr+got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("pipeline digest rewritten:\n%s", got)
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no digest file (%v): generate it with: go test ./internal/engine -run TestPipelineDigest -pipe-update", err)
	}
	var want []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			want = append(want, line)
		}
	}
	if w := strings.Join(want, "\n") + "\n"; w != got {
		t.Fatalf("pipeline output differs from %s:\nwant:\n%sgot:\n%sIf the change is intended, regenerate with: go test ./internal/engine -run TestPipelineDigest -pipe-update", pipeDigestFile, w, got)
	}
}
