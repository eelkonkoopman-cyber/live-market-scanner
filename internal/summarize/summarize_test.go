package summarize

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

func TestStripHTML(t *testing.T) {
	in := `<html><head><title>x</title><script>var a=1;</script></head>
<body><h1>Offering Announced</h1><p>ABC Corp prices $10&nbsp;million offering.</p>
<div>5,000,000 shares at $2.00</div><style>.x{}</style></body></html>`
	got := stripHTML(in)
	for _, want := range []string{"Offering Announced", "$10 million offering", "5,000,000 shares at $2.00"} {
		if !strings.Contains(got, want) {
			t.Fatalf("stripHTML missing %q in:\n%s", want, got)
		}
	}
	for _, ban := range []string{"<p>", "var a=1", ".x{}"} {
		if strings.Contains(got, ban) {
			t.Fatalf("stripHTML leaked %q in:\n%s", ban, got)
		}
	}
}

func TestBuildPromptHeadlineOnly(t *testing.T) {
	sys, p := buildPrompt(Article{Tickers: []string{"ABCD"}, Headline: "ABCD halted"}, "")
	if !strings.Contains(sys, "Stance:") {
		t.Fatal("system prompt lost the stance instruction")
	}
	if !strings.Contains(p, "ABCD halted") || !strings.Contains(p, "headline only") {
		t.Fatalf("prompt wrong:\n%s", p)
	}
}

func TestClip(t *testing.T) {
	if got := clip(strings.Repeat("a", 10), 5); !strings.HasPrefix(got, "aaaaa") || !strings.Contains(got, "truncated") {
		t.Fatalf("clip: %q", got)
	}
	if got := clip("abc", 5); got != "abc" {
		t.Fatalf("clip no-op: %q", got)
	}
}

// TestAPIBackend runs the real SDK against a local mock of the Messages API and
// checks the request the default configuration sends: Claude Opus 5, adaptive
// thinking, medium effort, the roomier max_tokens, and the server-side refusal
// fallback to Opus 4.8.
func TestAPIBackend(t *testing.T) {
	var gotReq map[string]any
	var gotBeta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBeta = r.Header.Get("anthropic-beta")
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_1", "type": "message", "role": "assistant",
			"model": "claude-opus-5",
			"content": [{"type": "text", "text": "Offering priced. Stance: Bearish, dilution"}],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 10, "output_tokens": 20}
		}`))
	}))
	defer srv.Close()

	b := &apiBackend{
		client: anthropic.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("test-key")),
		model:  DefaultModel,
	}
	out, err := b.run(context.Background(), "sys", "user prompt")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "Stance: Bearish") {
		t.Fatalf("output: %q", out)
	}
	if gotReq["model"] != "claude-opus-5" {
		t.Fatalf("model sent: %v", gotReq["model"])
	}
	think, _ := gotReq["thinking"].(map[string]any)
	if think["type"] != "adaptive" {
		t.Fatalf("thinking config sent: %v", gotReq["thinking"])
	}
	oc, _ := gotReq["output_config"].(map[string]any)
	if oc["effort"] != "medium" {
		t.Fatalf("effort sent: %v", gotReq["output_config"])
	}
	if gotReq["max_tokens"] != float64(apiMaxTokens) {
		t.Fatalf("max_tokens sent: %v", gotReq["max_tokens"])
	}
	var fbModel any
	if fbs, _ := gotReq["fallbacks"].([]any); len(fbs) == 1 {
		fb0, _ := fbs[0].(map[string]any)
		fbModel = fb0["model"]
	}
	if fbModel != FallbackModel {
		t.Fatalf("fallbacks sent: %v", gotReq["fallbacks"])
	}
	if !strings.Contains(gotBeta, "server-side-fallback-2026-06-01") {
		t.Fatalf("anthropic-beta header: %q", gotBeta)
	}
}

// TestAPIBackendRefusal maps a refusal stop reason to an error.
func TestAPIBackendRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_1", "type": "message", "role": "assistant",
			"model": "claude-opus-5", "content": [],
			"stop_reason": "refusal",
			"usage": {"input_tokens": 1, "output_tokens": 0}
		}`))
	}))
	defer srv.Close()
	b := &apiBackend{client: anthropic.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("k")), model: DefaultModel}
	if _, err := b.run(context.Background(), "s", "p"); err == nil {
		t.Fatal("refusal should surface as error")
	}
}

