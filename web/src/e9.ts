// 9EMA flag MARKET SCAN — the pure, DOM-free half of its UI: how the e9State
// column reads as a badge, and what a scan alert says out loud. Kept out of
// LiveGrid.tsx / ws.ts so both rules are unit-tested in plain node
// (e9.test.ts) instead of being buried in a component and a websocket handler.
//
// The scan itself runs server-side (internal/flagscan): every stock whose
// pre-market volume is at least 1x its 10-day average daily volume (the pmVolX
// field) gets the SAME 9 EMA flag rules the Signal window runs
// (signals/emaFlag.ts) on its 1-minute bars, and the result lands on the row
// as the e9* fields. e9State is the stage, numbered in the order of the
// engine's FlagState:
//
//   0 idle      a candidate with no setup forming
//   1 impulse   the engine's BREAKOUT: a bar broke out far above the rising
//               EMA — wait for it to go sideways (no entry is possible yet)
//   2 flat      FLAT: sideways above the EMA, the EMA has not caught up
//   3 ready     READY: the EMA is within `readyDistPct` — long above the trigger
//   4 long      LONG: the flag high broke while ready
//
// State 1 is labelled BREAKOUT because that is what the Signal window calls it
// ("BREAKOUT — wait for it to go sideways"). The seeded scan named "breakout"
// lists state 4 (LONG): the break of the FLAG high, the trade itself.

import { fmtDur, fmtPrice } from './format';
import { psFilterTag, psPlanText } from './powerSetup';
import { volBreakText } from './volBreak';

export interface E9Badge {
  state: number;
  label: string; // the word on the badge
  cls: string; // modifier class on .e9-badge (styles.css)
  title: string; // tooltip: what the stage means and what to do about it
}

const E9_BADGES: readonly E9Badge[] = [
  { state: 0, label: 'IDLE', cls: 'idle', title: 'a pre-market-volume candidate, but no 9EMA flag is forming' },
  {
    state: 1,
    label: 'BREAKOUT',
    cls: 'breakout',
    title: 'broke out far above the rising 9 EMA — wait for it to go sideways (no entry yet)',
  },
  {
    state: 2,
    label: 'FLAT',
    cls: 'flat',
    title: 'moving sideways above the 9 EMA — the flag is open, the EMA has not caught up yet',
  },
  {
    state: 3,
    label: 'READY',
    cls: 'ready',
    title: 'the 9 EMA has caught up (within 4%) — long above the trigger price',
  },
  {
    state: 4,
    label: 'LONG',
    cls: 'long',
    title: 'the flag high broke while ready — long, the stop trails the 9 EMA',
  },
];

// e9Badge maps an e9State wire value to its badge, or null for "no badge": the
// field is Null when the symbol is not a candidate (or not evaluated yet), and
// anything that is not a whole stage number is a bug upstream that should read
// as a dash rather than as a plausible-looking wrong label.
export function e9Badge(v: unknown): E9Badge | null {
  if (typeof v !== 'number' || !Number.isInteger(v)) return null;
  return E9_BADGES[v] ?? null;
}

// The 9 EMA SLAM's badge in the Signal window's flag list. Not an e9State stage
// (no wire value maps to it, hence state -1): a slam is an EVENT — one closed
// candle from above the 9 EMA to well under it out of a sideways flag (or a
// long) — and the stage it leaves behind is usually plain idle, which is not
// what a "9EMA Slam" row is about. flagListRows picks it from e9SlamAge.
export const E9_SLAM_BADGE: E9Badge = {
  state: -1,
  label: 'SLAM',
  cls: 'slam',
  title: 'slammed through the 9 EMA: one 1-minute candle from above the EMA to well under it, out of a sideways flag (or a long), closing near its low on volume — a bearish alert, not a trade',
};

// The badges of the market-wide 1-minute alert scans (2026-10-07) in the same
// flag list. Like SLAM they are EVENTS, not e9State stages (no wire value maps
// to them, hence the negative states). The first alert of each rule is a solid
// fill in the candle's colour (DILDO green, CLIMAX red); the second, the "look
// now" entry prompt, is a green outline (PB, BOTTOM); the intrabar TOUCH cue is
// dashed amber (styles.css .e9-badge.dildo/.pb/.touch/.climax/.bottom).
export const DILDO_BADGE: E9Badge = {
  state: -2,
  label: 'DILDO',
  cls: 'dildo',
  title: 'a green dildo: one 1-minute candle up 8%+ open to close on 5x+ the recent volume, closing near its high — alert 1; the scalp comes on the 9 EMA pullback (PB). A "look now" prompt, not a system',
};
export const DILDO_PB_BADGE: E9Badge = {
  state: -3,
  label: 'PB',
  cls: 'pb',
  title: 'after a green dildo, a 1-minute candle dipped to the 9 EMA and closed holding it — the scalp entry prompt (alert 2), at that close. A "look now" prompt, not a system: no edge after costs in calibration',
};
export const DILDO_TOUCH_BADGE: E9Badge = {
  state: -4,
  label: 'TOUCH',
  cls: 'touch',
  title: 'after a green dildo, the forming 1-minute candle is touching the 9 EMA right now — PB follows if it closes holding the EMA. An early "look now" cue, not a system',
};
export const CLIMAX_BADGE: E9Badge = {
  state: -5,
  label: 'CLIMAX',
  cls: 'climax',
  title: 'a volume climax: a 1-minute sell-off candle making a fresh low 6%+ under the 10-bar high on 8x+ the volume, with a long lower wick — alert 1; BOTTOM follows if the low holds and price turns up. A "look now" prompt, not a system',
};
export const BOTTOM_BADGE: E9Badge = {
  state: -6,
  label: 'BOTTOM',
  cls: 'bottom',
  title: "the climax low held and a 1-minute candle closed above the climax candle's midpoint within 5 candles — alert 2, at that close. A \"look now\" prompt, not a system: no edge after costs in calibration",
};

// BOUNCY BALL SHORT (2026-10-14): a solid fill in the red of a breakdown.
export const BBS_BADGE: E9Badge = {
  state: -7,
  label: 'BBS',
  cls: 'bbs',
  title: 'bouncy ball short: a support touched 3+ times with a lower high on each bounce, then a 1-minute candle closed under it. A "look now" prompt, not a system (uncalibrated)',
};
// BOUNCY BALL FORMING (2026-10-14): the base before the break, outlined.
export const BBS_FORM_BADGE: E9Badge = {
  state: -8,
  label: 'FORM',
  cls: 'bbsform',
  title: 'bouncy ball forming: a support touched 3+ times with 2+ lower highs, the last close within 1% of it — watch for the break (BBS). A "look now" prompt, not a system (uncalibrated)',
};

// IGNITION (2026-10-14): the small first candle out of a quiet base, a solid
// amber fill (the spark before the green DILDO).
export const IGN_BADGE: E9Badge = {
  state: -9,
  label: 'IGN',
  cls: 'ign',
  title: 'ignition: the small first candle (+2–8%) out of a quiet 15-candle base (≤ 8% range) on 5x+ volume, closing over the base and making a new high of the day — often right before a dildo. A "look now" prompt, not a system (uncalibrated)',
};

// POWER CANDLE (2026-10-21): a dildo-rule candle with any top wick.
export const POWER_BADGE: E9Badge = {
  state: -10,
  label: 'POWER',
  cls: 'power',
  title: 'power candle: a 1-minute candle passing every green dildo rule (the live tuned settings, new high of the day) except the close-near-high one, so any top wick; a real dildo alerts as a dildo only. A "look now" prompt, not a system',
};

// POWER SETUP (2026-10-27): a power candle that passed both filters, with a trade plan (docs/power-setup.md).
export const POWER_SETUP_BADGE: E9Badge = {
  state: -11,
  label: 'P-SETUP',
  cls: 'power',
  title: 'power setup: a power candle that passed both filters (close < 21% over the day open, 1m 9EMA up 2.1%+ over 3 bars). Plan: buy limit at the 9EMA (valid ~15 min), stop 6% under it, target +3R, trail under the previous 5-minute low. Alerts only - nothing is ordered',
};

