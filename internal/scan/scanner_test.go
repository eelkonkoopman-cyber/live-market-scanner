package scan

import (
	"context"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/engine"
)

func snapOf(asOf int64, rows ...core.Row) *core.MetricSnapshot {
	snap := &core.MetricSnapshot{AsOf: asOf, Sess: core.SessionRegular, Rows: rows, Index: map[string]int{}}
	for i := range rows {
		snap.Index[rows[i].Sym] = i
	}
	return snap
}

func readTick(t *testing.T, s *Scanner) core.ScanTick {
	t.Helper()
	select {
	case tk := <-s.ticks:
		return tk
	default:
		t.Fatal("expected a tick, none queued")
		return core.ScanTick{}
	}
}

func readAlert(t *testing.T, s *Scanner) core.AlertMsg {
	t.Helper()
	select {
	case a := <-s.alerts:
		return a
	default:
		t.Fatal("expected an alert, none queued")
		return core.AlertMsg{}
	}
}

func gotAlert(s *Scanner) bool {
	select {
	case <-s.alerts:
		return true
	default:
		return false
	}
}

func drain(s *Scanner) {
	for {
		select {
		case <-s.ticks:
		default:
			return
		}
	}
}

func momScan() core.ScanDef {
	return core.ScanDef{
		ID: "m", Name: "mom", Enabled: true,
		Filter: core.Filter{Field: core.FieldChg5m, Cmp: core.CmpGT, Value: 2.0},
		Sort:   []core.SortKey{{Field: core.FieldChg5m, Dir: "desc"}},
		Limit:  10, Columns: []string{"last", "chg5m"},
	}
}

func TestScannerTickAndDelta(t *testing.T) {
	s := New(nil)
	s.upsert(momScan())

	s.process(snapOf(1000, mkRow("A", 10, 5), mkRow("B", 10, 3), mkRow("C", 10, 1)))
	tk := readTick(t, s)
	if len(tk.Order) != 2 || tk.Order[0] != "A" || tk.Order[1] != "B" {
		t.Fatalf("order want [A B] got %v", tk.Order)
	}
	if len(tk.Rows) != 2 {
		t.Fatalf("first tick should carry both rows, got %d", len(tk.Rows))
	}

	// B jumps above A; A unchanged; C still excluded.
	s.process(snapOf(1250, mkRow("A", 10, 5), mkRow("B", 10, 6), mkRow("C", 10, 1)))
	tk2 := readTick(t, s)
	if tk2.Order[0] != "B" || tk2.Order[1] != "A" {
		t.Fatalf("reorder want [B A] got %v", tk2.Order)
	}
	if _, ok := tk2.Rows["B"]; !ok {
		t.Fatal("B changed -> must be in delta rows")
	}
	if _, ok := tk2.Rows["A"]; ok {
		t.Fatal("A unchanged -> must NOT be in delta rows")
	}
	if tk2.Seq != tk.Seq+1 {
		t.Fatalf("seq should increment: %d -> %d", tk.Seq, tk2.Seq)
	}
}

func TestScannerNewsOverlay(t *testing.T) {
	s := New(nil)
	def := momScan()
	def.Columns = []string{"last", "chg5m", "news", "newsAge"}
	s.upsert(def)

	// Headline for A at t=1000s; snapshot at t=1060s -> age 60s, fresh.
	s.MarkNews([]core.NewsItem{{ID: "n1", Tickers: []string{"A"}, TS: 1000_000}})
	(<-s.ctrl)(s) // apply the queued ctrl op without Run()

	snap := snapOf(1060_000, mkRow("A", 10, 5), mkRow("B", 10, 3))
	s.process(snap)
	tk := readTick(t, s)

	rowA, ok := tk.Rows["A"]
	if !ok {
		t.Fatal("A missing from tick")
	}
	if rowA["news"] != true {
		t.Fatalf("A should have fresh news, row=%v", rowA)
	}
	if age, _ := rowA["newsAge"].(int); age != 60 {
		t.Fatalf("A newsAge want 60 got %v", rowA["newsAge"])
	}
	if rowB, ok := tk.Rows["B"]; ok {
		if rowB["news"] == true {
			t.Fatalf("B has no news, row=%v", rowB)
		}
	}

	// A news-filtered scan matches only A.
	def2 := core.ScanDef{
		ID: "n", Name: "news", Enabled: true,
		Filter: core.Filter{Field: core.FieldNews, Cmp: core.CmpIsTrue},
		Limit:  10, Columns: []string{"last"},
	}
	s.upsert(def2)
	s.process(snapOf(1061_000, mkRow("A", 10, 5), mkRow("B", 10, 3)))
	var newsTick core.ScanTick
	for i := 0; i < 2; i++ {
		tk := readTick(t, s)
		if tk.ID == "n" {
			newsTick = tk
		}
	}
	if len(newsTick.Order) != 1 || newsTick.Order[0] != "A" {
		t.Fatalf("news scan want [A] got %v", newsTick.Order)
	}

	// Stale news (>30 min): News=false but NewsAge still filterable.
	s.process(snapOf(1000_000+31*60_000, mkRow("A", 10, 5)))
	for i := 0; i < 2; i++ {
		tk := readTick(t, s)
		if tk.ID == "n" && len(tk.Order) != 0 {
			t.Fatalf("stale news should leave the news scan, got %v", tk.Order)
		}
	}
}

