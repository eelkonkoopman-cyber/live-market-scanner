import { describe, expect, it } from 'vitest';
import { alertSpeech, flagListRows, flagScanLabel, isAlertRuleScanId, orderFlagScans, SIGNAL_SWITCH_IDS, defaultSignalSwitches } from './e9';
import { eventBadge, kindBadge } from './flagEvents';
import { applyFollow, followMatches, FOLLOW_KINDS, FOLLOW_LABELS, normalizeFollow } from './signalFollow';
import { SOUNDS } from './audio';
import { fmtSpokenPrice, powerSetupUtterance, psAplusRisk, psFilterTag, psPlanText } from './powerSetup';
import { FIELD_BY_KEY } from './fields';

describe('power setup wiring', () => {
  it('switch default ON, spoken name, label, badge, sound, fields', () => {
    expect(SIGNAL_SWITCH_IDS).toContain('power-setup');
    expect(defaultSignalSwitches()['power-setup']).toBe(true);
    expect(alertSpeech('ABCD', 'Power Setup')).toBe('ABCD: power setup');
    expect(flagScanLabel('Power Setup')).toBe('power setup');
    expect(isAlertRuleScanId('power-setup')).toBe(true);
    expect(kindBadge('powerSetup').text).toBe('P-SETUP');
    expect(SOUNDS).toContain('psetup');
    for (const k of ['psPass', 'psFromOpen', 'psEmaSlope', 'psGap', 'psEntry', 'psStop', 'psTarget', 'psAge']) expect(FIELD_BY_KEY[k]).toBeTruthy();
    expect(orderFlagScans([{ id: 'x' }, { id: 'power-setup' }]).map((s) => s.id)[0]).toBe('power-setup');
  });

  it('spoken text carries entry and stop, rounded sensibly', () => {
    expect(powerSetupUtterance('ABCD: power setup', 'power-setup', { psEntry: 1.23363, psStop: 1.159612 })).toBe('ABCD: power setup, entry 1.23, stop 1.16');
    expect(powerSetupUtterance('ABCD: power setup', 'power-setup', { psEntry: 0.5703, psStop: 0.5361 })).toBe('ABCD: power setup, entry 0.57, stop 0.536'.replace('0.57,', '0.57,'));
    expect(powerSetupUtterance('ABCD: power setup', 'power-setup', { psEntry: 0.04567, psStop: 0.04293 })).toBe('ABCD: power setup, entry 0.0457, stop 0.0429');
    expect(powerSetupUtterance('ABCD: power candle', 'power-candle', { psEntry: 1, psStop: 0.9 })).toBe('ABCD: power candle');
    expect(powerSetupUtterance('ABCD: power setup', 'power-setup', {})).toBe('ABCD: power setup');
    expect(fmtSpokenPrice(12)).toBe('12');
  });

  it('filter tag and plan text; plain power rows carry the tag', () => {
    expect(psFilterTag({ psFromOpen: 15, psEmaSlope: 1.9, psGap: 20 })).toBe('A✓ slope✗');
    expect(psFilterTag({ psFromOpen: 23, psEmaSlope: 3, psGap: null })).toBe('A✗ slope✓');
    expect(psPlanText({ psEntry: 2, psStop: 1.88, psTarget: 2.36 })).toBe('entry 2 · stop 1.88 · target 2.36');
    const rows = flagListRows(['ABCD'], { ABCD: { pcAge: 30, pcBodyPct: 15, psFromOpen: 15, psEmaSlope: 1.9, psGap: 20, last: 1.38 } }, new Set(), 'power-candle');
    expect(rows[0].psTag).toBe('A✓ slope✗');
    const rows2 = flagListRows(['ABCD'], { ABCD: { psAge: 30, psPass: 1, psFromOpen: 15, psEmaSlope: 3, psGap: 20, psEntry: 1.2, psStop: 1.13, psTarget: 1.42, last: 1.38 } }, new Set(), 'power-setup');
    expect(rows2[0].badge?.label).toBe('P-SETUP');
    expect(rows2[0].title).toContain('entry 1.2');
  });

  it('A+ grade: alert text, badges, order-ticket risk', () => {
    const plan = { psEntry: 1.23363, psStop: 1.159612 };
    expect(powerSetupUtterance('ABCD: power setup', 'power-setup', { ...plan, psAplus: 1, psBaseRange: 3.84 })).toBe('ABCD: power setup A+ (tight base 3.8%), entry 1.23, stop 1.16');
    expect(powerSetupUtterance('ABCD: power setup', 'power-setup', { ...plan, psAplus: 0, psBaseRange: 7 })).toBe('ABCD: power setup, entry 1.23, stop 1.16');
    expect(FIELD_BY_KEY.psAplus).toBeTruthy();
    expect(FIELD_BY_KEY.psBaseRange).toBeTruthy();
    const rows = flagListRows(['ABCD'], { ABCD: { psAge: 30, psPass: 1, psAplus: 1, psBaseRange: 3.8, psEntry: 1.2, psStop: 1.13, last: 1.38 } }, new Set(), 'power-setup');
    expect(rows[0].badge?.label).toBe('P-SETUP A+');
    expect(eventBadge({ kind: 'powerSetup', label: 'P-SETUP A+' }).text).toBe('P-SETUP A+');
    expect(eventBadge({ kind: 'powerSetup', label: 'P-SETUP' }).text).toBe('P-SETUP');
    const g = { aplus: true, mult: 1.5 };
    expect(psAplusRisk(100, g, true, false)).toEqual({ risk: 150, applied: true, eligible: true });
    expect(psAplusRisk(100, g, true, true)).toEqual({ risk: 100, applied: false, eligible: true }); // user override
    expect(psAplusRisk(100, g, false, false).risk).toBe(100); // Custom preset: never
    expect(psAplusRisk(100, { aplus: false, mult: 1 }, true, false).risk).toBe(100);
    expect(psAplusRisk(100, null, true, false).eligible).toBe(false);
  });

  it('follow option "Power setup"', () => {
    expect(FOLLOW_KINDS).toContain('powerSetup');
    expect(FOLLOW_LABELS.powerSetup).toBe('Power setup');
    expect(normalizeFollow('powerSetup')).toBe('powerSetup');
    expect(followMatches('powerSetup', 'power-setup')).toBe(true);
    expect(followMatches('powerSetup', 'power-candle')).toBe(false);
    expect(followMatches('power', 'power-setup')).toBe(false);
    const cells = [{ sym: 'AAA', setup: 'flag' as const, follow: 'powerSetup' as const }];
    expect(applyFollow(cells, 'power-setup', 'BBB')[0].sym).toBe('BBB');
    expect(applyFollow(cells, 'power-candle', 'BBB')).toBe(cells);
  });
});
