import { describe, expect, it } from 'vitest';
import { earlyDildoUtterance, isEarlyDildo, splitEarlyNote } from './dildoEarly';
import { flagListRows } from './e9';

describe('early green dildo', () => {
  it('says "early green dildo" only for an early Green Dildo alert', () => {
    expect(earlyDildoUtterance('ABCD: HOD dildo', 'dildo-1m', 'ABCD', { dildoEarly: 1 })).toBe('ABCD: early HOD dildo');
    expect(earlyDildoUtterance('ABCD: HOD dildo', 'dildo-1m', 'ABCD', { dildoEarly: 0 })).toBe('ABCD: HOD dildo');
    expect(earlyDildoUtterance('ABCD: HOD dildo', 'dildo-1m', 'ABCD', {})).toBe('ABCD: HOD dildo'); // unknown
    expect(earlyDildoUtterance('ABCD: HOD dildo', 'dildo-1m', 'ABCD', undefined)).toBe('ABCD: HOD dildo');
    expect(earlyDildoUtterance('ABCD: dildo pullback', 'dildo-pb', 'ABCD', { dildoEarly: 1 })).toBe('ABCD: dildo pullback');
  });

  it('reads the row flag', () => {
    expect(isEarlyDildo({ dildoEarly: 1 })).toBe(true);
    expect(isEarlyDildo({ dildoEarly: 0 })).toBe(false);
    expect(isEarlyDildo({ dildoEarly: null })).toBe(false);
  });

  it('splits the history note marker on dildo rows only', () => {
    expect(splitEarlyNote('dildo', 'early; +9.0% body')).toEqual({ early: true, note: '+9.0% body' });
    expect(splitEarlyNote('dildo', '+9.0% body')).toEqual({ early: false, note: '+9.0% body' });
    expect(splitEarlyNote('climax', 'early; x')).toEqual({ early: false, note: 'early; x' });
    expect(splitEarlyNote('dildo', undefined)).toEqual({ early: false, note: undefined });
  });

  it('flags early rows in the Green Dildo list', () => {
    const rows = flagListRows(['A', 'B'], { A: { dildoAge: 5, dildoEarly: 1 }, B: { dildoAge: 5, dildoEarly: 0 } }, new Set(), 'dildo-1m');
    expect(rows.map((r) => r.early)).toEqual([true, undefined]);
  });
});
