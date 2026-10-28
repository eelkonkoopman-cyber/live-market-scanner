// NewsPanel: (1) the scan-membership selector only changes identity when the
// membership does (so a tick of the active scan leaves the panel alone), and
// (2) the rows print the same age / clock / date text, fresh and in-scan
// classes as the original per-row code did (native toLocale*String calls).
import { afterEach, describe, expect, it, vi } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { NewsPanel, createScanSymsSelector } from './NewsPanel';
import { useStore, type GridData } from '../store';
import type { NewsItem, ScanDef, TickFrame } from '../types';

type SelState = Parameters<ReturnType<typeof createScanSymsSelector>>[0];
const st = (activeId: string | null, grids: Record<string, string[]>): SelState => ({
  activeId,
  grids: Object.fromEntries(Object.entries(grids).map(([k, order]) => [k, { order }])),
});

describe('createScanSymsSelector', () => {
  it('hands back the same Set while the membership is unchanged, however the grid is rebuilt', () => {
    const sel = createScanSymsSelector();
    const a = sel(st('mom', { mom: ['AAA', 'BBB', 'CCC'] }));
    expect([...a].sort()).toEqual(['AAA', 'BBB', 'CCC']);
    expect(sel(st('mom', { mom: ['AAA', 'BBB', 'CCC'] }))).toBe(a); // a new order array every tick
    expect(sel(st('mom', { mom: ['CCC', 'AAA', 'BBB'] }))).toBe(a); // a re-rank
    const same = st('mom', { mom: ['BBB', 'CCC', 'AAA'], other: ['ZZZ'] });
    expect(sel(same)).toBe(a);
    expect(sel(same)).toBe(a); // and the very same state again
  });

  it('gives a new Set when a symbol enters, leaves or is swapped, and tracks the active scan', () => {
    const sel = createScanSymsSelector();
    const a = sel(st('mom', { mom: ['AAA', 'BBB'] }));
    const added = sel(st('mom', { mom: ['AAA', 'BBB', 'CCC'] }));
    expect(added).not.toBe(a);
    expect(added.has('CCC')).toBe(true);
    const swapped = sel(st('mom', { mom: ['AAA', 'BBB', 'DDD'] })); // same size, different member
    expect(swapped).not.toBe(added);
    expect(swapped.has('CCC')).toBe(false);
    expect(swapped.has('DDD')).toBe(true);
    const left = sel(st('mom', { mom: ['AAA'] }));
    expect([...left]).toEqual(['AAA']);
    const other = sel(st('gap', { mom: ['AAA'], gap: ['XXX'] }));
    expect([...other]).toEqual(['XXX']);
  });

  it('is the empty set with no active scan, no grid yet, or an empty grid — and stays the same empty set', () => {
    const sel = createScanSymsSelector();
    const none = sel(st(null, {}));
    expect(none.size).toBe(0);
    expect(sel(st('mom', {}))).toBe(none); // active scan, no grid cached yet
    expect(sel(st('mom', { mom: [] }))).toBe(none);
    sel(st('mom', { mom: ['AAA'] }));
    expect(sel(st(null, {})).size).toBe(0);
  });

  it('matches new Set(grid?.order ?? []) for any sequence of states', () => {
    const sel = createScanSymsSelector();
    const seq: SelState[] = [
      st(null, {}),
      st('a', { a: ['X', 'Y'] }),
      st('a', { a: ['Y', 'X'] }),
      st('a', { a: ['Y'] }),
      st('b', { a: ['Y'], b: ['Q', 'R', 'S'] }),
      st('b', { b: [] }),
      st('c', { b: [] }),
      st('a', { a: ['X', 'Y'] }),
    ];
    for (const s of seq) {
      const want = new Set(s.activeId ? (s.grids[s.activeId]?.order ?? []) : []);
      expect([...sel(s)].sort()).toEqual([...want].sort());
    }
  });

  it('works on the real store: applyTicks of the active scan with the same members keeps the Set', () => {
    const sel = createScanSymsSelector();
    useStore.setState({ activeId: 'mom', grids: {} });
    const tick = (seq: number, order: string[], full = false): TickFrame => ({ id: 'mom', seq, asOf: seq, full, order, rows: { [order[0]!]: { last: seq } as never } });
    useStore.getState().applyTicks([tick(1, ['AAA', 'BBB'], true)]);
    const a = sel(useStore.getState());
    const g1 = useStore.getState().grids.mom;
    useStore.getState().applyTicks([tick(2, ['BBB', 'AAA'])]);
    expect(useStore.getState().grids.mom).not.toBe(g1); // the store DID publish a new grid ...
    expect(sel(useStore.getState())).toBe(a); // ... and the selector's output did not move
    useStore.getState().applyTicks([tick(3, ['BBB', 'AAA', 'CCC'])]);
    expect(sel(useStore.getState())).not.toBe(a);
    useStore.setState({ activeId: null, grids: {} });
  });
});

