package backtest

import (
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/emaflag"
)

func rulesNamed(t *testing.T, name string) Rules {
	t.Helper()
	r, ok := VariantByName(name)
	if !ok {
		t.Fatalf("no variant %s", name)
	}
	return r
}

// A flat day: px 10, volume 100k a minute. The 9 EMA is exactly 10, so the
// 1% trail sits at 9.90; a signal with trigger 10 and stop 9.50 sizes to
// min(50/0.5 = 100, 2000/10 = 200, 2% of 5 x 100k = 10000) = 100 shares.
func flatDay(t *testing.T, sym string) *Candidate {
	t.Helper()
	return mkCand(sym, day(t, 10, 100_000))
}

func tradeOf(t *testing.T, dr DayResult, sym string) Trade {
	t.Helper()
	for _, tr := range dr.Trades {
		if tr.Sym == sym {
			return tr
		}
	}
	t.Fatalf("no trade in %s: trades %v skips %v", sym, symsOf(dr), dr.Skips)
	return Trade{}
}

func symsOf(dr DayResult) []string {
	var s []string
	for _, tr := range dr.Trades {
		s = append(s, tr.Sym+"@"+ETClock(tr.EntryTs))
	}
	return s
}

// ─── BEAT 2026-10-05: the exit variants on a real tape ───────────────────

func beatDay(t *testing.T) PreparedDay {
	t.Helper()
	bars := loadFixture(t, "beat-2026-10-05")
	c := Candidate{Sym: "BEAT", Date: "2026-10-05", Adv10: 5_000_000, Bars: bars}
	pd := PrepareDay("2026-10-05", []Candidate{c}, emaflag.DefaultParams(), 80)
	if len(pd.Signals) != 1 {
		t.Fatalf("BEAT: %d signals, want 1", len(pd.Signals))
	}
	return pd
}

func TestExitVariantsOnBEAT(t *testing.T) {
	pd := beatDay(t)

	// V1: the trailing stop order at EMA(09:13 bar) * 0.99 is touched in the 09:14 bar
	// (open 0.858, low 0.82301): it exits at the trail, near 0.8246, before costs.
	dr := SimulateDay(pd, rulesNamed(t, "V1"))
	if len(dr.Trades) != 1 {
		t.Fatalf("V1: %d trades, skips %v", len(dr.Trades), dr.Skips)
	}
	v1 := dr.Trades[0]
	if ETClock(v1.ExitTs) != "09:14" || v1.ExitKind != ExitStop || !approx(v1.ExitRaw, 0.8246, 0.0002) {
		t.Errorf("V1 exit %s %s at %.4f, want 09:14 stop near 0.8246", ETClock(v1.ExitTs), v1.ExitKind, v1.ExitRaw)
	}
	if ETClock(v1.EntryTs) != "09:00" || !approx(v1.EntryRaw, 0.6477374, 1e-9) {
		t.Errorf("V1 entry %s at %.7f, want 09:00 at 0.6477374", ETClock(v1.EntryTs), v1.EntryRaw)
	}

	// V2: the tighter trail (0.5%) is touched in the same bar, higher
	dr2 := SimulateDay(pd, rulesNamed(t, "V2"))
	v2 := dr2.Trades[0]
	if ETClock(v2.ExitTs) != "09:14" || !(v2.ExitRaw > v1.ExitRaw) {
		t.Errorf("V2 exit %s at %.4f, want 09:14 above V1's %.4f", ETClock(v2.ExitTs), v2.ExitRaw, v1.ExitRaw)
	}

	// V3: close mode: the engine's EXIT - the 09:15 bar CLOSES at 0.801633 under its trail
	dr3 := SimulateDay(pd, rulesNamed(t, "V3"))
	v3 := dr3.Trades[0]
	if ETClock(v3.ExitTs) != "09:15" || v3.ExitKind != ExitClose || !approx(v3.ExitRaw, 0.8016, 0.0001) {
		t.Errorf("V3 exit %s %s at %.4f, want 09:15 close at 0.8016", ETClock(v3.ExitTs), v3.ExitKind, v3.ExitRaw)
	}

	// the same position, the same entry, whatever the exit: sized once from the engine's stop
	if v1.Shares != v3.Shares || v1.Shares < 1 {
		t.Errorf("shares V1 %d V3 %d", v1.Shares, v3.Shares)
	}
	// risk $50 / (0.6477374 - 0.599445) = 1035 shares
	if v1.Shares != 1035 {
		t.Errorf("shares = %d, want 1035 = floor(50 / (trigger - stop))", v1.Shares)
	}

	// V4 and V5 trade it the same way (09:00 is inside every window, one trade under every limit)
	for _, name := range []string{"V4", "V5"} {
		d := SimulateDay(pd, rulesNamed(t, name))
		if len(d.Trades) != 1 || d.Trades[0].ExitRaw != v1.ExitRaw {
			t.Errorf("%s: trades %d", name, len(d.Trades))
		}
	}
}

// ─── Entry fill ──────────────────────────────────────────────────────────

