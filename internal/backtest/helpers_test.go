package backtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

const fixturesDir = "../../web/src/signals/fixtures"

// loadFixture reads a captured 1-minute fixture (previous trading day + the day).
func loadFixture(t testing.TB, name string) []core.Bar {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixturesDir, name+".json"))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	var fx struct {
		Bars []core.Bar `json:"bars"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return fx.Bars
}

// etMs is the ms timestamp of "2026-10-05 09:00" ET.
func etMs(t testing.TB, stamp string) int64 {
	t.Helper()
	tm, err := time.ParseInLocation("2006-01-02 15:04", stamp, etLocation())
	if err != nil {
		t.Fatalf("bad stamp %q: %v", stamp, err)
	}
	return tm.UnixMilli()
}

// flatBars builds n 1-minute bars from start ("2006-01-02 15:04" ET) at a flat
// price with a constant volume: a series whose 9 EMA is exactly px.
func flatBars(t testing.TB, start string, n int, px float64, vol int64) []core.Bar {
	t.Helper()
	t0 := etMs(t, start)
	out := make([]core.Bar, n)
	for i := range out {
		out[i] = core.Bar{T: t0 + int64(i)*minuteMs, O: px, H: px, L: px, C: px, V: vol}
	}
	return out
}

// day is the standard synthetic day: 08:50 to 16:00 ET (430 bars) at px.
func day(t testing.TB, px float64, vol int64) []core.Bar {
	return flatBars(t, "2026-10-05 08:50", 430, px, vol)
}

// setBar overwrites the bar that starts at stamp.
func setBar(t testing.TB, bars []core.Bar, stamp string, o, h, l, c float64) {
	t.Helper()
	i := barIndex(bars, etMs(t, stamp))
	if i < 0 {
		t.Fatalf("no bar at %s", stamp)
	}
	bars[i].O, bars[i].H, bars[i].L, bars[i].C = o, h, l, c
}

// dropBars removes the bars from..to (inclusive stamps): a minute with no print has no bar.
func dropBars(t testing.TB, bars []core.Bar, from, to string) []core.Bar {
	t.Helper()
	a, b := etMs(t, from), etMs(t, to)
	var out []core.Bar
	for _, x := range bars {
		if x.T < a || x.T > b {
			out = append(out, x)
		}
	}
	return out
}

func mkCand(sym string, bars []core.Bar) *Candidate {
	return &Candidate{Sym: sym, Date: "2026-10-05", Adv10: 1e6, Bars: bars}
}

// sigAt is a hand-made signal on the bar at stamp.
func sigAt(t testing.TB, c *Candidate, stamp string, trigger, stop float64, rank int) Signal {
	t.Helper()
	ts := etMs(t, stamp)
	i := barIndex(c.Bars, ts)
	if i < 0 {
		t.Fatalf("%s has no bar at %s", c.Sym, stamp)
	}
	return Signal{Cand: c, Idx: i, Ts: ts, Trigger: trigger, Stop: stop, Rank: rank}
}

func dayOf(sigs ...Signal) PreparedDay {
	sortSignals(sigs)
	return PreparedDay{Date: "2026-10-05", Signals: sigs}
}

func approx(a, b, tol float64) bool { return a-b <= tol && b-a <= tol }
