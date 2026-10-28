package news

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

type fakeFeed struct {
	items    []core.NewsItem
	bzItems  []core.NewsItem
	bzBodies map[string]string
	bzErr    error
	bzCalls  int
	refCalls int
	refErr   error
}

func (f *fakeFeed) Snapshot(context.Context) ([]core.Seed, error)         { return nil, nil }
func (f *fakeFeed) Stream(context.Context, chan<- core.Agg, func()) error { return nil }
func (f *fakeFeed) NewsSince(context.Context, int64) ([]core.NewsItem, error) {
	f.refCalls++
	return f.items, f.refErr
}
func (f *fakeFeed) Reference(context.Context) ([]core.Ticker, error) { return nil, nil }
func (f *fakeFeed) BenzingaNews(context.Context, int64) ([]core.NewsItem, map[string]string, error) {
	f.bzCalls++
	return f.bzItems, f.bzBodies, f.bzErr
}
func (f *fakeFeed) BenzingaNewsFor(context.Context, string, int64) ([]core.NewsItem, map[string]string, error) {
	return nil, nil, nil
}
func (f *fakeFeed) Fundamentals(context.Context, string) (core.Fundamentals, error) {
	return core.Fundamentals{}, nil
}

func TestPollDedupesByID(t *testing.T) {
	ff := &fakeFeed{items: []core.NewsItem{{ID: "a1", Headline: "x", TS: 1000}}}
	p := New(ff)

	p.poll(context.Background())
	select {
	case b := <-p.out:
		if len(b) != 1 || b[0].ID != "a1" {
			t.Fatalf("first batch mismatch: %+v", b)
		}
	default:
		t.Fatal("expected first batch")
	}

	// Same item returned again -> deduped by ID -> no second batch.
	p.poll(context.Background())
	select {
	case <-p.out:
		t.Fatal("duplicate article should not be re-emitted")
	default:
	}
}

func TestPollDropDoesNotAdvanceCursor(t *testing.T) {
	ff := &fakeFeed{items: []core.NewsItem{{ID: "a1", TS: 1000}}}
	p := New(ff)

	// Fill the out channel so the send fails.
	for i := 0; i < cap(p.out); i++ {
		p.out <- nil
	}
	p.poll(context.Background()) // dropped
	if p.lastRef != 0 || p.seen["a1"] {
		t.Fatalf("drop must not advance cursor/seen: last=%d seen=%v", p.lastRef, p.seen["a1"])
	}

	// Drain, then the same item is re-fetched and delivered.
	for i := 0; i < cap(p.out); i++ {
		<-p.out
	}
	p.poll(context.Background())
	select {
	case b := <-p.out:
		if len(b) != 1 || b[0].ID != "a1" {
			t.Fatalf("re-emit after drain mismatch: %+v", b)
		}
	default:
		t.Fatal("expected re-emit after drain")
	}
	if p.lastRef != 1000 || !p.seen["a1"] {
		t.Fatalf("successful send must advance cursor/seen: last=%d seen=%v", p.lastRef, p.seen["a1"])
	}
}

// TestReferenceThrottledBenzingaEveryTick proves the two sources poll on
// independent cadences: Benzinga (real-time) every tick, reference (hourly
// recency) only when its own interval elapses. This is what lets the Benzinga
// interval drop to ~2s without multiplying requests to the slow endpoint.
func TestReferenceThrottledBenzingaEveryTick(t *testing.T) {
	ff := &fakeFeed{}
	p := New(ff, WithIntervals(time.Second, time.Hour))

	for i := 0; i < 5; i++ {
		p.poll(context.Background())
	}
	if ff.bzCalls != 5 {
		t.Fatalf("benzinga should poll every tick: want 5 got %d", ff.bzCalls)
	}
	if ff.refCalls != 1 {
		t.Fatalf("reference should poll once per refInterval: want 1 got %d", ff.refCalls)
	}
}

