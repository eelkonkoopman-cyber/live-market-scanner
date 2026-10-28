package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// fakeScanner is a controllable core.Scanner for hub tests.
type fakeScanner struct {
	ticks      chan core.ScanTick
	alerts     chan core.AlertMsg
	defs       []core.ScanDef
	upserts    chan core.ScanDef
	resyncTick core.ScanTick
	resyncOK   bool
}

func newFake() *fakeScanner {
	return &fakeScanner{
		ticks:   make(chan core.ScanTick, 8),
		alerts:  make(chan core.AlertMsg, 8),
		upserts: make(chan core.ScanDef, 8),
	}
}

func (f *fakeScanner) Upsert(d core.ScanDef) {
	select {
	case f.upserts <- d:
	default:
	}
}
func (f *fakeScanner) Delete(string)                       {}
func (f *fakeScanner) List() []core.ScanDef                { return f.defs }
func (f *fakeScanner) Ticks() <-chan core.ScanTick         { return f.ticks }
func (f *fakeScanner) Alerts() <-chan core.AlertMsg        { return f.alerts }
func (f *fakeScanner) Entries() <-chan core.ScanEntry      { return nil }
func (f *fakeScanner) Resync(string) (core.ScanTick, bool) { return f.resyncTick, f.resyncOK }
func (f *fakeScanner) Run(ctx context.Context) error       { <-ctx.Done(); return nil }

func dialWS(t *testing.T, ctx context.Context, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(ts.URL, "http") + "/stream"
	conn, _, err := websocket.Dial(ctx, u, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

// waitClients waits until srv has registered n sockets. websocket.Dial returns
// once the handshake is done, but the server adds the client only after its
// Accept returns: a frame broadcast in between reaches nobody.
func waitClients(t *testing.T, srv *Server, n int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		srv.mu.RLock()
		got := len(srv.clients)
		srv.mu.RUnlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("server registered %d clients, want %d", got, n)
		}
	}
}

