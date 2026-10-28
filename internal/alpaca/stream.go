package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

// ---- wire -----------------------------------------------------------------

// wsMsg is the union of every Alpaca stream message we read. "c" is
// conditions ([]string) on trades/quotes but the close (number) on bars, so it
// stays raw. Every key that differs only by case ("T"/"t", "S"/"s") is
// declared on both sides so encoding/json's case-insensitive fallback can
// never cross-assign them.
type wsMsg struct {
	T    string          `json:"T"`
	Sym  string          `json:"S"`
	Ts   string          `json:"t"`
	Px   float64         `json:"p"`
	Size float64         `json:"s"`
	C    json.RawMessage `json:"c"`
	O    float64         `json:"o"`
	H    float64         `json:"h"`
	L    float64         `json:"l"`
	V    float64         `json:"v"`
	VW   float64         `json:"vw"`
	N    int64           `json:"n"`
	BP   float64         `json:"bp"`
	BS   float64         `json:"bs"`
	AP   float64         `json:"ap"`
	AS   float64         `json:"as"`
	Msg  string          `json:"msg"`
	Code int             `json:"code"`
}

// MinuteBar is a streamed 1-minute bar ("b" = final, "u" = updated/corrected).
type MinuteBar struct {
	Sym     string
	Updated bool
	Bar     core.Bar
	VWAP    float64
	Trades  int64
}

type ctlErr struct {
	Code int
	Msg  string
}

// decoded is one frame's content.
type decoded struct {
	trades []Trade
	quotes []core.Quote
	bars   []MinuteBar
	succ   []string // success msgs ("connected", "authenticated")
	subs   int      // subscription acks
	errs   []ctlErr
}

// quoteLot: SIP quote sizes arrive in round lots (normalized to shares, as
// internal/feed does for Massive). UNVERIFIED for Alpaca - check with
// feedcompare against the tape before relying on quote sizes.
const quoteLot = 100

func decodeFrame(data []byte) (decoded, error) {
	var msgs []wsMsg
	if err := json.Unmarshal(data, &msgs); err != nil {
		var one wsMsg
		if json.Unmarshal(data, &one) != nil {
			return decoded{}, err
		}
		msgs = []wsMsg{one}
	}
	var d decoded
	for i := range msgs {
		m := &msgs[i]
		switch m.T {
		case "t":
			var conds []string
			if len(m.C) > 0 {
				_ = json.Unmarshal(m.C, &conds)
			}
			d.trades = append(d.trades, Trade{Sym: m.Sym, Price: m.Px, Size: int64(m.Size), TS: parseTS(m.Ts), Conds: conds})
		case "q":
			d.quotes = append(d.quotes, core.Quote{Sym: m.Sym, Bid: m.BP, Ask: m.AP,
				BidSz: int64(m.BS) * quoteLot, AskSz: int64(m.AS) * quoteLot, TS: parseTS(m.Ts)})
		case "b", "u":
			var cl float64
			if len(m.C) > 0 {
				_ = json.Unmarshal(m.C, &cl)
			}
			d.bars = append(d.bars, MinuteBar{Sym: m.Sym, Updated: m.T == "u", VWAP: m.VW, Trades: m.N,
				Bar: core.Bar{T: parseTS(m.Ts), O: m.O, H: m.H, L: m.L, C: cl, V: int64(m.V)}})
		case "success":
			d.succ = append(d.succ, m.Msg)
		case "subscription":
			d.subs++
		case "error":
			d.errs = append(d.errs, ctlErr{Code: m.Code, Msg: m.Msg})
		}
	}
	return d, nil
}

// ---- errors ---------------------------------------------------------------

var (
	// ErrAuth: 402 auth failed / 401 not authenticated / 404 auth timeout.
	ErrAuth = errors.New("alpaca stream auth failed")
	// ErrConnLimit: 406 - another connection with these keys is open
	// (Alpaca allows one stream connection per key per feed).
	ErrConnLimit = errors.New("alpaca stream connection limit exceeded (another client is using these keys)")
	// ErrSubscription: 405 symbol limit / 409 insufficient subscription.
	ErrSubscription = errors.New("alpaca stream subscription rejected")
	errStale        = errors.New("alpaca stream stale (no data)")
)

func ctlError(e ctlErr) error {
	switch e.Code {
	case 401, 402, 404:
		return fmt.Errorf("%w: %d %s", ErrAuth, e.Code, e.Msg)
	case 406:
		return fmt.Errorf("%w: %d %s", ErrConnLimit, e.Code, e.Msg)
	case 405, 409, 410:
		return fmt.Errorf("%w: %d %s", ErrSubscription, e.Code, e.Msg)
	}
	return fmt.Errorf("alpaca stream error %d: %s", e.Code, e.Msg)
}

