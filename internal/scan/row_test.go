package scan

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestRowJSONIncludesSecTypeWhenRequested(t *testing.T) {
	r := core.Row{Sym: "A", SecType: "ETF"}
	m := rowJSON(&r, []string{core.FieldSecType})
	if got, ok := m[core.FieldSecType]; !ok || got != "ETF" {
		t.Fatalf("rowJSON[secType] = %v, want ETF", got)
	}
}

func TestRowJSONOmitsSecTypeWhenNotRequested(t *testing.T) {
	r := core.Row{Sym: "A", SecType: "ETF"}
	m := rowJSON(&r, []string{core.FieldLast})
	if _, ok := m[core.FieldSecType]; ok {
		t.Fatal("secType should not appear when not a requested column")
	}
}

func TestRowChangedFiresOnSecTypeChange(t *testing.T) {
	prev := core.Row{Sym: "A", SecType: "CS"}
	cur := prev
	if rowChanged(prev, cur, []string{core.FieldSecType}) {
		t.Fatal("identical rows should not be reported as changed")
	}
	cur.SecType = "ETF"
	if !rowChanged(prev, cur, []string{core.FieldSecType}) {
		t.Fatal("a secType change should be reported as changed")
	}
}

// The inline chart's bars ride along only with scans that show the chart
// column: they are the bulk of a row's bytes and nothing else reads them.
func TestRowJSONBarsOnlyWithChartColumn(t *testing.T) {
	r := core.Row{Sym: "A", Last: 1, Bars: []core.Bar{{T: 1, C: 1}, {T: 2, C: 2}}}
	if _, ok := rowJSON(&r, []string{core.FieldLast})["bars"]; ok {
		t.Fatal("bars must not be sent for a scan without the chart column")
	}
	if _, ok := rowJSON(&r, []string{core.FieldLast, chartColumn})["bars"]; !ok {
		t.Fatal("bars must be sent for a scan with the chart column")
	}
}
