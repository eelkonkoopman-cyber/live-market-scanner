// 9EMA flag market scan, web side: the e9* registry entries, how each column
// formats, the e9State badge, and what a scan alert says out loud. The engine
// behind the numbers is covered by emaFlag.golden.test.ts and the Go tests;
// this pins only the presentation, in plain node (no DOM, no audio).
import { describe, expect, it } from 'vitest';
import {
  BOTTOM_BADGE,
  CLIMAX_ALERTS_KEY,
  CLIMAX_BADGE,
  CLIMAX_SCAN_IDS,
  DEFAULT_FLAG_SCAN_ID,
  DILDO_ALERTS_KEY,
  DILDO_BADGE,
  DILDO_PB_BADGE,
  DILDO_SCAN_IDS,
  DILDO_TOUCH_BADGE,
  E9_SCAN_IDS,
  E9_SLAM_BADGE,
  extraScanMutes,
  isAlertRuleScanId,
  isClimaxScanId,
  isDildoScanId,
  isSlamScanId,
  alertSpeech,
  alertUtterance,
  e9Badge,
  flagListEmptyText,
  flagListHead,
  flagListMissingText,
  flagListRows,
  flagScanLabel,
  fmtEmaDist,
  isE9ScanId,
  orderFlagScans,
  parseAlertToggle,
  parseFlagListPrefs,
  spokenScanName,
} from './e9';
import { FIELD_BY_KEY, FIELD_DEFS, FIELD_GROUPS, columnableFields, fieldsInGroup, filterableFields } from './fields';
import { formatField } from './format';

const E9_KEYS = ['e9State', 'e9Dist', 'e9Trigger', 'e9Stop', 'e9FlagBars', 'e9Age', 'e9SlamPct', 'e9SlamAge', 'e9HodFlagAge'];

describe('e9 field registry', () => {
  it('registers all nine fields in the e9flag group, in column order (the two 9 EMA SLAM fields last)', () => {
    expect(fieldsInGroup('e9flag').map((d) => d.key)).toEqual(E9_KEYS);
  });

  it('the e9flag group has a menu label, and every field group used is declared', () => {
    expect(FIELD_GROUPS.find((g) => g.id === 'e9flag')?.label).toBe('9EMA flag (market scan)');
    const declared = new Set(FIELD_GROUPS.map((g) => g.id));
    for (const d of FIELD_DEFS) expect(declared.has(d.group), d.key).toBe(true);
  });

  it('e9State is the badge kind; the rest reuse the existing numeric kinds', () => {
    expect(FIELD_BY_KEY.e9State.kind).toBe('e9State');
    expect(FIELD_BY_KEY.e9Dist.kind).toBe('pct');
    expect(FIELD_BY_KEY.e9Trigger.kind).toBe('price');
    expect(FIELD_BY_KEY.e9Stop.kind).toBe('price');
    expect(FIELD_BY_KEY.e9FlagBars.kind).toBe('int');
    expect(FIELD_BY_KEY.e9Age.kind).toBe('dur');
    // the 9 EMA SLAM pair: how far under its EMA the slam bar closed (a signed %), and how long ago it was
    expect(FIELD_BY_KEY.e9SlamPct.kind).toBe('pct');
    expect(FIELD_BY_KEY.e9SlamAge.kind).toBe('dur');
  });

  it('the slam fields explain the signal and the seeded scan in their hints', () => {
    expect(FIELD_BY_KEY.e9SlamAge.hint).toMatch(/slam/i);
    expect(FIELD_BY_KEY.e9SlamAge.hint).toMatch(/300/); // the seeded "9EMA Slam" scan's window
    expect(FIELD_BY_KEY.e9SlamPct.hint).toMatch(/under/i);
    expect(FIELD_BY_KEY.e9SlamPct.unit).toBe('%');
    expect(FIELD_BY_KEY.e9SlamAge.unit).toBe('sec');
  });

  it("the slam % hint states the open rule as the engine applies it: up to slamOpenTolPct 0.5% under the previous bar's EMA", () => {
    const hint = FIELD_BY_KEY.e9SlamPct.hint!;
    expect(hint).not.toMatch(/opens at or above the EMA/i); // the rule tolerates an open a tick under the line
    expect(hint).toMatch(/0\.5%/);
    expect(hint).toMatch(/previous (bar|candle)/i);
  });

  it('has short headers (<= 12 chars) and a hint on every field', () => {
    for (const k of E9_KEYS) {
      const d = FIELD_BY_KEY[k];
      expect(d.label.length, k).toBeLessThanOrEqual(12);
      expect(d.hint, k).toBeTruthy();
    }
  });

  it('every field is both a filter/sort choice and a column choice', () => {
    const filters = new Set(filterableFields().map((d) => d.key));
    const columns = new Set(columnableFields().map((d) => d.key));
    for (const k of E9_KEYS) {
      expect(filters.has(k), `${k} filter`).toBe(true);
      expect(columns.has(k), `${k} column`).toBe(true);
    }
  });
});

describe('e9 cell formatting', () => {
  it('distance is a signed percent, toned by sign', () => {
    expect(formatField('e9Dist', 3.456)).toEqual({ text: '+3.46%', tone: 'pos' });
    expect(formatField('e9Dist', -0.5)).toEqual({ text: '-0.50%', tone: 'neg' });
  });

  it('trigger and stop are prices (4 decimals under $1, 2 above)', () => {
    expect(formatField('e9Trigger', 0.5917).text).toBe('0.5917');
    expect(formatField('e9Stop', 12.3).text).toBe('12.30');
  });

  it('flag length is a plain count', () => {
    expect(formatField('e9FlagBars', 7).text).toBe('7');
  });

  it('age is a duration, like the other age columns', () => {
    expect(formatField('e9Age', 45).text).toBe('45s');
    expect(formatField('e9Age', 300).text).toBe('5m');
    expect(formatField('e9Age', 5400).text).toBe('1.5h');
  });

  it('the slam % is a signed percent (negative: closed under the EMA), the slam age a duration', () => {
    expect(formatField('e9SlamPct', -4.3)).toEqual({ text: '-4.30%', tone: 'neg' });
    expect(formatField('e9SlamAge', 45)).toEqual({ text: '45s', tone: '' });
    expect(formatField('e9SlamAge', 120).text).toBe('2m');
  });

  it('a stock outside the candidate set (Null fields) shows a dash everywhere', () => {
    for (const k of E9_KEYS) expect(formatField(k, null).text, k).toBe('–');
  });
});

