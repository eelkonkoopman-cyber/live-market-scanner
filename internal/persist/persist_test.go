package persist

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestScanRepoRoundTrip(t *testing.T) {
	ctx := context.Background()
	r := openTest(t).ScanRepo()

	def := core.ScanDef{
		ID:      "mom5",
		Name:    "5-min Momentum",
		Enabled: true,
		Filter: core.Filter{Op: "and", Conds: []core.Filter{
			{Field: core.FieldLast, Cmp: core.CmpBetween, Value: []any{1, 20}},
			{Field: core.FieldChg5m, Cmp: core.CmpGT, Value: 2},
		}},
		Sort:    []core.SortKey{{Field: core.FieldChg5m, Dir: "desc"}},
		Limit:   50,
		Columns: []string{"last", "chg5m", "rvol"},
		Alert:   core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: "ping", CooldownSec: 120, RequireReentry: true},
	}
	if err := r.Save(ctx, def); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Update (rev bump) then re-list.
	def.Name = "5-min Momentum v2"
	if err := r.Save(ctx, def); err != nil {
		t.Fatalf("save2: %v", err)
	}

	got, err := r.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 scan, got %d", len(got))
	}
	g := got[0]
	if g.ID != "mom5" || g.Name != "5-min Momentum v2" || !g.Enabled || g.Limit != 50 {
		t.Fatalf("roundtrip mismatch: %+v", g)
	}
	if g.Filter.Op != "and" || len(g.Filter.Conds) != 2 {
		t.Fatalf("filter tree lost: %+v", g.Filter)
	}
	if !g.Alert.Enabled || g.Alert.Sound != "ping" || g.Alert.CooldownSec != 120 {
		t.Fatalf("alert config lost: %+v", g.Alert)
	}

	if err := r.Delete(ctx, "mom5"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got, _ := r.List(ctx); len(got) != 0 {
		t.Fatalf("want 0 after delete, got %d", len(got))
	}
}

