//! Indicator series renderer (BUILD-PLAN §3.2/§3.7) — paints the computed
//! `PaneSeries` (overlay lines on the price pane; lines + histogram in oscillator
//! panes) plus horizontal guide levels. Index-aligned + viewport-culled.

import type { DataModel } from '../core/datamodel';
import type { TimeScale } from '../core/timescale';
import type { PriceScale } from '../core/pricescale';
import type { GuideLevel, LineStyle, PaneSeries, Rect } from '../core/types';
import { align } from './layers';

function dash(ctx: CanvasRenderingContext2D, style: LineStyle): void {
  ctx.setLineDash(style === 'dashed' ? [5, 3] : style === 'dotted' ? [1, 3] : []);
}

function visible(data: DataModel, ts: TimeScale): { from: number; to: number } {
  const r = ts.visibleIndexRange();
  return {
    from: Math.max(0, Math.floor(r.from) - 1),
    to: Math.min(data.length - 1, Math.ceil(r.to) + 1),
  };
}

function drawLine(
  ctx: CanvasRenderingContext2D,
  values: (number | null)[],
  toY: (v: number) => number,
  ts: TimeScale,
  from: number,
  to: number,
  color: string,
  lineWidth: number,
  lineStyle: LineStyle,
): void {
  ctx.strokeStyle = color;
  ctx.lineWidth = lineWidth;
  ctx.lineJoin = 'round';
  dash(ctx, lineStyle);
  ctx.beginPath();
  let pen = false;
  for (let i = from; i <= to; i++) {
    const v = values[i];
    if (v == null || Number.isNaN(v)) {
      pen = false;
      continue;
    }
    const x = ts.indexToX(i);
    const y = toY(v);
    if (!pen) {
      ctx.moveTo(x, y);
      pen = true;
    } else {
      ctx.lineTo(x, y);
    }
  }
  ctx.stroke();
  ctx.setLineDash([]);
}

function drawHistogram(
  ctx: CanvasRenderingContext2D,
  values: (number | null)[],
  perBar: (string | null)[] | undefined,
  baseColor: string,
  ps: PriceScale,
  ts: TimeScale,
  from: number,
  to: number,
): void {
  const half = Math.max(0.5, Math.min(ts.barSpacing * 0.34, ts.barSpacing / 2 - 0.5));
  const yZero = ps.priceToY(0);
  for (let i = from; i <= to; i++) {
    const v = values[i];
    if (v == null || Number.isNaN(v)) continue;
    const x = ts.indexToX(i);
    const y = ps.priceToY(v);
    const top = Math.min(y, yZero);
    const h = Math.max(1, Math.abs(y - yZero));
    ctx.fillStyle = perBar?.[i] ?? baseColor;
    ctx.fillRect(Math.round(x - half), Math.round(top), Math.max(1, Math.round(half * 2)), Math.round(h));
  }
}

/** Draw all guide levels + series for one pane (caller has clipped to the rect). */
export function drawPaneStudies(
  ctx: CanvasRenderingContext2D,
  plot: Rect,
  series: PaneSeries[],
  guides: GuideLevel[],
  data: DataModel,
  ts: TimeScale,
  ps: PriceScale,
  /** y for a volume-scale value (price pane only; lines with scale 'volume') */
  volY?: (v: number) => number,
): void {
  if (data.length === 0) return;
  const { from, to } = visible(data, ts);
  const priceY = (v: number) => ps.priceToY(v);

  // guide levels behind the series
  for (const g of guides) {
    const y = ps.priceToY(g.value);
    if (y < plot.y - 1 || y > plot.y + plot.h + 1) continue;
    ctx.strokeStyle = g.color;
    ctx.lineWidth = 1;
    ctx.setLineDash([4, 4]);
    ctx.beginPath();
    ctx.moveTo(plot.x, align(y));
    ctx.lineTo(plot.x + plot.w, align(y));
    ctx.stroke();
    ctx.setLineDash([]);
  }

  // histograms first (behind lines), then lines
  for (const s of series) {
    if (s.kind === 'histogram') drawHistogram(ctx, s.values, s.colors, s.color, ps, ts, from, to);
  }
  for (const s of series) {
    if (s.kind !== 'line') continue;
    if (s.scale === 'volume' && !volY) continue;
    drawLine(ctx, s.values, s.scale === 'volume' ? volY! : priceY, ts, from, to, s.color, s.lineWidth, s.lineStyle);
  }
}
