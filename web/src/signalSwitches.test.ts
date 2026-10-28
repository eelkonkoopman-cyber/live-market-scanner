// The Signal window's per-signal on/off switches (2026-10-08, user: "I also
// want to be able to switch off the flag signal. I want to be able to choose
// every single to switch on and off."). One switch per seeded signal scan, in
// the window's "Alerts" menu: OFF = that scan's alerts make no sound and say
// nothing in THIS window (other windows are unaffected), the window does not
// subscribe to it (unless its left-bar list shows it — the rows need the scan),
// and the matching chart-cell sound of the user's own chart stocks is silent
// too. These pin the pure rules (e9.ts) and the alert path (ws.ts) end to end.
import { describe, expect, it, vi } from 'vitest';
import {
  CLIMAX_ALERTS_KEY,
  DILDO_ALERTS_KEY,
  SIGNAL_SWITCHES,
  SIGNAL_SWITCHES_KEY,
  SIGNAL_SWITCH_IDS,
  cellEventHeard,
  defaultSignalSwitches,
  isSignalSwitchId,
  parseSignalSwitches,
  serializeSignalSwitches,
  signalAlertScanIds,
  signalScansOff,
  type SignalSwitches,
} from './e9';

vi.mock('./audio', () => ({ play: vi.fn() }));
vi.mock('./voice', () => ({ speak: vi.fn(), speakRegimeAlert: vi.fn(), speakCard: vi.fn() }));

const ALL_IDS = [
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
];

const withOff = (...off: string[]): SignalSwitches => {
  const s = { ...defaultSignalSwitches() } as Record<string, boolean>;
  for (const id of off) s[id] = false;
  return s as SignalSwitches;
};

describe('the switch list', () => {
  it('one switch per signal, in menu order, each with a label and a tooltip', () => {
    expect([...SIGNAL_SWITCH_IDS]).toEqual(ALL_IDS);
    expect(SIGNAL_SWITCHES.map((s) => s.id)).toEqual(ALL_IDS);
    expect(SIGNAL_SWITCHES.map((s) => s.label)).toEqual([
      '9EMA Flag sideways',
      '9EMA Flag ready',
      '9EMA Flag breakout',
      '9EMA Slam',
      'HOD Flag',
      'Green Dildo',
      'Dildo 9EMA Pullback',
      'Dildo 9EMA Touch',
      'Volume Climax',
      'Bottom Confirmed',
      'Bouncy Ball Short',
      'Bouncy forming',
      'Ignition',
      'Power Candle',
      'Power Setup',
      'Vol Break',
      'Vol Break 9EMA',
      'PM New High',
      'Daily Breakout',
      'PDL Break',
      'VWAP Bull Cross',
      'VWAP Bear Cross',
      'EP Watch',
      'EP Break',
    ]);
    for (const s of SIGNAL_SWITCHES) expect(s.title.length, s.id).toBeGreaterThan(10);
  });

  it('isSignalSwitchId: the nine seeded scans only (never an Object.prototype key)', () => {
    for (const id of ALL_IDS) expect(isSignalSwitchId(id), id).toBe(true);
    for (const id of ['gappers', '', 'constructor', 'toString', '__proto__']) expect(isSignalSwitchId(id), id).toBe(false);
  });

  it('defaults: every signal ON except the opt-in Dildo 9EMA Touch, Bouncy forming and Daily Breakout', () => {
    const d = defaultSignalSwitches();
    for (const id of ALL_IDS) expect(d[id as keyof SignalSwitches], id).toBe(id !== 'dildo-touch' && id !== 'bouncy-forming' && id !== 'daily-breakout');
  });
});

describe('persistence (localStorage, a new key) and migration of the old Dildo / Climax toggles', () => {
  it('has its own, new key next to the old two', () => {
    expect(SIGNAL_SWITCHES_KEY).toMatch(/^mktscanner\.signal\./);
    expect(SIGNAL_SWITCHES_KEY).not.toBe(DILDO_ALERTS_KEY);
    expect(SIGNAL_SWITCHES_KEY).not.toBe(CLIMAX_ALERTS_KEY);
  });

  it('round-trips every combination it writes', () => {
    const s = withOff('e9flag-ready', 'dildo-1m', 'climax-confirm');
    expect(parseSignalSwitches(serializeSignalSwitches(s), null, null)).toEqual(s);
    const t = { ...defaultSignalSwitches(), 'dildo-touch': true };
    expect(parseSignalSwitches(serializeSignalSwitches(t), '0', '0')).toEqual(t); // the saved blob wins over the old toggles
  });

  it('nothing saved yet: the defaults, with the old toggles carried over', () => {
    expect(parseSignalSwitches(null, null, null)).toEqual(defaultSignalSwitches());
    expect(parseSignalSwitches(null, '1', '1')).toEqual(defaultSignalSwitches());
    expect(parseSignalSwitches(null, '0', null)).toEqual(withOff('dildo-1m', 'dildo-pb'));
    expect(parseSignalSwitches(null, null, '0')).toEqual(withOff('climax-1m', 'climax-confirm'));
    expect(parseSignalSwitches(undefined, '0', '0')).toEqual(withOff('dildo-1m', 'dildo-pb', 'climax-1m', 'climax-confirm'));
  });

  it('a garbled or foreign blob degrades to the migrated defaults, never a crash', () => {
    for (const raw of ['', 'garbage', '[]', '"x"', '1', 'null', '[true]']) {
      expect(parseSignalSwitches(raw, '0', null), raw).toEqual(withOff('dildo-1m', 'dildo-pb'));
    }
  });

  it('a partial blob: what it has (booleans only), the defaults for the rest; unknown keys ignored', () => {
    const got = parseSignalSwitches(JSON.stringify({ 'e9flag-sideways': false, 'dildo-touch': true, 'climax-1m': 'no', gappers: false }), '0', '0');
    expect(got).toEqual({ ...withOff('e9flag-sideways'), 'dildo-touch': true });
    expect(Object.keys(got).sort()).toEqual([...ALL_IDS].sort());
  });
});

