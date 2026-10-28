package orderticket

import (
	"context"
	"strings"
	"testing"
)

func (r *rig) acct(tag, value, cur string) {
	r.tk.mu.Lock()
	gen, acct := r.tk.gen, r.tk.account
	r.tk.mu.Unlock()
	r.tk.onAccountSummary(gen, acctReqID, acct, tag, value, cur)
}

func TestAcctTagsAskForUSDLedger(t *testing.T) {
	// byte-level: the exact frame Connect now writes for the account summary
	got := encReqAccountSummary(acctReqID, "All", acctTags)
	eqFields(t, "reqAccountSummary", got, []string{"62", "1", "8002", "All",
		"AvailableFunds,ExcessLiquidity,BuyingPower,NetLiquidation,$LEDGER:USD"})
}

func TestPctRiskUSDConversion(t *testing.T) {
	r := newRig(t, nil)
	r.f.avail = "" // only this test's rows
	r.connect()
	r.acct("AvailableFunds", "3000", "GBP")
	r.acct("NetLiquidation", "2800", "GBP")
	f := r.tk.State().Funds
	if f == nil || f.HaveUSD || !strings.Contains(f.USDNote, "rate") {
		t.Fatalf("no rate must leave %% off: %+v", f)
	}
	// ledger rows in USD: never overwrite base values; ExchangeRate is kept
	r.acct("NetLiquidationByCurrency", "99", "USD")
	r.acct("NetLiquidation", "99", "USD")
	r.acct("ExchangeRate", "0.8", "USD")
	f = r.tk.State().Funds
	if !f.HaveUSD || f.NetLiq != 2800 || !near(f.USDRate, 0.8) || !near(f.NetLiqUSD, 3500) {
		t.Fatalf("%+v", f)
	}
	r.acct("ExchangeRate", "0", "USD") // a zero rate is ignored, never stored
	if f = r.tk.State().Funds; !near(f.USDRate, 0.8) {
		t.Fatalf("%+v", f)
	}
}

func TestPctRiskUSDAccountNeedsNoRate(t *testing.T) {
	f := &Funds{Currency: "USD"}
	f.usdView(5000, true, 0)
	if !f.HaveUSD || f.NetLiqUSD != 5000 {
		t.Fatalf("%+v", f)
	}
	g := &Funds{Currency: "GBP"}
	g.usdView(5000, true, 0)
	if g.HaveUSD || g.NetLiqUSD != 0 {
		t.Fatalf("%+v", g)
	}
	h := &Funds{Currency: "GBP"}
	h.usdView(0, false, 0.8)
	if h.HaveUSD {
		t.Fatalf("%+v", h)
	}
}

func TestPctRiskSettings(t *testing.T) {
	r := newRig(t, nil)
	ctx := context.Background()
	if s := DefaultSettings(); s.RiskMode != "$" || s.RiskPctLow != 1 || s.RiskPctMid != 2 || s.RiskPctHigh != 3 {
		t.Fatalf("%+v", s)
	}
	if s := (Settings{}).withTPDefaults(); s.RiskMode != "$" || s.RiskPctHigh != 3 {
		t.Fatalf("old saves must default to $ mode: %+v", s)
	}
	if err := r.tk.SetRiskPctPresets(ctx, 0.5, 1.5, 4.5); err != nil || !strings.Contains(r.meta[MetaKey], `"riskPctHigh":4.5`) {
		t.Fatalf("%v %s", err, r.meta[MetaKey])
	}
	for _, bad := range [][3]float64{{0, 1, 2}, {1, 2, 11}} {
		if err := r.tk.SetRiskPctPresets(ctx, bad[0], bad[1], bad[2]); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
	if err := r.tk.SetRiskMode(ctx, "x"); err == nil {
		t.Fatal("bad mode accepted")
	}
	if err := r.tk.SetRiskMode(ctx, "%"); err != nil || !strings.Contains(r.meta[MetaKey], `"riskMode":"%"`) {
		t.Fatalf("%v %s", err, r.meta[MetaKey])
	}
	_ = r.tk.SetRiskMode(ctx, "$")
	if err := r.tk.SetPreset(ctx, presetCustom, false, 0); err != nil || r.tk.State().Settings.RiskMode != "$" {
		t.Fatal("custom must keep the mode")
	}
	if err := r.tk.SetPreset(ctx, presetPower, false, 0); err != nil || r.tk.State().Settings.RiskMode != "%" {
		t.Fatal("power must switch to %")
	}
	// the connection form (no % fields) keeps them
	s := r.tk.State().Settings
	s.RiskMode, s.RiskPctLow, s.RiskPctMid, s.RiskPctHigh = "", 0, 0, 0
	if err := r.tk.SetSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	if g := r.tk.State().Settings; g.RiskMode != "%" || g.RiskPctHigh != 4.5 {
		t.Fatalf("%+v", g)
	}
}
