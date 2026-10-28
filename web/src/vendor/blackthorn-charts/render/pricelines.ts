//! Horizontal price-line renderer — draws PriceLine[] across the price pane at
//! fixed price values (prior close, session high/low, entry levels) with an
//! optional left-anchored label chip. Mirrors the guide-level style in
//! studies.ts but lives on the price pane and carries text labels.

import type { PriceScale } from '../core/pricescale';
import type { ChartColors, LineStyle, PriceLine, Rect } from '../core/types';
import { align } from './layers';
import { font } from './fonts';

function dash(ctx: CanvasRenderingContext2D, style: LineStyle | undefined): void {
  ctx.setLineDash(style === 'solid' ? [] : style === 'dotted' ? [1, 3] : [5, 3]);
}

/** Draw horizontal reference lines on the price pane (caller has clipped to rect). */
export function drawPriceLines(
  ctx: CanvasRenderingContext2D,
  plot: Rect,
  lines: PriceLine[],
  ps: PriceScale,
  colors: ChartColors,
): void {
  if (!lines.length) return;
  ctx.font = font(10);
  ctx.textBaseline = 'middle';
  for (const ln of lines) {
    if (!Number.isFinite(ln.price)) continue;
    const y = ps.priceToY(ln.price);
    if (y < plot.y - 1 || y > plot.y + plot.h + 1) continue;
    const color = ln.color || colors.axisText;

    ctx.strokeStyle = color;
    ctx.lineWidth = ln.lineWidth ?? 1;
    dash(ctx, ln.lineStyle);
    ctx.beginPath();
    ctx.moveTo(plot.x, align(y));
    ctx.lineTo(plot.x + plot.w, align(y));
    ctx.stroke();
    ctx.setLineDash([]);

    if (ln.label) {
      const padX = 4;
      const boxH = 13;
      const tw = ctx.measureText(ln.label).width;
      const bx = plot.x + 2;
      const by = Math.max(plot.y, Math.min(plot.y + plot.h - boxH, y - boxH / 2));
      ctx.fillStyle = colors.crosshairLabelBg;
      ctx.fillRect(bx, by, tw + padX * 2, boxH);
      ctx.fillStyle = color;
      ctx.textAlign = 'left';
      ctx.fillText(ln.label, bx + padX, by + boxH / 2);
    }
  }
}
