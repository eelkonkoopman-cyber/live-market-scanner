package orderticket

// Soft stop loss, run by the ticket (NOT an IB stop order, so it also works in
// extended hours where IB stops do not trigger), one-cancels-other with the TP.
//
// Level: on the entry's first fill, stop = 9EMA (last closed 1m bar) x (1 -
// stopPct) for a long, x (1 + stopPct) for a short (tick-rounded away from the
// market). If that is already at/through the fill price (or there is no EMA),
// fill x (1 -/+ stopPct) is used. The level is then fixed, unless the order
// was armed with "stop follows 9EMA": after each closed bar it moves with the
// EMA, raise-only for a long, lower-only for a short. Level, percent and the
// follow flag are audited ("stop" records) so a restart/reconnect re-arms it.
//
// Trigger: two independent price sources (stopsrc.go) - the scanner's
// per-second aggregates (FeedAgg; the second's LOW for a long, HIGH for a
// short) and TWS's own last-trade ticks (reqMktData per stopped symbol). The
// condition is px <= stop (long) / >= stop (short). Debounce, per source: it
// must hold on 2 consecutive prices, or stay true for 1 s (checked again on
// the 1 s loop), whichever comes first; one print back on the right side
// resets that source. Whichever source confirms first triggers. An armed stop
// with no price from either source for > 3 s raises "SL blind" (red).
//
// Exit (OCA), never overselling:
//  1. cancel the remaining entry (if it still works) and the TP child;
//  2. wait until BOTH are confirmed finished by orderStatus (Cancelled /
//     Filled / ...); cancels are re-sent every 3 s while unconfirmed. Nothing is
//     sold while either could still fill;
//  3. size the exit from confirmed fills only: held = entry filled - TP filled;
//     held <= 0 -> done (the TP won the race);
//  4. exit LMT for held: long SELL at last x 0.99 (rounded down), short BUY at
//     last x 1.01 (rounded up), DAY, outsideRth, orderRef oticket:SYM:stop;
//  5. every 3 s while unfilled, re-price (same id, same qty) to last x (1 -/+
//     (k+1)%) for re-price k = 1..5, then stay.
//
// A full TP fill disarms the stop; a partial TP fill shrinks what the stop
// exits. The stop only acts while connected; State.StopUnwatched flags armed
// stops while disconnected.

import (
	"context"
	"fmt"
	"time"
)

const (
	stopConfirmAfter = time.Second
	stopResendEvery  = 3 * time.Second
	stopRepriceEvery = 3 * time.Second
	stopMaxReprices  = 5
	stopExitStep     = 0.01
)

// Stop states.
const (
	stArmed     = "armed"
	stTriggered = "triggered" // cancels sent, waiting for confirmation
	stExiting   = "exiting"   // exit order working
	stDone      = "done"
	stDisarmed  = "disarmed" // TP filled / nothing held
)

// stop is an order's soft stop (embedded in ord).
type stop struct {
	StopPct      float64
	StopFollow   bool
	StopLevel    float64
	StopOff      bool // the user disabled it
	StopState    string
	stopDeb      [nPxSrc]stopDeb // debounce, one per price source (stopsrc.go)
	blindSince   time.Time       // SL blind latch (stopsrc.go); zero = not blind
	blindAlert   string
	stopBar      int64
	cancelAt     time.Time // last cancel send (entry/TP) while triggered
	ExitID       int64
	ExitQty      int64
	ExitFilled   int64
	ExitStatus   string
	ExitPx       float64
	closing      bool // started by CLOSE, not by the stop level
	exitReprices int
	exitAt       time.Time
	// breakeven (breakeven.go): per-order rule captured at arming + the latch
	BE          bool
	BEOff       bool
	BEPct       float64
	BEOffsetPct float64
}

func (o *ord) held() int64 { return o.Filled - o.ChildFilled - o.SLFilled - o.ExitFilled }

func (o *ord) stopWatching() bool {
	return !o.StopOff && (o.StopState == stArmed || o.StopState == stTriggered || o.StopState == stExiting)
}

