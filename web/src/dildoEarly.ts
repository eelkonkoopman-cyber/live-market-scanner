// Early green dildo (2026-10-13): every Green Dildo alert still fires, but it
// says which kind it is. The server judges each dildo with the auto-trader's
// own rule (internal/dildopb JudgeEarly: a quiet 30-bar base of at most 8%, the
// close above its high, the first dildo of the day, a new high of the day) and sends the verdict on
// the row as dildoEarly (1 early / 0 not / absent = unknown) and on the SIGNALS
// history row as a leading 'early; ' on the note (flaglog EarlyNotePrefix).
import { GREEN_DILDO_SCAN_ID } from './dildoNews';

export const EARLY_NOTE_PREFIX = 'early; ';
export const EARLY_BADGE = { label: 'EARLY', title: 'Early green dildo: the first big candle out of a quiet base (the auto-trader’s rule)' };

// isEarlyDildo: the row's dildo was an early one (unknown counts as not).
export function isEarlyDildo(row: Readonly<Record<string, unknown>> | null | undefined): boolean {
  return row?.dildoEarly === 1;
}

// earlyDildoUtterance: 'SYM: green dildo' -> 'SYM: early green dildo' for an
// early Green Dildo alert; anything else unchanged.
export function earlyDildoUtterance(say: string, scanId: string, sym: string, row: Readonly<Record<string, unknown>> | null | undefined): string {
  if (scanId !== GREEN_DILDO_SCAN_ID || !isEarlyDildo(row)) return say;
  const head = `${sym}: `;
  return say.startsWith(head) ? `${head}early ${say.slice(head.length)}` : say;
}

// splitEarlyNote: a history row's EARLY flag and its note without the marker.
export function splitEarlyNote(kind: string, note: string | undefined): { early: boolean; note: string | undefined } {
  if (kind === 'dildo' && note?.startsWith(EARLY_NOTE_PREFIX)) return { early: true, note: note.slice(EARLY_NOTE_PREFIX.length) };
  return { early: false, note };
}
