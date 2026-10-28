//! Columnar, index-addressable bar storage with O(log n) time↔index lookup.
//
// Each field is backed by a Float64Array (BUILD-PLAN §1.2). Bars are sorted
// ascending by `ts`. The index-based scale (TimeScale) addresses bars by
// position, so this is the bridge from a real timestamp back to a bar.

import type { Candle, PriceRange } from './types';

export class DataModel {
  private _ts = new Float64Array(0);
  private _o = new Float64Array(0);
  private _h = new Float64Array(0);
  private _l = new Float64Array(0);
  private _c = new Float64Array(0);
  private _v = new Float64Array(0);
  private _n = 0;
  /** bumped on every setData — lets caches invalidate. */
  version = 0;

  setData(candles: Candle[]): void {
    const sorted = candles.slice().sort((a, b) => a.ts - b.ts);
    const n = sorted.length;
    this._ts = new Float64Array(n);
    this._o = new Float64Array(n);
    this._h = new Float64Array(n);
    this._l = new Float64Array(n);
    this._c = new Float64Array(n);
    this._v = new Float64Array(n);
    for (let i = 0; i < n; i++) {
      const b = sorted[i];
      this._ts[i] = b.ts;
      this._o[i] = b.open;
      this._h[i] = b.high;
      this._l[i] = b.low;
      this._c[i] = b.close;
      this._v[i] = b.volume;
    }
    this._n = n;
    this.version++;
  }

  /** Append a live bar, or replace the trailing one when it re-reports the same
   *  `ts` (a forming candle ticking). Out-of-order bars older than the tail are
   *  ignored. Returns true when a bar was appended (length grew).
   *
   *  Amortized O(1): capacity grows geometrically, so a per-second tick doesn't
   *  reallocate six typed arrays. `_n` stays the logical length, which every
   *  accessor already respects, so spare capacity is invisible. */
  update(b: Candle): boolean {
    const n = this._n;
    if (n > 0) {
      const tail = this._ts[n - 1];
      if (b.ts < tail) return false; // stale/out-of-order
      if (b.ts === tail) {
        this._o[n - 1] = b.open;
        this._h[n - 1] = b.high;
        this._l[n - 1] = b.low;
        this._c[n - 1] = b.close;
        this._v[n - 1] = b.volume;
        this.version++;
        return false;
      }
    }
    this.reserve(n + 1);
    this._ts[n] = b.ts;
    this._o[n] = b.open;
    this._h[n] = b.high;
    this._l[n] = b.low;
    this._c[n] = b.close;
    this._v[n] = b.volume;
    this._n = n + 1;
    this.version++;
    return true;
  }

  /** Grow the backing arrays to hold at least `need` bars (doubling). */
  private reserve(need: number): void {
    if (need <= this._ts.length) return;
    const cap = Math.max(need, this._ts.length * 2, 64);
    const ts = new Float64Array(cap);
    const o = new Float64Array(cap);
    const h = new Float64Array(cap);
    const l = new Float64Array(cap);
    const c = new Float64Array(cap);
    const v = new Float64Array(cap);
    ts.set(this._ts);
    o.set(this._o);
    h.set(this._h);
    l.set(this._l);
    c.set(this._c);
    v.set(this._v);
    this._ts = ts;
    this._o = o;
    this._h = h;
    this._l = l;
    this._c = c;
    this._v = v;
  }

  get length(): number {
    return this._n;
  }

  time(i: number): number {
    return this._ts[i];
  }
  open(i: number): number {
    return this._o[i];
  }
  high(i: number): number {
    return this._h[i];
  }
  low(i: number): number {
    return this._l[i];
  }
  close(i: number): number {
    return this._c[i];
  }
  volume(i: number): number {
    return this._v[i];
  }

  bar(i: number): Candle | null {
    if (i < 0 || i >= this._n) return null;
    return {
      ts: this._ts[i],
      open: this._o[i],
      high: this._h[i],
      low: this._l[i],
      close: this._c[i],
      volume: this._v[i],
    };
  }

