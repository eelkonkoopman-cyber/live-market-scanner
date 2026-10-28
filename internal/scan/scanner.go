// Package scan evaluates user scans against engine snapshots: it compiles each
// ScanDef's filter, sorts and caps the matches, diffs against the previous tick
// to emit minimal scan.tick frames, and runs the alert engine (enter / cross
// with cooldown + require-reentry). It implements core.Scanner. It imports core
// (and engine only in tests); it has no I/O.
package scan

import (
	"context"
	"math"
	"sort"
	"sync/atomic"
	"time"
	_ "time/tzdata" // embed tz DB so America/New_York resolves on Windows

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// scanState is the per-scan mutable state, owned solely by the Run goroutine
// (and, in tests, the single test goroutine).
type scanState struct {
	def  core.ScanDef
	pred predicate
	seq  uint64

	prevRows map[string]core.Row // last emitted row per sym (diff)
	prevSet  map[string]struct{} // membership last tick (enter/exit)
	// prevOrder is the Order of the last tick actually emitted (nil = none
	// since the last (re)load). An evaluation that changes no row and keeps
	// the same order emits nothing: at 10 Hz across ~30 scans that was ~250
	// frames a second carrying only a repeated symbol list, each one a store
	// update and a render in every window viewing the scan.
	prevOrder []string

	// Recurrence tracking. Hits are counted against basePred — the filter
	// MINUS any hits conditions — not against the displayed set: a
	// "hits >= 2" scan counting its own matches could never accrue a first
	// hit (the symbol is filtered out at 1, so it never enters, so it never
	// reaches 2). basePred == pred when the filter doesn't mention hits.
	basePred predicate
	baseSet  map[string]struct{} // basePred membership last tick
	hits     map[string]int      // sym -> base-criteria entries today (ET); cleared at rollover
	armed    map[string]bool     // enter hysteresis: must leave before re-firing
	cooldown map[string]int64    // sym -> next allowed fire (ms)
	baseline bool                // first eval after (re)load: suppress entries

	// compiled cross trigger
	crossField string
	crossCmp   string
	crossThr   float64
	prevVal    map[string]float64 // last value of crossField per sym

	// Resolved from def at upsert: whether the filter reads hits (then every
	// row is stamped before the filter runs, else only the matched ones),
	// the sort keys, and the stream / alert column lists.
	usesHits   bool
	sortKeys   []core.SortKey
	streamCols []string
	alertCols  []string

	// Scratch reused across evaluations (owned by the Run goroutine): match's
	// entries and result, and the spare membership maps the next tick fills.
	ents      []sortEnt
	matched   []*core.Row
	spareSet  map[string]struct{}
	spareBase map[string]struct{}
}

// snapshotRecycler is implemented by an engine that can reuse the buffers of
// snapshots the scanner is done with (engine.Engine).
type snapshotRecycler interface {
	Recycle(*core.MetricSnapshot)
}

// Scanner implements core.Scanner.
type Scanner struct {
	eng      core.Engine
	recycle  func(*core.MetricSnapshot) // the engine's Recycle, or nil
	ticks    chan core.ScanTick
	alerts   chan core.AlertMsg
	entries  chan core.ScanEntry
	ctrl     chan func(*Scanner)
	scans    map[string]*scanState
	last     *core.MetricSnapshot
	newsTS   map[string]int64           // sym -> newest headline ms (news overlay)
	shortRef map[string]core.ShortRef   // IBKR borrow availability (15-min refresh)
	trendRef map[string]core.TrendRef   // daily-chart trend (nightly refresh)
	flagRef  map[string]core.FlagRef    // bull-flag / consolidation setup (nightly refresh)
	iflagRef map[string]core.FlagRef    // intraday bull-flag setup (~60s worker refresh)
	e9Ref    map[string]core.EmaFlagRef // 9EMA flag verdict per pre-market volume leader (flagscan, ~1s refresh)
	ep       epState                    // Episodic Pivot overlay (ep.go)

	// Each overlay table's entries per snapshot row (overlayRows), and the
	// table versions they are keyed on: every setter bumps its table's version.
	newsVer, shortVer, trendVer, flagVer, iflagVer, e9Ver uint64
	newsRows                                              overlayRows[int64]
	shortRows                                             overlayRows[core.ShortRef]
	trendRows                                             overlayRows[core.TrendRef]
	flagRows, iflagRows                                   overlayRows[core.FlagRef]
	e9Rows                                                overlayRows[core.EmaFlagRef]
	shortAvail                                            []int64 // shortRows' Available per row: the rows' ShortAvail point into it

	// hitsDay is the ET date the per-scan hits counters belong to; when a
	// snapshot's AsOf crosses into a new ET day, every scan's counters clear.
	hitsDay string
	etLoc   *time.Location

	// counts is the current match count per scan id, republished after every
	// evaluation pass. The UI badges every tab from this, so a scan that is
	// matching nothing is visible without subscribing to all their tick
	// streams. Written only by the Run goroutine; read lock-free.
	counts atomic.Pointer[map[string]int]
}

// newsFreshSec: a row's News flag is true while its latest headline is at most
// this old; NewsAge stays filterable well beyond it.
const newsFreshSec = 1800

var _ core.Scanner = (*Scanner)(nil)

func New(eng core.Engine) *Scanner {
	// UTC fallback only shifts WHEN the daily hits reset lands, never breaks it.
	etLoc, err := time.LoadLocation("America/New_York")
	if err != nil {
		etLoc = time.UTC
	}
	var recycle func(*core.MetricSnapshot)
	if r, ok := eng.(snapshotRecycler); ok {
		recycle = r.Recycle
	}
	return &Scanner{
		eng:     eng,
		recycle: recycle,
		ticks:   make(chan core.ScanTick, 256),
		alerts:  make(chan core.AlertMsg, 256),
		entries: make(chan core.ScanEntry, 256),
		ctrl:    make(chan func(*Scanner), 64),
		scans:   make(map[string]*scanState),
		newsTS:  make(map[string]int64),
		ep:      newEPState(),
		etLoc:   etLoc,
	}
}

func (s *Scanner) Ticks() <-chan core.ScanTick    { return s.ticks }
func (s *Scanner) Alerts() <-chan core.AlertMsg   { return s.alerts }
func (s *Scanner) Entries() <-chan core.ScanEntry { return s.entries }

// Run consumes engine snapshots and processes control ops, all on one goroutine
// (single writer for scan state).
func (s *Scanner) Run(ctx context.Context) error {
	snaps := s.eng.Snapshots()
	for {
		select {
		case <-ctx.Done():
			return nil
		case fn := <-s.ctrl:
			fn(s)
		case snap := <-snaps:
			s.process(snap)
		}
	}
}

// --- public API (routed through ctrl for thread-safety with Run) ---

func (s *Scanner) Upsert(def core.ScanDef) { s.ctrl <- func(sc *Scanner) { sc.upsert(def) } }
func (s *Scanner) Delete(id string)        { s.ctrl <- func(sc *Scanner) { delete(sc.scans, id) } }

// SeedHits restores per-scan re-entry counters from the archive after a
// restart (counts[scanID][sym] = entries recorded on `day`, an ET date in
// 2006-01-02 form). A seed for a different day than the counters' current day
// is dropped — it was computed before an ET midnight crossed. Existing
// in-memory counters win when larger — the seed races the first evaluations.
func (s *Scanner) SeedHits(day string, counts map[string]map[string]int) {
	s.ctrl <- func(sc *Scanner) {
		if sc.hitsDay == "" {
			sc.hitsDay = day
		} else if sc.hitsDay != day {
			return // stale: computed for a previous ET day
		}
		for id, syms := range counts {
			st, ok := sc.scans[id]
			if !ok {
				continue
			}
			for sym, n := range syms {
				if n > st.hits[sym] {
					st.hits[sym] = n
				}
			}
		}
	}
}

// Counts returns the latest match count per scan id. It is a snapshot read off
// an atomic pointer, so it is safe to call from the status broadcaster every
// second without going through ctrl (which would stall on a busy Run loop).
func (s *Scanner) Counts() map[string]int {
	if p := s.counts.Load(); p != nil {
		return *p
	}
	return nil
}

func (s *Scanner) List() []core.ScanDef {
	reply := make(chan []core.ScanDef, 1)
	s.ctrl <- func(sc *Scanner) {
		out := make([]core.ScanDef, 0, len(sc.scans))
		for _, st := range sc.scans {
			out = append(out, st.def)
		}
		// Map iteration is randomised, so without this the tab strip reshuffles
		// on every reload and the UI's default selection (first enabled scan)
		// lands on a different scan each time.
		sort.Slice(out, func(i, j int) bool {
			if out[i].Name != out[j].Name {
				return out[i].Name < out[j].Name
			}
			return out[i].ID < out[j].ID
		})
		reply <- out
	}
	return <-reply
}

func (s *Scanner) Resync(id string) (core.ScanTick, bool) {
	type res struct {
		tick core.ScanTick
		ok   bool
	}
	reply := make(chan res, 1)
	s.ctrl <- func(sc *Scanner) {
		t, ok := sc.resync(id)
		reply <- res{t, ok}
	}
	r := <-reply
	return r.tick, r.ok
}

// Row returns the latest merged metrics for one symbol (price levels for the
// tape read's context). Routed through ctrl so it reads the snapshot on the
// owning goroutine.
func (s *Scanner) Row(sym string) (core.Row, bool) {
	type res struct {
		row core.Row
		ok  bool
	}
	reply := make(chan res, 1)
	s.ctrl <- func(sc *Scanner) {
		if sc.last == nil {
			reply <- res{}
			return
		}
		if i, ok := sc.last.Index[sym]; ok && i < len(sc.last.Rows) {
			reply <- res{sc.last.Rows[i], true}
			return
		}
		reply <- res{}
	}
	r := <-reply
	return r.row, r.ok
}

// TopMovers returns the n biggest gainers and the n biggest losers of the day
// (ChgDay, % vs previous close) in the scanner's latest snapshot, biggest first,
// among rows with at least minVol shares traded and a price of at least
// minPrice (so a one-lot print on a dead name does not top the list). Routed
// through ctrl like Row, so it reads the snapshot on the scanner goroutine.
func (s *Scanner) TopMovers(n int, minVol int64, minPrice float64) (gain, loss []core.Row) {
	type res struct{ g, l []core.Row }
	reply := make(chan res, 1)
	s.ctrl <- func(sc *Scanner) {
		if sc.last == nil {
			reply <- res{}
			return
		}
		var ok []core.Row
		for i := range sc.last.Rows {
			r := &sc.last.Rows[i]
			if core.IsNull(r.ChgDay) || r.Vol < minVol || r.Last < minPrice {
				continue
			}
			ok = append(ok, *r)
		}
		sort.SliceStable(ok, func(i, j int) bool { return ok[i].ChgDay > ok[j].ChgDay })
		var out res
		for i := 0; i < len(ok) && i < n && ok[i].ChgDay > 0; i++ {
			out.g = append(out.g, ok[i])
		}
		for i := len(ok) - 1; i >= 0 && len(out.l) < n && ok[i].ChgDay < 0; i-- {
			out.l = append(out.l, ok[i])
		}
		reply <- out
	}
	r := <-reply
	return r.g, r.l
}

// ScanUsesFlags reports whether the scan with this id filters on any of the
// nightly bull-flag fields (see filterUsesFlags). It answers "was this entry
// produced by a scan the user built around the flag geometry", which is the
// FIRST and cheapest gate on the Claude setup reader — an entry from a scan
// that never mentions those fields is dropped before any row lookup, bar query
// or budget is touched. Routed through ctrl like Row/IsPriority so it reads
// scan state on the owning goroutine.
func (s *Scanner) ScanUsesFlags(id string) bool {
	reply := make(chan bool, 1)
	s.ctrl <- func(sc *Scanner) { reply <- sc.scanUsesFlags(id) }
	return <-reply
}

// scanUsesFlags is the actual check, factored out of the ctrl-channel wrapper
// so it's directly callable from tests (same convention as isPriority above).
// An unknown scan id is false: no filter, no flag intent.
func (s *Scanner) scanUsesFlags(id string) bool {
	st, ok := s.scans[id]
	if !ok {
		return false
	}
	return filterUsesFlags(st.def.Filter)
}

// ActiveSyms returns the symbols the user's scans have actually surfaced
// today: the union, across ALL scans, of current membership (prevSet) and
// every symbol that entered at least once today (hits keys). This is the gate
// for the all-filings EDGAR watcher — a filing only matters if some scan put
// the name in front of the user. Scans whose defs are disabled still
// contribute their hits history (deliberate: a name that entered earlier
// today remains relevant even if its scan was toggled off). The returned map
// is a fresh copy per call, so a caller may hold and mutate it across a whole
// poll cycle without touching scan state. Routed through ctrl like
// Row/IsPriority so it reads scan state on the owning goroutine.
func (s *Scanner) ActiveSyms() map[string]struct{} {
	reply := make(chan map[string]struct{}, 1)
	s.ctrl <- func(sc *Scanner) { reply <- sc.activeSyms() }
	return <-reply
}

// activeSyms builds the actual union, factored out of the ctrl-channel
// wrapper so it's directly callable from tests (same convention as
// isPriority/scanUsesFlags). Always a non-nil map, even with no scans.
func (s *Scanner) activeSyms() map[string]struct{} {
	out := make(map[string]struct{})
	for _, st := range s.scans {
		for sym := range st.prevSet {
			out[sym] = struct{}{}
		}
		for sym := range st.hits {
			out[sym] = struct{}{}
		}
	}
	return out
}

// FlagFor returns the stored nightly flag geometry for one symbol. The Row
// overlay copies only the seven filterable numbers onto rows, deliberately not
// BaseHigh or AsOf, so a consumer that needs the WHOLE classification — the
// setup reader needs AsOf as its cache key and BaseHigh as the level to
// describe — asks for the reference itself. Routed through ctrl (same reason
// as Row): flagRef is hot-swapped nightly by SetDailyFlags on that goroutine.
func (s *Scanner) FlagFor(sym string) (core.FlagRef, bool) {
	type res struct {
		ref core.FlagRef
		ok  bool
	}
	reply := make(chan res, 1)
	s.ctrl <- func(sc *Scanner) {
		fr, ok := sc.flagRef[sym]
		reply <- res{fr, ok}
	}
	r := <-reply
	return r.ref, r.ok
}

// IsPriority reports whether sym's current price is under the largest price
// ceiling configured on any ENABLED scan (see maxLastCeiling) — used to
// reorder rate-limited external fetches (e.g. RTPR article fetches during a
// burst of simultaneous releases) so tickers a user's fast/cheap-stock scans
// care about aren't stuck behind irrelevant noise. No scan defining a price
// ceiling means priority is off (every ticker is equal, matching the caller's
// default order) rather than everyone being flagged priority.
func (s *Scanner) IsPriority(sym string) bool {
	reply := make(chan bool, 1)
	s.ctrl <- func(sc *Scanner) { reply <- sc.isPriority(sym) }
	return <-reply
}

// isPriority is the actual check, factored out of the ctrl-channel wrapper so
// it's directly callable from tests (same convention as process()/upsert()
// in this file — no need to spin up Run() and a fake engine just to drain
// ctrl for a synchronous lookup).
func (s *Scanner) isPriority(sym string) bool {
	ceiling := s.priceCeiling()
	if ceiling <= 0 || s.last == nil {
		return false
	}
	i, ok := s.last.Index[sym]
	if !ok || i >= len(s.last.Rows) {
		return false
	}
	last := s.last.Rows[i].Last
	return !core.IsNull(last) && last < ceiling
}

// priceCeiling returns the largest price ceiling among enabled scans' filter
// trees, or 0 if none defines one.
func (s *Scanner) priceCeiling() float64 {
	var ceiling float64
	for _, st := range s.scans {
		if !st.def.Enabled {
			continue
		}
		if v, ok := maxLastCeiling(st.def.Filter); ok && v > ceiling {
			ceiling = v
		}
	}
	return ceiling
}

// RowJSON returns sym's current row projected over cols, wire-ready — the
// same shape a scan tick's Rows carries. Used by a pop-out tape window (no
// scan tiled) to get header stats for its watched symbol on demand.
func (s *Scanner) RowJSON(sym string, cols []string) (core.RowJSON, bool) {
	row, ok := s.Row(sym)
	if !ok {
		return nil, false
	}
	return rowJSON(&row, cols), true
}

// LoadAll bulk-loads persisted scans at startup.
func (s *Scanner) LoadAll(defs []core.ScanDef) {
	for _, d := range defs {
		s.Upsert(d)
	}
}

// MarkNews records fresh headlines so rows carry News/NewsAge — this is what
// makes "has news" filters and the news column light up for scanner symbols.
func (s *Scanner) MarkNews(items []core.NewsItem) {
	if len(items) == 0 {
		return
	}
	s.ctrl <- func(sc *Scanner) {
		sc.markEPNews(items)
		for _, it := range items {
			for _, sym := range it.Tickers {
				if it.TS > sc.newsTS[sym] {
					sc.newsTS[sym] = it.TS
					sc.newsVer++
				}
			}
		}
		// Bound memory: drop stale entries once the map grows large.
		if len(sc.newsTS) > 20000 {
			cutoff := time.Now().Add(-24 * time.Hour).UnixMilli()
			for sym, ts := range sc.newsTS {
				if ts < cutoff {
					delete(sc.newsTS, sym)
					sc.newsVer++
				}
			}
		}
	}
}

// SetShortable swaps in a fresh IBKR short-availability map (every ~15 min).
// SetDailyTrend hot-swaps the daily-chart trend reference (nightly recompute).
func (s *Scanner) SetDailyTrend(m map[string]core.TrendRef) {
	if m == nil {
		return
	}
	s.ctrl <- func(sc *Scanner) { sc.trendRef, sc.trendVer = m, sc.trendVer+1 }
}

// overlayTrend stamps daily-trend context onto snapshot rows. It runs
// UNCONDITIONALLY: rows arrive from the engine with zero-valued fields, and a
// zero DTrend means "flat" — absent symbols must be explicitly Nulled or every
// unknown ticker would read as a flat daily chart.
func (s *Scanner) overlayTrend(snap *core.MetricSnapshot) {
	refs, has, _ := s.trendRows.rows(snap, s.trendVer, s.trendRef)
	for i := range snap.Rows {
		r := &snap.Rows[i]
		if tr, ok := refs[i], has[i]; ok {
			r.DTrend, r.Chg20d = float64(tr.DTrend), tr.Chg20d
		} else {
			r.DTrend, r.Chg20d = core.Null, core.Null
		}
	}
}

// SetDailyFlags hot-swaps the bull-flag reference (nightly recompute), on the
// same cadence and through the same ctrl channel as SetDailyTrend so the Run
// goroutine stays the single writer.
func (s *Scanner) SetDailyFlags(m map[string]core.FlagRef) {
	if m == nil {
		return
	}
	s.ctrl <- func(sc *Scanner) { sc.flagRef, sc.flagVer = m, sc.flagVer+1 }
}

// SetIntradayFlags hot-swaps the INTRADAY bull-flag reference — the same
// FlagRef geometry as SetDailyFlags but measured over today's session at
// 5-minute-bucket resolution by the ~60s worker loop, held in its own map so a
// daily-flag scan and an intraday-flag scan run side by side without
// cross-talk. Same ctrl-channel route so the Run goroutine stays the single
// writer.
func (s *Scanner) SetIntradayFlags(m map[string]core.FlagRef) {
	if m == nil {
		return
	}
	s.ctrl <- func(sc *Scanner) { sc.iflagRef, sc.iflagVer = m, sc.iflagVer+1 }
}

// SetEmaFlags hot-swaps the 9EMA-flag reference: the live verdict (state,
// distance to the 9 EMA, trigger, stop, flag length, state-entry time, the
// day's latest 9 EMA SLAM) the flagscan service computes for each pre-market
// volume leader and republishes about once a second. Same ctrl-channel route
// as SetIntradayFlags, so the Run
// goroutine stays the single writer and a publish is picked up by the very next
// snapshot.
//
// Unlike the nightly/intraday setters, a nil or empty map is NOT ignored: it is
// a real publish meaning "no candidate is being evaluated right now" (the last
// volume leader dropped out, or the service is shutting down), and ignoring it
// would leave the previous verdicts -- BREAKOUT states and their alerts -- on
// screen forever. It clears every e9* field. The scanner keeps the map as
// given, so the publisher must hand over a map it never mutates again (the
// service builds a fresh one per publish, as the other setters' callers do).
func (s *Scanner) SetEmaFlags(m map[string]core.EmaFlagRef) {
	s.ctrl <- func(sc *Scanner) { sc.e9Ref, sc.e9Ver = m, sc.e9Ver+1 }
}

// finiteOrNull maps a published level to a row value: NaN (the emaflag
// package's "no such level") and, defensively, +-Inf become Null. Inf matters
// as much as NaN here -- IsNull only recognises NaN, and encoding/json refuses
// to marshal an Inf, which would fail the WHOLE scan tick frame for one bad
// level on one symbol.
func finiteOrNull(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return core.Null
	}
	return v
}

