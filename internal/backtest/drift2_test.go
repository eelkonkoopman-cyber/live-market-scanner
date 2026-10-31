package backtest

import (
	"math"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// spyBench: prev day 2026-09-01 closes 100 at 15:59 (with an extended-hours bar
// after it at 50 that must NOT count as the previous regular close); day
// 2026-09-02 closes 100 + slope*k at the k-th minute from 09:30 (H = L = O = C,
// volume 1000, so the VWAP is the running mean of the closes).
func spyBench(t *testing.T, slope float64) *Bench {
	t.Helper()
	prev := dayBars(t, "2026-09-01", "09:30", "15:59", func(k int) core.Bar {
		return core.Bar{O: 100, H: 100, L: 100, C: 100, V: 1000}
	})
	prev = append(prev, core.Bar{T: etMs(t, "2026-09-01 17:00"), O: 50, H: 50, L: 50, C: 50, V: 1000})
	day := dayBars(t, "2026-09-02", "09:30", "15:59", func(k int) core.Bar {
		c := 100 + slope*float64(k)
		return core.Bar{O: c, H: c, L: c, C: c, V: 1000}
	})
	b := NewBench("SPY")
	b.Add("2026-09-02", append(prev, day...))
	return b
}

func TestRegimeAtRising(t *testing.T) {
	b := spyBench(t, 0.01)
	// 10:00 = k 30: close 100.30; prev regular close 100 (not the 17:00 bar at 50).
	r, ok := b.RegimeAt("2026-09-02", etMs(t, "2026-09-02 10:00"))
	if !ok {
		t.Fatal("no regime at 10:00")
	}
	if !dnear(r.DayPct, 0.30) || !r.AboveVwap {
		t.Fatalf("day/vwap: %+v", r)
	}
	// 30 minutes before 10:00 is the 09:30 bar: its CLOSE is the reference (100.00)
	if !dnear(r.Pct30, 0.30) {
		t.Fatalf("spy30: %v", r.Pct30)
	}
	// 10:15 = k 45: the reference is the 09:45 bar's close (100.15)
	r, _ = b.RegimeAt("2026-09-02", etMs(t, "2026-09-02 10:15"))
	if want := 100 * (100.45/100.15 - 1); !dnear(r.Pct30, want) {
		t.Fatalf("spy30 at 10:15: %v want %v", r.Pct30, want)
	}
	// 09:45: nothing starts at or before 09:15, so the 09:30 open (100.00) is the reference
	r, _ = b.RegimeAt("2026-09-02", etMs(t, "2026-09-02 09:45"))
	if !dnear(r.Pct30, 0.15) {
		t.Fatalf("spy30 at 09:45: %v", r.Pct30)
	}
	// no bar at the cross minute (or an extended-hours minute) -> unknown
	if _, ok := b.RegimeAt("2026-09-02", etMs(t, "2026-09-02 08:00")); ok {
		t.Fatal("pre-market regime")
	}
	if _, ok := b.RegimeAt("2026-09-03", etMs(t, "2026-09-03 10:00")); ok {
		t.Fatal("regime on a day without bars")
	}
}

func TestRegimeAtFallingBelowVwap(t *testing.T) {
	b := spyBench(t, -0.01)
	r, ok := b.RegimeAt("2026-09-02", etMs(t, "2026-09-02 10:00"))
	if !ok || r.AboveVwap || !dnear(r.DayPct, -0.30) || !dnear(r.Pct30, -0.30) {
		t.Fatalf("%+v ok=%v", r, ok)
	}
}

func TestRegimeNoLookahead(t *testing.T) {
	// the regime at 10:00 must not change when later bars change
	b1 := spyBench(t, 0.01)
	r1, _ := b1.RegimeAt("2026-09-02", etMs(t, "2026-09-02 10:00"))
	b2 := spyBench(t, 0.01)
	d := b2.days["2026-09-02"]
	for i := range d {
		if d[i].T > etMs(t, "2026-09-02 10:00") {
			d[i].C, d[i].H, d[i].L, d[i].O, d[i].V = 1, 1, 1, 1, 5e9
		}
	}
	r2, _ := b2.RegimeAt("2026-09-02", etMs(t, "2026-09-02 10:00"))
	if r1 != r2 {
		t.Fatalf("lookahead: %+v vs %+v", r1, r2)
	}
}

func TestAttachRegimeAlignmentAndRelStr(t *testing.T) {
	b := spyBench(t, 0.01)
	sigs := []DriftSignal{
		{Date: "2026-09-02", CrossTs: etMs(t, "2026-09-02 10:00"), DayChgPct: 2.0},
		{Date: "2026-09-02", CrossTs: etMs(t, "2026-09-02 10:00"), DayChgPct: math.NaN()},
		{Date: "2026-09-02", CrossTs: etMs(t, "2026-09-02 08:00"), DayChgPct: 2.0},
	}
	b.AttachRegime(sigs)
	s := sigs[0]
	if !s.RegimeOK || !dnear(s.RelStrPct, 1.70) || s.RelStr != "1..3" || s.SpyDay != "0..0.5" || s.SpyVsVwap != "yes" || s.Spy30 != ">0.2" {
		t.Fatalf("%+v", s)
	}
	if s := sigs[1]; !s.RegimeOK || s.RelStr != "n/a" || s.SpyDay != "0..0.5" {
		t.Fatalf("NaN dayChg: %+v", s)
	}
	if s := sigs[2]; s.RegimeOK || s.SpyDay != "n/a" || s.SpyVsVwap != "n/a" || s.Spy30 != "n/a" || s.RelStr != "n/a" {
		t.Fatalf("no spy bar: %+v", s)
	}
}

func TestRegimeBuckets(t *testing.T) {
	for _, c := range []struct {
		v    float64
		want string
	}{{-0.51, "<-0.5"}, {-0.5, "-0.5..0"}, {-0.01, "-0.5..0"}, {0, "0..0.5"}, {0.5, "0..0.5"}, {0.51, ">0.5"}, {math.NaN(), "n/a"}} {
		if g := spyDayBucket(c.v); g != c.want {
			t.Errorf("spyDay %v: %s want %s", c.v, g, c.want)
		}
	}
	for _, c := range []struct {
		v    float64
		want string
	}{{-0.21, "<-0.2"}, {-0.2, "-0.2..0"}, {0, "0..0.2"}, {0.2, "0..0.2"}, {0.21, ">0.2"}} {
		if g := spy30Bucket(c.v); g != c.want {
			t.Errorf("spy30 %v: %s want %s", c.v, g, c.want)
		}
	}
	for _, c := range []struct {
		v    float64
		want string
	}{{-3.1, "<-3"}, {-3, "-3..-1"}, {-1.1, "-3..-1"}, {-1, "-1..1"}, {1, "-1..1"}, {1.1, "1..3"}, {3, "1..3"}, {3.1, ">3"}} {
		if g := relStrBucket(c.v); g != c.want {
			t.Errorf("relStr %v: %s want %s", c.v, g, c.want)
		}
	}
}

func TestD2Consistent(t *testing.T) {
	st := func(n int, mean, tt float64) DStat { return DStat{N: n, Mean: mean, T: tt} }
	h := 0.044
	cases := []struct {
		name   string
		per    []DStat
		pooled DStat
		ok     bool
		dir    int
	}{
		{"positive", []DStat{st(40, 0.2, 2), st(35, 0.15, 1.6)}, st(75, 0.18, 2.5), true, 1},
		{"negative", []DStat{st(40, -0.2, -2), st(35, -0.15, -1.6)}, st(75, -0.18, -2.5), true, -1},
		{"sign disagreement", []DStat{st(40, 0.2, 2), st(35, -0.15, -1.6)}, st(75, 0.02, 0.3), false, 0},
		{"n below 30 in one month", []DStat{st(40, 0.2, 2), st(29, 0.15, 3)}, st(69, 0.18, 3), false, 0},
		{"n exactly 30 passes", []DStat{st(30, 0.2, 2), st(30, 0.15, 1.5)}, st(60, 0.18, 2.5), true, 1},
		{"t below 1.5 in one month", []DStat{st(40, 0.2, 2), st(35, 0.15, 1.49)}, st(75, 0.18, 2.5), false, 0},
		{"pooled below hurdle", []DStat{st(40, 0.05, 2), st(35, 0.03, 1.6)}, st(75, 0.04, 2.5), false, 0},
		{"negative pooled inside hurdle", []DStat{st(40, -0.05, -2), st(35, -0.03, -1.6)}, st(75, -0.04, -2.5), false, 0},
		{"NaN t", []DStat{st(40, 0.2, math.NaN()), st(35, 0.15, 2)}, st(75, 0.18, 2.5), false, 0},
		{"one month only", []DStat{st(40, 0.2, 2)}, st(40, 0.2, 2), false, 0},
	}
	for _, c := range cases {
		ok, dir := D2Consistent(c.per, c.pooled, h, D2MinN, D2MinT)
		if ok != c.ok || dir != c.dir {
			t.Errorf("%s: got %v/%d want %v/%d", c.name, ok, dir, c.ok, c.dir)
		}
	}
}

func TestD2ChanceArithmetic(t *testing.T) {
	p := math.Erfc(1.5 / math.Sqrt2)
	if math.Abs(p-0.1336) > 1e-3 {
		t.Fatalf("p = %v", p)
	}
	if g, w := D2ChanceExpected(1000, 1.5), 1000*p*p*0.5; math.Abs(g-w) > 1e-9 {
		t.Fatalf("expected %v want %v", g, w)
	}
	if g := D2ChanceExpected(1000, 1.5); g < 8.8 || g > 9.1 {
		t.Fatalf("expected ~8.9, got %v", g)
	}
	if g := D2ChanceLiteral(1000); math.Abs(g-17.956) > 1e-9 {
		t.Fatalf("literal %v", g)
	}
	if D2ChanceExpected(0, 1.5) != 0 {
		t.Fatal("zero cells")
	}
}

func TestPairBucketing(t *testing.T) {
	base := d2Base()
	p := PairFeature(base["dayChg%"], base["spyVsVwap"])
	if p.Name != "dayChg% x spyVsVwap" {
		t.Fatalf("name %q", p.Name)
	}
	if want := len(base["dayChg%"].Buckets) * len(base["spyVsVwap"].Buckets); len(p.Buckets) != want {
		t.Fatalf("%d buckets, want %d", len(p.Buckets), want)
	}
	s := DriftSignal{DayChg: "-2..0", SpyVsVwap: "no"}
	got := p.Of(s)
	if got != "-2..0 | no" {
		t.Fatalf("pair bucket %q", got)
	}
	found := false
	for _, b := range p.Buckets {
		found = found || b == got
	}
	if !found {
		t.Fatal("pair bucket not in the list")
	}
	// exactly the pre-registered pairs
	n := 0
	for _, f := range D2Features() {
		if len(f.Name) > 3 && f.Name != "tod (entry)" && containsX(f.Name) {
			n++
		}
	}
	if n != 6 {
		t.Fatalf("%d pairs, want 6", n)
	}
}

func containsX(s string) bool {
	for i := 0; i+2 < len(s); i++ {
		if s[i] == ' ' && s[i+1] == 'x' && s[i+2] == ' ' {
			return true
		}
	}
	return false
}

// synth builds n first-per-symbol-day signals on each of the given dates with
// r60 = base +/- spread alternately.
func synth(dates []string, n int, base, spread float64) []DriftSignal {
	var out []DriftSignal
	for _, d := range dates {
		for i := 0; i < n; i++ {
			v := base + spread
			if i%2 == 1 {
				v = base - spread
			}
			s := DriftSignal{Type: VwIntrabar, Date: d, Sym: "S", First: true, Entry: 100, BenchOK: true, Tod: "10:00-10:30", SpyVsVwap: "yes", DayChg: "-2..0", SpyDay: "0..0.5"}
			for h := range s.Ret {
				s.Ret[h], s.Excess[h] = math.NaN(), math.NaN()
			}
			s.Ret[3], s.Excess[3] = v, v-0.01
			s.Ret[4], s.Excess[4] = v, v-0.01
			out = append(out, s)
		}
	}
	return out
}

func TestDrift2AnalyzePerMonthSplit(t *testing.T) {
	sep := synth([]string{"2026-09-02", "2026-09-03"}, 20, 0.2, 0.1) // 40
	oct := synth([]string{"2026-10-02"}, 40, 0.15, 0.1)              // 40
	sigs := append(sep, oct...)
	r := Drift2Analyze(sigs, DefaultVwapParams())
	if len(r.Months) != 2 || r.Months[0] != "2026-09" || r.Months[1] != "2026-10" {
		t.Fatalf("months %v", r.Months)
	}
	var c *D2Cell
	for i := range r.Cells {
		x := &r.Cells[i]
		if x.Type == VwIntrabar && x.Basis == "raw" && x.Horizon == "r60" && x.Feature == "tod (entry)" && x.Bucket == "10:00-10:30" {
			c = x
		}
	}
	if c == nil {
		t.Fatal("cell missing")
	}
	if c.PerMonth[0].N != 40 || c.PerMonth[1].N != 40 || c.Pooled.N != 80 {
		t.Fatalf("n: %+v", c.PerMonth)
	}
	if !dnear(c.PerMonth[0].Mean, 0.2) || !dnear(c.PerMonth[1].Mean, 0.15) || !dnear(c.Pooled.Mean, 0.175) {
		t.Fatalf("means: %+v pooled %+v", c.PerMonth, c.Pooled)
	}
	if !c.Eligible || !c.Consistent || c.Dir != 1 {
		t.Fatalf("flags: %+v", c)
	}
	// the pair cell carries the same signals
	var pair *D2Cell
	for i := range r.Cells {
		x := &r.Cells[i]
		if x.Type == VwIntrabar && x.Basis == "excess" && x.Horizon == "rEOD" && x.Feature == "dayChg% x spyDay%" && x.Bucket == "-2..0 | 0..0.5" {
			pair = x
		}
	}
	if pair == nil || pair.Pooled.N != 80 || !dnear(pair.PerMonth[0].Mean, 0.19) {
		t.Fatalf("pair: %+v", pair)
	}
	// other signal types are empty: not tested
	if r.NTested == 0 || r.NFlagged == 0 || r.NFlagged > r.NTested {
		t.Fatalf("counts: %+v", r)
	}
	if len(r.Consistent(-1)) != 0 || len(r.Consistent(1)) != r.NFlagged {
		t.Fatalf("directions: %d / %d", len(r.Consistent(1)), len(r.Consistent(-1)))
	}
	// sorted by min abs t, descending
	cs := r.Consistent(1)
	for i := 1; i < len(cs); i++ {
		if cs[i].MinAbsT() > cs[i-1].MinAbsT() {
			t.Fatal("not sorted by min |t|")
		}
	}
}

func TestDrift2AnalyzeSignDisagreementAndN(t *testing.T) {
	sep := synth([]string{"2026-09-02"}, 40, 0.2, 0.1)
	oct := synth([]string{"2026-10-02"}, 40, -0.2, 0.1)
	r := Drift2Analyze(append(sep, oct...), DefaultVwapParams())
	if r.NFlagged != 0 || r.NTested == 0 {
		t.Fatalf("sign disagreement flagged: %+v", r.NFlagged)
	}
	r = Drift2Analyze(append(synth([]string{"2026-09-02"}, 40, 0.2, 0.1), synth([]string{"2026-10-02"}, 29, 0.2, 0.1)...), DefaultVwapParams())
	if r.NFlagged != 0 || r.NTested != 0 {
		t.Fatalf("n=29 month: tested %d flagged %d", r.NTested, r.NFlagged)
	}
}

func TestDrift2NegativeCellAndReportSmoke(t *testing.T) {
	sigs := append(synth([]string{"2026-09-02"}, 40, -0.2, 0.1), synth([]string{"2026-10-02"}, 40, -0.25, 0.1)...)
	r := Drift2Analyze(sigs, DefaultVwapParams())
	if len(r.Consistent(-1)) == 0 || len(r.Consistent(1)) != 0 {
		t.Fatalf("neg %d pos %d", len(r.Consistent(-1)), len(r.Consistent(1)))
	}
	md := Drift2Markdown(r, "hdr")
	if len(md) < 500 || !containsStr(md, "CONSISTENT negative") || !containsStr(md, "Expected by chance") {
		t.Fatal("report incomplete")
	}
	rows := Drift2CSV(r)
	if len(rows) != len(r.Cells)+1 || len(rows[0]) != len(rows[1]) {
		t.Fatalf("csv shape %d x %d", len(rows), len(rows[0]))
	}
	if Drift2Summary(r) == "" {
		t.Fatal("summary")
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestBenchAddUnchangedForTrades(t *testing.T) {
	// the regime bookkeeping must not disturb the existing benchmark lookups
	b := spyBench(t, 0.01)
	got, ok := b.TradeRet("2026-09-02", etMs(t, "2026-09-02 10:00"), etMs(t, "2026-09-02 10:10"))
	if !ok || !dnear(got, 100*(100.40/100.30-1)) {
		t.Fatalf("%v %v", got, ok)
	}
}
