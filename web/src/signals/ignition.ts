// Pure IGNITION rule (1-minute only). No DOM, no store, no `Date.now()`;
// imports the chart engine TYPE-ONLY so its suite runs in node.
//
// The small first candle out of a quiet base right before a dildo. Example
// NCPL 2026-10-14 ~09:06 ET: a flat ~1.75-1.80 base, then a ~+5% green candle
// on a volume spike, then the big dildo the next minute. An ALERT ("SYM:
// ignition"), a "look now" prompt, NOT a system (uncalibrated).
//
// ONE rule, kept bit-identical with Go internal/ignition (the scanner engine
// runs ignition.Judge market-wide) by golden vectors
// (fixtures/golden/ignition-golden.json; regenerate with UPDATE_GOLDEN=1, see
// ignition.golden.test.ts).
//
// THE RULE, per closed 1-minute bar:
//  1. quiet base: the previous baseBars bars (any day, at least 10) span
//     (max high - min low)/min low <= maxBasePct;
//  2. volume >= minVolX x the mean of the previous volBars bars;
//  3. green body (c-o)/o in [minBodyPct, maxBodyPct] (a full dildo is not one);
//  4. close > the base high AND high > the day high before it (04:00 ET on);
//  5. close >= minPrice and close x volume >= minDvol.
// One per base (re-armed once a later bar's base is not quiet, or a new ET
// day), and at most one per cooldownMin minutes.

import type { Candle } from '../vendor/blackthorn-charts';
import { etDayKey, etParts } from '../marketClock';
import { fmtEt, fmtPx } from './shortFade';

export interface IgnitionParams {
  enabled: boolean; // true
  baseBars: number; // 15
  maxBasePct: number; // 8
  volBars: number; // 10
  minVolX: number; // 5
  minBodyPct: number; // 2
  maxBodyPct: number; // 8
  minPrice: number; // 0.20
  minDvol: number; // 50 000
  cooldownMin: number; // 10
}

export const DEFAULT_IGNITION_PARAMS: IgnitionParams = {
  enabled: true,
  baseBars: 15,
  maxBasePct: 8,
  volBars: 10,
  minVolX: 5,
  minBodyPct: 2,
  maxBodyPct: 8,
  minPrice: 0.2,
  minDvol: 50_000,
  cooldownMin: 10,
};

/** dildopb.EarlyMinBaseBars. */
export const IGNITION_MIN_BASE_BARS = 10;

export type IgnitionReason = 'ok' | 'lowPrice' | 'lowDvol' | 'bodySmall' | 'bodyBig' | 'noVolume' | 'fewBars' | 'wideBase' | 'noBreakout' | 'notNewHigh';

export interface IgnitionVerdict {
  reason: IgnitionReason;
  bodyPct: number;
  volX: number;
  dvol: number;
  rangePct: number;
  baseHigh: number;
  dayHigh: number;
}

export interface IgnitionEvent {
  ts: number;
  kind: 'ignition';
  price: number; // the close
  label: 'IGNITION';
  note: string;
  marker: 'buy';
  side: 'long';
  bodyPct: number;
  volX: number;
  dvol: number;
  rangePct: number;
  baseHigh: number;
  dayHigh: number;
}

export interface IgnitionInput {
  candles: Candle[];
  tfMs: number;
  nowMs: number;
  params?: Partial<IgnitionParams>;
}

/**
 * judgeIgnition judges one closed bar (ignition.Judge). hs/ls[from, to) are the
 * base bars before it; sumVol / nVol the previous volumes' sum (oldest first)
 * and count; dayHighBefore the day high before it (<= 0: none).
 */
