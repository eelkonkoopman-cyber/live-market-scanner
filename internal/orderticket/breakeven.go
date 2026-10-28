package orderticket

// Breakeven move for the soft stop: once the live price (the same feed as
// the stop trigger) reaches fill avg x (1 + BEPct%) for a long (x (1 - BEPct%)
// for a short), the stop level moves to the entry: avg x (1 + offset%) long,
// avg x (1 - offset%) short, rounded toward protecting the entry - but only if
// that tightens the stop (never loosens). One-shot per order (o.BE latch);
// "stop follows 9EMA" keeps working afterwards and, being raise-only (long) /
// lower-only (short), can never take the stop back past entry. More fills
// after the latch re-apply the rule at the new average only if that tightens.
// Per-order parameters are captured when the stop arms and are audited with
// every "stop" record (note "breakeven at <price>"), so a restart re-arms it.

import (
	"context"
	"fmt"
)

// beLevel: the entry-protecting stop for avg with the buffer.
func beLevel(side string, avg, offPct float64) float64 {
	if side == "BUY" {
		return snapPrice(avg*(1+offPct/100), true)
	}
	return snapPrice(avg*(1-offPct/100), false)
}

func tighter(side string, lv, cur float64) bool {
	if side == "BUY" {
		return lv > cur+1e-12
	}
	return lv < cur-1e-12
}

// beCheckLocked latches breakeven for o at live price last.
func (t *Ticket) beCheckLocked(o *ord, last float64) []func() {
	if o.BE || o.BEOff || o.BEPct <= 0 || o.StopState != stArmed || o.StopOff || !(o.Avg > 0) || o.held() <= 0 {
		return nil
	}
	reached := last >= o.Avg*(1+o.BEPct/100)-1e-9 // tolerance: 1.50 x 1.05 is 1.5750000000000002
	if o.Side == "SELL" {
		reached = last <= o.Avg*(1-o.BEPct/100)+1e-9
	}
	if !reached {
		return nil
	}
	o.BE = true
	lv := beLevel(o.Side, o.Avg, o.BEOffsetPct)
	why := fmt.Sprintf("breakeven at %s (latched; stop already tighter)", fmtPx(last))
	if tighter(o.Side, lv, o.StopLevel) {
		o.StopLevel = lv
		why = fmt.Sprintf("breakeven at %s", fmtPx(last))
		sign := "+"
		if o.Side == "SELL" {
			sign = "-"
		}
		t.note = fmt.Sprintf("%s stop moved to entry %s (%s%s reached)", o.Sym, fmtPx(lv), sign, fmtPct(o.BEPct))
	}
	return []func(){t.stopRec(o, why)}
}

// beRefillLocked: a new fill average after the latch moves the stop only if tighter.
func (t *Ticket) beRefillLocked(o *ord) []func() {
	if !o.BE || o.StopState != stArmed || !(o.Avg > 0) {
		return nil
	}
	if lv := beLevel(o.Side, o.Avg, o.BEOffsetPct); tighter(o.Side, lv, o.StopLevel) {
		o.StopLevel = lv
		return []func(){t.stopRec(o, "breakeven (new fill avg)")}
	}
	return nil
}

// SetBreakeven sets the default breakeven rule for new stops.
func (t *Ticket) SetBreakeven(ctx context.Context, on bool, pct, off float64) error {
	t.mu.Lock()
	s := t.settings
	s.BEOff = !on
	if pct != 0 {
		s.BEPct, s.BEOffsetPct = pct, off
	}
	if err := s.validate(); err != nil {
		t.mu.Unlock()
		return err
	}
	t.settings = s
	t.mu.Unlock()
	return t.saveSettings(ctx, s)
}
