package flagscan

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
)

// GREEN DILDO -> 9 EMA PULLBACK, alert 2: the engine hands every green dildo
// over on Config.Hot; the service tracks the symbol as a HOT name (tap first,
// REST backfill, one early resync) and runs the dildo rule over the chart's
// bars for every tracked symbol, publishing the pullback on the ref.

// hitAt is an engine dildo hit for sym whose bar began at barMs.
func hitAt(sym string, barMs int64) core.DildoHit {
	return core.DildoHit{Sym: sym, BarMs: barMs, O: 1, H: 1.1, L: 0.99, C: 1.09, V: 200_000, BodyPct: 9, BodyX: 4.5, VolX: 200, DollarVol: 218_000}
}

// flatAll serves every symbol a flat loaded history ending before now's minute.
func (e *env) flatAll() {
	e.setBars(func(context.Context, string) ([]core.Bar, error) { return flatBars(e.clock(), 60, 5), nil })
}

// poke folds one live print into sym's forming minute, so its bars changed
// and the next evaluate runs the engine for it.
func poke(s *service, sym string, now time.Time) {
	m := now.UnixMilli() / minuteMs * minuteMs
	s.handleAgg(core.Agg{Sym: sym, Open: 5, High: 5, Low: 5, Close: 5, Vol: 10, StartMs: m, EndMs: m + 1000}, now)
}

// tracked lists the service's symbols, sorted.
func tracked(s *service) []string {
	out := make([]string, 0, len(s.syms))
	for sym := range s.syms {
		out = append(out, sym)
	}
	sortStrings(out)
	return out
}

func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// ─── admission ───────────────────────────────────────────────────────────

// A hit is admitted on the spot: the tap is pointed at the symbol BEFORE its
// backfill is requested, and that first load goes ahead of a resync that is
// already due (one worker, so the order is observable).
func TestHotHitIsAdmittedAtOnceTapBeforeBarsFirstLoadAhead(t *testing.T) {
	e := newEnv(t)
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2})
	e.flatAll()
	tm := defaultTuning()
	tm.workers = 1
	s := e.service(tm)
	now := et(8, 50, 0)
	e.step(s, now)
	if !s.syms["A"].loaded {
		t.Fatal("A not loaded")
	}
	// A's first resync is due; a dildo on H lands at the same moment.
	now = s.syms["A"].due.Add(time.Second)
	e.setNow(now)
	before := len(e.logCopy())
	barMs := now.Truncate(time.Minute).UnixMilli() - minuteMs
	s.handleHot(hitAt("H", barMs), now)
	if !s.syms["H"].fetching || s.syms["A"].fetching {
		t.Fatalf("with one worker the first load of H must go before A's due resync: H %v A %v", s.syms["H"].fetching, s.syms["A"].fetching)
	}
	e.settle(s, now)
	log := e.logCopy()[before:]
	if len(log) < 2 || log[0] != "tap:A,H" || log[1] != "bars:H" {
		t.Fatalf("log %v, want the tap with H, then H's backfill", log)
	}
	s.evaluate(now)
	ref, ok := e.lastPub()["H"]
	if !ok || !ref.HotOnly {
		t.Fatalf("H published %+v (ok %v), want a HotOnly verdict", ref, ok)
	}
	if a := e.lastPub()["A"]; a.HotOnly {
		t.Fatal("A is a candidate: never HotOnly")
	}
	if he := s.hot["H"]; he.barMs != barMs || !he.until.Equal(time.UnixMilli(barMs+minuteMs).Add(HotHold)) {
		t.Fatalf("hot entry %+v", he)
	}
}

// A dildo on a symbol already tracked and loaded fetches nothing (the tap has
// every print of the dildo minute: A was admitted at 08:50:00, the dildo is the
// 08:51 bar) and leaves the tap alone; its parity check is armed at once.
func TestHotHitOnALoadedCandidateFetchesNothing(t *testing.T) {
	e := newEnv(t)
	e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2})
	e.flatAll()
	s := e.service(defaultTuning())
	now := et(8, 50, 0)
	e.step(s, now)
	taps, fetches := len(e.taps), e.countLog("bars:A")
	barMs := et(8, 51, 0).UnixMilli()
	now = time.UnixMilli(barMs + minuteMs).Add(3 * time.Second) // 08:52:03, the engine's judge
	e.setNow(now)
	s.handleHot(hitAt("A", barMs), now)
	e.settle(s, now)
	if len(e.taps) != taps || e.countLog("bars:A") != fetches {
		t.Fatalf("taps %d -> %d, A fetches %d -> %d: a loaded candidate needs nothing", taps, len(e.taps), fetches, e.countLog("bars:A"))
	}
	if st := s.syms["A"]; st.hot || !st.parityOK || st.parityMs == 0 || !st.earlyDue.IsZero() {
		t.Fatalf("A: hot %v parityOK %v parityMs %d earlyDue %v", st.hot, st.parityOK, st.parityMs, st.earlyDue)
	}
}

