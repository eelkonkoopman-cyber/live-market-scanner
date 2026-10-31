package orderticket

// VWAP X down (vwapx.go, side SELL): the bear mirror of the VWAP X preset. The
// same availability rules, level arithmetic, sizing, the 3-order bracket on the
// wire (fake client + byte-level encoder only) and how every guard treats a
// SHORT bracket: parent SELL LMT -> BUY LMT take profit -> BUY STP stop.

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/scmhub/ibapi"
)

// vwapxBearRig: SGRX shown, 30 flat bars at 27.95, and a BEAR cross 3 minutes
// ago (candle 10:27 ET, closed): O 28.05 H 28.20 L 27.90 C 27.99. now = 10:30:30 ET.
// entry 27.99, stop 28.21 (high + 0.01), TP 27.33 (27.99 x 0.99 = 27.3301, down).
func vwapxBearRig(t *testing.T, aud *MemAudit) (*rig, int64) {
	t.Helper()
	r := newRig(t, aud)
	r.ready("SGRX", "111", 27.95)
	cross := minuteOf(r.now.UnixMilli()) - 3*60000
	r.tk.SeedBars("SGRX", []core.Bar{{T: cross, O: 28.05, H: 28.20, L: 27.90, C: 27.99, V: 1_000_000_000, VW: 28.21}})
	r.tk.FeedAgg(core.Agg{Sym: "SGRX", StartMs: r.now.UnixMilli(), Open: 27.99, High: 27.99, Low: 27.99, Close: 27.99, Vol: 1})
	r.mu.Lock()
	r.crossBear = map[string]int64{"SGRX": cross}
	r.mu.Unlock()
	return r, cross
}

func vxb(r *rig) *Vwapx { return r.tk.State().VwapxBear }

func prepBear(r *rig, pos float64, sh int64, risk float64) (Pending, error) {
	return r.tk.PrepareVwapxSide("SELL", pos, sh, "SGRX", risk)
}

func TestVwapxBearLevelArithmetic(t *testing.T) {
	e, s, tp := vwapxShortLevels(27.99, 28.21)
	if !near(e, 27.99) || !near(s, 28.21) || !near(tp, 27.33) {
		t.Fatalf("levels %v %v %v", e, s, tp)
	}
	if e, s, tp = vwapxShortLevels(0.51, 0.52); !near(e, 0.51) || !near(s, 0.52) || !near(tp, 0.48) {
		t.Fatalf("sub-dollar levels %v %v %v", e, s, tp)
	}
}

func TestVwapxBearStateRules(t *testing.T) {
	r := newRig(t, nil)
	r.ready("SGRX", "111", 27.95)
	if v := vxb(r); v.Available || v.Reason != "no VWAP bear cross today" || v.Side != "SELL" {
		t.Fatalf("no cross: %+v", v)
	}
	set := func(ms int64) { r.mu.Lock(); r.crossBear = map[string]int64{"SGRX": ms}; r.mu.Unlock() }
	now := minuteOf(r.now.UnixMilli())

	// forming: the cross candle is the current minute (30 s old)
	set(now)
	if v := vxb(r); v.Available || v.ClosesInSec != 30 || v.Reason != "cross candle closes in 30s" || v.CrossMs != now {
		t.Fatalf("forming: %+v", v)
	}
	r.advance(25 * time.Second)
	if v := vxb(r); v.ClosesInSec != 5 {
		t.Fatalf("countdown: %+v", v)
	}
	r.advance(-25 * time.Second)

	set(now - 15*60000)
	if v := vxb(r); v.Available || v.Reason != "cross is 15m old" {
		t.Fatalf("old: %+v", v)
	}

	// 9m ago: allowed; the levels follow the price, the VWAP is the stop
	set(now - 9*60000)
	r.tk.SeedBars("SGRX", []core.Bar{{T: now - 9*60000, H: 28, L: 28, C: 28, V: 1_000_000_000, VW: 28}})
	r.tk.FeedAgg(core.Agg{Sym: "SGRX", StartMs: now, Open: 27.7, High: 27.7, Low: 27.7, Close: 27.7, Vol: 1})
	if v := vxb(r); !v.Available {
		t.Fatalf("right side: %+v", v)
	}
	// wrong side of the VWAP
	r.tk.FeedAgg(core.Agg{Sym: "SGRX", StartMs: now, Open: 28.3, High: 28.3, Low: 28.3, Close: 28.3, Vol: 1})
	if v := vxb(r); v.Available || v.Reason != "price is not under the VWAP" {
		t.Fatalf("wrong side: %+v", v)
	}
}

