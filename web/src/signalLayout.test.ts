// Regression coverage for the Signal window's grid layout state
// (signalLayout.ts) — addendum 2's pure, tested layout module, mirroring how
// store.ts's own makeGrid/normalizeGrid behave for the charts monitor.
// Addendum 4 adds the per-cell `side` selector ('short' | 'long' | 'both');
// iteration 6 adds the per-cell `setup` selector ('flag' | 'pullback') — flag is
// the default for new cells AND for every cell of a layout saved before it.
import { describe, expect, it } from 'vitest';
import { DEFAULT_FLAG_PARAMS } from './signals/emaFlag';
import { DEFAULT_DILDO_PARAMS } from './signals/dildoPb';
import { DEFAULT_CLIMAX_PARAMS } from './signals/climax';
import {
  getSignalRuleParams,
  layoutSyms,
  makeSignalLayout,
  normalizeClimaxParams,
  normalizeDildoParams,
  normalizeFlagParams,
  normalizeSignalLayout,
  setClimaxParams,
  setDildoParams,
  subscribeSignalRuleParams,
  type SignalCellDef,
} from './signalLayout';

describe('makeSignalLayout', () => {
  it('preserves cells that still fit when the preset changes (row-major, flat index)', () => {
    const prev: SignalCellDef[] = [
      { sym: 'ATYR', tf: '1m', side: 'both', setup: 'flag' },
      { sym: 'GCTK', tf: '5m', side: 'long', setup: 'pullback' },
    ];
    // Growing 1x1 (well, prev came from a 1x2) up to 2x2: index 0 and 1 keep
    // their old assignments (including the side and setup selectors), the two
    // new cells are empty and default to showing both sides on the flag engine.
    const grown = makeSignalLayout(2, 2, prev, 0);
    expect(grown.cells).toEqual([
      { sym: 'ATYR', tf: '1m', side: 'both', setup: 'flag' },
      { sym: 'GCTK', tf: '5m', side: 'long', setup: 'pullback' },
      { sym: null, tf: '1m', side: 'both', setup: 'flag' },
      { sym: null, tf: '1m', side: 'both', setup: 'flag' },
    ]);
    // Shrinking back down to 1x1 keeps only index 0.
    const shrunk = makeSignalLayout(1, 1, grown.cells, 0);
    expect(shrunk.cells).toEqual([{ sym: 'ATYR', tf: '1m', side: 'both', setup: 'flag' }]);
  });

  it('clamps active into the new cell count', () => {
    const l = makeSignalLayout(1, 1, undefined, 3);
    expect(l.active).toBe(0);
    const l2 = makeSignalLayout(2, 2, undefined, 3);
    expect(l2.active).toBe(3);
    const l3 = makeSignalLayout(2, 2, undefined, -1);
    expect(l3.active).toBe(0);
  });

  it('gives a cell handed over without a side (pre-addendum-4 shape) the default', () => {
    const legacy = [{ sym: 'ATYR', tf: '5m' }] as unknown as SignalCellDef[];
    expect(makeSignalLayout(1, 1, legacy).cells[0]).toEqual({ sym: 'ATYR', tf: '5m', side: 'both', setup: 'flag' });
  });

  it("gives a cell handed over without a setup (pre-iteration-6 shape) the flag engine, and leaves a saved pullback alone", () => {
    const legacy = [{ sym: 'ATYR', tf: '1m', side: 'short' }] as unknown as SignalCellDef[];
    expect(makeSignalLayout(1, 1, legacy).cells[0]).toEqual({ sym: 'ATYR', tf: '1m', side: 'short', setup: 'flag' });
    expect(makeSignalLayout(1, 1, [{ sym: 'ATYR', tf: '1m', side: 'short', setup: 'pullback' }]).cells[0].setup).toBe('pullback');
  });
});