func writeFrame(t *testing.T, ctx context.Context, conn *websocket.Conn, typ string, payload any) {
	t.Helper()
	var raw json.RawMessage
	if payload != nil {
		b, _ := json.Marshal(payload)
		raw = b
	}
	b, _ := json.Marshal(core.Frame{T: typ, D: raw})
	if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readUntil(t *testing.T, ctx context.Context, conn *websocket.Conn, typ string) core.Frame {
	t.Helper()
	for i := 0; i < 30; i++ {
		rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, data, err := conn.Read(rctx)
		cancel()
		if err != nil {
			t.Fatalf("read waiting for %s: %v", typ, err)
		}
		var f core.Frame
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		if f.T == typ {
			return f
		}
	}
	t.Fatalf("did not receive %s", typ)
	return core.Frame{}
}

func TestServerHelloListAndTick(t *testing.T) {
	fs := newFake()
	fs.defs = []core.ScanDef{{ID: "m", Name: "mom", Enabled: true, Columns: []string{"last"}}}
	srv := New(Config{Scanner: fs, Status: func() core.StatusMsg {
		return core.StatusMsg{Session: "regular", Feed: "live", Baseline: "ready"}
	}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn := dialWS(t, ctx, ts)
	defer conn.CloseNow()

	writeFrame(t, ctx, conn, core.THello, nil)
	list := readUntil(t, ctx, conn, core.TScanList)
	var lm core.ScanListMsg
	if err := json.Unmarshal(list.D, &lm); err != nil || len(lm.Scans) != 1 || lm.Scans[0].ID != "m" {
		t.Fatalf("scan.list mismatch: %+v err=%v", lm, err)
	}

	// Receiving scan.list proves the client is registered; now push a tick.
	fs.ticks <- core.ScanTick{ID: "m", Seq: 1, Order: []string{"AAA"}, Rows: map[string]core.RowJSON{"AAA": {"last": 10.0}}}
	tk := readUntil(t, ctx, conn, core.TScanTick)
	var st core.ScanTick
	if err := json.Unmarshal(tk.D, &st); err != nil || st.ID != "m" || len(st.Order) != 1 || st.Order[0] != "AAA" {
		t.Fatalf("scan.tick mismatch: %+v err=%v", st, err)
	}
}

func TestServerUpsertForwardedAndPersisted(t *testing.T) {
	fs := newFake()
	srv := New(Config{Scanner: fs})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn := dialWS(t, ctx, ts)
	defer conn.CloseNow()

	def := core.ScanDef{ID: "x", Name: "x", Enabled: true}
	writeFrame(t, ctx, conn, core.TScanUpsert, def)

	select {
	case got := <-fs.upserts:
		if got.ID != "x" {
			t.Fatalf("forwarded def mismatch: %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upsert was not forwarded to the scanner")
	}
}

func TestServerResync(t *testing.T) {
	fs := newFake()
	fs.resyncOK = true
	fs.resyncTick = core.ScanTick{ID: "m", Seq: 7, Full: true, Order: []string{"AAA", "BBB"},
		Rows: map[string]core.RowJSON{"AAA": {"last": 1.0}, "BBB": {"last": 2.0}}}
	srv := New(Config{Scanner: fs})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn := dialWS(t, ctx, ts)
	defer conn.CloseNow()

	writeFrame(t, ctx, conn, core.TScanResync, core.ScanRefMsg{ID: "m"})
	tk := readUntil(t, ctx, conn, core.TScanTick)
	var st core.ScanTick
	if err := json.Unmarshal(tk.D, &st); err != nil || !st.Full || st.Seq != 7 || len(st.Order) != 2 {
		t.Fatalf("resync full tick mismatch: %+v err=%v", st, err)
	}
}

func TestServerFundamentalsGet(t *testing.T) {
	fs := newFake()
	rev := 1_000_000.0
	srv := New(Config{Scanner: fs, Fundamentals: func(_ context.Context, sym string) (core.Fundamentals, error) {
		if sym != "ABCD" {
			t.Fatalf("unexpected sym: %s", sym)
		}
		return core.Fundamentals{Sym: sym, Quarters: []core.FinQuarter{{PeriodEnd: "2026-03-31", Revenue: &rev}}}, nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn := dialWS(t, ctx, ts)
	defer conn.CloseNow()

	writeFrame(t, ctx, conn, core.TFundamentalsGet, core.FundamentalsGetMsg{Sym: "ABCD"})
	f := readUntil(t, ctx, conn, core.TFundamentalsData)
	var got core.Fundamentals
	if err := json.Unmarshal(f.D, &got); err != nil || got.Sym != "ABCD" || len(got.Quarters) != 1 {
		t.Fatalf("fundamentals.data mismatch: %+v err=%v", got, err)
	}
	if got.Quarters[0].Revenue == nil || *got.Quarters[0].Revenue != rev {
		t.Fatalf("revenue lost in transit: %+v", got.Quarters[0])
	}
}

func TestServerFundamentalsNotEntitledMapsErrorCode(t *testing.T) {
	fs := newFake()
	srv := New(Config{Scanner: fs, Fundamentals: func(context.Context, string) (core.Fundamentals, error) {
		return core.Fundamentals{}, fmt.Errorf("wrapped: %w", core.ErrNotEntitled)
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn := dialWS(t, ctx, ts)
	defer conn.CloseNow()

	writeFrame(t, ctx, conn, core.TFundamentalsGet, core.FundamentalsGetMsg{Sym: "ABCD"})
	e := readUntil(t, ctx, conn, core.TError)
	var em core.ErrorMsg
	if err := json.Unmarshal(e.D, &em); err != nil || em.Code != "fundamentals-not-entitled" {
		t.Fatalf("expected fundamentals-not-entitled code, got %+v err=%v", em, err)
	}
	if em.Sym != "ABCD" {
		t.Fatalf("error frame must name the symbol the failed request was for, got %+v", em)
	}
}

// TestServerChartGetErrorIncludesSym proves a failed chart.get names the
// symbol it was for (Sym), so the UI can tell which chart cell failed instead
// of every chart error looking the same.
func TestServerChartGetErrorIncludesSym(t *testing.T) {
	fs := newFake()
	srv := New(Config{Scanner: fs, Chart: func(context.Context, string) ([]core.Bar, float64, error) {
		return nil, 0, fmt.Errorf("upstream fetch failed")
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn := dialWS(t, ctx, ts)
	defer conn.CloseNow()

	writeFrame(t, ctx, conn, core.TChartGet, core.ChartGetMsg{Sym: "WXYZ"})
	e := readUntil(t, ctx, conn, core.TError)
	var em core.ErrorMsg
	if err := json.Unmarshal(e.D, &em); err != nil || em.Code != "chart" {
		t.Fatalf("expected chart error code, got %+v err=%v", em, err)
	}
	if em.Sym != "WXYZ" {
		t.Fatalf("error frame must name the symbol the failed chart.get was for, got %+v", em)
	}
}

// TestTapeWatchSetUnionAcrossClients proves the server computes the UNION of
// every connected window's watched tape symbol, not just the most recent one.
func TestTapeWatchSetUnionAcrossClients(t *testing.T) {
	fs := newFake()
	type call struct {
		syms         []string
		lastSelected string
	}
	calls := make(chan call, 16)
	srv := New(Config{Scanner: fs, TapeWatchSet: func(syms, _ []string, lastSelected string) {
		sorted := append([]string(nil), syms...)
		sort.Strings(sorted)
		calls <- call{syms: sorted, lastSelected: lastSelected}
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	c1 := dialWS(t, ctx, ts)
	defer c1.CloseNow()
	c2 := dialWS(t, ctx, ts)
	defer c2.CloseNow()

	writeFrame(t, ctx, c1, core.TTapeWatch, core.TapeWatchMsg{Sym: "AAPL"})
	var last call
	select {
	case last = <-calls:
	case <-time.After(3 * time.Second):
		t.Fatal("no recompute after first tape.watch")
	}
	if len(last.syms) != 1 || last.syms[0] != "AAPL" || last.lastSelected != "AAPL" {
		t.Fatalf("want [AAPL]/AAPL after first watch, got %+v", last)
	}

	writeFrame(t, ctx, c2, core.TTapeWatch, core.TapeWatchMsg{Sym: "MSFT"})
	select {
	case last = <-calls:
	case <-time.After(3 * time.Second):
		t.Fatal("no recompute after second tape.watch")
	}
	if len(last.syms) != 2 || last.syms[0] != "AAPL" || last.syms[1] != "MSFT" || last.lastSelected != "MSFT" {
		t.Fatalf("want union [AAPL MSFT]/MSFT, got %+v", last)
	}
}

// TestTapeWatchSetShrinksOnDisconnect proves a closed window's symbol drops
// out of the union automatically, without the remaining window doing anything.
func TestTapeWatchSetShrinksOnDisconnect(t *testing.T) {
	fs := newFake()
	calls := make(chan []string, 16)
	srv := New(Config{Scanner: fs, TapeWatchSet: func(syms, _ []string, _ string) {
		sorted := append([]string(nil), syms...)
		sort.Strings(sorted)
		calls <- sorted
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	c1 := dialWS(t, ctx, ts)
	c2 := dialWS(t, ctx, ts)
	defer c2.CloseNow()

	writeFrame(t, ctx, c1, core.TTapeWatch, core.TapeWatchMsg{Sym: "AAPL"})
	select {
	case got := <-calls:
		if len(got) != 1 || got[0] != "AAPL" {
			t.Fatalf("want [AAPL], got %v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no recompute after first tape.watch")
	}

	writeFrame(t, ctx, c2, core.TTapeWatch, core.TapeWatchMsg{Sym: "MSFT"})
	select {
	case got := <-calls:
		if len(got) != 2 {
			t.Fatalf("want [AAPL MSFT], got %v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no recompute after second tape.watch")
	}

	c1.CloseNow() // disconnect -- AAPL should drop out of the union

	deadline := time.After(3 * time.Second)
	for {
		select {
		case got := <-calls:
			if len(got) == 1 && got[0] == "MSFT" {
				return
			}
		case <-deadline:
			t.Fatal("union never shrank to just MSFT after c1 disconnected")
		}
	}
}

// TestTapeWatchSetConcurrentClientsConverge fires tape.watch from many
// connections concurrently (run with -race): the coalescing recompute
// goroutine must serialize the union computation without racing, and the
// union must eventually reflect every symbol once all messages are processed.
func TestTapeWatchSetConcurrentClientsConverge(t *testing.T) {
	fs := newFake()
	var mu sync.Mutex
	var latest []string
	srv := New(Config{Scanner: fs, TapeWatchSet: func(syms, _ []string, _ string) {
		sorted := append([]string(nil), syms...)
		sort.Strings(sorted)
		mu.Lock()
		latest = sorted
		mu.Unlock()
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	const n = 12
	conns := make([]*websocket.Conn, n)
	for i := range conns {
		conns[i] = dialWS(t, ctx, ts)
	}
	defer func() {
		for _, c := range conns {
			c.CloseNow()
		}
	}()

	var wg sync.WaitGroup
	for i, c := range conns {
		wg.Add(1)
		go func(i int, c *websocket.Conn) {
			defer wg.Done()
			writeFrame(t, ctx, c, core.TTapeWatch, core.TapeWatchMsg{Sym: fmt.Sprintf("SYM%02d", i)})
		}(i, c)
	}
	wg.Wait()

	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		got := len(latest)
		snapshot := append([]string(nil), latest...)
		mu.Unlock()
		if got == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("union never converged to all %d symbols, last seen: %v", n, snapshot)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServerRowGet(t *testing.T) {
	fs := newFake()
	srv := New(Config{Scanner: fs, RowFor: func(sym string, cols []string) (core.RowJSON, bool) {
		if sym != "ABCD" {
			t.Fatalf("unexpected sym: %s", sym)
		}
		return core.RowJSON{"last": 12.5}, true
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn := dialWS(t, ctx, ts)
	defer conn.CloseNow()

	writeFrame(t, ctx, conn, core.TRowGet, core.RowGetMsg{Sym: "ABCD", Columns: []string{"last"}})
	f := readUntil(t, ctx, conn, core.TRowData)
	var got core.RowDataMsg
	if err := json.Unmarshal(f.D, &got); err != nil || got.Sym != "ABCD" {
		t.Fatalf("row.data mismatch: %+v err=%v", got, err)
	}
	if v, ok := got.Row["last"]; !ok || v.(float64) != 12.5 {
		t.Fatalf("row.data missing last: %+v", got.Row)
	}
}

func TestServerRowGetUnknownSymbolReturnsNilRow(t *testing.T) {
	fs := newFake()
	srv := New(Config{Scanner: fs, RowFor: func(string, []string) (core.RowJSON, bool) {
		return nil, false
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn := dialWS(t, ctx, ts)
	defer conn.CloseNow()

	writeFrame(t, ctx, conn, core.TRowGet, core.RowGetMsg{Sym: "ZZZZ"})
	f := readUntil(t, ctx, conn, core.TRowData)
	var got core.RowDataMsg
	if err := json.Unmarshal(f.D, &got); err != nil || got.Sym != "ZZZZ" || got.Row != nil {
		t.Fatalf("want nil row for unknown symbol, got %+v err=%v", got, err)
	}
}

// TestServerTapeClaudeSetForwarded proves tape.claudeSet frames reach the
// TapeClaudeSet callback with the exact symbol/enabled the client sent.
func TestServerTapeClaudeSetForwarded(t *testing.T) {
	fs := newFake()
	type call struct {
		sym string
		on  bool
	}
	calls := make(chan call, 4)
	srv := New(Config{Scanner: fs, TapeClaudeSet: func(sym string, on bool) {
		calls <- call{sym, on}
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn := dialWS(t, ctx, ts)
	defer conn.CloseNow()

	writeFrame(t, ctx, conn, core.TTapeClaudeSet, core.TapeClaudeSetMsg{Sym: "AAPL", Enabled: true})
	select {
	case got := <-calls:
		if got.sym != "AAPL" || !got.on {
			t.Fatalf("want AAPL/true, got %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tape.claudeSet was not forwarded")
	}

	writeFrame(t, ctx, conn, core.TTapeClaudeSet, core.TapeClaudeSetMsg{Sym: "AAPL", Enabled: false})
	select {
	case got := <-calls:
		if got.sym != "AAPL" || got.on {
			t.Fatalf("want AAPL/false, got %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tape.claudeSet (disable) was not forwarded")
	}
}

// TestServerTapeCommentarySetForwarded proves tape.commentarySet frames reach
// the TapeCommentarySet callback with the exact symbol/seconds the client sent.
func TestServerTapeCommentarySetForwarded(t *testing.T) {
	fs := newFake()
	type call struct {
		sym  string
		secs float64
	}
	calls := make(chan call, 4)
	srv := New(Config{Scanner: fs, TapeCommentarySet: func(sym string, secs float64) {
		calls <- call{sym, secs}
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn := dialWS(t, ctx, ts)
	defer conn.CloseNow()

	writeFrame(t, ctx, conn, core.TTapeCommentarySet, core.TapeCommentarySetMsg{Sym: "MSFT", Seconds: 30})
	select {
	case got := <-calls:
		if got.sym != "MSFT" || got.secs != 30 {
			t.Fatalf("want MSFT/30, got %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tape.commentarySet was not forwarded")
	}
}

// TestServerTapeRegimesBroadcast proves TapeRegimes events reach every
// connected client as tape.regime frames.
func TestServerTapeRegimesBroadcast(t *testing.T) {
	fs := newFake()
	regimes := make(chan core.TapeRegimeMsg, 4)
	srv := New(Config{Scanner: fs, TapeRegimes: regimes})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn := dialWS(t, ctx, ts)
	defer conn.CloseNow()
	waitClients(t, srv, 1) // the broadcast below must not race the server registering the socket

	regimes <- core.TapeRegimeMsg{
		Sym: "AAPL", Ts: 123, Trigger: "block_print", Headline: "Block print",
		Verdict: core.TapeVerdict{Control: "buyers", Confidence: 0.6, Text: "buyers stepping in", Source: "local"},
	}
	f := readUntil(t, ctx, conn, core.TTapeRegime)
	var got core.TapeRegimeMsg
	if err := json.Unmarshal(f.D, &got); err != nil || got.Sym != "AAPL" || got.Trigger != "block_print" || got.Verdict.Control != "buyers" {
		t.Fatalf("tape.regime mismatch: %+v err=%v", got, err)
	}
}

// TestServerTapeCardsBroadcast proves TapeCards events reach every connected
// client as tape.card frames.
func TestServerTapeCardsBroadcast(t *testing.T) {
	fs := newFake()
	cards := make(chan core.TapeCardMsg, 4)
	srv := New(Config{Scanner: fs, TapeCards: cards})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn := dialWS(t, ctx, ts)
	defer conn.CloseNow()
	waitClients(t, srv, 1) // the broadcast below must not race the server registering the socket

	cards <- core.TapeCardMsg{
		Sym: "AAPL", Ts: 123, Trigger: "heartbeat", Headline: "Live commentary",
		Model: "claude-3", Confidence: 0.7, WhatsHappening: "buyers pressing", WatchFor: "a break of vwap",
	}
	f := readUntil(t, ctx, conn, core.TTapeCard)
	var got core.TapeCardMsg
	if err := json.Unmarshal(f.D, &got); err != nil || got.Sym != "AAPL" || got.Model != "claude-3" || got.WatchFor != "a break of vwap" {
		t.Fatalf("tape.card mismatch: %+v err=%v", got, err)
	}
}

// TestServerSetupReadsBroadcast proves SetupReads reach every connected client
// as setup.read frames, with the camelCase payload the panel reads.
func TestServerSetupReadsBroadcast(t *testing.T) {
	fs := newFake()
	reads := make(chan core.SetupReadMsg, 4)
	srv := New(Config{Scanner: fs, SetupReads: reads})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn := dialWS(t, ctx, ts)
	defer conn.CloseNow()
	waitClients(t, srv, 1) // the broadcast below must not race the server registering the socket

	reads <- core.SetupReadMsg{
		Sym: "AAPL", Ts: 123, Quality: 4, Verdict: "textbook",
		WhatsHappening: "tight base under the pole high", Risks: []string{"thin volume"},
		Invalidation: "a close under 10.20", Model: "claude-3", AsOf: "2026-07-30",
	}
	f := readUntil(t, ctx, conn, core.TSetupRead)
	var got core.SetupReadMsg
	if err := json.Unmarshal(f.D, &got); err != nil {
		t.Fatalf("setup.read unmarshal: %v", err)
	}
	if got.Sym != "AAPL" || got.Quality != 4 || got.Verdict != "textbook" ||
		got.Model != "claude-3" || got.AsOf != "2026-07-30" ||
		got.Invalidation != "a close under 10.20" ||
		len(got.Risks) != 1 || got.Risks[0] != "thin volume" {
		t.Fatalf("setup.read mismatch: %+v", got)
	}
}

// TestServerTapePrintsRoutedOnlyToWatchingClient proves tape.prints batches
// are a per-window feed, not a broadcast: with two connected windows watching
// DIFFERENT symbols, a batch for AAPL reaches only the AAPL window — the MSFT
// window must see none, ever (unlike the ~1 Hz tape.stat frames, which stay
// fanoutAll).
func TestServerTapePrintsRoutedOnlyToWatchingClient(t *testing.T) {
	fs := newFake()
	prints := make(chan core.TapePrintsMsg, 4)
	watched := make(chan struct{}, 4)
	srv := New(Config{Scanner: fs, TapePrints: prints, TapeWatchSet: func([]string, []string, string) {
		watched <- struct{}{}
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	c1 := dialWS(t, ctx, ts)
	defer c1.CloseNow()
	c2 := dialWS(t, ctx, ts)
	defer c2.CloseNow()

	// Serialize the two watches on the recompute callback: once it fires, the
	// client's setTapeSym (which precedes it) is guaranteed applied.
	writeFrame(t, ctx, c1, core.TTapeWatch, core.TapeWatchMsg{Sym: "AAPL"})
	select {
	case <-watched:
	case <-time.After(3 * time.Second):
		t.Fatal("no recompute after c1's tape.watch")
	}
	writeFrame(t, ctx, c2, core.TTapeWatch, core.TapeWatchMsg{Sym: "MSFT"})
	select {
	case <-watched:
	case <-time.After(3 * time.Second):
		t.Fatal("no recompute after c2's tape.watch")
	}

	prints <- core.TapePrintsMsg{
		Sym:     "AAPL",
		Prints:  []core.Print{{TS: 1, Price: 4.52, Size: 100, Side: "buy", Notional: 452}},
		Dropped: 2,
	}

	f := readUntil(t, ctx, c1, core.TTapePrints)
	var got core.TapePrintsMsg
	if err := json.Unmarshal(f.D, &got); err != nil || got.Sym != "AAPL" || got.Dropped != 2 ||
		len(got.Prints) != 1 || got.Prints[0].Side != "buy" || got.Prints[0].Size != 100 {
		t.Fatalf("tape.prints mismatch on the watching client: %+v err=%v", got, err)
	}

	// The MSFT window must receive NO tape.prints frame in a bounded window.
	deadline := time.Now().Add(750 * time.Millisecond)
	for time.Now().Before(deadline) {
		rctx, cancelRead := context.WithTimeout(ctx, 250*time.Millisecond)
		_, data, err := c2.Read(rctx)
		cancelRead()
		if err != nil {
			break // idle: nothing is being pushed to c2
		}
		var fr core.Frame
		if json.Unmarshal(data, &fr) == nil && fr.T == core.TTapePrints {
			t.Fatalf("client watching MSFT must not receive AAPL's tape.prints: %s", data)
		}
	}
}

// TestServerTapePrintsSkipsIdleClient proves a window that never sent
// tape.watch (tapeSym "") receives no tape.prints at all, even while another
// window is receiving the same symbol's batches.
func TestServerTapePrintsSkipsIdleClient(t *testing.T) {
	fs := newFake()
	prints := make(chan core.TapePrintsMsg, 4)
	watched := make(chan struct{}, 4)
	srv := New(Config{Scanner: fs, TapePrints: prints, TapeWatchSet: func([]string, []string, string) {
		watched <- struct{}{}
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	c1 := dialWS(t, ctx, ts)
	defer c1.CloseNow()
	idle := dialWS(t, ctx, ts)
	defer idle.CloseNow()

	// Prove the idle client is fully registered (its readLoop answers hello)
	// before any batch flows — its silence below is then meaningful.
	writeFrame(t, ctx, idle, core.THello, nil)
	readUntil(t, ctx, idle, core.TScanList)

	writeFrame(t, ctx, c1, core.TTapeWatch, core.TapeWatchMsg{Sym: "AAPL"})
	select {
	case <-watched:
	case <-time.After(3 * time.Second):
		t.Fatal("no recompute after tape.watch")
	}

	prints <- core.TapePrintsMsg{Sym: "AAPL", Prints: []core.Print{{TS: 1, Price: 1, Size: 1, Side: "sell", Notional: 1}}}

	// Delivery to the watcher proves the fanout actually ran...
	readUntil(t, ctx, c1, core.TTapePrints)

	// ...and the idle window must have received none of it.
	deadline := time.Now().Add(750 * time.Millisecond)
	for time.Now().Before(deadline) {
		rctx, cancelRead := context.WithTimeout(ctx, 250*time.Millisecond)
		_, data, err := idle.Read(rctx)
		cancelRead()
		if err != nil {
			break // idle: nothing more is being pushed
		}
		var fr core.Frame
		if json.Unmarshal(data, &fr) == nil && fr.T == core.TTapePrints {
			t.Fatalf("client watching nothing must not receive tape.prints: %s", data)
		}
	}
}

// TestBroadcastSurvivesNilSetupReads is the server-side half of the
// SCANNER_FLAGREAD=0 kill switch. Every optional channel here is nil,
// SetupReads included, and the broadcast loop must keep serving everything
// else: a nil channel's select case can never become ready, so it is inert
// rather than fatal. (A CLOSED channel would be the opposite — permanently
// ready — which is why flagread's disabled Run leaves Reads() open; see
// TestDisabledWorkerKeepsAnOpenIdleReadsChannel.)
func TestBroadcastSurvivesNilSetupReads(t *testing.T) {
	fs := newFake()
	srv := New(Config{Scanner: fs}) // no SetupReads, no Entries, no TapeCards...
	if srv.cfg.SetupReads != nil {
		t.Fatal("this test is only meaningful with a nil SetupReads channel")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn := dialWS(t, ctx, ts)
	defer conn.CloseNow()
	waitClients(t, srv, 1) // the broadcast below must not race the server registering the socket

	fs.ticks <- core.ScanTick{ID: "m", Seq: 1, Order: []string{"AAA"}, Rows: map[string]core.RowJSON{"AAA": {"last": 10.0}}}

	// Read for a bounded window and account for EVERY frame. Two properties,
	// and the second is the one with teeth: the tick proves the loop still
	// runs, and the total absence of setup.read frames proves the nil case is
	// genuinely inert. Substituting a closed channel for the nil one would keep
	// the tick flowing (select picks at random among ready cases) while
	// emitting a torrent of zero-value setup.read frames — so asserting on the
	// tick alone would not catch it.
	sawTick, setupReads := false, 0
	deadline := time.Now().Add(750 * time.Millisecond)
	for time.Now().Before(deadline) {
		rctx, cancelRead := context.WithTimeout(ctx, 250*time.Millisecond)
		_, data, err := conn.Read(rctx)
		cancelRead()
		if err != nil {
			break // idle: nothing more is being pushed
		}
		var f core.Frame
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		switch f.T {
		case core.TScanTick:
			var st core.ScanTick
			if json.Unmarshal(f.D, &st) == nil && st.ID == "m" {
				sawTick = true
			}
		case core.TSetupRead:
			setupReads++
		}
	}
	if !sawTick {
		t.Fatal("a nil SetupReads channel wedged the broadcast loop: no scan.tick arrived")
	}
	if setupReads != 0 {
		t.Fatalf("nil SetupReads must emit nothing, got %d setup.read frames "+
			"(a permanently-ready case is spinning the broadcast loop)", setupReads)
	}
}

func TestServerStaticIndex(t *testing.T) {
	srv := New(Config{Scanner: newFake()})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Live Market Scanner") {
		t.Fatalf("index.html not served")
	}
	// SPA fallback: an unknown path returns index.html, not 404.
	resp2, err := http.Get(ts.URL + "/some/spa/route")
	if err != nil {
		t.Fatalf("get spa route: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("SPA fallback status %d", resp2.StatusCode)
	}
}

// TestTapeChangeCoalescingKeepsLatestSelection proves the tapeChange coalescer
// can't lose a selected symbol to a burst of signals. This is exactly the
// chart-switch scenario: switching the linked chart fires tape.watch "" (the
// old chart's effect cleanup) immediately followed by tape.watch "NEW" (the
// new chart's effect) — both call signalTapeChange before the recompute
// goroutine gets a turn, so they coalesce into one pending signal. Losing
// "NEW" here (as the old chan string implementation did, since a full buffer
// silently drops the send) meant recomputeTapeSet ran with lastSelected ""
// and tws.Show("") was a no-op: clicking the chart moved nothing in TWS.
//
// This drives signalTapeChange/drainTapeChange directly, without running
// runTapeRecompute's goroutine, so the coalescing behavior is deterministic
// and doesn't depend on scheduling.
func TestTapeChangeCoalescingKeepsLatestSelection(t *testing.T) {
	var got []string
	s := New(Config{TapeWatchSet: func(_, _ []string, sel string) {
		got = append(got, sel)
	}})

	s.signalTapeChange("")
	s.signalTapeChange("NEW")
	s.signalTapeChange("")

	s.drainTapeChange()

	if len(got) != 1 {
		t.Fatalf("want exactly one recompute, got %d: %+v", len(got), got)
	}
	if got[0] != "NEW" {
		t.Fatalf("want coalesced recompute to keep the latest non-empty selection %q, got %q", "NEW", got[0])
	}
}
