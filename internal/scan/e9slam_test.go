package scan

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// The 9 EMA SLAM fields (e9SlamAge / e9SlamPct): the latest SLAM of the
// symbol's current ET day, as the flagscan service publishes it on the
// EmaFlagRef (SlamMs = the slam bar's start, SlamPct = its close vs its 9 EMA).
// Same overlay, same Null rules as the other e9* fields, plus one of their own:
// a candidate WITHOUT a slam today is Null on both, never a 0 that
// `e9SlamAge <= 300` would read as "slammed just now".

// e9SlamCols are the seeded "9EMA Slam" scan's columns.
var e9SlamCols = []string{
	core.FieldLast, core.FieldChgDay, core.FieldPmVolX, core.FieldE9SlamPct, core.FieldE9SlamAge,
	core.FieldE9Dist, core.FieldNews,
}

// slamRef is SPAI 2026-10-07 just after its 08:15 ET slam: idle again (the
// FLAG FAILED of the same bar ended the flag), 4.3% under the EMA at the close
// of the slam bar, the slam bar starting at 1_000_000 ms.
func slamRef() core.EmaFlagRef {
	return core.EmaFlagRef{
		State: 0, DistPct: -2.1, Trigger: math.NaN(), Stop: math.NaN(),
		SlamMs: 1_000_000, SlamPct: -4.3252149810545255,
	}
}

func TestScannerE9SlamOverlayStampsBothFields(t *testing.T) {
	snap := snapOf(1_000_000+125_999, mkRow("SPAI", 4.57, 1)) // 125.999 s after the slam bar began
	e9Overlay(t, map[string]core.EmaFlagRef{"SPAI": slamRef()}, snap)
	r := snap.Rows[0]

	if r.E9SlamAge != 125 { // whole seconds, floored, from the SNAPSHOT clock
		t.Errorf("E9SlamAge want 125 got %v", r.E9SlamAge)
	}
	if r.E9SlamPct != -4.3252149810545255 {
		t.Errorf("E9SlamPct want -4.3252149810545255 got %v", r.E9SlamPct)
	}
	// The filter/sort layer and the wire see them (plain numbers: an age in
	// seconds and a signed %, like e9Age and e9Dist).
	if v := fieldNum(&r, core.FieldE9SlamAge); v != 125 {
		t.Errorf("fieldNum(e9SlamAge) = %v", v)
	}
	if v := fieldNum(&r, core.FieldE9SlamPct); v != -4.3252149810545255 {
		t.Errorf("fieldNum(e9SlamPct) = %v", v)
	}
	if v := fieldValue(&r, core.FieldE9SlamAge); v != 125.0 {
		t.Errorf("e9SlamAge wire value want float 125, got %v (%T)", v, v)
	}
	if v := fieldValue(&r, core.FieldE9SlamPct); v != -4.3252149810545255 {
		t.Errorf("e9SlamPct wire value got %v (%T)", v, v)
	}
	// The rest of the verdict is stamped as before: a slam is extra, it does not
	// replace the state machine's view.
	if r.E9State != 0 || core.IsNull(r.E9State) || r.E9Dist != -2.1 || !core.IsNull(r.E9Age) {
		t.Errorf("the flag verdict must be stamped unchanged: state %v dist %v age %v", r.E9State, r.E9Dist, r.E9Age)
	}
	if _, err := json.Marshal(rowJSON(&r, e9SlamCols)); err != nil {
		t.Fatalf("slam row must marshal: %v", err)
	}
}