func TestEntryNoFillOnGapAndRefund(t *testing.T) {
	r := rulesNamed(t, "V1")
	r.MaxTradesPerDay = 1 // so a refunded slot is visible

	a, b := flatDay(t, "AAA"), flatDay(t, "BBB")
	setBar(t, a.Bars, "2026-10-05 09:00", 10.15, 10.2, 10.15, 10.18) // opened above the limit (10.10)
	setBar(t, b.Bars, "2026-10-05 09:01", 10.05, 10.2, 10.05, 10.1)  // opened between trigger and limit

	dr := SimulateDay(dayOf(sigAt(t, a, "2026-10-05 09:00", 10, 9.5, 1), sigAt(t, b, "2026-10-05 09:01", 10, 9.5, 2)), r)
	if dr.Skips[SkipNoFill] != 1 {
		t.Errorf("skips = %v, want one no-fill", dr.Skips)
	}
	// the gap signal used no slot: the one trade of the day is still available to BBB
	tr := tradeOf(t, dr, "BBB")
	if tr.EntryRaw != 10.05 {
		t.Errorf("BBB fills at the larger of trigger and open: %v, want 10.05", tr.EntryRaw)
	}
	if want := 10.05 * 1.0025; !approx(tr.EntryFill, want, 1e-9) {
		t.Errorf("entry fill %v, want %v (0.25%% worse)", tr.EntryFill, want)
	}
	if dr.Entries != 1 {
		t.Errorf("entries = %d, want 1", dr.Entries)
	}

	// opening exactly at the limit still fills, and the slippage cannot push it through the limit
	c := flatDay(t, "CCC")
	setBar(t, c.Bars, "2026-10-05 09:00", 10.10, 10.2, 10.1, 10.15)
	r.MaxTradesPerDay = 10
	dr = SimulateDay(dayOf(sigAt(t, c, "2026-10-05 09:00", 10, 9.5, 1)), r)
	tr = tradeOf(t, dr, "CCC")
	if tr.EntryRaw != 10.10 || tr.EntryFill != 10.10 {
		t.Errorf("open at the limit: raw %v fill %v, want both 10.10 (a limit order never fills above its limit)", tr.EntryRaw, tr.EntryFill)
	}
}

// ─── Sizing ──────────────────────────────────────────────────────────────

func TestSizing(t *testing.T) {
	r := rulesNamed(t, "V1")
	run := func(c *Candidate, trigger, stop float64, mod func(*Rules)) DayResult {
		rr := r
		if mod != nil {
			mod(&rr)
		}
		return SimulateDay(dayOf(sigAt(t, c, "2026-10-05 09:00", trigger, stop, 1)), rr)
	}

	// risk binds: 50 / 0.5 = 100
	if tr := tradeOf(t, run(flatDay(t, "R"), 10, 9.5, nil), "R"); tr.Shares != 100 {
		t.Errorf("risk-bound shares = %d, want 100", tr.Shares)
	}
	// max position binds: 50 / 0.05 = 1000 but 2000 / 10 = 200
	if tr := tradeOf(t, run(flatDay(t, "P"), 10, 9.95, nil), "P"); tr.Shares != 200 {
		t.Errorf("position-bound shares = %d, want 200", tr.Shares)
	}
	// liquidity binds: 2% of the five bars BEFORE the entry bar (5 x 4000 = 20000) = 400 > 200; 5 x 1000 -> 100
	liq := mkCand("L", day(t, 10, 1000))
	if tr := tradeOf(t, run(liq, 10, 9.95, nil), "L"); tr.Shares != 100 {
		t.Errorf("liquidity-bound shares = %d, want 100 = 2%% of 5 x 1000", tr.Shares)
	}
	// ... and the entry bar's own volume is not part of it
	liq2 := mkCand("L2", day(t, 10, 1000))
	liq2.Bars[barIndex(liq2.Bars, etMs(t, "2026-10-05 09:00"))].V = 10_000_000
	if tr := tradeOf(t, run(liq2, 10, 9.95, nil), "L2"); tr.Shares != 100 {
		t.Errorf("entry bar volume leaked into the liquidity cap: shares = %d, want 100", tr.Shares)
	}
	// a minute with no print counts 0: with only two of the five prior bars present, 2% of 2000 = 40
	sparse := mkCand("S", dropBars(t, day(t, 10, 1000), "2026-10-05 08:57", "2026-10-05 08:59"))
	if tr := tradeOf(t, run(sparse, 10, 9.95, nil), "S"); tr.Shares != 40 {
		t.Errorf("sparse-bar shares = %d, want 40", tr.Shares)
	}

	// under one share
	if d := run(mkCand("Q", day(t, 10, 9)), 10, 9.5, nil); d.Skips[SkipSizeLiq] != 1 || len(d.Trades) != 0 {
		t.Errorf("liquidity under 1 share: %v", d.Skips)
	}
	if d := run(flatDay(t, "Q2"), 10, 4, func(r *Rules) { r.RiskPerTradeUSD = 5 }); d.Skips[SkipSizeRisk] != 1 {
		t.Errorf("risk under 1 share: %v", d.Skips)
	}
	if d := run(flatDay(t, "Q3"), 10, 9.5, func(r *Rules) { r.MaxPositionUSD = 5 }); d.Skips[SkipSizePos] != 1 {
		t.Errorf("position under 1 share: %v", d.Skips)
	}

	// no usable stop / trigger not above the stop / price outside the range
	if d := run(flatDay(t, "N"), 10, math.NaN(), nil); d.Skips[SkipNoStop] != 1 {
		t.Errorf("NaN stop: %v", d.Skips)
	}
	if d := run(flatDay(t, "N2"), 10, 10, nil); d.Skips[SkipNoStop] != 1 {
		t.Errorf("stop == trigger: %v", d.Skips)
	}
	if d := run(mkCand("N3", day(t, 0.1, 100_000)), 0.1, 0.09, nil); d.Skips[SkipPrice] != 1 {
		t.Errorf("trigger under $0.20: %v", d.Skips)
	}
	if d := run(mkCand("N4", day(t, 60, 100_000)), 60, 59, nil); d.Skips[SkipPrice] != 1 {
		t.Errorf("trigger over $50: %v", d.Skips)
	}
	// 50 / (1 - 0.95) is 999.9999999999993 in floating point: 1000 shares, not 999
	if got := floorShares(50 / (1.0 - 0.95)); got != 1000 {
		t.Errorf("floorShares(50/(1-0.95)) = %d, want 1000", got)
	}
}