// overlayFlags stamps the stored bull-flag geometry onto snapshot rows — BOTH
// the nightly daily-bar set (FlagScore..FlagBars) and the ~60s intraday
// 5-minute-bucket set (IFlagScore..IFlagBars), plus the live 9EMA-flag verdict
// (E9State..E9Age), in one pass over the snapshot —
// and resolves the one part of each stored set that can't wait for a
// recompute: the breakout.
//
// Like overlayTrend it runs UNCONDITIONALLY. Rows arrive from the engine with
// zero-valued fields, and zero is a MEANINGFUL value for two of these: a zero
// FlagScore would read as a real "we measured this setup and it scored 0", and
// worse, a zero FlagState would read as a real "no pattern here" verdict for a
// symbol we simply have no pattern data on. Absent symbols must therefore be
// explicitly Nulled — null fails every numeric comparison, so an unclassified
// ticker is excluded by `flagScore > 70` AND by `flagState == 0` alike (and
// identically for the iflag* fields).
//
// The State flip is the "stored geometry, live breakout" design: the pattern
// pass measures the base out of band and hands us BaseHigh, and all the live
// path does is one O(1) float compare per row per set to promote a coiling
// base (1) to a breakout (2) the moment price clears that level. No geometry,
// no candle rebuilding, nothing that could not run at tick rate — the actual
// measurement stays on its own cadence (nightly for daily flags, ~60s for
// intraday). A stored State of 0 is never promoted: without a base there is
// no level worth breaking out of. Each set promotes against ITS OWN stored
// BaseHigh — the daily base and the intraday base are different levels, and a
// symbol carrying both references breaks out of each independently.
//
// The 9EMA-flag fields differ in kind: the flagscan service already resolved
// the state machine (including the breakout) on 1-minute bars, so there is
// nothing to promote here -- the overlay only copies the verdict. Presence in
// the published map IS the "evaluated" bit: a symbol in it carries a real
// state (0 = evaluated, idle) and a symbol absent from it is not a candidate,
// so every e9* field is Null for it -- never a 0 that `e9State == 0` would
// read as "evaluated, no setup". NaN levels (no trigger/stop yet, EMA not
// seeded) become Null. E9Age counts from the ref's SinceMs to the SNAPSHOT's
// clock (AsOf, the engine tick time) rather than wall time, so it is
// consistent with every other age field on the row, ticks up between the
// service's publishes, and is deterministic under a test clock; a ref with no
// SinceMs (idle) has no age, and a SinceMs ahead of the snapshot (a publish
// that raced the tick) clamps to 0. The day's latest 9 EMA SLAM follows the
// same rules: E9SlamAge counts from the ref's SlamMs (the slam bar's start) to
// AsOf, and a candidate with no slam today (SlamMs 0) is Null on both slam
// fields -- never a 0 that `e9SlamAge <= 300` would read as a fresh slam.
func (s *Scanner) overlayFlags(snap *core.MetricSnapshot) {
	flags, hasFlag, _ := s.flagRows.rows(snap, s.flagVer, s.flagRef)
	iflags, hasIflag, _ := s.iflagRows.rows(snap, s.iflagVer, s.iflagRef)
	e9s, hasE9, _ := s.e9Rows.rows(snap, s.e9Ver, s.e9Ref)
	for i := range snap.Rows {
		r := &snap.Rows[i]
		if fr, ok := flags[i], hasFlag[i]; ok {
			state := fr.State
			if state == 1 && !core.IsNull(r.Last) && r.Last > fr.BaseHigh {
				state = 2 // daily base cleared intraday
			}
			r.FlagScore, r.FlagState = fr.Score, state
			r.FlagPole, r.FlagRange, r.FlagRetrace = fr.PoleGain, fr.RangePct, fr.Retrace
			r.FlagVolDry, r.FlagBars = fr.VolDry, float64(fr.Bars)
		} else {
			r.FlagScore, r.FlagState = core.Null, core.Null
			r.FlagPole, r.FlagRange, r.FlagRetrace = core.Null, core.Null, core.Null
			r.FlagVolDry, r.FlagBars = core.Null, core.Null
		}
		if fr, ok := iflags[i], hasIflag[i]; ok {
			state := fr.State
			if state == 1 && !core.IsNull(r.Last) && r.Last > fr.BaseHigh {
				state = 2 // intraday base cleared
			}
			r.IFlagScore, r.IFlagState = fr.Score, state
			r.IFlagPole, r.IFlagRange, r.IFlagRetrace = fr.PoleGain, fr.RangePct, fr.Retrace
			r.IFlagVolDry, r.IFlagBars = fr.VolDry, float64(fr.Bars)
		} else {
			r.IFlagScore, r.IFlagState = core.Null, core.Null
			r.IFlagPole, r.IFlagRange, r.IFlagRetrace = core.Null, core.Null, core.Null
			r.IFlagVolDry, r.IFlagBars = core.Null, core.Null
		}
		er, ok := e9s[i], hasE9[i]
		if ok && !er.HotOnly {
			r.E9State = float64(er.State)
			r.E9Dist = finiteOrNull(er.DistPct)
			r.E9Trigger, r.E9Stop = finiteOrNull(er.Trigger), finiteOrNull(er.Stop)
			r.E9FlagBars = float64(er.FlagBars)
			r.E9Age = core.Null
			if er.SinceMs > 0 {
				r.E9Age = float64(max(snap.AsOf-er.SinceMs, 0) / 1000)
			}
			r.E9SlamAge, r.E9SlamPct = core.Null, core.Null
			if er.SlamMs > 0 {
				r.E9SlamAge = float64(max(snap.AsOf-er.SlamMs, 0) / 1000)
				r.E9SlamPct = finiteOrNull(er.SlamPct)
			}
			r.E9HodFlagAge = core.Null
			if er.HodFlagMs > 0 {
				r.E9HodFlagAge = float64(max(snap.AsOf-er.HodFlagMs, 0) / 1000)
			}
		} else {
			// Not a candidate - or a HOT-ONLY ref (tracked only for its green
			// dildo): every e9* field stays Null, so a user-built e9 scan without
			// a pmVolX term never starts showing dildo names.
			r.E9State, r.E9Dist = core.Null, core.Null
			r.E9Trigger, r.E9Stop = core.Null, core.Null
			r.E9FlagBars, r.E9Age = core.Null, core.Null
			r.E9SlamAge, r.E9SlamPct = core.Null, core.Null
			r.E9HodFlagAge = core.Null
		}
		overlayDildo(r, er, ok, snap.AsOf)
	}
}

