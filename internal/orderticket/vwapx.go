package orderticket

// VWAP X: the order preset for the scanner's 9EMA VWAP bull cross (engine kind
// "vwapBull"). One click on the pane's VWAP X button, then the usual Confirm:
//
//	entry : BUY LMT at the CURRENT PRICE, DAY, outsideRth
//	stop  : a REAL stop at IB - SELL STP at the session VWAP, fixed when sent
//	exit  : SELL LMT take profit at entry + 3 x (entry - stop), rounded up
//
// It is a 3-order IB bracket, sent in ONE Confirm in this order:
//
//	1. parent  BUY  LMT  qty @ entry              Transmit=false
//	2. TP      SELL LMT  qty @ tp   ParentID=1    Transmit=false  OCA vwapx-<id>
//	3. stop    SELL STP  qty aux=stop ParentID=1  Transmit=true   OCA vwapx-<id>
//
// Nothing is live at IB until the LAST child (the stop) is transmitted, so a
// failure while sending leaves nothing working. IB sizes both children to the
// parent's filled quantity and cancels them if the parent is cancelled; the
// shared OCA group (type 1) makes a fill of one cancel the other.
//
// The order has NO soft stop (the stop lives at IB) and none of the 9EMA
// machinery: it does not follow the 9EMA, is not auto-cancelled on a fast drop,
// has no entry timeout, no 5m trail, no breakeven, and its TP is never re-priced
// to the fill average. Only the existing CANCEL and CLOSE buttons act on it
// (CLOSE also cancels the IB stop before it sells; see stop.go).
//
// VWAP X down (side SELL) is the exact mirror for the 9EMA VWAP BEAR cross
// (kind "vwapBear", Config.LatestVwapBear): a SHORT. Same Kind "vwapx" with
// Side SELL; entry SELL LMT at the current price, stop = BUY STP at the VWAP
// (rounded up), TP = BUY LMT at entry - 3 x (stop - entry) rounded down; the children
// are the same OCA bracket (same DAY/outsideRth/OCA constants). Short checks:
// a symbol KNOWN to have no shares to borrow (shortinfo.go) is unavailable
// ("not shortable (no borrow)"); unknown borrow stays available and says
// "borrow unknown". There is no SSR data in the scanner: IB enforces it.
//
// The candle: the cross comes from the flaglog recorder (Config.LatestVwapBull,
// the bar's START ms; the engine can fire it mid-candle). The preset is
// available only once that candle has CLOSED, for 10 minutes, while the price
// is on the right side of the VWAP. The VWAP is the ticket's own (book.vwap):
// the session's 1-minute bars, regular hours from 09:30, like the engine's scope
// VWAP. The levels move with the price until Confirm, which sends the ones
// shown if the price is still between that stop and TP.

import (
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	kindVwapx    = "vwapx"
	vwapxMaxAge  = 10 * time.Minute
	vwapxTPPct   = 1.0 // recorded on the order; the TP itself is vwapxTPR
	vwapxTPR     = 3.0 // take profit: 3 x the entry-VWAP distance
	vwapxSLNote  = "vwapx-sl"
	vwapxTPNote  = "vwapx-tp"
	vwapxOCAType = 1 // IB OCA type 1: cancel the rest of the group (with block)
)

// vwapxOCA: also put the two children in a shared OCA group. IB treats the
// children of one parent as one-cancels-other anyway; this is belt and braces.
// NOT verifiable without a live TWS (whether IB accepts an explicit OCA group
// on bracket children) - flip it to false if IB ever rejects the children.
const vwapxOCA = true

// Vwapx is the preset's state for the pane (State.Vwapx).
type Vwapx struct {
	Available   bool    `json:"available"`
	Reason      string  `json:"reason"`
	CrossMs     int64   `json:"crossMs"`     // the cross candle's start (ms)
	ClosesInSec int     `json:"closesInSec"` // >0 while the cross candle is still forming
	Entry       float64 `json:"entry"`       // current price
	Stop        float64 `json:"stop"`        // session VWAP
	TP          float64 `json:"tp"`          // entry + 3 x (entry - stop), rounded away
	AgeSec      int64   `json:"ageSec"`      // since the cross candle started
	StopPct     float64 `json:"stopPct"`     // (stop / entry - 1) x 100: negative long, positive short
	Side        string  `json:"side"`        // BUY (VWAP X, bull cross) | SELL (VWAP X down, bear cross: a short)
	// Borrow (short only): "ok" = IBKR lists shares to short, "unknown" = the
	// ticket has no (or no usable) short-availability data. A symbol known NOT
	// to be shortable is unavailable ("not shortable (no borrow)"). There is
	// no SSR (short-sale restriction) data anywhere in the scanner: IB enforces it.
	Borrow string `json:"borrow"`
}

