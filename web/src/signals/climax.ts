// Pure VOLUME CLIMAX -> BOTTOM CONFIRMED rule (1-minute only). No DOM, no
// store, no `Date.now()` (the caller passes `nowMs`); like emaFlag.ts it
// imports the vendored chart engine TYPE-ONLY, so its vitest suite runs in
// plain node.
//
// The trader (2026-10-07): "I want a fourth signal. Climatic Volume on a bottom
// or sell off move." Two moments, two alerts (both are SCAN alerts; this module
// only decides, and draws the Signal window's markers):
//   1. VOLUME CLIMAX: a closed 1-minute candle that makes a fresh low well under
//      the recent high, on the biggest volume of the last 10 bars and many times
//      their mean, with a lower wick (buyers showed up), closing under the 9 EMA
//      ("SYM: volume climax"). The marker sits at the LOW: the level that must hold.
//   2. BOTTOM CONFIRMED: within the next 5 closed traded bars of the same ET day,
//      a close above the climax candle's midpoint BEFORE any bar undercuts its
//      low ("SYM: bottom confirmed"), at that bar's close.
//
// It is an ALERT, a "look now" prompt, NOT a system. Calibration (60 days of
// cached bars, scratchpad climax-spec.md): the volume test doubles the chance
// that a sell-off low still holds 30 minutes later (49 % vs 24 % for the same
// sell-off on any volume), but neither entry is profitable after a 0.5 %
// round-trip cost (the confirmation close's +15-minute median is −0.73 % net).
//
// ONE rule, three users, kept bit-identical by golden vectors
// (fixtures/golden/climax-golden.json, regenerate with UPDATE_GOLDEN=1, see
// climax.golden.test.ts): the scanner engine (Go internal/engine, both alerts,
// market-wide, through internal/climax.Classify and climax.Latch), the Go
// climax.Evaluate, and the Signal window's markers (this file, evaluateClimax).
//
// THE CANDLE (classifyClimax; every number a param):
//   the prior bars are EXACTLY the last `lookback` (10) TRADED bars of the SAME
//   ET day — yesterday's never count (unlike the dildo); with fewer, no climax
//   high > low, and the priors' highest high > 0
//   fresh low: low <= the priors' lowest low (inclusive)
//   dropPct = (priorsHigh − low)/priorsHigh·100             >= minDropPct (6)
//   volX = volume / mean prior volume                       >= minVolX (8)  (mean 0 -> +Inf, passes)
//   requireMaxVol: volume >= the biggest single prior bar's (inclusive)
//   wickPct = (min(open, close) − low)/(high − low)·100     >= minWickPct (25)
//   close >= minPrice (0.50); close·volume >= minDollarVol (50 k)
//   requireBelowEma: close < EMA9 of this bar (strict; EMA unknown -> no climax)
//   never in the mute [muteStartMin, muteEndMin) ET (09:30-09:34: the open's
//   worst slice in calibration); coreHoursOnly: only bars starting 10:00-15:59 ET
// Priors are built oldest -> newest (`climaxPriors`): hi = max high, lo = min
// low, sumVol += volume, maxVol = max volume — the Go engine recomputes them
// from its ring in the same order, never as running sums.
//
// THE CONFIRMATION (a latch; Go climax.Latch is the same code in the engine and
// in climax.Evaluate). Per ET day, one latch, for each CLOSED bar j:
//   a) latch watching: bars++;  low[j] < climax low -> CLIMAX FAILED (checked
//      FIRST: a bar that undercuts and closes above mid fails); else close[j] >
//      mid -> BOTTOM CONFIRMED; else after the `confirmBars`-th bar -> expired
//      (silent). Both tests strict.
//   b) bar j a climax -> VOLUME CLIMAX; arm the latch (a newer climax replaces a
//      watching one: "latest supersedes"; usually the newer fresh low already
//      failed the older one on the same bar, then re-arms — XNDU 08:51).
// A confirmation never crosses the ET day. Events come from CLOSED bars only
// (append-only). The FORMING bar only moves the Result: a watching latch whose
// forming low already undercut the climax low reads FAILED (final — a low only
// falls); a forming close above mid changes nothing (a close is not final).

