package orderticket

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/ibwire"
)

// wire turns a field list into the exact framed bytes.
func wire(fields ...string) []byte {
	payload := []byte(strings.Join(fields, "\x00") + "\x00")
	h := make([]byte, 4)
	binary.BigEndian.PutUint32(h, uint32(len(payload)))
	return append(h, payload...)
}

func encBytes(f []string) []byte {
	a := make([]any, len(f))
	for i, x := range f {
		a[i] = x
	}
	return ibwire.Encode(a...)
}

func eqFields(t *testing.T, name string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		for i := 0; i < len(got) || i < len(want); i++ {
			var g, w string
			if i < len(got) {
				g = got[i]
			}
			if i < len(want) {
				w = want[i]
			}
			if g != w || i >= len(got) || i >= len(want) {
				t.Fatalf("%s: field %d = %q, want %q (len got %d want %d)", name, i, g, w, len(got), len(want))
			}
		}
	}
	if string(encBytes(got)) != string(wire(want...)) {
		t.Fatalf("%s: framed bytes differ", name)
	}
}

func TestEncodeSimpleRequestsGolden(t *testing.T) {
	// startApi as Connect writes it: VERSION 2, clientId, optionalCapabilities.
	if got, want := ibwire.Encode(outStartAPI, 2, int64(80), ""), wire("71", "2", "80", ""); string(got) != string(want) {
		t.Fatalf("startApi % x", got)
	}
	if got := ibwire.Encode(outStartAPI, 2, int64(80), ""); string(got) != "\x00\x00\x00\x0971\x002\x0080\x00\x00" {
		t.Fatalf("startApi raw % x", got)
	}
	eqFields(t, "reqIds", encReqIDs(), []string{"8", "1", "1"})
	eqFields(t, "reqManagedAccts", encReqManagedAccts(), []string{"17", "1"})
	eqFields(t, "reqCurrentTime", encReqCurrentTime(), []string{"49", "1"})
	eqFields(t, "reqOpenOrders", encReqOpenOrders(), []string{"5", "1"})
	eqFields(t, "subscribeToGroupEvents", encSubscribeGroup(8001, 1), []string{"68", "1", "8001", "1"})
	eqFields(t, "unsubscribeFromGroupEvents", encUnsubscribeGroup(8001), []string{"70", "1", "8001"})
	eqFields(t, "cancelOrder@176", encCancelOrder(176, 100), []string{"4", "1", "100", ""})
	eqFields(t, "cancelOrder@168", encCancelOrder(168, 100), []string{"4", "1", "100"})
	eqFields(t, "cancelOrder@192", encCancelOrder(192, 100), []string{"4", "100", ""})
	eqFields(t, "reqContractDetails@176", encReqContractDetails(176, 8100, &Contract{ConID: 8314}),
		[]string{"9", "8", "8100", "8314", "", "", "", "", "", "", "", "", "", "", "", "0", "", "", ""})
	eqFields(t, "reqContractDetails@175", encReqContractDetails(175, 8100, &Contract{ConID: 8314}),
		[]string{"9", "8", "8100", "8314", "", "", "", "", "", "", "", "", "", "", "", "0", "", ""})
}