describe('e9Badge', () => {
  it('maps 0..4 to idle / BREAKOUT / FLAT / READY / LONG', () => {
    expect([0, 1, 2, 3, 4].map((s) => e9Badge(s)?.label)).toEqual(['IDLE', 'BREAKOUT', 'FLAT', 'READY', 'LONG']);
  });

  it('carries the modifier class the stylesheet colours by', () => {
    expect([0, 1, 2, 3, 4].map((s) => e9Badge(s)?.cls)).toEqual(['idle', 'breakout', 'flat', 'ready', 'long']);
  });

  it('every badge explains itself in a tooltip', () => {
    for (const s of [0, 1, 2, 3, 4]) expect(e9Badge(s)?.title.length ?? 0).toBeGreaterThan(20);
  });

  it('no badge for a missing or malformed value (rendered as a dash)', () => {
    for (const v of [null, undefined, '3', NaN, Infinity, 2.5, -1, 5, true]) expect(e9Badge(v), String(v)).toBeNull();
  });
});

describe('spokenScanName', () => {
  it('spells out the three seeded 9EMA scans', () => {
    expect(spokenScanName('9EMA Flag — sideways')).toBe('nine E M A flag, sideways');
    expect(spokenScanName('9EMA Flag — ready')).toBe('nine E M A flag, ready');
    expect(spokenScanName('9EMA Flag — breakout')).toBe('nine E M A flag, breakout');
  });

  it('tolerates dash style, case and spacing of a renamed copy', () => {
    expect(spokenScanName('9ema flag – ready')).toBe('nine E M A flag, ready');
    expect(spokenScanName('9EMA Flag - ready')).toBe('nine E M A flag, ready');
    expect(spokenScanName('  9EMA  Flag —  ready  ')).toBe('nine E M A flag, ready');
  });

  it('spells out the seeded 9EMA Slam scan (and a renamed copy of it)', () => {
    expect(spokenScanName('9EMA Slam')).toBe('nine E M A slam');
    expect(spokenScanName('9ema slam')).toBe('nine E M A slam');
    expect(spokenScanName('  9 EMA  SLAM ')).toBe('nine E M A slam');
  });

  it('says the seeded HOD Flag scan as "HOD flag"', () => {
    expect(spokenScanName('HOD Flag')).toBe('HOD flag');
    expect(spokenScanName(' hod  flag ')).toBe('HOD flag');
  });

  it('any other scan is simply its own name', () => {
    expect(spokenScanName('Gappers')).toBe('Gappers');
    expect(spokenScanName('  Low float movers ')).toBe('Low float movers');
    expect(spokenScanName('9EMA Flag')).toBe('9EMA Flag'); // no stage word: not one of the seeded names
    expect(spokenScanName('9EMA Slam watch')).toBe('9EMA Slam watch'); // not the seeded name either
  });
});

describe('alertSpeech', () => {
  it('names the symbol first', () => {
    expect(alertSpeech('BEAT', '9EMA Flag — ready')).toBe('BEAT: nine E M A flag, ready');
    expect(alertSpeech('QTEX', 'Gappers')).toBe('QTEX: Gappers');
    expect(alertSpeech('SPAI', '9EMA Slam')).toBe('SPAI: nine E M A slam');
  });

  it('says just the symbol when the scan has no name', () => {
    expect(alertSpeech('BEAT', '  ')).toBe('BEAT');
  });
});

describe('alertUtterance', () => {
  const scans = [
    { id: 's-sideways', name: '9EMA Flag — sideways', alert: { speak: true } },
    { id: 's-quiet', name: 'Gappers', alert: { speak: false } },
    { id: 's-old', name: 'Old scan', alert: {} }, // saved before the option existed
    { id: 's-none', name: 'No alert block', alert: null },
  ];

  it('speaks for a scan that opted in', () => {
    expect(alertUtterance(scans, 's-sideways', 'APUS')).toBe('APUS: nine E M A flag, sideways');
  });

  it('the seeded 9EMA Slam scan (Speak on) says the same line the Signal window says for the slam event', () => {
    const seeded = [...scans, { id: 'e9flag-slam', name: '9EMA Slam', alert: { speak: true } }];
    expect(alertUtterance(seeded, 'e9flag-slam', 'SPAI')).toBe('SPAI: nine E M A slam');
  });

  it('stays silent when the scan did not ask for speech', () => {
    expect(alertUtterance(scans, 's-quiet', 'APUS')).toBeNull();
    expect(alertUtterance(scans, 's-old', 'APUS')).toBeNull();
    expect(alertUtterance(scans, 's-none', 'APUS')).toBeNull();
  });

  it('stays silent for an unknown scan or an empty symbol', () => {
    expect(alertUtterance(scans, 'deleted', 'APUS')).toBeNull();
    expect(alertUtterance([], 's-sideways', 'APUS')).toBeNull();
    expect(alertUtterance(scans, 's-sideways', '')).toBeNull();
  });
});

// --- the Signal window's left-bar list (FlagScanList.tsx) ---

describe('parseFlagListPrefs', () => {
  const def = { scanId: 'e9flag-sideways' };

  it('defaults to the sideways scan (FLAT, READY and LONG together)', () => {
    expect(DEFAULT_FLAG_SCAN_ID).toBe('e9flag-sideways');
    expect(parseFlagListPrefs(null)).toEqual(def);
    expect(parseFlagListPrefs(undefined)).toEqual(def);
    expect(parseFlagListPrefs('')).toEqual(def);
  });

  it('reads a saved choice, seeded scan or any other', () => {
    expect(parseFlagListPrefs('{"scanId":"e9flag-ready"}')).toEqual({ scanId: 'e9flag-ready' });
    expect(parseFlagListPrefs('{"scanId":"scan-ab12","extra":1}')).toEqual({ scanId: 'scan-ab12' });
    expect(parseFlagListPrefs('{"scanId":"  e9flag-breakout "}')).toEqual({ scanId: 'e9flag-breakout' });
  });

  it('survives garbage: not JSON, wrong shapes, wrong types, empty or absurd ids', () => {
    for (const raw of [
      'not json',
      '{"scanId":',
      'null',
      '42',
      '"e9flag-ready"',
      '[]',
      '["e9flag-ready"]',
      '{}',
      '{"scanId":5}',
      '{"scanId":null}',
      '{"scanId":""}',
      '{"scanId":"   "}',
      '{"scanId":{"x":1}}',
      `{"scanId":"${'x'.repeat(101)}"}`,
    ]) {
      expect(parseFlagListPrefs(raw), raw).toEqual(def);
    }
  });
});

