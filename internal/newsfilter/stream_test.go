package newsfilter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestHappyPathRecordsArticles(t *testing.T) {
	srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
		hello(ctx, c)
		send(ctx, c, evt(
			art("a1", "Alpha <b>&</b> Co announces results", map[string]any{
				"publishedAt": "2020-06-08T12:23:00-04:00", "description": "ignored",
				"source":  map[string]any{"id": "businesswire", "name": "Business Wire"},
				"symbols": []string{"alpha", " BETA "}, "industries": []string{"x"},
			}),
			art("a2", "Two", map[string]any{"publishedAt": "2020-06-08T15:50:02Z",
				"source": map[string]any{"id": "prNewswire", "name": "PR Newswire"}, "symbols": []string{"GAMA"}}),
			art("a3", "Three", map[string]any{"publishedAt": "2020-05-20T22:32:22+0000",
				"source": map[string]any{"id": "globenewswire", "name": "GlobeNewswire"}}),
			art("a4", "Four", map[string]any{"publishedAt": "2020-05-20T20:18:32.731Z",
				"source": map[string]any{"id": "accesswire", "name": "Accesswire"}}),
			art("a5", "Five", map[string]any{"publishedAt": "yesterday-ish",
				"source": map[string]any{"id": "benzinga", "name": "Benzinga"}}),
			art("a6", "Six", nil), // no publishedAt, no source at all
		))
		serve(ctx, c, nil)
	})
	r := newRig(t, []string{srv.url()}).run()
	waitArticles(t, r.dir, 6)
	r.stop()

	files, _ := filepath.Glob(filepath.Join(r.dir, "newsfilter-*.jsonl"))
	if raw, _ := os.ReadFile(files[0]); !strings.Contains(string(raw), `"title":"Alpha <b>&</b> Co announces results"`) {
		t.Errorf("titles are written without HTML escaping: %s", raw)
	}
	lines := readDirStrict(t, r.dir)
	arts := articlesOf(lines)
	if got := idsOf(arts); !reflect.DeepEqual(got, []string{"a1", "a2", "a3", "a4", "a5", "a6"}) {
		t.Fatalf("ids = %v", got)
	}
	wantKeys := []string{"id", "ms", "pub", "src", "srcName", "syms", "title", "url"}
	for _, a := range arts {
		var keys []string
		for k := range a {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if !reflect.DeepEqual(keys, wantKeys) {
			t.Fatalf("keys = %v, want %v", keys, wantKeys)
		}
	}
	a1 := arts[0]
	if a1["title"] != "Alpha <b>&</b> Co announces results" || a1["src"] != "businesswire" ||
		a1["srcName"] != "Business Wire" || a1["url"] != "https://example.test/a1" {
		t.Fatalf("a1 = %v", a1)
	}
	if got := a1["syms"].([]any); len(got) != 2 || got[0] != "ALPHA" || got[1] != "BETA" {
		t.Fatalf("syms are uppercased and trimmed: %v", got)
	}
	// All four documented date shapes parse; junk and a missing field give 0.
	wantPub := []int64{
		time.Date(2020, 6, 8, 16, 23, 0, 0, time.UTC).UnixMilli(),
		time.Date(2020, 6, 8, 15, 50, 2, 0, time.UTC).UnixMilli(),
		time.Date(2020, 5, 20, 22, 32, 22, 0, time.UTC).UnixMilli(),
		time.Date(2020, 5, 20, 20, 18, 32, 731e6, time.UTC).UnixMilli(),
		0, 0,
	}
	for i, a := range arts {
		if got := int64(a["pub"].(float64)); got != wantPub[i] {
			t.Errorf("%s pub = %d, want %d", a["id"], got, wantPub[i])
		}
	}
	// Every article of one frame shares the arrival stamp; syms is [] not null.
	for _, a := range arts {
		if got := int64(a["ms"].(float64)); got != t0.UnixMilli() {
			t.Errorf("%s ms = %d, want the frame's arrival %d", a["id"], got, t0.UnixMilli())
		}
	}
	if got := arts[5]["syms"].([]any); len(got) != 0 {
		t.Errorf("a6 syms = %v", got)
	}

	// The key reaches the server exactly as given, escaped properly.
	if q := srv.query(0); q.Get("apiKey") != testToken || q.Get("EIO") != "3" || q.Get("transport") != "websocket" {
		t.Errorf("handshake query = %v", q)
	}
	ev := eventsOf(lines, "connected")
	if len(ev) != 1 || ev[0]["url"] != "ws" || int64(ev[0]["ms"].(float64)) != t0.UnixMilli() {
		t.Errorf("connected events = %v", ev)
	}
	if len(eventsOf(lines, "stopped")) != 1 {
		t.Errorf("a clean shutdown records a stopped event: %v", lines)
	}
	if st := r.s.Stats(); st.Recorded != 6 || st.Articles != 6 || st.Dupes != 0 || st.ParseFails != 0 {
		t.Errorf("stats = %+v", st)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(r.dir); st.Mode().Perm() != 0o700 {
			t.Errorf("dir mode = %v", st.Mode().Perm())
		}
		if st, _ := os.Stat(files[0]); st.Mode().Perm() != 0o600 {
			t.Errorf("file mode = %v", st.Mode().Perm())
		}
	}
	if !strings.Contains(r.log.String(), "plaintext") {
		t.Errorf("a ws:// session must warn about plaintext: %s", r.log.String())
	}
}

