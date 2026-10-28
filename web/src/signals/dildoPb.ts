// Pure GREEN DILDO -> 9 EMA PULLBACK rule (1-minute only). No DOM, no store, no
// `Date.now()` (the caller passes `nowMs`); like emaFlag.ts it imports the
// vendored chart engine TYPE-ONLY, so its vitest suite runs in plain node.
//
// The trader (2026-10-07, with a 1-minute chart of a ~$0.50 stock):
//
//   "a lot of times when there is a green dildo candle i can enter on the 9EMA
//    for a scalp. Currently, i have to monitor stocks. Can we have a signal
//    scanner for the green dildo candles on the 1 minute as well?"
//
// Two moments, two alerts (both are SCAN alerts — this module only decides):
//   1. GREEN DILDO: a closed 1-minute candle that is a big green body on big
//      volume, closing near its high ("SYM: green dildo").
//   2. 9 EMA PULLBACK: price then comes back to the 1-minute 9 EMA while the
//      move still holds, and that touch bar closes back on/over the EMA
//      ("SYM: nine E M A pullback") — the scalp entry, at that bar's close.
//
// It is an ALERT, a "look now" prompt, not a system: on 60 days of cached bars
// no setting of this rule beat a 0.5 % round-trip cost with any significance
// (scratchpad dildo-spec.md, Calibration). The defaults below are the least-bad
// family of that calibration, not a fitted optimum.
//
// ONE module, three users, kept bit-identical by golden vectors
// (fixtures/golden/dildopb-golden.json, regenerate with UPDATE_GOLDEN=1, see
// dildoPb.golden.test.ts): the scanner engine's market-wide alert 1 (Go
// internal/engine, via internal/dildopb.Classify), flagscan's alert 2 (Go
// dildopb.Evaluate over the chart's REST bars), and the Signal window's chart
// markers (this file, evaluateDildo).
//
// THE CANDLE (classifyDildo; every number a param):
//   green: close > open, open > 0, high > low
//   bodyPct = (close − open)/open·100            >= minBodyPct (8)
//   close >= minPrice (0.20); close·volume >= minDollarVol (100 k)
//   the PRIOR bars are the up to `priorBars` (10) TRADED bars before it — empty
//   clock minutes do not exist as bars, and the priors may reach back into the
//   previous evening's post-market, exactly as on the chart — and there must be
//   at least `minPriorBars` (3) of them
//   bodyX = body / mean |close − open| of the priors   >= minBodyX (4)   (mean 0 -> +Inf, passes)
//   volX  = volume / mean volume of the priors          >= minVolX (5)    (mean 0 -> +Inf, passes)
//   topPct = (high − close)/(high − low)·100            <= maxTopPct (20) (closes in the top 20 %)
// The prior sums are always built oldest -> newest (`dildoPriors`), never as a
// running add/subtract window: a running sum drifts by ulps exactly at the 4x /
// 5x boundaries, and the Go engine recomputes its sums the same way.
//
// THE PULLBACK (evaluateDildo; the calibration harness's close-confirmed mode,
// 'mid' floor — scratchpad dildo/cal pullback(), Mode 'C' — plus "latest
// supersedes"). One pending ANCHOR at a time, the latest dildo. For each CLOSED
// bar j, in this order:
//   a) anchor d pending and ts[j] <= ts[d] + pbWindowMin:
//        e = ema[j−1] (no EMA yet -> the pullback checks skip this bar; the
//        bar may still be a dildo itself, step b), level = e·(1 + touchPct/100)
//        low[j] <= level (a TOUCH bar):
//          low[j] <= floor                       -> PB CANCELLED 'floor'
//          close[j] < ema[j]·(1 − holdPct/100)    -> PB CANCELLED 'ema'
//          else                                   -> 9 EMA PULLBACK at close[j]  (alert 2)
//          the anchor ends either way
//        else low[j] <= floor                     -> PB CANCELLED 'faded' (it died before reaching the EMA)
//      anchor pending and ts[j] beyond the window: it EXPIRES, silently.
//   b) bar j passes classifyDildo -> GREEN DILDO at close[j]; j becomes the
//      anchor, replacing any still pending ("latest supersedes").
// floor = (open + close)/2 of the dildo bar ('mid', the default) or its open.
//
// Events come from CLOSED bars only, so the event list is append-only by
// construction. The FORMING bar only moves the Result's pbState: with an anchor
// pending inside its window, low <= floor -> 4 (final: the closed verdict can
// only be a cancel), else low <= ema[f−1]·(1 + touchPct/100) -> 2 TOUCHING.
// Both tests are monotone while the bar prints (the thresholds are fixed, a low
// only falls), so TOUCHING never flickers back to watching inside a bar.
//
// The EMA is the chart's own (emaSeries from emaFlag.ts: SMA seed, then
// k = 2/(period+1), continuous over the previous day + today).

