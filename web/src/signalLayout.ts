import type { ChartTf } from './types';
import { normalizeFollow, type FollowKind } from './signalFollow';
import { DEFAULT_SETUP, SIGNAL_SETUPS, SIGNAL_SIDES, type SetupKind, type SignalSide } from './signals/view';
import { DEFAULT_FLAG_PARAMS, type FlagParams } from './signals/emaFlag';
import { DEFAULT_DILDO_PARAMS, type DildoParams } from './signals/dildoPb';
import { DEFAULT_CLIMAX_PARAMS, type ClimaxParams } from './signals/climax';

// Pure layout state for the Signal window's grid (?pane=signal — addendum 2,
// "the Signal window shows MULTIPLE stocks"). Mirrors store.ts's
// makeGrid/normalizeGrid for the charts monitor almost exactly, but lives
// OUTSIDE the zustand store and persists to its own localStorage key instead
// of a server-persisted blob: the charts window's grid is shared across every
// open charts window (chartgrid.get/set, server-authoritative) because one
// trader watching one grid from two windows is a reasonable thing to want.
// The Signal window is different — each cell runs a stateful engine
// (shortFade.ts) against its own symbol, and the user asked for "multiple
// stocks", not "the same grid mirrored twice" — so a SECOND Signal window
// would only confuse which one is "the" signal grid. localStorage is shared
// by every scannerwin window regardless (one WebView2 profile — see
// GLOBAL_SYM_KEY's comment in store.ts), so two open Signal windows WOULD
// start from the same saved layout and then drift apart with no sync between
// them, same as any other localStorage-only preference in this app. That's a
// deliberate, documented limitation, not a bug: one Signal window is the
// supported setup, and unlike the charts grid, this one does not add a wire
// message to fix it.

// The Signal window offers only 1m/5m (no 15m — a short-fade top forms and
// invalidates faster than the charts monitor's slowest timeframe is useful
// for), so ChartTf's third value is simply never produced here, even though
// the type itself (shared with the charts grid) still allows it.
const SIGNAL_TFS: readonly ChartTf[] = ['1m', '5m'];

const SYM_RE = /^[A-Z][A-Z0-9.\-]{0,9}$/;

// `side` (addendum 4) is the cell's display selector — short (lower high),
// long (higher low) or both — see signals/view.ts. It is display-only: the
// engine computes both sides for every cell regardless. Default 'both', which
// is also what a layout saved before this field existed normalises to.
//
// `setup` (iteration 6) is the cell's ENGINE: 'flag' (the 9 EMA flag engine,
// emaFlag.ts — long only, the default) or 'pullback' (shortFade.ts, the
// previous engine, still selectable per cell). A cell with no saved setup — i.e.
// every cell of a layout persisted before this selector existed — normalises to
// 'flag': the user asked for the flag engine to become the window's default,
// and an old layout's cells follow it. `side` only means something for a
// pullback cell; a flag cell keeps whatever side it had (so flipping it back to
// pullback restores the old S/L/S+L choice) but ignores it.
export interface SignalCellDef {
  sym: string | null;
  tf: ChartTf;
  side: SignalSide;
  setup: SetupKind;
  follow?: FollowKind; // auto-switch to the symbol of each new alert of this kind (default off)
}

export interface SignalLayout {
  rows: number;
  cols: number;
  cells: SignalCellDef[]; // length rows*cols, row-major
  active: number; // index of the cell the detail panel currently reads
}

export const SIGNAL_LAYOUT_KEY = 'mktscanner.signal.layout.v1';

// Same two preset families, same reasoning, as ChartsPane's PRESETS /
// PORTRAIT_PRESETS: a portrait monitor has height to spare and width to
// protect, so its presets stack one or two narrow columns instead of
// squeezing a wide grid into a tall, narrow window. The Signal window tops
// out at 2×2 (not 3×3 like the charts monitor) — a signal cell carries a
// state strip as well as a chart, so four is already dense before text stops
// being legible.
export const SIGNAL_PRESETS = [
  { rows: 1, cols: 1, label: '1×1' },
  { rows: 1, cols: 2, label: '2×1' },
  { rows: 2, cols: 2, label: '2×2' },
] as const;

