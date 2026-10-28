import type { FlagEvent, FlagEventKind } from './types';

// Pure helpers for the Signal window's signal history - the flag scan's
// signals plus the green dildo / volume climax alerts
// (components/FlagEventList.tsx): badge text/tone per kind, the visible+ordered
// event list, and the Eastern-time formatting (the server stores events per ET
// day, so the list reads in ET whatever this machine's timezone is).

export type FlagEventTone = 'pos' | 'neg' | 'warn' | 'muted' | 'info';
export type FlagEventFilter = 'all' | 'trades';

const BADGES: Record<FlagEventKind, { text: string; tone: FlagEventTone }> = {
  entry: { text: 'LONG', tone: 'pos' },
  add: { text: 'ADD', tone: 'pos' },
  exit: { text: 'EXIT', tone: 'neg' },
  failed: { text: 'FAILED', tone: 'neg' },
  earlyBreak: { text: 'EARLY', tone: 'warn' },
  emaNear: { text: 'READY', tone: 'warn' },
  flat: { text: 'FLAT', tone: 'muted' },
  impulse: { text: 'BREAKOUT', tone: 'muted' },
  slam: { text: 'SLAM', tone: 'neg' },
  // The market-wide 1-minute alerts (GREEN DILDO -> 9 EMA PULLBACK, VOLUME
  // CLIMAX -> BOTTOM CONFIRMED): each its own badge. Their dead ends (PB
  // CANCELLED, CLIMAX FAILED) are low-key rows (LOWKEY_KINDS).
  dildo: { text: 'DILDO', tone: 'info' },
  dildoPb: { text: '9EMA PB', tone: 'pos' },
  dildoCancel: { text: 'PB FAIL', tone: 'muted' },
  climax: { text: 'CLIMAX', tone: 'warn' },
  bottom: { text: 'BOTTOM', tone: 'pos' },
  climaxFail: { text: 'CLX FAIL', tone: 'muted' },
  bbs: { text: 'BBS', tone: 'neg' },
  ignition: { text: 'IGN', tone: 'warn' },
  power: { text: 'POWER', tone: 'pos' },
  powerSetup: { text: 'P-SETUP', tone: 'pos' },
  volBreak: { text: 'VOL BRK', tone: 'info' },
  volBreakEntry: { text: 'VB 9EMA', tone: 'pos' },
  vwapBull: { text: 'VWAP+', tone: 'pos' },
  vwapBear: { text: 'VWAP−', tone: 'neg' },
};

export function kindBadge(kind: FlagEventKind): { text: string; tone: FlagEventTone } {
  return BADGES[kind] ?? { text: String(kind).toUpperCase(), tone: 'muted' };
}

// eventBadge: kindBadge, except an A+ power setup (server label "P-SETUP A+") reads "P-SETUP A+".
export function eventBadge(e: Pick<FlagEvent, 'kind' | 'label'>): { text: string; tone: FlagEventTone } {
  const b = kindBadge(e.kind);
  return e.kind === 'powerSetup' && /\bA\+$/.test(e.label ?? '') ? { ...b, text: `${b.text} A+` } : b;
}

// The rows shown dimmed: the dildo's pullback that never came (PB CANCELLED)
// and the climax whose low broke (CLIMAX FAILED). They close a setup's story
// in the list ("that one is dead") without competing with the live signals.
export const LOWKEY_KINDS: readonly FlagEventKind[] = ['dildoCancel', 'climaxFail'];

export function isLowKey(kind: FlagEventKind): boolean {
  return LOWKEY_KINDS.includes(kind);
}

// The row's tooltip: label, price, the note (dildo / climax rows), the action.
export function eventTitle(e: Pick<FlagEvent, 'label' | 'price' | 'note'>): string {
  return `${e.label} at ${e.price ?? '–'}${e.note ? ` — ${e.note}` : ''} — click to load on the active chart, drag onto a chart`;
}

// The kinds that are actual trade signals (the "Trades" filter). The 9 EMA SLAM
// is an alert, not a trade, but it is the call the trader asked to be told about
// and it usually shares its bar with a FAILED or an EXIT (an EARLY BREAK when the
// bar first broke a flag that was not yet READY), so it stays next to them.
export const TRADE_KINDS: readonly FlagEventKind[] = ['entry', 'add', 'exit', 'failed', 'slam'];

// What the list shows, newest first. 'impulse' is dropped ALWAYS: every
// candidate's breakout is an event, so in a history list they are noise that
// would bury the signals that matter (the live Flag scan list already shows
// which stocks are in the BREAKOUT stage). The dildo / climax kinds show under
// 'all' only: 'trades' stays the flag setup's. Newest = ts descending, ties broken
// by the original (arrival) order descending. The input is not mutated.
export function visibleEvents(events: readonly FlagEvent[], filter: FlagEventFilter): FlagEvent[] {
  return events
    .map((e, i) => ({ e, i }))
    .filter(({ e }) => e.kind !== 'impulse' && (filter === 'all' || TRADE_KINDS.includes(e.kind)))
    .sort((a, b) => b.e.ts - a.e.ts || b.i - a.i)
    .map(({ e }) => e);
}

// The row key: one event is one (symbol, bar, kind) — the same identity the
// store dedupes live adds by.
export function flagEventKey(e: Pick<FlagEvent, 'sym' | 'ts' | 'kind'>): string {
  return `${e.sym}|${e.ts}|${e.kind}`;
}

