package feed

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

// Benzinga v2 news via Massive: GET /benzinga/v2/news. Unlike the generic
// /v2/reference/news (hourly, no body), this is real-time and carries the FULL
// article body (HTML) + teaser — the raw material for Claude summaries.
// Requires the Benzinga expansion on the Massive plan; un-entitled keys get
// HTTP 403, which the news poller treats as "fall back to reference news".

type bzResult struct {
	BenzingaID  int64    `json:"benzinga_id"`
	Title       string   `json:"title"`
	Teaser      string   `json:"teaser"`
	Body        string   `json:"body"`
	URL         string   `json:"url"`
	Author      string   `json:"author"`
	Published   string   `json:"published"`    // ISO 8601
	LastUpdated string   `json:"last_updated"` // ISO 8601
	Tickers     []string `json:"tickers"`
	Channels    []string `json:"channels"`
	Tags        []string `json:"tags"`
}

type bzResp struct {
	Results []bzResult `json:"results"`
}

// BenzingaNews fetches Benzinga articles published after sinceMs. It returns
// wire-ready items plus a bodies map (item ID -> full body HTML) that stays
// server-side for summarization — bodies are never broadcast over the WS.
func (c *Client) BenzingaNews(ctx context.Context, sinceMs int64) ([]core.NewsItem, map[string]string, error) {
	return c.benzinga(ctx, "", sinceMs)
}

// BenzingaNewsFor fetches one symbol's articles since sinceMs — the day's news
// for a scanner entrant, including anything published before the app started.
func (c *Client) BenzingaNewsFor(ctx context.Context, sym string, sinceMs int64) ([]core.NewsItem, map[string]string, error) {
	return c.benzinga(ctx, sym, sinceMs)
}

// bzLimit bounds one Benzinga page. It must comfortably exceed the poller's
// 30-min startup seed at burst rates (docs allow up to 50k): sort=published.desc
// means a full page silently drops the OLDEST tail, and the poller's cursor
// then advances past it forever — so hitting the limit is loudly logged.
const bzLimit = 1000

func (c *Client) benzinga(ctx context.Context, sym string, sinceMs int64) ([]core.NewsItem, map[string]string, error) {
	u := c.restBase + "/benzinga/v2/news?sort=published.desc&limit=" + strconv.Itoa(bzLimit)
	if sym != "" {
		u += "&tickers=" + url.QueryEscape(sym)
	}
	if sinceMs > 0 {
		ts := time.UnixMilli(sinceMs).UTC().Format(time.RFC3339)
		u += "&published.gte=" + url.QueryEscape(ts)
	}
	var resp bzResp
	if err := c.getJSON(ctx, u, &resp); err != nil {
		if IsEntitlementErr(err) {
			return nil, nil, fmt.Errorf("%w: %s", core.ErrNotEntitled, err)
		}
		if IsRateLimitErr(err) {
			return nil, nil, fmt.Errorf("%w: %s", core.ErrRateLimited, err)
		}
		return nil, nil, err
	}
	if len(resp.Results) >= bzLimit {
		logx.L().Warn("benzinga news page full — oldest articles in the window were dropped",
			"limit", bzLimit, "sym", sym, "sinceMs", sinceMs)
	}
	items := make([]core.NewsItem, 0, len(resp.Results))
	bodies := make(map[string]string, len(resp.Results))
	for _, r := range resp.Results {
		ms := parseRFC3339ms(r.Published)
		if sinceMs > 0 && ms != 0 && ms < sinceMs {
			continue
		}
		id := "bz" + strconv.FormatInt(r.BenzingaID, 10)
		items = append(items, core.NewsItem{
			ID:       id,
			Tickers:  r.Tickers,
			Headline: decodeEntities(r.Title),
			Source:   "Benzinga",
			URL:      r.URL,
			TS:       ms,
			Teaser:   decodeEntities(r.Teaser),
			HasBody:  r.Body != "",
		})
		if r.Body != "" {
			bodies[id] = r.Body // raw HTML; the summarizer strips + unescapes it
		}
	}
	return items, bodies, nil
}

// decodeEntities unescapes HTML entities in feed-supplied plain-text fields.
// Benzinga sends headlines/teasers HTML-escaped ("S&amp;P 500", "Big Tech&#39;s"),
// and these render verbatim in the news panel and get fed to Claude, so they
// have to be decoded here rather than at the display layer. Article bodies are
// left as raw HTML — the summarizer's stripHTML already unescapes those.
func decodeEntities(s string) string {
	if !strings.Contains(s, "&") {
		return s // fast path: no entities possible
	}
	return html.UnescapeString(s)
}