export const SIGNAL_PORTRAIT_PRESETS = [
  { rows: 2, cols: 1, label: '1×2' },
  { rows: 3, cols: 1, label: '1×3' },
  { rows: 4, cols: 1, label: '1×4' },
] as const;

// makeSignalLayout builds a rows×cols cell array, preserving whatever prior
// cells still fit (flat row-major index) so shrinking/growing the preset
// doesn't wipe assignments that survive the resize — same as store.ts's
// makeGrid. active is clamped into the new cell count (a cell that no longer
// exists can't stay "active").
export function makeSignalLayout(rows: number, cols: number, prev?: SignalCellDef[], active?: number): SignalLayout {
  const n = Math.max(1, rows * cols);
  const cells: SignalCellDef[] = [];
  for (let i = 0; i < n; i++) {
    const p = prev?.[i];
    // `?? 'both'` / `?? DEFAULT_SETUP` are defensive only: a caller holding
    // cells built before the side / setup fields existed (or a hand-written
    // literal) still gets a valid cell. New cells run the flag engine.
    cells.push(p ? { ...p, side: p.side ?? 'both', setup: p.setup ?? DEFAULT_SETUP } : { sym: null, tf: '1m', side: 'both', setup: DEFAULT_SETUP });
  }
  const a = active ?? 0;
  return { rows, cols, cells, active: Math.min(Math.max(0, a), n - 1) };
}

// normalizeSignalLayout validates a blob read back from localStorage. A
// foreign/old/corrupt shape degrades to null (caller falls back to a fresh
// 1×1 layout) rather than crashing the window on launch.
export function normalizeSignalLayout(raw: unknown): SignalLayout | null {
  if (!raw || typeof raw !== 'object') return null;
  const o = raw as { rows?: unknown; cols?: unknown; cells?: unknown; active?: unknown };
  const rows = typeof o.rows === 'number' && Number.isFinite(o.rows) ? Math.min(4, Math.max(1, Math.floor(o.rows))) : 0;
  const cols = typeof o.cols === 'number' && Number.isFinite(o.cols) ? Math.min(4, Math.max(1, Math.floor(o.cols))) : 0;
  if (!rows || !cols) return null;
  const rawCells = Array.isArray(o.cells) ? o.cells : [];
  const cells: SignalCellDef[] = [];
  for (let i = 0; i < rows * cols; i++) {
    const c = rawCells[i] as { sym?: unknown; tf?: unknown; side?: unknown; setup?: unknown; follow?: unknown } | undefined;
    const symRaw = c && typeof c.sym === 'string' ? c.sym.trim().toUpperCase() : '';
    const sym = SYM_RE.test(symRaw) ? symRaw : null;
    const tf = c && typeof c.tf === 'string' && (SIGNAL_TFS as readonly string[]).includes(c.tf) ? (c.tf as ChartTf) : '1m';
    const side = c && typeof c.side === 'string' && (SIGNAL_SIDES as readonly string[]).includes(c.side) ? (c.side as SignalSide) : 'both';
    // Missing (a layout from before the setup selector) or unknown -> the flag engine.
    const setup = c && typeof c.setup === 'string' && (SIGNAL_SETUPS as readonly string[]).includes(c.setup) ? (c.setup as SetupKind) : DEFAULT_SETUP;
    const follow = normalizeFollow(c?.follow);
    cells.push(follow === 'off' ? { sym, tf, side, setup } : { sym, tf, side, setup, follow });
  }
  const activeRaw = typeof o.active === 'number' && Number.isFinite(o.active) ? Math.floor(o.active) : 0;
  const active = Math.min(Math.max(0, activeRaw), cells.length - 1);
  return { rows, cols, cells, active };
}