func TestFloatRepo(t *testing.T) {
	ctx := context.Background()
	r := openTest(t).FloatRepo()

	so := int64(12_000_000)
	if err := r.UpsertFloats(ctx, []core.FloatRef{
		{Ticker: "AAA", SharesOut: &so, FloatShares: nil, AsOf: 1000, Source: "details_v3"},
		{Ticker: "BBB", SharesOut: nil, FloatShares: nil, AsOf: 5000, Source: "details_v3"},
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	floats, err := r.Floats(ctx)
	if err != nil {
		t.Fatalf("floats: %v", err)
	}
	if a := floats["AAA"]; a.SharesOut == nil || *a.SharesOut != so {
		t.Fatalf("AAA shares_out wrong: %+v", a)
	}
	if b := floats["BBB"]; b.SharesOut != nil {
		t.Fatalf("BBB shares_out should be nil: %+v", b)
	}

	// Stale = missing OR as_of < cutoff. Cutoff 4000: AAA(1000) stale, BBB(5000) fresh, CCC missing -> stale.
	stale, err := r.StaleTickers(ctx, 4000, []string{"AAA", "BBB", "CCC"})
	if err != nil {
		t.Fatalf("stale: %v", err)
	}
	if !equalSet(stale, []string{"AAA", "CCC"}) {
		t.Fatalf("stale mismatch: %v", stale)
	}
}

func TestBaselineLifecycle(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	cal := db.CalendarRepo()
	br := db.BaselineRepo()

	days := []core.MarketDay{mkDay("2026-06-01"), mkDay("2026-06-02"), mkDay("2026-06-03")}
	if err := cal.Upsert(ctx, days); err != nil {
		t.Fatalf("calendar upsert: %v", err)
	}
	if got, _ := cal.Sessions(ctx); len(got) != 3 || !got[0].IsTrading {
		t.Fatalf("sessions: %+v", got)
	}

	// Dense 3-bucket cumulative curves (buckets 0,1,2). Only bucket 2 carries a
	// positive value, so buckets 0,1 mean to 0 (-> null). BBB is absent on day 3.
	if err := br.UpsertVolumeCurves(ctx, "2026-06-01", map[string][]int64{"AAA": cum(100), "BBB": cum(10)}); err != nil {
		t.Fatalf("uc1: %v", err)
	}
	if err := br.UpsertVolumeCurves(ctx, "2026-06-02", map[string][]int64{"AAA": cum(200), "BBB": cum(20)}); err != nil {
		t.Fatalf("uc2: %v", err)
	}
	if err := br.UpsertVolumeCurves(ctx, "2026-06-03", map[string][]int64{"AAA": cum(300)}); err != nil {
		t.Fatalf("uc3: %v", err)
	}

	if has, _ := br.HasCurveForDate(ctx, "2026-06-02"); !has {
		t.Fatal("HasCurveForDate(06-02) should be true")
	}
	if has, _ := br.HasCurveForDate(ctx, "2026-06-09"); has {
		t.Fatal("HasCurveForDate(06-09) should be false")
	}

	window, err := br.TrailingTradingDays(ctx, "2026-06-03", 20)
	if err != nil || len(window) != 3 {
		t.Fatalf("window: %v %v", window, err)
	}

	// Zero buckets are "not observed" and never stored, so only bucket 2
	// produces baseline rows: one each for AAA and BBB.
	n, err := br.RecomputeBaseline(ctx, "2026-06-03", window)
	if err != nil {
		t.Fatalf("recompute: %v", err)
	}
	if n != 2 {
		t.Fatalf("want 2 baseline rows, got %d", n)
	}

	bt, err := br.LoadBaseline(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if v, ok := bt.Get("AAA", 2); !ok || v != 200 { // mean(100,200,300)
		t.Fatalf("AAA baseline want 200, got %v ok=%v", v, ok)
	}
	if c := bt.CountAt("AAA", 2); c != 3 {
		t.Fatalf("AAA count want 3, got %d", c)
	}
	if v, ok := bt.Get("BBB", 2); !ok || v != 15 { // mean(10,20) — day 3 absent
		t.Fatalf("BBB baseline want 15, got %v ok=%v", v, ok)
	}
	if c := bt.CountAt("BBB", 2); c != 2 {
		t.Fatalf("BBB count want 2, got %d", c)
	}
	// Zero-valued bucket means a null baseline (Get returns ok=false).
	if _, ok := bt.Get("AAA", 0); ok {
		t.Fatal("bucket 0 (all zeros) should be null, got a value")
	}

	// Status round-trip.
	if err := br.SetBaselineStatus(ctx, "ready", "2026-06-03"); err != nil {
		t.Fatalf("set status: %v", err)
	}
	st, cf, err := br.GetBaselineStatus(ctx)
	if err != nil || st != "ready" || cf != "2026-06-03" {
		t.Fatalf("status: %q %q %v", st, cf, err)
	}

	// Prune everything before 06-03: days 06-01 and 06-02, each 2 tickers x the one
	// stored (non-zero) bucket = 2 -> 4.
	del, err := br.PruneVolumeCurves(ctx, "2026-06-03")
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if del != 4 {
		t.Fatalf("prune want 4 rows, got %d", del)
	}
}

func TestFundamentalsRepo(t *testing.T) {
	ctx := context.Background()
	r := openTest(t).FundamentalsRepo()

	if _, ok, err := r.GetFundamentals(ctx, "ABCD"); err != nil || ok {
		t.Fatalf("expected miss before save, ok=%v err=%v", ok, err)
	}

	rev := 1_000_000.0
	cash := 6_000_000.0
	burn := 2_000_000.0
	f := core.Fundamentals{
		Sym: "ABCD",
		Quarters: []core.FinQuarter{
			{FiscalYear: 2026, FiscalQuarter: 1, PeriodEnd: "2026-03-31", Revenue: &rev, Cash: &cash},
		},
		BurnQuarterly: &burn,
		AsOf:          1700000000,
		Source:        "massive_financials_v1",
	}
	if err := r.SaveFundamentals(ctx, f); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok, err := r.GetFundamentals(ctx, "ABCD")
	if err != nil || !ok {
		t.Fatalf("get after save: ok=%v err=%v", ok, err)
	}
	if got.Sym != "ABCD" || len(got.Quarters) != 1 || got.Quarters[0].Revenue == nil || *got.Quarters[0].Revenue != rev {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if got.BurnQuarterly == nil || *got.BurnQuarterly != burn || got.AsOf != 1700000000 {
		t.Fatalf("burn/asof mismatch: %+v", got)
	}

	// Save again (conflict path) with an updated AsOf overwrites in place.
	f.AsOf = 1700000999
	if err := r.SaveFundamentals(ctx, f); err != nil {
		t.Fatalf("save2: %v", err)
	}
	got2, _, _ := r.GetFundamentals(ctx, "ABCD")
	if got2.AsOf != 1700000999 {
		t.Fatalf("upsert should update as_of, got %d", got2.AsOf)
	}
}

// Default status before any recompute should read "building".
func TestBaselineStatusDefault(t *testing.T) {
	st, _, err := openTest(t).BaselineRepo().GetBaselineStatus(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st != "building" {
		t.Fatalf("default status want building, got %q", st)
	}
}

// --- helpers ---

func mkDay(d string) core.MarketDay {
	return core.MarketDay{
		Date: d, IsTrading: true, IsHalfDay: false,
		SessOpenMin: 240, RegOpenMin: 570, RegCloseMin: 960, SessCloseMin: 1200, LastBucket: 191,
	}
}

// cum returns a dense 3-bucket cumulative curve with only the last bucket set.
func cum(v int64) []int64 { return []int64{0, 0, v} }

func equalSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	m := map[string]bool{}
	for _, g := range got {
		m[g] = true
	}
	for _, w := range want {
		if !m[w] {
			return false
		}
	}
	return true
}
