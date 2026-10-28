package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// client is one connected UI socket with its own outbound queue and view filter.
type client struct {
	conn *websocket.Conn
	send chan []byte

	mu        sync.RWMutex
	view      map[string]bool // scanIds this client wants; empty = all
	tapeSym   string          // symbol this window is tape-watching; "" = none
	chartSyms []string        // symbols this window live-charts (charts grid); nil = none
}

func (c *client) push(b []byte) {
	select {
	case c.send <- b:
	default: // queue full; drop (client will resync on seq gap)
	}
}

func (c *client) wants(scanID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.view) == 0 {
		return true
	}
	return c.view[scanID]
}

func (c *client) setView(ids []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.view = make(map[string]bool, len(ids))
	for _, id := range ids {
		c.view[id] = true
	}
}

func (c *client) setTapeSym(sym string) { c.mu.Lock(); c.tapeSym = sym; c.mu.Unlock() }
func (c *client) getTapeSym() string    { c.mu.RLock(); defer c.mu.RUnlock(); return c.tapeSym }

func (c *client) setChartSyms(syms []string) { c.mu.Lock(); c.chartSyms = syms; c.mu.Unlock() }
func (c *client) getChartSyms() []string     { c.mu.RLock(); defer c.mu.RUnlock(); return c.chartSyms }

func (s *Server) addClient(c *client) { s.mu.Lock(); s.clients[c] = struct{}{}; s.mu.Unlock() }
func (s *Server) removeClient(c *client) {
	s.mu.Lock()
	delete(s.clients, c)
	s.mu.Unlock()
	s.signalTapeChange("") // a closed window's symbol must drop out of the watched union
}

// signalTapeChange schedules a recompute of the union of every client's
// watched tape symbol. Coalescing: if a recompute is already pending, this is
// a no-op — the pending one will see the latest state when it runs. The
// channel send carries no payload; when lastSelected is non-empty it is
// stashed in pendingSel first so a coalesced burst can't lose it (a chart
// switch sends tape.watch "" then tape.watch "NEW" back-to-back, and losing
// "NEW" to the coalescer left TWS untouched). A "" lastSelected never
// overwrites a pending non-empty selection.
func (s *Server) signalTapeChange(lastSelected string) {
	if lastSelected != "" {
		s.selMu.Lock()
		s.pendingSel = lastSelected
		s.selMu.Unlock()
	}
	select {
	case s.tapeChange <- struct{}{}:
	default:
	}
}

// drainTapeChange handles one coalesced tape-change signal: it takes and
// clears the pending selection, then recomputes the tape set with it.
// Factored out of runTapeRecompute so tests can drive it without running the
// goroutine or relying on channel timing.
func (s *Server) drainTapeChange() {
	s.selMu.Lock()
	sel := s.pendingSel
	s.pendingSel = ""
	s.selMu.Unlock()
	s.recomputeTapeSet(sel)
}

func (s *Server) runTapeRecompute(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.tapeChange:
			s.drainTapeChange()
		}
	}
}

// recomputeTapeSet produces BOTH unions from one pass over the clients:
// tapeSyms (every window's tape-watched symbol — drives the tape engine,
// per-symbol trade/quote subscriptions, and via lastSelected the single TWS
// target) and chartSyms = tapeSyms ∪ every window's chart.watch grid set
// (drives only the ingest chart tap on the A.* aggregate stream). One merged
// callback so the two sources can never clobber each other's downstream set.
func (s *Server) recomputeTapeSet(lastSelected string) {
	if s.cfg.TapeWatchSet == nil {
		return
	}
	s.mu.RLock()
	tapeSeen := make(map[string]struct{}, len(s.clients))
	tapeSyms := make([]string, 0, len(s.clients))
	chartSeen := make(map[string]struct{}, len(s.clients))
	chartSyms := make([]string, 0, len(s.clients))
	addChart := func(sym string) {
		if _, dup := chartSeen[sym]; dup {
			return
		}
		chartSeen[sym] = struct{}{}
		chartSyms = append(chartSyms, sym)
	}
	for c := range s.clients {
		if sym := c.getTapeSym(); sym != "" {
			if _, dup := tapeSeen[sym]; !dup {
				tapeSeen[sym] = struct{}{}
				tapeSyms = append(tapeSyms, sym)
			}
			addChart(sym)
		}
		for _, sym := range c.getChartSyms() {
			addChart(sym)
		}
	}
	s.mu.RUnlock()
	s.cfg.TapeWatchSet(tapeSyms, chartSyms, lastSelected)
}

