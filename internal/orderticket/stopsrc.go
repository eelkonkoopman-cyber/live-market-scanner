package orderticket

// Price sources for the soft stop, and the "SL blind" guard.
//
// 1. Massive per-second aggregates (FeedAgg, srcAgg). They reach the ticket
//    about 3 s after the second STARTS (measured on 2026-10-29: breakeven and
//    trigger records land at bar start + ~3.24 s), and they stop completely
//    while the scanner's market stream reconnects.
// 2. TWS's own market data (srcTWS): one reqMktData (no generic ticks) per
//    symbol with a working soft stop (armed / triggered / exiting), at most
//    maxStopMD lines so the account's market-data line limit (100 by default,
//    shared with TWS's own windows) is never at risk. LAST ticks (type 4) feed
//    the trigger; BID/ASK ticks only prove the line is alive (they count for
//    the blind guard and never trigger: a quote can flicker far through the
//    stop in a thin pre-market book). Subscriptions are cancelled as soon as
//    the stop is finished, and all of them on Disconnect. Delayed data (ticks
//    66-68) is ignored.
//
// Each source is debounced separately (stop.go), so a 3 s-old aggregate on
// the right side never resets a TWS print through the stop; whichever source
// confirms first triggers.
//
// Aggregate LOW/HIGH instead of the close (decision, 2026-10-29): a long uses
// the second's low, a short its high, with the same 2-print / 1 s debounce.
// Close-only missed DAVI (lows 3.26, 3.26 at a 3.26 stop, but closes 3.2607,
// 3.26, 3.30 - one print, reset). The cost: two consecutive seconds that only
// wick to the stop now exit, where the close rule would have held; a single
// bad print (one second) still cannot trigger. For a stop, missing a real
// break is worse than an early exit, so the wick rule is used.
//
// SL blind: an ARMED stop whose symbol has had no price from either source for
// more than stopBlindAfter, while connected and inside 04:00-20:00 ET on a
// weekday, raises a red State.StopAlert "SL blind: <SYM> no price for N s".
// Start and end are audited (one record each, not one per second).

import (
	"fmt"
	"time"
)

const (
	srcAgg = iota
	srcTWS
	nPxSrc
)

var srcName = [nPxSrc]string{"agg", "TWS"}

const (
	stopMDBase     = 910_000_000 // request ids of the stop's price lines
	maxStopMD      = 10          // market-data lines the stops may use
	stopBlindAfter = 3 * time.Second
	twsPxFresh     = 3 * time.Second // a TWS last this recent prices the exit

	tickBid  = 1
	tickAsk  = 2
	tickLast = 4
)

type stopDeb struct {
	hits  int
	first time.Time
	last  float64 // the source's latest trigger value
	have  bool
}

// stopSub is one TWS price line for a stopped symbol.
type stopSub struct {
	reqID  int64
	last   float64
	lastAt time.Time
	err    string
}

// aggTrig: the aggregate's trigger value for the side (low long, high short),
// falling back to the close when the aggregate carries no range.
func aggTrig(side string, last, lo, hi float64) float64 {
	if side == "BUY" {
		if lo > 0 && lo <= last {
			return lo
		}
		return last
	}
	if hi >= last {
		return hi
	}
	return last
}

func (t *Ticket) pxSeenLocked(sym string, now time.Time) {
	if t.pxSeen == nil {
		t.pxSeen = map[string]time.Time{}
	}
	t.pxSeen[sym] = now
}

// stopLastLocked: the price an exit is sized against - a fresh TWS last if
// there is one (it is ~3 s ahead of the aggregates), else the ticket's quote.
func (t *Ticket) stopLastLocked(sym string, now time.Time) float64 {
	if s := t.stopMD[sym]; s != nil && s.last > 0 && now.Sub(s.lastAt) <= twsPxFresh {
		return s.last
	}
	return t.quoteLocked(sym, now).Price
}

// wantStopMDLocked: the symbols that need a TWS price line (sorted, capped).
func (t *Ticket) wantStopMDLocked() map[string]bool {
	want := map[string]bool{}
	if t.conn != "connected" || t.client == nil {
		return want
	}
	var syms []string
	for _, o := range t.orders {
		if o.stopWatching() && !want[o.Sym] {
			want[o.Sym] = true
			syms = append(syms, o.Sym)
		}
	}
	if len(syms) > maxStopMD {
		sortStrings(syms)
		want = map[string]bool{}
		for _, s := range syms[:maxStopMD] {
			want[s] = true
		}
	}
	return want
}

