package feed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Headlines below are real strings observed from the live Benzinga feed, which
// sends plain-text fields HTML-escaped.
func TestBenzingaDecodesEntitiesAndMapsFields(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		_, _ = w.Write([]byte(`{"results":[
		  {"benzinga_id":48123456,
		   "title":"Stock Market Today: S&amp;P 500, Dow Jones Futures Rise",
		   "teaser":"Big Tech&#39;s rally &amp; more",
		   "body":"<p>Full <b>article</b> body &amp; details</p>",
		   "url":"https://www.benzinga.com/news/1",
		   "published":"2026-07-27T12:31:54Z",
		   "tickers":["AAPL","MSFT"],
		   "channels":["News"]},
		  {"benzinga_id":48123457,"title":"Plain headline","url":"https://www.benzinga.com/news/2",
		   "published":"2026-07-27T12:00:00Z","tickers":["KUST"]}
		]}`))
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", RESTBase: srv.URL})
	since := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC).UnixMilli()
	items, bodies, err := c.BenzingaNews(context.Background(), since)
	if err != nil {
		t.Fatalf("benzinga: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items got %d", len(items))
	}

	a := items[0]
	if a.Headline != "Stock Market Today: S&P 500, Dow Jones Futures Rise" {
		t.Fatalf("headline entities not decoded: %q", a.Headline)
	}
	if a.Teaser != "Big Tech's rally & more" {
		t.Fatalf("teaser entities not decoded: %q", a.Teaser)
	}
	if a.ID != "bz48123456" {
		t.Fatalf("id mapping: %q", a.ID)
	}
	if !a.HasBody || bodies[a.ID] == "" {
		t.Fatalf("body not captured: hasBody=%v", a.HasBody)
	}
	// Bodies stay raw HTML — the summarizer strips + unescapes them itself.
	if !strings.Contains(bodies[a.ID], "<b>article</b>") {
		t.Fatalf("body should remain raw HTML: %q", bodies[a.ID])
	}
	if a.Source != "Benzinga" || len(a.Tickers) != 2 || a.TS <= 0 {
		t.Fatalf("field mapping: %+v", a)
	}
	if items[1].Headline != "Plain headline" {
		t.Fatalf("entity-free headline altered: %q", items[1].Headline)
	}

	// Whole-market fetch must not carry a tickers= filter.
	if strings.Contains(gotURL, "tickers=") {
		t.Fatalf("unexpected ticker filter on the broad poll: %s", gotURL)
	}
	if !strings.Contains(gotURL, "published.gte=") || !strings.Contains(gotURL, "sort=published.desc") {
		t.Fatalf("missing since/sort params: %s", gotURL)
	}
}

func TestBenzingaForSymbolAddsTickerFilter(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", RESTBase: srv.URL})
	if _, _, err := c.BenzingaNewsFor(context.Background(), "ABCD", 0); err != nil {
		t.Fatalf("benzingaFor: %v", err)
	}
	if !strings.Contains(gotURL, "tickers=ABCD") {
		t.Fatalf("per-symbol fetch must filter by ticker: %s", gotURL)
	}
}
