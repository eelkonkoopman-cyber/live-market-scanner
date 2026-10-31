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

func g2Rules(t testing.TB, id string) Grid2Rules {
	t.Helper()
	for _, r := range Grid2() {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no grid v2 variant %q", id)
	return Grid2Rules{}
}

// g2Run simulates one rule on a day.
func g2Run(t testing.TB, bars []core.Bar, r Grid2Rules) ([]VwTrade, VwCounts) {
	t.Helper()
	tr, cn := SimulateGrid2Day("TEST", vwDate, 10e6, bars, []Grid2Rules{r}, DefaultVwapParams())
	return tr[0], cn[0]
}

// prevDay is a previous-day tail closing at pc (the day change reference).
func prevDay(t testing.TB, pc float64) []core.Bar {
	return dayBars(t, "2026-10-02", "15:55", "15:59", func(k int) core.Bar {
		return core.Bar{O: pc, H: pc, L: pc, C: pc, V: 1000}
	})
}

// g2Day builds a day of flat 100 bars from 04:00 (long enough for an EMA left
// over from a different previous close to decay to exactly 100) up to the
// minute before `start`, then the specs. pc is the previous regular close (0 =
// no previous day).
func g2Day(t testing.TB, pc float64, start string, specs ...vs) []core.Bar {
	t.Helper()
	t0, ts := etMs(t, vwDate+" 04:00"), etMs(t, vwDate+" "+start)
	var day []core.Bar
	for x := t0; x < ts; x += minuteMs {
		day = append(day, core.Bar{T: x, O: 100, H: 100.003, L: 100, C: 100, V: bigV}) // typical 100.001: the VWAP sits just above the EMA, so d < 0 despite float noise
	}
	for i, s := range specs {
		day = append(day, core.Bar{T: ts + int64(i)*minuteMs, O: s.o, H: s.h, L: s.l, C: s.c, V: s.v})
	}
	if pc == 0 {
		return day
	}
	return append(prevDay(t, pc), day...)
}

// g2Scen: a small-volume 99..101 dip bar at 10:20 (day low 99; it fails the
// universe, so it fires nothing and the VWAP stays 100), flat to 10:29, the cross
// bar at 10:30 (low 100, close 101), then specs from 10:31.
func g2Scen(t testing.TB, pc float64, specs ...vs) []core.Bar {
	t.Helper()
	all := append(append([]vs{{100, 101, 99, 100, 1}}, repeatVs(flat(100, bigV), 9)...), append([]vs{crossBar()}, specs...)...)
	return g2Day(t, pc, "10:20", all...)
}

func repeatVs(v vs, n int) []vs {
	out := make([]vs, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestGrid2Registration(t *testing.T) {
	var ids []string
	for _, r := range Grid2() {
		ids = append(ids, r.ID)
	}
	if got := strings.Join(ids, " "); got != "D1 D2 D3 D4 D5 D6 D7 H1 H2 H3 H4 P1 R0" || Grid2Version != "grid v2" {
		t.Fatalf("grid v2 drifted: %s %s", got, Grid2Version)
	}
	d5, d6, d7, p1 := g2Rules(t, "D5"), g2Rules(t, "D6"), g2Rules(t, "D7"), g2Rules(t, "P1")
	if !d5.UseDayChg || d5.DayChgBelow != -2 || d5.MinBelow != 10 || d5.FromMin != 600 || d5.ToMin != 720 || d5.Stop != G2StopATR || d5.HoldMin != 0 || d5.Signal != VwIntrabar {
		t.Errorf("D5 %+v", d5)
	}
	if d6.HoldMin != 60 || d6.Stop != G2StopATR || d7.Signal != Drift5m || d7.Stop != G2StopATR || p1.Derive != "D5" {
		t.Errorf("D6/D7/P1 %+v %+v %+v", d6, d7, p1)
	}
	for id, want := range map[string]struct {
		stop string
		hold int
		dc   bool
	}{
		"H1": {G2StopCrossLow, 0, false}, "H2": {G2StopCrossLow, 60, false}, "H3": {G2StopATR, 0, false}, "H4": {G2StopDayLow, 0, false},
		"D1": {G2StopCrossLow, 0, true}, "D2": {G2StopCrossLow, 60, true}, "D3": {G2StopATR, 0, true}, "D4": {G2StopDayLow, 0, true},
	} {
		r := g2Rules(t, id)
		if r.Stop != want.stop || r.HoldMin != want.hold || r.UseDayChg != want.dc || r.Signal != VwIntrabar || r.MinBelow != 0 || r.ToMin != 0 {
			t.Errorf("%s %+v", id, r)
		}
	}
	if !g2Rules(t, "R0").Ref {
		t.Error("R0 must be the grid v1 reference")
	}
}

func TestGrid2Gate(t *testing.T) {
	d5 := g2Rules(t, "D5")
	ok := DriftSignal{DayChgPct: -2.5, BelowMin: 10, EntryMin: 10 * 60}
	if !Grid2Gate(d5, ok) {
		t.Fatal("a qualifying signal must pass")
	}
	for name, s := range map[string]DriftSignal{
		"dayChg -2 exactly": {DayChgPct: -2, BelowMin: 30, EntryMin: 650},
		"dayChg NaN":        {DayChgPct: math.NaN(), BelowMin: 30, EntryMin: 650},
		"dayChg up":         {DayChgPct: 1, BelowMin: 30, EntryMin: 650},
		"belowMin 9":        {DayChgPct: -3, BelowMin: 9, EntryMin: 650},
		"entry 09:59":       {DayChgPct: -3, BelowMin: 30, EntryMin: 599},
		"entry 12:00":       {DayChgPct: -3, BelowMin: 30, EntryMin: 720},
	} {
		if Grid2Gate(d5, s) {
			t.Errorf("%s must be gated out", name)
		}
	}
	if !Grid2Gate(d5, DriftSignal{DayChgPct: -3, BelowMin: 30, EntryMin: 719}) {
		t.Error("11:59 must pass")
	}
	if !Grid2Gate(g2Rules(t, "H1"), DriftSignal{DayChgPct: math.NaN(), BelowMin: 0, EntryMin: 570}) {
		t.Error("H1 has no gates")
	}
	d1 := g2Rules(t, "D1")
	if !Grid2Gate(d1, DriftSignal{DayChgPct: -2.01, EntryMin: 570}) || Grid2Gate(d1, DriftSignal{DayChgPct: -1.99}) {
		t.Error("D1 gates on dayChg only")
	}
}

func TestGrid2HoldToEODAndFixedStop(t *testing.T) {
	// entry 10:31 at 100.5 (stop 100, risk 0.5). Bar 2 rises; bar 3 dips to 100.4: the
	// trailing B0 stops out there, the fixed H1 stop does not.
	bars := g2Scen(t, 0,
		vs{100.5, 101, 100.6, 100.9, bigV},
		vs{100.9, 102, 101.5, 101.9, bigV},
		vs{101.9, 101.9, 100.4, 100.6, bigV},
		repeatVs(flat(101, bigV), 20)[0], repeatVs(flat(101, bigV), 20)[0])
	tr, cn := g2Run(t, bars, g2Rules(t, "H1"))
	x := one(t, tr, cn)
	if x.Reason != "eod" || x.Stop0 != 100 || x.Entry != 100.5 || x.BarsHeld != 5 || !approx(x.Exit, 101, 1e-9) {
		t.Errorf("H1 %+v", x)
	}
	if b0, c0 := gridRun(t, bars, "B0"); len(b0) != 1 || b0[0].Reason != "trail" {
		t.Errorf("B0 should have trailed out: %+v %+v", b0, c0)
	}
	// a fixed stop is hit at the stop
	stopped := g2Scen(t, 0, vs{100.5, 101, 100.6, 100.9, bigV}, vs{100.9, 101, 99.9, 100, bigV}, flat(100, bigV))
	st, sc := g2Run(t, stopped, g2Rules(t, "H1"))
	y := one(t, st, sc)
	if y.Reason != "stop" || !approx(y.Exit, 100, 1e-9) || !approx(y.R, -1, 1e-9) || y.BarsHeld != 2 {
		t.Errorf("H1 stop %+v", y)
	}
	// a gap through the stop exits at the open
	gap := g2Scen(t, 0, vs{100.5, 101, 100.6, 100.9, bigV}, vs{99.5, 99.6, 99.2, 99.3, bigV}, flat(99, bigV))
	gt, gc := g2Run(t, gap, g2Rules(t, "H1"))
	if z := one(t, gt, gc); !approx(z.Exit, 99.5, 1e-9) || z.Reason != "stop" {
		t.Errorf("gap %+v", z)
	}
}

func TestGrid2TimeExit(t *testing.T) {
	// entry bar 10:31; 70 more flat bars at 101. H2 exits at the close of the bar
	// starting at 11:31 (entry + 60 min): 61 bars held, reason time.
	specs := []vs{{100.5, 101, 100.6, 101, bigV}}
	specs = append(specs, repeatVs(flat(101, bigV), 70)...)
	bars := g2Scen(t, 0, specs...)
	tr, cn := g2Run(t, bars, g2Rules(t, "H2"))
	x := one(t, tr, cn)
	if x.Reason != "time" || x.BarsHeld != 61 || x.ExitTs != etMs(t, vwDate+" 11:31") || !approx(x.Exit, 101, 1e-9) {
		t.Errorf("H2 %+v", x)
	}
	h1t, h1c := g2Run(t, bars, g2Rules(t, "H1"))
	if y := one(t, h1t, h1c); y.Reason != "eod" || y.BarsHeld != 71 {
		t.Errorf("H1 on the same day %+v", y)
	}
	// a stop inside the hold beats the time exit
	stopped := g2Scen(t, 0, vs{100.5, 101, 100.6, 101, bigV}, vs{101, 101, 99.9, 100, bigV}, flat(100, bigV))
	st, sc := g2Run(t, stopped, g2Rules(t, "H2"))
	if z := one(t, st, sc); z.Reason != "stop" {
		t.Errorf("stop inside the hold %+v", z)
	}
	// the day ending before the 60 minutes: eod, not time
	short := g2Scen(t, 0, vs{100.5, 101, 100.6, 101, bigV}, flat(101, bigV), flat(101, bigV))
	sh, shc := g2Run(t, short, g2Rules(t, "H2"))
	if z := one(t, sh, shc); z.Reason != "eod" || z.BarsHeld != 3 {
		t.Errorf("short %+v", z)
	}
}

func TestGrid2DayLowStop(t *testing.T) {
	bars := g2Scen(t, 0, vs{100.5, 101, 100.6, 100.9, bigV}, vs{100.9, 101, 99.5, 100, bigV}, flat(100, bigV), flat(100, bigV))
	// the day's low through the cross bar is the dip bar's 99: stop 98.99
	tr, cn := g2Run(t, bars, g2Rules(t, "H4"))
	x := one(t, tr, cn)
	if !approx(x.Stop0, 98.99, 1e-9) || !approx(x.Risk, 100.5-98.99, 1e-9) || x.Reason != "eod" {
		t.Errorf("H4 %+v", x)
	}
	// the cross-low stop (100) is hit by the 99.5 low
	h1, c1 := g2Run(t, bars, g2Rules(t, "H1"))
	if y := one(t, h1, c1); y.Reason != "stop" {
		t.Errorf("H1 %+v", y)
	}
	// the low printed AFTER the cross bar does not count (as of the cross bar)
	if !approx(x.Stop0, 98.99, 1e-9) {
		t.Errorf("stop must use the low as of the cross bar: %v", x.Stop0)
	}
	// entry at or below the day-low stop is skipped (gap down below the day's low)
	gapDown := g2Scen(t, 0, vs{98, 98.5, 97.5, 98, bigV}, flat(98, bigV))
	gt, gc := g2Run(t, gapDown, g2Rules(t, "H4"))
	if len(gt) != 0 || gc.SkippedGap != 1 {
		t.Errorf("gap down: %d trades, counts %+v", len(gt), gc)
	}
	// dayLowSeries: regular hours only, running minimum, other days NaN
	dl := dayLowSeries(append(prevDay(t, 50), core.Bar{T: etMs(t, vwDate+" 09:00"), L: 1}, core.Bar{T: etMs(t, vwDate+" 09:30"), L: 7}, core.Bar{T: etMs(t, vwDate+" 09:31"), L: 9}, core.Bar{T: etMs(t, vwDate+" 09:32"), L: 5}), vwDate)
	n := len(dl)
	if !math.IsNaN(dl[0]) || !math.IsNaN(dl[n-4]) || dl[n-3] != 7 || dl[n-2] != 7 || dl[n-1] != 5 {
		t.Errorf("dayLowSeries %v", dl)
	}
}

func TestGrid2ATRStop(t *testing.T) {
	bars := g2Scen(t, 0, vs{100.5, 101, 100.6, 100.9, bigV}, flat(100.9, bigV))
	m := barIndex(bars, etMs(t, vwDate+" 10:30"))
	atr := atrSeries(bars, vwATRPeriod)[m]
	if !(atr > 0) {
		t.Fatalf("atr %v", atr)
	}
	tr, cn := g2Run(t, bars, g2Rules(t, "H3"))
	x := one(t, tr, cn)
	if !approx(x.Stop0, 100.5-1.5*atr, 1e-9) {
		t.Errorf("stop %v want %v", x.Stop0, 100.5-1.5*atr)
	}
	// no ATR history (< 14 bars): not traded, counted with the no-entry cases
	short := vwScen(t, 100, bigV, "09:41", crossBar(), vs{100.5, 101, 100.6, 100.9, bigV})
	short = short[len(short)-8:]
	if st, sc := g2Run(t, short, g2Rules(t, "H3")); len(st) != 0 {
		t.Errorf("short history traded: %+v %+v", st, sc)
	}
}

func TestGrid2ContextGatesInSimulation(t *testing.T) {
	after := []vs{{100.5, 101, 100.6, 100.9, bigV}, flat(100.9, bigV), flat(100.9, bigV)}
	down := g2Scen(t, 105, after...) // 101 vs 105 = -3.8%
	up := g2Scen(t, 100, after...)   // +1%
	none := g2Scen(t, 0, after...)   // no previous close: dayChg unknown
	for _, id := range []string{"D1", "D3", "D5"} {
		if tr, cn := g2Run(t, down, g2Rules(t, id)); len(tr) != 1 {
			t.Errorf("%s down day: %d trades %+v", id, len(tr), cn)
		}
		for name, bars := range map[string][]core.Bar{"up": up, "unknown": none} {
			if tr, _ := g2Run(t, bars, g2Rules(t, id)); len(tr) != 0 {
				t.Errorf("%s %s day must be gated out", id, name)
			}
		}
	}
	tr, _ := g2Run(t, down, g2Rules(t, "D5"))
	if x := tr[0]; x.BelowMin < 10 || x.DayChg > -2 || x.EntryMin != 10*60+31 {
		t.Errorf("D5 context %+v", x)
	}
	if h, _ := g2Run(t, up, g2Rules(t, "H3")); len(h) != 1 {
		t.Error("H3 has no gates")
	}
}

func TestGrid2EntryWindowAndFiveMinute(t *testing.T) {
	// a cross at 09:41 enters at 09:42: before the 10:00 window
	early := g2Day(t, 105, "09:41", crossBar(), vs{100.5, 101, 100.6, 100.9, bigV}, flat(100.9, bigV))
	if tr, _ := g2Run(t, early, g2Rules(t, "D5")); len(tr) != 0 {
		t.Error("a 09:42 entry must be outside D5's window")
	}
	if tr, _ := g2Run(t, early, g2Rules(t, "D3")); len(tr) != 1 {
		t.Error("D3 has no window")
	}
	// the 5m signal: candle 10:00-10:04 crossing, entry 10:05 at 105, stop = candle low 100
	five := fiveScen(t, 105)
	r := Grid2Rules{ID: "T5", Signal: Drift5m, Stop: G2StopCrossLow, MinBelow: 10, FromMin: 600, ToMin: 720}
	tr, cn := g2Run(t, five, r)
	x := one(t, tr, cn)
	if x.EntryTs != etMs(t, vwDate+" 10:05") || x.Entry != 105 || x.Stop0 != 100 || x.Shares != 10 || x.BelowMin != 30 {
		t.Errorf("5m trade %+v", x)
	}
	if d7, _ := g2Run(t, five, g2Rules(t, "D7")); len(d7) != 0 { // dayChg is +5% there
		t.Error("D7 needs a down day")
	}
}

// The grid v2 engine with the trailing exit reproduces grid v1's baseline.
func TestGrid2EngineMatchesB0WhenTrailing(t *testing.T) {
	poke := vs{100, 101, 100, 100, bigV}
	fixtures := map[string][]core.Bar{
		"trail":   vwScen(t, 100, bigV, "09:41", crossBar(), vs{100.5, 101, 100.6, 100.9, bigV}, vs{100.9, 101.5, 100.8, 101.4, bigV}, vs{101.4, 101.4, 100.7, 100.8, bigV}, flat(100, bigV)),
		"gapstop": vwScen(t, 100, bigV, "09:41", crossBar(), vs{100.5, 101, 100.6, 100.9, bigV}, vs{100.9, 101.5, 100.8, 101.4, bigV}, vs{100.3, 100.4, 100.2, 100.3, bigV}, flat(100, bigV)),
		"eod":     vwScen(t, 100, bigV, "09:41", append([]vs{crossBar()}, repeatVs(vs{101, 101.05, 101, 101.02, bigV}, 400)...)...),
		"gapskip": vwScen(t, 100, bigV, "09:41", crossBar(), vs{99.9, 100.5, 99.8, 100, bigV}, flat(100, bigV)),
		"size0":   vwScen(t, 100, bigV, "09:41", vs{100, 101, 40, 101, bigV}, vs{101, 102, 100, 101, bigV}, flat(101, bigV)),
		"rearm":   vwScen(t, 100, bigV, "09:41", poke, vs{100.2, 101, 99, 100, bigV}, poke, vs{100.1, 100.1, 99, 99.5, bigV}, flat(99, bigV)),
	}
	r := Grid2Rules{ID: "TR", Signal: VwIntrabar, Stop: G2StopCrossLow, Trail: true}
	for name, bars := range fixtures {
		want, _ := SimulateVwapCrossDay("TEST", vwDate, 10e6, bars, VwIntrabar, DefaultVwapParams())
		got, _ := g2Run(t, bars, r)
		if len(want) != len(got) {
			t.Fatalf("%s: %d vs %d trades", name, len(want), len(got))
		}
		for i := range want {
			w, g := want[i], got[i]
			if g.Reason == "trail" {
				g.Reason = "stop"
			}
			if w.CrossTs != g.CrossTs || w.EntryTs != g.EntryTs || w.Entry != g.Entry || w.Stop0 != g.Stop0 || w.ExitTs != g.ExitTs || w.Exit != g.Exit ||
				w.Reason != g.Reason || w.Sized != g.Sized || w.Shares != g.Shares || w.NetUSD != g.NetUSD || w.RNet != g.RNet || w.BarsHeld != g.BarsHeld {
				t.Errorf("%s trade %d:\nwant %+v\ngot  %+v", name, i, w, g)
			}
		}
	}
}

func TestGrid2R0IsGridV1B0(t *testing.T) {
	bars := g2Scen(t, 105, vs{100.5, 101, 100.6, 100.9, bigV}, vs{100.9, 101.5, 100.8, 101.4, bigV}, vs{101.4, 101.4, 100.7, 100.8, bigV}, flat(100, bigV))
	got, gc := g2Run(t, bars, g2Rules(t, "R0"))
	want, wc := SimulateVwapRulesDay("TEST", vwDate, 10e6, bars, gridRules(t, "B0"), DefaultVwapParams())
	if len(got) != len(want) || len(got) == 0 {
		t.Fatalf("%d vs %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("trade %d differs:\n%+v\n%+v", i, got[i], want[i])
		}
	}
	if gc.Fired != wc.Fired || gc.Traded != wc.Traded {
		t.Errorf("counts %+v %+v", gc, wc)
	}
}

func TestGrid2OnePositionPerSymbol(t *testing.T) {
	// a second cross while the position is open is ignored (and counted)
	poke := vs{100, 101, 100, 100.2, bigV}
	bars := g2Scen(t, 0, vs{100.5, 101, 100.6, 100.9, bigV}, vs{100, 100, 99.9, 99.95, bigV}, poke, vs{100.5, 101, 100.6, 100.9, bigV}, flat(100.9, bigV))
	tr, cn := g2Run(t, bars, g2Rules(t, "H4"))
	if len(tr) != 1 {
		t.Fatalf("trades %d %+v", len(tr), cn)
	}
	if cn.Fired < 1 {
		t.Errorf("counts %+v", cn)
	}
}

// ─── portfolio ──────────────────────────────────────────────────────────

func pt(t testing.TB, date, sym, entry, exit string, rnet float64) VwTrade {
	return VwTrade{Date: date, Sym: sym, EntryTs: etMs(t, date+" "+entry), ExitTs: etMs(t, date+" "+exit), RNet: rnet, Sized: true, Variant: "D5"}
}

func syms(ts []VwTrade) string {
	var s []string
	for _, x := range ts {
		s = append(s, x.Sym)
	}
	return strings.Join(s, ",")
}

func TestPortfolioMaxConcurrentOrdersByEntryTime(t *testing.T) {
	d := "2026-10-05"
	in := []VwTrade{ // deliberately not in entry order
		pt(t, d, "E", "10:31", "10:50", 1),
		pt(t, d, "D", "10:03", "10:10", 1),
		pt(t, d, "B", "10:01", "10:40", 1),
		pt(t, d, "A", "10:00", "10:30", 1),
		pt(t, d, "C", "10:02", "10:20", 1),
		pt(t, d, "F", "10:20", "10:25", 1),
	}
	kept, sk := ApplyPortfolio(in, DefaultPortfolioLimits())
	// A B C open -> D skipped; F at 10:20: C's bar (10:20) ended at 10:21 > 10:20, still open -> skipped;
	// E at 10:31: A's exit bar 10:30 ended at 10:31 -> a slot is free -> kept
	if syms(kept) != "A,B,C,E" || sk.MaxPos != 2 || sk.DayLoss != 0 {
		t.Errorf("kept %s skips %+v", syms(kept), sk)
	}
	// 2 concurrent limit
	kept, sk = ApplyPortfolio(in, PortfolioLimits{MaxConcurrent: 2})
	if syms(kept) != "A,B,E" || sk.MaxPos != 3 {
		t.Errorf("limit 2: kept %s skips %+v", syms(kept), sk)
	}
	// unsized trades are not positions
	in[3].Sized = false
	kept, _ = ApplyPortfolio(in, DefaultPortfolioLimits())
	if strings.Contains(syms(kept), "A") {
		t.Errorf("unsized kept: %s", syms(kept))
	}
}

func TestPortfolioDailyLossLimit(t *testing.T) {
	d, d2 := "2026-10-05", "2026-10-06"
	in := []VwTrade{
		pt(t, d, "A", "10:00", "10:10", -2),
		pt(t, d, "B", "10:05", "10:20", -1.5),
		pt(t, d, "C", "10:15", "10:40", 0.5),  // only A is closed (-2): allowed
		pt(t, d, "D", "10:30", "10:45", 1),    // A and B closed: -3.5 <= -3: skipped
		pt(t, d, "E", "11:00", "11:05", 1),    // still skipped
		pt(t, d2, "F", "10:00", "10:10", 1),   // a new day
		pt(t, d2, "G", "10:20", "10:30", -3),  // exactly -3 realised once closed
		pt(t, d2, "H", "10:31", "10:40", 1),   // realised F(+1) + G(-3) = -2: allowed
		pt(t, d2, "I", "10:41", "10:50", 1),   // realised -2 + 1 = -1: allowed
		pt(t, d2, "J", "10:00", "10:45", -10), // entered before anything closed
		pt(t, d2, "K", "10:55", "10:56", 1),   // realised F+G+H+I+J(-10, exit 10:45 closed) = deeply negative: skipped
	}
	kept, sk := ApplyPortfolio(in, DefaultPortfolioLimits())
	if syms(kept) != "A,B,C,F,J,G,H,I" || sk.DayLoss != 3 || sk.MaxPos != 0 {
		t.Errorf("kept %s skips %+v", syms(kept), sk)
	}
	// exactly -3R realised blocks (<=)
	exact := []VwTrade{pt(t, d, "A", "10:00", "10:10", -3), pt(t, d, "B", "10:11", "10:20", 1)}
	if kept, sk = ApplyPortfolio(exact, DefaultPortfolioLimits()); len(kept) != 1 || sk.DayLoss != 1 {
		t.Errorf("exact -3R: %s %+v", syms(kept), sk)
	}
}

// ─── drawdown, worst day, excess ─────────────────────────────────────────

func TestMaxDrawdownAndWorstDay(t *testing.T) {
	d := "2026-10-05"
	ts := []VwTrade{ // exit-time order: +1 -2 -1 +3 -0.5; the input is shuffled
		pt(t, d, "D", "13:00", "13:10", 3),
		pt(t, d, "A", "10:00", "10:10", 1),
		pt(t, d, "E", "14:00", "14:10", -0.5),
		pt(t, d, "B", "11:00", "11:10", -2),
		pt(t, d, "C", "12:00", "12:10", -1),
	}
	// cum 1, -1, -2, 1, 0.5; peak 1: drawdown 3
	if dd := MaxDrawdownR(ts); !approx(dd, 3, 1e-12) {
		t.Errorf("maxDD %v", dd)
	}
	if dd := MaxDrawdownR([]VwTrade{pt(t, d, "A", "10:00", "10:10", -2), pt(t, d, "B", "11:00", "11:10", 1)}); !approx(dd, 2, 1e-12) {
		t.Errorf("a losing start counts from 0: %v", dd)
	}
	if dd := MaxDrawdownR(nil); dd != 0 {
		t.Errorf("empty %v", dd)
	}
	if dd := MaxDrawdownR([]VwTrade{pt(t, d, "A", "10:00", "10:10", 1), pt(t, d, "B", "11:00", "11:10", 2)}); dd != 0 {
		t.Errorf("monotone %v", dd)
	}
	all := append(ts, pt(t, "2026-10-06", "Z", "10:00", "10:10", -4), pt(t, "2026-10-06", "Y", "11:00", "11:10", 1))
	w, day, n := WorstDayR(all)
	if !approx(w, -3, 1e-12) || day != "2026-10-06" || n != 2 {
		t.Errorf("worst day %v %s %d", w, day, n)
	}
	if _, _, n := WorstDayR(nil); n != 0 {
		t.Error("no days")
	}
}

func spyDay(t testing.TB, date string) []core.Bar {
	// one bar per minute 10:00..15:59, open 100+0.01k, close 100.005+0.01k
	return dayBars(t, date, "10:00", "15:59", func(k int) core.Bar {
		o := 100 + 0.01*float64(k)
		return core.Bar{O: o, H: o + 0.02, L: o - 0.01, C: o + 0.005, V: 1000}
	})
}

func TestBenchAlignmentAndExcess(t *testing.T) {
	b := NewBench("SPY")
	b.Add(vwDate, append(spyDay(t, vwDate), prevDay(t, 99)...))
	if !b.Has(vwDate) || b.Has("2026-10-06") || b.NDays() != 1 {
		t.Fatalf("days: %v", b.NDays())
	}
	// entry bar 10:02 (k=2, open 100.02), exit bar 10:07 (k=7, close 100.075)
	tr := VwTrade{Date: vwDate, EntryTs: etMs(t, vwDate+" 10:02"), ExitTs: etMs(t, vwDate+" 10:07"), RetPct: 0.5, Sized: true, Shares: 10, Entry: 100, NetUSD: 3}
	br, ok := b.TradeRet(vwDate, tr.EntryTs, tr.ExitTs)
	if !ok || !approx(br, 100*(100.075/100.02-1), 1e-9) {
		t.Fatalf("bench ret %v %v", br, ok)
	}
	// an exit inside a bar (a stop) uses that bar's close: the last bar starting at or before the exit timestamp
	if br2, _ := b.TradeRet(vwDate, tr.EntryTs, tr.ExitTs+30_000); !approx(br2, br, 1e-12) {
		t.Errorf("mid-bar exit %v vs %v", br2, br)
	}
	// the same minute in and out: open to close of that bar
	if br3, ok := b.TradeRet(vwDate, tr.EntryTs, tr.EntryTs); !ok || !approx(br3, 100*(100.025/100.02-1), 1e-9) {
		t.Errorf("same-bar %v", br3)
	}
	ts := []VwTrade{tr,
		{Date: vwDate, EntryTs: etMs(t, vwDate+" 09:45"), ExitTs: etMs(t, vwDate+" 09:50"), RetPct: 1, Sized: true},             // before the benchmark bars
		{Date: "2026-10-06", EntryTs: etMs(t, "2026-10-06 10:02"), ExitTs: etMs(t, "2026-10-06 10:07"), RetPct: 1, Sized: true}, // day missing
	}
	b.AttachTrades(ts)
	if !ts[0].BenchOK || !approx(ts[0].Excess, 0.5-br, 1e-9) || !approx(ts[0].ExcessNet, TradeNetPct(tr)-br, 1e-9) {
		t.Errorf("attached %+v", ts[0])
	}
	if ts[1].BenchOK || ts[2].BenchOK {
		t.Error("trades without benchmark bars must stay BenchOK=false")
	}
	ex := TradeExcess(ts)
	if ex.N != 1 || ex.Missing != 2 || !approx(ex.Mean, 0.5-br, 1e-9) || ex.T != 0 {
		t.Errorf("excess %+v", ex)
	}
	if TradeNetPct(tr) != 100*3.0/(10*100) {
		t.Errorf("net pct %v", TradeNetPct(tr))
	}
	var nilBench *Bench
	if _, ok := nilBench.TradeRet(vwDate, 1, 2); ok || nilBench.Has(vwDate) {
		t.Error("a nil benchmark has no data")
	}
}

func TestExcessTStat(t *testing.T) {
	var ts []VwTrade
	for _, x := range []float64{1, 2, 3} {
		ts = append(ts, VwTrade{BenchOK: true, Excess: x, ExcessNet: x - 0.1, BenchRet: 0.5})
	}
	ts = append(ts, VwTrade{}) // no benchmark
	ex := TradeExcess(ts)
	if ex.N != 3 || ex.Missing != 1 || !approx(ex.Mean, 2, 1e-12) || !approx(ex.T, 2/(1/math.Sqrt(3)), 1e-9) || !approx(ex.MeanNet, 1.9, 1e-12) || !approx(ex.MeanBen, 0.5, 1e-12) {
		t.Errorf("%+v", ex)
	}
	if e := TradeExcess([]VwTrade{{}}); e.N != 0 || e.Missing != 1 || e.Mean != 0 || e.T != 0 {
		t.Errorf("all missing %+v", e)
	}
}

func TestBenchDriftExcess(t *testing.T) {
	b := NewBench("SPY")
	b.Add(vwDate, spyDay(t, vwDate))
	sig := DriftSignal{Type: VwIntrabar, First: true, Entry: 100, Date: vwDate, EntryTs: etMs(t, vwDate+" 10:00"), Ret: [5]float64{1, 2, 3, 4, 5}}
	miss := DriftSignal{Type: VwIntrabar, First: true, Entry: 100, Date: "2026-10-06", EntryTs: etMs(t, "2026-10-06 10:00"), Ret: [5]float64{1, 2, 3, 4, 5}}
	sigs := []DriftSignal{sig, miss}
	b.AttachDrift(sigs)
	if !sigs[0].BenchOK || sigs[1].BenchOK {
		t.Fatalf("bench ok %v %v", sigs[0].BenchOK, sigs[1].BenchOK)
	}
	// open 100.00; r5 = close of the bar starting at 10:05 (k=5: 100.055); EOD = 15:59 (k=359: 103.595)
	if !approx(sigs[0].BenchRet[0], 100*(100.055/100-1), 1e-9) || !approx(sigs[0].BenchRet[4], 100*(103.595/100-1), 1e-9) {
		t.Errorf("bench %v", sigs[0].BenchRet)
	}
	for h := 0; h < 5; h++ {
		if !approx(sigs[0].Excess[h], sigs[0].Ret[h]-sigs[0].BenchRet[h], 1e-12) {
			t.Errorf("excess[%d]", h)
		}
	}
	// the markdown/csv only grow benchmark columns when a signal has them
	plain := []DriftSignal{miss}
	if strings.Contains(DriftMarkdown(plain, DefaultVwapParams(), "h"), "excess") {
		t.Error("no benchmark: the drift report must be unchanged")
	}
	if !strings.Contains(DriftMarkdown(sigs, DefaultVwapParams(), "h"), "excess") {
		t.Error("benchmark columns missing")
	}
	rows, _ := DriftCSV(plain)
	rows2, _ := DriftCSV(sigs)
	if len(rows2[0]) != len(rows[0])+11 {
		t.Errorf("csv columns %d vs %d", len(rows2[0]), len(rows[0]))
	}
}

// ─── report ─────────────────────────────────────────────────────────────

func TestGrid2ReportFiles(t *testing.T) {
	bars := g2Scen(t, 105, vs{100.5, 101, 100.6, 100.9, bigV}, flat(100.9, bigV), flat(100.9, bigV))
	rules := Grid2()
	tr, cn := SimulateGrid2Day("TEST", vwDate, 10e6, bars, rules, DefaultVwapParams())
	bench := NewBench("SPY")
	so := StatOpts{Resamples: 200, Seed: 1}
	rep := Grid2Report{Version: Grid2Version, BenchSym: "SPY", BenchMiss: []string{vwDate}, Limits: DefaultPortfolioLimits()}
	for i, r := range rules {
		bench.AttachTrades(tr[i])
		trades, c := tr[i], cn[i]
		var sk *PortfolioSkips
		if r.Derive != "" {
			var k PortfolioSkips
			trades, k = ApplyPortfolio(tr[4], DefaultPortfolioLimits())
			c, sk = cn[4], &k
		}
		v := BuildGrid2Variant(r, trades, c, so)
		v.Portfolio = sk
		rep.Variants = append(rep.Variants, v)
	}
	dir := t.TempDir()
	paths, err := WriteGrid2Files(dir, rep)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 5 {
		t.Fatalf("files %v", paths)
	}
	for _, p := range paths {
		if st, err := os.Stat(p); err != nil || st.Size() == 0 {
			t.Errorf("%s: %v", p, err)
		}
	}
	js, _ := os.ReadFile(filepath.Join(dir, "grid2.json"))
	var back Grid2Report
	if err := json.Unmarshal(js, &back); err != nil || len(back.Variants) != 13 || back.Variants[0].ID != "D1" || back.Variants[12].ID != "R0" {
		t.Fatalf("json: %v %d", err, len(back.Variants))
	}
	md := rep.Markdown()
	for _, want := range []string{"grid v2", "days without benchmark bars: 1", "Max DD", "Exits stop/trail/time/eod", "out-of-sample"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q", want)
		}
	}
	d5 := rep.Variants[4]
	if d5.ID != "D5" || d5.Stats.Trades != 1 || d5.Extra.DaysTraded != 1 || d5.Extra.Excess.Missing != 1 {
		t.Errorf("D5 %+v", d5)
	}
	if p1 := rep.Variants[11]; p1.ID != "P1" || p1.Portfolio == nil || p1.Stats.Trades != 1 {
		t.Errorf("P1 %+v", p1)
	}
}
