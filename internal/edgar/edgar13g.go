// Package edgar watches SEC EDGAR's real-time "current filings" feed.
//
// It hosts two watchers sharing one fair-access HTTP client and one daily
// CIK→ticker map:
//   - Watcher (this file + filing.go): Schedule 13G / 13G-A stake
//     disclosures, with the ownership percentage parsed from the filing's
//     own structured primary_doc.xml.
//   - AllWatcher (allfilings.go): every other filing type, gated to symbols
//     the day's scans have actually surfaced.
//
// The 13G watcher targets the disclosure an investor files when a
// stake in a company crosses 5% ownership. Unlike almost every other source
// in this app, these are filed by the ACQUIRING investor, not the company, so
// there is frequently no accompanying press release at all — Benzinga, RTPR,
// and every wire service can simply never see one. Motivation (user
// observation, 2026-07-28): stocks moved in after-hours on 13G filings with
// no corresponding headline anywhere in the news pipeline.
//
// Verified live before building this (read-only GETs against the real EDGAR
// endpoints, with the fair-access User-Agent SEC requires):
//   - The "current events" feed reflects a new filing within about a minute
//     of acceptance and is filterable server-side by form type
//     (type=SC+13G, type=SC 13G/A) — a low-volume, precisely-scoped feed, so
//     polling it (rather than the unfiltered firehose) can't be buried by
//     unrelated filing types during a busy day.
//   - Every filing produces one Atom entry per ROLE sharing the same
//     accession number — at minimum a "(Subject)" entry (the affected
//     issuer) and one or more filer entries ("(Reporting)"/"(Filed by)").
//     Only the Subject entry is kept.
//   - Since 2023 every Schedule 13G/13G-A ships a structured primary_doc.xml
//     (schema http://www.sec.gov/edgar/schedule13g) carrying the issuer,
//     the filer, and the exact ownership percentage directly — no HTML
//     scraping or PDF text extraction needed. This app is real-time-only
//     (it never looks at historical pre-2023 filings), so the older
//     free-text filing format is out of scope.
//
// This is the only news-shaped source in the app that needs no API key or
// token: EDGAR is free and public, gated only by the fair-access User-Agent
// policy (internal/summarize/fetch.go already uses the same one for reading
// filing pages linked from other sources).
package edgar

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

const (
	fairAccessUA = "LiveMarketScanner eelkonkoopman@outlook.com" // SEC-required identifying UA; max 10 req/s

	defaultFeedBase     = "https://www.sec.gov/cgi-bin/browse-edgar"
	defaultTickersURL   = "https://www.sec.gov/files/company_tickers.json"
	defaultArchivesBase = "https://www.sec.gov/Archives/edgar/data"

	// 13G is an infrequent, non-time-critical catalyst (unlike breaking
	// news) — a minute of poll latency on top of EDGAR's own ~1-minute
	// processing lag is immaterial, and keeps requests trivially inside
	// SEC's 10 req/s fair-access cap.
	defaultPollInterval = time.Minute
	tickerRefreshEvery  = 24 * time.Hour
)

// formTypes are the two current-feed queries polled every tick. Filtering
// server-side (rather than scanning the unfiltered firehose) means a busy
// filing day for OTHER form types can never bury or delay 13G detection.
var formTypes = []string{"SC 13G", "SC 13G/A"}

// Batch mirrors every other source's shape in this app: articles plus their
// bodies, ready for news.Poller.AddExternal.
type Batch struct {
	Items  []core.NewsItem
	Bodies map[string]string
}

type Watcher struct {
	http *http.Client
	out  chan Batch

	tickers *tickerCache

	// seen bounds re-processing of accession numbers already forwarded.
	// baseline suppresses the first poll's results (whatever is already on
	// the feed at startup) so a restart doesn't replay old filings as new.
	seen      map[string]bool
	seenOrder []string
	baseline  bool

	pollInterval time.Duration
	// Overridable for tests; all three point at the real SEC hosts by
	// default. Fields rather than package consts so a test never has to
	// mutate shared global state.
	feedBase      string
	archivesBase  string
	tickerListURL string

	// health optionally reports the shared current-events feed's health under
	// the name "edgar" for the status bar — BOTH this watcher and AllWatcher
	// (which holds a pointer back to this Watcher and reads this same field
	// directly, being in the same package) report under it, since they poll
	// the same underlying feed. nil is a no-op, guarded at every call site.
	// edgar.New takes no config today, so this is set post-construction via
	// SetHealth rather than a constructor option.
	health core.HealthReporter
}

