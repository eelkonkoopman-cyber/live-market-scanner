import { useEffect, useState } from 'react';
import { useStore } from '../store';
import { setTapeClaude, setTapeCommentary } from '../ws';
import {
  getVoicePrefs,
  setVoiceOn,
  setVoiceRate,
  setVoiceName,
  setVoiceInstant,
  isVoiceSupported,
  getAvailableVoices,
  speak,
  stopSpeaking,
} from '../voice';

// TapeControls is the tape reader's toolbar: pause (mutes alerts/voice —
// ingestion and the live gauge/CVD/book stats keep running regardless),
// the per-symbol Claude/Local brain toggle, the client-only voice controls
// (on/off, rate, voice picker, instant-alerts), and the live-commentary
// interval — ported from the standalone Python app's static/index.html
// #toolbar (pause-btn / brain-btn / voice-toggle / voice-rate / voice-select
// / voice-instant / commentary-select).
//
// Gap vs the Python source (flagged, not guessed around): server.py's
// toolbar disables brain-btn/commentary-select off a `has_claude` flag —
// whether an ANTHROPIC_API_KEY is configured at all, server-wide. This Go
// port's wire protocol carries no equivalent signal: core.TapeStat only has
// claudeEnabled (this symbol's current on/off state), not a "Claude is even
// configured" flag. So the brain toggle and commentary control below are
// never disabled for that reason — they just wire straight to
// claudeEnabled/commentarySecs and trust the server to no-op sensibly if no
// key is configured. Worth a follow-up if the orchestration stage wants to
// add that signal.
export function TapeControls({
  sym,
  claudeEnabled,
  commentarySecs,
}: {
  sym: string;
  claudeEnabled: boolean;
  commentarySecs: number;
}) {
  const paused = useStore((s) => s.tapePaused);
  const setPaused = useStore((s) => s.setTapePaused);

  // voice.ts is a plain module (localStorage-backed), not a store — mirror
  // its prefs into local state, refreshed after every setter call (all
  // synchronous) so the toolbar reflects them immediately.
  const [voice, setVoice] = useState(getVoicePrefs());
  const [voices, setVoices] = useState<SpeechSynthesisVoice[]>(() => getAvailableVoices());
  const supported = isVoiceSupported();

  // The voice list is often empty until the browser fires voiceschanged.
  // addEventListener here is additive — it does not disturb voice.ts's own
  // `synth.onvoiceschanged = pickVoice` assignment.
  useEffect(() => {
    if (!supported) return;
    const onChanged = () => setVoices(getAvailableVoices());
    window.speechSynthesis.addEventListener('voiceschanged', onChanged);
    return () => window.speechSynthesis.removeEventListener('voiceschanged', onChanged);
  }, [supported]);

  // Commentary interval: a locally-editable draft, clamped client-side to
  // [15, 600] seconds (0 = off) — mirrors internal/tape/tape.go's
  // clampCommentary / server.py's clamp_commentary. Only re-synced from the
  // server value on a symbol switch, so it doesn't fight the user mid-edit
  // against the ~1Hz tape.stat refresh.
  const [draft, setDraft] = useState(String(commentarySecs));
  useEffect(() => setDraft(String(commentarySecs)), [sym]); // eslint-disable-line react-hooks/exhaustive-deps
  const commitCommentary = () => {
    const n = parseInt(draft, 10);
    const secs = !Number.isFinite(n) || n <= 0 ? 0 : Math.min(600, Math.max(15, n));
    setDraft(String(secs));
    setTapeCommentary(sym, secs);
  };

  const togglePause = () => {
    const next = !paused;
    if (next) stopSpeaking(); // cut off mid-sentence, mirrors app.js's initPause()
    setPaused(next);
  };

  const toggleVoiceOn = () => {
    const on = !voice.on;
    setVoiceOn(on);
    setVoice(getVoicePrefs());
    if (on) speak('Voice on. Claude will narrate the tape.'); // this click also unlocks audio
    else stopSpeaking();
  };

  return (
    <div className="tape-row tape-toolbar">
      <button
        className={`segbtn ${paused ? 'on' : ''}`}
        onClick={togglePause}
        title="Silence alerts, voice and Claude reads. Market data keeps recording, so nothing is lost."
      >
        {paused ? '▶ Resume' : '⏸ Pause'}
      </button>

      <button
        className={`segbtn ${claudeEnabled ? 'on' : ''}`}
        onClick={() => setTapeClaude(sym, !claudeEnabled)}
        title={
          claudeEnabled
            ? 'Claude is reading the tape (uses your API credits). Click for local-only.'
            : 'Local rules only — free and instant. Click to bring Claude in.'
        }
      >
        {claudeEnabled ? '🧠 Claude' : '⚡ Local'}
      </button>

      {supported ? (
        <>
          <button
            className={`segbtn ${voice.on ? 'on' : ''}`}
            onClick={toggleVoiceOn}
            title="Speak regime alerts and Claude reads aloud"
          >
            {voice.on ? '🔊 Voice: On' : '🔇 Voice: Off'}
          </button>
          <label className="tape-toolbar-field">
            Speed
            <input
              type="range"
              min={0.8}
              max={1.3}
              step={0.05}
              value={voice.rate}
              onChange={(e) => {
                setVoiceRate(parseFloat(e.target.value) || 1.0);
                setVoice(getVoicePrefs());
              }}
            />
          </label>
          <label className="tape-toolbar-field">
            Voice
            <select
              value={voice.voiceName}
              onChange={(e) => {
                setVoiceName(e.target.value);
                setVoice(getVoicePrefs());
                if (getVoicePrefs().on) speak('Voice changed.');
              }}
            >
              <option value="">auto</option>
              {voices.map((v) => (
                <option key={v.name} value={v.name}>
                  {v.name} ({v.lang})
                </option>
              ))}
            </select>
          </label>
          <label className="tape-toolbar-field chk">
            <input
              type="checkbox"
              checked={voice.instant}
              onChange={(e) => {
                setVoiceInstant(e.target.checked);
                setVoice(getVoicePrefs());
              }}
            />
            Instant alerts
          </label>
        </>
      ) : (
        <span className="muted fund-note">Voice unsupported in this browser</span>
      )}

      <label
        className="tape-toolbar-field"
        title="Periodic Claude narration when the tape goes quiet (15-600s, 0 = off)"
      >
        Commentary
        <input
          className="commentary-input"
          type="number"
          min={0}
          max={600}
          step={5}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onBlur={commitCommentary}
          onKeyDown={(e) => {
            if (e.key === 'Enter') (e.target as HTMLInputElement).blur();
          }}
        />
        <span className="unit">s</span>
      </label>
    </div>
  );
}
