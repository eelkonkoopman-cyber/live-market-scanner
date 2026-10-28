package scan

import (
	"encoding/json"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// VOLUME CLIMAX -> BOTTOM CONFIRMED: nine engine fields (no overlay: both
// alerts are judged in the engine), filterable and sortable like any other,
// and the two seeded scans' shape firing two separate ENTER alerts in sequence.

// sigRowOf is mkRow with every dildo* / climax* field Null, the way the
// engine's toRow leaves them for a ticker with no signal today.
func sigRowOf(sym string, last float64) core.Row {
	r := mkRow(sym, last, 0)
	r.DildoAge, r.DildoBody, r.DildoVolX, r.DildoDvol = core.Null, core.Null, core.Null, core.Null
	r.DildoPbAge, r.DildoPbPx, r.DildoTouchAge, r.DildoPbState = core.Null, core.Null, core.Null, core.Null
	r.ClimaxAge, r.ClimaxDropPct, r.ClimaxVolX, r.ClimaxWickPct, r.ClimaxDvol = core.Null, core.Null, core.Null, core.Null, core.Null
	r.ClimaxLow, r.ClimaxState, r.ClimaxConfAge, r.ClimaxConfPx = core.Null, core.Null, core.Null, core.Null
	return r
}

// climaxRow is a BIYA-like row: a climax `age` s ago (low 1.78, 13.6 % drop,
// 9.6x, 34 % wick, $717k), in `state`, confirmed confAge s ago at 1.879 when
// state is 2.
func climaxRowOf(sym string, age, state, confAge float64) core.Row {
	r := sigRowOf(sym, 1.88)
	r.ClimaxAge, r.ClimaxDropPct, r.ClimaxVolX, r.ClimaxWickPct, r.ClimaxDvol = age, 13.59, 9.6, 33.7, 716_518
	r.ClimaxLow, r.ClimaxState = 1.78, state
	if state == 2 {
		r.ClimaxConfAge, r.ClimaxConfPx = confAge, 1.879
	}
	return r
}

var climaxFields = []string{
	core.FieldClimaxAge, core.FieldClimaxDropPct, core.FieldClimaxVolX, core.FieldClimaxWickPct, core.FieldClimaxDvol,
	core.FieldClimaxLow, core.FieldClimaxState, core.FieldClimaxConfAge, core.FieldClimaxConfPx,
}

// Every field is reachable by fieldNum (so by filters and sorts) and shaped
// right on the wire: climaxState an int, the rest numbers, Null -> nil.
func TestPredicateClimaxFields(t *testing.T) {
	r := climaxRowOf("BIYA", 63, 2, 3)
	want := map[string]float64{
		core.FieldClimaxAge: 63, core.FieldClimaxDropPct: 13.59, core.FieldClimaxVolX: 9.6, core.FieldClimaxWickPct: 33.7,
		core.FieldClimaxDvol: 716_518, core.FieldClimaxLow: 1.78, core.FieldClimaxState: 2, core.FieldClimaxConfAge: 3,
		core.FieldClimaxConfPx: 1.879,
	}
	for _, f := range climaxFields {
		if v := fieldNum(&r, f); v != want[f] {
			t.Errorf("fieldNum(%s) = %v, want %v", f, v, want[f])
		}
		p, err := compile(core.Filter{Field: f, Cmp: core.CmpGTE, Value: want[f]})
		if err != nil || !p(&r) {
			t.Errorf("%s >= %v must match (err %v)", f, want[f], err)
		}
		null := sigRowOf("NONE", 2)
		if p(&null) {
			t.Errorf("%s: a Null must never match", f)
		}
		if v := fieldValue(&null, f); v != nil {
			t.Errorf("%s: Null wire value %v, want nil", f, v)
		}
	}
	if v := fieldValue(&r, core.FieldClimaxState); v != 2 {
		t.Errorf("climaxState on the wire %v (%T), want int 2", v, v)
	}
	if v := fieldValue(&r, core.FieldClimaxAge); v != 63.0 {
		t.Errorf("climaxAge on the wire %v (%T), want float 63", v, v)
	}
	eq, _ := compile(core.Filter{Field: core.FieldClimaxState, Cmp: core.CmpEq, Value: 2})
	for st, wantMatch := range map[float64]bool{1: false, 2: true, 3: false, 4: false} {
		row := climaxRowOf("X", 63, st, 3)
		if eq(&row) != wantMatch {
			t.Errorf("climaxState == 2 on state %v: %v", st, !wantMatch)
		}
	}
	if _, err := json.Marshal(rowJSON(&r, climaxFields)); err != nil {
		t.Fatal(err)
	}
	// Sorted by climaxAge asc, Null last.
	rows := []core.Row{climaxRowOf("OLD", 200, 1, 0), sigRowOf("NONE", 2), climaxRowOf("NEW", 70, 1, 0)}
	sortRows(rows, []core.SortKey{{Field: core.FieldClimaxAge, Dir: "asc"}})
	if rows[0].Sym != "NEW" || rows[1].Sym != "OLD" || rows[2].Sym != "NONE" {
		t.Fatalf("sort %s %s %s", rows[0].Sym, rows[1].Sym, rows[2].Sym)
	}
}

// The seeded climax scans' rules (cmd/scanner climaxScans; restated here, the
// cmd test pins the seeded defs to the same shape).
func climaxTestScans() (climax1m, confirm core.ScanDef) {
	alert := func(sound string) core.AlertConfig {
		return core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: sound, CooldownSec: 600, Speak: true}
	}
	climax1m = core.ScanDef{ID: "climax-1m", Name: "Volume Climax", Enabled: true, Limit: 50,
		Filter: core.Filter{Op: "and", Conds: []core.Filter{
			{Field: core.FieldClimaxAge, Cmp: core.CmpLTE, Value: 240},
			{Field: core.FieldLast, Cmp: core.CmpGTE, Value: 0.50},
			{Field: core.FieldClimaxDvol, Cmp: core.CmpGTE, Value: 50000},
		}},
		Sort:  []core.SortKey{{Field: core.FieldClimaxAge, Dir: "asc"}},
		Alert: alert("climax"),
	}
	confirm = core.ScanDef{ID: "climax-confirm", Name: "Bottom Confirmed", Enabled: true, Limit: 50,
		Filter: core.Filter{Op: "and", Conds: []core.Filter{
			{Field: core.FieldClimaxState, Cmp: core.CmpEq, Value: 2},
			{Field: core.FieldClimaxConfAge, Cmp: core.CmpLTE, Value: 240},
			{Field: core.FieldClimaxAge, Cmp: core.CmpLTE, Value: 1200},
		}},
		Sort:  []core.SortKey{{Field: core.FieldClimaxConfAge, Dir: "asc"}},
		Alert: alert("bottom"),
	}
	return
}

