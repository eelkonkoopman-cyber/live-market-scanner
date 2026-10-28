// The toast text for a tws.auto frame: the server's outcome for one alert of a
// scan with "Send to TWS when it alerts" on (internal/server/twsauto.go).
//   sent     -> "TWS → ABCD · Green Dildo"
//   skipped  -> "Green Dildo ABCD not sent to TWS — you picked XYZ 8s ago"
// Returns null for a frame with no symbol (nothing worth saying).
export function twsAutoToast(d: unknown): string | null {
  const m = (d ?? {}) as { sym?: unknown; scanName?: unknown; sent?: unknown; reason?: unknown };
  const sym = typeof m.sym === 'string' ? m.sym.trim().toUpperCase() : '';
  if (!sym) return null;
  const name = typeof m.scanName === 'string' ? m.scanName.trim() : '';
  if (m.sent === true) return name ? `TWS → ${sym} · ${name}` : `TWS → ${sym}`;
  const reason = typeof m.reason === 'string' ? m.reason.trim() : '';
  return `${name ? `${name} ` : ''}${sym} not sent to TWS${reason ? ` — ${reason}` : ''}`;
}
