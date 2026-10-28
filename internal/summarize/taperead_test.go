package summarize

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func testSnapshot() core.TapeSnapshot {
	return core.TapeSnapshot{
		Symbol:     "ABCD",
		Trigger:    "cvd_divergence",
		Headline:   "CVD diverging from price",
		LastPrice:  1.82,
		Bid:        1.81,
		Ask:        1.82,
		CVDSession: 45000,
		Delta10s:   1200,
		Book: core.BookStats{
			BidAbsorb:      3,
			AskAbsorb:      0,
			BidAbsorbedVol: 15000,
			DepthImbalance: 0.4,
		},
	}
}

// svcWithBackend builds a Service whose backend is already resolved to a
// direct apiBackend against srv, bypassing pick()'s env/CLI auto-detection —
// same construction style as TestAPIBackend/TestAPIBackendRefusal in
// summarize_test.go.
func svcWithBackend(be backend) *Service {
	return &Service{model: "claude-test-model", sem: make(chan struct{}, 2), be: be}
}

// TestReadTapeControlUserMessage checks the request Claude actually receives:
// the literal user-message pieces from Python's f-string, and that the book
// stats are re-tagged snake_case (bid_absorb, not bidAbsorb) to match
// tapeControlSystemPrompt's literal field names.
func TestReadTapeControlUserMessage(t *testing.T) {
	var gotReq map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_1", "type": "message", "role": "assistant",
			"model": "claude-test-model",
			"content": [{"type": "text", "text": "{\"control\":\"buyers\",\"confidence\":0.8,\"whats_happening\":\"buyers lifting\",\"evidence\":[\"e1\"],\"watch_for\":\"a pullback\"}"}],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 10, "output_tokens": 20}
		}`))
	}))
	defer srv.Close()

	ab := &apiBackend{client: anthropic.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("k")), model: "claude-test-model"}
	svc := svcWithBackend(ab)

	got, err := svc.ReadTapeControl(context.Background(), testSnapshot(), "CVD diverging from price")
	if err != nil {
		t.Fatalf("ReadTapeControl: %v", err)
	}
	if got.Control != "buyers" || got.Confidence != 0.8 || got.Err != "" {
		t.Fatalf("unexpected ControlRead: %+v", got)
	}

	msgs, _ := gotReq["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %v", gotReq["messages"])
	}
	blocks, _ := msgs[0].(map[string]any)["content"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 content block, got %v", msgs[0].(map[string]any)["content"])
	}
	content, _ := blocks[0].(map[string]any)["text"].(string)
	for _, want := range []string{
		"Regime shift flagged: CVD diverging from price",
		"Snapshot (JSON):",
		"Who is in control right now?",
		`"trigger": "cvd_divergence"`,
		`"cvd_session": 45000`,
		`"bid_absorb": 3`, // book re-tagged snake_case, not bidAbsorb
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("user message missing %q; got:\n%s", want, content)
		}
	}
	if strings.Contains(content, "bidAbsorb") {
		t.Fatalf("user message leaked camelCase book field; got:\n%s", content)
	}

	sysBlocks, _ := gotReq["system"].([]any)
	if len(sysBlocks) != 1 {
		t.Fatalf("expected 1 system block, got %v", gotReq["system"])
	}
	sysText, _ := sysBlocks[0].(map[string]any)["text"].(string)
	if !strings.Contains(sysText, "whats_happening") || !strings.Contains(sysText, "Respond ONLY with the JSON object") {
		t.Fatalf("system prompt missing expected content:\n%s", sysText)
	}
}

// TestReadTapeControlNetworkError covers Python's `except Exception as exc`
// path: the API call itself fails (here, a 5xx). ReadTapeControl must still
// return a valid balanced/0-confidence ControlRead (nil Go error), with Err
// naming the failure and WhatsHappening falling back to the headline.
func TestReadTapeControlNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"boom"}}`))
	}))
	defer srv.Close()

	ab := &apiBackend{
		client: anthropic.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("k"), option.WithMaxRetries(0)),
		model:  "claude-test-model",
	}
	svc := svcWithBackend(ab)

	got, err := svc.ReadTapeControl(context.Background(), testSnapshot(), "CVD diverging from price")
	if err != nil {
		t.Fatalf("ReadTapeControl should not surface a Go error for an API failure, got %v", err)
	}
	if got.Control != "balanced" || got.Confidence != 0 {
		t.Fatalf("expected balanced/0-confidence fallback, got %+v", got)
	}
	if got.WhatsHappening != "CVD diverging from price" {
		t.Fatalf("WhatsHappening should fall back to the headline, got %q", got.WhatsHappening)
	}
	if !strings.Contains(got.Err, "Claude request failed") {
		t.Fatalf("Err should name the request failure, got %q", got.Err)
	}
	if got.Model != "claude-test-model" {
		t.Fatalf("Model should still be set on a fallback read, got %q", got.Model)
	}
}

