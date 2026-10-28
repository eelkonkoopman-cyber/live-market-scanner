package lang

import "testing"

// The convention throughout: headlines marked REAL CAPTURE are verbatim from
// this app's own Benzinga stream (scanner.db, 2026-08); the rest are
// constructed but shaped like the feed's real output. False positives on
// English are the catastrophic failure mode (news silently lost), so the
// English negatives below are the decisive set, not the foreign positives.

// --- layer 1: script share ---------------------------------------------------

func TestForeignScriptPositives(t *testing.T) {
	cases := []struct {
		name, headline string
	}{
		// REAL CAPTURE (ja): mostly CJK with Latin "CEO" mixed in — the reason
		// the threshold is a low share, not a majority.
		{"ja real", "アップル決算の予測市場プレビュー：ティム・クックCEO最後の決算説明会で何を語るか？"},
		// REAL CAPTURE (ko).
		{"ko real", "국장 한 달 새 28% 피바다..."},
		// Constructed (zh).
		{"zh constructed", "苹果公司公布第三季度财报，营收超出华尔街预期"},
		// Constructed (ru).
		{"ru constructed", "Акции Газпрома выросли на фоне роста цен на газ"},
		// Constructed (ar) — exercises a table beyond the CJK/Cyrillic ones.
		{"ar constructed", "أسهم أرامكو ترتفع بعد إعلان الأرباح الفصلية"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !Foreign(c.headline) {
				t.Fatalf("non-Latin-script headline must be foreign: %q", c.headline)
			}
		})
	}
}

func TestScriptShareBoundaries(t *testing.T) {
	// 50/50 mixed script (4 Latin letters, 4 Katakana): well over the 0.2
	// share — a headline half-written in Katakana is the ja edition whatever
	// tickers it quotes.
	if !Foreign("AAPL アップル") {
		t.Fatal("50/50 Latin/Katakana must be foreign")
	}
	// An English sentence quoting one CJK brand name: 2 Han letters against
	// 25 Latin ones, far under the share — must survive.
	if Foreign("Alibaba's 淘宝 unit posts record GMV") {
		t.Fatal("English headline quoting a CJK name must not be dropped")
	}
	// Knife edge: exactly 20% non-Latin (1 Han, 4 Latin). "Exceeds" is
	// strict, and ties break toward keeping — the package's one asymmetry.
	if Foreign("ABCD 宝") {
		t.Fatal("exactly-at-threshold share must stay English (ties keep)")
	}
}

// --- layer 2: the stopword duel ----------------------------------------------

func TestForeignStopwordPositives(t *testing.T) {
	cases := []struct {
		name, headline string
	}{
		// REAL CAPTURE (it): dei/per/di = 3 distinct Italian hits, 0 English.
		{"it real", "Crollo dei titoli legati ai chip a Wall Street: per l'analista la domanda di semiconduttori IA super"},
		// Constructed (it, ALL CAPS): regulatory-wire shouting must still be
		// recognized — proves matching happens on the lowercased headline.
		{"it all-caps constructed", "CROLLO DEI TITOLI: PER L'ANALISTA LA DOMANDA DI CHIP RESTA DEBOLE"},
		// Constructed (es).
		{"es constructed", "Las acciones de Nvidia suben tras los resultados del trimestre"},
		// Constructed (de).
		{"de constructed", "Der DAX schließt im Minus und die Anleger warten auf die Zinsentscheidung"},
		// Constructed (fr).
		{"fr constructed", "Le CAC 40 recule avec les valeurs bancaires sur des craintes pour la croissance"},
		// Constructed (pt).
		{"pt constructed", "Ações do Ibovespa caem com os resultados da Petrobras para o trimestre"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !Foreign(c.headline) {
				t.Fatalf("foreign Latin-script headline must be foreign: %q", c.headline)
			}
		})
	}
}

