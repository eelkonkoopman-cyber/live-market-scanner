//! Last-price line — a dashed line across the price pane at the last close,
//! plus a filled chip in the price-axis gutter, coloured by the last bar's
//! direction (the TradingView convention). Drawn on the series layer, above
//! the candles; the crosshair layer paints over it when they overlap.

import type { DataModel } from '../core/datamodel';
import type { PriceScale } from '../core/pricescale';
import type { ChartColors, Rect } from '../core/types';
import { align } from './layers';
import { font } from './fonts';

export function drawLastPrice(
  ctx: CanvasRenderingContext2D,
  plot: Rect,
  data: DataModel,
  ps: PriceScale,
  colors: ChartColors,
  axisW: number,
): void {
  const n = data.length;
  if (n === 0) return;
  const price = data.close(n - 1);
  const y = ps.priceToY(price);
  if (y < plot.y || y > plot.y + plot.h) return;
  const color = price >= data.open(n - 1) ? colors.upBody : colors.downBody;

  ctx.save();
  ctx.beginPath();
  ctx.rect(plot.x, plot.y, plot.w + axisW, plot.h);
  ctx.clip();

  ctx.strokeStyle = color;
  ctx.lineWidth = 1;
  ctx.setLineDash([2, 2]);
  ctx.beginPath();
  ctx.moveTo(plot.x, align(y));
  ctx.lineTo(plot.x + plot.w, align(y));
  ctx.stroke();
  ctx.setLineDash([]);

  const label = price.toFixed(Math.abs(price) < 1 ? 4 : 2);
  const h = 16;
  ctx.font = font(11);
  ctx.fillStyle = color;
  ctx.fillRect(plot.x + plot.w, Math.round(y - h / 2), axisW, h);
  ctx.fillStyle = colors.background;
  ctx.textAlign = 'left';
  ctx.textBaseline = 'middle';
  ctx.fillText(label, plot.x + plot.w + 6, Math.round(y - h / 2) + h / 2 + 0.5);
  ctx.restore();
}
