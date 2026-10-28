package engine

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// chartEMA is a line-for-line port of the chart engine's emaArr
// (web/src/vendor/blackthorn-charts/indicators/registry.ts): SMA seed over the
// first `period` values, then k = 2/(period+1); NaN during warm-up.
func chartEMA(vals []float64, period int) []float64 {
	out := make([]float64, len(vals))
	k := 2.0 / float64(period+1)
	var prev, sum float64
	for i, v := range vals {
		out[i] = math.NaN()
		if i < period-1 {
			sum += v
			continue
		}
		if i == period-1 {
			sum += v
			prev = sum / float64(period)
		} else {
			prev = v*k + prev*(1-k)
		}
		out[i] = prev
	}
	return out
}

// candle1m prints one trade in minute m (counted from c0) closing at c.
func candle1m(ts *tickerState, m int, c float64) {
	sec := c0 + int64(m)*60 + 30
	ts.applyAgg(core.Agg{Sym: ts.sym, Open: c, High: c, Low: c, Close: c, Vol: 100,
		StartMs: sec * 1000, EndMs: (sec + 1) * 1000})
}

// closeMinute completes minute m via the 1 Hz clock (no next-minute print).
func closeMinute(ts *tickerState, m int) {
	sec := c0 + int64(m+1)*60 + 3
	ts.syncTo(sec)
	ts.tick1m(sec)
}

func TestEma9MatchesChartFormula(t *testing.T) {
	closes := make([]float64, 45)
	for i := range closes { // a wavy, trending series with no exact ties
		closes[i] = 10 + 0.05*float64(i) + 0.3*math.Sin(float64(i)*0.7)
	}
	ref := chartEMA(closes, 9)
	ts := newTS5()
	for i, c := range closes {
		candle1m(ts, i, c)
		closeMinute(ts, i)
		if ts.emaN != i+1 {
			t.Fatalf("candle %d: emaN %d", i, ts.emaN)
		}
		if i >= 8 && math.Abs(ts.ema9-ref[i]) > 1e-9 {
			t.Fatalf("candle %d: ema %v, chart %v", i, ts.ema9, ref[i])
		}
	}
}

// seq feeds closes as consecutive completed 1-minute candles from minute 0.
func seq(ts *tickerState, closes ...float64) {
	for i, c := range closes {
		candle1m(ts, i, c)
		closeMinute(ts, i)
	}
}

func rising(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = 10 + 0.02*float64(i)
	}
	return out
}

func TestEma9CrossUnderFiresOncePerLoss(t *testing.T) {
	ts := newTS5()
	closes := append(rising(35), 9.50) // candle 35 closes far under a rising EMA
	seq(ts, closes...)
	end := (c0 + 36*60) * 1000 // end of minute 35
	r := row5(ts)
	if core.IsNull(r.BrkEma9Age) || ts.emaXMs != end {
		t.Fatalf("losing the 9 EMA must fire at the candle's end: age %v xMs %d want %d", r.BrkEma9Age, ts.emaXMs, end)
	}
	if r.VsEma9 >= 0 || r.Ema9 <= 0 {
		t.Fatalf("price is under the EMA: vs %v ema %v", r.VsEma9, r.Ema9)
	}

	// Another close below: still the same loss, no re-fire.
	candle1m(ts, 36, 9.40)
	closeMinute(ts, 36)
	if ts.emaXMs != end {
		t.Fatalf("staying under must not re-fire: %d", ts.emaXMs)
	}
	// Back above, then under again: a NEW loss.
	candle1m(ts, 37, 11.00)
	closeMinute(ts, 37)
	candle1m(ts, 38, 9.30)
	closeMinute(ts, 38)
	if want := (c0 + 39*60) * 1000; ts.emaXMs != want {
		t.Fatalf("a reclaim then a new close under must re-fire: %d want %d", ts.emaXMs, want)
	}
}

func TestEma9NothingDuringWarmup(t *testing.T) {
	ts := newTS5()
	seq(ts, append(rising(20), 9.0)...) // a clear cross at candle 21: still warming up
	if ts.emaXMs != 0 {
		t.Fatal("no signal before the EMA has warmed up")
	}
	r := row5(ts)
	if !core.IsNull(r.Ema9) || !core.IsNull(r.VsEma9) || !core.IsNull(r.BrkEma9Age) {
		t.Fatalf("fields stay empty during warm-up: %v %v %v", r.Ema9, r.VsEma9, r.BrkEma9Age)
	}
}

