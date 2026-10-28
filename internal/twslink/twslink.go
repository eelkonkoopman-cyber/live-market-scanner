// Package twslink pushes the selected ticker into Interactive Brokers TWS via
// the officially documented Display Groups API — the same colored chain-link
// groups TWS uses to sync its own windows. Clicking a row in the scanner
// switches every TWS window chained to the configured group.
//
// Why the socket API and not window automation: TWS is a Java Swing app that
// exposes no UI Automation tree, so keystroke injection would be blind and
// would steal focus. updateDisplayGroup is IB's supported path and touches no
// window focus at all, which matters when TWS lives on a second monitor.
//
// SAFETY: this client implements ONLY the handshake, symbol lookup, and the
// four display-group messages. It has no encoder for placeOrder or any other
// order/account request, so it cannot transmit a trade.
package twslink

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/ibwire"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

// Outgoing message IDs (verified against the reference IB client).
const (
	outReqMktData       = 1
	outCancelMktData    = 2
	outReqMktDepth      = 10
	outCancelMktDepth   = 11
	outStartAPI         = 71
	outReqMatchingSyms  = 81
	outSubscribeGroup   = 68
	outUpdateDisplayGrp = 69
	outUnsubscribeGroup = 70
)

// Incoming message IDs we care about; everything else is ignored.
const (
	inTickSize      = 2
	inErrMsg        = 4
	inNextValidID   = 9 // marks the end of the startApi handshake
	inMarketDepth   = 12
	inMarketDepthL2 = 13
	inTickGeneric   = 45
	inSymbolSamples = 79
)

// Tick TYPE ids delivered under generic tick 236 ("Shortable"). These are tick
// types, not message ids — 46 arrives via tickGeneric, 89 via tickSize.
const (
	tickShortable       = 46 // indicator: >2.5 = 1000+ shares, >1.5 = locatable, <=1.5 = no
	tickShortableShares = 89 // exact share count (needs TWS build 974+)

	genericTickShortable = "236"
)

const (
	subReqID        = 9001 // reqId reused by subscribe + updateDisplayGroup
	mktReqID        = 9002 // the single market-data line we ever hold
	depthReqID      = 9003 // the single market-depth line we ever hold
	depthNumRows    = 10
	lookupReqIDBase = 9100
	dialTimeout     = 4 * time.Second
)

// Config controls the TWS connection. Zero values use sane defaults.
type Config struct {
	Enabled  bool
	Addr     string // default 127.0.0.1:7496 (live) — 7497 is paper
	ClientID int    // must not collide with another API client (default 77)
	Group    int    // TWS colored link group 1..7 (default 1)
	Exchange string // routing used in conid@exchange (default SMART)
	// LiveShort subscribes generic tick 236 ("Shortable") for the selected
	// symbol, giving exact real-time borrow availability. Costs exactly ONE
	// market-data line — IBKR's default allowance is 100, shared with the TWS
	// GUI — because the previous subscription is cancelled on every switch.
	LiveShort bool
	// LiveDepth subscribes real Level 2 market depth (reqMktDepth) for the
	// selected symbol. Depth requests draw from the SAME scarce market-data
	// line pool as LiveShort/reqMktData (confirmed from IB's own docs, not
	// assumed) — so, like LiveShort, exactly one line is ever held: the
	// previous depth subscription is cancelled on every switch.
	LiveDepth bool
}

// Client keeps one persistent TWS connection and drives the display group.
type Client struct {
	cfg Config

	show      chan string // buffered 1; Show keeps only the latest pending request
	shortOut  chan core.ShortLive
	depthOut  chan core.DepthUpdate
	conn      net.Conn
	connected atomic.Bool
	lastErr   atomic.Pointer[string]

	// state owned by the session goroutine
	pending    map[int64]string // lookup reqId -> symbol awaiting resolution
	nextReqID  int64
	want       string // most recent symbol requested (sent once connected)
	subscribed bool
	mktOpen    bool           // a market-data line is currently held
	short      core.ShortLive // latest borrow state for `want`
	depthOpen  bool           // a market-depth line is currently held
	bookBids   []core.DepthLevel
	bookAsks   []core.DepthLevel

	// conIDs is the only state read from outside the session goroutine
	// (ConID, for tests/diagnostics), so it carries its own lock.
	cmu    sync.RWMutex
	conIDs map[string]int64 // symbol -> conId cache
}

