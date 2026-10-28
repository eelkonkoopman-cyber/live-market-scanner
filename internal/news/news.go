// Package news polls the Massive news endpoints and pushes fresh headlines
// onto a channel the server broadcasts. Two sources, merged and deduped:
//
//   - Benzinga (/benzinga/v2/news): real-time, carries the full article body —
//     preferred. If the key lacks the Benzinga expansion (HTTP 403), the source
//     is parked and re-probed every 10 minutes so enabling the add-on later
//     needs no restart.
//   - Reference (/v2/reference/news): all plans, hourly recency, headline +
//     description only — breadth fallback, always polled.
//
// The poller also keeps a bounded article cache (item + full body) that the
// server's Claude summarizer reads via Get(id).
//
// Every entry path — the REST poll here, plus everything merged through
// AddExternal (Benzinga WS push, rtpr wires, autosum chases, SEC EDGAR) —
// passes ONE language gate (keep) that drops Benzinga's translated foreign-
// language editions via internal/lang. SCANNER_NEWS_FOREIGN=1 bypasses it.
package news

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/lang"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

const (
	// Benzinga is the real-time source, so end-to-end news latency is dominated by
	// this interval: a headline sits undiscovered for up to one tick. Poll it hard.
	defaultBzInterval = 1 * time.Second

	// bzOverlap is subtracted from the Benzinga cursor on every poll. The cursor
	// tracks the newest PUBLISH timestamp seen, but Benzinga's REST index runs
	// seconds behind publication — so an article that indexes late gets
	// leapfrogged by the cursor and, with an exact-cursor query, would never be
	// fetched at all. Re-querying the trailing window costs a few duplicate
	// results (dropped by the seen/seenURL dedupe) and guarantees late-indexed
	// articles are picked up. Sizing: index lag is seconds, so 30s covers it
	// several times over — and since everything in the window is re-downloaded
	// WITH BODIES every tick, each extra minute here is pure duplicate payload
	// (a 2-min window measured ~110-320 MB/hour of discarded bytes). Entrants
	// with slower headlines are covered separately by the autosum chaser.
	bzOverlap = 30 * time.Second
	// Reference news only carries hourly recency (see the package doc), so polling
	// it at the Benzinga rate is pure waste — it can't surface anything new that
	// fast. Decoupling the two cuts news latency 4x while REDUCING total requests
	// against the endpoint that never has fresh data.
	defaultRefInterval = 60 * time.Second
	// minInterval floors both intervals so a misconfigured env var can't turn the
	// poller into a rate-limit magnet.
	minInterval = 500 * time.Millisecond

	bzRetryAfter  = 10 * time.Minute // re-probe Benzinga entitlement
	cacheCapacity = 800              // articles kept for summarization

	// Rate-limit backoff bounds. Faster polling makes a 429 far more likely, so
	// the poller must yield rather than hammer through it.
	rlBackoffStart = 5 * time.Second
	rlBackoffMax   = 2 * time.Minute

	// foreignDropLogEvery paces the language gate's Debug line: ~53% of the
	// push stream is translated editions, so per-drop logging would be its
	// own firehose. The first drop always logs (proof the gate is live).
	foreignDropLogEvery = 25
)

type cached struct {
	item core.NewsItem
	body string
}

type Poller struct {
	feed core.FeedClient
	out  chan []core.NewsItem

	// bzInterval drives the loop (the real-time source); refInterval gates the
	// slow breadth source, which is polled on a multiple of the same tick.
	bzInterval  time.Duration
	refInterval time.Duration
	nextRef     time.Time

	lastBz  int64 // newest Benzinga publish ms seen
	lastRef int64 // newest reference publish ms seen
	seen    map[string]bool
	seenURL map[string]bool

	bzDown    bool
	bzRetryAt time.Time

	// allowForeign bypasses the language gate (SCANNER_NEWS_FOREIGN=1). Read
	// once at construction — never per item — and immutable after New, so
	// keep() reads it without a lock. foreignDrops counts gate drops across
	// ALL entry paths; atomic because AddExternal runs on several goroutines
	// (bzstream, rtpr, autosum, edgar) concurrently with the poll loop.
	allowForeign bool
	foreignDrops atomic.Int64

	// rate-limit backoff (429): pause polling entirely until rlUntil.
	rlUntil   time.Time
	rlBackoff time.Duration

	mu         sync.RWMutex
	cache      map[string]cached
	cacheOrder []string // FIFO eviction

	// sink receives every accepted (post-dedupe) article with its source tag
	// ("poll", "push", "chase") — the persistence hook. Set before Run starts
	// and never changed after, so it is read without a lock. It is called
	// outside p.mu and must not block (main wires it to a buffered channel
	// with a drop-on-full send).
	sink func(items []core.NewsItem, bodies map[string]string, via string)
}

