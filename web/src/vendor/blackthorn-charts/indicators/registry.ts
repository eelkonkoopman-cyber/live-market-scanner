//! Indicator math + registry (BUILD-PLAN §3.5) — ported verbatim from the app's
//! `chart/chartIndicators.ts`, with two deliberate changes for this engine:
//!   1. compute outputs are index-aligned `(number|null)[]` arrays (one slot per
//!      candle, null = warm-up) instead of lightweight-charts whitespace series;
//!      the renderer pairs `values[i]` with candle `i`, so no time/seconds dance.
//!   2. the engine is ms-native — no `toSec()` anywhere.
// The IndicatorDef / IndicatorInstance / registry SHAPES are preserved so the
// app's persisted `IndicatorInstance` JSON keeps working unchanged.

import type { Candle, GuideLevel, LineStyle } from '../core/types';

export type PriceSource = 'close' | 'open' | 'high' | 'low' | 'hl2' | 'hlc3' | 'ohlc4';
export type IndicatorParams = Record<string, number | string>;

export type ComputeResult = {
  lines: Record<string, (number | null)[]>;
  histograms?: Record<string, (number | null)[]>;
  /** horizontal price levels (session levels): key → price */
  levels?: Record<string, number | null>;
};

export type ParamSchemaEntry =
  | { key: string; label: string; type: 'int'; min: number; max: number }
  | { key: string; label: string; type: 'float'; min: number; max: number; step: number }
  | { key: string; label: string; type: 'enum'; options: readonly string[] }
  /** a ms-epoch the user picks on the chart (anchored VWAP); 0 = auto */
  | { key: string; label: string; type: 'timestamp' };

export type RenderSpec = {
  /** key into ComputeResult.lines / .histograms / .levels */
  key: string;
  seriesType: 'line' | 'histogram' | 'level';
  /** legend name for this series ("Upper", "Signal"); defaults to the key */
  label?: string;
  /** 'volume' lines are drawn in the volume band's scale (volume MA) */
  scale?: 'price' | 'volume';
  style: { color?: string; lineWidth?: number; lineStyle?: LineStyle };
};

export type IndicatorGroup = 'Trend' | 'Momentum' | 'Volatility' | 'Volume' | 'Levels';

export type IndicatorDef = {
  id: string;
  name: string;
  /** dialog grouping + one-line description */
  group: IndicatorGroup;
  description: string;
  category: 'overlay' | 'oscillator';
  compute: keyof typeof COMPUTE;
  defaultParams: IndicatorParams;
  paramSchema: ParamSchemaEntry[];
  render: RenderSpec[];
  guideLevels?: GuideLevel[];
  range?: { min: number; max: number };
};

/** A live, user-added indicator on a chart (persisted JSON shape). */
export type IndicatorInstance = {
  instanceId: string;
  indicatorId: string;
  params: IndicatorParams;
  styleOverrides: Record<string, Partial<RenderSpec['style']>>;
  visible: boolean;
};

// ─── ET calendar-day helper (for VWAP day reset) ────────────────────────────

const _etDay = new Intl.DateTimeFormat('en-CA', {
  timeZone: 'America/New_York',
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
});
/** YYYY-MM-DD in America/New_York for a ms timestamp. */
function etDate(ts: number): string {
  return _etDay.format(new Date(ts));
}

// ─── Compute helpers ────────────────────────────────────────────────────────

function sourceVal(c: Candle, source: PriceSource | string): number {
  switch (source) {
    case 'open':
      return c.open;
    case 'high':
      return c.high;
    case 'low':
      return c.low;
    case 'hl2':
      return (c.high + c.low) / 2;
    case 'hlc3':
      return (c.high + c.low + c.close) / 3;
    case 'ohlc4':
      return (c.open + c.high + c.low + c.close) / 4;
    default:
      return c.close;
  }
}

