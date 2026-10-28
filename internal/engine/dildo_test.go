package engine

import (
	"context"
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	_ "time/tzdata"

	"github.com/eelkonkoopman/marketscanner/internal/climax"
	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
)

// ─── shared helpers (climax_test.go uses them too) ───────────────────────

var nyLoc = func() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return loc
}()

// etOff is the UTC->ET offset in force at ms, the value Engine.etOffMs holds.
func etOff(ms int64) int64 {
	_, off := time.UnixMilli(ms).In(nyLoc).Zone()
	return int64(off) * 1000
}

// etMs is an ET wall-clock time on 2026-10-07 (a Wednesday, EDT), in ms.
func etMs(h, m int) int64 { return time.Date(2026, 10, 7, h, m, 0, 0, nyLoc).UnixMilli() }

// newSigTS is a hand-built ticker with the closed-bar judge wired (the shape
// shard.get gives it), the given signals on, the offset of 2026-10-07 (EDT).
func newSigTS(dildo, clim bool) (*tickerState, *signals) {
	sg := &signals{dildo: dildo, climax: clim, etOff: new(atomic.Int64)}
	sg.etOff.Store(-4 * 3_600_000)
	if dildo {
		sg.hits = make(chan core.DildoHit, dildoHitsBuffer)
	}
	return &tickerState{sym: "X", curveBkt: -1, curveFirst: -1, barHead: -1, sig: sg}, sg
}

// aggOf is one per-second aggregate carrying a whole bar, StartMs = its start.
func aggOf(b core.Bar) core.Agg {
	return core.Agg{Sym: "X", Open: b.O, High: b.H, Low: b.L, Close: b.C, Vol: b.V, StartMs: b.T, EndMs: b.T + 1000}
}

// feedBar applies one bar as one aggregate and lets the 1 Hz clock judge it
// (its minute end + the 3 s grace), with the offset in force at the bar.
func feed1m(ts *tickerState, b core.Bar) {
	ts.applyAgg(aggOf(b))
	ts.tickDildo(b.T+m1Ms+m1Grace, etOff(b.T))
}

// flatBars1m is n quiet 1-minute bars from start: price ~p, |body| 0.02*p,
// high 1.03p, low 0.99p, volume 1000.
func flatBars1m(start int64, n int, p float64) []core.Bar {
	out := make([]core.Bar, n)
	for i := range out {
		out[i] = core.Bar{T: start + int64(i)*m1Ms, O: p, H: p * 1.03, L: p * 0.99, C: p * 1.02, V: 1000}
		if i%2 == 1 {
			out[i].O, out[i].C = p*1.02, p
		}
	}
	return out
}

// dildoBar is a textbook green dildo after flatBars1m(.., 1.00): +9 % body
// (4.5x the 0.02 mean), closing in the top 9 % of its range, 200x volume,
// $218k.
func dildoBar(t int64) core.Bar {
	return core.Bar{T: t, O: 1.00, H: 1.10, L: 0.99, C: 1.09, V: 200_000}
}

func drainHits(sg *signals) []core.DildoHit {
	var out []core.DildoHit
	for {
		select {
		case h := <-sg.hits:
			out = append(out, h)
		default:
			return out
		}
	}
}

// sigRow is toRow + the shard's signal stamp, at nowMs.
func sigRow(ts *tickerState, nowMs int64) core.Row {
	r := ts.toRow(0, nil, nil, nil, core.SessionRegular)
	ts.signalRow(&r, nowMs, etOff(nowMs))
	return r
}

// ─── fixtures (web/src/signals/fixtures/{dildo,climax}) ──────────────────

type sigFixture struct {
	Sym  string     `json:"sym"`
	Date string     `json:"date"`
	Bars []core.Bar `json:"bars"`
}

func loadSigFixtures(t *testing.T, dir string) map[string]sigFixture {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "web", "src", "signals", "fixtures", dir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no %s fixtures (err %v)", dir, err)
	}
	out := map[string]sigFixture{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var fx sigFixture
		if err := json.Unmarshal(raw, &fx); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		out[fx.Sym] = fx
	}
	return out
}

// todayStart is 00:00 ET of the fixture's date (its "today").
func (fx sigFixture) todayStart(t *testing.T) int64 {
	d, err := time.ParseInLocation("2006-01-02", fx.Date, nyLoc)
	if err != nil {
		t.Fatal(err)
	}
	return d.UnixMilli()
}

func etClock(ms int64) string { return time.UnixMilli(ms).In(nyLoc).Format("15:04") }

// ─── bar building ────────────────────────────────────────────────────────

