package backtest

import (
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/emaflag"
)

func dbar(c, h float64, v int64) core.DailyBar { return core.DailyBar{O: c, H: h, L: c, C: c, V: v} }

func TestADV10(t *testing.T) {
	var hist []map[string]core.DailyBar
	for i := 0; i < 10; i++ {
		day := map[string]core.DailyBar{"FULL": dbar(1, 1, 1000)}
		if i < 4 {
			day["FOUR"] = dbar(1, 1, 500)
		}
		if i < 5 {
			day["FIVE"] = dbar(1, 1, int64(100*(i+1)))
		}
		hist = append(hist, day)
	}
	if v, ok := ADV10("FULL", hist, 5); !ok || v != 1000 {
		t.Errorf("FULL = %v %v", v, ok)
	}
	if _, ok := ADV10("FOUR", hist, 5); ok {
		t.Errorf("4 days of history: want unknown (needs 5)")
	}
	if v, ok := ADV10("FIVE", hist, 5); !ok || v != 300 { // (100+200+300+400+500)/5
		t.Errorf("FIVE = %v %v, want the mean over the 5 days present = 300", v, ok)
	}
	if _, ok := ADV10("NONE", hist, 5); ok {
		t.Errorf("unknown symbol")
	}
}

// dfull is a coherent daily bar.
func dfull(o, h, l, c float64, v int64) core.DailyBar {
	return core.DailyBar{O: o, H: h, L: l, C: c, V: v}
}

func TestPrefilter(t *testing.T) {
	hist := make([]map[string]core.DailyBar, 10)
	for i := range hist {
		hist[i] = map[string]core.DailyBar{}
	}
	for _, s := range []struct {
		sym   string
		close float64
	}{{"GOOD", 2}, {"GOOD2", 4}, {"CHEAP", 0.1}, {"PRICEY", 80}, {"QUIET", 2}, {"FLAT", 2}, {"EDGE", 50}, {"BOT", 0.2},
		{"RUNNER", 0.18}, {"FALLER", 1}} {
		for i := range hist {
			hist[i][s.sym] = dbar(s.close, s.close, 1000) // adv10 = 1000
		}
	}
	// a name with only 4 days of history (the last four, so it has a previous close)
	for i := 6; i < 10; i++ {
		hist[i]["SHORT"] = dbar(2, 2, 1000)
	}
	today := map[string]core.DailyBar{
		"GOOD":   dfull(2.5, 2.7, 2.4, 2.5, 5000),    // ratio 5; gapped up and ran
		"GOOD2":  dfull(4.4, 4.5, 4.3, 4.4, 3000),    // ratio 3
		"CHEAP":  dfull(0.1, 0.15, 0.1, 0.1, 9000),   // the whole day under $0.20: never in the price filter
		"PRICEY": dfull(80, 90, 80, 85, 9000),        // the whole day over $50
		"QUIET":  dfull(2, 2.5, 2, 2.2, 999),         // dayVol < adv10
		"FLAT":   dfull(2, 2.04, 2, 2.02, 5000),      // range within 2%: no 5% impulse over any EMA was possible
		"EDGE":   dfull(50, 52.5, 50, 52, 1000),      // yesterday's close 50 inside; volume == adv10
		"BOT":    dfull(0.2, 0.2101, 0.2, 0.2, 2000), // yesterday's close 0.20 inside the range
		"NEW":    dfull(2, 3, 2, 3, 9000),            // no previous bar
		"SHORT":  dfull(2, 3, 2, 3, 9000),            // 4 days of history
		// yesterday's close ($0.18) is under the price filter but the name ran to $0.45: the trader filters on the
		// trigger price, so it is a candidate (the first screen tested yesterday's close and dropped it)
		"RUNNER": dfull(0.19, 0.45, 0.17, 0.4, 4000),
		// gapped DOWN from 1.00 and never got near 5% over yesterday's close: still a candidate
		"FALLER": dfull(0.72, 0.76, 0.70, 0.71, 2500),
	}
	got, st := Prefilter("2026-10-05", today, hist, DefaultPrefilter())
	var syms []string
	for _, c := range got {
		syms = append(syms, c.Sym)
	}
	// ratios: GOOD 5, RUNNER 4, GOOD2 3, FALLER 2.5, BOT 2, EDGE 1
	if strings.Join(syms, " ") != "GOOD RUNNER GOOD2 FALLER BOT EDGE" {
		t.Fatalf("kept %v (stats %+v), want GOOD RUNNER GOOD2 FALLER BOT EDGE in ratio order", syms, st)
	}
	if got[0].Adv10 != 1000 || got[0].PrevClose != 2 || got[0].Ratio != 5 || got[0].SplitFactor != 1 {
		t.Errorf("GOOD: %+v", got[0])
	}
	want := PrefilterStats{Date: "2026-10-05", Universe: 12, NoPrev: 1, NoAdv: 1, OutOfPrice: 2, LowVolume: 1, NoSpike: 1, Passed: 6, Kept: 6, Dropped: 0}
	if st != want {
		t.Errorf("stats %+v, want %+v", st, want)
	}

	// the cap
	o := DefaultPrefilter()
	o.Cap = 2
	got, st = Prefilter("2026-10-05", today, hist, o)
	if len(got) != 2 || st.Passed != 6 || st.Kept != 2 || st.Dropped != 4 || got[1].Sym != "RUNNER" {
		t.Errorf("cap 2: kept %d, stats %+v", len(got), st)
	}
}

