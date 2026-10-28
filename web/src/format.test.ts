// Formatting coverage for the field-registry cell formatter: each `kind`
// (price/pct/compact/ratio/dur/...) has exactly one rendering rule shared by
// every column of that kind, so pinning it down here catches a regression
// wherever it is consumed (grid cells, activity log, symbol panels).
import { describe, expect, it } from 'vitest';
import { fmtCompact, fmtDur, fmtPct, fmtPrice, fmtRatio, formatField } from './format';

describe('fmtPrice', () => {
  it('renders a whole-dollar price to 2 decimals', () => {
    expect(fmtPrice(22)).toBe('22.00');
  });

  it('renders sub-$1 prices to 4 decimals so they keep their tick precision', () => {
    expect(fmtPrice(0.4567)).toBe('0.4567');
  });

  it('renders an ordinary price to 2 decimals', () => {
    expect(fmtPrice(27.4)).toBe('27.40');
  });

  it('non-finite input renders as a dash', () => {
    expect(fmtPrice(null)).toBe('–');
    expect(fmtPrice(undefined)).toBe('–');
    expect(fmtPrice(NaN)).toBe('–');
  });
});

describe('fmtPct', () => {
  it('signs a positive change', () => {
    expect(fmtPct(3.4)).toBe('+3.40%');
  });

  it('a negative change already carries its own sign', () => {
    expect(fmtPct(-0.5)).toBe('-0.50%');
  });

  it('unsigned mode never adds a leading +', () => {
    expect(fmtPct(12.3, false)).toBe('12.30%');
  });

  it('non-finite input renders as a dash', () => {
    expect(fmtPct(NaN)).toBe('–');
  });
});

describe('fmtCompact', () => {
  it('compacts large counts the same way fmt does', () => {
    expect(fmtCompact(6_800_000)).toBe('6.8M');
  });

  it('leaves small counts as a plain grouped number', () => {
    expect(fmtCompact(9999)).toBe('9,999');
  });

  it('non-finite input renders as a dash', () => {
    expect(fmtCompact(NaN)).toBe('–');
  });
});

describe('fmtRatio', () => {
  it('renders a multiple with an x suffix', () => {
    expect(fmtRatio(2.58)).toBe('2.58x');
  });

  it('non-finite input renders as a dash', () => {
    expect(fmtRatio(NaN)).toBe('–');
  });
});

describe('fmtDur', () => {
  it('renders sub-minute durations in seconds', () => {
    expect(fmtDur(35)).toBe('35s');
  });

  it('renders an exact number of minutes with no decimal', () => {
    expect(fmtDur(300)).toBe('5m');
  });

  it('renders a fractional minute count to one decimal', () => {
    expect(fmtDur(270)).toBe('4.5m');
  });

  it('renders durations past an hour in hours', () => {
    expect(fmtDur(4320)).toBe('1.2h');
  });

  it('negative or non-finite input renders as a dash', () => {
    expect(fmtDur(-5)).toBe('–');
    expect(fmtDur(NaN)).toBe('–');
  });
});

describe('formatField', () => {
  it('null/undefined always renders as a dash, regardless of kind', () => {
    expect(formatField('chg5m', null)).toEqual({ text: '–', tone: '' });
    expect(formatField('chg5m', undefined)).toEqual({ text: '–', tone: '' });
  });

  it('signed percent tones a falling value negative', () => {
    expect(formatField('chg5m', -1.25)).toEqual({ text: '-1.25%', tone: 'neg' });
  });

  it('unsigned percent (pctAbs) never tones, even for a fade metric', () => {
    expect(formatField('preFade', 12.3)).toEqual({ text: '12.30%', tone: '' });
  });

  it('shortAvail prefixes the IBKR capped-file floor marker', () => {
    expect(formatField('shortAvail', 10_000_000, { shortCapped: true }).text).toBe('>10M');
  });

  it('shortAvail without the capped flag renders a plain compacted count', () => {
    expect(formatField('shortAvail', 250_000, { shortCapped: false }).text).toBe('250K');
  });

  it('flagState renders the named state and tones breakout/none', () => {
    expect(formatField('flagState', 1).text).toBe('coiling');
    expect(formatField('flagState', 2)).toEqual({ text: 'breakout', tone: 'pos' });
    expect(formatField('flagState', 0)).toEqual({ text: 'none', tone: 'muted' });
  });

  it('secType maps wire codes to words', () => {
    expect(formatField('secType', 'ADRC').text).toBe('ADR');
    expect(formatField('secType', 'CS').text).toBe('Stock');
    expect(formatField('secType', 'ETF').text).toBe('ETF');
  });

  it('an unknown key falls back to the generic formatter instead of rendering blank', () => {
    expect(formatField('someFutureField', 42).text).toBe('42');
  });
});
