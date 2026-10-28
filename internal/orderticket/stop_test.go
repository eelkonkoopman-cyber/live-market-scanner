package orderticket

import (
	"context"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// placeOrder: a confirmed order of ~666 sh at EMA 1.50 (ids read from state).
func placeOrder(t *testing.T, r *rig, side string) (pid, cid int64, qty int64) {
	t.Helper()
	p, err := r.tk.Prepare(side, 1000, "SGRX")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatal(err)
	}
	o := r.tk.State().Orders[0]
	return o.ID, o.ChildID, o.Qty
}

func price(r *rig, px float64) {
	r.tk.FeedAgg(core.Agg{Sym: "SGRX", StartMs: minuteOf(r.now.UnixMilli()), Open: px, High: px, Low: px, Close: px, Vol: 1})
}

func view(t *testing.T, r *rig, id int64) OrderView {
	t.Helper()
	for _, o := range r.tk.State().Orders {
		if o.ID == id {
			return o
		}
	}
	t.Fatalf("order %d not shown", id)
	return OrderView{}
}

func cancels(r *rig) []int64 {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	return append([]int64(nil), r.f.cancels...)
}

func exits(r *rig) []placed {
	var out []placed
	for _, x := range r.f.placesCopy() {
		if strings.HasSuffix(x.o.OrderRef, ":stop") {
			out = append(out, x)
		}
	}
	return out
}

func armedLong(t *testing.T) (*rig, int64, int64, int64) {
	r := newRig(t, nil)
	r.ready("SGRX", "111", 1.50)
	pid, cid, q := placeOrder(t, r, "BUY")
	r.f.w.OrderStatus(pid, "Filled", Qty(q), 0, 1.50, 9100, 0, 1.50, 80, "", 0)
	if v := view(t, r, pid); v.StopState != stArmed || !near(v.StopLevel, 1.48) || v.StopLabel != "SL 1.48 (soft)" {
		t.Fatalf("not armed %+v", v)
	}
	return r, pid, cid, q
}

func TestStopLevels(t *testing.T) {
	for _, c := range []struct {
		side            string
		ema, fill, want float64
	}{
		{"BUY", 1.50, 1.52, 1.48},  // 1.485 down
		{"BUY", 1.60, 1.55, 1.53},  // EMA level 1.584 >= fill -> fill x 0.99 = 1.5345 down
		{"BUY", 0, 1.55, 1.53},     // no EMA
		{"SELL", 1.50, 1.48, 1.52}, // 1.515 up
		{"SELL", 1.40, 1.45, 1.47}, // 1.414 <= fill -> fill x 1.01 = 1.4645 up
	} {
		if got := stopLevelFor(c.side, c.ema, c.fill, 1); !near(got, c.want) {
			t.Errorf("%+v: %v", c, got)
		}
	}
}