export function loadSignalLayout(): SignalLayout {
  try {
    const raw = localStorage.getItem(SIGNAL_LAYOUT_KEY);
    const parsed = raw ? normalizeSignalLayout(JSON.parse(raw)) : null;
    return parsed ?? makeSignalLayout(1, 1);
  } catch {
    return makeSignalLayout(1, 1); // private-mode / quota errors / corrupt JSON -> fresh default
  }
}

export function saveSignalLayout(l: SignalLayout): void {
  try {
    localStorage.setItem(SIGNAL_LAYOUT_KEY, JSON.stringify(l));
  } catch {
    /* best-effort */
  }
}

// layoutSyms is the deduped symbol set across every cell, first-appearance
// (row-major) order — feeds chart.watch and the store's signalSyms.
export function layoutSyms(l: SignalLayout): string[] {
  const out: string[] = [];
  for (const c of l.cells) if (c.sym && !out.includes(c.sym)) out.push(c.sym);
  return out;
}

// ─── The flag engine's Tuning params (iteration 6) ───────────────────────
//
// One set for every flag cell, persisted under its own key next to the layout
// (the pullback engine's params stay under SignalPane's own PARAMS_KEY). Kept
// here, pure and tested, rather than inside the React pane: the engine takes
// these straight into its arithmetic, so what a stale, hand-edited or foreign
// blob is allowed to put in them deserves a test.
export const FLAG_PARAMS_KEY = 'mktscanner.signal.flagParams.v1';

// The bar-count / length params: whole numbers, at least 1 (an EMA length or a
// flat-bar count of 0, or of 2.5, would silently turn the engine's arithmetic
// into NaNs). Every other numeric param may be any finite number >= 0 (maxAdds
// additionally a whole number). The ranges mirror the Tuning drawer's own
// (SignalPanel's FLAG_NUMERIC).
const FLAG_WHOLE_KEYS: ReadonlySet<string> = new Set(['emaPeriod', 'atrPeriod', 'breakoutLookback', 'impulseBaseBars', 'minFlatBars']);

// Params that are a FRACTION, at most 1: the 9 EMA SLAM's close-in-range (0.35 = the close sits in the lowest 35% of
// the bar's range). Over 1 the test could never fail, which is a typo, not a setting — it falls back to the default.
const FLAG_FRACTION_KEYS: ReadonlySet<string> = new Set(['slamCloseInRange']);

/**
 * Merge a persisted blob over DEFAULT_FLAG_PARAMS. Only keys the engine knows
 * are kept (nothing foreign can smuggle in a field), and a value of the wrong
 * TYPE or outside its range falls back to the default for that one field — a
 * half-written or old blob degrades field by field instead of poisoning the
 * engine or crashing the window. A blob saved before a param existed (the 9 EMA
 * SLAM's `slam` / `slam*`, 2026-10-07) simply takes that param's default.
 */
export function normalizeFlagParams(raw: unknown): FlagParams {
  const out: FlagParams = { ...DEFAULT_FLAG_PARAMS };
  if (!raw || typeof raw !== 'object') return out;
  const src = raw as Record<string, unknown>;
  const dst = out as unknown as Record<string, unknown>;
  for (const k of Object.keys(DEFAULT_FLAG_PARAMS)) {
    const v = src[k];
    if (typeof v !== typeof dst[k]) continue;
    if (typeof v === 'number') {
      if (!Number.isFinite(v) || v < 0) continue;
      if ((FLAG_WHOLE_KEYS.has(k) || k === 'maxAdds') && !Number.isInteger(v)) continue;
      if (FLAG_WHOLE_KEYS.has(k) && v < 1) continue;
      if (FLAG_FRACTION_KEYS.has(k) && v > 1) continue;
    }
    dst[k] = v;
  }
  return out;
}

