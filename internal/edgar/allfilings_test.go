package edgar

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// realShapedAllFeed mirrors, entry for entry, the exact XML structure of the
// REAL captured feed in edgar13g_test.go (realCurrentFeed): same prolog and
// ISO-8859-1 declaration, same element order inside <entry>, same title
// shape "{form} - {Name} ({CIK}) ({Role})", same urn:tag accession ids, same
// category term carrying the form type. Only the companies/forms are staged,
// spanning every gate the all-filings watcher applies:
//
//   - 8-K by Acme Corp (Filer)      — CIK resolvable, symbol IN the filter: KEPT, neutral
//   - 424B5 by Dilute Inc (Filer)   — in-set: KEPT with Sentiment "negative"
//   - 8-K by Other Corp (Filer)     — CIK resolvable but symbol NOT in set: SKIPPED
//   - SC 13G on Acme Corp (Subject+Reporting roles, one accession) — in-set
//     but owned by the specialized 13G watcher: SKIPPED
//   - 8-K by Ghost Corp (Filer)     — CIK absent from the ticker map: SKIPPED
const realShapedAllFeed = `<?xml version="1.0" encoding="ISO-8859-1" ?>
<feed xmlns="http://www.w3.org/2005/Atom">
<title>Latest Filings - Mon, 03 Aug 2026 09:31:12 EDT</title>
<updated>2026-08-03T09:31:12-04:00</updated>
<entry>
<title>8-K - Acme Corp (0001111111) (Filer)</title>
<link rel="alternate" type="text/html" href="https://www.sec.gov/Archives/edgar/data/1111111/000111111126000101/0001111111-26-000101-index.htm"/>
<summary type="html"> &lt;b&gt;Filed:&lt;/b&gt; 2026-08-03 &lt;b&gt;AccNo:&lt;/b&gt; 0001111111-26-000101 &lt;b&gt;Size:&lt;/b&gt; 24 KB</summary>
<updated>2026-08-03T09:30:40-04:00</updated>
<category scheme="https://www.sec.gov/" label="form type" term="8-K"/>
<id>urn:tag:sec.gov,2008:accession-number=0001111111-26-000101</id>
</entry>
<entry>
<title>424B5 - Dilute Inc (0002222222) (Filer)</title>
<link rel="alternate" type="text/html" href="https://www.sec.gov/Archives/edgar/data/2222222/000222222226000055/0002222222-26-000055-index.htm"/>
<summary type="html"> &lt;b&gt;Filed:&lt;/b&gt; 2026-08-03 &lt;b&gt;AccNo:&lt;/b&gt; 0002222222-26-000055 &lt;b&gt;Size:&lt;/b&gt; 180 KB</summary>
<updated>2026-08-03T09:30:12-04:00</updated>
<category scheme="https://www.sec.gov/" label="form type" term="424B5"/>
<id>urn:tag:sec.gov,2008:accession-number=0002222222-26-000055</id>
</entry>
<entry>
<title>8-K - Other Corp (0003333333) (Filer)</title>
<link rel="alternate" type="text/html" href="https://www.sec.gov/Archives/edgar/data/3333333/000333333326000201/0003333333-26-000201-index.htm"/>
<summary type="html"> &lt;b&gt;Filed:&lt;/b&gt; 2026-08-03 &lt;b&gt;AccNo:&lt;/b&gt; 0003333333-26-000201 &lt;b&gt;Size:&lt;/b&gt; 31 KB</summary>
<updated>2026-08-03T09:29:55-04:00</updated>
<category scheme="https://www.sec.gov/" label="form type" term="8-K"/>
<id>urn:tag:sec.gov,2008:accession-number=0003333333-26-000201</id>
</entry>
<entry>
<title>SC 13G - Big Fund LP (0004444444) (Reporting)</title>
<link rel="alternate" type="text/html" href="https://www.sec.gov/Archives/edgar/data/4444444/000444444426000007/0004444444-26-000007-index.htm"/>
<summary type="html"> &lt;b&gt;Filed:&lt;/b&gt; 2026-08-03 &lt;b&gt;AccNo:&lt;/b&gt; 0004444444-26-000007 &lt;b&gt;Size:&lt;/b&gt; 12 KB</summary>
<updated>2026-08-03T09:29:31-04:00</updated>
<category scheme="https://www.sec.gov/" label="form type" term="SC 13G"/>
<id>urn:tag:sec.gov,2008:accession-number=0004444444-26-000007</id>
</entry>
<entry>
<title>SC 13G - Acme Corp (0001111111) (Subject)</title>
<link rel="alternate" type="text/html" href="https://www.sec.gov/Archives/edgar/data/1111111/000444444426000007/0004444444-26-000007-index.htm"/>
<summary type="html"> &lt;b&gt;Filed:&lt;/b&gt; 2026-08-03 &lt;b&gt;AccNo:&lt;/b&gt; 0004444444-26-000007 &lt;b&gt;Size:&lt;/b&gt; 12 KB</summary>
<updated>2026-08-03T09:29:31-04:00</updated>
<category scheme="https://www.sec.gov/" label="form type" term="SC 13G"/>
<id>urn:tag:sec.gov,2008:accession-number=0004444444-26-000007</id>
</entry>
<entry>
<title>8-K - Ghost Corp (0005555555) (Filer)</title>
<link rel="alternate" type="text/html" href="https://www.sec.gov/Archives/edgar/data/5555555/000555555526000009/0005555555-26-000009-index.htm"/>
<summary type="html"> &lt;b&gt;Filed:&lt;/b&gt; 2026-08-03 &lt;b&gt;AccNo:&lt;/b&gt; 0005555555-26-000009 &lt;b&gt;Size:&lt;/b&gt; 19 KB</summary>
<updated>2026-08-03T09:29:02-04:00</updated>
<category scheme="https://www.sec.gov/" label="form type" term="8-K"/>
<id>urn:tag:sec.gov,2008:accession-number=0005555555-26-000009</id>
</entry>
</feed>`