// TestEnglishSurvivors is THE decisive set: every one of these is English news
// the user must never lose, and each is shaped like a past false-positive
// trap — name particles, foreign-looking tickers, all-caps wires, terse
// corporate boilerplate.
func TestEnglishSurvivors(t *testing.T) {
	cases := []struct {
		name, headline string
	}{
		// All-caps wire: its English stopwords (THE, ON) only exist after
		// lowercasing. Zero foreign hits besides — the fail-then-fix control.
		{"all-caps buyback", "FERRARI N.V.: PERIODIC REPORT ON THE BUYBACK PROGRAM"},
		{"reg NAV wire", "REG-First Trust Global Funds PLC Net Asset Value(s)"},
		{"long newsroom headline", "Cathie Wood Snaps Up $14.3 Million of Meta Stock After Post-Earnings Selloff, Ark Sheds Caterpillar Shares"},
		// DE is Deere's ticker, not German/Spanish "de".
		{"DE ticker", "DE: Deere Q3 Earnings Top Estimates"},
		// LA as a place name: one Spanish hit is a name particle, not grammar.
		{"LA place name", "LA Rams Parent Co. Explores Stadium District Expansion"},
		// The canonical two-particle corporate name: es hits de+la (2), but
		// the English content-word extension (plc, announces, results)
		// outvotes them — the case the extension exists for.
		{"De La Rue", "De La Rue plc Announces Half-Year Results"},
		// "Casa" is a name, not a stopword — zero hits anywhere.
		{"Casa Systems", "Casa Systems Announces Nasdaq Listing Compliance"},
		// "Des Moines" + "EST" = two distinct FRENCH hits inside pure English;
		// the English hits must outnumber them.
		{"Des Moines EST", "Principal Financial Group of Des Moines Schedules Earnings Call at 10 a.m. EST"},
		{"short halt", "AAPL halted"},
		{"short fed", "Fed cuts 25bps"},
		// Single-particle names: distinct counting means repeating "de" in a
		// Latin-American corporate name never reaches 2.
		{"repeated particle", "Companhia de Saneamento de Minas Gerais Prices Offering"},

		// The next block was found DROPPED by the adversarial English sweep
		// (2026-08-03) and is the reason for rule (c), case-as-evidence: a
		// cased token in a mixed-case headline is a ticker or name particle,
		// never a foreign function word.
		// Ticker lists: DE/LA/EL/DO/UNA/CON/EM/OS are all real symbols and all
		// collide with es/pt stopwords.
		{"ticker list movers", "Movers: DE, LA Among Top Gainers"},
		{"ticker list losers", "Top Losers: EL, DO, UNA"},
		{"ticker list watch", "Watchlist: DE, CON, EM, OS"},
		{"ticker list inline", "DE, LA, EN among today's movers"},
		// Title-Case US proper nouns built from Spanish particles.
		{"Del Taco Los Angeles", "Del Taco Opens 50 New Los Angeles Locations"},
		{"Las Vegas El Paso", "Las Vegas Sands, El Paso Electric Among Movers"},
		{"El Paso La Quinta", "El Paso Electric, La Quinta Lead Gainers"},
		// Quoted foreign title inside English: 'La' is cased, 'de' alone is 1.
		{"quoted foreign title", "Netflix's 'La Casa de Papel' Spinoff Drives Subscriber Beat"},
		// EDGAR all-filings headline naming an ALL-CAPS Latin-American filer:
		// SABESP (NYSE: SBS) files 6-Ks constantly; before rule (c) its DE+DO
		// hits outvoted zero English hits and the filing was silently gated.
		{"EDGAR SABESP", "SEC filing: 6-K — COMPANHIA DE SANEAMENTO BASICO DO ESTADO DE SAO PAULO-SABESP (SBS)"},
		{"EDGAR Banco de Chile", "SEC filing: 20-F (financials) — Banco de Chile (BCH)"},
		// rtpr/GlobeNewswire English PR shape with a foreign corporate name.
		{"rtpr foreign name", "Vinci Partners de Brasil Announces Secondary Offering"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if Foreign(c.headline) {
				t.Fatalf("ENGLISH headline wrongly dropped — the catastrophic failure: %q", c.headline)
			}
		})
	}
}

func TestStopwordDuelBoundaries(t *testing.T) {
	// Exactly 2 distinct Italian hits (dei, che) vs 1 English (after):
	// 2 >= minStopwordHits and 2 > 1 — foreign by the thinnest margin.
	if !Foreign("Analisi dei mercati: che succede a Wall Street after hours") {
		t.Fatal("2 same-language hits beating 1 English hit must be foreign")
	}
	// Same headline with the English side reaching a TIE (after, the): strict
	// ">" means the tie keeps it — English is never outvoted by an equal.
	if Foreign("Analisi dei mercati che muovono Wall Street AFTER THE BELL") {
		t.Fatal("a tie between foreign and English hits must keep the headline")
	}
}

// TestCaseEvidenceBoundaries pins rule (c)'s two edges: an ALL-CAPS headline
// has no case signal (every token stays eligible — the all-caps Italian wire
// must still be caught), while ONE lowercase letter anywhere restores the
// cased-token exclusion.
func TestCaseEvidenceBoundaries(t *testing.T) {
	// No lowercase letters at all: DEI/PER/LA/DI must still count — dropping
	// the caseless carve-out would silently lose the all-caps foreign wires.
	if !Foreign("CROLLO DEI TITOLI: PER L'ANALISTA LA DOMANDA DI CHIP RESTA DEBOLE") {
		t.Fatal("ALL-CAPS Italian wire must remain foreign (caseless headlines keep every token eligible)")
	}
	// The same Spanish-particle tokens as the ticker-list traps, but genuinely
	// lowercase mid-sentence: still foreign.
	if !Foreign("Las acciones de Nvidia suben tras los resultados del trimestre") {
		t.Fatal("lowercase mid-sentence particles are real grammar and must stay foreign")
	}
	// One lowercase word is enough to flip the headline out of caseless mode:
	// the cased DE/LA tokens go back to being tickers.
	if Foreign("DE, LA among MOVERS") {
		t.Fatal("a mixed-case headline must treat cased DE/LA as tickers, not stopwords")
	}
}

func TestNoPoolingAcrossLanguages(t *testing.T) {
	// One German hit (der) + one French hit (le) in an otherwise stopword-free
	// English headline: pooled they'd fake the 2-hit minimum, per-language
	// they are two singles. Must survive.
	if Foreign("Der Spiegel Report Lifts Le Monde Parent's Ad Outlook") {
		t.Fatal("single hits from different languages must not pool to a false 2")
	}
}

func TestDegenerateInputs(t *testing.T) {
	for _, h := range []string{"", "$BTC +5%", "25 100 3.14", "!!! --- %%%"} {
		if Foreign(h) {
			t.Fatalf("no letters means nothing to judge — must keep: %q", h)
		}
	}
}
