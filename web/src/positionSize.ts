// Position size (2026-10-14): the Signal window's "Position size $" box. Each
// Green Dildo alert also says how many shares that money buys at the dildo's
// close — "ABCD: early green dildo. News headline 10 seconds ago. 1,250
// shares." — and the left-bar SIGNALS row shows "1,250 sh". 0 / empty = off.
// Display and speech only: nothing here touches detection.
import { GREEN_DILDO_SCAN_ID } from './dildoNews';

export const POSITION_SIZE_KEY = 'mktscanner.signal.positionSize.v1';
export const DEFAULT_POSITION_SIZE = 2000;

// parsePositionSize: a stored / typed value -> dollars (>= 0); garbage -> default,
// empty -> 0 (off).
export function parsePositionSize(raw: string | null): number {
  if (raw === null) return DEFAULT_POSITION_SIZE;
  if (raw.trim() === '') return 0;
  const n = Number(raw);
  return Number.isFinite(n) && n >= 0 ? n : DEFAULT_POSITION_SIZE;
}

export function loadPositionSize(): number {
  try {
    return parsePositionSize(localStorage.getItem(POSITION_SIZE_KEY));
  } catch {
    return DEFAULT_POSITION_SIZE;
  }
}

export function savePositionSize(n: number): void {
  try {
    localStorage.setItem(POSITION_SIZE_KEY, String(n));
  } catch {
    /* best-effort */
  }
}

// sharesFor: whole shares `size` dollars buys at `price`; 0 when off or no price.
export function sharesFor(size: number, price: number | null | undefined): number {
  if (!(size > 0) || typeof price !== 'number' || !(price > 0)) return 0;
  return Math.floor(size / price + 1e-9);
}

// dildoPrice: the dildo's close from the alert row, else its last.
export function dildoPrice(row: Readonly<Record<string, unknown>> | null | undefined): number | null {
  for (const k of ['dildoClose', 'close', 'last']) {
    const v = row?.[k];
    if (typeof v === 'number' && v > 0) return v;
  }
  return null;
}

export function fmtShares(n: number): string {
  return n.toLocaleString('en-US');
}

// dildoSharesSuffix: '. 1,250 shares' for a Green Dildo alert, '' otherwise.
export function dildoSharesSuffix(scanId: string, row: Readonly<Record<string, unknown>> | null | undefined, size: number): string {
  if (scanId !== GREEN_DILDO_SCAN_ID) return '';
  const n = sharesFor(size, dildoPrice(row));
  if (n <= 0) return '';
  return `. ${fmtShares(n)} ${n === 1 ? 'share' : 'shares'}`;
}
