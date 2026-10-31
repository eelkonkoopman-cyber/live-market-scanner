package backtest

import (
	"math"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func dnear(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// dayBars: one bar per minute from..to (inclusive, "15:04") on `date` with the
// k-th bar built by f.
func dayBars(t testing.TB, date, from, to string, f func(k int) core.Bar) []core.Bar {
	t.Helper()
	t0, t1 := etMs(t, date+" "+from), etMs(t, date+" "+to)
	var out []core.Bar
	for x, k := t0, 0; x <= t1; x, k = x+minuteMs, k+1 {
		b := f(k)
		b.T = x
		out = append(out, b)
	}
	return out
}

func TestDriftForwardEODTruncation(t *testing.T) {
	bars := dayBars(t, vwDate, "15:40", "15:59", func(k int) core.Bar {
		o := 100 + float64(k)
		return core.Bar{O: o, H: o + 1, L: o - 1, C: o + 0.5, V: 1000}
	})
	e := 10 // 15:50, open 110
	ret, units, mfe, mae, riskPct, ok := DriftForward(bars, e, 108)
	if !ok {
		t.Fatal("not ok")
	}
	// r5: last bar starting <= 15:55 = k15, close 115.5; r15/r30/r60/EOD: the day ends at k19, close 119.5
	want := [5]float64{100 * (115.5/110 - 1), 100 * (119.5/110 - 1), 100 * (119.5/110 - 1), 100 * (119.5/110 - 1), 100 * (119.5/110 - 1)}
	for i := range want {
		if !dnear(ret[i], want[i]) {
			t.Errorf("ret[%d]=%v want %v", i, ret[i], want[i])
		}
	}
	if !dnear(mfe, 100*(120.0/110-1)) || !dnear(mae, 100*(109.0/110-1)) {
		t.Errorf("mfe %v mae %v", mfe, mae)
	}
	if !dnear(riskPct, 100*2.0/110) {
		t.Errorf("riskPct %v", riskPct)
	}
	if !dnear(units[0], want[0]/riskPct) || !dnear(units[4], want[4]/riskPct) {
		t.Errorf("units %v", units)
	}
	// entry at or below the cross low: no risk unit
	_, u2, _, _, rp, _ := DriftForward(bars, e, 110)
	if !math.IsNaN(rp) || !math.IsNaN(u2[1]) {
		t.Errorf("no-risk units: %v %v", rp, u2)
	}
	if _, _, _, _, _, ok := DriftForward(bars, len(bars), 1); ok {
		t.Error("past the end must not be ok")
	}
}

func TestDriftForwardMidDayHorizons(t *testing.T) {
	bars := dayBars(t, vwDate, "10:00", "15:59", func(k int) core.Bar {
		o := 100 + float64(k)*0.1
		c := 100 + float64(k+1)*0.1
		return core.Bar{O: o, H: c + 0.1, L: o - 0.1, C: c, V: 1000}
	})
	ret, _, mfe, mae, _, ok := DriftForward(bars, 0, 99)
	if !ok {
		t.Fatal("not ok")
	}
	// bar k closes at 100+0.1(k+1); horizon N = the bar starting N minutes after the entry
	for i, n := range []int{5, 15, 30, 60} {
		if want := 0.1 * float64(n+1); !dnear(ret[i], want) {
			t.Errorf("r%d=%v want %v", n, ret[i], want)
		}
	}
	if !dnear(ret[4], 0.1*360) {
		t.Errorf("rEOD %v", ret[4])
	}
	// MFE/MAE: bars k=0..29 only (starting before entry+30min)
	if !dnear(mfe, 100*((100+0.1*30+0.1)/100-1)) || !dnear(mae, 100*((99.9)/100-1)) {
		t.Errorf("mfe %v mae %v", mfe, mae)
	}
}

func TestDriftStat(t *testing.T) {
	s := DriftStat([]float64{1, 2, 3, 4, 5, math.NaN()})
	if s.N != 5 || !dnear(s.Mean, 3) || !dnear(s.Median, 3) || !dnear(s.Pos, 100) {
		t.Fatalf("%+v", s)
	}
	if want := 3 / (math.Sqrt(2.5) / math.Sqrt(5)); !dnear(s.T, want) {
		t.Errorf("t=%v want %v", s.T, want)
	}
	if s := DriftStat([]float64{2}); !math.IsNaN(s.T) || s.N != 1 {
		t.Errorf("single %+v", s)
	}
	if s := DriftStat([]float64{2, 2, 2}); !math.IsNaN(s.T) {
		t.Errorf("zero variance %+v", s)
	}
	if s := DriftStat([]float64{-1, 1, 3}); !dnear(s.Pos, 200.0/3) {
		t.Errorf("pos %+v", s)
	}
}

func TestDriftBuckets(t *testing.T) {
	for _, c := range []struct {
		got, want string
	}{
		{pctBucket(-3), "<-2"}, {pctBucket(-2), "-2..0"}, {pctBucket(-0.01), "-2..0"}, {pctBucket(0), "0..2"}, {pctBucket(2), "2..5"},
		{pctBucket(5), "5..10"}, {pctBucket(10), "5..10"}, {pctBucket(10.1), ">10"}, {pctBucket(math.NaN()), "n/a"},
		{rvolBucket(1.5), "1.5-2"}, {rvolBucket(2), "2-3"}, {rvolBucket(5), "3-5"}, {rvolBucket(5.1), ">5"},
		{burstBucket(0.9), "<1"}, {burstBucket(1), "1-2"}, {burstBucket(2), "2-4"}, {burstBucket(4), "2-4"}, {burstBucket(4.1), ">4"},
		{crossNoBucket(1), "1st"}, {crossNoBucket(2), "2nd"}, {crossNoBucket(3), "3rd+"}, {crossNoBucket(7), "3rd+"},
		{belowBucket(9), "<10"}, {belowBucket(10), "10-30"}, {belowBucket(30), "30-90"}, {belowBucket(90), "30-90"}, {belowBucket(91), ">90"},
		{distBucket(-0.1), "<0"}, {distBucket(0), "0-0.2"}, {distBucket(0.2), "0.2-0.5"}, {distBucket(0.5), "0.2-0.5"}, {distBucket(0.51), ">0.5"},
		{priceBucket(30), "25-50"}, {priceBucket(50), "50-100"}, {priceBucket(100), "100-300"}, {priceBucket(300), "100-300"}, {priceBucket(301), ">300"},
		{todBucket(9*60 + 45), "09:30-10:00"}, {todBucket(10 * 60), "10:00-10:30"}, {todBucket(10*60 + 30), "10:30-11:00"},
		{todBucket(11 * 60), "11:00-12:00"}, {todBucket(12 * 60), "12:00-14:00"}, {todBucket(14 * 60), "14:00-16:00"},
	} {
		if c.got != c.want {
			t.Errorf("bucket got %q want %q", c.got, c.want)
		}
	}
}

func TestDriftBurstAndBelowRun(t *testing.T) {
	var bars []core.Bar
	for i := 0; i < 12; i++ {
		bars = append(bars, core.Bar{V: 100})
	}
	bars[10].V = 400
	if got := burstAt(bars, 10); !dnear(got, 4) {
		t.Errorf("burst %v", got)
	}
	if !math.IsNaN(burstAt(bars, 8)) {
		t.Error("burst with fewer than 9 prior bars must be NaN")
	}
	nan := math.NaN()
	dd := []float64{nan, -1, 0, -2, 1, -1, 0, -3}
	if got := belowRun(dd, 8); got != 3 {
		t.Errorf("run %d want 3", got)
	}
	if got := belowRun(dd, 4); got != 3 { // idx 3,2,1 then NaN
		t.Errorf("run %d want 3", got)
	}
	if got := belowRun(dd, 5); got != 0 {
		t.Errorf("run %d want 0", got)
	}
}

// featScen: previous day closes (98) with an after-hours print (99, must be
// ignored), then the vwapcross scenario with a first cross at 09:41, a drop that
// re-arms the side, and a second cross at 09:44.
func featScen(t testing.TB) []core.Bar {
	prev := dayBars(t, "2026-10-02", "15:55", "15:59", func(k int) core.Bar {
		return core.Bar{O: 98, H: 98, L: 98, C: 98, V: 1000}
	})
	prev = append(prev, core.Bar{T: etMs(t, "2026-10-02 16:05"), O: 99, H: 99, L: 99, C: 99, V: 1000})
	day := vwScen(t, 100, bigV, "09:41",
		crossBar(), vs{90, 90, 90, 90, bigV}, vs{90, 90, 90, 90, bigV}, vs{100, 130, 100, 130, bigV},
		flat(130, bigV), flat(130, bigV), flat(130, bigV))
	return append(prev, day...)
}

func driftOf(sigs []DriftSignal, typ string) []DriftSignal {
	var o []DriftSignal
	for _, s := range sigs {
		if s.Type == typ {
			o = append(o, s)
		}
	}
	return o
}

func TestDriftFeaturesOnScenario(t *testing.T) {
	sigs := DriftDay("TEST", vwDate, 10e6, featScen(t), DefaultVwapParams())
	cl := driftOf(sigs, VwClose)
	if len(cl) != 2 {
		t.Fatalf("close signals %d, want 2", len(cl))
	}
	a, b := cl[0], cl[1]
	if a.CrossTs != etMs(t, vwDate+" 09:41") || a.EntryTs != etMs(t, vwDate+" 09:42") || a.Entry != 90 || !a.First || b.First {
		t.Errorf("first signal %+v", a)
	}
	if !dnear(a.GapPct, 100*(100.0/98-1)) || a.Gap != "2..5" { // the 16:05 print is not a regular close
		t.Errorf("gap %v %s", a.GapPct, a.Gap)
	}
	if !dnear(a.DayChgPct, 100*(101.0/98-1)) || a.DayChg != "2..5" {
		t.Errorf("dayChg %v %s", a.DayChgPct, a.DayChg)
	}
	vw := (11*100*float64(bigV) + (101+100+101)/3.0*float64(bigV)) / (12 * float64(bigV))
	if !dnear(a.DistPct, 100*(101-vw)/vw) || a.DistB != ">0.5" {
		t.Errorf("dist %v %s", a.DistPct, a.DistB)
	}
	if !dnear(a.Burst, 1) || a.BurstB != "1-2" {
		t.Errorf("burst %v %s", a.Burst, a.BurstB)
	}
	if a.CrossN != 1 || a.CrossB != "1st" || b.CrossN != 2 || b.CrossB != "2nd" {
		t.Errorf("crossNo %d %d", a.CrossN, b.CrossN)
	}
	if a.BelowMin != 11 || a.BelowB != "10-30" { // 09:30..09:40 all at EMA <= VWAP
		t.Errorf("belowMin %d %s", a.BelowMin, a.BelowB)
	}
	if b.BelowMin != 2 || b.BelowB != "<10" { // 09:42, 09:43
		t.Errorf("second belowMin %d", b.BelowMin)
	}
	if a.AboveOpen != "yes" || a.Trend5 != "yes" || a.PriceB != "100-300" || a.Tod != "09:30-10:00" || a.RvolB != ">5" {
		t.Errorf("flags %+v", a)
	}
	// entry (90) below the cross low (100): no stop units
	if !math.IsNaN(a.Units[1]) {
		t.Errorf("entry below the cross low must have no stop units, got %v", a.Units[1])
	}
	if !dnear(a.Ret[0], 100*(130.0/90-1)) { // r5: the last bar starting <= 09:47 is a flat 130 bar
		t.Errorf("r5 %v", a.Ret[0])
	}
	// the intrabar variant also fires on the first cross
	ib := driftOf(sigs, VwIntrabar)
	if len(ib) == 0 || ib[0].CrossN != 1 || ib[0].CrossTs != a.CrossTs {
		t.Errorf("intrabar %+v", ib)
	}
	// features of the first signal use only data up to the cross bar: dropping later bars keeps them
	cut := featScen(t)
	cut = cut[:len(cut)-5]
	for _, s := range driftOf(DriftDay("TEST", vwDate, 10e6, cut, DefaultVwapParams()), VwClose)[:1] {
		if s.Gap != a.Gap || s.DayChg != a.DayChg || s.BelowMin != a.BelowMin || s.Trend5 != a.Trend5 || !dnear(s.DistPct, a.DistPct) || !dnear(s.Burst, a.Burst) {
			t.Errorf("features changed with later bars: %+v vs %+v", s, a)
		}
	}
}

// fiveScen: a flat regular day before, then a flat 09:30-09:59, then a climb
// 101..105 over 10:00-10:04 (the 10:04 close is closeLast), then flat.
func fiveScen(t testing.TB, closeLast float64) []core.Bar {
	prev := dayBars(t, "2026-10-02", "09:30", "15:59", func(k int) core.Bar {
		return core.Bar{O: 100, H: 100, L: 100, C: 100, V: bigV}
	})
	day := dayBars(t, vwDate, "09:30", "09:59", func(k int) core.Bar {
		return core.Bar{O: 100, H: 100, L: 100, C: 100, V: bigV}
	})
	for i := 0; i < 5; i++ {
		c := 101 + float64(i)
		if i == 4 {
			c = closeLast
		}
		day = append(day, core.Bar{T: etMs(t, vwDate+" 10:00") + int64(i)*minuteMs, O: c - 1, H: c, L: math.Min(c-1, 100), C: c, V: bigV})
	}
	for i := 5; i < 9; i++ {
		day = append(day, core.Bar{T: etMs(t, vwDate+" 10:00") + int64(i)*minuteMs, O: closeLast, H: closeLast, L: closeLast, C: closeLast, V: bigV})
	}
	return append(prev, day...)
}

func TestDriftFiveMinuteSignal(t *testing.T) {
	sigs := DriftDay("TEST", vwDate, 10e6, fiveScen(t, 105), DefaultVwapParams())
	f := driftOf(sigs, Drift5m)
	if len(f) != 1 {
		t.Fatalf("5m signals %d, want 1: %+v", len(f), f)
	}
	s := f[0]
	// evaluated at the completed 10:00-10:05 candle: cross bar = its last 1-minute bar (10:04), entry = 10:05
	if s.CrossTs != etMs(t, vwDate+" 10:04") || s.EntryTs != etMs(t, vwDate+" 10:05") {
		t.Errorf("cross %d entry %d", s.CrossTs, s.EntryTs)
	}
	if s.CrossLow != 100 || s.CrossClose != 105 { // the candle's low, its close
		t.Errorf("candle low %v close %v", s.CrossLow, s.CrossClose)
	}
	if s.CrossN != 1 || s.BelowMin != 30 { // six earlier judged 5m candles 09:30..09:55 (EMA == VWAP)
		t.Errorf("crossNo %d belowMin %d", s.CrossN, s.BelowMin)
	}
	if s.Trend5 != "yes" {
		t.Errorf("trend5 %s", s.Trend5)
	}
	// no lookahead into the forming candle: when the candle closes back at 100 the climb inside it cannot fire
	if f2 := driftOf(DriftDay("TEST", vwDate, 10e6, fiveScen(t, 100), DefaultVwapParams()), Drift5m); len(f2) != 0 {
		t.Errorf("a candle closing at the EMA must not fire: %+v", f2)
	}
	// and a signal does not depend on bars after the entry bar
	cut := fiveScen(t, 105)
	cut = cut[:len(cut)-3]
	if f3 := driftOf(DriftDay("TEST", vwDate, 10e6, cut, DefaultVwapParams()), Drift5m); len(f3) != 1 || f3[0].CrossTs != s.CrossTs {
		t.Errorf("signal changed with later bars: %+v", f3)
	}
	// a cut inside the forming candle (nothing after 10:03) yields no 5m signal
	cut2 := fiveScen(t, 105)
	cut2 = cut2[:len(cut2)-9+4]
	if f4 := driftOf(DriftDay("TEST", vwDate, 10e6, cut2, DefaultVwapParams()), Drift5m); len(f4) != 0 {
		t.Errorf("a forming candle must not signal: %+v", f4)
	}
}

func TestRegular5mCandles(t *testing.T) {
	bars := dayBars(t, vwDate, "09:28", "09:41", func(k int) core.Bar {
		return core.Bar{O: 1, H: float64(k + 2), L: float64(k), C: float64(k), V: 1}
	})
	cs := regular5m(bars)
	// 09:28/09:29 are pre-open; 09:30-09:34, 09:35-09:39, 09:40-09:41 (forming)
	if len(cs) != 3 {
		t.Fatalf("candles %d", len(cs))
	}
	if bars[cs[0].lastIdx].T != etMs(t, vwDate+" 09:34") || cs[0].close != 6 || cs[0].low != 2 {
		t.Errorf("candle 0 %+v", cs[0])
	}
	if cs[0].end != etMs(t, vwDate+" 09:35") {
		t.Errorf("end %d", cs[0].end)
	}
}

func TestDriftReportSmoke(t *testing.T) {
	sigs := DriftDay("TEST", vwDate, 10e6, featScen(t), DefaultVwapParams())
	SortDriftSignals(sigs)
	md := DriftMarkdown(sigs, DefaultVwapParams(), "hdr")
	if len(md) == 0 || md != DriftMarkdown(sigs, DefaultVwapParams(), "hdr") {
		t.Fatal("markdown empty or not deterministic")
	}
	rows, _ := DriftCSV(sigs)
	if len(rows) != len(sigs)+1 || len(rows[0]) != len(rows[1]) {
		t.Fatalf("csv rows %d cols %d/%d", len(rows), len(rows[0]), len(rows[1]))
	}
	if h := DriftHurdlePct([]DriftSignal{{Entry: 100}}, DefaultVwapParams()); !dnear(h, 0.03) {
		t.Errorf("hurdle %v", h)
	}
	// 50 identical-sign signals in one bucket flag; 10 do not
	var many []DriftSignal
	for i := 0; i < 50; i++ {
		many = append(many, DriftSignal{Type: VwClose, Entry: 100, Ret: [5]float64{1, 1 + float64(i%5)*0.01, 1, 1, 1}, Tod: "09:30-10:00"})
	}
	if fl, _ := DriftFlags(many, DefaultVwapParams()); len(fl) == 0 {
		t.Error("expected flags for n=50 with large t")
	}
	if fl, _ := DriftFlags(many[:10], DefaultVwapParams()); len(fl) != 0 {
		t.Error("n=10 must not flag")
	}
}
