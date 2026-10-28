package persist

import (
	"context"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestAvgDailyVolumeWindow(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	r := db.TrendRepo()
	dates := []string{"2026-09-17", "2026-09-18", "2026-09-21", "2026-09-22", "2026-09-23", "2026-09-24",
		"2026-09-25", "2026-09-28", "2026-09-29", "2026-09-30", "2026-10-01", "2026-10-02", "2026-10-05"}
	for i, d := range dates {
		bars := map[string]core.DailyBar{
			"ALL": {Date: d, C: 10, V: int64(1000 * (i + 1))}, // every day
		}
		if i >= len(dates)-5 { // NEW traded only on the last 5 dates (incl. today)
			bars["NEW"] = core.DailyBar{Date: d, C: 5, V: 9000}
		}
		if i == 3 || i == 6 || i == 9 { // THIN traded on 3 days in the window
			bars["THIN"] = core.DailyBar{Date: d, C: 2, V: 50}
		}
		if err := r.UpsertDailyBars(ctx, d, bars); err != nil {
			t.Fatal(err)
		}
	}
	adv, err := db.AvgDailyVolume(ctx, "2026-10-05", 10, 5)
	if err != nil {
		t.Fatal(err)
	}
	// Window: the 10 dates before 10-05 = indexes 2..11 -> volumes 3000..12000, mean 7500.
	if adv["ALL"] != 7500 {
		t.Fatalf("10-day average excluding today: want 7500 got %v", adv["ALL"])
	}
	if _, ok := adv["NEW"]; ok {
		t.Fatalf("NEW has 4 days in the window (today excluded): below minDays, got %v", adv["NEW"])
	}
	if _, ok := adv["THIN"]; ok {
		t.Fatal("THIN traded on only 3 of 10 days: must be excluded")
	}
}
