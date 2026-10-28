// Unit tests for the VOLUME CLIMAX -> BOTTOM CONFIRMED rule (climax.ts). The Go
// port is pinned to the same rule by the golden vectors (climax.golden.test.ts
// and internal/climax/golden_test.go); these tests pin the RULE itself.
import { describe, expect, it } from 'vitest';
import {
  CLIMAX_CONFIRMED,
  CLIMAX_EXPIRED,
  CLIMAX_FAILED,
  CLIMAX_NONE,
  CLIMAX_WATCHING,
  DEFAULT_CLIMAX_PARAMS,
  armLatch,
  classifyClimax,
  climaxPriors,
  evaluateClimax,
  stepLatch,
  type ClimaxParams,
  type ClimaxPriors,
  type ClimaxResult,
} from './climax';
import { emaSeries } from './emaFlag';
import { fmtEt } from './shortFade';
import { toCandle } from '../chartAgg';
import type { Bar } from '../types';
import type { Candle } from '../vendor/blackthorn-charts';

interface NodeFs {
  readFileSync(path: URL, encoding: 'utf8'): string;
}
const fsSpec = 'node:fs';
const fs = (await import(/* @vite-ignore */ fsSpec)) as NodeFs;

const MIN = 60_000;
const T0 = Date.UTC(2026, 9, 7, 14, 0); // 2026-10-07 10:00 ET (EDT)

function step(x: number, by: bigint): number {
  const f = new Float64Array([x]);
  const u = new BigInt64Array(f.buffer);
  u[0] = u[0]! + by;
  return f[0]!;
}
const nextUp = (x: number) => step(x, x >= 0 ? 1n : -1n);
const nextDown = (x: number) => step(x, x > 0 ? -1n : 1n);

function bar(i: number, o: number, h: number, l: number, c: number, v: number, t0 = T0): Candle {
  return { ts: t0 + i * MIN, open: o, high: h, low: l, close: c, volume: v };
}

// 12 bars drifting down from 10.0 to ~9.6 on 10 000 shares (the EMA sits above the price), then the climax bar 12:
// open 9.5, low 9.0, close 9.3, high 9.55 on 150 000 shares (15x, the biggest), a 46 % lower wick, $1.4M,
// 10 % under the 10-bar high. Mid = 9.275.
function base(): Candle[] {
  const cs: Candle[] = [];
  for (let i = 0; i < 12; i++) {
    const c = 10 - 0.035 * i;
    cs.push(bar(i, c + 0.02, c + 0.04, c - 0.04, c, 10_000));
  }
  cs.push(bar(12, 9.5, 9.55, 9.0, 9.3, 150_000));
  return cs;
}
const CLIMAX_TS = T0 + 12 * MIN;

const closedAt = (cs: Candle[]) => cs[cs.length - 1]!.ts + 10 * MIN;
const run = (cs: Candle[], params: Partial<ClimaxParams> = {}, nowMs = closedAt(cs)): ClimaxResult => evaluateClimax({ candles: cs, tfMs: MIN, nowMs, params });
const kinds = (r: ClimaxResult, t0 = T0) => r.events.map((e) => `${(e.ts - t0) / MIN}:${e.kind}`);

