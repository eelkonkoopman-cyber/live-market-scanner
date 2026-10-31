package backtest

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

const vwDate = "2026-10-05"

type vs struct { // one hand-made bar
	o, h, l, c float64
	v          int64
}

func flat(px float64, v int64) vs { return vs{px, px, px, px, v} }

// vwScen builds a day: flat bars at base (volume baseVol) from 08:50 through the
// minute before `start`, then the specs one per minute from `start` ("15:04").
// The flat run gives EMA = base and VWAP = base (d = 0, "at or under").
func vwScen(t testing.TB, base float64, baseVol int64, start string, specs ...vs) []core.Bar {
	t.Helper()
	t0 := etMs(t, vwDate+" 08:50")
	ts := etMs(t, vwDate+" "+start)
	var bars []core.Bar
	for x := t0; x < ts; x += minuteMs {
		bars = append(bars, core.Bar{T: x, O: base, H: base, L: base, C: base, V: baseVol})
	}
	for i, s := range specs {
		bars = append(bars, core.Bar{T: ts + int64(i)*minuteMs, O: s.o, H: s.h, L: s.l, C: s.c, V: s.v})
	}
	return bars
}

func vwRun(t testing.TB, bars []core.Bar, variant string, adv float64) ([]VwTrade, VwCounts) {
	t.Helper()
	return SimulateVwapCrossDay("TEST", vwDate, adv, bars, variant, DefaultVwapParams())
}

const bigV = 100_000

// crossSpecs: the cross bar (a close above the VWAP), then the entry bar and a
// stop-out bar.
func crossBar() vs { return vs{100, 101, 100, 101, bigV} }

func TestVwapSeriesMatchesReference(t *testing.T) {
	var bars []core.Bar
	t0 := etMs(t, "2026-10-02 15:50") // previous day's tail
	for i := 0; i < 10; i++ {
		p := 50 + float64(i%4)
		bars = append(bars, core.Bar{T: t0 + int64(i)*minuteMs, O: p, H: p + 1, L: p - 1, C: p + 0.5, V: 1000})
	}
	t1 := etMs(t, vwDate+" 09:25")
	for i := 0; i < 20; i++ {
		p := 60 + 0.37*float64(i%7)
		bars = append(bars, core.Bar{T: t1 + int64(i)*minuteMs, O: p, H: p + 0.4, L: p - 0.3, C: p + 0.1*float64(i%3), V: int64(1000 + 100*i)})
	}
	s := VwapSeries(bars, vwDate, 9)
	// reference EMA: SMA seed then k=0.2 over every close, prev day included
	ema := 0.0
	var pv, vv float64
	for i, b := range bars {
		switch {
		case i < 8:
			ema += b.C
		case i == 8:
			ema = (ema + b.C) / 9
		default:
			ema = b.C*0.2 + ema*0.8
		}
		if i >= 8 && math.Abs(s.Ema[i]-ema) > 1e-9 {
			t.Fatalf("bar %d ema %v want %v", i, s.Ema[i], ema)
		}
		if i < 8 && !math.IsNaN(s.Ema[i]) {
			t.Fatalf("bar %d ema should be NaN", i)
		}
		if ETDate(b.T) != vwDate || ETMinute(b.T) < 9*60+30 {
			if !math.IsNaN(s.Vwap[i]) {
				t.Fatalf("bar %d vwap before 09:30 should be NaN, got %v", i, s.Vwap[i])
			}
			continue
		}
		pv += (b.H + b.L + b.C) / 3 * float64(b.V)
		vv += float64(b.V)
		if math.Abs(s.Vwap[i]-pv/vv) > 1e-9 {
			t.Fatalf("bar %d vwap %v want %v", i, s.Vwap[i], pv/vv)
		}
	}
}

func TestVwapHandValues(t *testing.T) {
	bars := vwScen(t, 100, bigV, "09:41", crossBar())
	s := VwapSeries(bars, vwDate, 9)
	i := len(bars) - 1
	if !approx(s.Ema[i], 100.2, 1e-9) {
		t.Errorf("ema %v", s.Ema[i])
	}
	want := (11*100*float64(bigV) + (101+100+101)/3.0*float64(bigV)) / (12 * float64(bigV))
	if !approx(s.Vwap[i], want, 1e-9) {
		t.Errorf("vwap %v want %v", s.Vwap[i], want)
	}
}

