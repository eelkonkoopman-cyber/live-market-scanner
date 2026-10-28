import type { ChartAutoSettings, ChartGridLayout, SideBox, SideBoxMetric } from './types';

// Auto-fill for the charts monitor window: keep the grid's cells showing the
// top gainers of a scan WITHOUT turning the window into a disco. User: "I want
// on that chart the top 8 gainers automatically plotted. Make it user friendly
// so its not a disco." So the policy is deliberately sticky:
//
//  - a symbol on a chart never changes cell (no re-sorting by rank, ever);
//  - a newcomer must sit continuously in the top N (N = number of eligible,
//    i.e. un-pinned, cells) for ENTRY_MS before it may take a chart;
//  - a charted name is only replaceable after it has been ranked worse than
//    N + EXIT_MARGIN (or has left the ranking) for EXIT_MS;
//  - at most one replacement per SWAP_GAP_MS (filling an EMPTY cell is exempt);
//  - nothing changes while the mouse is over the grid (`held`).
//
// Everything here is pure (no React, no clock): autoStep takes `now` and the
// previous memo and returns the next ones, so it is fully unit-testable. The
// memo is deliberately NOT persisted - dwell clocks restart with the window.
// (Two charts windows both running Auto may make slightly different swaps -
// each runs its own clocks; one charts window is the supported setup.)

export const AUTO_ENTRY_MS = 30_000; // continuous time in the top N before a challenger may enter
export const AUTO_EXIT_MS = 60_000; // continuous time worse than N + margin before an incumbent is replaceable
export const AUTO_EXIT_MARGIN = 3; // an incumbent survives anywhere in ranks 1..N+3
export const AUTO_SWAP_GAP_MS = 15_000; // minimum spacing between replacements
export const AUTO_NEW_MS = 30_000; // how long the NEW badge shows on a freshly filled chart
export const AUTO_HOVER_RELEASE_MS = 10_000; // hover-freeze lets go after this long without mouse movement

// --- shared ranking (SideBoxes + Auto) ---

// The slice of a scan grid the ranking needs (structurally satisfied by the
// store's GridData).
export interface RankableGrid {
  order: string[];
  rows: Record<string, Record<string, unknown> | undefined>;
}

// rankSyms: the grid's rows that are actually moving the requested way
// (gainers: metric > 0, decliners: metric < 0 - a scan that doesn't filter on
// the metric would otherwise put a -0.5% row in a "gainers" list just because
// it sorted near the top), best first. Not sliced - callers cut it.
export function rankSyms(grid: RankableGrid | undefined, metric: SideBoxMetric, dir: 'desc' | 'asc'): string[] {
  if (!grid) return [];
  const val = (sym: string): number | null => {
    const v = grid.rows[sym]?.[metric];
    return typeof v === 'number' ? v : null;
  };
  const keep = dir === 'desc' ? (v: number) => v > 0 : (v: number) => v < 0;
  const out = grid.order.filter((s) => {
    const v = val(s);
    return v !== null && keep(v);
  });
  out.sort((a, b) => {
    const va = val(a) as number;
    const vb = val(b) as number;
    return dir === 'desc' ? vb - va : va - vb;
  });
  return out;
}

// The scan Auto draws from: the user's explicit choice (if that scan still
// exists), else the scan of the first side box that has one, else the first
// scan. null when the app has no scans yet.
export function resolveAutoScan(
  auto: ChartAutoSettings | undefined,
  boxes: SideBox[],
  scans: { id: string }[],
): string | null {
  const has = (id: string | null | undefined): id is string => !!id && scans.some((s) => s.id === id);
  if (has(auto?.scanId)) return auto.scanId;
  const boxScan = boxes.find((b) => has(b.scanId))?.scanId;
  if (boxScan) return boxScan;
  return scans[0]?.id ?? null;
}

// Convenience for selectors: the scan id Auto streams, or null when Auto is off.
export function activeAutoScan(layout: ChartGridLayout, boxes: SideBox[], scans: { id: string }[]): string | null {
  return layout.auto?.on ? resolveAutoScan(layout.auto, boxes, scans) : null;
}

// --- the step function ---

export interface AutoMemo {
  // sym -> when it was first seen in the top N of its current unbroken streak.
  firstInTop: Record<string, number>;
  // incumbent sym -> when it first ranked worse than N + margin (or vanished) in its current streak.
  outSince: Record<string, number>;
  lastSwap: number; // time of the last REPLACEMENT (fills don't count); 0 = never
  newAt: Record<number, number>; // cell index -> when Auto last filled it (drives the NEW badge)
}

export const emptyMemo = (): AutoMemo => ({ firstInTop: {}, outSince: {}, lastSwap: 0, newAt: {} });

