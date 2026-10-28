package news

import (
	"context"
	"fmt"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// The language gate (keep) must sit on BOTH entry paths — AddExternal (WS
// push, rtpr, autosum, EDGAR) and the poll loop's add closure — because a
// gate present on only one path is a silent regression: the other path keeps
// flooding the panel and nothing errors. Headlines marked REAL CAPTURE are
// verbatim from this app's own stream (scanner.db / live WS research,
// 2026-08), same convention as internal/lang's tests.
const (
	koreanHeadline  = "국장 한 달 새 28% 피바다..."                                                                                  // REAL CAPTURE (ko)
	italianHeadline = "Crollo dei titoli legati ai chip a Wall Street: per l'analista la domanda di semiconduttori IA super" // REAL CAPTURE (it)
	englishHeadline = "Acme Pharma's Drug Gets FDA Nod & Fast Track"
	secHeadline     = "SEC filing: 8-K (material event) — Acme Corp (ACME)"
)

// gateOff guarantees the bypass env var is unset for tests that assert
// drops, regardless of the invoking shell's environment.
func gateOff(t *testing.T) {
	t.Helper()
	t.Setenv("SCANNER_NEWS_FOREIGN", "")
}

func TestForeignDroppedOnAddExternalPath(t *testing.T) {
	gateOff(t)
	p := New(&fakeFeed{})
	var sunk []core.NewsItem
	p.SetSink(func(items []core.NewsItem, _ map[string]string, _ string) {
		sunk = append(sunk, items...)
	})

	fresh := p.AddExternal([]core.NewsItem{
		{ID: "bzKO1", Headline: koreanHeadline, TS: 1000},
		{ID: "bzEN1", Headline: englishHeadline, TS: 1001},
	}, nil, "push")

	if len(fresh) != 1 || fresh[0].ID != "bzEN1" {
		t.Fatalf("want only the English item fresh, got %+v", fresh)
	}
	// The foreign item must be gated BEFORE the cache, not cached-then-hidden.
	if _, _, ok := p.Get("bzKO1"); ok {
		t.Fatal("foreign item must not enter the article cache")
	}
	if _, _, ok := p.Get("bzEN1"); !ok {
		t.Fatal("English item must be cached")
	}
	// And the archive sink must never see it either.
	if len(sunk) != 1 || sunk[0].ID != "bzEN1" {
		t.Fatalf("sink must receive only the English item, got %+v", sunk)
	}
	if got := p.foreignDrops.Load(); got != 1 {
		t.Fatalf("drop counter: want 1, got %d", got)
	}
}

func TestForeignDroppedOnPollPath(t *testing.T) {
	gateOff(t)
	// Italian on this path (script layer on the other) so the two entry-path
	// tests jointly cover both of lang's layers end-to-end.
	ff := &fakeFeed{bzItems: []core.NewsItem{
		{ID: "bzIT1", Headline: italianHeadline, TS: 2000},
		{ID: "bzEN2", Headline: englishHeadline, TS: 2001},
	}}
	p := New(ff)

	p.poll(context.Background())
	select {
	case b := <-p.out:
		if len(b) != 1 || b[0].ID != "bzEN2" {
			t.Fatalf("poll batch must hold only the English item, got %+v", b)
		}
	default:
		t.Fatal("expected a batch with the English item")
	}
	if p.seen["bzIT1"] {
		t.Fatal("gated foreign item must not be marked seen")
	}
	if got := p.foreignDrops.Load(); got != 1 {
		t.Fatalf("drop counter: want 1, got %d", got)
	}
}

// SEC EDGAR filing headlines are English by construction ("SEC filing: ...")
// but do NOT pass the gate trivially: the filer's registered name rides in
// the headline, and ALL-CAPS Latin-American names ("COMPANHIA DE ... DO
// ESTADO DE SAO PAULO") are made of es/pt stopwords. The adversarial sweep
// (2026-08-03) proved the gate really dropped that shape before lang's
// case-as-evidence rule — SABESP (NYSE: SBS) files 6-Ks constantly, and every
// one would have been silently invisible. Both shapes stay pinned here at the
// gate level, end to end through AddExternal.
func TestSECFilingHeadlinePassesGate(t *testing.T) {
	gateOff(t)
	p := New(&fakeFeed{})
	fresh := p.AddExternal([]core.NewsItem{
		{ID: "sec-0001111111-26-000101", Headline: secHeadline, TS: 3000},
		{ID: "sec-0009999999-26-000001", Headline: "SEC filing: 6-K — COMPANHIA DE SANEAMENTO BASICO DO ESTADO DE SAO PAULO-SABESP (SBS)", TS: 3001},
	}, nil, "sec")
	if len(fresh) != 2 {
		t.Fatalf("both SEC filing headlines must pass the gate, got %+v", fresh)
	}
	if got := p.foreignDrops.Load(); got != 0 {
		t.Fatalf("no drops expected, got %d", got)
	}
}

// SCANNER_NEWS_FOREIGN=1 must bypass the gate on BOTH paths — read once at
// construction, so it's set before New.
func TestEnvBypassKeepsForeignOnBothPaths(t *testing.T) {
	t.Setenv("SCANNER_NEWS_FOREIGN", "1")
	ff := &fakeFeed{bzItems: []core.NewsItem{{ID: "bzIT2", Headline: italianHeadline, TS: 4000}}}
	p := New(ff)

	if fresh := p.AddExternal([]core.NewsItem{{ID: "bzKO2", Headline: koreanHeadline, TS: 4001}}, nil, "push"); len(fresh) != 1 {
		t.Fatalf("bypass: AddExternal must keep the foreign item, got %+v", fresh)
	}
	p.poll(context.Background())
	select {
	case b := <-p.out:
		if len(b) != 1 || b[0].ID != "bzIT2" {
			t.Fatalf("bypass: poll must keep the foreign item, got %+v", b)
		}
	default:
		t.Fatal("bypass: expected the foreign item from the poll path")
	}
	if got := p.foreignDrops.Load(); got != 0 {
		t.Fatalf("bypass must not count drops, got %d", got)
	}
}

// The counter is the only operational evidence the gate is working (and not
// over-working): it must count every drop, across both paths.
func TestForeignDropCounterCountsAcrossPaths(t *testing.T) {
	gateOff(t)
	ff := &fakeFeed{bzItems: []core.NewsItem{{ID: "bzIT3", Headline: italianHeadline, TS: 5000}}}
	p := New(ff)

	for i := 0; i < 3; i++ {
		p.AddExternal([]core.NewsItem{
			{ID: fmt.Sprintf("bzKO%d", 100+i), Headline: koreanHeadline, TS: 5001 + int64(i)},
		}, nil, "push")
	}
	p.poll(context.Background()) // +1 via the poll path
	if got := p.foreignDrops.Load(); got != 4 {
		t.Fatalf("drop counter: want 4 (3 push + 1 poll), got %d", got)
	}
}
