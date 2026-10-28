package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/persist"
)

// watchCall is one TapeWatchSet invocation, both unions sorted for comparison.
type watchCall struct {
	tape  []string
	chart []string
	last  string
}

func newWatchServer(t *testing.T) (*Server, chan watchCall, *httptest.Server, context.Context) {
	t.Helper()
	fs := newFake()
	calls := make(chan watchCall, 64)
	srv := New(Config{Scanner: fs, TapeWatchSet: func(tape, chart []string, last string) {
		ts := append([]string(nil), tape...)
		cs := append([]string(nil), chart...)
		sort.Strings(ts)
		sort.Strings(cs)
		calls <- watchCall{tape: ts, chart: cs, last: last}
	}})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, calls, ts, ctx
}

// waitForUnions drains recompute calls until one matches both expected unions
// (coalescing means intermediate states may or may not be observed), asserting
// on every call that the tape union never absorbs chart-only symbols.
func waitForUnions(t *testing.T, calls chan watchCall, wantTape, wantChart []string) watchCall {
	t.Helper()
	deadline := time.After(3 * time.Second)
	var last watchCall
	for {
		select {
		case last = <-calls:
			// On EVERY call — target state or intermediate — the tape union
			// must only ever hold tape-watched symbols, never grid ones (a
			// leak would trigger per-symbol trade/quote subscriptions).
			for _, s := range last.tape {
				found := false
				for _, w := range wantTape {
					if s == w {
						found = true
					}
				}
				if !found {
					t.Fatalf("tape union grew a symbol outside the tape-watch set: %+v (want tape %v)", last, wantTape)
				}
			}
			if eqStrings(last.tape, wantTape) && eqStrings(last.chart, wantChart) {
				return last
			}
		case <-deadline:
			t.Fatalf("unions never reached tape=%v chart=%v; last call %+v", wantTape, wantChart, last)
		}
	}
}

// eqStrings compares two sorted slices treating nil and empty as equal (an
// empty union arrives as a nil sorted copy).
func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestChartWatchMergedUnion proves ONE recompute produces TWO sets: the tape
// union (unchanged semantics — grid symbols must never leak into it, they'd
// trigger per-symbol trade/quote subscriptions) and the chart union = tape ∪
// every client's chart.watch set (the ingest A.* tap needs all of them).
func TestChartWatchMergedUnion(t *testing.T) {
	_, calls, ts, ctx := newWatchServer(t)

	c1 := dialWS(t, ctx, ts)
	defer c1.CloseNow()
	c2 := dialWS(t, ctx, ts)
	defer c2.CloseNow()
	c3 := dialWS(t, ctx, ts)
	defer c3.CloseNow()

	// A tape watcher: its symbol rides BOTH unions.
	writeFrame(t, ctx, c1, core.TTapeWatch, core.TapeWatchMsg{Sym: "AAPL"})
	waitForUnions(t, calls, []string{"AAPL"}, []string{"AAPL"})

	// A charts-grid window: its set joins ONLY the chart union.
	writeFrame(t, ctx, c2, core.TChartWatch, core.ChartWatchMsg{Syms: []string{"tsla", "NVDA", "TSLA"}})
	waitForUnions(t, calls, []string{"AAPL"}, []string{"AAPL", "NVDA", "TSLA"})

	// A second grid window; overlap (TSLA) dedupes across clients.
	writeFrame(t, ctx, c3, core.TChartWatch, core.ChartWatchMsg{Syms: []string{"AMD", "TSLA"}})
	waitForUnions(t, calls, []string{"AAPL"}, []string{"AAPL", "AMD", "NVDA", "TSLA"})

	// Replacing a grid window's whole set drops its old symbols from the union.
	writeFrame(t, ctx, c2, core.TChartWatch, core.ChartWatchMsg{Syms: []string{"MSFT"}})
	waitForUnions(t, calls, []string{"AAPL"}, []string{"AAPL", "AMD", "MSFT", "TSLA"})

	// Clearing (empty set) removes the rest of that window's contribution.
	writeFrame(t, ctx, c2, core.TChartWatch, core.ChartWatchMsg{Syms: nil})
	waitForUnions(t, calls, []string{"AAPL"}, []string{"AAPL", "AMD", "TSLA"})
}

