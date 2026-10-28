// evaluateBbs / evaluateIgnition look the ET day key (and ignition the ET hour)
// up ONCE PER UTC HOUR instead of once per candle. That is only the same answer
// because America/New_York's offsets are whole hours and its DST switches land
// on a UTC hour boundary, so the ET date and ET hour cannot change inside an
// aligned UTC hour. Two pins: (1) that premise, minute by minute, around every
// DST switch of 2025-2027; (2) the two evaluators against UNCACHED copies of
// their previous bodies (per-candle etDayKey / etParts) on series that run
// straight across a spring-forward and a fall-back night.
import { describe, expect, it } from 'vitest';
import { etDayKey, etParts } from '../marketClock';
import {
  BBS_BROKEN,
  BBS_FORMING,
  BBS_NONE,
  DEFAULT_BBS_PARAMS,
  analyzeBbs,
  bbsBreaks,
  bbsForming,
  evaluateBbs,
  type BbsInput,
  type BbsParams,
  type BbsResult,
} from './bouncyShort';
import { DEFAULT_IGNITION_PARAMS, evaluateIgnition, judgeIgnition, type IgnitionEvent, type IgnitionInput, type IgnitionParams, type IgnitionVerdict } from './ignition';
import { aaoiBars, randomBars } from './bouncyShort.fixtures';
import { ncplBars, randomIgnitionBars } from './ignition.fixtures';
import { fmtEt, fmtPx } from './shortFade';
import type { Candle } from '../vendor/blackthorn-charts';

const MIN = 60_000;
const HOUR = 3_600_000;
const DAY = 24 * HOUR;
const TIMEOUT = 60_000; // the uncached originals are slow, and the suite runs in parallel on a busy machine

// ─── the uncached originals (bodies as they were before the hour cache) ───────

