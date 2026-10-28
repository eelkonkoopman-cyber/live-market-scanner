package engine

import (
	"context"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// shard owns a disjoint subset of tickers (by hash). Exactly one goroutine
// (shard.run) ever touches its state map or any tickerState in it — the
// single-writer invariant, enforced structurally because all four event sources
// (streaming aggs, the 1 Hz clock, snapshot requests, control ops) are handled
// as cases of the same select loop on the same goroutine.
type shard struct {
	eng *Engine

	in   chan core.Agg     // streaming per-second aggregates
	tick chan int64        // 1 Hz forward-fill clock (epoch second)
	snap chan snapReq      // snapshot request from the merger
	ctrl chan func(*shard) // infrequent ops: seed close, session reset

	state map[string]*tickerState
	list  []*tickerState // state's tickers in a stable order (first sight), for the per-tick loops

	// Snapshot row cache. A ticker's row is rebuilt only when it is dirty (an agg
	// since the last build) or allDirty (a clock tick or a control op touched
	// every ticker) or the snapshot's inputs (rowKey) changed; otherwise its
	// cached row is reused and only signalRow, which ages to the snapshot clock,
	// runs again. rows is the reply buffer, reused: the merger copies it out
	// before it sends the next request.
	key      rowKey
	allDirty bool
	rows     []core.Row
}

// rowKey is every snapshot input buildRow reads besides the ticker's own state.
type rowKey struct {
	bucket   int
	sess     core.Session
	baseline *core.BaselineTable
	floats   *map[string]core.FloatRef
	secTypes *map[string]string
	ok       bool
}

// snapReq asks a shard to emit its rows. The shard does the read inside its own
// goroutine, so the snapshot can never race a concurrent update.
type snapReq struct {
	bucket   int
	baseline *core.BaselineTable
	floats   map[string]core.FloatRef
	secTypes map[string]string
	// floatsRef / secTypesRef are the pointers floats / secTypes were loaded
	// from: the tables' identities, for the per-ticker reference cache.
	floatsRef   *map[string]core.FloatRef
	secTypesRef *map[string]string
	sess        core.Session
	nowMs       int64 // the snapshot's clock (its AsOf): the signal fields' ages count to it
	etOff       int64 // the UTC->ET offset at that moment, ms ("today" for the signal latches)
	reply       chan []core.Row
}

func (s *shard) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case a := <-s.in:
			s.onAgg(a)
		case sec := <-s.tick:
			s.onTick(sec)
		case req := <-s.snap:
			req.reply <- s.snapshot(req)
		case fn := <-s.ctrl:
			s.onCtrl(fn)
		}
	}
}

// onAgg folds one streaming aggregate into its ticker's state.
func (s *shard) onAgg(a core.Agg) {
	t := s.get(a.Sym)
	t.applyAgg(a)
	t.dirty = true
}

// onCtrl runs a control op. Any op may touch any ticker, so every cached row is
// rebuilt at the next snapshot.
func (s *shard) onCtrl(fn func(*shard)) {
	fn(s)
	s.allDirty = true
}

// onTick is the 1 Hz forward-fill clock for sec (epoch second): every ticker's
// ring, 1-minute / 5-minute candles and closed-bar judge advance to it.
func (s *shard) onTick(sec int64) {
	// The UTC->ET offset is read ONCE per tick for the whole shard (the
	// closed-bar judge's ET day and minute), never per ticker.
	off := s.eng.etOffMs.Load()
	nowMs := sec * 1000
	for _, t := range s.list {
		t.syncTo(sec)
		t.tick1m(sec)
		t.tick5m(sec)
		t.tickDildo(nowMs, off)
	}
	s.allDirty = true // the ring advanced: every window, age and candle may have moved
}

// snapshot builds this shard's rows for one snapshot request. The returned
// slice is the shard's reply buffer: it stays valid until the next snapshot.
//
// A clean ticker (no agg, tick or control op since its row was built, same
// rowKey) skips recordCurve and buildRow: recordCurve would rewrite the same
// bucket with the same values, and buildRow reads only the ticker's state and
// the rowKey inputs. A bucket roll changes the key, so every ticker records it.
func (s *shard) snapshot(req snapReq) []core.Row {
	key := rowKey{bucket: req.bucket, sess: req.sess, baseline: req.baseline,
		floats: req.floatsRef, secTypes: req.secTypesRef, ok: true}
	all := s.allDirty || key != s.key
	s.key, s.allDirty = key, false
	rows := s.rows[:0]
	for _, t := range s.list {
		if all || t.dirty {
			t.recordCurve(req.bucket) // capture cumulative-vol curve for RVOL
			t.row = t.buildRow(req.sess, t.refs(&req))
			t.dirty = false
		}
		rows = append(rows, t.row)
		t.signalRow(&rows[len(rows)-1], req.nowMs, req.etOff) // dildo / climax latches, aged by the snapshot clock
	}
	s.rows = rows
	return rows
}

// get returns the state for sym, creating it (and bumping the tracked count) on
// first sight. Only ever called from the shard goroutine.
func (s *shard) get(sym string) *tickerState {
	t, ok := s.state[sym]
	if !ok {
		t = &tickerState{sym: sym, curveBkt: -1, curveFirst: -1, barHead: -1, m5ListenMs: s.eng.startMs, sig: s.eng.sig, dirty: true}
		s.state[sym] = t
		s.list = append(s.list, t)
		s.eng.tracked.Add(1)
	}
	return t
}

// shardIdx maps a symbol to a shard via inlined FNV-1a. mask = N-1 (N a power of
// two), so the modulo is a single AND.
func shardIdx(sym string, mask uint32) uint32 {
	const (
		offset = 2166136261
		prime  = 16777619
	)
	h := uint32(offset)
	for i := 0; i < len(sym); i++ {
		h ^= uint32(sym[i])
		h *= prime
	}
	return h & mask
}
