// voice.ts's speech queue cap (web-ux-1). speak() kept at most 3 utterances
// outstanding and, on the next one, CANCELLED the whole queue: right for the
// tape reader's narration (a burst of reads must not leave the voice talking
// long after the tape has moved on), wrong for the scans' spoken alerts
// ("SYM: green dildo"), which each name a stock that just fired. Since the
// Signal window hears the dildo and climax scans on top of its list's, five
// alerts in one burst are ordinary - and the fifth cancelled the first four,
// cutting the first off mid-word. Alert lines (speak's `force`) are never
// cancelled or dropped by the cap any more; narration keeps it.
//
// Node env: a fake speechSynthesis (a queue the test finishes by hand) and a
// fake SpeechSynthesisUtterance stand in, and the module is re-imported per
// test (its counters are module-level).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

interface FakeUtterance {
  text: string;
  onend: (() => void) | null;
  onerror: (() => void) | null;
}

class FakeSynth {
  queue: FakeUtterance[] = [];
  spoken: string[] = []; // every utterance handed to speak(), in order
  cancels = 0;
  onvoiceschanged: unknown = null;
  get speaking(): boolean {
    return this.queue.length > 0;
  }
  get pending(): boolean {
    return this.queue.length > 1;
  }
  getVoices(): unknown[] {
    return [];
  }
  speak(u: FakeUtterance): void {
    this.queue.push(u);
    this.spoken.push(u.text);
  }
  cancel(): void {
    this.cancels++;
    this.queue = [];
  }
  // finish ends the utterance being spoken (the engine's 'end' event).
  finish(): void {
    const u = this.queue.shift();
    u?.onend?.();
  }
  // queued is what is still to be heard (the one speaking first).
  queued(): string[] {
    return this.queue.map((u) => u.text);
  }
}

let synth: FakeSynth;

async function freshVoice() {
  vi.resetModules();
  return await import('./voice');
}

beforeEach(() => {
  synth = new FakeSynth();
  (globalThis as unknown as { window: unknown }).window = { speechSynthesis: synth };
  (globalThis as unknown as { SpeechSynthesisUtterance: unknown }).SpeechSynthesisUtterance = class {
    text: string;
    rate = 1;
    volume = 1;
    voice: unknown = null;
    onend: (() => void) | null = null;
    onerror: (() => void) | null = null;
    constructor(text: string) {
      this.text = text;
    }
  };
});

afterEach(() => {
  delete (globalThis as unknown as { window?: unknown }).window;
  delete (globalThis as unknown as { SpeechSynthesisUtterance?: unknown }).SpeechSynthesisUtterance;
});

const alert = { force: true };

describe('speak(): the queue cap', () => {
  it('a burst of five alert lines is spoken in full: none is cancelled or dropped (the web-ux-1 scenario)', async () => {
    const voice = await freshVoice();
    const lines = ['AAA: green dildo', 'BBB: green dildo', 'CCC: nine E M A flag, sideways', 'DDD: nine E M A flag, sideways', 'EEE: volume climax'];
    for (const l of lines) voice.speak(l, alert);
    expect(synth.cancels).toBe(0);
    expect(synth.queued()).toEqual(lines);
    // and they are heard one after another as each ends
    for (let i = 0; i < lines.length; i++) synth.finish();
    expect(synth.queued()).toEqual([]);
  });

  it('narration keeps its cap: a fifth read while four are outstanding cancels the backlog (as before)', async () => {
    const voice = await freshVoice();
    voice.setVoiceOn(true);
    for (let i = 1; i <= 5; i++) voice.speak(`read ${i}`);
    expect(synth.cancels).toBe(1);
    expect(synth.queued()).toEqual(['read 5']);
  });

  it('an alert line behind a narration backlog cuts the narration, not itself', async () => {
    const voice = await freshVoice();
    voice.setVoiceOn(true);
    for (let i = 1; i <= 4; i++) voice.speak(`read ${i}`);
    voice.speak('AAA: bottom confirmed', alert);
    expect(synth.cancels).toBe(1);
    expect(synth.queued()).toEqual(['AAA: bottom confirmed']);
  });

  it('narration behind queued alert lines never cancels them: that read is skipped', async () => {
    const voice = await freshVoice();
    voice.setVoiceOn(true);
    for (const s of ['AAA', 'BBB', 'CCC', 'DDD']) voice.speak(`${s}: green dildo`, alert);
    voice.speak('buyers in control');
    expect(synth.cancels).toBe(0);
    expect(synth.queued()).toEqual(['AAA: green dildo', 'BBB: green dildo', 'CCC: green dildo', 'DDD: green dildo']);
    // once the alerts have been heard, narration is spoken again
    for (let i = 0; i < 4; i++) synth.finish();
    voice.speak('sellers in control');
    expect(synth.queued()).toEqual(['sellers in control']);
  });

  it('the counters heal when the engine is idle (an end event that never came cannot block alert lines)', async () => {
    const voice = await freshVoice();
    for (let i = 0; i < voice.MAX_ALERT_LINES; i++) voice.speak(`S${i}: green dildo`, alert);
    synth.queue = []; // the engine went quiet without firing a single 'end'
    voice.speak('ZZZ: volume climax', alert);
    expect(synth.queued()).toEqual(['ZZZ: volume climax']);
  });

  it(`a runaway burst is bounded: past MAX_ALERT_LINES queued alert lines, more are skipped, none cancelled`, async () => {
    const voice = await freshVoice();
    for (let i = 0; i < voice.MAX_ALERT_LINES + 3; i++) voice.speak(`S${i}: green dildo`, alert);
    expect(synth.cancels).toBe(0);
    expect(synth.queued()).toHaveLength(voice.MAX_ALERT_LINES);
    expect(synth.queued()[0]).toBe('S0: green dildo');
  });

  it('stopSpeaking still cuts everything, alert lines included, and resets the count', async () => {
    const voice = await freshVoice();
    for (let i = 0; i < 3; i++) voice.speak(`S${i}: green dildo`, alert);
    voice.stopSpeaking();
    expect(synth.queued()).toEqual([]);
    voice.speak('AAA: volume climax', alert);
    expect(synth.queued()).toEqual(['AAA: volume climax']);
  });
});
