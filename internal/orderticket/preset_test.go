package orderticket

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func powerRig(t *testing.T, aud *MemAudit) *rig {
	t.Helper()
	r := newRig(t, aud)
	if err := r.tk.SetPreset(context.Background(), presetPower, false, 0); err != nil {
		t.Fatal(err)
	}
	r.ready("SGRX", "111", 1.50)
	return r
}

func placePower(t *testing.T, r *rig) (pid, cid, q int64) {
	t.Helper()
	p, err := r.tk.PrepareOrder("BUY", 0, 0, "SGRX", 0, false, 50)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatal(err)
	}
	o := r.tk.State().Orders[0]
	return o.ID, o.ChildID, o.Qty
}

func TestPowerPresetApplies(t *testing.T) {
	r := powerRig(t, nil)
	p, err := r.tk.PrepareOrder("BUY", 0, 0, "SGRX", 0, false, 50)
	if err != nil {
		t.Fatal(err)
	}
	// stop 1.50 x 0.94 = 1.41 -> 0.09/sh -> $50 = 555 sh; TP = 1.50 + 3 x 0.09 = 1.77
	if p.Qty != 555 || !near(p.PlannedStop, 1.41) || !near(p.TP, 1.77) || p.TPR != 3 || !p.Trail5m || !p.DropOff || p.EntryTimeoutMin != 15 {
		t.Fatalf("%+v", p)
	}
	for _, want := range []string{"POWER SETUP: BUY 555 SGRX @ 1.50", "TP 3R 1.77", "risk $50 (stop 1.41)", "· 5m trail", "· cancel if unfilled 15m"} {
		if !strings.Contains(p.Text, want) {
			t.Fatalf("%q missing %q", p.Text, want)
		}
	}
	if err := r.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatal(err)
	}
	ps := r.f.placesCopy()
	if !near(ps[1].o.LmtPrice, 1.77) {
		t.Fatalf("child TP %+v", ps[1].o)
	}
	pid := ps[0].id
	r.f.w.OrderStatus(pid, "Filled", 555, 0, 1.50, 9100, 0, 1.50, 80, "", 0)
	v := view(t, r, pid)
	if !near(v.StopLevel, 1.41) || v.PresetLabel != "PS · SL 1.41 · TP 3R 1.77 · 5m trail" {
		t.Fatalf("%+v", v)
	}
	r.tk.mu.Lock()
	o := r.tk.orders[pid]
	follow, be, beOff := o.StopFollow, o.BEPct, o.BEOff
	r.tk.mu.Unlock()
	if follow || be != 7.5 || beOff {
		t.Fatalf("follow %v be %v off %v", follow, be, beOff)
	}
	price(r, 1.61) // +7.33%: not yet
	if view(t, r, pid).StopBE {
		t.Fatal("BE before +7.5%")
	}
	price(r, 1.6125)
	if v := view(t, r, pid); !v.StopBE || !near(v.StopLevel, 1.50) {
		t.Fatalf("BE at 7.5%% %+v", v)
	}
	// the preset survives as the selection; custom orders are unaffected
	if !strings.Contains(r.meta[MetaKey], `"preset":"power"`) {
		t.Fatal(r.meta[MetaKey])
	}
}

func TestTPForRMath(t *testing.T) {
	if got := tpForR("BUY", 1.52, 1.41, 3); !near(got, 1.85) {
		t.Fatalf("long %v", got)
	}
	if got := tpForR("SELL", 2.00, 2.12, 3); !near(got, 1.64) {
		t.Fatalf("short %v", got)
	}
	// re-priced on the first fill from the fill avg and the stop at the fill
	r := powerRig(t, nil)
	pid, cid, q := placePower(t, r)
	r.f.w.OrderStatus(pid, "Submitted", 100, Qty(q-100), 1.49, 9100, 0, 1.49, 80, "", 0)
	// stop at fill: from min(EMA 1.50, fill 1.49) x 0.94 = 1.40 -> TP = 1.49 + 3 x 0.09 = 1.76
	var tp []placed
	for _, x := range r.f.placesCopy()[2:] {
		if x.id == cid {
			tp = append(tp, x)
		}
	}
	if len(tp) != 1 || !near(tp[0].o.LmtPrice, 1.76) {
		t.Fatalf("TP re-price %+v", tp)
	}
}

