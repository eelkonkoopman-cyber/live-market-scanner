package feed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func wsURLOf(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func TestStreamHandshakeAndAgg(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		ctx := r.Context()
		send := func(s string) error { return conn.Write(ctx, websocket.MessageText, []byte(s)) }

		if send(`[{"ev":"status","status":"connected","message":"Connected"}]`) != nil {
			return
		}
		if _, _, err := conn.Read(ctx); err != nil { // auth
			return
		}
		if send(`[{"ev":"status","status":"auth_success","message":"ok"}]`) != nil {
			return
		}
		if _, _, err := conn.Read(ctx); err != nil { // subscribe
			return
		}
		if send(`[{"ev":"status","status":"success","message":"subscribed to: A.*"}]`) != nil {
			return
		}
		_ = send(`[{"ev":"A","sym":"AAPL","v":100,"av":2000,"op":150,"vw":150.5,"o":150.1,"c":150.7,"h":151,"l":149.9,"a":150.4,"z":50,"s":1700000000000,"e":1700000001000}]`)
		<-ctx.Done()
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", WSURL: wsURLOf(srv)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan core.Agg, 4)
	reset := make(chan struct{}, 1)
	go func() {
		_ = c.Stream(ctx, out, func() {
			select {
			case reset <- struct{}{}:
			default:
			}
		})
	}()

	select {
	case <-reset:
	case <-time.After(5 * time.Second):
		t.Fatal("onReset not called after subscribe")
	}
	select {
	case a := <-out:
		if a.Sym != "AAPL" || a.Close != 150.7 || a.Vol != 100 || a.AccVol != 2000 {
			t.Fatalf("agg mismatch: %+v", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no agg received")
	}
}

// TestWatchTapeSetSubscribesBatchedSetOnConnect pre-arms a watched-symbol set
// BEFORE the connection exists, then confirms the handshake's post-subscribe
// resubscribe step sends the WHOLE set in one control frame, not one frame
// per symbol.
func TestWatchTapeSetSubscribesBatchedSetOnConnect(t *testing.T) {
	var tapeFrame string
	tapeRead := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		ctx := r.Context()
		send := func(s string) error { return conn.Write(ctx, websocket.MessageText, []byte(s)) }

		if send(`[{"ev":"status","status":"connected","message":"Connected"}]`) != nil {
			return
		}
		if _, _, err := conn.Read(ctx); err != nil { // auth
			return
		}
		if send(`[{"ev":"status","status":"auth_success","message":"ok"}]`) != nil {
			return
		}
		if _, _, err := conn.Read(ctx); err != nil { // subscribe A.*
			return
		}
		if send(`[{"ev":"status","status":"success","message":"subscribed to: A.*"}]`) != nil {
			return
		}
		_, data, err := conn.Read(ctx) // the tape resubscribe frame
		if err != nil {
			return
		}
		tapeFrame = string(data)
		tapeRead <- struct{}{}
		<-ctx.Done()
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", WSURL: wsURLOf(srv)})
	c.WatchTapeSet([]string{"AAPL", "MSFT"}) // no connection yet -> stored, applied on connect

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Stream(ctx, make(chan core.Agg, 4), nil) }()

	select {
	case <-tapeRead:
	case <-time.After(5 * time.Second):
		t.Fatal("no tape resubscribe frame received on connect")
	}
	for _, want := range []string{"T.AAPL", "Q.AAPL", "T.MSFT", "Q.MSFT"} {
		if !strings.Contains(tapeFrame, want) {
			t.Fatalf("resubscribe frame missing %q: %s", want, tapeFrame)
		}
	}
}

// TestWatchTapeSetDiffsAddedAndRemovedAfterConnect proves mid-stream
// reconciliation: adding a symbol subscribes just that one, and swapping one
// symbol for another issues an unsubscribe for the dropped one and a
// subscribe for the new one — never a blanket resubscribe of everything.
func TestWatchTapeSetDiffsAddedAndRemovedAfterConnect(t *testing.T) {
	frames := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		ctx := r.Context()
		send := func(s string) error { return conn.Write(ctx, websocket.MessageText, []byte(s)) }

		if send(`[{"ev":"status","status":"connected","message":"Connected"}]`) != nil {
			return
		}
		if _, _, err := conn.Read(ctx); err != nil { // auth
			return
		}
		if send(`[{"ev":"status","status":"auth_success","message":"ok"}]`) != nil {
			return
		}
		if _, _, err := conn.Read(ctx); err != nil { // subscribe A.*
			return
		}
		if send(`[{"ev":"status","status":"success","message":"subscribed to: A.*"}]`) != nil {
			return
		}
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			select {
			case frames <- string(data):
			default:
			}
		}
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", WSURL: wsURLOf(srv)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reset := make(chan struct{}, 1)
	go func() {
		_ = c.Stream(ctx, make(chan core.Agg, 4), func() {
			select {
			case reset <- struct{}{}:
			default:
			}
		})
	}()
	select {
	case <-reset:
	case <-time.After(5 * time.Second):
		t.Fatal("stream never connected")
	}

	c.WatchTapeSet([]string{"AAPL"})
	select {
	case f := <-frames:
		if !strings.Contains(f, "T.AAPL") || !strings.Contains(f, "Q.AAPL") {
			t.Fatalf("want a subscribe frame for AAPL, got %q", f)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no subscribe frame for AAPL")
	}

	c.WatchTapeSet([]string{"MSFT"}) // drop AAPL, add MSFT
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case f := <-frames:
			switch {
			case strings.Contains(f, "AAPL"):
				seen["unsub_aapl"] = true
			case strings.Contains(f, "MSFT"):
				seen["sub_msft"] = true
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("only got %d of 2 expected frames", i)
		}
	}
	if !seen["unsub_aapl"] || !seen["sub_msft"] {
		t.Fatalf("want an AAPL unsubscribe AND an MSFT subscribe, got %+v", seen)
	}
}

func TestStreamAuthFailedRetriesThenStops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		ctx := r.Context()
		_ = conn.Write(ctx, websocket.MessageText, []byte(`[{"ev":"status","status":"connected"}]`))
		_, _, _ = conn.Read(ctx) // auth
		_ = conn.Write(ctx, websocket.MessageText, []byte(`[{"ev":"status","status":"auth_failed","message":"bad key"}]`))
		<-ctx.Done()
	}))
	defer srv.Close()

	c := New(Config{APIKey: "bad", WSURL: wsURLOf(srv)})
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	out := make(chan core.Agg, 1)
	reset := make(chan struct{}, 1)
	// Auth keeps failing -> Stream retries (no subscribe -> onReset never fires) and
	// returns nil only when ctx is cancelled. It must not hang or panic.
	err := c.Stream(ctx, out, func() { reset <- struct{}{} })
	if err != nil {
		t.Fatalf("Stream should return nil on ctx cancel, got %v", err)
	}
	select {
	case <-reset:
		t.Fatal("onReset must not fire when auth keeps failing")
	default:
	}
}
