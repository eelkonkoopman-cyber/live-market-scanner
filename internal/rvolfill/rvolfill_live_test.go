package rvolfill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/config"
	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/feed"
	"github.com/eelkonkoopman/marketscanner/internal/persist"
)

// TestLiveRvolFill runs the real worker against Massive into a throwaway DB:
// rank the real snapshot, rebuild the last 5 weekdays for the top names in one
// range request each, and recompute the baseline. Every stored curve is then
// checked against an INDEPENDENT single-day request per (symbol, day): before
// 16:00 it must equal that day's own minute bars exactly (no other day leaks
// in), and the day must end at the grouped-daily volume. REST-only, writes
// nothing outside the temp DB; safe while the scanner runs. Opt-in:
//
//	SCANNER_LIVE_FEED_TEST=1 go test ./internal/rvolfill -run TestLiveRvolFill -v
func TestLiveRvolFill(t *testing.T) {
	if os.Getenv("SCANNER_LIVE_FEED_TEST") == "" {
		t.Skip("set SCANNER_LIVE_FEED_TEST=1 to run the live RVOL backfill test")
	}
	cfg, err := config.Load()
	if err != nil || cfg.MassiveKey == "" {
		t.Skip("no Massive API key configured")
	}
	c := feed.New(feed.Config{APIKey: cfg.MassiveKey, RESTBase: cfg.MassiveBase})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	db, err := persist.Open(ctx, filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// The 5 weekdays before today, most recent first (holidays just come back empty).
	var days []core.MarketDay
	for d := time.Now().In(ny).AddDate(0, 0, -1); len(days) < 5; d = d.AddDate(0, 0, -1) {
		if wd := d.Weekday(); wd != time.Saturday && wd != time.Sunday {
			days = append(days, fullDay(d.Format("2006-01-02")))
		}
	}
	dates := make([]string, len(days))
	for i, d := range days {
		dates[i] = d.Date
		g, err := c.GroupedDaily(ctx, d.Date)
		if err != nil {
			t.Fatalf("grouped daily %s: %v", d.Date, err)
		}
		if err := db.TrendRepo().UpsertDailyBars(ctx, d.Date, g); err != nil {
			t.Fatal(err)
		}
	}

	refreshed := false
	w := New(Config{
		Seeds: c.Snapshot,
		Keep: func(sym string) bool {
			n := len(sym)
			return n > 0 && !(n >= 5 && strings.ContainsRune("WRUZ", rune(sym[n-1]))) && !strings.ContainsAny(sym, "pqrwu.")
		},
		Bars:  c.IntradayBetween,
		Days:  func(context.Context) ([]core.MarketDay, error) { return days, nil },
		Store: db,
		Refresh: func(ctx context.Context, syms []string) {
			if _, err := db.BaselineRepo().RecomputeBaselineFor(ctx, dates[0], dates, syms); err != nil {
				t.Errorf("recompute: %v", err)
			}
			refreshed = true
		},
		Cap:     8,
		PassCap: 8,
	})
	w.runOnce(ctx, time.Now().In(ny))
	if !refreshed {
		t.Fatal("the pass rebuilt nothing")
	}
	filled, err := db.FilledCurveDates(ctx, dates)
	if err != nil {
		t.Fatal(err)
	}
	bt, err := db.BaselineRepo().LoadBaseline(ctx) // what the pass's per-ticker recompute stored
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for sym, got := range filled {
		if len(got) != len(dates) {
			t.Errorf("%s: every requested day must be marked, got %d of %d", sym, len(got), len(dates))
		}
		vols, _ := db.DailyVolumes(ctx, sym, dates)
		var tradedDays int
		for _, d := range days {
			bars, err := c.IntradayOn(ctx, sym, d.Date) // independent single-day request
			if err != nil {
				t.Fatalf("%s %s: %v", sym, d.Date, err)
			}
			var pre, all int64
			for _, b := range bars {
				tm := time.UnixMilli(b.T).In(ny)
				if tm.Format("2006-01-02") != d.Date {
					continue
				}
				m := tm.Hour()*60 + tm.Minute()
				if m < 240 || m >= 1200 {
					continue
				}
				all += b.V
				if m < 960 {
					pre += b.V
				}
			}
			if all == 0 {
				continue
			}
			tradedDays++
			// One-day baseline = that day's stored curve.
			if _, err := db.BaselineRepo().RecomputeBaseline(ctx, d.Date, []string{d.Date}); err != nil {
				t.Fatal(err)
			}
			one, _ := db.BaselineRepo().LoadBaseline(ctx)
			at143, _ := one.Get(sym, 143)
			at191, _ := one.Get(sym, 191)
			if int64(at143) != pre {
				t.Errorf("%s %s: stored 15:55 bucket %0.f != the day's own minute bars through 16:00 %d", sym, d.Date, at143, pre)
			}
			want := max(all, vols[d.Date])
			if int64(at191) != want {
				t.Errorf("%s %s: the day must end at max(daily %d, minute total %d), got %.0f", sym, d.Date, vols[d.Date], all, at191)
			}
			t.Logf("%-6s %s  minute-bars to 16:00 %12d  all %12d  daily %12d  gap at close %+.1f%%",
				sym, d.Date, pre, all, vols[d.Date], float64(vols[d.Date]-all)/float64(all)*100)
			checked++
		}
		if n := bt.CountAt(sym, 191); n != tradedDays {
			t.Errorf("%s: the 5-day baseline counts %d sessions at the close, want %d", sym, n, tradedDays)
		}
	}
	if checked == 0 {
		t.Fatal("nothing checked")
	}
	t.Logf("%d symbol-days verified against independent requests", checked)
}
