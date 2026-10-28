//! Chart typography — the one place every render layer gets its font from.
//
// The engine draws two kinds of text: numbers (axis ticks, crosshair chips,
// OHLC legend values, drawing readouts) and words (marker labels, drawing
// labels, the legend title). The host sets the families through
// `ChartOptions.fonts` so the chart uses the same type as the app around it;
// the defaults only stand in for an embed that never sets them.

import { DEFAULT_FONTS, type ChartFonts } from '../core/types';

const fonts: ChartFonts = { ...DEFAULT_FONTS };

export function setChartFonts(next: Partial<ChartFonts>): void {
  if (next.num) fonts.num = next.num;
  if (next.ui) fonts.ui = next.ui;
}

/** A canvas `ctx.font` string: `font(11)` for numbers, `font(11, 'ui', '600')`
 *  for a semibold label. */
export function font(px: number, kind: keyof ChartFonts = 'num', weight = ''): string {
  return `${weight ? weight + ' ' : ''}${px}px ${fonts[kind]}`;
}