func TestScannerEntryEvents(t *testing.T) {
	s := New(nil)
	s.upsert(momScan())

	drainEntries := func() []core.ScanEntry {
		var out []core.ScanEntry
		for {
			select {
			case e := <-s.entries:
				out = append(out, e)
			default:
				return out
			}
		}
	}

	// First eval after load: baseline — A matches but must NOT emit an entry.
	s.process(snapOf(1000, mkRow("A", 10, 5), mkRow("B", 10, 1)))
	if got := drainEntries(); len(got) != 0 {
		t.Fatalf("baseline eval must not emit entries, got %+v", got)
	}

	// B rises into the set -> exactly one entry, carrying price.
	s.process(snapOf(2000, mkRow("A", 10, 5), mkRow("B", 12, 4)))
	got := drainEntries()
	if len(got) != 1 || got[0].Sym != "B" || got[0].ScanID != "m" || got[0].Last != 12 || got[0].At != 2000 {
		t.Fatalf("want one entry for B@12, got %+v", got)
	}

	// Unchanged membership -> nothing.
	s.process(snapOf(3000, mkRow("A", 10, 5), mkRow("B", 12, 4)))
	if got := drainEntries(); len(got) != 0 {
		t.Fatalf("no new membership -> no entries, got %+v", got)
	}

	// Def edit resets the baseline: the re-send must not fire pseudo-entries.
	s.upsert(momScan())
	s.process(snapOf(4000, mkRow("A", 10, 5), mkRow("B", 12, 4)))
	if got := drainEntries(); len(got) != 0 {
		t.Fatalf("post-upsert baseline must not emit entries, got %+v", got)
	}
}

// TestScannerEntryEventsCarryNewsFlag: the Activity Log's "News" column reads
// this straight off the entry event, so it must reflect whatever the News
// overlay computed for that row in the SAME process() pass — not go stale, not
// require a second tick.
func TestScannerEntryEventsCarryNewsFlag(t *testing.T) {
	s := New(nil)
	s.upsert(momScan())

	drainEntries := func() []core.ScanEntry {
		var out []core.ScanEntry
		for {
			select {
			case e := <-s.entries:
				out = append(out, e)
			default:
				return out
			}
		}
	}

	// Baseline: neither symbol has news yet.
	s.process(snapOf(1000, mkRow("A", 10, 1), mkRow("B", 10, 1)))
	drainEntries()

	// Headline lands for A only, then both A and B enter the scan on the same
	// tick. A's entry must carry HasNews=true, B's must carry HasNews=false.
	s.MarkNews([]core.NewsItem{{ID: "n1", Tickers: []string{"A"}, TS: 1_000}})
	(<-s.ctrl)(s) // apply the queued MarkNews op without Run()

	s.process(snapOf(2_000, mkRow("A", 10, 5), mkRow("B", 10, 5)))
	got := drainEntries()
	if len(got) != 2 {
		t.Fatalf("want 2 entries (A and B), got %+v", got)
	}
	byS := map[string]core.ScanEntry{got[0].Sym: got[0], got[1].Sym: got[1]}
	if !byS["A"].HasNews {
		t.Fatalf("A's entry should carry HasNews=true, got %+v", byS["A"])
	}
	if byS["B"].HasNews {
		t.Fatalf("B's entry should carry HasNews=false, got %+v", byS["B"])
	}
}

