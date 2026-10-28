package backtest

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/emaflag"
)

func TestAdmitCrossing(t *testing.T) {
	d := "2026-10-05"
	bar := func(stamp string, v int64) core.Bar {
		ts := etMs(t, stamp)
		return core.Bar{T: ts, O: 1, H: 1, L: 1, C: 1, V: v}
	}
	bars := []core.Bar{
		bar("2026-10-02 15:00", 9_000_000), // the previous day: never counted
		bar("2026-10-02 19:59", 9_000_000),
		bar("2026-10-05 03:59", 9_000_000), // before 04:00: never counted
		bar("2026-10-05 04:00", 300),
		bar("2026-10-05 04:01", 400),
		bar("2026-10-05 04:02", 300), // cum 1000 == adv10: crossed (the threshold is inclusive)
		bar("2026-10-05 04:03", 500),
		bar("2026-10-05 09:29", 500),
		bar("2026-10-05 09:30", 9_000_000), // the open: not pre-market
	}
	ts, x, ok := Admit(bars, d, 1000)
	if !ok || ts != etMs(t, "2026-10-05 04:02") {
		t.Fatalf("admitted at %s ok=%v, want 04:02", ETStamp(ts), ok)
	}
	if want := 2000.0 / 1000; x != want {
		t.Errorf("final pmVolX = %v, want %v (pre-market only: 04:00..09:29)", x, want)
	}

	// one volume short at the 04:02 bar: the crossing moves to the next bar
	ts, _, ok = Admit(bars, d, 1001)
	if !ok || ts != etMs(t, "2026-10-05 04:03") {
		t.Errorf("adv10 1001: admitted at %s ok=%v, want 04:03", ETStamp(ts), ok)
	}

	// only the 09:30 print is big: the pre-market never reached 1x, so never a candidate
	if _, x, ok := Admit(bars, d, 5000); ok || x >= 1 {
		t.Errorf("adv10 5000: ok=%v x=%v, want never admitted (volume at/after 09:30 does not count)", ok, x)
	}
	if _, _, ok := Admit(bars, d, 0); ok {
		t.Errorf("adv10 0 must never admit")
	}
	// the first bar alone crosses
	if ts, _, ok := Admit(bars, d, 250); !ok || ts != etMs(t, "2026-10-05 04:00") {
		t.Errorf("adv10 250: admitted at %s ok=%v, want 04:00", ETStamp(ts), ok)
	}
}

// BEAT 2026-10-05 is a real LONG (FLAG) at 09:00 (trigger 0.6477374).
func TestPrimingNoEntryOnTheAdmissionMinute(t *testing.T) {
	bars := loadFixture(t, "beat-2026-10-05")
	p := emaflag.DefaultParams()
	mk := func(admit string) []Signal {
		c := &Candidate{Sym: "BEAT", Date: "2026-10-05", Adv10: 1e6, Bars: bars, AdmitTs: etMs(t, admit)}
		return Signals(c, p)
	}

	if got := mk("2026-10-05 08:30"); len(got) != 1 || ETClock(got[0].Ts) != "09:00" {
		t.Fatalf("admitted 08:30: signals = %+v, want the one entry at 09:00", got)
	}
	if got := mk("2026-10-05 08:59"); len(got) != 1 {
		t.Errorf("admitted 08:59: %d signals, want 1 (09:00 is the next minute, so it is acted on)", len(got))
	}
	if got := mk("2026-10-05 09:00"); len(got) != 0 {
		t.Errorf("admitted 09:00 (the signal minute): %d signals, want 0 - the trader primes on its first evaluation, so that entry is history", len(got))
	}
	if got := mk("2026-10-05 09:01"); len(got) != 0 {
		t.Errorf("admitted 09:01: %d signals, want 0", len(got))
	}

	// the signal's stop is the engine's initial stop at that moment
	s := mk("2026-10-05 08:30")[0]
	if !approx(s.Trigger, 0.6477374, 1e-9) {
		t.Errorf("trigger = %v, want 0.6477374", s.Trigger)
	}
	closes := make([]float64, len(bars))
	for i := range bars {
		closes[i] = bars[i].C
	}
	ema := emaSeries(closes, 9)
	if math.IsNaN(s.Stop) || !(s.Stop < s.Trigger) || s.Stop < ema[s.Idx-1]*0.99-1e-12 {
		t.Errorf("stop = %v: want a stop under the trigger %v and at least EMA(prev bar)*0.99 = %v", s.Stop, s.Trigger, ema[s.Idx-1]*0.99)
	}
	if !approx(s.Stop, 0.599445, 1e-6) {
		t.Errorf("stop = %v, want 0.599445 (max(flag low, EMA of the last closed bar) * 0.99)", s.Stop)
	}
}

