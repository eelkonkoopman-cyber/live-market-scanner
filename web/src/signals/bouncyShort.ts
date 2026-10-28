// Pure BOUNCY BALL SHORT rule (1-minute only). No DOM, no store, no
// `Date.now()`; imports the chart engine TYPE-ONLY so its suite runs in node.
//
// The trader (2026-10-14): "It has a solid support and is making lower highs
// on each bounce of that support and after that will sell off sharply."
// Example AAOI 2026-10-14: support ~115.85 touched ~09:50, 10:05, 10:20, 10:35
// ET, bounce highs 119.9 -> 117.8 -> 117.4 -> 116.8, broke ~10:40, sold to 110.
// An ALERT ("SYM: bouncy ball short"), a "look now" prompt, NOT a system
// (uncalibrated).
//
// ONE rule, kept bit-identical with Go internal/bbs (the scanner engine runs
// bbs.Analyze market-wide) by golden vectors
// (fixtures/golden/bbs-golden.json; regenerate with UPDATE_GOLDEN=1, see
// bouncyShort.golden.test.ts).
//
// THE STRUCTURE (analyzeBbs over closed bars of ONE ET day, oldest -> newest):
//  1. Support: walk back from the newest bar keeping S = the running minimum
//     low. Each bar whose low is under S*(1-band) closes a candidate base (S
//     over the bars after it); the first candidate that passes wins, else the
//     walk goes on, the last candidate being the whole window. No close in a
//     base can be under S*(1-band).
//  2. Touches: a bar with low <= S*(1+band). A touch is NEW only if it is
//     >= minSepBars after the previous touch bar AND some bar since that touch
//     reached S*(1+minBouncePct) (a real bounce); otherwise it extends it.
//  3. Bounce highs: the highest high from a touch's (last) bar until the next
//     touch (for the last touch: until the newest bar), counted once it reached
//     the bounce level. Lower highs: the trailing run, each <= the previous *
//     (1-lowerHighPct).
//  OK = touches >= minTouches and >= minLowerHighs trailing lower highs.
// THE ALERT, per closed bar j: window = the last `lookback` bars before j after
// the day's latest break; OK and close[j] < S*(1-breakPct) -> BOUNCY BALL SHORT
// at the close; a break clears the window. Otherwise j joins the window and the
// state is FORMING when analyze(window incl. j) is OK and close[j] <=
// S*(1+formingNearPct).

import type { Candle } from '../vendor/blackthorn-charts';
import { etDayKey } from '../marketClock';
import { fmtEt, fmtPx } from './shortFade';

export interface BbsParams {
  enabled: boolean; // true
  lookback: number; // 90
  supportBandPct: number; // 0.4
  minTouches: number; // 3
  minSepBars: number; // 3
  minBouncePct: number; // 0.5
  minLowerHighs: number; // 2
  lowerHighPct: number; // 0.2
  breakPct: number; // 0.3
  formingNearPct: number; // 1.0
}

export const DEFAULT_BBS_PARAMS: BbsParams = {
  enabled: true,
  lookback: 90,
  supportBandPct: 0.4,
  minTouches: 3,
  minSepBars: 3,
  minBouncePct: 0.5,
  minLowerHighs: 2,
  lowerHighPct: 0.2,
  breakPct: 0.3,
  formingNearPct: 1.0,
};

export interface BbsPattern {
  ok: boolean;
  support: number;
  touches: number;
  highs: number; // the trailing lower-high run's length (bounce highs)
  firstHigh: number;
  lastHigh: number;
  start: number; // the base's first index
}

export const BBS_NONE = 0;
export const BBS_FORMING = 1;
export const BBS_BROKEN = 2;

export interface BbsEvent {
  ts: number;
  kind: 'bbs';
  price: number; // the breaking close
  label: 'BOUNCY BALL SHORT';
  note: string;
  marker: 'sell';
  side: 'short';
  support: number;
  touches: number;
  highs: number;
  firstHigh: number;
  lastHigh: number;
}

export interface BbsResult {
  events: BbsEvent[];
  state: number; // the last ET day: 0 none, 1 forming, 2 broken
  support: number | null; // forming: the base's; broken: the latest break's
  lastTs: number | null; // the last ET day's latest break
}

export interface BbsInput {
  candles: Candle[];
  tfMs: number;
  nowMs: number;
  params?: Partial<BbsParams>;
}

/** analyzeBbs judges the base ending at the newest of hs/ls[from, to) (bbs.Analyze). */
export function analyzeBbs(hs: readonly number[], ls: readonly number[], from: number, to: number, p: BbsParams): BbsPattern {
  if (to - from <= 0) return { ok: false, support: 0, touches: 0, highs: 0, firstHigh: 0, lastHigh: 0, start: 0 };
  const band = p.supportBandPct / 100;
  let s = Infinity;
  for (let i = to - 1; i >= from; i--) {
    if (s !== Infinity && ls[i]! < s * (1 - band)) {
      const pt = bbsBase(hs, ls, from, i + 1, to, s, p);
      if (pt.ok) return pt;
    }
    if (ls[i]! < s) s = ls[i]!;
  }
  return bbsBase(hs, ls, from, from, to, s, p);
}

