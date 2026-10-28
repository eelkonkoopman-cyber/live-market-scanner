package summarize

import (
	"context"
	"strings"
	"testing"
)

func TestLocalModelSentinel(t *testing.T) {
	if got := NewLocal().Model(); got != "local" {
		t.Fatalf("Model() = %q, want %q", got, "local")
	}
}

// --- sentence splitting / lead extraction --------------------------------

func TestSplitSentencesHandlesAbbreviations(t *testing.T) {
	got := splitSentences("Acme Corp Inc. announced record earnings today. Shares rose 5%.")
	want := []string{
		"Acme Corp Inc. announced record earnings today.",
		"Shares rose 5%.",
	}
	if len(got) != len(want) {
		t.Fatalf("splitSentences: got %d sentences %v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sentence %d: got %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// TestLeadExtractionMultiSentenceArticle pins the "first 1-3 sentences" rule
// on a realistic multi-sentence article: a 4th sentence must never leak into
// the lead.
func TestLeadExtractionMultiSentenceArticle(t *testing.T) {
	sentences := []string{
		"Acme Corp announced a new product launch today.",
		"The device targets industrial customers.",
		"Analysts said the launch was expected.",
		"This fourth sentence should never appear in the lead.",
	}
	got := leadSentences(sentences)
	for _, s := range sentences[:3] {
		if !strings.Contains(got, strings.TrimSuffix(s, ".")) {
			t.Fatalf("lead missing sentence %q: got %q", s, got)
		}
	}
	if strings.Contains(got, "fourth sentence") {
		t.Fatalf("lead exceeded the 3-sentence cap: %q", got)
	}
}

// TestLeadExtractionWordCap pins the "~50 words, whichever is shorter" half
// of the rule: a long first sentence is still included alone (never dropped
// entirely), but a second sentence that would push the total past 50 words
// is excluded even though the sentence cap (3) hasn't been reached.
func TestLeadExtractionWordCap(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("lorem ", 55)) + "."
	sentences := []string{long, "Second sentence here.", "Third sentence here."}
	got := leadSentences(sentences)
	if got != long {
		t.Fatalf("lead should be exactly the long first sentence alone, got %q", got)
	}
}

// --- key-figure extraction -----------------------------------------------

func TestExtractKeyFiguresTruePositive(t *testing.T) {
	text := "The company priced a $50 million offering of 12,000,000 shares at a 15% discount to market."
	got := extractKeyFigures(text)
	joined := strings.Join(got, " | ")
	if !strings.Contains(joined, "$50 million") {
		t.Errorf("missing dollar figure, got %v", got)
	}
	if !strings.Contains(strings.ToLower(joined), "shares") {
		t.Errorf("missing share-count figure, got %v", got)
	}
	if !strings.Contains(joined, "15%") {
		t.Errorf("missing percent figure, got %v", got)
	}
}

func TestExtractKeyFiguresNonNumericText(t *testing.T) {
	text := "The company announced a new partnership and expanded its leadership team."
	got := extractKeyFigures(text)
	if len(got) != 0 {
		t.Fatalf("expected zero key figures from non-numeric text, got %v", got)
	}
}

// --- category flags --------------------------------------------------------

func TestDetectFlagsEachCategoryTruePositive(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"dilution", "The company announced a public offering of common stock and warrants.", "dilution/offering"},
		{"guidance", "Management raises guidance for fiscal 2026.", "guidance"},
		{"earnings", "The company beats estimates for the quarter with EPS of $0.42.", "earnings"},
		{"regulatory", "FDA approval granted after a successful phase 3 clinical trial.", "regulatory/FDA"},
		{"mna", "Acme Corp to acquire Widget Corp under a definitive agreement.", "M&A"},
		{"analyst", "An analyst upgrades the stock and raises its price target.", "analyst action"},
		{"halt", "Trading in the stock was halted pending news.", "halt/risk"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			flags, _ := detectFlags(c.text)
			if !containsStr(flags, c.want) {
				t.Fatalf("text %q: flags = %v, want to contain %q", c.text, flags, c.want)
			}
		})
	}
}

// --- negation guard --------------------------------------------------------

func TestNegationGuardDilution(t *testing.T) {
	positive := "The company announced a dilutive public offering to raise capital."
	flags, signals := detectFlags(positive)
	if !containsStr(flags, "dilution/offering") {
		t.Fatalf("true-positive dilution not flagged: flags=%v", flags)
	}
	if !containsStr(signals, "dilutive offering") {
		t.Fatalf("true-positive dilution signal missing: signals=%v", signals)
	}

	negated := "The company said it does not plan a dilutive offering at this time."
	flagsN, signalsN := detectFlags(negated)
	if containsStr(flagsN, "dilution/offering") {
		t.Fatalf("negated dilution mention must NOT be flagged: flags=%v", flagsN)
	}
	if containsStr(signalsN, "dilutive offering") {
		t.Fatalf("negated dilution mention must NOT produce a signal: signals=%v", signalsN)
	}
}