// Open comes from the print with the EARLIEST StartMs and the close from the
// LATEST, whatever order they arrive in; high/low are the extremes, volume the
// sum; and a print that arrives inside the 3 s grace still lands in its bar.
func TestDildoBarOpenEarliestCloseLatestAndGracePrint(t *testing.T) {
	ts, _ := newSigTS(true, true)
	m0 := etMs(10, 0)
	print := func(startMs int64, o, h, l, c float64, v int64) {
		ts.applyAgg(core.Agg{Sym: "X", Open: o, High: h, Low: l, Close: c, Vol: v, StartMs: startMs, EndMs: startMs + 1000})
	}
	print(m0+20_000, 1.05, 1.06, 1.04, 1.05, 100) // arrives first, mid-minute
	print(m0+2_000, 1.00, 1.01, 0.98, 1.00, 50)   // earlier StartMs: the open
	print(m0+59_000, 1.10, 1.12, 1.09, 1.11, 70)  // the latest: the close
	print(m0+30_000, 1.07, 1.08, 1.06, 1.07, 30)  // arrives last, but not the latest
	s := ts.d1.bar[1]
	if s.m != int32(m0/m1Ms) || s.o != 1.00 || s.c != 1.11 || s.h != 1.12 || s.l != 0.98 || s.v != 250 {
		t.Fatalf("bar %+v, want o 1.00 h 1.12 l 0.98 c 1.11 v 250", s)
	}
	// The next minute opens; the 10:00 bar waits in bar[0] for its grace.
	print(m0+60_000, 1.11, 1.11, 1.11, 1.11, 10)
	if ts.d1.bar[0].m != int32(m0/m1Ms) || ts.d1.bar[1].m != int32(m0/m1Ms)+1 {
		t.Fatalf("slots %+v", ts.d1.bar)
	}
	// A late print for 10:00 (StartMs 10:00:59.5) arrives at 10:01:02: it is the
	// latest of its minute, so it is the close.
	print(m0+59_500, 1.13, 1.14, 1.13, 1.13, 5)
	if s := ts.d1.bar[0]; s.c != 1.13 || s.h != 1.14 || s.v != 255 {
		t.Fatalf("grace print not folded: %+v", s)
	}
	// Not judged before end + 3 s; judged exactly once at the first tick >= it.
	ts.tickDildo(m0+m1Ms+m1Grace-1, etOff(m0))
	if ts.d1.lastJudged != 0 || ts.d1.n != 0 {
		t.Fatalf("judged before the grace ended: lastJudged %d n %d", ts.d1.lastJudged, ts.d1.n)
	}
	ts.tickDildo(m0+m1Ms+m1Grace, etOff(m0))
	if ts.d1.lastJudged != int32(m0/m1Ms) || ts.d1.n != 1 || ts.d1.bar[0].m != 0 {
		t.Fatalf("not judged at end + grace: lastJudged %d n %d slots %+v", ts.d1.lastJudged, ts.d1.n, ts.d1.bar)
	}
	if p := ts.d1.ring[0]; math.Abs(p.absBody-0.13) > 1e-12 || p.h != 1.14 || p.l != 0.98 || p.vol != 255 {
		t.Fatalf("ring slot %+v", p)
	}
	for k := 0; k < 5; k++ {
		ts.tickDildo(m0+m1Ms+m1Grace+int64(k)*1000, etOff(m0))
	}
	if ts.d1.n != 1 {
		t.Fatalf("judged twice: n %d", ts.d1.n)
	}
	// A print for the judged minute is dropped (the bar is final).
	print(m0+59_900, 9.99, 9.99, 9.99, 9.99, 1_000_000)
	if ts.d1.bar[0].m != 0 || ts.d1.bar[1].v != 10 || ts.d1.n != 1 {
		t.Fatalf("a print for a judged minute must be dropped: %+v n %d", ts.d1.bar, ts.d1.n)
	}
	// A quiet ticker: the 10:01 bar is judged by the clock alone.
	ts.tickDildo(m0+2*m1Ms+m1Grace, etOff(m0))
	if ts.d1.n != 2 || ts.d1.bar[1].m != 0 || ts.d1.lastJudged != int32(m0/m1Ms)+1 {
		t.Fatalf("quiet minute not judged: n %d slots %+v", ts.d1.n, ts.d1.bar)
	}
}

// Clock skew: a third minute's print arrives while the oldest slot is still
// waiting for the 1 Hz clock: it is judged then, in minute order, once.
func TestDildoClockSkewJudgesTheOldestSlotFirst(t *testing.T) {
	ts, _ := newSigTS(true, true)
	m0 := etMs(10, 0)
	for k := int64(0); k < 3; k++ {
		ts.applyAgg(aggOf(core.Bar{T: m0 + k*m1Ms, O: 1, H: 1, L: 1, C: 1, V: 10}))
	}
	if ts.d1.lastJudged != int32(m0/m1Ms) || ts.d1.n != 1 || ts.d1.bar[0].m != int32(m0/m1Ms)+1 || ts.d1.bar[1].m != int32(m0/m1Ms)+2 {
		t.Fatalf("skew path: lastJudged %d n %d slots %+v", ts.d1.lastJudged, ts.d1.n, ts.d1.bar)
	}
	ts.tickDildo(m0+3*m1Ms+m1Grace, etOff(m0))
	if ts.d1.n != 3 || ts.d1.lastJudged != int32(m0/m1Ms)+2 {
		t.Fatalf("n %d lastJudged %d", ts.d1.n, ts.d1.lastJudged)
	}
}

