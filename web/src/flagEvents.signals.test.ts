// The SIGNALS history's dildo / climax rows (GREEN DILDO, 9 EMA PULLBACK,
// VOLUME CLIMAX, BOTTOM CONFIRMED and the low-key PB CANCELLED / CLIMAX
// FAILED): badges, the All vs Trades split, the low-key flag and the row
// tooltip. Pure, plain node.
import { describe, expect, it } from 'vitest';
import { TRADE_KINDS, eventTitle, isLowKey, kindBadge, visibleEvents } from './flagEvents';
import type { FlagEvent, FlagEventKind } from './types';

const ev = (sym: string, ts: number, kind: FlagEventKind, note?: string): FlagEvent => ({
  ts,
  at: ts + 1000,
  sym,
  kind,
  label: kind.toUpperCase(),
  price: 1.5,
  ...(note ? { note } : {}),
});

const ALL_KINDS: FlagEventKind[] = [
  'impulse', 'flat', 'emaNear', 'entry', 'add', 'earlyBreak', 'failed', 'exit', 'slam',
  'dildo', 'dildoPb', 'dildoCancel', 'climax', 'bottom', 'climaxFail',
];

describe('kindBadge — the dildo and climax signals', () => {
  it('gives each its own badge', () => {
    expect(kindBadge('dildo')).toEqual({ text: 'DILDO', tone: 'info' });
    expect(kindBadge('dildoPb')).toEqual({ text: '9EMA PB', tone: 'pos' });
    expect(kindBadge('climax')).toEqual({ text: 'CLIMAX', tone: 'warn' });
    expect(kindBadge('bottom')).toEqual({ text: 'BOTTOM', tone: 'pos' });
    expect(kindBadge('dildoCancel')).toEqual({ text: 'PB FAIL', tone: 'muted' });
    expect(kindBadge('climaxFail')).toEqual({ text: 'CLX FAIL', tone: 'muted' });
  });
  it('no two kinds share a badge text', () => {
    const texts = ALL_KINDS.map((k) => kindBadge(k).text);
    expect(new Set(texts).size).toBe(ALL_KINDS.length);
  });
});

describe('visibleEvents — All vs Trades', () => {
  const day = [
    ev('BEAT', 100, 'entry'),
    ev('SBFM', 200, 'dildo', '+9.0% body'),
    ev('SBFM', 300, 'dildoPb', 'close 1.20 vs EMA 1.18'),
    ev('MB', 350, 'dildoCancel', 'low 1.10 broke the floor'),
    ev('BIYA', 400, 'climax', '12.6% under the 10-bar high'),
    ev('BIYA', 500, 'bottom', 'closed 1.88 above'),
    ev('XNDU', 550, 'climaxFail', 'low 8.99 undercut'),
    ev('BEAT', 600, 'exit'),
    ev('AAA', 650, 'impulse'),
  ];
  it('All shows every signal newest first (only the flag BREAKOUT stays hidden)', () => {
    expect(visibleEvents(day, 'all').map((e) => e.kind)).toEqual([
      'exit', 'climaxFail', 'bottom', 'climax', 'dildoCancel', 'dildoPb', 'dildo', 'entry',
    ]);
  });
  it('Trades stays the flag setup only (entries / exits and the slam)', () => {
    expect(TRADE_KINDS).toEqual(['entry', 'add', 'exit', 'failed', 'slam']);
    expect(visibleEvents(day, 'trades').map((e) => e.kind)).toEqual(['exit', 'entry']);
  });
});

describe('isLowKey', () => {
  it('marks only the PB CANCELLED / CLIMAX FAILED rows', () => {
    expect(ALL_KINDS.filter(isLowKey)).toEqual(['dildoCancel', 'climaxFail']);
  });
});

describe('eventTitle', () => {
  it('reads label, price and the note', () => {
    expect(eventTitle(ev('SBFM', 1, 'dildo', '+9.0% body (4.5x prior)'))).toBe(
      'DILDO at 1.5 — +9.0% body (4.5x prior) — click to load on the active chart, drag onto a chart',
    );
  });
  it('a flag row (no note) keeps the old text', () => {
    expect(eventTitle({ ...ev('BEAT', 1, 'entry'), price: null })).toBe('ENTRY at – — click to load on the active chart, drag onto a chart');
  });
});
