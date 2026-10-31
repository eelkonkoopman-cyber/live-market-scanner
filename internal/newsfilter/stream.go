// Package newsfilter is a SHADOW recorder for Newsfilter (newsfilter.io, $65 a
// month), the cheaper candidate to replace RTPR ($139). It connects to their
// websocket, notes the moment each article arrives, and appends one line per
// article to a day file under ~/.marketscanner/newsrace, so `go run
// ./cmd/newsrace` can later line it up against when RTPR delivered the same
// press release and answer "is it as fast, and does it miss anything".
//
// It is record-only by construction: there is no Out channel, nothing reaches
// news.Poller.AddExternal, the news panel, alerts, the autosummarizer, health,
// the WS hub or the database, and RTPR is untouched. Without a token the
// package is never started.
//
// The protocol is Socket.io v2 over Engine.IO v3 and was built from their docs
// alone (no key existed when this was written). Every parser is therefore
// forgiving and counts what it cannot use, and the first real run keeps its
// first frames verbatim in raw-frames.jsonl so the docs can be checked against
// reality on day one.
//
// Two rules the code leans on throughout:
//   - Run never returns an error. In cmd/scanner a component that returns one
//     cancels the whole scanner, so every failure here (network, disk, a bad
//     frame, a panic) is logged and retried instead.
//   - The API key travels in the URL, and websocket.Dial errors quote the URL.
//     Everything that could reach a log line, a stored error or a file goes
//     through scrub first.
package newsfilter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"runtime/debug"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

const (
	// Their docs only list the plaintext ws:// endpoint, which sends the key
	// unencrypted, so wss is tried first.
	wssURL = "wss://stream.newsfilter.io/socket.io/?EIO=3&transport=websocket"
	wsURL  = "ws://stream.newsfilter.io/socket.io/?EIO=3&transport=websocket"

	backoffStart = time.Second
	backoffMax   = 5 * time.Minute // also the wait after a refused key or plan

	dialTimeout  = 15 * time.Second
	writeTimeout = 10 * time.Second

	// pingMax caps the Engine.IO ping ("2") cadence. Their sample re-sends the
	// socket.io connect packet ("40") every 20 s as a keepalive; on a v2 server
	// that registers another socket per send and multiplies deliveries, so the
	// engine ping is sent instead.
	pingMax = 20 * time.Second
	pingMin = time.Second // floor, so a hostile pingInterval cannot spin a ticker

	// silenceMax: no frame at all for this long (or pingInterval+pingTimeout
	// when the server announced smaller values) means a dead connection.
	silenceMax = 60 * time.Second

	// healthyAfter: a session whose last frame came this long after the open
	// resets the backoff. It is judged by the last frame, not by when the session
	// ended: silenceMax is the same 60 s, so a socket that opened and then went
	// quiet would otherwise end exactly at the threshold and look healthy.
	healthyAfter = 60 * time.Second

	readLimit  = 1 << 22 // the library default (32 KiB) would cut batches of articles
	seenCap    = 50_000
	statsEvery = 10 * time.Minute
)

// Config configures a Stream.
type Config struct {
	Token string // Newsfilter API key
	Dir   string // day files and raw-frames.jsonl go here (created 0700)

	// URLs are the endpoints tried in order, without the key (it is appended as
	// apiKey=). The one that last worked is tried first on a reconnect. Default:
	// the wss:// endpoint, then the documented ws:// one.
	URLs []string

	// Now is the clock for arrival stamps, the UTC-midnight file rollover and
	// the health bookkeeping. Default time.Now.
	Now func() time.Time
}

// Stats is a point-in-time copy of the stream's counters.
type Stats struct {
	Frames      int64 // every websocket message received
	Articles    int64 // article objects with an id, duplicates included
	Dupes       int64 // of those, ids already seen
	Recorded    int64 // first sightings written
	NoID        int64 // article objects without an id (skipped)
	ParseFails  int64 // frames or article elements that could not be used
	BadFields   int64 // article fields present with an unusable type
	Binary      int64 // binary frames (ignored)
	OtherEvents int64 // well-formed events that are not "articles"
	Reconnects  int64 // sessions that reached the server and were later lost
	Panics      int64 // sessions dropped by a recovered panic
	DiskErrors  int64 // failed writes to the day file
}

type counters struct {
	frames, articles, dupes, recorded, noID, parseFails, badFields atomic.Int64
	binary, otherEvents, reconnects, panics, diskErrors            atomic.Int64
}

