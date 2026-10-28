// Package server serves the embedded UI and the /stream WebSocket that carries
// the scan/alert/news/status contract. It depends on core (interfaces) and the
// web package (embedded assets); it never imports engine/feed/persist directly.
package server

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/web"
)

// Config wires the server's collaborators.
type Config struct {
	Addr         string                                                               // listen address (default 127.0.0.1:7878)
	Scanner      core.Scanner                                                         // required: tick/alert source + scan CRUD
	Scans        core.ScanRepo                                                        // optional: persist scan upserts/deletes
	News         <-chan []core.NewsItem                                               // optional: news to broadcast
	Status       func() core.StatusMsg                                                // optional: status snapshot, broadcast ~1 Hz
	SetKey       func(ctx context.Context, key string) error                          // optional: set/persist API key
	Chart        func(ctx context.Context, sym string) ([]core.Bar, float64, error)   // optional: intraday bars + prior close for click-to-expand chart
	Fundamentals func(ctx context.Context, sym string) (core.Fundamentals, error)     // optional: SEC-filed revenue/cash/burn for a symbol
	Summarize    func(ctx context.Context, id string) (text, model string, err error) // optional: news summary by ID (local or Claude — model says which)
	NewsFor      func(ctx context.Context, sym string) ([]core.NewsItem, error)       // optional: a symbol's archived news history (click-to-load)
	TrendFor     func(sym string) (core.TrendRef, bool)                               // optional: daily-trend context for the symbol panel
	NewsScores   <-chan core.NewsScoreMsg                                             // optional: RTPR Impact Scores to attach to articles
	Entries      <-chan core.ScanEntry                                                // optional: scan entries for the activity log
	Summaries    <-chan core.NewsSummaryMsg                                           // optional: finished auto-summaries to broadcast
	TapeWatchSet func(tapeSyms, chartSyms []string, lastSelected string)              // optional: reconcile the watched symbol sets (unions across clients). tapeSyms drives the tape engine + per-symbol trade/quote subscriptions; chartSyms (a superset: tapeSyms ∪ every window's chart.watch set) drives only the ingest chart tap. lastSelected drives the single shared TWS/borrow target
	TapeRead     func(ctx context.Context, sym string) (string, error)                // optional: Claude interpretation of current order flow
	TapePrints   <-chan core.TapePrintsMsg                                            // optional: ~4 Hz raw-print batches, routed only to clients tape-watching that symbol
	TapeStats    <-chan core.TapeStat                                                 // optional: ~1 Hz order-flow digests to broadcast (one per watched symbol)
	TapeReadouts <-chan core.TapeReadoutMsg                                           // optional: finished auto tape reads to broadcast
	ChartBars    <-chan core.ChartBarMsg                                              // optional: live per-second bars for watched symbols
	ShortLive    <-chan core.ShortLive                                                // optional: live IBKR borrow availability for the selected symbol
	RowFor       func(sym string, cols []string) (core.RowJSON, bool)                 // optional: on-demand row snapshot for a symbol (pop-out tape windows)

	// Event-driven regime detection (Claude toggle + live commentary): the
	// successor to TapeRead's request/response button for automatic alerts.
	TapeRegimes       <-chan core.TapeRegimeMsg      // optional: instant, free local-verdict alert on a regime shift, to broadcast
	TapeCards         <-chan core.TapeCardMsg        // optional: completed tape-reading cards (local or Claude), to broadcast
	TapeClaudeSet     func(sym string, on bool)      // optional: toggle Claude regime reads for a symbol
	TapeCommentarySet func(sym string, secs float64) // optional: set the periodic live-commentary interval for a symbol (0 = off)

	// Claude setup reads (internal/flagread): a symbol entering a scan that
	// FILTERS ON the nightly bull-flag fields earns one scored, explained read
	// of that setup. Broadcast to every client like the tape cards above — the
	// read is about the symbol, not about who happens to be watching it.
	SetupReads <-chan core.SetupReadMsg // optional: finished bull-flag setup reads, to broadcast

	// Charts-grid window: persisted watchlist (ordered symbol pool) and the
	// opaque grid-layout blob. The server treats the layout as a string it
	// stores and echoes — the client owns its shape.
	WatchlistLoad func(ctx context.Context) ([]string, error)    // optional: load the persisted watchlist (ordered)
	WatchlistSave func(ctx context.Context, syms []string) error // optional: whole-list replace of the watchlist
	GridLoad      func(ctx context.Context) (string, error)      // optional: load the persisted grid-layout blob ("" = none)
	GridSave      func(ctx context.Context, layout string) error // optional: store the grid-layout blob

	// 9EMA flag scan activity (internal/flaglog): one ET day of the signals the
	// market-wide scan produced, for the Signal window's activity list. date is ""
	// (today) or YYYY-MM-DD, already validated. nil = feature off (the scan is
	// disabled), flagevents.get is then ignored.
	FlagEvents func(ctx context.Context, date string) (core.FlagEventsMsg, error)

	// Day Log (internal/daylog): AlertHook sees EVERY scan alert the scanner fires
	// (before any client-side muting), called from the broadcast loop, so it must
	// not block. DayLog answers daylog.get: one ET day ("" = today) of recorded
	// alerts and gainers / losers; snapshot asks for the movers snapshot first.
	// nil DayLog = the window gets an error frame.
	AlertHook func(core.AlertMsg)
	DayLog    func(ctx context.Context, date string, snapshot bool) (core.DayLogMsg, error)

	// Past-session review (the morning "what moved after hours yesterday"
	// window): persisted engine boundary snapshots, served raw — the client
	// derives every percentage. Both must be set for review.get to answer.
	ReviewLoad  func(ctx context.Context, date string, session core.Session) ([]core.SessionSnapshotRow, error) // optional: one (date, session)'s raw rows
	ReviewDates func(ctx context.Context) ([]string, error)                                                     // optional: distinct dates on hand, newest first

	// Today's symbol BLACKLIST (internal/blacklist): alerts of a listed symbol
	// are silenced in the Signal window and flagged in the Day Log. The hub sends
	// the list on hello and after every change, and routes blacklist.add /
	// blacklist.remove ({sym}) to BlacklistSet; a failure comes back to the
	// sender as an 'error' frame with code "blacklist".
	BlacklistView    func() []string                  // optional: today's list, sorted
	BlacklistChanged <-chan struct{}                  // optional: the list may have changed
	BlacklistSet     func(sym string, add bool) error // optional: add / remove one symbol

	// AlertToTWS moves the linked TWS window to sym when a scan with
	// AlertConfig.SendToTWS alerts (twslink's display-group Show: it never
	// places an order). The hub applies the guards (twsauto.go) and broadcasts a
	// tws.auto frame either way. MUST NOT block (Show only queues). nil = the
	// feature is off: flagged scans alert as usual and nothing is sent or reported.
	AlertToTWS func(sym string)

	// OrderTicket serves the Order Ticket pane's API under /api/order/
	// (internal/orderticket; it guards its own requests). nil = not mounted.
	OrderTicket http.Handler
	// AlertTWSLoad / AlertTWSSave persist the global "TWS follow" switch as a
	// string ("0" = off; anything else, including "", = on). Both optional: without
	// them the switch still works, in memory only.
	AlertTWSLoad func(ctx context.Context) (string, error)
	AlertTWSSave func(ctx context.Context, v string) error
	// DildoParamsGet returns the market-wide GREEN DILDO tuning as the panel's JSON
	// (the live, validated set); DildoParamsSet validates a client's JSON, persists
	// it, applies it to the engine judge and flagscan live, and returns the set that
	// took effect. The hub echoes that to every window as dildo.params.data.
	DildoParamsGet func() []byte
	DildoParamsSet func(ctx context.Context, raw []byte) ([]byte, error)
}