// An A+ power setup (psAplus 1: the 15 bars before the candle span <= 5%): sized at 1.5x risk in the order ticket.
export const POWER_SETUP_APLUS_BADGE: E9Badge = {
  ...POWER_SETUP_BADGE,
  label: 'P-SETUP A+',
  title: 'A+ power setup: the 15 one-minute bars before the power candle span 5% or less (a tight base). The order ticket sizes it at 1.5x the selected risk with the Power Setup preset. ' + POWER_SETUP_BADGE.title,
};

// VOLUME BREAK (2026-10-28; docs/volume-break.md): the break of a consolidation high on volume, then its 9 EMA entry.
export const VB_BADGE: E9Badge = {
  state: -12,
  label: 'VOL BRK',
  cls: 'vb',
  title: 'volume break: a stock up 10%+ on 1M+ shares closed a 1-minute candle over a 30+ minute consolidation high (tested twice, at most 12% wide) on 3x+ the range average 1-minute volume. Watch for the first pullback to the 1m 9 EMA (VB 9EMA). Alerts only - nothing is ordered',
};
export const VB_ENTRY_BADGE: E9Badge = {
  state: -13,
  label: 'VB 9EMA',
  cls: 'vb',
  title: 'volume break 9EMA entry: the first pullback after a volume break touched (or came within 0.3% of) the 1m 9 EMA within 30 minutes while still closing over the broken level. Invalid on a close back under the level. Alerts only - nothing is ordered',
};

// The three seeded scans are named "9EMA Flag — sideways|ready|breakout".
// A text-to-speech engine reads "9EMA" badly, so that prefix is spelled out.
const SEEDED_E9_NAME = /^9\s*EMA\s+flag\s*[—–-]\s*(\S.*)$/i;
// The fourth, "9EMA Slam" (cmd/scanner seeds it with Speak on).
const SEEDED_E9_SLAM_NAME = /^9\s*EMA\s+slam$/i;

// The market-wide 1-minute alert rules (2026-10-07), seeded by cmd/scanner
// (dildoScans / climaxScans) with Speak on — except the opt-in touch cue. Each
// entry: the seeded name (any case and spacing), what it is called out loud,
// and its label in the Signal window's picker. "9EMA" is spelled out for the
// speech engine as above: the dildo's second alert says "SYM: nine E M A
// pullback".
const SEEDED_ALERT_NAMES: readonly { re: RegExp; spoken: string; label: string }[] = [
  { re: /^green\s+dildo$/i, spoken: 'HOD dildo', label: 'HOD dildo' },
  { re: /^dildo\s+9\s*EMA\s+pullback$/i, spoken: 'nine E M A pullback', label: 'dildo pullback' },
  { re: /^dildo\s+9\s*EMA\s+touch$/i, spoken: 'nine E M A touch', label: 'dildo touch' },
  { re: /^volume\s+climax$/i, spoken: 'volume climax', label: 'climax' },
  { re: /^bottom\s+confirmed$/i, spoken: 'bottom confirmed', label: 'bottom' },
  { re: /^bouncy\s+ball\s+short$/i, spoken: 'bouncy ball short', label: 'bouncy short' },
  { re: /^bouncy\s+ball\s+forming$/i, spoken: 'bouncy ball forming', label: 'bouncy forming' },
  { re: /^power\s+candle$/i, spoken: 'power candle', label: 'power candle' },
  { re: /^power\s+setup$/i, spoken: 'power setup', label: 'power setup' },
  { re: /^ignition$/i, spoken: 'ignition', label: 'ignition' },
  { re: /^vol\s+break$/i, spoken: 'volume break', label: 'vol break' },
  { re: /^vol\s+break\s+9\s*EMA$/i, spoken: 'vol break nine E M A', label: 'vol break 9ema' },
  { re: /^pm\s+new\s+high$/i, spoken: 'pre-market high', label: 'pm new high' },
  { re: /^daily\s+breakout$/i, spoken: 'daily breakout', label: 'daily breakout' },
  { re: /^pdl\s+break$/i, spoken: 'PDL break', label: 'pdl break' },
  { re: /^vwap\s+bull\s+cross$/i, spoken: 'VWAP bull cross', label: 'vwap bull' },
  { re: /^vwap\s+bear\s+cross$/i, spoken: 'VWAP bear cross', label: 'vwap bear' },
  { re: /^hod\s+flag$/i, spoken: 'HOD flag', label: 'hod flag' },
  { re: /^ep\s+watch$/i, spoken: 'episodic pivot watch, earnings', label: 'ep watch' },
  { re: /^ep\s+break$/i, spoken: 'episodic pivot break', label: 'ep break' },
];

function seededAlertName(n: string): { spoken: string; label: string } | undefined {
  return SEEDED_ALERT_NAMES.find((x) => x.re.test(n));
}

// spokenScanName is what a scan is called out loud: the seeded 9EMA scans lose
// their written prefix ('9EMA Flag — sideways' -> 'nine E M A flag, sideways',
// '9EMA Slam' -> 'nine E M A slam' — the very line the Signal window speaks for
// the slam event itself, signals/view.ts), the seeded dildo / climax scans say
// their alert ('Dildo 9EMA Pullback' -> 'nine E M A pullback', 'Volume Climax'
// -> 'volume climax'), every other scan is simply its own name.
export function spokenScanName(name: string): string {
  const n = (name ?? '').trim();
  if (SEEDED_E9_SLAM_NAME.test(n)) return 'nine E M A slam';
  const alert = seededAlertName(n);
  if (alert) return alert.spoken;
  const m = SEEDED_E9_NAME.exec(n);
  return m ? `nine E M A flag, ${m[1].trim()}` : n;
}

// alertSpeech is the whole utterance, symbol first — the window sits on a
// spare monitor, so the first word has to say WHICH stock: 'BEAT: nine E M A
// flag, ready'.
export function alertSpeech(sym: string, scanName: string): string {
  const spoken = spokenScanName(scanName);
  return spoken ? `${sym}: ${spoken}` : sym;
}

// alertUtterance decides whether an alert frame is spoken and what is said.
// null = say nothing: the alert's scan is not in this window's list (deleted
// since, or the list has not arrived), the scan did not ask for speech, or the
// frame carries no symbol. The caller applies the per-window mute first —
// muting a scan silences its voice along with its tone.
export function alertUtterance(
  scans: ReadonlyArray<{ id: string; name: string; alert?: { speak?: boolean } | null }>,
  scanId: string,
  sym: string,
): string | null {
  if (!sym) return null;
  const scan = scans.find((x) => x.id === scanId);
  if (!scan?.alert?.speak) return null;
  return alertSpeech(sym, scan.name);
}

// ---------------------------------------------------------------------------
// The Signal window's left-bar list (components/FlagScanList.tsx): the 9EMA flag
// market scan's rows, below the watchlist chips, so the stocks the scan finds
// are on screen next to the charts instead of only in a scan window. Everything
// that can be decided without a DOM lives here so it is unit-tested in plain
// node: which scan feeds the list and how that choice is remembered, how the
// scans are ordered in the picker, and what each row says.

// The scans the scanner seeds (cmd/scanner/main.go emaFlagScans): the three
// stage scans, then "9EMA Slam" (e9SlamAge <= 300: a 9 EMA SLAM in the last
// five minutes, a bearish alert). The list's default is "sideways" (e9State >=
// 2): FLAT, READY and LONG names together, which is the whole set of stocks
// holding a flag.
export const SLAM_SCAN_ID = 'e9flag-slam';
export const E9_SCAN_IDS = ['e9flag-sideways', 'e9flag-ready', 'e9flag-breakout', SLAM_SCAN_ID] as const;
export const DEFAULT_FLAG_SCAN_ID: string = E9_SCAN_IDS[0];
export const FLAG_LIST_KEY = 'mktscanner.signal.flagList.v1';

export function isE9ScanId(id: string): boolean {
  return (E9_SCAN_IDS as readonly string[]).includes(id);
}

export function isSlamScanId(id: string): boolean {
  return id === SLAM_SCAN_ID;
}

