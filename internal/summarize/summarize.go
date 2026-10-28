// Package summarize turns news articles and SEC filings into short,
// trader-focused summaries. Two independent engines live here, both
// implementing the same Summarize(ctx, Article) (string, error) + Model()
// shape (internal/autosum's Summarizer interface):
//
//   - Service (this file): Claude-backed, via two interchangeable backends —
//     "api" (the official Anthropic Go SDK; needs ANTHROPIC_API_KEY, an
//     ANTHROPIC_AUTH_TOKEN, or an `ant auth login` profile on disk) or "cli"
//     (the local Claude Code CLI, `claude -p`, which runs on the user's
//     existing Claude subscription at no extra API cost). Backend selection
//     is automatic (env credentials -> api, else claude on PATH -> cli) and
//     overridable via SCANNER_SUMMARY_BACKEND=api|cli.
//   - LocalSummarizer (local.go): a free, offline, extractive/rule-based
//     engine — no LLM, no per-article network call beyond the same
//     pre-existing URL fetch the Claude path already uses as its own
//     body-less fallback. It reports Model() == LocalModel ("local"), a
//     sentinel real Claude model strings never collide with.
//
// Auto-summarization (internal/autosum) defaults to LocalSummarizer so the
// scanner needs no Claude credentials at all for its background news reads;
// Claude stays available as an explicit, opt-in upgrade (SCANNER_AUTOSUM_
// ENGINE=claude, or the "✨ Summarize"/"✨ Ask Claude" button in the UI),
// mirroring the tape reader's existing free-local-by-default,
// Claude-as-opt-in pattern (internal/tape's always-on local verdict + the
// per-symbol Claude toggle).
package summarize

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Article is everything the summarizer may use as source material. Body is the
// full article text when the feed supplied one (Benzinga); otherwise the
// summarizer falls back to fetching URL (SEC filings are fetched with the
// EDGAR fair-access User-Agent) and finally to headline+teaser alone.
type Article struct {
	ID       string
	Sym      string // primary ticker for context ("" ok)
	Tickers  []string
	Headline string
	Source   string
	URL      string
	Body     string // full text/HTML if the feed carried it
	Teaser   string
	TS       int64 // published, epoch ms
}

// backend is one way of running a prompt through Claude.
type backend interface {
	name() string
	run(ctx context.Context, system, prompt string) (string, error)
}

// Service picks a backend once (lazily) and serializes access with a small
// semaphore so a click-storm can't stampede the API or spawn N CLIs.
type Service struct {
	model  string
	effort string // output_config.effort, or the CLI's --effort
	pref   string // "", "api", "cli"

	mu  sync.Mutex
	be  backend
	sem chan struct{}
}

// DefaultModel is used unless SCANNER_CLAUDE_MODEL overrides it.
const DefaultModel = "claude-opus-5"

// DefaultEffort is sent as output_config.effort (and as the CLI's --effort)
// unless SCANNER_CLAUDE_EFFORT overrides it with low, medium, high, xhigh or
// max. Medium keeps these short, latency-sensitive reads quick and cheap.
const DefaultEffort = "medium"

// FallbackModel re-serves a request that the primary model's safety
// classifiers declined (see fallbackFor in api.go).
const FallbackModel = "claude-opus-4-8"

// validEfforts lists the effort levels the API accepts.
var validEfforts = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// New builds the service from environment configuration.
func New() *Service {
	model := strings.TrimSpace(os.Getenv("SCANNER_CLAUDE_MODEL"))
	if model == "" {
		model = DefaultModel
	}
	// An unknown effort level falls back to the default instead of reaching
	// the API, where it would fail every request.
	effort := strings.ToLower(strings.TrimSpace(os.Getenv("SCANNER_CLAUDE_EFFORT")))
	if !validEfforts[effort] {
		effort = DefaultEffort
	}
	return &Service{
		model:  model,
		effort: effort,
		pref:   strings.ToLower(strings.TrimSpace(os.Getenv("SCANNER_SUMMARY_BACKEND"))),
		sem:    make(chan struct{}, 2),
	}
}

// Model reports the configured Claude model (for cache bookkeeping).
func (s *Service) Model() string { return s.model }

// Effort reports the effort level sent with every request.
func (s *Service) Effort() string { return s.effort }