func TestCurveFrac(t *testing.T) {
	for _, c := range []struct{ min, want float64 }{
		{200, 0}, {240, 0}, {570, 0.03}, {585, 0.09}, {600, 0.15}, {660, 0.30}, {720, 0.45}, {960, 1}, {1000, 1},
	} {
		if g := CurveFrac(c.min); !approx(g, c.want, 1e-9) {
			t.Errorf("CurveFrac(%v) = %v want %v", c.min, g, c.want)
		}
	}
	prev := -1.0
	for m := 200.0; m <= 1000; m += 5 {
		if g := CurveFrac(m); g < prev {
			t.Fatalf("curve decreases at %v", m)
		}
		prev = CurveFrac(m)
	}
}

func TestCloseCrossFiresAtRightBar(t *testing.T) {
	bars := vwScen(t, 100, bigV, "09:41",
		crossBar(),
		vs{101.2, 101.4, 100.9, 101.3, bigV}, // entry bar
		vs{101.3, 101.3, 90, 91, bigV},       // stop out
	)
	tr, cnt := vwRun(t, bars, VwClose, 10e6)
	if len(tr) != 1 || cnt.Fired != 1 {
		t.Fatalf("trades %d fired %d", len(tr), cnt.Fired)
	}
	x := tr[0]
	if x.CrossMin != 9*60+41 || x.EntryMin != 9*60+42 {
		t.Errorf("cross %d entry %d", x.CrossMin, x.EntryMin)
	}
	if x.Entry != 101.2 || x.Stop0 != 100 || !approx(x.Risk, 1.2, 1e-9) {
		t.Errorf("entry %v stop %v risk %v", x.Entry, x.Stop0, x.Risk)
	}
	if x.CrossD <= 0 {
		t.Errorf("cross d %v", x.CrossD)
	}
}

func TestIntrabarFiresOnHighOnlyPoke(t *testing.T) {
	poke := vs{100, 101, 100, 100, bigV} // closes back at 100: the close variant sees no cross
	specs := []vs{poke, {100.2, 100.3, 99, 99.5, bigV}, flat(99, bigV)}
	bars := vwScen(t, 100, bigV, "09:41", specs...)
	if tr, cnt := vwRun(t, bars, VwClose, 10e6); len(tr) != 0 || cnt.Fired != 0 {
		t.Fatalf("close variant fired on a poke: %d %d", len(tr), cnt.Fired)
	}
	tr, cnt := vwRun(t, bars, VwIntrabar, 10e6)
	if len(tr) != 1 || cnt.Fired != 1 || tr[0].CrossMin != 9*60+41 {
		t.Fatalf("intrabar: %+v %+v", tr, cnt)
	}
	if tr[0].Entry != 100.2 {
		t.Errorf("entry %v", tr[0].Entry)
	}
}

func TestRearmOnlyAfterLaterBarClosesBelow(t *testing.T) {
	poke := vs{100, 101, 100, 100, bigV}
	// A (09:41) fires; B (09:42) pokes too but the bull is not re-armed (A is not
	// "a later bar"); B closes with d < 0, which re-arms; C (09:43) fires again.
	bars := vwScen(t, 100, bigV, "09:41",
		poke,
		vs{100.2, 101, 99, 100, bigV},
		poke,
		vs{100.1, 100.1, 99, 99.5, bigV},
		flat(99, bigV),
	)
	tr, cnt := vwRun(t, bars, VwIntrabar, 10e6)
	if len(tr) != 2 || cnt.Fired != 2 {
		t.Fatalf("trades %d fired %d (%+v)", len(tr), cnt.Fired, cnt)
	}
	if tr[0].CrossMin != 9*60+41 || tr[1].CrossMin != 9*60+43 {
		t.Errorf("cross minutes %d %d", tr[0].CrossMin, tr[1].CrossMin)
	}
}