// The two alerts fire in sequence for one symbol, each once, from its own scan:
// the climax enters "Volume Climax" when its bar is judged; a minute later the
// confirmation enters "Bottom Confirmed" while the symbol simply stays on the
// first scan (no second alert there). A confirmation more than 20 minutes
// after its climax (Decision C) never enters.
func TestScannerClimaxAlertsFireInSequence(t *testing.T) {
	s := New(nil)
	c1, cf := climaxTestScans()
	s.upsert(c1)
	s.upsert(cf)
	const t0 = int64(1_800_000_000_000)
	s.process(snapOf(t0)) // the post-load baseline
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

	s.process(snapOf(t0+63_000, climaxRowOf("BIYA", 63, 1, 0)))
	got := alerts()
	if len(got) != 1 || got[0].ScanID != "climax-1m" || got[0].Sym != "BIYA" || got[0].Sound != "climax" || got[0].Kind != "enter" {
		t.Fatalf("climax: alerts %+v, want one climax-1m enter for BIYA with the climax tone", got)
	}

	s.process(snapOf(t0+123_000, climaxRowOf("BIYA", 123, 2, 63)))
	got = alerts()
	if len(got) != 1 || got[0].ScanID != "climax-confirm" || got[0].Sym != "BIYA" || got[0].Sound != "bottom" {
		t.Fatalf("confirmation: alerts %+v, want exactly one climax-confirm enter (BIYA stays on climax-1m silently)", got)
	}
	s.process(snapOf(t0+124_000, climaxRowOf("BIYA", 124, 2, 64)))
	if got = alerts(); len(got) != 0 {
		t.Fatalf("staying on both scans must not re-alert: %+v", got)
	}

	// Decision C: a thin name confirms 21 minutes after its climax: no alert.
	s.process(snapOf(t0+200_000, climaxRowOf("THIN", 1263, 2, 63)))
	if got = alerts(); len(got) != 0 {
		t.Fatalf("a late confirmation (climaxAge 1263 > 1200) must not alert: %+v", got)
	}
	s.process(snapOf(t0+201_000, climaxRowOf("THIN2", 1200, 2, 63)))
	if got = alerts(); len(got) != 1 || got[0].Sym != "THIN2" {
		t.Fatalf("climaxAge exactly 1200 is inside: %+v", got)
	}
	// A failed or expired climax never confirms.
	s.process(snapOf(t0+202_000, climaxRowOf("FAIL", 300, 3, 0), climaxRowOf("EXP", 300, 4, 0)))
	if got = alerts(); len(got) != 0 {
		t.Fatalf("states 3/4 must not enter the confirm scan: %+v", got)
	}
}
