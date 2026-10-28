//! Pixel-space geometry for drawings: hit-testing and drag handles.
//
// Everything here works on projected pixel points (`Px`, with null for the
// coordinate a hline/vline doesn't carry) so it is framework- and scale-free.
// Rendering lives in render/drawings.ts; the manager in drawings/manager.ts.

import type { Drawing, DrawingType, HandleMode } from './model';
import { FIB_LEVELS } from './model';

export type Px = { x: number; y: number };
export type PxPoint = { x: number | null; y: number | null };
export type Dims = { width: number; height: number };

/** A drag handle: pixel position + which anchor/coordinate it edits. */
export type Handle = { x: number; y: number; pointIdx: number; mode: HandleMode; cursor?: string };

export function distToSegment(p: Px, a: Px, b: Px): number {
  const dx = b.x - a.x;
  const dy = b.y - a.y;
  const len2 = dx * dx + dy * dy;
  if (len2 === 0) return Math.hypot(p.x - a.x, p.y - a.y);
  let t = ((p.x - a.x) * dx + (p.y - a.y) * dy) / len2;
  t = Math.max(0, Math.min(1, t));
  return Math.hypot(p.x - (a.x + t * dx), p.y - (a.y + t * dy));
}

export function rectOf(a: Px, b: Px): { x: number; y: number; w: number; h: number } {
  return { x: Math.min(a.x, b.x), y: Math.min(a.y, b.y), w: Math.abs(b.x - a.x), h: Math.abs(b.y - a.y) };
}

function nearRectEdge(a: Px, b: Px, p: Px, tol: number): boolean {
  const r = rectOf(a, b);
  const insideX = p.x >= r.x - tol && p.x <= r.x + r.w + tol;
  const insideY = p.y >= r.y - tol && p.y <= r.y + r.h + tol;
  return (
    (Math.abs(p.x - r.x) <= tol && insideY) ||
    (Math.abs(p.x - (r.x + r.w)) <= tol && insideY) ||
    (Math.abs(p.y - r.y) <= tol && insideX) ||
    (Math.abs(p.y - (r.y + r.h)) <= tol && insideX)
  );
}

function insideRect(a: Px, b: Px, p: Px): boolean {
  const r = rectOf(a, b);
  return p.x >= r.x && p.x <= r.x + r.w && p.y >= r.y && p.y <= r.y + r.h;
}

/** Extend a ray from `a` through `b` out past the pane edge. */
export function rayEnd(a: Px, b: Px, dims: Dims): Px {
  const dx = b.x - a.x;
  const dy = b.y - a.y;
  if (dx === 0 && dy === 0) return b;
  const k = (dims.width + dims.height) / (Math.abs(dx) + Math.abs(dy));
  return { x: a.x + dx * k, y: a.y + dy * k };
}

/** Parallel channel: the second line is the base line shifted so it passes
 *  through the third point. Returns the offset in px. */
export function channelOffset(a: Px, b: Px, c: Px): { dx: number; dy: number } {
  // Vertical (price) offset keeps the lines parallel on screen.
  const dx = b.x - a.x;
  const t = dx === 0 ? 0 : (c.x - a.x) / dx;
  const yOnBase = a.y + (b.y - a.y) * t;
  return { dx: 0, dy: c.y - yOnBase };
}

const full = (p: PxPoint): p is Px => p.x != null && p.y != null;

/** Pixel points of a drawing with every coordinate present, else null. */
function complete(pts: PxPoint[], n: number): Px[] | null {
  if (pts.length < n) return null;
  const out: Px[] = [];
  for (let i = 0; i < n; i++) {
    const p = pts[i];
    if (!full(p)) return null;
    out.push(p);
  }
  return out;
}

export type HitResult = { part: 'body' | 'handle'; handle?: Handle };

/**
 * Does pixel `p` hit drawing `d` (projected as `pts`)? Handles are tested
 * first with a slightly larger radius so a corner grab wins over a body grab.
 */
export function hitTest(
  d: Drawing,
  pts: PxPoint[],
  p: Px,
  dims: Dims,
  tol: number,
  measureText: (s: string) => number,
  selected: boolean,
): HitResult | null {
  if (selected) {
    for (const h of handlesFor(d.type, pts, dims)) {
      if (Math.hypot(p.x - h.x, p.y - h.y) <= tol + 2) return { part: 'handle', handle: h };
    }
  }
  return bodyHit(d, pts, p, dims, tol, measureText) ? { part: 'body' } : null;
}

