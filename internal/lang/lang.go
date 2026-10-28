// Package lang decides whether a news headline is one of Benzinga's
// translated foreign-language editions. Motivation (measured against the
// user's own scanner.db, 2026-08-03): ~53% of the Benzinga WebSocket stream is
// translated editions (ja/ko/it/zh/es/de/fr/pt — 727 CJK articles in the last
// 3000 captured), no server-side language filter exists in any Benzinga or
// Massive API, and the only per-frame marker (content.type, e.g.
// "benzinga_wire_korea") is undocumented and not enumerable for English. So
// the headline text itself has to carry the decision.
//
// THE ASYMMETRY IS THE DESIGN: this package decides what news the user never
// sees, so an ENGLISH headline wrongly dropped (real news silently lost) is
// strictly worse than a foreign one kept (noise the eye skips). Every rule
// below ties, defaults and knife-edges toward "keep".
//
// Two layers, cheapest and most certain first:
//
//  1. SCRIPT SHARE. If more than maxNonLatinShare of the letters belong to a
//     non-Latin script (Han, Hiragana, Katakana, Hangul, Cyrillic, Greek,
//     Thai, Arabic, Hebrew), the headline is foreign outright — this is
//     ja/ko/zh/ru, unambiguous at any threshold. The threshold sits LOW (0.2,
//     not 0.5) because Japanese and Korean finance headlines are stuffed with
//     Latin brand names and tickers ("CEO", "AAPL", "IPO") that would dilute a
//     majority test; while an English headline quoting a CJK brand name
//     ("Alibaba's 淘宝 unit...") carries only a couple of non-Latin letters
//     against a full English sentence, far under 0.2.
//
//  2. STOPWORD DUEL for the Latin-script editions (it/es/de/fr/pt), which
//     layer 1 cannot see. Small per-language sets of high-frequency function
//     words are matched as lowercase whole words. The verdict is foreign only
//     when BOTH hold:
//
//     (a) at least minStopwordHits DISTINCT stopwords hit from the SAME
//     language — distinct, because English corporate names repeat one
//     particle ("Companhia de Saneamento de Minas Gerais") without ever
//     producing a second one, while a real foreign sentence cannot avoid
//     using several different function words; and same-language, never
//     pooled across languages, because es and pt share "de"/"para" and
//     "con"/"com" — pooling would let two languages' single hits sum to a
//     false 2 on an English headline that merely names two foreign things.
//
//     (b) that language's distinct hits STRICTLY EXCEED the distinct English
//     hits. An English headline containing "de la" (De La Rue, tickers
//     DE/LA/EN, "Des Moines" + "EST") still contains English function
//     words; a genuinely Italian headline contains none. Ties go to
//     English — see the asymmetry above.
//
//     (c) CASE IS EVIDENCE (found by the adversarial English sweep, which
//     dropped "Del Taco Opens 50 New Los Angeles Locations", ticker lists
//     like "Movers: DE, LA Among Top Gainers", and EDGAR headlines naming
//     ALL-CAPS Latin-American filers — "COMPANHIA DE SANEAMENTO ... DO
//     ESTADO DE SAO PAULO"). A real foreign function word sits lowercase
//     mid-sentence; an uppercase or Title-Case token in an otherwise
//     mixed-case headline is a ticker or a proper-noun particle. So in a
//     headline that contains ANY lowercase letter, only fully-lowercase
//     tokens may count as foreign stopwords. A headline with no lowercase
//     letters at all (an ALL-CAPS wire) carries no case signal, and there
//     every token still counts — the all-caps Italian edition stays
//     caught. English stopwords count regardless of case (they only ever
//     vote KEEP, so case can't cost an English headline). Residual risk: a
//     foreign headline written in Title Case would slip through — the
//     translated editions use sentence case, and a foreign headline kept
//     is the acceptable side of the asymmetry.
//
//     The English set holds the classic function words PLUS a few
//     high-frequency English finance-headline content words (announces,
//     reports, shares, plc, ...). The extension exists for exactly one shape
//     of headline: an English corporate announcement whose name embeds two
//     foreign particles and whose terse remainder has no room for "the" or
//     "of" — "De La Rue plc Announces Half-Year Results" is the canonical
//     capture-adjacent case. The extension can only ever vote KEEP, so it
//     cannot cost a single English headline; a genuinely foreign headline
//     says these words in its own language (annuncia, resultados, Aktien)
//     and is untouched by them.
//
// Tokenization splits on any non-letter and compares lowercase, so all-caps
// regulatory headlines ("FERRARI N.V.: PERIODIC REPORT ON THE BUYBACK
// PROGRAM") still surface their English stopwords, and accented Latin letters
// (für, não) stay inside their word. No letters at all ("$BTC +5%", empty
// string) means nothing to judge: keep.
//
// Pure functions, no I/O, no state, no dependencies. Runs per headline (a few
// dozen/sec peak), not per tick — clarity beats micro-optimization here.
package lang

