package scan

import (
	"bytes"
	"fmt"
	"sort"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// The overlays read their tables through a per-row cache keyed on the
// snapshot's IndexGen (the engine shares one Index across snapshots whose rows
// are in the same order). Two scanners get the same snapshots and the same
// overlay updates; one sees no IndexGen (every overlay looks every row up),
// the other a shared Index with an IndexGen that changes only when the row
// order does. The rows they leave and everything they emit must agree.
func TestOverlayCacheMatchesUncached(t *testing.T) {
	start := goldAt(20, 3, 59, 0)
	gen := newGoldGen(goldGenConfig{Rows: 1500, Seed: goldSeed + 3, Start: start, Active: 1, NoReorder: true})
	mk := func() *Scanner {
		s := New(nil)
		s.ticks = make(chan core.ScanTick, 1<<14)
		s.alerts = make(chan core.AlertMsg, 1<<16)
		s.entries = make(chan core.ScanEntry, 1<<16)
		return s
	}
	r := &goldRun{t: t, s: mk(), gen: gen, ov: newGoldRand(goldSeed ^ 0xbeef), stats: map[string]*goldStat{}, symIdx: map[string]int{}}
	for i := range gen.tk {
		r.allSyms = append(r.allSyms, gen.tk[i].sym)
		r.symIdx[gen.tk[i].sym] = i
	}
	b := mk()
	scanners := []*Scanner{r.s, b}
	for _, d := range goldLiveDefs(t) {
		for _, s := range scanners {
			s.upsert(d)
		}
	}
	drain := func() {
		for _, s := range scanners {
			for len(s.ctrl) > 0 {
				(<-s.ctrl)(s)
			}
		}
	}

	var shared map[string]int
	var sharedGen uint64
	var order []string
	reused := 0
	const steps = 300
	for step := 0; step < steps; step++ {
		asOf := start + int64(step)*80_000 // 03:59 -> ~10:39 ET: pre-market, the open, the morning
		if step > 0 {
			switch {
			case step%9 == 1:
				m := r.shortMap()
				for _, s := range scanners {
					s.SetShortable(m)
				}
			case step%9 == 3:
				m := r.trendMap(300)
				for _, s := range scanners {
					s.SetDailyTrend(m)
				}
			case step%9 == 5:
				m, im := r.flagMap(250), r.flagMap(120)
				for _, s := range scanners {
					s.SetDailyFlags(m)
					s.SetIntradayFlags(im)
				}
			}
			if step%3 == 0 {
				m := r.emaMap(asOf)
				for _, s := range scanners {
					s.SetEmaFlags(m)
				}
			}
			if step%4 == 2 {
				items := make([]core.NewsItem, 0, 6)
				for i := 0; i < 6; i++ {
					items = append(items, core.NewsItem{
						ID: fmt.Sprintf("n%d-%d", step, i), Tickers: []string{r.mixSym(r.movers, 0.7)},
						Headline: goldHeadlines[r.ov.intn(len(goldHeadlines))], TS: asOf - int64(r.ov.cube(0, 3000))*1000,
					})
				}
				for _, s := range scanners {
					s.MarkNews(items)
				}
			}
		}
		drain()

		gen.cfg.NoReorder = step%23 != 10 // now and then the order changes
		snap := gen.Next(asOf)
		same := len(order) == len(snap.Rows)
		for i := 0; same && i < len(order); i++ {
			same = order[i] == snap.Rows[i].Sym
		}
		if same {
			reused++
		} else {
			shared, sharedGen = snap.Index, sharedGen+1
			order = order[:0]
			for i := range snap.Rows {
				order = append(order, snap.Rows[i].Sym)
			}
		}
		plain := &core.MetricSnapshot{Gen: snap.Gen, AsOf: snap.AsOf, Sess: snap.Sess, Rows: append([]core.Row(nil), snap.Rows...), Index: snap.Index}
		cached := &core.MetricSnapshot{Gen: snap.Gen, AsOf: snap.AsOf, Sess: snap.Sess, Rows: append([]core.Row(nil), snap.Rows...), Index: shared, IndexGen: sharedGen}
		r.s.process(plain)
		b.process(cached)
		r.prev = plain
		r.refreshPools()

		for i := range plain.Rows {
			if x, y := goldRowText(nil, plain.Rows[i]), goldRowText(nil, cached.Rows[i]); !bytes.Equal(x, y) {
				t.Fatalf("step %d row %s:\n uncached %s\n cached   %s", step, plain.Rows[i].Sym, x, y)
			}
		}
		if x, y := overlayCacheOut(r.s), overlayCacheOut(b); x != y {
			t.Fatalf("step %d: the scanners emitted different frames:\n uncached %.2000s\n cached   %.2000s", step, x, y)
		}
	}
	if reused < steps/2 {
		t.Fatalf("the shared Index was reused on only %d of %d steps", reused, steps)
	}
	t.Logf("shared Index reused on %d of %d steps", reused, steps)
}

// overlayCacheOut drains what s emitted in one evaluation, canonically: scans
// evaluate in map order, so each kind is sorted by scan (and symbol).
func overlayCacheOut(s *Scanner) string {
	var ticks, alerts, entries [][]byte
	for len(s.ticks) > 0 {
		ticks = append(ticks, goldCanon(nil, <-s.ticks))
	}
	for len(s.alerts) > 0 {
		alerts = append(alerts, goldCanon(nil, <-s.alerts))
	}
	for len(s.entries) > 0 {
		entries = append(entries, goldCanon(nil, <-s.entries))
	}
	for _, xs := range [][][]byte{ticks, alerts, entries} {
		sort.Slice(xs, func(i, j int) bool { return bytes.Compare(xs[i], xs[j]) < 0 })
	}
	counts := s.Counts()
	ids := make([]string, 0, len(counts))
	for id := range counts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var c bytes.Buffer
	for _, id := range ids {
		fmt.Fprintf(&c, "%s=%d ", id, counts[id])
	}
	return fmt.Sprintf("%s\n%s\n%s\n%s", bytes.Join(ticks, []byte{'\n'}), bytes.Join(alerts, []byte{'\n'}), bytes.Join(entries, []byte{'\n'}), c.String())
}