// realShapedAllFeedSecondPage: the Acme 8-K again (dedup must drop it) plus
// one genuinely new in-set filing.
const realShapedAllFeedSecondPage = `<?xml version="1.0" encoding="ISO-8859-1" ?>
<feed xmlns="http://www.w3.org/2005/Atom">
<title>Latest Filings - Mon, 03 Aug 2026 09:31:42 EDT</title>
<updated>2026-08-03T09:31:42-04:00</updated>
<entry>
<title>10-Q - Acme Corp (0001111111) (Filer)</title>
<link rel="alternate" type="text/html" href="https://www.sec.gov/Archives/edgar/data/1111111/000111111126000102/0001111111-26-000102-index.htm"/>
<summary type="html"> &lt;b&gt;Filed:&lt;/b&gt; 2026-08-03 &lt;b&gt;AccNo:&lt;/b&gt; 0001111111-26-000102 &lt;b&gt;Size:&lt;/b&gt; 800 KB</summary>
<updated>2026-08-03T09:31:20-04:00</updated>
<category scheme="https://www.sec.gov/" label="form type" term="10-Q"/>
<id>urn:tag:sec.gov,2008:accession-number=0001111111-26-000102</id>
</entry>
<entry>
<title>8-K - Acme Corp (0001111111) (Filer)</title>
<link rel="alternate" type="text/html" href="https://www.sec.gov/Archives/edgar/data/1111111/000111111126000101/0001111111-26-000101-index.htm"/>
<summary type="html"> &lt;b&gt;Filed:&lt;/b&gt; 2026-08-03 &lt;b&gt;AccNo:&lt;/b&gt; 0001111111-26-000101 &lt;b&gt;Size:&lt;/b&gt; 24 KB</summary>
<updated>2026-08-03T09:30:40-04:00</updated>
<category scheme="https://www.sec.gov/" label="form type" term="8-K"/>
<id>urn:tag:sec.gov,2008:accession-number=0001111111-26-000101</id>
</entry>
</feed>`

const allTickersJSON = `{"0":{"cik_str":1111111,"ticker":"ACME","title":"Acme Corp"},` +
	`"1":{"cik_str":2222222,"ticker":"DLUT","title":"Dilute Inc"},` +
	`"2":{"cik_str":3333333,"ticker":"OTHR","title":"Other Corp"}}`

