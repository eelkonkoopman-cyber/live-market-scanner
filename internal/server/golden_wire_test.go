package server

// Golden test for the WebSocket wire encoding. A performance pass over the hub
// (the per-tick JSON encoding, fanout, anything that touches the bytes a window
// receives) must be bit-identical for the UI, so this test drives a scripted
// session through an in-process server and pins the SHA-256 of every frame each
// client receives.
//
// Two scripts run:
//
//   - hub: four real WebSocket clients with different views / tape-watch symbols
//     against a Started server whose scanner and feeds are unbuffered fakes. Every
//     broadcast frame type of the hot path (scan.tick of every shape, alert,
//     scan.enter, news, tape.*, chart.bar, short.live, tws.auto, blacklist ...)
//     is injected one step at a time (flagevents.add arrives via PublishFlagEvents,
//     which the flag log calls from its own goroutine). After each step a fence frame is broadcast
//     through the same single-goroutine select loop, so "everything the step
//     produced" is exactly the frames before the fence, and "this client received
//     nothing" (unviewed scan, other window's tape prints) is recorded as a zero
//     count rather than inferred from a timeout.
//   - req: request/response frames answered to one client (the hello bundle,
//     scan.resync full ticks, row.get, trend.get, chart.get, errors, ...).
//
// Recorded: testdata/golden_wire.txt (sha256, length, name per frame) is what the
// test compares; testdata/golden_wire_frames.jsonl carries the full bytes of
// every frame (one {"name","frame"} object per line) so a failure can show where
// the bytes diverge and a reviewer can diff an intended change.
//
//	go test ./internal/server -run TestGoldenWire                   (check)
//	go test ./internal/server -run TestGoldenWire -update-golden    (regenerate)
//
// Known behaviour pinned on purpose: json.Marshal refuses NaN and +-Inf, and
// mustFrame ignores that error, so ONE such value in ONE row turns the whole
// scan.tick into the data-less envelope {"t":"scan.tick"} (see the *-nonfinite
// steps). The scan layer maps NaN to null before it gets here (and finiteOrNull
// guards the e9 levels), but a rewritten encoder must reproduce this too, or the
// change has to be called out.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/golden_wire.txt and testdata/golden_wire_frames.jsonl from the current encoder")

const (
	goldenSumsFile   = "testdata/golden_wire.txt"
	goldenFramesFile = "testdata/golden_wire_frames.jsonl"
)

// ─── deterministic content ───────────────────────────────────────────────────

// detRand is a splitmix64 generator: the golden content must not depend on
// math/rand's stream or on the clock.
type detRand struct{ s uint64 }