// ─── Window, limits, ordering across symbols ─────────────────────────────

func TestWindow(t *testing.T) {
	a := flatDay(t, "AAA")
	b := flatDay(t, "BBB")
	c := flatDay(t, "CCC")
	// 10:59 inside [04:00, 11:00); 11:00 is not (half-open, like autotrade.inWindow)
	dr := SimulateDay(dayOf(
		sigAt(t, a, "2026-10-05 10:59", 10, 9.5, 1),
		sigAt(t, b, "2026-10-05 11:00", 10, 9.5, 2),
		sigAt(t, c, "2026-10-05 14:00", 10, 9.5, 3),
	), rulesNamed(t, "V1"))
	if len(dr.Trades) != 1 || dr.Trades[0].Sym != "AAA" || dr.Skips[SkipWindow] != 2 {
		t.Errorf("V1: trades %v skips %v, want only AAA with 2 window skips", symsOf(dr), dr.Skips)
	}
	// V4's window runs to 15:45
	dr = SimulateDay(dayOf(
		sigAt(t, a, "2026-10-05 10:59", 10, 9.5, 1),
		sigAt(t, b, "2026-10-05 11:00", 10, 9.5, 2),
		sigAt(t, c, "2026-10-05 15:44", 10, 9.5, 3),
		sigAt(t, flatDay(t, "DDD"), "2026-10-05 15:45", 10, 9.5, 4),
	), rulesNamed(t, "V4"))
	if len(dr.Trades) != 3 || dr.Skips[SkipWindow] != 1 {
		t.Errorf("V4: trades %v skips %v, want 3 trades and the 15:45 signal outside", symsOf(dr), dr.Skips)
	}
}

func TestMaxOpenAndRankOrderAtTheSameMinute(t *testing.T) {
	r := rulesNamed(t, "V1") // 3 open positions
	mk := func(sym string, rank int, stamp string) Signal {
		return sigAt(t, flatDay(t, sym), stamp, 10, 9.5, rank)
	}
	// four signals in one minute: the ranks decide who gets the three slots (the live scan
	// evaluates the strongest pmVolX first)
	dr := SimulateDay(dayOf(
		mk("D", 4, "2026-10-05 09:00"), mk("A", 3, "2026-10-05 09:00"), mk("B", 1, "2026-10-05 09:00"), mk("C", 2, "2026-10-05 09:00"),
		mk("E", 1, "2026-10-05 09:01"),
	), r)
	got := map[string]bool{}
	for _, tr := range dr.Trades {
		got[tr.Sym] = true
	}
	if !got["B"] || !got["C"] || !got["A"] || got["D"] || got["E"] || len(dr.Trades) != 3 {
		t.Errorf("trades %v, want B, C, A (ranks 1, 2, 3) and neither D (rank 4) nor E (a minute later)", symsOf(dr))
	}
	if dr.Skips[SkipMaxOpen] != 2 {
		t.Errorf("skips %v, want 2 x max open", dr.Skips)
	}
	if dr.MaxOpen != 3 {
		t.Errorf("MaxOpen = %d", dr.MaxOpen)
	}
}

func TestSymbolAndTradeLimitsAndSameBarExit(t *testing.T) {
	r := rulesNamed(t, "V1")
	r.MaxEntriesPerSym = 1
	r.MaxTradesPerDay = 3
	r.MaxOpenPositions = 10

	a := flatDay(t, "AAA")
	// AAA's first position is stopped out in the 09:10 bar (low under the 9.90 trail)
	setBar(t, a.Bars, "2026-10-05 09:10", 10, 10, 9.0, 9.1)
	dr := SimulateDay(dayOf(
		sigAt(t, a, "2026-10-05 09:00", 10, 9.5, 1),
		sigAt(t, a, "2026-10-05 09:05", 10, 9.5, 1), // still holding AAA
		sigAt(t, a, "2026-10-05 09:10", 10, 9.5, 1), // the bar it exits in: still counted as open (stop first)
		sigAt(t, a, "2026-10-05 09:11", 10, 9.5, 1), // closed, but the symbol's one entry is spent
		sigAt(t, flatDay(t, "BBB"), "2026-10-05 09:12", 10, 9.5, 2),
		sigAt(t, flatDay(t, "CCC"), "2026-10-05 09:13", 10, 9.5, 3),
		sigAt(t, flatDay(t, "DDD"), "2026-10-05 09:14", 10, 9.5, 4), // the fourth trade of a 3-trade day
	), r)
	if dr.Skips[SkipInSymbol] != 2 || dr.Skips[SkipMaxSymbol] != 1 || dr.Skips[SkipMaxTrades] != 1 || len(dr.Trades) != 3 {
		t.Errorf("trades %v skips %v, want 3 trades, 2 in-symbol, 1 max-symbol, 1 max-trades", symsOf(dr), dr.Skips)
	}
	if tr := tradeOf(t, dr, "AAA"); ETClock(tr.ExitTs) != "09:10" || tr.ExitKind != ExitStop || !approx(tr.ExitRaw, 9.9, 1e-9) {
		t.Errorf("AAA exit %s %s at %v, want 09:10 stop at 9.90", ETClock(tr.ExitTs), tr.ExitKind, tr.ExitRaw)
	}

	// the one-position limit: the bar the position exits in still counts it, the next bar does not
	r2 := rulesNamed(t, "V1")
	r2.MaxOpenPositions = 1
	a2 := flatDay(t, "AAA")
	setBar(t, a2.Bars, "2026-10-05 09:10", 10, 10, 9.0, 9.1)
	dr = SimulateDay(dayOf(
		sigAt(t, a2, "2026-10-05 09:00", 10, 9.5, 1),
		sigAt(t, flatDay(t, "BBB"), "2026-10-05 09:10", 10, 9.5, 2),
		sigAt(t, flatDay(t, "CCC"), "2026-10-05 09:11", 10, 9.5, 3),
	), r2)
	if dr.Skips[SkipMaxOpen] != 1 || len(dr.Trades) != 2 {
		t.Errorf("trades %v skips %v, want AAA and CCC, with BBB (09:10, AAA's exit bar) refused", symsOf(dr), dr.Skips)
	}
	if tradeOf(t, dr, "CCC").EntryTs != etMs(t, "2026-10-05 09:11") {
		t.Errorf("CCC entry %s", ETClock(tradeOf(t, dr, "CCC").EntryTs))
	}
}

