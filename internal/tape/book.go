// Level-1 book-event classification: inside-size dynamics derived purely from
// NBBO quote deltas (no L2 depth feed required). A bid whose size keeps
// replenishing at the same price after being traded into is being defended
// (absorption); size vanishing with no trades against it is being pulled;
// size merely appearing on a quiet quote is a weak "add"; the level itself
// stepping to a new price resets that side's accumulators. This is the Go
// mirror of engine.py's on_quote()/book_stats().
package tape

import (
	"math"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

const (
	// bookEventsKept caps the book-event ring -- mirrors engine.py's
	// `_book_events: Deque[tuple] = deque(maxlen=400)`.
	bookEventsKept = 400

	// bookLookback is the trailing window bookStats() aggregates event counts
	// over -- mirrors engine.py's book_stats(lookback: float = 30.0).
	bookLookback = 30 * time.Second

	// priceEq is the floating-point tolerance for "same price level",
	// matching engine.py's on_quote() (`abs(t.price - self.bid) < 1e-9`).
	priceEq = 1e-9

	// Book-event kinds, recorded in bookEvent.kind. Values match the Python
	// dict keys 1:1 (mapped to core.BookStats' counter fields by bookStats()).
	evBidAbsorb = "bid_absorb"
	evAskAbsorb = "ask_absorb"
	evBidAdd    = "bid_add"
	evAskAdd    = "ask_add"
	evBidPull   = "bid_pull"
	evAskPull   = "ask_pull"
	evBidUp     = "bid_up"
	evBidDown   = "bid_down"
	evAskUp     = "ask_up"
	evAskDown   = "ask_down"
)

// bookEvent is one classified inside-quote change, timestamped so bookStats
// can window it.
type bookEvent struct {
	ts   int64 // ms epoch (the quote's own TS)
	kind string
}

// noteBookEvent appends a classified event to st's capped ring.
func noteBookEvent(st *symState, ts int64, kind string) {
	st.bookEvents = append(st.bookEvents, bookEvent{ts: ts, kind: kind})
	if len(st.bookEvents) > bookEventsKept {
		st.bookEvents = st.bookEvents[1:]
	}
}

// classifyQuote compares an incoming NBBO quote against the quote that just
// prevailed (st.bid/st.ask/st.bidSz/st.askSz) and classifies what changed at
// each side independently: refill-after-a-hit is absorption, size appearing
// with nothing having traded against it is a mere add, size vanishing with
// zero trades since the last quote is a pull, and the level itself moving to
// a new price resets THAT side's live accumulators only. Must be called
// BEFORE the caller overwrites st.bid/st.ask/st.bidSz/st.askSz with q's
// values -- this is the Go mirror of engine.py's on_quote().
func classifyQuote(st *symState, q *core.Quote) {
	ts := q.TS
	// Snapshot "trades since the last quote" once, shared by both sides below
	// -- matching engine.py's on_quote(), which computes `traded` a single
	// time up top and reuses it for both the bid and ask pull checks.
	traded := st.trades - st.tradesAtQuote

	if st.bid > 0 && q.Bid > 0 {
		if q.Bid == st.bid {
			d := q.BidSz - st.bidSz
			switch {
			case d > 0:
				if st.bidLevelVol > 0 {
					// size came back AFTER sellers hit this bid -- it's being
					// defended. This is the real "refreshing bid".
					st.bidRefills++
					noteBookEvent(st, ts, evBidAbsorb)
				} else {
					noteBookEvent(st, ts, evBidAdd) // size added, nobody hitting it
				}
			case d < 0 && traded == 0:
				noteBookEvent(st, ts, evBidPull) // size vanished with no trade
			}
		} else {
			if q.Bid < st.bid {
				noteBookEvent(st, ts, evBidDown)
			} else {
				noteBookEvent(st, ts, evBidUp)
			}
			st.bidLevelVol = 0 // new level, start again
			st.bidRefills = 0
		}
	}
	if st.ask > 0 && q.Ask > 0 {
		if q.Ask == st.ask {
			d := q.AskSz - st.askSz
			switch {
			case d > 0:
				if st.askLevelVol > 0 {
					st.askRefills++
					noteBookEvent(st, ts, evAskAbsorb)
				} else {
					noteBookEvent(st, ts, evAskAdd)
				}
			case d < 0 && traded == 0:
				noteBookEvent(st, ts, evAskPull)
			}
		} else {
			if q.Ask > st.ask {
				noteBookEvent(st, ts, evAskUp)
			} else {
				noteBookEvent(st, ts, evAskDown)
			}
			st.askLevelVol = 0
			st.askRefills = 0
		}
	}
	st.tradesAtQuote = st.trades
}

// accumulateLevelVol adds a classified trade's size into bidLevelVol (sold
// into the current bid) or askLevelVol (bought from the current offer) when
// it traded AT the inside -- how much that level is soaking up. Must be
// called right after the trade's side is classified and before it's folded
// into the session's other totals, matching engine.py's on_trade() control
// flow exactly.
func accumulateLevelVol(st *symState, side string, price float64, size int64) {
	switch {
	case side == "sell" && st.bid > 0 && math.Abs(price-st.bid) < priceEq:
		st.bidLevelVol += float64(size)
	case side == "buy" && st.ask > 0 && math.Abs(price-st.ask) < priceEq:
		st.askLevelVol += float64(size)
	}
}

// bookStats aggregates st's book-event ring (counts windowed to lookback)
// plus the live, unwindowed refill/absorbed-volume/inside-imbalance state --
// the Go mirror of engine.py's book_stats(). L2 depth (BidDepth/AskDepth/
// DepthImbalance) is left zero here: this stage is Level-1 only, a later
// stage wires an actual depth ladder in.
func (e *Engine) bookStats(st *symState, lookback time.Duration) core.BookStats {
	cutoff := e.now().Add(-lookback).UnixMilli()
	var bs core.BookStats
	for _, ev := range st.bookEvents {
		if ev.ts < cutoff {
			continue
		}
		switch ev.kind {
		case evBidAbsorb:
			bs.BidAbsorb++
		case evAskAbsorb:
			bs.AskAbsorb++
		case evBidAdd:
			bs.BidAdd++
		case evAskAdd:
			bs.AskAdd++
		case evBidPull:
			bs.BidPull++
		case evAskPull:
			bs.AskPull++
		case evBidUp:
			bs.BidUp++
		case evBidDown:
			bs.BidDown++
		case evAskUp:
			bs.AskUp++
		case evAskDown:
			bs.AskDown++
		}
	}
	bs.BidRefills = st.bidRefills
	bs.AskRefills = st.askRefills
	bs.BidAbsorbedVol = st.bidLevelVol
	bs.AskAbsorbedVol = st.askLevelVol
	if v := st.bidSz + st.askSz; v > 0 {
		bs.InsideImbalance = float64(st.bidSz-st.askSz) / float64(v)
	}
	return bs
}
