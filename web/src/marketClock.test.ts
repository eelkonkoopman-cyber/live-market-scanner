// Pin down the market-clock math against the actual Intl ET conversion (no
// mocking Date/Intl) so a DST-boundary or weekend-rollover regression shows
// up here instead of silently mis-wording the status-bar clock pill.
import { describe, expect, it } from 'vitest';
import { countdown, etDayKey, etParts, nextBoundary } from './marketClock';

describe('countdown', () => {
  it('renders hours and minutes at an hour or more', () => {
    expect(countdown(5 * 3600_000 + 18 * 60_000)).toBe('5h 18m');
  });

  it('renders minutes only under an hour', () => {
    expect(countdown(12 * 60_000)).toBe('12m');
  });

  it('renders seconds only under a minute', () => {
    expect(countdown(45_000)).toBe('45s');
  });

  it('never goes negative', () => {
    expect(countdown(-5_000)).toBe('0s');
  });
});

describe('etParts', () => {
  it('converts a UTC instant to ET wall-clock fields', () => {
    // Monday 2026-09-14 14:00 UTC = 10:00 EDT.
    expect(etParts(Date.UTC(2026, 8, 14, 14, 0, 0))).toEqual({
      hh: 10,
      mm: 0,
      ss: 0,
      weekday: 1,
      minutes: 600,
    });
  });
});

describe('nextBoundary', () => {
  it('finds the same-day close boundary', () => {
    // Monday 2026-09-14 10:00 EDT -> next boundary is 16:00 EDT close.
    const b = nextBoundary(Date.UTC(2026, 8, 14, 14, 0, 0));
    expect(b.kind).toBe('close');
    expect(b.atMs).toBe(Date.UTC(2026, 8, 14, 20, 0, 0));
  });

  it('rolls a weekend boundary forward to Monday pre-market', () => {
    // Saturday 2026-09-12 11:00 EDT -> next boundary is Monday 04:00 EDT.
    const b = nextBoundary(Date.UTC(2026, 8, 12, 15, 0, 0));
    expect(b.kind).toBe('pre');
    expect(b.atMs).toBe(Date.UTC(2026, 8, 14, 8, 0, 0));
  });
});

describe('etDayKey', () => {
  it('gives the same key either side of ET midnight on the same calendar day, and a different one across the ET day boundary', () => {
    // 03:59 EDT and 04:01 EDT, both 2026-09-14 UTC-and-ET (EDT is UTC-4), are
    // the same ET trading date — this is the boundary the signal engine's
    // per-day grouping actually cares about (pre-market open at 04:00 ET),
    // not midnight.
    const before = Date.UTC(2026, 8, 14, 7, 59, 0); // 03:59 EDT
    const after = Date.UTC(2026, 8, 14, 8, 1, 0); // 04:01 EDT
    expect(etDayKey(before)).toBe(etDayKey(after));
    expect(etDayKey(before)).toBe('2026-09-14');

    // 23:30 ET one night vs 00:30 ET the next calendar day — genuinely
    // different ET trading dates.
    const lateNight = Date.UTC(2026, 8, 14, 3, 30, 0); // 2026-09-13 23:30 EDT
    const earlyNext = Date.UTC(2026, 8, 14, 4, 30, 0); // 2026-09-14 00:30 EDT
    expect(etDayKey(lateNight)).not.toBe(etDayKey(earlyNext));
    expect(etDayKey(lateNight)).toBe('2026-09-13');
    expect(etDayKey(earlyNext)).toBe('2026-09-14');
  });

  it('maps a UTC-midnight-straddling instant to the ET date, not the UTC date', () => {
    // 2026-09-14 23:00 EDT is 2026-09-15 03:00 UTC — past UTC midnight, but
    // still the 14th in America/New_York. A UTC-keyed implementation would
    // wrongly answer '2026-09-15' here.
    const ms = Date.UTC(2026, 8, 15, 3, 0, 0);
    expect(etDayKey(ms)).toBe('2026-09-14');
  });
});
