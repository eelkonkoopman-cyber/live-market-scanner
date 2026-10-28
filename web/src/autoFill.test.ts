import { describe, expect, it } from 'vitest';
import {
  AUTO_ENTRY_MS,
  AUTO_EXIT_MS,
  AUTO_NEW_MS,
  AUTO_SWAP_GAP_MS,
  autoStep,
  emptyMemo,
  rankSyms,
  resolveAutoScan,
  type AutoMemo,
} from './autoFill';

const syms = (n: number, prefix = 'S') => Array.from({ length: n }, (_, i) => `${prefix}${i + 1}`);

// A tiny driver: holds cells/memo and advances the clock.
function sim(opts: { cells: (string | null)[]; pinned?: boolean[] }) {
  let cells = opts.cells;
  const pinned = opts.pinned ?? cells.map(() => false);
  let memo: AutoMemo = emptyMemo();
  let t = 1_000_000;
  const api = {
    get cells() {
      return cells;
    },
    get memo() {
      return memo;
    },
    get t() {
      return t;
    },
    advance(ms: number) {
      t += ms;
    },
    step(ranking: string[], extra: { held?: boolean; initial?: boolean } = {}) {
      const r = autoStep({ cells, pinned, ranking, now: t, held: !!extra.held, initial: !!extra.initial, memo });
      cells = r.cells;
      memo = r.memo;
      return r.changed;
    },
  };
  return api;
}

describe('rankSyms', () => {
  const grid = {
    order: ['A', 'B', 'C', 'D', 'E'],
    rows: { A: { chgDay: 5 }, B: { chgDay: -2 }, C: { chgDay: 12 }, D: { chgDay: 0 }, E: { chg5m: 3 } },
  };
  it('keeps only positive movers for gainers, sorted descending', () => {
    expect(rankSyms(grid, 'chgDay', 'desc')).toEqual(['C', 'A']);
  });
  it('keeps only negative movers for decliners, worst first', () => {
    expect(rankSyms(grid, 'chgDay', 'asc')).toEqual(['B']);
  });
  it('uses the requested metric and tolerates a missing grid', () => {
    expect(rankSyms(grid, 'chg5m', 'desc')).toEqual(['E']);
    expect(rankSyms(undefined, 'chgDay', 'desc')).toEqual([]);
  });
});

describe('resolveAutoScan', () => {
  const scans = [{ id: 'a' }, { id: 'b' }, { id: 'c' }];
  const box = (scanId: string | null) => ({ id: 'x', scanId, dir: 'desc' as const, metric: 'chgDay' as const, limit: 10 });
  it('prefers the explicit choice, then the first box scan, then the first scan', () => {
    expect(resolveAutoScan({ on: true, scanId: 'c', metric: 'chgDay' }, [box('b')], scans)).toBe('c');
    expect(resolveAutoScan({ on: true, scanId: null, metric: 'chgDay' }, [box(null), box('b')], scans)).toBe('b');
    expect(resolveAutoScan({ on: true, scanId: 'gone', metric: 'chgDay' }, [], scans)).toBe('a');
    expect(resolveAutoScan(undefined, [], [])).toBeNull();
  });
});

