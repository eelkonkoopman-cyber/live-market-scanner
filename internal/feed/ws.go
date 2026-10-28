package feed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

type ctlMsg struct {
	Action string `json:"action"`
	Params string `json:"params"`
}

// wsConnRef is the live connection handle published for mid-stream control
// writes (tape subscribe/unsubscribe). ctx dies with the connection, so a
// write attempted against a dead conn fails fast instead of hanging.
type wsConnRef struct {
	conn *websocket.Conn
	ctx  context.Context
}

// tapeTopicsFor renders the trade+quote subscription params for a set of
// symbols, comma-joined so N symbols cost one control frame, not N.
func tapeTopicsFor(syms []string) string {
	parts := make([]string, 0, len(syms)*2)
	for _, s := range syms {
		parts = append(parts, "T."+s, "Q."+s)
	}
	return strings.Join(parts, ",")
}

// WatchTapeSet reconciles the watched trade/quote symbol set to exactly syms,
// issuing at most one batched unsubscribe frame and one batched subscribe
// frame — N open windows cost 2 control messages, not 2N. It re-uses the ONE
// live aggregate connection — Massive allows mixing channels and only permits
// ~1 connection per key, so opening a second would disconnect the first. If
// no connection is live right now, the desired set is stored and applied
// wholesale on the next (re)connect.
func (c *Client) WatchTapeSet(syms []string) {
	next := make(map[string]struct{}, len(syms))
	for _, s := range syms {
		if s != "" {
			next[s] = struct{}{}
		}
	}
	var prev map[string]struct{}
	if p := c.tapeSyms.Load(); p != nil {
		prev = *p
	}
	c.tapeSyms.Store(&next)

	ref := c.connp.Load()
	if ref == nil {
		return // applied on next connect
	}
	var added, removed []string
	for s := range next {
		if _, ok := prev[s]; !ok {
			added = append(added, s)
		}
	}
	for s := range prev {
		if _, ok := next[s]; !ok {
			removed = append(removed, s)
		}
	}
	if len(removed) > 0 {
		if err := c.writeCtl(ref.ctx, ref.conn, "unsubscribe", tapeTopicsFor(removed)); err != nil {
			logx.L().Debug("tape unsubscribe failed", "syms", removed, "err", err)
		}
	}
	if len(added) > 0 {
		if err := c.writeCtl(ref.ctx, ref.conn, "subscribe", tapeTopicsFor(added)); err != nil {
			logx.L().Warn("tape subscribe failed", "syms", added, "err", err)
		}
	}
}

func (c *Client) watchedSyms() []string {
	p := c.tapeSyms.Load()
	if p == nil || len(*p) == 0 {
		return nil
	}
	out := make([]string, 0, len(*p))
	for s := range *p {
		out = append(out, s)
	}
	return out
}

