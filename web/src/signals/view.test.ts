// Coverage for the Signal window's per-cell side selector (view.ts): the filter
// is DISPLAY-only, so these tests pin exactly what each side keeps and drops —
// events, level lines, the strip's state/headline, the plan text and the strict
// sound match — on hand-built results (no candles needed: the engine's own
// empty-input result is the type-safe base, so the fixtures cannot drift out of
// step with SignalResult when the engine grows a field).
//
// Iteration 6 adds the setup-agnostic SignalView: pullbackView (the pullback
// cell's lines/state repackaged — pinned to what SignalChart used to compute
// inline), flagView (the 9 EMA flag cell's lines, plan grid, strip text and
// chips) and the flag engine's sounds and spoken lines, including a replay of
// the REAL 2026-10-05 BEAT minute bars through the live engine.
import { describe, expect, it } from 'vitest';
import { evaluate, fmtEt, type SignalEvent, type SignalResult } from './shortFade';
import { evaluateFlag, type FlagEvent, type FlagResult } from './emaFlag';
import { toCandle } from '../chartAgg';
import { etDayKey } from '../marketClock';
import type { Bar } from '../types';
import { evaluateDildo, type DildoEvent } from './dildoPb';
import { evaluateClimax, type ClimaxEvent } from './climax';
import {
  chartMarkers,
  mergeViewEvents,
  eventOnSide,
  eventTag,
  flagView,
  planLine,
  pullbackView,
  sideRead,
  soundForEvent,
  viewForSide,
  voicePhrase,
} from './view';
import beatFixture from './fixtures/beat-2026-10-05.json';
import ptcFixture from './fixtures/ptc-2026-10-05-1325.json';
import spaiFixture from './fixtures/spai-2026-10-07.json';

const BASE: SignalResult = evaluate({ candles: [], tfMs: 60_000, nowMs: 0 });

function ev(over: Partial<SignalEvent> & Pick<SignalEvent, 'kind' | 'label' | 'side'>): SignalEvent {
  return { ts: 1, price: 1, note: '', ...over };
}

// A morning where both sides have printed: the shared spike/crack, a short
// entry, a long entry, both exits, and a new-HOD cover on each side.
const EVENTS: SignalEvent[] = [
  ev({ kind: 'spike', label: 'SPIKE', side: 'short', ts: 1 }),
  ev({ kind: 'crack', label: 'CRACK', side: 'short', ts: 2 }),
  ev({ kind: 'lowerHigh', label: 'LOWER HIGH', side: 'short', ts: 3 }),
  ev({ kind: 'entryLH', label: 'SHORT (LH)', side: 'short', ts: 4, marker: 'sell' }),
  ev({ kind: 'higherLow', label: 'HIGHER LOW', side: 'long', ts: 5 }),
  ev({ kind: 'entryHL', label: 'LONG (HL)', side: 'long', ts: 6, marker: 'buy' }),
  ev({ kind: 'longStopped', label: 'STOP (LONG)', side: 'long', ts: 7, marker: 'sell' }),
  ev({ kind: 'invalidated', label: 'NEW HOD', side: 'short', ts: 8, marker: 'buy' }),
  ev({ kind: 'longTarget', label: 'NEW HOD', side: 'long', ts: 8, marker: 'sell' }),
];

function both(over: Partial<SignalResult> = {}): SignalResult {
  return {
    ...BASE,
    events: EVENTS,
    levels: {
      ...BASE.levels,
      hod: 2,
      runLow: 1,
      pullbackLow: 1.2,
      swingLow: 1.2,
      lowerHigh: 1.6,
      lowerHighTs: 3,
      trigger: 1.19,
      stop: 1.62,
      entryPrice: 1.5,
      entryTs: 4,
      entryKind: 'lh',
      higherLow: 1.3,
      higherLowTs: 5,
      bounceHigh: 1.6,
      longTrigger: 1.61,
      longStop: 1.29,
      longEntryPrice: 1.45,
      longEntryTs: 6,
      longEntryKind: 'hl',
    },
    evidence: { ...BASE.evidence, riskPct: 8 },
    ...over,
  };
}

describe('viewForSide', () => {
  it("'both' hands back the very same object (SignalChart keys effects on result.events / result.levels identity)", () => {
    const r = both({ state: 'both' });
    expect(viewForSide(r, 'both')).toBe(r);
  });

  it("'short' keeps short + shared events, drops long events, and nulls every long-only level", () => {
    const v = viewForSide(both({ state: 'both' }), 'short');
    expect(v.events.map((e) => e.label)).toEqual(['SPIKE', 'CRACK', 'LOWER HIGH', 'SHORT (LH)', 'NEW HOD']);
    // The short's own cover marker (invalidated) survives in a short view.
    expect(v.events.find((e) => e.kind === 'invalidated')?.marker).toBe('buy');
    expect(v.levels.stop).toBe(1.62);
    expect(v.levels.trigger).toBe(1.19);
    expect(v.levels.lowerHigh).toBe(1.6);
    expect(v.levels.hod).toBe(2);
    expect(v.levels.runLow).toBe(1);
    for (const k of ['higherLow', 'higherLowTs', 'bounceHigh', 'longTrigger', 'longStop', 'longEntryPrice', 'longEntryTs', 'longEntryKind'] as const) {
      expect(v.levels[k], k).toBeNull();
    }
    expect(v.evidence.riskPct).toBe(8);
  });

  it("'long' keeps long + shared events, strips the short-cover marker off the shared ones, and nulls every short-only level", () => {
    const v = viewForSide(both({ state: 'both' }), 'long');
    expect(v.events.map((e) => e.label)).toEqual(['SPIKE', 'CRACK', 'HIGHER LOW', 'LONG (HL)', 'STOP (LONG)', 'NEW HOD', 'NEW HOD']);
    // Two 'NEW HOD's: the shared invalidated (list-only now — a ▲ 'cover' on a
    // long-only chart would read as a long entry) and the long's own target.
    const hods = v.events.filter((e) => e.label === 'NEW HOD');
    expect(hods.find((e) => e.kind === 'invalidated')?.marker).toBeUndefined();
    expect(hods.find((e) => e.kind === 'longTarget')?.marker).toBe('sell');
    // The original objects are never mutated.
    expect(EVENTS.find((e) => e.kind === 'invalidated')?.marker).toBe('buy');
    expect(v.levels.higherLow).toBe(1.3);
    expect(v.levels.longStop).toBe(1.29);
    expect(v.levels.longTrigger).toBe(1.61);
    expect(v.levels.hod).toBe(2);
    expect(v.levels.runLow).toBe(1);
    for (const k of ['lowerHigh', 'lowerHighTs', 'trigger', 'stop', 'entryPrice', 'entryTs', 'entryKind'] as const) {
      expect(v.levels[k], k).toBeNull();
    }
    expect(v.evidence.riskPct).toBeNull();
  });

  it('a side with no events of its own still shows the shared spike-top story', () => {
    const r = both({ events: EVENTS.slice(0, 3), state: 'bounce' });
    expect(viewForSide(r, 'long').events.map((e) => e.label)).toEqual(['SPIKE', 'CRACK']);
  });
});

