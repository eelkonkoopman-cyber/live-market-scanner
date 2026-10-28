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

// testSetupContext is a clean coiling flag: 45% pole, 6.1% base over 8 bars,
// 22% retrace, volume at 0.62x. Float is set so the nullable path renders a
// number; the Null-sentinel path is covered by TestBuildSetupPromptNulls.
func testSetupContext() core.SetupContext {
	fl := int64(8_400_000)
	return core.SetupContext{
		Sym: "ABCD",
		Flag: core.FlagRef{
			Score:    82.4,
			State:    1,
			PoleGain: 45.2,
			RangePct: 6.1,
			Retrace:  22,
			VolDry:   0.62,
			Bars:     8,
			BaseHigh: 12.55,
			AsOf:     "2026-07-30",
		},
		Bars: []core.DailyBar{
			{Date: "2026-07-20", O: 10.00, H: 10.80, L: 9.95, C: 10.70, V: 1_200_000},
			{Date: "2026-07-21", O: 10.70, H: 12.60, L: 10.65, C: 12.40, V: 4_100_000},
			{Date: "2026-07-22", O: 12.40, H: 12.55, L: 11.90, C: 12.10, V: 900_000},
		},
		Last:   12.40,
		ChgDay: 3.10,
		Rvol:   2.40,
		Float:  &fl,
	}
}

