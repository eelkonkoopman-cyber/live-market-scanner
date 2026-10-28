package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// fakeStream is an Alpaca-protocol websocket server. session runs after a
// successful handshake; authCode != 0 rejects the auth with that code.
type fakeStream struct {
	t        *testing.T
	authCode int
	conns    atomic.Int32
	subs     chan ctlMsg
	session  func(ctx context.Context, send func(string) error, n int32)
}

func (f *fakeStream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	n := f.conns.Add(1)
	ctx := r.Context()
	send := func(s string) error { return conn.Write(ctx, websocket.MessageText, []byte(s)) }
	if send(`[{"T":"success","msg":"connected"}]`) != nil {
		return
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		return
	}
	var auth ctlMsg
	_ = json.Unmarshal(data, &auth)
	if auth.Action != "auth" || auth.Key != testKeyID || auth.Secret != testSecret {
		_ = send(`[{"T":"error","code":402,"msg":"auth failed"}]`)
		return
	}
	if f.authCode != 0 {
		_ = send(`[{"T":"error","code":` + itoa(f.authCode) + `,"msg":"rejected"}]`)
		return
	}
	_ = send(`[{"T":"success","msg":"authenticated"}]`)
	_, data, err = conn.Read(ctx)
	if err != nil {
		return
	}
	var sub ctlMsg
	_ = json.Unmarshal(data, &sub)
	if f.subs != nil {
		select {
		case f.subs <- sub:
		default:
		}
	}
	_ = send(`[{"T":"subscription","trades":["*"],"quotes":[],"bars":[]}]`)
	// Drain control frames in the background so client writes never block.
	go func() {
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	}()
	if f.session != nil {
		f.session(ctx, send, n)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func startStream(t *testing.T, f *fakeStream, now func() time.Time, stale time.Duration) (*Client, chan core.Agg, chan struct{}, context.CancelFunc) {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := New(Config{
		Keys:       Keys{KeyID: testKeyID, Secret: testSecret},
		WSURL:      "ws" + strings.TrimPrefix(srv.URL, "http"),
		Now:        now,
		StaleAfter: stale,
	})
	out := make(chan core.Agg, 1024)
	resets := make(chan struct{}, 16)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = c.Stream(ctx, out, func() { resets <- struct{}{} })
	}()
	return c, out, resets, cancel
}

func recvAgg(t *testing.T, ch <-chan core.Agg) core.Agg {
	t.Helper()
	select {
	case a := <-ch:
		return a
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for an aggregate")
	}
	return core.Agg{}
}

func TestStreamHandshakeBatchedTradesToAggs(t *testing.T) {
	subs := make(chan ctlMsg, 1)
	f := &fakeStream{t: t, subs: subs, session: func(ctx context.Context, send func(string) error, _ int32) {
		// One frame, several messages (Alpaca batches), mixed types.
		_ = send(`[{"T":"t","S":"AAPL","i":1,"x":"V","p":150.1,"s":100,"c":["@"],"z":"C","t":"2026-10-27T12:00:00.100Z"},` +
			`{"T":"t","S":"AAPL","i":2,"x":"V","p":150.3,"s":50,"c":["@","I"],"z":"C","t":"2026-10-27T12:00:00.500Z"},` +
			`{"T":"b","S":"AAPL","o":1,"h":1,"l":1,"c":1,"v":1,"t":"2026-10-27T11:59:00Z","n":1,"vw":1},` +
			`{"T":"t","S":"AAPL","i":3,"x":"V","p":150.2,"s":200,"c":["@"],"z":"C","t":"2026-10-27T12:00:01.200Z"}]`)
		<-ctx.Done()
	}}
	// Clock far ahead so the flusher closes the second bucket too.
	_, out, resets, _ := startStream(t, f, func() time.Time { return time.Date(2026, 10, 27, 12, 1, 0, 0, time.UTC) }, -1)
	select {
	case <-resets:
	case <-time.After(5 * time.Second):
		t.Fatal("no onReset")
	}
	sub := <-subs
	if len(sub.Trades) != 1 || sub.Trades[0] != "*" || sub.Bars != nil {
		t.Fatalf("subscription %+v", sub)
	}
	a := recvAgg(t, out)
	if a.Sym != "AAPL" || a.Close != 150.1 || a.Vol != 150 || a.EndMs != time.Date(2026, 10, 27, 12, 0, 1, 0, time.UTC).UnixMilli() {
		t.Fatalf("first agg %+v", a)
	}
	b := recvAgg(t, out)
	if b.Close != 150.2 || b.Vol != 200 || b.AccVol != 350 {
		t.Fatalf("second agg %+v", b)
	}
}

func TestStreamReconnectsAfterDrop(t *testing.T) {
	f := &fakeStream{t: t, session: func(ctx context.Context, send func(string) error, n int32) {
		if n == 1 {
			return // drop the first connection right after subscribing
		}
		<-ctx.Done()
	}}
	_, _, resets, _ := startStream(t, f, time.Now, -1)
	for i := 0; i < 2; i++ {
		select {
		case <-resets:
		case <-time.After(10 * time.Second):
			t.Fatalf("reset %d never came", i+1)
		}
	}
	if f.conns.Load() < 2 {
		t.Fatalf("conns %d", f.conns.Load())
	}
}

func TestStreamStaleWatchdogReconnects(t *testing.T) {
	f := &fakeStream{t: t, session: func(ctx context.Context, send func(string) error, n int32) {
		<-ctx.Done() // silent: no trades at all
	}}
	// A weekday 10:00 ET: silence is abnormal.
	at := time.Date(2026, 10, 27, 10, 0, 0, 0, etLoc)
	var calls atomic.Int64
	now := func() time.Time { return at.Add(time.Duration(calls.Add(1)) * 100 * time.Millisecond) }
	_, _, resets, _ := startStream(t, f, now, 200*time.Millisecond)
	for i := 0; i < 2; i++ {
		select {
		case <-resets:
		case <-time.After(10 * time.Second):
			t.Fatalf("stale reconnect %d never came", i+1)
		}
	}
}

func TestStreamErrorCodes(t *testing.T) {
	for _, tc := range []struct {
		code int
		want error
	}{{406, ErrConnLimit}, {409, ErrSubscription}, {401, ErrAuth}} {
		srv := httptest.NewServer(&fakeStream{t: t, authCode: tc.code})
		c := New(Config{Keys: Keys{KeyID: testKeyID, Secret: testSecret}, WSURL: "ws" + strings.TrimPrefix(srv.URL, "http"), StaleAfter: -1})
		err := c.streamOnce(context.Background(), make(chan core.Agg, 1), nil, StreamOptions{})
		srv.Close()
		if !errors.Is(err, tc.want) {
			t.Errorf("code %d: got %v want %v", tc.code, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), testSecret) {
			t.Errorf("secret in error")
		}
	}
	// Wrong keys -> 402 auth failed.
	srv := httptest.NewServer(&fakeStream{t: t})
	defer srv.Close()
	c := New(Config{Keys: Keys{KeyID: "x", Secret: "y"}, WSURL: "ws" + strings.TrimPrefix(srv.URL, "http"), StaleAfter: -1})
	if err := c.streamOnce(context.Background(), make(chan core.Agg, 1), nil, StreamOptions{}); !errors.Is(err, ErrAuth) {
		t.Errorf("bad keys: %v", err)
	}
}

func TestStreamTapeTeesWatchedTradesAndQuotes(t *testing.T) {
	sink := make(chan core.TapeEvent, 16)
	ready := make(chan struct{})
	f := &fakeStream{t: t, session: func(ctx context.Context, send func(string) error, _ int32) {
		<-ready
		_ = send(`[{"T":"t","S":"OTHER","p":1,"s":100,"c":["@"],"t":"2026-10-27T12:00:00Z"},` +
			`{"T":"t","S":"WAT","p":2,"s":300,"c":["@"],"t":"2026-10-27T12:00:00Z"},` +
			`{"T":"q","S":"WAT","bp":1.99,"bs":3,"ap":2.01,"as":4,"c":["R"],"t":"2026-10-27T12:00:00Z"}]`)
		<-ctx.Done()
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := New(Config{Keys: Keys{KeyID: testKeyID, Secret: testSecret}, WSURL: "ws" + strings.TrimPrefix(srv.URL, "http"), StaleAfter: -1, TapeSink: sink})
	c.WatchTapeSet([]string{"WAT"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Stream(ctx, make(chan core.Agg, 64), func() { close(ready) }) }()
	var gotTrade, gotQuote bool
	for !(gotTrade && gotQuote) {
		select {
		case ev := <-sink:
			if ev.Trade != nil {
				if ev.Trade.Sym != "WAT" || ev.Trade.Size != 300 {
					t.Fatalf("trade %+v", ev.Trade)
				}
				gotTrade = true
			}
			if ev.Quote != nil {
				if ev.Quote.BidSz != 300 || ev.Quote.AskSz != 400 {
					t.Fatalf("quote %+v", ev.Quote)
				}
				gotQuote = true
			}
		case <-time.After(5 * time.Second):
			t.Fatal("tape events missing")
		}
	}
}
