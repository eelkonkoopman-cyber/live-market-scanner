package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/backtest"
	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/feed"
)

// ─── The splits listing client (no socket: a RoundTripper answers) ───────

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func reply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}

const testKey = "SECRETKEY123"

func splitClientWith(rt rtFunc) *splitClient {
	return newSplitClient("https://api.test.invalid", testKey, &http.Client{Transport: rt})
}

func TestSplitClientPaginatesAndSendsTheKeyOnlyAsAParam(t *testing.T) {
	var mu sync.Mutex
	var urls []string
	c := splitClientWith(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		urls = append(urls, r.URL.String())
		mu.Unlock()
		q := r.URL.Query()
		if q.Get("apiKey") != testKey {
			t.Errorf("request without the key: %s", r.URL.Redacted())
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("the key must travel as the apiKey param, like the feed's own calls")
		}
		if r.URL.Path != "/v3/reference/splits" || r.Host != "api.test.invalid" {
			t.Errorf("unexpected request %s", r.URL.Redacted())
		}
		if q.Get("cursor") == "" {
			if q.Get("execution_date.gte") != "2026-09-18" || q.Get("execution_date.lte") != "2026-10-06" || q.Get("limit") != "1000" {
				t.Errorf("first page query %v", q)
			}
			return reply(200, `{"status":"OK","results":[
				{"ticker":"REV","execution_date":"2026-10-06","split_from":20,"split_to":1,"id":"a"},
				{"ticker":"FWD","execution_date":"2026-09-30","split_from":1,"split_to":2,"id":"b"}],
				"next_url":"https://api.test.invalid/v3/reference/splits?cursor=abc"}`), nil
		}
		return reply(200, `{"status":"OK","results":[{"ticker":"ODD","execution_date":"2026-09-25","split_from":3,"split_to":2.5}]}`), nil
	})
	got, err := c.Splits(context.Background(), "2026-09-18", "2026-10-06")
	if err != nil {
		t.Fatal(err)
	}
	want := []backtest.StockSplit{
		{Sym: "REV", Date: "2026-10-06", From: 20, To: 1},
		{Sym: "FWD", Date: "2026-09-30", From: 1, To: 2},
		{Sym: "ODD", Date: "2026-09-25", From: 3, To: 2.5},
	}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("splits %+v, want %+v", got, want)
	}
	if len(urls) != 2 {
		t.Errorf("%d requests, want 2 pages", len(urls))
	}
}

func TestSplitClientNeverFollowsANextURLToAnotherHost(t *testing.T) {
	var hosts []string
	c := splitClientWith(func(r *http.Request) (*http.Response, error) {
		hosts = append(hosts, r.URL.Host)
		return reply(200, `{"results":[],"next_url":"https://evil.example/v3/reference/splits?cursor=x"}`), nil
	})
	_, err := c.Splits(context.Background(), "2026-09-18", "2026-10-06")
	if err == nil || !strings.Contains(err.Error(), "another host") {
		t.Errorf("err = %v, want a refusal to follow next_url to another host", err)
	}
	for _, h := range hosts {
		if h != "api.test.invalid" {
			t.Errorf("a request went to %s: the key would have left", h)
		}
	}
}

func TestSplitClientErrorsNeverCarryTheKey(t *testing.T) {
	// a transport error: net/http puts the full request URL (with the key) in it
	c := splitClientWith(func(r *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: r.URL.String(), Err: errors.New("connection reset")}
	})
	_, err := c.Splits(context.Background(), "2026-09-18", "2026-10-06")
	if err == nil || strings.Contains(err.Error(), testKey) || !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("transport error %v: must keep the cause and lose the key", err)
	}

	// a non-200 whose body echoes the key
	c = splitClientWith(func(r *http.Request) (*http.Response, error) {
		return reply(500, `{"error":"bad request /v3/reference/splits?apiKey=`+testKey+`&x=1 key `+testKey+`"}`), nil
	})
	_, err = c.Splits(context.Background(), "2026-09-18", "2026-10-06")
	var he *feed.HTTPError
	if err == nil || strings.Contains(err.Error(), testKey) || !errors.As(err, &he) || he.Status != 500 {
		t.Errorf("HTTP 500 error %v: want a *feed.HTTPError with status 500 and no key", err)
	}

	// 429 stays recognisable to the fetcher's backoff
	c = splitClientWith(func(r *http.Request) (*http.Response, error) { return reply(429, `slow down`), nil })
	_, err = c.Splits(context.Background(), "2026-09-18", "2026-10-06")
	if !feed.IsRateLimitErr(err) {
		t.Errorf("429 -> %v, want IsRateLimitErr", err)
	}
	// 401 stays recognisable as a refused key
	c = splitClientWith(func(r *http.Request) (*http.Response, error) { return reply(401, `no`), nil })
	if _, err = c.Splits(context.Background(), "2026-09-18", "2026-10-06"); !isAuthErr(err) {
		t.Errorf("401 -> %v, want isAuthErr", err)
	}
	// a body that is not JSON
	c = splitClientWith(func(r *http.Request) (*http.Response, error) { return reply(200, `<html>`), nil })
	if _, err = c.Splits(context.Background(), "2026-09-18", "2026-10-06"); err == nil {
		t.Errorf("garbage body accepted")
	}
}

