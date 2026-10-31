package newsfilter

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Everything here talks to a loopback httptest server; nothing in this package's
// tests ever reaches the real Newsfilter.

// testToken has every character that url.QueryEscape, url.PathEscape or JSON
// would spell differently, so a leak in ANY spelling shows up.
const testToken = `sec ret/+&=?%é"-TOKEN`

var t0 = time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)

type fakeClock struct{ ns atomic.Int64 }

func newFakeClock(t time.Time) *fakeClock {
	c := &fakeClock{}
	c.ns.Store(t.UnixNano())
	return c
}
func (c *fakeClock) Now() time.Time          { return time.Unix(0, c.ns.Load()).UTC() }
func (c *fakeClock) Advance(d time.Duration) { c.ns.Add(int64(d)) }

// logBuf is a goroutine-safe sink for the stream's slog output.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// fakeSleep stands in for the backoff wait: it records every duration and
// either returns at once or, with a gate, waits for the test to release it.
type fakeSleep struct {
	mu   sync.Mutex
	got  []time.Duration
	gate chan struct{}
}

func (f *fakeSleep) sleep(ctx context.Context, d time.Duration) bool {
	f.mu.Lock()
	f.got = append(f.got, d)
	f.mu.Unlock()
	if f.gate == nil {
		return ctx.Err() == nil
	}
	select {
	case <-f.gate:
		return true
	case <-ctx.Done():
		return false
	}
}

func (f *fakeSleep) durations() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.got...)
}

// rig is a Stream wired for tests: fake clock, captured log, fake backoff sleep.
type rig struct {
	t      *testing.T
	s      *Stream
	dir    string
	log    *logBuf
	clock  *fakeClock
	sl     *fakeSleep
	cancel context.CancelFunc
	done   chan error
}

func newRig(t *testing.T, urls []string, mut ...func(*Stream)) *rig {
	t.Helper()
	r := &rig{t: t, dir: t.TempDir(), log: &logBuf{}, clock: newFakeClock(t0), sl: &fakeSleep{}}
	r.s = New(Config{Token: testToken, Dir: r.dir, URLs: urls, Now: r.clock.Now})
	r.s.log = slog.New(slog.NewTextHandler(r.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r.s.pingMin = time.Millisecond
	r.s.sleep = r.sl.sleep
	for _, m := range mut {
		if m != nil {
			m(r.s)
		}
	}
	return r
}

// run starts Run in the background; stop (or test cleanup) cancels it and
// checks it returned nil.
func (r *rig) run() *rig {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan error, 1)
	go func() { r.done <- r.s.Run(ctx) }()
	r.t.Cleanup(func() { r.stop() })
	return r
}

func (r *rig) stop() {
	r.t.Helper()
	if r.cancel == nil {
		return
	}
	r.cancel()
	r.cancel = nil
	select {
	case err := <-r.done:
		if err != nil {
			r.t.Errorf("Run must return nil, got %v", err)
		}
	case <-time.After(10 * time.Second):
		r.t.Error("Run did not return after cancel")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// --- fake Newsfilter server ---

type nfServer struct {
	srv *httptest.Server
	mu  sync.Mutex
	n   int
	qs  []url.Values
}

// newNFServer serves the websocket endpoint; h runs once per accepted
// connection (n counts from 1).
func newNFServer(t *testing.T, h func(ctx context.Context, c *websocket.Conn, n int)) *nfServer {
	t.Helper()
	s := &nfServer{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.n++
		n := s.n
		s.qs = append(s.qs, r.URL.Query())
		s.mu.Unlock()
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		h(r.Context(), c, n)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *nfServer) conns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func (s *nfServer) query(i int) url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.qs[i]
}

// url is the endpoint without the key, as the Config wants it.
func (s *nfServer) url() string {
	return "ws" + strings.TrimPrefix(s.srv.URL, "http") + "/socket.io/?EIO=3&transport=websocket"
}

// statusServer answers every request with a fixed HTTP status and body.
func statusServer(t *testing.T, code int, body string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func wsURLOf(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/socket.io/?EIO=3&transport=websocket"
}

const helloFrame = `0{"sid":"sid1","upgrades":[],"pingInterval":25000,"pingTimeout":5000}`

func send(ctx context.Context, c *websocket.Conn, s string) { _ = sendErr(ctx, c, s) }

func sendErr(ctx context.Context, c *websocket.Conn, s string) error {
	return c.Write(ctx, websocket.MessageText, []byte(s))
}

func hello(ctx context.Context, c *websocket.Conn) {
	send(ctx, c, helloFrame)
	send(ctx, c, "40")
}

// serve reads client frames until the connection ends, handing each to on.
func serve(ctx context.Context, c *websocket.Conn, on func(msg string)) {
	for {
		_, m, err := c.Read(ctx)
		if err != nil {
			return
		}
		if on != nil {
			on(string(m))
		}
	}
}

// art renders an article object; extra keys override or add fields.
func art(id, title string, extra map[string]any) string {
	m := map[string]any{"id": id, "title": title, "sourceUrl": "https://example.test/" + id}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// evt wraps articles in a socket.io event frame.
func evt(arts ...string) string {
	return `42["articles",{"articles":[` + strings.Join(arts, ",") + `]}]`
}

// --- reading what the recorder wrote ---

type fileLine = map[string]any

// readDir returns every line of every newsfilter day file, oldest file first.
// A line that does not parse (a write still in flight while the stream runs)
// is skipped; readDirStrict is for assertions made after the stream stopped.
func readDir(dir string) []fileLine {
	names, _ := filepath.Glob(filepath.Join(dir, "newsfilter-*.jsonl"))
	sort.Strings(names)
	var out []fileLine
	for _, name := range names {
		out = append(out, readFileLines(name, nil)...)
	}
	return out
}

func readDirStrict(t *testing.T, dir string) []fileLine {
	t.Helper()
	names, _ := filepath.Glob(filepath.Join(dir, "newsfilter-*.jsonl"))
	sort.Strings(names)
	var out []fileLine
	for _, name := range names {
		out = append(out, readFileLines(name, t)...)
	}
	return out
}

// readFileLines parses one JSONL file; with a non-nil t a bad line fails it.
func readFileLines(path string, t *testing.T) []fileLine {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []fileLine
	for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if ln == "" {
			continue
		}
		var m fileLine
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			if t != nil {
				t.Fatalf("%s: bad line %q: %v", path, ln, err)
			}
			continue
		}
		out = append(out, m)
	}
	return out
}

// waitArticles blocks until the day files hold n article lines.
func waitArticles(t *testing.T, dir string, n int) {
	t.Helper()
	waitFor(t, "article lines in the day file", func() bool { return len(articlesOf(readDir(dir))) >= n })
}

// waitEvent blocks until the day files hold at least n events named ev.
func waitEvent(t *testing.T, dir, ev string, n int) {
	t.Helper()
	waitFor(t, ev+" event", func() bool { return len(eventsOf(readDir(dir), ev)) >= n })
}

func articlesOf(lines []fileLine) []fileLine {
	var out []fileLine
	for _, l := range lines {
		if _, ev := l["ev"]; !ev {
			out = append(out, l)
		}
	}
	return out
}

func eventsOf(lines []fileLine, ev string) []fileLine {
	var out []fileLine
	for _, l := range lines {
		if l["ev"] == ev {
			out = append(out, l)
		}
	}
	return out
}

func idsOf(lines []fileLine) []string {
	var out []string
	for _, l := range lines {
		out = append(out, l["id"].(string))
	}
	return out
}
