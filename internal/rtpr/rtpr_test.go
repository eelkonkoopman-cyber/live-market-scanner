package rtpr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeHealth records SourceOK/SourceFail calls in order; shared by this file
// and alerts_test.go (both package rtpr). Safe for concurrent use since the
// poller and the alerts session goroutine are both callers across the suite.
type fakeHealth struct {
	mu    sync.Mutex
	ok    []string
	fails []string
}

func (f *fakeHealth) SourceOK(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ok = append(f.ok, name)
}

func (f *fakeHealth) SourceFail(name, _ string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fails = append(f.fails, name)
}

func (f *fakeHealth) snapshot() (ok, fails []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ok...), append([]string(nil), f.fails...)
}

// fakeRefFrame builds one /feed/articles JSON page. article_url is always an
// ABSOLUTE URL on the real API (confirmed live) and fetchArticle follows it
// exactly as given, so the fixture must match — a relative path here would
// pass against a bug that silently no-ops on every real article fetch.
func fakeRefFrame(w http.ResponseWriter, base string, ids []string, nextToken string) {
	type ref struct {
		Ticker string `json:"ticker"`
		PubAt  string `json:"article_published_at"`
		URL    string `json:"article_url"`
	}
	refs := make([]ref, len(ids))
	for i, id := range ids {
		refs[i] = ref{Ticker: "TST", PubAt: "2026-07-28T12:00:00Z", URL: base + "/a/" + id}
	}
	resp := struct {
		Count     int    `json:"count"`
		NextToken string `json:"next_token"`
		Articles  []ref  `json:"articles"`
	}{Count: len(refs), NextToken: nextToken, Articles: refs}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// fakeArticlePage renders a minimal but structurally real permalink page for
// article id.
func fakeArticlePage(w http.ResponseWriter, id string) {
	fmt.Fprintf(w, `<html><body><main><article>
	  <div class="meta"><span class="meta-source">Test Wire</span><time datetime="2026-07-28T12:00:00Z"></time></div>
	  <h1 class="title">Headline %s</h1>
	  <div class="tickers"><span class="ticker">TST</span><span class="exchange">NASDAQ</span></div>
	  <div class="article-body">body of %s</div>
	</article></main></body></html>`, id, id)
}

func TestBaselineSeedsWithoutEmitting(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/feed/articles":
			fakeRefFrame(w, srv.URL, []string{"a1", "a2", "a3"}, "")
		default:
			fakeArticlePage(w, r.URL.Path)
		}
	}))
	defer srv.Close()

	p := New(Config{Token: "t", RESTBase: srv.URL, PollInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p.poll(ctx) // first poll: baseline seed only
	select {
	case b := <-p.Out():
		t.Fatalf("baseline poll must not emit a batch, got %+v", b)
	default:
	}
	if p.baseline {
		t.Fatal("baseline flag should clear after the first successful poll")
	}
	for _, id := range []string{"a1", "a2", "a3"} {
		if !p.seen[id] {
			t.Fatalf("baseline must mark %s as seen", id)
		}
	}
}

func TestSecondPollEmitsOnlyNewArticles(t *testing.T) {
	// Baseline's page has only a1-a3; a later poll's page also carries the
	// new b1 ahead of them (newest-first) -- only b1 should be emitted.
	var call int32
	var srv2 *httptest.Server
	srv2 = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/feed/articles" {
			fakeArticlePage(w, r.URL.Path)
			return
		}
		if atomic.AddInt32(&call, 1) == 1 {
			fakeRefFrame(w, srv2.URL, []string{"a1", "a2", "a3"}, "")
		} else {
			fakeRefFrame(w, srv2.URL, []string{"b1", "a1", "a2", "a3"}, "")
		}
	}))
	defer srv2.Close()

	p2 := New(Config{Token: "t", RESTBase: srv2.URL, PollInterval: time.Hour})
	p2.poll(context.Background()) // baseline: seeds a1,a2,a3
	select {
	case b := <-p2.Out():
		t.Fatalf("baseline poll must not emit, got %+v", b)
	default:
	}

	p2.poll(context.Background()) // incremental: b1 is new
	select {
	case b := <-p2.Out():
		if len(b.Items) != 1 || b.Items[0].ID != "rtprb1" {
			t.Fatalf("want exactly one new item rtprb1, got %+v", b.Items)
		}
		if b.Items[0].Headline != "Headline /a/b1" {
			t.Fatalf("headline not parsed from article page: %q", b.Items[0].Headline)
		}
		if b.Bodies["rtprb1"] == "" {
			t.Fatal("article body must be captured for the summarizer")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no batch emitted for the new article")
	}
}

