package engine

import "testing"

func init() { emvGateOff, sigMinPrice = true, 0 }

func TestEmvQualityGate(t *testing.T) {
	emvGateOff = false
	defer func() { emvGateOff = true }()
	ts := &tickerState{sym: "ABC", sig: &signals{}}
	types := map[string]string{"SPY": "ETF"}
	ts.sig.secTypes.Store(&types)
	const off = 0
	at := func(min int64) int64 { return 20000*86_400_000 + min*m1Ms }

	if ts.emvQuality(1, at(9*60+44), off) {
		t.Fatal("before 09:45 passed")
	}
	if !ts.emvQuality(1, at(9*60+45), off) {
		t.Fatal("clean cross at 09:45 blocked")
	}
	e := &ts.d1.ev
	e.lastCrossMs = at(10 * 60)
	if ts.emvQuality(-1, at(10*60+29), off) {
		t.Fatal("cross 29 min after the last passed")
	}
	if !ts.emvQuality(-1, at(10*60+30), off) {
		t.Fatal("cross 30 min after the last blocked")
	}
	e.prevCrossMs = at(9*60 + 50) // the bar being judged is itself the latest cross
	if ts.emvQuality(1, at(10*60), off) {
		t.Fatal("own bar: the previous cross was recent")
	}
	etf := &tickerState{sym: "SPY", sig: ts.sig}
	if etf.emvQuality(1, at(12*60), off) {
		t.Fatal("ETF passed")
	}
}

func TestEmvEarlyDir(t *testing.T) {
	cases := []struct {
		prev, live, c float64
		want          int8
	}{
		{0.001, 0.001, 99.9, -1}, // price lost the VWAP, EMA just above
		{-0.001, -0.001, 100.1, 1},
		{0.001, 0.001, 100.1, 0}, // price still above
		{0.01, 0.01, 99.9, 0},    // EMA too far
		{0.001, -0.001, 99.9, 0}, // already crossed
	}
	for _, c := range cases {
		if got := emvEarlyDir(c.prev, c.live, c.c, 100); got != c.want {
			t.Fatalf("%+v: %d", c, got)
		}
	}
}
