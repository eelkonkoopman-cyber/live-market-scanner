package alpaca

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

var etLoc = func() *time.Location {
	l, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return l
}()

// ---- wire types ----------------------------------------------------------

type bar struct {
	T  string  `json:"t"` // RFC3339 bar start
	O  float64 `json:"o"`
	H  float64 `json:"h"`
	L  float64 `json:"l"`
	C  float64 `json:"c"`
	V  float64 `json:"v"`
	N  int64   `json:"n"`
	VW float64 `json:"vw"`
}

func (b bar) ms() int64 { return parseTS(b.T) }

type latestTrade struct {
	T string  `json:"t"`
	P float64 `json:"p"`
	S float64 `json:"s"`
}

type snapshot struct {
	LatestTrade  *latestTrade `json:"latestTrade"`
	MinuteBar    *bar         `json:"minuteBar"`
	DailyBar     *bar         `json:"dailyBar"`
	PrevDailyBar *bar         `json:"prevDailyBar"`
}

type asset struct {
	Symbol   string `json:"symbol"`
	Name     string `json:"name"`
	Exchange string `json:"exchange"`
	Class    string `json:"class"`
	Status   string `json:"status"`
	Tradable bool   `json:"tradable"`
}

func parseTS(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

func etDate(ms int64) string { return time.UnixMilli(ms).In(etLoc).Format("2006-01-02") }

// ---- assets / reference ---------------------------------------------------

// listedExchanges are the national exchanges; anything else (OTC) is typed
// "OTC" so the universe filter drops it, as Massive's type filter does.
var listedExchanges = map[string]bool{"NYSE": true, "NASDAQ": true, "ARCA": true, "AMEX": true, "BATS": true, "NYSEARCA": true}

var (
	reETF     = regexp.MustCompile(`(?i)\b(ETF|ETN)\b|\bProShares\b|\biShares\b|\bDirexion\b|\bSPDR\b|\bInvesco QQQ\b`)
	reWarrant = regexp.MustCompile(`(?i)\bwarrants?\b`)
	reUnit    = regexp.MustCompile(`(?i)\bunits?\b`)
	reRight   = regexp.MustCompile(`(?i)\brights?\b`)
	rePref    = regexp.MustCompile(`(?i)\bpreferred\b|\bdepositary shares\b.*%|%.*\bdepositary shares\b|\bnotes? due\b|\bsenior notes\b`)
	reADR     = regexp.MustCompile(`(?i)\bamerican depositary\b|\bADRs?\b|\bADSs?\b`)
)

// assetType maps an Alpaca asset to the Massive security type codes the
// universe filter understands (CS / ADRC / ETF allowed). Alpaca's asset list
// carries no security type, so this is a name heuristic - see the doc.
func assetType(a asset) string {
	if !listedExchanges[strings.ToUpper(a.Exchange)] {
		return "OTC"
	}
	n := a.Name
	switch {
	case reWarrant.MatchString(n):
		return "WARRANT"
	case reRight.MatchString(n) && !reETF.MatchString(n):
		return "RIGHT"
	case reUnit.MatchString(n) && !reETF.MatchString(n):
		return "UNIT"
	case rePref.MatchString(n):
		return "PFD"
	case reETF.MatchString(n):
		return "ETF"
	case reADR.MatchString(n):
		return "ADRC"
	}
	return "CS"
}

// listAssets returns the active US-equity assets, cached for 6h.
func (c *Client) listAssets(ctx context.Context) ([]asset, error) {
	c.assetsMu.Lock()
	defer c.assetsMu.Unlock()
	if c.assets != nil && c.now().Sub(c.assetsAt) < 6*time.Hour {
		return c.assets, nil
	}
	var as []asset
	if err := c.getJSON(ctx, c.tradingBase+"/v2/assets?status=active&asset_class=us_equity", &as); err != nil {
		return nil, err
	}
	sort.Slice(as, func(i, j int) bool { return as[i].Symbol < as[j].Symbol })
	c.assets, c.assetsAt = as, c.now()
	return as, nil
}

// Reference lists active tickers typed like Massive's reference endpoint.
func (c *Client) Reference(ctx context.Context) ([]core.Ticker, error) {
	as, err := c.listAssets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]core.Ticker, 0, len(as))
	for _, a := range as {
		if a.Symbol == "" {
			continue
		}
		out = append(out, core.Ticker{Symbol: a.Symbol, Name: a.Name, Type: assetType(a), Active: a.Status == "active"})
	}
	return out, nil
}