func TestPaginationWalksBackAcrossMultiplePages(t *testing.T) {
	// Baseline sees only "a1" (page 1, no next_token -> stops there).
	// The next poll's page 1 is entirely new (b1,b2), forcing a walk to page 2
	// where it finds a1 already-seen and stops -- proving multi-page pull.
	var call int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/feed/articles" {
			fakeArticlePage(w, r.URL.Path)
			return
		}
		n := atomic.AddInt32(&call, 1)
		switch {
		case n == 1:
			fakeRefFrame(w, srv.URL, []string{"a1"}, "") // baseline
		case r.URL.Query().Get("next_token") == "":
			fakeRefFrame(w, srv.URL, []string{"b2", "b1"}, "page2") // page 1 of the follow-up poll
		default:
			fakeRefFrame(w, srv.URL, []string{"a1"}, "") // page 2: hits the seen boundary
		}
	}))
	defer srv.Close()

	p := New(Config{Token: "t", RESTBase: srv.URL, PollInterval: time.Hour})
	p.poll(context.Background()) // baseline
	<-time.After(50 * time.Millisecond)

	p.poll(context.Background())
	// Each article is now delivered as its own batch as soon as its fetch
	// completes (see TestArticlesStreamAsEachFetchCompletes), so the 2 new
	// items across the paginated walk arrive as 2 separate receives, still
	// oldest-first: b1 (older) then b2 (newer).
	var got []string
	for i := 0; i < 2; i++ {
		select {
		case b := <-p.Out():
			if len(b.Items) != 1 {
				t.Fatalf("want each delivery to carry exactly 1 item, got %d: %+v", len(b.Items), b.Items)
			}
			got = append(got, b.Items[0].ID)
		case <-time.After(2 * time.Second):
			t.Fatalf("only got %d of 2 expected deliveries: %v", i, got)
		}
	}
	if len(got) != 2 || got[0] != "rtprb1" || got[1] != "rtprb2" {
		t.Fatalf("want oldest-first order [rtprb1 rtprb2], got %v", got)
	}
}