// The market-wide 1-minute alert scans (2026-10-07; cmd/scanner dildoScans /
// climaxScans). Each rule is two sequential alerts on one symbol:
//   dildo-1m        "Green Dildo"          dildoAge <= 240       says "SYM: HOD dildo"
//   dildo-pb        "Dildo 9EMA Pullback"  dildoPbState == 3     says "SYM: nine E M A pullback"
//   dildo-touch     "Dildo 9EMA Touch"     dildoPbState == 2     the intrabar cue, alert OFF by default
//   climax-1m       "Volume Climax"        climaxAge <= 240      says "SYM: volume climax"
//   climax-confirm  "Bottom Confirmed"     climaxState == 2      says "SYM: bottom confirmed"
// They are NOT 9EMA flag scans: isE9ScanId stays e9-only.
export const DILDO_SCAN_IDS = ['dildo-1m', 'dildo-pb', 'dildo-touch'] as const;
export const CLIMAX_SCAN_IDS = ['climax-1m', 'climax-confirm'] as const;
// BOUNCY BALL SHORT (2026-10-14; cmd/scanner bbsScans): one alert.
//   bouncy-short    "Bouncy Ball Short"    bbsAge <= 240         says "SYM: bouncy ball short"
//   bouncy-forming  "Bouncy Ball Forming"  bbsState == 1         says "SYM: bouncy ball forming" (switch off by default)
export const BBS_SCAN_IDS = ['bouncy-short', 'bouncy-forming'] as const;

export function isBbsScanId(id: string): boolean {
  return (BBS_SCAN_IDS as readonly string[]).includes(id);
}

// IGNITION (2026-10-14; cmd/scanner ignitionScans): one alert.
//   ignition        "Ignition"             ignAge <= 240         says "SYM: ignition"
export const IGNITION_SCAN_IDS = ['ignition'] as const;

export function isIgnitionScanId(id: string): boolean {
  return (IGNITION_SCAN_IDS as readonly string[]).includes(id);
}

// POWER CANDLE (2026-10-21; cmd/scanner powerCandleScans): one alert.
//   power-candle    "Power Candle"         pcAge <= 240          says "SYM: power candle"
export const POWER_SCAN_IDS = ['power-candle'] as const;

export function isPowerScanId(id: string): boolean {
  return (POWER_SCAN_IDS as readonly string[]).includes(id);
}

// POWER SETUP (2026-10-27; cmd/scanner powerSetupScans; docs/power-setup.md): psPass == 1 and psAge <= 900;
// says "SYM: power setup, entry X, stop Y" (powerSetup.ts).
export const POWER_SETUP_SCAN_IDS = ['power-setup'] as const;

export function isPowerSetupScanId(id: string): boolean {
  return (POWER_SETUP_SCAN_IDS as readonly string[]).includes(id);
}

// VOLUME BREAK (2026-10-28; cmd/scanner volBreakScans; docs/volume-break.md): two alerts.
//   vol-break       "Vol Break"       vbAge <= 240                     says "SYM: volume break 3.43 (4.2x vol), nine E M A x.xx"
//   vol-break-9ema  "Vol Break 9EMA"  vbState == 2, vbEntryAge <= 240  says "SYM: vol break nine E M A entry x.xx, level 3.43"
export const VOL_BREAK_SCAN_IDS = ['vol-break', 'vol-break-9ema'] as const;

export function isVolBreakScanId(id: string): boolean {
  return (VOL_BREAK_SCAN_IDS as readonly string[]).includes(id);
}

// PM NEW HIGH (2026-10-14; cmd/scanner pmNewHighScans): pre-market only,
// rvol >= 2 and last at the day's (= pre-market's) high; says "SYM: pre-market high".
export const PM_HIGH_SCAN_IDS = ['pm-new-high'] as const;

// DAILY BREAKOUT (2026-10-16; cmd/scanner dailyBreakScans): a stock over $30
// that crossed a daily level today (14-day high / swing-high pivot / previous
// day's high) and is still above it; alert OFF by default, says "SYM: daily breakout".
export const DAILY_BREAK_SCAN_IDS = ['daily-breakout'] as const;

// PDL BREAK (2026-10-16; cmd/scanner pdlBreakScans): a stock over $25 that traded
// at/above the previous day's low and broke below it today and is still below;
// alert ON by default, says "SYM: PDL break".
export const PDL_BREAK_SCAN_IDS = ['pdl-break'] as const;

// 9EMA VWAP CROSS (2026-10-25; cmd/scanner emaVwapBullScans / emaVwapBearScans): the 9 EMA of 1-minute closes crossing the
// session VWAP (pre-market VWAP before 09:30, regular-hours VWAP after), on stocks over $25 with avg volume 1M+ and RVOL 1.5+;
// evBullAge / evBearAge <= 240; alerts ON by default, says "SYM: VWAP bull cross" / "SYM: VWAP bear cross".
export const EMA_VWAP_SCAN_IDS = ['vwap-bull', 'vwap-bear'] as const;

// HOD FLAG (2026-10-16; cmd/scanner hodFlagScans): the 9EMA flag forming (FLAT) on a run-up that made a new high of the
// day (pre-market included), e9HodFlagAge <= 300; once per flag; alert ON by default, says "SYM: HOD flag".
export const HOD_FLAG_SCAN_IDS = ['hod-flag'] as const;

// EPISODIC PIVOT (cmd/scanner epWatchScans / epBreakScans; docs/episodic-pivot.md): EP Watch = pre-market gap 10%+ on an
// earnings headline in a neglected stock ("SYM: episodic pivot watch, earnings"); EP Break = a qualified stock trading
// above the 09:30-09:35 high between 09:35 and 11:00 ("SYM: episodic pivot break"). Both alerts ON by default.
export const EP_SCAN_IDS = ['ep-watch', 'ep-break'] as const;

export function isDildoScanId(id: string): boolean {
  return (DILDO_SCAN_IDS as readonly string[]).includes(id);
}

export function isClimaxScanId(id: string): boolean {
  return (CLIMAX_SCAN_IDS as readonly string[]).includes(id);
}

// isAlertRuleScanId: one of the five seeded dildo / climax scans.
export function isAlertRuleScanId(id: string): boolean {
  return isDildoScanId(id) || isClimaxScanId(id) || isBbsScanId(id) || isIgnitionScanId(id) || isPowerScanId(id) || isPowerSetupScanId(id) || isVolBreakScanId(id);
}

// The seeded names, for the "not on this scanner yet" text.
const SEEDED_ALERT_SCAN_NAME: ReadonlyMap<string, string> = new Map([
  ['dildo-1m', 'Green Dildo'],
  ['dildo-pb', 'Dildo 9EMA Pullback'],
  ['dildo-touch', 'Dildo 9EMA Touch'],
  ['climax-1m', 'Volume Climax'],
  ['climax-confirm', 'Bottom Confirmed'],
  ['bouncy-short', 'Bouncy Ball Short'],
  ['bouncy-forming', 'Bouncy Ball Forming'],
  ['ignition', 'Ignition'],
  ['power-candle', 'Power Candle'],
  ['power-setup', 'Power Setup'],
  ['vol-break', 'Vol Break'],
  ['vol-break-9ema', 'Vol Break 9EMA'],
  ['pm-new-high', 'PM New High'],
  ['daily-breakout', 'Daily Breakout'],
  ['pdl-break', 'PDL Break'],
  ['vwap-bull', 'VWAP Bull Cross'],
  ['vwap-bear', 'VWAP Bear Cross'],
  ['hod-flag', 'HOD Flag'],
  ['ep-watch', 'EP Watch'],
  ['ep-break', 'EP Break'],
]);

