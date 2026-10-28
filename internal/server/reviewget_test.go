package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// reviewFixture is a canned two-day session-stats store for review.get tests.
var reviewFixture = map[string]map[core.Session][]core.SessionSnapshotRow{
	"2026-08-17": {
		core.SessionPost: {
			{Sym: "AAA", Hi: 26, Lo: 17, Close: 25, Vwap: 21, Vol: 500, PrevClose: 10},
			{Sym: "BBB", Hi: 4, Lo: 3, Close: 3.5, Vwap: 3.6, Vol: 1000, PrevClose: 3},
		},
	},
	"2026-08-14": {
		core.SessionPre: {
			{Sym: "CCC", Hi: 9, Lo: 8, Close: 8.5, Vwap: 8.4, Vol: 77, PrevClose: 8},
		},
	},
}

func reviewTestConfig(fs *fakeScanner) Config {
	return Config{
		Scanner: fs,
		ReviewDates: func(context.Context) ([]string, error) {
			return []string{"2026-08-17", "2026-08-14"}, nil // newest first, the repo contract
		},
		ReviewLoad: func(_ context.Context, date string, sess core.Session) ([]core.SessionSnapshotRow, error) {
			return reviewFixture[date][sess], nil // unknown pair: nil, nil — the repo contract
		},
	}
}

// TestServerReviewGetTwoClients drives review.get end-to-end over two real WS
// clients on one server: the first sends an EMPTY request and must get the
// newest date's after-hours ("post") rows — the wake-up-and-review default —
// while the second asks for an explicit older date + session and must get
// exactly that. Both replies always carry the full date-picker list.
func TestServerReviewGetTwoClients(t *testing.T) {
	fs := newFake()
	srv := New(reviewTestConfig(fs))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	c1 := dialWS(t, ctx, ts)
	defer c1.CloseNow()
	c2 := dialWS(t, ctx, ts)
	defer c2.CloseNow()

	// Client 1: empty request -> newest date, "post" session by default.
	writeFrame(t, ctx, c1, core.TReviewGet, core.ReviewGetMsg{})
	f := readUntil(t, ctx, c1, core.TReviewData)
	var got core.ReviewDataMsg
	if err := json.Unmarshal(f.D, &got); err != nil {
		t.Fatalf("review.data unmarshal: %v", err)
	}
	if got.Date != "2026-08-17" || got.Session != "post" {
		t.Fatalf("empty request must resolve newest date + post session, got %s/%s", got.Date, got.Session)
	}
	if len(got.Dates) != 2 || got.Dates[0] != "2026-08-17" || got.Dates[1] != "2026-08-14" {
		t.Fatalf("dates picker list mismatch: %v", got.Dates)
	}
	if len(got.Rows) != 2 || got.Rows[0].Sym != "AAA" || got.Rows[0].Close != 25 ||
		got.Rows[0].PrevClose != 10 || got.Rows[1].Sym != "BBB" || got.Rows[1].Vol != 1000 {
		t.Fatalf("raw rows lost in transit: %+v", got.Rows)
	}

	// Client 2: explicit older date + session honored verbatim.
	writeFrame(t, ctx, c2, core.TReviewGet, core.ReviewGetMsg{Date: "2026-08-14", Session: "pre"})
	f = readUntil(t, ctx, c2, core.TReviewData)
	var got2 core.ReviewDataMsg
	if err := json.Unmarshal(f.D, &got2); err != nil {
		t.Fatalf("review.data 2 unmarshal: %v", err)
	}
	if got2.Date != "2026-08-14" || got2.Session != "pre" {
		t.Fatalf("explicit date+session must be honored, got %s/%s", got2.Date, got2.Session)
	}
	if len(got2.Rows) != 1 || got2.Rows[0].Sym != "CCC" || got2.Rows[0].Vwap != 8.4 {
		t.Fatalf("explicit-date rows mismatch: %+v", got2.Rows)
	}
	if len(got2.Dates) != 2 {
		t.Fatalf("picker list must always ride along: %v", got2.Dates)
	}
}

