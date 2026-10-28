//! Drawing renderer — paints every annotation on its own canvas layer
//! (between the series and the crosshair), plus hover glow, selection
//! handles, the placement preview, and price/time chips on the axes.

import type { Drawing, DrawingPoint } from '../drawings/model';
import { FIB_LEVELS, POINTS_NEEDED } from '../drawings/model';
import { channelOffset, positionBox, rayEnd, rectOf } from '../drawings/geometry';
import type { Dims, Handle, Px, PxPoint } from '../drawings/geometry';
import type { ChartColors, LineStyle, Rect } from '../core/types';
import { align } from './layers';
import { font } from './fonts';

export type DrawingsView = {
  items: { d: Drawing; pts: PxPoint[] }[];
  selectedId: string | null;
  hoverId: string | null;
  handles: Handle[];
  pending: PxPoint[];
  pendingType: Drawing['type'] | null;
  preview: Px | null;
};

export type DrawingsFmt = {
  price(p: number): string;
  time(ts: number): string;
  /** Bars and elapsed wall time between two anchors (measure tool). */
  span(t0: number, t1: number): { bars: number; text: string };
  toPrice(y: number): number;
  toTime(x: number): number;
};

const labelFont = (): string => font(11, 'ui', '600');

function dash(ctx: CanvasRenderingContext2D, style: LineStyle): void {
  ctx.setLineDash(style === 'dashed' ? [6, 4] : style === 'dotted' ? [2, 3] : []);
}

function withAlpha(color: string, a: number): string {
  if (color.startsWith('#')) {
    let h = color.slice(1);
    if (h.length === 3) h = h.split('').map((c) => c + c).join('');
    return `rgba(${parseInt(h.slice(0, 2), 16)},${parseInt(h.slice(2, 4), 16)},${parseInt(h.slice(4, 6), 16)},${a})`;
  }
  const m = color.match(/^rgba?\(([^)]+)\)$/);
  if (m) {
    const p = m[1].split(',').map((s) => s.trim());
    return `rgba(${p[0]},${p[1]},${p[2]},${a})`;
  }
  return color;
}

const complete = (pts: PxPoint[], n: number): Px[] | null => {
  if (pts.length < n) return null;
  const out: Px[] = [];
  for (let i = 0; i < n; i++) {
    const p = pts[i];
    if (p.x == null || p.y == null) return null;
    out.push({ x: p.x, y: p.y });
  }
  return out;
};

function line(ctx: CanvasRenderingContext2D, a: Px, b: Px): void {
  ctx.beginPath();
  ctx.moveTo(a.x, a.y);
  ctx.lineTo(b.x, b.y);
  ctx.stroke();
}

function arrowHead(ctx: CanvasRenderingContext2D, a: Px, b: Px, size: number): void {
  const ang = Math.atan2(b.y - a.y, b.x - a.x);
  ctx.beginPath();
  ctx.moveTo(b.x, b.y);
  ctx.lineTo(b.x - size * Math.cos(ang - Math.PI / 6), b.y - size * Math.sin(ang - Math.PI / 6));
  ctx.lineTo(b.x - size * Math.cos(ang + Math.PI / 6), b.y - size * Math.sin(ang + Math.PI / 6));
  ctx.closePath();
  ctx.fill();
}

/** Small filled text box (position / measure annotations). */
function labelBox(ctx: CanvasRenderingContext2D, lines: string[], x: number, y: number, color: string, colors: ChartColors, anchor: 'left' | 'center' = 'left'): void {
  ctx.font = font(11);
  const padX = 6;
  const lineH = 14;
  let w = 0;
  for (const l of lines) w = Math.max(w, ctx.measureText(l).width);
  w += padX * 2;
  const h = lines.length * lineH + 6;
  const bx = anchor === 'center' ? x - w / 2 : x;
  ctx.fillStyle = withAlpha(colors.crosshairLabelBg, 0.92);
  ctx.strokeStyle = withAlpha(color, 0.6);
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.roundRect(bx, y, w, h, 3);
  ctx.fill();
  ctx.stroke();
  ctx.fillStyle = colors.legendText;
  ctx.textAlign = 'left';
  ctx.textBaseline = 'middle';
  lines.forEach((l, i) => ctx.fillText(l, bx + padX, y + 3 + lineH * i + lineH / 2));
}

