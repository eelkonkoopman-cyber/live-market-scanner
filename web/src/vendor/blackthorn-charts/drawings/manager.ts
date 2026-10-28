//! Drawing manager — placement state machine, hover / selection / drag,
//! undo & redo, and the event stream the app binds to.
//
// Pixel coordinates are price-pane local (the chart clips drawings to the
// price pane). The manager never touches the DOM: the chart feeds it
// pointer events and a `Projection`; the renderer reads its state.

import type { Drawing, DrawingPoint, DrawingTool, DrawingType, HandleMode } from './model';
import { POINTS_NEEDED, makeDrawing, moveDrawing, moveHandle, pointFor } from './model';
import { boundsOf, handlesFor, hitTest } from './geometry';
import type { Dims, Handle, Px, PxPoint } from './geometry';

export type Logical = { time: number; price: number };

/** What the chart lends the manager: logical↔pixel projection, snapping,
 *  text measurement and the bar interval (for the position tool's default
 *  width). */
export interface Projection {
  toPx(p: DrawingPoint): PxPoint;
  toLogical(px: Px): Logical;
  /** Magnet-aware: time snapped to the nearest bar, price to the nearest
   *  O/H/L/C when the magnet is on and the cursor is close to one. */
  snap(px: Px, magnet: boolean): Logical;
  dims(): Dims;
  measureText(s: string): number;
  interval(): number;
}

export type DrawingsEvent =
  | { type: 'change'; drawings: Drawing[] }
  | { type: 'select'; id: string | null }
  | { type: 'tool'; tool: DrawingTool }
  | { type: 'editText'; id: string }
  | { type: 'history'; canUndo: boolean; canRedo: boolean };

const CLICK_TOL = 4;
const HIT_TOL = 6;
const HISTORY_MAX = 60;

type Drag = { id: string; mode: 'body' | 'handle'; handle: Handle | null; orig: Drawing; start: Logical; base: Drawing[] };

export class DrawingManager {
  drawings: Drawing[] = [];
  selectedId: string | null = null;
  hoverId: string | null = null;
  hoverHandle: Handle | null = null;
  tool: DrawingTool = 'cursor';
  stayInMode = false;
  magnet = true;
  visible = true;
  hitTolerance = HIT_TOL;
  /** Placement in progress: logical points placed so far + the live cursor. */
  pending: DrawingPoint[] = [];
  preview: Px | null = null;

  private drag: Drag | null = null;
  /** A press in draw mode that may turn into drag-to-draw. */
  private press: Px | null = null;
  private undoStack: Drawing[][] = [];
  private redoStack: Drawing[][] = [];
  private subs = new Set<(e: DrawingsEvent) => void>();

  constructor(private proj: Projection) {}

  // ── events ────────────────────────────────────────────────────────────
  subscribe(cb: (e: DrawingsEvent) => void): void {
    this.subs.add(cb);
  }
  unsubscribe(cb: (e: DrawingsEvent) => void): void {
    this.subs.delete(cb);
  }
  private emit(e: DrawingsEvent): void {
    for (const cb of this.subs) cb(e);
  }
  private emitHistory(): void {
    this.emit({ type: 'history', canUndo: this.undoStack.length > 0, canRedo: this.redoStack.length > 0 });
  }

  // ── data ──────────────────────────────────────────────────────────────
  /** Hydrate from persistence: no history entry, selection dropped if gone. */
  set(list: Drawing[]): void {
    this.drawings = list.slice();
    if (this.selectedId && !this.drawings.some((d) => d.id === this.selectedId)) this.select(null);
    this.hoverId = null;
    this.hoverHandle = null;
  }
  get(): Drawing[] {
    return this.drawings;
  }
  private commit(next: Drawing[], base: Drawing[] = this.drawings): void {
    this.undoStack.push(base);
    if (this.undoStack.length > HISTORY_MAX) this.undoStack.shift();
    this.redoStack = [];
    this.drawings = next;
    this.emit({ type: 'change', drawings: this.drawings });
    this.emitHistory();
  }
  add(d: Drawing): void {
    this.commit([...this.drawings, d]);
  }
  update(id: string, patch: Partial<Drawing>): void {
    if (!this.drawings.some((d) => d.id === id)) return;
    this.commit(this.drawings.map((d) => (d.id === id ? { ...d, ...patch, style: { ...d.style, ...(patch.style ?? {}) } } : d)));
  }
  remove(id: string): void {
    if (!this.drawings.some((d) => d.id === id)) return;
    this.commit(this.drawings.filter((d) => d.id !== id));
    if (this.selectedId === id) this.select(null);
    if (this.hoverId === id) this.hoverId = null;
  }
  clear(): void {
    if (this.drawings.length === 0) return;
    this.commit([]);
    this.select(null);
    this.hoverId = null;
  }
  /** Duplicate a drawing, nudged right by a few bars so it is visibly new. */
  clone(id: string): Drawing | null {
    const src = this.drawings.find((d) => d.id === id);
    if (!src) return null;
    const dt = this.proj.interval() * 6;
    const copy = { ...moveDrawing(src, src.type === 'hline' ? 0 : dt, 0), id: makeDrawing(src.type, []).id, locked: false };
    this.add(copy);
    this.select(copy.id);
    return copy;
  }
  canUndo(): boolean {
    return this.undoStack.length > 0;
  }
  canRedo(): boolean {
    return this.redoStack.length > 0;
  }
  undo(): boolean {
    const prev = this.undoStack.pop();
    if (!prev) return false;
    this.redoStack.push(this.drawings);
    this.drawings = prev;
    if (this.selectedId && !prev.some((d) => d.id === this.selectedId)) this.select(null);
    this.emit({ type: 'change', drawings: this.drawings });
    this.emitHistory();
    return true;
  }
  redo(): boolean {
    const next = this.redoStack.pop();
    if (!next) return false;
    this.undoStack.push(this.drawings);
    this.drawings = next;
    if (this.selectedId && !next.some((d) => d.id === this.selectedId)) this.select(null);
    this.emit({ type: 'change', drawings: this.drawings });
    this.emitHistory();
    return true;
  }

