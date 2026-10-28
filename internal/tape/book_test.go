package tape

import (
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// quoteSz applies a quote with explicit bid/ask sizes -- quote() in
// tape_test.go hardcodes 100/100, too coarse for book-event classification,
// which is driven entirely by how sizes change between quotes.
func quoteSz(e *Engine, sym string, bid float64, bidSz int64, ask float64, askSz int64, atMs int64) {
	e.apply(core.TapeEvent{Quote: &core.Quote{Sym: sym, Bid: bid, BidSz: bidSz, Ask: ask, AskSz: askSz, TS: atMs}})
}

func TestBookRefillAfterHitClassifiesAsAbsorb(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quoteSz(e, "ABCD", 99.99, 1000, 100.01, 1000, ms) // establish the inside quote

	for i := 0; i < 4; i++ {
		trade(e, "ABCD", 99.99, 500, ms)                  // sellers hit the bid
		quoteSz(e, "ABCD", 99.99, 300, 100.01, 1000, ms)  // size drops -- consumed by the trade, not a pull
		quoteSz(e, "ABCD", 99.99, 1500, 100.01, 1000, ms) // ...and refreshes at the SAME price
	}

	st := e.syms["ABCD"]
	if st.bidRefills < 3 {
		t.Fatalf("bidRefills: got %d want >= 3", st.bidRefills)
	}
	if st.bidLevelVol != 2000 {
		t.Fatalf("bidLevelVol: got %v want 2000 (4 x 500 sold into it)", st.bidLevelVol)
	}
	if st.bid != 99.99 {
		t.Fatalf("bid should never have moved -- it's being defended, got %v", st.bid)
	}

	b := e.bookStats(st, bookLookback)
	if b.BidAbsorb < 3 {
		t.Fatalf("BookStats.BidAbsorb: got %d want >= 3", b.BidAbsorb)
	}
	if b.BidRefills < 3 {
		t.Fatalf("BookStats.BidRefills: got %d want >= 3", b.BidRefills)
	}
	if b.BidAbsorbedVol != 2000 {
		t.Fatalf("BookStats.BidAbsorbedVol: got %v want 2000", b.BidAbsorbedVol)
	}
}

func TestBookSizeAddedWithNoPriorTradeIsAdd(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quoteSz(e, "ABCD", 99.99, 1000, 100.01, 1000, ms)
	// bid size grows with nobody hitting it; ask size shrinks with no trade either
	quoteSz(e, "ABCD", 99.99, 2000, 100.01, 200, ms)

	st := e.syms["ABCD"]
	b := e.bookStats(st, bookLookback)
	if b.BidAdd != 1 || b.BidAbsorb != 0 {
		t.Fatalf("bidAdd=%d bidAbsorb=%d, want add=1 absorb=0 (size added, never hit)", b.BidAdd, b.BidAbsorb)
	}
	if b.AskPull != 1 {
		t.Fatalf("askPull: got %d want 1", b.AskPull)
	}
}

func TestBookSizeVanishingWithZeroTradesIsPull(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quoteSz(e, "ABCD", 1.82, 5000, 1.83, 2000, ms)
	for _, sz := range []int64{3800, 2600, 1500, 700, 300} { // size vanishing, no trades in between
		quoteSz(e, "ABCD", 1.82, sz, 1.83, 2000, ms)
	}
	st := e.syms["ABCD"]
	b := e.bookStats(st, bookLookback)
	if b.BidPull != 5 {
		t.Fatalf("bidPull: got %d want 5", b.BidPull)
	}
}

func TestBookSizeDropAfterATradeIsNotAPull(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quoteSz(e, "ABCD", 99.99, 1000, 100.01, 1000, ms)
	trade(e, "ABCD", 99.99, 800, ms) // hits the bid
	quoteSz(e, "ABCD", 99.99, 200, 100.01, 1000, ms)

	st := e.syms["ABCD"]
	b := e.bookStats(st, bookLookback)
	if b.BidPull != 0 {
		t.Fatalf("bidPull: got %d want 0 -- size was consumed by a trade, not pulled", b.BidPull)
	}
}

func TestBookPriceMoveResetsOnlyThatSidesAccumulators(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quoteSz(e, "ABCD", 99.99, 1000, 100.01, 1000, ms)
	trade(e, "ABCD", 99.99, 500, ms)                  // sold into the bid
	trade(e, "ABCD", 100.01, 300, ms)                 // bought from the offer
	quoteSz(e, "ABCD", 99.99, 1500, 100.01, 1300, ms) // both sides refill at the same prices

	st := e.syms["ABCD"]
	if st.bidLevelVol != 500 || st.askLevelVol != 300 {
		t.Fatalf("levelVol before the move: bid=%v ask=%v, want bid=500 ask=300", st.bidLevelVol, st.askLevelVol)
	}
	if st.bidRefills != 1 || st.askRefills != 1 {
		t.Fatalf("refills before the move: bid=%d ask=%d, want bid=1 ask=1", st.bidRefills, st.askRefills)
	}

	quoteSz(e, "ABCD", 99.98, 800, 100.01, 1300, ms) // bid steps down; ask untouched

	if st.bidLevelVol != 0 || st.bidRefills != 0 {
		t.Fatalf("bid side should reset on its own price move: vol=%v refills=%d", st.bidLevelVol, st.bidRefills)
	}
	if st.askLevelVol != 300 || st.askRefills != 1 {
		t.Fatalf("ask side must be untouched by the bid's price move: vol=%v refills=%d", st.askLevelVol, st.askRefills)
	}
	b := e.bookStats(st, bookLookback)
	if b.BidDown != 1 {
		t.Fatalf("BidDown: got %d want 1", b.BidDown)
	}
}

func TestBookStatsSeparatesWindowedCountsFromLiveAccumulators(t *testing.T) {
	e := newTest()
	tick := t0
	e.now = func() time.Time { return tick }
	ms := tick.UnixMilli()
	quoteSz(e, "ABCD", 99.99, 1000, 100.01, 1000, ms)
	trade(e, "ABCD", 99.99, 500, ms)
	quoteSz(e, "ABCD", 99.99, 1500, 100.01, 1000, ms) // the one refill/absorb event, timestamped "now"

	tick = tick.Add(60 * time.Second) // now well past the 30s lookback

	st := e.syms["ABCD"]
	b := e.bookStats(st, bookLookback)
	if b.BidAbsorb != 0 {
		t.Fatalf("the absorb EVENT is 60s old, outside the 30s lookback window: got %d want 0", b.BidAbsorb)
	}
	if b.BidRefills != 1 || b.BidAbsorbedVol != 500 {
		t.Fatalf("live refill/absorbed-volume counters must NOT be time-windowed: refills=%d vol=%v", b.BidRefills, b.BidAbsorbedVol)
	}
}

func TestBookStatsWindowsOldEventsOutButKeepsRecentOnes(t *testing.T) {
	e := newTest()
	tick := t0
	e.now = func() time.Time { return tick }
	ms := tick.UnixMilli()
	quoteSz(e, "ABCD", 99.99, 1000, 100.01, 1000, ms)
	quoteSz(e, "ABCD", 99.99, 2000, 100.01, 1000, ms) // an old bid_add

	tick = tick.Add(60 * time.Second)                               // past the lookback
	quoteSz(e, "ABCD", 99.99, 3000, 100.01, 1000, tick.UnixMilli()) // a fresh bid_add

	st := e.syms["ABCD"]
	b := e.bookStats(st, bookLookback)
	if b.BidAdd != 1 {
		t.Fatalf("windowed BidAdd should only count the recent event, got %d want 1", b.BidAdd)
	}
}

func TestBookStatsInsideImbalanceFromLiveQuoteSizes(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quoteSz(e, "ABCD", 99.99, 3000, 100.01, 1000, ms)
	st := e.syms["ABCD"]
	b := e.bookStats(st, bookLookback)
	want := float64(3000-1000) / float64(3000+1000)
	if b.InsideImbalance != want {
		t.Fatalf("insideImbalance: got %v want %v", b.InsideImbalance, want)
	}
}