// placeOrderWant is the field list EClient.placeOrder (client 176) produces at
// server version 176 for the ticket's parent order, written out by hand
// (unset-value spellings follow ibapi v0.10.37; see twsclient_ibapi_test.go).
func placeOrderWant(id, parent string, transmit, action, qty, lmt, tif, ref string) []string {
	return []string{
		"3", id, // PLACE_ORDER, no VERSION (>= ORDER_CONTAINER 145)
		"0", "SGRX", "STK", "", "", "", "", "SMART", "", "USD", "", "", "", "", // conId..secId
		action, qty, "LMT", lmt, "", // action totalQuantity orderType lmtPrice auxPrice
		tif, "", "U1234567", "", "0", ref, transmit, parent, // tif ocaGroup account openClose origin orderRef transmit parentId
		"0", "0", "0", "0", "1", "0", // blockOrder sweepToFill displaySize triggerMethod outsideRth hidden
		"", "0", "", "", // sharesAllocation discretionaryAmt goodAfterTime goodTillDate
		"", "", "", "", // faGroup faMethod faPercentage faProfile(<177)
		"",            // modelCode
		"0", "", "-1", // shortSaleSlot designatedLocation exemptCode
		"0", "", "", "0", "", "", // ocaType rule80A settlingFirm allOrNone minQty percentOffset
		"0", "0", "", // eTradeOnly firmQuoteOnly nbboPriceCap
		"0", "", "", "", "", "", "0", // auctionStrategy startingPrice stockRefPrice delta rangeLower rangeUpper overridePct
		"", "", "", "", // volatility volatilityType deltaNeutralOrderType deltaNeutralAuxPrice
		"0", "", "", "", // continuousUpdate referencePriceType trailStopPrice trailingPercent
		"", "", "", "", "", "", // scaleInit scaleSubs scalePriceIncrement scaleTable activeStart activeStop
		"", "0", "", "", "0", // hedgeType optOutSmartRouting clearingAccount clearingIntent notHeld
		"0", "", "", "0", "", "0", "0", "0", // deltaNeutralContract algoStrategy algoId whatIf miscOptions solicited randomizeSize randomizePrice
		"0", "", unsetDbl, unsetDbl, unsetDbl, unsetDbl, unsetDbl, "0", // conditions adjustedOrderType triggerPrice lmtPriceOffset adjStop adjStopLimit adjTrailAmt adjTrailUnit
		"", "", "", "", // extOperator softDollar name value cashQty
		"", "", "", "", // mifid2 decisionMaker decisionAlgo executionTrader executionAlgo
		"0", "0", "0", "", unsetInt, unsetInt, "0", // dontUseAutoPriceForHedge isOmsContainer discUpToLimit usePriceMgmtAlgo duration postToAts autoCancelParent
		"", "", // advancedErrorOverride(>=166) manualOrderTime(>=169)
	}
}

func ticketOrders() (*Contract, *Order, *Order) {
	o := &ord{ID: 100, ChildID: 101, Sym: "SGRX", Side: "BUY", Qty: 666, Limit: 1.53, TP: 1.69}
	tk := &Ticket{account: "U1234567"}
	return contractFor("SGRX"), tk.entryOrder(o, false), tk.tpOrder(o)
}

func TestEncodePlaceOrderGolden(t *testing.T) {
	ct, par, ch := ticketOrders()
	eqFields(t, "parent@176", encPlaceOrder(176, 100, ct, par),
		placeOrderWant("100", "0", "0", "BUY", "666", "1.53", "DAY", "oticket:SGRX"))
	eqFields(t, "child@176", encPlaceOrder(176, 101, ct, ch),
		placeOrderWant("101", "100", "1", "SELL", "666", "1.69", "GTC", "oticket:SGRX:tp"))

	// gates: <166 drops both trailing fields; <145 adds VERSION after the id.
	// Every gate is also byte-checked against ibapi in twsclient_ibapi_test.go.
	w := placeOrderWant("100", "0", "0", "BUY", "666", "1.53", "DAY", "oticket:SGRX")
	eqFields(t, "parent@165", encPlaceOrder(165, 100, ct, par), w[:len(w)-2])
	eqFields(t, "parent@168", encPlaceOrder(168, 100, ct, par), w[:len(w)-1])
	// <145 also drops isOmsContainer..autoCancelParent (gates 145..162).
	old := append([]string{"3", "45"}, w[1:len(w)-8]...)
	eqFields(t, "parent@144", encPlaceOrder(144, 100, ct, par), old)
	if got := encPlaceOrder(177, 100, ct, par); len(got) != len(w)-1 {
		t.Fatalf("faProfile not dropped at 177: %d fields", len(got))
	}
}

// ─── decoding ────────────────────────────────────────────────────────────

type rec struct {
	mu      sync.Mutex
	calls   []string
	next    int64
	accts   []string
	errs    [][]any
	stat    []any
	open    []any
	cd      []any
	grp     []any
	ends    int
	t       int64
	cdEnd   int64
	close   int
	ready   chan struct{}
	acct    [][]string
	acctEnd int64
	ticks   [][]any
}