func TestDuplicatesAcrossFramesAndReconnect(t *testing.T) {
	srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
		hello(ctx, c)
		switch n {
		case 1:
			send(ctx, c, evt(art("a", "A", nil), art("b", "B", nil)))
			send(ctx, c, evt(art("b", "B again", nil), art("c", "C", nil)))
			c.Close(websocket.StatusNormalClosure, "bye")
		default:
			send(ctx, c, evt(art("a", "A replay", nil), art("c", "C replay", nil), art("d", "D", nil)))
			serve(ctx, c, nil)
		}
	})
	r := newRig(t, []string{srv.url()}).run()
	waitArticles(t, r.dir, 4)
	waitFor(t, "second session's frame", func() bool { return r.s.Stats().Articles >= 7 })
	r.stop()

	got := idsOf(articlesOf(readDirStrict(t, r.dir)))
	if !reflect.DeepEqual(got, []string{"a", "b", "c", "d"}) {
		t.Fatalf("one line per id expected, got %v", got)
	}
	// 2 + 2 + 3 article objects, 4 first sightings, 3 repeats.
	if st := r.s.Stats(); st.Articles != 7 || st.Recorded != 4 || st.Dupes != 3 {
		t.Fatalf("stats = %+v", st)
	}
	// Only the first sighting is kept: the replayed title must not replace it.
	if arts := articlesOf(readDirStrict(t, r.dir)); arts[0]["title"] != "A" {
		t.Fatalf("first sighting must win: %v", arts[0])
	}
}

func TestSeenSetStaysBounded(t *testing.T) {
	var frames []string
	for i := 0; i < 40; i++ {
		frames = append(frames, evt(art(string(rune('A'+i%26))+string(rune('a'+i/26)), "t", nil)))
	}
	srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
		hello(ctx, c)
		for _, f := range frames {
			send(ctx, c, f)
		}
		serve(ctx, c, nil)
	})
	r := newRig(t, []string{srv.url()}, func(s *Stream) { s.seen = newIDSet(8) }).run()
	waitArticles(t, r.dir, 40)
	r.stop()
	if got := r.s.seen.len(); got != 8 {
		t.Fatalf("seen-set must hold at most its cap (8), has %d", got)
	}
	if len(r.s.seen.ring) != 8 {
		t.Fatalf("ring grew to %d", len(r.s.seen.ring))
	}
}

func TestIDSetEvictsOldest(t *testing.T) {
	s := newIDSet(3)
	for _, id := range []string{"a", "b", "c"} {
		if !s.add(id) {
			t.Fatalf("%s should be new", id)
		}
	}
	if s.add("a") {
		t.Fatal("a is still inside the window")
	}
	if !s.add("d") { // evicts a, the oldest
		t.Fatal("d should be new")
	}
	if s.len() != 3 {
		t.Fatalf("len = %d", s.len())
	}
	if !s.add("a") { // a was evicted, so it counts as new again
		t.Fatal("evicted a should be new again")
	}
	if s.add("d") {
		t.Fatal("d must still be remembered")
	}
}

func TestReconnectBackoffResetsAfterHealthySession(t *testing.T) {
	var opens atomic.Int64
	srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
		hello(ctx, c)
		if n == 3 {
			// Health is judged by the last frame: one has to arrive after the
			// clock jump below, or the session only looks like it was quiet.
			send(ctx, c, "3")
		}
		c.Close(websocket.StatusGoingAway, "restart")
	})
	r := newRig(t, []string{srv.url()})
	// Session 3 "stays up" two minutes: the clock jumps right after the open
	// packet has been handled (the second frame is the 40).
	r.s.onFrame = func(p []byte) {
		switch string(p) {
		case helloFrame:
			opens.Add(1)
		case "40":
			if opens.Load() == 3 {
				r.clock.Advance(2 * time.Minute)
			}
		}
	}
	r.sl.gate = make(chan struct{})
	r.run()
	for i := 1; i <= 4; i++ {
		i := i
		waitFor(t, "backoff wait", func() bool { return len(r.sl.durations()) >= i })
		if i < 4 {
			r.sl.gate <- struct{}{}
		}
	}
	r.stop()

	want := []time.Duration{time.Second, 2 * time.Second, time.Second, 2 * time.Second}
	if got := r.sl.durations()[:4]; !reflect.DeepEqual(got, want) {
		t.Fatalf("backoff waits = %v, want %v (doubling, reset after the healthy third session)", got, want)
	}
	lines := readDirStrict(t, r.dir)
	conn, drop := eventsOf(lines, "connected"), eventsOf(lines, "dropped")
	if len(conn) != 4 || len(drop) != 4 {
		t.Fatalf("want 4 connected + 4 dropped events, got %d + %d", len(conn), len(drop))
	}
	for _, d := range drop {
		if d["err"] == "" || d["err"] == nil || d["last"] == nil {
			t.Errorf("dropped event needs err and last: %v", d)
		}
	}
	if st := r.s.Stats(); st.Reconnects < 4 {
		t.Errorf("reconnects = %d", st.Reconnects)
	}
}

func TestServerClosingFramesDropTheSession(t *testing.T) {
	for _, tc := range []struct{ name, frame string }{
		{"engine close", "1"},
		{"socket.io disconnect", "41"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
				hello(ctx, c)
				send(ctx, c, tc.frame)
				serve(ctx, c, nil)
			})
			r := newRig(t, []string{srv.url()}).run()
			waitFor(t, "two sessions", func() bool { return srv.conns() >= 2 })
			r.stop()
			drops := eventsOf(readDirStrict(t, r.dir), "dropped")
			if len(drops) == 0 || !strings.Contains(drops[0]["err"].(string), "server") {
				t.Fatalf("dropped = %v", drops)
			}
			if d := r.sl.durations(); len(d) == 0 || d[0] != time.Second {
				t.Fatalf("an ordinary drop takes the short backoff, got %v", d)
			}
		})
	}
}

