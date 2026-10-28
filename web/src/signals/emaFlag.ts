// Pure 9 EMA FLAG engine (LONG ONLY): turns a symbol's own candles into a
// deterministic "it broke out, it is now going sideways while the 9 EMA catches
// up — buy the break of the flag" call. No DOM, no store, no `Date.now()` (the
// caller passes `nowMs`); like shortFade.ts it imports the vendored chart
// engine TYPE-ONLY, so its vitest suite runs in plain node against hand-built
// candles and the real 1-minute fixtures in src/signals/fixtures/.
//
// THE SETUP, in the trader's own words (2026-10-05, a TWS screenshot of BEAT's
// 1-minute chart with horizontal red lines drawn on the flags):
//
//   "I want to alter the Algorithm. Long only. I want to get signals when a
//    stock is breaking out and then in a more or less horizontal way is
//    approaching the 9EMA. I want a signal or alert that the price is moving
//    more or less horizontal on a stock."
//
// The picture behind it: a penny momentum stock fires an IMPULSE leg far above
// its rising 9 EMA, then stops going up and moves SIDEWAYS — a flag — while the
// 9 EMA, which is still rising underneath, catches up to the flat price. When
// the EMA is close the risk to a stop under it is small, and the break of the
// flag's high is the trade; afterwards the 9 EMA is the trail (the user rides
// the line). Two moments matter to him and the engine reports both: "it is
// going sideways" (FLAT — an alert on its own, he asked for it by name) and
// "the EMA has caught up" (READY / 9 EMA NEAR — the setup is armed).
//
// THE STATE MACHINE, and why each gate exists — idle -> impulse -> flat ->
// ready -> long (long may also be open while a later flag forms, for the adds):
//
//   idle     Nothing to trade. Waiting for an impulse.
//   impulse  A CLOSE at least `minExtPct` (5%) above the EMA on a bar whose
//            HIGH beats the highest high of the prior `breakoutLookback` (30)
//            bars of the same ET day — and at least 5 prior bars exist that
//            day. Both halves are needed: the extension says "this is
//            accelerating away from the average" (a drifting stock sits on its
//            EMA and never gets 5% above it), the new N-bar high says "it is
//            a breakout, not just an extended bounce inside a range" (BEAT's
//            08:50 close was +6% over the EMA but under the 0.5899 high of
//            08:39 — chop, not a breakout; 08:55 beat it). The 5-prior-bar
//            floor stops the first bars of a pre-market session, where "the
//            prior high" is two prints and meaningless, from arming anything.
//            No entry is possible in this state: it exists to say "wait".
//            The impulse remembers its BASE LOW (lowest low of the
//            `impulseBaseBars` bars before the impulse bar, and the bar
//            itself) — where the move started — and its running TOP (highest
//            high since). top - baseLow is "the move", the yardstick the next
//            two gates are measured in, because "flat" is relative: a 0.02
//            wiggle is nothing on a 0.35 leg and everything on a 0.05 leg.
//   flat     The last `minFlatBars` (3) closed bars, all at/after the top bar,
//            qualify as sideways: their CLOSES span at most `maxFlatOfMovePct`
//            (35%) of the move AND at most `maxFlatPct` (10%) of price (the
//            second cap exists only because 35% of an enormous move can be a
//            loose bag); no close is more than `emaBreakPct` (1%) under its
//            own EMA; and the lowest low of the window stays above the
//            halfway point of the move (`maxRetracePct`, 50%). The top bar
//            itself COUNTS toward the bars needed (and its close must hold the
//            EMA floor and sit inside the opening span) but it is the thrust
//            the flag hangs from, not part of the flag: the retrace low, the
//            flag's low (box, stop) and its running close range are measured
//            over the bars AFTER it. Closes, not
//            highs/lows, because wicks on a penny stock are noise the trader
//            ignores — he sees a flag where the closes stack up. The EMA floor
//            is the "above the 9 EMA" of his sentence: a flag that has already
//            lost the EMA is not a flag, it is a pullback, and that is the
//            other engine's (shortFade.ts) territory. The retrace cap makes
//            it a pause, not a reversal. The flag is CAPPED by the high it is
//            holding under (flagHigh = top), its trigger is that high plus a
//            small buffer (`breakBufferPct`, 0.3% — a tick through the high is
//            a poke, not a break), and the flag stays open while its closes
//            keep satisfying the span rules. A flag that drifts out of them
//            without breaking FADES silently (outcome 'faded'); a new one can
//            form from later bars. An alert fires once, when it opens.
//            A flag also EXPIRES: one whose box (top bar included) spans more
//            than `maxFlagBars` (30) closed bars without breaking ends silently
//            as 'stale' — see FLAG EXPIRY below.
//   ready    The flag's latest close is within `readyDistPct` (4%) above the
//            EMA: the EMA has caught up. This is the user's "approaching the
//            9EMA", and it is the gate that makes the entry good: with the EMA
//            this close, the stop under it is a few percent away, so the
//            break of the flag high is a cheap, well-defined risk. A flag
//            that opens already inside the band goes straight to ready.
//   long     Price traded above the flag high (+ buffer) while READY. The
//            stop is max(flag low, EMA of the last closed bar) less
//            `stopBufferPct` and then TRAILS the EMA up (never down); the
//            trade is closed by a CLOSE under it (`stopByClose`, the repo's
//            standing convention: structure is judged on closed bars so a
//            wick can't un-happen). Later flags that break while long are
//            ADDs, at most `maxAdds` — the trader's own marks on BEAT show
//            three flags in one run, and scaling in on each is what he does;
//            more than that is chasing.
//
// FLAG EXPIRY (`maxFlagBars`, 30). A flag is a pause inside a move, and on a
// 1-minute chart a pause that has not resolved in ~30 minutes is a RANGE, not
// a flag: the thrust it hangs from is half an hour old, the trader who would
// have bought the break is long gone, and "waiting for the break of a
// 30-minute-old high" is a wish, not a setup (internal/pattern's maxBaseBars
// draws the same line for daily bases). Nothing used to bound it: PTC, a $192
// stock that gapped +34% at 04:13 and drifted sideways all day, sat in ONE
// "active" flag from 06:34 to 13:24 (373 bars, READY the whole time) and the
// market scan listed it as a fresh flag for six hours. A flag whose box —
// counted from its TOP bar, inclusive — spans more than `maxFlagBars` closed
// bars without breaking therefore ends with outcome 'stale': silent (no event,
// like 'faded'), drawn dashed in the muted colour, and the state falls back to
// IMPULSE (it is off the sideways / ready scans). The second half of the rule:
// NO new flag may open until a NEW TOP is made (a high above the current one).
// Without it the very next bar would re-open a flag from the same top and print
// FLAT again, over and over, for the whole day. Both halves are ONE test: "the
// top is more than `maxFlagBars` bars old" (counted from the top bar,
// inclusive). It expires a flag under that top, and it refuses to open one (a
// flag opened under such a top would be born stale, and a dead flag gets no
// FLAT alert); only a higher high moves the top and so resets the count. A
// span-'faded' flag keeps its old freedom: a fade means the closes spread out,
// so a fresh tight window under the same top, while that top is still young
// enough, is real new information (F6 pins it). The expiry is decided on a
// CLOSED bar, after that bar's own intrabar break has had its say (a flag that
// breaks on bar 31 is still a break), and BEFORE the bar's READY test (a flag
// never prints 9 EMA NEAR on the bar that expires it). The bars are BARS, not
// minutes: a thin stock prints no bar in a minute without a trade, so 30 bars
// can be 40 minutes of clock. 0 switches the expiry off.
//
// A break BEFORE the EMA has caught up is an EARLY BREAK: listed, never a
// marker, because that is the chase the whole setup exists to avoid
// (`entryNeedsReady` can switch the gate off). A CLOSE more than `emaBreakPct`
// under the EMA ends the setup: the flag FAILED if one was active, the impulse
// is over, and a long, if any, is left to its own stop.
//
// EMA and ATR run CONTINUOUSLY over the whole candle array (previous day +
// today), exactly as the chart draws its EMA (`emaSeries` is the vendored
// registry's `emaArr` arithmetic, byte for byte — F1 pins it), so the engine
// reads the same line the trader is looking at, with no re-seeding at the
// day boundary. The STATE machine, by contrast, runs per ET day (etDayKey,
// the same grouping the chart's VWAP resets on): an impulse or a position
// never leaks across midnight. ATR(14) (Wilder) is exposed on `levels.atr` for
// display and evidence, read at the last CLOSED bar so a forming bar's range
// can never move it; the only rule that reads it is the SLAM's body test (the
// ATR of the bar BEFORE the slam bar), and no long-side rule gates on it.
//
// Everything is judged on CLOSED bars — except the break of the flag high
// (and, when `stopByClose` is off, a stop hit by the low), which also run on
// the bar still printing: a forming bar's `high` only ever grows and its
// `low` only ever falls, so those two can only become MORE true before the bar
// closes, never flip back. That is the append-only contract (tested in F9): the
// closed-bar run reproduces every forming-bar event with the same ts, kind,
// label and price. It holds by construction — the closed loop runs the SAME
// `intrabar` step the forming pass runs, FIRST for each bar, against the same
// state, and only then the close-based rules (stop by close, trail, impulse,
// flag open/extend/fade, fail). The brief lists the exit rule first; with the
// exit ahead of the break a bar that both broke the flag high and closed under
// the stop would print ADD then EXIT on the forming pass but EXIT then LONG
// on the close — a different history. Putting the intrabar rules first is the
// same fix shortFade.ts made in iteration 6 (I6-F4). Stops computed at entry use
// the EMA of the last CLOSED bar (the forming bar's EMA moves with its close)
// for the same reason. One consequence: a position opened by a bar is also
// judged against that bar's own close (a bar that broke the flag high and
// closed under the stop exits at once, against the stop in force BEFORE this
// bar's raise), and its close raises the trail like any later bar's.
//
// Choices the brief left open, made here so they are reviewable:
//   - A high above the flag high that stays under the trigger (a poke) closes
//     the flag as 'faded': the top moved, so the old flag no longer holds
//     "under the high" and a fresh one has to form from the new top.
//   - A close under the EMA by more than `emaBreakPct` takes priority over a
//     span fade: it is reported as FLAG FAILED, the louder and truer message.
//   - A flag that fades on a bar is not re-detected on that same bar; a new
//     FLAT can open from the next bar on.
//   - The flag's span is measured over the closes since the flat WINDOW began
//     (not a rolling three), so a flag can't widen a little each bar forever;
//     the window's bars before the top-bar exclusion above only gate the
//     OPENING, the running range starts on the first bar after the top.
//   - Event prices are "the level the event is about": the high for BREAKOUT,
//     the trigger for FLAT / 9 EMA NEAR / EARLY BREAK / LONG / ADD, the close
//     for FLAG FAILED and SLAM, the exit price for EXIT.
//
// 9 EMA SLAM (2026-10-07, `slam` + the six `slam*` params). The trader again,
// with a TWS screenshot of SPAI's 1-minute chart:
//
//   "I would like to scan for this signal as well. 1 minute chart. A candle
//    after a stock has been up and trades more or less sideways SLAMS through
//    the 9EMA."
//
// The bearish mirror of the flag: the stock ran, went sideways above the
// rising 9 EMA — exactly what FLAT / READY / LONG already say — and then ONE
// candle opens up there and closes far under the line. SPAI 2026-10-07: run
// 08:01 4.09 -> 08:12 high 5.10, sideways 08:12-08:14 above the EMA (~4.85),
// then 08:15 O 4.9107 H 4.92 L 4.50 C 4.566 on ~1.6x the volume of the bars
// before it: from above the EMA to ~6% under it in one bar, closing near its
// low. 08:16 kept falling but OPENED under the EMA — the slam was 08:15.
// It is an ALERT and a chart marker only: the engine opens nothing on it and
// the long side (entries, exits, stops, states, levels) is not touched — a
// SLAM changes no state, it only reports. On a CLOSED bar i (never the forming
// bar: a forming close can still recover, and events are append-only):
//   1. setup: the state at the end of bar i-1 (same ET day) is flat, ready or
//      long — the existing machine's own "it ran up and went sideways (or broke
//      out of it)"; no second definition of "up then sideways";
//   2. from above: close[i-1] >= ema[i-1] and open[i] >= ema[i-1] * (1 -
//      `slamOpenTolPct`/100) (an open a tick under the line on a sub-$1 stock
//      still counts);
//   3. through: close[i] <= ema[i] * (1 - `slamBelowPct`/100);
//   4. hard: open - close >= `slamBodyAtr` x ATR (Wilder, `atrPeriod`, at bar
//      i-1; 0 = no ATR test) and (open - close)/open >= `slamMinBodyPct`/100;
//   5. weak close: close - low <= `slamCloseInRange` x (high - low);
//   6. volume: volume[i] >= `slamVolX` x the mean volume of the up-to-10 bars
//      before it that day (no test with fewer than 3 such bars).
// At most ONE per flag / position episode. The episodes are the position and
// the flag open at the end of bar i-1 — each its own: under edited params
// (trailEma off, a wide stop) a position can survive its slam and a LATER flag
// open next to it, and that flag still gets its slam. A bar may slam while
// either has not slammed, and its slam marks both. It is emitted LAST on its
// bar, after whatever that bar's long side reported: usually EXIT / FLAG
// FAILED (a close >= 3% under the EMA is through the 1% fail gate), but when
// the bar first broke a flag that was not yet READY it follows that EARLY
// BREAK — the break closed the flag before the fail gate saw it, so there is
// no FLAG FAILED. With no position open that EARLY BREAK is all it follows.
// While long (the break of a not-yet-READY ADD flag is an EARLY BREAK too) the
// same close is also under the stop (always, with the default EMA trail), so
// that bar reads EARLY BREAK, EXIT, SLAM. Its side is 'short' (the only short
// event this engine has), it carries no marker (the Signal window draws its
// own), and its price is the slam bar's close.
// Defaults calibrated (2026-10-07, read-only) on 60 days of cached minute bars
// of every pmVolX >= 1 candidate: ~2 slams a day market-wide, and the price
// 15 minutes later was lower 62% of the time (median -2.9%). The only change
// from the brief's starting values is `slamBelowPct` 2 -> 3 (2 gave ~1.4x the
// alerts, 167 vs 120 over the 60 days, and about half the follow-through:
// 15-minute median -1.6% vs -2.9%). SPAI 08:15 fires, 08:16 does not.

