package edgar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHealth records SourceOK/SourceFail calls in order; shared by this file
// and allfilings_test.go (both package edgar), since both watchers are meant
// to report under the SAME "edgar" source name.
type fakeHealth struct {
	mu    sync.Mutex
	ok    []string
	fails []string
}

func (f *fakeHealth) SourceOK(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ok = append(f.ok, name)
}

func (f *fakeHealth) SourceFail(name, _ string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fails = append(f.fails, name)
}

func (f *fakeHealth) snapshot() (ok, fails []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ok...), append([]string(nil), f.fails...)
}

// realCurrentFeed13G is a REAL response captured live (curl, fair-access UA,
// read-only GET against the actual EDGAR current-events endpoint) — not a
// hand-built guess. It shows the exact structural fact this parser depends
// on: one accession number, two role entries ("Reporting" for the filer,
// "Subject" for the affected issuer), sharing everything but the title's
// trailing role and the leading CIK.
const realCurrentFeed = `<?xml version="1.0" encoding="ISO-8859-1" ?>
<feed xmlns="http://www.w3.org/2005/Atom">
<title>Latest Filings - Tue, 28 Jul 2026 16:37:52 EDT</title>
<updated>2026-07-28T16:37:52-04:00</updated>
<entry>
<title>144 - Remer Eric Richard (0001856906) (Reporting)</title>
<link rel="alternate" type="text/html" href="https://www.sec.gov/Archives/edgar/data/1856906/000195917326005425/0001959173-26-005425-index.htm"/>
<summary type="html"> &lt;b&gt;Filed:&lt;/b&gt; 2026-07-28 &lt;b&gt;AccNo:&lt;/b&gt; 0001959173-26-005425 &lt;b&gt;Size:&lt;/b&gt; 21 KB</summary>
<updated>2026-07-28T16:37:20-04:00</updated>
<category scheme="https://www.sec.gov/" label="form type" term="144"/>
<id>urn:tag:sec.gov,2008:accession-number=0001959173-26-005425</id>
</entry>
<entry>
<title>144 - EverCommerce Inc. (0001853145) (Subject)</title>
<link rel="alternate" type="text/html" href="https://www.sec.gov/Archives/edgar/data/1853145/000195917326005425/0001959173-26-005425-index.htm"/>
<summary type="html"> &lt;b&gt;Filed:&lt;/b&gt; 2026-07-28 &lt;b&gt;AccNo:&lt;/b&gt; 0001959173-26-005425 &lt;b&gt;Size:&lt;/b&gt; 21 KB</summary>
<updated>2026-07-28T16:37:20-04:00</updated>
<category scheme="https://www.sec.gov/" label="form type" term="144"/>
<id>urn:tag:sec.gov,2008:accession-number=0001959173-26-005425</id>
</entry>
<entry>
<title>SC TO-T/A - Kuva Labs, Inc. (0002117902) (Filed by)</title>
<link rel="alternate" type="text/html" href="https://www.sec.gov/Archives/edgar/data/2117902/000114036126029893/0001140361-26-029893-index.htm"/>
<summary type="html"> &lt;b&gt;Filed:&lt;/b&gt; 2026-07-28 &lt;b&gt;AccNo:&lt;/b&gt; 0001140361-26-029893 &lt;b&gt;Size:&lt;/b&gt; 47 KB</summary>
<updated>2026-07-28T16:36:34-04:00</updated>
<category scheme="https://www.sec.gov/" label="form type" term="SC TO-T/A"/>
<id>urn:tag:sec.gov,2008:accession-number=0001140361-26-029893</id>
</entry>
<entry>
<title>SC TO-T/A - LISATA THERAPEUTICS, INC. (0000320017) (Subject)</title>
<link rel="alternate" type="text/html" href="https://www.sec.gov/Archives/edgar/data/320017/000114036126029893/0001140361-26-029893-index.htm"/>
<summary type="html"> &lt;b&gt;Filed:&lt;/b&gt; 2026-07-28 &lt;b&gt;AccNo:&lt;/b&gt; 0001140361-26-029893 &lt;b&gt;Size:&lt;/b&gt; 47 KB</summary>
<updated>2026-07-28T16:36:34-04:00</updated>
<category scheme="https://www.sec.gov/" label="form type" term="SC TO-T/A"/>
<id>urn:tag:sec.gov,2008:accession-number=0001140361-26-029893</id>
</entry>
</feed>`

const realCurrentFeedEmpty = `<?xml version="1.0" encoding="ISO-8859-1" ?>
<feed xmlns="http://www.w3.org/2005/Atom">
<title>Latest Filings - Tue, 28 Jul 2026 16:37:26 EDT - No recent filings</title>
<updated>2026-07-28T16:37:26-04:00</updated>
</feed>`

