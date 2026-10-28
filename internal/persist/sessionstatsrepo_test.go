package persist

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// TestSessionStatsRoundTrip proves a saved snapshot loads back exactly (raw
// values, symbol order), that another session of the SAME date lives alongside
// it, and that an unknown (date, session) answers nil/nil rather than erroring.
func TestSessionStatsRoundTrip(t *testing.T) {
	ctx := context.Background()
	r := openTest(t).SessionStatsRepo()

	// The three baselines a review consumer might confuse are all distinct on
	// purpose: prevClose 10, the regular close 20 (saved under the same date's
	// "regular" snapshot), and the post session's own first print (17, only
	// implied by Lo here). The stored post Close is 25 — the after-hours gain
	// derived at read time must use (25-20)/20 from the SIBLING regular row,
	// never (25-10)/10 (day change) nor (25-17)/17 (vs the post's first print).
	post := core.SessionSnapshot{
		Date:    "2026-08-17",
		Session: core.SessionPost,
		Rows: []core.SessionSnapshotRow{
			{Sym: "BBB", Hi: 4, Lo: 3, Close: 3.5, Vwap: 3.6, Vol: 1000, PrevClose: 3},
			{Sym: "AAA", Hi: 26, Lo: 17, Close: 25, Vwap: 22.5, Vol: 123456, PrevClose: 10},
		},
	}
	reg := core.SessionSnapshot{
		Date:    "2026-08-17",
		Session: core.SessionRegular,
		Rows: []core.SessionSnapshotRow{
			{Sym: "AAA", Hi: 21, Lo: 15, Close: 20, Vwap: 18, Vol: 999999, PrevClose: 10},
		},
	}
	if err := r.SaveSnapshot(ctx, post); err != nil {
		t.Fatalf("save post: %v", err)
	}
	if err := r.SaveSnapshot(ctx, reg); err != nil {
		t.Fatalf("save regular: %v", err)
	}

	got, err := r.Load(ctx, "2026-08-17", core.SessionPost)
	if err != nil {
		t.Fatalf("load post: %v", err)
	}
	want := []core.SessionSnapshotRow{ // ordered by sym, not insertion order
		{Sym: "AAA", Hi: 26, Lo: 17, Close: 25, Vwap: 22.5, Vol: 123456, PrevClose: 10},
		{Sym: "BBB", Hi: 4, Lo: 3, Close: 3.5, Vwap: 3.6, Vol: 1000, PrevClose: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("post round trip mismatch:\n got %+v\nwant %+v", got, want)
	}

	gotReg, err := r.Load(ctx, "2026-08-17", core.SessionRegular)
	if err != nil || len(gotReg) != 1 || !reflect.DeepEqual(gotReg[0], reg.Rows[0]) {
		t.Fatalf("regular row of the same date mismatch: %+v err=%v", gotReg, err)
	}
	// The read-time after-hours gain baseline: the sibling regular close.
	if gotReg[0].Close != 20 || got[0].Close != 25 || got[0].PrevClose != 10 {
		t.Fatalf("baseline values drifted: rth close %v, post close %v, prevClose %v",
			gotReg[0].Close, got[0].Close, got[0].PrevClose)
	}

	if rows, err := r.Load(ctx, "2026-08-16", core.SessionPost); err != nil || rows != nil {
		t.Fatalf("unknown date must be nil/nil, got %+v err=%v", rows, err)
	}
	if rows, err := r.Load(ctx, "2026-08-17", core.SessionPre); err != nil || rows != nil {
		t.Fatalf("unknown session must be nil/nil, got %+v err=%v", rows, err)
	}
}

// TestSessionStatsResaveIsIdempotent proves a re-freeze of the SAME boundary
// replaces the row set wholesale: symbols present in the old save but absent
// from the new one are gone, values of re-listed symbols are the new ones, and
// other (date, session) keys are untouched.
func TestSessionStatsResaveIsIdempotent(t *testing.T) {
	ctx := context.Background()
	r := openTest(t).SessionStatsRepo()

	first := core.SessionSnapshot{
		Date:    "2026-08-17",
		Session: core.SessionPost,
		Rows: []core.SessionSnapshotRow{
			{Sym: "AAA", Hi: 26, Lo: 17, Close: 25, Vwap: 22.5, Vol: 100, PrevClose: 10},
			{Sym: "GONE", Hi: 2, Lo: 1, Close: 1.5, Vwap: 1.4, Vol: 50, PrevClose: 1},
		},
	}
	other := core.SessionSnapshot{
		Date:    "2026-08-17",
		Session: core.SessionPre,
		Rows:    []core.SessionSnapshotRow{{Sym: "KEEP", Hi: 9, Lo: 8, Close: 8.5, Vwap: 8.4, Vol: 77, PrevClose: 8}},
	}
	if err := r.SaveSnapshot(ctx, first); err != nil {
		t.Fatalf("save first: %v", err)
	}
	if err := r.SaveSnapshot(ctx, other); err != nil {
		t.Fatalf("save other session: %v", err)
	}

	resave := core.SessionSnapshot{
		Date:    "2026-08-17",
		Session: core.SessionPost,
		Rows:    []core.SessionSnapshotRow{{Sym: "AAA", Hi: 27, Lo: 16, Close: 24, Vwap: 22, Vol: 200, PrevClose: 10}},
	}
	if err := r.SaveSnapshot(ctx, resave); err != nil {
		t.Fatalf("resave: %v", err)
	}

	got, err := r.Load(ctx, "2026-08-17", core.SessionPost)
	if err != nil || !reflect.DeepEqual(got, resave.Rows) {
		t.Fatalf("resave must replace wholesale (GONE gone, AAA updated): %+v err=%v", got, err)
	}
	keep, err := r.Load(ctx, "2026-08-17", core.SessionPre)
	if err != nil || !reflect.DeepEqual(keep, other.Rows) {
		t.Fatalf("resave of one session must not touch another: %+v err=%v", keep, err)
	}
	// The date appears once in the picker despite three saves.
	if dates, err := r.Dates(ctx); err != nil || !reflect.DeepEqual(dates, []string{"2026-08-17"}) {
		t.Fatalf("dates after resave: %v err=%v", dates, err)
	}
}

// TestSessionStatsDatesOrderAndPrune proves Dates lists distinct dates newest
// first and PruneBefore keeps exactly the newest N distinct dates (default 10
// when passed <= 0), deleting every row of the older ones.
func TestSessionStatsDatesOrderAndPrune(t *testing.T) {
	ctx := context.Background()
	r := openTest(t).SessionStatsRepo()

	// 12 dates saved deliberately out of chronological order.
	days := []int{5, 1, 12, 3, 9, 2, 11, 7, 4, 10, 6, 8}
	for _, d := range days {
		snap := core.SessionSnapshot{
			Date:    fmt.Sprintf("2026-08-%02d", d),
			Session: core.SessionPost,
			Rows:    []core.SessionSnapshotRow{{Sym: "AAA", Close: float64(d), PrevClose: 1}},
		}
		if err := r.SaveSnapshot(ctx, snap); err != nil {
			t.Fatalf("save day %d: %v", d, err)
		}
	}

	dates, err := r.Dates(ctx)
	if err != nil {
		t.Fatalf("dates: %v", err)
	}
	if len(dates) != 12 || dates[0] != "2026-08-12" || dates[11] != "2026-08-01" {
		t.Fatalf("dates must be distinct + newest first: %v", dates)
	}
	for i := 1; i < len(dates); i++ {
		if dates[i-1] <= dates[i] {
			t.Fatalf("dates not strictly descending at %d: %v", i, dates)
		}
	}

	// Default prune (<= 0 -> 10): the two oldest dates go.
	deleted, err := r.PruneBefore(ctx, 0)
	if err != nil || deleted != 2 {
		t.Fatalf("default prune want 2 deleted rows, got %d err=%v", deleted, err)
	}
	dates, _ = r.Dates(ctx)
	if len(dates) != 10 || dates[9] != "2026-08-03" {
		t.Fatalf("default prune must keep the newest 10: %v", dates)
	}

	// Explicit prune to 3.
	if deleted, err = r.PruneBefore(ctx, 3); err != nil || deleted != 7 {
		t.Fatalf("prune to 3 want 7 deleted rows, got %d err=%v", deleted, err)
	}
	dates, _ = r.Dates(ctx)
	if !reflect.DeepEqual(dates, []string{"2026-08-12", "2026-08-11", "2026-08-10"}) {
		t.Fatalf("prune to 3 kept wrong dates: %v", dates)
	}
	// The kept dates' rows still load.
	if rows, err := r.Load(ctx, "2026-08-12", core.SessionPost); err != nil || len(rows) != 1 || rows[0].Close != 12 {
		t.Fatalf("kept date lost its rows: %+v err=%v", rows, err)
	}
}

// TestSessionStatsBulkSaveIsFast proves the whole-market write path (one
// prepared-statement transaction) lands ~11k rows well under a second — the
// boundary writer must never hold the engine's channel consumer for long.
func TestSessionStatsBulkSaveIsFast(t *testing.T) {
	ctx := context.Background()
	r := openTest(t).SessionStatsRepo()

	rows := make([]core.SessionSnapshotRow, 0, 11000)
	for i := 0; i < 11000; i++ {
		rows = append(rows, core.SessionSnapshotRow{
			Sym: fmt.Sprintf("S%05d", i), Hi: 11, Lo: 9, Close: 10.5, Vwap: 10.2,
			Vol: int64(i), PrevClose: 10,
		})
	}
	snap := core.SessionSnapshot{Date: "2026-08-17", Session: core.SessionPost, Rows: rows}

	startAt := time.Now()
	if err := r.SaveSnapshot(ctx, snap); err != nil {
		t.Fatalf("bulk save: %v", err)
	}
	if elapsed := time.Since(startAt); elapsed > sessionStatsBulkBudget {
		t.Fatalf("bulk save of 11k rows took %v, must be well under %v", elapsed, sessionStatsBulkBudget)
	}
	got, err := r.Load(ctx, "2026-08-17", core.SessionPost)
	if err != nil || len(got) != 11000 {
		t.Fatalf("bulk load want 11000 rows, got %d err=%v", len(got), err)
	}
}