// overlayDildo stamps the GREEN DILDO -> 9 EMA PULLBACK verdict a flagscan ref
// carries (alert 2: the pullback to the chart's 9 EMA, judged on the chart's
// bars) onto a row, with the same Null rules as the e9* fields:
//
//   - no ref: dildoPbAge / dildoPbPx / dildoTouchAge / dildoPbState are Null,
//     and the engine's own dildoAge..dildoDvol (alert 1) are left as they are;
//   - dildoPbState is the ref's PbState (0 = evaluated, no dildo today);
//     dildoPbAge / dildoPbPx only with a confirmed pullback (PbMs > 0) and
//     dildoTouchAge only with a touch bar (TouchMs > 0), ages counted to the
//     snapshot's clock from the bar's START like e9SlamAge;
//   - the chart-anchor union: a ref dildo NEWER than the engine's (or one the
//     engine has none of - a 04:00 dildo whose priors are yesterday's
//     post-market bars, a restart) replaces dildoAge..dildoDvol, so it raises
//     alert 1 too. The same bar seen by both sides has the same age (both
//     count to AsOf), so it is not replaced and ENTER cannot fire twice.
func overlayDildo(r *core.Row, er core.EmaFlagRef, ok bool, asOf int64) {
	r.DildoPbAge, r.DildoPbPx, r.DildoTouchAge, r.DildoPbState = core.Null, core.Null, core.Null, core.Null
	if !ok {
		return
	}
	r.DildoPbState = float64(er.PbState)
	if er.PbMs > 0 {
		r.DildoPbAge = float64(max(asOf-er.PbMs, 0) / 1000)
		r.DildoPbPx = finiteOrNull(er.PbPx)
	}
	if er.TouchMs > 0 {
		r.DildoTouchAge = float64(max(asOf-er.TouchMs, 0) / 1000)
	}
	if er.DildoMs > 0 {
		if age := float64(max(asOf-er.DildoMs, 0) / 1000); core.IsNull(r.DildoAge) || age < r.DildoAge {
			r.DildoAge = age
			r.DildoBody = finiteOrNull(er.DildoBody)
			r.DildoVolX = finiteOrNull(er.DildoVolX)
			r.DildoDvol = finiteOrNull(er.DildoDvol)
			r.DildoEarly = float64(er.DildoEarly)
		} else if age == r.DildoAge {
			// The same dildo on both sides: the chart's bars (the trader's) decide
			// whether it was early - they reach back further than the engine's.
			r.DildoEarly = float64(er.DildoEarly)
		}
	}
}

