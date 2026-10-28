package scan

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// GREEN DILDO -> 9 EMA PULLBACK: four engine fields (alert 1, the engine's
// judge) and four overlay fields (alert 2, flagscan's pullback on the chart's
// bars, riding on the EmaFlagRef), plus the chart-anchor union.

var dildoFields = []string{
	core.FieldDildoAge, core.FieldDildoBody, core.FieldDildoVolX, core.FieldDildoDvol,
	core.FieldDildoPbAge, core.FieldDildoPbPx, core.FieldDildoTouchAge, core.FieldDildoPbState,
}

// engineDildo is a row whose engine judge latched a dildo `age` s ago.
func engineDildo(sym string, age float64) core.Row {
	r := sigRowOf(sym, 0.58)
	r.DildoAge, r.DildoBody, r.DildoVolX, r.DildoDvol = age, 18.4, 25, 420_000
	return r
}

// pbRef is a flagscan verdict for a symbol whose 1_000_000 ms dildo pulled back
// to the 9 EMA on the 1_300_000 ms bar (closing at 0.6079).
func pbRef() core.EmaFlagRef {
	return core.EmaFlagRef{
		State: 1, DistPct: 2, Trigger: math.NaN(), Stop: math.NaN(),
		DildoMs: 1_000_000, DildoBody: 18.4, DildoBodyX: 6.1, DildoVolX: 25, DildoDvol: 420_000,
		PbState: 3, TouchMs: 1_300_000, PbMs: 1_300_000, PbPx: 0.6079,
	}
}

func dildoOverlay(t *testing.T, refs map[string]core.EmaFlagRef, snap *core.MetricSnapshot) {
	t.Helper()
	e9Overlay(t, refs, snap)
}

// Every field reaches the filter/sort layer and the wire (dildoPbState an int).
func TestPredicateDildoFields(t *testing.T) {
	r := engineDildo("PDSB", 63)
	r.DildoPbAge, r.DildoPbPx, r.DildoTouchAge, r.DildoPbState = 5, 0.6079, 5, 3
	want := map[string]float64{
		core.FieldDildoAge: 63, core.FieldDildoBody: 18.4, core.FieldDildoVolX: 25, core.FieldDildoDvol: 420_000,
		core.FieldDildoPbAge: 5, core.FieldDildoPbPx: 0.6079, core.FieldDildoTouchAge: 5, core.FieldDildoPbState: 3,
	}
	null := sigRowOf("NONE", 1)
	for _, f := range dildoFields {
		if v := fieldNum(&r, f); v != want[f] {
			t.Errorf("fieldNum(%s) = %v, want %v", f, v, want[f])
		}
		p, err := compile(core.Filter{Field: f, Cmp: core.CmpLTE, Value: want[f]})
		if err != nil || !p(&r) || p(&null) {
			t.Errorf("%s <= %v: matches %v, Null matches %v (err %v)", f, want[f], p(&r), p(&null), err)
		}
		if v := fieldValue(&null, f); v != nil {
			t.Errorf("%s: Null wire value %v", f, v)
		}
	}
	if v := fieldValue(&r, core.FieldDildoPbState); v != 3 {
		t.Errorf("dildoPbState on the wire %v (%T), want int 3", v, v)
	}
	eq, _ := compile(core.Filter{Field: core.FieldDildoPbState, Cmp: core.CmpEq, Value: 3})
	for st := 0; st <= 5; st++ {
		row := engineDildo("X", 63)
		row.DildoPbState = float64(st)
		if eq(&row) != (st == 3) {
			t.Errorf("dildoPbState == 3 on state %d", st)
		}
	}
	if _, err := json.Marshal(rowJSON(&r, dildoFields)); err != nil {
		t.Fatal(err)
	}
}