// closeClients force-closes all hijacked WS connections on shutdown.
func (s *Server) closeClients() {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.clients {
		_ = c.conn.CloseNow()
	}
}

// anyWants reports whether at least one connected client views scanID.
func (s *Server) anyWants(scanID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.clients {
		if c.wants(scanID) {
			return true
		}
	}
	return false
}

func (s *Server) fanout(b []byte, scanID string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.clients {
		if c.wants(scanID) {
			c.push(b)
		}
	}
}

// fanoutTapeSym pushes b only to clients whose window is tape-watching sym —
// the tape-symbol mirror of fanout()'s per-client view filter. Raw-print
// batches are high-rate and window-specific, so unlike the ~1 Hz TapeStats
// they are never broadcast to everyone; a client watching nothing ("")
// receives none.
func (s *Server) fanoutTapeSym(b []byte, sym string) {
	if sym == "" {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.clients {
		if c.getTapeSym() == sym {
			c.push(b)
		}
	}
}

func (s *Server) fanoutAll(b []byte) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.clients {
		c.push(b)
	}
}

// fanoutExcept pushes b to every client but one — used when the sender already
// holds the state it just set (chartgrid.set), where a self-echo would only
// re-trigger its own render loop.
func (s *Server) fanoutExcept(b []byte, except *client) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.clients {
		if c != except {
			c.push(b)
		}
	}
}

// broadcast fans scanner ticks/alerts, news, and periodic status to all clients.
// A nil News/Status leaves that select case permanently blocked (never fires).
func (s *Server) broadcast(ctx context.Context) {
	ticks := s.cfg.Scanner.Ticks()
	alerts := s.cfg.Scanner.Alerts()

	var statusC <-chan time.Time
	if s.cfg.Status != nil {
		tk := time.NewTicker(time.Second)
		defer tk.Stop()
		statusC = tk.C
	}

	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticks:
			// Encoding a tick is the hub's main per-tick cost, and most scans
			// are viewed by no window at all (the main window's view is its
			// open tiles; the Signal window's is three scans): skip the JSON
			// work entirely when nobody would receive it.
			if s.anyWants(t.ID) {
				s.fanout(mustFrame(core.TScanTick, t), t.ID)
			}
		case a := <-alerts:
			if s.cfg.AlertHook != nil {
				s.cfg.AlertHook(a)
			}
			s.fanout(mustFrame(core.TAlert, a), a.ScanID)
			if s.cfg.AlertToTWS != nil {
				// Handed to runTwsAuto (in order) rather than handled here: it asks
				// the scanner for the scan's definition, which must never stall
				// the loop that carries ticks and every other alert.
				select {
				case s.twsAlerts <- a:
				default:
				}
			}
		case e := <-s.cfg.Entries:
			s.fanoutAll(mustFrame(core.TScanEnter, e))
		case m := <-s.cfg.Summaries:
			s.fanoutAll(mustFrame(core.TNewsSummary, m))
		case ts := <-s.cfg.TapeStats:
			s.fanoutAll(mustFrame(core.TTapeStat, ts))
		case pr := <-s.cfg.TapePrints:
			s.fanoutTapeSym(mustFrame(core.TTapePrints, pr), pr.Sym)
		case tr := <-s.cfg.TapeReadouts:
			s.fanoutAll(mustFrame(core.TTapeReadout, tr))
		case rg := <-s.cfg.TapeRegimes:
			s.fanoutAll(mustFrame(core.TTapeRegime, rg))
		case tc := <-s.cfg.TapeCards:
			s.fanoutAll(mustFrame(core.TTapeCard, tc))
		case sr := <-s.cfg.SetupReads:
			s.fanoutAll(mustFrame(core.TSetupRead, sr))
		case cb := <-s.cfg.ChartBars:
			s.fanoutAll(mustFrame(core.TChartBar, cb))
		case sl := <-s.cfg.ShortLive:
			s.fanoutAll(mustFrame(core.TShortLive, sl))
		case items := <-s.cfg.News:
			s.fanoutAll(mustFrame(core.TNews, core.NewsMsg{Items: items}))
		case sc := <-s.cfg.NewsScores:
			s.fanoutAll(mustFrame(core.TNewsScore, sc))
		case <-statusC:
			s.fanoutAll(mustFrame(core.TStatus, s.cfg.Status()))
		}
	}
}

