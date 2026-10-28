// The Signal window's SIGNALS history as it renders (server-side, no DOM) with
// the GREEN DILDO / 9 EMA PULLBACK / VOLUME CLIMAX / BOTTOM rows next to the
// flag rows: distinct badges, the price, the one-line note, the low-key
// cancelled / failed rows, All vs Trades, and a row click loading the chart.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import type { ReactElement } from 'react';
import { FlagEventList, FlagEventRow } from './FlagEventList';
import { todayET } from '../flagEvents';
import { useStore } from '../store';
import type { FlagEvent } from '../types';

const T = Date.UTC(2026, 9, 8, 8, 24); // 04:24 ET
const min = 60_000;

const DAY: FlagEvent[] = [
  { ts: T, at: T + 63_000, sym: 'SBFM', kind: 'dildo', label: 'GREEN DILDO', price: 1.09, note: '+9.0% body (4.5x prior), 200x volume, $218k; floor 1.04' },
  { ts: T + 6 * min, at: T + 7 * min, sym: 'SBFM', kind: 'dildoPb', label: '9 EMA PULLBACK', price: 1.21, note: 'close 1.21 vs EMA 1.19 (+1.6%), 6 min after the dildo' },
  { ts: T + 7 * min, at: T + 8 * min, sym: 'MB', kind: 'dildoCancel', label: 'PB CANCELLED', price: 7.1, note: 'low 7.10 broke the floor 7.20' },
  { ts: T + 8 * min, at: T + 9 * min, sym: 'BIYA', kind: 'climax', label: 'VOLUME CLIMAX', price: 1.78, note: '12.6% under the 10-bar high, 20.0x volume' },
  { ts: T + 9 * min, at: T + 10 * min, sym: 'BIYA', kind: 'bottom', label: 'BOTTOM CONFIRMED', price: 1.879, note: 'closed 1.88 above the climax midpoint 1.84' },
  { ts: T + 10 * min, at: T + 11 * min, sym: 'XNDU', kind: 'climaxFail', label: 'CLIMAX FAILED', price: 8.99, note: 'low 8.99 undercut the 04:32 climax low 9.00' },
  { ts: T + 11 * min, at: T + 12 * min, sym: 'BEAT', kind: 'entry', label: 'LONG (FLAG)', price: 0.6477 },
];

const initial = useStore.getInitialState();
const saved = { flagEvents: initial.flagEvents };

function render(events: FlagEvent[] = DAY, date?: string): string {
  const today = todayET(Date.now());
  const day = date ?? today;
  Object.assign(useStore.getInitialState(), { flagEvents: { date: day, dates: day === today ? [today] : [today, day], events } });
  return renderToStaticMarkup(<FlagEventList onChart={new Set(['BIYA'])} onPick={() => {}} />);
}

afterEach(() => {
  Object.assign(useStore.getInitialState(), saved);
  vi.unstubAllGlobals();
});