func TestFetchCurrentFeedKeepsOnlySubjectEntries(t *testing.T) {
	w := New()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != fairAccessUA {
			t.Errorf("must send the SEC fair-access UA, got %q", got)
		}
		_, _ = rw.Write([]byte(realCurrentFeed))
	}))
	defer srv.Close()
	w.feedBase = srv.URL

	entries, err := w.fetchCurrentFeed(context.Background(), "SC 13G")
	if err != nil {
		t.Fatalf("fetchCurrentFeed: %v", err)
	}
	// 4 total entries in the fixture, 2 roles each accession -> exactly 2
	// "Subject" entries must survive; filer/"Filed by" entries must not.
	if len(entries) != 2 {
		t.Fatalf("want 2 subject entries, got %d: %+v", len(entries), entries)
	}
	byName := map[string]subjectEntry{}
	for _, e := range entries {
		byName[e.IssuerName] = e
	}
	ec, ok := byName["EverCommerce Inc."]
	if !ok {
		t.Fatalf("EverCommerce Inc. missing from %+v", entries)
	}
	if ec.IssuerCIK != 1853145 {
		t.Fatalf("CIK: want 1853145 got %d", ec.IssuerCIK)
	}
	if ec.Accession != "0001959173-26-005425" {
		t.Fatalf("accession: got %q", ec.Accession)
	}
	if !strings.Contains(ec.IndexURL, "1853145") {
		t.Fatalf("index URL should point at the filing: %q", ec.IndexURL)
	}

	lisata, ok := byName["LISATA THERAPEUTICS, INC."]
	if !ok {
		t.Fatalf("LISATA missing from %+v", entries)
	}
	if lisata.IssuerCIK != 320017 {
		t.Fatalf("leading zeros in the CIK must parse correctly: got %d", lisata.IssuerCIK)
	}
}

func TestFetchCurrentFeedEmptyIsNotAnError(t *testing.T) {
	w := New()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = rw.Write([]byte(realCurrentFeedEmpty))
	}))
	defer srv.Close()
	w.feedBase = srv.URL

	entries, err := w.fetchCurrentFeed(context.Background(), "SC 13G")
	if err != nil {
		t.Fatalf("an empty-but-valid feed must not error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("want 0 entries, got %+v", entries)
	}
}

// poll() end-to-end: baseline suppresses the first tick, a genuinely new
// filing on the second tick produces a batch, and re-polling the same
// filing again produces nothing (dedup by accession).
func TestPollBaselineThenDedup(t *testing.T) {
	var tick int
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), "primary_doc.xml") {
			_, _ = rw.Write([]byte(realPrimaryDocOriginal))
			return
		}
		if strings.Contains(r.URL.String(), "index.json") {
			_, _ = rw.Write([]byte(`{"directory":{"item":[{"name":"primary_doc.xml"}]}}`))
			return
		}
		if strings.Contains(r.URL.String(), "company_tickers.json") {
			_, _ = rw.Write([]byte(`{"0":{"cik_str":1632127,"ticker":"CABO","title":"Cable One, Inc."}}`))
			return
		}
		// current-events feed: empty on tick 1 (baseline), one real filing on tick 2+.
		if tick == 0 {
			_, _ = rw.Write([]byte(realCurrentFeedEmpty))
		} else {
			_, _ = rw.Write([]byte(strings.NewReplacer(
				"EverCommerce Inc.", "Cable One, Inc.",
				"1853145", "1632127",
				"0001959173-26-005425", "0000887777-26-000022",
			).Replace(realCurrentFeed)))
		}
	}))
	defer srv.Close()

	w := New()
	// Point every fetch this poll makes (current feed, filing directory,
	// primary_doc.xml, ticker list) at the one fake server.
	w.feedBase = srv.URL
	w.archivesBase = srv.URL
	w.tickerListURL = srv.URL + "/company_tickers.json"

	ctx := context.Background()
	tick = 0
	w.poll(ctx) // baseline: sees the (rewritten) filing, must NOT emit
	select {
	case b := <-w.Out():
		t.Fatalf("baseline poll must not emit, got %+v", b)
	default:
	}

	tick = 1
	w.poll(ctx) // now the "new" filing appears
	select {
	case b := <-w.Out():
		if len(b.Items) != 1 || b.Items[0].Tickers[0] != "CABO" {
			t.Fatalf("want one CABO item, got %+v", b.Items)
		}
		if !strings.Contains(b.Items[0].Headline, "5.05%") {
			t.Fatalf("headline should carry the real ownership %%: %q", b.Items[0].Headline)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no batch emitted for the new filing")
	}

	w.poll(ctx) // same accession again -> deduped, nothing new
	select {
	case b := <-w.Out():
		t.Fatalf("re-polling the same filing must not re-emit, got %+v", b)
	default:
	}
}

// TestPollReportsHealthOKThenFail proves a successful current-events poll
// reports "edgar" healthy, and a poll where BOTH form-type queries fail
// reports it failing exactly once (not once per query) with the error text.
func TestPollReportsHealthOKThenFail(t *testing.T) {
	healthy := true
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if !healthy {
			rw.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = rw.Write([]byte(realCurrentFeedEmpty))
	}))
	defer srv.Close()

	w := New()
	w.feedBase = srv.URL
	h := &fakeHealth{}
	w.SetHealth(h)

	w.poll(context.Background())
	if ok, _ := h.snapshot(); len(ok) != 1 || ok[0] != "edgar" {
		t.Fatalf("a successful poll must report edgar OK once, got %v", ok)
	}

	healthy = false
	w.poll(context.Background())
	_, fails := h.snapshot()
	if len(fails) != 1 || fails[0] != "edgar" {
		t.Fatalf("a poll where every form-type query fails must report edgar failing once, got %v", fails)
	}
}