// SetHealth wires an optional health reporter for the shared current-events
// feed. Least invasive: New() takes no Config struct today, so a setter
// avoids adding one just for this.
func (w *Watcher) SetHealth(h core.HealthReporter) { w.health = h }

func New() *Watcher {
	return &Watcher{
		http:          &http.Client{Timeout: 20 * time.Second},
		out:           make(chan Batch, 16),
		tickers:       newTickerCache(),
		seen:          make(map[string]bool),
		baseline:      true,
		pollInterval:  defaultPollInterval,
		feedBase:      defaultFeedBase,
		archivesBase:  defaultArchivesBase,
		tickerListURL: defaultTickersURL,
	}
}

func (w *Watcher) Out() <-chan Batch { return w.out }

func (w *Watcher) Run(ctx context.Context) error {
	tk := time.NewTicker(w.pollInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tk.C:
			w.poll(ctx)
		}
	}
}

func (w *Watcher) poll(ctx context.Context) {
	// local guards within this single pass: the two form-type queries are
	// expected to be mutually exclusive in real EDGAR data (a filing has
	// exactly one form type), but nothing enforces that server-side, so an
	// accession appearing under both must still only be built and emitted
	// once per poll — markSeen alone isn't enough since it only runs after
	// the whole fresh list is assembled.
	local := map[string]bool{}
	var fresh []subjectEntry
	var pollErr error
	for _, form := range formTypes {
		entries, err := w.fetchCurrentFeed(ctx, form)
		if err != nil {
			logx.L().Warn("edgar13g: current-feed poll failed", "form", form, "err", err)
			if pollErr == nil {
				pollErr = err
			}
			continue
		}
		for _, e := range entries {
			if w.seen[e.Accession] || local[e.Accession] {
				continue
			}
			local[e.Accession] = true
			fresh = append(fresh, e)
		}
	}
	// One health call per poll tick (not per form type): a source that's
	// reachable again by the second query in the same tick is, in fact,
	// reachable — reporting per-query would otherwise flap ok/failing/ok
	// within a single second on the status bar for no useful reason.
	if w.health != nil {
		if pollErr != nil {
			w.health.SourceFail("edgar", pollErr.Error())
		} else {
			w.health.SourceOK("edgar")
		}
	}
	for _, e := range fresh {
		w.markSeen(e.Accession)
	}
	if w.baseline {
		w.baseline = false
		return // suppress whatever was already on the feed at startup
	}
	if len(fresh) == 0 {
		return
	}

	items := make([]core.NewsItem, 0, len(fresh))
	bodies := make(map[string]string, len(fresh))
	for _, e := range fresh {
		it, body, ok := w.buildItem(ctx, e)
		if !ok {
			continue
		}
		items = append(items, it)
		bodies[it.ID] = body
	}
	if len(items) == 0 {
		return
	}
	select {
	case w.out <- Batch{Items: items, Bodies: bodies}:
	default: // consumer behind; the next poll's feed check already moved on
	}
}

func (w *Watcher) markSeen(acc string) {
	if w.seen[acc] {
		return
	}
	w.seen[acc] = true
	w.seenOrder = append(w.seenOrder, acc)
	if len(w.seenOrder) > 5000 { // bound memory, same cap used elsewhere in this app
		drop := w.seenOrder[0]
		w.seenOrder = w.seenOrder[1:]
		delete(w.seen, drop)
	}
}

// --- current-events Atom feed ---

// subjectEntry is one "(Subject)" role entry from the current-events feed —
// the affected issuer, not the filer.
type subjectEntry struct {
	IssuerName string
	IssuerCIK  int
	Accession  string
	IndexURL   string
	Filed      time.Time
	Amendment  bool
}

