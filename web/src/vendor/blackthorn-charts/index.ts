//! Blackthorn Charts — public entry point.
//
// Built to a standalone IIFE (global `BlackthornCharts`) for embedding, and to
// ESM for the Svelte binding. Implements candlestick/bars/line/area/Heikin-Ashi
// + volume, ET axes, session shading, execution markers, native multi-pane
// indicators (SMA/EMA/Bollinger/VWAP/RSI/MACD), pan/zoom/crosshair, and the
// coordinate / visible-range / click APIs the app's drawing overlay needs.

export { createChart } from './chart';
export type { ChartApi, ClickEvent } from './chart';

export { DEFAULT_DRAWING_STYLE, FIB_LEVELS, POINTS_NEEDED, makeDrawing } from './drawings/model';
export type { Drawing, DrawingPoint, DrawingStyle, DrawingTool, DrawingType } from './drawings/model';
export type { DrawingsEvent } from './drawings/manager';

export { US_EQUITIES, NYSE_HOLIDAYS, NYSE_HALF_DAYS, sessionRanges, wallToUtc } from './sessions/sessionmodel';
export type { SessionDef, SessionSegment, BandRange } from './sessions/sessionmodel';

export { DEFAULT_COLORS, DEFAULT_FONTS } from './core/types';
export type {
  Candle,
  ChartType,
  Execution,
  ChartOptions,
  ChartColors,
  ChartFonts,
  PriceRange,
  LineStyle,
  PaneSeries,
  GuideLevel,
  PriceLine,
} from './core/types';

export {
  INDICATOR_REGISTRY,
  COMPUTE,
  getIndicator,
  resolveParams,
  makeInstance,
  instanceLabel,
} from './indicators/registry';
export type { IndicatorValue } from './indicators/controller';
export type {
  IndicatorDef,
  IndicatorGroup,
  IndicatorInstance,
  IndicatorParams,
  ParamSchemaEntry,
  RenderSpec,
  PriceSource,
  ComputeResult,
} from './indicators/registry';