describe('orderFlagScans / flagScanLabel', () => {
  const scan = (id: string, name = id) => ({ id, name });

  it('puts the seeded scans first, in stage order, then the rest as listed', () => {
    const scans = [scan('gappers'), scan('e9flag-breakout'), scan('mine'), scan('e9flag-sideways'), scan('e9flag-ready')];
    expect(orderFlagScans(scans).map((s) => s.id)).toEqual([
      'e9flag-sideways',
      'e9flag-ready',
      'e9flag-breakout',
      'gappers',
      'mine',
    ]);
  });

  it('the 9EMA Slam scan is a seeded flag scan too, listed after the three stage scans', () => {
    const scans = [scan('e9flag-slam'), scan('gappers'), scan('e9flag-breakout'), scan('e9flag-sideways'), scan('e9flag-ready')];
    expect(orderFlagScans(scans).map((s) => s.id)).toEqual(['e9flag-sideways', 'e9flag-ready', 'e9flag-breakout', 'e9flag-slam', 'gappers']);
    expect(orderFlagScans([scan('mine'), scan('e9flag-slam')]).map((s) => s.id)).toEqual(['e9flag-slam', 'mine']);
  });

  it('lists only the seeded scans that exist and does not mutate its input', () => {
    const scans = [scan('gappers'), scan('e9flag-ready')];
    const before = scans.map((s) => s.id);
    expect(orderFlagScans(scans).map((s) => s.id)).toEqual(['e9flag-ready', 'gappers']);
    expect(scans.map((s) => s.id)).toEqual(before);
    expect(orderFlagScans([])).toEqual([]);
  });

  it('knows the four seeded ids (the default stays the sideways scan)', () => {
    expect([...E9_SCAN_IDS]).toEqual(['e9flag-sideways', 'e9flag-ready', 'e9flag-breakout', 'e9flag-slam']);
    expect(E9_SCAN_IDS.every(isE9ScanId)).toBe(true);
    expect(isE9ScanId('gappers')).toBe(false);
    expect(DEFAULT_FLAG_SCAN_ID).toBe('e9flag-sideways');
    expect(isSlamScanId('e9flag-slam')).toBe(true);
    expect(isSlamScanId('e9flag-sideways')).toBe(false);
  });

  it('shortens the seeded names to the stage word, leaves other names alone', () => {
    expect(flagScanLabel('9EMA Flag — sideways')).toBe('sideways');
    expect(flagScanLabel('9ema flag - ready')).toBe('ready');
    expect(flagScanLabel('9EMA Slam')).toBe('slam');
    expect(flagScanLabel('9ema SLAM')).toBe('slam');
    expect(flagScanLabel('Gappers')).toBe('Gappers');
    expect(flagScanLabel('9EMA Flag')).toBe('9EMA Flag');
  });
});

describe('fmtEmaDist', () => {
  it('is a signed percent with one decimal', () => {
    expect(fmtEmaDist(3.456)).toBe('+3.5%');
    expect(fmtEmaDist(-0.52)).toBe('-0.5%');
    expect(fmtEmaDist(12)).toBe('+12.0%');
  });

  it('never prints a negative zero', () => {
    expect(fmtEmaDist(0)).toBe('+0.0%');
    expect(fmtEmaDist(-0.04)).toBe('+0.0%');
    expect(fmtEmaDist(0.04)).toBe('+0.0%');
  });

  it('is a dash for anything that is not a finite number', () => {
    for (const v of [null, undefined, '3', NaN, Infinity, -Infinity, true, {}]) expect(fmtEmaDist(v), String(v)).toBe('–');
  });
});