func (s *Scanner) SetShortable(m map[string]core.ShortRef) {
	if m == nil {
		return
	}
	s.ctrl <- func(sc *Scanner) { sc.shortRef, sc.shortVer = m, sc.shortVer+1 }
}

// overlayShort stamps IBKR borrow availability onto snapshot rows. Symbols
// absent from the file stay nil/Null — "unknown", not "zero available" — so a
// `shortAvail > 0` filter never silently includes names IBKR doesn't list.
func (s *Scanner) overlayShort(snap *core.MetricSnapshot) {
	if len(s.shortRef) == 0 {
		return
	}
	refs, has, rebuilt := s.shortRows.rows(snap, s.shortVer, s.shortRef)
	if rebuilt {
		// A fresh array whenever the entries change: rows of earlier snapshots
		// (and Row copies handed out) keep pointing at the old one.
		s.shortAvail = make([]int64, len(refs))
		for i := range refs {
			s.shortAvail[i] = refs[i].Available
		}
	}
	for i := range snap.Rows {
		if !has[i] {
			continue
		}
		r, sr := &snap.Rows[i], &refs[i]
		r.ShortAvail = &s.shortAvail[i]
		r.ShortCapped = sr.Capped
		r.ShortFee = sr.FeeRate
	}
}

// overlayNews stamps News/NewsAge onto snapshot rows before scan evaluation.
// The snapshot is freshly built per tick and consumed only here, so in-place
// mutation is safe.
func (s *Scanner) overlayNews(snap *core.MetricSnapshot) {
	if len(s.newsTS) == 0 {
		return
	}
	tss, has, _ := s.newsRows.rows(snap, s.newsVer, s.newsTS)
	for i := range snap.Rows {
		r := &snap.Rows[i]
		ts, ok := tss[i], has[i]
		if !ok || ts > snap.AsOf {
			continue
		}
		age := int((snap.AsOf - ts) / 1000)
		r.NewsAge = age
		r.News = age <= newsFreshSec
	}
}

