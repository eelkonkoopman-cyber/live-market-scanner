// Package bzstream connects to Benzinga's OWN news WebSocket
// (wss://api.benzinga.com/api/v1/news/stream) — true push delivery, ~25ms from
// publication, which eliminates the REST index lag entirely. It is a separate
// product from the Massive Benzinga add-on and needs a Benzinga-issued token
// (SCANNER_BZ_WS_TOKEN, format "bz.production***"); without one the package is
// simply never started and the REST poller carries the load alone.
//
// Delivery contract: batches go through news.Poller.AddExternal in main, so
// the WS and the REST poll share one dedupe — article IDs here get the same
// "bz<id>" prefix the REST client mints, and whichever source sees a story
// first wins while the other's copy is dropped.
package bzstream

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

const (
	defaultURL = "wss://api.benzinga.com/api/v1/news/stream"

	backoffStart = time.Second
	backoffMax   = 5 * time.Minute // includes bad-token; a fixed env token can't heal fast

	// pingEvery keeps the connection warm; Benzinga answers the literal text
	// command "ping" with "pong".
	pingEvery = 30 * time.Second
	readWait  = 90 * time.Second // no frame (news or pong) for this long = dead conn
)

// Batch is one delivered article set (usually a single article per frame).
type Batch struct {
	Items  []core.NewsItem
	Bodies map[string]string
}

type Config struct {
	Token string
	URL   string // override for tests; default wss://api.benzinga.com/api/v1/news/stream

	// Health optionally reports this stream's connection state under the
	// name "bzpush" for the status bar; nil is a no-op (guarded at every call
	// site), so tests and the no-token case never need to supply one.
	Health core.HealthReporter
}

type Stream struct {
	cfg Config
	out chan Batch

	// Diagnostics, all owned by the session goroutine (no locking). The parse
	// failure counters exist because this protocol was built from docs, not
	// observed traffic — any mismatch must be visible in the log, not silent.
	frames     int
	parsed     int
	parseFails int
	badTimes   int
	wireDrops  int // translated-edition frames (content.type "benzinga_wire_*") dropped
	lastStats  time.Time

	dump   *os.File // SCANNER_BZ_WS_DUMP=1: raw frames -> ~/.marketscanner/bzws_dump.jsonl
	dumped int
}

var errNoID = errors.New("frame has no article id")

func New(cfg Config) *Stream {
	if cfg.URL == "" {
		cfg.URL = defaultURL
	}
	return &Stream{cfg: cfg, out: make(chan Batch, 64)}
}

// noteParseFail records a frame the parser could not use. The first few are
// logged with a payload prefix so a schema mismatch diagnoses itself from the
// ordinary log; after that only every 100th, to stay quiet on a bad day.
func (s *Stream) noteParseFail(payload []byte, err error) {
	s.parseFails++
	if s.parseFails <= 3 || s.parseFails%100 == 0 {
		head := string(payload)
		if len(head) > 220 {
			head = head[:220] + "…"
		}
		logx.L().Warn("benzinga ws frame not usable", "n", s.parseFails, "err", err, "payload", head)
	}
}

func (s *Stream) noteBadTime(raw string) {
	s.badTimes++
	if s.badTimes <= 3 {
		logx.L().Warn("benzinga ws timestamp unparseable (stamped as now; latency metric off)", "created", raw)
	}
}

// maybeStats logs a throughput/parse summary every 5 minutes — the cheap way
// to see "the socket is up but delivering nothing" without a debugger.
func (s *Stream) maybeStats() {
	if time.Since(s.lastStats) < 5*time.Minute {
		return
	}
	s.lastStats = time.Now()
	logx.L().Info("benzinga ws stats", "frames", s.frames, "articles", s.parsed,
		"parseFails", s.parseFails, "badTimestamps", s.badTimes, "wireDrops", s.wireDrops)
}

func (s *Stream) dumpFrame(payload []byte) {
	if s.dump == nil || s.dumped >= 500 {
		return
	}
	s.dumped++
	_, _ = s.dump.Write(append(payload, '\n'))
}

// Out delivers parsed article batches (consumer: main's news forward loop).
func (s *Stream) Out() <-chan Batch { return s.out }

