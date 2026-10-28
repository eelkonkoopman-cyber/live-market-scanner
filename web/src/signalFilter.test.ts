import { beforeEach, describe, expect, it, vi } from 'vitest';
import { EMPTY_SIGNAL_FILTER, KINDS_KEY, filterEvents, loadKindChips, presentKinds, saveKindChips } from './flagEvents';
import type { FlagEvent } from './types';

// 2026-10-08: EDT, so 12:00Z = 08:00 ET
const at = (h: number, m: number) => Date.UTC(2026, 9, 8, h + 4, m);
const ev = (sym: string, kind: FlagEvent['kind'], h: number, m: number): FlagEvent => ({ ts: at(h, m), at: at(h, m), sym, kind, label: kind, price: 10 });

const DAY = [ev('AAPL', 'dildo', 4, 10), ev('TSLA', 'vwapBull', 9, 45), ev('AAPL', 'vwapBear', 10, 30), ev('NVDA', 'power', 15, 0), ev('IMP', 'impulse', 10, 0)];

describe('filterEvents', () => {
  it('open filter keeps everything', () => {
    expect(filterEvents(DAY, EMPTY_SIGNAL_FILTER)).toHaveLength(5);
  });
  it('kinds are multi-select', () => {
    const r = filterEvents(DAY, { ...EMPTY_SIGNAL_FILTER, kinds: ['vwapBull', 'power'] });
    expect(r.map((e) => e.sym)).toEqual(['TSLA', 'NVDA']);
  });
  it('symbol search is a case-insensitive substring', () => {
    expect(filterEvents(DAY, { ...EMPTY_SIGNAL_FILTER, sym: ' aap' }).map((e) => e.kind)).toEqual(['dildo', 'vwapBear']);
  });
  it('time range is inclusive ET and tolerates one open end / bad input', () => {
    const f = (from: string, to: string) => filterEvents(DAY, { ...EMPTY_SIGNAL_FILTER, from, to }).map((e) => e.sym);
    expect(f('09:45', '10:30')).toEqual(['TSLA', 'AAPL', 'IMP']);
    expect(f('10:00', '')).toEqual(['AAPL', 'NVDA', 'IMP']);
    expect(f('', '04:10')).toEqual(['AAPL']);
    expect(f('nope', '')).toHaveLength(5);
  });
  it('hides blacklisted symbols', () => {
    const r = filterEvents(DAY, { ...EMPTY_SIGNAL_FILTER, hideBlacklisted: true }, (s) => s === 'AAPL');
    expect(r.map((e) => e.sym)).toEqual(['TSLA', 'NVDA', 'IMP']);
    expect(filterEvents(DAY, EMPTY_SIGNAL_FILTER, (s) => s === 'AAPL')).toHaveLength(5);
  });
  it('is fast on thousands of rows', () => {
    const big = Array.from({ length: 20000 }, (_, i) => ev('S' + (i % 500), i % 2 ? 'vwapBull' : 'dildo', 9, i % 60));
    const t = performance.now();
    const r = filterEvents(big, { ...EMPTY_SIGNAL_FILTER, kinds: ['dildo'], sym: 'S1', from: '09:10', to: '09:50' });
    expect(r.length).toBeGreaterThan(0);
    expect(performance.now() - t).toBeLessThan(500);
  });
});

describe('presentKinds + persistence', () => {
  beforeEach(() => {
    const m = new Map<string, string>();
    vi.stubGlobal('localStorage', { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v) });
  });
  it('lists present kinds without impulse, in badge order', () => {
    expect(presentKinds(DAY)).toEqual(['dildo', 'power', 'vwapBull', 'vwapBear']);
  });
  it('chip selection round-trips through localStorage', () => {
    expect(loadKindChips()).toEqual([]);
    saveKindChips(['dildo', 'vwapBull']);
    expect(loadKindChips()).toEqual(['dildo', 'vwapBull']);
    localStorage.setItem(KINDS_KEY, '{bad');
    expect(loadKindChips()).toEqual([]);
  });
});
