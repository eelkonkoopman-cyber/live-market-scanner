# Episodic Pivot (EP)

An alert-only implementation of the Qullamaggie-style Episodic Pivot. It does not trade, size or place anything; nothing here is investment advice.

## What an EP is

An EP is a stock that has been neglected for months and then gaps up hard on a real catalyst (here: earnings) on heavy volume. The idea is that the news changes how the market values the company, so the move can run for days to weeks instead of fading. Most attempts fail and are stopped out at -1R; the edge, when there is one, comes from a minority of large winners.

## The rules as tested

Backtest universe: US stocks, 2024-01-02 to 2026-10-16, daily bars (Massive grouped daily bars, raw volumes), news from the Massive news endpoint. A day D is a candidate when all of:

- gap: open(D) >= 1.10 x close(D-1)
- open >= $5 and open x day volume >= $20M
- volume(D) >= 3 x the 50-day average volume (or a 252-session volume high)
- neglected: close(D-1) <= 1.10 x close(63 sessions before D-1)
- catalyst: the symbol has news published between the 16:00 ET close of D-1 and the 16:00 ET close of D. The set that held up is the EARNINGS subset: a title matching earnings, results, revenue, guidance, quarter, Q1-Q4, EPS or sales.

Entry at the open of D. Stop = open - 1 x ADR20, where ADR20 is the mean of (high/low - 1) over the 20 sessions before D. Exit: one third sold at the close of day 3 if above entry, the rest trailed and sold on the first daily close below the 10-day SMA (60-day cap). Risk $100 per trade, $2 cost per trade, position capped at $25,000.

## Backtest result

Entry at the open, stop open - 1 x ADR20, 1/3 off at day 3 and SMA10 trail (the "A2" variant, the only one without look-ahead):

| variant | trades | avg R | profit factor | win % | >=3R % |
|---|---|---|---|---|---|
| base EP (any news) | 835 | +0.20 | 1.28 | 30.1 | 9.3 |
| earnings-titled news only | 326 | +0.55 | 1.80 | 31.9 | 13.2 |
| control: same filters, no news at all | 1627 | -0.07 | 0.87 | 26.0 | 6.9 |

Out of sample (train 2024-01..2025-06, test 2025-07..2026-10), earnings only: train +0.57R (238 trades), test +0.50R (88 trades). The base EP without the earnings restriction did not hold: train +0.35R, test 0.00R (profit factor 0.97). That is why only the earnings version is implemented.

## Caveats

- Daily-bar approximation. Entry is the official open; a real entry above the opening-range high comes later and at a worse price. The scanner's break rule is therefore NOT the tested entry. The R-multiples above do not transfer one-to-one to an opening-range-high entry.
- The stop (open - 1 x ADR20) can be hit on day D itself; the test counts that as a -1R loss.
- Thin news coverage: only about a third of candidates had any news in the archive, and 638 of 4,443 candidates had an earnings-titled headline. The keyword match is crude (it also hits "reports", "raises", etc. in non-earnings contexts in the live version).
- Small out-of-sample sample: 88 trades. +0.50R on 88 trades has wide error bars, and the strategy has a 1-in-3 win rate with a median trade of -1R. Years faded: the base set was +0.43R in 2024, +0.08R in 2025, -0.06R in 2026.
- Exits ignore gaps through the stop beyond what the daily bar shows, and costs are a flat $2.
- Past results do not predict future results. This is a research prompt, not a recommendation.

## How the scanner implements it

Two seeded scans (each seeded once under its own key, never resurrected after a deletion), both with alerts on by default and a Signal-window switch each. Blacklisted symbols are silenced as for every other scan.

### EP Watch (`seeded.epwatch.v1`, id `ep-watch`)

Pre-market only (the filter requires session = pre, 04:00 to 09:30 ET). A row has `epPass = 1` when all of:

- gap >= 10% vs the previous close (pre-market: the last price; the open once it exists)
- price >= $5 (pre-market last, or the open)
- volume pace: volume so far >= 1x the average daily volume, OR the intraday relative volume (rvol, the projection) >= 3. The average daily volume is the scanner's `avgVol`, built from the days it has stored (about 20-30 trading days), so it is an "available-days average", not the tested 50-day average.
- earnings catalyst: a headline for the symbol published since the previous 16:00 ET whose title matches `earnings|results|quarter|Q[1-4]|revenue|EPS|guidance|reports|beats|misses|raises|outlook` (whole words, case-insensitive). Headlines come from the live news feed (Benzinga push, RTPR, Massive news) and from the stored articles of each candidate. Copy such as "why X shares are trading higher" does not match.
- neglected: the previous close is <= +10% over the close 63 sessions earlier ("3m chg %"). The stored daily history is too short for this, so for each candidate gapping >= 10% on >= $5 the scanner fetches the Massive daily bars once per day (about 110 calendar days, cached for the day, retried after 5 minutes on failure). Until that fetch returns, the symbol cannot pass.

Columns: last, gap %, volume x average, earnings headline, headline age, 3m chg %, ADR20, planned stop, news. Planned stop = price - 1 x ADR20 (ADR20 over the days available when fewer than 20; the "ADR days" column shows how many; below 5 days it is unknown and so is the stop). Speaks "SYM: episodic pivot watch, earnings".

### EP Break (`seeded.epbreak.v1`, id `ep-break`)

09:35 to 11:00 ET. A symbol that passed EP Watch before the open (latched for the day), or passes the same rules at the open, and trades above the opening-range high (09:30-09:35). The ORH is the regular-session high seen up to 09:35:00; if the scanner was started after 09:35 there is no ORH and no break fires for the day. The first break is latched, so it alerts once per symbol per day. Columns: last, ORH, stop, risk % (last to stop), gap %, volume x average, headline, ADR20, break age. Speaks "SYM: episodic pivot break". Position size is not shown: use the Size $ helper with the stop shown.

The rules live in `internal/ep`, the row overlay in `internal/scan/ep.go`, the daily-context worker and seeds in `cmd/scanner/epwatch.go`. `SCANNER_EP=0` stops the worker (the scans then stay empty).

## Trade plan (the tested one, for reference)

- Entry: above the opening-range high after 09:35 (the scanner's EP Break), not before.
- Stop: open - 1 x ADR20 (the "EP stop" column), or below the day's low so far if that is tighter for you. Risk a fixed small fraction of the account per trade.
- Day 3: take a partial (one third in the test) if the position is above entry.
- Remainder: trail and exit on the first daily close below the 10-day SMA.
- Expect most trades to lose 1R and a few to run; sizing must survive a long losing streak.