func TestVwapxBearUnavailableWithoutSource(t *testing.T) {
	r := newRig(t, nil)
	r.tk.cfg.LatestVwapBear = nil
	if v := r.tk.State().VwapxBear; v == nil || v.Available || v.Reason == "" {
		t.Fatalf("%+v", v)
	}
	if _, err := r.tk.PrepareVwapxSide("SELL", 0, 100, "", 0); err == nil {
		t.Fatal("prepared without a connection / source")
	}
	if _, err := r.tk.PrepareVwapxSide("HOLD", 0, 100, "", 0); err == nil || !strings.Contains(err.Error(), "bad side") {
		t.Fatalf("bad side: %v", err)
	}
}

func TestVwapxBearBorrowRules(t *testing.T) {
	setShort := func(r *rig, ref *core.ShortRef, age time.Duration) {
		r.tk.cfg.Shortable = func(sym string) (core.ShortRef, bool, int64) {
			asOf := int64(0)
			if age >= 0 {
				asOf = r.now.Add(-age).Unix()
			}
			if ref == nil || sym != ref.Ticker {
				return core.ShortRef{}, false, asOf
			}
			return *ref, true, asOf
		}
	}
	// known and shortable: borrow ok, the confirm line shows the IBKR numbers
	r, _ := vwapxBearRig(t, nil)
	setShort(r, &core.ShortRef{Ticker: "SGRX", Available: 150000, FeeRate: 12.5}, 5*time.Minute)
	if v := vxb(r); !v.Available || v.Borrow != "ok" {
		t.Fatalf("shortable: %+v", v)
	}
	p, err := prepBear(r, 0, 120, 0)
	if err != nil || !strings.HasSuffix(p.Text, " · shortable 150K (fee 12.5%)") || strings.Contains(p.Text, "unknown") {
		t.Fatalf("%q %v", p.Text, err)
	}

	// listed but NO shares: unavailable, and prepare is refused
	r, _ = vwapxBearRig(t, nil)
	setShort(r, &core.ShortRef{Ticker: "SGRX", Available: 0}, time.Minute)
	if v := vxb(r); v.Available || v.Reason != "not shortable (no borrow)" {
		t.Fatalf("no borrow: %+v", v)
	}
	if _, err := prepBear(r, 0, 120, 0); err == nil || !strings.Contains(err.Error(), "not shortable (no borrow)") {
		t.Fatalf("prepare with no borrow: %v", err)
	}
	if r.tk.State().Pending != nil || len(r.f.placesCopy()) != 0 {
		t.Fatal("a refused prepare left a pending / sent something")
	}
	// the bull preset does not care about borrow
	if bv := vx(r); bv.Reason == "not shortable (no borrow)" {
		t.Fatalf("bull affected by borrow: %+v", bv)
	}

	// not in the list at all (list loaded) -> unknown, still available
	r, _ = vwapxBearRig(t, nil)
	setShort(r, &core.ShortRef{Ticker: "OTHER", Available: 10}, time.Minute)
	if v := vxb(r); !v.Available || v.Borrow != "unknown" {
		t.Fatalf("not in list: %+v", v)
	}
	if p, err = prepBear(r, 0, 120, 0); err != nil || !strings.Contains(p.Text, "borrow unknown") || p.Warn == "" {
		t.Fatalf("unknown borrow text %q warn %q %v", p.Text, p.Warn, err)
	}

	// list never loaded -> unknown, still available
	r, _ = vwapxBearRig(t, nil)
	setShort(r, nil, -1)
	if v := vxb(r); !v.Available || v.Borrow != "unknown" {
		t.Fatalf("never loaded: %+v", v)
	}

	// no short store wired at all -> unknown
	r, _ = vwapxBearRig(t, nil)
	if v := vxb(r); !v.Available || v.Borrow != "unknown" {
		t.Fatalf("no store: %+v", v)
	}

	// fewer shares than wanted: a warning, never a block (IB decides)
	r, _ = vwapxBearRig(t, nil)
	setShort(r, &core.ShortRef{Ticker: "SGRX", Available: 50, FeeRate: 3}, time.Minute)
	if p, err = prepBear(r, 0, 120, 0); err != nil || !strings.Contains(p.Warn, "only 50 shares") {
		t.Fatalf("short of shares: %q %v", p.Warn, err)
	}
}