// Stream is the Newsfilter recorder. Create it with New and run it with Run.
type Stream struct {
	cfg     Config
	urls    []string
	now     func() time.Time
	log     *slog.Logger // nil = logx.L(), resolved per call
	secrets []string     // every spelling of the key, longest first

	rec  *recorder
	raw  *rawCapture
	seen *idSet
	n    counters

	// Timing and hooks, unexported so tests can shorten them.
	backoffStart, backoffMax time.Duration
	dialTimeout              time.Duration
	pingMax, pingMin         time.Duration
	silenceMax               time.Duration
	healthyAfter             time.Duration
	statsEvery               time.Duration
	sleep                    func(ctx context.Context, d time.Duration) bool
	onFrame                  func(payload []byte) // test hook, called on every text frame before handling

	// Touched only by the goroutine running Run.
	cur         int // index into urls of the endpoint that last worked
	warnedPlain bool
	lastStats   time.Time
	lastDiskLog time.Time
}

// New builds a Stream. It does nothing until Run.
func New(cfg Config) *Stream {
	s := &Stream{
		cfg:          cfg,
		urls:         cfg.URLs,
		now:          cfg.Now,
		secrets:      secretsFor(cfg.Token),
		seen:         newIDSet(seenCap),
		backoffStart: backoffStart,
		backoffMax:   backoffMax,
		dialTimeout:  dialTimeout,
		pingMax:      pingMax,
		pingMin:      pingMin,
		silenceMax:   silenceMax,
		healthyAfter: healthyAfter,
		statsEvery:   statsEvery,
		sleep:        sleepCtx,
	}
	if len(s.urls) == 0 {
		s.urls = []string{wssURL, wsURL}
	}
	if s.now == nil {
		s.now = time.Now
	}
	s.rec = &recorder{dir: cfg.Dir}
	s.raw = &rawCapture{scrub: s.scrub}
	return s
}

// Stats returns a copy of the counters; safe to call while Run is running.
func (s *Stream) Stats() Stats {
	return Stats{
		Frames: s.n.frames.Load(), Articles: s.n.articles.Load(), Dupes: s.n.dupes.Load(),
		Recorded: s.n.recorded.Load(), NoID: s.n.noID.Load(), ParseFails: s.n.parseFails.Load(),
		BadFields: s.n.badFields.Load(), Binary: s.n.binary.Load(), OtherEvents: s.n.otherEvents.Load(),
		Reconnects: s.n.reconnects.Load(), Panics: s.n.panics.Load(), DiskErrors: s.n.diskErrors.Load(),
	}
}

func (s *Stream) l() *slog.Logger {
	if s.log != nil {
		return s.log
	}
	return logx.L()
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// --- the key never leaves ---

// secretsFor lists every spelling of the key that could show up in text: raw,
// query-escaped (how it sits in the dial URL), path-escaped and JSON-escaped.
func secretsFor(token string) []string {
	if token == "" {
		return nil
	}
	set := map[string]bool{token: true, url.QueryEscape(token): true, url.PathEscape(token): true}
	// JSON spellings: encoding/json's default (escapes & < >) and the recorder's
	// own (jsonLine switches HTML escaping off).
	if b, err := json.Marshal(token); err == nil && len(b) >= 2 {
		set[string(b[1:len(b)-1])] = true
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(token) == nil {
		if b := bytes.TrimSpace(buf.Bytes()); len(b) >= 2 {
			set[string(b[1:len(b)-1])] = true
		}
	}
	if strings.Contains(token, "/") { // some encoders (PHP's) also escape the slash in JSON
		for v := range maps.Clone(set) {
			set[strings.ReplaceAll(v, "/", `\/`)] = true
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) || len(out[i]) == len(out[j]) && out[i] < out[j] })
	return out
}

// scrub removes the API key, in every spelling, from text bound for a log, a
// stored error or a file.
func (s *Stream) scrub(in string) string {
	for _, v := range s.secrets {
		in = strings.ReplaceAll(in, v, "***")
	}
	return in
}

// cleanErr is err with the key scrubbed out. The chain is deliberately lost:
// a wrapped *url.Error would still carry the keyed URL.
func (s *Stream) cleanErr(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(s.scrub(err.Error()))
}

// excerpt is a short, scrubbed prefix of a payload for log lines. It scrubs a
// generous window first and cuts afterwards, so a cut can never leave half a key.
func (s *Stream) excerpt(p []byte, n int) string {
	if len(p) > 4*n {
		p = p[:4*n]
	}
	return clip(s.scrub(string(p)), n)
}

// capped scrubs v and only then cuts it to n bytes. The other order would let
// the cut fall inside an echoed key and leave a prefix no scrub spelling matches.
func (s *Stream) capped(v string, n int) string { return clip(s.scrub(v), n) }

// keyed appends the API key to an endpoint.
func keyed(base, token string) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + "apiKey=" + url.QueryEscape(token)
}

