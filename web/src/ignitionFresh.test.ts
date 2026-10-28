import { describe, expect, it } from 'vitest';
import { freshIgnitionUtterance, isFreshIgnition, splitFreshNote, FRESH_BADGE } from './ignitionFresh';
import { flagListRows } from './e9';

describe('fresh ignition', () => {
  it('speech', () => {
    expect(freshIgnitionUtterance('NCPL: ignition', 'ignition', 'NCPL', { ignFresh: 1 })).toBe('NCPL: fresh ignition');
    expect(freshIgnitionUtterance('NCPL: ignition', 'ignition', 'NCPL', { ignFresh: 0 })).toBe('NCPL: ignition');
    expect(freshIgnitionUtterance('NCPL: ignition', 'ignition', 'NCPL', {})).toBe('NCPL: ignition');
    expect(freshIgnitionUtterance('NCPL: green dildo', 'dildo-1m', 'NCPL', { ignFresh: 1 })).toBe('NCPL: green dildo');
  });

  it('verdict: fresh / not / unknown', () => {
    expect(isFreshIgnition({ ignFresh: 1 })).toBe(true);
    expect(isFreshIgnition({ ignFresh: 0 })).toBe(false);
    expect(isFreshIgnition({ ignFresh: null })).toBe(false);
  });

  it('history note', () => {
    expect(splitFreshNote('ignition', 'fresh; +5.0% candle')).toEqual({ fresh: true, note: '+5.0% candle' });
    expect(splitFreshNote('ignition', '+5.0% candle')).toEqual({ fresh: false, note: '+5.0% candle' });
    expect(splitFreshNote('dildo', 'fresh; x')).toEqual({ fresh: false, note: 'fresh; x' });
  });

  it('list badge', () => {
    expect(FRESH_BADGE.label).toBe('FRESH');
    const rows = flagListRows(['A', 'B', 'C'], { A: { ignAge: 5, ignFresh: 1 }, B: { ignAge: 5, ignFresh: 0 }, C: { ignAge: 5 } }, new Set(), 'ignition');
    expect(rows.map((r) => r.ignFresh ?? false)).toEqual([true, false, false]);
  });
});