// stopLevelFor: EMA-based level with the fill-price fallback.
func stopLevelFor(side string, ema, fill, pct float64) float64 {
	// measured from the lower of 9EMA and fill (long; the higher for a short),
	// so a fill through the EMA never leaves the stop closer than pct to the entry
	p := pct / 100
	if side == "BUY" {
		base := fill
		if ema > 0 && ema < fill {
			base = ema
		}
		return snapPrice(base*(1-p), false)
	}
	base := fill
	if ema > fill {
		base = ema
	}
	return snapPrice(base*(1+p), true)
}

func stopHit(side string, last, level float64) bool {
	if side == "BUY" {
		return last <= level
	}
	return last >= level
}

// armStopLocked arms the stop at the entry's first fill.
func (t *Ticket) armStopLocked(o *ord, now time.Time) []func() {
	if o.StopState != "" || o.Filled <= 0 || o.Avg <= 0 {
		return nil
	}
	if o.StopPct <= 0 {
		o.StopPct = t.settings.StopPct
		o.StopFollow = t.settings.StopFollow
	}
	q := t.quoteLocked(o.Sym, now)
	o.StopLevel = stopLevelFor(o.Side, q.EMA, o.Avg, o.StopPct)
	o.stopBar = q.LastClosed
	if o.FillAt == 0 {
		o.FillAt = now.UnixMilli()
	}
	if o.BEPct <= 0 {
		o.BEOff, o.BEPct, o.BEOffsetPct = t.settings.BEOff, t.settings.BEPct, t.settings.BEOffsetPct
	}
	o.StopState = stArmed
	return []func(){t.stopRec(o, "arm")}
}

func (t *Ticket) stopRec(o *ord, why string) func() {
	rec := auditRec{Ev: "stop", ID: o.ID, Sym: o.Sym, StopLevel: o.StopLevel, StopPct: o.StopPct, StopFollow: o.StopFollow, StopOff: o.StopOff, StopState: o.StopState, Note: why,
		StopBE: o.BE, BEOff: o.BEOff, BEPct: o.BEPct, BEOffsetPct: o.BEOffsetPct, FillAt: o.FillAt}
	return func() { t.record(rec) }
}

// onPrice is one live price for sym (from FeedAgg).
// The stop's trigger value is the aggregate's LOW for a long (HIGH for a
// short), not its close: a second that traded through the stop counts even if
// it closed back above (see stopsrc.go for the trade-off).
func (t *Ticket) onPrice(sym string, last float64) { t.onAggPx(sym, last, last, last) }

func (t *Ticket) onAggPx(sym string, last, lo, hi float64) {
	if !(last > 0) {
		return
	}
	now := t.now()
	t.mu.Lock()
	var acts []func()
	t.tapeAddLocked(sym, last, now)
	t.pxSeenLocked(sym, now)
	for _, o := range t.orders {
		if o.Sym == sym {
			acts = append(acts, t.dropCheckLocked(o, last, now, true)...)
			acts = append(acts, t.beCheckLocked(o, last)...)
			acts = append(acts, t.trailCheckLocked(o, last, now)...)
			acts = append(acts, t.stopCheckLocked(o, aggTrig(o.Side, last, lo, hi), now, true, srcAgg)...)
		}
	}
	t.mu.Unlock()
	run(acts)
}

// stopCheckLocked applies the debounce; fresh=false is a re-check of the
// current price (1 s rule / reconnect) that does not count as a new print.
// Each price source (Massive aggregates, TWS ticks) is debounced on its own,
// so a late print from the slower source never resets the faster one; the
// first source to confirm triggers.
func (t *Ticket) stopCheckLocked(o *ord, last float64, now time.Time, fresh bool, src int) []func() {
	if o.StopState != stArmed || o.StopOff || t.conn != "connected" || t.client == nil || !(last > 0) {
		return nil
	}
	d := &o.stopDeb[src]
	if fresh {
		d.last, d.have = last, true
	}
	if !stopHit(o.Side, last, o.StopLevel) {
		if fresh {
			d.hits = 0
		}
		return nil
	}
	if d.hits == 0 {
		d.first = now
	}
	if fresh || d.hits == 0 {
		d.hits++
	}
	if d.hits < 2 && now.Sub(d.first) < stopConfirmAfter {
		return nil
	}
	o.StopState = stTriggered
	o.Following = false
	o.Note = fmt.Sprintf("soft stop hit at %s (%s)", fmtPx(last), srcName[src])
	return append([]func(){t.stopRec(o, "triggered at "+fmtPx(last)+" ("+srcName[src]+")")}, t.stopCancelsLocked(o, now)...)
}