// Priors are TRADED bars: empty clock minutes never enter the ring, so a
// dildo after a gap is judged against the bars that actually traded.
func TestDildoPriorsCountTradedBarsAndSkipEmptyMinutes(t *testing.T) {
	ts, sg := newSigTS(true, false)
	start := etMs(10, 0)
	// Three quiet traded bars, each 5 minutes apart (12 empty minutes between).
	for i := int64(0); i < 3; i++ {
		feed1m(ts, core.Bar{T: start + i*5*m1Ms, O: 1.00, H: 1.03, L: 0.99, C: 1.02, V: 1000})
	}
	if ts.d1.n != 3 {
		t.Fatalf("ring holds %d bars, want the 3 traded ones", ts.d1.n)
	}
	feed1m(ts, dildoBar(start+15*m1Ms))
	hits := drainHits(sg)
	if len(hits) != 1 || hits[0].BarMs != start+15*m1Ms {
		t.Fatalf("hits %+v, want the 10:15 dildo judged on its 3 traded priors", hits)
	}
	// 3 priors of |body| 0.02 and 1000 volume each: bodyX 0.09/0.02, volX 200.
	if h := hits[0]; math.Abs(h.BodyX-4.5) > 1e-9 || math.Abs(h.VolX-200) > 1e-9 || h.O != 1.00 || h.C != 1.09 || h.V != 200_000 {
		t.Fatalf("hit %+v", h)
	}
	// With only 2 traded bars before it, the same candle is not judged.
	ts2, sg2 := newSigTS(true, false)
	for i := int64(0); i < 2; i++ {
		feed1m(ts2, core.Bar{T: start + i*5*m1Ms, O: 1.00, H: 1.03, L: 0.99, C: 1.02, V: 1000})
	}
	feed1m(ts2, dildoBar(start+15*m1Ms))
	if hits := drainHits(sg2); len(hits) != 0 || ts2.d1.latch.barMs != 0 {
		t.Fatalf("2 priors < minPriorBars 3: hits %+v latch %+v", hits, ts2.d1.latch)
	}
}

// The minute the engine began listening in was only partly observed: it is a
// prior (pushed into the ring) but never judged, however big it looks.
func TestDildoPartialMinuteIsNeverJudged(t *testing.T) {
	ts, sg := newSigTS(true, true)
	start := etMs(10, 0)
	for _, b := range flatBars1m(start, 10, 1.00) {
		feed1m(ts, b)
	}
	listen := start + 10*m1Ms + 17_000 // started listening 17 s into the 10:10 minute
	ts2, sg2 := newSigTS(true, true)
	ts2.m5ListenMs = listen
	// The same history, but everything before the listen minute never reached
	// it: only the listen minute (a dildo by its numbers) and later.
	feed1m(ts2, dildoBar(start+10*m1Ms))
	if hits := drainHits(sg2); len(hits) != 0 || ts2.d1.n != 1 || ts2.d1.todayN != 0 {
		t.Fatalf("partial minute judged: hits %+v n %d todayN %d", hits, ts2.d1.n, ts2.d1.todayN)
	}
	// The control: with the whole history, that candle is a dildo.
	feed1m(ts, dildoBar(start+10*m1Ms))
	if hits := drainHits(sg); len(hits) != 1 {
		t.Fatalf("control: hits %+v, want the 10:10 dildo", hits)
	}
}

// seedNow is ingest's (re)connect re-seed of sym (Engine.Seed) at ms, with the
// closure it queues run in place of the shard loop; it returns the ticker.
func seedNow(e *Engine, sym string, ms int64) *tickerState {
	e.Seed(core.Seed{Agg: core.Agg{Sym: sym, Close: 10, Vol: 1000, StartMs: ms - 1000, EndMs: ms}})
	sh := e.shards[shardIdx(sym, e.mask)]
	(<-sh.ctrl)(sh)
	return sh.state[sym]
}

// RV2-SRV-2: the part-observed minute is the one streaming began in, not the
// one the engine was built in. New() runs at 10:15:57; ingest's whole-market
// REST seed and the WebSocket subscribe take until 10:16:50, so the first
// streamed minute (10:16) saw only its last 10 s: volume 100 of a real ~1,000.
// Judged as complete it became a prior that pulled the 10-bar mean down so far
// that a 7,500-share flush at 10:26 read as 8.2x - a climax that the full tape
// (7.5x) does not have.
func TestJudgePartialMinuteIsTheFirstStreamedOneNotTheEngineStart(t *testing.T) {
	clock := etMs(10, 15) + 57_000
	e := New(Options{Climax: true, Now: func() time.Time { return time.UnixMilli(clock) }})
	clock = etMs(10, 16) + 50_000 // ingest seeds as the stream starts
	ts := seedNow(e, "X", clock)
	start := etMs(10, 16)
	feed1m(ts, core.Bar{T: start + 50_000, O: 10.1, H: 10.3, L: 9.9, C: 10.2, V: 100})
	if ts.d1.todayN != 0 || ts.d1.n != 1 {
		t.Fatalf("the first streamed minute was judged as complete: todayN %d n %d, want 0 (restarted) / 1 (pushed)", ts.d1.todayN, ts.d1.n)
	}
	for _, b := range flatBars1m(start+m1Ms, 9, 10) {
		feed1m(ts, b)
	}
	feed1m(ts, core.Bar{T: start + 10*m1Ms, O: 9.80, H: 9.85, L: 9.00, C: 9.50, V: 7_500})
	if ts.d1.cl.State != climax.StateNone {
		t.Fatalf("a climax armed on a part-observed prior: %+v", ts.d1.cl)
	}
}