// The Null rules: a non-candidate is Null on both (like every e9* field); a
// candidate with no slam today (SlamMs 0) is Null on both, whatever SlamPct
// holds (a zero-valued ref must not become a "0 s old, 0%" slam); a non-finite
// SlamPct is Null while the age stays real; a slam stamped ahead of the
// snapshot clamps to 0.
func TestScannerE9SlamOverlayNullRules(t *testing.T) {
	noSlam := slamRef()
	noSlam.SlamMs, noSlam.SlamPct = 0, 0 // the zero value refOf gives for a day without a slam
	noSlamNaN := slamRef()
	noSlamNaN.SlamMs, noSlamNaN.SlamPct = 0, math.NaN()
	badPct := slamRef()
	badPct.SlamPct = math.Inf(-1)
	ahead := slamRef()
	ahead.SlamMs = 2_000_400

	snap := snapOf(2_000_000,
		mkRow("NOSLAM", 5, 1), mkRow("NOSLAMNAN", 5, 1), mkRow("BADPCT", 5, 1), mkRow("AHEAD", 5, 1), mkRow("UNKNOWN", 5, 1))
	e9Overlay(t, map[string]core.EmaFlagRef{
		"NOSLAM": noSlam, "NOSLAMNAN": noSlamNaN, "BADPCT": badPct, "AHEAD": ahead,
	}, snap)
	row := func(sym string) core.Row { return snap.Rows[snap.Index[sym]] }

	for _, sym := range []string{"NOSLAM", "NOSLAMNAN", "UNKNOWN"} {
		r := row(sym)
		for _, f := range []string{core.FieldE9SlamAge, core.FieldE9SlamPct} {
			if v := fieldNum(&r, f); !core.IsNull(v) {
				t.Errorf("%s: %s want Null, got %v", sym, f, v)
			}
			if v := fieldValue(&r, f); v != nil {
				t.Errorf("%s: %s wire value want nil, got %v", sym, f, v)
			}
		}
	}
	// A candidate without a slam is still a candidate: its flag verdict is real.
	if r := row("NOSLAM"); r.E9State != 0 || core.IsNull(r.E9State) {
		t.Errorf("NOSLAM e9State want a real 0, got %v", r.E9State)
	}
	if r := row("UNKNOWN"); !core.IsNull(r.E9State) {
		t.Errorf("UNKNOWN e9State want Null, got %v", r.E9State)
	}

	if r := row("BADPCT"); r.E9SlamAge != 1000 || !core.IsNull(r.E9SlamPct) {
		t.Errorf("BADPCT: age want 1000 and pct Null, got %v / %v", r.E9SlamAge, r.E9SlamPct)
	}
	if r := row("AHEAD"); r.E9SlamAge != 0 {
		t.Errorf("AHEAD: a slam stamped ahead of the snapshot clamps to 0, got %v", r.E9SlamAge)
	}
	r := row("BADPCT")
	if _, err := json.Marshal(rowJSON(&r, e9SlamCols)); err != nil {
		t.Fatalf("a non-finite SlamPct must never reach the JSON encoder: %v", err)
	}
}

// A nil or empty publish clears the slam fields with every other verdict, and
// a later publish without the slam (the symbol re-admitted, its slam not
// replayed yet) Nulls them again rather than keeping the old one.
func TestScannerE9SlamClearsWithThePublish(t *testing.T) {
	s := New(nil)
	stamp := func() core.Row {
		snap := snapOf(1_100_000, mkRow("A", 5, 1))
		s.overlayFlags(snap)
		return snap.Rows[0]
	}
	s.SetEmaFlags(map[string]core.EmaFlagRef{"A": slamRef()})
	(<-s.ctrl)(s)
	if r := stamp(); r.E9SlamAge != 100 {
		t.Fatalf("A slammed 100 s ago, got %v", r.E9SlamAge)
	}
	if got := s.e9Ref["A"]; got.SlamMs != 1_000_000 || got.SlamPct != slamRef().SlamPct {
		t.Fatalf("the overlay must not mutate the stored reference: %+v", got)
	}

	plain := slamRef()
	plain.SlamMs, plain.SlamPct = 0, 0
	for name, m := range map[string]map[string]core.EmaFlagRef{"nil": nil, "empty": {}, "no slam": {"A": plain}} {
		s.SetEmaFlags(map[string]core.EmaFlagRef{"A": slamRef()})
		(<-s.ctrl)(s)
		s.SetEmaFlags(m)
		(<-s.ctrl)(s)
		r := stamp()
		if !core.IsNull(r.E9SlamAge) || !core.IsNull(r.E9SlamPct) {
			t.Errorf("%s publish must clear the slam: age %v pct %v", name, r.E9SlamAge, r.E9SlamPct)
		}
	}
}

// Every comparison operator over the two fields excludes the Nulls, so the
// seeded filter `e9SlamAge <= 300` matches only a real, recent slam - never a
// candidate without one (which a zero would) and never a non-candidate.
func TestPredicateE9SlamFields(t *testing.T) {
	age, err := compile(core.Filter{Field: core.FieldE9SlamAge, Cmp: core.CmpLTE, Value: 300.0})
	if err != nil {
		t.Fatal(err)
	}
	pct, err := compile(core.Filter{Field: core.FieldE9SlamPct, Cmp: core.CmpLTE, Value: -4.0})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(age, pct float64) core.Row {
		r := mkRow("X", 5, 1)
		r.E9SlamAge, r.E9SlamPct = age, pct
		return r
	}
	for _, c := range []struct {
		name             string
		r                core.Row
		wantAge, wantPct bool
	}{
		{"fresh deep slam", mk(0, -4.3), true, true},
		{"exactly 300 s", mk(300, -3.5), true, false},
		{"301 s", mk(301, -6), false, true},
		{"no slam (Null)", mk(core.Null, core.Null), false, false},
	} {
		if got := age(&c.r); got != c.wantAge {
			t.Errorf("%s: e9SlamAge <= 300 = %v, want %v", c.name, got, c.wantAge)
		}
		if got := pct(&c.r); got != c.wantPct {
			t.Errorf("%s: e9SlamPct <= -4 = %v, want %v", c.name, got, c.wantPct)
		}
	}
	// Not daily-flag fields: a slam scan must not gate the paid setup reader.
	for _, f := range []string{core.FieldE9SlamAge, core.FieldE9SlamPct} {
		if isFlagField(f) || filterUsesFlags(core.Filter{Field: f, Cmp: core.CmpLTE, Value: 1.0}) {
			t.Errorf("%s must not count as a daily-flag field", f)
		}
	}
}