// stopCancelsLocked (re)sends the cancels the exit waits for.
func (t *Ticket) stopCancelsLocked(o *ord, now time.Time) []func() {
	c := t.client
	if c == nil {
		return nil
	}
	o.cancelAt = now
	var acts []func()
	if o.parentActive() {
		id := o.ID
		rec := auditRec{Ev: "cancel", ID: id, Sym: o.Sym, Note: "soft stop"}
		acts = append(acts, func() { t.record(rec); c.CancelOrder(id) })
	}
	if o.childActive() || o.tpUnconfirmed() { // an unconfirmed "Filled" TP may still work
		id := o.ChildID
		rec := auditRec{Ev: "cancel", ID: id, Sym: o.Sym, Note: "soft stop (OCA)"}
		acts = append(acts, func() { t.record(rec); c.CancelOrder(id) })
	}
	if o.slActive() { // VWAP X: the real IB stop must be gone before anything is sold (CLOSE)
		id := o.SLID
		rec := auditRec{Ev: "cancel", ID: id, Sym: o.Sym, Note: "close (IB stop of a VWAP X order)"}
		acts = append(acts, func() { t.record(rec); c.CancelOrder(id) })
	}
	return acts
}

// stopProgressLocked moves a triggered/exiting stop on (orderStatus and Tick).
func (t *Ticket) stopProgressLocked(o *ord, now time.Time) []func() {
	if t.conn != "connected" || t.client == nil {
		return nil
	}
	switch o.StopState {
	case stArmed:
		if !o.tpReportedFilled() || o.parentActive() {
			o.stopAlert = ""
			return nil
		}
		if ok, why := o.tpFillConfirmed(); !ok {
			return t.tpUnconfirmedLocked(o, why)
		}
		o.StopState = stDisarmed
		o.stopAlert = ""
		return []func(){t.stopRec(o, "TP filled; confirmed by "+o.childMsg.String())}
	case stTriggered:
		if o.parentActive() || o.childActive() || o.slActive() { // not confirmed yet: never sell
			if now.Sub(o.cancelAt) >= stopResendEvery {
				return t.stopCancelsLocked(o, now)
			}
			return nil
		}
		held := o.held()
		if held <= 0 && o.ChildFilled > 0 {
			if ok, why := o.tpFillConfirmed(); !ok { // never sell, never disarm: alert
				acts := t.tpUnconfirmedLocked(o, why)
				// the TP may in fact still work: (re)cancel it; a confirmed
				// Cancelled resets the TP fill and the exit is sized from that.
				if c := t.client; now.Sub(o.cancelAt) >= stopResendEvery {
					o.cancelAt = now
					id := o.ChildID
					rec := auditRec{Ev: "cancel", ID: id, Sym: o.Sym, Note: "soft stop (TP fill unconfirmed)"}
					acts = append(acts, func() { t.record(rec); c.CancelOrder(id) })
				}
				return acts
			}
		}
		if held <= 0 {
			o.StopState = stDisarmed
			o.stopAlert = ""
			o.Note = "soft stop: nothing held (TP filled)"
			return []func(){t.stopRec(o, "nothing held; last "+o.lastMsg)}
		}
		last := t.stopLastLocked(o.Sym, now)
		if !(last > 0) {
			last = o.StopLevel
		}
		if !(last > 0) { // no price at all yet: wait (never an exit at 0)
			return nil
		}
		o.ExitID = t.nextID
		t.nextID++
		o.ExitQty = held
		o.ExitPx = stopExitPx(o.Side, last, 0)
		o.ExitStatus = "Sent"
		o.exitAt = now
		o.StopState = stExiting
		t.byExit[o.ExitID] = o
		cid := t.settings.ClientID
		t.known[o.ExitID] = auditRec{ID: o.ExitID, ParentID: o.ID, ClientID: cid, Sym: o.Sym, Side: o.Side, Note: "stop-exit"}
		rec := auditRec{Ev: "place", ID: o.ExitID, ParentID: o.ID, ClientID: cid, Sym: o.Sym, Side: o.Side, Qty: held, Limit: o.ExitPx, Note: "stop-exit"}
		return []func(){t.sendExit(o, rec)}
	case stExiting:
		if o.ExitStatus == "Filled" || o.ExitFilled >= o.ExitQty {
			o.StopState = stDone
			return []func(){t.stopRec(o, "exit filled")}
		}
		if finished(o.ExitStatus) || o.exitReprices >= stopMaxReprices || now.Sub(o.exitAt) < stopRepriceEvery {
			return nil
		}
		last := t.stopLastLocked(o.Sym, now)
		if !(last > 0) {
			return nil
		}
		o.exitReprices++
		o.exitAt = now
		o.ExitPx = stopExitPx(o.Side, last, o.exitReprices)
		rec := auditRec{Ev: "modify", ID: o.ExitID, Sym: o.Sym, Limit: o.ExitPx, Note: fmt.Sprintf("stop exit re-price %d", o.exitReprices)}
		return []func(){t.sendExit(o, rec)}
	}
	return nil
}