// kindOf labels an endpoint "wss" or "ws" for the connected event.
func kindOf(base string) string {
	b := strings.ToLower(base)
	if strings.HasPrefix(b, "wss://") || strings.HasPrefix(b, "https://") {
		return "wss"
	}
	return "ws"
}

// --- run loop ---

// sessionEnd is how one connection attempt ended.
type sessionEnd struct {
	err     error // scrubbed, never nil for an ended session
	opened  bool  // the server answered with a socket.io/engine.io frame
	healthy bool  // its last frame came at least healthyAfter after the open
	refused bool  // key or plan refused (HTTP 401/403 or a socket.io error frame)
}

// Run connects and reconnects until ctx is done and ALWAYS returns nil: errors
// are logged and retried, disk trouble never stops it, and a panic inside a
// session is recovered and counted as a dropped session.
func (s *Stream) Run(ctx context.Context) error {
	defer func() {
		if r := recover(); r != nil {
			s.l().Error("newsfilter: panic in run loop; recorder stopped", "panic", s.scrub(fmt.Sprint(r)),
				"stack", s.scrub(string(debug.Stack())))
		}
	}()
	if err := s.raw.open(s.cfg.Dir); err != nil {
		s.l().Warn("newsfilter: raw-frame capture unavailable", "err", s.scrub(err.Error()))
	}
	defer s.raw.close()
	defer s.rec.close()
	// Every exit leaves a stopped event, also one during a backoff wait or a
	// failing dial: without it the report would count the time the recorder was
	// off as Newsfilter being disconnected.
	defer s.event(Event{Ev: EvStopped})
	s.lastStats = s.now()

	backoff := s.backoffStart
	for ctx.Err() == nil {
		end := s.step(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if end.opened {
			s.n.reconnects.Add(1)
		}
		wait := backoff
		switch {
		case end.refused:
			wait = s.backoffMax
			backoff = s.backoffMax
			s.l().Warn("newsfilter REFUSED the API key or plan; check the key and that the plan includes the websocket stream",
				"err", end.err, "retry_in", wait)
		case end.healthy:
			wait = s.backoffStart
			backoff = min(s.backoffStart*2, s.backoffMax)
			s.l().Warn("newsfilter dropped; reconnecting", "err", end.err, "in", wait)
		default:
			backoff = min(backoff*2, s.backoffMax)
			if end.opened {
				s.l().Warn("newsfilter dropped; reconnecting", "err", end.err, "in", wait)
			} else {
				s.l().Warn("newsfilter could not connect; retrying", "err", end.err, "in", wait)
			}
		}
		if !s.sleep(ctx, wait) {
			return nil
		}
	}
	return nil
}

// step runs one attempt with a last-resort recover, so nothing below Run can
// end the loop.
func (s *Stream) step(ctx context.Context) (end sessionEnd) {
	defer func() {
		if r := recover(); r != nil {
			s.n.panics.Add(1)
			msg := s.scrub(fmt.Sprint(r))
			s.l().Error("newsfilter: panic outside a session; retrying", "panic", msg, "stack", s.scrub(string(debug.Stack())))
			end = sessionEnd{err: errors.New("panic: " + msg)}
		}
	}()
	return s.attempt(ctx)
}

// attempt tries the endpoints in order, remembered one first, until one of them
// gets a session going. A refused key stops the tour: the other endpoint
// would only repeat the answer.
func (s *Stream) attempt(ctx context.Context) sessionEnd {
	var end sessionEnd
	for i := range s.urls {
		idx := (s.cur + i) % len(s.urls)
		end = s.session(ctx, idx)
		if end.opened || end.refused || ctx.Err() != nil {
			return end
		}
		if i < len(s.urls)-1 {
			s.l().Info("newsfilter endpoint failed; trying the next", "transport", kindOf(s.urls[idx]), "err", end.err)
		}
	}
	return end
}

// link is one live connection.
type link struct {
	s    *Stream
	ctx  context.Context
	conn *websocket.Conn
	idx  int
	kind string

	silence  time.Duration
	opened   bool
	openedAt time.Time
	lastRecv int64
	sawConn  atomic.Bool // a socket.io "40" connect packet arrived (read by the ping goroutine)

	pcancel   context.CancelFunc
	pdone     chan struct{}
	pingPanic atomic.Pointer[string] // set by the keepalive goroutine when it panicked (scrubbed)
	refused   bool
}

func (s *Stream) session(ctx context.Context, idx int) (end sessionEnd) {
	l := &link{s: s, ctx: ctx, idx: idx, kind: kindOf(s.urls[idx]), silence: s.silenceMax}
	defer func() {
		if r := recover(); r != nil {
			s.n.panics.Add(1)
			msg := s.scrub(fmt.Sprint(r))
			s.l().Error("newsfilter: panic in session; dropping it", "panic", msg, "stack", s.scrub(string(debug.Stack())))
			end.err = errors.New("panic: " + msg)
		}
		end.opened, end.refused = l.opened, l.refused
		end.healthy = l.opened && time.UnixMilli(l.lastRecv).Sub(l.openedAt) >= s.healthyAfter
		l.finish(end.err)
	}()

	// Warn before dialing, not on the first frame: the key is already on the wire
	// when the dial starts, and an attempt that is refused or closed before any
	// frame would otherwise send it in clear without a word in the log.
	if l.kind == "ws" && !s.warnedPlain {
		s.warnedPlain = true
		s.l().Warn("newsfilter is dialing the plaintext ws:// endpoint (wss was not usable): the API key travels unencrypted")
	}
	dctx, cancel := context.WithTimeout(ctx, s.dialTimeout)
	conn, resp, err := websocket.Dial(dctx, keyed(s.urls[idx], s.cfg.Token), nil)
	cancel()
	if err != nil {
		if resp != nil && (resp.StatusCode == 401 || resp.StatusCode == 403) {
			l.refused = true
			return sessionEnd{err: fmt.Errorf("newsfilter refused the key or plan (HTTP %d on dial)", resp.StatusCode)}
		}
		return sessionEnd{err: s.cleanErr(err)}
	}
	l.conn = conn
	conn.SetReadLimit(readLimit)
	defer conn.CloseNow()
	defer l.stopPing()

	for {
		rctx, rcancel := context.WithTimeout(ctx, l.silence)
		typ, payload, rerr := conn.Read(rctx)
		recv := s.now().UnixMilli() // stamped before ANY parsing: parse cost must never count against Newsfilter
		timedOut := errors.Is(rctx.Err(), context.DeadlineExceeded)
		rcancel()
		if rerr != nil {
			switch {
			case ctx.Err() != nil:
				return sessionEnd{err: ctx.Err()}
			case l.pingPanic.Load() != nil:
				return sessionEnd{err: errors.New("panic: " + *l.pingPanic.Load())}
			case timedOut:
				return sessionEnd{err: fmt.Errorf("no frame for %s: connection presumed dead", l.silence)}
			}
			return sessionEnd{err: s.cleanErr(rerr)}
		}
		l.lastRecv = recv
		s.n.frames.Add(1)
		s.tick(l)
		if typ != websocket.MessageText {
			s.n.binary.Add(1)
			s.raw.add(recv, nil, len(payload), true)
			continue
		}
		if !plainPingPong(payload) {
			s.raw.add(recv, payload, len(payload), false)
		}
		if s.onFrame != nil {
			s.onFrame(payload)
		}
		if err := l.handle(recv, payload); err != nil {
			return sessionEnd{err: err}
		}
	}
}

// finish records how a session that reached the server ended.
func (l *link) finish(err error) {
	if !l.opened {
		return
	}
	s := l.s
	if l.ctx.Err() != nil {
		return // shutting down: Run writes the stopped event
	}
	msg := ""
	if err != nil {
		msg = s.scrub(err.Error())
	}
	s.event(Event{Ev: EvDropped, Last: l.lastRecv, Err: msg})
}

// open marks the connection as up, once: starts the keepalive and records the
// connected event. The first Engine.IO packet carries the server's ping
// timings; pi/pt are zero when it is not that packet.
func (l *link) open(pi, pt time.Duration) {
	if l.opened {
		return
	}
	s := l.s
	l.opened = true
	l.openedAt = s.now()
	s.cur = l.idx // remember the endpoint that worked

	every := pingEvery(pi, s.pingMax, s.pingMin)
	l.silence = silenceFor(pi, pt, s.silenceMax)
	var pctx context.Context
	pctx, l.pcancel = context.WithCancel(l.ctx)
	l.pdone = make(chan struct{})
	go l.pingLoop(pctx, every)

	s.event(Event{Ev: EvConnected, URL: l.kind})
	s.l().Info("newsfilter connected (recording only)", "transport", l.kind, "ping", every, "silence", l.silence)
}

// pingEvery is the Engine.IO ping cadence: the server's pingInterval, capped at
// pingMax (20 s) and floored at pingMin; pingMax when the server announced none.
func pingEvery(pi, pingMax, pingMin time.Duration) time.Duration {
	every := pingMax
	if pi > 0 && pi < every {
		every = pi
	}
	return max(every, pingMin)
}

// silenceFor is how long the connection may stay silent before it is presumed
// dead: silenceMax (60 s), or pingInterval+pingTimeout when both are known and
// smaller.
func silenceFor(pi, pt, silenceMax time.Duration) time.Duration {
	if pi > 0 && pt > 0 && pi+pt < silenceMax {
		return pi + pt
	}
	return silenceMax
}

// stopPing ends the keepalive goroutine and waits for it, so nothing writes to
// the connection after the session is over.
func (l *link) stopPing() {
	if l.pcancel != nil {
		l.pcancel()
		<-l.pdone
	}
}

// pingLoop sends the Engine.IO ping ("2"); the server answers "3", and any
// frame at all counts as liveness.
func (l *link) pingLoop(ctx context.Context, every time.Duration) {
	defer close(l.pdone)
	defer func() { // nothing in here may take the scanner down: count it, log it, drop the session
		if r := recover(); r != nil {
			s := l.s
			s.n.panics.Add(1)
			msg := s.scrub(fmt.Sprint(r))
			l.pingPanic.Store(&msg)
			s.l().Error("newsfilter: panic in keepalive; dropping the session", "panic", msg, "stack", s.scrub(string(debug.Stack())))
			_ = l.conn.CloseNow() // the reader fails at once and ends the session
		}
	}()
	tk := time.NewTicker(every)
	defer tk.Stop()
	warned := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			if err := l.write(ctx, "2"); err != nil {
				return
			}
			if !warned && !l.sawConn.Load() {
				warned = true
				l.s.l().Warn("newsfilter: no socket.io connect packet (40) yet; articles may not flow until the server sends it")
			}
		}
	}
}