func (r *rec) add(s string) { r.mu.Lock(); r.calls = append(r.calls, s); r.mu.Unlock() }
func (r *rec) NextValidID(id int64) {
	r.mu.Lock()
	r.next = id
	r.mu.Unlock()
	r.add("next")
}
func (r *rec) ManagedAccounts(a []string) {
	r.mu.Lock()
	r.accts = a
	r.mu.Unlock()
	r.add("accts")
}
func (r *rec) CurrentTime(t int64) { r.t = t; r.add("time") }
func (r *rec) ConnectionClosed()   { r.mu.Lock(); r.close++; r.mu.Unlock(); r.add("closed") }
func (r *rec) Error(reqID, errTime, code int64, msg, adv string) {
	r.errs = append(r.errs, []any{reqID, errTime, code, msg, adv})
}
func (r *rec) DisplayGroupUpdated(reqID int64, info string) { r.grp = []any{reqID, info} }
func (r *rec) ContractDetails(reqID int64, cd *ContractDetails) {
	r.cd = []any{reqID, cd.Contract}
}
func (r *rec) ContractDetailsEnd(reqID int64) { r.cdEnd = reqID }
func (r *rec) OpenOrder(id int64, c *Contract, o *Order, st *OrderState) {
	r.open = []any{id, *c, *o, *st}
}
func (r *rec) OpenOrderEnd() { r.ends++ }
func (r *rec) AccountSummary(reqID int64, account, tag, value, currency string) {
	r.mu.Lock()
	r.acct = append(r.acct, []string{strconv.FormatInt(reqID, 10), account, tag, value, currency})
	r.mu.Unlock()
}
func (r *rec) AccountSummaryEnd(reqID int64) { r.acctEnd = reqID }
func (r *rec) TickPrice(reqID, tickType int64, price float64) {
	r.ticks = append(r.ticks, []any{"price", reqID, tickType, price})
}
func (r *rec) TickSize(reqID, tickType int64, size float64) {
	r.ticks = append(r.ticks, []any{"size", reqID, tickType, size})
}
func (r *rec) TickGeneric(reqID, tickType int64, value float64) {
	r.ticks = append(r.ticks, []any{"generic", reqID, tickType, value})
}
func (r *rec) TickString(reqID, tickType int64, value string) {
	r.ticks = append(r.ticks, []any{"string", reqID, tickType, value})
}
func (r *rec) TickReqParams(reqID int64, minTick float64, bbo string, perm int64) {
	r.ticks = append(r.ticks, []any{"params", reqID, minTick, bbo, perm})
}
func (r *rec) OrderStatus(id int64, status string, filled, remaining Qty, avg float64,
	permID, parentID int64, lastFill float64, clientID int64, whyHeld string, mktCap float64) {
	r.stat = []any{id, status, filled, remaining, avg, permID, parentID, lastFill, clientID, whyHeld, mktCap}
}

func TestDecodeSimpleMessages(t *testing.T) {
	r := &rec{}
	dispatch(r, 176, []string{"9", "1", "100"})
	dispatch(r, 176, []string{"15", "1", "U1234567,U7654321"})
	dispatch(r, 176, []string{"49", "1", "1760000000"})
	dispatch(r, 176, []string{"52", "1", "8100"})
	dispatch(r, 176, []string{"53", "1"})
	dispatch(r, 176, []string{"68", "1", "8001", "8314@SMART"})
	if r.next != 100 || !reflect.DeepEqual(r.accts, []string{"U1234567", "U7654321"}) || r.t != 1760000000 ||
		r.cdEnd != 8100 || r.ends != 1 || !reflect.DeepEqual(r.grp, []any{int64(8001), "8314@SMART"}) {
		t.Fatalf("%+v", r)
	}
	dispatch(r, 176, []string{"9", "1"}) // truncated: no callback
	if r.next != 100 {
		t.Fatal("short nextValidId delivered")
	}
}

func TestDecodeError(t *testing.T) {
	r := &rec{}
	dispatch(r, 176, []string{"4", "2", "-1", "2104", "Market data farm connection is OK:usfarm", ""})
	dispatch(r, 176, []string{"4", "2", "100", "201", "Order rejected", `{"x":1}`})
	dispatch(r, 165, []string{"4", "2", "101", "202", "Order cancelled"})
	dispatch(r, 194, []string{"4", "2", "102", "399", "warn", "", "1760000000123"})
	want := [][]any{
		{int64(-1), int64(0), int64(2104), "Market data farm connection is OK:usfarm", ""},
		{int64(100), int64(0), int64(201), "Order rejected", `{"x":1}`},
		{int64(101), int64(0), int64(202), "Order cancelled", ""},
		{int64(102), int64(1760000000123), int64(399), "warn", ""},
	}
	if !reflect.DeepEqual(r.errs, want) {
		t.Fatalf("%v", r.errs)
	}
}

func TestDecodeOrderStatus(t *testing.T) {
	r := &rec{}
	dispatch(r, 176, []string{"3", "100", "Submitted", "200", "466", "1.53", "9100", "0", "1.53", "80", "", "0"})
	want := []any{int64(100), "Submitted", Qty(200), Qty(466), 1.53, int64(9100), int64(0), 1.53, int64(80), "", 0.0}
	if !reflect.DeepEqual(r.stat, want) {
		t.Fatalf("%v", r.stat)
	}
	r.stat = nil
	dispatch(r, 130, []string{"3", "6", "100", "Filled", "666", "0", "1.5", "9100", "0", "1.5", "80", "", "0"})
	if r.stat == nil || r.stat[1] != "Filled" || r.stat[2] != Qty(666) {
		t.Fatalf("pre-131 %v", r.stat)
	}
}