func TestUniverseRejects(t *testing.T) {
	p := DefaultVwapParams()
	cases := []struct {
		name                      string
		px, adv, dvol, sess, rvol float64
		want                      string
	}{
		{"ok", 30, 2e6, 6e5, 6e5, 2, ""},
		{"price", 25, 2e6, 6e5, 6e5, 2, "price"},
		{"adv", 30, 999_999, 6e5, 6e5, 2, "adv10"},
		{"bar$", 30, 2e6, 4e5, 6e5, 2, "barDollarVol"},
		{"sess", 30, 2e6, 6e5, 4e5, 2, "sessionVol"},
		{"rvol", 30, 2e6, 6e5, 6e5, 1.49, "rvol"},
	}
	for _, c := range cases {
		if g := VwUniverseReject(p, c.px, c.adv, c.dvol, c.sess, c.rvol); g != c.want {
			t.Errorf("%s: %q want %q", c.name, g, c.want)
		}
	}
	// and through the simulation, each filter rejects the cross it should
	run := func(bars []core.Bar, adv float64) VwCounts {
		tr, cnt := vwRun(t, bars, VwClose, adv)
		if len(tr) != 0 || cnt.Fired != 0 {
			t.Fatalf("expected no signal, got %d/%d", len(tr), cnt.Fired)
		}
		return cnt
	}
	specs := []vs{crossBar(), flat(101, bigV)}
	if c := run(vwScen(t, 20, bigV, "09:41", vs{20, 21, 20, 21, bigV}, flat(21, bigV)), 10e6); c.UniverseReject["price"] != 1 {
		t.Errorf("price: %+v", c)
	}
	if c := run(vwScen(t, 100, bigV, "09:41", specs...), 900_000); c.UniverseReject["adv10"] != 1 {
		t.Errorf("adv10: %+v", c)
	}
	if c := run(vwScen(t, 100, bigV, "09:41", vs{100, 101, 100, 101, 1000}, flat(101, bigV)), 10e6); c.UniverseReject["barDollarVol"] != 1 {
		t.Errorf("bar$: %+v", c)
	}
	if c := run(vwScen(t, 100, 2000, "09:41", vs{100, 101, 100, 101, 5000}, flat(101, bigV)), 10e6); c.UniverseReject["sessionVol"] != 1 {
		t.Errorf("sess: %+v", c)
	}
	if c := run(vwScen(t, 100, bigV, "09:41", specs...), 1e9); c.UniverseReject["rvol"] != 1 {
		t.Errorf("rvol: %+v", c)
	}
}

func TestEntryIsNextOpenAndGapBelowStopSkipped(t *testing.T) {
	bars := vwScen(t, 100, bigV, "09:41", crossBar(), vs{99.9, 100.5, 99.8, 100, bigV}, flat(100, bigV))
	tr, cnt := vwRun(t, bars, VwClose, 10e6)
	if len(tr) != 0 || cnt.SkippedGap != 1 || cnt.Fired != 1 {
		t.Fatalf("gap: trades %d counts %+v", len(tr), cnt)
	}
}

func TestStopHitOnEntryBar(t *testing.T) {
	bars := vwScen(t, 100, bigV, "09:41", crossBar(), vs{100.5, 100.6, 99.5, 100, bigV}, flat(100, bigV))
	tr, _ := vwRun(t, bars, VwClose, 10e6)
	if len(tr) != 1 {
		t.Fatalf("trades %d", len(tr))
	}
	x := tr[0]
	if x.Exit != 100 || x.Reason != "stop" || x.BarsHeld != 1 || !approx(x.R, -1, 1e-9) {
		t.Errorf("%+v", x)
	}
	// net: risk 0.5 -> 100 shares; gross -50; slippage 2; commission 2 x $1 min
	if x.Shares != 100 || !approx(x.GrossUSD, -50, 1e-9) || !approx(x.CostUSD, 4, 1e-9) || !approx(x.NetUSD, -54, 1e-9) || !approx(x.RNet, -1.08, 1e-9) {
		t.Errorf("net arithmetic: %+v", x)
	}
}

func TestTrailingStopRaisesAndFillsAtStop(t *testing.T) {
	bars := vwScen(t, 100, bigV, "09:41", crossBar(),
		vs{100.5, 101, 100.6, 100.9, bigV}, // entry bar 100.5, low 100.6 -> stop 100.6
		vs{100.9, 101.5, 100.8, 101.4, bigV},
		vs{101.4, 101.4, 100.7, 100.8, bigV}, // low 100.7 <= stop 100.8: out at 100.8
		flat(100, bigV))
	tr, _ := vwRun(t, bars, VwClose, 10e6)
	if len(tr) != 1 {
		t.Fatalf("trades %d", len(tr))
	}
	x := tr[0]
	if !approx(x.Exit, 100.8, 1e-9) || x.Reason != "stop" || x.BarsHeld != 3 || !approx(x.R, 0.3/0.5, 1e-9) {
		t.Errorf("%+v", x)
	}
}

