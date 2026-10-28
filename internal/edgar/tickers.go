package edgar

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

// tickerCache maps SEC CIK numbers to trading tickers, from EDGAR's own
// company_tickers.json (~10k entries, ~800KB, confirmed live) — the only way
// to turn a filing's issuerCik into the symbol this app tracks, since the
// filing feed itself never carries a ticker. Refreshed once a day; a stale
// or momentarily-unavailable cache just means a very recently IPO'd issuer's
// 13G is skipped until the next refresh, not an error anywhere.
type tickerCache struct {
	mu       sync.RWMutex
	byCIK    map[int]string
	loadedAt time.Time
}

func newTickerCache() *tickerCache { return &tickerCache{byCIK: map[int]string{}} }

// lookup returns the ticker for a CIK ("", false) if unknown or not yet loaded.
func (c *tickerCache) lookup(cik int) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.byCIK[cik]
	return t, ok
}

func (c *tickerCache) stale() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return time.Since(c.loadedAt) > tickerRefreshEvery
}

// ensureFresh refreshes the cache if it's empty or past its TTL. Safe to call
// on every poll tick — it no-ops instantly once warm.
func (w *Watcher) ensureTickersFresh(ctx context.Context) {
	if !w.tickers.stale() {
		return
	}
	body, err := w.getSEC(ctx, w.tickerListURL)
	if err != nil {
		logx.L().Warn("edgar13g: ticker list refresh failed", "err", err)
		return
	}
	var raw map[string]tickerRow
	if err := json.Unmarshal(body, &raw); err != nil {
		logx.L().Warn("edgar13g: ticker list decode failed", "err", err)
		return
	}
	byCIK := make(map[int]string, len(raw))
	for _, r := range raw {
		if r.CIK > 0 && r.Ticker != "" {
			byCIK[r.CIK] = r.Ticker
		}
	}
	w.tickers.mu.Lock()
	w.tickers.byCIK = byCIK
	w.tickers.loadedAt = time.Now()
	w.tickers.mu.Unlock()
}

type tickerRow struct {
	CIK    int    `json:"cik_str"`
	Ticker string `json:"ticker"`
	Title  string `json:"title"`
}