describe('flagListRows', () => {
  const rows = {
    AAA: { e9State: 3, e9Dist: 1.234, e9Trigger: 12.3456, e9FlagBars: 9, e9Age: 300, pmVolX: 3.05 },
    BBB: { e9State: 2, e9Dist: -0.6, e9Trigger: 0.5917, e9FlagBars: 4, e9Age: 45, pmVolX: 142.6 },
    CCC: { e9State: 4, e9Dist: 0.02, e9Trigger: 7, e9FlagBars: 12, e9Age: 5400, pmVolX: 12.34 },
  };

  it("keeps the scan's own order (it is the scan's sort, not ours)", () => {
    expect(flagListRows(['CCC', 'AAA', 'BBB'], rows, new Set()).map((r) => r.sym)).toEqual(['CCC', 'AAA', 'BBB']);
    expect(flagListRows(['BBB', 'AAA'], rows, new Set()).map((r) => r.sym)).toEqual(['BBB', 'AAA']);
    expect(flagListRows([], rows, new Set())).toEqual([]);
  });

  it("formats each column the way the grid does (badge, signed 1-dp percent, the grid's duration)", () => {
    const [a, b, c] = flagListRows(['AAA', 'BBB', 'CCC'], rows, new Set());
    expect([a.badge?.label, b.badge?.label, c.badge?.label]).toEqual(['READY', 'FLAT', 'LONG']);
    expect([a.badge?.cls, b.badge?.cls, c.badge?.cls]).toEqual(['ready', 'flat', 'long']);
    expect([a.dist, b.dist, c.dist]).toEqual(['+1.2%', '-0.6%', '+0.0%']);
    expect([a.age, b.age, c.age]).toEqual(['5m', '45s', '1.5h']);
    // the age text is the grid's formatter, not a second copy of it
    for (const [r, v] of [
      [a, 300],
      [b, 45],
      [c, 5400],
    ] as const) {
      expect(r.age).toBe(formatField('e9Age', v).text);
    }
  });

  it('explains the trade in the row title: trigger, flag length, pre-market multiple', () => {
    const [a, b, c] = flagListRows(['AAA', 'BBB', 'CCC'], rows, new Set());
    expect(a.title).toBe('long above 12.35 · flag 9 bars · pm vol 3.05×');
    expect(b.title).toBe('long above 0.5917 · flag 4 bars · pm vol 143×');
    expect(c.title).toBe('long above 7.00 · flag 12 bars · pm vol 12.3×');
  });

  it('marks the symbols that are on a chart in this window', () => {
    const out = flagListRows(['AAA', 'BBB', 'CCC'], rows, new Set(['BBB', 'ZZZ']));
    expect(out.map((r) => r.onChart)).toEqual([false, true, false]);
  });

  it('a symbol with no row yet, or a row with no e9 fields, reads as dashes', () => {
    const out = flagListRows(['AAA', 'NEW', 'PLAIN'], { AAA: rows.AAA, PLAIN: { last: 5, chgDay: 1.2 } }, new Set());
    for (const r of out.slice(1)) {
      expect(r.badge).toBeNull();
      expect(r.dist).toBe('–');
      expect(r.age).toBe('–');
      expect(r.title).toBe('long above – · flag – bars · pm vol –');
      expect(r.onChart).toBe(false);
    }
    expect(out.map((r) => r.sym)).toEqual(['AAA', 'NEW', 'PLAIN']);
  });

  // The "9EMA Slam" scan's rows carry e9SlamAge / e9SlamPct / e9Dist but no
  // e9State or e9Age (its columns are last, chgDay, pmVolX, e9SlamPct,
  // e9SlamAge, e9Dist, news): without a slam rule every one of them would read
  // as a dash badge and a dash age.
  describe('slam rows (the 9EMA Slam scan)', () => {
    const slam = { e9SlamAge: 95, e9SlamPct: -4.3, e9Dist: -6.21, pmVolX: 1.6, last: 4.21 };

    it('a row with a slam age is badged SLAM, its age is the time since the slam, its title says how hard it slammed', () => {
      const [r] = flagListRows(['SPAI'], { SPAI: slam }, new Set(['SPAI']));
      expect(r.badge).toEqual(E9_SLAM_BADGE);
      expect(r.badge?.label).toBe('SLAM');
      expect(r.badge?.cls).toBe('slam');
      expect(r.dist).toBe('-6.2%');
      expect(r.age).toBe(formatField('e9SlamAge', 95).text);
      expect(r.age).toBe('1.6m');
      expect(r.title).toBe('slammed 4.3% under the 9 EMA · 1.6m ago · pm vol 1.60×');
      expect(r.onChart).toBe(true);
    });

    it('the SLAM badge explains itself (and says it is an alert, not a trade)', () => {
      expect(E9_SLAM_BADGE.title.length).toBeGreaterThan(20);
      expect(E9_SLAM_BADGE.title).toMatch(/9 EMA/);
      expect(E9_SLAM_BADGE.title).toMatch(/alert/i);
      // it is not one of the e9State stages: no wire value maps to it
      for (const s of [0, 1, 2, 3, 4]) expect(e9Badge(s)?.label).not.toBe('SLAM');
    });

    it('without the slam % the title still reads, with no number in it', () => {
      const [r] = flagListRows(['X'], { X: { e9SlamAge: 30, pmVolX: 2 } }, new Set());
      expect(r.badge?.label).toBe('SLAM');
      expect(r.title).toBe('slammed through the 9 EMA · 30s ago · pm vol 2.00×');
    });

    it('a scan that carries BOTH the stage and the slam: a fresh slam (<= 5 min) wins, an older one gives the stage back', () => {
      const both = (e9SlamAge: number) => flagListRows(['X'], { X: { e9State: 0, e9Age: 10, e9SlamAge, e9SlamPct: -3.5, e9Dist: -4 } }, new Set())[0];
      expect(both(300).badge?.label).toBe('SLAM');
      expect(both(300).age).toBe('5m');
      const old = flagListRows(['X'], { X: { e9State: 3, e9Age: 120, e9SlamAge: 1800, e9SlamPct: -3.5, e9Trigger: 5, e9FlagBars: 4 } }, new Set())[0];
      expect(old.badge?.label).toBe('READY');
      expect(old.age).toBe('2m'); // the stage's own age, not the old slam's
      expect(old.title).toBe('long above 5.00 · flag 4 bars · pm vol –');
    });

    it('a null, negative or non-numeric slam age is no slam at all (the row reads exactly as before)', () => {
      for (const v of [null, undefined, NaN, Infinity, -5, '60']) {
        const [r] = flagListRows(['X'], { X: { e9State: 2, e9Age: 45, e9SlamAge: v, e9SlamPct: -3 } }, new Set());
        expect(r.badge?.label, String(v)).toBe('FLAT');
        expect(r.age).toBe('45s');
      }
      const [plain] = flagListRows(['X'], { X: { e9SlamAge: null, e9SlamPct: null, e9Dist: -2 } }, new Set());
      expect(plain.badge).toBeNull();
      expect(plain.title).toBe('long above – · flag – bars · pm vol –');
      expect(JSON.stringify(plain)).not.toContain('NaN');
    });
  });

  it('null, malformed and non-finite values are dashes, never NaN or a throw', () => {
    const [r] = flagListRows(
      ['X'],
      { X: { e9State: null, e9Dist: NaN, e9Trigger: null, e9FlagBars: Infinity, e9Age: 'soon', pmVolX: undefined } },
      new Set(),
    );
    expect(r).toEqual({
      sym: 'X',
      badge: null,
      dist: '–',
      age: '–',
      title: 'long above – · flag – bars · pm vol –',
      onChart: false,
    });
    expect(JSON.stringify(r)).not.toContain('NaN');
  });
});

// ===========================================================================
// GREEN DILDO -> 9 EMA PULLBACK and VOLUME CLIMAX -> BOTTOM CONFIRMED
// (2026-10-07): two market-wide 1-minute alert rules, each two sequential
// alerts, seeded as five scans (dildo-1m, dildo-pb, dildo-touch, climax-1m,
// climax-confirm). Their web side: the 17 row fields, what each alert says out
// loud, where the scans sit in the Signal window's picker, how their rows read
// in the left-bar list, and which scans the Signal window listens to.

const DILDO_KEYS = ['dildoAge', 'dildoBody', 'dildoVolX', 'dildoDvol', 'dildoPbAge', 'dildoPbPx', 'dildoTouchAge', 'dildoPbState'];
const CLIMAX_KEYS = [
  'climaxAge',
  'climaxDropPct',
  'climaxVolX',
  'climaxWickPct',
  'climaxDvol',
  'climaxLow',
  'climaxState',
  'climaxConfAge',
  'climaxConfPx',
];