func TestSocketIOErrorFrameTakesTheLongBackoff(t *testing.T) {
	srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
		hello(ctx, c)
		send(ctx, c, `44"Authentication error"`)
		serve(ctx, c, nil)
	})
	r := newRig(t, []string{srv.url()})
	r.sl.gate = make(chan struct{})
	r.run()
	waitFor(t, "first wait", func() bool { return len(r.sl.durations()) >= 1 })
	r.sl.gate <- struct{}{}
	waitFor(t, "second wait", func() bool { return len(r.sl.durations()) >= 2 })
	r.stop()

	for i, d := range r.sl.durations()[:2] {
		if d != backoffMax {
			t.Errorf("wait %d = %v, want the 5 minute cap after a refused key", i, d)
		}
	}
	log := r.log.String()
	if !strings.Contains(log, "REFUSED") || !strings.Contains(log, "Authentication error") || !strings.Contains(log, "retry_in=5m0s") {
		t.Errorf("log must say plainly that the key or plan was refused:\n%s", log)
	}
	if srv.conns() != 2 {
		t.Errorf("connections = %d, want 2 (it keeps trying, slowly)", srv.conns())
	}
}

func TestHTTPRefusalOnDialTakesTheLongBackoff(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			refused, hits := statusServer(t, code, "no")
			second := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) { hello(ctx, c); serve(ctx, c, nil) })
			r := newRig(t, []string{wsURLOf(refused), second.url()}).run()
			waitFor(t, "backoff wait", func() bool { return len(r.sl.durations()) >= 1 })
			r.stop()
			if d := r.sl.durations()[0]; d != backoffMax {
				t.Errorf("wait = %v, want 5m", d)
			}
			if second.conns() != 0 {
				t.Errorf("a refused key must not be retried on the other endpoint, saw %d connections", second.conns())
			}
			if hits.Load() < 1 {
				t.Error("server never contacted")
			}
			if log := r.log.String(); !strings.Contains(log, "REFUSED") || !strings.Contains(log, "HTTP "+strconv.Itoa(code)) {
				t.Errorf("log must name the refusal:\n%s", log)
			}
		})
	}
}

func TestOtherDialFailuresTakeTheShortBackoff(t *testing.T) {
	bad, hits := statusServer(t, http.StatusInternalServerError, "boom")
	r := newRig(t, []string{wsURLOf(bad)})
	r.sl.gate = make(chan struct{})
	r.run()
	waitFor(t, "first wait", func() bool { return len(r.sl.durations()) >= 1 })
	r.sl.gate <- struct{}{}
	waitFor(t, "second wait", func() bool { return len(r.sl.durations()) >= 2 })
	r.stop()
	if got := r.sl.durations()[:2]; got[0] != time.Second || got[1] != 2*time.Second {
		t.Fatalf("waits = %v", got)
	}
	if hits.Load() < 2 {
		t.Fatalf("hits = %d", hits.Load())
	}
	// A dial that never connected has no connected or dropped event to record;
	// only the shutdown leaves its stopped event.
	for _, l := range readDirStrict(t, r.dir) {
		if l["ev"] != "stopped" {
			t.Fatalf("a dial that never connected records no events besides stopped, got %v", l)
		}
	}
}

func TestGarbageNeverKillsTheSession(t *testing.T) {
	hugeTitle := strings.Repeat("x", 3_000_000) // inside the 4 MiB read limit
	srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
		hello(ctx, c)
		for _, f := range []string{
			`42not json`,
			`42["other",{}]`,
			`42["articles",{"articles":"x"}]`,
			`42`,
			"", // an empty text message
			`4`,
			`9zzz`,
			`42["articles",{"articles":[` +
				`{"id":"ok1","title":"t1","symbols":"AAPL"},` +
				`{"id":5,"title":7,"publishedAt":"2020-06-08T15:50:02Z"},` +
				`12,null,{"title":"no id"},` +
				`{"id":"ok2","publishedAt":"junk","source":{"id":3,"name":"N"},"symbols":["abc",1," de "]}` +
				`]}]`,
			`42["articles",[{"id":"bare1","title":"bare array"}]]`,
			`42/news,12["articles",{"articles":[{"id":"ns1","title":"namespace and ack id"}]}]`,
			evt(art("huge", hugeTitle, nil)),
		} {
			send(ctx, c, f)
		}
		_ = c.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3})
		send(ctx, c, evt(art("last", "still here", nil)))
		serve(ctx, c, nil)
	})
	r := newRig(t, []string{srv.url()}).run()
	waitArticles(t, r.dir, 7)
	r.stop()

	if srv.conns() != 1 {
		t.Fatalf("garbage must not drop the session: %d connections", srv.conns())
	}
	arts := articlesOf(readDirStrict(t, r.dir))
	if got := idsOf(arts); !reflect.DeepEqual(got, []string{"ok1", "5", "ok2", "bare1", "ns1", "huge", "last"}) {
		t.Fatalf("recorded ids = %v", got)
	}
	if n := len(arts[5]["title"].(string)); n != maxTitle {
		t.Errorf("huge title recorded at %d bytes, want it capped at %d", n, maxTitle)
	}
	ok2 := arts[2]
	if ok2["pub"].(float64) != 0 || ok2["src"] != "" || ok2["srcName"] != "N" {
		t.Errorf("ok2 = %v", ok2)
	}
	if syms := ok2["syms"].([]any); len(syms) != 2 || syms[0] != "ABC" || syms[1] != "DE" {
		t.Errorf("ok2 syms = %v", syms)
	}
	want := Stats{
		Frames:   11 + 2 + 2, // 11 text frames in the list, the binary one and the last, plus the two hello frames
		Articles: 7, Recorded: 7,
		ParseFails:  3 + 3 + 2, // unusable frames: not json, bad list, bare 42; empty, "4", "9zzz"; the 12 and the null
		NoID:        1,
		BadFields:   1 + 1 + 2, // ok1 symbols; id 5 title; ok2 source.id and a numeric symbol
		Binary:      1,
		OtherEvents: 1,
	}
	if st := r.s.Stats(); st != want {
		t.Errorf("stats = %+v\nwant    %+v", st, want)
	}
}