describe('classifyClimax: every gate flips at its boundary', () => {
  const cs = base();
  const pr = climaxPriors(cs, 2, 12);
  const ema = emaSeries(cs.map((c) => c.close), 9)[12]!;
  const c = cs[12]!;
  const ET = 600; // 10:00
  const loose: ClimaxParams = { ...DEFAULT_CLIMAX_PARAMS, minDropPct: 0, minVolX: 0, requireMaxVol: false, minWickPct: 0, requireBelowEma: false, minPrice: 0, minDollarVol: 0, muteStartMin: 0, muteEndMin: 0 };
  const m = classifyClimax(c, pr, ema, ET, loose)!;

  it('measures the candle', () => {
    expect(pr.n).toBe(10);
    expect(m.dropPct).toBeCloseTo(((pr.hi - 9) / pr.hi) * 100, 12);
    expect(m.volX).toBe(15);
    expect(m.wickPct).toBeCloseTo(((9.3 - 9) / 0.55) * 100, 9);
    expect(m.dollarVol).toBeCloseTo(1_395_000, 4);
    expect(m.mid).toBe(9.275);
    expect(m.emaDistPct!).toBeLessThan(0);
    expect(classifyClimax(c, pr, ema, ET, DEFAULT_CLIMAX_PARAMS), 'it is a climax under the defaults').not.toBeNull();
  });

  const gates: [keyof ClimaxParams, number][] = [
    ['minDropPct', m.dropPct],
    ['minVolX', m.volX],
    ['minWickPct', m.wickPct],
    ['minPrice', c.close],
    ['minDollarVol', m.dollarVol],
  ];
  for (const [k, at] of gates) {
    it(`${k}: inclusive at the boundary, one ulp over fails`, () => {
      expect(classifyClimax(c, pr, ema, ET, { ...loose, [k]: at })).not.toBeNull();
      expect(classifyClimax(c, pr, ema, ET, { ...loose, [k]: nextUp(at) })).toBeNull();
    });
  }

  it('drop exactly 6 % and volX exactly 8 pass under the defaults', () => {
    // priors: high 50, volume 10 000 each; the bar: low 47 ((50 - 47)/50·100 is exactly 6 in doubles), 80 000 shares (8x)
    const flat: ClimaxPriors = { n: 10, hi: 50, lo: 48, sumVol: 100_000, maxVol: 10_000 };
    const b: Candle = { ts: T0, open: 48.5, high: 49, low: 47, close: 48, volume: 80_000 };
    const hit = classifyClimax(b, flat, 49.5, ET, DEFAULT_CLIMAX_PARAMS)!;
    expect(hit).not.toBeNull();
    expect(hit.dropPct).toBe(6);
    expect(hit.volX).toBe(8);
    expect(classifyClimax({ ...b, volume: 79_999 }, flat, 49.5, ET, DEFAULT_CLIMAX_PARAMS), '7.9999x').toBeNull();
    expect(classifyClimax({ ...b, low: nextUp(47) }, flat, 49.5, ET, DEFAULT_CLIMAX_PARAMS), 'a low one ulp higher: just under 6 %').toBeNull();
  });

  it('a prior mean volume of 0 passes (+Infinity)', () => {
    const hit = classifyClimax(c, { ...pr, sumVol: 0, maxVol: 0 }, ema, ET, DEFAULT_CLIMAX_PARAMS)!;
    expect(hit.volX).toBe(Infinity);
  });

  it('requireMaxVol: volume == the biggest prior passes, maxVol + 1 fails', () => {
    expect(classifyClimax(c, { ...pr, maxVol: 150_000 }, ema, ET, DEFAULT_CLIMAX_PARAMS)).not.toBeNull();
    expect(classifyClimax(c, { ...pr, maxVol: 150_001 }, ema, ET, DEFAULT_CLIMAX_PARAMS)).toBeNull();
    expect(classifyClimax(c, { ...pr, maxVol: 150_001 }, ema, ET, { ...DEFAULT_CLIMAX_PARAMS, requireMaxVol: false })).not.toBeNull();
  });

  it('wick exactly 25 % passes', () => {
    // range 9-10 (1), min(open, close) 9.25 -> wick exactly 25 % in doubles
    const b: Candle = { ...c, open: 9.5, high: 10, low: 9, close: 9.25 };
    const hit = classifyClimax(b, pr, ema, ET, DEFAULT_CLIMAX_PARAMS)!;
    expect(hit).not.toBeNull();
    expect(hit.wickPct).toBe(25);
    expect(classifyClimax({ ...b, close: nextDown(9.25) }, pr, ema, ET, DEFAULT_CLIMAX_PARAMS), 'one ulp less wick').toBeNull();
  });

  it('fresh low: low == the priors\' lowest low passes, one tick above fails', () => {
    expect(classifyClimax(c, { ...pr, lo: 9.0 }, ema, ET, DEFAULT_CLIMAX_PARAMS)).not.toBeNull();
    expect(classifyClimax(c, { ...pr, lo: nextDown(9.0) }, ema, ET, DEFAULT_CLIMAX_PARAMS)).toBeNull();
  });

  it('close == EMA fails (strict), an unknown EMA fails, requireBelowEma false passes both', () => {
    expect(classifyClimax(c, pr, 9.3, ET, DEFAULT_CLIMAX_PARAMS)).toBeNull();
    expect(classifyClimax(c, pr, nextUp(9.3), ET, DEFAULT_CLIMAX_PARAMS)).not.toBeNull();
    expect(classifyClimax(c, pr, null, ET, DEFAULT_CLIMAX_PARAMS)).toBeNull();
    expect(classifyClimax(c, pr, NaN, ET, DEFAULT_CLIMAX_PARAMS)).toBeNull();
    const noEma = { ...DEFAULT_CLIMAX_PARAMS, requireBelowEma: false };
    expect(classifyClimax(c, pr, 9.3, ET, noEma)).not.toBeNull();
    const h = classifyClimax(c, pr, null, ET, noEma)!;
    expect(h.emaDistPct).toBeNull();
  });

  it('price 0.50 and $vol 50k are inclusive floors', () => {
    const cheap: Candle = { ts: T0, open: 0.52, high: 0.53, low: 0.48, close: 0.5, volume: 100_000 };
    const cpr: ClimaxPriors = { n: 10, hi: 0.6, lo: 0.49, sumVol: 100_000, maxVol: 10_000 };
    expect(classifyClimax(cheap, cpr, 0.55, ET, DEFAULT_CLIMAX_PARAMS), '$0.50 x 100 000 = $50k').not.toBeNull();
    expect(classifyClimax({ ...cheap, volume: 99_999 }, cpr, 0.55, ET, DEFAULT_CLIMAX_PARAMS)).toBeNull();
    expect(classifyClimax({ ...cheap, close: 0.4999, open: 0.52 }, cpr, 0.55, ET, { ...DEFAULT_CLIMAX_PARAMS, minDollarVol: 0 })).toBeNull();
  });

  it('h == l is never a climax; 9 vs 10 priors', () => {
    expect(classifyClimax({ ...c, high: 9, low: 9, open: 9, close: 9 }, pr, ema, ET, loose)).toBeNull();
    expect(classifyClimax(c, { ...pr, n: 9 }, ema, ET, DEFAULT_CLIMAX_PARAMS)).toBeNull();
  });

  it('the mute: 09:29 and 09:35 judge, 09:30 and 09:34 never', () => {
    for (const [min, ok] of [[569, true], [570, false], [574, false], [575, true]] as const) {
      expect(classifyClimax(c, pr, ema, min, DEFAULT_CLIMAX_PARAMS) !== null, `etMin ${min}`).toBe(ok);
    }
    expect(classifyClimax(c, pr, ema, 572, { ...DEFAULT_CLIMAX_PARAMS, muteStartMin: 575, muteEndMin: 570 }), 'start >= end disables the mute').not.toBeNull();
  });

  it('coreHoursOnly: 09:59 and 16:00 never, 10:00 and 15:59 judge', () => {
    const core = { ...DEFAULT_CLIMAX_PARAMS, coreHoursOnly: true };
    for (const [min, ok] of [[599, false], [600, true], [959, true], [960, false]] as const) {
      expect(classifyClimax(c, pr, ema, min, core) !== null, `etMin ${min}`).toBe(ok);
    }
  });

  it('climaxPriors over [from, to), oldest -> newest', () => {
    const p2 = climaxPriors(cs, 0, 3);
    expect(p2.n).toBe(3);
    expect(p2.hi).toBe(cs[0]!.high);
    expect(p2.lo).toBe(cs[2]!.low);
    expect(p2.sumVol).toBe(30_000);
    expect(p2.maxVol).toBe(10_000);
    const none = climaxPriors(cs, 4, 4);
    expect(none).toEqual({ n: 0, hi: -Infinity, lo: Infinity, sumVol: 0, maxVol: 0 });
  });
});

