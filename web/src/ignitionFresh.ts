// Fresh ignition (2026-10-14): every Ignition alert still fires, but it says
// whether the stock was still flat on the day when it ignited. The server
// judges it (internal/engine ignFresh: the ignition bar's open vs the previous
// close, at most +3%) and sends it on the row as ignFresh (1 fresh / 0 not /
// absent = unknown) and on the SIGNALS history row as a leading 'fresh; ' on
// the note (flaglog FreshNotePrefix).
export const IGNITION_SCAN_ID = 'ignition';
export const FRESH_NOTE_PREFIX = 'fresh; ';
export const FRESH_BADGE = { label: 'FRESH', title: 'Fresh ignition: the stock was up at most 3% on the day before this candle' };

// isFreshIgnition: the row's ignition was a fresh one (unknown counts as not).
export function isFreshIgnition(row: Readonly<Record<string, unknown>> | null | undefined): boolean {
  return row?.ignFresh === 1;
}

// freshIgnitionUtterance: 'SYM: ignition' -> 'SYM: fresh ignition' for a fresh
// Ignition alert; anything else unchanged.
export function freshIgnitionUtterance(say: string, scanId: string, sym: string, row: Readonly<Record<string, unknown>> | null | undefined): string {
  if (scanId !== IGNITION_SCAN_ID || !isFreshIgnition(row)) return say;
  const head = `${sym}: `;
  return say.startsWith(head) ? `${head}fresh ${say.slice(head.length)}` : say;
}

// splitFreshNote: a history row's FRESH flag and its note without the marker.
export function splitFreshNote(kind: string, note: string | undefined): { fresh: boolean; note: string | undefined } {
  if (kind === 'ignition' && note?.startsWith(FRESH_NOTE_PREFIX)) return { fresh: true, note: note.slice(FRESH_NOTE_PREFIX.length) };
  return { fresh: false, note };
}

// Skipped ignitions (2026-10-14): an ignition failing the server's skip rules
// (internal/engine ignSkip) does not alert, but still lands in the SIGNALS
// history, dimmed, its note led by the reason: 'skip: up 7.2% on the day; ...'.
export const SKIP_NOTE_PREFIX = 'skip: ';

// isSkippedIgnition: a history row of a skipped ignition (shown dimmed).
export function isSkippedIgnition(kind: string, note: string | undefined): boolean {
  return kind === 'ignition' && !!note?.startsWith(SKIP_NOTE_PREFIX);
}