func (r *detRand) next() uint64 {
	r.s += 0x9e3779b97f4a7c15
	z := r.s
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// float is uniform in [0,1).
func (r *detRand) float() float64 { return float64(r.next()>>11) / (1 << 53) }

func ptr[T any](v T) *T { return &v }

// wireSym is a unique 4-letter symbol per (i, salt): 7919 is coprime to 26^4, so
// i -> i*7919 mod 26^4 is a bijection for i < 26^4.
func wireSym(i int, salt uint64) string {
	n := (uint64(i)*7919 + salt*131) % (26 * 26 * 26 * 26)
	b := [4]byte{}
	for k := 3; k >= 0; k-- {
		b[k] = byte('A' + n%26)
		n /= 26
	}
	return string(b[:])
}

// f32 rounds through float32 the way engine prices reach the wire (the ticker
// state is float32), which is what gives live rows their long 17-digit floats.
func f32(v float64) float64 { return float64(float32(v)) }

// wireCols8 is the column set of a typical live scan row on the wire: the six
// default-ish columns plus the sidebox extras.
var wireCols8 = []string{"last", "chg5m", "chgDay", "vol", "rvol", "news", "newsAge", "pmVolX"}

// wireBars is the inline-chart series a row carries when its scan shows the
// chart column.
func wireBars(r *detRand, n int, base float64) []core.Bar {
	bars := make([]core.Bar, n)
	t0 := int64(1_791_000_000_000)
	px := base
	for i := range bars {
		o := px
		c := o * (1 + (r.float()-0.5)*0.02)
		h := math.Max(o, c) * (1 + r.float()*0.004)
		l := math.Min(o, c) * (1 - r.float()*0.004)
		bars[i] = core.Bar{T: t0 + int64(i)*300_000, O: f32(o), H: f32(h), L: f32(l), C: f32(c), V: int64(r.next() % 4_000_000)}
		px = c
	}
	return bars
}

// wireRow builds one wire row over cols with the value kinds the scan layer
// really emits (fieldValue): float64 for the numeric fields, int for the
// discrete ones, bool, string, nil for null, and []core.Bar for the chart.
func wireRow(r *detRand, cols []string) core.RowJSON {
	m := make(core.RowJSON, len(cols)+1)
	last := f32(0.2 + r.float()*180)
	for _, c := range cols {
		switch c {
		case "last":
			m[c] = last
		case "chg5m", "chgDay", "chg1m", "chgSess":
			m[c] = f32((r.float() - 0.3) * 24)
		case "vol", "sessVol", "avgVol":
			m[c] = float64(r.next() % 90_000_000)
		case "rvol":
			m[c] = r.float() * 14
		case "news":
			m[c] = r.next()%5 == 0
		case "newsAge":
			if r.next()%5 == 0 {
				m[c] = int(r.next() % 7200)
			} else {
				m[c] = nil
			}
		case "pmVolX", "dbPct", "pdlPct":
			if r.next()%3 == 0 {
				m[c] = r.float() * 9
			} else {
				m[c] = nil
			}
		case "dtrend", "hits", "flagState":
			m[c] = int(r.next() % 3)
		case "session":
			m[c] = [...]string{"pre", "regular", "post"}[r.next()%3]
		case "secType":
			m[c] = "CS"
		case "halted":
			m[c] = false
		case "float", "shsOut":
			m[c] = int64(r.next() % 400_000_000)
		case "chart":
			m["bars"] = wireBars(r, 24, last)
		default:
			m[c] = f32(r.float() * 100)
		}
	}
	return m
}

// wireTick builds a tick over nOrder ordered symbols carrying nRows rows
// (evenly spaced through the order, like a changed-only delta).
func wireTick(id string, seq uint64, full bool, nOrder, nRows int, cols []string, seed uint64) core.ScanTick {
	r := &detRand{s: seed}
	order := make([]string, nOrder)
	for i := range order {
		order[i] = wireSym(i, seed)
	}
	rows := make(map[string]core.RowJSON, nRows)
	for j := 0; j < nRows; j++ {
		sym := order[j*nOrder/nRows]
		rows[sym] = wireRow(r, cols)
	}
	return core.ScanTick{ID: id, Seq: seq, AsOf: 1_791_039_600_000 + int64(seq)*100, Full: full, Order: order, Rows: rows}
}

// wireExoticRows is a tick body with every value kind and encoder pitfall the
// wire could ever carry: float formatting edges, integer widths, escapes,
// invalid UTF-8, typed nils, nested containers, map-key ordering.
func wireExoticRows() ([]string, map[string]core.RowJSON) {
	a, b := 0.1, 0.2
	return []string{"KINDS", "A&B", "C<D>", "EMPTY", "NILROW"}, map[string]core.RowJSON{
		"KINDS": {
			"f64":      12.329999923706055,
			"f64whole": 1234567.0,
			"f64sum":   a + b,
			"f64neg0":  math.Copysign(0, -1),
			"f64zero":  0.0,
			"f64tiny":  5e-324,
			"f64e-6":   1e-6,
			"f64e-7":   1e-7,
			"f64e20":   1e20,
			"f64e21":   1e21,
			"f64big":   math.MaxFloat64,
			"f64neg":   -273.15,
			"f32":      float32(0.1),
			"f32big":   float32(16777216),
			"int":      -42,
			"int64max": int64(math.MaxInt64),
			"int64min": int64(math.MinInt64),
			"uint64":   uint64(math.MaxUint64),
			"intzero":  0,
			"boolT":    true,
			"boolF":    false,
			"nil":      nil,
			"nilptr":   (*float64)(nil),
			"ptr":      ptr(3.25),
			"str":      "plain",
			"strempty": "",
			"number":   json.Number("12.50"),
			"raw":      json.RawMessage("[1, 2,\n 3]"),
		},
		"A&B": {
			"quote":    `a"b\c`,
			"html":     "<b>R&D</b> 'x'",
			"ctrl":     "tab\tnl\ncr\r\x00\x1f\x7f",
			"lsep":     "a\u2028b\u2029c",
			"unicode":  "caf\u00e9 \u4e16\u754c \U0001F680",
			"badutf8":  "bad\xffutf8\xc3",
			"Zed":      1,
			"alpha":    2,
			"beta2":    3,
			"beta10":   4,
			"_under":   5,
			"\u00e9":   6,
			"k\"q":     7,
			"<tag>":    8,
			"":         9,
			"bars":     []core.Bar{{T: 1_791_000_000_000, O: 1.5, H: 2, L: 1.25, C: 1.75, V: 1234}, {T: 1_791_000_300_000, O: 1.75, H: 1.75, L: 1.75, C: 1.75, V: 0}},
			"barsNone": []core.Bar{},
			"barsNil":  []core.Bar(nil),
		},
		"C<D>": {
			"floats":  []float64{1.5, 2, 1e21, 0.1},
			"strs":    []string{"x", "<y>", ""},
			"anys":    []any{1, "two", 3.5, nil, true},
			"nested":  map[string]any{"b": 2, "a": []any{1.0, map[string]any{"z": nil, "y": "é"}}},
			"emptyM":  map[string]any{},
			"emptyS":  []any{},
			"nilSl":   []string(nil),
			"strKeys": map[string]string{"k2": "v2", "k1": "v1"},
		},
		"EMPTY":  {},
		"NILROW": nil,
	}
}

// ─── recording ───────────────────────────────────────────────────────────────

type goldenFrame struct {
	name string
	b    []byte
}

type goldenSet struct {
	t      *testing.T
	frames []goldenFrame
	seen   map[string]bool
}

func newGoldenSet(t *testing.T) *goldenSet { return &goldenSet{t: t, seen: map[string]bool{}} }

func (g *goldenSet) add(name string, b []byte) {
	g.t.Helper()
	if g.seen[name] {
		g.t.Fatalf("golden frame name used twice: %s", name)
	}
	g.seen[name] = true
	g.frames = append(g.frames, goldenFrame{name: name, b: append([]byte(nil), b...)})
}

// addStep records everything one client received in one step: a count entry
// (so "received nothing" is pinned) and one entry per frame.
func (g *goldenSet) addStep(step, client string, frames [][]byte) {
	g.t.Helper()
	g.add(fmt.Sprintf("%s/%s/n", step, client), []byte(fmt.Sprintf(`{"frames":%d}`, len(frames))))
	for k, f := range frames {
		g.add(fmt.Sprintf("%s/%s/%02d", step, client, k), f)
	}
}

func sumLine(g goldenFrame) string {
	h := sha256.Sum256(g.b)
	return fmt.Sprintf("%s\t%d\t%s", hex.EncodeToString(h[:]), len(g.b), g.name)
}

const goldenHeader = "# sha256 <TAB> bytes <TAB> frame name. Every WS frame the hub sends in the scripted session of golden_wire_test.go.\n" +
	"# Regenerate (only for an INTENDED wire change): go test ./internal/server -run TestGoldenWire -update-golden\n"

func renderSums(frames []goldenFrame) []byte {
	var sb strings.Builder
	sb.WriteString(goldenHeader)
	for _, f := range frames {
		sb.WriteString(sumLine(f))
		sb.WriteByte('\n')
	}
	return []byte(sb.String())
}

func renderFrames(t *testing.T, frames []goldenFrame) []byte {
	var buf bytes.Buffer
	for _, f := range frames {
		if !json.Valid(f.b) {
			t.Fatalf("frame %s is not valid JSON (cannot be embedded in the jsonl): %q", f.name, f.b)
		}
		name, _ := json.Marshal(f.name)
		buf.WriteString(`{"name":`)
		buf.Write(name)
		buf.WriteString(`,"frame":`)
		buf.Write(f.b)
		buf.WriteString("}\n")
	}
	return buf.Bytes()
}

// loadGoldenFrames reads the jsonl back (best effort: it only serves the diff
// display, the pass/fail verdict comes from the sums file).
func loadGoldenFrames() map[string][]byte {
	raw, err := os.ReadFile(filepath.FromSlash(goldenFramesFile))
	if err != nil {
		return nil
	}
	out := map[string][]byte{}
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec struct {
			Name  string          `json:"name"`
			Frame json.RawMessage `json:"frame"`
		}
		if json.Unmarshal(line, &rec) == nil {
			out[rec.Name] = []byte(rec.Frame)
		}
	}
	return out
}

// firstDiff is the index of the first differing byte (or the shorter length).
func firstDiff(a, b []byte) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func excerpt(b []byte, at int) string {
	lo, hi := max(at-50, 0), min(at+70, len(b))
	return strconv.Quote(string(b[lo:hi]))
}

