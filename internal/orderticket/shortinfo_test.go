package orderticket

import (
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func shortRig(t *testing.T, ref *core.ShortRef, age time.Duration) *rig {
	t.Helper()
	r := newRig(t, nil)
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
	r.ready("SGRX", "111", 1.50)
	return r
}

func TestShortInfoState(t *testing.T) {
	r := shortRig(t, &core.ShortRef{Ticker: "SGRX", Available: 150000, FeeRate: 12.5}, 5*time.Minute)
	si := r.tk.State().Short
	if si == nil || !si.Known || !si.Shortable || si.Avail != 150000 || si.Fee != 12.5 || si.AgeMin != 5 || si.Stale || si.Source == "" {
		t.Fatalf("%+v", si)
	}
	// not in the list
	r2 := shortRig(t, &core.ShortRef{Ticker: "OTHER"}, time.Minute)
	if si := r2.tk.State().Short; si.Known || si.Note != "not in IBKR's short list" {
		t.Fatalf("%+v", si)
	}
	// list never loaded / stale
	r3 := shortRig(t, nil, -1)
	if si := r3.tk.State().Short; !si.Stale || si.Note != "short list not loaded yet" {
		t.Fatalf("%+v", si)
	}
	r4 := shortRig(t, &core.ShortRef{Ticker: "SGRX", Available: 0}, 45*time.Minute)
	if si := r4.tk.State().Short; !si.Stale || si.Shortable || !strings.Contains(si.Note, "45m old") {
		t.Fatalf("%+v", si)
	}
	// no store wired: nil
	r5 := newRig(t, nil)
	r5.ready("SGRX", "111", 1.50)
	if r5.tk.State().Short != nil {
		t.Fatal("short info without a store")
	}
}

func TestShortConfirmLine(t *testing.T) {
	r := shortRig(t, &core.ShortRef{Ticker: "SGRX", Available: 150000, FeeRate: 12.5}, time.Minute)
	p, err := r.tk.Prepare("SELL", 1000, "SGRX")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p.Text, " · shortable 150K (fee 12.5%)") || strings.Contains(p.Warn, "short") {
		t.Fatalf("%q / %q", p.Text, p.Warn)
	}
	if p, _ = r.tk.Prepare("BUY", 1000, "SGRX"); strings.Contains(p.Text, "shortable") {
		t.Fatal("long shows short availability")
	}
	// fewer than wanted: amber warning, not blocked
	r2 := shortRig(t, &core.ShortRef{Ticker: "SGRX", Available: 100, FeeRate: 80}, time.Minute)
	p, err = r2.tk.Prepare("SELL", 1000, "SGRX")
	if err != nil || !strings.Contains(p.Warn, "only 100 shares available to short (666 wanted) — IB decides") {
		t.Fatalf("%q %v", p.Warn, err)
	}
	if err := r2.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatalf("blocked: %v", err)
	}
	// none
	r3 := shortRig(t, &core.ShortRef{Ticker: "SGRX", Available: 0}, time.Minute)
	if p, _ = r3.tk.Prepare("SELL", 1000, "SGRX"); !strings.Contains(p.Warn, "no shares to short") {
		t.Fatalf("%q", p.Warn)
	}
	// capped ">10000000" is a floor: never warns
	r4 := shortRig(t, &core.ShortRef{Ticker: "SGRX", Available: 10_000_000, Capped: true, FeeRate: 0.25}, time.Minute)
	if p, _ = r4.tk.Prepare("SELL", 1000, "SGRX"); !strings.Contains(p.Text, "shortable 10M+ (fee 0.25%)") || strings.Contains(p.Warn, "short") {
		t.Fatalf("%q / %q", p.Text, p.Warn)
	}
}

func TestFmtCompact(t *testing.T) {
	for n, want := range map[int64]string{950: "950", 1500: "1.5K", 2000: "2K", 150000: "150K", 1_250_000: "1.25M", 10_000_000: "10M"} {
		if got := fmtCompact(n); got != want {
			t.Errorf("%d: %q want %q", n, got, want)
		}
	}
}
