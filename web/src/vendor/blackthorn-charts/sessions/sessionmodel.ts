//! Declarative session model + DST-correct timezone math (BUILD-PLAN §2.1–2.4).
//
// Sessions are DATA. Timezone resolution is done via the browser `Intl` API (no
// Luxon dependency): we probe the tz offset at a guessed instant and refine once,
// which handles spring-forward / fall-back without hand-rolling DST.

export type SessionSegment = {
  type: 'premarket' | 'regular' | 'postmarket';
  /** 'HH:MM' in the exchange tz */
  start: string;
  end: string;
  /** null = unshaded */
  color: string | null;
  border?: 'dashed' | 'solid' | null;
};

export type SessionDef = {
  name: string;
  /** IANA tz, e.g. 'America/New_York' */
  timezone: string;
  /** trading weekdays, 1..5 = Mon..Fri */
  days: number[];
  segments: SessionSegment[];
  /** 'YYYY-MM-DD' full-day closes */
  holidays: string[];
  /** 'YYYY-MM-DD' -> early close 'HH:MM' */
  halfDays: Record<string, string>;
};

export type BandRange = {
  type: SessionSegment['type'];
  startTs: number;
  endTs: number;
  color: string;
  border?: 'dashed' | 'solid' | null;
};

// NYSE full-day closes, 2024–2027 (matches the desktop session window).
export const NYSE_HOLIDAYS: string[] = [
  // 2024
  '2024-01-01', '2024-01-15', '2024-02-19', '2024-03-29', '2024-05-27',
  '2024-06-19', '2024-07-04', '2024-09-02', '2024-11-28', '2024-12-25',
  // 2025
  '2025-01-01', '2025-01-20', '2025-02-17', '2025-04-18', '2025-05-26',
  '2025-06-19', '2025-07-04', '2025-09-01', '2025-11-27', '2025-12-25',
  // 2026
  '2026-01-01', '2026-01-19', '2026-02-16', '2026-04-03', '2026-05-25',
  '2026-06-19', '2026-07-03', '2026-09-07', '2026-11-26', '2026-12-25',
  // 2027
  '2027-01-01', '2027-01-18', '2027-02-15', '2027-03-26', '2027-05-31',
  '2027-06-18', '2027-07-05', '2027-09-06', '2027-11-25', '2027-12-24',
];

// Half-days: early close 13:00 ET (day after Thanksgiving, Christmas Eve, July 3).
export const NYSE_HALF_DAYS: Record<string, string> = {
  '2024-07-03': '13:00', '2024-11-29': '13:00', '2024-12-24': '13:00',
  '2025-07-03': '13:00', '2025-11-28': '13:00', '2025-12-24': '13:00',
  '2026-11-27': '13:00', '2026-12-24': '13:00',
  '2027-11-26': '13:00',
};

export const US_EQUITIES: SessionDef = {
  name: 'US Equities',
  timezone: 'America/New_York',
  days: [1, 2, 3, 4, 5],
  segments: [
    { type: 'premarket', start: '04:00', end: '09:30', color: 'rgba(229,196,92,0.12)', border: 'dashed' },
    { type: 'regular', start: '09:30', end: '16:00', color: null },
    { type: 'postmarket', start: '16:00', end: '20:00', color: 'rgba(80,130,200,0.16)', border: 'dashed' },
  ],
  holidays: NYSE_HOLIDAYS,
  halfDays: NYSE_HALF_DAYS,
};

const _fmtCache = new Map<string, Intl.DateTimeFormat>();
function fmt(tz: string): Intl.DateTimeFormat {
  let f = _fmtCache.get(tz);
  if (!f) {
    f = new Intl.DateTimeFormat('en-US', {
      timeZone: tz,
      year: 'numeric', month: '2-digit', day: '2-digit',
      hour: '2-digit', minute: '2-digit', second: '2-digit',
      hour12: false,
    });
    _fmtCache.set(tz, f);
  }
  return f;
}

