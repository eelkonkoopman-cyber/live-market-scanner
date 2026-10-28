// article.go parses RTPR's article permalink page. RTPR's "Article Feed API"
// is body-free by design: the REST feed and the alerts WebSocket both return
// only a ticker/timestamp/URL ref, and following that URL returns a
// server-rendered HTML page meant to be BOTH human-shareable and machine-
// fetched (confirmed live: neither an Accept header, a .json suffix, nor a
// format=json query param produces a JSON alternative). So the "API response"
// for full content is this HTML page, and parsing it is the integration.
package rtpr

import (
	"bytes"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// article is what parseArticle extracts from one permalink page.
type article struct {
	Headline string
	Source   string // wire name, e.g. "ACCESSWIRE", "PR Newswire", "Business Wire"
	BodyHTML string // raw inner HTML of .article-body — kept raw, like every
	// other news source in this app; the summarizer strips markup itself.
	Tickers []string
	TS      int64 // ms, from <time datetime="...">
}

// parseArticle extracts the fields above from one permalink page's HTML. It
// returns ok=false (not an error) for a page that doesn't have the expected
// structure — e.g. a "not found"/expired page — since that's a normal,
// expected outcome (hot-window links expire) rather than a bug to log loudly.
func parseArticle(body []byte) (article, bool) {
	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return article{}, false
	}

	var a article
	timeNode := findFirst(root, func(n *html.Node) bool { return n.Type == html.ElementNode && n.Data == "time" })
	if timeNode == nil {
		return article{}, false // no <time> tag -> not a real article page
	}
	if dt, ok := attr(timeNode, "datetime"); ok {
		a.TS = parseArticleTime(dt)
	}

	if h1 := findFirst(root, hasClass("title")); h1 != nil {
		a.Headline = strings.TrimSpace(textOf(h1))
	}
	if src := findFirst(root, hasClass("meta-source")); src != nil {
		a.Source = strings.TrimSpace(textOf(src))
	}
	if tickers := findFirst(root, hasClass("tickers")); tickers != nil {
		// Each ticker is its own <span class="ticker">SYM</span>, optionally
		// followed by a sibling <span class="exchange">EXCH</span> — we only
		// need the symbol, so siblings are skipped rather than paired up.
		for n := tickers.FirstChild; n != nil; n = n.NextSibling {
			if hasClass("ticker")(n) {
				if sym := strings.TrimSpace(textOf(n)); sym != "" {
					a.Tickers = append(a.Tickers, sym)
				}
			}
		}
	}
	if bodyDiv := findFirst(root, hasClass("article-body")); bodyDiv != nil {
		a.BodyHTML = strings.TrimSpace(innerHTML(bodyDiv))
	}

	if a.Headline == "" {
		return article{}, false
	}
	return a, true
}

// toNewsItem converts a parsed article + its permalink-derived id into the
// shared core.NewsItem shape every news source in this app funnels through.
func (a article) toNewsItem(id, url string) core.NewsItem {
	return core.NewsItem{
		ID:       id,
		Tickers:  a.Tickers,
		Headline: a.Headline,
		Source:   a.Source,
		URL:      url,
		TS:       a.TS,
		HasBody:  a.BodyHTML != "",
	}
}

// --- tiny DOM helpers (golang.org/x/net/html gives a tree, not a query API) ---

func findFirst(n *html.Node, match func(*html.Node) bool) *html.Node {
	if match(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findFirst(c, match); found != nil {
			return found
		}
	}
	return nil
}

func hasClass(class string) func(*html.Node) bool {
	return func(n *html.Node) bool {
		if n.Type != html.ElementNode {
			return false
		}
		c, ok := attr(n, "class")
		if !ok {
			return false
		}
		for _, part := range strings.Fields(c) {
			if part == class {
				return true
			}
		}
		return false
	}
}

func attr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

// textOf concatenates all text-node descendants (x/net/html decodes entities
// into text nodes automatically, so no separate unescape step is needed).
func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// innerHTML re-serializes a node's CHILDREN (not the node itself), preserving
// nested markup (links, tables) as raw HTML — matching how every other news
// source's body is stored in this app (raw HTML; the summarizer strips it).
func innerHTML(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		_ = html.Render(&b, c)
	}
	return b.String()
}

// parseArticleTime parses the <time datetime="..."> attribute, observed live
// as RFC3339 with milliseconds ("2026-07-28T11:40:00.637Z"). A few extra
// layouts are tolerated defensively — this endpoint isn't documented with a
// versioned schema, only observed, so treat the exact format as unstable.
func parseArticleTime(s string) int64 {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixMilli()
		}
	}
	return time.Now().UnixMilli()
}

// idFromURL extracts the permalink's path segment as the article id, e.g.
// "nACSSWlska" from "https://rtpr.io/a/nACSSWlska?exp=...&sig=...". This is
// stable and available without parsing the page at all.
func idFromURL(rawURL string) string {
	u := rawURL
	if i := strings.IndexByte(u, '?'); i >= 0 {
		u = u[:i]
	}
	if i := strings.LastIndexByte(u, '/'); i >= 0 {
		u = u[i+1:]
	}
	return u
}
