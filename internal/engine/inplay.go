package engine

import (
	"sort"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// InPlayLeaders returns the live IN-PLAY ranking: every tracked ticker with a known 10-day
// average daily volume and some volume today, ranked by its day volume so far (sessVol, the
// 04:00-anchored session volume) over that average, descending (ties by symbol), capped at
// limit entries (limit <= 0 = uncapped). Each entry carries the last price, today's
// pre-market high once it is final and complete (pmhLevel: frozen at 09:30, 0 before then or
// when the engine did not see the whole pre-market) and the official open.
//
// Like PmVolLeaders it is data only: a ctrl-closure fan-out (every read on the owning shard
// goroutine, nothing added to the per-tick hot path) that blocks the CALLER until every shard
// has answered - so it must only be called while Run is live, and never from a goroutine the
// engine waits on. The result is a fresh slice the caller owns.
func (e *Engine) InPlayLeaders(limit int) []core.InPlayEntry {
	reply := make(chan []core.InPlayEntry, len(e.shards))
	for _, sh := range e.shards {
		sh.ctrl <- func(s *shard) {
			var m []core.InPlayEntry
			for sym, t := range s.state {
				if x, ok := t.inPlayX(); ok {
					m = append(m, core.InPlayEntry{Sym: sym, X: x, Last: float64(t.last), Pmh: float64(t.pmhLevel()), Open: float64(t.dayOpen)})
				}
			}
			reply <- m
		}
	}
	var out []core.InPlayEntry
	for range e.shards {
		out = append(out, (<-reply)...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].X != out[j].X {
			return out[i].X > out[j].X
		}
		return out[i].Sym < out[j].Sym
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// inPlayX is the day's volume so far over the 10-day average daily volume; ok=false when the
// average is unknown or nothing traded yet. Runs on the owning shard goroutine.
func (t *tickerState) inPlayX() (float64, bool) {
	if t.adv10 > 0 && t.sessVol > 0 {
		return float64(t.sessVol) / float64(t.adv10), true
	}
	return 0, false
}
