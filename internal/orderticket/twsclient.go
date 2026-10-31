package orderticket

// TWSClient: the order ticket's own raw-socket TWS API client. It replaces
// github.com/scmhub/ibapi v0.10.37, which connected to TWS 10.51 but never
// delivered nextValidId / managedAccounts. It shares framing + handshake with
// internal/twslink (internal/ibwire), whose client is proven against the same
// TWS, and implements ONLY the messages the ticket uses.
//
// Messages are encoded for the negotiated server version, offered range
// v100..176 (ibwire.ClientVersionRange), following IB's reference client
// (EClient.java / EDecoder.java / EOrderDecoder.java). Server-version gates
// implemented (constants from EClient/EDecoder):
//
//	MIN_SERVER_VER_LINKING                 70  startApi optionalCapabilities
//	MIN_SERVER_VER_ORDER_CONTAINER        145  placeOrder has no version field;
//	                                           openOrder has no version field
//	MIN_SERVER_VER_MARKET_CAP_PRICE       131  orderStatus has no version field
//	MIN_SERVER_VER_SIZE_RULES             164  contractData has no version field
//	MIN_SERVER_VER_ADVANCED_ORDER_REJECT  166  error advancedOrderRejectJson;
//	                                           placeOrder advancedErrorOverride
//	MIN_SERVER_VER_MANUAL_ORDER_TIME      169  placeOrder manualOrderTime;
//	                                           cancelOrder manualOrderCancelTime
//	MIN_SERVER_VER_BOND_ISSUERID          176  reqContractDetails issuerId
//	MIN_SERVER_VER_FA_PROFILE_DESUPPORT   177  faProfile dropped (placeOrder, openOrder)
//	MIN_SERVER_VER_CME_TAGGING_FIELDS     192  cancelOrder version field dropped
//	MIN_SERVER_VER_ERROR_TIME             194  error errorTime field
//
// placeOrder also carries every field gate from 102 (PEGGED_TO_BENCHMARK) to
// 162 (AUTO_CANCEL_PARENT); see the svXxx constants. Byte equality with
// ibapi v0.10.37 for every gate is asserted in twsclient_ibapi_test.go.
//
// Gates above 176 can never be hit with this client range; they are coded so a
// lower-than-176 server (100..175) and the field layout stay explicit.
// Every gate below 100 (the range floor) is always on and encoded unconditionally.
//
// Requests are held until nextValidId arrives: TWS silently closes the socket
// if a request precedes the end of the startApi handshake (see twslink).

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/ibwire"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

// Outgoing message ids (EClient.java).
const (
	outPlaceOrder        = 3
	outCancelOrder       = 4
	outReqOpenOrders     = 5
	outReqIDs            = 8
	outReqContractData   = 9
	outReqManagedAccts   = 17
	outReqCurrentTime    = 49
	outSubscribeGroup    = 68
	outUnsubscribeGroup  = 70 // 69 is updateDisplayGroup, which the ticket never sends
	outStartAPI          = 71
	outReqAcctSummary    = 62
	outCancelAcctSummary = 63
	inAcctSummary        = 63
	inAcctSummaryEnd     = 64
	inOrderStatus        = 3
	inErrMsg             = 4
	inOpenOrder          = 5
	inNextValidID        = 9
	inContractData       = 10
	inManagedAccts       = 15
	inCurrentTime        = 49
	inContractDataEnd    = 52
	inOpenOrderEnd       = 53
	inDisplayGroupUpdate = 68
)

// Server-version gates (EClient.java MIN_SERVER_VER_*).
const (
	svPeggedToBenchmark   = 102
	svModelsSupport       = 103
	svExtOperator         = 105
	svSoftDollarTier      = 106
	svCashQty             = 111
	svDecisionMaker       = 138
	svMifidExecution      = 139
	svAutoPriceForHedge   = 141
	svDPegOrders          = 148
	svPriceMgmtAlgo       = 151
	svDuration            = 158
	svPostToAts           = 160
	svAutoCancelParent    = 162
	svMarketCapPrice      = 131
	svOrderContainer      = 145
	svSizeRules           = 164
	svAdvancedOrderReject = 166
	svManualOrderTime     = 169
	svWhatIfExtFields     = 142
	svFullOrderPreview    = 195
	svBondIssuerID        = 176
	svFAProfileDesupport  = 177
	svCMETagging          = 192
	svErrorTime           = 194
	svMin                 = 100
)

