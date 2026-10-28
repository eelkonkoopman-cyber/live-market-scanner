// The SIGNALS list's badge colours as the CSS resolves them in each of the six
// AfterBell themes (UI-1): the DILDO badge ('info' tone) must be a hue of its
// own - apart from the gold warn badges (CLIMAX, READY, EARLY), the green pos
// ones (9EMA PB, BOTTOM, LONG) and the red neg ones - and legible on the
// panel. warm (the default) and paper set accent == warn, so an accent-based
// 'info' badge came out the same gold as CLIMAX there. Reads styles.css and
// afterbell-themes.css and resolves the var() chains itself (no DOM).
import { describe, expect, it } from 'vitest';
import { kindBadge } from './flagEvents';

interface NodeFs {
  readFileSync(path: URL, encoding: 'utf8'): string;
}
const fsSpec = 'node:fs';
const fs = (await import(/* @vite-ignore */ fsSpec)) as NodeFs;

const read = (f: string) => fs.readFileSync(new URL(`./${f}`, import.meta.url), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');
const STYLES = read('styles.css');
const THEMES_CSS = read('afterbell-themes.css');

type Decls = Record<string, string>;

function decls(body: string): Decls {
  const out: Decls = {};
  for (const m of body.matchAll(/([\w-]+)\s*:\s*([^;]+);/g)) out[m[1]!] = m[2]!.trim();
  return out;
}

// The declarations of the first rule whose selector (or the last selector of
// its list) is exactly `selector`, starting a line.
function rule(css: string, selector: string): Decls {
  const esc = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const m = new RegExp(`(?:^|[}\\n])[ \\t]*${esc}\\s*\\{([^}]*)\\}`).exec(css);
  return m ? decls(m[1]!) : {};
}

const THEMES = ['warm', 'slate', 'midnight', 'forest', 'plum', 'paper'] as const;
const ROOT = rule(STYLES, ':root');

type Rgb = [number, number, number];

// The rgb triplet a property of `.flagev-badge.<tone>` resolves to in `theme`.
function badgeColor(theme: string, tone: string, prop: 'color' | 'border-color' | 'background'): Rgb {
  const scope: Decls = {
    ...rule(THEMES_CSS, `[data-theme='${theme}']`),
    ...ROOT,
    ...rule(STYLES, `.flagev-badge.${tone}`),
    ...rule(STYLES, `[data-theme='${theme}'] .flagev-badge.${tone}`),
  };
  let v = scope[prop] ?? '';
  for (let i = 0; i < 10 && v.includes('var('); i++) {
    v = v.replace(/var\((--[\w-]+)\)/g, (_, n: string) => scope[n] ?? '');
  }
  const m = /rgb\(\s*(\d+)\s+(\d+)\s+(\d+)/.exec(v);
  if (!m) throw new Error(`${theme} .flagev-badge.${tone} ${prop}: unresolved '${scope[prop]}' -> '${v}'`);
  return [Number(m[1]), Number(m[2]), Number(m[3])];
}

function themeRgb(theme: string, token: string): Rgb {
  const v = rule(THEMES_CSS, `[data-theme='${theme}']`)[token] ?? '';
  const n = v.split(/\s+/).map(Number);
  if (n.length !== 3 || n.some(Number.isNaN)) throw new Error(`${theme} ${token}: '${v}'`);
  return n as Rgb;
}

const dist = (a: Rgb, b: Rgb) => Math.hypot(a[0] - b[0], a[1] - b[1], a[2] - b[2]);

function luminance([r, g, b]: Rgb): number {
  const lin = (c: number) => {
    const s = c / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}

const contrast = (a: Rgb, b: Rgb) => {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x) as [number, number];
  return (hi + 0.05) / (lo + 0.05);
};

describe('SIGNALS badge colours per theme', () => {
  it('DILDO is the info tone', () => {
    expect(kindBadge('dildo')).toEqual({ text: 'DILDO', tone: 'info' });
    expect(kindBadge('climax').tone).toBe('warn');
  });

  for (const theme of THEMES) {
    it(`${theme}: DILDO stands apart from the warn / pos / neg badges and reads on the panel`, () => {
      for (const prop of ['color', 'border-color', 'background'] as const) {
        const info = badgeColor(theme, 'info', prop);
        for (const other of ['warn', 'pos', 'neg']) {
          const d = dist(info, badgeColor(theme, other, prop));
          expect(d, `${prop} vs ${other}: ${info} vs ${badgeColor(theme, other, prop)}`).toBeGreaterThanOrEqual(80);
        }
      }
      const text = badgeColor(theme, 'info', 'color');
      expect(contrast(text, themeRgb(theme, '--ab-bg-surface'))).toBeGreaterThanOrEqual(4.5);
    });
  }
});