// Stream connects, subscribes to A.*, and delivers aggregates on out until ctx
// is done. It reconnects with capped backoff on transient errors, invoking
// onReset after each successful (re)subscribe so the caller can re-seed
// day-state. It returns nil on ctx cancellation and errFatal on a config error
// (bad key / forbidden subscription) where retrying is pointless.
func (c *Client) Stream(ctx context.Context, out chan<- core.Agg, onReset func()) error {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}
		// No key yet (e.g. user hasn't pasted one) — wait and re-check rather than
		// dialing pointlessly. Recovers automatically once SetKey is called.
		if !c.HasKey() {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
			}
			continue
		}
		err := c.streamOnce(ctx, out, onReset)
		if err == nil {
			return nil // ctx done
		}
		// Retry ALL errors (including auth) with backoff, so changing the key at
		// runtime recovers without a restart. A persistently bad key just retries
		// every ~30s (logged), not hammered.
		if errors.Is(err, errFatal) {
			logx.L().Warn("feed auth/subscribe rejected; will retry", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (c *Client) streamOnce(ctx context.Context, out chan<- core.Agg, onReset func()) error {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	conn, _, err := websocket.Dial(dialCtx, c.wsURL, nil)
	cancel()
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.CloseNow()       // immediate close (no handshake wait) — snappy reconnect on error
	conn.SetReadLimit(32 << 20) // whole-market frames can be large

	// Bounded handshake: connected -> auth -> auth_success -> subscribe.
	hsCtx, hsCancel := context.WithTimeout(ctx, 20*time.Second)
	err = func() error {
		if e := c.waitStatus(hsCtx, conn, "connected"); e != nil {
			return e
		}
		if e := c.writeCtl(ctx, conn, "auth", c.key()); e != nil {
			return e
		}
		if e := c.waitStatus(hsCtx, conn, "auth_success"); e != nil {
			return e
		}
		return c.writeCtl(ctx, conn, "subscribe", subTopic)
	}()
	hsCancel()
	if err != nil {
		return err
	}

	if onReset != nil {
		onReset()
	}
	// Per-connection context so the pinger dies when this connection ends, rather
	// than leaking one goroutine per reconnect onto the outer stream ctx.
	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()
	go pinger(connCtx, conn)

	// Publish the connection for mid-stream tape (un)subscribes, and re-apply
	// the watched symbol set — a reconnect must restore every window's tape,
	// not just aggregates.
	c.connp.Store(&wsConnRef{conn: conn, ctx: connCtx})
	defer c.connp.Store(nil)
	if syms := c.watchedSyms(); len(syms) > 0 {
		if err := c.writeCtl(ctx, conn, "subscribe", tapeTopicsFor(syms)); err != nil {
			logx.L().Warn("tape resubscribe failed", "syms", syms, "err", err)
		}
	}

	dec := newFrameDecoder() // this connection's read goroutine owns it
	for {
		if ctx.Err() != nil {
			return nil
		}
		_, data, rerr := conn.Read(ctx)
		if rerr != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read: %w", rerr)
		}
		aggs, sts, tape := dec.decode(data)
		for _, a := range aggs {
			select {
			case out <- a:
			case <-ctx.Done():
				return nil
			}
		}
		for _, ev := range tape {
			if c.tapeSink == nil {
				break
			}
			select {
			case c.tapeSink <- ev:
			default: // tape engine behind; drop (flow metrics tolerate gaps)
			}
		}
		// Mid-stream status frames are almost always tape subscribe failures
		// (the aggregate subscription was already accepted during the
		// handshake). Report them to the tape UI rather than tearing down the
		// connection — the whole-market feed must survive an unentitled tape.
		for _, s := range sts {
			if s.Status == "error" || s.Status == "max_subscriptions" {
				logx.L().Warn("tape subscription rejected", "status", s.Status, "msg", s.Message)
				if c.tapeStatus != nil {
					c.tapeStatus(s.Message)
				}
			}
		}
	}
}

// waitStatus reads frames until a status == want, failing fast (errFatal) on
// auth_failed / error so a bad key is reported, not retried forever.
func (c *Client) waitStatus(ctx context.Context, conn *websocket.Conn, want string) error {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		_, sts, _ := decodeFrame(data)
		for _, s := range sts {
			switch s.Status {
			case want:
				return nil
			case "connected":
				// keep reading toward auth_success
			case "auth_failed":
				return fmt.Errorf("%w: auth_failed: %s", errFatal, s.Message)
			case "error", "max_subscriptions":
				return fmt.Errorf("%w: %s: %s", errFatal, s.Status, s.Message)
			}
		}
	}
}

func (c *Client) writeCtl(ctx context.Context, conn *websocket.Conn, action, params string) error {
	b, _ := json.Marshal(ctlMsg{Action: action, Params: params})
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, b)
}

// pinger keeps the connection alive so a quiet market doesn't look dead.
func pinger(ctx context.Context, conn *websocket.Conn) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_ = conn.Ping(pctx)
			cancel()
		}
	}
}
