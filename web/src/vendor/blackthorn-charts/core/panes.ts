//! Native multi-pane layout (BUILD-PLAN §3.1).
//
// The price pane (panes[0]) always exists and takes the remaining height; each
// oscillator pane (RSI/MACD) is stacked below it with its own price scale and a
// clamped pixel height, drag-resizable. The time scale (x) is shared across all
// panes so vertical gridlines/crosshair line up; only the bottom pane shows the
// ET time axis.

import { PriceScale } from './pricescale';
import { PRICE_AXIS_W, TIME_AXIS_H } from './types';
import type { GuideLevel, PaneSeries, PriceLine, Rect } from './types';

export type PaneKind = 'price' | 'oscillator';

/** clamp range for an oscillator pane's pixel height (matches the old UI). */
export const OSC_MIN_H = 70;
export const OSC_MAX_H = 380;
/** minimum height the price pane is allowed to shrink to. */
export const PRICE_MIN_H = 90;
/** default height for a freshly-added oscillator pane. */
export const OSC_DEFAULT_H = 130;

export interface Pane {
  id: string;
  kind: PaneKind;
  scale: PriceScale;
  rect: Rect;
  /** desired height in px (oscillator panes only; price pane is the remainder). */
  heightPx: number;
  /** pinned y-range (e.g. RSI 0–100); when set, autofit is skipped. */
  range?: { min: number; max: number };
  guides: GuideLevel[];
  series: PaneSeries[];
  /** indicator-supplied horizontal levels (session levels) — price pane. */
  priceLines: PriceLine[];
  /** for the legend / axis precision; set by the controller. */
  label?: string;
}

export class PaneLayout {
  panes: Pane[];
  /** plot width (px) shared by every pane = total − price axis. */
  plotW = 0;
  /** total stacked content height (px) = total − time axis. */
  contentH = 0;
  /** price-axis gutter width (px); grows to fit the widest label. */
  axisW = PRICE_AXIS_W;

  constructor() {
    const price: Pane = {
      id: 'price',
      kind: 'price',
      scale: new PriceScale(),
      rect: { x: 0, y: 0, w: 0, h: 0 },
      heightPx: 0,
      guides: [],
      series: [],
      priceLines: [],
    };
    this.panes = [price];
  }

  get pricePane(): Pane {
    return this.panes[0];
  }

  get oscillators(): Pane[] {
    return this.panes.slice(1);
  }

  getPane(id: string): Pane | undefined {
    return this.panes.find((p) => p.id === id);
  }

  addPane(id: string, opts: { range?: { min: number; max: number }; guides?: GuideLevel[]; heightPx?: number; label?: string } = {}): Pane {
    const existing = this.getPane(id);
    if (existing) return existing;
    const pane: Pane = {
      id,
      kind: 'oscillator',
      scale: new PriceScale(),
      rect: { x: 0, y: 0, w: 0, h: 0 },
      // default: a fifth of the chart (clamped), not a fixed 130 px
      heightPx: opts.heightPx ?? (this.contentH > 0 ? Math.round(this.contentH * 0.22) : OSC_DEFAULT_H),
      ...(opts.range ? { range: opts.range } : {}),
      guides: opts.guides ?? [],
      series: [],
      priceLines: [],
      ...(opts.label ? { label: opts.label } : {}),
    };
    // oscillator panes use tighter margins (no volume band at the bottom).
    pane.scale.scaleMargins = { top: 0.12, bottom: 0.12 };
    this.panes.push(pane);
    return pane;
  }

  /** Order the oscillator panes to match `ids` (unknown ids keep their
   *  relative order at the end). */
  setOrder(ids: string[]): void {
    const oscs = this.oscillators;
    const rank = new Map(ids.map((id, i) => [id, i]));
    oscs.sort((a, b) => (rank.get(a.id) ?? 1e9) - (rank.get(b.id) ?? 1e9));
    this.panes = [this.pricePane, ...oscs];
  }

  removePane(id: string): void {
    const i = this.panes.findIndex((p) => p.id === id);
    if (i > 0) this.panes.splice(i, 1); // never remove the price pane
  }

  /** Compute each pane's pixel rect. Price axis sits on the right (all panes);
   *  the ET time axis is a strip below the bottom pane. */
  recompute(totalW: number, totalH: number): void {
    this.plotW = Math.max(10, totalW - this.axisW);
    this.contentH = Math.max(10, totalH - TIME_AXIS_H);

    const oscs = this.oscillators;
    let oscTotal = 0;
    for (const p of oscs) {
      p.heightPx = Math.max(OSC_MIN_H, Math.min(OSC_MAX_H, p.heightPx));
      oscTotal += p.heightPx;
    }
    // price pane gets the remainder; if oscillators would crowd it out, shrink
    // them proportionally so the price pane keeps PRICE_MIN_H.
    let priceH = this.contentH - oscTotal;
    if (priceH < PRICE_MIN_H && oscTotal > 0) {
      const avail = Math.max(0, this.contentH - PRICE_MIN_H);
      const k = avail / oscTotal;
      for (const p of oscs) p.heightPx = Math.max(OSC_MIN_H, Math.floor(p.heightPx * k));
      oscTotal = oscs.reduce((s, p) => s + p.heightPx, 0);
      priceH = Math.max(PRICE_MIN_H, this.contentH - oscTotal);
    }

    let y = 0;
    const price = this.pricePane;
    price.heightPx = priceH;
    price.rect = { x: 0, y, w: this.plotW, h: priceH };
    price.scale.setRect(y, priceH);
    y += priceH;
    for (const p of oscs) {
      p.rect = { x: 0, y, w: this.plotW, h: p.heightPx };
      p.scale.setRect(y, p.heightPx);
      y += p.heightPx;
    }
  }

  /** Pane whose rect contains pixel y (the price pane if below the stack). */
  paneAtY(y: number): Pane {
    for (const p of this.panes) {
      if (y >= p.rect.y && y <= p.rect.y + p.rect.h) return p;
    }
    return this.pricePane;
  }

  /** The y of each oscillator pane's top edge — the drag dividers. Returns
   *  [{ paneId, y }] (one per oscillator; the boundary above it). */
  dividers(): { paneId: string; y: number }[] {
    return this.oscillators.map((p) => ({ paneId: p.id, y: p.rect.y }));
  }
}
