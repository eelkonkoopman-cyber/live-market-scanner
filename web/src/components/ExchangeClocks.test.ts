import { describe, it, expect } from 'vitest';
import { zonedTime, hhmmss, sessionTag, handAngles } from './ExchangeClocks';

describe('ExchangeClocks', () => {
  it('formats NY and London around the US DST start (2026-03-08)', () => {
    const before = Date.UTC(2026, 2, 8, 6, 59, 59); // 01:59:59 EST
    const after = Date.UTC(2026, 2, 8, 7, 0, 0); // 03:00:00 EDT
    expect(hhmmss(zonedTime('America/New_York', before))).toBe('01:59:59');
    expect(zonedTime('America/New_York', before).abbr).toBe('EST');
    expect(hhmmss(zonedTime('America/New_York', after))).toBe('03:00:00');
    expect(zonedTime('America/New_York', after).abbr).toBe('EDT');
    expect(hhmmss(zonedTime('Europe/London', after))).toBe('07:00:00');
    expect(zonedTime('Europe/London', after).abbr).toBe('GMT');
  });
  it('London BST after 2026-03-29 01:00 UTC', () => {
    const t = zonedTime('Europe/London', Date.UTC(2026, 2, 29, 1, 0, 0));
    expect(hhmmss(t)).toBe('02:00:00');
    expect(t.abbr).toBe('BST');
  });
  it('session tags', () => {
    const z = (weekday: number, h: number, m: number) => ({ h, m, s: 0, weekday, abbr: 'EDT' });
    expect(sessionTag(z(3, 3, 59))).toBe('CLOSED');
    expect(sessionTag(z(3, 4, 0))).toBe('PRE');
    expect(sessionTag(z(3, 9, 30))).toBe('MARKET OPEN');
    expect(sessionTag(z(3, 16, 0))).toBe('AFTER');
    expect(sessionTag(z(3, 20, 0))).toBe('CLOSED');
    expect(sessionTag(z(6, 10, 0))).toBe('CLOSED');
    expect(sessionTag(zonedTime('America/New_York', Date.UTC(2026, 9, 22, 14, 0)))).toBe('MARKET OPEN');
  });
  it('hand angles', () => {
    expect(handAngles({ h: 15, m: 30, s: 0, weekday: 1, abbr: '' })).toEqual({ hour: 105, minute: 180, second: 0 });
    expect(handAngles({ h: 0, m: 0, s: 45, weekday: 1, abbr: '' }).second).toBe(270);
  });
});
