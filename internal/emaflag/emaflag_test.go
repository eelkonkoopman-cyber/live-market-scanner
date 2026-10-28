package emaflag

import (
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// Focused unit tests for the two things the golden vectors can only exercise
// indirectly: the EMA's seeding (the whole engine reads this one line) and the
// ET day boundary (state resets per ET day; the EMA does not). The golden
// vectors (golden_test.go) carry the rule-by-rule parity with the TS engine.

func TestEmaSeries_SMASeedThenRecursion(t *testing.T) {
	// period 3: the first value is the SMA of the first three closes, at index 2;
	// then k = 2/(3+1) = 0.5.
	got := emaSeries([]float64{1, 2, 3, 4, 5, 6}, 3)
	want := []float64{math.NaN(), math.NaN(), 2, 3, 4, 5}
	for i := range want {
		if math.IsNaN(want[i]) {
			if !math.IsNaN(got[i]) {
				t.Errorf("ema[%d] = %v, want NaN (warm-up)", i, got[i])
			}
			continue
		}
		if got[i] != want[i] {
			t.Errorf("ema[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestEmaSeries_ExactArithmetic(t *testing.T) {
	// The recursion is close*k + prev*(1-k) with k = 2/(period+1), evaluated in
	// exactly that order (the chart's emaArr): pin the bits for period 9.
	closes := []float64{0.4, 0.41, 0.43, 0.42, 0.45, 0.44, 0.46, 0.47, 0.5, 0.52, 0.51}
	got := emaSeries(closes, 9)
	sum := 0.0
	for _, c := range closes[:9] {
		sum += c
	}
	seed := sum / 9
	k := 2.0 / 10.0
	e9 := closes[9]*k + seed*(1-k)
	e10 := closes[10]*k + e9*(1-k)
	if got[8] != seed || got[9] != e9 || got[10] != e10 {
		t.Errorf("ema = [%v %v %v], want [%v %v %v]", got[8], got[9], got[10], seed, e9, e10)
	}
	for i := 0; i < 8; i++ {
		if !math.IsNaN(got[i]) {
			t.Errorf("ema[%d] should still be warming up, got %v", i, got[i])
		}
	}
}

func TestEmaSeries_ShortInputAndPeriodOne(t *testing.T) {
	for i, v := range emaSeries([]float64{1, 2}, 9) {
		if !math.IsNaN(v) {
			t.Errorf("fewer closes than the period: ema[%d] = %v, want NaN", i, v)
		}
	}
	// period 1: k = 1, the EMA IS the close.
	c := []float64{3, 5, 4}
	for i, v := range emaSeries(c, 1) {
		if v != c[i] {
			t.Errorf("period 1: ema[%d] = %v, want %v", i, v, c[i])
		}
	}
}

func TestEvaluate_EmptyAndWarmup(t *testing.T) {
	r := Evaluate(nil, 60_000, 0, DefaultParams())
	if r.State != StateIdle || len(r.Events) != 0 || r.StateSinceMs != 0 || r.FlagBars != 0 {
		t.Errorf("no bars: %+v", r)
	}
	for name, v := range map[string]float64{"Ema": r.Ema, "Trigger": r.Trigger, "Stop": r.Stop, "FlagHigh": r.FlagHigh, "FlagLow": r.FlagLow, "EntryPrice": r.EntryPrice, "EmaDistPct": r.EmaDistPct} {
		if !math.IsNaN(v) {
			t.Errorf("no bars: %s = %v, want NaN", name, v)
		}
	}

	// Five bars: the 9 EMA has not seeded, so the engine is idle with a NaN EMA
	// and distance (never a divide-by-zero or a 0 that reads as "price at the EMA").
	bars := make([]core.Bar, 5)
	for i := range bars {
		bars[i] = core.Bar{T: int64(1_790_000_000_000 + i*60_000), O: 1, H: 1, L: 1, C: 1}
	}
	r = Evaluate(bars, 60_000, bars[4].T+10*60_000, DefaultParams())
	if r.State != StateIdle || !math.IsNaN(r.Ema) || !math.IsNaN(r.EmaDistPct) {
		t.Errorf("warm-up: %+v", r)
	}
}

func TestEvaluate_BadEmaPeriodDoesNotPanic(t *testing.T) {
	p := DefaultParams()
	p.EmaPeriod = 0
	bars := []core.Bar{{T: 1_790_000_000_000, O: 1, H: 1, L: 1, C: 1}}
	_ = Evaluate(bars, 60_000, bars[0].T+120_000, p)
}

func TestStateValuesAndNames(t *testing.T) {
	// The numeric values are the e9State wire contract (the TS FlagState order).
	want := map[State]string{StateIdle: "idle", StateImpulse: "impulse", StateFlat: "flat", StateReady: "ready", StateLong: "long"}
	for s, name := range want {
		if s.String() != name {
			t.Errorf("State(%d).String() = %q, want %q", int(s), s.String(), name)
		}
	}
	if StateIdle != 0 || StateImpulse != 1 || StateFlat != 2 || StateReady != 3 || StateLong != 4 {
		t.Error("State values are the e9State wire contract: 0 idle, 1 impulse, 2 flat, 3 ready, 4 long")
	}
}

// ─── The ET day boundary ─────────────────────────────────────────────────

func etMs(t *testing.T, y int, m time.Month, d, hh, mm int) int64 {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return time.Date(y, m, d, hh, mm, 0, 0, loc).UnixMilli()
}

// run builds `n` flat bars at 1.00 starting at `start`, one per minute.
func flatRun(start int64, n int) []core.Bar {
	bars := make([]core.Bar, n)
	for i := range bars {
		bars[i] = core.Bar{T: start + int64(i)*60_000, O: 1, H: 1, L: 1, C: 1, V: 1000}
	}
	return bars
}

// impulseBar is a thrust 10% over a flat 1.00 base (high 1.12, close 1.10).
func impulseBar(ts int64) core.Bar {
	return core.Bar{T: ts, O: 1, H: 1.12, L: 1, C: 1.10, V: 50_000}
}

// holdBar is a bar parked under the impulse's top: the sideways flag.
func holdBar(ts int64) core.Bar {
	return core.Bar{T: ts, O: 1.10, H: 1.105, L: 1.095, C: 1.10, V: 5000}
}

// A flag that forms across UTC midnight must still form: the day key is the ET
// calendar date, not the UTC date. 20:00 EDT is 00:00 UTC (19:00 EST in
// winter), so an engine that grouped by UTC day would reset at the 20:00 bar
// and never see the third flat bar. Both offsets are covered.
func TestEvaluate_DayKeyIsEasternNotUTC(t *testing.T) {
	cases := []struct {
		name   string
		y      int
		m      time.Month
		d      int
		hh, mm int // the clock time of the LAST flat bar, which sits on UTC midnight
	}{
		{"EDT (UTC-4): UTC midnight is 20:00 ET", 2026, time.October, 5, 20, 0},
		{"EST (UTC-5): UTC midnight is 19:00 ET", 2026, time.December, 1, 19, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lastFlat := etMs(t, c.y, c.m, c.d, c.hh, c.mm)
			if utcMidnight := time.UnixMilli(lastFlat).UTC(); utcMidnight.Hour() != 0 || utcMidnight.Minute() != 0 {
				t.Fatalf("test setup: %s is not UTC midnight", utcMidnight)
			}
			// 15 flat bars, the impulse (the top bar), then two parked bars. The flag window may
			// start ON the top bar (MinFlatBars 3 = top + 2), so FLAT opens on the second parked
			// bar, which sits on UTC midnight.
			start := lastFlat - int64(15+2)*60_000
			bars := flatRun(start, 15)
			bars = append(bars, impulseBar(start+15*60_000))
			for k := 1; k <= 2; k++ {
				bars = append(bars, holdBar(start+int64(15+k)*60_000))
			}
			if bars[len(bars)-1].T != lastFlat {
				t.Fatalf("test setup: last bar %d, want %d", bars[len(bars)-1].T, lastFlat)
			}
			r := Evaluate(bars, 60_000, lastFlat+10*60_000, DefaultParams())
			var flat *Event
			for i := range r.Events {
				if r.Events[i].Kind == KindFlat {
					flat = &r.Events[i]
				}
			}
			if flat == nil || flat.Ts != lastFlat {
				t.Fatalf("FLAT should open on the UTC-midnight bar %d (same ET day), events %+v", lastFlat, r.Events)
			}
			if r.State != StateFlat && r.State != StateReady {
				t.Errorf("state = %s, want flat or ready", r.State)
			}
			if r.StateSinceMs != lastFlat {
				t.Errorf("StateSinceMs = %d, want the FLAT bar %d", r.StateSinceMs, lastFlat)
			}
			if r.FlagBars != 3 {
				t.Errorf("FlagBars = %d, want 3 (the top bar plus two parked bars)", r.FlagBars)
			}
		})
	}
}

// The state machine runs per ET day; the EMA runs on across it. An impulse
// live at the end of one ET day must not turn the next morning's quiet bars
// into a flag, and the next day's EMA must not be re-seeded (it would still be
// warming up four bars in).
func TestEvaluate_StateResetsAtEtMidnightButEmaContinues(t *testing.T) {
	d1 := etMs(t, 2026, time.October, 5, 18, 0) // 18:00 ET
	bars := flatRun(d1, 15)
	bars = append(bars, impulseBar(d1+15*60_000))

	day1 := Evaluate(bars, 60_000, bars[len(bars)-1].T+10*60_000, DefaultParams())
	if day1.State != StateImpulse {
		t.Fatalf("day 1 alone: state = %s, want impulse (events %+v)", day1.State, day1.Events)
	}
	if day1.StateSinceMs != bars[len(bars)-1].T {
		t.Errorf("impulse StateSinceMs = %d, want the BREAKOUT bar %d", day1.StateSinceMs, bars[len(bars)-1].T)
	}

	// Next ET morning, 04:00: four bars parked right under the old top. With the
	// impulse leaked across midnight these would be a FLAT (3 bars, tight span);
	// reset, the day has fewer than 5 bars so nothing can arm.
	d2 := etMs(t, 2026, time.October, 6, 4, 0)
	for k := 0; k < 4; k++ {
		bars = append(bars, holdBar(d2+int64(k)*60_000))
	}
	r := Evaluate(bars, 60_000, bars[len(bars)-1].T+10*60_000, DefaultParams())
	if r.State != StateIdle || r.StateSinceMs != 0 {
		t.Fatalf("day 2: state = %s since %d, want idle (impulse leaked across ET midnight)", r.State, r.StateSinceMs)
	}
	for _, e := range r.Events {
		if e.Ts >= d2 {
			t.Errorf("day 2 emitted %+v, want nothing", e)
		}
	}
	// ... but the events list still carries day 1's BREAKOUT (all days, oldest first).
	if len(r.Events) != 1 || r.Events[0].Kind != KindImpulse {
		t.Errorf("events = %+v, want day 1's single BREAKOUT", r.Events)
	}

	// The EMA at the last bar is the continuous series over BOTH days.
	closes := make([]float64, len(bars))
	for i := range bars {
		closes[i] = bars[i].C
	}
	want := emaSeries(closes, 9)[len(bars)-1]
	if math.IsNaN(r.Ema) || r.Ema != want {
		t.Errorf("Ema = %v, want the continuous %v (not re-seeded at the day boundary)", r.Ema, want)
	}
}

// Forming vs closed on the last bar: a flag break by the high of the bar still
// printing is an entry already (the same events once it closes - the golden
// vectors pin that across the fixtures), and the stop uses the EMA of the last
// CLOSED bar.
func TestEvaluate_FormingBarBreaksByHigh(t *testing.T) {
	start := etMs(t, 2026, time.October, 5, 9, 0)
	bars := flatRun(start, 15)
	bars = append(bars, impulseBar(start+15*60_000))
	// Three tight bars at the ready distance... the EMA needs to catch up, so
	// let the flag sit long enough: 12 more bars at 1.10.
	for k := 1; k <= 12; k++ {
		bars = append(bars, holdBar(start+int64(15+k)*60_000))
	}
	// The forming bar prints a high through the trigger 1.12 * 1.003.
	last := core.Bar{T: start + 28*60_000, O: 1.10, H: 1.14, L: 1.10, C: 1.13, V: 1000}
	bars = append(bars, last)

	forming := Evaluate(bars, 60_000, last.T+30_000, DefaultParams())
	closed := Evaluate(bars, 60_000, last.T+10*60_000, DefaultParams())
	if forming.State != StateLong || closed.State != StateLong {
		t.Fatalf("states forming %s closed %s, want long on both (events %+v)", forming.State, closed.State, closed.Events)
	}
	if forming.StateSinceMs != last.T || closed.StateSinceMs != last.T {
		t.Errorf("StateSinceMs forming %d closed %d, want the break bar %d", forming.StateSinceMs, closed.StateSinceMs, last.T)
	}
	wantEntry := 1.12 * (1 + DefaultParams().BreakBufferPct/100)
	if !near(forming.EntryPrice, wantEntry) || !near(closed.EntryPrice, wantEntry) {
		t.Errorf("EntryPrice forming %v closed %v, want the trigger %v", forming.EntryPrice, closed.EntryPrice, wantEntry)
	}
	if len(forming.Events) == 0 || forming.Events[len(forming.Events)-1].Kind != KindEntry {
		t.Errorf("forming run's last event should be the entry, got %+v", forming.Events)
	}
	// The closed run reproduces every forming event, same ts/kind/label/price.
	for i, e := range forming.Events {
		if i >= len(closed.Events) || closed.Events[i] != e {
			t.Errorf("event %d: forming %+v, closed run has %+v", i, e, closed.Events)
			break
		}
	}
}

func TestDefaultParams_FieldForField(t *testing.T) {
	// DEFAULT_FLAG_PARAMS in emaFlag.ts; the golden "defaults" cases carry the TS
	// values and TestGoldenFullCases asserts they equal this, so this literal is
	// the readable second copy.
	want := Params{
		EmaPeriod: 9, AtrPeriod: 14, MinExtPct: 5, BreakoutLookback: 30, ImpulseBaseBars: 10, MinFlatBars: 3,
		MaxFlatOfMovePct: 35, MaxFlatPct: 10, MaxRetracePct: 50, EmaBreakPct: 1, ReadyDistPct: 4,
		EntryNeedsReady: true, BreakBufferPct: 0.3, StopBufferPct: 1.0, TrailEma: true, StopByClose: true, MaxAdds: 2,
		MaxFlagBars: 30, Slam: true, SlamBelowPct: 3, SlamOpenTolPct: 0.5, SlamBodyAtr: 1, SlamMinBodyPct: 2,
		SlamCloseInRange: 0.35, SlamVolX: 1.3,
	}
	if DefaultParams() != want {
		t.Errorf("DefaultParams() = %+v, want %+v", DefaultParams(), want)
	}
}

// ─── Flag expiry (MaxFlagBars) ───────────────────────────────────────────
//
// The Go side of F15 / F16 in emaFlag.test.ts: the golden vectors pin the rule
// across the fixtures (the maxFlagBars=6 set exercises it on a dozen of them),
// these pin its edges by name - the 30th bar from the top is still a flag, the
// 31st expires it, an expiry is silent and final until a higher high, the
// expiring bar prints no 9 EMA NEAR, a break on the expiring bar is a break,
// and 0 switches it off.

const expiryAnchor = int64(1758700800000) // 04:00 ET, 2025-09-24; bar i is i minutes later

func xbar(i int, o, h, l, c float64, v int64) core.Bar {
	return core.Bar{T: expiryAnchor + int64(i)*60_000, O: o, H: h, L: l, C: c, V: v}
}

// expiryRange: 20 quiet bars at 1.00, the thrust (bar 20: top 1.20, base low
// 0.998), then sideways drift bars 21..to: closes cycle 1.180/1.184/1.188/1.192,
// highs 0.004 over the close (never above the 1.20 top), lows 0.004 under.
func expiryRange(to int) []core.Bar {
	var bars []core.Bar
	for i := 0; i < 20; i++ {
		bars = append(bars, xbar(i, 1.0, 1.002, 0.998, 1.0, 1000))
	}
	bars = append(bars, xbar(20, 1.0, 1.2, 0.999, 1.18, 9000))
	for i := 21; i <= to; i++ {
		c := 1.18 + float64(i%4)*0.004
		bars = append(bars, xbar(i, c-0.001, c+0.004, c-0.004, c, 1200))
	}
	return bars
}

func evalClosed(bars []core.Bar, p Params) Result {
	return Evaluate(bars, 60_000, bars[len(bars)-1].T+10*60_000, p)
}

// stamps is "<bar number> <kind>" per event, like the TS test's.
func stamps(r Result) []string {
	out := make([]string, len(r.Events))
	for i, e := range r.Events {
		out[i] = strconv.FormatInt((e.Ts-expiryAnchor)/60_000, 10) + " " + e.Kind
	}
	return out
}

func wantStamps(t *testing.T, what string, r Result, want ...string) {
	t.Helper()
	got := stamps(r)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s: events %v, want %v", what, got, want)
	}
}

func TestFlagExpiry_ThirtiethBarIsAFlagThirtyFirstExpiresIt(t *testing.T) {
	p := DefaultParams()
	// bar 20 (the top) is bar 1, so bar 49 is the 30th: still a flag, READY since the EMA caught up on 26.
	at49 := evalClosed(expiryRange(49), p)
	if at49.State != StateReady || at49.FlagBars != 30 {
		t.Fatalf("bar 49: state %s flagBars %d, want ready / 30", at49.State, at49.FlagBars)
	}
	wantStamps(t, "bar 49", at49, "20 impulse", "22 flat", "26 emaNear")

	// bar 50 is the 31st: the flag ends, silently, and the state is impulse dated to that bar.
	bars50 := expiryRange(50)
	at50 := evalClosed(bars50, p)
	if at50.State != StateImpulse || at50.FlagBars != 0 {
		t.Fatalf("bar 50: state %s flagBars %d, want impulse / 0", at50.State, at50.FlagBars)
	}
	wantStamps(t, "bar 50", at50, "20 impulse", "22 flat", "26 emaNear")
	if at50.StateSinceMs != bars50[50].T {
		t.Errorf("StateSinceMs = %d, want the expiring bar %d", at50.StateSinceMs, bars50[50].T)
	}
	for name, v := range map[string]float64{"Trigger": at50.Trigger, "FlagHigh": at50.FlagHigh, "FlagLow": at50.FlagLow, "Stop": at50.Stop} {
		if !math.IsNaN(v) {
			t.Errorf("expired flag: %s = %v, want NaN", name, v)
		}
	}
}

func TestFlagExpiry_FinalUntilANewHighAndZeroSwitchesItOff(t *testing.T) {
	p := DefaultParams()
	bars := expiryRange(60) // 11 bars past the expiry
	r := evalClosed(bars, p)
	wantStamps(t, "through bar 60", r, "20 impulse", "22 flat", "26 emaNear") // no FLAT re-emitted
	if r.State != StateImpulse {
		t.Errorf("state = %s, want impulse", r.State)
	}
	// the forming clock on the last bar agrees: the expiry is a closed-bar rule
	if f := Evaluate(bars, 60_000, bars[60].T+30_000, p); f.State != StateImpulse {
		t.Errorf("forming: state = %s, want impulse", f.State)
	}
	// 0 = no limit: ONE flag, 41 bars old, READY (what PTC showed for six hours)
	p0 := p
	p0.MaxFlagBars = 0
	off := evalClosed(bars, p0)
	if off.State != StateReady || off.FlagBars != 41 {
		t.Errorf("no limit: state %s flagBars %d, want ready / 41", off.State, off.FlagBars)
	}

	// A NEW high (bar 61 prints 1.215, through the old trigger 1.2036 - but there is no flag, so it is
	// not an entry) is a new top: three bars later a fresh flag opens under it, READY at once.
	bars = append(bars, xbar(61, 1.2, 1.215, 1.19, 1.205, 5000), xbar(62, 1.205, 1.212, 1.2, 1.206, 1500), xbar(63, 1.206, 1.213, 1.2, 1.207, 1500))
	r = evalClosed(bars, p)
	wantStamps(t, "new top", r, "20 impulse", "22 flat", "26 emaNear", "63 flat")
	if r.State != StateReady || r.FlagBars != 3 || !near(r.Trigger, 1.215*1.003) {
		t.Errorf("new flag: state %s flagBars %d trigger %v, want ready / 3 / %v", r.State, r.FlagBars, r.Trigger, 1.215*1.003)
	}
	bars = append(bars, xbar(64, 1.207, 1.22, 1.205, 1.219, 4000))
	r = evalClosed(bars, p)
	wantStamps(t, "break of the new flag", r, "20 impulse", "22 flat", "26 emaNear", "63 flat", "64 entry")
}

func TestFlagExpiry_BreakOnTheExpiringBarIsStillABreak(t *testing.T) {
	bars := append(expiryRange(49), xbar(50, 1.19, 1.21, 1.19, 1.205, 4000))
	r := evalClosed(bars, DefaultParams())
	wantStamps(t, "break on bar 50", r, "20 impulse", "22 flat", "26 emaNear", "50 entry")
	if r.State != StateLong {
		t.Errorf("state = %s, want long", r.State)
	}
}

func TestFlagExpiry_ExpiringBarPrintsNoEmaNear(t *testing.T) {
	// Without a limit the EMA catches up on bar 26 (flag bars 7). Limit 6 expires the flag on that very bar:
	// the expiry runs before the READY test, so no 9 EMA NEAR.
	bars := expiryRange(30)
	p := DefaultParams()
	p.MaxFlagBars = 0
	wantStamps(t, "no limit", evalClosed(bars, p), "20 impulse", "22 flat", "26 emaNear")
	p.MaxFlagBars = 6
	r := evalClosed(bars, p)
	wantStamps(t, "limit 6", r, "20 impulse", "22 flat")
	if r.State != StateImpulse {
		t.Errorf("limit 6: state = %s, want impulse", r.State)
	}
	p.MaxFlagBars = 7 // the 7th bar is still a flag, READY on it
	wantStamps(t, "limit 7", evalClosed(bars, p), "20 impulse", "22 flat", "26 emaNear")
}

func TestFlagExpiry_TopOlderThanTheLimitRefusesAFlag(t *testing.T) {
	// The F6 fade sequence: the flag opens on 22 and a 1.115 close on 24 fades it; a NEW flat
	// would open on 26, 7 bars from the top. Limit 6: it would be born stale, so no FLAT.
	var bars []core.Bar
	for i := 0; i < 20; i++ {
		bars = append(bars, xbar(i, 1.0, 1.002, 0.998, 1.0, 1000))
	}
	bars = append(bars,
		xbar(20, 1.0, 1.2, 0.999, 1.18, 9000),
		xbar(21, 1.179, 1.184, 1.176, 1.18, 1500),
		xbar(22, 1.183, 1.188, 1.18, 1.184, 1500),
		xbar(23, 1.187, 1.192, 1.184, 1.188, 1500),
		xbar(24, 1.14, 1.15, 1.105, 1.115, 2000),
		xbar(25, 1.115, 1.12, 1.11, 1.116, 1500),
		xbar(26, 1.116, 1.12, 1.11, 1.115, 1500),
	)
	p := DefaultParams()
	wantStamps(t, "default", evalClosed(bars, p), "20 impulse", "22 flat", "26 flat")
	p.MaxFlagBars = 7
	wantStamps(t, "limit 7", evalClosed(bars, p), "20 impulse", "22 flat", "26 flat")
	p.MaxFlagBars = 6
	r := evalClosed(bars, p)
	wantStamps(t, "limit 6", r, "20 impulse", "22 flat")
	if r.State != StateImpulse {
		t.Errorf("limit 6: state = %s, want impulse", r.State)
	}
}

// PTC at 13:25 ET: one flag from 06:34 to 13:24 (373 bars, READY, trigger 199.27) before the expiry.
func TestFlagExpiry_PTCNineHoursSidewaysIsARange(t *testing.T) {
	bars := loadFixtures(t)["ptc-2026-10-05-1325"]
	if len(bars) == 0 {
		t.Fatal("ptc-2026-10-05-1325 fixture missing")
	}
	p := DefaultParams()
	now := bars[len(bars)-1].T + 10*60_000

	r := Evaluate(bars, 60_000, now, p)
	if r.State != StateImpulse || r.FlagBars != 0 || !math.IsNaN(r.Trigger) {
		t.Fatalf("defaults: state %s flagBars %d trigger %v, want impulse / 0 / NaN", r.State, r.FlagBars, r.Trigger)
	}
	// the state dates from the bar that expired the flag: 31 bars from the 06:34 top, 07:16 ET (PTC prints no bar
	// in a minute without a trade, so 31 bars are 43 minutes)
	top := etMs(t, 2026, time.October, 5, 6, 34)
	ti := -1
	for i := range bars {
		if bars[i].T == top {
			ti = i
		}
	}
	if ti < 0 || ti+30 >= len(bars) {
		t.Fatalf("06:34 top bar not found (index %d)", ti)
	}
	if want := bars[ti+30].T; r.StateSinceMs != want || want != etMs(t, 2026, time.October, 5, 7, 16) {
		t.Errorf("StateSinceMs = %d, want the 31st bar from the top %d (07:16 ET)", r.StateSinceMs, bars[ti+30].T)
	}
	// the expiry is silent: the events are the no-limit run's
	p0 := p
	p0.MaxFlagBars = 0
	old := Evaluate(bars, 60_000, now, p0)
	if old.State != StateReady || old.FlagBars != 373 || !near(old.Trigger, 198.67*1.003) {
		t.Errorf("no limit: state %s flagBars %d trigger %v, want ready / 373 / %v", old.State, old.FlagBars, old.Trigger, 198.67*1.003)
	}
	if len(r.Events) != len(old.Events) {
		t.Fatalf("events: %d with the expiry, %d without - the expiry must be silent", len(r.Events), len(old.Events))
	}
	for i := range r.Events {
		if r.Events[i] != old.Events[i] {
			t.Errorf("event %d differs: %+v vs %+v", i, r.Events[i], old.Events[i])
		}
	}
	// ... and no flag of the day, at any closed bar, is older than 30 bars
	for n := 1; n <= len(bars); n += 7 {
		if s := Evaluate(bars[:n], 60_000, bars[n-1].T+10*60_000, p); (s.State == StateFlat || s.State == StateReady) && s.FlagBars > 30 {
			t.Fatalf("after %d bars: a %s flag of %d bars", n, s.State, s.FlagBars)
		}
	}
}

// ─── EmaClosed: the 9 EMA of the last CLOSED bar ─────────────────────────

// While the last bar is forming Ema includes its close and EmaClosed does not;
// once it has closed they are the same number. EmaClosedTs is the bar the number
// belongs to, so a consumer (the auto-trader's trailing stop) can act once per
// closed bar. Warm-up and an empty series read NaN / 0, never a 0 that looks like
// a price.
func TestEvaluate_EmaClosedIsTheEmaOfTheLastClosedBar(t *testing.T) {
	start := etMs(t, 2026, time.October, 5, 9, 0)
	bars := flatRun(start, 15)
	// a last bar that closes far from the average, so the two EMAs visibly differ
	bars = append(bars, core.Bar{T: start + 15*60_000, O: 1, H: 1.30, L: 1, C: 1.25, V: 1000})
	closes := make([]float64, len(bars))
	for i := range bars {
		closes[i] = bars[i].C
	}
	ema := emaSeries(closes, 9)
	last := len(bars) - 1

	forming := Evaluate(bars, 60_000, bars[last].T+30_000, DefaultParams())
	if forming.Ema != ema[last] {
		t.Fatalf("Ema = %v, want the EMA at the forming bar %v", forming.Ema, ema[last])
	}
	if forming.EmaClosed != ema[last-1] || forming.EmaClosedTs != bars[last-1].T {
		t.Errorf("forming: EmaClosed %v at %d, want %v at %d (the bar before)", forming.EmaClosed, forming.EmaClosedTs, ema[last-1], bars[last-1].T)
	}
	if forming.EmaClosed == forming.Ema {
		t.Error("the test bar must move the EMA, or it proves nothing")
	}

	closed := Evaluate(bars, 60_000, bars[last].T+60_000, DefaultParams())
	if closed.EmaClosed != ema[last] || closed.EmaClosedTs != bars[last].T || closed.EmaClosed != closed.Ema {
		t.Errorf("closed: EmaClosed %v at %d, want %v at %d (== Ema %v)", closed.EmaClosed, closed.EmaClosedTs, ema[last], bars[last].T, closed.Ema)
	}

	// no bar of the day has closed yet: nothing to trail from
	one := Evaluate(bars[:1], 60_000, bars[0].T+10_000, DefaultParams())
	if !math.IsNaN(one.EmaClosed) || one.EmaClosedTs != 0 {
		t.Errorf("one forming bar: EmaClosed %v at %d, want NaN / 0", one.EmaClosed, one.EmaClosedTs)
	}
	// warm-up: closed bars exist but the EMA has not seeded
	warm := Evaluate(bars[:5], 60_000, bars[4].T+60_000, DefaultParams())
	if !math.IsNaN(warm.EmaClosed) || warm.EmaClosedTs != 0 {
		t.Errorf("warm-up: EmaClosed %v at %d, want NaN / 0", warm.EmaClosed, warm.EmaClosedTs)
	}
	if e := Evaluate(nil, 60_000, 0, DefaultParams()); !math.IsNaN(e.EmaClosed) || e.EmaClosedTs != 0 {
		t.Errorf("no bars: EmaClosed %v at %d", e.EmaClosed, e.EmaClosedTs)
	}
}

// A position's engine stop trails EmaClosed * (1 - StopBufferPct/100): the two
// fields agree, which is what lets the trader's own trail (TrailStopPct below the
// same number) sit at or above the engine's whenever it is configured tighter.
func TestEvaluate_EngineTrailIsEmaClosedMinusTheStopBuffer(t *testing.T) {
	for _, fx := range []string{"beat-2026-10-05"} {
		bars := loadFixtures(t)[fx]
		checked := 0
		for end := 40; end <= len(bars); end++ {
			now := bars[end-1].T + 60_000 // the last bar has just closed
			r := Evaluate(bars[:end], 60_000, now, DefaultParams())
			if r.State != StateLong || math.IsNaN(r.EmaClosed) {
				continue
			}
			checked++
			if want := r.EmaClosed * (1 - DefaultParams().StopBufferPct/100); r.Stop < want-1e-12 {
				t.Errorf("%s at %d: engine stop %.6f is under EmaClosed*0.99 = %.6f", fx, bars[end-1].T, r.Stop, want)
			}
		}
		if checked == 0 {
			t.Errorf("%s: never long in the window, the test proved nothing", fx)
		}
	}
}