// Run connects and reconnects with capped backoff until ctx is done. It never
// returns a fatal error: a broken WS must degrade to REST polling, not kill
// the app.
func (s *Stream) Run(ctx context.Context) error {
	if os.Getenv("SCANNER_BZ_WS_DUMP") == "1" {
		if home, err := os.UserHomeDir(); err == nil {
			path := filepath.Join(home, ".marketscanner", "bzws_dump.jsonl")
			if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err == nil {
				s.dump = f
				defer f.Close()
				logx.L().Info("benzinga ws raw-frame dump enabled", "path", path, "cap", 500)
			}
		}
	}
	backoff := backoffStart
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := s.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if s.cfg.Health != nil {
			s.cfg.Health.SourceFail("bzpush", err.Error())
		}
		logx.L().Warn("benzinga ws dropped; reconnecting", "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, backoffMax)
	}
}

func (s *Stream) session(ctx context.Context) error {
	u := s.cfg.URL + "?token=" + url.QueryEscape(s.cfg.Token)
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	conn, _, err := websocket.Dial(dctx, u, nil)
	cancel()
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "bye")
	conn.SetReadLimit(1 << 22) // article bodies are large; default 32 KiB truncates them

	logx.L().Info("benzinga ws connected (push news active)")
	if s.cfg.Health != nil {
		s.cfg.Health.SourceOK("bzpush")
	}

	// Replay the server's cache first: covers articles published while we were
	// reconnecting. The shared dedupe drops everything already seen.
	if err := conn.Write(ctx, websocket.MessageText, []byte("replay")); err != nil {
		return err
	}

	// Ping loop keeps NATs/proxies from idling the connection out.
	pctx, pcancel := context.WithCancel(ctx)
	defer pcancel()
	go func() {
		tk := time.NewTicker(pingEvery)
		defer tk.Stop()
		for {
			select {
			case <-pctx.Done():
				return
			case <-tk.C:
				if err := conn.Write(pctx, websocket.MessageText, []byte("ping")); err != nil {
					return
				}
			}
		}
	}()

	for {
		rctx, rcancel := context.WithTimeout(ctx, readWait)
		_, payload, err := conn.Read(rctx)
		rcancel()
		if err != nil {
			return err
		}
		s.frames++
		s.maybeStats()
		if string(payload) == "pong" {
			continue
		}
		s.dumpFrame(payload)
		if b, ok := s.parse(payload); ok {
			s.parsed++
			select {
			case s.out <- b:
			default: // consumer behind; drop — the REST overlap poll repairs the gap
			}
		}
	}
}

// --- wire format ---
//
// Built from Benzinga's docs, then hardened against the variants their native
// API is known to ship (the REST feed we poll is Massive's NORMALIZED proxy;
// this socket is Benzinga raw): ids as strings or numbers, stocks as plain
// strings or {name}/{symbol} objects, sometimes under "securities", and
// RFC1123 timestamps alongside ISO 8601.

type envelope struct {
	Kind string `json:"kind"`
	Data struct {
		Action  string  `json:"action"` // created | updated | deleted
		Content content `json:"content"`
	} `json:"data"`
}

// flexID tolerates 123 and "123".
type flexID int64

func (f *flexID) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	*f = flexID(v)
	return err
}

// flexStock tolerates "AAPL", {"name":"AAPL"} and {"symbol":"AAPL"}.
type flexStock struct{ Name string }

func (f *flexStock) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &f.Name)
	}
	var o struct {
		Name   string `json:"name"`
		Symbol string `json:"symbol"`
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return err
	}
	f.Name = o.Name
	if f.Name == "" {
		f.Name = o.Symbol
	}
	return nil
}