func TestStopDebounceAndOCAOrdering(t *testing.T) {
	r, pid, cid, q := armedLong(t)
	price(r, 1.47) // one print through
	price(r, 1.49) // back above: reset
	price(r, 1.47)
	if len(cancels(r)) != 0 {
		t.Fatal("single print triggered")
	}
	price(r, 1.40) // second consecutive
	if c := cancels(r); len(c) != 1 || c[0] != cid {
		t.Fatalf("cancels %v (want only the TP %d; the entry is filled)", c, cid)
	}
	if len(exits(r)) != 0 || view(t, r, pid).StopState != stTriggered {
		t.Fatal("exit sent before the TP cancel was confirmed")
	}
	r.tk.Tick(context.Background())
	if len(exits(r)) != 0 {
		t.Fatal("exit sent before confirmation (tick)")
	}
	r.f.w.OrderStatus(cid, "Cancelled", 0, Qty(q), 0, 0, pid, 0, 80, "", 0)
	ex := exits(r)
	if len(ex) != 1 {
		t.Fatalf("exits %+v", ex)
	}
	e := ex[0].o
	if e.Action != "SELL" || e.TotalQuantity.Int() != q || !near(e.LmtPrice, 1.38) || !e.OutsideRTH || e.TIF != "DAY" ||
		e.OrderType != "LMT" || e.OrderRef != "oticket:SGRX:stop" || e.WhatIf || e.ParentID != 0 {
		t.Fatalf("exit %+v", e)
	}
	eid := ex[0].id
	want := []float64{1.37, 1.35, 1.34, 1.33, 1.31} // 1.40 x 0.98 .. 0.94, rounded down
	for i, w := range want {
		r.advance(3 * time.Second)
		r.tk.Tick(context.Background())
		ex = exits(r)
		if len(ex) != i+2 || ex[i+1].id != eid || !near(ex[i+1].o.LmtPrice, w) || ex[i+1].o.TotalQuantity.Int() != q {
			t.Fatalf("re-price %d: %+v", i+1, ex[len(ex)-1])
		}
	}
	r.advance(3 * time.Second)
	r.tk.Tick(context.Background())
	if len(exits(r)) != 6 {
		t.Fatal("more than 5 re-prices")
	}
	r.f.w.OrderStatus(eid, "Filled", Qty(q), 0, 1.33, 0, 0, 1.33, 80, "", 0)
	if v := view(t, r, pid); v.StopState != stDone || v.ExitFilled != q {
		t.Fatalf("%+v", v)
	}
}

func TestStopOneSecondRule(t *testing.T) {
	r, _, cid, _ := armedLong(t)
	price(r, 1.47)
	r.advance(time.Second)
	r.tk.Tick(context.Background())
	if c := cancels(r); len(c) != 1 || c[0] != cid {
		t.Fatalf("1 s rule: %v", c)
	}
}

func TestStopTPFillsDuringCancelNoOversell(t *testing.T) {
	r, pid, cid, q := armedLong(t)
	price(r, 1.40)
	price(r, 1.40)
	r.f.w.OrderStatus(cid, "Filled", Qty(q), 0, 1.65, 0, pid, 1.65, 80, "", 0) // TP won the race
	r.tk.Tick(context.Background())
	if len(exits(r)) != 0 || view(t, r, pid).StopState != stDisarmed {
		t.Fatalf("oversold: %+v", exits(r))
	}
}

func TestStopPartialTPExitsRemainder(t *testing.T) {
	r, pid, cid, q := armedLong(t)
	r.f.w.OrderStatus(cid, "Submitted", 40, Qty(q-40), 1.65, 0, pid, 1.65, 80, "", 0)
	price(r, 1.40)
	price(r, 1.40)
	r.f.w.OrderStatus(cid, "Cancelled", 40, Qty(q-40), 1.65, 0, pid, 1.65, 80, "", 0)
	if ex := exits(r); len(ex) != 1 || ex[0].o.TotalQuantity.Int() != q-40 {
		t.Fatalf("%+v", ex)
	}
}

func TestStopDisarmOnTPFill(t *testing.T) {
	r, pid, cid, q := armedLong(t)
	r.f.w.OrderStatus(cid, "Filled", Qty(q), 0, 1.65, 0, pid, 1.65, 80, "", 0)
	r.tk.Tick(context.Background())
	if v := view(t, r, pid); v.StopState != stDisarmed {
		t.Fatalf("%+v", v)
	}
	price(r, 1.40)
	price(r, 1.40)
	if len(cancels(r)) != 0 || len(exits(r)) != 0 {
		t.Fatal("disarmed stop acted")
	}
}

func TestStopEntryPartiallyFilled(t *testing.T) {
	r := newRig(t, nil)
	r.ready("SGRX", "111", 1.50)
	pid, cid, q := placeOrder(t, r, "BUY")
	r.f.w.OrderStatus(pid, "Submitted", 50, Qty(q-50), 1.50, 9100, 0, 1.50, 80, "", 0)
	price(r, 1.40)
	price(r, 1.40)
	c := cancels(r)
	if len(c) != 2 || c[0] != pid || c[1] != cid {
		t.Fatalf("cancels %v: entry first, then TP", c)
	}
	r.f.w.OrderStatus(cid, "Cancelled", 0, Qty(q), 0, 0, pid, 0, 80, "", 0)
	if len(exits(r)) != 0 {
		t.Fatal("exit while the entry could still fill")
	}
	r.f.w.OrderStatus(pid, "Cancelled", 60, Qty(q-60), 1.50, 9100, 0, 1.50, 80, "", 0) // 10 more filled meanwhile
	if ex := exits(r); len(ex) != 1 || ex[0].o.TotalQuantity.Int() != 60 {
		t.Fatalf("%+v", ex)
	}
}