// TestReadTapeControlRefusal covers Python's stop_reason=="refusal" path.
func TestReadTapeControlRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_1", "type": "message", "role": "assistant",
			"model": "claude-test-model", "content": [],
			"stop_reason": "refusal",
			"usage": {"input_tokens": 1, "output_tokens": 0}
		}`))
	}))
	defer srv.Close()

	ab := &apiBackend{client: anthropic.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("k")), model: "claude-test-model"}
	svc := svcWithBackend(ab)

	got, err := svc.ReadTapeControl(context.Background(), testSnapshot(), "CVD diverging from price")
	if err != nil {
		t.Fatalf("ReadTapeControl should not surface a Go error for a refusal, got %v", err)
	}
	if got.Control != "balanced" || got.Confidence != 0 || got.WhatsHappening != "CVD diverging from price" {
		t.Fatalf("expected balanced/0-confidence headline fallback, got %+v", got)
	}
	if got.Err != "Claude declined to read this snapshot." {
		t.Fatalf("Err should be the specific refusal message, got %q", got.Err)
	}
}

// TestReadTapeControlUnparseable covers Python's json.JSONDecodeError path.
func TestReadTapeControlUnparseable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_1", "type": "message", "role": "assistant",
			"model": "claude-test-model",
			"content": [{"type": "text", "text": "not json at all"}],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 10, "output_tokens": 5}
		}`))
	}))
	defer srv.Close()

	ab := &apiBackend{client: anthropic.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("k")), model: "claude-test-model"}
	svc := svcWithBackend(ab)

	got, err := svc.ReadTapeControl(context.Background(), testSnapshot(), "CVD diverging from price")
	if err != nil {
		t.Fatalf("ReadTapeControl should not surface a Go error for unparseable output, got %v", err)
	}
	if got.Control != "balanced" || got.Confidence != 0 || got.WhatsHappening != "CVD diverging from price" {
		t.Fatalf("expected balanced/0-confidence headline fallback, got %+v", got)
	}
	if got.Err != "Claude returned unparseable output." {
		t.Fatalf("Err should be the specific parse-failure message, got %q", got.Err)
	}
}

// TestReadTapeControlBackendUnavailable checks that a non-API backend (e.g.
// the service picked the CLI backend) is reported as the distinct
// ErrTapeControlUnavailable sentinel, not folded into a real API error.
func TestReadTapeControlBackendUnavailable(t *testing.T) {
	svc := svcWithBackend(&cliBackend{path: "claude", model: "claude-test-model"})

	got, err := svc.ReadTapeControl(context.Background(), testSnapshot(), "CVD diverging from price")
	if !errors.Is(err, ErrTapeControlUnavailable) {
		t.Fatalf("expected ErrTapeControlUnavailable, got %v", err)
	}
	if got.Control != "" || got.Confidence != 0 || got.WhatsHappening != "" || got.Model != "" || got.Err != "" || len(got.Evidence) != 0 {
		t.Fatalf("expected zero-value ControlRead alongside the sentinel, got %+v", got)
	}
}

// TestReadTapeControlConfidenceClamped verifies the same defensive clamping
// Python applies to a stray out-of-range confidence value.
func TestReadTapeControlConfidenceClamped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_1", "type": "message", "role": "assistant",
			"model": "claude-test-model",
			"content": [{"type": "text", "text": "{\"control\":\"sellers\",\"confidence\":5,\"whats_happening\":\"x\",\"evidence\":[\"a\",\"b\",\"c\",\"d\",\"e\",\"f\",\"g\"],\"watch_for\":\"y\"}"}],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 10, "output_tokens": 5}
		}`))
	}))
	defer srv.Close()

	ab := &apiBackend{client: anthropic.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("k")), model: "claude-test-model"}
	svc := svcWithBackend(ab)

	got, err := svc.ReadTapeControl(context.Background(), testSnapshot(), "headline")
	if err != nil {
		t.Fatalf("ReadTapeControl: %v", err)
	}
	if got.Confidence != 1 {
		t.Fatalf("confidence should clamp to 1, got %v", got.Confidence)
	}
	if len(got.Evidence) != 6 {
		t.Fatalf("evidence should be capped at 6, got %d: %v", len(got.Evidence), got.Evidence)
	}
}