// streamOriginPatterns are the page origins (host[:port]) allowed to open the
// socket besides the page the scanner itself serves (same host as the request):
// the loopback names, which covers the Vite dev server. path.Match reads the
// square brackets of an IPv6 literal as a character class, so they are escaped.
var streamOriginPatterns = []string{
	"127.0.0.1", "127.0.0.1:*",
	"localhost", "localhost:*",
	`\[::1\]`, `\[::1\]:*`,
}

// loopbackHost reports whether the host (or host:port) of a request names this
// machine: localhost (and *.localhost), or a loopback address.
func loopbackHost(hostport string) bool {
	h := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		h = hh
	}
	h = strings.ToLower(strings.Trim(h, "[]"))
	if h == "" {
		return false
	}
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// loopbackPeer reports whether the TCP peer of a request (r.RemoteAddr) is this
// machine. Unlike the Host and Origin headers, a client cannot choose it.
func loopbackPeer(remote string) bool {
	h, _, err := net.SplitHostPort(remote)
	if err != nil {
		h = remote
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	// The socket carries requests that act (the order ticket, TWS links,
	// settings), so who may open it matters even though the scanner
	// listens on loopback only: browsers treat ws://127.0.0.1 as a trustworthy
	// origin, so ANY web page the user has open could reach it. Two checks:
	//   - the Host must be a loopback name. A page whose DNS name was re-pointed at
	//     127.0.0.1 (DNS rebinding) sends an Origin equal to its own Host, which the
	//     websocket library accepts before it looks at any pattern;
	//   - the Origin, when there is one, must be a loopback page (no Origin = a
	//     native client such as the scanner window or a test);
	//   - the TCP peer must itself be this machine. Host and Origin are chosen by the
	//     client: a program on another machine sends Host 127.0.0.1 and no Origin, and
	//     with a non-loopback SCANNER_LISTEN would otherwise be let in.
	if !loopbackHost(r.Host) || !loopbackPeer(r.RemoteAddr) {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: streamOriginPatterns})
	if err != nil {
		return
	}
	conn.SetReadLimit(1 << 20) // scan defs can be a few KB; 1 MiB is plenty

	base := context.Background()
	if p := s.ctxp.Load(); p != nil {
		base = *p
	}
	ctx, cancel := context.WithCancel(base)
	defer cancel()

	c := &client{conn: conn, send: make(chan []byte, 256), view: map[string]bool{}}
	s.addClient(c)
	defer s.removeClient(c)

	go s.writeLoop(ctx, c)
	s.readLoop(ctx, c) // blocks until disconnect / shutdown
	conn.CloseNow()
}

