package tape

import (
	"testing"
	"time"
)

// applyCtl drains and applies one pending ctl-routed mutation. Production
// code applies these inside Run()'s select loop; these unit tests have no
// Run() goroutine, so a call to SetClaudeEnabled/SetCommentaryInterval/
// NoteReadComplete (all of which route through e.ctl exactly like
// SetSymbols) must be followed by applyCtl to actually take effect.
func applyCtl(e *Engine) {
	select {
	case fn := <-e.ctl:
		fn(e)
	default:
	}
}

func TestSetClaudeEnabledTogglesAndResetsHeartbeatTimer(t *testing.T) {
	e := newTest()
	st := e.syms["ABCD"]
	st.lastReadAt = 0 // simulate "never read"

	e.SetClaudeEnabled("ABCD", true)
	applyCtl(e)
	if !st.claudeEnabled {
		t.Fatalf("claudeEnabled should be true after SetClaudeEnabled(true)")
	}
	if st.lastReadAt != t0.UnixMilli() {
		t.Fatalf("SetClaudeEnabled must reset the heartbeat timer to now: got %d want %d", st.lastReadAt, t0.UnixMilli())
	}

	e.SetClaudeEnabled("ABCD", false)
	applyCtl(e)
	if st.claudeEnabled {
		t.Fatalf("claudeEnabled should be false after SetClaudeEnabled(false)")
	}
}

func TestSetClaudeEnabledNoopForUnwatchedSymbol(t *testing.T) {
	e := newTest() // only "ABCD" is watched
	e.SetClaudeEnabled("ZZZZ", true)
	applyCtl(e) // must not panic despite "ZZZZ" having no symState
	if _, ok := e.syms["ZZZZ"]; ok {
		t.Fatalf("SetClaudeEnabled must not create state for an unwatched symbol")
	}
}

func TestSetCommentaryIntervalClampsToBounds(t *testing.T) {
	e := newTest()
	st := e.syms["ABCD"]

	cases := []struct{ in, want float64 }{
		{0, 0},                     // off stays off
		{-5, 0},                    // negative -> off
		{5, heartbeatMinSecs},      // below the floor -> clamped up
		{60, 60},                   // within bounds -> unchanged
		{10_000, heartbeatMaxSecs}, // above the ceiling -> clamped down
	}
	for _, c := range cases {
		e.SetCommentaryInterval("ABCD", c.in)
		applyCtl(e)
		if st.commentarySecs != c.want {
			t.Fatalf("SetCommentaryInterval(%v): got %v want %v", c.in, st.commentarySecs, c.want)
		}
	}
}

func TestSetCommentaryIntervalResetsHeartbeatTimer(t *testing.T) {
	e := newTest()
	st := e.syms["ABCD"]
	st.lastReadAt = 0

	e.SetCommentaryInterval("ABCD", 30)
	applyCtl(e)
	if st.lastReadAt != t0.UnixMilli() {
		t.Fatalf("SetCommentaryInterval must reset the heartbeat timer to now: got %d want %d", st.lastReadAt, t0.UnixMilli())
	}
}

func TestNoteReadCompleteUpdatesLastReadAt(t *testing.T) {
	e := newTest()
	st := e.syms["ABCD"]
	later := t0.Add(45 * time.Second)

	e.NoteReadComplete("ABCD", later)
	applyCtl(e)
	if st.lastReadAt != later.UnixMilli() {
		t.Fatalf("NoteReadComplete: got %d want %d", st.lastReadAt, later.UnixMilli())
	}
}

func TestNoteReadCompleteNoopForUnwatchedSymbol(t *testing.T) {
	e := newTest()
	e.NoteReadComplete("ZZZZ", t0)
	applyCtl(e) // must not panic
	if _, ok := e.syms["ZZZZ"]; ok {
		t.Fatalf("NoteReadComplete must not create state for an unwatched symbol")
	}
}

// TestCheckHeartbeatsFiresWhenDueAndWarm proves a synthetic "heartbeat"
// RegimeEvent fires once a warmed-up, Claude-enabled symbol's commentary
// interval elapses, and not before.
func TestCheckHeartbeatsFiresWhenDueAndWarm(t *testing.T) {
	e := newTest()
	tick := t0
	e.now = func() time.Time { return tick }
	ms := tick.UnixMilli()
	warmUp(e, "ABCD", ms)
	drainRegimes(e) // flush anything the warm-up trickle itself queued

	st := e.syms["ABCD"]
	st.claudeEnabled = true
	st.commentarySecs = 30
	st.lastReadAt = ms // "just read" -- not due yet

	e.checkHeartbeats()
	if evs := drainRegimes(e); len(evs) != 0 {
		t.Fatalf("must not fire before commentarySecs has elapsed, got %+v", evs)
	}

	tick = tick.Add(30 * time.Second)
	e.checkHeartbeats()
	evs := drainRegimes(e)
	if len(evs) != 1 {
		t.Fatalf("expected exactly one heartbeat event, got %d: %+v", len(evs), evs)
	}
	if evs[0].Trigger != "heartbeat" || evs[0].Sym != "ABCD" || evs[0].Headline != "Live commentary" {
		t.Fatalf("heartbeat event mismatch: %+v", evs[0])
	}
	if !evs[0].ClaudeOn {
		t.Fatalf("heartbeat event must carry ClaudeOn=true from claudeEnabled")
	}
}

