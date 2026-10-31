package orderticket

// VWAP X (vwapx.go): availability rules, level arithmetic, sizing, the 3-order
// bracket on the wire (fake client only), and how every guard treats it.

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// vwapxRig: SGRX shown, 30 flat bars at 27.95, and a bull cross 3 minutes ago
// (candle 10:27 ET, closed): O 27.90 H 28.05 L 27.81 C 27.99. now = 10:30:30 ET.
func vwapxRig(t *testing.T, aud *MemAudit) (*rig, int64) {
	t.Helper()
	r := newRig(t, aud)
	r.ready("SGRX", "111", 27.95)
	cross := minuteOf(r.now.UnixMilli()) - 3*60000
	r.tk.SeedBars("SGRX", []core.Bar{{T: cross, O: 27.90, H: 28.05, L: 27.81, C: 27.99, V: 1_000_000_000, VW: 27.80}})
	r.tk.FeedAgg(core.Agg{Sym: "SGRX", StartMs: r.now.UnixMilli(), Open: 27.99, High: 27.99, Low: 27.99, Close: 27.99, Vol: 1})
	r.mu.Lock()
	r.cross = map[string]int64{"SGRX": cross}
	r.mu.Unlock()
	return r, cross
}

func vx(r *rig) *Vwapx { return r.tk.State().Vwapx }

func TestVwapxLevelArithmetic(t *testing.T) {
	e, s, tp := vwapxLevels(10.20, 10.10) // 10c risk, 30c target
	if !near(e, 10.20) || !near(s, 10.10) || !near(tp, 10.50) {
		t.Fatalf("levels %v %v %v", e, s, tp)
	}
	if e, s, tp = vwapxLevels(0.51, 0.50); !near(e, 0.51) || !near(s, 0.50) || !near(tp, 0.54) {
		t.Fatalf("sub-dollar levels %v %v %v", e, s, tp)
	}
}

func TestBookVwap(t *testing.T) {
	r := newRig(t, nil)
	r.ready("SGRX", "111", 27.95)
	m := minuteOf(r.now.UnixMilli())
	r.tk.SeedBars("SGRX", []core.Bar{{T: m - 60000, H: 10, L: 10, C: 10, V: 1_000_000_000, VW: 10}, {T: m - 24*3600000, H: 99, L: 99, C: 99, V: 1_000_000_000, VW: 99}})
	if v := r.tk.book.vwap("SGRX", r.now); v < 9.9 || v > 10.1 {
		t.Fatalf("vwap %v: yesterday must not count", v)
	}
}

func TestVwapxStateRules(t *testing.T) {
	r := newRig(t, nil)
	r.ready("SGRX", "111", 27.95)
	if v := vx(r); v.Available || v.Reason != "no VWAP bull cross today" {
		t.Fatalf("no cross: %+v", v)
	}
	set := func(ms int64) { r.mu.Lock(); r.cross = map[string]int64{"SGRX": ms}; r.mu.Unlock() }
	now := minuteOf(r.now.UnixMilli())

	// forming: the cross candle is the current minute (30 s old)
	set(now)
	if v := vx(r); v.Available || v.ClosesInSec != 30 || v.Reason != "cross candle closes in 30s" || v.CrossMs != now {
		t.Fatalf("forming: %+v", v)
	}
	r.advance(25 * time.Second)
	if v := vx(r); v.ClosesInSec != 5 {
		t.Fatalf("countdown: %+v", v)
	}
	r.advance(-25 * time.Second)

	// old
	set(now - 15*60000)
	if v := vx(r); v.Available || v.Reason != "cross is 15m old" {
		t.Fatalf("old: %+v", v)
	}

	// 9m ago: allowed; the levels follow the price, the VWAP is the stop
	set(now - 9*60000)
	r.tk.SeedBars("SGRX", []core.Bar{{T: now - 9*60000, H: 28, L: 28, C: 28, V: 1_000_000_000, VW: 28}})
	r.tk.FeedAgg(core.Agg{Sym: "SGRX", StartMs: now, Open: 28.3, High: 28.3, Low: 28.3, Close: 28.3, Vol: 1})
	if v := vx(r); !v.Available {
		t.Fatalf("right side: %+v", v)
	}
	// wrong side of the VWAP
	r.tk.FeedAgg(core.Agg{Sym: "SGRX", StartMs: now, Open: 27.7, High: 27.7, Low: 27.7, Close: 27.7, Vol: 1})
	if v := vx(r); v.Available || v.Reason != "price is not above the VWAP" {
		t.Fatalf("wrong side: %+v", v)
	}
}

