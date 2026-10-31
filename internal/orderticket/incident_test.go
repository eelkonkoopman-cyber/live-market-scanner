package orderticket

// Replays of the 2026-10-29 live trades (ONCO, DAVI) and the fixes made for
// them: aggregate-low trigger, TWS price line, SL blind, TP verification.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

type sec struct{ h, l, c float64 }

// feedSec delivers one per-second aggregate, then 1 s passes (with the loop).
func feedSec(r *rig, s sec) {
	r.tk.FeedAgg(core.Agg{Sym: "SGRX", StartMs: minuteOf(r.now.UnixMilli()), Open: s.c, High: s.h, Low: s.l, Close: s.c, Vol: 100})
	r.advance(time.Second)
	r.tk.Tick(context.Background())
}

// incidentLong: a long filled at avg with a 5% follow stop, EMA ema.
func incidentLong(t *testing.T, ema, avg float64) (*rig, int64, int64, int64) {
	t.Helper()
	r := newRig(t, nil)
	r.ready("SGRX", "111", ema)
	if err := r.tk.SetStopSettings(context.Background(), 5, true); err != nil {
		t.Fatal(err)
	}
	pid, cid, q := placeOrder(t, r, "BUY")
	r.f.w.OrderStatus(pid, "Filled", Qty(q), 0, avg, 9100, 0, avg, 80, "", 0)
	return r, pid, cid, q
}

// DAVI 07:37:00-07:37:11 ET (Massive 1 s aggregates), stop 3.26.
var daviSecs = []sec{
	{3.35, 3.34, 3.34}, {3.3395, 3.32, 3.32}, {3.32, 3.30, 3.30}, {3.31, 3.30, 3.30}, {3.32, 3.31, 3.32},
	{3.33, 3.30, 3.32}, {3.33, 3.309, 3.33}, {3.306, 3.306, 3.306},
	{3.301, 3.26, 3.2607}, {3.27, 3.26, 3.26}, {3.33, 3.26, 3.30}, {3.34, 3.29, 3.32},
}

func TestReplayDAVICloseRuleMissedLowRuleTriggers(t *testing.T) {
	r, pid, cid, _ := incidentLong(t, 3.432, 3.44)
	_ = cid
	if v := view(t, r, pid); !near(v.StopLevel, 3.26) {
		t.Fatalf("stop %v, want 3.26 as on 2026-10-29", v.StopLevel)
	}

	// the same seconds with their real lows: 3.26, 3.26, 3.26 -> triggers
	r, pid, cid, _ = incidentLong(t, 3.432, 3.44)
	for i, s := range daviSecs {
		r.tk.FeedAgg(core.Agg{Sym: "SGRX", StartMs: minuteOf(r.now.UnixMilli()), Open: s.c, High: s.h, Low: s.l, Close: s.c, Vol: 100})
		if i == 8 && len(cancels(r)) != 0 {
			t.Fatal("one wick second must not trigger")
		}
		r.advance(900 * time.Millisecond)
		r.tk.Tick(context.Background())
		r.advance(100 * time.Millisecond)
	}
	if c := cancels(r); len(c) == 0 || c[0] != cid || view(t, r, pid).StopState != stTriggered {
		t.Fatalf("low rule did not trigger: cancels %v state %s", c, view(t, r, pid).StopState)
	}
}

// Close rule, real arrival phase (aggregates at .24, loop at .14): one close at
// the stop, the next back above before 1 s has passed -> no trigger. This is
// the DAVI miss, kept as documentation of why the low is used now.
func TestReplayDAVICloseOnlyMisses(t *testing.T) {
	r, _, _, _ := incidentLong(t, 3.432, 3.44)
	for _, s := range daviSecs {
		r.tk.FeedAgg(core.Agg{Sym: "SGRX", StartMs: minuteOf(r.now.UnixMilli()), Open: s.c, High: s.c, Low: s.c, Close: s.c, Vol: 100})
		r.advance(900 * time.Millisecond)
		r.tk.Tick(context.Background())
		r.advance(100 * time.Millisecond)
	}
	if len(cancels(r)) != 0 {
		t.Fatal("close-only sequence triggered; the replay no longer matches 2026-10-29")
	}
}

