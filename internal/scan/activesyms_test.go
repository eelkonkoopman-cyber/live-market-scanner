package scan

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/engine"
)

// ActiveSyms feeds the all-filings EDGAR watcher: a filing is only shown if
// some scan surfaced the name today. These tests pin the union semantics —
// current membership PLUS today's hits history, across every scan.

// The union must span ALL scans: a symbol surfaced by any one of them is
// active, and a symbol surfaced by none of them must not be.
func TestActiveSymsUnionAcrossScans(t *testing.T) {
	s := New(nil)
	s.upsert(momScan()) // "m": chg5m > 2
	cheap := core.ScanDef{
		ID: "cheap", Name: "cheap", Enabled: true, Limit: 10,
		Filter:  core.Filter{Field: core.FieldLast, Cmp: core.CmpLT, Value: 5.0},
		Columns: []string{"last"},
	}
	s.upsert(cheap)

	// A matches only "m" (chg5m 5, $100); B matches only "cheap" ($2, chg5m 0);
	// C matches neither.
	s.process(snapOf(1000, mkRow("A", 100, 5), mkRow("B", 2, 0), mkRow("C", 50, 0)))

	got := s.activeSyms()
	if _, ok := got["A"]; !ok {
		t.Fatalf("A matched scan m -> must be active, got %v", got)
	}
	if _, ok := got["B"]; !ok {
		t.Fatalf("B matched scan cheap -> must be active, got %v", got)
	}
	if _, ok := got["C"]; ok {
		t.Fatalf("C matched no scan -> must NOT be active, got %v", got)
	}
}

// "Surfaced today" outlives membership: a symbol that entered earlier and has
// since dropped out stays active via its hits history — and keeps contributing
// even after its scan is toggled off (a name the user saw this morning is
// still worth a filing alert this afternoon).
func TestActiveSymsIncludesExitedHitsAndDisabledScans(t *testing.T) {
	s := New(nil)
	def := momScan()
	s.upsert(def)

	base := time.Date(2026, 7, 28, 15, 0, 0, 0, s.etLoc).UnixMilli()
	// B matched at baseline (hits floors to 1)...
	s.process(snapOf(base, mkRow("B", 10, 5)))
	// ...then faded out of the set.
	s.process(snapOf(base+60_000, mkRow("B", 10, 1)))

	if _, still := s.scans["m"].prevSet["B"]; still {
		t.Fatal("precondition broken: B should no longer be matched")
	}
	if _, ok := s.activeSyms()["B"]; !ok {
		t.Fatal("B entered earlier today -> must stay active after exiting the set")
	}

	// Toggle the scan off: hits history survives the upsert and must still count.
	def.Enabled = false
	s.upsert(def)
	s.process(snapOf(base+120_000, mkRow("B", 10, 5))) // disabled: not evaluated
	if _, ok := s.activeSyms()["B"]; !ok {
		t.Fatal("a disabled scan's hits history must still contribute to ActiveSyms")
	}
}

// The EDGAR watcher ranges over the result unconditionally, so an empty
// scanner must hand back an empty map, never nil.
func TestActiveSymsEmptyScannerIsEmptyNonNil(t *testing.T) {
	s := New(nil)
	got := s.activeSyms()
	if got == nil {
		t.Fatal("empty scanner: want an empty non-nil map, got nil")
	}
	if len(got) != 0 {
		t.Fatalf("empty scanner: want no symbols, got %v", got)
	}
}

// Callers hold the snapshot across a whole poll cycle and may mutate it; that
// must never corrupt scanner state. Proven by mutating one snapshot and
// checking a second call is unaffected.
func TestActiveSymsReturnsAFreshCopy(t *testing.T) {
	s := New(nil)
	s.upsert(momScan())
	s.process(snapOf(1000, mkRow("A", 10, 5)))

	first := s.activeSyms()
	if _, ok := first["A"]; !ok {
		t.Fatalf("precondition: A must be active, got %v", first)
	}
	delete(first, "A")
	first["FAKE"] = struct{}{}

	second := s.activeSyms()
	if _, ok := second["A"]; !ok {
		t.Fatal("mutating a returned snapshot must not remove A from scanner state")
	}
	if _, ok := second["FAKE"]; ok {
		t.Fatal("mutating a returned snapshot must not inject FAKE into scanner state")
	}
}

// The public accessor must be safe while Run() is processing engine ticks:
// readers hammer ActiveSyms (and mutate their copies, which -race would flag
// as a data race if they aliased scan state) while the engine keeps the Run
// goroutine rewriting prevSet/hits. Same harness as TestScannerRunWithEngine.
func TestActiveSymsRaceCleanUnderConcurrentTicks(t *testing.T) {
	eng := engine.New(engine.Options{TickHz: 20})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	sc := New(eng)
	sc.Upsert(core.ScanDef{ID: "all", Name: "all", Enabled: true, Limit: 100, Columns: []string{"last"}})
	go func() { _ = sc.Run(ctx) }()

	// Keep fresh aggs flowing so process() keeps running while readers hold
	// and mutate their snapshots.
	feeding := make(chan struct{})
	go func() {
		defer close(feeding)
		for i := int64(0); i < 100; i++ {
			eng.Apply(core.Agg{Sym: "AAA", Close: 10, High: 10, Low: 10, Vol: 100, EndMs: 1_700_000_000_000 + i*1000})
			eng.Apply(core.Agg{Sym: "BBB", Close: 20, High: 20, Low: 20, Vol: 100, EndMs: 1_700_000_000_000 + i*1000})
			time.Sleep(time.Millisecond)
		}
	}()

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				m := sc.ActiveSyms()
				m["MUTATE"] = struct{}{} // a real copy tolerates caller writes
				delete(m, "AAA")
			}
		}()
	}
	wg.Wait()
	<-feeding

	// After the dust settles both fed symbols must be visible in the union —
	// and none of the reader-side mutations may have leaked in.
	deadline := time.After(3 * time.Second)
	for {
		syms := sc.ActiveSyms()
		_, okA := syms["AAA"]
		_, okB := syms["BBB"]
		if okA && okB {
			if _, leaked := syms["MUTATE"]; leaked {
				t.Fatal("caller-side mutation leaked into scanner state")
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("AAA+BBB never both surfaced via ActiveSyms within 3s (got %v)", syms)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}