describe('sideRead', () => {
  it("'both' is the engine's combined read verbatim", () => {
    const r = both({ state: 'both', headline: 'TWO-SIDED — x', detail: ['a', 'b'] });
    expect(sideRead(r, 'both')).toEqual({ state: 'both', headline: 'TWO-SIDED — x', detail: ['a', 'b'] });
  });

  it('a single side reports its own open/armed state and its own headline (not the combined one)', () => {
    const r = both({
      state: 'both',
      headline: 'TWO-SIDED — x',
      short: { state: 'short', headline: 'SHORT signalled', detail: ['s'] },
      long: { state: 'long', headline: 'LONG signalled', detail: ['l'] },
    });
    expect(sideRead(r, 'short')).toEqual({ state: 'short', headline: 'SHORT signalled', detail: ['s'] });
    expect(sideRead(r, 'long')).toEqual({ state: 'long', headline: 'LONG signalled', detail: ['l'] });
    expect(sideRead({ ...r, short: { ...r.short, state: 'armed' } }, 'short').state).toBe('armed');
  });

  it('a side with nothing of its own shows the shared prefix while the engine is still in the prefix (spike = do not short)', () => {
    const r = both({
      state: 'spike',
      short: { state: 'none', headline: 'SPIKING — do not short', detail: [] },
      long: { state: 'none', headline: 'Long: no higher low yet', detail: [] },
    });
    expect(sideRead(r, 'short').state).toBe('spike');
    expect(sideRead(r, 'long').state).toBe('spike');
  });

  it("a side masked by the OTHER side's open trade falls back to 'bounce', never 'idle' (the chart hides the base line on idle)", () => {
    const r = both({
      state: 'long',
      short: { state: 'none', headline: 'BOUNCING — a lower high arms the short', detail: [] },
      long: { state: 'long', headline: 'LONG signalled', detail: [] },
    });
    expect(sideRead(r, 'short').state).toBe('bounce');
    expect(viewForSide(r, 'short').state).toBe('bounce');
    const s = both({ state: 'short', short: { state: 'short', headline: 'SHORT', detail: [] }, long: { state: 'none', headline: 'Long: waiting', detail: [] } });
    expect(sideRead(s, 'long').state).toBe('bounce');
  });
});

describe('eventOnSide (sound gate)', () => {
  it("is strict: a 'short' cell never sounds a long event and a 'long' cell never sounds a short one; 'both' sounds all", () => {
    const shortEntry = EVENTS.find((e) => e.kind === 'entryLH')!;
    const longEntry = EVENTS.find((e) => e.kind === 'entryHL')!;
    expect(eventOnSide(shortEntry, 'short')).toBe(true);
    expect(eventOnSide(shortEntry, 'long')).toBe(false);
    expect(eventOnSide(longEntry, 'long')).toBe(true);
    expect(eventOnSide(longEntry, 'short')).toBe(false);
    expect(eventOnSide(shortEntry, 'both') && eventOnSide(longEntry, 'both')).toBe(true);
  });

  it("does NOT let the shared invalidated cover through to a long-only cell (that bell is the short's)", () => {
    const cover = EVENTS.find((e) => e.kind === 'invalidated')!;
    expect(eventOnSide(cover, 'long')).toBe(false);
    expect(eventOnSide(cover, 'short')).toBe(true);
  });
});

describe('eventTag', () => {
  it('tags each row S or L, and leaves shared context untagged', () => {
    const tag = (kind: string, marker?: 'buy' | 'sell') => eventTag(EVENTS.find((e) => e.kind === kind && (marker ? e.marker === marker : true))!);
    expect(tag('entryLH')).toBe('S');
    expect(tag('lowerHigh')).toBe('S');
    expect(tag('entryHL')).toBe('L');
    expect(tag('higherLow')).toBe('L');
    expect(tag('longTarget')).toBe('L');
    expect(tag('spike')).toBeNull();
    expect(tag('crack')).toBeNull();
    // a marker-carrying invalidated is a short being covered -> S; a list-only reset is context
    expect(tag('invalidated')).toBe('S');
    expect(eventTag(ev({ kind: 'invalidated', label: 'NEW HOD', side: 'short' }))).toBeNull();
  });
});

describe('planLine', () => {
  const open = both({
    state: 'both',
    short: { state: 'short', headline: '', detail: [] },
    long: { state: 'long', headline: '', detail: [] },
  });

  it("joins both open plans for 'both' and shows just one side's for a single side", () => {
    expect(planLine(open, 'both')).toBe('Short 1.50 · Stop 1.62  |  Long 1.45 · Stop 1.29');
    expect(planLine(open, 'short')).toBe('Short 1.50 · Stop 1.62');
    expect(planLine(open, 'long')).toBe('Long 1.45 · Stop 1.29');
  });

  it('shows the trigger for an armed side and nothing at all while neither side is armed or open', () => {
    const armed = both({ state: 'armed', short: { state: 'armed', headline: '', detail: [] }, long: { state: 'armed', headline: '', detail: [] } });
    expect(planLine(armed, 'both')).toBe('Short < 1.19 · Stop 1.62  |  Long > 1.61 · Stop 1.29');
    expect(planLine(BASE, 'both')).toBe('');
  });
});

describe('soundForEvent (addendum 4 §5 mapping, by label)', () => {
  const snd = (label: string, side: 'short' | 'long', marker?: 'buy' | 'sell') => soundForEvent(ev({ kind: 'spike', label, side, marker }));

  it('short entries fall, long entries rise, adds are the single note on either side', () => {
    for (const l of ['SHORT (LH)', 'SHORT', 'SHORT (VWAP)', 'SHORT (VWAP REJECT)']) expect(snd(l, 'short'), l).toBe('short');
    for (const l of ['LONG (HL)', 'LONG', 'LONG (VWAP HOLD)']) expect(snd(l, 'long'), l).toBe('long');
    for (const l of ['ADD (LH)', 'BREAKDOWN', 'VWAP REJECT']) expect(snd(l, 'short'), l).toBe('add');
    for (const l of ['ADD (HL)', 'BREAKOUT', 'VWAP HOLD']) expect(snd(l, 'long'), l).toBe('add');
  });

  it('stops ring the bell, targets and lower highs ping, and chatter is silent', () => {
    expect(snd('STOP', 'short')).toBe('bell');
    expect(snd('STOP (LONG)', 'long')).toBe('bell');
    expect(snd('LOWER HIGH', 'short')).toBe('ping');
    expect(snd('T1 BOUNCE HIGH', 'long')).toBe('ping');
    expect(snd('T2 HOD', 'long')).toBe('ping');
    for (const l of ['SPIKE', 'CRACK', 'HIGHER LOW', 'VWAP LOST', 'VWAP RECLAIMED', 'T1 VWAP', 'BASE']) expect(snd(l, 'short'), l).toBeNull();
  });

  it("'NEW HOD' is the long's take-profit ping but the short's cover bell, and only with a marker on the short side", () => {
    expect(snd('NEW HOD', 'long', 'sell')).toBe('ping');
    expect(snd('NEW HOD', 'short', 'buy')).toBe('bell');
    expect(snd('NEW HOD', 'short')).toBeNull(); // a setup reset with no live short is list-only
  });
});