// TestServerReviewGetDayWithNoData proves a date/session pair the scanner
// never observed answers honestly: an empty rows array (never fabricated
// values, never an error) with the resolved date echoed back.
func TestServerReviewGetDayWithNoData(t *testing.T) {
	fs := newFake()
	srv := New(reviewTestConfig(fs))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn := dialWS(t, ctx, ts)
	defer conn.CloseNow()

	// 2026-08-14 has a pre snapshot but NO post one (holiday-shaped gap).
	writeFrame(t, ctx, conn, core.TReviewGet, core.ReviewGetMsg{Date: "2026-08-14", Session: "post"})
	f := readUntil(t, ctx, conn, core.TReviewData)
	var got core.ReviewDataMsg
	if err := json.Unmarshal(f.D, &got); err != nil {
		t.Fatalf("review.data unmarshal: %v", err)
	}
	if got.Date != "2026-08-14" || got.Session != "post" || got.Rows == nil || len(got.Rows) != 0 {
		t.Fatalf("missing session must answer empty rows honestly: %+v", got)
	}
}

// TestServerReviewGetNoHistory proves a store with no snapshots at all answers
// with an empty date and empty lists — the client renders "no data yet", never
// a fabricated day.
func TestServerReviewGetNoHistory(t *testing.T) {
	fs := newFake()
	srv := New(Config{
		Scanner:     fs,
		ReviewDates: func(context.Context) ([]string, error) { return nil, nil },
		ReviewLoad: func(context.Context, string, core.Session) ([]core.SessionSnapshotRow, error) {
			t.Error("ReviewLoad must not be called when no dates exist")
			return nil, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn := dialWS(t, ctx, ts)
	defer conn.CloseNow()

	writeFrame(t, ctx, conn, core.TReviewGet, core.ReviewGetMsg{})
	f := readUntil(t, ctx, conn, core.TReviewData)
	var got core.ReviewDataMsg
	if err := json.Unmarshal(f.D, &got); err != nil {
		t.Fatalf("review.data unmarshal: %v", err)
	}
	if got.Date != "" || len(got.Dates) != 0 || len(got.Rows) != 0 || got.Dates == nil || got.Rows == nil {
		t.Fatalf("no history must answer empty (but non-null) lists: %+v", got)
	}
}

// TestServerReviewGetErrorMapsReviewCode proves a failing load maps to the
// standard "review" error code on the requesting client.
func TestServerReviewGetErrorMapsReviewCode(t *testing.T) {
	fs := newFake()
	srv := New(Config{
		Scanner:     fs,
		ReviewDates: func(context.Context) ([]string, error) { return []string{"2026-08-17"}, nil },
		ReviewLoad: func(context.Context, string, core.Session) ([]core.SessionSnapshotRow, error) {
			return nil, errors.New("disk on fire")
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn := dialWS(t, ctx, ts)
	defer conn.CloseNow()

	writeFrame(t, ctx, conn, core.TReviewGet, core.ReviewGetMsg{})
	e := readUntil(t, ctx, conn, core.TError)
	var em core.ErrorMsg
	if err := json.Unmarshal(e.D, &em); err != nil || em.Code != "review" {
		t.Fatalf("want error code \"review\", got %+v err=%v", em, err)
	}
}

// TestServerReviewGetUnknownSessionErrors proves a bogus session label is a
// "review" error, not a silent default.
func TestServerReviewGetUnknownSessionErrors(t *testing.T) {
	fs := newFake()
	srv := New(reviewTestConfig(fs))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn := dialWS(t, ctx, ts)
	defer conn.CloseNow()

	writeFrame(t, ctx, conn, core.TReviewGet, core.ReviewGetMsg{Session: "lunch"})
	e := readUntil(t, ctx, conn, core.TError)
	var em core.ErrorMsg
	if err := json.Unmarshal(e.D, &em); err != nil || em.Code != "review" {
		t.Fatalf("want error code \"review\" for unknown session, got %+v err=%v", em, err)
	}
}
