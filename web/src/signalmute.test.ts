// rv2-web-1, end to end: the Signal window's 🔇 and '🗣 Voice' off must stop an
// alert burst that is ALREADY queued, not only the alert frames that arrive
// after the click. play() schedules each tone after the one before (up to
// MAX_TONE_BACKLOG_S ahead) and up to MAX_ALERT_LINES spoken alert lines queue,
// so at one minute's close (several green dildos, a flag alert, a pullback, a
// climax) the window kept playing and talking for 20-30 s after the trader had
// silenced it.
//
// The real ws.ts, audio.ts, voice.ts and store.ts, driven the way the app is
// (alert frames through the socket's onmessage; the toolbar's state as
// SignalPane mirrors it into the store), with a fake AudioContext that records
// every connection and a fake speechSynthesis that never finishes a line.
import { afterEach, describe, expect, it, vi } from 'vitest';

interface FakeNode {
  out: unknown[];
}

const SCANS = [
  { id: 'e9flag-sideways', name: '9EMA Flag - sideways', enabled: true, alert: { enabled: true, speak: true, sound: 'flat' } },
  { id: 'dildo-1m', name: 'Green Dildo', enabled: true, alert: { enabled: true, speak: true, sound: 'dildo' } },
  { id: 'dildo-pb', name: 'Dildo 9EMA Pullback', enabled: true, alert: { enabled: true, speak: true, sound: 'long' } },
  { id: 'climax-1m', name: 'Volume Climax', enabled: true, alert: { enabled: true, speak: true, sound: 'climax' } },
];

const BURST: [string, string, string][] = [
  ['dildo-1m', 'AAAA', 'dildo'],
  ['dildo-1m', 'BBBB', 'dildo'],
  ['dildo-1m', 'CCCC', 'dildo'],
  ['e9flag-sideways', 'DDDD', 'flat'],
  ['dildo-pb', 'EEEE', 'long'],
  ['dildo-1m', 'FFFF', 'dildo'],
  ['climax-1m', 'GGGG', 'climax'],
  ['dildo-1m', 'HHHH', 'dildo'],
  ['e9flag-sideways', 'IIII', 'flat'],
  ['dildo-1m', 'JJJJ', 'dildo'],
];

// signalWindow loads fresh modules over the fakes and plays the burst into it.
async function signalWindow() {
  vi.resetModules();
  const notes: (FakeNode & { t: number })[] = [];
  const param = { value: 0, setValueAtTime() {}, exponentialRampToValueAtTime() {} };
  let destination: unknown = null;
  class FakeCtx {
    currentTime = 100;
    destination = {};
    constructor() {
      destination = this.destination;
    }
    resume() {
      return Promise.resolve();
    }
    createOscillator() {
      const o = {
        t: -1,
        out: [] as unknown[],
        frequency: { ...param },
        connect: (n: unknown) => o.out.push(n),
        start: (t: number) => {
          o.t = t;
          notes.push(o);
        },
        stop() {},
      };
      return o;
    }
    createGain() {
      const g = {
        out: [] as unknown[],
        gain: { ...param },
        connect: (n: unknown) => g.out.push(n),
        disconnect: () => {
          g.out.length = 0;
        },
      };
      return g;
    }
  }
  const queue: unknown[] = [];
  let cancels = 0;
  const synth = {
    get speaking() {
      return queue.length > 0;
    },
    get pending() {
      return queue.length > 1;
    },
    getVoices: () => [],
    speak: (u: unknown) => queue.push(u),
    cancel: () => {
      cancels++;
      queue.length = 0;
    },
    onvoiceschanged: null,
  };
  vi.stubGlobal('window', { AudioContext: FakeCtx, speechSynthesis: synth });
  vi.stubGlobal('speechSynthesis', synth);
  vi.stubGlobal(
    'SpeechSynthesisUtterance',
    class {
      text: string;
      constructor(t: string) {
        this.text = t;
      }
    },
  );
  vi.stubGlobal('localStorage', { getItem: () => null, setItem() {}, removeItem() {} });
  let sock: { onmessage: (e: { data: string }) => void } | null = null;
  class FakeSocket {
    static OPEN = 1;
    readyState = 1;
    onopen: unknown = null;
    onclose: unknown = null;
    onerror: unknown = null;
    onmessage: ((e: { data: string }) => void) | null = null;
    constructor() {
      sock = this as never;
    }
    send(): void {}
    close(): void {}
  }
  vi.stubGlobal('WebSocket', FakeSocket);
  vi.stubGlobal('location', { protocol: 'http:', host: 'localhost:0' });
  const audio = await import('./audio');
  const ws = await import('./ws');
  const { useStore } = await import('./store');
  audio.unlock();
  ws.hearOnlyViewedScans();
  ws.start();
  useStore.setState({ scans: SCANS as never, mutedScans: {}, signalSoundMuted: false, signalVoiceOff: false });
  ws.viewTiles(SCANS.map((s) => s.id));
  for (const [scanId, sym, sound] of BURST) sock!.onmessage({ data: JSON.stringify({ t: 'alert', d: { scanId, sym, sound } }) });
  const audible = (n: unknown): boolean => n === destination || !!(n as FakeNode).out?.some(audible);
  return {
    store: useStore,
    queued: () => queue.length,
    cancels: () => cancels,
    sounding: () => notes.filter(audible).length,
    lastStart: () => Math.max(...notes.map((n) => n.t)) - 100,
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('the Signal window toolbar stops an alert burst already queued', () => {
  it('the burst queues ten spoken lines and runs its tones on for seconds (what there is to stop)', async () => {
    const w = await signalWindow();
    expect(w.queued()).toBe(10);
    expect(w.sounding()).toBeGreaterThan(10);
    expect(w.lastStart()).toBeGreaterThan(2);
  });

  it('🔇: nothing more is heard - no spoken line left queued, no tone still connected to the speakers', async () => {
    const w = await signalWindow();
    w.store.getState().setSignalSoundMuted(true);
    expect(w.queued()).toBe(0);
    expect(w.cancels()).toBeGreaterThan(0);
    expect(w.sounding()).toBe(0);
  });

  it("'🗣 Voice' off: the queued lines are cancelled, the tones play on", async () => {
    const w = await signalWindow();
    const tones = w.sounding();
    w.store.getState().setSignalVoiceOff(true);
    expect(w.queued()).toBe(0);
    expect(w.sounding()).toBe(tones);
  });
});
