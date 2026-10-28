package orderticket

import (
	"bufio"
	"bytes"
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/scmhub/ibapi"
)

// ─── PRSO 2026-10-26 regression (bracket fill at server version 176) ──────

// prsoBracket replays the TWS sequence of a bracket whose entry fills:
// openOrder echoes (PreSubmitted), entry Filled, TP activated (Submitted).
func prsoBracket(t *testing.T) (r *rig, aud *MemAudit, pid, cid, q int64) {
	t.Helper()
	aud = &MemAudit{}
	r = newRig(t, aud)
	r.ready("SGRX", "111", 1.50)
	pid, cid, q = placeOrder(t, r, "BUY")
	tp := view(t, r, pid).TP
	ent := func(st string) {
		r.f.w.OpenOrder(pid, contractFor("SGRX"), &Order{Action: "BUY", OrderType: "LMT", TotalQuantity: Qty(q), LmtPrice: 1.50,
			TIF: "DAY", OrderRef: refPrefix + "SGRX", ClientID: 80, PermID: 9100}, &OrderState{Status: st, Parsed: true})
	}
	child := func(st string) {
		r.f.w.OpenOrder(cid, contractFor("SGRX"), &Order{Action: "SELL", OrderType: "LMT", TotalQuantity: Qty(q), LmtPrice: tp,
			TIF: "GTC", OrderRef: refPrefix + "SGRX:tp", ClientID: 80, PermID: 9101, ParentID: pid}, &OrderState{Status: st, Parsed: true})
	}
	child("PreSubmitted")
	ent("PreSubmitted")
	r.f.w.OrderStatus(cid, "PreSubmitted", 0, Qty(q), 0, 9101, pid, 0, 80, "", 0)
	r.f.w.OrderStatus(pid, "Submitted", 0, Qty(q), 0, 9100, 0, 0, 80, "", 0)
	// the fill
	r.f.w.OrderStatus(pid, "Filled", Qty(q), 0, 1.50, 9100, 0, 1.50, 80, "", 0)
	ent("Filled")
	child("Submitted")
	r.f.w.OrderStatus(cid, "Submitted", 0, Qty(q), 0, 9101, pid, 0, 80, "", 0)
	if v := view(t, r, pid); v.StopState != stArmed {
		t.Fatalf("not armed %+v", v)
	}
	return r, aud, pid, cid, q
}

func lastStop(aud *MemAudit) auditRec {
	var out auditRec
	for _, x := range memRecs(aud) {
		if x.Ev == "stop" {
			out = x
		}
	}
	return out
}

// A "Filled" for the TP id at a price a SELL LMT at the TP could never give
// (the PRSO case: TP +10% above, market below) keeps the stop armed + alert,
// and the stop still fires and exits when the TP turns out to be alive.
func TestPRSOImplausibleTPFillKeepsStopArmed(t *testing.T) {
	r, aud, pid, cid, q := prsoBracket(t)
	r.f.w.OrderStatus(cid, "Filled", Qty(q), 0, 1.47, 9101, pid, 1.47, 80, "", 0)
	r.tk.Tick(context.Background())
	v := view(t, r, pid)
	if v.StopState != stArmed {
		t.Fatalf("disarmed on an impossible TP fill: %+v", v)
	}
	s := r.tk.State()
	if !strings.Contains(s.StopAlert, "NOT confirmed") || !strings.Contains(s.StopAlert, "below the TP limit") {
		t.Fatalf("no red alert: %q", s.StopAlert)
	}
	if n := lastStop(aud).Note; !strings.Contains(n, "orderStatus id=") || !strings.Contains(n, "status=Filled") {
		t.Fatalf("audit lacks the message: %q", n)
	}
	// price falls through the stop (1.48): stop fires, re-cancels the TP
	price(r, 1.40)
	price(r, 1.40)
	if c := cancels(r); len(c) == 0 || c[len(c)-1] != cid {
		t.Fatalf("TP not cancelled: %v", c)
	}
	if len(exits(r)) != 0 {
		t.Fatal("sold before the TP was confirmed dead")
	}
	r.f.w.OrderStatus(cid, "Cancelled", 0, Qty(q), 0, 9101, pid, 0, 80, "", 0)
	if ex := exits(r); len(ex) != 1 || ex[0].o.TotalQuantity.Int() != q {
		t.Fatalf("exit %+v", ex)
	}
}