func TestOversizedFrameDropsTheSessionAndRecovers(t *testing.T) {
	srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
		hello(ctx, c)
		if n == 1 {
			c.SetReadLimit(1 << 24)
			send(ctx, c, evt(art("toobig", strings.Repeat("y", 5<<20), nil))) // over the 4 MiB client limit
			serve(ctx, c, nil)
			return
		}
		send(ctx, c, evt(art("after", "recovered", nil)))
		serve(ctx, c, nil)
	})
	r := newRig(t, []string{srv.url()}).run()
	waitArticles(t, r.dir, 1)
	r.stop()
	if got := idsOf(articlesOf(readDirStrict(t, r.dir))); !reflect.DeepEqual(got, []string{"after"}) {
		t.Fatalf("ids = %v", got)
	}
	if srv.conns() < 2 {
		t.Fatalf("expected a reconnect, saw %d connections", srv.conns())
	}
}

func TestKeepaliveSendsEnginePingAndNeverConnectPacket(t *testing.T) {
	var mu sync.Mutex
	var got []string
	srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
		send(ctx, c, `0{"sid":"s","pingInterval":25,"pingTimeout":2000}`)
		send(ctx, c, "40")
		pinged := false
		serve(ctx, c, func(m string) {
			mu.Lock()
			got = append(got, m)
			cnt := len(got)
			mu.Unlock()
			if m == "2" && cnt >= 3 && !pinged {
				pinged = true
				send(ctx, c, "2") // a server ping must be answered with 3
			}
		})
	})
	r := newRig(t, []string{srv.url()}).run()
	waitFor(t, "pings and the pong to our ping", func() bool {
		mu.Lock()
		defer mu.Unlock()
		pings, pongs := 0, 0
		for _, m := range got {
			switch m {
			case "2":
				pings++
			case "3":
				pongs++
			}
		}
		return pings >= 4 && pongs >= 1
	})
	r.stop()
	mu.Lock()
	defer mu.Unlock()
	for _, m := range got {
		if m != "2" && m != "3" {
			t.Errorf("client sent %q; only engine ping 2 and pong 3 are allowed (40 would register another socket)", m)
		}
	}
	if srv.conns() != 1 {
		t.Errorf("connections = %d", srv.conns())
	}
}

func TestKeepaliveCadenceAndSilenceLimit(t *testing.T) {
	s := New(Config{})
	if s.pingMax != 20*time.Second || s.silenceMax != 60*time.Second || s.healthyAfter != 60*time.Second {
		t.Fatalf("defaults: ping cap %v, silence %v, healthy %v", s.pingMax, s.silenceMax, s.healthyAfter)
	}
	pi, pt := parseOpen([]byte(`{"sid":"x","pingInterval":25000,"pingTimeout":5000}`))
	if pi != 25*time.Second || pt != 5*time.Second {
		t.Fatalf("parseOpen = %v %v", pi, pt)
	}
	for _, c := range []struct{ pi, want time.Duration }{
		{25 * time.Second, 20 * time.Second}, // capped
		{5 * time.Second, 5 * time.Second},   // the server's own interval when shorter
		{0, 20 * time.Second},                // unknown
		{time.Millisecond, time.Second},      // floored
	} {
		if got := pingEvery(c.pi, 20*time.Second, time.Second); got != c.want {
			t.Errorf("pingEvery(%v) = %v, want %v", c.pi, got, c.want)
		}
	}
	for _, c := range []struct{ pi, pt, want time.Duration }{
		{25 * time.Second, 5 * time.Second, 30 * time.Second},  // smaller than 60 s: use it
		{50 * time.Second, 30 * time.Second, 60 * time.Second}, // larger: the 60 s cap
		{0, 5 * time.Second, 60 * time.Second},                 // not both known
		{25 * time.Second, 0, 60 * time.Second},
	} {
		if got := silenceFor(c.pi, c.pt, 60*time.Second); got != c.want {
			t.Errorf("silenceFor(%v,%v) = %v, want %v", c.pi, c.pt, got, c.want)
		}
	}
}

func TestSilenceTriggersReconnect(t *testing.T) {
	tests := []struct {
		name  string
		hello string
		mut   func(*Stream)
	}{
		// pingInterval+pingTimeout (30+70 ms) is smaller than the 60 s default.
		{"from the open packet", `0{"sid":"s","pingInterval":30,"pingTimeout":70}`, nil},
		// No timings announced: the configured silence limit applies.
		{"default limit", `0{}`, func(s *Stream) { s.silenceMax = 120 * time.Millisecond }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
				send(ctx, c, tc.hello)
				send(ctx, c, "40")
				serve(ctx, c, nil) // reads our pings and never answers
			})
			r := newRig(t, []string{srv.url()}, tc.mut).run()
			waitFor(t, "a reconnect after silence", func() bool { return srv.conns() >= 2 })
			r.stop()
			drops := eventsOf(readDirStrict(t, r.dir), "dropped")
			if len(drops) == 0 || !strings.Contains(drops[0]["err"].(string), "no frame for") {
				t.Fatalf("dropped events = %v", drops)
			}
		})
	}
}

