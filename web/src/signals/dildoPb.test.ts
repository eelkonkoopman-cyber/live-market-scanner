// Unit tests for the GREEN DILDO -> 9 EMA PULLBACK rule (dildoPb.ts). The Go
// port is pinned to the same rule by the golden vectors (dildoPb.golden.test.ts
// and internal/dildopb/golden_test.go); these tests pin the RULE itself.
import { describe, expect, it } from 'vitest';
import {
  DEFAULT_DILDO_PARAMS,
  PB_CANCELLED,
  PB_CONFIRMED,
  PB_EXPIRED,
  PB_NONE,
  PB_TOUCHING,
  PB_WATCHING,
  classifyDildo,
  dildoPriors,
  evaluateDildo,
  fmtDvol,
  type DildoParams,
  type DildoResult,
} from './dildoPb';
import { emaSeries } from './emaFlag';
import { fmtEt } from './shortFade';
import { toCandle } from '../chartAgg';
import type { Bar } from '../types';
import type { Candle } from '../vendor/blackthorn-charts';

declare const process: { env: Record<string, string | undefined> };
interface NodeFs {
  readFileSync(path: URL, encoding: 'utf8'): string;
  readdirSync(path: URL): string[];
}
const fsSpec = 'node:fs';
const fs = (await import(/* @vite-ignore */ fsSpec)) as NodeFs;

const MIN = 60_000;
const T0 = Date.UTC(2026, 9, 7, 13, 0); // 2026-10-07 09:00 ET (EDT)

function nextUp(x: number): number {
  const f = new Float64Array([x]);
  const u = new BigInt64Array(f.buffer);
  u[0] = u[0]! + (x >= 0 ? 1n : -1n);
  return f[0]!;
}

function bar(i: number, o: number, h: number, l: number, c: number, v: number, t0 = T0): Candle {
  return { ts: t0 + i * MIN, open: o, high: h, low: l, close: c, volume: v };
}

// 12 quiet bars at 1.00, then a dildo 1.00 -> 1.20 on 100k shares (body 20 %, priors' mean body 0 -> bodyX ∞,
// volX 100, $120k), then bars that hold up at ~1.20 while the EMA climbs (1.04, 1.074, 1.0992, 1.11936 ...).
// The floor (mid) is 1.10; at bar 16 the touch level is ema[15]·1.01 ≈ 1.1306.
function base(): Candle[] {
  const cs: Candle[] = [];
  for (let i = 0; i < 12; i++) cs.push(bar(i, 1, 1.005, 0.995, 1, 1000));
  cs.push(bar(12, 1, 1.205, 0.995, 1.2, 100_000));
  cs.push(bar(13, 1.2, 1.22, 1.19, 1.21, 5000));
  cs.push(bar(14, 1.21, 1.22, 1.19, 1.2, 5000));
  cs.push(bar(15, 1.2, 1.22, 1.19, 1.2, 5000));
  return cs;
}
const DILDO_TS = T0 + 12 * MIN;

const closedAt = (cs: Candle[]) => cs[cs.length - 1]!.ts + 10 * MIN;
const run = (cs: Candle[], params: Partial<DildoParams> = {}, nowMs = closedAt(cs)): DildoResult => evaluateDildo({ candles: cs, tfMs: MIN, nowMs, params });
const kinds = (r: DildoResult) => r.events.map((e) => `${(e.ts - T0) / MIN}:${e.kind}${e.reason ? `/${e.reason}` : ''}`);

