// Regression coverage for the per-window, per-scan sound-alert mute ("The
// sound alert needs to be configured PER scan window... I want to choose
// from which scanner i get a sound"). mutedScans is deliberately plain
// in-memory Zustand state — never localStorage/sessionStorage — because this
// app's windows share one WebView2 profile and localStorage is already
// deliberately shared across them (GLOBAL_SYM_KEY's follow mode); a mute
// leaking the same way would be the opposite of "per window". These tests
// pin the toggle's own correctness; true cross-process window isolation is
// guaranteed by construction (a separate JS runtime per window), not by
// anything testable in a single process — see store.ts's doc comment.
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { useStore } from './store';
import { play, silence } from './audio';
import { speak, stopSpeaking } from './voice';
import { start } from './ws';

// ws.ts's dispatch() is private: the alert tests below drive it the way the app
// does — through the socket's onmessage — with audio.ts and voice.ts replaced by
// spies (neither has a DOM in this node environment, and the point is only WHICH
// of the two calls an alert frame reaches).
vi.mock('./audio', () => ({ play: vi.fn(), silence: vi.fn() }));
vi.mock('./voice', () => ({ speak: vi.fn(), stopSpeaking: vi.fn(), speakRegimeAlert: vi.fn(), speakCard: vi.fn() }));

describe('toggleScanMute', () => {
  it('a scan never touched is audible by default', () => {
    expect(useStore.getState().mutedScans['untouched-scan']).toBeFalsy();
  });

  it('toggling flips it on, then off again', () => {
    const { toggleScanMute } = useStore.getState();
    toggleScanMute('scan-a');
    expect(useStore.getState().mutedScans['scan-a']).toBe(true);
    toggleScanMute('scan-a');
    expect(useStore.getState().mutedScans['scan-a']).toBe(false);
  });

  it('muting one scan does not affect another (each button reads its own scanId)', () => {
    const { toggleScanMute } = useStore.getState();
    toggleScanMute('scan-x');
    expect(useStore.getState().mutedScans['scan-x']).toBe(true);
    expect(useStore.getState().mutedScans['scan-y']).toBeFalsy();
  });

  it('setScans prunes mute flags for scans that no longer exist, leaves live ones alone', () => {
    const { toggleScanMute, setScans } = useStore.getState();
    toggleScanMute('will-be-deleted');
    toggleScanMute('stays-around');
    setScans([{ id: 'stays-around', name: 'Stays', enabled: true } as never]);
    const after = useStore.getState().mutedScans;
    expect(after['will-be-deleted']).toBeUndefined();
    expect(after['stays-around']).toBe(true);
  });
});