func TestStopShortAndFollowLowerOnly(t *testing.T) {
	r := newRig(t, nil)
	if err := r.tk.SetStopSettings(context.Background(), 1, true); err != nil {
		t.Fatal(err)
	}
	r.ready("SGRX", "111", 1.50)
	pid, cid, q := placeOrder(t, r, "SELL")
	r.f.w.OrderStatus(pid, "Filled", Qty(q), 0, 1.50, 9100, 0, 1.50, 80, "", 0)
	if v := view(t, r, pid); !near(v.StopLevel, 1.52) {
		t.Fatalf("%+v", v)
	}
	r.closeBar("SGRX", 1.30) // EMA 1.46 -> 1.4746 up 1.48: lowered
	r.tk.Tick(context.Background())
	if v := view(t, r, pid); !near(v.StopLevel, 1.48) {
		t.Fatalf("not lowered %+v", v)
	}
	r.closeBar("SGRX", 1.45) // EMA rises a bit: never raised
	r.tk.Tick(context.Background())
	if v := view(t, r, pid); !near(v.StopLevel, 1.48) {
		t.Fatalf("raised %+v", v)
	}
	price(r, 1.49)
	price(r, 1.50)
	r.f.w.OrderStatus(cid, "Cancelled", 0, Qty(q), 0, 0, pid, 0, 80, "", 0)
	if ex := exits(r); len(ex) != 1 || ex[0].o.Action != "BUY" || !near(ex[0].o.LmtPrice, 1.52) { // 1.50 x 1.01 = 1.515 up
		t.Fatalf("%+v", ex)
	}
}

func TestStopFixedWithoutFollow(t *testing.T) {
	r, pid, _, _ := armedLong(t)
	r.closeBar("SGRX", 1.57) // EMA up, below the +5% breakeven trigger (1.575)
	r.tk.Tick(context.Background())
	if v := view(t, r, pid); !near(v.StopLevel, 1.48) {
		t.Fatalf("moved %+v", v)
	}
}

func TestStopDisableToggleAndUnwatched(t *testing.T) {
	r, pid, _, _ := armedLong(t)
	if err := r.tk.SetStopEnabled(pid, false); err != nil {
		t.Fatal(err)
	}
	price(r, 1.40)
	price(r, 1.40)
	if len(cancels(r)) != 0 || view(t, r, pid).StopOn {
		t.Fatal("disabled stop acted")
	}
	_ = r.tk.SetStopEnabled(pid, true)
	if s := r.tk.State(); s.StopWarn == "" || s.StopUnwatched {
		t.Fatalf("%+v", s)
	}
	r.tk.Disconnect()
	if !r.tk.State().StopUnwatched {
		t.Fatal("no STOP NOT WATCHED flag")
	}
	price(r, 1.40)
	price(r, 1.40)
	if len(cancels(r)) != 0 {
		t.Fatal("acted while disconnected")
	}
}