func TestVwapxUnavailableWithoutSource(t *testing.T) {
	r := newRig(t, nil)
	r.tk.cfg.LatestVwapBull = nil
	if v := r.tk.State().Vwapx; v == nil || v.Available || v.Reason == "" {
		t.Fatalf("%+v", v)
	}
	if _, err := r.tk.PrepareVwapx(0, 100, "", 0); err == nil {
		t.Fatal("prepared without a connection / source")
	}
}

func TestVwapxPrepareTextAndSizing(t *testing.T) {
	r, cross := vwapxRig(t, nil)

	p, err := r.tk.PrepareVwapx(0, 120, "SGRX", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := "BUY 120 SGRX LMT 27.99 (VWAP X 10:27) · STP 27.80 (VWAP) · TP 3R 28.56 · ~$3,359, risk $23"
	if p.Kind != kindVwapx || p.Text != want || p.CrossMs != cross || p.Qty != 120 || !near(p.Limit, 27.99) || !near(p.PlannedStop, 27.80) || !near(p.TP, 28.56) || !p.StopOff {
		t.Fatalf("pending %q\n%+v", p.Text, p)
	}
	if p.Margin == nil || p.Margin.Status != "ok" {
		t.Fatalf("margin %+v", p.Margin)
	}
	if len(r.f.placesCopy()) != 0 {
		t.Fatal("prepare must not place anything")
	}
	if len(r.f.whatIfsCopy()) != 1 {
		t.Fatalf("what-if %d", len(r.f.whatIfsCopy()))
	}

	// position $: floor(3000 / 27.99) = 107
	if p, err = r.tk.PrepareVwapx(3000, 0, "SGRX", 0); err != nil || p.Qty != 107 {
		t.Fatalf("position sizing %+v %v", p, err)
	}
	// risk $: 50 / (27.99 - 27.80) = 263, trimmed by max position $5000 / 27.99 = 178
	if p, err = r.tk.PrepareVwapx(0, 0, "SGRX", 50); err != nil || p.Qty != 178 || p.Bound != "max position $" {
		t.Fatalf("risk sizing %+v %v", p, err)
	}
	// risk $ 20 / 0.19 = 105, uncapped
	if p, err = r.tk.PrepareVwapx(0, 0, "SGRX", 20); err != nil || p.Qty != 105 || p.Bound != "" || !near(p.Risk, 105*0.19) {
		t.Fatalf("risk sizing %+v %v", p, err)
	}
	// shares above the max position $ are refused, as in the normal flow
	if _, err = r.tk.PrepareVwapx(0, 500, "SGRX", 0); err == nil || !strings.Contains(err.Error(), "above the max") {
		t.Fatalf("over max: %v", err)
	}
	if _, err = r.tk.PrepareVwapx(0, 0, "SGRX", 0); err == nil {
		t.Fatal("no size given")
	}
	if _, err = r.tk.PrepareVwapx(0, 10, "ABCD", 0); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("symbol mismatch: %v", err)
	}
}

func TestVwapxPrepareRefusedWhenUnavailable(t *testing.T) {
	r, cross := vwapxRig(t, nil)
	r.mu.Lock()
	r.cross["SGRX"] = minuteOf(r.now.UnixMilli()) // forming
	r.mu.Unlock()
	if _, err := r.tk.PrepareVwapx(0, 10, "SGRX", 0); err == nil || !strings.Contains(err.Error(), "closes in") {
		t.Fatalf("forming: %v", err)
	}
	r.mu.Lock()
	r.cross["SGRX"] = cross - 20*60000
	r.mu.Unlock()
	if _, err := r.tk.PrepareVwapx(0, 10, "SGRX", 0); err == nil || !strings.Contains(err.Error(), "old") {
		t.Fatalf("old: %v", err)
	}
	if r.tk.State().Pending != nil {
		t.Fatal("a refused prepare left a pending")
	}
}

