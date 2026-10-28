import { describe, expect, it } from 'vitest';
import { fitCount, orderTabs } from './tabFit';

// Pills of width 100 with a 5px gap: n pills take n*105.
const W = [100, 100, 100, 100];
const GAP = 5;
const RESERVE = 80; // More + "+" + the gap between them
const PLUS = 24; // "+" alone

describe('fitCount', () => {
  it('returns every pill when everything fits (even with a tight margin)', () => {
    expect(fitCount(W, 1000, GAP, RESERVE, PLUS)).toBe(4);
    expect(fitCount([], 50, GAP, RESERVE, PLUS)).toBe(0);
  });

  it('returns 0 when not even one pill fits next to More and "+"', () => {
    // 100 + 5 + 80 = 185 > 150
    expect(fitCount(W, 150, GAP, RESERVE, PLUS)).toBe(0);
    expect(fitCount(W, 0, GAP, RESERVE, PLUS)).toBe(0);
  });

  it('is exact at the boundary', () => {
    // 2 pills + reserve = 210 + 80 = 290
    expect(fitCount(W, 290, GAP, RESERVE, PLUS)).toBe(2);
    expect(fitCount(W, 289, GAP, RESERVE, PLUS)).toBe(1);
    // 3 pills + reserve = 315 + 80 = 395
    expect(fitCount(W, 395, GAP, RESERVE, PLUS)).toBe(3);
    expect(fitCount(W, 394, GAP, RESERVE, PLUS)).toBe(2);
  });

  it('returns all pills when they fit only because More is not needed', () => {
    // 4 pills = 420, + "+" 24 = 444 fits in 444; but 4 pills + full reserve (500) would not.
    expect(fitCount(W, 444, GAP, RESERVE, PLUS)).toBe(4);
    // One px short: More becomes necessary, so only 3 fit beside it (395 <= 443).
    expect(fitCount(W, 443, GAP, RESERVE, PLUS)).toBe(3);
  });

  it('defaults reserveIfAll to reserve', () => {
    expect(fitCount(W, 499, GAP, RESERVE)).toBe(3);
    expect(fitCount(W, 500, GAP, RESERVE)).toBe(4);
  });

  it('copes with uneven widths and keeps scan order (leading pills only)', () => {
    expect(fitCount([200, 20, 20, 20], 290, GAP, RESERVE, PLUS)).toBe(1);
    expect(fitCount([20, 200, 20, 20], 250, GAP, RESERVE, PLUS)).toBe(1);
  });
});

describe('orderTabs', () => {
  const ids = ['a', 'b', 'c', 'd', 'e'];

  it('puts tiled scans first, in tile order, then the rest in scan order', () => {
    expect(orderTabs(ids, ['d', 'b'])).toEqual(['d', 'b', 'a', 'c', 'e']);
  });

  it('ignores tiled ids that are not in the scan list (and duplicates)', () => {
    expect(orderTabs(ids, ['zz', 'c', 'c'])).toEqual(['c', 'a', 'b', 'd', 'e']);
  });

  it('leaves the order unchanged when nothing is tiled', () => {
    expect(orderTabs(ids, [])).toEqual(ids);
  });
});
