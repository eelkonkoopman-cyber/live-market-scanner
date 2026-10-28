import { describe, expect, it } from 'vitest';
import { twsAutoToast } from './twsAuto';

describe('twsAutoToast', () => {
  it('says where the alert went when it was sent', () => {
    expect(twsAutoToast({ sym: 'abcd', scanName: 'Green Dildo', sent: true })).toBe('TWS → ABCD · Green Dildo');
  });

  it('says why it was not sent', () => {
    expect(twsAutoToast({ sym: 'ABCD', scanName: 'Green Dildo', sent: false, reason: 'you picked XYZ 8s ago' })).toBe(
      'Green Dildo ABCD not sent to TWS — you picked XYZ 8s ago',
    );
  });

  it('copes with a missing scan name or reason', () => {
    expect(twsAutoToast({ sym: 'ABCD', sent: true })).toBe('TWS → ABCD');
    expect(twsAutoToast({ sym: 'ABCD', sent: false })).toBe('ABCD not sent to TWS');
  });

  it('ignores a frame with no symbol', () => {
    expect(twsAutoToast({ sent: true })).toBeNull();
    expect(twsAutoToast(undefined)).toBeNull();
  });
});