describe('dildo and climax field registry', () => {
  it('registers the 8 dildo fields and the 9 climax fields, each signal in its own group, in column order', () => {
    expect(fieldsInGroup('dildo').map((d) => d.key)).toEqual(DILDO_KEYS);
    expect(fieldsInGroup('climax').map((d) => d.key)).toEqual(CLIMAX_KEYS);
    // the 9EMA flag group is untouched (its own test above pins its eight)
    for (const k of [...DILDO_KEYS, ...CLIMAX_KEYS]) expect(fieldsInGroup('e9flag').some((d) => d.key === k), k).toBe(false);
  });

  it('both groups have a menu label naming the signal', () => {
    expect(FIELD_GROUPS.find((g) => g.id === 'dildo')?.label).toMatch(/green dildo/i);
    expect(FIELD_GROUPS.find((g) => g.id === 'climax')?.label).toMatch(/volume climax/i);
  });

  it('ages are durations (sec), prices are prices, multiples are ratios, $ volumes are compact $, states are plain counts', () => {
    for (const k of ['dildoAge', 'dildoPbAge', 'dildoTouchAge', 'climaxAge', 'climaxConfAge']) {
      expect(FIELD_BY_KEY[k].kind, k).toBe('dur');
      expect(FIELD_BY_KEY[k].unit, k).toBe('sec');
    }
    for (const k of ['dildoPbPx', 'climaxLow', 'climaxConfPx']) expect(FIELD_BY_KEY[k].kind, k).toBe('price');
    for (const k of ['dildoVolX', 'climaxVolX']) expect(FIELD_BY_KEY[k].kind, k).toBe('ratio');
    for (const k of ['dildoDvol', 'climaxDvol']) {
      expect(FIELD_BY_KEY[k].kind, k).toBe('compact');
      expect(FIELD_BY_KEY[k].unit, k).toBe('$');
    }
    for (const k of ['dildoPbState', 'climaxState']) expect(FIELD_BY_KEY[k].kind, k).toBe('int');
    // the dildo's body is an up move (green tone); the climax's drop and wick are magnitudes, never toned
    expect(FIELD_BY_KEY.dildoBody.kind).toBe('pct');
    expect(FIELD_BY_KEY.climaxDropPct.kind).toBe('pctAbs');
    expect(FIELD_BY_KEY.climaxWickPct.kind).toBe('pctAbs');
  });

  it('short headers (<= 12 chars), unique, and a hint on every field', () => {
    const labels = new Set(FIELD_DEFS.map((d) => d.label));
    expect(labels.size).toBe(FIELD_DEFS.length); // no two columns share a header
    for (const k of [...DILDO_KEYS, ...CLIMAX_KEYS]) {
      const d = FIELD_BY_KEY[k];
      expect(d, k).toBeTruthy();
      expect(d.label.length, k).toBeLessThanOrEqual(12);
      expect(d.hint, k).toBeTruthy();
    }
  });

  it('every field is both a filter/sort choice and a column choice', () => {
    const filters = new Set(filterableFields().map((d) => d.key));
    const columns = new Set(columnableFields().map((d) => d.key));
    for (const k of [...DILDO_KEYS, ...CLIMAX_KEYS]) {
      expect(filters.has(k), `${k} filter`).toBe(true);
      expect(columns.has(k), `${k} column`).toBe(true);
    }
  });

  it('the state hints spell out every state number the seeded scans filter on', () => {
    const pb = FIELD_BY_KEY.dildoPbState.hint!;
    for (const w of ['1 = watching', '2 = touching', '3 = confirmed', '4 = cancelled', '5 = expired']) expect(pb).toContain(w);
    const cl = FIELD_BY_KEY.climaxState.hint!;
    for (const w of ['1 = watching', '2 = confirmed', '3 = failed', '4 = expired']) expect(cl).toContain(w);
  });

  it('the hints carry the floors, the windows and the "look now" caveat', () => {
    expect(FIELD_BY_KEY.dildoAge.hint).toMatch(/240/); // the seeded Green Dildo scan's window
    expect(FIELD_BY_KEY.dildoDvol.hint).toMatch(/100k/);
    expect(FIELD_BY_KEY.dildoPbAge.hint).toMatch(/look now/i);
    expect(FIELD_BY_KEY.climaxAge.hint).toMatch(/240/);
    expect(FIELD_BY_KEY.climaxDvol.hint).toMatch(/50k/);
    expect(FIELD_BY_KEY.climaxConfAge.hint).toMatch(/look now/i);
    expect(FIELD_BY_KEY.climaxConfAge.hint).toMatch(/1200/); // Decision C: the confirm scan's late-confirmation cut
  });
});

describe('dildo and climax cell formatting', () => {
  it('ages are the grid durations', () => {
    expect(formatField('dildoAge', 63)).toEqual({ text: '1.1m', tone: '' });
    expect(formatField('climaxConfAge', 45).text).toBe('45s');
  });

  it('the dildo body is a green signed percent; the climax drop and wick are unsigned and untoned', () => {
    expect(formatField('dildoBody', 12.4)).toEqual({ text: '+12.40%', tone: 'pos' });
    expect(formatField('climaxDropPct', 13.59)).toEqual({ text: '13.59%', tone: '' });
    expect(formatField('climaxWickPct', 33.7)).toEqual({ text: '33.70%', tone: '' });
  });

  it('multiples, prices, $ volumes and states', () => {
    expect(formatField('dildoVolX', 18.04).text).toBe('18.04x');
    expect(formatField('climaxVolX', 9.6).text).toBe('9.60x');
    expect(formatField('dildoPbPx', 0.57).text).toBe('0.5700');
    expect(formatField('climaxLow', 1.78).text).toBe('1.78');
    expect(formatField('climaxConfPx', 1.879).text).toBe('1.88');
    expect(formatField('dildoDvol', 1_234_567).text).toBe('1.2M');
    expect(formatField('climaxDvol', 716_518).text).toBe('716.5K');
    expect(formatField('dildoPbState', 3).text).toBe('3');
    expect(formatField('climaxState', 2).text).toBe('2');
  });

  it('a stock with no dildo / climax today (Null fields) shows a dash everywhere', () => {
    for (const k of [...DILDO_KEYS, ...CLIMAX_KEYS]) expect(formatField(k, null).text, k).toBe('–');
  });
});

describe('the dildo and climax scans out loud', () => {
  it('spokenScanName: the five seeded names (and renamed copies differing in case and spacing)', () => {
    expect(spokenScanName('Green Dildo')).toBe('HOD dildo');
    expect(spokenScanName('  green   DILDO ')).toBe('HOD dildo');
    expect(spokenScanName('Dildo 9EMA Pullback')).toBe('nine E M A pullback');
    expect(spokenScanName('dildo 9 ema pullback')).toBe('nine E M A pullback');
    expect(spokenScanName('Dildo 9EMA Touch')).toBe('nine E M A touch');
    expect(spokenScanName('Volume Climax')).toBe('volume climax');
    expect(spokenScanName('VOLUME  climax')).toBe('volume climax');
    expect(spokenScanName('Bottom Confirmed')).toBe('bottom confirmed');
    // not the seeded names: said as written
    expect(spokenScanName('Green Dildo 5m')).toBe('Green Dildo 5m');
    expect(spokenScanName('Dildo 9EMA')).toBe('Dildo 9EMA');
    expect(spokenScanName('Climax')).toBe('Climax');
    expect(spokenScanName('PM New High')).toBe('pre-market high');
    expect(spokenScanName('Daily Breakout')).toBe('daily breakout');
    expect(spokenScanName(' pdl  BREAK ')).toBe('PDL break');
    expect(spokenScanName('  daily   BREAKOUT ')).toBe('daily breakout');
    expect(spokenScanName(' pm  new high ')).toBe('pre-market high');
  });

  it('alertUtterance: the two dildo alerts and the two climax alerts, symbol first', () => {
    const seeded = [
      { id: 'dildo-1m', name: 'Green Dildo', alert: { speak: true } },
      { id: 'dildo-pb', name: 'Dildo 9EMA Pullback', alert: { speak: true } },
      { id: 'dildo-touch', name: 'Dildo 9EMA Touch', alert: { speak: false } }, // seeded with Speak off
      { id: 'climax-1m', name: 'Volume Climax', alert: { speak: true } },
      { id: 'climax-confirm', name: 'Bottom Confirmed', alert: { speak: true } },
    ];
    expect(alertUtterance(seeded, 'dildo-1m', 'PDSB')).toBe('PDSB: HOD dildo');
    expect(alertUtterance(seeded, 'dildo-pb', 'PDSB')).toBe('PDSB: nine E M A pullback');
    expect(alertUtterance(seeded, 'climax-1m', 'BIYA')).toBe('BIYA: volume climax');
    expect(alertUtterance(seeded, 'climax-confirm', 'BIYA')).toBe('BIYA: bottom confirmed');
    expect(alertUtterance(seeded, 'dildo-touch', 'PDSB')).toBeNull(); // opt-in cue: silent until Speak is switched on
    const touchOn = seeded.map((s) => (s.id === 'dildo-touch' ? { ...s, alert: { speak: true } } : s));
    expect(alertUtterance(touchOn, 'dildo-touch', 'PDSB')).toBe('PDSB: nine E M A touch');
  });
});