describe('voicePhrase', () => {
  const lv = { ...BASE.levels, stop: 0.674, longStop: 0.604 };
  const say = (kind: SignalEvent['kind'], label: string, side: 'short' | 'long', price: number, marker?: 'buy' | 'sell') =>
    voicePhrase('ATYR', ev({ kind, label, side, price, marker }), lv);

  it('speaks the short lines symbol first, exactly as before', () => {
    expect(say('entryLH', 'SHORT (LH)', 'short', 0.603, 'sell')).toBe('ATYR: short. Lower high. 0.6030. Stop 0.6740.');
    expect(say('entryLH', 'ADD (LH)', 'short', 0.6324, 'sell')).toBe('ATYR: add, lower high, 0.6324.');
    expect(say('breakdown', 'BREAKDOWN', 'short', 0.5763, 'sell')).toBe('ATYR: breakdown, 0.5763.');
    expect(say('vwapReject', 'SHORT (VWAP REJECT)', 'short', 0.549, 'sell')).toBe('ATYR: VWAP rejection, short, 0.5490. Stop 0.6740.');
    expect(say('vwapReject', 'VWAP REJECT', 'short', 0.549, 'sell')).toBe('ATYR: add, VWAP rejection, 0.5490.');
    expect(say('stopped', 'STOP', 'short', 0.687, 'buy')).toBe('ATYR: stop, 0.6870.');
    expect(say('invalidated', 'NEW HOD', 'short', 0.75, 'buy')).toBe('ATYR: new high, cover.');
    expect(say('invalidated', 'NEW HOD', 'short', 0.75)).toBeNull();
  });

  it('speaks the long lines symbol first, reading the LONG stop (not the short one)', () => {
    expect(say('entryHL', 'LONG (HL)', 'long', 0.61, 'buy')).toBe('ATYR: long. Higher low. 0.6100. Stop 0.6040.');
    expect(say('entryHL', 'ADD (HL)', 'long', 0.62, 'buy')).toBe('ATYR: add, higher low, 0.6200.');
    expect(say('breakout', 'BREAKOUT', 'long', 0.66, 'buy')).toBe('ATYR: breakout, 0.6600.');
    expect(say('breakout', 'LONG', 'long', 0.66, 'buy')).toBe('ATYR: long. Breakout. 0.6600. Stop 0.6040.');
    expect(say('vwapHold', 'LONG (VWAP HOLD)', 'long', 0.6, 'buy')).toBe('ATYR: VWAP hold, long, 0.6000. Stop 0.6040.');
    expect(say('vwapHold', 'VWAP HOLD', 'long', 0.6, 'buy')).toBe('ATYR: add, VWAP hold, 0.6000.');
    expect(say('longStopped', 'STOP (LONG)', 'long', 0.6, 'sell')).toBe('ATYR: long stop, 0.6000.');
    expect(say('longTarget', 'NEW HOD', 'long', 0.75, 'sell')).toBe('ATYR: new high, take profit.');
  });

  it('never speaks structure, warnings or targets (chatter)', () => {
    expect(say('higherLow', 'HIGHER LOW', 'long', 0.6)).toBeNull();
    expect(say('lowerHigh', 'LOWER HIGH', 'short', 0.66)).toBeNull();
    expect(say('longTarget', 'T1 BOUNCE HIGH', 'long', 0.66, 'sell')).toBeNull();
    expect(say('longTarget', 'T2 HOD', 'long', 0.75, 'sell')).toBeNull();
    expect(say('longWarn', 'VWAP LOST', 'long', 0.58)).toBeNull();
    expect(say('target', 'T1 VWAP', 'short', 0.58, 'buy')).toBeNull();
  });

  it('leaves the stop off when the level is gone', () => {
    expect(voicePhrase('ATYR', ev({ kind: 'entryHL', label: 'LONG (HL)', side: 'long', price: 0.61 }), { ...lv, longStop: null })).toBe('ATYR: long. Higher low. 0.6100.');
  });
});

// ─── Iteration 6 — SignalView, the flag cell's view, the flag sounds/voice ──

describe('pullbackView — the pullback cell repackaged (lines pinned to what SignalChart used to build inline)', () => {
  const open = both({ state: 'both' });
  const lineKey = (l: { label: string; style: string; tone: string }) => `${l.label}|${l.style}|${l.tone}`;

  it("'both': every level line in the chart's old order, with the old colours expressed as tones", () => {
    const v = pullbackView(open, 'both');
    expect(v.setup).toBe('pullback');
    expect(v.lines.map(lineKey)).toEqual([
      'HOD 2.00|solid|down', // downBody
      'Stop 1.62|dashed|stop', // downBody
      'Short < 1.19|solid|entry', // accent
      'Base 1.00|dotted|up', // upBody
      // lowerHigh is NOT drawn on its own once there is a stop
      'HL 1.30|dotted|up', // upBody
      'Long > 1.61|solid|entry', // accent
      'Long stop 1.29|dashed|up', // upBody
    ]);
    expect(v.lines.map((l) => l.price)).toEqual([2, 1.62, 1.19, 1, 1.3, 1.61, 1.29]);
  });

  it("'both' hands the engine's own events array through (identity-keyed chart effects keep their cadence), state label = the state upper-cased, plan text = planLine", () => {
    const r = both({ state: 'armed', short: { state: 'armed', headline: 'ARMED', detail: ['d'] }, long: { state: 'none', headline: 'x', detail: [] } });
    const v = pullbackView(r, 'both');
    expect(v.events).toBe(r.events);
    expect(v.state).toBe('armed');
    expect(v.stateLabel).toBe('ARMED');
    expect(v.headline).toBe(r.headline);
    expect(v.planText).toBe(planLine(r, 'both'));
    expect(v.planText).toBe('Short < 1.19 · Stop 1.62');
    // the flag-only parts are empty for a pullback view
    expect(v.plan).toEqual([]);
    expect(v.chips).toEqual([]);
    expect(v.boxes).toEqual([]);
  });

  it("a single side draws only its own levels (the short never gets the long's lines and vice versa)", () => {
    const s = pullbackView(open, 'short');
    expect(s.lines.map((l) => l.label)).toEqual(['HOD 2.00', 'Stop 1.62', 'Short < 1.19', 'Base 1.00']);
    const l = pullbackView(open, 'long');
    expect(l.lines.map((x) => x.label)).toEqual(['HOD 2.00', 'Base 1.00', 'HL 1.30', 'Long > 1.61', 'Long stop 1.29']);
    // and the state/headline are that side's own read (sideRead), the plan the side's plan
    expect(l.state).toBe(sideRead(open, 'long').state);
    expect(l.planText).toBe(planLine(open, 'long'));
  });

  it('the base line is hidden while the engine is idle, and a lone lower high shows only while there is no stop', () => {
    const idle = pullbackView(both({ state: 'idle', levels: { ...both().levels, stop: null, trigger: null, higherLow: null, longStop: null, longTrigger: null } }), 'both');
    expect(idle.lines.map((x) => x.label)).toEqual(['HOD 2.00', 'LH 1.60']);
    expect(idle.lines.find((x) => x.label.startsWith('Base'))).toBeUndefined();
    expect(idle.lines.find((x) => x.label.startsWith('LH'))).toMatchObject({ style: 'dotted', tone: 'down' });
  });

  it('with nothing known yet there are no lines at all', () => {
    expect(pullbackView(BASE, 'both').lines).toEqual([]);
  });
});

// A hand-built flag result on the engine's own empty result, so the fixtures
// cannot drift out of step with FlagResult when the engine grows a field.
const FBASE: FlagResult = evaluateFlag({ candles: [], tfMs: 60_000, nowMs: 0 });

function fev(over: Partial<FlagEvent> & Pick<FlagEvent, 'kind' | 'label'>): FlagEvent {
  return { ts: 1, price: 1, note: '', side: 'long', ...over };
}

function flagResult(over: Partial<FlagResult> = {}): FlagResult {
  return { ...FBASE, ...over };
}

