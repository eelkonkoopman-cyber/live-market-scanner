package orderticket

// Byte-equality against github.com/scmhub/ibapi v0.10.37's encoder, captured
// offline: an EClient is put into the "connected" state by reflection (no
// socket, no network) and the framed request is read off its request channel.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/scmhub/ibapi"
)

func setUnexported(v reflect.Value, name string, val any) {
	f := v.FieldByName(name)
	reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().Set(reflect.ValueOf(val).Convert(f.Type()))
}

func ibCapture(t *testing.T, sv int, call func(c *ibapi.EClient)) []byte {
	t.Helper()
	c := ibapi.NewEClient(nil)
	v := reflect.ValueOf(c).Elem()
	setUnexported(v, "serverVersion", sv)
	setUnexported(v, "connState", ibapi.CONNECTED)
	conn := v.FieldByName("conn")
	conn = reflect.NewAt(conn.Type(), unsafe.Pointer(conn.UnsafeAddr())).Elem()
	setUnexported(conn.Elem(), "isConnected", true)
	ch := v.FieldByName("reqChan")
	ch = reflect.NewAt(ch.Type(), unsafe.Pointer(ch.UnsafeAddr())).Elem()
	call(c)
	got, ok := ch.TryRecv()
	if !ok {
		t.Fatalf("ibapi produced no request at sv %d", sv)
	}
	return got.Bytes()
}

func ibOrder(o *Order) *ibapi.Order {
	io := ibapi.NewOrder()
	io.Action = o.Action
	io.OrderType = o.OrderType
	io.TotalQuantity = ibapi.StringToDecimal(strconv.FormatFloat(float64(o.TotalQuantity), 'f', -1, 64))
	io.LmtPrice = o.LmtPrice
	if o.OrderType == "STP" { // a stop order has no limit price: ibapi leaves it unset
		io.LmtPrice = ibapi.UNSET_FLOAT
	}
	if o.AuxPrice > 0 {
		io.AuxPrice = o.AuxPrice
	}
	io.TIF = o.TIF
	io.OCAGroup = o.OcaGroup
	io.OCAType = o.OcaType
	io.Account = o.Account
	io.OrderRef = o.OrderRef
	io.Transmit = o.Transmit
	io.ParentID = o.ParentID
	io.OutsideRTH = o.OutsideRTH
	return io
}

func ibContract(c *Contract) *ibapi.Contract {
	ic := ibapi.NewContract()
	ic.ConID, ic.Symbol, ic.SecType, ic.Exchange, ic.Currency = c.ConID, c.Symbol, c.SecType, c.Exchange, c.Currency
	return ic
}

func assertSame(t *testing.T, name string, mine []string, ib []byte) {
	t.Helper()
	if got := encBytes(mine); !bytes.Equal(got, ib) {
		ibf := bytes.Split(bytes.TrimSuffix(ib[4:], []byte{0}), []byte{0})
		for i := 0; i < len(mine) || i < len(ibf); i++ {
			var a, b string
			if i < len(mine) {
				a = mine[i]
			}
			if i < len(ibf) {
				b = string(ibf[i])
			}
			if a != b || i >= len(mine) || i >= len(ibf) {
				if len(b) > 24 {
					b = b[:24] + "..."
				}
				t.Errorf("%s: field %d mine %q ibapi %q (len %d vs %d)", name, i, a, b, len(mine), len(ibf))
			}
		}
		t.Fatalf("%s: bytes differ", name)
	}
}

func TestPlaceOrderMatchesIbapi(t *testing.T) {
	ct, par, ch := ticketOrders()
	for _, sv := range []int{100, 101, 102, 103, 105, 106, 111, 138, 139, 141, 144, 145, 148, 151, 158, 160, 162, 165, 166, 169, 175, 176} {
		whole := *par
		whole.LmtPrice, whole.TotalQuantity = 2, 100
		for i, o := range []*Order{par, ch, &whole} {
			id := int64(100 + i)
			ib := ibCapture(t, sv, func(c *ibapi.EClient) { c.PlaceOrder(id, ibContract(ct), ibOrder(o)) })
			assertSame(t, "placeOrder@"+strconv.Itoa(sv)+"#"+strconv.Itoa(i), encPlaceOrder(sv, id, ct, o), ib)
		}
	}
}

