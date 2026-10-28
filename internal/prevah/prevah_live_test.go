package prevah

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/config"
	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/feed"
)

// TestLivePrevAh runs the real loader against Massive: rank the real
// whole-market snapshot, fetch the previous trading day's 1-minute bars for the
// top names, and take each one's 16:00-20:00 ET high. Every level is then
// re-derived from an INDEPENDENT request — the two-day fetch the charts use —
// which also proves the date filter keeps the two days apart. REST-only and
// read-only; safe while the scanner runs. Opt-in, like the Benzinga live test:
//
//	SCANNER_LIVE_FEED_TEST=1 go test ./internal/prevah -run TestLivePrevAh -v
func TestLivePrevAh(t *testing.T) {
	if os.Getenv("SCANNER_LIVE_FEED_TEST") == "" {
		t.Skip("set SCANNER_LIVE_FEED_TEST=1 to run the live after-hours-high test")
	}
	cfg, err := config.Load()
	if err != nil || cfg.MassiveKey == "" {
		t.Skip("no Massive API key configured")
	}
	c := feed.New(feed.Config{APIKey: cfg.MassiveKey, RESTBase: cfg.MassiveBase})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var applied map[string]float64
	w := New(Config{
		Seeds: c.Snapshot,
		// Rough stand-in for the universe (the real one needs a reference load):
		// skip the warrant/right/unit/preferred suffixes that dominate raw movers.
		Keep: func(sym string) bool {
			n := len(sym)
			return n > 0 && !(n >= 5 && (sym[n-1] == 'W' || sym[n-1] == 'R' || sym[n-1] == 'U' || sym[n-1] == 'Z')) && !strings.ContainsAny(sym, "pqrwu.")
		},
		BarsOn:  c.IntradayOn,
		Apply:   func(m map[string]float64) { applied = m },
		Session: func() core.Session { return core.SessionPre }, // pretend the day has started
		Cap:     12,
	})
	now := time.Now()
	w.tick(ctx, now)
	w.tick(ctx, now.Add(settle+time.Second))
	prevDay := w.prevDay
	t.Logf("previous trading day: %s; levels: %d of 12 fetched", prevDay, len(applied))
	if len(applied) == 0 {
		t.Fatal("no after-hours levels from the top-ranked names — the loader found nothing")
	}

	syms := make([]string, 0, len(applied))
	for s := range applied {
		syms = append(syms, s)
	}
	sort.Strings(syms)
	for _, sym := range syms {
		bars, err := c.IntradayWithPrevDay(ctx, sym) // independent: two-day range
		if err != nil {
			t.Fatalf("%s: cross-check fetch: %v", sym, err)
		}
		hi, ok := PostHigh(bars, prevDay, w.loc)
		var n int
		var first, last time.Time
		for _, b := range bars {
			bt := time.UnixMilli(b.T).In(w.loc)
			if m := bt.Hour()*60 + bt.Minute(); bt.Format("2006-01-02") == prevDay && m >= ahStartMin && m < ahEndMin {
				if n == 0 {
					first = bt
				}
				last = bt
				n++
			}
		}
		t.Logf("%-6s loader %-9.4f cross-check %-9.4f  (%d after-hours bars, %s-%s ET)",
			sym, applied[sym], hi, n, first.Format("15:04"), last.Format("15:04"))
		if !ok || hi != applied[sym] {
			t.Errorf("%s: loader level %v != independent after-hours high %v", sym, applied[sym], hi)
		}
	}
}
