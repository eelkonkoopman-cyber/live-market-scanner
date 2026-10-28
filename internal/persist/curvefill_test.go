package persist

import (
	"context"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestSaveFilledCurvesMergesAndMarksEveryDate(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	br := db.BaselineRepo()
	get := func(day string, bucket int) int64 {
		var v int64
		_ = db.sql.QueryRowContext(ctx, `SELECT cum_vol FROM volume_curve_history WHERE ticker='AAA' AND trade_date=? AND bucket=?`, day, bucket).Scan(&v)
		return v
	}
	// Live-recorded afternoon on 10-01.
	if err := br.UpsertVolumeCurves(ctx, "2026-10-01", map[string][]int64{"AAA": {0, 0, 900}}); err != nil {
		t.Fatal(err)
	}
	err := db.SaveFilledCurves(ctx, "AAA",
		map[string][]int64{"2026-10-01": {100, 200, 300}},
		[]string{"2026-09-30", "2026-10-01"}) // 09-30: no trades, no curve
	if err != nil {
		t.Fatal(err)
	}
	if get("2026-10-01", 0) != 100 || get("2026-10-01", 2) != 900 {
		t.Fatalf("rebuilt data fills gaps, the larger live value wins: %d, %d", get("2026-10-01", 0), get("2026-10-01", 2))
	}
	f, err := db.FilledCurveDates(ctx, []string{"2026-09-30", "2026-10-01", "2026-10-02"})
	if err != nil {
		t.Fatal(err)
	}
	if !f["AAA"]["2026-09-30"] || !f["AAA"]["2026-10-01"] || f["AAA"]["2026-10-02"] {
		t.Fatalf("every attempted date is marked (even one without trades), nothing else: %v", f)
	}
	// Prune takes the markers with the curves.
	if _, err := br.PruneVolumeCurves(ctx, "2026-10-01"); err != nil {
		t.Fatal(err)
	}
	if f, _ = db.FilledCurveDates(ctx, []string{"2026-09-30", "2026-10-01"}); f["AAA"]["2026-09-30"] || !f["AAA"]["2026-10-01"] {
		t.Fatalf("prune must drop markers before the cutoff only: %v", f)
	}
}

func TestDailyVolumes(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	tr := db.TrendRepo()
	for d, v := range map[string]int64{"2026-10-01": 1500, "2026-10-02": 2500} {
		if err := tr.UpsertDailyBars(ctx, d, map[string]core.DailyBar{"AAA": {C: 1, V: v}, "BBB": {C: 1, V: 7}}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.DailyVolumes(ctx, "AAA", []string{"2026-10-01", "2026-10-02", "2026-10-05"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["2026-10-01"] != 1500 || got["2026-10-02"] != 2500 {
		t.Fatalf("one ticker's volumes on the dates it has: %v", got)
	}
}

func TestRecomputeBaselineForTouchesOnlyThoseTickers(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	br := db.BaselineRepo()
	for _, d := range []string{"2026-09-30", "2026-10-01"} {
		if err := br.UpsertVolumeCurves(ctx, d, map[string][]int64{"AAA": {0, 100}, "BBB": {0, 50}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := br.RecomputeBaseline(ctx, "2026-10-01", []string{"2026-09-30", "2026-10-01"}); err != nil {
		t.Fatal(err)
	}
	// New history for both, plus a day outside the window.
	for d, v := range map[string]int64{"2026-10-01": 300, "2026-10-02": 9999} {
		if err := br.UpsertVolumeCurves(ctx, d, map[string][]int64{"AAA": {0, v}, "BBB": {0, v}}); err != nil {
			t.Fatal(err)
		}
	}
	part, err := br.RecomputeBaselineFor(ctx, "2026-10-01", []string{"2026-09-30", "2026-10-01"}, []string{"AAA"})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := part.Get("AAA", 1); !ok || v != 200 || part.CountAt("AAA", 1) != 2 {
		t.Fatalf("AAA = mean(100, 300) over the window only: got %v n=%d", v, part.CountAt("AAA", 1))
	}
	if _, ok := part.Get("BBB", 1); ok {
		t.Fatal("the partial table holds only the listed tickers")
	}
	all, err := br.LoadBaseline(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := all.Get("BBB", 1); v != 50 {
		t.Fatalf("an unlisted ticker's stored baseline is untouched: BBB = %v", v)
	}
	if v, _ := all.Get("AAA", 1); v != 200 {
		t.Fatalf("the listed ticker's stored baseline is updated: AAA = %v", v)
	}
}
