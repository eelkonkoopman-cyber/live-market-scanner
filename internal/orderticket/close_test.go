package orderticket

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestPrepareStopOffPersistsAndReplays(t *testing.T) {
	aud := &MemAudit{}
	r := newRig(t, aud)
	r.ready("SGRX", "111", 1.50)
	p, err := r.tk.PrepareOrder("BUY", 1000, 0, "SGRX", 0, true, 0)
	if err != nil || !strings.HasSuffix(p.Text, ", no SL") || !p.StopOff {
		t.Fatalf("%+v %v", p, err)
	}
	if err := r.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatal(err)
	}
	o := r.tk.State().Orders[0]
	pid, cid, q := o.ID, o.ChildID, o.Qty
	r.f.w.OrderStatus(pid, "Submitted", 100, Qty(q-100), 1.50, 9100, 0, 1.50, 80, "", 0)
	if v := view(t, r, pid); v.StopOn || v.StopState != "" {
		t.Fatalf("stop armed although off: %+v", v)
	}
	price(r, 1.40)
	price(r, 1.40)
	if len(cancels(r)) != 0 {
		t.Fatal("disabled stop fired")
	}
	r.tk.Disconnect()

	r2 := newRig(t, aud)
	r2.f.onOpen = func(w Wrapper) {
		w.OpenOrder(pid, contractFor("SGRX"), &Order{Action: "BUY", OrderType: "LMT", TotalQuantity: Qty(q), LmtPrice: 1.5,
			TIF: "DAY", OrderRef: refPrefix + "SGRX", ClientID: 80}, &OrderState{})
		w.OpenOrder(cid, contractFor("SGRX"), &Order{Action: "SELL", OrderType: "LMT", TotalQuantity: Qty(q), LmtPrice: 1.65,
			TIF: "GTC", OrderRef: refPrefix + "SGRX:tp", ParentID: pid, ClientID: 80}, &OrderState{})
	}
	r2.ready("SGRX", "111", 1.50)
	deadline := time.Now().Add(2 * time.Second)
	for len(r2.tk.State().Orders) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("not adopted")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if v := view(t, r2, pid); v.StopOn {
		t.Fatalf("stop-off not replayed %+v", v)
	}
	// default prepare keeps the stop on
	p, _ = r2.tk.Prepare("BUY", 1000, "SGRX")
	if p.StopOff || !strings.HasSuffix(p.Text, ", SL 1% under 9EMA (soft)") {
		t.Fatalf("%+v", p)
	}
}

func TestCancelSymbolOnlyUnfilled(t *testing.T) {
	r := newRig(t, nil)
	r.ready("SGRX", "111", 1.50)
	r.f.w.OpenOrder(555, contractFor("SGRX"), &Order{Action: "BUY", OrderType: "LMT", TotalQuantity: 10, OrderRef: "manual", ClientID: 80}, &OrderState{})
	if s := r.tk.State(); s.CancelCount != 0 || s.CloseQty != 0 {
		t.Fatalf("buttons enabled with nothing: %+v", s)
	}
	pid, cid, _ := placeOrder(t, r, "BUY")
	if r.tk.State().CancelCount != 1 {
		t.Fatal("cancel count")
	}
	msg, err := r.tk.CancelSymbol("SGRX")
	if err != nil || !strings.Contains(msg, "entry+TP") {
		t.Fatalf("%q %v", msg, err)
	}
	if c := cancels(r); len(c) != 2 || c[0] != pid || c[1] != cid {
		t.Fatalf("cancels %v", c)
	}

	// partly filled: only the rest of the entry; the TP stays
	r2 := newRig(t, nil)
	r2.ready("SGRX", "111", 1.50)
	pid2, _, q2 := placeOrder(t, r2, "BUY")
	r2.f.w.OrderStatus(pid2, "Submitted", 100, Qty(q2-100), 1.50, 9100, 0, 1.50, 80, "", 0)
	_, _ = r2.tk.CancelSymbol("SGRX")
	if c := cancels(r2); len(c) != 1 || c[0] != pid2 {
		t.Fatalf("cancels %v", c)
	}
	// fully filled: nothing
	r3, _, _, _ := armedLong(t)
	if msg, _ := r3.tk.CancelSymbol("SGRX"); len(cancels(r3)) != 0 || !strings.HasPrefix(msg, "nothing") {
		t.Fatalf("filled position cancelled: %q", msg)
	}
	for _, rr := range []*rig{r, r2, r3} {
		for _, id := range cancels(rr) {
			if id == 555 {
				t.Fatal("foreign order touched")
			}
		}
	}
	if _, err := r.tk.CancelSymbol("ABCD"); err == nil {
		t.Fatal("cancel for another symbol accepted")
	}
}

func TestCloseSizesFromConfirmedFills(t *testing.T) {
	r, pid, cid, q := armedLong(t)
	r.f.w.OrderStatus(cid, "Submitted", 40, Qty(q-40), 1.65, 0, pid, 1.65, 80, "", 0)
	price(r, 1.60)
	if s := r.tk.State(); s.CloseQty != q-40 {
		t.Fatalf("close qty %d", s.CloseQty)
	}
	p, err := r.tk.PrepareClose("SGRX")
	if err != nil || p.Qty != q-40 || p.Text != "CLOSE "+fmtQty(q-40)+" SGRX @ ~1.60" {
		t.Fatalf("%+v %v", p, err)
	}
	if len(cancels(r)) != 0 || len(exits(r)) != 0 {
		t.Fatal("PrepareClose acted")
	}
	if err := r.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatal(err)
	}
	if c := cancels(r); len(c) != 1 || c[0] != cid || len(exits(r)) != 0 {
		t.Fatalf("cancels %v exits %d: TP cancel must be confirmed first", c, len(exits(r)))
	}
	r.f.w.OrderStatus(cid, "Cancelled", 50, Qty(q-50), 1.65, 0, pid, 1.65, 80, "", 0) // 10 more TP fills in flight
	ex := exits(r)
	if len(ex) != 1 || ex[0].o.TotalQuantity.Int() != q-50 || ex[0].o.Action != "SELL" || !near(ex[0].o.LmtPrice, 1.58) {
		t.Fatalf("%+v", ex)
	}
	if r.tk.State().CloseQty != 0 {
		t.Fatal("close still enabled while exiting")
	}
}

func TestCloseAndStopGiveOneExit(t *testing.T) {
	r, pid, cid, q := armedLong(t)
	price(r, 1.60)
	p, _ := r.tk.PrepareClose("SGRX")
	if err := r.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatal(err)
	}
	price(r, 1.40) // the stop level is crossed while the close waits
	price(r, 1.40)
	r.f.w.OrderStatus(cid, "Cancelled", 0, Qty(q), 0, 0, pid, 0, 80, "", 0)
	r.tk.Tick(context.Background())
	ids := map[int64]bool{}
	for _, x := range exits(r) {
		ids[x.id] = true
	}
	if len(ids) != 1 {
		t.Fatalf("exits %v", ids)
	}

	// and the reverse: the stop fired first -> CLOSE is disabled
	r2, _, _, _ := armedLong(t)
	price(r2, 1.40)
	price(r2, 1.40)
	if s := r2.tk.State(); s.CloseQty != 0 {
		t.Fatalf("close enabled during the stop exit: %+v", s.CloseQty)
	}
	if _, err := r2.tk.PrepareClose("SGRX"); err == nil {
		t.Fatal("close accepted while the stop exits")
	}
}