// --- internal (called from the Run goroutine, or directly in tests) ---

func (s *Scanner) upsert(def core.ScanDef) {
	pred, err := compile(def.Filter)
	if err != nil {
		pred = func(*core.Row) bool { return false } // bad filter matches nothing
	}
	st, ok := s.scans[def.ID]
	if !ok {
		st = &scanState{
			prevRows: map[string]core.Row{},
			prevSet:  map[string]struct{}{},
			armed:    map[string]bool{},
			cooldown: map[string]int64{},
			prevVal:  map[string]float64{},
			baseSet:  map[string]struct{}{},
			hits:     map[string]int{},
		}
		s.scans[def.ID] = st
	}
	st.def = def
	st.pred = pred
	st.basePred = pred
	st.usesHits = filterUsesHits(def.Filter)
	st.sortKeys = def.Sort
	if len(st.sortKeys) == 0 {
		st.sortKeys = defaultSort
	}
	st.streamCols = streamColumnsOf(def)
	st.alertCols = columnsOf(def)
	if st.usesHits {
		base := stripHits(def.Filter)
		switch {
		case base.IsLeaf() && base.Field == core.FieldHits:
			// The whole filter was a bare hits leaf: base = match-all.
			st.basePred = func(*core.Row) bool { return true }
		case !base.IsLeaf() && len(base.Conds) == 0:
			// Every branch was hits-only and got pruned by stripHits (e.g. a
			// root "or" whose sole child was a hits condition): base = match-all,
			// for the same reason as the bare-leaf case above.
			st.basePred = func(*core.Row) bool { return true }
		default:
			if bp, berr := compile(base); berr == nil {
				st.basePred = bp
			}
		}
	}
	st.baseSet = map[string]struct{}{} // def edit: re-baseline base membership too
	// reset diff baseline so the next tick re-sends the full matched set
	st.prevRows = map[string]core.Row{}
	st.prevOrder = nil
	st.prevSet = map[string]struct{}{}
	st.baseline = true // suppress entry events on the first eval after a (re)load

	st.crossField, st.crossCmp, st.crossThr = "", "", 0
	if def.Alert.On == core.AlertOnCross && def.Alert.Trigger != nil && def.Alert.Trigger.IsLeaf() {
		st.crossField = def.Alert.Trigger.Field
		st.crossCmp = def.Alert.Trigger.Cmp
		if f, e := toFloat(def.Alert.Trigger.Value); e == nil {
			st.crossThr = f
		}
	}
}

