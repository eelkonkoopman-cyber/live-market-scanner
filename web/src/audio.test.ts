// Pins audio.ts's pending-until-unlock queue (review window:F6). The queue
// exists to cover ONE race — a tone requested in the instant before the
// click-to-unlock lands — so it must not become an archive: a window left
// locked for hours used to replay every queued trade tone at once, at the
// same AudioContext time, when the button was finally clicked (a burst of
// overlapping noise that also bypassed SignalPane's 10-minute recency rule).
//
// Node env, so a tiny fake AudioContext stands in for Web Audio and the
// module is re-imported per test (its lock/queue state is module-level).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

interface FakeOsc {
  frequency: { value: number; ramps: { to: number; at: number }[] };
  startedAt: number | null;
  stoppedAt: number | null;
  out: unknown[]; // the nodes it is connected to
}

let oscillators: FakeOsc[] = [];
let lastCtx: FakeAudioContext | null = null;

class FakeAudioContext {
  currentTime = 5000;
  destination = {};
  constructor() {
    lastCtx = this; // eslint-disable-line @typescript-eslint/no-this-alias
  }
  resume(): Promise<void> {
    return Promise.resolve();
  }
  createGain() {
    const node = {
      gain: { setValueAtTime() {}, exponentialRampToValueAtTime() {} },
      out: [] as unknown[],
      connect(n: unknown) {
        node.out.push(n);
      },
      disconnect() {
        node.out.length = 0;
      },
    };
    return node;
  }
  createOscillator() {
    const osc: FakeOsc & { type: string; connect(n: unknown): void; start(t: number): void; stop(t: number): void } = {
      type: '',
      frequency: {
        value: 0,
        ramps: [],
        setValueAtTime(v: number) {
          osc.frequency.value = v;
        },
        exponentialRampToValueAtTime(to: number, at: number) {
          osc.frequency.ramps.push({ to, at });
        },
      } as FakeOsc['frequency'],
      startedAt: null,
      stoppedAt: null,
      out: [],
      connect(n: unknown) {
        osc.out.push(n);
      },
      start(t: number) {
        osc.startedAt = t;
      },
      stop(t: number) {
        osc.stoppedAt = t;
      },
    };
    oscillators.push(osc);
    return osc;
  }
}

async function freshAudio() {
  vi.resetModules();
  return await import('./audio');
}

beforeEach(() => {
  oscillators = [];
  lastCtx = null;
  vi.useFakeTimers();
  vi.setSystemTime(new Date('2026-09-24T10:00:00Z'));
  (globalThis as unknown as { window: unknown }).window = { AudioContext: FakeAudioContext };
});

afterEach(() => {
  vi.useRealTimers();
  delete (globalThis as unknown as { window?: unknown }).window;
});