func TestFallbackToSecondURLIsRemembered(t *testing.T) {
	bad, badHits := statusServer(t, http.StatusBadGateway, "")
	good := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
		hello(ctx, c)
		send(ctx, c, evt(art("x"+string(rune('0'+n)), "t", nil)))
		if n == 1 {
			c.Close(websocket.StatusGoingAway, "restart")
			return
		}
		serve(ctx, c, nil)
	})
	r := newRig(t, []string{wsURLOf(bad), good.url()}).run()
	waitArticles(t, r.dir, 2)
	r.stop()

	if r.s.cur != 1 {
		t.Errorf("the working endpoint must be remembered, cur = %d", r.s.cur)
	}
	if good.conns() < 2 {
		t.Fatalf("good connections = %d", good.conns())
	}
	if badHits.Load() != 1 {
		t.Errorf("after the fallback the failing endpoint must not be tried first again: hits = %d", badHits.Load())
	}
	if n := strings.Count(r.log.String(), "plaintext"); n != 1 {
		t.Errorf("the plaintext warning is logged once, saw %d:\n%s", n, r.log.String())
	}
}

func TestDefaultURLsTryWSSFirst(t *testing.T) {
	s := New(Config{Token: "k"})
	if len(s.urls) != 2 || !strings.HasPrefix(s.urls[0], "wss://stream.newsfilter.io/socket.io/") ||
		!strings.HasPrefix(s.urls[1], "ws://stream.newsfilter.io/socket.io/") {
		t.Fatalf("urls = %v", s.urls)
	}
	if kindOf(s.urls[0]) != "wss" || kindOf(s.urls[1]) != "ws" {
		t.Fatal("kindOf")
	}
	if got := keyed(s.urls[0], "a b&c"); !strings.HasSuffix(got, "transport=websocket&apiKey=a+b%26c") {
		t.Fatalf("keyed = %s", got)
	}
	if got := keyed("ws://h/p", "k"); got != "ws://h/p?apiKey=k" {
		t.Fatalf("keyed without a query = %s", got)
	}
}

func TestRunReturnsNilWhenCancelledWhileConnected(t *testing.T) {
	srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) { hello(ctx, c); serve(ctx, c, nil) })
	r := newRig(t, []string{srv.url()}).run()
	waitEvent(t, r.dir, "connected", 1)
	r.stop() // asserts Run returned nil within the deadline
	if len(eventsOf(readDirStrict(t, r.dir), "stopped")) != 1 {
		t.Fatal("expected a stopped event")
	}
}

func TestRunReturnsNilBeforeAnythingConnects(t *testing.T) {
	r := newRig(t, []string{"ws://127.0.0.1:1/socket.io/?EIO=3&transport=websocket"}).run()
	waitFor(t, "first failed attempt", func() bool { return len(r.sl.durations()) >= 1 })
	r.stop()
}

func TestUnwritableDirNeverStopsTheStream(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("a file where a directory must go"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(blocker, "sub")
	srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
		hello(ctx, c)
		for i := 0; sendErr(ctx, c, evt(art("u"+string(rune('a'+i%26))+string(rune('a'+i/26)), "t", nil))) == nil; i++ {
			time.Sleep(5 * time.Millisecond)
		}
	})
	r := newRig(t, []string{srv.url()}, func(s *Stream) {
		s.cfg.Dir = dir
		s.rec.dir = dir
	}).run()
	waitFor(t, "frames processed despite the broken disk", func() bool { return r.s.Stats().Recorded >= 10 })
	select {
	case err := <-r.done:
		t.Fatalf("Run exited (%v) because of a disk error", err)
	default:
	}
	if r.s.Stats().DiskErrors < 10 {
		t.Errorf("disk errors = %d", r.s.Stats().DiskErrors)
	}
	r.stop()
	// Rate limited: the clock never moved, so exactly one complaint.
	if n := strings.Count(r.log.String(), "cannot write the day file"); n != 1 {
		t.Errorf("disk error logged %d times, want once (rate limited)", n)
	}
}

func TestPanicInFrameHandlingDropsOnlyTheSession(t *testing.T) {
	srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
		hello(ctx, c)
		if n == 1 {
			send(ctx, c, evt(art("boom", "BOOM", nil)))
			serve(ctx, c, nil)
			return
		}
		send(ctx, c, evt(art("fine", "all good", nil)))
		serve(ctx, c, nil)
	})
	r := newRig(t, []string{srv.url()}, func(s *Stream) {
		s.onFrame = func(p []byte) {
			if strings.Contains(string(p), "BOOM") {
				panic("bad frame " + testToken)
			}
		}
	}).run()
	waitArticles(t, r.dir, 1)
	r.stop()

	if got := idsOf(articlesOf(readDirStrict(t, r.dir))); !reflect.DeepEqual(got, []string{"fine"}) {
		t.Fatalf("ids = %v", got)
	}
	if st := r.s.Stats(); st.Panics != 1 {
		t.Errorf("panics = %d", st.Panics)
	}
	log := r.log.String()
	if !strings.Contains(log, "panic in session") {
		t.Errorf("panic not logged:\n%s", log)
	}
	if strings.Contains(log, testToken) {
		t.Errorf("a panic value carrying the key must be scrubbed:\n%s", log)
	}
	drops := eventsOf(readDirStrict(t, r.dir), "dropped")
	if len(drops) == 0 || !strings.HasPrefix(drops[0]["err"].(string), "panic:") {
		t.Errorf("dropped = %v", drops)
	}
}

