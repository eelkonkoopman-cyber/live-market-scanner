package orderticket

import "testing"

func TestMoveStopVwapxResendsTheIBStop(t *testing.T) {
	r, _ := vwapxRig(t, nil)
	p, err := r.tk.PrepareVwapx(0, 120, "SGRX", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatal(err)
	}
	n := len(r.f.placesCopy())
	var id int64
	for k := range r.tk.orders {
		id = k
	}
	if err = r.tk.MoveStop(id, 27.50); err != nil {
		t.Fatal(err)
	}
	ps := r.f.placesCopy()
	if len(ps) != n+1 || ps[n].o.OrderType != "STP" || !near(ps[n].o.AuxPrice, 27.50) || !ps[n].o.Transmit {
		t.Fatalf("%+v", ps[n:])
	}
	if err = r.tk.MoveStop(id, 28.50); err == nil {
		t.Fatal("stop above the price accepted")
	}
	if len(r.f.placesCopy()) != n+1 {
		t.Fatal("refused move sent an order")
	}
}

func TestMoveStopSoftLatches(t *testing.T) {
	r := newRig(t, nil)
	r.tk.orders[7] = &ord{ID: 7, Sym: "SGRX", Side: "BUY", Qty: 10, Filled: 10, Avg: 28}
	r.tk.orders[7].StopFollow, r.tk.orders[7].StopState, r.tk.orders[7].StopLevel = true, stArmed, 27
	r.ready("SGRX", "111", 27.95)
	if err := r.tk.MoveStop(7, 27.50); err != nil {
		t.Fatal(err)
	}
	o := r.tk.orders[7]
	if !near(o.StopLevel, 27.50) || o.StopFollow || !o.BE {
		t.Fatalf("%+v", o)
	}
}