const twsDialTimeout = 4 * time.Second

// TWSClient implements EClient over a raw socket.
type TWSClient struct {
	w Wrapper

	mu      sync.Mutex
	conn    net.Conn
	srv     int
	ready   bool     // nextValidId seen: requests may be written
	queue   [][]byte // requests made before ready
	closing atomic.Bool
	up      atomic.Bool
}

// NewTWSClient builds a disconnected client delivering callbacks to w.
func NewTWSClient(w Wrapper) *TWSClient { return &TWSClient{w: w} }

// ServerVersion is the negotiated server version (0 before Connect).
func (c *TWSClient) ServerVersion() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.srv
}

// Connect dials, performs the handshake, sends startApi and starts the reader.
func (c *TWSClient) Connect(host string, port int, clientID int64) error {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), twsDialTimeout)
	if err != nil {
		return err
	}
	r := bufio.NewReaderSize(conn, 1<<16)
	srv, err := ibwire.Handshake(conn, r, 10*time.Second)
	if err != nil {
		conn.Close()
		return err
	}
	if srv < svMin {
		conn.Close()
		return fmt.Errorf("TWS server version %d is below %d", srv, svMin)
	}
	// startApi: VERSION 2, clientId, optionalCapabilities (>= LINKING).
	if _, err := conn.Write(ibwire.Encode(outStartAPI, 2, clientID, "")); err != nil {
		conn.Close()
		return err
	}
	c.mu.Lock()
	c.conn, c.srv, c.ready, c.queue = conn, srv, false, nil
	c.mu.Unlock()
	c.closing.Store(false)
	c.up.Store(true)
	logx.L().Info("order ticket TWS handshake", "serverVersion", srv, "client", clientID)
	go c.readLoop(conn, r, srv)
	return nil
}

// Disconnect closes the socket; no ConnectionClosed callback follows.
func (c *TWSClient) Disconnect() error {
	c.closing.Store(true)
	c.up.Store(false)
	c.mu.Lock()
	conn := c.conn
	c.conn, c.ready, c.queue = nil, false, nil
	c.mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

func (c *TWSClient) IsConnected() bool { return c.up.Load() }

func (c *TWSClient) send(fields []string) {
	args := make([]any, len(fields))
	for i, f := range fields {
		args[i] = f
	}
	b := ibwire.Encode(args...)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return
	}
	if !c.ready {
		c.queue = append(c.queue, b)
		return
	}
	if _, err := c.conn.Write(b); err != nil {
		c.conn.Close() // the reader sees it and reports ConnectionClosed
	}
}

func (c *TWSClient) version() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.srv
}

func (c *TWSClient) ReqIDs()          { c.send(encReqIDs()) }
func (c *TWSClient) ReqManagedAccts() { c.send(encReqManagedAccts()) }
func (c *TWSClient) ReqCurrentTime()  { c.send(encReqCurrentTime()) }
func (c *TWSClient) ReqOpenOrders()   { c.send(encReqOpenOrders()) }
func (c *TWSClient) PlaceOrder(id int64, ct *Contract, o *Order) {
	c.send(encPlaceOrder(c.version(), id, ct, o))
}
func (c *TWSClient) CancelOrder(id int64) { c.send(encCancelOrder(c.version(), id)) }
func (c *TWSClient) SubscribeToGroupEvents(reqID int64, groupID int) {
	c.send(encSubscribeGroup(reqID, groupID))
}
func (c *TWSClient) UnsubscribeFromGroupEvents(reqID int64) { c.send(encUnsubscribeGroup(reqID)) }
func (c *TWSClient) ReqAccountSummary(reqID int64, group, tags string) {
	c.send(encReqAccountSummary(reqID, group, tags))
}
func (c *TWSClient) CancelAccountSummary(reqID int64) { c.send(encCancelAccountSummary(reqID)) }
func (c *TWSClient) ReqContractDetails(reqID int64, ct *Contract) {
	c.send(encReqContractDetails(c.version(), reqID, ct))
}

