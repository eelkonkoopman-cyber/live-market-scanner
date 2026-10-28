// Day Log pane logic (?pane=daylog): the pure parts - filtering, sorting, the
// per-scan summary and the CSV export of what the server recorded
// (internal/daylog: every scan alert of an ET day with its +5/+15/+30 minute
// outcome, plus the end-of-day top gainers / losers). No React in here.
import type { DayAlert } from './types';
import { etDayKey } from './marketClock';

export type OutcomeKey = 'out5' | 'out15' | 'out30' | 'maxUp' | 'maxDown';
export type SortKey = 'at' | 'sym' | 'scanName' | 'price' | 'chgDay' | 'vol' | 'rvol' | 'newsAge' | OutcomeKey;
export type SortDir = 'asc' | 'desc';

export const OUTCOME_KEYS: OutcomeKey[] = ['out5', 'out15', 'out30', 'maxUp', 'maxDown'];

// todayEt is the date the picker opens on: today in New York, not the PC's.
export function todayEt(nowMs: number = Date.now()): string {
  return etDayKey(nowMs);
}

export interface DayLogFilter {
  scan: string; // scan id, '' = every scan
  query: string; // symbol search: prefix match on the symbol, case-insensitive; ',' or space lists several
  hideBlacklisted: boolean;
}

export const NO_FILTER: DayLogFilter = { scan: '', query: '', hideBlacklisted: false };

// symTerms: the search box as a list of upper-case terms.
export function symTerms(query: string): string[] {
  return query
    .toUpperCase()
    .split(/[\s,]+/)
    .filter((t) => t.length > 0);
}

export function filterAlerts(alerts: readonly DayAlert[], f: DayLogFilter): DayAlert[] {
  const terms = symTerms(f.query);
  return alerts.filter((a) => {
    if (f.scan && a.scanId !== f.scan) return false;
    if (f.hideBlacklisted && a.blacklisted) return false;
    if (terms.length > 0 && !terms.some((t) => a.sym.toUpperCase().startsWith(t))) return false;
    return true;
  });
}

function keyValue(a: DayAlert, key: SortKey): number | string | null {
  switch (key) {
    case 'sym':
      return a.sym;
    case 'scanName':
      return a.scanName || a.scanId;
    default:
      return a[key];
  }
}

// sortAlerts: a new array. Rows with no value in the sort column (an outcome not
// filled yet, no RVOL ...) always sink to the bottom, whichever way it sorts;
// ties keep time order.
export function sortAlerts(alerts: readonly DayAlert[], key: SortKey, dir: SortDir): DayAlert[] {
  const sign = dir === 'asc' ? 1 : -1;
  return alerts
    .map((a, i) => ({ a, i }))
    .sort((x, y) => {
      const vx = keyValue(x.a, key);
      const vy = keyValue(y.a, key);
      if (vx === null && vy === null) return x.i - y.i;
      if (vx === null) return 1;
      if (vy === null) return -1;
      let c = 0;
      if (typeof vx === 'string' && typeof vy === 'string') c = vx.localeCompare(vy);
      else c = (vx as number) - (vy as number);
      return c !== 0 ? sign * c : x.i - y.i;
    })
    .map((e) => e.a);
}

// nextSort: what a header click does - the same column flips direction, a new
// column starts descending (time starts ascending, symbol and scan too).
export function nextSort(cur: { key: SortKey; dir: SortDir }, key: SortKey): { key: SortKey; dir: SortDir } {
  if (cur.key === key) return { key, dir: cur.dir === 'asc' ? 'desc' : 'asc' };
  return { key, dir: key === 'at' || key === 'sym' || key === 'scanName' ? 'asc' : 'desc' };
}

export interface ScanSummary {
  scanId: string;
  scanName: string;
  count: number;
  n15: number; // alerts whose +15m outcome is known
  avg15: number | null; // mean +15m %, null when none known
  upPct: number | null; // % of the known +15m outcomes that are above zero
}

// summarizeByScan: per scan - alert count, mean +15 minute change and the share
// that went up - busiest scan first. Only alerts with a known +15m outcome enter
// the average and the share (an unfilled outcome is not a zero).
export function summarizeByScan(alerts: readonly DayAlert[]): ScanSummary[] {
  const by = new Map<string, { name: string; count: number; sum: number; n: number; up: number }>();
  for (const a of alerts) {
    let e = by.get(a.scanId);
    if (!e) {
      e = { name: a.scanName || a.scanId, count: 0, sum: 0, n: 0, up: 0 };
      by.set(a.scanId, e);
    }
    e.count++;
    if (a.out15 !== null) {
      e.n++;
      e.sum += a.out15;
      if (a.out15 > 0) e.up++;
    }
  }
  return [...by.entries()]
    .map(([scanId, e]) => ({
      scanId,
      scanName: e.name,
      count: e.count,
      n15: e.n,
      avg15: e.n > 0 ? e.sum / e.n : null,
      upPct: e.n > 0 ? (e.up / e.n) * 100 : null,
    }))
    .sort((a, b) => b.count - a.count || a.scanName.localeCompare(b.scanName));
}

// scanOptions: the scans that fired on the day, for the filter menu.
export function scanOptions(alerts: readonly DayAlert[]): { id: string; name: string; count: number }[] {
  return summarizeByScan(alerts).map((s) => ({ id: s.scanId, name: s.scanName, count: s.count }));
}

const ET_TIME = new Intl.DateTimeFormat('en-GB', {
  timeZone: 'America/New_York',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  hour12: false,
});

// fmtEtTime: HH:MM:SS Eastern.
export function fmtEtTime(ms: number): string {
  return ET_TIME.format(new Date(ms));
}

// fmtAge: seconds as '45s', '12m', '3h 05m'.
export function fmtAge(sec: number | null): string {
  if (sec === null || !isFinite(sec)) return '';
  if (sec < 0) return '0s';
  if (sec < 90) return `${Math.round(sec)}s`;
  if (sec < 3600) return `${Math.round(sec / 60)}m`;
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  return `${h}h ${String(m).padStart(2, '0')}m`;
}

function csvCell(v: string): string {
  return /[",\r\n]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v;
}

function num(v: number | null, dec: number): string {
  return v === null || !isFinite(v) ? '' : v.toFixed(dec);
}

export const CSV_HEADER = [
  'date', 'time_et', 'symbol', 'scan', 'kind', 'price', 'chg_day_pct', 'volume', 'rvol', 'news_age_min',
  'news_headline', 'note', 'blacklisted', 'chg_5m_pct', 'chg_15m_pct', 'chg_30m_pct', 'max_up_30m_pct', 'max_down_30m_pct',
];

// alertsToCsv: the rows as shown (same columns as the server's own daylog/<date>.csv).
export function alertsToCsv(alerts: readonly DayAlert[]): string {
  const lines = [CSV_HEADER.join(',')];
  for (const a of alerts) {
    lines.push(
      [
        a.day, fmtEtTime(a.at), a.sym, a.scanName || a.scanId, a.kind, num(a.price, 4), num(a.chgDay, 2),
        a.vol === null ? '' : String(a.vol), num(a.rvol, 2), a.newsAge === null ? '' : String(Math.floor(a.newsAge / 60)),
        a.newsHeadline, a.note, a.blacklisted ? 'yes' : '', num(a.out5, 2), num(a.out15, 2), num(a.out30, 2), num(a.maxUp, 2), num(a.maxDown, 2),
      ]
        .map(csvCell)
        .join(','),
    );
  }
  return lines.join('\r\n') + '\r\n';
}
