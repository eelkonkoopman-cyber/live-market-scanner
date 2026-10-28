package feed

import (
	"context"
	"sync"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

type detailsResp struct {
	Results struct {
		Ticker                      string `json:"ticker"`
		ShareClassSharesOutstanding *int64 `json:"share_class_shares_outstanding"`
		WeightedSharesOutstanding   *int64 `json:"weighted_shares_outstanding"`
	} `json:"results"`
}

// Floats fetches share-structure reference for the given symbols via the Ticker
// Details v3 endpoint, with bounded concurrency. Per-symbol failures are skipped
// (best-effort). True free float isn't provided by the API, so FloatShares stays
// nil and SharesOut carries share_class_shares_outstanding (proxy).
func (c *Client) Floats(ctx context.Context, syms []string) ([]core.FloatRef, error) {
	out := make([]core.FloatRef, 0, len(syms))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	now := time.Now().Unix()

	for _, sym := range syms {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(sym string) {
			defer wg.Done()
			defer func() { <-sem }()
			var d detailsResp
			if err := c.getJSON(ctx, c.restBase+"/v3/reference/tickers/"+sym, &d); err != nil {
				return
			}
			so := d.Results.ShareClassSharesOutstanding
			if so == nil {
				so = d.Results.WeightedSharesOutstanding
			}
			mu.Lock()
			out = append(out, core.FloatRef{Ticker: sym, SharesOut: so, AsOf: now, Source: "details_v3"})
			mu.Unlock()
		}(sym)
	}
	wg.Wait()
	return out, ctx.Err()
}
