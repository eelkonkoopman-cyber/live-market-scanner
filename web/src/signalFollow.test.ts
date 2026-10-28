import { describe, expect, it } from 'vitest';
import { applyFollow, followMatches, normalizeFollow } from './signalFollow';

const cell = (o: Partial<{ sym: string | null; setup: 'flag' | 'pullback'; follow: 'off' | 'dildo' | 'hod' }> = {}) =>
  ({ sym: 'AAA', setup: 'pullback' as const, follow: 'off' as const, ...o });

describe('signalFollow', () => {
  it('normalizes', () => {
    expect(normalizeFollow('hod')).toBe('hod');
    expect(normalizeFollow('x')).toBe('off');
    expect(normalizeFollow(undefined)).toBe('off');
  });
  it('matches scans', () => {
    expect(followMatches('dildo', 'dildo-1m')).toBe(true);
    expect(followMatches('dildo', 'hod-flag')).toBe(false);
    expect(followMatches('hod', 'hod-flag')).toBe(true);
    expect(followMatches('off', 'hod-flag')).toBe(false);
  });
  it('dildo follow switches symbol, keeps mode', () => {
    const r = applyFollow([cell({ follow: 'dildo' }), cell({ follow: 'hod' })], 'dildo-1m', 'BBB');
    expect(r[0]).toMatchObject({ sym: 'BBB', setup: 'pullback' });
    expect(r[1].sym).toBe('AAA');
  });
  it('hod follow switches symbol and sets flag mode', () => {
    const r = applyFollow([cell({ follow: 'dildo' }), cell({ follow: 'hod' })], 'hod-flag', 'CCC');
    expect(r[0].sym).toBe('AAA');
    expect(r[1]).toMatchObject({ sym: 'CCC', setup: 'flag' });
  });
  it('returns same array when nothing changes', () => {
    const cells = [cell({ follow: 'off' }), cell({ follow: 'hod', sym: 'CCC', setup: 'flag' })];
    expect(applyFollow(cells, 'hod-flag', 'CCC')).toBe(cells);
    expect(applyFollow(cells, 'dildo-1m', 'ZZZ')).toBe(cells);
  });
});
