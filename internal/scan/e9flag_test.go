package scan

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/engine"
)

// e9Cols are the columns the seeded 9EMA-flag scans stream, so these tests
// exercise the same wire shape the UI gets.
var e9Cols = []string{
	core.FieldLast, core.FieldChgDay, core.FieldPmVolX, core.FieldE9State, core.FieldE9Dist,
	core.FieldE9Trigger, core.FieldE9FlagBars, core.FieldE9Age, core.FieldNews,
}

// e9Fields is every 9EMA-flag field, for the "all or none" assertions (the
// two 9 EMA SLAM fields included: they follow the same candidate Null rule).
var e9Fields = []string{
	core.FieldE9State, core.FieldE9Dist, core.FieldE9Trigger,
	core.FieldE9Stop, core.FieldE9FlagBars, core.FieldE9Age,
	core.FieldE9SlamAge, core.FieldE9SlamPct,
}

func e9Row(r core.Row, field string) float64 { return fieldNum(&r, field) }

// bkRef is a fully-populated BREAKOUT verdict: state 4 (long), 3.2% above the
// 9 EMA, a 5.55 trigger and 5.12 stop over a 7-bar flag, entered at 1_000_000 ms.
func bkRef() core.EmaFlagRef {
	return core.EmaFlagRef{State: 4, DistPct: 3.2, Trigger: 5.55, Stop: 5.12, FlagBars: 7, SinceMs: 1_000_000}
}

func e9Overlay(t *testing.T, refs map[string]core.EmaFlagRef, snap *core.MetricSnapshot) *Scanner {
	t.Helper()
	s := New(nil)
	s.SetEmaFlags(refs)
	(<-s.ctrl)(s)
	s.overlayFlags(snap)
	return s
}

// The overlay must copy every field of the verdict onto the row -- widening the
// two ints, deriving the age from the SNAPSHOT clock in whole seconds -- and
// every one of them must be reachable by the filter/sort layer and shaped right
// on the wire (state and bars as ints, the rest as numbers).
func TestScannerEmaFlagOverlayStampsAllFields(t *testing.T) {
	snap := snapOf(1_000_000+125_999, mkRow("BRK", 5.60, 1)) // 125.999 s after the breakout bar
	e9Overlay(t, map[string]core.EmaFlagRef{"BRK": bkRef()}, snap)
	r := snap.Rows[0]

	for _, c := range []struct {
		field string
		got   float64
		want  float64
	}{
		{core.FieldE9State, r.E9State, 4},
		{core.FieldE9Dist, r.E9Dist, 3.2},
		{core.FieldE9Trigger, r.E9Trigger, 5.55},
		{core.FieldE9Stop, r.E9Stop, 5.12},
		{core.FieldE9FlagBars, r.E9FlagBars, 7},
		{core.FieldE9Age, r.E9Age, 125}, // whole seconds, floored: 125.999 -> 125
	} {
		if c.got != c.want {
			t.Errorf("row.%s want %v got %v", c.field, c.want, c.got)
		}
		if v := fieldNum(&r, c.field); v != c.want {
			t.Errorf("fieldNum(%s) want %v got %v (the filter/sort layer must see it)", c.field, c.want, v)
		}
	}

	if v := fieldValue(&r, core.FieldE9State); v != 4 {
		t.Errorf("e9State wire value want int(4), got %v (%T)", v, v)
	}
	if v := fieldValue(&r, core.FieldE9FlagBars); v != 7 {
		t.Errorf("e9FlagBars wire value want int(7), got %v (%T)", v, v)
	}
	if v := fieldValue(&r, core.FieldE9Dist); v != 3.2 {
		t.Errorf("e9Dist wire value want 3.2, got %v (%T)", v, v)
	}
	if v := fieldValue(&r, core.FieldE9Age); v != 125.0 {
		t.Errorf("e9Age wire value want 125, got %v (%T)", v, v)
	}

	// No cross-talk: the other overlays have no data for BRK and read Null.
	if !core.IsNull(r.FlagState) || !core.IsNull(r.IFlagState) {
		t.Errorf("flagState/iflagState must stay Null, got %v/%v", r.FlagState, r.IFlagState)
	}
}