export function judgeIgnition(
  hs: readonly number[],
  ls: readonly number[],
  from: number,
  to: number,
  sumVol: number,
  nVol: number,
  o: number,
  h: number,
  c: number,
  v: number,
  dayHighBefore: number,
  p: IgnitionParams,
): IgnitionVerdict {
  // dildopb.JudgeEarly (FirstOnly off, NewHighOfDay on)
  if (p.baseBars > 0 && to - from > p.baseBars) from = to - p.baseBars;
  const n = to - from;
  const vd: IgnitionVerdict = { reason: 'ok', bodyPct: 0, volX: 0, dvol: c * v, rangePct: 0, baseHigh: 0, dayHigh: Math.max(dayHighBefore, 0) };
  if (o > 0) vd.bodyPct = ((c - o) / o) * 100;
  if (nVol > 0) vd.volX = v / (sumVol / nVol);
  let early: 'ok' | 'few' | 'wide' | 'noBreakout' | 'notNewHigh' = 'ok';
  if (n < IGNITION_MIN_BASE_BARS) early = 'few';
  else {
    let hi = hs[from]!;
    let lo = ls[from]!;
    for (let i = from + 1; i < to; i++) {
      hi = Math.max(hi, hs[i]!);
      lo = Math.min(lo, ls[i]!);
    }
    if (!(lo > 0)) early = 'wide';
    else {
      vd.rangePct = ((hi - lo) / lo) * 100;
      vd.baseHigh = hi;
      if (vd.rangePct > p.maxBasePct) early = 'wide';
      else if (!(c > hi)) early = 'noBreakout';
      else if (dayHighBefore > 0 && !(h > dayHighBefore)) early = 'notNewHigh';
    }
  }
  if (early === 'few' || (nVol === 0 && early !== 'wide')) vd.reason = 'fewBars';
  else if (early === 'wide') vd.reason = 'wideBase';
  if (vd.reason !== 'ok') return vd;
  if (!(c >= p.minPrice)) vd.reason = 'lowPrice';
  else if (!(vd.dvol >= p.minDvol)) vd.reason = 'lowDvol';
  else if (!(vd.bodyPct >= p.minBodyPct)) vd.reason = 'bodySmall';
  else if (!(vd.bodyPct <= p.maxBodyPct)) vd.reason = 'bodyBig';
  else if (!(vd.volX >= p.minVolX)) vd.reason = 'noVolume';
  else if (early === 'noBreakout') vd.reason = 'noBreakout';
  else if (early === 'notNewHigh') vd.reason = 'notNewHigh';
  return vd;
}

const quiet = (vd: IgnitionVerdict): boolean => vd.reason !== 'fewBars' && vd.reason !== 'wideBase';

export function evaluateIgnition(input: IgnitionInput): IgnitionEvent[] {
  const p: IgnitionParams = { ...DEFAULT_IGNITION_PARAMS, ...input.params };
  const out: IgnitionEvent[] = [];
  const candles = input.candles;
  const n = candles.length;
  if (input.tfMs !== 60_000 || !p.enabled || n === 0) return out;
  const last = input.nowMs < candles[n - 1]!.ts + input.tfMs ? n - 2 : n - 1;
  const hs = candles.map((c) => c.high);
  const ls = candles.map((c) => c.low);
  let spent = false;
  let lastMs = 0;
  let day = '';
  let dayHi = 0;
  // etDayKey / etParts are an Intl call each. The ET date and ET hour can only
  // change on a UTC HOUR boundary (America/New_York's offsets are whole hours
  // and its DST switches happen on the hour), so one lookup per UTC hour gives
  // both for every candle in it (the same trick as emaFlag.ts / climax.ts).
  let lastHour = Number.NaN;
  let key = '';
  let hourEt = 0;
  for (let j = 0; j <= last; j++) {
    const b = candles[j]!;
    const hour = Math.floor(b.ts / 3_600_000);
    if (hour !== lastHour) {
      lastHour = hour;
      key = etDayKey(b.ts);
      hourEt = etParts(b.ts).hh;
    }
    const d = key;
    const newDay = j === 0 || d !== day;
    if (newDay) {
      day = d;
      dayHi = 0;
    }
    const lo = Math.max(0, j - p.baseBars);
    const vlo = Math.max(0, j - p.volBars);
    let sumVol = 0;
    for (let k = vlo; k < j; k++) sumVol += candles[k]!.volume;
    const vd = judgeIgnition(hs, ls, lo, j, sumVol, j - vlo, b.open, b.high, b.close, b.volume, dayHi, p);
    // ignition.Latch.Step
    if (newDay || !quiet(vd)) spent = false;
    if (vd.reason === 'ok' && !spent && !(lastMs !== 0 && b.ts - lastMs < p.cooldownMin * 60_000)) {
      spent = true;
      lastMs = b.ts;
      out.push({
        ts: b.ts,
        kind: 'ignition',
        price: b.close,
        label: 'IGNITION',
        note:
          `${fmtEt(b.ts)} ignition: +${vd.bodyPct.toFixed(1)}% candle on ${isFinite(vd.volX) ? vd.volX.toFixed(1) : '∞'}x volume ` +
          `out of a ${vd.rangePct.toFixed(1)}% base, closed ${fmtPx(b.close)} over the ${fmtPx(vd.baseHigh)} base high`,
        marker: 'buy',
        side: 'long',
        bodyPct: vd.bodyPct,
        volX: vd.volX,
        dvol: vd.dvol,
        rangePct: vd.rangePct,
        baseHigh: vd.baseHigh,
        dayHigh: vd.dayHigh,
      });
    }
    if (hourEt >= 4 && b.high > dayHi) dayHi = b.high;
  }
  return out;
}
