package persist

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func newTapeArchive(t *testing.T) core.TapeArchive {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db.TapeArchive()
}

func TestTapeArchiveRoundTrip(t *testing.T) {
	ctx := context.Background()
	r := newTapeArchive(t)

	event := core.RegimeEvent{
		Sym: "ACME", Ts: 1000, Trigger: "buy_sweep", Headline: "Buyers sweeping the offer",
		Snapshot: core.TapeSnapshot{
			Symbol: "ACME", Trigger: "buy_sweep", Headline: "Buyers sweeping the offer",
			LastPrice: 12.34, Bid: 12.33, Ask: 12.35, Spread: 0.02, BidSize: 500, AskSize: 300,
			CVDSession: 1500, Delta10s: 200, Delta60s: 900, BuyVol60s: 3000, SellVol60s: 1200,
			Prints10s: 15, PrintsPerSecNow: 3, PrintsPerSecBaseline: 1.2,
			BlockThreshold: 5000, MedianPrintSize: 200, ControlScore: 0.6,
			Book:         core.BookStats{BidAbsorb: 2, AskAbsorb: 1, DepthImbalance: 0.3, InsideImbalance: 0.5},
			BookBids:     []core.DepthLevel{{Price: 12.33, Size: 500}},
			BookAsks:     []core.DepthLevel{{Price: 12.35, Size: 300}},
			RecentPrints: []core.TapeSnapshotPrint{{Price: 12.34, Size: 200, Side: "buy", Block: false}},
		},
	}
	verdict := core.TapeVerdict{
		Control: "buyers", Confidence: 0.82, Text: "Buyers in control",
		Evidence: []string{"bid_absorb x2", "cvd rising"}, Source: "local",
	}

	if err := r.SaveTapeRead(ctx, event, verdict,
		"Buyers are lifting offers aggressively.", "watch for a pullback", "local", ""); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := r.Recent(ctx, "ACME", 10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 read, got %d", len(got))
	}
	rec := got[0]
	if rec.Sym != "ACME" || rec.Ts != 1000 || rec.Trigger != "buy_sweep" || rec.Headline != "Buyers sweeping the offer" {
		t.Fatalf("event identity mismatch: %+v", rec)
	}
	if rec.Control != "buyers" || rec.Confidence != 0.82 {
		t.Fatalf("verdict mismatch: %+v", rec)
	}
	if rec.WhatsHappening != "Buyers are lifting offers aggressively." || rec.WatchFor != "watch for a pullback" ||
		rec.Model != "local" || rec.Err != "" {
		t.Fatalf("read fields mismatch: %+v", rec)
	}
	if len(rec.Evidence) != 2 || rec.Evidence[0] != "bid_absorb x2" || rec.Evidence[1] != "cvd rising" {
		t.Fatalf("evidence json roundtrip mismatch: %+v", rec.Evidence)
	}
	snap := rec.Snapshot
	if snap.Symbol != "ACME" || snap.LastPrice != 12.34 || snap.Bid != 12.33 || snap.Ask != 12.35 ||
		snap.CVDSession != 1500 || snap.Delta10s != 200 || snap.ControlScore != 0.6 {
		t.Fatalf("snapshot json roundtrip mismatch: %+v", snap)
	}
	if len(snap.BookBids) != 1 || snap.BookBids[0].Price != 12.33 || snap.BookBids[0].Size != 500 {
		t.Fatalf("snapshot book bids roundtrip mismatch: %+v", snap.BookBids)
	}
	if len(snap.RecentPrints) != 1 || snap.RecentPrints[0].Side != "buy" || snap.RecentPrints[0].Size != 200 {
		t.Fatalf("snapshot recent prints roundtrip mismatch: %+v", snap.RecentPrints)
	}
	if snap.Book.BidAbsorb != 2 || snap.Book.AskAbsorb != 1 || snap.Book.DepthImbalance != 0.3 || snap.Book.InsideImbalance != 0.5 {
		t.Fatalf("snapshot nested book stats roundtrip mismatch: %+v", snap.Book)
	}
}

// TestTapeArchiveRecentOrderAndSymFilter checks the two things a Recent(sym,
// limit) query must get right beyond plain field fidelity: newest-first
// ordering, per-symbol filtering, and that a failed Claude read (non-empty
// Err, e.g. no evidence at all) survives too.
func TestTapeArchiveRecentOrderAndSymFilter(t *testing.T) {
	ctx := context.Background()
	r := newTapeArchive(t)

	mk := func(sym string, ts int64, trigger string) core.RegimeEvent {
		return core.RegimeEvent{Sym: sym, Ts: ts, Trigger: trigger, Headline: trigger + " headline"}
	}
	if err := r.SaveTapeRead(ctx, mk("ACME", 1000, "buy_sweep"),
		core.TapeVerdict{Control: "buyers", Confidence: 0.7}, "first read", "", "local", ""); err != nil {
		t.Fatalf("save1: %v", err)
	}
	if err := r.SaveTapeRead(ctx, mk("ACME", 2000, "cvd_flip"),
		core.TapeVerdict{Control: "sellers", Confidence: 0.9}, "", "", "claude-x", "read failed: timeout"); err != nil {
		t.Fatalf("save2: %v", err)
	}
	if err := r.SaveTapeRead(ctx, mk("BETA", 1500, "block_print"),
		core.TapeVerdict{Control: "balanced", Confidence: 0.5}, "beta read", "", "local", ""); err != nil {
		t.Fatalf("save3: %v", err)
	}

	got, err := r.Recent(ctx, "ACME", 10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(got) != 2 || got[0].Trigger != "cvd_flip" || got[1].Trigger != "buy_sweep" {
		t.Fatalf("want [cvd_flip, buy_sweep] newest-first for ACME only, got %+v", got)
	}
	// Failed-read fields (empty evidence, non-empty error) round-trip cleanly.
	if got[0].Err != "read failed: timeout" || got[0].Model != "claude-x" || len(got[0].Evidence) != 0 {
		t.Fatalf("failed-read fields mismatch: %+v", got[0])
	}

	betaGot, err := r.Recent(ctx, "BETA", 10)
	if err != nil {
		t.Fatalf("recent beta: %v", err)
	}
	if len(betaGot) != 1 || betaGot[0].Trigger != "block_print" {
		t.Fatalf("sym filter leaked across symbols: %+v", betaGot)
	}

	// limit is respected even when more rows exist.
	limited, err := r.Recent(ctx, "ACME", 1)
	if err != nil {
		t.Fatalf("recent limited: %v", err)
	}
	if len(limited) != 1 || limited[0].Trigger != "cvd_flip" {
		t.Fatalf("limit=1 should keep only the newest row, got %+v", limited)
	}
}