// okSetupServer returns an httptest server replying with one structured
// setup-read JSON payload, and captures the request body into gotReq.
func okSetupServer(t *testing.T, gotReq *map[string]any, payload string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotReq != nil {
			_ = json.NewDecoder(r.Body).Decode(gotReq)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_1", "type": "message", "role": "assistant",
			"model": "claude-test-model",
			"content": [{"type": "text", "text": ` + jsonQuote(payload) + `}],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 10, "output_tokens": 20}
		}`))
	}))
}

// jsonQuote JSON-quotes the inner payload so it can be embedded as the text
// of a content block (the taperead tests hand-escape theirs inline).
func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func setupSvc(t *testing.T, srv *httptest.Server, noRetry bool) *Service {
	t.Helper()
	opts := []option.RequestOption{option.WithBaseURL(srv.URL), option.WithAPIKey("k")}
	if noRetry {
		opts = append(opts, option.WithMaxRetries(0))
	}
	return svcWithBackend(&apiBackend{client: anthropic.NewClient(opts...), model: "claude-test-model"})
}

// TestReadSetupUserMessage checks the request Claude actually receives: the
// geometry under the exact snake_case names setupReadSystemPrompt documents,
// the live row numbers, and the CSV bar rendering (header plus at least one
// bar line).
func TestReadSetupUserMessage(t *testing.T) {
	var gotReq map[string]any
	srv := okSetupServer(t, &gotReq, `{"quality":4,"verdict":"tight base, volume dried up","whats_happening":"Coiling under 12.55.","risks":["thin float"],"invalidation":"losing 11.90 ends the base"}`)
	defer srv.Close()

	got, err := setupSvc(t, srv, false).ReadSetup(context.Background(), testSetupContext())
	if err != nil {
		t.Fatalf("ReadSetup: %v", err)
	}
	if got.Quality != 4 || got.Verdict != "tight base, volume dried up" || got.Err != "" {
		t.Fatalf("unexpected SetupRead: %+v", got)
	}
	if got.Sym != "ABCD" || got.AsOf != "2026-07-30" || got.Model != "claude-test-model" || got.Ts == 0 {
		t.Fatalf("SetupRead identity fields wrong: %+v", got)
	}
	if got.Invalidation != "losing 11.90 ends the base" || len(got.Risks) != 1 {
		t.Fatalf("SetupRead body fields wrong: %+v", got)
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
		"Symbol: ABCD",
		"Setup geometry (measured on daily bars):",
		"score: 82.4",
		"state: 1",
		"pole_gain_pct: 45.20",
		"base_range_pct: 6.10",
		"retrace_pct: 22.00",
		"vol_dry: 0.62",
		"base_bars: 8",
		"base_high: 12.5500",
		"as_of: 2026-07-30",
		"last: 12.4000",
		"chg_day_pct: 3.10",
		"rvol: 2.40",
		"float_shares: 8400000",
		"date,open,high,low,close,volume",
		"2026-07-21,10.7000,12.6000,10.6500,12.4000,4100000",
		"Read this setup.",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("user message missing %q; got:\n%s", want, content)
		}
	}

	sysBlocks, _ := gotReq["system"].([]any)
	if len(sysBlocks) != 1 {
		t.Fatalf("expected 1 system block, got %v", gotReq["system"])
	}
	sysText, _ := sysBlocks[0].(map[string]any)["text"].(string)
	for _, want := range []string{
		"retrace_pct",
		"BELOW 1.0 IS CONSTRUCTIVE",
		"INVENT NOTHING",
		"SAY WHEN A SETUP IS WEAK",
		"Respond ONLY with the JSON object",
	} {
		if !strings.Contains(sysText, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, sysText)
		}
	}
}

// TestBuildSetupPromptNulls checks that a core.Row Null sentinel (NaN) renders
// as "unknown" rather than the literal "NaN", which the model would read as a
// number, and that a nil Float does the same.
func TestBuildSetupPromptNulls(t *testing.T) {
	in := testSetupContext()
	in.Rvol = core.Null
	in.ChgDay = core.Null
	in.Float = nil

	got := buildSetupPrompt(in)
	for _, want := range []string{"chg_day_pct: unknown", "rvol: unknown", "float_shares: unknown"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt missing %q; got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "NaN") {
		t.Fatalf("prompt leaked a NaN:\n%s", got)
	}
}

// TestBuildSetupPromptBarsCapped checks maxSetupBars keeps the NEWEST bars
// (the ones the geometry was measured over), oldest first.
func TestBuildSetupPromptBarsCapped(t *testing.T) {
	in := testSetupContext()
	in.Bars = nil
	for i := 0; i < maxSetupBars+10; i++ {
		in.Bars = append(in.Bars, core.DailyBar{Date: "2026-01-" + pad2(i), C: float64(i)})
	}

	got := buildSetupPrompt(in)
	lines := 0
	for _, ln := range strings.Split(got, "\n") {
		if strings.HasPrefix(ln, "2026-01-") {
			lines++
		}
	}
	if lines != maxSetupBars {
		t.Fatalf("expected %d bar lines, got %d", maxSetupBars, lines)
	}
	if strings.Contains(got, "2026-01-00,") {
		t.Fatalf("oldest bar should have been dropped, not the newest:\n%s", got)
	}
	if !strings.Contains(got, "2026-01-49,") {
		t.Fatalf("newest bar should be present:\n%s", got)
	}
}

func pad2(i int) string {
	if i < 10 {
		return "0" + string(rune('0'+i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

// TestReadSetupNetworkError covers the API-failure path: ReadSetup must still
// return a valid Quality-0 SetupRead (nil Go error) whose WhatsHappening falls
// back to the deterministic geometry line, so a failed read still renders.
func TestReadSetupNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"boom"}}`))
	}))
	defer srv.Close()

	got, err := setupSvc(t, srv, true).ReadSetup(context.Background(), testSetupContext())
	if err != nil {
		t.Fatalf("ReadSetup should not surface a Go error for an API failure, got %v", err)
	}
	if got.Quality != 0 || got.Verdict != "unrated" {
		t.Fatalf("expected Quality-0 unrated fallback, got %+v", got)
	}
	if !strings.Contains(got.Err, "Claude request failed") {
		t.Fatalf("Err should name the request failure, got %q", got.Err)
	}
	if got.Sym != "ABCD" || got.AsOf != "2026-07-30" || got.Model != "claude-test-model" || got.Ts == 0 {
		t.Fatalf("fallback must still carry identity fields, got %+v", got)
	}
	for _, want := range []string{"ABCD", "coiling", "score 82", "retrace 22% of pole"} {
		if !strings.Contains(got.WhatsHappening, want) {
			t.Fatalf("fallback WhatsHappening missing %q, got %q", want, got.WhatsHappening)
		}
	}
}

// TestReadSetupRefusal covers the stop_reason=="refusal" path.
func TestReadSetupRefusal(t *testing.T) {
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

	got, err := setupSvc(t, srv, false).ReadSetup(context.Background(), testSetupContext())
	if err != nil {
		t.Fatalf("ReadSetup should not surface a Go error for a refusal, got %v", err)
	}
	if got.Quality != 0 || got.Verdict != "unrated" || got.WhatsHappening == "" {
		t.Fatalf("expected a valid populated fallback, got %+v", got)
	}
	if got.Err != "Claude declined to read this setup." {
		t.Fatalf("Err should be the specific refusal message, got %q", got.Err)
	}
}

