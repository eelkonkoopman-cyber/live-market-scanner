package backtest

import (
	"bytes"
	"encoding/csv"
	"math"
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestSplitIndexFactor(t *testing.T) {
	x := NewSplitIndex([]StockSplit{
		{Sym: "REV", Date: "2026-09-10", From: 20, To: 1}, // 1-for-20 reverse
		{Sym: "FWD", Date: "2026-09-20", From: 1, To: 2},  // 2-for-1 forward
		{Sym: "TWO", Date: "2026-09-30", From: 10, To: 1},
		{Sym: "TWO", Date: "2026-09-15", From: 2, To: 1}, // out of order on purpose
		{Sym: "BAD", Date: "2026-09-20", From: 0, To: 1},
		{Sym: "BAD2", Date: "2026-09-20", From: 1, To: math.NaN()},
		{Sym: "", Date: "2026-09-20", From: 1, To: 2},
	})
	if x.Len() != 4 {
		t.Errorf("Len = %d, want 4 (the three invalid rows are dropped)", x.Len())
	}
	for _, c := range []struct {
		sym, date string
		want      float64
	}{
		{"REV", "2026-09-09", 20},  // the split is after the day: history was multiplied by 20
		{"REV", "2026-09-10", 1},   // executed ON the day: the day is already post-split, nothing to undo
		{"REV", "2026-09-11", 1},   // before the day: nothing to undo
		{"FWD", "2026-09-19", 0.5}, // a forward split divided the adjusted history
		{"TWO", "2026-09-14", 20},  // both are after the day: 2 x 10
		{"TWO", "2026-09-15", 10},  // the first executed that day
		{"TWO", "2026-10-01", 1},
		{"NONE", "2026-09-01", 1},
		{"BAD", "2026-09-01", 1},
	} {
		if got := x.Factor(c.sym, c.date); got != c.want {
			t.Errorf("Factor(%s, %s) = %v, want %v", c.sym, c.date, got, c.want)
		}
	}
	var nilIdx *SplitIndex
	if nilIdx.Factor("REV", "2026-09-01") != 1 || nilIdx.Len() != 0 {
		t.Errorf("a nil index is no splits")
	}
}

func TestUnadjustBars(t *testing.T) {
	in := []core.Bar{
		{T: 1, O: 6.0, H: 6.4, L: 5.8, C: 6.2, V: 5000},
		{T: 2, O: 3.0, H: 3.0, L: 3.0, C: 3.0, V: 7},
	}
	orig := append([]core.Bar(nil), in...)
	out := UnadjustBars(in, 20) // a 1-for-20 reverse split after the day
	if len(out) != 2 || !approx(out[0].O, 0.30, 1e-12) || !approx(out[0].H, 0.32, 1e-12) || !approx(out[0].L, 0.29, 1e-12) || !approx(out[0].C, 0.31, 1e-12) || out[0].V != 100_000 || out[1].V != 140 || out[0].T != 1 {
		t.Errorf("1:20: %+v", out)
	}
	for i := range in {
		if in[i] != orig[i] {
			t.Errorf("the input was modified: %+v", in)
		}
	}
	// a forward split (factor 0.5): the real price was double, the real volume half; a fractional share count is rounded
	fw := UnadjustBars([]core.Bar{{T: 1, O: 5, H: 5, L: 5, C: 5, V: 101}}, 0.5)
	if fw[0].C != 10 || fw[0].V != 51 { // 50.5 rounds half away from zero
		t.Errorf("2:1 forward: %+v", fw[0])
	}
	// no split, or a factor that is not usable: the same bars, untouched
	for _, f := range []float64{1, 0, -2, math.NaN(), math.Inf(1)} {
		got := UnadjustBars(in, f)
		if len(got) != 2 || got[0] != in[0] || got[1] != in[1] {
			t.Errorf("factor %v changed the bars: %+v", f, got)
		}
	}
	if got := UnadjustBars(nil, 20); len(got) != 0 {
		t.Errorf("nil bars -> %v", got)
	}
}

// A name that reverse-split after the day is replayed on the basis the trader saw.
func TestPrefilterUsesTheBasisTheTraderSaw(t *testing.T) {
	hist := make([]map[string]core.DailyBar, 10)
	for i := range hist {
		hist[i] = map[string]core.DailyBar{
			"REV20":  dbar(6.0, 6.0, 50), // $0.30 a share before a 1-for-20 reverse split after the day: 1000 shares a day then
			"REV20B": dbar(3.0, 3.0, 50), // $0.15 then: under the $0.20 filter
			"FWD2":   dbar(30, 30, 4000), // $60 then, before a 2-for-1 forward split: over the $50 filter
		}
	}
	today := map[string]core.DailyBar{
		"REV20":  dfull(6.0, 7.0, 5.8, 6.6, 200),
		"REV20B": dfull(3.0, 3.2, 2.9, 3.1, 200),
		"FWD2":   dfull(30, 31, 29, 30, 9000),
	}
	factors := map[string]float64{"REV20": 20, "REV20B": 20, "FWD2": 0.5}

	// as the feed hands it over (adjusted, no factor): all three look like candidates in the $0.20-$50 range
	got, st := Prefilter("2026-10-05", today, hist, DefaultPrefilter())
	if len(got) != 3 {
		t.Fatalf("unadjusted: kept %d (%+v)", len(got), st)
	}

	o := DefaultPrefilter()
	o.SplitFactor = func(sym string) float64 { return factors[sym] } // 0 for a name with no split: treated as 1
	got, st = Prefilter("2026-10-05", today, hist, o)
	if len(got) != 1 || got[0].Sym != "REV20" || st.OutOfPrice != 2 {
		t.Fatalf("with the factors: kept %+v, stats %+v, want only REV20 ($0.15 real and $60 real are out of the price range)", got, st)
	}
	c := got[0]
	// real: prices / 20, volumes * 20; the ratio does not change
	if !approx(c.PrevClose, 0.30, 1e-12) || !approx(c.DayHigh, 0.35, 1e-12) || c.DayVol != 4000 || c.Adv10 != 1000 || c.Ratio != 4 || c.SplitFactor != 20 {
		t.Errorf("REV20: %+v", c)
	}
}

// ─── Sizing and costs on the real share count ────────────────────────────

// The same $2000 position in a $0.30 stock that reverse-split 1-for-20 after the
// day: the feed shows $6.00 and a twentieth of the shares, so costs per share
// (commission $0.0035 with a 1% cap, fees $0.002) shrank twentyfold and a $0.15
// trigger looked like $3.00 and passed the $0.20 filter. Un-adjusted, the trade
// is the one the live bot would have made.
func TestRealBasisSizingCostsAndPriceFilter(t *testing.T) {
	r := rulesNamed(t, "V1")
	const split = 20.0

	// the feed's view: $6.00, 5k a minute  ->  the real tape: $0.30, 100k a minute
	adjBars := day(t, 6.00, 5_000)
	realBars := UnadjustBars(adjBars, split)
	if realBars[0].C != 0.30 || realBars[0].V != 100_000 {
		t.Fatalf("setup: real bar %+v", realBars[0])
	}
	cReal := &Candidate{Sym: "SPL", Date: "2026-10-05", Adv10: 1e6, Bars: realBars, SplitFactor: split}
	// trigger 0.30, stop 0.2925: 50 / 0.0075 and 2000 / 0.30 are both 6666 shares
	dr := SimulateDay(dayOf(sigAt(t, cReal, "2026-10-05 09:00", 0.30, 0.2925, 1)), r)
	tr := tradeOf(t, dr, "SPL")
	if tr.Shares != 6666 {
		t.Fatalf("real basis: %d shares, want 6666", tr.Shares)
	}
	realCost := tr.Commission + tr.Fees
	if !approx(realCost, 66.66, 0.1) { // the review's figure: commission (1% cap) 2 x ~$20 + fees 2 x $13.33
		t.Errorf("real round-trip commission + fees = %.2f, want about 66.66", realCost)
	}
	if tr.SplitFactor != split {
		t.Errorf("trade split factor = %v, want 20", tr.SplitFactor)
	}

	// what the replay used to do: the adjusted tape taken at face value
	cAdj := mkCand("ADJ", adjBars)
	adr := SimulateDay(dayOf(sigAt(t, cAdj, "2026-10-05 09:00", 6.00, 5.85, 1)), r)
	atr := tradeOf(t, adr, "ADJ")
	adjCost := atr.Commission + atr.Fees
	if atr.Shares != 333 || !approx(adjCost, 3.66, 0.05) {
		t.Errorf("adjusted basis: %d shares costing %.2f, want 333 shares and about 3.66", atr.Shares, adjCost)
	}
	if atr.SplitFactor != 1 {
		t.Errorf("a trade with no split has factor %v, want 1", atr.SplitFactor)
	}
	// the gap the trade carries is what the adjusted prices would have understated
	if !approx(tr.SplitCostGap, realCost-adjCost, 0.01) || tr.SplitCostGap < 60 {
		t.Errorf("SplitCostGap = %.2f, want about %.2f (real %.2f - adjusted %.2f)", tr.SplitCostGap, realCost-adjCost, realCost, adjCost)
	}
	if atr.SplitCostGap != 0 {
		t.Errorf("no split, no gap: %v", atr.SplitCostGap)
	}

	// the price filter: a real $0.15 trigger is under $0.20, though the adjusted $3.00 passes
	cReal3 := &Candidate{Sym: "SPL3", Date: "2026-10-05", Adv10: 1e6, Bars: UnadjustBars(day(t, 3.00, 5_000), split), SplitFactor: split}
	d3 := SimulateDay(dayOf(sigAt(t, cReal3, "2026-10-05 09:00", 0.15, 0.14, 1)), r)
	if len(d3.Trades) != 0 || d3.Skips[SkipPrice] != 1 {
		t.Errorf("real $0.15: trades %v skips %v, want the price filter to skip it", symsOf(d3), d3.Skips)
	}
	cAdj3 := mkCand("ADJ3", day(t, 3.00, 5_000))
	if a3 := SimulateDay(dayOf(sigAt(t, cAdj3, "2026-10-05 09:00", 3.00, 2.9, 1)), r); len(a3.Trades) != 1 {
		t.Errorf("the adjusted $3.00 trades (the old behaviour): %v", a3.Skips)
	}
}

// The report separates the trades on names that split after the day.
func TestReportSeparatesSplitTrades(t *testing.T) {
	r := rulesNamed(t, "V1")
	cSplit := &Candidate{Sym: "SPL", Date: "2026-10-05", Adv10: 1e6, Bars: UnadjustBars(day(t, 6.00, 5_000), 20), SplitFactor: 20}
	cPlain := flatDay(t, "AAA")
	pd := dayOf(sigAt(t, cSplit, "2026-10-05 09:00", 0.30, 0.2925, 1), sigAt(t, cPlain, "2026-10-05 09:00", 10, 9.5, 2))
	vr := RunVariant([]PreparedDay{pd}, r)
	if len(vr.Trades) != 2 {
		t.Fatalf("trades %v", vr.Trades)
	}
	meta := RunMeta{Days: []string{"2026-10-05"}, Data: DataStats{SplitsKnown: true, SplitsAsOf: "2026-10-06", SplitsInWindow: 12}}
	rep := BuildReport(meta, []VariantResult{vr}, StatOpts{Resamples: 200, Seed: 1})
	si := rep.Variants[0].SplitImpact
	var splitNet, otherNet, gap float64
	for _, tr := range vr.Trades {
		if tr.Sym == "SPL" {
			splitNet, gap = tr.Net, tr.SplitCostGap
		} else {
			otherNet = tr.Net
		}
	}
	if si.Trades != 1 || !approx(si.TotalNet, splitNet, 1e-9) || !approx(si.AvgNet, splitNet, 1e-9) || !approx(si.OthersAvgNet, otherNet, 1e-9) || !approx(si.CostGap, gap, 1e-9) || gap < 60 {
		t.Errorf("split impact %+v (split net %.2f, others %.2f, gap %.2f)", si, splitNet, otherNet, gap)
	}
	md := rep.Markdown()
	for _, want := range []string{"split after the day", "un-adjusted", "2026-10-06"} {
		if !strings.Contains(md, want) {
			t.Errorf("report.md lacks %q", want)
		}
	}

	// trades.csv carries the factor
	var buf bytes.Buffer
	if err := WriteTradesCSV(&buf, []VariantResult{vr}); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	col := -1
	for i, h := range rows[0] {
		if h == "split_factor" {
			col = i
		}
	}
	if col < 0 {
		t.Fatalf("no split_factor column in %v", rows[0])
	}
	got := map[string]string{}
	for _, row := range rows[1:] {
		got[row[2]] = row[col]
	}
	if got["SPL"] != "20" || got["AAA"] != "1" {
		t.Errorf("split_factor by symbol: %v", got)
	}
}
