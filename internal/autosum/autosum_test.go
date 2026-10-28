package autosum

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/summarize"
)

type fakeFeed struct {
	core.FeedClient // embed nil: only BenzingaNewsFor is called
	items           []core.NewsItem
	bodies          map[string]string
	err             error
	calls           int
}

func (f *fakeFeed) BenzingaNewsFor(context.Context, string, int64) ([]core.NewsItem, map[string]string, error) {
	f.calls++
	return f.items, f.bodies, f.err
}

type fakeArticles struct {
	mu    sync.Mutex
	cache map[string]core.NewsItem
	local []core.NewsItem
}

func (a *fakeArticles) Get(id string) (core.NewsItem, string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	it, ok := a.cache[id]
	return it, "", ok
}
func (a *fakeArticles) AddExternal(items []core.NewsItem, _ map[string]string, _ string) []core.NewsItem {
	a.mu.Lock()
	defer a.mu.Unlock()
	var fresh []core.NewsItem
	for _, it := range items {
		if _, ok := a.cache[it.ID]; !ok {
			a.cache[it.ID] = it
			fresh = append(fresh, it)
		}
	}
	return fresh
}
func (a *fakeArticles) ItemsFor(string, int64) []core.NewsItem { return a.local }

type fakeSum struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (s *fakeSum) Summarize(_ context.Context, a summarize.Article) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, a.ID)
	if s.err != nil {
		return "", s.err
	}
	return "SUMMARY:" + a.ID, nil
}
func (s *fakeSum) Model() string { return "test-model" }
func (s *fakeSum) n() int        { s.mu.Lock(); defer s.mu.Unlock(); return len(s.calls) }

type fakeCache struct {
	mu    sync.Mutex
	saved map[string]string
	model map[string]string
}

func (c *fakeCache) GetSummary(_ context.Context, id string) (string, string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.saved[id]
	return s, c.model[id], ok, nil
}
func (c *fakeCache) SaveSummary(_ context.Context, id, _, model, sum string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.saved == nil {
		c.saved = map[string]string{}
		c.model = map[string]string{}
	}
	c.saved[id] = sum
	c.model[id] = model
	return nil
}

func newWorker(f *fakeFeed, a *fakeArticles, s *fakeSum, c *fakeCache) *Worker {
	w := New(Config{
		Feed: f, News: a, Sum: s, Cache: c,
		MaxPerSym: 2, MaxPerDay: 5, Cooldown: time.Minute,
	})
	// These tests use ancient article timestamps, which would all trigger the
	// news chaser and its real-time delays. The chaser has its own tests.
	w.chaseOn = false
	return w
}

func drainSums(w *Worker) []core.NewsSummaryMsg {
	var out []core.NewsSummaryMsg
	for {
		select {
		case m := <-w.sums:
			out = append(out, m)
		default:
			return out
		}
	}
}

func TestRunSymSummarizesNewestWithinBudget(t *testing.T) {
	ff := &fakeFeed{
		items: []core.NewsItem{ // newest first, 3 articles but MaxPerSym=2
			{ID: "n3", Tickers: []string{"ABCD"}, TS: 3000},
			{ID: "n2", Tickers: []string{"ABCD"}, TS: 2000},
			{ID: "n1", Tickers: []string{"ABCD"}, TS: 1000},
		},
		bodies: map[string]string{"n3": "b3"},
	}
	fa := &fakeArticles{cache: map[string]core.NewsItem{}}
	fs := &fakeSum{}
	fc := &fakeCache{}
	w := newWorker(ff, fa, fs, fc)

	if !w.shouldRun("ABCD") {
		t.Fatal("first entry should run")
	}
	w.runSym(context.Background(), "ABCD")

	if fs.n() != 2 {
		t.Fatalf("want 2 summaries (per-sym cap), got %d: %v", fs.n(), fs.calls)
	}
	sums := drainSums(w)
	if len(sums) != 2 || sums[0].ID != "n3" || sums[1].ID != "n2" {
		t.Fatalf("summaries out: %+v", sums)
	}
	if fc.saved["n3"] != "SUMMARY:n3" {
		t.Fatal("summary not cached")
	}
	// Articles were merged into the panel cache and re-broadcast.
	select {
	case items := <-w.news:
		if len(items) != 3 {
			t.Fatalf("news broadcast: %d items", len(items))
		}
	default:
		t.Fatal("expected news broadcast of fetched articles")
	}
}

func TestCooldownBlocksImmediateRerun(t *testing.T) {
	w := newWorker(&fakeFeed{}, &fakeArticles{}, &fakeSum{}, &fakeCache{})
	if !w.shouldRun("ABCD") {
		t.Fatal("first should run")
	}
	if w.shouldRun("ABCD") {
		t.Fatal("second within cooldown should not run")
	}
	if !w.shouldRun("WXYZ") {
		t.Fatal("different symbol unaffected")
	}
}

func TestCachedSummaryBroadcastWithoutRecompute(t *testing.T) {
	ff := &fakeFeed{items: []core.NewsItem{{ID: "n1", Tickers: []string{"A"}, TS: 1000}}}
	fa := &fakeArticles{cache: map[string]core.NewsItem{}}
	fs := &fakeSum{}
	fc := &fakeCache{saved: map[string]string{"n1": "OLD"}}
	w := newWorker(ff, fa, fs, fc)
	w.shouldRun("A")
	w.runSym(context.Background(), "A")
	if fs.n() != 0 {
		t.Fatalf("cache hit must not re-summarize, got %d calls", fs.n())
	}
	sums := drainSums(w)
	if len(sums) != 1 || sums[0].Summary != "OLD" {
		t.Fatalf("cached summary not broadcast: %+v", sums)
	}
}

func TestEntitlementFallbackUsesPollerCache(t *testing.T) {
	ff := &fakeFeed{err: fmt.Errorf("no: %w", core.ErrNotEntitled)}
	fa := &fakeArticles{local: []core.NewsItem{{ID: "r1", Tickers: []string{"A"}, TS: 1000}}}
	fs := &fakeSum{}
	w := newWorker(ff, fa, fs, &fakeCache{})
	w.shouldRun("A")
	w.runSym(context.Background(), "A")
	if fs.n() != 1 || fs.calls[0] != "r1" {
		t.Fatalf("fallback should summarize poller-cached item, calls=%v", fs.calls)
	}
}

func TestSummarizerErrorStopsRunAndRefundsBudget(t *testing.T) {
	ff := &fakeFeed{items: []core.NewsItem{
		{ID: "n2", TS: 2000}, {ID: "n1", TS: 1000},
	}}
	fs := &fakeSum{err: fmt.Errorf("not logged in")}
	w := newWorker(ff, &fakeArticles{cache: map[string]core.NewsItem{}}, fs, &fakeCache{})
	w.shouldRun("A")
	w.runSym(context.Background(), "A")
	if fs.n() != 1 {
		t.Fatalf("should stop after first failure, got %d", fs.n())
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dayDone != 0 || w.symDone["A"] != 0 {
		t.Fatalf("failed summary must refund budget: day=%d sym=%d", w.dayDone, w.symDone["A"])
	}
}
