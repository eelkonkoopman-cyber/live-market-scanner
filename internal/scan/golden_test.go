package scan

import (
	"flag"
	"fmt"
	"math"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// Golden-equivalence test for the scanner. It drives a Scanner holding the
// user's 32 live scans plus ~100 synthetic ones (golden_defs_test.go) through
// goldSteps consecutive engine-shaped snapshots of goldRows tickers
// (golden_gen_test.go), changing scans and overlays on the way, and fingerprints
// everything the scanner emits at every step: each scan.tick, alert and entry,
// the match counts, and (every 25 steps) the resync / Row / TopMovers answers.
// testdata/golden_scan.txt holds one line per step with a SHA-256 of that
// step's canonical items (golden_canon_test.go), recorded from the code BEFORE
// the performance work; any change that alters what the scanner emits fails at
// the first step it shows.
//
//	go test ./internal/scan -run TestGolden                  compare with the golden
//	go test ./internal/scan -run TestGolden -update-golden   rewrite the golden
//	GOLDEN_DUMP=<dir> go test ./internal/scan -run TestGolden
//	    also write every canonical item to <dir>/scan_steps.jsonl; dump the old
//	    and the new code to two folders and diff the files to see WHAT changed
//	GOLDEN_STATS=1 go test ./internal/scan -run TestGolden -v   per-scan activity
//
// Everything is deterministic: the market and the overlay inputs come from
// seeded PCG streams, snapshot times are fixed, and the one source of
// nondeterminism in the scanner (scans run in map order, so items of DIFFERENT
// scans interleave randomly) is removed by ordering items by scan id, which
// keeps each scan's own sequence.

var goldUpdate = flag.Bool("update-golden", false, "rewrite testdata/golden_scan.txt from the current code")

const (
	goldRows   = 3000
	goldSteps  = 700
	goldSeed   = 0x5ca9c0de
	goldPath   = "testdata/golden_scan.txt"
	goldHeader = "# scan golden v1"
)

// goldTimes is the snapshot clock: pre-market into the open (with the 09:35
// opening-range end), the late morning, the 16:00 close, the 20:00 end of the
// session, an ET midnight, the next day's 04:00 start and open.
func goldTimes() []int64 {
	segs := []struct {
		start, stride int64
		n             int
	}{
		{goldAt(20, 8, 56, 0), 15_000, 100},
		{goldAt(20, 9, 28, 0), 5_000, 120},
		{goldAt(20, 9, 40, 0), 40_000, 90},
		{goldAt(20, 15, 57, 0), 10_000, 60},
		{goldAt(20, 19, 50, 0), 15_000, 60},
		{goldAt(20, 23, 58, 30), 20_000, 12},
		{goldAt(21, 3, 58, 0), 10_000, 24},
		{goldAt(21, 7, 0, 0), 20_000, 100},
		{goldAt(21, 9, 29, 0), 10_000, 80},
		{goldAt(21, 12, 0, 0), 60_000, 54},
	}
	var out []int64
	for _, s := range segs {
		for i := 0; i < s.n; i++ {
			out = append(out, s.start+int64(i)*s.stride)
		}
	}
	if len(out) != goldSteps {
		panic(fmt.Sprintf("goldTimes: %d steps, want %d", len(out), goldSteps))
	}
	return out
}

// goldAcc reads a row's value for a scan field straight off the core.Row struct
// (by reflection, independent of the scan package's own accessors), for picking
// probe thresholds and overlay candidates. ok=false: null / not numeric.
var goldAcc = func() map[string]func(*core.Row) (float64, bool) {
	rt := reflect.TypeOf(core.Row{})
	out := map[string]func(*core.Row) (float64, bool){}
	for _, name := range goldFields {
		idx := -1
		for i := 0; i < rt.NumField(); i++ {
			if strings.EqualFold(rt.Field(i).Name, name) {
				idx = i
			}
		}
		if idx < 0 {
			continue
		}
		switch rt.Field(idx).Type.Kind() {
		case reflect.Float64:
			out[name] = func(r *core.Row) (float64, bool) {
				v := reflect.ValueOf(r).Elem().Field(idx).Float()
				return v, !math.IsNaN(v)
			}
		case reflect.Int, reflect.Int64:
			out[name] = func(r *core.Row) (float64, bool) {
				v := reflect.ValueOf(r).Elem().Field(idx).Int()
				return float64(v), !(name == "newsAge" && v < 0)
			}
		case reflect.Pointer: // Float, ShsOut, ShortAvail: nil = unknown
			out[name] = func(r *core.Row) (float64, bool) {
				p := reflect.ValueOf(r).Elem().Field(idx)
				if p.IsNil() {
					return 0, false
				}
				return float64(p.Elem().Int()), true
			}
		}
	}
	return out
}()

// goldStat is a scan's activity over the run (GOLDEN_STATS).
type goldStat struct {
	ticks, rows, alerts, entries, maxCount, fullRows int
}

// goldRun is one pass of the golden scenario.
type goldRun struct {
	t     testing.TB
	s     *Scanner
	gen   *goldGen
	ov    *goldRand // the overlay inputs' own stream, apart from the market's
	times []int64
	prev  *core.MetricSnapshot // last snapshot handed to process (rows carry the overlays)

	allSyms []string
	symIdx  map[string]int
	movers  []string // symbols with a big day change in prev
	leaders []string // pre-market volume leaders in prev
	gappers []string // gap >= 9% and >= $4 in prev

	stats map[string]*goldStat
	sum   struct{ ticks, rows, alerts, entries, resyncRows int }
}

func newGoldRun(t testing.TB) *goldRun {
	gen := newGoldGen(goldGenConfig{Rows: goldRows, Seed: goldSeed, Start: goldTimes()[0], Active: 1, Flicker: 0.02})
	s := New(nil)
	// Nothing may be dropped on a full channel: a golden that depends on how many
	// items fit in the production buffers would hide the ones that did not.
	s.ticks = make(chan core.ScanTick, 1<<14)
	s.alerts = make(chan core.AlertMsg, 1<<16)
	s.entries = make(chan core.ScanEntry, 1<<16)
	r := &goldRun{t: t, s: s, gen: gen, ov: newGoldRand(goldSeed ^ 0xfeed), times: goldTimes(), stats: map[string]*goldStat{}, symIdx: map[string]int{}}
	for i := range gen.tk {
		r.allSyms = append(r.allSyms, gen.tk[i].sym)
		r.symIdx[gen.tk[i].sym] = i
	}
	return r
}

// drainCtrl runs the queued control ops on this goroutine, the way Run would.
func (r *goldRun) drainCtrl() {
	for len(r.s.ctrl) > 0 {
		(<-r.s.ctrl)(r.s)
	}
}

// upsert queues a def, draining first when the ctrl buffer is nearly full (the
// initial load queues more defs than it holds).
func (r *goldRun) upsert(d core.ScanDef) {
	if len(r.s.ctrl) >= 48 {
		r.drainCtrl()
	}
	r.s.Upsert(d)
}

// blocking runs a reply-style Scanner method (Resync, Row, TopMovers, ...) on a
// helper goroutine while this one plays the Run loop and executes the control
// ops it queues.
func (r *goldRun) blocking(fn func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	for {
		select {
		case f := <-r.s.ctrl:
			f(r.s)
		case <-done:
			r.drainCtrl()
			return
		}
	}
}

func (r *goldRun) stat(id string) *goldStat {
	st := r.stats[id]
	if st == nil {
		st = &goldStat{}
		r.stats[id] = st
	}
	return st
}

// --- overlay inputs --------------------------------------------------------

func (r *goldRun) randSym() string { return r.allSyms[r.ov.intn(len(r.allSyms))] }

// mixSym is a mover with probability p, else any ticker.
func (r *goldRun) mixSym(pool []string, p float64) string {
	if len(pool) > 0 && r.ov.chance(p) {
		return pool[r.ov.intn(len(pool))]
	}
	return r.randSym()
}

func (r *goldRun) px(sym string) float64 {
	if r.prev != nil {
		if i, ok := r.prev.Index[sym]; ok && r.prev.Rows[i].Last > 0 {
			return r.prev.Rows[i].Last
		}
	}
	return 10
}

// refreshPools rebuilds the candidate pools from the snapshot just processed.
func (r *goldRun) refreshPools() {
	r.movers, r.leaders, r.gappers = r.movers[:0], r.leaders[:0], r.gappers[:0]
	for i := range r.prev.Rows {
		row := &r.prev.Rows[i]
		if v := row.ChgDay; !core.IsNull(v) && math.Abs(v) > 3 {
			r.movers = append(r.movers, row.Sym)
		}
		if v := row.PmVolX; !core.IsNull(v) && v >= 1 {
			r.leaders = append(r.leaders, row.Sym)
		}
		if g := epGap(row); !core.IsNull(g) && g >= 9 && row.Last >= 4 {
			r.gappers = append(r.gappers, row.Sym)
		}
	}
}

func (r *goldRun) publishNews(asOf int64, n int) {
	items := make([]core.NewsItem, 0, n)
	for i := 0; i < n; i++ {
		var tickers []string
		for k := 1 + r.ov.intn(3); k > 0; k-- {
			tickers = append(tickers, r.mixSym(r.movers, 0.7))
		}
		if r.ov.chance(0.05) {
			tickers = append(tickers, "ZZNONE")
		}
		ts := asOf - int64(r.ov.cube(0, 3000))*1000
		switch r.ov.intn(14) {
		case 0:
			ts = asOf + 400_000 // published "in the future": ignored until the clock catches up
		case 1:
			ts = asOf - 30*3600*1000
		case 2:
			tickers = nil
		}
		items = append(items, core.NewsItem{
			ID: fmt.Sprintf("n%d-%d", asOf, i), Tickers: tickers,
			Headline: goldHeadlines[r.ov.intn(len(goldHeadlines))], TS: ts,
		})
	}
	r.s.MarkNews(items)
}

func (r *goldRun) shortMap() map[string]core.ShortRef {
	m := map[string]core.ShortRef{}
	for i := range r.allSyms {
		if !r.ov.chance(0.33) {
			continue
		}
		sym := r.allSyms[i]
		ref := core.ShortRef{Ticker: sym, Available: int64(math.Round(r.ov.cube(100, 5e6)/100) * 100),
			FeeRate: goldRound(r.ov.between(0.3, 120), 2), RebateRate: goldRound(r.ov.between(-5, 4), 2), AsOf: 1}
		switch r.ov.intn(12) {
		case 0, 1:
			ref.Available = 0
		case 2:
			ref.Available, ref.Capped = 10_000_000, true
		}
		m[sym] = ref
	}
	return m
}

func (r *goldRun) trendMap(n int) map[string]core.TrendRef {
	m := map[string]core.TrendRef{}
	for len(m) < n {
		ref := core.TrendRef{DTrend: r.ov.intn(3) - 1, Chg20d: goldRound(r.ov.between(-30, 60), 2), Days: 20, AsOf: "2026-10-19"}
		if r.ov.chance(0.05) {
			ref.Chg20d = core.Null
		}
		m[r.mixSym(r.movers, 0.3)] = ref
	}
	return m
}

func (r *goldRun) flagMap(n int) map[string]core.FlagRef {
	m := map[string]core.FlagRef{}
	for len(m) < n {
		sym := r.mixSym(r.movers, 0.4)
		ref := core.FlagRef{
			Score: goldRound(r.ov.between(0, 100), 1), State: float64(r.ov.intn(2)),
			PoleGain: goldRound(r.ov.between(8, 90), 2), RangePct: goldRound(r.ov.between(1, 12), 2),
			Retrace: goldRound(r.ov.between(5, 50), 2), VolDry: goldRound(r.ov.between(0.2, 1.4), 2),
			Bars: 3 + r.ov.intn(40), BaseHigh: r.px(sym) * r.ov.between(0.97, 1.05), AsOf: "2026-10-19",
		}
		if r.ov.chance(0.05) {
			ref.BaseHigh = 0
		}
		if r.ov.chance(0.1) {
			ref.State = 0
		}
		m[sym] = ref
	}
	return m
}

func (r *goldRun) emaMap(asOf int64) map[string]core.EmaFlagRef {
	nan := math.NaN()
	m := map[string]core.EmaFlagRef{}
	n := 25 + r.ov.intn(40)
	for len(m) < n {
		sym := r.mixSym(r.leaders, 0.75)
		px := r.px(sym)
		ref := core.EmaFlagRef{
			State: r.ov.intn(5), DistPct: goldRound(r.ov.between(-3, 5), 2), FlagBars: 2 + r.ov.intn(30),
			Trigger: goldPx(px * 1.01), Stop: goldPx(px * 0.97),
		}
		switch r.ov.intn(12) {
		case 0:
			ref.DistPct = nan
		case 1:
			ref.Trigger, ref.Stop = nan, nan
		case 2:
			ref.DistPct = math.Inf(1)
		}
		switch {
		case r.ov.chance(0.5):
			ref.SinceMs = asOf - int64(r.ov.cube(0, 1800))*1000
		case r.ov.chance(0.2):
			ref.SinceMs = asOf + 5000 // ahead of the snapshot: age clamps to 0
		}
		if r.ov.chance(0.35) {
			ref.SlamMs, ref.SlamPct = asOf-int64(r.ov.between(0, 400))*1000, goldRound(r.ov.between(-6, -1), 2)
			if r.ov.chance(0.1) {
				ref.SlamPct = nan
			}
		}
		if r.ov.chance(0.25) {
			ref.HodFlagMs = asOf - int64(r.ov.between(0, 400))*1000
		}
		if r.ov.chance(0.55) {
			ref.DildoBody, ref.DildoVolX = goldRound(r.ov.between(1.5, 15), 2), goldRound(r.ov.cube(1.2, 60), 1)
			ref.DildoBodyX, ref.DildoDvol, ref.DildoEarly = goldRound(r.ov.cube(1.5, 20), 1), math.Round(r.ov.cube(2e4, 3e6)), int8(r.ov.intn(2))
			switch r.ov.intn(10) {
			case 0:
				ref.DildoVolX = math.Inf(1)
			case 1:
				ref.DildoBody = nan
			}
			switch r.ov.intn(3) {
			case 0: // the very bar the engine saw: equal ages on both sides
				for _, i := range r.gen.sigIdx {
					if ms := r.gen.tk[i].ev[gsDildo]; ms > 0 && r.ov.chance(0.3) {
						sym, ref.DildoMs = r.gen.tk[i].sym, ms
						break
					}
				}
				if ref.DildoMs == 0 {
					ref.DildoMs = asOf - 90_000
				}
			case 1: // newer than the engine's
				ref.DildoMs = asOf - int64(r.ov.between(0, 300))*1000
			default: // older than any
				ref.DildoMs = asOf - 1500_000
			}
		}
		ref.PbState = r.ov.intn(6)
		if r.ov.chance(0.4) {
			ref.TouchMs = asOf - int64(r.ov.between(0, 500))*1000
		}
		if ref.PbState == 3 {
			ref.PbMs, ref.PbPx = asOf-int64(r.ov.between(0, 300))*1000, goldPx(px*1.004)
			if r.ov.chance(0.2) {
				ref.PbPx = nan
			}
		}
		ref.HotOnly = r.ov.chance(0.15)
		m[sym] = ref
	}
	return m
}

// publishEP gives the pre-market gappers an earnings headline and a daily
// context, the way the news poller and the EP worker do.
func (r *goldRun) publishEP(asOf int64) {
	var items []core.NewsItem
	refs := map[string]core.EPRef{}
	for k := 0; k < 18 && len(r.gappers) > 0; k++ {
		sym := r.gappers[r.ov.intn(len(r.gappers))]
		ts := asOf - int64(r.ov.between(0, 14*3600))*1000
		if r.ov.chance(0.15) {
			ts = asOf - 20*3600*1000 // before the previous 16:00: not a catalyst
		}
		items = append(items, core.NewsItem{ID: fmt.Sprintf("ep%d-%d", asOf, k), Tickers: []string{sym}, Headline: goldHeadlines[r.ov.intn(3)], TS: ts})
		ref := core.EPRef{Chg3m: goldRound(r.ov.between(-20, 25), 1), AdrPct: goldRound(r.ov.between(2, 14), 2), AdrDays: 5 + r.ov.intn(16)}
		if r.ov.chance(0.1) {
			ref.Chg3m = core.Null
		}
		if r.ov.chance(0.1) {
			ref.AdrPct = core.Null
		}
		refs[sym] = ref
	}
	r.s.MarkNews(items)
	r.s.SetEP(refs)
}

func (r *goldRun) seedHits(day string, ids ...string) {
	counts := map[string]map[string]int{}
	for _, id := range ids {
		m := map[string]int{}
		for k := 0; k < 30; k++ {
			m[r.mixSym(r.movers, 0.5)] = 1 + r.ov.intn(5)
		}
		counts[id] = m
	}
	r.s.SeedHits(day, counts)
}

// --- the scenario ----------------------------------------------------------

// before queues the control ops scheduled for this step; they run (drainCtrl)
// just ahead of the step's snapshot.
func (r *goldRun) before(step int) {
	asOf := r.times[step]
	day1, day2 := "2026-10-20", "2026-10-21"

	if step == 0 {
		for _, d := range goldLiveDefs(r.t) {
			r.upsert(d)
		}
		for _, d := range goldSyntheticDefs() {
			r.upsert(d)
		}
		// A restart's restored counters arrive before the first snapshot.
		r.seedHits(day1, "gold-hits-leaf", "gold-hits-and", "gold-hits-nested", "mom5", "no-such-scan")
		return
	}

	// Probe scans: one field each, a new field every step.
	for k := 0; k < 3; k++ {
		r.upsert(r.probe(k, step))
	}

	// Overlays: none for the first 30 steps (the empty-reference paths), then
	// refreshed on each overlay's own cadence.
	if step >= 30 {
		if step%7 == 2 {
			r.s.SetEmaFlags(r.emaMap(asOf))
		}
		if step%11 == 3 {
			r.publishNews(asOf, 3+r.ov.intn(6))
		}
		if step%15 == 5 {
			r.s.SetIntradayFlags(r.flagMap(250 + r.ov.intn(100)))
		}
	}
	switch step {
	case 30, 140, 300, 450, 600:
		r.s.SetShortable(r.shortMap())
	case 380:
		r.s.SetShortable(map[string]core.ShortRef{}) // an empty publish: swapped in, overlay off
	case 381:
		r.s.SetShortable(nil) // ignored
	}
	switch step {
	case 30, 250, 440, 580:
		r.s.SetDailyTrend(r.trendMap(600))
		r.s.SetDailyFlags(r.flagMap(500))
	case 251:
		r.s.SetDailyTrend(nil) // ignored
		r.s.SetDailyFlags(nil)
		r.s.SetIntradayFlags(nil)
	}
	switch step {
	case 60:
		r.publishNews(asOf, 40)
	case 61:
		r.s.MarkNews(nil)
		r.s.MarkNews([]core.NewsItem{})
	case 330:
		r.s.SetEmaFlags(nil) // the candidate list emptied: every e9 field Null again
	case 331:
		r.s.SetEmaFlags(map[string]core.EmaFlagRef{})
	}
	// Episodic-pivot inputs: pre-market and the first hour of both days.
	switch step {
	case 25, 60, 90, 130, 210, 458, 480, 520, 570, 600:
		r.publishEP(asOf)
	}

	// Scan changes.
	switch step {
	case 120:
		r.seedHits(day1, "gold-hits-leaf", "gold-hits-and", "gold-hits-sort", "mom5") // counters merge: larger wins
	case 150:
		r.upsert(goldAddDef())
	case 200:
		r.upsert(goldEditDef(1))
	case 240:
		for _, d := range goldLiveDefs(r.t) {
			if d.ID == "mom5" {
				r.upsert(d) // an unchanged re-save: still a reload
			}
		}
	case 260:
		r.upsert(goldToggleDef(true))
	case 300:
		r.s.Delete("pm-new-high")
	case 330:
		r.upsert(goldEditDef(2))
		r.upsert(goldToggleDef(false))
	case 350:
		for _, d := range goldLiveDefs(r.t) {
			if d.ID == "scan-1787336442850" {
				d.Limit = 20
				d.Columns = []string{"last", "chgDay", "rvol", "hits", "chart"}
				d.Sort = []core.SortKey{{Field: "hits", Dir: "desc"}, {Field: "chgDay", Dir: "desc"}}
				r.upsert(d)
			}
		}
	case 420:
		r.upsert(goldEditDef(3))
	case 436:
		// The ET date turned over (step 435); a counter set computed for the old
		// day is stale and must be dropped.
		r.seedHits(day1, "gold-hits-leaf", "mom5")
	case 470:
		r.seedHits(day2, "gold-hits-leaf", "gold-hits-and", "mom5")
	case 520:
		for _, d := range goldLiveDefs(r.t) {
			if d.ID == "pm-new-high" {
				r.upsert(d) // brought back: fresh state
			}
		}
	case 600:
		r.upsert(goldEditDef(4))
	}
}

// probe builds probe scan k for this step from the previous snapshot: its field
// tested against a threshold that only a few percent of rows clear (so a probe
// matches ~100 rows, not all 3000).
func (r *goldRun) probe(k, step int) core.ScanDef {
	field := goldProbeField(k, step)
	var f core.Filter
	switch field {
	case "news", "halted", "shortable":
		f = goldAnd(goldLeaf(field, "isTrue", nil), goldLeaf("chgDay", ">", 3.0))
	case "session":
		f = goldAnd(goldLeaf(field, "==", [...]string{"pre", "regular", "post", "closed"}[step%4]), goldLeaf("chgDay", ">", 6.0))
	case "secType":
		f = goldAnd(goldLeaf(field, "==", [...]string{"CS", "ETF", "ADRC", ""}[step%4]), goldLeaf("chgDay", ">", 6.0))
	case "hits":
		// Not from the previous snapshot: outside a scan's own evaluation a
		// row's Hits is whichever scan stamped it last (map order).
		f = goldLeaf(field, ">=", float64(1+step%3))
	case "epEarnHL":
		f = goldOr(goldLeaf(field, "in", []string{goldHeadlines[0], goldHeadlines[1], goldHeadlines[2]}),
			goldAnd(goldLeaf(field, "==", ""), goldLeaf("chgDay", ">", 12.0)))
	default:
		f = goldLeaf(field, ">", 1e300) // matches nothing: no data for the field yet
		acc, ok := goldAcc[field]
		if !ok || r.prev == nil {
			break
		}
		var vals []float64
		for i := 0; i < len(r.prev.Rows); i += 5 {
			if v, ok := acc(&r.prev.Rows[i]); ok && !math.IsInf(v, 0) {
				vals = append(vals, v)
			}
		}
		if len(vals) < 40 {
			break
		}
		sort.Float64s(vals)
		lo, hi := vals[len(vals)*3/100], vals[len(vals)*97/100]
		switch (step + k) % 3 {
		case 0:
			f = goldLeaf(field, ">", hi)
		case 1:
			f = goldLeaf(field, "<", lo)
		default:
			ge := 0
			for _, v := range vals {
				if v >= hi {
					ge++
				}
			}
			if ge*100 <= len(vals)*6 {
				f = goldLeaf(field, "between", []any{hi, 1e300})
			} else {
				f = goldLeaf(field, ">", hi)
			}
		}
	}
	return goldProbeDef(k, step, f)
}

// collect drains what the step's process() emitted into canonical records, in
// canonical order, and returns them with the number of rows the ticks carried.
func (r *goldRun) collect() ([]goldRec, int) {
	var ticks []core.ScanTick
	var alerts []core.AlertMsg
	var entries []core.ScanEntry
	for len(r.s.ticks) > 0 {
		ticks = append(ticks, <-r.s.ticks)
	}
	for len(r.s.alerts) > 0 {
		alerts = append(alerts, <-r.s.alerts)
	}
	for len(r.s.entries) > 0 {
		entries = append(entries, <-r.s.entries)
	}
	sort.SliceStable(ticks, func(i, j int) bool { return ticks[i].ID < ticks[j].ID })
	sort.SliceStable(alerts, func(i, j int) bool { return alerts[i].ScanID < alerts[j].ScanID })
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].ScanID < entries[j].ScanID })

	var recs []goldRec
	rows := 0
	for _, x := range ticks {
		st := r.stat(x.ID)
		st.ticks++
		st.rows += len(x.Rows)
		rows += len(x.Rows)
		recs = append(recs, goldRec{"tick", x.ID, goldCanon(nil, x)})
	}
	for _, x := range alerts {
		r.stat(x.ScanID).alerts++
		recs = append(recs, goldRec{"alert", x.ScanID + "/" + x.Sym, goldCanon(nil, x)})
	}
	for _, x := range entries {
		r.stat(x.ScanID).entries++
		recs = append(recs, goldRec{"entry", x.ScanID + "/" + x.Sym, goldCanon(nil, x)})
	}
	r.sum.ticks += len(ticks)
	r.sum.rows += rows
	r.sum.alerts += len(alerts)
	r.sum.entries += len(entries)
	var counts any
	if c := r.s.Counts(); c != nil {
		m := make(map[string]any, len(c))
		for id, n := range c {
			m[id] = n
			if st := r.stat(id); n > st.maxCount {
				st.maxCount = n
			}
		}
		counts = m
	}
	recs = append(recs, goldRec{"counts", "", goldCanon(nil, counts)})
	return recs, rows
}