// The VWAP X bracket: the STP child (auxPrice = trigger, no limit, OCA group +
// type 1, parent id, last child so Transmit=true) and the OCA'd TP child must be
// byte-identical to ibapi's at every server version the client can negotiate.
func TestVwapxBracketMatchesIbapi(t *testing.T) {
	ct := contractFor("MGM")
	tk := &Ticket{account: "U1234567"}
	o := &ord{ID: 100, ChildID: 101, SLID: 102, Kind: kindVwapx, Sym: "MGM", Side: "BUY", Qty: 120, Limit: 27.99, TP: 28.27, SLPx: 27.80}
	orders := []*Order{tk.entryOrder(o, false), tk.vwapxTP(o, false), tk.slOrder(o, true)}
	if sl := orders[2]; sl.OrderType != "STP" || sl.AuxPrice != 27.80 || sl.OcaGroup != "vwapx-100" || sl.OcaType != 1 || sl.ParentID != 100 {
		t.Fatalf("stop child %+v", sl)
	}
	for _, sv := range []int{100, 141, 145, 158, 165, 166, 169, 175, 176} {
		for i, x := range orders {
			id := int64(100 + i)
			ib := ibCapture(t, sv, func(c *ibapi.EClient) { c.PlaceOrder(id, ibContract(ct), ibOrder(x)) })
			assertSame(t, "vwapx@"+strconv.Itoa(sv)+"#"+strconv.Itoa(i), encPlaceOrder(sv, id, ct, x), ib)
		}
	}
}

func TestOtherRequestsMatchIbapi(t *testing.T) {
	for _, sv := range []int{100, 168, 169, 175, 176} {
		s := "@" + strconv.Itoa(sv)
		assertSame(t, "reqIds"+s, encReqIDs(), ibCapture(t, sv, func(c *ibapi.EClient) { c.ReqIDs(1) }))
		assertSame(t, "reqManagedAccts"+s, encReqManagedAccts(), ibCapture(t, sv, func(c *ibapi.EClient) { c.ReqManagedAccts() }))
		assertSame(t, "reqCurrentTime"+s, encReqCurrentTime(), ibCapture(t, sv, func(c *ibapi.EClient) { c.ReqCurrentTime() }))
		assertSame(t, "reqOpenOrders"+s, encReqOpenOrders(), ibCapture(t, sv, func(c *ibapi.EClient) { c.ReqOpenOrders() }))
		assertSame(t, "subscribe"+s, encSubscribeGroup(8001, 1), ibCapture(t, sv, func(c *ibapi.EClient) { c.SubscribeToGroupEvents(8001, 1) }))
		assertSame(t, "unsubscribe"+s, encUnsubscribeGroup(8001), ibCapture(t, sv, func(c *ibapi.EClient) { c.UnsubscribeFromGroupEvents(8001) }))
		assertSame(t, "cancelOrder"+s, encCancelOrder(sv, 100), ibCapture(t, sv, func(c *ibapi.EClient) { c.CancelOrder(100, ibapi.NewOrderCancel()) }))
		ct := &Contract{ConID: 8314, Symbol: "SGRX", SecType: "STK", Exchange: "SMART", Currency: "USD"}
		assertSame(t, "reqContractDetails"+s, encReqContractDetails(sv, 8100, ct),
			ibCapture(t, sv, func(c *ibapi.EClient) { c.ReqContractDetails(8100, ibContract(ct)) }))
	}
}

// ─── margin check: what-if placeOrder, account summary ───────────────────

func TestWhatIfPlaceOrderMatchesIbapi(t *testing.T) {
	ct, par, _ := ticketOrders()
	for _, sv := range []int{100, 141, 142, 145, 162, 166, 169, 175, 176} {
		w := *par
		w.Transmit, w.WhatIf, w.ParentID, w.OrderRef = true, true, 0, whatIfRefPfx+"SGRX"
		ib := ibCapture(t, sv, func(c *ibapi.EClient) {
			io := ibOrder(&w)
			io.WhatIf = true
			c.PlaceOrder(99, ibContract(ct), io)
		})
		assertSame(t, "whatIf@"+strconv.Itoa(sv), encPlaceOrder(sv, 99, ct, &w), ib)
	}
}