func checkGolden(t *testing.T, frames []goldenFrame) {
	t.Helper()
	sums, jsonl := renderSums(frames), renderFrames(t, frames)
	sumsPath, framesPath := filepath.FromSlash(goldenSumsFile), filepath.FromSlash(goldenFramesFile)
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(sumsPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sumsPath, sums, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(framesPath, jsonl, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("golden rewritten: %d frames, %d bytes of sums, %d bytes of frames", len(frames), len(sums), len(jsonl))
		return
	}
	want, err := os.ReadFile(sumsPath)
	if err != nil {
		t.Fatalf("no golden file (%v): generate it with: go test ./internal/server -run TestGoldenWire -update-golden", err)
	}
	wantSum := map[string]string{} // name -> "sha\tlen"
	var wantOrder []string
	for _, line := range strings.Split(string(want), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			t.Fatalf("malformed golden line %q", line)
		}
		wantSum[parts[2]] = parts[0] + "\t" + parts[1]
		wantOrder = append(wantOrder, parts[2])
	}
	old := loadGoldenFrames()

	got := map[string]goldenFrame{}
	var bad []string
	for _, f := range frames {
		got[f.name] = f
		ws, ok := wantSum[f.name]
		if !ok {
			bad = append(bad, fmt.Sprintf("NEW      %s (%d bytes, not in the golden file)", f.name, len(f.b)))
			continue
		}
		h := sha256.Sum256(f.b)
		if gs := hex.EncodeToString(h[:]) + "\t" + strconv.Itoa(len(f.b)); gs != ws {
			msg := fmt.Sprintf("CHANGED  %s: want %s, got %s", f.name, strings.ReplaceAll(ws, "\t", " bytes="), strings.ReplaceAll(gs, "\t", " bytes="))
			if o, ok := old[f.name]; ok {
				at := firstDiff(o, f.b)
				msg += fmt.Sprintf("\n         first difference at byte %d\n         want ...%s\n         got  ...%s", at, excerpt(o, at), excerpt(f.b, at))
			}
			bad = append(bad, msg)
		}
	}
	for _, name := range wantOrder {
		if _, ok := got[name]; !ok {
			bad = append(bad, fmt.Sprintf("MISSING  %s (in the golden file, not produced now)", name))
		}
	}
	if len(bad) == 0 {
		return
	}
	shown := bad
	if len(shown) > 12 {
		shown = shown[:12]
	}
	t.Fatalf("wire encoding differs from testdata/golden_wire.txt in %d of %d frames:\n%s\n%s",
		len(bad), len(frames), strings.Join(shown, "\n"),
		"If this wire change is intended, review it against testdata/golden_wire_frames.jsonl and run: go test ./internal/server -run TestGoldenWire -update-golden")
}

// ─── harness ─────────────────────────────────────────────────────────────────

const wireTimeout = 5 * time.Second

// fenceID marks the end of a step: a scan.enter broadcast to everybody.
const fenceID = "__fence__"

var fenceMark = []byte(`"scanId":"` + fenceID + `"`)

// put sends v on an unbuffered channel the broadcast loop reads; the send
// returns once the loop has taken the value.
func put[T any](t *testing.T, ch chan<- T, v T) {
	t.Helper()
	select {
	case ch <- v:
	case <-time.After(wireTimeout):
		t.Fatalf("broadcast loop did not take %T within %v", v, wireTimeout)
	}
}

// dialBig is dialWS with a read limit above the biggest golden frame (the
// library default of 32 KiB is below a full 120-row tick).
func dialBig(t *testing.T, ctx context.Context, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	c := dialWS(t, ctx, ts)
	c.SetReadLimit(16 << 20)
	t.Cleanup(func() { c.CloseNow() })
	return c
}

func readFrameBytes(t *testing.T, ctx context.Context, c *websocket.Conn, what string) []byte {
	t.Helper()
	rctx, cancel := context.WithTimeout(ctx, wireTimeout)
	defer cancel()
	_, data, err := c.Read(rctx)
	if err != nil {
		t.Fatalf("read (%s): %v", what, err)
	}
	return data
}

func frameType(t *testing.T, b []byte) string {
	t.Helper()
	var f core.Frame
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("frame is not an envelope: %v: %q", err, b)
	}
	return f.T
}

// syncConn round-trips a hello so every earlier frame of this connection has
// been handled (frames of one connection are processed in order), consuming the
// hello bundle's n frames (scan.list first, then whatever the config adds:
// status, blacklist, watchlist).
func syncConn(t *testing.T, ctx context.Context, c *websocket.Conn, n int) {
	t.Helper()
	writeFrame(t, ctx, c, core.THello, nil)
	for i := 0; i < n; i++ {
		b := readFrameBytes(t, ctx, c, "sync")
		if i == 0 && frameType(t, b) != core.TScanList {
			t.Fatalf("sync: hello bundle starts with %s, want scan.list", frameType(t, b))
		}
	}
}

// helloFrames is the size of the hello bundle in the hub rig (scan.list and
// blacklist: no Status or WatchlistLoad configured).
const helloFrames = 2

// hubRig is the hub script's environment: a Started server with unbuffered
// fakes and four clients.
//
//	c1  empty view (wants every scan), no tape symbol
//	c2  view {gap}
//	c3  view {mom},       tape-watching MSFT
//	c4  view {gap, mom},  tape-watching AAPL
type hubRig struct {
	t   *testing.T
	ctx context.Context
	g   *goldenSet

	srv       *Server
	fs        *fakeScanner
	entries   chan core.ScanEntry
	summaries chan core.NewsSummaryMsg
	stats     chan core.TapeStat
	prints    chan core.TapePrintsMsg
	readouts  chan core.TapeReadoutMsg
	regimes   chan core.TapeRegimeMsg
	cards     chan core.TapeCardMsg
	setups    chan core.SetupReadMsg
	bars      chan core.ChartBarMsg
	short     chan core.ShortLive
	news      chan []core.NewsItem
	scores    chan core.NewsScoreMsg
	blChanged chan struct{}
	clock     atomic.Int64 // the server's clock (tws.auto frames carry it)

	names []string
	conns []*websocket.Conn
}