// vwap is sym's session VWAP at now from the book's 1-minute bars (0 = none):
// the bar's own VW, else the typical price, from the session's start.
func (b *book) vwap(sym string, now time.Time) float64 {
	et := now.In(etLoc())
	mod := et.Hour()*60 + et.Minute()
	start := 4 * 60
	switch {
	case mod >= 16*60:
		start = 16 * 60
	case mod >= 9*60+30:
		start = 9*60 + 30
	}
	from := time.Date(et.Year(), et.Month(), et.Day(), start/60, start%60, 0, 0, etLoc()).UnixMilli()
	b.mu.Lock()
	defer b.mu.Unlock()
	var num, den float64
	for k, x := range b.bars[sym] {
		if k < from || k > now.UnixMilli() || x.V <= 0 {
			continue
		}
		p := x.VW
		if !(p > 0) {
			p = (x.H + x.L + x.C) / 3
		}
		num += p * float64(x.V)
		den += float64(x.V)
	}
	if !(den > 0) {
		return 0
	}
	return num / den
}

// vwapxLevels: entry = the price rounded down, stop = the VWAP rounded down, tp
// = entry + 3 x the distance rounded up.
func vwapxLevels(price, vwap float64) (entry, stop, tp float64) {
	entry = snapPrice(price, false)
	stop = snapPrice(vwap, false)
	tp = snapPrice(entry+vwapxTPR*(entry-stop), true)
	return
}

// vwapxShortLevels is the bear mirror, each level rounded the other way.
func vwapxShortLevels(price, vwap float64) (entry, stop, tp float64) {
	entry = snapPrice(price, true)
	stop = snapPrice(vwap, true)
	tp = snapPrice(entry-vwapxTPR*(stop-entry), false)
	return
}

// vwapxLocked evaluates the (bull, long) preset for sym at now (q = sym's quote).
func (t *Ticket) vwapxLocked(sym string, q Quote, now time.Time) *Vwapx {
	return t.vwapxSideLocked("BUY", sym, q, now)
}

// vwapxSideLocked evaluates the preset for side BUY (bull cross) or SELL (bear cross, a short).
func (t *Ticket) vwapxSideLocked(side, sym string, q Quote, now time.Time) *Vwapx {
	v := &Vwapx{Side: side}
	short := side == "SELL"
	src, kindName := t.cfg.LatestVwapBull, "bull"
	if short {
		src, kindName = t.cfg.LatestVwapBear, "bear"
	}
	switch {
	case src == nil:
		v.Reason = "VWAP cross source not wired"
		return v
	case sym == "":
		v.Reason = "no symbol"
		return v
	}
	crossMs, ok := src(sym)
	if !ok {
		v.Reason = "no VWAP " + kindName + " cross today"
		return v
	}
	v.CrossMs = crossMs
	nowMs := now.UnixMilli()
	v.AgeSec = (nowMs - crossMs) / 1000
	if end := crossMs + 60000; nowMs < end {
		v.ClosesInSec = int(math.Ceil(float64(end-nowMs) / 1000))
		v.Reason = fmt.Sprintf("cross candle closes in %ds", v.ClosesInSec)
		return v
	}
	if time.Duration(nowMs-crossMs)*time.Millisecond > vwapxMaxAge {
		v.Reason = fmt.Sprintf("cross is %dm old", (nowMs-crossMs)/60000)
		return v
	}
	vwap := t.book.vwap(sym, now)
	if !(vwap > 0) || !(q.Price > 0) {
		v.Reason = "no VWAP or price yet"
		return v
	}
	if short {
		v.Entry, v.Stop, v.TP = vwapxShortLevels(q.Price, vwap)
		if !(v.Stop > v.Entry) || !(v.TP > 0) || !(v.TP < v.Entry) {
			v.Reason = "price is not under the VWAP"
			return v
		}
		v.StopPct = (v.Stop/v.Entry - 1) * 100
		v.Borrow = "unknown"
		if si := t.shortInfoLocked(sym, now); si != nil && si.Known {
			if !si.Shortable {
				v.Reason = "not shortable (no borrow)"
				return v
			}
			v.Borrow = "ok"
		}
		v.Available = true
		return v
	}
	v.Entry, v.Stop, v.TP = vwapxLevels(q.Price, vwap)
	if !(v.Stop > 0) || !(v.Stop < v.Entry) || !(v.TP > v.Entry) {
		v.Reason = "price is not above the VWAP"
		return v
	}
	v.StopPct = (v.Stop/v.Entry - 1) * 100
	v.Available = true
	return v
}

