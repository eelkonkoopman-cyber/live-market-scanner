package scan

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// Counts backs the per-tab match badges in the UI. The badges exist so a scan
// that is legitimately matching nothing is distinguishable from a broken feed,
// so a stale or missing count defeats the whole point.
func TestCountsTracksMembership(t *testing.T) {
	s := New(nil)

	hot := core.ScanDef{
		ID: "hot", Name: "Hot", Enabled: true, Limit: 50,
		Filter: core.Filter{Field: "chg5m", Cmp: ">", Value: 4.0},
	}
	never := core.ScanDef{
		ID: "never", Name: "Never", Enabled: true, Limit: 50,
		Filter: core.Filter{Field: "chg5m", Cmp: ">", Value: 1e9},
	}
	s.upsert(hot)
	s.upsert(never)

	if got := s.Counts(); got != nil {
		t.Fatalf("no evaluation yet, want nil counts, got %v", got)
	}

	s.process(&core.MetricSnapshot{AsOf: 1000, Rows: []core.Row{
		{Sym: "A", Chg5m: 9},
		{Sym: "B", Chg5m: 6},
		{Sym: "C", Chg5m: 0.5},
	}})

	c := s.Counts()
	if c["hot"] != 2 {
		t.Fatalf("hot: want 2 matches, got %d (%v)", c["hot"], c)
	}
	// A scan matching nothing must report 0 — not be absent, or the UI cannot
	// tell "no matches" from "no data yet".
	if n, ok := c["never"]; !ok || n != 0 {
		t.Fatalf("never: want an explicit 0, got %d ok=%v (%v)", n, ok, c)
	}

	// Membership shrinks -> the count must follow it down, not latch.
	s.process(&core.MetricSnapshot{AsOf: 2000, Rows: []core.Row{
		{Sym: "A", Chg5m: 9},
		{Sym: "B", Chg5m: 0.1},
		{Sym: "C", Chg5m: 0.5},
	}})
	if c := s.Counts(); c["hot"] != 1 {
		t.Fatalf("after B drops out: want 1, got %d (%v)", c["hot"], c)
	}

	// A disabled scan reports 0 rather than the count it held when switched off.
	hot.Enabled = false
	s.upsert(hot)
	s.process(&core.MetricSnapshot{AsOf: 3000, Rows: []core.Row{{Sym: "A", Chg5m: 9}}})
	if c := s.Counts(); c["hot"] != 0 {
		t.Fatalf("disabled scan should report 0, got %d (%v)", c["hot"], c)
	}
}
