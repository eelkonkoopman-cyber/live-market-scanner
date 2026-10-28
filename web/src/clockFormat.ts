// Cached Intl formatters for the clock strings the panels print once per row.
//
// `date.toLocaleTimeString('en-GB', { hour12: false })` builds a brand-new
// Intl.DateTimeFormat on EVERY call (V8 only caches the option-less calls):
// ~120 us each, so 500 news rows or 200 activity rows cost 25-60 ms a render.
// By the spec those methods are exactly `new Intl.DateTimeFormat(locale,
// options).format(date)` with the fields they default (ToDateTimeOptions)
// spelled out, so ONE instance per format, built with those defaults written
// in, prints the identical string (clockFormat.test.ts pins that against the
// original calls, DST nights and midnight included). A format() call is ~4 us.
//
// An instance fixes its time zone when it is built: a machine whose zone is
// switched while a window is open keeps printing the old zone until that
// window reloads (a DST change inside the zone is fine — the instance holds
// the zone's rules, not an offset).

export interface ClockFormats {
  /** `new Date(ts).toLocaleTimeString('en-GB', { hour12: false })` — "13:05:09" (midnight is "00:..."). */
  hms24: (ts: number) => string;
  /** `new Date(ts).toLocaleTimeString('en-US', { hour12: false })` — same string as the native call, midnight quirk included. */
  hms24US: (ts: number) => string;
  /** `new Date(ts).toLocaleDateString('en-GB', { day: '2-digit', month: 'short' })` — "05 Oct". */
  dayMonth: (ts: number) => string;
}

// What toLocale*String returns for an invalid time value; DateTimeFormat.format
// would throw a RangeError instead, which would take a whole React render down.
const INVALID_DATE = 'Invalid Date';

function viaDate(f: Intl.DateTimeFormat): (ts: number) => string {
  return (ts) => {
    const d = new Date(ts);
    return Number.isNaN(d.getTime()) ? INVALID_DATE : f.format(d);
  };
}

/** `timeZone` is for the tests; the app uses the machine's own zone (the default instance below). */
export function createClockFormats(timeZone?: string): ClockFormats {
  const hms = { hour12: false, hour: 'numeric', minute: 'numeric', second: 'numeric', timeZone } as const;
  return {
    hms24: viaDate(new Intl.DateTimeFormat('en-GB', hms)),
    hms24US: viaDate(new Intl.DateTimeFormat('en-US', hms)),
    dayMonth: viaDate(new Intl.DateTimeFormat('en-GB', { day: '2-digit', month: 'short', timeZone })),
  };
}

const local = createClockFormats();

export const fmtClock = local.hms24;
export const fmtClockUS = local.hms24US;
export const fmtDayMonth = local.dayMonth;
