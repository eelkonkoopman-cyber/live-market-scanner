package edgar

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

const (
	// allPollInterval is deliberately tighter than the 13G watcher's minute:
	// the unfiltered feed carries every form type, so a page (count=100) can
	// roll over quickly on a busy filing day. Still far inside SEC's
	// 10 req/s fair-access cap. The 13G watcher's cadence is unchanged.
	allPollInterval = 30 * time.Second

	// allSeenCap bounds the accession dedup memory. The unfiltered feed
	// moves much faster than the 13G-only one, so this is sized to hold
	// well over an hour of the busiest observed filing rate while staying
	// trivially small. The invariant that actually matters is structural,
	// not temporal: an accession leaves the ring only after allSeenCap
	// newer accessions were recorded, and by then it is long off the
	// 100-entry feed page — so a ring wrap can never re-admit a filing
	// still visible on the page, at any filing rate.
	allSeenCap = 2048

	// allPendingCap bounds the pending pen (filings whose CIK resolved to a
	// ticker that today's scans had NOT yet surfaced — see poll). Sized for
	// hours of resolvable out-of-set filings: the second-chance window that
	// matters is minutes (filing -> price move -> scan surfaces the symbol),
	// so FIFO eviction at this cap loses nothing that was still actionable.
	allPendingCap = 1024
)

// AllConfig wires the all-filings watcher to the rest of the app.
type AllConfig struct {
	// SymsFilter returns the set of symbols the day's scans have surfaced —
	// the only symbols worth emitting a filing for. Called once per poll
	// (not per entry) so a large set is snapshotted, not re-locked per
	// filing. Required: a nil SymsFilter disables the watcher (fail closed,
	// same stance as flagread's incomplete-config guard).
	SymsFilter func() map[string]struct{}
}

// AllWatcher polls the SAME EDGAR current-events feed as the 13G watcher but
// with NO form-type filter: every filing type the SEC publishes, gated down
// to the symbols the day's scans have actually surfaced (SymsFilter).
// Motivation (user request, 2026-08-03): material filings — 8-Ks, offering
// prospectuses, registrations, insider transactions — were invisible to the
// news pipeline unless a wire service happened to write them up.
//
// Division of labor with the 13G watcher: SC 13G / SC 13G/A entries are
// SKIPPED here even when their symbol passes the filter, because the
// specialized watcher owns them and produces a richer headline (it parses
// the filing's own primary_doc.xml for the exact ownership percentage).
// URL-level dedup downstream can NOT catch that overlap — the two watchers
// link different documents for the same filing — so the skip must happen
// here, at the form level.
//
// Construct with NewAll, sharing the 13G *Watcher's fair-access HTTP client
// (getSEC), ISO-8859-1 charset reader, and daily CIK→ticker map. Kill
// switch: SCANNER_EDGAR_ALL=0 (same convention as SCANNER_FLAGREAD,
// SCANNER_AUTOSUM, SCANNER_TWS).
type AllWatcher struct {
	shared *Watcher // fair-access client, feed base, CIK→ticker map
	cfg    AllConfig
	out    chan Batch

	enabled bool

	// seen bounds re-processing of accession numbers already forwarded;
	// baseline suppresses the entire first poll (whatever is already on the
	// feed at startup) so a restart doesn't replay old filings as new —
	// same rationale as the 13G watcher.
	seen      map[string]bool
	seenOrder []string
	baseline  bool

	// pagesAllNew counts polls (after baseline) where EVERY accession on
	// the page was unseen — the signal that more than one page of filings
	// arrived between polls and the oldest were silently missed (same
	// truncation concern internal/feed/benzinga.go warns about on a full
	// news page). Kept as a field so tests can assert the warning fired
	// without scraping logs.
	pagesAllNew int
	// unresolved counts entries skipped because their CIK has no ticker in
	// the daily map (funds, individuals, very recent IPOs).
	unresolved int

	// pending holds filings whose CIK resolved to a real ticker that was
	// NOT in SymsFilter's set at the time — the timing hole the adversarial
	// review named: for a momentum scanner the filing usually PRECEDES the
	// scan surfacing the symbol (the filing causes the move), so dropping
	// on first sight would hide exactly the filings the feature exists
	// for. Every poll re-checks the pen against the fresh SymsFilter
	// snapshot and emits matches once. The seen ring stays authoritative
	// for feed dedup; the pen is only the second chance. Bounded FIFO
	// (allPendingCap), same shape as seen/seenOrder.
	pending      map[string]allEntry
	pendingOrder []string
}

