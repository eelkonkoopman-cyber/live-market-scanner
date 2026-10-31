package orderticket

// CLOSE / CANCEL for the ticket's current symbol. Both act ONLY on orders this
// ticket created (t.orders: audit-known ids under its own client id); other
// TWS orders and positions are never seen, so never touched.
//
//   - CancelSymbol: cancels the symbol's still-working entries. An entry with
//     no fill also has its TP cancelled; a partly filled entry keeps its TP
//     (it protects the filled shares). Filled positions are untouched.
//   - CLOSE (PrepareClose -> Confirm): flattens the ticket's position in the
//     symbol through the soft-stop exit path (stop.go): cancel remaining entry
//     + TP, wait for TWS to confirm, size from confirmed fills only, LMT exit
//     at last -/+ 1% outsideRth with the 3 s re-price steps; one exit per
//     order, never more than held. It replaces (disarms) the soft stop; an
//     order whose stop already fired is left to that exit.

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// closeQtyLocked: shares the ticket holds in sym that CLOSE would exit
// (orders whose stop has not fired), plus whether any order could still fill.
func (t *Ticket) closeQtyLocked(sym string) (held int64, n int) {
	for _, o := range t.orders {
		if !strings.EqualFold(o.Sym, sym) || o.StopState == stTriggered || o.StopState == stExiting || o.StopState == stDone {
			continue
		}
		if h := o.held(); h > 0 {
			held += h
			n++
		}
	}
	return held, n
}

func (t *Ticket) cancelCountLocked(sym string) int {
	n := 0
	for _, o := range t.orders {
		if strings.EqualFold(o.Sym, sym) && o.parentActive() && !o.cancelled && o.StopState != stTriggered && o.StopState != stExiting {
			n++
		}
	}
	return n
}

// CancelSymbol cancels the current symbol's unfilled entries (see above).
func (t *Ticket) CancelSymbol(sym string) (string, error) {
	t.mu.Lock()
	cur, _ := t.currentSymLocked()
	if sym == "" {
		sym = cur
	}
	if sym == "" || !strings.EqualFold(sym, cur) {
		t.mu.Unlock()
		return "", t.refuseUnlocked(fmt.Sprintf("symbol mismatch: ticket %s, TWS %s", sym, cur))
	}
	if t.conn != "connected" || t.client == nil {
		t.mu.Unlock()
		return "", errors.New("not connected to TWS")
	}
	c := t.client
	var acts []func()
	var what []string
	for _, o := range t.orders {
		if !strings.EqualFold(o.Sym, sym) || !o.parentActive() || o.cancelled || o.StopState == stTriggered || o.StopState == stExiting {
			continue
		}
		o.cancelled, o.Following = true, false
		id := o.ID
		r := auditRec{Ev: "cancel", ID: id, Sym: o.Sym, Note: "CANCEL button"}
		acts = append(acts, func() { t.record(r); c.CancelOrder(id) })
		if o.Filled == 0 && o.slActive() { // VWAP X: IB cancels its children with the parent; say so explicitly
			sid := o.SLID
			r3 := auditRec{Ev: "cancel", ID: sid, Sym: o.Sym, Note: "CANCEL button (IB stop of unfilled entry)"}
			acts = append(acts, func() { t.record(r3); c.CancelOrder(sid) })
		}
		if o.Filled == 0 && o.childActive() {
			cid := o.ChildID
			r2 := auditRec{Ev: "cancel", ID: cid, Sym: o.Sym, Note: "CANCEL button (TP of unfilled entry)"}
			acts = append(acts, func() { t.record(r2); c.CancelOrder(cid) })
			o.Note = "cancel sent (entry + TP)"
			what = append(what, fmt.Sprintf("%s %d entry+TP", o.Side, o.ID))
		} else {
			o.Note = "cancel sent (rest of entry; TP kept for the filled shares)"
			what = append(what, fmt.Sprintf("%s %d rest of entry", o.Side, o.ID))
		}
	}
	msg := "nothing to cancel in " + sym
	if len(what) > 0 {
		msg = "cancel sent: " + strings.Join(what, ", ")
	}
	t.note = msg
	t.mu.Unlock()
	run(acts)
	return msg, nil
}

func (t *Ticket) refuseUnlocked(msg string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.refuse(msg)
}

// PrepareClose builds the CLOSE confirmation for the current symbol.
func (t *Ticket) PrepareClose(sym string) (Pending, error) {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending = nil
	cur, _ := t.currentSymLocked()
	if sym == "" {
		sym = cur
	}
	if cur == "" || !strings.EqualFold(sym, cur) {
		return Pending{}, t.refuse(fmt.Sprintf("symbol mismatch: ticket %s, TWS %s", sym, cur))
	}
	if t.conn != "connected" || t.client == nil {
		return Pending{}, t.refuse("not connected to TWS")
	}
	held, _ := t.closeQtyLocked(cur)
	if held <= 0 {
		return Pending{}, t.refuse("the ticket holds nothing in " + cur)
	}
	q := t.quoteLocked(cur, now)
	last := q.Price
	if !(last > 0) {
		return Pending{}, t.refuse("no price at all for " + cur + " (no bars yet)")
	}
	// CLOSE is never blocked by a stale 9EMA; an old last trade is shown so
	// the user confirms the limit knowingly.
	ageMin := (now.UnixMilli() - (q.LastAt + 60000)) / 60000
	t.tokSeq++
	p := Pending{Token: fmt.Sprintf("%d-%d", now.UnixNano(), t.tokSeq), Side: "CLOSE", Sym: cur, Qty: held, Limit: last, At: now.UnixMilli()}
	p.Text = fmt.Sprintf("CLOSE %s %s @ ~%s", fmtQty(held), cur, fmtPx(last))
	if ageMin >= 2 {
		p.Text += fmt.Sprintf(" (last trade %dm old)", ageMin)
	}
	t.pending = &p
	t.note = ""
	return p, nil
}

// confirmCloseLocked starts the exit for every holding order of p.Sym.
// Called from Confirm with the lock held; returns the client calls.
func (t *Ticket) confirmCloseLocked(p *Pending, now time.Time) ([]func(), error) {
	if t.conn != "connected" || t.client == nil {
		return nil, t.refuse("not connected to TWS")
	}
	cur, _ := t.currentSymLocked()
	if !strings.EqualFold(cur, p.Sym) {
		return nil, t.refuse(fmt.Sprintf("symbol mismatch: ticket %s, TWS %s", p.Sym, cur))
	}
	var acts []func()
	n := 0
	for _, o := range t.orders {
		if !strings.EqualFold(o.Sym, p.Sym) || o.held() <= 0 || o.StopState == stTriggered || o.StopState == stExiting || o.StopState == stDone {
			continue
		}
		o.StopOff = false // the close replaces the soft stop; it is watched like one
		o.StopState = stTriggered
		o.Following = false
		o.closing = true
		o.Note = "closing"
		acts = append(acts, t.stopRec(o, "CLOSE"))
		acts = append(acts, t.stopCancelsLocked(o, now)...)
		acts = append(acts, t.stopProgressLocked(o, now)...) // nothing left to cancel: exit at once
		n++
	}
	if n == 0 {
		return nil, t.refuse("the ticket holds nothing in " + p.Sym)
	}
	t.note = "closing: " + p.Text
	return acts, nil
}
