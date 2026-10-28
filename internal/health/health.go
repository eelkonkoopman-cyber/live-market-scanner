// Package health tracks the live ok/failing/off state of every optional or
// critical data source this process talks to (Benzinga push, RTPR's ref feed
// and alerts socket, EDGAR, the Massive feed itself), so the WS status frame
// can surface a source that has gone quietly bad instead of staying silent
// about it. Motivating case: RTPR rejecting a saved token with "Invalid or
// inactive API key" on every single poll, with nothing anywhere — not the UI,
// not even a glance at the app — making that visible short of reading logs.
package health

import (
	"sync"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// Tracker holds one core.SourceHealth entry per named source behind a mutex —
// sources report in from their own goroutines (the bzstream session loop, the
// rtpr poller tick, ...), and Snapshot is read once a second off the server's
// status closure, so all three methods must be safe for concurrent use.
type Tracker struct {
	mu      sync.Mutex
	sources map[string]core.SourceHealth
	now     func() time.Time // overridable clock; tests inject a fake
}

// New builds an empty Tracker. A source absent from Snapshot simply hasn't
// reported in yet (never polled, never marked Off) — every real source in
// main.go reports in (OK/Fail) or is marked Off during startup, so in
// practice nothing stays absent past the first tick.
func New() *Tracker {
	return &Tracker{sources: make(map[string]core.SourceHealth), now: time.Now}
}

// SourceOK records a successful poll/connect for name. Since — the current
// state's start time — resets only on the transition INTO "ok" (unknown or
// failing -> ok); an unbroken run of successes must not look like the state
// just started on every single call. LastOK always advances on every call, so
// the status bar can show genuine "when did this last actually succeed"
// freshness independent of how long the current streak has run.
func (t *Tracker) SourceOK(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now().UnixMilli()
	e := t.sources[name]
	if e.State != "ok" {
		e.State = "ok"
		e.Since = now
	}
	e.LastOK = now
	e.Msg = ""
	t.sources[name] = e
}

// SourceFail records a failed poll/connect for name with the error text msg.
// Since resets only on the transition INTO "failing" (unknown or ok ->
// failing) — a source that has been down for an hour should keep reporting
// that hour, not reset Since on every retry — while Msg always updates to the
// latest failure text.
func (t *Tracker) SourceFail(name, msg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now().UnixMilli()
	e := t.sources[name]
	if e.State != "failing" {
		e.State = "failing"
		e.Since = now
	}
	e.Msg = msg
	t.sources[name] = e
}

// Off marks name as deliberately unconfigured (e.g. no token present) —
// distinct from "failing" so the UI doesn't alarm on a source the user simply
// never enabled. Since always resets (Off is itself a state transition, called
// once at startup for a source that never runs) and Msg clears.
func (t *Tracker) Off(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.sources[name]
	e.State = "off"
	e.Since = t.now().UnixMilli()
	e.Msg = ""
	t.sources[name] = e
}

// Snapshot returns a copy of every known source's current state, safe for the
// caller to serialize onto the status frame without holding the Tracker's
// lock for the length of a JSON marshal.
func (t *Tracker) Snapshot() map[string]core.SourceHealth {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]core.SourceHealth, len(t.sources))
	for k, v := range t.sources {
		out[k] = v
	}
	return out
}

// var _ documents (and compile-checks) that *Tracker satisfies the interface
// every data source depends on to report its own health.
var _ core.HealthReporter = (*Tracker)(nil)
