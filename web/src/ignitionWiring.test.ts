import { describe, expect, it } from 'vitest';
import { alertSpeech, cellEventHeard, defaultSignalSwitches, flagListHead, flagListRows, flagScanLabel, isAlertRuleScanId, orderFlagScans } from './e9';
import { SOUNDS } from './audio';
import { chartMarkers, soundForEvent } from './signals/view';
import { kindBadge } from './flagEvents';
import { fieldDef } from './fields';
import { evaluateIgnition } from './signals/ignition';
import { ncplBars } from './signals/ignition.fixtures';

describe('ignition wiring', () => {
  it('speech, label, sound, switch', () => {
    expect(alertSpeech('NCPL', 'Ignition')).toBe('NCPL: ignition');
    expect(flagScanLabel('Ignition')).toBe('ignition');
    expect(SOUNDS).toContain('ignite');
    expect(defaultSignalSwitches().ignition).toBe(true);
    expect(cellEventHeard({ ...defaultSignalSwitches(), ignition: false }, 'flag', 'ignition')).toBe(false);
    expect(isAlertRuleScanId('ignition')).toBe(true);
    expect(orderFlagScans([{ id: 'x' }, { id: 'ignition' }, { id: 'bouncy-short' }]).map((s) => s.id)).toEqual(['bouncy-short', 'ignition', 'x']);
    expect(kindBadge('ignition').text).toBe('IGN');
  });

  it('row fields exist', () => {
    for (const k of ['ignAge', 'ignBodyPct', 'ignVolX']) expect(fieldDef(k)?.group, k).toBe('ignition');
  });

  it('left-bar list row', () => {
    const rows = flagListRows(['NCPL'], { NCPL: { ignAge: 63, ignBodyPct: 5.03, ignVolX: 15, last: 2.3 } }, new Set(), 'ignition');
    expect(rows[0]!.badge?.label).toBe('IGN');
    expect(rows[0]!.dist).toBe('+5.0%');
    expect(rows[0]!.title).toContain('ignition');
    expect(flagListHead('ignition').midKey).toBe('ignBodyPct');
  });

  it('chart marker is IGN ▲ and silent in a cell', () => {
    const ev = evaluateIgnition({ candles: ncplBars(), tfMs: 60_000, nowMs: Infinity });
    expect(chartMarkers(ev)).toEqual([{ ts: ev[0]!.ts, price: 1.88, side: 'buy', label: 'IGN' }]);
    expect(soundForEvent(ev[0]!)).toBeNull();
  });
});
