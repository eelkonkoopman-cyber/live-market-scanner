package summarize

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// fetchArticle downloads and de-HTMLs the article or filing at rawURL.
// SEC/EDGAR hosts get the fair-access User-Agent the SEC requires for
// automated clients; everything else gets a browser-ish UA (best-effort —
// paywalled/bot-blocked sites simply fail and the caller falls back).
func fetchArticle(ctx context.Context, rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("bad article url")
	}

	fctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(fctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	host := strings.ToLower(u.Hostname())
	if strings.HasSuffix(host, "sec.gov") {
		// SEC fair-access policy: "Company Name AdminContact@domain" UA required,
		// else EDGAR returns 403 "Undeclared Automated Tool". Max 10 req/s.
		req.Header.Set("User-Agent", "LiveMarketScanner eelkonkoopman@outlook.com")
	} else {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) MarketScanner/1.0")
	}

	resp, err := fetchClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: HTTP %d", host, resp.StatusCode)
	}
	// Filings can be huge; 4 MiB of HTML is more than enough source material.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	text := stripHTML(string(body))
	if len(strings.TrimSpace(text)) < 80 {
		return "", fmt.Errorf("page yielded no usable text")
	}
	return text, nil
}

var fetchClient = &http.Client{Timeout: 25 * time.Second}

var (
	reScript = regexp.MustCompile(`(?is)<(script|style|noscript|head|nav|footer|svg)[^>]*>.*?</\s*(script|style|noscript|head|nav|footer|svg)\s*>`)
	reTag    = regexp.MustCompile(`(?s)<[^>]*>`)
	reBlank  = regexp.MustCompile(`\n{3,}`)
	reSpaces = regexp.MustCompile(`[ \t\r\f]+`)
)

// stripHTML reduces an HTML document (or fragment) to readable plain text.
// Regex-based on purpose: filings and news bodies are simple documents and a
// dependency-free 95% solution beats a DOM parser here.
func stripHTML(s string) string {
	if !strings.Contains(s, "<") {
		return strings.TrimSpace(s)
	}
	s = reScript.ReplaceAllString(s, " ")
	// Preserve some block structure as newlines before dropping tags.
	for _, t := range []string{"</p>", "</div>", "</tr>", "</li>", "<br>", "<br/>", "<br />", "</h1>", "</h2>", "</h3>", "</table>"} {
		s = strings.ReplaceAll(s, t, t+"\n")
	}
	s = reTag.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, " ", " ") // &nbsp; → regular space
	s = reSpaces.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			out = append(out, ln)
		}
	}
	return strings.TrimSpace(reBlank.ReplaceAllString(strings.Join(out, "\n"), "\n\n"))
}