// slamTestScan mirrors the seeded "9EMA Slam" scan (cmd/scanner
// emaFlagSlamScans): pmVolX >= 1 AND e9SlamAge <= 300, freshest first.
func slamTestScan() core.ScanDef {
	return core.ScanDef{
		ID: "slam", Name: "9EMA Slam", Enabled: true, Limit: 50, Columns: e9SlamCols,
		Filter: core.Filter{Op: "and", Conds: []core.Filter{
			{Field: core.FieldPmVolX, Cmp: core.CmpGTE, Value: 1.0},
			{Field: core.FieldE9SlamAge, Cmp: core.CmpLTE, Value: 300.0},
		}},
		Sort:  []core.SortKey{{Field: core.FieldE9SlamAge, Dir: "asc"}},
		Alert: core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: "slam", CooldownSec: 300, Speak: true},
	}
}

// Through process(): the seeded shape picks exactly the fresh slams of the
// volume leaders, freshest first, with the slam columns on the wire; then the
// alert path: an ENTER alert when a new slam lands, carrying the slam row, and
// leaving again once it is older than five minutes.
func TestScannerE9SlamScanMatchesSortsAndAlerts(t *testing.T) {
	s := New(nil)
	s.upsert(slamTestScan())

	const now = int64(50_000_000)
	at := func(ageSec int64) int64 { return now - ageSec*1000 }
	ref := func(slamAgeSec int64, pct float64) core.EmaFlagRef {
		r := slamRef()
		r.SlamMs, r.SlamPct = at(slamAgeSec), pct
		return r
	}
	plain := slamRef()
	plain.SlamMs, plain.SlamPct = 0, 0
	s.SetEmaFlags(map[string]core.EmaFlagRef{
		"NEW":    ref(70, -4.3),
		"MID":    ref(200, -6.1),
		"OLD":    ref(301, -5.0),
		"LOWVOL": ref(65, -3.2),
		"NONE":   plain,
	})
	(<-s.ctrl)(s)
	s.process(snapOf(now, pmRow("NEW", 1.5), pmRow("MID", 3), pmRow("OLD", 2), pmRow("LOWVOL", 0.9),
		pmRow("NONE", 4), pmRow("UNKNOWN", 9)))
	tk := readTick(t, s)
	if len(tk.Order) != 2 || tk.Order[0] != "NEW" || tk.Order[1] != "MID" {
		t.Fatalf("slam scan rows %v, want [NEW MID] (OLD is 301 s old, LOWVOL fails pmVolX, NONE and UNKNOWN are Null)", tk.Order)
	}
	row := tk.Rows["NEW"]
	if row[core.FieldE9SlamAge] != 70.0 || row[core.FieldE9SlamPct] != -4.3 || row[core.FieldE9Dist] != -2.1 {
		t.Fatalf("slam row on the wire: %+v", row)
	}
	if _, has := row[core.FieldE9State]; has {
		t.Fatalf("e9State is not one of the slam scan's columns: %+v", row)
	}
	drain(s)
	for gotAlert(s) { // first-sight alerts for NEW and MID
	}

	// A new slam on a candidate that had none: it enters, with the slam tone.
	s.SetEmaFlags(map[string]core.EmaFlagRef{"NEW": ref(70, -4.3), "MID": ref(200, -6.1), "NONE": ref(60, -3.4)})
	(<-s.ctrl)(s)
	s.process(snapOf(now+1_000, pmRow("NEW", 1.5), pmRow("MID", 3), pmRow("NONE", 4)))
	drain(s)
	a := readAlert(t, s)
	if a.Sym != "NONE" || a.Kind != "enter" || a.Sound != "slam" {
		t.Fatalf("want an enter alert for NONE with the slam tone, got %+v", a)
	}
	if a.Row[core.FieldE9SlamPct] != -3.4 {
		t.Fatalf("the alert row must carry the slam: %+v", a.Row)
	}
	if gotAlert(s) {
		t.Fatal("NEW and MID were already in the scan: no second alert")
	}

	// MID's slam (its bar began 200 s before now) ages out of the scan on
	// the snapshot clock alone, with no new publish: at now+101 s it is 301 s old.
	s.process(snapOf(now+100_000, pmRow("NEW", 1.5), pmRow("MID", 3), pmRow("NONE", 4)))
	if tk = readTick(t, s); len(tk.Order) != 3 {
		t.Fatalf("at 300 s MID is still in (inclusive): %v", tk.Order)
	}
	s.process(snapOf(now+101_000, pmRow("NEW", 1.5), pmRow("MID", 3), pmRow("NONE", 4)))
	tk = readTick(t, s)
	if len(tk.Order) != 2 || tk.Order[0] != "NONE" || tk.Order[1] != "NEW" {
		t.Fatalf("at 301 s MID must have left, freshest first: %v", tk.Order)
	}
}
