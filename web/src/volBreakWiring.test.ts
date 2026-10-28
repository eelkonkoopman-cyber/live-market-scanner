import { describe, expect, it } from 'vitest';
import { alertSpeech, defaultSignalSwitches, flagListHead, flagListRows, flagScanLabel, isAlertRuleScanId, orderFlagScans, SIGNAL_SWITCH_IDS } from './e9';
import { allKinds, kindBadge } from './flagEvents';
import { applyFollow, followMatches, FOLLOW_KINDS, FOLLOW_LABELS, normalizeFollow } from './signalFollow';
import { SOUNDS } from './audio';
import { volBreakText, volBreakUtterance } from './volBreak';
import { FIELD_BY_KEY, FIELD_CATEGORIES } from './fields';

describe('volume break wiring', () => {
  it('switches default ON, spoken names, labels, sound, fields', () => {
    for (const id of ['vol-break', 'vol-break-9ema']) {
      expect(SIGNAL_SWITCH_IDS).toContain(id);
      expect(defaultSignalSwitches()[id as (typeof SIGNAL_SWITCH_IDS)[number]]).toBe(true);
      expect(isAlertRuleScanId(id)).toBe(true);
    }
    expect(alertSpeech('ABCD', 'Vol Break')).toBe('ABCD: volume break');
    expect(alertSpeech('ABCD', 'Vol Break 9EMA')).toBe('ABCD: vol break nine E M A');
    expect(flagScanLabel('Vol Break')).toBe('vol break');
    expect(flagScanLabel('Vol Break 9EMA')).toBe('vol break 9ema');
    expect(SOUNDS).toContain('volbreak');
    for (const k of ['vbAge', 'vbLevel', 'vbVolX', 'vbEma', 'vbState', 'vbEntryAge', 'vbEntryPx']) expect(FIELD_BY_KEY[k]?.group).toBe('volbreak');
    expect(FIELD_CATEGORIES.find((c) => c.label === 'Signals')?.groups).toContain('volbreak');
    expect(orderFlagScans([{ id: 'x' }, { id: 'vol-break-9ema' }, { id: 'vol-break' }]).map((s) => s.id)).toEqual(['vol-break', 'vol-break-9ema', 'x']);
  });

  it('spoken text carries the level, the volume multiple and the 9 EMA', () => {
    const row = { vbLevel: 3.43, vbVolX: 4.234, vbEma: 3.3112, vbEntryPx: 3.4521, vbState: 2 };
    expect(volBreakUtterance('ABCD: volume break', 'vol-break', row)).toBe('ABCD: volume break 3.43 (4.2x vol), nine E M A 3.31');
    expect(volBreakUtterance('ABCD: vol break nine E M A', 'vol-break-9ema', row)).toBe('ABCD: vol break nine E M A entry 3.45, level 3.43');
    expect(volBreakText('vol-break', row)).toBe('3.43 (4.2x vol), 9EMA 3.31');
    expect(volBreakUtterance('ABCD: volume break', 'vol-break', {})).toBe('ABCD: volume break');
    expect(volBreakUtterance('ABCD: power setup', 'power-setup', row)).toBe('ABCD: power setup');
  });

  it('badges: VOL BRK and VB 9EMA, in the list and in SIGNALS (with filter chips)', () => {
    const r1 = flagListRows(['ABCD'], { ABCD: { vbAge: 60, vbLevel: 3.43, vbVolX: 4.2, vbEma: 3.31, vbState: 1, last: 3.6 } }, new Set(), 'vol-break');
    expect(r1[0].badge?.label).toBe('VOL BRK');
    expect(r1[0].title).toContain('volume break 3.43 (4.2x vol)');
    const r2 = flagListRows(['ABCD'], { ABCD: { vbEntryAge: 30, vbState: 2, vbLevel: 3.43, vbEntryPx: 3.45, last: 3.5 } }, new Set(), 'vol-break-9ema');
    expect(r2[0].badge?.label).toBe('VB 9EMA');
    expect(flagListHead('vol-break').mid).toBe('Level');
    // a user scan carrying both ages: the newest event
    const r3 = flagListRows(['ABCD'], { ABCD: { vbAge: 300, vbEntryAge: 30, vbState: 2 } }, new Set(), 'mine');
    expect(r3[0].badge?.label).toBe('VB 9EMA');
    expect(kindBadge('volBreak').text).toBe('VOL BRK');
    expect(kindBadge('volBreakEntry').text).toBe('VB 9EMA');
    const chips = allKinds([]).map((c) => c.kind);
    expect(chips).toContain('volBreak');
    expect(chips).toContain('volBreakEntry');
  });

  it('follow option "Vol break" follows the break and its entry', () => {
    expect(FOLLOW_KINDS).toContain('volBreak');
    expect(FOLLOW_LABELS.volBreak).toBe('Vol break');
    expect(normalizeFollow('volBreak')).toBe('volBreak');
    expect(followMatches('volBreak', 'vol-break')).toBe(true);
    expect(followMatches('volBreak', 'vol-break-9ema')).toBe(true);
    expect(followMatches('volBreak', 'power-setup')).toBe(false);
    const cells = [{ sym: 'AAA', setup: 'flag' as const, follow: 'volBreak' as const }];
    expect(applyFollow(cells, 'vol-break', 'BBB')[0].sym).toBe('BBB');
  });
});
