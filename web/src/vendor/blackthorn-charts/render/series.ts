//! Main-series renderers (candles/bars/line/area/Heikin-Ashi) + volume
//! (BUILD-PLAN §1.6), viewport-culled.

import type { DataModel } from '../core/datamodel';
import type { TimeScale } from '../core/timescale';
import type { PriceScale } from '../core/pricescale';
import type { ChartColors, ChartType, Rect } from '../core/types';

function visibleSlice(data: DataModel, ts: TimeScale): { from: number; to: number } {
  const r = ts.visibleIndexRange();
  return {
    from: Math.max(0, Math.floor(r.from) - 1),
    to: Math.min(data.length - 1, Math.ceil(r.to) + 1),
  };
}

// ─── Heikin-Ashi transform (memoized by data version) ───────────────────────

type OHLC = { o: Float64Array; h: Float64Array; l: Float64Array; c: Float64Array };
let _haVer = -1;
let _ha: OHLC | null = null;
function heikinAshi(data: DataModel): OHLC {
  if (data.version === _haVer && _ha) return _ha;
  const n = data.length;
  const o = new Float64Array(n);
  const h = new Float64Array(n);
  const l = new Float64Array(n);
  const c = new Float64Array(n);
  for (let i = 0; i < n; i++) {
    const ro = data.open(i);
    const rh = data.high(i);
    const rl = data.low(i);
    const rc = data.close(i);
    c[i] = (ro + rh + rl + rc) / 4;
    o[i] = i === 0 ? (ro + rc) / 2 : (o[i - 1] + c[i - 1]) / 2;
    h[i] = Math.max(rh, o[i], c[i]);
    l[i] = Math.min(rl, o[i], c[i]);
  }
  _ha = { o, h, l, c };
  _haVer = data.version;
  return _ha;
}

/** Dispatch by chart type. */
export function drawMainSeries(
  ctx: CanvasRenderingContext2D,
  plot: Rect,
  data: DataModel,
  ts: TimeScale,
  ps: PriceScale,
  colors: ChartColors,
  type: ChartType,
): void {
  switch (type) {
    case 'bars':
      drawBars(ctx, plot, data, ts, ps, colors);
      break;
    case 'line':
      drawCloseLine(ctx, plot, data, ts, ps, colors, false);
      break;
    case 'area':
      drawCloseLine(ctx, plot, data, ts, ps, colors, true);
      break;
    case 'heikin-ashi':
      drawCandles(ctx, plot, data, ts, ps, colors, heikinAshi(data));
      break;
    case 'candles':
    default:
      drawCandles(ctx, plot, data, ts, ps, colors);
  }
}

export function drawCandles(
  ctx: CanvasRenderingContext2D,
  plot: Rect,
  data: DataModel,
  ts: TimeScale,
  ps: PriceScale,
  colors: ChartColors,
  view?: OHLC,
): void {
  const n = data.length;
  if (n === 0) return;
  const open = view ? (i: number) => view.o[i]! : (i: number) => data.open(i);
  const high = view ? (i: number) => view.h[i]! : (i: number) => data.high(i);
  const low = view ? (i: number) => view.l[i]! : (i: number) => data.low(i);
  const close = view ? (i: number) => view.c[i]! : (i: number) => data.close(i);
  const { from, to } = visibleSlice(data, ts);
  const half = Math.max(0.5, Math.min(ts.barSpacing * 0.34, ts.barSpacing / 2 - 0.5));
  const thin = ts.barSpacing < 3;

  for (let i = from; i <= to; i++) {
    const x = ts.indexToX(i);
    if (x < plot.x - ts.barSpacing || x > plot.x + plot.w + ts.barSpacing) continue;
    const o = open(i);
    const c = close(i);
    const up = c >= o;
    const wick = up ? colors.upWick : colors.downWick;
    const body = up ? colors.upBody : colors.downBody;
    const yHigh = ps.priceToY(high(i));
    const yLow = ps.priceToY(low(i));

    // wick
    ctx.strokeStyle = wick;
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(Math.round(x) + 0.5, yHigh);
    ctx.lineTo(Math.round(x) + 0.5, yLow);
    ctx.stroke();

    if (thin) continue;

    // body
    const yO = ps.priceToY(o);
    const yC = ps.priceToY(c);
    const top = Math.min(yO, yC);
    const h = Math.max(1, Math.abs(yC - yO));
    ctx.fillStyle = body;
    ctx.fillRect(Math.round(x - half), Math.round(top), Math.max(1, Math.round(half * 2)), Math.round(h));
  }
}

/** y-mapper for the volume band (bottom 14% of the price pane), or null when
 *  there is no volume in view — shared by the histogram and volume-scale lines. */
export function volumeMapper(plot: Rect, data: DataModel, ts: TimeScale): ((v: number) => number) | null {
  if (data.length === 0) return null;
  const { from, to } = visibleSlice(data, ts);
  const volMax = data.volumeMax(from, to);
  if (volMax <= 0) return null;
  const bandH = plot.h * 0.14;
  const baseline = plot.y + plot.h;
  return (v: number) => baseline - (v / volMax) * bandH;
}