describe('flagView', () => {
  const ready = flagResult({
    state: 'ready',
    headline: 'READY — 9 EMA caught up, long above 0.8626',
    detail: ['Flag 0.8329–0.8600 (4 bars), EMA 3.0% below', 'Stop would be 0.8246 (4.4% risk)'],
    levels: { ...FBASE.levels, trigger: 0.86258, stop: 0.8246, flagLow: 0.8329, flagHigh: 0.86, ema: 0.8328 },
    evidence: { ...FBASE.evidence, riskPct: 4.4, emaDistPct: 3.0, extPct: 12.6, flatBars: 4, flatSpanPctOfMove: 18.2, retracePct: 21.5, volDry: 0.45 },
    boxes: [{ startTs: 1, endTs: 5, high: 0.86, low: 0.8329, trigger: 0.86258, readyTs: 5, outcome: 'active' }],
  });

  it("a waiting flag (ready): 'Long > T' accent line, a Trigger/Stop/Risk/EMA distance/Flag range grid, the strip's plan text, the chips", () => {
    const v = flagView(ready);
    expect(v.setup).toBe('flag');
    expect(v.state).toBe('ready');
    expect(v.stateLabel).toBe('READY');
    expect(v.headline).toBe(ready.headline);
    expect(v.detail).toBe(ready.detail);
    expect(v.lines).toEqual([{ price: 0.86258, label: 'Long > 0.8626', style: 'solid', tone: 'entry' }]);
    expect(v.plan).toEqual([
      { k: 'Trigger', v: '0.8626' },
      { k: 'Stop', v: '0.8246' },
      { k: 'Risk %', v: '4.4%' },
      { k: 'EMA distance', v: '3.0%' },
      { k: 'Flag range', v: '0.8329–0.8600' },
    ]);
    expect(v.planText).toBe('Long > 0.8626 · stop 0.8246');
    expect(v.chips).toEqual([
      { k: 'ext', v: '+12.6%' },
      { k: 'flag bars', v: '4' },
      { k: 'span', v: '18% of move' },
      { k: 'EMA dist', v: '3.0%' },
      { k: 'retrace', v: '21.5%' },
      { k: 'vol dry', v: '45% of leg' },
    ]);
    expect(v.boxes).toBe(ready.boxes);
    expect(v.events).toBe(ready.events);
  });

  it("flat reads the same plan but badges 'FLAT'", () => {
    const v = flagView(flagResult({ ...ready, state: 'flat' }));
    expect(v.stateLabel).toBe('FLAT');
    expect(v.lines).toHaveLength(1);
    expect(v.plan.map((p) => p.k)).toEqual(['Trigger', 'Stop', 'Risk %', 'EMA distance', 'Flag range']);
  });

  it("long: the stop line (dashed, stop tone) replaces the trigger; the grid reads Entry / Stop / Risk % / Open P&L; the strip says 'Long E · stop S · +P%'", () => {
    const long = flagResult({
      state: 'long',
      levels: { ...FBASE.levels, entryPrice: 0.6477, stop: 0.5994, trigger: 0.8626, flagLow: 0.8, flagHigh: 0.86 },
      evidence: { ...FBASE.evidence, riskPct: 7.5, openPnlPct: 23.8, emaDistPct: 4.2 },
    });
    const v = flagView(long);
    expect(v.stateLabel).toBe('LONG');
    // a NEXT flag forming while long does not draw its trigger — the spec's lines are 'Long > T' while flat/ready, 'Stop S' while long
    expect(v.lines).toEqual([{ price: 0.5994, label: 'Stop 0.5994', style: 'dashed', tone: 'stop' }]);
    expect(v.plan).toEqual([
      { k: 'Entry', v: '0.6477' },
      { k: 'Stop', v: '0.5994' },
      { k: 'Risk %', v: '7.5%' },
      { k: 'Open P&L', v: '+23.8%' },
      { k: 'EMA distance', v: '4.2%' },
    ]);
    expect(v.planText).toBe('Long 0.6477 · stop 0.5994 · +23.8%');
    // a losing open trade keeps its minus sign
    expect(flagView(flagResult({ ...long, evidence: { ...long.evidence, openPnlPct: -1.25 } })).planText).toBe('Long 0.6477 · stop 0.5994 · -1.3%');
    // once the EMA trail has carried the stop above the entry the engine's risk % is negative: profit locked in, said as such
    const trailed = flagView(flagResult({ ...long, levels: { ...long.levels, stop: 0.7908 }, evidence: { ...long.evidence, riskPct: -22.1, openPnlPct: 44 } }));
    expect(trailed.plan.map((p) => [p.k, p.v])).toContainEqual(['Locked in', '+22.1%']);
    expect(trailed.plan.find((p) => p.k === 'Risk %')).toBeUndefined();
  });

  it('idle and impulse draw nothing and carry no plan; their badges are IDLE and BREAKOUT', () => {
    for (const [state, label] of [['idle', 'IDLE'], ['impulse', 'BREAKOUT']] as const) {
      const v = flagView(flagResult({ state }));
      expect(v.stateLabel).toBe(label);
      expect(v.lines).toEqual([]);
      expect(v.plan).toEqual([]);
      expect(v.planText).toBe('');
    }
  });

  // FLAG EXPIRY (emaFlag.ts, maxFlagBars): after its flag expires the engine's state stays 'impulse' (e9State 1, off the
  // sideways/ready scans) but the headline turns into 'RANGE — no flag: …'. The strip prints badge + headline side by
  // side, so a 'BREAKOUT' badge in front of it read 'BREAKOUT RANGE — no flag…' for six hours on PTC (2026-10-05).
  describe('the badge agrees with the headline once the top has expired', () => {
    const MIN = 60_000;
    const ptc = (ptcFixture.bars as Bar[]).map(toCandle);
    const ptcAt = (k: number) => evaluateFlag({ candles: ptc.slice(0, k), tfMs: MIN, nowMs: ptc[k - 1]!.ts + 10 * MIN });

    it("PTC at the end of the day: state impulse, badge 'RANGE', headline 'RANGE — no flag …'", () => {
      const r = ptcAt(ptc.length);
      expect(r.state).toBe('impulse');
      const v = flagView(r);
      expect(v.state).toBe('impulse'); // data-state / the CSS key stays the engine's own word
      expect(v.stateLabel).toBe('RANGE');
      expect(v.headline).toMatch(/^RANGE/);
      // the strip is `${badge} ${headline}`: the badge must not contradict the headline's first word
      expect(`${v.stateLabel} ${v.headline}`).not.toMatch(/BREAKOUT/);
    });

    it("a FRESH impulse (the top is young) still badges 'BREAKOUT' with the 'BREAKOUT — wait …' headline", () => {
      // The first bar of PTC's day-long run where the engine is in an impulse whose top is not yet 30 bars old.
      let fresh: FlagResult | null = null;
      for (let k = 50; k <= ptc.length && !fresh; k++) {
        const r = ptcAt(k);
        if (r.state === 'impulse' && r.headline.startsWith('BREAKOUT')) fresh = r;
      }
      expect(fresh).not.toBeNull();
      const v = flagView(fresh!);
      expect(v.stateLabel).toBe('BREAKOUT');
      expect(v.headline).toMatch(/^BREAKOUT/);
    });

    it('the label follows the engine flag, not the headline text', () => {
      expect(flagView(flagResult({ state: 'impulse', topExpired: true })).stateLabel).toBe('RANGE');
      expect(flagView(flagResult({ state: 'impulse', topExpired: false })).stateLabel).toBe('BREAKOUT');
      // only an impulse can be a range: no other state is relabelled
      expect(flagView(flagResult({ state: 'idle', topExpired: true })).stateLabel).toBe('IDLE');
    });
  });

  it('missing numbers read as dashes, never NaN or undefined', () => {
    const v = flagView(flagResult({ state: 'flat', levels: { ...FBASE.levels, trigger: 0.9 } }));
    expect(v.plan.find((p) => p.k === 'Stop')?.v).toBe('—');
    expect(v.plan.find((p) => p.k === 'Flag range')?.v).toBe('—');
    expect(v.planText).toBe('Long > 0.9000'); // no stop part when there is no stop
    expect(JSON.stringify(v)).not.toMatch(/NaN|undefined/);
  });
});

describe('soundForEvent — the 9 EMA flag labels', () => {
  const snd = (kind: FlagEvent['kind'], label: string) => soundForEvent(fev({ kind, label }));

  it("FLAT is the new two-note 'flat' tone, 9 EMA NEAR a ping, LONG (FLAG) the ascending long phrase, ADD (FLAG) the single add note, EXIT the bell", () => {
    expect(snd('flat', 'FLAT')).toBe('flat');
    expect(snd('emaNear', '9 EMA NEAR')).toBe('ping');
    expect(snd('entry', 'LONG (FLAG)')).toBe('long');
    expect(snd('add', 'ADD (FLAG)')).toBe('add');
    expect(snd('exit', 'EXIT')).toBe('bell');
  });

  it('FLAG FAILED, BREAKOUT and EARLY BREAK are silent', () => {
    expect(snd('failed', 'FLAG FAILED')).toBeNull();
    expect(snd('impulse', 'BREAKOUT')).toBeNull();
    expect(snd('earlyBreak', 'EARLY BREAK')).toBeNull();
  });

  it("the flag's silent BREAKOUT (kind 'impulse') does not collide with the pullback long side's BREAKOUT confirmation (kind 'breakout' -> the 'add' note)", () => {
    expect(soundForEvent(ev({ kind: 'breakout', label: 'BREAKOUT', side: 'long', marker: 'buy' }))).toBe('add');
    expect(soundForEvent(fev({ kind: 'impulse', label: 'BREAKOUT' }))).toBeNull();
  });

  it('every flag event is on side long, so a pullback cell showing only the short side would gate them out — which is why a flag cell reports side both', () => {
    const e = fev({ kind: 'entry', label: 'LONG (FLAG)', marker: 'buy' });
    expect(eventOnSide(e, 'both')).toBe(true);
    expect(eventOnSide(e, 'long')).toBe(true);
    expect(eventOnSide(e, 'short')).toBe(false);
  });
});

