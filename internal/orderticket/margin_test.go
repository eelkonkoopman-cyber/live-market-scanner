package orderticket

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestComputeMargin(t *testing.T) {
	st := func(ini, mnt, warn string) whatIfRes {
		return whatIfRes{st: &OrderState{Parsed: true, WhatIf: true, InitMarginChange: ini, MaintMarginChange: mnt, WarningText: warn}}
	}
	// 1000 sh @ 2.00: init 500, maint 600 -> 0.60/sh; avail 3000 -> 5000 sh, cap $5000/2 = 2500.
	m := computeMargin(st("500", "600", ""), 1000, 2, 3000, true, 5000, "USD")
	if m.Status != "ok" || m.Needed != 600 || m.MaxShares != 2500 || m.Text != "needs $600 margin (avail $3,000) — max 2,500 sh" {
		t.Fatalf("%+v", m)
	}
	// avail 300 -> floor(300/0.6) = 500 < 1000: over
	m = computeMargin(st("500", "600", "big order"), 1000, 2, 300, true, 5000, "USD")
	if m.Status != "over" || m.MaxShares != 500 || !strings.Contains(m.Text, "max 500 sh") || !strings.Contains(m.Text, "IB: big order") {
		t.Fatalf("%+v", m)
	}
	// no margin change (e.g. closing): only the cap limits
	if m = computeMargin(st("0", "0", ""), 100, 2, 10, true, 5000, "USD"); m.Status != "ok" || m.MaxShares != 2500 {
		t.Fatalf("%+v", m)
	}
	// no funds yet / timeout / undecodable / unset figures: unavailable
	for _, r := range []whatIfRes{
		{err: "no answer from TWS within 5s"},
		{st: &OrderState{}},
		st("1.7976931348623157E308", "", ""),
	} {
		if m = computeMargin(r, 100, 2, 1000, true, 5000, "USD"); m.Status != "unavailable" || !strings.HasPrefix(m.Text, "margin check unavailable") {
			t.Fatalf("%+v -> %+v", r, m)
		}
	}
	if m = computeMargin(st("100", "", ""), 100, 2, 0, false, 5000, "USD"); m.Status != "unavailable" || m.Needed != 100 {
		t.Fatalf("no funds %+v", m)
	}
}

func TestWhatIfBeforeConfirmAndUseMax(t *testing.T) {
	r := newRig(t, nil)
	r.f.avail = "100"
	r.f.nextID = 99
	r.ready("SGRX", "111", 1.50)
	waitFunds(t, r, 100)
	p, err := r.tk.Prepare("BUY", 1000, "SGRX") // 666 sh @ 1.50: needs 249.75 (25%) -> 0.375/sh -> max 266
	if err != nil {
		t.Fatal(err)
	}
	wi := r.f.whatIfsCopy()
	if len(wi) != 1 || len(r.f.placesCopy()) != 0 {
		t.Fatalf("whatIfs %d places %d", len(wi), len(r.f.placesCopy()))
	}
	w := wi[0]
	if w.id != 99 || !w.o.WhatIf || !w.o.Transmit || w.o.ParentID != 0 || w.o.TotalQuantity.Int() != 666 ||
		!near(w.o.LmtPrice, 1.5) || w.o.Action != "BUY" || w.o.OrderType != "LMT" || strings.HasPrefix(w.o.OrderRef, refPrefix) {
		t.Fatalf("what-if order %+v", w)
	}
	m := p.Margin
	if m == nil || m.Status != "over" || m.MaxShares != 266 || r.tk.State().Pending.Margin.MaxShares != 266 {
		t.Fatalf("margin %+v", m)
	}
	if err := r.tk.Confirm(p.Token, "SGRX"); err == nil || !strings.Contains(err.Error(), "margin max") {
		t.Fatalf("over-margin confirm: %v", err)
	}
	if len(r.f.placesCopy()) != 0 {
		t.Fatal("over-margin order sent")
	}
	// Use max: re-prepare with N shares, re-running the what-if
	p, err = r.tk.PrepareQty("BUY", 0, m.MaxShares, "SGRX")
	if err != nil || p.Margin.Status != "ok" || p.Qty != 266 {
		t.Fatalf("use max %+v %v", p, err)
	}
	if wi = r.f.whatIfsCopy(); len(wi) != 2 || wi[1].id != 100 || wi[1].o.TotalQuantity.Int() != 266 {
		t.Fatalf("second what-if %+v", wi)
	}
	if err := r.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatal(err)
	}
	ps := r.f.placesCopy()
	if len(ps) != 2 || ps[0].id != 101 || ps[1].id != 102 {
		t.Fatalf("real orders %+v", ps)
	}
	for _, x := range ps {
		if x.o.WhatIf {
			t.Fatal("a real order carries whatIf")
		}
	}
	// audit: what-ifs are notes with ID 0, never "place"
	for _, a := range r.aud.Recs {
		if a.Ev == "whatif" && a.ID != 0 {
			t.Fatalf("what-if audited with an id: %+v", a)
		}
		if a.Ev != "whatif" && (a.ID == 99 || a.ID == 100) {
			t.Fatalf("what-if id recorded as an order: %+v", a)
		}
	}
	// a late/duplicate what-if openOrder is never adopted; follow never touches it
	r.f.w.OpenOrder(99, contractFor("SGRX"), &Order{Action: "BUY", OrderRef: whatIfRefPfx + "SGRX", TotalQuantity: 666},
		&OrderState{WhatIf: true, Parsed: true})
	r.f.w.OrderStatus(99, "PreSubmitted", 0, 666, 0, 0, 0, 0, 80, "", 0)
	for _, o := range r.tk.State().Orders {
		if o.ID == 99 || o.ID == 100 {
			t.Fatalf("what-if adopted %+v", o)
		}
	}
	if err := r.tk.Cancel(99); err == nil {
		t.Fatal("what-if id cancellable")
	}
	r.closeBar("SGRX", 1.9)
	r.tk.Tick(context.Background())
	for _, x := range r.f.placesCopy() {
		if x.id == 99 || x.id == 100 {
			t.Fatalf("what-if id modified %+v", x)
		}
	}
	// restart: the audit's what-if notes confer no ownership
	tk2, err := New(Config{Audit: r.aud})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tk2.known[99]; ok {
		t.Fatal("what-if id known after restart")
	}
	if _, ok := tk2.known[101]; !ok {
		t.Fatal("real order not known after restart")
	}
}