// Server implements core.Server.
type Server struct {
	cfg     Config
	handler http.Handler
	http    *http.Server
	ctxp    atomic.Pointer[context.Context] // base ctx for accepted connections

	mu      sync.RWMutex
	clients map[*client]struct{}

	// tapeChange coalesces "some client's watched tape symbol changed" signals
	// into a single recompute goroutine, so concurrent tape.watch messages from
	// different windows can't race each other's read-diff-write of the union.
	// Buffered 1: a burst of rapid changes collapses to a single recompute.
	// The channel itself only NUDGES the recompute goroutine — it carries no
	// payload — because a burst of signals must not be able to lose the
	// symbol a click selected. selMu/pendingSel carry that payload instead:
	// the latest NON-EMPTY selection survives any coalescing, and a ""
	// signal (tape.watch cleanup, chart.watch, a closed window) never
	// overwrites a pending selection — it only means "recompute the union,
	// don't move TWS".
	tapeChange chan struct{}
	selMu      sync.Mutex
	pendingSel string

	// twsMu guards tws, the memory of the SendToTWS guards (twsauto.go); now is
	// their clock (time.Now; tests replace it before Start).
	twsMu sync.Mutex
	tws   twsAutoState
	now   func() time.Time
	// twsAlerts carries the broadcast loop's alerts to runTwsAuto, in order.
	twsAlerts chan core.AlertMsg
	// twsFollowOff is the global "TWS follow" switch turned off (zero value = on).
	// twsFollowLoaded: the persisted value has been read (once, lazily).
	twsFollowOff  atomic.Bool
	twsFollowOnce sync.Once
}