// ---------------------------------------------------------------------------
// The Signal window's per-signal switches (2026-10-08; user: "I want to be able
// to choose every single to switch on and off"). One switch per seeded signal
// scan, in the window's "Alerts" menu (components/SignalSwitchMenu.tsx). A
// switch that is OFF means, in THAT window only:
//   - the scan's alerts neither play nor speak (ws.ts's alert case reads the
//     store's signalScansOff, which SignalPane mirrors from signalScansOff());
//   - the window does not subscribe to the scan (signalAlertScanIds) — unless
//     its left-bar list shows it: the rows need the scan, its alerts stay silent;
//   - the matching chart-cell sound and voice line of the user's own chart
//     stocks is silent too (cellEventHeard, SignalPane.handleCellResult).
// Every other window is unaffected. Replaces the old "Dildo alerts" / "Climax
// alerts" toolbar toggles (their saved values are migrated on first load).
export const SIGNAL_SWITCH_IDS = [
  'e9flag-sideways',
  'e9flag-ready',
  'e9flag-breakout',
  'e9flag-slam',
  'hod-flag',
  'dildo-1m',
  'dildo-pb',
  'dildo-touch',
  'climax-1m',
  'climax-confirm',
  'bouncy-short',
  'bouncy-forming',
  'ignition',
  'power-candle',
  'power-setup',
  'vol-break',
  'vol-break-9ema',
  'pm-new-high',
  'daily-breakout',
  'pdl-break',
  'vwap-bull',
  'vwap-bear',
  'ep-watch',
  'ep-break',
] as const;
export type SignalSwitchId = (typeof SIGNAL_SWITCH_IDS)[number];
export type SignalSwitches = Readonly<Record<SignalSwitchId, boolean>>;

export interface SignalSwitchDef {
  id: SignalSwitchId;
  label: string; // the scan's seeded name, in the menu
  title: string; // tooltip: what it is and what it says out loud
  group: '9EMA flag' | '1-minute alerts';
}

export const SIGNAL_SWITCHES: readonly SignalSwitchDef[] = [
  {
    id: 'e9flag-sideways',
    label: '9EMA Flag sideways',
    group: '9EMA flag',
    title: 'FLAT: going sideways above the 9 EMA ("SYM: nine E M A flag, sideways"; a flag chart cell plays its FLAT tone)',
  },
  {
    id: 'e9flag-ready',
    label: '9EMA Flag ready',
    group: '9EMA flag',
    title: 'READY: the 9 EMA has caught up, long above the trigger ("SYM: nine E M A flag, ready"; a flag chart cell says "9 EMA caught up")',
  },
  {
    id: 'e9flag-breakout',
    label: '9EMA Flag breakout',
    group: '9EMA flag',
    title: 'LONG: the flag high broke while ready ("SYM: nine E M A flag, breakout"; a flag chart cell sounds its LONG / ADD / EXIT)',
  },
  {
    id: 'e9flag-slam',
    label: '9EMA Slam',
    group: '9EMA flag',
    title: 'SLAM: one candle from above the 9 EMA to well under it, a bearish alert ("SYM: nine E M A slam")',
  },
  {
    id: 'hod-flag',
    label: 'HOD Flag',
    group: '9EMA flag',
    title: 'a 9EMA flag starts forming (sideways above the 9 EMA) after a run-up that made a new high of the day, pre-market included ("SYM: HOD flag"); alerts once per flag',
  },
  {
    id: 'dildo-1m',
    label: 'Green Dildo',
    group: '1-minute alerts',
    title: 'alert 1 of the green dildo: one 1-minute candle up 8%+ on 5x+ volume ("SYM: HOD dildo")',
  },
  {
    id: 'dildo-pb',
    label: 'Dildo 9EMA Pullback',
    group: '1-minute alerts',
    title: 'alert 2 of the green dildo: price came back to the 9 EMA and held ("SYM: nine E M A pullback")',
  },
  {
    id: 'dildo-touch',
    label: 'Dildo 9EMA Touch',
    group: '1-minute alerts',
    title: 'the early intrabar cue: the forming candle is touching the 9 EMA after a green dildo ("SYM: nine E M A touch"), off by default',
  },
  {
    id: 'climax-1m',
    label: 'Volume Climax',
    group: '1-minute alerts',
    title: 'alert 1 of the volume climax: a sell-off candle with a fresh low on 8x+ volume ("SYM: volume climax")',
  },
  {
    id: 'climax-confirm',
    label: 'Bottom Confirmed',
    group: '1-minute alerts',
    title: 'alert 2 of the volume climax: the low held and price turned up ("SYM: bottom confirmed")',
  },
  {
    id: 'bouncy-short',
    label: 'Bouncy Ball Short',
    group: '1-minute alerts',
    title: 'a support touched 3+ times with lower highs on each bounce, then a 1-minute close under it ("SYM: bouncy ball short")',
  },
  {
    id: 'bouncy-forming',
    label: 'Bouncy forming',
    group: '1-minute alerts',
    title: 'the bouncy ball base before the break: support with 3+ touches and 2+ lower highs, price within 1% of it ("SYM: bouncy ball forming"), off by default',
  },
  {
    id: 'ignition',
    label: 'Ignition',
    group: '1-minute alerts',
    title: 'the small first candle (+2–8%) out of a quiet base on 5x+ volume, often right before a dildo ("SYM: ignition")',
  },
  {
    id: 'power-candle',
    label: 'Power Candle',
    group: '1-minute alerts',
    title: 'a 1-minute candle passing every green dildo rule (live settings, new high of the day) except the close-near-high one: any top wick, and not a dildo ("SYM: power candle")',
  },
  {
    id: 'power-setup',
    label: 'Power Setup',
    group: '1-minute alerts',
    title: 'a power candle that passed both filters (close < 21% over the day open, 1m 9EMA up 2.1%+ over 3 bars), with a plan: buy limit at the 9EMA, stop 6% under, target +3R ("SYM: power setup, entry X, stop Y")',
  },
  {
    id: 'vol-break',
    label: 'Vol Break',
    group: '1-minute alerts',
    title: 'a stock up 10%+ on 1M+ shares closes a 1-minute candle over a 30+ minute consolidation high (tested twice, at most 12% wide) on 3x+ the range average volume ("SYM: volume break 3.43 (4.2x vol), 9EMA x.xx"); once per level',
  },
  {
    id: 'vol-break-9ema',
    label: 'Vol Break 9EMA',
    group: '1-minute alerts',
    title: 'after a volume break: the first pullback to within 0.3% of the 1m 9 EMA inside 30 minutes, still closing over the level ("SYM: vol break 9EMA entry x.xx, level 3.43"); a close back under the level cancels it',
  },
  {
    id: 'pm-new-high',
    label: 'PM New High',
    group: '1-minute alerts',
    title: 'pre-market only: a stock on 2x+ relative volume trading at a new high of the pre-market; alerts again on each fresh high after a pullback ("SYM: pre-market high")',
  },
  {
    id: 'daily-breakout',
    label: 'Daily Breakout',
    group: '1-minute alerts',
    title: 'a stock over $30 that crossed a daily level today (14-day high, swing-high pivot or the previous day’s high) and is still above it ("SYM: daily breakout"), off by default',
  },
  {
    id: 'pdl-break',
    label: 'PDL Break',
    group: '1-minute alerts',
    title: 'a stock over $25 that traded at/above the previous day’s low and broke below it today (pre-market included) and is still below ("SYM: PDL break"); alerts once per break',
  },
  {
    id: 'vwap-bull',
    label: 'VWAP Bull Cross',
    group: '1-minute alerts',
    title: 'the 9 EMA of 1-minute closes crossed above the session VWAP (pre-market VWAP before 09:30, regular-hours VWAP after) on a stock over $25 with 1M+ average volume and 1.5x+ RVOL ("SYM: VWAP bull cross"); once per cross',
  },
  {
    id: 'vwap-bear',
    label: 'VWAP Bear Cross',
    group: '1-minute alerts',
    title: 'the 9 EMA of 1-minute closes crossed under the session VWAP on a stock over $25 with 1M+ average volume and 1.5x+ RVOL ("SYM: VWAP bear cross"); once per cross',
  },
  {
    id: 'ep-watch',
    label: 'EP Watch',
    group: '1-minute alerts',
    title: 'pre-market: gap 10%+ on $5+ with an earnings headline since the previous close, volume pace 1x+ the average day, and a neglected chart (3-month change +10% or less) ("SYM: episodic pivot watch, earnings"); an alert, not advice',
  },
  {
    id: 'ep-break',
    label: 'EP Break',
    group: '1-minute alerts',
    title: '09:35–11:00: an EP Watch stock trades above its 09:30–09:35 high, once per day ("SYM: episodic pivot break"); the row shows the stop and the risk distance',
  },
];

