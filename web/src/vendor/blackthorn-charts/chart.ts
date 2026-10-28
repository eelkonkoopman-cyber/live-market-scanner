//! Public chart API + input controller (BUILD-PLAN §1.8–1.9, §3, §4.2).
//
// `createChart(container, options)` builds the layered canvas, owns the data
// model + shared time scale + a native pane stack (price pane + oscillator
// panes), wires pan / zoom / crosshair / click / pane-divider drag, and renders
// every pane into the one canvas stack (clipped per pane). It also exposes the
// coordinate + visible-range + click APIs the app's drawing overlay and
// pattern-marking handle need.

import { DataModel } from './core/datamodel';
import { TimeScale } from './core/timescale';
import { PaneLayout } from './core/panes';
import type { Pane } from './core/panes';
import { LayerManager } from './render/layers';
import type { LayerName } from './render/layers';
import { drawPriceAxis, priceTicks, axisFont } from './render/grid';
import { drawTimeAxis } from './render/timeaxis';
import { drawMainSeries, drawVolume, volumeMapper } from './render/series';
import { drawPaneStudies } from './render/studies';
import { drawSessions } from './render/sessions';
import { drawMarkers } from './render/markers';
import { drawPriceLines } from './render/pricelines';
import { drawLastPrice } from './render/lastprice';
import { drawCrosshair } from './render/crosshair';
import { drawDrawings } from './render/drawings';
import { DrawingManager } from './drawings/manager';
import type { DrawingsEvent } from './drawings/manager';
import type { Drawing, DrawingTool } from './drawings/model';
import { MIN_BAR_SPACING, MAX_BAR_SPACING } from './core/timescale';
import { IndicatorController } from './indicators/controller';
import type { IndicatorInstance } from './indicators/registry';
import type { IndicatorValue } from './indicators/controller';
import { sessionRanges } from './sessions/sessionmodel';
import type { SessionDef, BandRange } from './sessions/sessionmodel';
import { DEFAULT_COLORS, PRICE_AXIS_W } from './core/types';
import type { Candle, ChartColors, ChartOptions, ChartType, Execution, PriceLine, Rect } from './core/types';
import { font, setChartFonts } from './render/fonts';

/** A chart click, emitted to `subscribeClick` subscribers (Mark-pattern pick). */
export type ClickEvent = { ts: number; price: number; bar: Candle | null };

export interface ChartApi {
  setData(candles: Candle[]): void;
  /** Live tick: append a bar, or update the forming one when `ts` repeats.
   *  Unlike setData this preserves the user's pan/zoom. */
  update(candle: Candle): void;
  setMarkers(executions: Execution[]): void;
  /** horizontal reference lines on the price pane (prior close, HOD/LOD, levels). */
  setPriceLines(lines: PriceLine[]): void;
  /** overlay + oscillator indicators (lib computes + renders + manages panes). */
  setIndicators(instances: IndicatorInstance[]): void;
  /** Pane geometry (container px) — the app overlays legends on it. */
  panes(): { id: string; kind: 'price' | 'oscillator'; rect: Rect }[];
  /** Legend values for every instance at bar `idx` (null = the last bar). */
  indicatorValuesAt(idx: number | null): { instanceId: string; values: IndicatorValue[] }[];
  /** Bar index under the crosshair, or null. */
  hoverIndex(): number | null;
  subscribeHover(cb: (idx: number | null) => void): void;
  unsubscribeHover(cb: (idx: number | null) => void): void;
  setChartType(type: ChartType): void;
  setVolumeVisible(v: boolean): void;
  setGridVisible(v: boolean): void;
  sessions: { apply(def: SessionDef): void; toggle(visible: boolean): void };
  timeScale(): {
    fitContent(): void;
    getVisibleRange(): { from: number; to: number } | null;
    setVisibleRange(r: { from: number; to: number }): void;
    scrollToTime(ts: number): void;
    /** keep the zoom, put the last bar at the right edge ("go to latest"). */
    scrollToLatest(): void;
    /** default zoom + latest bar at the right edge (double-click on the time axis). */
    resetView(): void;
    /** true while the last bar is on screen. */
    isAtLatest(): boolean;
    /** pan by a number of bars (positive = later bars; keyboard arrows). */
    scrollBy(bars: number): void;
    /** zoom by a factor around the plot centre (keyboard + / −). */
    zoomBy(factor: number): void;
  };
  /** price (Y) axis: auto-fit by default, or a user-pinned manual range that
   *  survives live ticks/data updates until explicitly reset — mirrors
   *  timeScale()'s auto/manual shape. */
  priceScale(): {
    isAutoScale(): boolean;
    setAutoScale(auto: boolean): void;
    setRange(min: number, max: number): void;
    getRange(): { min: number; max: number } | null;
  };
  /** Annotations: rendered on their own canvas layer, hit-tested and dragged
   *  by the engine. The app owns persistence + keyboard; it subscribes to
   *  `change` / `select` / `tool` / `editText` / `history` events. */
  drawings: {
    set(list: Drawing[]): void;
    get(): Drawing[];
    add(d: Drawing): void;
    update(id: string, patch: Partial<Drawing>): void;
    remove(id: string): void;
    clear(): void;
    clone(id: string): Drawing | null;
    select(id: string | null): void;
    getSelected(): Drawing | null;
    setTool(tool: DrawingTool): void;
    getTool(): DrawingTool;
    setStayInMode(v: boolean): void;
    setMagnet(v: boolean): void;
    setVisible(v: boolean): void;
    /** Escape: abandon placement → drop the tool → clear the selection. */
    cancel(): boolean;
    undo(): boolean;
    redo(): boolean;
    canUndo(): boolean;
    canRedo(): boolean;
    /** Pixel bounds (price-pane coords) of the selected drawing, for a
     *  floating properties bar. */
    selectionBounds(): Rect | null;
    subscribe(cb: (e: DrawingsEvent) => void): void;
    unsubscribe(cb: (e: DrawingsEvent) => void): void;
  };
  /** price-pane logical↔pixel projection. */
  timeToCoordinate(ts: number): number | null;
  coordinateToTime(x: number): number | null;
  priceToCoordinate(price: number): number | null;
  coordinateToPrice(y: number): number | null;
  pricePaneRect(): Rect;
  subscribeVisibleRangeChange(cb: () => void): void;
  unsubscribeVisibleRangeChange(cb: () => void): void;
  /** fires when the price axis flips between auto and manual (pin/reset,
   *  or a drag/wheel gesture on the price axis first takes manual control). */
  subscribePriceScaleChange(cb: () => void): void;
  unsubscribePriceScaleChange(cb: () => void): void;
  subscribeClick(cb: (e: ClickEvent) => void): void;
  unsubscribeClick(cb: (e: ClickEvent) => void): void;
  applyOptions(o: Partial<ChartOptions>): void;
  remove(): void;
}