func (l *link) write(ctx context.Context, frame string) error {
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return l.conn.Write(wctx, websocket.MessageText, []byte(frame))
}

// plainPingPong reports whether p is a bare Engine.IO ping or pong; those are
// not worth a raw-capture slot.
func plainPingPong(p []byte) bool {
	return len(p) > 0 && (p[0] == '2' || p[0] == '3') && (len(p) == 1 || string(p[1:]) == "probe")
}

// handle dispatches one text frame. A non-nil error ends the session.
func (l *link) handle(recv int64, p []byte) error {
	s := l.s
	if len(p) == 0 {
		s.parseFails(1, p, errors.New("empty frame"))
		return nil
	}
	switch p[0] {
	case '0': // engine open: {"sid":..,"pingInterval":..,"pingTimeout":..}
		pi, pt := parseOpen(p[1:])
		l.open(pi, pt)
	case '1':
		return errors.New("server closed the engine.io session (frame 1)")
	case '2': // server ping
		l.open(0, 0)
		return l.write(l.ctx, "3"+string(p[1:]))
	case '3': // pong
		l.open(0, 0)
	case '4':
		l.open(0, 0)
		return l.message(recv, p)
	default:
		s.parseFails(1, p, fmt.Errorf("unknown engine.io packet type %q", p[0]))
	}
	return nil
}