import type { Candle } from '../vendor/blackthorn-charts';
import { etDayKey, etParts } from '../marketClock';
import { emaSeries } from './emaFlag';
import { fmtEt, fmtPx } from './shortFade';
import { fmtDvol } from './dildoPb';

// ─── Types ───────────────────────────────────────────────────────────────

export interface ClimaxParams {
  enabled: boolean; // true  master switch (chart markers; the market scan's engine gate is env SCANNER_CLIMAX)
  lookback: number; // 10    the priors are exactly the last N traded bars of the same ET day
  minDropPct: number; // 6     (priors' highest high − low)/that high·100 >= this
  minVolX: number; // 8     volume >= this × the priors' mean volume (a mean of 0 passes)
  requireMaxVol: boolean; // true  volume >= the biggest single prior bar's volume
  minWickPct: number; // 25    lower wick (min(open, close) − low)/(high − low)·100 >= this
  requireBelowEma: boolean; // true  close < the bar's own EMA9 (strict; EMA unknown -> no climax)
  emaPeriod: number; // 9
  minPrice: number; // 0.50  close >= this
  minDollarVol: number; // 50000 close × volume >= this
  muteStartMin: number; // 570   ET minute-of-day: bars starting 09:30 …
  muteEndMin: number; // 575   … up to but EXCLUDING 09:35 are never a climax (start >= end: no mute)
  coreHoursOnly: boolean; // false true: only bars starting 10:00-15:59 ET
  confirmBars: number; // 5     the confirmation looks at the next N closed traded bars of the same ET day
}

export const DEFAULT_CLIMAX_PARAMS: ClimaxParams = {
  enabled: true,
  lookback: 10,
  minDropPct: 6,
  minVolX: 8,
  requireMaxVol: true,
  minWickPct: 25,
  requireBelowEma: true,
  emaPeriod: 9,
  minPrice: 0.5,
  minDollarVol: 50_000,
  muteStartMin: 570,
  muteEndMin: 575,
  coreHoursOnly: false,
  confirmBars: 5,
};

/** The prior bars, built oldest -> newest (see the module comment). */
export interface ClimaxPriors {
  n: number;
  hi: number; // highest high (−Infinity with none)
  lo: number; // lowest low (+Infinity with none)
  sumVol: number;
  maxVol: number;
}

export interface ClimaxHit {
  dropPct: number;
  volX: number; // +Infinity when the priors' mean volume is 0
  wickPct: number;
  dollarVol: number;
  emaDistPct: number | null; // (close − ema)/ema·100; null when the EMA is unknown (Go: NaN)
  mid: number; // (high + low)/2 — the level a close must beat to confirm
}

export type ClimaxEventKind = 'climax' | 'bottom' | 'climaxFail';

export interface ClimaxEvent {
  ts: number; // the bar's start
  kind: ClimaxEventKind;
  price: number; // climax: its LOW (the level that must hold); bottom: the confirming close; climaxFail: the undercutting low
  label: string; // 'VOLUME CLIMAX' | 'BOTTOM CONFIRMED' | 'CLIMAX FAILED'
  note: string;
  marker?: 'buy'; // climax (▲ under the low) and bottom (▲ at the close); a fail is list-only
  side: 'long';
  reason?: 'low'; // climaxFail only
}

/** Latch / Result states (the scan's climaxState). */
export const CLIMAX_NONE = 0;
export const CLIMAX_WATCHING = 1;
export const CLIMAX_CONFIRMED = 2;
export const CLIMAX_FAILED = 3;
export const CLIMAX_EXPIRED = 4;

