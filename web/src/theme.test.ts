// Regression coverage for the theme store (theme.ts). Node test environment
// (see vite.config default) means there is no `document`/`localStorage` —
// exactly the environment this module is designed to survive, so these
// tests double as proof the try/catch fallbacks actually work rather than
// merely existing. Mirrors summary.display.test.ts's style.
import { describe, expect, it, vi } from 'vitest';
import { DEFAULT_THEME, THEMES, getTheme, isThemeId, readStoredTheme, setTheme, subscribeTheme } from './theme';

describe('THEMES', () => {
  it('has 6 unique ids in the AfterBell settings-page order', () => {
    expect(THEMES.map((t) => t.id)).toEqual(['warm', 'paper', 'slate', 'midnight', 'forest', 'plum']);
    expect(new Set(THEMES.map((t) => t.id)).size).toBe(6);
  });

  it('exactly one theme is light (paper)', () => {
    expect(THEMES.filter((t) => t.light).map((t) => t.id)).toEqual(['paper']);
  });
});

describe('isThemeId', () => {
  it('accepts every real theme id', () => {
    for (const t of THEMES) expect(isThemeId(t.id)).toBe(true);
  });

  it('rejects garbage', () => {
    expect(isThemeId('nope')).toBe(false);
    expect(isThemeId('')).toBe(false);
    expect(isThemeId(undefined)).toBe(false);
    expect(isThemeId(null)).toBe(false);
    expect(isThemeId(42)).toBe(false);
  });
});

// These two run before the setTheme/subscribeTheme block below so the
// module-level `current` variable is still at its just-initialised value —
// vitest runs a file's tests top-to-bottom in one module instance, so
// ordering here is what keeps this a real assertion about the initial state
// rather than a coincidence of whatever the last test left behind.
describe('reads with no localStorage available', () => {
  it('readStoredTheme falls back to the default', () => {
    expect(readStoredTheme()).toBe(DEFAULT_THEME);
  });

  it('getTheme starts at the default too', () => {
    expect(getTheme()).toBe(DEFAULT_THEME);
  });
});

describe('setTheme / subscribeTheme', () => {
  it('notifies subscribers with the new id, and unsubscribe stops further calls', () => {
    const cb = vi.fn();
    const unsubscribe = subscribeTheme(cb);

    setTheme('midnight'); // no `document` in this env — applying the theme just no-ops
    expect(cb).toHaveBeenCalledTimes(1);
    expect(cb).toHaveBeenCalledWith('midnight');
    expect(getTheme()).toBe('midnight');

    unsubscribe();
    setTheme('forest');
    expect(cb).toHaveBeenCalledTimes(1); // unchanged: unsubscribe actually stopped delivery
    expect(getTheme()).toBe('forest'); // setTheme itself still took effect
  });
});