func (c *TWSClient) readLoop(conn net.Conn, r *bufio.Reader, srv int) {
	for {
		f, err := ibwire.ReadMsg(r)
		if err != nil {
			break
		}
		if len(f) > 0 && f[0] == strconv.Itoa(inNextValidID) {
			c.mu.Lock()
			if c.conn == conn && !c.ready {
				c.ready = true
				for _, b := range c.queue {
					if _, err := conn.Write(b); err != nil {
						conn.Close()
						break
					}
				}
				c.queue = nil
			}
			c.mu.Unlock()
		}
		dispatch(c.w, srv, f)
	}
	conn.Close()
	c.mu.Lock()
	mine := c.conn == conn
	if mine {
		c.conn, c.ready, c.queue = nil, false, nil
	}
	c.mu.Unlock()
	if mine && !c.closing.Load() {
		c.up.Store(false)
		c.w.ConnectionClosed()
	}
}

// ─── encoding ────────────────────────────────────────────────────────────

func b2s(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// fnum formats a set double the way ibapi's encodeFloat64 does (shortest
// round-trip decimal, no forced ".0": 2 -> "2", 1.53 -> "1.53").
func fnum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// lmtField: sendMax(lmtPrice). A STP order has no limit price: ibapi leaves
// it at UNSET_DOUBLE, which sendMax writes as an empty field.
func lmtField(o *Order) string {
	if o.OrderType == "STP" {
		return ""
	}
	return fnum(o.LmtPrice)
}

// auxField: sendMax(auxPrice) - the stop trigger price of a STP order; every
// other order the ticket sends leaves it unset (empty).
func auxField(o *Order) string {
	if o.AuxPrice > 0 {
		return fnum(o.AuxPrice)
	}
	return ""
}

func qnum(q Qty) string { return strconv.FormatFloat(float64(q), 'f', -1, 64) }

// Unset values as ibapi v0.10.37 writes them where it does NOT use sendMax
// (Double.MAX_VALUE in plain decimal, Integer.MAX_VALUE); TWS reads both back
// as unset. Byte-checked in twsclient_ibapi_test.go.
var (
	unsetDbl = strconv.FormatFloat(math.MaxFloat64, 'f', -1, 64)
	unsetInt = strconv.Itoa(math.MaxInt32)
)

func i64(v int64) string { return strconv.FormatInt(v, 10) }

func encReqIDs() []string          { return []string{"8", "1", "1"} } // VERSION 1, numIds
func encReqManagedAccts() []string { return []string{"17", "1"} }
func encReqCurrentTime() []string  { return []string{"49", "1"} }
func encReqOpenOrders() []string   { return []string{"5", "1"} }
func encSubscribeGroup(reqID int64, g int) []string {
	return []string{"68", "1", i64(reqID), strconv.Itoa(g)}
}
func encUnsubscribeGroup(reqID int64) []string { return []string{"70", "1", i64(reqID)} }

// encReqAccountSummary: [62, VERSION 1, reqId, group, tags].
func encReqAccountSummary(reqID int64, group, tags string) []string {
	return []string{"62", "1", i64(reqID), group, tags}
}

// encCancelAccountSummary: [63, VERSION 1, reqId].
func encCancelAccountSummary(reqID int64) []string { return []string{"63", "1", i64(reqID)} }

func encCancelOrder(sv int, id int64) []string {
	f := []string{"4"}
	if sv < svCMETagging {
		f = append(f, "1") // VERSION
	}
	f = append(f, i64(id))
	if sv >= svManualOrderTime {
		f = append(f, "") // manualOrderCancelTime
	}
	return f
}

// encReqContractDetails: VERSION 8.
func encReqContractDetails(sv int, reqID int64, ct *Contract) []string {
	f := []string{"9", "8", i64(reqID),
		i64(ct.ConID), ct.Symbol, ct.SecType,
		"", // lastTradeDateOrContractMonth
		"", // strike (Contract default UNSET_DOUBLE -> sendMax empty)
		"", // right
		"", // multiplier
		ct.Exchange,
		"", // primaryExch
		ct.Currency,
		"",  // localSymbol
		"",  // tradingClass
		"0", // includeExpired
		"",  // secIdType
		"",  // secId
	}
	if sv >= svBondIssuerID {
		f = append(f, "") // issuerId
	}
	return f
}

// encPlaceOrder encodes an LMT (or any simple, non-combo, non-algo) order the
// way EClient.placeOrder does; every field the ticket does not set goes out
// at IB's Order default ("" = UNSET_DOUBLE/UNSET_INTEGER via sendMax).
func encPlaceOrder(sv int, id int64, ct *Contract, o *Order) []string {
	f := []string{"3"}
	if sv < svOrderContainer {
		f = append(f, "45") // VERSION (pre ORDER_CONTAINER)
	}
	f = append(f, i64(id),
		// contract
		i64(ct.ConID), ct.Symbol, ct.SecType,
		"", // lastTradeDateOrContractMonth
		"", // strike (Contract default UNSET_DOUBLE -> sendMax empty)
		"", // right
		"", // multiplier
		ct.Exchange,
		"", // primaryExch
		ct.Currency,
		"", // localSymbol
		"", // tradingClass
		"", // secIdType
		"", // secId
		// main order fields
		o.Action,
		qnum(o.TotalQuantity),
		o.OrderType,
		lmtField(o), // sendMax(lmtPrice); unset for STP
		auxField(o), // sendMax(auxPrice): the STP trigger; unset otherwise
		// extended order fields
		o.TIF,
		o.OcaGroup,
		o.Account,
		"",  // openClose
		"0", // origin (CUSTOMER)
		o.OrderRef,
		b2s(o.Transmit),
		i64(o.ParentID),
		"0", // blockOrder
		"0", // sweepToFill
		"0", // displaySize
		"0", // triggerMethod
		b2s(o.OutsideRTH),
		"0", // hidden
		// (no combo legs: secType != BAG)
		"",  // deprecated sharesAllocation
		"0", // discretionaryAmt
		"",  // goodAfterTime
		"",  // goodTillDate
		"",  // faGroup
		"",  // faMethod
		"",  // faPercentage
	)
	if sv < svFAProfileDesupport {
		f = append(f, "") // faProfile
	}
	if sv >= svModelsSupport {
		f = append(f,
			"", // modelCode (>= MODELS_SUPPORT 103)
		)
	}
	f = append(f,
		"0",            // shortSaleSlot
		"",             // designatedLocation
		"-1",           // exemptCode
		i64(o.OcaType), // ocaType (0 unless the ticket sets an OCA group)
		"",             // rule80A
		"",             // settlingFirm
		"0",            // allOrNone
		"",             // minQty
		"",             // percentOffset
		"0",            // eTradeOnly
		"0",            // firmQuoteOnly
		"",             // nbboPriceCap
		"0",            // auctionStrategy (AUCTION_UNSET)
		"",             // startingPrice
		"",             // stockRefPrice
		"",             // delta
		"",             // stockRangeLower
		"",             // stockRangeUpper
		"0",            // overridePercentageConstraints
		"",             // volatility
		"",             // volatilityType
		"",             // deltaNeutralOrderType (empty: no delta-neutral sub-fields)
		"",             // deltaNeutralAuxPrice
		"0",            // continuousUpdate
		"",             // referencePriceType
		"",             // trailStopPrice
		"",             // trailingPercent
		"",             // scaleInitLevelSize
		"",             // scaleSubsLevelSize
		"",             // scalePriceIncrement (unset: no scale sub-fields)
		"",             // scaleTable
		"",             // activeStartTime
		"",             // activeStopTime
		"",             // hedgeType (empty: no hedgeParam)
		"0",            // optOutSmartRouting
		"",             // clearingAccount
		"",             // clearingIntent
		"0",            // notHeld
		"0",            // deltaNeutralContract present = false
		"",             // algoStrategy (empty: no algo params)
		"",             // algoId
		b2s(o.WhatIf),  // whatIf (true only for the ticket's margin check)
		"",             // orderMiscOptions
		"0",            // solicited
		"0",            // randomizeSize
		"0",            // randomizePrice
	)
	if sv >= svPeggedToBenchmark {
		f = append(f,
			// (not PEG BENCH)
			"0",      // conditions count
			"",       // adjustedOrderType
			unsetDbl, // triggerPrice (ibapi: encodeFloat64(UNSET_DOUBLE))
			unsetDbl, // lmtPriceOffset (ibapi: encodeFloat64(UNSET_DOUBLE))
			unsetDbl, // adjustedStopPrice (ibapi: encodeFloat64(UNSET_DOUBLE))
			unsetDbl, // adjustedStopLimitPrice (ibapi: encodeFloat64(UNSET_DOUBLE))
			unsetDbl, // adjustedTrailingAmount (ibapi: encodeFloat64(UNSET_DOUBLE))
			"0",      // adjustableTrailingUnit
		)
	}
	if sv >= svExtOperator {
		f = append(f,
			"", // extOperator
		)
	}
	if sv >= svSoftDollarTier {
		f = append(f,
			"", // softDollarTier name
			"", // softDollarTier value
		)
	}
	if sv >= svCashQty {
		f = append(f,
			"", // cashQty
		)
	}
	if sv >= svDecisionMaker {
		f = append(f,
			"", // mifid2DecisionMaker
			"", // mifid2DecisionAlgo
		)
	}
	if sv >= svMifidExecution {
		f = append(f,
			"", // mifid2ExecutionTrader
			"", // mifid2ExecutionAlgo
		)
	}
	if sv >= svAutoPriceForHedge {
		f = append(f,
			"0", // dontUseAutoPriceForHedge
		)
	}
	if sv >= svOrderContainer {
		f = append(f,
			"0", // isOmsContainer
		)
	}
	if sv >= svDPegOrders {
		f = append(f,
			"0", // discretionaryUpToLimitPrice
		)
	}
	if sv >= svPriceMgmtAlgo {
		f = append(f,
			"", // usePriceMgmtAlgo (unset)
		)
	}
	if sv >= svDuration {
		f = append(f,
			unsetInt, // duration (ibapi: encodeInt64(UNSET_INTEGER))
		)
	}
	if sv >= svPostToAts {
		f = append(f,
			unsetInt, // postToAts (ibapi: encodeInt64(UNSET_INTEGER))
		)
	}
	if sv >= svAutoCancelParent {
		f = append(f,
			"0", // autoCancelParent
		)
	}
	if sv >= svAdvancedOrderReject {
		f = append(f, "") // advancedErrorOverride
	}
	if sv >= svManualOrderTime {
		f = append(f, "") // manualOrderTime
	}
	// PEGBEST_PEGMID_OFFSETS (170): nothing for exchange != IBKRATS and
	// orderType not PEG BEST / PEG MID.
	return f
}

// ─── decoding ────────────────────────────────────────────────────────────

type fields struct {
	f   []string
	i   int
	bad bool
}

func (r *fields) s() string {
	if r.i >= len(r.f) {
		r.bad = true
		return ""
	}
	v := r.f[r.i]
	r.i++
	return v
}

func (r *fields) skip(n int) {
	for k := 0; k < n; k++ {
		r.s()
	}
}

func (r *fields) i64() int64 {
	v := strings.TrimSpace(r.s())
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		x, ferr := strconv.ParseFloat(v, 64)
		if ferr != nil {
			r.bad = true
			return 0
		}
		return int64(x)
	}
	return n
}

