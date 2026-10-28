package engine

// Test-only imports of feed/config: the engine's PRODUCTION code still depends
// only on core + logx (the import law); this opt-in live check needs real bars.

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/config"
	"github.com/eelkonkoopman/marketscanner/internal/core"
	massive "github.com/eelkonkoopman/marketscanner/internal/feed"
)

// TestLiveEma9MatchesChart replays the last completed trading day's real
// 1-minute bars through a FRESH scanner EMA (as if the scanner started at that
// day's first bar) and compares every "closed under the 9 EMA" signal with the
// chart's EMA, which also has the day before as history (exactly what the 1m
// chart cells load). Signals are compared only after the scanner's warm-up.
//
//	SCANNER_LIVE_FEED_TEST=1 go test ./internal/engine -run TestLiveEma9MatchesChart -v
//	(optional) SCANNER_LIVE_SYMS=SDEV,AMOD,...
func TestLiveEma9MatchesChart(t *testing.T) {
	if os.Getenv("SCANNER_LIVE_FEED_TEST") == "" {
		t.Skip("set SCANNER_LIVE_FEED_TEST=1 to run the live 9 EMA check")
	}
	cfg, err := config.Load()
	if err != nil || cfg.MassiveKey == "" {
		t.Skip("no Massive API key configured")
	}
	c := massive.New(massive.Config{APIKey: cfg.MassiveKey, RESTBase: cfg.MassiveBase})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	ny, _ := time.LoadLocation("America/New_York")
	back := func(d time.Time) time.Time {
		d = d.AddDate(0, 0, -1)
		for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			d = d.AddDate(0, 0, -1)
		}
		return d
	}
	day := back(time.Now().In(ny))
	hist := back(day)
	syms := strings.Split(os.Getenv("SCANNER_LIVE_SYMS"), ",")
	if syms[0] == "" {
		syms = []string{"SDEV", "AMOD", "NKE", "NVDA", "INTC", "AIXI", "XRPN", "MSTR", "BMNR", "TNMG"}
	}

	var totRef, totScan, totMatch int
	for _, sym := range syms {
		hb, err1 := c.IntradayOn(ctx, sym, hist.Format("2006-01-02"))
		db, err2 := c.IntradayOn(ctx, sym, day.Format("2006-01-02"))
		if err1 != nil || err2 != nil || len(db) < emaWarm+5 {
			t.Logf("%-5s skipped (bars: hist %d day %d, err %v %v)", sym, len(hb), len(db), err1, err2)
			continue
		}
		// Chart reference over hist+day.
		all := append(append([]core.Bar{}, hb...), db...)
		closes := make([]float64, len(all))
		for i, b := range all {
			closes[i] = b.C
		}
		ref := chartEMA(closes, emaPeriod)
		refX := map[int64]bool{} // candle END ms -> chart says "closed under after at/above"
		for i := len(hb); i < len(all); i++ {
			if i > 0 && !math.IsNaN(ref[i-1]) && closes[i] < ref[i] && closes[i-1] >= ref[i-1] {
				refX[all[i].T+m1Ms] = true
			}
		}
		// Scanner: fresh start at the day's first bar.
		ts := &tickerState{sym: sym, curveBkt: -1, curveFirst: -1, barHead: -1}
		scanX := map[int64]bool{}
		var warmFrom int64
		for _, b := range db {
			sec := b.T/1000 + 30
			ts.applyAgg(core.Agg{Sym: sym, Open: b.O, High: b.H, Low: b.L, Close: b.C, Vol: int64(b.V),
				StartMs: sec * 1000, EndMs: (sec + 1) * 1000})
			before := ts.emaXMs
			ts.tick1m(b.T/1000 + 60 + 3) // close this candle now
			if ts.emaN == emaWarm+1 {
				warmFrom = b.T + m1Ms
			}
			if ts.emaXMs != before {
				scanX[ts.emaXMs] = true
			}
		}
		nRef, match := 0, 0
		for end := range refX {
			if end >= warmFrom {
				nRef++
				if scanX[end] {
					match++
				}
			}
		}
		for end := range scanX {
			if !refX[end] {
				t.Errorf("%s: scanner signal at %s the chart does not show", sym, time.UnixMilli(end).In(ny).Format("15:04"))
			}
		}
		for end := range refX {
			if end >= warmFrom && !scanX[end] {
				t.Errorf("%s: chart signal at %s the scanner missed", sym, time.UnixMilli(end).In(ny).Format("15:04"))
			}
		}
		t.Logf("%-5s %4d bars | after warm-up (%s ET): chart %3d losses, scanner %3d, matching %3d",
			sym, len(db), time.UnixMilli(warmFrom).In(ny).Format("15:04"), nRef, len(scanX), match)
		totRef += nRef
		totScan += len(scanX)
		totMatch += match
	}
	t.Logf("TOTAL: chart %d losses after warm-up, scanner %d, matching %d (%s vs history from %s)",
		totRef, totScan, totMatch, day.Format("2006-01-02"), hist.Format("2006-01-02"))
	if totRef == 0 {
		t.Fatal("no signals to compare — pick more active symbols")
	}
}