export function isSignalSwitchId(id: string): id is SignalSwitchId {
  return (SIGNAL_SWITCH_IDS as readonly string[]).includes(id);
}

// Every signal ON, except the opt-in intrabar touch cue (its scan's alert is off
// by default too).
export function defaultSignalSwitches(): SignalSwitches {
  const out = {} as Record<SignalSwitchId, boolean>;
  for (const id of SIGNAL_SWITCH_IDS) out[id] = id !== 'dildo-touch' && id !== 'bouncy-forming' && id !== 'daily-breakout';
  return out;
}

// Remembered per browser (SignalPane reads and writes inside try/catch), under
// a key of its own; the two keys of the toggles it replaces are only read, for
// the migration.
export const SIGNAL_SWITCHES_KEY = 'mktscanner.signal.switches.v2';
export const DILDO_ALERTS_KEY = 'mktscanner.signal.dildoAlerts.v1';
export const CLIMAX_ALERTS_KEY = 'mktscanner.signal.climaxAlerts.v1';

// The old toggles' rule: default ON, only a saved '0' is off.
export function parseAlertToggle(raw: string | null | undefined): boolean {
  return raw !== '0';
}

// parseSignalSwitches reads the saved switches. A JSON object: its boolean
// values over the defaults (a missing or non-boolean entry, e.g. a signal added
// later, takes its default; unknown keys are ignored). Nothing saved, or
// anything unusable: the defaults with the old "Dildo alerts" / "Climax alerts"
// toggles carried over (off = both of that rule's alerts off).
export function parseSignalSwitches(
  raw: string | null | undefined,
  legacyDildo: string | null | undefined,
  legacyClimax: string | null | undefined,
): SignalSwitches {
  const out = { ...defaultSignalSwitches() } as Record<SignalSwitchId, boolean>;
  let v: unknown = null;
  if (raw) {
    try {
      v = JSON.parse(raw);
    } catch {
      v = null;
    }
  }
  if (typeof v === 'object' && v !== null && !Array.isArray(v)) {
    const o = v as Record<string, unknown>;
    for (const id of SIGNAL_SWITCH_IDS) {
      const val = Object.prototype.hasOwnProperty.call(o, id) ? o[id] : undefined;
      if (typeof val === 'boolean') out[id] = val;
    }
    return out;
  }
  if (!parseAlertToggle(legacyDildo)) out['dildo-1m'] = out['dildo-pb'] = false;
  if (!parseAlertToggle(legacyClimax)) out['climax-1m'] = out['climax-confirm'] = false;
  return out;
}

export function serializeSignalSwitches(s: SignalSwitches): string {
  const o: Record<string, boolean> = {};
  for (const id of SIGNAL_SWITCH_IDS) o[id] = s[id] === true;
  return JSON.stringify(o);
}

// signalScansOff: the switched-off signals, in menu order (what ws.ts silences).
export function signalScansOff(s: SignalSwitches): string[] {
  return SIGNAL_SWITCH_IDS.filter((id) => s[id] !== true);
}

// signalAlertScanIds is the Signal window's scan VIEW (view.set): the scan its
// left-bar list shows (always: the list's rows need it, whether or not it is a
// switched-on signal), then every switched-ON signal scan in menu order, each
// only if it exists on the scanner (not yet seeded, or deleted by the user),
// never twice. A switched-off signal is not subscribed (unless the list shows
// it, and then ws.ts keeps its alerts silent).
export function signalAlertScanIds(flagScanId: string, scanIds: Iterable<string>, switches: SignalSwitches): string[] {
  const have = new Set(scanIds);
  const out = [flagScanId];
  for (const id of SIGNAL_SWITCH_IDS) {
    if (switches[id] === true && have.has(id) && !out.includes(id)) out.push(id);
  }
  return out;
}

// Which switches govern a chart cell's event: a flag cell's engine events by
// stage (FLAT = sideways, 9 EMA NEAR = ready, the LONG / ADD / EXIT of the
// trade = breakout, SLAM = slam; FLAG FAILED follows any of the three stages),
// and the dildo / climax rule events merged into a flag cell by their scan.
// Pullback cells run their own engine, which is not one of the nine signals:
// never gated (its kinds overlap the flag engine's, hence the setup).
const FLAG_CELL_SWITCHES: ReadonlyMap<string, readonly SignalSwitchId[]> = new Map<string, readonly SignalSwitchId[]>([
  ['flat', ['e9flag-sideways']],
  ['emaNear', ['e9flag-ready']],
  ['entry', ['e9flag-breakout']],
  ['add', ['e9flag-breakout']],
  ['exit', ['e9flag-breakout']],
  ['failed', ['e9flag-sideways', 'e9flag-ready', 'e9flag-breakout']],
  ['slam', ['e9flag-slam']],
  ['dildo', ['dildo-1m']],
  ['dildoPb', ['dildo-pb']],
  ['dildoCancel', ['dildo-1m', 'dildo-pb']],
  ['climax', ['climax-1m']],
  ['bottom', ['climax-confirm']],
  ['climaxFail', ['climax-1m', 'climax-confirm']],
  ['bbs', ['bouncy-short']],
  ['ignition', ['ignition']],
]);

// cellEventHeard: may a chart cell's event sound / speak in this window? Yes
// unless every switch that governs it is off; an ungoverned event is always yes.
export function cellEventHeard(switches: SignalSwitches, setup: string, kind: string): boolean {
  if (setup !== 'flag') return true;
  const ids = FLAG_CELL_SWITCHES.get(kind);
  if (!ids) return true;
  return ids.some((id) => switches[id] === true);
}

// extraScanMutes lists the per-scan mutes the Signal window's switches take
// over: scans in the window's view other than the list's own (the ones a
// switch added) that are muted. Such a mute was set from the list while the
// list showed that scan; once the list moves on nothing on screen shows it, so
// it would silently swallow that scan's alerts with its switch reading ON. The
// switch is the visible control for those scans, so SignalPane releases them.
export function extraScanMutes(viewIds: readonly string[], flagScanId: string, mutedScans: Readonly<Record<string, boolean>>): string[] {
  return viewIds.filter((id) => id !== flagScanId && mutedScans[id] === true);
}

export interface FlagListPrefs {
  scanId: string; // the scan whose rows the list shows
}

// parseFlagListPrefs reads whatever localStorage handed back for FLAG_LIST_KEY.
// Anything that is not a JSON object with a usable scan id (nothing saved yet, a
// half-written blob, another version's shape, a hand edit) degrades to the
// default scan rather than crashing the pane — same stance as signalLayout's
// loaders. The id is not checked against the scan list here: the scan list
// arrives later, and a scan that is missing is a state the list shows, not one
// to silently overwrite the user's choice for.
export function parseFlagListPrefs(raw: string | null | undefined): FlagListPrefs {
  const fallback: FlagListPrefs = { scanId: DEFAULT_FLAG_SCAN_ID };
  if (!raw) return fallback;
  let v: unknown;
  try {
    v = JSON.parse(raw);
  } catch {
    return fallback;
  }
  if (typeof v !== 'object' || v === null || Array.isArray(v)) return fallback;
  const id = (v as { scanId?: unknown }).scanId;
  if (typeof id !== 'string') return fallback;
  const s = id.trim();
  return s && s.length <= 100 ? { scanId: s } : fallback;
}

// orderFlagScans puts the seeded scans first — the 9EMA stage scans in stage
// order, then the slam scan, then the dildo scans, then the climax scans (the
// ones that exist) — then every other scan in the order the server listed it.
const SEEDED_LIST_ORDER: readonly string[] = [...E9_SCAN_IDS, ...DILDO_SCAN_IDS, ...CLIMAX_SCAN_IDS, ...BBS_SCAN_IDS, ...IGNITION_SCAN_IDS, ...POWER_SCAN_IDS, ...POWER_SETUP_SCAN_IDS, ...VOL_BREAK_SCAN_IDS, ...PM_HIGH_SCAN_IDS, ...DAILY_BREAK_SCAN_IDS, ...PDL_BREAK_SCAN_IDS, ...EMA_VWAP_SCAN_IDS, ...HOD_FLAG_SCAN_IDS, ...EP_SCAN_IDS];