// Backend reports which backend will serve requests ("api", "cli", or "none").
func (s *Service) Backend() string {
	be, err := s.pick()
	if err != nil {
		return "none"
	}
	return be.name()
}

// pick resolves the backend once. Preference order for "auto": explicit API
// credentials in the environment win; otherwise a `claude` CLI on PATH; and as
// a last resort the SDK anyway (it may find an `ant auth login` profile).
func (s *Service) pick() (backend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.be != nil {
		return s.be, nil
	}
	hasEnvCreds := os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("ANTHROPIC_AUTH_TOKEN") != ""
	cliPath, cliOK := findClaude()

	switch s.pref {
	case "api":
		s.be = newAPIBackend(s.model, s.effort)
	case "cli":
		if !cliOK {
			return nil, errors.New("SCANNER_SUMMARY_BACKEND=cli but no Claude Code CLI found")
		}
		s.be = &cliBackend{path: cliPath, model: s.model, effort: s.effort}
	default: // auto
		switch {
		case hasEnvCreds:
			s.be = newAPIBackend(s.model, s.effort)
		case cliOK:
			s.be = &cliBackend{path: cliPath, model: s.model, effort: s.effort}
		default:
			s.be = newAPIBackend(s.model, s.effort) // may still work via an `anthropic auth login` profile
		}
	}
	return s.be, nil
}

// Summarize produces the trader-focused summary for one article.
func (s *Service) Summarize(ctx context.Context, a Article) (string, error) {
	be, err := s.pick()
	if err != nil {
		return "", err
	}

	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return "", ctx.Err()
	}

	text := sourceText(ctx, a)
	system, prompt := buildPrompt(a, text)

	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	out, err := be.run(cctx, system, prompt)
	if err != nil {
		return "", fmt.Errorf("summarize (%s): %w", be.name(), err)
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", errors.New("summarizer returned empty output")
	}
	return out, nil
}

// maxSourceChars caps the text handed to Claude — SEC filings can run to
// megabytes; ~150k chars is plenty for an accurate summary on any model.
const maxSourceChars = 150_000

// sourceText picks the best available source material: feed-supplied body,
// else a fetched URL (SEC/EDGAR aware), else teaser, else headline only.
func sourceText(ctx context.Context, a Article) string {
	if t := strings.TrimSpace(a.Body); t != "" {
		return clip(stripHTML(t), maxSourceChars)
	}
	if a.URL != "" {
		if t, err := fetchArticle(ctx, a.URL); err == nil && t != "" {
			return clip(t, maxSourceChars)
		}
	}
	return clip(strings.TrimSpace(a.Teaser), maxSourceChars)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n[...truncated...]"
}

const systemPrompt = `You are a markets analyst inside a real-time stock scanner used by an active intraday momentum trader. You summarize breaking news and SEC filings so the trader does not have to open and read them mid-session.

Rules:
- Maximum 120 words. Terse bullets, no preamble, no headers.
- Lead with WHAT happened in one line.
- Then WHY it can move the stock TODAY (catalyst, guidance, FDA, contract, offering...).
- For SEC filings and offerings, always extract the dealbreakers: offering size ($ and shares), price, warrants, dilution as % of shares outstanding if stated, effective date, and whether it is an ATM/shelf (S-3), direct offering (424B5), or material event (8-K item number).
- Flag anything time-sensitive (conference call time, effect date, halt).
- Final line exactly: "Stance: Bullish|Bearish|Neutral — <one short reason>".
- If the source text is thin (headline only), say so and summarize what can be inferred without invention.`

// buildPrompt renders the user prompt for one article.
func buildPrompt(a Article, text string) (system, prompt string) {
	var b strings.Builder
	fmt.Fprintf(&b, "Ticker(s): %s\n", strings.Join(a.Tickers, ", "))
	fmt.Fprintf(&b, "Headline: %s\n", a.Headline)
	if a.Source != "" {
		fmt.Fprintf(&b, "Source: %s\n", a.Source)
	}
	if a.TS > 0 {
		fmt.Fprintf(&b, "Published: %s\n", time.UnixMilli(a.TS).UTC().Format(time.RFC3339))
	}
	if text != "" {
		fmt.Fprintf(&b, "\nArticle/filing text:\n%s\n", text)
	} else {
		b.WriteString("\nNo article text available — headline only.\n")
	}
	b.WriteString("\nSummarize for the trader now.")
	return systemPrompt, b.String()
}