// A symbol that is not a published candidate is UNKNOWN, not "evaluated, no
// setup": all six fields Null, nil on the wire -- and the decisive consequence,
// `e9State == 0` matches only a symbol the service actually evaluated and found
// idle, never an unevaluated ticker (which is every ticker in the market bar
// the volume leaders).
func TestScannerEmaFlagOverlayUnknownSymbolIsNullAndIdleIsNotUnknown(t *testing.T) {
	s := New(nil)
	idle := momScan()
	idle.ID = "idle"
	idle.Filter = core.Filter{Field: core.FieldE9State, Cmp: core.CmpEq, Value: 0.0}
	idle.Columns = []string{"last", "e9State", "e9Age"}
	s.upsert(idle)

	s.SetEmaFlags(map[string]core.EmaFlagRef{
		"BRK":  bkRef(),
		"IDLE": {State: 0, DistPct: -1.5, Trigger: math.NaN(), Stop: math.NaN()}, // evaluated, nothing there
	})
	(<-s.ctrl)(s)

	s.process(snapOf(2_000_000, mkRow("BRK", 5.6, 1), mkRow("IDLE", 3.0, 1), mkRow("NEWIPO", 5, 1)))
	if order := readTick(t, s).Order; len(order) != 1 || order[0] != "IDLE" {
		t.Fatalf("e9State == 0 want [IDLE] only (unknown NEWIPO and breakout BRK must not match), got %v", order)
	}

	unk := s.last.Rows[s.last.Index["NEWIPO"]]
	for _, f := range e9Fields {
		if v := e9Row(unk, f); !core.IsNull(v) {
			t.Errorf("unknown symbol: %s want Null, got %v", f, v)
		}
		if v := fieldValue(&unk, f); v != nil {
			t.Errorf("unknown symbol: %s wire value want nil, got %v", f, v)
		}
	}

	// The evaluated-idle symbol: a real 0 state, no age (it has no state-entry
	// time), no levels, but its distance to the EMA is real.
	id := s.last.Rows[s.last.Index["IDLE"]]
	if id.E9State != 0 || core.IsNull(id.E9State) {
		t.Errorf("IDLE e9State want a real 0, got %v", id.E9State)
	}
	if !core.IsNull(id.E9Age) {
		t.Errorf("IDLE has no SinceMs -> e9Age must be Null, got %v", id.E9Age)
	}
	if !core.IsNull(id.E9Trigger) || !core.IsNull(id.E9Stop) {
		t.Errorf("IDLE levels were NaN -> must be Null, got %v/%v", id.E9Trigger, id.E9Stop)
	}
	if id.E9Dist != -1.5 {
		t.Errorf("IDLE e9Dist want -1.5, got %v", id.E9Dist)
	}
}

// NaN is the emaflag package's "no such level"; it must surface as Null, and so
// must an Inf -- IsNull only knows NaN, and encoding/json refuses to marshal an
// Inf, which would drop the WHOLE tick frame for one bad level on one symbol.
func TestScannerEmaFlagOverlayNonFiniteLevelsAreNull(t *testing.T) {
	snap := snapOf(2_000_000, mkRow("BAD", 5, 1))
	e9Overlay(t, map[string]core.EmaFlagRef{"BAD": {
		State: 3, DistPct: math.Inf(1), Trigger: math.NaN(), Stop: math.Inf(-1), FlagBars: 5, SinceMs: 1_000_000,
	}}, snap)
	r := snap.Rows[0]
	for _, f := range []string{core.FieldE9Dist, core.FieldE9Trigger, core.FieldE9Stop} {
		if v := e9Row(r, f); !core.IsNull(v) {
			t.Errorf("%s non-finite must be Null, got %v", f, v)
		}
	}
	if r.E9State != 3 || r.E9FlagBars != 5 {
		t.Errorf("the finite fields must still be stamped, got state %v bars %v", r.E9State, r.E9FlagBars)
	}
	if _, err := json.Marshal(rowJSON(&r, e9Cols)); err != nil {
		t.Fatalf("a non-finite level must never reach the JSON encoder: %v", err)
	}
}

// The age counts from the verdict's SinceMs to the snapshot's own clock, so a
// publish stamped fractionally ahead of the tick (a race between the service's
// clock and the engine's) clamps to a fresh 0 instead of going negative.
func TestScannerEmaFlagAgeClampsAndCounts(t *testing.T) {
	for _, c := range []struct {
		name  string
		asOf  int64
		since int64
		want  float64 // NaN = Null
	}{
		{"same instant", 5_000, 5_000, 0},
		{"ahead of the tick clamps to 0", 5_000, 5_400, 0},
		{"59.999 s floors to 59", 64_999, 5_000, 59},
		{"exactly 5 minutes", 305_000, 5_000, 300},
		{"no state-entry time -> Null", 305_000, 0, math.NaN()},
	} {
		t.Run(c.name, func(t *testing.T) {
			snap := snapOf(c.asOf, mkRow("X", 1, 1))
			ref := bkRef()
			ref.SinceMs = c.since
			e9Overlay(t, map[string]core.EmaFlagRef{"X": ref}, snap)
			got := snap.Rows[0].E9Age
			if math.IsNaN(c.want) {
				if !core.IsNull(got) {
					t.Fatalf("want Null, got %v", got)
				}
				return
			}
			if got != c.want {
				t.Fatalf("e9Age want %v got %v", c.want, got)
			}
		})
	}
}