// NewAll builds the all-filings watcher on top of the existing 13G watcher's
// shared plumbing. Enabled unless SCANNER_EDGAR_ALL=0.
func NewAll(shared *Watcher, cfg AllConfig) *AllWatcher {
	a := &AllWatcher{
		shared:   shared,
		cfg:      cfg,
		out:      make(chan Batch, 16),
		enabled:  os.Getenv("SCANNER_EDGAR_ALL") != "0",
		seen:     make(map[string]bool),
		pending:  make(map[string]allEntry),
		baseline: true,
	}
	// A missing dependency is a wiring bug; the failure mode we refuse is
	// emitting filings for symbols nobody's scans surfaced. Stay disabled
	// and say so (same stance as internal/flagread).
	if shared == nil || cfg.SymsFilter == nil {
		logx.L().Warn("edgar: all-filings watcher disabled: incomplete config")
		a.enabled = false
	}
	return a
}

// Out carries batches of filing NewsItems, same shape as the 13G watcher's.
func (a *AllWatcher) Out() <-chan Batch { return a.out }

func (a *AllWatcher) Run(ctx context.Context) error {
	if !a.enabled {
		logx.L().Info("edgar: all-filings watcher disabled (SCANNER_EDGAR_ALL=0 or incomplete config)")
		<-ctx.Done()
		return nil
	}
	tk := time.NewTicker(allPollInterval) // no immediate first poll, same as the 13G watcher
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tk.C:
			a.poll(ctx)
		}
	}
}

func (a *AllWatcher) poll(ctx context.Context) {
	entries, err := a.fetchAllCurrent(ctx)
	if err != nil {
		logx.L().Warn("edgar: all-filings poll failed", "err", err)
		if a.shared != nil && a.shared.health != nil {
			a.shared.health.SourceFail("edgar", err.Error())
		}
		return
	}
	// Reports under the SAME "edgar" name as the 13G watcher (a.shared) —
	// both poll the same underlying current-events feed, just with different
	// server-side filters, so one health identity per feed is correct, not
	// two competing ones on the status bar.
	if a.shared != nil && a.shared.health != nil {
		a.shared.health.SourceOK("edgar")
	}

	// One filing produces one Atom entry per ROLE (filer/subject/issuer/
	// reporting person), all sharing the accession number. Keep the entry
	// whose role names the AFFECTED COMPANY — its CIK is the one the
	// ticker map can resolve; a reporting person's or acquirer's never is.
	best := map[string]allEntry{}
	var order []string // page order of first appearance, for stable output
	for _, e := range entries {
		cur, ok := best[e.Accession]
		if !ok {
			order = append(order, e.Accession)
			best[e.Accession] = e
			continue
		}
		if roleRank(e.Role) > roleRank(cur.Role) {
			best[e.Accession] = e
		}
	}

	var fresh []allEntry
	for _, acc := range order {
		if a.seen[acc] {
			continue
		}
		a.markSeen(acc)
		fresh = append(fresh, best[acc])
	}

	if a.baseline {
		a.baseline = false
		return // suppress whatever was already on the feed at startup
	}
	if len(fresh) == 0 && len(a.pending) == 0 {
		return // nothing new and nothing held — a truly quiet tick
	}
	if len(fresh) > 0 && len(fresh) == len(order) {
		// Every accession on the page was new: more than one page's worth
		// of filings likely arrived since the last poll, and the oldest
		// scrolled off unseen (same warning internal/feed/benzinga.go
		// raises when a news page comes back full). That loss is PERMANENT —
		// this watcher fetches a single count=100 page and EDGAR's feed only
		// moves forward, so no later poll can recover what scrolled off; the
		// 30s cadence and the 100-entry page are what bound it.
		a.pagesAllNew++
		logx.L().Warn("edgar: all-filings page entirely new — oldest filings since last poll may have been missed",
			"pageAccessions", len(order))
	}

	a.shared.ensureTickersFresh(ctx)
	syms := a.cfg.SymsFilter() // once per poll, by contract

	items := make([]core.NewsItem, 0, len(fresh))
	skippedNoTicker := 0
	var held []allEntry // this poll's resolved-but-not-yet-surfaced filings
	for _, e := range fresh {
		if is13GForm(e.Form) {
			continue // the specialized 13G watcher owns these (richer headline)
		}
		ticker, ok := a.shared.tickers.lookup(e.CIK)
		if !ok {
			skippedNoTicker++
			continue
		}
		if _, in := syms[ticker]; !in {
			held = append(held, e) // second chance: the symbol may surface later
			continue
		}
		items = append(items, buildAllItem(e, ticker))
	}

	// Sweep EARLIER polls' held filings against this poll's snapshot, then pen
	// this poll's — sweeping first avoids re-testing the just-held entries
	// against the very snapshot that failed them.
	items = a.sweepPending(syms, items)
	for _, e := range held {
		a.holdPending(e)
	}

	if skippedNoTicker > 0 {
		a.unresolved += skippedNoTicker
		logx.L().Debug("edgar: all-filings entries with no resolvable ticker skipped",
			"thisPoll", skippedNoTicker, "total", a.unresolved)
	}
	if len(items) == 0 {
		return
	}
	select {
	case a.out <- Batch{Items: items}:
	default: // consumer behind; the next poll's feed check already moved on
	}
}