func TestGapThroughTrailedStopFillsAtOpen(t *testing.T) {
	bars := vwScen(t, 100, bigV, "09:41", crossBar(),
		vs{100.5, 101, 100.6, 100.9, bigV},
		vs{100.9, 101.5, 100.8, 101.4, bigV},
		vs{100.3, 100.4, 100.2, 100.3, bigV}, // opens under the 100.8 stop
		flat(100, bigV))
	tr, _ := vwRun(t, bars, VwClose, 10e6)
	if len(tr) != 1 || !approx(tr[0].Exit, 100.3, 1e-9) || tr[0].Reason != "stop" {
		t.Fatalf("%+v", tr)
	}
}

func TestEndOfDayExit(t *testing.T) {
	specs := []vs{crossBar()}
	// rising lows to the close: never stopped
	n := 0
	for m := 9*60 + 42; m <= 15*60+59; m++ {
		p := 101 + 0.01*float64(n)
		specs = append(specs, vs{p, p + 0.05, p, p + 0.02, bigV})
		n++
	}
	bars := vwScen(t, 100, bigV, "09:41", specs...)
	tr, _ := vwRun(t, bars, VwClose, 10e6)
	if len(tr) != 1 {
		t.Fatalf("trades %d", len(tr))
	}
	x := tr[0]
	last := bars[len(bars)-1]
	if x.Reason != "eod" || x.Exit != last.C || ETClock(x.ExitTs) != "15:59" || x.BarsHeld != n {
		t.Errorf("%+v (n=%d)", x, n)
	}
}

func TestNoEntryBarAtOrAfter1559(t *testing.T) {
	// the cross lands on 15:58: its next bar starts at 15:59, which is not entered
	bars := vwScen(t, 100, bigV, "15:58", crossBar(), flat(101, bigV))
	tr, cnt := vwRun(t, bars, VwClose, 10e6)
	if len(tr) != 0 || cnt.NoEntryBar != 1 || cnt.Fired != 1 {
		t.Fatalf("%d %+v", len(tr), cnt)
	}
	// a cross on the very last bar has no next bar at all
	bars = vwScen(t, 100, bigV, "15:59", crossBar())
	if tr, cnt = vwRun(t, bars, VwClose, 10e6); len(tr) != 0 || cnt.NoEntryBar != 1 {
		t.Fatalf("%d %+v", len(tr), cnt)
	}
}

func TestSizingSkips(t *testing.T) {
	// risk 60 per share -> floor(50/60) = 0 shares
	bars := vwScen(t, 100, bigV, "09:41", vs{100, 101, 40, 101, bigV}, vs{101, 102, 100, 101, bigV}, flat(101, bigV))
	tr, cnt := vwRun(t, bars, VwClose, 10e6)
	if cnt.SkippedSize != 1 || cnt.Traded != 0 || len(tr) != 1 || tr[0].Sized {
		t.Fatalf("%+v %+v", cnt, tr)
	}
	// risk 0.1 on a $200 name -> 500 shares = $100k > $25k
	bars = vwScen(t, 200, bigV, "09:41", vs{200, 201, 200, 201, bigV}, vs{200.1, 201, 199, 200, bigV}, flat(200, bigV))
	tr, cnt = vwRun(t, bars, VwClose, 10e6)
	if cnt.SkippedSize != 1 || cnt.Traded != 0 {
		t.Fatalf("%+v %+v", cnt, tr)
	}
}

func TestOnePositionPerSymbol(t *testing.T) {
	poke := vs{100, 101, 100, 100, bigV}
	// A fires. The entry bar carries big volume at a high typical price (VWAP up,
	// d < 0 at its close = re-arm) but a low that only raises the stop. Later bars
	// hold the position with rising lows while a second poke arrives.
	specs := []vs{
		poke,
		vs{100.5, 104.5, 100.5, 100.5, 400_000},
		vs{100.6, 100.7, 100.6, 100.6, bigV},
		vs{100.7, 109, 100.7, 100.7, bigV}, // second poke while the position is open
		vs{100.8, 100.8, 100.8, 100.8, bigV},
		vs{90, 90, 90, 90, bigV},
	}
	bars := vwScen(t, 100, bigV, "09:41", specs...)
	tr, cnt := vwRun(t, bars, VwIntrabar, 10e6)
	if len(tr) != 1 || cnt.Fired != 2 || cnt.IgnoredOpen != 1 {
		t.Fatalf("trades %d counts %+v", len(tr), cnt)
	}
}