// mbFixture is MB 2026-08-07 (the calibration's reference dildo, 16:51 ET).
func mbFixture(t *testing.T) []core.Bar {
	t.Helper()
	raw, err := os.ReadFile(fixturesDir + "/dildo/dildo-mb-2026-08-07.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Bars []core.Bar `json:"bars"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	return fx.Bars
}

// RV1-SRV-2: "loaded" is not "the tap holds the whole dildo minute". MB is
// admitted as a pmVolX candidate at 16:51:35, INSIDE its 16:51 dildo minute
// (the dildo's own volume is what lifts a name over 1x pre-market), and loads
// at once from a REST that serves completed minutes: its 16:51 bar is the
// tap's fragment from 16:51:35 on. The engine's hit at 16:52:03 must treat it
// like a symbol still waiting for its history - no parity verdict on that
// fragment, and the early resync at the bar's end + 20 s that makes the minute
// final - so the chart rule finds the 16:51 anchor at 16:52:20 (not at the
// phased first resync minutes later, past the first touch bars), the parity
// check counts no false miss, and the 17:01 pullback is published on time.
// Control: admitted at 16:50:30, before the minute, the tap holds all of it -
// parity at once and no early resync, as before.
func TestHotHitOnASymbolWhoseTapBeganInsideTheDildoMinute(t *testing.T) {
	bars := mbFixture(t)
	at := func(h, m, s int) time.Time { return time.Date(2026, 8, 7, h, m, s, 0, etLocation()) }
	dildoTs, pbTs := at(16, 51, 0).UnixMilli(), at(17, 1, 0).UnixMilli()
	barEnd := time.UnixMilli(dildoTs + minuteMs)
	const sym = "MB"

	type outcome struct {
		parityOK    bool
		earlyDue    time.Time
		firstAnchor time.Time
		firstConf   time.Time
		pbMs        int64
		misses      int64
	}
	run := func(adm time.Time) outcome {
		e := newEnv(t)
		e.setBars(func(context.Context, string) ([]core.Bar, error) {
			minute := e.clock().UnixMilli() / minuteMs * minuteMs
			n := 0
			for n < len(bars) && bars[n].T < minute {
				n++
			}
			return bars[:n], nil
		})
		s := e.service(defaultTuning())
		due := map[int64][]core.Agg{}
		for _, b := range bars {
			for _, a := range subAggs(sym, b) {
				if a.StartMs >= adm.UnixMilli() { // the tap delivers only what printed after it began
					due[a.StartMs/1000] = append(due[a.StartMs/1000], a)
				}
			}
		}
		var out outcome
		hit := barEnd.Add(3 * time.Second)
		for now := adm; !now.After(time.UnixMilli(pbTs + 3*minuteMs)); now = now.Add(time.Second) {
			e.setNow(now)
			if now.Equal(adm) {
				e.setCands(core.PmVolEntry{Sym: sym, PmVolX: 2})
				s.nextCand = time.Time{}
			}
			if now.Equal(hit) {
				if !s.syms[sym].loaded {
					t.Fatalf("admitted %s: not loaded by the hit", adm.Format("15:04:05"))
				}
				s.handleHot(hitAt(sym, dildoTs), now)
				out.parityOK, out.earlyDue = s.syms[sym].parityOK, s.syms[sym].earlyDue
			}
			s.tick(now)
			e.settle(s, now)
			for _, a := range due[now.Unix()] {
				s.handleAgg(a, now)
			}
			s.evaluate(now)
			ref, ok := e.lastPub()[sym]
			if !ok {
				continue
			}
			if ref.DildoMs == dildoTs && out.firstAnchor.IsZero() {
				out.firstAnchor = now
			}
			if ref.DildoMs == dildoTs && ref.PbState == dildopb.PbConfirmed && out.firstConf.IsZero() { // (an earlier dildo's pullback is not this one)
				out.firstConf, out.pbMs = now, ref.PbMs
			}
		}
		out.misses = s.dildoParityMiss
		return out
	}
	clock := func(t time.Time) string { return t.In(etLocation()).Format("15:04:05") }

	got := run(at(16, 51, 35))
	t.Logf("admitted inside the minute: parityOK %v earlyDue %s anchor %s confirmed %s misses %d",
		got.parityOK, clock(got.earlyDue), clock(got.firstAnchor), clock(got.firstConf), got.misses)
	if got.parityOK || !got.earlyDue.Equal(barEnd.Add(EarlyResync)) {
		t.Fatalf("at the hit: parityOK %v earlyDue %s, want no parity verdict yet and the early resync at %s",
			got.parityOK, clock(got.earlyDue), clock(barEnd.Add(EarlyResync)))
	}
	if got.firstAnchor.IsZero() || got.firstAnchor.After(barEnd.Add(EarlyResync)) {
		t.Fatalf("the 16:51 anchor first published at %s, want by the early resync (%s)", clock(got.firstAnchor), clock(barEnd.Add(EarlyResync)))
	}
	if got.misses != 0 {
		t.Fatalf("dildoParityMiss %d: the fragment must never be judged for parity", got.misses)
	}
	if got.pbMs != pbTs || got.firstConf.IsZero() || got.firstConf.After(time.UnixMilli(pbTs+minuteMs).Add(CloseGrace)) {
		t.Fatalf("the pullback: PbMs %s confirmed at %s, want 17:01 once that bar closed", clock(time.UnixMilli(got.pbMs)), clock(got.firstConf))
	}

	ctl := run(at(16, 50, 30))
	if !ctl.parityOK || !ctl.earlyDue.IsZero() || ctl.misses != 0 || ctl.pbMs != pbTs {
		t.Fatalf("control (admitted before the minute): parityOK %v earlyDue %s misses %d pbMs %d", ctl.parityOK, clock(ctl.earlyDue), ctl.misses, ctl.pbMs)
	}
}

// RV1-SRV-2, the other way a loaded symbol's dildo minute can be incomplete:
// the tap DROPPED aggregates (the loop fell behind) after the minute began.
// The hit then resyncs early and waits for it before the parity check. Drops
// seen before the minute began do not count.
func TestHotHitAfterTapDropsInTheDildoMinuteResyncsEarly(t *testing.T) {
	barMs := et(8, 51, 0).UnixMilli()
	barEnd := time.UnixMilli(barMs + minuteMs)
	hitNow := barEnd.Add(3 * time.Second)
	setup := func(dropAt time.Time) (*env, *service) {
		e := newEnv(t)
		e.setCands(core.PmVolEntry{Sym: "A", PmVolX: 2})
		e.flatAll()
		s := e.service(defaultTuning())
		e.step(s, et(8, 50, 0)) // admitted and loaded a minute before the dildo
		e.step(s, dropAt.Add(-time.Second))
		e.drops.Add(3)
		e.step(s, dropAt) // the tick that sees the tap's drop count rise
		e.setNow(hitNow)
		s.handleHot(hitAt("A", barMs), hitNow)
		return e, s
	}

	e, s := setup(et(8, 51, 20))
	st := s.syms["A"]
	if st.parityOK || !st.earlyDue.Equal(barEnd.Add(EarlyResync)) || !st.due.Equal(st.earlyDue) {
		t.Fatalf("drops inside the minute: parityOK %v earlyDue %v due %v, want the early resync at %v", st.parityOK, st.earlyDue, st.due, barEnd.Add(EarlyResync))
	}
	n := e.countLog("bars:A")
	e.step(s, barEnd.Add(EarlyResync-time.Second))
	if e.countLog("bars:A") != n || st.parityMs == 0 {
		t.Fatal("nothing may happen before the early resync")
	}
	e.step(s, barEnd.Add(EarlyResync))
	// The resync made the minute final and the parity check ran (these flat bars
	// have no dildo where the fake engine claimed one: one miss, counted then).
	if e.countLog("bars:A") != n+1 || !st.earlyDue.IsZero() || st.parityMs != 0 || s.dildoParityMiss != 1 {
		t.Fatalf("after the early resync: fetches %d -> %d earlyDue %v parity pending %d misses %d", n, e.countLog("bars:A"), st.earlyDue, st.parityMs, s.dildoParityMiss)
	}

	_, s = setup(et(8, 50, 30)) // the drops came before the dildo minute began
	if st := s.syms["A"]; !st.parityOK || !st.earlyDue.IsZero() {
		t.Fatalf("drops before the minute: parityOK %v earlyDue %v, want the tap trusted", st.parityOK, st.earlyDue)
	}
}

// The early resync: a first load that started before the dildo bar's end +
// EarlyResync - 5 s is followed by one resync at exactly the bar's end + 20 s
// (not the phased 2.5-5 min one), dispatched then; after it the ordinary
// ResyncEvery applies.
func TestHotEarlyResyncAtBarEndPlus20s(t *testing.T) {
	e := newEnv(t)
	e.flatAll()
	s := e.service(defaultTuning())
	barMs := et(9, 31, 0).UnixMilli()
	barEnd := time.UnixMilli(barMs + minuteMs)
	now := barEnd.Add(3 * time.Second)
	e.step(s, now)
	e.setNow(now)
	s.handleHot(hitAt("H", barMs), now)
	e.settle(s, now)
	st := s.syms["H"]
	if !st.loaded || !st.due.Equal(barEnd.Add(EarlyResync)) || !st.earlyDue.Equal(st.due) {
		t.Fatalf("after the first load: loaded %v due %v earlyDue %v, want %v", st.loaded, st.due, st.earlyDue, barEnd.Add(EarlyResync))
	}
	if st.parityOK {
		t.Fatal("the first load could not have the dildo minute final: parity waits for the early resync")
	}
	n := e.countLog("bars:H")
	e.step(s, barEnd.Add(EarlyResync-time.Second))
	if e.countLog("bars:H") != n {
		t.Fatal("the early resync went before barEnd + 20 s")
	}
	at := barEnd.Add(EarlyResync)
	e.step(s, at)
	if e.countLog("bars:H") != n+1 {
		t.Fatal("no early resync at barEnd + 20 s")
	}
	if !st.earlyDue.IsZero() || !st.due.Equal(at.Add(ResyncEvery)) {
		t.Fatalf("after the early resync: earlyDue %v due %v, want none / the ordinary ResyncEvery", st.earlyDue, st.due)
	}
	// The early resync made the minute final: the parity check ran (and, on
	// these flat bars, found no dildo where the fake engine claimed one).
	if st.parityMs != 0 || s.dildoParityMiss != 1 {
		t.Fatalf("parity: pending %d misses %d", st.parityMs, s.dildoParityMiss)
	}

	// A first load that started late enough needs no early resync.
	e2 := newEnv(t)
	e2.flatAll()
	s2 := e2.service(defaultTuning())
	late := barEnd.Add(16 * time.Second)
	e2.setNow(late)
	s2.handleHot(hitAt("L", barMs), late)
	e2.settle(s2, late)
	if st := s2.syms["L"]; !st.earlyDue.IsZero() || st.due.Before(late.Add(ResyncEvery/2)) || !st.parityOK {
		t.Fatalf("late first load: earlyDue %v due %v parityOK %v", st.earlyDue, st.due, st.parityOK)
	}
}

// ─── the hold ────────────────────────────────────────────────────────────

// The hold ends HotHold after the dildo bar's end: a hot-only symbol leaves
// (and its verdict leaves the map); one that is a candidate or pinned stays.
// A newer dildo extends the hold.
func TestHotExpiryDropsOnlyHotOnlySymbols(t *testing.T) {
	e := newEnv(t)
	e.setCands(core.PmVolEntry{Sym: "C", PmVolX: 2})
	e.flatAll()
	pins := &pinSet{}
	pins.set("P")
	cfg := e.config()
	cfg.Pinned = pins.get
	s := e.serviceCfg(cfg, defaultTuning())
	now := et(9, 40, 0)
	e.step(s, now)
	barMs := et(9, 39, 0).UnixMilli()
	for _, sym := range []string{"H", "C", "P", "X"} {
		s.handleHot(hitAt(sym, barMs), now)
	}
	s.handleHot(hitAt("X", barMs+5*minuteMs), now) // X's newer dildo: its hold runs 5 minutes longer
	e.settle(s, now)
	if got := strings.Join(tracked(s), ","); got != "C,H,P,X" {
		t.Fatalf("tracked %s", got)
	}
	if !s.syms["H"].hot || s.syms["C"].hot || s.syms["P"].hot || !s.syms["X"].hot {
		t.Fatal("hot-only must be exactly the symbols neither ranked nor pinned")
	}
	e.step(s, now)
	if !e.lastPub()["H"].HotOnly || e.lastPub()["C"].HotOnly || e.lastPub()["P"].HotOnly {
		t.Fatalf("HotOnly on the refs: %+v", e.lastPub())
	}

	end := time.UnixMilli(barMs + minuteMs).Add(HotHold)
	e.step(s, end.Add(-time.Second))
	if _, ok := s.syms["H"]; !ok {
		t.Fatal("H dropped before its hold ended")
	}
	e.step(s, end)
	if got := strings.Join(tracked(s), ","); got != "C,P,X" {
		t.Fatalf("after the hold: tracked %s, want C (candidate), P (pinned), X (extended)", got)
	}
	if _, ok := e.lastPub()["H"]; ok {
		t.Fatal("H's verdict must leave the published map")
	}
	if _, ok := s.hot["C"]; ok {
		t.Fatal("C's hold ended too (it stays as a candidate)")
	}
	e.step(s, end.Add(5*time.Minute))
	if got := strings.Join(tracked(s), ","); got != "C,P" {
		t.Fatalf("after X's extended hold: tracked %s", got)
	}
}

// MaxHot: one hot name too many evicts the one whose hold ends first.
func TestHotMaxHotEvictsTheEarliestExpiry(t *testing.T) {
	e := newEnv(t)
	e.flatAll()
	tm := defaultTuning()
	tm.maxHot = 3
	s := e.service(tm)
	now := et(9, 50, 0)
	e.setNow(now)
	base := et(9, 30, 0).UnixMilli()
	// Dildo bars B 09:32, A 09:31, D 09:33, then C 09:34 overflows the cap of 3:
	// A's hold ends first, so A goes.
	for i, sym := range []string{"B", "A", "D", "C"} {
		s.handleHot(hitAt(sym, base+int64([]int{2, 1, 3, 4}[i])*minuteMs), now)
	}
	e.settle(s, now)
	if _, ok := s.hot["A"]; ok || len(s.hot) != 3 || s.hotEvictions != 1 {
		t.Fatalf("hot %v evictions %d: A (earliest expiry) must be evicted", s.hot, s.hotEvictions)
	}
	if _, ok := s.syms["A"]; ok {
		t.Fatal("the evicted hot-only symbol must leave")
	}
	if st := s.stats(); st.hot != 3 || st.hotOnly != 3 || st.hotEvictions != 1 {
		t.Fatalf("stats %+v", st)
	}
}

// ─── OnEval ──────────────────────────────────────────────────────────────

// OnEval never sees a hot-only symbol (nor is it primed by one). When a hot
// name becomes a candidate its first OnEval is the unprimed replay, and a
// candidate that drops to hot-only starts unprimed again when it comes back.
func TestOnEvalSkipsHotOnlyAndPromotionStartsUnprimed(t *testing.T) {
	e := newEnv(t)
	e.flatAll()
	ups := &collector{}
	cfg := e.config()
	cfg.OnEval = ups.add
	s := e.serviceCfg(cfg, defaultTuning())
	now := et(9, 0, 0)
	e.step(s, now)
	s.handleHot(hitAt("H", now.Truncate(time.Minute).UnixMilli()-minuteMs), now)
	for k := 0; k < 3; k++ {
		now = now.Add(time.Minute)
		e.step(s, now)
	}
	if _, ok := e.lastPub()["H"]; !ok {
		t.Fatal("H must be evaluated and published")
	}
	if got := ups.take(); len(got) != 0 || s.syms["H"].primed {
		t.Fatalf("OnEval saw a hot-only symbol: %v (primed %v)", symsOf(got), s.syms["H"].primed)
	}

	// H qualifies: the next refresh makes it a candidate (still hot, no longer hot-only).
	e.setCands(core.PmVolEntry{Sym: "H", PmVolX: 1.5})
	s.nextCand = time.Time{}
	now = now.Add(time.Minute)
	e.step(s, now)
	if s.syms["H"].hot {
		t.Fatal("a candidate is not hot-only")
	}
	now = now.Add(time.Minute)
	poke(s, "H", now)
	e.step(s, now)
	if p := primedOf(ups.take(), "H"); len(p) < 2 || p[0] || !p[1] {
		t.Fatalf("H's OnEval primed flags %v, want an unprimed first update, then primed", p)
	}

	// It drops out of the candidates while its hold lasts: hot-only again, OnEval
	// stops; back as a candidate it starts unprimed once more.
	e.setCands()
	s.nextCand = time.Time{}
	now = now.Add(time.Minute)
	e.step(s, now)
	if !s.syms["H"].hot || s.syms["H"].primed {
		t.Fatalf("H hot-only again: hot %v primed %v", s.syms["H"].hot, s.syms["H"].primed)
	}
	now = now.Add(time.Minute)
	poke(s, "H", now)
	e.step(s, now)
	if got := ups.take(); len(primedOf(got, "H")) != 0 {
		t.Fatalf("OnEval while hot-only: %v", primedOf(got, "H"))
	}
	e.setCands(core.PmVolEntry{Sym: "H", PmVolX: 1.5})
	s.nextCand = time.Time{}
	now = now.Add(time.Minute)
	e.step(s, now)
	if p := primedOf(ups.take(), "H"); len(p) == 0 || p[0] {
		t.Fatalf("back as a candidate: primed %v, want unprimed first", p)
	}
}

// ─── the ref ─────────────────────────────────────────────────────────────

// withDildo carries every dildo field (and nothing for a day without one), and
// sameRef sees a change in each of them, NaN == NaN.
func TestRefCarriesTheDildoAndSameRefComparesEveryField(t *testing.T) {
	d := dildopb.Result{
		LastDildoTs: 1_000_000, LastDildoBodyPct: 9, LastDildoBodyX: math.Inf(1), LastDildoVolX: 200, LastDildoDvol: 218_000,
		PbState: dildopb.PbConfirmed, TouchTs: 1_300_000, PbTs: 1_300_000, PbPx: 7.93,
	}
	ref := withDildo(core.EmaFlagRef{State: 2, DistPct: 1, Trigger: math.NaN(), Stop: math.NaN()}, d, true)
	if ref.DildoMs != 1_000_000 || ref.DildoBody != 9 || !math.IsInf(ref.DildoBodyX, 1) || ref.DildoVolX != 200 || ref.DildoDvol != 218_000 ||
		ref.PbState != 3 || ref.TouchMs != 1_300_000 || ref.PbMs != 1_300_000 || ref.PbPx != 7.93 || !ref.HotOnly || ref.State != 2 {
		t.Fatalf("ref %+v", ref)
	}
	none := dildopb.Evaluate(nil, minuteMs, 0, dildopb.DefaultParams()) // LastDildoTs 0, NaN measures
	base := core.EmaFlagRef{State: 1, DistPct: 2, Trigger: 3, Stop: 4}
	if got := withDildo(base, none, false); got != base {
		t.Fatalf("no dildo today must leave the ref exactly refOf's: %+v", got)
	}
	if !sameRef(ref, ref) {
		t.Fatal("a ref equals itself (NaN levels included)")
	}
	for name, mut := range map[string]func(*core.EmaFlagRef){
		"DildoMs":    func(r *core.EmaFlagRef) { r.DildoMs++ },
		"DildoBody":  func(r *core.EmaFlagRef) { r.DildoBody = 9.5 },
		"DildoBodyX": func(r *core.EmaFlagRef) { r.DildoBodyX = 6 },
		"DildoVolX":  func(r *core.EmaFlagRef) { r.DildoVolX = math.NaN() },
		"DildoDvol":  func(r *core.EmaFlagRef) { r.DildoDvol = 1 },
		"PbState":    func(r *core.EmaFlagRef) { r.PbState = 2 },
		"TouchMs":    func(r *core.EmaFlagRef) { r.TouchMs = 0 },
		"PbMs":       func(r *core.EmaFlagRef) { r.PbMs = 1 },
		"PbPx":       func(r *core.EmaFlagRef) { r.PbPx = math.NaN() },
		"HotOnly":    func(r *core.EmaFlagRef) { r.HotOnly = false },
	} {
		other := ref
		mut(&other)
		if sameRef(ref, other) || sameRef(other, ref) {
			t.Errorf("%s differs: sameRef must say so", name)
		}
	}
}

// ─── the rule through the service ────────────────────────────────────────

// quietDildo is a symbol that printed a green dildo at 10:30 ET and then two
// bars that held well above both the floor and the EMA band, and nothing more.
func quietDildo() (bars []core.Bar, dildoTs int64) {
	start := et(10, 0, 0).UnixMilli()
	for i := 0; i < 30; i++ {
		b := core.Bar{T: start + int64(i)*minuteMs, O: 1.00, H: 1.03, L: 0.99, C: 1.02, V: 1000}
		if i%2 == 1 {
			b.O, b.C = 1.02, 1.00
		}
		bars = append(bars, b)
	}
	dildoTs = start + 30*minuteMs
	bars = append(bars, core.Bar{T: dildoTs, O: 1.00, H: 1.10, L: 0.99, C: 1.09, V: 200_000})
	for i := int64(1); i <= 2; i++ {
		bars = append(bars, core.Bar{T: dildoTs + i*minuteMs, O: 1.09, H: 1.12, L: 1.08, C: 1.10, V: 5000})
	}
	return bars, dildoTs
}

// A dildo still watching on a name that has gone quiet turns expired (state 5)
// once its window's last bar has closed - no bar changes, the clock alone
// re-runs the rule - and is published.
func TestQuietWatchingDildoTurnsExpired(t *testing.T) {
	bars, dildoTs := quietDildo()
	e := newEnv(t)
	e.setBars(func(context.Context, string) ([]core.Bar, error) { return bars, nil })
	s := e.service(defaultTuning())
	now := time.UnixMilli(dildoTs + 5*minuteMs)
	e.setNow(now)
	s.handleHot(hitAt("Q", dildoTs), now)
	e.settle(s, now)
	e.step(s, now)
	ref := e.lastPub()["Q"]
	if ref.DildoMs != dildoTs || ref.PbState != dildopb.PbWatching || !ref.HotOnly {
		t.Fatalf("watching: %+v", ref)
	}
	deadline := dildoTs + int64(dildopb.DefaultParams().PbWindowMin)*minuteMs
	expireAt := time.UnixMilli(deadline + minuteMs).Add(CloseGrace) // the engine's clock is now - CloseGrace
	e.step(s, expireAt.Add(-time.Second))
	if got := e.lastPub()["Q"].PbState; got != dildopb.PbWatching {
		t.Fatalf("1 s before the window closed: state %d", got)
	}
	n := len(e.published())
	e.step(s, expireAt)
	if got := e.lastPub()["Q"].PbState; got != dildopb.PbExpired || len(e.published()) != n+1 {
		t.Fatalf("at the window's close: state %d (publishes %d -> %d), want expired and published", got, n, len(e.published()))
	}
	e.step(s, expireAt.Add(time.Second))
	if len(e.published()) != n+1 {
		t.Fatal("an expired verdict must not be re-run and re-published every tick")
	}
}

// dildoParityMiss counts an engine dildo the chart's final bars do not show,
// and only that.
func TestDildoParityMissCountsAnAbsentAnchor(t *testing.T) {
	bars, dildoTs := quietDildo()
	e := newEnv(t)
	e.setCands(core.PmVolEntry{Sym: "Q", PmVolX: 2})
	e.setBars(func(context.Context, string) ([]core.Bar, error) { return bars, nil })
	s := e.service(defaultTuning())
	now := time.UnixMilli(dildoTs + 4*minuteMs)
	e.step(s, now)

	s.handleHot(hitAt("Q", dildoTs), now) // the chart has it
	e.step(s, now.Add(time.Second))
	if s.dildoParityMiss != 0 || s.syms["Q"].parityMs != 0 {
		t.Fatalf("a matching anchor: misses %d, pending %d", s.dildoParityMiss, s.syms["Q"].parityMs)
	}
	s.handleHot(hitAt("Q", dildoTs-5*minuteMs), now.Add(2*time.Second)) // a flat bar on the chart
	e.step(s, now.Add(3*time.Second))
	if s.dildoParityMiss != 1 {
		t.Fatalf("an absent anchor: misses %d, want 1", s.dildoParityMiss)
	}
	buf := captureLog(t)
	s.logStatus(now.Add(4 * time.Second))
	if !strings.Contains(buf.String(), "dildoParityMiss=1") || !strings.Contains(buf.String(), "hot=1") {
		t.Fatalf("status line lacks the dildo counters: %s", buf.String())
	}
}

// RV2-SRV-1: a forming bar that dips into the EMA band (TOUCH, TouchMs = that
// bar) and then, inside the SAME minute, breaks the dildo's floor (the rule's
// forming-bar branch cancels with TouchTs left 0) is the rule's own verdict,
// not a resync rewriting a published touch: no REST call happens in between,
// so dildoRewrites must stay 0 and nothing may log "a resync moved ...".
func TestFormingTouchThenFloorBreakIsNotARewrite(t *testing.T) {
	bars, dildoTs := quietDildo() // dildo 10:30 (floor 1.045), 10:31 and 10:32 held ~1.10
	bars = bars[:len(bars)-1]     // through 10:31: 10:32 comes from the tap
	e := newEnv(t)
	e.setBars(func(context.Context, string) ([]core.Bar, error) { return bars, nil })
	s := e.service(defaultTuning())
	buf := captureLog(t)
	now := time.UnixMilli(dildoTs + 2*minuteMs) // 10:32:00
	e.setNow(now)
	s.handleHot(hitAt("Q", dildoTs), now)
	e.settle(s, now)
	e.step(s, now)
	fetches := e.countLog("bars:Q")
	f := dildoTs + 2*minuteMs
	trade := func(at time.Time, a core.Agg) core.EmaFlagRef {
		e.setNow(at)
		s.handleAgg(a, at)
		s.tick(at)
		e.settle(s, at)
		return e.lastPub()["Q"]
	}
	// 10:32:10 the forming bar trades 1.05: inside the band, above the floor
	ref := trade(now.Add(10*time.Second), core.Agg{Sym: "Q", Open: 1.10, High: 1.10, Low: 1.05, Close: 1.06, Vol: 3000, StartMs: f + 5000, EndMs: f + 6000})
	if ref.PbState != dildopb.PbTouching || ref.TouchMs != f {
		t.Fatalf("10:32:10: state %d touch %d, want TOUCH on the forming 10:32 bar", ref.PbState, ref.TouchMs)
	}
	// 10:32:20 the same bar trades 1.04: under the 1.045 floor
	ref = trade(now.Add(20*time.Second), core.Agg{Sym: "Q", Open: 1.06, High: 1.06, Low: 1.04, Close: 1.045, Vol: 3000, StartMs: f + 15000, EndMs: f + 16000})
	if ref.PbState != dildopb.PbCancelled {
		t.Fatalf("10:32:20: state %d, want CANCELLED (floor)", ref.PbState)
	}
	if n := e.countLog("bars:Q"); n != fetches {
		t.Fatalf("the probe needs no REST call in between: fetches %d -> %d", fetches, n)
	}
	if s.dildoRewrites != 0 || strings.Contains(buf.String(), "resync moved") {
		t.Fatalf("dildoRewrites %d without any resync; log: %s", s.dildoRewrites, buf.String())
	}
	// the bar closes: the same floor cancel, now on the closed bar - still no rewrite
	ref = trade(time.UnixMilli(f+minuteMs).Add(CloseGrace), core.Agg{Sym: "Q", Open: 1.05, High: 1.05, Low: 1.05, Close: 1.05, Vol: 100, StartMs: f + minuteMs, EndMs: f + minuteMs + 1000})
	if ref.PbState != dildopb.PbCancelled || ref.TouchMs != f || s.dildoRewrites != 0 {
		t.Fatalf("closed: state %d touch %d rewrites %d, want CANCELLED on 10:32, 0 rewrites", ref.PbState, ref.TouchMs, s.dildoRewrites)
	}
}

// dildoAfterTheFact counts a published touch or pullback bar that a later
// evaluation of the same dildo moved or wiped - and not the forming bar's own
// TOUCH -> floor CANCEL, which the rule publishes with no touch bar.
func TestDildoAfterTheFactCountsOnlyRewrites(t *testing.T) {
	const d, t1, t2 = int64(1_000_000), int64(1_060_000), int64(1_120_000)
	for _, c := range []struct {
		name     string
		old, cur core.EmaFlagRef
		want     int64
	}{
		{"forming TOUCH, then the same bar breaks the floor (the rule's verdict)",
			core.EmaFlagRef{DildoMs: d, PbState: dildopb.PbTouching, TouchMs: t1},
			core.EmaFlagRef{DildoMs: d, PbState: dildopb.PbCancelled}, 0},
		{"forming TOUCH confirmed on that bar's close",
			core.EmaFlagRef{DildoMs: d, PbState: dildopb.PbTouching, TouchMs: t1},
			core.EmaFlagRef{DildoMs: d, PbState: dildopb.PbConfirmed, TouchMs: t1, PbMs: t1}, 0},
		{"a new dildo (another anchor)",
			core.EmaFlagRef{DildoMs: d, PbState: dildopb.PbConfirmed, TouchMs: t1, PbMs: t1},
			core.EmaFlagRef{DildoMs: t2, PbState: dildopb.PbWatching}, 0},
		{"a resync moved the touch bar",
			core.EmaFlagRef{DildoMs: d, PbState: dildopb.PbCancelled, TouchMs: t1},
			core.EmaFlagRef{DildoMs: d, PbState: dildopb.PbCancelled, TouchMs: t2}, 1},
		{"a resync wiped a forming TOUCH (back to watching)",
			core.EmaFlagRef{DildoMs: d, PbState: dildopb.PbTouching, TouchMs: t1},
			core.EmaFlagRef{DildoMs: d, PbState: dildopb.PbWatching}, 1},
		{"a resync wiped a closed bar's touch",
			core.EmaFlagRef{DildoMs: d, PbState: dildopb.PbCancelled, TouchMs: t1},
			core.EmaFlagRef{DildoMs: d, PbState: dildopb.PbCancelled}, 1},
		{"a resync moved the confirmed pullback",
			core.EmaFlagRef{DildoMs: d, PbState: dildopb.PbConfirmed, TouchMs: t1, PbMs: t1},
			core.EmaFlagRef{DildoMs: d, PbState: dildopb.PbConfirmed, TouchMs: t2, PbMs: t2}, 1},
		{"a resync wiped the confirmed pullback",
			core.EmaFlagRef{DildoMs: d, PbState: dildopb.PbConfirmed, TouchMs: t1, PbMs: t1},
			core.EmaFlagRef{DildoMs: d, PbState: dildopb.PbCancelled, TouchMs: t1}, 1},
	} {
		e := newEnv(t)
		s := e.service(defaultTuning())
		s.dildoAfterTheFact(&symState{sym: "Q"}, c.old, c.cur)
		if s.dildoRewrites != c.want {
			t.Errorf("%s: dildoRewrites %d, want %d", c.name, s.dildoRewrites, c.want)
		}
	}
}

// ─── replay: MB 2026-08-07, the calibration's reference dildo ────────────

// The engine's hit for MB's 16:51 ET dildo arrives at 16:52:03; REST serves
// what a chart.get would at each moment (every closed bar), the tap delivers
// every later print second by second from then on. The published pullback must
// show TOUCH (2) while the 17:01 bar forms, then CONFIRMED (3) with PbMs = 17:01
// once that bar has closed - when the 17:02 bar's first print arrives, or at
// its end + CloseGrace if none does - and never step back from 2 to 1.
func TestReplayMBDildoPullbackThroughTheTap(t *testing.T) {
	raw, err := os.ReadFile(fixturesDir + "/dildo/dildo-mb-2026-08-07.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Bars []core.Bar `json:"bars"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	bars := fx.Bars
	at := func(h, m int) int64 { return time.Date(2026, 8, 7, h, m, 0, 0, etLocation()).UnixMilli() }
	dildoTs, pbTs := at(16, 51), at(17, 1)

	const sym = "MB"
	e := newEnv(t)
	e.setBars(func(context.Context, string) ([]core.Bar, error) {
		minute := e.clock().UnixMilli() / minuteMs * minuteMs
		n := 0
		for n < len(bars) && bars[n].T < minute {
			n++
		}
		return bars[:n], nil
	})
	s := e.service(defaultTuning())
	start := time.UnixMilli(dildoTs + minuteMs).Add(3 * time.Second) // 16:52:03: the engine's judge
	admMinute := dildoTs + minuteMs
	due := map[int64][]core.Agg{}
	for _, b := range bars {
		if b.T < admMinute {
			continue
		}
		for _, a := range subAggs(sym, b) {
			due[a.StartMs/1000] = append(due[a.StartMs/1000], a)
		}
	}
	end := time.UnixMilli(pbTs + 3*minuteMs)
	type point struct {
		at    time.Time
		state int
		touch int64
		pbMs  int64
		pbPx  float64
	}
	var seen []point
	for now := start.Truncate(time.Second); !now.After(end); now = now.Add(time.Second) {
		e.setNow(now)
		if now.Equal(start.Truncate(time.Second)) {
			s.handleHot(hitAt(sym, dildoTs), now)
		}
		s.tick(now)
		e.settle(s, now)
		for _, a := range due[now.Unix()] {
			s.handleAgg(a, now)
		}
		s.evaluate(now)
		ref, ok := e.lastPub()[sym]
		if !ok {
			continue
		}
		if ref.DildoMs != dildoTs || !ref.HotOnly {
			t.Fatalf("%s: ref %+v, want the 16:51 dildo on a hot-only MB", now.In(etLocation()).Format("15:04:05"), ref)
		}
		if len(seen) == 0 || seen[len(seen)-1].state != ref.PbState {
			seen = append(seen, point{now, ref.PbState, ref.TouchMs, ref.PbMs, ref.PbPx})
		}
	}
	clock := func(t time.Time) string { return t.In(etLocation()).Format("15:04:05") }
	for _, p := range seen {
		t.Logf("%s state %d touch %s pb %s px %.4f", clock(p.at), p.state, clock(time.UnixMilli(p.touch)), clock(time.UnixMilli(p.pbMs)), p.pbPx)
	}
	var touch, conf *point
	for i := range seen {
		if seen[i].state == dildopb.PbTouching && touch == nil {
			touch = &seen[i]
		}
		if seen[i].state == dildopb.PbConfirmed && conf == nil {
			conf = &seen[i]
		}
		if i > 0 && seen[i-1].state == dildopb.PbTouching && seen[i].state == dildopb.PbWatching {
			t.Fatalf("TOUCH went back to watching at %s", clock(seen[i].at))
		}
	}
	if touch == nil || touch.at.UnixMilli() < pbTs || touch.at.UnixMilli() >= pbTs+minuteMs || touch.touch != pbTs {
		t.Fatalf("TOUCH %+v, want it published while the 17:01 bar formed", touch)
	}
	wantConf := time.UnixMilli(pbTs + minuteMs).Add(CloseGrace)
	for _, b := range bars {
		if b.T == pbTs+minuteMs { // a 17:02 bar: its first print (:00) closes 17:01 at once
			wantConf = time.UnixMilli(b.T)
		}
	}
	if conf == nil || !conf.at.Equal(wantConf) || conf.pbMs != pbTs || math.Abs(conf.pbPx-7.93) > 0.005 {
		t.Fatalf("CONFIRMED %+v, want PbMs 17:01 at %s, close ~7.93", conf, clock(wantConf))
	}
	if s.dildoParityMiss != 0 || e.countLog("bars:MB") < 2 {
		t.Fatalf("parity misses %d, MB fetches %d (want the first load and the early resync)", s.dildoParityMiss, e.countLog("bars:MB"))
	}
}

// ─── the loop ────────────────────────────────────────────────────────────

// Through Run: a hit on Config.Hot admits the symbol and its HotOnly verdict
// is published; a closed hand-off does not stop or spin the loop.
func TestRunReadsTheHotChannel(t *testing.T) {
	e := newEnv(t)
	e.flatAll()
	hot := make(chan core.DildoHit, 4)
	cfg := e.config()
	cfg.Hot = hot
	s := e.serviceCfg(cfg, fastTuning())
	ctx, cancel := context.WithCancel(context.Background())
	done := startRun(t, s, ctx)
	hot <- hitAt("H", e.clock().Truncate(time.Minute).UnixMilli()-minuteMs)
	close(hot)
	waitFor(t, "a HotOnly verdict for H", func() bool {
		for _, m := range e.published() {
			if r, ok := m["H"]; ok && r.HotOnly {
				return true
			}
		}
		return false
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A hit whose hold has already run out (a hand-off that lagged half an hour,
// a replay) admits nothing.
func TestHotStaleHitAdmitsNothing(t *testing.T) {
	e := newEnv(t)
	e.flatAll()
	s := e.service(defaultTuning())
	now := et(10, 0, 0)
	e.setNow(now)
	stale := now.Add(-HotHold - minuteMs*time.Millisecond).UnixMilli()
	s.handleHot(hitAt("OLD", stale), now)
	if len(s.hot) != 0 || len(s.syms) != 0 || len(e.taps) != 0 {
		t.Fatalf("a stale hit admitted something: hot %v syms %v taps %v", s.hot, tracked(s), e.taps)
	}
	s.handleHot(hitAt("NEW", now.Add(-HotHold).UnixMilli()), now) // its hold ends a minute from now
	if _, ok := s.syms["NEW"]; !ok {
		t.Fatal("a hit whose hold still runs must be admitted")
	}
}

// ─── the trader's hooks: OnHot, and the dildo on every update ────────────

// A symbol tracked only because of a green dildo reaches Config.OnHot (never OnEval),
// carrying the dildo rule's call (no events), the dildo bar and a copy of the latest
// bars; it is never Primed and never primes the symbol for a later OnEval.
func TestOnHotSeesHotOnlySymbolsWithTheDildo(t *testing.T) {
	bars, dildoTs := quietDildo()
	e := newEnv(t)
	e.setBars(func(context.Context, string) ([]core.Bar, error) { return bars, nil })
	evals, hots := &collector{}, &collector{}
	cfg := e.config()
	cfg.OnEval, cfg.OnHot = evals.add, hots.add
	s := e.serviceCfg(cfg, defaultTuning())
	now := time.UnixMilli(dildoTs + 3*minuteMs + 5_000)
	e.setNow(now)
	s.handleHot(hitAt("Q", dildoTs), now)
	e.settle(s, now)
	e.step(s, now)
	if got := evals.take(); len(got) != 0 {
		t.Fatalf("OnEval saw the hot-only symbol: %v", symsOf(got))
	}
	got := hots.take()
	if len(got) == 0 {
		t.Fatal("OnHot was not called")
	}
	u := got[len(got)-1]
	if u.Sym != "Q" || !u.HotOnly || u.Primed {
		t.Fatalf("update %s hotOnly %v primed %v", u.Sym, u.HotOnly, u.Primed)
	}
	if u.Dildo.LastDildoTs != dildoTs || u.Dildo.Floor != (1.00+1.09)/2 || u.Dildo.Events != nil {
		t.Fatalf("dildo %+v", u.Dildo)
	}
	if u.DildoBar.T != dildoTs || u.DildoBar.C != 1.09 || u.DildoBar.O != 1.00 {
		t.Fatalf("dildo bar %+v", u.DildoBar)
	}
	if n := len(u.Recent); n == 0 || n > updateRecentBars || u.Recent[n-1] != u.Last || u.Recent[0].T >= u.Recent[n-1].T {
		t.Fatalf("recent %+v (last %+v)", u.Recent, u.Last)
	}
	if u.Result.EmaClosedTs <= 0 || !(u.Result.EmaClosed > 0) {
		t.Fatalf("no closed-bar EMA: %+v", u.Result.EmaClosedTs)
	}
	// the copy is the consumer's: changing it touches nothing of the service's
	u.Recent[len(u.Recent)-1].C = 99
	if st := s.syms["Q"]; st.bars[len(st.bars)-1].C == 99 || st.primed {
		t.Fatalf("the service shares its bars or primed the symbol (primed %v)", st.primed)
	}
	// Without an OnHot hook nothing is called for it (and nothing breaks).
	e2 := newEnv(t)
	e2.setBars(func(context.Context, string) ([]core.Bar, error) { return bars, nil })
	ev2 := &collector{}
	cfg2 := e2.config()
	cfg2.OnEval = ev2.add
	s2 := e2.serviceCfg(cfg2, defaultTuning())
	e2.setNow(now)
	s2.handleHot(hitAt("Q", dildoTs), now)
	e2.settle(s2, now)
	e2.step(s2, now)
	if got := ev2.take(); len(got) != 0 {
		t.Fatalf("OnEval saw a hot-only symbol: %v", symsOf(got))
	}
}

// OnEval's updates (candidates and pins) carry the same dildo fields, HotOnly false.
func TestOnEvalCarriesTheDildoAndRecentBars(t *testing.T) {
	bars, dildoTs := quietDildo()
	e := newEnv(t)
	e.setCands(core.PmVolEntry{Sym: "Q", PmVolX: 2})
	e.setBars(func(context.Context, string) ([]core.Bar, error) { return bars, nil })
	evals, hots := &collector{}, &collector{}
	cfg := e.config()
	cfg.OnEval, cfg.OnHot = evals.add, hots.add
	s := e.serviceCfg(cfg, defaultTuning())
	now := time.UnixMilli(dildoTs + 3*minuteMs + 5_000)
	e.step(s, now)
	if got := hots.take(); len(got) != 0 {
		t.Fatalf("OnHot saw a candidate: %v", symsOf(got))
	}
	got := evals.take()
	if len(got) == 0 {
		t.Fatal("no OnEval update")
	}
	u := got[len(got)-1]
	if u.HotOnly || u.Dildo.LastDildoTs != dildoTs || u.DildoBar.T != dildoTs || u.Dildo.Events != nil || len(u.Recent) == 0 || u.Recent[len(u.Recent)-1] != u.Last {
		t.Fatalf("update hotOnly %v dildo %+v bar %+v recent %d", u.HotOnly, u.Dildo, u.DildoBar, len(u.Recent))
	}
	// a symbol with no dildo today: zero DildoBar, LastDildoTs 0
	e2 := newEnv(t)
	e2.setCands(core.PmVolEntry{Sym: "F", PmVolX: 2})
	e2.flatAll()
	ev2 := &collector{}
	cfg2 := e2.config()
	cfg2.OnEval = ev2.add
	s2 := e2.serviceCfg(cfg2, defaultTuning())
	e2.step(s2, et(9, 0, 0))
	g2 := ev2.take()
	if len(g2) == 0 || g2[0].Dildo.LastDildoTs != 0 || g2[0].DildoBar != (core.Bar{}) {
		t.Fatalf("no dildo: %+v", g2)
	}
}

// Every engine hit is handed to OnEngineDildo at once, carrying the engine's candle,
// before (and whatever) the chart's bars say of it.
func TestHotHitIsHandedToOnEngineDildo(t *testing.T) {
	e := newEnv(t)
	e.flatAll()
	s := e.service(defaultTuning())
	var got []EvalUpdate
	s.cfg.OnEngineDildo = func(u EvalUpdate) { got = append(got, u) }
	now := et(8, 50, 0)
	e.step(s, now)
	barMs := now.Truncate(time.Minute).UnixMilli() - minuteMs
	s.handleHot(hitAt("W", barMs), now)
	if len(got) != 1 {
		t.Fatalf("%d engine updates, want 1", len(got))
	}
	u := got[0]
	if !u.Engine || u.Sym != "W" || u.NowMs != now.UnixMilli() || u.Dildo.LastDildoTs != barMs ||
		u.DildoBar != (core.Bar{T: barMs, O: 1, H: 1.1, L: 0.99, C: 1.09, V: 200_000}) {
		t.Fatalf("engine update %+v", u)
	}
	s.cfg.OnEngineDildo = func(EvalUpdate) { panic("boom") }
	s.handleHot(hitAt("X", barMs), now) // a panicking hook costs the update, never the loop
	if _, ok := s.hot["X"]; !ok {
		t.Fatal("a panicking OnEngineDildo stopped the admission")
	}
}
