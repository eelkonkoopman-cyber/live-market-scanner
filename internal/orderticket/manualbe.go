package orderticket

// Manual breakeven ("SL → ENTRY"): move an order's soft stop to its fill avg
// (+ the breakeven offset, rounded protectively) on demand - the breakeven
// path of breakeven.go, latching BE. Only ever tightens; refused when the
// price is already through entry (the stop would fire at once: use CLOSE).
// An order whose stop is off (or never armed) but holds shares is armed at
// entry under the same price check. One click, no confirm: it only reduces
// risk. Audited as ev "stop" note "manual breakeven at X", so the restart
// replay keeps it; follow-9EMA stays raise-only (long) / lower-only (short).

import (
	"errors"
	"fmt"
	"strings"
)

// manualBEOK: the order holds shares and its stop has not fired.
func (o *ord) manualBEOK() bool {
	if o.Kind == kindVwapx { // its stop is a real IB order: not moved by the ticket
		return false
	}
	if o.held() <= 0 || !(o.Avg > 0) {
		return false
	}
	switch o.StopState {
	case "", stArmed:
		return true
	}
	return false
}

// manualBELocked moves o's stop to entry; returns the status text.
func (t *Ticket) manualBELocked(o *ord) (string, []func(), error) {
	if !o.manualBEOK() {
		return "", nil, errors.New("nothing held with a stop that can move")
	}
	off := o.BEOffsetPct
	if o.BEPct <= 0 { // rule not captured yet (stop never armed): current settings
		off = t.settings.BEOffsetPct
	}
	lv := beLevel(o.Side, o.Avg, off)
	last := t.quoteLocked(o.Sym, t.now()).Price
	if !(last > 0) {
		return "", nil, errors.New("no live price — try again")
	}
	if o.Side == "BUY" && last <= lv {
		return "", nil, errors.New("price is below entry — use CLOSE instead")
	}
	if o.Side == "SELL" && last >= lv {
		return "", nil, errors.New("price is above entry — use CLOSE instead")
	}
	armed := o.StopState == stArmed && !o.StopOff
	if armed && !tighter(o.Side, lv, o.StopLevel) {
		return "", nil, fmt.Errorf("stop %s is already at or past entry %s", fmtPx(o.StopLevel), fmtPx(lv))
	}
	if !armed { // stop off / never armed: arm it at entry
		if o.StopPct <= 0 {
			o.StopPct, o.StopFollow = t.settings.StopPct, t.settings.StopFollow
		}
		if o.BEPct <= 0 {
			o.BEOff, o.BEPct, o.BEOffsetPct = t.settings.BEOff, t.settings.BEPct, t.settings.BEOffsetPct
		}
		o.StopOff, o.StopState, o.stopDeb = false, stArmed, [nPxSrc]stopDeb{}
		o.stopBar = t.quoteLocked(o.Sym, t.now()).LastClosed
	}
	o.StopLevel, o.BE = lv, true
	msg := fmt.Sprintf("%s stop moved to entry %s (manual)", o.Sym, fmtPx(lv))
	return msg, []func(){t.stopRec(o, fmt.Sprintf("manual breakeven at %s", fmtPx(lv)))}, nil
}

// MoveStopToEntry is the per-order "→ BE" button.
func (t *Ticket) MoveStopToEntry(id int64) error {
	t.mu.Lock()
	o := t.orders[id]
	if o == nil {
		t.mu.Unlock()
		return fmt.Errorf("order %d is not a ticket order", id)
	}
	msg, acts, err := t.manualBELocked(o)
	if err != nil {
		t.note = "refused: " + err.Error()
	} else {
		t.note = msg
	}
	t.mu.Unlock()
	run(acts)
	t.retrack()
	return err
}

// MoveStopToEntrySym is "SL → ENTRY" for every eligible order of the symbol.
func (t *Ticket) MoveStopToEntrySym(sym string) error {
	t.mu.Lock()
	cur, _ := t.currentSymLocked()
	if sym == "" {
		sym = cur
	}
	if sym == "" || !strings.EqualFold(sym, cur) {
		err := t.refuse(fmt.Sprintf("symbol mismatch: ticket %s, TWS %s", sym, cur))
		t.mu.Unlock()
		return err
	}
	var acts []func()
	var done, why []string
	for _, o := range t.orders {
		if !strings.EqualFold(o.Sym, sym) || !o.manualBEOK() {
			continue
		}
		msg, a, err := t.manualBELocked(o)
		if err != nil {
			why = append(why, err.Error())
			continue
		}
		done = append(done, msg)
		acts = append(acts, a...)
	}
	var err error
	switch {
	case len(done) > 0:
		t.note = strings.Join(done, "; ")
	case len(why) > 0:
		err = t.refuse(why[0])
	default:
		err = t.refuse("nothing held in " + sym)
	}
	t.mu.Unlock()
	run(acts)
	t.retrack()
	return err
}

func (t *Ticket) manualBECountLocked(sym string) int {
	n := 0
	for _, o := range t.orders {
		if strings.EqualFold(o.Sym, sym) && o.manualBEOK() {
			n++
		}
	}
	return n
}