describe('voicePhrase — the 9 EMA flag lines (symbol first, numbers from the engine data)', () => {
  const entryEv = fev({ ts: 100, kind: 'entry', label: 'LONG (FLAG)', price: 0.6477, marker: 'buy', note: 'broke the flag high …' });
  const r = flagResult({
    state: 'ready',
    levels: { ...FBASE.levels, flagLow: 0.6055, flagHigh: 0.6458, trigger: 0.6477, stop: 0.5994 },
    evidence: { ...FBASE.evidence, emaDistPct: 3.3 },
    events: [entryEv],
  });
  const say = (e: FlagEvent, res: FlagResult = r) => voicePhrase('BEAT', e, res);

  it('FLAT: the sideways flag range and how far the EMA sits below', () => {
    expect(say(fev({ kind: 'flat', label: 'FLAT', price: 0.6477 }))).toBe('BEAT: going sideways above the 9 EMA, 0.6055 to 0.6458, EMA 3.3% below.');
  });

  it("FLAT falls back to the flag's box when the live flag levels are gone (the flag already broke), and drops the range when neither exists", () => {
    const gone = flagResult({
      levels: { ...FBASE.levels, flagLow: null, flagHigh: null },
      evidence: { ...FBASE.evidence, emaDistPct: 3.3 },
      boxes: [{ startTs: 50, endTs: 150, high: 0.6458, low: 0.6055, trigger: 0.6477, readyTs: null, outcome: 'broke' }],
    });
    expect(say(fev({ ts: 100, kind: 'flat', label: 'FLAT', price: 0.6477 }), gone)).toBe('BEAT: going sideways above the 9 EMA, 0.6055 to 0.6458, EMA 3.3% below.');
    expect(say(fev({ ts: 100, kind: 'flat', label: 'FLAT', price: 0.6477 }), flagResult({ evidence: { ...FBASE.evidence, emaDistPct: 3.3 } }))).toBe(
      'BEAT: going sideways above the 9 EMA, EMA 3.3% below.',
    );
  });

  it("9 EMA NEAR: the distance and the trigger (the event's own price)", () => {
    expect(say(fev({ kind: 'emaNear', label: '9 EMA NEAR', price: 0.86258 }), flagResult({ evidence: { ...FBASE.evidence, emaDistPct: 3.9 } }))).toBe(
      'BEAT: 9 EMA caught up, 3.9% below. Long above 0.8626.',
    );
  });

  it("FLAT and 9 EMA NEAR quote the event bar's own EMA distance (the engine stamps it on the event); the result's live distance is only the fallback", () => {
    // the result's live distance is a later (forming) bar's; the event's own wins
    const live = flagResult({ evidence: { ...FBASE.evidence, emaDistPct: 2.7 } });
    expect(say(fev({ kind: 'flat', label: 'FLAT', price: 0.6477, emaDistPct: 3.3 }), live)).toBe('BEAT: going sideways above the 9 EMA, EMA 3.3% below.');
    expect(say(fev({ kind: 'emaNear', label: '9 EMA NEAR', price: 0.86258, emaDistPct: 3.9 }), live)).toBe('BEAT: 9 EMA caught up, 3.9% below. Long above 0.8626.');
    // a close under the EMA keeps its sign on the NEAR line, as the FLAT line already does
    expect(say(fev({ kind: 'emaNear', label: '9 EMA NEAR', price: 0.86258, emaDistPct: -0.5 }), live)).toBe('BEAT: 9 EMA caught up, 0.5% above. Long above 0.8626.');
    expect(say(fev({ kind: 'flat', label: 'FLAT', price: 0.6477, emaDistPct: -0.8 }), live)).toBe('BEAT: going sideways above the 9 EMA, EMA 0.8% above.');
  });

  it('on the real BEAT bars, read on the FIRST tick of the next bar (the live path), the FLAT / NEAR voice still says the event bar\'s distance', () => {
    const all = (beatFixture.bars as Bar[]).map(toCandle);
    // The bar after the event bar printing its first trade: open = high = low = close.
    const firstTick = (hhmm: string) => {
      const i = all.findIndex((c) => etDayKey(c.ts) === '2026-10-05' && fmtEt(c.ts) === hhmm);
      const b = all[i];
      return evaluateFlag({ candles: [...all.slice(0, i), { ...b, high: b.open, low: b.open, close: b.open, volume: 100 }], tfMs: 60_000, nowMs: b.ts + 1000 });
    };
    const voiceOf = (res: FlagResult, label: string, hhmm: string) => voicePhrase('BEAT', res.events.find((e) => e.label === label && fmtEt(e.ts) === hhmm)!, res);
    expect(voiceOf(firstTick('09:00'), 'FLAT', '08:59')).toBe('BEAT: going sideways above the 9 EMA, 0.6055 to 0.6458, EMA 3.3% below.'); // the forming 09:00 bar alone would say 2.7%
    expect(voiceOf(firstTick('09:08'), '9 EMA NEAR', '09:07')).toBe('BEAT: 9 EMA caught up, 3.9% below. Long above 0.8626.');
  });

  it('LONG (FLAG) reads the entry price and the stop; ADD (FLAG) just the price', () => {
    expect(say(entryEv)).toBe('BEAT: long, flag breakout, 0.6477. Stop 0.5994.');
    expect(say(entryEv, flagResult({ levels: { ...FBASE.levels, stop: null } }))).toBe('BEAT: long, flag breakout, 0.6477.');
    expect(say(fev({ kind: 'add', label: 'ADD (FLAG)', price: 0.8626, marker: 'buy' }))).toBe('BEAT: add, flag breakout, 0.8626.');
  });

  it('EXIT reads the exit price and the P&L against the position\'s FIRST entry event (the exit\'s own levels are already cleared)', () => {
    const exit = fev({ ts: 200, kind: 'exit', label: 'EXIT', price: 0.8016, marker: 'sell', note: 'closed below the 9 EMA trail 0.8200 — exit at 0.8016, +23.8% from 0.6477' });
    const res = flagResult({
      state: 'idle',
      events: [entryEv, fev({ ts: 150, kind: 'add', label: 'ADD (FLAG)', price: 0.86, marker: 'buy' }), exit],
    });
    expect(say(exit, res)).toBe('BEAT: exit, closed below the 9 EMA, 0.8016, plus 23.8 percent.');
    // a loser says minus; a by-low exit (stopByClose off) says stop hit rather than closed below
    const loss = fev({ ts: 300, kind: 'exit', label: 'EXIT', price: 0.6, marker: 'sell', note: 'traded down to the stop 0.6000 — exit at 0.6000, -7.4% from 0.6477' });
    expect(say(loss, flagResult({ state: 'idle', events: [entryEv, loss] }))).toBe('BEAT: exit, stop hit, 0.6000, minus 7.4 percent.');
    // no matching entry in the events -> just the price
    expect(say(exit, flagResult({ state: 'idle', events: [exit] }))).toBe('BEAT: exit, closed below the 9 EMA, 0.8016.');
  });

  it('EXIT wording follows the engine note: a trail close, an initial-stop close and a by-low touch are three different things', () => {
    const mk = (note: string, price: number) => fev({ ts: 200, kind: 'exit', label: 'EXIT', price, marker: 'sell', note });
    const a = mk('closed below the stop 0.5994 — exit at 0.5950, -8.1% from 0.6477', 0.595);
    // the untrailed initial stop can be hit while the close is still ABOVE the EMA: never "closed below the 9 EMA"
    expect(say(a, flagResult({ state: 'idle', events: [entryEv, a] }))).toBe('BEAT: exit, closed below the stop, 0.5950, minus 8.1 percent.');
    const b = mk('traded down to the 9 EMA trail 0.6100 — exit at 0.6100, -5.8% from 0.6477', 0.61);
    expect(say(b, flagResult({ state: 'idle', events: [entryEv, b] }))).toBe('BEAT: exit, stop hit, 0.6100, minus 5.8 percent.');
    const c = mk('closed below the 9 EMA trail 0.8246 — exit at 0.8016, +23.8% from 0.6477', 0.8016);
    expect(say(c, flagResult({ state: 'idle', events: [entryEv, c] }))).toBe('BEAT: exit, closed below the 9 EMA, 0.8016, plus 23.8 percent.');
  });

  it('FLAG FAILED is spoken only when no position was open', () => {
    const failed = fev({ ts: 400, kind: 'failed', label: 'FLAG FAILED', price: 0.79, note: 'closed 3.0% under the 9 EMA — flag failed' });
    expect(say(failed, flagResult({ state: 'idle', events: [failed] }))).toBe('BEAT: flag failed, lost the 9 EMA.');
    // while long, or on the very bar the exit fired, the exit is the thing to hear
    expect(say(failed, flagResult({ state: 'long', events: [failed] }))).toBeNull();
    const exit = fev({ ts: 400, kind: 'exit', label: 'EXIT', price: 0.79, marker: 'sell', note: 'closed below the 9 EMA trail' });
    expect(say(failed, flagResult({ state: 'idle', events: [exit, failed] }))).toBeNull();
  });

  it('BREAKOUT and EARLY BREAK are never spoken', () => {
    expect(say(fev({ kind: 'impulse', label: 'BREAKOUT', price: 0.6252 }))).toBeNull();
    expect(say(fev({ kind: 'earlyBreak', label: 'EARLY BREAK', price: 0.6477 }))).toBeNull();
  });

  it("the pullback's own phrases are untouched by the flag branch (levels in, as ever)", () => {
    const lv = { ...BASE.levels, stop: 0.674 };
    expect(voicePhrase('ATYR', ev({ kind: 'entryLH', label: 'SHORT (LH)', side: 'short', price: 0.603, marker: 'sell' }), lv)).toBe('ATYR: short. Lower high. 0.6030. Stop 0.6740.');
  });
});

