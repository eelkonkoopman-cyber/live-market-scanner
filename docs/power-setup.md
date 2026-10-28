# Power Setup (alerts only)

A researched, rule-based long setup on top of the existing **Power candle**. The scanner alerts and shows a plan. **It never places an order**; you execute it yourself (see "Executing it with the Order Ticket").

## The rules

1. **Power candle** (engine judge, `internal/engine/dildo.go`, `ClassifyPower`): a 1-minute candle passing every green-dildo rule at the live tuned settings except the close-near-high one, **new high of the day only**.
2. **Filter A**: the power candle's close is **less than 21% above the day's open** (open = the first traded 1m bar from 04:00 ET).
3. **EMA slope**: the 1m 9 EMA (including the power candle's close) is **at least 2.1% higher than 3 bars earlier**.

**The gap filter is OFF.** The first version also required a gap of 10%+, but the research measured the gap off the 09:30 open, which is look-ahead for a pre-market signal. Measured honestly (open = the first 04:00 bar, previous = the previous regular close) the filter makes the system **worse**, so it was dropped. The gap is still computed and shown (`psGap` column). Env `SCANNER_PS_MIN_GAP` (0 = off, the default) can switch a threshold back on; the filter tag then shows a third mark.

An unknown input (the scanner joined mid-day so the 04:00 open is not trusted, fewer than 12 bars for the EMA) fails its filter and shows `?`.

**Quiet windows.** Power Setup does **not fire** 05:00-07:00 ET (the user does not trade then and the backtest was negative there) or 16:00-20:00 ET (after hours). It fires 04:00-05:00 and 07:00-16:00 ET. A power candle whose filters pass inside a window is still stamped (`psPass = 2`) and its SIGNALS note says `skipped: 05-07 ET window` so you can see what you missed. Configure with env `SCANNER_PS_QUIET` (default `05:00-07:00,16:00-20:00`; `none` clears them).

Backtest effect of skipping 05:00-07:00 (fixed $100 risk, $3,500 start): $10,577 and max drawdown $634 over 198 trades, versus $9,543 and $967 without. (The 05:00-07:00 weakness was seen on 23 trades: a hypothesis, not a proven rule.) With both windows skipped (05:00-07:00 and 16:00-20:00 ET, honest no-gap system): 192 trades, $3,500 -> $10,555, max drawdown $641 at fixed $100 risk; $3,500 -> $24,495, max drawdown $1,604 at 3% of equity (conservative same-bar ordering, hindsight top-mover universe).

**When it fires.** Most signals happen pre-market, 04:00-09:30 ET; the best stretch was 07:00-09:30 ET.

## The plan (shown with the alert)

| | |
|---|---|
| Entry | buy **limit at the 1m 9 EMA** at the signal close, valid ~15 minutes |
| Stop | EMA x 0.94 (6% under entry) |
| Target | entry + 3R (R = entry - stop, so +18%) |
| Trail | exit on a break of the **previous completed 5-minute bar's low** |
| Breakeven | only at +7.5%; never earlier |

Voice: "SYM: power setup, entry X, stop Y". Row fields: `psPass psFromOpen psEmaSlope psGap psEntry psStop psTarget psAge` (stamped on every power candle, pass or fail). Plain power candles carry the tag in the list and the SIGNALS note, e.g. `A✓ slope✗`, so you can see which filter failed and learn. SIGNALS history: kind `P-SETUP`. The Day Log records it through the alert path; a blacklisted symbol is silenced. Scan "Power Setup" (seed `seeded.powersetup.v1`, once, never resurrected), own Signal-window switch (default ON), Follow option "Power setup".

## Sizing

Risk **1-3% of the account per trade**. Position size = risk $ / (entry - stop). With a 6% stop, 2% risk means a position worth about 33% of the account (cap at your buying power). Start at 1%.

## A+ grade (2026-10-28): 1.5x risk

**A+ = the 15 one-minute bars before the power candle span ≤ 5%**: (max high − min low) / min low over those bars (the candle itself excluded; the window crosses into the previous day like the research, `feat.py` `rng15`). Fewer than 3 prior bars = not A+. **An A+ setup is sized at 1.5x the selected risk.**

- Backtest per trade: **+0.79R (A+) vs +0.39R (the rest)**; 54 of 192 trades were A+. The difference is only of **borderline significance**: treat the 1.5x as a modest tilt, not a proven edge.
- Voice: "SYM: power setup A+ (tight base 3.8%), entry X, stop Y" (a non-A+ alert is unchanged). SIGNALS history: label `P-SETUP A+`, note starting "A+ tight base 3.8%;"; the scan list badge reads `P-SETUP A+`.
- Row fields: `psAplus` (1 = A+, 0 = not) and `psBaseRange` (%, empty with fewer than 3 prior bars). An existing Power Setup scan gets both columns once (migration `migrated.powersetup.aplus.v1`).
- Order Ticket: when the loaded symbol's latest Power Setup today is A+ **and** the Power Setup preset is active, the risk is 1.5x the selected preset, shown as a button on the risk row ("A+ ×1.5 → $150") and on the RISK tile. Click it to size at the plain risk for that setup (override); it never applies silently or under the Custom preset.

## What the backtest showed (read the caveats)

- About 220 trades over 10 months on the filtered system; the longest losing streak was **12**. Expect that. Months were flat or negative in 3 of 10 (conservative fill order).
- Without the filters the same entry/exit was a loser (about -0.05R per trade over 949 trades); filter A plus the slope turn it positive. The earlier figures that included the gap filter (about +0.37R mean, ~22% wins) are **not trustworthy**: the gap was look-ahead.
- Simulated equity with a $3,500 start and 2% risk: roughly +85% to +223% depending on fill order and the pattern-day-trader limit, with 15-18% maximum drawdown.

Caveats, honestly:
- **Hindsight universe**: trades come only from stocks later picked as top movers. A live scanner sees every stock, including those that do not run. Real results will be worse.
- **Small out-of-sample**: the filters were chosen on the earlier months; the later months (about 90 trades) are a thin check. The equity numbers above were simulated with the gap filter and are optimistic.
- **1-minute same-bar ambiguity**: when one 1m bar touches both stop and target the order is unknowable; the "conservative" runs assume the stop first. Real fills can differ.
- Costs modelled at $1 or $0.005/share per side, zero slippage. Thin pre-market stocks slip, and a limit at the EMA may simply not fill.
- The research EMA was seeded from the first close; the engine's is SMA-seeded and carries across days. They converge within ~30 bars; the slope agrees closely for any stock with 40+ bars of history.

## Executing it with the Order Ticket

1. Hear "SYM: power setup, entry X, stop Y". Open the symbol (Follow: Power setup moves a chart for you).
2. In the Order Ticket enter a **buy limit at the entry**, size from the sizing rule, attach the **stop at the stop price** and a **take-profit at the target** (OCO bracket if you like).
3. Cancel the order if it has not filled after ~15 minutes.
4. After the fill, trail by hand under each completed 5-minute low; move the stop to breakeven only once +7.5% is reached.
5. Skip the trade if you are already in a position or the spread makes the limit meaningless.