// stopExitPx: last x (1 -/+ (k+1)%) for re-price k (0 = the first send).
func stopExitPx(side string, last float64, k int) float64 {
	step := stopExitStep * float64(k+1)
	if side == "BUY" { // long: sell below the market
		return snapPrice(last*(1-step), false)
	}
	return snapPrice(last*(1+step), true)
}

func (t *Ticket) exitOrder(o *ord) *Order {
	x := &Order{OrderType: "LMT", TotalQuantity: Qty(o.ExitQty), LmtPrice: o.ExitPx, TIF: "DAY", OutsideRTH: true,
		OrderRef: refPrefix + o.Sym + ":stop", Account: t.account, Transmit: true}
	x.Action = "SELL"
	if o.Side == "SELL" {
		x.Action = "BUY"
	}
	return x
}

func (t *Ticket) sendExit(o *ord, rec auditRec) func() {
	c, id, x, sym := t.client, o.ExitID, t.exitOrder(o), o.Sym
	return func() { t.record(rec); c.PlaceOrder(id, contractFor(sym), x) }
}

// stopFollowLocked moves a follow-9EMA stop after each closed bar (raise-only
// for a long, lower-only for a short).
func (t *Ticket) stopFollowLocked(o *ord, now time.Time) []func() {
	if o.StopState != stArmed || !o.StopFollow || o.StopOff {
		return nil
	}
	q := t.quoteLocked(o.Sym, now)
	if q.EMA <= 0 || q.Stale || q.LastClosed <= o.stopBar {
		return nil
	}
	o.stopBar = q.LastClosed
	p := o.StopPct / 100
	if o.Side == "BUY" {
		if lv := snapPrice(q.EMA*(1-p), false); lv > o.StopLevel {
			o.StopLevel = lv
			return []func(){t.stopRec(o, "follow 9EMA")}
		}
	} else if lv := snapPrice(q.EMA*(1+p), true); lv < o.StopLevel {
		o.StopLevel = lv
		return []func(){t.stopRec(o, "follow 9EMA")}
	}
	return nil
}

// stopTickLocked: 1 s rule, re-check after reconnect, follow, progress.
func (t *Ticket) stopTickLocked(now time.Time) []func() {
	var acts []func()
	recheck := t.stopRecheck
	t.stopRecheck = false
	for _, o := range t.orders {
		for src := range o.stopDeb {
			if o.StopState != stArmed {
				break
			}
			d := &o.stopDeb[src]
			if !recheck && d.hits == 0 {
				continue
			}
			last := d.last
			if !d.have && src == srcAgg {
				last = t.quoteLocked(o.Sym, now).Price
			}
			if d.have || last > 0 {
				acts = append(acts, t.stopCheckLocked(o, last, now, false, src)...)
			}
		}
		acts = append(acts, t.blindCheckLocked(o, now)...)
		acts = append(acts, t.stopFollowLocked(o, now)...)
		acts = append(acts, t.stopProgressLocked(o, now)...)
	}
	return acts
}