// ─── The fetcher: cache, dry, failures ───────────────────────────────────

func TestFetcherSplitsCachesAndDry(t *testing.T) {
	dir := t.TempDir()
	fk := &fake{splits: []backtest.StockSplit{{Sym: "REV", Date: "2026-10-06", From: 20, To: 1}}}
	f, _ := newFetcher(fk, dir)
	got, known, err := f.splits(context.Background(), "2026-09-18", "2026-10-06")
	if err != nil || !known || len(got) != 1 || got[0].Sym != "REV" || fk.splitsN.Load() != 1 {
		t.Fatalf("first call: %v known=%v err=%v calls=%d", got, known, err, fk.splitsN.Load())
	}
	if st := f.stats(); st.SplitsCalls != 1 || st.SplitsHits != 0 {
		t.Errorf("stats %+v", st)
	}

	// a second fetcher on the same cache: no call, a hit
	f2, _ := newFetcher(fk, dir)
	got, known, err = f2.splits(context.Background(), "2026-09-18", "2026-10-06")
	if err != nil || !known || len(got) != 1 || got[0].From != 20 || fk.splitsN.Load() != 1 {
		t.Fatalf("cached: %v known=%v err=%v calls=%d", got, known, err, fk.splitsN.Load())
	}
	if st := f2.stats(); st.SplitsCalls != 0 || st.SplitsHits != 1 {
		t.Errorf("cached stats %+v", st)
	}

	// another range is another listing: a miss
	if _, _, err := f2.splits(context.Background(), "2026-09-18", "2026-10-07"); err != nil || fk.splitsN.Load() != 2 {
		t.Errorf("a different as-of date must refetch: err %v calls %d", err, fk.splitsN.Load())
	}

	// -dry: the cache answers, a miss is "unknown", never a call
	fd, _ := newFetcher(nil, dir)
	if got, known, err := fd.splits(context.Background(), "2026-09-18", "2026-10-06"); err != nil || !known || len(got) != 1 {
		t.Errorf("dry with a cache: %v %v %v", got, known, err)
	}
	if got, known, err := fd.splits(context.Background(), "2026-01-01", "2026-10-06"); err != nil || known || got != nil {
		t.Errorf("dry without a cache: %v %v %v, want unknown and no error", got, known, err)
	}

	// a corrupt cache file is a miss
	if err := writeAtomic(f.cache.splitsPath("2026-09-18", "2026-10-08"), func(w io.Writer) error { _, e := w.Write([]byte("{nope")); return e }); err != nil {
		t.Fatal(err)
	}
	if _, known, err := f.splits(context.Background(), "2026-09-18", "2026-10-08"); err != nil || !known || fk.splitsN.Load() != 3 {
		t.Errorf("corrupt cache: known=%v err=%v calls=%d, want a refetch", known, err, fk.splitsN.Load())
	}
}

func TestSplitsFailureStopsTheRunAndSwitchOffRunsWithout(t *testing.T) {
	fk := world(t)
	fk.splitsErr = &feed.HTTPError{Status: 500, Msg: "boom"}
	f, _ := newFetcher(fk, t.TempDir())
	_, _, err := run(context.Background(), f, opts())
	if err == nil || !strings.Contains(err.Error(), "splits") {
		t.Fatalf("err = %v, want the splits failure to stop the run (a silent fallback would flatter the trader)", err)
	}

	// -splits=false: the run goes on, and says plainly that nothing was corrected
	fk2 := world(t)
	fk2.splitsErr = errors.New("must not be called")
	f2, _ := newFetcher(fk2, t.TempDir())
	o := opts()
	o.SkipSplits = true
	rep, _, err := run(context.Background(), f2, o)
	if err != nil {
		t.Fatal(err)
	}
	d := rep.Meta.Data
	if d.SplitsKnown || fk2.splitsN.Load() != 0 || !strings.Contains(d.SplitsNote, "-splits=false") {
		t.Errorf("switched off: known=%v calls=%d note=%q", d.SplitsKnown, fk2.splitsN.Load(), d.SplitsNote)
	}
	if md := rep.Markdown(); !strings.Contains(md, "NOT CORRECTED") {
		t.Errorf("report.md must say the splits were not corrected")
	}
}