export interface ClimaxResult {
  events: ClimaxEvent[]; // every ET day in the input, oldest first
  // The LAST ET day's latest climax (null / 0 = none):
  lastClimaxTs: number | null;
  climaxLow: number | null;
  climaxMid: number | null;
  dropPct: number | null;
  volX: number | null; // may be Infinity
  wickPct: number | null;
  dollarVol: number | null;
  state: number; // 0 none, 1 watching, 2 confirmed, 3 failed (also a forming-bar undercut), 4 expired
  bars: number; // closed traded bars folded after the climax
  confTs: number | null; // the confirming bar (state 2)
  confPx: number | null; // its close (state 2)
  failTs: number | null; // the undercutting bar (state 3; the forming bar's ts for a forming undercut)
  endTs: number | null; // the verdict bar (confirm / fail / expire)
}

export interface ClimaxInput {
  candles: Candle[]; // ts ascending; may span the previous trading day + today
  tfMs: number; // the rule is 1-minute only: anything but 60 000 returns an empty result
  nowMs: number; // the last candle is FORMING iff nowMs < last.ts + tfMs
  params?: Partial<ClimaxParams>;
}

// ─── The candle ──────────────────────────────────────────────────────────

/** The priors over candles[from, to), oldest -> newest. */
export function climaxPriors(candles: readonly Candle[], from: number, to: number): ClimaxPriors {
  let hi = -Infinity;
  let lo = Infinity;
  let sumVol = 0;
  let maxVol = 0;
  for (let k = from; k < to; k++) {
    const c = candles[k]!;
    if (c.high > hi) hi = c.high;
    if (c.low < lo) lo = c.low;
    sumVol += c.volume;
    if (c.volume > maxVol) maxVol = c.volume;
  }
  return { n: Math.max(0, to - from), hi, lo, sumVol, maxVol };
}

/**
 * Is `bar` a volume climax? `ema` is the bar's own EMA (null while unknown),
 * `etMin` its START's ET minute-of-day. Pure; Go's climax.Classify is the same
 * expressions in the same order (the golden vectors pin the bits).
 */
export function classifyClimax(bar: Candle, pr: ClimaxPriors, ema: number | null, etMin: number, p: ClimaxParams): ClimaxHit | null {
  const o = bar.open;
  const h = bar.high;
  const l = bar.low;
  const c = bar.close;
  const v = bar.volume;
  if (!p.enabled || !(pr.n >= p.lookback) || !(h > l) || !(pr.hi > 0)) return null;
  if (!(l <= pr.lo)) return null;
  const dropPct = ((pr.hi - l) / pr.hi) * 100;
  if (!(dropPct >= p.minDropPct)) return null;
  const meanVol = pr.sumVol / pr.n;
  const volX = meanVol > 0 ? v / meanVol : Infinity;
  if (!(volX >= p.minVolX)) return null;
  if (p.requireMaxVol && !(v >= pr.maxVol)) return null;
  const wickPct = ((Math.min(o, c) - l) / (h - l)) * 100;
  if (!(wickPct >= p.minWickPct)) return null;
  if (!(c >= p.minPrice)) return null;
  const dollarVol = c * v;
  if (!(dollarVol >= p.minDollarVol)) return null;
  const emaKnown = ema != null && Number.isFinite(ema);
  if (p.requireBelowEma && !(emaKnown && c < (ema as number))) return null;
  if (p.muteStartMin < p.muteEndMin && etMin >= p.muteStartMin && etMin < p.muteEndMin) return null;
  if (p.coreHoursOnly && !(etMin >= 600 && etMin < 960)) return null;
  return {
    dropPct,
    volX,
    wickPct,
    dollarVol,
    emaDistPct: emaKnown ? ((c - (ema as number)) / (ema as number)) * 100 : null,
    mid: (h + l) / 2,
  };
}

// ─── The latch ───────────────────────────────────────────────────────────