// allHarness serves the ticker list plus whichever feed body the current
// tick selects, and records every current-events query for assertions.
type allHarness struct {
	srv   *httptest.Server
	a     *AllWatcher
	mu    sync.Mutex
	feeds []string // feeds[tick]; last one repeats when polls outnumber entries
	tick  int
	qs    []url.Values // every ?action=getcurrent query seen

	syms        map[string]struct{} // live filter set; swap with setSyms
	filterCalls int
}

func newAllHarness(t *testing.T, filterSyms []string, feeds ...string) *allHarness {
	t.Helper()
	h := &allHarness{feeds: feeds}
	h.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), "company_tickers.json") {
			_, _ = rw.Write([]byte(allTickersJSON))
			return
		}
		h.mu.Lock()
		h.qs = append(h.qs, r.URL.Query())
		i := h.tick
		if i >= len(h.feeds) {
			i = len(h.feeds) - 1
		}
		body := h.feeds[i]
		h.mu.Unlock()
		_, _ = rw.Write([]byte(body))
	}))
	t.Cleanup(h.srv.Close)

	shared := New()
	shared.feedBase = h.srv.URL
	shared.tickerListURL = h.srv.URL + "/company_tickers.json"

	h.setSyms(filterSyms...)
	h.a = NewAll(shared, AllConfig{SymsFilter: func() map[string]struct{} {
		h.mu.Lock()
		h.filterCalls++
		set := h.syms
		h.mu.Unlock()
		return set
	}})
	return h
}

func (h *allHarness) setTick(n int) {
	h.mu.Lock()
	h.tick = n
	h.mu.Unlock()
}

// setSyms swaps the scan-surfaced set mid-test — the real scanner's set grows
// all day, and the timing tests depend on a symbol surfacing AFTER its filing.
func (h *allHarness) setSyms(syms ...string) {
	set := make(map[string]struct{}, len(syms))
	for _, s := range syms {
		set[s] = struct{}{}
	}
	h.mu.Lock()
	h.syms = set
	h.mu.Unlock()
}

func drainAll(t *testing.T, a *AllWatcher) []Batch {
	t.Helper()
	var out []Batch
	for {
		select {
		case b := <-a.Out():
			out = append(out, b)
		default:
			return out
		}
	}
}

// The catastrophic failure modes for this watcher are silent DROPS of real
// filings and silent LEAKS of unfiltered ones — so this test pins both
// directions: exactly the two in-set, non-13G filings survive, with the
// exact headline/label/sentiment/ID/URL shape, and nothing else does.
func TestAllPollKeepsOnlyScanSurfacedSymbols(t *testing.T) {
	h := newAllHarness(t, []string{"ACME", "DLUT"}, realCurrentFeedEmpty, realShapedAllFeed)
	ctx := context.Background()

	h.a.poll(ctx) // baseline (empty page)
	if got := drainAll(t, h.a); got != nil {
		t.Fatalf("baseline poll must not emit, got %+v", got)
	}

	h.setTick(1)
	h.a.poll(ctx)
	batches := drainAll(t, h.a)
	if len(batches) != 1 {
		t.Fatalf("want exactly 1 batch, got %d: %+v", len(batches), batches)
	}
	items := batches[0].Items
	if len(items) != 2 {
		t.Fatalf("want exactly the 2 in-set non-13G filings, got %d: %+v", len(items), items)
	}
	byID := map[string]int{}
	for i, it := range items {
		byID[it.ID] = i
	}

	// The in-set 8-K: kept, labeled, neutral.
	i, ok := byID["sec-0001111111-26-000101"]
	if !ok {
		t.Fatalf("Acme 8-K missing (an in-set filing was silently dropped): %+v", items)
	}
	acme := items[i]
	if acme.Headline != "SEC filing: 8-K (material event) — Acme Corp (ACME)" {
		t.Fatalf("Headline: %q", acme.Headline)
	}
	if acme.Sentiment != "" {
		t.Fatalf("an 8-K is not a dilution form; Sentiment must stay empty, got %q", acme.Sentiment)
	}
	if len(acme.Tickers) != 1 || acme.Tickers[0] != "ACME" {
		t.Fatalf("Tickers: %+v", acme.Tickers)
	}
	if acme.Source != "SEC EDGAR" {
		t.Fatalf("Source: %q", acme.Source)
	}
	if acme.URL != "https://www.sec.gov/Archives/edgar/data/1111111/000111111126000101/0001111111-26-000101-index.htm" {
		t.Fatalf("URL must be the filing index page from the Atom link: %q", acme.URL)
	}
	if acme.HasBody {
		t.Fatal("HasBody must be false: the summarizer fetches the sec.gov URL itself")
	}
	want := time.Date(2026, 8, 3, 9, 30, 40, 0, time.FixedZone("EDT", -4*3600)).UnixMilli()
	if acme.TS != want {
		t.Fatalf("TS from the entry's updated field: want %d got %d", want, acme.TS)
	}

	// The in-set 424B5: kept, negative sentiment.
	i, ok = byID["sec-0002222222-26-000055"]
	if !ok {
		t.Fatalf("Dilute 424B5 missing: %+v", items)
	}
	dlut := items[i]
	if dlut.Headline != "SEC filing: 424B5 (prospectus (offering)) — Dilute Inc (DLUT)" {
		t.Fatalf("Headline: %q", dlut.Headline)
	}
	if dlut.Sentiment != "negative" {
		t.Fatalf("a 424B5 is dilution paperwork; Sentiment must be negative, got %q", dlut.Sentiment)
	}

	// The NEGATIVE space: the resolvable-but-out-of-set 8-K must not leak.
	if _, leaked := byID["sec-0003333333-26-000201"]; leaked {
		t.Fatal("Other Corp (OTHR) is not in the scan-surfaced set and must not be emitted")
	}
	// The filter snapshot is taken once per poll, not per entry.
	h.mu.Lock()
	calls := h.filterCalls
	h.mu.Unlock()
	if calls != 1 {
		t.Fatalf("SymsFilter must be called exactly once per emitting poll, got %d", calls)
	}
}