describe('classifyDildo: every gate flips at its boundary', () => {
  const priors = { n: 10, sumAbsBody: 0.5, sumVol: 100_000 }; // mean body 0.05, mean volume 10 000
  const green: Candle = { ts: T0, open: 1, high: 1.25, low: 0.98, close: 1.24, volume: 120_000 };
  const loose: DildoParams = { ...DEFAULT_DILDO_PARAMS, minBodyPct: 0, minBodyX: 0, maxTopPct: 100, minVolX: 0, minPrice: 0, minDollarVol: 0, minPriorBars: 0 };
  const measured = classifyDildo(green, priors, loose)!;

  it('measures the candle', () => {
    expect(measured.bodyPct).toBeCloseTo(24, 9);
    expect(measured.bodyX).toBeCloseTo(4.8, 9);
    expect(measured.volX).toBe(12);
    expect(measured.topPct).toBeCloseTo((0.01 / 0.27) * 100, 9);
    expect(measured.dollarVol).toBeCloseTo(148_800, 6);
  });

  // Each threshold set EXACTLY to the measured value passes (>= / <=), one ulp stricter fails.
  const gates: [keyof DildoParams, number, number][] = [
    ['minBodyPct', measured.bodyPct, nextUp(measured.bodyPct)],
    ['minBodyX', measured.bodyX, nextUp(measured.bodyX)],
    ['minVolX', measured.volX, nextUp(measured.volX)],
    ['minDollarVol', measured.dollarVol, nextUp(measured.dollarVol)],
    ['minPrice', green.close, nextUp(green.close)],
  ];
  for (const [k, at, over] of gates) {
    it(`${k}: inclusive at the boundary, one ulp over fails`, () => {
      expect(classifyDildo(green, priors, { ...loose, [k]: at }), `${k} = measured`).not.toBeNull();
      expect(classifyDildo(green, priors, { ...loose, [k]: over }), `${k} = measured + 1 ulp`).toBeNull();
    });
  }
  it('maxTopPct: inclusive at the boundary, one ulp under fails', () => {
    const t = measured.topPct;
    expect(classifyDildo(green, priors, { ...loose, maxTopPct: t })).not.toBeNull();
    const f = new Float64Array([t]);
    const u = new BigInt64Array(f.buffer);
    u[0] = u[0]! - 1n;
    expect(classifyDildo(green, priors, { ...loose, maxTopPct: f[0]! })).toBeNull();
  });
  it('the defaults at their round numbers: body 8 %, bodyX 4, top 20 %, volX 5, $0.20, $100k', () => {
    // body exactly 8 % of an open of 25 (2/25·100 is 8 in doubles), 2 / 0.5 = 4x, 5 x 20 000, top 0.5/2.5 = 20 %
    const c: Candle = { ts: T0, open: 25, high: 27.5, low: 25, close: 27, volume: 100_000 };
    const pr = { n: 10, sumAbsBody: 5, sumVol: 200_000 };
    const hit = classifyDildo(c, pr, DEFAULT_DILDO_PARAMS)!;
    expect(hit).not.toBeNull();
    expect(hit.bodyPct).toBe(8);
    expect(hit.bodyX).toBe(4);
    expect(hit.volX).toBe(5);
    expect(hit.topPct).toBe(20);
    expect(classifyDildo({ ...c, volume: 99_999 }, pr, DEFAULT_DILDO_PARAMS), 'volX just under 5').toBeNull();
    expect(classifyDildo(c, { ...pr, sumAbsBody: 5.0001 }, DEFAULT_DILDO_PARAMS), 'bodyX just under 4').toBeNull();
    expect(classifyDildo({ ...c, high: 27.5001 }, pr, DEFAULT_DILDO_PARAMS), 'top just over 20 %').toBeNull();
    // price 0.20 and $100k exactly: 0.2 -> 0.25 (+25 %) on 500 000 shares = $125k; at $0.20 close... use a 0.16 open
    const cheap: Candle = { ts: T0, open: 0.16, high: 0.2, low: 0.16, close: 0.2, volume: 500_000 };
    expect(classifyDildo(cheap, { n: 3, sumAbsBody: 0, sumVol: 0 }, DEFAULT_DILDO_PARAMS), 'close 0.20 passes the price floor').not.toBeNull();
    expect(classifyDildo({ ...cheap, close: 0.1999, high: 0.1999 }, { n: 3, sumAbsBody: 0, sumVol: 0 }, DEFAULT_DILDO_PARAMS), 'close 0.1999 fails').toBeNull();
    expect(classifyDildo({ ...cheap, volume: 499_999 }, { n: 3, sumAbsBody: 0, sumVol: 0 }, DEFAULT_DILDO_PARAMS), '$99,999.80 fails').toBeNull();
  });
  it('2 vs 3 prior bars', () => {
    const thin = { n: 3, sumAbsBody: 0.03, sumVol: 3000 };
    expect(classifyDildo(green, thin, DEFAULT_DILDO_PARAMS)).not.toBeNull();
    expect(classifyDildo(green, { ...thin, n: 2 }, DEFAULT_DILDO_PARAMS)).toBeNull();
  });
  it('a red bar, a doji, h == l and an open of 0 are never a dildo', () => {
    expect(classifyDildo({ ...green, close: 0.99 }, priors, loose)).toBeNull();
    expect(classifyDildo({ ...green, close: 1 }, priors, loose)).toBeNull();
    expect(classifyDildo({ ...green, high: 1.24, low: 1.24, open: 1.24 }, priors, loose)).toBeNull();
    expect(classifyDildo({ ...green, open: 0 }, priors, loose)).toBeNull();
  });
  it('a prior mean of 0 passes with an infinite multiple', () => {
    const hit = classifyDildo(green, { n: 5, sumAbsBody: 0, sumVol: 0 }, DEFAULT_DILDO_PARAMS)!;
    expect(hit.bodyX).toBe(Infinity);
    expect(hit.volX).toBe(Infinity);
  });
  it('dildoPriors sums oldest -> newest over [from, to)', () => {
    const cs = [bar(0, 1, 1, 1, 1.1, 10), bar(1, 1, 1, 1, 0.8, 20), bar(2, 1, 1, 1, 1, 30)];
    const pr = dildoPriors(cs, 0, 2);
    expect(pr.n).toBe(2);
    expect(pr.sumAbsBody).toBe(Math.abs(1.1 - 1) + Math.abs(0.8 - 1));
    expect(pr.sumVol).toBe(30);
    expect(dildoPriors(cs, 2, 2)).toEqual({ n: 0, sumAbsBody: 0, sumVol: 0 });
  });
});

