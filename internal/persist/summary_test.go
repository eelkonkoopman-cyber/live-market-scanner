package persist

import (
	"context"
	"testing"
)

// TestSummaryRepoRoundTrip pins GetSummary's widened (summary, model, ok, err)
// signature: the model column must round-trip correctly whether it holds the
// "local" sentinel (internal/summarize.LocalModel) or a real Claude model
// string — the manual "news.summarize" handler's silent-Claude-short-circuit
// fix depends entirely on being able to tell the two apart from the cache.
func TestSummaryRepoRoundTrip(t *testing.T) {
	ctx := context.Background()
	r := openTest(t).SummaryRepo()

	if _, _, ok, err := r.GetSummary(ctx, "missing"); err != nil || ok {
		t.Fatalf("unknown id must miss: ok=%v err=%v", ok, err)
	}

	// A local (sentinel) read.
	if err := r.SaveSummary(ctx, "art1", "ABCD", "local", "Local read: something happened."); err != nil {
		t.Fatalf("save local: %v", err)
	}
	sum, model, ok, err := r.GetSummary(ctx, "art1")
	if err != nil || !ok {
		t.Fatalf("get local: ok=%v err=%v", ok, err)
	}
	if sum != "Local read: something happened." || model != "local" {
		t.Fatalf("local round-trip mismatch: sum=%q model=%q", sum, model)
	}

	// A real Claude model string, on a different id.
	if err := r.SaveSummary(ctx, "art2", "WXYZ", "claude-opus-4-8", "Claude's take."); err != nil {
		t.Fatalf("save claude: %v", err)
	}
	sum2, model2, ok2, err2 := r.GetSummary(ctx, "art2")
	if err2 != nil || !ok2 {
		t.Fatalf("get claude: ok=%v err=%v", ok2, err2)
	}
	if sum2 != "Claude's take." || model2 != "claude-opus-4-8" {
		t.Fatalf("claude round-trip mismatch: sum=%q model=%q", sum2, model2)
	}

	// Re-saving the same id (e.g. a local read later overwritten by a real
	// Claude read) must update the model column, not just the text.
	if err := r.SaveSummary(ctx, "art1", "ABCD", "claude-opus-4-8", "Claude's upgraded take."); err != nil {
		t.Fatalf("save overwrite: %v", err)
	}
	sum3, model3, ok3, err3 := r.GetSummary(ctx, "art1")
	if err3 != nil || !ok3 {
		t.Fatalf("get after overwrite: ok=%v err=%v", ok3, err3)
	}
	if sum3 != "Claude's upgraded take." || model3 != "claude-opus-4-8" {
		t.Fatalf("overwrite must update both summary and model: sum=%q model=%q", sum3, model3)
	}
}
