package backtest

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func gridRules(t testing.TB, id string) VwapRules {
	t.Helper()
	for _, r := range VwapGrid() {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no grid variant %q", id)
	return VwapRules{}
}

func gridRun(t testing.TB, bars []core.Bar, id string) ([]VwTrade, VwCounts) {
	t.Helper()
	return SimulateVwapRulesDay("TEST", vwDate, 10e6, bars, gridRules(t, id), DefaultVwapParams())
}

func one(t testing.TB, tr []VwTrade, cnt VwCounts) VwTrade {
	t.Helper()
	if len(tr) != 1 {
		t.Fatalf("trades %d counts %+v", len(tr), cnt)
	}
	return tr[0]
}

func TestGridRegistration(t *testing.T) {
	var ids []string
	for _, r := range VwapGrid() {
		ids = append(ids, r.ID)
	}
	want := "B0 X1 X2 X3 X4 X5 X6 S1 S2 S3 S4 F1 F2 F3 F4 F5 U1 U1i U2"
	if strings.Join(ids, " ") != want || GridVersion != "grid v1" {
		t.Fatalf("grid drifted: %v %s", ids, GridVersion)
	}
	for _, r := range VwapGrid() {
		if r.ID == "U1" || r.ID == "U2" {
			if r.Signal != VwClose {
				t.Errorf("%s must use the close signal", r.ID)
			}
		} else if r.Signal != VwIntrabar {
			t.Errorf("%s must use the intrabar signal", r.ID)
		}
	}
}

// B0 must reproduce the existing simulation exactly (exit reason "trail" is the
// split-out name of a raised stop being hit).
func TestGridB0MatchesExistingBaseline(t *testing.T) {
	poke := vs{100, 101, 100, 100, bigV}
	var eod []vs
	eod = append(eod, crossBar())
	n := 0
	for m := 9*60 + 42; m <= 15*60+59; m++ {
		p := 101 + 0.01*float64(n)
		eod = append(eod, vs{p, p + 0.05, p, p + 0.02, bigV})
		n++
	}
	fixtures := map[string][]core.Bar{
		"trail":   vwScen(t, 100, bigV, "09:41", crossBar(), vs{100.5, 101, 100.6, 100.9, bigV}, vs{100.9, 101.5, 100.8, 101.4, bigV}, vs{101.4, 101.4, 100.7, 100.8, bigV}, flat(100, bigV)),
		"gapstop": vwScen(t, 100, bigV, "09:41", crossBar(), vs{100.5, 101, 100.6, 100.9, bigV}, vs{100.9, 101.5, 100.8, 101.4, bigV}, vs{100.3, 100.4, 100.2, 100.3, bigV}, flat(100, bigV)),
		"eod":     vwScen(t, 100, bigV, "09:41", eod...),
		"gapskip": vwScen(t, 100, bigV, "09:41", crossBar(), vs{99.9, 100.5, 99.8, 100, bigV}, flat(100, bigV)),
		"size0":   vwScen(t, 100, bigV, "09:41", vs{100, 101, 40, 101, bigV}, vs{101, 102, 100, 101, bigV}, flat(101, bigV)),
		"bigpos":  vwScen(t, 200, bigV, "09:41", vs{200, 201, 200, 201, bigV}, vs{200.1, 201, 199, 200, bigV}, flat(200, bigV)),
		"rearm":   vwScen(t, 100, bigV, "09:41", poke, vs{100.2, 101, 99, 100, bigV}, poke, vs{100.1, 100.1, 99, 99.5, bigV}, flat(99, bigV)),
		"nolast":  vwScen(t, 100, bigV, "15:58", crossBar(), flat(101, bigV)),
	}
	b0 := gridRules(t, "B0")
	for name, bars := range fixtures {
		for _, sig := range []string{VwIntrabar, VwClose} {
			r := b0
			r.Signal = sig
			wantT, wantC := SimulateVwapCrossDay("TEST", vwDate, 10e6, bars, sig, DefaultVwapParams())
			gotT, gotC := SimulateVwapRulesDay("TEST", vwDate, 10e6, bars, r, DefaultVwapParams())
			if len(wantT) != len(gotT) {
				t.Fatalf("%s/%s: %d vs %d trades", name, sig, len(wantT), len(gotT))
			}
			for i := range wantT {
				w, g := wantT[i], gotT[i]
				if g.Reason == "trail" {
					g.Reason = "stop"
				}
				w.Variant, g.Variant = "", ""
				if w != g {
					t.Errorf("%s/%s trade %d:\nwant %+v\ngot  %+v", name, sig, i, w, g)
				}
			}
			if wantC.Fired != gotC.Fired || wantC.SkippedGap != gotC.SkippedGap || wantC.SkippedSize != gotC.SkippedSize ||
				wantC.NoEntryBar != gotC.NoEntryBar || wantC.Traded != gotC.Traded || wantC.IgnoredOpen != gotC.IgnoredOpen {
				t.Errorf("%s/%s counts %+v vs %+v", name, sig, wantC, gotC)
			}
		}
	}
}

// entry bar 100.5 (risk 0.5 against the cross low 100).
func exitScen(t testing.TB, specs ...vs) []core.Bar {
	return vwScen(t, 100, bigV, "09:41", append([]vs{crossBar()}, specs...)...)
}

func TestX1TwoCandleLowTrail(t *testing.T) {
	bars := exitScen(t,
		vs{100.5, 101, 100.2, 100.9, bigV},
		vs{100.9, 101.5, 100.8, 101.4, bigV},
		vs{101.4, 101.4, 100.5, 100.6, bigV},  // prev-low trail (100.8) is hit here; the 2-candle trail (100.2) is not
		vs{100.6, 100.6, 100.4, 100.45, bigV}, // 2-candle stop is now min(100.5, 100.8) = 100.5
		flat(100, bigV))
	b0, c0 := gridRun(t, bars, "B0")
	if x := one(t, b0, c0); !approx(x.Exit, 100.8, 1e-9) || x.BarsHeld != 3 {
		t.Fatalf("B0 %+v", x)
	}
	tr, cnt := gridRun(t, bars, "X1")
	x := one(t, tr, cnt)
	if !approx(x.Exit, 100.5, 1e-9) || x.BarsHeld != 4 || x.Reason != "trail" {
		t.Errorf("X1 %+v", x)
	}
}

func TestX2EmaTrail(t *testing.T) {
	// EMA after the cross bar 100.2; after the entry bar (close 101.5) 100.46
	bars := exitScen(t,
		vs{100.5, 101.6, 100.3, 101.5, bigV},
		vs{101, 101.2, 100.4, 100.5, bigV}, // low 100.4 <= 100.46 x 0.9995
		flat(100, bigV))
	tr, cnt := gridRun(t, bars, "X2")
	x := one(t, tr, cnt)
	want := (101.5*0.2 + 100.2*0.8) * (1 - 0.0005)
	if !approx(x.Exit, want, 1e-9) || x.Reason != "trail" || x.BarsHeld != 2 {
		t.Errorf("X2 exit %v want %v: %+v", x.Exit, want, x)
	}
}

func TestFixedTakeProfitsAndBothHitIsStop(t *testing.T) {
	// X3: +1% of 100.5 = 101.505; the stop stays at 100 (no trail), so the 100.2 low is survived
	bars := exitScen(t,
		vs{100.5, 101, 100.6, 100.9, bigV},
		vs{100.9, 101.6, 100.2, 101.4, bigV},
		flat(100, bigV))
	tr, cnt := gridRun(t, bars, "X3")
	x := one(t, tr, cnt)
	if x.Reason != "tp" || !approx(x.Exit, 100.5*1.01, 1e-9) || x.BarsHeld != 2 {
		t.Errorf("X3 %+v", x)
	}
	// both stop and TP inside one bar: the stop wins
	both := exitScen(t, vs{100.5, 101.6, 99.9, 101, bigV}, flat(100, bigV))
	tr, cnt = gridRun(t, both, "X3")
	if x = one(t, tr, cnt); x.Reason != "stop" || !approx(x.Exit, 100, 1e-9) {
		t.Errorf("both-hit %+v", x)
	}
	// X4 1.5R: 100.5 + 1.5 x 0.5 = 101.25 ; X5 2R = 101.5
	r := exitScen(t, vs{100.5, 101.3, 100.4, 101, bigV}, vs{101, 101.6, 100.9, 101.5, bigV}, flat(100, bigV))
	tr, cnt = gridRun(t, r, "X4")
	if x = one(t, tr, cnt); x.Reason != "tp" || !approx(x.Exit, 101.25, 1e-9) || !approx(x.R, 1.5, 1e-9) || x.BarsHeld != 1 {
		t.Errorf("X4 %+v", x)
	}
	tr, cnt = gridRun(t, r, "X5")
	if x = one(t, tr, cnt); x.Reason != "tp" || !approx(x.Exit, 101.5, 1e-9) || !approx(x.R, 2, 1e-9) || x.BarsHeld != 2 {
		t.Errorf("X5 %+v", x)
	}
}

func TestX6TrailsOnlyAfterOneR(t *testing.T) {
	bars := exitScen(t,
		vs{100.5, 100.9, 100.4, 100.8, bigV}, // high 100.9 < entry + 1R (101.0): fixed stop 100
		vs{100.8, 100.9, 100.2, 100.3, bigV}, // low 100.2: B0 (stop 100.4) would be out, X6 is not
		vs{100.3, 101.2, 100.3, 101.1, bigV}, // touches 101.0 -> trail to this low
		vs{101.1, 101.1, 100.2, 100.5, bigV}, // low 100.2 <= 100.3
		flat(100, bigV))
	b0, c0 := gridRun(t, bars, "B0")
	if x := one(t, b0, c0); !approx(x.Exit, 100.4, 1e-9) || x.BarsHeld != 2 {
		t.Fatalf("B0 %+v", x)
	}
	tr, cnt := gridRun(t, bars, "X6")
	x := one(t, tr, cnt)
	if !approx(x.Exit, 100.3, 1e-9) || x.BarsHeld != 4 || x.Reason != "trail" {
		t.Errorf("X6 %+v", x)
	}
}

func TestS1WideStop(t *testing.T) {
	bars := exitScen(t, vs{100.5, 101, 100.3, 100.9, bigV}, flat(100, bigV))
	s := VwapSeries(bars, vwDate, 9)
	crossIdx := 0
	for i, b := range bars {
		if ETMinute(b.T) == 9*60+41 {
			crossIdx = i
		}
	}
	tr, cnt := gridRun(t, bars, "S1")
	x := one(t, tr, cnt)
	want := math.Min(100, s.Vwap[crossIdx]*0.997)
	if !approx(x.Stop0, want, 1e-9) || x.Stop0 >= 100 || !approx(x.Risk, 100.5-want, 1e-9) {
		t.Errorf("S1 stop %v want %v", x.Stop0, want)
	}
	// S3 = S1 stop + EMA trail
	r3 := gridRules(t, "S3")
	if r3.Stop != StopWide || r3.Exit != ExitTrailEMA {
		t.Errorf("S3 %+v", r3)
	}
}

func TestS2ATRStopAndS4(t *testing.T) {
	// constant 1.0 true range -> ATR 1
	var cb []core.Bar
	for i := 0; i < 30; i++ {
		cb = append(cb, core.Bar{T: int64(i) * minuteMs, O: 10, H: 10.5, L: 9.5, C: 10, V: 1})
	}
	a := atrSeries(cb, 14)
	if !math.IsNaN(a[12]) || !approx(a[13], 1, 1e-12) || !approx(a[29], 1, 1e-12) {
		t.Errorf("atr %v %v %v", a[12], a[13], a[29])
	}
	// Wilder smoothing: one TR of 15 after a flat ATR of 1 -> (13*1 + 15)/14 = 2
	cb = append(cb, core.Bar{T: 30 * minuteMs, O: 10, H: 17, L: 2, C: 10, V: 1})
	if g := atrSeries(cb, 14)[30]; !approx(g, 2, 1e-12) {
		t.Errorf("wilder step %v", g)
	}
	// in the scenario the true ranges are 0 until the cross bar (TR 1): ATR = 1/14
	bars := exitScen(t, vs{100.5, 100.7, 100.45, 100.6, bigV}, flat(100, bigV))
	tr, cnt := gridRun(t, bars, "S2")
	x := one(t, tr, cnt)
	if want := 100.5 - 1.5/14; !approx(x.Stop0, want, 1e-9) {
		t.Errorf("S2 stop %v want %v", x.Stop0, want)
	}
	// S4: 1.5R TP above the ATR stop
	tr, cnt = gridRun(t, bars, "S4")
	x = one(t, tr, cnt)
	risk := 1.5 / 14 // entry - stop = 1.5 x ATR
	if x.Reason != "tp" || !approx(x.Exit, 100.5+1.5*risk, 1e-9) || !approx(x.R, 1.5, 1e-9) {
		t.Errorf("S4 %+v", x)
	}
}

func TestF1EntriesFrom1000Only(t *testing.T) {
	bars := vwScen(t, 100, bigV, "09:41", crossBar(), vs{100.5, 100.6, 99.5, 100, bigV}, flat(100, bigV))
	tr, cnt := gridRun(t, bars, "F1")
	if len(tr) != 0 || cnt.UniverseReject["f1_time"] != 1 || cnt.Fired != 0 {
		t.Fatalf("09:41: %d %+v", len(tr), cnt)
	}
	bars = vwScen(t, 100, bigV, "09:58", crossBar(), vs{100.5, 100.6, 99.5, 100, bigV}, flat(100, bigV))
	if tr, cnt = gridRun(t, bars, "F1"); len(tr) != 0 || cnt.UniverseReject["f1_time"] != 1 {
		t.Fatalf("09:58 (entry 09:59): %d %+v", len(tr), cnt)
	}
	bars = vwScen(t, 100, bigV, "09:59", crossBar(), vs{100.5, 100.6, 99.5, 100, bigV}, flat(100, bigV))
	tr, cnt = gridRun(t, bars, "F1")
	x := one(t, tr, cnt)
	if x.EntryMin != 10*60 {
		t.Errorf("entry %d", x.EntryMin)
	}
}

func TestF2VolumeSurge(t *testing.T) {
	// the 9 bars before have volume 100k: the cross bar needs >= 200k
	weak := vwScen(t, 100, bigV, "09:41", crossBar(), vs{100.5, 100.6, 99.5, 100, bigV}, flat(100, bigV))
	tr, cnt := gridRun(t, weak, "F2")
	if len(tr) != 0 || cnt.UniverseReject["f2_volume"] != 1 {
		t.Fatalf("weak: %d %+v", len(tr), cnt)
	}
	for _, c := range []struct {
		v    int64
		want int
	}{{199_999, 0}, {200_000, 1}, {250_000, 1}} {
		cb := vs{100, 101, 100, 101, c.v}
		bars := vwScen(t, 100, bigV, "09:41", cb, vs{100.5, 100.6, 99.5, 100, bigV}, flat(100, bigV))
		if tr, _ := gridRun(t, bars, "F2"); len(tr) != c.want {
			t.Errorf("vol %d: %d trades want %d", c.v, len(tr), c.want)
		}
	}
}

func TestF3VwapRising(t *testing.T) {
	// flat 100 then a cross: VWAP (100.06) > VWAP ten bars earlier (100) -> passes
	rising := vwScen(t, 100, bigV, "09:41", crossBar(), vs{100.5, 100.6, 99.5, 100, bigV}, flat(100, bigV))
	if tr, cnt := gridRun(t, rising, "F3"); len(tr) != 1 {
		t.Fatalf("rising: %d %+v", len(tr), cnt)
	}
	// five bars at 105 first, then 100s: VWAP falls from 105 to ~102 by the cross
	var specs []vs
	for i := 0; i < 5; i++ {
		specs = append(specs, flat(105, bigV))
	}
	for i := 0; i < 6; i++ {
		specs = append(specs, flat(100, bigV))
	}
	specs = append(specs, vs{100, 110, 100, 100, bigV}, vs{100.5, 100.6, 99.5, 100, bigV}, flat(100, bigV))
	falling := vwScen(t, 100, bigV, "09:30", specs...)
	if tr, cnt := gridRun(t, falling, "B0"); len(tr) != 1 || cnt.Fired != 1 {
		t.Fatalf("B0 on the falling-VWAP day: %d %+v", len(tr), cnt)
	}
	tr, cnt := gridRun(t, falling, "F3")
	if len(tr) != 0 || cnt.UniverseReject["f3_vwapRising"] != 1 {
		t.Fatalf("falling: %d %+v", len(tr), cnt)
	}
}

func TestF4FiveMinuteTrend(t *testing.T) {
	// helper arithmetic: 5-minute buckets are clock aligned, a candle counts only once complete
	var bars []core.Bar
	t0 := etMs(t, vwDate+" 08:50")
	for i := 0; i < 70; i++ {
		px := 100 + 0.1*float64(i)
		bars = append(bars, core.Bar{T: t0 + int64(i)*minuteMs, O: px, H: px, L: px, C: px, V: 1})
	}
	cs, ema := candles5(bars)
	if len(cs) != 14 || cs[0].close != 100.4 || !approx(cs[1].close, 100.9, 1e-9) {
		t.Fatalf("candles %d %+v", len(cs), cs[:2])
	}
	// rising closes: last completed candle at 09:41 (bucket 09:35-09:40) is above its EMA
	at := etMs(t, vwDate+" 09:41")
	if !trend5mOK(cs, ema, at) {
		t.Error("rising series should pass")
	}
	// the 09:40 candle ends at 09:45: not yet complete at 09:44, so the 09:35 one is used
	i := 0
	for ; i < len(cs); i++ {
		if cs[i].end > etMs(t, vwDate+" 09:44") {
			break
		}
	}
	if cs[i].end != etMs(t, vwDate+" 09:45") {
		t.Errorf("bucket end %d", cs[i].end)
	}
	// falling closes fail
	for k := range bars {
		bars[k].C = 100 - 0.1*float64(k)
	}
	cs, ema = candles5(bars)
	if trend5mOK(cs, ema, at) {
		t.Error("falling series should fail")
	}
	// warm-up: before 9 candles are complete there is no EMA -> fail
	if trend5mOK(cs, ema, etMs(t, vwDate+" 09:10")) {
		t.Error("no EMA yet should fail")
	}
	// integration: flat closes = close equals the EMA -> not "above" -> rejected
	flatDay := vwScen(t, 100, bigV, "09:41", crossBar(), vs{100.5, 100.6, 99.5, 100, bigV}, flat(100, bigV))
	if tr, cnt := gridRun(t, flatDay, "F4"); len(tr) != 0 || cnt.UniverseReject["f4_trend5m"] != 1 {
		t.Fatalf("flat: %d %+v", len(tr), cnt)
	}
	// premarket at 98, then 100: the 09:35 candle (close 100) sits above its EMA9 (<100)
	up := vwScen(t, 100, bigV, "09:41", crossBar(), vs{100.5, 100.6, 99.5, 100, bigV}, flat(100, bigV))
	for i := range up {
		if ETMinute(up[i].T) < 9*60+30 {
			up[i].O, up[i].H, up[i].L, up[i].C = 98, 98, 98, 98
		}
	}
	if tr, cnt := gridRun(t, up, "F4"); len(tr) != 1 {
		t.Fatalf("up: %d %+v", len(tr), cnt)
	}
}

func TestF5Composition(t *testing.T) {
	r := gridRules(t, "F5")
	if r.FromMin != 600 || r.MinVol1m != 2 || r.Stop != StopWide || r.Exit != ExitTrailEMA || r.VwapRising || r.Trend5m {
		t.Errorf("%+v", r)
	}
}

func limitScen(t testing.TB, specs ...vs) []core.Bar {
	return vwScen(t, 100, bigV, "09:41", append([]vs{crossBar()}, specs...)...)
}

func TestU1LimitFillWindowAndTakeProfit(t *testing.T) {
	// cross close 101 -> limit 101, stop 99.99. m+1 stays above the limit, m+2 touches it.
	bars := limitScen(t,
		vs{101.5, 102, 101.2, 101.8, bigV},
		vs{101.8, 101.9, 100.9, 101.0, bigV}, // fills at the limit 101 (opens above)
		vs{101, 102.1, 100.5, 101.9, bigV},   // high >= 101 x 1.01 = 102.01
		flat(100, bigV))
	tr, cnt := gridRun(t, bars, "U1")
	x := one(t, tr, cnt)
	if !approx(x.Entry, 101, 1e-9) || x.EntryMin != 9*60+43 || !approx(x.Stop0, 99.99, 1e-9) || x.Reason != "tp" || !approx(x.Exit, 102.01, 1e-9) || x.BarsHeld != 2 {
		t.Errorf("U1 %+v", x)
	}
	if cnt.Unfilled != 0 {
		t.Errorf("unfilled %d", cnt.Unfilled)
	}
}

func TestU1UnfilledOutsideWindow(t *testing.T) {
	up := vs{102, 103, 101.5, 102.5, bigV}
	bars := limitScen(t, up, up, up, vs{102, 102, 100.9, 101, bigV}, flat(100, bigV)) // the 4th bar would fill: too late
	tr, cnt := gridRun(t, bars, "U1")
	if len(tr) != 0 || cnt.Unfilled != 1 || cnt.Fired != 1 {
		t.Fatalf("%d %+v", len(tr), cnt)
	}
	// the 3rd bar is still inside the window
	bars = limitScen(t, up, up, vs{102, 102, 101, 101.5, bigV}, flat(100, bigV))
	tr, cnt = gridRun(t, bars, "U1")
	if x := one(t, tr, cnt); !approx(x.Entry, 101, 1e-9) || cnt.Unfilled != 0 {
		t.Errorf("%+v", x)
	}
}

func TestU1GapOpenBelowLimitFillsAtOpen(t *testing.T) {
	bars := limitScen(t, vs{100.5, 100.8, 100.2, 100.6, bigV}, vs{100.6, 101.6, 100.5, 101.5, bigV}, flat(100, bigV))
	tr, cnt := gridRun(t, bars, "U1")
	x := one(t, tr, cnt)
	// filled at the open 100.5, not the limit; risk 100.5 - 99.99; TP 1% above the fill
	if !approx(x.Entry, 100.5, 1e-9) || !approx(x.Risk, 0.51, 1e-9) || x.Reason != "tp" || !approx(x.Exit, 100.5*1.01, 1e-9) {
		t.Errorf("%+v", x)
	}
}

func TestU1StopRulesAndBothHit(t *testing.T) {
	// fill bar touches both the stop and the TP: stop first
	both := limitScen(t, vs{101, 102.5, 99.9, 101, bigV}, flat(100, bigV))
	tr, cnt := gridRun(t, both, "U1")
	if x := one(t, tr, cnt); x.Reason != "stop" || !approx(x.Exit, 99.99, 1e-9) {
		t.Errorf("both-hit %+v", x)
	}
	// the stop is a real stop: a gap through it fills at the open
	gap := limitScen(t, vs{101.2, 101.4, 100.9, 101.1, bigV}, vs{99.5, 99.8, 99.4, 99.6, bigV}, flat(100, bigV))
	tr, cnt = gridRun(t, gap, "U1")
	if x := one(t, tr, cnt); x.Reason != "stop" || !approx(x.Exit, 99.5, 1e-9) {
		t.Errorf("gap %+v", x)
	}
}

func TestU1EndOfDayFlat(t *testing.T) {
	specs := []vs{vs{101.1, 101.2, 100.9, 101.1, bigV}} // fills at 101
	for m := 9*60 + 43; m <= 15*60+59; m++ {
		specs = append(specs, vs{101.1, 101.2, 101.05, 101.1, bigV})
	}
	bars := limitScen(t, specs...)
	tr, cnt := gridRun(t, bars, "U1")
	x := one(t, tr, cnt)
	if x.Reason != "eod" || ETClock(x.ExitTs) != "15:59" || !approx(x.Exit, 101.1, 1e-9) {
		t.Errorf("%+v", x)
	}
}

func TestU1iIntrabarSignal(t *testing.T) {
	// a poke (low 99) that closes back at 100: limit 100, stop 98.99
	poke := vs{100, 101, 99, 100, bigV}
	bars := vwScen(t, 100, bigV, "09:41", poke, vs{100.5, 100.6, 100, 100.2, bigV}, vs{100.2, 101.1, 100.1, 101, bigV}, flat(100, bigV))
	tr, cnt := gridRun(t, bars, "U1i")
	x := one(t, tr, cnt)
	if !approx(x.Entry, 100, 1e-9) || !approx(x.Stop0, 98.99, 1e-9) || x.Reason != "tp" || !approx(x.Exit, 101, 1e-9) {
		t.Errorf("U1i %+v", x)
	}
}

func TestU2AddsF1AndF2(t *testing.T) {
	scen := func(start string, v int64) []core.Bar {
		return vwScen(t, 100, bigV, start, vs{100, 101, 100, 101, v}, vs{101.5, 102, 101, 101.8, bigV}, vs{101.8, 102.5, 101.4, 102.4, bigV}, flat(100, bigV))
	}
	if tr, cnt := gridRun(t, scen("09:41", 250_000), "U1"); len(tr) != 1 {
		t.Fatalf("U1 at 09:41: %d %+v", len(tr), cnt)
	}
	if tr, cnt := gridRun(t, scen("09:41", 250_000), "U2"); len(tr) != 0 || cnt.UniverseReject["f1_time"] != 1 {
		t.Fatalf("U2 at 09:41: %d %+v", len(tr), cnt)
	}
	if tr, cnt := gridRun(t, scen("10:05", bigV), "U2"); len(tr) != 0 || cnt.UniverseReject["f2_volume"] != 1 {
		t.Fatalf("U2 weak volume: %d %+v", len(tr), cnt)
	}
	if tr, cnt := gridRun(t, scen("10:05", 250_000), "U2"); len(tr) != 1 {
		t.Fatalf("U2 at 10:05 strong volume: %d %+v", len(tr), cnt)
	}
}

func TestGridCapRemoval(t *testing.T) {
	// $200 name, risk 0.1: 500 shares = $100k, over the cap
	bars := vwScen(t, 200, bigV, "09:41", vs{200, 201, 200, 201, bigV}, vs{200.1, 201, 199, 200, bigV}, flat(200, bigV))
	p := DefaultVwapParams()
	r := gridRules(t, "B0")
	capped, cc := SimulateVwapRulesDay("TEST", vwDate, 10e6, bars, r, p)
	if len(capped) != 1 || capped[0].Sized || cc.SkippedSize != 1 {
		t.Fatalf("capped sim %+v %+v", capped, cc)
	}
	p.MaxPosUSD = math.Inf(1)
	tr, cn := SimulateVwapRulesDay("TEST", vwDate, 10e6, bars, r, p)
	if len(tr) != 1 || !tr[0].Sized {
		t.Fatalf("uncapped sim %+v", tr)
	}
	v := BuildGridVariant(r, tr, cn, DefaultVwapParams(), StatOpts{Resamples: 100, Seed: 1})
	if v.Stats.Trades != 0 || v.StatsNoCap.Trades != 1 || v.SkippedSize != 1 || v.StatsNoCap.AvgRNet == 0 {
		t.Errorf("%+v", v)
	}
}

func TestGridFilesSortedByID(t *testing.T) {
	rep := GridReport{Version: GridVersion, Meta: VwMeta{Params: DefaultVwapParams(), Days: []string{vwDate}, Resamples: 100, Seed: 1}}
	bars := exitScen(t, vs{100.5, 101.6, 100.3, 101.5, bigV}, flat(100, bigV))
	for _, id := range []string{"X1", "B0", "U1"} { // deliberately not in order
		tr, cnt := gridRun(t, bars, id)
		rep.Variants = append(rep.Variants, BuildGridVariant(gridRules(t, id), tr, cnt, DefaultVwapParams(), StatOpts{Resamples: 100, Seed: 1}))
	}
	dir := t.TempDir()
	paths, err := WriteGridFiles(dir, rep)
	if err != nil || len(paths) != 3 {
		t.Fatal(err, paths)
	}
	csvb, _ := os.ReadFile(filepath.Join(dir, "grid.csv"))
	lines := strings.Split(strings.TrimSpace(string(csvb)), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[1], "B0,") || !strings.HasPrefix(lines[2], "U1,") || !strings.HasPrefix(lines[3], "X1,") {
		t.Errorf("csv not sorted by id:\n%s", csvb)
	}
	md, _ := os.ReadFile(filepath.Join(dir, "grid.md"))
	if !strings.Contains(string(md), "NOT by performance") || !strings.Contains(string(md), "net R (no cap)") && !strings.Contains(string(md), "Net R (no cap)") {
		t.Error("grid.md lacks the sort note or the no-cap column")
	}
	if _, err := os.Stat(filepath.Join(dir, "grid.csv")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "grid.json")); err != nil {
		t.Error(err)
	}
}