// ─── the rows ────────────────────────────────────────────────────────────────

const NOW = Date.UTC(2026, 9, 14, 15, 0, 0);
const initial = useStore.getInitialState();
const saved = {
  news: initial.news,
  scans: initial.scans,
  activeId: initial.activeId,
  grids: initial.grids,
  chartSym: initial.chartSym,
  newsFollowSym: initial.newsFollowSym,
  newsScanOnly: initial.newsScanOnly,
  newsQuery: initial.newsQuery,
};

const item = (id: string, ageMs: number, tickers: string[]): NewsItem => ({
  id,
  tickers,
  headline: `Headline ${id}`,
  source: 'Benzinga',
  url: `https://example.com/${id}`,
  ts: NOW - ageMs,
  sentiment: 'neutral',
});

afterEach(() => {
  vi.useRealTimers();
  Object.assign(useStore.getInitialState(), saved);
});

// The row's time text exactly as the per-row code produced it before the cached formatters.
function originalAbs(ts: number): string {
  const d = new Date(ts);
  const time = d.toLocaleTimeString('en-GB', { hour12: false });
  if (d.toDateString() === new Date().toDateString()) return time;
  return `${d.toLocaleDateString('en-GB', { day: '2-digit', month: 'short' })} ${time}`;
}
function originalAgo(ts: number): string {
  const s = Math.max(0, Math.floor((Date.now() - ts) / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  return `${Math.floor(s / 3600)}h`;
}

describe('NewsPanel rows', () => {
  it('print the original age, clock and date text, and the fresh / in-scan classes', () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(NOW);
    const news = [
      item('a', 12_000, ['AAA']), // seconds old, fresh, in the scan
      item('b', 7 * 60_000 + 5_000, ['BBB']), // minutes old, not fresh
      item('c', 3 * 3_600_000, ['AAA', 'CCC']), // hours old (maybe yesterday's date in some zones)
      item('d', 3 * 86_400_000 + 1_000, ['DDD']), // days old: carries the date prefix
      item('e', 40 * 86_400_000, ['EEE']), // a month old
      item('f', 0, []), // just now, no tickers
    ];
    const grid: GridData = { order: ['AAA', 'ZZZ'], rows: {}, asOf: NOW };
    Object.assign(useStore.getInitialState(), {
      news,
      scans: [{ id: 'mom', name: 'Momentum' } as unknown as ScanDef],
      activeId: 'mom',
      grids: { mom: grid },
      chartSym: null,
      newsFollowSym: true,
      newsScanOnly: false,
      newsQuery: '',
    });
    const html = renderToStaticMarkup(<NewsPanel />);
    const rows = html.split('<div class="news-item ').slice(1);
    expect(rows.length).toBe(news.length);
    news.forEach((n, i) => {
      const row = rows[i]!;
      expect(row).toContain(`<span class="news-ago">${originalAgo(n.ts)}</span>`);
      expect(row).toContain(`<span class="news-abs">${originalAbs(n.ts)}</span>`);
      expect(row).toContain(`title="${new Date(n.ts).toLocaleString()}"`);
      const cls = /^([^"]*)"/.exec(row)![1]!.trim().split(/\s+/);
      expect(cls.includes('fresh')).toBe(NOW - n.ts < 5 * 60_000);
      expect(cls.includes('inscan')).toBe(n.tickers.some((t) => grid.order.includes(t)));
    });
    // spot values that do not depend on the machine's zone
    expect(rows[0]).toContain('<span class="news-ago">12s</span>');
    expect(rows[1]).toContain('<span class="news-ago">7m</span>');
    expect(rows[2]).toContain('<span class="news-ago">3h</span>');
    expect(rows[3]).toMatch(/news-abs">\d\d \w+ \d\d:\d\d:\d\d</);
  });
});