// TestReadSetupUnparseable covers the JSON-unmarshal-failure path.
func TestReadSetupUnparseable(t *testing.T) {
	srv := okSetupServer(t, nil, "not json at all")
	defer srv.Close()

	got, err := setupSvc(t, srv, false).ReadSetup(context.Background(), testSetupContext())
	if err != nil {
		t.Fatalf("ReadSetup should not surface a Go error for unparseable output, got %v", err)
	}
	if got.Quality != 0 || got.Verdict != "unrated" || got.WhatsHappening == "" {
		t.Fatalf("expected a valid populated fallback, got %+v", got)
	}
	if got.Err != "Claude returned unparseable output." {
		t.Fatalf("Err should be the specific parse-failure message, got %q", got.Err)
	}
}

// TestReadSetupBackendUnavailable checks that a non-API backend is reported as
// the distinct ErrSetupReadUnavailable sentinel — and, so it cannot be
// confused with ErrTapeControlUnavailable, that it is NOT that one.
func TestReadSetupBackendUnavailable(t *testing.T) {
	svc := svcWithBackend(&cliBackend{path: "claude", model: "claude-test-model"})

	got, err := svc.ReadSetup(context.Background(), testSetupContext())
	if !errors.Is(err, ErrSetupReadUnavailable) {
		t.Fatalf("expected ErrSetupReadUnavailable, got %v", err)
	}
	if errors.Is(err, ErrTapeControlUnavailable) {
		t.Fatalf("ErrSetupReadUnavailable must be distinct from ErrTapeControlUnavailable")
	}
	if got.Sym != "" || got.Quality != 0 || got.Verdict != "" || got.Model != "" || got.Err != "" || len(got.Risks) != 0 {
		t.Fatalf("expected zero-value SetupRead alongside the sentinel, got %+v", got)
	}
}

// TestReadSetupQualityClampedHigh: an out-of-range high quality must not leak.
func TestReadSetupQualityClampedHigh(t *testing.T) {
	srv := okSetupServer(t, nil, `{"quality":7,"verdict":"v","whats_happening":"w","risks":["a"],"invalidation":"i"}`)
	defer srv.Close()

	got, err := setupSvc(t, srv, false).ReadSetup(context.Background(), testSetupContext())
	if err != nil {
		t.Fatalf("ReadSetup: %v", err)
	}
	if got.Quality != 5 {
		t.Fatalf("quality should clamp to 5, got %d", got.Quality)
	}
}

// TestReadSetupQualityClampedLow: a 0 must clamp to 1, never leak through as
// the value setupFallback reserves for a FAILED read.
func TestReadSetupQualityClampedLow(t *testing.T) {
	srv := okSetupServer(t, nil, `{"quality":0,"verdict":"v","whats_happening":"w","risks":["a"],"invalidation":"i"}`)
	defer srv.Close()

	got, err := setupSvc(t, srv, false).ReadSetup(context.Background(), testSetupContext())
	if err != nil {
		t.Fatalf("ReadSetup: %v", err)
	}
	if got.Quality != 1 {
		t.Fatalf("quality should clamp to 1, got %d", got.Quality)
	}
	if got.Err != "" {
		t.Fatalf("a successful read must not carry Err, got %q", got.Err)
	}
}

// TestReadSetupRisksCapped mirrors ReadTapeControl's Evidence cap.
func TestReadSetupRisksCapped(t *testing.T) {
	srv := okSetupServer(t, nil, `{"quality":2,"verdict":"","whats_happening":"w","risks":["a","b","c","d","e","f","g"],"invalidation":"i"}`)
	defer srv.Close()

	got, err := setupSvc(t, srv, false).ReadSetup(context.Background(), testSetupContext())
	if err != nil {
		t.Fatalf("ReadSetup: %v", err)
	}
	if len(got.Risks) != maxSetupRisks {
		t.Fatalf("risks should be capped at %d, got %d: %v", maxSetupRisks, len(got.Risks), got.Risks)
	}
	if got.Verdict != "unrated" {
		t.Fatalf("an empty verdict should default to unrated, got %q", got.Verdict)
	}
}
