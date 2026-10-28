package backtest

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/emaflag"
	"github.com/eelkonkoopman/marketscanner/internal/flagscan"
)

// The shipped variant's engine inputs match the live 9EMA flag scan (the
// auto-trader whose defaults it also mirrored has been removed).
func TestShippedRulesMatchFlagScan(t *testing.T) {
	r := ShippedRules()
	if r.MaxCandidates != flagscan.MaxCandidates {
		t.Errorf("candidate cap %d vs flagscan %d", r.MaxCandidates, flagscan.MaxCandidates)
	}
	if r.Flag != emaflag.DefaultParams() {
		t.Errorf("engine params are not emaflag.DefaultParams()")
	}
	if MinPmVolX != flagscan.MinPmVolX {
		t.Errorf("pmVolX threshold %v vs flagscan %v", MinPmVolX, flagscan.MinPmVolX)
	}
}

func TestVariants(t *testing.T) {
	vs := Variants()
	if len(vs) != 5 {
		t.Fatalf("%d variants", len(vs))
	}
	for i, v := range vs {
		if err := v.Validate(); err != nil {
			t.Errorf("%s: %v", v.Name, err)
		}
		if v.Name != "V"+string(rune('1'+i)) || v.Desc == "" {
			t.Errorf("variant %d: %q %q", i, v.Name, v.Desc)
		}
	}
	// the table of the task
	if v := vs[0]; v.TrailExit != TrailStop || v.TrailStopPct != 1 || v.WindowEndMin != 11*60 || v.MaxTradesPerDay != 10 || v.MaxDailyLossUSD != 200 {
		t.Errorf("V1: %+v", v)
	}
	if v := vs[1]; v.TrailStopPct != 0.5 || v.TrailExit != TrailStop || v.WindowEndMin != 11*60 {
		t.Errorf("V2: %+v", v)
	}
	if v := vs[2]; v.TrailExit != TrailClose || v.WindowEndMin != 11*60 || v.MaxTradesPerDay != 10 {
		t.Errorf("V3: %+v", v)
	}
	if v := vs[3]; v.TrailStopPct != 1 || v.WindowEndMin != 15*60+45 || v.MaxTradesPerDay != 10 {
		t.Errorf("V4: %+v", v)
	}
	if v := vs[4]; v.TrailStopPct != 1 || v.WindowEndMin != 11*60 || v.MaxTradesPerDay != 5 || v.MaxDailyLossUSD != 150 {
		t.Errorf("V5: %+v", v)
	}
	if _, ok := VariantByName("v3"); !ok {
		t.Errorf("VariantByName is case-insensitive")
	}
	if _, ok := VariantByName("V9"); ok {
		t.Errorf("V9 does not exist")
	}
	bad := vs[0]
	bad.WindowEndMin = bad.WindowStartMin
	if bad.Validate() == nil {
		t.Errorf("an empty window must not validate")
	}
}