/** EMA over a number array; aligned to input, null during warm-up. */
function emaArr(arr: number[], period: number): (number | null)[] {
  const out: (number | null)[] = new Array(arr.length).fill(null);
  const k = 2 / (period + 1);
  let prev: number | null = null;
  let sum = 0;
  for (let i = 0; i < arr.length; i++) {
    if (i < period - 1) {
      sum += arr[i]!;
      continue;
    }
    if (i === period - 1) {
      sum += arr[i]!;
      prev = sum / period;
    } else {
      prev = arr[i]! * k + (prev as number) * (1 - k);
    }
    out[i] = prev;
  }
  return out;
}

// ─── Compute functions (math identical to the app) ──────────────────────────

export function sma(candles: Candle[], params: IndicatorParams = {}): ComputeResult {
  const period = Number(params.period ?? 20);
  const source = String(params.source ?? 'close');
  const vals = candles.map((c) => sourceVal(c, source));
  const raw: (number | null)[] = new Array(candles.length).fill(null);
  let sum = 0;
  for (let i = 0; i < vals.length; i++) {
    sum += vals[i]!;
    if (i >= period) sum -= vals[i - period]!;
    if (i >= period - 1) raw[i] = sum / period;
  }
  return { lines: { value: raw } };
}

export function ema(candles: Candle[], params: IndicatorParams = {}): ComputeResult {
  const period = Number(params.period ?? 20);
  const source = String(params.source ?? 'close');
  const vals = candles.map((c) => sourceVal(c, source));
  return { lines: { value: emaArr(vals, period) } };
}

export function bollinger(candles: Candle[], params: IndicatorParams = {}): ComputeResult {
  const period = Number(params.period ?? 20);
  const mult = Number(params.mult ?? 2);
  const source = String(params.source ?? 'close');
  const vals = candles.map((c) => sourceVal(c, source));
  const upper: (number | null)[] = new Array(candles.length).fill(null);
  const middle: (number | null)[] = new Array(candles.length).fill(null);
  const lower: (number | null)[] = new Array(candles.length).fill(null);
  for (let i = period - 1; i < vals.length; i++) {
    let sum = 0;
    for (let j = i - period + 1; j <= i; j++) sum += vals[j]!;
    const mean = sum / period;
    let sq = 0;
    for (let j = i - period + 1; j <= i; j++) sq += (vals[j]! - mean) ** 2;
    const sd = Math.sqrt(sq / period);
    middle[i] = mean;
    upper[i] = mean + mult * sd;
    lower[i] = mean - mult * sd;
  }
  return { lines: { upper, middle, lower } };
}

export function rsi(candles: Candle[], params: IndicatorParams = {}): ComputeResult {
  const period = Number(params.period ?? 14);
  const raw: (number | null)[] = new Array(candles.length).fill(null);
  let avgGain = 0;
  let avgLoss = 0;
  for (let i = 1; i < candles.length; i++) {
    const change = candles[i]!.close - candles[i - 1]!.close;
    const gain = Math.max(change, 0);
    const loss = Math.max(-change, 0);
    if (i <= period) {
      avgGain += gain;
      avgLoss += loss;
      if (i === period) {
        avgGain /= period;
        avgLoss /= period;
      } else {
        continue;
      }
    } else {
      avgGain = (avgGain * (period - 1) + gain) / period;
      avgLoss = (avgLoss * (period - 1) + loss) / period;
    }
    raw[i] = avgLoss === 0 ? 100 : 100 - 100 / (1 + avgGain / avgLoss);
  }
  return { lines: { value: raw } };
}

