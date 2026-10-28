//! Execution markers — entry/exit arrows (BUILD-PLAN §5.1).
//
// buy  = green ▲ below the bar's low, pointing up at the entry
// sell = red   ▼ above the bar's high, pointing down at the exit
// Each marker also draws a short dashed tick at the exact fill price + a label.

import type { DataModel } from '../core/datamodel';
import type { TimeScale } from '../core/timescale';
import type { PriceScale } from '../core/pricescale';
import type { ChartColors, Execution, Rect } from '../core/types';
import { font } from './fonts';

const ARROW = 9; // half-width / height of the triangle

function labelPill(
  ctx: CanvasRenderingContext2D,
  text: string,
  cx: number,
  cy: number,
  color: string,
  bg: string,
  textColor: string,
): void {
  ctx.font = font(11, 'ui', '600');
  const padX = 5;
  const w = ctx.measureText(text).width + padX * 2;
  const h = 16;
  const x = cx - w / 2;
  const y = cy - h / 2;
  ctx.fillStyle = bg;
  ctx.strokeStyle = color;
  ctx.lineWidth = 1;
  const r = 3;
  ctx.beginPath();
  ctx.moveTo(x + r, y);
  ctx.arcTo(x + w, y, x + w, y + h, r);
  ctx.arcTo(x + w, y + h, x, y + h, r);
  ctx.arcTo(x, y + h, x, y, r);
  ctx.arcTo(x, y, x + w, y, r);
  ctx.closePath();
  ctx.fill();
  ctx.stroke();
  ctx.fillStyle = textColor;
  ctx.textAlign = 'center';
  ctx.textBaseline = 'middle';
  ctx.fillText(text, cx, y + h / 2 + 0.5);
}

export function drawMarkers(
  ctx: CanvasRenderingContext2D,
  plot: Rect,
  executions: Execution[],
  data: DataModel,
  ts: TimeScale,
  ps: PriceScale,
  colors: ChartColors,
): void {
  if (data.length === 0) return;
  for (const ex of executions) {
    const idx = data.timeToIndex(ex.ts);
    const x = ts.indexToX(idx);
    if (x < plot.x - 40 || x > plot.x + plot.w + 40) continue;
    const buy = ex.side === 'buy';
    const color = buy ? colors.buyMarker : colors.sellMarker;
    const yPrice = ps.priceToY(ex.price);

    // dashed level tick at the exact fill price, drawn through the bar
    const tickHalf = Math.max(10, ts.barSpacing * 2);
    ctx.strokeStyle = color;
    ctx.lineWidth = 1;
    ctx.setLineDash([3, 2]);
    ctx.beginPath();
    ctx.moveTo(x - tickHalf, yPrice);
    ctx.lineTo(x + tickHalf, yPrice);
    ctx.stroke();
    ctx.setLineDash([]);

    // Triangle arrow with its TIP at the exact fill price, on the candle:
    // buy points up (body just below the price), sell points down (above).
    ctx.beginPath();
    if (buy) {
      ctx.moveTo(x, yPrice);
      ctx.lineTo(x - ARROW, yPrice + ARROW);
      ctx.lineTo(x + ARROW, yPrice + ARROW);
    } else {
      ctx.moveTo(x, yPrice);
      ctx.lineTo(x - ARROW, yPrice - ARROW);
      ctx.lineTo(x + ARROW, yPrice - ARROW);
    }
    ctx.closePath();
    ctx.fillStyle = color;
    ctx.fill();
    // dark outline so the arrow stays visible over a same-colored candle body
    ctx.strokeStyle = 'rgba(0,0,0,0.55)';
    ctx.lineWidth = 1;
    ctx.stroke();

    // label offset clear of the arrow (below for buy, above for sell)
    if (ex.label) {
      let ly = buy ? yPrice + ARROW + 12 : yPrice - ARROW - 12;
      ly = Math.max(plot.y + 10, Math.min(plot.y + plot.h - 10, ly));
      let lx = x;
      const approxHalf = (ctx.measureText(ex.label).width + 10) / 2;
      lx = Math.max(plot.x + approxHalf, Math.min(plot.x + plot.w - approxHalf, lx));
      labelPill(ctx, ex.label, lx, ly, color, colors.crosshairLabelBg, colors.legendText);
    }
  }
}
