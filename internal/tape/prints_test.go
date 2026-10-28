package tape

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func drainPrints(e *Engine) []core.TapePrintsMsg {
	var out []core.TapePrintsMsg
	for {
		select {
		case m := <-e.printsOut:
			out = append(out, m)
		default:
			return out
		}
	}
}

// TestFlushPrintsEmitsBatchOldestFirstWithSides proves a flush emits one batch
// per symbol carrying EVERY trade since the last flush (not just notable ones),
// oldest first, with the exact same Lee-Ready aggressor side the CVD/verdict
// pipeline classified.
func TestFlushPrintsEmitsBatchOldestFirstWithSides(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quote(e, "ABCD", 4.50, 4.52, ms)
	trade(e, "ABCD", 4.52, 100, ms)   // at ask -> buy
	trade(e, "ABCD", 4.50, 200, ms+1) // at bid -> sell
	trade(e, "ABCD", 4.51, 300, ms+2) // at mid -> tick rule vs 4.50 -> buy

	e.flushPrints()
	msgs := drainPrints(e)
	if len(msgs) != 1 {
		t.Fatalf("expected exactly one batch, got %d: %+v", len(msgs), msgs)
	}
	m := msgs[0]
	if m.Sym != "ABCD" || m.Dropped != 0 {
		t.Fatalf("batch header mismatch: %+v", m)
	}
	if len(m.Prints) != 3 {
		t.Fatalf("expected all 3 prints (raw tape is every trade), got %d", len(m.Prints))
	}
	wantSides := []string{"buy", "sell", "buy"}
	wantSizes := []int64{100, 200, 300}
	for i, p := range m.Prints {
		if p.TS != ms+int64(i) {
			t.Fatalf("prints must be oldest first: print %d TS=%d want %d", i, p.TS, ms+int64(i))
		}
		if p.Side != wantSides[i] || p.Size != wantSizes[i] {
			t.Fatalf("print %d side/size mismatch: %+v", i, p)
		}
	}
	if p := m.Prints[0]; p.Price != 4.52 || p.Notional != p.Price*float64(p.Size) {
		t.Fatalf("print price/notional mismatch: %+v", p)
	}
}

// TestFlushPrintsEmitsNothingWithoutTrades proves quotes are not prints and a
// symbol with nothing pending emits nothing (including right after a flush).
func TestFlushPrintsEmitsNothingWithoutTrades(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quote(e, "ABCD", 4.50, 4.52, ms) // quote only -- not a print

	e.flushPrints()
	if msgs := drainPrints(e); len(msgs) != 0 {
		t.Fatalf("quotes must not produce print batches, got %+v", msgs)
	}

	trade(e, "ABCD", 4.52, 100, ms)
	e.flushPrints()
	if msgs := drainPrints(e); len(msgs) != 1 {
		t.Fatalf("expected one batch after a trade, got %+v", msgs)
	}
	e.flushPrints() // nothing new pended since -> silence, not an empty batch
	if msgs := drainPrints(e); len(msgs) != 0 {
		t.Fatalf("a flush with nothing pending must emit nothing, got %+v", msgs)
	}
}

// TestFlushPrintsBatchCapKeepsNewestAndCountsDropped proves the per-batch cap:
// overflow drops the OLDEST prints and counts them into Dropped.
func TestFlushPrintsBatchCapKeepsNewestAndCountsDropped(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	quote(e, "ABCD", 4.50, 4.52, ms)
	const extra = 10
	for i := 0; i < printBatchCap+extra; i++ {
		trade(e, "ABCD", 4.52, 1, ms+int64(i))
	}

	e.flushPrints()
	msgs := drainPrints(e)
	if len(msgs) != 1 {
		t.Fatalf("expected one batch, got %d", len(msgs))
	}
	m := msgs[0]
	if len(m.Prints) != printBatchCap {
		t.Fatalf("batch must cap at %d prints, got %d", printBatchCap, len(m.Prints))
	}
	if m.Dropped != extra {
		t.Fatalf("overflow must be counted: Dropped=%d want %d", m.Dropped, extra)
	}
	// The NEWEST prints survive: the first kept print is the (extra+1)-th trade.
	if m.Prints[0].TS != ms+extra {
		t.Fatalf("cap must drop the oldest: first kept TS=%d want %d", m.Prints[0].TS, ms+extra)
	}
	if last := m.Prints[len(m.Prints)-1]; last.TS != ms+int64(printBatchCap+extra-1) {
		t.Fatalf("newest print missing: last TS=%d", last.TS)
	}
}

// TestFlushPrintsFullChannelDropsBatchAndSurfacesDroppedLater proves the
// engine NEVER blocks on a slow consumer: with printsOut full, the whole batch
// is discarded and its size (plus any Dropped it was already carrying) shows
// up as Dropped on the next batch that does get delivered.
func TestFlushPrintsFullChannelDropsBatchAndSurfacesDroppedLater(t *testing.T) {
	e := newTest()
	e.printsOut = make(chan core.TapePrintsMsg, 1) // shrink the buffer for the test
	ms := t0.UnixMilli()
	quote(e, "ABCD", 4.50, 4.52, ms)

	// Batch A fills the 1-slot channel.
	trade(e, "ABCD", 4.52, 100, ms)
	trade(e, "ABCD", 4.52, 100, ms+1)
	e.flushPrints()

	// Batch B (3 prints, already carrying 2 dropped from a batch-cap overflow)
	// finds the channel full: it must be discarded WITHOUT blocking, and its
	// 3+2 losses must carry forward.
	trade(e, "ABCD", 4.50, 100, ms+2)
	trade(e, "ABCD", 4.50, 100, ms+3)
	trade(e, "ABCD", 4.50, 100, ms+4)
	e.syms["ABCD"].pendingDropped += 2
	e.flushPrints() // must return immediately; a blocking send would hang here

	// Consumer catches up: batch A is intact, then batch C reports the losses.
	msgs := drainPrints(e)
	if len(msgs) != 1 || len(msgs[0].Prints) != 2 || msgs[0].Dropped != 0 {
		t.Fatalf("batch A should be the only delivered batch so far: %+v", msgs)
	}
	trade(e, "ABCD", 4.52, 500, ms+5)
	e.flushPrints()
	msgs = drainPrints(e)
	if len(msgs) != 1 {
		t.Fatalf("expected batch C, got %+v", msgs)
	}
	c := msgs[0]
	if len(c.Prints) != 1 || c.Prints[0].Size != 500 {
		t.Fatalf("batch C prints mismatch: %+v", c)
	}
	if c.Dropped != 5 { // batch B's 3 prints + the 2 it was already carrying
		t.Fatalf("batch C must surface the full loss: Dropped=%d want 5", c.Dropped)
	}
}