// TestCheckHeartbeatsSkipsWhenClaudeDisabledOrCommentaryOff proves both gates
// are independently required.
func TestCheckHeartbeatsSkipsWhenClaudeDisabledOrCommentaryOff(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	warmUp(e, "ABCD", ms)
	drainRegimes(e)
	st := e.syms["ABCD"]

	st.claudeEnabled = false
	st.commentarySecs = 30
	st.lastReadAt = 0
	e.checkHeartbeats()
	if evs := drainRegimes(e); len(evs) != 0 {
		t.Fatalf("must not fire with Claude disabled, got %+v", evs)
	}

	st.claudeEnabled = true
	st.commentarySecs = 0
	e.checkHeartbeats()
	if evs := drainRegimes(e); len(evs) != 0 {
		t.Fatalf("must not fire with commentary off, got %+v", evs)
	}
}

// TestCheckHeartbeatsRequiresWarmup proves the same warm-up gate the six
// regime detectors use (>=warmupTrades trades, live two-sided quote) also
// gates the heartbeat.
func TestCheckHeartbeatsRequiresWarmup(t *testing.T) {
	e := newTest()
	st := e.syms["ABCD"]
	st.claudeEnabled = true
	st.commentarySecs = 15
	st.lastReadAt = 0 // long "overdue" if warm-up weren't enforced

	e.checkHeartbeats() // no trades, no quote at all yet
	if evs := drainRegimes(e); len(evs) != 0 {
		t.Fatalf("must not fire before any trades/quote, got %+v", evs)
	}

	// Enough trades, but still no live two-sided quote.
	ms := t0.UnixMilli()
	for i := 0; i < warmupTrades; i++ {
		trade(e, "ABCD", 10.00, 1, ms)
	}
	drainRegimes(e)
	e.checkHeartbeats()
	if evs := drainRegimes(e); len(evs) != 0 {
		t.Fatalf("must not fire without a live quote even with enough trades, got %+v", evs)
	}
}

// TestCheckHeartbeatsResetByNoteReadComplete proves a completed read (not
// just the passage of time since detection) is what the gate measures from --
// mirrors server.py's `_last_read_wall` semantics: a slow read pushes the
// next commentary tick out from when it actually finished.
func TestCheckHeartbeatsResetByNoteReadComplete(t *testing.T) {
	e := newTest()
	tick := t0
	e.now = func() time.Time { return tick }
	ms := tick.UnixMilli()
	warmUp(e, "ABCD", ms)
	drainRegimes(e)

	st := e.syms["ABCD"]
	st.claudeEnabled = true
	st.commentarySecs = 30
	st.lastReadAt = ms

	tick = tick.Add(20 * time.Second) // not due yet at 20s
	// A read completes now (e.g. a real regime-triggered Claude read finished).
	e.NoteReadComplete("ABCD", tick)
	applyCtl(e)

	tick = tick.Add(20 * time.Second) // 20s since the reset -- still short of 30s
	e.checkHeartbeats()
	if evs := drainRegimes(e); len(evs) != 0 {
		t.Fatalf("NoteReadComplete must push the next fire out from its own timestamp, got %+v", evs)
	}

	tick = tick.Add(10 * time.Second) // now 30s since the NoteReadComplete reset
	e.checkHeartbeats()
	if evs := drainRegimes(e); len(evs) != 1 {
		t.Fatalf("expected exactly one heartbeat once 30s have elapsed since the reset, got %+v", evs)
	}
}

// TestCheckHeartbeatsPerSymbolIndependent proves commentary/cooldown state
// never leaks across symbols.
func TestCheckHeartbeatsPerSymbolIndependent(t *testing.T) {
	e := newTest("AAAA", "BBBB")
	tick := t0
	e.now = func() time.Time { return tick }
	ms := tick.UnixMilli()
	warmUp(e, "AAAA", ms)
	warmUp(e, "BBBB", ms)
	drainRegimes(e)

	stA, stB := e.syms["AAAA"], e.syms["BBBB"]
	stA.claudeEnabled, stA.commentarySecs, stA.lastReadAt = true, 30, ms-30_000 // already due
	stB.claudeEnabled, stB.commentarySecs, stB.lastReadAt = true, 30, ms        // not due

	e.checkHeartbeats()
	evs := drainRegimes(e)
	if len(evs) != 1 || evs[0].Sym != "AAAA" {
		t.Fatalf("expected only AAAA's heartbeat to fire, got %+v", evs)
	}
}
