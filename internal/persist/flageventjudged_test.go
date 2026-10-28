package persist

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// A 9 EMA PULLBACK / PB CANCELLED row stores the bar of the dildo it judged
// (flaglog rebuilds its one-verdict-per-dildo guard from it after a restart);
// every other row stores 0.
func TestFlagEventJudgedRoundTrip(t *testing.T) {
	ctx := context.Background()
	r := openTest(t).FlagEventRepo()
	p := 1.2
	dildo := fev("AAA", 100, "dildo", &p)
	pb := fev("AAA", 400, "dildoPb", &p)
	pb.Judged = 100
	if err := r.Insert(ctx, "2026-10-08", []core.FlagEvent{dildo, pb}); err != nil {
		t.Fatal(err)
	}
	got, err := r.Day(ctx, "2026-10-08")
	if err != nil {
		t.Fatal(err)
	}
	if want := []core.FlagEvent{dildo, pb}; !reflect.DeepEqual(got, want) {
		t.Fatalf("judged not round-tripped:\n got %+v\nwant %+v", got, want)
	}
}

// A database from before the judged column: migrated in place, its rows read
// back with judged 0 (unknown), new rows store theirs.
func TestFlagEventMigrationAddsTheJudgedColumn(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	h, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE flag_events (
  day   TEXT NOT NULL,
  sym   TEXT NOT NULL,
  ts    INTEGER NOT NULL,
  kind  TEXT NOT NULL,
  label TEXT NOT NULL,
  price REAL,
  at    INTEGER NOT NULL,
  note  TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (sym, ts, kind)
)`,
		`INSERT INTO flag_events(day,sym,ts,kind,label,price,at,note) VALUES('2026-10-07','OLD',5,'dildoPb','9 EMA PULLBACK',2.5,6,'held')`,
	} {
		if _, err := h.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("legacy schema: %v", err)
		}
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open must migrate flag_events in place: %v", err)
	}
	defer db.Close()
	r := db.FlagEventRepo()
	n := fev("NEW", 9, "dildoCancel", nil)
	n.Judged = 3
	if err := r.Insert(ctx, "2026-10-07", []core.FlagEvent{n}); err != nil {
		t.Fatal(err)
	}
	got, err := r.Day(ctx, "2026-10-07")
	if err != nil || len(got) != 2 || got[0].Judged != 0 || got[0].Note != "held" || got[1].Judged != 3 {
		t.Fatalf("rows after migration: %+v err=%v", got, err)
	}
}
