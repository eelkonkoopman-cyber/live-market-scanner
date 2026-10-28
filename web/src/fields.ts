// Field registry — the ONE place that knows what every scan/row field is
// called, what unit it carries, how it should be formatted, which group it
// belongs to, and what it means. The grid header, the scan editor's
// dropdowns and column chooser, the symbol panels and the activity log all
// read from here so a field never renders as a raw key like "preVolPctAdv"
// in one place and "PM % ADV" in another. Keys mirror internal/core/metric.go
// (the Field* constants) exactly; the wire never changes.
//
// Formatting itself lives in format.ts (formatField), which switches on
// `kind` — this file is data plus a few lookups, no rendering.

export type FieldKind =
  | 'price' // $ value: 2 decimals, 4 below $1
  | 'pct' // signed percent: "+3.42%", toned pos/neg
  | 'pctAbs' // unsigned percent (a magnitude that is never negative by construction): "12.3%"
  | 'int' // plain count: "12"
  | 'compact' // large count: "6.8M" (shares, volume)
  | 'ratio' // multiple: "2.58x"
  | 'score' // 0-100 integer score
  | 'dur' // seconds rendered as a duration: "4m", "1.2h"
  | 'bool' // rendered as a dot by the grid
  | 'enum' // secType codes mapped to words
  | 'trend' // dtrend 1/0/-1 rendered as an arrow badge by the grid
  | 'flagState' // 0/1/2 rendered as none / coiling / breakout
  | 'e9State' // 0-4 rendered as an idle / BREAKOUT / FLAT / READY / LONG badge by the grid (e9.ts)
  | 'hits' // per-scan entry count rendered as a badge by the grid
  | 'text'
  | 'chart'; // the inline candlestick column (no value of its own)

export type FieldGroupId =
  | 'price'
  | 'candle5m'
  | 'levels'
  | 'ema1m'
  | 'volume'
  | 'session'
  | 'premarket'
  | 'rth'
  | 'flags'
  | 'iflags'
  | 'e9flag'
  | 'dildo'
  | 'climax'
  | 'bbs'
  | 'ignition'
  | 'power'
  | 'volbreak'
  | 'emavwap'
  | 'short'
  | 'reference'
  | 'other';

export interface FieldDef {
  key: string; // wire key (core.Field* constant)
  label: string; // short column header, ≤ 12 chars
  name: string; // full human name for menus and tooltips
  unit?: string; // unit as shown in menus ("%", "$", "shares", "sec", "x")
  kind: FieldKind;
  group: FieldGroupId;
  hint?: string; // longer explanation for tooltips
  filter?: boolean; // usable in scan filters / sort dropdowns (default true)
  column?: boolean; // usable as a grid column (default true)
}

export const FIELD_GROUPS: { id: FieldGroupId; label: string }[] = [
  { id: 'price', label: 'Price & change' },
  { id: 'candle5m', label: '5-min candle' },
  { id: 'levels', label: 'Key level breaks' },
  { id: 'ema1m', label: '1-min 9 EMA' },
  { id: 'volume', label: 'Volume' },
  { id: 'session', label: 'Current session' },
  { id: 'premarket', label: 'Pre-market (frozen at 09:30)' },
  { id: 'rth', label: 'Regular session (frozen at 16:00)' },
  { id: 'flags', label: 'Bull flag — daily' },
  { id: 'iflags', label: 'Bull flag — intraday' },
  { id: 'e9flag', label: '9EMA flag (market scan)' },
  { id: 'dildo', label: 'Green dildo → 9 EMA pullback (1-min, whole market)' },
  { id: 'climax', label: 'Volume climax → bottom confirmed (1-min, whole market)' },
  { id: 'bbs', label: 'Bouncy ball short (1-min, whole market)' },
  { id: 'ignition', label: 'Ignition (1-min, whole market)' },
  { id: 'power', label: 'Power candle (1-min, whole market)' },
  { id: 'volbreak', label: 'Volume break → 9 EMA entry (1-min, stocks in play)' },
  { id: 'emavwap', label: '9 EMA / VWAP cross (1-min)' },
  { id: 'short', label: 'Short availability (IBKR)' },
  { id: 'reference', label: 'Reference & trend' },
  { id: 'other', label: 'Other' },
];

const BACKFILL_PRE =
  "frozen when pre-market ends at 09:30 — empty before then; if the scanner wasn't running at 09:30, the top ~300 movers are backfilled from Massive a few minutes after startup";
const BACKFILL_RTH =
  "frozen at the 16:00 close — empty before then; if the scanner wasn't running at 16:00, the top ~300 movers are backfilled from Massive a few minutes after an evening startup";