// TestRateLimitBacksOff proves a 429 pauses polling instead of hammering, and
// that a clean poll clears the backoff.
func TestRateLimitBacksOff(t *testing.T) {
	ff := &fakeFeed{bzErr: fmt.Errorf("%w: HTTP 429", core.ErrRateLimited)}
	p := New(ff, WithIntervals(time.Millisecond, time.Hour))

	p.poll(context.Background())
	if p.rlBackoff != rlBackoffStart {
		t.Fatalf("first 429 should arm backoff, got %v", p.rlBackoff)
	}
	callsAfterFirst := ff.bzCalls

	// While backing off, further ticks must not hit the API at all.
	p.poll(context.Background())
	if ff.bzCalls != callsAfterFirst {
		t.Fatalf("must not call the API while backing off: %d -> %d", callsAfterFirst, ff.bzCalls)
	}

	// Backoff doubles on a repeat 429.
	p.rlUntil = time.Time{}
	p.poll(context.Background())
	if p.rlBackoff != 2*rlBackoffStart {
		t.Fatalf("repeat 429 should double backoff, got %v", p.rlBackoff)
	}

	// A clean poll clears it.
	ff.bzErr = nil
	p.rlUntil = time.Time{}
	p.poll(context.Background())
	if p.rlBackoff != 0 || !p.rlUntil.IsZero() {
		t.Fatalf("successful poll must clear backoff, got %v / %v", p.rlBackoff, p.rlUntil)
	}
}

// TestWithIntervalsFloors guards the misconfiguration path: an absurdly small
// env value must not turn the poller into a rate-limit magnet.
func TestWithIntervalsFloors(t *testing.T) {
	p := New(&fakeFeed{}, WithIntervals(time.Millisecond, time.Millisecond))
	if p.bzInterval != minInterval || p.refInterval != minInterval {
		t.Fatalf("intervals should floor at %v, got %v / %v", minInterval, p.bzInterval, p.refInterval)
	}
	// Zero means "keep the default", not "poll continuously".
	d := New(&fakeFeed{}, WithIntervals(0, 0))
	if d.bzInterval != defaultBzInterval || d.refInterval != defaultRefInterval {
		t.Fatalf("zero should keep defaults, got %v / %v", d.bzInterval, d.refInterval)
	}
}

// lagFeed models Benzinga's REST index honestly: articles become QUERYABLE
// later than their publish timestamp, and the since-filter is applied to the
// publish time, exactly like the real endpoint.
type lagFeed struct {
	fakeFeed
	// articles queryable as of call N (1-based): indexedAt[i] <= N
	arts      []core.NewsItem
	indexedAt []int
	calls     int
}

func (f *lagFeed) BenzingaNews(_ context.Context, sinceMs int64) ([]core.NewsItem, map[string]string, error) {
	f.calls++
	var out []core.NewsItem
	for i, a := range f.arts {
		if f.indexedAt[i] <= f.calls && a.TS >= sinceMs {
			out = append(out, a)
		}
	}
	return out, nil, nil
}

// TestLateIndexedArticleIsNotSkipped is the "news never showed up" regression:
// article A (published 10:00:00) hits the REST index AFTER article B
// (published 10:00:20) has already advanced the cursor past it. With an exact
// cursor query A is unfetchable forever; the overlap window must recover it.
func TestLateIndexedArticleIsNotSkipped(t *testing.T) {
	base := time.Now().UnixMilli()
	ff := &lagFeed{
		arts: []core.NewsItem{
			{ID: "bzB", Headline: "B fast", TS: base + 20_000},
			{ID: "bzA", Headline: "A late-indexed", TS: base}, // older publish, later index
		},
		indexedAt: []int{1, 2},
	}
	p := New(ff)
	p.lastBz = base - 60_000

	p.poll(context.Background())
	b := <-p.out
	if len(b) != 1 || b[0].ID != "bzB" {
		t.Fatalf("poll 1 should emit only B, got %+v", b)
	}
	if p.lastBz != base+20_000 {
		t.Fatalf("cursor should sit past A's publish time, got %d", p.lastBz)
	}

	// A is now indexed. Its publish TS is behind the cursor: only the overlap
	// window makes it visible to this query.
	p.poll(context.Background())
	select {
	case b := <-p.out:
		if len(b) != 1 || b[0].ID != "bzA" {
			t.Fatalf("poll 2 should emit the late-indexed A, got %+v", b)
		}
	default:
		t.Fatal("late-indexed article was permanently skipped by the cursor")
	}

	// And the overlap must not re-emit either one on subsequent polls.
	p.poll(context.Background())
	select {
	case b := <-p.out:
		t.Fatalf("overlap window re-emitted deduped articles: %+v", b)
	default:
	}
}