func TestBootstrapDeterministic(t *testing.T) {
	xs := []float64{-1, -1, 2, 0.5, -0.3, 4, -1, 0.1}
	a1, b1 := BootstrapMeanCI(xs, 10000, 7)
	a2, b2 := BootstrapMeanCI(xs, 10000, 7)
	if a1 != a2 || b1 != b2 {
		t.Fatal("same seed, different interval")
	}
	if a3, _ := BootstrapMeanCI(xs, 10000, 8); a3 == a1 {
		t.Error("different seed gave the same lower bound")
	}
	trades := []VwTrade{{Sized: true, R: 1, RNet: 0.9, NetUSD: 45, GrossUSD: 50, Reason: "stop", BarsHeld: 3}, {Sized: true, R: -1, RNet: -1.1, NetUSD: -55, GrossUSD: -50, Reason: "eod", BarsHeld: 9}}
	s1 := SummarizeVwap(trades, StatOpts{Resamples: 500, Seed: 3})
	s2 := SummarizeVwap(trades, StatOpts{Resamples: 500, Seed: 3})
	if s1 != s2 || s1.Trades != 2 || s1.Wins != 1 || !approx(s1.AvgRNet, -0.1, 1e-9) || !approx(s1.PctStop, 50, 1e-9) || !approx(s1.MedBars, 6, 1e-9) {
		t.Errorf("%+v", s1)
	}
}

func TestVwapPrefilter(t *testing.T) {
	mk := func(h float64, v int64) core.DailyBar { return core.DailyBar{H: h, L: h - 1, C: h, V: v} }
	hist := make([]map[string]core.DailyBar, 6)
	for i := range hist {
		hist[i] = map[string]core.DailyBar{"AAA": mk(30, 2_000_000), "LOWP": mk(10, 2_000_000), "THIN": mk(30, 500_000), "QUIET": mk(30, 2_000_000)}
	}
	today := map[string]core.DailyBar{"AAA": mk(31, 2_000_000), "LOWP": mk(20, 3_000_000), "THIN": mk(31, 5_000_000), "QUIET": mk(31, 1_000_000), "NEW": mk(31, 9_000_000)}
	got, st := VwapPrefilter(vwDate, today, hist, DefaultVwPrefilter())
	if len(got) != 1 || got[0].Sym != "AAA" || st.NoAdv != 1 || st.LowPrice != 1 || st.LowAdv != 1 || st.LowVolume != 1 || st.Kept != 1 {
		t.Fatalf("%+v %+v", got, st)
	}
}

func TestVwapReportFiles(t *testing.T) {
	bars := vwScen(t, 100, bigV, "09:41", crossBar(), vs{100.5, 100.6, 99.5, 100, bigV}, flat(100, bigV))
	tr, cnt := vwRun(t, bars, VwClose, 10e6)
	rep := VwReport{Meta: VwMeta{From: vwDate, End: vwDate, Days: []string{vwDate}, Params: DefaultVwapParams(), Resamples: 200, Seed: 1}}
	rep.Variants = append(rep.Variants, BuildVwapReport(VwClose, tr, cnt, StatOpts{Resamples: 200, Seed: 1}))
	dir := t.TempDir()
	paths, err := WriteVwapFiles(dir, rep)
	if err != nil || len(paths) != 3 {
		t.Fatal(err, paths)
	}
	md, _ := os.ReadFile(filepath.Join(dir, "report.md"))
	if !strings.Contains(string(md), "Bull VWAP Cross") || !strings.Contains(string(md), "Variant: close") {
		t.Error("report.md incomplete")
	}
	js, _ := os.ReadFile(filepath.Join(dir, "summary.json"))
	var back map[string]any
	if err := json.Unmarshal(js, &back); err != nil {
		t.Fatal(err)
	}
	csvb, _ := os.ReadFile(filepath.Join(dir, "trades.csv"))
	if strings.Count(string(csvb), "\n") != 2 {
		t.Errorf("csv:\n%s", csvb)
	}
}
