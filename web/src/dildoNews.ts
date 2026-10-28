// Green Dildo + news (2026-10-13): when a green dildo fires and the stock had a
// headline shortly before, the alert says so — "ABCD: green dildo. News
// headline 10 seconds ago" — so the trader knows which hotkeys to reach for.
// The headline comes from the browser's own news store (the 'news' frames).
// Only the Green Dildo scan (dildo-1m) is touched; every other alert is as was.
import type { NewsItem } from './types';

export const GREEN_DILDO_SCAN_ID = 'dildo-1m';
// A headline counts when it is at most this old at the moment of the alert.
export const DILDO_NEWS_WINDOW_MS = 60 * 60 * 1000;

// latestNewsFor: the newest headline for `sym` published at or before `atMs`
// and no older than DILDO_NEWS_WINDOW_MS at that moment, else null.
export function latestNewsFor(news: readonly NewsItem[], sym: string, atMs: number): NewsItem | null {
  if (!sym) return null;
  let best: NewsItem | null = null;
  for (const n of news) {
    if (!n.tickers?.includes(sym)) continue;
    const age = atMs - n.ts;
    if (age < 0 || age > DILDO_NEWS_WINDOW_MS) continue;
    if (!best || n.ts > best.ts) best = n;
  }
  return best;
}

// spokenAge: '1 second', '10 seconds', '1 minute', '4 minutes' (whole minutes from 60 s).
export function spokenAge(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return s === 1 ? '1 second' : `${s} seconds`;
  const m = Math.floor(s / 60);
  return m === 1 ? '1 minute' : `${m} minutes`;
}

// shortAge: '10s' / '4m' for the NEWS badge.
export function shortAge(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m`;
}

// dildoNewsSuffix: '. News headline 10 seconds ago' for a Green Dildo alert
// with a headline in the window, '' otherwise (and for every other scan).
export function dildoNewsSuffix(scanId: string, sym: string, news: readonly NewsItem[], atMs: number): string {
  if (scanId !== GREEN_DILDO_SCAN_ID) return '';
  const n = latestNewsFor(news, sym, atMs);
  return n ? `. News headline ${spokenAge(atMs - n.ts)} ago` : '';
}

// newsBadge: the 'NEWS 4m' badge (age measured at `ageAtMs`, which is the alert
// time for history rows, now for the live list) with the headline as tooltip;
// null when there is no headline within the window of the alert time.
export function newsBadge(
  news: readonly NewsItem[],
  sym: string,
  alertAtMs: number,
  ageAtMs: number = alertAtMs,
): { label: string; title: string } | null {
  const n = latestNewsFor(news, sym, alertAtMs);
  return n ? { label: `NEWS ${shortAge(ageAtMs - n.ts)}`, title: n.headline } : null;
}