export function loadFlagParams(): FlagParams {
  try {
    const raw = localStorage.getItem(FLAG_PARAMS_KEY);
    return normalizeFlagParams(raw ? JSON.parse(raw) : null);
  } catch {
    return { ...DEFAULT_FLAG_PARAMS }; // private-mode / corrupt JSON -> the defaults
  }
}

export function saveFlagParams(p: FlagParams): void {
  try {
    localStorage.setItem(FLAG_PARAMS_KEY, JSON.stringify(p));
  } catch {
    /* best-effort */
  }
}

// ─── The GREEN DILDO and VOLUME CLIMAX chart params (2026-10-07) ─────────
//
// The two alert rules (signals/dildoPb.ts, signals/climax.ts) also draw their
// markers on every 1-minute flag cell. Their Tuning edits change the CHART
// MARKERS ONLY (the climax; the dildo set is server-owned, see below) and each set persists under its own key, validated and
// merged field by field like the flag params above. There are no fraction keys:
// every value is a percent, a multiple, a count, a price or ET minutes.
export const DILDO_PARAMS_KEY = 'mktscanner.signal.dildoParams.v1';
export const CLIMAX_PARAMS_KEY = 'mktscanner.signal.climaxParams.v1';

interface ParamRules {
  whole: ReadonlySet<string>; // whole numbers ...
  minOne: ReadonlySet<string>; // ... of which these are at least 1 (lengths, counts that divide)
  max?: Readonly<Record<string, number>>; // inclusive upper bounds
  choices?: Readonly<Record<string, readonly string[]>>; // string params: one of these
}

const DILDO_RULES: ParamRules = {
  whole: new Set(['priorBars', 'minPriorBars', 'pbWindowMin', 'emaPeriod']),
  minOne: new Set(['priorBars', 'pbWindowMin', 'emaPeriod']),
  max: { maxTopPct: 100 }, // parity with dildopb.Parse (Go)
  choices: { floor: ['mid', 'open'] },
};

const CLIMAX_RULES: ParamRules = {
  whole: new Set(['lookback', 'emaPeriod', 'muteStartMin', 'muteEndMin', 'confirmBars']),
  minOne: new Set(['lookback', 'emaPeriod', 'confirmBars']),
  max: { muteStartMin: 1440, muteEndMin: 1440 },
};

/**
 * Merge a persisted blob over `defaults`: only known keys, the right TYPE, a
 * finite number >= 0 within the key's rules — anything else falls back to the
 * default for that one field (a half-written or foreign blob degrades field by
 * field instead of poisoning the rule's arithmetic or crashing the window).
 */
function normalizeRuleParams<P extends object>(defaults: P, raw: unknown, rules: ParamRules): P {
  const out = { ...defaults };
  if (!raw || typeof raw !== 'object') return out;
  const src = raw as Record<string, unknown>;
  const dst = out as unknown as Record<string, unknown>;
  for (const k of Object.keys(defaults)) {
    const v = src[k];
    if (typeof v !== typeof dst[k]) continue;
    if (typeof v === 'number') {
      if (!Number.isFinite(v) || v < 0) continue;
      if (rules.whole.has(k) && !Number.isInteger(v)) continue;
      if (rules.minOne.has(k) && v < 1) continue;
      const max = rules.max?.[k];
      if (max !== undefined && v > max) continue;
    }
    if (typeof v === 'string' && !(rules.choices?.[k] ?? []).includes(v)) continue;
    dst[k] = v;
  }
  return out;
}

export function normalizeDildoParams(raw: unknown): DildoParams {
  return normalizeRuleParams(DEFAULT_DILDO_PARAMS, raw, DILDO_RULES);
}

export function normalizeClimaxParams(raw: unknown): ClimaxParams {
  return normalizeRuleParams(DEFAULT_CLIMAX_PARAMS, raw, CLIMAX_RULES);
}