// checkpoint is the every-25-steps battery: a resync of every scan (and of an
// unknown id), Row for a few symbols, TopMovers, and the introspection calls.
func (r *goldRun) checkpoint() []goldRec {
	var recs []goldRec
	add := func(key string, b []byte) { recs = append(recs, goldRec{"misc", key, b}) }

	var defs []core.ScanDef
	r.blocking(func() { defs = r.s.List() })
	var list [][]byte
	for _, d := range defs {
		list = append(list, goldCanon(nil, map[string]any{"id": d.ID, "name": d.Name, "enabled": d.Enabled, "limit": d.Limit}))
	}
	add("list", goldArr(list...))

	ids := make([]string, 0, len(defs)+1)
	for _, d := range defs {
		ids = append(ids, d.ID)
	}
	ids = append(ids, "no-such-scan")
	for _, id := range ids {
		var tk core.ScanTick
		var ok bool
		r.blocking(func() { tk, ok = r.s.Resync(id) })
		r.sum.resyncRows += len(tk.Rows)
		r.stat(id).fullRows += len(tk.Rows)
		add("resync/"+id, goldArr(goldCanon(nil, ok), goldCanon(nil, tk)))
	}

	rows := r.prev.Rows
	for _, sym := range []string{rows[0].Sym, rows[len(rows)/3].Sym, rows[2*len(rows)/3].Sym, r.leaders0(), "zz-none"} {
		var row core.Row
		var ok bool
		r.blocking(func() { row, ok = r.s.Row(sym) })
		add("row/"+sym, goldArr(goldCanon(nil, ok), goldRowText(nil, row)))
	}
	for _, p := range []struct {
		n      int
		minVol int64
		minPx  float64
	}{{10, 1000, 1}, {3, 0, 0}, {25, 200_000, 5}} {
		var gain, loss []core.Row
		r.blocking(func() { gain, loss = r.s.TopMovers(p.n, p.minVol, p.minPx) })
		var sides [][]byte
		for _, side := range [][]core.Row{gain, loss} {
			var one [][]byte
			for _, row := range side {
				one = append(one, goldRowText(nil, row))
			}
			sides = append(sides, goldArr(one...))
		}
		add(fmt.Sprintf("movers/%d/%d/%g", p.n, p.minVol, p.minPx), goldArr(sides...))
	}

	var active map[string]struct{}
	r.blocking(func() { active = r.s.ActiveSyms() })
	syms := make([]string, 0, len(active))
	for s := range active {
		syms = append(syms, s)
	}
	sort.Strings(syms)
	add("active", goldCanon(nil, fmt.Sprint(len(syms), " ", strings.Join(syms, ","))))

	var misc [][]byte
	for _, sym := range []string{rows[0].Sym, rows[len(rows)/2].Sym, r.leaders0(), "zz-none"} {
		var pri bool
		var fr core.FlagRef
		var fok bool
		r.blocking(func() { pri = r.s.IsPriority(sym); fr, fok = r.s.FlagFor(sym) })
		misc = append(misc, goldArr(goldCanon(nil, sym), goldCanon(nil, pri), goldCanon(nil, fok), goldCanon(nil, fr)))
	}
	for _, id := range []string{"gold-edit", "ep-watch", "flags-intraday", "gold-cols-all", "no-such-scan"} {
		var uses bool
		r.blocking(func() { uses = r.s.ScanUsesFlags(id) })
		misc = append(misc, goldArr(goldCanon(nil, id), goldCanon(nil, uses)))
	}
	var cands []string
	r.blocking(func() { cands = r.s.EPCandidates(15) })
	misc = append(misc, goldCanon(nil, cands))
	add("introspect", goldArr(misc...))

	sort.SliceStable(recs, func(i, j int) bool { return recs[i].key < recs[j].key })
	return recs
}