export function drawVolume(
  ctx: CanvasRenderingContext2D,
  plot: Rect,
  data: DataModel,
  ts: TimeScale,
  colors: ChartColors,
): void {
  const n = data.length;
  if (n === 0) return;
  const { from, to } = visibleSlice(data, ts);
  const volMax = data.volumeMax(from, to);
  if (volMax <= 0) return;
  const bandH = plot.h * 0.14;
  const baseline = plot.y + plot.h;
  const half = Math.max(0.5, Math.min(ts.barSpacing * 0.34, ts.barSpacing / 2 - 0.5));

  for (let i = from; i <= to; i++) {
    const x = ts.indexToX(i);
    if (x < plot.x - ts.barSpacing || x > plot.x + plot.w + ts.barSpacing) continue;
    const h = (data.volume(i) / volMax) * bandH;
    if (h < 0.5) continue;
    ctx.fillStyle = data.close(i) >= data.open(i) ? colors.volumeUp : colors.volumeDown;
    ctx.fillRect(Math.round(x - half), baseline - h, Math.max(1, Math.round(half * 2)), h);
  }
}

/** OHLC bars: a high–low stick with a left tick (open) and right tick (close). */
function drawBars(
  ctx: CanvasRenderingContext2D,
  plot: Rect,
  data: DataModel,
  ts: TimeScale,
  ps: PriceScale,
  colors: ChartColors,
): void {
  const { from, to } = visibleSlice(data, ts);
  const tick = Math.max(1, Math.min(ts.barSpacing * 0.34, ts.barSpacing / 2 - 0.5));
  ctx.lineWidth = 1;
  for (let i = from; i <= to; i++) {
    const x = ts.indexToX(i);
    if (x < plot.x - ts.barSpacing || x > plot.x + plot.w + ts.barSpacing) continue;
    const o = data.open(i);
    const c = data.close(i);
    ctx.strokeStyle = c >= o ? colors.upBody : colors.downBody;
    const xa = Math.round(x) + 0.5;
    ctx.beginPath();
    ctx.moveTo(xa, ps.priceToY(data.high(i)));
    ctx.lineTo(xa, ps.priceToY(data.low(i)));
    const yO = Math.round(ps.priceToY(o)) + 0.5;
    ctx.moveTo(xa - tick, yO);
    ctx.lineTo(xa, yO);
    const yC = Math.round(ps.priceToY(c)) + 0.5;
    ctx.moveTo(xa, yC);
    ctx.lineTo(xa + tick, yC);
    ctx.stroke();
  }
}

/** Close-price line (and, for `area`, a translucent fill down to the baseline). */
function drawCloseLine(
  ctx: CanvasRenderingContext2D,
  plot: Rect,
  data: DataModel,
  ts: TimeScale,
  ps: PriceScale,
  colors: ChartColors,
  area: boolean,
): void {
  const { from, to } = visibleSlice(data, ts);
  if (to < from) return;
  const accent = colors.upBody;
  ctx.lineJoin = 'round';
  ctx.beginPath();
  for (let i = from; i <= to; i++) {
    const x = ts.indexToX(i);
    const y = ps.priceToY(data.close(i));
    if (i === from) ctx.moveTo(x, y);
    else ctx.lineTo(x, y);
  }
  if (area) {
    const base = plot.y + plot.h;
    ctx.lineTo(ts.indexToX(to), base);
    ctx.lineTo(ts.indexToX(from), base);
    ctx.closePath();
    const grad = ctx.createLinearGradient(0, plot.y, 0, base);
    grad.addColorStop(0, withAlpha(accent, 0.28));
    grad.addColorStop(1, withAlpha(accent, 0.02));
    ctx.fillStyle = grad;
    ctx.fill();
    // re-stroke just the top line
    ctx.beginPath();
    for (let i = from; i <= to; i++) {
      const x = ts.indexToX(i);
      const y = ps.priceToY(data.close(i));
      if (i === from) ctx.moveTo(x, y);
      else ctx.lineTo(x, y);
    }
  }
  ctx.strokeStyle = accent;
  ctx.lineWidth = 2;
  ctx.stroke();
}

/** Apply an alpha to a #rgb/#rrggbb/rgb()/rgba() color (best-effort). */
function withAlpha(color: string, a: number): string {
  if (color.startsWith('#')) {
    let h = color.slice(1);
    if (h.length === 3) h = h.split('').map((ch) => ch + ch).join('');
    const r = parseInt(h.slice(0, 2), 16);
    const g = parseInt(h.slice(2, 4), 16);
    const b = parseInt(h.slice(4, 6), 16);
    return `rgba(${r},${g},${b},${a})`;
  }
  const m = color.match(/^rgba?\(([^)]+)\)$/);
  if (m) {
    const parts = m[1].split(',').map((s) => s.trim());
    return `rgba(${parts[0]},${parts[1]},${parts[2]},${a})`;
  }
  return color;
}
