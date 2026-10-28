import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  DEFAULT_POSITION_SIZE,
  POSITION_SIZE_KEY,
  dildoPrice,
  dildoSharesSuffix,
  loadPositionSize,
  parsePositionSize,
  savePositionSize,
  sharesFor,
} from './positionSize';
import { dildoNewsSuffix } from './dildoNews';

describe('sharesFor', () => {
  it('floors to whole shares', () => {
    expect(sharesFor(2000, 1.6)).toBe(1250);
    expect(sharesFor(2000, 3)).toBe(666);
    expect(sharesFor(2000, 0.1)).toBe(20000);
  });
  it('is off at 0 / no price', () => {
    expect(sharesFor(0, 2)).toBe(0);
    expect(sharesFor(2000, null)).toBe(0);
    expect(sharesFor(2000, 0)).toBe(0);
  });
});

describe('dildoPrice', () => {
  it('prefers the dildo close, falls back to last', () => {
    expect(dildoPrice({ dildoClose: 2, last: 3 })).toBe(2);
    expect(dildoPrice({ last: 3 })).toBe(3);
    expect(dildoPrice(null)).toBeNull();
  });
});

describe('dildoSharesSuffix', () => {
  it('only for the Green Dildo, off at 0', () => {
    expect(dildoSharesSuffix('dildo-1m', { last: 1.6 }, 2000)).toBe('. 1,250 shares');
    expect(dildoSharesSuffix('dildo-1m', { last: 1.6 }, 0)).toBe('');
    expect(dildoSharesSuffix('e9flag-slam', { last: 1.6 }, 2000)).toBe('');
    expect(dildoSharesSuffix('dildo-1m', { last: 1500 }, 2000)).toBe('. 1 share');
  });
  it('comes after the news suffix', () => {
    const now = 1_000_000_000;
    const news = [{ ts: now - 10_000, tickers: ['ABCD'], headline: 'h' }] as never;
    const say = 'ABCD: early green dildo' + dildoNewsSuffix('dildo-1m', 'ABCD', news, now) + dildoSharesSuffix('dildo-1m', { last: 1.6 }, 2000) + '.';
    expect(say).toBe('ABCD: early green dildo. News headline 10 seconds ago. 1,250 shares.');
  });
});

describe('persistence', () => {
  beforeEach(() => {
    const m = new Map<string, string>();
    vi.stubGlobal('localStorage', {
      getItem: (k: string) => m.get(k) ?? null,
      setItem: (k: string, v: string) => void m.set(k, v),
    });
  });
  it('defaults to 2000, round-trips, empty = off', () => {
    expect(loadPositionSize()).toBe(DEFAULT_POSITION_SIZE);
    savePositionSize(5000);
    expect(loadPositionSize()).toBe(5000);
    localStorage.setItem(POSITION_SIZE_KEY, '');
    expect(loadPositionSize()).toBe(0);
    expect(parsePositionSize('junk')).toBe(DEFAULT_POSITION_SIZE);
  });
  it('survives blocked storage', () => {
    vi.stubGlobal('localStorage', { getItem: () => { throw new Error('x'); }, setItem: () => { throw new Error('x'); } });
    expect(loadPositionSize()).toBe(DEFAULT_POSITION_SIZE);
    expect(() => savePositionSize(1)).not.toThrow();
  });
});
