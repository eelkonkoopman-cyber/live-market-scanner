package scan

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// The ignition fields are filterable and Null -> excluded.
func TestPredicateIgnitionFields(t *testing.T) {
	r := sigRowOf("NCPL", 1.88)
	r.IgnAge, r.IgnBodyPct, r.IgnVolX = 60, 5.03, 15
	if fieldNum(&r, core.FieldIgnAge) != 60 || fieldNum(&r, core.FieldIgnBodyPct) != 5.03 || fieldNum(&r, core.FieldIgnVolX) != 15 {
		t.Fatal("fieldNum")
	}
	p, err := compile(core.Filter{Field: core.FieldIgnAge, Cmp: core.CmpLTE, Value: 240})
	if err != nil || !p(&r) {
		t.Fatalf("must match (err %v)", err)
	}
	r.IgnAge = core.Null
	if p(&r) {
		t.Fatal("Null must not match")
	}
}

func TestIgnFreshField(t *testing.T) {
	r := core.Row{IgnFresh: 1}
	if fieldNum(&r, core.FieldIgnFresh) != 1 {
		t.Fatal("ignFresh")
	}
}

// The seeded Ignition filter (ignAge <= 240 AND ignPass == 1): a passing
// ignition matches (alerts), a skipped one or no verdict does not.
func TestIgnPassFilter(t *testing.T) {
	p, err := compile(core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldIgnAge, Cmp: core.CmpLTE, Value: 240},
		{Field: core.FieldIgnPass, Cmp: core.CmpEq, Value: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	r := sigRowOf("NCPL", 1.88)
	r.IgnAge, r.IgnPass = 60, 1
	if fieldNum(&r, core.FieldIgnPass) != 1 || !p(&r) {
		t.Fatal("passing ignition must match")
	}
	r.IgnPass = 0
	if p(&r) {
		t.Fatal("skipped ignition must not match")
	}
	r.IgnPass = core.Null
	if p(&r) {
		t.Fatal("Null must not match")
	}
}