import (
	"strings"
	"unicode"
)

const (
	// maxNonLatinShare: share of letters in a non-Latin script above which the
	// headline is foreign outright. Low on purpose — Japanese/Korean finance
	// headlines are full of Latin tickers and brand names, so a majority test
	// would miss them, while an English headline quoting one CJK name sits far
	// below 0.2. "Exceeds", strictly: a knife-edge headline stays English.
	maxNonLatinShare = 0.20
	// minStopwordHits: distinct same-language foreign stopwords required
	// before a Latin-script headline can even be suspected. One hit is a name
	// particle ("Banco de Chile", "LA Rams"); two DIFFERENT ones from the same
	// language start to look like grammar.
	minStopwordHits = 2
)

// nonLatinScripts are the scripts of the Benzinga translated editions that
// layer 1 can catch on sight (ja/ko/zh/ru and neighbours). Latin-script
// editions (it/es/de/fr/pt) are invisible here and fall to the stopword duel.
var nonLatinScripts = []*unicode.RangeTable{
	unicode.Han,
	unicode.Hiragana,
	unicode.Katakana,
	unicode.Hangul,
	unicode.Cyrillic,
	unicode.Greek,
	unicode.Thai,
	unicode.Arabic,
	unicode.Hebrew,
}

// foreignStopwords: per-language high-frequency function words, lowercase.
// Kept SMALL deliberately — each word is one more chance for an English
// headline to hit, so only the words a foreign sentence can hardly avoid.
// es/pt overlap ("de", "para", "com"/"con") is why hits are counted per
// language and never pooled — see the package doc.
var foreignStopwords = map[string]map[string]struct{}{
	"it": wordSet("di dei della delle nel sulla per che come gli"),
	"es": wordSet("de la el los las para por con del una"),
	"de": wordSet("der die das und für mit von auf eine nicht"),
	"fr": wordSet("le les des une pour avec sur dans est"),
	"pt": wordSet("de da do dos em para com uma não os"),
}

// englishStopwords: the classic function words, plus the finance-headline
// content-word extension (second line). The extension can only vote KEEP —
// rationale in the package doc.
var englishStopwords = wordSet(
	"the of to and in on for at with from says after amid as is are " +
		"announces reports shares stock inc plc corp ltd earnings results")

// Foreign reports whether headline reads as one of the feed's translated
// foreign-language editions. False means KEEP: an empty, symbols-only or
// merely ambiguous headline is never dropped — see the package doc for why
// every tie breaks that way.
func Foreign(headline string) bool {
	// Layer 1: script share.
	letters, nonLatin := 0, 0
	for _, r := range headline {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		for _, script := range nonLatinScripts {
			if unicode.Is(script, r) {
				nonLatin++
				break
			}
		}
	}
	if letters == 0 {
		return false // "$BTC +5%", "", "25%": nothing to judge — keep
	}
	if float64(nonLatin) > maxNonLatinShare*float64(letters) {
		return true
	}

	// Layer 2: the stopword duel. Tokens keep their original case so rule (c)
	// can tell a mid-sentence function word ("de") from a ticker or name
	// particle ("DE", "Del") — see the package doc. caseless: an ALL-CAPS wire
	// has no case signal, so there every token stays eligible.
	caseless := !strings.ContainsFunc(headline, unicode.IsLower)
	var langHits map[string]map[string]struct{} // language -> distinct hits
	var englishHits map[string]struct{}
	for _, w := range strings.FieldsFunc(headline, notLetter) {
		lw := strings.ToLower(w)
		if _, ok := englishStopwords[lw]; ok {
			if englishHits == nil {
				englishHits = make(map[string]struct{})
			}
			englishHits[lw] = struct{}{} // any case: English hits only vote KEEP
		}
		if !caseless && w != lw {
			continue // rule (c): cased token in a mixed-case headline = name/ticker
		}
		for code, set := range foreignStopwords {
			if _, ok := set[lw]; ok {
				if langHits == nil {
					langHits = make(map[string]map[string]struct{})
				}
				if langHits[code] == nil {
					langHits[code] = make(map[string]struct{})
				}
				langHits[code][lw] = struct{}{}
			}
		}
	}

	best := 0 // max over languages, never a pooled sum
	for _, hits := range langHits {
		if len(hits) > best {
			best = len(hits)
		}
	}
	return best >= minStopwordHits && best > len(englishHits)
}

func notLetter(r rune) bool { return !unicode.IsLetter(r) }

// wordSet turns a space-separated word list into a membership set.
func wordSet(words string) map[string]struct{} {
	m := make(map[string]struct{}, 16)
	for _, w := range strings.Fields(words) {
		m[w] = struct{}{}
	}
	return m
}
