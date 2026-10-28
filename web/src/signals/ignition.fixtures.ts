// Synthetic IGNITION fixtures (no network): an NCPL-2026-10-14-shaped morning
// (flat ~1.75-1.80 base, a ~+5% candle on a volume spike at 09:06 ET, the big
// dildo at 09:07) plus the negative shapes. Shared by the unit and golden tests
// (the golden test writes them into ignition-golden.json, which Go
// internal/ignition and internal/engine replay).
import type { Candle } from '../vendor/blackthorn-charts';
import { etMs14 } from './bouncyShort.fixtures';

const r4 = (x: number): number => Math.round(x * 10_000) / 10_000;

interface Shape {
  baseLo?: number; // the base's low (1.75)
  baseHi?: number; // the base's high (1.80)
  ignClose?: number; // 1.88 (+5% from 1.79)
  ignVol?: number; // 300 000
  earlyHigh?: number; // a pre-market high before the base (none)
}

/** 08:40-09:05 base (26 bars, 20k volume), 09:06 ignition, 09:07 dildo, then a drift. */
export function ncplShaped(s: Shape = {}): Candle[] {
  const lo = s.baseLo ?? 1.75;
  const hi = s.baseHi ?? 1.8;
  const out: Candle[] = [];
  if (s.earlyHigh !== undefined) {
    out.push({ ts: etMs14(7, 0), open: 1.78, high: s.earlyHigh, low: 1.77, close: 1.79, volume: 30_000 });
    out.push({ ts: etMs14(7, 1), open: 1.79, high: 1.8, low: 1.76, close: 1.77, volume: 20_000 });
  }
  const mid = (lo + hi) / 2;
  for (let i = 0; i < 26; i++) {
    const up = i % 2 === 0;
    const open = r4(up ? mid - 0.01 : mid + 0.01);
    const close = r4(up ? mid + 0.01 : mid - 0.01);
    out.push({ ts: etMs14(8, 40 + i), open, high: i % 5 === 0 ? hi : r4(Math.max(open, close) + 0.005), low: i % 5 === 2 ? lo : r4(Math.min(open, close) - 0.005), close, volume: 20_000 });
  }
  const o = 1.79;
  const c = s.ignClose ?? 1.88;
  out.push({ ts: etMs14(9, 6), open: o, high: r4(c + 0.01), low: 1.785, close: c, volume: s.ignVol ?? 300_000 });
  out.push({ ts: etMs14(9, 7), open: c, high: 2.4, low: c, close: 2.3, volume: 2_000_000 });
  const drift = [2.25, 2.32, 2.2, 2.28, 2.35, 2.3];
  let prev = 2.3;
  drift.forEach((x, k) => {
    out.push({ ts: etMs14(9, 8 + k), open: prev, high: r4(Math.max(prev, x) + 0.02), low: r4(Math.min(prev, x) - 0.02), close: x, volume: 400_000 });
    prev = x;
  });
  return out;
}

export const ncplBars = (): Candle[] => ncplShaped();
/** Base 1.60-1.80 (12.5%): not quiet. */
export const wideBaseBars = (): Candle[] => ncplShaped({ baseLo: 1.6 });
/** The first candle is +10.6%: a dildo, not an ignition. */
export const bigBodyBars = (): Candle[] => ncplShaped({ ignClose: 1.98 });
/** 60k on the ignition candle: 3x, no spike. */
export const noVolumeBars = (): Candle[] => ncplShaped({ ignVol: 60_000 });
/** A 07:00 high of 2.10: the candle is not a new high of the day. */
export const belowDayHighBars = (): Candle[] => ncplShaped({ earlyHigh: 2.1 });

/** A seeded random 1-minute walk from 09:00 with quiet stretches and occasional spikes. */
export function randomIgnitionBars(seed: number, n: number): Candle[] {
  let s = seed >>> 0;
  const rnd = (): number => {
    s = (Math.imul(s, 1664525) + 1013904223) >>> 0;
    return s / 4294967296;
  };
  const out: Candle[] = [];
  let px = 1 + rnd() * 4;
  for (let i = 0; i < n; i++) {
    const spike = rnd() < 0.06;
    const open = r4(px);
    const move = spike ? 0.015 + rnd() * 0.09 : (rnd() - 0.5) * 0.012;
    const close = r4(open * (1 + move));
    const high = r4(Math.max(open, close) * (1 + rnd() * 0.004));
    const low = r4(Math.min(open, close) * (1 - rnd() * 0.004));
    const volume = Math.round(spike ? 50_000 + rnd() * 400_000 : 8_000 + rnd() * 20_000);
    out.push({ ts: etMs14(9, 0) + i * 60_000, open, high, low, close, volume });
    px = close;
  }
  return out;
}
