package backtest

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func g3Rules(t testing.TB, id string) Grid2Rules {
	t.Helper()
	for _, r := range Grid3() {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no grid v3 variant %q", id)
	return Grid2Rules{}
}

func TestGrid3Registration(t *testing.T) {
	var ids []string
	for _, r := range Grid3() {
		ids = append(ids, r.ID)
	}
	if got := strings.Join(ids, " "); got != "V0 V1 V2 V3 V4 V5 V6 V7" || Grid3Version != "grid v3" {
		t.Fatalf("grid v3 drifted: %s %s", got, Grid3Version)
	}
	for id, w := range map[string]struct {
		sig, stop         string
		first, tr5, avoid bool
		dist              float64
		derive            string
	}{
		"V1": {VwIntrabar, G2StopCrossLow, true, true, false, 0, ""},
		"V2": {VwIntrabar, G2StopATR, true, true, false, 0, ""},
		"V3": {VwIntrabar, G2StopDayLow, true, true, false, 0, ""},
		"V4": {VwIntrabar, G2StopATR, true, true, true, 0, ""},
		"V5": {VwClose, G2StopATR, true, false, false, 0.5, ""},
		"V6": {VwIntrabar, G2StopATR, true, true, true, 0, "V4"},
		"V7": {VwIntrabar, G2StopATR, true, false, false, 0, ""},
		"V0": {VwIntrabar, G2StopATR, false, false, false, 0, ""},
	} {
		r := g3Rules(t, id)
		if r.Signal != w.sig || r.Stop != w.stop || r.FirstOnly != w.first || r.Trend5Yes != w.tr5 || r.Avoid != w.avoid || r.MinDist != w.dist || r.Derive != w.derive || r.HoldMin != 0 || r.UseDayChg {
			t.Errorf("%s %+v", id, r)
		}
	}
	if l := g3Rules(t, "V6").Lim; l.MaxConcurrent != 5 || l.DayLossR != 3 {
		t.Errorf("V6 limits %+v", l)
	}
}

func TestGrid3GatesFirstTrend5AvoidDist(t *testing.T) {
	nan := math.NaN()
	ok := DriftSignal{CrossN: 1, Trend5: "yes", DayChgPct: 1, SpyDayPct: 0, DistPct: 0.8}
	v1, v4, v5, v7, v0 := g3Rules(t, "V1"), g3Rules(t, "V4"), g3Rules(t, "V5"), g3Rules(t, "V7"), g3Rules(t, "V0")
	if !Grid2Gate(v1, ok) || !Grid2Gate(v4, ok) || !Grid2Gate(v5, ok) || !Grid2Gate(v7, ok) || !Grid2Gate(v0, ok) {
		t.Fatal("a qualifying signal must pass every variant")
	}
	second := ok
	second.CrossN = 2
	if Grid2Gate(v1, second) || Grid2Gate(v7, second) || Grid2Gate(v5, second) || !Grid2Gate(v0, second) {
		t.Error("only crossNo 1 is first; V0 takes every signal")
	}
	no5 := ok
	no5.Trend5 = "no"
	if Grid2Gate(v1, no5) || Grid2Gate(v4, no5) || !Grid2Gate(v7, no5) || !Grid2Gate(v5, no5) {
		t.Error("trend5 gates V1-V4/V6 only")
	}
	// AVOID: dayChg < -2 AND spyDay < -0.5 (strict, both)
	bad := ok
	bad.DayChgPct, bad.SpyDayPct = -2.01, -0.51
	if Grid2Gate(v4, bad) || !Grid2Gate(v1, bad) {
		t.Error("AVOID skips only V4/V6")
	}
	for name, s := range map[string]DriftSignal{
		"dayChg -2 exactly":   {DayChgPct: -2, SpyDayPct: -1},
		"spyDay -0.5 exactly": {DayChgPct: -3, SpyDayPct: -0.5},
		"stock only":          {DayChgPct: -3, SpyDayPct: 0.2},
		"spy only":            {DayChgPct: 0.5, SpyDayPct: -1},
		"spy unknown":         {DayChgPct: -3, SpyDayPct: nan},
		"stock unknown":       {DayChgPct: nan, SpyDayPct: -1},
	} {
		s.CrossN, s.Trend5 = 1, "yes"
		if !Grid2Gate(v4, s) {
			t.Errorf("%s must NOT be avoided", name)
		}
	}
	// V5: distVwap strictly above 0.5, NaN fails
	for d, want := range map[float64]bool{0.51: true, 0.5: false, 0.2: false, -1: false, nan: false} {
		s := ok
		s.DistPct = d
		if Grid2Gate(v5, s) != want {
			t.Errorf("V5 distVwap %v: want %v", d, want)
		}
	}
}

// g3EarlyFail: a low-volume first poke (fails the universe but is a raw cross),
// a dip below, then a qualifying poke.
func g3EarlyFail(t testing.TB, pc float64) []core.Bar {
	poke := vs{100, 101, 100, 100, bigV}
	return g2Day(t, pc, "10:20", append(append([]vs{{100, 101, 100, 100, 1}}, repeatVs(flat(99.5, bigV), 5)...),
		append([]vs{poke}, repeatVs(flat(100.9, bigV), 20)...)...)...)
}

func TestGrid3FirstCrossSemantics(t *testing.T) {
	p := DefaultVwapParams()
	// first raw cross passes the universe: crossNo 1
	good := g3Scen(t, 105, flat(100.5, bigV), flat(100.6, bigV))
	var sigs []DriftSignal
	for _, s := range DriftDay("T", vwDate, 10e6, good, p) {
		if s.Type == VwIntrabar {
			sigs = append(sigs, s)
		}
	}
	if len(sigs) == 0 || sigs[0].CrossN != 1 {
		t.Fatalf("expected a first-cross signal: %+v", sigs)
	}
	r := g3Rules(t, "V7")

	if tr, _ := g2Run(t, good, r); len(tr) != 1 {
		t.Errorf("a day's first universe-passing cross must trade under FirstOnly: %d", len(tr))
	}
	// an earlier universe-FAILED raw cross still counts: the later passing cross is crossNo 2
	bars := g3EarlyFail(t, 100)
	var later []DriftSignal
	for _, s := range DriftDay("T", vwDate, 10e6, bars, p) {
		if s.Type == VwIntrabar {
			later = append(later, s)
		}
	}
	if len(later) == 0 {
		t.Fatal("fixture produced no passing signal")
	}
	for _, s := range later {
		if s.CrossN < 2 {
			t.Errorf("universe-failed earlier cross must count: crossNo %d", s.CrossN)
		}
	}
	if tr, _ := g2Run(t, bars, r); len(tr) != 0 {
		t.Errorf("FirstOnly must skip a day whose first raw cross failed the universe: %d trades", len(tr))
	}
	all := r
	all.FirstOnly = false
	if tr, _ := g2Run(t, bars, all); len(tr) == 0 {
		t.Error("without FirstOnly the later cross trades")
	}
}

func TestGrid3AvoidWithBenchmark(t *testing.T) {
	spy := func(close float64) *Bench {
		b := NewBench("SPY")
		prev := dayBars(t, "2026-10-02", "09:30", "15:59", func(int) core.Bar { return core.Bar{O: 100, H: 100, L: 100, C: 100, V: 1000} })
		day := dayBars(t, vwDate, "09:30", "15:59", func(int) core.Bar { return core.Bar{O: close, H: close, L: close, C: close, V: 1000} })
		b.Add(vwDate, append(prev, day...))
		return b
	}
	r := g3Rules(t, "V4")
	r.Trend5Yes = false // trend5 is unit-tested in the gate test; this isolates AVOID
	run := func(pc float64, b *Bench) int {
		bars := g3Scen(t, pc, flat(100.5, bigV), flat(100.6, bigV))
		tr, _ := SimulateGrid2DayRegime("T", vwDate, 10e6, bars, []Grid2Rules{r}, DefaultVwapParams(), b)
		return len(tr[0])
	}
	// stock close 101 vs pc 105 = -3.8%; SPY 99 vs 100 = -1%: avoided
	if n := run(105, spy(99)); n != 0 {
		t.Errorf("weak stock on a weak market must be avoided: %d", n)
	}
	if n := run(105, spy(100)); n != 1 { // SPY flat: only the stock is down
		t.Errorf("weak stock on a flat market is not avoided: %d", n)
	}
	if n := run(100, spy(99)); n != 1 { // stock up 1%, SPY down
		t.Errorf("a firm stock on a weak market is not avoided: %d", n)
	}
	if n := run(105, nil); n != 1 {
		t.Errorf("no benchmark: nothing to judge, not avoided: %d", n)
	}
}

func TestGrid3V0ReproducesGrid2H3(t *testing.T) {
	fixtures := map[string][]core.Bar{
		"win":   g2Scen(t, 105, flat(100.5, bigV), vs{100.9, 101.5, 100.8, 101.4, bigV}, flat(101.2, bigV)),
		"stop":  g2Scen(t, 100, vs{100.5, 100.6, 90, 95, bigV}, flat(95, bigV)),
		"early": g3EarlyFail(t, 100),
		"nosig": g2Day(t, 100, "10:20", repeatVs(flat(100, bigV), 30)...),
	}
	for name, bars := range fixtures {
		want, wc := g2Run(t, bars, g2Rules(t, "H3"))
		got, gc := g2Run(t, bars, g3Rules(t, "V0"))
		if len(got) != len(want) {
			t.Fatalf("%s: %d vs %d trades", name, len(got), len(want))
		}
		for i := range got {
			g, w := got[i], want[i]
			g.Variant, w.Variant = "", ""
			if g != w {
				t.Errorf("%s trade %d:\n%+v\n%+v", name, i, g, w)
			}
		}
		if !reflect.DeepEqual(gc, wc) {
			t.Errorf("%s counts %+v %+v", name, gc, wc)
		}
	}
}

func TestGrid3V5UsesCloseSignalAndDistGate(t *testing.T) {
	// the cross bar closes at 101 against a VWAP near 100: distVwap about +1%
	bars := g3Scen(t, 100, flat(101, bigV), flat(101, bigV))
	r := g3Rules(t, "V5")
	tr, _ := g2Run(t, bars, r)
	if len(tr) != 1 || tr[0].CrossTs != etMs(t, vwDate+" 10:30") {
		t.Fatalf("V5 must trade the close-signal cross: %+v", tr)
	}
	strict := r
	strict.MinDist = 5 // above the real distance
	if tr, _ := g2Run(t, bars, strict); len(tr) != 0 {
		t.Errorf("a distVwap below the gate must not trade: %d", len(tr))
	}
}

func TestGrid3PortfolioMax5AndDailyLoss(t *testing.T) {
	d := "2026-10-05"
	var in []VwTrade
	for i, s := range []string{"A", "B", "C", "D", "E", "F", "G"} {
		in = append(in, pt(t, d, s, "10:0"+string(rune('0'+i)), "10:50", 1))
	}
	kept, sk := ApplyPortfolio(in, Grid3Limits())
	if syms(kept) != "A,B,C,D,E" || sk.MaxPos != 2 || sk.DayLoss != 0 {
		t.Errorf("kept %s skips %+v", syms(kept), sk)
	}
	loss := []VwTrade{pt(t, d, "A", "10:00", "10:10", -3), pt(t, d, "B", "10:11", "10:20", 1)}
	if kept, sk = ApplyPortfolio(loss, Grid3Limits()); len(kept) != 1 || sk.DayLoss != 1 {
		t.Errorf("-3R: %s %+v", syms(kept), sk)
	}
}

func g3Variant(id string, avg float64) Grid2Variant {
	v := Grid2Variant{ID: id, Signal: VwIntrabar}
	v.Stats.AvgRNet, v.Stats.Trades = avg, 10
	return v
}

func TestGrid3WorseMonthRowAndFiles(t *testing.T) {
	mk := func(from string, a, b float64) Grid2Report {
		return Grid2Report{Version: Grid3Version, Meta: VwMeta{From: from}, BenchSym: "SPY", Variants: []Grid2Variant{g3Variant("V1", a), g3Variant("V2", b)}}
	}
	sep, oct := mk("2026-09-01", 0.10, -0.20), mk("2026-10-01", -0.05, 0.30)
	m := Grid3Min([]Grid2Report{sep, oct})
	if m["V1"] != 1 || m["V2"] != 0 {
		t.Fatalf("worse month: %v", m)
	}
	md := Grid3CombinedMarkdown([]Grid2Report{sep, oct})
	if !strings.Contains(md, "min (2026-10)") || !strings.Contains(md, "min (2026-09)") {
		t.Errorf("min rows missing:\n%s", md)
	}
	dir := t.TempDir()
	var dirs []string
	for i, r := range []Grid2Report{sep, oct} {
		d := filepath.Join(dir, string(rune('a'+i)))
		if _, err := WriteGrid3Files(d, r); err != nil {
			t.Fatal(err)
		}
		for _, n := range []string{"grid3.md", "grid3.csv", "grid3.json"} {
			if _, err := os.Stat(filepath.Join(d, n)); err != nil {
				t.Error(err)
			}
		}
		dirs = append(dirs, d)
	}
	p, err := WriteGrid3Both(dir, dirs)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), "min (2026-10)") {
		t.Error("combined file lacks the min row")
	}
}

// g3Scen: flat 100 bars, the cross bar at 10:30 as the day's FIRST raw cross
// (g2Scen's 10:20 dip bar is itself a universe-failed raw cross: its cross is 2nd), then specs.
func g3Scen(t testing.TB, pc float64, specs ...vs) []core.Bar {
	t.Helper()
	return g2Day(t, pc, "10:30", append([]vs{crossBar()}, specs...)...)
}