describe('evaluateDildo: the pullback', () => {
  it('the base series: a dildo at bar 12, the EMA climbing under the hold', () => {
    const r = run(base());
    expect(kinds(r)).toEqual(['12:dildo']);
    expect(r.lastDildoTs).toBe(DILDO_TS);
    expect(r.floor).toBeCloseTo(1.1, 12);
    expect(r.deadlineTs).toBe(DILDO_TS + 20 * MIN);
    expect(r.pbState).toBe(PB_WATCHING);
    const ema = emaSeries(base().map((c) => c.close), 9);
    expect(ema[15]).toBeCloseTo(1.11936, 9);
    expect(r.events[0]!.note).toBe(`${fmtEt(DILDO_TS)} green dildo +20.0% body (∞x prior), 100x volume, $120k; floor 1.10`);
    expect(r.events[0]!.marker).toBe('buy');
  });

  it('touch -> confirm: the 9 EMA PULLBACK at the touch bar close', () => {
    const cs = [...base(), bar(16, 1.2, 1.2, 1.12, 1.18, 8000)];
    const r = run(cs);
    expect(kinds(r)).toEqual(['12:dildo', '16:dildoPb']);
    const pb = r.events[1]!;
    expect(pb.price).toBe(1.18);
    expect(pb.label).toBe('9 EMA PULLBACK');
    expect(pb.marker).toBe('buy');
    const ema16 = emaSeries(cs.map((c) => c.close), 9)[16]!;
    expect(pb.emaDistPct).toBe(((1.18 - ema16) / ema16) * 100);
    expect(pb.note).toBe(`${fmtEt(cs[16]!.ts)} pullback to the 9 EMA held: close 1.18 vs EMA 1.13 (+4.3%), 4 min after the dildo`);
    expect(r.pbState).toBe(PB_CONFIRMED);
    expect(r.pbTs).toBe(cs[16]!.ts);
    expect(r.touchTs).toBe(cs[16]!.ts);
    expect(r.pbPx).toBe(1.18);
  });

  it("touch with low <= floor -> PB CANCELLED 'floor'", () => {
    const r = run([...base(), bar(16, 1.2, 1.2, 1.09, 1.18, 8000)]);
    expect(kinds(r)).toEqual(['12:dildo', '16:dildoCancel/floor']);
    expect(r.events[1]!.marker).toBeUndefined();
    expect(r.events[1]!.price).toBe(1.09);
    expect(r.events[1]!.note).toMatch(/pullback cancelled: low 1\.09 broke the floor 1\.10$/);
    expect(r.pbState).toBe(PB_CANCELLED);
    expect(r.pbTs).toBeNull();
  });

  it("touch with close < EMA x 0.99 -> PB CANCELLED 'ema'", () => {
    const r = run([...base(), bar(16, 1.2, 1.2, 1.101, 1.105, 8000)]);
    expect(kinds(r)).toEqual(['12:dildo', '16:dildoCancel/ema']);
    expect(r.events[1]!.price).toBe(1.105);
    expect(r.events[1]!.note).toMatch(/pullback cancelled: closed 1\.0% under the 9 EMA$/);
    expect(r.pbState).toBe(PB_CANCELLED);
  });

  it("the floor breaks before any touch -> PB CANCELLED 'faded'", () => {
    // bar 13's touch level is ema[12]·1.01 = 1.0504, under the floor 1.10: a low of 1.08 breaks the floor without touching
    const cs = base().slice(0, 13);
    cs.push(bar(13, 1.2, 1.2, 1.08, 1.15, 5000));
    const r = run(cs);
    expect(kinds(r)).toEqual(['12:dildo', '13:dildoCancel/faded']);
    expect(r.events[1]!.note).toMatch(/pullback cancelled: faded below the floor 1\.10 before reaching the 9 EMA$/);
    expect(r.pbState).toBe(PB_CANCELLED);
  });

  it('expiry at 20 minutes: a later bar past the window, or a quiet name once nowMs >= deadline + 60 s', () => {
    const cs = base();
    // a steady climb of 0.05 a bar: the EMA lags ~0.2 under it, so no bar comes near the touch level
    for (let i = 16; i <= 32; i++) {
      const c = 1.3 + 0.05 * (i - 16);
      cs.push(bar(i, c - 0.04, c + 0.005, c - 0.045, c, 2000));
    }
    const atDeadline = run(cs); // bar 32 = deadline (12 + 20): still inside the window
    expect(atDeadline.events.length).toBe(1);
    expect(atDeadline.pbState).toBe(PB_EXPIRED); // nowMs = bar 32 + 10 min >= deadline + 60 s
    expect(run(cs, {}, cs[cs.length - 1]!.ts + 30_000).pbState, 'the deadline bar still forming').toBe(PB_WATCHING);
    expect(run(cs, {}, DILDO_TS + 20 * MIN + 59_999).pbState).toBe(PB_WATCHING);
    expect(run(cs, {}, DILDO_TS + 21 * MIN).pbState).toBe(PB_EXPIRED);
    // a touch on the first bar past the window is ignored
    const late = [...cs, bar(33, 2.1, 2.1, 1.5, 1.9, 8000)];
    expect(kinds(run(late))).toEqual(['12:dildo']);
    expect(run(late).pbState).toBe(PB_EXPIRED);
    // ... and one ON the deadline bar counts
    const ema = emaSeries(cs.map((c) => c.close), 9);
    const onTime = [...cs.slice(0, cs.length - 1), bar(32, 2.05, 2.06, ema[31]!, 2.0, 8000)];
    expect(late[33]!.low, 'the late bar would touch').toBeLessThanOrEqual(ema[32]! * 1.01);
    expect(kinds(run(onTime))).toEqual(['12:dildo', '32:dildoPb']);
  });

  it('a new dildo inside the window supersedes the pending anchor', () => {
    const cs = base();
    cs.push(bar(16, 1.2, 1.45, 1.2, 1.44, 200_000)); // a second dildo (+20 %, on far more volume than its priors)
    cs.push(bar(17, 1.44, 1.45, 1.4, 1.42, 5000));
    const r = run(cs);
    expect(kinds(r)).toEqual(['12:dildo', '16:dildo']);
    expect(r.lastDildoTs).toBe(cs[16]!.ts);
    expect(r.floor).toBeCloseTo(1.32, 12);
    expect(r.deadlineTs).toBe(cs[16]!.ts + 20 * MIN);
    // the old anchor's floor (1.10) no longer decides anything: a low of 1.30 breaks the NEW floor (1.32)
    const r2 = run([...cs, bar(18, 1.42, 1.42, 1.3, 1.35, 5000)]);
    expect(kinds(r2)).toEqual(['12:dildo', '16:dildo', '18:dildoCancel/faded']);
  });

  it('an unknown EMA skips the pullback checks of that bar (the harness), the bar may still be a dildo', () => {
    // a dildo at bar 3 (3 priors); bars 4-8 have no ema[j-1] yet (period 9): their floor breaks are not judged
    const cs: Candle[] = [];
    for (let i = 0; i < 3; i++) cs.push(bar(i, 1, 1.005, 0.995, 1, 1000));
    cs.push(bar(3, 1, 1.205, 0.995, 1.2, 100_000));
    cs.push(bar(4, 1.2, 1.2, 1.0, 1.05, 5000)); // low 1.00 <= floor 1.10, but ema[3] is null
    const r = run(cs);
    expect(kinds(r)).toEqual(['3:dildo']);
    expect(r.pbState).toBe(PB_WATCHING);
  });

  it('a 5-minute timeframe, or enabled false, returns an empty result', () => {
    const cs = [...base(), bar(16, 1.2, 1.2, 1.12, 1.18, 8000)];
    for (const r of [evaluateDildo({ candles: cs, tfMs: 5 * MIN, nowMs: closedAt(cs) }), run(cs, { enabled: false }), evaluateDildo({ candles: [], tfMs: MIN, nowMs: T0 })]) {
      expect(r.events).toEqual([]);
      expect(r.pbState).toBe(PB_NONE);
      expect(r.lastDildoTs).toBeNull();
    }
  });

  it("floor 'open' uses the dildo bar's open", () => {
    expect(run(base(), { floor: 'open' }).floor).toBe(1);
  });

  it('the Result reports the LAST ET day only', () => {
    // the same series a day earlier, then one quiet bar today: the dildo is yesterday's
    const y = base().map((c) => ({ ...c, ts: c.ts - 24 * 60 * MIN }));
    const r = run([...y, bar(0, 1.2, 1.2, 1.2, 1.2, 100)]);
    expect(kinds(r).length).toBe(1);
    expect(r.lastDildoTs).toBeNull();
    expect(r.pbState).toBe(PB_NONE);
  });

  it('prior bars may reach back into the previous day (the 04:00 case)', () => {
    const y = [];
    for (let i = 0; i < 10; i++) y.push(bar(i, 1, 1.005, 0.995, 1, 1000, Date.UTC(2026, 9, 6, 23, 0))); // 19:00-19:09 ET yesterday
    const today = Date.UTC(2026, 9, 7, 8, 0); // 04:00 ET
    const r = run([...y, bar(0, 1, 1.205, 0.995, 1.2, 100_000, today)]);
    expect(r.events.map((e) => e.kind)).toEqual(['dildo']);
    expect(r.lastDildoTs).toBe(today);
  });
});

