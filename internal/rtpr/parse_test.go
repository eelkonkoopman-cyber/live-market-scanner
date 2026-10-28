package rtpr

import (
	"strings"
	"testing"
	"time"
)

// realCaptureFSS and realCaptureRBLX are trimmed REAL captures (curl, with a
// live API key, read-only GETs) of RTPR's actual permalink pages — not
// hand-built guesses. That matters: this endpoint isn't documented with a
// versioned JSON schema (following article_url returns rendered HTML,
// confirmed live against every content-negotiation trick — Accept header,
// .json suffix, format=json query param — none changed the response), so a
// fixture built from the docs alone could easily be wrong the way the
// Benzinga created/created_at field was. Only CSS/nav boilerplate is trimmed;
// every tag and attribute below the <main> is verbatim from the response.

const realCaptureFSS = `<!doctype html>
<html lang="en">
<head><title>x</title></head>
<body>
<nav class="nav"><div class="nav-inner"><a class="brand" href="https://rtpr.io/">RTPR</a></div></nav>
<main>
  <article class="card">
    <header class="card-header">
      <div class="meta">
        <span class="meta-source">PR Newswire</span> · <time datetime="2026-07-28T11:40:00.011Z">Jul 28, 2026, 07:40 AM EDT</time>
      </div>
      <h1 class="title">Federal Signal Corporation Declares Dividend of $0.15 per share</h1>
      <div class="tickers"><span class="ticker">FSS</span><span class="exchange">NYSE</span></div>
    </header>
    <div class="card-body">
      <div class="article-body">DES PLAINES, Ill., July 28, 2026 -- Federal Signal Corporation
today declared a quarterly dividend of $0.15 per share, payable
on <a href="https://example.test/x" target="_blank" rel="nofollow noopener">August 15, 2026</a>.

SOURCE: Federal Signal Corporation</div>
      <div class="footer-meta"><span>Article ID: <code>nPn8xyVHwa</code></span></div>
    </div>
  </article>
</main>
</body>
</html>`

// realCaptureRBLX is the actual Roblox securities-lawsuit article — genuine
// entity usage (&amp;, &#x27;) in both the title and body, single ticker.
const realCaptureRBLX = `<!doctype html>
<html lang="en">
<head><title>x</title></head>
<body>
<main>
  <article class="card">
    <header class="card-header">
      <div class="meta">
        <span class="meta-source">ACCESSWIRE</span> · <time datetime="2026-07-28T11:40:00.637Z">Jul 28, 2026, 07:40 AM EDT</time>
      </div>
      <h1 class="title">Securities Lawsuit Alert: Roblox Corporation (RBLX) Investors - Contact Levi &amp; Korsinsky Before August 7, 2026</h1>
      <div class="tickers"><span class="ticker">RBLX</span><span class="exchange">NYSE</span></div>
    </header>
    <div class="card-body">
      <div class="article-body">NEW YORK, NY / ACCESS Newswire / July 28, 2026 / Levi &amp; Korsinsky, LLP
announces that a securities class action lawsuit has been filed on behalf of
investors who purchased or otherwise acquired Roblox Corporation (NYSE:RBLX)
securities. <a href="https://pr.report/o9t3" target="_blank" rel="nofollow noopener">Learn about Roblox Class Action</a>.

Roblox&#x27;s organic growth potential was allegedly misrepresented.

SOURCE: Levi &amp; Korsinsky, LLP</div>
      <div class="footer-meta"><span>Article ID: <code>nACSSWlska</code></span></div>
    </div>
  </article>
</main>
</body>
</html>`

