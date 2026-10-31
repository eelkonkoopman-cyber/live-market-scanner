package ingest

import (
	"testing"
)

// The order ticket's tap never drops: with no consumer, a flood far beyond the
// chart tap's 512 buffer is queued (and merged, never lost, past the per-symbol
// bound), the pump never blocks, and the lowest low survives for the stop.
func TestTicketTapIsLosslessAndNeverBlocks(t *testing.T) {
	ing, in := flagIngest(t)
	ing.SetTicketSyms([]string{"AAA", "BBB"})

	const n = 200 // under the per-symbol bound: every aggregate kept as-is
	for v := int64(1); v <= n; v++ {
		in <- flagAgg("AAA", v)
		in <- flagAgg("ZZZ", v) // not a member
	}
	b := flagAgg("BBB", 1)
	b.Low = 9.5
	in <- b
	waitApplied(t, ing, 2*n+1)
	select {
	case <-ing.TicketNotify():
	default:
		t.Fatal("no wake-up")
	}
	got := ing.DrainTicket()
	if len(got) != n+1 {
		t.Fatalf("drained %d, want %d", len(got), n+1)
	}
	for i := 0; i < n; i++ {
		if got[i].Sym != "AAA" || got[i].Vol != int64(i+1) {
			t.Fatalf("agg %d = %+v: lost or reordered", i, got[i])
		}
	}
	if got[n].Sym != "BBB" || got[n].Low != 9.5 {
		t.Fatalf("%+v", got[n])
	}

	// Past the bound: merged into the last queued one, low/high/volume kept.
	var vol int64
	for v := int64(1); v <= tkMaxPerSym+50; v++ {
		a := flagAgg("AAA", v)
		if v == tkMaxPerSym+20 {
			a.Low = 7 // a wick in a merged second must survive
		}
		vol += v
		in <- a
	}
	waitApplied(t, ing, 2*n+1+tkMaxPerSym+50)
	got = ing.DrainTicket()
	if len(got) != tkMaxPerSym || ing.TicketMerged() != 50 {
		t.Fatalf("kept %d merged %d", len(got), ing.TicketMerged())
	}
	var sum int64
	for _, a := range got {
		sum += a.Vol
	}
	if sum != vol || got[len(got)-1].Low != 7 {
		t.Fatalf("volume %d want %d, last low %v", sum, vol, got[len(got)-1].Low)
	}
	if again := ing.DrainTicket(); len(again) != 0 {
		t.Fatalf("not emptied: %d", len(again))
	}
}
