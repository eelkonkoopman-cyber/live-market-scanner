package summarize

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// LocalModel is LocalSummarizer.Model()'s return value. It is a sentinel,
// never a real Anthropic model string — callers use it to tell "this summary
// was a free local read" from "this summary cost a Claude call" (see
// cmd/scanner/main.go's manual "news.summarize" handler, which must not treat
// a cached local read as a substitute for an explicitly requested Claude
// read).
const LocalModel = "local"

// LocalSummarizer produces trader-focused news summaries WITHOUT calling any
// LLM — no Claude, no API key, no per-article cost. It implements the same
// interface as Service (internal/autosum's Summarizer), so it can be dropped
// into autosum.Config.Sum as a drop-in, zero-cost default.
//
// Design: extractive + rule-based, not generative. Financial news is
// inverted-pyramid (the lead sentence(s) almost always carry the core fact),
// so a lead extraction gets most of the value a generative summary would,
// and a small regex lexicon can pull the dealbreaker numbers (offering size,
// dilution %) with very low false-positive risk — it is pure pattern
// matching on digits, no judgment involved.
//
// What this deliberately does NOT do: emit a directional call ("Stance:
// Bullish/Bearish/Neutral"). Claude's prompt asks for exactly that because a
// careful reader (human or LLM) can weigh context a keyword match cannot — a
// "miss" that was already priced in, a dilutive raise that funds a wanted
// catalyst, and so on. A rule-based system faking that same confidence would
// be actively worse than saying nothing, since a trader treats a stated
// stance as considered judgment. So LocalSummarizer surfaces only the narrow
// set of category flags where the *label itself* is genuinely high-
// confidence pattern matching ("beats estimates" is either in the text or it
// isn't) under a distinctly-named "Signal:" line — never anything that reads
// like Claude's "Stance:" line.
type LocalSummarizer struct{}

// NewLocal builds the local engine. It holds no state and needs no config.
func NewLocal() *LocalSummarizer { return &LocalSummarizer{} }

// Model reports the sentinel model name — see LocalModel's doc comment.
func (l *LocalSummarizer) Model() string { return LocalModel }

// Summarize implements autosum.Summarizer. It never errors on its own
// account (there is no backend to fail) and never returns an empty string —
// a bare headline still produces a short, honest sentence.
func (l *LocalSummarizer) Summarize(ctx context.Context, a Article) (string, error) {
	// Reuses summarize.go's existing sourceText priority (body -> fetched
	// URL -> teaser -> headline-only) so "no Claude API calls anywhere in
	// this path" is true by construction: the only network fetch left is the
	// SAME pre-existing article-URL fetch the Claude path already falls back
	// to, not a new external dependency.
	text := sourceText(ctx, a)

	headline := strings.TrimSpace(a.Headline)
	full := headline
	if text != "" {
		full = headline + "\n" + text
	}

	sentenceSrc := text
	if sentenceSrc == "" {
		sentenceSrc = headline
	}
	lead := leadSentences(splitSentences(sentenceSrc))
	if lead == "" {
		lead = headline
	}

	figures := extractKeyFigures(full)
	flags, signals := detectFlags(full)

	var b strings.Builder
	if lead != "" {
		b.WriteString(lead)
	}
	if len(figures) > 0 {
		fmt.Fprintf(&b, "\nKey figures: %s", strings.Join(figures, "; "))
	}
	if len(flags) > 0 {
		fmt.Fprintf(&b, "\nFlags: %s", strings.Join(flags, ", "))
	}
	if len(signals) > 0 {
		fmt.Fprintf(&b, "\nSignal: %s", strings.Join(signals, "; "))
	}
	if text == "" && headline != "" {
		// Headline-only source: an honest disclosure, never silence and
		// never a fabricated read of text that doesn't exist.
		b.WriteString("\n(Local read: headline only — no article body, fetched page, or teaser was available; this is a thin read.)")
	}

	out := strings.TrimSpace(b.String())
	if out == "" {
		// Absolute last resort (e.g. an entirely empty Article): mirror
		// summarize.go's own empty-output guard — never empty, never silent.
		out = "(Local read: no summarizable text was available for this article.)"
	}
	return out, nil
}

// --- sentence splitting -----------------------------------------------