func TestDailyLossHaltRealized(t *testing.T) {
	r := rulesNamed(t, "V1") // $200
	a := flatDay(t, "AAA")
	// AAA gaps from 10 to 7 in the 09:04 bar: the trail (9.90) is jumped, the exit fills at the open 7.00.
	// (6.965 - 10.025) * 100 = -306 realized, which is more than $200.
	setBar(t, a.Bars, "2026-10-05 09:04", 7, 7, 7, 7)
	for i := barIndex(a.Bars, etMs(t, "2026-10-05 09:05")); i < len(a.Bars); i++ {
		a.Bars[i].O, a.Bars[i].H, a.Bars[i].L, a.Bars[i].C = 7, 7, 7, 7
	}
	dr := SimulateDay(dayOf(
		sigAt(t, a, "2026-10-05 09:00", 10, 9.5, 1),
		sigAt(t, flatDay(t, "CCC"), "2026-10-05 09:04", 10, 9.5, 2), // inside the exit bar: the loss is not realized yet
		sigAt(t, flatDay(t, "BBB"), "2026-10-05 09:05", 10, 9.5, 3), // after it: halted
		sigAt(t, flatDay(t, "DDD"), "2026-10-05 09:30", 10, 9.5, 4),
	), r)
	if !dr.Halted || dr.HaltTs != etMs(t, "2026-10-05 09:05") {
		t.Errorf("halted=%v at %s, want 09:05", dr.Halted, ETClock(dr.HaltTs))
	}
	if dr.Skips[SkipHalted] != 2 || len(dr.Trades) != 2 {
		t.Errorf("trades %v skips %v, want AAA + CCC and two halted", symsOf(dr), dr.Skips)
	}
	if tr := tradeOf(t, dr, "AAA"); !approx(tr.pxPnL, -306, 1e-6) {
		t.Errorf("AAA price P&L = %v, want -306", tr.pxPnL)
	}

	// V5's tighter $150 limit trips on a smaller loss the $200 limit allows
	b := flatDay(t, "AAA")
	setBar(t, b.Bars, "2026-10-05 09:04", 8.5, 8.5, 8.5, 8.5) // exit at 8.5: (8.4575 - 10.025) * 100 = -156.75
	for i := barIndex(b.Bars, etMs(t, "2026-10-05 09:05")); i < len(b.Bars); i++ {
		b.Bars[i].O, b.Bars[i].H, b.Bars[i].L, b.Bars[i].C = 8.5, 8.5, 8.5, 8.5
	}
	sigs := func() PreparedDay {
		return dayOf(sigAt(t, b, "2026-10-05 09:00", 10, 9.5, 1), sigAt(t, flatDay(t, "BBB"), "2026-10-05 09:06", 10, 9.5, 2))
	}
	if d := SimulateDay(sigs(), rulesNamed(t, "V1")); d.Halted || len(d.Trades) != 2 {
		t.Errorf("V1 ($200): halted=%v trades %v", d.Halted, symsOf(d))
	}
	if d := SimulateDay(sigs(), rulesNamed(t, "V5")); !d.Halted || d.Skips[SkipHalted] != 1 || len(d.Trades) != 1 {
		t.Errorf("V5 ($150): halted=%v trades %v skips %v", d.Halted, symsOf(d), d.Skips)
	}
}

func TestDailyLossHaltOnMarkedOpenPnL(t *testing.T) {
	r := rulesNamed(t, "V1")
	r.MaxDailyLossUSD = 5
	a := flatDay(t, "AAA")
	// the 09:02 bar closes at 9.95 (low 9.95 is above the 9.90 trail: still holding): marked
	// (9.95 - 10.025) * 100 = -7.50, under -5
	setBar(t, a.Bars, "2026-10-05 09:02", 10, 10, 9.95, 9.95)
	dr := SimulateDay(dayOf(
		sigAt(t, a, "2026-10-05 09:00", 10, 9.5, 1),
		sigAt(t, flatDay(t, "CCC"), "2026-10-05 09:02", 10, 9.5, 2), // marked at the 09:01 close (10.00): -2.50, fine
		sigAt(t, flatDay(t, "BBB"), "2026-10-05 09:03", 10, 9.5, 3), // marked at the 09:02 close: halted
	), r)
	if !dr.Halted || dr.HaltTs != etMs(t, "2026-10-05 09:03") || dr.Skips[SkipHalted] != 1 || len(dr.Trades) != 2 {
		t.Errorf("halted=%v at %s, trades %v skips %v", dr.Halted, ETClock(dr.HaltTs), symsOf(dr), dr.Skips)
	}
}

