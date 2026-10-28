package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// TestPmVolXMatchesToRowEverywhere is the one-formula guarantee: for every
// combination of session and ticker state, tickerState.pmVolX (what the
// 9EMA-flag candidate list ranks by) and the Row.PmVolX column toRow stamps
// agree exactly -- same value when known, Null exactly when ok=false. There is
// one method behind both, and this table fails if anyone re-inlines a second
// copy of the formula into either side.
func TestPmVolXMatchesToRowEverywhere(t *testing.T) {
	states := map[string]func() *tickerState{
		"adv10 unknown": func() *tickerState {
			ts := newTS5()
			ts.sessVol = 5_000_000
			ts.frozenPre = frozenScope{set: true, vol: 4_000_000, complete: true}
			return ts
		},
		"adv10 known, nothing frozen": func() *tickerState {
			ts := newTS5()
			ts.adv10, ts.sessVol = 1_000_000, 3_500_000
			return ts
		},
		"adv10 known, frozen complete": func() *tickerState {
			ts := newTS5()
			ts.adv10, ts.sessVol = 2_000_000, 9_000_000
			ts.frozenPre = frozenScope{set: true, vol: 4_000_000, complete: true}
			return ts
		},
		"adv10 known, frozen incomplete": func() *tickerState {
			ts := newTS5()
			ts.adv10, ts.sessVol = 2_000_000, 9_000_000
			ts.frozenPre = frozenScope{set: true, vol: 4_000_000, complete: false}
			return ts
		},
		"adv10 known, frozen slot never set": func() *tickerState {
			ts := newTS5()
			ts.adv10, ts.sessVol = 2_000_000, 9_000_000
			ts.frozenPre = frozenScope{complete: true} // complete without set: still no value
			return ts
		},
	}
	sessions := []core.Session{core.SessionClosed, core.SessionPre, core.SessionRegular, core.SessionPost}
	for name, mk := range states {
		for _, sess := range sessions {
			ts := mk()
			x, ok := ts.pmVolX(sess)
			r := ts.toRow(0, nil, nil, nil, sess)
			if ok == core.IsNull(r.PmVolX) {
				t.Errorf("%s / %v: pmVolX ok=%v but row.PmVolX=%v (known and null must be opposites)", name, sess, ok, r.PmVolX)
				continue
			}
			if ok && x != r.PmVolX {
				t.Errorf("%s / %v: pmVolX %v != row.PmVolX %v", name, sess, x, r.PmVolX)
			}
		}
	}

	// Spot-check the values themselves, so "both wrong the same way" can't pass.
	live := states["adv10 known, nothing frozen"]()
	if x, ok := live.pmVolX(core.SessionPre); !ok || x != 3.5 {
		t.Fatalf("live pre-market: want 3.5, got %v ok=%v", x, ok)
	}
	if _, ok := live.pmVolX(core.SessionRegular); ok {
		t.Fatal("after 09:30 with no frozen pre-market there is no value (day volume is not pre-market volume)")
	}
	frozen := states["adv10 known, frozen complete"]()
	if x, ok := frozen.pmVolX(core.SessionRegular); !ok || x != 2 {
		t.Fatalf("frozen complete: want the frozen 4M / 2M = 2, got %v ok=%v", x, ok)
	}
	if x, ok := frozen.pmVolX(core.SessionPre); !ok || x != 4.5 {
		t.Fatalf("while pre-market is live the day volume wins: want 9M / 2M = 4.5, got %v ok=%v", x, ok)
	}
}

// pmvolEngine starts a running engine whose clock the test moves (the pre-market
// "complete" flag is decided from that clock at the 04:00 reset).
func pmvolEngine(t *testing.T, at time.Time) (*Engine, *atomic.Pointer[time.Time]) {
	t.Helper()
	var clock atomic.Pointer[time.Time]
	clock.Store(&at)
	e := New(Options{TickHz: 50, Now: func() time.Time { return *clock.Load() }})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = e.Run(ctx) }()
	return e, &clock
}

// volAgg is one aggregate carrying only what pmVolX reads: a print and a volume.
func volAgg(sym string, vol int64) core.Agg {
	const sec = int64(1_700_000_000)
	return core.Agg{Sym: sym, Open: 10, High: 10, Low: 10, Close: 10, Vol: vol, StartMs: sec * 1000, EndMs: (sec + 1) * 1000}
}

// rowWhere waits for a snapshot row of sym that satisfies cond and returns it.
func rowWhere(t *testing.T, e *Engine, sym string, cond func(core.Row) bool) core.Row {
	t.Helper()
	var got core.Row
	waitForCond(t, e, sym, 3*time.Second, func(r core.Row) bool {
		if cond(r) {
			got = r
			return true
		}
		return false
	})
	return got
}

func leaderSyms(es []core.PmVolEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Sym
	}
	return out
}