describe('evaluateDildo: the forming bar', () => {
  const touchBar = bar(16, 1.2, 1.2, 1.12, 1.18, 8000);
  const nowIn = (b: Candle) => b.ts + 30_000;

  it('a forming touch -> TOUCHING (2), touchTs = the forming bar, no event', () => {
    const cs = [...base(), touchBar];
    const r = run(cs, {}, nowIn(touchBar));
    expect(kinds(r)).toEqual(['12:dildo']);
    expect(r.pbState).toBe(PB_TOUCHING);
    expect(r.touchTs).toBe(touchBar.ts);
    expect(r.pbTs).toBeNull();
  });

  it('a forming low <= floor -> 4 (final), never TOUCHING', () => {
    const r = run([...base(), { ...touchBar, low: 1.09 }], {}, nowIn(touchBar));
    expect(kinds(r)).toEqual(['12:dildo']);
    expect(r.pbState).toBe(PB_CANCELLED);
    expect(r.touchTs).toBeNull();
  });

  it('as the forming bar prints its low only falls: 1 -> 2 -> 4, never 2 -> 1, and never an event', () => {
    const states: number[] = [];
    for (const low of [1.2, 1.18, 1.15, 1.131, 1.13, 1.12, 1.11, 1.1, 1.09]) {
      const r = run([...base(), { ...touchBar, low, close: Math.max(low, 1.15) }], {}, nowIn(touchBar));
      expect(r.events.length).toBe(1);
      states.push(r.pbState);
    }
    expect(states).toEqual([1, 1, 1, 1, 2, 2, 2, 4, 4]);
  });
});