  /** Nearest bar index to `ts` (clamped to [0, length-1]). -1 when empty. */
  timeToIndex(ts: number): number {
    const n = this._n;
    if (n === 0) return -1;
    if (ts <= this._ts[0]) return 0;
    if (ts >= this._ts[n - 1]) return n - 1;
    // binary search for the largest index with _ts[i] <= ts
    let lo = 0;
    let hi = n - 1;
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1;
      if (this._ts[mid] <= ts) lo = mid;
      else hi = mid - 1;
    }
    // lo has _ts[lo] <= ts < _ts[lo+1]; pick nearest of the two
    const next = lo + 1;
    return ts - this._ts[lo] <= this._ts[next] - ts ? lo : next;
  }

  indexToTime(i: number): number {
    if (this._n === 0) return 0;
    const c = Math.max(0, Math.min(this._n - 1, Math.round(i)));
    return this._ts[c];
  }

  /** Representative bar interval (ms): median of consecutive diffs, so overnight
   *  / session gaps don't skew the right-edge extrapolation. */
  interval(): number {
    if (this._intervalVer === this.version) return this._interval;
    const n = this._n;
    let out = 60_000;
    if (n >= 2) {
      const diffs: number[] = [];
      for (let i = 1; i < n; i++) diffs.push(this._ts[i] - this._ts[i - 1]);
      diffs.sort((a, b) => a - b);
      const m = diffs[diffs.length >> 1];
      if (m > 0) out = m;
    }
    this._interval = out;
    this._intervalVer = this.version;
    return out;
  }
  private _interval = 60_000;
  private _intervalVer = -1;

  /** Fractional bar index for a timestamp, interpolating between bars and
   *  extrapolating (by `interval()`) outside the loaded range. Inverse of
   *  `fractionalToTime`. Powers drawing-point projection at the right edge. */
  timeToFractional(ts: number): number {
    const n = this._n;
    if (n === 0) return 0;
    if (n === 1 || ts <= this._ts[0]) return (ts - this._ts[0]) / this.interval();
    if (ts >= this._ts[n - 1]) return n - 1 + (ts - this._ts[n - 1]) / this.interval();
    let lo = 0;
    let hi = n - 1;
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1;
      if (this._ts[mid] <= ts) lo = mid;
      else hi = mid - 1;
    }
    const span = this._ts[lo + 1] - this._ts[lo] || 1;
    return lo + (ts - this._ts[lo]) / span;
  }

  /** Timestamp for a fractional bar index (inverse of `timeToFractional`). */
  fractionalToTime(idx: number): number {
    const n = this._n;
    if (n === 0) return 0;
    if (idx <= 0) return this._ts[0] + idx * this.interval();
    if (idx >= n - 1) return this._ts[n - 1] + (idx - (n - 1)) * this.interval();
    const lo = Math.floor(idx);
    const span = this._ts[lo + 1] - this._ts[lo];
    return this._ts[lo] + (idx - lo) * span;
  }

  /** First index whose time is >= `ts`, in [0, length]. `length` if none. */
  lowerBound(ts: number): number {
    let lo = 0;
    let hi = this._n;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if (this._ts[mid] < ts) lo = mid + 1;
      else hi = mid;
    }
    return lo;
  }

  /** High/low over the inclusive index slice [from, to] (clamped). */
  highLow(from: number, to: number): PriceRange {
    const n = this._n;
    if (n === 0) return { min: 0, max: 1 };
    const a = Math.max(0, Math.floor(from));
    const b = Math.min(n - 1, Math.ceil(to));
    let min = Infinity;
    let max = -Infinity;
    for (let i = a; i <= b; i++) {
      if (this._l[i] < min) min = this._l[i];
      if (this._h[i] > max) max = this._h[i];
    }
    if (!isFinite(min) || !isFinite(max)) return { min: 0, max: 1 };
    if (min === max) {
      // flat slice — give it a tiny band so the scale doesn't divide by zero
      const pad = Math.abs(min) * 0.01 || 1;
      return { min: min - pad, max: max + pad };
    }
    return { min, max };
  }

  /** Max volume over the inclusive index slice [from, to] (clamped). */
  volumeMax(from: number, to: number): number {
    const n = this._n;
    if (n === 0) return 0;
    const a = Math.max(0, Math.floor(from));
    const b = Math.min(n - 1, Math.ceil(to));
    let max = 0;
    for (let i = a; i <= b; i++) if (this._v[i] > max) max = this._v[i];
    return max;
  }
}