// Signals from the previous day's bars are not today's.
func TestSignalsOnlyOfTheDay(t *testing.T) {
	bars := loadFixture(t, "beat-2026-10-05")
	c := &Candidate{Sym: "BEAT", Date: "2026-10-02", Adv10: 1e6, Bars: bars} // pretend Friday is the day
	for _, s := range Signals(c, emaflag.DefaultParams()) {
		if ETDate(s.Ts) != "2026-10-02" {
			t.Errorf("signal on %s for day 2026-10-02", ETDate(s.Ts))
		}
	}
}

// emaSeries must be the engine's EMA to the last bit: the trail is hung from it.
func TestEMAMatchesEngine(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(fixturesDir, "*.json"))
	if len(files) == 0 {
		t.Fatal("no fixtures")
	}
	checked := 0
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		if _, err := os.Stat(f); err != nil {
			continue
		}
		bars := loadFixture(t, name)
		if len(bars) < 12 {
			continue
		}
		res := emaflag.Evaluate(bars, minuteMs, bars[len(bars)-1].T+minuteMs, emaflag.DefaultParams())
		if math.IsNaN(res.EmaClosed) {
			continue
		}
		closes := make([]float64, len(bars))
		for i := range bars {
			closes[i] = bars[i].C
		}
		got := emaSeries(closes, 9)[len(bars)-1]
		if got != res.EmaClosed {
			t.Errorf("%s: EMA %v, engine %v", name, got, res.EmaClosed)
		}
		checked++
	}
	if checked < 5 {
		t.Errorf("only %d fixtures compared", checked)
	}
}

// PrepareDay: the cap, the ranking and the funnel counts.
func TestPrepareDayCapAndRank(t *testing.T) {
	bars := loadFixture(t, "beat-2026-10-05")
	mk := func(sym string, adv float64) Candidate {
		return Candidate{Sym: sym, Date: "2026-10-05", Adv10: adv, Bars: bars}
	}
	// BEAT's pre-market volume is ~80M: adv10 5M -> 16x, 20M -> ~4x, 1e12 -> never admitted
	cands := []Candidate{mk("AAA", 5_000_000), mk("BBB", 20_000_000), mk("CCC", 1e12)}
	pd := PrepareDay("2026-10-05", cands, emaflag.DefaultParams(), 0)
	if pd.Fetched != 3 || pd.Admitted != 2 || pd.CapDropped != 0 || len(pd.Kept) != 2 {
		t.Fatalf("funnel = fetched %d admitted %d capDropped %d kept %d, want 3/2/0/2", pd.Fetched, pd.Admitted, pd.CapDropped, len(pd.Kept))
	}
	// the stronger multiple ranks first
	for _, s := range pd.Signals {
		if s.Cand.Sym == "AAA" && s.Rank != 1 || s.Cand.Sym == "BBB" && s.Rank != 2 {
			t.Errorf("%s has rank %d", s.Cand.Sym, s.Rank)
		}
	}
	// cap of 1: only the strongest is kept, the other is counted as dropped
	pd = PrepareDay("2026-10-05", cands, emaflag.DefaultParams(), 1)
	if pd.Admitted != 2 || pd.CapDropped != 1 || len(pd.Kept) != 1 || pd.Kept[0].Sym != "AAA" {
		t.Errorf("cap 1: admitted %d capDropped %d kept %v", pd.Admitted, pd.CapDropped, pd.Kept)
	}
}
