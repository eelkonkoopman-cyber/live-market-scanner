package persist

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func fev(sym string, ts int64, kind string, price *float64) core.FlagEvent {
	return core.FlagEvent{Ts: ts, At: ts + 5, Sym: sym, Kind: kind, Label: kind + "!", Price: price}
}

func TestFlagEventInsertDedupeAndOrder(t *testing.T) {
	ctx := context.Background()
	r := openTest(t).FlagEventRepo()
	p := 12.5

	if got, err := r.Day(ctx, "2026-10-05"); err != nil || len(got) != 0 {
		t.Fatalf("fresh day should be empty: %v err=%v", got, err)
	}
	if err := r.Insert(ctx, "2026-10-05", nil); err != nil {
		t.Fatalf("empty insert: %v", err)
	}

	// Same ts on two symbols: insertion order breaks the tie; ts order wins over insertion.
	first := []core.FlagEvent{fev("TSLA", 200, "impulse", &p), fev("AAPL", 100, "flat", nil), fev("NVDA", 200, "entry", &p)}
	if err := r.Insert(ctx, "2026-10-05", first); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// A repeat (with a different At/label) is ignored; the new one is added.
	dup := fev("TSLA", 200, "impulse", nil)
	dup.At, dup.Label = 999, "changed"
	if err := r.Insert(ctx, "2026-10-05", []core.FlagEvent{dup, fev("TSLA", 300, "exit", &p)}); err != nil {
		t.Fatalf("insert dup: %v", err)
	}
	got, err := r.Day(ctx, "2026-10-05")
	if err != nil {
		t.Fatal(err)
	}
	want := []core.FlagEvent{first[1], first[0], first[2], fev("TSLA", 300, "exit", &p)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("day mismatch:\n got %+v\nwant %+v", got, want)
	}
	if got[0].Price != nil || got[1].Price == nil || *got[1].Price != 12.5 {
		t.Fatalf("price nil/value not round-tripped: %+v", got)
	}
	if other, _ := r.Day(ctx, "2026-10-04"); len(other) != 0 {
		t.Fatalf("other day should be empty: %v", other)
	}
}

func TestFlagEventDaysDescLimit(t *testing.T) {
	ctx := context.Background()
	r := openTest(t).FlagEventRepo()
	for i, d := range []string{"2026-10-01", "2026-10-03", "2026-10-02"} {
		if err := r.Insert(ctx, d, []core.FlagEvent{fev("A", int64(i+1), "flat", nil), fev("B", int64(i+1), "flat", nil)}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.Days(ctx, 10)
	if err != nil || !reflect.DeepEqual(got, []string{"2026-10-03", "2026-10-02", "2026-10-01"}) {
		t.Fatalf("days: %v err=%v", got, err)
	}
	got, err = r.Days(ctx, 2)
	if err != nil || !reflect.DeepEqual(got, []string{"2026-10-03", "2026-10-02"}) {
		t.Fatalf("limited days: %v err=%v", got, err)
	}
}

func TestFlagEventPrune(t *testing.T) {
	ctx := context.Background()
	r := openTest(t).FlagEventRepo()
	for i, d := range []string{"2026-08-30", "2026-09-05", "2026-09-06", "2026-10-06"} {
		if err := r.Insert(ctx, d, []core.FlagEvent{fev("A", int64(i+1), "flat", nil)}); err != nil {
			t.Fatal(err)
		}
	}
	// today 2026-10-06 minus 30 days = 2026-09-06: older days go, the cutoff day stays.
	if err := r.Prune(ctx, 30, "2026-10-06"); err != nil {
		t.Fatalf("prune: %v", err)
	}
	got, err := r.Days(ctx, 10)
	if err != nil || !reflect.DeepEqual(got, []string{"2026-10-06", "2026-09-06"}) {
		t.Fatalf("days after prune: %v err=%v", got, err)
	}
	if err := r.Prune(ctx, 30, "not-a-date"); err == nil {
		t.Fatal("bad today should error")
	}
}

// The dildo / climax signals carry a one-line note: it round-trips with the
// row, an empty note (every flag kind) reads back empty, and the first
// recording's note wins over a repeat's (INSERT OR IGNORE).
func TestFlagEventNoteRoundTrip(t *testing.T) {
	ctx := context.Background()
	r := openTest(t).FlagEventRepo()
	p := 1.09
	dildo := fev("SBFM", 100, "dildo", &p)
	dildo.Label, dildo.Note = "GREEN DILDO", "+9.0% body (4.5x prior), 200x volume, $218k; floor 1.0450"
	bottom := fev("BIYA", 200, "bottom", &p)
	bottom.Label, bottom.Note = "BOTTOM CONFIRMED", "closed 9.60 above the climax midpoint 9.43"
	flag := fev("BEAT", 150, "entry", &p) // no note
	if err := r.Insert(ctx, "2026-10-08", []core.FlagEvent{dildo, flag, bottom}); err != nil {
		t.Fatal(err)
	}
	again := dildo
	again.Note = "changed"
	if err := r.Insert(ctx, "2026-10-08", []core.FlagEvent{again}); err != nil {
		t.Fatal(err)
	}
	got, err := r.Day(ctx, "2026-10-08")
	if err != nil {
		t.Fatal(err)
	}
	if want := []core.FlagEvent{dildo, flag, bottom}; !reflect.DeepEqual(got, want) {
		t.Fatalf("notes not round-tripped:\n got %+v\nwant %+v", got, want)
	}
}

// flag_events shipped without the note column. An existing database is
// migrated in place (ensureColumn): its rows keep loading, with an empty note,
// and new rows store theirs.
func TestFlagEventMigrationAddsTheNoteColumn(t *testing.T) {
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
  PRIMARY KEY (sym, ts, kind)
)`,
		`INSERT INTO flag_events(day,sym,ts,kind,label,price,at) VALUES('2026-10-07','OLD',5,'entry','LONG (FLAG)',2.5,6)`,
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
		t.Fatalf("open must migrate the legacy flag_events in place: %v", err)
	}
	defer db.Close()
	r := db.FlagEventRepo()
	old, err := r.Day(ctx, "2026-10-07")
	if err != nil || len(old) != 1 || old[0].Sym != "OLD" || old[0].Note != "" || old[0].Price == nil || *old[0].Price != 2.5 {
		t.Fatalf("legacy row after migration: %+v err=%v", old, err)
	}
	n := fev("NEW", 7, "climax", nil)
	n.Note = "12.6% under the 10-bar high"
	if err := r.Insert(ctx, "2026-10-07", []core.FlagEvent{n}); err != nil {
		t.Fatal(err)
	}
	got, err := r.Day(ctx, "2026-10-07")
	if err != nil || len(got) != 2 || got[1].Note != n.Note {
		t.Fatalf("new row's note after migration: %+v err=%v", got, err)
	}
}