type atomFeed struct {
	Entries []atomEntry `xml:"entry"`
}
type atomEntry struct {
	Title    string       `xml:"title"`
	Link     atomLink     `xml:"link"`
	Updated  string       `xml:"updated"`
	Category atomCategory `xml:"category"`
	ID       string       `xml:"id"`
}
type atomLink struct {
	Href string `xml:"href,attr"`
}
type atomCategory struct {
	Term string `xml:"term,attr"`
}

// titleRE splits "{form} - {Name} ({CIK}) ({Role})" — the exact shape
// confirmed live across every entry captured (e.g.
// "SC 13G - Cable One, Inc. (0001632127) (Subject)"). The form prefix is
// skipped non-greedily up to the FIRST " - ", which is always the form/name
// boundary: form codes never contain a space-hyphen-space themselves, even
// ones with an internal hyphen like "SC TO-T/A" (the hyphen there has no
// surrounding spaces). Name is captured greedily so it can itself contain
// " - " without being mis-split.
var titleRE = regexp.MustCompile(`^.*?\s-\s(.*)\s\((\d+)\)\s\(([^)]+)\)$`)

// accessionRE pulls the accession number out of the entry id
// ("urn:tag:sec.gov,2008:accession-number=0001959173-26-005425").
var accessionRE = regexp.MustCompile(`accession-number=([\d-]+)`)

func (w *Watcher) fetchCurrentFeed(ctx context.Context, formType string) ([]subjectEntry, error) {
	u := w.feedBase + "?action=getcurrent&type=" + url.QueryEscape(formType) +
		"&company=&dateb=&owner=include&count=100&output=atom"
	body, err := w.getSEC(ctx, u)
	if err != nil {
		return nil, err
	}
	var feed atomFeed
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.CharsetReader = xmlCharsetReader
	if err := dec.Decode(&feed); err != nil {
		return nil, fmt.Errorf("decode current feed: %w", err)
	}
	out := make([]subjectEntry, 0, len(feed.Entries))
	for _, e := range feed.Entries {
		m := titleRE.FindStringSubmatch(strings.TrimSpace(e.Title))
		if m == nil || !strings.EqualFold(strings.TrimSpace(m[3]), "subject") {
			continue // only the affected issuer's own entry, not the filer's
		}
		accM := accessionRE.FindStringSubmatch(e.ID)
		if accM == nil {
			continue
		}
		cik, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		filed, _ := time.Parse(time.RFC3339, e.Updated) // best-effort; zero value is fine as a fallback
		out = append(out, subjectEntry{
			IssuerName: strings.TrimSpace(m[1]),
			IssuerCIK:  cik,
			Accession:  accM[1],
			IndexURL:   e.Link.Href,
			Filed:      filed,
			Amendment:  strings.EqualFold(strings.TrimSpace(formType), "SC 13G/A"),
		})
	}
	return out, nil
}

// xmlCharsetReader lets encoding/xml decode EDGAR's Atom feed, which
// declares encoding="ISO-8859-1" in its prolog — confirmed on every real
// feed fetch this session. Go's decoder refuses ANY declared non-UTF-8
// charset unless CharsetReader is set, even if the actual bytes are pure
// ASCII, so this must run on every decode, not just ones with high bytes.
// ISO-8859-1 maps byte value N directly to Unicode code point U+00NN, so
// converting is a one-to-one byte->rune widening — no charset table needed.
func xmlCharsetReader(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "utf-8", "us-ascii", "":
		return input, nil
	case "iso-8859-1", "latin1", "latin-1":
		raw, err := io.ReadAll(input)
		if err != nil {
			return nil, err
		}
		runes := make([]rune, len(raw))
		for i, b := range raw {
			runes[i] = rune(b)
		}
		return bytes.NewReader([]byte(string(runes))), nil
	default:
		return nil, fmt.Errorf("unsupported XML charset %q", charset)
	}
}

// getSEC performs a fair-access GET (the UA every EDGAR request in this app
// must send, or SEC returns 403 "Undeclared Automated Tool").
func (w *Watcher) getSEC(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", fairAccessUA)
	resp, err := w.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", rawURL, resp.StatusCode)
	}
	return body, nil
}
