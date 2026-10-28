// The Signal window's left-bar flag list as it renders (server-side, no DOM),
// for the seeded "9EMA Slam" scan: its rows carry e9SlamAge / e9SlamPct /
// e9Dist but no e9State, so the list must badge them SLAM (not a dash), age
// them by the slam, say so in the empty text, and list the scan in the picker
// with the three stage scans. The row rules themselves are pinned in e9.test.ts
// (flagListRows); this proves the component wires them to the screen.
import { afterEach, describe, expect, it } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { FlagScanList } from './FlagScanList';
import { fieldDef } from '../fields';
import { useStore, type GridData } from '../store';
import type { ScanDef } from '../types';

const scan = (id: string, name: string): ScanDef =>
  ({ id, name, enabled: true, filter: {}, sort: [], limit: 50, columns: [], alert: { enabled: true } }) as unknown as ScanDef;

const SCANS = [
  scan('gappers', 'Gappers'),
  scan('e9flag-slam', '9EMA Slam'),
  scan('e9flag-breakout', '9EMA Flag — breakout'),
  scan('e9flag-sideways', '9EMA Flag — sideways'),
  scan('e9flag-ready', '9EMA Flag — ready'),
];

const SPAI_ROW = { last: 4.21, chgDay: 12.5, pmVolX: 1.6, e9SlamPct: -4.32, e9SlamAge: 95, e9Dist: -6.21, news: false };

// zustand's hook reads its INITIAL state during a server render (no subscription),
// so the test writes what it wants into that snapshot (and puts it back after).
const initial = useStore.getInitialState();
const saved = { feed: initial.feed, scans: initial.scans, grids: initial.grids, mutedScans: initial.mutedScans };

function render(scanId: string, opts: { scans?: ScanDef[]; grids?: Record<string, GridData> } = {}): string {
  Object.assign(useStore.getInitialState(), {
    feed: 'up',
    scans: opts.scans ?? SCANS,
    grids: opts.grids ?? {},
    mutedScans: {},
  });
  return renderToStaticMarkup(<FlagScanList scanId={scanId} onScanId={() => {}} onChart={new Set(['SPAI'])} onPick={() => {}} />);
}

afterEach(() => {
  Object.assign(useStore.getInitialState(), saved);
});

describe('FlagScanList — the 9EMA Slam scan', () => {
  it('a slam row reads SLAM in the stage column (red badge), its EMA distance, and the time since the slam', () => {
    const html = render('e9flag-slam', { grids: { 'e9flag-slam': { order: ['SPAI'], rows: { SPAI: SPAI_ROW }, asOf: 1 } } });
    expect(html).toContain('<span class="e9-badge slam"');
    expect(html).toMatch(/class="e9-badge slam"[^>]*>SLAM<\/span>/);
    expect(html).toContain('-6.2%');
    expect(html).toContain('>1.6m<');
    expect(html).toContain('title="slammed 4.3% under the 9 EMA · 1.6m ago · pm vol 1.60×"');
    expect(html).toContain('on-chart'); // SPAI is on a chart in this window
    expect(html).toContain('Flag scan · 1');
  });

  it("the Age header explains the slam's age, not the stage's", () => {
    const html = render('e9flag-slam', { grids: { 'e9flag-slam': { order: ['SPAI'], rows: { SPAI: SPAI_ROW }, asOf: 1 } } });
    const slamHint = fieldDef('e9SlamAge')!.hint!;
    const stageHint = fieldDef('e9Age')!.hint!;
    expect(html).toContain(`title="${escapeAttr(slamHint)}">Age</span>`);
    expect(html).not.toContain(`title="${escapeAttr(stageHint)}">Age</span>`);
    // a stage scan keeps the stage's hint
    const side = render('e9flag-sideways', { grids: { 'e9flag-sideways': { order: ['BEAT'], rows: { BEAT: { e9State: 3, e9Age: 60, e9Dist: 2 } }, asOf: 1 } } });
    expect(side).toContain(`title="${escapeAttr(stageHint)}">Age</span>`);
    expect(side).toMatch(/class="e9-badge ready"[^>]*>READY<\/span>/);
  });

  it('no slam in the window: the empty text says so (not "no stocks flagging")', () => {
    const html = render('e9flag-slam', { grids: { 'e9flag-slam': { order: [], rows: {}, asOf: 1 } } });
    expect(html).toContain('No 9 EMA slams in the last 5 minutes');
    expect(html).not.toContain('No stocks flagging right now');
    // the stage scans keep their own wording
    expect(render('e9flag-sideways', { grids: { 'e9flag-sideways': { order: [], rows: {}, asOf: 1 } } })).toContain('No stocks flagging right now');
  });

  it('a scanner that has not seeded the slam scan yet says to restart it, naming the slam scan', () => {
    const html = render('e9flag-slam', { scans: SCANS.filter((s) => s.id !== 'e9flag-slam') });
    expect(html).toContain('The 9EMA Slam scan is not on this scanner');
    expect(html).toContain('run.bat');
  });

  it("the picker lists it with the seeded flag scans, after the three stage scans, as 'slam'", () => {
    const html = render('e9flag-slam', { grids: { 'e9flag-slam': { order: [], rows: {}, asOf: 1 } } });
    const group = /<optgroup label="9EMA flag scans">(.*?)<\/optgroup>/.exec(html)?.[1] ?? '';
    const options = [...group.matchAll(/<option value="([^"]+)"[^>]*>([^<]*)<\/option>/g)].map((m) => [m[1], m[2]]);
    expect(options).toEqual([
      ['e9flag-sideways', 'sideways'],
      ['e9flag-ready', 'ready'],
      ['e9flag-breakout', 'breakout'],
      ['e9flag-slam', 'slam'],
    ]);
  });
});

