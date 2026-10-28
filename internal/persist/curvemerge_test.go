package persist

import (
	"context"
	"testing"
)

// A periodic save, a restarted process and the nightly flush all write the same
// day: the larger cumulative value per bucket must win, and unobserved (<= 0)
// buckets must never be stored.
func TestUpsertVolumeCurvesMergesAndSkipsUnobserved(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	br := db.BaselineRepo()
	day := "2026-10-05"
	get := func(bucket int) (int64, bool) {
		var v int64
		err := db.sql.QueryRowContext(ctx, `SELECT cum_vol FROM volume_curve_history WHERE ticker='AAA' AND trade_date=? AND bucket=?`, day, bucket).Scan(&v)
		return v, err == nil
	}
	must := func(c []int64) {
		if err := br.UpsertVolumeCurves(ctx, day, map[string][]int64{"AAA": c}); err != nil {
			t.Fatal(err)
		}
	}
	must([]int64{0, 0, 100, 150})   // morning save: buckets 0-1 unobserved
	must([]int64{0, 0, 80, 0, 300}) // a restarted process saw less of bucket 2
	if v, _ := get(2); v != 100 {
		t.Fatalf("bucket 2: the larger value must win, got %d", v)
	}
	if v, _ := get(3); v != 150 {
		t.Fatalf("bucket 3 must keep the earlier save, got %d", v)
	}
	if v, _ := get(4); v != 300 {
		t.Fatalf("bucket 4 new, got %d", v)
	}
	if _, ok := get(0); ok {
		t.Fatal("unobserved buckets must not be stored")
	}
}

// Rows saved before this fix stored zeros for unobserved buckets; the baseline
// must ignore them (in both the mean and the session count).
func TestRecomputeBaselineIgnoresLegacyZeroRows(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	br := db.BaselineRepo()
	for _, d := range []string{"2026-09-28", "2026-09-29"} {
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO volume_curve_history(ticker,trade_date,bucket,cum_vol) VALUES('AAA',?,7,0)`, d); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO volume_curve_history(ticker,trade_date,bucket,cum_vol) VALUES('AAA','2026-09-30',7,900)`); err != nil {
		t.Fatal(err)
	}
	if _, err := br.RecomputeBaseline(ctx, "2026-09-30", []string{"2026-09-28", "2026-09-29", "2026-09-30"}); err != nil {
		t.Fatal(err)
	}
	bt, err := br.LoadBaseline(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := bt.Get("AAA", 7); !ok || v != 900 {
		t.Fatalf("legacy zeros must not drag the mean down: want 900, got %v", v)
	}
	if c := bt.CountAt("AAA", 7); c != 1 {
		t.Fatalf("legacy zeros must not count as sessions: want 1, got %d", c)
	}
}