/** climax.Latch in Go, field for field (ts in ms, the climax bar's START). */
export interface ClimaxLatch {
  barTs: number;
  low: number;
  hit: ClimaxHit;
  bars: number;
  state: number;
  endTs: number; // 0 while watching
  confPx: number;
}

export function armLatch(barTs: number, low: number, hit: ClimaxHit): ClimaxLatch {
  return { barTs, low, hit, bars: 0, state: CLIMAX_WATCHING, endTs: 0, confPx: 0 };
}

/** One CLOSED traded bar after the climax, the same ET day. Only a watching latch moves. */
export function stepLatch(x: ClimaxLatch, barTs: number, l: number, c: number, p: ClimaxParams): number {
  if (x.state !== CLIMAX_WATCHING) return x.state;
  x.bars++;
  if (l < x.low) {
    x.state = CLIMAX_FAILED;
    x.endTs = barTs;
  } else if (c > x.hit.mid) {
    x.state = CLIMAX_CONFIRMED;
    x.endTs = barTs;
    x.confPx = c;
  } else if (x.bars >= p.confirmBars) {
    x.state = CLIMAX_EXPIRED;
    x.endTs = barTs;
  }
  return x.state;
}

// ─── Evaluate ────────────────────────────────────────────────────────────

function emptyResult(): ClimaxResult {
  return {
    events: [],
    lastClimaxTs: null,
    climaxLow: null,
    climaxMid: null,
    dropPct: null,
    volX: null,
    wickPct: null,
    dollarVol: null,
    state: CLIMAX_NONE,
    bars: 0,
    confTs: null,
    confPx: null,
    failTs: null,
    endTs: null,
  };
}

const plural = (n: number, w: string): string => `${n} ${w}${n === 1 ? '' : 's'}`;