// ONCO: avg 0.6468, breakeven moved the stop to 0.6468; then TWS echoed the TP
// at 0.64 (a limit the ticket never sent) and reported it Filled at 0.64 a
// moment before the 07:08:12 aggregate (low 0.64) reached the ticket.
func TestReplayONCORogueTPEchoIsNotAdoptedAndNeverOversells(t *testing.T) {
	r, pid, cid, q := incidentLong(t, 0.646, 0.6468)
	feedSec(r, sec{0.6844, 0.6827, 0.6827}) // 07:07:04: +5% -> breakeven
	v := view(t, r, pid)
	if !v.StopBE || !near(v.StopLevel, 0.6468) {
		t.Fatalf("breakeven %+v", v)
	}
	r.tk.mu.Lock()
	o := r.tk.orders[pid]
	want := o.TP
	ch := *r.tk.tpOrder(o)
	r.tk.mu.Unlock()
	if !(want > 0.6468) {
		t.Fatalf("intended TP %v", want)
	}
	r.advance(5 * time.Second)
	nBefore := len(r.f.placesCopy())

	ch.LmtPrice = 0.64 // TWS reports the TP at 0.64
	r.f.w.OpenOrder(cid, contractFor("SGRX"), &ch, &OrderState{Status: "Submitted"})
	pl := r.f.placesCopy()
	if len(pl) != nBefore+1 || pl[len(pl)-1].id != cid || !near(pl[len(pl)-1].o.LmtPrice, want) {
		t.Fatalf("TP not re-sent at %v: %+v", want, pl[nBefore:])
	}
	if a := r.tk.State().StopAlert; !strings.Contains(a, "TP MISMATCH") || !strings.Contains(a, "0.64") {
		t.Fatalf("alert %q", a)
	}

	// ... and Filled at 0.64: below every TP the ticket sent -> NOT confirmed
	r.f.w.OrderStatus(cid, "Filled", Qty(q), 0, 0.64, 9101, pid, 0.64, 80, "", 0)
	if v := view(t, r, pid); v.StopState != stArmed {
		t.Fatalf("unproven TP fill disarmed the stop: %s", v.StopState)
	}
	feedSec(r, sec{0.6517, 0.64, 0.6466}) // 07:08:12
	feedSec(r, sec{0.6442, 0.64, 0.6403}) // 07:08:13
	for i := 0; i < 5; i++ {
		r.advance(time.Second)
		r.tk.Tick(context.Background())
	}
	if len(exits(r)) != 0 {
		t.Fatal("sold while the TP fill was unproven (oversell risk)")
	}
	if !strings.Contains(r.tk.State().StopAlert, "NOT confirmed") {
		t.Fatalf("alert %q", r.tk.State().StopAlert)
	}
}

// The TWS price line: subscribed while the stop works, triggers ahead of the
// ~3 s aggregates, a late aggregate never resets it, cancelled when done.
func TestTWSPriceLineTriggersFirstAndIsCancelled(t *testing.T) {
	r, pid, cid, q := armedLong(t) // stop 1.48
	r.tk.Tick(context.Background())
	var line int64
	reqs, _ := mdCalls(r)
	for _, m := range reqs {
		if m.generic == "" && m.sym == "SGRX" && m.id >= stopMDBase {
			line = m.id
		}
	}
	if line == 0 {
		t.Fatalf("no stop price line: %+v", reqs)
	}
	r.f.w.TickPrice(line, tickBid, 1.40) // a quote never triggers
	r.f.w.TickPrice(line, tickBid, 1.40)
	if len(cancels(r)) != 0 {
		t.Fatal("bid triggered")
	}
	r.f.w.TickPrice(line, tickLast, 1.47)
	price(r, 1.49) // a ~3 s old aggregate above the stop: must not reset TWS's count
	r.f.w.TickPrice(line, tickLast, 1.46)
	if c := cancels(r); len(c) != 1 || c[0] != cid {
		t.Fatalf("TWS ticks did not trigger: %v", c)
	}
	if !strings.Contains(view(t, r, pid).Note, "TWS") {
		t.Fatalf("note %q", view(t, r, pid).Note)
	}
	r.f.w.OrderStatus(cid, "Cancelled", 0, Qty(q), 0, 0, pid, 0, 80, "", 0)
	ex := exits(r)
	if len(ex) != 1 || ex[0].o.TotalQuantity.Int() != q || !near(ex[0].o.LmtPrice, 1.44) { // 1.46 x 0.99, the fresh TWS last
		t.Fatalf("exit %+v", ex)
	}
	r.f.w.OrderStatus(ex[0].id, "Filled", Qty(q), 0, 1.45, 0, 0, 1.45, 80, "", 0)
	r.tk.Tick(context.Background())
	if _, cans := mdCalls(r); len(cans) == 0 || cans[len(cans)-1] != line {
		t.Fatalf("line not cancelled after the stop finished: %v", cans)
	}
}

