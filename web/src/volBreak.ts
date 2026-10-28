// VOLUME BREAK (2026-10-28; docs/volume-break.md): the web side of the engine's
// volume break judge (internal/volbreak, internal/engine/volbreak.go). Two
// alerts, nothing is ordered:
//   vol-break       "Vol Break"       vbAge <= 240                       "SYM: volume break 3.43 (4.2x vol), nine E M A 3.31"
//   vol-break-9ema  "Vol Break 9EMA"  vbState == 2 and vbEntryAge <= 240 "SYM: vol break nine E M A entry 3.45, level 3.43"
import { fmtSpokenPrice } from './powerSetup';

export const VOL_BREAK_SCAN_ID = 'vol-break';
export const VOL_BREAK_ENTRY_SCAN_ID = 'vol-break-9ema';

type Row = Readonly<Record<string, unknown>> | null | undefined;

function num(v: unknown): number | null {
  return typeof v === 'number' && isFinite(v) ? v : null;
}

// volBreakText: the measured part of each alert, as written in the list and the
// SIGNALS notes ('3.43 (4.2x vol), 9EMA 3.31' / 'entry 3.45, level 3.43'); ''
// when the row does not carry the numbers.
export function volBreakText(scanId: string, row: Row, ema = '9EMA'): string {
  const lvl = num(row?.vbLevel);
  if (lvl == null) return '';
  if (scanId === VOL_BREAK_SCAN_ID) {
    const x = num(row?.vbVolX);
    const e = num(row?.vbEma);
    return `${fmtSpokenPrice(lvl)}${x != null ? ` (${x.toFixed(1)}x vol)` : ''}${e != null ? `, ${ema} ${fmtSpokenPrice(e)}` : ''}`;
  }
  if (scanId === VOL_BREAK_ENTRY_SCAN_ID) {
    const px = num(row?.vbEntryPx);
    return `${px != null ? `entry ${fmtSpokenPrice(px)}, ` : ''}level ${fmtSpokenPrice(lvl)}`;
  }
  return '';
}

// volBreakUtterance: 'ABCD: volume break' -> 'ABCD: volume break 3.43 (4.2x vol), nine E M A 3.31';
// 'ABCD: vol break nine E M A' -> 'ABCD: vol break nine E M A entry 3.45, level 3.43'.
// "9EMA" is spelled out for the speech engine like every other 9 EMA alert. Anything else unchanged.
export function volBreakUtterance(say: string, scanId: string, row: Row): string {
  const t = volBreakText(scanId, row, 'nine E M A');
  return t ? `${say} ${t}` : say;
}