// The hits column answers "is this stock recurring?" — so the count must be
// per-scan, incremented on genuine re-entry, floored at 1 for anything
// currently matched, filterable via `hits >= 2`, and reset at the ET day
// boundary. All of that in one scenario:
func TestScannerRecurringHits(t *testing.T) {
	s := New(nil)
	def := momScan()
	def.Columns = []string{"last", "chg5m", "hits"}
	s.upsert(def)

	day1 := time.Date(2026, 7, 28, 15, 0, 0, 0, s.etLoc) // 15:00 ET

	// Baseline: A matched at load -> floors to 1 (present = first hit).
	s.process(snapOf(day1.UnixMilli(), mkRow("A", 10, 5), mkRow("B", 10, 1)))
	tk := readTick(t, s)
	if got := tk.Rows["A"]["hits"]; got != 1 {
		t.Fatalf("baseline member: hits want 1, got %v", got)
	}

	// B enters -> 1. A stays -> still 1.
	s.process(snapOf(day1.Add(time.Minute).UnixMilli(), mkRow("A", 10, 5), mkRow("B", 10, 4)))
	tk = readTick(t, s)
	if got := tk.Rows["B"]["hits"]; got != 1 {
		t.Fatalf("first entry: hits want 1, got %v", got)
	}

	// B exits...
	s.process(snapOf(day1.Add(2*time.Minute).UnixMilli(), mkRow("A", 10, 5), mkRow("B", 10, 1)))
	drain(s)
	// ...and re-enters -> 2, and the emitted row must carry it immediately.
	s.process(snapOf(day1.Add(3*time.Minute).UnixMilli(), mkRow("A", 10, 5), mkRow("B", 10, 4)))
	tk = readTick(t, s)
	if got := tk.Rows["B"]["hits"]; got != 2 {
		t.Fatalf("re-entry: hits want 2, got %v", got)
	}
	if got := tk.Rows["A"]; got != nil && got["hits"] != nil && got["hits"] != 1 {
		t.Fatalf("A never left: hits must stay 1, got %v", got["hits"])
	}

	// New ET day -> counters clear; continuously-present symbols floor back
	// to 1. Assert on scanner state (the delta frame omits unchanged rows).
	day2 := day1.Add(24 * time.Hour)
	s.process(snapOf(day2.UnixMilli(), mkRow("A", 10, 5), mkRow("B", 10, 4)))
	drain(s)
	if got := s.scans["m"].hits["A"]; got != 1 {
		t.Fatalf("after ET rollover A wants 1, got %d", got)
	}
	if got := s.scans["m"].hits["B"]; got != 1 {
		t.Fatalf("after ET rollover B (still present) wants 1, got %d", got)
	}
}

// `hits >= 2` as a FILTER is the "recurring movers only" scan. Hits must
// accrue from the BASE criteria (filter minus the hits condition), or such a
// scan could never organically fire: a symbol held out at 1 hit would never
// enter, so its counter would never reach 2.
func TestScannerHitsFilterOrganicallyReachable(t *testing.T) {
	s := New(nil)
	def := momScan()
	def.ID = "rec"
	def.Columns = []string{"last", "chg5m", "hits"}
	def.Filter = core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldChg5m, Cmp: core.CmpGT, Value: 2.0},
		{Field: core.FieldHits, Cmp: core.CmpGTE, Value: 2.0},
	}}
	s.upsert(def)

	base := time.Date(2026, 7, 28, 15, 0, 0, 0, s.etLoc).UnixMilli()

	// Baseline pass: A meets the base criteria -> floors to 1; display empty.
	s.process(snapOf(base, mkRow("A", 10, 5)))
	if tk := readTick(t, s); len(tk.Order) != 0 {
		t.Fatalf("first appearance must not match a hits>=2 scan, got %v", tk.Order)
	}
	// A drops out of the base criteria...
	s.process(snapOf(base+60_000, mkRow("A", 10, 1)))
	drain(s)
	// ...and pops AGAIN: second base entry -> hits=2 -> NOW it matches.
	s.process(snapOf(base+120_000, mkRow("A", 10, 5)))
	tk := readTick(t, s)
	if len(tk.Order) != 1 || tk.Order[0] != "A" {
		t.Fatalf("second pop must match the recurring scan organically, got %v", tk.Order)
	}
	if got := tk.Rows["A"]["hits"]; got != 2 {
		t.Fatalf("displayed hits want 2, got %v", got)
	}
}

// The emitted ScanEntry for a hits-gated scan must carry the TRUE base-hits
// count at that moment (2, not 1) — this is the value EntryHitsMax later
// seeds from. Counting archived ENTRIES instead (the pre-fix approach) would
// undercount by exactly the filter's own threshold, since entries below the
// threshold never display and so never archive at all.
func TestScannerEmittedEntryCarriesTrueHitsNotDisplayCount(t *testing.T) {
	s := New(nil)
	def := momScan()
	def.ID = "rec"
	def.Filter = core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldChg5m, Cmp: core.CmpGT, Value: 2.0},
		{Field: core.FieldHits, Cmp: core.CmpGTE, Value: 2.0},
	}}
	s.upsert(def)

	drainEntries := func() []core.ScanEntry {
		var out []core.ScanEntry
		for {
			select {
			case e := <-s.entries:
				out = append(out, e)
			default:
				return out
			}
		}
	}

	base := time.Date(2026, 7, 28, 15, 0, 0, 0, s.etLoc).UnixMilli()
	s.process(snapOf(base, mkRow("A", 10, 5))) // baseline pop 1: base-hits floors to 1, no display
	if got := drainEntries(); len(got) != 0 {
		t.Fatalf("no display entry expected yet, got %+v", got)
	}
	s.process(snapOf(base+60_000, mkRow("A", 10, 1))) // drop out of base criteria
	drain(s)
	drainEntries()
	s.process(snapOf(base+120_000, mkRow("A", 10, 5))) // pop 2: base-hits=2, NOW displays

	got := drainEntries()
	if len(got) != 1 {
		t.Fatalf("want exactly one display entry, got %+v", got)
	}
	if got[0].Hits != 2 {
		t.Fatalf("the ONLY archived entry so far must carry Hits=2 (the true count), not 1 (a display-entry count would read 1 here): got %d", got[0].Hits)
	}
}

