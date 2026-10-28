// mergeHeldOrder is the one piece of the hover-freeze that touches no React
// state, so it gets pinned down directly: frozen symbols must never move,
// and live-only arrivals must always land after them, never interleaved.
import { describe, expect, it } from 'vitest';
import { mergeHeldOrder } from './gridHold';

describe('mergeHeldOrder', () => {
  it('keeps frozen symbols in place and appends a live-only arrival at the end', () => {
    expect(mergeHeldOrder(['A', 'B', 'C'], ['C', 'D', 'A'])).toEqual(['A', 'B', 'C', 'D']);
  });

  it('with nothing frozen yet, passes the live order straight through', () => {
    expect(mergeHeldOrder([], ['X', 'Y'])).toEqual(['X', 'Y']);
  });

  it('a symbol present in both frozen and live is a no-op', () => {
    expect(mergeHeldOrder(['A'], ['A'])).toEqual(['A']);
  });

  it('frozen symbols that dropped out of live stay put — nothing shifts', () => {
    expect(mergeHeldOrder(['A', 'B'], [])).toEqual(['A', 'B']);
  });
});