func sameSyms(got []core.PmVolEntry, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i].Sym != want[i] {
			return false
		}
	}
	return true
}

// Live pre-market: the multiple is today's volume so far over adv10, a symbol
// is a candidate at EXACTLY the threshold (>=), the order is by multiple desc
// with a symbol tiebreak, the cap trims from the bottom, and a symbol without
// an adv10 never qualifies however much it has traded. Every returned multiple
// equals the pmVolX column the same symbol shows in the snapshot.
func TestPmVolLeadersLivePreMarket(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	e, _ := pmvolEngine(t, time.Date(2026, 10, 5, 8, 0, 0, 0, ny))
	e.SetSession(core.SessionPre)
	_ = e.Curves(0) // ctrl round trip: the 04:00 reset has landed before any print

	vols := map[string][2]int64{ // sym -> {volume so far, adv10}; adv10 0 = never seeded
		"LIVE1": {3_500_000, 1_000_000}, // 3.5x
		"TIE":   {3_500_000, 1_000_000}, // 3.5x, ties LIVE1 -> symbol order
		"EDGE":  {2_000_000, 2_000_000}, // exactly 1.0x
		"LOW":   {400_000, 1_000_000},   // 0.4x
		"NOADV": {9_000_000, 0},         // huge volume, unknown average
	}
	for sym, v := range vols {
		e.Apply(volAgg(sym, v[0]))
	}
	for sym := range vols {
		waitForSym(t, e, sym, 3*time.Second)
	}
	adv := map[string]float64{}
	for sym, v := range vols {
		if v[1] > 0 {
			adv[sym] = float64(v[1])
		}
	}
	e.SetAdv10(adv)
	_ = e.Curves(0)

	got := e.PmVolLeaders(1.0, 0)
	if !sameSyms(got, "LIVE1", "TIE", "EDGE") {
		t.Fatalf("pmVolX >= 1 want [LIVE1 TIE EDGE] (desc, symbol tiebreak, EDGE at exactly 1.0, NOADV never), got %v", leaderSyms(got))
	}
	if got[0].PmVolX != 3.5 || got[2].PmVolX != 1.0 {
		t.Fatalf("multiples want 3.5 .. 1.0, got %v and %v", got[0].PmVolX, got[2].PmVolX)
	}
	if got := e.PmVolLeaders(1.0, 2); !sameSyms(got, "LIVE1", "TIE") {
		t.Fatalf("limit 2 keeps the TOP two, got %v", leaderSyms(got))
	}
	if got := e.PmVolLeaders(1.0, -1); len(got) != 3 {
		t.Fatalf("limit <= 0 is uncapped (a scan's Limit convention), got %v", leaderSyms(got))
	}
	if got := e.PmVolLeaders(3.5, 0); !sameSyms(got, "LIVE1", "TIE") {
		t.Fatalf("threshold is inclusive at 3.5: want [LIVE1 TIE], got %v", leaderSyms(got))
	}
	if got := e.PmVolLeaders(3.6, 0); len(got) != 0 {
		t.Fatalf("nothing is at 3.6x, got %v", leaderSyms(got))
	}
	if got := e.PmVolLeaders(0.1, 0); !sameSyms(got, "LIVE1", "TIE", "EDGE", "LOW") {
		t.Fatalf("a low threshold adds LOW but still never NOADV, got %v", leaderSyms(got))
	}

	// The accessor and the column agree for every symbol it returned...
	for _, ent := range e.PmVolLeaders(0.1, 0) {
		ent := ent
		r := rowWhere(t, e, ent.Sym, func(r core.Row) bool { return !core.IsNull(r.PmVolX) })
		if r.PmVolX != ent.PmVolX {
			t.Errorf("%s: PmVolLeaders says %v, the snapshot's pmVolX column says %v", ent.Sym, ent.PmVolX, r.PmVolX)
		}
	}
	// ...and a symbol it left out has no column value either.
	r := rowWhere(t, e, "NOADV", func(r core.Row) bool { return r.Vol == 9_000_000 })
	if !core.IsNull(r.PmVolX) || !core.IsNull(r.Adv10) {
		t.Fatalf("NOADV must have a null pmVolX/adv10, got %v/%v", r.PmVolX, r.Adv10)
	}
}