func TestStopRestartReArms(t *testing.T) {
	aud := &MemAudit{}
	r := newRig(t, aud)
	r.ready("SGRX", "111", 1.50)
	pid, cid, q := placeOrder(t, r, "BUY")
	r.f.w.OrderStatus(pid, "Filled", Qty(q), 0, 1.50, 9100, 0, 1.50, 80, "", 0)
	r.tk.Disconnect()

	r2 := newRig(t, aud)
	r2.f.onOpen = func(w Wrapper) { // parent filled: only the TP is open
		w.OpenOrder(cid, contractFor("SGRX"), &Order{Action: "SELL", OrderType: "LMT", TotalQuantity: Qty(q), LmtPrice: 1.65,
			TIF: "GTC", OrderRef: refPrefix + "SGRX:tp", ParentID: pid, ClientID: 80}, &OrderState{})
	}
	r2.connect()
	r2.seedBars("SGRX", 30, 1.40) // price already below the stop on reconnect
	r2.show("111")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if os := r2.tk.State().Orders; len(os) == 1 && os[0].StopState == stArmed {
			if !near(os[0].StopLevel, 1.48) {
				t.Fatalf("%+v", os[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not re-armed %+v", r2.tk.State().Orders)
		}
		time.Sleep(2 * time.Millisecond)
	}
	r2.tk.Tick(context.Background()) // immediate check (first hit)
	r2.advance(time.Second)
	r2.tk.Tick(context.Background()) // still through after 1 s
	if c := cancels(r2); len(c) != 1 || c[0] != cid {
		t.Fatalf("restart re-arm did not fire: %v", c)
	}
}

// Randomized: whatever order fills / cancels / prices arrive in, the exit
// never sells more than confirmed entry fills minus TP fills.
func TestStopNeverOversellsRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for iter := 0; iter < 150; iter++ {
		r := newRig(t, nil)
		r.ready("SGRX", "111", 1.50)
		pid, cid, q := placeOrder(t, r, "BUY")
		entry := int64(1 + rng.Intn(int(q)))
		entryDone := entry == q
		st := "Submitted"
		if entryDone {
			st = "Filled"
		}
		r.f.w.OrderStatus(pid, st, Qty(entry), Qty(q-entry), 1.50, 9100, 0, 1.50, 80, "", 0)
		tp := int64(0)
		if rng.Intn(2) == 0 {
			tp = int64(rng.Intn(int(entry) + 1))
			r.f.w.OrderStatus(cid, "Submitted", Qty(tp), Qty(q-tp), 1.65, 0, pid, 1.65, 80, "", 0)
		}
		price(r, 1.40)
		price(r, 1.40)
		for step := 0; step < 4; step++ { // racing fills while the cancels are in flight
			switch rng.Intn(3) {
			case 0:
				if !entryDone && entry < q {
					entry += int64(rng.Intn(int(q-entry) + 1))
					r.f.w.OrderStatus(pid, "Submitted", Qty(entry), Qty(q-entry), 1.50, 9100, 0, 1.50, 80, "", 0)
				}
			case 1:
				if tp < entry {
					tp += int64(rng.Intn(int(entry-tp) + 1))
					r.f.w.OrderStatus(cid, "Submitted", Qty(tp), Qty(q-tp), 1.65, 0, pid, 1.65, 80, "", 0)
				}
			case 2:
				r.advance(3 * time.Second)
				r.tk.Tick(context.Background())
			}
		}
		if !entryDone {
			r.f.w.OrderStatus(pid, "Cancelled", Qty(entry), Qty(q-entry), 1.50, 9100, 0, 1.50, 80, "", 0)
		}
		if rng.Intn(3) == 0 {
			tp = entry
			r.f.w.OrderStatus(cid, "Filled", Qty(tp), 0, 1.65, 0, pid, 1.65, 80, "", 0)
		} else {
			r.f.w.OrderStatus(cid, "Cancelled", Qty(tp), Qty(q-tp), 1.65, 0, pid, 1.65, 80, "", 0)
		}
		r.tk.Tick(context.Background())
		ids := map[int64]int64{}
		for _, x := range exits(r) {
			ids[x.id] = x.o.TotalQuantity.Int()
		}
		var sold int64
		for _, n := range ids {
			sold += n
		}
		if sold > entry-tp || len(ids) > 1 {
			t.Fatalf("iter %d: exit %d sh (%d ids) but held %d (entry %d, tp %d)", iter, sold, len(ids), entry-tp, entry, tp)
		}
		if entry-tp > 0 && sold != entry-tp {
			t.Fatalf("iter %d: held %d but exit %d", iter, entry-tp, sold)
		}
		r.tk.Disconnect()
	}
}
