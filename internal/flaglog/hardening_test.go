package flaglog

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/climax"
	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
	"github.com/eelkonkoopman/marketscanner/internal/persist"
)

// Review-2 hardening: the per-dildo verdict guard survives a restart exactly
// (RV2-D1), a failed Load is retried instead of hiding the morning (RV2-D2),
// and rows a full writer queue could not take still reach the DB (RV2-D4).

func hhmmKinds(evs []core.FlagEvent) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = time.UnixMilli(e.Ts).In(et).Format("15:04") + " " + e.Kind
	}
	return out
}

// RV2-D1: a REST rewrite moves the 10:00 dildo's recorded PB (10:04) to 10:02
// and makes 10:03 a new dildo; that dildo's own PB lands at 10:06. A restart
// between 10:03 and 10:06 must not drop the 10:06 9 EMA PULLBACK: the guard is
// rebuilt from the dildo each verdict row judged (persisted), not inferred
// from row order.
func TestRestartKeepsTheVerdictOfADildoARewriteCreated(t *testing.T) {
	m := func(k int64) int64 { return tue10 + k*60_000 }
	morning := dupd("AAA", m(5), true, dev(m(0), dildopb.KindDildo, 1), dev(m(4), dildopb.KindPb, 1.05))
	rewrite := dupd("AAA", m(5)+1000, true, dev(m(0), dildopb.KindDildo, 1), dev(m(2), dildopb.KindPb, 1.03), dev(m(3), dildopb.KindDildo, 1.2))
	later := dupd("AAA", m(7), true, dev(m(0), dildopb.KindDildo, 1), dev(m(2), dildopb.KindPb, 1.03),
		dev(m(3), dildopb.KindDildo, 1.2), dev(m(6), dildopb.KindPb, 1.25))
	want := []string{"10:00 dildo", "10:03 dildo", "10:04 dildoPb", "10:06 dildoPb"}

	// Live, no restart.
	live := New(newFakeRepo(), func() time.Time { return time.UnixMilli(m(7)) })
	live.OnDildo(morning)
	live.OnDildo(rewrite)
	live.OnDildo(later)
	if msg, _ := live.Day(context.Background(), ""); !reflect.DeepEqual(hhmmKinds(msg.Events), want) {
		t.Fatalf("live history %v, want %v", hhmmKinds(msg.Events), want)
	}

	// The same with a restart (through the real SQLite repo) before 10:06.
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "scanner.db")
	db, err := persist.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	r1 := New(db.FlagEventRepo(), func() time.Time { return time.UnixMilli(m(5)) })
	rctx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- r1.Run(rctx) }()
	r1.OnDildo(morning)
	r1.OnDildo(rewrite)
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db2, err := persist.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	r2 := New(db2.FlagEventRepo(), func() time.Time { return time.UnixMilli(m(7)) })
	pubs := collect(r2)
	if err := r2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	r2.OnDildo(rewrite) // the restart's replay: nothing new
	if got := pubs(); len(got) != 0 {
		t.Fatalf("the replay published %v", pubKinds(got))
	}
	r2.OnDildo(later)
	if got := pubKinds(pubs()); !reflect.DeepEqual(got, [][]string{{"AAA/dildoPb"}}) {
		t.Fatalf("after the restart the 10:03 dildo's PB published %v, want [[AAA/dildoPb]]", got)
	}
	if msg, _ := r2.Day(ctx, ""); !reflect.DeepEqual(hhmmKinds(msg.Events), want) {
		t.Fatalf("history after the restart %v, want %v (as live)", hhmmKinds(msg.Events), want)
	}
	// And the moved verdict of the 10:00 dildo still adds nothing.
	r2.OnDildo(dupd("AAA", m(8), true, dev(m(0), dildopb.KindDildo, 1), dev(m(5), dildopb.KindCancel, 0.9)))
	if got := pubs(); len(got) != 1 {
		t.Fatalf("a moved verdict of the 10:00 dildo added %v", pubKinds(got[1:]))
	}
}

// failOnceRepo fails its first n Day calls (a one-off read error at startup).
type failOnceRepo struct {
	*fakeRepo
	mu    sync.Mutex
	fails int
}

func (f *failOnceRepo) Day(ctx context.Context, day string) ([]core.FlagEvent, error) {
	f.mu.Lock()
	if f.fails > 0 {
		f.fails--
		f.mu.Unlock()
		return nil, errors.New("disk I/O error")
	}
	f.mu.Unlock()
	return f.fakeRepo.Day(ctx, day)
}

