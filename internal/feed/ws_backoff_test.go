package feed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// A connection that ran healthily must reset the reconnect backoff: before the
// fix every drop doubled it for good (32 s blind after five drops).
func TestStreamBackoffResetsAfterHealthyConnection(t *testing.T) {
	old := backoffResetAfter
	backoffResetAfter = 50 * time.Millisecond
	defer func() { backoffResetAfter = old }()

	var conns atomic.Int32
	third := make(chan time.Time, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		send := func(s string) error { return conn.Write(ctx, websocket.MessageText, []byte(s)) }
		if send(`[{"ev":"status","status":"connected"}]`) != nil {
			return
		}
		if _, _, err := conn.Read(ctx); err != nil {
			return
		}
		if send(`[{"ev":"status","status":"auth_success"}]`) != nil {
			return
		}
		if _, _, err := conn.Read(ctx); err != nil {
			return
		}
		if conns.Add(1) == 3 {
			third <- time.Now()
		}
		time.Sleep(120 * time.Millisecond) // healthy for longer than the reset threshold, then drop
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", WSURL: wsURLOf(srv)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	go func() { _ = c.Stream(ctx, make(chan core.Agg, 4), nil) }()
	select {
	case at := <-third:
		// with the reset: 1 s + 1 s of backoff (~2.3 s); without: 1 s + 2 s (~3.3 s)
		if d := at.Sub(start); d > 2900*time.Millisecond {
			t.Fatalf("third connection after %v: backoff did not reset", d)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("never reconnected twice")
	}
}
