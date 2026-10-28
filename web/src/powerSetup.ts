// POWER SETUP (2026-10-27; docs/power-setup.md): the web side of the engine's
// power-candle filters (internal/engine/power_setup.go, core/powersetup.go).
// The numbers mirror core.PsMaxFromOpenPct / PsMinSlopePct / PsMinGapPct.
export const POWER_SETUP_SCAN_ID = 'power-setup';
export const PS_MAX_FROM_OPEN_PCT = 21;
export const PS_MIN_SLOPE_PCT = 2.1;

type Row = Readonly<Record<string, unknown>> | null | undefined;

function num(v: unknown): number | null {
  return typeof v === 'number' && isFinite(v) ? v : null;
}

function mark(v: number | null, pass: boolean): string {
  return v == null ? '?' : pass ? '✓' : '✗';
}

// psFilterTag: "A✓ slope✗" (a "?" is a filter whose value is unknown, which fails). The gap
// filter is OFF by default (honest re-test), so it has no mark; the gap stays a column.
export function psFilterTag(r: Row): string {
  const fo = num(r?.psFromOpen);
  const sl = num(r?.psEmaSlope);
  const skip = r?.psPass === 2 ? ' · skipped: quiet window' : '';
  return `A${mark(fo, fo != null && fo < PS_MAX_FROM_OPEN_PCT)} slope${mark(sl, sl != null && sl >= PS_MIN_SLOPE_PCT)}${skip}`;
}

// fmtSpokenPrice: a price rounded sensibly for the voice and the list: 2 decimals from $1,
// 3 from 10 cents, 4 below; trailing zeros dropped ("0.57", never "0.570").
export function fmtSpokenPrice(p: number): string {
  const d = p >= 1 ? 2 : p >= 0.1 ? 3 : 4;
  return String(Number(p.toFixed(d)));
}

// psPlanText: "entry 3.45 · stop 3.24 · target 4.07" (empty parts when the plan is unknown).
export function psPlanText(r: Row): string {
  const e = num(r?.psEntry);
  const s = num(r?.psStop);
  const t = num(r?.psTarget);
  if (e == null) return 'plan unknown (9EMA not seeded)';
  return [`entry ${fmtSpokenPrice(e)}`, s != null ? `stop ${fmtSpokenPrice(s)}` : '', t != null ? `target ${fmtSpokenPrice(t)}` : ''].filter(Boolean).join(' · ');
}

// A+ GRADE (2026-10-28; core.PsAplus*): the 15 one-minute bars before the power
// candle span <= 5% (a tight base). The order ticket sizes an A+ setup at 1.5x.
export const PS_APLUS_RISK_MULT = 1.5;

// psAplusText: 'A+ (tight base 3.8%)' for an A+ row, '' otherwise.
export function psAplusText(r: Row): string {
  if (r?.psAplus !== 1) return '';
  const b = num(r?.psBaseRange);
  return b != null ? `A+ (tight base ${b.toFixed(1)}%)` : 'A+';
}

// powerSetupUtterance: 'ABCD: power setup' -> 'ABCD: power setup, entry 3.45, stop 3.24';
// an A+ setup: 'ABCD: power setup A+ (tight base 3.8%), entry 3.45, stop 3.24'.
// Anything else (or a row without the plan) is unchanged.
export function powerSetupUtterance(say: string, scanId: string, row: Row): string {
  if (scanId !== POWER_SETUP_SCAN_ID) return say;
  const e = num(row?.psEntry);
  const s = num(row?.psStop);
  if (e == null || s == null) return say;
  const a = psAplusText(row);
  return `${say}${a ? ` ${a}` : ''}, entry ${fmtSpokenPrice(e)}, stop ${fmtSpokenPrice(s)}`;
}

// psAplusRisk: the risk the order ticket sizes with. An A+ latest Power Setup
// (mult > 1) with the Power Setup preset active and not overridden -> risk x mult.
export function psAplusRisk(base: number, grade: { aplus: boolean; mult: number } | null | undefined, presetPower: boolean, overridden: boolean): { risk: number; applied: boolean; eligible: boolean } {
  const eligible = !!grade?.aplus && grade.mult > 1 && presetPower;
  const applied = eligible && !overridden && base > 0;
  return { risk: applied ? Math.round(base * grade!.mult * 100) / 100 : base, applied, eligible };
}
