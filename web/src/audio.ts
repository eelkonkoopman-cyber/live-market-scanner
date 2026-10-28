// Synthesized alert tones via Web Audio — no binary assets to ship/embed.
// Browsers block audio until a user gesture, so unlock() must run from a click.

let ctx: AudioContext | null = null;
let unlocked = false;

// The pending-until-unlock queue exists to cover ONE race: a tone requested in
// the instant before the click-to-unlock gesture lands. It is not an archive.
// Entries carry the time they were queued and only ones younger than
// PENDING_MAX_AGE_MS are played on unlock — a window left locked for hours
// used to replay every queued trade tone at once, all starting at the same
// AudioContext time (a burst of overlapping noise), and bypass SignalPane's
// 10-minute recency rule for sound, which is applied when play() is CALLED,
// not when the sound finally plays.
const PENDING_MAX_AGE_MS = 10_000;
const pending: { name: string; at: number }[] = [];

const FREQS: Record<string, number> = {
  ping: 880,
  chime: 1320,
  alert: 660,
  bell: 1046,
};

// SEQUENCES are multi-note tones — the Signal window's trade alerts (§11:
// "a trade signal must be unmistakable and must name the stock, because the
// window sits on a spare monitor the trader is not looking at"). 'short' is
// two clearly descending notes so it can't be mistaken for any of the single
// flat tones above; 'add' is one note at the second (lower) pitch alone, so
// an add reads as "more of the same trade", not a fresh alert. 'long' (the
// pullback engine's long side, addendum 4) is 'short' mirrored — the same two
// pitches, ASCENDING (587 Hz then 880 Hz) — so the direction of the trade is
// audible from the phrase's contour alone, without looking at the window:
// down = short, up = long. Long adds reuse 'add' (one 587 Hz note): "more of
// the same trade" carries no direction, and the voice line names it anyway.
// 'flat' (the 9 EMA flag engine, iteration 6) is the heads-up that a stock has
// gone sideways above the rising EMA — "look at this one now, the entry is
// coming" — deliberately NOT an entry tone: two soft, IDENTICAL notes at one
// pitch (740 Hz, 120 ms each, gain 0.22: quieter and shorter than the trade
// phrases, and with no contour, so it can't be mistaken for 'short'/'long').
// 'slam' (the 9 EMA SLAM, 2026-10-07: a candle out of a flag that SLAMS through
// the EMA — a bearish alert, not a trade) is the only tone that MOVES: one note
// gliding down two octaves (1175 Hz -> 294 Hz in 340 ms), held at full level for
// most of the fall so the drop itself is heard. Every other tone is steady
// notes, so a fall can't be mistaken for 'short' (two stepped notes), 'flat',
// 'long' or the single pings.
// Three more scan tones (2026-10-07), one per market-wide 1-minute alert:
// 'dildo' (the "Green Dildo" scan) is the only THREE-note phrase: three quick
// steady notes climbing a major triad (523 -> 659 -> 784 Hz, 70 ms each) — a
// bright run up, shorter per note than any two-note phrase, so it reads as
// neither 'long' (two notes, a fifth) nor 'flat' (two equal notes). The dildo's
// pullback alert reuses 'long' (the rising entry phrase). 'climax' (the "Volume
// Climax" scan) is ONE low, heavy, held note (G3, 196 Hz, held 0.25 s then
// decaying to 0.45 s): the only tone under 290 Hz and the only long single
// note, so it can't be confused with the short single pings or the slam's
// fall. 'bottom' (the "Bottom Confirmed" scan) starts on that same climax pitch
// and leaps an OCTAVE up (196 -> 392 Hz): rising like 'long', but two octaves
// lower and a different interval, and it echoes the climax it confirms.
interface ToneStep {
  freq: number;
  dur: number; // seconds
  gain: number;
  to?: number; // glide: the frequency at the end of the note (exponential), absent = a steady pitch
  hold?: number; // seconds at full level before the decay starts (absent = the usual pluck: decay right away)
}
const SEQUENCES: Record<string, ToneStep[]> = {
  short: [
    { freq: 880, dur: 0.18, gain: 0.35 },
    { freq: 587, dur: 0.18, gain: 0.35 },
  ],
  add: [{ freq: 587, dur: 0.22, gain: 0.25 }],
  long: [
    { freq: 587, dur: 0.18, gain: 0.35 },
    { freq: 880, dur: 0.18, gain: 0.35 },
  ],
  flat: [
    { freq: 740, dur: 0.12, gain: 0.22 },
    { freq: 740, dur: 0.12, gain: 0.22 },
  ],
  slam: [{ freq: 1175, to: 294, dur: 0.34, gain: 0.35, hold: 0.22 }],
  dildo: [
    { freq: 523, dur: 0.07, gain: 0.3 },
    { freq: 659, dur: 0.07, gain: 0.3 },
    { freq: 784, dur: 0.07, gain: 0.3 },
  ],
  climax: [{ freq: 196, dur: 0.45, gain: 0.42, hold: 0.25 }],
  bottom: [
    { freq: 196, dur: 0.16, gain: 0.38 },
    { freq: 392, dur: 0.22, gain: 0.38 },
  ],
  // 'bounce' (the "Bouncy Ball Short" scan, 2026-10-14): a ball dropping — four
  // falling plucks, each shorter than the last.
  bounce: [
    { freq: 784, dur: 0.14, gain: 0.32 },
    { freq: 587, dur: 0.11, gain: 0.32 },
    { freq: 440, dur: 0.08, gain: 0.32 },
    { freq: 330, dur: 0.06, gain: 0.32 },
  ],
  // 'bounceSoft' (the "Bouncy Ball Forming" scan, 2026-10-14): a quiet heads-up,
  // two soft low plucks — distinct from the louder four-step 'bounce'.
  bounceSoft: [
    { freq: 440, dur: 0.1, gain: 0.16 },
    { freq: 392, dur: 0.14, gain: 0.16 },
  ],
  // 'ignite' (the "Ignition" scan, 2026-10-14): a short sharp spark — one quick
  // high blip snapping up, shorter and higher than the dildo's rising triad.
  ignite: [
    { freq: 1319, dur: 0.035, gain: 0.34 },
    { freq: 1760, dur: 0.05, gain: 0.34 },
  ],
  // 'psetup' (the "Power Setup" scan, 2026-10-27): a trade plan, not a prompt - a
  // rising triad (entry, stop, target) closed by a long high hold; longer and
  // brighter than the dildo's triad, unlike every other tone.
  psetup: [
    { freq: 659, dur: 0.09, gain: 0.34 },
    { freq: 831, dur: 0.09, gain: 0.34 },
    { freq: 1109, dur: 0.09, gain: 0.34 },
    { freq: 1661, dur: 0.3, gain: 0.34 },
  ],
  // 'volbreak' (the "Vol Break" scan, 2026-10-28): a level giving way - one
  // short low knock, then a jump up a sixth that holds (two notes, a contour
  // no other phrase has: 'long' is a fifth higher up, 'bottom' an octave lower).
  volbreak: [
    { freq: 330, dur: 0.07, gain: 0.34 },
    { freq: 554, dur: 0.26, gain: 0.34, hold: 0.1 },
  ],
};