function bodyHit(d: Drawing, pts: PxPoint[], p: Px, dims: Dims, tol: number, measureText: (s: string) => number): boolean {
  switch (d.type) {
    case 'trendline':
    case 'arrow':
    case 'measure':
    case 'rect':
    case 'fib': {
      const c = complete(pts, 2);
      if (!c) return false;
      if (d.type === 'trendline' || d.type === 'arrow') return distToSegment(p, c[0], c[1]) <= tol;
      if (d.type === 'rect' || d.type === 'measure') return nearRectEdge(c[0], c[1], p, tol) || insideRect(c[0], c[1], p);
      const r = rectOf(c[0], c[1]);
      return p.x >= r.x - tol && p.x <= r.x + r.w + tol && p.y >= r.y - tol && p.y <= r.y + r.h + tol;
    }
    case 'ray': {
      const c = complete(pts, 2);
      return !!c && distToSegment(p, c[0], rayEnd(c[0], c[1], dims)) <= tol;
    }
    case 'hline':
      return pts[0]?.y != null && Math.abs(p.y - pts[0].y) <= tol;
    case 'vline':
      return pts[0]?.x != null && Math.abs(p.x - pts[0].x) <= tol;
    case 'channel': {
      const c = complete(pts, 3);
      if (!c) return false;
      const off = channelOffset(c[0], c[1], c[2]);
      const a2 = { x: c[0].x + off.dx, y: c[0].y + off.dy };
      const b2 = { x: c[1].x + off.dx, y: c[1].y + off.dy };
      if (distToSegment(p, c[0], c[1]) <= tol || distToSegment(p, a2, b2) <= tol) return true;
      // inside the parallelogram: between the two lines at this x
      const minX = Math.min(c[0].x, c[1].x);
      const maxX = Math.max(c[0].x, c[1].x);
      if (p.x < minX || p.x > maxX) return false;
      const t = c[1].x === c[0].x ? 0 : (p.x - c[0].x) / (c[1].x - c[0].x);
      const y1 = c[0].y + (c[1].y - c[0].y) * t;
      const y2 = y1 + off.dy;
      return p.y >= Math.min(y1, y2) && p.y <= Math.max(y1, y2);
    }
    case 'long':
    case 'short': {
      const box = positionBox(pts);
      if (!box) return false;
      return p.x >= box.x0 - tol && p.x <= box.x1 + tol && p.y >= box.top - tol && p.y <= box.bottom + tol;
    }
    case 'text': {
      const c = complete(pts, 1);
      if (!c) return false;
      const w = Math.max(20, measureText(d.text || 'Text') + 8);
      return p.x >= c[0].x - 4 && p.x <= c[0].x + w && p.y >= c[0].y - 14 && p.y <= c[0].y + 4;
    }
  }
}

/** Pixel extents of a position tool: entry x → end x, and the price band. */
export function positionBox(pts: PxPoint[]): { x0: number; x1: number; entryY: number; targetY: number; stopY: number; top: number; bottom: number } | null {
  const entry = pts[0];
  const end = pts[1];
  const stop = pts[2];
  if (!entry || !end || !stop || entry.x == null || entry.y == null || end.x == null || end.y == null || stop.y == null) return null;
  const x0 = Math.min(entry.x, end.x);
  const x1 = Math.max(entry.x, end.x);
  return {
    x0,
    x1,
    entryY: entry.y,
    targetY: end.y,
    stopY: stop.y,
    top: Math.min(entry.y, end.y, stop.y),
    bottom: Math.max(entry.y, end.y, stop.y),
  };
}

/** Drag handles for a drawing type given its projected points. */
export function handlesFor(type: DrawingType, pts: PxPoint[], dims: Dims): Handle[] {
  const both = (n: number): Handle[] => {
    const c = complete(pts, n);
    return c ? c.map((pt, i) => ({ x: pt.x, y: pt.y, pointIdx: i, mode: 'both' })) : [];
  };
  switch (type) {
    case 'trendline':
    case 'ray':
    case 'arrow':
    case 'rect':
    case 'fib':
    case 'measure':
      return both(2);
    case 'channel':
      return both(3);
    case 'hline':
      return pts[0]?.y != null ? [{ x: dims.width / 2, y: pts[0].y, pointIdx: 0, mode: 'price', cursor: 'ns-resize' }] : [];
    case 'vline':
      return pts[0]?.x != null ? [{ x: pts[0].x, y: dims.height / 2, pointIdx: 0, mode: 'time', cursor: 'ew-resize' }] : [];
    case 'text':
      return both(1);
    case 'long':
    case 'short': {
      const box = positionBox(pts);
      if (!box) return [];
      const midX = (box.x0 + box.x1) / 2;
      return [
        { x: midX, y: box.targetY, pointIdx: 1, mode: 'price', cursor: 'ns-resize' },
        { x: midX, y: box.stopY, pointIdx: 2, mode: 'price', cursor: 'ns-resize' },
        { x: box.x1, y: box.entryY, pointIdx: 1, mode: 'time', cursor: 'ew-resize' },
      ];
    }
  }
}

/** Bounding box (px) of a drawing — anchors the floating properties bar. */
export function boundsOf(d: Drawing, pts: PxPoint[], dims: Dims, measureText: (s: string) => number): { x: number; y: number; w: number; h: number } | null {
  switch (d.type) {
    case 'hline':
      return pts[0]?.y != null ? { x: 0, y: pts[0].y, w: dims.width, h: 0 } : null;
    case 'vline':
      return pts[0]?.x != null ? { x: pts[0].x, y: 0, w: 0, h: dims.height } : null;
    case 'text': {
      const c = complete(pts, 1);
      return c ? { x: c[0].x, y: c[0].y - 14, w: measureText(d.text || 'Text') + 8, h: 18 } : null;
    }
    case 'long':
    case 'short': {
      const box = positionBox(pts);
      return box ? { x: box.x0, y: box.top, w: box.x1 - box.x0, h: box.bottom - box.top } : null;
    }
    default: {
      const xs: number[] = [];
      const ys: number[] = [];
      for (const p of pts) {
        if (p.x != null) xs.push(p.x);
        if (p.y != null) ys.push(p.y);
      }
      if (d.type === 'channel' && pts.length === 3) {
        const c = complete(pts, 3);
        if (c) {
          const off = channelOffset(c[0], c[1], c[2]);
          ys.push(c[0].y + off.dy, c[1].y + off.dy);
        }
      }
      if (!xs.length || !ys.length) return null;
      const x = Math.min(...xs);
      const y = Math.min(...ys);
      return { x, y, w: Math.max(...xs) - x, h: Math.max(...ys) - y };
    }
  }
}

export { FIB_LEVELS };
