package main

import (
	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/flaglog"
	"github.com/eelkonkoopman/marketscanner/internal/flagscan"
)

// The Signal window's SIGNALS history (internal/flaglog) records three
// sources: the 9EMA flag scan's events (flagscan OnEval), the dildo rule's
// GREEN DILDO / 9 EMA PULLBACK / PB CANCELLED on every tracked symbol
// (flagscan OnDildo) and the engine's own alerts - GREEN DILDO, VOLUME
// CLIMAX, BOTTOM CONFIRMED, CLIMAX FAILED (Engine.SignalHits, each only while
// its SCANNER_DILDO / SCANNER_CLIMAX switch is on). The window's Dildo /
// Climax alert toggles are sound only: they never reach the history.

// signalLogWanted reports whether the recorder is built: when any of its
// sources runs - the 9EMA flag scan (SCANNER_E9FLAGS) or the engine's alert
// hand-off (nil when both SCANNER_DILDO and SCANNER_CLIMAX are off). With none
// there is nothing to record: server.Config.FlagEvents stays nil and the
// window shows no history.
func signalLogWanted(emaFlags bool, hits <-chan core.SignalHit) bool {
	return emaFlags || hits != nil
}

// wireSignalLog hands every flag-scan evaluation (OnEval) and every dildo-rule
// call (OnDildo) to the recorder AND to whatever is already wired. The recorder's hooks are cheap and never block. A nil
// recorder leaves cfg as it is.
func wireSignalLog(cfg flagscan.Config, rec *flaglog.Recorder) flagscan.Config {
	if rec == nil {
		return cfg
	}
	prevEval, prevDildo := cfg.OnEval, cfg.OnDildo
	cfg.OnEval = func(u flagscan.EvalUpdate) {
		rec.OnEval(u)
		if prevEval != nil {
			prevEval(u)
		}
	}
	cfg.OnDildo = func(u flagscan.DildoUpdate) {
		rec.OnDildo(u)
		if prevDildo != nil {
			prevDildo(u)
		}
	}
	return cfg
}
