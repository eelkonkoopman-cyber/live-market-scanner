// Pure US-market wall-clock helpers: no React, no store, nothing that reads
// `Date.now()` itself — every function takes the current time as `nowMs` so
// callers (the StatusBar clock pill; marketClock.test.ts) can pin it down.
// Time-zone math goes through Intl rather than a fixed UTC offset so it
// stays correct across the EST/EDT transitions.
//
// No holiday calendar anywhere in here — same documented gap as the rest of
// the app's session handling — Thanksgiving, early closes, etc. are not
// accounted for; boundaries are the plain Mon-Fri 04:00/09:30/16:00/20:00 ET
// schedule.

// One instant, decomposed into US-Eastern wall-clock fields.
export interface EtParts {
  hh: number;
  mm: number;
  ss: number;
  weekday: number; // 0 Sun .. 6 Sat
  minutes: number; // hh*60 + mm, for comparing against boundary times
}

export type BoundaryKind = 'pre' | 'open' | 'close' | 'ah';

export interface Boundary {
  kind: BoundaryKind;
  atMs: number;
}

const WEEKDAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

const ET_FORMAT = new Intl.DateTimeFormat('en-US', {
  timeZone: 'America/New_York',
  hour12: false,
  weekday: 'short',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
});

// Same construction as the vendored chart engine's own VWAP day-reset key
// (indicators/registry.ts's private `etDate`) — 'en-CA' formats as
// YYYY-MM-DD directly, so there is no field-reassembly to get wrong. Exported
// (registry.ts's copy is module-private) so the signal engine's per-day
// grouping (shortFade.ts) buckets candles into the SAME trading day the
// chart's own VWAP study resets on; two independent copies of this
// construction would be a silent-drift risk the moment either one's Intl
// options changed.
const ET_DAY_FORMAT = new Intl.DateTimeFormat('en-CA', {
  timeZone: 'America/New_York',
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
});

/** 'YYYY-MM-DD' in America/New_York for a ms timestamp. */
export function etDayKey(ms: number): string {
  return ET_DAY_FORMAT.format(new Date(ms));
}

// Decompose an instant into US-Eastern wall-clock fields.
export function etParts(nowMs: number): EtParts {
  let hh = 0;
  let mm = 0;
  let ss = 0;
  let weekday = 0;
  for (const p of ET_FORMAT.formatToParts(new Date(nowMs))) {
    if (p.type === 'hour') hh = Number(p.value) % 24; // hour12:false emits '24' at midnight
    else if (p.type === 'minute') mm = Number(p.value);
    else if (p.type === 'second') ss = Number(p.value);
    else if (p.type === 'weekday') weekday = WEEKDAYS.indexOf(p.value);
  }
  return { hh, mm, ss, weekday, minutes: hh * 60 + mm };
}

// The four daily session boundaries, in order, as minutes-since-midnight ET.
const BOUNDARIES: { kind: BoundaryKind; minutes: number }[] = [
  { kind: 'pre', minutes: 4 * 60 },
  { kind: 'open', minutes: 9 * 60 + 30 },
  { kind: 'close', minutes: 16 * 60 },
  { kind: 'ah', minutes: 20 * 60 },
];

const DAY_MS = 86_400_000;

// The next of pre-market-open (04:00), open (09:30), close (16:00), or
// after-hours-end (20:00) ET, in that daily order. Weekends have none of
// these boundaries — if the one picked would land on a Saturday or Sunday,
// this rolls forward to Monday 04:00 ET instead.
export function nextBoundary(nowMs: number): Boundary {
  const now = etParts(nowMs);

  let days = 0;
  let targetMinutes = BOUNDARIES[0].minutes;
  let kind: BoundaryKind = 'pre';
  const today = BOUNDARIES.find((b) => b.minutes > now.minutes);
  if (today) {
    targetMinutes = today.minutes;
    kind = today.kind;
  } else {
    days = 1; // every boundary today has passed — next one is tomorrow's pre-market
  }

  // Whatever we picked, if it lands on a weekend, push to Monday 04:00 —
  // there is nothing to hit on a Saturday or Sunday.
  const targetWeekday = (now.weekday + days) % 7;
  if (targetWeekday === 6) {
    days += 2; // Saturday -> Monday
    targetMinutes = BOUNDARIES[0].minutes;
    kind = 'pre';
  } else if (targetWeekday === 0) {
    days += 1; // Sunday -> Monday
    targetMinutes = BOUNDARIES[0].minutes;
    kind = 'pre';
  }

  let atMs = nowMs + (targetMinutes - now.minutes) * 60_000 - now.ss * 1000 + days * DAY_MS;

  // The arithmetic above assumes a fixed ET/UTC offset across the whole gap,
  // which is wrong exactly twice a year (spring-forward/fall-back). Re-derive
  // the ET wall-clock time we actually landed on and nudge by one hour if a
  // DST transition moved it off the intended HH:MM. Good enough — a boundary
  // that falls ON a transition night can still end up up to an hour off.
  const targetHH = Math.floor(targetMinutes / 60);
  const targetMM = targetMinutes % 60;
  const landed = etParts(atMs);
  if (landed.hh !== targetHH || landed.mm !== targetMM) {
    const forward = etParts(atMs + 3_600_000);
    const back = etParts(atMs - 3_600_000);
    if (forward.hh === targetHH && forward.mm === targetMM) atMs += 3_600_000;
    else if (back.hh === targetHH && back.mm === targetMM) atMs -= 3_600_000;
  }

  return { kind, atMs };
}

// Short countdown text: "5h 18m" at an hour or more, "12m" at a minute or
// more, else "45s". Never negative — a boundary already passed (or exactly
// now) reads as "0s".
export function countdown(ms: number): string {
  if (ms <= 0) return '0s';
  const totalSec = Math.floor(ms / 1000);
  const hh = Math.floor(totalSec / 3600);
  const mm = Math.floor((totalSec % 3600) / 60);
  const ss = totalSec % 60;
  if (hh >= 1) return `${hh}h ${mm}m`;
  if (mm >= 1) return `${mm}m`;
  return `${ss}s`;
}

const BOUNDARY_LABEL: Record<BoundaryKind, string> = {
  pre: 'pre-market in',
  open: 'opens in',
  close: 'closes in',
  ah: 'AH ends in',
};

// Status-bar clock pill's countdown line, e.g. "opens in 5h 18m". `session`
// is the server's current session label ('pre' | 'regular' | 'post', else
// treated as closed) and is accepted here because callers naturally have it
// (see StatusBar) and it documents intent, but the wording is always driven
// by nextBoundary's OWN kind rather than a lookup keyed on `session`: in the
// well-formed case the two already agree — 'pre' naturally has 'open' next,
// 'regular' has 'close', 'post' has 'ah', closed has 'pre' — so deriving the
// wording from `session` would just recompute the same string. If the two
// ever disagree (client/server clock skew, a stale frame) describing
// whichever boundary is ACTUALLY next is correct; forcing the session's
// implied boundary could describe one already behind us.
export function sessionLine(nowMs: number, session: string): string {
  const boundary = nextBoundary(nowMs);
  return `${BOUNDARY_LABEL[boundary.kind]} ${countdown(boundary.atMs - nowMs)}`;
}