export const SOUNDS = [...Object.keys(FREQS), ...Object.keys(SEQUENCES)];

export function isUnlocked(): boolean {
  return unlocked;
}

export function unlock(): void {
  if (!ctx) {
    const AC = window.AudioContext || (window as unknown as { webkitAudioContext: typeof AudioContext }).webkitAudioContext;
    ctx = new AC();
  }
  void ctx.resume();
  unlocked = true;
  const cutoff = Date.now() - PENDING_MAX_AGE_MS;
  const fresh = pending.filter((p) => p.at >= cutoff).map((p) => p.name);
  pending.length = 0;
  // A burst of the same name waiting at the click would start at one
  // AudioContext time and just stack into a louder single tone, so each name
  // plays once — and different names play in turn (playInTurn), not on top of
  // each other.
  playInTurn(fresh);
}

// busyUntil is the AudioContext time the last tone scheduled by play() or
// playInTurn() stops, plus IN_TURN_GAP_S: the earliest a new play() starts.
// Alerts that fire together reach play() one frame at a time (ws.ts plays each
// alert frame as it lands; one minute's close can bring a dildo, a climax and
// a flag-scan 'long' to the Signal window in the same instant). Started at
// ctx.currentTime they sounded as one chord - none of the phrases designed to
// be told apart by ear could be - and identical tones added up past full scale
// (four dildo phrases at gain 0.3 = 1.2, clipping). So play() starts a tone
// once the one before it has stopped, the way playInTurn sequences its names.
let busyUntil = 0;

// MAX_TONE_BACKLOG_S bounds how far ahead of the clock that queue may run. A
// tone that would start later than this starts at once instead (on top, as
// before): a burst cannot turn into a long train, and an AudioContext whose
// clock has stalled (suspended) does not pile up a queue that plays for ages
// when it resumes. Real bursts are a few distinct tones, well under it.
export const MAX_TONE_BACKLOG_S = 8;

// out is the node every tone plays through on its way to the speakers (one
// unity GainNode, made on first use). silence() disconnects it, which cuts
// every tone already scheduled through it at once, and the next tone gets a
// fresh one.
let out: GainNode | null = null;