// TestAPIBackendHaikuOmitsUnsupportedParams: Haiku 4.5 rejects adaptive
// thinking and output_config.effort and has no refusal fallback, so none of
// them may be sent when it is the configured model.
func TestAPIBackendHaikuOmitsUnsupportedParams(t *testing.T) {
	var gotReq map[string]any
	var gotBeta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBeta = r.Header.Get("anthropic-beta")
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_2", "type": "message", "role": "assistant",
			"model": "claude-haiku-4-5",
			"content": [{"type": "text", "text": "ok"}],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 1, "output_tokens": 1}
		}`))
	}))
	defer srv.Close()
	b := &apiBackend{client: anthropic.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("k")), model: "claude-haiku-4-5", effort: "medium"}
	if _, err := b.run(context.Background(), "s", "p"); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, key := range []string{"thinking", "output_config", "fallbacks"} {
		if _, ok := gotReq[key]; ok {
			t.Fatalf("%s must not be sent for Haiku: %v", key, gotReq[key])
		}
	}
	if strings.Contains(gotBeta, "server-side-fallback") {
		t.Fatalf("fallback beta header must not be sent for Haiku: %q", gotBeta)
	}
}

// TestNewReadsModelAndEffort covers the environment overrides and their
// defaults. An unknown effort falls back to DefaultEffort instead of failing
// every request.
func TestNewReadsModelAndEffort(t *testing.T) {
	t.Setenv("SCANNER_CLAUDE_MODEL", "")
	t.Setenv("SCANNER_CLAUDE_EFFORT", "")
	if s := New(); s.Model() != "claude-opus-5" || s.Effort() != "medium" {
		t.Fatalf("defaults: model=%q effort=%q", s.Model(), s.Effort())
	}
	t.Setenv("SCANNER_CLAUDE_EFFORT", " HIGH ")
	if s := New(); s.Effort() != "high" {
		t.Fatalf("override: effort=%q", s.Effort())
	}
	t.Setenv("SCANNER_CLAUDE_EFFORT", "turbo")
	if s := New(); s.Effort() != DefaultEffort {
		t.Fatalf("invalid effort should fall back: effort=%q", s.Effort())
	}
}

// TestCLIArgsCarryModelAndEffort: the CLI backend mirrors the API backend's
// model and effort, and leaves effort off for Haiku.
func TestCLIArgsCarryModelAndEffort(t *testing.T) {
	got := strings.Join((&cliBackend{path: "claude", model: "claude-opus-5", effort: "medium"}).args("sys"), " ")
	if !strings.Contains(got, "--model claude-opus-5") || !strings.Contains(got, "--effort medium") {
		t.Fatalf("args: %s", got)
	}
	got = strings.Join((&cliBackend{path: "claude", model: "claude-haiku-4-5", effort: "medium"}).args("sys"), " ")
	if strings.Contains(got, "--effort") {
		t.Fatalf("haiku args must not carry --effort: %s", got)
	}
}

func TestSourceTextPrefersBody(t *testing.T) {
	got := sourceText(context.Background(), Article{Body: "<p>Full body</p>", Teaser: "teaser", URL: ""})
	if got != "Full body" {
		t.Fatalf("sourceText: %q", got)
	}
	got = sourceText(context.Background(), Article{Teaser: "just teaser"})
	if got != "just teaser" {
		t.Fatalf("sourceText fallback: %q", got)
	}
}

// TestLiveSummarize is an opt-in integration test against the real Claude
// backend (API or CLI, whichever New() picks). Run with:
//
//	SCANNER_LIVE_SUMMARY_TEST=1 go test ./internal/summarize -run TestLiveSummarize -v
func TestLiveSummarize(t *testing.T) {
	if os.Getenv("SCANNER_LIVE_SUMMARY_TEST") == "" {
		t.Skip("set SCANNER_LIVE_SUMMARY_TEST=1 to run the live Claude integration test")
	}
	svc := New()
	t.Logf("backend=%s model=%s effort=%s", svc.Backend(), svc.Model(), svc.Effort())
	out, err := svc.Summarize(context.Background(), Article{
		ID: "live-test", Sym: "ABCD", Tickers: []string{"ABCD"},
		Headline: "ABCD Pharma announces pricing of $12 million public offering",
		Source:   "Benzinga",
		Body: `<p>ABCD Pharma (NASDAQ: ABCD) today announced the pricing of its public offering of
6,000,000 shares of common stock at a public offering price of $2.00 per share, together with
warrants to purchase up to 6,000,000 shares at an exercise price of $2.20. Gross proceeds are
expected to be approximately $12.0 million. The offering is expected to close on or about
July 24, 2026. ABCD had 18,000,000 shares outstanding prior to the offering. Proceeds will fund
the Phase 2 trial of ABCD-101 and general corporate purposes.</p>`,
		TS: 1753142400000,
	})
	if err != nil {
		t.Fatalf("live summarize failed: %v", err)
	}
	t.Logf("summary:\n%s", out)
	if !strings.Contains(out, "Stance:") {
		t.Errorf("summary missing the mandated stance line:\n%s", out)
	}
}

func TestFetchArticleSECUserAgent(t *testing.T) {
	// Not sec.gov (httptest host), but verify basic fetch + strip path works.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html><body><p>" + strings.Repeat("Filing text. ", 20) + "</p></body></html>"))
	}))
	defer srv.Close()
	got, err := fetchArticle(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(got, "Filing text.") {
		t.Fatalf("fetched text: %q", got)
	}
}
