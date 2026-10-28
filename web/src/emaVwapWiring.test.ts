import { describe, expect, it } from 'vitest';
import { EMA_VWAP_SCAN_IDS, alertSpeech, defaultSignalSwitches, flagScanLabel, orderFlagScans, signalAlertScanIds, spokenScanName } from './e9';
import { FIELD_BY_KEY } from './fields';
import { kindBadge } from './flagEvents';
import { applyFollow, followMatches, normalizeFollow } from './signalFollow';

describe('9EMA VWAP cross wiring', () => {
  it('speaks "SYM: VWAP bull cross" / "SYM: VWAP bear cross"', () => {
    expect(spokenScanName('VWAP Bull Cross')).toBe('VWAP bull cross');
    expect(alertSpeech('AAPL', 'VWAP Bull Cross')).toBe('AAPL: VWAP bull cross');
    expect(alertSpeech('AAPL', 'VWAP Bear Cross')).toBe('AAPL: VWAP bear cross');
    expect(flagScanLabel('VWAP Bear Cross')).toBe('vwap bear');
  });
  it('has its own switches, default ON, and subscribes only when on', () => {
    const d = defaultSignalSwitches();
    expect(d['vwap-bull'] && d['vwap-bear']).toBe(true);
    expect(signalAlertScanIds('gappers', ['vwap-bull', 'vwap-bear'], d)).toEqual(['gappers', 'vwap-bull', 'vwap-bear']);
    expect(signalAlertScanIds('gappers', ['vwap-bull', 'vwap-bear'], { ...d, 'vwap-bear': false })).toEqual(['gappers', 'vwap-bull']);
    expect(orderFlagScans([{ id: 'x' }, { id: 'vwap-bear' }, { id: 'pdl-break' }, { id: 'vwap-bull' }]).map((s) => s.id)).toEqual(['pdl-break', 'vwap-bull', 'vwap-bear', 'x']);
  });
  it('the Signal window follow option', () => {
    expect(normalizeFollow('vwap')).toBe('vwap');
    for (const id of EMA_VWAP_SCAN_IDS) expect(followMatches('vwap', id)).toBe(true);
    expect(followMatches('vwap', 'dildo-1m')).toBe(false);
    expect(followMatches('dildo', 'vwap-bull')).toBe(false);
    const cells = [{ sym: 'AAA', setup: 'pullback' as const, follow: 'vwap' as const }];
    expect(applyFollow(cells, 'vwap-bear', 'BBB')[0]).toMatchObject({ sym: 'BBB', setup: 'pullback' });
  });
  it('fields and history badges', () => {
    for (const k of ['evBullAge', 'evBearAge', 'evEma', 'evVwap', 'evDist']) expect(FIELD_BY_KEY[k]?.group, k).toBe('emavwap');
    expect(kindBadge('vwapBull')).toEqual({ text: 'VWAP+', tone: 'pos' });
    expect(kindBadge('vwapBear').text).toBe('VWAP\u2212');
  });
});