// ─── Exits on synthetic tapes ────────────────────────────────────────────

// exitOn runs one signal (trigger 10, stop 9.50) on a flat 10.00 tape that the
// caller has edited, under the variant, and returns the trade.
func exitOn(t *testing.T, variant string, edit func(bars []core.Bar) []core.Bar) Trade {
	t.Helper()
	c := mkCand("X", edit(day(t, 10, 100_000)))
	dr := SimulateDay(dayOf(sigAt(t, c, "2026-10-05 09:00", 10, 9.5, 1)), rulesNamed(t, variant))
	if len(dr.Trades) != 1 {
		t.Fatalf("%s: trades %v skips %v", variant, symsOf(dr), dr.Skips)
	}
	return dr.Trades[0]
}

func TestTouchTrailExit(t *testing.T) {
	// the 09:10 bar dips to 9.89, under the 9.90 trail: exit AT the trail
	tr := exitOn(t, "V1", func(b []core.Bar) []core.Bar { setBar(t, b, "2026-10-05 09:10", 10, 10, 9.89, 9.95); return b })
	if ETClock(tr.ExitTs) != "09:10" || tr.ExitKind != ExitStop || !approx(tr.ExitRaw, 9.9, 1e-9) {
		t.Errorf("touch: %s %s at %v, want 09:10 stop at 9.90", ETClock(tr.ExitTs), tr.ExitKind, tr.ExitRaw)
	}
	// a low exactly AT the trail touches it (<=)
	tr = exitOn(t, "V1", func(b []core.Bar) []core.Bar { setBar(t, b, "2026-10-05 09:10", 10, 10, 9.9, 9.95); return b })
	if ETClock(tr.ExitTs) != "09:10" {
		t.Errorf("a low at the trail must touch it: exit %s", ETClock(tr.ExitTs))
	}
	// a bar that opens under the trail fills at the open, not at the trail
	tr = exitOn(t, "V1", func(b []core.Bar) []core.Bar { setBar(t, b, "2026-10-05 09:10", 9.6, 9.8, 9.5, 9.7); return b })
	if !approx(tr.ExitRaw, 9.6, 1e-9) {
		t.Errorf("gap down: exit at %v, want the open 9.60", tr.ExitRaw)
	}
	// V2's 0.5% trail sits at 9.95: a low of 9.93 touches it, but not V1's 9.90
	edit := func(b []core.Bar) []core.Bar { setBar(t, b, "2026-10-05 09:10", 10, 10, 9.93, 9.97); return b }
	if tr := exitOn(t, "V2", edit); ETClock(tr.ExitTs) != "09:10" || !approx(tr.ExitRaw, 9.95, 1e-9) {
		t.Errorf("V2: exit %s at %v, want 09:10 at 9.95", ETClock(tr.ExitTs), tr.ExitRaw)
	}
	if tr := exitOn(t, "V1", edit); ETClock(tr.ExitTs) == "09:10" {
		t.Errorf("V1 must not be touched by a 9.93 low")
	}
}

func TestTrailIsRaisedAfterEachClosedBarAndNeverLowered(t *testing.T) {
	// a ramp: 10.00 -> 12.00 over 20 minutes from 09:01, then a dip. The trail follows
	// the EMA up (never down), so a later low well above the INITIAL stop still exits.
	c := mkCand("X", day(t, 10, 100_000))
	px := 10.0
	for i := 1; i <= 20; i++ {
		px += 0.1
		stamp := ETStamp(etMs(t, "2026-10-05 09:00") + int64(i)*minuteMs)
		setBar(t, c.Bars, stamp, px-0.1, px, px-0.1, px)
	}
	// 09:21: gives some back, to 11.40: far above the stale initial stop (9.50), under the raised trail
	setBar(t, c.Bars, "2026-10-05 09:21", 12, 12, 11.4, 11.8)
	for i := 22; i < 100; i++ {
		stamp := ETStamp(etMs(t, "2026-10-05 09:00") + int64(i)*minuteMs)
		setBar(t, c.Bars, stamp, 11.8, 11.8, 11.8, 11.8)
	}
	closes := make([]float64, len(c.Bars))
	for i := range c.Bars {
		closes[i] = c.Bars[i].C
	}
	ema := emaSeries(closes, 9)
	i0 := barIndex(c.Bars, etMs(t, "2026-10-05 09:00"))
	i21 := barIndex(c.Bars, etMs(t, "2026-10-05 09:21"))
	// the trail in force during the 09:21 bar: the max over the closed bars i0..i21-1 of EMA * 0.99
	want := 9.5
	for k := i0; k < i21; k++ {
		want = math.Max(want, ema[k]*0.99)
	}
	if want < 11 {
		t.Fatalf("test setup: the trail only reached %v", want)
	}
	dr := SimulateDay(dayOf(sigAt(t, c, "2026-10-05 09:00", 10, 9.5, 1)), rulesNamed(t, "V1"))
	tr := dr.Trades[0]
	if ETClock(tr.ExitTs) != "09:21" || tr.ExitKind != ExitStop || !approx(tr.ExitRaw, want, 1e-9) {
		t.Errorf("exit %s %s at %v, want 09:21 stop at the raised trail %v", ETClock(tr.ExitTs), tr.ExitKind, tr.ExitRaw, want)
	}
	if tr.ExitRaw <= 9.5 {
		t.Errorf("the trail was never raised above the initial stop")
	}
}