export function macd(candles: Candle[], params: IndicatorParams = {}): ComputeResult {
  const fast = Number(params.fast ?? 12);
  const slow = Number(params.slow ?? 26);
  const signal = Number(params.signal ?? 9);
  const closes = candles.map((c) => c.close);
  const fastE = emaArr(closes, fast);
  const slowE = emaArr(closes, slow);
  const macdRaw = closes.map((_, i) =>
    fastE[i] != null && slowE[i] != null ? (fastE[i] as number) - (slowE[i] as number) : null,
  );

  // signal = EMA of the defined portion of the MACD line
  const defined: { v: number; i: number }[] = [];
  macdRaw.forEach((v, i) => {
    if (v != null) defined.push({ v, i });
  });
  const sigVals = emaArr(
    defined.map((d) => d.v),
    signal,
  );
  const sigRaw: (number | null)[] = new Array(candles.length).fill(null);
  defined.forEach((d, j) => {
    if (sigVals[j] != null) sigRaw[d.i] = sigVals[j]!;
  });

  const hist: (number | null)[] = new Array(candles.length).fill(null);
  for (let i = 0; i < candles.length; i++) {
    const m = macdRaw[i];
    const s = sigRaw[i];
    hist[i] = m != null && s != null ? m - s : null;
  }
  return { lines: { macd: macdRaw, signal: sigRaw }, histograms: { hist } };
}

export function vwap(candles: Candle[]): ComputeResult {
  const raw: (number | null)[] = new Array(candles.length).fill(null);
  let cumPV = 0;
  let cumV = 0;
  let day: string | null = null;
  for (let i = 0; i < candles.length; i++) {
    const c = candles[i]!;
    const d = etDate(c.ts);
    if (d !== day) {
      cumPV = 0;
      cumV = 0;
      day = d;
    }
    const tp = (c.high + c.low + c.close) / 3;
    cumPV += tp * c.volume;
    cumV += c.volume;
    if (cumV > 0) raw[i] = cumPV / cumV;
  }
  return { lines: { value: raw } };
}

/** Wilder ATR (true range smoothed like RSI's averages). */
export function atr(candles: Candle[], params: IndicatorParams = {}): ComputeResult {
  const period = Number(params.period ?? 14);
  const raw: (number | null)[] = new Array(candles.length).fill(null);
  let acc = 0;
  let prev: number | null = null;
  for (let i = 0; i < candles.length; i++) {
    const c = candles[i]!;
    const pc = i > 0 ? candles[i - 1]!.close : c.close;
    const tr = Math.max(c.high - c.low, Math.abs(c.high - pc), Math.abs(c.low - pc));
    if (i < period) {
      acc += tr;
      if (i === period - 1) {
        prev = acc / period;
        raw[i] = prev;
      }
      continue;
    }
    prev = ((prev as number) * (period - 1) + tr) / period;
    raw[i] = prev;
  }
  return { lines: { value: raw } };
}

/** Session VWAP with ±1σ / ±2σ bands (σ of typical price around VWAP,
 *  volume-weighted, reset each ET day). */
export function vwapBands(candles: Candle[], params: IndicatorParams = {}): ComputeResult {
  const m1 = Number(params.mult1 ?? 1);
  const m2 = Number(params.mult2 ?? 2);
  const n = candles.length;
  const value: (number | null)[] = new Array(n).fill(null);
  const up1: (number | null)[] = new Array(n).fill(null);
  const dn1: (number | null)[] = new Array(n).fill(null);
  const up2: (number | null)[] = new Array(n).fill(null);
  const dn2: (number | null)[] = new Array(n).fill(null);
  let cumPV = 0;
  let cumV = 0;
  let cumPV2 = 0;
  let day: string | null = null;
  for (let i = 0; i < n; i++) {
    const c = candles[i]!;
    const d = etDate(c.ts);
    if (d !== day) {
      cumPV = 0;
      cumV = 0;
      cumPV2 = 0;
      day = d;
    }
    const tp = (c.high + c.low + c.close) / 3;
    cumPV += tp * c.volume;
    cumPV2 += tp * tp * c.volume;
    cumV += c.volume;
    if (cumV > 0) {
      const v = cumPV / cumV;
      const sd = Math.sqrt(Math.max(0, cumPV2 / cumV - v * v));
      value[i] = v;
      up1[i] = v + m1 * sd;
      dn1[i] = v - m1 * sd;
      up2[i] = v + m2 * sd;
      dn2[i] = v - m2 * sd;
    }
  }
  return { lines: { value, up1, dn1, up2, dn2 } };
}