import type { Candle } from '../vendor/blackthorn-charts';
import { etDayKey } from '../marketClock';
import { fmtEt, fmtPx } from './shortFade';

// ─── Types ───────────────────────────────────────────────────────────────

export interface FlagParams {
  emaPeriod: number; // 9
  atrPeriod: number; // 14   (Wilder: evidence, and the SLAM's body test)
  minExtPct: number; // 5    impulse: a CLOSE at least this % above the EMA …
  breakoutLookback: number; // 30   … on a bar whose HIGH beats the highest high of the prior N bars of the same ET day (needs >= 5 prior bars that day)
  impulseBaseBars: number; // 10   the impulse's base low = min low of the N bars before the impulse bar (and the bar itself)
  minFlatBars: number; // 3    closed bars, all at/after the top bar, that must qualify as flat
  maxFlatOfMovePct: number; // 35   flat = the window's CLOSES span <= this % of the move (top - base low)
  maxFlatPct: number; // 10   … and <= this % of price (sanity cap for enormous moves)
  maxRetracePct: number; // 50   the flag's lowest LOW must hold above base + (1 - this/100) * move
  emaBreakPct: number; // 1    a CLOSE more than this % under the EMA ends the setup (flag FAILED if one was active)
  readyDistPct: number; // 4    READY: the flag's latest close is within this % above the EMA
  entryNeedsReady: boolean; // true a flag-high break counts as an entry only once READY; before that it is an EARLY BREAK (list-only)
  breakBufferPct: number; // 0.3  trigger = flag high * (1 + this/100)
  stopBufferPct: number; // 1.0  initial stop = max(flag low, EMA of the last CLOSED bar) * (1 - this/100); trailing uses the same buffer
  trailEma: boolean; // true while long, each closed bar raises the stop to EMA * (1 - buffer) (never lowers it)
  stopByClose: boolean; // true exit on a CLOSE below the stop; false = any LOW at/below it (also on the forming bar)
  maxAdds: number; // 2    later flag breakouts while long are ADDs, at most this many per position
  maxFlagBars: number; // 30   a flag whose box (top bar included) spans more than this many closed bars without breaking ends 'stale' (silent); no new flag until a new top. 0 = no limit
  // 9 EMA SLAM (see the module comment): an alert only, it never changes the state, the levels or the long side's events.
  slam: boolean; // true  master switch for the SLAM event
  slamBelowPct: number; // 3    the slam bar CLOSES at least this % under its own EMA …
  slamOpenTolPct: number; // 0.5  … having OPENED no more than this % under the previous bar's EMA (whose close was at/above it)
  slamBodyAtr: number; // 1    its body (open − close) is at least this many ATRs (Wilder, `atrPeriod`, previous bar); 0 = no ATR test
  slamMinBodyPct: number; // 2    … and at least this % of its open
  slamCloseInRange: number; // 0.35 it closes in the lowest this fraction of its range: close − low <= this × (high − low)
  slamVolX: number; // 1.3  its volume is at least this × the mean of the up-to-10 earlier bars of the day (no test with fewer than 3)
}