import type { Candle } from '../vendor/blackthorn-charts';
import { etDayKey } from '../marketClock';
import { emaSeries } from './emaFlag';
import { fmtEt, fmtPx } from './shortFade';

// ─── Types ───────────────────────────────────────────────────────────────

export interface DildoParams {
  enabled: boolean; // true  master switch (chart markers; the market scan's engine gate is env SCANNER_DILDO)
  minBodyPct: number; // 8     bodyPct = (close − open)/open·100 >= this
  minBodyX: number; // 4     body >= this × the mean |close − open| of the prior bars (a mean of 0 passes)
  maxTopPct: number; // 20    (high − close)/(high − low)·100 <= this: it closes in the top 20 % of its range
  minVolX: number; // 5     volume >= this × the mean volume of the prior bars (a mean of 0 passes)
  priorBars: number; // 10    the prior bars are the up to this many TRADED bars before the candle (they may span the previous day)
  minPriorBars: number; // 3     no dildo is judged with fewer prior bars
  minPrice: number; // 0.20  close >= this
  minDollarVol: number; // 100000 close × volume >= this
  pbWindowMin: number; // 20    the pullback is looked for on bars with ts <= dildo ts + this many minutes
  touchPct: number; // 1     touch level = EMA9[j−1] × (1 + this/100)
  holdPct: number; // 1     the touch bar must close >= EMA9[j] × (1 − this/100)
  floor: 'mid' | 'open'; // 'mid' floor = (open + close)/2 of the dildo bar; 'open' = its open
  emaPeriod: number; // 9
}

export const DEFAULT_DILDO_PARAMS: DildoParams = {
  enabled: true,
  minBodyPct: 8,
  minBodyX: 4,
  maxTopPct: 20,
  minVolX: 5,
  priorBars: 10,
  minPriorBars: 3,
  minPrice: 0.2,
  minDollarVol: 100_000,
  pbWindowMin: 20,
  touchPct: 1,
  holdPct: 1,
  floor: 'mid',
  emaPeriod: 9,
};

/** The prior bars' sums, built oldest -> newest (see the module comment). */
export interface DildoPriors {
  n: number;
  sumAbsBody: number;
  sumVol: number;
}

export interface DildoHit {
  bodyPct: number;
  bodyX: number; // +Infinity when the priors' mean body is 0
  volX: number; // +Infinity when the priors' mean volume is 0
  topPct: number;
  dollarVol: number;
}

export type DildoEventKind =
  | 'dildo' // GREEN DILDO: the candle closed (alert 1's moment; marker 'buy')
  | 'dildoPb' // 9 EMA PULLBACK: the touch bar held the EMA (alert 2's moment; marker 'buy')
  | 'dildoCancel'; // PB CANCELLED: the pullback broke the floor / closed under the EMA / faded (list-only)

export type DildoCancelReason = 'floor' | 'ema' | 'faded';