// marketSymbols are the symbols snapshots / grouped bars are fetched for:
// listed (non-OTC) active assets.
func (c *Client) marketSymbols(ctx context.Context) ([]string, error) {
	as, err := c.listAssets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(as))
	for _, a := range as {
		if a.Symbol != "" && listedExchanges[strings.ToUpper(a.Exchange)] {
			out = append(out, a.Symbol)
		}
	}
	return out, nil
}

func chunks(s []string, n int) [][]string {
	var out [][]string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	if len(s) > 0 {
		out = append(out, s)
	}
	return out
}

// parallel runs fn over the chunks with bounded concurrency; first error wins.
func parallel(ctx context.Context, cs [][]string, fn func(context.Context, []string) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	for _, ch := range cs {
		wg.Add(1)
		sem <- struct{}{}
		go func(ch []string) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := fn(ctx, ch); err != nil {
				once.Do(func() { first = err; cancel() })
			}
		}(ch)
	}
	wg.Wait()
	return first
}

// ---- snapshots ------------------------------------------------------------

func (c *Client) snapshots(ctx context.Context, syms []string) (map[string]snapshot, error) {
	out := make(map[string]snapshot, len(syms))
	var mu sync.Mutex
	err := parallel(ctx, chunks(syms, c.snapChunk), func(ctx context.Context, ch []string) error {
		u := c.dataBase + "/v2/stocks/snapshots?feed=" + c.feed + "&symbols=" + url.QueryEscape(strings.Join(ch, ","))
		var m map[string]snapshot
		if err := c.getJSON(ctx, u, &m); err != nil {
			return err
		}
		mu.Lock()
		for k, v := range m {
			out[k] = v
		}
		mu.Unlock()
		return nil
	})
	return out, err
}

// Snapshot is the whole-market seed: multi-symbol snapshots over the listed
// asset list (Alpaca has no single whole-market snapshot call).
func (c *Client) Snapshot(ctx context.Context) ([]core.Seed, error) {
	syms, err := c.marketSymbols(ctx)
	if err != nil {
		return nil, err
	}
	m, err := c.snapshots(ctx, syms)
	if err != nil {
		return nil, err
	}
	today := c.now().In(etLoc).Format("2006-01-02")
	out := make([]core.Seed, 0, len(m))
	for sym, s := range m {
		if sd, ok := s.toSeed(sym, today, c.now()); ok {
			out = append(out, sd)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sym < out[j].Sym })
	c.sagg.Seed(out)
	return out, nil
}

// toSeed mirrors internal/feed's snapTicker.toSeed. Alpaca's dailyBar is the
// LATEST daily bar: before today's first trade it is still yesterday's, so a
// dailyBar dated before today is treated as the previous close and the day
// block as empty (Massive clears "day" overnight instead).
func (s snapshot) toSeed(sym, today string, now time.Time) (core.Seed, bool) {
	var day, prev bar
	if s.DailyBar != nil {
		if etDate(s.DailyBar.ms()) == today {
			day = *s.DailyBar
			if s.PrevDailyBar != nil {
				prev = *s.PrevDailyBar
			}
		} else {
			prev = *s.DailyBar
		}
	} else if s.PrevDailyBar != nil {
		prev = *s.PrevDailyBar
	}
	var mb bar
	if s.MinuteBar != nil && etDate(s.MinuteBar.ms()) == today {
		mb = *s.MinuteBar
	}
	var ltP float64
	var ltMs int64
	if s.LatestTrade != nil {
		ltP, ltMs = s.LatestTrade.P, parseTS(s.LatestTrade.T)
	}
	last := firstNonZero(day.C, mb.C, prev.C)
	if ltMs != 0 && etDate(ltMs) == today && ltP > 0 {
		last = ltP
	}
	if last == 0 {
		return core.Seed{}, false
	}
	vol := int64(day.V)
	vw := firstNonZero(day.VW, mb.VW)
	endMs := ltMs
	if m := mb.ms(); m > endMs {
		endMs = m
	}
	if endMs == 0 {
		endMs = now.UnixMilli()
	}
	return core.Seed{
		Agg: core.Agg{
			Sym:     sym,
			Open:    firstNonZero(day.O, mb.O),
			High:    firstNonZero(day.H, mb.H, last),
			Low:     firstNonZero(day.L, mb.L, last),
			Close:   last,
			VWAP:    vw,
			Vol:     vol,
			AccVol:  vol,
			DayOpen: day.O,
			DayVWAP: vw,
			StartMs: endMs - 1000,
			EndMs:   endMs,
		},
		PrevClose: prev.C,
	}, true
}