// sent: prepare + confirm 120 sh; returns the three ids.
func sentVwapx(t *testing.T, r *rig) (pid, tpID, slID int64) {
	t.Helper()
	p, err := r.tk.PrepareVwapx(0, 120, "SGRX", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatal(err)
	}
	o := r.tk.State().Orders[0]
	return o.ID, o.ChildID, o.SLID
}

func TestVwapxConfirmSendsTheBracket(t *testing.T) {
	aud := &MemAudit{}
	r, _ := vwapxRig(t, aud)
	p, err := r.tk.PrepareVwapx(0, 120, "SGRX", 0)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(r.f.placesCopy()); n != 0 {
		t.Fatalf("%d orders before Confirm", n)
	}
	if err := r.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatal(err)
	}
	got := r.f.placesCopy()
	if len(got) != 3 {
		t.Fatalf("placeOrder x%d, want 3: %+v", len(got), got)
	}
	par, tp, sl := got[0], got[1], got[2]
	pid := par.id
	if tp.id != pid+1 || sl.id != pid+2 {
		t.Fatalf("ids %d %d %d", par.id, tp.id, sl.id)
	}
	grp := "vwapx-" + strconv.FormatInt(pid, 10)
	// 1 parent: BUY LMT 120 @ 27.99, DAY, outsideRth, Transmit=false
	if o := par.o; o.Action != "BUY" || o.OrderType != "LMT" || o.TotalQuantity != 120 || !near(o.LmtPrice, 27.99) || o.TIF != "DAY" ||
		!o.OutsideRTH || o.Transmit || o.ParentID != 0 || o.OcaGroup != "" || o.AuxPrice != 0 || o.OrderRef != "oticket:SGRX" || o.Account != "U1234567" {
		t.Fatalf("parent %+v", o)
	}
	// 2 TP child: SELL LMT 120 @ 28.56, ParentID, Transmit=false, OCA
	if o := tp.o; o.Action != "SELL" || o.OrderType != "LMT" || o.TotalQuantity != 120 || !near(o.LmtPrice, 28.56) || o.TIF != "DAY" ||
		!o.OutsideRTH || o.Transmit || o.ParentID != pid || o.OcaGroup != grp || o.OcaType != 1 || o.OrderRef != "oticket:SGRX:tp" {
		t.Fatalf("tp %+v", o)
	}
	// 3 stop child: SELL STP 120 aux 27.80, ParentID, Transmit=TRUE (last), OCA
	if o := sl.o; o.Action != "SELL" || o.OrderType != "STP" || o.TotalQuantity != 120 || !near(o.AuxPrice, 27.80) || o.LmtPrice != 0 || o.TIF != "DAY" ||
		!o.OutsideRTH || !o.Transmit || o.ParentID != pid || o.OcaGroup != grp || o.OcaType != 1 || o.OrderRef != "oticket:SGRX:sl" {
		t.Fatalf("stop %+v", o)
	}
	for _, x := range got {
		if x.c.Symbol != "SGRX" || x.c.SecType != "STK" || x.c.Exchange != "SMART" || x.c.Currency != "USD" || x.o.WhatIf {
			t.Fatalf("contract/whatif %+v", x)
		}
	}

	// the order is shown with its IB stop; no soft stop anywhere
	v := r.tk.State().Orders[0]
	if v.Kind != kindVwapx || v.SLID != sl.id || !near(v.SL, 27.80) || v.SLStatus != "Sent" || v.StopOn || v.StopState != "" || v.Following || v.StopLabel != "SL 27.80 (IB stop)" || !v.Active || v.CanBE {
		t.Fatalf("view %+v", v)
	}

	// audit: kind on all three ids, a place record each
	places := map[int64]auditRec{}
	for _, a := range aud.Recs {
		if a.Ev == "place" {
			places[a.ID] = a
		}
	}
	if len(places) != 3 || places[pid].Kind != kindVwapx || places[tp.id].Kind != kindVwapx || places[sl.id].Kind != kindVwapx ||
		places[sl.id].ParentID != pid || !near(places[sl.id].Limit, 27.80) {
		t.Fatalf("audit %+v", aud.Recs)
	}

	// single use: a second Confirm with the same token sends nothing
	if err := r.tk.Confirm(p.Token, "SGRX"); err == nil {
		t.Fatal("token reused")
	}
	if n := len(r.f.placesCopy()); n != 3 {
		t.Fatalf("second confirm sent orders: %d", n)
	}
}