export const FIELD_DEFS: FieldDef[] = [
  // --- price & change ---
  { key: 'last', label: 'Last', name: 'Last price', unit: '$', kind: 'price', group: 'price' },
  { key: 'chg1m', label: '1m %', name: '1-minute change', unit: '%', kind: 'pct', group: 'price', hint: 'percent change over the last 60 seconds' },
  {
    key: 'c1mChg',
    label: '1m candle %',
    name: '1-minute candle change',
    unit: '%',
    kind: 'pct',
    group: 'price',
    hint: "live 1-minute candle: price vs the previous candle's close. The candle that just closed stays shown through the next minute while it is the stronger one",
  },
  { key: 'chg5m', label: '5m %', name: '5-minute change', unit: '%', kind: 'pct', group: 'price', hint: 'percent change over the last 5 minutes — the signature scan metric' },
  { key: 'chg15m', label: '15m %', name: '15-minute change', unit: '%', kind: 'pct', group: 'price', hint: 'percent change over the last 15 minutes' },
  { key: 'chgDay', label: 'Day %', name: 'Day change', unit: '%', kind: 'pct', group: 'price', hint: 'percent vs the previous official close — live all day, so it is the "gap %" while pre-market is still running' },
  { key: 'gap', label: 'Gap %', name: 'Open gap', unit: '%', kind: 'pct', group: 'price', hint: 'day open vs the previous close, percent' },
  { key: 'vwap', label: 'VWAP', name: 'VWAP', unit: '$', kind: 'price', group: 'price', hint: 'volume-weighted average price since 04:00 ET' },
  { key: 'vsVwap', label: 'vs VWAP', name: 'Distance from VWAP', unit: '%', kind: 'pct', group: 'price', hint: 'percent of last price above (+) or below (−) VWAP' },
  { key: 'hod', label: 'HOD', name: 'High of day', unit: '$', kind: 'price', group: 'price' },
  { key: 'lod', label: 'LOD', name: 'Low of day', unit: '$', kind: 'price', group: 'price' },
  {
    key: 'pmVolX',
    label: 'PM vol x',
    name: 'Pre-market volume vs 10-day avg',
    unit: 'x',
    kind: 'ratio',
    group: 'premarket',
    hint: "pre-market volume so far as a multiple of the stock's average FULL-DAY volume over the last 10 trading days (Massive daily bars, extended hours included). 3 = it has already traded three normal days' worth before the open. Live during pre-market; after 09:30 it shows the final pre-market figure when the scanner saw the whole pre-market",
  },
  {
    key: 'adv10',
    label: 'Avg vol 10d',
    name: '10-day average daily volume',
    unit: 'shares',
    kind: 'compact',
    group: 'volume',
    hint: 'average full-day volume (extended hours included) over the last 10 trading days, today excluded',
  },
  { key: 'vsHod', label: 'vs HOD', name: 'Below high of day', unit: '%', kind: 'pct', group: 'price', hint: 'percent below the high of day' },
  {
    key: 'brk5mAge',
    label: '5m low brk',
    name: 'Broke previous 5-min low',
    unit: 'sec',
    kind: 'dur',
    group: 'candle5m',
    hint: "seconds since the current 5-minute candle traded below the previous candle's low — empty when it hasn't. Filter < 60 for breaks in the last minute; any value means it has broken",
  },
  {
    key: 'vsPrev5mLow',
    label: 'vs 5m low',
    name: 'vs previous 5-min low',
    unit: '%',
    kind: 'pct',
    group: 'candle5m',
    hint: "last price vs the previous 5-minute candle's low; negative = trading below it now (a break that has since bounced back above reads positive)",
  },
  {
    key: 'prev5mLow',
    label: 'Prev 5m low',
    name: 'Previous 5-min low',
    unit: '$',
    kind: 'price',
    group: 'candle5m',
    hint: "low of the previous completed 5-minute candle (clock-aligned: 09:30, 09:35, …; extended hours included)",
  },
  {
    key: 'brk5mCloseAge',
    label: '5m close brk',
    name: 'Broke previous 5-min close',
    unit: 'sec',
    kind: 'dur',
    group: 'candle5m',
    hint: "seconds since the current 5-minute candle traded below the previous candle's CLOSE — empty when it hasn't. Fires far more often than the low version: a new candle usually opens at the previous close, so one downtick counts. Filter < 60 for breaks in the last minute",
  },
  {
    key: 'brkPmhAge',
    label: 'PMH brk',
    name: 'Broke pre-market high',
    unit: 'sec',
    kind: 'dur',
    group: 'levels',
    hint: "seconds since price last broke above today's pre-market high (04:00-09:30). Live from 09:30; empty if it hasn't, or if the scanner didn't see the whole pre-market (a too-low high would fake breakouts). A close back below re-arms it, so a reclaim counts again. Filter < 60 for fresh breaks",
  },
  {
    key: 'brkPmh5mAge',
    label: 'PMH 5m brk',
    name: 'Broke pre-market high (5-min close)',
    unit: 'sec',
    kind: 'dur',
    group: 'levels',
    hint: "5-minute timeframe: seconds since a 5-minute candle CLOSED above today's pre-market high (a wick that closes back under doesn't count). A 5-minute close back at or below re-arms it. Filter < 300 to keep a break listed for one candle",
  },
  {
    key: 'vsPmh',
    label: 'vs PMH',
    name: 'vs pre-market high',
    unit: '%',
    kind: 'pct',
    group: 'levels',
    hint: "last price vs today's pre-market high; positive = above it",
  },
  {
    key: 'brkPrevAhAge',
    label: 'Prev AH brk',
    name: 'Broke previous after-hours high',
    unit: 'sec',
    kind: 'dur',
    group: 'levels',
    hint: "seconds since price last broke above the previous trading day's after-hours high (16:00-20:00 ET). Levels come from Massive minute bars for the ~500 most active names, topped up during the day. A close back below re-arms it. Filter < 60 for fresh breaks",
  },
  {
    key: 'brkPrevAh5mAge',
    label: 'AH 5m brk',
    name: 'Broke previous after-hours high (5-min close)',
    unit: 'sec',
    kind: 'dur',
    group: 'levels',
    hint: "5-minute timeframe: seconds since a 5-minute candle CLOSED above the previous trading day's after-hours high. A 5-minute close back at or below re-arms it. Filter < 300 to keep a break listed for one candle",
  },
  {
    key: 'vsPrevAh',
    label: 'vs prev AH',
    name: 'vs previous after-hours high',
    unit: '%',
    kind: 'pct',
    group: 'levels',
    hint: "last price vs the previous trading day's after-hours high; positive = above it",
  },
  {
    key: 'prevAhHigh',
    label: 'Prev AH hi',
    name: 'Previous after-hours high',
    unit: '$',
    kind: 'price',
    group: 'levels',
    hint: "the previous trading day's 16:00-20:00 ET high",
  },
  {
    key: 'dbLevel',
    label: 'Level $',
    name: 'Daily level broken',
    unit: '$',
    kind: 'price',
    group: 'levels',
    hint: 'the highest daily level price crossed today and still above: the 14-day high, a swing-high pivot of the last ~60 days, or the previous day’s high. From daily bars; needs a price over $30',
  },
  {
    key: 'dbType',
    label: 'Level',
    name: 'Daily level type',
    unit: '',
    kind: 'text',
    group: 'levels',
    hint: 'which daily level was broken: 14D HIGH, PIVOT or PDH. Sorting puts 14D HIGH first',
  },
  {
    key: 'dbPct',
    label: 'Above lvl',
    name: '% above the daily level',
    unit: '%',
    kind: 'pct',
    group: 'levels',
    hint: 'last price vs the broken daily level, percent; small = a fresh break',
  },
  {
    key: 'dbAge',
    label: 'Brk age',
    name: 'Time since the daily level break',
    unit: 'sec',
    kind: 'dur',
    group: 'levels',
    hint: 'seconds since price crossed the daily level (pre-market included)',
  },
  {
    key: 'pdlLevel',
    label: 'PDL $',
    name: 'Previous day low',
    unit: '$',
    kind: 'price',
    group: 'levels',
    hint: 'the previous trading day’s low, shown while price has broken below it today. Needs a price over $25',
  },
  {
    key: 'pdlPct',
    label: 'Below PDL',
    name: '% below the previous day low',
    unit: '%',
    kind: 'pct',
    group: 'levels',
    hint: 'how far last price is below the previous day’s low, percent (positive); small = a fresh break',
  },
  {
    key: 'pdlAge',
    label: 'PDL age',
    name: 'Time since the PDL break',
    unit: 'sec',
    kind: 'dur',
    group: 'levels',
    hint: 'seconds since price crossed below the previous day’s low (pre-market included); empty after a scanner restart',
  },
  {
    key: 'epPass',
    label: 'EP pass',
    name: 'Episodic pivot rules pass',
    unit: '',
    kind: 'bool',
    group: 'levels',
    hint: '1 when every EP Watch rule passes: gap 10%+, $5+, volume pace, an earnings headline since the previous 16:00 ET and a 3-month change of +10% or less (docs/episodic-pivot.md)',
  },
  {
    key: 'epGap',
    label: 'EP gap',
    name: 'Episodic pivot gap',
    unit: '%',
    kind: 'pct',
    group: 'levels',
    hint: 'gap vs the previous close: the last price in pre-market, the open after 09:30',
  },
  {
    key: 'epVolX',
    label: 'Vol x avg',
    name: 'Volume vs average daily volume',
    unit: 'x',
    kind: 'ratio',
    group: 'levels',
    hint: 'volume so far as a multiple of the average daily volume (the days the scanner has stored, up to 20)',
  },
  {
    key: 'epEarnHL',
    label: 'Earnings HL',
    name: 'Earnings headline',
    unit: '',
    kind: 'text',
    group: 'levels',
    hint: 'the newest headline published since the previous 16:00 ET that mentions earnings, results, a quarter, revenue, EPS, guidance, reports, beats, misses, raises or outlook',
  },
  {
    key: 'epEarnAge',
    label: 'Earn age',
    name: 'Age of the earnings headline',
    unit: 'sec',
    kind: 'dur',
    group: 'levels',
    hint: 'seconds since that earnings headline was published',
  },
  {
    key: 'ep3m',
    label: '3m chg',
    name: '3-month change',
    unit: '%',
    kind: 'pct',
    group: 'levels',
    hint: 'the previous close vs the close about 63 sessions earlier; EP wants a neglected stock (+10% or less)',
  },
  {
    key: 'epAdr',
    label: 'ADR20',
    name: 'Average daily range (20 days)',
    unit: '%',
    kind: 'pctAbs',
    group: 'levels',
    hint: 'mean of (high / low - 1) over the last 20 sessions; fewer days are used when fewer exist (see the days column)',
  },
  {
    key: 'epAdrDays',
    label: 'ADR days',
    name: 'Days behind the ADR',
    unit: '',
    kind: 'int',
    group: 'levels',
    hint: 'how many daily bars the ADR used; under 20 means a short history',
  },
  {
    key: 'epStop',
    label: 'EP stop',
    name: 'Planned stop',
    unit: '$',
    kind: 'price',
    group: 'levels',
    hint: 'open (or pre-market last) minus 1 x ADR20. A plan, not an order',
  },
  {
    key: 'epOrh',
    label: 'ORH',
    name: 'Opening-range high',
    unit: '$',
    kind: 'price',
    group: 'levels',
    hint: 'the 09:30-09:35 high, taken from the regular-session high seen up to 09:35:00. Empty if the scanner started after 09:35',
  },
  {
    key: 'epBrkAge',
    label: 'ORH brk age',
    name: 'Time since the ORH break',
    unit: 'sec',
    kind: 'dur',
    group: 'levels',
    hint: 'seconds since price first traded above the opening-range high after 09:35 (once per symbol per day)',
  },
  {
    key: 'epRiskPct',
    label: 'Risk %',
    name: 'Distance to the stop',
    unit: '%',
    kind: 'pctAbs',
    group: 'levels',
    hint: 'how far the last price is above the planned stop, % of the last price',
  },
  {
    key: 'brkEma9Age',
    label: '9EMA lost',
    name: 'Closed under the 1-min 9 EMA',
    unit: 'sec',
    kind: 'dur',
    group: 'ema1m',
    hint: "seconds since a 1-minute candle CLOSED below the 1-minute 9 EMA right after one closed at or above it (the same EMA the charts window draws). Empty until the scanner has seen 30 one-minute candles for the stock, so its EMA agrees with the chart's. Active stocks do this often (~every 9 minutes), so combine with other filters. Filter < 60 for fresh losses",
  },
  {
    key: 'vsEma9',
    label: 'vs 9EMA',
    name: 'vs 1-min 9 EMA',
    unit: '%',
    kind: 'pct',
    group: 'ema1m',
    hint: 'last price vs the 1-minute 9 EMA of completed candles; negative = below it',
  },
  {
    key: 'ema9',
    label: '9 EMA',
    name: '1-min 9 EMA',
    unit: '$',
    kind: 'price',
    group: 'ema1m',
    hint: 'the 1-minute 9 EMA after the latest completed candle',
  },
  {
    key: 'brk5mCloseUpAge',
    label: '5m close up',
    name: 'Broke above previous 5-min close',
    unit: 'sec',
    kind: 'dur',
    group: 'candle5m',
    hint: "long side: seconds since the current 5-minute candle traded ABOVE the previous candle's close — empty when it hasn't. Fires often (a new candle opens at the previous close; one uptick counts). Filter < 60 for breaks in the last minute",
  },
  {
    key: 'vsPrev5mClose',
    label: 'vs 5m close',
    name: 'vs previous 5-min close',
    unit: '%',
    kind: 'pct',
    group: 'candle5m',
    hint: "last price vs the previous 5-minute candle's close; negative = below it, positive = above it. Require a real move rather than a one-tick poke: e.g. < -0.5 for shorts, > 0.5 for longs",
  },
  {
    key: 'prev5mClose',
    label: 'Prev 5m cls',
    name: 'Previous 5-min close',
    unit: '$',
    kind: 'price',
    group: 'candle5m',
    hint: 'close (last print) of the previous completed 5-minute candle',
  },

  // --- volume ---
  { key: 'vol', label: 'Vol', name: 'Volume', unit: 'shares', kind: 'compact', group: 'volume', hint: 'cumulative volume since 04:00 ET' },
  { key: 'rvol', label: 'RVOL', name: 'Relative volume', unit: 'x', kind: 'ratio', group: 'volume', hint: "today's cumulative volume divided by the 20-session average at this time of day" },
  {
    key: 'c1mRvol',
    label: '1m RVOL',
    name: '1-minute candle relative volume',
    unit: 'x',
    kind: 'ratio',
    group: 'volume',
    hint: "this 1-minute candle's volume ÷ the average of the 9 minutes before it (empty minutes count as 0). Empty for the first 9 minutes after the scanner starts watching a stock",
  },
  { key: 'avgVol', label: 'Avg vol', name: 'Average daily volume', unit: 'shares/day', kind: 'compact', group: 'volume', hint: 'trailing 20-session mean of REGULAR-SESSION (09:30-16:00) volume only — the conventional ADV, excluding pre/post-market. Empty until ~5+ sessions of history exist for this ticker.' },
  { key: 'rot', label: 'Float rot', name: 'Float rotation', unit: 'x', kind: 'ratio', group: 'volume', hint: 'volume divided by float' },

  // --- current session ---
  { key: 'chgSess', label: 'Sess %', name: 'Session change', unit: '%', kind: 'pct', group: 'session', hint: "percent vs the current session's open price; resets at 04:00, 09:30 and 16:00 ET" },
  { key: 'sessVol', label: 'Sess vol', name: 'Session volume', unit: 'shares', kind: 'compact', group: 'session', hint: 'volume within the current session only' },
  { key: 'sessVwap', label: 'Sess VWAP', name: 'Session VWAP', unit: '$', kind: 'price', group: 'session' },
  { key: 'sessHigh', label: 'Sess high', name: 'Session high', unit: '$', kind: 'price', group: 'session' },
  { key: 'sessLow', label: 'Sess low', name: 'Session low', unit: '$', kind: 'price', group: 'session' },

  // --- pre-market (frozen) ---
  { key: 'preChg', label: 'PM %', name: 'Pre-market change', unit: '% vs prev close', kind: 'pct', group: 'premarket', hint: `pre-market % move vs previous close, ${BACKFILL_PRE}` },
  { key: 'preGap', label: 'PM gap %', name: 'Pre-market peak gap', unit: '% peak vs prev close', kind: 'pct', group: 'premarket', hint: 'peak pre-market gain: the session HIGH vs the previous close, frozen at 09:30 — catches overnight gaps AND mid-premarket news spikes (unlike PM %, which is where it ENDED); empty before 09:30' },
  { key: 'preFade', label: 'PM fade %', name: 'Pre-market fade', unit: '% off high', kind: 'pctAbs', group: 'premarket', hint: 'how much of the pre-market move was given back: % from the pre-market HIGH down to the 09:30 price, never negative. Measured off the session HIGH, so a single spike print can overstate the tradeable fade; empty before 09:30' },
  { key: 'preHigh', label: 'PM high', name: 'Pre-market high', unit: '$', kind: 'price', group: 'premarket', hint: `pre-market session high, ${BACKFILL_PRE}` },
  { key: 'preLow', label: 'PM low', name: 'Pre-market low', unit: '$', kind: 'price', group: 'premarket', hint: `pre-market session low, ${BACKFILL_PRE}` },
  { key: 'preVol', label: 'PM vol', name: 'Pre-market volume', unit: 'shares', kind: 'compact', group: 'premarket', hint: `shares traded in the pre-market, ${BACKFILL_PRE}` },
  { key: 'preVolPctAdv', label: 'PM % ADV', name: 'Pre-market volume as % of ADV', unit: '% of avg vol', kind: 'pctAbs', group: 'premarket', hint: 'pre-market volume so far as a % of average daily volume — LIVE throughout pre-market (holds its value once 09:30 passes). 10%+ of a typical day already traded before the open is a strong unusual-interest signal. Empty until avg vol has enough history.' },
  { key: 'preVwap', label: 'PM VWAP', name: 'Pre-market VWAP', unit: '$', kind: 'price', group: 'premarket', hint: `pre-market VWAP, ${BACKFILL_PRE}` },

  // --- regular session (frozen) ---
  { key: 'rthChg', label: 'RTH %', name: 'Regular-session change', unit: '% vs prev close', kind: 'pct', group: 'rth', hint: `regular-session % move vs previous close, ${BACKFILL_RTH}` },
  { key: 'rthGap', label: 'RTH gap %', name: 'Regular-session peak gap', unit: '% peak vs prev close', kind: 'pct', group: 'rth', hint: "peak regular-session gain: the session HIGH vs the previous close, frozen at the 16:00 close — the day's best mark, not where it ended (that's RTH %); empty before 16:00" },
  { key: 'rthFade', label: 'RTH fade %', name: 'Regular-session fade', unit: '% off high', kind: 'pctAbs', group: 'rth', hint: "how much of the day's move was given back: % from the regular-session HIGH down to the 16:00 close, never negative; empty before 16:00" },
  { key: 'rthHigh', label: 'RTH high', name: 'Regular-session high', unit: '$', kind: 'price', group: 'rth', hint: `regular-session high, ${BACKFILL_RTH}` },
  { key: 'rthLow', label: 'RTH low', name: 'Regular-session low', unit: '$', kind: 'price', group: 'rth', hint: `regular-session low, ${BACKFILL_RTH}` },
  { key: 'rthVol', label: 'RTH vol', name: 'Regular-session volume', unit: 'shares', kind: 'compact', group: 'rth', hint: `shares traded in the regular session, ${BACKFILL_RTH}` },
  { key: 'rthVwap', label: 'RTH VWAP', name: 'Regular-session VWAP', unit: '$', kind: 'price', group: 'rth', hint: `regular-session VWAP, ${BACKFILL_RTH}` },

  // --- bull flag, daily ---
  { key: 'flagScore', label: 'Flag score', name: 'Bull-flag score', unit: '0-100', kind: 'score', group: 'flags', hint: 'bull-flag setup quality, 0-100: a big advance that then paused in a tight, shallow, quiet base scores highest — "Flag score > 70" keeps only the cleanest setups' },
  { key: 'flagState', label: 'Flag', name: 'Bull-flag state', unit: '0/1/2', kind: 'flagState', group: 'flags', hint: '0 = none, 1 = coiling (tight range holding after a move up), 2 = breaking out above that range — 2 is stamped live the moment price clears the base high' },
  { key: 'flagPole', label: 'Pole %', name: 'Pole size', unit: '%', kind: 'pctAbs', group: 'flags', hint: 'size of the advance the base is resting on, % from the swing low to the high (the "pole")' },
  { key: 'flagRange', label: 'Range %', name: 'Base range', unit: '%', kind: 'pctAbs', group: 'flags', hint: 'tightness of the base: its high-to-low range as a % of its middle — smaller = tighter coil' },
  { key: 'flagRetrace', label: 'Retrace %', name: 'Pole retrace', unit: '% of pole', kind: 'pctAbs', group: 'flags', hint: '% of the pole given back from the high — LOWER is better: a shallow pullback held the gain, a deep one means the move failed' },
  { key: 'flagVolDry', label: 'Vol dry', name: 'Base vs pole volume', unit: 'x pole', kind: 'ratio', group: 'flags', hint: 'base volume vs pole volume — BELOW 1 means volume dried up while it consolidated (sellers exhausted), the constructive case; above 1 = still being distributed' },
  { key: 'flagBars', label: 'Base bars', name: 'Base length', unit: 'daily bars', kind: 'int', group: 'flags', hint: 'how many daily bars the base has lasted so far (a few days to a few weeks is typical)' },

  // --- bull flag, intraday ---
  { key: 'iflagScore', label: 'iFlag score', name: 'Intraday flag score', unit: '0-100', kind: 'score', group: 'iflags', hint: "intraday bull-flag quality, 0-100, from TODAY's 5-min bars (recomputed ~every minute): a push up this session that then coiled tight and quiet scores highest" },
  { key: 'iflagState', label: 'iFlag', name: 'Intraday flag state', unit: '0/1/2', kind: 'flagState', group: 'iflags', hint: "recomputed ~every minute from today's 5-min bars: 0 = none, 1 = coiling now, 2 = breaking out above the intraday range — 2 is stamped live the moment price clears the base high" },
  { key: 'iflagPole', label: 'iPole %', name: 'Intraday pole size', unit: '%', kind: 'pctAbs', group: 'iflags', hint: "size of today's push the coil is resting on, % from the session swing low to the high" },
  { key: 'iflagRange', label: 'iRange %', name: 'Intraday base range', unit: '%', kind: 'pctAbs', group: 'iflags', hint: "tightness of today's coil: its high-to-low range as a % of its middle — smaller = tighter" },
  { key: 'iflagRetrace', label: 'iRetrace %', name: 'Intraday pole retrace', unit: '% of pole', kind: 'pctAbs', group: 'iflags', hint: "% of today's pole given back from its high — LOWER is better" },
  { key: 'iflagVolDry', label: 'iVol dry', name: 'Intraday base vs pole volume', unit: 'x pole', kind: 'ratio', group: 'iflags', hint: "coil volume vs pole volume within today's session — BELOW 1 means volume dried up while it coiled, the constructive case" },
  { key: 'iflagBars', label: 'iBase bars', name: 'Intraday base length', unit: '5m bars', kind: 'int', group: 'iflags', hint: "how long today's coil has lasted, in 5-minute bars (4-18 bars ≈ the 20-90 min sweet spot)" },

  // --- 9EMA flag, market scan ---
  // The Signal window's 9 EMA flag engine (signals/emaFlag.ts, same rules and
  // defaults) run server-side on 1-minute bars for every stock whose pre-market
  // volume is >= 1x its 10-day average (the pmVolX field, up to 80 of them). All
  // eight are empty for a stock outside that candidate set.
  {
    key: 'e9State',
    label: '9EMA flag',
    name: '9EMA flag stage (market scan)',
    unit: '0-4',
    kind: 'e9State',
    group: 'e9flag',
    hint: "where the stock is in the Signal window's 9EMA flag setup, on 1-minute bars, re-checked every second for every stock with pre-market volume of at least 1x its 10-day average: 0 = idle, 1 = BREAKOUT (ran far above the rising 9 EMA — wait for it to go sideways), 2 = FLAT (sideways above the EMA), 3 = READY (the EMA has caught up, within 4% — long above the trigger), 4 = LONG (the flag high broke while ready). Empty for stocks outside the candidate set. \"9EMA flag >= 2\" keeps stocks that are flagging or past it",
  },
  {
    key: 'e9Dist',
    label: 'EMA dist %',
    name: 'Distance above the 9 EMA',
    unit: '%',
    kind: 'pct',
    group: 'e9flag',
    hint: "latest 1-minute close vs the 9 EMA, percent (positive = above it). The 9EMA flag turns READY once this is within 4%, so sort ascending to see which flags the EMA is closest to catching",
  },
  {
    key: 'e9Trigger',
    label: 'Flag trigger',
    name: '9EMA flag trigger price',
    unit: '$',
    kind: 'price',
    group: 'e9flag',
    hint: "the long entry: the flag high plus a 0.3% buffer — a break above it while READY is the LONG signal. Empty while idle or only BREAKOUT (no flag yet)",
  },
  {
    key: 'e9Stop',
    label: 'Flag stop',
    name: '9EMA flag stop price',
    unit: '$',
    kind: 'price',
    group: 'e9flag',
    hint: "the stop a break of the flag would get — the higher of the flag low and the 9 EMA, less 1% — and, once LONG, the open trade's stop, which trails the 9 EMA up (never down); a 1-minute close under it exits. Empty while idle or only BREAKOUT",
  },
  {
    key: 'e9FlagBars',
    label: 'Flag bars',
    name: '9EMA flag length',
    unit: 'bars',
    kind: 'int',
    group: 'e9flag',
    hint: 'how many 1-minute bars the current flag has been holding sideways (it needs at least 3 to open)',
  },
  {
    key: 'e9Age',
    label: 'Stage age',
    name: 'Time in this 9EMA flag stage',
    unit: 'sec',
    kind: 'dur',
    group: 'e9flag',
    hint: 'seconds since the stock reached its current 9EMA flag stage. "Stage age <= 300" on a LONG stage keeps only fresh breakouts. Empty while idle',
  },
  // The 9 EMA SLAM (emaFlag.ts, same candidates): one closed 1-minute candle that
  // falls from above the 9 EMA to well under it out of a sideways flag (or a long).
  // Both are empty for a non-candidate and for a candidate with no slam today.
  {
    key: 'e9SlamPct',
    label: 'Slam %',
    name: '9 EMA slam depth',
    unit: '%',
    kind: 'pct',
    group: 'e9flag',
    hint: "how far under its 9 EMA today's latest 9 EMA SLAM candle closed, percent (negative: it closed at least 3% under). A SLAM is one 1-minute candle out of a sideways flag (or a long) that opens at the EMA (no more than 0.5% under the previous bar's EMA, after a close at or above it) and closes well under it: a big body (at least 1 ATR and 2%), closing near its low, on 1.3x the recent volume. An alert, not a trade. Empty without a slam today",
  },
  {
    key: 'e9SlamAge',
    label: 'Slam age',
    name: 'Time since the 9 EMA slam',
    unit: 'sec',
    kind: 'dur',
    group: 'e9flag',
    hint: 'seconds since the stock\'s latest 9 EMA SLAM today (one 1-minute candle from above the 9 EMA to well under it, out of a sideways flag or a long). "Slam age <= 300" keeps slams from the last 5 minutes — the seeded "9EMA Slam" scan. Empty without a slam today',
  },
  {
    key: 'e9HodFlagAge',
    label: 'HOD flag age',
    name: 'Time since the HOD flag formed',
    unit: 'sec',
    kind: 'dur',
    group: 'e9flag',
    hint: "seconds since the stock's latest 9 EMA flag began forming (FLAT: sideways above the 9 EMA) after a run-up that made a NEW HIGH OF THE DAY, pre-market included. HOD flag age <= 300 keeps flags from the last 5 minutes — the seeded HOD Flag scan. Empty without one today",
  },

  // --- green dildo -> 9 EMA pullback (2026-10-07) ---
  // Two sequential alerts on 1-minute candles, market-wide (signals/dildoPb.ts,
  // Go internal/dildopb). The engine judges every closed minute of every stock
  // for the dildo (the first four); the 9EMA flag scanner then follows the
  // stock on chart-identical bars for the pullback (the last four). All eight
  // are empty without a dildo today. Alerts only — "look now" prompts.
  {
    key: 'dildoAge',
    label: 'Dildo age',
    name: 'Time since the green dildo',
    unit: 'sec',
    kind: 'dur',
    group: 'dildo',
    hint: 'seconds since today\'s latest GREEN DILDO candle started: one 1-minute candle up at least 8% open to close, at least 4x the average body and 5x the average volume of the up to 10 (at least 3) traded 1-minute bars before it, closing in the top 20% of its range (close at least $0.20, at least $100k traded in the candle). Every stock is judged the moment each candle closes, so the first value seen is about 63s. "Dildo age <= 240" keeps dildos of the last 4 minutes — the seeded "Green Dildo" scan. An alert, a "look now" prompt, not a system. Empty without a dildo today',
  },
  {
    key: 'dildoBody',
    label: 'Dildo body',
    name: 'Green dildo body',
    unit: '%',
    kind: 'pct',
    group: 'dildo',
    hint: "the green dildo candle's body, open to close, as a percent of its open (at least 8%). Empty without a dildo today",
  },
  {
    key: 'dildoVolX',
    label: 'Dildo vol x',
    name: 'Green dildo volume vs the bars before it',
    unit: 'x',
    kind: 'ratio',
    group: 'dildo',
    hint: "the dildo candle's volume as a multiple of the average volume of the up to 10 traded 1-minute bars before it (at least 5x). Empty when those bars traded nothing, or without a dildo today",
  },
  {
    key: 'dildoDvol',
    label: 'Dildo $vol',
    name: 'Green dildo dollar volume',
    unit: '$',
    kind: 'compact',
    group: 'dildo',
    hint: 'dollars traded in the dildo candle (close x volume). The scanner only flags candles of at least $100k; raise the bar in a scan filter ("Dildo $vol >= 250000") — a number under 100k changes nothing. Empty without a dildo today',
  },
  {
    key: 'dildoPbAge',
    label: 'PB age',
    name: 'Time since the dildo 9 EMA pullback',
    unit: 'sec',
    kind: 'dur',
    group: 'dildo',
    hint: 'seconds since the 9 EMA PULLBACK after a green dildo started: within 20 minutes of the dildo, a 1-minute candle dipped to the 9 EMA (within 1% above it), held the dildo body\'s midpoint and closed no more than 1% under the EMA — the scalp entry prompt, at that candle\'s close. Followed on the charts\' own bars by the 9EMA flag scanner. "PB age <= 240" with "PB state = 3" is the seeded "Dildo 9EMA Pullback" scan. A "look now" prompt, not a system: no edge after costs in calibration. Empty without a pullback',
  },
  {
    key: 'dildoPbPx',
    label: 'PB close',
    name: 'Dildo 9 EMA pullback close',
    unit: '$',
    kind: 'price',
    group: 'dildo',
    hint: "the pullback candle's close — the entry price the calibration measured. Empty without a pullback",
  },
  {
    key: 'dildoTouchAge',
    label: 'Touch age',
    name: 'Time since the dildo 9 EMA touch',
    unit: 'sec',
    kind: 'dur',
    group: 'dildo',
    hint: 'seconds since the 1-minute candle touching the 9 EMA after a green dildo started — the candle is still forming (PB state 2); if it closes holding the EMA it becomes the pullback. The seeded "Dildo 9EMA Touch" scan (its alert is off until you switch it on) is the early, intrabar cue. Empty without a touch',
  },
  {
    key: 'dildoPbState',
    label: 'PB state',
    name: 'Dildo 9 EMA pullback state',
    unit: '0-5',
    kind: 'int',
    group: 'dildo',
    hint: "where the latest green dildo's pullback stands: 0 = none, 1 = watching (waiting up to 20 minutes for the dip to the 9 EMA), 2 = touching (the forming candle is at the EMA now), 3 = confirmed (a candle closed holding the EMA: the pullback alert), 4 = cancelled (it broke the dildo body's midpoint, or closed more than 1% under the EMA), 5 = expired (no pullback within 20 minutes). Empty for a stock the 9EMA flag scanner is not following",
  },

  // --- volume climax -> bottom confirmed (2026-10-07) ---
  // Two sequential alerts on 1-minute candles, market-wide and engine-only
  // (signals/climax.ts, Go internal/climax): the climax candle, then a close
  // above its midpoint before any candle undercuts its low. All nine are empty
  // without a climax today. Alerts only — "look now" prompts.
  {
    key: 'climaxAge',
    label: 'Climax age',
    name: 'Time since the volume climax',
    unit: 'sec',
    kind: 'dur',
    group: 'climax',
    hint: "seconds since today's latest VOLUME CLIMAX candle started: a 1-minute candle making a fresh low at least 6% under the high of today's 10 traded bars before it, on at least 8x their average volume and at least as much as any one of them, with a lower wick of at least 25% of its range, closing under the 1-minute 9 EMA (close at least $0.50, at least $50k traded in the candle; candles starting 09:30-09:34 never count). Every stock is judged the moment each candle closes, so the first value seen is about 63s. \"Climax age <= 240\" is the seeded \"Volume Climax\" scan. A \"look now\" prompt, not a system: no edge after costs in calibration. Empty without a climax today",
  },
  {
    key: 'climaxDropPct',
    label: 'Climax drop',
    name: 'Volume climax drop',
    unit: '% off the 10-bar high',
    kind: 'pctAbs',
    group: 'climax',
    hint: "how far the climax candle's low is under the highest high of the 10 traded bars before it, percent (at least 6%). Empty without a climax today",
  },
  {
    key: 'climaxVolX',
    label: 'Climax vol x',
    name: 'Volume climax volume vs the bars before it',
    unit: 'x',
    kind: 'ratio',
    group: 'climax',
    hint: "the climax candle's volume as a multiple of the average volume of the 10 traded bars before it (at least 8x, and at least as much as any one of them). Empty when those bars traded nothing, or without a climax today",
  },
  {
    key: 'climaxWickPct',
    label: 'Climax wick',
    name: 'Volume climax lower wick',
    unit: '% of range',
    kind: 'pctAbs',
    group: 'climax',
    hint: "the climax candle's lower wick — from the lower of its open and close down to its low — as a percent of its high-low range (at least 25%): the selling was bought. Empty without a climax today",
  },
  {
    key: 'climaxDvol',
    label: 'Climax $vol',
    name: 'Volume climax dollar volume',
    unit: '$',
    kind: 'compact',
    group: 'climax',
    hint: 'dollars traded in the climax candle (close x volume). The scanner only flags candles of at least $50k; raise the bar in a scan filter ("Climax $vol >= 250000") — a number under 50k changes nothing. Empty without a climax today',
  },
  {
    key: 'climaxLow',
    label: 'Climax low',
    name: 'Volume climax low',
    unit: '$',
    kind: 'price',
    group: 'climax',
    hint: "the climax candle's low — the level that must hold: a later 1-minute candle trading under it fails the climax. Empty without a climax today",
  },
  {
    key: 'climaxState',
    label: 'Climax state',
    name: 'Volume climax state',
    unit: '1-4',
    kind: 'int',
    group: 'climax',
    hint: "where today's latest volume climax stands: 1 = watching (the next 5 traded 1-minute candles decide), 2 = confirmed (one closed above the climax candle's midpoint before any undercut its low: the bottom-confirmed alert), 3 = failed (a candle undercut the climax low first), 4 = expired (neither within 5 candles, or the day ended). Empty without a climax today",
  },
  {
    key: 'climaxConfAge',
    label: 'Bottom age',
    name: 'Time since the bottom was confirmed',
    unit: 'sec',
    kind: 'dur',
    group: 'climax',
    hint: 'seconds since the candle that CONFIRMED the bottom started: within 5 traded 1-minute candles of the volume climax it closed above the climax candle\'s midpoint, and no candle undercut the climax low first. Only while Climax state = 2. "Bottom age <= 240" with "Climax state = 2" and "Climax age <= 1200" (which drops the rare late confirmation on a thin stock) is the seeded "Bottom Confirmed" scan. A "look now" prompt, not a system: no edge after costs in calibration',
  },
  {
    key: 'climaxConfPx',
    label: 'Bottom close',
    name: 'Bottom confirmed close',
    unit: '$',
    kind: 'price',
    group: 'climax',
    hint: "the confirming candle's close — the entry price the calibration measured. Only while Climax state = 2",
  },

  // --- bouncy ball short (2026-10-14) ---
  // Engine-native over every stock (signals/bouncyShort.ts, Go internal/bbs):
  // a support touched 3+ times with lower highs on each bounce, then a closed
  // 1-minute candle under it. An alert, a "look now" prompt (uncalibrated).
  {
    key: 'bbsAge',
    label: 'BBS age',
    name: 'Time since the bouncy ball short',
    unit: 'sec',
    kind: 'dur',
    group: 'bbs',
    hint: "seconds since today's latest BOUNCY BALL SHORT candle started: within today's last 90 traded 1-minute candles a support (the lowest low of a base) was touched at least 3 times (lows within 0.4%, touches 3+ candles apart with a 0.5%+ bounce between), each bounce topped at least 0.2% lower than the one before (at least 2 lower highs), and then a candle CLOSED more than 0.3% under the support. \"BBS age <= 240\" is the seeded \"Bouncy Ball Short\" scan. A \"look now\" prompt, not a system. Empty without one today",
  },
  {
    key: 'bbsSupport',
    label: 'BBS support',
    name: 'Bouncy ball short support',
    unit: '$',
    kind: 'price',
    group: 'bbs',
    hint: "the support level: the forming base's while BBS state = 1, the broken one's while BBS state = 2",
  },
  {
    key: 'bbsState',
    label: 'BBS state',
    name: 'Bouncy ball short state',
    unit: '1-2',
    kind: 'int',
    group: 'bbs',
    hint: '1 = forming (support + lower highs, the last close within 1% of the support: watch for the break), 2 = broke today. "BBS state = 1" lists the forming bases (no alert by default). Empty otherwise',
  },
  {
    key: 'bbsDvol',
    label: 'BBS $vol',
    name: 'Bouncy ball short dollar volume',
    unit: '$',
    kind: 'compact',
    group: 'bbs',
    hint: 'dollars traded in the breaking candle (close x volume); the seeded scan wants at least $100k',
  },
  {
    key: 'bbsDistPct',
    label: 'BBS dist',
    name: 'Bouncy ball forming: distance above support',
    unit: '%',
    kind: 'pct',
    group: 'bbs',
    hint: 'while a bouncy ball base is forming (BBS state = 1): how far the last price sits above the support, %. The seeded "Bouncy Ball Forming" scan sorts by it, nearest first. Empty otherwise',
  },

  // --- 9 EMA / VWAP cross (2026-10-25) ---
  // Engine-native (emavwap.go): the 9 EMA of 1-minute closes vs the session VWAP, judged on every closed 1-minute bar.
  {
    key: 'evBullAge',
    label: 'Bull age',
    name: 'Time since the bull VWAP cross',
    unit: 'sec',
    kind: 'dur',
    group: 'emavwap',
    hint: "seconds since the 1-minute bar that closed with the 9 EMA of 1-minute closes crossing ABOVE the session VWAP (pre-market VWAP before 09:30, the regular-hours VWAP from 09:30), today's latest. The EMA needs 9 closed 1-minute bars first and the cross must clear 0.05% of the VWAP; a cross outside the universe waits up to 10 minutes for it and is aged from the bar that fired",
  },
  {
    key: 'evBearAge',
    label: 'Bear age',
    name: 'Time since the bear VWAP cross',
    unit: 'sec',
    kind: 'dur',
    group: 'emavwap',
    hint: 'seconds since the 1-minute bar that closed with the 9 EMA of 1-minute closes crossing UNDER the session VWAP, today’s latest (same rules as the bull cross)',
  },
  { key: 'evEma', label: 'EMA9 1m', name: '9 EMA of 1-minute closes', unit: '$', kind: 'price', group: 'emavwap', hint: 'the 9 EMA of 1-minute closes after the latest closed 1-minute bar (SMA seed over the first 9 closes); empty until 9 bars have closed' },
  { key: 'evVwap', label: 'VWAP 1m', name: 'Session VWAP at the 1-minute bar', unit: '$', kind: 'price', group: 'emavwap', hint: 'the session VWAP read when the latest 1-minute bar closed: the pre-market VWAP before 09:30, the regular-hours VWAP from 09:30' },
  { key: 'evDist', label: 'EMA vs VWAP', name: '9 EMA vs VWAP', unit: '%', kind: 'pct', group: 'emavwap', hint: '(9 EMA of 1-minute closes − VWAP) ÷ VWAP at the latest closed 1-minute bar, %; positive = the EMA is above the VWAP' },
  { key: 'evBarVol', label: 'bar vol', name: 'Cross bar volume', unit: 'sh', kind: 'compact', group: 'emavwap', hint: 'shares traded in the 1-minute bar of the latest VWAP cross (bull or bear)' },
  { key: 'evBarDvol', label: 'bar $vol', name: 'Cross bar dollar volume', unit: '$', kind: 'compact', group: 'emavwap', hint: 'dollar volume (close x shares) of the 1-minute bar of the latest VWAP cross' },
  { key: 'evSessVol', label: 'day vol', name: 'Day volume at the cross', unit: 'sh', kind: 'compact', group: 'emavwap', hint: "today's cumulative volume (from 04:00 ET) when the latest VWAP cross bar closed" },

  // --- power candle (2026-10-21) ---
  // Engine-native over every stock: a dildo-rule candle with any top wick (never a real dildo).
  {
    key: 'pcAge',
    label: 'PWR age',
    name: 'Time since the power candle',
    unit: 'sec',
    kind: 'dur',
    group: 'power',
    hint: "seconds since today's latest POWER CANDLE started: a closed 1-minute candle passing every green dildo rule (the live Signal-window settings, new high of the day) except the close-near-high one, so any top wick; a real dildo is a dildo, not this. \"PWR age <= 240\" is the seeded \"Power Candle\" scan. Empty without one today",
  },
  { key: 'pcBodyPct', label: 'PWR body', name: 'Power candle body', unit: '%', kind: 'pct', group: 'power', hint: "the power candle's green body, (close − open) / open, %" },
  { key: 'pcTopPct', label: 'PWR wick', name: 'Power candle top wick', unit: '%', kind: 'pct', group: 'power', hint: "the power candle's top wick, (high − close) / (high − low), %" },
  { key: 'pcBodyX', label: 'PWR body×', name: 'Power candle body multiple', unit: 'x', kind: 'ratio', group: 'power', hint: "the power candle's body ÷ the mean body of the prior candles; empty = infinite (flat priors)" },
  { key: 'pcVolX', label: 'PWR vol×', name: 'Power candle volume multiple', unit: 'x', kind: 'ratio', group: 'power', hint: "the power candle's volume ÷ the mean volume of the prior candles; empty = infinite" },
  { key: 'pcDvol', label: 'PWR $vol', name: 'Power candle dollar volume', unit: '$', kind: 'compact', group: 'power', hint: "dollars traded in the power candle (close × volume)" },

  // --- power setup (2026-10-27): the filters + the plan, stamped on EVERY power candle (docs/power-setup.md) ---
  { key: 'psPass', label: 'PS pass', name: 'Power setup passes all filters', unit: '0-1', kind: 'int', group: 'power', hint: '2 = the filters passed inside a quiet window (05–07 and 16–20 ET by default): skipped; 1 = the latest power candle also passed both filters (close < 21% over the day open, 1m 9EMA up 2.1%+ over 3 bars); 0 = it did not (the values below say why). "PS pass = 1" is the seeded "Power Setup" scan. Empty without a power candle today' },
  { key: 'psFromOpen', label: 'PS from open', name: 'Power candle vs the day open', unit: '%', kind: 'pct', group: 'power', hint: "filter A: the power candle's close vs the day's open (the first traded 1-minute bar from 04:00 ET), %. Must be under 21" },
  { key: 'psEmaSlope', label: 'PS slope', name: '1m 9EMA slope at the power candle', unit: '%', kind: 'pct', group: 'power', hint: 'the 1-minute 9 EMA at the power candle close vs 3 bars earlier, %. Must be 2.1 or more' },
  { key: 'psGap', label: 'PS gap', name: 'Gap at the open', unit: '%', kind: 'pct', group: 'power', hint: 'the gap at the open vs the previous close, %: before 09:30 ET the first 04:00 print, from 09:30 the regular-session open. Shown for information; the gap filter is OFF by default (SCANNER_PS_MIN_GAP > 0 turns it on)' },
  { key: 'psEntry', label: 'PS entry', name: 'Power setup entry', unit: '$', kind: 'price', group: 'power', hint: 'plan: buy limit at the 1-minute 9 EMA at the power candle close (valid about 15 minutes)' },
  { key: 'psStop', label: 'PS stop', name: 'Power setup stop', unit: '$', kind: 'price', group: 'power', hint: 'plan: stop = 9 EMA x 0.94 (6% under the entry)' },
  { key: 'psTarget', label: 'PS target', name: 'Power setup target', unit: '$', kind: 'price', group: 'power', hint: 'plan: entry + 3R (R = entry - stop); the trade is trailed under the previous completed 5-minute low' },
  { key: 'psAge', label: 'PS age', name: 'Time since the power setup candle', unit: 'sec', kind: 'dur', group: 'power', hint: 'seconds since the latest power candle (the one the setup fields describe) started. "PS age <= 900" keeps the buy limit inside its ~15 minute validity' },
  { key: 'psAplus', label: 'PS A+', name: 'Power setup graded A+', unit: '0-1', kind: 'int', group: 'power', hint: '1 = the setup is A+: the 15 one-minute bars before the power candle span 5% or less (a tight base); the order ticket sizes it at 1.5x the selected risk with the Power Setup preset. 0 = not A+ (or not a setup)' },
  { key: 'psBaseRange', label: 'PS base', name: 'Power setup base range', unit: '%', kind: 'pct', group: 'power', hint: 'the 15 one-minute bars before the power candle: (max high - min low) / min low, %. A+ at 5% or less; empty with fewer than 3 prior bars' },

  // --- volume break (2026-10-28; internal/volbreak, docs/volume-break.md) ---
  { key: 'vbAge', label: 'VB age', name: 'Time since the volume break', unit: 'sec', kind: 'dur', group: 'volbreak', hint: "seconds since today's latest VOLUME BREAK candle started: on a stock up 10%+ with 1M+ shares, a closed 1-minute candle over the high of a 30+ minute consolidation (high tested twice, at most 12% wide) on 3x+ the range's average 1-minute volume. Once per level. \"VB age <= 240\" is the seeded \"Vol Break\" scan. Empty without one today" },
  { key: 'vbLevel', label: 'VB level', name: 'Volume break level', unit: '$', kind: 'price', group: 'volbreak', hint: 'the consolidation high that broke; a close back under it cancels the setup' },
  { key: 'vbVolX', label: 'VB vol×', name: 'Volume break volume multiple', unit: 'x', kind: 'ratio', group: 'volbreak', hint: "the break candle's volume ÷ the range's average 1-minute volume (3x or more)" },
  { key: 'vbEma', label: 'VB 9EMA', name: '1m 9 EMA at the volume break', unit: '$', kind: 'price', group: 'volbreak', hint: 'the 1-minute 9 EMA at the break candle close: where the pullback entry is expected' },
  { key: 'vbState', label: 'VB state', name: 'Volume break state', unit: '1-4', kind: 'int', group: 'volbreak', hint: '1 = broke, waiting for the 9 EMA pullback; 2 = the 9EMA entry came; 3 = failed (closed back under the level); 4 = expired (no pullback within 30 minutes). Empty without a break today' },
  { key: 'vbEntryAge', label: 'VB entry age', name: 'Time since the volume break 9EMA entry', unit: 'sec', kind: 'dur', group: 'volbreak', hint: 'seconds since the entry candle started: the first pullback after the break whose low came within 0.3% of the 1m 9 EMA (or through it) while closing over the level. "VB state = 2 and VB entry age <= 240" is the seeded "Vol Break 9EMA" scan' },
  { key: 'vbEntryPx', label: 'VB entry', name: 'Volume break 9EMA entry', unit: '$', kind: 'price', group: 'volbreak', hint: 'the 1-minute 9 EMA at the entry candle (the level to work a limit at). Alerts only - nothing is ordered' },

  // --- ignition (2026-10-14) ---
  // Engine-native over every stock (signals/ignition.ts, Go internal/ignition).
  {
    key: 'ignAge',
    label: 'IGN age',
    name: 'Time since the ignition',
    unit: 'sec',
    kind: 'dur',
    group: 'ignition',
    hint: "seconds since today's latest IGNITION candle started: a closed 1-minute candle up 2–8% (a full dildo is not one) on 5x+ the prior 10 candles' mean volume, out of a quiet base (the 15 candles before it span ≤ 8%), closing over the base high and making a new high of the day; close ≥ $0.20 and ≥ $50k traded in it. One per base, at most one per 10 minutes. \"IGN age <= 240\" is the seeded \"Ignition\" scan. A \"look now\" prompt, not a system. Empty without one today",
  },
  {
    key: 'ignBodyPct',
    label: 'IGN body',
    name: 'Ignition candle body',
    unit: '%',
    kind: 'pct',
    group: 'ignition',
    hint: "the ignition candle's green body, (close − open) / open, %",
  },
  {
    key: 'ignVolX',
    label: 'IGN vol×',
    name: 'Ignition volume multiple',
    unit: 'x',
    kind: 'ratio',
    group: 'ignition',
    hint: "the ignition candle's volume ÷ the mean volume of the 10 candles before it",
  },
  {
    key: 'ignPass',
    label: 'IGN pass',
    name: 'Ignition passes the skip rules',
    unit: '0-1',
    kind: 'int',
    group: 'ignition',
    hint: '1 = the ignition passes all four skip rules (up at most 3% on the day before it, candle ≤ $250k traded, close ≥ $1.60, volume ≤ 50x the prior 10 candles); 0 = skipped (still listed, dimmed, in SIGNALS). "IGN pass = 1" is part of the seeded "Ignition" scan. Empty without an ignition today',
  },

  // --- short availability ---
  { key: 'shortable', label: 'Shortable', name: 'IBKR shortable', kind: 'bool', group: 'short', filter: false, hint: 'IBKR currently has shares to borrow (refreshed every 15 min)' },
  { key: 'shortAvail', label: 'Borrow', name: 'Shares available to borrow', unit: 'shares', kind: 'compact', group: 'short', hint: 'IBKR shares available to borrow, refreshed every 15 min; ">10M" is a floor, not an exact count' },
  { key: 'shortFee', label: 'Fee %', name: 'Borrow fee', unit: '% / yr', kind: 'pctAbs', group: 'short', hint: 'IBKR annual borrow cost, % — high = hard to borrow' },

  // --- reference & trend ---
  { key: 'float', label: 'Float', name: 'Free float', unit: 'shares', kind: 'compact', group: 'reference', hint: 'free float (weekly reference; may be a shares-outstanding proxy)' },
  { key: 'shsOut', label: 'Shs out', name: 'Shares outstanding', unit: 'shares', kind: 'compact', group: 'reference' },
  { key: 'dtrend', label: 'Daily', name: 'Daily trend', unit: '1/0/-1', kind: 'trend', group: 'reference', hint: 'daily-chart trend: 1 = uptrend, 0 = flat, -1 = downtrend — "Daily trend >= 0" hides daily downtrends' },
  { key: 'chg20d', label: '20d %', name: '20-day change', unit: '%', kind: 'pct', group: 'reference', hint: '% change over the last ~20 trading days (daily chart)' },
  { key: 'secType', label: 'Type', name: 'Security type', kind: 'enum', group: 'reference', hint: 'Common Stock (CS), ETF, or ADR (ADRC)' },
  { key: 'news', label: 'News', name: 'Fresh news', kind: 'bool', group: 'reference', filter: false, hint: 'a headline published in the last 30 minutes' },
  { key: 'newsAge', label: 'News age', name: 'Time since latest headline', unit: 'sec', kind: 'dur', group: 'reference', hint: 'seconds since the latest headline (300 = 5 min)' },

  // --- other ---
  { key: 'hits', label: 'Hits', name: 'Entries into this scan today', unit: 'x today', kind: 'hits', group: 'other', hint: 'times this stock entered THIS scan today — 2+ = recurring mover ("Hits >= 2" filters to repeats only)' },
  { key: 'halted', label: 'Halted', name: 'Trading halted', kind: 'bool', group: 'other', filter: false },
  { key: 'chart', label: 'Chart', name: 'Inline chart', kind: 'chart', group: 'other', filter: false, hint: 'inline candlestick chart for each row (widens rows to fit it)' },
  { key: 'sym', label: 'Symbol', name: 'Symbol', kind: 'text', group: 'other', column: false },
];

