package orderticket

// Manual stop: the order row's SL box moves one order's stop to a typed price,
// tighter or wider. A soft stop takes the level and stops following the 9EMA or
// auto-breakeven (they would overwrite it); a VWAP X stop is re-sent to IB under
// its own id. Refused when the price is already through the new level (use
// CLOSE). Audited like the manual breakeven, so a restart keeps a soft level.

import (
	"errors"
	"fmt"
)

// canMoveStop: a soft stop that holds shares and has not fired, or a live IB stop.
func (o *ord) canMoveStop() bool {
	if o.Kind == kindVwapx {
		return o.slActive()
	}
	return o.manualBEOK()
}

// MoveStop sets order id's stop to px.
func (t *Ticket) MoveStop(id int64, px float64) error {
	t.mu.Lock()
	acts, err := t.moveStopLocked(id, px)
	if err != nil {
		t.note = "refused: " + err.Error()
	}
	t.mu.Unlock()
	run(acts)
	t.retrack()
	return err
}

func (t *Ticket) moveStopLocked(id int64, px float64) ([]func(), error) {
	o := t.orders[id]
	if o == nil {
		return nil, fmt.Errorf("order %d is not a ticket order", id)
	}
	if !o.canMoveStop() {
		return nil, errors.New("no stop that can move")
	}
	if !(px > 0) {
		return nil, errors.New("bad stop price")
	}
	px = snapPrice(px, o.Side == "SELL")
	last := t.quoteLocked(o.Sym, t.now()).Price
	if !(last > 0) {
		return nil, errors.New("no live price: try again")
	}
	if o.Side == "BUY" && px >= last {
		return nil, errors.New("stop is at or above the price: use CLOSE instead")
	}
	if o.Side == "SELL" && px <= last {
		return nil, errors.New("stop is at or below the price: use CLOSE instead")
	}
	t.note = fmt.Sprintf("%s stop moved to %s (manual)", o.Sym, fmtPx(px))
	if o.Kind == kindVwapx {
		c := t.client
		if c == nil || t.conn != "connected" {
			return nil, errors.New("not connected to TWS")
		}
		o.SLPx = px
		x := t.slOrder(o, true)
		rec := auditRec{Ev: "modify", ID: o.SLID, ParentID: o.ID, Sym: o.Sym, Limit: px, Note: "manual stop"}
		sid, sym := o.SLID, o.Sym
		return []func(){func() { t.record(rec); c.PlaceOrder(sid, contractFor(sym), x) }}, nil
	}
	if o.StopState != stArmed || o.StopOff {
		if o.StopPct <= 0 {
			o.StopPct = t.settings.StopPct
		}
		o.StopOff, o.StopState, o.stopDeb = false, stArmed, [nPxSrc]stopDeb{}
		o.stopBar = t.quoteLocked(o.Sym, t.now()).LastClosed
	}
	o.StopLevel, o.StopFollow, o.BE = px, false, true
	return []func(){t.stopRec(o, "manual stop at "+fmtPx(px))}, nil
}