func TestCloseModeVersusTouch(t *testing.T) {
	// 09:10 dips to 9.92 (above the 9.90 trail) and CLOSES at 9.85 (under it); the next bars sit at 9.85
	edit := func(b []core.Bar) []core.Bar {
		setBar(t, b, "2026-10-05 09:10", 10, 10, 9.92, 9.85)
		for i := barIndex(b, etMs(t, "2026-10-05 09:11")); i < len(b); i++ {
			b[i].O, b[i].H, b[i].L, b[i].C = 9.85, 9.85, 9.85, 9.85
		}
		return b
	}
	// close mode exits at that close
	v3 := exitOn(t, "V3", edit)
	if ETClock(v3.ExitTs) != "09:10" || v3.ExitKind != ExitClose || !approx(v3.ExitRaw, 9.85, 1e-9) {
		t.Errorf("V3: %s %s at %v, want 09:10 close at 9.85", ETClock(v3.ExitTs), v3.ExitKind, v3.ExitRaw)
	}
	// the touch stop is NOT touched in 09:10 (low 9.92 > 9.90): it goes in the next bar, at the open
	v1 := exitOn(t, "V1", edit)
	if ETClock(v1.ExitTs) != "09:11" || v1.ExitKind != ExitStop || !approx(v1.ExitRaw, 9.85, 1e-9) {
		t.Errorf("V1: %s %s at %v, want 09:11 stop at the open 9.85", ETClock(v1.ExitTs), v1.ExitKind, v1.ExitRaw)
	}

	// an intrabar plunge through the trail and the hard stop, closing back up: V1 and V3 both stop,
	// V1 at the trail (9.90), V3 at the hard stop (9.50) - close mode keeps the entry's stop as the order
	edit2 := func(b []core.Bar) []core.Bar { setBar(t, b, "2026-10-05 09:10", 10, 10, 9.4, 9.95); return b }
	if tr := exitOn(t, "V1", edit2); !approx(tr.ExitRaw, 9.9, 1e-9) || tr.ExitKind != ExitStop {
		t.Errorf("V1 plunge: %v %s", tr.ExitRaw, tr.ExitKind)
	}
	if tr := exitOn(t, "V3", edit2); !approx(tr.ExitRaw, 9.5, 1e-9) || tr.ExitKind != ExitStop || ETClock(tr.ExitTs) != "09:10" {
		t.Errorf("V3 plunge: %v %s at %s, want the hard stop 9.50", tr.ExitRaw, tr.ExitKind, ETClock(tr.ExitTs))
	}
	// a plunge that GAPS under the hard stop fills at the open
	edit3 := func(b []core.Bar) []core.Bar { setBar(t, b, "2026-10-05 09:10", 9.3, 9.4, 9.2, 9.3); return b }
	if tr := exitOn(t, "V3", edit3); !approx(tr.ExitRaw, 9.3, 1e-9) {
		t.Errorf("V3 gap: %v, want the open 9.30", tr.ExitRaw)
	}
}

func TestEntryBarStopComesFirst(t *testing.T) {
	// the break bar itself dipped under the stop: with no way to order the dip and the break in a
	// minute bar, the trade is stopped out at the stop on its own entry bar (all variants)
	edit := func(b []core.Bar) []core.Bar { setBar(t, b, "2026-10-05 09:00", 10, 10.2, 9.45, 10.1); return b }
	for _, v := range []string{"V1", "V3"} {
		tr := exitOn(t, v, edit)
		if tr.ExitTs != tr.EntryTs || tr.ExitKind != ExitStop || !approx(tr.ExitRaw, 9.5, 1e-9) {
			t.Errorf("%s: exit %s %s at %v, want the entry bar stopped at 9.50", v, ETClock(tr.ExitTs), tr.ExitKind, tr.ExitRaw)
		}
	}
}

func TestFlattenAndEndOfData(t *testing.T) {
	tr := exitOn(t, "V1", func(b []core.Bar) []core.Bar {
		setBar(t, b, "2026-10-05 15:50", 10.3, 10.4, 10.2, 10.3)
		return b
	})
	if ETClock(tr.ExitTs) != "15:50" || tr.ExitKind != ExitFlatten || !approx(tr.ExitRaw, 10.3, 1e-9) {
		t.Errorf("flatten: %s %s at %v, want 15:50 at that bar's open 10.30", ETClock(tr.ExitTs), tr.ExitKind, tr.ExitRaw)
	}
	// no bar from 15:50 to 15:54: the first bar at or after 15:50 is 15:55
	tr = exitOn(t, "V1", func(b []core.Bar) []core.Bar {
		b = dropBars(t, b, "2026-10-05 15:50", "2026-10-05 15:54")
		setBar(t, b, "2026-10-05 15:55", 10.2, 10.3, 10.1, 10.2)
		return b
	})
	if ETClock(tr.ExitTs) != "15:55" || !approx(tr.ExitRaw, 10.2, 1e-9) {
		t.Errorf("sparse flatten: %s at %v, want 15:55 at 10.20", ETClock(tr.ExitTs), tr.ExitRaw)
	}
	// the stop is not consulted at the flatten bar (the position is sold at the open)
	tr = exitOn(t, "V1", func(b []core.Bar) []core.Bar {
		setBar(t, b, "2026-10-05 15:50", 10.5, 10.5, 9.0, 9.0)
		return b
	})
	if tr.ExitKind != ExitFlatten || !approx(tr.ExitRaw, 10.5, 1e-9) {
		t.Errorf("flatten bar: %s at %v", tr.ExitKind, tr.ExitRaw)
	}
	// the data ends with the position open: the last close
	tr = exitOn(t, "V1", func(b []core.Bar) []core.Bar {
		b = b[:barIndex(b, etMs(t, "2026-10-05 10:00"))+1]
		b[len(b)-1].C = 10.07
		b[len(b)-1].H = 10.07
		return b
	})
	if tr.ExitKind != ExitEOD || ETClock(tr.ExitTs) != "10:00" || !approx(tr.ExitRaw, 10.07, 1e-9) {
		t.Errorf("end of data: %s %s at %v", ETClock(tr.ExitTs), tr.ExitKind, tr.ExitRaw)
	}
}