describe('autoStep', () => {
  it('initial fill puts the current top N into empty cells in rank order, at once', () => {
    const s = sim({ cells: [null, null, null, null] });
    const changed = s.step(syms(10), { initial: true });
    expect(s.cells).toEqual(['S1', 'S2', 'S3', 'S4']);
    expect(changed).toEqual([0, 1, 2, 3]);
    expect(Object.keys(s.memo.newAt).map(Number)).toEqual([0, 1, 2, 3]);
  });

  it('a non-initial fill still needs the entry dwell', () => {
    const s = sim({ cells: [null, null] });
    expect(s.step(syms(5))).toEqual([]);
    expect(s.cells).toEqual([null, null]);
    s.advance(AUTO_ENTRY_MS - 1);
    expect(s.step(syms(5))).toEqual([]);
    s.advance(1);
    expect(s.step(syms(5))).toEqual([0, 1]);
    expect(s.cells).toEqual(['S1', 'S2']);
  });

  it('filling an empty cell is not rate limited by the swap gap', () => {
    const s = sim({ cells: [null, null, null] });
    s.step(syms(5));
    s.advance(AUTO_ENTRY_MS);
    expect(s.step(syms(5))).toEqual([0, 1, 2]); // three fills in one step
  });

  it('a challenger needs 30 s of CONTINUOUS top-N; a dip resets the clock', () => {
    const s = sim({ cells: ['A', 'B'] });
    const top = ['A', 'B', 'X']; // N = 2: X is rank 3, not top N
    s.step(top);
    // X enters the top 2 (displacing B to rank 3 - still within N+3, so B stays).
    const rise = ['A', 'X', 'B'];
    s.advance(1000);
    s.step(rise);
    s.advance(AUTO_ENTRY_MS - 2000);
    s.step(top); // dips out of the top N: clock resets
    s.advance(1000);
    s.step(rise);
    expect(s.memo.firstInTop.X).toBe(s.t);
    s.advance(AUTO_ENTRY_MS - 1);
    s.step(rise);
    expect(s.memo.firstInTop.X).toBeLessThan(s.t); // clock is running, not yet 30 s
  });

  it('an incumbent ranked N+1..N+3 is never replaced, however long', () => {
    const s = sim({ cells: ['A', 'B'] });
    // N = 2; B sits at rank 5 (index 4 = N+3 -> 5th place is N+3 => still safe is index < N+3 = 5)
    const ranking = ['X', 'Y', 'A', 'Z', 'B']; // A rank 3, B rank 5 = N+3
    for (let i = 0; i < 20; i++) {
      s.step(ranking);
      s.advance(10_000);
    }
    expect(s.cells).toEqual(['A', 'B']);
    expect(s.memo.outSince.B).toBeUndefined();
  });

  it('worse than N+3 for 60 s makes an incumbent replaceable - earlier does not', () => {
    const s = sim({ cells: ['A', 'B'] });
    const ranking = ['X', 'Y', 'C', 'D', 'E', 'A', 'B']; // N=2: A rank 6, B rank 7 -> out (>= N+3 index)
    // Hmm: A index 5 >= 5 -> out. X,Y are in the top 2 and will qualify at 30 s.
    s.step(ranking);
    s.advance(AUTO_ENTRY_MS);
    expect(s.step(ranking)).toEqual([]); // challengers qualified, incumbents not yet 60 s out
    s.advance(AUTO_EXIT_MS - AUTO_ENTRY_MS - 1);
    expect(s.step(ranking)).toEqual([]);
    s.advance(1);
    const changed = s.step(ranking);
    expect(changed).toEqual([1]); // B is the worst-ranked incumbent
    expect(s.cells).toEqual(['A', 'X']);
  });

  it('at most one replacement per 15 s', () => {
    const s = sim({ cells: ['A', 'B'] });
    const ranking = ['X', 'Y', 'C', 'D', 'E', 'A', 'B'];
    s.step(ranking);
    s.advance(AUTO_EXIT_MS);
    expect(s.step(ranking)).toEqual([1]);
    expect(s.cells).toEqual(['A', 'X']);
    s.advance(AUTO_SWAP_GAP_MS - 1);
    expect(s.step(ranking)).toEqual([]);
    s.advance(1);
    expect(s.step(ranking)).toEqual([0]);
    expect(s.cells).toEqual(['Y', 'X']);
  });

  it('an incumbent absent from the ranking counts as worst', () => {
    const s = sim({ cells: ['A', 'B'] });
    const ranking = ['X', 'Y', 'C', 'D', 'E', 'A']; // B absent, A rank 6 (out)
    s.step(ranking);
    s.advance(AUTO_EXIT_MS);
    expect(s.step(ranking)).toEqual([1]); // absent B goes first
  });

  it('held: no change, clocks keep running, swaps wait for release', () => {
    const s = sim({ cells: ['A', 'B'] });
    const ranking = ['X', 'Y', 'C', 'D', 'E', 'A', 'B'];
    s.step(ranking, { held: true });
    s.advance(AUTO_EXIT_MS + 5000);
    expect(s.step(ranking, { held: true })).toEqual([]);
    expect(s.cells).toEqual(['A', 'B']);
    expect(s.step(ranking)).toEqual([1]); // released: earned places apply at once
    expect(s.cells).toEqual(['A', 'X']);
  });

  it('held does not block the initial fill from happening later', () => {
    const s = sim({ cells: [null, null] });
    expect(s.step(syms(5), { held: true, initial: true })).toEqual([]);
    expect(s.cells).toEqual([null, null]);
    expect(s.step(syms(5), { initial: true })).toEqual([0, 1]);
  });

  it('pinned cells are untouched and their symbols are never duplicated', () => {
    const s = sim({ cells: ['P', null, null], pinned: [true, false, false] });
    // P is also ranked first, but is on a pinned chart: skipped, and takes no rank.
    const ranking = ['P', 'S1', 'S2', 'S3'];
    s.step(ranking, { initial: true });
    expect(s.cells).toEqual(['P', 'S1', 'S2']);
    // Even far out of the ranking for ages, the pinned chart stays.
    const worse = ['S1', 'S2', 'T1', 'T2', 'T3', 'T4', 'T5'];
    s.advance(AUTO_EXIT_MS * 3);
    s.step(worse);
    s.advance(AUTO_EXIT_MS * 3);
    s.step(worse);
    expect(s.cells[0]).toBe('P');
    expect(s.cells.filter((c) => c === 'P')).toHaveLength(1);
  });

  it('never places a symbol in two cells', () => {
    const s = sim({ cells: ['S1', null, null] });
    s.step(syms(6), { initial: true });
    expect(new Set(s.cells).size).toBe(3);
    expect(s.cells[0]).toBe('S1');
  });

  it('never reorders: a symbol keeps its cell while it stays', () => {
    const s = sim({ cells: ['A', 'B', 'C'] });
    const shuffles = [
      ['C', 'B', 'A'],
      ['B', 'A', 'C', 'X'],
      ['A', 'C', 'B'],
      ['X', 'C', 'A', 'B'],
    ];
    for (const r of shuffles) {
      s.step(r);
      s.advance(40_000);
      s.step(r);
      s.advance(40_000);
    }
    expect(s.cells).toEqual(['A', 'B', 'C']);
  });

  it('a ranking shorter than the grid leaves the other cells as they are', () => {
    const s = sim({ cells: ['A', 'B', null, null] });
    s.step(['A'], { initial: true });
    s.advance(AUTO_EXIT_MS * 2);
    s.step(['A']);
    expect(s.cells).toEqual(['A', 'B', null, null]);
  });

  it('an empty ranking changes nothing and resets the clocks', () => {
    const s = sim({ cells: ['A', 'B'] });
    const out = ['X', 'Y', 'C', 'D', 'E'];
    s.step(out);
    s.advance(AUTO_EXIT_MS - 1000);
    s.step([]); // scan momentarily gone (reconnect)
    expect(s.memo.outSince).toEqual({});
    s.advance(2000);
    expect(s.step(out)).toEqual([]); // clocks restarted: nothing is 60 s old
    expect(s.cells).toEqual(['A', 'B']);
  });

  it('NEW stamps expire and are dropped for pinned cells', () => {
    const s = sim({ cells: [null, null] });
    s.step(syms(4), { initial: true });
    expect(Object.keys(s.memo.newAt)).toHaveLength(2);
    s.advance(AUTO_NEW_MS);
    s.step(syms(4));
    expect(s.memo.newAt).toEqual({});
  });
});
