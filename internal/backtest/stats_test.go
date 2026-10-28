package backtest

import (
	"encoding/json"
	"math"
	"testing"
)

func TestBootstrapIsDeterministic(t *testing.T) {
	xs := make([]float64, 100)
	for i := range xs {
		xs[i] = float64(i)
	}
	lo1, hi1 := BootstrapMeanCI(xs, 10_000, 42)
	lo2, hi2 := BootstrapMeanCI(xs, 10_000, 42)
	if lo1 != lo2 || hi1 != hi2 {
		t.Fatalf("same data, same seed: (%v, %v) vs (%v, %v)", lo1, hi1, lo2, hi2)
	}
	// something that ran in between must not matter: no shared state
	BootstrapMeanCI([]float64{1, 2, 3}, 100, 7)
	lo3, hi3 := BootstrapMeanCI(xs, 10_000, 42)
	if lo1 != lo3 || hi1 != hi3 {
		t.Errorf("a different call in between changed the interval")
	}
	if lo4, hi4 := BootstrapMeanCI(xs, 10_000, 43); lo4 == lo1 && hi4 == hi1 {
		t.Errorf("a different seed gave the identical interval")
	}
	// 0..99: mean 49.5, sd 29.0, so the interval of the mean is about 49.5 +- 1.96 * 2.9 = (43.8, 55.2)
	if !(lo1 > 42 && lo1 < 46 && hi1 > 53 && hi1 < 57) {
		t.Errorf("interval (%v, %v), want about (43.8, 55.2)", lo1, hi1)
	}
	// constant data: a point
	if lo, hi := BootstrapMeanCI([]float64{3, 3, 3, 3}, 1000, 1); lo != 3 || hi != 3 {
		t.Errorf("constant data: (%v, %v)", lo, hi)
	}
	if lo, hi := BootstrapMeanCI(nil, 1000, 1); lo != 0 || hi != 0 {
		t.Errorf("no data: (%v, %v)", lo, hi)
	}
}

func tr(date string, net, gross, trigger float64, entryMin int) Trade {
	return Trade{Date: date, Net: net, Gross: gross, Trigger: trigger, EntryMin: entryMin, InitialRisk: 50, RNet: net / 50, RGross: gross / 50, Commission: 0.5, Fees: 0.2, Slippage: 1}
}

func TestSummarize(t *testing.T) {
	days := []string{"2026-09-01", "2026-09-02", "2026-09-03", "2026-09-04", "2026-10-01"}
	trades := []Trade{
		tr("2026-09-01", 100, 110, 0.5, 5*60),
		tr("2026-09-01", -40, -30, 3, 8*60),
		tr("2026-09-03", -80, -70, 8, 10*60),
		tr("2026-09-04", 20, 30, 12, 12*60),
	}
	s := Summarize("x", trades, days, StatOpts{Resamples: 2000, Seed: 1})
	if s.Trades != 4 || s.Days != 5 || s.Wins != 2 || s.WinRate != 0.5 {
		t.Errorf("counts: %+v", s)
	}
	if !approx(s.TotalNet, 0, 1e-9) || !approx(s.AvgNet, 0, 1e-9) || !approx(s.TotalGross, 40, 1e-9) || !approx(s.AvgGross, 10, 1e-9) {
		t.Errorf("totals: net %v avg %v gross %v", s.TotalNet, s.AvgNet, s.TotalGross)
	}
	if !approx(s.MedNet, -10, 1e-9) { // sorted -80 -40 20 100: (-40 + 20) / 2
		t.Errorf("median net = %v, want -10", s.MedNet)
	}
	if s.ProfitFactor == nil || !approx(*s.ProfitFactor, 120.0/120.0, 1e-9) {
		t.Errorf("profit factor = %v, want 1.0 (120 won / 120 lost)", s.ProfitFactor)
	}
	// daily net: +60, 0, -80, +20, 0: equity 60, 60, -20, 0, 0; peak 60, trough -20: drawdown 80
	if !approx(s.MaxDrawdown, 80, 1e-9) {
		t.Errorf("max drawdown = %v, want 80", s.MaxDrawdown)
	}
	if s.BestDay != 60 || s.BestDayDate != "2026-09-01" || s.WorstDay != -80 || s.WorstDayDt != "2026-09-03" {
		t.Errorf("best %v on %s, worst %v on %s", s.BestDay, s.BestDayDate, s.WorstDay, s.WorstDayDt)
	}
	if s.DaysWithTrade != 3 || !approx(s.PctDaysWithTrade, 60, 1e-9) || !approx(s.AvgTradesPerDay, 0.8, 1e-9) {
		t.Errorf("days with a trade %d (%v%%), trades/day %v", s.DaysWithTrade, s.PctDaysWithTrade, s.AvgTradesPerDay)
	}
	if !approx(s.DayMeanNet, 0, 1e-9) {
		t.Errorf("mean net per day = %v", s.DayMeanNet)
	}
	if !approx(s.Commission, 4*0.7, 1e-9) || !approx(s.Slippage, 4, 1e-9) {
		t.Errorf("costs: commission+fees %v slippage %v", s.Commission, s.Slippage)
	}
	if !(s.NetCILo <= s.AvgNet && s.AvgNet <= s.NetCIHi) {
		t.Errorf("CI (%v, %v) does not hold the mean %v", s.NetCILo, s.NetCIHi, s.AvgNet)
	}
	if !approx(s.AvgRNet, 0, 1e-9) || !approx(s.AvgRGros, 0.2, 1e-9) {
		t.Errorf("R: net %v gross %v", s.AvgRNet, s.AvgRGros)
	}

	// no losing trade: no profit factor; no trade: all zeros, nothing NaN, and it must marshal
	win := Summarize("w", []Trade{tr("2026-09-01", 5, 5, 1, 300)}, days, StatOpts{Resamples: 100, Seed: 1})
	if win.ProfitFactor != nil {
		t.Errorf("profit factor with no losing trade = %v, want nil", *win.ProfitFactor)
	}
	empty := Summarize("e", nil, days, StatOpts{Resamples: 100, Seed: 1})
	b, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("empty summary does not marshal: %v", err)
	}
	if empty.Trades != 0 || empty.WinRate != 0 || math.IsNaN(empty.AvgNet) || empty.ProfitFactor != nil {
		t.Errorf("empty summary: %s", b)
	}
	if _, err := json.Marshal(Summarize("nodays", nil, nil, StatOpts{Resamples: 10, Seed: 1})); err != nil {
		t.Errorf("no days: %v", err)
	}
}

