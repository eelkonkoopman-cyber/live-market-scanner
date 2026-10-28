package main

import (
	"context"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dlevel"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

const (
	dlevelMinClose = 25.0 // only names that closed above this get levels (the scan wants > $30; margin for a gap up)
	dlevelSince    = 100  // calendar days of daily bars read (the DB keeps ~45; this just never cuts them)
)

// Vars so tests can shorten them.
var (
	dlevelEvery = time.Minute
	dlevelFirst = 20 * time.Second
)

// prevWeekday is the weekday before date ("2006-01-02"); holidays are not
// known here, which only ever makes the PDH level conservative (omitted).
func prevWeekday(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	t = t.AddDate(0, 0, -1)
	for t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		t = t.AddDate(0, 0, -1)
	}
	return t.Format("2006-01-02")
}

// computeDailyLevels turns the stored daily bars into per-symbol levels for
// today (an ET date). Only symbols whose newest close is above dlevelMinClose
// are kept.
func computeDailyLevels(all map[string][]core.DailyBar, today string) map[string][]dlevel.Level {
	prev := prevWeekday(today)
	out := make(map[string][]dlevel.Level)
	for sym, bs := range all {
		var last *core.DailyBar
		for i := len(bs) - 1; i >= 0; i-- {
			if bs[i].Date < today {
				last = &bs[i]
				break
			}
		}
		if last == nil || last.C <= dlevelMinClose {
			continue
		}
		if lv := dlevel.Compute(bs, today, last.Date == prev); len(lv) > 0 {
			out[sym] = lv
		}
	}
	return out
}

// computePrevLows maps each symbol (newest close above dlevelMinClose) to its
// previous-day low, under the same staleness rule as the PDH level.
func computePrevLows(all map[string][]core.DailyBar, today string) map[string]float32 {
	prev := prevWeekday(today)
	out := make(map[string]float32)
	for sym, bs := range all {
		var last *core.DailyBar
		for i := len(bs) - 1; i >= 0; i-- {
			if bs[i].Date < today {
				last = &bs[i]
				break
			}
		}
		if last == nil || last.C <= dlevelMinClose {
			continue
		}
		if lo := dlevel.PrevLow(bs, today, last.Date == prev); lo > 0 {
			out[sym] = lo
		}
	}
	return out
}

// runDailyLevels keeps the engine's daily levels (Daily Breakout scan) current.
// Source: daily_close_history (Massive grouped daily bars, kept by the nightly
// job) - no extra REST. Levels are recomputed when the ET date or the newest
// stored bar changes and re-applied every minute from the cache (cheap and
// idempotent: it reaches tickers created later).
func runDailyLevels(ctx context.Context,
	maxDate func(ctx context.Context) (string, error),
	load func(ctx context.Context, since string) (map[string][]core.DailyBar, error),
	apply func(map[string][]dlevel.Level), applyLows func(map[string]float32), now func() time.Time) error {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		ny = time.UTC
	}
	wait := dlevelFirst
	var key string
	var cache map[string][]dlevel.Level
	var lows map[string]float32
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		wait = dlevelEvery
		t := now().In(ny)
		day := t.Format("2006-01-02")
		md, err := maxDate(ctx)
		if err != nil {
			logx.L().Warn("dlevels: max date failed; retrying", "err", err)
			continue
		}
		if k := day + "|" + md; k != key {
			all, err := load(ctx, t.AddDate(0, 0, -dlevelSince).Format("2006-01-02"))
			if err != nil {
				logx.L().Warn("dlevels: load failed; retrying", "err", err)
				continue
			}
			cache = computeDailyLevels(all, day)
			lows = computePrevLows(all, day)
			key = k
			logx.L().Info("daily levels loaded", "day", day, "newestBar", md, "symbols", len(cache))
		}
		apply(cache)
		applyLows(lows)
	}
}
