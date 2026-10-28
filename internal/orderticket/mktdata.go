package orderticket

// Read-only market data for the SHORTABLE tile: one reqMktData subscription
// (generic tick list "236" = shortable) for the ticket's CURRENT symbol, so
// the tile shows TWS's live count:
//
//	tick 89 "Shortable shares" (tickSize; some builds send it as tickGeneric)
//	tick 46 "Shortable" indicator (tickGeneric): > 2.5 shares available /
//	        easy to borrow, > 1.5 hard to borrow (locate), <= 1.5 none
//
// One subscription at a time: the old one is cancelled before a new symbol's
// is requested, and on Disconnect. Request ids live in their own range
// (mdReqBase+), apart from order / what-if ids and the other request ids.
// Market-data errors (354 not subscribed, 10089/10168 data permissions, 10197
// competing session, ...) never reach the status banner: they show in the
// tile tooltip and the tile falls back to the IBKR file. Nothing order-related
// is sent from here.
//
// Encoders/decoders follow ibapi v0.10.37 at server versions 100..176
// (byte-checked in twsclient_ibapi_test.go).

import (
	"strconv"
	"time"
)

const (
	outReqMktData    = 1
	outCancelMktData = 2
	inTickPrice      = 1
	inTickSize       = 2
	inTickGeneric    = 45
	inTickString     = 46
	inTickReqParams  = 81

	tickShortable       = 46 // generic: shortable indicator
	tickShortableShares = 89 // size: shortable shares

	mdReqBase         = 900_000_000
	svSmartComponents = 114 // MIN_SERVER_VER_REQ_SMART_COMPONENTS
	liveWithin        = 60 * time.Second
)

// encReqMktData: reqMktData VERSION 11 for a plain STK contract.
func encReqMktData(sv int, reqID int64, ct *Contract, generic string) []string {
	f := []string{"1", "11", i64(reqID),
		i64(ct.ConID), ct.Symbol, ct.SecType,
		"", // lastTradeDateOrContractMonth
		"", // strike (UNSET -> sendMax empty)
		"", // right
		"", // multiplier
		ct.Exchange,
		"", // primaryExch
		ct.Currency,
		"",  // localSymbol
		"",  // tradingClass (>= 68)
		"0", // deltaNeutralContract present (>= 40)
		generic,
		"0", // snapshot
	}
	if sv >= svSmartComponents {
		f = append(f, "0") // regulatorySnapshot
	}
	return append(f, "") // mktDataOptions (>= LINKING 70)
}

// encCancelMktData: [2, VERSION 2, reqId].
func encCancelMktData(reqID int64) []string { return []string{"2", "2", i64(reqID)} }

func (c *TWSClient) ReqMktData(reqID int64, ct *Contract, generic string) {
	c.send(encReqMktData(c.version(), reqID, ct, generic))
}
func (c *TWSClient) CancelMktData(reqID int64) { c.send(encCancelMktData(reqID)) }

// dispatchTick decodes the market-data messages; ok=false for other ids.
func dispatchTick(w Wrapper, id int, r *fields) bool {
	switch id {
	case inTickPrice: // [1, version, reqId, tickType, price, size, attrMask]
		r.skip(1)
		reqID, tt, px := r.i64(), r.i64(), r.f64()
		r.skip(2)
		if !r.bad {
			w.TickPrice(reqID, tt, px)
		}
	case inTickSize: // [2, version, reqId, tickType, size]
		r.skip(1)
		reqID, tt, sz := r.i64(), r.i64(), r.f64()
		if !r.bad {
			w.TickSize(reqID, tt, sz)
		}
	case inTickGeneric: // [45, version, reqId, tickType, value]
		r.skip(1)
		reqID, tt, v := r.i64(), r.i64(), r.f64()
		if !r.bad {
			w.TickGeneric(reqID, tt, v)
		}
	case inTickString: // [46, version, reqId, tickType, value]
		r.skip(1)
		reqID, tt, v := r.i64(), r.i64(), r.s()
		if !r.bad {
			w.TickString(reqID, tt, v)
		}
	case inTickReqParams: // [81, tickerId, minTick, bboExchange, snapshotPermissions] (no version)
		reqID, mt, bbo, perm := r.i64(), r.f64(), r.s(), r.i64()
		if !r.bad {
			w.TickReqParams(reqID, mt, bbo, perm)
		}
	default:
		return false
	}
	return true
}

// ─── ticket side ─────────────────────────────────────────────────────────

type mdSub struct {
	reqID      int64
	sym        string
	liveShares float64
	haveShares bool
	liveInd    float64
	haveInd    bool
	liveAt     time.Time
	err        string
}

// syncMktDataLocked keeps exactly one subscription on the current symbol
// while connected; returns the client calls (cancel old first, then request).
func (t *Ticket) syncMktDataLocked() []func() {
	want := ""
	if t.conn == "connected" && t.client != nil {
		want, _ = t.currentSymLocked()
	}
	if want == t.md.sym {
		return nil
	}
	c := t.client
	var acts []func()
	if t.md.reqID != 0 && c != nil {
		old := t.md.reqID
		acts = append(acts, func() { c.CancelMktData(old) })
	}
	t.md = mdSub{}
	if want != "" && c != nil {
		t.mdSeq++
		id := mdReqBase + t.mdSeq
		t.md = mdSub{reqID: id, sym: want}
		ct := contractFor(want)
		acts = append(acts, func() { c.ReqMktData(id, ct, "236") })
	}
	return acts
}

func (t *Ticket) onTick(gen, reqID, tickType int64, v float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.live(gen) || reqID == 0 || reqID != t.md.reqID {
		return
	}
	switch tickType {
	case tickShortableShares:
		t.md.liveShares, t.md.haveShares = v, true
	case tickShortable:
		t.md.liveInd, t.md.haveInd = v, true
	default:
		return
	}
	t.md.liveAt, t.md.err = t.now(), ""
}

// mdErrorLocked swallows market-data errors for the subscription (true = handled).
func (t *Ticket) mdErrorLocked(reqID, code int64, msg string) bool {
	if reqID == 0 || reqID != t.md.reqID {
		return reqID >= mdReqBase // an old subscription's late error: drop too
	}
	t.md.err = "IB " + strconv.FormatInt(code, 10) + ": " + msg
	return true
}