func TestEma9CloseAtEmaIsNotUnder(t *testing.T) {
	ts := newTS5()
	flat := make([]float64, 36)
	for i := range flat {
		flat[i] = 10.0 // EMA is exactly 10.0
	}
	seq(ts, flat...)
	if ts.emaXMs != 0 || !ts.emaAbove {
		t.Fatalf("closing AT the EMA is not under it: xMs %d above %v", ts.emaXMs, ts.emaAbove)
	}
	candle1m(ts, 36, 9.9)
	closeMinute(ts, 36)
	if ts.emaXMs == 0 {
		t.Fatal("after closes AT the EMA, a close under it is a loss")
	}
}

func TestEma9ClockClosesAQuietCandle(t *testing.T) {
	ts := newTS5()
	seq(ts, rising(35)...)
	candle1m(ts, 35, 9.5) // its only print; nothing trades in minute 36
	sec := c0 + 36*60 + 2 // 2 s after the minute: inside the grace
	ts.syncTo(sec)
	ts.tick1m(sec)
	if ts.emaXMs != 0 {
		t.Fatal("must wait out the grace for late per-second aggregates")
	}
	ts.syncTo(sec + 1)
	ts.tick1m(sec + 1)
	if ts.emaXMs == 0 {
		t.Fatal("the clock must close the candle after the grace, with no next print")
	}
}

func TestEma9LateAndOutOfOrderPrints(t *testing.T) {
	ts := newTS5()
	seq(ts, rising(10)...)
	ema := ts.ema9
	candle1m(ts, 3, 50) // a print for a minute already folded in: ignored
	if ts.ema9 != ema || ts.m1Start != 0 {
		t.Fatalf("a late print must not reopen history: ema %v -> %v", ema, ts.ema9)
	}
	// Within one minute, an EARLIER second arriving later is not the close.
	sec := c0 + 10*60
	ts.applyAgg(core.Agg{Sym: "X", Close: 10.40, High: 10.4, Low: 10.4, StartMs: (sec + 40) * 1000, EndMs: (sec + 41) * 1000})
	ts.applyAgg(core.Agg{Sym: "X", Close: 99.0, High: 99, Low: 99, StartMs: (sec + 5) * 1000, EndMs: (sec + 6) * 1000})
	if ts.m1Cl != 10.40 {
		t.Fatalf("the close is the latest print by time, got %v", ts.m1Cl)
	}
}

func TestEma9SkipsEmptyMinutes(t *testing.T) {
	ts := newTS5()
	closes := rising(12)
	for i, c := range closes { // trade only every 3rd minute
		candle1m(ts, i*3, c)
		closeMinute(ts, i*3)
	}
	ref := chartEMA(closes, 9)
	if ts.emaN != 12 || math.Abs(ts.ema9-ref[11]) > 1e-9 {
		t.Fatalf("EMA must run over traded candles only, like the chart: n %d ema %v ref %v", ts.emaN, ts.ema9, ref[11])
	}
}

func TestEma9DayResetKeepsEmaClearsSignal(t *testing.T) {
	ts := newTS5()
	seq(ts, append(rising(35), 9.5)...)
	ema, n := ts.ema9, ts.emaN
	ts.resetSession()
	if ts.emaXMs != 0 {
		t.Fatal("yesterday's 9 EMA loss is not today's signal")
	}
	if ts.ema9 != ema || ts.emaN != n {
		t.Fatal("the EMA itself must run on through the night, like the chart's")
	}
}

// Through the real engine: the LAST candle (the loss) gets no following print,
// so only the shard's 1 Hz clock (tick1m) can close it. Prints are placed in
// the recent PAST so the real wall clock is already beyond every candle's end.
func TestEngineEma9ClockClosesTheLossCandle(t *testing.T) {
	e := New(Options{TickHz: 50})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = e.Run(ctx) }()
	start := (time.Now().Unix()/60 - 60) * 60 // an hour ago, minute-aligned
	closes := append(rising(35), 9.50)
	for i, c := range closes {
		sec := start + int64(i)*60 + 30
		e.Apply(core.Agg{Sym: "EMX", Open: c, High: c, Low: c, Close: c, Vol: 100, StartMs: sec * 1000, EndMs: (sec + 1) * 1000})
	}
	waitForCond(t, e, "EMX", 5*time.Second, func(r core.Row) bool {
		return !core.IsNull(r.BrkEma9Age) && r.VsEma9 < 0
	})
}