describe('the latch', () => {
  const hit = { dropPct: 10, volX: 15, wickPct: 40, dollarVol: 1e6, emaDistPct: -2, mid: 9.275 };
  const P = DEFAULT_CLIMAX_PARAMS;

  it('a close EQUAL to mid does not confirm; a low EQUAL to the climax low does not fail', () => {
    const x = armLatch(CLIMAX_TS, 9.0, hit);
    expect(stepLatch(x, CLIMAX_TS + MIN, 9.0, 9.275, P)).toBe(CLIMAX_WATCHING);
    expect(x.bars).toBe(1);
    expect(stepLatch(x, CLIMAX_TS + 2 * MIN, 9.0, nextUp(9.275), P)).toBe(CLIMAX_CONFIRMED);
    expect(x.endTs).toBe(CLIMAX_TS + 2 * MIN);
    expect(x.confPx).toBe(nextUp(9.275));
    // once decided it never moves again
    expect(stepLatch(x, CLIMAX_TS + 3 * MIN, 8, 8, P)).toBe(CLIMAX_CONFIRMED);
    expect(x.bars).toBe(2);
  });

  it('fail before confirm on one bar: an undercut that closes above mid FAILS', () => {
    const x = armLatch(CLIMAX_TS, 9.0, hit);
    expect(stepLatch(x, CLIMAX_TS + MIN, nextDown(9.0), 9.5, P)).toBe(CLIMAX_FAILED);
    expect(x.endTs).toBe(CLIMAX_TS + MIN);
  });

  it('expiry exactly at the 5th bar', () => {
    const x = armLatch(CLIMAX_TS, 9.0, hit);
    for (let k = 1; k <= 4; k++) expect(stepLatch(x, CLIMAX_TS + k * MIN, 9.1, 9.2, P)).toBe(CLIMAX_WATCHING);
    expect(stepLatch(x, CLIMAX_TS + 5 * MIN, 9.1, 9.2, P)).toBe(CLIMAX_EXPIRED);
    expect(x.endTs).toBe(CLIMAX_TS + 5 * MIN);
    expect(x.bars).toBe(5);
  });
});