// The REAL 2026-10-05 BEAT minute bars through the live engine, one closed bar
// at a time, the way the Signal window sees them: every event the first time it
// appears gets its tone and its line. Asserts the cues' SHAPE for each event
// kind (not the engine's rule outcomes — emaFlag.test.ts pins those) and logs
// the cue sheet once for the orchestrator.
describe('flag sounds and voice on the real BEAT 1-minute bars (2026-10-05)', () => {
  const candles = (beatFixture.bars as Bar[]).map(toCandle);

  function replay() {
    const seen = new Set<string>();
    const cues: { time: string; kind: string; label: string; price: number; sound: string | null; phrase: string | null }[] = [];
    for (let i = 20; i < candles.length; i++) {
      const res = evaluateFlag({ candles: candles.slice(0, i + 1), tfMs: 60_000, nowMs: candles[i].ts + 60_000 });
      for (const e of res.events) {
        const key = `${e.ts}:${e.kind}:${e.label}`;
        if (seen.has(key)) continue;
        seen.add(key);
        cues.push({ time: fmtEt(e.ts), kind: e.kind, label: e.label, price: e.price, sound: soundForEvent(e), phrase: voicePhrase('BEAT', e, res) });
      }
    }
    return cues;
  }

  it('each event kind earns exactly the cue the brief lists', () => {
    const cues = replay();
    expect(cues.length).toBeGreaterThan(0);
    // eslint-disable-next-line no-console
    console.log('BEAT cue sheet (time kind label price | tone | voice):\n' + cues.map((c) => `${c.time} ${c.kind} ${c.label} ${c.price.toFixed(4)} | ${c.sound ?? '-'} | ${c.phrase ?? '(silent)'}`).join('\n'));

    for (const c of cues) {
      switch (c.kind) {
        case 'impulse':
        case 'earlyBreak':
          expect([c.sound, c.phrase], `${c.time} ${c.label}`).toEqual([null, null]);
          break;
        case 'flat':
          expect(c.sound).toBe('flat');
          expect(c.phrase).toMatch(/^BEAT: going sideways above the 9 EMA, \d\.\d{4} to \d\.\d{4}, EMA \d+\.\d% below\.$/);
          break;
        case 'emaNear':
          expect(c.sound).toBe('ping');
          expect(c.phrase).toMatch(/^BEAT: 9 EMA caught up, \d+\.\d% below\. Long above \d\.\d{4}\.$/);
          break;
        case 'entry':
          expect(c.sound).toBe('long');
          expect(c.phrase).toMatch(/^BEAT: long, flag breakout, \d\.\d{4}\. Stop \d\.\d{4}\.$/);
          break;
        case 'add':
          expect(c.sound).toBe('add');
          expect(c.phrase).toMatch(/^BEAT: add, flag breakout, \d\.\d{4}\.$/);
          break;
        case 'exit':
          expect(c.sound).toBe('bell');
          expect(c.phrase).toMatch(/^BEAT: exit, closed below the 9 EMA, \d\.\d{4}, (plus|minus) \d+\.\d percent\.$/);
          break;
        case 'failed':
          expect(c.sound).toBeNull();
          // spoken only when no position was open on that bar
          expect(c.phrase === null || c.phrase === 'BEAT: flag failed, lost the 9 EMA.').toBe(true);
          break;
        case 'slam':
          expect([c.sound, c.phrase]).toEqual(['slam', 'BEAT: nine E M A slam']);
          break;
        default:
          throw new Error(`unexpected flag event kind ${c.kind}`);
      }
    }
    // the day's story is present: it went sideways, got a long and got out
    const kinds = new Set(cues.map((c) => c.kind));
    for (const k of ['flat', 'entry', 'exit']) expect(kinds.has(k), k).toBe(true);
  });

  it("the entry's spoken stop and price are the engine's own (entry.price and levels.stop on the bar it printed)", () => {
    for (let i = 20; i < candles.length; i++) {
      const res = evaluateFlag({ candles: candles.slice(0, i + 1), tfMs: 60_000, nowMs: candles[i].ts + 60_000 });
      const entry = res.events.find((e) => e.kind === 'entry' && e.ts === candles[i].ts);
      if (!entry) continue;
      const fp = (v: number) => (Math.abs(v) < 1 ? v.toFixed(4) : v.toFixed(2));
      expect(voicePhrase('BEAT', entry, res)).toBe(`BEAT: long, flag breakout, ${fp(entry.price)}. Stop ${fp(res.levels.stop!)}.`);
      return;
    }
    throw new Error('no entry found on BEAT');
  });

  it("flagView on the live BEAT result: while a flag waits there is a 'Long > T' line, while long a 'Stop S' line — never both", () => {
    let sawWaiting = false;
    let sawLong = false;
    for (let i = 20; i < candles.length; i++) {
      const res = evaluateFlag({ candles: candles.slice(0, i + 1), tfMs: 60_000, nowMs: candles[i].ts + 60_000 });
      const v = flagView(res);
      const labels = v.lines.map((l) => l.label.split(' ')[0]);
      if (res.state === 'flat' || res.state === 'ready') {
        sawWaiting = true;
        expect(labels).toEqual(['Long']);
        expect(v.lines[0].tone).toBe('entry');
        expect(v.planText.startsWith('Long > ')).toBe(true);
      } else if (res.state === 'long') {
        sawLong = true;
        expect(labels).toEqual(['Stop']);
        expect(v.lines[0].tone).toBe('stop');
        expect(v.planText.startsWith('Long ')).toBe(true);
      } else {
        expect(v.lines).toEqual([]);
        expect(v.planText).toBe('');
      }
      expect(JSON.stringify(v)).not.toMatch(/NaN|undefined/);
    }
    expect(sawWaiting && sawLong).toBe(true);
  });
});