export interface AutoStepInput {
  cells: (string | null)[]; // current symbol per cell
  pinned: boolean[]; // per cell; pinned cells are never touched
  ranking: string[]; // best first, from rankSyms
  now: number;
  held: boolean; // mouse over the grid: observe, never change
  initial: boolean; // first step after Auto was switched on: empty cells fill at once
  memo: AutoMemo;
}

export interface AutoStepResult {
  cells: (string | null)[];
  memo: AutoMemo;
  changed: number[]; // indices whose symbol Auto changed this step
}

// autoStep advances the dwell clocks and applies at most the changes the rules
// allow. HELD semantics (decided): the clocks keep running while held - a
// challenger that has earned its place keeps its seniority - but nothing is
// changed; swaps simply wait for the mouse to leave, then follow the usual
// one-per-SWAP_GAP pacing. A no-data step (empty ranking, e.g. the scan isn't
// loaded or the feed dropped) changes nothing and RESETS the clocks, so a
// reconnect gap can't make every chart look "absent for a minute".
export function autoStep(input: AutoStepInput): AutoStepResult {
  const { cells, pinned, ranking, now, held, initial } = input;
  const unchanged = (memo: AutoMemo): AutoStepResult => ({ cells, memo, changed: [] });

  if (ranking.length === 0) {
    return unchanged({ ...input.memo, firstInTop: {}, outSince: {}, newAt: pruneNew(input.memo.newAt, pinned, now) });
  }

  const eligible: number[] = [];
  const pinnedSyms = new Set<string>();
  for (let i = 0; i < cells.length; i++) {
    if (pinned[i]) {
      const s = cells[i];
      if (s) pinnedSyms.add(s);
    } else eligible.push(i);
  }
  const N = eligible.length;
  const memo: AutoMemo = {
    firstInTop: {},
    outSince: {},
    lastSwap: input.memo.lastSwap,
    newAt: pruneNew(input.memo.newAt, pinned, now),
  };
  if (N === 0) return unchanged(memo);

  // A pinned chart's symbol is not placeable, so it doesn't take up a rank.
  const eff = ranking.filter((s) => !pinnedSyms.has(s));
  const rank = new Map<string, number>();
  eff.forEach((s, i) => rank.set(s, i));

  // Clocks. firstInTop carries over only while the symbol stays in the top N;
  // outSince only while an incumbent stays worse than N + margin.
  for (let i = 0; i < N && i < eff.length; i++) {
    const s = eff[i];
    memo.firstInTop[s] = input.memo.firstInTop[s] ?? now;
  }
  for (const ci of eligible) {
    const s = cells[ci];
    if (!s) continue;
    const r = rank.get(s);
    if (r === undefined || r >= N + AUTO_EXIT_MARGIN) memo.outSince[s] = input.memo.outSince[s] ?? now;
  }

  if (held) return unchanged(memo);

  const out = cells.slice();
  const changed: number[] = [];
  const placed = new Set<string>(out.filter((s): s is string => !!s));
  const qualifies = (s: string) => initial || now - (memo.firstInTop[s] ?? now) >= AUTO_ENTRY_MS;
  // Qualified challengers: top N, not on any chart, best first.
  const challengers = (): string[] => eff.slice(0, N).filter((s) => !placed.has(s) && qualifies(s));

  // 1) Fill empty eligible cells (no rate limit). Entry dwell still applies,
  // except on the initial step.
  for (const ci of eligible) {
    if (out[ci]) continue;
    const next = challengers()[0];
    if (!next) break;
    out[ci] = next;
    placed.add(next);
    memo.newAt[ci] = now;
    changed.push(ci);
  }

  // 2) At most one replacement per SWAP_GAP: the replaceable incumbent with
  // the worst current rank (absent = worst) gives way to the best qualified
  // challenger.
  if (now - memo.lastSwap >= AUTO_SWAP_GAP_MS) {
    let victim = -1;
    let victimRank = -1;
    for (const ci of eligible) {
      const s = out[ci];
      if (!s || changed.includes(ci)) continue;
      const since = memo.outSince[s];
      if (since === undefined || now - since < AUTO_EXIT_MS) continue;
      const r = rank.get(s) ?? Infinity;
      if (r > victimRank) {
        victim = ci;
        victimRank = r;
      }
    }
    const next = victim >= 0 ? challengers()[0] : undefined;
    if (victim >= 0 && next) {
      delete memo.outSince[out[victim] as string];
      out[victim] = next;
      memo.newAt[victim] = now;
      memo.lastSwap = now;
      changed.push(victim);
    }
  }

  return { cells: changed.length ? out : cells, memo, changed };
}

function pruneNew(newAt: Record<number, number>, pinned: boolean[], now: number): Record<number, number> {
  const out: Record<number, number> = {};
  for (const k of Object.keys(newAt)) {
    const i = Number(k);
    if (!pinned[i] && now - newAt[i] < AUTO_NEW_MS) out[i] = newAt[i];
  }
  return out;
}
