// Package autosum turns scanner entries into automatic Claude news summaries:
// when a symbol newly enters a scan, it pulls the day's articles for that
// symbol (Benzinga per-ticker fetch; poller cache as fallback), summarizes the
// newest few that aren't already cached, and broadcasts both the articles and
// the summaries so the UI has them ready before the trader clicks anything.
//
// Cost controls: per-symbol cooldown, per-symbol daily article budget, global
// daily budget, and the summarizer's own concurrency semaphore. Disable
// entirely with SCANNER_AUTOSUM=0.
package autosum

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
	"github.com/eelkonkoopman/marketscanner/internal/summarize"
)

// Articles abstracts the news poller (cache merge + per-ticker lookup).
type Articles interface {
	Get(id string) (core.NewsItem, string, bool)
	AddExternal(items []core.NewsItem, bodies map[string]string, via string) []core.NewsItem
	ItemsFor(sym string, sinceMs int64) []core.NewsItem
}

// Summarizer abstracts summarize.Service for tests.
type Summarizer interface {
	Summarize(ctx context.Context, a summarize.Article) (string, error)
	Model() string
}

type Config struct {
	Entries <-chan core.ScanEntry
	Feed    core.FeedClient
	News    Articles
	Sum     Summarizer
	Cache   core.SummaryRepo

	MaxPerSym int           // articles auto-summarized per symbol per day (default 3)
	MaxPerDay int           // global auto-summary budget per day (default 60)
	Cooldown  time.Duration // min gap between runs for the same symbol (default 10m)
	Now       func() time.Time

	// News chaser: a scanner pop usually BEATS Benzinga's REST index — the
	// price moves within a second, the headline becomes queryable seconds
	// later. When an entrant has no fresh article yet, re-fetch just that
	// ticker on this schedule until one lands (or the schedule runs out).
	// Disable with SCANNER_NEWS_CHASE=0.
	ChaseDelays []time.Duration // waits between re-fetches (default ~90s total)
	FreshWindow time.Duration   // "fresh" = published within this (default 10m)
}

type Worker struct {
	cfg  Config
	news chan []core.NewsItem
	sums chan core.NewsSummaryMsg

	mu      sync.Mutex
	lastRun map[string]time.Time // sym -> last auto-run
	symDone map[string]int       // sym -> summaries produced today
	dayDone int                  // global summaries produced today
	day     string               // ET date the counters belong to

	loc      *time.Location
	enabled  bool
	chaseOn  bool
	chaseSem chan struct{} // caps concurrent chasers; full = skip the chase

	// fetchSem bounds in-flight per-ticker REST calls (initial fetches AND
	// chase attempts). An entry burst at the open — dozens of symbols within
	// seconds — must queue here instead of firing dozens of concurrent
	// requests at the same key the 1 Hz market-wide poll depends on: a
	// self-inflicted 429 pauses ALL news for 5s-2min at the worst moment.
	// Blocking acquire: entrant fetches are delayed, never dropped.
	fetchSem chan struct{}
}

func New(cfg Config) *Worker {
	if cfg.MaxPerSym <= 0 {
		cfg.MaxPerSym = envInt("SCANNER_AUTOSUM_MAX", 3)
	}
	if cfg.MaxPerDay <= 0 {
		cfg.MaxPerDay = envInt("SCANNER_AUTOSUM_DAILY", 60)
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 10 * time.Minute
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if len(cfg.ChaseDelays) == 0 {
		// ~90s of decaying re-fetches: dense while the move is seconds old
		// (where the index lag usually resolves), sparse toward the tail.
		cfg.ChaseDelays = []time.Duration{
			3 * time.Second, 6 * time.Second, 12 * time.Second,
			24 * time.Second, 45 * time.Second,
		}
	}
	if cfg.FreshWindow <= 0 {
		cfg.FreshWindow = 10 * time.Minute
	}
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.UTC
	}
	return &Worker{
		cfg:      cfg,
		news:     make(chan []core.NewsItem, 16),
		sums:     make(chan core.NewsSummaryMsg, 64),
		lastRun:  map[string]time.Time{},
		symDone:  map[string]int{},
		loc:      loc,
		enabled:  os.Getenv("SCANNER_AUTOSUM") != "0",
		chaseOn:  os.Getenv("SCANNER_NEWS_CHASE") != "0",
		chaseSem: make(chan struct{}, 4),
		fetchSem: make(chan struct{}, 4),
	}
}