// GREEN DILDO -> 9 EMA PULLBACK and VOLUME CLIMAX -> BOTTOM CONFIRMED (2026-10-07):
// five more seeded scans the list can show. Their rows carry the dildo* /
// climax* columns and no e9 fields, so the list badges them by the scan, swaps
// the EMA column for the dildo's body or the climax's drop, ages them by their
// own event and says so in the headers, the empty text and the picker.
const ALERT_SCANS = [
  ...SCANS,
  scan('climax-confirm', 'Bottom Confirmed'),
  scan('dildo-touch', 'Dildo 9EMA Touch'),
  scan('climax-1m', 'Volume Climax'),
  scan('dildo-1m', 'Green Dildo'),
  scan('dildo-pb', 'Dildo 9EMA Pullback'),
];
const PDSB_DILDO = { last: 0.578, dildoBody: 18.44, dildoVolX: 18.04, dildoDvol: 1_234_567, dildoAge: 63, chgDay: 25.1, vol: 5e6, news: false };
const BIYA_BOTTOM = { last: 1.879, climaxConfPx: 1.879, climaxLow: 1.78, climaxDropPct: 13.59, climaxVolX: 9.6, climaxConfAge: 63, chgDay: -6.5, news: false };

describe('FlagScanList — the dildo and climax alert scans', () => {
  it('a Green Dildo row: DILDO badge, the body under a "Body" header, the time since the dildo', () => {
    const html = render('dildo-1m', { scans: ALERT_SCANS, grids: { 'dildo-1m': { order: ['PDSB'], rows: { PDSB: PDSB_DILDO }, asOf: 1 } } });
    expect(html).toMatch(/class="e9-badge dildo"[^>]*>DILDO<\/span>/);
    expect(html).toContain('>+18.4%<');
    expect(html).toContain('>1.1m<');
    expect(html).toContain(`title="${escapeAttr(fieldDef('dildoBody')!.hint!)}">Body</span>`);
    expect(html).toContain(`title="${escapeAttr(fieldDef('dildoAge')!.hint!)}">Age</span>`);
    expect(html).not.toContain('>EMA</span>');
    expect(html).toContain('title="green dildo +18.4% body · 18.0× volume · $1.2M candle · 1.1m ago"');
  });

  it('a Bottom Confirmed row: BOTTOM badge, the drop under a "Drop" header (red), the time since the confirmation', () => {
    const html = render('climax-confirm', { scans: ALERT_SCANS, grids: { 'climax-confirm': { order: ['BIYA'], rows: { BIYA: BIYA_BOTTOM }, asOf: 1 } } });
    expect(html).toMatch(/class="e9-badge bottom"[^>]*>BOTTOM<\/span>/);
    expect(html).toContain('class="sb-num neg">-13.6%<');
    expect(html).toContain(`title="${escapeAttr(fieldDef('climaxDropPct')!.hint!)}">Drop</span>`);
    expect(html).toContain(`title="${escapeAttr(fieldDef('climaxConfAge')!.hint!)}">Age</span>`);
  });

  it('the PB, TOUCH and CLIMAX badges', () => {
    const one = (id: string, row: Record<string, number>) =>
      render(id, { scans: ALERT_SCANS, grids: { [id]: { order: ['X'], rows: { X: row }, asOf: 1 } } });
    expect(one('dildo-pb', { dildoPbPx: 0.57, dildoBody: 9, dildoPbAge: 125 })).toMatch(/class="e9-badge pb"[^>]*>PB<\/span>/);
    expect(one('dildo-touch', { dildoBody: 9, dildoTouchAge: 30 })).toMatch(/class="e9-badge touch"[^>]*>TOUCH<\/span>/);
    expect(one('climax-1m', { climaxDropPct: 7, climaxAge: 70 })).toMatch(/class="e9-badge climax"[^>]*>CLIMAX<\/span>/);
  });

  it('empty states say what the scan waits for', () => {
    const empty = (id: string) => render(id, { scans: ALERT_SCANS, grids: { [id]: { order: [], rows: {}, asOf: 1 } } });
    expect(empty('dildo-1m')).toContain('No green dildo in the last 4 minutes');
    expect(empty('dildo-pb')).toContain('No 9 EMA pullback in the last 4 minutes');
    expect(empty('climax-1m')).toContain('No volume climax in the last 4 minutes');
    expect(empty('climax-confirm')).toContain('No bottom confirmed in the last 4 minutes');
    for (const id of ['dildo-1m', 'dildo-pb', 'dildo-touch', 'climax-1m', 'climax-confirm']) expect(empty(id), id).not.toContain('No stocks flagging');
  });

  it('a scanner that has not seeded them yet names the scan and says to restart it', () => {
    const html = render('climax-1m', { scans: SCANS });
    expect(html).toContain('The Volume Climax scan is not on this scanner');
    expect(html).toContain('run.bat');
  });

  it('the picker lists them in their own group after the 9EMA flag scans: dildo first, then climax', () => {
    const html = render('dildo-1m', { scans: ALERT_SCANS, grids: { 'dildo-1m': { order: [], rows: {}, asOf: 1 } } });
    const groups = [...html.matchAll(/<optgroup label="([^"]+)">(.*?)<\/optgroup>/g)].map((m) => m[1]);
    expect(groups).toEqual(['9EMA flag scans', '1-minute alerts', 'your scans']);
    const group = /<optgroup label="1-minute alerts">(.*?)<\/optgroup>/.exec(html)?.[1] ?? '';
    const options = [...group.matchAll(/<option value="([^"]+)"[^>]*>([^<]*)<\/option>/g)].map((m) => [m[1], m[2]]);
    expect(options).toEqual([
      ['dildo-1m', 'HOD dildo'],
      ['dildo-pb', 'dildo pullback'],
      ['dildo-touch', 'dildo touch'],
      ['climax-1m', 'climax'],
      ['climax-confirm', 'bottom'],
    ]);
    // the flag group is unchanged
    const flag = /<optgroup label="9EMA flag scans">(.*?)<\/optgroup>/.exec(html)?.[1] ?? '';
    expect([...flag.matchAll(/<option value="([^"]+)"/g)].map((m) => m[1])).toEqual(['e9flag-sideways', 'e9flag-ready', 'e9flag-breakout', 'e9flag-slam']);
  });
});

// renderToStaticMarkup escapes attribute values; mirror it for the hint strings.
function escapeAttr(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/'/g, '&#x27;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}