export interface DildoEvent {
  ts: number; // the bar's start
  kind: DildoEventKind;
  price: number; // the dildo's close; the touch bar's close; for a cancel, the bar's low ('floor' / 'faded') or close ('ema')
  label: string; // 'GREEN DILDO' | '9 EMA PULLBACK' | 'PB CANCELLED'
  note: string;
  marker?: 'buy';
  side: 'long';
  reason?: DildoCancelReason; // dildoCancel only
  emaDistPct?: number; // dildoPb only: the touch bar's close vs its own EMA, % (positive = above)
}

/** pbState values (the scan's dildoPbState). */
export const PB_NONE = 0;
export const PB_WATCHING = 1;
export const PB_TOUCHING = 2;
export const PB_CONFIRMED = 3;
export const PB_CANCELLED = 4;
export const PB_EXPIRED = 5;

export interface DildoResult {
  events: DildoEvent[]; // every ET day in the input, oldest first (emission order == ts order)
  // The LAST ET day only (null / 0 = none): its latest dildo, and that anchor's pullback.
  lastDildoTs: number | null;
  lastDildoBodyPct: number | null;
  lastDildoBodyX: number | null; // may be Infinity
  lastDildoVolX: number | null; // may be Infinity
  lastDildoDvol: number | null;
  pbState: number; // 0 none, 1 watching, 2 touching, 3 confirmed, 4 cancelled, 5 expired
  touchTs: number | null; // the touch bar (a closed touch bar of any verdict, or the forming bar while TOUCHING)
  pbTs: number | null; // the confirming touch bar (pbState 3)
  pbPx: number | null; // its close: the entry (pbState 3)
  floor: number | null;
  deadlineTs: number | null; // dildo ts + pbWindowMin
}

export interface DildoInput {
  candles: Candle[]; // ts ascending; may span the previous trading day + today
  tfMs: number; // the rule is 1-minute only: anything but 60 000 returns an empty result
  nowMs: number; // the last candle is FORMING iff nowMs < last.ts + tfMs
  params?: Partial<DildoParams>;
}

// ─── The candle ──────────────────────────────────────────────────────────

/** Sums over candles[from, to), oldest -> newest — the one construction every user of classifyDildo shares. */
export function dildoPriors(candles: readonly Candle[], from: number, to: number): DildoPriors {
  let sumAbsBody = 0;
  let sumVol = 0;
  for (let k = from; k < to; k++) {
    const c = candles[k]!;
    sumAbsBody += Math.abs(c.close - c.open);
    sumVol += c.volume;
  }
  return { n: Math.max(0, to - from), sumAbsBody, sumVol };
}

/**
 * Is `bar` a green dildo given its priors? Pure; Go's dildopb.Classify is the
 * same expressions in the same order (the golden vectors pin the bits).
 */
export function classifyDildo(bar: Candle, pr: DildoPriors, p: DildoParams): DildoHit | null {
  const o = bar.open;
  const h = bar.high;
  const l = bar.low;
  const c = bar.close;
  const v = bar.volume;
  if (!(c > o) || !(o > 0) || !(h > l)) return null;
  const bodyPct = ((c - o) / o) * 100;
  if (!(bodyPct >= p.minBodyPct)) return null;
  if (!(c >= p.minPrice)) return null;
  const dollarVol = c * v;
  if (!(dollarVol >= p.minDollarVol)) return null;
  if (!(pr.n >= p.minPriorBars)) return null;
  const meanBody = pr.sumAbsBody / pr.n;
  const bodyX = meanBody > 0 ? (c - o) / meanBody : Infinity;
  if (!(bodyX >= p.minBodyX)) return null;
  const meanVol = pr.sumVol / pr.n;
  const volX = meanVol > 0 ? v / meanVol : Infinity;
  if (!(volX >= p.minVolX)) return null;
  const topPct = ((h - c) / (h - l)) * 100;
  if (!(topPct <= p.maxTopPct)) return null;
  return { bodyPct, bodyX, volX, topPct, dollarVol };
}