// A hits condition nested inside an "or" group must not leave basePred
// permanently matching nothing. stripHits drops the hits leaf, and an emptied
// OR group compiles to a hard false (correct default boolean algebra for a
// genuine empty OR) — which would otherwise make ANY scan with a hits-only OR
// branch silently unmatchable forever, with no error surfaced anywhere.
func TestScannerHitsInsideOrGroupDoesNotBreakBase(t *testing.T) {
	cases := []struct {
		name   string
		filter core.Filter
	}{
		{
			name:   "root OR with only a hits leaf",
			filter: core.Filter{Op: "or", Conds: []core.Filter{{Field: core.FieldHits, Cmp: core.CmpGTE, Value: 2.0}}},
		},
		{
			name: "hits-only OR nested inside an AND",
			filter: core.Filter{Op: "and", Conds: []core.Filter{
				{Field: core.FieldChg5m, Cmp: core.CmpGT, Value: -1000.0}, // always true for mkRow rows, just present
				{Op: "or", Conds: []core.Filter{{Field: core.FieldHits, Cmp: core.CmpGTE, Value: 2.0}}},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(nil)
			def := momScan()
			def.ID = "rec"
			def.Columns = []string{"last", "chg5m", "hits"}
			def.Filter = tc.filter
			s.upsert(def)

			// Both shapes strip down to a match-all (or effectively match-all)
			// base, so a continuously-PRESENT symbol never re-enters (nothing
			// to transition from). A must be genuinely ABSENT from a snapshot
			// to exit the base set, exactly like a ticker the engine drops.
			base := time.Date(2026, 7, 28, 15, 0, 0, 0, s.etLoc).UnixMilli()
			s.process(snapOf(base, mkRow("A", 10, 5))) // baseline: floors to 1 IF basePred matches at all
			if got := s.scans["rec"].hits["A"]; got != 1 {
				t.Fatalf("basePred must match A on baseline (the pre-fix bug: an emptied hits-only OR "+
					"compiles to a hard false, so hits never floors to 1 at all) — got hits=%d", got)
			}
			drain(s)
			s.process(snapOf(base + 60_000)) // A absent this tick -> exits the base set
			drain(s)
			s.process(snapOf(base+120_000, mkRow("A", 10, 5))) // re-entry -> hits=2 -> now displays

			tk := readTick(t, s)
			if len(tk.Order) != 1 || tk.Order[0] != "A" {
				t.Fatalf("basePred must still be reachable despite the hits-only OR branch, got %v", tk.Order)
			}
			if got := tk.Rows["A"]["hits"]; got != 2 {
				t.Fatalf("hits want 2 after re-entry, got %v", got)
			}
		})
	}
}

// SeedHits (the restart path) must make prior entries from the archive count.
func TestScannerSeedHits(t *testing.T) {
	s := New(nil)
	def := momScan()
	def.ID = "rec"
	def.Filter = core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldChg5m, Cmp: core.CmpGT, Value: 2.0},
		{Field: core.FieldHits, Cmp: core.CmpGTE, Value: 3.0},
	}}
	s.upsert(def)
	s.SeedHits("2026-07-28", map[string]map[string]int{"rec": {"A": 3}, "ghost-scan": {"B": 9}})
	(<-s.ctrl)(s) // apply without Run()

	base := time.Date(2026, 7, 28, 15, 0, 0, 0, s.etLoc).UnixMilli()
	s.process(snapOf(base, mkRow("A", 10, 5), mkRow("B", 10, 5)))
	tk := readTick(t, s)
	if len(tk.Order) != 1 || tk.Order[0] != "A" {
		t.Fatalf("seeded A@3 must match hits>=3; unseeded B must not: got %v", tk.Order)
	}

	// A seed computed before an ET midnight (day mismatch) must be dropped —
	// yesterday's recurrence must not leak into today's counters.
	s2 := New(nil)
	s2.upsert(def)
	s2.process(snapOf(base, mkRow("A", 10, 1))) // sets hitsDay to 2026-07-28
	<-s2.ticks
	s2.SeedHits("2026-07-27", map[string]map[string]int{"rec": {"A": 9}})
	(<-s2.ctrl)(s2)
	if got := s2.scans["rec"].hits["A"]; got != 0 {
		t.Fatalf("stale-day seed must be dropped, got hits=%d", got)
	}
}

