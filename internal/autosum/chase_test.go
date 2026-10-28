package autosum

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// chaseFeed models the race the chaser exists for: the per-ticker fetch
// returns nothing until call number readyAt, when the article finally hits
// Benzinga's REST index.
type chaseFeed struct {
	core.FeedClient // embed nil: only BenzingaNewsFor is called

	mu      sync.Mutex
	calls   int
	readyAt int
	item    core.NewsItem
}

func (f *chaseFeed) BenzingaNewsFor(context.Context, string, int64) ([]core.NewsItem, map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.readyAt > 0 && f.calls >= f.readyAt {
		return []core.NewsItem{f.item}, nil, nil
	}
	return nil, nil, nil
}

func (f *chaseFeed) n() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

func chaseWorker(ff *chaseFeed, fs *fakeSum) *Worker {
	return New(Config{
		Feed: ff, News: &fakeArticles{cache: map[string]core.NewsItem{}}, Sum: fs, Cache: &fakeCache{},
		MaxPerSym: 2, MaxPerDay: 5, Cooldown: time.Minute,
		ChaseDelays: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond},
	})
}

// The pop beats the index: entry fetch is empty, the headline appears on the
// second chase attempt. It must be broadcast (news dot) and summarized, and
// the chase must stop as soon as it catches — not run out its schedule.
func TestChaseCatchesLateIndexedHeadline(t *testing.T) {
	ff := &chaseFeed{
		readyAt: 3, // entry fetch + 1 empty chase, lands on chase attempt 2
		item:    core.NewsItem{ID: "hot1", Tickers: []string{"POP"}, TS: time.Now().Add(-5 * time.Second).UnixMilli()},
	}
	fs := &fakeSum{}
	w := chaseWorker(ff, fs)

	w.runSym(context.Background(), "POP")

	select {
	case batch := <-w.news:
		if len(batch) != 1 || batch[0].ID != "hot1" {
			t.Fatalf("broadcast batch mismatch: %+v", batch)
		}
	default:
		t.Fatal("chased headline was never broadcast — the scanner dot would stay dark")
	}
	if fs.n() != 1 || fs.calls[0] != "hot1" {
		t.Fatalf("chased headline should be summarized once, got %v", fs.calls)
	}
	if got := ff.n(); got != 3 {
		t.Fatalf("chase must stop on the catching attempt: want 3 fetches, got %d", got)
	}
}

// No headline ever appears: the chase must run out its schedule and give up
// quietly — no broadcast, no summaries, no leftover retries.
func TestChaseGivesUp(t *testing.T) {
	ff := &chaseFeed{} // always empty
	fs := &fakeSum{}
	w := chaseWorker(ff, fs)

	w.runSym(context.Background(), "POP")

	select {
	case b := <-w.news:
		t.Fatalf("nothing to broadcast, got %+v", b)
	default:
	}
	if fs.n() != 0 {
		t.Fatalf("nothing to summarize, got %v", fs.calls)
	}
	if got, want := ff.n(), 1+4; got != want {
		t.Fatalf("want entry fetch + every chase attempt = %d, got %d", want, got)
	}
}

// A stale morning article must not stop the chase: the pop implies a NEW
// headline, so "day news exists but nothing fresh" still chases — and the
// fresh article wins once it lands.
func TestChaseRunsDespiteStaleDayNews(t *testing.T) {
	stale := core.NewsItem{ID: "old1", Tickers: []string{"POP"}, TS: time.Now().Add(-3 * time.Hour).UnixMilli()}
	fresh := core.NewsItem{ID: "hot2", Tickers: []string{"POP"}, TS: time.Now().UnixMilli()}

	ff := &staleThenFreshFeed{stale: stale, fresh: fresh, freshAt: 2}
	fs := &fakeSum{}
	w := New(Config{
		Feed: ff, News: &fakeArticles{cache: map[string]core.NewsItem{}}, Sum: fs, Cache: &fakeCache{},
		MaxPerSym: 1, MaxPerDay: 5, Cooldown: time.Minute,
		ChaseDelays: []time.Duration{time.Millisecond, time.Millisecond},
	})

	w.runSym(context.Background(), "POP")

	// MaxPerSym=1: the ONE summary slot must go to the fresh article, proving
	// the chase result (newest first) replaced the stale-only initial set.
	if fs.n() != 1 || fs.calls[0] != "hot2" {
		t.Fatalf("the fresh headline should be summarized first, got %v", fs.calls)
	}
}

type staleThenFreshFeed struct {
	core.FeedClient

	mu      sync.Mutex
	calls   int
	freshAt int
	stale   core.NewsItem
	fresh   core.NewsItem
}

func (f *staleThenFreshFeed) BenzingaNewsFor(context.Context, string, int64) ([]core.NewsItem, map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls >= f.freshAt {
		return []core.NewsItem{f.fresh, f.stale}, nil, nil // newest first, like the API
	}
	return []core.NewsItem{f.stale}, nil, nil
}
