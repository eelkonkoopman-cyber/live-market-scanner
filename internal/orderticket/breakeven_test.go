package orderticket

import (
	"context"
	"strings"
	"testing"
	"time"
)

func beRecs(r *rig) int {
	n := 0
	for _, a := range r.aud.Recs {
		if a.Ev == "stop" && strings.HasPrefix(a.Note, "breakeven") {
			n++
		}
	}
	return n
}

func TestBreakevenLongOnceAndLabel(t *testing.T) {
	r, pid, _, _ := armedLong(t) // avg 1.50, stop 1.48
	price(r, 1.57)
	if v := view(t, r, pid); v.StopBE || !near(v.StopLevel, 1.48) {
		t.Fatalf("moved early %+v", v)
	}
	price(r, 1.575) // +5%
	v := view(t, r, pid)
	if !v.StopBE || !near(v.StopLevel, 1.50) || v.StopLabel != "SL 1.50 (soft, BE)" {
		t.Fatalf("%+v", v)
	}
	if n := r.tk.State().Note; n != "SGRX stop moved to entry 1.50 (+5% reached)" {
		t.Fatalf("note %q", n)
	}
	price(r, 1.80)
	price(r, 1.90)
	if beRecs(r) != 1 || !near(view(t, r, pid).StopLevel, 1.50) {
		t.Fatal("breakeven not one-shot")
	}
	// the stop now fires at entry
	price(r, 1.50)
	price(r, 1.49)
	if len(cancels(r)) != 1 {
		t.Fatal("breakeven stop did not fire")
	}
}

func TestBreakevenShortAndOffset(t *testing.T) {
	r := newRig(t, nil)
	if err := r.tk.SetBreakeven(context.Background(), true, 5, 0.2); err != nil {
		t.Fatal(err)
	}
	r.ready("SGRX", "111", 1.50)
	pid, _, q := placeOrder(t, r, "SELL")
	r.f.w.OrderStatus(pid, "Filled", Qty(q), 0, 1.50, 9100, 0, 1.50, 80, "", 0)
	if v := view(t, r, pid); !near(v.StopLevel, 1.52) {
		t.Fatalf("%+v", v)
	}
	price(r, 1.43)
	if view(t, r, pid).StopBE {
		t.Fatal("early")
	}
	price(r, 1.425)                                                  // -5%
	if v := view(t, r, pid); !v.StopBE || !near(v.StopLevel, 1.49) { // 1.50 x 0.998 = 1.497 -> down 1.49
		t.Fatalf("%+v", v)
	}
	if !strings.Contains(r.tk.State().Note, "(-5% reached)") {
		t.Fatal(r.tk.State().Note)
	}
	if beLevel("BUY", 1.50, 0.2) != 1.51 { // 1.503 -> up
		t.Fatal("long offset")
	}
}

func TestBreakevenNeverLoosens(t *testing.T) {
	tk := &Ticket{}
	o := &ord{Side: "BUY", Avg: 1.50, Filled: 100}
	o.StopState, o.StopLevel, o.BEPct = stArmed, 1.55, 5
	tk.beCheckLocked(o, 1.60)
	if !o.BE || !near(o.StopLevel, 1.55) {
		t.Fatalf("loosened: %+v", o.stop)
	}
	s := &ord{Side: "SELL", Avg: 1.50, Filled: 100}
	s.StopState, s.StopLevel, s.BEPct = stArmed, 1.45, 5
	tk.beCheckLocked(s, 1.40)
	if !s.BE || !near(s.StopLevel, 1.45) {
		t.Fatalf("short loosened: %+v", s.stop)
	}
}

func TestBreakevenThenFollow9EMA(t *testing.T) {
	r := newRig(t, nil)
	_ = r.tk.SetStopSettings(context.Background(), 1, true)
	r.tk.settings.EMALive = false
	r.ready("SGRX", "111", 1.50)
	pid, _, q := placeOrder(t, r, "BUY")
	r.f.w.OrderStatus(pid, "Filled", Qty(q), 0, 1.50, 9100, 0, 1.50, 80, "", 0)
	r.closeBar("SGRX", 1.70) // the 1.70 print latches BE (1.50); bar closes: EMA 1.54
	r.tk.Tick(context.Background())
	if v := view(t, r, pid); !v.StopBE || !near(v.StopLevel, 1.52) { // follow raised past entry: 1.5246 -> 1.52
		t.Fatalf("%+v", v)
	}
	r.closeBar("SGRX", 1.56) // EMA falls back: raise-only keeps 1.52
	r.tk.Tick(context.Background())
	if v := view(t, r, pid); !near(v.StopLevel, 1.52) {
		t.Fatalf("follow lowered the stop %+v", v)
	}
}

func TestBreakevenPartialFillAvg(t *testing.T) {
	r := newRig(t, nil)
	r.ready("SGRX", "111", 1.50)
	pid, _, q := placeOrder(t, r, "BUY")
	r.f.w.OrderStatus(pid, "Submitted", 100, Qty(q-100), 1.50, 9100, 0, 1.50, 80, "", 0)
	price(r, 1.58)
	if v := view(t, r, pid); !v.StopBE || !near(v.StopLevel, 1.50) {
		t.Fatalf("%+v", v)
	}
	r.f.w.OrderStatus(pid, "Filled", Qty(q), 0, 1.52, 9100, 0, 1.53, 80, "", 0) // new avg: tighter
	if v := view(t, r, pid); !near(v.StopLevel, 1.52) {
		t.Fatalf("not re-applied at the new avg %+v", v)
	}
	r.f.w.OrderStatus(pid, "Filled", Qty(q), 0, 1.51, 9100, 0, 1.51, 80, "", 0) // lower avg: keep the higher level
	if v := view(t, r, pid); !near(v.StopLevel, 1.52) {
		t.Fatalf("loosened on a lower avg %+v", v)
	}
}

func TestBreakevenRestartAndDisabled(t *testing.T) {
	aud := &MemAudit{}
	r := newRig(t, aud)
	r.ready("SGRX", "111", 1.50)
	pid, cid, q := placeOrder(t, r, "BUY")
	r.f.w.OrderStatus(pid, "Filled", Qty(q), 0, 1.50, 9100, 0, 1.50, 80, "", 0)
	price(r, 1.60)
	r.tk.Disconnect()

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
	if v := view(t, r2, pid); !v.StopBE || !near(v.StopLevel, 1.50) || v.StopLabel != "SL 1.50 (soft, BE)" {
		t.Fatalf("restart %+v", v)
	}

	// disabled: no move
	r3 := newRig(t, nil)
	ctx := context.Background()
	if err := r3.tk.SetBreakeven(ctx, false, 0, 0); err != nil {
		t.Fatal(err)
	}
	r3.ready("SGRX", "111", 1.50)
	p3, _, q3 := placeOrder(t, r3, "BUY")
	r3.f.w.OrderStatus(p3, "Filled", Qty(q3), 0, 1.50, 9100, 0, 1.50, 80, "", 0)
	price(r3, 1.70)
	if v := view(t, r3, p3); v.StopBE || !near(v.StopLevel, 1.48) {
		t.Fatalf("disabled moved %+v", v)
	}
	for _, bad := range [][2]float64{{0.4, 0}, {51, 0}, {5, -0.1}, {5, 5.5}} {
		if err := r3.tk.SetBreakeven(ctx, true, bad[0], bad[1]); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
	if d := DefaultSettings(); d.BEOff || d.BEPct != 5 || d.BEOffsetPct != 0 {
		t.Fatalf("%+v", d)
	}
}
