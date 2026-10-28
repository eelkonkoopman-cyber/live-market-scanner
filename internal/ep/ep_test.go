package ep

import (
	"math"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestIsEarnings(t *testing.T) {
	yes := []string{
		"Acme Reports Third Quarter 2026 Results",
		"ACME beats Q3 EPS estimates, raises FY guidance",
		"Acme Q2 revenue tops forecasts",
		"Acme misses on earnings; outlook cut",
		"Acme Announces Fiscal Fourth Quarter Results",
	}
	no := []string{
		"Why Acme Shares Are Trading Higher Today",
		"Acme Announces Strategic Partnership With Beta Corp",
		"Acme stock jumps after FDA approval",
		"Acme prices public offering of 5 million shares",
		"Acme to present at the Quartermaster conference", // 'quarter' only as a whole word
		"Acme shares are surging; here is why",
		"",
	}
	for _, s := range yes {
		if !IsEarnings(s) {
			t.Errorf("want earnings: %q", s)
		}
	}
	for _, s := range no {
		if IsEarnings(s) {
			t.Errorf("want NOT earnings: %q", s)
		}
	}
}

func bars(n int, closeAt func(i int) float64) []core.DailyBar {
	out := make([]core.DailyBar, n)
	for i := range out {
		c := closeAt(i)
		out[i] = core.DailyBar{C: c, H: c * 1.05, L: c}
	}
	return out
}

func TestStats(t *testing.T) {
	// 70 flat bars: 3m change 0, ADR20 5%.
	chg, adr, d := Stats(bars(70, func(int) float64 { return 10 }))
	if chg != 0 || math.Abs(adr-5) > 1e-9 || d != 20 {
		t.Fatalf("flat: %v %v %v", chg, adr, d)
	}
	// Rising: close(D-1)=20 vs 63 sessions earlier 10 -> +100%.
	b := bars(70, func(i int) float64 { return 10 })
	for i := 64; i < 70; i++ {
		b[i].C = 20
	}
	if chg, _, _ := Stats(b); math.Abs(chg-100) > 1e-9 {
		t.Fatalf("rising chg = %v", chg)
	}
	// Short history: 3m unknown, ADR over what exists, marked by days.
	chg, adr, d = Stats(bars(12, func(int) float64 { return 10 }))
	if !core.IsNull(chg) || d != 12 || math.Abs(adr-5) > 1e-9 {
		t.Fatalf("short: %v %v %v", chg, adr, d)
	}
	// Below MinAdrDays: ADR unknown.
	if _, adr, _ := Stats(bars(3, func(int) float64 { return 10 })); !core.IsNull(adr) {
		t.Fatal("3 bars must not give an ADR")
	}
	if chg, adr, d := Stats(nil); !core.IsNull(chg) || !core.IsNull(adr) || d != 0 {
		t.Fatal("empty")
	}
}

func base() Input {
	return Input{GapPct: 12, Price: 20, Vol: 1_500_000, AvgVol: 1_000_000, Rvol: core.Null, Earnings: true, Chg3m: 4}
}

func TestPassFilters(t *testing.T) {
	if !Pass(base()) {
		t.Fatal("base must pass")
	}
	cases := map[string]func(*Input){
		"gap 9.9%":                  func(i *Input) { i.GapPct = 9.9 },
		"gap unknown":               func(i *Input) { i.GapPct = core.Null },
		"price 4.99":                func(i *Input) { i.Price = 4.99 },
		"no earnings":               func(i *Input) { i.Earnings = false },
		"3m +10.5% (not neglected)": func(i *Input) { i.Chg3m = 10.5 },
		"3m unknown":                func(i *Input) { i.Chg3m = core.Null },
		"volume pace short":         func(i *Input) { i.Vol = 900_000 },
		"no avg volume":             func(i *Input) { i.AvgVol = 0 },
	}
	for name, mut := range cases {
		in := base()
		mut(&in)
		if Pass(in) {
			t.Errorf("%s must fail", name)
		}
	}
	// Edges that pass: gap exactly 10, price exactly 5, 3m exactly +10, vol exactly 1x.
	in := base()
	in.GapPct, in.Price, in.Chg3m, in.Vol = 10, 5, 10, 1_000_000
	if !Pass(in) {
		t.Error("edges must pass")
	}
	// Projected pace: low volume now but rvol 3x passes; 2.9x does not.
	in = base()
	in.Vol, in.Rvol = 100_000, 3
	if !Pass(in) {
		t.Error("rvol 3 must pass")
	}
	in.Rvol = 2.9
	if Pass(in) {
		t.Error("rvol 2.9 must fail")
	}
}

func TestStopAndWindow(t *testing.T) {
	if got := StopPrice(20, 5); math.Abs(got-19) > 1e-9 {
		t.Fatalf("stop %v", got)
	}
	if !core.IsNull(StopPrice(20, core.Null)) {
		t.Fatal("unknown ADR -> unknown stop")
	}
	et, _ := time.LoadLocation("America/New_York")
	// Tuesday 08:00 ET -> Monday 16:00 ET; Monday 08:00 ET -> Friday 16:00; Tuesday 17:00 -> same day 16:00.
	chk := func(now time.Time, want string) {
		t.Helper()
		if got := PrevClose16(now, et).Format("2006-01-02 15:04"); got != want {
			t.Errorf("%v -> %s want %s", now, got, want)
		}
	}
	chk(time.Date(2026, 10, 20, 8, 0, 0, 0, et), "2026-10-19 16:00")
	chk(time.Date(2026, 10, 19, 8, 0, 0, 0, et), "2026-10-16 16:00")
	chk(time.Date(2026, 10, 20, 17, 0, 0, 0, et), "2026-10-20 16:00")
}
