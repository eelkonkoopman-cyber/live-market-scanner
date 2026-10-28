package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/engine"
	"github.com/eelkonkoopman/marketscanner/internal/universe"
)

// flagIngest builds an Ingest over a running engine and starts only its pump,
// returning the pump's input channel (the test plays the WebSocket).
func flagIngest(t *testing.T) (*Ingest, chan core.Agg) {
	t.Helper()
	eng := engine.New(engine.Options{TickHz: 20})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = eng.Run(ctx) }()
	ing := New(&fakeFeed{}, eng, universe.NewAll())
	in := make(chan core.Agg, 16)
	go ing.pump(ctx, in)
	return ing, in
}

func flagAgg(sym string, vol int64) core.Agg {
	return core.Agg{Sym: sym, Close: 10, High: 10, Low: 10, Vol: vol, EndMs: 1_700_000_001_000}
}

// waitApplied blocks until the pump has pushed n aggregates into the engine --
// i.e. has finished processing (and therefore teeing) everything sent so far.
func waitApplied(t *testing.T, ing *Ingest, n int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for ing.applied.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("pump applied %d of %d aggregates within 5s: it is blocked", ing.applied.Load(), n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func drainFlag(ing *Ingest) []core.Agg {
	var out []core.Agg
	for {
		select {
		case a := <-ing.FlagAggs():
			out = append(out, a)
		default:
			return out
		}
	}
}

// Only the symbols in the flag set are teed -- and the flag tap is independent
// of the chart tap: a symbol in one set never leaks into the other's channel,
// one in both reaches both, and every aggregate still reaches the engine.
func TestFlagTapTeesMembersOnlyAndIsIndependentOfChartTap(t *testing.T) {
	ing, in := flagIngest(t)
	ing.SetFlagSyms([]string{"AAA", "BBB"})
	ing.SetChartSyms([]string{"BBB", "CCC"})

	for _, a := range []core.Agg{flagAgg("AAA", 1), flagAgg("BBB", 2), flagAgg("CCC", 3), flagAgg("ZZZ", 4)} {
		in <- a
	}
	waitApplied(t, ing, 4) // all four reached the engine: the tees never hold anything back

	flag := drainFlag(ing)
	if len(flag) != 2 || flag[0].Sym != "AAA" || flag[1].Sym != "BBB" {
		t.Fatalf("flag tap want [AAA BBB] in arrival order, got %+v", flag)
	}
	var chart []string
	for len(ing.chartOut) > 0 {
		chart = append(chart, (<-ing.ChartAggs()).Sym)
	}
	if len(chart) != 2 || chart[0] != "BBB" || chart[1] != "CCC" {
		t.Fatalf("chart tap want [BBB CCC] (no AAA, no ZZZ), got %v", chart)
	}
	if n := ing.FlagDrops(); n != 0 {
		t.Fatalf("nothing overflowed, drops want 0 got %d", n)
	}
}

// SetFlagSyms is a wholesale replace: shrinking stops a symbol at once, growing
// starts one, nil/empty disables the tap, and an empty symbol string is ignored.
func TestSetFlagSymsReplacesGrowsShrinksAndDisables(t *testing.T) {
	ing, in := flagIngest(t)
	var sent int64
	push := func(a core.Agg) { in <- a; sent++ }

	ing.SetFlagSyms([]string{"AAA", "", "BBB"})
	push(flagAgg("AAA", 1))
	push(flagAgg("", 1)) // an empty symbol is never a member
	waitApplied(t, ing, sent)
	ing.SetFlagSyms([]string{"BBB"}) // drop AAA
	push(flagAgg("AAA", 2))
	push(flagAgg("BBB", 2))
	waitApplied(t, ing, sent)
	ing.SetFlagSyms([]string{"AAA", "BBB"}) // grow back
	push(flagAgg("AAA", 3))
	waitApplied(t, ing, sent)
	ing.SetFlagSyms(nil) // disable
	push(flagAgg("AAA", 4))
	push(flagAgg("BBB", 4))
	waitApplied(t, ing, sent)

	got := drainFlag(ing)
	type kv struct {
		sym string
		vol int64
	}
	want := []kv{{"AAA", 1}, {"BBB", 2}, {"AAA", 3}}
	if len(got) != len(want) {
		t.Fatalf("want %v teed, got %+v", want, got)
	}
	for i := range want {
		if got[i].Sym != want[i].sym || got[i].Vol != want[i].vol {
			t.Fatalf("tee %d: want %v got %s/%d (the set must replace, not accumulate; nil must disable)",
				i, want[i], got[i].Sym, got[i].Vol)
		}
	}
}

// The tee must never block the pump: with nobody consuming FlagAggs, flooding
// the tap past its buffer drops the overflow (counted, exactly) while the pump
// keeps feeding the engine -- a slow flag service can never stall the feed.
// FIFO order of what was kept is preserved, and the tap resumes once drained.
func TestFlagTapNeverBlocksPumpAndCountsDrops(t *testing.T) {
	ing, in := flagIngest(t)
	ing.SetFlagSyms([]string{"AAA"})

	const extra = 250
	total := int64(flagOutBuf + extra)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for v := int64(0); v < total; v++ {
			in <- flagAgg("AAA", v+1) // Vol doubles as a sequence number
		}
		in <- flagAgg("NOT-A-MEMBER", 1) // the feed carries on past the flood
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("feeding the pump stalled: a full flag tap blocked it")
	}
	waitApplied(t, ing, total+1) // every aggregate, the sentinel included, reached the engine

	if n := ing.FlagDrops(); n != extra {
		t.Fatalf("drops want exactly %d (sent %d into a %d buffer, no consumer), got %d", extra, total, flagOutBuf, n)
	}
	if n := len(ing.flagOut); n != flagOutBuf {
		t.Fatalf("the buffer should be full (%d), holds %d", flagOutBuf, n)
	}

	// What was kept is the OLDEST flagOutBuf, in order (a full channel drops the
	// newcomer, it never reorders or evicts).
	got := drainFlag(ing)
	if len(got) != flagOutBuf {
		t.Fatalf("drained %d, want %d", len(got), flagOutBuf)
	}
	for i, a := range got {
		if a.Vol != int64(i+1) {
			t.Fatalf("kept aggregate %d has seq %d, want %d: order broken", i, a.Vol, i+1)
		}
	}

	// Drained: the tap flows again and the drop counter did not move.
	in <- flagAgg("AAA", 9999)
	waitApplied(t, ing, total+2)
	if again := drainFlag(ing); len(again) != 1 || again[0].Vol != 9999 {
		t.Fatalf("the tap must resume after a drain, got %+v", again)
	}
	if n := ing.FlagDrops(); n != extra {
		t.Fatalf("drops must not move without overflow: want %d got %d", extra, n)
	}
}

// Before any SetFlagSyms the tap is simply off: the pump must not tee, drop or
// count anything for it.
func TestFlagTapOffByDefault(t *testing.T) {
	ing, in := flagIngest(t)
	in <- flagAgg("AAA", 1)
	waitApplied(t, ing, 1)
	if got := drainFlag(ing); len(got) != 0 || ing.FlagDrops() != 0 {
		t.Fatalf("tap must be off until SetFlagSyms: teed %+v drops %d", got, ing.FlagDrops())
	}
}