/** VWAP anchored at `anchorTs` (0 = the start of the latest ET session). */
export function avwap(candles: Candle[], params: IndicatorParams = {}): ComputeResult {
  const n = candles.length;
  const raw: (number | null)[] = new Array(n).fill(null);
  if (n === 0) return { lines: { value: raw } };
  let anchor = Number(params.anchorTs ?? 0);
  if (!(anchor > 0)) {
    const lastDay = etDate(candles[n - 1]!.ts);
    let i = n - 1;
    while (i > 0 && etDate(candles[i - 1]!.ts) === lastDay) i--;
    anchor = candles[i]!.ts;
  }
  let cumPV = 0;
  let cumV = 0;
  for (let i = 0; i < n; i++) {
    const c = candles[i]!;
    if (c.ts < anchor) continue;
    const tp = (c.high + c.low + c.close) / 3;
    cumPV += tp * c.volume;
    cumV += c.volume;
    if (cumV > 0) raw[i] = cumPV / cumV;
  }
  return { lines: { value: raw } };
}

/** Four EMAs at once (9 / 20 / 50 / 200 by default). */
export function emaRibbon(candles: Candle[], params: IndicatorParams = {}): ComputeResult {
  const closes = candles.map((c) => c.close);
  const p = [params.p1 ?? 9, params.p2 ?? 20, params.p3 ?? 50, params.p4 ?? 200].map(Number);
  return { lines: { e1: emaArr(closes, p[0]), e2: emaArr(closes, p[1]), e3: emaArr(closes, p[2]), e4: emaArr(closes, p[3]) } };
}

/** SMA of volume, drawn in the volume band. */
export function volumeMa(candles: Candle[], params: IndicatorParams = {}): ComputeResult {
  const period = Number(params.period ?? 20);
  const raw: (number | null)[] = new Array(candles.length).fill(null);
  let sum = 0;
  for (let i = 0; i < candles.length; i++) {
    sum += candles[i]!.volume;
    if (i >= period) sum -= candles[i - period]!.volume;
    if (i >= period - 1) raw[i] = sum / period;
  }
  return { lines: { value: raw } };
}

const _etMin = new Intl.DateTimeFormat('en-US', { timeZone: 'America/New_York', hour: '2-digit', minute: '2-digit', hour12: false });
function etMinuteOfDay(ts: number): number {
  const [h, m] = _etMin.format(new Date(ts)).split(':').map(Number);
  return (h === 24 ? 0 : h) * 60 + m;
}

/** Levels for the latest session in the data: previous regular close,
 *  today's regular-session high/low so far, premarket high/low. */
export function sessionLevels(candles: Candle[]): ComputeResult {
  const n = candles.length;
  const levels: Record<string, number | null> = { prevClose: null, hod: null, lod: null, pmHigh: null, pmLow: null };
  if (n === 0) return { lines: {}, levels };
  const today = etDate(candles[n - 1]!.ts);
  let hod = -Infinity;
  let lod = Infinity;
  let pmHigh = -Infinity;
  let pmLow = Infinity;
  let i = n - 1;
  for (; i >= 0 && etDate(candles[i]!.ts) === today; i--) {
    const c = candles[i]!;
    const mod = etMinuteOfDay(c.ts);
    if (mod >= 570 && mod <= 960) {
      if (c.high > hod) hod = c.high;
      if (c.low < lod) lod = c.low;
    } else if (mod < 570) {
      if (c.high > pmHigh) pmHigh = c.high;
      if (c.low < pmLow) pmLow = c.low;
    }
  }
  // previous session's regular close: last bar at/before 16:00 ET on the prior day
  if (i >= 0) {
    const prevDay = etDate(candles[i]!.ts);
    let j = i;
    let close: number | null = null;
    for (; j >= 0 && etDate(candles[j]!.ts) === prevDay; j--) {
      if (etMinuteOfDay(candles[j]!.ts) <= 960) {
        close = candles[j]!.close;
        break;
      }
    }
    levels.prevClose = close ?? candles[i]!.close;
  }
  if (isFinite(hod)) levels.hod = hod;
  if (isFinite(lod)) levels.lod = lod;
  if (isFinite(pmHigh)) levels.pmHigh = pmHigh;
  if (isFinite(pmLow)) levels.pmLow = pmLow;
  return { lines: {}, levels };
}