// PrevClose returns the previous session's close for one symbol.
func (c *Client) PrevClose(ctx context.Context, sym string) (float64, error) {
	var s snapshot
	u := c.dataBase + "/v2/stocks/" + url.PathEscape(sym) + "/snapshot?feed=" + c.feed
	if err := c.getJSON(ctx, u, &s); err != nil {
		return 0, err
	}
	sd, ok := s.toSeed(sym, c.now().In(etLoc).Format("2006-01-02"), c.now())
	if !ok {
		return 0, nil
	}
	return sd.PrevClose, nil
}

// ---- bars -----------------------------------------------------------------

// dayBounds turns inclusive ET dates into an RFC3339 [start, end) window,
// with end clamped to now (Alpaca rejects / delays future ends).
func (c *Client) dayBounds(from, to string) (string, string, error) {
	f, err := time.ParseInLocation("2006-01-02", from, etLoc)
	if err != nil {
		return "", "", err
	}
	t, err := time.ParseInLocation("2006-01-02", to, etLoc)
	if err != nil {
		return "", "", err
	}
	end := t.AddDate(0, 0, 1)
	if now := c.now(); end.After(now) {
		end = now
	}
	return f.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339), nil
}

type singleBarsResp struct {
	Bars          []bar   `json:"bars"`
	NextPageToken *string `json:"next_page_token"`
}

type multiBarsResp struct {
	Bars          map[string][]bar `json:"bars"`
	NextPageToken *string          `json:"next_page_token"`
}

const maxPages = 200

// symbolBars pages through one symbol's bars.
func (c *Client) symbolBars(ctx context.Context, sym, timeframe, from, to string) ([]bar, error) {
	start, end, err := c.dayBounds(from, to)
	if err != nil {
		return nil, err
	}
	base := fmt.Sprintf("%s/v2/stocks/%s/bars?timeframe=%s&start=%s&end=%s&adjustment=split&feed=%s&limit=10000&sort=asc",
		c.dataBase, url.PathEscape(sym), timeframe, url.QueryEscape(start), url.QueryEscape(end), c.feed)
	var out []bar
	tok := ""
	for page := 0; page < maxPages; page++ {
		u := base
		if tok != "" {
			u += "&page_token=" + url.QueryEscape(tok)
		}
		var r singleBarsResp
		if err := c.getJSON(ctx, u, &r); err != nil {
			return nil, err
		}
		out = append(out, r.Bars...)
		if r.NextPageToken == nil || *r.NextPageToken == "" {
			return out, nil
		}
		tok = *r.NextPageToken
	}
	return out, fmt.Errorf("alpaca bars %s: more than %d pages", sym, maxPages)
}