func (r *fields) f64() float64 {
	v := strings.TrimSpace(r.s())
	if v == "" {
		return 0
	}
	x, err := strconv.ParseFloat(v, 64)
	if err != nil {
		r.bad = true
		return 0
	}
	if x >= math.MaxFloat64 { // UNSET_DOUBLE
		return 0
	}
	return x
}

func (r *fields) b() bool { return r.i64() != 0 }

var errShort = errors.New("short TWS message")

// dispatch decodes one incoming message and calls w. Unknown ids are ignored.
func dispatch(w Wrapper, sv int, f []string) {
	if len(f) == 0 {
		return
	}
	id, err := strconv.Atoi(f[0])
	if err != nil {
		return
	}
	r := &fields{f: f, i: 1}
	if dispatchTick(w, id, r) { // market data (mktdata.go)
		return
	}
	switch id {
	case inNextValidID: // [9, version, orderId]
		r.skip(1)
		v := r.i64()
		if !r.bad {
			w.NextValidID(v)
		}
	case inManagedAccts: // [15, version, accountsList]
		r.skip(1)
		s := r.s()
		if !r.bad {
			w.ManagedAccounts(strings.Split(s, ","))
		}
	case inCurrentTime: // [49, version, time]
		r.skip(1)
		v := r.i64()
		if !r.bad {
			w.CurrentTime(v)
		}
	case inErrMsg:
		reqID, errTime, code, msg, adv, err := decodeError(sv, f)
		if err == nil {
			w.Error(reqID, errTime, code, msg, adv)
		}
	case inOrderStatus:
		if sv < svMarketCapPrice {
			r.skip(1) // version
		}
		oid := r.i64()
		status := r.s()
		filled := Qty(r.f64())
		remaining := Qty(r.f64())
		avg := r.f64()
		perm := r.i64()
		parent := r.i64()
		last := r.f64()
		cid := r.i64()
		why := r.s()
		mkt := r.f64()
		if !r.bad {
			w.OrderStatus(oid, status, filled, remaining, avg, perm, parent, last, cid, why, mkt)
		}
	case inOpenOrder:
		oid, c, o, st, err := decodeOpenOrder(sv, f)
		if err == nil {
			w.OpenOrder(oid, c, o, st)
		}
	case inAcctSummary: // [63, version, reqId, account, tag, value, currency]
		r.skip(1)
		reqID := r.i64()
		acct, tag, val, cur := r.s(), r.s(), r.s(), r.s()
		if !r.bad {
			w.AccountSummary(reqID, acct, tag, val, cur)
		}
	case inAcctSummaryEnd: // [64, version, reqId]
		r.skip(1)
		v := r.i64()
		if !r.bad {
			w.AccountSummaryEnd(v)
		}
	case inOpenOrderEnd:
		w.OpenOrderEnd()
	case inContractData:
		reqID, cd, err := decodeContractData(sv, f)
		if err == nil {
			w.ContractDetails(reqID, cd)
		}
	case inContractDataEnd: // [52, version, reqId]
		r.skip(1)
		v := r.i64()
		if !r.bad {
			w.ContractDetailsEnd(v)
		}
	case inDisplayGroupUpdate: // [68, version, reqId, contractInfo]
		r.skip(1)
		reqID := r.i64()
		info := r.s()
		if !r.bad {
			w.DisplayGroupUpdated(reqID, info)
		}
	}
}

