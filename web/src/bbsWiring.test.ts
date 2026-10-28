import { describe, expect, it } from 'vitest';
import { alertSpeech, cellEventHeard, defaultSignalSwitches, flagListHead, flagListRows, flagScanLabel, isAlertRuleScanId, orderFlagScans } from './e9';
import { SOUNDS } from './audio';
import { chartMarkers, soundForEvent } from './signals/view';
import { kindBadge } from './flagEvents';
import { evaluateBbs } from './signals/bouncyShort';
import { aaoiBars } from './signals/bouncyShort.fixtures';

describe('bouncy ball short wiring', () => {
  it('speech, label, sound, switch', () => {
    expect(alertSpeech('AAOI', 'Bouncy Ball Short')).toBe('AAOI: bouncy ball short');
    expect(flagScanLabel('Bouncy Ball Short')).toBe('bouncy short');
    expect(SOUNDS).toContain('bounce');
    expect(defaultSignalSwitches()['bouncy-short']).toBe(true);
    expect(cellEventHeard({ ...defaultSignalSwitches(), 'bouncy-short': false }, 'flag', 'bbs')).toBe(false);
    expect(isAlertRuleScanId('bouncy-short')).toBe(true);
    expect(orderFlagScans([{ id: 'x' }, { id: 'bouncy-short' }, { id: 'climax-1m' }]).map((s) => s.id)).toEqual(['climax-1m', 'bouncy-short', 'x']);
    expect(kindBadge('bbs').text).toBe('BBS');
  });

  it('left-bar list row', () => {
    const rows = flagListRows(['AAOI'], { AAOI: { bbsAge: 63, bbsSupport: 115.85, bbsDvol: 2_304_000, last: 115.2 } }, new Set(), 'bouncy-short');
    expect(rows[0]!.badge?.label).toBe('BBS');
    expect(rows[0]!.dist).toBe('-0.6%');
    expect(rows[0]!.title).toContain('115.85 support');
    expect(flagListHead('bouncy-short').midKey).toBe('bbsSupport');
  });

  it('chart marker is BBS ▼ and silent in a cell', () => {
    const ev = evaluateBbs({ candles: aaoiBars(), tfMs: 60_000, nowMs: Infinity }).events;
    expect(chartMarkers(ev)).toEqual([{ ts: ev[0]!.ts, price: 115.2, side: 'sell', label: 'BBS' }]);
    expect(soundForEvent(ev[0]!)).toBeNull();
  });
});
