// Today's blacklist silences alerts: a blacklisted symbol makes no tone and no
// spoken line in ANY scan (dildo, flag, climax...), every other symbol still
// sounds, the list updates live with each 'blacklist' frame, and a list from an
// earlier ET day (it expires at 00:00 ET) silences nothing.
// Same real ws/audio/voice/store harness as signalmute.test.ts.
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

// signalWindow loads fresh modules over the fakes and plays the burst into it.
async function scannerWindow() {
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
  const send = (f: unknown) => sock!.onmessage({ data: JSON.stringify(f) });
  const alert = (scanId: string, sym: string) => send({ t: 'alert', d: { scanId, sym, sound: 'ping' } });
  const status = (blacklist: string[]) => send({ t: 'blacklist', d: { syms: blacklist } });
  const spoken = () => queue.map((u) => (u as { text: string }).text);
  return { store: useStore, alert, status, spoken, tones: () => notes.length };
}

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('blacklisted symbols are never heard', () => {
  it('silences a blacklisted symbol in every scan, others still speak', async () => {
    const w = await scannerWindow();
    w.status(['AAAA']);
    for (const s of SCANS) w.alert(s.id, 'AAAA');
    expect(w.tones()).toBe(0);
    expect(w.spoken()).toEqual([]);
    w.alert('dildo-1m', 'BBBB');
    expect(w.tones()).toBeGreaterThan(0);
    expect(w.spoken().some((t) => t.includes('BBBB'))).toBe(true);
    expect(w.spoken().some((t) => t.includes('AAAA'))).toBe(false);
  });

  it('follows the list live: added -> silent, removed -> heard again', async () => {
    const w = await scannerWindow();
    w.status([]);
    w.alert('climax-1m', 'CCCC');
    expect(w.spoken().filter((t) => t.includes('CCCC')).length).toBe(1);
    w.status(['CCCC']);
    w.alert('climax-1m', 'CCCC');
    expect(w.spoken().filter((t) => t.includes('CCCC')).length).toBe(1);
    w.status([]);
    w.alert('climax-1m', 'CCCC');
    expect(w.spoken().filter((t) => t.includes('CCCC')).length).toBe(2);
  });

  it("expires at 00:00 ET: yesterday's list silences nothing", async () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date('2026-10-14T23:00:00-04:00'));
    const w = await scannerWindow();
    w.status(['DDDD']);
    w.alert('dildo-1m', 'DDDD');
    expect(w.spoken().length).toBe(0);
    vi.setSystemTime(new Date('2026-10-15T00:01:00-04:00'));
    w.alert('dildo-1m', 'DDDD');
    expect(w.spoken().some((t) => t.includes('DDDD'))).toBe(true);
  });
});
