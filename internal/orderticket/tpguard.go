package orderticket

// TP-fill confirmation for the soft stop (incident 2026-10-26, PRSO 30673/30674:
// the stop was disarmed as "TP filled" while the TP limit was ~10% above the
// market, leaving the position without a stop).
//
// Rules:
//   - an orderStatus moves fills / the stop only when it is provably ours: not a
//     what-if id, same client id, same perm id as the order we know, and (for
//     the TP) a parentId equal to our entry. Anything else is ignored and
//     audited ("status" record, "IGNORED ...").
//   - the stop is disarmed on "TP filled" only when the latest orderStatus for
//     the TP id itself says Filled with filled == TP quantity, remaining 0, an
//     average price that a limit at the TP price could actually have produced,
//     the entry no longer working and entry filled == TP filled exactly
//     (nothing held, nothing over-counted). An openOrder status alone never
//     counts. The client does not subscribe to execDetails/commissionReport,
//     so those cannot be cross-checked here.
//   - otherwise the stop stays ARMED and a red alert names what is wrong.
//   - every orderStatus on a ticket id is audited, and a disarm records the
//     message (type, id, status, filled, remaining, avg, perm, parent, client)
//     that caused it.

import "fmt"

// statusMsg is one decoded orderStatus.
type statusMsg struct {
	ID, Filled, Remaining      int64
	Status, WhyHeld            string
	Avg, LastFill              float64
	PermID, ParentID, ClientID int64
}

func (m statusMsg) String() string {
	return fmt.Sprintf("orderStatus id=%d status=%s filled=%d remaining=%d avg=%s lastFill=%s perm=%d parent=%d client=%d",
		m.ID, m.Status, m.Filled, m.Remaining, fmtPx(m.Avg), fmtPx(m.LastFill), m.PermID, m.ParentID, m.ClientID)
}

// tpGuard is embedded in ord.
type tpGuard struct {
	childQty  int64      // TP quantity (placement / TP openOrder)
	tpBound   float64    // worst TP limit ever sent: lowest for a SELL TP, highest for a BUY TP
	childMsg  *statusMsg // latest accepted orderStatus for the TP id
	lastMsg   string     // latest accepted orderStatus for any of this order's ids
	stopAlert string     // red: a TP fill that could not be confirmed
	lastAud   map[int64]string
}

func (o *ord) noteTP(px float64) {
	if !(px > 0) {
		return
	}
	if o.tpBound == 0 || (o.Side == "BUY" && px < o.tpBound) || (o.Side == "SELL" && px > o.tpBound) {
		o.tpBound = px
	}
}

// ownerLocked: the ticket order an id belongs to (entry, TP or stop exit).
func (t *Ticket) ownerLocked(id int64) *ord {
	if o := t.orders[id]; o != nil {
		return o
	}
	if o := t.byChild[id]; o != nil {
		return o
	}
	if o := t.bySL[id]; o != nil {
		return o
	}
	return t.byExit[id]
}

// foreignStatusLocked returns why an orderStatus must not be applied ("" = ok).
func (t *Ticket) foreignStatusLocked(m statusMsg) string {
	if t.wiIDs[m.ID] {
		return "what-if id"
	}
	k, known := t.known[m.ID]
	if known && k.ClientID != 0 && m.ClientID != 0 && int(m.ClientID) != k.ClientID {
		return fmt.Sprintf("client id %d, ours %d", m.ClientID, k.ClientID)
	}
	wantPerm := int64(0)
	if known {
		wantPerm = k.PermID
	}
	if o := t.orders[m.ID]; o != nil {
		if o.PermID != 0 {
			wantPerm = o.PermID
		}
		if m.ParentID != 0 {
			return fmt.Sprintf("entry reported with parent %d", m.ParentID)
		}
	} else if o := t.byChild[m.ID]; o != nil {
		if o.ChildPermID != 0 {
			wantPerm = o.ChildPermID
		}
		if m.ParentID != 0 && m.ParentID != o.ID {
			return fmt.Sprintf("TP reported with parent %d, ours %d", m.ParentID, o.ID)
		}
	} else if o := t.bySL[m.ID]; o != nil {
		if o.SLPermID != 0 {
			wantPerm = o.SLPermID
		}
		if m.ParentID != 0 && m.ParentID != o.ID {
			return fmt.Sprintf("stop reported with parent %d, ours %d", m.ParentID, o.ID)
		}
	}
	if wantPerm != 0 && m.PermID != 0 && m.PermID != wantPerm {
		return fmt.Sprintf("perm id %d, ours %d", m.PermID, wantPerm)
	}
	if m.Filled < 0 || m.Remaining < 0 {
		return "negative quantity"
	}
	return ""
}