func (s *Server) writeLoop(ctx context.Context, c *client) {
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-c.send:
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.conn.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (s *Server) readLoop(ctx context.Context, c *client) {
	for {
		_, data, err := c.conn.Read(ctx)
		if err != nil {
			return
		}
		var f core.Frame
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		s.handle(ctx, c, f)
	}
}

func (s *Server) handle(ctx context.Context, c *client, f core.Frame) {
	switch f.T {
	case core.THello:
		c.push(mustFrame(core.TScanList, core.ScanListMsg{Scans: s.cfg.Scanner.List()}))
		if s.cfg.Status != nil {
			c.push(mustFrame(core.TStatus, s.cfg.Status()))
		}
		if s.cfg.BlacklistView != nil {
			c.push(mustFrame(core.TBlacklist, core.BlacklistMsg{Syms: s.cfg.BlacklistView()}))
		}
		if s.cfg.WatchlistLoad != nil {
			// Rides the hello bundle like scan.list, but scan.list is an
			// in-memory read while this hits the DB — so it goes off the read
			// loop and lands as its own frame slightly later.
			go s.pushWatchlist(ctx, c)
		}

	case core.TScanUpsert:
		var def core.ScanDef
		if json.Unmarshal(f.D, &def) == nil {
			s.cfg.Scanner.Upsert(def)
			if s.cfg.Scans != nil {
				// WithoutCancel so a save started just before shutdown still persists.
				_ = s.cfg.Scans.Save(context.WithoutCancel(ctx), def)
			}
			s.broadcastScanList()
		}

	case core.TScanDelete:
		var m core.ScanRefMsg
		if json.Unmarshal(f.D, &m) == nil {
			s.cfg.Scanner.Delete(m.ID)
			if s.cfg.Scans != nil {
				_ = s.cfg.Scans.Delete(context.WithoutCancel(ctx), m.ID)
			}
			s.broadcastScanList()
		}

	case core.TViewSet:
		var m core.ViewSetMsg
		if json.Unmarshal(f.D, &m) == nil {
			c.setView(m.ScanIDs)
		}

	case core.TScanResync:
		var m core.ScanRefMsg
		if json.Unmarshal(f.D, &m) == nil {
			if tick, ok := s.cfg.Scanner.Resync(m.ID); ok {
				c.push(mustFrame(core.TScanTick, tick))
			}
		}

	case core.TSetKey:
		var m core.SetKeyMsg
		if json.Unmarshal(f.D, &m) == nil && s.cfg.SetKey != nil {
			if err := s.cfg.SetKey(context.WithoutCancel(ctx), m.Key); err != nil {
				c.push(mustFrame(core.TError, core.ErrorMsg{Code: "setkey", Msg: err.Error()}))
			} else if s.cfg.Status != nil {
				s.fanoutAll(mustFrame(core.TStatus, s.cfg.Status())) // reflect hasKey immediately
			}
		}

	case core.TChartGet:
		var m core.ChartGetMsg
		if json.Unmarshal(f.D, &m) == nil && s.cfg.Chart != nil && m.Sym != "" {
			// REST fetch off the read loop so a slow request can't stall inbound
			// frames; result is pushed to the requesting client only.
			go func() {
				bars, prevClose, err := s.cfg.Chart(ctx, m.Sym)
				if err != nil {
					c.push(mustFrame(core.TError, core.ErrorMsg{Code: "chart", Msg: err.Error(), Sym: m.Sym}))
					return
				}
				c.push(mustFrame(core.TChartData, core.ChartDataMsg{Sym: m.Sym, Bars: bars, PrevClose: prevClose}))
			}()
		}

	case core.TNewsFor:
		var m core.NewsForMsg
		if json.Unmarshal(f.D, &m) == nil && s.cfg.NewsFor != nil && m.Sym != "" {
			// Archive read off the read loop; history goes to the requesting
			// client only, as a normal TNews frame it merges by article id.
			go func() {
				items, err := s.cfg.NewsFor(ctx, m.Sym)
				if err != nil || len(items) == 0 {
					return // nothing archived for this symbol is a normal outcome
				}
				c.push(mustFrame(core.TNews, core.NewsMsg{Items: items}))
			}()
		}

	case core.TTrendGet:
		var m core.TrendGetMsg
		if json.Unmarshal(f.D, &m) == nil && s.cfg.TrendFor != nil && m.Sym != "" {
			// In-memory map lookup; an explicit nil-Ref reply lets the panel
			// render "no history" instead of a stale previous symbol.
			var msg core.TrendDataMsg
			msg.Sym = m.Sym
			if ref, ok := s.cfg.TrendFor(m.Sym); ok {
				msg.Ref = &ref
			}
			c.push(mustFrame(core.TTrendData, msg))
		}

	case core.TFundamentalsGet:
		var m core.FundamentalsGetMsg
		if json.Unmarshal(f.D, &m) == nil && s.cfg.Fundamentals != nil && m.Sym != "" {
			go func() {
				data, err := s.cfg.Fundamentals(ctx, m.Sym)
				if err != nil {
					code := "fundamentals"
					if errors.Is(err, core.ErrNotEntitled) {
						code = "fundamentals-not-entitled"
					}
					c.push(mustFrame(core.TError, core.ErrorMsg{Code: code, Msg: err.Error(), Sym: m.Sym}))
					return
				}
				c.push(mustFrame(core.TFundamentalsData, data))
			}()
		}

	case core.TTapeWatch:
		var m core.TapeWatchMsg
		if json.Unmarshal(f.D, &m) == nil {
			c.setTapeSym(m.Sym) // "" stops this window watching
			if m.Sym != "" {
				s.noteUserSelection(m.Sym) // the SendToTWS guard: the user just picked this themselves
			}
			s.signalTapeChange(m.Sym)
		}

	case core.TChartWatch:
		var m core.ChartWatchMsg
		if json.Unmarshal(f.D, &m) == nil {
			c.setChartSyms(normalizeChartSyms(m.Syms)) // empty/nil clears
			s.signalTapeChange("")                     // charts never move the TWS target
		}

	case core.TWatchlistGet:
		if s.cfg.WatchlistLoad != nil {
			go s.pushWatchlist(ctx, c)
		}

	case core.TWatchlistSet:
		var m core.WatchlistMsg
		if json.Unmarshal(f.D, &m) == nil && s.cfg.WatchlistSave != nil {
			syms := normalizeSyms(m.Syms, 0)
			// WithoutCancel so a save started just before a window closes still
			// persists (the scan.upsert Save pattern); the broadcast keeps every
			// open window's watchlist in sync, sender included.
			go func() {
				if err := s.cfg.WatchlistSave(context.WithoutCancel(ctx), syms); err != nil {
					c.push(mustFrame(core.TError, core.ErrorMsg{Code: "watchlist", Msg: err.Error()}))
					return
				}
				s.fanoutAll(mustFrame(core.TWatchlistData, core.WatchlistMsg{Syms: syms}))
			}()
		}

	case core.TFlagEventsGet:
		if s.cfg.FlagEvents != nil {
			var m core.FlagEventsGetMsg
			if len(f.D) > 0 {
				_ = json.Unmarshal(f.D, &m)
			}
			if m.Date != "" {
				if _, err := time.Parse("2006-01-02", m.Date); err != nil {
					c.push(mustFrame(core.TError, core.ErrorMsg{Code: "flagevents", Msg: "bad date " + strconv.Quote(m.Date) + ": want YYYY-MM-DD"}))
					break
				}
			}
			go s.pushFlagEvents(ctx, c, m.Date)
		}

	case core.TDayLogGet:
		var m core.DayLogGetMsg
		if len(f.D) > 0 {
			_ = json.Unmarshal(f.D, &m)
		}
		if m.Date != "" {
			if _, err := time.Parse("2006-01-02", m.Date); err != nil {
				c.push(mustFrame(core.TError, core.ErrorMsg{Code: "daylog", Msg: "bad date " + strconv.Quote(m.Date) + ": want YYYY-MM-DD"}))
				break
			}
		}
		if s.cfg.DayLog == nil {
			c.push(mustFrame(core.TError, core.ErrorMsg{Code: "daylog", Msg: "the Day Log is not running"}))
			break
		}
		go s.pushDayLog(ctx, c, m)

	case core.TAlertTWSGet:
		go c.push(mustFrame(core.TAlertTWSData, core.AlertTWSMsg{Enabled: s.twsFollow()}))

	case core.TAlertTWSSet:
		var m core.AlertTWSMsg
		if json.Unmarshal(f.D, &m) == nil {
			s.twsFollow() // make sure the lazy load cannot overwrite this set
			s.twsFollowOff.Store(!m.Enabled)
			go func() {
				if s.cfg.AlertTWSSave != nil {
					v := "1"
					if !m.Enabled {
						v = "0"
					}
					if err := s.cfg.AlertTWSSave(context.WithoutCancel(ctx), v); err != nil {
						c.push(mustFrame(core.TError, core.ErrorMsg{Code: "alerttws", Msg: err.Error()}))
					}
				}
				s.fanoutAll(mustFrame(core.TAlertTWSData, core.AlertTWSMsg{Enabled: !s.twsFollowOff.Load()}))
			}()
		}

	case core.TDildoParamsGet:
		if s.cfg.DildoParamsGet != nil {
			go c.push(mustFrame(core.TDildoParamsData, json.RawMessage(s.cfg.DildoParamsGet())))
		}

	case core.TDildoParamsSet:
		if s.cfg.DildoParamsSet != nil {
			raw := append([]byte(nil), f.D...)
			go func() {
				out, err := s.cfg.DildoParamsSet(context.WithoutCancel(ctx), raw)
				if err != nil {
					c.push(mustFrame(core.TError, core.ErrorMsg{Code: "dildo.params", Msg: err.Error()}))
				}
				if out != nil { // applied live even when persisting failed
					s.fanoutAll(mustFrame(core.TDildoParamsData, json.RawMessage(out)))
				}
			}()
		}

	case core.TChartGridGet:
		if s.cfg.GridLoad != nil {
			go func() {
				layout, err := s.cfg.GridLoad(ctx)
				if err != nil {
					c.push(mustFrame(core.TError, core.ErrorMsg{Code: "chartgrid", Msg: err.Error()}))
					return
				}
				if layout == "" {
					layout = "null" // valid JSON for the RawMessage field
				}
				c.push(mustFrame(core.TChartGridData, core.ChartGridMsg{Layout: json.RawMessage(layout)}))
			}()
		}

	case core.TChartGridSet:
		var m core.ChartGridMsg
		if json.Unmarshal(f.D, &m) == nil && s.cfg.GridSave != nil {
			if len(m.Layout) > maxChartGridBytes {
				c.push(mustFrame(core.TError, core.ErrorMsg{Code: "chartgrid", Msg: "layout too large"}))
				return
			}
			go func() {
				if err := s.cfg.GridSave(context.WithoutCancel(ctx), string(m.Layout)); err != nil {
					c.push(mustFrame(core.TError, core.ErrorMsg{Code: "chartgrid", Msg: err.Error()}))
					return
				}
				// To all OTHER windows: the sender already renders this layout,
				// so echoing it back would only re-trigger its own render loop.
				s.fanoutExcept(mustFrame(core.TChartGridData, core.ChartGridMsg{Layout: m.Layout}), c)
			}()
		}

	case core.TReviewGet:
		var m core.ReviewGetMsg
		if json.Unmarshal(f.D, &m) == nil && s.cfg.ReviewLoad != nil && s.cfg.ReviewDates != nil {
			// DB reads off the read loop (the chart.get precedent); the reply —
			// a few thousand raw rows at most — goes to the requester only.
			go s.pushReview(ctx, c, m)
		}

	case core.TTapeClaudeSet:
		var m core.TapeClaudeSetMsg
		if json.Unmarshal(f.D, &m) == nil && s.cfg.TapeClaudeSet != nil && m.Sym != "" {
			s.cfg.TapeClaudeSet(m.Sym, m.Enabled)
		}

	case core.TTapeCommentarySet:
		var m core.TapeCommentarySetMsg
		if json.Unmarshal(f.D, &m) == nil && s.cfg.TapeCommentarySet != nil && m.Sym != "" {
			s.cfg.TapeCommentarySet(m.Sym, m.Seconds)
		}

	case core.TRowGet:
		var m core.RowGetMsg
		if json.Unmarshal(f.D, &m) == nil && s.cfg.RowFor != nil && m.Sym != "" {
			row, ok := s.cfg.RowFor(m.Sym, m.Columns)
			if !ok {
				row = nil
			}
			c.push(mustFrame(core.TRowData, core.RowDataMsg{Sym: m.Sym, Row: row}))
		}

	case core.TTapeRead:
		var m core.TapeReadMsg
		if json.Unmarshal(f.D, &m) == nil && s.cfg.TapeRead != nil && m.Sym != "" {
			go func() {
				rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
				defer cancel()
				text, err := s.cfg.TapeRead(rctx, m.Sym)
				out := core.TapeReadoutMsg{Sym: m.Sym, Text: text, At: time.Now().UnixMilli()}
				if err != nil {
					out.Text, out.Err = "", err.Error()
				}
				c.push(mustFrame(core.TTapeReadout, out))
			}()
		}

	case core.TNewsSummarize:
		var m core.NewsSummarizeMsg
		if json.Unmarshal(f.D, &m) == nil && s.cfg.Summarize != nil && m.ID != "" {
			// Claude call runs off the read loop; WithoutCancel so a disconnect
			// mid-call still finishes and lands in the summary cache.
			go func() {
				sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Minute)
				defer cancel()
				sum, model, err := s.cfg.Summarize(sctx, m.ID)
				if err != nil {
					c.push(mustFrame(core.TNewsSummary, core.NewsSummaryMsg{ID: m.ID, Err: err.Error()}))
					return
				}
				c.push(mustFrame(core.TNewsSummary, core.NewsSummaryMsg{ID: m.ID, Summary: sum, Model: model}))
			}()
		}

	case core.TBlacklistAdd, core.TBlacklistRemove:
		s.handleBlacklist(c, f)

	case core.TNewsView, core.TSettings:
		// v1: accepted, no-op. News is broadcast to all; settings apply elsewhere.
	}
}