// SetStopEnabled is the per-order "disable stop" toggle.
func (t *Ticket) SetStopEnabled(id int64, on bool) error {
	t.mu.Lock()
	o := t.orders[id]
	if o == nil {
		t.mu.Unlock()
		return fmt.Errorf("order %d is not a ticket order", id)
	}
	if o.Kind == kindVwapx {
		t.mu.Unlock()
		return fmt.Errorf("a VWAP X order has a real stop at IB, no soft stop")
	}
	if o.StopState == stTriggered || o.StopState == stExiting {
		t.mu.Unlock()
		return fmt.Errorf("the stop already fired")
	}
	o.StopOff = !on
	o.stopDeb = [nPxSrc]stopDeb{}
	why := "disabled"
	if on {
		why = "enabled"
		if o.StopPct <= 0 { // not armed yet: it arms on the first fill with the current settings
			o.StopPct, o.StopFollow = t.settings.StopPct, t.settings.StopFollow
		}
	}
	acts := []func(){t.stopRec(o, why)}
	if on && o.StopState == "" && o.Filled > 0 {
		acts = append(acts, t.armStopLocked(o, t.now())...)
	}
	t.mu.Unlock()
	run(acts)
	t.retrack()
	return nil
}

// SetStopSettings changes the default stop % and follow option for new orders.
func (t *Ticket) SetStopSettings(ctx context.Context, pct float64, follow bool) error {
	t.mu.Lock()
	s := t.settings
	s.StopPct, s.StopFollow = pct, follow
	if err := s.validate(); err != nil {
		t.mu.Unlock()
		return err
	}
	t.settings = s
	t.mu.Unlock()
	return t.saveSettings(ctx, s)
}

// restoreStopLocked re-arms an adopted order's stop from the audit (restart).
// A stop that had fired resumes from confirmed fills; if the audit shows an
// exit order was placed, it is resumed (never a second exit).
func (t *Ticket) restoreStopLocked(o *ord, k auditRec) {
	if k.Kind == kindVwapx { // real stop at IB: no soft stop, no 9EMA follow / drop-cancel
		o.Kind, o.StopOff, o.DropOff, o.Following = kindVwapx, true, true, false
		return
	}
	o.Preset, o.TPR, o.Trail5m, o.DropOff, o.EntryDeadline, o.FillAt = k.Preset, k.TPR, k.Trail5m, k.DropOff, k.EntryDeadline, k.FillAt
	if o.BEPct <= 0 && k.BEPct > 0 {
		o.BEOff, o.BEPct, o.BEOffsetPct = k.BEOff, k.BEPct, k.BEOffsetPct
	}
	if k.StopPct <= 0 {
		return
	}
	o.StopPct, o.StopFollow, o.StopLevel, o.StopOff = k.StopPct, k.StopFollow, k.StopLevel, k.StopOff
	o.BE, o.BEOff, o.BEPct, o.BEOffsetPct = k.StopBE, k.BEOff, k.BEPct, k.BEOffsetPct
	o.StopState = k.StopState
	if o.StopState == "" && o.StopLevel > 0 {
		o.StopState = stArmed
	}
	if o.StopState == stTriggered || o.StopState == stExiting {
		o.StopState = stTriggered
		for id, k2 := range t.known {
			if k2.ParentID == o.ID && k2.Note == "stop-exit" {
				o.ExitID, o.ExitQty, o.ExitPx = id, k2.Qty, k2.Limit
				o.StopState, o.exitReprices, o.ExitStatus = stExiting, stopMaxReprices, "Submitted"
				t.byExit[id] = o
			}
		}
	}
}

func stopLabel(o *ord) string {
	if o.Kind == kindVwapx && o.SLPx > 0 {
		return fmt.Sprintf("SL %s (IB stop)", fmtPx(o.SLPx))
	}
	if o.StopLevel <= 0 {
		return ""
	}
	if o.BE {
		return fmt.Sprintf("SL %s (soft, BE)", fmtPx(o.StopLevel))
	}
	return fmt.Sprintf("SL %s (soft)", fmtPx(o.StopLevel))
}