func TestSplits(t *testing.T) {
	cases := []struct {
		trigger float64
		band    string
	}{{0.2, "under $1"}, {0.9999, "under $1"}, {1, "$1 - $5"}, {4.99, "$1 - $5"}, {5, "$5 - $50"}, {49.9, "$5 - $50"}}
	for _, k := range cases {
		if got := PriceBand(Trade{Trigger: k.trigger}); got != k.band {
			t.Errorf("PriceBand(%v) = %q, want %q", k.trigger, got, k.band)
		}
	}
	tcases := []struct {
		hhmm   string
		bucket string
	}{{"04:00", "04:00-07:00"}, {"06:59", "04:00-07:00"}, {"07:00", "07:00-09:30"}, {"09:29", "07:00-09:30"},
		{"09:30", "09:30-11:00"}, {"10:59", "09:30-11:00"}, {"11:00", "11:00-15:45"}, {"15:44", "11:00-15:45"}}
	for _, k := range tcases {
		m, _ := ParseHHMM(k.hhmm)
		if got := TimeBucket(Trade{EntryMin: m}); got != k.bucket {
			t.Errorf("TimeBucket(%s) = %q, want %q", k.hhmm, got, k.bucket)
		}
	}
	trades := []Trade{
		tr("2026-09-01", 100, 110, 0.5, 5*60),
		tr("2026-09-02", -40, -30, 3, 8*60),
		tr("2026-10-03", -80, -70, 8, 10*60),
		tr("2026-10-04", 20, 30, 12, 12*60),
	}
	sp := SplitAll(trades)
	if len(sp.Price) != 3 || sp.Price[0].Trades != 1 || sp.Price[1].Trades != 1 || sp.Price[2].Trades != 2 {
		t.Errorf("price split: %+v", sp.Price)
	}
	if len(sp.Time) != 4 || sp.Time[0].Trades != 1 || sp.Time[3].Trades != 1 {
		t.Errorf("time split: %+v", sp.Time)
	}
	if len(sp.Month) != 2 || sp.Month[0].Label != "2026-09" || sp.Month[0].Trades != 2 || !approx(sp.Month[0].TotalNet, 60, 1e-9) || !approx(sp.Month[1].TotalNet, -60, 1e-9) {
		t.Errorf("month split: %+v", sp.Month)
	}
	if !approx(sp.Month[0].WinRate, 0.5, 1e-9) || !approx(sp.Month[0].AvgNet, 30, 1e-9) {
		t.Errorf("month 09: %+v", sp.Month[0])
	}
}