// News carries articles discovered by per-symbol fetches (server broadcasts).
func (w *Worker) News() <-chan []core.NewsItem { return w.news }

// Summaries carries finished auto-summaries (server broadcasts to all clients).
func (w *Worker) Summaries() <-chan core.NewsSummaryMsg { return w.sums }

func (w *Worker) Run(ctx context.Context) error {
	if !w.enabled {
		logx.L().Info("auto-summarize disabled (SCANNER_AUTOSUM=0)")
		<-ctx.Done()
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-w.cfg.Entries:
			if w.shouldRun(e.Sym) {
				go w.runSym(ctx, e.Sym)
			}
		}
	}
}

// shouldRun applies the per-symbol cooldown + daily budgets (single check-and-
// mark so concurrent entries for one symbol can't double-run).
func (w *Worker) shouldRun(sym string) bool {
	now := w.cfg.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	if d := now.In(w.loc).Format("2006-01-02"); d != w.day {
		w.day = d
		w.symDone = map[string]int{}
		w.dayDone = 0
	}
	if w.dayDone >= w.cfg.MaxPerDay || w.symDone[sym] >= w.cfg.MaxPerSym {
		return false
	}
	if now.Sub(w.lastRun[sym]) < w.cfg.Cooldown {
		return false
	}
	w.lastRun[sym] = now
	return true
}

