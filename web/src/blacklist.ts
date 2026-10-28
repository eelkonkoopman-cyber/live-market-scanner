// Today's symbol blacklist (internal/blacklist on the server): alerts of a
// listed symbol make no tone or speech, and the Day Log flags them. The server
// sends the whole list on hello and after every change ('blacklist' frame); it
// clears at 00:00 ET.
import { etDayKey } from './marketClock';

/** Normalises a 'blacklist' frame's `d` into a sorted, upper-case list (never null). */
export function normalizeBlacklist(raw: unknown): string[] {
  const syms = (raw as { syms?: unknown } | null)?.syms;
  if (!Array.isArray(syms)) return [];
  return [...new Set(syms.filter((s): s is string => typeof s === 'string' && s.trim() !== '').map((s) => s.trim().toUpperCase()))].sort();
}

/**
 * isBlacklisted: sym is on the list received at listAtMs. The list belongs to
 * one ET day (the server clears it at 00:00 ET), so a list from an earlier ET
 * day silences nothing: no broadcast may arrive at midnight.
 */
export function isBlacklisted(list: readonly string[] | null | undefined, listAtMs: number, sym: string, nowMs: number): boolean {
  if (!list || list.length === 0 || !sym) return false;
  if (etDayKey(listAtMs) !== etDayKey(nowMs)) return false;
  return list.includes(sym.toUpperCase());
}