func TestAccountSummaryRequestsMatchIbapi(t *testing.T) {
	for _, sv := range []int{100, 176} {
		s := "@" + strconv.Itoa(sv)
		assertSame(t, "reqAccountSummary"+s, encReqAccountSummary(acctReqID, "All", acctTags),
			ibCapture(t, sv, func(c *ibapi.EClient) { c.ReqAccountSummary(acctReqID, "All", acctTags) }))
		assertSame(t, "cancelAccountSummary"+s, encCancelAccountSummary(acctReqID),
			ibCapture(t, sv, func(c *ibapi.EClient) { c.CancelAccountSummary(acctReqID) }))
	}
}

// ibRec captures ibapi's decoded callbacks.
type ibRec struct {
	ibapi.Wrapper
	open chan []any
	acct chan []any
	tick chan []any
}

func (w *ibRec) TickPrice(id ibapi.TickerID, tt ibapi.TickType, px float64, _ ibapi.TickAttrib) {
	w.tick <- []any{"price", id, tt, px}
}
func (w *ibRec) TickSize(id ibapi.TickerID, tt ibapi.TickType, sz ibapi.Decimal) {
	w.tick <- []any{"size", id, tt, sz.Float()}
}
func (w *ibRec) TickGeneric(id ibapi.TickerID, tt ibapi.TickType, v float64) {
	w.tick <- []any{"generic", id, tt, v}
}
func (w *ibRec) TickString(id ibapi.TickerID, tt ibapi.TickType, v string) {
	w.tick <- []any{"string", id, tt, v}
}
func (w *ibRec) TickReqParams(id ibapi.TickerID, mt float64, bbo string, perm int64) {
	w.tick <- []any{"params", id, mt, bbo, perm}
}

func (w *ibRec) OpenOrder(id ibapi.OrderID, c *ibapi.Contract, o *ibapi.Order, st *ibapi.OrderState) {
	w.open <- []any{id, c, o, st}
}
func (w *ibRec) AccountSummary(reqID int64, account, tag, value, currency string) {
	w.acct <- []any{reqID, account, tag, value, currency}
}
func (w *ibRec) AccountSummaryEnd(reqID int64) { w.acct <- []any{reqID} }

func split4(data []byte, atEOF bool) (int, []byte, error) {
	if len(data) < 4 {
		if atEOF {
			return 0, nil, io.EOF
		}
		return 0, nil, nil
	}
	n := int(binary.BigEndian.Uint32(data[:4])) + 4
	if n > len(data) {
		return 0, nil, nil
	}
	return n, data[4:n], nil
}

// ibDecode feeds one framed message to ibapi v0.10.37's own EDecoder (via its
// exported EReader; no socket) at server version sv.
func ibDecode(t *testing.T, sv int, f []string) *ibRec {
	t.Helper()
	w := &ibRec{open: make(chan []any, 1), acct: make(chan []any, 1), tick: make(chan []any, 4)}
	d := &ibapi.EDecoder{}
	v := reflect.ValueOf(d).Elem()
	setUnexported(v, "wrapper", ibapi.EWrapper(w))
	setUnexported(v, "serverVersion", sv)
	sc := bufio.NewScanner(bytes.NewReader(encBytes(f)))
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	sc.Split(split4)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var wg sync.WaitGroup
	ibapi.EReader(ctx, sc, d, &wg)
	return w
}

func recv(t *testing.T, ch chan []any) []any {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("ibapi decoded nothing")
		return nil
	}
}

func unset0(v float64) float64 {
	if v == ibapi.UNSET_FLOAT {
		return 0
	}
	return v
}

