import { EMA_VWAP_SCAN_IDS } from './e9';
import type { SetupKind } from './signals/view';

// Per-chart "Follow" selector: a Signal-window chart pane that follows an alert
// switches its symbol whenever that alert fires (ws.ts alert case).
export type FollowKind = 'off' | 'dildo' | 'hod' | 'flag' | 'vwap' | 'power' | 'powerSetup' | 'volBreak';
export const FOLLOW_KINDS: readonly FollowKind[] = ['off', 'dildo', 'power', 'powerSetup', 'volBreak', 'hod', 'flag', 'vwap'];
export const FOLLOW_LABELS: Record<FollowKind, string> = { off: 'Off', dildo: 'Dildo', hod: 'HOD flag', flag: 'Every flag', vwap: 'VWAP cross', power: 'Power candle', powerSetup: 'Power setup', volBreak: 'Vol break' };

// scan ids each follow kind reacts to (e9.ts: green dildo = dildo-1m, HOD flag = hod-flag)
const FOLLOW_SCANS: Record<Exclude<FollowKind, 'off'>, readonly string[]> = {
  dildo: ['dildo-1m'],
  hod: ['hod-flag'],
  flag: ['e9flag-sideways'],
  vwap: EMA_VWAP_SCAN_IDS, // the 9EMA VWAP cross, bull and bear
  power: ['power-candle'],
  powerSetup: ['power-setup'],
  volBreak: ['vol-break', 'vol-break-9ema'], // the break and its 9 EMA entry
};

export function normalizeFollow(v: unknown): FollowKind {
  return typeof v === 'string' && (FOLLOW_KINDS as readonly string[]).includes(v) ? (v as FollowKind) : 'off';
}

export function followMatches(kind: FollowKind, scanId: string): boolean {
  return kind !== 'off' && FOLLOW_SCANS[kind].includes(scanId);
}

export interface FollowCell {
  sym: string | null;
  setup: SetupKind;
  follow?: FollowKind;
}

// applyFollow returns the cells after an alert; unchanged array when nothing moves.
// Following HOD flag defaults the cell to the 9EMA flag mode; Dildo keeps the mode.
export function applyFollow<T extends FollowCell>(cells: T[], scanId: string, sym: string): T[] {
  if (!sym) return cells;
  let changed = false;
  const out = cells.map((c) => {
    if (!followMatches(c.follow ?? 'off', scanId)) return c;
    const setup: SetupKind = c.follow === 'hod' || c.follow === 'flag' ? 'flag' : c.setup;
    if (c.sym === sym && c.setup === setup) return c;
    changed = true;
    return { ...c, sym, setup };
  });
  return changed ? out : cells;
}

export interface FollowAlert {
  scanId: string;
  sym: string;
}
const listeners = new Set<(a: FollowAlert) => void>();
export function subscribeFollowAlerts(fn: (a: FollowAlert) => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}
// Ungated scan-alert channel for the charts grid: fires for every alert frame
// (blacklist applied by the emitter), regardless of mute / Signal switches.
const scanListeners = new Set<(a: FollowAlert) => void>();
export function subscribeScanAlerts(fn: (a: FollowAlert) => void): () => void {
  scanListeners.add(fn);
  return () => scanListeners.delete(fn);
}
export function emitScanAlert(a: FollowAlert): void {
  scanListeners.forEach((fn) => fn(a));
}
export function emitFollowAlert(a: FollowAlert): void {
  listeners.forEach((fn) => fn(a));
}
