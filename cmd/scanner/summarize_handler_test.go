package main

import (
	"context"
	"errors"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/summarize"
)

// fakeSummaryCache is an in-memory core.SummaryRepo for testing
// newsSummarizeHandler's short-circuit logic without a real database.
type fakeSummaryCache struct {
	sums   map[string]string
	models map[string]string
}

func newFakeSummaryCache() *fakeSummaryCache {
	return &fakeSummaryCache{sums: map[string]string{}, models: map[string]string{}}
}

func (c *fakeSummaryCache) GetSummary(ctx context.Context, id string) (string, string, bool, error) {
	s, ok := c.sums[id]
	if !ok {
		return "", "", false, nil
	}
	return s, c.models[id], true, nil
}

func (c *fakeSummaryCache) SaveSummary(ctx context.Context, id, sym, model, summary string) error {
	c.sums[id] = summary
	c.models[id] = model
	return nil
}

// fakeClaude records whether it was called, so tests can assert the
// short-circuit either did or did not reach Claude.
type fakeClaude struct {
	calls int
	out   string
	err   error
}

func (f *fakeClaude) Summarize(ctx context.Context, a summarize.Article) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	return f.out, nil
}

func (f *fakeClaude) Model() string { return "claude-opus-4-8" }

func fakeLiveGet(item core.NewsItem, body string, ok bool) func(id string) (core.NewsItem, string, bool) {
	return func(id string) (core.NewsItem, string, bool) { return item, body, ok }
}

func fakeArchiveGet(item core.NewsItem, body string, ok bool, err error) func(ctx context.Context, id string) (core.NewsItem, string, bool, error) {
	return func(ctx context.Context, id string) (core.NewsItem, string, bool, error) { return item, body, ok, err }
}

// TestNewsSummarizeHandlerLocalCacheHitDoesNotShortCircuit is the
// fail-then-fix-then-pass test for the "silent Claude short-circuit" wiring
// bug: a cache entry whose model is the "local" sentinel must NOT be served
// as-is by the manual "✨ Summarize"/"✨ Ask Claude" handler — it must fall
// through, call Claude, and overwrite the cache entry with the real result.
//
// To confirm this test actually exercises the fix (not a tautology), it was
// run once against the pre-fix behavior — a plain `if ok { return cached }`
// short-circuit with no model check — where it failed with "Claude was not
// called for a local-model cache hit", exactly the silent-short-circuit bug
// this build fixes. Restoring the guard below makes it pass again.
func TestNewsSummarizeHandlerLocalCacheHitDoesNotShortCircuit(t *testing.T) {
	cache := newFakeSummaryCache()
	if err := cache.SaveSummary(context.Background(), "art1", "ABCD", summarize.LocalModel, "Local read: something happened."); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	item := core.NewsItem{ID: "art1", Tickers: []string{"ABCD"}, Headline: "Something happened"}
	claude := &fakeClaude{out: "Claude's real take."}
	handler := newsSummarizeHandler(cache, fakeLiveGet(item, "body text", true), fakeArchiveGet(core.NewsItem{}, "", false, nil), claude)

	text, model, err := handler(context.Background(), "art1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claude.calls != 1 {
		t.Fatalf("Claude was not called for a local-model cache hit (calls=%d) — silent short-circuit regressed", claude.calls)
	}
	if text != "Claude's real take." || model != "claude-opus-4-8" {
		t.Fatalf("handler did not return Claude's fresh result: text=%q model=%q", text, model)
	}
	// The cache entry must be overwritten with the real result, not left as
	// the stale local read.
	cachedSum, cachedModel, ok, err := cache.GetSummary(context.Background(), "art1")
	if err != nil || !ok {
		t.Fatalf("expected the cache entry to still exist: ok=%v err=%v", ok, err)
	}
	if cachedSum != "Claude's real take." || cachedModel != "claude-opus-4-8" {
		t.Fatalf("cache entry was not overwritten with the real result: sum=%q model=%q", cachedSum, cachedModel)
	}
}

// TestNewsSummarizeHandlerRealModelCacheHitShortCircuits is the mirror case:
// a cache entry whose model is already a real Claude model must be served
// as-is, and Claude must NOT be called again.
func TestNewsSummarizeHandlerRealModelCacheHitShortCircuits(t *testing.T) {
	cache := newFakeSummaryCache()
	if err := cache.SaveSummary(context.Background(), "art2", "WXYZ", "claude-opus-4-8", "Claude's earlier take."); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	claude := &fakeClaude{out: "should never be reached"}
	handler := newsSummarizeHandler(cache, fakeLiveGet(core.NewsItem{}, "", false), fakeArchiveGet(core.NewsItem{}, "", false, nil), claude)

	text, model, err := handler(context.Background(), "art2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claude.calls != 0 {
		t.Fatalf("Claude was called again for an already-Claude cache hit (calls=%d)", claude.calls)
	}
	if text != "Claude's earlier take." || model != "claude-opus-4-8" {
		t.Fatalf("handler did not return the cached Claude result as-is: text=%q model=%q", text, model)
	}
}

// TestNewsSummarizeHandlerMissingArticleErrors covers the plain "article
// evicted from both the live cache and the archive" failure path, unrelated
// to the short-circuit fix but exercised here since it shares the handler.
func TestNewsSummarizeHandlerMissingArticleErrors(t *testing.T) {
	cache := newFakeSummaryCache()
	claude := &fakeClaude{out: "unused"}
	handler := newsSummarizeHandler(cache, fakeLiveGet(core.NewsItem{}, "", false), fakeArchiveGet(core.NewsItem{}, "", false, errors.New("db down")), claude)

	if _, _, err := handler(context.Background(), "missing"); err == nil {
		t.Fatal("expected an error for an article missing from both caches")
	}
	if claude.calls != 0 {
		t.Fatalf("Claude should not be called when the article can't be found (calls=%d)", claude.calls)
	}
}