export const DEFAULT_FLAG_PARAMS: FlagParams = {
  emaPeriod: 9,
  atrPeriod: 14,
  minExtPct: 5,
  breakoutLookback: 30,
  impulseBaseBars: 10,
  minFlatBars: 3,
  maxFlatOfMovePct: 35,
  maxFlatPct: 10,
  maxRetracePct: 50,
  emaBreakPct: 1,
  readyDistPct: 4,
  entryNeedsReady: true,
  breakBufferPct: 0.3,
  stopBufferPct: 1.0,
  trailEma: true,
  stopByClose: true,
  maxAdds: 2,
  maxFlagBars: 30,
  slam: true,
  slamBelowPct: 3,
  slamOpenTolPct: 0.5,
  slamBodyAtr: 1,
  slamMinBodyPct: 2,
  slamCloseInRange: 0.35,
  slamVolX: 1.3,
};

export type FlagState = 'idle' | 'impulse' | 'flat' | 'ready' | 'long';

export type FlagEventKind =
  | 'impulse' // a breakout bar armed the engine: "wait for it to go sideways" (list-only)
  | 'flat' // the price is moving sideways above the rising EMA — the alert the trader asked for (list-only)
  | 'emaNear' // the flag is inside `readyDistPct` of the EMA: armed (list-only)
  | 'entry' // LONG: the flag high broke while READY (marker 'buy')
  | 'add' // ADD: a later flag broke while long, capped by `maxAdds` (marker 'buy')
  | 'earlyBreak' // the flag high broke before the EMA caught up — chase risk (list-only)
  | 'failed' // a close more than `emaBreakPct` under the EMA while a flag was active (list-only)
  | 'exit' // the long's stop (EMA trail) was hit (marker 'sell')
  | 'slam'; // 9 EMA SLAM: one candle from above the EMA to well under it out of a flag / position (bearish alert, side 'short', no marker)

export interface FlagEvent {
  ts: number; // the candle's ts the event belongs to
  kind: FlagEventKind;
  price: number; // the level the event is about (see the module comment)
  label: string; // 'BREAKOUT', 'FLAT', '9 EMA NEAR', 'LONG (FLAG)', 'ADD (FLAG)', 'EARLY BREAK', 'FLAG FAILED', 'EXIT', 'SLAM (9 EMA)'
  note: string; // one sentence for the events list, with ET times and prices
  marker?: 'buy' | 'sell'; // draw as an engine Execution marker; absent = list-only
  // The Signal window's view contract carries a side on every event. Every event of this long-only engine is 'long'
  // except the SLAM, the one bearish call it makes (an alert, never a trade): 'short'.
  side: 'long' | 'short';
  // FLAT / 9 EMA NEAR / SLAM only: the EVENT bar's own close vs its 9 EMA, % (positive = above; a SLAM's is negative).
  // The voice reads this instead of the result's live distance, which is the FORMING next bar's by the time the event
  // is first seen.
  emaDistPct?: number;
}

// One flag, for drawing. The same object is updated as the flag extends and
// closes, so a result's boxes always describe each flag's final state.
export interface FlagBox {
  startTs: number; // the TOP bar the flag hangs from
  endTs: number; // the last bar the flag held (or the bar that broke it)
  high: number; // the flag high (the top it is holding under)
  low: number; // the flag's lowest low since the top
  trigger: number; // high * (1 + breakBufferPct/100)
  readyTs: number | null; // the bar the EMA caught up on, or null
  // 'stale': outlived `maxFlagBars` without breaking (a range, not a flag); the Signal window draws it like 'faded'.
  outcome: 'active' | 'broke' | 'early' | 'failed' | 'faded' | 'stale';
}