// TestChartWatchDisconnectShrinksChartUnion proves a closed charts window's
// symbols drop out of the chart union on the unregister recompute, exactly
// like a closed tape window's symbol does.
func TestChartWatchDisconnectShrinksChartUnion(t *testing.T) {
	_, calls, ts, ctx := newWatchServer(t)

	c1 := dialWS(t, ctx, ts)
	defer c1.CloseNow()
	c2 := dialWS(t, ctx, ts)

	writeFrame(t, ctx, c1, core.TTapeWatch, core.TapeWatchMsg{Sym: "AAPL"})
	waitForUnions(t, calls, []string{"AAPL"}, []string{"AAPL"})

	writeFrame(t, ctx, c2, core.TChartWatch, core.ChartWatchMsg{Syms: []string{"TSLA", "NVDA"}})
	waitForUnions(t, calls, []string{"AAPL"}, []string{"AAPL", "NVDA", "TSLA"})

	c2.CloseNow() // charts window closes -> its grid symbols must drop out

	waitForUnions(t, calls, []string{"AAPL"}, []string{"AAPL"})
}

// TestChartWatchCapsAtSixteen proves a malformed / oversized chart.watch frame
// cannot grow the union unboundedly: only the first 16 symbols are accepted.
func TestChartWatchCapsAtSixteen(t *testing.T) {
	_, calls, ts, ctx := newWatchServer(t)

	c1 := dialWS(t, ctx, ts)
	defer c1.CloseNow()

	syms := make([]string, 20)
	for i := range syms {
		syms[i] = fmt.Sprintf("SYM%02d", i)
	}
	writeFrame(t, ctx, c1, core.TChartWatch, core.ChartWatchMsg{Syms: syms})

	want := make([]string, 16) // first 16 in sent order survive the cap
	copy(want, syms[:16])
	sort.Strings(want)
	waitForUnions(t, calls, []string{}, want)
}

// TestWatchlistSetPersistsAndBroadcasts drives watchlist.set through a REAL
// temp-file SQLite DB and proves (a) the normalized ordered list lands in the
// watchlist table and (b) every connected window — sender included — receives
// the watchlist.data broadcast that keeps them in sync.
func TestWatchlistSetPersistsAndBroadcasts(t *testing.T) {
	db, err := persist.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := db.WatchlistRepo()

	fs := newFake()
	srv := New(Config{Scanner: fs, WatchlistLoad: repo.List, WatchlistSave: repo.Replace})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	c1 := dialWS(t, ctx, ts)
	defer c1.CloseNow()
	c2 := dialWS(t, ctx, ts)
	defer c2.CloseNow()

	// Dial returns before the server registers the connection, so prove c2 is
	// registered (its readLoop answers) before c1's broadcast can run without it.
	writeFrame(t, ctx, c2, core.TWatchlistGet, nil)
	var got core.WatchlistMsg
	if err := json.Unmarshal(readUntil(t, ctx, c2, core.TWatchlistData).D, &got); err != nil || len(got.Syms) != 0 {
		t.Fatalf("fresh DB must answer an empty watchlist: %+v err=%v", got, err)
	}

	writeFrame(t, ctx, c1, core.TWatchlistSet, core.WatchlistMsg{Syms: []string{"tsla", " nvda ", "TSLA", "AMD"}})

	want := []string{"TSLA", "NVDA", "AMD"} // uppercased, deduped, order preserved

	f := readUntil(t, ctx, c2, core.TWatchlistData)
	if err := json.Unmarshal(f.D, &got); err != nil || !reflect.DeepEqual(got.Syms, want) {
		t.Fatalf("watchlist.data on second client mismatch: %+v err=%v", got, err)
	}
	f = readUntil(t, ctx, c1, core.TWatchlistData)
	if err := json.Unmarshal(f.D, &got); err != nil || !reflect.DeepEqual(got.Syms, want) {
		t.Fatalf("watchlist.data on sender mismatch: %+v err=%v", got, err)
	}

	// Persisted, in order.
	stored, err := repo.List(context.Background())
	if err != nil || !reflect.DeepEqual(stored, want) {
		t.Fatalf("persisted watchlist mismatch: %v err=%v", stored, err)
	}

	// A later watchlist.get (e.g. a freshly opened window) is answered.
	writeFrame(t, ctx, c2, core.TWatchlistGet, nil)
	f = readUntil(t, ctx, c2, core.TWatchlistData)
	if err := json.Unmarshal(f.D, &got); err != nil || !reflect.DeepEqual(got.Syms, want) {
		t.Fatalf("watchlist.get answer mismatch: %+v err=%v", got, err)
	}

	// The hello bundle carries it too (scan.list precedent, async).
	c3 := dialWS(t, ctx, ts)
	defer c3.CloseNow()
	writeFrame(t, ctx, c3, core.THello, nil)
	f = readUntil(t, ctx, c3, core.TWatchlistData)
	if err := json.Unmarshal(f.D, &got); err != nil || !reflect.DeepEqual(got.Syms, want) {
		t.Fatalf("watchlist on hello mismatch: %+v err=%v", got, err)
	}
}