func etHHMM(ms int64) string { return time.UnixMilli(ms).In(etLoc()).Format("15:04") }

func vwapxText(p Pending) string {
	if p.Side == "SELL" {
		return vwapxShortText(p)
	}
	s := fmt.Sprintf("BUY %s %s LMT %s (VWAP X %s) · STP %s (VWAP) · TP 3R %s",
		fmtQty(p.Qty), p.Sym, fmtPx(p.Limit), etHHMM(p.CrossMs), fmtPx(p.PlannedStop), fmtPx(p.TP))
	s += fmt.Sprintf(" · ~$%s, risk $%s", fmtQty(int64(math.Round(float64(p.Qty)*p.Limit))), fmtQty(int64(math.Round(p.Risk))))
	if p.Bound != "" {
		s += " [" + p.Bound + "]"
	}
	return s
}

// vwapxShortText: "SELL SHORT 120 MGM LMT 27.99 (VWAP X↓ 14:01) · STP 28.21 (VWAP) ·
// TP 3R 27.33 · ~$3,359, risk $26 · shortable 150K (fee 12.5%)" (or "borrow unknown").
func vwapxShortText(p Pending) string {
	s := fmt.Sprintf("SELL SHORT %s %s LMT %s (VWAP X↓ %s) · STP %s (VWAP) · TP 3R %s",
		fmtQty(p.Qty), p.Sym, fmtPx(p.Limit), etHHMM(p.CrossMs), fmtPx(p.PlannedStop), fmtPx(p.TP))
	s += fmt.Sprintf(" · ~$%s, risk $%s", fmtQty(int64(math.Round(float64(p.Qty)*p.Limit))), fmtQty(int64(math.Round(p.Risk))))
	if p.Bound != "" {
		s += " [" + p.Bound + "]"
	}
	if n := strings.Replace(p.ShortNote, "shortable unknown", "borrow unknown", 1); n != "" {
		s += n
	} else {
		s += " · borrow unknown"
	}
	return s
}

// PrepareVwapx builds the confirmation for the VWAP X bracket (BUY / long).
// Sizing is the normal one: shares > 0, else riskUSD > 0 (floor(risk / (entry -
// stop))), else floor(position$ / entry); always capped by the max position $.
// Like every prepare it asks TWS for the margin (what-if) first.
func (t *Ticket) PrepareVwapx(positionUSD float64, shares int64, sym string, riskUSD float64) (Pending, error) {
	return t.PrepareVwapxSide("BUY", positionUSD, shares, sym, riskUSD)
}

// PrepareVwapxSide: side BUY = VWAP X (bull cross, long), SELL = VWAP X down
// (bear cross, short). Same sizing, same what-if, same confirm flow.
func (t *Ticket) PrepareVwapxSide(side string, positionUSD float64, shares int64, sym string, riskUSD float64) (Pending, error) {
	side = strings.ToUpper(side)
	if side == "" {
		side = "BUY"
	}
	if side != "BUY" && side != "SELL" {
		return Pending{}, fmt.Errorf("bad side %q", side)
	}
	t.freshenBook(t.curSym())
	p, err := t.prepareVwapxPending(side, positionUSD, shares, sym, riskUSD)
	if err != nil {
		return Pending{}, err
	}
	return t.finishPrepare(p)
}

