package scan

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestMaxLastCeilingLT(t *testing.T) {
	v, ok := maxLastCeiling(core.Filter{Field: core.FieldLast, Cmp: core.CmpLT, Value: 20.0})
	if !ok || v != 20 {
		t.Fatalf("want (20,true), got (%v,%v)", v, ok)
	}
}

func TestMaxLastCeilingLTE(t *testing.T) {
	v, ok := maxLastCeiling(core.Filter{Field: core.FieldLast, Cmp: core.CmpLTE, Value: 5.0})
	if !ok || v != 5 {
		t.Fatalf("want (5,true), got (%v,%v)", v, ok)
	}
}

func TestMaxLastCeilingBetweenUsesHighEnd(t *testing.T) {
	v, ok := maxLastCeiling(core.Filter{Field: core.FieldLast, Cmp: core.CmpBetween, Value: []any{1.0, 20.0}})
	if !ok || v != 20 {
		t.Fatalf("want the HIGH end (20,true), got (%v,%v)", v, ok)
	}
}

func TestMaxLastCeilingIgnoresOtherFieldsAndComparators(t *testing.T) {
	if _, ok := maxLastCeiling(core.Filter{Field: core.FieldChg5m, Cmp: core.CmpLT, Value: 20.0}); ok {
		t.Fatal("a ceiling on a non-last field must not count")
	}
	if _, ok := maxLastCeiling(core.Filter{Field: core.FieldLast, Cmp: core.CmpGT, Value: 20.0}); ok {
		t.Fatal("last > N is a FLOOR, not a ceiling -- must not count")
	}
}

func TestMaxLastCeilingWalksNestedGroupsAndTakesLargest(t *testing.T) {
	f := core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldChg5m, Cmp: core.CmpGT, Value: 2.0},
		{Op: "or", Conds: []core.Filter{
			{Field: core.FieldLast, Cmp: core.CmpLT, Value: 5.0},
			{Field: core.FieldLast, Cmp: core.CmpLT, Value: 20.0},
		}},
	}}
	v, ok := maxLastCeiling(f)
	if !ok || v != 20 {
		t.Fatalf("want the largest ceiling found anywhere in the tree (20,true), got (%v,%v)", v, ok)
	}
}

func TestPriceCeilingIgnoresDisabledScans(t *testing.T) {
	s := New(nil)
	s.upsert(core.ScanDef{ID: "a", Name: "a", Enabled: false,
		Filter: core.Filter{Field: core.FieldLast, Cmp: core.CmpLT, Value: 20.0}})
	if c := s.priceCeiling(); c != 0 {
		t.Fatalf("a disabled scan's ceiling must not count, got %v", c)
	}
}

func TestIsPriorityUnderCeilingOnly(t *testing.T) {
	s := New(nil)
	s.upsert(core.ScanDef{ID: "cheap", Name: "cheap", Enabled: true,
		Filter: core.Filter{Field: core.FieldLast, Cmp: core.CmpLT, Value: 20.0}})
	s.process(snapOf(1000, mkRow("CHP", 10, 5), mkRow("EXP", 250, 5)))

	if !s.isPriority("CHP") {
		t.Fatal("CHP is $10, under the $20 ceiling -- should be priority")
	}
	if s.isPriority("EXP") {
		t.Fatal("EXP is $250, over the $20 ceiling -- should not be priority")
	}
	if s.isPriority("UNKNOWN") {
		t.Fatal("a symbol with no row at all must not be priority")
	}
}

func TestIsPriorityOffWithNoScanDefiningACeiling(t *testing.T) {
	s := New(nil)
	s.upsert(momScan()) // chg5m > 2, no price ceiling anywhere
	s.process(snapOf(1000, mkRow("CHP", 10, 5), mkRow("EXP", 250, 5)))

	if s.isPriority("CHP") || s.isPriority("EXP") {
		t.Fatal("with no scan defining a price ceiling, nothing should be flagged priority")
	}
}
