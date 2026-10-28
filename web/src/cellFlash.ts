// The live grid's up/down cell flash (LiveGrid's GridRow): a numeric cell whose
// value changed since the row's previous render gets 'flash-up' (green) or
// 'flash-down' (red), and the CSS animation plays once.
//
// Columns in NO_FLASH_COLS never flash. e9Age counts up by a second every tick
// — a green flash on every row once a second would drown the stage changes it
// sits next to, and an age growing is not a move. Its sibling e9State DOES
// flash: a stage change (up = further along the setup) is exactly the event to
// catch the eye. e9SlamAge (the seeded "9EMA Slam" scan's age column, seconds
// since the slam) ticks the same way: a green 'up' pulse once a second on a
// bearish alert's row, drowning the e9SlamPct / e9Dist changes beside it.
// The green dildo and volume climax scans (2026-10-07) carry five more such
// ages — since the dildo, its 9 EMA pullback and touch, the climax and its
// confirmation — and the same reasoning holds for each.
export const NO_FLASH_COLS: ReadonlySet<string> = new Set([
  'e9Age',
  'e9SlamAge',
  'e9HodFlagAge',
  'dildoAge',
  'dildoPbAge',
  'dildoTouchAge',
  'climaxAge',
  'climaxConfAge',
  'bbsAge',
  'ignAge',
  'vbAge',
  'vbEntryAge',
  'evBullAge',
  'evBearAge',
]);

// Their states (dildoPbState, climaxState) are VERDICTS, not stages: a higher
// number is not further along (climaxState 1 watching, 2 confirmed, 3 failed,
// 4 expired; dildoPbState 1 watching, 2 touching, 3 confirmed, 4 cancelled,
// 5 expired), so flashing them by number turned a failed climax or a cancelled
// pullback green. They flash by what the NEW state means instead: green for a
// confirmation (and the pullback's touch, the early cue), red for a failure or
// a cancel, nothing for an expiry (no verdict) or a new climax / dildo
// re-arming the latch (watching again, 0 = none today).
const VERDICT_FLASH: Readonly<Record<string, Readonly<Record<number, CellFlash>>>> = {
  climaxState: { 2: 'flash-up', 3: 'flash-down' },
  dildoPbState: { 2: 'flash-up', 3: 'flash-up', 4: 'flash-down' },
};

export type CellFlash = '' | 'flash-up' | 'flash-down';

export function cellFlash(col: string, v: unknown, prev: unknown): CellFlash {
  if (NO_FLASH_COLS.has(col) || typeof v !== 'number' || typeof prev !== 'number' || v === prev) return '';
  const verdict = VERDICT_FLASH[col];
  if (verdict) return verdict[v] ?? '';
  return v > prev ? 'flash-up' : 'flash-down';
}