// whatIfTail is openOrder from triggerMethod on; rich=true exercises every
// variable-length branch (delta-neutral, combo legs, smart routing params,
// scale, hedge, delta-neutral contract, algo params).
func whatIfTail(rich bool) []string {
	f := []string{"0", "", "0"} // triggerMethod volatility volatilityType
	if rich {
		f = append(f, "LMT", "", "5", "", "", "", "", "0", "0", "")
	} else {
		f = append(f, "", "")
	}
	f = append(f, "0", "0", "", "", "", "", "") // continuousUpdate refPriceType trail x2 basis x2 comboDescrip
	if rich {
		f = append(f, "1", "1", "1", "BUY", "SMART", "0", "0", "", "-1", "1", "1.5", "1", "tag", "val")
	} else {
		f = append(f, "0", "0", "0")
	}
	f = append(f, "", "") // scaleInit scaleSubs
	if rich {
		f = append(f, "0.05", "0.01", "1", "0.1", "0", "1", "1", "0", "D", "0.5")
	} else {
		f = append(f, "", "")
	}
	f = append(f, "0", "", "", "0") // optOut clearing x2 notHeld
	if rich {
		f = append(f, "1", "7", "0.5", "10", "Adaptive", "1", "adaptivePriority", "Normal")
	} else {
		f = append(f, "0", "")
	}
	f = append(f, "0", // solicited
		"1", "PreSubmitted",
		"12000.5", "10000", "50000", "1250.25", "1000.2", "-1.5",
		"13250.75", "11000.2", "49998.5",
		"1.0", "1.0", "1.7976931348623157E308", "USD",
		"Order size exceeds the limit")
	for i := 0; i < 40; i++ { // fields ibapi reads after warningText (randomize..imbalance)
		f = append(f, "0")
	}
	return f
}

func TestWhatIfOpenOrderDecodeMatchesIbapi(t *testing.T) {
	for _, sv := range []int{145, 160, 166, 170, 176} {
		for _, rich := range []bool{false, true} {
			head := openOrderFields(sv)
			head = head[:len(head)-8] // through parentId
			f := append(append([]string(nil), head...), whatIfTail(rich)...)
			name := "sv" + strconv.Itoa(sv) + map[bool]string{false: "/plain", true: "/rich"}[rich]

			r := &rec{}
			dispatch(r, sv, f)
			if r.open == nil {
				t.Fatalf("%s: mine decoded nothing", name)
			}
			mine := r.open[3].(OrderState)
			got := recv(t, ibDecode(t, sv, f).open)
			io, ist := got[2].(*ibapi.Order), got[3].(*ibapi.OrderState)
			if !mine.Parsed {
				t.Fatalf("%s: tail not parsed", name)
			}
			if got[0].(int64) != r.open[0].(int64) || io.ParentID != r.open[2].(Order).ParentID {
				t.Fatalf("%s: head differs", name)
			}
			pairs := [][2]any{
				{mine.WhatIf, io.WhatIf}, {mine.Status, ist.Status},
				{mine.InitMarginBefore, ist.InitMarginBefore}, {mine.MaintMarginBefore, ist.MaintMarginBefore},
				{mine.EquityWithLoanBefore, ist.EquityWithLoanBefore}, {mine.InitMarginChange, ist.InitMarginChange},
				{mine.MaintMarginChange, ist.MaintMarginChange}, {mine.EquityWithLoanChange, ist.EquityWithLoanChange},
				{mine.InitMarginAfter, ist.InitMarginAfter}, {mine.MaintMarginAfter, ist.MaintMarginAfter},
				{mine.EquityWithLoanAfter, ist.EquityWithLoanAfter},
				{mine.Commission, unset0(ist.CommissionAndFees)}, {mine.MinCommission, unset0(ist.MinCommissionAndFees)},
				{mine.MaxCommission, unset0(ist.MaxCommissionAndFees)}, {mine.CommissionCurrency, ist.CommissionAndFeesCurrency},
				{mine.WarningText, ist.WarningText},
			}
			for i, p := range pairs {
				if p[0] != p[1] {
					t.Errorf("%s: field %d mine %v ibapi %v", name, i, p[0], p[1])
				}
			}
			if ist.WarningText != "Order size exceeds the limit" || ist.InitMarginChange != "1250.25" || !io.WhatIf {
				t.Fatalf("%s: ibapi itself misread the fixture: %+v", name, ist)
			}
			// one field short of warningText: head still delivered, state unparsed
			r = &rec{}
			dispatch(r, sv, f[:len(f)-41])
			if r.open == nil || r.open[3].(OrderState).Parsed {
				t.Fatalf("%s: truncated tail %v", name, r.open)
			}
		}
	}
}