type content struct {
	ID    flexID `json:"id"`
	Title string `json:"title"`
	// Type is undocumented but observed live: Benzinga's translated foreign-
	// language editions arrive as "benzinga_wire_<country>" (captured:
	// "benzinga_wire_korea"), while English frames carry "story",
	// "benzinga_partner", "benzinga_slideshow", ... The English list is NOT
	// enumerable from a single connection, so parse() blacklists the wire
	// prefix and keeps everything else — see the drop site for why.
	Type string `json:"type"`
	// CreatedAt is the real field name on Benzinga's native socket — confirmed
	// against a live capture (SCANNER_BZ_WS_DUMP=1). Created is kept as a
	// fallback since it's what the published API docs describe; this API has
	// already been observed to diverge from its own docs once, so don't trust
	// either name alone. Getting this wrong doesn't drop the article (parse
	// still succeeds) — it silently stamps published_ms as "now" instead of
	// the true publish time, corrupting exactly the feed-latency numbers this
	// archive exists to produce. That's how the created/created_at mismatch
	// was found: every push-sourced article had a suspicious ~0ms latency.
	CreatedAt  string      `json:"created_at"`
	Created    string      `json:"created"`
	Teaser     string      `json:"teaser"`
	Body       string      `json:"body"`
	URL        string      `json:"url"`
	Stocks     []flexStock `json:"stocks"`
	Securities []flexStock `json:"securities"` // some payloads use this name
}

// parse converts one WS frame to a Batch. Only "created" (and "updated", which
// the shared dedupe drops when the article is already known) carry news;
// deletions and unknown kinds are ignored.
func (s *Stream) parse(payload []byte) (Batch, bool) {
	var env envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		s.noteParseFail(payload, err)
		return Batch{}, false
	}
	c := env.Data.Content
	if strings.EqualFold(env.Data.Action, "deleted") {
		return Batch{}, false
	}
	// Translated-edition wires (~53% of the stream measured 2026-08) are
	// dropped on sight by their content.type prefix — the cheap belt; the
	// headline-text language gate in internal/news is the braces. This is
	// deliberately a PREFIX BLACKLIST and never an allowed-type list: an
	// unknown new English type ("story_v2") wrongly dropped would silently
	// lose real news, which is the one failure mode this pipeline refuses.
	if strings.HasPrefix(c.Type, "benzinga_wire_") {
		s.wireDrops++
		if s.wireDrops <= 3 || s.wireDrops%100 == 0 {
			logx.L().Debug("benzinga ws translated-wire frame dropped", "n", s.wireDrops, "type", c.Type)
		}
		return Batch{}, false
	}
	if c.ID == 0 {
		s.noteParseFail(payload, errNoID)
		return Batch{}, false
	}

	createdRaw := c.CreatedAt
	if createdRaw == "" {
		createdRaw = c.Created
	}
	ts, tsOK := parseTime(createdRaw)
	if !tsOK {
		s.noteBadTime(createdRaw)
	}
	stocks := c.Stocks
	if len(stocks) == 0 {
		stocks = c.Securities
	}
	tickers := make([]string, 0, len(stocks))
	for _, st := range stocks {
		if t := strings.ToUpper(strings.TrimSpace(st.Name)); t != "" {
			tickers = append(tickers, t)
		}
	}

	// Same ID scheme as the REST client ("bz" + benzinga article id) — this is
	// what makes the WS and REST sources dedupe against each other.
	id := "bz" + strconv.FormatInt(int64(c.ID), 10)
	item := core.NewsItem{
		ID:       id,
		Tickers:  tickers,
		Headline: decodeEntities(c.Title),
		Source:   "Benzinga",
		URL:      c.URL,
		TS:       ts,
		Teaser:   decodeEntities(c.Teaser),
		HasBody:  c.Body != "",
	}
	b := Batch{Items: []core.NewsItem{item}}
	if c.Body != "" {
		b.Bodies = map[string]string{id: c.Body} // raw HTML; the summarizer strips it
	}
	return b, true
}

// parseTime accepts ISO 8601 AND the RFC1123 form Benzinga's native API ships
// ("Mon, 28 Jul 2026 04:27:51 -0400"). The bool reports whether the string
// actually parsed — a now-stamped fallback corrupts the archive's
// received-published latency metric, so callers surface failures loudly.
func parseTime(s string) (int64, bool) {
	for _, layout := range []string{
		time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z0700",
		time.RFC1123Z, time.RFC1123, "2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixMilli(), true
		}
	}
	return time.Now().UnixMilli(), false // better now than dropped
}

// decodeEntities matches the REST client's behavior: Benzinga sends plain-text
// fields HTML-escaped; bodies stay raw HTML for the summarizer.
func decodeEntities(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	return html.UnescapeString(s)
}