export function orderFlagScans<T extends { id: string }>(scans: readonly T[]): T[] {
  const seeded: T[] = [];
  for (const id of SEEDED_LIST_ORDER) {
    const s = scans.find((x) => x.id === id);
    if (s) seeded.push(s);
  }
  return [...seeded, ...scans.filter((x) => !SEEDED_LIST_ORDER.includes(x.id))];
}

// flagScanLabel is a scan's name in the list's own picker: the seeded scans lose
// their '9EMA Flag — ' / '9EMA ' prefix (the picker sits under a FLAG SCAN
// heading, and the full name would not fit the head), the seeded dildo / climax
// scans get a short label ('green dildo', 'dildo pullback', 'dildo touch',
// 'climax', 'bottom'), every other scan keeps its name.
export function flagScanLabel(name: string): string {
  const n = (name ?? '').trim();
  if (SEEDED_E9_SLAM_NAME.test(n)) return 'slam';
  const alert = seededAlertName(n);
  if (alert) return alert.label;
  const m = SEEDED_E9_NAME.exec(n);
  return m ? m[1].trim() : n;
}

// fmtEmaDist: the 9 EMA distance as a signed percent with one decimal ('+3.5%'),
// narrower than the grid's two-decimal column because the list column is 44px.
// A value that rounds to zero is '+0.0%', never the '-0.0%' toFixed would print.
export function fmtEmaDist(v: unknown): string {
  if (typeof v !== 'number' || !isFinite(v)) return '–';
  const t = v.toFixed(1);
  if (Number(t) === 0) return '+0.0%';
  return `${v >= 0 ? '+' : ''}${t}%`;
}

// The pre-market volume multiple, same drop-the-decimals-as-it-grows rule as
// SideBoxes' fmtPmX (that module pulls in the store, so this is a copy kept in
// step with it, not an import) with the multiplication sign the row title uses.
function fmtPmMultiple(v: unknown): string {
  if (typeof v !== 'number' || !isFinite(v)) return '–';
  if (v >= 99.5) return `${Math.round(v)}×`;
  if (v >= 9.995) return `${v.toFixed(1)}×`;
  return `${v.toFixed(2)}×`;
}

export interface FlagListRow {
  sym: string;
  badge: E9Badge | null; // null = not evaluated / not on this scan's columns (rendered as a dash)
  dist: string; // the middle column (flagListHead): signed percent from the 9 EMA — on the dildo scans the dildo's body, on the climax scans the climax's drop — '–' when unknown
  age: string; // time in the stage (a slam row: since the slam), the grid's own duration format ('45s', '5m', '1.5h'), '–' when unknown
  title: string; // the row's tooltip: the trigger, the flag length and the pre-market multiple (a slam row: how hard and how long ago)
  onChart: boolean; // the symbol is on a chart in this window right now
  ignFresh?: true; // an IGNITION row whose ignition was fresh (ignFresh 1): the FRESH badge
  psTag?: string; // a POWER / P-SETUP row: which power-setup filters passed, e.g. 'A✓ slope✗ gap✓'
  early?: true; // a GREEN DILDO row whose dildo was an early one (dildoEarly 1): the EARLY badge
}

// How long a slam outranks the stage on a row that carries both (a scan of the
// user's own with e9State AND e9SlamAge columns): the seeded "9EMA Slam" scan's
// own window. Within it the stage is whatever the slam left behind (idle, in
// practice), which says less than "it just slammed"; after it, the stage is the
// newer news.
const SLAM_FRESH_SEC = 300;

// flagListRows turns one scan's grid (its server-ordered symbol list and the
// row object per symbol) into the list's rows. ORDER IS THE SCAN'S OWN — the
// seeded "sideways" scan sorts strongest stage first, then closest to its EMA,
// and re-sorting here would hide that. Every field is optional on the wire (a
// scan that does not carry the e9* columns, a row that has not streamed yet): a
// missing or non-numeric value is a dash, never a thrown error or a 'NaN'.
//
// A SLAM row — one carrying a slam age (e9SlamAge: the "9EMA Slam" scan's rows,
// which have no e9State / e9Age columns at all) — is badged SLAM and aged by
// the slam, unless the row also carries a real stage and the slam is older than
// SLAM_FRESH_SEC.
//
// The five seeded dildo / climax scans (pass the list's scanId) badge every row
// by the scan — their rows carry no e9 fields — show the dildo's body or the
// climax's drop in the middle column (flagListHead names it) and age the row by
// the scan's own event. A scan of the user's own that carries their age fields
// but no e9 stage or slam badges each row by its NEWEST event (alertKindOfRow).
export function flagListRows(
  order: readonly string[],
  rows: Readonly<Record<string, Readonly<Record<string, unknown>>>>,
  onChart: ReadonlySet<string>,
  scanId?: string,
): FlagListRow[] {
  const seededKind = scanId != null ? ALERT_KIND_BY_SCAN.get(scanId) : undefined;
  return order.map((sym) => {
    const r = rows[sym] ?? {};
    if (seededKind) return alertListRow(sym, r, seededKind, alertMid(seededKind, r), onChart.has(sym));
    const stage = e9Badge(r.e9State);
    const slamAge = typeof r.e9SlamAge === 'number' && isFinite(r.e9SlamAge) && r.e9SlamAge >= 0 ? r.e9SlamAge : null;
    if (!stage && slamAge == null) {
      const kind = alertKindOfRow(r);
      if (kind) return alertListRow(sym, r, kind, fmtEmaDist(r.e9Dist), onChart.has(sym));
    }
    if (slamAge != null && (!stage || slamAge <= SLAM_FRESH_SEC)) {
      const pct = typeof r.e9SlamPct === 'number' && isFinite(r.e9SlamPct) ? r.e9SlamPct : null;
      const how = pct != null ? `slammed ${Math.abs(pct).toFixed(1)}% under the 9 EMA` : 'slammed through the 9 EMA';
      const age = fmtDur(slamAge);
      return {
        sym,
        badge: E9_SLAM_BADGE,
        dist: fmtEmaDist(r.e9Dist),
        age,
        title: `${how} · ${age} ago · pm vol ${fmtPmMultiple(r.pmVolX)}`,
        onChart: onChart.has(sym),
      };
    }
    const trigger = typeof r.e9Trigger === 'number' ? fmtPrice(r.e9Trigger) : '–';
    const bars = typeof r.e9FlagBars === 'number' && isFinite(r.e9FlagBars) ? String(Math.round(r.e9FlagBars)) : '–';
    return {
      sym,
      badge: stage,
      dist: fmtEmaDist(r.e9Dist),
      age: typeof r.e9Age === 'number' ? fmtDur(r.e9Age) : '–',
      title: `long above ${trigger} · flag ${bars} bars · pm vol ${fmtPmMultiple(r.pmVolX)}`,
      onChart: onChart.has(sym),
    };
  });
}

// ---------------------------------------------------------------------------
// The dildo / climax alert scans in the left-bar list (2026-10-07).

type AlertRowKind = 'dildo' | 'dildoPb' | 'dildoTouch' | 'climax' | 'bottom' | 'bbs' | 'bbsForm' | 'ign' | 'power' | 'powerSetup' | 'vb' | 'vbEntry';

// A Map, not an object literal: a scan id is user data and must never hit an
// Object.prototype key.
const ALERT_KIND_BY_SCAN: ReadonlyMap<string, AlertRowKind> = new Map<string, AlertRowKind>([
  ['dildo-1m', 'dildo'],
  ['dildo-pb', 'dildoPb'],
  ['dildo-touch', 'dildoTouch'],
  ['climax-1m', 'climax'],
  ['climax-confirm', 'bottom'],
  ['bouncy-short', 'bbs'],
  ['bouncy-forming', 'bbsForm'],
  ['ignition', 'ign'],
  ['power-candle', 'power'],
  ['power-setup', 'powerSetup'],
  ['vol-break', 'vb'],
  ['vol-break-9ema', 'vbEntry'],
]);