func TestAllPollSkips13GFormsOwnedBySpecializedWatcher(t *testing.T) {
	// ACME is IN the set and the fixture's SC 13G names Acme Corp as
	// Subject — the only thing keeping it out is the form-level skip.
	// URL dedup downstream can never catch this overlap (the two watchers
	// link different documents), so a regression here double-posts every
	// 13G with a worse headline.
	h := newAllHarness(t, []string{"ACME", "DLUT"}, realCurrentFeedEmpty, realShapedAllFeed)
	ctx := context.Background()
	h.a.poll(ctx) // baseline
	h.setTick(1)
	h.a.poll(ctx)
	for _, b := range drainAll(t, h.a) {
		for _, it := range b.Items {
			if it.ID == "sec-0004444444-26-000007" {
				t.Fatalf("SC 13G leaked into the all-filings stream: %+v", it)
			}
			if strings.Contains(it.Headline, "SC 13G") {
				t.Fatalf("13G-shaped headline leaked: %q", it.Headline)
			}
		}
	}
}

func TestAllPollSkipsUnresolvableCIK(t *testing.T) {
	h := newAllHarness(t, []string{"ACME", "DLUT"}, realCurrentFeedEmpty, realShapedAllFeed)
	ctx := context.Background()
	h.a.poll(ctx) // baseline
	h.setTick(1)
	h.a.poll(ctx)
	for _, b := range drainAll(t, h.a) {
		for _, it := range b.Items {
			if strings.Contains(it.Headline, "Ghost Corp") {
				t.Fatalf("an unresolvable CIK must be skipped, not fabricated: %+v", it)
			}
		}
	}
	if h.a.unresolved != 1 {
		t.Fatalf("the skip must be counted: want 1 unresolved, got %d", h.a.unresolved)
	}
}

func TestAllPollDedupsAccessionAcrossPolls(t *testing.T) {
	h := newAllHarness(t, []string{"ACME", "DLUT"},
		realCurrentFeedEmpty, realShapedAllFeed, realShapedAllFeedSecondPage)
	ctx := context.Background()
	h.a.poll(ctx) // baseline
	h.setTick(1)
	h.a.poll(ctx)
	drainAll(t, h.a) // first page's emissions, covered elsewhere

	h.setTick(2)
	h.a.poll(ctx) // Acme 8-K repeats + one new 10-Q
	batches := drainAll(t, h.a)
	if len(batches) != 1 || len(batches[0].Items) != 1 {
		t.Fatalf("only the genuinely new filing may emit, got %+v", batches)
	}
	it := batches[0].Items[0]
	if it.ID != "sec-0001111111-26-000102" {
		t.Fatalf("want the new 10-Q, got %+v", it)
	}
	if it.Headline != "SEC filing: 10-Q (financials) — Acme Corp (ACME)" {
		t.Fatalf("Headline: %q", it.Headline)
	}

	h.a.poll(ctx) // identical page again -> nothing at all
	if got := drainAll(t, h.a); got != nil {
		t.Fatalf("re-polling the same page must not re-emit, got %+v", got)
	}
}

