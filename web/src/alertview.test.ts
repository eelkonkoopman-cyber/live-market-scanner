// Which scan alerts a window plays and speaks (web-ux-2). The server treats an
// EMPTY scan view as "every scan", so a window that never narrows its view - the
// tape reader pop-out (open all day), the review pop-out, a charts window whose
// side boxes show no scan - is sent every scan's alert frames, and ws.ts played
// and force-spoke each of them. With the four whole-market dildo / climax scans
// alerting and speaking, every "SYM: green dildo" was heard twice (the Signal
// window AND the tape window), and the Signal window's controls could not
// silence the tape window's copy.
//
// A window that tiles no scan of its own (main.tsx calls hearOnlyViewedScans
// for tape, charts, review and signal) now hears only the scans it asked for
// (viewTiles / viewActive; an empty view = none). The main window and a
// single-scan pop-out keep "no view = every scan", as before.
//
// ws.ts is driven the way the app does - through the socket's onmessage - with
// audio.ts and voice.ts replaced by spies; each test loads a fresh ws.ts (its
// view and flag are module state) and a fresh store.
import { describe, expect, it, vi } from 'vitest';

vi.mock('./audio', () => ({ play: vi.fn() }));
vi.mock('./voice', () => ({ speak: vi.fn(), speakRegimeAlert: vi.fn(), speakCard: vi.fn() }));

const SCANS = [
  { id: 'e9flag-sideways', name: '9EMA flag - sideways', enabled: true, alert: { enabled: true, speak: true, sound: 'flat' } },
  { id: 'dildo-1m', name: 'Green Dildo', enabled: true, alert: { enabled: true, speak: true, sound: 'dildo' } },
  { id: 'climax-1m', name: 'Volume Climax', enabled: true, alert: { enabled: true, speak: true, sound: 'climax' } },
];

async function freshWindow(tilesNoScan: boolean) {
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
  if (tilesNoScan) ws.hearOnlyViewedScans();
  ws.start();
  useStore.setState({ scans: SCANS as never, mutedScans: {}, signalSoundMuted: false, signalVoiceOff: false });
  const heard = (scanId: string, sym: string) => {
    vi.mocked(play).mockClear();
    vi.mocked(speak).mockClear();
    const sound = SCANS.find((x) => x.id === scanId)!.alert.sound;
    sock!.onmessage!({ data: JSON.stringify({ t: 'alert', d: { scanId, sym, sound } }) });
    return { tone: vi.mocked(play).mock.calls.map((c) => c[0]), said: vi.mocked(speak).mock.calls.map((c) => c[0]) };
  };
  return { ws, heard };
}

describe('scan alerts in a window that tiles no scan (tape, charts, review, signal)', () => {
  it('the tape reader (never narrows its view) neither plays nor speaks any scan alert', async () => {
    const { heard } = await freshWindow(true);
    expect(heard('dildo-1m', 'ABCD')).toEqual({ tone: [], said: [] });
    expect(heard('climax-1m', 'WXYZ')).toEqual({ tone: [], said: [] });
    expect(heard('e9flag-sideways', 'BEAT')).toEqual({ tone: [], said: [] });
  });

  it('a charts window whose side boxes show no scan (viewTiles([])) is silent too', async () => {
    const { ws, heard } = await freshWindow(true);
    ws.viewTiles([]);
    expect(heard('dildo-1m', 'ABCD')).toEqual({ tone: [], said: [] });
  });

  it('it hears exactly the scans it asked for - also when a server that forgot its view (a reconnect) sends others', async () => {
    const { ws, heard } = await freshWindow(true);
    ws.viewTiles(['e9flag-sideways', 'dildo-1m']);
    expect(heard('dildo-1m', 'ABCD')).toEqual({ tone: ['dildo'], said: ['ABCD: HOD dildo'] });
    expect(heard('e9flag-sideways', 'BEAT')).toEqual({ tone: ['flat'], said: ['BEAT: nine E M A flag, sideways'] });
    expect(heard('climax-1m', 'WXYZ')).toEqual({ tone: [], said: [] });
    ws.viewActive('climax-1m');
    expect(heard('climax-1m', 'WXYZ')).toEqual({ tone: ['climax'], said: ['WXYZ: volume climax'] });
    expect(heard('dildo-1m', 'ABCD')).toEqual({ tone: [], said: [] });
  });
});

describe('scan alerts in the main window and a single-scan pop-out (unchanged)', () => {
  it('with no view sent it hears every scan, as before', async () => {
    const { heard } = await freshWindow(false);
    expect(heard('dildo-1m', 'ABCD')).toEqual({ tone: ['dildo'], said: ['ABCD: HOD dildo'] });
    expect(heard('climax-1m', 'WXYZ')).toEqual({ tone: ['climax'], said: ['WXYZ: volume climax'] });
  });

  it('narrowed to its tiles it hears those; all tiles closed (an empty view) it hears every scan again, as before', async () => {
    const { ws, heard } = await freshWindow(false);
    ws.viewTiles(['climax-1m']);
    expect(heard('climax-1m', 'WXYZ').tone).toEqual(['climax']);
    expect(heard('dildo-1m', 'ABCD').tone).toEqual([]);
    ws.viewTiles([]);
    expect(heard('dildo-1m', 'ABCD').tone).toEqual(['dildo']);
  });
});

describe('Green Dildo with a fresh headline (2026-10-13)', () => {
  it('says the headline age; other scans and stale news unchanged; same tone', async () => {
    const { heard } = await freshWindow(false);
    const { useStore } = await import('./store');
    const now = Date.now();
    useStore.setState({
      news: [
        { id: 'n1', tickers: ['ABCD', 'WXYZ'], headline: 'h', source: 's', url: '', ts: now - 10_000, sentiment: '' },
        { id: 'n2', tickers: ['OLDN'], headline: 'h', source: 's', url: '', ts: now - 2 * 3_600_000, sentiment: '' },
      ],
    });
    expect(heard('dildo-1m', 'ABCD')).toEqual({ tone: ['dildo'], said: ['ABCD: HOD dildo. News headline 10 seconds ago'] });
    expect(heard('dildo-1m', 'OLDN').said).toEqual(['OLDN: HOD dildo']);
    expect(heard('climax-1m', 'WXYZ').said).toEqual(['WXYZ: volume climax']);
  });
});
