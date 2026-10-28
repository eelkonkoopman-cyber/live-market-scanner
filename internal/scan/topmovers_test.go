package scan

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestTopMovers(t *testing.T) {
	s := New(nil)
	mk := func(sym string, last, chg float64, vol int64) core.Row {
		r := mkRow(sym, last, 0)
		r.ChgDay, r.Vol = chg, vol
		return r
	}
	nullChg := mkRow("NUL", 5, 0)
	nullChg.Vol = 1_000_000
	s.last = &core.MetricSnapshot{Rows: []core.Row{
		mk("A", 5, 10, 1e6), mk("B", 5, 50, 1e6), mk("C", 5, -20, 1e6), mk("D", 5, -5, 1e6),
		mk("THIN", 5, 900, 100), mk("PENNY", 0.05, 800, 1e7), mk("FLAT", 5, 0, 1e6), nullChg,
	}}
	go func() {
		for f := range s.ctrl {
			f(s)
		}
	}()
	gain, loss := s.TopMovers(2, 50_000, 0.2)
	if len(gain) != 2 || gain[0].Sym != "B" || gain[1].Sym != "A" {
		t.Fatalf("gainers biggest first, thin and penny names skipped: %+v", gain)
	}
	if len(loss) != 2 || loss[0].Sym != "C" || loss[1].Sym != "D" {
		t.Fatalf("losers biggest first: %+v", loss)
	}
	if g, l := s.TopMovers(5, 50_000, 0.2); len(g) != 2 || len(l) != 2 {
		t.Fatalf("only real movers, flat and null excluded: %d %d", len(g), len(l))
	}
}