// The overlay's Null rules and the stamping.
func TestScannerDildoOverlay(t *testing.T) {
	noPb := pbRef()
	noPb.PbState, noPb.TouchMs, noPb.PbMs, noPb.PbPx = 1, 0, 0, math.NaN() // watching
	touching := pbRef()
	touching.PbState, touching.PbMs, touching.PbPx, touching.TouchMs = 2, 0, math.NaN(), 1_360_000
	noDildo := core.EmaFlagRef{State: 0, DistPct: 1, Trigger: math.NaN(), Stop: math.NaN()} // evaluated, no dildo today
	infX := pbRef()
	infX.DildoVolX = math.Inf(1)

	const asOf = 1_365_500
	snap := snapOf(asOf,
		engineDildo("NOREF", 70), sigRowOf("NONE", 1), sigRowOf("PB", 0.61), sigRowOf("WATCH", 0.6),
		sigRowOf("TOUCH", 0.6), sigRowOf("IDLE", 3), sigRowOf("INF", 0.6))
	dildoOverlay(t, map[string]core.EmaFlagRef{"PB": pbRef(), "WATCH": noPb, "TOUCH": touching, "IDLE": noDildo, "INF": infX}, snap)
	row := func(sym string) core.Row { return snap.Rows[snap.Index[sym]] }

	// No ref: the overlay's four are Null, the engine's dildo stays.
	if r := row("NOREF"); r.DildoAge != 70 || r.DildoBody != 18.4 || !core.IsNull(r.DildoPbAge) || !core.IsNull(r.DildoPbState) ||
		!core.IsNull(r.DildoTouchAge) || !core.IsNull(r.DildoPbPx) {
		t.Fatalf("NOREF %+v", r)
	}
	if r := row("NONE"); !core.IsNull(r.DildoAge) || !core.IsNull(r.DildoPbState) {
		t.Fatal("no ref, no engine dildo: all Null")
	}
	// A confirmed pullback: its age from the pullback bar's START, its close, the
	// touch age, state 3; the ref's dildo fills the engine-less row (union).
	r := row("PB")
	if r.DildoPbAge != 65 || r.DildoPbPx != 0.6079 || r.DildoTouchAge != 65 || r.DildoPbState != 3 {
		t.Fatalf("PB pullback fields %v %v %v %v", r.DildoPbAge, r.DildoPbPx, r.DildoTouchAge, r.DildoPbState)
	}
	if r.DildoAge != 365 || r.DildoBody != 18.4 || r.DildoVolX != 25 || r.DildoDvol != 420_000 {
		t.Fatalf("PB union: %v %v %v %v", r.DildoAge, r.DildoBody, r.DildoVolX, r.DildoDvol)
	}
	// Watching: PbMs 0 -> Null age and price, no touch yet.
	if r := row("WATCH"); r.DildoPbState != 1 || !core.IsNull(r.DildoPbAge) || !core.IsNull(r.DildoPbPx) || !core.IsNull(r.DildoTouchAge) {
		t.Fatalf("WATCH %v %v %v %v", r.DildoPbState, r.DildoPbAge, r.DildoPbPx, r.DildoTouchAge)
	}
	if r := row("TOUCH"); r.DildoPbState != 2 || r.DildoTouchAge != 5 || !core.IsNull(r.DildoPbAge) {
		t.Fatalf("TOUCH %v %v %v", r.DildoPbState, r.DildoTouchAge, r.DildoPbAge)
	}
	// Evaluated, no dildo today: a real 0, every age Null, the dildo Null.
	if r := row("IDLE"); r.DildoPbState != 0 || core.IsNull(r.DildoPbState) || !core.IsNull(r.DildoAge) || !core.IsNull(r.DildoPbAge) {
		t.Fatalf("IDLE %v %v %v", r.DildoPbState, r.DildoAge, r.DildoPbAge)
	}
	if r := row("INF"); !core.IsNull(r.DildoVolX) || r.DildoAge != 365 {
		t.Fatalf("INF: a +Inf multiple must be Null: %v", r.DildoVolX)
	}
	for _, sym := range []string{"PB", "INF", "TOUCH"} {
		rr := row(sym)
		if _, err := json.Marshal(rowJSON(&rr, dildoFields)); err != nil {
			t.Fatalf("%s: %v", sym, err)
		}
	}
}

// A HotOnly ref (tracked only for its dildo) leaves EVERY e9* field Null and
// fills only the dildo fields; the same verdict on a candidate stamps both.
func TestScannerDildoHotOnlyLeavesE9Null(t *testing.T) {
	hot := pbRef()
	hot.HotOnly = true
	hot.State, hot.SlamMs, hot.SlamPct, hot.SinceMs = 2, 1_100_000, -3, 1_000_000
	cand := hot
	cand.HotOnly = false
	snap := snapOf(1_365_500, sigRowOf("HOT", 0.6), sigRowOf("CAND", 0.6))
	dildoOverlay(t, map[string]core.EmaFlagRef{"HOT": hot, "CAND": cand}, snap)
	h, c := snap.Rows[snap.Index["HOT"]], snap.Rows[snap.Index["CAND"]]
	for _, f := range e9Fields {
		if v := fieldNum(&h, f); !core.IsNull(v) {
			t.Errorf("HotOnly %s = %v, want Null", f, v)
		}
	}
	if h.DildoPbState != 3 || h.DildoPbAge != 65 || h.DildoAge != 365 {
		t.Fatalf("HotOnly dildo fields %v %v %v", h.DildoPbState, h.DildoPbAge, h.DildoAge)
	}
	if c.E9State != 2 || c.E9SlamAge != 265 || c.DildoPbState != 3 {
		t.Fatalf("candidate: e9State %v e9SlamAge %v dildoPbState %v", c.E9State, c.E9SlamAge, c.DildoPbState)
	}
	// A user-built e9 scan with no pmVolX term never shows the hot-only name.
	p, _ := compile(core.Filter{Field: core.FieldE9State, Cmp: core.CmpGTE, Value: 0})
	if p(&h) || !p(&c) {
		t.Fatal("e9State >= 0 must match the candidate and never the hot-only name")
	}
}

