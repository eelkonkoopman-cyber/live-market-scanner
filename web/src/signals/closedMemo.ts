// Closed-bar memo for the signal rules that are a pure function of CLOSED
// candles. No DOM, no store, no `Date.now()`.
//
// The Signal window re-evaluates every cell on every live bar update, and a
// forming-bar update changes nothing the closed-bar rules can see: they only
// ever read candles[0 .. last], where `last` is the newest candle that has
// closed at `nowMs` (bouncyShort.ts evaluateBbs and ignition.ts
// evaluateIgnition: `last = nowMs < candles[n-1].ts + tfMs ? n - 2 : n - 1`,
// the loop is `j <= last`, and every look-back index is < j). The forming
// candle, and `nowMs` beyond deciding which candle that is, cannot reach the
// result.
//
// So the memo's key is the closed candles THEMSELVES: how many there are, every
// one's ts / open / high / low / close / volume (compared value by value
// against a private snapshot, so a refetch that rewrites history, or another
// symbol's candles, is a miss — nothing is assumed from array identity or from
// "only the last bar moves"), plus tfMs and the params object. A hit returns
// the previous result object untouched; a miss runs `evaluate` on the full
// input exactly as before. The comparison is a few thousand number compares,
// the evaluation it saves is 10-30 ms on a two-day 1-minute series.
//
// ONLY for evaluators that read nothing but closed candles. emaFlag / dildoPb /
// climax / shortFade have forming-bar states, so they are NOT wrapped.

import type { Candle } from '../vendor/blackthorn-charts';

export interface ClosedBarInput<P> {
  candles: Candle[];
  tfMs: number;
  nowMs: number;
  params?: P;
}

const FIELDS = 6; // ts, open, high, low, close, volume

/** How many of `candles` have closed at `nowMs` — the evaluators' `last + 1`. */
export function closedCount(candles: readonly Candle[], tfMs: number, nowMs: number): number {
  const n = candles.length;
  return n > 0 && nowMs < candles[n - 1]!.ts + tfMs ? n - 1 : n;
}

/** One memo per call site (per cell): it remembers the last run only. */
export function closedBarMemo<P, R>(evaluate: (input: ClosedBarInput<P>) => R): (input: ClosedBarInput<P>) => R {
  let snap = new Float64Array(0); // the cached run's closed candles, FIELDS numbers each
  let count = -1; // how many closed candles the cached run had; -1 = nothing cached
  let tfMs = Number.NaN;
  let params: P | undefined;
  let result: R | undefined;

  const same = (candles: readonly Candle[], k: number): boolean => {
    for (let i = 0, o = 0; i < k; i++, o += FIELDS) {
      const c = candles[i]!;
      // Object.is, not ===: only a bit-identical candle may reuse the result
      // (a NaN candle matches itself and re-evaluates identically; -0 is not +0).
      if (
        !Object.is(snap[o], c.ts) ||
        !Object.is(snap[o + 1], c.open) ||
        !Object.is(snap[o + 2], c.high) ||
        !Object.is(snap[o + 3], c.low) ||
        !Object.is(snap[o + 4], c.close) ||
        !Object.is(snap[o + 5], c.volume)
      ) {
        return false;
      }
    }
    return true;
  };

  return (input) => {
    const candles = input.candles;
    if (candles.length === 0) return evaluate(input);
    const k = closedCount(candles, input.tfMs, input.nowMs);
    if (count === k && tfMs === input.tfMs && params === input.params && same(candles, k)) return result as R;

    const fresh = evaluate(input); // a throw leaves the previous entry as it was
    if (snap.length < k * FIELDS) snap = new Float64Array(k * FIELDS + 512 * FIELDS);
    for (let i = 0, o = 0; i < k; i++, o += FIELDS) {
      const c = candles[i]!;
      snap[o] = c.ts;
      snap[o + 1] = c.open;
      snap[o + 2] = c.high;
      snap[o + 3] = c.low;
      snap[o + 4] = c.close;
      snap[o + 5] = c.volume;
    }
    count = k;
    tfMs = input.tfMs;
    params = input.params;
    result = fresh;
    return fresh;
  };
}