// The same after a feed reconnect mid-session (ingest re-seeds on every
// (re)connect): the stream is back 40 s into 10:10, and that minute's last
// 20 s look like a climax. It is a prior, never judged, and todayN restarts;
// without the reconnect the same prints are a whole minute and arm it.
func TestJudgePartialMinuteAfterAFeedReconnect(t *testing.T) {
	for _, reconnect := range []bool{false, true} {
		clock := etMs(9, 58)
		e := New(Options{Climax: true, Now: func() time.Time { return time.UnixMilli(clock) }})
		clock = etMs(9, 59)
		ts := seedNow(e, "X", clock)
		start := etMs(10, 0)
		for _, b := range flatBars1m(start, 10, 10) {
			feed1m(ts, b)
		}
		if reconnect {
			clock = start + 10*m1Ms + 40_000
			seedNow(e, "X", clock)
		}
		c := climaxBar(start + 10*m1Ms)
		c.T += 40_000
		feed1m(ts, c)
		switch {
		case !reconnect && ts.d1.cl.State != climax.StateWatching:
			t.Fatalf("control (no reconnect): %+v, want the 10:10 climax armed", ts.d1.cl)
		case reconnect && (ts.d1.cl.State != climax.StateNone || ts.d1.todayN != 0):
			t.Fatalf("the minute the stream came back in was judged: %+v todayN %d", ts.d1.cl, ts.d1.todayN)
		}
	}
}

// The calibrated liquidity floor is applied by the engine itself: close >=
// $0.20 and candle dollar volume >= $100k.
func TestDildoFloorIsApplied(t *testing.T) {
	start := etMs(10, 0)
	at := start + 10*m1Ms
	for _, c := range []struct {
		name string
		base float64 // the flat priors' price
		bar  core.Bar
		want bool
	}{
		{"textbook", 1.00, dildoBar(at), true},
		{"$99,998.78 of candle volume", 1.00, core.Bar{T: at, O: 1.00, H: 1.10, L: 0.99, C: 1.09, V: 91_742}, false},
		{"exactly $100k (inclusive)", 1.00, core.Bar{T: at, O: 1.00, H: 1.26, L: 0.99, C: 1.25, V: 80_000}, true},
		{"close $0.179", 0.15, core.Bar{T: at, O: 0.15, H: 0.18, L: 0.149, C: 0.179, V: 9_000_000}, false},
		{"close exactly $0.20 (inclusive)", 0.18, core.Bar{T: at, O: 0.18, H: 0.201, L: 0.179, C: 0.20, V: 1_000_000}, true},
	} {
		ts, sg := newSigTS(true, false)
		for _, b := range flatBars1m(start, 10, c.base) {
			feed1m(ts, b)
		}
		feed1m(ts, c.bar)
		if got := len(drainHits(sg)) == 1; got != c.want {
			t.Errorf("%s: dildo %v, want %v", c.name, got, c.want)
		}
	}
}

// A full hits channel never blocks the shard: the hit is dropped and counted,
// and the row latch (alert 1) still stands.
func TestDildoFullHitsChannelCountsDropsWithoutBlocking(t *testing.T) {
	ts, sg := newSigTS(true, false)
	sg.hits = make(chan core.DildoHit, 1)
	start := etMs(10, 0)
	for _, b := range flatBars1m(start, 10, 1.00) {
		feed1m(ts, b)
	}
	feed1m(ts, dildoBar(start+10*m1Ms))
	for _, b := range flatBars1m(start+11*m1Ms, 10, 1.09) {
		feed1m(ts, b)
	}
	second := core.Bar{T: start + 21*m1Ms, O: 1.09, H: 1.20, L: 1.08, C: 1.19, V: 300_000}
	done := make(chan struct{})
	go func() { feed1m(ts, second); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a full hits channel blocked the judge")
	}
	if sg.drops.Load() != 1 || len(drainHits(sg)) != 1 {
		t.Fatalf("drops %d, want 1 (and the first hit still queued)", sg.drops.Load())
	}
	if ts.d1.latch.barMs != second.T {
		t.Fatalf("latch %+v, want the dropped hit's bar: alert 1 stands on the row", ts.d1.latch)
	}
}