export interface FlagLevels {
  ema: number | null; // the EMA at the last candle (what the chart line shows right now)
  atr: number | null; // Wilder ATR at the last CLOSED bar
  baseLow: number | null; // the live impulse's base low
  top: number | null; // the live impulse's running high
  topTs: number | null;
  flagHigh: number | null; // the active flag's cap
  flagLow: number | null; // the active flag's lowest low since the top
  trigger: number | null; // flagHigh * (1 + buffer), while a flag is active
  stop: number | null; // the open position's stop; else the stop a break of the active flag would get
  entryPrice: number | null; // the FIRST entry of the open position (an add never moves it)
  entryTs: number | null;
  adds: number;
}

export interface FlagEvidence {
  extPct: number | null; // % the impulse's top sits above the EMA (at the top bar)
  flatBars: number | null; // bars the active flag has held, top bar included
  flatSpanPctOfMove: number | null; // span of the flag's closes as % of the move
  flatSpanPct: number | null; // span of the flag's closes as % of price
  emaDistPct: number | null; // the last candle's close vs the EMA, % (positive = above)
  retracePct: number | null; // how much of the move the lows since the top have given back, %
  volDry: number | null; // mean volume since the top bar / mean volume of the leg up to it; under 1 = drying up
  riskPct: number | null; // (trigger - stop)/trigger while a flag is active; (entry - stop)/entry while long
  openPnlPct: number | null; // the last close vs the entry, while long
  // The latest 9 EMA SLAM of the evaluated (last) ET day, null without one: its bar's ts, its close, and that close
  // vs the bar's EMA in % (negative: under it). What the market scan's e9SlamAge / e9SlamPct read (Go: Result.LastSlam*).
  lastSlamTs: number | null;
  lastSlamClose: number | null;
  lastSlamPct: number | null;
  // HOD FLAG (alert only): the FLAT bar's ts of the evaluated day's latest flag whose run-up top was a NEW HIGH OF THE
  // DAY (above every earlier bar's high of the day, pre-market included); null without one. Decided once when the flag
  // opens, so it is once per flag. What the market scan's e9HodFlagAge reads (Go: Result.LastHodFlagTs).
  lastHodFlagTs: number | null;
}

export interface FlagResult {
  state: FlagState;
  dayKey: string; // ET 'YYYY-MM-DD' of the last day evaluated ('' for no bars)
  levels: FlagLevels;
  evidence: FlagEvidence;
  events: FlagEvent[]; // ALL days, oldest first, in EMISSION order
  boxes: FlagBox[]; // every flag of every day, oldest first
  // True when the state is 'impulse' but its top is older than a flag may be (FLAG EXPIRY: the flag expired, or none
  // formed in time) — the engine's own 'RANGE — no flag' read. The one place that decides it; the headline and the
  // Signal window's strip badge (view.ts) both key on it, so the badge cannot say BREAKOUT over a RANGE headline.
  topExpired: boolean;
  headline: string;
  detail: string[];
}

export interface FlagInput {
  candles: Candle[]; // ts ascending; may span the previous trading day + today
  tfMs: number; // candle width (60_000 for 1m, 300_000 for 5m)
  nowMs: number; // the last candle is the FORMING bar iff nowMs < last.ts + tfMs
  params?: Partial<FlagParams>;
}

// ─── EMA / ATR (the chart's own arithmetic) ──────────────────────────────

/**
 * EMA of `closes`, aligned to the input, null during warm-up. The arithmetic is
 * the vendored chart engine's `emaArr` (indicators/registry.ts) copied
 * operation for operation — SMA seed of the first `period` closes, then
 * k = 2/(period+1) — so the numbers are bit-identical to the line on the chart
 * (F1 asserts element-by-element equality against COMPUTE.ema).
 */
export function emaSeries(closes: number[], period: number): (number | null)[] {
  const out: (number | null)[] = new Array(closes.length).fill(null);
  const k = 2 / (period + 1);
  let prev: number | null = null;
  let sum = 0;
  for (let i = 0; i < closes.length; i++) {
    if (i < period - 1) {
      sum += closes[i]!;
      continue;
    }
    if (i === period - 1) {
      sum += closes[i]!;
      prev = sum / period;
    } else {
      prev = closes[i]! * k + (prev as number) * (1 - k);
    }
    out[i] = prev;
  }
  return out;
}

// Wilder ATR, same construction as the registry's `atr` (SMA of the first
// `period` true ranges, then (prev*(period-1) + tr)/period).
function atrSeries(candles: Candle[], period: number): (number | null)[] {
  const out: (number | null)[] = new Array(candles.length).fill(null);
  let acc = 0;
  let prev: number | null = null;
  for (let i = 0; i < candles.length; i++) {
    const c = candles[i]!;
    const pc = i > 0 ? candles[i - 1]!.close : c.close;
    const tr = Math.max(c.high - c.low, Math.abs(c.high - pc), Math.abs(c.low - pc));
    if (i < period) {
      acc += tr;
      if (i === period - 1) {
        prev = acc / period;
        out[i] = prev;
      }
      continue;
    }
    prev = ((prev as number) * (period - 1) + tr) / period;
    out[i] = prev;
  }
  return out;
}

// ─── Formatting helpers ──────────────────────────────────────────────────

function pct1(v: number): string {
  return `${v.toFixed(1)}%`;
}

// The close's side of the EMA in words: '3.3% above' for a close over it, '0.2% under it' for one
// under it (a flag may hold up to `emaBreakPct` under the line, so the distance can be negative).
function distPhrase(dist: number): string {
  return dist >= 0 ? `${pct1(dist)} above` : `${pct1(-dist)} under it`;
}

// Signed one-decimal percent ('+23.8%', '-3.0%'); never prints '-0.0%'.
function spct1(v: number): string {
  const r = Number(v.toFixed(1));
  const z = Object.is(r, -0) ? 0 : r;
  return `${z >= 0 ? '+' : ''}${z.toFixed(1)}%`;
}

// ─── Per-day machine ─────────────────────────────────────────────────────

// An impulse needs this many earlier bars the same ET day before "a new
// N-bar high" means anything (the brief: >= 5).
const MIN_PRIOR_BARS = 5;

// The SLAM's volume yardstick: the mean volume of up to this many bars before
// the slam bar, the same ET day ...
const SLAM_VOL_BARS = 10;
// ... and no volume test at all with fewer than this many (the day's first
// prints are no yardstick).
const SLAM_MIN_VOL_BARS = 3;

interface Ctx {
  p: FlagParams;
  ts: number[];
  open: number[];
  high: number[];
  low: number[];
  close: number[];
  volume: number[];
  ema: (number | null)[];
  atr: (number | null)[];
}

interface ActiveFlag {
  box: FlagBox;
  trigger: number;
  wStart: number; // first index of the flat window the flag opened on: its span is measured from here
  endIdx: number; // last index the flag held
  minClose: number;
  maxClose: number;
  ready: boolean;
  slammed: boolean; // this flag's episode already had its SLAM (at most one per episode)
}

interface Position {
  entryIdx: number;
  entryPrice: number;
  entryTs: number;
  initialStop: number;
  stop: number;
  adds: number;
  slammed: boolean; // this position's episode already had its SLAM (at most one per episode)
}