describe('FlagEventList — dildo and climax signals', () => {
  it('lists every signal under All with its own badge, price and note', () => {
    const html = render();
    expect(html).toContain('Signals · 7');
    expect(html).toMatch(/class="flagev-badge info"[^>]*>DILDO<\/span>/);
    expect(html).toMatch(/class="flagev-badge pos"[^>]*>9EMA PB<\/span>/);
    expect(html).toMatch(/class="flagev-badge warn"[^>]*>CLIMAX<\/span>/);
    expect(html).toMatch(/class="flagev-badge pos"[^>]*>BOTTOM<\/span>/);
    expect(html).toMatch(/class="flagev-badge muted"[^>]*>PB FAIL<\/span>/);
    expect(html).toMatch(/class="flagev-badge muted"[^>]*>CLX FAIL<\/span>/);
    expect(html).toMatch(/class="flagev-badge pos"[^>]*>LONG<\/span>/);
    expect(html).toContain('>1.09<'); // price, the flag rows' format (fmtPrice)
    expect(html).toContain('<span class="flagev-note">+9.0% body (4.5x prior), 200x volume, $218k; floor 1.04</span>');
    expect(html).toContain('<span class="flagev-note">closed 1.88 above the climax midpoint 1.84</span>');
    expect(html).toContain('>04:24<');
  });

  it('newest first, the flag row without a note line', () => {
    const html = render();
    const order = [...html.matchAll(/<span class="sb-sym">([A-Z]+)<\/span>/g)].map((m) => m[1]);
    expect(order).toEqual(['BEAT', 'XNDU', 'BIYA', 'BIYA', 'MB', 'SBFM', 'SBFM']);
    const beatRow = html.slice(html.indexOf('BEAT'), html.indexOf('XNDU'));
    expect(beatRow).not.toContain('flagev-note');
    expect(html.match(/class="flagev-note"/g)).toHaveLength(6);
  });

  it('the cancelled / failed rows are low-key, the others are not', () => {
    const html = render();
    const rows = [...html.matchAll(/<div class="(sidebox-row flagev-row[^"]*)"/g)].map((m) => m[1]);
    expect(rows).toHaveLength(7);
    expect(rows.filter((c) => c.includes('lowkey'))).toHaveLength(2);
    expect(html).toMatch(/class="sidebox-row flagev-row lowkey"[^>]*title="CLIMAX FAILED at 8.99 — low 8.99 undercut/);
    expect(html).toContain('sidebox-row flagev-row on-chart'); // BIYA is on a chart in this window
  });

  it('Trades keeps only the flag setup rows', () => {
    vi.stubGlobal('localStorage', { getItem: () => 'trades', setItem: () => {} });
    const full = render();
    expect(full).toContain('Signals · 1');
    // rows only — the filter chips list every kind on purpose
    const html = full.slice(full.indexOf('class="sidebox-list"'));
    expect(html).toContain('>LONG<');
    for (const t of ['DILDO', '9EMA PB', 'CLIMAX', 'BOTTOM', 'PB FAIL', 'CLX FAIL']) {
      expect(html).not.toContain(`>${t}<`);
    }
  });

  // UI-2: a day of only dildo / climax signals under Trades has signals — the
  // list must not say it has none (the user checks it for what they missed).
  it('Trades on a day of only dildo / climax signals points to them under All', () => {
    vi.stubGlobal('localStorage', { getItem: () => 'trades', setItem: () => {} });
    const html = render(DAY.filter((e) => e.kind !== 'entry'));
    expect(html).toContain('Signals · 0');
    expect(html).not.toContain('No signals yet today');
    expect(html).toContain('No trade signals yet today — 6 signals under All.');
    expect(render(DAY.slice(0, 1))).toContain('No trade signals yet today — 1 signal under All.');
  });

  it('a past day under Trades the same; a day with nothing to show still says so', () => {
    vi.stubGlobal('localStorage', { getItem: () => 'trades', setItem: () => {} });
    const past = render(DAY.filter((e) => e.kind !== 'entry'), '2026-10-07');
    expect(past).toContain('No trade signals on Wed 7 Oct — 6 signals under All.');
    // A BREAKOUT is hidden under All too: nothing to point to.
    const impulse: FlagEvent = { ts: T, at: T, sym: 'ZZZ', kind: 'impulse', label: 'BREAKOUT', price: 2 };
    expect(render([impulse])).toContain('No signals yet today');
    expect(render([impulse], '2026-10-07')).toContain('No signals on Wed 7 Oct.');
    expect(render([])).toContain('No signals yet today');
  });
});

describe('FlagEventRow — click to chart', () => {
  it('a dildo row loads its symbol on the active chart and drags as text', () => {
    const onPick = vi.fn();
    const row = FlagEventRow({ e: DAY[0]!, onChart: false, fresh: false, onPick }) as ReactElement<{
      onClick: () => void;
      onDragStart: (ev: { dataTransfer: { effectAllowed: string; setData: (t: string, v: string) => void } }) => void;
      draggable: boolean;
      title: string;
    }>;
    row.props.onClick();
    expect(onPick).toHaveBeenCalledWith('SBFM');
    expect(row.props.draggable).toBe(true);
    const setData = vi.fn();
    const dt = { effectAllowed: '', setData };
    row.props.onDragStart({ dataTransfer: dt });
    expect(setData).toHaveBeenCalledWith('text/plain', 'SBFM');
    expect(row.props.title).toContain('GREEN DILDO at 1.09 — +9.0% body');
  });

  it('a bottom row on a chart is marked, a fresh one flashes', () => {
    const html = renderToStaticMarkup(<FlagEventRow e={DAY[4]!} onChart fresh onPick={() => {}} />);
    expect(html).toContain('class="sidebox-row flagev-row on-chart new"');
    expect(html).toContain('>BOTTOM<');
  });
});