func newHubRig(t *testing.T, g *goldenSet) *hubRig {
	t.Helper()
	r := &hubRig{t: t, g: g, fs: newFake(),
		entries:   make(chan core.ScanEntry),
		summaries: make(chan core.NewsSummaryMsg),
		stats:     make(chan core.TapeStat),
		prints:    make(chan core.TapePrintsMsg),
		readouts:  make(chan core.TapeReadoutMsg),
		regimes:   make(chan core.TapeRegimeMsg),
		cards:     make(chan core.TapeCardMsg),
		setups:    make(chan core.SetupReadMsg),
		bars:      make(chan core.ChartBarMsg),
		short:     make(chan core.ShortLive),
		news:      make(chan []core.NewsItem),
		scores:    make(chan core.NewsScoreMsg),
		blChanged: make(chan struct{}),
	}
	r.fs.ticks = make(chan core.ScanTick)
	r.fs.alerts = make(chan core.AlertMsg)
	r.fs.defs = []core.ScanDef{
		{ID: "dildo", Name: "Green Dildo", Enabled: true, Alert: core.AlertConfig{Enabled: true, On: core.AlertOnEnter, SendToTWS: true}},
		{ID: "gap", Name: "Gap", Enabled: true, Alert: core.AlertConfig{Enabled: true, On: core.AlertOnEnter}},
	}
	r.clock.Store(time.Date(2026, 10, 26, 13, 30, 0, 0, time.UTC).UnixMilli())

	srv := New(Config{
		Scanner: r.fs, Entries: r.entries, Summaries: r.summaries,
		TapeStats: r.stats, TapePrints: r.prints, TapeReadouts: r.readouts,
		TapeRegimes: r.regimes, TapeCards: r.cards, SetupReads: r.setups,
		ChartBars: r.bars, ShortLive: r.short, News: r.news, NewsScores: r.scores,
		BlacklistView:    func() []string { return []string{"BADD", "WXYZ"} },
		BlacklistChanged: r.blChanged,
		AlertToTWS:       func(string) {},
	})
	srv.now = func() time.Time { return time.UnixMilli(r.clock.Load()) }
	r.srv = srv
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.ctx = ctx
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	for _, name := range []string{"c1", "c2", "c3", "c4"} {
		r.names = append(r.names, name)
		r.conns = append(r.conns, dialBig(t, ctx, ts))
	}
	// One client at a time: tape.watch from two connections is handled on two
	// goroutines, and the TWS guard remembers whichever was handled last.
	writeFrame(t, ctx, r.conns[1], core.TViewSet, core.ViewSetMsg{ScanIDs: []string{"gap"}})
	syncConn(t, ctx, r.conns[1], helloFrames)
	writeFrame(t, ctx, r.conns[2], core.TViewSet, core.ViewSetMsg{ScanIDs: []string{"mom"}})
	writeFrame(t, ctx, r.conns[2], core.TTapeWatch, core.TapeWatchMsg{Sym: "MSFT"})
	syncConn(t, ctx, r.conns[2], helloFrames)
	writeFrame(t, ctx, r.conns[3], core.TViewSet, core.ViewSetMsg{ScanIDs: []string{"gap", "mom"}})
	writeFrame(t, ctx, r.conns[3], core.TTapeWatch, core.TapeWatchMsg{Sym: "AAPL"})
	syncConn(t, ctx, r.conns[3], helloFrames)
	syncConn(t, ctx, r.conns[0], helloFrames)
	return r
}

// collect reads one client's frames up to the step's fence, and until each type
// in extra (frames produced on another goroutine, e.g. tws.auto, which may land
// before or after the fence) has been seen once. The fence itself is not
// recorded.
func (r *hubRig) collect(c *websocket.Conn, extra []string) [][]byte {
	r.t.Helper()
	need := map[string]int{}
	for _, e := range extra {
		need[e]++
	}
	var frames [][]byte
	fenced := false
	for !fenced || len(need) > 0 {
		data := readFrameBytes(r.t, r.ctx, c, "step")
		if bytes.Contains(data, fenceMark) {
			fenced = true
			continue
		}
		frames = append(frames, data)
		if typ := frameType(r.t, data); need[typ] > 0 {
			if need[typ]--; need[typ] == 0 {
				delete(need, typ)
			}
		}
	}
	return frames
}

// step injects events via send, fences, and records what each client received.
func (r *hubRig) step(name string, send func(), extra ...string) {
	r.t.Helper()
	send()
	put(r.t, r.entries, core.ScanEntry{ScanID: fenceID})
	for i, c := range r.conns {
		r.g.addStep("hub/"+name, r.names[i], r.collect(c, extra))
	}
}

func (r *hubRig) tick(t core.ScanTick) func()  { return func() { put(r.t, r.fs.ticks, t) } }
func (r *hubRig) alert(a core.AlertMsg) func() { return func() { put(r.t, r.fs.alerts, a) } }

func (r *hubRig) setView(i int, ids ...string) {
	r.t.Helper()
	writeFrame(r.t, r.ctx, r.conns[i], core.TViewSet, core.ViewSetMsg{ScanIDs: ids})
	syncConn(r.t, r.ctx, r.conns[i], helloFrames)
}

// ─── payload builders for the broadcast frame types ──────────────────────────

func wireAlertRow(seed uint64) core.RowJSON {
	r := &detRand{s: seed}
	return wireRow(r, append(append([]string(nil), wireCols8...), "session", "secType", "dtrend", "hits"))
}

func wireTapeStat(seed uint64) core.TapeStat {
	r := &detRand{s: seed}
	prints := make([]core.Print, 12)
	for i := range prints {
		sz := int64(100 + r.next()%5000)
		px := f32(10 + r.float())
		prints[i] = core.Print{TS: 1_791_039_600_000 + int64(i)*350, Price: px, Size: sz,
			Side: [...]string{"buy", "sell", "neutral"}[r.next()%3], Notional: float64(sz) * px}
	}
	cvd := make([]core.CVDPoint, 30)
	for i := range cvd {
		cvd[i] = core.CVDPoint{Ts: 1_791_039_000_000 + int64(i)*10_000, Value: int64(r.next()%200_000) - 100_000}
	}
	return core.TapeStat{
		Sym: "AAPL", AsOf: 1_791_039_600_000, Watching: true, WatchSecs: 312, QuoteRule: true,
		Bid: 10.41, Ask: 10.43, Delta1m: -12_500, Delta5m: 88_000, DeltaTotal: 420_000,
		BuyPct5m: 61.25, SellPct5m: 38.75, AvgSize5m: 212.5, Rate1m: 3.4, TotalTrades: 1840,
		Prints: prints, Control: 0.42,
		Delta10s: 4_300, Ratio1s: 0.1, Ratio10s: -0.2, Ratio60s: 0.35, PrintsPerSec: 4, PrintsPerSecBaseline: 2.75,
		BlockThreshold: 5_000, MedianSize: 150, CVDSeries: cvd,
		Book: core.BookStats{BidAbsorb: 3, AskAbsorb: 1, BidAdd: 4, BidPull: 2, BidUp: 5, AskDown: 1,
			BidRefills: 2, BidAbsorbedVol: 12_000.5, BidDepth: 55_000, AskDepth: 41_000, DepthImbalance: 0.15, InsideImbalance: -0.05},
		Verdict:       core.TapeVerdict{Control: "buyers", Confidence: 0.62, Text: "buyers absorbing <the> offer & lifting", Evidence: []string{"bid refilled 2x", "delta +88k"}, Source: "local"},
		ClaudeEnabled: true, CommentarySecs: 30, HasDepth: true,
		BookBids: []core.DepthLevel{{Price: 10.41, Size: 1200}, {Price: 10.40, Size: 3400}},
		BookAsks: []core.DepthLevel{{Price: 10.43, Size: 900}, {Price: 10.44, Size: 2100}},
	}
}