func (s *Scanner) process(snap *core.MetricSnapshot) {
	if prev := s.last; prev != nil && prev != snap && s.recycle != nil {
		// Everything that reads a snapshot runs on this goroutine and reads
		// s.last, and nothing keeps a pointer into prev's rows beyond the
		// evaluation that made it (match's scratch is rewritten before it is
		// read again), so the engine may refill prev once snap replaces it.
		defer s.recycle(prev)
	}
	s.overlayNews(snap)
	s.overlayShort(snap)
	s.overlayTrend(snap)
	s.overlayFlags(snap)
	s.overlayEP(snap)
	s.last = snap
	// Hits counters are per ET day. Only a real TRANSITION clears them: the
	// first snapshot ever observed (hitsDay=="") must not wipe, or it would
	// race the startup SeedHits and silently discard the restored counters.
	if day := time.UnixMilli(snap.AsOf).In(s.etLoc).Format("2006-01-02"); day != s.hitsDay {
		if s.hitsDay != "" {
			for _, st := range s.scans {
				st.hits = make(map[string]int)
			}
		}
		s.hitsDay = day
	}
	for _, st := range s.scans {
		if !st.def.Enabled {
			continue
		}
		s.evalScan(st, snap)
	}
	s.publishCounts()
}

// publishCounts snapshots each scan's current membership size. A disabled scan
// reports 0 rather than the stale count it held when it was switched off.
func (s *Scanner) publishCounts() {
	c := make(map[string]int, len(s.scans))
	for id, st := range s.scans {
		if st.def.Enabled {
			c[id] = len(st.prevSet)
		}
	}
	s.counts.Store(&c)
}