func (t *Ticket) prepareVwapxPending(side string, positionUSD float64, shares int64, sym string, riskUSD float64) (Pending, error) {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending = nil
	if t.conn != "connected" {
		return Pending{}, t.refuse("not connected to TWS")
	}
	cur, _ := t.currentSymLocked()
	if cur == "" {
		return Pending{}, t.refuse("no symbol: TWS has not reported one")
	}
	if sym != "" && !strings.EqualFold(sym, cur) {
		return Pending{}, t.refuse(fmt.Sprintf("symbol mismatch: ticket %s, TWS %s", sym, cur))
	}
	if shares < 0 || shares > maxOrderShares {
		return Pending{}, t.refuse("bad share count")
	}
	if riskUSD < 0 || riskUSD > 5000 {
		return Pending{}, t.refuse("bad risk $")
	}
	if shares == 0 && !(positionUSD > 0) && !(riskUSD > 0) {
		return Pending{}, t.refuse("pick a risk or enter shares")
	}
	if shares == 0 && riskUSD == 0 && positionUSD > t.settings.MaxPositionUSD {
		return Pending{}, t.refuse(fmt.Sprintf("position $%.0f is above the max $%.0f", positionUSD, t.settings.MaxPositionUSD))
	}
	q := t.quoteLocked(cur, now)
	v := t.vwapxSideLocked(side, cur, q, now)
	if !v.Available {
		return Pending{}, t.refuse(vwapxName(side) + ": " + v.Reason)
	}
	qty, bound := shares, ""
	switch {
	case qty > 0:
		if float64(qty)*v.Entry > t.settings.MaxPositionUSD+1e-9 {
			return Pending{}, t.refuse(fmt.Sprintf("%d shares x %s is above the max $%.0f", qty, fmtPx(v.Entry), t.settings.MaxPositionUSD))
		}
	case riskUSD > 0:
		qty, bound = riskSize(side, riskUSD, v.Entry, v.Stop, t.settings.MaxPositionUSD)
	default:
		qty = sizeQty(positionUSD, v.Entry, v.Entry, t.settings.MaxPositionUSD)
	}
	if qty < 1 {
		return Pending{}, t.refuse("quantity is 0")
	}
	t.tokSeq++
	p := Pending{
		Token: fmt.Sprintf("%d-%d", now.UnixNano(), t.tokSeq), Kind: kindVwapx, Side: side, Sym: cur, Qty: qty,
		Limit: v.Entry, TP: v.TP, At: now.UnixMilli(), TPPct: vwapxTPPct, CrossMs: v.CrossMs,
		RiskUSD: riskUSD, PlannedStop: v.Stop, Bound: bound, StopOff: true, // the stop is IB's, not the soft stop
		Risk: float64(qty) * perShareRisk(side, v.Entry, v.Stop),
	}
	return p, nil
}

// confirmVwapxLocked sends the 3-order bracket for p (lock held; returns the
// client calls to run after it is released). Everything the preset depends on
// is re-checked: a changed cross / level / price needs a fresh VWAP X click.
func (t *Ticket) confirmVwapxLocked(p *Pending, now time.Time) ([]func(), error) {
	if t.conn != "connected" || t.client == nil || !t.haveNext {
		return nil, t.refuse("not connected to TWS")
	}
	side := p.Side
	if side != "SELL" {
		side = "BUY"
	}
	v := t.vwapxSideLocked(side, p.Sym, t.quoteLocked(p.Sym, now), now)
	if !v.Available {
		return nil, t.refuse(vwapxName(side) + " no longer valid: " + v.Reason)
	}
	if v.CrossMs != p.CrossMs {
		return nil, t.refuse(vwapxName(side) + " new cross: press " + vwapxName(side) + " again")
	}
	if px := t.quoteLocked(p.Sym, now).Price; px > 0 && ((side == "BUY" && (px <= p.PlannedStop || px >= p.TP)) || (side == "SELL" && (px >= p.PlannedStop || px <= p.TP))) {
		return nil, t.refuse(vwapxName(side) + " price left the levels: press " + vwapxName(side) + " again")
	}
	if p.Qty < 1 || !(p.Limit > 0) || !(p.TP > 0) || !(p.PlannedStop > 0) ||
		(side == "BUY" && (!(p.TP > p.Limit) || !(p.PlannedStop < p.Limit))) ||
		(side == "SELL" && (!(p.TP < p.Limit) || !(p.PlannedStop > p.Limit))) {
		return nil, t.refuse("quantity or price invalid")
	}
	if float64(p.Qty)*p.Limit > t.settings.MaxPositionUSD+1e-9 {
		return nil, t.refuse("above the max position $")
	}
	if m := p.Margin; m != nil && m.Status != "unavailable" && p.Qty > m.MaxShares {
		return nil, t.refuse(fmt.Sprintf("%d shares is above the margin max %d (use max)", p.Qty, m.MaxShares))
	}
	o := &ord{
		ID: t.nextID, ChildID: t.nextID + 1, SLID: t.nextID + 2, Kind: kindVwapx, Sym: p.Sym, Side: side, Qty: p.Qty,
		Limit: p.Limit, TP: p.TP, SLPx: p.PlannedStop, CrossMs: p.CrossMs, Remaining: p.Qty,
		Status: "Sent", ChildStatus: "Sent", SLStatus: "Sent", PlacedAt: now.UnixMilli(), TPPct: vwapxTPPct,
	}
	o.StopOff, o.DropOff = true, true // no soft stop, no 9EMA fast-drop cancel
	o.noteTP(o.TP)
	o.noteTPSent(o.TP, now)
	o.childQty = o.Qty
	t.nextID += 3
	t.orders[o.ID] = o
	t.byChild[o.ChildID] = o
	t.bySL[o.SLID] = o
	cid := t.settings.ClientID
	t.known[o.ID] = auditRec{ID: o.ID, ClientID: cid, Sym: o.Sym, Side: o.Side, TPPct: o.TPPct, StopOff: true, DropOff: true, Kind: kindVwapx}
	t.known[o.ChildID] = auditRec{ID: o.ChildID, ParentID: o.ID, ClientID: cid, Sym: o.Sym, Side: o.Side, TPPct: o.TPPct, Kind: kindVwapx, Note: vwapxTPNote}
	t.known[o.SLID] = auditRec{ID: o.SLID, ParentID: o.ID, ClientID: cid, Sym: o.Sym, Side: o.Side, Kind: kindVwapx, Note: vwapxSLNote}
	c := t.client
	parent, tp, sl := t.entryOrder(o, false), t.vwapxTP(o, false), t.slOrder(o, true)
	t.note = "sent: " + p.Text
	id, cidTP, idSL, sym := o.ID, o.ChildID, o.SLID, o.Sym
	acts := []func(){
		func() {
			t.record(auditRec{Ev: "place", ID: id, ClientID: cid, Sym: sym, Side: side, Qty: o.Qty, Limit: o.Limit, TP: o.TP, TPPct: o.TPPct, Kind: kindVwapx,
				StopLevel: o.SLPx, Note: fmt.Sprintf("%s cross %s: entry %s, IB stop %s, TP %s", vwapxName(side), etHHMM(o.CrossMs), fmtPx(o.Limit), fmtPx(o.SLPx), fmtPx(o.TP))})
			t.record(auditRec{Ev: "stop", ID: id, Sym: sym, StopOff: true, Kind: kindVwapx, Note: "placed: no soft stop (real STP child at IB)"})
			t.record(auditRec{Ev: "place", ID: cidTP, ParentID: id, ClientID: cid, Sym: sym, Side: side, Qty: o.Qty, Limit: o.TP, TPPct: o.TPPct, Kind: kindVwapx, Note: vwapxTPNote})
			t.record(auditRec{Ev: "place", ID: idSL, ParentID: id, ClientID: cid, Sym: sym, Side: side, Qty: o.Qty, Limit: o.SLPx, Kind: kindVwapx, Note: vwapxSLNote})
		},
		func() { c.PlaceOrder(id, contractFor(sym), parent) },
		func() { c.PlaceOrder(cidTP, contractFor(sym), tp) },
		func() { c.PlaceOrder(idSL, contractFor(sym), sl) },
	}
	return acts, nil
}