// ─── Today's blacklist ───────────────────────────────────────────────────

func blacklistError(c *client, ref int64, sym, msg string) {
	c.push(mustFrame(core.TError, core.ErrorMsg{Code: "blacklist", Msg: msg, Ref: ref, Sym: sym}))
}

// handleBlacklist serves blacklist.add / blacklist.remove ({sym}).
func (s *Server) handleBlacklist(c *client, f core.Frame) {
	var m core.BlacklistSymMsg
	if err := json.Unmarshal(f.D, &m); err != nil {
		blacklistError(c, f.ID, "", f.T+": unreadable request: "+err.Error())
		return
	}
	sym := strings.ToUpper(strings.TrimSpace(m.Sym))
	if sym == "" {
		blacklistError(c, f.ID, "", f.T+": a symbol is required")
		return
	}
	if s.cfg.BlacklistSet == nil {
		blacklistError(c, f.ID, sym, "the blacklist is not available in this session")
		return
	}
	if err := s.cfg.BlacklistSet(sym, f.T == core.TBlacklistAdd); err != nil {
		blacklistError(c, f.ID, sym, err.Error())
	}
}

// runBlacklist broadcasts the list to every window after each change.
func (s *Server) runBlacklist(ctx context.Context) {
	if s.cfg.BlacklistView == nil || s.cfg.BlacklistChanged == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.cfg.BlacklistChanged:
			s.fanoutAll(mustFrame(core.TBlacklist, core.BlacklistMsg{Syms: s.cfg.BlacklistView()}))
		}
	}
}

