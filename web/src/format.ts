import { fieldDef } from './fields';

// Compact notation for large counts (volume, float, shares): 6.8M, 12.4M, 1.2B.
const compact = new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 });

export function fmt(v: unknown): string {
  if (v === null || v === undefined) return '–';
  if (typeof v === 'boolean') return v ? '●' : '';
  if (typeof v === 'number') {
    if (!isFinite(v)) return '–';
    if (Number.isInteger(v)) {
      // Keep small integers exact (prices, counts); compact the big ones so the
      // column never truncates. 9,999 → "9,999", 6,800,000 → "6.8M".
      return Math.abs(v) >= 10_000 ? compact.format(v) : v.toLocaleString();
    }
    return v.toFixed(2);
  }
  if (Array.isArray(v)) return ''; // series are rendered as charts, not text
  return String(v);
}

// Compact dollar figures (revenue, cash, burn): always compact notation with a
// $ prefix, regardless of exact fractional cents in the source value —
// fundamentals amounts are large enough that cents are always noise.
export function money(v: number | null | undefined): string {
  if (v == null || !isFinite(v)) return '–';
  const sign = v < 0 ? '-' : '';
  return `${sign}$${compact.format(Math.abs(v))}`;
}

// Price columns need MORE precision below $1 (a $0.35 stock quoted to 2dp
// throws away a third of its tick resolution) and a steady 2dp above it, so a
// column of prices lines up instead of "22" sitting next to "27.40".
export function fmtPrice(v: number | null | undefined): string {
  if (v == null || !isFinite(v)) return '–';
  return Math.abs(v) < 1 ? v.toFixed(4) : v.toFixed(2);
}

// Percent columns always carry the unit and, for signed metrics (change,
// distance from VWAP, ...), an explicit '+' — without it a column of mixed
// signs reads as all-positive at a glance.
export function fmtPct(v: number | null | undefined, signed = true): string {
  if (v == null || !isFinite(v)) return '–';
  const sign = signed && v >= 0 ? '+' : '';
  return `${sign}${v.toFixed(2)}%`;
}

// Same compacting rule as fmt's integer branch, pulled out standalone so
// formatField can apply it to any wire value of this kind (rounded first —
// a share/volume count is never fractional, so a fractional wire value is
// just transport noise, not a real value to preserve).
export function fmtCompact(v: number | null | undefined): string {
  if (v == null || !isFinite(v)) return '–';
  const r = Math.round(v);
  return Math.abs(r) >= 10_000 ? compact.format(r) : r.toLocaleString();
}

export function fmtRatio(v: number | null | undefined): string {
  if (v == null || !isFinite(v)) return '–';
  return `${v.toFixed(2)}x`;
}

// Durations arrive from the wire in seconds; rendering in whatever unit keeps
// the number small (s, then m, then h) is what makes "300" legible as "5m"
// instead of a raw second count nobody can parse at a glance.
export function fmtDur(sec: number | null | undefined): string {
  if (sec == null || !isFinite(sec) || sec < 0) return '–';
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${+(sec / 60).toFixed(1)}m`;
  return `${+(sec / 3600).toFixed(1)}h`;
}

// secType is the one enum field today; wire codes map to words a scanning
// human recognizes at a glance rather than the raw IBKR security-type code.
const SEC_TYPE_WORDS: Record<string, string> = { CS: 'Stock', ETF: 'ETF', ADRC: 'ADR' };

// formatField is the ONE cell formatter: every grid cell (and, via the same
// registry, symbol-panel stats) goes through here so a field's presentation —
// decimals, sign, unit, tone — is decided once from the registry's `kind`
// instead of by ad-hoc key checks scattered through components.
export function formatField(
  key: string,
  v: unknown,
  row?: Record<string, unknown>
): { text: string; tone: 'pos' | 'neg' | 'muted' | '' } {
  if (v === null || v === undefined) return { text: '–', tone: '' };
  const kind = fieldDef(key)?.kind;
  switch (kind) {
    case 'price':
      return typeof v === 'number' ? { text: fmtPrice(v), tone: '' } : { text: String(v), tone: '' };
    case 'pct':
      return typeof v === 'number'
        ? { text: fmtPct(v), tone: v >= 0 ? 'pos' : 'neg' }
        : { text: String(v), tone: '' };
    case 'pctAbs':
      return typeof v === 'number' ? { text: fmtPct(v, false), tone: '' } : { text: String(v), tone: '' };
    case 'int':
      return typeof v === 'number' ? { text: Math.round(v).toLocaleString(), tone: '' } : { text: String(v), tone: '' };
    case 'compact': {
      if (typeof v !== 'number') return { text: String(v), tone: '' };
      // IBKR caps its borrow file at 10,000,000 shares — ">10M" marks that as
      // a floor, not a precise count, wherever shortAvail is shown.
      const cap = key === 'shortAvail' && row?.shortCapped ? '>' : '';
      return { text: `${cap}${fmtCompact(v)}`, tone: '' };
    }
    case 'ratio':
      return typeof v === 'number' ? { text: fmtRatio(v), tone: '' } : { text: String(v), tone: '' };
    case 'score':
      return typeof v === 'number' ? { text: String(Math.round(v)), tone: '' } : { text: String(v), tone: '' };
    case 'dur':
      return typeof v === 'number' ? { text: fmtDur(v), tone: '' } : { text: String(v), tone: '' };
    case 'flagState': {
      if (typeof v !== 'number') return { text: String(v), tone: '' };
      if (v === 2) return { text: 'breakout', tone: 'pos' };
      if (v === 1) return { text: 'coiling', tone: '' };
      return { text: 'none', tone: 'muted' };
    }
    case 'enum':
      return key === 'secType'
        ? { text: SEC_TYPE_WORDS[String(v)] ?? String(v), tone: '' }
        : { text: String(v), tone: '' };
    case 'text':
      return { text: String(v), tone: '' };
    case 'bool':
    case 'trend':
    case 'hits':
    case 'chart':
      // The grid renders these itself (dot / arrow badge / hit badge / inline
      // chart) — formatField only owns the plain text+tone cells.
      return { text: '', tone: '' };
    default:
      // Unknown key (a future server field the UI doesn't know yet): fall
      // back to the generic formatter so nothing renders blank.
      return { text: fmt(v), tone: '' };
  }
}
