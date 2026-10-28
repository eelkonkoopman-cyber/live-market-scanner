//! Time axis — a TradingView-style tick ladder from minutes to years.
//
// The level is chosen from px-per-bar × bar interval so labels stay ~80 px
// apart at any zoom; ticks land on the first bar at/after each boundary, so
// overnight and weekend gaps (which the index-based scale collapses) never
// produce phantom ticks. Day / month / year boundaries are drawn brighter
// than plain times, and daily/weekly charts skip the minute levels entirely.

import type { DataModel } from '../core/datamodel';
import type { TimeScale } from '../core/timescale';
import type { ChartColors, Rect } from '../core/types';
import { align } from './layers';
import { font } from './fonts';

type Wall = { y: number; mo: number; d: number; h: number; mi: number; wd: number; dayKey: number };

const MON = ['', 'Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const WD: Record<string, number> = { Sun: 0, Mon: 1, Tue: 2, Wed: 3, Thu: 4, Fri: 5, Sat: 6 };

const _fmt = new Map<string, Intl.DateTimeFormat>();
function fmt(tz: string): Intl.DateTimeFormat {
  let f = _fmt.get(tz);
  if (!f) {
    f = new Intl.DateTimeFormat('en-US', {
      timeZone: tz,
      year: 'numeric',
      month: 'numeric',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
      weekday: 'short',
      hour12: false,
    });
    _fmt.set(tz, f);
  }
  return f;
}

// Wall-clock parts per timestamp, memoised (a pan re-labels the same bars).
const _wall = new Map<string, Map<number, Wall>>();
function wall(ts: number, tz: string): Wall {
  let cache = _wall.get(tz);
  if (!cache) {
    cache = new Map();
    _wall.set(tz, cache);
  }
  let w = cache.get(ts);
  if (w) return w;
  if (cache.size > 20_000) cache.clear();
  const g: Record<string, string> = {};
  for (const p of fmt(tz).formatToParts(new Date(ts))) if (p.type !== 'literal') g[p.type] = p.value;
  const h = Number(g.hour) === 24 ? 0 : Number(g.hour);
  const y = Number(g.year);
  const mo = Number(g.month);
  const d = Number(g.day);
  w = { y, mo, d, h, mi: Number(g.minute), wd: WD[g.weekday] ?? 0, dayKey: y * 10000 + mo * 100 + d };
  cache.set(ts, w);
  return w;
}

type Level =
  | { kind: 'minutes'; step: number }
  | { kind: 'days'; every: number }
  | { kind: 'weeks' }
  | { kind: 'months'; every: number }
  | { kind: 'years'; every: number };

const MIN = 60_000;
const DAY = 86_400_000;
// `ms` sizes a rung for intraday data; `bars` sizes it for daily/weekly data,
// where a week is 5 bars and a month ~21 (weekends and holidays are not bars).
const LADDER: { level: Level; ms: number; bars: number }[] = [
  ...[1, 2, 3, 5, 10, 15, 30, 60, 120, 180, 240, 360, 720].map((m) => ({ level: { kind: 'minutes', step: m } as Level, ms: m * MIN, bars: 0 })),
  { level: { kind: 'days', every: 1 }, ms: DAY, bars: 1 },
  { level: { kind: 'days', every: 2 }, ms: 2 * DAY, bars: 2 },
  { level: { kind: 'weeks' }, ms: 7 * DAY, bars: 5 },
  { level: { kind: 'months', every: 1 }, ms: 30 * DAY, bars: 21 },
  { level: { kind: 'months', every: 3 }, ms: 91 * DAY, bars: 63 },
  { level: { kind: 'months', every: 6 }, ms: 182 * DAY, bars: 126 },
  { level: { kind: 'years', every: 1 }, ms: 365 * DAY, bars: 252 },
  { level: { kind: 'years', every: 2 }, ms: 730 * DAY, bars: 504 },
  { level: { kind: 'years', every: 5 }, ms: 1826 * DAY, bars: 1260 },
  { level: { kind: 'years', every: 10 }, ms: 3652 * DAY, bars: 2520 },
];

// Minimum px between ticks when choosing the rung; the per-label collision
// check below keeps wider labels apart. Short day numbers can sit closer.
const TICK_PX = 50;

function pickLevel(barSpacing: number, interval: number): Level {
  const bs = Math.max(0.1, barSpacing);
  const daily = interval >= DAY;
  if (daily) {
    const perBar = interval / DAY; // 1 for daily, ~7 for weekly bars
    const targetBars = TICK_PX / bs;
    for (const rung of LADDER) {
      if (rung.level.kind === 'minutes') continue;
      if (rung.bars / perBar >= targetBars) return rung.level;
    }
  } else {
    const target = (TICK_PX / bs) * interval;
    for (const rung of LADDER) if (rung.ms >= target) return rung.level;
  }
  return LADDER[LADDER.length - 1].level;
}

/** Label for bar `w` when it starts a tick of `level` after `p`; null otherwise. */
function tickLabel(level: Level, w: Wall, p: Wall | null, ts: number, prevTs: number, intraday: boolean): { text: string; major: boolean } | null {
  const dayChanged = !p || w.dayKey !== p.dayKey;
  const monthChanged = !p || w.y !== p.y || w.mo !== p.mo;
  const yearChanged = !p || w.y !== p.y;
  const dateLabel = (): { text: string; major: boolean } => {
    if (yearChanged) return { text: String(w.y), major: true };
    if (monthChanged) return { text: MON[w.mo], major: true };
    return { text: intraday && level.kind === 'minutes' ? `${MON[w.mo]} ${w.d}` : String(w.d), major: true };
  };
  switch (level.kind) {
    case 'minutes': {
      if (dayChanged) return dateLabel();
      const slot = Math.floor((w.h * 60 + w.mi) / level.step);
      const pslot = Math.floor(((p as Wall).h * 60 + (p as Wall).mi) / level.step);
      if (slot === pslot) return null;
      return { text: `${String(w.h).padStart(2, '0')}:${String(w.mi).padStart(2, '0')}`, major: false };
    }
    case 'days': {
      if (!dayChanged) return null;
      if (level.every > 1 && !monthChanged && Math.floor(ts / DAY) % level.every !== 0) return null;
      return dateLabel();
    }
    case 'weeks': {
      if (!dayChanged) return null;
      const weekStart = !p || w.wd < p.wd || ts - prevTs >= 6 * DAY;
      if (!weekStart && !monthChanged) return null;
      return dateLabel();
    }
    case 'months': {
      if (!monthChanged) return null;
      if (level.every > 1 && !yearChanged && (w.mo - 1) % level.every !== 0) return null;
      return dateLabel();
    }
    case 'years': {
      if (!yearChanged) return null;
      if (level.every > 1 && w.y % level.every !== 0) return null;
      return { text: String(w.y), major: true };
    }
  }
}

/** Vertical time grid across the whole stacked area + labels under the bottom pane. */
export function drawTimeAxis(
  ctx: CanvasRenderingContext2D,
  content: Rect,
  data: DataModel,
  ts: TimeScale,
  colors: ChartColors,
  tz: string,
  showGrid: boolean,
): void {
  const n = data.length;
  if (n === 0) return;
  const interval = data.interval();
  const level = pickLevel(ts.barSpacing, interval);
  const intraday = interval < DAY;
  const range = ts.visibleIndexRange();
  const from = Math.max(0, Math.floor(range.from));
  const to = Math.min(n - 1, Math.ceil(range.to));

  ctx.font = font(11);
  ctx.textBaseline = 'alphabetic';
  ctx.textAlign = 'center';
  let lastRight = -Infinity;
  for (let i = from; i <= to; i++) {
    const t = data.time(i);
    const w = wall(t, tz);
    const prevTs = i > 0 ? data.time(i - 1) : t;
    const p = i > 0 ? wall(prevTs, tz) : null;
    const tick = tickLabel(level, w, p, t, prevTs, intraday);
    if (!tick) continue;
    const x = ts.indexToX(i);
    if (x < content.x - 1 || x > content.x + content.w + 1) continue;
    const tw = ctx.measureText(tick.text).width;
    if (x - tw / 2 < lastRight + 10) continue;
    lastRight = x + tw / 2;
    if (showGrid) {
      ctx.strokeStyle = colors.grid;
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.moveTo(align(x), content.y);
      ctx.lineTo(align(x), content.y + content.h);
      ctx.stroke();
    }
    ctx.fillStyle = tick.major ? colors.legendText : colors.axisText;
    ctx.fillText(tick.text, x, content.y + content.h + 14);
  }
}