// match filters, sorts, and caps a snapshot for one scan: pointers into
// snap.Rows, best first. The slice is st's scratch, valid until st's next
// match. With account set it also runs the hits accounting (accountHits),
// sharing the filter pass with it when the filter does not mention hits
// (basePred is pred then).
//
// Hits is a per-scan overlay stamped onto the SHARED snapshot row: safe because
// scans evaluate sequentially on the Run goroutine and every scan re-stamps
// before reading. A filter that reads hits gets every row stamped before it
// runs (that is what makes "hits >= 2" filterable); otherwise only the matched
// rows are, after the accounting, which is all the sort, the stream and the
// alerts read.
func (s *Scanner) match(st *scanState, snap *core.MetricSnapshot, account bool) []*core.Row {
	ents := st.ents[:0]
	if st.usesHits {
		if account {
			accountHits(st, snap)
		}
		for i := range snap.Rows {
			r := &snap.Rows[i]
			r.Hits = st.hits[r.Sym]
			if st.pred(r) {
				ents = append(ents, sortEnt{r: r})
			}
		}
	} else {
		for i := range snap.Rows {
			if r := &snap.Rows[i]; st.pred(r) {
				ents = append(ents, sortEnt{r: r})
			}
		}
		if account {
			cur := st.nextBaseSet()
			for i := range ents {
				accountHit(st, cur, ents[i].r.Sym)
			}
			st.spareBase, st.baseSet = st.baseSet, cur
		}
		for i := range ents {
			ents[i].r.Hits = st.hits[ents[i].r.Sym]
		}
	}
	sortEnts(ents, st.sortKeys)
	if n := st.def.Limit; n > 0 && len(ents) > n {
		ents = ents[:n]
	}
	st.ents = ents
	out := st.matched[:0]
	for i := range ents {
		out = append(out, ents[i].r)
	}
	st.matched = out
	return out
}

// accountHits updates a scan's recurrence counters from base-criteria
// membership transitions. Baseline passes (first eval after (re)load) floor
// present symbols to 1 without counting an "entry" — same suppression idiom
// as entry events.
func accountHits(st *scanState, snap *core.MetricSnapshot) {
	cur := st.nextBaseSet()
	for i := range snap.Rows {
		r := &snap.Rows[i]
		if !st.basePred(r) {
			continue
		}
		accountHit(st, cur, r.Sym)
	}
	st.spareBase, st.baseSet = st.baseSet, cur
}

// accountHit records sym as passing the base criteria this tick.
func accountHit(st *scanState, cur map[string]struct{}, sym string) {
	cur[sym] = struct{}{}
	if _, was := st.baseSet[sym]; !was && !st.baseline {
		st.hits[sym]++
	} else if st.hits[sym] == 0 {
		st.hits[sym] = 1 // present today = first hit (baseline, def edit, ET reset)
	}
}

// nextBaseSet is an empty map for this tick's base membership: the one two
// ticks old, cleared (baseSet is only read by the evaluation that replaces
// it).
func (st *scanState) nextBaseSet() map[string]struct{} {
	m := st.spareBase
	if m == nil {
		return make(map[string]struct{}, len(st.baseSet))
	}
	clear(m)
	return m
}

// nextSet is nextBaseSet for the displayed membership (prevSet).
func (st *scanState) nextSet() map[string]struct{} {
	m := st.spareSet
	if m == nil {
		return make(map[string]struct{}, len(st.prevSet))
	}
	clear(m)
	return m
}

func (s *Scanner) evalScan(st *scanState, snap *core.MetricSnapshot) {
	matched := s.match(st, snap, true)
	cols := st.streamCols

	// The order is compared with the one on screen before a new one is built:
	// most evaluations keep it.
	sameOrder := st.prevOrder != nil && len(st.prevOrder) == len(matched)
	var rows map[string]core.RowJSON
	curSet := st.nextSet()
	for i, r := range matched {
		if sameOrder && st.prevOrder[i] != r.Sym {
			sameOrder = false
		}
		curSet[r.Sym] = struct{}{}
		// Hits were already updated by the accounting and stamped by match(),
		// so the emitted row carries the fresh count on its entry tick.
		_, was := st.prevSet[r.Sym]
		isEntry := !st.baseline && !was
		// prevRows is only ever compared on Last and cols, so an unchanged row
		// keeps the stored copy: it is equal on every field rowChanged reads.
		if prev, had := st.prevRows[r.Sym]; !had || rowChangedP(&prev, r, cols) {
			if rows == nil {
				rows = make(map[string]core.RowJSON)
			}
			rows[r.Sym] = rowJSON(r, cols)
			st.prevRows[r.Sym] = *r
		}
		// Entry event on genuinely new membership (not the post-load baseline).
		if isEntry {
			s.emitEntry(core.ScanEntry{ScanID: st.def.ID, Sym: r.Sym, At: snap.AsOf, Last: r.Last, HasNews: r.News, Hits: r.Hits})
		}
	}
	st.baseline = false
	if len(st.prevRows) > len(curSet) {
		for sym := range st.prevRows {
			if _, ok := curSet[sym]; !ok {
				delete(st.prevRows, sym)
			}
		}
	}

	// Nothing to say: no row body changed and the order is the one already on
	// screen. Seq is only advanced on an emitted tick, so the client's gap
	// check (seq must be prev+1) stays exact across suppressed evaluations.
	if len(rows) > 0 || !sameOrder {
		order := make([]string, len(matched))
		for i, r := range matched {
			order[i] = r.Sym
		}
		if rows == nil {
			rows = make(map[string]core.RowJSON)
		}
		st.seq++
		st.prevOrder = order
		s.emitTick(core.ScanTick{
			ID: st.def.ID, Seq: st.seq, AsOf: snap.AsOf, Full: false, Order: order, Rows: rows,
		})
	}

	s.detectAlerts(st, matched, curSet, snap.AsOf, st.alertCols) // alerts carry the scan's own columns
	st.spareSet, st.prevSet = st.prevSet, curSet
}