// The TP's openOrder alone saying Filled never disarms.
func TestTPOpenOrderFilledAloneDoesNotDisarm(t *testing.T) {
	r, _, pid, cid, q := prsoBracket(t)
	tp := view(t, r, pid).TP
	r.f.w.OpenOrder(cid, contractFor("SGRX"), &Order{Action: "SELL", OrderType: "LMT", TotalQuantity: Qty(q), LmtPrice: tp,
		TIF: "GTC", OrderRef: refPrefix + "SGRX:tp", ClientID: 80, PermID: 9101, ParentID: pid}, &OrderState{Status: "Filled", Parsed: true})
	r.tk.Tick(context.Background())
	if v := view(t, r, pid); v.StopState != stArmed || v.ChildStatus == "Filled" {
		t.Fatalf("%+v", v)
	}
}

// orderStatus not provably for our TP (other client, other perm, other
// parent, a what-if id) is ignored and audited.
func TestForeignOrderStatusIgnored(t *testing.T) {
	for name, send := range map[string]func(w Wrapper, pid, cid, q int64){
		"client": func(w Wrapper, pid, cid, q int64) {
			w.OrderStatus(cid, "Filled", Qty(q), 0, 1.65, 9101, pid, 1.65, 81, "", 0)
		},
		"perm": func(w Wrapper, pid, cid, q int64) {
			w.OrderStatus(cid, "Filled", Qty(q), 0, 1.65, 7777, pid, 1.65, 80, "", 0)
		},
		"parent": func(w Wrapper, pid, cid, q int64) {
			w.OrderStatus(cid, "Filled", Qty(q), 0, 1.65, 9101, pid+50, 1.65, 80, "", 0)
		},
	} {
		r, aud, pid, cid, q := prsoBracket(t)
		send(r.f.w, pid, cid, q)
		r.tk.Tick(context.Background())
		v := view(t, r, pid)
		if v.StopState != stArmed || v.ChildFilled != 0 || v.ChildStatus != "Submitted" {
			t.Fatalf("%s: applied %+v", name, v)
		}
		found := false
		for _, x := range memRecs(aud) {
			if x.Ev == "status" && x.ID == cid && strings.Contains(x.Note, "IGNORED") {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: not audited", name)
		}
	}
}

// Qty / accounting mismatches keep the stop armed.
func TestTPFillInconsistentQtyKeepsStop(t *testing.T) {
	r, _, pid, cid, q := prsoBracket(t)
	tp := view(t, r, pid).TP
	r.f.w.OrderStatus(cid, "Filled", Qty(q+100), 0, tp, 9101, pid, tp, 80, "", 0)
	r.tk.Tick(context.Background())
	if v := view(t, r, pid); v.StopState != stArmed || r.tk.State().StopAlert == "" {
		t.Fatalf("%+v", v)
	}
	// a genuine full fill afterwards clears it and disarms
	r.f.w.OrderStatus(cid, "Filled", Qty(q), 0, tp+0.01, 9101, pid, tp+0.01, 80, "", 0)
	if v := view(t, r, pid); v.StopState != stDisarmed || r.tk.State().StopAlert != "" {
		t.Fatalf("%+v alert %q", v, r.tk.State().StopAlert)
	}
}

// A real TP fill disarms and the audit names the message that did it.
func TestTPFillConfirmedDisarmAudited(t *testing.T) {
	r, aud, pid, cid, q := prsoBracket(t)
	tp := view(t, r, pid).TP
	r.f.w.OrderStatus(cid, "Filled", Qty(q), 0, tp, 9101, pid, tp, 80, "", 0)
	if v := view(t, r, pid); v.StopState != stDisarmed {
		t.Fatalf("%+v", v)
	}
	n := lastStop(aud).Note
	for _, want := range []string{"TP filled; confirmed by orderStatus", "status=Filled", "filled=" + itoa(q), "remaining=0", "perm=9101", "client=80"} {
		if !strings.Contains(n, want) {
			t.Fatalf("audit note %q lacks %q", n, want)
		}
	}
	// every orderStatus is in the audit
	st := 0
	for _, x := range memRecs(aud) {
		if x.Ev == "status" {
			st++
		}
	}
	if st < 5 {
		t.Fatalf("only %d status records", st)
	}
	_ = cid
}

func itoa(v int64) string { return i64(v) }

// ─── decoders vs ibapi v0.10.37 at server version 176 ────────────────────

type ibStat struct {
	ibapi.Wrapper
	stat chan []any
	open chan []any
}

func (w *ibStat) OrderStatus(id ibapi.OrderID, status string, filled, remaining ibapi.Decimal, avg float64,
	perm, parent int64, last float64, client int64, why string, mkt float64) {
	w.stat <- []any{int64(id), status, filled.Float(), remaining.Float(), avg, perm, parent, last, client, why, mkt}
}
func (w *ibStat) OpenOrder(id ibapi.OrderID, c *ibapi.Contract, o *ibapi.Order, st *ibapi.OrderState) {
	w.open <- []any{id, c, o, st}
}

func ibDecodeStat(t *testing.T, sv int, f []string) *ibStat {
	t.Helper()
	w := &ibStat{stat: make(chan []any, 1), open: make(chan []any, 1)}
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

func TestOrderStatusDecodeMatchesIbapi(t *testing.T) {
	for _, f := range [][]string{
		{"3", "30673", "Filled", "1275", "0", "0.3995", "461319001", "0", "0.3995", "80", "", "0"},
		{"3", "30674", "Submitted", "0", "1275", "0", "461319002", "30673", "0", "80", "", "0"},
		{"3", "30674", "PreSubmitted", "0", "1275", "0", "461319002", "30673", "0", "80", "locate", "1.7976931348623157E308"},
	} {
		r := &rec{}
		dispatch(r, 176, f)
		got := recv(t, ibDecodeStat(t, 176, f).stat)
		if r.stat == nil {
			t.Fatalf("mine decoded nothing: %v", f)
		}
		mine := []any{r.stat[0], r.stat[1], float64(r.stat[2].(Qty)), float64(r.stat[3].(Qty)), r.stat[4], r.stat[5], r.stat[6], r.stat[7], r.stat[8], r.stat[9]}
		if !reflect.DeepEqual(mine, got[:10]) {
			t.Fatalf("mine %v ibapi %v", mine, got[:10])
		}
	}
}

// A regular (not what-if) TP openOrder at 176: same head and OrderState status as ibapi.
func TestRegularOpenOrderDecodeMatchesIbapi(t *testing.T) {
	head := openOrderFields(176)
	head = head[:len(head)-8]
	tail := whatIfTail(false)
	for i := range tail {
		if tail[i] == "PreSubmitted" && tail[i-1] == "1" {
			tail[i-1], tail[i] = "0", "Submitted" // whatIf=false, a live status
			break
		}
	}
	f := append(append([]string(nil), head...), tail...)
	r := &rec{}
	dispatch(r, 176, f)
	got := recv(t, ibDecodeStat(t, 176, f).open)
	io, ist := got[2].(*ibapi.Order), got[3].(*ibapi.OrderState)
	mine, mo := r.open[3].(OrderState), r.open[2].(Order)
	if !mine.Parsed || mine.WhatIf || mine.Status != "Submitted" || ist.Status != mine.Status || io.WhatIf ||
		io.ParentID != mo.ParentID || io.PermID != mo.PermID || int64(io.ClientID) != mo.ClientID || io.OrderRef != mo.OrderRef {
		t.Fatalf("mine %+v %+v ibapi %+v %+v", mo, mine, io, ist)
	}
}

func memRecs(a *MemAudit) []auditRec { r, _ := a.Load(); return r }