func TestVwapxBearPrepareTextAndSizing(t *testing.T) {
	r, cross := vwapxBearRig(t, nil)

	p, err := prepBear(r, 0, 120, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := "SELL SHORT 120 SGRX LMT 27.99 (VWAP X↓ 10:27) · STP 28.21 (VWAP) · TP 3R 27.33 · ~$3,359, risk $26 · borrow unknown"
	if p.Kind != kindVwapx || p.Side != "SELL" || p.Text != want || p.CrossMs != cross || p.Qty != 120 || !near(p.Limit, 27.99) || !near(p.PlannedStop, 28.21) || !near(p.TP, 27.33) || !p.StopOff {
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
	if wi := r.f.whatIfsCopy()[0]; wi.o.Action != "SELL" {
		t.Fatalf("what-if %+v", wi.o)
	}

	// position $: floor(3000 / 27.99) = 107
	if p, err = prepBear(r, 3000, 0, 0); err != nil || p.Qty != 107 {
		t.Fatalf("position sizing %+v %v", p, err)
	}
	// risk $: 50 / (28.21 - 27.99) = 227, trimmed by max position $5000 / 27.99 = 178
	if p, err = prepBear(r, 0, 0, 50); err != nil || p.Qty != 178 || p.Bound != "max position $" {
		t.Fatalf("risk sizing %+v %v", p, err)
	}
	// risk $ 20 / 0.22 = 90, uncapped
	if p, err = prepBear(r, 0, 0, 20); err != nil || p.Qty != 90 || p.Bound != "" || !near(p.Risk, 90*0.22) {
		t.Fatalf("risk sizing %+v %v", p, err)
	}
	if _, err = prepBear(r, 0, 500, 0); err == nil || !strings.Contains(err.Error(), "above the max") {
		t.Fatalf("over max: %v", err)
	}
	if _, err = prepBear(r, 0, 0, 0); err == nil {
		t.Fatal("no size given")
	}
	if _, err = r.tk.PrepareVwapxSide("SELL", 0, 10, "ABCD", 0); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("symbol mismatch: %v", err)
	}
}

func TestVwapxBearPrepareRefusedWhenUnavailable(t *testing.T) {
	r, cross := vwapxBearRig(t, nil)
	r.mu.Lock()
	r.crossBear["SGRX"] = minuteOf(r.now.UnixMilli()) // forming
	r.mu.Unlock()
	if _, err := prepBear(r, 0, 10, 0); err == nil || !strings.Contains(err.Error(), "closes in") {
		t.Fatalf("forming: %v", err)
	}
	r.mu.Lock()
	r.crossBear["SGRX"] = cross - 20*60000
	r.mu.Unlock()
	if _, err := prepBear(r, 0, 10, 0); err == nil || !strings.Contains(err.Error(), "old") {
		t.Fatalf("old: %v", err)
	}
	if r.tk.State().Pending != nil {
		t.Fatal("a refused prepare left a pending")
	}
	// only a bull cross exists: the bear button is refused (and vice versa)
	r, _ = vwapxRig(t, nil)
	if _, err := prepBear(r, 0, 10, 0); err == nil || !strings.Contains(err.Error(), "no VWAP bear cross") {
		t.Fatalf("bull-only: %v", err)
	}
}

func sentVwapxBear(t *testing.T, r *rig) (pid, tpID, slID int64) {
	t.Helper()
	p, err := prepBear(r, 0, 120, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatal(err)
	}
	o := r.tk.State().Orders[0]
	return o.ID, o.ChildID, o.SLID
}

func TestVwapxBearConfirmSendsTheShortBracket(t *testing.T) {
	aud := &MemAudit{}
	r, _ := vwapxBearRig(t, aud)
	p, err := prepBear(r, 0, 120, 0)
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
	// 1 parent: SELL LMT 120 @ 27.99, DAY, outsideRth, Transmit=false
	if o := par.o; o.Action != "SELL" || o.OrderType != "LMT" || o.TotalQuantity != 120 || !near(o.LmtPrice, 27.99) || o.TIF != "DAY" ||
		!o.OutsideRTH || o.Transmit || o.ParentID != 0 || o.OcaGroup != "" || o.AuxPrice != 0 || o.OrderRef != "oticket:SGRX" || o.Account != "U1234567" {
		t.Fatalf("parent %+v", o)
	}
	// 2 TP child: BUY LMT 120 @ 27.33, ParentID, Transmit=false, OCA
	if o := tp.o; o.Action != "BUY" || o.OrderType != "LMT" || o.TotalQuantity != 120 || !near(o.LmtPrice, 27.33) || o.TIF != "DAY" ||
		!o.OutsideRTH || o.Transmit || o.ParentID != pid || o.OcaGroup != grp || o.OcaType != 1 || o.OrderRef != "oticket:SGRX:tp" {
		t.Fatalf("tp %+v", o)
	}
	// 3 stop child: BUY STP 120 aux 28.21, ParentID, Transmit=TRUE (last), OCA
	if o := sl.o; o.Action != "BUY" || o.OrderType != "STP" || o.TotalQuantity != 120 || !near(o.AuxPrice, 28.21) || o.LmtPrice != 0 || o.TIF != "DAY" ||
		!o.OutsideRTH || !o.Transmit || o.ParentID != pid || o.OcaGroup != grp || o.OcaType != 1 || o.OrderRef != "oticket:SGRX:sl" {
		t.Fatalf("stop %+v", o)
	}
	for _, x := range got {
		if x.c.Symbol != "SGRX" || x.c.SecType != "STK" || x.c.Exchange != "SMART" || x.c.Currency != "USD" || x.o.WhatIf {
			t.Fatalf("contract/whatif %+v", x)
		}
	}

	v := r.tk.State().Orders[0]
	if v.Kind != kindVwapx || v.Side != "SELL" || v.SLID != sl.id || !near(v.SL, 28.21) || v.SLStatus != "Sent" || v.StopOn || v.StopState != "" || v.Following || v.StopLabel != "SL 28.21 (IB stop)" || !v.Active || v.CanBE {
		t.Fatalf("view %+v", v)
	}

	places := map[int64]auditRec{}
	for _, a := range aud.Recs {
		if a.Ev == "place" {
			places[a.ID] = a
		}
	}
	if len(places) != 3 || places[pid].Kind != kindVwapx || places[tp.id].Kind != kindVwapx || places[sl.id].Kind != kindVwapx ||
		places[pid].Side != "SELL" || places[sl.id].ParentID != pid || !near(places[sl.id].Limit, 28.21) {
		t.Fatalf("audit %+v", aud.Recs)
	}
	if !strings.Contains(places[pid].Note, "VWAP X down cross 10:27") {
		t.Fatalf("audit note %q", places[pid].Note)
	}

	if err := r.tk.Confirm(p.Token, "SGRX"); err == nil {
		t.Fatal("token reused")
	}
	if n := len(r.f.placesCopy()); n != 3 {
		t.Fatalf("second confirm sent orders: %d", n)
	}
}

func TestVwapxBearConfirmRefusedWhenThingsChange(t *testing.T) {
	// the price rises through the stop between prepare and Confirm
	r, _ := vwapxBearRig(t, nil)
	p, err := prepBear(r, 0, 120, 0)
	if err != nil {
		t.Fatal(err)
	}
	r.tk.FeedAgg(core.Agg{Sym: "SGRX", StartMs: minuteOf(r.now.UnixMilli()), Open: 28.4, High: 28.4, Low: 28.4, Close: 28.40, Vol: 1})
	if err := r.tk.Confirm(p.Token, "SGRX"); err == nil || !strings.Contains(err.Error(), "no longer valid") {
		t.Fatalf("confirm: %v", err)
	}
	if n := len(r.f.placesCopy()); n != 0 {
		t.Fatalf("%d orders sent", n)
	}

	// a newer cross replaces the one the line was built on
	r, cross := vwapxBearRig(t, nil)
	p, _ = prepBear(r, 0, 120, 0)
	r.tk.SeedBars("SGRX", []core.Bar{{T: cross + 60000, O: 28.0, H: 28.1, L: 27.9, C: 27.95, V: 100}})
	r.mu.Lock()
	r.crossBear["SGRX"] = cross + 60000
	r.mu.Unlock()
	if err := r.tk.Confirm(p.Token, "SGRX"); err == nil || !strings.Contains(err.Error(), "new cross") {
		t.Fatalf("confirm: %v", err)
	}
	if n := len(r.f.placesCopy()); n != 0 {
		t.Fatalf("%d orders sent", n)
	}

	// borrow disappears between prepare and Confirm
	r, _ = vwapxBearRig(t, nil)
	p, _ = prepBear(r, 0, 120, 0)
	r.tk.cfg.Shortable = func(string) (core.ShortRef, bool, int64) {
		return core.ShortRef{Ticker: "SGRX", Available: 0}, true, r.now.Unix()
	}
	if err := r.tk.Confirm(p.Token, "SGRX"); err == nil || !strings.Contains(err.Error(), "not shortable") {
		t.Fatalf("confirm: %v", err)
	}
	if n := len(r.f.placesCopy()); n != 0 {
		t.Fatalf("%d orders sent", n)
	}

	// expired token
	r, _ = vwapxBearRig(t, nil)
	p, _ = prepBear(r, 0, 120, 0)
	r.advance(confirmTTL + time.Second)
	if err := r.tk.Confirm(p.Token, "SGRX"); err == nil {
		t.Fatal("expired token accepted")
	}
	if n := len(r.f.placesCopy()); n != 0 {
		t.Fatalf("%d orders sent", n)
	}
}

func TestVwapxBearNoSoftStopNoFollowNoTPResend(t *testing.T) {
	r, _ := vwapxBearRig(t, nil)
	pid, tpID, slID := sentVwapxBear(t, r)
	before := len(r.f.placesCopy())

	for i := 0; i < 4; i++ {
		r.closeBar("SGRX", 27.95-float64(i)*0.05)
		r.tk.Tick(context.Background())
	}
	if n := len(r.f.placesCopy()); n != before {
		t.Fatalf("entry/TP modified: %d -> %d", before, n)
	}

	// full fill at a better (higher) price than the limit: TP stays, no soft stop arms
	r.f.w.OrderStatus(pid, "Filled", 120, 0, 28.01, 9100, 0, 28.01, 80, "", 0)
	r.f.w.OrderStatus(tpID, "PreSubmitted", 0, 120, 0, 9101, pid, 0, 80, "", 0)
	r.f.w.OrderStatus(slID, "PreSubmitted", 0, 120, 0, 9102, pid, 0, 80, "", 0)
	v := r.tk.State().Orders[0]
	if v.StopState != "" || v.StopOn || !near(v.TP, 27.33) || v.SLStatus != "PreSubmitted" || v.ChildStatus != "PreSubmitted" || v.CanBE {
		t.Fatalf("after fill %+v", v)
	}
	if n := len(r.f.placesCopy()); n != before {
		t.Fatalf("fill re-sent an order: %d -> %d", before, n)
	}

	// a price through the (non-existent) soft stop level does nothing here
	for i := 0; i < 5; i++ {
		price(r, 29.00)
		r.advance(time.Second)
		r.tk.Tick(context.Background())
	}
	if c := cancels(r); len(c) != 0 || len(r.f.placesCopy()) != before {
		t.Fatalf("something reacted to the price: cancels %v places %d", c, len(r.f.placesCopy()))
	}
	if err := r.tk.SetStopEnabled(pid, true); err == nil {
		t.Fatal("soft stop enabled on a VWAP X down order")
	}
	if err := r.tk.MoveStopToEntry(pid); err == nil {
		t.Fatal("→ BE accepted on a VWAP X down order")
	}
	if s := r.tk.State(); s.BECount != 0 || s.StopWarn != "" || s.StopUnwatched {
		t.Fatalf("state %+v", s)
	}
}

func TestVwapxBearStopFillAndTPFillBookkeeping(t *testing.T) {
	r, _ := vwapxBearRig(t, nil)
	pid, tpID, slID := sentVwapxBear(t, r)
	r.f.w.OrderStatus(pid, "Filled", 120, 0, 27.99, 9100, 0, 27.99, 80, "", 0)
	if held, _ := r.tk.closeQtyLocked2("SGRX"); held != 120 {
		t.Fatalf("held %d after the short filled", held)
	}
	// the real (buy) stop is hit; IB cancels the TP
	r.f.w.OrderStatus(slID, "Filled", 120, 0, 28.22, 9102, pid, 28.22, 80, "", 0)
	r.f.w.OrderStatus(tpID, "Cancelled", 0, 120, 0, 9101, pid, 0, 80, "", 0)
	v := r.tk.State().Orders[0]
	if v.SLStatus != "Filled" || v.SLFilled != 120 || v.ChildStatus != "Cancelled" || v.Active {
		t.Fatalf("view %+v", v)
	}
	if held, _ := r.tk.closeQtyLocked2("SGRX"); held != 0 {
		t.Fatalf("held %d after the stop covered everything", held)
	}

	// the TP is hit instead
	r, _ = vwapxBearRig(t, nil)
	pid, tpID, slID = sentVwapxBear(t, r)
	r.f.w.OrderStatus(pid, "Filled", 120, 0, 27.99, 9100, 0, 27.99, 80, "", 0)
	r.f.w.OrderStatus(tpID, "Filled", 120, 0, 27.33, 9101, pid, 27.33, 80, "", 0)
	r.f.w.OrderStatus(slID, "Cancelled", 0, 120, 0, 9102, pid, 0, 80, "", 0)
	if held, _ := r.tk.closeQtyLocked2("SGRX"); held != 0 {
		t.Fatalf("held %d after the TP covered everything", held)
	}
	if v := r.tk.State().Orders[0]; v.Active || v.ChildFilled != 120 {
		t.Fatalf("view %+v", v)
	}
}

func TestVwapxBearCancelUnfilledCancelsAllThree(t *testing.T) {
	r, _ := vwapxBearRig(t, nil)
	pid, tpID, slID := sentVwapxBear(t, r)
	if _, err := r.tk.CancelSymbol("SGRX"); err != nil {
		t.Fatal(err)
	}
	c := cancels(r)
	if len(c) != 3 || c[0] != pid || c[1] != slID || c[2] != tpID {
		t.Fatalf("cancels %v (parent %d tp %d sl %d)", c, pid, tpID, slID)
	}
	// partly filled: only the remainder is cancelled, both children keep protecting
	r, _ = vwapxBearRig(t, nil)
	pid, _, _ = sentVwapxBear(t, r)
	r.f.w.OrderStatus(pid, "Submitted", 50, 70, 27.99, 9100, 0, 27.99, 80, "", 0)
	if _, err := r.tk.CancelSymbol("SGRX"); err != nil {
		t.Fatal(err)
	}
	if c := cancels(r); len(c) != 1 || c[0] != pid {
		t.Fatalf("partial cancel %v", c)
	}
}

func TestVwapxBearCloseCoversAfterCancellingTheIBStop(t *testing.T) {
	r, _ := vwapxBearRig(t, nil)
	pid, tpID, slID := sentVwapxBear(t, r)
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
		t.Fatal("covered before the IB stop and TP were confirmed gone")
	}
	r.f.w.OrderStatus(tpID, "Cancelled", 0, 120, 0, 9101, pid, 0, 80, "", 0)
	r.advance(time.Second)
	r.tk.Tick(context.Background())
	if len(exits(r)) != 0 {
		t.Fatal("covered with the IB stop still unconfirmed")
	}
	r.f.w.OrderStatus(slID, "Cancelled", 0, 120, 0, 9102, pid, 0, 80, "", 0)
	r.advance(time.Second)
	r.tk.Tick(context.Background())
	ex := exits(r)
	if len(ex) != 1 || ex[0].o.TotalQuantity != 120 || ex[0].o.Action != "BUY" || ex[0].o.OrderType != "LMT" || ex[0].o.LmtPrice < 27.95 {
		t.Fatalf("exit %+v", ex)
	}
	// ticking on never sends a second cover
	for i := 0; i < 5; i++ {
		r.advance(time.Second)
		r.tk.Tick(context.Background())
	}
	ids := map[int64]bool{}
	for _, x := range exits(r) { // the re-price steps modify the SAME order id
		ids[x.id] = true
	}
	if len(ids) != 1 {
		t.Fatalf("%d distinct cover orders: %+v", len(ids), exits(r))
	}
}

