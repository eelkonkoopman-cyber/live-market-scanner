//! Core data types for the Blackthorn Charts engine.
//
// `Candle` mirrors Blackthorn's exact feed shape (see ARCHITECTURE.md §7) so the
// data adapters stay byte-compatible with the rest of the app.

export type Candle = {
  /** ms epoch (bar start) */
  ts: number;
  open: number;
  high: number;
  low: number;
  close: number;
  volume: number;
  /** present in the feed; powers a future VWAP study. Optional here. */
  vwap?: number | null;
};

export type ChartType = 'candles' | 'bars' | 'line' | 'area' | 'heikin-ashi';

export type LineStyle = 'solid' | 'dashed' | 'dotted';

/** A computed indicator series to paint in a pane, index-aligned to candles
 *  (one slot per bar; null = whitespace / warm-up). */
export type PaneSeries =
  | { kind: 'line'; values: (number | null)[]; color: string; lineWidth: number; lineStyle: LineStyle; scale?: 'price' | 'volume' }
  | {
      kind: 'histogram';
      values: (number | null)[];
      /** optional per-bar color (e.g. MACD sign coloring); falls back to `color`. */
      colors?: (string | null)[];
      color: string;
    };

/** Horizontal guide line at a fixed value in a pane (RSI 70/30, MACD 0). */
export type GuideLevel = { value: number; color: string };

/** A horizontal reference line on the PRICE pane at a fixed price (e.g. prior
 *  close, session high/low, an entry level). Rendered across the full plot width
 *  with an optional left-anchored label chip. */
export type PriceLine = {
  price: number;
  color?: string;
  label?: string;
  lineStyle?: LineStyle;
  lineWidth?: number;
};

/** A trade execution to plot as a marker (entry/exit arrow). */
export type Execution = {
  /** ms epoch — snapped to the containing bar */
  ts: number;
  price: number;
  side: 'buy' | 'sell';
  label?: string;
};

export type PriceRange = { min: number; max: number };

export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}

/** Theme colors. Defaults are a dark theme that matches the desktop app. */
export interface ChartColors {
  background: string;
  grid: string;
  axisText: string;
  axisLine: string;
  upBody: string;
  upWick: string;
  downBody: string;
  downWick: string;
  volumeUp: string;
  volumeDown: string;
  crosshair: string;
  crosshairLabelBg: string;
  buyMarker: string;
  sellMarker: string;
  legendText: string;
  /** UI accent — placement previews, focus rings. */
  accent: string;
}

export const DEFAULT_COLORS: ChartColors = {
  background: '#0e1116',
  grid: 'rgba(255,255,255,0.05)',
  axisText: 'rgba(220,224,232,0.65)',
  axisLine: 'rgba(255,255,255,0.12)',
  upBody: '#26a69a',
  upWick: '#26a69a',
  downBody: '#ef5350',
  downWick: '#ef5350',
  volumeUp: 'rgba(38,166,154,0.45)',
  volumeDown: 'rgba(239,83,80,0.45)',
  crosshair: 'rgba(220,224,232,0.45)',
  crosshairLabelBg: '#1b2230',
  buyMarker: '#26c281',
  sellMarker: '#ff5b5b',
  legendText: 'rgba(230,234,242,0.9)',
  accent: '#e5c45c',
};

/** Font families for canvas text. `num` carries every number (axis ticks,
 *  crosshair chips, legend values); `ui` carries words (marker and drawing
 *  labels, the legend title). Family lists only — sizes and weights are the
 *  engine's. */
export interface ChartFonts {
  num: string;
  ui: string;
}

export const DEFAULT_FONTS: ChartFonts = {
  num: 'ui-monospace, Menlo, Consolas, monospace',
  ui: 'ui-sans-serif, system-ui, -apple-system, sans-serif',
};

export interface ChartOptions {
  colors?: Partial<ChartColors>;
  fonts?: Partial<ChartFonts>;
  /** bars of right-edge padding (default 4) */
  rightOffset?: number;
  /** show pre/post-market session shading (default true) */
  showSessionBands?: boolean;
  /** IANA tz for axis/crosshair time labels (default America/New_York) */
  timezone?: string;
  /** main-series style (default 'candles') */
  chartType?: ChartType;
  /** draw the volume histogram on the price pane (default true) */
  showVolume?: boolean;
  /** draw background grid lines (default true) */
  showGrid?: boolean;
  /** draw the top-left OHLC legend (default true) */
  showLegend?: boolean;
  /** text drawn ahead of the OHLC legend, e.g. "SPY · 5m" */
  legendTitle?: string;
  /** dashed line + axis chip at the last close (default true) */
  showLastPrice?: boolean;
}

/** Width (px) reserved on the right for the price axis. */
export const PRICE_AXIS_W = 62;
/** Height (px) reserved at the bottom for the time axis. */
export const TIME_AXIS_H = 22;