function priceChip(ctx: CanvasRenderingContext2D, plot: Rect, axisW: number, y: number, text: string, color: string, colors: ChartColors): void {
  if (y < plot.y || y > plot.y + plot.h) return;
  const h = 16;
  ctx.font = font(11);
  ctx.fillStyle = color;
  ctx.fillRect(plot.x + plot.w, Math.round(y - h / 2), axisW, h);
  ctx.fillStyle = colors.background;
  ctx.textAlign = 'left';
  ctx.textBaseline = 'middle';
  ctx.fillText(text, plot.x + plot.w + 6, Math.round(y - h / 2) + h / 2 + 0.5);
}

function timeChip(ctx: CanvasRenderingContext2D, plot: Rect, contentH: number, x: number, text: string, color: string, colors: ChartColors): void {
  if (x < plot.x || x > plot.x + plot.w) return;
  ctx.font = font(11);
  const w = ctx.measureText(text).width + 12;
  ctx.fillStyle = color;
  ctx.fillRect(Math.round(x - w / 2), contentH + 1, w, 16);
  ctx.fillStyle = colors.background;
  ctx.textAlign = 'center';
  ctx.textBaseline = 'middle';
  ctx.fillText(text, x, contentH + 9);
}

const pct = (v: number) => `${v >= 0 ? '+' : ''}${v.toFixed(2)}%`;
const signed = (v: number, fmt: (n: number) => string) => `${v >= 0 ? '+' : '−'}${fmt(Math.abs(v))}`;

