package orderticket

// TP verification against TWS (added 2026-10-29 after DAVI / ONCO).
//
// Evidence: ONCO's TP (SELL LMT, last SENT limit 0.7439, lowest ever sent
// 0.7389) was reported Filled at avg 0.64, and the ticket CONFIRMED that fill.
// tpFillConfirmed rejects a long TP fill below tpBound, so tpBound must have
// been <= 0.64 - and the only other writer of tpBound was the openOrder echo
// of the TP (o.TP = x.LmtPrice; noteTP). So TWS reported the TP at a limit the
// ticket never sent (no "modify" record), and the ticket silently adopted it.
//
// Rules now:
//   - The ticket's intended TP (tpWant) is what it last SENT. An openOrder
//     echo only records TWS's limit (tpTWS); it never changes the intended TP
//     and never widens tpBound, so a fill at a limit the ticket did not send
//     is NOT confirmed (the stop stays armed, red alert - never an oversell).
//   - Echo != intended (outside the 3 s a modify may be in flight), or a long
//     TP at/below the entry fill (short: at/above): audit "tp mismatch", red
//     State.StopAlert, re-send the intended TP (same id), at most every 3 s and
//     tpMaxResend times, then alert only.
//   - A sent modify not echoed within tpVerifyAfter asks TWS for its open
//     orders (reqOpenOrders, read-only) and raises the alert after 2 tries.
//   - Never sent: a long TP at or below the fill average (a short's at or
//     above); on the entry's first fill the TP is re-sent at fill x (1 + tp%)
//     (the order's own rule, tpAt) and verified like any modify.
//   - IB errors on a ticket order / TP id are audited ("error" records).

import (
	"fmt"
	"math"
	"time"
)

const (
	tpInFlight    = 3 * time.Second
	tpVerifyAfter = 5 * time.Second
	tpMaxResend   = 3
)

type tpVerify struct {
	tpWant     float64   // the TP limit the ticket last sent
	tpSentAt   time.Time // when it was sent
	tpTWS      float64   // TWS's TP limit (latest openOrder echo)
	tpOK       bool      // TWS echoed tpWant after the last send
	tpResends  int
	tpAsks     int
	tpAlert    string
	tpResentAt time.Time
}

func pxEq(a, b float64) bool { return math.Abs(a-b) <= 1e-6*math.Max(1, math.Abs(b)) }

// tpBadSide: a TP that would sell at/below the fill (long) or buy at/above it (short).
func tpBadSide(side string, tp, avg float64) bool {
	if !(avg > 0) || !(tp > 0) {
		return false
	}
	if side == "BUY" {
		return tp <= avg+1e-9
	}
	return tp >= avg-1e-9
}

// tpSafeLocked returns the TP to send: never through the fill average.
func (o *ord) tpSafe(tp float64) float64 {
	if o.Filled > 0 && tpBadSide(o.Side, tp, o.Avg) {
		return o.tpAt(o.Avg, o.StopLevel)
	}
	return tp
}

func (o *ord) noteTPSent(tp float64, now time.Time) {
	o.tpWant, o.tpSentAt, o.tpOK, o.tpAsks = tp, now, false, 0
}

// tpEchoLocked handles TWS's openOrder for the TP (limit lmt).
func (t *Ticket) tpEchoLocked(o *ord, lmt float64, now time.Time) []func() {
	o.tpTWS = lmt
	if o.tpWant == 0 { // adopted after a restart: TWS's limit is all we know
		o.tpWant, o.TP, o.tpOK = lmt, lmt, true
		o.noteTP(lmt)
		if !tpBadSide(o.Side, lmt, o.Avg) {
			return nil
		}
	}
	if pxEq(lmt, o.tpWant) && !tpBadSide(o.Side, lmt, o.Avg) {
		o.tpOK, o.tpResends, o.tpAlert = true, 0, ""
		return nil
	}
	if now.Sub(o.tpSentAt) < tpInFlight && !tpBadSide(o.Side, lmt, o.Avg) {
		return nil // an older modify's echo; ours is still in flight
	}
	return t.tpMismatchLocked(o, now)
}

func (t *Ticket) tpMismatchLocked(o *ord, now time.Time) []func() {
	if finished(o.ChildStatus) || o.ChildID == 0 {
		return nil
	}
	want := o.tpSafe(o.tpWant)
	alert := fmt.Sprintf("%s TP MISMATCH: TWS has %s, ticket sent %s", o.Sym, fmtPx(o.tpTWS), fmtPx(want))
	if tpBadSide(o.Side, o.tpTWS, o.Avg) {
		alert += fmt.Sprintf(" (through the entry fill %s!)", fmtPx(o.Avg))
	}
	var acts []func()
	if alert != o.tpAlert {
		o.tpAlert = alert
		rec := auditRec{Ev: "stop", ID: o.ID, Sym: o.Sym, Limit: o.tpTWS, Note: "tp mismatch: " + alert}
		acts = append(acts, func() { t.record(rec) })
	}
	if o.tpResends < tpMaxResend && now.Sub(o.tpResentAt) >= tpInFlight {
		o.tpResends++
		o.tpResentAt = now
		o.TP = want
		acts = append(acts, t.sendTPLocked(o, fmt.Sprintf("tp re-send %d (TWS had %s)", o.tpResends, fmtPx(o.tpTWS)))...)
	}
	return acts
}

// tpVerifyTickLocked: unconfirmed modifies (1 s loop).
func (t *Ticket) tpVerifyTickLocked(now time.Time) []func() {
	var acts []func()
	c := t.client
	asked := false
	for _, o := range t.orders {
		if o.ChildID == 0 || o.tpOK || o.tpSentAt.IsZero() || finished(o.ChildStatus) {
			continue
		}
		if o.tpTWS > 0 && !pxEq(o.tpTWS, o.tpWant) && now.Sub(o.tpSentAt) >= tpVerifyAfter {
			acts = append(acts, t.tpMismatchLocked(o, now)...)
			continue
		}
		if now.Sub(o.tpSentAt) < tpVerifyAfter*time.Duration(o.tpAsks+1) {
			continue
		}
		o.tpAsks++
		if o.tpAsks > 2 {
			if o.tpAlert == "" {
				o.tpAlert = fmt.Sprintf("%s TP %s NOT confirmed by TWS - check the TP in TWS", o.Sym, fmtPx(o.tpWant))
				rec := auditRec{Ev: "stop", ID: o.ID, Sym: o.Sym, Limit: o.tpWant, Note: "tp unconfirmed: no openOrder echo"}
				acts = append(acts, func() { t.record(rec) })
			}
			continue
		}
		if !asked && c != nil {
			asked = true
			acts = append(acts, c.ReqOpenOrders)
		}
	}
	return acts
}

// orderErrorRecLocked audits an IB error on a ticket order / TP / exit id.
func (t *Ticket) orderErrorRecLocked(reqID, code int64, msg string) func() {
	o := t.orders[reqID]
	role := "entry"
	if o == nil {
		if o = t.byChild[reqID]; o != nil {
			role = "tp"
		} else if o = t.bySL[reqID]; o != nil {
			role = "sl"
		} else if o = t.byExit[reqID]; o != nil {
			role = "stop-exit"
		}
	}
	if o == nil {
		return nil
	}
	rec := auditRec{Ev: "error", ID: reqID, Sym: o.Sym, Note: fmt.Sprintf("%s: IB %d: %s", role, code, msg)}
	return func() { go t.record(rec) }
}