func TestAccountSummaryDecodeMatchesIbapi(t *testing.T) {
	f := []string{"63", "1", "8002", "U1234567", "AvailableFunds", "25000.50", "USD"}
	r := &rec{}
	dispatch(r, 176, f)
	got := recv(t, ibDecode(t, 176, f).acct)
	if len(r.acct) != 1 {
		t.Fatal("mine decoded nothing")
	}
	want := []string{strconv.FormatInt(got[0].(int64), 10), got[1].(string), got[2].(string), got[3].(string), got[4].(string)}
	if !reflect.DeepEqual(r.acct[0], want) {
		t.Fatalf("mine %v ibapi %v", r.acct[0], want)
	}
	e := []string{"64", "1", "8002"}
	dispatch(r, 176, e)
	if g := recv(t, ibDecode(t, 176, e).acct); g[0].(int64) != r.acctEnd || r.acctEnd != 8002 {
		t.Fatalf("end mine %d ibapi %v", r.acctEnd, g)
	}
}

// ─── market data (shortable shares) ─────────────────────────────────────

func TestMktDataRequestsMatchIbapi(t *testing.T) {
	for _, sv := range []int{100, 114, 150, 176} {
		for _, ct := range []*Contract{contractFor("SGRX"), {ConID: 8314, Symbol: "BRK B", SecType: "STK", Exchange: "SMART", Currency: "USD"}} {
			s := "@" + strconv.Itoa(sv) + " " + ct.Symbol
			assertSame(t, "reqMktData"+s, encReqMktData(sv, mdReqBase+1, ct, "236"),
				ibCapture(t, sv, func(c *ibapi.EClient) { c.ReqMktData(mdReqBase+1, ibContract(ct), "236", false, false, nil) }))
		}
		// the soft stop's price line: no generic ticks (stopsrc.go)
		sct := contractFor("ONCO")
		assertSame(t, "reqMktData(stop)@"+strconv.Itoa(sv), encReqMktData(sv, stopMDBase+1, sct, ""),
			ibCapture(t, sv, func(c *ibapi.EClient) { c.ReqMktData(stopMDBase+1, ibContract(sct), "", false, false, nil) }))
		assertSame(t, "cancelMktData(stop)@"+strconv.Itoa(sv), encCancelMktData(stopMDBase+1),
			ibCapture(t, sv, func(c *ibapi.EClient) { c.CancelMktData(stopMDBase + 1) }))
		assertSame(t, "cancelMktData@"+strconv.Itoa(sv), encCancelMktData(mdReqBase+1),
			ibCapture(t, sv, func(c *ibapi.EClient) { c.CancelMktData(mdReqBase + 1) }))
	}
}

func TestTickDecodeMatchesIbapi(t *testing.T) {
	id := strconv.Itoa(mdReqBase + 7)
	cases := [][]string{
		{"2", "6", id, "89", "152300"},           // tickSize: shortable shares
		{"45", "6", id, "46", "3"},               // tickGeneric: shortable indicator
		{"46", "6", id, "45", "1793041200"},      // tickString (last timestamp)
		{"1", "6", id, "4", "37.91", "100", "0"}, // tickPrice LAST (+ ibapi's LAST_SIZE)
		{"81", id, "0.01", "9c0001", "3"},        // tickReqParams (no version)
	}
	for _, f := range cases {
		r := &rec{}
		dispatch(r, 176, f)
		if len(r.ticks) != 1 {
			t.Fatalf("%v: mine %v", f, r.ticks)
		}
		got := recv(t, ibDecode(t, 176, f).tick)
		if fmt.Sprint(got) != fmt.Sprint(r.ticks[0]) {
			t.Fatalf("%v: mine %v ibapi %v", f, r.ticks[0], got)
		}
	}
}