describe("signalAlertScanIds (the window's scan view = what it subscribes to)", () => {
  const SCANS = ['gappers', ...ALL_IDS];

  it("defaults: the list's scan first, then every switched-on signal that exists; Touch (off) is not subscribed", () => {
    expect(signalAlertScanIds('e9flag-sideways', SCANS, defaultSignalSwitches())).toEqual([
      'e9flag-sideways',
      'e9flag-ready',
      'e9flag-breakout',
      'e9flag-slam',
      'hod-flag',
      'dildo-1m',
      'dildo-pb',
      'climax-1m',
      'climax-confirm',
      'bouncy-short',
      'ignition',
      'power-candle',
      'power-setup',
      'vol-break',
      'vol-break-9ema',
      'pm-new-high',
      'pdl-break',
      'vwap-bull',
      'vwap-bear',
      'ep-watch',
      'ep-break',
    ]);
  });

  it('a switched-off signal is not subscribed; switching Touch on subscribes it', () => {
    const v = signalAlertScanIds('gappers', SCANS, withOff('e9flag-ready', 'e9flag-breakout', 'dildo-pb', 'climax-1m'));
    expect(v).toEqual(['gappers', 'e9flag-sideways', 'e9flag-slam', 'hod-flag', 'dildo-1m', 'climax-confirm', 'bouncy-short', 'ignition', 'power-candle', 'power-setup', 'vol-break', 'vol-break-9ema', 'pm-new-high', 'pdl-break', 'vwap-bull', 'vwap-bear', 'ep-watch', 'ep-break']);
    const all = signalAlertScanIds('gappers', SCANS, { ...defaultSignalSwitches(), 'dildo-touch': true, 'bouncy-forming': true, 'daily-breakout': true });
    expect(all).toEqual(['gappers', ...ALL_IDS]);
  });

  it("everything off: only the list's own scan (its rows need it — its alerts are gated, see below)", () => {
    const off = withOff(...ALL_IDS);
    expect(signalAlertScanIds('e9flag-sideways', SCANS, off)).toEqual(['e9flag-sideways']);
    expect(signalAlertScanIds('gappers', SCANS, off)).toEqual(['gappers']);
  });

  it("only scans that exist on the scanner, never twice, the list's scan always", () => {
    expect(signalAlertScanIds('dildo-pb', ['dildo-pb', 'dildo-1m', 'e9flag-slam'], defaultSignalSwitches())).toEqual([
      'dildo-pb',
      'e9flag-slam',
      'dildo-1m',
    ]);
    expect(signalAlertScanIds('gone', [], defaultSignalSwitches())).toEqual(['gone']);
  });

  it('signalScansOff lists exactly the switched-off signals (what ws.ts silences)', () => {
    expect(signalScansOff(defaultSignalSwitches())).toEqual(['dildo-touch', 'bouncy-forming', 'daily-breakout']);
    expect(signalScansOff(withOff('e9flag-sideways', 'climax-confirm'))).toEqual(['e9flag-sideways', 'dildo-touch', 'climax-confirm', 'bouncy-forming', 'daily-breakout']);
  });
});

