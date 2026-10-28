package orderticket

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTPMathBothSides(t *testing.T) {
	for _, c := range []struct {
		side      string
		pct, want float64
	}{
		{"BUY", 1, 2.53},   // 2.525 -> up
		{"BUY", 10, 2.75},  // 2.75
		{"SELL", 1, 2.47},  // 2.475 -> down
		{"SELL", 10, 2.25}, // 2.25
	} {
		if got := tpPrice(c.side, 2.50, c.pct); !near(got, c.want) {
			t.Errorf("%s %v%%: %v want %v", c.side, c.pct, got, c.want)
		}
	}
	if !near(tpPrice("BUY", 0.8123, 1), 0.8205) || !near(tpPrice("SELL", 0.8124, 1), 0.8042) { // 0.820423 up; 0.804276 down
		t.Fatal("sub-dollar ticks")
	}
}

func TestPrepareTPPresetAndValidation(t *testing.T) {
	r := newRig(t, nil)
	r.ready("SGRX", "111", 1.50)
	p, err := r.tk.PrepareTP("BUY", 1000, 0, "SGRX", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !near(p.TP, 1.52) || p.TPPct != 1 || !strings.Contains(p.Text, "TP +1% 1.52, SL 1% under 9EMA (soft)") {
		t.Fatalf("%+v", p)
	}
	p, _ = r.tk.PrepareTP("SELL", 1000, 0, "SGRX", 1)
	if !near(p.TP, 1.48) || !strings.Contains(p.Text, "TP -1% 1.48, SL 1% over 9EMA (soft)") {
		t.Fatalf("%+v", p)
	}
	if p, _ = r.tk.PrepareTP("BUY", 1000, 0, "SGRX", 0); p.TPPct != 10 || !near(p.TP, 1.65) {
		t.Fatalf("default %+v", p)
	}
	if _, err := r.tk.PrepareTP("BUY", 1000, 0, "SGRX", 5); err == nil || !strings.Contains(err.Error(), "preset") {
		t.Fatalf("5%% accepted: %v", err)
	}
	ctx := context.Background()
	for _, bad := range [][2]float64{{0.05, 10}, {1, 60}} {
		if err := r.tk.SetTPPresets(ctx, bad[0], bad[1]); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
	if err := r.tk.SetTPPresets(ctx, 2, 8); err != nil || !strings.Contains(r.meta[MetaKey], `"tpLowPct":2`) {
		t.Fatalf("%v %s", err, r.meta[MetaKey])
	}
	if _, err := r.tk.PrepareTP("BUY", 1000, 0, "SGRX", 1); err == nil {
		t.Fatal("old preset accepted after change")
	}
	if p, err = r.tk.PrepareTP("BUY", 1000, 0, "SGRX", 2); err != nil || p.TPPct != 2 {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestTPPercentKeptOnFillAndReplay(t *testing.T) {
	aud := &MemAudit{}
	r := newRig(t, aud)
	r.f.nextID = 99
	r.ready("SGRX", "111", 1.50)
	p, err := r.tk.PrepareTP("BUY", 1000, 0, "SGRX", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatal(err)
	}
	ps := r.f.placesCopy()
	if !near(ps[1].o.LmtPrice, 1.52) {
		t.Fatalf("child %+v", ps[1].o)
	}
	// first fill: TP re-priced from the fill avg with the order's own 1%
	r.f.w.OrderStatus(100, "Submitted", Qty(100), Qty(566), 1.53, 9100, 0, 1.53, 80, "", 0)
	ps = r.f.placesCopy()
	if last := ps[len(ps)-1]; last.id != 101 || !near(last.o.LmtPrice, 1.55) { // 1.5453 -> up
		t.Fatalf("re-price %+v", last)
	}

	// restart: replay adopts the parent with ITS percent (1%), and a legacy
	// record without one gets 10%.
	aud.Recs = append(aud.Recs,
		auditRec{Ev: "place", ID: 200, ClientID: 80, Sym: "SGRX", Side: "BUY", Qty: 100, Limit: 1.5, TP: 1.65},
		auditRec{Ev: "place", ID: 201, ParentID: 200, ClientID: 80, Sym: "SGRX", Side: "BUY", Qty: 100, Limit: 1.65})
	r2 := newRig(t, aud)
	r2.f.nextID = 300
	r2.f.onOpen = func(w Wrapper) {
		for _, id := range []int64{100, 200} {
			w.OpenOrder(id, contractFor("SGRX"), &Order{Action: "BUY", OrderType: "LMT", TotalQuantity: 100, LmtPrice: 1.5,
				TIF: "DAY", OrderRef: refPrefix + "SGRX", ClientID: 80}, &OrderState{})
		}
	}
	r2.ready("SGRX", "111", 1.50)
	deadline := time.Now().Add(2 * time.Second)
	for len(r2.tk.State().Orders) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("not adopted %+v", r2.tk.State().Orders)
		}
		time.Sleep(2 * time.Millisecond)
	}
	r2.f.w.OrderStatus(100, "Submitted", Qty(10), Qty(90), 2.00, 0, 0, 2, 80, "", 0)
	r2.f.w.OrderStatus(200, "Submitted", Qty(10), Qty(90), 2.00, 0, 0, 2, 80, "", 0)
	got := map[int64]float64{}
	for _, x := range r2.f.placesCopy() {
		got[x.id] = x.o.LmtPrice
	}
	if !near(got[101], 2.02) || !near(got[201], 2.20) {
		t.Fatalf("replayed TP prices %v (want 101->2.02 at 1%%, legacy 201->2.20 at 10%%)", got)
	}
}

func TestTPPlaceOrderBytesDifferOnlyInPrice(t *testing.T) {
	tk := &Ticket{account: "U1234567"}
	o1 := &ord{ID: 100, ChildID: 101, Sym: "SGRX", Side: "BUY", Qty: 666, Limit: 1.53, TP: tpPrice("BUY", 1.53, 1)}
	o10 := *o1
	o10.TP = tpPrice("BUY", 1.53, 10)
	a := encPlaceOrder(176, 101, contractFor("SGRX"), tk.tpOrder(o1))
	b := encPlaceOrder(176, 101, contractFor("SGRX"), tk.tpOrder(&o10))
	if len(a) != len(b) {
		t.Fatal("length differs")
	}
	var diff []int
	for i := range a {
		if a[i] != b[i] {
			diff = append(diff, i)
		}
	}
	if len(diff) != 1 || a[diff[0]] != "1.55" || b[diff[0]] != "1.69" {
		t.Fatalf("diff at %v: %q vs %q", diff, a, b)
	}
}