export const COMPUTE = { sma, ema, bollinger, rsi, macd, vwap, atr, vwapBands, avwap, emaRibbon, volumeMa, sessionLevels } as const;

// ─── Registry (defs identical to the app) ───────────────────────────────────

const SOURCE_OPTIONS = ['close', 'open', 'hl2', 'hlc3', 'ohlc4'] as const;

export const INDICATOR_REGISTRY: IndicatorDef[] = [
  {
    id: 'sma',
    name: 'SMA',
    group: 'Trend',
    description: 'Simple moving average of price.',
    category: 'overlay',
    compute: 'sma',
    defaultParams: { period: 20, source: 'close' },
    paramSchema: [
      { key: 'period', label: 'Length', type: 'int', min: 1, max: 400 },
      { key: 'source', label: 'Source', type: 'enum', options: SOURCE_OPTIONS },
    ],
    render: [{ key: 'value', seriesType: 'line', style: { color: '#3b82f6', lineWidth: 2 } }],
  },
  {
    id: 'ema',
    name: 'EMA',
    group: 'Trend',
    description: 'Exponential moving average — reacts faster than the SMA.',
    category: 'overlay',
    compute: 'ema',
    defaultParams: { period: 20, source: 'close' },
    paramSchema: [
      { key: 'period', label: 'Length', type: 'int', min: 1, max: 400 },
      { key: 'source', label: 'Source', type: 'enum', options: SOURCE_OPTIONS },
    ],
    render: [{ key: 'value', seriesType: 'line', style: { color: '#f59e0b', lineWidth: 2 } }],
  },
  {
    id: 'bollinger',
    name: 'Bollinger Bands',
    group: 'Volatility',
    description: 'SMA with bands N standard deviations away.',
    category: 'overlay',
    compute: 'bollinger',
    defaultParams: { period: 20, mult: 2, source: 'close' },
    paramSchema: [
      { key: 'period', label: 'Length', type: 'int', min: 1, max: 400 },
      { key: 'mult', label: 'StdDev', type: 'float', min: 0.1, max: 10, step: 0.1 },
      { key: 'source', label: 'Source', type: 'enum', options: SOURCE_OPTIONS },
    ],
    render: [
      { key: 'upper', seriesType: 'line', style: { color: '#3b82f6', lineWidth: 1 } },
      { key: 'middle', seriesType: 'line', style: { color: '#f59e0b', lineWidth: 1, lineStyle: 'dashed' } },
      { key: 'lower', seriesType: 'line', style: { color: '#3b82f6', lineWidth: 1 } },
    ],
  },
  {
    id: 'vwap',
    name: 'VWAP',
    group: 'Volume',
    description: 'Volume-weighted average price, reset each session.',
    category: 'overlay',
    compute: 'vwap',
    defaultParams: {},
    paramSchema: [],
    render: [{ key: 'value', seriesType: 'line', style: { color: '#a855f7', lineWidth: 1, lineStyle: 'dashed' } }],
  },
  {
    id: 'rsi',
    name: 'RSI',
    group: 'Momentum',
    description: 'Relative strength index, 0–100 with 70 / 30 guides.',
    category: 'oscillator',
    compute: 'rsi',
    defaultParams: { period: 14 },
    paramSchema: [{ key: 'period', label: 'Length', type: 'int', min: 1, max: 100 }],
    render: [{ key: 'value', seriesType: 'line', style: { color: '#a855f7', lineWidth: 2 } }],
    guideLevels: [
      { value: 70, color: '#ef444460' },
      { value: 30, color: '#22c55e60' },
    ],
    range: { min: 0, max: 100 },
  },
  {
    id: 'macd',
    name: 'MACD',
    group: 'Momentum',
    description: 'Fast minus slow EMA, its signal line and histogram.',
    category: 'oscillator',
    compute: 'macd',
    defaultParams: { fast: 12, slow: 26, signal: 9 },
    paramSchema: [
      { key: 'fast', label: 'Fast', type: 'int', min: 1, max: 200 },
      { key: 'slow', label: 'Slow', type: 'int', min: 1, max: 200 },
      { key: 'signal', label: 'Signal', type: 'int', min: 1, max: 200 },
    ],
    render: [
      { key: 'hist', seriesType: 'histogram', label: 'Histogram', style: {} },
      { key: 'macd', seriesType: 'line', label: 'MACD', style: { color: '#3b82f6', lineWidth: 2 } },
      { key: 'signal', seriesType: 'line', label: 'Signal', style: { color: '#f59e0b', lineWidth: 2 } },
    ],
    guideLevels: [{ value: 0, color: '#6b728060' }],
  },
  {
    id: 'emaRibbon',
    name: 'EMA Ribbon',
    group: 'Trend',
    description: 'Four EMAs (9 / 20 / 50 / 200) as one indicator.',
    category: 'overlay',
    compute: 'emaRibbon',
    defaultParams: { p1: 9, p2: 20, p3: 50, p4: 200 },
    paramSchema: [
      { key: 'p1', label: 'EMA 1', type: 'int', min: 1, max: 400 },
      { key: 'p2', label: 'EMA 2', type: 'int', min: 1, max: 400 },
      { key: 'p3', label: 'EMA 3', type: 'int', min: 1, max: 400 },
      { key: 'p4', label: 'EMA 4', type: 'int', min: 1, max: 400 },
    ],
    render: [
      { key: 'e1', seriesType: 'line', label: 'EMA 1', style: { color: '#22c55e', lineWidth: 1 } },
      { key: 'e2', seriesType: 'line', label: 'EMA 2', style: { color: '#eab308', lineWidth: 1 } },
      { key: 'e3', seriesType: 'line', label: 'EMA 3', style: { color: '#f97316', lineWidth: 1 } },
      { key: 'e4', seriesType: 'line', label: 'EMA 4', style: { color: '#ef4444', lineWidth: 1 } },
    ],
  },
  {
    id: 'atr',
    name: 'ATR',
    group: 'Volatility',
    description: 'Average true range — how far price moves per bar.',
    category: 'oscillator',
    compute: 'atr',
    defaultParams: { period: 14 },
    paramSchema: [{ key: 'period', label: 'Length', type: 'int', min: 1, max: 200 }],
    render: [{ key: 'value', seriesType: 'line', style: { color: '#14b8a6', lineWidth: 2 } }],
  },
  {
    id: 'vwapBands',
    name: 'VWAP Bands',
    group: 'Volume',
    description: 'Session VWAP with ±1σ and ±2σ bands.',
    category: 'overlay',
    compute: 'vwapBands',
    defaultParams: { mult1: 1, mult2: 2 },
    paramSchema: [
      { key: 'mult1', label: 'Band 1 (σ)', type: 'float', min: 0.1, max: 5, step: 0.1 },
      { key: 'mult2', label: 'Band 2 (σ)', type: 'float', min: 0.1, max: 5, step: 0.1 },
    ],
    render: [
      { key: 'value', seriesType: 'line', label: 'VWAP', style: { color: '#a855f7', lineWidth: 1 } },
      { key: 'up1', seriesType: 'line', label: '+1σ', style: { color: '#a855f7', lineWidth: 1, lineStyle: 'dashed' } },
      { key: 'dn1', seriesType: 'line', label: '−1σ', style: { color: '#a855f7', lineWidth: 1, lineStyle: 'dashed' } },
      { key: 'up2', seriesType: 'line', label: '+2σ', style: { color: '#a855f7', lineWidth: 1, lineStyle: 'dotted' } },
      { key: 'dn2', seriesType: 'line', label: '−2σ', style: { color: '#a855f7', lineWidth: 1, lineStyle: 'dotted' } },
    ],
  },
  {
    id: 'avwap',
    name: 'Anchored VWAP',
    group: 'Volume',
    description: 'VWAP from a bar you pick — a swing low, a gap, an earnings bar.',
    category: 'overlay',
    compute: 'avwap',
    defaultParams: { anchorTs: 0 },
    paramSchema: [{ key: 'anchorTs', label: 'Anchor', type: 'timestamp' }],
    render: [{ key: 'value', seriesType: 'line', style: { color: '#ec4899', lineWidth: 2 } }],
  },
  {
    id: 'volumeMa',
    name: 'Volume MA',
    group: 'Volume',
    description: 'Moving average of volume, drawn over the volume bars.',
    category: 'overlay',
    compute: 'volumeMa',
    defaultParams: { period: 20 },
    paramSchema: [{ key: 'period', label: 'Length', type: 'int', min: 1, max: 400 }],
    render: [{ key: 'value', seriesType: 'line', scale: 'volume', style: { color: '#eab308', lineWidth: 1 } }],
  },
  {
    id: 'sessionLevels',
    name: 'Session Levels',
    group: 'Levels',
    description: 'Previous close, high / low of day and premarket high / low.',
    category: 'overlay',
    compute: 'sessionLevels',
    defaultParams: {},
    paramSchema: [],
    render: [
      { key: 'prevClose', seriesType: 'level', label: 'Prev close', style: { color: '#94a3b8', lineStyle: 'dashed' } },
      { key: 'hod', seriesType: 'level', label: 'HOD', style: { color: '#22c55e', lineStyle: 'dashed' } },
      { key: 'lod', seriesType: 'level', label: 'LOD', style: { color: '#ef4444', lineStyle: 'dashed' } },
      { key: 'pmHigh', seriesType: 'level', label: 'PM high', style: { color: '#e5c45c', lineStyle: 'dotted' } },
      { key: 'pmLow', seriesType: 'level', label: 'PM low', style: { color: '#e5c45c', lineStyle: 'dotted' } },
    ],
  },
];

