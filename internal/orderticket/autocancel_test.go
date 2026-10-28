package orderticket

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// trendRig: closed-candle mode (isolates the conditions), 30 closed bars from
// `from` stepping by `step`; the ticket shows SGRX and has one entry working.
func trendRig(t *testing.T, side string, from, step float64) (*rig, int64, int64) {
	t.Helper()
	r := newRig(t, nil)
	r.tk.settings.EMALive = false
	r.connect()
	cur := minuteOf(r.now.UnixMilli())
	var bars []core.Bar
	for i := 30; i >= 1; i-- {
		c := from + step*float64(30-i)
		bars = append(bars, core.Bar{T: cur - int64(i)*60000, O: c, H: c, L: c, C: c, V: 100})
	}
	r.tk.SeedBars("SGRX", bars)
	r.show("111")
	pid, cid, _ := placeOrder(t, r, side)
	return r, pid, cid
}

func emaNow(r *rig) float64 {
	r.tk.mu.Lock()
	defer r.tk.mu.Unlock()
	return r.tk.book.quoteMode("SGRX", r.now, false).EMA
}

func TestDropCancelBuyNeedsAllThree(t *testing.T) {
	r, pid, cid := trendRig(t, "BUY", 2.00, -0.01) // falling 9EMA ~1.75
	e := emaNow(r)
	price(r, 1.90)   // the recent high
	price(r, e*1.03) // dropped >3% but still 3% above the EMA: not near
	price(r, e*1.03)
	if len(cancels(r)) != 0 {
		t.Fatal("cancelled while not near the 9EMA")
	}
	price(r, e*1.005) // near + dropped + falling: first hit
	if len(cancels(r)) != 0 {
		t.Fatal("no debounce")
	}
	price(r, e*1.004) // second consecutive
	c := cancels(r)
	if len(c) != 2 || c[0] != pid || c[1] != cid {
		t.Fatalf("cancels %v (entry + TP)", c)
	}
	s := r.tk.State()
	if !strings.HasPrefix(s.Note, "Cancelled SGRX entry: 9EMA falling + price -") || !strings.Contains(s.Note, "in 60 s (near 9EMA)") {
		t.Fatalf("banner %q", s.Note)
	}
	got := false
	for _, a := range r.aud.Recs {
		if a.Ev == "autocancel" && a.ID == pid && strings.Contains(a.Note, "move -") {
			got = true
		}
	}
	if !got {
		t.Fatalf("audit %+v", r.aud.Recs)
	}
	// following stopped: no re-price into the drop
	before := len(r.f.placesCopy())
	r.advance(10 * time.Second)
	r.tk.Tick(context.Background())
	if len(r.f.placesCopy()) != before {
		t.Fatal("re-priced after the auto-cancel")
	}
}

func TestDropCancelNeedsFallingEMAAndDrop(t *testing.T) {
	// rising EMA: no cancel even on a fast drop to it
	r, _, _ := trendRig(t, "BUY", 1.00, 0.01)
	e := emaNow(r)
	price(r, e*1.10)
	price(r, e)
	price(r, e)
	if len(cancels(r)) != 0 {
		t.Fatal("cancelled with a rising 9EMA")
	}
	// falling EMA, near, but no fast drop (only 1%)
	r2, _, _ := trendRig(t, "BUY", 2.00, -0.01)
	e = emaNow(r2)
	price(r2, e*1.01)
	price(r2, e)
	price(r2, e)
	if len(cancels(r2)) != 0 {
		t.Fatal("cancelled without a fast drop")
	}
	// the drop is older than DropSec: no
	r3, _, _ := trendRig(t, "BUY", 2.00, -0.01)
	e = emaNow(r3)
	price(r3, e*1.10)
	r3.advance(61 * time.Second)
	price(r3, e)
	price(r3, e)
	if len(cancels(r3)) != 0 {
		t.Fatal("drop outside the window counted")
	}
}

func TestDropCancelShortMirror(t *testing.T) {
	r, pid, cid := trendRig(t, "SELL", 1.00, 0.01) // rising 9EMA ~1.25
	e := emaNow(r)
	price(r, e*0.90) // the recent low
	price(r, e*0.996)
	price(r, e*0.997) // spiked >3% up into the EMA
	if c := cancels(r); len(c) != 2 || c[0] != pid || c[1] != cid {
		t.Fatalf("short cancels %v", c)
	}
	if n := r.tk.State().Note; !strings.Contains(n, "9EMA rising + price +") {
		t.Fatalf("%q", n)
	}
}

func TestDropCancelOneSecondRule(t *testing.T) {
	r, pid, _ := trendRig(t, "BUY", 2.00, -0.01)
	e := emaNow(r)
	price(r, 1.90)
	price(r, e) // one hit
	r.advance(time.Second)
	r.tk.Tick(context.Background())
	if c := cancels(r); len(c) == 0 || c[0] != pid {
		t.Fatalf("1 s rule %v", c)
	}
}

func TestDropCancelPartialAndFilled(t *testing.T) {
	r, pid, _ := trendRig(t, "BUY", 2.00, -0.01)
	o := view(t, r, pid)
	r.f.w.OrderStatus(pid, "Submitted", 100, Qty(o.Qty-100), o.Limit, 9100, 0, o.Limit, 80, "", 0)
	e := emaNow(r)
	price(r, 1.90)
	price(r, e)
	price(r, e)
	if c := cancels(r); len(c) != 1 || c[0] != pid {
		t.Fatalf("partial: %v (remainder only, TP kept)", c)
	}
	if v := view(t, r, pid); v.StopState != stArmed {
		t.Fatalf("soft stop dropped for the filled shares: %+v", v)
	}
	// fully filled: never
	r2, pid2, _ := trendRig(t, "BUY", 2.00, -0.01)
	o2 := view(t, r2, pid2)
	r2.f.w.OrderStatus(pid2, "Filled", Qty(o2.Qty), 0, o2.Limit, 9100, 0, o2.Limit, 80, "", 0)
	price(r2, 1.90)
	price(r2, e)
	price(r2, e)
	for _, id := range cancels(r2) {
		if id == pid2 {
			t.Fatal("filled entry cancelled")
		}
	}
}

func TestDropCancelDisabledAndSettings(t *testing.T) {
	r, _, _ := trendRig(t, "BUY", 2.00, -0.01)
	ctx := context.Background()
	if err := r.tk.SetDropCancel(ctx, false, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	e := emaNow(r)
	price(r, 1.90)
	price(r, e)
	price(r, e)
	if len(cancels(r)) != 0 {
		t.Fatal("disabled guard cancelled")
	}
	for _, bad := range []struct {
		pct  float64
		sec  int
		near float64
	}{{0.4, 60, 1}, {21, 60, 1}, {3, 9, 1}, {3, 301, 1}, {3, 60, 5.5}, {3, 60, -1}} {
		if err := r.tk.SetDropCancel(ctx, true, bad.pct, bad.sec, bad.near); err == nil {
			t.Fatalf("%+v accepted", bad)
		}
	}
	if err := r.tk.SetDropCancel(ctx, true, 5, 120, 0); err != nil || r.tk.settings.NearPct != 0 || r.tk.settings.DropCancelOff {
		t.Fatalf("%v %+v", err, r.tk.settings)
	}
	if d := DefaultSettings(); d.DropCancelOff || d.DropPct != 3 || d.DropSec != 60 || d.NearPct != 1 {
		t.Fatalf("defaults %+v", d)
	}
}