describe('normalizeSignalLayout', () => {
  it('rejects a foreign/garbage shape', () => {
    expect(normalizeSignalLayout(null)).toBeNull();
    expect(normalizeSignalLayout('nope')).toBeNull();
    expect(normalizeSignalLayout({})).toBeNull();
    // Non-numeric rows/cols (same "not a number at all" rejection store.ts's
    // own normalizeGrid uses) — a numeric 0 instead clamps up to 1, same as
    // there, so that is not the "garbage" case to assert here.
    expect(normalizeSignalLayout({ rows: 'x', cols: 2, cells: [] })).toBeNull();
  });

  it('lowercases syms up, trims, and ticker-gates each cell', () => {
    const raw = {
      rows: 1,
      cols: 2,
      cells: [{ sym: '  atyr  ', tf: '5m' }, { sym: 'not a ticker!', tf: 'bogus' }],
      active: 1,
    };
    const l = normalizeSignalLayout(raw);
    expect(l).toEqual({
      rows: 1,
      cols: 2,
      cells: [
        { sym: 'ATYR', tf: '5m', side: 'both', setup: 'flag' }, // no side/setup saved (a layout from before addendum 4 / iteration 6) -> 'both' / 'flag'
        { sym: null, tf: '1m', side: 'both', setup: 'flag' }, // invalid ticker -> null; invalid tf -> '1m'
      ],
      active: 1,
    });
  });

  it("keeps a valid saved side and degrades anything else to 'both'", () => {
    const l = normalizeSignalLayout({
      rows: 2,
      cols: 2,
      cells: [
        { sym: 'ATYR', tf: '1m', side: 'short' },
        { sym: 'GCTK', tf: '1m', side: 'long' },
        { sym: 'SPHL', tf: '1m', side: 'both' },
        { sym: 'WETO', tf: '1m', side: 'sideways' },
      ],
      active: 0,
    });
    expect(l?.cells.map((c) => c.side)).toEqual(['short', 'long', 'both', 'both']);
  });

  it("keeps a valid saved setup and degrades a missing or unknown one to 'flag' (existing saved layouts become flag cells)", () => {
    const l = normalizeSignalLayout({
      rows: 2,
      cols: 2,
      cells: [
        { sym: 'ATYR', tf: '1m', side: 'short' }, // saved before the selector existed
        { sym: 'GCTK', tf: '1m', side: 'long', setup: 'pullback' },
        { sym: 'SPHL', tf: '1m', setup: 'flag' },
        { sym: 'WETO', tf: '1m', setup: 'breakout' }, // unknown -> flag
      ],
      active: 0,
    });
    expect(l?.cells.map((c) => c.setup)).toEqual(['flag', 'pullback', 'flag', 'flag']);
    // The side survives next to a flag setup (flipping the cell back to pullback restores it).
    expect(l?.cells[0].side).toBe('short');
  });

  it('round-trips through JSON (what loadSignalLayout/saveSignalLayout do) with the setup intact', () => {
    const l = makeSignalLayout(1, 2, [
      { sym: 'ATYR', tf: '1m', side: 'both', setup: 'flag' },
      { sym: 'GCTK', tf: '5m', side: 'long', setup: 'pullback' },
    ]);
    expect(normalizeSignalLayout(JSON.parse(JSON.stringify(l)))).toEqual(l);
  });

  it('clamps active into range', () => {
    const l = normalizeSignalLayout({ rows: 1, cols: 1, cells: [{ sym: 'ATYR', tf: '1m' }], active: 99 });
    expect(l?.active).toBe(0);
  });
});

describe('layoutSyms', () => {
  it('dedupes across cells, first-appearance order', () => {
    const l = makeSignalLayout(2, 2, [
      { sym: 'GCTK', tf: '1m', side: 'both', setup: 'flag' },
      { sym: 'ATYR', tf: '1m', side: 'both', setup: 'flag' },
      { sym: 'GCTK', tf: '5m', side: 'both', setup: 'pullback' },
      { sym: null, tf: '1m', side: 'both', setup: 'flag' },
    ]);
    expect(layoutSyms(l)).toEqual(['GCTK', 'ATYR']);
  });

  it('is empty for an all-empty layout', () => {
    expect(layoutSyms(makeSignalLayout(1, 1))).toEqual([]);
  });
});