// syncStopMDLocked reconciles the stop price lines (cancel first, then request).
func (t *Ticket) syncStopMDLocked() []func() {
	if t.stopMD == nil {
		t.stopMD = map[string]*stopSub{}
		t.stopMDIDs = map[int64]string{}
	}
	want := t.wantStopMDLocked()
	c := t.client
	var acts []func()
	for sym, s := range t.stopMD {
		if want[sym] {
			continue
		}
		delete(t.stopMD, sym)
		delete(t.stopMDIDs, s.reqID)
		if c != nil {
			id := s.reqID
			acts = append(acts, func() { c.CancelMktData(id) })
		}
	}
	var add []string
	for sym := range want {
		if t.stopMD[sym] == nil {
			add = append(add, sym)
		}
	}
	sortStrings(add)
	for _, sym := range add {
		t.stopMDSeq++
		id := stopMDBase + t.stopMDSeq
		t.stopMD[sym] = &stopSub{reqID: id}
		t.stopMDIDs[id] = sym
		ct := contractFor(sym)
		acts = append(acts, func() { c.ReqMktData(id, ct, "") })
	}
	return acts
}

// stopMDIDsLocked: every open stop price line (Disconnect cancels them).
func (t *Ticket) stopMDIDsLocked() []int64 {
	var ids []int64
	for _, s := range t.stopMD {
		ids = append(ids, s.reqID)
	}
	return ids
}

// onTickPrice: a TWS price tick; LAST feeds the stop, every price proves life.
func (t *Ticket) onTickPrice(gen, reqID, tickType int64, px float64) {
	if reqID < stopMDBase || !(px > 0) {
		return
	}
	t.mu.Lock()
	if !t.live(gen) {
		t.mu.Unlock()
		return
	}
	sym, ok := t.stopMDIDs[reqID]
	s := t.stopMD[sym]
	if !ok || s == nil {
		t.mu.Unlock()
		return
	}
	now := t.now()
	var acts []func()
	switch tickType {
	case tickBid, tickAsk:
		t.pxSeenLocked(sym, now)
	case tickLast:
		t.pxSeenLocked(sym, now)
		s.last, s.lastAt, s.err = px, now, ""
		for _, o := range t.orders {
			if o.Sym == sym {
				acts = append(acts, t.stopCheckLocked(o, px, now, true, srcTWS)...)
			}
		}
	}
	t.mu.Unlock()
	run(acts)
}

// stopMDErrorLocked keeps a stop price line's error (true = handled).
func (t *Ticket) stopMDErrorLocked(reqID, code int64, msg string) bool {
	if reqID < stopMDBase {
		return false
	}
	if sym, ok := t.stopMDIDs[reqID]; ok {
		if s := t.stopMD[sym]; s != nil {
			s.err = fmt.Sprintf("IB %d: %s", code, msg)
		}
	}
	return true // a late error of a cancelled line: dropped too
}

// sessionOpen: 04:00-20:00 ET on a weekday (the hours the symbol can trade).
func sessionOpen(now time.Time) bool {
	et := now.In(etLoc())
	if wd := et.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return false
	}
	m := et.Hour()*60 + et.Minute()
	return m >= 4*60 && m < 20*60
}

// blindCheckLocked raises / clears the SL blind alert for an armed stop.
func (t *Ticket) blindCheckLocked(o *ord, now time.Time) []func() {
	watching := o.StopState == stArmed && !o.StopOff && t.conn == "connected" && sessionOpen(now)
	base := time.UnixMilli(o.FillAt)
	if seen := t.pxSeen[o.Sym]; seen.After(base) {
		base = seen
	}
	gap := now.Sub(base)
	if !watching || o.FillAt == 0 || gap <= stopBlindAfter {
		if o.blindSince.IsZero() {
			return nil
		}
		was := now.Sub(o.blindSince) + stopBlindAfter
		o.blindSince, o.blindAlert = time.Time{}, ""
		if !watching {
			return []func(){t.stopRec(o, "SL blind cleared (stop no longer armed)")}
		}
		return []func(){t.stopRec(o, fmt.Sprintf("SL price back after %d s", int(was.Seconds())))}
	}
	alert := fmt.Sprintf("SL blind: %s no price for %d s", o.Sym, int(gap.Seconds()))
	if s := t.stopMD[o.Sym]; s != nil && s.err != "" {
		alert += " (TWS " + s.err + ")"
	}
	o.blindAlert = alert
	if !o.blindSince.IsZero() {
		return nil
	}
	o.blindSince = now
	return []func(){t.stopRec(o, alert)}
}