func vwapxName(side string) string {
	if side == "SELL" {
		return "VWAP X down"
	}
	return "VWAP X"
}

// exitAction: the side of the bracket's children (they close the entry).
func exitAction(entrySide string) string {
	if entrySide == "SELL" {
		return "BUY"
	}
	return "SELL"
}

func vwapxOcaGroup(o *ord) string {
	if !vwapxOCA {
		return ""
	}
	return fmt.Sprintf("vwapx-%d", o.ID)
}

func vwapxOcaType() int64 {
	if !vwapxOCA {
		return 0
	}
	return vwapxOCAType
}

// vwapxTP is the take-profit child: SELL LMT at the TP, DAY, outsideRth, a child
// of the entry. transmit=false while the bracket is being built (the stop
// child, sent last, transmits it); a later re-send (tpverify) transmits.
func (t *Ticket) vwapxTP(o *ord, transmit bool) *Order {
	return &Order{Action: exitAction(o.Side), OrderType: "LMT", TotalQuantity: Qty(o.Qty), LmtPrice: o.TP, TIF: "DAY", OutsideRTH: true,
		OcaGroup: vwapxOcaGroup(o), OcaType: vwapxOcaType(), OrderRef: refPrefix + o.Sym + ":tp", Account: t.account,
		ParentID: o.ID, Transmit: transmit}
}

// slOrder is the REAL stop child: SELL STP, trigger = AuxPrice, DAY. IB accepts
// outsideRth on a STP order (the stop may then trigger in the extended session
// instead of waiting for 09:30 ET); how it fills out there is IB's call.
func (t *Ticket) slOrder(o *ord, transmit bool) *Order {
	return &Order{Action: exitAction(o.Side), OrderType: "STP", TotalQuantity: Qty(o.Qty), AuxPrice: o.SLPx, TIF: "DAY", OutsideRTH: true,
		OcaGroup: vwapxOcaGroup(o), OcaType: vwapxOcaType(), OrderRef: refPrefix + o.Sym + ":sl", Account: t.account,
		ParentID: o.ID, Transmit: transmit}
}

func (o *ord) slActive() bool { return o.SLID != 0 && !finished(o.SLStatus) }