func TestWhatIfTimeoutStillConfirmable(t *testing.T) {
	r := newRig(t, nil)
	r.f.whatIfReply = func(*Order) *OrderState { return nil } // TWS silent
	r.ready("SGRX", "111", 1.50)
	start := time.Now()
	p, err := r.tk.Prepare("BUY", 1000, "SGRX")
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 250*time.Millisecond {
		t.Fatalf("did not wait for the what-if (%v)", d)
	}
	if p.Margin == nil || p.Margin.Status != "unavailable" || !strings.Contains(p.Margin.Text, "margin check unavailable") {
		t.Fatalf("%+v", p.Margin)
	}
	if err := r.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatalf("unavailable margin check must still allow confirm: %v", err)
	}
	if len(r.f.placesCopy()) != 2 {
		t.Fatal("order not sent")
	}
}

func TestWhatIfErrorIsUnavailable(t *testing.T) {
	r := newRig(t, nil)
	r.f.nextID = 99
	r.f.whatIfReply = func(*Order) *OrderState {
		go r.f.w.Error(99, 0, 201, "Order rejected - reason: margin", "")
		return nil
	}
	r.ready("SGRX", "111", 1.50)
	p, err := r.tk.Prepare("BUY", 1000, "SGRX")
	if err != nil || p.Margin.Status != "unavailable" || !strings.Contains(p.Margin.Text, "IB 201") {
		t.Fatalf("%+v %v", p.Margin, err)
	}
}

func waitFunds(t *testing.T, r *rig, want float64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if f := r.tk.State().Funds; f != nil && f.HaveAvail && f.Avail == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("funds %+v", r.tk.State().Funds)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestAccountSummarySubscription(t *testing.T) {
	r := newRig(t, nil)
	r.f.avail = "25000.5"
	r.connect()
	waitFunds(t, r, 25000.5)
	r.f.mu.Lock()
	subs := r.f.acctSubs
	r.f.mu.Unlock()
	if subs != 1 {
		t.Fatalf("subs %d", subs)
	}
	// other accounts' rows and other req ids are ignored
	r.f.w.AccountSummary(acctReqID, "U999", "AvailableFunds", "1", "USD")
	r.f.w.AccountSummary(1, "U1234567", "AvailableFunds", "2", "USD")
	r.f.w.AccountSummary(acctReqID, "U1234567", "NetLiquidation", "40000", "USD")
	if f := r.tk.State().Funds; f.Avail != 25000.5 || f.NetLiq != 40000 {
		t.Fatalf("%+v", f)
	}
	r.tk.Disconnect()
	deadline := time.Now().Add(2 * time.Second)
	for {
		r.f.mu.Lock()
		c := r.f.acctCancels
		r.f.mu.Unlock()
		if c == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("account summary not cancelled on disconnect")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if r.tk.State().Funds != nil {
		t.Fatal("funds kept after disconnect")
	}
}