// abbreviations is the exception list a naive "split on . ! ?" would
// otherwise cut mid-sentence on. Not exhaustive — "good enough for lead
// extraction" is the goal, not a perfect splitter.
var abbreviations = map[string]bool{
	"inc": true, "corp": true, "co": true, "ltd": true, "llc": true, "plc": true,
	"u.s": true, "u.k": true, "u.s.a": true,
	"mr": true, "mrs": true, "ms": true, "dr": true, "jr": true, "sr": true, "st": true,
	"vs": true, "etc": true, "approx": true, "no": true, "fy": true,
	"q1": true, "q2": true, "q3": true, "q4": true,
}

// sentenceSplitRe grabs runs of non-terminator text ending in one or more
// terminators (or end of string) as a candidate sentence.
var sentenceSplitRe = regexp.MustCompile(`[^.!?]+(?:[.!?]+|$)`)

// splitSentences pragmatically splits text into sentences, re-merging a
// split that landed right after a known abbreviation ("Inc.", "U.S.", "Q1.")
// back onto the following clause.
func splitSentences(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	raw := sentenceSplitRe.FindAllString(text, -1)
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if len(out) > 0 && endsWithAbbrev(out[len(out)-1]) {
			out[len(out)-1] = out[len(out)-1] + " " + s
			continue
		}
		out = append(out, s)
	}
	return out
}

// endsWithAbbrev reports whether s ends in one of abbreviations' entries
// immediately before its trailing period (case-insensitive).
func endsWithAbbrev(s string) bool {
	s = strings.TrimSpace(s)
	if !strings.HasSuffix(s, ".") {
		return false
	}
	trimmed := strings.TrimSuffix(s, ".")
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return false
	}
	last := strings.Trim(fields[len(fields)-1], `()[]"'`)
	return abbreviations[strings.ToLower(last)]
}

// leadWordCap is the "or ~50 words, whichever is shorter" budget from the
// design: the lead always includes at least the first sentence (even if that
// alone runs long), but never adds a further sentence once the running word
// count would exceed the cap, and never takes more than leadSentenceCap
// sentences regardless.
const (
	leadWordCap     = 50
	leadSentenceCap = 3
)

// leadSentences takes the first 1-3 sentences (or ~50 words, whichever is
// shorter) as the "what happened" core. Financial news is inverted-pyramid —
// the lead almost always carries the core fact.
func leadSentences(sentences []string) string {
	if len(sentences) == 0 {
		return ""
	}
	var b strings.Builder
	words := 0
	for i, s := range sentences {
		if i >= leadSentenceCap {
			break
		}
		wc := len(strings.Fields(s))
		if i > 0 && words+wc > leadWordCap {
			break
		}
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString(s)
		words += wc
	}
	return strings.TrimSpace(b.String())
}

// --- key-figure extraction ---------------------------------------------

// Dollar amounts, share counts, and percentages: exactly the "offering
// size/price/dilution %" detail the existing Claude prompt calls out as
// essential, and the one thing a rule-based system can extract with LOW
// false-positive risk — it's pattern matching on numbers, no judgment
// involved.
var (
	reDollar  = regexp.MustCompile(`(?i)\$\s?[0-9][0-9,]*(?:\.[0-9]+)?\s?(?:million|billion|thousand|mm|bn)?\b`)
	reShares  = regexp.MustCompile(`(?i)\b[0-9][0-9,]*(?:\.[0-9]+)?\s?(?:million|billion|thousand)?\s+shares\b`)
	rePercent = regexp.MustCompile(`\b[0-9]+(?:\.[0-9]+)?\s?%`)
)

// maxKeyFigures caps the figures line so a filing dense with numbers doesn't
// dominate the summary — a handful of the first-mentioned ones is enough to
// flag "go read the filing", which is the whole point of this line.
const maxKeyFigures = 6

// extractKeyFigures pulls dollar amounts, share counts, and percentages from
// the FULL source text (not just the lead) — offering terms are routinely
// buried well past the first sentence.
func extractKeyFigures(text string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(matches []string) {
		for _, m := range matches {
			m = strings.TrimSpace(m)
			if m == "" || seen[m] {
				continue
			}
			seen[m] = true
			out = append(out, m)
			if len(out) >= maxKeyFigures {
				return
			}
		}
	}
	add(reDollar.FindAllString(text, -1))
	if len(out) < maxKeyFigures {
		add(reShares.FindAllString(text, -1))
	}
	if len(out) < maxKeyFigures {
		add(rePercent.FindAllString(text, -1))
	}
	return out
}

