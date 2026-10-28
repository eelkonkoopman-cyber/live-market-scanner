package main

import (
	"context"
	"os"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/ep"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

// epEnabled gates the Episodic Pivot daily-context worker (env SCANNER_EP,
// default on). Off, the scans stay empty: without a 3-month change a gapper
// never qualifies.
func epEnabled() bool { return os.Getenv("SCANNER_EP") != "0" }

// Vars so tests can shorten them.
var (
	epEvery      = 20 * time.Second
	epBatch      = 12 // candidates fetched per cycle (one REST call each)
	epRetryAfter = 5 * time.Minute
)

// epCandidateScan caps the candidate list runEP walks: in effect every gapper
// still lacking a context (a few dozen on a busy morning).
const epCandidateScan = 1 << 16

// epSource is everything the worker needs, as seams (no network in tests).
type epSource struct {
	Candidates func(max int) []string
	Daily      func(ctx context.Context, sym, from, to string) ([]core.DailyBar, error)
	StoredBars func(ctx context.Context, sym, since string) ([]core.DailyBar, error) // daily_close_history fallback
	Articles   func(ctx context.Context, sym string, limit int) ([]core.NewsItem, error)
	MarkNews   func([]core.NewsItem)
	Publish    func(map[string]core.EPRef)
	Now        func() time.Time
}

// epRefFor builds a candidate's daily context: the Massive daily aggregates
// when they come back (a 63-session lookback, ~110 calendar days), else the
// stored daily_close_history (ADR over the days available, 3-month change
// unknown so the symbol cannot qualify). The newest bar must be before today.
func epRefFor(ctx context.Context, src epSource, sym, today string) (core.EPRef, bool) {
	now := src.Now()
	from := now.AddDate(0, 0, -110).Format("2006-01-02")
	var bars []core.DailyBar
	if src.Daily != nil {
		if b, err := src.Daily(ctx, sym, from, today); err == nil {
			bars = b
		}
	}
	if len(bars) == 0 && src.StoredBars != nil {
		if b, err := src.StoredBars(ctx, sym, from); err == nil {
			bars = b
		}
	}
	for len(bars) > 0 && bars[len(bars)-1].Date >= today {
		bars = bars[:len(bars)-1]
	}
	if len(bars) == 0 {
		return core.EPRef{}, false
	}
	chg, adr, days := ep.Stats(bars)
	return core.EPRef{Chg3m: chg, AdrPct: adr, AdrDays: days}, true
}

// runEP keeps the EP overlay's daily context current: every epEvery it asks
// the scanner for gapper candidates without a context, fetches each once per
// day, replays the stored articles of the symbol into the earnings matcher
// (a scanner restarted in pre-market would otherwise forget last night's
// headline) and publishes the batch. A failed symbol is retried after
// epRetryAfter.
func runEP(ctx context.Context, src epSource) error {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		ny = time.UTC
	}
	day := ""
	tried := map[string]time.Time{}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(epEvery):
		}
		now := src.Now().In(ny)
		if d := now.Format("2006-01-02"); d != day {
			day, tried = d, map[string]time.Time{}
		}
		batch := map[string]core.EPRef{}
		// Every qualifying symbol, not the first few: the snapshot lists rows in
		// a stable order, so a capped list could keep returning the same names
		// that fail (no daily bars) and are skipped here, starving the rest.
		for _, sym := range src.Candidates(epCandidateScan) {
			if len(batch) >= epBatch {
				break
			}
			if t, ok := tried[sym]; ok && now.Sub(t) < epRetryAfter {
				continue
			}
			tried[sym] = now
			if src.Articles != nil && src.MarkNews != nil {
				if items, aerr := src.Articles(ctx, sym, 50); aerr == nil && len(items) > 0 {
					src.MarkNews(items)
				}
			}
			ref, ok := epRefFor(ctx, src, sym, day)
			if !ok {
				logx.L().Debug("ep: no daily bars", "sym", sym)
				continue
			}
			batch[sym] = ref
		}
		if len(batch) > 0 {
			src.Publish(batch)
		}
	}
}

// EP seeds: own keys, seeded once, never resurrected.
const (
	epWatchSeedKey = "seeded.epwatch.v1"
	epWatchID      = "ep-watch"
	epBreakSeedKey = "seeded.epbreak.v1"
	epBreakID      = "ep-break"
)

// epWatchScans: "EP Watch" (docs/episodic-pivot.md), pre-market only: the
// overlay's epPass (gap >= 10%, >= $5, volume pace, earnings headline since
// the previous 16:00 ET, 3-month change <= +10%). Alert on enter, ON by
// default, Speak "SYM: episodic pivot watch, earnings". An alert only.
func epWatchScans() []core.ScanDef {
	return []core.ScanDef{{
		ID: epWatchID, Name: "EP Watch", Enabled: true,
		Filter: core.Filter{Op: "and", Conds: []core.Filter{
			{Field: core.FieldSession, Cmp: core.CmpEq, Value: "pre"},
			{Field: core.FieldEpPass, Cmp: core.CmpEq, Value: 1},
		}},
		Sort:    []core.SortKey{{Field: core.FieldEpGap, Dir: "desc"}},
		Limit:   50,
		Columns: []string{"last", "epGap", "epVolX", "epEarnHL", "epEarnAge", "ep3m", "epAdr", "epStop", "news"},
		Alert:   core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: "ping", CooldownSec: 600, RequireReentry: true, Speak: true},
	}}
}

// epBreakScans: "EP Break", 09:35-11:00 ET: a symbol that passed EP Watch (or
// passes the same rules at the open) and traded above the 09:30-09:35 high.
// epBrkAge is set once per symbol per day by the overlay, so the row enters
// once; the 24 h cooldown is a second guard. Alert ON, Speak "SYM: episodic
// pivot break".
func epBreakScans() []core.ScanDef {
	return []core.ScanDef{{
		ID: epBreakID, Name: "EP Break", Enabled: true,
		Filter: core.Filter{Op: "and", Conds: []core.Filter{
			{Field: core.FieldEpBrkAge, Cmp: core.CmpGTE, Value: 0},
		}},
		Sort:    []core.SortKey{{Field: core.FieldEpBrkAge, Dir: "asc"}},
		Limit:   50,
		Columns: []string{"last", "epOrh", "epStop", "epRiskPct", "epGap", "epVolX", "epEarnHL", "epAdr", "epBrkAge", "news"},
		Alert:   core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: "ping", CooldownSec: 86400, Speak: true},
	}}
}
