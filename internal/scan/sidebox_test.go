package scan

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// The charts window's gainers/decliners boxes show price, volume, Day %, 5m %
// and PM vol x for whichever scan feeds them, so every tick row carries those
// fields even when the scan's own columns don't.
func TestTickRowsCarrySideBoxFields(t *testing.T) {
	s := New(nil)
	def := momScan()
	def.Columns = []string{core.FieldChg5m, core.FieldRvol} // no last/vol/chgDay/pmVolX
	s.upsert(def)

	r := mkRow("A", 10, 5)
	r.ChgDay, r.Vol, r.PmVolX = 12.5, 900_000, 3.4
	s.process(snapOf(1000, r))
	row := readTick(t, s).Rows["A"]
	for f, want := range map[string]any{
		core.FieldLast: 10.0, core.FieldVol: int64(900_000), core.FieldChgDay: 12.5,
		core.FieldChg5m: 5.0, core.FieldPmVolX: 3.4,
	} {
		got, ok := row[f]
		if !ok {
			t.Fatalf("tick row must carry %s for the side boxes: %v", f, row)
		}
		if gf, ok := toF(got); !ok || gf != asF(want) {
			t.Errorf("%s: want %v got %v", f, want, got)
		}
	}
	if _, ok := row[core.FieldRvol]; !ok {
		t.Error("the scan's own columns still ride along")
	}

	// A change in a side-box field alone is a change worth resending.
	r.PmVolX = 3.9
	s.process(snapOf(2000, r))
	if got, _ := toF(readTick(t, s).Rows["A"][core.FieldPmVolX]); got != 3.9 {
		t.Fatalf("a pmVolX-only change must resend the row, got %v", got)
	}

	// A full resync carries them too (the charts window resyncs its box scans).
	full, ok := s.resync(def.ID)
	if !ok {
		t.Fatal("resync failed")
	}
	if _, ok := full.Rows["A"][core.FieldPmVolX]; !ok {
		t.Fatal("resync rows must carry the side-box fields")
	}
}

func TestStreamColumnsKeepOrderAndNoDuplicates(t *testing.T) {
	got := streamColumnsOf(core.ScanDef{Columns: []string{core.FieldVol, core.FieldNews, core.FieldLast}})
	want := []string{core.FieldVol, core.FieldNews, core.FieldLast, core.FieldChgDay, core.FieldChg5m, core.FieldPmVolX}
	if len(got) != len(want) {
		t.Fatalf("want %v got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("want %v got %v", want, got)
		}
	}
	def := core.ScanDef{Columns: []string{core.FieldVol}}
	_ = streamColumnsOf(def)
	if len(def.Columns) != 1 {
		t.Fatal("the scan's own column list must not be modified")
	}
}

func toF(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	}
	return 0, false
}

func asF(v any) float64 { f, _ := toF(v); return f }
