package scan

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// serveCtrl runs the ctrl-channel drain loop the way Run does, so the exported
// reply-waiting accessors (ScanUsesFlags, FlagFor) can be called from a test
// without standing up an engine. Run itself dereferences s.eng immediately, so
// it can't be used with New(nil).
func serveCtrl(t *testing.T, s *Scanner) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		for {
			select {
			case fn := <-s.ctrl:
				fn(s)
			case <-done:
				return
			}
		}
	}()
	t.Cleanup(func() { close(done) })
}

func TestFilterUsesFlagsEveryFlagFieldAsALeaf(t *testing.T) {
	for _, field := range []string{
		core.FieldFlagScore, core.FieldFlagState, core.FieldFlagPole,
		core.FieldFlagRange, core.FieldFlagRetrace, core.FieldFlagVolDry,
		core.FieldFlagBars,
	} {
		if !filterUsesFlags(core.Filter{Field: field, Cmp: core.CmpGT, Value: 1.0}) {
			t.Errorf("%s is a flag field -- filterUsesFlags must say true", field)
		}
	}
}

func TestFilterUsesFlagsIgnoresOtherFields(t *testing.T) {
	for _, field := range []string{
		core.FieldLast, core.FieldChg5m, core.FieldRvol, core.FieldHits,
		core.FieldDTrend, core.FieldChg20d, "", "flag", "flagWhatever",
	} {
		if filterUsesFlags(core.Filter{Field: field, Cmp: core.CmpGT, Value: 1.0}) {
			t.Errorf("%q is not a flag field -- filterUsesFlags must say false", field)
		}
	}
	// dtrend/chg20d are the OTHER nightly overlay: near neighbours that must
	// not drag a plain trend scan into paid setup reads.
	if isFlagField(core.FieldDTrend) || isFlagField(core.FieldChg20d) {
		t.Fatal("the daily-trend fields are not flag fields")
	}
}

func TestFilterUsesFlagsWalksNestedGroups(t *testing.T) {
	f := core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldLast, Cmp: core.CmpBetween, Value: []any{1.0, 20.0}},
		{Op: "or", Conds: []core.Filter{
			{Field: core.FieldRvol, Cmp: core.CmpGT, Value: 2.0},
			{Field: core.FieldFlagRetrace, Cmp: core.CmpLT, Value: 40.0},
		}},
	}}
	if !filterUsesFlags(f) {
		t.Fatal("a flag leaf nested two groups deep still counts")
	}

	none := core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldLast, Cmp: core.CmpLT, Value: 20.0},
		{Op: "or", Conds: []core.Filter{
			{Field: core.FieldRvol, Cmp: core.CmpGT, Value: 2.0},
			{Field: core.FieldChg5m, Cmp: core.CmpGT, Value: 5.0},
		}},
	}}
	if filterUsesFlags(none) {
		t.Fatal("no flag leaf anywhere -- must be false")
	}
	if filterUsesFlags(core.Filter{}) {
		t.Fatal("the empty match-all filter uses no fields at all")
	}
}

func TestScanUsesFlagsPerScanID(t *testing.T) {
	s := New(nil)
	serveCtrl(t, s)

	setups := momScan()
	setups.ID = "setups"
	setups.Filter = core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldFlagScore, Cmp: core.CmpGTE, Value: 70.0},
		{Field: core.FieldFlagState, Cmp: core.CmpGTE, Value: 1.0},
	}}
	s.Upsert(setups)
	s.Upsert(momScan()) // id "m": chg5m only

	if !s.ScanUsesFlags("setups") {
		t.Fatal("the setups scan filters on flagScore/flagState")
	}
	if s.ScanUsesFlags("m") {
		t.Fatal("a plain momentum scan must not be treated as a flag scan")
	}
	if s.ScanUsesFlags("nosuchscan") {
		t.Fatal("an unknown scan id has no filter and no flag intent")
	}
}

// A scan that only SORTS or DISPLAYS the flag columns is not a flag scan: the
// user did not express the intent to shortlist setups, so its entrants must
// not earn a paid read.
func TestScanUsesFlagsIgnoresSortAndColumns(t *testing.T) {
	s := New(nil)
	def := momScan()
	def.ID = "cols"
	def.Sort = []core.SortKey{{Field: core.FieldFlagScore, Dir: "desc"}}
	def.Columns = []string{"last", "flagScore", "flagState"}
	s.upsert(def)

	if s.scanUsesFlags("cols") {
		t.Fatal("sorting on or showing flagScore is not filtering on it")
	}
}

func TestFlagForReturnsStoredReference(t *testing.T) {
	s := New(nil)
	serveCtrl(t, s)
	s.SetDailyFlags(map[string]core.FlagRef{"TIGHT": textbookFlag()})

	fr, ok := s.FlagFor("TIGHT")
	if !ok {
		t.Fatal("TIGHT was classified -- want ok")
	}
	// The two fields the Row overlay deliberately drops are the reason this
	// accessor exists.
	if fr.AsOf != "2026-07-30" {
		t.Fatalf("AsOf (the setup read's cache key) want 2026-07-30, got %q", fr.AsOf)
	}
	if fr.BaseHigh != 12.5 {
		t.Fatalf("BaseHigh want 12.5, got %v", fr.BaseHigh)
	}
	if fr.Score != 82.5 || fr.State != 1 || fr.Bars != 7 {
		t.Fatalf("geometry not carried through: %+v", fr)
	}

	if _, ok := s.FlagFor("NEWIPO"); ok {
		t.Fatal("an unclassified symbol must report ok=false, not a zero FlagRef")
	}
}
