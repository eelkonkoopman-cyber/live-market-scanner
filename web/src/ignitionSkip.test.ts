import { describe, expect, it } from 'vitest';
import { freshIgnitionUtterance, isSkippedIgnition, splitFreshNote } from './ignitionFresh';
import { FIELD_BY_KEY } from './fields';

describe('skipped ignition', () => {
  it('a skip note marks the history row dimmed, keeping the reason in the note', () => {
    const note = 'skip: up 7.2% on the day; +5.0% candle on 15.0x volume';
    expect(isSkippedIgnition('ignition', note)).toBe(true);
    expect(splitFreshNote('ignition', note)).toEqual({ fresh: false, note });
    expect(isSkippedIgnition('ignition', 'fresh; +5.0% candle')).toBe(false);
    expect(isSkippedIgnition('ignition', '+5.0% candle')).toBe(false);
    expect(isSkippedIgnition('dildo', 'skip: x')).toBe(false);
  });
  it('passing fresh ignitions still say "fresh ignition"', () => {
    expect(freshIgnitionUtterance('NCPL: ignition', 'ignition', 'NCPL', { ignFresh: 1, ignPass: 1 })).toBe('NCPL: fresh ignition');
  });
  it('ignPass is a filterable field', () => {
    expect(FIELD_BY_KEY.ignPass?.group).toBe('ignition');
  });
});
