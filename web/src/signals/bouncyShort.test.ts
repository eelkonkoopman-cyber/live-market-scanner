import { describe, expect, it } from 'vitest';
import { BBS_BROKEN, BBS_FORMING, BBS_NONE, DEFAULT_BBS_PARAMS, analyzeBbs, evaluateBbs } from './bouncyShort';
import { aaoiBars, etMs14, flatHighsBars, stairBars, twoTouchBars } from './bouncyShort.fixtures';
import type { Candle } from '../vendor/blackthorn-charts';

const closed = (c: Candle[]) => evaluateBbs({ candles: c, tfMs: 60_000, nowMs: c[c.length - 1]!.ts + 600_000 });

describe('bouncy ball short', () => {
  it('fires once on the AAOI-shaped break at 10:40 with support 115.85 and four lower highs', () => {
    const r = closed(aaoiBars());
    expect(r.events.map((e) => e.ts)).toEqual([etMs14(10, 40)]);
    const e = r.events[0]!;
    expect(e.price).toBe(115.2);
    expect(e.support).toBe(115.85);
    expect(e.touches).toBeGreaterThanOrEqual(4);
    expect(e.highs).toBe(4);
    expect(e.firstHigh).toBe(119.9);
    expect(e.lastHigh).toBe(116.8);
    expect(e.note).toContain('bouncy ball short');
    expect(r.state).toBe(BBS_BROKEN);
    expect(r.lastTs).toBe(etMs14(10, 40));
  });

  it('is FORMING at the 10:35 touch, not at 10:20 (one lower high only)', () => {
    const bars = aaoiBars();
    const upTo = (h: number, m: number) => closed(bars.filter((b) => b.ts <= etMs14(h, m)));
    expect(upTo(10, 20).state).toBe(BBS_NONE);
    const f = upTo(10, 35);
    expect(f.state).toBe(BBS_FORMING);
    expect(f.support).toBe(115.85);
    expect(f.events).toEqual([]);
  });

  it('ignores the forming last bar', () => {
    const bars = aaoiBars().filter((b) => b.ts <= etMs14(10, 40));
    expect(evaluateBbs({ candles: bars, tfMs: 60_000, nowMs: etMs14(10, 40) + 30_000 }).events).toEqual([]);
  });

  it('negatives: flat highs, a stair of lower lows, two touches', () => {
    expect(closed(flatHighsBars()).events).toEqual([]);
    expect(closed(stairBars()).events).toEqual([]);
    expect(closed(twoTouchBars()).events).toEqual([]);
  });

  it('analyze: a base that fails falls back to the whole window', () => {
    const hs = [10, 10.5, 10.6, 10.6, 10.6];
    const ls = [9.0, 10.0, 10.0, 10.0, 10.0];
    const pt = analyzeBbs(hs, ls, 0, 5, DEFAULT_BBS_PARAMS);
    expect(pt.support).toBe(9);
    expect(pt.ok).toBe(false);
  });

  it('fires on a gap-down break straight from a bounce bar', () => {
    const bars = aaoiBars().filter((b) => b.ts <= etMs14(10, 38));
    const t = etMs14(10, 39);
    bars.push({ ts: t, open: 116.7, high: 116.75, low: 115.0, close: 115.1, volume: 50_000 });
    const r = closed(bars);
    expect(r.events.map((e) => e.ts)).toEqual([t]);
    expect(r.events[0]!.support).toBe(115.85);
  });

  it('is FORMING on a bounce bar within 1% of support', () => {
    const bars = aaoiBars().filter((b) => b.ts <= etMs14(10, 36));
    expect(closed(bars).state).toBe(BBS_FORMING);
  });

  it('only 1-minute', () => {
    const c = aaoiBars();
    expect(evaluateBbs({ candles: c, tfMs: 300_000, nowMs: Infinity }).events).toEqual([]);
  });
});
