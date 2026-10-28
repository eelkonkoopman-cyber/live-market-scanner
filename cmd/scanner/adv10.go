package main

import (
	"context"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

const (
	adv10Days    = 10 // the trading-day window the user asked for
	adv10MinDays = 5  // a ticker needs this many traded days in the window
)

// Vars (not consts) so tests can shorten them.
var (
	adv10Every = 5 * time.Minute
	adv10First = 15 * time.Second // let the startup snapshot create the tickers first
)

// runAdv10 keeps the engine's 10-day average daily volume current. It reads
// daily_close_history (Massive's whole-market daily bars, kept up to date by the
// daily-trend job), excluding today, and re-applies every few minutes: that
// picks up a newly fetched day and reaches tickers created after the last pass
// (seeding never creates tickers). Cheap: one aggregate query.
func runAdv10(ctx context.Context,
	load func(ctx context.Context, beforeDate string, days, minDays int) (map[string]float64, error),
	apply func(map[string]float64), now func() time.Time) error {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		ny = time.UTC
	}
	wait := adv10First
	var lastDay string
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		wait = adv10Every
		day := now().In(ny).Format("2006-01-02")
		adv, err := load(ctx, day, adv10Days, adv10MinDays)
		if err != nil {
			logx.L().Warn("adv10: load failed; retrying next pass", "err", err)
			continue
		}
		apply(adv)
		if day != lastDay {
			logx.L().Info("adv10 loaded", "before", day, "tickers", len(adv))
			lastDay = day
		}
	}
}