  // ── selection / tool ──────────────────────────────────────────────────
  select(id: string | null): void {
    if (this.selectedId === id) return;
    this.selectedId = id;
    this.emit({ type: 'select', id });
  }
  getSelected(): Drawing | null {
    return this.drawings.find((d) => d.id === this.selectedId) ?? null;
  }
  setTool(tool: DrawingTool): void {
    this.pending = [];
    this.preview = null;
    this.press = null;
    if (this.tool === tool) return;
    this.tool = tool;
    if (tool !== 'cursor') this.select(null);
    this.emit({ type: 'tool', tool });
  }
  /** Escape: abandon a placement in progress, else drop the tool, else
   *  clear the selection. Returns true when something changed. */
  cancel(): boolean {
    if (this.pending.length > 0 || this.tool !== 'cursor') {
      this.setTool('cursor');
      return true;
    }
    if (this.selectedId) {
      this.select(null);
      return true;
    }
    return false;
  }
  get dragging(): boolean {
    return this.drag !== null;
  }
  get placing(): boolean {
    return this.tool !== 'cursor';
  }

  // ── projection helpers ────────────────────────────────────────────────
  projected(): { d: Drawing; pts: PxPoint[] }[] {
    return this.drawings.map((d) => ({ d, pts: d.points.map((p) => this.proj.toPx(p)) }));
  }
  selectionBounds(): { x: number; y: number; w: number; h: number } | null {
    const d = this.getSelected();
    if (!d) return null;
    return boundsOf(d, d.points.map((p) => this.proj.toPx(p)), this.proj.dims(), (s) => this.proj.measureText(s));
  }
  private hitAt(px: Px): { d: Drawing; part: 'body' | 'handle'; handle: Handle | null } | null {
    const dims = this.proj.dims();
    const measure = (s: string) => this.proj.measureText(s);
    // the selected drawing first (its handles win), then topmost-first
    const order = this.drawings.slice().reverse();
    const sel = this.getSelected();
    if (sel) order.unshift(sel);
    for (const d of order) {
      const pts = d.points.map((p) => this.proj.toPx(p));
      const hit = hitTest(d, pts, px, dims, this.hitTolerance, measure, d.id === this.selectedId);
      if (hit) return { d, part: hit.part, handle: hit.handle ?? null };
    }
    return null;
  }

  // ── input ─────────────────────────────────────────────────────────────
  /** Press. Returns what the chart should do: 'placed' / 'drag' mean the
   *  manager took the event (no pan); 'none' means fall through. */
  onDown(px: Px): 'placed' | 'drag' | 'none' {
    if (!this.visible) return 'none';
    if (this.tool !== 'cursor') {
      this.placePoint(px);
      this.press = px;
      return 'placed';
    }
    const hit = this.hitAt(px);
    if (!hit) {
      if (this.selectedId) this.select(null);
      return 'none';
    }
    this.select(hit.d.id);
    if (hit.d.locked) return 'drag'; // swallow the press, but nothing moves
    this.drag = {
      id: hit.d.id,
      mode: hit.part,
      handle: hit.handle,
      orig: hit.d,
      start: this.proj.toLogical(px),
      base: this.drawings,
    };
    return 'drag';
  }