func TestVwapxConfirmRefusedWhenThingsChange(t *testing.T) {
	// the price falls through the stop between prepare and Confirm
	r, _ := vwapxRig(t, nil)
	p, err := r.tk.PrepareVwapx(0, 120, "SGRX", 0)
	if err != nil {
		t.Fatal(err)
	}
	r.tk.FeedAgg(core.Agg{Sym: "SGRX", StartMs: minuteOf(r.now.UnixMilli()), Open: 27.7, High: 27.7, Low: 27.7, Close: 27.70, Vol: 1})
	if err := r.tk.Confirm(p.Token, "SGRX"); err == nil || !strings.Contains(err.Error(), "no longer valid") {
		t.Fatalf("confirm: %v", err)
	}
	if n := len(r.f.placesCopy()); n != 0 {
		t.Fatalf("%d orders sent", n)
	}

	// a newer cross replaces the one the line was built on
	r, cross := vwapxRig(t, nil)
	p, _ = r.tk.PrepareVwapx(0, 120, "SGRX", 0)
	r.tk.SeedBars("SGRX", []core.Bar{{T: cross + 60000, O: 27.9, H: 28.0, L: 27.85, C: 27.95, V: 100}})
	r.mu.Lock()
	r.cross["SGRX"] = cross + 60000
	r.mu.Unlock()
	if err := r.tk.Confirm(p.Token, "SGRX"); err == nil || !strings.Contains(err.Error(), "new cross") {
		t.Fatalf("confirm: %v", err)
	}
	if n := len(r.f.placesCopy()); n != 0 {
		t.Fatalf("%d orders sent", n)
	}

	// expired token
	r, _ = vwapxRig(t, nil)
	p, _ = r.tk.PrepareVwapx(0, 120, "SGRX", 0)
	r.advance(confirmTTL + time.Second)
	if err := r.tk.Confirm(p.Token, "SGRX"); err == nil {
		t.Fatal("expired token accepted")
	}
	if n := len(r.f.placesCopy()); n != 0 {
		t.Fatalf("%d orders sent", n)
	}
}

func TestVwapxNoSoftStopNoFollowNoTPResend(t *testing.T) {
	r, _ := vwapxRig(t, nil)
	pid, tpID, slID := sentVwapx(t, r)
	before := len(r.f.placesCopy())

	// the 9EMA moves and time passes: the entry is never re-priced
	for i := 0; i < 4; i++ {
		r.closeBar("SGRX", 27.95+float64(i)*0.05)
		r.tk.Tick(context.Background())
	}
	if n := len(r.f.placesCopy()); n != before {
		t.Fatalf("entry/TP modified: %d -> %d", before, n)
	}

	// full fill at a better price than the limit: TP stays, no soft stop arms
	r.f.w.OrderStatus(pid, "Filled", 120, 0, 27.97, 9100, 0, 27.97, 80, "", 0)
	r.f.w.OrderStatus(tpID, "PreSubmitted", 0, 120, 0, 9101, pid, 0, 80, "", 0)
	r.f.w.OrderStatus(slID, "PreSubmitted", 0, 120, 0, 9102, pid, 0, 80, "", 0)
	v := r.tk.State().Orders[0]
	if v.StopState != "" || v.StopOn || !near(v.TP, 28.56) || v.SLStatus != "PreSubmitted" || v.ChildStatus != "PreSubmitted" || v.CanBE {
		t.Fatalf("after fill %+v", v)
	}
	if n := len(r.f.placesCopy()); n != before {
		t.Fatalf("fill re-sent an order: %d -> %d", before, n)
	}

	// a price through the (non-existent) soft stop level does nothing here
	for i := 0; i < 5; i++ {
		price(r, 27.00)
		r.advance(time.Second)
		r.tk.Tick(context.Background())
	}
	if c := cancels(r); len(c) != 0 || len(r.f.placesCopy()) != before {
		t.Fatalf("something reacted to the price: cancels %v places %d", c, len(r.f.placesCopy()))
	}
	// the user cannot bolt a soft stop on, and "→ BE" is not offered
	if err := r.tk.SetStopEnabled(pid, true); err == nil {
		t.Fatal("soft stop enabled on a VWAP X order")
	}
	if err := r.tk.MoveStopToEntry(pid); err == nil {
		t.Fatal("→ BE accepted on a VWAP X order")
	}
	if s := r.tk.State(); s.BECount != 0 || s.StopWarn != "" || s.StopUnwatched {
		t.Fatalf("state %+v", s)
	}
}