// message handles a socket.io packet ("4" + type + ...).
func (l *link) message(recv int64, p []byte) error {
	s := l.s
	if len(p) < 2 {
		s.parseFails(1, p, errors.New("truncated socket.io packet"))
		return nil
	}
	switch p[1] {
	case '0': // connect
		l.sawConn.Store(true)
	case '1':
		return errors.New("server sent a socket.io disconnect (41)")
	case '2': // event: the news
		s.onEvent(recv, p[2:])
	case '3': // ack
	case '4': // error: a bad key or plan most likely looks like this
		l.refused = true
		return fmt.Errorf("newsfilter refused the key or plan (socket.io error frame: %s)", s.excerpt(p[2:], 200))
	case '5', '6': // binary event / ack
		s.n.binary.Add(1)
	default:
		s.parseFails(1, p, fmt.Errorf("unknown socket.io packet type %q", p[1]))
	}
	return nil
}

// onEvent turns one "articles" event into day-file lines: first sightings only,
// all written with the frame's arrival stamp in a single write.
func (s *Stream) onEvent(recv int64, body []byte) {
	res, err := parseEvent(body)
	if err != nil {
		s.parseFails(1, body, err)
		return
	}
	if res.other {
		s.n.otherEvents.Add(1)
		return
	}
	if res.bad > 0 {
		s.parseFails(res.bad, body, errNotObject)
	}
	s.n.noID.Add(int64(res.noID))
	var buf bytes.Buffer
	for _, a := range res.articles {
		s.n.articles.Add(1)
		s.n.badFields.Add(int64(a.badFields))
		if !s.seen.add(a.ID) {
			s.n.dupes.Add(1)
			continue
		}
		s.n.recorded.Add(1)
		syms := make([]string, 0, len(a.Syms))
		for _, sym := range a.Syms {
			syms = append(syms, strings.ToUpper(s.scrub(sym)))
		}
		buf.Write(jsonLine(Record{
			Ms: recv, ID: s.scrub(a.ID), Pub: a.PubMs, Src: s.capped(a.Src, maxSrc), SrcName: s.capped(a.SrcName, maxSrc),
			Title: s.capped(a.Title, maxTitle), Syms: syms, URL: s.capped(a.URL, maxURL),
		}))
	}
	s.save(recv, s.scrubBytes(buf.Bytes()))
}