/** The touch / bounce pass over [b, to) against the support s (bbs.base). */
function bbsBase(hs: readonly number[], ls: readonly number[], from: number, b: number, to: number, s: number, p: BbsParams): BbsPattern {
  const band = p.supportBandPct / 100;
  const thr = s * (1 + band);
  const bounceLvl = s * (1 + p.minBouncePct / 100);
  const lhK = 1 - p.lowerHighPct / 100;
  let touches = 0;
  let lastT = -1;
  let curHi = -Infinity;
  let bounced = false;
  let nH = 0;
  let run = 0;
  let prevH = 0;
  let firstH = 0;
  for (let k = b; k <= to; k++) {
    let closeBounce = false;
    if (k === to) closeBounce = touches > 0 && bounced;
    else if (ls[k]! <= thr) {
      if (touches === 0) touches = 1;
      else if (k - lastT >= p.minSepBars && bounced) {
        closeBounce = true;
        touches++;
      }
    }
    if (closeBounce) {
      if (nH > 0 && curHi <= prevH * lhK) run++;
      else {
        run = 1;
        firstH = curHi;
      }
      prevH = curHi;
      nH++;
    }
    if (k === to) break;
    if (ls[k]! <= thr) {
      lastT = k;
      curHi = hs[k]!;
      bounced = hs[k]! >= bounceLvl;
      continue;
    }
    if (touches > 0) {
      if (hs[k]! > curHi) curHi = hs[k]!;
      if (hs[k]! >= bounceLvl) bounced = true;
    }
  }
  const pt: BbsPattern = { ok: false, support: s, touches, highs: 0, firstHigh: 0, lastHigh: 0, start: b - from };
  if (nH > 0) {
    pt.highs = run;
    pt.firstHigh = firstH;
    pt.lastHigh = prevH;
  }
  pt.ok = touches >= p.minTouches && run - 1 >= p.minLowerHighs;
  return pt;
}

export const bbsBreaks = (pt: BbsPattern, c: number, p: BbsParams): boolean => pt.ok && c < pt.support * (1 - p.breakPct / 100);
export const bbsForming = (pt: BbsPattern, c: number, p: BbsParams): boolean => pt.ok && c <= pt.support * (1 + p.formingNearPct / 100);

export function evaluateBbs(input: BbsInput): BbsResult {
  const p: BbsParams = { ...DEFAULT_BBS_PARAMS, ...input.params };
  const res: BbsResult = { events: [], state: BBS_NONE, support: null, lastTs: null };
  const candles = input.candles;
  const n = candles.length;
  if (input.tfMs !== 60_000 || !p.enabled || n === 0) return res;
  const last = input.nowMs < candles[n - 1]!.ts + input.tfMs ? n - 2 : n - 1;
  const look = Math.max(1, Math.floor(p.lookback));
  let hs: number[] = [];
  let ls: number[] = [];
  let ws = 0;
  let day = '';
  let state = BBS_NONE;
  let sup: number | null = null;
  let lastTs: number | null = null;
  // etDayKey is an Intl format() per call. The ET calendar date can only change
  // on a UTC HOUR boundary (America/New_York's offsets are whole hours and its
  // DST switches happen on the hour), so one lookup per UTC hour gives the same
  // key for every candle in it (the same trick as emaFlag.ts / shortFade.ts).
  let lastHour = Number.NaN;
  let key = '';
  for (let j = 0; j <= last; j++) {
    const c = candles[j]!;
    const hour = Math.floor(c.ts / 3_600_000);
    if (hour !== lastHour) {
      lastHour = hour;
      key = etDayKey(c.ts);
    }
    const d = key;
    if (j === 0 || d !== day) {
      day = d;
      hs = [];
      ls = [];
      ws = 0;
      state = BBS_NONE;
      sup = null;
      lastTs = null;
    }
    if (ls.length - ws > look) ws = ls.length - look;
    const pt = analyzeBbs(hs, ls, ws, ls.length, p);
    if (bbsBreaks(pt, c.close, p)) {
      res.events.push({
        ts: c.ts,
        kind: 'bbs',
        price: c.close,
        label: 'BOUNCY BALL SHORT',
        note:
          `${fmtEt(c.ts)} bouncy ball short: closed ${fmtPx(c.close)} under the ${fmtPx(pt.support)} support ` +
          `(${pt.touches} touches), lower highs ${fmtPx(pt.firstHigh)} → ${fmtPx(pt.lastHigh)} over ${pt.highs} bounces`,
        marker: 'sell',
        side: 'short',
        support: pt.support,
        touches: pt.touches,
        highs: pt.highs,
        firstHigh: pt.firstHigh,
        lastHigh: pt.lastHigh,
      });
      hs = [];
      ls = [];
      ws = 0;
      state = BBS_BROKEN;
      sup = pt.support;
      lastTs = c.ts;
      continue;
    }
    hs.push(c.high);
    ls.push(c.low);
    if (ls.length - ws > look) ws = ls.length - look;
    const pf = analyzeBbs(hs, ls, ws, ls.length, p);
    if (bbsForming(pf, c.close, p)) {
      state = BBS_FORMING;
      sup = pf.support;
    } else if (lastTs !== null) {
      state = BBS_BROKEN;
      sup = res.events[res.events.length - 1]!.support;
    } else {
      state = BBS_NONE;
      sup = null;
    }
  }
  res.state = state;
  res.support = sup;
  res.lastTs = lastTs;
  return res;
}