// Each event's age field (seconds since its candle started) and badge.
const ALERT_AGE_KEY: Readonly<Record<AlertRowKind, string>> = {
  dildo: 'dildoAge',
  dildoPb: 'dildoPbAge',
  dildoTouch: 'dildoTouchAge',
  climax: 'climaxAge',
  bottom: 'climaxConfAge',
  bbs: 'bbsAge',
  bbsForm: 'bbsFormAge', // no such field (a base has no event time): the age column reads '–'
  ign: 'ignAge',
  power: 'pcAge',
  powerSetup: 'psAge',
  vb: 'vbAge',
  vbEntry: 'vbEntryAge',
};
const ALERT_BADGE: Readonly<Record<AlertRowKind, E9Badge>> = {
  dildo: DILDO_BADGE,
  dildoPb: DILDO_PB_BADGE,
  dildoTouch: DILDO_TOUCH_BADGE,
  climax: CLIMAX_BADGE,
  bottom: BOTTOM_BADGE,
  bbs: BBS_BADGE,
  bbsForm: BBS_FORM_BADGE,
  ign: IGN_BADGE,
  power: POWER_BADGE,
  powerSetup: POWER_SETUP_BADGE,
  vb: VB_BADGE,
  vbEntry: VB_ENTRY_BADGE,
};

const isDildoKind = (k: AlertRowKind): boolean => k === 'dildo' || k === 'dildoPb' || k === 'dildoTouch';

// The order a tie between two ages is broken in (a scan of the user's own):
// the second alert outranks the first, and a confirmed pullback outranks the
// touch on the same candle (the touch candle IS the pullback candle once it
// closes holding the EMA, so the two ages are equal then).
const ALERT_KIND_ORDER: readonly AlertRowKind[] = ['dildoPb', 'bottom', 'dildoTouch', 'dildo', 'climax', 'bbs', 'ign', 'power', 'powerSetup', 'vbEntry', 'vb'];

function finite(v: unknown): number | null {
  return typeof v === 'number' && isFinite(v) ? v : null;
}

function ageOf(v: unknown): number | null {
  const n = finite(v);
  return n != null && n >= 0 ? n : null;
}

// With the state column on the row, an age only counts in its own state: a
// touch that was then cancelled keeps its touch time, and that is history, not
// TOUCH; likewise a pullback age outside state 3 and a bottom age outside
// climax state 2 (which the server never sends, but a row is wire data).
function stateAllows(kind: AlertRowKind, r: Readonly<Record<string, unknown>>): boolean {
  const pb = finite(r.dildoPbState);
  const cl = finite(r.climaxState);
  if (kind === 'dildoTouch') return pb == null || pb === 2;
  if (kind === 'dildoPb') return pb == null || pb === 3;
  if (kind === 'bottom') return cl == null || cl === 2;
  if (kind === 'vbEntry') return finite(r.vbState) == null || r.vbState === 2;
  return true;
}

// alertKindOfRow: the newest dildo / climax event a row carries, or null.
function alertKindOfRow(r: Readonly<Record<string, unknown>>): AlertRowKind | null {
  let best: AlertRowKind | null = null;
  let bestAge = Infinity;
  for (const k of ALERT_KIND_ORDER) {
    const age = ageOf(r[ALERT_AGE_KEY[k]]);
    if (age == null || !stateAllows(k, r)) continue;
    if (age < bestAge) {
      best = k;
      bestAge = age;
    }
  }
  return best;
}

// The middle column of a seeded alert scan's row: the dildo's body (a rise,
// '+18.4%') or the climax's drop off the 10-bar high (a fall, '-13.6%').
function alertMid(kind: AlertRowKind, r: Readonly<Record<string, unknown>>): string {
  if (isDildoKind(kind)) return fmtEmaDist(r.dildoBody);
  if (kind === 'bbsForm') return fmtEmaDist(r.bbsDistPct);
  if (kind === 'ign') return fmtEmaDist(r.ignBodyPct);
  if (kind === 'power') return fmtEmaDist(r.pcBodyPct);
  if (kind === 'powerSetup') return fmtEmaDist(r.psFromOpen);
  if (kind === 'vb' || kind === 'vbEntry') {
    const lvl = finite(r.vbLevel);
    const last = finite(r.last);
    return lvl != null && last != null && lvl > 0 ? fmtEmaDist(((last - lvl) / lvl) * 100) : '–';
  }
  if (kind === 'bbs') {
    const sup = finite(r.bbsSupport);
    const last = finite(r.last);
    return sup != null && last != null && sup > 0 ? fmtEmaDist(((last - sup) / sup) * 100) : '–';
  }
  const drop = finite(r.climaxDropPct);
  return drop == null ? '–' : fmtEmaDist(-drop);
}

// A candle's dollar volume as the climax notes write it: '$717k', '$1.2M'.
function fmtDollarVol(v: number): string {
  return v < 1e6 ? `$${Math.round(v / 1e3)}k` : `$${(v / 1e6).toFixed(1)}M`;
}

// The row's tooltip: what happened, in the words of the event, then how long ago.
// Every number is optional (a row that has not streamed it yet): the phrase
// for it drops out, never 'NaN'.
function alertTitle(kind: AlertRowKind, r: Readonly<Record<string, unknown>>, age: string | null): string {
  const parts: string[] = [];
  const body = finite(r.dildoBody);
  const bodyTxt = body != null ? `${fmtEmaDist(body)} body` : null;
  const volume = (v: number | null) => {
    if (v != null) parts.push(`${v.toFixed(1)}× volume`);
  };
  const dollars = (v: number | null) => {
    if (v != null && v >= 0) parts.push(`${fmtDollarVol(v)} candle`);
  };
  switch (kind) {
    case 'dildo':
      parts.push(bodyTxt ? `green dildo ${bodyTxt}` : 'green dildo');
      volume(finite(r.dildoVolX));
      dollars(finite(r.dildoDvol));
      break;
    case 'dildoPb': {
      const px = finite(r.dildoPbPx);
      parts.push(px != null ? `pulled back to the 9 EMA and held: closed ${fmtPrice(px)}` : 'pulled back to the 9 EMA and held');
      if (bodyTxt) parts.push(`dildo ${bodyTxt}`);
      break;
    }
    case 'dildoTouch':
      parts.push('touching the 9 EMA now (the candle is still forming)');
      if (bodyTxt) parts.push(`dildo ${bodyTxt}`);
      break;
    case 'climax': {
      const low = finite(r.climaxLow);
      const drop = finite(r.climaxDropPct);
      const what: string[] = [];
      if (low != null) what.push(`low ${fmtPrice(low)}`);
      if (drop != null) what.push(`${drop.toFixed(1)}% under the 10-bar high`);
      parts.push(what.length ? `volume climax: ${what.join(', ')}` : 'volume climax');
      volume(finite(r.climaxVolX));
      dollars(finite(r.climaxDvol));
      break;
    }
    case 'bottom': {
      const px = finite(r.climaxConfPx);
      const low = finite(r.climaxLow);
      parts.push(px != null ? `bottom confirmed: closed ${fmtPrice(px)}` : 'bottom confirmed');
      if (low != null) {
        const risk = px != null && px > 0 ? ` (${(((px - low) / px) * 100).toFixed(1)}% under)` : '';
        parts.push(`climax low ${fmtPrice(low)}${risk}`);
      }
      break;
    }
    case 'bbs': {
      const sup = finite(r.bbsSupport);
      parts.push(sup != null ? `bouncy ball short: closed under the ${fmtPrice(sup)} support` : 'bouncy ball short');
      dollars(finite(r.bbsDvol));
      break;
    }
    case 'bbsForm': {
      const sup = finite(r.bbsSupport);
      const d = finite(r.bbsDistPct);
      parts.push(sup != null ? `bouncy ball forming over the ${fmtPrice(sup)} support` : 'bouncy ball forming');
      if (d != null) parts.push(`${fmtEmaDist(d)} above it`);
      break;
    }
    case 'power': {
      const b = finite(r.pcBodyPct);
      const w = finite(r.pcTopPct);
      parts.push(b != null ? `power candle ${fmtEmaDist(b)} body` : 'power candle');
      if (w != null) parts.push(`${w.toFixed(0)}% top wick`);
      volume(finite(r.pcVolX));
      dollars(finite(r.pcDvol));
      parts.push(psFilterTag(r));
      break;
    }
    case 'powerSetup': {
      parts.push(`power setup: ${psFilterTag(r)}`);
      parts.push(psPlanText(r));
      break;
    }
    case 'vb': {
      const t = volBreakText('vol-break', r);
      parts.push(t ? `volume break ${t}` : 'volume break');
      break;
    }
    case 'vbEntry': {
      const t = volBreakText('vol-break-9ema', r);
      parts.push(t ? `vol break 9EMA ${t}` : 'vol break 9EMA entry');
      break;
    }
    case 'ign': {
      const b = finite(r.ignBodyPct);
      parts.push(b != null ? `ignition ${fmtEmaDist(b)} candle out of a quiet base` : 'ignition out of a quiet base');
      volume(finite(r.ignVolX));
      break;
    }
  }
  if (age) parts.push(`${age} ago`);
  return parts.join(' · ');
}