// ─── Formatting (the Go port formats the same text: dildopb Event.Note) ──

/** A multiple for a note: '6.1' / '18', or '∞' when the priors' mean was 0. */
function xf(v: number, digits: number): string {
  return Number.isFinite(v) ? v.toFixed(digits) : '∞';
}

/** Candle dollar volume: '$717k' under $1M, '$1.2M' from there. */
export function fmtDvol(v: number): string {
  return v < 1e6 ? `$${Math.round(v / 1e3)}k` : `$${(v / 1e6).toFixed(1)}M`;
}

// Signed one-decimal percent ('+1.6%', '-0.4%'); never prints '-0.0%' (emaFlag.ts's spct1).
function spct1(v: number): string {
  const r = Number(v.toFixed(1));
  const z = Object.is(r, -0) ? 0 : r;
  return `${z >= 0 ? '+' : ''}${z.toFixed(1)}%`;
}

// ─── The pullback ────────────────────────────────────────────────────────

interface Anchor {
  idx: number;
  ts: number;
  hit: DildoHit;
  floor: number;
  deadline: number;
  verdict: number; // 0 pending, else PB_CONFIRMED / PB_CANCELLED / PB_EXPIRED
  touchTs: number | null;
  pbTs: number | null;
  pbPx: number | null;
}

function emptyResult(): DildoResult {
  return {
    events: [],
    lastDildoTs: null,
    lastDildoBodyPct: null,
    lastDildoBodyX: null,
    lastDildoVolX: null,
    lastDildoDvol: null,
    pbState: PB_NONE,
    touchTs: null,
    pbTs: null,
    pbPx: null,
    floor: null,
    deadlineTs: null,
  };
}