// ─── the ring ────────────────────────────────────────────────────────────

// The judge recomputes its priors from the ring at judge time; across ring
// wraps, empty minutes, day changes and the partial minute, the sums must be
// BIT-identical to a brute-force sum over the last bars pushed (the chart's
// dildopb priors and climax.PriorsOf, oldest -> newest).
func TestDildoRingSumsEqualBruteForceBitForBit(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 50; trial++ {
		ts, _ := newSigTS(true, true)
		var pushed []core.Bar
		at := etMs(4, 0) - 600*m1Ms
		ts.m5ListenMs = at + 30_000 // the first minute is partial
		for i := 0; i < 400; i++ {
			at += int64(1+rng.Intn(4)) * m1Ms
			if rng.Intn(50) == 0 {
				at += 9 * 3_600_000 // overnight: a new ET day
			}
			p := 0.3 + rng.Float64()*20
			b := core.Bar{T: at, O: p, H: p * (1 + rng.Float64()*0.2), L: p * (1 - rng.Float64()*0.2), C: p * (0.9 + rng.Float64()*0.2), V: rng.Int63n(5_000_000)}
			b.H, b.L = max(b.H, b.O, b.C), min(b.L, b.O, b.C)
			feed1m(ts, b)
			pushed = append(pushed, b)
			n := min(len(pushed), judgeRing)
			if ts.d1.n != n {
				t.Fatalf("trial %d bar %d: ring count %d, want %d", trial, i, ts.d1.n, n)
			}
			last := pushed[len(pushed)-n:]
			var wantBody, wantVol float64
			for _, q := range last {
				wantBody += math.Abs(q.C - q.O)
				wantVol += float64(q.V)
			}
			gotBody, gotVol := ts.d1.dildoSums()
			if math.Float64bits(gotBody) != math.Float64bits(wantBody) || math.Float64bits(gotVol) != math.Float64bits(wantVol) {
				t.Fatalf("trial %d bar %d: dildo sums %v/%v, brute force %v/%v", trial, i, gotBody, gotVol, wantBody, wantVol)
			}
			if n == judgeRing {
				got := ts.d1.climaxPriors(judgeRing)
				want := climax.PriorsOf(pushed, len(pushed)-judgeRing, len(pushed))
				if got.N != want.N || math.Float64bits(got.Hi) != math.Float64bits(want.Hi) || math.Float64bits(got.Lo) != math.Float64bits(want.Lo) ||
					math.Float64bits(got.SumVol) != math.Float64bits(want.SumVol) || got.MaxVol != want.MaxVol {
					t.Fatalf("trial %d bar %d: climax priors %+v, PriorsOf %+v", trial, i, got, want)
				}
			}
		}
	}
}

// The ring length is the calibrated rules' prior count, in both rules.
func TestJudgeRingMatchesTheRuleDefaults(t *testing.T) {
	if p := dildopb.DefaultParams(); p.PriorBars != judgeRing || p.EmaPeriod != emaPeriod {
		t.Fatalf("dildopb PriorBars %d EmaPeriod %d, the judge assumes %d / %d", p.PriorBars, p.EmaPeriod, judgeRing, emaPeriod)
	}
	if p := climax.DefaultParams(); p.Lookback != judgeRing || p.EmaPeriod != emaPeriod {
		t.Fatalf("climax Lookback %d EmaPeriod %d, the judge assumes %d / %d", p.Lookback, p.EmaPeriod, judgeRing, emaPeriod)
	}
	if dildopb.Live() != dildopb.DefaultParams() || climaxParams != climax.DefaultParams() {
		t.Fatal("the engine must judge with the calibrated defaults")
	}
}

// ─── the row ─────────────────────────────────────────────────────────────