func wirePrints(sym string, n int) core.TapePrintsMsg {
	r := &detRand{s: 77}
	ps := make([]core.Print, n)
	for i := range ps {
		sz := int64(1 + r.next()%900)
		px := f32(25 + r.float())
		ps[i] = core.Print{TS: 1_791_039_600_000 + int64(i)*40, Price: px, Size: sz,
			Side: [...]string{"buy", "sell", "neutral"}[r.next()%3], Notional: float64(sz) * px}
	}
	return core.TapePrintsMsg{Sym: sym, Prints: ps}
}

func wireNews() []core.NewsItem {
	return []core.NewsItem{
		{ID: "bz-1", Tickers: []string{"AAPL", "MSFT"}, Headline: "Apple <b>beats</b> & raises \"guidance\"", Source: "Benzinga",
			URL: "https://example.com/a?x=1&y=2", TS: 1_791_039_000_000, Sentiment: "positive", Teaser: "Short teaser", HasBody: true,
			Score: ptr(87), ScoreTier: "high", EventType: "earnings", Direction: "bullish"},
		{ID: "rt-2", Tickers: []string{"XYZW"}, Headline: "Biotech halts trial \u2014 shares fall", Source: "RTPR",
			URL: "https://example.com/b", TS: 1_791_039_300_000, Sentiment: "negative"},
		{ID: "sec-3", Tickers: nil, Headline: "", Source: "EDGAR", URL: "", TS: 0, Sentiment: ""},
	}
}

// ─── the hub script ──────────────────────────────────────────────────────────

