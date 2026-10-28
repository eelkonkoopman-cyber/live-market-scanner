package twslink

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// TestLiveTWS drives a REAL, running TWS: connects, subscribes to a display
// group, resolves a ticker to its conId, and tells the group to display it.
// Watch TWS — any window chained to the group should switch symbol.
//
// It sends ONLY handshake / symbol-lookup / display-group messages; this
// package has no order encoder at all, so it cannot place a trade.
//
//	SCANNER_LIVE_TWS_TEST=1 go test ./internal/twslink -run TestLiveTWS -v
//	  optional: SCANNER_LIVE_SYM=AAPL  SCANNER_TWS_GROUP=1
func TestLiveTWS(t *testing.T) {
	if os.Getenv("SCANNER_LIVE_TWS_TEST") == "" {
		t.Skip("set SCANNER_LIVE_TWS_TEST=1 to drive a real TWS")
	}
	sym := os.Getenv("SCANNER_LIVE_SYM")
	if sym == "" {
		sym = "AAPL"
	}
	group := 1
	if g := os.Getenv("SCANNER_TWS_GROUP"); g != "" {
		if n, err := strconv.Atoi(g); err == nil && n >= 1 && n <= 7 {
			group = n
		}
	}

	c := New(Config{Enabled: true, Group: group, ClientID: 78, LiveShort: true})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	// Wait for the link to come up.
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if up, _ := c.Status(); up {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	up, errMsg := c.Status()
	if !up {
		t.Fatalf("did not connect to TWS: %s", errMsg)
	}
	t.Logf("connected to TWS, driving display group %d", group)

	c.Show(sym)
	time.Sleep(4 * time.Second) // allow lookup + updateDisplayGroup round-trip

	// The conId cache is the proof the lookup resolved and we had something
	// real to push (updateDisplayGroup itself is fire-and-forget by design).
	id, ok := c.ConID(sym)
	if !ok {
		t.Fatalf("symbol %s never resolved to a conId — check TWS API permissions", sym)
	}
	t.Logf("resolved %s -> conId %d, pushed %d@%s to group %d", sym, id, id, c.cfg.Exchange, group)

	// Live borrow availability (generic tick 236). The indicator (tick 46) and
	// the exact share count (tick 89) arrive as SEPARATE ticks, so keep
	// draining for the whole window instead of returning on the first one.
	deadline = time.Now().Add(12 * time.Second)
	var live core.ShortLive
	ticks := 0
	for time.Now().Before(deadline) {
		select {
		case live = <-c.ShortLive():
			ticks++
		case <-time.After(500 * time.Millisecond):
		}
	}
	if ticks == 0 {
		t.Logf("no shortability tick in 12s — %s likely lacks a Level 1 market data subscription", sym)
		return
	}
	t.Logf("live borrow %s after %d ticks: indicator=%.1f (%s) shares=%d",
		live.Sym, ticks, live.Indicator, core.ShortLiveLabel(live.Indicator), live.Shares)
	if live.Shares < 0 {
		t.Logf("note: exact share count (tick 89) not delivered — needs TWS build 974+ and IB may omit it for deep names")
	}
	if _, errNow := c.Status(); errNow != "" {
		t.Logf("note: last link error: %s", errNow)
	}
}

// TestLiveTWSDepth drives a REAL, running TWS: connects, resolves a ticker,
// requests real Level 2 market depth (reqMktDepth), and logs whatever comes
// back -- a real book, a specific TWS error (e.g. "not entitled"-shaped), or
// silence. This is the only way to learn whether the account actually has a
// paid Level 2 subscription for a given venue; it's opt-in and not run by
// the normal test suite.
//
//	SCANNER_LIVE_TWS_DEPTH_TEST=1 go test ./internal/twslink -run TestLiveTWSDepth -v
//	  optional: SCANNER_LIVE_SYM=AAPL
func TestLiveTWSDepth(t *testing.T) {
	if os.Getenv("SCANNER_LIVE_TWS_DEPTH_TEST") == "" {
		t.Skip("set SCANNER_LIVE_TWS_DEPTH_TEST=1 to drive a real TWS")
	}
	sym := os.Getenv("SCANNER_LIVE_SYM")
	if sym == "" {
		sym = "AAPL"
	}

	c := New(Config{Enabled: true, ClientID: 79, LiveDepth: true})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if up, _ := c.Status(); up {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	up, errMsg := c.Status()
	if !up {
		t.Fatalf("did not connect to TWS: %s", errMsg)
	}
	t.Logf("connected to TWS, requesting Level 2 depth for %s", sym)

	c.Show(sym) // triggers the symbol lookup + push chain that watchDepth hangs off

	deadline = time.Now().Add(15 * time.Second)
	var last core.DepthUpdate
	updates := 0
	for time.Now().Before(deadline) {
		select {
		case last = <-c.Depth():
			updates++
			t.Logf("depth update %d: sym=%s bids=%d asks=%d note=%q", updates, last.Sym, len(last.Bids), len(last.Asks), last.Note)
		case <-time.After(500 * time.Millisecond):
		}
	}
	if updates == 0 {
		t.Logf("no depth update in 15s for %s -- either not entitled for this venue (TWS stayed silent, no error) or the account lacks a Level 2 subscription", sym)
		return
	}
	if last.Note != "" {
		t.Logf("TWS reported: %s", last.Note)
		return
	}
	t.Logf("real Level 2 confirmed for %s: %d bid levels, %d ask levels", sym, len(last.Bids), len(last.Asks))
	for i, lvl := range last.Bids {
		t.Logf("  bid[%d] %.4f x %.0f", i, lvl.Price, lvl.Size)
	}
	for i, lvl := range last.Asks {
		t.Logf("  ask[%d] %.4f x %.0f", i, lvl.Price, lvl.Size)
	}
}