describe('normalizeFlagParams (the flag engine persisted Tuning)', () => {
  it('nothing / garbage / an empty object -> exactly the defaults', () => {
    expect(normalizeFlagParams(null)).toEqual(DEFAULT_FLAG_PARAMS);
    expect(normalizeFlagParams('nope')).toEqual(DEFAULT_FLAG_PARAMS);
    expect(normalizeFlagParams({})).toEqual(DEFAULT_FLAG_PARAMS);
    // a fresh object each time, never the shared defaults
    expect(normalizeFlagParams(null)).not.toBe(DEFAULT_FLAG_PARAMS);
  });

  it('keeps valid saved values field by field and fills the rest from the defaults', () => {
    const p = normalizeFlagParams({ emaPeriod: 20, minExtPct: 7.5, trailEma: false, maxAdds: 0 });
    expect(p).toEqual({ ...DEFAULT_FLAG_PARAMS, emaPeriod: 20, minExtPct: 7.5, trailEma: false, maxAdds: 0 });
  });

  it('a wrong-typed, non-finite, negative or out-of-range value falls back to the default of that one field — the others still apply', () => {
    const p = normalizeFlagParams({
      emaPeriod: 0, // an EMA length of 0 would be NaN arithmetic
      atrPeriod: 2.5, // lengths are whole numbers
      minFlatBars: 0,
      breakoutLookback: '30', // wrong type
      minExtPct: -1, // negative
      maxFlatPct: null,
      readyDistPct: 6, // fine
      stopByClose: 'yes', // wrong type
      entryNeedsReady: false, // fine
      maxAdds: 1.5, // whole number
    });
    expect(p).toEqual({ ...DEFAULT_FLAG_PARAMS, readyDistPct: 6, entryNeedsReady: false });
  });

  it('ignores keys the engine does not have', () => {
    const p = normalizeFlagParams({ emaPeriod: 5, bogus: 1, __proto__x: 2 });
    expect(Object.keys(p).sort()).toEqual(Object.keys(DEFAULT_FLAG_PARAMS).sort());
    expect(p.emaPeriod).toBe(5);
  });

  describe('the 9 EMA SLAM params (2026-10-07)', () => {
    it('a blob saved before they existed gets their defaults (the switch on), the rest of it still applies', () => {
      const old = { emaPeriod: 9, minExtPct: 6, maxFlagBars: 20 }; // a pre-SLAM save
      const p = normalizeFlagParams(old);
      expect(p).toEqual({ ...DEFAULT_FLAG_PARAMS, minExtPct: 6, maxFlagBars: 20 });
      expect(p.slam).toBe(true);
      expect(p.slamBelowPct).toBe(3);
    });

    it('keeps valid values: the switch off, and every threshold (fractions and zero included)', () => {
      const saved = { slam: false, slamBelowPct: 2.5, slamOpenTolPct: 0, slamBodyAtr: 0, slamMinBodyPct: 1.5, slamCloseInRange: 1, slamVolX: 0 };
      expect(normalizeFlagParams(saved)).toEqual({ ...DEFAULT_FLAG_PARAMS, ...saved });
    });

    it('a wrong type, a negative, a non-finite value or a close-in-range over 1 falls back to that one default', () => {
      const p = normalizeFlagParams({
        slam: 'false', // wrong type
        slamBelowPct: -2, // negative
        slamOpenTolPct: Number.NaN,
        slamBodyAtr: Number.POSITIVE_INFINITY,
        slamMinBodyPct: '2', // wrong type
        slamCloseInRange: 1.5, // a fraction of the range: over 1 is a typo
        slamVolX: 2, // fine
      });
      expect(p).toEqual({ ...DEFAULT_FLAG_PARAMS, slamVolX: 2 });
    });
  });
});