const CLICK_TOL = 4; // px movement below which a press counts as a click
const DIVIDER_TOL = 4; // px around a pane boundary that grabs the divider

export function createChart(container: HTMLElement, options: ChartOptions = {}): ChartApi {
  const colors: ChartColors = { ...DEFAULT_COLORS, ...(options.colors || {}) };
  if (options.fonts) setChartFonts(options.fonts);
  const tz = options.timezone || 'America/New_York';
  let showBands = options.showSessionBands !== false;
  let showVolume = options.showVolume !== false;
  let showGrid = options.showGrid !== false;
  let showLegend = options.showLegend !== false;
  let showLastPrice = options.showLastPrice !== false;
  let legendTitle = options.legendTitle ?? '';
  let chartType: ChartType = options.chartType || 'candles';

  const data = new DataModel();
  const ts = new TimeScale();
  if (options.rightOffset != null) ts.rightOffset = options.rightOffset;
  const layout = new PaneLayout();
  const indCtrl = new IndicatorController(layout);
  let indicators: IndicatorInstance[] = [];

  let executions: Execution[] = [];
  let priceLines: PriceLine[] = [];
  let sessionDef: SessionDef | null = null;
  let bandCache: { key: string; ranges: BandRange[] } | null = null;
  let userInteracted = false;
  // Persistent manual-price-scale flag (mirrors userInteracted for the time
  // axis). Once true, `layout.pricePane.range` holds the pinned {min,max} and
  // NOTHING but an explicit priceScale().setAutoScale(true) may clear it —
  // not setData, not update, not a resize/recomputeRanges pass. This is the
  // "silent auto-scale reset" guard: see recomputeRanges()'s price-pane branch.
  let priceUserSet = false;
  let mouse: { x: number; y: number } | null = null;

  const viewSubs = new Set<() => void>();
  const hoverSubs = new Set<(idx: number | null) => void>();
  let hoverIdx: number | null = null;
  function updateHover(p: { x: number; y: number } | null): void {
    let idx: number | null = null;
    if (p && data.length > 0 && p.x >= 0 && p.x <= layout.plotW && p.y >= 0 && p.y <= layout.contentH) {
      idx = Math.max(0, Math.min(data.length - 1, Math.round(ts.xToIndex(p.x))));
    }
    if (idx === hoverIdx) return;
    hoverIdx = idx;
    for (const cb of hoverSubs) cb(idx);
  }
  const priceScaleSubs = new Set<() => void>();
  const clickSubs = new Set<(e: ClickEvent) => void>();

  container.style.background = colors.background;

  // ── ranges ──────────────────────────────────────────────────────────────
  function visIdx(): { a: number; b: number } {
    const r = ts.visibleIndexRange();
    return { a: Math.max(0, Math.floor(r.from)), b: Math.min(data.length - 1, Math.ceil(r.to)) };
  }

  function seriesMinMax(values: (number | null)[], a: number, b: number, acc: { lo: number; hi: number }): void {
    for (let i = a; i <= b; i++) {
      const v = values[i];
      if (v == null || Number.isNaN(v)) continue;
      if (v < acc.lo) acc.lo = v;
      if (v > acc.hi) acc.hi = v;
    }
  }

  function recomputeRanges(): void {
    if (data.length === 0) return;
    // The gutter width depends on the labels, the labels on the ranges, the
    // ranges on the plot width: one extra pass settles it.
    for (let pass = 0; pass < 2; pass++) {
      computeRanges();
      if (!syncAxisWidth()) break;
    }
  }

  /** Widen (or narrow) the price-axis gutter to the widest label it will
   *  show. Returns true when the layout changed. */
  function syncAxisWidth(): boolean {
    const ctx = lm.ctx('grid');
    ctx.font = axisFont();
    let w = 0;
    for (const pane of layout.panes) {
      const r = pane.scale.range();
      const t = priceTicks(r.min, r.max, pane.rect.h);
      for (const v of t.values) w = Math.max(w, ctx.measureText(v.toFixed(t.decimals)).width);
    }
    const last = data.close(data.length - 1);
    w = Math.max(w, ctx.measureText(last.toFixed(Math.abs(last) < 1 ? 4 : 2)).width);
    const want = Math.max(PRICE_AXIS_W, Math.min(140, Math.ceil((w + 14) / 8) * 8));
    if (want === layout.axisW) return false;
    layout.axisW = want;
    const rect = container.getBoundingClientRect();
    layout.recompute(rect.width || 600, rect.height || 400);
    ts.setWidth(layout.plotW);
    return true;
  }

  function computeRanges(): void {
    const { a, b } = visIdx();

    // price pane: pinned range (manual scale), else autofit to candles +
    // executions + overlay study extents — mirrors the oscillator branch
    // below exactly: a pinned range wins over auto-compute, full stop.
    if (layout.pricePane.range) {
      layout.pricePane.scale.applyAutoRange(layout.pricePane.range.min, layout.pricePane.range.max);
    } else {
      const hl = data.highLow(a, b);
      const acc = { lo: hl.min, hi: hl.max };
      for (const ex of executions) {
        const idx = data.timeToIndex(ex.ts);
        if (idx >= a && idx <= b) {
          if (ex.price < acc.lo) acc.lo = ex.price;
          if (ex.price > acc.hi) acc.hi = ex.price;
        }
      }
      for (const s of layout.pricePane.series) seriesMinMax(s.values, a, b, acc);
      for (const pl of priceLines) {
        if (!Number.isFinite(pl.price)) continue;
        if (pl.price < acc.lo) acc.lo = pl.price;
        if (pl.price > acc.hi) acc.hi = pl.price;
      }
      const pad = (acc.hi - acc.lo) * 0.06 || 1;
      layout.pricePane.scale.applyAutoRange(acc.lo - pad, acc.hi + pad);
    }

    // oscillator panes: pinned range, else autofit to series + guides
    for (const pane of layout.oscillators) {
      if (pane.range) {
        pane.scale.applyAutoRange(pane.range.min, pane.range.max);
        continue;
      }
      const oacc = { lo: Infinity, hi: -Infinity };
      for (const s of pane.series) seriesMinMax(s.values, a, b, oacc);
      for (const g of pane.guides) {
        if (g.value < oacc.lo) oacc.lo = g.value;
        if (g.value > oacc.hi) oacc.hi = g.value;
      }
      if (!isFinite(oacc.lo) || !isFinite(oacc.hi)) pane.scale.applyAutoRange(0, 1);
      else {
        const op = (oacc.hi - oacc.lo) * 0.1 || 1;
        pane.scale.applyAutoRange(oacc.lo - op, oacc.hi + op);
      }
    }
  }

  function bands(): BandRange[] {
    if (!sessionDef || data.length === 0) return [];
    const from = data.time(0);
    const to = data.time(data.length - 1);
    const key = `${from}|${to}|${sessionDef.name}|${data.version}`;
    if (!bandCache || bandCache.key !== key) bandCache = { key, ranges: sessionRanges(from, to, sessionDef) };
    return bandCache.ranges;
  }

  // ── render ──────────────────────────────────────────────────────────────
  function contentRect(): Rect {
    return { x: 0, y: 0, w: layout.plotW, h: layout.contentH };
  }

  function drawPane(ctx: CanvasRenderingContext2D, pane: Pane): void {
    ctx.save();
    ctx.beginPath();
    ctx.rect(pane.rect.x, pane.rect.y, pane.rect.w, pane.rect.h);
    ctx.clip();
    if (pane.kind === 'price') {
      drawMainSeries(ctx, pane.rect, data, ts, pane.scale, colors, chartType);
      if (showVolume) drawVolume(ctx, pane.rect, data, ts, colors);
      const volY = showVolume ? volumeMapper(pane.rect, data, ts) : null;
      drawPaneStudies(ctx, pane.rect, pane.series, pane.guides, data, ts, pane.scale, volY ?? undefined);
      drawPriceLines(ctx, pane.rect, [...pane.priceLines, ...priceLines], pane.scale, colors);
      drawMarkers(ctx, pane.rect, executions, data, ts, pane.scale, colors);
    } else {
      drawPaneStudies(ctx, pane.rect, pane.series, pane.guides, data, ts, pane.scale);
    }
    ctx.restore();
  }

  function onDraw(name: LayerName, ctx: CanvasRenderingContext2D): void {
    switch (name) {
      case 'grid':
        for (const pane of layout.panes) drawPriceAxis(ctx, pane.rect, pane.scale, colors, showGrid);
        drawTimeAxis(ctx, contentRect(), data, ts, colors, tz, showGrid);
        break;
      case 'sessions':
        if (showBands) drawSessions(ctx, contentRect(), bands(), data, ts);
        break;
      case 'series':
        for (const pane of layout.panes) drawPane(ctx, pane);
        if (showLastPrice) drawLastPrice(ctx, layout.pricePane.rect, data, layout.pricePane.scale, colors, layout.axisW);
        break;
      case 'drawings':
        if (dm.visible && data.length > 0) {
          drawDrawings(
            ctx,
            layout.pricePane.rect,
            layout.axisW,
            layout.contentH,
            {
              items: dm.projected(),
              selectedId: dm.selectedId,
              hoverId: dm.hoverId,
              handles: dm.selectedHandles(),
              pending: dm.pendingPx(),
              pendingType: dm.tool === 'cursor' ? null : dm.tool,
              preview: dm.preview,
            },
            colors,
            drawingsFmt,
          );
        }
        break;
      case 'crosshair':
        drawCrosshair(ctx, layout, mouse, data, ts, colors, tz, showLegend, legendTitle);
        break;
    }
  }

  const lm = new LayerManager(container, onDraw);

  // ── drawings ─────────────────────────────────────────────────────────────
  const dm = new DrawingManager({
    toPx: (pt) => ({
      x: pt.time != null && data.length > 0 ? ts.indexToX(data.timeToFractional(pt.time)) : null,
      y: pt.price != null && data.length > 0 ? layout.pricePane.scale.priceToY(pt.price) : null,
    }),
    toLogical: (px) => ({
      time: data.length > 0 ? data.fractionalToTime(ts.xToIndex(px.x)) : 0,
      price: layout.pricePane.scale.yToPrice(px.y),
    }),
    snap: (px, magnet) => {
      const sc = layout.pricePane.scale;
      if (data.length === 0) return { time: 0, price: sc.yToPrice(px.y) };
      const fidx = ts.xToIndex(px.x);
      const idx = Math.round(fidx);
      const onBar = idx >= 0 && idx < data.length;
      // time always snaps to a bar (anchors survive timeframe changes best)
      const time = onBar ? data.time(idx) : data.fractionalToTime(fidx);
      let price = sc.yToPrice(px.y);
      if (magnet && onBar) {
        let best = Infinity;
        let bestP = price;
        for (const v of [data.open(idx), data.high(idx), data.low(idx), data.close(idx)]) {
          const d = Math.abs(sc.priceToY(v) - px.y);
          if (d < best) {
            best = d;
            bestP = v;
          }
        }
        if (best <= 12) price = bestP;
      }
      return { time, price };
    },
    dims: () => ({ width: layout.pricePane.rect.w, height: layout.pricePane.rect.h }),
    measureText: (str) => {
      const c = lm.ctx('drawings');
      c.font = font(12);
      return c.measureText(str).width;
    },
    interval: () => data.interval(),
  });
  dm.subscribe(() => lm.invalidate('drawings'));

  const _dtf = new Map<string, Intl.DateTimeFormat>();
  function fmtTime(t: number): string {
    const daily = data.interval() >= 86_400_000;
    const key = daily ? 'd' : 'i';
    let f = _dtf.get(key);
    if (!f) {
      f = daily
        ? new Intl.DateTimeFormat('en-US', { timeZone: tz, month: 'short', day: 'numeric', year: 'numeric' })
        : new Intl.DateTimeFormat('en-US', { timeZone: tz, month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false });
      _dtf.set(key, f);
    }
    return f.format(new Date(t));
  }
  function humanSpan(ms: number): string {
    const m = Math.round(ms / 60_000);
    if (m < 60) return `${m}m`;
    const h = Math.floor(m / 60);
    if (h < 24) return `${h}h ${m % 60}m`;
    const d = Math.floor(h / 24);
    return `${d}d ${h % 24}h`;
  }
  const drawingsFmt = {
    price: (v: number) => v.toFixed(Math.abs(v) < 1 ? 4 : 2),
    time: fmtTime,
    span: (t0: number, t1: number) => ({
      bars: data.length > 0 ? Math.abs(Math.round(data.timeToFractional(t1) - data.timeToFractional(t0))) : 0,
      text: humanSpan(Math.abs(t1 - t0)),
    }),
    toPrice: (y: number) => layout.pricePane.scale.yToPrice(y),
    toTime: (x: number) => (data.length > 0 ? data.fractionalToTime(ts.xToIndex(x)) : 0),
  };

  function inPricePane(p: { x: number; y: number }): boolean {
    const r = layout.pricePane.rect;
    return p.x >= r.x && p.x < r.x + r.w && p.y >= r.y && p.y <= r.y + r.h;
  }

  // a transient Candle[] view for the indicator controller (it reads OHLCV)
  function candleArray(): Candle[] {
    const n = data.length;
    const out: Candle[] = new Array(n);
    for (let i = 0; i < n; i++) out[i] = data.bar(i)!;
    return out;
  }

  function syncIndicators(): void {
    const changed = indCtrl.sync(indicators, candleArray(), data.version, colors);
    if (changed) {
      const rect = container.getBoundingClientRect();
      layout.recompute(rect.width || 600, rect.height || 400);
      ts.setWidth(layout.plotW);
    }
  }

  function notifyView(): void {
    for (const cb of viewSubs) cb();
  }

  function relayout(): void {
    const rect = container.getBoundingClientRect();
    const w = rect.width || container.clientWidth || 600;
    const h = rect.height || container.clientHeight || 400;
    lm.resize(w, h);
    layout.recompute(w, h);
    ts.setWidth(layout.plotW);
    if (!userInteracted) ts.scrollToLatest(data.length);
    else ts.clamp(data.length);
    recomputeRanges();
    lm.invalidateAll();
    notifyView();
  }

  /** Shared tail of every gesture that moves the time scale. */
  function afterTimeGesture(): void {
    userInteracted = true;
    recomputeRanges();
    lm.invalidateAll();
    notifyView();
  }

  // ── input ─────────────────────────────────────────────────────────────────
  let dragging = false;
  let dragStartX = 0;
  let dragStartFirst = 0;
  let downPos: { x: number; y: number } | null = null;
  let moved = 0;
  let dividerDrag: { pane: Pane; startY: number; startH: number } | null = null;
  // Price-axis drag (pans the pinned Y range) — a third gesture, distinct
  // from the chart-body time-drag (`dragging`) and the pane-divider resize
  // drag (`dividerDrag`); see onDown for the hit-test that keeps them apart.
  let priceDrag: { startY: number; startMin: number; startMax: number } | null = null;
  // Time-axis drag zooms (TradingView: drag the time scale right to stretch,
  // left to compress), pivoting on the bar under the press.
  let timeAxisDrag: { startX: number; startSpacing: number } | null = null;

  const pos = (e: { clientX: number; clientY: number }): { x: number; y: number } => {
    const r = container.getBoundingClientRect();
    return { x: e.clientX - r.left, y: e.clientY - r.top };
  };

  // ── touch: pinch zoom + kinetic pan ───────────────────────────────────────
  const pointers = new Map<number, { x: number; y: number }>();
  let pinch: { startDist: number; startSpacing: number } | null = null;
  /** recent pan positions for the fling velocity at release */
  let flingSamples: { t: number; x: number }[] = [];
  let kineticRaf = 0;
  function stopKinetic(): void {
    if (kineticRaf) cancelAnimationFrame(kineticRaf);
    kineticRaf = 0;
  }
  /** Decelerating pan after a fast drag release (touch and mouse alike). */
  function startKinetic(velocityPxPerMs: number): void {
    stopKinetic();
    let v = velocityPxPerMs;
    let last = performance.now();
    const step = (frameTime?: number): void => {
      const now = typeof frameTime === 'number' ? frameTime : performance.now();
      const dt = Math.max(0, Math.min(64, now - last));
      last = now;
      ts.firstVisibleIndex -= (v * dt) / ts.barSpacing;
      ts.clamp(data.length);
      v *= Math.pow(0.94, dt / 16);
      afterTimeGesture();
      kineticRaf = Math.abs(v) > 0.02 ? requestAnimationFrame(step) : 0;
    };
    kineticRaf = requestAnimationFrame(step);
  }
  function pinchDistance(): number {
    const [a, b] = [...pointers.values()];
    return Math.hypot(a.x - b.x, a.y - b.y);
  }
  function pinchMidX(): number {
    const [a, b] = [...pointers.values()];
    return (a.x + b.x) / 2;
  }

  function dividerAt(y: number): Pane | null {
    for (const d of layout.dividers()) {
      if (Math.abs(y - d.y) <= DIVIDER_TOL) return layout.getPane(d.paneId) ?? null;
    }
    return null;
  }

  /** True when (x,y) is over the price pane's slice of the right-edge price-axis
   *  gutter (x in [plotW, plotW+PRICE_AXIS_W]) — the hit-test shared by
   *  price-axis drag-to-pan and wheel-to-zoom. Scoped to the price pane only:
   *  the gutter beside an oscillator pane keeps today's behavior untouched. */
  function overPriceAxis(x: number, y: number): boolean {
    return x >= layout.plotW && layout.paneAtY(y) === layout.pricePane;
  }

  /** True over the time-axis strip below the bottom pane. */
  function overTimeAxis(x: number, y: number): boolean {
    return y > layout.contentH && x < layout.plotW;
  }

  function hoverCursor(p: { x: number; y: number }): string {
    if (dividerAt(p.y)) return 'row-resize';
    if (overPriceAxis(p.x, p.y)) return 'ns-resize';
    if (overTimeAxis(p.x, p.y)) return 'ew-resize';
    return 'crosshair';
  }

  function notifyPriceScale(): void {
    for (const cb of priceScaleSubs) cb();
  }

  /** Marks the price scale manual (idempotent past the first call) and pins
   *  `range` — the shared tail of every gesture/API path that takes manual
   *  control, so the priceUserSet→true transition is notified exactly once. */
  function pinPriceRange(range: { min: number; max: number }): void {
    layout.pricePane.range = range;
    if (!priceUserSet) {
      priceUserSet = true;
      notifyPriceScale();
    }
  }

  const onMove = (e: PointerEvent): void => {
    const p = pos(e);
    if (pointers.has(e.pointerId)) pointers.set(e.pointerId, p);
    if (pinch && pointers.size >= 2) {
      const dist = pinchDistance();
      if (dist > 0) {
        ts.zoomAt(pinchMidX(), (pinch.startSpacing * dist) / pinch.startDist, data.length);
        afterTimeGesture();
      }
      return;
    }
    // no crosshair under a finger — it hides the bar being pointed at
    mouse = e.pointerType === 'touch' ? null : p;
    updateHover(e.pointerType === 'touch' ? null : p);
    if (dividerDrag) {
      const dy = p.y - dividerDrag.startY;
      dividerDrag.pane.heightPx = dividerDrag.startH - dy; // drag up → bigger pane
      const rect = container.getBoundingClientRect();
      layout.recompute(rect.width || 600, rect.height || 400);
      ts.setWidth(layout.plotW);
      recomputeRanges();
      lm.invalidateAll();
      return;
    }
    if (priceDrag) {
      // Pan the pinned range: translate min/max by the pixel delta through
      // the scale captured at drag-start (mirrors the time-drag's
      // deltaBars-through-barSpacing math, one dimension over).
      const dy = p.y - priceDrag.startY;
      const scale = layout.pricePane.scale;
      const innerH = scale.height * (1 - scale.scaleMargins.top - scale.scaleMargins.bottom) || 1;
      const span = priceDrag.startMax - priceDrag.startMin;
      const shift = (dy / innerH) * span;
      pinPriceRange({ min: priceDrag.startMin + shift, max: priceDrag.startMax + shift });
      recomputeRanges();
      lm.invalidateAll();
      notifyView();
      return;
    }
    if (dm.dragging) {
      dm.onMove(p);
      container.style.cursor = dm.cursorFor() ?? 'move';
      lm.invalidate('crosshair');
      return;
    }
    if (timeAxisDrag) {
      const dx = p.x - timeAxisDrag.startX;
      ts.zoomAt(timeAxisDrag.startX, timeAxisDrag.startSpacing * Math.pow(2, dx / 200), data.length);
      afterTimeGesture();
      return;
    }
    if (dragging) {
      const deltaBars = (p.x - dragStartX) / ts.barSpacing;
      ts.firstVisibleIndex = dragStartFirst - deltaBars;
      ts.clamp(data.length);
      if (downPos) moved = Math.max(moved, Math.hypot(p.x - downPos.x, p.y - downPos.y));
      const now = performance.now();
      flingSamples.push({ t: now, x: p.x });
      if (flingSamples.length > 6) flingSamples.shift();
      afterTimeGesture();
    } else {
      if (inPricePane(p) || dm.placing) {
        if (dm.onMove(p)) lm.invalidate('drawings');
      } else if (dm.hoverId) {
        dm.hoverId = null;
        dm.hoverHandle = null;
        lm.invalidate('drawings');
      }
      const dc = dm.cursorFor();
      container.style.cursor = dc && dc !== 'crosshair' ? dc : hoverCursor(p);
      lm.invalidate('crosshair');
    }
  };
  const onLeave = (): void => {
    mouse = null;
    updateHover(null);
    lm.invalidate('crosshair');
  };
  const onDown = (e: PointerEvent): void => {
    const p = pos(e);
    stopKinetic();
    pointers.set(e.pointerId, p);
    try {
      container.setPointerCapture(e.pointerId);
    } catch {
      /* not supported */
    }
    // bigger targets for fingers
    dm.hitTolerance = e.pointerType === 'touch' ? 14 : 6;
    if (pointers.size === 2) {
      // second finger: abandon whatever the first started and pinch-zoom
      dragging = false;
      downPos = null;
      priceDrag = null;
      timeAxisDrag = null;
      dividerDrag = null;
      dm.abortPress();
      pinch = { startDist: pinchDistance() || 1, startSpacing: ts.barSpacing };
      return;
    }
    if (pointers.size > 2) return;
    const div = dividerAt(p.y);
    if (div) {
      dividerDrag = { pane: div, startY: p.y, startH: div.heightPx };
      container.style.cursor = 'row-resize';
      return;
    }
    if (overPriceAxis(p.x, p.y)) {
      const r = layout.pricePane.scale.range();
      priceDrag = { startY: p.y, startMin: r.min, startMax: r.max };
      container.style.cursor = 'ns-resize';
      return;
    }
    if (overTimeAxis(p.x, p.y)) {
      timeAxisDrag = { startX: p.x, startSpacing: ts.barSpacing };
      container.style.cursor = 'ew-resize';
      return;
    }
    if (inPricePane(p) && dm.onDown(p) !== 'none') {
      lm.invalidate('drawings');
      container.style.cursor = dm.cursorFor() ?? 'crosshair';
      return;
    }
    dragging = true;
    downPos = p;
    moved = 0;
    dragStartX = p.x;
    dragStartFirst = ts.firstVisibleIndex;
    flingSamples = [{ t: performance.now(), x: p.x }];
    container.style.cursor = 'grabbing';
  };
  const onUp = (e: PointerEvent): void => {
    pointers.delete(e.pointerId);
    if (pinch) {
      if (pointers.size < 2) pinch = null;
      return;
    }
    if (dm.dragging || dm.placing) {
      dm.onUp(pos(e));
      lm.invalidate('drawings');
      if (!dividerDrag && !priceDrag && !timeAxisDrag && !dragging) {
        container.style.cursor = dm.cursorFor() ?? 'crosshair';
        return;
      }
    }
    if (dividerDrag) {
      dividerDrag = null;
      container.style.cursor = 'crosshair';
      return;
    }
    if (priceDrag) {
      priceDrag = null;
      container.style.cursor = 'crosshair';
      return;
    }
    if (timeAxisDrag) {
      timeAxisDrag = null;
      container.style.cursor = 'ew-resize';
      return;
    }
    if (dragging && downPos && moved <= CLICK_TOL) emitClick(pos(e));
    else if (dragging && flingSamples.length >= 2) {
      // fling: velocity over the last few samples, only if the release was fast
      const a = flingSamples[0];
      const b = flingSamples[flingSamples.length - 1];
      const dt = b.t - a.t;
      const v = dt > 0 ? (b.x - a.x) / dt : 0;
      if (Math.abs(v) > 0.3 && performance.now() - b.t < 80) startKinetic(v);
    }
    dragging = false;
    downPos = null;
    container.style.cursor = 'crosshair';
  };
  const onWheel = (e: WheelEvent): void => {
    e.preventDefault();
    stopKinetic();
    const p = pos(e);
    if (overPriceAxis(p.x, p.y)) {
      // Pivot-preserving price zoom — the exact pattern onWheel already uses
      // for the time axis (compute the value under the cursor BEFORE
      // changing the range, rescale, reposition so it stays under the
      // cursor), one dimension over.
      const scale = layout.pricePane.scale;
      const pivot = scale.yToPrice(p.y);
      const r = scale.range();
      const span = r.max - r.min || 1;
      const factor = e.deltaY < 0 ? 1.1 : 1 / 1.1;
      const minSpan = Math.max(Math.abs(pivot) * 1e-4, 1e-6); // never collapse to zero height
      const newSpan = Math.max(minSpan, span / factor);
      const frac = (r.max - pivot) / span; // pivot's fractional distance below the top of the range
      pinPriceRange({ min: pivot - (1 - frac) * newSpan, max: pivot + frac * newSpan });
      recomputeRanges();
      lm.invalidateAll();
      notifyView();
      return;
    }
    // Normalise line/page deltas to pixels (Firefox reports lines).
    const unit = e.deltaMode === 1 ? 33 : e.deltaMode === 2 ? 100 : 1;
    const dx = e.deltaX * unit;
    const dy = e.deltaY * unit;
    const pinch = e.ctrlKey || e.metaKey; // trackpad pinch arrives as ctrl+wheel
    if (!pinch && (e.shiftKey || Math.abs(dx) > Math.abs(dy))) {
      // Horizontal trackpad scroll (or shift+wheel) pans, like TradingView.
      const delta = Math.abs(dx) >= Math.abs(dy) ? dx : dy;
      if (delta === 0) return;
      ts.firstVisibleIndex += delta / ts.barSpacing;
      ts.clamp(data.length);
      afterTimeGesture();
      return;
    }
    if (dy === 0) return;
    // One mouse-wheel notch (100 px) = 1.25×; pinch deltas are small and
    // continuous, so they get a faster curve. Capped so a burst can't jump.
    const factor = Math.min(2, Math.max(0.5, Math.pow(1.25, -dy / (pinch ? 40 : 100))));
    ts.zoomAt(p.x, ts.barSpacing * factor, data.length);
    afterTimeGesture();
  };

  // Double-click an axis to reset it: price axis → auto-scale, time axis →
  // default zoom at the latest bar. The chart body keeps single-click semantics.
  const onDblClick = (e: MouseEvent): void => {
    const p = pos(e);
    if (inPricePane(p) && dm.onDblClick(p)) return;
    if (overPriceAxis(p.x, p.y)) {
      priceUserSet = false;
      delete layout.pricePane.range;
      recomputeRanges();
      lm.invalidateAll();
      notifyView();
      notifyPriceScale();
    } else if (overTimeAxis(p.x, p.y)) {
      ts.resetView(data.length);
      userInteracted = false;
      recomputeRanges();
      lm.invalidateAll();
      notifyView();
    }
  };

  function emitClick(p: { x: number; y: number }): void {
    if (data.length === 0 || clickSubs.size === 0) return;
    const idx = Math.max(0, Math.min(data.length - 1, Math.round(ts.xToIndex(p.x))));
    const bar = data.bar(idx);
    const ev: ClickEvent = { ts: data.time(idx), price: layout.pricePane.scale.yToPrice(p.y), bar };
    for (const cb of clickSubs) cb(ev);
  }

  container.style.cursor = 'crosshair';
  container.style.touchAction = 'none'; // we own pan + pinch on touch screens
  container.addEventListener('pointermove', onMove);
  container.addEventListener('pointerleave', onLeave);
  container.addEventListener('pointerdown', onDown);
  window.addEventListener('pointerup', onUp);
  window.addEventListener('pointercancel', onUp);
  container.addEventListener('wheel', onWheel, { passive: false });
  container.addEventListener('dblclick', onDblClick);

  const ro = new ResizeObserver(() => relayout());
  ro.observe(container);
  relayout();

  // ── public API ──────────────────────────────────────────────────────────
  return {
    setData(candles: Candle[]): void {
      // A user who has panned/zoomed keeps their place: the time at the
      // right edge stays at the right edge at the same px-per-bar, so a
      // timeframe switch or reload never throws the view away. Otherwise
      // open at the default view (default zoom, latest bar on the right).
      const keepView = data.length > 0 && userInteracted;
      const rightEdgeTime = keepView ? data.fractionalToTime(ts.visibleIndexRange().to) : 0;
      data.setData(candles);
      bandCache = null;
      syncIndicators();
      ts.setWidth(layout.plotW);
      if (keepView && data.length > 0) {
        ts.firstVisibleIndex = data.timeToFractional(rightEdgeTime) - layout.plotW / ts.barSpacing;
        ts.clamp(data.length);
      } else {
        userInteracted = false;
        ts.resetView(data.length);
      }
      recomputeRanges();
      lm.invalidateAll();
      notifyView();
    },
    update(candle: Candle): void {
      const appended = data.update(candle);
      syncIndicators();
      // Deliberately does NOT clear userInteracted: a trader who zoomed into a
      // level keeps that view while the tape ticks. Auto-follow the right edge
      // only while they haven't taken manual control.
      if (!userInteracted) ts.scrollToLatest(data.length);
      else if (appended) ts.clamp(data.length);
      recomputeRanges();
      lm.invalidateAll();
      notifyView();
    },
    setMarkers(execs: Execution[]): void {
      executions = execs.slice();
      recomputeRanges();
      lm.invalidate('series');
    },
    setPriceLines(lines: PriceLine[]): void {
      priceLines = lines.slice();
      recomputeRanges(); // a level can extend the autorange (e.g. a gap to prior close)
      lm.invalidateAll(); // ranges may shift → redraw axis + series together
      notifyView();
    },
    setIndicators(instances: IndicatorInstance[]): void {
      indicators = instances.slice();
      syncIndicators();
      recomputeRanges();
      lm.invalidateAll();
      notifyView();
    },
    panes() {
      return layout.panes.map((p) => ({ id: p.id, kind: p.kind, rect: { ...p.rect } }));
    },
    indicatorValuesAt(idx: number | null) {
      const at = idx ?? data.length - 1;
      return indicators.map((inst) => ({ instanceId: inst.instanceId, values: indCtrl.valuesAt(inst, at) }));
    },
    hoverIndex() {
      return hoverIdx;
    },
    subscribeHover(cb) {
      hoverSubs.add(cb);
    },
    unsubscribeHover(cb) {
      hoverSubs.delete(cb);
    },
    setChartType(type: ChartType): void {
      chartType = type;
      lm.invalidate('series');
    },
    setVolumeVisible(v: boolean): void {
      showVolume = v;
      lm.invalidate('series');
    },
    setGridVisible(v: boolean): void {
      showGrid = v;
      lm.invalidate('grid');
    },
    sessions: {
      apply(def: SessionDef): void {
        sessionDef = def;
        bandCache = null;
        lm.invalidate('sessions');
      },
      toggle(visible: boolean): void {
        showBands = visible;
        lm.invalidate('sessions');
      },
    },
    timeScale() {
      return {
        fitContent(): void {
          userInteracted = false;
          ts.fitContent(data.length);
          recomputeRanges();
          lm.invalidateAll();
          notifyView();
        },
        getVisibleRange(): { from: number; to: number } | null {
          if (data.length === 0) return null;
          const r = ts.visibleIndexRange();
          return { from: data.fractionalToTime(r.from), to: data.fractionalToTime(r.to) };
        },
        setVisibleRange(r: { from: number; to: number }): void {
          if (data.length === 0) return;
          const fromIdx = data.timeToFractional(r.from);
          const toIdx = data.timeToFractional(r.to);
          const barsVis = Math.max(1, toIdx - fromIdx);
          ts.barSpacing = Math.max(MIN_BAR_SPACING, Math.min(MAX_BAR_SPACING, layout.plotW / barsVis));
          ts.firstVisibleIndex = fromIdx;
          ts.clamp(data.length);
          userInteracted = true;
          recomputeRanges();
          lm.invalidateAll();
          notifyView();
        },
        scrollToTime(tsMs: number): void {
          if (data.length === 0) return;
          const idx = data.timeToFractional(tsMs);
          const visBars = layout.plotW / ts.barSpacing;
          ts.firstVisibleIndex = idx - visBars / 2;
          ts.clamp(data.length);
          userInteracted = true;
          recomputeRanges();
          lm.invalidateAll();
          notifyView();
        },
        scrollToLatest(): void {
          ts.scrollToLatest(data.length);
          // back at the live edge: let new data auto-follow again
          userInteracted = false;
          recomputeRanges();
          lm.invalidateAll();
          notifyView();
        },
        resetView(): void {
          ts.resetView(data.length);
          userInteracted = false;
          recomputeRanges();
          lm.invalidateAll();
          notifyView();
        },
        isAtLatest(): boolean {
          return ts.isAtLatest(data.length);
        },
        scrollBy(bars: number): void {
          if (data.length === 0) return;
          stopKinetic();
          ts.firstVisibleIndex += bars;
          ts.clamp(data.length);
          afterTimeGesture();
        },
        zoomBy(factor: number): void {
          if (data.length === 0) return;
          stopKinetic();
          ts.zoomAt(layout.plotW / 2, ts.barSpacing * factor, data.length);
          afterTimeGesture();
        },
      };
    },
    priceScale() {
      return {
        isAutoScale(): boolean {
          return !priceUserSet;
        },
        setAutoScale(auto: boolean): void {
          if (auto) {
            priceUserSet = false;
            delete layout.pricePane.range;
            recomputeRanges();
            lm.invalidateAll();
            notifyView();
            notifyPriceScale();
          } else {
            // Pin the CURRENTLY-displayed auto-computed range so the
            // auto→manual transition is visually seamless (no jump).
            const r = layout.pricePane.scale.range();
            layout.pricePane.range = { min: r.min, max: r.max };
            priceUserSet = true;
            recomputeRanges();
            lm.invalidateAll();
            notifyView();
            notifyPriceScale();
          }
        },
        setRange(min: number, max: number): void {
          layout.pricePane.range = { min, max };
          priceUserSet = true;
          recomputeRanges();
          lm.invalidateAll();
          notifyView();
          notifyPriceScale();
        },
        getRange(): { min: number; max: number } | null {
          return layout.pricePane.range ? { ...layout.pricePane.range } : null;
        },
      };
    },
    drawings: {
      set: (list) => dm.set(list),
      get: () => dm.get(),
      add: (d) => dm.add(d),
      update: (id, patch) => dm.update(id, patch),
      remove: (id) => dm.remove(id),
      clear: () => dm.clear(),
      clone: (id) => dm.clone(id),
      select: (id) => dm.select(id),
      getSelected: () => dm.getSelected(),
      setTool: (tool) => {
        dm.setTool(tool);
        container.style.cursor = dm.cursorFor() ?? 'crosshair';
      },
      getTool: () => dm.tool,
      setStayInMode: (v) => {
        dm.stayInMode = v;
      },
      setMagnet: (v) => {
        dm.magnet = v;
      },
      setVisible: (v) => {
        dm.visible = v;
        lm.invalidate('drawings');
      },
      cancel: () => {
        const changed = dm.cancel();
        container.style.cursor = dm.cursorFor() ?? 'crosshair';
        return changed;
      },
      undo: () => dm.undo(),
      redo: () => dm.redo(),
      canUndo: () => dm.canUndo(),
      canRedo: () => dm.canRedo(),
      selectionBounds: () => dm.selectionBounds(),
      subscribe: (cb) => dm.subscribe(cb),
      unsubscribe: (cb) => dm.unsubscribe(cb),
    },
    timeToCoordinate(tsMs: number): number | null {
      if (data.length === 0) return null;
      return ts.indexToX(data.timeToFractional(tsMs));
    },
    coordinateToTime(x: number): number | null {
      if (data.length === 0) return null;
      return data.fractionalToTime(ts.xToIndex(x));
    },
    priceToCoordinate(price: number): number | null {
      if (data.length === 0) return null;
      return layout.pricePane.scale.priceToY(price);
    },
    coordinateToPrice(y: number): number | null {
      if (data.length === 0) return null;
      return layout.pricePane.scale.yToPrice(y);
    },
    pricePaneRect(): Rect {
      const r = layout.pricePane.rect;
      return { x: r.x, y: r.y, w: r.w, h: r.h };
    },
    subscribeVisibleRangeChange(cb: () => void): void {
      viewSubs.add(cb);
    },
    unsubscribeVisibleRangeChange(cb: () => void): void {
      viewSubs.delete(cb);
    },
    subscribePriceScaleChange(cb: () => void): void {
      priceScaleSubs.add(cb);
    },
    unsubscribePriceScaleChange(cb: () => void): void {
      priceScaleSubs.delete(cb);
    },
    subscribeClick(cb: (e: ClickEvent) => void): void {
      clickSubs.add(cb);
    },
    unsubscribeClick(cb: (e: ClickEvent) => void): void {
      clickSubs.delete(cb);
    },
    applyOptions(o: Partial<ChartOptions>): void {
      if (o.colors) {
        Object.assign(colors, o.colors);
        container.style.background = colors.background;
      }
      if (o.fonts) {
        setChartFonts(o.fonts);
        recomputeRanges(); // the axis gutter is measured in the font
      }
      if (o.showSessionBands != null) showBands = o.showSessionBands;
      if (o.showVolume != null) showVolume = o.showVolume;
      if (o.showGrid != null) showGrid = o.showGrid;
      if (o.showLegend != null) showLegend = o.showLegend;
      if (o.showLastPrice != null) showLastPrice = o.showLastPrice;
      if (o.legendTitle != null) legendTitle = o.legendTitle;
      if (o.chartType) chartType = o.chartType;
      if (o.rightOffset != null) ts.rightOffset = o.rightOffset;
      lm.invalidateAll();
    },
    remove(): void {
      ro.disconnect();
      stopKinetic();
      container.removeEventListener('pointermove', onMove);
      container.removeEventListener('pointerleave', onLeave);
      container.removeEventListener('pointerdown', onDown);
      window.removeEventListener('pointerup', onUp);
      window.removeEventListener('pointercancel', onUp);
      container.removeEventListener('wheel', onWheel);
      container.removeEventListener('dblclick', onDblClick);
      indCtrl.destroy();
      viewSubs.clear();
      hoverSubs.clear();
      priceScaleSubs.clear();
      clickSubs.clear();
      lm.destroy();
    },
  };
}