// TestChartGridRoundTripBroadcastsToOthersOnly proves chartgrid.set stores the
// opaque blob verbatim, broadcasts chartgrid.data to every OTHER client (the
// sender already renders that layout — a self-echo would re-trigger its render
// loop), and chartgrid.get returns the stored blob.
func TestChartGridRoundTripBroadcastsToOthersOnly(t *testing.T) {
	var mu sync.Mutex
	stored := ""
	fs := newFake()
	srv := New(Config{Scanner: fs,
		GridLoad: func(context.Context) (string, error) { mu.Lock(); defer mu.Unlock(); return stored, nil },
		GridSave: func(_ context.Context, layout string) error {
			mu.Lock()
			defer mu.Unlock()
			stored = layout
			return nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	c1 := dialWS(t, ctx, ts)
	defer c1.CloseNow()
	c2 := dialWS(t, ctx, ts)
	defer c2.CloseNow()

	// Dial returns before the server registers the connection, so prove c2 is
	// registered (its readLoop answers hello) before c1's fanout can skip it.
	writeFrame(t, ctx, c2, core.THello, nil)
	readUntil(t, ctx, c2, core.TScanList)

	layout := `{"v":1,"rows":2,"cols":2,"cells":["AAPL","TSLA","NVDA",null]}`
	writeFrame(t, ctx, c1, core.TChartGridSet, core.ChartGridMsg{Layout: json.RawMessage(layout)})

	// The OTHER window receives the new layout verbatim...
	f := readUntil(t, ctx, c2, core.TChartGridData)
	var got core.ChartGridMsg
	if err := json.Unmarshal(f.D, &got); err != nil || string(got.Layout) != layout {
		t.Fatalf("chartgrid.data mismatch on other client: %s err=%v", got.Layout, err)
	}

	// ...it was persisted...
	mu.Lock()
	if stored != layout {
		mu.Unlock()
		t.Fatalf("layout not persisted: %q", stored)
	}
	mu.Unlock()

	// ...and the SENDER got no self-echo in a bounded window.
	deadline := time.Now().Add(750 * time.Millisecond)
	for time.Now().Before(deadline) {
		rctx, cancelRead := context.WithTimeout(ctx, 250*time.Millisecond)
		_, data, err := c1.Read(rctx)
		cancelRead()
		if err != nil {
			break // idle: nothing is being pushed to the sender
		}
		var fr core.Frame
		if json.Unmarshal(data, &fr) == nil && fr.T == core.TChartGridData {
			t.Fatalf("sender must not receive its own chartgrid.data echo: %s", data)
		}
	}

	// chartgrid.get round-trips the stored blob (a freshly opened window).
	writeFrame(t, ctx, c2, core.TChartGridGet, nil)
	f = readUntil(t, ctx, c2, core.TChartGridData)
	if err := json.Unmarshal(f.D, &got); err != nil || string(got.Layout) != layout {
		t.Fatalf("chartgrid.get mismatch: %s err=%v", got.Layout, err)
	}
}

// TestChartGridRejectsOversizeLayout proves a blob over 64KB is rejected with
// the standard error frame and never reaches GridSave.
func TestChartGridRejectsOversizeLayout(t *testing.T) {
	saves := make(chan string, 1)
	fs := newFake()
	srv := New(Config{Scanner: fs,
		GridLoad: func(context.Context) (string, error) { return "", nil },
		GridSave: func(_ context.Context, layout string) error { saves <- layout; return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	c1 := dialWS(t, ctx, ts)
	defer c1.CloseNow()

	big := `"` + strings.Repeat("a", 64<<10) + `"` // 64KB+2 of valid JSON
	writeFrame(t, ctx, c1, core.TChartGridSet, core.ChartGridMsg{Layout: json.RawMessage(big)})

	e := readUntil(t, ctx, c1, core.TError)
	var em core.ErrorMsg
	if err := json.Unmarshal(e.D, &em); err != nil || em.Code != "chartgrid" {
		t.Fatalf("want chartgrid error code, got %+v err=%v", em, err)
	}
	select {
	case got := <-saves:
		t.Fatalf("oversize layout must never reach GridSave, got %d bytes", len(got))
	case <-time.After(300 * time.Millisecond):
	}
}