// statusRec audits an orderStatus on a ticket id (consecutive duplicates once).
func (t *Ticket) statusRec(o *ord, m statusMsg, role string) func() {
	note := role + ": " + m.String()
	if o.lastAud == nil {
		o.lastAud = map[int64]string{}
	}
	if o.lastAud[m.ID] == note {
		return func() {}
	}
	o.lastAud[m.ID] = note
	rec := auditRec{Ev: "status", ID: m.ID, Sym: o.Sym, Note: note}
	return func() { t.record(rec) }
}

// tpFillConfirmed: is "the TP filled everything held" proven? why explains a no.
func (o *ord) tpFillConfirmed() (bool, string) {
	m := o.childMsg
	if m == nil {
		return false, "no orderStatus for the TP id"
	}
	if m.ID != o.ChildID {
		return false, fmt.Sprintf("orderStatus id %d is not the TP %d", m.ID, o.ChildID)
	}
	if m.Status != "Filled" {
		return false, "TP status " + m.Status
	}
	q := o.childQty
	if q <= 0 {
		q = o.Qty
	}
	if m.Filled != q || m.Remaining != 0 {
		return false, fmt.Sprintf("TP filled %d remaining %d of %d", m.Filled, m.Remaining, q)
	}
	if o.parentActive() {
		return false, "entry still working"
	}
	if o.Filled != m.Filled || o.ExitFilled != 0 {
		return false, fmt.Sprintf("entry filled %d, TP filled %d, exit filled %d", o.Filled, m.Filled, o.ExitFilled)
	}
	if !(m.Avg > 0) {
		return false, "TP fill has no price"
	}
	if b := o.tpBound; b > 0 {
		eps := b * 1e-6
		if o.Side == "BUY" && m.Avg < b-eps { // long: SELL LMT never fills below its limit
			return false, fmt.Sprintf("TP avg %s below the TP limit %s", fmtPx(m.Avg), fmtPx(b))
		}
		if o.Side == "SELL" && m.Avg > b+eps { // short: BUY LMT never fills above its limit
			return false, fmt.Sprintf("TP avg %s above the TP limit %s", fmtPx(m.Avg), fmtPx(b))
		}
	}
	return true, ""
}

// tpReportedFilled: something claims the TP took (part of) the position.
func (o *ord) tpReportedFilled() bool {
	return o.ChildID != 0 && (o.ChildStatus == "Filled" || o.ChildFilled > 0 && o.held() <= 0)
}

// tpUnconfirmedLocked keeps the stop as it is and raises/refreshes the alert.
func (t *Ticket) tpUnconfirmedLocked(o *ord, why string) []func() {
	alert := fmt.Sprintf("%s: TP reported filled but NOT confirmed (%s) - soft stop stays ARMED, check TWS", o.Sym, why)
	if o.stopAlert == alert {
		return nil
	}
	o.stopAlert = alert
	note := "TP fill NOT confirmed, stop kept: " + why
	if o.childMsg != nil {
		note += "; " + o.childMsg.String()
	}
	return []func(){t.stopRec(o, note)}
}

// tpUnconfirmed: the TP is reported (part-)filled/finished as Filled but that
// is not proven; it is treated as possibly still working.
func (o *ord) tpUnconfirmed() bool {
	if o.ChildID == 0 || !(o.ChildStatus == "Filled" || o.ChildFilled > 0 && o.held() <= 0) {
		return false
	}
	ok, _ := o.tpFillConfirmed()
	return !ok
}