describe('evaluateClimax', () => {
  it('the base series: VOLUME CLIMAX at bar 12, priced at the low, with its note', () => {
    const r = run(base());
    expect(kinds(r)).toEqual(['12:climax']);
    const e = r.events[0]!;
    expect(e.price).toBe(9.0);
    expect(e.marker).toBe('buy');
    expect(e.label).toBe('VOLUME CLIMAX');
    expect(e.note).toMatch(/^10:12 volume climax: low 9\.00 is \d+\.\d% under the 10-bar high, 15\.0x volume \(biggest of 10\), 55% lower wick, \$1\.4M; bottom confirms on a close above 9\.28 within 5 bars$/);
    expect(r.state).toBe(CLIMAX_WATCHING);
    expect(r.lastClimaxTs).toBe(CLIMAX_TS);
    expect(r.climaxMid).toBe(9.275);
    expect(r.bars).toBe(0);
  });

  it('BOTTOM CONFIRMED on the first close above mid', () => {
    const r = run([...base(), bar(13, 9.3, 9.35, 9.1, 9.25, 50_000), bar(14, 9.25, 9.4, 9.2, 9.35, 40_000)]);
    expect(kinds(r)).toEqual(['12:climax', '14:bottom']);
    const b = r.events[1]!;
    expect(b.price).toBe(9.35);
    expect(b.marker).toBe('buy');
    expect(b.note).toBe(`10:14 bottom confirmed: closed 9.35 above the climax midpoint 9.28, 2 bars after the 10:12 climax; the climax low 9.00 is 3.7% under`);
    expect(r.state).toBe(CLIMAX_CONFIRMED);
    expect(r.confTs).toBe(T0 + 14 * MIN);
    expect(r.confPx).toBe(9.35);
    expect(r.endTs).toBe(T0 + 14 * MIN);
    expect(r.bars).toBe(2);
  });

  it('CLIMAX FAILED when a bar undercuts the low first', () => {
    const r = run([...base(), bar(13, 9.3, 9.5, 8.95, 9.4, 50_000)]);
    expect(kinds(r)).toEqual(['12:climax', '13:climaxFail']);
    expect(r.events[1]!.price).toBe(8.95);
    expect(r.events[1]!.reason).toBe('low');
    expect(r.events[1]!.marker).toBeUndefined();
    expect(r.events[1]!.note).toBe('10:13 climax failed: low 8.95 undercut the 10:12 climax low 9.00');
    expect(r.state).toBe(CLIMAX_FAILED);
    expect(r.failTs).toBe(T0 + 13 * MIN);
  });

  it('expired after 5 bars with neither: state 4, no event', () => {
    const cs = base();
    for (let i = 13; i <= 17; i++) cs.push(bar(i, 9.2, 9.27, 9.05, 9.2, 20_000));
    const r = run(cs);
    expect(kinds(r)).toEqual(['12:climax']);
    expect(r.state).toBe(CLIMAX_EXPIRED);
    expect(r.endTs).toBe(T0 + 17 * MIN);
    expect(r.bars).toBe(5);
    expect(run(cs.slice(0, 17)).state, 'four bars in: still watching').toBe(CLIMAX_WATCHING);
  });

  it('a confirmation never crosses the ET day', () => {
    // a 19:5x climax, then the next bars are tomorrow's
    const eve = Date.UTC(2026, 9, 7, 23, 45); // 19:45 ET
    const cs = base().map((c) => ({ ...c, ts: c.ts - T0 + eve }));
    const tomorrow = Date.UTC(2026, 9, 8, 8, 0); // 04:00 ET
    const all = [...cs, bar(0, 9.3, 9.6, 9.2, 9.5, 50_000, tomorrow)];
    const r = run(all);
    expect(r.events.map((e) => e.kind)).toEqual(['climax']);
    expect(r.lastClimaxTs, 'the last day has no climax').toBeNull();
    expect(r.state).toBe(CLIMAX_NONE);
  });

  it('priors are TODAY\'s bars only: 9 bars today is no climax, even with yesterday\'s bars before them', () => {
    const y = base().slice(0, 12).map((c) => ({ ...c, ts: c.ts - 24 * 60 * MIN }));
    const today = base().slice(3); // bars 3..11 (9 bars) then the climax bar
    const r = run([...y, ...today]);
    expect(r.events.filter((e) => e.ts >= T0).length).toBe(0);
    // with 10 bars today it fires
    expect(run([...y, ...base().slice(2)]).events.filter((e) => e.ts >= T0).map((e) => e.kind)).toEqual(['climax']);
  });

  it('latest supersedes; fail and re-arm on one bar', () => {
    // bar 13 undercuts (fails the bar-12 climax) AND is itself a climax: a fresh low, the biggest volume, a wick
    const cs = [...base(), bar(13, 9.0, 9.05, 8.4, 8.8, 400_000)];
    const r = run(cs);
    expect(kinds(r)).toEqual(['12:climax', '13:climaxFail', '13:climax']);
    expect(r.state).toBe(CLIMAX_WATCHING);
    expect(r.lastClimaxTs).toBe(T0 + 13 * MIN);
    expect(r.climaxLow).toBe(8.4);
    // supersede while still watching (no verdict), here with an EQUAL low, so the old one never fails
    const eq = [...base(), bar(13, 9.2, 9.3, 9.0, 9.1, 400_000)];
    const r2 = run(eq);
    expect(kinds(r2)).toEqual(['12:climax', '13:climax']);
    expect(r2.lastClimaxTs).toBe(T0 + 13 * MIN);
    expect(r2.bars).toBe(0);
  });

  it('the forming bar: an undercut reads FAILED with no event; a close above mid stays watching', () => {
    const cs = base();
    const f = bar(13, 9.3, 9.5, 8.9, 9.4, 50_000);
    const r = run([...cs, f], {}, f.ts + 30_000);
    expect(kinds(r)).toEqual(['12:climax']);
    expect(r.state).toBe(CLIMAX_FAILED);
    expect(r.failTs).toBe(f.ts);
    const up = bar(13, 9.3, 9.5, 9.1, 9.45, 50_000);
    const r2 = run([...cs, up], {}, up.ts + 30_000);
    expect(kinds(r2)).toEqual(['12:climax']);
    expect(r2.state).toBe(CLIMAX_WATCHING);
    expect(r2.confTs).toBeNull();
    expect(r2.bars).toBe(0);
  });

  it('a 5-minute timeframe, or enabled false, returns an empty result', () => {
    for (const r of [evaluateClimax({ candles: base(), tfMs: 5 * MIN, nowMs: closedAt(base()) }), run(base(), { enabled: false }), evaluateClimax({ candles: [], tfMs: MIN, nowMs: T0 })]) {
      expect(r.events).toEqual([]);
      expect(r.state).toBe(CLIMAX_NONE);
    }
  });

  it('the mute and core-hours switch apply to the bar START in ET (EDT and EST)', () => {
    for (const t0 of [Date.UTC(2026, 9, 7, 13, 18), Date.UTC(2026, 11, 7, 14, 18)]) {
      // bar 12 starts 09:30 ET on both dates (EDT: 13:30 UTC, EST: 14:30 UTC)
      const cs = base().map((c) => ({ ...c, ts: c.ts - T0 + t0 }));
      expect(fmtEt(cs[12]!.ts)).toBe('09:30');
      expect(run(cs).events, 'muted').toEqual([]);
      expect(run(cs, { muteStartMin: 0, muteEndMin: 0 }).events.length).toBe(1);
      expect(run(cs, { muteStartMin: 0, muteEndMin: 0, coreHoursOnly: true }).events.length).toBe(0);
    }
  });
});