func TestStopBlindAlertAndAudit(t *testing.T) {
	r, _, _, _ := armedLong(t)
	r.advance(2 * time.Second)
	r.tk.Tick(context.Background())
	if strings.Contains(r.tk.State().StopAlert, "blind") {
		t.Fatal("blind too early")
	}
	r.advance(2 * time.Second)
	r.tk.Tick(context.Background())
	if a := r.tk.State().StopAlert; !strings.Contains(a, "SL blind: SGRX no price for 4 s") {
		t.Fatalf("alert %q", a)
	}
	r.advance(time.Second)
	r.tk.Tick(context.Background()) // still blind: no second record
	price(r, 1.50)
	r.tk.Tick(context.Background())
	if a := r.tk.State().StopAlert; strings.Contains(a, "blind") {
		t.Fatalf("not cleared %q", a)
	}
	var starts, ends int
	for _, a := range r.aud.Recs {
		if strings.HasPrefix(a.Note, "SL blind:") {
			starts++
		}
		if strings.HasPrefix(a.Note, "SL price back") {
			ends++
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("audit starts %d ends %d", starts, ends)
	}
}

func TestStopPriceLinesCapped(t *testing.T) {
	r := newRig(t, nil)
	r.connect()
	r.tk.mu.Lock()
	for i := 0; i < maxStopMD+5; i++ {
		id := int64(5000 + i)
		r.tk.orders[id] = &ord{ID: id, Sym: string(rune('A'+i)) + "X", Side: "BUY", stop: stop{StopState: stArmed}}
	}
	acts := r.tk.syncStopMDLocked()
	r.tk.mu.Unlock()
	run(acts)
	n := 0
	reqs, _ := mdCalls(r)
	for _, m := range reqs {
		if m.id >= stopMDBase {
			n++
		}
	}
	if n != maxStopMD {
		t.Fatalf("%d lines", n)
	}
}

// The TP is re-sent at fill x (1 + tp%) on the first fill, never sent at or
// below the fill, and an unechoed modify asks TWS, then alerts.
func TestTPFillResendGuardAndVerify(t *testing.T) {
	r, pid, cid, _ := armedLong(t) // avg 1.50
	r.tk.mu.Lock()
	o := r.tk.orders[pid]
	want := o.tpAt(1.50, o.StopLevel)
	r.tk.mu.Unlock()
	pl := r.f.placesCopy()
	if last := pl[len(pl)-1]; last.id != cid || !near(last.o.LmtPrice, want) || last.o.ParentID != pid || last.o.Action != "SELL" {
		t.Fatalf("no TP re-send at the fill: %+v", last)
	}
	r.tk.mu.Lock()
	o.TP = 1.49 // something asks for a TP below the fill
	acts := r.tk.sendTPLocked(o, "test")
	r.tk.mu.Unlock()
	run(acts)
	pl = r.f.placesCopy()
	if !near(pl[len(pl)-1].o.LmtPrice, want) {
		t.Fatalf("TP sent through the fill: %v", pl[len(pl)-1].o.LmtPrice)
	}
	// no echo: reqOpenOrders after 5 s and 10 s, then a red alert
	for i := 0; i < 16; i++ {
		r.advance(time.Second)
		r.tk.Tick(context.Background())
	}
	r.f.mu.Lock()
	asks := r.f.openReqs
	r.f.mu.Unlock()
	if asks < 2 || !strings.Contains(r.tk.State().StopAlert, "NOT confirmed by TWS") {
		t.Fatalf("asks %d alert %q", asks, r.tk.State().StopAlert)
	}
	// the echo with the right limit clears it
	r.tk.mu.Lock()
	ch := *r.tk.tpOrder(o)
	r.tk.mu.Unlock()
	r.f.w.OpenOrder(cid, contractFor("SGRX"), &ch, &OrderState{Status: "Submitted"})
	if a := r.tk.State().StopAlert; strings.Contains(a, "TP") {
		t.Fatalf("not cleared %q", a)
	}
}