// SetEmaFlags is a wholesale replace through ctrl, and -- unlike the nightly and
// intraday setters, which ignore nil -- a nil or empty map is a real publish
// ("nobody is a candidate now") that must clear every verdict, or a stale
// BREAKOUT would sit in the scan (and keep its alert armed) forever. The
// overlay never writes back into the stored reference.
func TestScannerSetEmaFlagsReplacesAndNilClears(t *testing.T) {
	s := New(nil)
	stamp := func() core.Row {
		snap := snapOf(2_000_000, mkRow("A", 5, 1), mkRow("B", 5, 1))
		s.overlayFlags(snap)
		return snap.Rows[0]
	}

	s.SetEmaFlags(map[string]core.EmaFlagRef{"A": bkRef()})
	(<-s.ctrl)(s)
	if r := stamp(); r.E9State != 4 {
		t.Fatalf("A published as state 4, got %v", r.E9State)
	}
	if got := s.e9Ref["A"]; got != bkRef() {
		t.Fatalf("the overlay must not mutate the stored reference: %+v", got)
	}

	swap := core.EmaFlagRef{State: 2, DistPct: 0.4, Trigger: 6, Stop: 5, FlagBars: 3, SinceMs: 1_500_000}
	s.SetEmaFlags(map[string]core.EmaFlagRef{"A": swap, "B": swap})
	(<-s.ctrl)(s)
	snap := snapOf(2_000_000, mkRow("A", 5, 1), mkRow("B", 5, 1))
	s.overlayFlags(snap)
	if snap.Rows[0].E9State != 2 || snap.Rows[1].E9State != 2 || snap.Rows[0].E9Age != 500 {
		t.Fatalf("a new publish replaces the old one wholesale, got %v/%v age %v",
			snap.Rows[0].E9State, snap.Rows[1].E9State, snap.Rows[0].E9Age)
	}

	for name, empty := range map[string]map[string]core.EmaFlagRef{"nil": nil, "empty": {}} {
		s.SetEmaFlags(map[string]core.EmaFlagRef{"A": bkRef()})
		(<-s.ctrl)(s)
		s.SetEmaFlags(empty)
		(<-s.ctrl)(s)
		r := stamp()
		for _, f := range e9Fields {
			if v := e9Row(r, f); !core.IsNull(v) {
				t.Errorf("%s publish must clear every verdict: %s still %v", name, f, v)
			}
		}
	}
}

// pmRow is a row for the market-scan filter tests: a pre-market volume multiple
// on top of mkRow's nulls.
func pmRow(sym string, pmVolX float64) core.Row {
	r := mkRow(sym, 5, 1)
	r.PmVolX = pmVolX
	return r
}

// e9TestScans mirrors the shape of the three seeded 9EMA-flag scans (pmVolX >= 1
// AND the stage filter; the sorts that put the most actionable name first) so
// the predicate, the sort and the new fields are exercised together.
func e9TestScans() (sideways, ready, breakout core.ScanDef) {
	base := func(id string, stage core.Filter, sort ...core.SortKey) core.ScanDef {
		return core.ScanDef{
			ID: id, Name: id, Enabled: true, Limit: 50, Columns: e9Cols, Sort: sort,
			Filter: core.Filter{Op: "and", Conds: []core.Filter{
				{Field: core.FieldPmVolX, Cmp: core.CmpGTE, Value: 1.0},
				stage,
			}},
			Alert: core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: id, CooldownSec: 300, Speak: true},
		}
	}
	sideways = base("flat", core.Filter{Field: core.FieldE9State, Cmp: core.CmpGTE, Value: 2.0},
		core.SortKey{Field: core.FieldE9State, Dir: "desc"}, core.SortKey{Field: core.FieldE9Dist, Dir: "asc"})
	ready = base("ready", core.Filter{Field: core.FieldE9State, Cmp: core.CmpEq, Value: 3.0},
		core.SortKey{Field: core.FieldE9Dist, Dir: "asc"})
	breakout = base("long", core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldE9State, Cmp: core.CmpEq, Value: 4.0},
		{Field: core.FieldE9Age, Cmp: core.CmpLTE, Value: 300.0},
	}}, core.SortKey{Field: core.FieldE9Age, Dir: "asc"})
	return
}