// All of one day's mutable state in one object (not loose `let`s): the rules
// below are closures that assign it, and TypeScript's control-flow narrowing
// does not follow assignments made through a closure call, so a bare
// `let flag: X | null = null` read back in the outer scope would be typed
// `null` forever (the same trap shortFade.ts documents at `stateIn`).
interface DayState {
  impulse: boolean;
  baseLow: number;
  baseLowIdx: number;
  top: number;
  topIdx: number;
  lowSinceTop: number; // lowest low of the closed bars AFTER the top bar (Infinity until one exists — the top bar is the thrust, not part of its own flag)
  flag: ActiveFlag | null;
  pos: Position | null;
  slamIdx: number; // the day's latest SLAM bar (-1: none yet)
  slamPct: number; // its close vs its EMA, % (negative)
  hodFlagIdx: number; // FLAT bar of the day's latest flag whose top was a new high of the day (-1: none)
}

interface DayOutcome {
  events: FlagEvent[];
  boxes: FlagBox[];
  st: DayState;
}

function processDay(c: Ctx, s: number, e: number, forming: boolean): DayOutcome {
  const { p, ts, open, high, low, close, volume, ema, atr } = c;
  const events: FlagEvent[] = [];
  const boxes: FlagBox[] = [];
  const st: DayState = { impulse: false, baseLow: 0, baseLowIdx: -1, top: 0, topIdx: -1, lowSinceTop: 0, flag: null, pos: null, slamIdx: -1, slamPct: 0, hodFlagIdx: -1 };
  const extFloor = 1 - p.emaBreakPct / 100; // a close under ema * extFloor ends the setup
  const minFlat = Math.max(1, Math.floor(p.minFlatBars));

  function emit(i: number, kind: FlagEventKind, price: number, label: string, note: string, marker?: 'buy' | 'sell', extra?: { emaDistPct?: number; side?: 'short' }): void {
    const ev: FlagEvent = marker ? { ts: ts[i]!, kind, price, label, note, marker, side: 'long' } : { ts: ts[i]!, kind, price, label, note, side: 'long' };
    if (extra?.side) ev.side = extra.side;
    if (extra?.emaDistPct != null) ev.emaDistPct = extra.emaDistPct;
    events.push(ev);
  }

  // ── the position ──────────────────────────────────────────────────────

  function exitPos(i: number, by: 'close' | 'low'): void {
    const pos = st.pos!;
    const price = by === 'close' ? close[i]! : pos.stop;
    const pnl = (price / pos.entryPrice - 1) * 100;
    const trailed = pos.stop > pos.initialStop;
    const what = trailed ? `${p.emaPeriod} EMA trail ${fmtPx(pos.stop)}` : `stop ${fmtPx(pos.stop)}`;
    const verb = by === 'close' ? 'closed below the' : 'traded down to the';
    emit(i, 'exit', price, 'EXIT', `${verb} ${what} — exit at ${fmtPx(price)}, ${spct1(pnl)} from ${fmtPx(pos.entryPrice)}`, 'sell');
    st.pos = null;
  }

  // ── the flag ──────────────────────────────────────────────────────────

  // The break of the active flag's trigger — judged on `high`, so it is also
  // run on the forming bar. Uses state exactly as it stood after the last
  // CLOSED bar (the flag, `ready`, `lowSinceTop`, the EMA of bar i-1).
  function breakFlag(i: number, f: ActiveFlag): void {
    const emaLast = ema[i - 1] ?? ema[i]!;
    const ready = f.ready;
    f.box.endTs = ts[i]!;
    st.flag = null;
    if (ready || !p.entryNeedsReady) {
      f.box.outcome = 'broke';
      const pos = st.pos;
      if (!pos) {
        const stop = Math.max(st.lowSinceTop, emaLast) * (1 - p.stopBufferPct / 100);
        const risk = ((f.trigger - stop) / f.trigger) * 100;
        st.pos = { entryIdx: i, entryPrice: f.trigger, entryTs: ts[i]!, initialStop: stop, stop, adds: 0, slammed: false };
        emit(
          i,
          'entry',
          f.trigger,
          'LONG (FLAG)',
          `broke the flag high ${fmtPx(f.box.high)} ` + (ready ? `after the ${p.emaPeriod} EMA caught up` : `before the ${p.emaPeriod} EMA caught up`) + ` — stop ${fmtPx(stop)} (${pct1(risk)} risk)`,
          'buy',
        );
      } else if (pos.adds < p.maxAdds) {
        pos.adds++;
        emit(i, 'add', f.trigger, 'ADD (FLAG)', `broke the next flag high ${fmtPx(f.box.high)} — add ${pos.adds}/${p.maxAdds}, stop stays ${fmtPx(pos.stop)}`, 'buy');
      }
      // else: already at the add cap — the flag is spent and nothing prints.
    } else {
      f.box.outcome = 'early';
      emit(i, 'earlyBreak', f.trigger, 'EARLY BREAK', `broke out before the ${p.emaPeriod} EMA caught up — chase risk`);
    }
  }

  // The rules that run on the forming bar as well as the closed one (see the
  // module comment): the flag break by high, and — only when the stop is a
  // hard order — the stop by low.
  function intrabar(i: number): void {
    const f = st.flag;
    if (st.impulse && f && high[i]! > f.trigger) breakFlag(i, f);
    if (!p.stopByClose && st.pos && low[i]! <= st.pos.stop) exitPos(i, 'low');
  }

  // The current top is older than a flag under it may be: counted in bars from the top bar (inclusive) to bar i, more
  // than `maxFlagBars` (0 = no limit). The ONE definition of FLAG EXPIRY in the module comment.
  function topExpired(i: number): boolean {
    return p.maxFlagBars > 0 && i - st.topIdx + 1 > p.maxFlagBars;
  }

  function openFlag(i: number, emaI: number): void {
    // A top already older than a flag may be opens nothing: the flag would be born stale, and a dead flag gets no FLAT
    // alert. This is also what makes an expiry final: the same count that expires a flag refuses the next one under the
    // same top, until a higher high moves the top.
    if (topExpired(i)) return;
    const wStart = i - minFlat + 1;
    if (wStart < st.topIdx) return;
    const move = st.top - st.baseLow;
    if (!(move > 0)) return;
    // The window may start ON the top bar (it counts toward `minFlatBars`, and
    // its close must still hold the EMA floor and sit inside the opening span),
    // but the top bar is the thrust the flag hangs from, not part of the flag:
    // its wide low and its close would otherwise be measured as the flag's own
    // (box 0.64-0.86 for a flag whose lows are 0.75; a retrace gate rejected on
    // the thrust bar's wick; a fade a bar before the break). The flag's retrace
    // low and its running close range start on the bar AFTER the top — `m` —
    // except with `minFlatBars` 1, where the window is the one bar and may be
    // the top bar itself.
    const m = minFlat > 1 ? Math.max(wStart, st.topIdx + 1) : wStart;
    let lo = Infinity;
    let hi = -Infinity;
    let minLow = Infinity;
    let fLo = Infinity;
    let fHi = -Infinity;
    for (let k = wStart; k <= i; k++) {
      const ek = ema[k];
      if (ek == null || !(close[k]! >= ek * extFloor)) return;
      if (close[k]! < lo) lo = close[k]!;
      if (close[k]! > hi) hi = close[k]!;
      if (k >= m) {
        if (low[k]! < minLow) minLow = low[k]!;
        if (close[k]! < fLo) fLo = close[k]!;
        if (close[k]! > fHi) fHi = close[k]!;
      }
    }
    const span = hi - lo;
    if (span > (p.maxFlatOfMovePct / 100) * move || span > (p.maxFlatPct / 100) * close[i]!) return;
    if (minLow < st.baseLow + (1 - p.maxRetracePct / 100) * move) return;
    // `lowSinceTop` starts empty on a top bar (see the top handlers), so a flag
    // opened on the top bar itself (minFlatBars 1) takes its low from that bar.
    if (!Number.isFinite(st.lowSinceTop)) st.lowSinceTop = minLow;

    const trigger = st.top * (1 + p.breakBufferPct / 100);
    const box: FlagBox = { startTs: ts[st.topIdx]!, endTs: ts[i]!, high: st.top, low: st.lowSinceTop, trigger, readyTs: null, outcome: 'active' };
    const f: ActiveFlag = { box, trigger, wStart, endIdx: i, minClose: fLo, maxClose: fHi, ready: false, slammed: false };
    st.flag = f;
    boxes.push(box);
    // HOD FLAG: the run-up's top is above every earlier high of this ET day.
    if (st.topIdx > s) {
      let hod = -Infinity;
      for (let k = s; k < st.topIdx; k++) if (high[k]! > hod) hod = high[k]!;
      if (st.top > hod) st.hodFlagIdx = i;
    }
    const dist = (close[i]! / emaI - 1) * 100;
    emit(
      i,
      'flat',
      trigger,
      'FLAT',
      `sideways for ${i - st.topIdx + 1} bars at ${fmtPx(st.lowSinceTop)}–${fmtPx(st.top)} above the rising ${p.emaPeriod} EMA (${distPhrase(dist)}) — long above ${fmtPx(trigger)}`,
      undefined,
      { emaDistPct: dist },
    );
    if (close[i]! <= emaI * (1 + p.readyDistPct / 100)) {
      f.ready = true;
      box.readyTs = ts[i]!;
    }
  }

  // Returns true when the flag ended on this bar (faded by span, or expired as 'stale').
  function extendFlag(i: number, emaI: number, f: ActiveFlag): boolean {
    if (close[i]! < f.minClose) f.minClose = close[i]!;
    if (close[i]! > f.maxClose) f.maxClose = close[i]!;
    const move = st.top - st.baseLow;
    const span = f.maxClose - f.minClose;
    if (span > (p.maxFlatOfMovePct / 100) * move || span > (p.maxFlatPct / 100) * close[i]!) {
      f.box.outcome = 'faded';
      st.flag = null;
      return true;
    }
    f.endIdx = i;
    f.box.endTs = ts[i]!;
    f.box.low = st.lowSinceTop;
    // EXPIRY: the box now spans more than `maxFlagBars` bars from the top bar (inclusive). Silent. The box keeps the
    // bar that expired it (that bar still held the span rules; the flag just ran out of time). Checked BEFORE the READY
    // test below, so an expiring flag prints no 9 EMA NEAR.
    if (topExpired(i)) {
      f.box.outcome = 'stale';
      st.flag = null;
      return true;
    }
    if (!f.ready && close[i]! <= emaI * (1 + p.readyDistPct / 100)) {
      f.ready = true;
      f.box.readyTs = ts[i]!;
      const dist = (close[i]! / emaI - 1) * 100;
      emit(
        i,
        'emaNear',
        f.trigger,
        '9 EMA NEAR',
        `the ${p.emaPeriod} EMA has caught up — ${dist >= 0 ? `${pct1(dist)} below the flag` : `${pct1(-dist)} above the close`} — long above ${fmtPx(f.trigger)}`,
        undefined,
        { emaDistPct: dist },
      );
    }
    return false;
  }

  function tryStartImpulse(i: number, emaI: number): void {
    if (i - s < MIN_PRIOR_BARS) return;
    if (!(close[i]! >= emaI * (1 + p.minExtPct / 100))) return;
    let priorHigh = -Infinity;
    for (let k = Math.max(s, i - p.breakoutLookback); k < i; k++) if (high[k]! > priorHigh) priorHigh = high[k]!;
    if (!(high[i]! > priorHigh)) return;
    let bl = low[i]!;
    let blIdx = i;
    for (let k = Math.max(s, i - p.impulseBaseBars); k < i; k++) {
      if (low[k]! < bl) {
        bl = low[k]!;
        blIdx = k;
      }
    }
    st.impulse = true;
    st.baseLow = bl;
    st.baseLowIdx = blIdx;
    st.top = high[i]!;
    st.topIdx = i;
    st.lowSinceTop = Infinity;
    const ext = (close[i]! / emaI - 1) * 100;
    emit(i, 'impulse', high[i]!, 'BREAKOUT', `+${pct1(ext)} above the ${p.emaPeriod} EMA, new ${p.breakoutLookback}-bar high ${fmtPx(high[i]!)} — wait for it to go sideways`);
  }

  // ── the SLAM (see 9 EMA SLAM in the module comment) ───────────────────

  // Rules 2-6 on CLOSED bar i, after the bar's own rules have run (so it is
  // emitted after whatever that bar's long side reported: EXIT, FLAG FAILED, or
  // EARLY BREAK). `pos` / `flag` are the position and the flag open at the end
  // of bar i-1 (rule 1, decided by the caller; either may be null, and a flag
  // can be open next to a position), and they are the only thing this marks —
  // BOTH, since the one bar slammed through both: a SLAM changes no state,
  // level or long event.
  function trySlam(i: number, emaI: number, pos: Position | null, flag: ActiveFlag | null): void {
    if (i - 1 < s) return; // rule 1 is the same ET day (an episode never exists on a day's first bar anyway)
    const emaPrev = ema[i - 1];
    if (emaPrev == null) return;
    const o = open[i]!;
    const c = close[i]!;
    // 2. from above: the previous close held the EMA, and this bar opened at/near it
    if (!(close[i - 1]! >= emaPrev)) return;
    if (!(o >= emaPrev * (1 - p.slamOpenTolPct / 100))) return;
    // 3. through: closed well under its own EMA
    if (!(c <= emaI * (1 - p.slamBelowPct / 100))) return;
    // 4. a hard candle: a big body, in ATRs of the bar before and in % of the open
    const body = o - c;
    if (!(body > 0)) return;
    const atrPrev = atr[i - 1] ?? null;
    if (p.slamBodyAtr > 0 && (atrPrev == null || !(body >= p.slamBodyAtr * atrPrev))) return;
    if (!(body / o >= p.slamMinBodyPct / 100)) return;
    // 5. closed weak: in the lower part of its range
    if (!(c - low[i]! <= p.slamCloseInRange * (high[i]! - low[i]!))) return;
    // 6. on volume: vs the mean of the up-to-10 earlier bars of the day (no test with fewer than 3)
    let volSum = 0;
    let nVol = 0;
    for (let k = i - 1; k >= s && nVol < SLAM_VOL_BARS; k--) {
      volSum += volume[k]!;
      nVol++;
    }
    const volMean = nVol >= SLAM_MIN_VOL_BARS ? volSum / nVol : null;
    if (volMean != null && !(volume[i]! >= p.slamVolX * volMean)) return;

    if (pos) pos.slammed = true;
    if (flag) flag.slammed = true;
    const dist = (c / emaI - 1) * 100;
    st.slamIdx = i;
    st.slamPct = dist;
    const bodyText = atrPrev != null && atrPrev > 0 ? `a ${(body / atrPrev).toFixed(1)} ATR body` : `a ${pct1((body / o) * 100)} body`;
    const volText = volMean != null && volMean > 0 ? ` on ${(volume[i]! / volMean).toFixed(1)}× volume` : '';
    emit(
      i,
      'slam',
      c,
      'SLAM (9 EMA)',
      `${fmtEt(ts[i]!)} slammed through the ${p.emaPeriod} EMA from above: closed ${fmtPx(c)}, ${pct1(-dist)} under the EMA ${fmtPx(emaI)} — ${bodyText}${volText}`,
      undefined,
      { emaDistPct: dist, side: 'short' },
    );
  }

  // ── the closed-bar loop ───────────────────────────────────────────────

  // One CLOSED bar through the long-side rules, in order.
  function closedBar(i: number, emaI: number): void {
    // 1. the intrabar rules — the very step the forming pass runs.
    intrabar(i);

    // 2. a position: the close-based stop, then the EMA trail.
    const pos = st.pos;
    if (pos) {
      if (p.stopByClose && close[i]! < pos.stop) exitPos(i, 'close');
      else if (p.trailEma) pos.stop = Math.max(pos.stop, emaI * (1 - p.stopBufferPct / 100));
    }

    // 3. FAIL: a close well under the EMA ends the setup (and a long is left to its own stop).
    if (close[i]! < emaI * extFloor) {
      const f = st.flag;
      if (f) {
        f.box.outcome = 'failed';
        st.flag = null;
        emit(i, 'failed', close[i]!, 'FLAG FAILED', `closed ${pct1((1 - close[i]! / emaI) * 100)} under the ${p.emaPeriod} EMA — flag failed`);
      }
      st.impulse = false;
      return;
    }

    // 4. the impulse: start one, or move its top and keep a flag honest.
    if (!st.impulse) {
      tryStartImpulse(i, emaI);
      return;
    }
    if (high[i]! > st.top) {
      // A new top. A flag still open here was poked (high above its cap but not
      // through the trigger — a real break was handled in step 1): it can no
      // longer be "under the high", so it closes quietly.
      if (st.flag) {
        st.flag.box.outcome = 'faded';
        st.flag = null;
      }
      st.top = high[i]!;
      st.topIdx = i;
      st.lowSinceTop = Infinity;
    } else if (low[i]! < st.lowSinceTop) {
      st.lowSinceTop = low[i]!;
    }
    const f = st.flag;
    if (f) extendFlag(i, emaI, f);
    else openFlag(i, emaI);
  }

  const lastClosed = forming ? e - 2 : e - 1;
  for (let i = s; i <= lastClosed; i++) {
    const emaI = ema[i];
    if (emaI == null) continue; // EMA warm-up (the first period-1 bars of the whole array)
    // SLAM rule 1: the episodes open at the END of bar i-1 (flat / ready / long), taken before this bar's rules can
    // close them — the position and the flag, each an episode of its own (a later flag can open next to a position
    // that survived its slam under edited params). A bar may slam while either has not slammed yet; one that already
    // slammed is not slammed twice.
    const slamPos = p.slam ? st.pos : null;
    const slamFlag = p.slam ? st.flag : null;
    closedBar(i, emaI);
    if ((slamPos && !slamPos.slammed) || (slamFlag && !slamFlag.slammed)) trySlam(i, emaI, slamPos, slamFlag);
  }

  // ── the forming bar ───────────────────────────────────────────────────
  if (forming) intrabar(e - 1);

  return { events, boxes, st };
}