// The close-mode exit is the engine's own EXIT: replay every fixture's entries and compare. And a stop-mode exit is
// never later than the engine's EXIT (a close under the trail implies the touch).
func TestExitsAgainstTheEngineOnEveryFixture(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(fixturesDir, "*.json"))
	compared, closeExits := 0, 0
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		if strings.HasPrefix(name, "zz-") {
			continue // the synthetic tie fixtures
		}
		bars := loadFixture(t, name)
		if len(bars) < 20 {
			continue
		}
		date := ETDate(bars[len(bars)-1].T)
		c := &Candidate{Sym: name, Date: date, Adv10: 1, Bars: bars}
		p := emaflag.DefaultParams()
		sigs := Signals(c, p)
		if len(sigs) == 0 {
			continue
		}
		full := emaflag.Evaluate(bars, minuteMs, bars[len(bars)-1].T+minuteMs, p)
		for _, v := range []string{"V1", "V3"} {
			r := rulesNamed(t, v)
			r.MaxOpenPositions, r.MaxTradesPerDay, r.MaxEntriesPerSym, r.MaxLiquidityPct = 100, 100, 100, 100
			r.MinPrice, r.MaxPrice = 0.0001, 100000
			r.MaxDailyLossUSD = 1e9
			r.Costs = NoCosts()
			for _, s := range sigs {
				pd := PreparedDay{Date: date, Signals: []Signal{s}}
				dr := SimulateDay(pd, r)
				if len(dr.Trades) != 1 {
					continue // no fill, or no usable size
				}
				tr := dr.Trades[0]
				// the engine's EXIT for this position: the first EXIT at or after the entry bar
				var eng *emaflag.Event
				for i := range full.Events {
					if e := &full.Events[i]; e.Kind == emaflag.KindExit && e.Ts >= s.Ts {
						eng = e
						break
					}
				}
				if eng == nil {
					continue
				}
				compared++
				if tr.ExitKind == ExitClose {
					closeExits++
					if tr.ExitTs != eng.Ts || !approx(tr.ExitRaw, eng.Price, 1e-9*math.Max(1, eng.Price)) {
						t.Errorf("%s %s entry %s: close exit %s at %v, engine EXIT %s at %v", name, v, ETClock(s.Ts), ETClock(tr.ExitTs), tr.ExitRaw, ETClock(eng.Ts), eng.Price)
					}
				}
				if tr.ExitTs > eng.Ts && tr.ExitKind != ExitFlatten {
					t.Errorf("%s %s entry %s: exit %s is later than the engine's EXIT %s", name, v, ETClock(s.Ts), ETClock(tr.ExitTs), ETClock(eng.Ts))
				}
			}
		}
	}
	t.Logf("compared %d trade exits against the engine, %d of them close-mode exits", compared, closeExits)
	if compared < 2 || closeExits < 1 {
		t.Errorf("compared %d exits (%d close exits): the fixtures no longer exercise the comparison", compared, closeExits)
	}
}

// Running the same day twice gives the same trades.
func TestSimulationIsDeterministic(t *testing.T) {
	pd := beatDay(t)
	r := rulesNamed(t, "V1")
	a, b := SimulateDay(pd, r), SimulateDay(pd, r)
	if len(a.Trades) != len(b.Trades) || a.Net != b.Net || a.Trades[0].Net != b.Trades[0].Net {
		t.Errorf("two runs differ: %+v vs %+v", a.Trades, b.Trades)
	}
}

func TestVol5(t *testing.T) {
	bars := dropBars(t, day(t, 10, 100), "2026-10-05 08:57", "2026-10-05 08:58")
	i := barIndex(bars, etMs(t, "2026-10-05 09:00"))
	// slots 08:55..08:59 before 09:00: 08:57 and 08:58 have no bar
	if got := vol5(bars, i, 0); got != 300 {
		t.Errorf("vol5 = %d, want 300 (three of five slots printed)", got)
	}
	if got := vol5(bars, 0, 0); got != 0 {
		t.Errorf("vol5 at the first bar = %d, want 0", got)
	}
	// admission inside the window: only the bars at or after it count (08:58 has no bar, 08:59 does)
	if got := vol5(bars, i, etMs(t, "2026-10-05 08:59")); got != 100 {
		t.Errorf("vol5 admitted 08:59 = %d, want 100 (the 08:59 bar alone)", got)
	}
	if got := vol5(bars, i, etMs(t, "2026-10-05 08:56")); got != 200 {
		t.Errorf("vol5 admitted 08:56 = %d, want 200 (08:56 and 08:59)", got)
	}
	if got := vol5(bars, i, etMs(t, "2026-10-05 08:30")); got != 300 {
		t.Errorf("vol5 admitted 08:30 = %d, want 300 (admission before the window changes nothing)", got)
	}
	if got := vol5(bars, i, etMs(t, "2026-10-05 09:00")); got != 0 {
		t.Errorf("vol5 admitted on the entry bar = %d, want 0", got)
	}

	// the F4 case: bars 06:57..07:02 at 50k, entry bar 07:02
	var five []core.Bar
	for _, s := range []string{"06:57", "06:58", "06:59", "07:00", "07:01", "07:02"} {
		five = append(five, core.Bar{T: etMs(t, "2026-10-05 "+s), O: 1, H: 1, L: 1, C: 1, V: 50_000})
	}
	if got := vol5(five, 5, etMs(t, "2026-10-05 07:01")); got != 50_000 {
		t.Errorf("admitted 07:01: vol5 = %d, want 50000", got)
	}
	if got := vol5(five, 5, etMs(t, "2026-10-05 06:00")); got != 250_000 {
		t.Errorf("admitted 06:00: vol5 = %d, want 250000", got)
	}
}