func (r *hubRig) script() {
	exOrder, exRows := wireExoticRows()

	// scan.tick, every shape the scanner emits and the edge cases around them.
	r.step("tick-01-gap-50x8", r.tick(wireTick("gap", 1, false, 50, 50, wireCols8, 11)))
	r.step("tick-02-gap-delta-7of50", r.tick(wireTick("gap", 2, false, 50, 7, wireCols8, 12)))
	r.step("tick-03-mom-chart-bars", r.tick(wireTick("mom", 1, false, 20, 10, append([]string{"chart", "dtrend", "session", "secType"}, wireCols8...), 13)))
	r.step("tick-04-gap-order-only", r.tick(core.ScanTick{ID: "gap", Seq: 3, AsOf: 1_791_039_600_300, Order: wireTick("gap", 3, false, 50, 1, wireCols8, 11).Order, Rows: map[string]core.RowJSON{}}))
	r.step("tick-05-mom-order-only-500", r.tick(core.ScanTick{ID: "mom", Seq: 2, AsOf: 1_791_039_600_400, Order: wireTick("mom", 2, false, 500, 1, wireCols8, 14).Order, Rows: map[string]core.RowJSON{}}))
	r.step("tick-06-gap-empty-nonnil", r.tick(core.ScanTick{ID: "gap", Seq: 4, AsOf: 1_791_039_600_500, Order: []string{}, Rows: map[string]core.RowJSON{}}))
	r.step("tick-07-gap-nil-order-rows", r.tick(core.ScanTick{ID: "gap", Seq: 5, AsOf: 1_791_039_600_600}))
	r.step("tick-08-gap-full-resync-120x8", r.tick(wireTick("gap", 6, true, 120, 120, wireCols8, 15)))
	r.step("tick-09-mom-full-with-nulls", r.tick(func() core.ScanTick {
		tk := wireTick("mom", 3, true, 30, 30, wireCols8, 16)
		i := 0
		for _, sym := range tk.Order { // null out a third of every row's fields
			row := tk.Rows[sym]
			keys := make([]string, 0, len(row))
			for k := range row {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if i%3 == 0 {
					row[k] = nil
				}
				i++
			}
		}
		return tk
	}()))
	r.step("tick-10-exotic-values", r.tick(core.ScanTick{ID: "gap", Seq: 7, AsOf: 1_791_039_600_700, Order: exOrder, Rows: exRows}))
	r.step("tick-11-nonfinite-nan", r.tick(core.ScanTick{ID: "gap", Seq: 8, AsOf: 1_791_039_600_800, Order: []string{"NANN", "OKAY"},
		Rows: map[string]core.RowJSON{"NANN": {"last": math.NaN()}, "OKAY": {"last": 1.5}}}))
	r.step("tick-12-nonfinite-inf", r.tick(core.ScanTick{ID: "mom", Seq: 4, AsOf: 1_791_039_600_900, Order: []string{"INFF"},
		Rows: map[string]core.RowJSON{"INFF": {"last": math.Inf(1), "chgDay": math.Inf(-1)}}}))
	r.step("tick-13-gap-big-seq-asof", r.tick(core.ScanTick{ID: "gap", Seq: math.MaxUint64, AsOf: math.MaxInt64, Order: []string{"BIGG"},
		Rows: map[string]core.RowJSON{"BIGG": {"last": 1.0}}}))

	// Routing: an unviewed scan reaches only the clients that want everything;
	// when c1 narrows its view too, it reaches nobody (the encode is skipped).
	r.step("route-01-solo-reaches-only-c1", r.tick(wireTick("solo", 1, false, 5, 5, wireCols8, 21)))
	r.setView(0, "gap")
	r.step("route-02-solo-reaches-nobody", r.tick(wireTick("solo", 2, false, 5, 5, wireCols8, 22)))
	r.step("route-03-gap-after-narrow", r.tick(wireTick("gap", 9, false, 10, 3, wireCols8, 23)))
	r.setView(0) // empty view = wants every scan again
	r.step("route-04-mom-after-widen", r.tick(wireTick("mom", 6, false, 10, 3, wireCols8, 24)))

	// alert
	r.step("alert-01-gap-enter", r.alert(core.AlertMsg{ScanID: "gap", Sym: "ABCD", Kind: "enter", At: 1_791_039_601_000, Sound: "ping", Row: wireAlertRow(31)}))
	r.step("alert-02-mom-cross-nil-row", r.alert(core.AlertMsg{ScanID: "mom", Sym: "WXYZ", Kind: "cross", At: 1_791_039_602_000, Sound: ""}))
	r.step("alert-03-gap-cross-exotic-row", r.alert(core.AlertMsg{ScanID: "gap", Sym: "A&B", Kind: "cross", At: 1_791_039_603_000, Sound: "bell<1>", Row: exRows["A&B"]}))
	r.step("alert-04-solo-only-c1", r.alert(core.AlertMsg{ScanID: "solo", Sym: "ONLY", Kind: "enter", At: 1_791_039_604_000, Sound: "ping", Row: core.RowJSON{"last": 3.0}}))

	// tws.auto: an alert of the flagged scan, then the three guard outcomes. The
	// frame is produced on runTwsAuto's goroutine, so the step waits for it.
	// (c3/c4 picked MSFT/AAPL when they connected; the server clock stands still.)
	r.step("tws-01-skipped-user-pick", r.alert(core.AlertMsg{ScanID: "dildo", Sym: "ABCD", Kind: "enter", At: 1_791_039_605_000, Sound: "ping", Row: core.RowJSON{"last": 4.5}}), core.TTwsAuto)
	r.clock.Add(30_000)
	r.step("tws-02-sent", r.alert(core.AlertMsg{ScanID: "dildo", Sym: "EFGH", Kind: "enter", At: 1_791_039_635_000, Sound: "ping", Row: core.RowJSON{"last": 5.5}}), core.TTwsAuto)
	r.clock.Add(1_000)
	r.step("tws-03-skipped-min-gap", r.alert(core.AlertMsg{ScanID: "dildo", Sym: "IJKL", Kind: "enter", At: 1_791_039_636_000, Sound: "ping", Row: core.RowJSON{"last": 6.5}}), core.TTwsAuto)

	// fanoutAll feeds
	r.step("enter-01", func() {
		put(r.t, r.entries, core.ScanEntry{ScanID: "gap", Sym: "ABCD", At: 1_791_039_601_000, Last: f32(12.34), HasNews: true, Hits: 2})
	})
	r.step("enter-02-zero", func() { put(r.t, r.entries, core.ScanEntry{ScanID: "mom"}) })
	r.step("summary-ok", func() {
		put(r.t, r.summaries, core.NewsSummaryMsg{ID: "bz-1", Summary: "Apple raised guidance.\nTwo bullets \u2022 <ok>", Model: "local"})
	})
	r.step("summary-err", func() { put(r.t, r.summaries, core.NewsSummaryMsg{ID: "bz-9", Err: "claude: overloaded"}) })
	r.step("tape-stat-full", func() { put(r.t, r.stats, wireTapeStat(41)) })
	r.step("tape-stat-minimal", func() { put(r.t, r.stats, core.TapeStat{Sym: "IDLE", AsOf: 5}) })
	r.step("tape-readout-text", func() {
		put(r.t, r.readouts, core.TapeReadoutMsg{Sym: "AAPL", Text: "Buyers in control; bid refilling.", At: 1_791_039_606_000})
	})
	r.step("tape-readout-err", func() {
		put(r.t, r.readouts, core.TapeReadoutMsg{Sym: "AAPL", Err: "trades channel not entitled", At: 1_791_039_607_000})
	})
	r.step("tape-regime", func() {
		put(r.t, r.regimes, core.TapeRegimeMsg{Sym: "AAPL", Ts: 1_791_039_608_000, Trigger: "block_print", Headline: "Block print",
			Verdict: core.TapeVerdict{Control: "sellers", Confidence: 0.55, Text: "seller hitting the bid", Source: "local"}})
	})
	r.step("tape-card", func() {
		put(r.t, r.cards, core.TapeCardMsg{Sym: "AAPL", Ts: 1_791_039_609_000, Trigger: "regime_shift", Headline: "Control flips to buyers",
			Model: "claude-3", Control: "buyers", Confidence: 0.7, WhatsHappening: "Offer pulled, bid stacked.",
			Evidence: []string{"ask size 900 -> 0", "delta +88k"}, WatchFor: "a break of 10.45"})
	})
	r.step("setup-read", func() {
		put(r.t, r.setups, core.SetupReadMsg{Sym: "AAPL", Ts: 1_791_039_610_000, Quality: 4, Verdict: "textbook",
			WhatsHappening: "tight base under the pole high", Risks: []string{"thin volume", "<gap fill>"},
			Invalidation: "a close under 10.20", Model: "claude-3", AsOf: "2026-10-23"})
	})
	r.step("setup-read-failed", func() {
		put(r.t, r.setups, core.SetupReadMsg{Sym: "ZZZZ", Ts: 1_791_039_611_000, Err: "refused", Model: "claude-3", AsOf: "2026-10-23"})
	})
	r.step("chart-bar", func() {
		put(r.t, r.bars, core.ChartBarMsg{Sym: "AAPL", T: 1_791_039_600_000, O: f32(10.41), H: f32(10.5), L: f32(10.39), C: f32(10.44), V: 12_345, VWAP: 10.4312345678})
	})
	r.step("short-live", func() {
		put(r.t, r.short, core.ShortLive{Sym: "AAPL", Indicator: 3, Shares: 250_000, At: 1_791_039_612_000})
	})
	r.step("short-live-err", func() {
		put(r.t, r.short, core.ShortLive{Sym: "AAPL", Indicator: 0, Shares: -1, At: 1_791_039_613_000, Err: "no market data subscription"})
	})
	r.step("news-items", func() { put(r.t, r.news, wireNews()) })
	r.step("news-nil-items", func() { put(r.t, r.news, nil) })
	r.step("news-score", func() {
		put(r.t, r.scores, core.NewsScoreMsg{ArticleID: "bz-1", Sym: "AAPL", Score: 87, Tier: "high", EventType: "earnings", Direction: "bullish"})
	})

	// tape.prints is routed to the window watching that symbol only.
	r.step("prints-01-aapl-only-c4", func() { put(r.t, r.prints, wirePrints("AAPL", 25)) })
	r.step("prints-02-msft-only-c3", func() { put(r.t, r.prints, wirePrints("MSFT", 3)) })
	r.step("prints-03-nobody-watches", func() { put(r.t, r.prints, wirePrints("IDLE", 3)) })
	r.step("prints-04-empty-sym-dropped", func() { put(r.t, r.prints, wirePrints("", 3)) })
	r.step("prints-05-dropped-counter", func() {
		p := wirePrints("AAPL", 2)
		p.Dropped = 17
		put(r.t, r.prints, p)
	})

	// flagevents.add: PublishFlagEvents is called by the flag log from its own
	// goroutine, not through the select loop; it pushes to every client before it
	// returns, so the fence that follows still marks the end of the step.
	r.step("flagevents-add", func() {
		r.srv.PublishFlagEvents(core.FlagEventsMsg{Date: "2026-10-23", Events: []core.FlagEvent{
			{Ts: 1_791_039_000_000, At: 1_791_039_001_000, Sym: "ABCD", Kind: "entry", Label: "ENTRY", Price: ptr(f32(3.21))},
			{Ts: 1_791_039_060_000, At: 1_791_039_061_000, Sym: "EFGH", Kind: "dildo", Label: "GREEN DILDO", Note: "body 4.1% on 6.2x volume <ok>", Judged: 99},
			{Ts: 1_791_039_120_000, At: 1_791_039_121_000, Sym: "IJKL", Kind: "flat", Label: "FLAT"},
		}})
	})

	// blacklist: runBlacklist is its own goroutine, so wait for the frame by type.
	r.step("blacklist-changed", func() { put(r.t, r.blChanged, struct{}{}) }, core.TBlacklist)
}

func TestGoldenWire(t *testing.T) {
	g := newGoldenSet(t)

	hub := newHubRig(t, g)
	hub.script()

	runReqScript(t, g)

	checkGolden(t, g.frames)
}

// ─── the request/response script ─────────────────────────────────────────────

// reqRig drives one client against a server that is built but never Started:
// no broadcast loop and so no 1 Hz status ticker, only the handlers.
type reqRig struct {
	t    *testing.T
	ctx  context.Context
	conn *websocket.Conn
	g    *goldenSet
}

