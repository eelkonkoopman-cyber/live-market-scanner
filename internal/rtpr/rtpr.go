// Package rtpr polls RTPR (rtpr.io), a third-party aggregator of Business
// Wire, PR Newswire, GlobeNewswire and AccessWire — added to close the gap
// where Benzinga's own write-up of a company press release lags the wire
// itself by minutes (observed directly: a GLMD release was on the wire ~9
// minutes before Benzinga published its version).
//
// Unlike Benzinga, RTPR has no raw "everything" push socket — their alerts
// WebSocket only fires on customer-defined rules. For blanket coverage of our
// whole universe, the REST "Article Feed API" (most-recent-first, paginated)
// is the right surface, polled the same way the Benzinga REST poller is.
//
// The feed is also body-free by design: both it and the alerts socket return
// only a ticker/timestamp/permalink ref. The permalink itself returns a
// server-rendered HTML page (confirmed live — no Accept header, .json suffix,
// or format=json query param produces JSON), so getting the headline/body
// means parsing that page (parse.go), not decoding a versioned JSON schema.
//
// Delivery contract: batches go through news.Poller.AddExternal in main, via
// "rtpr" — sharing the same dedupe/cache/archive as Benzinga's poll, push, and
// the per-symbol chaser.
package rtpr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

const (
	defaultRESTBase = "https://api.rtpr.io"

	defaultPollInterval = 3 * time.Second // see budget comment below on why not 1s
	defaultPageLimit    = 100
	maxPagesPerPoll     = 5 // bounds worst-case request cost during a burst

	// RTPR's documented cap is 60 req/min, SHARED across the ref-poll and every
	// per-article content fetch on one key. requestsPerMinute is set below that
	// (not at it) so our own steady polling never trips their limiter — the
	// margin is spent on content fetches, which is where volume actually
	// varies (an open-bell burst of new articles costs one fetch each).
	requestsPerMinute = 50

	rlBackoffStart = 5 * time.Second
	rlBackoffMax   = 2 * time.Minute
)

var errRateLimited = errors.New("rtpr: rate limited")

// Batch mirrors bzstream's shape: one delivered set of articles plus their
// bodies, keyed by the same id scheme AddExternal expects.
type Batch struct {
	Items  []core.NewsItem
	Bodies map[string]string
}

type Config struct {
	Token string

	// RESTBase overrides the feed API host; tests point this at an httptest
	// server. Article permalinks are always followed exactly as returned by
	// the feed (that's the API's own contract), so there is no separate
	// override for them.
	RESTBase string

	PollInterval time.Duration
	PageLimit    int // articles requested per feed page; RTPR allows 1-500

	// Priority reports whether a ticker's article should jump the fetch queue
	// ahead of others discovered in the same poll (e.g. a burst of
	// simultaneous releases). nil means no reordering — fetched in discovery
	// (oldest-published-first) order, as before this existed.
	Priority func(ticker string) bool

	// Health optionally reports this poller's ref-feed health under the name
	// "rtpr" for the status bar; nil is a no-op (guarded at every call site).
	Health core.HealthReporter
}

type Poller struct {
	cfg  Config
	http *http.Client
	out  chan Batch

	budget *reqBudget

	// seen bounds re-fetching content for articles already forwarded. This is
	// LOCAL bookkeeping for "where to stop paginating backward" — a separate
	// concern from AddExternal's own cross-source id/URL dedupe downstream.
	seen      map[string]bool
	seenOrder []string
	baseline  bool // true until the first poll has seeded `seen`

	rlUntil   time.Time
	rlBackoff time.Duration
}

func New(cfg Config) *Poller {
	if cfg.RESTBase == "" {
		cfg.RESTBase = defaultRESTBase
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if cfg.PageLimit <= 0 {
		cfg.PageLimit = defaultPageLimit
	}
	return &Poller{
		cfg:      cfg,
		http:     &http.Client{Timeout: 20 * time.Second},
		out:      make(chan Batch, 64),
		budget:   newReqBudget(requestsPerMinute),
		seen:     make(map[string]bool),
		baseline: true,
	}
}

// Out delivers article batches (consumer: main's news forward loop).
func (p *Poller) Out() <-chan Batch { return p.out }

func (p *Poller) Run(ctx context.Context) error {
	tk := time.NewTicker(p.cfg.PollInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tk.C:
			p.poll(ctx)
		}
	}
}