describe('audio pending-until-unlock queue', () => {
  it('plays a tone requested just before the unlock click (the race the queue exists for)', async () => {
    const audio = await freshAudio();
    audio.play('short'); // locked: queued, nothing sounds
    expect(oscillators).toHaveLength(0);
    vi.advanceTimersByTime(2_000);
    audio.unlock();
    expect(oscillators).toHaveLength(2); // the two notes of 'short'
  });

  it('drops tones that have waited longer than the gesture race — hours-old alerts are not replayed on the click', async () => {
    const audio = await freshAudio();
    audio.play('short');
    audio.play('add');
    vi.advanceTimersByTime(5 * 60 * 60_000); // the window sat locked for five hours
    audio.play('long'); // a fresh one, right before the click
    vi.advanceTimersByTime(1_000);
    audio.unlock();
    expect(oscillators.map((o) => o.frequency.value)).toEqual([587, 880]); // only 'long' (587 then 880 Hz)
  });

  it('a burst of the same tone waiting at the click plays once, not stacked at one start time', async () => {
    const audio = await freshAudio();
    for (let i = 0; i < 12; i++) audio.play('short');
    vi.advanceTimersByTime(1_000);
    audio.unlock();
    expect(oscillators).toHaveLength(2); // one 'short' phrase, not twelve
  });

  it('nothing queued hours ago sounds, however many were queued (and the queue is pruned as new ones arrive)', async () => {
    const audio = await freshAudio();
    for (let i = 0; i < 50; i++) {
      audio.play('add');
      vi.advanceTimersByTime(60 * 60_000); // an hour apart
    }
    audio.unlock();
    expect(oscillators).toHaveLength(0); // the newest is an hour old: nothing is fresh enough
  });

  it("'flat' (the 9 EMA flag heads-up) is two identical 740 Hz notes — no contour, so it can't be mistaken for the descending 'short' / ascending 'long' trade phrases — and is a selectable tone name", async () => {
    const audio = await freshAudio();
    audio.unlock();
    audio.play('flat');
    expect(oscillators.map((o) => o.frequency.value)).toEqual([740, 740]);
    // the second note starts only after the first one's 120 ms has elapsed
    expect(oscillators[1]!.startedAt! - oscillators[0]!.startedAt!).toBeCloseTo(0.12, 5);
    expect(audio.SOUNDS).toContain('flat');
  });

  it("'slam' (the 9 EMA SLAM alert) is ONE note that falls: a fast glide down two octaves, so it can't be mistaken for any stepped tone", async () => {
    const audio = await freshAudio();
    audio.unlock();
    audio.play('slam');
    expect(oscillators).toHaveLength(1);
    const o = oscillators[0]!;
    expect(o.frequency.ramps).toHaveLength(1);
    const from = o.frequency.value;
    const { to, at } = o.frequency.ramps[0]!;
    expect(from).toBe(1175);
    expect(to).toBe(294);
    expect(to).toBeLessThan(from / 2); // falls more than an octave
    // short: the glide and the note both end within ~a third of a second
    expect(at - o.startedAt!).toBeCloseTo(0.34, 5);
    expect(o.stoppedAt! - o.startedAt!).toBeLessThan(0.4);
    expect(audio.SOUNDS).toContain('slam');
  });

  it("no other tone glides: 'flat', 'ping', 'long' and 'short' are steady notes — the slam's fall is its own", async () => {
    const audio = await freshAudio();
    audio.unlock();
    for (const name of ['flat', 'ping', 'long', 'short', 'add', 'bell']) audio.play(name);
    expect(oscillators.length).toBeGreaterThan(0);
    for (const o of oscillators) expect(o.frequency.ramps).toEqual([]);
  });

  // RV2-INT-2: a long slammed out of its position prints EXIT and SLAM (9 EMA) on one bar, so one chart result
  // asks for the 'bell' and the 'slam' glide in the same tick. Started together, the 1046 Hz bell beat against the
  // start of the 1175 Hz fall; playInTurn plays them one after the other, in the order given.
  it('playInTurn: tones requested together play one after another (the bell, then the slam fall), never on top', async () => {
    const audio = await freshAudio();
    audio.unlock();
    audio.playInTurn(['bell', 'slam']);
    expect(oscillators.map((o) => o.frequency.value)).toEqual([1046, 1175]);
    const [bell, slam] = oscillators;
    expect(bell!.startedAt).toBe(5000); // the first one right away
    expect(slam!.startedAt!).toBeGreaterThan(bell!.stoppedAt!); // the fall starts after the bell has stopped
    expect(slam!.startedAt! - bell!.startedAt!).toBeLessThan(0.5); // ... promptly: one alert, not two
    expect(slam!.frequency.ramps[0]!.at - slam!.startedAt!).toBeCloseTo(0.34, 5); // the glide moved with its note
  });

  it('playInTurn: a sequence waits for every note of the one before; a repeated name plays once; one name is play()', async () => {
    const audio = await freshAudio();
    audio.unlock();
    audio.playInTurn(['short', 'bell', 'short']);
    expect(oscillators.map((o) => o.frequency.value)).toEqual([880, 587, 1046]);
    expect(oscillators[2]!.startedAt!).toBeGreaterThan(oscillators[1]!.stoppedAt!);
    oscillators = [];
    audio.playInTurn(['slam']);
    expect(oscillators.map((o) => o.startedAt)).toEqual([5000]);
    oscillators = [];
    audio.playInTurn([]);
    expect(oscillators).toHaveLength(0);
  });

  it('playInTurn while locked queues like play(): the click plays what is fresh, still one after another', async () => {
    const audio = await freshAudio();
    audio.playInTurn(['bell', 'slam']);
    expect(oscillators).toHaveLength(0);
    vi.advanceTimersByTime(1_000);
    audio.unlock();
    expect(oscillators.map((o) => o.frequency.value)).toEqual([1046, 1175]);
    expect(oscillators[1]!.startedAt!).toBeGreaterThan(oscillators[0]!.stoppedAt!);
  });

  // GREEN DILDO and VOLUME CLIMAX -> BOTTOM CONFIRMED (2026-10-07): three new scan
  // tones. Each must be told apart from every other tone by ear alone, because
  // the Signal window sits on a spare monitor.
  it("'dildo' (the Green Dildo scan) is three quick rising steady notes: 523 -> 659 -> 784 Hz, 70 ms each", async () => {
    const audio = await freshAudio();
    audio.unlock();
    audio.play('dildo');
    expect(oscillators.map((o) => o.frequency.value)).toEqual([523, 659, 784]);
    for (const o of oscillators) expect(o.frequency.ramps).toEqual([]); // steady notes, not a glide
    expect(oscillators[1]!.startedAt! - oscillators[0]!.startedAt!).toBeCloseTo(0.07, 5);
    expect(oscillators[2]!.startedAt! - oscillators[1]!.startedAt!).toBeCloseTo(0.07, 5);
    expect(oscillators[2]!.stoppedAt! - oscillators[0]!.startedAt!).toBeLessThan(0.25); // a quick phrase
    expect(audio.SOUNDS).toContain('dildo');
  });

  it("'climax' (the Volume Climax scan) is ONE low held note: 196 Hz for 0.45 s — the only tone under 290 Hz and the only long single note", async () => {
    const audio = await freshAudio();
    audio.unlock();
    audio.play('climax');
    expect(oscillators).toHaveLength(1);
    const o = oscillators[0]!;
    expect(o.frequency.value).toBe(196);
    expect(o.frequency.ramps).toEqual([]);
    expect(o.stoppedAt! - o.startedAt!).toBeCloseTo(0.46, 5);
    expect(audio.SOUNDS).toContain('climax');
  });

  it("'bottom' (the Bottom Confirmed scan) leaps an octave up from the climax pitch: 196 then 392 Hz", async () => {
    const audio = await freshAudio();
    audio.unlock();
    audio.play('bottom');
    expect(oscillators.map((o) => o.frequency.value)).toEqual([196, 392]);
    for (const o of oscillators) expect(o.frequency.ramps).toEqual([]);
    expect(oscillators[1]!.startedAt! - oscillators[0]!.startedAt!).toBeCloseTo(0.16, 5);
    expect(audio.SOUNDS).toContain('bottom');
  });

  it('every tone in SOUNDS has its own fingerprint (pitches, note count, lengths, glide) — no two sound alike', async () => {
    const audio = await freshAudio();
    audio.unlock();
    const prints = new Map<string, string>();
    for (const name of audio.SOUNDS) {
      oscillators = [];
      audio.play(name);
      expect(oscillators.length, name).toBeGreaterThan(0);
      const fp = oscillators
        .map((o) => `${o.frequency.value}${o.frequency.ramps.map((r) => `>${r.to}`).join('')}/${(o.stoppedAt! - o.startedAt!).toFixed(3)}`)
        .join(' ');
      prints.set(name, fp);
    }
    const seen = new Map<string, string>();
    for (const [name, fp] of prints) {
      expect(seen.get(fp), `${name} sounds exactly like ${seen.get(fp)}`).toBeUndefined();
      seen.set(fp, name);
    }
    // the three new names are all there, and nothing else lost its name
    for (const n of ['ping', 'chime', 'alert', 'bell', 'short', 'add', 'long', 'flat', 'slam', 'dildo', 'climax', 'bottom']) expect(audio.SOUNDS, n).toContain(n);
  });

  it("the new tones are distinct where it matters: 'dildo' is the only three-note phrase, 'climax' the only tone under 290 Hz besides its own 'bottom' leap, and neither of those glides like 'slam'", async () => {
    const audio = await freshAudio();
    audio.unlock();
    const notes = (name: string) => {
      oscillators = [];
      audio.play(name);
      return oscillators.slice();
    };
    for (const name of audio.SOUNDS) {
      const n = notes(name);
      if (name !== 'dildo') expect(n.length, name).not.toBe(3);
      const low = n.some((o) => o.frequency.value < 290);
      if (name !== 'climax' && name !== 'bottom') expect(low, name).toBe(false);
    }
    // 'bottom' is not 'long' moved down: a different interval (an octave, 2:1) from 'long' (a fifth, 880/587)
    const b = notes('bottom').map((o) => o.frequency.value);
    const l = notes('long').map((o) => o.frequency.value);
    expect(b[1]! / b[0]!).toBe(2);
    expect(l[1]! / l[0]!).not.toBeCloseTo(2, 1);
  });

  it('after the unlock, play() sounds immediately and queues nothing', async () => {
    const audio = await freshAudio();
    audio.unlock();
    audio.play('add');
    expect(oscillators).toHaveLength(1);
    expect(audio.isUnlocked()).toBe(true);
  });
});