// fetch pulls sym's day-news once: Benzinga per-ticker (poller cache as the
// un-entitled fallback), merging anything new into the shared cache and
// broadcasting it so the grid's news dot lights up immediately. The error is
// returned so the chaser can tell "nothing indexed yet" from "stop trying".
func (w *Worker) fetch(ctx context.Context, sym string, since int64) ([]core.NewsItem, map[string]string, error) {
	select {
	case w.fetchSem <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	items, bodies, err := w.cfg.Feed.BenzingaNewsFor(ctx, sym, since)
	<-w.fetchSem
	if err != nil {
		if !errors.Is(err, core.ErrNotEntitled) {
			logx.L().Warn("autosum news fetch failed", "sym", sym, "err", err)
		}
		return w.cfg.News.ItemsFor(sym, since), nil, err // fallback: whatever the poller saw
	}
	if fresh := w.cfg.News.AddExternal(items, bodies, "chase"); len(fresh) > 0 {
		select {
		case w.news <- fresh: // panel + scanner learn about these immediately
		default:
		}
	}
	return items, bodies, nil
}

// hasFresh reports whether any article was published within the fresh window —
// i.e. whether the entry's move already has a headline attached.
func (w *Worker) hasFresh(items []core.NewsItem) bool {
	cut := w.cfg.Now().Add(-w.cfg.FreshWindow).UnixMilli()
	for _, it := range items {
		if it.TS >= cut {
			return true
		}
	}
	return false
}

// chase re-fetches sym on a decaying schedule until a fresh headline appears.
// It exists because the price pop and its headline race: the scanner sees the
// move on a 1-second tick while Benzinga's REST index runs seconds behind, so
// the one fetch at entry time routinely comes back empty-handed. Returns the
// final item set (with bodies) once something fresh lands, or nil if the
// schedule runs out.
func (w *Worker) chase(ctx context.Context, sym string, since int64) ([]core.NewsItem, map[string]string) {
	select {
	case w.chaseSem <- struct{}{}:
		defer func() { <-w.chaseSem }()
	default:
		return nil, nil // too many concurrent chasers; the 1s global poll still covers this
	}
	t := time.NewTimer(0)
	defer t.Stop()
	for i, d := range w.cfg.ChaseDelays {
		t.Reset(d)
		select {
		case <-ctx.Done():
			return nil, nil
		case <-t.C:
		}
		items, bodies, err := w.fetch(ctx, sym, since)
		switch {
		case errors.Is(err, core.ErrNotEntitled), core.IsRateLimited(err):
			return nil, nil // no point (403), or we're part of the problem (429)
		case err != nil:
			continue // transient; the next attempt may succeed
		}
		if w.hasFresh(items) {
			logx.L().Info("news chase caught the headline", "sym", sym, "attempt", i+1)
			return items, bodies
		}
	}
	return nil, nil
}

// runSym fetches the symbol's day-news and summarizes the newest uncached few.
func (w *Worker) runSym(ctx context.Context, sym string) {
	rctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	since := w.dayStart()
	items, bodies, err := w.fetch(rctx, sym, since)

	// No headline for a symbol that just popped usually means the article
	// hasn't hit Benzinga's index yet — chase it while the move is young.
	// Transient fetch errors chase too (the loop doubles as a retry); a 403
	// or 429 means re-fetching can't help, so those don't.
	if w.chaseOn && !errors.Is(err, core.ErrNotEntitled) && !core.IsRateLimited(err) && !w.hasFresh(items) {
		if got, bs := w.chase(rctx, sym, since); len(got) > 0 {
			items, bodies = got, bs
		}
	}
	if len(items) == 0 {
		return
	}

	for _, it := range items { // newest first from both sources
		if !w.budgetOne(sym) {
			return
		}
		if sum, model, ok, _ := w.cfg.Cache.GetSummary(rctx, it.ID); ok {
			w.emit(core.NewsSummaryMsg{ID: it.ID, Summary: sum, Model: model})
			w.unbudgetOne(sym) // cache hits are free — don't count them
			continue
		}
		item, body, _ := w.cfg.News.Get(it.ID)
		if item.ID == "" {
			item, body = it, bodies[it.ID]
		}
		sum, err := w.cfg.Sum.Summarize(rctx, summarize.Article{
			ID: item.ID, Sym: sym, Tickers: item.Tickers, Headline: item.Headline,
			Source: item.Source, URL: item.URL, Body: body, Teaser: item.Teaser, TS: item.TS,
		})
		if err != nil {
			logx.L().Warn("autosum failed", "sym", sym, "id", item.ID, "err", err)
			w.unbudgetOne(sym)
			return // backend problem (auth, quota) — stop the run, don't burn budget
		}
		_ = w.cfg.Cache.SaveSummary(rctx, item.ID, sym, w.cfg.Sum.Model(), sum)
		w.emit(core.NewsSummaryMsg{ID: item.ID, Summary: sum, Model: w.cfg.Sum.Model()})
	}
}

// budgetOne reserves one summary slot for sym; false when budgets are spent.
func (w *Worker) budgetOne(sym string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dayDone >= w.cfg.MaxPerDay || w.symDone[sym] >= w.cfg.MaxPerSym {
		return false
	}
	w.dayDone++
	w.symDone[sym]++
	return true
}

func (w *Worker) unbudgetOne(sym string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Floor at zero: a refund can land after shouldRun's ET-midnight counter
	// reset (the reservation was taken on the OLD day), and a negative counter
	// would grant the new day extra budget.
	if w.dayDone > 0 {
		w.dayDone--
	}
	if w.symDone[sym] > 0 {
		w.symDone[sym]--
	}
}

func (w *Worker) emit(m core.NewsSummaryMsg) {
	select {
	case w.sums <- m:
	default:
	}
}

// dayStart returns "the trading day's news window" start: 16:00 ET of the
// previous calendar day, so after-hours filings that drive today's gap are
// included alongside everything published today.
func (w *Worker) dayStart() int64 {
	now := w.cfg.Now().In(w.loc)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, w.loc)
	return midnight.Add(-8 * time.Hour).UnixMilli()
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
