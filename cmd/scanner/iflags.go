package main

import (
	"context"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
	"github.com/eelkonkoopman/marketscanner/internal/pattern"
)

// intradayFlagInterval is how often the intraday flag pass recomputes. One
// minute against 5-minute buckets means a fresh coil is scored well within a
// bar of forming, while the pass itself stays negligible: the engine's
// SessionBars read is already gated to the active fraction of the market, and
// pattern.AnalyzeWith is microseconds per symbol.
const intradayFlagInterval = 60 * time.Second

// runIntradayFlags is the intraday sibling of the nightly EnableDailyFlags
// pass: every interval it pulls each active ticker's session-so-far 5-minute
// bars out of the engine and re-runs the SAME bull-flag geometry under
// pattern.IntradayParams, publishing map[sym]FlagRef for the scanner's
// iflagScore/... columns. Geometry never runs in the engine — this loop is
// where it lives.
//
// bars/publish are injected (engine.SessionBars / scan.SetIntradayFlags in
// production) so the loop is unit-testable without an engine, mirroring
// runTapeRegimeOrchestrator's injected-dependency style.
//
// Every cycle publishes, even an empty map: after the engine's session reset
// SessionBars goes empty, and publishing that empty map is exactly what wipes
// yesterday's stale intraday flags (the scan overlay stamps Null on every
// symbol absent from the newest map). FlagRef.AsOf needs no handling here —
// AnalyzeWith stamps it from the last bar's Date, which SessionBars already
// labels with the bucket's ET start time ("2006-01-02 15:04").
//
// A plain for-range-ticker loop cannot overlap itself: a cycle that somehow
// ran long simply delays (and coalesces) subsequent ticks. Do not add
// per-cycle goroutines.
func runIntradayFlags(
	ctx context.Context,
	interval time.Duration,
	bars func() map[string][]core.DailyBar,
	publish func(map[string]core.FlagRef),
) error {
	p := pattern.IntradayParams()
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tk.C:
			candidates := bars()
			flags := make(map[string]core.FlagRef)
			for sym, bs := range candidates {
				if ref, ok := pattern.AnalyzeWith(p, bs); ok {
					flags[sym] = ref
				}
			}
			publish(flags)
			logx.L().Debug("intraday flag pass", "candidates", len(candidates), "flags", len(flags))
		}
	}
}