// ─── 9 EMA SLAM (2026-10-07): the one bearish call the flag engine makes ───
//
// The engine (emaFlag.ts) emits it as kind 'slam', label 'SLAM (9 EMA)', side
// 'short', price = the slam bar's close, the bar's own EMA distance on
// `emaDistPct`, and NO marker: it is an alert, the Signal window draws its own.
// These pin the window's half: the red ▼ on the chart (flagView + chartMarkers),
// the falling 'slam' tone, the spoken line, and that a flag cell (side 'both')
// lets it through the sound gate like every other flag event.
describe('9 EMA SLAM in the Signal window', () => {
  const slamEv = fev({
    ts: 900,
    kind: 'slam',
    label: 'SLAM (9 EMA)',
    side: 'short',
    price: 4.566,
    emaDistPct: -4.32,
    note: '08:15 slammed through the 9 EMA from above: closed 4.57, 4.3% under the EMA 4.77 — a 1.7 ATR body on 1.6× volume',
  });
  const failedEv = fev({ ts: 900, kind: 'failed', label: 'FLAG FAILED', price: 4.566, note: 'closed 4.3% under the 9 EMA — flag failed' });
  const exitEv = fev({ ts: 900, kind: 'exit', label: 'EXIT', price: 4.566, marker: 'sell', note: 'closed below the 9 EMA trail 4.7300 — exit at 4.5660, -6.0% from 4.8600' });
  const entryEv = fev({ ts: 600, kind: 'entry', label: 'LONG (FLAG)', price: 4.86, marker: 'buy', note: 'broke the flag high …' });

  it("sounds the new falling 'slam' tone (by label, like every flag event)", () => {
    expect(soundForEvent(slamEv)).toBe('slam');
    // the FLAG FAILED it shares its bar with stays silent, the EXIT keeps its bell
    expect(soundForEvent(failedEv)).toBeNull();
    expect(soundForEvent(exitEv)).toBe('bell');
  });

  it("says '<SYM>: nine E M A slam' — the same line the 9EMA Slam scan's alert speaks", () => {
    expect(voicePhrase('SPAI', slamEv, flagResult({ state: 'idle', events: [failedEv, slamEv] }))).toBe('SPAI: nine E M A slam');
    // whatever state the result is in (a long slammed out of its position, too)
    expect(voicePhrase('SPAI', slamEv, flagResult({ state: 'long', events: [entryEv, exitEv, slamEv] }))).toBe('SPAI: nine E M A slam');
  });

  it("the bar's FLAG FAILED is not spoken on top of the slam (the slam says it louder); a FLAG FAILED on its own still is", () => {
    expect(voicePhrase('SPAI', failedEv, flagResult({ state: 'idle', events: [failedEv, slamEv] }))).toBeNull();
    const earlier = fev({ ts: 500, kind: 'failed', label: 'FLAG FAILED', price: 4.4, note: 'closed 3.0% under the 9 EMA — flag failed' });
    expect(voicePhrase('SPAI', earlier, flagResult({ state: 'idle', events: [earlier, failedEv, slamEv] }))).toBe('SPAI: flag failed, lost the 9 EMA.');
    // the EXIT on a slammed long is still read out (it is the trade's own line)
    expect(voicePhrase('SPAI', exitEv, flagResult({ state: 'idle', events: [entryEv, exitEv, slamEv] }))).toBe(
      'SPAI: exit, closed below the 9 EMA, 4.57, minus 6.0 percent.',
    );
  });

  it("a flag cell reports side 'both', so its short-side slam passes the sound gate (mute and voice then gate it like the rest)", () => {
    expect(eventOnSide(slamEv, 'both')).toBe(true);
    // a pullback cell narrowed to its long side would gate it — but the pullback engine never emits one
    expect(eventOnSide(slamEv, 'long')).toBe(false);
  });

  it("flagView hands the chart a red ▼ for the slam: the view's copy carries marker 'sell', the engine's own event is untouched", () => {
    const res = flagResult({ state: 'idle', events: [entryEv, failedEv, slamEv] });
    const v = flagView(res);
    expect(v.events).toHaveLength(3);
    expect(v.events[0]).toBe(entryEv); // the others are the very same objects
    expect(v.events[1]).toBe(failedEv);
    expect(v.events[2]).toEqual({ ...slamEv, marker: 'sell' });
    expect(slamEv.marker).toBeUndefined(); // the raw result (the sound diff's input) is not mutated
    expect(res.events[2]).toBe(slamEv);
  });

  it("without a slam flagView still hands the engine's own events array through (identity-keyed chart effects)", () => {
    const res = flagResult({ state: 'idle', events: [entryEv, failedEv] });
    expect(flagView(res).events).toBe(res.events);
  });

  it("chartMarkers: every marker event as before, the slam as a red ▼ at its close labelled 'SLAM (9 EMA)'", () => {
    const v = flagView(flagResult({ state: 'idle', events: [entryEv, failedEv, slamEv] }));
    expect(chartMarkers(v.events)).toEqual([
      { ts: 600, price: 4.86, side: 'buy', label: 'LONG (FLAG)' },
      { ts: 900, price: 4.566, side: 'sell', label: 'SLAM (9 EMA)' },
    ]);
  });

  it('a slam on the bar of an EXIT joins that ▼ (one arrow, both names) instead of stacking a second pill on the same price', () => {
    const v = flagView(flagResult({ state: 'idle', events: [entryEv, exitEv, slamEv] }));
    expect(chartMarkers(v.events)).toEqual([
      { ts: 600, price: 4.86, side: 'buy', label: 'LONG (FLAG)' },
      { ts: 900, price: 4.566, side: 'sell', label: 'EXIT · SLAM' },
    ]);
  });

  it("chartMarkers leaves the pullback's markers exactly as SignalChart drew them (two markers on one bar stay two)", () => {
    expect(chartMarkers(EVENTS)).toEqual(EVENTS.filter((e) => e.marker).map((e) => ({ ts: e.ts, price: e.price, side: e.marker, label: e.label })));
    expect(chartMarkers([])).toEqual([]);
  });

  describe("on the real SPAI bars (2026-10-07, the trader's chart)", () => {
    const candles = (spaiFixture.bars as Bar[]).map(toCandle);
    const MIN = 60_000;

    // Bar by bar, the way the Signal window sees them: each event the first time
    // it appears, with the tone and the line it earns.
    function replay() {
      const seen = new Set<string>();
      const cues: { day: string; time: string; kind: string; sound: string | null; phrase: string | null }[] = [];
      let last: FlagResult | null = null;
      for (let i = 20; i < candles.length; i++) {
        const res = evaluateFlag({ candles: candles.slice(0, i + 1), tfMs: MIN, nowMs: candles[i].ts + MIN });
        last = res;
        for (const e of res.events) {
          const key = `${e.ts}:${e.kind}:${e.label}`;
          if (seen.has(key)) continue;
          seen.add(key);
          cues.push({ day: etDayKey(e.ts), time: fmtEt(e.ts), kind: e.kind, sound: soundForEvent(e), phrase: voicePhrase('SPAI', e, res) });
        }
      }
      return { cues, last: last! };
    }

    it("08:15 earns the 'slam' tone and 'SPAI: nine E M A slam' — once — and that bar's FLAG FAILED stays quiet", () => {
      const cues = replay().cues.filter((c) => c.day === '2026-10-07');
      const slams = cues.filter((c) => c.kind === 'slam');
      expect(slams.map((c) => [c.time, c.sound, c.phrase])).toEqual([['08:15', 'slam', 'SPAI: nine E M A slam']]);
      const failed815 = cues.find((c) => c.kind === 'failed' && c.time === '08:15');
      expect(failed815).toBeDefined();
      expect([failed815!.sound, failed815!.phrase]).toEqual([null, null]);
      // 08:16 kept falling but opened under the EMA: no second slam, no second line
      expect(cues.filter((c) => c.time === '08:16' && c.sound === 'slam')).toEqual([]);
    });

    it('the chart draws one red ▼ at the 08:15 close, labelled SLAM (9 EMA)', () => {
      const { last } = replay();
      const marks = chartMarkers(flagView(last).events).filter((m) => /SLAM/.test(m.label));
      expect(marks).toHaveLength(1);
      expect(fmtEt(marks[0].ts)).toBe('08:15');
      expect(marks[0].side).toBe('sell');
      expect(marks[0].price).toBeCloseTo(4.566, 6);
      expect(marks[0].label).toBe('SLAM (9 EMA)');
    });
  });
});

// ─── GREEN DILDO / VOLUME CLIMAX in a 1-minute flag cell (2026-10-07) ─────

