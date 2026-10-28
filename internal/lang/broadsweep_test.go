package lang

import "testing"

// TestEnglishBroadSweep is the adversarial breadth corpus (2026-08-03): 100+
// realistic English finance headlines spanning every style the feed carries —
// all-caps PR wires, ticker lists, Spanish-particle US proper nouns, ADR and
// EDGAR filer names, UK regulatory boilerplate, terse two-worders, symbol
// soup. The named subtests in TestEnglishSurvivors pin the individual bugs
// this sweep caught; this corpus stays to catch the NEXT heuristic tweak that
// trades an English headline away. Every case must survive.
func TestEnglishBroadSweep(t *testing.T) {
	cases := []string{
		// PR-wire all-caps styles
		"FERRARI N.V.: PERIODIC REPORT ON THE BUYBACK PROGRAM",
		"NOTICE OF ANNUAL GENERAL MEETING",
		"CORRECTION - ACME CORP",
		"UPDATE 1-ACME CORP RAISES GUIDANCE",
		"ACME CORP ANNOUNCES PRICING OF PUBLIC OFFERING",
		"HALT: NEWS PENDING",
		// Ticker-heavy movers lists (DE, LA, EN, EL, DO etc. are real tickers)
		"DE, LA, EN among today's movers",
		"Movers: DE, LA Among Top Gainers",
		"Top Losers: EL, DO, UNA",
		"Watchlist: DE, CON, EM, OS",
		"DE: Deere Q3 Earnings Top Estimates",
		"LA: Lithium Americas Spikes 40%",
		// US place / company names built from Spanish particles
		"Del Taco Opens 50 New Los Angeles Locations",
		"Las Vegas Sands, El Paso Electric Among Movers",
		"Los Angeles Rams Stadium Deal Expands",
		"El Pollo Loco Names New CEO",
		"Con Edison Declares Quarterly Dividend",
		"Del Monte Foods Files Chapter 11",
		"La-Z-Boy Beats On Revenue",
		"Las Vegas Strip Revenue Hits Record",
		"El Paso Electric, La Quinta Lead Gainers",
		"Casa Systems Announces Nasdaq Listing Compliance",
		// Latin-American ADRs (EDGAR / PR headline shapes)
		"SEC filing: 6-K — COMPANHIA DE SANEAMENTO BASICO DO ESTADO DE SAO PAULO-SABESP (SBS)",
		"SEC filing: 20-F (financials) — Banco de Chile (BCH)",
		"SEC filing: 424B5 (prospectus (offering)) — Companhia Energetica de Minas Gerais (CIG)",
		"SEC filing: 6-K — Banco Bilbao Vizcaya Argentaria de Espana (BBVA)",
		"SEC filing: 8-K (material event) — Acme Corp (ACME)",
		"Companhia de Saneamento de Minas Gerais Prices Offering",
		"Compania de Minas Buenaventura Reports Q2 Production",
		"Grupo Aeroportuario del Sureste Traffic Up 4%",
		"Grupo Aeroportuario del Pacifico, Grupo Aeroportuario del Sureste Report Traffic",
		"Embotelladora Andina Declares Dividend",
		// Foreign person/paper names quoted in English
		"Moderna's Jose Rodriguez Says Pipeline Intact",
		"Moderna's José Rodríguez says trial on track",
		"Der Spiegel Report Lifts Le Monde Parent's Ad Outlook",
		"Deutsche Bank's die-hard bulls double down",
		"Le Pen Ruling Rattles French Bond Market",
		// UK spellings / LSE regulatory
		"REG-First Trust Global Funds PLC Net Asset Value(s)",
		"Holding(s) in Company",
		"Total Voting Rights and Capital",
		"Aviva plc: Transaction in Own Shares",
		"Publication of a Prospectus",
		"Barclays Colour-Codes Its Analyst Ratings",
		"Centrica Announces Capitalisation of Reserves",
		// Terse one/two-word headlines
		"AAPL halted",
		"Fed cuts 25bps",
		"Earnings beat",
		"Guidance withdrawn",
		"CEO resigns",
		"Dividend declared",
		"Offering priced",
		"Trading resumed",
		// Mostly numbers/symbols
		"$BTC +5%",
		"S&P 500 -1.2%, Dow -0.8%, Nasdaq -1.9%",
		"WTI $78.40 (+2.1%)",
		"10-yr yield 4.62%",
		// Hyphenated / possessive
		"Coca-Cola's Q2 Beats; Guidance Raised",
		"Estée Lauder's Margin Story Isn't Over, Analyst Says",
		"Pre-Market Movers: What's Up And What's Down",
		"After-hours: Nvidia up 4% on data-center strength",
		"Best-in-class balance sheet, says analyst",
		// Long newsroom headlines
		"Cathie Wood Snaps Up $14.3 Million of Meta Stock After Post-Earnings Selloff, Ark Sheds Caterpillar Shares",
		"Why Under Armour Shares Are Trading Higher By Around 25%; Here Are 20 Stocks Moving Premarket",
		"10 Consumer Discretionary Stocks With Whale Alerts In Today's Session",
		"Insiders Buying The Dip: 4 Stocks Insiders Are Purchasing During Market Correction",
		// Foreign-word product/brand names inside English
		"Chipotle Tests New Carne Asada Nationwide",
		"Constellation Brands' Modelo Especial Becomes Top US Beer",
		"Toyota Unveils New Land Cruiser Para Rally Edition",
		"Nestle's La Laitiere Brand Expands Into US",
		"Anheuser-Busch Launches Nueva Cerveza Line",
		// Quoted foreign titles inside English
		"Netflix's 'La Casa de Papel' Spinoff Drives Subscriber Beat",
		"Disney's 'Encanto' Merchandise Sales Top Estimates",
		// Misc particle collisions (die/est/dos/em/os/is/as)
		"Die-casting Specialist Files For IPO",
		"Est. EPS $1.02 vs $0.98 consensus",
		"DOS Attack Disclosure Sends Shares Lower",
		"EM Debt Funds See Record Inflows",
		"OS Upgrade Cycle Boosts Chip Demand",
		"Is The Rally Over? Analysts Weigh In",
		"Nvidia As A Long-Term Play",
		"Uma Thurman Joins Board of Media Startup",
		"Nao Robotics Wins Defense Contract",
		// Headlines naming two foreign things (cross-language singles)
		"Volkswagen und Porsche? No — VW Brand Chief Denies Merger Talk",
		"Nintendo, Sony Lead Japan Rally",
		"Banco Santander, Banco de Chile Post Strong Quarters",
		"LVMH's Louis Vuitton, Dior Drive Beat",
		"Sanofi, L'Oreal In Talks Over Consumer Unit",
		// Numbers-heavy earnings lines
		"Acme Q2 EPS $0.45 Beats $0.40 Estimate, Sales $1.2B Beat $1.1B Estimate",
		"Acme Sees FY26 EPS $2.10-$2.30 vs $2.15 Est",
		// Crypto / macro
		"Bitcoin Tops $120K As ETF Inflows Accelerate",
		"Dollar Slips, Gold Gains Ahead Of CPI",
		"OPEC+ Weighs Output Hike",
		// Analyst actions
		"Goldman Sachs Upgrades Acme To Buy",
		"Morgan Stanley Maintains Overweight On Nvidia, Raises Price Target To $150",
		"Con Edison Downgraded To Underweight At JPMorgan",
		// Halts / offerings / dilution wires
		"Acme Announces Proposed Public Offering Of Common Stock",
		"Acme Prices $50M Registered Direct Offering",
		"Acme Announces Reverse Stock Split",
		"Acme Regains Compliance With Nasdaq Minimum Bid Price Requirement",
		// Insider / 13G-shaped English
		"SEC filing: SC 13D (activist stake) — Acme Corp (ACME)",
		"SEC filing: 4 (insider transaction) — El Paso Electric Co (EE)",
		"SEC filing: 25-NSE (delisting notice) — Del Taco Restaurants Inc (TACO)",
		// rtpr / GlobeNewswire English shapes
		"Acme Corp. Announces Fourth Quarter and Full Year 2025 Financial Results",
		"Acme Therapeutics Doses First Patient in Phase 2 Trial",
		"Acme Inc. to Present at the 2026 Wells Fargo Healthcare Conference",
		"Vinci Partners de Brasil Announces Secondary Offering",
	}
	for _, h := range cases {
		if Foreign(h) {
			t.Errorf("ENGLISH DROPPED: %q", h)
		}
	}
}