// ---- options for the probe tool ------------------------------------------

// StreamOptions are extras used by cmd/feedcompare; the scanner passes none.
type StreamOptions struct {
	Bars    chan<- MinuteBar            // also subscribe bars + updatedBars ("*" or Symbols)
	OnTrade func(t Trade, recvMs int64) // raw trade hook (latency measurement)
}

// ---- connection -----------------------------------------------------------

type wsConnRef struct {
	conn *websocket.Conn
	ctx  context.Context
}

type ctlMsg struct {
	Action      string   `json:"action"`
	Key         string   `json:"key,omitempty"`
	Secret      string   `json:"secret,omitempty"`
	Trades      []string `json:"trades,omitempty"`
	Quotes      []string `json:"quotes,omitempty"`
	Bars        []string `json:"bars,omitempty"`
	UpdatedBars []string `json:"updatedBars,omitempty"`
}

func (c *Client) tradeSyms() []string {
	if len(c.symbols) > 0 {
		return c.symbols
	}
	return []string{"*"}
}

// Stream implements core.FeedClient: subscribe to whole-market trades and
// deliver per-second aggregates built from them.
func (c *Client) Stream(ctx context.Context, out chan<- core.Agg, onReset func()) error {
	return c.StreamWith(ctx, out, onReset, StreamOptions{})
}

