//! Index-based x-axis (BUILD-PLAN §1.3).
//
// The x-axis is indexed by bar POSITION, not wall-clock time, so weekends and
// non-trading hours collapse (like TradingView). Every x maps to a bar index —
// this is what lets session bands / markers never get dropped at the right edge.
//
//   x = (index − firstVisibleIndex) × barSpacing

import type { DataModel } from './datamodel';

/** px per bar a fresh chart opens at — readable candles, ~70 bars per 560 px
 *  (TradingView opens at ~6). Fit-all is an explicit action, never the default. */
export const DEFAULT_BAR_SPACING = 8;
export const MIN_BAR_SPACING = 0.5;
export const MAX_BAR_SPACING = 90;

export class TimeScale {
  firstVisibleIndex = 0;
  /** px per bar (zoom) */
  barSpacing = DEFAULT_BAR_SPACING;
  /** bars of right-edge padding */
  rightOffset = 4;
  /** plot-area width in px */
  width = 0;

  setWidth(w: number): void {
    this.width = w;
  }

  indexToX(index: number): number {
    return (index - this.firstVisibleIndex) * this.barSpacing;
  }

  xToIndex(x: number): number {
    return x / this.barSpacing + this.firstVisibleIndex;
  }

  timeToX(ts: number, data: DataModel): number | null {
    if (data.length === 0) return null;
    return this.indexToX(data.timeToIndex(ts));
  }

  /** Visible index window for the current width, as floats. */
  visibleIndexRange(): { from: number; to: number } {
    return {
      from: this.firstVisibleIndex,
      to: this.firstVisibleIndex + this.width / this.barSpacing,
    };
  }

  /** Frame [0, n-1] plus rightOffset into the current width. */
  fitContent(n: number): void {
    if (n <= 0 || this.width <= 0) return;
    const slots = n + this.rightOffset;
    this.barSpacing = Math.max(0.5, this.width / slots);
    this.firstVisibleIndex = 0;
  }

  /** Keep the current zoom, put the last bar (plus rightOffset) at the right
   *  edge — the "go to latest" / default view. */
  scrollToLatest(n: number): void {
    if (n <= 0 || this.width <= 0) return;
    this.firstVisibleIndex = n - 1 + this.rightOffset - this.width / this.barSpacing;
    this.clamp(n);
  }

  /** Default view: default zoom, anchored at the latest bar. */
  resetView(n: number): void {
    this.barSpacing = DEFAULT_BAR_SPACING;
    this.scrollToLatest(n);
  }

  /** True when the last bar is on screen (or there is no data). */
  isAtLatest(n: number): boolean {
    if (n <= 0) return true;
    return this.indexToX(n - 1) <= this.width + 0.5;
  }

  /** Set a zoom level, keeping the bar under pixel `x` where it is. */
  zoomAt(x: number, barSpacing: number, n: number): void {
    const idxUnder = this.xToIndex(x);
    this.barSpacing = Math.max(MIN_BAR_SPACING, Math.min(MAX_BAR_SPACING, barSpacing));
    this.firstVisibleIndex = idxUnder - x / this.barSpacing;
    this.clamp(n);
  }

  /** Clamp the pan so a little content always stays on screen. */
  clamp(n: number): void {
    if (n <= 0) return;
    const visBars = this.width / this.barSpacing;
    const maxFirst = n - 1 + this.rightOffset - visBars * 0.15;
    const minFirst = -(visBars * 0.85);
    if (this.firstVisibleIndex > maxFirst) this.firstVisibleIndex = maxFirst;
    if (this.firstVisibleIndex < minFirst) this.firstVisibleIndex = minFirst;
  }
}