// toRow Nulls every signal field; signalRow stamps today's latest dildo with
// its age counted from the bar's START (the e9SlamAge convention: the first
// value a snapshot shows is ~63 s); a +Inf multiple is Null; a latch from
// before today's 04:00 ET is not shown.
func TestDildoRowFields(t *testing.T) {
	ts, _ := newSigTS(true, true)
	r := ts.toRow(0, nil, nil, nil, core.SessionRegular)
	for name, v := range map[string]float64{
		"dildoAge": r.DildoAge, "dildoBody": r.DildoBody, "dildoVolX": r.DildoVolX, "dildoDvol": r.DildoDvol,
		"dildoPbAge": r.DildoPbAge, "dildoPbPx": r.DildoPbPx, "dildoTouchAge": r.DildoTouchAge, "dildoPbState": r.DildoPbState,
		"climaxAge": r.ClimaxAge, "climaxDropPct": r.ClimaxDropPct, "climaxVolX": r.ClimaxVolX, "climaxWickPct": r.ClimaxWickPct,
		"climaxDvol": r.ClimaxDvol, "climaxLow": r.ClimaxLow, "climaxState": r.ClimaxState, "climaxConfAge": r.ClimaxConfAge,
		"climaxConfPx": r.ClimaxConfPx,
	} {
		if !core.IsNull(v) {
			t.Errorf("toRow %s = %v, want Null", name, v)
		}
	}
	if r := sigRow(ts, etMs(10, 0)); !core.IsNull(r.DildoAge) {
		t.Fatalf("no dildo yet: dildoAge %v", r.DildoAge)
	}

	start := etMs(10, 0)
	for _, b := range flatBars1m(start, 10, 1.00) {
		feed1m(ts, b)
	}
	d := dildoBar(start + 10*m1Ms)
	feed1m(ts, d)
	r = sigRow(ts, d.T+m1Ms+m1Grace) // the first snapshot after the judge
	if r.DildoAge != 63 || math.Abs(r.DildoBody-9) > 1e-9 || math.Abs(r.DildoVolX-200) > 1e-9 || math.Abs(r.DildoDvol-218_000) > 1e-6 {
		t.Fatalf("row %v %v %v %v, want 63 s, 9 %%, 200x, $218k", r.DildoAge, r.DildoBody, r.DildoVolX, r.DildoDvol)
	}
	if r := sigRow(ts, d.T+239_999); r.DildoAge != 239 {
		t.Fatalf("whole seconds, floored: %v", r.DildoAge)
	}
	// The overlay's four fields are not the engine's.
	if !core.IsNull(r.DildoPbAge) || !core.IsNull(r.DildoPbState) {
		t.Fatal("the pullback fields are the scanner overlay's, Null from the engine")
	}
	// +Inf multiple: Null on the row.
	ts.d1.latch.volX = math.Inf(1)
	if r := sigRow(ts, d.T+70_000); !core.IsNull(r.DildoVolX) || r.DildoAge != 70 {
		t.Fatalf("+Inf volX must be Null: %v", r.DildoVolX)
	}
	// Still today at 19:59; the next day at 04:00 it is gone (the rollover is
	// the row's "today" rule; nothing is reset).
	if r := sigRow(ts, etMs(19, 59)); core.IsNull(r.DildoAge) {
		t.Fatal("a dildo of today must show all day")
	}
	next := time.Date(2026, 10, 8, 4, 0, 0, 0, nyLoc).UnixMilli()
	if r := sigRow(ts, next); !core.IsNull(r.DildoAge) || !core.IsNull(r.DildoBody) || !core.IsNull(r.DildoDvol) {
		t.Fatalf("yesterday's dildo after the 04:00 rollover: %v", r.DildoAge)
	}
	if r := sigRow(ts, next-1); core.IsNull(r.DildoAge) {
		t.Fatal("03:59:59.999 is still the previous trading day's view (before the anchor nothing new can print)")
	}
}

// ─── parity with the chart rule (dildopb.Evaluate) over the fixtures ─────

// engineDildos replays bars through a fresh judge, one aggregate per bar
// (StartMs = the bar's start), and returns the dildo bars it hit with the
// number of priors the engine had at each.
func engineDildos(t *testing.T, bars []core.Bar) (hits []int64, priors map[int64]int) {
	t.Helper()
	ts, sg := newSigTS(true, true)
	priors = map[int64]int{}
	var prev int64
	for _, b := range bars {
		if ds := dildopb.DayStartMs(b.T); prev > 0 && prev < ds && b.T >= ds {
			ts.resetSession() // the 04:00 rollover (the Engine's clock) clears yesterday's hod
		}
		prev = b.T
		n := ts.d1.n
		feed1m(ts, b)
		for _, h := range drainHits(sg) {
			hits = append(hits, h.BarMs)
			priors[h.BarMs] = n
		}
		priors[b.T] = n
	}
	return hits, priors
}

func chartDildos(bars []core.Bar, from int64) []int64 {
	res := dildopb.Evaluate(bars, 60_000, bars[len(bars)-1].T+2*m1Ms, dildopb.DefaultParams())
	var out []int64
	for _, ev := range res.Events {
		if ev.Kind == dildopb.KindDildo && ev.Ts >= from {
			out = append(out, ev.Ts)
		}
	}
	return out
}

// hodOnly keeps the dildos whose high is strictly above every earlier high of
// their ET day (dildopb.DayHighBefore; none earlier = a HOD): the only ones the
// engine alerts.
func hodOnly(bars []core.Bar, ms []int64) []int64 {
	var out []int64
	for _, m := range ms {
		for _, b := range bars {
			if b.T == m {
				if hi := dildopb.DayHighBefore(bars, m); hi == 0 || b.H > hi {
					out = append(out, m)
				}
			}
		}
	}
	return out
}

// clearDayHigh forgets the day high so far (a test's earlier bars at other prices).
func clearDayHigh(ts *tickerState) {
	ts.d1.dayHi, ts.hod, ts.hodSet = 0, 0, false
}

func clocks(ms []int64) []string {
	out := make([]string, len(ms))
	for i, v := range ms {
		out[i] = etClock(v)
	}
	return out
}