// StreamWith is Stream with probe options. It reconnects with capped backoff
// on every error (auth included, so fixed keys recover without a restart).
func (c *Client) StreamWith(ctx context.Context, out chan<- core.Agg, onReset func(), opt StreamOptions) error {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}
		if !c.HasKey() {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
			}
			continue
		}
		start := c.now()
		err := c.streamOnce(ctx, out, onReset, opt)
		if err == nil {
			return nil
		}
		if c.now().Sub(start) > 2*time.Minute {
			backoff = time.Second // it ran fine for a while: reconnect quickly
		}
		wait := backoff
		switch {
		case errors.Is(err, ErrConnLimit):
			wait = max(wait, 30*time.Second)
			logx.L().Warn("alpaca stream: connection limit; is feedcompare or another app using these keys?", "err", err)
		case errors.Is(err, ErrAuth), errors.Is(err, ErrSubscription):
			wait = max(wait, 30*time.Second)
			logx.L().Warn("alpaca stream rejected; will retry", "err", err)
		default:
			logx.L().Warn("alpaca stream dropped; reconnecting", "err", err, "in", wait)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (c *Client) streamOnce(ctx context.Context, out chan<- core.Agg, onReset func(), opt StreamOptions) error {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	conn, _, err := websocket.Dial(dialCtx, c.wsURL, nil)
	cancel()
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(64 << 20)

	hsCtx, hsCancel := context.WithTimeout(ctx, 20*time.Second)
	err = func() error {
		if e := waitFor(hsCtx, conn, "connected"); e != nil {
			return e
		}
		k := c.keys()
		if e := writeCtl(hsCtx, conn, ctlMsg{Action: "auth", Key: k.KeyID, Secret: k.Secret}); e != nil {
			return e
		}
		if e := waitFor(hsCtx, conn, "authenticated"); e != nil {
			return e
		}
		sub := ctlMsg{Action: "subscribe", Trades: c.tradeSyms()}
		if q := c.watchedSyms(); len(q) > 0 {
			sub.Quotes = q
		}
		if opt.Bars != nil {
			sub.Bars, sub.UpdatedBars = c.tradeSyms(), c.tradeSyms()
		}
		if e := writeCtl(hsCtx, conn, sub); e != nil {
			return e
		}
		return waitFor(hsCtx, conn, "")
	}()
	hsCancel()
	if err != nil {
		return err
	}
	if onReset != nil {
		onReset()
	}

	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()
	c.connp.Store(&wsConnRef{conn: conn, ctx: connCtx})
	defer c.connp.Store(nil)

	var lastData atomic.Int64
	lastData.Store(c.now().UnixMilli())
	var stale atomic.Bool
	go pinger(connCtx, conn)
	go c.flusher(connCtx, out)
	go c.watchdog(connCtx, conn, &lastData, &stale)

	tape := c.tapeSink
	for {
		_, data, rerr := conn.Read(connCtx)
		if rerr != nil {
			if ctx.Err() != nil {
				return nil
			}
			if stale.Load() {
				return errStale
			}
			return fmt.Errorf("read: %w", rerr)
		}
		recv := c.now().UnixMilli()
		lastData.Store(recv)
		d, derr := decodeFrame(data)
		if derr != nil {
			logx.L().Debug("alpaca frame decode failed", "err", derr)
			continue
		}
		var watched map[string]struct{}
		if p := c.tapeSyms.Load(); p != nil && tape != nil {
			watched = *p
		}
		for _, t := range d.trades {
			if opt.OnTrade != nil {
				opt.OnTrade(t, recv)
			}
			if _, ok := watched[t.Sym]; ok {
				tr := core.Trade{Sym: t.Sym, Price: t.Price, Size: t.Size, TS: t.TS}
				select {
				case tape <- core.TapeEvent{Trade: &tr}:
				default:
				}
			}
			for _, a := range c.sagg.Add(t) {
				select {
				case out <- a:
				case <-ctx.Done():
					return nil
				}
			}
		}
		if tape != nil {
			for i := range d.quotes {
				q := d.quotes[i]
				select {
				case tape <- core.TapeEvent{Quote: &q}:
				default:
				}
			}
		}
		if opt.Bars != nil {
			for _, b := range d.bars {
				select {
				case opt.Bars <- b:
				default:
				}
			}
		}
		for _, e := range d.errs {
			err := ctlError(e)
			if e.Code == 406 || e.Code == 401 || e.Code == 402 {
				return err
			}
			// Mid-stream errors are almost always a rejected quote (tape)
			// subscription; keep the whole-market trade stream alive.
			logx.L().Warn("alpaca stream error frame", "err", err)
			if c.tapeStatus != nil {
				c.tapeStatus(e.Msg)
			}
		}
	}
}

// flusher closes per-second buckets whose second has passed (with a grace
// period for prints that arrive slightly late) for symbols that stopped
// trading - a busy symbol's buckets close on its next trade instead.
const flushGrace = 1500 * time.Millisecond

func (c *Client) flusher(ctx context.Context, out chan<- core.Agg) {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, a := range c.sagg.FlushBefore(c.now().Add(-flushGrace).UnixMilli()) {
				select {
				case out <- a:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// activeHours: weekday 04:00-20:00 ET, when a whole-market trade stream is
// never silent for a minute. Outside it silence is normal.
func activeHours(t time.Time) bool {
	et := t.In(etLoc)
	if wd := et.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return false
	}
	h := et.Hour()
	return h >= 4 && h < 20
}

func (c *Client) watchdog(ctx context.Context, conn *websocket.Conn, last *atomic.Int64, stale *atomic.Bool) {
	if c.staleAfter < 0 {
		return
	}
	period := min(c.staleAfter/4, 5*time.Second)
	if period <= 0 {
		period = 50 * time.Millisecond
	}
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := c.now()
			if len(c.symbols) == 0 && activeHours(now) && now.UnixMilli()-last.Load() > c.staleAfter.Milliseconds() {
				stale.Store(true)
				conn.CloseNow()
				return
			}
		}
	}
}

// waitFor reads control frames until a success with msg == want (or, for
// want == "", a subscription ack). Error frames fail with a typed error.
func waitFor(ctx context.Context, conn *websocket.Conn, want string) error {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		d, err := decodeFrame(data)
		if err != nil {
			continue
		}
		if len(d.errs) > 0 {
			return ctlError(d.errs[0])
		}
		if want == "" && d.subs > 0 {
			return nil
		}
		for _, s := range d.succ {
			if s == want {
				return nil
			}
		}
	}
}

func writeCtl(ctx context.Context, conn *websocket.Conn, m ctlMsg) error {
	b, _ := json.Marshal(m)
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, b)
}

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

// ---- tape (time & sales for watched symbols) -----------------------------

// WatchTapeSet: trades for every symbol already arrive on the wildcard trade
// subscription, so only QUOTES are (un)subscribed per symbol; watched trades
// are teed to the tape sink by the reader.
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
		return
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
	sort.Strings(added)
	sort.Strings(removed)
	if len(removed) > 0 {
		_ = writeCtl(ref.ctx, ref.conn, ctlMsg{Action: "unsubscribe", Quotes: removed})
	}
	if len(added) > 0 {
		if err := writeCtl(ref.ctx, ref.conn, ctlMsg{Action: "subscribe", Quotes: added}); err != nil {
			logx.L().Warn("alpaca quote subscribe failed", "syms", added, "err", err)
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
	sort.Strings(out)
	return out
}
