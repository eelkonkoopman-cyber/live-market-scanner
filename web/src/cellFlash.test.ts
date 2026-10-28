// The live grid's cell flash (cellFlash.ts, used by LiveGrid's GridRow). The
// ages the server re-publishes every second must never flash: the seeded
// "9EMA Slam" scan shows e9SlamAge, and a green 'up' pulse once a second on a
// bearish alert's row would drown the e9SlamPct / e9Dist changes beside it.
import { describe, expect, it } from 'vitest';
import { cellFlash, NO_FLASH_COLS } from './cellFlash';

describe('cellFlash', () => {
  it('a changed number flashes up or down; an unchanged or non-numeric one does not', () => {
    expect(cellFlash('last', 4.21, 4.2)).toBe('flash-up');
    expect(cellFlash('last', 4.19, 4.2)).toBe('flash-down');
    expect(cellFlash('last', 4.2, 4.2)).toBe('');
    expect(cellFlash('last', 4.2, undefined)).toBe('');
    expect(cellFlash('news', true, false)).toBe('');
    expect(cellFlash('e9State', 3, 2)).toBe('flash-up'); // a stage change IS the event to catch the eye
  });

  it('the per-second ages never flash: e9Age and the slam scan\'s e9SlamAge', () => {
    for (const col of ['e9Age', 'e9SlamAge']) {
      expect(NO_FLASH_COLS.has(col), col).toBe(true);
      expect(cellFlash(col, 96, 95), col).toBe('');
      expect(cellFlash(col, 0, 300), col).toBe(''); // a new slam / stage restarts the age: still not a move
    }
  });

  it('the dildo and climax scans\' per-second ages never flash either (their stage-like fields still do)', () => {
    for (const col of ['dildoAge', 'dildoPbAge', 'dildoTouchAge', 'climaxAge', 'climaxConfAge']) {
      expect(NO_FLASH_COLS.has(col), col).toBe(true);
      expect(cellFlash(col, 64, 63), col).toBe('');
      expect(cellFlash(col, 3, 239), col).toBe(''); // a newer event restarts the age: still not a move
    }
    // a state change and the event's own numbers are real news
    expect(cellFlash('dildoPbState', 3, 2)).toBe('flash-up');
    expect(cellFlash('climaxState', 2, 1)).toBe('flash-up');
    expect(cellFlash('dildoBody', 12.4, 9.1)).toBe('flash-up');
    expect(cellFlash('climaxConfPx', 1.88, 1.9)).toBe('flash-down');
  });

  // web-ux-5: the two state columns are verdicts, not stages - a higher number
  // is not "further along" (climaxState 3 = failed, 4 = expired; dildoPbState
  // 4 = cancelled, 5 = expired), so they flash by what the new state MEANS:
  // green for a confirmation (and the pullback's touch), red for a failure or
  // a cancel, nothing for an expiry or a new climax / dildo re-arming.
  it('the climax and dildo pullback states flash by verdict, not by number', () => {
    // climaxState: 1 watching, 2 confirmed, 3 failed, 4 expired
    expect(cellFlash('climaxState', 2, 1)).toBe('flash-up'); // bottom confirmed
    expect(cellFlash('climaxState', 3, 1)).toBe('flash-down'); // failed: a new low
    expect(cellFlash('climaxState', 4, 1)).toBe(''); // expired: no verdict
    expect(cellFlash('climaxState', 1, 3)).toBe(''); // a new climax re-arms: not a move
    expect(cellFlash('climaxState', 1, 2)).toBe('');
    expect(cellFlash('climaxState', 1, 4)).toBe('');
    // dildoPbState: 1 watching, 2 touching, 3 confirmed, 4 cancelled, 5 expired
    expect(cellFlash('dildoPbState', 2, 1)).toBe('flash-up'); // touching the 9 EMA
    expect(cellFlash('dildoPbState', 3, 2)).toBe('flash-up'); // pullback confirmed
    expect(cellFlash('dildoPbState', 3, 1)).toBe('flash-up');
    expect(cellFlash('dildoPbState', 4, 1)).toBe('flash-down'); // cancelled
    expect(cellFlash('dildoPbState', 4, 2)).toBe('flash-down');
    expect(cellFlash('dildoPbState', 5, 1)).toBe(''); // expired
    expect(cellFlash('dildoPbState', 1, 4)).toBe(''); // a new dildo re-anchors
    expect(cellFlash('dildoPbState', 1, 3)).toBe('');
    expect(cellFlash('dildoPbState', 0, 3)).toBe(''); // no dildo today (a new day)
    // unchanged / non-numeric: nothing, as for every column
    expect(cellFlash('climaxState', 2, 2)).toBe('');
    expect(cellFlash('dildoPbState', 3, undefined)).toBe('');
    // e9State keeps its number order (a stage change, up = further along)
    expect(cellFlash('e9State', 1, 4)).toBe('flash-down');
  });

  it('a Green Dildo row re-sent as its age ticks: only last flashes', () => {
    const a = { last: 0.578, dildoBody: 18.44, dildoVolX: 18.04, dildoDvol: 1_234_567, dildoAge: 63 };
    const b = { ...a, last: 0.581, dildoAge: 64 };
    const flashes = Object.fromEntries(Object.keys(b).map((c) => [c, cellFlash(c, b[c as keyof typeof b], a[c as keyof typeof a])]));
    expect(flashes).toEqual({ last: 'flash-up', dildoBody: '', dildoVolX: '', dildoDvol: '', dildoAge: '' });
  });

  it('a 9EMA Slam row over three re-sends (age ticks, last moves): only the real moves flash', () => {
    // SPAI slammed at 08:15; the row is re-sent each time its age ticks AND each time last / e9Dist move
    const sends = [
      { last: 4.566, e9SlamAge: 95, e9SlamPct: -4.33, e9Dist: -4.33 },
      { last: 4.55, e9SlamAge: 95, e9SlamPct: -4.33, e9Dist: -4.6 },
      { last: 4.55, e9SlamAge: 96, e9SlamPct: -4.33, e9Dist: -4.6 },
    ];
    const flashes = sends.slice(1).map((row, k) => Object.fromEntries(Object.keys(row).map((c) => [c, cellFlash(c, row[c as keyof typeof row], sends[k]![c as keyof typeof row])])));
    expect(flashes).toEqual([
      { last: 'flash-down', e9SlamAge: '', e9SlamPct: '', e9Dist: 'flash-down' },
      { last: '', e9SlamAge: '', e9SlamPct: '', e9Dist: '' },
    ]);
  });
});
