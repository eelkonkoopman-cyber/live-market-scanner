import { describe, expect, it } from 'vitest';
import { fmtPmX } from './components/SideBoxes';

describe('fmtPmX', () => {
  it('drops decimals as the multiple grows so the narrow column fits', () => {
    expect(fmtPmX(0.01)).toBe('0.01x');
    expect(fmtPmX(3.05)).toBe('3.05x');
    expect(fmtPmX(9.994)).toBe('9.99x');
    expect(fmtPmX(9.996)).toBe('10.0x');
    expect(fmtPmX(31.07)).toBe('31.1x');
    expect(fmtPmX(99.49)).toBe('99.5x');
    expect(fmtPmX(99.5)).toBe('100x');
    expect(fmtPmX(142.6)).toBe('143x');
    expect(fmtPmX(1234.4)).toBe('1234x');
  });
  it('shows a dash when there is no value', () => {
    expect(fmtPmX(null)).toBe('–');
    expect(fmtPmX(undefined)).toBe('–');
    expect(fmtPmX(NaN)).toBe('–');
  });
});