export function evaluateDildo(input: DildoInput): DildoResult {
  const p: DildoParams = { ...DEFAULT_DILDO_PARAMS, ...input.params };
  const res = emptyResult();
  const candles = input.candles;
  const n = candles.length;
  if (input.tfMs !== 60_000 || !p.enabled || n === 0) return res;

  const closes = new Array<number>(n);
  for (let i = 0; i < n; i++) closes[i] = candles[i]!.close;
  const ema = emaSeries(closes, Math.max(1, Math.floor(p.emaPeriod)));
  const forming = input.nowMs < candles[n - 1]!.ts + input.tfMs;
  const lastClosed = forming ? n - 2 : n - 1;
  const windowMs = p.pbWindowMin * 60_000;
  const touchK = 1 + p.touchPct / 100;
  const holdK = 1 - p.holdPct / 100;
  const P = p.emaPeriod;
  const events = res.events;

  let pending: Anchor | null = null; // the anchor still waiting for its pullback
  let latest: Anchor | null = null; // the latest dildo (pending or decided)

  function emit(j: number, kind: DildoEventKind, price: number, label: string, note: string, extra?: { reason?: DildoCancelReason; emaDistPct?: number }): void {
    const ev: DildoEvent = kind === 'dildoCancel' ? { ts: candles[j]!.ts, kind, price, label, note, side: 'long' } : { ts: candles[j]!.ts, kind, price, label, note, marker: 'buy', side: 'long' };
    if (extra?.reason) ev.reason = extra.reason;
    if (extra?.emaDistPct != null) ev.emaDistPct = extra.emaDistPct;
    events.push(ev);
  }

  function cancel(j: number, a: Anchor, reason: DildoCancelReason, price: number, note: string): void {
    a.verdict = PB_CANCELLED;
    emit(j, 'dildoCancel', price, 'PB CANCELLED', `${fmtEt(candles[j]!.ts)} pullback cancelled: ${note}`, { reason });
  }

  for (let j = 0; j <= lastClosed; j++) {
    const c = candles[j]!;
    // a) the pending anchor's pullback
    const a = pending;
    if (a) {
      if (c.ts <= a.deadline) {
        const e = j >= 1 ? ema[j - 1] : null;
        if (e != null) {
          const level = e * touchK;
          if (c.low <= level) {
            a.touchTs = c.ts;
            const ej = ema[j]!;
            if (c.low <= a.floor) {
              cancel(j, a, 'floor', c.low, `low ${fmtPx(c.low)} broke the floor ${fmtPx(a.floor)}`);
            } else if (c.close < ej * holdK) {
              cancel(j, a, 'ema', c.close, `closed ${(((ej - c.close) / ej) * 100).toFixed(1)}% under the ${P} EMA`);
            } else {
              a.verdict = PB_CONFIRMED;
              a.pbTs = c.ts;
              a.pbPx = c.close;
              const dist = ((c.close - ej) / ej) * 100;
              const mins = Math.round((c.ts - a.ts) / 60_000);
              emit(
                j,
                'dildoPb',
                c.close,
                '9 EMA PULLBACK',
                `${fmtEt(c.ts)} pullback to the ${P} EMA held: close ${fmtPx(c.close)} vs EMA ${fmtPx(ej)} (${spct1(dist)}), ${mins} min after the dildo`,
                { emaDistPct: dist },
              );
            }
            pending = null;
          } else if (c.low <= a.floor) {
            cancel(j, a, 'faded', c.low, `faded below the floor ${fmtPx(a.floor)} before reaching the ${P} EMA`);
            pending = null;
          }
        }
      } else {
        a.verdict = PB_EXPIRED; // silent
        pending = null;
      }
    }
    // b) is bar j itself a dildo? (latest supersedes)
    const from = Math.max(0, j - p.priorBars);
    const hit = classifyDildo(c, dildoPriors(candles, from, j), p);
    if (hit) {
      const floor = p.floor === 'open' ? c.open : (c.open + c.close) / 2;
      const anchor: Anchor = { idx: j, ts: c.ts, hit, floor, deadline: c.ts + windowMs, verdict: 0, touchTs: null, pbTs: null, pbPx: null };
      pending = anchor;
      latest = anchor;
      emit(
        j,
        'dildo',
        c.close,
        'GREEN DILDO',
        `${fmtEt(c.ts)} green dildo +${hit.bodyPct.toFixed(1)}% body (${xf(hit.bodyX, 1)}x prior), ${xf(hit.volX, 0)}x volume, ${fmtDvol(hit.dollarVol)}; floor ${fmtPx(floor)}`,
      );
    }
  }

  // ── the Result: the last ET day's latest dildo ──
  const L: Anchor | null = latest;
  if (L && etDayKey(L.ts) === etDayKey(candles[n - 1]!.ts)) {
    res.lastDildoTs = L.ts;
    res.lastDildoBodyPct = L.hit.bodyPct;
    res.lastDildoBodyX = L.hit.bodyX;
    res.lastDildoVolX = L.hit.volX;
    res.lastDildoDvol = L.hit.dollarVol;
    res.floor = L.floor;
    res.deadlineTs = L.deadline;
    res.touchTs = L.touchTs;
    res.pbTs = L.pbTs;
    res.pbPx = L.pbPx;
    if (L.verdict !== 0) {
      res.pbState = L.verdict;
    } else {
      // still pending after the last CLOSED bar: the forming bar may move the state, never emit
      let st = 0;
      if (forming) {
        const f = candles[n - 1]!;
        const e = n >= 2 ? ema[n - 2] : null;
        if (f.ts <= L.deadline && e != null) {
          if (f.low <= L.floor) st = PB_CANCELLED;
          else if (f.low <= e * touchK) {
            st = PB_TOUCHING;
            res.touchTs = f.ts;
          }
        }
      }
      res.pbState = st || (input.nowMs >= L.deadline + input.tfMs ? PB_EXPIRED : PB_WATCHING);
    }
  }
  return res;
}
