package scan

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestPDLBreakFilterAndSort(t *testing.T) {
	pred, err := compile(core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldLast, Cmp: core.CmpGT, Value: 25},
		{Field: core.FieldPdlPct, Cmp: core.CmpGT, Value: 0},
	}})
	if err != nil {
		t.Fatal(err)
	}
	a, b, c := mkRow("A", 40, 0), mkRow("B", 40, 0), mkRow("C", 20, 0)
	a.PdlPct, b.PdlPct, c.PdlPct = 2.0, 0.4, 1.0
	e := mkRow("E", 40, 0)
	e.PdlPct = core.Null
	rows := []core.Row{a, b, c, e}
	var m []core.Row
	for i := range rows {
		if pred(&rows[i]) {
			m = append(m, rows[i])
		}
	}
	sortRows(m, []core.SortKey{{Field: core.FieldPdlPct, Dir: "asc"}})
	if len(m) != 2 || m[0].Sym != "B" || m[1].Sym != "A" {
		t.Fatalf("order wrong: %d rows", len(m))
	}
}