// ConID returns the cached IB contract id for a symbol, if resolved.
func (c *Client) ConID(sym string) (int64, bool) {
	c.cmu.RLock()
	defer c.cmu.RUnlock()
	id, ok := c.conIDs[sym]
	return id, ok
}

func New(cfg Config) *Client {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:7496"
	}
	if cfg.ClientID == 0 {
		cfg.ClientID = 77
	}
	if cfg.Group <= 0 || cfg.Group > 7 {
		cfg.Group = 1
	}
	if cfg.Exchange == "" {
		cfg.Exchange = "SMART"
	}
	return &Client{
		cfg:       cfg,
		show:      make(chan string, 1),
		shortOut:  make(chan core.ShortLive, 8),
		depthOut:  make(chan core.DepthUpdate, 8),
		conIDs:    map[string]int64{},
		pending:   map[int64]string{},
		nextReqID: lookupReqIDBase,
	}
}

// ShortLive streams real-time borrow availability for the selected symbol.
func (c *Client) ShortLive() <-chan core.ShortLive { return c.shortOut }

// Depth streams real Level 2 market depth for the selected symbol.
func (c *Client) Depth() <-chan core.DepthUpdate { return c.depthOut }

// Show asks TWS to display sym. Non-blocking and safe before connection —
// only the latest pending request is kept: if the session loop isn't
// draining (e.g. TWS is disconnected/reconnecting) and a request is already
// queued, it is discarded in favor of sym, so a burst of clicks can never
// leave a stale symbol to replay once the link comes back.
func (c *Client) Show(sym string) {
	if !c.cfg.Enabled || sym == "" {
		return
	}
	for {
		select {
		case c.show <- sym:
			return
		default:
		}
		select {
		case <-c.show: // discard the stale queued request; the newer one wins
		default:
		}
	}
}

// Status reports link state for the UI/status bar.
func (c *Client) Status() (connected bool, err string) {
	if p := c.lastErr.Load(); p != nil {
		err = *p
	}
	return c.connected.Load(), err
}

func (c *Client) Run(ctx context.Context) error {
	if !c.cfg.Enabled {
		logx.L().Info("TWS link disabled")
		<-ctx.Done()
		return nil
	}
	backoff := 2 * time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := c.session(ctx); err != nil && ctx.Err() == nil {
			msg := err.Error()
			c.lastErr.Store(&msg)
			// "TWS isn't running yet" is the one expected failure, and it fails
			// at dial — stay quiet for that. Anything else means we connected
			// and then broke, which the user must be able to see: logging that
			// at Debug hid a connect/drop loop behind a UI that just said
			// "waiting".
			if strings.Contains(msg, "dial TWS") {
				logx.L().Debug("TWS not reachable yet", "err", err)
			} else {
				logx.L().Warn("TWS link dropped; reconnecting", "err", err)
			}
		}
		c.connected.Store(false)
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

// session runs one connection: handshake, subscribe, then serve show requests.
func (c *Client) session(ctx context.Context) error {
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", c.cfg.Addr)
	if err != nil {
		return fmt.Errorf("dial TWS %s: %w", c.cfg.Addr, err)
	}
	defer conn.Close()
	c.conn = conn

	// ONE buffered reader for the whole session. A second reader over the same
	// socket would silently discard whatever the first had already buffered
	// past the greeting, resyncing the stream mid-message — which killed the
	// connection immediately after every successful handshake.
	r := bufio.NewReaderSize(conn, 1<<16)

	srvVer, err := c.handshake(conn, r)
	if err != nil {
		return err
	}
	if err := c.send(outStartAPI, 2, c.cfg.ClientID, ""); err != nil {
		return err
	}
	logx.L().Info("TWS link connected", "addr", c.cfg.Addr, "serverVersion", srvVer, "group", c.cfg.Group)
	c.connected.Store(true)
	c.lastErr.Store(nil)
	c.subscribed = false
	// The old socket took any market-data subscription with it; clear the flag
	// so the re-request below doesn't try to cancel a line that no longer
	// exists (which TWS answers with a spurious error).
	c.mktOpen = false
	c.depthOpen = false

	// TWS silently CLOSES the socket if a request arrives before the startApi
	// handshake has finished — no error message, just EOF. Wait for
	// nextValidId, which IB emits at the end of that handshake, before sending
	// anything else.
	if err := c.awaitReady(conn, r); err != nil {
		return err
	}

	// Subscribing binds subReqID to the link group; updateDisplayGroup then
	// reuses that same id, which the API requires.
	if err := c.send(outSubscribeGroup, 1, subReqID, c.cfg.Group); err != nil {
		return err
	}
	c.subscribed = true
	if c.want != "" {
		c.request(c.want) // re-assert selection after a reconnect
	}

	msgs := make(chan []string, 64)
	errc := make(chan error, 1)
	go func() { errc <- c.readLoop(r, msgs) }()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errc:
			return err
		case sym := <-c.show:
			c.want = sym
			c.request(sym)
		case f := <-msgs:
			c.handle(f)
		}
	}
}

