//! Layered Canvas 2D manager (BUILD-PLAN §1.5).
//
// Stacks CSS-absolute <canvas> elements back-to-front so cheap, frequent updates
// (crosshair) never force a redraw of expensive layers (series). Redraws are
// dirty-tracked and coalesced into a single rAF.

export type LayerName = 'grid' | 'sessions' | 'series' | 'drawings' | 'crosshair';
export const LAYER_ORDER: LayerName[] = ['grid', 'sessions', 'series', 'drawings', 'crosshair'];

/** Snap to a half-pixel for crisp 1px strokes. */
export const align = (v: number): number => Math.round(v) + 0.5;

export class LayerManager {
  private layers = new Map<LayerName, { canvas: HTMLCanvasElement; ctx: CanvasRenderingContext2D }>();
  private dirty = new Set<LayerName>();
  private raf = 0;
  private dpr = 1;
  width = 0;
  height = 0;

  constructor(
    container: HTMLElement,
    private onDraw: (name: LayerName, ctx: CanvasRenderingContext2D) => void,
  ) {
    const cstyle = container.style;
    if (getComputedStyle(container).position === 'static') cstyle.position = 'relative';
    cstyle.overflow = 'hidden';
    for (const name of LAYER_ORDER) {
      const canvas = document.createElement('canvas');
      const s = canvas.style;
      s.position = 'absolute';
      s.left = '0';
      s.top = '0';
      s.width = '100%';
      s.height = '100%';
      // only the top layer needs to receive mouse events; the chart attaches
      // its listeners to the container, so let events pass through.
      s.pointerEvents = 'none';
      container.appendChild(canvas);
      const ctx = canvas.getContext('2d');
      if (!ctx) throw new Error('2D canvas context unavailable');
      this.layers.set(name, { canvas, ctx });
    }
  }

  ctx(name: LayerName): CanvasRenderingContext2D {
    return this.layers.get(name)!.ctx;
  }

  resize(width: number, height: number): void {
    this.width = width;
    this.height = height;
    this.dpr = window.devicePixelRatio || 1;
    for (const { canvas, ctx } of this.layers.values()) {
      canvas.width = Math.max(1, Math.round(width * this.dpr));
      canvas.height = Math.max(1, Math.round(height * this.dpr));
      ctx.setTransform(this.dpr, 0, 0, this.dpr, 0, 0);
    }
    this.invalidateAll();
  }

  invalidate(name: LayerName): void {
    this.dirty.add(name);
    this.schedule();
  }

  invalidateAll(): void {
    for (const n of LAYER_ORDER) this.dirty.add(n);
    this.schedule();
  }

  private schedule(): void {
    if (this.raf) return;
    this.raf = requestAnimationFrame(() => {
      this.raf = 0;
      const todo = [...this.dirty];
      this.dirty.clear();
      for (const name of todo) {
        const ctx = this.ctx(name);
        ctx.clearRect(0, 0, this.width, this.height);
        this.onDraw(name, ctx);
      }
    });
  }

  destroy(): void {
    if (this.raf) cancelAnimationFrame(this.raf);
    this.raf = 0;
    for (const { canvas } of this.layers.values()) canvas.remove();
    this.layers.clear();
  }
}
