package feed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestPrevTradingDaySkipsWeekend is table-driven over a full week: a midweek
// date just steps back one calendar day, but Monday's previous trading day
// must be the prior Friday (skipping Sunday AND Saturday), not Sunday.
func TestPrevTradingDaySkipsWeekend(t *testing.T) {
	cases := []struct {
		name string
		in   string // YYYY-MM-DD
		want string // YYYY-MM-DD
	}{
		{"tuesday-to-monday", "2026-08-25", "2026-08-24"},    // Tue -> Mon (midweek, no skip)
		{"wednesday-to-tuesday", "2026-08-26", "2026-08-25"}, // Wed -> Tue
		{"monday-to-friday", "2026-08-24", "2026-08-21"},     // Mon -> prior Fri, NOT Sunday
		{"sunday-to-friday", "2026-08-23", "2026-08-21"},     // Sun -> Fri (2 calendar days back)
		{"saturday-to-friday", "2026-08-22", "2026-08-21"},   // Sat -> Fri
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, err := time.Parse("2006-01-02", tc.in)
			if err != nil {
				t.Fatalf("bad fixture date %q: %v", tc.in, err)
			}
			got := prevTradingDay(in).Format("2006-01-02")
			if got != tc.want {
				t.Fatalf("prevTradingDay(%s) = %s, want %s (in was a %s)",
					tc.in, got, tc.want, in.Weekday())
			}
		})
	}
}

// TestIntradayWithPrevDayRequestsRange proves the aggregates request spans
// [previous trading day, today] via the SAME endpoint's independent from/to
// date params — computed here the same way production code does (today in
// America/New_York, stepped back skipping the weekend), so the test is
// correct on whatever real day it happens to run.
func TestIntradayWithPrevDayRequestsRange(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.UTC
	}
	today := time.Now().In(loc)
	wantTo := today.Format("2006-01-02")
	wantFrom := prevTradingDay(today).Format("2006-01-02")

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"results":[
			{"t":1700000000000,"o":10,"h":11,"l":9,"c":10.5,"v":100},
			{"t":1700086400000,"o":20,"h":21,"l":19,"c":20.5,"v":200}
		]}`))
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", RESTBase: srv.URL})
	bars, err := c.IntradayWithPrevDay(context.Background(), "AAPL")
	if err != nil {
		t.Fatalf("IntradayWithPrevDay: %v", err)
	}

	wantPath := "/v2/aggs/ticker/AAPL/range/1/minute/" + wantFrom + "/" + wantTo
	if gotPath != wantPath {
		t.Fatalf("request path = %q, want %q", gotPath, wantPath)
	}
	if wantFrom == wantTo {
		t.Fatalf("from/to collapsed to the same date (%s) — prevTradingDay must always step back at least one day", wantFrom)
	}
	if !strings.Contains(wantPath, wantFrom) {
		t.Fatalf("sanity: wantPath %q should contain wantFrom %q", wantPath, wantFrom)
	}

	// The two days' bars decode through the SAME shape (T is absolute epoch
	// ms), sorted asc by the endpoint (sort=asc) with zero special-casing.
	if len(bars) != 2 {
		t.Fatalf("want 2 bars (one per day), got %d: %+v", len(bars), bars)
	}
	if bars[0].T != 1700000000000 || bars[1].T != 1700086400000 || bars[1].T <= bars[0].T {
		t.Fatalf("bars must arrive sorted asc across the multi-day range: %+v", bars)
	}
}

// TestIntradayStillRequestsTodayOnly pins Intraday's existing today-only
// contract (from==to) unchanged: internal/prefill's windowStats has zero date
// filtering, so a second calendar day of bars here would silently double-
// count prefill's frozen SessionStats ("prefill date blindness" — see
// internal/prefill/prefill.go). This is the regression guard that would fail
// if Intraday were ever widened instead of adding IntradayWithPrevDay.
func TestIntradayStillRequestsTodayOnly(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.UTC
	}
	wantDate := time.Now().In(loc).Format("2006-01-02")

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", RESTBase: srv.URL})
	if _, err := c.Intraday(context.Background(), "AAPL"); err != nil {
		t.Fatalf("Intraday: %v", err)
	}
	wantPath := "/v2/aggs/ticker/AAPL/range/1/minute/" + wantDate + "/" + wantDate
	if gotPath != wantPath {
		t.Fatalf("Intraday must stay today-only: request path = %q, want %q", gotPath, wantPath)
	}
}

// Intraday keeps each minute bar's vw (Engine.SeedScopeVwap's input).
func TestIntradayKeepsBarVwap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"v":1200.5,"vw":80.7931,"o":80.5,"c":80.9,"h":81,"l":80.4,"t":1761667200000,"n":42}]}`))
	}))
	defer srv.Close()
	c := New(Config{APIKey: "k", RESTBase: srv.URL})
	bars, err := c.Intraday(context.Background(), "APH")
	if err != nil {
		t.Fatalf("Intraday: %v", err)
	}
	if len(bars) != 1 || bars[0].VW != 80.7931 || bars[0].V != 1200 || bars[0].C != 80.9 {
		t.Fatalf("bars %+v", bars)
	}
}