function drawShape(ctx: CanvasRenderingContext2D, d: Drawing, pts: PxPoint[], plot: Rect, dims: Dims, colors: ChartColors, fmt: DrawingsFmt, emphasis: number): void {
  const st = d.style;
  const color = st.color;
  ctx.strokeStyle = color;
  ctx.fillStyle = color;
  ctx.lineWidth = st.lineWidth + emphasis;
  ctx.lineCap = 'round';
  ctx.lineJoin = 'round';
  dash(ctx, st.lineStyle);
  switch (d.type) {
    case 'trendline': {
      const c = complete(pts, 2);
      if (c) line(ctx, c[0], c[1]);
      break;
    }
    case 'ray': {
      const c = complete(pts, 2);
      if (c) line(ctx, c[0], rayEnd(c[0], c[1], dims));
      break;
    }
    case 'arrow': {
      const c = complete(pts, 2);
      if (c) {
        line(ctx, c[0], c[1]);
        ctx.setLineDash([]);
        arrowHead(ctx, c[0], c[1], 8 + st.lineWidth * 2);
      }
      break;
    }
    case 'hline': {
      const y = pts[0]?.y;
      if (y != null) line(ctx, { x: plot.x, y }, { x: plot.x + plot.w, y });
      break;
    }
    case 'vline': {
      const x = pts[0]?.x;
      if (x != null) line(ctx, { x, y: plot.y }, { x, y: plot.y + plot.h });
      break;
    }
    case 'rect': {
      const c = complete(pts, 2);
      if (c) {
        const r = rectOf(c[0], c[1]);
        ctx.fillStyle = withAlpha(color, 0.08);
        ctx.fillRect(r.x, r.y, r.w, r.h);
        ctx.strokeRect(r.x, r.y, r.w, r.h);
      }
      break;
    }
    case 'fib': {
      const c = complete(pts, 2);
      if (!c) break;
      const x1 = Math.min(c[0].x, c[1].x);
      const x2 = Math.max(c[0].x, c[1].x);
      const p0 = d.points[0].price ?? 0;
      const p1 = d.points[1].price ?? 0;
      ctx.font = font(11);
      ctx.textAlign = 'left';
      ctx.textBaseline = 'bottom';
      ctx.setLineDash([4, 3]);
      ctx.lineWidth = 1 + emphasis;
      for (const lvl of FIB_LEVELS) {
        const y = c[0].y + (c[1].y - c[0].y) * lvl;
        line(ctx, { x: x1, y }, { x: x2, y });
        const price = p0 + (p1 - p0) * lvl;
        ctx.fillStyle = color;
        ctx.fillText(`${lvl.toFixed(3)}  ${fmt.price(price)}`, x1 + 4, y - 2);
      }
      break;
    }
    case 'channel': {
      const c = complete(pts, 3);
      if (!c) break;
      const off = channelOffset(c[0], c[1], c[2]);
      const a2 = { x: c[0].x + off.dx, y: c[0].y + off.dy };
      const b2 = { x: c[1].x + off.dx, y: c[1].y + off.dy };
      ctx.fillStyle = withAlpha(color, 0.08);
      ctx.beginPath();
      ctx.moveTo(c[0].x, c[0].y);
      ctx.lineTo(c[1].x, c[1].y);
      ctx.lineTo(b2.x, b2.y);
      ctx.lineTo(a2.x, a2.y);
      ctx.closePath();
      ctx.fill();
      line(ctx, c[0], c[1]);
      line(ctx, a2, b2);
      break;
    }
    case 'measure': {
      const c = complete(pts, 2);
      if (!c) break;
      const r = rectOf(c[0], c[1]);
      const t0 = d.points[0].time ?? 0;
      const t1 = d.points[1].time ?? 0;
      const pr0 = d.points[0].price ?? 0;
      const pr1 = d.points[1].price ?? 0;
      const up = pr1 >= pr0;
      const tone = up ? colors.upBody : colors.downBody;
      ctx.fillStyle = withAlpha(tone, 0.12);
      ctx.fillRect(r.x, r.y, r.w, r.h);
      ctx.strokeStyle = withAlpha(tone, 0.8);
      ctx.setLineDash([]);
      ctx.lineWidth = 1 + emphasis;
      ctx.strokeRect(r.x, r.y, r.w, r.h);
      const dp = pr1 - pr0;
      const span = fmt.span(Math.min(t0, t1), Math.max(t0, t1));
      labelBox(
        ctx,
        [`${signed(dp, fmt.price)}  (${pct(pr0 !== 0 ? (dp / pr0) * 100 : 0)})`, `${span.bars} bars · ${span.text}`],
        r.x + r.w / 2,
        up ? r.y - 40 : r.y + r.h + 6,
        tone,
        colors,
        'center',
      );
      break;
    }
    case 'long':
    case 'short': {
      const box = positionBox(pts);
      if (!box) break;
      const entry = d.points[0].price ?? 0;
      const target = d.points[1].price ?? 0;
      const stop = d.points[2].price ?? 0;
      const w = box.x1 - box.x0;
      const gain = colors.upBody;
      const loss = colors.downBody;
      // profit zone (entry ↔ target), loss zone (entry ↔ stop)
      ctx.fillStyle = withAlpha(gain, 0.12);
      ctx.fillRect(box.x0, Math.min(box.entryY, box.targetY), w, Math.abs(box.targetY - box.entryY));
      ctx.fillStyle = withAlpha(loss, 0.12);
      ctx.fillRect(box.x0, Math.min(box.entryY, box.stopY), w, Math.abs(box.stopY - box.entryY));
      ctx.setLineDash([]);
      ctx.lineWidth = 1 + emphasis;
      ctx.strokeStyle = gain;
      line(ctx, { x: box.x0, y: align(box.targetY) }, { x: box.x1, y: align(box.targetY) });
      ctx.strokeStyle = loss;
      line(ctx, { x: box.x0, y: align(box.stopY) }, { x: box.x1, y: align(box.stopY) });
      ctx.strokeStyle = colors.legendText;
      ctx.setLineDash([3, 3]);
      line(ctx, { x: box.x0, y: align(box.entryY) }, { x: box.x1, y: align(box.entryY) });
      ctx.setLineDash([]);
      const risk = Math.abs(entry - stop);
      const reward = Math.abs(target - entry);
      const r = risk > 0 ? reward / risk : 0;
      const tgtPct = entry !== 0 ? ((target - entry) / entry) * 100 : 0;
      const stopPct = entry !== 0 ? ((stop - entry) / entry) * 100 : 0;
      const targetAbove = box.targetY <= box.stopY;
      labelBox(ctx, [`Target ${fmt.price(target)}  ${signed(target - entry, fmt.price)} (${pct(tgtPct)})  ${r.toFixed(2)}R`], box.x0, targetAbove ? box.targetY - 22 : box.targetY + 4, gain, colors);
      labelBox(ctx, [`Stop ${fmt.price(stop)}  ${signed(stop - entry, fmt.price)} (${pct(stopPct)})`], box.x0, targetAbove ? box.stopY + 4 : box.stopY - 22, loss, colors);
      labelBox(ctx, [`${d.type === 'long' ? 'Long' : 'Short'} ${fmt.price(entry)}  · risk ${fmt.price(risk)}`], box.x1 + 6, box.entryY - 10, colors.legendText, colors);
      break;
    }
    case 'text': {
      const c = complete(pts, 1);
      if (!c) break;
      ctx.setLineDash([]);
      ctx.font = font(12);
      ctx.textAlign = 'left';
      ctx.textBaseline = 'alphabetic';
      const text = d.text || 'Text';
      if (emphasis > 0) {
        const w = ctx.measureText(text).width;
        ctx.fillStyle = withAlpha(color, 0.12);
        ctx.fillRect(c[0].x - 3, c[0].y - 12, w + 6, 16);
      }
      ctx.fillStyle = d.text ? color : withAlpha(color, 0.5);
      ctx.fillText(text, c[0].x, c[0].y);
      break;
    }
  }
  ctx.setLineDash([]);
}