// --- category flags + negation guard -------------------------------------

// flagKeyword is one lexicon entry: a compiled whole-word/phrase matcher and,
// for the narrow subset of keywords that ARE a genuinely high-confidence
// directional signal (e.g. "beats estimates" vs "misses estimates"), the
// label to surface on the separate "Signal:" line. Most keywords carry no
// signal — they only earn the article a category flag.
type flagKeyword struct {
	re     *regexp.Regexp
	signal string // "" = category flag only, no directional signal
}

type flagCategory struct {
	name     string
	keywords []flagKeyword
}

// mkKeyword compiles phrase into a case-insensitive, whole-word/phrase
// regex (spaces become flexible whitespace so "public offering" also matches
// "public  offering" across a line wrap).
func mkKeyword(phrase, signal string) flagKeyword {
	pat := strings.ReplaceAll(regexp.QuoteMeta(phrase), " ", `\s+`)
	return flagKeyword{re: regexp.MustCompile(`(?i)\b` + pat + `\b`), signal: signal}
}

// wordGap tolerates a handful of words sitting between a directional verb and
// the noun it modifies — real headlines routinely read "cuts full-year
// guidance" or "beats Wall Street estimates", not the bare adjacent phrase a
// literal mkKeyword match assumes. Bounded (at most 4 words) and
// non-greedy so it still reports the FIRST plausible pairing rather than
// spanning an entire paragraph.
const wordGap = `(?:\s+\S+){0,4}?`

// mkVerbNoun builds a case-insensitive whole-word matcher for "<one of
// verbAlt> ... <one of nounAlt>" tolerating wordGap between them. Used only
// for the guidance/estimates directional signals: a strict adjacent-phrase
// match on these specifically (unlike the mostly-single-word dilution/
// regulatory/M&A/analyst keywords) silently drops the negative half of a
// genuinely mixed headline like "beats estimates but cuts full-year
// guidance" — a real integrity risk for a feature whose one hard rule is
// never look more confident/one-sided than the source actually supports.
func mkVerbNoun(verbAlt, nounAlt, signal string) flagKeyword {
	// wordGap matches zero or more WHOLE filler words, each already carrying
	// its own leading \s+; the mandatory \s+ right after it is the separator
	// before the noun itself (present even at zero fillers, since verb and
	// noun are always two distinct words). Folding that separator into the
	// noun's own leading boundary instead (i.e. `wordGap + \b + noun`) is a
	// trap: at zero fillers a bare \b can't cross the space between verb and
	// noun, and at one-or-more fillers the non-greedy gap swallows the noun
	// itself as its own last "filler" before ever trying to match it as the
	// noun — either way the match silently fails, even on the plain adjacent
	// phrase. Keeping the separator explicit here is what makes both the
	// zero-gap ("beats estimates") and gapped ("cuts full-year guidance")
	// cases actually match.
	pat := `(?i)\b(?:` + verbAlt + `)\b` + wordGap + `\s+(?:` + nounAlt + `)\b`
	return flagKeyword{re: regexp.MustCompile(pat), signal: signal}
}