func TestVwapxStopFillAndTPFillBookkeeping(t *testing.T) {
	r, _ := vwapxRig(t, nil)
	pid, tpID, slID := sentVwapx(t, r)
	r.f.w.OrderStatus(pid, "Filled", 120, 0, 27.99, 9100, 0, 27.99, 80, "", 0)
	// the real stop is hit; IB cancels the TP
	r.f.w.OrderStatus(slID, "Filled", 120, 0, 27.79, 9102, pid, 27.79, 80, "", 0)
	r.f.w.OrderStatus(tpID, "Cancelled", 0, 120, 0, 9101, pid, 0, 80, "", 0)
	v := r.tk.State().Orders[0]
	if v.SLStatus != "Filled" || v.SLFilled != 120 || v.ChildStatus != "Cancelled" || v.Active {
		t.Fatalf("view %+v", v)
	}
	if held, _ := r.tk.closeQtyLocked2("SGRX"); held != 0 {
		t.Fatalf("held %d after the stop sold everything", held)
	}
}

// closeQtyLocked2 is closeQtyLocked taking the lock (tests).
func (t *Ticket) closeQtyLocked2(sym string) (int64, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closeQtyLocked(sym)
}

func TestVwapxCancelUnfilledCancelsAllThree(t *testing.T) {
	r, _ := vwapxRig(t, nil)
	pid, tpID, slID := sentVwapx(t, r)
	if _, err := r.tk.CancelSymbol("SGRX"); err != nil {
		t.Fatal(err)
	}
	c := cancels(r)
	if len(c) != 3 || c[0] != pid || c[1] != slID || c[2] != tpID {
		t.Fatalf("cancels %v (parent %d tp %d sl %d)", c, pid, tpID, slID)
	}
	// a partly filled entry: only the remainder is cancelled, both children keep protecting
	r, _ = vwapxRig(t, nil)
	pid, _, _ = sentVwapx(t, r)
	r.f.w.OrderStatus(pid, "Submitted", 50, 70, 27.99, 9100, 0, 27.99, 80, "", 0)
	if _, err := r.tk.CancelSymbol("SGRX"); err != nil {
		t.Fatal(err)
	}
	if c := cancels(r); len(c) != 1 || c[0] != pid {
		t.Fatalf("partial cancel %v", c)
	}
}

