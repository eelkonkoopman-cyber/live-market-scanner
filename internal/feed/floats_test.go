package feed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFloatsParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/AAPL"):
			_, _ = w.Write([]byte(`{"status":"OK","results":{"ticker":"AAPL","share_class_shares_outstanding":15000000000,"weighted_shares_outstanding":15100000000}}`))
		case strings.HasSuffix(r.URL.Path, "/NOSH"):
			_, _ = w.Write([]byte(`{"status":"OK","results":{"ticker":"NOSH","weighted_shares_outstanding":42}}`))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", RESTBase: srv.URL})
	refs, err := c.Floats(context.Background(), []string{"AAPL", "NOSH", "BAD"})
	if err != nil {
		t.Fatalf("floats: %v", err)
	}
	m := map[string]*int64{}
	for _, r := range refs {
		m[r.Ticker] = r.SharesOut
	}
	if m["AAPL"] == nil || *m["AAPL"] != 15000000000 {
		t.Fatalf("AAPL share_class_shares_outstanding wrong: %v", m["AAPL"])
	}
	if m["NOSH"] == nil || *m["NOSH"] != 42 { // falls back to weighted
		t.Fatalf("NOSH should fall back to weighted_shares_outstanding: %v", m["NOSH"])
	}
	if _, ok := m["BAD"]; ok {
		t.Fatal("BAD (404) should be skipped, not included")
	}
}
