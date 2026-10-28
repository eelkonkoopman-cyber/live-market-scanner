package feed

import (
	"context"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

type snapBar struct {
	O  float64 `json:"o"`
	H  float64 `json:"h"`
	L  float64 `json:"l"`
	C  float64 `json:"c"`
	V  float64 `json:"v"`
	VW float64 `json:"vw"`
}

type snapMin struct {
	O  float64 `json:"o"`
	H  float64 `json:"h"`
	L  float64 `json:"l"`
	C  float64 `json:"c"`
	V  float64 `json:"v"`
	AV float64 `json:"av"`
	VW float64 `json:"vw"`
	T  int64   `json:"t"` // ms
}

type snapTicker struct {
	Ticker  string  `json:"ticker"`
	Day     snapBar `json:"day"`
	Min     snapMin `json:"min"`
	PrevDay snapBar `json:"prevDay"`
	Updated int64   `json:"updated"` // ns
}

type snapshotResp struct {
	Status  string       `json:"status"`
	Tickers []snapTicker `json:"tickers"`
}

// Snapshot fetches the full-market snapshot and converts each ticker into a seed
// row (current price/vol/hi/lo/open/vwap + prev close).
func (c *Client) Snapshot(ctx context.Context) ([]core.Seed, error) {
	var resp snapshotResp
	if err := c.getJSON(ctx, c.restBase+"/v2/snapshot/locale/us/markets/stocks/tickers", &resp); err != nil {
		return nil, err
	}
	out := make([]core.Seed, 0, len(resp.Tickers))
	for _, t := range resp.Tickers {
		if t.Ticker == "" {
			continue
		}
		out = append(out, t.toSeed())
	}
	return out, nil
}

func (t snapTicker) toSeed() core.Seed {
	// During pre-market the `day` block can be empty; fall back to `min`.
	last := firstNonZero(t.Day.C, t.Min.C, t.PrevDay.C)
	vol := int64(firstNonZero(t.Day.V, t.Min.AV))
	vw := firstNonZero(t.Day.VW, t.Min.VW)

	endMs := t.Min.T
	if endMs == 0 {
		endMs = t.Updated / 1_000_000 // ns -> ms
	}
	if endMs == 0 {
		endMs = time.Now().UnixMilli() // never 0 (0 collides with the ring's "untouched" sentinel)
	}

	return core.Seed{
		Agg: core.Agg{
			Sym:     t.Ticker,
			Open:    firstNonZero(t.Day.O, t.Min.O),
			High:    firstNonZero(t.Day.H, t.Min.H, last),
			Low:     firstNonZero(t.Day.L, t.Min.L, last),
			Close:   last,
			VWAP:    vw,
			Vol:     vol,
			AccVol:  vol,
			DayOpen: t.Day.O,
			DayVWAP: vw,
			StartMs: endMs - 1000,
			EndMs:   endMs,
		},
		PrevClose: t.PrevDay.C,
	}
}