func TestNegationGuardGoingConcernHalt(t *testing.T) {
	positive := "The auditor issued a going concern warning about the company."
	flags, signals := detectFlags(positive)
	if !containsStr(flags, "halt/risk") {
		t.Fatalf("true-positive going-concern not flagged: flags=%v", flags)
	}
	if !containsStr(signals, "going concern (negative)") {
		t.Fatalf("true-positive going-concern signal missing: signals=%v", signals)
	}

	negated := "The auditor said there is no going concern doubt about the company's ability to continue."
	flagsN, signalsN := detectFlags(negated)
	if containsStr(flagsN, "halt/risk") {
		t.Fatalf("negated going-concern mention must NOT be flagged: flags=%v", flagsN)
	}
	if containsStr(signalsN, "going concern (negative)") {
		t.Fatalf("negated going-concern mention must NOT produce a signal: signals=%v", signalsN)
	}
}

// --- Summarize: thin source, integrity guard ------------------------------

func TestSummarizeHeadlineOnlyProducesHonestDisclosure(t *testing.T) {
	a := Article{Headline: "XYZ Corp announces corporate update."}
	out, err := NewLocal().Summarize(context.Background(), a)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if out == "" {
		t.Fatal("Summarize must never return an empty string")
	}
	if !strings.Contains(out, "headline only") {
		t.Fatalf("expected an honest thin-source disclosure, got %q", out)
	}
}

func TestSummarizeCompletelyEmptyArticleNeverEmptyNeverCrashes(t *testing.T) {
	out, err := NewLocal().Summarize(context.Background(), Article{})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("Summarize must never return an empty string, even for a zero-value Article")
	}
}

// TestSummarizeNeverFabricatesADirectionalStance is the single most important
// integrity test in this file: the local engine must never emit anything
// that looks like Claude's "Stance: Bullish|Bearish|Neutral" line. Narrow,
// genuinely high-confidence directional observations are allowed, but only
// under the distinctly-worded "Signal:" line.
func TestSummarizeNeverFabricatesADirectionalStance(t *testing.T) {
	a := Article{
		Headline: "Acme Corp beats estimates and raises guidance while pricing a dilutive public offering.",
		Body: "Acme Corp beats estimates for the second quarter and raises guidance for the full year. " +
			"Separately, the company priced a dilutive public offering of $50 million. Shares fell 10% after hours.",
	}
	out, err := NewLocal().Summarize(context.Background(), a)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if strings.Contains(out, "Stance:") {
		t.Fatalf("local summary must never contain a fabricated Stance line, got %q", out)
	}
	if !strings.Contains(out, "Signal:") {
		t.Fatalf("expected a Signal: line for these high-confidence keyword matches, got %q", out)
	}
}

// TestSignalGuidanceCutWithInterveningWords is a regression test for a real
// defect found in adversarial review: the guidance/estimates signal keywords
// originally matched only the bare adjacent phrase ("cuts guidance"), so
// realistic headline phrasing like "cuts full-year guidance" (a modifier word
// sitting between the verb and "guidance") silently failed to match at all —
// no category flag, no signal, nothing. mkVerbNoun's bounded word-gap fixes
// this.
func TestSignalGuidanceCutWithInterveningWords(t *testing.T) {
	flags, signals := detectFlags("The company cuts full-year guidance amid softening demand.")
	if !containsStr(flags, "guidance") {
		t.Fatalf("expected a guidance flag, got flags=%v", flags)
	}
	if !containsStr(signals, "guidance cut (negative)") {
		t.Fatalf("expected the guidance-cut signal, got signals=%v", signals)
	}
}

// TestSignalMixedBeatsEstimatesButCutsGuidanceSurfacesBothSignals is the
// adversarial-review "ambiguous mixed case" this build was specifically
// asked to hand-trace: a headline that beats on earnings but cuts guidance in
// the same breath must never collapse to a single, one-sided confident
// signal. Before the mkVerbNoun fix, "cuts full-year guidance" failed to
// match at all (see TestSignalGuidanceCutWithInterveningWords), so this exact
// headline used to silently drop the negative half and surface ONLY "earnings
// beat (positive)" — a materially misleading one-sided read of genuinely
// mixed news. Both signals must now appear, in either order, and neither a
// fabricated single-direction "Stance:" line nor any single merged verdict
// may appear.
func TestSignalMixedBeatsEstimatesButCutsGuidanceSurfacesBothSignals(t *testing.T) {
	a := Article{Headline: "Widget Corp beats estimates for the third quarter but cuts full-year guidance amid softening demand."}
	out, err := NewLocal().Summarize(context.Background(), a)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if strings.Contains(out, "Stance:") {
		t.Fatalf("must never fabricate a Stance line, got %q", out)
	}
	if !strings.Contains(out, "earnings beat (positive)") {
		t.Fatalf("missing the earnings-beat signal: %q", out)
	}
	if !strings.Contains(out, "guidance cut (negative)") {
		t.Fatalf("missing the guidance-cut signal (this is the regression this test guards): %q", out)
	}
}

// TestSignalEstimatesBeatWithInterveningWords pins the same gap-tolerance for
// the earnings category: "beats Wall Street estimates" is far more common
// real-world phrasing than the bare "beats estimates".
func TestSignalEstimatesBeatWithInterveningWords(t *testing.T) {
	flags, signals := detectFlags("The company beats Wall Street estimates for the quarter.")
	if !containsStr(flags, "earnings") {
		t.Fatalf("expected an earnings flag, got flags=%v", flags)
	}
	if !containsStr(signals, "earnings beat (positive)") {
		t.Fatalf("expected the earnings-beat signal, got signals=%v", signals)
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