// After 09:30 the multiple is the FROZEN pre-market volume, not the day volume
// (regular-session prints must not inflate it), it survives into post-market,
// and the accessor still agrees with the column.
func TestPmVolLeadersFrozenCompleteAfterTheOpen(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	e, clock := pmvolEngine(t, time.Date(2026, 10, 5, 4, 0, 5, 0, ny)) // watched since the real 04:00 open
	e.SetSession(core.SessionPre)
	_ = e.Curves(0)

	e.Apply(volAgg("FZ", 4_000_000))    // 2.0x of its adv10 by 09:30
	e.Apply(volAgg("FZLOW", 500_000))   // 0.5x
	e.Apply(volAgg("FZNOADV", 700_000)) // never seeded
	for _, s := range []string{"FZ", "FZLOW", "FZNOADV"} {
		waitForSym(t, e, s, 3*time.Second)
	}
	e.SetAdv10(map[string]float64{"FZ": 2_000_000, "FZLOW": 1_000_000})
	_ = e.Curves(0)

	if got := e.PmVolLeaders(1.0, 0); !sameSyms(got, "FZ") || got[0].PmVolX != 2.0 {
		t.Fatalf("live pre-market: want [FZ 2.0], got %+v", got)
	}

	open := time.Date(2026, 10, 5, 9, 30, 0, 0, ny)
	clock.Store(&open)
	e.SetSession(core.SessionRegular) // freeze, then wipe the scope
	_ = e.Curves(0)
	e.Apply(volAgg("FZ", 6_000_000)) // regular-session volume: day total is now 10M, frozen is still 4M
	rowWhere(t, e, "FZ", func(r core.Row) bool { return r.Vol == 10_000_000 && r.PreVol == 4_000_000 })

	got := e.PmVolLeaders(1.0, 0)
	if !sameSyms(got, "FZ") || got[0].PmVolX != 2.0 {
		t.Fatalf("after the open the frozen 4M/2M = 2.0 counts, not the 10M day volume: got %+v", got)
	}
	r := rowWhere(t, e, "FZ", func(r core.Row) bool { return !core.IsNull(r.PmVolX) })
	if r.PmVolX != got[0].PmVolX {
		t.Fatalf("snapshot pmVolX %v != PmVolLeaders %v", r.PmVolX, got[0].PmVolX)
	}

	e.SetSession(core.SessionPost) // the frozen pre-market value is held all day
	_ = e.Curves(0)
	if got := e.PmVolLeaders(1.0, 0); !sameSyms(got, "FZ") || got[0].PmVolX != 2.0 {
		t.Fatalf("post-market must keep the frozen value: got %+v", got)
	}
}

// A process that started mid-pre-market froze only PART of the session at 09:30
// (frozenPre.complete=false): its multiple would understate the truth, so the
// symbol is excluded after the open -- exactly as the pmVolX column reads Null
// for it -- while during pre-market itself it qualified on the day volume.
func TestPmVolLeadersFrozenIncompleteExcluded(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	e, clock := pmvolEngine(t, time.Date(2026, 10, 5, 7, 43, 0, 0, ny)) // started mid-pre-market
	e.SetSession(core.SessionPre)
	_ = e.Curves(0)

	e.Apply(volAgg("PART", 5_000_000))
	waitForSym(t, e, "PART", 3*time.Second)
	e.SetAdv10(map[string]float64{"PART": 1_000_000})
	_ = e.Curves(0)
	if got := e.PmVolLeaders(1.0, 0); !sameSyms(got, "PART") || got[0].PmVolX != 5.0 {
		t.Fatalf("live pre-market PART is 5.0x and qualifies, got %+v", got)
	}

	open := time.Date(2026, 10, 5, 9, 30, 0, 0, ny)
	clock.Store(&open)
	e.SetSession(core.SessionRegular)
	_ = e.Curves(0)

	if got := e.PmVolLeaders(0, 0); len(got) != 0 {
		t.Fatalf("a partial frozen pre-market would understate it: PART must be excluded, got %+v", got)
	}
	// Non-vacuous: the freeze did run (pre* is populated) and the column is Null.
	r := rowWhere(t, e, "PART", func(r core.Row) bool { return r.PreVol == 5_000_000 && r.Sess == core.SessionRegular })
	if !core.IsNull(r.PmVolX) {
		t.Fatalf("the column must be Null too, got %v", r.PmVolX)
	}
}

// PmVolLeaders is a ctrl-closure fan-out: under -race this proves it never
// touches shard state outside the shard goroutine, even while aggregates and
// adv10 re-seeds stream in.
func TestPmVolLeadersRaceWithApply(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	e, _ := pmvolEngine(t, time.Date(2026, 10, 5, 8, 0, 0, 0, ny))
	e.SetSession(core.SessionPre)
	_ = e.Curves(0)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		syms := []string{"AAA", "BBB", "CCC", "DDD"}
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			e.Apply(volAgg(syms[i%len(syms)], 5_000))
			if i%50 == 0 {
				e.SetAdv10(map[string]float64{"AAA": 1_000, "BBB": 2_000, "CCC": 3_000, "DDD": 4_000})
			}
		}
	}()
	for i := 0; i < 50; i++ {
		_ = e.PmVolLeaders(1.0, 3)
		_ = e.SessionBars()
	}
	close(stop)
	wg.Wait()
}