// Daily-trend overlay: values stamp onto rows for column/sort/filter use, and
// symbols ABSENT from the reference must read Null — not 0, which would mean
// "flat daily chart" for every unknown ticker.
func TestScannerTrendOverlayAndFilter(t *testing.T) {
	s := New(nil)
	def := momScan()
	def.ID = "nodown"
	def.Columns = []string{"last", "chg5m", "dtrend", "chg20d"}
	def.Filter = core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldChg5m, Cmp: core.CmpGT, Value: 2.0},
		{Field: core.FieldDTrend, Cmp: core.CmpGTE, Value: 0.0}, // hide daily downtrends
	}}
	s.upsert(def)

	s.SetDailyTrend(map[string]core.TrendRef{
		"UP":   {DTrend: 1, Chg20d: 34.5, VsSMA: 12.1, Days: 30, AsOf: "2026-07-28"},
		"DOWN": {DTrend: -1, Chg20d: -22.0, VsSMA: -9.0, Days: 30, AsOf: "2026-07-28"},
		"FLAT": {DTrend: 0, Chg20d: 0.4, VsSMA: 0.2, Days: 30, AsOf: "2026-07-28"},
	})
	(<-s.ctrl)(s)

	// All four pop; NEWIPO has no reference at all.
	base := time.Date(2026, 7, 28, 15, 0, 0, 0, s.etLoc).UnixMilli()
	s.process(snapOf(base, mkRow("UP", 10, 5), mkRow("DOWN", 10, 6), mkRow("FLAT", 10, 4), mkRow("NEWIPO", 10, 7)))
	tk := readTick(t, s)

	got := map[string]bool{}
	for _, sym := range tk.Order {
		got[sym] = true
	}
	if !got["UP"] || !got["FLAT"] {
		t.Fatalf("up + flat must pass dtrend >= 0, got %v", tk.Order)
	}
	if got["DOWN"] {
		t.Fatalf("daily downtrend must be filtered out, got %v", tk.Order)
	}
	// Null comparisons are false in this DSL (numeric guard), so an unknown
	// trend is EXCLUDED by dtrend >= 0 — conservative, and consistent with
	// how rvol/chg filters treat unwarmed rows.
	if got["NEWIPO"] {
		t.Fatalf("unknown trend must not pass a dtrend filter, got %v", tk.Order)
	}

	if v := tk.Rows["UP"]["dtrend"]; v != 1 {
		t.Fatalf("UP dtrend column want 1, got %v", v)
	}
	if v := tk.Rows["FLAT"]["chg20d"]; v != 0.4 {
		t.Fatalf("FLAT chg20d column want 0.4, got %v", v)
	}
}

// textbookFlag is a fully-populated reference for a clean setup: a 45% pole,
// a 7-bar base spanning 6.2% that gave back 18.4% on volume down to 62% of the
// pole's. Tests vary one field at a time off this.
func textbookFlag() core.FlagRef {
	return core.FlagRef{
		Score: 82.5, State: 1, PoleGain: 45.0, RangePct: 6.2, Retrace: 18.4,
		VolDry: 0.62, Bars: 7, BaseHigh: 12.5, AsOf: "2026-07-30",
	}
}

// The overlay must transfer every measurement the nightly pass produced onto
// the row, including the int->float64 widening of the bar count.
func TestScannerFlagOverlayStampsAllFields(t *testing.T) {
	s := New(nil)
	s.SetDailyFlags(map[string]core.FlagRef{"TIGHT": textbookFlag()})
	(<-s.ctrl)(s)

	// Last is under BaseHigh, so State passes through unpromoted.
	snap := snapOf(1000, mkRow("TIGHT", 11.9, 1))
	s.overlayFlags(snap)
	r := snap.Rows[0]

	for _, c := range []struct {
		field string
		got   float64
		want  float64
	}{
		{core.FieldFlagScore, r.FlagScore, 82.5},
		{core.FieldFlagState, r.FlagState, 1},
		{core.FieldFlagPole, r.FlagPole, 45.0},
		{core.FieldFlagRange, r.FlagRange, 6.2},
		{core.FieldFlagRetrace, r.FlagRetrace, 18.4},
		{core.FieldFlagVolDry, r.FlagVolDry, 0.62},
		{core.FieldFlagBars, r.FlagBars, 7},
	} {
		if c.got != c.want {
			t.Errorf("row.%s want %v got %v", c.field, c.want, c.got)
		}
		// Every one of them must also be reachable by the filter/sort layer.
		if v := fieldNum(&r, c.field); v != c.want {
			t.Errorf("fieldNum(%s) want %v got %v", c.field, c.want, v)
		}
	}

	// The two discrete fields ride the wire as ints, like dtrend and hits.
	if v := fieldValue(&r, core.FieldFlagState); v != 1 {
		t.Errorf("flagState wire value want int(1), got %v (%T)", v, v)
	}
	if v := fieldValue(&r, core.FieldFlagBars); v != 7 {
		t.Errorf("flagBars wire value want int(7), got %v (%T)", v, v)
	}
	if v := fieldValue(&r, core.FieldFlagScore); v != 82.5 {
		t.Errorf("flagScore wire value want 82.5, got %v (%T)", v, v)
	}
}

