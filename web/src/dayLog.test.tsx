// The Day Log window: filtering, sorting, the per-scan summary and the CSV
// export (dayLog.ts), the daylog.data -> store path, and the pane as it renders.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { DayLogPane } from './panes/DayLogPane';

// The status bar reads window.location and talks to the socket: not under test here.
vi.mock('./components/StatusBar', () => ({ StatusBar: () => null }));
import { useStore } from './store';
import {
  NO_FILTER,
  alertsToCsv,
  filterAlerts,
  fmtAge,
  nextSort,
  scanOptions,
  sortAlerts,
  summarizeByScan,
  symTerms,
  todayEt,
} from './dayLog';
import type { DayAlert, DayLogFrame } from './types';

const T = Date.UTC(2026, 9, 15, 14, 0); // 10:00 ET

function alert(over: Partial<DayAlert>): DayAlert {
  return {
    id: 1, day: '2026-10-15', at: T, sym: 'ABCD', scanId: 'gap', scanName: 'Gap Up', kind: 'enter',
    price: 2.5, chgDay: 30, vol: 900_000, rvol: 3, newsHeadline: '', newsAge: null, note: '', blacklisted: false,
    out5: null, out15: null, out30: null, maxUp: null, maxDown: null, done: false, ...over,
  };
}

const DAY: DayAlert[] = [
  alert({ id: 1, sym: 'AAA', at: T, out15: 4, out5: 1, price: 1 }),
  alert({ id: 2, sym: 'BBB', at: T + 60_000, out15: -2, price: 3 }),
  alert({ id: 3, sym: 'AAB', at: T + 120_000, out15: null, price: 2 }),
  alert({ id: 4, sym: 'ZZZ', at: T + 180_000, scanId: 'dildo', scanName: 'Green Dildo', out15: 6, blacklisted: true, note: 'early dildo' }),
];

describe('filter', () => {
  it('filters by scan, by symbol prefix (several terms) and hides blacklisted', () => {
    expect(filterAlerts(DAY, { ...NO_FILTER, scan: 'dildo' }).map((a) => a.sym)).toEqual(['ZZZ']);
    expect(filterAlerts(DAY, { ...NO_FILTER, query: 'aa' }).map((a) => a.sym)).toEqual(['AAA', 'AAB']);
    expect(filterAlerts(DAY, { ...NO_FILTER, query: 'bbb, zzz' }).map((a) => a.sym)).toEqual(['BBB', 'ZZZ']);
    expect(filterAlerts(DAY, { ...NO_FILTER, hideBlacklisted: true }).map((a) => a.sym)).toEqual(['AAA', 'BBB', 'AAB']);
    expect(filterAlerts(DAY, { scan: 'gap', query: 'a', hideBlacklisted: false }).map((a) => a.sym)).toEqual(['AAA', 'AAB']);
    expect(symTerms(' ab,, cd  ')).toEqual(['AB', 'CD']);
  });
});

describe('sort', () => {
  it('sorts outcome columns both ways and sinks unfilled rows to the bottom either way', () => {
    expect(sortAlerts(DAY, 'out15', 'desc').map((a) => a.sym)).toEqual(['ZZZ', 'AAA', 'BBB', 'AAB']);
    expect(sortAlerts(DAY, 'out15', 'asc').map((a) => a.sym)).toEqual(['BBB', 'AAA', 'ZZZ', 'AAB']);
  });
  it('sorts text and time, keeping time order on ties, and does not mutate', () => {
    expect(sortAlerts(DAY, 'sym', 'asc').map((a) => a.sym)).toEqual(['AAA', 'AAB', 'BBB', 'ZZZ']);
    expect(sortAlerts(DAY, 'at', 'desc').map((a) => a.id)).toEqual([4, 3, 2, 1]);
    expect(sortAlerts(DAY, 'scanName', 'desc').map((a) => a.id)).toEqual([4, 1, 2, 3]);
    expect(DAY.map((a) => a.id)).toEqual([1, 2, 3, 4]);
  });
  it('a header click flips the same column and starts a new numeric one descending', () => {
    expect(nextSort({ key: 'at', dir: 'desc' }, 'at')).toEqual({ key: 'at', dir: 'asc' });
    expect(nextSort({ key: 'at', dir: 'desc' }, 'out15')).toEqual({ key: 'out15', dir: 'desc' });
    expect(nextSort({ key: 'out15', dir: 'desc' }, 'sym')).toEqual({ key: 'sym', dir: 'asc' });
  });
});