describe('dildo / climax scan ids', () => {
  it('knows the five seeded ids; none of them is an e9 scan', () => {
    expect([...DILDO_SCAN_IDS]).toEqual(['dildo-1m', 'dildo-pb', 'dildo-touch']);
    expect([...CLIMAX_SCAN_IDS]).toEqual(['climax-1m', 'climax-confirm']);
    for (const id of DILDO_SCAN_IDS) {
      expect(isDildoScanId(id), id).toBe(true);
      expect(isClimaxScanId(id), id).toBe(false);
      expect(isE9ScanId(id), id).toBe(false);
      expect(isAlertRuleScanId(id), id).toBe(true);
    }
    for (const id of CLIMAX_SCAN_IDS) {
      expect(isClimaxScanId(id), id).toBe(true);
      expect(isDildoScanId(id), id).toBe(false);
      expect(isE9ScanId(id), id).toBe(false);
      expect(isAlertRuleScanId(id), id).toBe(true);
    }
    for (const id of [...E9_SCAN_IDS, 'gappers', '']) expect(isAlertRuleScanId(id), id).toBe(false);
  });

  it('orderFlagScans: e9 stages -> slam -> dildo -> climax -> the rest as listed', () => {
    const scan = (id: string) => ({ id, name: id });
    const scans = ['climax-confirm', 'gappers', 'dildo-touch', 'e9flag-slam', 'climax-1m', 'dildo-1m', 'mine', 'e9flag-sideways', 'dildo-pb'].map(scan);
    expect(orderFlagScans(scans).map((s) => s.id)).toEqual([
      'e9flag-sideways',
      'e9flag-slam',
      'dildo-1m',
      'dildo-pb',
      'dildo-touch',
      'climax-1m',
      'climax-confirm',
      'gappers',
      'mine',
    ]);
  });

  it('flagScanLabel: short picker labels for the five seeded names', () => {
    expect(flagScanLabel('Green Dildo')).toBe('HOD dildo');
    expect(flagScanLabel('Dildo 9EMA Pullback')).toBe('dildo pullback');
    expect(flagScanLabel('Dildo 9EMA Touch')).toBe('dildo touch');
    expect(flagScanLabel('Volume Climax')).toBe('climax');
    expect(flagScanLabel('Bottom Confirmed')).toBe('bottom');
    expect(flagScanLabel('My dildos')).toBe('My dildos');
  });
});

// The Signal window's scan view (signalAlertScanIds), its per-signal switches
// (which replaced the "Dildo alerts" / "Climax alerts" toggles) and their
// migration are pinned in signalSwitches.test.ts. A scan a SWITCH adds to the
// view is governed by the switch: a mute set from the list while the list
// showed it (then switched away) would silently swallow its alerts with the
// switch reading ON.
describe('extraScanMutes (per-scan mutes the Signal window switches take over)', () => {
  const view = ['e9flag-sideways', 'dildo-1m', 'dildo-pb', 'climax-1m', 'climax-confirm'];

  it("a mute on a scan the toggle added is released; the list's own scan keeps its mute", () => {
    expect(extraScanMutes(view, 'e9flag-sideways', { 'dildo-1m': true, 'e9flag-sideways': true })).toEqual(['dildo-1m']);
    expect(extraScanMutes(['dildo-1m', 'dildo-pb'], 'dildo-1m', { 'dildo-1m': true })).toEqual([]);
  });

  it('only real mutes, only scans in the view', () => {
    expect(extraScanMutes(view, 'e9flag-sideways', {})).toEqual([]);
    expect(extraScanMutes(view, 'e9flag-sideways', { 'dildo-pb': false, 'climax-confirm': true, gappers: true })).toEqual(['climax-confirm']);
    expect(extraScanMutes(['e9flag-sideways'], 'e9flag-sideways', { 'dildo-1m': true })).toEqual([]); // switched off: not in the view, left alone
  });
});

describe("parseAlertToggle (the old Dildo / Climax alerts toggles, read for the migration)", () => {
  it('has its own localStorage keys', () => {
    expect(DILDO_ALERTS_KEY).toMatch(/^mktscanner\.signal\./);
    expect(CLIMAX_ALERTS_KEY).toMatch(/^mktscanner\.signal\./);
    expect(DILDO_ALERTS_KEY).not.toBe(CLIMAX_ALERTS_KEY);
  });

  it('defaults ON; only an explicit "0" turns it off', () => {
    expect(parseAlertToggle(null)).toBe(true);
    expect(parseAlertToggle(undefined)).toBe(true);
    expect(parseAlertToggle('1')).toBe(true);
    expect(parseAlertToggle('0')).toBe(false);
    for (const raw of ['', 'false', 'off', '{}', 'garbage']) expect(parseAlertToggle(raw), raw).toBe(true);
  });
});