const etClock = new Intl.DateTimeFormat('en-GB', {
  timeZone: 'America/New_York',
  hour: '2-digit',
  minute: '2-digit',
  hourCycle: 'h23',
});

// 'HH:MM' (24h) in America/New_York.
export function etTime(ms: number): string {
  return etClock.format(new Date(ms));
}

const etDay = new Intl.DateTimeFormat('en-CA', {
  timeZone: 'America/New_York',
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
});

// 'YYYY-MM-DD' of that instant in America/New_York.
export function todayET(nowMs: number): string {
  const parts = etDay.formatToParts(new Date(nowMs));
  const g = (t: string) => parts.find((p) => p.type === t)?.value ?? '';
  return `${g('year')}-${g('month')}-${g('day')}`;
}

const WEEKDAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

// 'Today' for today, else 'Mon 5 Oct'. The YYYY-MM-DD is a CALENDAR date, so
// the weekday comes from a UTC construction of that date (no local-timezone
// shift can move it a day).
export function dateLabel(date: string, today: string): string {
  if (date === today) return 'Today';
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date);
  if (!m) return date;
  const y = Number(m[1]);
  const mo = Number(m[2]);
  const d = Number(m[3]);
  const wd = new Date(Date.UTC(y, mo - 1, d)).getUTCDay();
  return `${WEEKDAYS[wd]} ${d} ${MONTHS[mo - 1]}`;
}

// ---- SIGNALS list filter bar (components/FlagEventList.tsx) ----

export interface SignalFilter {
  kinds: readonly string[]; // selected kind chips; empty = every kind
  sym: string; // symbol search: case-insensitive substring ('' = all)
  from: string; // 'HH:MM' ET inclusive, '' = open
  to: string; // 'HH:MM' ET inclusive, '' = open
  hideBlacklisted: boolean;
}

export const EMPTY_SIGNAL_FILTER: SignalFilter = { kinds: [], sym: '', from: '', to: '', hideBlacklisted: false };

const HHMM = /^([01]\d|2[0-3]):[0-5]\d$/;

/** True when no part of the filter narrows the list. */
export function isFilterOpen(f: SignalFilter): boolean {
  return f.kinds.length === 0 && f.sym.trim() === '' && !HHMM.test(f.from) && !HHMM.test(f.to) && !f.hideBlacklisted;
}

/**
 * filterEvents narrows an already visible/ordered list. Time bounds compare the
 * ET 'HH:MM' of the bar (a malformed bound is ignored); from > to wraps
 * (from OR to). Order is kept.
 */
export function filterEvents(events: readonly FlagEvent[], f: SignalFilter, isBlacklisted: (sym: string) => boolean = () => false): FlagEvent[] {
  if (isFilterOpen(f)) return events.slice();
  const kinds = f.kinds.length ? new Set<string>(f.kinds) : null;
  const q = f.sym.trim().toUpperCase();
  const from = HHMM.test(f.from) ? f.from : '';
  const to = HHMM.test(f.to) ? f.to : '';
  const wrap = from !== '' && to !== '' && from > to;
  return events.filter((e) => {
    if (kinds && !kinds.has(e.kind)) return false;
    if (q && !e.sym.toUpperCase().includes(q)) return false;
    if (from || to) {
      const t = etTime(e.ts);
      if (wrap ? !(t >= from || t <= to) : (from && t < from) || (to && t > to)) return false;
    }
    if (f.hideBlacklisted && isBlacklisted(e.sym)) return false;
    return true;
  });
}

/** The kinds present in events, in a stable display order (known kinds first). */
export function presentKinds(events: readonly FlagEvent[]): string[] {
  const order = Object.keys(BADGES);
  const seen = new Set<string>();
  for (const e of events) if (e.kind !== 'impulse') seen.add(e.kind);
  return [...seen].sort((a, b) => {
    const ia = order.indexOf(a);
    const ib = order.indexOf(b);
    return (ia < 0 ? 999 : ia) - (ib < 0 ? 999 : ib) || a.localeCompare(b);
  });
}

// allKinds: every known signal kind (the badge table order) plus any extra kind
// present in the list, so a chip never disappears just because that signal has
// not fired yet today; counts per kind let the UI dim the empty ones.
export function allKinds(events: readonly FlagEvent[]): { kind: string; n: number }[] {
  const n = new Map<string, number>();
  for (const e of events) if (e.kind !== 'impulse') n.set(e.kind, (n.get(e.kind) ?? 0) + 1);
  const kinds = [...new Set([...Object.keys(BADGES).filter((k) => k !== 'impulse'), ...presentKinds(events)])];
  return kinds.map((kind) => ({ kind, n: n.get(kind) ?? 0 }));
}

export const KINDS_KEY = 'mktscanner.signal.kindChips.v1';

export function loadKindChips(): string[] {
  try {
    const v = JSON.parse(localStorage.getItem(KINDS_KEY) ?? '[]');
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];
  } catch {
    return [];
  }
}

export function saveKindChips(kinds: readonly string[]): void {
  try {
    localStorage.setItem(KINDS_KEY, JSON.stringify(kinds));
  } catch {
    // storage blocked: the selection just doesn't persist
  }
}
