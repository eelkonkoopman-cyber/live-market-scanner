# Volume Break (alerts only)

A continuation scalp on stocks in play: a consolidation high breaks on volume, then you buy the first pullback to the 1-minute 9 EMA. The scanner alerts and shows the levels. **It never places an order.** The rule is uncalibrated: treat it as a "look now" prompt.

## The rules

All on closed 1-minute bars (the engine's shared closed-bar judge, `internal/engine/volbreak.go`; the rule is `internal/volbreak`).

1. **Universe**: the stock is up **10%+ on the day** (close vs the previous close) with **1M+ shares** session volume. Any price.
2. **Range**: the longest run of bars, ending at the bar before the one judged, whose (high − low) / low stays **≤ 12%** (`MaxRangePct`). It must span **≥ 30 minutes** and its high must be **tested twice**: two separate visits (a bar out of the zone between) where a bar's high came within **0.5%** of it, the first and last at least 5 minutes apart.
3. **VOL BREAK**: a bar **closes above the range high** on **≥ 3×** the range's average 1-minute volume. Once per level: a new range must form before it can fire again. Voice: "SYM: volume break 3.43 (4.2x vol), 9EMA 3.31".
4. **VOL BREAK 9EMA**: within **30 minutes**, the first later bar whose **low touches or comes within 0.3%** of the 1m 9 EMA while it **still closes above the range high**. Voice: "SYM: vol break 9EMA entry 3.45, level 3.43" (the entry is the 9 EMA at that bar).
5. **Invalid** when a bar closes back under the range high first (`vbState 3`), or no pullback comes within 30 minutes (`vbState 4`). A new ET day expires a waiting break.
6. **Once per level**: a range must start after the latest break bar, so the same level never fires twice; a new range has to form first.

All thresholds are constants at the top of `internal/volbreak/volbreak.go`.

The voice spells "9EMA" as "nine E M A", like the other 9 EMA alerts; the SIGNALS notes write "9EMA".

**Memory.** The rule keeps the last 2 hours of bars, but only for tickers that have closed a bar 10%+ over the previous close. The ring is allocated then, so the range forms from the bars after that moment. A scanner started mid-day needs 30 minutes of bars before it can see a range.

## Where it shows

| | |
|---|---|
| Scans | "Vol Break" (`vol-break`, `vbAge <= 240`, `volbreak` tone) and "Vol Break 9EMA" (`vol-break-9ema`, `vbState = 2` and `vbEntryAge <= 240`, the rising `long` tone). Seeded once under `seeded.volbreak.v1` and never brought back after a deletion |
| Row fields | `vbAge vbLevel vbVolX vbEma vbState vbEntryAge vbEntryPx` (scan editor: Signals → Volume break) |
| Signal window | its own switches "Vol Break" and "Vol Break 9EMA" (default ON), Follow option "Vol break" (either alert), list badges **VOL BRK** / **VB 9EMA** |
| SIGNALS history | kinds `volBreak` / `volBreakEntry`, badges VOL BRK / VB 9EMA, each with a filter chip |
| Blacklist | a blacklisted symbol is muted like every other alert |
| Engine switch | env `SCANNER_VOLBREAK=0` turns the judge off (default on) |