describe('summary per scan', () => {
  it('counts alerts, averages the known +15m outcomes and gives the share that went up', () => {
    const s = summarizeByScan(DAY);
    expect(s.map((x) => x.scanId)).toEqual(['gap', 'dildo']); // busiest first
    expect(s[0]).toMatchObject({ count: 3, n15: 2, avg15: 1, upPct: 50 }); // (4 + -2) / 2, one of two up; the unfilled one is not a zero
    expect(s[1]).toMatchObject({ count: 1, n15: 1, avg15: 6, upPct: 100 });
    expect(summarizeByScan([alert({})])[0]).toMatchObject({ avg15: null, upPct: null });
    expect(scanOptions(DAY)).toEqual([
      { id: 'gap', name: 'Gap Up', count: 3 },
      { id: 'dildo', name: 'Green Dildo', count: 1 },
    ]);
  });
});

describe('formatting and export', () => {
  it('formats ages', () => {
    expect(fmtAge(null)).toBe('');
    expect(fmtAge(45)).toBe('45s');
    expect(fmtAge(12 * 60)).toBe('12m');
    expect(fmtAge(3 * 3600 + 5 * 60)).toBe('3h 05m');
  });
  it('exports the shown rows as CSV with ET times, blanks for unknowns and quoted text', () => {
    const csv = alertsToCsv([alert({ sym: 'AB', out15: 1.234, newsHeadline: 'Said "yes", then no', newsAge: 600, note: 'early dildo', blacklisted: true })]);
    const [head, row] = csv.trim().split('\r\n');
    expect(head.startsWith('date,time_et,symbol,scan,kind,price,')).toBe(true);
    expect(row).toBe('2026-10-15,10:00:00,AB,Gap Up,enter,2.5000,30.00,900000,3.00,10,"Said ""yes"", then no",early dildo,yes,,1.23,,,');
  });
  it('todayEt is the New York date', () => {
    expect(todayEt(Date.UTC(2026, 9, 16, 2, 0))).toBe('2026-10-15'); // 22:00 ET the evening before
  });
});

describe('store and pane', () => {
  // Server rendering reads the store's initial state (zustand's server snapshot), like the other render tests.
  const saved = useStore.getInitialState().dayLog;
  const seed = (dayLog: typeof saved) => Object.assign(useStore.getInitialState(), { dayLog });
  afterEach(() => {
    seed(saved);
    useStore.setState({ dayLog: saved });
    vi.unstubAllGlobals();
  });

  it('keeps the frame, loading and error states apart', () => {
    const s = useStore.getState();
    s.dayLogPending();
    expect(useStore.getState().dayLog).toMatchObject({ loading: true, error: null });
    const frame: DayLogFrame = { date: '2026-10-15', dates: ['2026-10-15'], alerts: DAY, movers: [], csvDir: 'C:\\x\\daylog' };
    s.setDayLog(frame);
    expect(useStore.getState().dayLog).toEqual({ frame, loading: false, error: null });
    s.dayLogFailed('boom');
    expect(useStore.getState().dayLog).toMatchObject({ frame, loading: false, error: 'boom' });
  });

  it('renders the alerts, the per-scan summary and the movers of today', () => {
    const frame: DayLogFrame = {
      date: todayEt(),
      dates: [todayEt()],
      alerts: DAY.map((a) => ({ ...a, day: todayEt() })),
      movers: [
        { day: todayEt(), side: 'gain', rank: 1, sym: 'MOON', price: 5, chgDay: 120, vol: 5_000_000, headline: 'Moon shot', takenAt: T },
        { day: todayEt(), side: 'loss', rank: 1, sym: 'FALL', price: 1, chgDay: -45, vol: 2_000_000, headline: '', takenAt: T },
      ],
      csvDir: 'C:\\data\\daylog',
    };
    seed({ frame, loading: false, error: null });
    const html = renderToStaticMarkup(<DayLogPane />);
    for (const want of ['AAA', 'BBB', 'ZZZ', 'Gap Up', 'Green Dildo', 'early dildo', 'MOON', '+120.00%', 'FALL', '-45.00%', 'Top gainers', 'Top losers', 'Export CSV', 'C:\\data\\daylog']) {
      expect(html).toContain(want);
    }
    expect(html).toContain('⛔'); // the blacklisted name is flagged
    expect(html).toContain('4 of 4 alerts');
  });

  it('says so when the day has no alerts', () => {
    seed({ frame: { date: todayEt(), dates: [todayEt()], alerts: [], movers: [], csvDir: '' }, loading: false, error: null });
    expect(renderToStaticMarkup(<DayLogPane />)).toContain('No alerts recorded yet today');
  });
});