describe('dildo and climax events in the Signal window: markers, merge, silence', () => {
  const flagEv = (over: Partial<FlagEvent> & Pick<FlagEvent, 'ts' | 'kind' | 'label'>): FlagEvent => ({ price: 1, note: '', side: 'long', ...over });
  const dEv = (over: Partial<DildoEvent> & Pick<DildoEvent, 'ts' | 'kind' | 'label'>): DildoEvent => ({ price: 1, note: '', side: 'long', ...over });
  const cEv = (over: Partial<ClimaxEvent> & Pick<ClimaxEvent, 'ts' | 'kind' | 'label'>): ClimaxEvent => ({ price: 1, note: '', side: 'long', ...over });

  it('mergeViewEvents keeps identity with nothing to merge, and sorts stably (flag, then dildo, then climax on a tie)', () => {
    const base: FlagEvent[] = [flagEv({ ts: 1, kind: 'flat', label: 'FLAT' }), flagEv({ ts: 3, kind: 'entry', label: 'LONG (FLAG)', marker: 'buy' })];
    expect(mergeViewEvents(base)).toBe(base);
    expect(mergeViewEvents(base, [], [])).toBe(base);
    const d = [dEv({ ts: 3, kind: 'dildoPb', label: '9 EMA PULLBACK', marker: 'buy' }), dEv({ ts: 2, kind: 'dildo', label: 'GREEN DILDO', marker: 'buy' })];
    const c = [cEv({ ts: 3, kind: 'bottom', label: 'BOTTOM CONFIRMED', marker: 'buy' }), cEv({ ts: 0, kind: 'climax', label: 'VOLUME CLIMAX', marker: 'buy' })];
    expect(mergeViewEvents(base, d, c).map((e) => `${e.ts}:${e.kind}`)).toEqual(['0:climax', '1:flat', '2:dildo', '3:entry', '3:dildoPb', '3:bottom']);
  });

  it('draws ▲ DILDO / ▲ 9 EMA PB at the closes, ▲ CLIMAX at the low and ▲ BOTTOM at the close; cancels and fails draw nothing', () => {
    const marks = chartMarkers([
      dEv({ ts: 10, kind: 'dildo', label: 'GREEN DILDO', price: 1.2, marker: 'buy' }),
      dEv({ ts: 20, kind: 'dildoPb', label: '9 EMA PULLBACK', price: 1.18, marker: 'buy' }),
      dEv({ ts: 30, kind: 'dildoCancel', label: 'PB CANCELLED', price: 1.09, reason: 'floor' }),
      cEv({ ts: 40, kind: 'climax', label: 'VOLUME CLIMAX', price: 9.0, marker: 'buy' }),
      cEv({ ts: 50, kind: 'bottom', label: 'BOTTOM CONFIRMED', price: 9.35, marker: 'buy' }),
      cEv({ ts: 60, kind: 'climaxFail', label: 'CLIMAX FAILED', price: 8.9, reason: 'low' }),
    ]);
    expect(marks).toEqual([
      { ts: 10, price: 1.2, side: 'buy', label: 'DILDO' },
      { ts: 20, price: 1.18, side: 'buy', label: '9 EMA PB' },
      { ts: 40, price: 9.0, side: 'buy', label: 'CLIMAX' },
      { ts: 50, price: 9.35, side: 'buy', label: 'BOTTOM' },
    ]);
  });

  it('a pullback or a bottom on the bar of a flag LONG joins it (the SLAM merge rule)', () => {
    const base: FlagEvent[] = [flagEv({ ts: 5, kind: 'entry', label: 'LONG (FLAG)', price: 2, marker: 'buy' })];
    const merged = mergeViewEvents(base, [dEv({ ts: 5, kind: 'dildoPb', label: '9 EMA PULLBACK', price: 1.99, marker: 'buy' })], [cEv({ ts: 5, kind: 'bottom', label: 'BOTTOM CONFIRMED', price: 1.98, marker: 'buy' })]);
    expect(chartMarkers(merged)).toEqual([{ ts: 5, price: 2, side: 'buy', label: 'LONG (FLAG) · 9 EMA PB · BOTTOM' }]);
    // a dildo and a climax on one bar: one ▲, 'DILDO · CLIMAX'
    expect(chartMarkers(mergeViewEvents([], [dEv({ ts: 7, kind: 'dildo', label: 'GREEN DILDO', marker: 'buy' })], [cEv({ ts: 7, kind: 'climax', label: 'VOLUME CLIMAX', marker: 'buy' })])).map((m) => m.label)).toEqual(['DILDO · CLIMAX']);
    // an EXIT ▼ on the same bar is a different side: no join
    const exit = [flagEv({ ts: 9, kind: 'exit', label: 'EXIT', marker: 'sell' })];
    expect(chartMarkers(mergeViewEvents(exit, [dEv({ ts: 9, kind: 'dildo', label: 'GREEN DILDO', marker: 'buy' })])).map((m) => m.label)).toEqual(['EXIT', 'DILDO']);
  });

  it('every dildo and climax kind is silent: no tone, no spoken line, in a flag cell or a pullback cell', () => {
    const flag = evaluateFlag({ candles: [], tfMs: 60_000, nowMs: 0 });
    const events: (DildoEvent | ClimaxEvent)[] = [
      dEv({ ts: 1, kind: 'dildo', label: 'GREEN DILDO', marker: 'buy' }),
      dEv({ ts: 1, kind: 'dildoPb', label: '9 EMA PULLBACK', marker: 'buy', emaDistPct: 1.2 }),
      dEv({ ts: 1, kind: 'dildoCancel', label: 'PB CANCELLED', reason: 'ema' }),
      cEv({ ts: 1, kind: 'climax', label: 'VOLUME CLIMAX', marker: 'buy' }),
      cEv({ ts: 1, kind: 'bottom', label: 'BOTTOM CONFIRMED', marker: 'buy' }),
      cEv({ ts: 1, kind: 'climaxFail', label: 'CLIMAX FAILED', reason: 'low' }),
    ];
    for (const e of events) {
      expect(soundForEvent(e), e.kind).toBeNull();
      expect(voicePhrase('ABC', e, flag), e.kind).toBeNull();
      expect(voicePhrase('ABC', e, BASE.levels), e.kind).toBeNull();
    }
  });

  it('on real bars: MB 2026-08-07 (dildo 16:51 -> 9 EMA PB 17:01) and BIYA 2026-10-07 (climax 11:03 -> bottom 11:04)', async () => {
    const read = async (path: string) => ((await import(/* @vite-ignore */ path)) as { default: { bars: Bar[] } }).default.bars.map(toCandle);
    const mb = await read('./fixtures/dildo/dildo-mb-2026-08-07.json');
    const nowMb = mb[mb.length - 1]!.ts + 600_000;
    const flagMb = evaluateFlag({ candles: mb, tfMs: 60_000, nowMs: nowMb });
    const dMb = evaluateDildo({ candles: mb, tfMs: 60_000, nowMs: nowMb });
    const marks = chartMarkers(mergeViewEvents(flagView(flagMb).events, dMb.events)).filter((m) => /DILDO|9 EMA PB/.test(m.label));
    expect(marks.filter((m) => etDayKey(m.ts) === '2026-08-07').map((m) => `${fmtEt(m.ts)} ${m.label}`)).toContain('17:01 9 EMA PB');
    const biya = await read('./fixtures/climax/climax-biya-2026-10-07.json');
    const nowB = biya[biya.length - 1]!.ts + 600_000;
    const cB = evaluateClimax({ candles: biya, tfMs: 60_000, nowMs: nowB });
    const flagB = evaluateFlag({ candles: biya, tfMs: 60_000, nowMs: nowB });
    const bm = chartMarkers(mergeViewEvents(flagView(flagB).events, [], cB.events)).filter((m) => /CLIMAX|BOTTOM/.test(m.label));
    expect(bm.map((m) => `${fmtEt(m.ts)} ${m.label} ${m.price}`)).toEqual(['11:03 CLIMAX 1.78', '11:04 BOTTOM 1.879']);
  });
});
