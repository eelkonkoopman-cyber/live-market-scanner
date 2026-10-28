// Voice (Web Speech TTS) prefs + regime-alert narration — the port of the
// standalone Python app's static/app.js voice section (pickVoice/speak/
// speakRead/onRegimeAlert/blip). Browsers require no user gesture for speech
// synthesis (unlike Web Audio), so there is no unlock() here to mirror
// audio.ts's — prefs just load from localStorage at module init, in the same
// try/catch-guarded style store.ts uses for its persisted layout.
import { play } from './audio';
import type { TapeCard, TapeRegimeFrame } from './types';

const PREFS_KEY = 'mktscanner.voice.v1';

export interface VoicePrefs {
  on: boolean;
  rate: number;
  voiceName: string; // '' = auto-pick (first English voice, else voices[0])
  instant: boolean; // speak the instant local-verdict regime alert (tape.regime)
}

const DEFAULT_PREFS: VoicePrefs = { on: false, rate: 1.0, voiceName: '', instant: true };

function loadPrefs(): VoicePrefs {
  try {
    const raw = localStorage.getItem(PREFS_KEY);
    return raw ? { ...DEFAULT_PREFS, ...JSON.parse(raw) } : { ...DEFAULT_PREFS };
  } catch {
    return { ...DEFAULT_PREFS }; // private-mode / quota errors -> fall back to defaults
  }
}

function savePrefs(): void {
  try {
    localStorage.setItem(PREFS_KEY, JSON.stringify(prefs));
  } catch {
    /* best-effort */
  }
}

let prefs = loadPrefs();

export function getVoicePrefs(): VoicePrefs {
  return prefs;
}

export function setVoiceOn(on: boolean): void {
  prefs = { ...prefs, on };
  savePrefs();
}

export function setVoiceRate(rate: number): void {
  prefs = { ...prefs, rate };
  savePrefs();
}

export function setVoiceName(voiceName: string): void {
  prefs = { ...prefs, voiceName };
  savePrefs();
  pickVoice();
}

export function setVoiceInstant(instant: boolean): void {
  prefs = { ...prefs, instant };
  savePrefs();
}

/* --------------------------------------------------------------- voice (TTS) */

const synth: SpeechSynthesis | undefined = typeof window !== 'undefined' ? window.speechSynthesis : undefined;
const voiceSupported = !!synth;
let chosenVoice: SpeechSynthesisVoice | null = null;
let outstanding = 0; // utterances queued or being spoken, every caller
let alertLines = 0; // of which scan alert lines (speak's `force`)

// MAX_ALERT_LINES bounds the queued alert lines: past it a new one is skipped
// (never the queue cancelled). A real burst is a handful; this only stops a
// runaway from talking for minutes.
export const MAX_ALERT_LINES = 12;

export function isVoiceSupported(): boolean {
  return voiceSupported;
}

// getAvailableVoices lists the browser's installed voices, for a future
// voice-select control (empty until the browser fires voiceschanged at least
// once — callers should re-read after that event).
export function getAvailableVoices(): SpeechSynthesisVoice[] {
  return synth ? synth.getVoices() : [];
}

function pickVoice(): void {
  if (!synth) return;
  const voices = synth.getVoices();
  if (!voices.length) return;
  chosenVoice =
    voices.find((v) => v.name === prefs.voiceName) ||
    voices.find((v) => v.lang && v.lang.toLowerCase().startsWith('en')) ||
    voices[0];
}

if (synth) {
  pickVoice();
  synth.onvoiceschanged = pickVoice;
}