describe('the left-bar list for the dildo and climax scans', () => {
  it('head: the middle column and the age column follow the scan', () => {
    expect(flagListHead('e9flag-sideways')).toEqual({ mid: 'EMA', midKey: 'e9Dist', ageKey: 'e9Age' });
    expect(flagListHead('e9flag-slam')).toEqual({ mid: 'EMA', midKey: 'e9Dist', ageKey: 'e9SlamAge' });
    expect(flagListHead('gappers')).toEqual({ mid: 'EMA', midKey: 'e9Dist', ageKey: 'e9Age' });
    expect(flagListHead('dildo-1m')).toEqual({ mid: 'Body', midKey: 'dildoBody', ageKey: 'dildoAge' });
    expect(flagListHead('dildo-pb')).toEqual({ mid: 'Body', midKey: 'dildoBody', ageKey: 'dildoPbAge' });
    expect(flagListHead('dildo-touch')).toEqual({ mid: 'Body', midKey: 'dildoBody', ageKey: 'dildoTouchAge' });
    expect(flagListHead('climax-1m')).toEqual({ mid: 'Drop', midKey: 'climaxDropPct', ageKey: 'climaxAge' });
    expect(flagListHead('climax-confirm')).toEqual({ mid: 'Drop', midKey: 'climaxDropPct', ageKey: 'climaxConfAge' });
    // every key it names is a registered field (the header's tooltip is that field's hint)
    for (const id of [...E9_SCAN_IDS, ...DILDO_SCAN_IDS, ...CLIMAX_SCAN_IDS]) {
      const h = flagListHead(id);
      expect(FIELD_BY_KEY[h.midKey]?.hint, id).toBeTruthy();
      expect(FIELD_BY_KEY[h.ageKey]?.hint, id).toBeTruthy();
    }
  });

  it("empty texts: the spec's wording for the four alert scans, the touch cue, and the old wording elsewhere", () => {
    expect(flagListEmptyText('dildo-1m', 'Green Dildo')).toMatch(/^No green dildo in the last 4 minutes/);
    expect(flagListEmptyText('dildo-pb', 'Dildo 9EMA Pullback')).toMatch(/^No 9 EMA pullback in the last 4 minutes/);
    expect(flagListEmptyText('climax-1m', 'Volume Climax')).toMatch(/^No volume climax in the last 4 minutes/);
    expect(flagListEmptyText('climax-confirm', 'Bottom Confirmed')).toMatch(/^No bottom confirmed in the last 4 minutes/);
    expect(flagListEmptyText('dildo-touch', 'Dildo 9EMA Touch')).toMatch(/9 EMA/);
    expect(flagListEmptyText('e9flag-slam', '9EMA Slam')).toMatch(/^No 9 EMA slams in the last 5 minutes/);
    expect(flagListEmptyText('e9flag-ready', '9EMA Flag — ready')).toMatch(/^No stocks flagging right now/);
    expect(flagListEmptyText('gappers', 'Gappers')).toBe('No stocks in Gappers right now.');
  });

  it('missing scan: the seeded alert scans name themselves and say how they get seeded', () => {
    expect(flagListMissingText('dildo-1m')).toMatch(/Green Dildo scan is not on this scanner/);
    expect(flagListMissingText('dildo-pb')).toMatch(/Dildo 9EMA Pullback scan/);
    expect(flagListMissingText('dildo-touch')).toMatch(/Dildo 9EMA Touch scan/);
    expect(flagListMissingText('climax-1m')).toMatch(/Volume Climax scan/);
    expect(flagListMissingText('climax-confirm')).toMatch(/Bottom Confirmed scan/);
    for (const id of [...DILDO_SCAN_IDS, ...CLIMAX_SCAN_IDS]) expect(flagListMissingText(id), id).toMatch(/run\.bat/);
    expect(flagListMissingText('e9flag-slam')).toMatch(/The 9EMA Slam scan is not on this scanner yet/);
    expect(flagListMissingText('e9flag-ready')).toMatch(/9EMA flag scans are not on this scanner/);
    expect(flagListMissingText('gone')).toBe('That scan no longer exists — pick another one above.');
  });

  describe('rows of the seeded alert scans (badged by the scan; their rows carry no e9 fields)', () => {
    // the seeded scans' columns (cmd/scanner/main.go dildoScans / climaxScans) plus the side-box fields every row carries
    const dildoRow = { last: 0.578, dildoBody: 18.44, dildoVolX: 18.04, dildoDvol: 1_234_567, dildoAge: 63, chgDay: 25.1, vol: 5e6, news: false, pmVolX: 0.2 };
    const pbRow = { last: 0.57, dildoPbPx: 0.57, dildoBody: 18.44, dildoPbAge: 125, chgDay: 25.1, vol: 5e6, news: false };
    const touchRow = { last: 0.565, dildoBody: 18.44, dildoTouchAge: 30, chgDay: 25.1, news: false };
    const climaxRow = { last: 1.84, climaxDropPct: 13.59, climaxVolX: 9.6, climaxDvol: 716_518, climaxLow: 1.78, climaxAge: 63, chgDay: -8.2, vol: 2e6, news: false };
    const bottomRow = { last: 1.879, climaxConfPx: 1.879, climaxLow: 1.78, climaxDropPct: 13.59, climaxVolX: 9.6, climaxConfAge: 63, chgDay: -6.5, news: false };

    it('Green Dildo: DILDO badge, the body in the middle column, the time since the dildo', () => {
      const [r] = flagListRows(['PDSB'], { PDSB: dildoRow }, new Set(['PDSB']), 'dildo-1m');
      expect(r.badge).toEqual(DILDO_BADGE);
      expect(r.badge?.label).toBe('DILDO');
      expect(r.badge?.cls).toBe('dildo');
      expect(r.dist).toBe('+18.4%');
      expect(r.age).toBe(formatField('dildoAge', 63).text);
      expect(r.title).toBe('green dildo +18.4% body · 18.0× volume · $1.2M candle · 1.1m ago');
      expect(r.onChart).toBe(true);
    });

    it('Dildo 9EMA Pullback: PB badge, aged by the pullback, the entry close in the title', () => {
      const [r] = flagListRows(['PDSB'], { PDSB: pbRow }, new Set(), 'dildo-pb');
      expect(r.badge).toEqual(DILDO_PB_BADGE);
      expect(r.badge?.label).toBe('PB');
      expect(r.dist).toBe('+18.4%');
      expect(r.age).toBe('2.1m');
      expect(r.title).toBe('pulled back to the 9 EMA and held: closed 0.5700 · dildo +18.4% body · 2.1m ago');
    });

    it('Dildo 9EMA Touch: TOUCH badge, aged by the touch, says the candle is still forming', () => {
      const [r] = flagListRows(['PDSB'], { PDSB: touchRow }, new Set(), 'dildo-touch');
      expect(r.badge).toEqual(DILDO_TOUCH_BADGE);
      expect(r.badge?.label).toBe('TOUCH');
      expect(r.age).toBe('30s');
      expect(r.title).toBe('touching the 9 EMA now (the candle is still forming) · dildo +18.4% body · 30s ago');
    });

    it('Volume Climax: CLIMAX badge, the drop as a negative percent, the time since the climax', () => {
      const [r] = flagListRows(['BIYA'], { BIYA: climaxRow }, new Set(), 'climax-1m');
      expect(r.badge).toEqual(CLIMAX_BADGE);
      expect(r.badge?.label).toBe('CLIMAX');
      expect(r.badge?.cls).toBe('climax');
      expect(r.dist).toBe('-13.6%');
      expect(r.age).toBe('1.1m');
      expect(r.title).toBe('volume climax: low 1.78, 13.6% under the 10-bar high · 9.6× volume · $717k candle · 1.1m ago');
    });

    it('Bottom Confirmed: BOTTOM badge, aged by the confirmation, the confirming close and the risk to the climax low', () => {
      const [r] = flagListRows(['BIYA'], { BIYA: bottomRow }, new Set(), 'climax-confirm');
      expect(r.badge).toEqual(BOTTOM_BADGE);
      expect(r.badge?.label).toBe('BOTTOM');
      expect(r.badge?.cls).toBe('bottom');
      expect(r.dist).toBe('-13.6%');
      expect(r.age).toBe('1.1m');
      expect(r.title).toBe('bottom confirmed: closed 1.88 · climax low 1.78 (5.3% under) · 1.1m ago');
    });

    it('every badge explains itself and says it is a "look now" prompt; none collides with a stage or SLAM', () => {
      const badges = [DILDO_BADGE, DILDO_PB_BADGE, DILDO_TOUCH_BADGE, CLIMAX_BADGE, BOTTOM_BADGE];
      for (const b of badges) {
        expect(b.title.length, b.label).toBeGreaterThan(40);
        expect(b.title, b.label).toMatch(/look now/i);
      }
      const all = [...[0, 1, 2, 3, 4].map((s) => e9Badge(s)!), E9_SLAM_BADGE, ...badges];
      expect(new Set(all.map((b) => b.cls)).size).toBe(all.length);
      expect(new Set(all.map((b) => b.label)).size).toBe(all.length);
      expect(new Set(all.map((b) => b.state)).size).toBe(all.length);
    });

    it("a row that has not streamed its fields yet keeps the scan's badge and reads as dashes (never NaN)", () => {
      for (const id of [...DILDO_SCAN_IDS, ...CLIMAX_SCAN_IDS]) {
        const [r] = flagListRows(['X'], { X: { last: 2, dildoBody: NaN, climaxDropPct: Infinity, dildoAge: 'soon' } }, new Set(), id);
        expect(r.badge, id).not.toBeNull();
        expect(r.dist, id).toBe('–');
        expect(r.age, id).toBe('–');
        expect(r.title, id).not.toMatch(/NaN|undefined|Infinity/);
        const [none] = flagListRows(['Y'], {}, new Set(), id);
        expect(none.age, id).toBe('–');
        expect(none.title, id).not.toMatch(/NaN|undefined/);
      }
    });

    it("keeps the scan's own order", () => {
      const rows = { A: dildoRow, B: dildoRow, C: dildoRow };
      expect(flagListRows(['C', 'A', 'B'], rows, new Set(), 'dildo-1m').map((r) => r.sym)).toEqual(['C', 'A', 'B']);
    });

    it('the e9 scans read exactly as before when given their id', () => {
      const rows = { AAA: { e9State: 3, e9Dist: 1.234, e9Trigger: 12.3456, e9FlagBars: 9, e9Age: 300, pmVolX: 3.05 } };
      expect(flagListRows(['AAA'], rows, new Set(), 'e9flag-ready')).toEqual(flagListRows(['AAA'], rows, new Set()));
    });
  });

  describe("a scan of the user's own carrying the alert fields (no e9 stage, no slam)", () => {
    it('the newest event badges the row; the middle column stays the EMA distance its header names', () => {
      const [d] = flagListRows(['X'], { X: { dildoAge: 90, dildoBody: 10, e9Dist: 2.5 } }, new Set(), 'mine');
      expect(d.badge?.label).toBe('DILDO');
      expect(d.age).toBe('1.5m');
      expect(d.dist).toBe('+2.5%');
      const [pb] = flagListRows(['X'], { X: { dildoAge: 400, dildoPbAge: 120, dildoBody: 10 } }, new Set(), 'mine');
      expect(pb.badge?.label).toBe('PB');
      expect(pb.age).toBe('2m');
      const [c] = flagListRows(['X'], { X: { climaxAge: 70, climaxConfAge: 10, climaxLow: 1, climaxConfPx: 1.1 } }, new Set(), 'mine');
      expect(c.badge?.label).toBe('BOTTOM');
      expect(c.age).toBe('10s');
    });

    it('a touch and a pullback on the same bar: PB wins the tie', () => {
      const [r] = flagListRows(['X'], { X: { dildoTouchAge: 65, dildoPbAge: 65 } }, new Set(), 'mine');
      expect(r.badge?.label).toBe('PB');
    });

    it('with the state column present, TOUCH needs state 2 and PB state 3 (a stale touch after a cancel is not shown)', () => {
      const [cancelled] = flagListRows(['X'], { X: { dildoTouchAge: 20, dildoAge: 300, dildoPbState: 4 } }, new Set(), 'mine');
      expect(cancelled.badge?.label).toBe('DILDO');
      const [touching] = flagListRows(['X'], { X: { dildoTouchAge: 20, dildoAge: 300, dildoPbState: 2 } }, new Set(), 'mine');
      expect(touching.badge?.label).toBe('TOUCH');
      const [failed] = flagListRows(['X'], { X: { climaxConfAge: 20, climaxAge: 300, climaxState: 3 } }, new Set(), 'mine');
      expect(failed.badge?.label).toBe('CLIMAX');
    });

    it('a real e9 stage or a fresh slam still wins (the flag scans read exactly as before)', () => {
      const [stage] = flagListRows(['X'], { X: { e9State: 3, e9Age: 60, dildoAge: 10 } }, new Set(), 'mine');
      expect(stage.badge?.label).toBe('READY');
      const [slam] = flagListRows(['X'], { X: { e9SlamAge: 30, climaxAge: 5 } }, new Set(), 'mine');
      expect(slam.badge?.label).toBe('SLAM');
    });

    it('null, negative or non-numeric ages are no event at all', () => {
      const [r] = flagListRows(['X'], { X: { dildoAge: null, dildoPbAge: -5, climaxAge: '60', climaxConfAge: NaN } }, new Set(), 'mine');
      expect(r.badge).toBeNull();
      expect(r.title).toBe('long above – · flag – bars · pm vol –');
    });
  });
});

describe('episodic pivot scans', () => {
  it('speak their alert names', () => {
    expect(spokenScanName('EP Watch')).toBe('episodic pivot watch, earnings');
    expect(spokenScanName(' ep  BREAK ')).toBe('episodic pivot break');
  });
  it('the EP fields exist with short unique headers and hints', () => {
    for (const k of ['epPass', 'epGap', 'epVolX', 'epEarnHL', 'epEarnAge', 'ep3m', 'epAdr', 'epAdrDays', 'epStop', 'epOrh', 'epBrkAge', 'epRiskPct']) {
      const d = FIELD_BY_KEY[k];
      expect(d, k).toBeTruthy();
      expect(d.label.length, k).toBeLessThanOrEqual(12);
      expect(d.hint, k).toBeTruthy();
    }
  });
});