export const FIELD_BY_KEY: Record<string, FieldDef> = Object.fromEntries(FIELD_DEFS.map((d) => [d.key, d]));

export function fieldDef(key: string): FieldDef | undefined {
  return FIELD_BY_KEY[key];
}

// fieldLabel is the short header text; unknown keys (a future server field the
// UI doesn't know yet) fall back to the raw key so nothing ever renders blank.
export function fieldLabel(key: string): string {
  return FIELD_BY_KEY[key]?.label ?? key;
}

// fieldMenuLabel is the longer form for dropdowns: "5-minute change (%)".
export function fieldMenuLabel(key: string): string {
  const d = FIELD_BY_KEY[key];
  if (!d) return key;
  return d.unit ? `${d.name} (${d.unit})` : d.name;
}

// fieldTitle is the tooltip text: full name, unit, wire key and hint.
export function fieldTitle(key: string): string {
  const d = FIELD_BY_KEY[key];
  if (!d) return key;
  const head = d.unit ? `${d.name} (${d.unit})` : d.name;
  return d.hint ? `${head} · ${d.key}\n${d.hint}` : `${head} · ${d.key}`;
}

// Fields usable in scan filters and sort dropdowns, in registry order.
export function filterableFields(): FieldDef[] {
  return FIELD_DEFS.filter((d) => d.filter !== false && d.kind !== 'chart');
}

