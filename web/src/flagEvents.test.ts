// The Signal window's signal-history helpers: pure, plain node.
import { describe, expect, it } from 'vitest';
import { TRADE_KINDS, dateLabel, etTime, flagEventKey, kindBadge, todayET, visibleEvents } from './flagEvents';
import type { FlagEvent, FlagEventKind } from './types';

const ev = (sym: string, ts: number, kind: FlagEventKind): FlagEvent => ({ ts, at: ts + 1000, sym, kind, label: kind, price: 1 });

describe('kindBadge', () => {
  it('maps every kind', () => {
    expect(kindBadge('entry')).toEqual({ text: 'LONG', tone: 'pos' });
    expect(kindBadge('add')).toEqual({ text: 'ADD', tone: 'pos' });
    expect(kindBadge('exit')).toEqual({ text: 'EXIT', tone: 'neg' });
    expect(kindBadge('failed')).toEqual({ text: 'FAILED', tone: 'neg' });
    expect(kindBadge('earlyBreak')).toEqual({ text: 'EARLY', tone: 'warn' });
    expect(kindBadge('emaNear')).toEqual({ text: 'READY', tone: 'warn' });
    expect(kindBadge('flat')).toEqual({ text: 'FLAT', tone: 'muted' });
    expect(kindBadge('impulse')).toEqual({ text: 'BREAKOUT', tone: 'muted' });
    expect(kindBadge('slam')).toEqual({ text: 'SLAM', tone: 'neg' });
  });
  it('does not throw on an unknown kind', () => {
    expect(kindBadge('weird' as FlagEventKind).tone).toBe('muted');
  });
});

describe('visibleEvents', () => {
  const evs = [
    ev('AAA', 100, 'impulse'),
    ev('AAA', 200, 'flat'),
    ev('BBB', 300, 'entry'),
    ev('CCC', 300, 'emaNear'),
    ev('AAA', 400, 'exit'),
    ev('DDD', 150, 'failed'),
  ];
  it('drops impulse and orders newest first (ties: later arrival first)', () => {
    expect(visibleEvents(evs, 'all').map((e) => e.sym + e.kind)).toEqual(['AAAexit', 'CCCemaNear', 'BBBentry', 'AAAflat', 'DDDfailed']);
  });
  it('trades keeps only trade kinds', () => {
    expect(TRADE_KINDS).toEqual(['entry', 'add', 'exit', 'failed', 'slam']);
    expect(visibleEvents(evs, 'trades').map((e) => e.kind)).toEqual(['exit', 'entry', 'failed']);
  });

  it("a 9 EMA SLAM shows under both filters, right after the FLAG FAILED / EXIT it shares its bar with (it is emitted after them)", () => {
    const slamDay = [ev('SPAI', 500, 'flat'), ev('SPAI', 900, 'failed'), ev('SPAI', 900, 'slam'), ev('ZZZ', 700, 'emaNear')];
    expect(visibleEvents(slamDay, 'all').map((e) => e.sym + e.kind)).toEqual(['SPAIslam', 'SPAIfailed', 'ZZZemaNear', 'SPAIflat']);
    expect(visibleEvents(slamDay, 'trades').map((e) => e.kind)).toEqual(['slam', 'failed']);
  });
  it('does not mutate the input', () => {
    const copy = evs.map((e) => ({ ...e }));
    visibleEvents(evs, 'all');
    expect(evs).toEqual(copy);
  });
});

describe('flagEventKey', () => {
  it('is sym|ts|kind', () => {
    expect(flagEventKey(ev('AAA', 5, 'entry'))).toBe('AAA|5|entry');
  });
});

describe('etTime', () => {
  it('formats 24h Eastern time (EDT and EST)', () => {
    expect(etTime(Date.UTC(2026, 9, 5, 13, 30))).toBe('09:30'); // EDT, UTC-4
    expect(etTime(Date.UTC(2026, 10, 10, 14, 5))).toBe('09:05'); // EST, UTC-5
    expect(etTime(Date.UTC(2026, 9, 5, 4, 0))).toBe('00:00'); // midnight is 00, not 24
    expect(etTime(Date.UTC(2026, 9, 5, 22, 7))).toBe('18:07');
  });
});

describe('todayET', () => {
  it('uses the New York calendar day', () => {
    expect(todayET(Date.UTC(2026, 9, 6, 3, 59))).toBe('2026-10-05'); // 23:59 ET the day before
    expect(todayET(Date.UTC(2026, 9, 6, 4, 0))).toBe('2026-10-06');
    expect(todayET(Date.UTC(2026, 0, 15, 12, 0))).toBe('2026-01-15');
  });
});

describe('dateLabel', () => {
  it('says Today for today', () => {
    expect(dateLabel('2026-10-06', '2026-10-06')).toBe('Today');
  });
  it('formats other days as a calendar date', () => {
    expect(dateLabel('2026-10-05', '2026-10-06')).toBe('Mon 5 Oct');
    expect(dateLabel('2026-01-01', '2026-10-06')).toBe('Thu 1 Jan');
    expect(dateLabel('2026-12-31', '2026-10-06')).toBe('Thu 31 Dec');
  });
  it('passes through an unparseable date', () => {
    expect(dateLabel('nope', '2026-10-06')).toBe('nope');
  });
});