func (r *reqRig) do(name, typ string, payload any, n int) {
	r.t.Helper()
	r.doID(name, typ, 0, payload, n)
}

func (r *reqRig) doID(name, typ string, id int64, payload any, n int) {
	r.t.Helper()
	var raw json.RawMessage
	if payload != nil {
		b, _ := json.Marshal(payload)
		raw = b
	}
	b, _ := json.Marshal(core.Frame{T: typ, ID: id, D: raw})
	if err := r.conn.Write(r.ctx, websocket.MessageText, b); err != nil {
		r.t.Fatalf("write %s: %v", typ, err)
	}
	var frames [][]byte
	for i := 0; i < n; i++ {
		frames = append(frames, readFrameBytes(r.t, r.ctx, r.conn, name))
	}
	r.g.addStep("req/"+name, "c1", frames)
}

func runReqScript(t *testing.T, g *goldenSet) {
	t.Helper()
	fs := newFake()
	fs.defs = []core.ScanDef{
		{ID: "gap", Name: "Gap <5%>", Enabled: true, Limit: 50,
			Filter: core.Filter{Op: "and", Conds: []core.Filter{
				{Field: "chgDay", Cmp: core.CmpGT, Value: 5.0},
				{Field: "last", Cmp: core.CmpBetween, Value: []any{0.25, 25.0}},
				{Field: "secType", Cmp: core.CmpIn, Value: []any{"CS", "ADRC"}},
				{Op: "or", Conds: []core.Filter{{Field: "news", Cmp: core.CmpIsTrue}, {Field: "rvol", Cmp: core.CmpGTE, Value: 5.0}}},
			}},
			Sort:    []core.SortKey{{Field: "chgDay", Dir: "desc"}, {Field: "sym", Dir: "asc"}},
			Columns: []string{"last", "chgDay", "vol", "rvol", "news"},
			Alert:   core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: "ping", CooldownSec: 60, RequireReentry: true, Speak: true}},
		{ID: "cross", Name: "Cross", Enabled: false,
			Filter: core.Filter{Field: "last", Cmp: core.CmpLT, Value: 25},
			Alert:  core.AlertConfig{Enabled: true, On: core.AlertOnCross, Trigger: &core.Filter{Field: "chg5m", Cmp: core.CmpCrossAbove, Value: 3.5}, SendToTWS: true}},
	}
	fs.resyncOK = true
	fs.resyncTick = wireTick("gap", 41, true, 40, 40, wireCols8, 51)

	var statusN atomic.Int64
	status := func() core.StatusMsg {
		statusN.Add(1)
		return core.StatusMsg{Session: "regular", Feed: "live", Tracked: 10983, MsgRate: 4210, Baseline: "ready", HasKey: true,
			TwsLink: "linked", ClockMs: 1_791_039_600_000,
			Counts: map[string]int{"gap": 12, "mom": 0, "dildo-1m": 3},
			Sources: map[string]core.SourceHealth{
				"massive": {State: "ok", Since: 1_791_000_000_000, LastOK: 1_791_039_599_000},
				"rtpr":    {State: "failing", Msg: "Invalid or inactive API key", Since: 1_791_000_000_000},
				"edgar":   {State: "off"},
			}}
	}
	savedKey := ""
	srv := New(Config{
		Scanner: fs,
		Status:  status,
		SetKey: func(_ context.Context, key string) error {
			if key == "bad" {
				return errors.New("key rejected: <401>")
			}
			savedKey = key
			return nil
		},
		BlacklistView: func() []string { return []string{"BADD", "WXYZ"} },
		BlacklistSet: func(sym string, add bool) error {
			if sym == "FAIL" {
				return errors.New("cannot blacklist FAIL")
			}
			return nil
		},
		WatchlistLoad: func(context.Context) ([]string, error) { return []string{"AAPL", "TSLA", "NVDA"}, nil },
		WatchlistSave: func(context.Context, []string) error { return nil },
		GridLoad: func(context.Context) (string, error) {
			return "{ \"cols\": 3,\n  \"cells\": [ {\"sym\":\"AAPL\"}, {\"sym\":\"TSLA\"} ] }", nil
		},
		RowFor: func(sym string, cols []string) (core.RowJSON, bool) {
			if sym == "AAPL" {
				return core.RowJSON{"last": f32(189.37), "chg5m": f32(0.42), "vol": 51_000_000.0, "news": true, "pmVolX": nil}, true
			}
			return nil, false
		},
		TrendFor: func(sym string) (core.TrendRef, bool) {
			if sym == "AAPL" {
				return core.TrendRef{DTrend: 1, Chg20d: 7.25, VsSMA: 3.1, Days: 20, AsOf: "2026-10-23"}, true
			}
			return core.TrendRef{}, false
		},
		Chart: func(_ context.Context, sym string) ([]core.Bar, float64, error) {
			if sym == "ERRR" {
				return nil, 0, errors.New("upstream fetch failed")
			}
			return wireBars(&detRand{s: 61}, 12, 10), 9.87, nil
		},
		Fundamentals: func(_ context.Context, sym string) (core.Fundamentals, error) {
			switch sym {
			case "NOEN":
				return core.Fundamentals{}, fmt.Errorf("wrapped: %w", core.ErrNotEntitled)
			case "FAIL":
				return core.Fundamentals{}, errors.New("boom")
			}
			return core.Fundamentals{Sym: sym, AsOf: 1_790_000_000, Source: "massive_financials_v1",
				Quarters: []core.FinQuarter{
					{FiscalYear: 2026, FiscalQuarter: 2, PeriodEnd: "2026-06-30", FilingDate: "2026-08-05", Revenue: ptr(1_250_000.0), Cash: ptr(4_100_000.5), OperatingCF: ptr(-900_000.0)},
					{FiscalYear: 2026, FiscalQuarter: 1, PeriodEnd: "2026-03-31", FilingDate: "2026-05-06"},
				}, BurnQuarterly: ptr(900_000.0), RunwayMonths: ptr(13.7)}, nil
		},
		NewsFor: func(_ context.Context, sym string) ([]core.NewsItem, error) { return wireNews()[:2], nil },
		FlagEvents: func(_ context.Context, date string) (core.FlagEventsMsg, error) {
			return core.FlagEventsMsg{Date: "2026-10-23", Dates: []string{"2026-10-23", "2026-10-22"},
				Events: []core.FlagEvent{
					{Ts: 1_791_039_000_000, At: 1_791_039_001_000, Sym: "ABCD", Kind: "entry", Label: "ENTRY", Price: ptr(3.21)},
					{Ts: 1_791_039_060_000, At: 1_791_039_061_000, Sym: "EFGH", Kind: "dildo", Label: "GREEN DILDO", Note: "body 4.1% on 6.2x volume", Judged: 99},
				}}, nil
		},
		DayLog: func(_ context.Context, date string, snapshot bool) (core.DayLogMsg, error) {
			return core.DayLogMsg{Date: "2026-10-23", Dates: []string{"2026-10-23"}, CSVDir: `C:\Users\x\daylog`,
				Alerts: []core.DayAlert{{ID: 1, Day: "2026-10-23", At: 1_791_039_000_000, Sym: "ABCD", ScanID: "gap", ScanName: "Gap", Kind: "enter",
					Price: ptr(3.21), ChgDay: ptr(12.5), Vol: ptr(int64(1_200_000)), Rvol: ptr(6.5), NewsHL: "Halts <trial>", NewsAge: ptr(int64(120)), Note: "early dildo",
					Out5: ptr(1.5), Out15: ptr(-0.5), Out30: nil, MaxUp: ptr(4.0), MaxDown: ptr(-2.0), Done: true},
					{ID: 2, Day: "2026-10-23", At: 1_791_039_100_000, Sym: "EFGH", ScanID: "gap", ScanName: "Gap", Kind: "cross", Blacklisted: true}},
				Movers: []core.DayMover{{Day: "2026-10-23", Side: "gain", Rank: 1, Sym: "ABCD", Price: ptr(3.21), ChgDay: ptr(55.5), Vol: ptr(int64(9_000_000)), Headline: "x", TakenAt: 1_791_050_000_000}}}, nil
		},
		ReviewDates: func(context.Context) ([]string, error) { return []string{"2026-10-23", "2026-10-22"}, nil },
		ReviewLoad: func(_ context.Context, date string, sess core.Session) ([]core.SessionSnapshotRow, error) {
			return []core.SessionSnapshotRow{{Sym: "ABCD", Hi: 3.5, Lo: 3.0, Close: 3.4, Vwap: 3.25, Vol: 1_200_000, PrevClose: 3.1}}, nil
		},
		DildoParamsGet: func() []byte { return []byte(`{"minBodyPct": 3.5,  "volX": 4}`) },
		DildoParamsSet: func(_ context.Context, raw []byte) ([]byte, error) { return []byte(`{"minBodyPct":3.5,"volX":4}`), nil },
		AlertTWSSave:   func(context.Context, string) error { return nil },
		Summarize: func(_ context.Context, id string) (string, string, error) {
			if id == "bad" {
				return "", "", errors.New("no body cached")
			}
			return "Short summary.", "local", nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	r := &reqRig{t: t, ctx: ctx, g: g, conn: dialBig(t, ctx, ts)}

	r.do("hello", core.THello, nil, 4) // scan.list, status, blacklist, watchlist.data
	r.do("scan-resync-full", core.TScanResync, core.ScanRefMsg{ID: "gap"}, 1)
	r.do("scan-upsert-rebroadcasts-list", core.TScanUpsert, fs.defs[1], 1)
	r.do("scan-delete-rebroadcasts-list", core.TScanDelete, core.ScanRefMsg{ID: "nope"}, 1)
	r.do("row-get-known", core.TRowGet, core.RowGetMsg{Sym: "AAPL", Columns: []string{"last"}}, 1)
	r.do("row-get-unknown", core.TRowGet, core.RowGetMsg{Sym: "ZZZZ"}, 1)
	r.do("trend-get-known", core.TTrendGet, core.TrendGetMsg{Sym: "AAPL"}, 1)
	r.do("trend-get-unknown", core.TTrendGet, core.TrendGetMsg{Sym: "ZZZZ"}, 1)
	r.do("chart-get-ok", core.TChartGet, core.ChartGetMsg{Sym: "AAPL"}, 1)
	r.do("chart-get-error", core.TChartGet, core.ChartGetMsg{Sym: "ERRR"}, 1)
	r.do("fundamentals-ok", core.TFundamentalsGet, core.FundamentalsGetMsg{Sym: "ABCD"}, 1)
	r.do("fundamentals-not-entitled", core.TFundamentalsGet, core.FundamentalsGetMsg{Sym: "NOEN"}, 1)
	r.do("fundamentals-error", core.TFundamentalsGet, core.FundamentalsGetMsg{Sym: "FAIL"}, 1)
	r.do("news-for", core.TNewsFor, core.NewsForMsg{Sym: "AAPL"}, 1)
	r.do("news-summarize-ok", core.TNewsSummarize, core.NewsSummarizeMsg{ID: "bz-1"}, 1)
	r.do("news-summarize-err", core.TNewsSummarize, core.NewsSummarizeMsg{ID: "bad"}, 1)
	r.do("watchlist-get", core.TWatchlistGet, nil, 1)
	r.do("watchlist-set-broadcasts", core.TWatchlistSet, core.WatchlistMsg{Syms: []string{" aapl", "TSLA", "tsla", ""}}, 1)
	r.do("chartgrid-get-compacts-layout", core.TChartGridGet, nil, 1)
	r.do("flagevents-get", core.TFlagEventsGet, core.FlagEventsGetMsg{Date: "2026-10-23"}, 1)
	r.do("flagevents-bad-date", core.TFlagEventsGet, core.FlagEventsGetMsg{Date: "10/23"}, 1)
	r.do("daylog-get", core.TDayLogGet, core.DayLogGetMsg{Date: "2026-10-23"}, 1)
	r.do("daylog-bad-date", core.TDayLogGet, core.DayLogGetMsg{Date: "nope"}, 1)
	r.do("review-get-defaults", core.TReviewGet, core.ReviewGetMsg{}, 1)
	r.do("review-get-bad-session", core.TReviewGet, core.ReviewGetMsg{Session: "lunch"}, 1)
	r.do("alerttws-get", core.TAlertTWSGet, nil, 1)
	r.do("alerttws-set-broadcasts", core.TAlertTWSSet, core.AlertTWSMsg{Enabled: false}, 1)
	r.do("dildo-params-get-raw", core.TDildoParamsGet, nil, 1)
	r.do("dildo-params-set-broadcasts", core.TDildoParamsSet, json.RawMessage(`{"minBodyPct":3.5}`), 1)
	r.do("setkey-ok-broadcasts-status", core.TSetKey, core.SetKeyMsg{Key: "good"}, 1)
	r.do("setkey-error", core.TSetKey, core.SetKeyMsg{Key: "bad"}, 1)
	r.doID("blacklist-add-error-has-ref", core.TBlacklistAdd, 4242, core.BlacklistSymMsg{Sym: " fail "}, 1)
	r.doID("blacklist-add-empty-sym", core.TBlacklistAdd, 4243, core.BlacklistSymMsg{Sym: "  "}, 1)

	// Sanity: the harness really exercised the fakes it claims to.
	if savedKey != "good" {
		t.Fatalf("settings.setKey did not reach the SetKey callback (saved %q)", savedKey)
	}
	if statusN.Load() == 0 {
		t.Fatal("the Status callback was never called")
	}
}