// The Signal window's toolbar 🔊/🔇 and '🗣 Voice' used to gate only the trade
// signals its own cells print; the flag SCAN's alerts (this window now narrows to
// e9flag-sideways, which is seeded with "speak the symbol") went straight through
// ws.ts's alert case and ignored both buttons. SignalPane mirrors the two into
// the store (signalSoundMuted / signalVoiceOff) and the alert case honours them.
describe('alert frames honour the Signal window toolbar (signalSoundMuted / signalVoiceOff)', () => {
  const SCAN = { id: 'e9flag-sideways', name: '9EMA flag - sideways', enabled: true, alert: { enabled: true, speak: true } };
  const ALERT = JSON.stringify({ t: 'alert', d: { scanId: 'e9flag-sideways', sym: 'BEAT', sound: 'flat' } });
  let sock: { onmessage: ((e: { data: string }) => void) | null };
  const fire = () => sock.onmessage!({ data: ALERT });

  beforeAll(() => {
    class FakeSocket {
      static OPEN = 1;
      readyState = 0;
      onopen: unknown = null;
      onclose: unknown = null;
      onerror: unknown = null;
      onmessage: ((e: { data: string }) => void) | null = null;
      constructor() {
        sock = this;
      }
      send(): void {}
      close(): void {}
    }
    vi.stubGlobal('WebSocket', FakeSocket);
    vi.stubGlobal('location', { protocol: 'http:', host: 'localhost:0' });
    start();
  });

  beforeEach(() => {
    useStore.setState({ scans: [SCAN as never], mutedScans: {}, signalSoundMuted: false, signalVoiceOff: false });
    vi.mocked(play).mockClear();
    vi.mocked(speak).mockClear();
    vi.mocked(silence).mockClear();
    vi.mocked(stopSpeaking).mockClear();
  });

  afterEach(() => {
    useStore.setState({ mutedScans: {}, signalSoundMuted: false, signalVoiceOff: false });
  });

  it('the defaults leave every other window alone: the tone plays and the symbol is spoken', () => {
    fire();
    expect(play).toHaveBeenCalledWith('flat');
    expect(speak).toHaveBeenCalledWith('BEAT: nine E M A flag, sideways', { force: true });
  });

  it('the toolbar mute (signalSoundMuted) silences the tone AND the voice, like the list mute does', () => {
    useStore.getState().setSignalSoundMuted(true);
    expect(useStore.getState().signalSoundMuted).toBe(true);
    fire();
    expect(play).not.toHaveBeenCalled();
    expect(speak).not.toHaveBeenCalled();
  });

  it('voice off (signalVoiceOff) keeps the tone and drops only the spoken line', () => {
    useStore.getState().setSignalVoiceOff(true);
    expect(useStore.getState().signalVoiceOff).toBe(true);
    fire();
    expect(play).toHaveBeenCalledWith('flat');
    expect(speak).not.toHaveBeenCalled();
  });

  it("the seeded 9EMA Slam scan's alert: its own falling tone and 'SPAI: nine E M A slam', under the same toolbar gates", () => {
    const SLAM = { id: 'e9flag-slam', name: '9EMA Slam', enabled: true, alert: { enabled: true, speak: true, sound: 'slam' } };
    useStore.setState({ scans: [SCAN as never, SLAM as never] });
    const fireSlam = () => sock.onmessage!({ data: JSON.stringify({ t: 'alert', d: { scanId: 'e9flag-slam', sym: 'SPAI', sound: 'slam' } }) });
    fireSlam();
    expect(play).toHaveBeenCalledWith('slam');
    expect(speak).toHaveBeenCalledWith('SPAI: nine E M A slam', { force: true });
    // voice off: the tone alone
    vi.mocked(play).mockClear();
    vi.mocked(speak).mockClear();
    useStore.getState().setSignalVoiceOff(true);
    fireSlam();
    expect(play).toHaveBeenCalledWith('slam');
    expect(speak).not.toHaveBeenCalled();
    // the toolbar mute: nothing at all
    vi.mocked(play).mockClear();
    useStore.getState().setSignalVoiceOff(false);
    useStore.getState().setSignalSoundMuted(true);
    fireSlam();
    expect(play).not.toHaveBeenCalled();
    expect(speak).not.toHaveBeenCalled();
    // and the list's per-scan mute on the slam scan silences it without touching the sideways scan
    useStore.getState().setSignalSoundMuted(false);
    useStore.getState().toggleScanMute('e9flag-slam');
    fireSlam();
    expect(play).not.toHaveBeenCalled();
    fire();
    expect(play).toHaveBeenCalledWith('flat');
  });

  it("the list's own per-scan mute still works and the toolbar flags are independent of it", () => {
    useStore.getState().toggleScanMute('e9flag-sideways');
    fire();
    expect(play).not.toHaveBeenCalled();
    expect(speak).not.toHaveBeenCalled();
    // un-muting the scan while the toolbar mute is on stays silent
    useStore.getState().toggleScanMute('e9flag-sideways');
    useStore.getState().setSignalSoundMuted(true);
    fire();
    expect(play).not.toHaveBeenCalled();
  });

  // rv2-web-1: tones play one after another (up to 8 s ahead) and up to 12
  // spoken alert lines queue, so a burst at one minute's close kept playing and
  // talking long after the trader pressed 🔇 or turned Voice off: the flags only
  // gated NEW alert frames. Turning either on now also stops what is queued.
  it('pressing 🔇 cuts what a burst already queued: the tones (silence) and the spoken lines (stopSpeaking), once', () => {
    for (let i = 0; i < 10; i++) fire();
    expect(play).toHaveBeenCalledTimes(10);
    useStore.getState().setSignalSoundMuted(true);
    expect(silence).toHaveBeenCalledTimes(1);
    expect(stopSpeaking).toHaveBeenCalledTimes(1);
    // it stays muted: other store updates stop nothing more
    useStore.getState().setSignalSoundMuted(true);
    useStore.setState({ mutedScans: {} });
    expect(silence).toHaveBeenCalledTimes(1);
    expect(stopSpeaking).toHaveBeenCalledTimes(1);
    // un-muting stops nothing; muting again cuts again
    useStore.getState().setSignalSoundMuted(false);
    expect(silence).toHaveBeenCalledTimes(1);
    useStore.getState().setSignalSoundMuted(true);
    expect(silence).toHaveBeenCalledTimes(2);
  });

  it('turning 🗣 Voice off cancels the queued spoken lines and leaves the tones playing', () => {
    for (let i = 0; i < 10; i++) fire();
    useStore.getState().setSignalVoiceOff(true);
    expect(stopSpeaking).toHaveBeenCalledTimes(1);
    expect(silence).not.toHaveBeenCalled();
    useStore.getState().setSignalVoiceOff(false); // voice back on: nothing to stop
    expect(stopSpeaking).toHaveBeenCalledTimes(1);
  });
});