func (s *Server) broadcastScanList() {
	s.fanoutAll(mustFrame(core.TScanList, core.ScanListMsg{Scans: s.cfg.Scanner.List()}))
}

// pushWatchlist loads the persisted watchlist and pushes it to one client
// (rides the hello bundle and answers watchlist.get). Runs off the read loop —
// it hits the DB, unlike scan.list's in-memory read.
func (s *Server) pushWatchlist(ctx context.Context, c *client) {
	syms, err := s.cfg.WatchlistLoad(ctx)
	if err != nil {
		c.push(mustFrame(core.TError, core.ErrorMsg{Code: "watchlist", Msg: err.Error()}))
		return
	}
	if syms == nil {
		syms = []string{} // an empty list serializes as [], not null
	}
	c.push(mustFrame(core.TWatchlistData, core.WatchlistMsg{Syms: syms}))
}

// pushFlagEvents answers one flagevents.get off the read loop (a past day hits
// the DB): flagevents.data on success, an error frame coded "flagevents" on
// failure.
func (s *Server) pushFlagEvents(ctx context.Context, c *client, date string) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	msg, err := s.cfg.FlagEvents(ctx, date)
	if err != nil {
		c.push(mustFrame(core.TError, core.ErrorMsg{Code: "flagevents", Msg: err.Error()}))
		return
	}
	c.push(mustFrame(core.TFlagEventsData, msg))
}