func TestEntryTimeout(t *testing.T) {
	ctx := context.Background()
	r := powerRig(t, nil)
	pid, cid, _ := placePower(t, r)
	r.advance(14 * time.Minute)
	r.tk.Tick(ctx)
	if len(cancels(r)) != 0 {
		t.Fatal("cancelled early")
	}
	r.advance(time.Minute)
	r.tk.Tick(ctx)
	if c := cancels(r); len(c) != 2 || c[0] != pid || c[1] != cid {
		t.Fatalf("timeout cancels %v", c)
	}
	r.tk.Tick(ctx)
	if len(cancels(r)) != 2 {
		t.Fatal("timeout fired twice")
	}
	// partly filled: remainder only; filled: untouched
	r2 := powerRig(t, nil)
	p2, _, q2 := placePower(t, r2)
	r2.f.w.OrderStatus(p2, "Submitted", 100, Qty(q2-100), 1.50, 9100, 0, 1.50, 80, "", 0)
	r2.advance(15 * time.Minute)
	r2.tk.Tick(ctx)
	if c := cancels(r2); len(c) != 1 || c[0] != p2 {
		t.Fatalf("partial %v", c)
	}
	r3 := powerRig(t, nil)
	p3, _, q3 := placePower(t, r3)
	r3.f.w.OrderStatus(p3, "Filled", Qty(q3), 0, 1.50, 9100, 0, 1.50, 80, "", 0)
	r3.advance(15 * time.Minute)
	r3.tk.Tick(ctx)
	if len(cancels(r3)) != 0 {
		t.Fatal("filled entry cancelled by the timeout")
	}
	// Custom default: no timeout
	r4 := newRig(t, nil)
	r4.ready("SGRX", "111", 1.50)
	placeOrder(t, r4, "BUY")
	r4.advance(60 * time.Minute)
	r4.tk.Tick(ctx)
	if len(cancels(r4)) != 0 {
		t.Fatal("custom order timed out")
	}
}

func TestTrail5mExit(t *testing.T) {
	r := powerRig(t, nil) // now 14:30:30 UTC
	pid, cid, q := placePower(t, r)
	r.f.w.OrderStatus(pid, "Filled", Qty(q), 0, 1.50, 9100, 0, 1.50, 80, "", 0) // fill 14:30:30
	r.f.w.OrderStatus(cid, "Submitted", 40, Qty(q-40), 1.77, 0, pid, 1.77, 80, "", 0)
	at := func(h, m, s int) time.Time { return time.Date(2026, 10, 14, h, m, s, 0, time.UTC) }
	var bars []core.Bar
	for m := 30; m < 40; m++ {
		lo := 1.55
		if m < 35 {
			lo = 1.60 // the fill's own 5m bar (14:30-14:35)
		}
		bars = append(bars, core.Bar{T: at(14, m, 0).UnixMilli(), O: 1.6, H: 1.62, L: lo, C: 1.6, V: 100})
	}
	r.tk.SeedBars("SGRX", bars)
	// 14:37: the previous 5m bar (14:30-14:35) is not wholly after the fill -> inactive
	r.mu.Lock()
	r.now = at(14, 37, 0)
	r.mu.Unlock()
	price(r, 1.58)
	price(r, 1.58)
	if len(cancels(r)) != 0 {
		t.Fatal("trail active before the first completed 5m bar after the fill")
	}
	// 14:40:10: previous completed bar 14:35-14:40, low 1.55
	r.mu.Lock()
	r.now = at(14, 40, 10)
	r.mu.Unlock()
	price(r, 1.56) // above the low
	price(r, 1.54) // one print below
	if len(cancels(r)) != 0 {
		t.Fatal("no debounce")
	}
	price(r, 1.54)
	if c := cancels(r); len(c) != 1 || c[0] != cid {
		t.Fatalf("trail cancels %v", c)
	}
	if !strings.Contains(r.tk.State().Note, "5m trail exit: last 1.54 < 5m low 1.55") {
		t.Fatalf("note %q", r.tk.State().Note)
	}
	r.f.w.OrderStatus(cid, "Cancelled", 40, Qty(q-40), 1.77, 0, pid, 1.77, 80, "", 0)
	ex := exits(r)
	if len(ex) != 1 || ex[0].o.TotalQuantity.Int() != q-40 || ex[0].o.Action != "SELL" || !ex[0].o.OutsideRTH {
		t.Fatalf("exit %+v", ex)
	}
}

