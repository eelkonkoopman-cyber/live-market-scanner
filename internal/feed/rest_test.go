package feed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestGroupedDailyParsesRealShape pins the ACTUAL live response shape
// (captured via a real GET against the real endpoint with a live key, then
// round-tripped through this exact decoder) — not a hand-typed guess. This
// regression exists because the real bug slipped past a manual byte-eyeball
// check: every row carries BOTH "T" (ticker, string) and "t" (bar-start epoch
// ms, a number). groupedBar originally declared a field only for "T"; Go's
// encoding/json matches an undeclared JSON key case-INSENSITIVELY against
// whatever field IS declared, so "t" silently collided with the "T"-tagged
// field and every single call failed to decode — for the app's ENTIRE first
// session with this feature, with each failure just logging a Warn and
// moving to the next date, so daily_close_history stayed empty with no error
// visible anywhere in the running app.
//
// It now also pins the o/h/l/v widening against the SAME real capture, because
// those keys carry two more traps of exactly the same family: "v" arrives
// FRACTIONAL (868021.675123), so an int64-typed volume field would fail the
// whole response, and "vw"/"n" must stay undeclared without case-folding onto
// any tag we do declare.
func TestGroupedDailyParsesRealShape(t *testing.T) {
	const realCapture = `{"queryCount":12402,"resultsCount":12402,"adjusted":true,"results":[` +
		`{"T":"BFAM","v":868021.675123,"vw":78.2536,"o":76.32,"c":77.85,"h":79.25,"l":76.32,"t":1785182400000,"n":16935},` +
		`{"T":"VSDA","v":1658.101669,"vw":59.2423,"o":58.88,"c":59.3824,"h":59.3824,"l":58.88,"t":1785182400000,"n":95}` +
		`]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(realCapture))
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", RESTBase: srv.URL})
	bars, err := c.GroupedDaily(context.Background(), "2026-07-27")
	if err != nil {
		t.Fatalf("GroupedDaily must decode the real wire shape (both T and t present): %v", err)
	}
	if len(bars) != 2 {
		t.Fatalf("want 2 bars, got %d: %+v", len(bars), bars)
	}
	// The ticker itself — the exact field "t" clobbered. Assert the keys are
	// the real symbols and that no row landed under an empty/garbage key,
	// which is what a "T"/"t" collision degrades to if it ever stops erroring.
	for _, want := range []string{"BFAM", "VSDA"} {
		if _, ok := bars[want]; !ok {
			t.Fatalf("ticker %q must decode from \"T\", unclobbered by \"t\"; got keys %+v", want, bars)
		}
	}
	// Volume truncates from the wire's fractional share count (868021.675123).
	if got := bars["BFAM"]; got.Date != "2026-07-27" || got.O != 76.32 || got.H != 79.25 ||
		got.L != 76.32 || got.C != 77.85 || got.V != 868021 {
		t.Fatalf("BFAM bar mismatch: %+v", got)
	}
	if got := bars["VSDA"]; got.Date != "2026-07-27" || got.O != 58.88 || got.H != 59.3824 ||
		got.L != 58.88 || got.C != 59.3824 || got.V != 1658 {
		t.Fatalf("VSDA bar mismatch: %+v", got)
	}
}

// A holiday (or a date with genuinely zero results) must decode to an empty
// map, not an error — the scheduler relies on this to skip the date quietly.
func TestGroupedDailyEmptyResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"queryCount":0,"resultsCount":0,"adjusted":true,"results":[]}`))
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", RESTBase: srv.URL})
	bars, err := c.GroupedDaily(context.Background(), "2026-07-04")
	if err != nil {
		t.Fatalf("empty results must not error: %v", err)
	}
	if len(bars) != 0 {
		t.Fatalf("want empty map, got %+v", bars)
	}
}

func TestSnapshotParse(t *testing.T) {
	body := `{"status":"OK","tickers":[
	  {"ticker":"AAPL",
	   "day":{"o":150,"h":152,"l":149,"c":151,"v":1000000,"vw":150.8},
	   "min":{"av":1200000,"o":151,"h":151.2,"l":150.8,"c":151.1,"v":5000,"vw":151,"t":1700000000000},
	   "prevDay":{"o":148,"h":150,"l":147,"c":149,"v":900000,"vw":148.5},
	   "updated":1700000000000000000}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/snapshot/locale/us/markets/stocks/tickers" {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", RESTBase: srv.URL})
	seeds, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(seeds) != 1 {
		t.Fatalf("want 1 seed got %d", len(seeds))
	}
	s := seeds[0]
	if s.Sym != "AAPL" || s.Close != 151 || s.Vol != 1000000 ||
		s.PrevClose != 149 || s.DayOpen != 150 || s.High != 152 || s.Low != 149 {
		t.Fatalf("seed mismatch: %+v", s)
	}
}

func TestSnapshotHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()
	c := New(Config{APIKey: "bad", RESTBase: srv.URL})
	if _, err := c.Snapshot(context.Background()); err == nil {
		t.Fatal("expected error on HTTP 403")
	}
}

func TestNewsSince(t *testing.T) {
	body := `{"results":[
	  {"id":"a1","title":"Big news","article_url":"https://x/1","published_utc":"2026-06-24T13:00:00Z","tickers":["AAPL"],"publisher":{"name":"Wire"},"insights":[{"ticker":"AAPL","sentiment":"positive"}]},
	  {"id":"a2","title":"Old news","article_url":"https://x/2","published_utc":"2026-06-24T11:00:00Z","tickers":["MSFT"],"publisher":{"name":"Wire"}}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", RESTBase: srv.URL})
	since := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC).UnixMilli()
	items, err := c.NewsSince(context.Background(), since)
	if err != nil {
		t.Fatalf("news: %v", err)
	}
	if len(items) != 1 { // a2 (11:00) filtered out by the since guard
		t.Fatalf("want 1 item after filter, got %d", len(items))
	}
	it := items[0]
	if it.ID != "a1" || it.Headline != "Big news" || it.Source != "Wire" ||
		it.Sentiment != "positive" || len(it.Tickers) != 1 || it.Tickers[0] != "AAPL" {
		t.Fatalf("news item mismatch: %+v", it)
	}
}
