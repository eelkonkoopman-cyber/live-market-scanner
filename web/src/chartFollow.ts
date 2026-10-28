import type { ChartCell } from './types';

// Charts-grid "Follow scan": a cell with followScan set switches to the symbol
// of each NEW alert of that scan (scan has alerts enabled) or to the newest
// entrant of the scan's results (scan without alerts). Pure helpers; the
// drivers live in ChartsPane.tsx. Setting a symbol by hand or pinning a cell
// turns its follow Off (store.ts assignCell / toggleCellPin).

export function normalizeFollowScan(v: unknown): string | undefined {
  return typeof v === 'string' && v ? v : undefined;
}

// Cells after an event for scanId/sym; same array when nothing moves.
export function applyScanFollow(cells: ChartCell[], scanId: string, sym: string): ChartCell[] {
  const s = sym.trim().toUpperCase();
  if (!s) return cells;
  let changed = false;
  const out = cells.map((c) => {
    if (c.followScan !== scanId || c.sym === s) return c;
    changed = true;
    return { ...c, sym: s };
  });
  return changed ? out : cells;
}

// Symbols present in order but not in prev. prev undefined = first look
// (baseline), nothing is an entrant.
export function newEntrants(prev: readonly string[] | undefined, order: readonly string[]): string[] {
  if (!prev) return [];
  const had = new Set(prev);
  return order.filter((s) => !had.has(s));
}

// Scan ids that cells follow, split by whether the scan has alerts enabled.
export function followedScans(
  cells: readonly ChartCell[],
  scans: readonly { id: string; alert?: { enabled?: boolean } }[],
): { alertIds: string[]; entrantIds: string[] } {
  const alertIds: string[] = [];
  const entrantIds: string[] = [];
  const ids = new Set(cells.map((c) => c.followScan).filter((x): x is string => !!x));
  for (const id of ids) {
    const sc = scans.find((s) => s.id === id);
    if (!sc) continue;
    (sc.alert?.enabled ? alertIds : entrantIds).push(id);
  }
  return { alertIds, entrantIds };
}
