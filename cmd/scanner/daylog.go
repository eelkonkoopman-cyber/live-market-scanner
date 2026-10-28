package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/flaglog"
)

// dayLogEnabled is the kill switch of the Day Log: SCANNER_DAYLOG=0 records nothing.
func dayLogEnabled() bool { return os.Getenv("SCANNER_DAYLOG") != "0" }

// dayLogDir is where the per-day CSV files live: next to the database.
func dayLogDir(dbPath string) string { return filepath.Join(filepath.Dir(dbPath), "daylog") }

// signalNoteFor returns the Day Log note of an alert on sym at atMs taken from
// the Signal window's history (flaglog): the notes of the signals recorded for
// that name on the alert's bar or just before it, e.g. a dildo's measure or an
// ignition's "skip: ..." reason. "" when there are none or no signal log.
func signalNoteFor(rec *flaglog.Recorder) func(sym string, atMs int64) string {
	if rec == nil {
		return nil
	}
	return func(sym string, atMs int64) string {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		msg, err := rec.Day(ctx, "")
		if err != nil {
			return ""
		}
		return noteFromEvents(msg.Events, sym, atMs)
	}
}

// noteFromEvents joins the labelled notes of sym's events whose bar started
// within the three minutes before the alert (or 30 s after it).
func noteFromEvents(evs []core.FlagEvent, sym string, atMs int64) string {
	var parts []string
	for _, e := range evs {
		if e.Sym != sym || e.Note == "" || e.Ts < atMs-3*60_000 || e.Ts > atMs+30_000 {
			continue
		}
		parts = append(parts, e.Label+": "+e.Note)
	}
	return strings.Join(parts, "; ")
}
