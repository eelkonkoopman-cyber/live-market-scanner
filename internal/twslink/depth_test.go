package twslink

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// The outgoing REQ_MKT_DEPTH field sequence must match exactly what the
// reference IB client sends: VERSION(5), reqId, conId, empty contract
// fields, exchange, "USD", numRows, isSmartDepth(1), empty mktDataOptions.
func TestWatchDepthSendsExactFieldSequence(t *testing.T) {
	var buf bytes.Buffer
	c := New(Config{Enabled: true, Exchange: "SMART", LiveDepth: true})
	c.conn = &nopConn{w: &buf}
	c.subscribed = true

	c.watchDepth(111)

	want := strings.Join([]string{
		"10", "5", "9003", "111", "", "STK", "", "", "", "", "SMART", "", "USD", "", "", "10", "1", "",
	}, "\x00") + "\x00"
	if got := buf.String()[4:]; got != want {
		t.Fatalf("payload =\n%q\nwant\n%q", got, want)
	}
	if !c.depthOpen {
		t.Fatal("depthOpen should be true after a successful request")
	}
}

// Switching symbols must cancel the previous depth subscription before
// opening a new one, so the client never holds more than one depth line —
// the same discipline TestWatchShortHoldsExactlyOneLine locks in for borrow.
func TestWatchDepthHoldsExactlyOneLine(t *testing.T) {
	var buf bytes.Buffer
	c := New(Config{Enabled: true, LiveDepth: true})
	c.conn = &nopConn{w: &buf}
	c.subscribed = true

	c.watchDepth(111)
	first := buf.String()
	if strings.Contains(first, strconv.Itoa(outCancelMktDepth)+"\x001\x00") {
		t.Fatal("should not cancel when no subscription is open")
	}

	buf.Reset()
	c.watchDepth(222)
	second := buf.String()
	cancelPrefix := strconv.Itoa(outCancelMktDepth) + "\x001\x00" + strconv.Itoa(depthReqID)
	if !strings.HasPrefix(second[4:], cancelPrefix) {
		t.Fatalf("second watch must cancel first: %q", second)
	}
	if !strings.Contains(second, "222") {
		t.Fatalf("expected new conId in re-request: %q", second)
	}
}

func TestLiveDepthDisabledSendsNothing(t *testing.T) {
	var buf bytes.Buffer
	c := New(Config{Enabled: true, LiveDepth: false})
	c.conn = &nopConn{w: &buf}
	c.subscribed = true
	c.watchDepth(111)
	if buf.Len() != 0 {
		t.Fatalf("LiveDepth off must not request market depth: %q", buf.String())
	}
}

// MARKET_DEPTH_L2 carries an extra marketMaker field between position and
// operation that plain MARKET_DEPTH doesn't -- insert/update/delete must
// land correctly through either message's field offsets.
func TestOnDepthInsertUpdateDeleteL2(t *testing.T) {
	c := New(Config{Enabled: true})
	c.want = "IBM"

	// insert bid at position 0: [msgId, version, reqId, position, marketMaker, operation, side, price, size, isSmartDepth]
	c.onDepth([]string{"13", "0", "9003", "0", "ARCA", "0", "1", "10.00", "500", "0"}, true)
	if len(c.bookBids) != 1 || c.bookBids[0].Price != 10.00 || c.bookBids[0].Size != 500 {
		t.Fatalf("after insert: %+v", c.bookBids)
	}

	// a better bid arrives, inserted at position 0 -- the old level 0 shifts to 1.
	c.onDepth([]string{"13", "0", "9003", "0", "ARCA", "0", "1", "10.05", "300", "0"}, true)
	if len(c.bookBids) != 2 || c.bookBids[0].Price != 10.05 || c.bookBids[1].Price != 10.00 {
		t.Fatalf("after second insert: %+v", c.bookBids)
	}

	// update position 1's size in place.
	c.onDepth([]string{"13", "0", "9003", "1", "ARCA", "1", "1", "10.00", "700", "0"}, true)
	if len(c.bookBids) != 2 || c.bookBids[1].Size != 700 {
		t.Fatalf("after update: %+v", c.bookBids)
	}

	// delete position 0 -- position 1 shifts up to 0.
	c.onDepth([]string{"13", "0", "9003", "0", "ARCA", "2", "1", "10.05", "300", "0"}, true)
	if len(c.bookBids) != 1 || c.bookBids[0].Price != 10.00 || c.bookBids[0].Size != 700 {
		t.Fatalf("after delete: %+v", c.bookBids)
	}

	// side 0 = ask, independent of the bid side above.
	c.onDepth([]string{"13", "0", "9003", "0", "ARCA", "0", "0", "10.10", "400", "0"}, true)
	if len(c.bookAsks) != 1 || c.bookAsks[0].Price != 10.10 {
		t.Fatalf("ask insert: %+v", c.bookAsks)
	}
	if len(c.bookBids) != 1 {
		t.Fatalf("ask update must not disturb the bid side: %+v", c.bookBids)
	}
}