// multiBars pages through a multi-symbol bars request.
func (c *Client) multiBars(ctx context.Context, syms []string, timeframe, from, to string) (map[string][]bar, error) {
	start, end, err := c.dayBounds(from, to)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]bar)
	var mu sync.Mutex
	err = parallel(ctx, chunks(syms, c.barsChunk), func(ctx context.Context, ch []string) error {
		base := fmt.Sprintf("%s/v2/stocks/bars?symbols=%s&timeframe=%s&start=%s&end=%s&adjustment=split&feed=%s&limit=10000&sort=asc",
			c.dataBase, url.QueryEscape(strings.Join(ch, ",")), timeframe, url.QueryEscape(start), url.QueryEscape(end), c.feed)
		tok := ""
		for page := 0; page < maxPages; page++ {
			u := base
			if tok != "" {
				u += "&page_token=" + url.QueryEscape(tok)
			}
			var r multiBarsResp
			if err := c.getJSON(ctx, u, &r); err != nil {
				return err
			}
			mu.Lock()
			for s, bs := range r.Bars {
				out[s] = append(out[s], bs...)
			}
			mu.Unlock()
			if r.NextPageToken == nil || *r.NextPageToken == "" {
				return nil
			}
			tok = *r.NextPageToken
		}
		return fmt.Errorf("alpaca multi-bars: more than %d pages", maxPages)
	})
	return out, err
}

func toCoreBars(bs []bar) []core.Bar {
	out := make([]core.Bar, 0, len(bs))
	for _, b := range bs {
		out = append(out, core.Bar{T: b.ms(), O: b.O, H: b.H, L: b.L, C: b.C, V: int64(b.V), VW: b.VW})
	}
	return out
}

func (c *Client) minuteRange(ctx context.Context, sym, from, to string) ([]core.Bar, error) {
	bs, err := c.symbolBars(ctx, sym, "1Min", from, to)
	if err != nil {
		return nil, err
	}
	return toCoreBars(bs), nil
}

// Intraday: today's 1-minute bars (extended hours included). Today only - see
// internal/feed.Intraday for why (prefill's date blindness).
func (c *Client) Intraday(ctx context.Context, sym string) ([]core.Bar, error) {
	d := c.now().In(etLoc).Format("2006-01-02")
	return c.minuteRange(ctx, sym, d, d)
}

// IntradayWithPrevDay: previous weekday's and today's 1-minute bars.
func (c *Client) IntradayWithPrevDay(ctx context.Context, sym string) ([]core.Bar, error) {
	today := c.now().In(etLoc)
	prev := today.AddDate(0, 0, -1)
	for wd := prev.Weekday(); wd == time.Saturday || wd == time.Sunday; wd = prev.Weekday() {
		prev = prev.AddDate(0, 0, -1)
	}
	return c.minuteRange(ctx, sym, prev.Format("2006-01-02"), today.Format("2006-01-02"))
}

// IntradayOn: one ET date's 1-minute bars.
func (c *Client) IntradayOn(ctx context.Context, sym, date string) ([]core.Bar, error) {
	return c.minuteRange(ctx, sym, date, date)
}

// IntradayBetween: ET dates from..to inclusive (paged, so no 50k cap).
func (c *Client) IntradayBetween(ctx context.Context, sym, from, to string) ([]core.Bar, error) {
	return c.minuteRange(ctx, sym, from, to)
}

func toDaily(bs []bar) []core.DailyBar {
	out := make([]core.DailyBar, 0, len(bs))
	for _, b := range bs {
		if b.C <= 0 {
			continue
		}
		out = append(out, core.DailyBar{Date: etDate(b.ms()), O: b.O, H: b.H, L: b.L, C: b.C, V: int64(b.V)})
	}
	return out
}

// DailyRange: one symbol's split-adjusted daily bars, ascending.
func (c *Client) DailyRange(ctx context.Context, sym, from, to string) ([]core.DailyBar, error) {
	bs, err := c.symbolBars(ctx, sym, "1Day", from, to)
	if err != nil {
		return nil, err
	}
	return toDaily(bs), nil
}

// GroupedDaily rebuilds Massive's grouped-daily call: every listed asset's
// daily bar for one date, via chunked multi-symbol 1Day bars. A holiday comes
// back empty, as with Massive. Only CURRENTLY active assets are covered (the
// asset list has no history), so names delisted since `date` are missing.
func (c *Client) GroupedDaily(ctx context.Context, date string) (map[string]core.DailyBar, error) {
	syms, err := c.marketSymbols(ctx)
	if err != nil {
		return nil, err
	}
	m, err := c.multiBars(ctx, syms, "1Day", date, date)
	if err != nil {
		return nil, err
	}
	out := make(map[string]core.DailyBar, len(m))
	for sym, bs := range m {
		for _, d := range toDaily(bs) {
			if d.Date == date {
				out[sym] = d
			}
		}
	}
	return out, nil
}