func TestParseArticleRealCaptureSingleTicker(t *testing.T) {
	a, ok := parseArticle([]byte(realCaptureFSS))
	if !ok {
		t.Fatal("real capture rejected")
	}
	if a.Headline != "Federal Signal Corporation Declares Dividend of $0.15 per share" {
		t.Fatalf("headline: %q", a.Headline)
	}
	if a.Source != "PR Newswire" {
		t.Fatalf("source: %q", a.Source)
	}
	if len(a.Tickers) != 1 || a.Tickers[0] != "FSS" {
		t.Fatalf("tickers: %v", a.Tickers)
	}
	wantTS := time.Date(2026, 7, 28, 11, 40, 0, 11_000_000, time.UTC).UnixMilli()
	if a.TS != wantTS {
		t.Fatalf("ts: want %d got %d", wantTS, a.TS)
	}
	if !strings.Contains(a.BodyHTML, "Federal Signal Corporation") {
		t.Fatalf("body missing expected text: %q", a.BodyHTML)
	}
	// The body's <a> tag must survive as raw HTML, matching how every other
	// news source's body is stored (summarizer strips markup itself later).
	if !strings.Contains(a.BodyHTML, `<a href="https://example.test/x"`) {
		t.Fatalf("body must keep nested markup as raw HTML: %q", a.BodyHTML)
	}
}

func TestParseArticleRealCaptureEntityDecoding(t *testing.T) {
	a, ok := parseArticle([]byte(realCaptureRBLX))
	if !ok {
		t.Fatal("real capture rejected")
	}
	// x/net/html decodes entities into text nodes automatically; confirm the
	// headline comes through clean, not literally "&amp;".
	if a.Headline != "Securities Lawsuit Alert: Roblox Corporation (RBLX) Investors - Contact Levi & Korsinsky Before August 7, 2026" {
		t.Fatalf("entities not decoded in headline: %q", a.Headline)
	}
	if a.Source != "ACCESSWIRE" {
		t.Fatalf("source: %q", a.Source)
	}
	// Body stays valid raw HTML (matching every other source in this app —
	// see benzinga.go/bzstream.go: "raw HTML; the summarizer strips it") —
	// re-serializing through x/net/html re-escapes the apostrophe as a
	// (functionally identical) numeric entity rather than a bare "'", which
	// is correct: a literal "&" or "'" in text content isn't valid HTML to
	// author, only to render. Check content, not the exact entity spelling.
	if !strings.Contains(a.BodyHTML, "Roblox") || !strings.Contains(a.BodyHTML, "organic growth potential") {
		t.Fatalf("body missing expected text: %q", a.BodyHTML)
	}
	if !strings.Contains(a.BodyHTML, "&#39;") && !strings.Contains(a.BodyHTML, "&#x27;") {
		t.Fatalf("expected the apostrophe preserved as a valid HTML entity, got: %q", a.BodyHTML)
	}
}

// TestParseArticleMultiTicker is NOT from a live capture — none of the
// samples pulled during this integration happened to carry more than one
// ticker. It exercises the documented structure (repeated
// <span class="ticker">/<span class="exchange"> pairs inside .tickers), which
// the single-ticker real captures above already prove the parser reads
// correctly for the first pair; this only confirms it doesn't stop after one.
func TestParseArticleMultiTicker(t *testing.T) {
	const html = `<html><body><main><article>
	  <div class="meta"><time datetime="2026-07-28T12:00:00Z"></time></div>
	  <h1 class="title">Two tickers</h1>
	  <div class="tickers">
	    <span class="ticker">AAPL</span><span class="exchange">NASDAQ</span>
	    <span class="ticker">MSFT</span><span class="exchange">NASDAQ</span>
	  </div>
	  <div class="article-body">body</div>
	</article></main></body></html>`
	a, ok := parseArticle([]byte(html))
	if !ok {
		t.Fatal("rejected")
	}
	if len(a.Tickers) != 2 || a.Tickers[0] != "AAPL" || a.Tickers[1] != "MSFT" {
		t.Fatalf("tickers: %v", a.Tickers)
	}
}

func TestParseArticleRejectsNonArticlePage(t *testing.T) {
	// An expired hot-window link or a 404 body — no <time> tag at all.
	if _, ok := parseArticle([]byte(`<html><body><h1>Not Found</h1></body></html>`)); ok {
		t.Fatal("a page with no <time> tag must not parse as an article")
	}
}

func TestIDFromURL(t *testing.T) {
	cases := map[string]string{
		"https://rtpr.io/a/nACSSWlska?exp=1777497152&sig=cd34": "nACSSWlska",
		"https://rtpr.io/a/nPn8xyVHwa":                         "nPn8xyVHwa",
	}
	for in, want := range cases {
		if got := idFromURL(in); got != want {
			t.Fatalf("idFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}