// The predicate, sort and null rules over the new fields, end to end through
// the overlay: every stage scan picks exactly its names, in its order, and the
// pmVolX gate, the age ceiling, the idle verdict and the unknown symbol are
// each excluded for the right reason.
func TestScannerEmaFlagStageScansMatchAndSort(t *testing.T) {
	s := New(nil)
	sw, rd, bk := e9TestScans()
	s.upsert(sw)
	s.upsert(rd)
	s.upsert(bk)

	const now = int64(10_000_000)
	at := func(ageSec int64) int64 { return now - ageSec*1000 }
	s.SetEmaFlags(map[string]core.EmaFlagRef{
		"FLAT1":   {State: 2, DistPct: 1.5, Trigger: 5.2, Stop: 4.9, FlagBars: 6, SinceMs: at(200)},
		"FLAT2":   {State: 2, DistPct: 0.5, Trigger: 5.2, Stop: 4.9, FlagBars: 9, SinceMs: at(400)},
		"RDY":     {State: 3, DistPct: 2.5, Trigger: 5.2, Stop: 4.9, FlagBars: 8, SinceMs: at(100)},
		"BRKNEW":  {State: 4, DistPct: 4.0, Trigger: 5.2, Stop: 4.9, FlagBars: 8, SinceMs: at(10)},
		"BRKOLD":  {State: 4, DistPct: 6.0, Trigger: 5.2, Stop: 4.9, FlagBars: 8, SinceMs: at(600)},
		"LOWVOL":  {State: 4, DistPct: 4.0, Trigger: 5.2, Stop: 4.9, FlagBars: 8, SinceMs: at(5)},
		"IDLE":    {State: 0, DistPct: -3, Trigger: math.NaN(), Stop: math.NaN()},
		"IMPULSE": {State: 1, DistPct: 9, Trigger: math.NaN(), Stop: math.NaN(), SinceMs: at(30)},
	})
	(<-s.ctrl)(s)

	s.process(snapOf(now,
		pmRow("FLAT1", 2), pmRow("FLAT2", 1.0), pmRow("RDY", 3), pmRow("BRKNEW", 1.2), pmRow("BRKOLD", 5),
		pmRow("LOWVOL", 0.5), pmRow("IDLE", 4), pmRow("IMPULSE", 4), pmRow("UNKNOWN", 9)))

	ticks := map[string]core.ScanTick{}
	for i := 0; i < 3; i++ {
		tk := readTick(t, s)
		ticks[tk.ID] = tk
	}
	eq := func(id string, want ...string) {
		t.Helper()
		got := ticks[id].Order
		if len(got) != len(want) {
			t.Fatalf("%s want %v got %v", id, want, got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s want %v got %v", id, want, got)
			}
		}
	}
	// e9State >= 2: state desc, then distance to the EMA asc. LOWVOL (pmVolX 0.5),
	// IDLE (0), IMPULSE (1) and UNKNOWN (Null) are out.
	eq("flat", "BRKNEW", "BRKOLD", "RDY", "FLAT2", "FLAT1")
	eq("ready", "RDY")
	eq("long", "BRKNEW") // BRKOLD is 600 s old (> 300), LOWVOL fails pmVolX

	// The wire row carries the stage columns in their wire shapes.
	row := ticks["long"].Rows["BRKNEW"]
	if row[core.FieldE9State] != 4 || row[core.FieldE9FlagBars] != 8 || row[core.FieldE9Age] != 10.0 ||
		row[core.FieldE9Trigger] != 5.2 || row[core.FieldE9Dist] != 4.0 {
		t.Fatalf("breakout row on the wire: %+v", row)
	}
	if _, has := row[core.FieldE9Stop]; has {
		t.Fatalf("e9Stop is not one of the streamed columns and must not ride along: %+v", row)
	}
}

