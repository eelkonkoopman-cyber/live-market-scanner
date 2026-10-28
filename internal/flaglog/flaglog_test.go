package flaglog

import (
	"context"
	"errors"
	"math"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/emaflag"
	"github.com/eelkonkoopman/marketscanner/internal/flagscan"
)

var et = func() *time.Location {
	l, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return l
}()

// etMs is an ET wall-clock time as ms.
func etMs(y int, m time.Month, d, hh, mm int) int64 {
	return time.Date(y, m, d, hh, mm, 0, 0, et).UnixMilli()
}

var (
	tue10 = etMs(2026, 10, 6, 10, 0) // "today" in most tests
	nowT  = time.UnixMilli(tue10 + 5*60_000)
)

// fakeRepo is an in-memory core.FlagEventRepo.
type fakeRepo struct {
	mu     sync.Mutex
	rows   map[string][]core.FlagEvent
	pruned bool
	insErr error
	ins    chan struct{} // signalled per Insert call (buffered)
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{rows: map[string][]core.FlagEvent{}, ins: make(chan struct{}, 100)}
}

func (f *fakeRepo) Insert(_ context.Context, day string, evs []core.FlagEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	defer func() { f.ins <- struct{}{} }()
	if f.insErr != nil {
		return f.insErr
	}
	f.rows[day] = append(f.rows[day], evs...)
	return nil
}
func (f *fakeRepo) Day(_ context.Context, day string) ([]core.FlagEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]core.FlagEvent(nil), f.rows[day]...), nil
}
func (f *fakeRepo) Days(_ context.Context, limit int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for d, v := range f.rows {
		if len(v) > 0 {
			out = append(out, d)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (f *fakeRepo) Prune(context.Context, int, string) error { f.pruned = true; return nil }

func ev(ts int64, kind string, price float64) emaflag.Event {
	return emaflag.Event{Ts: ts, Kind: kind, Label: kind + "!", Price: price}
}

func upd(sym string, nowMs int64, evs ...emaflag.Event) flagscan.EvalUpdate {
	return flagscan.EvalUpdate{Sym: sym, NowMs: nowMs, Result: emaflag.Result{Events: evs}}
}

// collect wires a recorder whose publishes land in a slice.
func collect(r *Recorder) (get func() []core.FlagEventsMsg) {
	var mu sync.Mutex
	var got []core.FlagEventsMsg
	r.SetPublish(func(m core.FlagEventsMsg) {
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
	})
	return func() []core.FlagEventsMsg {
		mu.Lock()
		defer mu.Unlock()
		return append([]core.FlagEventsMsg(nil), got...)
	}
}

func TestOnEvalRecordsOnceAndPublishesOnlyNew(t *testing.T) {
	cases := []struct {
		name     string
		updates  []flagscan.EvalUpdate
		wantMem  int
		wantPubs []int // events per publish
	}{
		{"new events recorded", []flagscan.EvalUpdate{
			upd("AAA", tue10+60_000, ev(tue10, "impulse", 5), ev(tue10+60_000, "flat", 5.1)),
		}, 2, []int{2}},
		{"same events again add nothing", []flagscan.EvalUpdate{
			upd("AAA", tue10+60_000, ev(tue10, "impulse", 5)),
			upd("AAA", tue10+120_000, ev(tue10, "impulse", 5)),
		}, 1, []int{1}},
		{"cumulative growth publishes only the delta", []flagscan.EvalUpdate{
			upd("AAA", tue10+60_000, ev(tue10, "impulse", 5)),
			upd("AAA", tue10+120_000, ev(tue10, "impulse", 5), ev(tue10+60_000, "flat", 5)),
		}, 2, []int{1, 1}},
		{"prior-day events ignored", []flagscan.EvalUpdate{
			upd("AAA", tue10, ev(etMs(2026, 10, 5, 15, 59), "exit", 5), ev(tue10, "impulse", 5)),
		}, 1, []int{1}},
		{"only prior-day events: no publish", []flagscan.EvalUpdate{
			upd("AAA", tue10, ev(etMs(2026, 10, 5, 15, 59), "exit", 5)),
		}, 0, nil},
		{"same bar+kind on another symbol is new", []flagscan.EvalUpdate{
			upd("AAA", tue10, ev(tue10, "impulse", 5)),
			upd("BBB", tue10, ev(tue10, "impulse", 5)),
		}, 2, []int{1, 1}},
		{"same bar, other kind is new", []flagscan.EvalUpdate{
			upd("AAA", tue10, ev(tue10, "impulse", 5), ev(tue10, "flat", 5)),
		}, 2, []int{2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := New(newFakeRepo(), func() time.Time { return nowT })
			pubs := collect(r)
			for _, u := range tc.updates {
				r.OnEval(u)
			}
			msg, err := r.Day(context.Background(), "")
			if err != nil {
				t.Fatal(err)
			}
			if len(msg.Events) != tc.wantMem {
				t.Fatalf("in-memory events = %d, want %d: %+v", len(msg.Events), tc.wantMem, msg.Events)
			}
			var counts []int
			for _, p := range pubs() {
				if p.Date != "2026-10-06" {
					t.Fatalf("publish date %q", p.Date)
				}
				counts = append(counts, len(p.Events))
			}
			if !reflect.DeepEqual(counts, tc.wantPubs) {
				t.Fatalf("publish counts %v, want %v", counts, tc.wantPubs)
			}
		})
	}
}

func TestEventFieldsAndNaNPrice(t *testing.T) {
	r := New(newFakeRepo(), func() time.Time { return nowT })
	r.OnEval(upd("AAA", tue10, ev(tue10, "impulse", math.NaN()), ev(tue10+1, "flat", math.Inf(1)), ev(tue10+2, "entry", 7.25)))
	msg, _ := r.Day(context.Background(), "2026-10-06")
	if len(msg.Events) != 3 {
		t.Fatalf("events: %+v", msg.Events)
	}
	if msg.Events[0].Price != nil || msg.Events[1].Price != nil {
		t.Fatalf("NaN/Inf price must be nil: %+v", msg.Events[:2])
	}
	e := msg.Events[2]
	if e.Price == nil || *e.Price != 7.25 || e.Sym != "AAA" || e.Kind != "entry" || e.Label != "entry!" ||
		e.Ts != tue10+2 || e.At != nowT.UnixMilli() {
		t.Fatalf("event fields: %+v", e)
	}
}

func TestDayRolloverResets(t *testing.T) {
	now := nowT
	r := New(newFakeRepo(), func() time.Time { return now })
	pubs := collect(r)
	r.OnEval(upd("AAA", tue10, ev(tue10, "impulse", 5)))

	wed := etMs(2026, 10, 7, 9, 45)
	now = time.UnixMilli(wed)
	// Yesterday's event is still in the bars; today's is new. Same key as
	// Tuesday's would be if the seen-set had not been reset (it must not matter).
	r.OnEval(upd("AAA", wed, ev(tue10, "impulse", 5), ev(wed, "impulse", 6)))

	msg, _ := r.Day(context.Background(), "")
	if msg.Date != "2026-10-07" || len(msg.Events) != 1 || msg.Events[0].Ts != wed {
		t.Fatalf("after rollover: %+v", msg)
	}
	got := pubs()
	if len(got) != 2 || got[1].Date != "2026-10-07" || len(got[1].Events) != 1 {
		t.Fatalf("publishes: %+v", got)
	}
}

func TestLoadMakesKnownEventsNotNew(t *testing.T) {
	repo := newFakeRepo()
	known := core.FlagEvent{Ts: tue10, At: 1, Sym: "AAA", Kind: "impulse", Label: "old"}
	repo.rows["2026-10-06"] = []core.FlagEvent{known}
	r := New(repo, func() time.Time { return nowT })
	pubs := collect(r)
	if err := r.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !repo.pruned {
		t.Fatal("Load must prune")
	}
	// The restart's replay: the known event again plus a genuinely new one.
	r.OnEval(upd("AAA", tue10+60_000, ev(tue10, "impulse", 5), ev(tue10+60_000, "flat", 5)))
	got := pubs()
	if len(got) != 1 || len(got[0].Events) != 1 || got[0].Events[0].Kind != "flat" {
		t.Fatalf("only the new event may publish: %+v", got)
	}
	msg, _ := r.Day(context.Background(), "")
	if len(msg.Events) != 2 || msg.Events[0].Label != "old" {
		t.Fatalf("loaded + new: %+v", msg.Events)
	}
}

func TestDayDatesAndPastDay(t *testing.T) {
	repo := newFakeRepo()
	repo.rows["2026-10-05"] = []core.FlagEvent{{Ts: 9, Sym: "ZZZ", Kind: "exit"}}
	r := New(repo, func() time.Time { return nowT })

	msg, err := r.Day(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Date != "2026-10-06" || len(msg.Events) != 0 || msg.Events == nil {
		t.Fatalf("empty today: %+v", msg)
	}
	if !reflect.DeepEqual(msg.Dates, []string{"2026-10-06", "2026-10-05"}) {
		t.Fatalf("dates must include today (empty) newest first: %v", msg.Dates)
	}
	past, _ := r.Day(context.Background(), "2026-10-05")
	if past.Date != "2026-10-05" || len(past.Events) != 1 || past.Events[0].Sym != "ZZZ" {
		t.Fatalf("past day: %+v", past)
	}

	// Today with events is not duplicated in Dates; the list caps at 30.
	r.OnEval(upd("AAA", tue10, ev(tue10, "impulse", 5)))
	for i := 1; i <= 40; i++ {
		repo.rows[time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -i).Format(dayLayout)] = []core.FlagEvent{{Ts: int64(i)}}
	}
	repo.rows["2026-10-06"] = []core.FlagEvent{{Ts: 1}} // persisted too
	msg, _ = r.Day(context.Background(), "")
	if len(msg.Dates) != 30 || msg.Dates[0] != "2026-10-06" || msg.Dates[1] != "2026-10-05" {
		t.Fatalf("dates: %v", msg.Dates)
	}
	seen := map[string]bool{}
	for _, d := range msg.Dates {
		if seen[d] {
			t.Fatalf("duplicate date %s in %v", d, msg.Dates)
		}
		seen[d] = true
	}
}

func TestDayOrdersByTsThenArrival(t *testing.T) {
	r := New(newFakeRepo(), func() time.Time { return nowT })
	r.OnEval(upd("AAA", tue10+300_000, ev(tue10+120_000, "impulse", 5)))
	r.OnEval(upd("BBB", tue10+300_000, ev(tue10+60_000, "impulse", 5), ev(tue10+120_000, "flat", 5)))
	msg, _ := r.Day(context.Background(), "")
	var order []string
	for _, e := range msg.Events {
		order = append(order, e.Sym+"/"+e.Kind)
	}
	if want := []string{"BBB/impulse", "AAA/impulse", "BBB/flat"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order %v, want %v", order, want)
	}
}

func TestFullQueueDoesNotBlock(t *testing.T) {
	r := newRecorder(newFakeRepo(), func() time.Time { return nowT }, 1)
	pubs := collect(r)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 5; i++ { // no writer running: the queue (cap 1) fills at once
			r.OnEval(upd("AAA", tue10, ev(tue10+int64(i), "impulse", 5)))
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("OnEval blocked on a full queue")
	}
	msg, _ := r.Day(context.Background(), "")
	if len(msg.Events) != 5 || len(pubs()) != 5 {
		t.Fatalf("events stay in memory and publish even when the queue is full: mem=%d pubs=%d", len(msg.Events), len(pubs()))
	}
	if r.drops != 4 {
		t.Fatalf("drops = %d, want 4", r.drops)
	}
}

func TestRunPersistsAndDrainsOnCancel(t *testing.T) {
	repo := newFakeRepo()
	r := New(repo, func() time.Time { return nowT })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	r.OnEval(upd("AAA", tue10, ev(tue10, "impulse", 5)))
	select {
	case <-repo.ins:
	case <-time.After(2 * time.Second):
		t.Fatal("writer never inserted")
	}
	// Queued right before cancel: the final drain must still write it.
	r.OnEval(upd("BBB", tue10, ev(tue10, "flat", 5)))
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not exit")
	}
	got, _ := repo.Day(context.Background(), "2026-10-06")
	if len(got) != 2 {
		t.Fatalf("persisted %d events, want 2: %+v", len(got), got)
	}
}

func TestRunSurvivesInsertError(t *testing.T) {
	repo := newFakeRepo()
	repo.insErr = errors.New("disk full")
	r := New(repo, func() time.Time { return nowT })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	r.OnEval(upd("AAA", tue10, ev(tue10, "impulse", 5)))
	<-repo.ins
	select {
	case err := <-done:
		t.Fatalf("Run must not exit on a write error: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestNilPublishAndEmptyEvents(t *testing.T) {
	r := New(newFakeRepo(), nil) // nil clock = time.Now, nil publish
	r.OnEval(upd("AAA", time.Now().UnixMilli()))
	r.OnEval(upd("AAA", time.Now().UnixMilli(), ev(time.Now().UnixMilli(), "impulse", 1)))
}
