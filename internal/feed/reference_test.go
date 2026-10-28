package feed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReferencePaginationAndDedupe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		ty, cursor := q.Get("type"), q.Get("cursor")
		switch {
		case ty == "CS" && cursor == "":
			next := "http://" + r.Host + "/v3/reference/tickers?type=CS&cursor=c2"
			_, _ = w.Write([]byte(`{"results":[{"ticker":"AAPL","type":"CS","active":true}],"next_url":"` + next + `"}`))
		case ty == "CS" && cursor == "c2":
			_, _ = w.Write([]byte(`{"results":[{"ticker":"MSFT","type":"CS","active":true}]}`))
		default: // ETF, ADRC: empty
			_, _ = w.Write([]byte(`{"results":[]}`))
		}
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", RESTBase: srv.URL})
	tks, err := c.Reference(context.Background())
	if err != nil {
		t.Fatalf("reference: %v", err)
	}
	got := map[string]bool{}
	for _, tk := range tks {
		got[tk.Symbol] = true
	}
	if len(tks) != 2 || !got["AAPL"] || !got["MSFT"] {
		t.Fatalf("pagination result mismatch: %+v", tks)
	}
}
