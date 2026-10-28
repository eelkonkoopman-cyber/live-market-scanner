// AfterBell's six theme palettes, ported to the scanner. Theming itself is
// pure CSS — afterbell-themes.css defines the six `[data-theme]` blocks of
// `--ab-*` tokens and styles.css derives the app's semantic tokens (--bg,
// --fg, --accent, ...) from them (see chartTheme.ts for the chart engine's
// own reader). This module only knows the theme IDS/metadata and how to
// apply, persist, and broadcast the active one — no CSS lives here.
//
// Global across every window (main + every pop-out `?pane=` window), UNLIKE
// per-window scan mutes (see store.ts's mutedScans comment): a "mute scan X"
// preference must stay local to one window because that's a per-window
// setting by nature, but a THEME is a display preference for the person
// sitting at the monitors — every window sharing this browser profile
// should paint the same palette. So this follows the same cross-window
// pattern as store.ts's GLOBAL_SYM_KEY: write to localStorage, and every
// OTHER window picks the change up via the `storage` event (which the
// browser never fires in the window that wrote the value, so there's no
// feedback loop) — see initTheme() below.
//
// AfterBell itself (a separate app, different origin — D:\Blackthorn Live)
// persists its theme choice in a cookie. This app cannot read that cookie
// even when both happen to run on the same machine: cookies and
// localStorage are partitioned per-origin by the browser, not shared just
// because one person runs two apps. So the scanner keeps its own
// independent setting, under its own key, defaulting to the same theme
// ('warm') AfterBell ships with — that keeps first impressions consistent
// across the two apps without either one actually reading the other's data.
export type ThemeId = 'warm' | 'paper' | 'slate' | 'midnight' | 'forest' | 'plum';

export interface ThemeDef {
  id: ThemeId;
  name: string;
  description: string;
  swatches: [string, string, string, string];
  light: boolean;
}

// Copied verbatim from AfterBell's settings-page THEMES list
// (src/routes/settings/+page.svelte, near line 164) — same order, names,
// descriptions, and swatches, so the two apps' pickers read as one system.
export const THEMES: ThemeDef[] = [
  {
    id: 'warm',
    name: 'AfterBell Dark',
    description: 'Warm cream on near-black with a gold accent. Brand default.',
    swatches: ['#0a0a0a', '#1a1815', '#ece5d4', '#d4a653'],
    light: false,
  },
  {
    id: 'paper',
    name: 'AfterBell Bright',
    description: 'Warm cream paper with deep ink. Bright theme for daytime sessions.',
    swatches: ['#f6f1e7', '#fbf8f0', '#1a1815', '#b8860b'],
    light: true,
  },
  {
    id: 'slate',
    name: 'AfterBell Slate',
    description: 'The original v0.1.x cool slate palette. Pairs with the same gold accent.',
    swatches: ['#1c2430', '#3a4757', '#f2f4f6', '#d4af37'],
    light: false,
  },
  {
    id: 'midnight',
    name: 'Midnight',
    description: 'Deep saturated navy with an ice-blue accent. Bloomberg-terminal vibe.',
    swatches: ['#0b1628', '#1d3052', '#e6f0ff', '#5cb8e8'],
    light: false,
  },
  {
    id: 'forest',
    name: 'Forest',
    description: 'Green-black with a sage accent. Calming for long sessions.',
    swatches: ['#0a1410', '#152a1f', '#e8efde', '#87a96b'],
    light: false,
  },
  {
    id: 'plum',
    name: 'Plum',
    description: 'Aubergine-black with a rose-magenta accent. Distinctive and editorial.',
    swatches: ['#14101e', '#2a2240', '#f0e8f4', '#c97aa6'],
    light: false,
  },
];

export const DEFAULT_THEME: ThemeId = 'warm';

// Raw id string, not JSON — mirrors GLOBAL_SYM_KEY's plain-string storage in
// store.ts (there's nothing to parse), unlike PaneSizes' JSON blob.
export const THEME_KEY = 'mktscanner.theme.v1';

const THEME_IDS = new Set<string>(THEMES.map((t) => t.id));

export function isThemeId(x: unknown): x is ThemeId {
  return typeof x === 'string' && THEME_IDS.has(x);
}

export function themeDef(id: ThemeId): ThemeDef {
  // THEMES always has exactly one entry per ThemeId, so this is safe; typing
  // it through .find() would otherwise force a bogus `| undefined` on every
  // caller for a case that can't actually happen.
  return THEMES.find((t) => t.id === id) as ThemeDef;
}

// try/catch: private-mode / storage-quota throws must not crash the app over
// a cosmetic preference — same defensive shape as store.ts's loadLayout.
// Garbage or absent -> DEFAULT_THEME, never a partial/invalid theme.
export function readStoredTheme(): ThemeId {
  try {
    const raw = localStorage.getItem(THEME_KEY);
    return isThemeId(raw) ? raw : DEFAULT_THEME;
  } catch {
    return DEFAULT_THEME;
  }
}

// Module-level "current theme", initialised once from storage (or the
// default, in tests/node where there's no localStorage). Kept in sync by
// setTheme() (this window) and the storage listener installed by initTheme()
// (other windows). A plain synchronous variable — not React state — because
// non-React code (chartColors(), the chart components' effects) needs to
// read it too; useTheme.ts is the thin React binding over the top.
let current: ThemeId = readStoredTheme();

export function getTheme(): ThemeId {
  return current;
}

function applyTheme(id: ThemeId): void {
  if (typeof document === 'undefined') return; // node test environment: no-op
  document.documentElement.dataset.theme = id;
}

type Listener = (id: ThemeId) => void;
const listeners = new Set<Listener>();

// Sets the theme for THIS window: applies it to <html>, persists it (so
// every OTHER window picks it up below, and so a future load of THIS window
// starts painted the same way), and notifies this window's own subscribers
// directly. That direct notify is necessary, not redundant with the storage
// listener: the browser's `storage` event fires only in OTHER tabs/windows,
// never the one that called localStorage.setItem, so useTheme() in this
// window would otherwise never see its own change.
export function setTheme(id: ThemeId): void {
  current = id;
  applyTheme(id);
  try {
    localStorage.setItem(THEME_KEY, id);
  } catch {
    /* best-effort, mirrors store.ts's saveGlobalSym */
  }
  for (const cb of listeners) cb(current);
}

export function subscribeTheme(cb: (id: ThemeId) => void): () => void {
  listeners.add(cb);
  return () => listeners.delete(cb);
}

let installed = false;

// Applies whatever is currently in storage (index.html's inline pre-paint
// script already set the DOM attribute before this module even loaded, but
// not this module's own `current` variable, and storage could in principle
// have changed between that inline script and this call) and installs the
// cross-window `storage` listener exactly once. Safe to call more than once
// — e.g. if a future pop-out entry point calls it independently of
// main.tsx's own call — the `installed` guard is checked (and only set)
// AFTER confirming `window` exists, so a node/test call never marks it
// installed without actually attaching anything.
export function initTheme(): void {
  current = readStoredTheme();
  applyTheme(current);
  if (installed || typeof window === 'undefined') return;
  installed = true;
  window.addEventListener('storage', (e: StorageEvent) => {
    // Only react to OUR key. e.newValue is null on a cleared key
    // (localStorage.clear() or removeItem) which is never a valid theme, so
    // isThemeId's check conveniently rejects it as garbage rather than
    // needing a separate null guard.
    if (e.key !== THEME_KEY || !isThemeId(e.newValue)) return;
    current = e.newValue;
    applyTheme(current);
    for (const cb of listeners) cb(current);
  });
}