// The three seeded scans alert on ENTER with Speak. This pins the alert path for
// the new fields: the alert carries the row with e9State, fires once on entering
// the breakout stage, is suppressed inside the cooldown, fires again after it,
// and Speak survives into the stored scan definition (the scanner passes it
// through untouched -- it is a client presentation flag).
func TestScannerEmaFlagBreakoutAlertOnEnterWithCooldown(t *testing.T) {
	s := New(nil)
	_, _, bk := e9TestScans()
	s.upsert(bk)
	if !s.scans["long"].def.Alert.Speak {
		t.Fatal("Speak must be preserved on the stored scan definition")
	}

	const t0 = int64(100_000_000)
	publish := func(state int, sinceMs int64) {
		s.SetEmaFlags(map[string]core.EmaFlagRef{"X": {State: state, DistPct: 2, Trigger: 5, Stop: 4, FlagBars: 6, SinceMs: sinceMs}})
		(<-s.ctrl)(s)
	}
	step := func(asOf int64, state int) {
		publish(state, asOf-1000)
		s.process(snapOf(asOf, pmRow("X", 3)))
		drain(s)
	}

	step(t0, 3) // baseline pass: READY is not the breakout stage, and entries are suppressed anyway
	if gotAlert(s) {
		t.Fatal("no alert on the baseline evaluation")
	}
	step(t0+1_000, 4) // enters BREAKOUT
	a := readAlert(t, s)
	if a.Sym != "X" || a.Kind != "enter" || a.ScanID != "long" || a.Sound != "long" {
		t.Fatalf("want an enter alert for X from scan long with its sound, got %+v", a)
	}
	if a.Row[core.FieldE9State] != 4 || a.Row[core.FieldE9Age] != 1.0 {
		t.Fatalf("the alert row must carry the verdict (state 4, age 1 s), got %+v", a.Row)
	}

	step(t0+2_000, 3)  // falls back to READY: leaves the scan, re-arms
	step(t0+60_000, 4) // re-enters inside the 300 s cooldown
	if gotAlert(s) {
		t.Fatal("a re-entry inside the cooldown must not alert")
	}
	step(t0+61_000, 3)
	step(t0+400_000, 4) // cooldown (until t0+301 s) has expired
	if a := readAlert(t, s); a.Sym != "X" || a.Kind != "enter" {
		t.Fatalf("after the cooldown a fresh entry alerts again, got %+v", a)
	}
}

// A scan filtering on the 9EMA-flag fields is NOT a daily-flag scan: those are
// the gate for the paid Claude setup reader, which must not read 1-minute
// breakouts as if they were nightly daily-bar setups.
func TestFilterUsesFlagsIgnoresEmaFlagFields(t *testing.T) {
	for _, field := range e9Fields {
		if isFlagField(field) {
			t.Errorf("%s is a 9EMA-flag field -- it must not gate the daily setup reader", field)
		}
		if filterUsesFlags(core.Filter{Field: field, Cmp: core.CmpGT, Value: 1.0}) {
			t.Errorf("a filter on %s must not count as a daily-flag scan", field)
		}
	}
}

// End to end through a real engine and the Run loop: a republishing goroutine
// swaps verdicts (SetEmaFlags -> ctrl) while snapshots stream through the
// overlay, so -race proves the hot swap never touches the map outside the Run
// goroutine -- and a published verdict shows up in a resynced scan row while an
// unpublished symbol's stays Null.
func TestScannerRunWithEmaFlagHotSwap(t *testing.T) {
	eng := engine.New(engine.Options{TickHz: 20})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	sc := New(eng)
	sc.Upsert(core.ScanDef{ID: "all", Name: "all", Enabled: true, Limit: 100, Columns: []string{"last", "e9State", "e9Age"}})
	go func() { _ = sc.Run(ctx) }()

	stop := make(chan struct{})
	pubDone := make(chan struct{})
	go func() {
		defer close(pubDone)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			// A fresh map per publish, as the service does; AAA flips state so
			// every tick may see a different map.
			sc.SetEmaFlags(map[string]core.EmaFlagRef{"AAA": {State: 3 + i%2, DistPct: 1, Trigger: math.NaN(), Stop: math.NaN(), FlagBars: 5, SinceMs: 1_700_000_000_000}})
			time.Sleep(time.Millisecond)
		}
	}()
	defer func() { close(stop); <-pubDone }()

	go func() {
		for i := int64(0); i < 200; i++ {
			eng.Apply(core.Agg{Sym: "AAA", Close: 10, High: 10, Low: 10, Vol: 100, EndMs: 1_700_000_000_000 + i*1000})
			eng.Apply(core.Agg{Sym: "BBB", Close: 20, High: 20, Low: 20, Vol: 100, EndMs: 1_700_000_000_000 + i*1000})
			time.Sleep(time.Millisecond)
		}
	}()

	deadline := time.After(5 * time.Second)
	for {
		if tk, ok := sc.Resync("all"); ok {
			a, b := tk.Rows["AAA"], tk.Rows["BBB"]
			if a != nil && b != nil {
				if st, _ := a[core.FieldE9State].(int); (st == 3 || st == 4) && b[core.FieldE9State] == nil && b[core.FieldE9Age] == nil {
					return
				}
			}
		}
		select {
		case <-deadline:
			t.Fatal("never saw AAA carry a published verdict while BBB stayed Null")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}