function alertListRow(sym: string, r: Readonly<Record<string, unknown>>, kind: AlertRowKind, mid: string, onChart: boolean): FlagListRow {
  const ageSec = ageOf(r[ALERT_AGE_KEY[kind]]);
  const age = ageSec != null ? fmtDur(ageSec) : '–';
  const row: FlagListRow = { sym, badge: ALERT_BADGE[kind], dist: mid, age, title: alertTitle(kind, r, ageSec != null ? age : null), onChart };
  if (kind === 'dildo' && r.dildoEarly === 1) row.early = true;
  if (kind === 'ign' && r.ignFresh === 1) row.ignFresh = true;
  if (kind === 'powerSetup' && r.psAplus === 1) row.badge = POWER_SETUP_APLUS_BADGE;
  if ((kind === 'power' || kind === 'powerSetup') && (finite(r.psFromOpen) != null || finite(r.psEmaSlope) != null || finite(r.psGap) != null)) row.psTag = psFilterTag(r);
  return row;
}

// flagListHead: what the list's middle and age columns hold for this scan —
// the header word, and the field whose hint is each header's tooltip.
export interface FlagListHead {
  mid: string; // 'EMA' (the 9EMA scans and every other scan), 'Body' (the dildo scans), 'Drop' (the climax scans), 'Sup' (the bouncy ball short: last vs its support)
  midKey: string;
  ageKey: string;
}

export function flagListHead(scanId: string): FlagListHead {
  const kind = ALERT_KIND_BY_SCAN.get(scanId);
  if (kind === 'bbs') return { mid: 'Sup', midKey: 'bbsSupport', ageKey: ALERT_AGE_KEY[kind] };
  if (kind === 'bbsForm') return { mid: 'Sup', midKey: 'bbsDistPct', ageKey: 'bbsAge' };
  if (kind === 'powerSetup') return { mid: 'From open', midKey: 'psFromOpen', ageKey: ALERT_AGE_KEY[kind] };
  if (kind === 'power') return { mid: 'Body', midKey: 'pcBodyPct', ageKey: ALERT_AGE_KEY[kind] };
  if (kind === 'ign') return { mid: 'Body', midKey: 'ignBodyPct', ageKey: ALERT_AGE_KEY[kind] };
  if (kind === 'vb' || kind === 'vbEntry') return { mid: 'Level', midKey: 'vbLevel', ageKey: ALERT_AGE_KEY[kind] };
  if (kind) {
    return isDildoKind(kind)
      ? { mid: 'Body', midKey: 'dildoBody', ageKey: ALERT_AGE_KEY[kind] }
      : { mid: 'Drop', midKey: 'climaxDropPct', ageKey: ALERT_AGE_KEY[kind] };
  }
  return { mid: 'EMA', midKey: 'e9Dist', ageKey: isSlamScanId(scanId) ? 'e9SlamAge' : 'e9Age' };
}

// flagListMissingText: the list's body when its scan is not on the scanner.
export function flagListMissingText(scanId: string): string {
  if (isSlamScanId(scanId)) {
    return 'The 9EMA Slam scan is not on this scanner yet — restarting it with run.bat seeds it (once: a deleted one is not brought back).';
  }
  const seeded = SEEDED_ALERT_SCAN_NAME.get(scanId);
  if (seeded) return `The ${seeded} scan is not on this scanner yet — restarting it with run.bat seeds it (once: a deleted one is not brought back).`;
  if (isE9ScanId(scanId)) return 'The 9EMA flag scans are not on this scanner — restart it with run.bat to seed them.';
  return 'That scan no longer exists — pick another one above.';
}

// flagListEmptyText: the list's body when its scan has no rows right now —
// what the scan is waiting for, so an empty list reads as "nothing yet", not
// "broken".
export function flagListEmptyText(scanId: string, scanName: string): string {
  switch (scanId) {
    case SLAM_SCAN_ID:
      return 'No 9 EMA slams in the last 5 minutes — the scan watches pre-market volume ≥ 1× the 10-day average.';
    case 'power-setup':
      return 'No power setup in the last 15 minutes — a power candle that also passes both filters (close < 21% over the open, 9EMA up 2.1%+).';
    case 'dildo-1m':
      return 'No green dildo in the last 4 minutes — the scan watches every stock, from $0.20 with a $100k+ candle.';
    case 'dildo-pb':
      return 'No 9 EMA pullback in the last 4 minutes — one can follow a green dildo for up to 20 minutes.';
    case 'dildo-touch':
      return 'No stock touching the 9 EMA after a green dildo right now.';
    case 'climax-1m':
      return 'No volume climax in the last 4 minutes — the scan watches every stock, from $0.50 with a $50k+ candle.';
    case 'climax-confirm':
      return 'No bottom confirmed in the last 4 minutes — one can follow a volume climax within 5 candles.';
    case 'bouncy-short':
      return 'No bouncy ball short in the last 4 minutes — the scan watches every stock, from $1 with a $100k+ breaking candle.';
    case 'bouncy-forming':
      return 'No bouncy ball base forming right now — the scan watches every stock from $1 with 200k+ shares traded today.';
    case 'ignition':
      return 'No ignition in the last 4 minutes — the scan watches every stock, from $0.20 with a $50k+ candle.';
    case 'power-candle':
      return 'No power candle in the last 4 minutes — the scan watches every stock for a dildo-sized candle with a top wick.';
    case 'vol-break':
      return 'No volume break in the last 4 minutes — a stock up 10%+ on 1M+ shares closing over a 30+ minute range high on 3x+ volume.';
    case 'vol-break-9ema':
      return 'No 9 EMA entry after a volume break in the last 4 minutes — the first pullback must come within 30 minutes of the break.';
    case 'pm-new-high':
      return 'No stock at a new pre-market high on 2x+ relative volume right now (the scan only matches in the pre-market).';
    case 'daily-breakout':
      return 'No stock over $30 has crossed a daily level (14-day high, pivot or previous high) and held above it right now.';
    case 'hod-flag':
      return 'No 9EMA flag forming after a new high of the day in the last 5 minutes — the scan watches pre-market volume ≥ 1× the 10-day average.';
    case 'pdl-break':
      return 'No stock over $25 has broken below the previous day’s low and stayed under it right now.';
    case 'vwap-bull':
      return 'No 9 EMA cross above the VWAP in the last 4 minutes — the scan watches stocks over $25 with 1M+ average volume and 1.5x+ RVOL.';
    case 'vwap-bear':
      return 'No 9 EMA cross under the VWAP in the last 4 minutes — the scan watches stocks over $25 with 1M+ average volume and 1.5x+ RVOL.';
    case 'ep-watch':
      return 'No episodic pivot candidate right now — needs a pre-market gap of 10%+ on $5+, an earnings headline since the previous close, volume pace and a 3-month change of +10% or less.';
    case 'ep-break':
      return 'No episodic pivot has broken its 09:30–09:35 high yet (09:35–11:00 only).';
  }
  return isE9ScanId(scanId)
    ? 'No stocks flagging right now — the scan watches pre-market volume ≥ 1× the 10-day average.'
    : `No stocks in ${scanName} right now.`;
}
