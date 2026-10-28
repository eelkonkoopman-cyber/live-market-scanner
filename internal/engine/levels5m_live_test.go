package engine

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/config"
	"github.com/eelkonkoopman/marketscanner/internal/core"
	massive "github.com/eelkonkoopman/marketscanner/internal/feed"
)

// TestLiveLevel5mBreaks replays the last completed trading day's real 1-minute
// bars and compares the scanner's 5-minute CLOSE breaks of the pre-market high
// with an independent computation straight from the bars, and reports how many
// trade-level (wick) breaks the 5-minute rule filters out.
//
//	SCANNER_LIVE_FEED_TEST=1 go test ./internal/engine -run TestLiveLevel5mBreaks -v
func TestLiveLevel5mBreaks(t *testing.T) {
	if os.Getenv("SCANNER_LIVE_FEED_TEST") == "" {
		t.Skip("set SCANNER_LIVE_FEED_TEST=1 to run the live 5-minute level check")
	}
	cfg, err := config.Load()
	if err != nil || cfg.MassiveKey == "" {
		t.Skip("no Massive API key configured")
	}
	c := massive.New(massive.Config{APIKey: cfg.MassiveKey, RESTBase: cfg.MassiveBase})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ny, _ := time.LoadLocation("America/New_York")
	day := time.Now().In(ny).AddDate(0, 0, -1)
	for day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
		day = day.AddDate(0, 0, -1)
	}
	open := time.Date(day.Year(), day.Month(), day.Day(), 9, 30, 0, 0, ny).UnixMilli()
	syms := strings.Split(os.Getenv("SCANNER_LIVE_SYMS"), ",")
	if syms[0] == "" {
		syms = []string{"SDEV", "AMOD", "NKE", "NVDA", "INTC", "AIXI", "XRPN", "MSTR", "BMNR", "TNMG"}
	}

	var totTick, tot5, totRef int
	for _, sym := range syms {
		bars, err := c.IntradayOn(ctx, sym, day.Format("2006-01-02"))
		if err != nil || len(bars) < 60 {
			t.Logf("%-5s skipped (%d bars, err %v)", sym, len(bars), err)
			continue
		}
		var pmh float64
		for _, b := range bars {
			if b.T < open && b.H > pmh {
				pmh = b.H
			}
		}
		if pmh <= 0 {
			t.Logf("%-5s skipped (no pre-market)", sym)
			continue
		}

		// Independent reference: 5-minute candles straight from the bars.
		type c5 struct {
			start int64
			close float64
		}
		var cs []c5
		for _, b := range bars {
			st := b.T - b.T%m5Ms
			if len(cs) == 0 || cs[len(cs)-1].start != st {
				cs = append(cs, c5{st, b.C})
			} else {
				cs[len(cs)-1].close = b.C
			}
		}
		ref := map[int64]bool{}
		armed, init := false, false
		var prevC float64
		for _, k := range cs {
			if k.start >= open { // the level exists from the 09:30 freeze on
				if !init {
					init, armed = true, prevC > 0 && float32(prevC) <= float32(pmh)
				}
				if float32(k.close) > float32(pmh) {
					if armed {
						ref[k.start+m5Ms] = true
						armed = false
					}
				} else {
					armed = true
				}
			}
			prevC = k.close
		}

		// Scanner replay.
		ts := &tickerState{sym: sym, curveBkt: -1, curveFirst: -1, barHead: -1}
		got := map[int64]bool{}
		ticks := 0
		frozen := false
		for _, b := range bars {
			if !frozen && b.T >= open {
				withPMH(ts, float32(pmh), true) // the 09:30 freeze
				frozen = true
			}
			sec := b.T/1000 + 30
			before, beforeTick := ts.pmh5.brkMs, ts.pmhBrk.brkMs
			ts.applyAgg(core.Agg{Sym: sym, Open: b.O, High: b.H, Low: b.L, Close: b.C, Vol: int64(b.V),
				StartMs: sec * 1000, EndMs: (sec + 1) * 1000})
			ts.tick5m(b.T/1000 + 60 + 3)
			if ts.pmh5.brkMs != before {
				got[ts.pmh5.brkMs] = true
			}
			if ts.pmhBrk.brkMs != beforeTick {
				ticks++
			}
		}
		for end := range got {
			if !ref[end] {
				t.Errorf("%s: scanner 5m break at %s not in the reference", sym, time.UnixMilli(end).In(ny).Format("15:04"))
			}
		}
		for end := range ref {
			if !got[end] {
				t.Errorf("%s: reference 5m break at %s missed by the scanner", sym, time.UnixMilli(end).In(ny).Format("15:04"))
			}
		}
		t.Logf("%-5s PMH %-9.4f | breaks: trade-level %3d -> 5-min close %2d (reference %2d)", sym, pmh, ticks, len(got), len(ref))
		totTick += ticks
		tot5 += len(got)
		totRef += len(ref)
	}
	t.Logf("TOTAL %s: trade-level breaks %d, 5-minute close breaks %d, reference %d", day.Format("2006-01-02"), totTick, tot5, totRef)
}