// ---- news (Alpaca's news API is Benzinga) --------------------------------

type newsArticle struct {
	ID        int64    `json:"id"`
	Headline  string   `json:"headline"`
	Summary   string   `json:"summary"`
	Content   string   `json:"content"`
	URL       string   `json:"url"`
	Source    string   `json:"source"`
	CreatedAt string   `json:"created_at"`
	Symbols   []string `json:"symbols"`
}

type newsResp struct {
	News          []newsArticle `json:"news"`
	NextPageToken *string       `json:"next_page_token"`
}

const newsPages = 10

func (c *Client) news(ctx context.Context, sym string, sinceMs int64, content bool) ([]core.NewsItem, map[string]string, error) {
	base := c.dataBase + "/v1beta1/news?sort=desc&limit=50"
	if content {
		base += "&include_content=true"
	}
	if sym != "" {
		base += "&symbols=" + url.QueryEscape(sym)
	}
	if sinceMs > 0 {
		base += "&start=" + url.QueryEscape(time.UnixMilli(sinceMs).UTC().Format(time.RFC3339))
	}
	var items []core.NewsItem
	bodies := map[string]string{}
	tok := ""
	for page := 0; page < newsPages; page++ {
		u := base
		if tok != "" {
			u += "&page_token=" + url.QueryEscape(tok)
		}
		var r newsResp
		if err := c.getJSON(ctx, u, &r); err != nil {
			return nil, nil, err
		}
		for _, a := range r.News {
			ms := parseTS(a.CreatedAt)
			if sinceMs > 0 && ms != 0 && ms < sinceMs {
				continue
			}
			// "bz"+id: the same ID internal/feed and bzstream give a Benzinga
			// article, so the news store dedupes across sources.
			id := "bz" + strconv.FormatInt(a.ID, 10)
			src := "Benzinga"
			if a.Source != "" && !strings.EqualFold(a.Source, "benzinga") {
				src = a.Source
			}
			items = append(items, core.NewsItem{
				ID: id, Tickers: a.Symbols, Headline: a.Headline, Source: src,
				URL: a.URL, TS: ms, Teaser: a.Summary, HasBody: a.Content != "",
			})
			if a.Content != "" {
				bodies[id] = a.Content
			}
		}
		if r.NextPageToken == nil || *r.NextPageToken == "" {
			break
		}
		tok = *r.NextPageToken
	}
	return items, bodies, nil
}

// NewsSince: headline-only poll (no bodies).
func (c *Client) NewsSince(ctx context.Context, sinceMs int64) ([]core.NewsItem, error) {
	items, _, err := c.news(ctx, "", sinceMs, false)
	return items, err
}

// BenzingaNews: Alpaca's news feed IS Benzinga, with full content.
func (c *Client) BenzingaNews(ctx context.Context, sinceMs int64) ([]core.NewsItem, map[string]string, error) {
	return c.news(ctx, "", sinceMs, true)
}

// BenzingaNewsFor: one symbol's articles since sinceMs.
func (c *Client) BenzingaNewsFor(ctx context.Context, sym string, sinceMs int64) ([]core.NewsItem, map[string]string, error) {
	return c.news(ctx, sym, sinceMs, true)
}

// ---- not available on Alpaca ---------------------------------------------

// Fundamentals: Alpaca has no SEC financials endpoint.
func (c *Client) Fundamentals(ctx context.Context, sym string) (core.Fundamentals, error) {
	return core.Fundamentals{}, fmt.Errorf("%w: Alpaca has no fundamentals data", core.ErrNotEntitled)
}

// Floats: Alpaca has no shares-outstanding data. Returning nothing leaves the
// floats already stored in the database untouched (schedule skips empty).
func (c *Client) Floats(ctx context.Context, syms []string) ([]core.FloatRef, error) {
	return nil, nil
}

func firstNonZero(vals ...float64) float64 {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}
