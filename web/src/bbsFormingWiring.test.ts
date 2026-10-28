import { describe, expect, it } from 'vitest';
import {
  SIGNAL_SWITCH_IDS,
  alertSpeech,
  defaultSignalSwitches,
  flagListEmptyText,
  flagListHead,
  flagListMissingText,
  flagListRows,
  flagScanLabel,
  isAlertRuleScanId,
  orderFlagScans,
} from './e9';
import { SOUNDS } from './audio';
import { FIELD_BY_KEY } from './fields';

describe('bouncy ball forming wiring', () => {
  it('speech, label, soft sound, 11th switch default OFF', () => {
    expect(alertSpeech('AAOI', 'Bouncy Ball Forming')).toBe('AAOI: bouncy ball forming');
    expect(flagScanLabel('Bouncy Ball Forming')).toBe('bouncy forming');
    expect(SOUNDS).toContain('bounceSoft');
    expect(SIGNAL_SWITCH_IDS.length).toBe(24);
    expect(SIGNAL_SWITCH_IDS[11]).toBe('bouncy-forming');
    expect(defaultSignalSwitches()['bouncy-forming']).toBe(false);
    expect(isAlertRuleScanId('bouncy-forming')).toBe(true);
    expect(orderFlagScans([{ id: 'x' }, { id: 'bouncy-forming' }, { id: 'bouncy-short' }]).map((s) => s.id)).toEqual(['bouncy-short', 'bouncy-forming', 'x']);
  });

  it('left-bar list row: distance above support', () => {
    const rows = flagListRows(['AAOI'], { AAOI: { bbsState: 1, bbsSupport: 115.85, bbsDistPct: 0.32, last: 116.26 } }, new Set(), 'bouncy-forming');
    expect(rows[0]!.badge?.label).toBe('FORM');
    expect(rows[0]!.dist).toBe('+0.3%');
    expect(rows[0]!.title).toContain('115.85 support');
    expect(flagListHead('bouncy-forming').midKey).toBe('bbsDistPct');
    expect(flagListMissingText('bouncy-forming')).toContain('Bouncy Ball Forming');
    expect(flagListEmptyText('bouncy-forming', 'Bouncy Ball Forming')).toMatch(/forming/i);
  });

  it('bbsDistPct is a field', () => {
    expect(FIELD_BY_KEY.bbsDistPct?.group).toBe('bbs');
  });
});