// ─── Real fixtures ───────────────────────────────────────────────────────

const FIXTURES = new URL('./fixtures/climax/', import.meta.url);
function fixture(name: string): Candle[] {
  const fx = JSON.parse(fs.readFileSync(new URL(`${name}.json`, FIXTURES), 'utf8')) as { bars: Bar[] };
  return fx.bars.map(toCandle);
}
const ev = (r: ClimaxResult) => r.events.map((e) => `${fmtEt(e.ts)} ${e.kind}`);
const climaxAt = (r: ClimaxResult, hm: string) => r.events.find((e) => e.kind === 'climax' && fmtEt(e.ts) === hm)!;

describe('real fixtures (fixtures/climax/), under the defaults', () => {
  it('BIYA 2026-10-07: VOLUME CLIMAX 11:03 -> BOTTOM CONFIRMED 11:04, nothing else all day', () => {
    const cs = fixture('climax-biya-2026-10-07');
    const r = run(cs);
    expect(ev(r)).toEqual(['11:03 climax', '11:04 bottom']);
    const c = r.events[0]!;
    expect(c.price).toBe(1.78);
    expect(c.note).toBe('11:03 volume climax: low 1.78 is 13.6% under the 10-bar high, 9.6x volume (biggest of 10), 34% lower wick, $717k; bottom confirms on a close above 1.87 within 5 bars');
    expect(r.events[1]!.note).toBe('11:04 bottom confirmed: closed 1.88 above the climax midpoint 1.87, 1 bar after the 11:03 climax; the climax low 1.78 is 5.3% under');
    expect(r.dropPct!.toFixed(2)).toBe('13.59');
    expect(r.volX!.toFixed(2)).toBe('9.60');
    expect(r.wickPct!.toFixed(1)).toBe('33.7');
    expect(r.climaxMid).toBeCloseTo(1.8691, 12);
    expect(r.confPx).toBe(1.879);
    expect(r.state).toBe(CLIMAX_CONFIRMED);
    // the bar itself: O 1.95 H 1.9582 L 1.78 C 1.84 V 389,412, close under the EMA 1.9777
    const i = cs.findIndex((x) => x.ts === c.ts);
    expect([cs[i]!.open, cs[i]!.high, cs[i]!.low, cs[i]!.close, cs[i]!.volume]).toEqual([1.95, 1.9582, 1.78, 1.84, 389_412]);
    expect(emaSeries(cs.map((x) => x.close), 9)[i]!.toFixed(4)).toBe('1.9777');
  });

  it('LHSW 2026-09-02: 10:12 -> BOTTOM 10:13; 15:17 -> FAILED 15:18', () => {
    const r = run(fixture('climax-lhsw-2026-09-02'));
    expect(ev(r)).toEqual(['10:12 climax', '10:13 bottom', '15:17 climax', '15:18 climaxFail']);
    const upTo = fixture('climax-lhsw-2026-09-02').filter((c) => c.ts <= climaxAt(r, '10:12').ts);
    const r1012 = run(upTo);
    expect(r1012.dropPct!.toFixed(2)).toBe('18.82');
    expect(r1012.volX!.toFixed(2)).toBe('11.91');
    expect(r1012.wickPct!.toFixed(1)).toBe('33.7');
    expect(r1012.climaxMid).toBeCloseTo(3.01415, 12);
    expect(r.events[1]!.price).toBe(3.3779);
    expect(r.state).toBe(CLIMAX_FAILED);
    expect(r.dropPct!.toFixed(2)).toBe('7.31');
    expect(r.volX!.toFixed(2)).toBe('16.66');
    expect(r.wickPct!.toFixed(1)).toBe('79.3');
  });

  it('CLRO 2026-08-06: 12:26, never confirmed -> expired 12:31 (state 4, no event)', () => {
    const r = run(fixture('climax-clro-2026-08-06'));
    expect(ev(r)).toEqual(['12:26 climax']);
    expect(r.state).toBe(CLIMAX_EXPIRED);
    expect(fmtEt(r.endTs!)).toBe('12:31');
    expect(r.dropPct!.toFixed(2)).toBe('11.23');
    expect(r.volX!.toFixed(2)).toBe('9.09');
    expect(r.wickPct!.toFixed(1)).toBe('44.3');
    expect(r.climaxLow).toBe(8.22);
    expect(r.climaxMid).toBe(8.525);
  });

  it('ARMP 2026-09-14: the 09:30 candidate is muted; 13:51 -> BOTTOM 13:52; 14:17 -> FAILED 14:18', () => {
    const cs = fixture('climax-armp-2026-09-14');
    const r = run(cs);
    expect(ev(r)).toEqual(['13:51 climax', '13:52 bottom', '14:17 climax', '14:18 climaxFail']);
    expect(r.events[1]!.price).toBe(5.28);
    // unmuted, 09:30 is a climax and fails at 09:32 (the calibration's muted candidate)
    const day = Date.UTC(2026, 8, 14, 4, 0); // 2026-09-14 00:00 ET
    const unmuted = run(cs, { muteStartMin: 0, muteEndMin: 0 });
    expect(ev({ ...unmuted, events: unmuted.events.filter((e) => e.ts >= day) }).slice(0, 2)).toEqual(['09:30 climax', '09:32 climaxFail']);
    const r0930 = run(cs.filter((c) => c.ts <= climaxAt(unmuted, '09:30').ts), { muteStartMin: 0, muteEndMin: 0 });
    expect(r0930.dropPct!.toFixed(2)).toBe('7.73');
    expect(r0930.volX!.toFixed(2)).toBe('12.71');
  });

  it('XNDU 2026-09-22 (pre-market): 08:50 -> at 08:51 FAILED and a new climax on the same bar -> FAILED 08:53', () => {
    const r = run(fixture('climax-xndu-2026-09-22'));
    expect(ev(r)).toEqual(['08:50 climax', '08:51 climaxFail', '08:51 climax', '08:53 climaxFail']);
    expect(r.dropPct!.toFixed(2)).toBe('15.81');
    expect(r.volX!.toFixed(2)).toBe('10.49');
    expect(r.events[3]!.price).toBe(5.48);
    expect(r.climaxLow).toBe(5.54);
  });

  it('requireBelowEma false: the same events on all five (every candidate in them is under the EMA)', () => {
    for (const f of ['climax-biya-2026-10-07', 'climax-lhsw-2026-09-02', 'climax-clro-2026-08-06', 'climax-armp-2026-09-14', 'climax-xndu-2026-09-22']) {
      const cs = fixture(f);
      expect(ev(run(cs, { requireBelowEma: false })), f).toEqual(ev(run(cs)));
    }
  });

  it('coreHoursOnly: XNDU has no events; ARMP and LHSW keep their 10:00-15:59 events', () => {
    expect(ev(run(fixture('climax-xndu-2026-09-22'), { coreHoursOnly: true }))).toEqual([]);
    expect(ev(run(fixture('climax-armp-2026-09-14'), { coreHoursOnly: true }))).toEqual(['13:51 climax', '13:52 bottom', '14:17 climax', '14:18 climaxFail']);
    expect(ev(run(fixture('climax-lhsw-2026-09-02'), { coreHoursOnly: true }))).toEqual(['10:12 climax', '10:13 bottom', '15:17 climax', '15:18 climaxFail']);
  });
});