// ─── Real fixtures ───────────────────────────────────────────────────────

const FIXTURES = new URL('./fixtures/dildo/', import.meta.url);
function fixture(name: string): Candle[] {
  const fx = JSON.parse(fs.readFileSync(new URL(`${name}.json`, FIXTURES), 'utf8')) as { bars: Bar[] };
  return fx.bars.map(toCandle);
}
const etEvents = (r: DildoResult, day: string) =>
  r.events.filter((e) => new Intl.DateTimeFormat('en-CA', { timeZone: 'America/New_York' }).format(new Date(e.ts)) === day).map((e) => `${fmtEt(e.ts)} ${e.kind}${e.reason ? `/${e.reason}` : ''}`);

describe('real fixtures (fixtures/dildo/)', () => {
  it('MB 2026-08-07: the reference dildo 16:51 (+8.6 %) -> 9 EMA PULLBACK 17:01 at 7.93 (after hours)', () => {
    const cs = fixture('dildo-mb-2026-08-07');
    const r = run(cs);
    expect(etEvents(r, '2026-08-07')).toEqual(['05:03 dildo', '05:09 dildoCancel/faded', '15:08 dildo', '15:17 dildoPb', '16:51 dildo', '17:01 dildoPb']);
    const d = r.events[r.events.length - 2]!;
    const pb = r.events[r.events.length - 1]!;
    expect(d.note).toMatch(/^16:51 green dildo \+8\.6% body \(42\.0x prior\), 8x volume, \$112k; floor 7\.67$/);
    expect(pb.price).toBe(7.93);
    expect(r.pbState).toBe(PB_CONFIRMED);
    expect(r.pbPx).toBe(7.93);
    expect(r.lastDildoBodyX!.toFixed(1)).toBe('42.0'); // harness detail: 42.0x body, 7.8x vol, $112k
    expect(r.lastDildoVolX!.toFixed(1)).toBe('7.8');
    // the harness's 9 EMA at the dildo close: 7.4253
    const ema = emaSeries(cs.map((c) => c.close), 9);
    expect(ema[cs.findIndex((c) => c.ts === d.ts)]!.toFixed(4)).toBe('7.4253');
  });

  it('PDSB 2026-09-08: 09:30 0.4880 -> 0.5782 (sub-$1) -> 9 EMA PULLBACK 09:36 at 0.6079', () => {
    const r = run(fixture('dildo-pdsb-2026-09-08'));
    expect(etEvents(r, '2026-09-08')).toEqual(['08:30 dildo', '08:31 dildo', '08:42 dildoCancel/ema', '09:30 dildo', '09:36 dildoPb']);
    expect(r.events[3]!.note).toMatch(/^09:30 green dildo \+18\.5% body \(16\.3x prior\), 7x volume, \$4\.3M; floor 0\.5331$/);
    expect(r.events[4]!.price).toBe(0.6079);
  });

  it('CRMT 2026-07-14: 09:30 2.9250 -> 3.3090 -> 9 EMA PULLBACK 09:39 at 3.5301', () => {
    const cs = fixture('dildo-crmt-2026-07-14');
    const r = run(cs);
    expect(etEvents(r, '2026-07-14')).toEqual(['09:30 dildo', '09:39 dildoPb']);
    expect(r.events[0]!.note).toMatch(/^09:30 green dildo \+13\.1% body \(15\.1x prior\), 54x volume, \$174k; floor 3\.12$/);
    expect(r.events[1]!.price).toBe(3.5301);
    const ema = emaSeries(cs.map((c) => c.close), 9);
    expect(ema[cs.findIndex((c) => c.ts === r.events[0]!.ts)]!.toFixed(4)).toBe('3.0176');
  });

  it('VBIO 2026-08-12: 09:56 +21.5 % -> PB CANCELLED (died before any touch)', () => {
    const r = run(fixture('dildo-vbio-2026-08-12'));
    expect(etEvents(r, '2026-08-12')).toEqual(['09:34 dildo', '09:45 dildo', '09:56 dildo', '10:01 dildoCancel/faded']);
    expect(r.events[2]!.note).toMatch(/^09:56 green dildo \+21\.5% body \(4\.6x prior\), 6x volume, \$4\.3M; floor 1\.19$/);
    expect(r.pbState).toBe(PB_CANCELLED);
  });

  it('WVVIP 2026-08-26: 04:01 (priors from the previous evening) and 04:02 supersedes it -> 04:09 cancelled (floor)', () => {
    const cs = fixture('dildo-wvvip-2026-08-26');
    const r = run(cs);
    expect(etEvents(r, '2026-08-26')).toEqual(['04:01 dildo', '04:02 dildo', '04:09 dildoCancel/floor']);
    // the 04:01 dildo's priors are all yesterday's post-market bars
    const i = cs.findIndex((c) => c.ts === r.events.find((e) => fmtEt(e.ts) === '04:01')!.ts);
    expect(new Intl.DateTimeFormat('en-CA', { timeZone: 'America/New_York' }).format(new Date(cs[i - 2]!.ts))).toBe('2026-08-25');
    expect(r.events.find((e) => fmtEt(e.ts) === '04:01')!.note).toMatch(/^04:01 green dildo \+38\.8% body \(48\.1x prior\), 8x volume, \$434k/);
    // judged on its own (no 04:02 anchor) the 04:01 dildo pulls back at 04:09 at 7.9499, as the calibration harness has it
    const without0402 = cs.filter((c) => fmtEt(c.ts) !== '04:02' || c.ts < cs[i]!.ts);
    expect(etEvents(run(without0402), '2026-08-26')).toEqual(['04:01 dildo', '04:09 dildoPb']);
    expect(run(without0402).pbPx).toBe(7.9499);
  });
});

