package feed

import (
	"context"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

type refTicker struct {
	Ticker string `json:"ticker"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Active bool   `json:"active"`
}

type refResp struct {
	Results []refTicker `json:"results"`
	NextURL string      `json:"next_url"`
}

// Reference lists active tradable tickers for the regular-securities types we
// care about (common stock, ADRs, ETFs), following cursor pagination.
func (c *Client) Reference(ctx context.Context) ([]core.Ticker, error) {
	types := []string{"CS", "ADRC", "ETF"}
	seen := make(map[string]struct{})
	var out []core.Ticker
	for _, ty := range types {
		u := c.restBase + "/v3/reference/tickers?market=stocks&active=true&limit=1000&type=" + ty
		for page := 0; u != "" && page < 50; page++ {
			var resp refResp
			if err := c.getJSON(ctx, u, &resp); err != nil {
				return nil, err
			}
			for _, r := range resp.Results {
				if r.Ticker == "" {
					continue
				}
				if _, dup := seen[r.Ticker]; dup {
					continue
				}
				seen[r.Ticker] = struct{}{}
				out = append(out, core.Ticker{Symbol: r.Ticker, Type: r.Type, Name: r.Name, Active: r.Active})
			}
			u = resp.NextURL // absolute URL; getJSON appends apiKey
		}
	}
	return out, nil
}