function loadKey<P>(key: string, normalize: (raw: unknown) => P): P {
  try {
    const raw = localStorage.getItem(key);
    return normalize(raw ? JSON.parse(raw) : null);
  } catch {
    return normalize(null); // private-mode / corrupt JSON -> the defaults
  }
}

function saveKey(key: string, value: unknown): void {
  try {
    localStorage.setItem(key, JSON.stringify(value));
  } catch {
    /* best-effort */
  }
}

export const loadClimaxParams = (): ClimaxParams => loadKey(CLIMAX_PARAMS_KEY, normalizeClimaxParams);
export const saveClimaxParams = (p: ClimaxParams): void => saveKey(CLIMAX_PARAMS_KEY, p);

// One shared, subscribable copy for the window (React's useSyncExternalStore
// reads it): every SignalCell evaluates with it and SignalPanel's Tuning edits
// it, without the pane having to thread two more param props through. The
// snapshot object is replaced (never mutated) on a change, so it is stable
// between changes, as useSyncExternalStore requires.
export interface SignalRuleParams {
  dildo: DildoParams;
  climax: ClimaxParams;
}

let ruleParams: SignalRuleParams | null = null;
const ruleListeners = new Set<() => void>();

export function getSignalRuleParams(): SignalRuleParams {
  if (!ruleParams) ruleParams = { dildo: DEFAULT_DILDO_PARAMS, climax: loadClimaxParams() };
  return ruleParams;
}

export function subscribeSignalRuleParams(fn: () => void): () => void {
  ruleListeners.add(fn);
  return () => {
    ruleListeners.delete(fn);
  };
}

function publishRuleParams(next: SignalRuleParams): void {
  ruleParams = next;
  for (const fn of [...ruleListeners]) fn();
}

// The GREEN DILDO tuning is SERVER-OWNED (2026-10-20): the same set drives the
// chart markers and the market-wide alert (engine judge + flagscan). The panel
// edits optimistically, then sends dildo.params.set after a quiet 500 ms; the
// server's dildo.params.data broadcast (applyServerDildoParams) is the truth
// for every window. The old localStorage value (DILDO_PARAMS_KEY) is ignored.
// ws.ts registers the sender (this module stays free of the socket).
export const DILDO_SEND_DEBOUNCE_MS = 500;
let dildoSender: ((p: DildoParams) => void) | null = null;
let dildoTimer: ReturnType<typeof setTimeout> | null = null;
let dildoPending: DildoParams | null = null;

export function registerDildoParamsSender(fn: ((p: DildoParams) => void) | null): void {
  dildoSender = fn;
}

function flushDildoParams(): void {
  dildoTimer = null;
  const p = dildoPending;
  dildoPending = null;
  if (p && dildoSender) dildoSender(p);
}

/** A panel edit: applied locally at once, sent to the server debounced. */
export function setDildoParams(p: DildoParams): void {
  const dildo = normalizeDildoParams(p);
  publishRuleParams({ ...getSignalRuleParams(), dildo });
  dildoPending = dildo;
  if (dildoTimer) clearTimeout(dildoTimer);
  dildoTimer = setTimeout(flushDildoParams, DILDO_SEND_DEBOUNCE_MS);
}

/** The panel's "Reset to defaults" for the green dildo. */
export function resetDildoParams(): void {
  setDildoParams(DEFAULT_DILDO_PARAMS);
}

/** dildo.params.data from the server. Skipped while a local edit is still waiting to be sent (it would bounce the user's typing back). */
export function applyServerDildoParams(raw: unknown): void {
  if (dildoPending) return;
  const dildo = normalizeDildoParams(raw);
  const cur = getSignalRuleParams().dildo;
  if (JSON.stringify(cur) === JSON.stringify(dildo)) return;
  publishRuleParams({ ...getSignalRuleParams(), dildo });
}

export function setClimaxParams(p: ClimaxParams): void {
  const climax = normalizeClimaxParams(p);
  saveClimaxParams(climax);
  publishRuleParams({ ...getSignalRuleParams(), climax });
}