/** Offset in ms such that (utcMs + offset) read as UTC equals the tz wall clock. */
function tzOffsetMs(utcMs: number, tz: string): number {
  const parts = fmt(tz).formatToParts(new Date(utcMs));
  const g: Record<string, number> = {};
  for (const p of parts) if (p.type !== 'literal') g[p.type] = Number(p.value);
  // 'hour' can come back as 24 at midnight in some engines — normalize.
  const hour = g.hour === 24 ? 0 : g.hour;
  const asUtc = Date.UTC(g.year, g.month - 1, g.day, hour, g.minute, g.second);
  return asUtc - utcMs;
}

/** Map (calendar date in tz, 'HH:MM' wall time in tz) → UTC ms, DST-aware. */
export function wallToUtc(dateISO: string, hhmm: string, tz: string): number {
  const [Y, M, D] = dateISO.split('-').map(Number);
  const [h, m] = hhmm.split(':').map(Number);
  const guess = Date.UTC(Y, M - 1, D, h, m, 0);
  // Refine twice: the offset itself depends on the instant near DST edges.
  let utc = guess - tzOffsetMs(guess, tz);
  utc = guess - tzOffsetMs(utc, tz);
  return utc;
}

/** Wall-clock components of a UTC instant in tz. */
export function utcToWall(ts: number, tz: string): { dateISO: string; hhmm: string; weekday: number } {
  const parts = fmt(tz).formatToParts(new Date(ts));
  const g: Record<string, number> = {};
  for (const p of parts) if (p.type !== 'literal') g[p.type] = Number(p.value);
  const hour = g.hour === 24 ? 0 : g.hour;
  const dateISO = `${g.year}-${pad2(g.month)}-${pad2(g.day)}`;
  const hhmm = `${pad2(hour)}:${pad2(g.minute)}`;
  // weekday from a UTC date built from the wall components (0=Sun..6=Sat)
  const weekday = new Date(Date.UTC(g.year, g.month - 1, g.day)).getUTCDay();
  return { dateISO, hhmm, weekday };
}

function pad2(n: number): string {
  return n < 10 ? '0' + n : '' + n;
}

function isTradingDay(dateISO: string, weekday: number, def: SessionDef): boolean {
  if (!def.days.includes(weekday)) return false;
  if (def.holidays.includes(dateISO)) return false;
  return true;
}

/** Clamp 'HH:MM' to the half-day early close if one applies to this date. */
function effectiveEnd(dateISO: string, end: string, def: SessionDef): string {
  const close = def.halfDays[dateISO];
  if (close && end > close) return close;
  return end;
}

/** Concrete colored band ranges for each trading day in [fromTs, toTs] (+1d padding). */
export function sessionRanges(fromTs: number, toTs: number, def: SessionDef): BandRange[] {
  const out: BandRange[] = [];
  const DAY = 86_400_000;
  // Walk calendar days in the exchange tz, with ±1 day padding.
  for (let t = fromTs - DAY; t <= toTs + DAY; t += DAY) {
    const { dateISO, weekday } = utcToWall(t, def.timezone);
    if (!isTradingDay(dateISO, weekday, def)) continue;
    for (const seg of def.segments) {
      if (!seg.color) continue; // regular session is unshaded
      const end = effectiveEnd(dateISO, seg.end, def);
      if (end <= seg.start) continue; // half-day collapsed this segment
      const startTs = wallToUtc(dateISO, seg.start, def.timezone);
      const endTs = wallToUtc(dateISO, end, def.timezone);
      out.push({ type: seg.type, startTs, endTs, color: seg.color, border: seg.border ?? null });
    }
  }
  // De-dup days the padding may have visited twice.
  out.sort((a, b) => a.startTs - b.startTs);
  const dedup: BandRange[] = [];
  for (const r of out) {
    const prev = dedup[dedup.length - 1];
    if (prev && prev.startTs === r.startTs && prev.type === r.type) continue;
    dedup.push(r);
  }
  return dedup;
}