func TestDayFileRollsOverAtUTCMidnight(t *testing.T) {
	step := make(chan struct{})
	srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
		hello(ctx, c)
		send(ctx, c, evt(art("late", "before midnight", nil)))
		select {
		case <-step:
		case <-ctx.Done():
			return
		}
		send(ctx, c, evt(art("early", "after midnight", nil)))
		serve(ctx, c, nil)
	})
	r := newRig(t, []string{srv.url()})
	r.clock.ns.Store(time.Date(2026, 10, 30, 23, 59, 58, 0, time.UTC).UnixNano())
	r.run()
	waitArticles(t, r.dir, 1)
	r.clock.Advance(5 * time.Second)
	step <- struct{}{}
	waitArticles(t, r.dir, 2)
	r.stop()

	a := readFileLines(filepath.Join(r.dir, "newsfilter-2026-10-30.jsonl"), t)
	b := readFileLines(filepath.Join(r.dir, "newsfilter-2026-10-31.jsonl"), t)
	if got := idsOf(articlesOf(a)); !reflect.DeepEqual(got, []string{"late"}) {
		t.Errorf("10-30 file articles = %v", got)
	}
	if got := idsOf(articlesOf(b)); !reflect.DeepEqual(got, []string{"early"}) {
		t.Errorf("10-31 file articles = %v", got)
	}
	if len(eventsOf(a, "connected")) != 1 || len(eventsOf(b, "stopped")) != 1 {
		t.Errorf("events landed in the wrong day files: %v / %v", a, b)
	}
}

func TestRawCaptureCapsAndTruncates(t *testing.T) {
	srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
		send(ctx, c, "3") // bare engine frames are not captured
		send(ctx, c, "2")
		hello(ctx, c)
		send(ctx, c, evt(art("big", strings.Repeat("z", 100_000), nil)))
		_ = c.Write(ctx, websocket.MessageBinary, []byte{9, 9})
		send(ctx, c, `42["articles",{"note":"`+testToken+`"}]`) // carries the key: must be scrubbed
		for i := 0; i < 260; i++ {
			send(ctx, c, evt(art("r"+string(rune('a'+i%26))+string(rune('a'+i/26%26))+string(rune('a'+i/676)), "t", nil)))
		}
		serve(ctx, c, nil)
	})
	r := newRig(t, []string{srv.url()})
	rawPath := filepath.Join(r.dir, rawFileName)
	if err := os.WriteFile(rawPath, []byte("stale line from an earlier run\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.run()
	waitArticles(t, r.dir, 261)
	r.stop()

	lines := readFileLines(rawPath, t)
	if len(lines) != rawFrameCap {
		t.Fatalf("raw capture has %d lines, want exactly %d", len(lines), rawFrameCap)
	}
	if f := lines[0]["frame"].(string); f != helloFrame {
		t.Errorf("first captured frame = %q (pings and pongs must be skipped, stale content truncated)", f)
	}
	if lines[1]["frame"] != "40" {
		t.Errorf("second frame = %v", lines[1])
	}
	big := lines[2]
	if big["trunc"] != true || len(big["frame"].(string)) > rawFrameMax || int(big["len"].(float64)) < 100_000 {
		t.Errorf("big frame: trunc=%v kept=%d len=%v", big["trunc"], len(big["frame"].(string)), big["len"])
	}
	if lines[3]["bin"] != true || lines[3]["len"].(float64) != 2 {
		t.Errorf("binary frame line = %v", lines[3])
	}
	b, _ := os.ReadFile(rawPath)
	if strings.Contains(string(b), "stale line") {
		t.Error("raw-frames.jsonl must be truncated at each start")
	}
	for _, v := range secretsFor(testToken) {
		if strings.Contains(string(b), v) {
			t.Errorf("raw capture contains the key (%q)", v)
		}
	}
	if !strings.Contains(string(b), "***") {
		t.Error("expected the scrubbed marker in the captured key-carrying frame")
	}
}

func TestStatsLineAndAliveHeartbeat(t *testing.T) {
	srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
		hello(ctx, c)
		for sendErr(ctx, c, "3") == nil {
			time.Sleep(10 * time.Millisecond)
		}
	})
	// Real clock so the cadence is real; the cadence itself is shortened.
	r := newRig(t, []string{srv.url()}, func(s *Stream) {
		s.now = time.Now
		s.statsEvery = 30 * time.Millisecond
	}).run()
	waitEvent(t, r.dir, "alive", 2)
	r.stop()
	log := r.log.String()
	for _, f := range []string{"newsfilter stats", "frames=", "articles=", "dupes=", "parseFails=", "reconnects="} {
		if !strings.Contains(log, f) {
			t.Errorf("stats line lacks %q:\n%s", f, log)
		}
	}
}

func TestNothingIsExportedForDelivery(t *testing.T) {
	// The recorder must not grow a way to hand articles to the rest of the app.
	typ := reflect.TypeOf(&Stream{})
	for i := 0; i < typ.NumMethod(); i++ {
		switch name := typ.Method(i).Name; name {
		case "Run", "Stats":
		default:
			t.Errorf("unexpected exported method %s on the shadow recorder", name)
		}
	}
}