// request either sends the cached conId straight through or kicks off a lookup.
func (c *Client) request(sym string) {
	if id, ok := c.ConID(sym); ok {
		c.push(id)
		return
	}
	reqID := c.nextReqID
	c.nextReqID++
	c.pending[reqID] = sym
	if err := c.send(outReqMatchingSyms, reqID, sym); err != nil {
		logx.L().Debug("TWS symbol lookup failed", "sym", sym, "err", err)
	}
}

// push commands the group to display the contract, and re-points the single
// market-data line at it for live borrow availability. Fire-and-forget: TWS
// only replies on error.
func (c *Client) push(conID int64) {
	if !c.subscribed {
		return
	}
	info := strconv.FormatInt(conID, 10) + "@" + c.cfg.Exchange
	if err := c.send(outUpdateDisplayGrp, 1, subReqID, info); err != nil {
		logx.L().Debug("TWS updateDisplayGroup failed", "err", err)
	}
	c.watchShort(conID)
	c.watchDepth(conID)
}

// watchShort moves the one market-data subscription to conID and asks for
// generic tick 236 (Shortable). Exactly one line is ever held: the previous
// subscription is cancelled first, so this can never grow into the account's
// market-data line budget no matter how fast symbols are clicked.
func (c *Client) watchShort(conID int64) {
	if !c.cfg.LiveShort {
		return
	}
	if c.mktOpen {
		if err := c.send(outCancelMktData, 2, mktReqID); err != nil {
			logx.L().Debug("TWS cancelMktData failed", "err", err)
		}
		c.mktOpen = false
	}
	// Contract fields are positional; conId alone identifies it, so the rest
	// stay empty apart from the routing/type hints.
	err := c.send(
		outReqMktData, 11, mktReqID,
		conID,
		"",    // symbol (conId wins)
		"STK", // secType
		"",    // lastTradeDateOrContractMonth
		"",    // strike (unset)
		"",    // right
		"",    // multiplier
		c.cfg.Exchange,
		"",    // primaryExchange
		"USD", // currency
		"",    // localSymbol
		"",    // tradingClass
		0,     // deltaNeutralContract present = false
		genericTickShortable,
		0, // snapshot
		0, // regulatorySnapshot
		"",
	)
	if err != nil {
		logx.L().Debug("TWS reqMktData failed", "err", err)
		return
	}
	c.mktOpen = true
	c.short = core.ShortLive{Sym: c.want, Shares: -1, At: time.Now().UnixMilli()}
}

// onTick folds a shortability tick into the live state and publishes it.
func (c *Client) onTick(f []string, valueIdx int, isSize bool) {
	// [msgId, version, reqId, tickType, value]
	if len(f) < 5 {
		return
	}
	if reqID, err := strconv.ParseInt(f[2], 10, 64); err != nil || reqID != mktReqID {
		return
	}
	tickType, err := strconv.ParseInt(f[3], 10, 64)
	if err != nil {
		return
	}
	raw := strings.TrimSpace(f[valueIdx])
	switch {
	case !isSize && tickType == tickShortable:
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return
		}
		c.short.Indicator = v
	case isSize && tickType == tickShortableShares:
		// Sizes arrive as decimals and use a max-int sentinel for "unset".
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || v >= float64(math.MaxInt32) {
			return
		}
		c.short.Shares = int64(v)
	default:
		return
	}
	c.short.Sym = c.want
	c.short.At = time.Now().UnixMilli()
	select {
	case c.shortOut <- c.short:
	default: // UI behind; the next tick supersedes
	}
}