// The impulse bound is exact at its edge, and never vacuous.
func TestPrefilterImpulseBoundEdgeAndMargin(t *testing.T) {
	hist := make([]map[string]core.DailyBar, 5)
	for i := range hist {
		hist[i] = map[string]core.DailyBar{"X": dbar(50, 50, 1000)}
	}
	at := func(hi float64, o PrefilterOpts) PrefilterStats {
		_, st := Prefilter("2026-10-05", map[string]core.DailyBar{"X": dfull(50, hi, 50, 50, 1000)}, hist, o)
		return st
	}
	o := DefaultPrefilter()
	o.SpikeMarginPct = 0
	if st := at(52.5, o); st.Passed != 1 { // exactly 5% over the lowest low
		t.Errorf("high == 1.05 x low with no margin: %+v", st)
	}
	if st := at(52.49, o); st.NoSpike != 1 {
		t.Errorf("high just under 1.05 x low with no margin: %+v", st)
	}
	// the default margin shaves 2% off the threshold: 52.5 * 0.98 = 51.45
	d := DefaultPrefilter()
	if st := at(51.46, d); st.Passed != 1 {
		t.Errorf("default margin: 51.46 should pass: %+v", st)
	}
	if st := at(51.4, d); st.NoSpike != 1 {
		t.Errorf("default margin: 51.40 should fail: %+v", st)
	}
	// a margin that swallows the 5% would make the test vacuous (the high is never under the low): the default must not
	if d.MinSpike*(1-d.SpikeMarginPct/100) <= 1 {
		t.Errorf("default spike threshold %.4f x low is not above 1: the test would pass every name", d.MinSpike*(1-d.SpikeMarginPct/100))
	}
	// yesterday's LOW counts too: a name that gapped up (yesterday's low 40, today 49..51) had room for the impulse
	hist2 := make([]map[string]core.DailyBar, 5)
	for i := range hist2 {
		hist2[i] = map[string]core.DailyBar{"X": dfull(41, 45, 40, 41, 1000)}
	}
	_, st := Prefilter("2026-10-05", map[string]core.DailyBar{"X": dfull(49, 51, 49, 50, 1000)}, hist2, d)
	if st.Passed != 1 {
		t.Errorf("gap up from a 40 low: %+v", st)
	}
}