describe('the GREEN DILDO and VOLUME CLIMAX chart params (2026-10-07)', () => {
  it('a missing, foreign or corrupt blob is the defaults', () => {
    for (const raw of [null, undefined, 'x', 42, []]) {
      expect(normalizeDildoParams(raw)).toEqual(DEFAULT_DILDO_PARAMS);
      expect(normalizeClimaxParams(raw)).toEqual(DEFAULT_CLIMAX_PARAMS);
    }
  });

  it('round-trips every field through JSON', () => {
    const d = { ...DEFAULT_DILDO_PARAMS, enabled: false, minBodyPct: 6.5, minBodyX: 3, maxTopPct: 25, minVolX: 4, priorBars: 8, minPriorBars: 2, minPrice: 0.5, minDollarVol: 250_000, pbWindowMin: 15, touchPct: 0.5, holdPct: 2, floor: 'open' as const, emaPeriod: 12 };
    expect(normalizeDildoParams(JSON.parse(JSON.stringify(d)))).toEqual(d);
    const c = { ...DEFAULT_CLIMAX_PARAMS, enabled: false, lookback: 12, minDropPct: 5, minVolX: 6, requireMaxVol: false, minWickPct: 30, requireBelowEma: false, emaPeriod: 20, minPrice: 1, minDollarVol: 100_000, muteStartMin: 570, muteEndMin: 580, coreHoursOnly: true, confirmBars: 3 };
    expect(normalizeClimaxParams(JSON.parse(JSON.stringify(c)))).toEqual(c);
  });

  it('a bad field falls back to its own default, field by field; unknown keys are dropped', () => {
    const d = normalizeDildoParams({ minBodyPct: -1, minBodyX: 'x', priorBars: 2.5, pbWindowMin: 0, emaPeriod: 0, floor: 'close', minVolX: 7, junk: 1 });
    expect(d).toEqual({ ...DEFAULT_DILDO_PARAMS, minVolX: 7 });
    expect('junk' in d).toBe(false);
    expect(normalizeDildoParams({ minPriorBars: 0 }).minPriorBars, 'zero priors is allowed (whole, >= 0)').toBe(0);
    const c = normalizeClimaxParams({ lookback: 0, confirmBars: 1.5, muteStartMin: 1441, muteEndMin: 'x', minDropPct: Infinity, minWickPct: 0, coreHoursOnly: 'yes' });
    expect(c).toEqual({ ...DEFAULT_CLIMAX_PARAMS, minWickPct: 0 });
    expect(normalizeClimaxParams({ muteStartMin: 0, muteEndMin: 0 }), 'start >= end (no mute) is a valid setting').toEqual({ ...DEFAULT_CLIMAX_PARAMS, muteStartMin: 0, muteEndMin: 0 });
  });

  it('the shared store: one stable snapshot, replaced and published on a change (and normalised)', () => {
    const a = getSignalRuleParams();
    expect(getSignalRuleParams()).toBe(a);
    let calls = 0;
    const off = subscribeSignalRuleParams(() => calls++);
    setDildoParams({ ...a.dildo, minBodyPct: 10 });
    const b = getSignalRuleParams();
    expect(b).not.toBe(a);
    expect(b.dildo.minBodyPct).toBe(10);
    expect(b.climax).toBe(a.climax);
    setClimaxParams({ ...b.climax, confirmBars: -3 });
    expect(getSignalRuleParams().climax.confirmBars, 'normalised on the way in').toBe(DEFAULT_CLIMAX_PARAMS.confirmBars);
    expect(calls).toBe(2);
    off();
    setDildoParams(DEFAULT_DILDO_PARAMS);
    setClimaxParams(DEFAULT_CLIMAX_PARAMS);
    expect(calls, 'unsubscribed').toBe(2);
    expect(getSignalRuleParams()).toEqual({ dildo: DEFAULT_DILDO_PARAMS, climax: DEFAULT_CLIMAX_PARAMS });
  });
});