// ─── End to end: a name that reverse-split after its day ─────────────────

// worldSplit is world() with BEAT replaced by SPLT as the feed shows a name that
// reverse-split 1-for-20 on 2026-10-06 (after the test day 2026-10-05): prices
// x20, volumes / 20, on the daily bars and the minute bars alike.
func worldSplit(t testing.TB) *fake {
	t.Helper()
	f := world(t)
	adjDaily := func(b core.DailyBar) core.DailyBar {
		return core.DailyBar{Date: b.Date, O: b.O * 20, H: b.H * 20, L: b.L * 20, C: b.C * 20, V: b.V / 20}
	}
	for _, m := range f.grouped {
		m["SPLT"] = adjDaily(m["BEAT"])
		delete(m, "BEAT")
	}
	var adj []core.Bar
	for _, b := range beatBars(t) {
		adj = append(adj, core.Bar{T: b.T, O: b.O * 20, H: b.H * 20, L: b.L * 20, C: b.C * 20, V: b.V / 20})
	}
	f.bars = map[string][]core.Bar{"SPLT": adj}
	f.splits = []backtest.StockSplit{
		{Sym: "SPLT", Date: "2026-10-06", From: 20, To: 1},
		{Sym: "OLD", Date: "2026-09-01", From: 20, To: 1}, // long before the sample: irrelevant
	}
	return f
}

