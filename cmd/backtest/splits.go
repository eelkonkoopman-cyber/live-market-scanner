package main

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

	"github.com/eelkonkoopman/marketscanner/internal/backtest"
	"github.com/eelkonkoopman/marketscanner/internal/feed"
)

// The feed's bars are split-adjusted as of the fetch (adjusted=true is hard-coded
// in internal/feed, which this tool may not edit), so the backtest needs the
// splits listing to put names that split after a test day back on that day's
// basis (internal/backtest/splits.go). internal/feed has no method for it; this
// is the one extra REST call, written the way the feed's own are: GET with the key
// as the apiKey query parameter, the key scrubbed out of every error, and the
// *feed.HTTPError type for a bad status so the fetcher's 429 backoff and refused-
// key handling work on it unchanged.

const (
	defaultRESTBase = "https://api.massive.com" // internal/feed's default; cfg.MassiveBase overrides it
	maxSplitPages   = 50                        // 1000 splits a page: a few hundred a year exist
	maxSplitBody    = 16 << 20
)

type splitClient struct {
	base string
	key  string
	http *http.Client
}

func newSplitClient(base, key string, hc *http.Client) *splitClient {
	if base == "" {
		base = defaultRESTBase
	}
	if hc == nil {
		hc = &http.Client{}
	}
	return &splitClient{base: strings.TrimRight(base, "/"), key: key, http: hc}
}

type splitsResp struct {
	Results []struct {
		Ticker string  `json:"ticker"`
		Date   string  `json:"execution_date"`
		From   float64 `json:"split_from"`
		To     float64 `json:"split_to"`
	} `json:"results"`
	NextURL string `json:"next_url"`
}

// Splits lists every split executed from..to (ISO dates, inclusive), following
// the cursor pages.
func (c *splitClient) Splits(ctx context.Context, from, to string) ([]backtest.StockSplit, error) {
	u := fmt.Sprintf("%s/v3/reference/splits?execution_date.gte=%s&execution_date.lte=%s&order=asc&sort=execution_date&limit=1000", c.base, url.QueryEscape(from), url.QueryEscape(to))
	var out []backtest.StockSplit
	for page := 0; u != ""; page++ {
		if page >= maxSplitPages {
			return nil, fmt.Errorf("splits listing: more than %d pages, stopping", maxSplitPages)
		}
		var resp splitsResp
		if err := c.get(ctx, u, &resp); err != nil {
			return nil, err
		}
		for _, r := range resp.Results {
			out = append(out, backtest.StockSplit{Sym: r.Ticker, Date: r.Date, From: r.From, To: r.To})
		}
		u = resp.NextURL
		if u != "" && !sameOrigin(c.base, u) {
			// the key goes with every request: never to a host the feed's base is not
			return nil, errors.New("splits listing: next_url points to another host, refusing to send the key there")
		}
	}
	return out, nil
}

// sameOrigin reports whether next is on the same scheme and host as base.
func sameOrigin(base, next string) bool {
	a, err1 := url.Parse(base)
	b, err2 := url.Parse(next)
	return err1 == nil && err2 == nil && a.Scheme == b.Scheme && strings.EqualFold(a.Host, b.Host)
}

// get is one authenticated GET. rawURL never carries the key; it is added to the
// request only, and every error that leaves here has it scrubbed.
func (c *splitClient) get(ctx context.Context, rawURL string, v any) error {
	sep := "?"
	if strings.Contains(rawURL, "?") {
		sep = "&"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL+sep+"apiKey="+url.QueryEscape(c.key), nil)
	if err != nil {
		return c.scrub(err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return c.scrub(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxSplitBody))
	if resp.StatusCode != http.StatusOK {
		snip := string(body)
		if len(snip) > 200 {
			snip = snip[:200]
		}
		return &feed.HTTPError{Status: resp.StatusCode, Msg: c.scrubText(fmt.Sprintf("GET %s: HTTP %d: %s", rawURL, resp.StatusCode, snip))}
	}
	if err := json.Unmarshal(body, v); err != nil {
		return c.scrub(fmt.Errorf("splits listing: not JSON: %w", err))
	}
	return nil
}

var apiKeyParam = regexp.MustCompile(`(?i)([?&]apikey=)[^&#\s"']*`)

// scrubText replaces the key (by parameter name, then raw and query-escaped) with REDACTED.
func (c *splitClient) scrubText(s string) string {
	s = apiKeyParam.ReplaceAllString(s, "${1}REDACTED")
	if c.key != "" {
		s = strings.ReplaceAll(s, c.key, "REDACTED")
		if esc := url.QueryEscape(c.key); esc != c.key {
			s = strings.ReplaceAll(s, esc, "REDACTED")
		}
	}
	return s
}

// scrub returns err with the key removed from its message. An error whose text
// is clean is returned as it is (so errors.Is / errors.As still reach the cause);
// otherwise it is flattened to a plain error, leaving no unwrap path back to it.
func (c *splitClient) scrub(err error) error {
	if err == nil {
		return nil
	}
	if msg := err.Error(); c.scrubText(msg) != msg {
		return errors.New(c.scrubText(msg))
	}
	return err
}