// ─── The daily-loss halt after the day's last signal ─────────────────────

// The halt is checked at every minute boundary, but only the minutes up to the
// next signal group were scanned: a halt that trips after the day's LAST signal
// was never recorded, so "days the daily-loss halt tripped" undercounted. It
// changes no trade (no entry follows), only the count.
func TestHaltAfterTheLastSignalIsRecorded(t *testing.T) {
	r := rulesNamed(t, "V1") // $200
	var sigs []Signal
	for i, s := range []string{"AAA", "BBB", "CCC"} {
		c := flatDay(t, s)
		// 09:00: in at 10.00 (100 shares, stop 9.50); 09:10: every name gaps to 8.00
		for k := barIndex(c.Bars, etMs(t, "2026-10-05 09:10")); k < len(c.Bars); k++ {
			c.Bars[k].O, c.Bars[k].H, c.Bars[k].L, c.Bars[k].C = 8, 8, 8, 8
		}
		sigs = append(sigs, sigAt(t, c, "2026-10-05 09:00", 10, 9.5, i+1))
	}
	dr := SimulateDay(dayOf(sigs...), r)
	if len(dr.Trades) != 3 {
		t.Fatalf("trades %v skips %v, want all three", symsOf(dr), dr.Skips)
	}
	if !(dr.Net <= -r.MaxDailyLossUSD) {
		t.Fatalf("setup: the day's net %.2f must be past -%.0f", dr.Net, r.MaxDailyLossUSD)
	}
	// each exits in the 09:10 bar at the open 8.00 (jumping the 9.90 trail), realized from 09:11:
	// 3 x (7.96 - 10.025) x 100 = -619.5; before that the marks (the 09:09 close, 10.00) are only -7.50
	if !dr.Halted || dr.HaltTs != etMs(t, "2026-10-05 09:11") {
		t.Errorf("halted=%v at %s (day net %.2f), want the halt at 09:11 even though no signal follows", dr.Halted, ETClock(dr.HaltTs), dr.Net)
	}

	// a small loss after the last signal is not a halt
	c := flatDay(t, "AAA")
	for k := barIndex(c.Bars, etMs(t, "2026-10-05 09:10")); k < len(c.Bars); k++ {
		c.Bars[k].O, c.Bars[k].H, c.Bars[k].L, c.Bars[k].C = 9.6, 9.6, 9.6, 9.6
	}
	dr = SimulateDay(dayOf(sigAt(t, c, "2026-10-05 09:00", 10, 9.5, 1)), r)
	if len(dr.Trades) != 1 || dr.Halted || dr.HaltTs != 0 {
		t.Errorf("a $%.2f loss: halted=%v, want no halt", dr.Net, dr.Halted)
	}
}

// ─── Liquidity only counts the minutes since admission ───────────────────

// The live trader's vol5 sums volume it has OBSERVED since the symbol was
// admitted; a minute before admission counts 0 ("a symbol only just admitted
// sizes conservatively"). The replay summed five full bars wherever admission
// was, so an entry within four minutes of admission was sized on liquidity the
// live trader did not have.
func TestSizingCountsOnlyMinutesSinceAdmission(t *testing.T) {
	r := rulesNamed(t, "V1")
	// 06:57 .. 07:02 at 50k a bar, flat 0.50; the signal is on the 07:02 bar (trigger 0.50, stop 0.48:
	// risk caps at 2500 shares, position at 4000)
	shares := func(admit string) int64 {
		c := &Candidate{Sym: "ADM", Date: "2026-10-05", Adv10: 1e6, Bars: flatBars(t, "2026-10-05 06:57", 6, 0.50, 50_000), AdmitTs: etMs(t, admit)}
		dr := SimulateDay(dayOf(sigAt(t, c, "2026-10-05 07:02", 0.50, 0.48, 1)), r)
		return tradeOf(t, dr, "ADM").Shares
	}
	// admitted long before: all five prior bars are observed: 2% of 250,000 = 5000, so risk binds
	if got := shares("2026-10-05 06:00"); got != 2500 {
		t.Errorf("admitted 06:00: %d shares, want 2500 (risk-bound)", got)
	}
	// admitted at 07:01: only the 07:01 bar was observed: 2% of 50,000 = 1000
	if got := shares("2026-10-05 07:01"); got != 1000 {
		t.Errorf("admitted 07:01: %d shares, want 1000 = 2%% of the one bar since admission", got)
	}
	// admitted at 06:59: 06:59, 07:00, 07:01 observed: 2% of 150,000 = 3000, risk binds again
	if got := shares("2026-10-05 06:59"); got != 2500 {
		t.Errorf("admitted 06:59: %d shares, want 2500", got)
	}
	// admitted at 06:58: five slots 06:57..07:01 but 06:57 is before admission: 4 bars = 200,000 -> 4000 > 2500
	if got := shares("2026-10-05 06:58"); got != 2500 {
		t.Errorf("admitted 06:58: %d shares, want 2500", got)
	}
}