func TestAllPollBaselineSuppressesFirstPoll(t *testing.T) {
	// Harsher than the empty-page variant: the FIRST poll already carries
	// real in-set filings, and every one of them must be swallowed —
	// they're pre-startup history, not news.
	h := newAllHarness(t, []string{"ACME", "DLUT"}, realShapedAllFeed, realShapedAllFeedSecondPage)
	ctx := context.Background()
	h.a.poll(ctx)
	if got := drainAll(t, h.a); got != nil {
		t.Fatalf("baseline poll must not emit even with in-set filings on the page, got %+v", got)
	}
	if h.a.pagesAllNew != 0 {
		t.Fatal("the truncation warning must not fire on the baseline poll")
	}

	h.setTick(1)
	h.a.poll(ctx) // only the 10-Q is new relative to the baseline
	batches := drainAll(t, h.a)
	if len(batches) != 1 || len(batches[0].Items) != 1 || batches[0].Items[0].ID != "sec-0001111111-26-000102" {
		t.Fatalf("after baseline, only genuinely new filings emit: %+v", batches)
	}
}

func TestAllPollCountsEntirelyNewPageAsTruncationSignal(t *testing.T) {
	h := newAllHarness(t, []string{"ACME", "DLUT"},
		realCurrentFeedEmpty, realShapedAllFeed, realShapedAllFeedSecondPage)
	ctx := context.Background()
	h.a.poll(ctx) // baseline
	if h.a.pagesAllNew != 0 {
		t.Fatal("baseline must not count as a truncation signal")
	}

	h.setTick(1)
	h.a.poll(ctx) // every accession on this page is new -> filings may have been missed
	if h.a.pagesAllNew != 1 {
		t.Fatalf("an entirely-new page after baseline must be flagged, got %d", h.a.pagesAllNew)
	}

	h.setTick(2)
	h.a.poll(ctx) // page overlaps (Acme 8-K already seen) -> normal, no signal
	if h.a.pagesAllNew != 1 {
		t.Fatalf("a partially-seen page is the healthy case and must not be flagged, got %d", h.a.pagesAllNew)
	}
}

// TestAllPollEmitsFilingWhenSymbolSurfacesLater is the timing hole the
// adversarial review named: a filing lands at T for a symbol the scans have
// NOT surfaced yet, and the symbol surfaces minutes later — for a momentum
// scanner that is the NORMAL order (the filing causes the move that surfaces
// the symbol). Ring-marking the accession seen on first sight while
// out-of-set made the filing permanently invisible; the pending pen must
// give it a second chance, exactly once.
func TestAllPollEmitsFilingWhenSymbolSurfacesLater(t *testing.T) {
	// OTHR's 8-K is on the page but only ACME/DLUT are surfaced.
	h := newAllHarness(t, []string{"ACME", "DLUT"}, realCurrentFeedEmpty, realShapedAllFeed)
	ctx := context.Background()
	h.a.poll(ctx) // baseline
	h.setTick(1)
	h.a.poll(ctx)
	for _, b := range drainAll(t, h.a) {
		for _, it := range b.Items {
			if it.ID == "sec-0003333333-26-000201" {
				t.Fatalf("OTHR must not emit while out of the set: %+v", it)
			}
		}
	}

	// The scans surface OTHR one poll later; the page is unchanged (the
	// accession is long since ring-seen), so only the pending pen can save it.
	h.setSyms("ACME", "DLUT", "OTHR")
	signalsBefore := h.a.pagesAllNew
	h.a.poll(ctx)
	if h.a.pagesAllNew != signalsBefore {
		t.Fatal("a fully-seen page swept only for pending filings must not count as a truncation signal")
	}
	batches := drainAll(t, h.a)
	if len(batches) != 1 || len(batches[0].Items) != 1 {
		t.Fatalf("the held OTHR filing must emit once its symbol surfaces, got %+v", batches)
	}
	it := batches[0].Items[0]
	if it.ID != "sec-0003333333-26-000201" {
		t.Fatalf("want the held OTHR 8-K, got %+v", it)
	}
	if it.Headline != "SEC filing: 8-K (material event) — Other Corp (OTHR)" {
		t.Fatalf("Headline: %q", it.Headline)
	}
	// TS must be the FILING time, not the emit time — the panel orders by it.
	want := time.Date(2026, 8, 3, 9, 29, 55, 0, time.FixedZone("EDT", -4*3600)).UnixMilli()
	if it.TS != want {
		t.Fatalf("TS must stay the filing time: want %d got %d", want, it.TS)
	}

	// Exactly once: further polls with the same page and set emit nothing.
	h.a.poll(ctx)
	if got := drainAll(t, h.a); got != nil {
		t.Fatalf("a pending filing may only emit once, got %+v", got)
	}
}

