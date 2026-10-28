package feed

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// assertNoKey fails if err's message, or the URL of any *url.Error still
// reachable through errors.As, carries the key in raw or query-escaped form.
func assertNoKey(t *testing.T, err error, key string) {
	t.Helper()
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	for _, form := range []string{key, url.QueryEscape(key)} {
		if strings.Contains(err.Error(), form) {
			t.Fatalf("error message leaks the API key (%q): %s", form, err)
		}
		var ue *url.Error
		if errors.As(err, &ue) && strings.Contains(ue.URL, form) {
			t.Fatalf("*url.Error.URL reachable via errors.As leaks the API key: %s", ue.URL)
		}
	}
}

// TestRESTErrorsNeverLeakAPIKey: every getJSON failure mode must come back
// with the key scrubbed. Observed live 2026-10-05: a chart.get for ALEC failed
// with `Get "https://api.massive.com/v2/aggs/...&apiKey=<key>": http2: server
// sent GOAWAY ...` and the hub pushed that string to the UI as a toast.
func TestRESTErrorsNeverLeakAPIKey(t *testing.T) {
	// The second key needs query-escaping, so its on-the-wire form differs
	// from the raw value; both must be scrubbed.
	for _, key := range []string{"Zq7sEcReTkEy0123456789abcdefXYZ", "k3y+with/slash=and&amp"} {
		t.Run(key, func(t *testing.T) {
			t.Run("transport GOAWAY on chart fetch", func(t *testing.T) {
				c := New(Config{APIKey: key, RESTBase: "https://api.massive.com", HTTP: &http.Client{
					Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
						return nil, errors.New(`http2: server sent GOAWAY and closed the connection; LastStreamID=1999, ErrCode=NO_ERROR, debug=""`)
					}),
				}})
				_, err := c.IntradayWithPrevDay(context.Background(), "ALEC")
				assertNoKey(t, err, key)
				var ue *url.Error
				if !errors.As(err, &ue) {
					t.Fatalf("want a *url.Error still in the chain, got %T: %v", err, err)
				}
				if !strings.Contains(ue.URL, "/v2/aggs/ticker/ALEC/") || !strings.Contains(ue.URL, "apiKey=REDACTED") {
					t.Fatalf("redacted URL should keep the path and mark the key: %s", ue.URL)
				}
				if !strings.Contains(err.Error(), "GOAWAY") {
					t.Fatalf("underlying cause must survive redaction: %v", err)
				}
			})

			t.Run("transport error echoing the URL", func(t *testing.T) {
				c := New(Config{APIKey: key, RESTBase: "https://api.massive.com", HTTP: &http.Client{
					Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
						return nil, fmt.Errorf("bad Location for %s (key %s)", r.URL, key)
					}),
				}})
				_, err := c.Fundamentals(context.Background(), "ALEC")
				assertNoKey(t, err, key)
			})

			t.Run("connection refused", func(t *testing.T) {
				srv := httptest.NewServer(http.NotFoundHandler())
				base := srv.URL
				srv.Close() // nothing listening any more: a real dial error
				c := New(Config{APIKey: key, RESTBase: base})
				_, err := c.Intraday(context.Background(), "ALEC")
				assertNoKey(t, err, key)
				if !strings.Contains(err.Error(), "apiKey=REDACTED") {
					t.Fatalf("want the URL kept with the key marked REDACTED: %v", err)
				}
			})

			t.Run("request URL fails to parse", func(t *testing.T) {
				c := New(Config{APIKey: key, RESTBase: "https://api.massive.com"})
				var v any
				err := c.getJSON(context.Background(), "https://api.massive.com/v2/\x7fbad", &v)
				assertNoKey(t, err, key)
			})

			t.Run("HTTP status error on a next_url carrying apiKey", func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Error(w, "forbidden: key "+r.URL.Query().Get("apiKey"), http.StatusForbidden)
				}))
				defer srv.Close()
				c := New(Config{APIKey: key, RESTBase: srv.URL})
				var v any
				err := c.getJSON(context.Background(), srv.URL+"/v3/reference/tickers?cursor=abc&apiKey="+url.QueryEscape(key), &v)
				assertNoKey(t, err, key)
				if !IsEntitlementErr(err) {
					t.Fatalf("redaction must keep the *HTTPError status for IsEntitlementErr: %T %v", err, err)
				}
			})

			t.Run("cancellation stays detectable", func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				c := New(Config{APIKey: key, RESTBase: "https://api.massive.com"})
				_, err := c.Intraday(ctx, "ALEC")
				assertNoKey(t, err, key)
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("errors.Is(context.Canceled) must survive redaction: %v", err)
				}
			})
		})
	}
}
