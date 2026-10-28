//! Session shading renderer — the priority feature (BUILD-PLAN §2.5).
//
// Bands are anchored to BAR INDICES, not pixels: each band's [startTs, endTs] is
// resolved to the first bar at/after that instant, then projected through the
// index scale. Ranges that extend past the visible edge are CLAMPED, not dropped
// — the fix for the old `timeToCoordinate === null` right-edge dropouts.

import type { DataModel } from '../core/datamodel';
import type { TimeScale } from '../core/timescale';
import type { Rect } from '../core/types';
import type { BandRange } from '../sessions/sessionmodel';
import { align } from './layers';

/** Multiply the alpha of an rgba()/rgb()/#hex colour by k (best effort). */
function scaleAlpha(color: string, k: number): string {
  const m = color.match(/^rgba?\(([^)]+)\)$/);
  if (m) {
    const parts = m[1].split(',').map((s) => s.trim());
    const a = parts.length > 3 ? Number(parts[3]) : 1;
    return `rgba(${parts[0]},${parts[1]},${parts[2]},${(a * k).toFixed(3)})`;
  }
  if (color.startsWith('#')) {
    let h = color.slice(1);
    if (h.length === 3) h = h.split('').map((c) => c + c).join('');
    const r = parseInt(h.slice(0, 2), 16);
    const g = parseInt(h.slice(2, 4), 16);
    const b = parseInt(h.slice(4, 6), 16);
    return `rgba(${r},${g},${b},${k.toFixed(3)})`;
  }
  return color;
}

export function drawSessions(
  ctx: CanvasRenderingContext2D,
  plot: Rect,
  ranges: BandRange[],
  data: DataModel,
  ts: TimeScale,
): void {
  if (data.length === 0) return;
  const half = ts.barSpacing / 2;
  const right = plot.x + plot.w;
  // Zoomed far out the bands would tile the whole background; fade them so
  // the price action stays the subject (full strength from 4 px per bar).
  const fade = Math.max(0.3, Math.min(1, (ts.barSpacing - 0.5) / 3.5));

  for (const band of ranges) {
    // left edge = left side of the first bar >= startTs
    // right edge = left side of the first bar >= endTs (= right side of last
    // in-session bar when no bar starts exactly at endTs, e.g. data ends 19:55)
    const lbStart = data.lowerBound(band.startTs);
    const lbEnd = data.lowerBound(band.endTs);
    let xL = ts.indexToX(lbStart) - half;
    let xR = ts.indexToX(lbEnd) - half;
    if (xR <= plot.x || xL >= right) continue; // fully off-screen
    xL = Math.max(plot.x, xL);
    xR = Math.min(right, xR);
    if (xR - xL < 0.5) continue;

    ctx.fillStyle = fade < 1 ? scaleAlpha(band.color, fade) : band.color;
    ctx.fillRect(xL, plot.y, xR - xL, plot.h);

    if (band.border) {
      ctx.strokeStyle = band.color.replace(/[\d.]+\)$/, '0.5)');
      ctx.lineWidth = 1;
      if (band.border === 'dashed') ctx.setLineDash([4, 3]);
      ctx.beginPath();
      ctx.moveTo(align(xL), plot.y);
      ctx.lineTo(align(xL), plot.y + plot.h);
      ctx.moveTo(align(xR), plot.y);
      ctx.lineTo(align(xR), plot.y + plot.h);
      ctx.stroke();
      ctx.setLineDash([]);
    }
  }
}
