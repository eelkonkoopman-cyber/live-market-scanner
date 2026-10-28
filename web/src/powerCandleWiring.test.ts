import { describe, expect, it } from 'vitest';
import { alertSpeech, flagScanLabel, flagListRows, orderFlagScans, SIGNAL_SWITCH_IDS, defaultSignalSwitches } from './e9';
import { kindBadge } from './flagEvents';

describe('power candle wiring', () => {
  it('speech, label, switch (default ON), history badge, list row', () => {
    expect(alertSpeech('WHLR', 'Power Candle')).toBe('WHLR: power candle');
    expect(flagScanLabel('Power Candle')).toBe('power candle');
    expect(SIGNAL_SWITCH_IDS).toContain('power-candle');
    expect(defaultSignalSwitches()['power-candle']).toBe(true);
    expect(kindBadge('power').text).toBe('POWER');
    expect(orderFlagScans([{ id: 'x' }, { id: 'power-candle' }]).map((s) => s.id)).toEqual(['power-candle', 'x']);
    const rows = flagListRows(['WHLR'], { WHLR: { pcAge: 63, pcBodyPct: 32.8, pcTopPct: 36, pcVolX: 9, pcDvol: 253_000, last: 0.57 } }, new Set(), 'power-candle');
    expect(rows[0].badge?.label).toBe('POWER');
  });
});
