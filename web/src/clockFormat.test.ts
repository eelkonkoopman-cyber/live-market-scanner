// The cached clock formatters (clockFormat.ts) must print EXACTLY what the
// toLocale*String calls they replaced print — compared here against those
// original calls over midnights, the DST nights of Europe/London and
// America/New_York (2025-2027), zones with half-hour offsets, random instants,
// the machine's own zone, and invalid times.
import { describe, expect, it } from 'vitest';
import { createClockFormats, fmtClock, fmtClockUS, fmtDayMonth } from './clockFormat';

const MIN = 60_000;
const HOUR = 3_600_000;

// The nth (1-based) or last (n = -1) Sunday of a month, as a UTC day number.
function sunday(year: number, month: number, n: number): number {
  if (n > 0) {
    const first = new Date(Date.UTC(year, month, 1)).getUTCDay();
    return 1 + ((7 - first) % 7) + (n - 1) * 7;
  }
  const lastDay = new Date(Date.UTC(year, month + 1, 0));
  return lastDay.getUTCDate() - lastDay.getUTCDay();
}

// Every DST switch instant of London (last Sun Mar/Oct, 01:00 UTC) and New York
// (2nd Sun Mar 07:00 UTC, 1st Sun Nov 06:00 UTC) for 2025-2027.
const SWITCHES: number[] = [];
for (const y of [2025, 2026, 2027]) {
  SWITCHES.push(Date.UTC(y, 2, sunday(y, 2, -1), 1), Date.UTC(y, 9, sunday(y, 9, -1), 1));
  SWITCHES.push(Date.UTC(y, 2, sunday(y, 2, 2), 7), Date.UTC(y, 10, sunday(y, 10, 1), 6));
}

// Dense around every switch: +-120 min in 15-minute steps (odd offsets), plus the exact instant and one ms either side.
const NEAR_SWITCH: number[] = [];
for (const s of SWITCHES) {
  for (let t = s - 120 * MIN + 7 * MIN; t <= s + 120 * MIN; t += 15 * MIN) NEAR_SWITCH.push(t);
  NEAR_SWITCH.push(s - 1, s, s + 1);
}

// One whole day, a few minutes either side of every half hour (+ odd seconds):
// every zone's local midnight falls inside one of these windows.
function aroundHalfHours(start: number): number[] {
  const out: number[] = [];
  for (let t = start; t < start + 24 * HOUR; t += HOUR / 2) {
    for (let d = -3; d <= 3; d++) out.push(t + d * MIN + (d + 4) * 7_000);
  }
  return out;
}
const AROUND_MIDNIGHTS = [...aroundHalfHours(Date.UTC(2026, 0, 14)), ...aroundHalfHours(Date.UTC(2026, 6, 14))];

function randoms(n: number, seed: number): number[] {
  let s = seed >>> 0;
  const out: number[] = [];
  for (let i = 0; i < n; i++) {
    s = (Math.imul(s, 1664525) + 1013904223) >>> 0;
    out.push(Date.UTC(2020, 0, 1) + Math.floor((s / 4294967296) * 10 * 365 * 24) * HOUR + (s % HOUR));
  }
  return out;
}

const ZONES: (string | undefined)[] = [undefined /* the machine's own */, 'Europe/London', 'America/New_York', 'UTC', 'Asia/Kolkata', 'Australia/Lord_Howe'];

describe('cached clock formatters print what toLocale*String printed', () => {
  for (const tz of ZONES) {
    it(`zone ${tz ?? 'machine local'}`, () => {
      const f = createClockFormats(tz);
      const times = [...NEAR_SWITCH, ...AROUND_MIDNIGHTS, ...randoms(60, 7)];
      let midnights = 0;
      for (const ts of times) {
        const d = new Date(ts);
        const gb = d.toLocaleTimeString('en-GB', { hour12: false, timeZone: tz });
        if (gb.startsWith('00:')) midnights++;
        expect(f.hms24(ts)).toBe(gb);
        expect(f.hms24US(ts)).toBe(d.toLocaleTimeString('en-US', { hour12: false, timeZone: tz }));
        expect(f.dayMonth(ts)).toBe(d.toLocaleDateString('en-GB', { day: '2-digit', month: 'short', timeZone: tz }));
      }
      expect(midnights).toBeGreaterThan(8); // the midnight hour really was sampled
    }, 60_000); // the native originals build a formatter per call: slow on a busy machine
  }

  it('the exported instances are the machine-local calls, exactly as the components used them', () => {
    for (const ts of [...NEAR_SWITCH.filter((_, i) => i % 5 === 0), ...randoms(50, 11)]) {
      const d = new Date(ts);
      expect(fmtClock(ts)).toBe(d.toLocaleTimeString('en-GB', { hour12: false }));
      expect(fmtClockUS(ts)).toBe(d.toLocaleTimeString('en-US', { hour12: false }));
      expect(fmtDayMonth(ts)).toBe(d.toLocaleDateString('en-GB', { day: '2-digit', month: 'short' }));
    }
  });

  it('keeps the format the UI shows: 24-hour, zero padded, midnight 00', () => {
    const f = createClockFormats('UTC');
    expect(f.hms24(Date.UTC(2026, 9, 5, 9, 5, 3))).toBe('09:05:03');
    expect(f.hms24(Date.UTC(2026, 9, 5, 0, 0, 0))).toBe('00:00:00');
    expect(f.hms24(Date.UTC(2026, 9, 5, 23, 59, 59))).toBe('23:59:59');
    expect(f.dayMonth(Date.UTC(2026, 9, 5, 12))).toBe('05 Oct');
  });

  it('answers an invalid time like toLocale*String does, instead of throwing', () => {
    for (const bad of [NaN, Infinity, -Infinity, 8.64e15 + 1, undefined as unknown as number]) {
      const d = new Date(bad);
      expect(fmtClock(bad)).toBe(d.toLocaleTimeString('en-GB', { hour12: false }));
      expect(fmtClockUS(bad)).toBe(d.toLocaleTimeString('en-US', { hour12: false }));
      expect(fmtDayMonth(bad)).toBe(d.toLocaleDateString('en-GB', { day: '2-digit', month: 'short' }));
      expect(fmtClock(bad)).toBe('Invalid Date');
    }
  });
});