func TestEndToEndUnadjustsANameThatSplitAfterItsDay(t *testing.T) {
	// the reference run: BEAT itself, never split
	refFake := world(t)
	fRef, _ := newFetcher(refFake, t.TempDir())
	refRep, refRes, err := run(context.Background(), fRef, opts())
	if err != nil {
		t.Fatal(err)
	}
	ref := refRes[0].Trades[0]

	fk := worldSplit(t)
	dir := t.TempDir()
	f, _ := newFetcher(fk, dir)
	rep, res, err := run(context.Background(), f, opts())
	if err != nil {
		t.Fatal(err)
	}
	if fk.splitsFrom != "2026-09-18" || fk.splitsTo != "2026-10-06" {
		t.Errorf("listing asked for %s..%s, want the first history day to the as-of date 2026-09-18..2026-10-06", fk.splitsFrom, fk.splitsTo)
	}
	d := rep.Meta.Data
	if !d.SplitsKnown || d.SplitsInWindow != 2 || d.SplitsCalls != 1 || d.SplitsFrom != "2026-09-18" || d.SplitsAsOf != "2026-10-06" {
		t.Errorf("data stats %+v", d)
	}
	p := rep.Meta.Prep[0]
	if p.Fetched != 1 || p.SplitAdjusted != 1 || p.Admitted != 1 || p.Signals != 1 || p.Prefilter.Passed != 2 {
		t.Errorf("funnel %+v", p)
	}
	if len(res[0].Trades) != 1 {
		t.Fatalf("V1: %d trades", len(res[0].Trades))
	}
	tr := res[0].Trades[0]
	if tr.Sym != "SPLT" || tr.SplitFactor != 20 {
		t.Errorf("trade %+v", tr)
	}
	// the same trade as BEAT's own: the real basis, to the share and the cent
	if tr.Shares != ref.Shares || tr.Shares != 1035 || !approxF(tr.Trigger, ref.Trigger, 1e-9) || !approxF(tr.EntryRaw, ref.EntryRaw, 1e-9) || !approxF(tr.ExitRaw, ref.ExitRaw, 1e-9) {
		t.Errorf("split run: %d shares trigger %.7f entry %.7f exit %.7f; BEAT itself: %d, %.7f, %.7f, %.7f", tr.Shares, tr.Trigger, tr.EntryRaw, tr.ExitRaw, ref.Shares, ref.Trigger, ref.EntryRaw, ref.ExitRaw)
	}
	if !approxF(tr.Commission+tr.Fees, ref.Commission+ref.Fees, 1e-6) || !approxF(tr.Net, ref.Net, 1e-6) {
		t.Errorf("costs %.4f net %.4f; BEAT itself %.4f / %.4f", tr.Commission+tr.Fees, tr.Net, ref.Commission+ref.Fees, ref.Net)
	}
	if tr.SplitCostGap < 5 {
		t.Errorf("SplitCostGap = %.2f: the adjusted prices would have hidden most of the ~$%.0f of costs", tr.SplitCostGap, tr.Commission+tr.Fees)
	}
	si := rep.Variants[0].SplitImpact
	if si.Trades != 1 || si.CostGap != tr.SplitCostGap {
		t.Errorf("split impact %+v", si)
	}
	if refRep.Variants[0].SplitImpact.Trades != 0 {
		t.Errorf("BEAT never split: %+v", refRep.Variants[0].SplitImpact)
	}

	// the same data with the listing switched off: the old behaviour, the costs the review found
	fOff, _ := newFetcher(worldSplit(t), t.TempDir())
	o := opts()
	o.SkipSplits = true
	_, offRes, err := run(context.Background(), fOff, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(offRes[0].Trades) != 1 {
		t.Fatalf("without the correction: %d trades, want the same one trade on the adjusted tape", len(offRes[0].Trades))
	}
	off := offRes[0].Trades[0]
	if off.SplitFactor != 1 || off.Shares*10 > tr.Shares || (off.Commission+off.Fees)*5 > tr.Commission+tr.Fees {
		t.Errorf("without the correction: %d shares costing %.2f, vs %d shares costing %.2f: the adjusted basis should be many times smaller", off.Shares, off.Commission+off.Fees, tr.Shares, tr.Commission+tr.Fees)
	}
	// ... and the gap the corrected trade reports is what that understatement was (within the share rounding)
	if !approxF(tr.SplitCostGap, tr.Commission+tr.Fees-(off.Commission+off.Fees), 0.15) {
		t.Errorf("SplitCostGap %.2f vs the actual understatement %.2f", tr.SplitCostGap, tr.Commission+tr.Fees-(off.Commission+off.Fees))
	}

	// the cache holds the listing: a rerun, and a dry run, make no call and give the same trade
	callsBefore := fk.splitsN.Load()
	f2, _ := newFetcher(fk, dir)
	rep2, res2, err := run(context.Background(), f2, opts())
	if err != nil || fk.splitsN.Load() != callsBefore || rep2.Meta.Data.SplitsHits != 1 || rep2.Meta.Data.SplitsCalls != 0 || res2[0].Trades[0].Net != tr.Net {
		t.Errorf("rerun: err %v, splits calls %d -> %d, hits %d", err, callsBefore, fk.splitsN.Load(), rep2.Meta.Data.SplitsHits)
	}
	f3, _ := newFetcher(nil, dir)
	rep3, res3, err := run(context.Background(), f3, opts())
	if err != nil || !rep3.Meta.Data.SplitsKnown || res3[0].Trades[0].Net != tr.Net {
		t.Errorf("dry rerun: err %v known=%v", err, rep3.Meta.Data.SplitsKnown)
	}
	// a dry run for another as-of date has no listing in the cache: unknown, loudly, but it runs
	o3 := opts()
	o3.AsOf = "2026-10-09"
	f4, _ := newFetcher(nil, dir)
	rep4, _, err := run(context.Background(), f4, o3)
	if err != nil || rep4.Meta.Data.SplitsKnown || !strings.Contains(rep4.Meta.Data.SplitsNote, "-asof") {
		t.Errorf("dry other as-of: err %v known=%v note %q", err, rep4.Meta.Data.SplitsKnown, rep4.Meta.Data.SplitsNote)
	}
	// the report says what it did, and carries no key
	md := rep.Markdown()
	for _, want := range []string{"un-adjusted", "split after the day", "SPLT"} {
		if want == "SPLT" {
			continue // the report lists counts, not symbols
		}
		if !strings.Contains(md, want) {
			t.Errorf("report.md lacks %q", want)
		}
	}
	var buf bytes.Buffer
	if err := backtest.WriteTradesCSV(&buf, res); err != nil || !strings.Contains(buf.String(), "split_factor") {
		t.Errorf("trades.csv: %v", err)
	}
}

func approxF(a, b, tol float64) bool { return a-b <= tol && b-a <= tol }

// ─── Flags ───────────────────────────────────────────────────────────────

func TestSplitFlags(t *testing.T) {
	var out, errb bytes.Buffer
	if code := realMain([]string{"-dry", "-asof", "tomorrow"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "-asof") {
		t.Errorf("-asof tomorrow: exit %d %q", code, errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := realMain([]string{"-h"}, &out, &errb); code != 0 || !strings.Contains(errb.String(), "-splits") || !strings.Contains(errb.String(), "-asof") {
		t.Errorf("-h: exit %d, usage lacks the split flags: %q", code, errb.String())
	}
	// -splits=false parses (a dry run on an empty cache still ends in the clean not-enough-days error)
	out.Reset()
	errb.Reset()
	dir := t.TempDir()
	if code := realMain([]string{"-dry", "-splits=false", "-days", "3", "-cache", dir + "/c", "-out", dir + "/o"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "not enough") {
		t.Errorf("-splits=false: exit %d %q", code, errb.String())
	}
}
