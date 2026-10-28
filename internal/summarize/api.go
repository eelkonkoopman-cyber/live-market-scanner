package summarize

import (
	"context"
	"errors"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

// apiMaxTokens caps thinking plus the visible reply on every request. Adaptive
// thinking counts against max_tokens, so the old caps (2048 for summaries,
// 1024 for the structured reads) could cut a reply off once the model thought
// for a while. The prompts keep the visible text short, and only generated
// tokens are billed, so the roomier cap costs nothing extra. It stays below
// the SDK's non-streaming limit (see CalculateNonStreamingTimeout).
const apiMaxTokens = 16000

// apiBackend calls the Anthropic Messages API through the official Go SDK.
// The zero-arg client resolves credentials from ANTHROPIC_API_KEY,
// ANTHROPIC_AUTH_TOKEN, or an `ant auth login` profile on disk.
type apiBackend struct {
	client anthropic.Client
	model  string
	effort string // "" means DefaultEffort
}

func newAPIBackend(model, effort string) *apiBackend {
	return &apiBackend{client: anthropic.NewClient(), model: model, effort: effort}
}

func (b *apiBackend) name() string { return "api" }

// fallbackFor names the model that re-serves a refused request, or "" when
// the configured model needs none. Claude Opus 5 and the Fable/Mythos 5
// models run safety classifiers that occasionally decline a benign request
// (HTTP 200 with stop_reason "refusal"). A server-side fallback retries that
// request on Opus 4.8 inside the same API call, so a rare false positive still
// yields a summary or tape read instead of an error.
func fallbackFor(model string) string {
	for _, prefix := range []string{"claude-opus-5", "claude-fable-5", "claude-mythos-5"} {
		if strings.HasPrefix(model, prefix) {
			return FallbackModel
		}
	}
	return ""
}

// newParams builds the request that run and runStructured both send. It uses
// the beta Messages endpoint because server-side fallbacks are a beta feature.
// Adaptive thinking and output_config.effort go on every model that accepts
// them; Haiku 4.5 (a valid cheap override via SCANNER_CLAUDE_MODEL) rejects
// both, so they are omitted there rather than sent as params it 400s on.
func (b *apiBackend) newParams(system, prompt string) anthropic.BetaMessageNewParams {
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(b.model),
		MaxTokens: apiMaxTokens,
		System: []anthropic.BetaTextBlockParam{{
			Text:         system,
			CacheControl: anthropic.NewBetaCacheControlEphemeralParam(), // stable prefix: cache across requests
		}},
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(prompt)),
		},
	}
	if !strings.Contains(b.model, "haiku") {
		adaptive := anthropic.BetaThinkingConfigAdaptiveParam{}
		params.Thinking = anthropic.BetaThinkingConfigParamUnion{OfAdaptive: &adaptive}
		effort := b.effort
		if effort == "" {
			effort = DefaultEffort
		}
		params.OutputConfig.Effort = anthropic.BetaOutputConfigEffort(effort)
	}
	if fb := fallbackFor(b.model); fb != "" {
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_06_01}
		params.Fallbacks = []anthropic.BetaFallbackParam{{Model: anthropic.Model(fb)}}
	}
	return params
}

func (b *apiBackend) run(ctx context.Context, system, prompt string) (string, error) {
	resp, err := b.client.Beta.Messages.New(ctx, b.newParams(system, prompt))
	if err != nil {
		return "", err
	}
	// A refusal that survived the fallback, or one from a model without one.
	if resp.StopReason == anthropic.BetaStopReasonRefusal {
		return "", errors.New("model declined to summarize this article")
	}
	return betaReplyText(resp), nil
}

// runStructured is run's structured-output sibling: it constrains the reply
// to the given JSON schema via OutputConfig instead of returning free text, for
// callers (ReadTapeControl, ReadSetup) that need a parseable object back.
// Unlike run, a refusal is reported through the refused return rather than
// folded into err, so the caller can tell "the model declined" apart from "the
// request failed" and produce two different fallback messages.
func (b *apiBackend) runStructured(ctx context.Context, system, prompt string, schema map[string]any) (text string, refused bool, err error) {
	params := b.newParams(system, prompt)
	params.OutputConfig.Format = anthropic.BetaJSONOutputFormatParam{Schema: schema}
	resp, err := b.client.Beta.Messages.New(ctx, params)
	if err != nil {
		return "", false, err
	}
	if resp.StopReason == anthropic.BetaStopReasonRefusal {
		return "", true, nil
	}
	return betaReplyText(resp), false, nil
}

// betaReplyText joins the reply's text blocks. Thinking blocks are skipped; on
// Opus 5 their text is empty anyway, because display defaults to omitted.
func betaReplyText(resp *anthropic.BetaMessage) string {
	var out strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			out.WriteString(t.Text)
		}
	}
	return out.String()
}