// TestArticlesStreamAsEachFetchCompletes proves the head-of-line-blocking fix
// directly: an already-fetched article must reach Out() immediately, without
// waiting for a slower fetch elsewhere in the same poll() call to finish.
// Under the old code (one accumulated batch, sent only after the whole refs
// loop completes) this test hangs until its own timeout, since "first"'s
// delivery was gated on "second"'s fetch also finishing.
func TestArticlesStreamAsEachFetchCompletes(t *testing.T) {
	release := make(chan struct{})
	var call int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/feed/articles":
			if atomic.AddInt32(&call, 1) == 1 {
				fakeRefFrame(w, srv.URL, []string{"seed"}, "") // baseline
			} else {
				fakeRefFrame(w, srv.URL, []string{"second", "first"}, "") // API returns newest-first
			}
		case r.URL.Path == "/a/second":
			<-release // deliberately slow: blocks until the test releases it
			fakeArticlePage(w, r.URL.Path)
		default:
			fakeArticlePage(w, r.URL.Path)
		}
	}))
	defer srv.Close()

	p := New(Config{Token: "t", RESTBase: srv.URL, PollInterval: time.Hour})
	p.poll(context.Background()) // baseline, seeds "seed"

	done := make(chan struct{})
	go func() {
		p.poll(context.Background()) // processes "first" (fast) then "second" (blocked)
		close(done)
	}()

	select {
	case b := <-p.Out():
		if len(b.Items) != 1 || b.Items[0].ID != "rtprfirst" {
			t.Fatalf("want rtprfirst delivered without waiting on the slow fetch, got %+v", b.Items)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rtprfirst was never delivered -- it's being held hostage behind the slower fetch")
	}

	select {
	case <-done:
		t.Fatal("poll() returned before the blocked fetch was released -- test setup is wrong")
	default:
	}

	close(release)
	select {
	case b := <-p.Out():
		if len(b.Items) != 1 || b.Items[0].ID != "rtprsecond" {
			t.Fatalf("want rtprsecond delivered after being unblocked, got %+v", b.Items)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rtprsecond was never delivered after being unblocked")
	}
	<-done
}

// TestBudgetRefillsSmoothlyNotInOneLump proves the refill-cliff fix: once
// drained, a token must reappear after roughly ONE smoothed interval, not
// have to wait for the entire window (the old "dump max tokens once per
// window" design forced exactly that wait on every caller unlucky enough to
// drain the bucket right after a refill).
func TestBudgetRefillsSmoothlyNotInOneLump(t *testing.T) {
	b := newReqBudgetEvery(5, 100*time.Millisecond) // smoothed: 1 token per 20ms
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := b.wait(ctx); err != nil {
			t.Fatalf("drain initial token %d: %v", i, err)
		}
	}
	start := time.Now()
	if err := b.wait(ctx); err != nil {
		t.Fatalf("wait for smoothed refill: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 60*time.Millisecond {
		t.Fatalf("token took %v to reappear -- expected ~20ms (smoothed), not close to the full 100ms window", elapsed)
	}
}

// TestPriorityTickersFetchedFirst proves the burst-prioritization fix: a
// ticker flagged priority must be fetched (and thus delivered) ahead of a
// non-priority ticker discovered in the SAME poll, even when the default
// oldest-published-first order would otherwise put it second.
func TestPriorityTickersFetchedFirst(t *testing.T) {
	type wireRef struct {
		Ticker string `json:"ticker"`
		PubAt  string `json:"article_published_at"`
		URL    string `json:"article_url"`
	}
	var call int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/feed/articles" {
			fakeArticlePage(w, r.URL.Path)
			return
		}
		if atomic.AddInt32(&call, 1) == 1 {
			fakeRefFrame(w, srv.URL, []string{"seed"}, "")
			return
		}
		// API is newest-first: "cheap" published LATER (newer) so without
		// reordering it would naturally sort SECOND after collectFresh's
		// oldest-first reversal -- "expensive" (older) would fetch first.
		// Priority must flip that back to cheap-first.
		resp := struct {
			Count     int       `json:"count"`
			NextToken string    `json:"next_token"`
			Articles  []wireRef `json:"articles"`
		}{
			Count: 2,
			Articles: []wireRef{
				{Ticker: "CHP", PubAt: "2026-07-28T12:00:01Z", URL: srv.URL + "/a/cheap"},
				{Ticker: "EXP", PubAt: "2026-07-28T12:00:00Z", URL: srv.URL + "/a/expensive"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := New(Config{
		Token: "t", RESTBase: srv.URL, PollInterval: time.Hour,
		Priority: func(ticker string) bool { return ticker == "CHP" },
	})
	p.poll(context.Background()) // baseline, seeds "seed"

	p.poll(context.Background())
	var order []string
	for i := 0; i < 2; i++ {
		select {
		case b := <-p.Out():
			order = append(order, b.Items[0].ID)
		case <-time.After(2 * time.Second):
			t.Fatalf("only got %d of 2 expected deliveries: %v", i, order)
		}
	}
	if len(order) != 2 || order[0] != "rtprcheap" || order[1] != "rtprexpensive" {
		t.Fatalf("want the priority ticker's article delivered first, got %v", order)
	}
}

func TestRateLimitTriggersBackoff(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	p := New(Config{Token: "t", RESTBase: srv.URL, PollInterval: time.Hour})
	p.poll(context.Background()) // baseline attempt -> 429
	if p.rlUntil.IsZero() {
		t.Fatal("a 429 during baseline seeding must arm the backoff")
	}
	if !p.baseline {
		t.Fatal("a failed baseline seed must retry as baseline, not silently proceed")
	}

	n := atomic.LoadInt32(&calls)
	p.poll(context.Background()) // still within rlUntil -> must skip, no new HTTP call
	if atomic.LoadInt32(&calls) != n {
		t.Fatal("poll during the backoff window must not make another request")
	}
}

// TestHealthOKOnSuccessfulPolls proves both a successful baseline seed and a
// successful post-baseline poll report the ref feed healthy, so the status
// bar reflects "still working" on every good tick, not just the first.
func TestHealthOKOnSuccessfulPolls(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/feed/articles":
			fakeRefFrame(w, srv.URL, []string{"a1"}, "")
		default:
			fakeArticlePage(w, r.URL.Path)
		}
	}))
	defer srv.Close()

	h := &fakeHealth{}
	p := New(Config{Token: "t", RESTBase: srv.URL, PollInterval: time.Hour, Health: h})

	p.poll(context.Background()) // baseline seed
	if ok, _ := h.snapshot(); len(ok) != 1 || ok[0] != "rtpr" {
		t.Fatalf("successful baseline seed must report rtpr OK once, got %v", ok)
	}

	p.poll(context.Background()) // post-baseline poll, no new articles this time
	if ok, _ := h.snapshot(); len(ok) != 2 || ok[1] != "rtpr" {
		t.Fatalf("successful post-baseline poll must report rtpr OK again, got %v", ok)
	}
}

// TestHealthFailOnBaselineSeedError proves the "rtpr feed baseline seed
// failed; will retry" path reports the source failing with the error text.
func TestHealthFailOnBaselineSeedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // not 429: the hard-failure path, not backoff
	}))
	defer srv.Close()

	h := &fakeHealth{}
	p := New(Config{Token: "t", RESTBase: srv.URL, PollInterval: time.Hour, Health: h})
	p.poll(context.Background())

	_, fails := h.snapshot()
	if len(fails) != 1 || fails[0] != "rtpr" {
		t.Fatalf("a failed baseline seed must report rtpr failing once, got %v", fails)
	}
	if !p.baseline {
		t.Fatal("a failed baseline seed must retry as baseline")
	}
}

// TestHealthFailOnPerPollError proves the "rtpr feed poll failed" path
// (post-baseline) also reports the source failing.
func TestHealthFailOnPerPollError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	h := &fakeHealth{}
	p := New(Config{Token: "t", RESTBase: srv.URL, PollInterval: time.Hour, Health: h})
	p.baseline = false // skip straight to the per-poll (collectFresh) path

	p.poll(context.Background())

	_, fails := h.snapshot()
	if len(fails) != 1 || fails[0] != "rtpr" {
		t.Fatalf("a failed per-poll fetch must report rtpr failing once, got %v", fails)
	}
}