// A symbol with no pattern data is UNKNOWN, not "measured and found wanting".
// All seven fields must read Null, and the subtle half of that contract is
// flagState: a zero there would be a real "no pattern" verdict, so a
// `flagState == 0` scan would silently fill up with every ticker the nightly
// pass has never seen.
func TestScannerFlagOverlayUnknownSymbolIsNull(t *testing.T) {
	s := New(nil)

	best := momScan()
	best.ID = "setups"
	best.Filter = core.Filter{Field: core.FieldFlagScore, Cmp: core.CmpGT, Value: 70.0}
	best.Columns = []string{"last", "flagScore", "flagState"}
	s.upsert(best)

	nopat := momScan()
	nopat.ID = "nopattern"
	nopat.Filter = core.Filter{Field: core.FieldFlagState, Cmp: core.CmpEq, Value: 0.0}
	nopat.Columns = []string{"last", "flagState"}
	s.upsert(nopat)

	s.SetDailyFlags(map[string]core.FlagRef{"TIGHT": textbookFlag()})
	(<-s.ctrl)(s)

	base := time.Date(2026, 7, 30, 15, 0, 0, 0, s.etLoc).UnixMilli()
	s.process(snapOf(base, mkRow("TIGHT", 11.9, 1), mkRow("NEWIPO", 5, 1)))

	ticks := map[string]core.ScanTick{}
	for i := 0; i < 2; i++ {
		tk := readTick(t, s)
		ticks[tk.ID] = tk
	}
	if order := ticks["setups"].Order; len(order) != 1 || order[0] != "TIGHT" {
		t.Fatalf("flagScore > 70 want [TIGHT] (unknown NEWIPO must not pass), got %v", order)
	}
	if order := ticks["nopattern"].Order; len(order) != 0 {
		t.Fatalf("flagState == 0 must match NOTHING — an unclassified symbol is unknown, "+
			"not a confirmed absence of a pattern — got %v", order)
	}

	row := s.last.Rows[s.last.Index["NEWIPO"]]
	for _, c := range []struct {
		field string
		got   float64
	}{
		{core.FieldFlagScore, row.FlagScore},
		{core.FieldFlagState, row.FlagState},
		{core.FieldFlagPole, row.FlagPole},
		{core.FieldFlagRange, row.FlagRange},
		{core.FieldFlagRetrace, row.FlagRetrace},
		{core.FieldFlagVolDry, row.FlagVolDry},
		{core.FieldFlagBars, row.FlagBars},
	} {
		if !core.IsNull(c.got) {
			t.Errorf("unknown symbol: %s want Null, got %v", c.field, c.got)
		}
		if v := fieldValue(&row, c.field); v != nil {
			t.Errorf("unknown symbol: %s wire value want nil, got %v", c.field, v)
		}
	}
}

// "Nightly geometry, live breakout": the base is measured overnight, but the
// promotion to state 2 happens on the tick that clears the stored base high.
func TestScannerFlagLiveBreakoutPromotion(t *testing.T) {
	coiling := textbookFlag() // State 1, BaseHigh 12.5
	nopattern := textbookFlag()
	nopattern.State = 0

	cases := []struct {
		name  string
		ref   core.FlagRef
		last  float64
		want  float64
		notes string
	}{
		{name: "inside the base stays coiling", ref: coiling, last: 12.0, want: 1},
		{name: "exactly at the base high stays coiling", ref: coiling, last: 12.5, want: 1},
		{name: "clears the base high -> breakout", ref: coiling, last: 12.51, want: 2},
		{name: "far above the base high -> breakout", ref: coiling, last: 20.0, want: 2},
		{name: "no pattern is never promoted", ref: nopattern, last: 99.0, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(nil)
			s.SetDailyFlags(map[string]core.FlagRef{"X": tc.ref})
			(<-s.ctrl)(s)

			snap := snapOf(1000, mkRow("X", tc.last, 1))
			s.overlayFlags(snap)
			if got := snap.Rows[0].FlagState; got != tc.want {
				t.Fatalf("stored state %v, last %v, baseHigh %v: flagState want %v got %v",
					tc.ref.State, tc.last, tc.ref.BaseHigh, tc.want, got)
			}
			// The promotion is live-only: it must never be written back into the
			// nightly reference, or the next tick below the base high couldn't
			// undo it.
			if got := s.flagRef["X"].State; got != tc.ref.State {
				t.Fatalf("stored reference mutated: want %v got %v", tc.ref.State, got)
			}
		})
	}
}