// ─── Result assembly (last day) ──────────────────────────────────────────

function emptyLevels(): FlagLevels {
  return { ema: null, atr: null, baseLow: null, top: null, topTs: null, flagHigh: null, flagLow: null, trigger: null, stop: null, entryPrice: null, entryTs: null, adds: 0 };
}

function emptyEvidence(): FlagEvidence {
  return {
    extPct: null,
    flatBars: null,
    flatSpanPctOfMove: null,
    flatSpanPct: null,
    emaDistPct: null,
    retracePct: null,
    volDry: null,
    riskPct: null,
    openPnlPct: null,
    lastSlamTs: null,
    lastSlamClose: null,
    lastSlamPct: null,
    lastHodFlagTs: null,
  };
}

function idleText(p: FlagParams): { headline: string; detail: string[] } {
  return { headline: 'No breakout yet', detail: [`Waiting for a close ≥${p.minExtPct}% above the ${p.emaPeriod} EMA on a new ${p.breakoutLookback}-bar high`] };
}

function mean(vals: number[], from: number, to: number): number | null {
  if (to < from) return null;
  let sum = 0;
  for (let k = from; k <= to; k++) sum += vals[k]!;
  return sum / (to - from + 1);
}

function summarise(c: Ctx, s: number, e: number, forming: boolean, st: DayState): Omit<FlagResult, 'events' | 'boxes' | 'dayKey'> {
  const { p, ts, close, volume, ema, atr } = c;
  const lastIdx = e - 1;
  const lastClosed = forming ? lastIdx - 1 : lastIdx;
  const emaNow = ema[lastIdx] ?? null;
  const emaLast = lastClosed >= s ? (ema[lastClosed] ?? null) : null;
  const f = st.flag;
  const pos = st.pos;
  const move = st.impulse ? st.top - st.baseLow : 0;

  const levels = emptyLevels();
  levels.ema = emaNow;
  levels.atr = lastClosed >= 0 ? (atr[lastClosed] ?? null) : null;
  if (st.impulse) {
    levels.baseLow = st.baseLow;
    levels.top = st.top;
    levels.topTs = ts[st.topIdx]!;
  }
  let prospectiveStop: number | null = null;
  if (f) {
    levels.flagHigh = f.box.high;
    levels.flagLow = st.lowSinceTop;
    levels.trigger = f.trigger;
    if (emaLast != null) prospectiveStop = Math.max(st.lowSinceTop, emaLast) * (1 - p.stopBufferPct / 100);
  }
  if (pos) {
    levels.stop = pos.stop;
    levels.entryPrice = pos.entryPrice;
    levels.entryTs = pos.entryTs;
    levels.adds = pos.adds;
  } else {
    levels.stop = prospectiveStop;
  }

  const ev = emptyEvidence();
  if (st.impulse) {
    const emaTop = ema[st.topIdx];
    ev.extPct = emaTop != null ? (st.top / emaTop - 1) * 100 : null;
    if (move > 0 && Number.isFinite(st.lowSinceTop)) ev.retracePct = ((st.top - st.lowSinceTop) / move) * 100;
  }
  if (f && lastClosed >= s) {
    ev.flatBars = f.endIdx - st.topIdx + 1;
    const span = f.maxClose - f.minClose;
    if (move > 0) ev.flatSpanPctOfMove = (span / move) * 100;
    ev.flatSpanPct = (span / close[f.endIdx]!) * 100;
    const flagVol = mean(volume, st.topIdx + 1, f.endIdx);
    const legVol = mean(volume, st.baseLowIdx, st.topIdx);
    ev.volDry = flagVol != null && legVol != null && legVol > 0 ? flagVol / legVol : null;
  }
  const closeNow = close[lastIdx]!;
  if (emaNow != null && emaNow > 0) ev.emaDistPct = (closeNow / emaNow - 1) * 100;
  if (pos) {
    ev.openPnlPct = (closeNow / pos.entryPrice - 1) * 100;
    ev.riskPct = ((pos.entryPrice - pos.stop) / pos.entryPrice) * 100;
  } else if (f && prospectiveStop != null) {
    ev.riskPct = ((f.trigger - prospectiveStop) / f.trigger) * 100;
  }
  if (st.slamIdx >= 0) {
    ev.lastSlamTs = ts[st.slamIdx]!;
    ev.lastSlamClose = close[st.slamIdx]!;
    ev.lastSlamPct = st.slamPct;
  }
  if (st.hodFlagIdx >= 0) ev.lastHodFlagTs = ts[st.hodFlagIdx]!;

  const state: FlagState = pos ? 'long' : f ? (f.ready ? 'ready' : 'flat') : st.impulse ? 'impulse' : 'idle';
  const topExpired = state === 'impulse' && p.maxFlagBars > 0 && lastClosed - st.topIdx + 1 > p.maxFlagBars;
  const P = p.emaPeriod;
  const dist = ev.emaDistPct ?? 0;
  let headline: string;
  let detail: string[];
  if (state === 'long' && pos) {
    headline = `LONG since ${fmtEt(pos.entryTs)} at ${fmtPx(pos.entryPrice)} (${spct1(ev.openPnlPct ?? 0)} open)`;
    detail = [
      `Stop ${fmtPx(pos.stop)}${p.trailEma ? ` (${P} EMA trail)` : ''}${pos.adds ? ` · adds ${pos.adds}/${p.maxAdds}` : ''}`,
      p.stopByClose ? 'Exit on a close below the stop' : 'Exit when price trades down to the stop',
    ];
  } else if (state === 'ready' && f) {
    headline = `READY — ${P} EMA caught up, long above ${fmtPx(f.trigger)}`;
    detail = [
      `Flag ${fmtPx(st.lowSinceTop)}–${fmtPx(f.box.high)} (${ev.flatBars ?? 0} bars), ` + (dist >= 0 ? `EMA ${pct1(dist)} below` : `EMA ${pct1(-dist)} above (price under it)`),
      prospectiveStop != null ? `Stop would be ${fmtPx(prospectiveStop)} (${pct1(ev.riskPct ?? 0)} risk)` : 'Stop would sit under the EMA',
      `A close >${p.emaBreakPct}% under the EMA fails it`,
    ];
  } else if (state === 'flat' && f) {
    headline = `FLAT — sideways above the ${P} EMA (${distPhrase(dist)})`;
    detail = [`Flag ${fmtPx(st.lowSinceTop)}–${fmtPx(f.box.high)} since ${fmtEt(f.box.startTs)} (${ev.flatBars ?? 0} bars)`, `READY when the EMA is within ${p.readyDistPct}%`, `Long above ${fmtPx(f.trigger)}`];
  } else if (topExpired) {
    // The top is older than a flag may be (FLAG EXPIRY: its flag expired, or none formed in time): "wait for it to go
    // sideways" would be a lie, it already has, or never did.
    headline = `RANGE — no flag: the top is over ${p.maxFlagBars} bars old`;
    detail = [
      `Top ${fmtPx(st.top)} at ${fmtEt(ts[st.topIdx]!)}, ${spct1(ev.extPct ?? 0)} above the ${P} EMA`,
      `A new flag needs a new high above ${fmtPx(st.top)}`,
    ];
  } else if (state === 'impulse') {
    headline = 'BREAKOUT — wait for it to go sideways';
    detail = [
      `Top ${fmtPx(st.top)} at ${fmtEt(ts[st.topIdx]!)}, ${spct1(ev.extPct ?? 0)} above the ${P} EMA`,
      `A flag needs ${p.minFlatBars} bars holding within ${p.maxFlatOfMovePct}% of the move above the EMA`,
    ];
  } else {
    const t = idleText(p);
    headline = t.headline;
    detail = t.detail;
  }
  return { state, levels, evidence: ev, topExpired, headline, detail };
}