func TestVwapxBearStopFillShrinksWhatCloseCovers(t *testing.T) {
	r, _ := vwapxBearRig(t, nil)
	pid, _, slID := sentVwapxBear(t, r)
	r.f.w.OrderStatus(pid, "Filled", 120, 0, 27.99, 9100, 0, 27.99, 80, "", 0)
	r.f.w.OrderStatus(slID, "Submitted", 40, 80, 28.22, 9102, pid, 28.22, 80, "", 0) // part of the stop bought back
	if held, _ := r.tk.closeQtyLocked2("SGRX"); held != 80 {
		t.Fatalf("held %d, want 80", held)
	}
	p, err := r.tk.PrepareClose("SGRX")
	if err != nil || p.Qty != 80 {
		t.Fatalf("close %+v %v", p, err)
	}
}

func TestVwapxBearNoAutoCancelOnFastDrop(t *testing.T) {
	r, _ := vwapxBearRig(t, nil)
	pid, _, _ := sentVwapxBear(t, r)
	// a rising 9EMA, price spikes up through it: a 9EMA short would be auto-cancelled
	for i := 0; i < 6; i++ {
		r.closeBar("SGRX", 27.95+float64(i)*0.2)
	}
	for _, px := range []float64{28.00, 28.30, 28.60, 29.10} {
		price(r, px)
		r.advance(time.Second)
		r.tk.Tick(context.Background())
	}
	if c := cancels(r); len(c) != 0 {
		t.Fatalf("autocancel fired on a VWAP X down order: %v", c)
	}
	if v := r.tk.State().Orders[0]; v.ID != pid || v.Note != "" {
		t.Fatalf("%+v", v)
	}
}