// decodeError: [4, version, id, code, msg, advancedOrderRejectJson(>=166), errorTime(>=194)].
func decodeError(sv int, f []string) (reqID, errTime, code int64, msg, adv string, err error) {
	r := &fields{f: f, i: 1}
	r.skip(1) // version (always 2 at these server versions)
	reqID = r.i64()
	code = r.i64()
	msg = r.s()
	if r.bad {
		return 0, 0, 0, "", "", errShort
	}
	if sv >= svAdvancedOrderReject && r.i < len(f) {
		adv = r.s()
	}
	if sv >= svErrorTime && r.i < len(f) {
		errTime = r.i64()
	}
	return reqID, errTime, code, msg, adv, nil
}

// decodeContractData reads the leading fields of contractData up to conId:
// [10, version(<164), reqId, symbol, secType, lastTradeDate, strike, right,
// exchange, currency, localSymbol, marketName, tradingClass, conId, ...].
func decodeContractData(sv int, f []string) (int64, *ContractDetails, error) {
	r := &fields{f: f, i: 1}
	if sv < svSizeRules {
		r.skip(1)
	}
	reqID := r.i64()
	cd := &ContractDetails{}
	cd.Contract.Symbol = r.s()
	cd.Contract.SecType = r.s()
	r.skip(3) // lastTradeDate, strike, right
	cd.Contract.Exchange = r.s()
	cd.Contract.Currency = r.s()
	r.skip(3) // localSymbol, marketName, tradingClass
	cd.Contract.ConID = r.i64()
	if r.bad {
		return 0, nil, errShort
	}
	return reqID, cd, nil
}