// flagCategories is the keyword lexicon. Category order is the order flags
// are reported in, kept stable for predictable output.
var flagCategories = []flagCategory{
	{
		name: "dilution/offering",
		keywords: []flagKeyword{
			mkKeyword("public offering", "dilutive offering"),
			mkKeyword("private placement", "dilutive offering"),
			mkKeyword("registered direct", "dilutive offering"),
			mkKeyword("warrants", ""),
			mkKeyword("dilutive", "dilutive offering"),
			mkKeyword("dilution", "dilutive offering"),
			mkKeyword("at-the-market", "dilutive offering"),
			// Bare "ATM" is explicitly in the required lexicon even though it
			// risks matching an unrelated "ATM" mention (cash machine, etc.) —
			// there's no negation to guard against a wrong-topic match, only
			// a wrong-polarity one, so this one keyword trades a little
			// precision for the recall the design calls for.
			mkKeyword("ATM", "dilutive offering"),
			mkKeyword("shelf registration", ""),
			mkKeyword("convertible notes", ""),
			mkKeyword("convertible note", ""),
		},
	},
	{
		name: "guidance",
		keywords: []flagKeyword{
			// verb...guidance, gap-tolerant (see mkVerbNoun) so "raises
			// full-year guidance" / "cuts its 2026 guidance" match, not
			// just the bare adjacent phrase.
			mkVerbNoun("raises?|raised|boosts?|boosted|hikes?|hiked|increases?|increased", "guidance", "guidance raised (positive)"),
			mkVerbNoun("cuts?|cut|lowers?|lowered|slashes?|slashed|trims?|trimmed|narrows?|narrowed|reduces?|reduced|withdraws?|withdrew|withdrawn", "guidance", "guidance cut (negative)"),
			mkKeyword("reaffirms guidance", ""),
			mkKeyword("guidance", ""),
		},
	},
	{
		name: "earnings",
		keywords: []flagKeyword{
			// verb...estimates/expectations/consensus, gap-tolerant so
			// "beats Wall Street estimates" / "missed analysts'
			// expectations" match, not just the bare adjacent phrase.
			mkVerbNoun("beats?|beat|tops?|topped|exceeds?|exceeded", "estimates?|expectations|consensus", "earnings beat (positive)"),
			mkVerbNoun(`misses?|missed|falls?\s+short(?:\s+of)?`, "estimates?|expectations|consensus", "earnings miss (negative)"),
			mkKeyword("EPS", ""),
			mkKeyword("revenue of", ""),
		},
	},
	{
		name: "regulatory/FDA",
		keywords: []flagKeyword{
			mkKeyword("FDA", ""),
			mkKeyword("approval", ""),
			mkKeyword("clinical trial", ""),
			mkKeyword("phase 1", ""),
			mkKeyword("phase 2", ""),
			mkKeyword("phase 3", ""),
			mkKeyword("complete response letter", ""),
		},
	},
	{
		name: "M&A",
		keywords: []flagKeyword{
			mkKeyword("acquire", ""),
			mkKeyword("acquisition", ""),
			mkKeyword("merger", ""),
			mkKeyword("definitive agreement", ""),
		},
	},
	{
		name: "analyst action",
		keywords: []flagKeyword{
			mkKeyword("upgrades", ""),
			mkKeyword("downgrades", ""),
			mkKeyword("price target", ""),
			mkKeyword("initiates coverage", ""),
		},
	},
	{
		name: "halt/risk",
		keywords: []flagKeyword{
			mkKeyword("halted", "trading halt (negative)"),
			mkKeyword("going concern", "going concern (negative)"),
			mkKeyword("delisting", "delisting risk (negative)"),
			mkKeyword("bankruptcy", "bankruptcy risk (negative)"),
			mkKeyword("restatement", "restatement (negative)"),
		},
	},
}

// negationWindow is how many characters BEFORE a keyword match are scanned
// for a negation word — a short window on purpose (this is a simple guard,
// not a parser): enough to catch "does not plan a dilutive offering" without
// reaching back far enough to false-suppress an unrelated earlier negation.
const negationWindow = 40

// negationRe matches a negation word/contraction as a whole word: "not",
// "no", "without", "denies"/"deny"/"denied", "never", or an "...n't"
// contraction (doesn't, hasn't, isn't, ...).
var negationRe = regexp.MustCompile(`(?i)\b(not|no|without|denies|deny|denied|never)\b|\w+n't\b`)

// negatedBefore reports whether a negation word appears in the
// negationWindow characters immediately preceding byte offset matchStart in
// text — the guard that keeps "the company does not plan a dilutive
// offering" from being tagged dilution.
func negatedBefore(text string, matchStart int) bool {
	start := matchStart - negationWindow
	if start < 0 {
		start = 0
	}
	return negationRe.MatchString(text[start:matchStart])
}

// detectFlags scans full (headline + source text) against flagCategories and
// returns the category names that matched (order-stable) plus the narrow
// high-confidence directional signals among them (deduped, order-stable).
// Every match is passed through negatedBefore first, so a negated mention
// (see the doc comment above) contributes to neither list.
func detectFlags(full string) (flags []string, signals []string) {
	seenSignal := map[string]bool{}
	for _, cat := range flagCategories {
		matched := false
		for _, kw := range cat.keywords {
			for _, loc := range kw.re.FindAllStringIndex(full, -1) {
				if negatedBefore(full, loc[0]) {
					continue
				}
				matched = true
				if kw.signal != "" && !seenSignal[kw.signal] {
					seenSignal[kw.signal] = true
					signals = append(signals, kw.signal)
				}
			}
		}
		if matched {
			flags = append(flags, cat.name)
		}
	}
	return flags, signals
}