func equalInt64s(a, b []int64) bool {
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

// Fed the previous day AND today (as the chart is), the engine's dildos for
// today are EXACTLY the chart's - WVVIP's 04:01 dildo, whose priors are the
// previous evening's post-market bars, included. Fed today only (the 03:30
// cold start), the engine misses only dildos it had fewer than 3 traded bars
// before (WVVIP 04:01: the known v1 limit) and finds nothing the chart does
// not.
func TestDildoEngineParityWithTheChartRule(t *testing.T) {
	fixtures := loadSigFixtures(t, "dildo")
	if len(fixtures) != 5 {
		t.Fatalf("%d dildo fixtures, want 5", len(fixtures))
	}
	syms := make([]string, 0, len(fixtures))
	for s := range fixtures {
		syms = append(syms, s)
	}
	sort.Strings(syms)
	for _, sym := range syms {
		fx := fixtures[sym]
		today := fx.todayStart(t)
		want := hodOnly(fx.Bars, chartDildos(fx.Bars, today))

		got, _ := engineDildos(t, fx.Bars)
		var gotToday []int64
		for _, ms := range got {
			if ms >= today {
				gotToday = append(gotToday, ms)
			}
		}
		if !equalInt64s(gotToday, want) {
			t.Errorf("%s prev day + today: engine %v, chart %v", sym, clocks(gotToday), clocks(want))
		}

		var todays []core.Bar
		for _, b := range fx.Bars {
			if b.T >= today {
				todays = append(todays, b)
			}
		}
		cold, priors := engineDildos(t, todays)
		var expect []int64
		for _, ms := range want {
			if priors[ms] >= dildopb.DefaultParams().MinPriorBars {
				expect = append(expect, ms)
			}
		}
		if !equalInt64s(cold, expect) {
			t.Errorf("%s today only: engine %v, chart minus <3-prior bars %v", sym, clocks(cold), clocks(expect))
		}
		t.Logf("%-5s chart %v | engine prev+today %v | engine today-only %v", sym, clocks(want), clocks(gotToday), clocks(cold))
		if sym == "WVVIP" && len(want) > 0 {
			at0401 := etFixtureMs(t, fx, 4, 1)
			if want[0] != at0401 {
				t.Fatalf("WVVIP: the chart's first dildo today is %s, want 04:01", etClock(want[0]))
			}
			for _, ms := range cold {
				if ms == at0401 {
					t.Errorf("WVVIP cold start: the 04:01 dildo (1 traded bar before it today) must be missed: %v", clocks(cold))
				}
			}
		}
	}
}

func etFixtureMs(t *testing.T, fx sigFixture, h, m int) int64 {
	return fx.todayStart(t) + int64(h)*3_600_000 + int64(m)*m1Ms
}

// ─── the engine end to end ───────────────────────────────────────────────

// Through New/Run: the Options gate the judge; a dildo reaches the hits channel
// and the snapshot row, aged from the bar's start by the snapshot clock.
func TestEngineDildoEndToEnd(t *testing.T) {
	off := New(Options{})
	if off.DildoHits() != nil || off.DildoDrops() != 0 || off.sig != nil {
		t.Fatal("with both signals off the judge must not be wired")
	}

	start := time.Date(2026, 6, 23, 10, 0, 0, 0, nyLoc).UnixMilli() // a past day: the real 1 Hz clock is long past every bar
	// The engine starts listening before the first bar (a bar from before the
	// start is part-observed and never judged); the snapshots then read 10:11:30.
	var clock atomic.Int64
	clock.Store(start - m1Ms)
	eng := New(Options{TickHz: 20, Now: func() time.Time { return time.UnixMilli(clock.Load()) }, Dildo: true, Climax: true})
	clock.Store(start + 11*m1Ms + 30_000)
	if eng.etOffMs.Load() != -4*3_600_000 {
		t.Fatalf("etOffMs %d, want EDT", eng.etOffMs.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()
	bars := append(flatBars1m(start, 10, 1.00), dildoBar(start+10*m1Ms))
	for _, b := range bars {
		a := aggOf(b)
		a.Sym = "DLDO"
		eng.Apply(a)
	}
	select {
	case h := <-eng.DildoHits():
		if h.Sym != "DLDO" || h.BarMs != start+10*m1Ms || h.C != 1.09 {
			t.Fatalf("hit %+v", h)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no dildo hit from the engine")
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case snap := <-eng.Snapshots():
			i, ok := snap.Index["DLDO"]
			if !ok || core.IsNull(snap.Rows[i].DildoAge) {
				continue
			}
			if r := snap.Rows[i]; r.DildoAge != 90 || math.Abs(r.DildoBody-9) > 1e-9 {
				t.Fatalf("row dildoAge %v body %v, want 90 s (10:10:00 -> 10:11:30), 9 %%", r.DildoAge, r.DildoBody)
			}
			return
		case <-deadline:
			t.Fatal("no snapshot carried the dildo")
		}
	}
}

// ─── cost ────────────────────────────────────────────────────────────────

// accumDildo runs on every print of every ticker: O(1), no allocation.
func BenchmarkAccumDildo(b *testing.B) {
	ts, _ := newSigTS(true, true)
	base := etMs(10, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sec := int64(i % 50) // 50 prints within one minute, then the next minute
		m := int64(i / 50)
		ts.accumDildo(core.Agg{Open: 1, High: 1.01, Low: 0.99, Close: 1, Vol: 100, StartMs: base + m*m1Ms + sec*1000})
	}
}

// One closed traded bar judged with both signals on and a full ring: O(10),
// no allocation.
func BenchmarkJudge(b *testing.B) {
	ts, sg := newSigTS(true, true)
	go func() {
		for range sg.hits {
		}
	}()
	b.Cleanup(func() { close(sg.hits) })
	base := etMs(10, 0)
	for _, bar := range flatBars1m(base, 20, 1.00) {
		feed1m(ts, bar)
	}
	off := etOff(base)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m := int32(base/m1Ms) + 20 + int32(i)
		s := d1Slot{m: m, o: 1.00, h: 1.01, l: 0.99, c: 1.005, v: 1000, oTs: int64(m) * m1Ms, cTs: int64(m) * m1Ms}
		ts.judgeBar(&s, off)
	}
}

// signalMarket is 11,000 tickers with the judge on and a warm ring each.
func signalMarket() ([]*tickerState, *signals, int64) {
	sg := &signals{dildo: true, climax: true, etOff: new(atomic.Int64), hits: make(chan core.DildoHit, dildoHitsBuffer)}
	sg.etOff.Store(-4 * 3_600_000)
	base := etMs(10, 0)
	tss := make([]*tickerState, 11_000)
	for i := range tss {
		tss[i] = &tickerState{sym: "S", curveBkt: -1, curveFirst: -1, barHead: -1, sig: sg}
		for _, bar := range flatBars1m(base, 12, 1.00+float64(i%97)/10) {
			tss[i].accumDildo(aggOf(bar))
			tss[i].tickDildo(bar.T+m1Ms+m1Grace, sg.etOff.Load())
		}
	}
	return tss, sg, base + 12*m1Ms
}

// The market-wide cost, 11,000 tickers. Per SECOND (the 1 Hz shard tick, the
// common case: no slot is due): one tickDildo per ticker. Per MINUTE: every
// ticker prints its minute and the clock judges it (dildo + climax, full ring).
func BenchmarkSignals11k(b *testing.B) {
	b.Run("tick-per-second", func(b *testing.B) {
		tss, sg, next := signalMarket()
		off := sg.etOff.Load()
		for _, ts := range tss { // a forming minute in every ticker, not yet due
			ts.accumDildo(core.Agg{Open: 1, High: 1, Low: 1, Close: 1, Vol: 1, StartMs: next})
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			nowMs := next + int64(i%60)*1000
			for _, ts := range tss {
				ts.tickDildo(nowMs, off)
			}
		}
	})
	b.Run("judge-per-minute", func(b *testing.B) {
		tss, sg, next := signalMarket()
		go func() {
			for range sg.hits {
			}
		}()
		b.Cleanup(func() { close(sg.hits) })
		off := sg.etOff.Load()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			t0 := next + int64(i)*m1Ms
			a := core.Agg{Open: 1.00, High: 1.01, Low: 0.99, Close: 1.005, Vol: 1000, StartMs: t0}
			for _, ts := range tss {
				ts.accumDildo(a)
			}
			for _, ts := range tss {
				ts.tickDildo(t0+m1Ms+m1Grace, off)
			}
		}
	})
}

// The Signal window's tuning panel reaches the judge: a swap of dildopb's live
// params changes the next bar's verdict, with no restart.
func TestDildoFollowsLiveParams(t *testing.T) {
	t.Cleanup(dildopb.ResetLive)
	start := etMs(10, 0)
	at := start + 10*m1Ms
	run := func() int {
		ts, sg := newSigTS(true, false)
		for _, b := range flatBars1m(start, 10, 1.00) {
			feed1m(ts, b)
		}
		feed1m(ts, dildoBar(at))
		return len(drainHits(sg))
	}
	if run() != 1 {
		t.Fatal("defaults must call the textbook dildo")
	}
	p := dildopb.DefaultParams()
	p.MinBodyPct = 12 // the textbook body is +9 %
	dildopb.SetLive(p)
	if run() != 0 {
		t.Fatal("minBodyPct 12 must reject the +9 % body")
	}
	p = dildopb.DefaultParams()
	p.Enabled = false
	dildopb.SetLive(p)
	if run() != 0 {
		t.Fatal("enabled=false must silence the judge")
	}
	p = dildopb.DefaultParams()
	p.PriorBars, p.MinPriorBars = 4, 4 // a shorter yardstick still sees 4 bars
	dildopb.SetLive(p)
	if run() != 1 {
		t.Fatal("priorBars 4 must still call the dildo")
	}
	dildopb.ResetLive()
	if run() != 1 {
		t.Fatal("reset must restore the defaults")
	}
}
