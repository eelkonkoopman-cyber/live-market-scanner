// AfterBell's chart color mapping (src/lib/components/chart/chartTheme.ts +
// src/lib/utils/themeColors.ts), ported to read the scanner's OWN `--ab-*`
// tokens (afterbell-themes.css) instead of Blackthorn's app.css tokens
// (--bg-base/--gain/...) — identical semantics and identical fallback
// triplets, so the vendored chart engine repaints exactly like it does in
// the app it was built for.
//
// ChartColors/ChartFonts come from the vendored engine as TYPE-ONLY imports
// (never a value) so this mapping can never silently drift out of sync with
// what the engine actually expects, while this module — and its vitest
// suite, which runs in vitest's node environment — never pulls in the
// engine's canvas/DOM-heavy runtime just to learn the shape of an object.
import type { ChartColors, ChartFonts } from './vendor/blackthorn-charts';

// Warm-theme fallback triplets, copied from AfterBell's DEFAULT_TRIPLETS
// (src/lib/utils/themeColors.ts) for the subset of tokens the chart uses.
// Used whenever a `--ab-*` var is missing or blank (no document in tests,
// afterbell-themes.css not yet loaded, or a hand-edited page with the token
// removed) so the chart never renders unstyled or invisible.
const DEFAULT_TRIPLETS: Record<string, string> = {
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

// '10 10 10' -> 'rgb(10, 10, 10)'; with alpha -> 'rgba(10, 10, 10, 0.4)'.
// trim + split on runs of whitespace so an extra space or two in a computed
// CSS value (or a hand-typed triplet) doesn't leave stray characters in the
// output.
export function tripletToRgb(triplet: string, alpha?: number): string {
  const [r, g, b] = triplet.trim().split(/\s+/);
  return alpha == null ? `rgb(${r}, ${g}, ${b})` : `rgba(${r}, ${g}, ${b}, ${alpha})`;
}

// Reads one token via `read`, falling back to the warm default triplet when
// the theme's own stylesheet doesn't define it (or returns '').
function tripletOf(read: (name: string) => string, name: string): string {
  return read(name).trim() || DEFAULT_TRIPLETS[name];
}

// Builds the full color set from any triplet reader — the live DOM
// (chartColors(), below) or a plain object (tests). Mapping is identical to
// AfterBell's blackthornChartColors: background/grid/axis derive from
// bg-base/text-primary/text-muted/border, candles from gain/loss, and so on.
export function chartColorsFrom(read: (name: string) => string): ChartColors {
  const t = (name: string) => tripletOf(read, name);
  return {
    background: tripletToRgb(t('--ab-bg-base')),
    grid: tripletToRgb(t('--ab-text-primary'), 0.05),
    axisText: tripletToRgb(t('--ab-text-muted')),
    axisLine: tripletToRgb(t('--ab-border')),
    upBody: tripletToRgb(t('--ab-gain')),
    upWick: tripletToRgb(t('--ab-gain')),
    downBody: tripletToRgb(t('--ab-loss')),
    downWick: tripletToRgb(t('--ab-loss')),
    volumeUp: tripletToRgb(t('--ab-gain'), 0.4),
    volumeDown: tripletToRgb(t('--ab-loss'), 0.4),
    crosshair: tripletToRgb(t('--ab-text-secondary'), 0.45),
    crosshairLabelBg: tripletToRgb(t('--ab-bg-elevated')),
    buyMarker: tripletToRgb(t('--ab-gain')),
    sellMarker: tripletToRgb(t('--ab-loss')),
    legendText: tripletToRgb(t('--ab-text-primary')),
    // AfterBell's own chart theme maps accent the same way (a direct token
    // passthrough, no derived alpha) — placement previews and focus rings.
    accent: tripletToRgb(t('--ab-accent')),
  };
}

// Live read from the active theme's computed styles. SSR/test-safe: with no
// `document` (node test environment), falls straight to the warm defaults —
// mirrors readThemeColors()'s SSR fallback in AfterBell. Call fresh on each
// chart build / theme change so the chart re-colors live; nothing here
// caches a stale snapshot.
export function chartColors(): ChartColors {
  if (typeof document === 'undefined') return chartColorsFrom(() => '');
  const style = getComputedStyle(document.documentElement);
  return chartColorsFrom((name) => style.getPropertyValue(name));
}

// Prev-close / reference-line color. Kept separate from ChartColors (rather
// than folded into it) because AfterBell's chart engine has no such field —
// the scanner's prev-close line is its own feature. Deliberately reuses
// --ab-warn (gold in 5 of 6 themes) rather than a gain/loss color so the
// level line stays visually distinct from the candles even in Forest, where
// the THEME ACCENT is sage — close enough to the gain color that an
// accent-colored line would blend into up-candles.
export function chartLevelColor(): string {
  if (typeof document === 'undefined') return tripletToRgb(DEFAULT_TRIPLETS['--ab-warn']);
  const raw = getComputedStyle(document.documentElement).getPropertyValue('--ab-warn');
  return tripletToRgb(raw.trim() || DEFAULT_TRIPLETS['--ab-warn']);
}

// String-constant mirrors of the engine's own DEFAULT_FONTS (core/types.ts).
// Duplicated as literals — rather than importing that runtime value — so this
// module stays import-type-only against the engine (see the top-of-file
// comment); these only ever surface as a last-resort fallback anyway, when
// the scanner's own --mono/--sans tokens are missing.
const NUM_FALLBACK = 'ui-monospace, Menlo, Consolas, monospace';
const UI_FALLBACK = 'ui-sans-serif, system-ui, -apple-system, sans-serif';

// Builds the font set from any reader — the live DOM (chartFonts(), below)
// or a plain object (tests). Numbers (axis ticks, the
// crosshair chip, the OHLC legend) stay on the app's monospace stack: canvas
// text can't use tabular-figure OpenType features, so proportional digits
// would make those values jitter sideways bar-to-bar as digit widths change.
// Words (drawing/marker labels, the legend title) use the app's own UI
// typeface instead — the same "chart type matches the app around it" idea as
// AfterBell's blackthornChartFonts.
export function chartFontsFrom(read: (name: string) => string): ChartFonts {
  return {
    num: read('--mono').trim() || NUM_FALLBACK,
    ui: read('--sans').trim() || UI_FALLBACK,
  };
}

// Live read from the active theme's computed styles. SSR/test-safe: with no
// `document` (node test environment), falls straight to the fallback stack —
// mirrors chartColors()'s SSR fallback above. Call fresh on each chart build
// (and once more after web fonts finish loading) so the chart never keeps
// measuring the pre-load fallback metrics.
export function chartFonts(): ChartFonts {
  if (typeof document === 'undefined') return chartFontsFrom(() => '');
  const style = getComputedStyle(document.documentElement);
  return chartFontsFrom((name) => style.getPropertyValue(name));
}
