package engine

import (
	"sort"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// VWAP / 1m-EMA SEED after a restart or a stream reconnect.
//
// The session VWAP (scopeVwapNum/Den) is accumulated from the live per-second
// aggregates, so an engine started mid-session only knew the prints it saw, and
// the 9EMA VWAP cross judged a wrong VWAP (APH 2026-10-28: 80.31 vs a true
// regular-hours 80.79). SeedScopeVwap rebuilds it from today's REST 1-minute
// bars (Σ vw × v; close when a bar has no vw, the live fallback):
//
//   - only bars that START inside the current scope (emvScope: pre-market
//     04:00, regular 09:30, post 16:00) and have ENDED by now are used; the
//     in-progress bar never is;
//   - cut = the end of the last seeded bar. Prints from cut on come from the
//     live minute ring (vwMin, the last vwMinSlots minutes), so nothing is
//     counted twice: the REST bar owns its whole minute, live owns the rest.
//     A minute ≥ cut that began before the stream connected only has the
//     prints seen since (the same gap the live path always had).
//   - the 1-minute 9 EMA is rebuilt from every completed bar of today up to
//     cut with finishEmv's arithmetic, and the live judge skips bars ending at
//     or before cut (emvState.seedEnd), so no close is folded twice;
//   - the cross state restarts: prevOK=false and side=0, so the first judged
//     bar after a seed only takes its side silently and a cross needs a second
//     judged bar (see emavwap.go "a side seen without a cross").
//
// A seed whose scope does not match the engine's current session (overnight,
// closed, or a boundary between fetch and apply) is ignored.

const vwMinSlots = 8 // power of two; minutes of live sums kept for the join

type vwMinSum struct {
	m        int64 // epoch minute; 0 = empty
	num, den float64
}

// scopeKind maps a session to its emvScope kind (scope % 4); -1 = none.
func scopeKind(s core.Session) int64 {
	switch s {
	case core.SessionPre:
		return 1
	case core.SessionRegular:
		return 2
	case core.SessionPost:
		return 3
	}
	return -1
}

// scopeStartMs is the epoch ms the scope sc begins at.
func scopeStartMs(sc, off int64) int64 {
	startMin := [4]int64{0, 240, 570, 960}[sc%4]
	return (sc/4)*86_400_000 + startMin*m1Ms - off
}

// SeedScopeVwap seeds a tracked symbol's current-scope VWAP and 1-minute EMA
// from today's 1-minute bars (oldest first). Untracked symbols are skipped.
// bars is read on the shard goroutine: the caller must not modify it after.
func (e *Engine) SeedScopeVwap(sym string, bars []core.Bar) {
	if sym == "" || len(bars) == 0 {
		return
	}
	e.shards[shardIdx(sym, e.mask)].ctrl <- func(s *shard) {
		t, ok := s.state[sym]
		if !ok {
			return
		}
		t.seedScopeVwap(bars, e.now().UnixMilli(), e.etOffMs.Load(), core.Session(e.session.Load()))
	}
}

// seedScopeVwap is SeedScopeVwap on the shard; reports whether it seeded.
func (t *tickerState) seedScopeVwap(bars []core.Bar, nowMs, off int64, sess core.Session) bool {
	sc := emvScope(nowMs, off)
	if sc%4 != scopeKind(sess) {
		return false
	}
	if !sort.SliceIsSorted(bars, func(i, j int) bool { return bars[i].T < bars[j].T }) {
		return false
	}
	start := scopeStartMs(sc, off)
	var num, den float64
	cut := int64(0)
	for i := range bars {
		b := &bars[i]
		end := b.T + m1Ms
		if b.T < start || end > nowMs || b.V <= 0 {
			continue
		}
		vw := b.VW
		if vw == 0 {
			vw = b.C
		}
		num += vw * float64(b.V)
		den += float64(b.V)
		cut = end
	}
	if den == 0 {
		return false // REST has nothing for this scope: keep the live sums
	}
	cutM := cut / m1Ms
	startM := start / m1Ms
	for i := range t.vwMin {
		sl := &t.vwMin[i]
		if sl.m >= cutM && sl.m >= startM {
			num += sl.num
			den += sl.den
		}
	}
	t.scopeVwapNum, t.scopeVwapDen = num, den

	// the 1-minute 9 EMA from every completed traded bar of today up to cut
	ev := &t.d1.ev
	ev.emaN, ev.sum, ev.ema = 0, 0, 0
	for i := range bars {
		b := &bars[i]
		if b.T+m1Ms > cut || b.V <= 0 || !(b.C > 0) {
			continue
		}
		ev.emaN++
		switch {
		case ev.emaN < emaPeriod:
			ev.sum += b.C
		case ev.emaN == emaPeriod:
			ev.sum += b.C
			ev.ema = ev.sum / float64(emaPeriod)
		default:
			ev.ema = float64(b.C*emvK) + float64(ev.ema*(1-emvK))
		}
	}
	ev.seedEnd = cut
	ev.scope = sc
	ev.prevOK, ev.side, ev.pendUp, ev.pendDn = false, 0, false, false
	ev.waitUp, ev.waitDn = 0, 0
	return true
}

// EmvCandidates lists the tracked symbols that can reach the 9EMA VWAP cross
// universe (last > $25, average volume ≥ 1M), highest relative volume first,
// so the restart seed covers them even when they are not snapshot movers.
func (e *Engine) EmvCandidates() []string {
	type cand struct {
		sym  string
		rvol float64
	}
	reply := make(chan []cand, len(e.shards))
	for _, sh := range e.shards {
		sh.ctrl <- func(s *shard) {
			var out []cand
			for sym, t := range s.state {
				r := &t.ref.v
				if !t.ref.ok || !r.avgVolOK || r.avgVol < emvMinAvgVol || !(float64(t.last) > emvMinPrice) {
					continue
				}
				rv := 0.0
				if r.rvolOK && r.rvolBase > 0 {
					rv = float64(t.sessVol) / r.rvolBase
				}
				out = append(out, cand{sym, rv})
			}
			reply <- out
		}
	}
	var all []cand
	for range e.shards {
		all = append(all, <-reply...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].rvol != all[j].rvol {
			return all[i].rvol > all[j].rvol
		}
		return all[i].sym < all[j].sym
	})
	out := make([]string, len(all))
	for i, c := range all {
		out[i] = c.sym
	}
	return out
}