// sweepPending re-checks every held filing against the current SymsFilter
// snapshot, appending matches to items (emitted-once: matches leave the pen).
// The ticker is re-resolved rather than stored: the daily map can only get
// fresher, and an entry that resolved once keeps its place until it does again.
// A swept item keeps its FILING timestamp, so a filing emitted minutes after
// the fact still sorts into its true position on the panel.
func (a *AllWatcher) sweepPending(syms map[string]struct{}, items []core.NewsItem) []core.NewsItem {
	if len(a.pendingOrder) == 0 {
		return items
	}
	kept := a.pendingOrder[:0]
	for _, acc := range a.pendingOrder {
		e := a.pending[acc]
		ticker, ok := a.shared.tickers.lookup(e.CIK)
		if !ok {
			kept = append(kept, acc) // resolved once; hold through a map hiccup
			continue
		}
		if _, in := syms[ticker]; in {
			items = append(items, buildAllItem(e, ticker))
			delete(a.pending, acc)
			continue
		}
		kept = append(kept, acc)
	}
	a.pendingOrder = kept
	return items
}

// holdPending pens a resolved-but-out-of-set filing for later sweeps, bounded
// FIFO exactly like markSeen.
func (a *AllWatcher) holdPending(e allEntry) {
	if _, ok := a.pending[e.Accession]; ok {
		return
	}
	a.pending[e.Accession] = e
	a.pendingOrder = append(a.pendingOrder, e.Accession)
	if len(a.pendingOrder) > allPendingCap {
		drop := a.pendingOrder[0]
		a.pendingOrder = a.pendingOrder[1:]
		delete(a.pending, drop)
	}
}

func (a *AllWatcher) markSeen(acc string) {
	if a.seen[acc] {
		return
	}
	a.seen[acc] = true
	a.seenOrder = append(a.seenOrder, acc)
	if len(a.seenOrder) > allSeenCap {
		drop := a.seenOrder[0]
		a.seenOrder = a.seenOrder[1:]
		delete(a.seen, drop)
	}
}

// allEntry is one Atom entry from the unfiltered current-events feed, before
// role selection and symbol gating.
type allEntry struct {
	Form      string // e.g. "8-K", "424B5", "SC 13G/A" — from the category term
	Company   string
	CIK       int
	Role      string // "Filer" | "Subject" | "Issuer" | "Reporting" | "Filed by"
	Accession string
	IndexURL  string
	Filed     time.Time
}