// Plain MARKET_DEPTH (no marketMaker field) must parse through its own,
// shifted-by-one field offsets, not the L2 offsets.
func TestOnDepthInsertNonL2(t *testing.T) {
	c := New(Config{Enabled: true})
	c.want = "AA"

	// [msgId, version, reqId, position, operation, side, price, size]
	c.onDepth([]string{"12", "0", "9003", "0", "0", "1", "25.50", "1000"}, false)
	if len(c.bookBids) != 1 || c.bookBids[0].Price != 25.50 || c.bookBids[0].Size != 1000 {
		t.Fatalf("non-L2 insert: %+v", c.bookBids)
	}
}

func TestOnDepthPublishesSnapshot(t *testing.T) {
	c := New(Config{Enabled: true})
	c.want = "IBM"
	c.onDepth([]string{"12", "0", "9003", "0", "0", "1", "10.00", "500"}, false)

	select {
	case d := <-c.depthOut:
		if d.Sym != "IBM" || len(d.Bids) != 1 || d.Bids[0].Price != 10.00 || d.Note != "" {
			t.Fatalf("published snapshot: %+v", d)
		}
	default:
		t.Fatal("expected a depth update to be published")
	}
}

// A depth frame for a different reqId must be ignored entirely -- both
// message shapes.
func TestOnDepthIgnoresForeignReqID(t *testing.T) {
	c := New(Config{Enabled: true})
	c.want = "IBM"

	c.onDepth([]string{"13", "0", "12345", "0", "ARCA", "0", "1", "10.00", "500", "0"}, true)
	c.onDepth([]string{"12", "0", "12345", "0", "0", "1", "10.00", "500"}, false)

	if len(c.bookBids) != 0 || len(c.bookAsks) != 0 {
		t.Fatalf("foreign reqId should not mutate the book: bids=%+v asks=%+v", c.bookBids, c.bookAsks)
	}
	select {
	case d := <-c.depthOut:
		t.Fatalf("nothing should have been published, got %+v", d)
	default:
	}
}

// An ERR_MSG whose id field matches our depth reqId must publish a Note and
// close the subscription -- routed through handle(), the same entry point
// the session goroutine actually uses, not onDepthError directly, so the
// reqId-correlation logic in handle() is exercised too.
func TestDepthErrorPublishesNoteAndClosesSubscription(t *testing.T) {
	c := New(Config{Enabled: true})
	c.want = "IBM"
	c.depthOpen = true
	c.bookBids = []core.DepthLevel{{Price: 10, Size: 100}}

	// [errMsg, version, id, errorTime, code, msg] -- TWS's own "halted" text.
	c.handle([]string{"4", "2", "9003", "20260730 12:00:00", "316",
		"Market depth data has been HALTED. Please re-subscribe."})

	if c.depthOpen {
		t.Fatal("depthOpen should be false after a depth-scoped error")
	}
	if len(c.bookBids) != 0 {
		t.Fatalf("stale book should be cleared: %+v", c.bookBids)
	}
	select {
	case d := <-c.depthOut:
		if d.Sym != "IBM" || d.Note != "Market depth data has been HALTED. Please re-subscribe." {
			t.Fatalf("published note: %+v", d)
		}
		if len(d.Bids) != 0 || len(d.Asks) != 0 {
			t.Fatalf("an error update must carry no book: %+v", d)
		}
	default:
		t.Fatal("expected a depth update carrying the error note")
	}
}

// An error for a DIFFERENT request (e.g. the borrow market-data line) must
// never be attributed to the depth subscription.
func TestErrorForOtherRequestDoesNotTouchDepth(t *testing.T) {
	c := New(Config{Enabled: true})
	c.want = "IBM"
	c.depthOpen = true

	c.handle([]string{"4", "2", strconv.Itoa(mktReqID), "20260730 12:00:00", "300", "Can't find EId with tickerId"})

	if !c.depthOpen {
		t.Fatal("an unrelated request's error must not close the depth subscription")
	}
	select {
	case d := <-c.depthOut:
		t.Fatalf("nothing should have been published, got %+v", d)
	default:
	}
}