// ─── The append-only property ────────────────────────────────────────────

describe('append-only: every prefix (closed or forming at partial OHLC) is a prefix of the full run', () => {
  const series: [string, Candle[]][] = [
    ['base+touch', [...base(), bar(16, 1.2, 1.2, 1.12, 1.18, 8000), bar(17, 1.18, 1.25, 1.17, 1.24, 6000)]],
    ['mb', fixture('dildo-mb-2026-08-07')],
    ['pdsb', fixture('dildo-pdsb-2026-09-08')],
    ['wvvip', fixture('dildo-wvvip-2026-08-26')],
  ];
  const sig = (r: DildoResult) => r.events.map((e) => `${e.ts}|${e.kind}|${e.label}|${e.price}|${e.reason ?? ''}|${e.note}`);
  for (const [name, cs] of series) {
    it(name, () => {
      const full = sig(run(cs));
      const from = Math.max(1, cs.length - 400);
      for (let k = from; k <= cs.length; k++) {
        const pre = cs.slice(0, k);
        const closed = sig(run(pre));
        expect(full.slice(0, closed.length), `${name} prefix ${k}`).toEqual(closed);
        // the k-th bar still forming, at partial OHLC
        const b = cs[k - 1]!;
        for (const f of [0.2, 0.6]) {
          const partial: Candle = { ...b, high: b.open + (b.high - b.open) * f, low: b.open - (b.open - b.low) * f, close: b.open + (b.close - b.open) * f, volume: Math.round(b.volume * f) };
          const r = run([...cs.slice(0, k - 1), partial], {}, b.ts + 30_000);
          const ev = sig(r);
          expect(full.slice(0, ev.length), `${name} forming ${k} @${f}`).toEqual(ev);
          expect(r.events.every((e) => e.ts < b.ts), 'a forming bar never emits').toBe(true);
        }
      }
    });
  }
});

describe('formatting', () => {
  it('fmtDvol', () => {
    expect(fmtDvol(716_518.08)).toBe('$717k');
    expect(fmtDvol(999_499)).toBe('$999k');
    expect(fmtDvol(1_487_830.05)).toBe('$1.5M');
    expect(fmtDvol(4_303_495)).toBe('$4.3M');
  });
  it('UPDATE_GOLDEN is not set by accident in a normal run', () => {
    expect(['1', undefined]).toContain(process.env.UPDATE_GOLDEN);
  });
});