// scrubBytes keeps the key out of a recorded line even if some field carries it.
func (s *Stream) scrubBytes(b []byte) []byte {
	for _, v := range s.secrets {
		if bytes.Contains(b, []byte(v)) {
			b = bytes.ReplaceAll(b, []byte(v), []byte("***"))
		}
	}
	return b
}

// parseFails counts frames or article elements that could not be used. The
// first few are logged with a payload prefix so a schema mismatch diagnoses
// itself from the ordinary log; after that only every 100th, to stay quiet on
// a bad day.
func (s *Stream) parseFails(delta int, p []byte, err error) {
	after := s.n.parseFails.Add(int64(delta))
	before := after - int64(delta)
	if after <= 3 || after/100 != before/100 {
		s.l().Warn("newsfilter frame not usable", "n", after, "err", s.scrub(err.Error()), "payload", s.excerpt(p, 220))
	}
}

// event appends a connection event to the day file.
func (s *Stream) event(e Event) {
	e.Ms = s.now().UnixMilli()
	s.save(e.Ms, s.scrubBytes(jsonLine(e)))
}

// save appends to the day file. A disk error is counted and logged at most
// once a minute and otherwise ignored: recording must never stop or block the
// stream.
func (s *Stream) save(ms int64, b []byte) {
	if len(b) == 0 {
		return
	}
	if err := s.rec.write(ms, b); err != nil {
		s.n.diskErrors.Add(1)
		if now := s.now(); s.lastDiskLog.IsZero() || now.Sub(s.lastDiskLog) >= time.Minute {
			s.lastDiskLog = now
			s.l().Warn("newsfilter: cannot write the day file (stream keeps running)", "dir", s.cfg.Dir, "err", s.scrub(err.Error()))
		}
	}
}

// tick logs the periodic stats line and writes the alive heartbeat. It rides on
// frame arrival; our own pings make a frame at least every ~20 s while
// connected.
func (s *Stream) tick(l *link) {
	now := s.now()
	if now.Sub(s.lastStats) < s.statsEvery {
		return
	}
	s.lastStats = now
	st := s.Stats()
	s.l().Info("newsfilter stats", "frames", st.Frames, "articles", st.Articles, "dupes", st.Dupes,
		"recorded", st.Recorded, "parseFails", st.ParseFails, "reconnects", st.Reconnects)
	if l.opened {
		s.event(Event{Ev: EvAlive})
	}
}