func TestPresetRestartAndCustomTrail(t *testing.T) {
	aud := &MemAudit{}
	r := powerRig(t, aud)
	pid, cid, q := placePower(t, r)
	r.f.w.OrderStatus(pid, "Filled", Qty(q), 0, 1.50, 9100, 0, 1.50, 80, "", 0)
	r.tk.Disconnect()
	r2 := newRig(t, aud)
	r2.f.onOpen = func(w Wrapper) {
		w.OpenOrder(cid, contractFor("SGRX"), &Order{Action: "SELL", OrderType: "LMT", TotalQuantity: Qty(q), LmtPrice: 1.77,
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
	r2.tk.mu.Lock()
	o := r2.tk.orders[pid]
	got := []any{o.Preset, o.TPR, o.Trail5m, o.DropOff, o.BEPct, o.FillAt > 0, o.StopLevel}
	r2.tk.mu.Unlock()
	if got[0] != presetPower || got[1] != 3.0 || got[2] != true || got[3] != true || got[4] != 7.5 || got[5] != true || !near(got[6].(float64), 1.41) {
		t.Fatalf("restart %v", got)
	}
	// Custom with the 5m trail on: per-order flag, % TP unchanged
	r3 := newRig(t, nil)
	if err := r3.tk.SetPreset(context.Background(), presetCustom, true, 0); err != nil {
		t.Fatal(err)
	}
	r3.ready("SGRX", "111", 1.50)
	p, _ := r3.tk.Prepare("BUY", 1000, "SGRX")
	if !p.Trail5m || p.TPR != 0 || !strings.Contains(p.Text, "TP +10% 1.65") || strings.Contains(p.Text, "POWER") {
		t.Fatalf("%+v", p)
	}
	if err := r3.tk.SetPreset(context.Background(), "turbo", false, 0); err == nil {
		t.Fatal("unknown preset accepted")
	}
}

// The pane reads the loaded symbol's latest Power Setup grade from State.
func TestStatePsGrade(t *testing.T) {
	r := newRig(t, nil)
	r.ready("SGRX", "111", 1.50)
	if r.tk.State().PsGrade != nil {
		t.Fatal("no source: no grade")
	}
	grades := map[string]bool{"SGRX": true}
	r.tk.cfg.PowerSetup = func(sym string) (bool, float64, int64, bool) {
		a, ok := grades[sym]
		return a, 3.8, 123, ok
	}
	if g := r.tk.State().PsGrade; g == nil || !g.Aplus || g.Mult != 1.5 || g.BaseRange != 3.8 || g.BarMs != 123 {
		t.Fatalf("A+ grade %+v", g)
	}
	grades["SGRX"] = false
	if g := r.tk.State().PsGrade; g == nil || g.Aplus || g.Mult != 1 || g.BaseRange != 0 {
		t.Fatalf("plain grade %+v", g)
	}
}

func TestNewsFlagPreset(t *testing.T) {
	tk := &Ticket{}
	tk.settings.Preset = presetNews
	p := Pending{Side: "BUY", Limit: 2, TPPct: 5}
	tk.applyPresetLocked(&p, 2)
	if p.Preset != presetNews || !p.Trail5m || p.TPPct != nfTPPct || p.TP != 3 {
		t.Fatalf("pending %+v", p)
	}
	o := &ord{}
	tk.presetToOrderLocked(o, &p, time.Now())
	if !o.Trail5m || !o.BEOff || presetLabel(o) != "News Flag · 5m trail" {
		t.Fatalf("order %+v label %q", o, presetLabel(o))
	}
}
