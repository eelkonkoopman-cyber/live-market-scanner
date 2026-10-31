package orderticket

import (
	"strings"
	"testing"
	"time"
)

func TestManualBELong(t *testing.T) {
	r, pid, _, _ := armedLong(t) // avg 1.50, stop 1.48
	if r.tk.State().BECount != 1 || !view(t, r, pid).CanBE {
		t.Fatal("button disabled with a held position")
	}
	price(r, 1.55) // in profit, below the +5% auto trigger
	if err := r.tk.MoveStopToEntrySym("SGRX"); err != nil {
		t.Fatal(err)
	}
	v := view(t, r, pid)
	if !v.StopBE || !near(v.StopLevel, 1.50) || r.tk.State().Note != "SGRX stop moved to entry 1.50 (manual)" {
		t.Fatalf("%+v %q", v, r.tk.State().Note)
	}
	found := false
	for _, a := range r.aud.Recs {
		if a.Ev == "stop" && a.Note == "manual breakeven at 1.50" && a.StopBE && near(a.StopLevel, 1.50) {
			found = true
		}
	}
	if !found {
		t.Fatal("not audited")
	}
	// again: never loosens / no-op refused
	if err := r.tk.MoveStopToEntry(pid); err == nil || !strings.Contains(err.Error(), "already at or past entry") {
		t.Fatalf("%v", err)
	}
	// parent + TP + the TP re-sent at the fill average on the first fill (tpverify.go)
	if len(r.f.placesCopy()) != 3 || len(cancels(r)) != 0 {
		t.Fatal("manual breakeven sent orders")
	}
}

func TestManualBERefusedThroughEntry(t *testing.T) {
	r, pid, _, _ := armedLong(t)
	price(r, 1.49) // below entry (above the 1.48 stop)
	err := r.tk.MoveStopToEntry(pid)
	if err == nil || err.Error() != "price is below entry — use CLOSE instead" {
		t.Fatalf("%v", err)
	}
	if v := view(t, r, pid); !near(v.StopLevel, 1.48) || v.StopBE {
		t.Fatalf("moved %+v", v)
	}
	if n := r.tk.State().Note; !strings.Contains(n, "use CLOSE instead") {
		t.Fatalf("note %q", n)
	}
}

func TestManualBEShort(t *testing.T) {
	r := newRig(t, nil)
	r.ready("SGRX", "111", 1.50)
	pid, _, q := placeOrder(t, r, "SELL")
	r.f.w.OrderStatus(pid, "Filled", Qty(q), 0, 1.50, 9100, 0, 1.50, 80, "", 0)
	price(r, 1.53)
	if err := r.tk.MoveStopToEntry(pid); err == nil || !strings.Contains(err.Error(), "price is above entry") {
		t.Fatalf("%v", err)
	}
	price(r, 1.45)
	if err := r.tk.MoveStopToEntry(pid); err != nil {
		t.Fatal(err)
	}
	if v := view(t, r, pid); !near(v.StopLevel, 1.50) || !v.StopBE {
		t.Fatalf("%+v", v)
	}
}

func TestManualBEArmsWhenStopOff(t *testing.T) {
	r := newRig(t, nil)
	r.ready("SGRX", "111", 1.50)
	p, _ := r.tk.PrepareOrder("BUY", 1000, 0, "SGRX", 0, true, 0) // SL off
	if err := r.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatal(err)
	}
	o := r.tk.State().Orders[0]
	r.f.w.OrderStatus(o.ID, "Filled", Qty(o.Qty), 0, 1.50, 9100, 0, 1.50, 80, "", 0)
	if v := view(t, r, o.ID); v.StopOn || !v.CanBE {
		t.Fatalf("%+v", v)
	}
	price(r, 1.56)
	if err := r.tk.MoveStopToEntry(o.ID); err != nil {
		t.Fatal(err)
	}
	if v := view(t, r, o.ID); !v.StopOn || v.StopState != stArmed || !near(v.StopLevel, 1.50) {
		t.Fatalf("not armed at entry %+v", v)
	}
	price(r, 1.50)
	price(r, 1.49)
	if c := cancels(r); len(c) != 1 || c[0] != o.ChildID {
		t.Fatalf("armed stop did not fire: %v", c)
	}
}

func TestManualBENothingHeldAndRestart(t *testing.T) {
	r := newRig(t, nil)
	r.ready("SGRX", "111", 1.50)
	placeOrder(t, r, "BUY") // unfilled
	if r.tk.State().BECount != 0 {
		t.Fatal("enabled with nothing held")
	}
	if err := r.tk.MoveStopToEntrySym("SGRX"); err == nil {
		t.Fatal("accepted with nothing held")
	}

	aud := &MemAudit{}
	r1 := newRig(t, aud)
	r1.ready("SGRX", "111", 1.50)
	pid, cid, q := placeOrder(t, r1, "BUY")
	r1.f.w.OrderStatus(pid, "Filled", Qty(q), 0, 1.50, 9100, 0, 1.50, 80, "", 0)
	price(r1, 1.55)
	if err := r1.tk.MoveStopToEntrySym(""); err != nil {
		t.Fatal(err)
	}
	r1.tk.Disconnect()
	r2 := newRig(t, aud)
	r2.f.onOpen = func(w Wrapper) {
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
	if v := view(t, r2, pid); !v.StopBE || !near(v.StopLevel, 1.50) {
		t.Fatalf("restart lost the manual breakeven %+v", v)
	}
}