func TestVwapxCloseCancelsTheIBStopBeforeSelling(t *testing.T) {
	r, _ := vwapxRig(t, nil)
	pid, tpID, slID := sentVwapx(t, r)
	r.f.w.OrderStatus(pid, "Filled", 120, 0, 27.99, 9100, 0, 27.99, 80, "", 0)
	r.f.w.OrderStatus(tpID, "PreSubmitted", 0, 120, 0, 9101, pid, 0, 80, "", 0)
	r.f.w.OrderStatus(slID, "PreSubmitted", 0, 120, 0, 9102, pid, 0, 80, "", 0)

	p, err := r.tk.PrepareClose("SGRX")
	if err != nil || p.Qty != 120 {
		t.Fatalf("close %+v %v", p, err)
	}
	if err := r.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatal(err)
	}
	c := cancels(r)
	if len(c) != 2 || !((c[0] == tpID && c[1] == slID) || (c[0] == slID && c[1] == tpID)) {
		t.Fatalf("close cancels %v, want TP %d and IB stop %d", c, tpID, slID)
	}
	if len(exits(r)) != 0 {
		t.Fatal("sold before the IB stop and TP were confirmed gone")
	}
	r.f.w.OrderStatus(tpID, "Cancelled", 0, 120, 0, 9101, pid, 0, 80, "", 0)
	r.advance(time.Second)
	r.tk.Tick(context.Background())
	if len(exits(r)) != 0 {
		t.Fatal("sold with the IB stop still unconfirmed")
	}
	r.f.w.OrderStatus(slID, "Cancelled", 0, 120, 0, 9102, pid, 0, 80, "", 0)
	r.advance(time.Second)
	r.tk.Tick(context.Background())
	if ex := exits(r); len(ex) != 1 || ex[0].o.TotalQuantity != 120 || ex[0].o.Action != "SELL" {
		t.Fatalf("exit %+v", ex)
	}
}

func TestVwapxStopFillShrinksWhatCloseSells(t *testing.T) {
	r, _ := vwapxRig(t, nil)
	pid, _, slID := sentVwapx(t, r)
	r.f.w.OrderStatus(pid, "Filled", 120, 0, 27.99, 9100, 0, 27.99, 80, "", 0)
	r.f.w.OrderStatus(slID, "Submitted", 40, 80, 27.79, 9102, pid, 27.79, 80, "", 0) // part of the stop filled
	if held, _ := r.tk.closeQtyLocked2("SGRX"); held != 80 {
		t.Fatalf("held %d, want 80", held)
	}
}

func TestVwapxNoAutoCancelOnFastDrop(t *testing.T) {
	r, _ := vwapxRig(t, nil)
	pid, _, _ := sentVwapx(t, r)
	// falling 9EMA, price crashes to the EMA: a 9EMA order would be auto-cancelled
	for i := 0; i < 6; i++ {
		r.closeBar("SGRX", 27.95-float64(i)*0.2)
	}
	for _, px := range []float64{27.90, 27.60, 27.20, 26.90} {
		price(r, px)
		r.advance(time.Second)
		r.tk.Tick(context.Background())
	}
	if c := cancels(r); len(c) != 0 {
		t.Fatalf("autocancel fired on a VWAP X order: %v", c)
	}
	if v := r.tk.State().Orders[0]; v.ID != pid || v.Note != "" {
		t.Fatalf("%+v", v)
	}
}

func TestVwapxEchoesAndErrors(t *testing.T) {
	aud := &MemAudit{}
	r, _ := vwapxRig(t, aud)
	pid, tpID, slID := sentVwapx(t, r)
	c := contractFor("SGRX")
	// openOrder echoes: the STP child must never be mistaken for the TP child
	r.f.w.OpenOrder(tpID, c, &Order{Action: "SELL", OrderType: "LMT", TotalQuantity: 120, LmtPrice: 28.56, TIF: "DAY", OrderRef: refPrefix + "SGRX:tp", ParentID: pid, ClientID: 80, PermID: 9101}, &OrderState{Status: "PreSubmitted"})
	r.f.w.OpenOrder(slID, c, &Order{Action: "SELL", OrderType: "STP", TotalQuantity: 120, TIF: "DAY", OrderRef: refPrefix + "SGRX:sl", ParentID: pid, ClientID: 80, PermID: 9102}, &OrderState{Status: "PreSubmitted"})
	v := r.tk.State().Orders[0]
	if v.ChildID != tpID || v.SLID != slID || v.ChildStatus != "PreSubmitted" || v.SLStatus != "PreSubmitted" || !near(v.TP, 28.56) {
		t.Fatalf("view %+v", v)
	}
	if s := r.tk.State(); s.StopAlert != "" {
		t.Fatalf("alert %q (tpverify flagged the bracket)", s.StopAlert)
	}
	// an orderStatus for the stop with a foreign perm id is ignored (tpguard rules)
	r.f.w.OrderStatus(slID, "Filled", 120, 0, 1, 777, pid, 1, 80, "", 0)
	if v := r.tk.State().Orders[0]; v.SLFilled != 0 || v.SLStatus != "PreSubmitted" {
		t.Fatalf("foreign status applied %+v", v)
	}
	// IB rejects the stop child: audited as role "sl"
	r.f.w.Error(slID, 0, 201, "rejected", "")
	time.Sleep(20 * time.Millisecond)
	found := false
	for _, a := range aud.Recs {
		if a.Ev == "error" && a.ID == slID && strings.HasPrefix(a.Note, "sl: IB 201") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no audited stop error: %+v", aud.Recs)
	}
}

