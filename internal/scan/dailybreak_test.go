package scan

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestDailyBreakFilterAndSort(t *testing.T) {
	pred, err := compile(core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldLast, Cmp: core.CmpGT, Value: 30},
		{Field: core.FieldDbPct, Cmp: core.CmpGT, Value: 0},
	}})
	if err != nil {
		t.Fatal(err)
	}
	a, b, c, d := mkRow("A", 40, 0), mkRow("B", 40, 0), mkRow("C", 40, 0), mkRow("D", 20, 0)
	a.DbType, a.DbPct = 3, 0.2 // PDH, very fresh
	b.DbType, b.DbPct = 1, 2.0 // 14D HIGH, 2% above
	c.DbType, c.DbPct = 1, 0.5 // 14D HIGH, fresher
	d.DbType, d.DbPct = 1, 1.0 // under $30
	e := mkRow("E", 40, 0)     // no level
	e.DbType, e.DbPct = core.Null, core.Null
	rows := []core.Row{a, b, c, d, e}
	var m []core.Row
	for i := range rows {
		if pred(&rows[i]) {
			m = append(m, rows[i])
		}
	}
	sortRows(m, []core.SortKey{{Field: core.FieldDbType, Dir: "asc"}, {Field: core.FieldDbPct, Dir: "asc"}})
	if len(m) != 3 || m[0].Sym != "C" || m[1].Sym != "B" || m[2].Sym != "A" {
		t.Fatalf("order wrong: %d rows", len(m))
	}
	if v := fieldValue(&b, core.FieldDbType); v != "14D HIGH" {
		t.Fatalf("dbType wire = %v", v)
	}
	if v := fieldValue(&e, core.FieldDbType); v != nil {
		t.Fatalf("null dbType wire = %v", v)
	}
}
