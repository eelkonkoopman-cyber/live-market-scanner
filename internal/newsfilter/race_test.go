package newsfilter

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

var (
	base = time.Date(2026, 10, 30, 13, 0, 0, 0, time.UTC).UnixMilli()
	min1 = time.Minute.Milliseconds()
	hr1  = time.Hour.Milliseconds()
	day1 = 24 * hr1
)

func rt(id, title, source string, tickers []string, pub, recv int64) RTPRArticle {
	return RTPRArticle{ID: id, Title: title, Source: source, Tickers: tickers, PublishedMs: pub, ReceivedMs: recv}
}

func nf(id, title, src string, syms []string, pub, ms int64) Record {
	return Record{ID: id, Title: title, Src: src, Syms: syms, Pub: pub, Ms: ms}
}

func tk(s ...string) []string { return s }

func pairsOf(m Matching) [][2]int {
	var out [][2]int
	for _, p := range m.Pairs {
		out = append(out, [2]int{p.R, p.N})
	}
	return out
}

func TestTitleKey(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Acme Corp. Announces Q3 Results", "acme corp announces q3 results"},
		{"  --ACME   Corp, announces Q3 results!!  ", "acme corp announces q3 results"},
		{"Levi &amp; Korsinsky", "levi korsinsky"},
		{"Levi & Korsinsky", "levi korsinsky"},
		{"Roblox \u201cCorp\u201d Investors \u2014 Act Now", "roblox corp investors act now"},
		{`Roblox "Corp" Investors - Act Now`, "roblox corp investors act now"},
		{"It\u2019s a deal", "it s a deal"},
		{"It's a deal", "it s a deal"},
		{"", ""},
		{"!!! ... ---", ""},
		{"Zürich 北京 Q4", "zürich 北京 q4"},
	} {
		if got := TitleKey(c.in); got != c.want {
			t.Errorf("TitleKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	long := strings.Repeat("abcdefghij ", 20)
	k := TitleKey(long)
	if len([]rune(k)) > 70 || k != strings.TrimSpace(k) {
		t.Errorf("long key = %q (%d)", k, len([]rune(k)))
	}
	// Anything past the first 70 characters is noise.
	a := TitleKey(strings.Repeat("x", 70) + " - Acme Corp (NASDAQ: ACME)")
	b := TitleKey(strings.Repeat("x", 70) + " | Business Wire")
	if a != b {
		t.Errorf("tails must not matter: %q vs %q", a, b)
	}
}

func TestWireMapping(t *testing.T) {
	for in, want := range map[string]string{
		"prNewswire": "prnewswire", "PRNEWSWIRE": "prnewswire", "globenewswire": "globenewswire",
		"businesswire": "businesswire", "accesswire": "accesswire",
		"benzinga": "", "reuters": "", "sec-api": "", "": "", "newsfile": "",
	} {
		if got := WireOfNewsfilter(in); got != want {
			t.Errorf("WireOfNewsfilter(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"PR Newswire": "prnewswire", "Business Wire": "businesswire", "BUSINESS WIRE": "businesswire",
		"GlobeNewswire": "globenewswire", "Globe Newswire": "globenewswire", "GlobeNewswire Inc.": "globenewswire",
		"ACCESSWIRE": "accesswire", "ACCESS Newswire": "accesswire", "Accesswire": "accesswire",
		"Newsfile Corp": "", "Newsfile Corp.": "", "Canada Newswire": "", "": "", "Benzinga": "",
	} {
		if got := WireOfRTPR(in); got != want {
			t.Errorf("WireOfRTPR(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMatchExactAndLeadSign(t *testing.T) {
	r := []RTPRArticle{rt("r1", "Acme Announces Results", "Business Wire", tk("ACME"), base, base+5000)}
	n := []Record{nf("n1", "Acme Announces Results", "businesswire", tk("ACME"), base, base+2000)}
	m := Match(r, n)
	if len(m.Pairs) != 1 || m.Pairs[0].Lead != 3000 {
		t.Fatalf("pairs = %+v: Newsfilter arrived 3 s before RTPR, so the lead is +3000", m.Pairs)
	}
	if len(m.RTPROnly) != 0 || len(m.NFOnly) != 0 {
		t.Fatalf("leftovers: %+v", m)
	}
	// RTPR first gives a negative lead.
	n[0].Ms = base + 9000
	if m := Match(r, n); m.Pairs[0].Lead != -4000 {
		t.Fatalf("lead = %d, want -4000", m.Pairs[0].Lead)
	}
}

func TestMatchTitleNoise(t *testing.T) {
	r := []RTPRArticle{
		rt("r1", "Securities Lawsuit Alert: Roblox Corporation (RBLX) Investors - Contact Levi & Korsinsky Before August 7, 2026", "ACCESSWIRE", tk("RBLX"), base, base+1),
		rt("r2", "Acme\u2019s \u201cBig\u201d Deal \u2014 Webcast Tonight", "PR Newswire", tk("ACME"), base, base+1),
	}
	n := []Record{
		nf("n1", "Securities Lawsuit Alert: Roblox Corporation (RBLX) Investors - Contact Levi &amp; Korsinsky Before August 7, 2026", "accesswire", tk("RBLX"), base, base+1),
		nf("n2", `ACME's "Big" deal - webcast tonight`, "prNewswire", tk("ACME"), base, base+1),
	}
	if got := pairsOf(Match(r, n)); !reflect.DeepEqual(got, [][2]int{{0, 0}, {1, 1}}) {
		t.Fatalf("pairs = %v", got)
	}
}

func TestMatchBoilerplateRepeatedAcrossDaysDoesNotPair(t *testing.T) {
	const title = "Investor Alert: Pomerantz Law Firm Investigates Claims On Behalf of Investors"
	r := []RTPRArticle{
		rt("r-yesterday", title, "Business Wire", tk("ACME"), base-day1, base-day1+1000),
		rt("r-today", title, "Business Wire", tk("ACME"), base, base+1000),
	}
	n := []Record{nf("n-today", title, "businesswire", tk("ACME"), base+500, base+400)}
	m := Match(r, n)
	if got := pairsOf(m); !reflect.DeepEqual(got, [][2]int{{1, 0}}) {
		t.Fatalf("pairs = %v, want only today's article", got)
	}
	if !reflect.DeepEqual(m.RTPROnly, []int{0}) {
		t.Fatalf("yesterday's copy is RTPR-only: %+v", m)
	}
	// Just inside and just outside the six hour window.
	n2 := []Record{nf("n", title, "businesswire", tk("ACME"), base+6*hr1, base+6*hr1)}
	one := []RTPRArticle{rt("r", title, "Business Wire", tk("ACME"), base, base)}
	if len(Match(one, n2).Pairs) != 1 {
		t.Error("exactly six hours apart still pairs")
	}
	n2[0].Pub++
	if len(Match(one, n2).Pairs) != 0 {
		t.Error("beyond six hours must not pair")
	}
}

func TestMatchSameTitleDifferentCompanyDoesNotPair(t *testing.T) {
	const title = "Company Announces Third Quarter 2026 Financial Results"
	r := []RTPRArticle{rt("r", title, "GlobeNewswire", tk("AAA"), base, base)}
	n := []Record{nf("n", title, "globenewswire", tk("BBB"), base, base)}
	m := Match(r, n)
	if len(m.Pairs) != 0 || len(m.RTPROnly) != 1 || len(m.NFOnly) != 1 {
		t.Fatalf("different tickers must not pair: %+v", m)
	}
}

func TestMatchTickerRules(t *testing.T) {
	const title = "Same Headline Everywhere"
	cases := []struct {
		name  string
		r, n  []string
		pairs int
	}{
		{"one shared of several", tk("AAA", "BBB", "CCC"), tk("ZZZ", "CCC"), 1},
		{"no overlap", tk("AAA", "BBB"), tk("CCC", "DDD"), 0},
		{"case and prefix are ignored", tk("nasdaq:acme"), tk("$ACME"), 1},
		{"RTPR has none", nil, tk("AAA"), 1},
		{"Newsfilter has none", tk("AAA"), nil, 1},
		{"neither has any", nil, nil, 1},
		{"blank tickers count as none", tk(" ", ""), tk("AAA"), 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Match([]RTPRArticle{rt("r", title, "PR Newswire", c.r, base, base)},
				[]Record{nf("n", title, "prnewswire", c.n, base, base)})
			if len(m.Pairs) != c.pairs {
				t.Fatalf("pairs = %d, want %d", len(m.Pairs), c.pairs)
			}
		})
	}
}

func TestMatchEachArticlePairsOnceNearestFirst(t *testing.T) {
	const title = "Quarterly Dividend Declared"
	// Slices are deliberately in the "wrong" order: first-come pairing would
	// cross them, nearest-publication-first must not.
	r := []RTPRArticle{
		rt("r-noon", title, "Business Wire", tk("ACME"), base+2*hr1, base+2*hr1+100),
		rt("r-ten", title, "Business Wire", tk("ACME"), base, base+100),
	}
	n := []Record{
		nf("n-ten", title, "businesswire", tk("ACME"), base+min1, base+50),
		nf("n-noon", title, "businesswire", tk("ACME"), base+2*hr1+min1, base+2*hr1+50),
	}
	if got := pairsOf(Match(r, n)); !reflect.DeepEqual(got, [][2]int{{0, 1}, {1, 0}}) {
		t.Fatalf("pairs = %v, want noon<->noon and ten<->ten", got)
	}
	// One RTPR article, two candidates: it pairs with the nearer one only.
	m := Match(r[1:], n)
	if got := pairsOf(m); !reflect.DeepEqual(got, [][2]int{{0, 0}}) || !reflect.DeepEqual(m.NFOnly, []int{1}) {
		t.Fatalf("one-to-one broken: %+v", m)
	}
}

func TestMatchWithoutPublicationTimesUsesArrival(t *testing.T) {
	const title = "No Publication Time Here"
	r := []RTPRArticle{rt("r", title, "Business Wire", tk("ACME"), 0, base+2000)}
	n := []Record{nf("n", title, "businesswire", tk("ACME"), 0, base)}
	if len(Match(r, n).Pairs) != 1 {
		t.Fatal("unknown publication times fall back to arrival times")
	}
	n[0].Ms = base - 7*hr1
	if len(Match(r, n).Pairs) != 0 {
		t.Fatal("arrival times far apart must not pair either")
	}
	// One side known, the other not: arrival times decide.
	r[0].PublishedMs = base
	n[0].Ms = base
	if len(Match(r, n).Pairs) != 1 {
		t.Fatal("one unknown publication time still pairs by arrival")
	}
}

func TestMatchEmptyTitlesNeverPair(t *testing.T) {
	m := Match([]RTPRArticle{rt("r", "", "x", nil, base, base)}, []Record{nf("n", "!!!", "x", nil, base, base)})
	if len(m.Pairs) != 0 || len(m.RTPROnly) != 1 || len(m.NFOnly) != 1 {
		t.Fatalf("%+v", m)
	}
}

func TestMatchRTPROnlyAndNewsfilterOnly(t *testing.T) {
	r := []RTPRArticle{
		rt("both", "Shared Story", "Business Wire", tk("A"), base, base),
		rt("rtpr-only", "Only RTPR Has This", "Business Wire", tk("A"), base, base),
	}
	n := []Record{
		nf("both", "Shared Story", "businesswire", tk("A"), base, base),
		nf("nf-only", "Only Newsfilter Has This", "businesswire", tk("A"), base, base),
	}
	m := Match(r, n)
	if len(m.Pairs) != 1 || !reflect.DeepEqual(m.RTPROnly, []int{1}) || !reflect.DeepEqual(m.NFOnly, []int{1}) {
		t.Fatalf("%+v", m)
	}
}

// --- Analyze ---

func wideCov() Coverage {
	return Coverage{Intervals: []Interval{{From: base - 30*day1, To: base + 30*day1}}}
}

func TestAnalyzeKeepsNonWireSourcesOutOfTheCoverageFigures(t *testing.T) {
	r := []RTPRArticle{
		rt("r1", "Wire Story One", "Business Wire", tk("A"), base, base+1000),
		rt("r2", "Newsfile Only Story", "Newsfile Corp", tk("B"), base, base+1000),
		rt("r3", "Another Newsfile Story", "Newsfile Corp.", tk("C"), base, base+1000),
		rt("r4", "Odd Wire Story", "Some Other Wire", tk("D"), base, base+1000),
	}
	n := []Record{
		nf("n1", "Wire Story One", "businesswire", tk("A"), base, base+400),
		nf("n2", "Newsfile Only Story", "benzinga", tk("B"), base, base+400), // a rewrite, not the wire
		nf("n3", "Reuters Item", "reuters", tk("E"), base, base+400),
		nf("n4", "Only On Newsfilter", "globenewswire", tk("F"), base, base+400),
	}
	rep := Analyze(r, n, wideCov(), Options{}, base-hr1, base+hr1)
	if rep.RTPRTotal != 4 || rep.RTPRWire != 1 || rep.NotListedTotal != 3 {
		t.Errorf("RTPR split wrong: %+v", rep)
	}
	if rep.NotListed["Newsfile Corp"] != 1 || rep.NotListed["Newsfile Corp."] != 1 || rep.NotListed["Some Other Wire"] != 1 {
		t.Errorf("not-on-list = %v", rep.NotListed)
	}
	if rep.NFTotal != 4 || rep.NFWire != 2 || rep.NFOther["benzinga"] != 1 || rep.NFOther["reuters"] != 1 {
		t.Errorf("Newsfilter split wrong: %+v", rep)
	}
	if rep.Matched != 1 || rep.RTPROnly != 0 || rep.NFOnly != 1 {
		t.Errorf("matched %d, RTPR-only %d, Newsfilter-only %d: Newsfile must not count as a miss", rep.Matched, rep.RTPROnly, rep.NFOnly)
	}
	if got := rep.NFCoverage(); got != 100 {
		t.Errorf("Newsfilter coverage of RTPR = %v", got)
	}
	if got := rep.RTPRCoverage(); got != 50 {
		t.Errorf("RTPR coverage of Newsfilter = %v", got)
	}
}

func TestAnalyzePerWireCoverage(t *testing.T) {
	var r []RTPRArticle
	var n []Record
	add := func(wireRTPR, wireNF, id string, inR, inN bool) {
		title := "Story " + id
		if inR {
			r = append(r, rt("r"+id, title, wireRTPR, tk("T"+id), base, base+1000))
		}
		if inN {
			n = append(n, nf("n"+id, title, wireNF, tk("T"+id), base, base+500))
		}
	}
	add("Business Wire", "businesswire", "1", true, true)
	add("Business Wire", "businesswire", "2", true, true)
	add("Business Wire", "businesswire", "3", true, false) // RTPR-only
	add("PR Newswire", "prNewswire", "4", true, true)
	add("PR Newswire", "prNewswire", "5", false, true) // Newsfilter-only
	add("PR Newswire", "prNewswire", "6", false, true)
	add("GlobeNewswire", "globenewswire", "7", true, false)
	add("ACCESSWIRE", "accesswire", "8", true, true)
	rep := Analyze(r, n, wideCov(), Options{}, base-hr1, base+hr1)

	got := map[string]WireStat{}
	for _, w := range rep.Wires {
		got[w.Wire] = w
	}
	if w := got["businesswire"]; w.MatchedR != 2 || w.RTPROnly != 1 || w.NFOnly != 0 || w.RTPR != 3 || w.NF != 2 {
		t.Errorf("businesswire = %+v", w)
	}
	if w := got["prnewswire"]; w.MatchedR != 1 || w.RTPROnly != 0 || w.NFOnly != 2 || w.RTPR != 1 || w.NF != 3 {
		t.Errorf("prnewswire = %+v", w)
	}
	if w := got["globenewswire"]; w.MatchedR != 0 || w.RTPROnly != 1 || w.NF != 0 {
		t.Errorf("globenewswire = %+v", w)
	}
	if w := got["accesswire"]; w.MatchedR != 1 || w.MatchedN != 1 {
		t.Errorf("accesswire = %+v", w)
	}
	if rep.Matched != 4 || rep.RTPROnly != 2 || rep.NFOnly != 2 || rep.WireMismatch != 0 {
		t.Errorf("totals: %+v", rep)
	}
	if len(rep.Wires) != 4 || rep.Wires[0].Wire != "prnewswire" {
		t.Errorf("wire order = %+v", rep.Wires)
	}
}

func TestAnalyzeWireMismatchIsCounted(t *testing.T) {
	r := []RTPRArticle{rt("r", "Cross Posted Release", "Business Wire", tk("A"), base, base)}
	n := []Record{nf("n", "Cross Posted Release", "prnewswire", tk("A"), base, base)}
	if rep := Analyze(r, n, wideCov(), Options{}, base-hr1, base+hr1); rep.Matched != 1 || rep.WireMismatch != 1 {
		t.Fatalf("%+v", rep)
	}
}

// pairsWithLeads builds one matched pair per lead (rtpr.received - newsfilter.received).
func pairsWithLeads(tickerFor func(i int) string, leads ...int64) ([]RTPRArticle, []Record) {
	var r []RTPRArticle
	var n []Record
	for i, l := range leads {
		title := fmt.Sprintf("Distinct Story Number %d", i)
		tkr := tickerFor(i)
		r = append(r, rt(fmt.Sprint("r", i), title, "Business Wire", tk(tkr), base, base+l))
		n = append(n, nf(fmt.Sprint("n", i), title, "businesswire", tk(tkr), base, base))
	}
	return r, n
}

func ticker(i int) string { return fmt.Sprintf("T%d", i) }

func TestAnalyzeLeadPercentilesAndShareFirst(t *testing.T) {
	r, n := pairsWithLeads(ticker, 20000, -5000, 500, 0, 3000, -1000, 800, 200, 6000, 1500)
	rep := Analyze(r, n, wideCov(), Options{}, base-hr1, base+hr1)
	l := rep.Lead
	// sorted: -5000 -1000 0 200 500 800 1500 3000 6000 20000 (nearest rank)
	if l.N != 10 || l.P10 != -5000 || l.P50 != 500 || l.P90 != 6000 {
		t.Errorf("lead = %+v", l)
	}
	if l.NFFirst != 7 { // strictly positive; the 0 is a tie, not a win
		t.Errorf("NFFirst = %d, want 7", l.NFFirst)
	}
	if l.Outliers != 0 { // 20 s is the largest gap here, nowhere near ten minutes
		t.Errorf("outliers = %d", l.Outliers)
	}
	r2, n2 := pairsWithLeads(ticker, 11*min1, -12*min1, 1000)
	if o := Analyze(r2, n2, wideCov(), Options{}, base-hr1, base+hr1).Lead.Outliers; o != 2 {
		t.Errorf("outliers = %d, want 2", o)
	}
	if got := percentile(nil, 0.5); got != 0 {
		t.Errorf("percentile(nil) = %d", got)
	}
	if got := percentile([]int64{7}, 0.9); got != 7 {
		t.Errorf("percentile single = %d", got)
	}
}

func TestAnalyzeOwnStocksPriceCut(t *testing.T) {
	prices := map[string]float64{"LOW": 5, "EDGE": 20, "HIGH": 50, "ZERO": 0}
	names := []string{"LOW", "EDGE", "HIGH", "NOPX", "ZERO", "LOW"}
	r, n := pairsWithLeads(func(i int) string { return names[i] }, 1000, 2000, 3000, 4000, 5000, 6000)
	rep := Analyze(r, n, wideCov(), Options{MaxPx: 20, Prices: prices}, base-hr1, base+hr1)
	// LOW (5), EDGE (20, "at or under"), LOW again: three pairs; HIGH, NOPX (no price), ZERO (bad price) are out.
	if rep.Own.N != 3 || rep.Lead.N != 6 {
		t.Fatalf("own %d of %d, want 3 of 6", rep.Own.N, rep.Lead.N)
	}
	if rep.Own.P50 != 2000 || rep.Own.NFFirst != 3 {
		t.Errorf("own lead = %+v", rep.Own)
	}
	if none := Analyze(r, n, wideCov(), Options{MaxPx: 20}, base-hr1, base+hr1); none.Own.N != 0 {
		t.Errorf("without prices nothing is an own stock: %+v", none.Own)
	}
	if none := Analyze(r, n, wideCov(), Options{Prices: prices}, base-hr1, base+hr1); none.Own.N != 0 {
		t.Errorf("without a cut nothing is an own stock: %+v", none.Own)
	}
}

func TestOwnStockUsesSharedTickersWhenBothSidesHaveThem(t *testing.T) {
	o := Options{MaxPx: 20, Prices: map[string]float64{"CHEAP": 3, "RICH": 300}}
	if o.isOwn(tk("RICH", "CHEAP"), tk("RICH")) {
		t.Error("only the shared ticker (RICH) counts when both sides name tickers")
	}
	if !o.isOwn(tk("RICH", "CHEAP"), tk("CHEAP", "OTHER")) {
		t.Error("CHEAP is shared and cheap")
	}
	if !o.isOwn(nil, tk("cheap")) || !o.isOwn(tk("$CHEAP"), nil) {
		t.Error("with one side blank the other side's tickers decide")
	}
	if o.isOwn(nil, nil) {
		t.Error("no tickers at all is never an own stock")
	}
}

func TestAnalyzeExcludesRTPROnlyArticlesFromDisconnectedGaps(t *testing.T) {
	cov := Coverage{Intervals: []Interval{{From: base - hr1, To: base + 10*min1}, {From: base + 20*min1, To: base + hr1}}}
	r := []RTPRArticle{
		rt("matched-in-gap", "Matched In The Gap", "Business Wire", tk("M"), base+14*min1, base+15*min1),
		rt("recv-in-gap", "A Received In Gap", "Business Wire", tk("A"), base+9*min1, base+11*min1),
		rt("pub-in-gap", "B Published In Gap", "Business Wire", tk("B"), base+19*min1, base+21*min1),
		rt("both-in-gap", "C Both In Gap", "Business Wire", tk("C"), base+14*min1, base+15*min1),
		rt("covered", "D Covered", "Business Wire", tk("D"), base+29*min1, base+30*min1),
		rt("no-pub", "E No Publication Time", "Business Wire", tk("E"), 0, base+5*min1),
		rt("no-pub-gap", "F No Pub In Gap", "Business Wire", tk("F"), 0, base+15*min1),
	}
	n := []Record{nf("n", "Matched In The Gap", "businesswire", tk("M"), base+14*min1, base+14*min1)}
	rep := Analyze(r, n, cov, Options{}, base-hr1, base+hr1)
	if rep.Matched != 1 {
		t.Errorf("a pair is real evidence wherever it falls: matched = %d", rep.Matched)
	}
	if rep.ExcludedGap != 4 {
		t.Errorf("excluded = %d, want 4 (recv-in-gap, pub-in-gap, both-in-gap, no-pub-gap)", rep.ExcludedGap)
	}
	if rep.RTPROnly != 2 {
		t.Errorf("RTPR-only = %d, want 2 (covered, no-pub)", rep.RTPROnly)
	}
	var out strings.Builder
	rep.Write(&out)
	if !strings.Contains(out.String(), "4 RTPR articles excluded") {
		t.Errorf("the report must print how many were excluded:\n%s", out.String())
	}
}

func TestAnalyzeVerdictNeedsTwoHundredPairs(t *testing.T) {
	leads := make([]int64, 199)
	for i := range leads {
		leads[i] = 1500
	}
	r, n := pairsWithLeads(ticker, leads...)
	rep := Analyze(r, n, wideCov(), Options{}, base-hr1, base+hr1)
	if rep.Matched != 199 || !strings.HasPrefix(rep.Verdict, "not enough data yet") || !strings.Contains(rep.Verdict, "199") {
		t.Fatalf("verdict = %q", rep.Verdict)
	}

	leads = append(leads, 1500)
	r, n = pairsWithLeads(ticker, leads...)
	rep = Analyze(r, n, wideCov(), Options{}, base-hr1, base+hr1)
	if strings.Contains(rep.Verdict, "not enough") || !strings.Contains(rep.Verdict, "200 matched pairs") ||
		!strings.Contains(rep.Verdict, "Newsfilter is usually earlier") || !strings.Contains(rep.Verdict, "100.0%") {
		t.Fatalf("verdict = %q", rep.Verdict)
	}

	for want, lead := range map[string]int64{"RTPR is usually earlier": -2000, "about level on speed": 100} {
		for i := range leads {
			leads[i] = lead
		}
		r, n = pairsWithLeads(ticker, leads...)
		if v := Analyze(r, n, wideCov(), Options{}, base-hr1, base+hr1).Verdict; !strings.Contains(v, want) {
			t.Errorf("verdict %q lacks %q", v, want)
		}
	}
}

func TestAnalyzeEmptyInputs(t *testing.T) {
	rep := Analyze(nil, nil, Coverage{}, Options{MaxPx: 20}, base-hr1, base)
	if rep.Matched != 0 || !strings.Contains(rep.Verdict, "not enough data yet") {
		t.Fatalf("%+v", rep)
	}
	var out strings.Builder
	rep.Write(&out) // must not panic or divide by zero
	if !strings.Contains(out.String(), "n/a") || !strings.Contains(out.String(), "Verdict: not enough data yet") {
		t.Errorf("empty report:\n%s", out.String())
	}
}

func TestReportWriteLayout(t *testing.T) {
	r, n := pairsWithLeads(ticker, 1500, -500, 3000)
	n = append(n, nf("x", "Reuters Thing", "reuters", tk("Q"), base, base))
	r = append(r, rt("y", "Newsfile Thing", "Newsfile Corp", tk("Q"), base, base))
	rep := Analyze(r, n, Coverage{
		Intervals: []Interval{{From: base - hr1, To: base + hr1}}, Reconnects: 3,
		DisconnectedMs: 130_000, OfflineMs: 3 * hr1,
	}, Options{MaxPx: 20, Prices: map[string]float64{"T0": 4}}, base-hr1, base+hr1)
	var b strings.Builder
	rep.Write(&b)
	out := b.String()
	for _, want := range []string{
		"Newsfilter vs RTPR", "positive = Newsfilter first", "Newsfile Corp 1", "reuters 1",
		"Matched pairs:        3", "RTPR-only 0", "Newsfilter-only 0",
		"Business Wire", "PR Newswire", "GlobeNewswire", "Accesswire",
		"lead p10", "p50", "p90", "own stocks (latest close <= $20.00)",
		"3 reconnects", "disconnected 2m10s", "recorder not running 3h00m",
		"Verdict: not enough data yet (3 matched pairs, need 200).",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
	for in, want := range map[int64]string{
		3000: "+3.00s", -500: "-0.50s", 0: "+0.00s", 125_000: "+2m05s", -3 * hr1: "-3h00m",
	} {
		if got := fmtLead(in); got != want {
			t.Errorf("fmtLead(%d) = %q, want %q", in, got, want)
		}
	}
}

// --- the verdict line is what a cancel decision is read from ---

// The coverage share is computed after dropping articles that fell in a
// Newsfilter gap and RTPR articles on wires Newsfilter does not list; the
// verdict has to say so, with the numbers, or it claims more than the data shows.
func TestVerdictQualifiesTheCoverageFigures(t *testing.T) {
	leads := make([]int64, 925)
	for i := range leads {
		leads[i] = 1500
	}

	// 75 Newsfile articles (7.5% of RTPR's) that Newsfilter does not list at all.
	r, n := pairsWithLeads(ticker, leads...)
	for i := 0; i < 75; i++ {
		r = append(r, rt(fmt.Sprint("nf", i), fmt.Sprintf("Low Priced Canadian Name %d Drills", i), "Newsfile Corp.", tk(fmt.Sprintf("C%d", i)), base, base+1000))
	}
	rep := Analyze(r, n, wideCov(), Options{}, base-hr1, base+hr1)
	if rep.NotListedTotal != 75 || rep.NFCoverage() != 100 {
		t.Fatalf("setup: not listed %d, coverage %v", rep.NotListedTotal, rep.NFCoverage())
	}
	for _, want := range []string{
		"100.0% of RTPR's articles on the four shared wires while it was connected",
		"75, 7.5% of RTPR, are on wires Newsfilter does not list",
		"0 more excluded",
	} {
		if !strings.Contains(rep.Verdict, want) {
			t.Errorf("verdict lacks %q:\n%s", want, rep.Verdict)
		}
	}

	// 200 pairs, but the recorder was connected 10 minutes of a 25 hour window and
	// 5000 RTPR articles arrived while it was down.
	leads = leads[:200]
	r, n = pairsWithLeads(ticker, leads...)
	for i := 0; i < 5000; i++ {
		r = append(r, rt(fmt.Sprint("gap", i), fmt.Sprintf("Released While Down %d", i), "Business Wire", tk(fmt.Sprintf("G%d", i)), base+6*hr1, base+6*hr1+1000))
	}
	cov := Coverage{Intervals: []Interval{{From: base - 5*min1, To: base + 5*min1}}}
	rep = Analyze(r, n, cov, Options{}, base-12*hr1, base+13*hr1)
	if rep.ExcludedGap != 5000 || rep.RTPROnly != 0 {
		t.Fatalf("setup: excluded %d, RTPR-only %d", rep.ExcludedGap, rep.RTPROnly)
	}
	for _, want := range []string{
		fmt.Sprintf("%d more excluded", rep.ExcludedGap),
		"connected 0.7% of the window",
		fmt.Sprintf("%d, 0.0%% of RTPR, are on wires Newsfilter does not list", rep.NotListedTotal),
	} {
		if !strings.Contains(rep.Verdict, want) {
			t.Errorf("verdict lacks %q:\n%s", want, rep.Verdict)
		}
	}
}

// RTPR's feed is keyed by ticker, so Newsfilter releases that name none
// (private companies) say nothing about RTPR's coverage.
func TestRTPRCoverageLeavesOutNewsfilterRecordsWithoutATicker(t *testing.T) {
	leads := make([]int64, 300)
	r, n := pairsWithLeads(ticker, leads...)
	for i := 0; i < 900; i++ {
		n = append(n, nf(fmt.Sprint("p", i), fmt.Sprintf("Private Hospital Group %d Opens Clinic", i), "prNewswire", []string{}, base, base+500))
	}
	rep := Analyze(r, n, wideCov(), Options{}, base-hr1, base+hr1)
	if rep.NFOnly != 0 || rep.NFNoSyms != 900 || rep.Matched != 300 {
		t.Fatalf("NFOnly %d, NFNoSyms %d, matched %d", rep.NFOnly, rep.NFNoSyms, rep.Matched)
	}
	if got := rep.RTPRCoverage(); got != 100 {
		t.Errorf("RTPR coverage = %v: 900 ticker-less releases are not RTPR misses", got)
	}
	for _, w := range rep.Wires {
		if w.Wire == "prnewswire" && (w.NFNoSyms != 900 || w.NF != 0) {
			t.Errorf("prnewswire = %+v", w)
		}
	}
	var out strings.Builder
	rep.Write(&out)
	for _, want := range []string{"900 more without a ticker not counted", "RTPR carries 100.0% of Newsfilter's ticker-bearing wire articles (900 without a ticker left out)"} {
		if !strings.Contains(out.String()+rep.Verdict, want) {
			t.Errorf("report lacks %q:\n%s", want, out.String())
		}
	}

	// Mixed: one matched record with a ticker, one matched without (it is not
	// counted either way), one unmatched with a ticker.
	rr := []RTPRArticle{
		rt("r1", "Story One", "Business Wire", tk("A"), base, base),
		rt("r2", "Story Two", "Business Wire", tk("B"), base, base),
	}
	nn := []Record{
		nf("n1", "Story One", "businesswire", tk("A"), base, base),
		nf("n2", "Story Two", "businesswire", nil, base, base),
		nf("n3", "Story Three", "businesswire", tk("C"), base, base),
	}
	rep = Analyze(rr, nn, wideCov(), Options{}, base-hr1, base+hr1)
	if rep.Matched != 2 || rep.MatchedSyms != 1 || rep.NFOnly != 1 || rep.RTPRCoverage() != 50 {
		t.Errorf("matched %d (with ticker %d), NFOnly %d, coverage %v", rep.Matched, rep.MatchedSyms, rep.NFOnly, rep.RTPRCoverage())
	}
}

// --- headline differences the title key cannot absorb ---

func TestNearMissesFlagHeadlineWordingDifferences(t *testing.T) {
	const rtprTitle = "Acme Corp Reports Third Quarter Results"
	for name, nfTitle := range map[string]string{
		"suffix":                 rtprTitle + " - Acme Corp",
		"prefix":                 "UPDATE: " + rtprTitle,
		"truncated by a vendor":  "Acme Corp Reports Third Quarter Res...",
		"double escaped entity":  "Acme Corp Reports Third Quarter Results &amp;amp; More",
		"decomposed accent":      "Acme Corp Reports Third Quarter R\u0065\u0301sults",
		"different final phrase": rtprTitle + " and Outlook",
	} {
		t.Run(name, func(t *testing.T) {
			r := []RTPRArticle{rt("r", rtprTitle, "Business Wire", tk("ACME"), base, base+4000)}
			n := []Record{nf("n", nfTitle, "businesswire", tk("ACME"), base+20_000, base)}
			if got := len(Match(r, n).Pairs); got != 0 {
				t.Fatalf("setup: the titles pair (%d), so this is not a near miss", got)
			}
			rep := Analyze(r, n, wideCov(), Options{}, base-hr1, base+hr1)
			if rep.RTPROnly != 1 || rep.NFOnly != 1 || rep.NearMiss != 1 {
				t.Fatalf("RTPR-only %d, Newsfilter-only %d, near misses %d", rep.RTPROnly, rep.NFOnly, rep.NearMiss)
			}
			if len(rep.NearMissEx) != 1 || rep.NearMissEx[0].RTPR != rtprTitle || rep.NearMissEx[0].Newsfilter != nfTitle {
				t.Errorf("examples = %+v", rep.NearMissEx)
			}
			var out strings.Builder
			rep.Write(&out)
			for _, want := range []string{"Near misses: 1 unmatched pairs share a ticker", "RTPR:       " + rtprTitle, "Newsfilter: " + nfTitle} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("report lacks %q:\n%s", want, out.String())
				}
			}
		})
	}
}

func TestNearMissesIgnoreUnrelatedUnmatchedArticles(t *testing.T) {
	r := []RTPRArticle{
		rt("other-ticker", "Headline One Differs", "Business Wire", tk("AAA"), base, base),
		rt("too-far", "Headline Two Differs", "Business Wire", tk("BBB"), base, base),
		rt("no-ticker", "Headline Three Differs", "Business Wire", nil, base, base),
		rt("paired", "Headline Four Same", "Business Wire", tk("DDD"), base, base),
	}
	n := []Record{
		nf("n1", "Another Story Entirely", "businesswire", tk("ZZZ"), base, base),                  // other ticker
		nf("n2", "Headline Two Is Worded Otherwise", "businesswire", tk("BBB"), base+2*min1, base), // two minutes apart
		nf("n3", "Headline Three Is Worded Otherwise", "businesswire", nil, base, base),            // no ticker to share
		nf("n4", "Headline Four Same", "businesswire", tk("DDD"), base, base),                      // simply paired
	}
	rep := Analyze(r, n, wideCov(), Options{}, base-hr1, base+hr1)
	if rep.Matched != 1 || rep.NearMiss != 0 || len(rep.NearMissEx) != 0 {
		t.Fatalf("matched %d, near misses %d (%+v)", rep.Matched, rep.NearMiss, rep.NearMissEx)
	}
	var out strings.Builder
	rep.Write(&out)
	if !strings.Contains(out.String(), "Near misses: 0 ") {
		t.Errorf("the count is printed even when it is zero:\n%s", out.String())
	}
}

func TestNearMissesPairOnceNearestFirstAndLimitExamples(t *testing.T) {
	var r []RTPRArticle
	var n []Record
	for i := 0; i < 5; i++ {
		tkr := fmt.Sprintf("T%d", i)
		r = append(r, rt(fmt.Sprint("r", i), fmt.Sprintf("Release %d reported by the wire", i), "Business Wire", tk(tkr), base, base))
		n = append(n, nf(fmt.Sprint("n", i), fmt.Sprintf("RELEASE %d (reported by the wire) - Company", i), "businesswire", tk(tkr), base, base))
	}
	// One more Newsfilter record for ticker T0 cannot make a second near miss for it.
	n = append(n, nf("extra", "Release 0 again, worded differently", "businesswire", tk("T0"), base+30_000, base))
	rep := Analyze(r, n, wideCov(), Options{}, base-hr1, base+hr1)
	if rep.NearMiss != 5 || len(rep.NearMissEx) != nearExamples {
		t.Fatalf("near misses %d, examples %d, want 5 and %d", rep.NearMiss, len(rep.NearMissEx), nearExamples)
	}
}

// The 70 character key is what separates one release from the next.
func TestTitleKeyLengthSeparatesReleasesBySeventyCharacters(t *testing.T) {
	a := func(n int) string { return strings.Repeat("a", n) }
	if got := len([]rune(TitleKey(a(300)))); got != titleKeyLen || titleKeyLen != 70 {
		t.Fatalf("key is %d characters, want 70", got)
	}
	pairs := func(x, y string) int {
		r := []RTPRArticle{rt("r", x, "Business Wire", tk("ACME"), base, base)}
		n := []Record{nf("n", y, "businesswire", tk("ACME"), base, base)}
		return len(Match(r, n).Pairs)
	}
	// Differing at character 65: two different releases, however alike they start.
	if pairs(a(64)+"x tail", a(64)+"y tail") != 0 {
		t.Error("titles that differ at character 65 must not pair")
	}
	if TitleKey(a(64)+"x") == TitleKey(a(64)+"y") {
		t.Error("keys differ at character 65")
	}
	// Differing at character 71: past the key, so a vendor's different ending pairs.
	if pairs(a(70)+"x tail", a(70)+"y tail") != 1 {
		t.Error("titles that differ only after character 70 must pair")
	}
	// Characters count after normalization, spaces collapsed.
	if pairs(strings.Repeat("ab ", 23)+"X", strings.Repeat("ab ", 23)+"Y") != 0 || pairs(strings.Repeat("ab ", 24)+"X", strings.Repeat("ab ", 24)+"Y") != 1 {
		t.Error("the cut is at 70 normalized characters: the 70th decides, the 73rd does not")
	}
}

// The entity is inside the 70 characters that make up the key, so a missing
// UnescapeString splits the pair.
func TestMatchDecodesEntitiesInsideTheKey(t *testing.T) {
	r := []RTPRArticle{rt("r", "AT&T Reports Third Quarter Results", "Business Wire", tk("T"), base, base)}
	n := []Record{nf("n", "AT&amp;T Reports Third Quarter Results", "businesswire", tk("T"), base, base)}
	if len(Match(r, n).Pairs) != 1 {
		t.Fatal("an HTML entity in the first 70 characters must not split a pair")
	}
}

// Exact ties in publication distance are broken by position (RTPR index, then
// Newsfilter index), never left to the sort. The oracle sorts the same edges by
// a total order; with many ties an unstable sort on distance alone disagrees.
func TestMatchTiesAreBrokenByPosition(t *testing.T) {
	const title = "Same Release Everywhere"
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 20; trial++ {
		size := 30 + rng.Intn(20)
		r := make([]RTPRArticle, size)
		n := make([]Record, size)
		for i := range r {
			r[i] = rt(fmt.Sprint("r", i), title, "Business Wire", tk("ACME"), base+int64(rng.Intn(4))*1000, base+int64(i))
			n[i] = nf(fmt.Sprint("n", i), title, "businesswire", tk("ACME"), base+int64(rng.Intn(4))*1000, base+int64(i))
		}

		type edge struct {
			r, n int
			dt   int64
		}
		var edges []edge
		for i := range r {
			for j := range n {
				edges = append(edges, edge{i, j, absI(r[i].PublishedMs - n[j].Pub)})
			}
		}
		sort.Slice(edges, func(x, y int) bool {
			if edges[x].dt != edges[y].dt {
				return edges[x].dt < edges[y].dt
			}
			if edges[x].r != edges[y].r {
				return edges[x].r < edges[y].r
			}
			return edges[x].n < edges[y].n
		})
		usedR, usedN := map[int]bool{}, map[int]bool{}
		want := map[[2]int]bool{}
		for _, e := range edges {
			if !usedR[e.r] && !usedN[e.n] {
				usedR[e.r], usedN[e.n] = true, true
				want[[2]int{e.r, e.n}] = true
			}
		}

		for run := 0; run < 2; run++ { // the same input twice gives the same answer
			got := map[[2]int]bool{}
			for _, p := range pairsOf(Match(r, n)) {
				got[p] = true
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("trial %d run %d: pairs differ from the (distance, RTPR index, Newsfilter index) order", trial, run)
			}
		}
	}
}

// --- coverage from events ---

func TestBuildCoverage(t *testing.T) {
	ev := func(name string, ms int64) Event { return Event{Ev: name, Ms: ms} }
	events := []Event{
		ev(EvConnected, 1000),
		ev(EvAlive, 2000),
		{Ev: EvDropped, Ms: 5000, Last: 4500, Err: "silent socket"}, // frames stopped at 4500, noticed at 5000
		ev(EvConnected, 8000),
		ev(EvStopped, 9000),
		ev(EvConnected, 12000), // never closed: the process died; articles prove it lived to 13500
		ev(EvAlive, 13000),
		ev(EvConnected, 20000),
		{Ev: EvDropped, Ms: 25000}, // no Last: the drop time itself
	}
	records := []Record{{Ms: 2500}, {Ms: 13500}}
	// Out-of-order input must not matter.
	events[0], events[len(events)-1] = events[len(events)-1], events[0]

	c := BuildCoverage(events, records, 0, 30000)
	want := []Interval{{1000, 4500}, {8000, 9000}, {12000, 13500}, {20000, 25000}}
	if !reflect.DeepEqual(c.Intervals, want) {
		t.Fatalf("intervals = %v, want %v", c.Intervals, want)
	}
	if c.Reconnects != 2 {
		t.Errorf("reconnects = %d", c.Reconnects)
	}
	if c.DisconnectedMs != (8000-4500)+(30000-25000) { // after each drop, the last one still ongoing
		t.Errorf("disconnected = %d", c.DisconnectedMs)
	}
	if c.OfflineMs != (12000-9000)+(20000-13500) { // after the clean stop and after the crash
		t.Errorf("offline = %d", c.OfflineMs)
	}
	for ms, want := range map[int64]bool{
		999: false, 1000: true, 4500: true, 4501: false, 7999: false, 8000: true, 9000: true, 9001: false,
		13500: true, 13501: false, 25000: true, 25001: false, 40000: false,
	} {
		if got := c.Covered(ms); got != want {
			t.Errorf("Covered(%d) = %v, want %v", ms, got, want)
		}
	}
	if got := c.CoveredMs(0, 100000); got != 3500+1000+1500+5000 {
		t.Errorf("CoveredMs = %d", got)
	}
	if got := c.CoveredMs(4000, 8500); got != 500+500 {
		t.Errorf("clipped CoveredMs = %d", got)
	}
}

func TestBuildCoverageStillConnectedAndEmpty(t *testing.T) {
	c := BuildCoverage([]Event{{Ev: EvConnected, Ms: 100}, {Ev: EvAlive, Ms: 700}}, nil, 0, 5000)
	if len(c.Intervals) != 1 || c.Intervals[0] != (Interval{100, 700}) || c.DisconnectedMs != 0 || c.Reconnects != 0 {
		t.Fatalf("%+v", c)
	}
	e := BuildCoverage(nil, nil, 0, 5000)
	if len(e.Intervals) != 0 || e.Covered(1) {
		t.Fatalf("%+v", e)
	}
	// A drop with a Last before the connection even started cannot move the end backwards.
	c = BuildCoverage([]Event{{Ev: EvConnected, Ms: 100}, {Ev: EvDropped, Ms: 200, Last: 50}}, nil, 0, 300)
	if len(c.Intervals) != 1 || c.Intervals[0] != (Interval{100, 200}) {
		t.Fatalf("%+v", c)
	}
	// A drop or stop with nothing open is ignored.
	c = BuildCoverage([]Event{{Ev: EvDropped, Ms: 10}, {Ev: EvStopped, Ms: 20}}, nil, 0, 40)
	if len(c.Intervals) != 0 || c.Reconnects != 0 {
		t.Fatalf("%+v", c)
	}
	// A long-lived connection whose connected event predates the files read is
	// known from its heartbeats alone.
	c = BuildCoverage([]Event{{Ev: EvAlive, Ms: 1000}, {Ev: EvAlive, Ms: 1600}, {Ev: EvAlive, Ms: 2200}}, nil, 0, 3000)
	if len(c.Intervals) != 1 || c.Intervals[0] != (Interval{1000, 2200}) {
		t.Fatalf("heartbeat-only coverage: %+v", c)
	}
}

// Events from before the window are read (the state at its start depends on
// them), but the reconnect count and the gap times describe the window only.
func TestBuildCoverageCountsOnlyTheWindow(t *testing.T) {
	events := []Event{
		{Ev: EvConnected, Ms: 0},
		{Ev: EvDropped, Ms: base - 20*hr1, Last: base - 20*hr1}, // long before the window
		{Ev: EvConnected, Ms: base - 30*min1},
		{Ev: EvDropped, Ms: base + 10*min1, Last: base + 10*min1}, // inside
		{Ev: EvConnected, Ms: base + 25*min1},
	}
	// A two hour window around base. The gap that began 20 h before it counts for
	// the part of it that falls inside: from the window start up to the reconnect
	// at -30 min, which is 30 min.
	c := BuildCoverage(events, nil, base-hr1, base+hr1)
	if c.Reconnects != 1 {
		t.Errorf("reconnects = %d, want 1 (the drop 20 h before the window is not in it)", c.Reconnects)
	}
	if want := 30*min1 + 15*min1; c.DisconnectedMs != want {
		t.Errorf("disconnected = %d, want %d (30 min of the old gap inside the window + the 15 min gap)", c.DisconnectedMs, want)
	}
	if c.OfflineMs != 0 {
		t.Errorf("offline = %d", c.OfflineMs)
	}
	var out strings.Builder
	Analyze(nil, nil, c, Options{}, base-hr1, base+hr1).Write(&out)
	if !strings.Contains(out.String(), "1 reconnects, disconnected 45m00s") {
		t.Errorf("report:\n%s", out.String())
	}

	// A gap still open at the end of the window runs to its end and no further.
	c = BuildCoverage([]Event{{Ev: EvConnected, Ms: 0}, {Ev: EvDropped, Ms: 1000, Last: 1000}}, nil, 5000, 8000)
	if c.Reconnects != 0 || c.DisconnectedMs != 3000 {
		t.Errorf("still down: reconnects %d, disconnected %d, want 0 and 3000", c.Reconnects, c.DisconnectedMs)
	}
	// Nothing of a gap that ended before the window opened counts.
	c = BuildCoverage([]Event{{Ev: EvConnected, Ms: 0}, {Ev: EvDropped, Ms: 1000, Last: 1000}, {Ev: EvConnected, Ms: 4000}}, nil, 5000, 8000)
	if c.Reconnects != 0 || c.DisconnectedMs != 0 || c.OfflineMs != 0 {
		t.Errorf("an old gap leaked into the window: %+v", c)
	}
}

// A process that ends while the stream is down (during the backoff wait, say)
// writes a stopped event with nothing open. The time after it is the recorder
// being off, not Newsfilter dropping the connection.
func TestBuildCoverageStoppedDuringAGapIsOffTimeNotDisconnectedTime(t *testing.T) {
	events := []Event{
		{Ev: EvConnected, Ms: 0},
		{Ev: EvDropped, Ms: 1000, Last: 900},
		{Ev: EvStopped, Ms: 1300}, // the user closed the scanner during the wait
		{Ev: EvConnected, Ms: 20000},
	}
	c := BuildCoverage(events, nil, 0, 30000)
	if c.DisconnectedMs != 400 || c.OfflineMs != 18700 || c.Reconnects != 1 {
		t.Errorf("disconnected %d, offline %d, reconnects %d; want 400, 18700, 1", c.DisconnectedMs, c.OfflineMs, c.Reconnects)
	}
	// Without the stopped event the whole stretch reads as Newsfilter being down.
	if c := BuildCoverage(append(events[:2:2], events[3]), nil, 0, 30000); c.DisconnectedMs != 19100 || c.OfflineMs != 0 {
		t.Errorf("old behaviour for a file with no stopped event: disconnected %d, offline %d", c.DisconnectedMs, c.OfflineMs)
	}
	// Still off when the report is made: the trailing stretch is time off too.
	c = BuildCoverage(events[:3], nil, 0, 5000)
	if c.DisconnectedMs != 400 || c.OfflineMs != 3700 {
		t.Errorf("trailing: disconnected %d, offline %d; want 400 and 3700", c.DisconnectedMs, c.OfflineMs)
	}
	// A second stopped event (another exit with nothing running) changes nothing.
	c = BuildCoverage([]Event{{Ev: EvConnected, Ms: 0}, {Ev: EvStopped, Ms: 500}, {Ev: EvStopped, Ms: 800}, {Ev: EvConnected, Ms: 2000}}, nil, 0, 3000)
	if c.OfflineMs != 1500 || c.DisconnectedMs != 0 {
		t.Errorf("repeated stopped: offline %d, disconnected %d; want 1500 and 0", c.OfflineMs, c.DisconnectedMs)
	}
}