// pushDayLog answers one daylog.get off the read loop: daylog.data on success,
// an error frame coded "daylog" on failure.
func (s *Server) pushDayLog(ctx context.Context, c *client, m core.DayLogGetMsg) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	msg, err := s.cfg.DayLog(ctx, m.Date, m.Snapshot)
	if err != nil {
		c.push(mustFrame(core.TError, core.ErrorMsg{Code: "daylog", Msg: err.Error()}))
		return
	}
	c.push(mustFrame(core.TDayLogData, msg))
}

// PublishFlagEvents broadcasts freshly recorded flag-scan signals to every
// connected client (flagevents.add). It never blocks: fanoutAll only does a
// non-blocking push per client (a client whose queue is full drops the frame),
// so the flag scan's evaluation loop can call it directly.
func (s *Server) PublishFlagEvents(m core.FlagEventsMsg) {
	s.fanoutAll(mustFrame(core.TFlagEventsAdd, m))
}

// pushReview answers one review.get: resolve the requested (date, session) —
// empty date means the newest date on hand, empty session defaults to "post"
// (the after-hours list the morning watchlist is built from) — and push the
// raw rows plus the full date-picker list to the requesting client. A day with
// no data answers honestly with empty rows (or, with no history at all, an
// empty date and empty picker), never fabricated values. Any failure maps to
// the standard "review" error code.
func (s *Server) pushReview(ctx context.Context, c *client, m core.ReviewGetMsg) {
	fail := func(msg string) {
		c.push(mustFrame(core.TError, core.ErrorMsg{Code: "review", Msg: msg}))
	}
	dates, err := s.cfg.ReviewDates(ctx)
	if err != nil {
		fail(err.Error())
		return
	}
	sessStr := m.Session
	if sessStr == "" {
		sessStr = core.SessionPost.String()
	}
	sess, ok := core.ParseSession(sessStr)
	if !ok {
		fail("unknown session " + m.Session)
		return
	}
	date := m.Date
	if date == "" && len(dates) > 0 {
		date = dates[0]
	}
	var rows []core.SessionSnapshotRow
	if date != "" {
		if rows, err = s.cfg.ReviewLoad(ctx, date, sess); err != nil {
			fail(err.Error())
			return
		}
	}
	if dates == nil {
		dates = []string{} // serialize as [], not null
	}
	if rows == nil {
		rows = []core.SessionSnapshotRow{}
	}
	c.push(mustFrame(core.TReviewData, core.ReviewDataMsg{
		Date: date, Session: sess.String(), Dates: dates, Rows: rows,
	}))
}

