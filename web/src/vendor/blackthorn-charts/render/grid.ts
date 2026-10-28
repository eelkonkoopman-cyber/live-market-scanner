//! Grid + per-pane price axis + shared ET time axis (BUILD-PLAN §1.7 / §3.3).
//
// In a multi-pane chart each pane draws its own right price axis (over its own
// PriceScale + rect); the vertical time grid + ET labels are drawn once across
// the whole stacked area, with labels only under the bottom pane.

import type { PriceScale } from '../core/pricescale';
import type { ChartColors, Rect } from '../core/types';
import { align } from './layers';
import { font } from './fonts';

function niceNum(range: number, round: boolean): number {
  const exp = Math.floor(Math.log10(range));
  const f = range / Math.pow(10, exp);
  let nf: number;
  if (round) nf = f < 1.5 ? 1 : f < 3 ? 2 : f < 7 ? 5 : 10;
  else nf = f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10;
  return nf * Math.pow(10, exp);
}

/** The tick values + label precision a pane's price axis will show — shared
 *  with the axis-width measurement in chart.ts. */
export function priceTicks(min: number, max: number, plotH: number): { values: number[]; decimals: number } {
  const targetTicks = Math.max(2, Math.floor(plotH / 48));
  const step = niceNum((max - min) / targetTicks, true) || 1;
  const decimals = step < 0.1 ? 3 : step < 1 ? 2 : max < 1 ? 4 : 2;
  const values: number[] = [];
  const first = Math.ceil(min / step) * step;
  for (let p = first; p <= max && values.length < 200; p += step) values.push(p);
  return { values, decimals };
}

export const axisFont = (): string => font(11);

/** Horizontal grid + right price axis for one pane. */
export function drawPriceAxis(
  ctx: CanvasRenderingContext2D,
  plot: Rect,
  ps: PriceScale,
  colors: ChartColors,
  showGrid: boolean,
): void {
  const { min, max } = ps.range();
  const { values, decimals } = priceTicks(min, max, plot.h);
  ctx.font = axisFont();
  ctx.textBaseline = 'middle';
  for (const p of values) {
    const y = ps.priceToY(p);
    if (y < plot.y || y > plot.y + plot.h) continue;
    if (showGrid) {
      ctx.strokeStyle = colors.grid;
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.moveTo(plot.x, align(y));
      ctx.lineTo(plot.x + plot.w, align(y));
      ctx.stroke();
    }
    ctx.fillStyle = colors.axisText;
    ctx.textAlign = 'left';
    ctx.fillText(p.toFixed(decimals), plot.x + plot.w + 6, y);
  }

  // pane separators: right axis edge + bottom edge
  ctx.strokeStyle = colors.axisLine;
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.moveTo(align(plot.x + plot.w), plot.y);
  ctx.lineTo(align(plot.x + plot.w), plot.y + plot.h);
  ctx.moveTo(plot.x, align(plot.y + plot.h));
  ctx.lineTo(plot.x + plot.w, align(plot.y + plot.h));
  ctx.stroke();
}
