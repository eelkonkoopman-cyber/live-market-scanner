package engine

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/scan"
)

// recycleSink is a core.Engine for the scanner that relays a running engine's
// snapshots, checking each one is whole (every row where its Index says, Index
// and rows the same size) before the scanner sees it. It embeds the engine, so
// the scanner finds Recycle on it and the merger refills buffers it hands back.
type recycleSink struct {
	*Engine
	in   <-chan *core.MetricSnapshot
	out  chan *core.MetricSnapshot
	bad  chan string
	seen atomic.Int64
	back atomic.Int64 // snapshots the scanner handed back
}

func (k *recycleSink) Snapshots() <-chan *core.MetricSnapshot { return k.out }

func (k *recycleSink) Recycle(snap *core.MetricSnapshot) {
	k.back.Add(1)
	k.Engine.Recycle(snap)
}

func (k *recycleSink) relay(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case snap := <-k.in:
			if len(snap.Index) != len(snap.Rows) {
				k.bad <- fmt.Sprintf("gen %d: Index has %d entries for %d rows", snap.Gen, len(snap.Index), len(snap.Rows))
				return
			}
			for i := range snap.Rows {
				if j, ok := snap.Index[snap.Rows[i].Sym]; !ok || j != i {
					k.bad <- fmt.Sprintf("gen %d: row %d is %q but Index says %d (%v)", snap.Gen, i, snap.Rows[i].Sym, j, ok)
					return
				}
			}
			k.seen.Add(1)
			k.out <- snap
		}
	}
}

// The merger refills snapshot buffers the scanner hands back (Recycle) while
// the shards, the clock and the scanner all run: under -race any read of a
// recycled buffer after it was handed back is a reported race, and a buffer
// refilled while still in use shows up as rows that disagree with their Index.
func TestRecycledSnapshotsUnderLoad(t *testing.T) {
	e := New(Options{TickHz: 200, Dildo: true, Climax: true, Bbs: true, Ignition: true, EmaVwap: true})
	sink := &recycleSink{Engine: e, in: e.Snapshots(), out: make(chan *core.MetricSnapshot), bad: make(chan string, 1)}
	sc := scan.New(sink)
	for i := 0; i < 4; i++ {
		sc.Upsert(core.ScanDef{
			ID: fmt.Sprintf("s%d", i), Name: "s", Enabled: true, Limit: 25,
			Filter:  core.Filter{Field: core.FieldVol, Cmp: core.CmpGT, Value: float64(i * 50)},
			Sort:    []core.SortKey{{Field: core.FieldChgDay, Dir: "desc"}},
			Columns: []string{core.FieldLast, core.FieldChgDay, core.FieldVol, core.FieldHits},
			Alert:   core.AlertConfig{Enabled: true, On: core.AlertOnEnter},
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go e.Run(ctx)
	go sink.relay(ctx)
	go sc.Run(ctx)
	go func() { // the hub's side: drain everything the scanner emits
		for {
			select {
			case <-ctx.Done():
				return
			case <-sc.Ticks():
			case <-sc.Alerts():
			case <-sc.Entries():
			}
		}
	}()
	go func() { // the feed: 3,000 symbols trading
		start := time.Now().Unix()
		for n := 0; ctx.Err() == nil; n++ {
			sym := fmt.Sprintf("S%04d", n%3000)
			px := 1 + float64(n%977)/100
			sec := start + int64(n/3000)
			e.Apply(core.Agg{Sym: sym, Open: px, High: px * 1.01, Low: px * 0.99, Close: px, Vol: int64(100 + n%500), StartMs: sec * 1000, EndMs: sec*1000 + 1000})
			if n%5000 == 0 {
				sc.TopMovers(5, 0, 0) // a reader routed through the scanner's goroutine
			}
		}
	}()
	select {
	case msg := <-sink.bad:
		t.Fatal(msg)
	case <-ctx.Done():
	}
	n, back := sink.seen.Load(), sink.back.Load()
	if n < 10 || back < 5 {
		t.Fatalf("only %d snapshots checked, %d recycled", n, back)
	}
	t.Logf("%d snapshots checked, %d recycled", n, back)
}