func TestVwapxRestartAdoptsWithoutFollowOrSoftStop(t *testing.T) {
	aud := &MemAudit{}
	r, _ := vwapxRig(t, aud)
	pid, tpID, slID := sentVwapx(t, r)
	r.tk.Disconnect()

	r2 := newRig(t, aud)
	r2.f.onOpen = func(w Wrapper) {
		ct := contractFor("SGRX")
		w.OpenOrder(pid, ct, &Order{Action: "BUY", OrderType: "LMT", TotalQuantity: 120, LmtPrice: 27.99, TIF: "DAY", OrderRef: refPrefix + "SGRX", ClientID: 80, PermID: 9100}, &OrderState{Status: "Submitted"})
		w.OpenOrder(slID, ct, &Order{Action: "SELL", OrderType: "STP", TotalQuantity: 120, TIF: "DAY", OrderRef: refPrefix + "SGRX:sl", ParentID: pid, ClientID: 80, PermID: 9102}, &OrderState{Status: "PreSubmitted"})
		w.OpenOrder(tpID, ct, &Order{Action: "SELL", OrderType: "LMT", TotalQuantity: 120, LmtPrice: 28.56, TIF: "DAY", OrderRef: refPrefix + "SGRX:tp", ParentID: pid, ClientID: 80, PermID: 9101}, &OrderState{Status: "PreSubmitted"})
	}
	r2.connect()
	r2.seedBars("SGRX", 30, 27.95)
	r2.show("111")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if os := r2.tk.State().Orders; len(os) == 1 && os[0].SLID == slID && os[0].ChildID == tpID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not adopted %+v", r2.tk.State().Orders)
		}
		time.Sleep(2 * time.Millisecond)
	}
	r2.f.w.OrderStatus(pid, "Filled", 120, 0, 27.99, 9100, 0, 27.99, 80, "", 0)
	for i := 0; i < 5; i++ {
		r2.closeBar("SGRX", 27.95)
		r2.tk.Tick(context.Background())
	}
	v := r2.tk.State().Orders[0]
	if v.Kind != kindVwapx || v.Following || v.StopState != "" || v.StopOn || v.FollowLabel != "" {
		t.Fatalf("adopted view %+v", v)
	}
	if n := len(r2.f.placesCopy()); n != 0 {
		t.Fatalf("an adopted VWAP X order was modified: %d placeOrder", n)
	}
	if c := cancels(r2); len(c) != 0 {
		t.Fatalf("cancels %v", c)
	}
}

// The ordinary 9EMA order is untouched: one child, a soft stop, no Kind.
func TestVwapxLeavesNormalOrderAlone(t *testing.T) {
	r := newRig(t, nil)
	r.ready("SGRX", "111", 1.50)
	pid, cid, q := placeOrder(t, r, "BUY")
	if got := r.f.placesCopy(); len(got) != 2 || got[0].o.Transmit || !got[1].o.Transmit || got[1].o.OcaGroup != "" || got[1].o.TIF != "GTC" || got[1].o.OrderType != "LMT" {
		t.Fatalf("normal bracket changed: %+v", got)
	}
	r.f.w.OrderStatus(pid, "Filled", Qty(q), 0, 1.50, 9100, 0, 1.50, 80, "", 0)
	if v := view(t, r, pid); v.Kind != "" || v.SLID != 0 || v.StopState != stArmed || v.ChildID != cid {
		t.Fatalf("%+v", v)
	}
}