// decodeOpenOrder parses the openOrder fields up to parentId, following
// EOrderDecoder's read order with version = serverVersion (>= 145 sends no
// version field), then (decodeOrderTail) on through the OrderState block up to
// warningText. Fields after warningText (conditions, ...) are not read.
// Head layout:
//
//	orderId | conId symbol secType lastTradeDate strike right multiplier
//	exchange currency localSymbol tradingClass | action totalQuantity
//	orderType lmtPrice auxPrice tif ocaGroup account openClose origin orderRef
//	clientId permId outsideRth hidden discretionaryAmt goodAfterTime
//	sharesAllocation(skipped) faGroup faMethod faPercentage faProfile(<177)
//	modelCode goodTillDate rule80A percentOffset settlingFirm shortSaleSlot
//	designatedLocation exemptCode auctionStrategy startingPrice stockRefPrice
//	delta stockRangeLower stockRangeUpper displaySize blockOrder sweepToFill
//	allOrNone minQty ocaType eTradeOnly firmQuoteOnly nbboPriceCap | parentId
func decodeOpenOrder(sv int, f []string) (int64, *Contract, *Order, *OrderState, error) {
	r := &fields{f: f, i: 1}
	if sv < svOrderContainer {
		r.skip(1) // version
	}
	oid := r.i64()
	c := &Contract{}
	c.ConID = r.i64()
	c.Symbol = r.s()
	c.SecType = r.s()
	r.skip(3) // lastTradeDate, strike, right
	r.skip(1) // multiplier
	c.Exchange = r.s()
	c.Currency = r.s()
	r.skip(2) // localSymbol, tradingClass
	o := &Order{}
	o.Action = r.s()
	o.TotalQuantity = Qty(r.f64())
	o.OrderType = r.s()
	o.LmtPrice = r.f64()
	r.skip(1) // auxPrice
	o.TIF = r.s()
	o.OcaGroup = r.s()
	o.Account = r.s()
	r.skip(2) // openClose, origin
	o.OrderRef = r.s()
	o.ClientID = r.i64()
	o.PermID = r.i64()
	o.OutsideRTH = r.b()
	r.skip(1) // hidden
	r.skip(2) // discretionaryAmt, goodAfterTime
	r.skip(1) // sharesAllocation
	r.skip(3) // faGroup, faMethod, faPercentage
	if sv < svFAProfileDesupport {
		r.skip(1) // faProfile
	}
	if sv >= svModelsSupport {
		r.skip(1) // modelCode
	}
	r.skip(1) // goodTillDate
	r.skip(3) // rule80A, percentOffset, settlingFirm
	r.skip(3) // shortSaleSlot, designatedLocation, exemptCode
	r.skip(1) // auctionStrategy
	r.skip(3) // startingPrice, stockRefPrice, delta
	r.skip(2) // stockRangeLower, stockRangeUpper
	r.skip(1) // displaySize
	r.skip(3) // blockOrder, sweepToFill, allOrNone
	r.skip(2) // minQty, ocaType
	r.skip(3) // eTradeOnly, firmQuoteOnly, nbboPriceCap
	o.ParentID = r.i64()
	if r.bad {
		return 0, nil, nil, nil, errShort
	}
	return oid, c, o, decodeOrderTail(sv, r), nil
}

