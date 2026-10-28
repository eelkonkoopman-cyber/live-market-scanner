import { describe, expect, it } from 'vitest';
import { DEFAULT_IGNITION_PARAMS, evaluateIgnition, judgeIgnition } from './ignition';
import { belowDayHighBars, bigBodyBars, ncplBars, noVolumeBars, wideBaseBars } from './ignition.fixtures';
import { etMs14 } from './bouncyShort.fixtures';
import type { Candle } from '../vendor/blackthorn-charts';

const run = (candles: Candle[]) => evaluateIgnition({ candles, tfMs: 60_000, nowMs: Infinity });

describe('ignition rule', () => {
  it('fires once on the NCPL-shaped 09:06 candle, not on the dildo after it', () => {
    const ev = run(ncplBars());
    expect(ev.map((e) => e.ts)).toEqual([etMs14(9, 6)]);
    const e = ev[0]!;
    expect(e.price).toBe(1.88);
    expect(e.bodyPct).toBeCloseTo(5.03, 2);
    expect(e.volX).toBe(15);
    expect(e.baseHigh).toBe(1.8);
    expect(e.marker).toBe('buy');
    expect(e.note).toContain('ignition');
  });

  it('negatives: wide base, a dildo-sized body, no volume spike, below the day high', () => {
    expect(run(wideBaseBars())).toEqual([]);
    expect(run(bigBodyBars())).toEqual([]);
    expect(run(noVolumeBars())).toEqual([]);
    expect(run(belowDayHighBars())).toEqual([]);
  });

  it('the forming last candle is ignored', () => {
    const bars = ncplBars().slice(0, 27);
    expect(evaluateIgnition({ candles: bars, tfMs: 60_000, nowMs: bars[26]!.ts + 30_000 })).toEqual([]);
    expect(evaluateIgnition({ candles: bars, tfMs: 60_000, nowMs: bars[26]!.ts + 60_000 })).toHaveLength(1);
  });

  it('one per base and a 10-minute cooldown', () => {
    // two ignition-shaped candles back to back out of the same base: only the first
    const bars = ncplBars().slice(0, 27);
    const t = bars[26]!.ts + 60_000;
    bars.push({ ts: t, open: 1.88, high: 1.96, low: 1.875, close: 1.95, volume: 900_000 });
    expect(run(bars)).toHaveLength(1);
  });

  it('judge reasons', () => {
    const hs = Array(15).fill(1.8);
    const ls = Array(15).fill(1.75);
    const j = (o: number, c: number, v: number, dh = 1.8) => judgeIgnition(hs, ls, 0, 15, 200_000, 10, o, c + 0.01, c, v, dh, DEFAULT_IGNITION_PARAMS).reason;
    expect(j(1.79, 1.88, 300_000)).toBe('ok');
    expect(j(1.79, 1.81, 300_000)).toBe('bodySmall');
    expect(j(1.79, 1.98, 300_000)).toBe('bodyBig');
    expect(j(1.79, 1.88, 60_000)).toBe('noVolume');
    expect(j(1.79, 1.88, 300_000, 2.1)).toBe('notNewHigh');
    expect(j(1.79, 1.88, 20_000)).toBe('lowDvol');
    expect(judgeIgnition(hs, ls, 6, 15, 200_000, 9, 1.79, 1.89, 1.88, 300_000, 0, DEFAULT_IGNITION_PARAMS).reason).toBe('fewBars');
  });
});