// The price test is the trader's: the day's RANGE has to reach [min, max], not yesterday's close.
func TestPrefilterPriceRangeOverlap(t *testing.T) {
	hist := make([]map[string]core.DailyBar, 5)
	for i := range hist {
		hist[i] = map[string]core.DailyBar{"X": dbar(0.18, 0.18, 1000), "Y": dbar(60, 60, 1000)}
	}
	today := map[string]core.DailyBar{
		"X": dfull(0.19, 0.45, 0.18, 0.4, 5000), // yesterday $0.18, ran to $0.45
		"Y": dfull(55, 56, 40, 41, 5000),        // yesterday $60 (over $50), sold off into the range
	}
	got, st := Prefilter("2026-10-05", today, hist, DefaultPrefilter())
	if len(got) != 2 || st.OutOfPrice != 0 {
		t.Errorf("kept %d stats %+v, want both: the range overlaps [0.20, 50]", len(got), st)
	}
	// just under $0.20 all day (even with the 1% slack) and just over $50 all day
	today = map[string]core.DailyBar{
		"X": dfull(0.19, 0.1979, 0.18, 0.19, 5000), // 0.1979 * 1.01 = 0.1999 < 0.20
		"Y": dfull(51, 52, 50.6, 51, 5000),         // low 50.6 > 50 * 1.01
	}
	if got, st := Prefilter("2026-10-05", today, hist, DefaultPrefilter()); len(got) != 0 || st.OutOfPrice != 2 {
		t.Errorf("kept %d stats %+v, want both out of the price range", len(got), st)
	}
}

// gapDownBars is an offering-style tape: yesterday flat at 1.00, today a gap down
// to 0.70 on 50k shares a minute from 07:00, then an impulse at 07:40 (H 0.75,
// C 0.745), a two-bar flag, a break at 07:43 that the engine calls LONG (FLAG),
// and a dump at 07:44 that stops the position out.
func gapDownBars(t *testing.T) []core.Bar {
	t.Helper()
	prev := flatBars(t, "2026-10-02 15:00", 60, 1.00, 10_000)
	today := flatBars(t, "2026-10-05 07:00", 600, 0.70, 50_000)
	bars := append(prev, today...)
	setBar(t, bars, "2026-10-05 07:40", 0.70, 0.75, 0.70, 0.745) // impulse
	setBar(t, bars, "2026-10-05 07:41", 0.745, 0.748, 0.738, 0.742)
	setBar(t, bars, "2026-10-05 07:42", 0.742, 0.746, 0.739, 0.741)
	setBar(t, bars, "2026-10-05 07:43", 0.742, 0.76, 0.741, 0.755) // break
	setBar(t, bars, "2026-10-05 07:44", 0.75, 0.75, 0.70, 0.70)    // dump
	return bars
}

// The first screen kept a name only when the DAY'S HIGH reached 5% over
// yesterday's close. That reads the day's outcome: a gap-down name that calls a
// losing LONG (FLAG) never trades 5% over yesterday and was never fetched, while
// an identical setup that ran was kept - an upward bias in the expected value.
// The screen must keep every name that could have called the flag.
func TestPrefilterKeepsAGapDownThatCallsALosingFlag(t *testing.T) {
	bars := gapDownBars(t)
	c := Candidate{Sym: "GAPD", Date: "2026-10-05", Adv10: 100_000, PrevClose: 1.0, Bars: bars}
	pd := PrepareDay("2026-10-05", []Candidate{c}, emaflag.DefaultParams(), 80)
	dr := SimulateDay(pd, rulesNamed(t, "V1"))
	if len(dr.Trades) != 1 || !(dr.Trades[0].Net < 0) {
		t.Fatalf("premise: the admitted gap-down name must trade once and lose; trades %v skips %v (the scenario needs rebuilding if the engine changed)", symsOf(dr), dr.Skips)
	}

	hi, lo, vol := 0.0, 1e9, int64(0)
	for _, b := range bars {
		if ETDate(b.T) == "2026-10-05" {
			hi, lo, vol = max(hi, b.H), min(lo, b.L), vol+b.V
		}
	}
	hist := make([]map[string]core.DailyBar, 10)
	for i := range hist {
		hist[i] = map[string]core.DailyBar{"GAPD": {O: 1, H: 1, L: 1, C: 1, V: 100_000}}
	}
	today := map[string]core.DailyBar{"GAPD": {O: 0.70, H: hi, L: lo, C: 0.70, V: vol}}
	kept, st := Prefilter("2026-10-05", today, hist, DefaultPrefilter())
	if len(kept) != 1 || st.Passed != 1 || st.NoSpike != 0 || st.OutOfPrice != 0 {
		t.Errorf("grouped day H %.3f L %.3f V %d (prevClose 1.00): kept %d, stats %+v - a losing flag on an admitted name must not be screened out", hi, lo, vol, len(kept), st)
	}
}