func (p *Poller) poll(ctx context.Context) {
	if !p.rlUntil.IsZero() && time.Now().Before(p.rlUntil) {
		return // backing off after a 429 — skip this tick entirely
	}

	if p.baseline {
		p.seedBaseline(ctx)
		return
	}

	refs, err := p.collectFresh(ctx)
	if err != nil {
		if errors.Is(err, errRateLimited) {
			p.backoff()
		} else {
			logx.L().Warn("rtpr feed poll failed", "err", err)
			if p.cfg.Health != nil {
				p.cfg.Health.SourceFail("rtpr", err.Error())
			}
		}
		return
	}
	if p.cfg.Health != nil {
		p.cfg.Health.SourceOK("rtpr")
	}
	if len(refs) == 0 {
		p.clearBackoff()
		return
	}

	// Move priority tickers (e.g. a "cheap stocks" scan's price ceiling) to
	// the front of this poll's fetch queue, stable within each tier so
	// discovery order (oldest-published-first) is preserved among ties. This
	// runs BEFORE the fetch loop below, so a priority ticker's article is
	// both fetched and delivered ahead of non-priority ones from the same
	// poll, not just re-labeled after the fact.
	if p.cfg.Priority != nil {
		sort.SliceStable(refs, func(i, j int) bool {
			return p.cfg.Priority(refs[i].Ticker) && !p.cfg.Priority(refs[j].Ticker)
		})
	}

	// Deliver each article the moment ITS OWN fetch completes, instead of
	// accumulating the whole poll's refs into one batch and sending it only
	// after every fetch in the loop finishes. Under a burst (many releases
	// landing on the wire within the same second — routine right at 08:00 ET
	// premarket) a single slow/budget-throttled fetch anywhere in this list
	// would otherwise hold every ALREADY-fetched article hostage until the
	// whole backlog drains. Observed live: two names' articles were part of a
	// 58-article 08:00:00 ET burst and both landed ~90-96s after publish,
	// batched together with ~50 unrelated tickers, because the batch-send was
	// gated on the slowest fetch in that one poll() call finishing.
	for _, r := range refs {
		art, ok := p.fetchArticle(ctx, r.URL)
		if !ok {
			continue // expired hot-window link or transient fetch failure; not fatal
		}
		id := "rtpr" + idFromURL(r.URL)
		bodies := map[string]string{}
		if art.BodyHTML != "" {
			bodies[id] = art.BodyHTML
		}
		select {
		case p.out <- Batch{Items: []core.NewsItem{art.toNewsItem(id, r.URL)}, Bodies: bodies}:
		default: // consumer behind; drop — the next poll's ref list already moved on
		}
	}
	p.clearBackoff()
}

// seedBaseline marks the current top page as already-seen WITHOUT emitting it,
// so a (re)start doesn't replay recent history as if it were breaking news —
// the same "suppress the first evaluation" idiom internal/scan uses for scan
// entries.
func (p *Poller) seedBaseline(ctx context.Context) {
	resp, err := p.fetchPage(ctx, "")
	if err != nil {
		if errors.Is(err, errRateLimited) {
			p.backoff()
		} else {
			logx.L().Warn("rtpr feed baseline seed failed; will retry", "err", err)
			if p.cfg.Health != nil {
				p.cfg.Health.SourceFail("rtpr", err.Error())
			}
		}
		return // p.baseline stays true; retried next tick
	}
	for _, r := range resp.Articles {
		p.markSeen(idFromURL(r.URL))
	}
	p.baseline = false
	p.clearBackoff()
	if p.cfg.Health != nil {
		p.cfg.Health.SourceOK("rtpr")
	}
}

// ref is one entry from the feed API's body-free article list.
type ref struct {
	Ticker string
	PubAt  string
	URL    string
}

// collectFresh walks the feed newest-first, paginating via next_token, until
// it hits an already-seen article or runs out of pages (bounded by
// maxPagesPerPoll so a pathological gap can't turn one tick into an unbounded
// backfill). Returned oldest-first, matching how every other source in this
// app emits.
func (p *Poller) collectFresh(ctx context.Context) ([]ref, error) {
	var fresh []ref
	token := ""
	for page := 0; page < maxPagesPerPoll; page++ {
		resp, err := p.fetchPage(ctx, token)
		if err != nil {
			if len(fresh) > 0 && errors.Is(err, errRateLimited) {
				break // keep what we already found rather than discard it
			}
			return nil, err
		}
		stoppedAtSeen := false
		for _, r := range resp.Articles {
			if p.seen[idFromURL(r.URL)] {
				stoppedAtSeen = true
				break // newest-first order -> everything further is seen too
			}
			fresh = append(fresh, r)
		}
		if stoppedAtSeen || resp.NextToken == "" {
			break
		}
		token = resp.NextToken
	}
	for _, r := range fresh {
		p.markSeen(idFromURL(r.URL))
	}
	for i, j := 0, len(fresh)-1; i < j; i, j = i+1, j-1 {
		fresh[i], fresh[j] = fresh[j], fresh[i]
	}
	return fresh, nil
}