// Option customizes a Poller at construction.
type Option func(*Poller)

// WithIntervals overrides the poll cadences. Non-positive values keep the
// default; both are floored at minInterval.
func WithIntervals(bz, ref time.Duration) Option {
	return func(p *Poller) {
		if bz > 0 {
			p.bzInterval = max(bz, minInterval)
		}
		if ref > 0 {
			p.refInterval = max(ref, minInterval)
		}
	}
}

func New(fc core.FeedClient, opts ...Option) *Poller {
	p := &Poller{
		feed:         fc,
		out:          make(chan []core.NewsItem, 16),
		bzInterval:   defaultBzInterval,
		refInterval:  defaultRefInterval,
		seen:         make(map[string]bool),
		seenURL:      make(map[string]bool),
		cache:        make(map[string]cached),
		allowForeign: os.Getenv("SCANNER_NEWS_FOREIGN") == "1",
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Out is the channel of fresh news batches for the server to broadcast.
func (p *Poller) Out() <-chan []core.NewsItem { return p.out }

// Get returns a cached article and its full body ("" if the source had none).
// Called from server goroutines; safe concurrently with polling.
func (p *Poller) Get(id string) (core.NewsItem, string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	c, ok := p.cache[id]
	return c.item, c.body, ok
}

// SetSink registers the persistence hook. Must be called before Run (and
// before any AddExternal caller starts) — the field is read unlocked.
func (p *Poller) SetSink(fn func(items []core.NewsItem, bodies map[string]string, via string)) {
	p.sink = fn
}

func (p *Poller) emitSink(items []core.NewsItem, bodies map[string]string, via string) {
	if p.sink != nil && len(items) > 0 {
		p.sink(items, bodies, via)
	}
}

// keep is the ONE language door every source passes through: the poll loop's
// add closure AND AddExternal (Benzinga WS push, rtpr, autosum chases, SEC
// EDGAR). EDGAR's "SEC filing: ..." headlines are English by construction but
// do NOT pass trivially — they embed the filer's registered name, often an
// ALL-CAPS Latin-American one ("COMPANHIA DE SANEAMENTO ... DO ESTADO DE SAO
// PAULO"), which is exactly why lang's stopword duel weighs token case; the
// gate test pins that shape. False means the headline reads as one of Benzinga's
// translated foreign-language editions and is dropped before dedupe/cache,
// so it is never cached, never marked seen, never archived. A single shared
// helper — not per-path checks — because a gate present on only one path is
// exactly the silent regression the tests below exist to prevent. Pure
// lang.Foreign underneath: every tie keeps, an English headline wrongly
// dropped being strictly worse than a foreign one kept.
func (p *Poller) keep(it core.NewsItem) bool {
	if p.allowForeign || !lang.Foreign(it.Headline) {
		return true
	}
	if n := p.foreignDrops.Add(1); n == 1 || n%foreignDropLogEvery == 0 {
		logx.L().Debug("foreign-language headlines dropped", "total", n, "last", it.Headline)
	}
	return false
}

// AddExternal merges articles fetched outside the poll loop (the auto-
// summarizer's per-symbol day fetch, the Benzinga push stream) into the cache,
// returning only the items that were new — callers broadcast those so the
// panel sees them once. via tags the source for the persistence sink.
func (p *Poller) AddExternal(items []core.NewsItem, bodies map[string]string, via string) []core.NewsItem {
	fresh := p.addExternalLocked(items, bodies)
	p.emitSink(fresh, bodies, via) // outside p.mu: the sink is foreign code
	return fresh
}

func (p *Poller) addExternalLocked(items []core.NewsItem, bodies map[string]string) []core.NewsItem {
	p.mu.Lock()
	defer p.mu.Unlock()
	var fresh []core.NewsItem
	for _, it := range items {
		if it.ID == "" {
			continue
		}
		if !p.keep(it) {
			continue // translated edition — gated before it can enter cache/seen
		}
		if _, ok := p.cache[it.ID]; ok {
			continue
		}
		p.cache[it.ID] = cached{item: it, body: bodies[it.ID]}
		p.cacheOrder = append(p.cacheOrder, it.ID)
		p.seen[it.ID] = true // poll loop must not re-emit it
		if u := normURL(it.URL); u != "" {
			p.seenURL[u] = true
		}
		fresh = append(fresh, it)
	}
	for len(p.cacheOrder) > cacheCapacity {
		delete(p.cache, p.cacheOrder[0])
		p.cacheOrder = p.cacheOrder[1:]
	}
	return fresh
}

// ItemsFor returns cached articles mentioning sym published at/after sinceMs,
// newest first — the fallback source when Benzinga isn't entitled.
func (p *Poller) ItemsFor(sym string, sinceMs int64) []core.NewsItem {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []core.NewsItem
	for _, c := range p.cache {
		if c.item.TS < sinceMs {
			continue
		}
		for _, t := range c.item.Tickers {
			if t == sym {
				out = append(out, c.item)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS > out[j].TS })
	return out
}

func (p *Poller) Run(ctx context.Context) error {
	// Seed from the last 30 minutes so the panel isn't empty on launch.
	start := time.Now().Add(-30 * time.Minute).UnixMilli()
	p.lastBz, p.lastRef = start, start
	tk := time.NewTicker(p.bzInterval)
	defer tk.Stop()
	logx.L().Info("news poller started", "benzinga", p.bzInterval, "reference", p.refInterval)
	p.poll(ctx)
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
	// Backing off after a 429 — skip this tick entirely rather than compounding it.
	if !p.rlUntil.IsZero() && time.Now().Before(p.rlUntil) {
		return
	}

	var fresh []core.NewsItem
	bodies := map[string]string{}
	batchURL := map[string]bool{} // in-batch cross-source dedupe

	add := func(it core.NewsItem) bool {
		if !p.keep(it) {
			return false // translated edition — same door AddExternal uses
		}
		u := normURL(it.URL)
		if p.dupe(it) || (u != "" && batchURL[u]) {
			return false
		}
		if u != "" {
			batchURL[u] = true
		}
		fresh = append(fresh, it)
		return true
	}

	// Benzinga first: real-time + bodies. Entitlement failures park the source.
	if !p.bzDown || time.Now().After(p.bzRetryAt) {
		items, bs, err := p.feed.BenzingaNews(ctx, p.lastBz-bzOverlap.Milliseconds())
		switch {
		case err == nil:
			if p.bzDown {
				logx.L().Info("benzinga news source is now available")
				p.bzDown = false
			}
			p.clearBackoff()
			for _, it := range items {
				if !add(it) {
					continue
				}
				if b := bs[it.ID]; b != "" {
					bodies[it.ID] = b
				}
			}
		case errors.Is(err, core.ErrNotEntitled):
			if !p.bzDown {
				logx.L().Warn("benzinga news not entitled on this key; using reference news (re-probing periodically)", "err", err)
			}
			p.bzDown = true
			p.bzRetryAt = time.Now().Add(bzRetryAfter)
		case core.IsRateLimited(err):
			p.backoff("benzinga")
			return
		default:
			logx.L().Warn("benzinga news poll failed", "err", err)
		}
	}

	// Reference news: breadth across publishers. Its recency is hourly, so it is
	// polled on its own much slower schedule — polling it every Benzinga tick
	// would burn requests for data that cannot have changed.
	refDue := !time.Now().Before(p.nextRef)
	if refDue {
		p.nextRef = time.Now().Add(p.refInterval)
		refItems, err := p.feed.NewsSince(ctx, p.lastRef)
		switch {
		case err == nil:
			for _, it := range refItems {
				add(it)
			}
		case core.IsRateLimited(err):
			p.backoff("reference")
		default:
			logx.L().Warn("news poll failed", "err", err)
		}
	}

	if len(fresh) == 0 {
		return
	}
	sort.Slice(fresh, func(i, j int) bool { return fresh[i].TS < fresh[j].TS }) // oldest first; UI prepends

	// Commit (advance cursors + mark seen + cache) ONLY on a successful send,
	// so a dropped batch is re-fetched next poll rather than silently lost.
	select {
	case p.out <- fresh:
		p.commit(fresh, bodies)
		p.emitSink(fresh, bodies, "poll")
	default:
		// Consumer behind; leave cursors/seen unchanged -> retried next poll. The
		// reference source must also be re-armed, or the items it contributed to
		// this dropped batch would wait a full refInterval instead of one tick.
		if refDue {
			p.nextRef = time.Time{}
		}
	}
}

// backoff pauses all polling after a 429, doubling the wait up to rlBackoffMax.
// A successful poll clears it (see clearBackoff).
func (p *Poller) backoff(source string) {
	if p.rlBackoff == 0 {
		p.rlBackoff = rlBackoffStart
	} else if p.rlBackoff < rlBackoffMax {
		p.rlBackoff *= 2
		if p.rlBackoff > rlBackoffMax {
			p.rlBackoff = rlBackoffMax
		}
	}
	p.rlUntil = time.Now().Add(p.rlBackoff)
	logx.L().Warn("news source rate-limited; backing off",
		"source", source, "for", p.rlBackoff,
		"hint", "raise SCANNER_NEWS_POLL if this repeats")
}

// clearBackoff resets the rate-limit state after a clean poll.
func (p *Poller) clearBackoff() {
	p.rlBackoff = 0
	p.rlUntil = time.Time{}
}

// dupe reports whether the item was already emitted (by ID, or by URL when the
// same story arrives via both sources). It runs on the poll goroutine but MUST
// take the read lock: AddExternal writes seen/seenURL concurrently from autosum
// (per-entry fetches, chasers) and the Benzinga push stream — an unlocked read
// against those writes is a fatal "concurrent map read and map write".
func (p *Poller) dupe(it core.NewsItem) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if it.ID != "" && p.seen[it.ID] {
		return true
	}
	if u := normURL(it.URL); u != "" && p.seenURL[u] {
		return true
	}
	return false
}

func (p *Poller) commit(fresh []core.NewsItem, bodies map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, it := range fresh {
		if it.ID != "" {
			p.seen[it.ID] = true
			// Membership check before the FIFO append (mirrors AddExternal):
			// after a seen-map reset the overlap window re-commits items that
			// are still cached, and a duplicate cacheOrder entry would later
			// evict the article while its ghost still held a capacity slot.
			if _, ok := p.cache[it.ID]; !ok {
				p.cacheOrder = append(p.cacheOrder, it.ID)
			}
			p.cache[it.ID] = cached{item: it, body: bodies[it.ID]}
		}
		if u := normURL(it.URL); u != "" {
			p.seenURL[u] = true
		}
		isBz := strings.HasPrefix(it.ID, "bz")
		if isBz && it.TS > p.lastBz {
			p.lastBz = it.TS
		}
		if !isBz && it.TS > p.lastRef {
			p.lastRef = it.TS
		}
	}
	for len(p.cacheOrder) > cacheCapacity {
		delete(p.cache, p.cacheOrder[0])
		p.cacheOrder = p.cacheOrder[1:]
	}
	if len(p.seen) > 5000 { // bound memory
		p.seen = make(map[string]bool)
	}
	if len(p.seenURL) > 5000 {
		p.seenURL = make(map[string]bool)
	}
}

// normURL canonicalizes an article URL for cross-source dedupe.
func normURL(u string) string {
	u = strings.TrimSpace(strings.ToLower(u))
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimPrefix(u, "http://")
	u = strings.TrimPrefix(u, "www.")
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	return strings.TrimSuffix(u, "/")
}