// The pending pen is a bounded FIFO, exactly like the seen ring: it must
// never grow without limit, and eviction must drop the OLDEST held filing.
func TestPendingPenBoundedFIFO(t *testing.T) {
	h := newAllHarness(t, []string{"ACME"}, realCurrentFeedEmpty)
	for i := 0; i <= allPendingCap; i++ {
		h.a.holdPending(allEntry{Accession: fmt.Sprintf("acc-%06d", i), Form: "8-K", CIK: 1})
	}
	if len(h.a.pending) != allPendingCap || len(h.a.pendingOrder) != allPendingCap {
		t.Fatalf("pen must cap at %d, got map=%d order=%d",
			allPendingCap, len(h.a.pending), len(h.a.pendingOrder))
	}
	if _, held := h.a.pending["acc-000000"]; held {
		t.Fatal("eviction must drop the oldest held filing")
	}
	if _, held := h.a.pending[fmt.Sprintf("acc-%06d", allPendingCap)]; !held {
		t.Fatal("the newest held filing must survive eviction")
	}
	// Holding the same accession twice must not double-book the FIFO.
	h.a.holdPending(allEntry{Accession: fmt.Sprintf("acc-%06d", allPendingCap)})
	if len(h.a.pendingOrder) != allPendingCap {
		t.Fatalf("re-holding must not duplicate FIFO slots, got %d", len(h.a.pendingOrder))
	}
}

func TestAllFeedRequestHasNoTypeFilterAndFullPage(t *testing.T) {
	h := newAllHarness(t, []string{"ACME"}, realCurrentFeedEmpty)
	h.a.poll(context.Background())
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.qs) != 1 {
		t.Fatalf("want 1 feed request, got %d", len(h.qs))
	}
	q := h.qs[0]
	if _, present := q["type"]; !present {
		t.Fatal("the type parameter must be present (and empty) — EDGAR treats a missing param differently in cached responses")
	}
	if q.Get("type") != "" {
		t.Fatalf("the all-filings feed must NOT filter by form type, got type=%q", q.Get("type"))
	}
	if q.Get("count") != "100" {
		t.Fatalf("count: want 100 got %q", q.Get("count"))
	}
	if q.Get("output") != "atom" {
		t.Fatalf("output: want atom got %q", q.Get("output"))
	}
	if q.Get("action") != "getcurrent" {
		t.Fatalf("action: want getcurrent got %q", q.Get("action"))
	}
}

