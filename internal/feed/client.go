// Package feed implements core.FeedClient against the Massive (ex-Polygon) API:
// a WebSocket stream of per-second aggregates and REST snapshot/news endpoints.
// It depends only on core (and stdlib + the websocket lib); it knows nothing of
// the engine or scanner.
package feed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

const (
	defaultWSURL    = "wss://socket.massive.com/stocks" // legacy wss://socket.polygon.io/stocks also works
	defaultRESTBase = "https://api.massive.com"
	subTopic        = "A.*" // whole-market per-second aggregates
)

// errFatal marks a non-retryable failure (bad key / forbidden subscription).
var errFatal = errors.New("fatal feed error")

// Config configures the client. Zero values fall back to Massive defaults.
type Config struct {
	APIKey   string
	WSURL    string
	RESTBase string
	HTTP     *http.Client
	// TapeSink receives trade/quote events for the currently watched symbol
	// (see WatchTape). Optional; nil disables tape decoding.
	TapeSink chan<- core.TapeEvent
	// TapeStatus reports tape subscription problems (e.g. the plan lacks the
	// trades channel) without disturbing the aggregate stream.
	TapeStatus func(msg string)
}

// Client is the Massive feed client. The API key is held atomically so it can be
// set/changed at runtime (e.g. from the UI settings panel).
type Client struct {
	keyp     atomic.Pointer[string]
	wsURL    string
	restBase string
	http     *http.Client

	tapeSink   chan<- core.TapeEvent
	tapeStatus func(msg string)
	tapeSyms   atomic.Pointer[map[string]struct{}] // desired watched symbol set (nil/empty = none)
	connp      atomic.Pointer[wsConnRef]           // live connection, for mid-stream (un)subscribe
}

var _ core.FeedClient = (*Client)(nil)

func New(cfg Config) *Client {
	c := &Client{
		wsURL: cfg.WSURL, restBase: cfg.RESTBase, http: cfg.HTTP,
		tapeSink: cfg.TapeSink, tapeStatus: cfg.TapeStatus,
	}
	c.SetKey(cfg.APIKey)
	if c.wsURL == "" {
		c.wsURL = defaultWSURL
	}
	if c.restBase == "" {
		c.restBase = defaultRESTBase
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 30 * time.Second}
	}
	return c
}

// SetKey updates the API key (used by all subsequent REST/WS calls).
func (c *Client) SetKey(k string) { c.keyp.Store(&k) }

// HasKey reports whether a non-empty key is set.
func (c *Client) HasKey() bool { return c.key() != "" }

func (c *Client) key() string {
	if p := c.keyp.Load(); p != nil {
		return *p
	}
	return ""
}

// getJSON performs an authenticated GET and decodes the JSON body into v. The
// API key is sent as the apiKey query param (Massive/Polygon convention).
//
// Every error it returns has the key scrubbed out: net/http's *url.Error
// embeds the full request URL, and these messages travel on to logs and to the
// UI (chart.get / fundamentals.get push err.Error() to the client as a toast).
func (c *Client) getJSON(ctx context.Context, rawURL string, v any) error {
	key := c.key() // one read, so the key we send is the key we redact
	return redactKey(c.doGetJSON(ctx, rawURL, key, v), key)
}

func (c *Client) doGetJSON(ctx context.Context, rawURL, key string, v any) error {
	sep := "?"
	if strings.Contains(rawURL, "?") {
		sep = "&"
	}
	full := rawURL + sep + "apiKey=" + url.QueryEscape(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode != http.StatusOK {
		return &HTTPError{Status: resp.StatusCode, Msg: fmt.Sprintf("GET %s: HTTP %d: %s", rawURL, resp.StatusCode, snippet(body))}
	}
	return json.Unmarshal(body, v)
}

// apiKeyParam matches an apiKey query param's value wherever it appears in a
// message (request URL, redirect target, echoed Location header).
var apiKeyParam = regexp.MustCompile(`(?i)([?&]apikey=)[^&#\s"']*`)

// redactKeyText replaces the API key in s with "REDACTED": first by query
// param name (independent of the key's value), then any bare occurrence of
// the key itself, raw or query-escaped.
func redactKeyText(s, key string) string {
	s = apiKeyParam.ReplaceAllString(s, "${1}REDACTED")
	if key != "" {
		s = strings.ReplaceAll(s, key, "REDACTED")
		if esc := url.QueryEscape(key); esc != key {
			s = strings.ReplaceAll(s, esc, "REDACTED")
		}
	}
	return s
}

// redactKey returns err with the API key scrubbed from its message. A
// *url.Error is rebuilt (not wrapped) around a redacted URL, so errors.As
// can't reach the original while Timeout() and errors.Is(context.Canceled)
// still work; an *HTTPError keeps its status for IsEntitlementErr /
// IsRateLimitErr. Any other error whose text carries the key is flattened to
// a plain error, leaving no unwrap path back to it.
func redactKey(err error, key string) error {
	switch e := err.(type) {
	case nil:
		return nil
	case *url.Error:
		return &url.Error{Op: e.Op, URL: redactKeyText(e.URL, key), Err: redactKey(e.Err, key)}
	case *HTTPError:
		return &HTTPError{Status: e.Status, Msg: redactKeyText(e.Msg, key)}
	}
	if msg := err.Error(); redactKeyText(msg, key) != msg {
		return errors.New(redactKeyText(msg, key))
	}
	return err
}

// HTTPError carries the status code so callers can distinguish entitlement
// errors (403 on an add-on endpoint) from transient failures.
type HTTPError struct {
	Status int
	Msg    string
}

func (e *HTTPError) Error() string { return e.Msg }

// IsEntitlementErr reports whether err is an HTTP 401/403/404 — the statuses a
// valid-but-unsubscribed key gets on an add-on endpoint like Benzinga news.
func IsEntitlementErr(err error) bool {
	var he *HTTPError
	if !errors.As(err, &he) {
		return false
	}
	return he.Status == http.StatusUnauthorized || he.Status == http.StatusForbidden || he.Status == http.StatusNotFound
}

// IsRateLimitErr reports whether err is an HTTP 429 (provider throttling).
func IsRateLimitErr(err error) bool {
	var he *HTTPError
	if !errors.As(err, &he) {
		return false
	}
	return he.Status == http.StatusTooManyRequests
}

func firstNonZero(vals ...float64) float64 {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}

func snippet(b []byte) string {
	const n = 200
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}
