package backtest

import "math"

// Commission is one side's commission on shares at price: the per-share rate
// with a per-order floor, capped at CommissionMaxPct of the trade value. The cap
// wins over the floor (IBKR: "minimum $0.35 per order, maximum 1% of trade
// value") so a $20 order pays $0.20, not $0.35.
func (c Costs) Commission(shares int64, price float64) float64 {
	if shares <= 0 || !(price > 0) {
		return 0
	}
	n := float64(shares)
	comm := math.Max(c.CommissionPerShare*n, c.CommissionMin)
	if c.CommissionMaxPct > 0 {
		comm = math.Min(comm, c.CommissionMaxPct/100*n*price)
	}
	return comm
}

// Fees is one side's regulatory / exchange charge.
func (c Costs) Fees(shares int64) float64 {
	if shares <= 0 {
		return 0
	}
	return c.FeePerShare * float64(shares)
}

// EntryFill is the price a BUY fills at: the model price (the trigger, or the
// bar's open when it opened above) worse by EntrySlipPct, but never above the
// order's limit (a limit order cannot fill worse than its limit).
func (c Costs) EntryFill(modelPx, limit float64) float64 {
	px := modelPx * (1 + c.EntrySlipPct/100)
	if limit > 0 && px > limit {
		px = limit
	}
	return px
}

// ExitFill is the price a SELL fills at: the model price worse by the slippage
// of the exit's kind (ExitStop -> StopSlipPct, everything else FlattenSlipPct).
func (c Costs) ExitFill(modelPx float64, kind string) float64 {
	slip := c.FlattenSlipPct
	if kind == ExitStop {
		slip = c.StopSlipPct
	}
	return modelPx * (1 - slip/100)
}