func sbfmMorning() (*fakeRepo, core.FlagEvent) {
	repo := newFakeRepo()
	row := core.FlagEvent{Ts: etMs(2026, 10, 6, 4, 24), Sym: "SBFM", Kind: dildopb.KindDildo, Label: "GREEN DILDO", Note: "engine"}
	repo.rows["2026-10-06"] = []core.FlagEvent{
		row,
		{Ts: etMs(2026, 10, 6, 4, 40), Sym: "XYZ", Kind: climax.KindClimax, Label: "VOLUME CLIMAX"},
	}
	return repo, row
}

// RV2-D2: Load fails at startup; the first new signal must not hide the
// morning's rows for the rest of the session - serving today retries the load
// and merges it (seen-set included, so a replay of a loaded row is not new).
func TestFailedLoadIsRetriedWhenTodayIsServed(t *testing.T) {
	repo, sbfm := sbfmMorning()
	r := New(&failOnceRepo{fakeRepo: repo, fails: 1}, func() time.Time { return time.UnixMilli(tue10 + 6*60_000) })
	pubs := collect(r)
	if err := r.Load(context.Background()); err == nil {
		t.Fatal("want the load error")
	}
	r.OnSignal(core.SignalHit{Sym: "NEW", Kind: dildopb.KindDildo, BarMs: tue10 + 5*60_000, Price: 2, O: 1.8, C: 2})
	msg, err := r.Day(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(msg.Events); !reflect.DeepEqual(got, []string{"SBFM/dildo", "XYZ/climax", "NEW/dildo"}) {
		t.Fatalf("today after a failed load serves %v, want the morning too", got)
	}
	r.OnDildo(dupd("SBFM", tue10+6*60_000, true, dev(sbfm.Ts, dildopb.KindDildo, 1.09)))
	if got := pubKinds(pubs()); !reflect.DeepEqual(got, [][]string{{"NEW/dildo"}}) {
		t.Fatalf("publishes %v: a replay of a loaded row is not new", got)
	}
	if msg, _ := r.Day(context.Background(), ""); len(msg.Events) != 3 {
		t.Fatalf("a second serve changed the day: %v", kinds(msg.Events))
	}
}

// RV2-D2, the writer side: with no client asking, the writer retries the
// failed load after it writes.
func TestFailedLoadIsRetriedByTheWriter(t *testing.T) {
	repo, sbfm := sbfmMorning()
	r := New(&failOnceRepo{fakeRepo: repo, fails: 1}, func() time.Time { return time.UnixMilli(tue10 + 6*60_000) })
	pubs := collect(r)
	if err := r.Load(context.Background()); err == nil {
		t.Fatal("want the load error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx) //nolint:errcheck
	r.OnSignal(core.SignalHit{Sym: "NEW", Kind: dildopb.KindDildo, BarMs: tue10 + 5*60_000, Price: 2, O: 1.8, C: 2})
	deadline := time.Now().Add(3 * time.Second)
	for {
		r.mu.Lock()
		n := len(r.events)
		r.mu.Unlock()
		if n == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the writer never merged the morning: %d events in memory", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.OnDildo(dupd("SBFM", tue10+6*60_000, true, dev(sbfm.Ts, dildopb.KindDildo, 1.09)))
	if got := pubKinds(pubs()); !reflect.DeepEqual(got, [][]string{{"NEW/dildo"}}) {
		t.Fatalf("publishes %v: a replay of a loaded row is not new", got)
	}
}

// RV2-D4: rows a full writer queue could not take (the engine's alerts are
// never replayed) are held and written once the writer catches up.
func TestFullQueueRowsStillReachTheRepo(t *testing.T) {
	repo := newFakeRepo()
	c := tue10 + 60_000
	r := newRecorder(repo, func() time.Time { return nowT }, 1)
	// No writer running: the queue (cap 1) fills at once.
	r.OnSignal(core.SignalHit{Sym: "AAA", Kind: climax.KindClimax, BarMs: c, Price: 9, Low: 9, Mid: 9.4})
	r.OnSignal(core.SignalHit{Sym: "AAA", Kind: climax.KindBottom, BarMs: c + 60_000, Price: 9.6, Mid: 9.4, Low: 9, ClimaxMs: c, Bars: 1})
	r.OnSignal(core.SignalHit{Sym: "BBB", Kind: climax.KindFail, BarMs: c + 60_000, Price: 8.9, Mid: 9.4, Low: 9, ClimaxMs: c, Bars: 1})
	if r.drops != 2 {
		t.Fatalf("drops = %d, want 2", r.drops)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx) //nolint:errcheck
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, _ := repo.Day(context.Background(), "2026-10-06")
		if len(got) == 3 {
			if k := kinds(got); !reflect.DeepEqual(k, []string{"AAA/climax", "AAA/bottom", "BBB/climaxFail"}) {
				t.Fatalf("persisted %v", k)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("persisted %v, want all three alerts", kinds(got))
		}
		time.Sleep(5 * time.Millisecond)
	}
}
