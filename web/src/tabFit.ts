// Pure layout maths for the scan tab bar's overflow ("More") behaviour. No
// React, no DOM — ScanTabs measures the real pill widths and hands them here,
// so the decision is unit-testable and, crucially, depends ONLY on widths and
// the available space: nothing about WHICH scans are active or matching moves
// a tab in or out of view (the user reverted an earlier auto-resizing tab bar,
// so tabs keep their fixed scan order and only the cut-off edge is dynamic).

/**
 * How many LEADING pills fit in `available` px.
 *
 * Every pill is followed by one `gap`, so the first n pills occupy
 * sum(w[0..n)) + n*gap. `reserve` is what must stay free after them when some
 * pills overflow: the "More" button, the "+" button and the gap between those
 * two (the gap after the last visible pill is already counted in n*gap).
 *
 * If ALL pills fit, the More button is not rendered, so only `reserveIfAll`
 * (default: `reserve`; callers pass just the "+" width) has to remain free —
 * in that case the full count is returned. Otherwise the largest n whose pills
 * plus `reserve` still fit is returned (0 when not even one fits).
 */
export function fitCount(
  pillWidths: number[],
  available: number,
  gap: number,
  reserve: number,
  reserveIfAll: number = reserve,
): number {
  const total = pillWidths.length;
  let used = 0;
  for (let i = 0; i < total; i++) used += pillWidths[i] + gap;
  if (used + reserveIfAll <= available) return total;

  let n = 0;
  used = 0;
  while (n < total && used + pillWidths[n] + gap + reserve <= available) {
    used += pillWidths[n] + gap;
    n++;
  }
  return n;
}

/**
 * Display order of the scan pills: scans that are tiled (`openTiles`) first, in
 * tile order, then every other scan in its normal order. Ids in `openTiles`
 * that no longer exist are ignored.
 *
 * This keeps the tab bar free of self-moving layout: the order changes only
 * when the USER tiles or closes a scan, never because a match count changed.
 */
export function orderTabs(scanIds: string[], openTiles: string[]): string[] {
  const present = new Set(scanIds);
  const tiled = openTiles.filter((id, i) => present.has(id) && openTiles.indexOf(id) === i);
  const tiledSet = new Set(tiled);
  return [...tiled, ...scanIds.filter((id) => !tiledSet.has(id))];
}