describe('chart-cell sounds follow the switches (flag cells)', () => {
  const on = defaultSignalSwitches();

  it('each flag event belongs to its stage switch', () => {
    const cases: [string, string][] = [
      ['flat', 'e9flag-sideways'],
      ['emaNear', 'e9flag-ready'],
      ['entry', 'e9flag-breakout'],
      ['add', 'e9flag-breakout'],
      ['exit', 'e9flag-breakout'],
      ['slam', 'e9flag-slam'],
    ];
    for (const [kind, id] of cases) {
      expect(cellEventHeard(on, 'flag', kind), kind).toBe(true);
      expect(cellEventHeard(withOff(id), 'flag', kind), `${kind} with ${id} off`).toBe(false);
      // and only its own switch: every OTHER switch off leaves it heard
      expect(cellEventHeard(withOff(...ALL_IDS.filter((x) => x !== id)), 'flag', kind), `${kind} with only ${id} on`).toBe(true);
    }
  });

  it('FLAG FAILED is heard while any 9EMA flag stage is on, silent with all three off', () => {
    expect(cellEventHeard(withOff('e9flag-sideways', 'e9flag-ready'), 'flag', 'failed')).toBe(true);
    expect(cellEventHeard(withOff('e9flag-sideways', 'e9flag-ready', 'e9flag-breakout'), 'flag', 'failed')).toBe(false);
  });

  it('the dildo / climax rule events follow their own switches', () => {
    expect(cellEventHeard(withOff('dildo-1m'), 'flag', 'dildo')).toBe(false);
    expect(cellEventHeard(withOff('dildo-pb'), 'flag', 'dildoPb')).toBe(false);
    expect(cellEventHeard(withOff('climax-1m'), 'flag', 'climax')).toBe(false);
    expect(cellEventHeard(withOff('climax-confirm'), 'flag', 'bottom')).toBe(false);
    expect(cellEventHeard(on, 'flag', 'bottom')).toBe(true);
  });

  it('pullback cells are not one of the nine signals: never gated (their kinds overlap the flag engine’s)', () => {
    const off = withOff(...ALL_IDS);
    for (const kind of ['entry', 'entryLH', 'breakout', 'stopped', 'longTarget', 'flat']) expect(cellEventHeard(off, 'pullback', kind), kind).toBe(true);
    expect(cellEventHeard(off, 'flag', 'impulse')).toBe(true); // ungoverned (and silent anyway)
  });
});

// ---------------------------------------------------------------------------
// The alert path (ws.ts), driven through the socket like alertview.test.ts.

const SCAN_DEFS = [
  { id: 'e9flag-sideways', name: '9EMA Flag — sideways', enabled: true, alert: { enabled: true, speak: true, sound: 'flat' } },
  { id: 'dildo-1m', name: 'Green Dildo', enabled: true, alert: { enabled: true, speak: true, sound: 'dildo' } },
  { id: 'gappers', name: 'Gappers', enabled: true, alert: { enabled: true, speak: true, sound: 'ping' } },
];

async function signalWindow() {
  vi.resetModules();
  let sock: { onmessage: ((e: { data: string }) => void) | null } | null = null;
  class FakeSocket {
    static OPEN = 1;
    readyState = 1;
    onopen: unknown = null;
    onclose: unknown = null;
    onerror: unknown = null;
    onmessage: ((e: { data: string }) => void) | null = null;
    constructor() {
      sock = this; // eslint-disable-line @typescript-eslint/no-this-alias
    }
    send(): void {}
    close(): void {}
  }
  vi.stubGlobal('WebSocket', FakeSocket);
  vi.stubGlobal('location', { protocol: 'http:', host: 'localhost:0' });
  const ws = await import('./ws');
  const { useStore } = await import('./store');
  const { play } = await import('./audio');
  const { speak } = await import('./voice');
  ws.hearOnlyViewedScans();
  ws.start();
  useStore.setState({ scans: SCAN_DEFS as never, mutedScans: {}, signalSoundMuted: false, signalVoiceOff: false });
  ws.viewTiles(SCAN_DEFS.map((s) => s.id));
  const heard = (scanId: string, sym: string) => {
    vi.mocked(play).mockClear();
    vi.mocked(speak).mockClear();
    const sound = SCAN_DEFS.find((x) => x.id === scanId)!.alert.sound;
    sock!.onmessage!({ data: JSON.stringify({ t: 'alert', d: { scanId, sym, sound } }) });
    return { tone: vi.mocked(play).mock.calls.map((c) => c[0]), said: vi.mocked(speak).mock.calls.map((c) => c[0]) };
  };
  return { store: useStore, heard };
}

describe('alert gating in the Signal window (ws.ts)', () => {
  it('starts with no signal switched off (every other window: nothing changes)', async () => {
    const { store, heard } = await signalWindow();
    expect(store.getState().signalScansOff).toEqual([]);
    expect(heard('e9flag-sideways', 'BEAT').tone).toEqual(['flat']);
  });

  it('a switched-off signal neither plays nor speaks — even while it is still in the view (the list shows it)', async () => {
    const { store, heard } = await signalWindow();
    store.getState().setSignalScansOff(['e9flag-sideways']);
    expect(heard('e9flag-sideways', 'BEAT')).toEqual({ tone: [], said: [] });
    expect(heard('dildo-1m', 'ABCD')).toEqual({ tone: ['dildo'], said: ['ABCD: HOD dildo'] });
    expect(heard('gappers', 'GAPP').tone).toEqual(['ping']); // not a signal: untouched
    store.getState().setSignalScansOff([]);
    expect(heard('e9flag-sideways', 'BEAT')).toEqual({ tone: ['flat'], said: ['BEAT: nine E M A flag, sideways'] });
  });

  it('a scan id that is an Object.prototype key is not "off"', async () => {
    const { store } = await signalWindow();
    store.getState().setSignalScansOff(['dildo-1m']);
    expect(store.getState().signalScansOff.includes('constructor')).toBe(false);
  });
});

describe('episodic pivot switches', () => {
  it('both are on by default, and spoken as the scans are named', () => {
    const d = defaultSignalSwitches();
    expect(d['ep-watch']).toBe(true);
    expect(d['ep-break']).toBe(true);
  });
});