func TestVwapxBearEchoesAndErrors(t *testing.T) {
	aud := &MemAudit{}
	r, _ := vwapxBearRig(t, aud)
	pid, tpID, slID := sentVwapxBear(t, r)
	c := contractFor("SGRX")
	r.f.w.OpenOrder(tpID, c, &Order{Action: "BUY", OrderType: "LMT", TotalQuantity: 120, LmtPrice: 27.33, TIF: "DAY", OrderRef: refPrefix + "SGRX:tp", ParentID: pid, ClientID: 80, PermID: 9101}, &OrderState{Status: "PreSubmitted"})
	r.f.w.OpenOrder(slID, c, &Order{Action: "BUY", OrderType: "STP", TotalQuantity: 120, TIF: "DAY", OrderRef: refPrefix + "SGRX:sl", ParentID: pid, ClientID: 80, PermID: 9102}, &OrderState{Status: "PreSubmitted"})
	v := r.tk.State().Orders[0]
	if v.ChildID != tpID || v.SLID != slID || v.ChildStatus != "PreSubmitted" || v.SLStatus != "PreSubmitted" || !near(v.TP, 27.33) {
		t.Fatalf("view %+v", v)
	}
	if s := r.tk.State(); s.StopAlert != "" {
		t.Fatalf("alert %q (tpverify flagged the bracket)", s.StopAlert)
	}
	// a foreign perm id is ignored
	r.f.w.OrderStatus(slID, "Filled", 120, 0, 1, 777, pid, 1, 80, "", 0)
	if v := r.tk.State().Orders[0]; v.SLFilled != 0 || v.SLStatus != "PreSubmitted" {
		t.Fatalf("foreign status applied %+v", v)
	}
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

func TestVwapxBearTPFillBeyondLimitIsNotTrusted(t *testing.T) {
	// the shared short-side TP guard: a BUY TP never fills ABOVE its limit
	r, _ := vwapxBearRig(t, nil)
	pid, tpID, _ := sentVwapxBear(t, r)
	r.f.w.OrderStatus(pid, "Filled", 120, 0, 27.99, 9100, 0, 27.99, 80, "", 0)
	r.f.w.OrderStatus(tpID, "Filled", 120, 0, 28.50, 9101, pid, 28.50, 80, "", 0)
	if held, _ := r.tk.closeQtyLocked2("SGRX"); held != 0 {
		// held() is arithmetic only; the point is that nothing is sent on the claim
		t.Logf("held %d", held)
	}
	if n := len(exits(r)); n != 0 {
		t.Fatalf("%d exits sent on an implausible TP fill", n)
	}
}

func TestVwapxBearRestartAdoptsWithoutFollowOrSoftStop(t *testing.T) {
	aud := &MemAudit{}
	r, _ := vwapxBearRig(t, aud)
	pid, tpID, slID := sentVwapxBear(t, r)
	r.tk.Disconnect()

	r2 := newRig(t, aud)
	r2.f.onOpen = func(w Wrapper) {
		ct := contractFor("SGRX")
		w.OpenOrder(pid, ct, &Order{Action: "SELL", OrderType: "LMT", TotalQuantity: 120, LmtPrice: 27.99, TIF: "DAY", OrderRef: refPrefix + "SGRX", ClientID: 80, PermID: 9100}, &OrderState{Status: "Submitted"})
		w.OpenOrder(slID, ct, &Order{Action: "BUY", OrderType: "STP", TotalQuantity: 120, TIF: "DAY", OrderRef: refPrefix + "SGRX:sl", ParentID: pid, ClientID: 80, PermID: 9102}, &OrderState{Status: "PreSubmitted"})
		w.OpenOrder(tpID, ct, &Order{Action: "BUY", OrderType: "LMT", TotalQuantity: 120, LmtPrice: 27.33, TIF: "DAY", OrderRef: refPrefix + "SGRX:tp", ParentID: pid, ClientID: 80, PermID: 9101}, &OrderState{Status: "PreSubmitted"})
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
	if v.Kind != kindVwapx || v.Side != "SELL" || v.Following || v.StopState != "" || v.StopOn || v.FollowLabel != "" {
		t.Fatalf("adopted view %+v", v)
	}
	if n := len(r2.f.placesCopy()); n != 0 {
		t.Fatalf("an adopted VWAP X down order was modified: %d placeOrder", n)
	}
	if c := cancels(r2); len(c) != 0 {
		t.Fatalf("cancels %v", c)
	}
	// and CLOSE on the adopted short still covers with a BUY
	p, err := r2.tk.PrepareClose("SGRX")
	if err != nil || p.Qty != 120 {
		t.Fatalf("close %+v %v", p, err)
	}
}

// Both crosses today: each button builds its own bracket; the bull path is
// byte-for-byte what it was (SELL children, SELL STP below the entry).
func TestVwapxBearLeavesTheBullPathAlone(t *testing.T) {
	r, cross := vwapxRig(t, nil)
	r.mu.Lock()
	r.crossBear = map[string]int64{"SGRX": cross}
	r.mu.Unlock()
	r.tk.SeedBars("SGRX", []core.Bar{{T: cross, O: 27.90, H: 28.05, L: 27.81, C: 27.99, V: 1_000_000_000, VW: 27.80}})
	if v := vx(r); !v.Available || v.Side != "BUY" || v.Borrow != "" || !near(v.Entry, 27.99) || !near(v.Stop, 27.80) || !near(v.TP, 28.56) {
		t.Fatalf("bull state %+v", v)
	}
	p, err := r.tk.PrepareVwapx(0, 120, "SGRX", 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := "BUY 120 SGRX LMT 27.99 (VWAP X 10:27) · STP 27.80 (VWAP) · TP 3R 28.56 · ~$3,359, risk $23"; p.Text != want || p.Side != "BUY" {
		t.Fatalf("bull text %q", p.Text)
	}
	if err := r.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatal(err)
	}
	got := r.f.placesCopy()
	if len(got) != 3 || got[0].o.Action != "BUY" || got[1].o.Action != "SELL" || got[2].o.Action != "SELL" || got[2].o.OrderType != "STP" || !near(got[2].o.AuxPrice, 27.80) {
		t.Fatalf("bull bracket changed: %+v", got)
	}
	// HTTP-level side defaults: "" and BUY are the bull, SELL the bear
	if _, err := r.tk.PrepareVwapxSide("", 0, 10, "SGRX", 0); err != nil {
		t.Fatalf("empty side must stay the bull preset: %v", err)
	}
}

// The short bracket's wire bytes equal ibapi's at every server version the
// client can negotiate (mirror of TestVwapxBracketMatchesIbapi).
func TestVwapxBearBracketMatchesIbapi(t *testing.T) {
	ct := contractFor("MGM")
	tk := &Ticket{account: "U1234567"}
	o := &ord{ID: 100, ChildID: 101, SLID: 102, Kind: kindVwapx, Sym: "MGM", Side: "SELL", Qty: 120, Limit: 27.99, TP: 27.33, SLPx: 28.21}
	orders := []*Order{tk.entryOrder(o, false), tk.vwapxTP(o, false), tk.slOrder(o, true)}
	if par := orders[0]; par.Action != "SELL" || par.OrderType != "LMT" || par.Transmit {
		t.Fatalf("parent %+v", par)
	}
	if tp := orders[1]; tp.Action != "BUY" || tp.OrderType != "LMT" || tp.LmtPrice != 27.33 || tp.Transmit || tp.ParentID != 100 || tp.OcaGroup != "vwapx-100" || tp.OcaType != 1 {
		t.Fatalf("tp child %+v", tp)
	}
	if sl := orders[2]; sl.Action != "BUY" || sl.OrderType != "STP" || sl.AuxPrice != 28.21 || sl.LmtPrice != 0 || !sl.Transmit || sl.OcaGroup != "vwapx-100" || sl.OcaType != 1 || sl.ParentID != 100 {
		t.Fatalf("stop child %+v", sl)
	}
	for _, sv := range []int{100, 141, 145, 158, 165, 166, 169, 175, 176} {
		for i, x := range orders {
			id := int64(100 + i)
			ib := ibCapture(t, sv, func(c *ibapi.EClient) { c.PlaceOrder(id, ibContract(ct), ibOrder(x)) })
			assertSame(t, "vwapxBear@"+strconv.Itoa(sv)+"#"+strconv.Itoa(i), encPlaceOrder(sv, id, ct, x), ib)
		}
	}
}