func TestDecodeContractData(t *testing.T) {
	r := &rec{}
	dispatch(r, 176, []string{"10", "8100", "BRK B", "STK", "", "0", "", "SMART", "USD", "BRK B", "NMS", "BRK B", "72063691", "0.01", "1", "more..."})
	if !reflect.DeepEqual(r.cd, []any{int64(8100), Contract{ConID: 72063691, Symbol: "BRK B", SecType: "STK", Exchange: "SMART", Currency: "USD"}}) {
		t.Fatalf("%v", r.cd)
	}
	r.cd = nil
	dispatch(r, 163, []string{"10", "8", "8101", "SGRX", "STK", "", "0", "", "SMART", "USD", "SGRX", "NMS", "SGRX", "8314", "0.01"})
	if r.cd == nil || r.cd[1].(Contract).ConID != 8314 {
		t.Fatalf("pre-164 %v", r.cd)
	}
}

// openOrderFields builds a server-176 openOrder: the leading block the client
// parses, then a tail of arbitrary later fields it must ignore.
func openOrderFields(sv int) []string {
	f := []string{"5", "101",
		"8314", "SGRX", "STK", "", "0", "", "", "SMART", "USD", "SGRX", "NMS",
		"SELL", "666", "LMT", "1.69", "", "GTC", "", "U1234567", "", "0", "oticket:SGRX:tp",
		"80", "9101", "1", "0", "0", "", "", "", "", ""}
	if sv < 177 {
		f = append(f, "") // faProfile
	}
	f = append(f, "", "", "", "", "", // modelCode goodTillDate rule80A percentOffset settlingFirm
		"0", "", "-1", "0", "", "", "", "", "", // shortSale x3, auction, box x3, range x2
		"0", "0", "0", "0", "", "0", "0", "0", "", // displaySize block sweep aon minQty ocaType eTrade firmQuote nbbo
		"100",                                           // parentId
		"0", "", "", "", "", "Submitted", "junk", "...") // triggerMethod ... (ignored)
	return f
}

func TestDecodeOpenOrder(t *testing.T) {
	for _, sv := range []int{176, 177} {
		r := &rec{}
		dispatch(r, sv, openOrderFields(sv))
		if r.open == nil {
			t.Fatalf("sv %d: no callback", sv)
		}
		c, o := r.open[1].(Contract), r.open[2].(Order)
		if r.open[0] != int64(101) || c.Symbol != "SGRX" || c.SecType != "STK" || c.ConID != 8314 {
			t.Fatalf("sv %d contract %+v", sv, r.open)
		}
		want := Order{Action: "SELL", OrderType: "LMT", TotalQuantity: 666, LmtPrice: 1.69, TIF: "GTC", Account: "U1234567",
			OrderRef: "oticket:SGRX:tp", ParentID: 100, OutsideRTH: true, ClientID: 80, PermID: 9101}
		if o != want {
			t.Fatalf("sv %d order %+v", sv, o)
		}
	}
	// pre-145: a VERSION field after the id
	f := openOrderFields(176)
	f = append([]string{"5", "34"}, f[1:]...)
	r := &rec{}
	dispatch(r, 144, f)
	if r.open == nil || r.open[2].(Order).ParentID != 100 {
		t.Fatalf("pre-145 %v", r.open)
	}
	// truncated before parentId: dropped, not mis-parsed
	r = &rec{}
	dispatch(r, 176, openOrderFields(176)[:40])
	if r.open != nil {
		t.Fatal("truncated openOrder delivered")
	}
}

// ─── end to end against a fake TWS (127.0.0.1:0, test only) ──────────────

