package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/summarize"
)

// startOrchestrator wires runTapeRegimeOrchestrator with buffered channels and
// starts it on its own goroutine, cancelled at test cleanup.
func startOrchestrator(
	t *testing.T,
	readControl func(ctx context.Context, snap core.TapeSnapshot, headline string) (summarize.ControlRead, error),
	rowVWAP func(sym string) (vwap, hod, lod float64, ok bool),
	saveRead func(ctx context.Context, event core.RegimeEvent, verdict core.TapeVerdict, whatsHappening, watchFor, model, errMsg string) error,
	noteReadComplete func(sym string, at time.Time),
) (events chan core.RegimeEvent, regimes chan core.TapeRegimeMsg, cards chan core.TapeCardMsg) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	events = make(chan core.RegimeEvent, 16)
	regimes = make(chan core.TapeRegimeMsg, 16)
	cards = make(chan core.TapeCardMsg, 16)
	go runTapeRegimeOrchestrator(ctx, events, regimes, cards, readControl, rowVWAP, saveRead, noteReadComplete)
	return events, regimes, cards
}

func noRowVWAP(string) (float64, float64, float64, bool) { return 0, 0, 0, false }

// TestOrchestratorNonHeartbeatSendsRegimeMsgAndLocalCard proves a regular
// (non-heartbeat) regime event with Claude off produces both an instant
// TapeRegimeMsg (from the local verdict) and a local-mode TapeCardMsg, and
// that it gets archived under model "local".
func TestOrchestratorNonHeartbeatSendsRegimeMsgAndLocalCard(t *testing.T) {
	var savedModel atomic.Value
	saveRead := func(_ context.Context, _ core.RegimeEvent, _ core.TapeVerdict, whatsHappening, watchFor, model, errMsg string) error {
		savedModel.Store(model)
		return nil
	}
	events, regimes, cards := startOrchestrator(t, nil, noRowVWAP, saveRead, nil)

	ev := core.RegimeEvent{
		Sym: "AAPL", Ts: 100, Trigger: "block_print", Headline: "Block print", ClaudeOn: false,
		Verdict: core.TapeVerdict{Control: "buyers", Confidence: 0.6, Text: "buyers stepping in", Source: "local"},
	}
	events <- ev

	select {
	case rg := <-regimes:
		if rg.Sym != "AAPL" || rg.Trigger != "block_print" || rg.Verdict.Control != "buyers" {
			t.Fatalf("tape regime mismatch: %+v", rg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected a TapeRegimeMsg for a non-heartbeat event")
	}

	select {
	case c := <-cards:
		if c.Sym != "AAPL" || c.Model != "local" || c.Control != "buyers" || c.WhatsHappening != "buyers stepping in" || c.Confidence != 0.6 {
			t.Fatalf("tape card mismatch: %+v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected a local TapeCardMsg")
	}

	deadline := time.Now().Add(2 * time.Second)
	for savedModel.Load() == nil {
		if time.Now().After(deadline) {
			t.Fatal("saveRead was never called")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := savedModel.Load().(string); got != "local" {
		t.Fatalf("archived model: got %q want local", got)
	}
}

// TestOrchestratorHeartbeatSkipsRegimeMsg proves a heartbeat event never
// produces an instant TapeRegimeMsg (it carries no headline news to alert on)
// but still produces a card.
func TestOrchestratorHeartbeatSkipsRegimeMsg(t *testing.T) {
	events, regimes, cards := startOrchestrator(t, nil, noRowVWAP, func(context.Context, core.RegimeEvent, core.TapeVerdict, string, string, string, string) error {
		return nil
	}, nil)

	events <- core.RegimeEvent{
		Sym: "AAPL", Ts: 100, Trigger: "heartbeat", Headline: "Live commentary", ClaudeOn: false,
		Verdict: core.TapeVerdict{Control: "balanced", Confidence: 0.4, Text: "two-sided", Source: "local"},
	}

	select {
	case rg := <-regimes:
		t.Fatalf("heartbeat must not produce a TapeRegimeMsg, got %+v", rg)
	case <-time.After(200 * time.Millisecond):
	}

	select {
	case c := <-cards:
		if c.Trigger != "heartbeat" || c.Model != "local" {
			t.Fatalf("card mismatch: %+v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected a card for the heartbeat event")
	}
}

// TestOrchestratorClaudeOnPopulatesVWAPAndUsesReadResult proves that, for a
// Claude-enabled symbol, the snapshot handed to readControl has Vwap/
// SessionHigh/SessionLow filled in from rowVWAP (left zero by the tape
// engine), and that the resulting card/persisted verdict come from the
// ControlRead, not the local verdict.
func TestOrchestratorClaudeOnPopulatesVWAPAndUsesReadResult(t *testing.T) {
	var gotSnap core.TapeSnapshot
	readControl := func(_ context.Context, snap core.TapeSnapshot, headline string) (summarize.ControlRead, error) {
		gotSnap = snap
		return summarize.ControlRead{
			Control: "sellers", Confidence: 0.8, WhatsHappening: "sellers pressing", WatchFor: "a vwap reclaim",
			Model: "claude-sonnet-5",
		}, nil
	}
	rowVWAP := func(sym string) (float64, float64, float64, bool) {
		if sym != "AAPL" {
			t.Fatalf("unexpected sym for rowVWAP: %s", sym)
		}
		return 12.34, 15.0, 9.0, true
	}
	var savedVerdict atomic.Value
	saveRead := func(_ context.Context, _ core.RegimeEvent, verdict core.TapeVerdict, whatsHappening, watchFor, model, errMsg string) error {
		savedVerdict.Store(verdict)
		return nil
	}
	var notedSym atomic.Value
	noteReadComplete := func(sym string, _ time.Time) { notedSym.Store(sym) }

	events, _, cards := startOrchestrator(t, readControl, rowVWAP, saveRead, noteReadComplete)
	events <- core.RegimeEvent{
		Sym: "AAPL", Ts: 100, Trigger: "block_print", Headline: "Block print", ClaudeOn: true,
		Verdict:  core.TapeVerdict{Control: "buyers", Confidence: 0.6, Text: "local text", Source: "local"},
		Snapshot: core.TapeSnapshot{Symbol: "AAPL"},
	}

	select {
	case c := <-cards:
		if c.Model != "claude-sonnet-5" || c.Control != "sellers" || c.WhatsHappening != "sellers pressing" || c.WatchFor != "a vwap reclaim" || c.Confidence != 0.8 {
			t.Fatalf("card should reflect the Claude read, got %+v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected a card built from the Claude read")
	}

	if gotSnap.Vwap != 12.34 || gotSnap.SessionHigh != 15.0 || gotSnap.SessionLow != 9.0 {
		t.Fatalf("snapshot handed to readControl missing row VWAP/session high/low: %+v", gotSnap)
	}

	deadline := time.Now().Add(2 * time.Second)
	for savedVerdict.Load() == nil {
		if time.Now().After(deadline) {
			t.Fatal("saveRead was never called")
		}
		time.Sleep(5 * time.Millisecond)
	}
	v := savedVerdict.Load().(core.TapeVerdict)
	if v.Control != "sellers" || v.Source != "api" {
		t.Fatalf("persisted verdict should be the Claude read, got %+v", v)
	}

	deadline = time.Now().Add(2 * time.Second)
	for notedSym.Load() == nil {
		if time.Now().After(deadline) {
			t.Fatal("noteReadComplete was never called")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if notedSym.Load().(string) != "AAPL" {
		t.Fatalf("noteReadComplete sym: got %v want AAPL", notedSym.Load())
	}
}

// TestOrchestratorUnavailableFallsBackToLocalCard proves
// ErrTapeControlUnavailable is treated exactly like the Claude-off path: a
// local-mode card built from the event's own verdict, not an error card.
func TestOrchestratorUnavailableFallsBackToLocalCard(t *testing.T) {
	readControl := func(context.Context, core.TapeSnapshot, string) (summarize.ControlRead, error) {
		return summarize.ControlRead{}, summarize.ErrTapeControlUnavailable
	}
	var noted atomic.Bool
	noteReadComplete := func(string, time.Time) { noted.Store(true) }

	events, _, cards := startOrchestrator(t, readControl, noRowVWAP, func(context.Context, core.RegimeEvent, core.TapeVerdict, string, string, string, string) error {
		return nil
	}, noteReadComplete)
	events <- core.RegimeEvent{
		Sym: "AAPL", Ts: 100, Trigger: "block_print", Headline: "Block print", ClaudeOn: true,
		Verdict: core.TapeVerdict{Control: "buyers", Confidence: 0.6, Text: "local text", Source: "local"},
	}

	select {
	case c := <-cards:
		if c.Model != "local" || c.Control != "buyers" || c.WhatsHappening != "local text" || c.Confidence != 0.6 || c.Err != "" {
			t.Fatalf("expected a clean local-mode fallback card, got %+v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected a fallback card")
	}

	deadline := time.Now().Add(2 * time.Second)
	for !noted.Load() {
		if time.Now().After(deadline) {
			t.Fatal("noteReadComplete should still fire on the unavailable-sentinel fallback")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestOrchestratorBusyDropsConcurrentEventForSameSymbol proves a second event
// for a symbol already awaiting a Claude read (e.g. a heartbeat firing again
// before the first read completed) is dropped rather than stacking a second
// concurrent call, and that the symbol becomes available again once the first
// read completes.
func TestOrchestratorBusyDropsConcurrentEventForSameSymbol(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	readControl := func(ctx context.Context, _ core.TapeSnapshot, _ string) (summarize.ControlRead, error) {
		calls.Add(1)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return summarize.ControlRead{Model: "local"}, nil
	}
	events, _, cards := startOrchestrator(t, readControl, noRowVWAP, func(context.Context, core.RegimeEvent, core.TapeVerdict, string, string, string, string) error {
		return nil
	}, func(string, time.Time) {})

	mkEv := func() core.RegimeEvent {
		return core.RegimeEvent{Sym: "AAPL", Ts: 100, Trigger: "heartbeat", Headline: "x", ClaudeOn: true}
	}
	events <- mkEv()
	// Give the first read time to actually start (and register its call) before
	// firing the second.
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("first read never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	events <- mkEv() // dropped: AAPL is busy

	time.Sleep(100 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 readControl call while busy, got %d", got)
	}

	close(release) // let the first read finish
	select {
	case <-cards:
	case <-time.After(2 * time.Second):
		t.Fatal("expected a card once the first read completes")
	}

	// AAPL is no longer busy once the main loop processes the completion
	// signal (a `done` receive) -- that happens asynchronously just after the
	// card was sent, so retry the send rather than racing a single attempt
	// against it: each retry that lands while still (briefly) busy is safely
	// dropped by design, and exactly one succeeds once busy actually clears.
	deadline = time.Now().Add(2 * time.Second)
	for calls.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("expected a second readControl call once AAPL freed up, got %d calls", calls.Load())
		}
		events <- mkEv()
		time.Sleep(20 * time.Millisecond)
	}
}

// TestOrchestratorConcurrentSymbolsIndependent proves busy-tracking is keyed
// per symbol: a slow read in flight for one symbol must never block another
// symbol's read from starting.
func TestOrchestratorConcurrentSymbolsIndependent(t *testing.T) {
	blockAAPL := make(chan struct{})
	readControl := func(ctx context.Context, snap core.TapeSnapshot, _ string) (summarize.ControlRead, error) {
		if snap.Symbol == "AAPL" {
			select {
			case <-blockAAPL:
			case <-ctx.Done():
			}
		}
		return summarize.ControlRead{Model: "local"}, nil
	}
	events, _, cards := startOrchestrator(t, readControl, noRowVWAP, func(context.Context, core.RegimeEvent, core.TapeVerdict, string, string, string, string) error {
		return nil
	}, func(string, time.Time) {})

	events <- core.RegimeEvent{Sym: "AAPL", Trigger: "block_print", ClaudeOn: true, Snapshot: core.TapeSnapshot{Symbol: "AAPL"}}
	events <- core.RegimeEvent{Sym: "MSFT", Trigger: "block_print", ClaudeOn: true, Snapshot: core.TapeSnapshot{Symbol: "MSFT"}}

	// MSFT's read isn't blocked, so its card must arrive even while AAPL's is stuck.
	select {
	case c := <-cards:
		if c.Sym != "MSFT" {
			t.Fatalf("expected MSFT's card first, got %+v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("MSFT's read must not be blocked by AAPL's in-flight read")
	}
	close(blockAAPL)
	select {
	case c := <-cards:
		if c.Sym != "AAPL" {
			t.Fatalf("expected AAPL's card after unblocking, got %+v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AAPL's card never arrived after unblocking")
	}
}

// TestOrchestratorPersistsErrorMessageFromControlRead proves a Claude call
// that comes back with a populated Err (network failure, refusal, unparseable
// output -- all valid nil-error ControlReads per summarize.ReadTapeControl)
// still produces a card carrying that Err, and archives it under the read's
// own Model.
func TestOrchestratorPersistsErrorMessageFromControlRead(t *testing.T) {
	readControl := func(context.Context, core.TapeSnapshot, string) (summarize.ControlRead, error) {
		return summarize.ControlRead{
			Control: "balanced", Confidence: 0, WhatsHappening: "Block print",
			Model: "claude-sonnet-5", Err: "Claude request failed: boom",
		}, nil
	}
	var savedErr atomic.Value
	saveRead := func(_ context.Context, _ core.RegimeEvent, _ core.TapeVerdict, _, _, _, errMsg string) error {
		savedErr.Store(errMsg)
		return nil
	}
	events, _, cards := startOrchestrator(t, readControl, noRowVWAP, saveRead, func(string, time.Time) {})
	events <- core.RegimeEvent{Sym: "AAPL", Trigger: "block_print", Headline: "Block print", ClaudeOn: true}

	select {
	case c := <-cards:
		if c.Err != "Claude request failed: boom" {
			t.Fatalf("card should carry the read's Err, got %+v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected a card")
	}
	deadline := time.Now().Add(2 * time.Second)
	for savedErr.Load() == nil {
		if time.Now().After(deadline) {
			t.Fatal("saveRead was never called")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := savedErr.Load().(string); got != "Claude request failed: boom" {
		t.Fatalf("archived error: got %q", got)
	}
}