// web-ux-1: alerts that fire together reach play() one frame at a time (ws.ts
// plays each alert frame as it lands), and since the Signal window hears the
// listed scan AND the dildo / climax scans, one minute's close can ask for the
// dildo run, the climax's held G3 and the long phrase in the same instant.
// Started together they were one chord - none of the contours designed to be
// told apart by ear - and identical tones added up past full scale (4 dildo
// phrases at 0.3 = 1.2). play() now starts a tone once the one before has
// stopped (IN_TURN_GAP_S later), the way playInTurn does.
describe('play(): tones requested together sound one after another', () => {
  // The notes of each phrase: [first start, last stop].
  const spans = () => {
    const out: { from: number; to: number }[] = [];
    for (const o of oscillators) out.push({ from: o.startedAt!, to: o.stoppedAt! });
    return out;
  };

  it('a dildo, a climax and a long requested at one moment play in turn, in order, never on top', async () => {
    const audio = await freshAudio();
    audio.unlock();
    audio.play('dildo');
    audio.play('climax');
    audio.play('long');
    expect(oscillators.map((o) => o.frequency.value)).toEqual([523, 659, 784, 196, 587, 880]);
    const s = spans();
    expect(s[0]!.from).toBe(5000); // the first right away
    expect(s[3]!.from).toBeGreaterThan(s[2]!.to); // the climax after the dildo's last note
    expect(s[4]!.from).toBeGreaterThan(s[3]!.to); // the long after the climax
    expect(s[4]!.from - s[0]!.from).toBeLessThan(1); // promptly: one burst, not a queue
  });

  it('a burst of the same tone plays as separate phrases, never stacked (no clipping)', async () => {
    const audio = await freshAudio();
    audio.unlock();
    for (let i = 0; i < 4; i++) audio.play('dildo');
    expect(oscillators).toHaveLength(12);
    const s = spans();
    for (let k = 3; k < s.length; k += 3) expect(s[k]!.from, `phrase ${k / 3}`).toBeGreaterThan(s[k - 1]!.to);
  });

  it('a tone after a playInTurn phrase waits for it too', async () => {
    const audio = await freshAudio();
    audio.unlock();
    audio.playInTurn(['bell', 'slam']);
    audio.play('climax');
    const s = spans();
    expect(s[2]!.from).toBeGreaterThan(s[1]!.to);
  });

  it('once the previous tone has finished, a new one starts at once', async () => {
    let ctxRef: { currentTime: number } | null = null;
    (globalThis as unknown as { window: unknown }).window = {
      AudioContext: class extends FakeAudioContext {
        constructor() {
          super();
          ctxRef = this; // eslint-disable-line @typescript-eslint/no-this-alias
        }
      },
    };
    const audio = await freshAudio();
    audio.unlock();
    audio.play('climax');
    ctxRef!.currentTime = 5002; // two seconds on: the 0.46 s climax is long over
    audio.play('bottom');
    expect(oscillators[1]!.startedAt).toBe(5002);
  });

  it('the queue never runs far ahead: past the backlog a tone starts at once (a stalled clock does not build a train)', async () => {
    const audio = await freshAudio();
    audio.unlock();
    for (let i = 0; i < 60; i++) audio.play('climax'); // the fake clock never moves: 60 x 0.5 s would be 30 s
    for (const o of oscillators) expect(o.startedAt!).toBeLessThanOrEqual(5000 + audio.MAX_TONE_BACKLOG_S);
  });
});