// Caps on client-supplied payloads: a malformed frame must not grow the ingest
// chart union or the persisted layout blob unboundedly.
const (
	maxChartWatchSyms = 16       // symbols one window's chart.watch set may hold; extras are ignored
	maxChartGridBytes = 64 << 10 // chartgrid.set layout blob cap; oversize frames are rejected
)

// normalizeSyms trims, uppercases, drops empties, and dedupes (keeping first
// occurrence, order preserved); max > 0 caps the accepted set, ignoring extras.
func normalizeSyms(syms []string, max int) []string {
	out := make([]string, 0, len(syms))
	seen := make(map[string]struct{}, len(syms))
	for _, s := range syms {
		sym := strings.ToUpper(strings.TrimSpace(s))
		if sym == "" {
			continue
		}
		if _, dup := seen[sym]; dup {
			continue
		}
		seen[sym] = struct{}{}
		out = append(out, sym)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}

// normalizeChartSyms is chart.watch's sym handling: dedupe + uppercase like
// the rest of the wire, capped so one window can't blow up the union.
func normalizeChartSyms(syms []string) []string { return normalizeSyms(syms, maxChartWatchSyms) }

// mustFrame marshals a payload into a Frame envelope of the given type.
func mustFrame(typ string, payload any) []byte {
	raw, _ := json.Marshal(payload)
	// The core.Frame envelope written directly around raw: marshalling the
	// Frame would copy and re-scan the whole payload only to compact what
	// json.Marshal already left compact (and HTML-escaped). Same bytes:
	// {"t":typ} plus "d" unless raw is empty (omitempty); ID is zero here.
	t, _ := json.Marshal(typ)
	f := make([]byte, 0, len(raw)+len(t)+12)
	f = append(f, `{"t":`...)
	f = append(f, t...)
	if len(raw) > 0 {
		f = append(f, `,"d":`...)
		f = append(f, raw...)
	}
	return append(f, '}')
}
