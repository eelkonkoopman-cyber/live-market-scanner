package orderticket

import (
	"context"
	"strings"
	"testing"
	"time"
)

// IB 201 for a what-if (e.g. the SELL preview) must reach the margin/preview
// line only, never the status banner - also when it arrives late.
func TestWhatIfErrorNeverReachesBanner(t *testing.T) {
	r := newRig(t, nil)
	r.f.avail = "100000"
	r.f.whatIfReply = func(o *Order) *OrderState {
		if o.Action == "SELL" {
			go func() { // PlaceOrder holds the fake's lock while asking; answer after it
				r.f.mu.Lock()
				var id int64
				for i := len(r.f.whatIfs) - 1; i >= 0; i-- {
					if r.f.whatIfs[i].o.Action == "SELL" {
						id = r.f.whatIfs[i].id
						break
					}
				}
				r.f.mu.Unlock()
				r.f.w.Error(id, 0, 201, "Order rejected - reason:YOUR ORDER IS NOT ACCEPTED. EQUITY WITH LOAN VALUE MUST EXCEED THE INITIAL MARGIN", "")
			}()
			return nil
		}
		return defaultWhatIf(o)
	}
	r.ready("SGRX", "111", 1.50)
	waitFunds(t, r, 100000)
	r.tk.Tick(context.Background())
	p := waitPreview(t, r)
	if !strings.Contains(p.Text, "Max short rejected by IB (margin)") || !strings.HasPrefix(p.Text, "Max long ") {
		t.Fatalf("preview %q", p.Text)
	}
	if n := r.tk.State().Note; strings.Contains(n, "201") {
		t.Fatalf("what-if error on the banner: %q", n)
	}
	// Prepare SELL: rejected what-if -> margin line, not the banner
	pp, err := r.tk.Prepare("SELL", 1000, "SGRX")
	if err != nil || pp.Margin.Status != "unavailable" || !strings.Contains(pp.Margin.Text, "IB 201") {
		t.Fatalf("%+v %v", pp.Margin, err)
	}
	if n := r.tk.State().Note; strings.Contains(n, "201") {
		t.Fatalf("banner %q", n)
	}
	// late error for a finished what-if id
	wi := r.f.whatIfsCopy()
	r.f.w.Error(wi[0].id, 0, 201, "late rejection", "")
	if n := r.tk.State().Note; strings.Contains(n, "late") {
		t.Fatalf("late what-if error on the banner: %q", n)
	}
	// a newer preview (qty change) replaces the rejected one
	r.f.whatIfReply = nil
	r.tk.SetPreviewSize(0, 200, 0)
	r.tk.Tick(context.Background())
	waitIdle(t, r)
	if p := r.tk.State().Preview; p == nil || strings.Contains(p.Text, "rejected") || p.Qty != 200 {
		t.Fatalf("stale rejection kept: %+v", p)
	}
	// a real (non-what-if) error does reach the banner, and clears on a symbol change
	r.f.w.Error(-1, 0, 200, "No security definition", "")
	if !strings.Contains(r.tk.State().Note, "IB 200") {
		t.Fatal("real error hidden")
	}
	r.seedBars("ABCD", 30, 2)
	r.show("222")
	if n := r.tk.State().Note; n != "" {
		t.Fatalf("banner kept after symbol change: %q", n)
	}
}

func TestCloseAndCancelNeverBlockedByStaleEMA(t *testing.T) {
	r, pid, _, q := armedLong(t)
	_ = pid
	r.advance(20 * time.Minute) // newest bar 20m old: BUY/SELL blocked
	s := r.tk.State()
	if s.EMAOK || s.CloseQty != q {
		t.Fatalf("emaOk %v closeQty %d", s.EMAOK, s.CloseQty)
	}
	if _, err := r.tk.Prepare("BUY", 1000, "SGRX"); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("BUY with a 20m-old EMA: %v", err)
	}
	p, err := r.tk.PrepareClose("SGRX")
	if err != nil || !strings.Contains(p.Text, "m old)") {
		t.Fatalf("close %+v %v", p, err)
	}
	if err := r.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatalf("close confirm with stale EMA: %v", err)
	}
	r2 := newRig(t, nil)
	r2.ready("SGRX", "111", 1.50)
	placeOrder(t, r2, "BUY")
	r2.advance(20 * time.Minute)
	if r2.tk.State().CancelCount != 1 {
		t.Fatal("cancel disabled")
	}
	if _, err := r2.tk.CancelSymbol("SGRX"); err != nil || len(cancels(r2)) != 2 {
		t.Fatalf("cancel with stale EMA: %v %v", err, cancels(r2))
	}
	if err := r2.tk.SetStaleMin(context.Background(), 30); err != nil || !r2.tk.State().EMAOK {
		t.Fatalf("staleMin 30: %v", err)
	}
	if err := r2.tk.SetStaleMin(context.Background(), 0); err == nil {
		t.Fatal("staleMin 0 accepted")
	}
}

func TestBaseCurrencyLabels(t *testing.T) {
	r := newRig(t, nil)
	r.f.avail = "2899.5"
	r.connect()
	waitFunds(t, r, 2899.5)
	r.f.w.AccountSummary(acctReqID, "U1234567", "AvailableFunds", "2899.5", "GBP")
	s := r.tk.State()
	if s.Currency != "GBP" || s.Funds.Currency != "GBP" {
		t.Fatalf("%+v", s.Funds)
	}
	r.seedBars("SGRX", 30, 1.5)
	r.show("111")
	p, err := r.tk.Prepare("BUY", 1000, "SGRX")
	if err != nil || !strings.HasPrefix(p.Margin.Text, "needs £") || !strings.Contains(p.Margin.Text, "(avail £2,899)") {
		t.Fatalf("%+v %v", p.Margin, err)
	}
	// both sides of the max calc are GBP from IB: 666 x 1.5 x 25% = 249.75 GBP -> 0.375/sh; 2899.5/0.375 = 7732 > cap 3333
	if p.Margin.MaxShares != 3333 {
		t.Fatalf("max %d", p.Margin.MaxShares)
	}
	if curSym("USD") != "$" || curSym("EUR") != "€" || curSym("CHF") != "CHF " {
		t.Fatal("curSym")
	}
}