// rv2-web-1: the Signal window's 🔇 has to stop what an alert burst already
// queued. play() schedules each tone after the one before (up to
// MAX_TONE_BACKLOG_S ahead), so a burst at one minute's close can keep sounding
// for seconds after the trader pressed mute. silence() cuts every tone already
// scheduled at once and forgets the backlog; the next tone sounds normally.
describe('silence(): cuts every tone already scheduled', () => {
  // A node is audible when some path of connections reaches the speakers.
  const audible = (n: unknown): boolean => {
    if (n === lastCtx!.destination) return true;
    const out = (n as { out?: unknown[] }).out;
    return !!out && out.some(audible);
  };

  it('a queued burst goes quiet at once, and the next tone starts right away, audible', async () => {
    const audio = await freshAudio();
    audio.unlock();
    for (let i = 0; i < 4; i++) audio.play('dildo');
    audio.play('climax');
    audio.play('long');
    expect(oscillators.length).toBe(15);
    expect(oscillators.every(audible)).toBe(true);
    expect(Math.max(...oscillators.map((o) => o.startedAt!))).toBeGreaterThan(5001); // the tail runs on past a second
    audio.silence();
    expect(oscillators.filter(audible)).toEqual([]); // every note already scheduled is cut
    const before = oscillators.length;
    audio.play('bottom');
    const fresh = oscillators.slice(before);
    expect(fresh.map((o) => o.frequency.value)).toEqual([196, 392]);
    expect(fresh[0]!.startedAt).toBe(5000); // not behind the silenced backlog
    expect(fresh.every(audible)).toBe(true);
  });

  it('tones waiting for the unlock click are dropped too', async () => {
    const audio = await freshAudio();
    audio.play('dildo');
    audio.play('climax');
    audio.silence();
    audio.unlock();
    expect(oscillators).toHaveLength(0);
  });

  it('is safe before any sound and twice in a row', async () => {
    const audio = await freshAudio();
    audio.silence();
    audio.unlock();
    audio.silence();
    audio.silence();
    audio.play('ping');
    expect(oscillators).toHaveLength(1);
    expect(audible(oscillators[0])).toBe(true);
  });
});