func TestAllWatcherDisabledByEnv(t *testing.T) {
	t.Setenv("SCANNER_EDGAR_ALL", "0")
	h := newAllHarness(t, []string{"ACME"}, realShapedAllFeed) // constructed under the kill switch
	if h.a.enabled {
		t.Fatal("SCANNER_EDGAR_ALL=0 must disable the watcher")
	}
	if h.a.Out() == nil {
		t.Fatal("a disabled watcher still owes the caller a non-nil Out() channel")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.a.Run(ctx) }()

	select {
	case b := <-h.a.Out():
		t.Fatalf("disabled watcher emitted: %+v", b)
	case <-time.After(150 * time.Millisecond):
		// silent, as it should be
	}
	h.mu.Lock()
	polled := len(h.qs)
	h.mu.Unlock()
	if polled != 0 {
		t.Fatalf("disabled watcher hit the feed %d times", polled)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("disabled Run should exit cleanly on ctx cancel, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("disabled Run did not return after ctx cancel")
	}
}

func TestAllWatcherNilSymsFilterFailsClosed(t *testing.T) {
	a := NewAll(New(), AllConfig{})
	if a.enabled {
		t.Fatal("no SymsFilter means no gating; the watcher must fail closed (disabled)")
	}
}

// The label map and dilution classification, including the negative space:
// unknown forms render bare (never a guessed label), and non-dilution forms
// never pick up the bearish sentiment.
// TestAllPollReportsHealthUnderSharedEdgarName proves the all-filings watcher
// reports under the SAME "edgar" name the 13G watcher uses — they share one
// underlying current-events feed via the shared *Watcher, so one health
// identity is correct, not two competing ones on the status bar.
func TestAllPollReportsHealthUnderSharedEdgarName(t *testing.T) {
	h := newAllHarness(t, []string{"ACME"}, realCurrentFeedEmpty, realCurrentFeedEmpty)
	fh := &fakeHealth{}
	h.a.shared.SetHealth(fh)
	ctx := context.Background()

	h.a.poll(ctx) // baseline
	h.setTick(1)
	h.a.poll(ctx) // successful poll, empty feed

	if ok, _ := fh.snapshot(); len(ok) < 1 || ok[len(ok)-1] != "edgar" {
		t.Fatalf("a successful all-filings poll must report edgar OK, got %v", ok)
	}
}

// TestAllPollReportsHealthFailOnFetchError proves a fetch failure reports
// "edgar" failing with the error text, same name as the 13G watcher.
func TestAllPollReportsHealthFailOnFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), "company_tickers.json") {
			_, _ = rw.Write([]byte(allTickersJSON))
			return
		}
		rw.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	shared := New()
	shared.feedBase = srv.URL
	shared.tickerListURL = srv.URL + "/company_tickers.json"
	fh := &fakeHealth{}
	shared.SetHealth(fh)

	a := NewAll(shared, AllConfig{SymsFilter: func() map[string]struct{} { return map[string]struct{}{} }})
	a.poll(context.Background())

	_, fails := fh.snapshot()
	if len(fails) != 1 || fails[0] != "edgar" {
		t.Fatalf("a failed all-filings poll must report edgar failing once, got %v", fails)
	}
}

func TestFormLabelsAndDilutionSentiment(t *testing.T) {
	cases := []struct {
		form, label   string
		wantSentiment string
	}{
		{"8-K", "material event", ""},
		{"424B1", "prospectus (offering)", "negative"},
		{"424B3", "prospectus (offering)", "negative"},
		{"424B5", "prospectus (offering)", "negative"},
		{"S-1", "registration (possible offering)", "negative"},
		{"S-1/A", "registration (possible offering)", "negative"},
		{"S-3", "registration (possible offering)", "negative"},
		{"S-3/A", "registration (possible offering)", "negative"},
		{"F-1", "registration (possible offering)", "negative"},
		{"F-3", "registration (possible offering)", "negative"},
		{"SC 13D", "activist stake", ""},
		{"SC 13D/A", "activist stake", ""},
		{"4", "insider transaction", ""},
		{"25", "delisting notice", ""},
		{"25-NSE", "delisting notice", ""},
		{"10-K", "financials", ""},
		{"10-Q", "financials", ""},
		{"SC TO-T/A", "", ""}, // unknown: raw code, no label, no sentiment
		{"144", "", ""},
	}
	for _, c := range cases {
		if got := formLabel(c.form); got != c.label {
			t.Errorf("formLabel(%q): want %q got %q", c.form, c.label, got)
		}
		it := buildAllItem(allEntry{Form: c.form, Company: "Acme Corp", Accession: "0000000000-26-000001"}, "ACME")
		if it.Sentiment != c.wantSentiment {
			t.Errorf("Sentiment for %q: want %q got %q", c.form, c.wantSentiment, it.Sentiment)
		}
		wantHeadline := "SEC filing: " + c.form
		if c.label != "" {
			wantHeadline += " (" + c.label + ")"
		}
		wantHeadline += " — Acme Corp (ACME)"
		if it.Headline != wantHeadline {
			t.Errorf("Headline for %q: want %q got %q", c.form, wantHeadline, it.Headline)
		}
	}
}