function output(ac: AudioContext): AudioNode {
  if (!out) {
    out = ac.createGain();
    out.connect(ac.destination);
  }
  return out;
}

// silence stops every tone already scheduled and forgets the backlog, so the
// next play() starts at once; tones waiting for the unlock click are dropped
// too. The Signal window's 🔇 calls it (ws.ts, rv2-web-1): play() runs tones
// one after another up to MAX_TONE_BACKLOG_S ahead, so without it a burst kept
// sounding for seconds after the trader pressed mute.
export function silence(): void {
  pending.length = 0;
  busyUntil = 0;
  if (out) {
    out.disconnect();
    out = null;
  }
}

export function play(name: string): void {
  if (!unlocked || !ctx) {
    // The pending-until-unlock queue works the same for a sequence name. Stale
    // entries are dropped as new ones arrive, so a window that stays locked
    // all morning cannot grow the queue without bound.
    const now = Date.now();
    while (pending.length && pending[0]!.at < now - PENDING_MAX_AGE_MS) pending.shift();
    pending.push({ name, at: now });
    return;
  }
  let t = Math.max(ctx.currentTime, busyUntil);
  if (t - ctx.currentTime > MAX_TONE_BACKLOG_S) t = ctx.currentTime;
  busyUntil = Math.max(busyUntil, sound(ctx, name, t) + IN_TURN_GAP_S);
}

// The gap between two tones played in turn: long enough to hear two tones,
// short enough to stay one alert.
const IN_TURN_GAP_S = 0.04;

// playInTurn plays tones requested at ONE moment one after another, in the
// order given, each starting IN_TURN_GAP_S after the one before has stopped.
// play() starts every tone at ctx.currentTime, so two calls in one tick sound
// together: a long slammed out of its position prints EXIT and SLAM (9 EMA) on
// one bar, and the 1046 Hz bell beat against the start of the slam's 1175 Hz
// fall. A name given twice plays once (two copies at one moment were one
// louder tone before, never two). Locked, it queues each name like play().
// It starts at once (the Signal window's trade tones are not held behind an
// alert tone) and a play() after it waits for its last note (busyUntil).
export function playInTurn(names: readonly string[]): void {
  if (!unlocked || !ctx) {
    for (const n of names) play(n);
    return;
  }
  let t = ctx.currentTime;
  for (const n of new Set(names)) t = sound(ctx, n, t) + IN_TURN_GAP_S;
  busyUntil = Math.max(busyUntil, t);
}

// sound schedules one named tone to start at AudioContext time t and returns
// the time its last oscillator stops.
function sound(ac: AudioContext, name: string, t: number): number {
  const seq = SEQUENCES[name];
  if (seq) return playSequence(ac, seq, t);
  const freq = FREQS[name] ?? FREQS.ping;
  const osc = ac.createOscillator();
  const gain = ac.createGain();
  osc.type = 'sine';
  osc.frequency.value = freq;
  osc.connect(gain);
  gain.connect(output(ac));
  gain.gain.setValueAtTime(0.0001, t);
  gain.gain.exponentialRampToValueAtTime(0.3, t + 0.01);
  gain.gain.exponentialRampToValueAtTime(0.0001, t + 0.25);
  osc.start(t);
  osc.stop(t + 0.26);
  return t + 0.26;
}

// playSequence schedules each note of a multi-note tone back-to-back on the
// shared AudioContext (one oscillator+gain pair per note, each note's start
// time offset by the previous notes' durations) — the same envelope shape as
// the single-tone path above (a fast attack, exponential decay to silence),
// just repeated per step so 'short'/'add' read as one deliberate phrase
// rather than two unrelated pings. It starts at t and returns the time the
// last note stops.
function playSequence(ac: AudioContext, steps: ToneStep[], t: number): number {
  let end = t;
  for (const step of steps) {
    const osc = ac.createOscillator();
    const gain = ac.createGain();
    osc.type = 'sine';
    osc.frequency.value = step.freq;
    if (step.to != null) {
      osc.frequency.setValueAtTime(step.freq, t);
      osc.frequency.exponentialRampToValueAtTime(step.to, t + step.dur);
    }
    osc.connect(gain);
    gain.connect(output(ac));
    gain.gain.setValueAtTime(0.0001, t);
    gain.gain.exponentialRampToValueAtTime(step.gain, t + 0.01);
    if (step.hold != null && step.hold > 0.01 && step.hold < step.dur) gain.gain.setValueAtTime(step.gain, t + step.hold);
    gain.gain.exponentialRampToValueAtTime(0.0001, t + step.dur);
    osc.start(t);
    osc.stop(t + step.dur + 0.01);
    end = t + step.dur + 0.01;
    t += step.dur;
  }
  return end;
}