export function evaluateClimax(input: ClimaxInput): ClimaxResult {
  const p: ClimaxParams = { ...DEFAULT_CLIMAX_PARAMS, ...input.params };
  const res = emptyResult();
  const candles = input.candles;
  const n = candles.length;
  if (input.tfMs !== 60_000 || !p.enabled || n === 0) return res;

  const closes = new Array<number>(n);
  for (let i = 0; i < n; i++) closes[i] = candles[i]!.close;
  const ema = emaSeries(closes, Math.max(1, Math.floor(p.emaPeriod)));
  const forming = input.nowMs < candles[n - 1]!.ts + input.tfMs;
  const lastClosed = forming ? n - 2 : n - 1;
  const lookback = Math.max(0, Math.floor(p.lookback));
  const events = res.events;

  // Day starts, and every bar's ET minute-of-day. Both can only change their
  // offset on a UTC HOUR boundary (America/New_York's offsets are whole hours,
  // its DST switches happen on the hour), so one Intl lookup per UTC hour gives
  // the ET hour and the day key of every bar in it (the emaFlag.ts trick).
  const dayStarts: number[] = [];
  const etMin = new Array<number>(n);
  let lastHour = Number.NaN;
  let hourEt = 0;
  let key = '';
  let prevKey = '';
  for (let i = 0; i < n; i++) {
    const ts = candles[i]!.ts;
    const hour = Math.floor(ts / 3_600_000);
    if (hour !== lastHour) {
      lastHour = hour;
      key = etDayKey(ts);
      hourEt = etParts(hour * 3_600_000).hh;
    }
    etMin[i] = hourEt * 60 + Math.floor((ts - hour * 3_600_000) / 60_000);
    if (i === 0 || key !== prevKey) dayStarts.push(i);
    prevKey = key;
  }

  let latch: ClimaxLatch | null = null;
  let formingFail = false;
  for (let d = 0; d < dayStarts.length; d++) {
    const s = dayStarts[d]!;
    const e = d + 1 < dayStarts.length ? dayStarts[d + 1]! : n;
    latch = null; // one latch per ET day: a confirmation never crosses it
    const end = Math.min(e - 1, lastClosed);
    for (let j = s; j <= end; j++) {
      const c = candles[j]!;
      // a) the watching latch
      if (latch && latch.state === CLIMAX_WATCHING) {
        const st = stepLatch(latch, c.ts, c.low, c.close, p);
        if (st === CLIMAX_CONFIRMED) {
          const risk = ((c.close - latch.low) / c.close) * 100;
          events.push({
            ts: c.ts,
            kind: 'bottom',
            price: c.close,
            label: 'BOTTOM CONFIRMED',
            note: `${fmtEt(c.ts)} bottom confirmed: closed ${fmtPx(c.close)} above the climax midpoint ${fmtPx(latch.hit.mid)}, ${plural(latch.bars, 'bar')} after the ${fmtEt(latch.barTs)} climax; the climax low ${fmtPx(latch.low)} is ${risk.toFixed(1)}% under`,
            marker: 'buy',
            side: 'long',
          });
        } else if (st === CLIMAX_FAILED) {
          events.push({
            ts: c.ts,
            kind: 'climaxFail',
            price: c.low,
            label: 'CLIMAX FAILED',
            note: `${fmtEt(c.ts)} climax failed: low ${fmtPx(c.low)} undercut the ${fmtEt(latch.barTs)} climax low ${fmtPx(latch.low)}`,
            side: 'long',
            reason: 'low',
          });
        }
      }
      // b) is bar j a climax? (exactly `lookback` priors of this day)
      if (j - s >= lookback) {
        const hit = classifyClimax(c, climaxPriors(candles, j - lookback, j), ema[j] ?? null, etMin[j]!, p);
        if (hit) {
          latch = armLatch(c.ts, c.low, hit); // latest supersedes
          const volText = Number.isFinite(hit.volX) ? hit.volX.toFixed(1) : '∞';
          events.push({
            ts: c.ts,
            kind: 'climax',
            price: c.low,
            label: 'VOLUME CLIMAX',
            note:
              `${fmtEt(c.ts)} volume climax: low ${fmtPx(c.low)} is ${hit.dropPct.toFixed(1)}% under the ${lookback}-bar high, ${volText}x volume` +
              `${p.requireMaxVol ? ` (biggest of ${lookback})` : ''}, ${hit.wickPct.toFixed(0)}% lower wick, ${fmtDvol(hit.dollarVol)}; ` +
              `bottom confirms on a close above ${fmtPx(hit.mid)} within ${plural(p.confirmBars, 'bar')}`,
            marker: 'buy',
            side: 'long',
          });
        }
      }
    }
    // the forming bar (the last day's last bar): a watching latch it undercuts has FAILED (final)
    if (d === dayStarts.length - 1 && forming && latch && latch.state === CLIMAX_WATCHING) {
      const f = candles[n - 1]!;
      if (f.low < latch.low) formingFail = true;
    }
  }

  // ── the Result: the last ET day's latch ──
  const x = latch as ClimaxLatch | null;
  if (x) {
    res.lastClimaxTs = x.barTs;
    res.climaxLow = x.low;
    res.climaxMid = x.hit.mid;
    res.dropPct = x.hit.dropPct;
    res.volX = x.hit.volX;
    res.wickPct = x.hit.wickPct;
    res.dollarVol = x.hit.dollarVol;
    res.bars = x.bars;
    res.state = x.state;
    if (x.state === CLIMAX_CONFIRMED) {
      res.confTs = x.endTs;
      res.confPx = x.confPx;
    }
    if (x.state === CLIMAX_FAILED) res.failTs = x.endTs;
    if (x.state !== CLIMAX_WATCHING) res.endTs = x.endTs;
    if (formingFail) {
      const fts = candles[n - 1]!.ts;
      res.state = CLIMAX_FAILED;
      res.failTs = fts;
      res.endTs = fts;
    }
  }
  return res;
}