// The chart-anchor union picks the NEWER anchor; the same bar seen by both
// sides keeps the engine's values and age (it cannot re-enter).
func TestScannerDildoChartAnchorUnion(t *testing.T) {
	const asOf = 1_365_500
	older := pbRef() // ref dildo at 1_000_000: 365 s old
	same := engineDildo("SAME", 365)
	same.DildoBody = 18.39 // the engine's own measure of the same bar
	newerEngine := engineDildo("ENG", 100)
	snap := snapOf(asOf, same, newerEngine, engineDildo("REF", 900))
	dildoOverlay(t, map[string]core.EmaFlagRef{"SAME": older, "ENG": older, "REF": older}, snap)
	row := func(sym string) core.Row { return snap.Rows[snap.Index[sym]] }
	if r := row("SAME"); r.DildoAge != 365 || r.DildoBody != 18.39 {
		t.Fatalf("the same bar must keep the engine's values: age %v body %v", r.DildoAge, r.DildoBody)
	}
	if r := row("ENG"); r.DildoAge != 100 {
		t.Fatalf("the engine's newer dildo must stay: %v", r.DildoAge)
	}
	if r := row("REF"); r.DildoAge != 365 || r.DildoBody != 18.4 {
		t.Fatalf("the ref's newer dildo must replace an older engine one: %v %v", r.DildoAge, r.DildoBody)
	}
}

// The seeded dildo scans' rules (cmd/scanner dildoScans; the cmd test pins the
// seeded defs to the same shape).
func dildoTestScans() (d1m, pb core.ScanDef) {
	alert := func(sound string) core.AlertConfig {
		return core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: sound, CooldownSec: 600, Speak: true}
	}
	d1m = core.ScanDef{ID: "dildo-1m", Name: "Green Dildo", Enabled: true, Limit: 50,
		Filter: core.Filter{Op: "and", Conds: []core.Filter{
			{Field: core.FieldDildoAge, Cmp: core.CmpLTE, Value: 240},
			{Field: core.FieldLast, Cmp: core.CmpGTE, Value: 0.20},
			{Field: core.FieldDildoDvol, Cmp: core.CmpGTE, Value: 100000},
		}},
		Sort:  []core.SortKey{{Field: core.FieldDildoAge, Dir: "asc"}},
		Alert: alert("dildo"),
	}
	pb = core.ScanDef{ID: "dildo-pb", Name: "Dildo 9EMA Pullback", Enabled: true, Limit: 50,
		Filter: core.Filter{Op: "and", Conds: []core.Filter{
			{Field: core.FieldDildoPbState, Cmp: core.CmpEq, Value: 3},
			{Field: core.FieldDildoPbAge, Cmp: core.CmpLTE, Value: 240},
		}},
		Sort:  []core.SortKey{{Field: core.FieldDildoPbAge, Dir: "asc"}},
		Alert: alert("long"),
	}
	return
}

// ENTER fires "Green Dildo" when the engine's dildo appears and then, once the
// pullback is confirmed, "Dildo 9EMA Pullback" - two separate alerts for one
// symbol, each once; the chart's view of the same bar arriving later (the
// union) does not re-fire the first.
func TestScannerDildoAlertsFireInSequence(t *testing.T) {
	s := New(nil)
	d1m, pb := dildoTestScans()
	s.upsert(d1m)
	s.upsert(pb)
	const dildoMs = int64(1_800_000_000_000)
	s.process(snapOf(dildoMs)) // the post-load baseline
	drain(s)
	alerts := func() []core.AlertMsg {
		var out []core.AlertMsg
		for {
			select {
			case a := <-s.alerts:
				out = append(out, a)
			default:
				return out
			}
		}
	}

	// Alert 1: the engine's dildo, 63 s after its bar began.
	s.process(snapOf(dildoMs+63_000, engineDildo("PDSB", 63)))
	if got := alerts(); len(got) != 1 || got[0].ScanID != "dildo-1m" || got[0].Sound != "dildo" || got[0].Sym != "PDSB" {
		t.Fatalf("alert 1: %+v", got)
	}
	// flagscan publishes the same dildo (watching): same bar, same age, no re-entry.
	watch := pbRef()
	watch.DildoMs, watch.PbState, watch.TouchMs, watch.PbMs, watch.PbPx = dildoMs, 1, 0, 0, math.NaN()
	s.SetEmaFlags(map[string]core.EmaFlagRef{"PDSB": watch})
	(<-s.ctrl)(s)
	s.process(snapOf(dildoMs+70_000, engineDildo("PDSB", 70)))
	if got := alerts(); len(got) != 0 {
		t.Fatalf("the same dildo from both sides must not re-alert: %+v", got)
	}
	// Alert 2: the pullback confirmed on the 09:36-like bar, 63 s after it began.
	conf := watch
	conf.PbState, conf.TouchMs, conf.PbMs, conf.PbPx = 3, dildoMs+6*60_000, dildoMs+6*60_000, 0.6079
	s.SetEmaFlags(map[string]core.EmaFlagRef{"PDSB": conf})
	(<-s.ctrl)(s)
	s.process(snapOf(dildoMs+423_000, engineDildo("PDSB", 423)))
	got := alerts()
	if len(got) != 1 || got[0].ScanID != "dildo-pb" || got[0].Sound != "long" || got[0].Sym != "PDSB" {
		t.Fatalf("alert 2: %+v", got)
	}
}
