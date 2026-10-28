// Pure helper behind the grid's hover-freeze (see LiveGrid.tsx): while the
// mouse sits over the list, row ORDER stops reshuffling underneath it — cell
// VALUES still update live, only the array position a symbol occupies is
// held — so a scan sorted by a fast-moving field doesn't turn "click this
// row" into a chase. Kept free of React so the merge logic is trivially
// testable on its own.

// Every symbol from `frozen` keeps its original slot, including ones no
// longer present in `live` (so nothing already on screen shifts), followed by
// whatever is in `live` that wasn't already in `frozen`, in live order (a
// genuinely new arrival lands at the bottom instead of jumping into the
// middle of a held list).
export function mergeHeldOrder(frozen: string[], live: string[]): string[] {
  const frozenSet = new Set(frozen);
  const arrivals = live.filter((sym) => !frozenSet.has(sym));
  return [...frozen, ...arrivals];
}