// Fields usable as grid columns (the column chooser), in registry order.
export function columnableFields(): FieldDef[] {
  return FIELD_DEFS.filter((d) => d.column !== false);
}

export function fieldsInGroup(id: FieldGroupId, pool: FieldDef[] = FIELD_DEFS): FieldDef[] {
  return pool.filter((d) => d.group === id);
}

// Tone for a numeric value of this field: signed percents color by sign,
// everything else stays neutral. Used by grid cells and the symbol panels.
export function fieldTone(key: string, v: unknown): 'pos' | 'neg' | '' {
  const d = FIELD_BY_KEY[key];
  if (!d || d.kind !== 'pct' || typeof v !== 'number' || !isFinite(v)) return '';
  return v >= 0 ? 'pos' : 'neg';
}

// Top-level categories for the scan editor's field dropdowns (filters + sort),
// shown as <optgroup>s so the long list is navigable. Each registry group maps
// to one category; the group label is kept as a prefix when a category holds
// several groups. Order here is the order in the menu.
export const FIELD_CATEGORIES: { label: string; groups: FieldGroupId[] }[] = [
  { label: 'Price', groups: ['price', 'candle5m', 'session'] },
  { label: 'Volume', groups: ['volume', 'premarket', 'rth'] },
  { label: 'Levels & EMA', groups: ['levels', 'ema1m'] },
  { label: 'Fundamentals (news, float, short)', groups: ['reference', 'short'] },
  { label: 'Signals', groups: ['e9flag', 'dildo', 'power', 'volbreak', 'emavwap', 'ignition', 'climax', 'bbs', 'flags', 'iflags'] },
  { label: 'Other', groups: ['other'] },
];

// groupedFields splits a field list into the menu categories (empty ones are
// dropped; any group not mapped above lands in "Other").
export function groupedFields(pool: FieldDef[]): { label: string; defs: FieldDef[] }[] {
  const mapped = new Set(FIELD_CATEGORIES.flatMap((c) => c.groups));
  const out = FIELD_CATEGORIES.map((c) => ({
    label: c.label,
    defs: c.groups.flatMap((g) => pool.filter((d) => d.group === g)),
  }));
  const stray = pool.filter((d) => !mapped.has(d.group));
  if (stray.length) out[out.length - 1].defs.push(...stray);
  return out.filter((c) => c.defs.length > 0);
}