func TestArrivalIsStampedBeforeParsing(t *testing.T) {
	srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
		hello(ctx, c)
		send(ctx, c, evt(art("a", "t", nil), art("b", "t", nil)))
		serve(ctx, c, nil)
	})
	r := newRig(t, []string{srv.url()})
	// Whatever happens after the frame came off the socket (the hook stands in
	// for slow parsing) must not leak into the arrival stamp.
	r.s.onFrame = func(p []byte) {
		if strings.HasPrefix(string(p), "42") {
			r.clock.Advance(7 * time.Second)
		}
	}
	r.run()
	waitArticles(t, r.dir, 2)
	r.stop()
	for _, a := range articlesOf(readDirStrict(t, r.dir)) {
		if got := int64(a["ms"].(float64)); got != t0.UnixMilli() {
			t.Errorf("%s ms = %d, want the arrival instant %d, not the post-parse clock", a["id"], got, t0.UnixMilli())
		}
	}
}

// The key is on the wire from the moment a plaintext dial starts, so the
// warning must be logged before the dial, not when a first frame shows up: an
// attempt that is refused or closed before any frame would never get one.
func TestPlaintextWarningComesBeforeTheKeyIsSent(t *testing.T) {
	deadWSS := "wss://" + freePort(t) + "/socket.io/?EIO=3&transport=websocket"
	for _, tc := range []struct {
		name   string
		answer func(w http.ResponseWriter, r *http.Request)
	}{
		{"refused with 403", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }},
		{"accepted, then closed before any frame", func(w http.ResponseWriter, r *http.Request) {
			if c, err := websocket.Accept(w, r, nil); err == nil {
				c.Close(websocket.StatusGoingAway, "bye")
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs atomic.Pointer[logBuf]
			var hits atomic.Int64
			var warnedBeforeFirstRequest atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if hits.Add(1) == 1 { // the key is in this request's URL
					lb := logs.Load()
					warnedBeforeFirstRequest.Store(lb != nil && strings.Contains(lb.String(), "plaintext"))
				}
				tc.answer(w, r)
			}))
			t.Cleanup(srv.Close)

			r := newRig(t, []string{deadWSS, wsURLOf(srv)})
			logs.Store(r.log)
			r.run()
			waitFor(t, "the attempt to end", func() bool { return len(r.sl.durations()) >= 1 })
			r.stop()

			if hits.Load() < 1 {
				t.Fatal("the plaintext endpoint was never dialed")
			}
			if !warnedBeforeFirstRequest.Load() {
				t.Error("the plaintext warning was not logged before the key went out")
			}
			var warns int
			for _, line := range strings.Split(r.log.String(), "\n") {
				if strings.Contains(line, "plaintext") {
					warns++
					if !strings.Contains(line, "level=WARN") {
						t.Errorf("the plaintext notice must be a Warn: %s", line)
					}
				}
			}
			if warns != 1 {
				t.Errorf("plaintext warning logged %d times, want once:\n%s", warns, r.log.String())
			}
			if got := eventsOf(readDirStrict(t, r.dir), "connected"); len(got) != 0 {
				t.Errorf("the session never opened, yet connected = %v", got)
			}
			assertNoKey(t, r)
		})
	}
}

// silenceMax and healthyAfter are both 60 s in production. A socket that opened
// and then went quiet ends at exactly that threshold, so judging health by the
// end of the session would reset the backoff on every such cycle.
func TestSilentSessionDoesNotResetTheBackoff(t *testing.T) {
	srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
		send(ctx, c, "0{}") // no ping timings: the configured silence limit applies
		send(ctx, c, "40")
		serve(ctx, c, nil) // ignores our pings
	})
	r := newRig(t, []string{srv.url()}, func(s *Stream) {
		s.now = time.Now // a real clock: the session really lasts as long as the silence limit
		s.silenceMax = 200 * time.Millisecond
		s.healthyAfter = 200 * time.Millisecond
	}).run()
	waitFor(t, "four backoff waits", func() bool { return len(r.sl.durations()) >= 4 })
	r.stop()
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	if got := r.sl.durations()[:4]; !reflect.DeepEqual(got, want) {
		t.Fatalf("backoff waits = %v, want %v (a session that went quiet after the open is not healthy)", got, want)
	}
}

func TestKeepalivePanicDropsTheSessionAndIsLogged(t *testing.T) {
	srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) { hello(ctx, c); serve(ctx, c, nil) })
	r := newRig(t, []string{srv.url()}, func(s *Stream) {
		s.pingMax, s.pingMin = 0, 0 // a zero cadence makes time.NewTicker panic inside the keepalive goroutine
	})
	r.sl.gate = make(chan struct{})
	r.run()
	waitFor(t, "the session to be dropped", func() bool { return len(r.sl.durations()) >= 1 })
	r.stop()

	if st := r.s.Stats(); st.Panics != 1 || st.Reconnects != 1 {
		t.Errorf("a keepalive panic is a dropped session: panics %d, reconnects %d", st.Panics, st.Reconnects)
	}
	if log := r.log.String(); !strings.Contains(log, "panic in keepalive") || !strings.Contains(log, "level=ERROR") {
		t.Errorf("the panic must be logged:\n%s", log)
	}
	drops := eventsOf(readDirStrict(t, r.dir), "dropped")
	if len(drops) != 1 || !strings.HasPrefix(drops[0]["err"].(string), "panic:") {
		t.Errorf("dropped = %v", drops)
	}
	assertNoKey(t, r)
}