// twsFollow reports whether alert-driven TWS Shows are enabled, loading the
// persisted value on first use.
func (s *Server) twsFollow() bool {
	s.twsFollowOnce.Do(func() {
		if s.cfg.AlertTWSLoad == nil {
			return
		}
		ctx := context.Background()
		if p := s.ctxp.Load(); p != nil {
			ctx = *p
		}
		if v, err := s.cfg.AlertTWSLoad(ctx); err == nil && strings.TrimSpace(v) == "0" {
			s.twsFollowOff.Store(true)
		}
	})
	return !s.twsFollowOff.Load()
}

var _ core.Server = (*Server)(nil)

func New(cfg Config) *Server {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:7878"
	}
	s := &Server{cfg: cfg, clients: map[*client]struct{}{}, tapeChange: make(chan struct{}, 1), now: time.Now, twsAlerts: make(chan core.AlertMsg, 64)}
	s.handler = s.buildHandler()
	return s
}

func (s *Server) Addr() string          { return s.cfg.Addr }
func (s *Server) Handler() http.Handler { return s.handler }

// Start begins the broadcast loop without binding a listener (used by tests that
// drive the handler via httptest).
func (s *Server) Start(ctx context.Context) {
	s.ctxp.Store(&ctx)
	go s.broadcast(ctx)
	go s.runTapeRecompute(ctx)
	go s.runBlacklist(ctx)
	go s.runTwsAuto(ctx)
}

// Run starts the broadcaster and serves HTTP until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	s.ctxp.Store(&ctx)
	go s.broadcast(ctx)
	go s.runTapeRecompute(ctx)
	go s.runBlacklist(ctx)
	go s.runTwsAuto(ctx)
	s.http = &http.Server{Addr: s.cfg.Addr, Handler: s.handler}
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.http.Shutdown(sh)
		s.closeClients() // force-close hijacked WS conns; Shutdown doesn't
	}()
	if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) buildHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/stream", s.serveWS)
	mux.HandleFunc("/api/daylog", s.serveDayLog)
	mux.HandleFunc("/api/daylog/dates", s.serveDayLogDates)
	if s.cfg.OrderTicket != nil {
		mux.Handle("/api/order/", s.cfg.OrderTicket)
	}

	// Go's profiler, so CPU/heap/goroutine profiles of the LIVE scanner can be
	// taken without a rebuild (e.g. `go tool pprof http://127.0.0.1:7878/debug/
	// pprof/profile?seconds=30`). Loopback callers only: the listen address is
	// configurable, and a profile is not something to hand to the LAN.
	pprofOnly := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !loopbackHost(r.RemoteAddr) {
				http.NotFound(w, r)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("/debug/pprof/", pprofOnly(pprof.Index))
	mux.HandleFunc("/debug/pprof/cmdline", pprofOnly(pprof.Cmdline))
	mux.HandleFunc("/debug/pprof/profile", pprofOnly(pprof.Profile))
	mux.HandleFunc("/debug/pprof/symbol", pprofOnly(pprof.Symbol))
	mux.HandleFunc("/debug/pprof/trace", pprofOnly(pprof.Trace))

	dist := web.FS()
	fileServer := http.FileServer(http.FS(dist))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		// Serve a real asset if it exists; otherwise fall back to index.html so
		// client-side routes resolve (SPA).
		if _, err := fs.Stat(dist, p); err != nil {
			b, e := fs.ReadFile(dist, "index.html")
			if e != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(b)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
	return mux
}
