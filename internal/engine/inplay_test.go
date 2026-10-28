package engine

import (
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// The in-play ranking (the auto-trader's PMH break universe): day volume / adv10,
// descending with a symbol tie-break, capped; unknown averages and silent names absent; the
// pre-market high only once final and complete.
func TestInPlayLeadersRanksDayVolumeOverAdv10(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	e, _ := pmvolEngine(t, time.Date(2026, 10, 5, 8, 0, 0, 0, ny))
	e.SetSession(core.SessionPre)
	_ = e.Curves(0)
	vols := map[string][2]int64{
		"BIG":   {6_000_000, 1_000_000}, // 6x
		"TIE":   {3_000_000, 1_000_000}, // 3x
		"ATIE":  {3_000_000, 1_000_000}, // 3x: before TIE by symbol
		"SMALL": {100_000, 1_000_000},   // 0.1x
		"NOADV": {9_000_000, 0},         // unknown average: absent
	}
	for sym, v := range vols {
		e.Apply(volAgg(sym, v[0]))
	}
	for sym := range vols {
		waitForSym(t, e, sym, 3*time.Second)
	}
	adv := map[string]float64{}
	for sym, v := range vols {
		if v[1] > 0 {
			adv[sym] = float64(v[1])
		}
	}
	e.SetAdv10(adv)
	_ = e.Curves(0)
	got := e.InPlayLeaders(0)
	var syms []string
	for _, g := range got {
		syms = append(syms, g.Sym)
	}
	if len(got) != 4 || syms[0] != "BIG" || syms[1] != "ATIE" || syms[2] != "TIE" || syms[3] != "SMALL" || got[0].X != 6 {
		t.Fatalf("ranking %+v", got)
	}
	if got[0].Last <= 0 || got[0].Pmh != 0 {
		t.Fatalf("BIG %+v: a last price, no final pre-market high before 09:30", got[0])
	}
	if top := e.InPlayLeaders(2); len(top) != 2 || top[1].Sym != "ATIE" {
		t.Fatalf("top 2 %+v", top)
	}
}