// Every exit writes a stopped event, also one that happens while the stream is
// waiting out a backoff, so the report can tell the recorder being off from the
// connection being down.
func TestStoppedEventIsWrittenOnEveryExit(t *testing.T) {
	stoppedOnly := func(t *testing.T, lines []fileLine) {
		t.Helper()
		if got := eventsOf(lines, "stopped"); len(got) != 1 {
			t.Fatalf("want exactly one stopped event, got %v", lines)
		}
	}
	t.Run("during the backoff after a failed dial", func(t *testing.T) {
		r := newRig(t, []string{"ws://" + freePort(t) + "/socket.io/?EIO=3&transport=websocket"})
		r.sl.gate = make(chan struct{})
		r.run()
		waitFor(t, "the backoff wait", func() bool { return len(r.sl.durations()) >= 1 })
		r.stop()
		stoppedOnly(t, readDirStrict(t, r.dir))
	})
	t.Run("during the backoff after a drop", func(t *testing.T) {
		srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
			hello(ctx, c)
			c.Close(websocket.StatusGoingAway, "restart")
		})
		r := newRig(t, []string{srv.url()})
		r.sl.gate = make(chan struct{})
		r.run()
		waitFor(t, "the backoff wait", func() bool { return len(r.sl.durations()) >= 1 })
		r.stop()
		lines := readDirStrict(t, r.dir)
		stoppedOnly(t, lines)
		var order []string
		for _, l := range lines {
			order = append(order, l["ev"].(string))
		}
		if !reflect.DeepEqual(order, []string{"connected", "dropped", "stopped"}) {
			t.Errorf("events = %v", order)
		}
	})
}

func TestOverlongFieldsAreCappedOrRefusedInTheDayFile(t *testing.T) {
	big := strings.Repeat("Q", 600_000)
	srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
		hello(ctx, c)
		// An id of 600 KB would sit in the seen-set for the life of the process.
		send(ctx, c, evt(art(big, "huge id", nil)))
		send(ctx, c, evt(art("fat", "t", map[string]any{
			"source":  map[string]any{"id": big, "name": big},
			"symbols": []string{big, "ok"},
		})))
		send(ctx, c, evt(art("last", "marker", nil)))
		serve(ctx, c, nil)
	})
	r := newRig(t, []string{srv.url()}).run()
	waitFor(t, "the marker article", func() bool { return r.s.Stats().Recorded >= 2 })
	r.stop()

	arts := articlesOf(readDirStrict(t, r.dir))
	if got := idsOf(arts); !reflect.DeepEqual(got, []string{"fat", "last"}) {
		t.Fatalf("ids = %v (the 600 KB id is no id)", got)
	}
	fat := arts[0]
	if n := len(fat["src"].(string)); n != maxSrc {
		t.Errorf("src is %d bytes, want it capped at %d", n, maxSrc)
	}
	if n := len(fat["srcName"].(string)); n != maxSrc {
		t.Errorf("srcName is %d bytes, want it capped at %d", n, maxSrc)
	}
	if syms := fat["syms"].([]any); len(syms) != 1 || syms[0] != "OK" {
		t.Errorf("syms = %v: a 600 KB symbol is dropped, the real one kept", syms)
	}
	if got := r.s.seen.len(); got != 2 {
		t.Errorf("the seen-set holds %d ids, want 2 (the refused id must not be remembered)", got)
	}
	if st := r.s.Stats(); st.NoID != 1 {
		t.Errorf("stats = %+v, want the overlong id counted as an article without an id", st)
	}
	files, _ := filepath.Glob(filepath.Join(r.dir, "newsfilter-*.jsonl"))
	if st, err := os.Stat(files[0]); err != nil || st.Size() > 8<<10 {
		t.Errorf("day file is %d bytes after three articles with 600 KB fields", st.Size())
	}
}

// A day file that ends mid-line (a crash, a full disk) must not swallow the
// next line, which on a restart is the connected event.
func TestTornLineInTheDayFileDoesNotEatTheNextEvent(t *testing.T) {
	srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
		hello(ctx, c)
		send(ctx, c, evt(art("a", "after the tear", nil)))
		serve(ctx, c, nil)
	})
	r := newRig(t, []string{srv.url()})
	day := filepath.Join(r.dir, fileName(t0.Format(dayLayout)))
	if err := os.WriteFile(day, []byte(`{"ms":1,"id":"torn","ti`), 0o600); err != nil {
		t.Fatal(err)
	}
	r.run()
	waitArticles(t, r.dir, 1)
	r.stop()

	lines := readDir(r.dir) // the fragment itself is unreadable by design
	if got := eventsOf(lines, "connected"); len(got) != 1 {
		t.Fatalf("the connected event was lost behind the fragment: %v", lines)
	}
	if got := idsOf(articlesOf(lines)); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("ids = %v", got)
	}
	c, err := LoadDir(r.dir, t0.Add(-time.Hour).UnixMilli(), t0.Add(time.Hour).UnixMilli())
	if err != nil || c.Bad != 1 || len(c.Events) != 2 || len(c.Records) != 1 {
		t.Errorf("only the fragment should be unreadable: bad %d, events %d, records %d, err %v", c.Bad, len(c.Events), len(c.Records), err)
	}
}

func TestServerPingProbeIsEchoedAsPongProbe(t *testing.T) {
	got := make(chan string, 4)
	srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
		hello(ctx, c)
		send(ctx, c, "2probe")
		serve(ctx, c, func(m string) {
			if strings.HasPrefix(m, "3") {
				select {
				case got <- m:
				default:
				}
			}
		})
	})
	newRig(t, []string{srv.url()}).run()
	select {
	case m := <-got:
		if m != "3probe" {
			t.Errorf("reply to 2probe = %q, want 3probe", m)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no reply to the server's ping")
	}
}
