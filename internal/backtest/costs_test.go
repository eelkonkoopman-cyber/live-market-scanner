package backtest

import (
	"testing"
)

func TestCommissionMath(t *testing.T) {
	c := DefaultCosts()
	cases := []struct {
		shares int64
		price  float64
		want   float64
		why    string
	}{
		{100, 10, 0.35, "100 x 0.0035 = 0.35: exactly the minimum"},
		{50, 10, 0.35, "50 x 0.0035 = 0.175: raised to the 0.35 minimum"},
		{1000, 10, 3.5, "1000 x 0.0035"},
		{10000, 2, 35, "10000 x 0.0035 = 35, under the 1% cap of 200"},
		{100, 0.2, 0.2, "a $20 order: the 1% cap (0.20) wins over the 0.35 minimum"},
		{2000, 0.2, 4, "2000 x 0.0035 = 7 is over the 1% cap of 4.00"},
		{0, 10, 0, "no shares"},
	}
	for _, k := range cases {
		if got := c.Commission(k.shares, k.price); !approx(got, k.want, 1e-9) {
			t.Errorf("Commission(%d, %v) = %v, want %v (%s)", k.shares, k.price, got, k.want, k.why)
		}
	}
	if got := c.Fees(1000); !approx(got, 2, 1e-9) {
		t.Errorf("Fees(1000) = %v, want 2.00", got)
	}
}

func TestSlippageMath(t *testing.T) {
	c := DefaultCosts()
	if got := c.EntryFill(10, 10.1); !approx(got, 10.025, 1e-12) {
		t.Errorf("EntryFill = %v, want 10.025 (+0.25%%)", got)
	}
	if got := c.EntryFill(10.1, 10.1); got != 10.1 {
		t.Errorf("EntryFill at the limit = %v, want the limit", got)
	}
	if got := c.ExitFill(10, ExitStop); !approx(got, 9.95, 1e-12) {
		t.Errorf("stop exit = %v, want 9.95 (-0.5%%)", got)
	}
	for _, k := range []string{ExitFlatten, ExitClose, ExitEOD} {
		if got := c.ExitFill(10, k); !approx(got, 9.99, 1e-12) {
			t.Errorf("%s exit = %v, want 9.99 (-0.1%%)", k, got)
		}
	}
	z := NoCosts()
	if z.EntryFill(10, 10.1) != 10 || z.ExitFill(10, ExitStop) != 10 || z.Commission(100, 10) != 0 || z.Fees(100) != 0 {
		t.Errorf("NoCosts is not frictionless")
	}
}

// One trade through the whole cost model, by hand: 100 shares, entry bar open 10.00 (trigger 10),
// stopped at the 9.90 trail.
//
//	entry fill  10 * 1.0025       = 10.025
//	exit fill   9.90 * (1-0.005)  = 9.8505
//	gross       (9.90 - 10) * 100 = -10.00
//	price P&L   (9.8505 - 10.025) * 100 = -17.45   -> slippage cost 7.45
//	commission  0.35 + 0.35 = 0.70, fees 2 x 0.2 = 0.40
//	net         -17.45 - 0.70 - 0.40 = -18.55
func TestTradeCostsByHand(t *testing.T) {
	c := flatDay(t, "X")
	setBar(t, c.Bars, "2026-10-05 09:10", 10, 10, 9.89, 9.95)
	dr := SimulateDay(dayOf(sigAt(t, c, "2026-10-05 09:00", 10, 9.5, 1)), rulesNamed(t, "V1"))
	tr := dr.Trades[0]
	checks := []struct {
		name      string
		got, want float64
	}{
		{"entry fill", tr.EntryFill, 10.025}, {"exit fill", tr.ExitFill, 9.8505}, {"gross", tr.Gross, -10},
		{"slippage", tr.Slippage, 7.45}, {"commission", tr.Commission, 0.70}, {"fees", tr.Fees, 0.40},
		{"net", tr.Net, -18.55}, {"initial risk", tr.InitialRisk, 50}, {"R net", tr.RNet, -18.55 / 50}, {"R gross", tr.RGross, -10.0 / 50},
	}
	for _, k := range checks {
		if !approx(k.got, k.want, 1e-9) {
			t.Errorf("%s = %v, want %v", k.name, k.got, k.want)
		}
	}
	// the identity the report relies on
	if !approx(tr.Net, tr.Gross-tr.Slippage-tr.Commission-tr.Fees, 1e-9) {
		t.Errorf("net %v != gross - slippage - commission - fees", tr.Net)
	}

	// the same trade with the frictionless model: net == gross
	r := rulesNamed(t, "V1")
	r.Costs = NoCosts()
	z := SimulateDay(dayOf(sigAt(t, c, "2026-10-05 09:00", 10, 9.5, 1)), r).Trades[0]
	if !approx(z.Net, z.Gross, 1e-9) || !approx(z.Gross, tr.Gross, 1e-9) {
		t.Errorf("no costs: net %v gross %v (with costs: gross %v)", z.Net, z.Gross, tr.Gross)
	}
}