// ─── Top-level entry point ───────────────────────────────────────────────

export function evaluateFlag(input: FlagInput): FlagResult {
  const p: FlagParams = { ...DEFAULT_FLAG_PARAMS, ...input.params };
  const candles = input.candles;
  const n = candles.length;
  if (n === 0) {
    const t = idleText(p);
    return { state: 'idle', dayKey: '', levels: emptyLevels(), evidence: emptyEvidence(), events: [], boxes: [], topExpired: false, headline: t.headline, detail: t.detail };
  }

  const ts = new Array<number>(n);
  const open = new Array<number>(n);
  const high = new Array<number>(n);
  const low = new Array<number>(n);
  const close = new Array<number>(n);
  const volume = new Array<number>(n);
  for (let i = 0; i < n; i++) {
    const c = candles[i]!;
    ts[i] = c.ts;
    open[i] = c.open;
    high[i] = c.high;
    low[i] = c.low;
    close[i] = c.close;
    volume[i] = c.volume;
  }
  const ctx: Ctx = { p, ts, open, high, low, close, volume, ema: emaSeries(close, p.emaPeriod), atr: atrSeries(candles, p.atrPeriod) };

  // Day ranges (candles are ts-ascending, so a day is one contiguous run).
  // etDayKey is an Intl format() per call — the ET calendar date can only
  // change on a UTC HOUR boundary (America/New_York's offsets are whole hours
  // and its DST switches happen on the hour), so one lookup per UTC hour gives
  // the same key for every candle in it (the same trick as shortFade.evaluate).
  const dayStarts: number[] = [];
  const dayKeys: string[] = [];
  let lastHour = Number.NaN;
  let key = '';
  let prevKey = '';
  for (let i = 0; i < n; i++) {
    const hour = Math.floor(ts[i]! / 3_600_000);
    if (hour !== lastHour) {
      lastHour = hour;
      key = etDayKey(ts[i]!);
    }
    if (i === 0 || key !== prevKey) {
      dayStarts.push(i);
      dayKeys.push(key);
    }
    prevKey = key;
  }

  const events: FlagEvent[] = [];
  const boxes: FlagBox[] = [];
  let last: Omit<FlagResult, 'events' | 'boxes' | 'dayKey'> | null = null;
  for (let d = 0; d < dayStarts.length; d++) {
    const s = dayStarts[d]!;
    const e = d + 1 < dayStarts.length ? dayStarts[d + 1]! : n;
    const isLastDay = d === dayStarts.length - 1;
    const forming = isLastDay && input.nowMs < ts[n - 1]! + input.tfMs;
    const out = processDay(ctx, s, e, forming);
    for (const ev of out.events) events.push(ev);
    for (const b of out.boxes) boxes.push(b);
    if (isLastDay) last = summarise(ctx, s, e, forming, out.st);
  }
  return { ...last!, dayKey: dayKeys[dayKeys.length - 1]!, events, boxes };
}
