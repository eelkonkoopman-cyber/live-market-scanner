package orderticket

import (
	"context"
	"strings"
	"testing"
)

func TestRiskSizeMath(t *testing.T) {
	// long: limit 2.43, stop 2.40 -> 0.03/sh; 50/0.03 = 1666.67 -> 1666 (down)
	if q, b := riskSize("BUY", 50, 2.43, 2.40, 1e6); q != 1666 || b != "" {
		t.Fatalf("long %d %q", q, b)
	}
	if q, _ := riskSize("SELL", 50, 2.43, 2.46, 1e6); q != 1666 {
		t.Fatalf("short %d", q)
	}
	// fallback rule: EMA level at/through the limit -> limit x (1 - SL%)
	stop := stopLevelFor("BUY", 2.10, 2.00, 1) // 2.079 >= 2.00 -> 1.98
	if !near(stop, 1.98) {
		t.Fatalf("stop %v", stop)
	}
	if q, _ := riskSize("BUY", 50, 2.00, stop, 1e6); q != 2500 {
		t.Fatalf("fallback %d", q)
	}
	// cap trims and says so
	if q, b := riskSize("BUY", 100, 1.50, 1.48, 5000); q != 3333 || b != "max position $" {
		t.Fatalf("cap %d %q", q, b)
	}
	if q, _ := riskSize("BUY", 50, 1.50, 1.50, 5000); q != 0 {
		t.Fatal("zero distance sized")
	}
}

func TestPrepareByRisk(t *testing.T) {
	r := newRig(t, nil)
	r.ready("SGRX", "111", 1.50)
	// limit 1.50, stop 1.485 -> 1.48: 0.02/sh -> $50 = 2500 sh
	p, err := r.tk.PrepareOrder("BUY", 0, 0, "SGRX", 0, false, 50)
	if err != nil {
		t.Fatal(err)
	}
	if p.Qty != 2500 || !near(p.PlannedStop, 1.48) || p.Bound != "" || !strings.Contains(p.Text, ", risk $50 (stop 1.48), SL 1% under 9EMA (soft)") {
		t.Fatalf("%+v", p)
	}
	// short: stop 1.515 -> 1.52
	if p, _ = r.tk.PrepareOrder("SELL", 0, 0, "SGRX", 0, false, 50); p.Qty != 2500 || !near(p.PlannedStop, 1.52) {
		t.Fatalf("short %+v", p)
	}
	// max position $ cap
	p, _ = r.tk.PrepareOrder("BUY", 0, 0, "SGRX", 0, false, 100)
	if p.Qty != 3333 || p.Bound != "max position $" || !strings.Contains(p.Text, "[max position $]") {
		t.Fatalf("cap %+v", p)
	}
	// SL off: still sized from the SL% setting, flagged notional
	p, _ = r.tk.PrepareOrder("BUY", 0, 0, "SGRX", 0, true, 50)
	if p.Qty != 2500 || !strings.Contains(p.Text, "(SL off — risk is notional), no SL") {
		t.Fatalf("SL off %q", p.Text)
	}
	// shares override wins; the risk it implies is reported
	p, _ = r.tk.PrepareOrder("BUY", 0, 300, "SGRX", 0, false, 50)
	if p.Qty != 300 || !near(p.Risk, 6) || !strings.Contains(p.Text, "risk $6 (stop 1.48)") {
		t.Fatalf("override %+v", p)
	}
	// legacy position $ still accepted, no risk part
	p, _ = r.tk.PrepareOrder("BUY", 600, 0, "SGRX", 0, false, 0)
	if p.Qty != 400 || strings.Contains(p.Text, "risk $") {
		t.Fatalf("legacy %+v", p)
	}
	if _, err := r.tk.PrepareOrder("BUY", 0, 0, "SGRX", 0, false, 0); err == nil {
		t.Fatal("no size accepted")
	}
}

func TestRiskTrimmedByMarginMax(t *testing.T) {
	r := newRig(t, nil)
	r.f.avail = "500" // 25% margin: 0.375/sh -> max 1333
	r.ready("SGRX", "111", 1.50)
	waitFunds(t, r, 500)
	p, err := r.tk.PrepareOrder("BUY", 0, 0, "SGRX", 0, false, 50)
	if err != nil {
		t.Fatal(err)
	}
	if p.Qty != 1333 || p.Bound != "margin max" || p.Margin.Status != "ok" || !strings.Contains(p.Text, "[margin max]") {
		t.Fatalf("%+v %+v", p, p.Margin)
	}
	if err := r.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatal(err)
	}
	if ps := r.f.placesCopy(); ps[0].o.TotalQuantity.Int() != 1333 {
		t.Fatalf("%+v", ps[0].o)
	}
}

func TestRiskPresetsAndPreview(t *testing.T) {
	r := newRig(t, nil)
	ctx := context.Background()
	if err := r.tk.SetRiskPresets(ctx, 10, 20, 30); err != nil || !strings.Contains(r.meta[MetaKey], `"riskMid":20`) {
		t.Fatalf("%v %s", err, r.meta[MetaKey])
	}
	for _, bad := range [][3]float64{{0.5, 20, 30}, {10, 20, 6000}} {
		if err := r.tk.SetRiskPresets(ctx, bad[0], bad[1], bad[2]); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
	if s := DefaultSettings(); s.RiskLow != 25 || s.RiskMid != 50 || s.RiskHigh != 100 {
		t.Fatalf("%+v", s)
	}
	r.ready("SGRX", "111", 1.50)
	r.tk.SetPreviewSize(0, 0, 50)
	z := r.tk.State().Sizing
	if z == nil || z.Qty != 2500 || !near(z.Stop, 1.48) || !near(z.Position, 3750) || !near(z.Risk, 50) {
		t.Fatalf("sizing %+v", z)
	}
	r.tk.Tick(ctx)
	waitPreview(t, r)
	for _, w := range r.f.whatIfsCopy() {
		if w.o.TotalQuantity.Int() != 2500 {
			t.Fatalf("preview not risk-sized: %+v", w.o)
		}
	}
}