func (c *Client) handle(f []string) {
	if len(f) == 0 {
		return
	}
	switch f[0] {
	case strconv.Itoa(inSymbolSamples):
		c.onSymbolSamples(f)
	case strconv.Itoa(inTickGeneric):
		c.onTick(f, 4, false)
	case strconv.Itoa(inTickSize):
		c.onTick(f, 4, true)
	case strconv.Itoa(inMarketDepth):
		c.onDepth(f, false)
	case strconv.Itoa(inMarketDepthL2):
		c.onDepth(f, true)
	case strconv.Itoa(inErrMsg):
		// [4, version, id, errorTime?, code, msg, ...] — layout varies by
		// server version, so scan for the code rather than indexing blindly.
		// 21xx are informational status notices, not failures.
		//
		// f[2] is the "id" field (this app's own request id, or -1 for a
		// connection-level notice) at every server version we've observed —
		// used here to attribute a depth-specific error to our one depth
		// subscription without needing to parse the version-dependent
		// errorTime/code/msg fields positionally.
		isDepthErr := len(f) > 2 && f[2] == strconv.Itoa(depthReqID)
		for _, v := range f[1:] {
			if code, err := strconv.Atoi(v); err == nil && code >= 100 && code < 10000 {
				if code >= 2100 && code < 2200 {
					return
				}
				logx.L().Debug("TWS API notice", "code", code, "fields", f)
				if isDepthErr {
					c.onDepthError(f[len(f)-1])
				}
				return
			}
		}
	}
}

// onSymbolSamples resolves the pending lookup, preferring an exact ticker match
// on a US stock so "AA" doesn't land on some fuzzy relative.
func (c *Client) onSymbolSamples(f []string) {
	// [79, reqId, count, {conId, symbol, secType, primaryExch, currency, nDeriv, derivs..., desc, issuer}...]
	if len(f) < 3 {
		return
	}
	reqID, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil {
		return
	}
	want, ok := c.pending[reqID]
	if !ok {
		return
	}
	delete(c.pending, reqID)

	n, _ := strconv.Atoi(f[2])
	i := 3
	var best int64
	for k := 0; k < n && i+5 < len(f); k++ {
		conID, _ := strconv.ParseInt(f[i], 10, 64)
		sym, secType, currency := f[i+1], f[i+2], f[i+4]
		nDeriv, _ := strconv.Atoi(f[i+5])
		i += 6 + nDeriv
		i += 2 // description + issuerId on modern server versions
		if strings.EqualFold(sym, want) && secType == "STK" && currency == "USD" && best == 0 {
			best = conID
		}
	}
	if best == 0 {
		logx.L().Debug("TWS: no exact US stock match", "sym", want)
		return
	}
	c.cmu.Lock()
	c.conIDs[want] = best
	c.cmu.Unlock()
	if c.want == want { // still the selected symbol
		c.push(best)
	}
}

// --- wire protocol ---

// handshake performs the "API" greeting and reads the server version.
// awaitReady drains the post-startApi burst until nextValidId arrives, which is
// how TWS signals the connection is ready for requests. Frames seen along the
// way (managed accounts, "farm connection is OK" notices) are handled normally
// so nothing is lost.
func (c *Client) awaitReady(conn net.Conn, r *bufio.Reader) error {
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	want := strconv.Itoa(inNextValidID)
	for {
		f, err := readMsg(r)
		if err != nil {
			return fmt.Errorf("waiting for TWS handshake to finish: %w", err)
		}
		if len(f) > 0 && f[0] == want {
			return nil
		}
		c.handle(f)
	}
}

// handshake performs the "API" greeting and reads the server version. It reads
// through the caller's reader — never its own — so no buffered bytes are lost.
func (c *Client) handshake(conn net.Conn, r *bufio.Reader) (int, error) {
	return ibwire.Handshake(conn, r, 10*time.Second)
}

// send frames one message: 4-byte big-endian length + null-terminated fields.
func (c *Client) send(fields ...any) error {
	_, err := c.conn.Write(ibwire.Encode(fields...))
	return err
}

func (c *Client) readLoop(r *bufio.Reader, out chan<- []string) error {
	for {
		fields, err := readMsg(r)
		if err != nil {
			return err
		}
		select {
		case out <- fields:
		default: // slow consumer; drop (all handled messages are idempotent)
		}
	}
}

// readMsg reads one length-prefixed message (see ibwire.ReadMsg).
func readMsg(r *bufio.Reader) ([]string, error) { return ibwire.ReadMsg(r) }
