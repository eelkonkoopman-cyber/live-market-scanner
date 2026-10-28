// Package alpaca implements the scanner's market-data feed against Alpaca
// Market Data v2 (Algo Trader Plus: SIP, all US exchanges, extended hours).
//
// It is a drop-in alternative to internal/feed (Massive): the same method set
// (core.FeedClient plus the extra REST helpers cmd/scanner wires), returning
// the same core types. Differences that cannot be hidden are documented in
// docs/alpaca-feed.md. The most important one: Alpaca has no per-second
// aggregate channel, so Stream subscribes to the whole-market trade channel
// and builds the per-second core.Agg values itself (see secagg.go).
//
// Keys are sent ONLY as request headers (REST) and inside the websocket auth
// frame; they never appear in a URL, a log line, or an error message.
package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

const (
	DefaultDataBase    = "https://data.alpaca.markets"
	DefaultTradingBase = "https://api.alpaca.markets"
	defaultStreamBase  = "wss://stream.data.alpaca.markets/v2/"
)

// Config configures the client. Zero values fall back to Alpaca defaults.
type Config struct {
	Keys Keys
	// Feed is "sip" (default, Algo Trader Plus) or "iex" (free plan, testing).
	Feed        string
	DataBase    string // REST market data, default https://data.alpaca.markets
	TradingBase string // /v2/assets only; default Keys.TradingBase or https://api.alpaca.markets
	WSURL       string // default wss://stream.data.alpaca.markets/v2/<feed>
	HTTP        *http.Client
	// Symbols, when non-empty, replaces the "*" wildcard trade subscription
	// (the free IEX plan allows only 30 symbols and refuses the wildcard).
	Symbols []string
	// TapeSink / TapeStatus: as internal/feed.Config.
	TapeSink   chan<- core.TapeEvent
	TapeStatus func(msg string)
	// StaleAfter: reconnect when no data frame arrived for this long during
	// 04:00-20:00 ET on a weekday. Default 60s; <0 disables.
	StaleAfter time.Duration
	// Now is the clock (tests). Default time.Now.
	Now func() time.Time
	// SnapshotChunk / BarsChunk: symbols per multi-symbol request.
	SnapshotChunk int
	BarsChunk     int
}

// Client is the Alpaca feed client.
type Client struct {
	keysp       atomic.Pointer[Keys]
	feed        string
	dataBase    string
	tradingBase string
	wsURL       string
	http        *http.Client
	symbols     []string
	staleAfter  time.Duration
	now         func() time.Time
	snapChunk   int
	barsChunk   int

	tapeSink   chan<- core.TapeEvent
	tapeStatus func(msg string)
	tapeSyms   atomic.Pointer[map[string]struct{}]
	connp      atomic.Pointer[wsConnRef]

	sagg *SecAgg // trades -> per-second aggregates (shared across reconnects)

	assetsMu sync.Mutex
	assets   []asset
	assetsAt time.Time
}

var _ core.FeedClient = (*Client)(nil)

// New builds a client.
func New(cfg Config) *Client {
	c := &Client{
		feed: strings.ToLower(strings.TrimSpace(cfg.Feed)), dataBase: cfg.DataBase,
		tradingBase: cfg.TradingBase, wsURL: cfg.WSURL, http: cfg.HTTP,
		symbols: cfg.Symbols, staleAfter: cfg.StaleAfter, now: cfg.Now,
		snapChunk: cfg.SnapshotChunk, barsChunk: cfg.BarsChunk,
		tapeSink: cfg.TapeSink, tapeStatus: cfg.TapeStatus,
		sagg: NewSecAgg(),
	}
	k := cfg.Keys
	c.keysp.Store(&k)
	if c.feed == "" {
		c.feed = "sip"
	}
	if c.dataBase == "" {
		c.dataBase = DefaultDataBase
	}
	if c.tradingBase == "" {
		c.tradingBase = k.TradingBase
	}
	if c.tradingBase == "" {
		c.tradingBase = DefaultTradingBase
	}
	if c.wsURL == "" {
		c.wsURL = defaultStreamBase + c.feed
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 30 * time.Second}
	}
	if c.staleAfter == 0 {
		c.staleAfter = 60 * time.Second
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.snapChunk <= 0 {
		c.snapChunk = 500
	}
	if c.barsChunk <= 0 {
		c.barsChunk = 500
	}
	return c
}

// Feed returns "sip" or "iex".
func (c *Client) Feed() string { return c.feed }

// HasKey reports whether a full key pair is configured.
func (c *Client) HasKey() bool { return c.keys().Valid() }

// SetKey accepts "KEYID:SECRET" (the UI's single key box). Anything else is
// ignored: Alpaca keys normally come from ~/.marketscanner/alpaca.json.
func (c *Client) SetKey(s string) {
	id, sec, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok || id == "" || sec == "" {
		return
	}
	k := c.keys()
	k.KeyID, k.Secret = strings.TrimSpace(id), strings.TrimSpace(sec)
	c.keysp.Store(&k)
}

func (c *Client) keys() Keys {
	if p := c.keysp.Load(); p != nil {
		return *p
	}
	return Keys{}
}

// HTTPError carries the status so callers can map it (entitlement, 429).
type HTTPError struct {
	Status int
	Msg    string
}

func (e *HTTPError) Error() string { return e.Msg }

func (e *HTTPError) Is(target error) bool {
	switch target {
	case core.ErrRateLimited:
		return e.Status == http.StatusTooManyRequests
	case core.ErrNotEntitled:
		return e.Status == http.StatusForbidden || e.Status == http.StatusUnauthorized
	}
	return false
}

// getJSON GETs fullURL with the key headers and decodes into v. Errors never
// contain the keys (they are not in the URL; bodies are scrubbed anyway).
func (c *Client) getJSON(ctx context.Context, fullURL string, v any) error {
	k := c.keys()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("APCA-API-KEY-ID", k.KeyID)
	req.Header.Set("APCA-API-SECRET-KEY", k.Secret)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return scrub(err, k)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("GET %s: HTTP %d: %s", fullURL, resp.StatusCode, snippet(body))
		return &HTTPError{Status: resp.StatusCode, Msg: scrubText(msg, k)}
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("decode %s: %w", fullURL, err)
	}
	return nil
}

func scrubText(s string, k Keys) string {
	for _, x := range []string{k.Secret, k.KeyID} {
		if x != "" {
			s = strings.ReplaceAll(s, x, "REDACTED")
		}
	}
	return s
}

func scrub(err error, k Keys) error {
	if err == nil {
		return nil
	}
	if m := err.Error(); scrubText(m, k) != m {
		return errors.New(scrubText(m, k))
	}
	return err
}

func snippet(b []byte) string {
	const n = 200
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}