func (p *Poller) markSeen(id string) {
	if p.seen[id] {
		return
	}
	p.seen[id] = true
	p.seenOrder = append(p.seenOrder, id)
	if len(p.seenOrder) > 5000 { // bound memory, same cap news.go uses
		drop := p.seenOrder[0]
		p.seenOrder = p.seenOrder[1:]
		delete(p.seen, drop)
	}
}

func (p *Poller) backoff() {
	if p.rlBackoff == 0 {
		p.rlBackoff = rlBackoffStart
	} else {
		p.rlBackoff = min(p.rlBackoff*2, rlBackoffMax)
	}
	p.rlUntil = time.Now().Add(p.rlBackoff)
}

func (p *Poller) clearBackoff() { p.rlBackoff = 0 }

// --- HTTP ---

type feedResp struct {
	Count     int    `json:"count"`
	NextToken string `json:"next_token"`
	Articles  []ref  `json:"articles"`
}

func (r *ref) UnmarshalJSON(b []byte) error {
	var w struct {
		Ticker string `json:"ticker"`
		PubAt  string `json:"article_published_at"`
		URL    string `json:"article_url"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	r.Ticker, r.PubAt, r.URL = w.Ticker, w.PubAt, w.URL
	return nil
}

func (p *Poller) fetchPage(ctx context.Context, nextToken string) (feedResp, error) {
	if err := p.budget.wait(ctx); err != nil {
		return feedResp{}, err
	}
	u := p.cfg.RESTBase + "/feed/articles?limit=" + strconv.Itoa(p.cfg.PageLimit)
	if nextToken != "" {
		u += "&next_token=" + url.QueryEscape(nextToken)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return feedResp{}, err
	}
	req.Header.Set("Authorization", "Bearer "+p.cfg.Token)
	resp, err := p.http.Do(req)
	if err != nil {
		return feedResp{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return feedResp{}, errRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return feedResp{}, fmt.Errorf("rtpr feed: status %d: %s", resp.StatusCode, b)
	}
	var out feedResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return feedResp{}, fmt.Errorf("rtpr feed: decode: %w", err)
	}
	return out, nil
}

// fetchArticle follows a permalink exactly as given (RTPR's stated contract)
// and parses the returned HTML page. A non-200 or unparseable page is treated
// as "skip this one" rather than a hard error — an expired hot-window link or
// a template hiccup shouldn't stop the rest of the batch.
func (p *Poller) fetchArticle(ctx context.Context, articleURL string) (article, bool) {
	if err := p.budget.wait(ctx); err != nil {
		return article{}, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, articleURL, nil)
	if err != nil {
		return article{}, false
	}
	req.Header.Set("Authorization", "Bearer "+p.cfg.Token)
	resp, err := p.http.Do(req)
	if err != nil {
		logx.L().Warn("rtpr article fetch failed", "url", articleURL, "err", err)
		return article{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return article{}, false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return article{}, false
	}
	return parseArticle(body)
}

// --- rate budget ---

// reqBudget throttles combined ref-poll + content-fetch calls to stay under
// RTPR's 60/min cap, which is shared across both surfaces on one API key.
// Blocking: callers wait for a token rather than being dropped, so a burst is
// delayed, never silently lost.
//
// Refill is SMOOTHED (one token every window/max) rather than dumped in one
// lump once per window. A lump-sum refill still averages out to the same
// max/window rate, but once a burst drains the bucket to zero, every caller
// then has to wait for the FULL window to elapse before a single token
// reappears — observed live: a burst of ~58 simultaneous articles drained the
// bucket and the tail of that batch stalled up to ~60s waiting on the next
// once-a-minute top-up. Smoothing removes that stall/no-stall cliff while
// keeping the same long-run rate.
type reqBudget struct{ tokens chan struct{} }

func newReqBudget(max int) *reqBudget {
	return newReqBudgetEvery(max, time.Minute)
}

// newReqBudgetEvery is the real constructor; window is a parameter (rather
// than a hardcoded time.Minute) purely so tests can use a short window
// instead of waiting on a real minute-long tick.
func newReqBudgetEvery(max int, window time.Duration) *reqBudget {
	b := &reqBudget{tokens: make(chan struct{}, max)}
	for i := 0; i < max; i++ {
		b.tokens <- struct{}{}
	}
	go b.refill(max, window)
	return b
}

func (b *reqBudget) refill(max int, window time.Duration) {
	interval := window / time.Duration(max)
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for range tk.C {
		select {
		case b.tokens <- struct{}{}:
		default: // already full
		}
	}
}

func (b *reqBudget) wait(ctx context.Context) error {
	select {
	case <-b.tokens:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
