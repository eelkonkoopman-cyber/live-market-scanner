package persist

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func f64(v float64) *float64 { return &v }
func i64(v int64) *int64     { return &v }

func TestDayLogAlertRoundTripAndOutcome(t *testing.T) {
	ctx := context.Background()
	r := openTest(t).DayLogRepo()

	a := core.DayAlert{Day: "2026-10-16", At: 1000, Sym: "ABCD", ScanID: "s1", ScanName: "Scan One", Kind: "enter",
		Price: f64(2.5), ChgDay: f64(31.2), Vol: i64(900000), NewsHL: "Deal signed", NewsAge: i64(120), Note: "early dildo", Blacklisted: true}
	id, err := r.InsertAlert(ctx, a)
	if err != nil || id == 0 {
		t.Fatalf("insert: %v id=%d", err, id)
	}
	// An alert with nothing optional keeps its NULLs, and the same scan firing twice is two rows.
	if _, err := r.InsertAlert(ctx, core.DayAlert{Day: "2026-10-16", At: 500, Sym: "ZZ", ScanID: "s2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.InsertAlert(ctx, core.DayAlert{Day: "2026-10-15", At: 1, Sym: "OLD", ScanID: "s1"}); err != nil {
		t.Fatal(err)
	}

	got, err := r.Alerts(ctx, "2026-10-16")
	if err != nil || len(got) != 2 {
		t.Fatalf("alerts: %v %+v", err, got)
	}
	if got[0].Sym != "ZZ" || got[0].Price != nil || got[0].Vol != nil || got[0].Out5 != nil || got[0].Done {
		t.Fatalf("ordered by time with NULLs kept: %+v", got[0])
	}
	g := got[1]
	if g.ID != id || *g.Price != 2.5 || *g.ChgDay != 31.2 || *g.Vol != 900000 || g.NewsHL != "Deal signed" || *g.NewsAge != 120 || !g.Blacklisted || g.Note != "early dildo" || g.ScanName != "Scan One" {
		t.Fatalf("round trip: %+v", g)
	}

	pend, _ := r.PendingAlerts(ctx, "2026-10-16")
	if len(pend) != 2 {
		t.Fatalf("both pending: %d", len(pend))
	}
	if err := r.SetOutcome(ctx, id, core.DayOutcome{Out5: f64(1.5), Out15: f64(-2), Out30: nil, MaxUp: f64(4), MaxDown: f64(-3), Done: true}); err != nil {
		t.Fatal(err)
	}
	pend, _ = r.PendingAlerts(ctx, "2026-10-16")
	if len(pend) != 1 || pend[0].Sym != "ZZ" {
		t.Fatalf("finished alert leaves the pending list: %+v", pend)
	}
	got, _ = r.Alerts(ctx, "2026-10-16")
	g = got[1]
	if *g.Out5 != 1.5 || *g.Out15 != -2 || g.Out30 != nil || *g.MaxUp != 4 || *g.MaxDown != -3 || !g.Done {
		t.Fatalf("outcome: %+v", g)
	}
}

func TestDayLogMoversReplaceAndDays(t *testing.T) {
	ctx := context.Background()
	r := openTest(t).DayLogRepo()
	mk := func(side string, rank int, sym string) core.DayMover {
		return core.DayMover{Day: "2026-10-16", Side: side, Rank: rank, Sym: sym, Price: f64(1), ChgDay: f64(float64(rank)), TakenAt: 5, Headline: "h " + sym}
	}
	if err := r.ReplaceMovers(ctx, "2026-10-16", []core.DayMover{mk("loss", 1, "L1"), mk("gain", 2, "G2"), mk("gain", 1, "G1")}); err != nil {
		t.Fatal(err)
	}
	ms, _ := r.Movers(ctx, "2026-10-16")
	if len(ms) != 3 || ms[0].Sym != "G1" || ms[1].Sym != "G2" || ms[2].Sym != "L1" || ms[0].Headline != "h G1" {
		t.Fatalf("gainers by rank then losers: %+v", ms)
	}
	if err := r.ReplaceMovers(ctx, "2026-10-16", []core.DayMover{mk("gain", 1, "NEW")}); err != nil {
		t.Fatal(err)
	}
	if ms, _ = r.Movers(ctx, "2026-10-16"); len(ms) != 1 || ms[0].Sym != "NEW" {
		t.Fatalf("snapshot replaced whole: %+v", ms)
	}
	_, _ = r.InsertAlert(ctx, core.DayAlert{Day: "2026-10-14", At: 1, Sym: "A", ScanID: "s"})
	days, _ := r.Days(ctx, 10)
	if len(days) != 2 || days[0] != "2026-10-16" || days[1] != "2026-10-14" {
		t.Fatalf("days newest first: %v", days)
	}
}

// A database from before the Day Log has neither table: Open adds them, keeps
// the old data and a second Open is a no-op.
func TestDayLogMigrationOnExistingDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `DROP TABLE day_alerts`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `DROP TABLE day_movers`); err != nil {
		t.Fatal(err)
	}
	if err := db.SetMeta(ctx, "keep", "me"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	db, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if v, _ := db.GetMeta(ctx, "keep"); v != "me" {
		t.Fatalf("old data lost: %q", v)
	}
	if _, err := db.DayLogRepo().InsertAlert(ctx, core.DayAlert{Day: "2026-10-16", At: 1, Sym: "A", ScanID: "s"}); err != nil {
		t.Fatalf("day_alerts missing after migrate: %v", err)
	}
	db.Close()
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("second reopen: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM day_alerts`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows survive a reopen: n=%d err=%v", n, err)
	}
	var one sql.NullInt64
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM day_movers`).Scan(&one); err != nil {
		t.Fatalf("day_movers missing: %v", err)
	}
}