  /** Move. Returns true when the drawings layer needs a repaint. */
  onMove(px: Px): boolean {
    if (this.drag) {
      const dr = this.drag;
      let next: Drawing;
      if (dr.mode === 'handle' && dr.handle) {
        next = moveHandle(dr.orig, dr.handle.pointIdx, dr.handle.mode, this.proj.snap(px, this.magnet));
      } else {
        const cur = this.proj.toLogical(px);
        next = moveDrawing(dr.orig, cur.time - dr.start.time, cur.price - dr.start.price);
      }
      this.drawings = this.drawings.map((d) => (d.id === dr.id ? next : d));
      this.emit({ type: 'change', drawings: this.drawings });
      return true;
    }
    if (this.tool !== 'cursor') {
      if (this.pending.length > 0) {
        this.preview = px;
        return true;
      }
      return false;
    }
    if (!this.visible) return false;
    const hit = this.hitAt(px);
    const id = hit?.d.id ?? null;
    const handle = hit?.handle ?? null;
    const changed = id !== this.hoverId || (handle?.pointIdx ?? -1) !== (this.hoverHandle?.pointIdx ?? -1) || (handle?.mode ?? '') !== (this.hoverHandle?.mode ?? '');
    this.hoverId = id;
    this.hoverHandle = handle;
    return changed;
  }

  /** Release: commits a drag, or finishes a drag-to-draw placement. */
  onUp(px: Px): void {
    if (this.drag) {
      const dr = this.drag;
      this.drag = null;
      const moved = this.drawings.find((d) => d.id === dr.id);
      if (moved && moved !== dr.orig) this.commit(this.drawings, dr.base);
      return;
    }
    if (this.press && this.tool !== 'cursor') {
      const moved = Math.hypot(px.x - this.press.x, px.y - this.press.y) > CLICK_TOL;
      this.press = null;
      const type = this.tool as DrawingType;
      // drag-to-draw: a press+drag places the second point on release
      if (moved && this.pending.length > 0 && this.pending.length === POINTS_NEEDED[type] - 1) this.placePoint(px);
    }
  }

  /** Double-click: opens text editing on a text drawing. */
  onDblClick(px: Px): boolean {
    if (this.tool !== 'cursor' || !this.visible) return false;
    const hit = this.hitAt(px);
    if (hit && hit.d.type === 'text') {
      this.select(hit.d.id);
      this.emit({ type: 'editText', id: hit.d.id });
      return true;
    }
    return false;
  }

  /** Cursor the chart should show, or null to leave it to the chart. */
  cursorFor(): string | null {
    if (this.drag) return this.drag.handle?.cursor ?? 'move';
    if (this.tool !== 'cursor') return 'crosshair';
    if (this.hoverHandle) return this.hoverHandle.cursor ?? 'pointer';
    if (this.hoverId) return 'pointer';
    return null;
  }

  private placePoint(px: Px): void {
    const type = this.tool as DrawingType;
    const logical = this.proj.snap(px, this.magnet);
    const next = [...this.pending, pointFor(type, logical)];
    if (next.length < POINTS_NEEDED[type]) {
      this.pending = next;
      this.preview = px;
      return;
    }
    this.pending = [];
    this.preview = null;
    const d = this.build(type, next);
    this.add(d);
    this.select(d.id);
    if (type === 'text') this.emit({ type: 'editText', id: d.id });
    if (!this.stayInMode) this.setTool('cursor');
  }

  /** Materialise a drawing from its placed points (position tools grow
   *  their target/stop/end anchors from the single entry click). */
  private build(type: DrawingType, points: DrawingPoint[]): Drawing {
    if (type === 'long' || type === 'short') {
      const entry = points[0];
      const price = entry.price ?? 0;
      const time = entry.time ?? 0;
      const long = type === 'long';
      const target = long ? price * 1.02 : price * 0.98;
      const stop = long ? price * 0.99 : price * 1.01;
      return makeDrawing(
        type,
        [entry, { time: time + this.proj.interval() * 20, price: target }, { price: stop }],
        { color: long ? '#22c55e' : '#ef4444' },
      );
    }
    return makeDrawing(type, points, undefined, type === 'text' ? '' : undefined);
  }

  /** Forget a press in progress (a second finger landed): the point already
   *  placed stays pending, nothing is committed. */
  abortPress(): void {
    this.press = null;
    this.drag = null;
  }

  /** Placement points projected to pixels (for the preview). */
  pendingPx(): PxPoint[] {
    return this.pending.map((p) => this.proj.toPx(p));
  }

  /** Handles of the selected drawing (for the renderer). */
  selectedHandles(): Handle[] {
    const d = this.getSelected();
    if (!d) return [];
    return handlesFor(d.type, d.points.map((p) => this.proj.toPx(p)), this.proj.dims());
  }
}

export type { Handle, HandleMode, Px, PxPoint, Dims };