// The sink is the persistence hook: it must fire once per FRESH article with
// the right source tag, and stay silent for duplicates — the archive's
// received_ms/via columns are a latency measurement, so double-fires would
// corrupt the data the whole feature exists to collect.
func TestSinkReceivesFreshOnlyWithVia(t *testing.T) {
	p := New(&fakeFeed{})
	type got struct {
		n   int
		via string
	}
	var calls []got
	p.SetSink(func(items []core.NewsItem, _ map[string]string, via string) {
		calls = append(calls, got{len(items), via})
	})

	items := []core.NewsItem{{ID: "bzS1", TS: 1000, URL: "https://x.test/s1"}}
	if fresh := p.AddExternal(items, nil, "push"); len(fresh) != 1 {
		t.Fatalf("first add: want 1 fresh, got %d", len(fresh))
	}
	// Same article again (poll overlap / chaser double-cover): no sink call.
	if fresh := p.AddExternal(items, nil, "chase"); len(fresh) != 0 {
		t.Fatalf("dup add: want 0 fresh, got %d", len(fresh))
	}
	if len(calls) != 1 || calls[0].n != 1 || calls[0].via != "push" {
		t.Fatalf("sink calls: want exactly one {1, push}, got %+v", calls)
	}
}

// TestPollAndAddExternalConcurrently is the crash-race regression: dupe() on
// the poll goroutine reads seen/seenURL while AddExternal writes them from
// autosum/bzstream goroutines. Run under -race (the suite always is in CI);
// before dupe() took the read lock this was a fatal concurrent map access.
func TestPollAndAddExternalConcurrently(t *testing.T) {
	base := time.Now().UnixMilli()
	ff := &fakeFeed{}
	p := New(ff)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			p.AddExternal([]core.NewsItem{
				{ID: fmt.Sprintf("ext%d", i), TS: base + int64(i), URL: fmt.Sprintf("https://x.test/a/%d", i)},
			}, nil, "chase")
		}
	}()
	for i := 0; i < 200; i++ {
		ff.bzItems = []core.NewsItem{
			{ID: fmt.Sprintf("bz%d", i), TS: base + int64(i), URL: fmt.Sprintf("https://x.test/b/%d", i)},
		}
		p.poll(context.Background())
		// Keep the out channel from filling so commit keeps running (and with
		// it the seen-map writes on the poll side).
		select {
		case <-p.out:
		default:
		}
	}
	<-done
}

func TestBenzingaBodyCachedAndGettable(t *testing.T) {
	ff := &fakeFeed{
		bzItems:  []core.NewsItem{{ID: "bz1", Headline: "Offering", TS: 2000, HasBody: true, URL: "https://benzinga.com/a/1"}},
		bzBodies: map[string]string{"bz1": "<p>full body</p>"},
	}
	p := New(ff)
	p.poll(context.Background())
	b := <-p.out
	if len(b) != 1 || b[0].ID != "bz1" {
		t.Fatalf("batch: %+v", b)
	}
	item, body, ok := p.Get("bz1")
	if !ok || body != "<p>full body</p>" || item.Headline != "Offering" {
		t.Fatalf("Get: ok=%v body=%q item=%+v", ok, body, item)
	}
	if p.lastBz != 2000 {
		t.Fatalf("benzinga cursor: %d", p.lastBz)
	}
}

func TestEntitlementFailureParksBenzinga(t *testing.T) {
	ff := &fakeFeed{bzErr: fmt.Errorf("wrapped: %w", core.ErrNotEntitled)}
	p := New(ff)
	p.poll(context.Background())
	if !p.bzDown {
		t.Fatal("entitlement error should park the benzinga source")
	}
	calls := ff.bzCalls
	p.poll(context.Background()) // still parked, retry-at in the future
	if ff.bzCalls != calls {
		t.Fatal("parked source should not be re-polled before retryAt")
	}
}

func TestCrossSourceURLDedupe(t *testing.T) {
	ff := &fakeFeed{
		bzItems: []core.NewsItem{{ID: "bz9", Headline: "Same story", TS: 3000, URL: "https://www.benzinga.com/news/9?utm=x"}},
		items:   []core.NewsItem{{ID: "ref9", Headline: "Same story", TS: 3000, URL: "http://benzinga.com/news/9"}},
	}
	p := New(ff)
	p.poll(context.Background())
	b := <-p.out
	if len(b) != 1 {
		t.Fatalf("URL dedupe failed, got %d items: %+v", len(b), b)
	}
}