// speak is the low-level TTS primitive: queues an utterance if voice is on
// and supported, silently no-ops otherwise. Caps the backlog at 3 outstanding
// utterances (cancels and drops the queue beyond that) so a burst of reads
// can't leave the voice talking long after the tape has moved on.
//
// The cap is NARRATION's. A forced utterance (a scan's spoken alert, "SYM:
// green dildo") names a stock that just fired, and a burst of them is ordinary
// (the Signal window hears its list's scan plus the dildo and climax scans):
// cancelling the queue on the fifth line dropped the first four and cut the
// first off mid-word. So alert lines are never cancelled or dropped by the cap:
// one arriving behind a narration backlog cancels the narration (as before) and
// is spoken; narration arriving behind queued alert lines is skipped instead of
// cancelling them; and only a runaway past MAX_ALERT_LINES queued alert lines
// skips the newest. Before holding anything back for queued alert lines it
// checks the engine really is busy (speaking or pending), so counts left by an
// 'end' event that never fired cannot block them.
//
// `opts.force` bypasses the shared `prefs.on` gate ONLY. It exists for the
// Signal window (?pane=signal), which has its own per-window voice toggle
// (`mktscanner.signal.voice.v1`, default ON) that must work independently of
// the tape reader's shared narration preference — that one defaults OFF and
// is about reading the tape, so gating a trade signal behind it meant nothing
// was ever spoken with the defaults. A forced utterance still uses everything
// else this module owns: the chosen voice, the shared rate, and the same
// outstanding-queue cap (so a signal burst cannot pile up behind tape
// narration or vice versa). Every existing caller passes no second argument
// and behaves exactly as before.
export function speak(text: string, opts?: { force?: boolean }): void {
  const force = !!opts?.force;
  if ((!prefs.on && !force) || !synth || !text) return;
  if ((outstanding > 3 || alertLines >= MAX_ALERT_LINES) && alertLines > 0 && !synth.speaking && !synth.pending) {
    // About to hold something back for queued alert lines, yet the engine is
    // idle: the counts were left by end events that never came. Start over.
    outstanding = 0;
    alertLines = 0;
  }
  if (outstanding > 3) {
    if (alertLines === 0) {
      synth.cancel(); // only narration is waiting: drop it, as always
      outstanding = 0;
    } else if (!force) {
      return; // alert lines are waiting: they are not cut for a read
    }
  }
  if (force && alertLines >= MAX_ALERT_LINES) return;
  const u = new SpeechSynthesisUtterance(String(text));
  u.rate = prefs.rate;
  u.volume = 1;
  if (chosenVoice) u.voice = chosenVoice;
  outstanding++;
  if (force) alertLines++;
  const done = () => {
    outstanding = Math.max(0, outstanding - 1);
    if (force) alertLines = Math.max(0, alertLines - 1);
  };
  u.onend = done;
  u.onerror = done;
  synth.speak(u);
}

// stopSpeaking cancels any in-progress or queued utterance immediately —
// called when the user pauses, mirroring app.js's initPause() cutting speech
// off mid-sentence rather than letting it finish over a now-paused feed.
export function stopSpeaking(): void {
  if (!synth) return;
  synth.cancel();
  outstanding = 0;
  alertLines = 0;
}

// speakRegimeAlert narrates the instant, free local-verdict alert the moment a
// regime shift is detected (tape.regime) — ported from app.js's
// onRegimeAlert(). Prefers the local verdict's own text (a proper
// who's-in-control read, arrives instantly); falls back to the raw detector
// headline when no verdict text is available.
export function speakRegimeAlert(frame: TapeRegimeFrame): void {
  if (!prefs.instant) return;
  const v = frame.verdict;
  if (v && v.text) {
    const who = v.control === 'balanced' ? '' : `${v.control} in control. `;
    speak(who + v.text);
  } else {
    speak(frame.headline);
  }
}

// speakCard narrates a completed tape-reading card (tape.card) — ported from
// app.js's renderRead()/speakRead() dedup rules. Never speaks (or blips) a
// replayed card (history resent on reconnect), a "local"-model card (its
// verdict was already spoken by the instant tape.regime alert — speaking it
// again here would repeat every regime shift twice), or a card belonging to a
// symbol we're no longer watching.
//
// Note: unlike the Python source's card (which spreads ClaudeRead.control),
// core.TapeCardMsg carries no "control" field — the ongoing per-symbol
// control read lives on TapeStat.verdict.control instead. This narrates the
// card's own whatsHappening/headline text rather than an invented
// "buyers/sellers in control" prefix.
export function speakCard(card: TapeCard, replay: boolean, currentSym: string | null): void {
  const stale = replay || card.model === 'local' || (!!card.sym && !!currentSym && card.sym !== currentSym);
  if (stale) return;
  if (prefs.on) {
    if (card.err) {
      // The instant alert already spoke the headline; only say it here when
      // instant alerts are off (avoids repeating).
      if (!prefs.instant) speak(card.headline || 'Regime shift detected');
      return;
    }
    speak(card.whatsHappening || card.headline || '');
  } else if (!card.err) {
    // Voice is off: a short audible cue in place of narration, same as the
    // Python UI's blip().
    play('alert');
  }
}