/** Price/time chips the drawing shows on the axes (hline/vline always; the
 *  selected drawing's anchors too). */
function drawChips(ctx: CanvasRenderingContext2D, d: Drawing, pts: PxPoint[], plot: Rect, axisW: number, contentH: number, colors: ChartColors, fmt: DrawingsFmt, selected: boolean): void {
  const color = d.style.color;
  if (d.type === 'hline' && pts[0]?.y != null) priceChip(ctx, plot, axisW, pts[0].y, fmt.price(d.points[0].price ?? 0), color, colors);
  else if (d.type === 'vline' && pts[0]?.x != null) timeChip(ctx, plot, contentH, pts[0].x, fmt.time(d.points[0].time ?? 0), color, colors);
  else if (selected) {
    d.points.forEach((p, i) => {
      const px = pts[i];
      if (!px) return;
      if (p.price != null && px.y != null) priceChip(ctx, plot, axisW, px.y, fmt.price(p.price), color, colors);
      if (p.time != null && px.x != null && d.type !== 'long' && d.type !== 'short') timeChip(ctx, plot, contentH, px.x, fmt.time(p.time), color, colors);
    });
  }
}

export function drawDrawings(
  ctx: CanvasRenderingContext2D,
  plot: Rect,
  axisW: number,
  contentH: number,
  view: DrawingsView,
  colors: ChartColors,
  fmt: DrawingsFmt,
): void {
  const dims: Dims = { width: plot.w, height: plot.h };

  // shapes, clipped to the price pane
  ctx.save();
  ctx.beginPath();
  ctx.rect(plot.x, plot.y, plot.w, plot.h);
  ctx.clip();
  for (const { d, pts } of view.items) {
    const hover = d.id === view.hoverId && d.id !== view.selectedId;
    if (hover) {
      ctx.save();
      ctx.globalAlpha = 0.3;
      drawShape(ctx, d, pts, plot, dims, colors, fmt, 3);
      ctx.restore();
    }
    drawShape(ctx, d, pts, plot, dims, colors, fmt, 0);
  }

  // placement preview
  if (view.pendingType && view.pending.length > 0 && view.preview) {
    const p0 = view.pending[0];
    const x1 = p0.x ?? view.preview.x;
    const y1 = p0.y ?? view.preview.y;
    ctx.strokeStyle = colors.accent;
    ctx.lineWidth = 1;
    ctx.setLineDash([4, 3]);
    const t = view.pendingType;
    if (t === 'rect' || t === 'fib' || t === 'measure') {
      const r = rectOf({ x: x1, y: y1 }, view.preview);
      ctx.strokeRect(r.x, r.y, r.w, r.h);
    } else if (t === 'channel' && view.pending.length === 2) {
      const p1 = view.pending[1];
      const a = { x: x1, y: y1 };
      const b = { x: p1.x ?? view.preview.x, y: p1.y ?? view.preview.y };
      line(ctx, a, b);
      const off = channelOffset(a, b, view.preview);
      line(ctx, { x: a.x, y: a.y + off.dy }, { x: b.x, y: b.y + off.dy });
    } else {
      line(ctx, { x: x1, y: y1 }, view.preview);
    }
    ctx.setLineDash([]);
    if (POINTS_NEEDED[t] > 1) {
      ctx.fillStyle = colors.accent;
      ctx.beginPath();
      ctx.arc(x1, y1, 3, 0, Math.PI * 2);
      ctx.fill();
    }
  }

  // selection handles
  const sel = view.items.find((it) => it.d.id === view.selectedId);
  if (sel) {
    for (const h of view.handles) {
      ctx.beginPath();
      ctx.arc(h.x, h.y, 4.5, 0, Math.PI * 2);
      ctx.fillStyle = colors.background;
      ctx.fill();
      ctx.lineWidth = 2;
      ctx.strokeStyle = sel.d.style.color;
      ctx.stroke();
    }
  }
  ctx.restore();

  // axis chips (outside the pane clip)
  for (const { d, pts } of view.items) drawChips(ctx, d, pts, plot, axisW, contentH, colors, fmt, d.id === view.selectedId);
}

export type { DrawingPoint };
export { labelFont };