// ─── The append-only property ────────────────────────────────────────────

describe('append-only: every prefix (closed or forming at partial OHLC) is a prefix of the full run', () => {
  const series: [string, Candle[]][] = [
    ['base+fail+rearm', [...base(), bar(13, 9.0, 9.05, 8.4, 8.8, 400_000), bar(14, 8.8, 9.0, 8.7, 8.95, 50_000)]],
    ['biya', fixture('climax-biya-2026-10-07')],
    ['xndu', fixture('climax-xndu-2026-09-22')],
    ['lhsw', fixture('climax-lhsw-2026-09-02')],
  ];
  const sig = (r: ClimaxResult) => r.events.map((e) => `${e.ts}|${e.kind}|${e.label}|${e.price}|${e.reason ?? ''}|${e.note}`);
  for (const [name, cs] of series) {
    it(name, () => {
      const full = sig(run(cs));
      for (let k = Math.max(1, cs.length - 400); k <= cs.length; k++) {
        const closed = sig(run(cs.slice(0, k)));
        expect(full.slice(0, closed.length), `${name} prefix ${k}`).toEqual(closed);
        const b = cs[k - 1]!;
        for (const f of [0.2, 0.6]) {
          const partial: Candle = { ...b, high: b.open + (b.high - b.open) * f, low: b.open - (b.open - b.low) * f, close: b.open + (b.close - b.open) * f, volume: Math.round(b.volume * f) };
          const r = run([...cs.slice(0, k - 1), partial], {}, b.ts + 30_000);
          const e = sig(r);
          expect(full.slice(0, e.length), `${name} forming ${k} @${f}`).toEqual(e);
          expect(r.events.every((x) => x.ts < b.ts), 'a forming bar never emits').toBe(true);
        }
      }
    });
  }
});