const BY_ID: Record<string, IndicatorDef> = Object.fromEntries(INDICATOR_REGISTRY.map((d) => [d.id, d]));

export function getIndicator(id: string): IndicatorDef | null {
  return BY_ID[id] ?? null;
}

export function resolveParams(indicatorId: string, params?: IndicatorParams): IndicatorParams {
  const def = BY_ID[indicatorId];
  return { ...(def?.defaultParams ?? {}), ...(params ?? {}) };
}

let _seq = 0;
export function makeInstance(indicatorId: string, params?: IndicatorParams): IndicatorInstance | null {
  const def = BY_ID[indicatorId];
  if (!def) return null;
  return {
    instanceId: `ind-${++_seq}-${Math.floor(performance.now())}`,
    indicatorId,
    params: { ...def.defaultParams, ...(params ?? {}) },
    styleOverrides: {},
    visible: true,
  };
}

/** Short legend label — "EMA 20", "Bollinger Bands 20,2", … */
export function instanceLabel(inst: IndicatorInstance): string {
  const def = BY_ID[inst.indicatorId];
  if (!def) return inst.indicatorId;
  const p = inst.params ?? {};
  const nums = def.paramSchema
    .filter((s) => s.type === 'int' || s.type === 'float')
    .map((s) => p[s.key])
    .filter((v) => v != null);
  return nums.length ? `${def.name} ${nums.join(',')}` : def.name;
}