func TestTWSClientEndToEnd(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	got := make(chan []string, 32)
	srvErr := make(chan string, 4)
	connc := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		connc <- conn
		br := bufio.NewReader(conn)
		greet := make([]byte, 4)
		if _, err := io.ReadFull(br, greet); err != nil || string(greet) != "API\x00" {
			srvErr <- "bad greeting"
			return
		}
		v, err := ibwire.ReadMsg(br)
		if err != nil || len(v) != 1 || v[0] != "v100..176" {
			srvErr <- "bad version block"
			return
		}
		start, err := ibwire.ReadMsg(br)
		_ = start
		// server version reply first (TWS sends it right after the version block)
		_ = err
		srvErr <- "" // placeholder to keep ordering simple
		got <- start
		for {
			f, err := ibwire.ReadMsg(br)
			if err != nil {
				return
			}
			got <- f
		}
	}()

	// The fake writes its greeting reply only after reading the version block,
	// so do it from the client side via a helper goroutine on the accepted conn.
	r := &rec{}
	c := NewTWSClient(r)
	done := make(chan error, 1)
	go func() {
		addr := ln.Addr().(*net.TCPAddr)
		done <- c.Connect("127.0.0.1", addr.Port, 80)
	}()
	conn := <-connc
	defer conn.Close()
	time.Sleep(50 * time.Millisecond)
	if _, err := conn.Write(wire("176", "20261017 10:00:00 GMT")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("connect: %v", err)
	}
	if e := <-srvErr; e != "" {
		t.Fatal(e)
	}
	next := func() []string {
		select {
		case f := <-got:
			return f
		case <-time.After(3 * time.Second):
			t.Fatal("fake TWS received nothing")
			return nil
		}
	}
	eqFields(t, "startApi", next(), []string{"71", "2", "80", ""})
	if c.ServerVersion() != 176 || !c.IsConnected() {
		t.Fatalf("sv %d", c.ServerVersion())
	}

	// A request before nextValidId must be held back.
	c.ReqManagedAccts()
	select {
	case f := <-got:
		t.Fatalf("request sent before nextValidId: %v", f)
	case <-time.After(150 * time.Millisecond):
	}

	_, _ = conn.Write(append(wire("15", "1", "U1234567"), wire("9", "1", "100")...))
	eqFields(t, "reqManagedAccts (flushed)", next(), []string{"17", "1"})
	deadline := time.Now().Add(2 * time.Second)
	for {
		r.mu.Lock()
		ok := r.next == 100 && len(r.accts) == 1 && r.accts[0] == "U1234567"
		r.mu.Unlock()
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("nextValidId / managedAccounts not delivered")
		}
		time.Sleep(10 * time.Millisecond)
	}

	ct, par, ch := ticketOrders()
	c.PlaceOrder(100, ct, par)
	c.PlaceOrder(101, ct, ch)
	p1, p2 := next(), next()
	eqFields(t, "parent on the wire", p1, placeOrderWant("100", "0", "0", "BUY", "666", "1.53", "DAY", "oticket:SGRX"))
	eqFields(t, "child on the wire", p2, placeOrderWant("101", "100", "1", "SELL", "666", "1.69", "GTC", "oticket:SGRX:tp"))
	// spot-check by name: transmit / parentId / tif / outsideRth / orderRef
	const iTIF, iRef, iTransmit, iParent, iRTH = 21, 26, 27, 28, 33
	if p1[iTransmit] != "0" || p1[iParent] != "0" || p2[iTransmit] != "1" || p2[iParent] != "100" ||
		p1[iTIF] != "DAY" || p2[iTIF] != "GTC" || p1[iRTH] != "1" || p2[iRef] != "oticket:SGRX:tp" {
		t.Fatalf("named fields: %v / %v", p1[:35], p2[:35])
	}

	c.CancelOrder(100)
	eqFields(t, "cancel", next(), []string{"4", "1", "100", ""})

	// Socket closed by TWS: ConnectionClosed, IsConnected false.
	conn.Close()
	deadline = time.Now().Add(2 * time.Second)
	for {
		r.mu.Lock()
		n := r.close
		r.mu.Unlock()
		if n == 1 && !c.IsConnected() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("EOF not reported as ConnectionClosed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.PlaceOrder(102, ct, par) // after close: dropped, no panic
}

func TestTWSClientDisconnectIsSilent(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		g := make([]byte, 4)
		_, _ = io.ReadFull(br, g)
		_, _ = ibwire.ReadMsg(br)
		_, _ = conn.Write(wire("176", "x"))
		for {
			if _, err := ibwire.ReadMsg(br); err != nil {
				return
			}
		}
	}()
	r := &rec{}
	c := NewTWSClient(r)
	if err := c.Connect("127.0.0.1", ln.Addr().(*net.TCPAddr).Port, 80); err != nil {
		t.Fatal(err)
	}
	_ = c.Disconnect()
	time.Sleep(100 * time.Millisecond)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.close != 0 || c.IsConnected() {
		t.Fatal("user Disconnect reported as ConnectionClosed")
	}
}
