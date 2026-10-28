// Regression coverage for the AfterBell chart-color bridge. Two things could
// silently go wrong here without TypeScript ever noticing: an off-by-one in
// tripletToRgb's string formatting (a chart library that takes a literal CSS
// color string won't complain, it'll just render the wrong candle color), or
// a forgotten per-token fallback leaving a chart transparent/invisible when
// afterbell-themes.css hasn't loaded. Mirrors summary.display.test.ts's
// style: pure functions, no DOM, one file per module under test.
import { describe, expect, it } from 'vitest';
import { chartColorsFrom, chartFontsFrom, chartLevelColor, tripletToRgb } from './chartTheme';

describe('tripletToRgb', () => {
  it('formats a plain triplet with no alpha', () => {
    expect(tripletToRgb('10 10 10')).toBe('rgb(10, 10, 10)');
  });

  it('formats a triplet with alpha', () => {
    expect(tripletToRgb('123 168 123', 0.4)).toBe('rgba(123, 168, 123, 0.4)');
  });

  it('tolerates extra internal whitespace, with and without alpha', () => {
    expect(tripletToRgb('  10   20    30  ')).toBe('rgb(10, 20, 30)');
    expect(tripletToRgb('201   122  110', 0.05)).toBe('rgba(201, 122, 110, 0.05)');
  });
});

// The warm-theme triplets from AfterBell's DEFAULT_TRIPLETS, standing in for
// getComputedStyle on a live warm-themed document.
const WARM: Record<string, string> = {
  '--ab-bg-base': '10 10 10',
  '--ab-bg-elevated': '26 24 21',
  '--ab-border': '42 37 32',
  '--ab-text-primary': '236 229 212',
  '--ab-text-secondary': '184 176 160',
  '--ab-text-muted': '122 115 103',
  '--ab-gain': '123 168 123',
  '--ab-loss': '201 122 110',
  '--ab-warn': '212 166 83',
  '--ab-accent': '212 166 83',
};

describe('chartColorsFrom', () => {
  it('maps the warm triplets to the expected chart colors', () => {
    const c = chartColorsFrom((name) => WARM[name] ?? '');
    expect(c.background).toBe('rgb(10, 10, 10)');
    expect(c.upBody).toBe('rgb(123, 168, 123)');
    expect(c.downWick).toBe('rgb(201, 122, 110)');
    expect(c.volumeUp).toBe('rgba(123, 168, 123, 0.4)');
    expect(c.grid).toBe('rgba(236, 229, 212, 0.05)');
    expect(c.accent).toBe('rgb(212, 166, 83)');
  });

  it('a reader returning "" for everything still yields the warm defaults', () => {
    const c = chartColorsFrom(() => '');
    expect(c.background).toBe('rgb(10, 10, 10)');
    expect(c.upBody).toBe('rgb(123, 168, 123)');
    expect(c.downBody).toBe('rgb(201, 122, 110)');
    expect(c.volumeDown).toBe('rgba(201, 122, 110, 0.4)');
    expect(c.crosshair).toBe('rgba(184, 176, 160, 0.45)');
    expect(c.crosshairLabelBg).toBe('rgb(26, 24, 21)');
    expect(c.legendText).toBe('rgb(236, 229, 212)');
    expect(c.accent).toBe('rgb(212, 166, 83)');
  });

  it('reflects a different theme (midnight) rather than bleeding warm defaults', () => {
    const MIDNIGHT: Record<string, string> = {
      '--ab-gain': '92 208 168',
      '--ab-loss': '240 128 128',
      '--ab-bg-base': '11 22 40',
      '--ab-accent': '92 184 232',
    };
    const c = chartColorsFrom((name) => MIDNIGHT[name] ?? '');
    expect(c.background).toBe('rgb(11, 22, 40)');
    expect(c.upBody).toBe('rgb(92, 208, 168)');
    expect(c.downBody).toBe('rgb(240, 128, 128)');
    expect(c.volumeUp).toBe('rgba(92, 208, 168, 0.4)');
    expect(c.accent).toBe('rgb(92, 184, 232)');
  });
});

describe('chartLevelColor', () => {
  it('is the warm default gold when document is undefined (node test env)', () => {
    expect(chartLevelColor()).toBe('rgb(212, 166, 83)');
  });
});

// Mirrors chartColorsFrom's coverage above: a forgotten fallback here would
// leave the canvas measuring metrics for a font-family string that was never
// actually applied, not just a cosmetic mismatch (see chartFontsFrom's WHY
// comment in chartTheme.ts for why numbers and words are split at all).
describe('chartFontsFrom', () => {
  it('reads --mono and --sans', () => {
    const TOKENS: Record<string, string> = {
      '--mono': 'Berkeley Mono, ui-monospace, monospace',
      '--sans': 'Inter, ui-sans-serif, sans-serif',
    };
    const f = chartFontsFrom((name) => TOKENS[name] ?? '');
    expect(f.num).toBe('Berkeley Mono, ui-monospace, monospace');
    expect(f.ui).toBe('Inter, ui-sans-serif, sans-serif');
  });

  it('blank values fall back to the engine defaults', () => {
    const f = chartFontsFrom(() => '');
    expect(f.num).toBe('ui-monospace, Menlo, Consolas, monospace');
    expect(f.ui).toBe('ui-sans-serif, system-ui, -apple-system, sans-serif');
  });
});
