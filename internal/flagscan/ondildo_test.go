package flagscan

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
)

// Config.OnDildo: the dildo rule's events (GREEN DILDO, 9 EMA PULLBACK, PB
// CANCELLED) of every tracked symbol - hot-only names included, which OnEval
// never sees - for the Signal window's signal history (internal/flaglog).

type dildoCollector struct {
	mu   sync.Mutex
	list []DildoUpdate
}

func (c *dildoCollector) add(u DildoUpdate) {
	c.mu.Lock()
	c.list = append(c.list, u)
	c.mu.Unlock()
}

func (c *dildoCollector) take() []DildoUpdate {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.list
	c.list = nil
	return out
}

// mbUpTo serves MB 2026-08-07's bars that began before the env clock's minute
// (a REST answer of completed minutes), whatever the symbol.
func mbUpTo(e *env, bars []core.Bar) func(context.Context, string) ([]core.Bar, error) {
	return func(context.Context, string) ([]core.Bar, error) {
		minute := e.clock().UnixMilli() / minuteMs * minuteMs
		n := 0
		for n < len(bars) && bars[n].T < minute {
			n++
		}
		return bars[:n], nil
	}
}

func mbTimes() (dildoTs, pbTs int64) {
	at := func(h, m int) int64 { return time.Date(2026, 8, 7, h, m, 0, 0, etLocation()).UnixMilli() }
	return at(16, 51), at(17, 1)
}

// hasEvent reports whether u carries an event of kind on the bar at ts.
func hasEvent(u DildoUpdate, ts int64, kind string) bool {
	for _, ev := range u.Result.Events {
		if ev.Ts == ts && ev.Kind == kind {
			return true
		}
	}
	return false
}

// A hot-only name (MB, admitted by the engine's 16:51 dildo) and a pmVolX
// candidate on the same bars both hand their dildo events over, each with its
// own HotOnly flag and the service clock - while OnEval still sees only the
// candidate.
func TestOnDildoHandsOverHotOnlyAndCandidateEvents(t *testing.T) {
	bars := mbFixture(t)
	dildoTs, pbTs := mbTimes()
	e := newEnv(t)
	e.setBars(mbUpTo(e, bars))
	e.setCands(core.PmVolEntry{Sym: "CAND", PmVolX: 2})
	dl, ups := &dildoCollector{}, &collector{}
	cfg := e.config()
	cfg.OnDildo, cfg.OnEval = dl.add, ups.add
	s := e.serviceCfg(cfg, defaultTuning())

	now := time.UnixMilli(pbTs + 3*minuteMs)
	e.setNow(now)
	s.handleHot(hitAt("MB", dildoTs), now)
	e.settle(s, now)
	e.step(s, now)

	got := map[string]DildoUpdate{}
	for _, u := range dl.take() {
		got[u.Sym] = u
	}
	mb, ok := got["MB"]
	if !ok || !mb.HotOnly || mb.NowMs != now.UnixMilli() || !hasEvent(mb, dildoTs, dildopb.KindDildo) || !hasEvent(mb, pbTs, dildopb.KindPb) {
		t.Fatalf("MB update %+v (present %v), want hot-only with the 16:51 dildo and the 17:01 pullback", mb, ok)
	}
	cand, ok := got["CAND"]
	if !ok || cand.HotOnly || !hasEvent(cand, dildoTs, dildopb.KindDildo) || !hasEvent(cand, pbTs, dildopb.KindPb) {
		t.Fatalf("CAND update %+v (present %v), want a candidate with the same events", cand, ok)
	}
	for _, u := range ups.take() {
		if u.Sym != "CAND" {
			t.Fatalf("OnEval saw %s: hot-only names stay out of it", u.Sym)
		}
	}

	// Nothing changed: no evaluation, no update (the hook rides the evaluations).
	e.step(s, now.Add(time.Second))
	if again := dl.take(); len(again) != 0 {
		t.Fatalf("no evaluation ran, yet %d updates", len(again))
	}
}

// A symbol whose bars hold no dildo produces no update at all.
func TestOnDildoIsSilentWithoutEvents(t *testing.T) {
	e := newEnv(t)
	e.flatAll()
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2})
	dl, ups := &dildoCollector{}, &collector{}
	cfg := e.config()
	cfg.OnDildo, cfg.OnEval = dl.add, ups.add
	s := e.serviceCfg(cfg, defaultTuning())
	now := et(9, 0, 0)
	for k := 0; k < 3; k++ {
		e.step(s, now)
		poke(s, "A", now)
		now = now.Add(time.Minute)
	}
	if len(ups.take()) == 0 {
		t.Fatal("A must be evaluated")
	}
	if got := dl.take(); len(got) != 0 {
		t.Fatalf("no dildo in the bars, yet %d OnDildo updates", len(got))
	}
}

// SCANNER_DILDO=0 reaches the rule as Enabled false: no events, no updates.
func TestOnDildoOffWithTheRuleDisabled(t *testing.T) {
	bars := mbFixture(t)
	dildoTs, pbTs := mbTimes()
	e := newEnv(t)
	e.setBars(mbUpTo(e, bars))
	e.setCands(core.PmVolEntry{Sym: "CAND", PmVolX: 2})
	dl := &dildoCollector{}
	cfg := e.config()
	cfg.OnDildo = dl.add
	cfg.Dildo = dildopb.DefaultParams()
	cfg.Dildo.Enabled = false
	s := e.serviceCfg(cfg, defaultTuning())
	now := time.UnixMilli(pbTs + 3*minuteMs)
	e.setNow(now)
	s.handleHot(hitAt("MB", dildoTs), now)
	e.settle(s, now)
	e.step(s, now)
	if _, ok := e.lastPub()["CAND"]; !ok {
		t.Fatal("CAND must still be evaluated")
	}
	if got := dl.take(); len(got) != 0 {
		t.Fatalf("dildo rule off, yet %d OnDildo updates", len(got))
	}
}

// A panicking OnDildo costs that one update: the verdicts are published and
// the other symbols still get theirs.
func TestAPanickingOnDildoCostsOneUpdate(t *testing.T) {
	bars := mbFixture(t)
	dildoTs, pbTs := mbTimes()
	e := newEnv(t)
	e.setBars(mbUpTo(e, bars))
	dl := &dildoCollector{}
	cfg := e.config()
	cfg.OnDildo = func(u DildoUpdate) {
		if u.Sym == "AAA" {
			panic("boom")
		}
		dl.add(u)
	}
	s := e.serviceCfg(cfg, defaultTuning())
	now := time.UnixMilli(pbTs + 3*minuteMs)
	e.setNow(now)
	s.handleHot(hitAt("AAA", dildoTs), now)
	s.handleHot(hitAt("BBB", dildoTs), now)
	e.settle(s, now)
	e.step(s, now)
	pub := e.lastPub()
	if pub["AAA"].DildoMs != dildoTs || pub["BBB"].DildoMs != dildoTs {
		t.Fatalf("both verdicts must publish: %+v", pub)
	}
	if got := dl.take(); len(got) != 1 || got[0].Sym != "BBB" {
		t.Fatalf("updates %+v, want BBB's only", got)
	}
	if s.dildoHookPanics != 1 {
		t.Fatalf("panics counted %d, want 1", s.dildoHookPanics)
	}
}