func (s *Scanner) detectAlerts(st *scanState, matched []*core.Row, curSet map[string]struct{}, asOf int64, cols []string) {
	ac := st.def.Alert

	// Maintain enter-arming regardless of enabled, so toggling behaves sanely.
	for sym := range st.prevSet {
		if _, still := curSet[sym]; !still {
			st.armed[sym] = true // left the set -> re-armed
		}
	}
	for sym := range curSet {
		if _, ok := st.armed[sym]; !ok {
			st.armed[sym] = true // first sight arms
		}
	}

	if !ac.Enabled {
		if ac.On == core.AlertOnCross {
			for _, r := range matched {
				st.prevVal[r.Sym] = fieldNum(r, st.crossField)
			}
		}
		return
	}

	for _, r := range matched {
		sym := r.Sym
		var fire bool
		var kind string

		switch ac.On {
		case core.AlertOnCross:
			cur := fieldNum(r, st.crossField)
			prev, had := st.prevVal[sym]
			if had && !core.IsNull(cur) && !core.IsNull(prev) {
				switch st.crossCmp {
				case core.CmpCrossAbove:
					fire = prev < st.crossThr && cur >= st.crossThr
				case core.CmpCrossBelow:
					fire = prev > st.crossThr && cur <= st.crossThr
				}
			}
			kind = "cross"
			st.prevVal[sym] = cur
		default: // enter
			_, was := st.prevSet[sym]
			fire = !was && st.armed[sym]
			kind = "enter"
		}

		if !fire || asOf < st.cooldown[sym] {
			continue
		}
		s.emitAlert(core.AlertMsg{
			ScanID: st.def.ID, Sym: sym, Kind: kind, At: asOf, Sound: ac.Sound,
			Row: rowJSON(r, cols),
		})
		if ac.On == core.AlertOnEnter {
			st.armed[sym] = false
		}
		if ac.CooldownSec > 0 {
			st.cooldown[sym] = asOf + int64(ac.CooldownSec)*1000
		}
	}
}

// resync rebuilds the current full tick for a scan and resets its diff baseline.
func (s *Scanner) resync(id string) (core.ScanTick, bool) {
	st, ok := s.scans[id]
	if !ok || s.last == nil {
		return core.ScanTick{}, false
	}
	matched := s.match(st, s.last, false)
	cols := st.streamCols
	order := make([]string, len(matched))
	rows := make(map[string]core.RowJSON, len(matched))
	st.prevRows = make(map[string]core.Row, len(matched))
	curSet := make(map[string]struct{}, len(matched))
	for i, r := range matched {
		order[i] = r.Sym
		rows[r.Sym] = rowJSON(r, cols)
		st.prevRows[r.Sym] = *r
		curSet[r.Sym] = struct{}{}
	}
	st.prevSet = curSet
	st.prevOrder = order
	return core.ScanTick{
		ID: id, Seq: st.seq, AsOf: s.last.AsOf, Full: true, Order: order, Rows: rows,
	}, true
}

func (s *Scanner) emitTick(t core.ScanTick) {
	select {
	case s.ticks <- t:
	default: // consumer behind; drop (seq gap -> client resyncs)
	}
}

func (s *Scanner) emitAlert(a core.AlertMsg) {
	select {
	case s.alerts <- a:
	default:
	}
}

func (s *Scanner) emitEntry(e core.ScanEntry) {
	select {
	case s.entries <- e:
	default: // consumer behind; entries are advisory, drop
	}
}

// overlayRows caches, for one overlay table and one snapshot row order (the
// snapshot's IndexGen), the table's entry for each row, so an overlay runs
// without a symbol lookup per row per snapshot: the tables change from about
// once a second (the 9EMA flags) to once a night (trend), the row order almost
// never. Owned by the Run goroutine.
type overlayRows[V any] struct {
	gen  uint64 // IndexGen the slices were built for; 0 = none
	ver  uint64 // table version they were built from
	vals []V
	has  []bool
}

// rows returns m's entry for each of snap's rows (vals[i], has[i]), and whether
// they were looked up afresh. They come from the cache while snap has the row
// order (a non-zero IndexGen) and the table version (ver) they were built for;
// a snapshot without an IndexGen is always looked up. A rebuild allocates new
// slices, never reusing the old ones.
func (c *overlayRows[V]) rows(snap *core.MetricSnapshot, ver uint64, m map[string]V) (vals []V, has []bool, rebuilt bool) {
	if snap.IndexGen != 0 && c.gen == snap.IndexGen && c.ver == ver && len(c.has) == len(snap.Rows) {
		return c.vals, c.has, false
	}
	vals, has = make([]V, len(snap.Rows)), make([]bool, len(snap.Rows))
	for i := range snap.Rows {
		vals[i], has[i] = m[snap.Rows[i].Sym]
	}
	c.gen, c.ver, c.vals, c.has = snap.IndexGen, ver, vals, has
	return vals, has, true
}