// leaders0 is the first pre-market volume leader (or any symbol).
func (r *goldRun) leaders0() string {
	if len(r.leaders) > 0 {
		return r.leaders[0]
	}
	return r.allSyms[7]
}

// run plays the whole scenario and returns the golden lines.
func (r *goldRun) run() []string {
	dump := goldOpenDump()
	defer dump.close()
	lines := make([]string, 0, goldSteps+3)
	for step := 0; step < goldSteps; step++ {
		r.before(step)
		r.drainCtrl()
		snap := r.gen.Next(r.times[step])
		r.s.process(snap)
		r.prev = snap
		r.refreshPools()
		recs, rows := r.collect()
		if step%25 == 24 {
			recs = append(recs, r.checkpoint()...)
		}
		sum := goldSummarise(recs)
		sum.rows = rows
		lines = append(lines, goldLine(step, snap.AsOf, sum))
		dump.step(step, recs)
	}
	return lines
}

func TestGoldenScan(t *testing.T) {
	r := newGoldRun(t)
	lines := r.run()
	if os.Getenv("GOLDEN_STATS") != "" {
		r.logStats(t)
	}
	t.Logf("golden: %d steps, %d ticks carrying %d rows, %d alerts, %d entries, %d resync rows", len(lines), r.sum.ticks, r.sum.rows, r.sum.alerts, r.sum.entries, r.sum.resyncRows)

	header := fmt.Sprintf("%s rows=%d steps=%d seed=%#x", goldHeader, goldRows, goldSteps, goldSeed)
	got := append([]string{header}, lines...)
	if *goldUpdate {
		if err := os.WriteFile(goldPath, []byte(strings.Join(got, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d lines)", goldPath, len(got))
		return
	}
	raw, err := os.ReadFile(goldPath)
	if err != nil {
		t.Fatalf("%v\nrecord the golden from the reference code first: go test ./internal/scan -run TestGolden -update-golden", err)
	}
	want := strings.Split(strings.TrimRight(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n"), "\n")
	for i := 0; i < len(want) && i < len(got); i++ {
		if want[i] == got[i] {
			continue
		}
		t.Errorf("golden mismatch at line %d (%s):\n  want %s\n  got  %s\n%s", i+1, goldStepName(i), want[i], got[i], goldDiffTokens(want[i], got[i]))
		return
	}
	if len(want) != len(got) {
		t.Fatalf("golden has %d lines, this run produced %d", len(want), len(got))
	}
}

func goldStepName(line int) string {
	if line == 0 {
		return "header"
	}
	return fmt.Sprintf("step %d", line-1)
}

// goldDiffTokens names the fields of two golden lines that differ.
func goldDiffTokens(want, got string) string {
	w, g := strings.Fields(want), strings.Fields(got)
	var b strings.Builder
	for i := 0; i < len(w) && i < len(g); i++ {
		if w[i] != g[i] {
			k, _, _ := strings.Cut(w[i], "=")
			fmt.Fprintf(&b, "  differs: %s\n", k)
		}
	}
	b.WriteString("  to see what changed: GOLDEN_DUMP=<dir> on the reference and on this code, then diff the scan_steps.jsonl files")
	return b.String()
}

func (r *goldRun) logStats(t testing.TB) {
	ids := make([]string, 0, len(r.stats))
	for id := range r.stats {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	fmt.Fprintf(&b, "%-26s %7s %8s %7s %7s %6s %9s\n", "scan", "ticks", "tickrows", "alerts", "entries", "maxN", "resyncrows")
	for _, id := range ids {
		s := r.stats[id]
		fmt.Fprintf(&b, "%-26s %7d %8d %7d %7d %6d %9d\n", id, s.ticks, s.rows, s.alerts, s.entries, s.maxCount, s.fullRows)
	}
	t.Log("\n" + b.String())
}
