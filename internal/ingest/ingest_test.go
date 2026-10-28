package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/engine"
	"github.com/eelkonkoopman/marketscanner/internal/universe"
)

type fakeFeed struct {
	seeds []core.Seed
	aggs  []core.Agg
}

func (f *fakeFeed) Snapshot(context.Context) ([]core.Seed, error)             { return f.seeds, nil }
func (f *fakeFeed) NewsSince(context.Context, int64) ([]core.NewsItem, error) { return nil, nil }
func (f *fakeFeed) Reference(context.Context) ([]core.Ticker, error)          { return nil, nil }
func (f *fakeFeed) BenzingaNews(context.Context, int64) ([]core.NewsItem, map[string]string, error) {
	return nil, nil, nil
}
func (f *fakeFeed) BenzingaNewsFor(context.Context, string, int64) ([]core.NewsItem, map[string]string, error) {
	return nil, nil, nil
}
func (f *fakeFeed) Fundamentals(context.Context, string) (core.Fundamentals, error) {
	return core.Fundamentals{}, nil
}
func (f *fakeFeed) Stream(ctx context.Context, out chan<- core.Agg, onReset func()) error {
	if onReset != nil {
		onReset()
	}
	for _, a := range f.aggs {
		select {
		case out <- a:
		case <-ctx.Done():
			return nil
		}
	}
	<-ctx.Done()
	return nil
}

func TestIngestSeedsAndFiltersUniverse(t *testing.T) {
	eng := engine.New(engine.Options{TickHz: 20})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	uni := universe.NewAll()
	uni.Replace([]string{"AAA"}, nil) // only AAA is in the universe

	ff := &fakeFeed{
		seeds: []core.Seed{{Agg: core.Agg{Sym: "AAA", Close: 9, High: 9, Low: 9, Vol: 1000, EndMs: 1_700_000_000_000}, PrevClose: 8}},
		aggs: []core.Agg{
			{Sym: "AAA", Close: 10, High: 10, Low: 10, Vol: 50, EndMs: 1_700_000_001_000},
			{Sym: "ZZZ", Close: 5, High: 5, Low: 5, Vol: 50, EndMs: 1_700_000_001_000}, // not in universe
		},
	}
	ing := New(ff, eng, uni)
	go func() { _ = ing.Run(ctx) }()

	deadline := time.After(3 * time.Second)
	for {
		select {
		case snap := <-eng.Snapshots():
			// Universe filtering must hold in EVERY snapshot, not just the one
			// we finally assert on.
			if _, bad := snap.Index["ZZZ"]; bad {
				t.Fatal("ZZZ is outside the universe and must be filtered out")
			}
			i, ok := snap.Index["AAA"]
			if !ok {
				continue
			}
			// The seed (close 9) is applied before the live agg (close 10), so an
			// early snapshot legitimately shows the seed price. Asserting on the
			// first snapshot that merely contains AAA made this flaky under load;
			// wait for the agg to actually land instead.
			if snap.Rows[i].Last != 10 {
				continue
			}
			return
		case <-deadline:
			t.Fatal("no snapshot with AAA at the live agg price (10) within 3s")
		}
	}
}

// TestSetChartSymsTeesMultipleSymbolsIndependently proves two windows'
// selected symbols both reach ChartAggs() concurrently, while a third,
// untracked symbol never does.
func TestSetChartSymsTeesMultipleSymbolsIndependently(t *testing.T) {
	eng := engine.New(engine.Options{TickHz: 20})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	uni := universe.NewAll()
	ing := New(&fakeFeed{}, eng, uni)
	ing.SetChartSyms([]string{"AAA", "BBB"})

	in := make(chan core.Agg, 4)
	go ing.pump(ctx, in)

	in <- core.Agg{Sym: "AAA", Close: 10, High: 10, Low: 10, Vol: 1, EndMs: 1_700_000_001_000}
	in <- core.Agg{Sym: "BBB", Close: 20, High: 20, Low: 20, Vol: 1, EndMs: 1_700_000_001_000}
	in <- core.Agg{Sym: "CCC", Close: 30, High: 30, Low: 30, Vol: 1, EndMs: 1_700_000_001_000} // never added to the chart set

	seen := map[string]bool{}
	deadline := time.After(2 * time.Second)
	for len(seen) < 2 {
		select {
		case a := <-ing.ChartAggs():
			if a.Sym == "CCC" {
				t.Fatal("CCC was never added to the chart set and must not be teed")
			}
			seen[a.Sym] = true
		case <-deadline:
			t.Fatalf("only saw %v within 2s, want both AAA and BBB", seen)
		}
	}
	// Confirm CCC really never arrives (not just a timing fluke above).
	select {
	case extra := <-ing.ChartAggs():
		t.Fatalf("CCC must never be teed, got %+v", extra)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestSetChartSymsShrinkStopsOneSymbolWithoutAffectingOther proves dropping
// one symbol from the watched set stops its tee immediately while a
// still-watched symbol keeps flowing.
func TestSetChartSymsShrinkStopsOneSymbolWithoutAffectingOther(t *testing.T) {
	eng := engine.New(engine.Options{TickHz: 20})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	uni := universe.NewAll()
	ing := New(&fakeFeed{}, eng, uni)
	ing.SetChartSyms([]string{"AAA", "BBB"})

	in := make(chan core.Agg, 4)
	go ing.pump(ctx, in)

	recv := func() core.Agg {
		select {
		case a := <-ing.ChartAggs():
			return a
		case <-time.After(2 * time.Second):
			t.Fatal("expected a chart agg, got none")
			return core.Agg{}
		}
	}

	in <- core.Agg{Sym: "AAA", Close: 10, High: 10, Low: 10, Vol: 1, EndMs: 1_700_000_001_000}
	in <- core.Agg{Sym: "BBB", Close: 20, High: 20, Low: 20, Vol: 1, EndMs: 1_700_000_001_000}
	got := map[string]bool{}
	got[recv().Sym] = true
	got[recv().Sym] = true
	if !got["AAA"] || !got["BBB"] {
		t.Fatalf("want both teed before shrinking, got %v", got)
	}

	ing.SetChartSyms([]string{"BBB"})                                                          // drop AAA
	in <- core.Agg{Sym: "AAA", Close: 11, High: 11, Low: 11, Vol: 1, EndMs: 1_700_000_002_000} // must NOT be teed anymore
	in <- core.Agg{Sym: "BBB", Close: 21, High: 21, Low: 21, Vol: 1, EndMs: 1_700_000_002_000} // must still be teed

	a := recv()
	if a.Sym != "BBB" || a.Close != 21 {
		t.Fatalf("want only BBB's next agg teed, got %+v", a)
	}
	select {
	case extra := <-ing.ChartAggs():
		t.Fatalf("AAA must not be teed after being dropped from the chart set, got %+v", extra)
	case <-time.After(200 * time.Millisecond):
	}
}
