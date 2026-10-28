package orderticket

import (
	"context"
	"strings"
	"testing"
	"time"
)

func waitPreview(t *testing.T, r *rig) *Preview {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if p := r.tk.State().Preview; p != nil {
			return p
		}
		if time.Now().After(deadline) {
			t.Fatal("no preview")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func waitIdle(t *testing.T, r *rig) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		r.tk.mu.Lock()
		b := r.tk.pvBusy
		r.tk.mu.Unlock()
		if !b {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("preview round never finished")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestPreviewOnSymbolBothSidesAndThrottle(t *testing.T) {
	r := newRig(t, nil)
	r.f.avail = "1000"
	r.ready("SGRX", "111", 1.50)
	waitFunds(t, r, 1000)
	ctx := context.Background()
	r.tk.Tick(ctx)
	p := waitPreview(t, r)
	wi := r.f.whatIfsCopy()
	if len(wi) != 2 || wi[0].id == wi[1].id {
		t.Fatalf("whatIfs %+v", wi)
	}
	sides := map[string]placed{}
	for _, w := range wi {
		sides[w.o.Action] = w
		if !w.o.WhatIf || w.o.TotalQuantity.Int() != previewDefaultQty || !strings.HasPrefix(w.o.OrderRef, whatIfRefPfx) || w.o.ParentID != 0 {
			t.Fatalf("preview what-if %+v", w.o)
		}
	}
	if !near(sides["BUY"].o.LmtPrice, 1.5) || !near(sides["SELL"].o.LmtPrice, 1.5) {
		t.Fatalf("prices %+v", sides)
	}
	// 25% of 1.50 = 0.375/sh; 1000/0.375 = 2666 > cap 5000/1.5 = 3333 -> 2666
	if p.Sym != "SGRX" || p.Text != "Max long 2,666 sh · Max short 2,666 sh (margin $/sh 0.38 / 0.38)" {
		t.Fatalf("%+v", p)
	}
	if len(r.f.placesCopy()) != 0 {
		t.Fatal("preview placed a real order")
	}
	// throttle: same symbol + qty within 20 s -> nothing
	r.tk.Tick(ctx)
	r.advance(5 * time.Second)
	r.tk.Tick(ctx)
	waitIdle(t, r)
	if n := len(r.f.whatIfsCopy()); n != 2 {
		t.Fatalf("throttle broken: %d what-ifs", n)
	}
	// qty change -> new round
	r.tk.SetPreviewSize(0, 200, 0)
	r.tk.Tick(ctx)
	waitIdle(t, r)
	if wi = r.f.whatIfsCopy(); len(wi) != 4 || wi[3].o.TotalQuantity.Int() != 200 {
		t.Fatalf("qty change %+v", wi)
	}
	// 20 s later -> again
	r.advance(21 * time.Second)
	r.tk.Tick(ctx)
	waitIdle(t, r)
	if n := len(r.f.whatIfsCopy()); n != 6 {
		t.Fatalf("after 20 s: %d", n)
	}
	// symbol change -> both sides for the new symbol
	r.seedBars("ABCD", 30, 2.0)
	r.show("222")
	r.tk.Tick(ctx)
	waitIdle(t, r)
	wi = r.f.whatIfsCopy()
	if len(wi) != 8 || wi[6].c.Symbol != "ABCD" || wi[7].c.Symbol != "ABCD" || wi[6].o.Action == wi[7].o.Action {
		t.Fatalf("symbol change %+v", wi[6:])
	}
	if p := r.tk.State().Preview; p == nil || p.Sym != "ABCD" {
		t.Fatalf("%+v", p)
	}
	if len(r.f.placesCopy()) != 0 {
		t.Fatal("real orders sent")
	}
	// never adopted / known
	for _, w := range wi {
		if _, ok := r.tk.known[w.id]; ok {
			t.Fatalf("preview id %d known", w.id)
		}
	}
	if len(r.tk.State().Orders) != 0 {
		t.Fatal("preview became an order")
	}
}

func TestPreviewDroppedWhenSymbolChangesAndNA(t *testing.T) {
	r := newRig(t, nil)
	r.f.whatIfReply = func(*Order) *OrderState { return nil } // TWS silent
	r.ready("SGRX", "111", 1.50)
	r.tk.Tick(context.Background())
	r.seedBars("ABCD", 30, 2.0)
	r.show("222") // SGRX no longer shown while its round is in flight
	waitIdle(t, r)
	r.tk.mu.Lock()
	got := r.tk.previews["SGRX"]
	r.tk.mu.Unlock()
	if got != nil {
		t.Fatalf("stale result kept %+v", got)
	}
	// the next round (ABCD) times out -> "margin n/a"
	r.tk.Tick(context.Background())
	if p := waitPreview(t, r); p.Sym != "ABCD" || p.Text != "margin n/a" {
		t.Fatalf("%+v", p)
	}
}

func TestPreviewDoesNotDisturbPrepare(t *testing.T) {
	r := newRig(t, nil)
	r.ready("SGRX", "111", 1.50)
	p, err := r.tk.Prepare("BUY", 1000, "SGRX")
	if err != nil {
		t.Fatal(err)
	}
	r.tk.Tick(context.Background()) // preview round while the confirmation is pending
	waitPreview(t, r)
	s := r.tk.State()
	if s.Pending == nil || s.Pending.Token != p.Token || s.Pending.Margin.Status != "ok" {
		t.Fatalf("pending disturbed %+v", s.Pending)
	}
	if err := r.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatal(err)
	}
	ps := r.f.placesCopy()
	if len(ps) != 2 || ps[0].o.WhatIf || ps[1].o.WhatIf {
		t.Fatalf("%+v", ps)
	}
	for _, w := range r.f.whatIfsCopy() {
		if w.id == ps[0].id || w.id == ps[1].id {
			t.Fatal("what-if and real order share an id")
		}
	}
}