function evaluateBbsUncached(input: BbsInput): BbsResult {
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
  for (let j = 0; j <= last; j++) {
    const c = candles[j]!;
    const d = etDayKey(c.ts);
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

const quiet = (vd: IgnitionVerdict): boolean => vd.reason !== 'fewBars' && vd.reason !== 'wideBase';

function evaluateIgnitionUncached(input: IgnitionInput): IgnitionEvent[] {
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
  for (let j = 0; j <= last; j++) {
    const b = candles[j]!;
    const d = etDayKey(b.ts);
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
    if (etParts(b.ts).hh >= 4 && b.high > dayHi) dayHi = b.high;
  }
  return out;
}

// ─── 1. the premise ──────────────────────────────────────────────────────────

function sunday(year: number, month: number, n: number): number {
  const first = new Date(Date.UTC(year, month, 1)).getUTCDay();
  return 1 + ((7 - first) % 7) + (n - 1) * 7;
}

// New York's switches: 2nd Sunday of March 07:00 UTC, 1st Sunday of November 06:00 UTC.
const NY_SWITCHES: number[] = [];
for (const y of [2025, 2026, 2027]) NY_SWITCHES.push(Date.UTC(y, 2, sunday(y, 2, 2), 7), Date.UTC(y, 10, sunday(y, 10, 1), 6));

describe('the ET date and ET hour are constant inside an aligned UTC hour', () => {
  it('holds minute by minute for 12 h either side of every DST switch of 2025-2027', () => {
    let checked = 0;
    for (const sw of NY_SWITCHES) {
      for (let t = sw - 12 * HOUR; t < sw + 12 * HOUR; t += MIN) {
        const hourStart = Math.floor(t / HOUR) * HOUR;
        expect(etDayKey(t)).toBe(etDayKey(hourStart));
        expect(etParts(t).hh).toBe(etParts(hourStart).hh);
        checked++;
      }
    }
    expect(checked).toBe(NY_SWITCHES.length * 24 * 60);
  }, TIMEOUT);
});

// ─── 2. the evaluators against their uncached originals ──────────────────────

const base14 = Date.UTC(2026, 9, 14);
/** The same bars moved by whole days so they start on UTC day `utcDay`. */
const onDay = (bars: Candle[], utcDay: number): Candle[] => bars.map((c) => ({ ...c, ts: c.ts + (utcDay - base14) }));
/** The same bars moved so the first one lands on `start`. */
const startAt = (bars: Candle[], start: number): Candle[] => bars.map((c) => ({ ...c, ts: c.ts - bars[0]!.ts + start }));

// A day-and-a-bit of continuous 1-minute bars straight across the switch night,
// then days with a known bouncy-ball break and a known ignition after it.
function acrossSwitch(sw: number, seed: number): Candle[] {
  const day = Math.floor(sw / DAY) * DAY;
  return [
    ...startAt(randomIgnitionBars(seed, 1500), sw - 11 * HOUR),
    ...onDay(aaoiBars(), day + 3 * DAY),
    ...onDay(ncplBars(), day + 4 * DAY),
    ...startAt(randomBars(seed + 1, 500), day + 5 * DAY + 13 * HOUR),
  ];
}

const SERIES: [string, Candle[]][] = [
  ['spring-forward 2026-03-08', acrossSwitch(Date.UTC(2026, 2, 8, 7), 3)],
  ['fall-back 2026-11-01', acrossSwitch(Date.UTC(2026, 10, 1, 6), 5)],
];

const BBS_PARAMS: Record<string, Partial<BbsParams> | undefined> = {
  defaults: undefined,
  loose: { lookback: 40, supportBandPct: 0.8, minTouches: 2, minSepBars: 2, minBouncePct: 0.3, minLowerHighs: 1, lowerHighPct: 0.05, breakPct: 0.1 },
};
const IGN_PARAMS: Record<string, Partial<IgnitionParams> | undefined> = {
  defaults: undefined,
  loose: { baseBars: 12, maxBasePct: 4, minVolX: 3, minBodyPct: 1.2, maxBodyPct: 6, minDvol: 20_000, cooldownMin: 3 },
};

describe('hour-cached evaluators == uncached originals across DST nights', () => {
  let bbsEvents = 0;
  let ignEvents = 0;
  let bbsStates = new Set<number>();

  for (const [name, candles] of SERIES) {
    it(name, () => {
      expect(candles.every((c, i) => i === 0 || c.ts > candles[i - 1]!.ts)).toBe(true); // ascending
      // prefix cuts through the switch night, the known-event days and the end; closed and forming clocks
      const cuts = new Set<number>([candles.length, 700, 1300, 1520, 1560, 1750]);
      for (const cut of cuts) {
        if (cut > candles.length) continue;
        const cs = candles.slice(0, cut);
        for (const nowMs of [cs[cs.length - 1]!.ts + 10 * MIN, cs[cs.length - 1]!.ts + 30_000]) {
          for (const [pn, params] of Object.entries(BBS_PARAMS)) {
            const input: BbsInput = { candles: cs, tfMs: 60_000, nowMs, params };
            const fresh = evaluateBbs(input);
            expect(fresh, `bbs ${pn} cut ${cut}`).toEqual(evaluateBbsUncached(input));
            bbsEvents += fresh.events.length;
            bbsStates.add(fresh.state);
          }
          for (const [pn, params] of Object.entries(IGN_PARAMS)) {
            const input: IgnitionInput = { candles: cs, tfMs: 60_000, nowMs, params };
            const fresh = evaluateIgnition(input);
            expect(fresh, `ignition ${pn} cut ${cut}`).toEqual(evaluateIgnitionUncached(input));
            ignEvents += fresh.length;
          }
        }
      }
    }, TIMEOUT);
  }

  it('the series were not vacuous: both rules fired and bbs visited every state', () => {
    expect(bbsEvents).toBeGreaterThan(0);
    expect(ignEvents).toBeGreaterThan(0);
    expect([...bbsStates].sort()).toEqual([BBS_NONE, BBS_FORMING, BBS_BROKEN]);
  });
});
