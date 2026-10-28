package feed

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

type newsPublisher struct {
	Name string `json:"name"`
}

type newsInsight struct {
	Ticker    string `json:"ticker"`
	Sentiment string `json:"sentiment"`
}

type newsResult struct {
	ID           string        `json:"id"`
	Title        string        `json:"title"`
	Description  string        `json:"description"`
	ArticleURL   string        `json:"article_url"`
	PublishedUTC string        `json:"published_utc"`
	Tickers      []string      `json:"tickers"`
	Publisher    newsPublisher `json:"publisher"`
	Insights     []newsInsight `json:"insights"`
}

type newsResp struct {
	Results []newsResult `json:"results"`
}

// NewsSince fetches recent news, optionally filtered to items published after
// sinceMs (the server filter plus a client-side guard).
func (c *Client) NewsSince(ctx context.Context, sinceMs int64) ([]core.NewsItem, error) {
	u := c.restBase + "/v2/reference/news?order=desc&limit=200"
	if sinceMs > 0 {
		ts := time.UnixMilli(sinceMs).UTC().Format(time.RFC3339)
		u += "&published_utc.gte=" + url.QueryEscape(ts)
	}
	var resp newsResp
	if err := c.getJSON(ctx, u, &resp); err != nil {
		if IsRateLimitErr(err) {
			return nil, fmt.Errorf("%w: %s", core.ErrRateLimited, err)
		}
		return nil, err
	}
	out := make([]core.NewsItem, 0, len(resp.Results))
	for _, n := range resp.Results {
		ms := parseRFC3339ms(n.PublishedUTC)
		if sinceMs > 0 && ms != 0 && ms <= sinceMs {
			continue
		}
		item := core.NewsItem{
			ID:       n.ID,
			Tickers:  n.Tickers,
			Headline: decodeEntities(n.Title),
			Source:   n.Publisher.Name,
			URL:      n.ArticleURL,
			TS:       ms,
			Teaser:   decodeEntities(n.Description),
		}
		if len(n.Insights) > 0 {
			item.Sentiment = n.Insights[0].Sentiment
		}
		out = append(out, item)
	}
	return out, nil
}

func parseRFC3339ms(s string) int64 {
	if s == "" {
		return 0
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UnixMilli()
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UnixMilli()
	}
	return 0
}
