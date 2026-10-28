package universe

import (
	"context"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

type fakeRef struct {
	tks []core.Ticker
	err error
}

func (f fakeRef) Reference(context.Context) ([]core.Ticker, error) { return f.tks, f.err }

func TestLoadFiltersToRegularSecurities(t *testing.T) {
	r := fakeRef{tks: []core.Ticker{
		{Symbol: "AAPL", Type: "CS", Active: true},
		{Symbol: "SPY", Type: "ETF", Active: true},
		{Symbol: "BABA", Type: "ADRC", Active: true},
		{Symbol: "WARR", Type: "WARRANT", Active: true}, // excluded type
		{Symbol: "OLD", Type: "CS", Active: false},      // inactive excluded
	}}
	u, err := Load(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if u.Len() != 3 {
		t.Fatalf("want 3 symbols, got %d", u.Len())
	}
	for _, s := range []string{"AAPL", "SPY", "BABA"} {
		if !u.Contains(s) {
			t.Fatalf("%s should be in the universe", s)
		}
	}
	if u.Contains("WARR") || u.Contains("OLD") {
		t.Fatal("excluded symbol present")
	}
}

func TestLoadBuildsTypesMapForAdmittedSymbolsOnly(t *testing.T) {
	r := fakeRef{tks: []core.Ticker{
		{Symbol: "AAPL", Type: "CS", Active: true},
		{Symbol: "SPY", Type: "ETF", Active: true},
		{Symbol: "BABA", Type: "ADRC", Active: true},
		{Symbol: "WARR", Type: "WARRANT", Active: true}, // excluded type -> no entry
		{Symbol: "OLD", Type: "CS", Active: false},      // inactive -> no entry
	}}
	u, err := Load(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	types := u.Types()
	want := map[string]string{"AAPL": "CS", "SPY": "ETF", "BABA": "ADRC"}
	if len(types) != len(want) {
		t.Fatalf("Types() = %v, want %v", types, want)
	}
	for sym, ty := range want {
		if types[sym] != ty {
			t.Fatalf("Types()[%s] = %q, want %q", sym, types[sym], ty)
		}
	}
	if _, ok := types["WARR"]; ok {
		t.Fatal("an excluded-type ticker must not appear in Types()")
	}
	if _, ok := types["OLD"]; ok {
		t.Fatal("an inactive ticker must not appear in Types()")
	}
}

func TestNewAllAdmitsEverything(t *testing.T) {
	u := NewAll()
	if !u.Contains("ANYTHING") {
		t.Fatal("NewAll universe should admit all symbols")
	}
	u.Replace([]string{"AAA"}, map[string]string{"AAA": "CS"})
	if u.Contains("BBB") || !u.Contains("AAA") {
		t.Fatal("Replace should leave all-mode and restrict to the set")
	}
}
