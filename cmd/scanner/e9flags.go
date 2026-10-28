package main

import (
	"context"
	"os"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
	"github.com/eelkonkoopman/marketscanner/internal/emaflag"
	"github.com/eelkonkoopman/marketscanner/internal/engine"
	"github.com/eelkonkoopman/marketscanner/internal/flagscan"
	"github.com/eelkonkoopman/marketscanner/internal/ingest"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
	"github.com/eelkonkoopman/marketscanner/internal/scan"
)

// emaFlagsEnabled is the kill switch for the 9EMA-flag market scan: it spends
// REST calls on the user's Massive key (an admission backfill per candidate and
// a resync per candidate every five minutes, capped at flagscan.MaxCandidates),
// so like prefill / prevah / rvolfill it can be turned off from the
// environment: SCANNER_E9FLAGS=0 starts nothing, the e9* columns stay Null and
// the three seeded scans simply stay empty.
func emaFlagsEnabled() bool { return os.Getenv("SCANNER_E9FLAGS") != "0" }

// emaFlagConfig wires the production components into the flagscan service, the
// way runIntradayFlags is handed engine.SessionBars and scan.SetIntradayFlags.
// Every field is a method value on a component that already exists, so this is
// the whole adapter - and the only place that knows which component feeds which
// seam:
//
//   - Candidates  Engine.PmVolLeaders: the tracked symbols whose pre-market
//     volume is at least minX x their 10-day average (the scan field pmVolX,
//     one formula behind both);
//   - Bars        feed.Client.IntradayWithPrevDay, the very REST call the chart
//     window's chart.get makes (previous trading day + today, 1-minute bars):
//     the scan starts from the same history the Signal window draws;
//   - Aggs/SetTap/Drops  Ingest.FlagAggs / SetFlagSyms / FlagDrops: the second
//     tee off the Massive aggregate stream (no second WebSocket, no extra
//     subscription - the A.* stream already carries every symbol);
//   - Publish     Scanner.SetEmaFlags: the verdict map the scanner overlays on
//     its rows as e9State ... e9Age;
//   - Params      the Signal window's defaults (emaflag.DefaultParams, held equal
//     to DEFAULT_FLAG_PARAMS by the golden vectors), so a scan verdict and the
//     chart's verdict for a symbol are the same call;
//   - Hot / Dildo / DildoDrops  the GREEN DILDO hand-off: Engine.DildoHits (nil,
//     so off, when SCANNER_DILDO=0 built the engine without the judge), the
//     calibrated dildo rule (dildopb.DefaultParams, held equal to
//     DEFAULT_DILDO_PARAMS by its golden vectors) and the engine's drop count:
//     alert 2, the pullback to the chart's 9 EMA, judged on these same bars.
//     With the judge off the rule goes over DISABLED too: flagscan also judges
//     the dildo on its own candidates (the chart-anchor union raises alert 1
//     from them), so SCANNER_DILDO=0 must reach it or both alerts keep firing
//     for every pmVolX candidate and pinned name. Enabled false on the
//     defaults, never the zero Params (which flagscan reads as "the defaults").
func emaFlagConfig(eng *engine.Engine, fc marketFeed, ing *ingest.Ingest, scn *scan.Scanner) flagscan.Config {
	dildo := dildopb.DefaultParams()
	judgeOn := eng.DildoHits() != nil // the engine judge's own switch (Options.Dildo = SCANNER_DILDO)
	dildo.Enabled = judgeOn
	return flagscan.Config{
		Candidates: eng.PmVolLeaders,
		Bars:       fc.IntradayWithPrevDay,
		Aggs:       ing.FlagAggs(),
		SetTap:     ing.SetFlagSyms,
		Publish:    scn.SetEmaFlags,
		Now:        time.Now,
		Params:     emaflag.DefaultParams(),
		Drops:      ing.FlagDrops,
		Hot:        eng.DildoHits(),
		Dildo:      dildo,
		// The tuning panel's params (dildopb.Live), the engine judge's too; the
		// env switch still wins over the panel's own "enabled".
		DildoLive: func() dildopb.Params {
			p := dildopb.Live()
			p.Enabled = p.Enabled && judgeOn
			return p
		},
		DildoDrops: eng.DildoDrops,
	}
}

// warnDildoAlert2Off is the one log line for a scanner whose green-dildo judge
// is on but whose 9EMA flag scan is off (SCANNER_E9FLAGS=0): alert 1 ("SYM:
// green dildo") still works - it is the engine's - but nothing judges alert 2,
// the pullback to the chart's 9 EMA, so "Dildo 9EMA Pullback" stays empty.
func warnDildoAlert2Off() {
	logx.L().Warn("green dildo: the 9 EMA pullback alert (alert 2) needs the 9EMA flag scan, which is off (SCANNER_E9FLAGS=0); the green dildo alert itself still works")
}

// runEmaFlags runs the market scan until ctx ends. The scan is an optional
// extra: a failure to start it (a Config the service refuses - a wiring bug,
// not a runtime condition) is logged and swallowed, because the lifecycle
// helper in run() cancels EVERY component when one returns an error, and a
// misconfigured side feature must never take the scanner the trader is using
// down with it (the tray is handled the same way). The service itself returns
// nil on cancel.
func runEmaFlags(ctx context.Context, cfg flagscan.Config) error {
	if err := flagscan.Run(ctx, cfg); err != nil {
		logx.L().Error("9EMA flag scan failed to start; continuing without it", "err", err)
	}
	return nil
}
