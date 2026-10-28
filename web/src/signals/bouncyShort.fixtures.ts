// Synthetic BOUNCY BALL SHORT fixtures (no network): an AAOI-2026-10-14-shaped
// day built from the trader's numbers, plus the negative shapes. Shared by the
// unit and golden tests (the golden test writes them into bbs-golden.json, which
// Go internal/bbs and internal/engine replay).
import type { Candle } from '../vendor/blackthorn-charts';

const MIN = 60_000;
/** An ET wall-clock minute on 2026-10-14 (EDT, UTC-4). */
export const etMs14 = (h: number, m: number): number => Date.UTC(2026, 9, 14, h + 4, m);

/**
 * pathBars walks closes through the waypoints [hh, mm, close, high?, low?]
 * linearly, one bar per minute: open = the previous close, high/low = the body
 * +-0.10 unless a waypoint pins them. Volume 20 000.
 */
export function pathBars(points: ReadonlyArray<readonly [number, number, number, number?, number?]>): Candle[] {
  const out: Candle[] = [];
  let prev = points[0]![2];
  for (let w = 0; w + 1 < points.length; w++) {
    const a = points[w]!;
    const b = points[w + 1]!;
    const t0 = etMs14(a[0], a[1]);
    const t1 = etMs14(b[0], b[1]);
    const steps = Math.round((t1 - t0) / MIN);
    for (let i = w === 0 ? 0 : 1; i <= steps; i++) {
      const close = i === 0 ? a[2] : Math.round((a[2] + ((b[2] - a[2]) * i) / steps) * 100) / 100;
      const pin = i === steps ? b : i === 0 ? a : null;
      const open = prev;
      const high = pin && pin[3] !== undefined ? pin[3] : Math.round((Math.max(open, close) + 0.1) * 100) / 100;
      const low = pin && pin[4] !== undefined ? pin[4] : Math.round((Math.min(open, close) - 0.1) * 100) / 100;
      out.push({ ts: t0 + i * MIN, open, high: Math.max(high, open, close), low: Math.min(low, open, close), close, volume: 20_000 });
      prev = close;
    }
  }
  return out;
}

const T = 115.85; // the support low
const TC = 115.95; // a touch bar's close

/** AAOI-shaped: touches 09:50/10:05/10:20/10:35, highs 119.9/117.8/117.4/116.8, break 10:40, sell to 110. */
export const aaoiBars = (): Candle[] =>
  pathBars([
    [9, 30, 121.0],
    [9, 40, 120.5],
    [9, 50, TC, undefined, T],
    [9, 55, 119.8, 119.9],
    [10, 5, TC, undefined, T],
    [10, 10, 117.7, 117.8],
    [10, 20, TC, undefined, T],
    [10, 26, 117.3, 117.4],
    [10, 35, TC, undefined, T],
    [10, 38, 116.7, 116.8],
    [10, 40, 115.2],
    [10, 55, 110.0],
  ]);

/** Same base, but every bounce tops at 118.0: support without lower highs. */
export const flatHighsBars = (): Candle[] =>
  pathBars([
    [9, 30, 121.0],
    [9, 40, 120.5],
    [9, 50, TC, undefined, T],
    [9, 55, 117.9, 118.0],
    [10, 5, TC, undefined, T],
    [10, 10, 117.9, 118.0],
    [10, 20, TC, undefined, T],
    [10, 26, 117.9, 118.0],
    [10, 35, TC, undefined, T],
    [10, 38, 117.9, 118.0],
    [10, 40, 115.2],
    [10, 55, 110.0],
  ]);

/** Lower highs on a stair of lower lows (no clean support), then a drop. */
export const stairBars = (): Candle[] =>
  pathBars([
    [9, 30, 121.0],
    [9, 40, 120.5],
    [9, 50, 116.0, undefined, 115.85],
    [9, 55, 119.8, 119.9],
    [10, 5, 115.1, undefined, 115.0],
    [10, 10, 117.7, 117.8],
    [10, 20, 114.3, undefined, 114.2],
    [10, 26, 117.3, 117.4],
    [10, 35, 113.5, undefined, 113.4],
    [10, 38, 116.7, 116.8],
    [10, 40, 112.6],
    [10, 55, 108.0],
  ]);

/** Only two touches (09:50, 10:05) with a lower high, then the break. */
export const twoTouchBars = (): Candle[] =>
  pathBars([
    [9, 30, 121.0],
    [9, 40, 120.5],
    [9, 50, TC, undefined, T],
    [9, 55, 119.8, 119.9],
    [10, 5, TC, undefined, T],
    [10, 10, 117.7, 117.8],
    [10, 11, 115.2],
    [10, 30, 110.0],
  ]);

/** Deterministic PRNG (mulberry32). */
export function prng(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/** A seeded random walk of n bars from 09:30 (random bases, bounces and breaks happen by chance). */
export function randomBars(seed: number, n: number): Candle[] {
  const r = prng(seed);
  const out: Candle[] = [];
  let c = 20 + r() * 10;
  for (let i = 0; i < n; i++) {
    const o = c;
    c = Math.round(Math.max(1, o * (1 + (r() - 0.5) * 0.012)) * 100) / 100;
    const h = Math.round((Math.max(o, c) + r() * 0.08) * 100) / 100;
    const l = Math.round((Math.min(o, c) - r() * 0.08) * 100) / 100;
    out.push({ ts: etMs14(9, 30) + i * MIN, open: o, high: h, low: l, close: c, volume: 10_000 });
  }
  return out;
}