// A "flagState cross above 2" alert is the actual breakout notification. It
// must fire on the coiling->breakout transition and exactly once, not on every
// subsequent tick that is still above the base.
func TestScannerFlagBreakoutAlertFiresOnce(t *testing.T) {
	s := New(nil)
	def := momScan()
	def.ID = "brk"
	def.Filter = core.Filter{} // match all, so membership doesn't gate the cross
	def.Columns = []string{"last", "flagState", "flagScore"}
	def.Alert = core.AlertConfig{
		Enabled: true, On: core.AlertOnCross, Sound: "ping",
		Trigger: &core.Filter{Field: core.FieldFlagState, Cmp: core.CmpCrossAbove, Value: 2.0},
	}
	s.upsert(def)
	s.SetDailyFlags(map[string]core.FlagRef{"X": textbookFlag()})
	(<-s.ctrl)(s)

	// Coiling under the base high: establishes prevVal, no cross on first sight.
	s.process(snapOf(1000, mkRow("X", 12.0, 1)))
	if gotAlert(s) {
		t.Fatal("no cross on first sight")
	}
	drain(s)

	// Price clears 12.5 -> state 1 -> 2 -> the breakout alert.
	s.process(snapOf(1250, mkRow("X", 12.6, 1)))
	a := readAlert(t, s)
	if a.Sym != "X" || a.Kind != "cross" || a.ScanID != "brk" {
		t.Fatalf("expected a breakout cross alert for X, got %+v", a)
	}
	if a.Row["flagState"] != 2 {
		t.Fatalf("the alert's row must carry the promoted state 2, got %v", a.Row["flagState"])
	}
	drain(s)

	// Extended further above the base: still a breakout, but not a NEW one.
	s.process(snapOf(1500, mkRow("X", 13.5, 1)))
	if gotAlert(s) {
		t.Fatal("breakout alert must not repeat while price stays above the base high")
	}
	drain(s)
	s.process(snapOf(1750, mkRow("X", 14.0, 1)))
	if gotAlert(s) {
		t.Fatal("breakout alert must not repeat on a second tick above the base high")
	}
}

// flagScore is a "rank my best setups" column, so a desc sort must put the
// highest score first and leave unclassified symbols at the bottom rather than
// letting them masquerade as a score of 0.
func TestScannerFlagScoreSort(t *testing.T) {
	s := New(nil)
	def := momScan()
	def.ID = "rank"
	def.Filter = core.Filter{} // match all, including the unclassified symbol
	def.Sort = []core.SortKey{{Field: core.FieldFlagScore, Dir: "desc"}}
	def.Columns = []string{"last", "flagScore"}
	s.upsert(def)

	best, mid := textbookFlag(), textbookFlag()
	best.Score, mid.Score = 91.0, 44.0
	s.SetDailyFlags(map[string]core.FlagRef{"BEST": best, "MID": mid})
	(<-s.ctrl)(s)

	s.process(snapOf(1000, mkRow("MID", 11.9, 1), mkRow("NONE", 5, 1), mkRow("BEST", 11.9, 1)))
	tk := readTick(t, s)
	if len(tk.Order) != 3 || tk.Order[0] != "BEST" || tk.Order[1] != "MID" || tk.Order[2] != "NONE" {
		t.Fatalf("flagScore desc want [BEST MID NONE] (nulls last), got %v", tk.Order)
	}

	def.Sort = []core.SortKey{{Field: core.FieldFlagScore, Dir: "asc"}}
	s.upsert(def)
	s.process(snapOf(2000, mkRow("MID", 11.9, 1), mkRow("NONE", 5, 1), mkRow("BEST", 11.9, 1)))
	tk = readTick(t, s)
	if len(tk.Order) != 3 || tk.Order[0] != "MID" || tk.Order[1] != "BEST" || tk.Order[2] != "NONE" {
		t.Fatalf("flagScore asc want [MID BEST NONE] (nulls still last), got %v", tk.Order)
	}
}

func TestScannerAlertEnterHysteresis(t *testing.T) {
	s := New(nil)
	def := momScan()
	def.Alert = core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: "ping", RequireReentry: true}
	s.upsert(def)

	// A enters -> one alert.
	s.process(snapOf(1000, mkRow("A", 10, 5)))
	a := readAlert(t, s)
	if a.Sym != "A" || a.Kind != "enter" || a.Sound != "ping" {
		t.Fatalf("alert mismatch: %+v", a)
	}
	drain(s)

	// Still in set -> no repeat alert.
	s.process(snapOf(1250, mkRow("A", 10, 6)))
	if gotAlert(s) {
		t.Fatal("should not re-alert while still matching")
	}
	drain(s)

	// A leaves (chg5m < 2) ...
	s.process(snapOf(1500, mkRow("A", 10, 1)))
	drain(s)
	// ... then re-enters -> alert again.
	s.process(snapOf(1750, mkRow("A", 10, 5)))
	if a2 := readAlert(t, s); a2.Sym != "A" {
		t.Fatalf("should re-alert after re-entry, got %+v", a2)
	}
}

