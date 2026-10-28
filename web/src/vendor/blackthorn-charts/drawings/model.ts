//! Drawing model — the persisted JSON shape of a chart annotation.
//
// Anchors are LOGICAL (time / price) so drawings survive pan, zoom, resize
// and timeframe changes. hline omits `time`, vline omits `price`; position
// tools carry [entry, {end time, target}, {stop}]. The shape is a superset
// of the app's original overlay format, so persisted drawings load as-is.

import type { LineStyle } from '../core/types';

export type DrawingType =
  | 'trendline'
  | 'ray'
  | 'arrow'
  | 'hline'
  | 'vline'
  | 'rect'
  | 'fib'
  | 'channel'
  | 'measure'
  | 'long'
  | 'short'
  | 'text';

export type DrawingTool = DrawingType | 'cursor';

export type DrawingStyle = { color: string; lineWidth: number; lineStyle: LineStyle };

export type DrawingPoint = { time?: number; price?: number };

export type Drawing = {
  id: string;
  type: DrawingType;
  points: DrawingPoint[];
  style: DrawingStyle;
  text?: string;
  /** locked drawings render normally but cannot be dragged or resized. */
  locked?: boolean;
};

export const DEFAULT_DRAWING_STYLE: DrawingStyle = { color: '#f59e0b', lineWidth: 1.5, lineStyle: 'solid' };

/** Clicks that place a drawing of each type. Position tools take one click
 *  (the entry) and are then shaped by dragging their handles. */
export const POINTS_NEEDED: Record<DrawingType, number> = {
  trendline: 2,
  ray: 2,
  arrow: 2,
  hline: 1,
  vline: 1,
  rect: 2,
  fib: 2,
  channel: 3,
  measure: 2,
  long: 1,
  short: 1,
  text: 1,
};

export const FIB_LEVELS = [0, 0.236, 0.382, 0.5, 0.618, 0.786, 1];

let _seq = 0;
export function makeDrawing(type: DrawingType, points: DrawingPoint[], style?: Partial<DrawingStyle>, text?: string): Drawing {
  return {
    id: `draw-${Date.now()}-${++_seq}`,
    type,
    points,
    style: { ...DEFAULT_DRAWING_STYLE, ...(style ?? {}) },
    ...(type === 'text' ? { text: text ?? '' } : {}),
  };
}

/** Which logical coordinates a click contributes for a tool's point. */
export function pointFor(type: DrawingType, logical: { time: number; price: number }): DrawingPoint {
  if (type === 'hline') return { price: logical.price };
  if (type === 'vline') return { time: logical.time };
  return { time: logical.time, price: logical.price };
}

/** Whole-shape translate by a logical (time, price) delta. */
export function moveDrawing(d: Drawing, dTime: number, dPrice: number): Drawing {
  return {
    ...d,
    points: d.points.map((pt) => ({
      ...(pt.time != null ? { time: pt.time + dTime } : {}),
      ...(pt.price != null ? { price: pt.price + dPrice } : {}),
    })),
  };
}

export type HandleMode = 'both' | 'price' | 'time';

/** Move one anchor: `mode` limits which coordinate the handle controls
 *  (a position tool's stop handle only moves its price, its end handle
 *  only its time). */
export function moveHandle(d: Drawing, pointIdx: number, mode: HandleMode, logical: { time: number; price: number }): Drawing {
  const points = d.points.map((pt, i) => {
    if (i !== pointIdx) return pt;
    const next: DrawingPoint = { ...pt };
    if (pt.time != null && mode !== 'price') next.time = logical.time;
    if (pt.price != null && mode !== 'time') next.price = logical.price;
    return next;
  });
  return { ...d, points };
}