// fetchAllCurrent pulls one unfiltered page (type= left empty, count=100 —
// the same page size the 13G queries use) and parses every entry, reusing
// the package's titleRE / accessionRE / charset reader.
func (a *AllWatcher) fetchAllCurrent(ctx context.Context) ([]allEntry, error) {
	u := a.shared.feedBase + "?action=getcurrent&type=&company=&dateb=&owner=include&count=100&output=atom"
	body, err := a.shared.getSEC(ctx, u)
	if err != nil {
		return nil, err
	}
	var feed atomFeed
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.CharsetReader = xmlCharsetReader
	if err := dec.Decode(&feed); err != nil {
		return nil, fmt.Errorf("decode all-filings feed: %w", err)
	}
	out := make([]allEntry, 0, len(feed.Entries))
	for _, e := range feed.Entries {
		title := strings.TrimSpace(e.Title)
		m := titleRE.FindStringSubmatch(title)
		if m == nil {
			continue
		}
		accM := accessionRE.FindStringSubmatch(e.ID)
		if accM == nil {
			continue
		}
		cik, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		form := strings.TrimSpace(e.Category.Term)
		if form == "" {
			// Fallback: the title's form prefix, everything before the
			// first " - " (the same boundary titleRE relies on).
			if i := strings.Index(title, " - "); i > 0 {
				form = strings.TrimSpace(title[:i])
			}
		}
		filed, _ := time.Parse(time.RFC3339, e.Updated) // best-effort; zero value falls back to now
		out = append(out, allEntry{
			Form:      form,
			Company:   strings.TrimSpace(m[1]),
			CIK:       cik,
			Role:      strings.TrimSpace(m[3]),
			Accession: accM[1],
			IndexURL:  e.Link.Href,
			Filed:     filed,
		})
	}
	return out, nil
}

// roleRank orders the per-accession role entries by how likely their CIK is
// the AFFECTED COMPANY's: Subject (tender offers, 13D/G, 144) beats Issuer
// (insider forms 3/4/5) beats Filer (the company's own filings: 8-K, 10-K,
// S-1, 424B…). "Reporting" and "Filed by" are people/acquirers — their CIK
// never resolves to a ticker, so they rank last.
func roleRank(role string) int {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "subject":
		return 3
	case "issuer":
		return 2
	case "filer":
		return 1
	default:
		return 0
	}
}

func is13GForm(form string) bool {
	f := strings.ToUpper(strings.TrimSpace(form))
	return f == "SC 13G" || f == "SC 13G/A"
}

// formLabel maps the handful of trader-meaningful form codes to a plain-
// English tag rendered after the code. Unknown forms return "" and the raw
// code stands alone — never guess a label.
func formLabel(form string) string {
	f := strings.ToUpper(strings.TrimSpace(form))
	switch f {
	case "8-K":
		return "material event"
	case "SC 13D", "SC 13D/A":
		return "activist stake"
	case "4":
		return "insider transaction"
	case "25", "25-NSE":
		return "delisting notice"
	case "10-K", "10-Q":
		return "financials"
	case "S-1", "S-1/A", "S-3", "S-3/A", "F-1", "F-3":
		return "registration (possible offering)"
	}
	if strings.HasPrefix(f, "424B") {
		return "prospectus (offering)"
	}
	return ""
}

// isDilutionForm marks the share-supply forms (shelf/offering paperwork)
// whose appearance is mechanically bearish for the float — those get
// Sentiment "negative"; every other form stays neutral ("").
func isDilutionForm(form string) bool {
	f := strings.ToUpper(strings.TrimSpace(form))
	for _, p := range []string{"424B", "S-1", "S-3", "F-1", "F-3"} {
		if strings.HasPrefix(f, p) {
			return true
		}
	}
	return false
}

// buildAllItem renders one gated filing as a NewsItem. HasBody stays false:
// the summarizer's existing sec.gov URL fetch already covers ✨ Summarize
// from the filing index page, so no body is fabricated here.
func buildAllItem(e allEntry, ticker string) core.NewsItem {
	headline := "SEC filing: " + e.Form
	if label := formLabel(e.Form); label != "" {
		headline += " (" + label + ")"
	}
	headline += " — " + e.Company + " (" + ticker + ")"

	sentiment := ""
	if isDilutionForm(e.Form) {
		sentiment = "negative"
	}
	ts := e.Filed.UnixMilli()
	if ts <= 0 {
		ts = timeNowMs()
	}
	return core.NewsItem{
		ID:        "sec-" + e.Accession,
		Tickers:   []string{ticker},
		Headline:  headline,
		Source:    "SEC EDGAR",
		URL:       e.IndexURL,
		TS:        ts,
		Sentiment: sentiment,
		HasBody:   false,
	}
}