func TestScannerAlertCross(t *testing.T) {
	s := New(nil)
	def := momScan()
	def.Filter = core.Filter{} // match all, so membership doesn't gate the cross
	def.Alert = core.AlertConfig{
		Enabled: true, On: core.AlertOnCross, Sound: "ping",
		Trigger: &core.Filter{Field: core.FieldChg5m, Cmp: core.CmpCrossAbove, Value: 5.0},
	}
	s.upsert(def)

	// Establish a baseline value below 5 (no cross yet — no prior value).
	s.process(snapOf(1000, mkRow("A", 10, 3)))
	if gotAlert(s) {
		t.Fatal("no cross on first sight")
	}
	drain(s)
	// Cross above 5.
	s.process(snapOf(1250, mkRow("A", 10, 6)))
	if a := readAlert(t, s); a.Sym != "A" || a.Kind != "cross" {
		t.Fatalf("expected cross alert, got %+v", a)
	}
	drain(s)
	// Stays above -> no repeat cross.
	s.process(snapOf(1500, mkRow("A", 10, 7)))
	if gotAlert(s) {
		t.Fatal("no repeat cross while staying above")
	}
}

func TestScannerResyncFull(t *testing.T) {
	s := New(nil)
	s.upsert(momScan())
	s.process(snapOf(1000, mkRow("A", 10, 5), mkRow("B", 10, 4)))
	drain(s)

	tick, ok := s.resync("m")
	if !ok || !tick.Full {
		t.Fatalf("resync should return a full tick, got ok=%v full=%v", ok, tick.Full)
	}
	if len(tick.Order) != 2 || len(tick.Rows) != 2 {
		t.Fatalf("resync full tick should carry all rows: %+v", tick)
	}
}

// End-to-end through the real engine: Apply -> engine snapshot -> scanner tick.
func TestScannerRunWithEngine(t *testing.T) {
	eng := engine.New(engine.Options{TickHz: 20})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	sc := New(eng)
	sc.Upsert(core.ScanDef{ID: "all", Name: "all", Enabled: true, Limit: 100, Columns: []string{"last"}})
	go func() { _ = sc.Run(ctx) }()

	eng.Apply(core.Agg{Sym: "AAA", Close: 10, High: 10, Low: 10, Vol: 100, EndMs: 1_700_000_000_000})

	deadline := time.After(3 * time.Second)
	for {
		select {
		case tk := <-sc.Ticks():
			for _, sym := range tk.Order {
				if sym == "AAA" {
					return
				}
			}
		case <-deadline:
			t.Fatal("no tick containing AAA within 3s")
		}
	}
}

// An evaluation that changes nothing (same order, no row body changed) must
// emit no tick and must not advance seq — the client reads a seq gap as "I
// missed one" and asks for a resync, so a silent evaluation has to be
// invisible to the sequence. The next real change then goes out as prev+1.
func TestScannerSuppressesUnchangedTick(t *testing.T) {
	s := New(nil)
	s.upsert(momScan())

	s.process(snapOf(1000, mkRow("A", 10, 5), mkRow("B", 10, 3)))
	first := readTick(t, s)

	// Identical market → silence.
	s.process(snapOf(1100, mkRow("A", 10, 5), mkRow("B", 10, 3)))
	select {
	case tk := <-s.ticks:
		t.Fatalf("unchanged evaluation emitted a tick: %+v", tk)
	default:
	}

	// A real change → one tick, seq exactly prev+1.
	s.process(snapOf(1200, mkRow("A", 11, 5), mkRow("B", 10, 3)))
	next := readTick(t, s)
	if next.Seq != first.Seq+1 {
		t.Fatalf("seq after a suppressed tick: want %d got %d", first.Seq+1, next.Seq)
	}
	if _, ok := next.Rows["A"]; !ok || len(next.Rows) != 1 {
		t.Fatalf("delta should carry only A, got %v", next.Rows)
	}

	// Membership change with no row bodies (B drops out) is still a tick.
	s.process(snapOf(1300, mkRow("A", 11, 5), mkRow("B", 10, 1)))
	drop := readTick(t, s)
	if len(drop.Order) != 1 || drop.Order[0] != "A" || len(drop.Rows) != 0 {
		t.Fatalf("exit-only tick want order [A] no rows, got %v / %v", drop.Order, drop.Rows)
	}

	// A def (re)load resets the baseline: the next evaluation re-sends
	// everything even though the market is unchanged.
	s.upsert(momScan())
	s.process(snapOf(1400, mkRow("A", 11, 5), mkRow("B", 10, 1)))
	re := readTick(t, s)
	if len(re.Rows) != 1 {
		t.Fatalf("post-reload tick should carry the full set, got %v", re.Rows)
	}
}