// decodeOrderTail reads openOrder from triggerMethod through the OrderState
// block, mirroring ibapi v0.10.37's processOpenOrderMsg (decodeTriggerMethod ..
// decodeWhatIfInfoAndCommissionAndFees). Only for sv >= ORDER_CONTAINER (145),
// where version == serverVersion so every per-version gate (20..34) is on;
// below 145, and from FULL_ORDER_PREVIEW_FIELDS (195, never negotiated by this
// client) on, the state is returned unparsed (Parsed=false). A malformed tail
// also yields Parsed=false; the head (ids, contract, order) is unaffected.
// Field equality with ibapi is asserted in twsclient_ibapi_test.go.
func decodeOrderTail(sv int, r *fields) *OrderState {
	st := &OrderState{}
	if sv < svOrderContainer || sv >= svFullOrderPreview {
		return st
	}
	count := func() int {
		n := r.i64()
		if n < 0 || n > 10000 {
			r.bad = true
			return 0
		}
		return int(n)
	}
	r.skip(1) // triggerMethod
	// decodeVolOrderParams(readOpenOrderAttribs=true)
	r.skip(2) // volatility, volatilityType
	dnType := r.s()
	r.skip(1) // deltaNeutralAuxPrice
	if dnType != "" {
		r.skip(4) // deltaNeutralConId, settlingFirm, clearingAccount, clearingIntent
		r.skip(4) // deltaNeutralOpenClose, shortSale, shortSaleSlot, designatedLocation
	}
	r.skip(2)                    // continuousUpdate, referencePriceType
	r.skip(2)                    // trailStopPrice, trailingPercent
	r.skip(2)                    // basisPoints, basisPointsType
	r.skip(1)                    // comboLegsDescrip
	r.skip(8 * count())          // combo legs
	r.skip(count())              // order combo leg prices
	r.skip(2 * count())          // smartComboRoutingParams
	r.skip(2)                    // scaleInitLevelSize, scaleSubsLevelSize
	if inc := r.f64(); inc > 0 { // scalePriceIncrement (unset -> 0)
		r.skip(7)
	}
	if hedge := r.s(); hedge != "" {
		r.skip(1) // hedgeParam
	}
	r.skip(1)  // optOutSmartRouting
	r.skip(2)  // clearingAccount, clearingIntent
	r.skip(1)  // notHeld
	if r.b() { // deltaNeutralContract present: conId, delta, price
		r.skip(3)
	}
	if algo := r.s(); algo != "" {
		r.skip(2 * count())
	}
	r.skip(1) // solicited
	st.WhatIf = r.b()
	st.Status = r.s()
	if sv >= svWhatIfExtFields {
		st.InitMarginBefore = r.s()
		st.MaintMarginBefore = r.s()
		st.EquityWithLoanBefore = r.s()
		st.InitMarginChange = r.s()
		st.MaintMarginChange = r.s()
		st.EquityWithLoanChange = r.s()
	}
	st.InitMarginAfter = r.s()
	st.MaintMarginAfter = r.s()
	st.EquityWithLoanAfter = r.s()
	st.Commission = r.f64()
	st.MinCommission = r.f64()
	st.MaxCommission = r.f64()
	st.CommissionCurrency = r.s()
	st.WarningText = r.s()
	if r.bad {
		return &OrderState{}
	}
	st.Parsed = true
	return st
}
