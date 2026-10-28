// Pins the short-fade engine's state machine against hand-built candle
// fixtures — no chart, no store, no WS in the loop (node env, see
// review.derive.test.ts for the sibling style this follows: one `describe`
// per scenario, a tiny row/bar factory, comments saying what real behaviour
// each test pins down).
import { describe, expect, it } from 'vitest';
import { DEFAULT_PARAMS, evaluate, fmtEt, fmtPx, type SignalParams, type SignalEvent } from './shortFade';
import { etDayKey } from '../marketClock';
import { toCandle } from '../chartAgg';
import type { Candle } from '../vendor/blackthorn-charts';
import type { Bar } from '../types';
import atyrFixture from './fixtures/atyr-2026-09-24.json';
import gctkFixture from './fixtures/gctk-2026-09-24.json';
import wetoFixture from './fixtures/weto-2026-09-24.json';
import sphlFixture from './fixtures/sphl-2026-09-24.json';
import dcxFixture from './fixtures/dcx-2026-09-24.json';

// Anchored at a fixed ET morning per the brief (2026-09-24 04:00 ET was the
// brief's own example ms value; etDayKey says what calendar date that ms
// value actually lands on — asserted below rather than assumed, and every
// other test derives its expected dayKey from etDayKey too, never from a
// hand-typed date string).
const ANCHOR = 1758700800000;

function bar(minuteIdx: number, o: number, h: number, l: number, c: number, v: number, dayOffsetMs = 0): Candle {
  return { ts: ANCHOR + dayOffsetMs + minuteIdx * 60_000, open: o, high: h, low: l, close: c, volume: v };
}

function kinds(events: { kind: string }[]): string[] {
  return events.map((e) => e.kind);
}

// ─── The append-only contract, as the engine actually guarantees it ──────
//
// Feeding the engine one bar at a time — the last bar FORMING, then the same
// bar CLOSED — what may change between the two evaluations? Two things hold,
// and the replay harness below (T14 / T25 / T30) asserts exactly those:
//   1. Closed history is a STRICT prefix: everything emitted for the bars
//      that were already closed is byte-identical, in the same order, no
//      matter what the newest bar does (the forming pass runs after the
//      closed loop and can only append).
//   2. The forming bar's OWN events all survive its close, as a SET keyed on
//      (ts, kind, label, side, price), with no duplicates. Their ORDER within
//      the newly closed bar may change: a swing that the close confirms is
//      emitted at the close, ahead of an intrabar breakdown/breakout/stop the
//      same bar printed (addendum B2 needs that order — ATYR's 06:23 entryLH
//      comes before its breakdown), so the closed list can interleave a
//      `lowerHigh` / `ADD (LH)` in front of an event the forming list ended
//      on. A strict "forming is a prefix of closed" check asserts more than
//      the engine promises and only held on the default params by luck.
const evKey = (e: SignalEvent): string => `${e.ts}|${e.kind}|${e.label}|${e.side}|${e.price}`;

function expectAppendOnlyReplay(all: Candle[], params: SignalParams, dayKey = '2026-09-24'): void {
  const yesterday = all.filter((c) => etDayKey(c.ts) !== dayKey);
  const today = all.filter((c) => etDayKey(c.ts) === dayKey);
  let prevClosed: SignalEvent[] = [];
  for (let i = 0; i < today.length; i++) {
    const slice = [...yesterday, ...today.slice(0, i + 1)];
    const lastTs = slice[slice.length - 1]!.ts;
    const forming = evaluate({ candles: slice, tfMs: 60_000, nowMs: lastTs + 20_000, params });
    const closed = evaluate({ candles: slice, tfMs: 60_000, nowMs: lastTs + 60_001, params });
    // (1) the already-closed history is a strict prefix of both evaluations.
    expect(forming.events.slice(0, prevClosed.length)).toEqual(prevClosed);
    expect(closed.events.slice(0, prevClosed.length)).toEqual(prevClosed);
    // (2) the forming bar's own events survive its close, once each.
    const closedKeys = closed.events.map(evKey);
    expect(new Set(closedKeys).size).toBe(closedKeys.length);
    for (const e of forming.events.slice(prevClosed.length)) expect(closedKeys).toContain(evKey(e));
    prevClosed = closed.events;
  }
}

describe('shortFade anchor', () => {
  it('ANCHOR really is the ET morning the fixtures assume', () => {
    expect(etDayKey(ANCHOR)).toBe('2025-09-24');
  });
});

describe('evaluate — ATYR-like morning (test 1)', () => {
  // The honest fixture: built bar-by-bar from the brief's §1 story (base
  // 0.38-0.40, a 10-bar run to 0.75 HOD on rising/climax volume, a 4-bar
  // drop to 0.60, a 3-bar bounce to 0.66, a break down to 0.44, a bounce to
  // 0.50 under VWAP) using the STORY's own round numbers as the actual price
  // levels — not reverse-engineered from the assertions below. Every number
  // asserted after the fixture is what DEFAULT_PARAMS' rules produce when run
  // over it, checked by actually running the engine (see the PR notes for
  // this file), not assumed.
  const bars: Candle[] = [
    // flat base 0.38-0.40, small volume
    bar(0, 0.39, 0.395, 0.385, 0.39, 2000),
    bar(1, 0.39, 0.4, 0.385, 0.395, 2200),
    bar(2, 0.395, 0.398, 0.388, 0.39, 1800),
    bar(3, 0.39, 0.395, 0.382, 0.385, 2100),
    bar(4, 0.385, 0.392, 0.38, 0.388, 1900),
    bar(5, 0.388, 0.395, 0.385, 0.392, 2000),
    bar(6, 0.392, 0.398, 0.388, 0.395, 2300),
    bar(7, 0.395, 0.4, 0.39, 0.398, 2100),
    bar(8, 0.398, 0.4, 0.392, 0.395, 1900),
    bar(9, 0.395, 0.399, 0.39, 0.397, 2000),
    // 10-bar run to the 0.75 HOD, rising volume, climax on the HOD bar
    bar(10, 0.397, 0.425, 0.395, 0.42, 3000),
    bar(11, 0.42, 0.455, 0.415, 0.45, 3500),
    bar(12, 0.45, 0.485, 0.445, 0.48, 4000),
    bar(13, 0.48, 0.525, 0.475, 0.52, 4800),
    bar(14, 0.52, 0.565, 0.515, 0.56, 5500),
    bar(15, 0.56, 0.605, 0.555, 0.6, 6200),
    bar(16, 0.6, 0.645, 0.595, 0.64, 7000),
    bar(17, 0.64, 0.685, 0.635, 0.68, 8000),
    bar(18, 0.68, 0.715, 0.675, 0.71, 9500),
    bar(19, 0.71, 0.75, 0.705, 0.745, 20000), // HOD 0.75, climax volume
    // 4-bar drop to 0.60
    bar(20, 0.745, 0.75, 0.68, 0.69, 9000),
    bar(21, 0.69, 0.695, 0.64, 0.645, 6000),
    bar(22, 0.645, 0.65, 0.615, 0.62, 5500),
    bar(23, 0.62, 0.625, 0.6, 0.605, 5000), // low touches 0.60
    // 3-bar bounce to 0.66
    bar(24, 0.605, 0.63, 0.605, 0.62, 4000),
    bar(25, 0.62, 0.645, 0.615, 0.635, 4200),
    bar(26, 0.635, 0.66, 0.625, 0.65, 4500), // lower high 0.66
    // turn down, confirming the lower high
    bar(27, 0.65, 0.655, 0.62, 0.625, 3800),
    bar(28, 0.625, 0.64, 0.6, 0.605, 3900),
    // break through 0.60 down to 0.44
    bar(29, 0.605, 0.61, 0.59, 0.595, 6000), // breaks the 0.60 pullback low
    bar(30, 0.595, 0.6, 0.55, 0.56, 7000),
    bar(31, 0.56, 0.565, 0.51, 0.52, 6500),
    bar(32, 0.52, 0.53, 0.47, 0.48, 6000),
    bar(33, 0.48, 0.485, 0.44, 0.45, 5500), // reaches 0.44
    // bounce to 0.50, still well under the session VWAP
    bar(34, 0.45, 0.48, 0.44, 0.46, 4000),
    bar(35, 0.46, 0.5, 0.455, 0.49, 4200),
    bar(36, 0.49, 0.505, 0.48, 0.495, 4000),
  ];
  const nowMs = bars[bars.length - 1]!.ts + 60_000;
  const r = evaluate({ candles: bars, tfMs: 60_000, nowMs });

  it('produces exactly spike, crack, lowerHigh, entryLH, breakdown, target(T1 VWAP) — the bounce to 0.50 stays under VWAP (~0.58, still elevated by the run), so no vwapReject fires here; that branch is exercised by the VWAP tests below instead. Iteration 2 (B2, lhEntry default true): the CONFIRMING close of the 0.66 lower high is now the entry itself (entryLH), with the old break of the 0.60 pullback low demoted to `breakdown` (confirmation/add) since the engine is already short by then — see the lhEntry:false variant right below, which pins the OLD (pre-iteration-2) `entry`-only path so both mechanisms stay covered.', () => {
    expect(kinds(r.events)).toEqual(['spike', 'crack', 'lowerHigh', 'entryLH', 'breakdown', 'target']);
    const target = r.events.find((e) => e.kind === 'target')!;
    expect(target.label).toBe('T1 VWAP');
  });

  it('entryLH shorts at the close that confirms the 0.66 lower high, breakdown then confirms the 0.60 pullback-low break, stop sits above the 0.66 lower high, runLow is the 0.38 base', () => {
    const entryLH = r.events.find((e) => e.kind === 'entryLH')!;
    expect(entryLH.label).toBe('SHORT (LH)');
    expect(entryLH.marker).toBe('sell');
    const breakdown = r.events.find((e) => e.kind === 'breakdown')!;
    expect(breakdown.price).toBeCloseTo(0.6 * (1 - 0.3 / 100), 6);
    expect(breakdown.marker).toBe('sell');
    expect(r.levels.stop).toBeCloseTo(0.66 * 1.01, 6);
    expect(r.levels.runLow).toBeCloseTo(0.38, 6);
    expect(r.levels.entryKind).toBe('lh');
  });

  it('with lhEntry:false, the SAME fixture falls back to the iteration-1 shape: entry (not entryLH/breakdown) breaks the 0.60 pullback low after the lower high, same price/stop as before', () => {
    const r2 = evaluate({ candles: bars, tfMs: 60_000, nowMs, params: { ...DEFAULT_PARAMS, lhEntry: false } });
    expect(kinds(r2.events)).toEqual(['spike', 'crack', 'lowerHigh', 'entry', 'target']);
    const entry = r2.events.find((e) => e.kind === 'entry')!;
    expect(entry.price).toBeCloseTo(0.6 * (1 - 0.3 / 100), 6);
    expect(entry.marker).toBe('sell');
    expect(r2.levels.stop).toBeCloseTo(0.66 * 1.01, 6);
    expect(r2.levels.entryKind).toBe('break');
  });

  it('flags the HOD bar as climax volume and ends short', () => {
    expect(r.evidence.climaxVol).toBe(true);
    expect(r.state).toBe('short');
    expect(r.headline.startsWith('SHORT signalled')).toBe(true);
  });
});

// Simplified params shared by the structural tests below (2-10): small
// round numbers so a handful of bars is enough to exercise one rule at a
// time, instead of needing an ATYR-sized run to clear DEFAULT_PARAMS'
// real-world thresholds. minVwapExtPct:0 disables that leg of the arming
// gate (see 2.2) so a run alone is enough to arm; minGapPct:1000 makes the
// gap leg unreachable so only the intended gate fires; climaxVolMult:0
// disables the (evidence-only) climax check so it never distracts from the
// rule under test.
// Iteration 2: lhEntry:false here too, so tests 2-10 (and F1/F3 below) keep
// exercising the iteration-1 armed -> `entry` mechanics they were written
// to pin, unaffected by lhEntry defaulting to true — that path gets its own
// dedicated coverage in the new tests (T1's lhEntry:false variant, T12,
// T15-T17).
const SIMPLE: Partial<SignalParams> = {
  minSpikePct: 10,
  minVwapExtPct: 0,
  minGapPct: 1000,
  minPullbackPct: 3,
  breakBufferPct: 0.5,
  stopBufferPct: 1,
  climaxVolMult: 0,
  lhEntry: false,
};

describe('evaluate — no spike (test 2)', () => {
  it('a 4% drift never clears the arming gate: idle, zero events', () => {
    const bars: Candle[] = [
      bar(0, 0.5, 0.501, 0.499, 0.5, 1000),
      bar(1, 0.5, 0.503, 0.499, 0.502, 1000),
      bar(2, 0.502, 0.505, 0.5, 0.504, 1000),
      bar(3, 0.504, 0.507, 0.502, 0.506, 1000),
      bar(4, 0.506, 0.509, 0.504, 0.508, 1000),
      bar(5, 0.508, 0.511, 0.506, 0.51, 1000),
      bar(6, 0.51, 0.513, 0.508, 0.512, 1000),
      bar(7, 0.512, 0.515, 0.51, 0.514, 1000),
      bar(8, 0.514, 0.517, 0.512, 0.516, 1000),
      bar(9, 0.516, 0.52, 0.514, 0.518, 1000),
    ];
    const nowMs = bars[bars.length - 1]!.ts + 60_000;
    const r = evaluate({ candles: bars, tfMs: 60_000, nowMs, params: DEFAULT_PARAMS });
    expect(r.state).toBe('idle');
    expect(r.events).toEqual([]);
    expect(r.headline).toBe('No spike yet');
  });
});

describe('evaluate — gapper arms via the gap gate alone (test 3)', () => {
  // Gaps to 0.70 on prevClose 0.50 (a 40% gap) then drifts DOWN on a tight
  // band that never exceeds the opening high, so the intraday run gate
  // (runLow -> HOD) stays tiny — only the gap gate can arm this fixture.
  const bars: Candle[] = [
    bar(0, 0.68, 0.7, 0.67, 0.69, 5000),
    bar(1, 0.69, 0.69, 0.66, 0.67, 3000),
    bar(2, 0.67, 0.68, 0.65, 0.66, 3000),
    bar(3, 0.66, 0.67, 0.64, 0.65, 3000),
    bar(4, 0.65, 0.66, 0.63, 0.64, 3000),
    bar(5, 0.64, 0.65, 0.62, 0.63, 3000),
    bar(6, 0.63, 0.64, 0.61, 0.62, 3000),
    bar(7, 0.62, 0.63, 0.6, 0.61, 3000),
    bar(8, 0.61, 0.62, 0.59, 0.6, 3000),
    bar(9, 0.6, 0.61, 0.58, 0.59, 3000),
  ];
  const nowMs = bars[bars.length - 1]!.ts + 60_000;

  it('arms with prevClose supplied: gapGainPct >= 20, runGainPct stays small', () => {
    const r = evaluate({ candles: bars, tfMs: 60_000, nowMs, prevClose: 0.5, params: DEFAULT_PARAMS });
    expect(r.state).not.toBe('idle');
    expect(kinds(r.events)).toContain('spike');
    expect(r.evidence.gapGainPct).toBeCloseTo(40, 0);
    expect(r.evidence.runGainPct!).toBeLessThan(15);
  });

  it('does NOT arm with prevClose omitted — the gap gate has nothing to compare against', () => {
    const r = evaluate({ candles: bars, tfMs: 60_000, nowMs, params: DEFAULT_PARAMS });
    expect(r.state).toBe('idle');
    expect(r.events).toEqual([]);
    expect(r.evidence.gapGainPct).toBeNull();
  });
});

// Shared by tests 4 and 6: a spike to HOD 11.5, crack, a confirmed swing low
// (10.9) then a confirmed lower high (11.4) -> armed, THEN a bar that prints
// above the old HOD (11.8) -> invalidated, then a fresh crack/swing
// low(11.2)/lower high(11.7) cycle re-arms against the new HOD.
function buildInvalidationBars(): Candle[] {
  return [
    bar(0, 10, 10, 10, 10, 1000),
    bar(1, 10, 10.2, 10, 10.1, 1000),
    bar(2, 10.5, 11.5, 10.4, 11.4, 5000), // HOD 11.5, arms (run 15%)
    bar(3, 11.5, 11.5, 11.0, 11.1, 1000), // crack
    bar(4, 11.1, 11.2, 10.9, 11.0, 1000),
    bar(5, 11.0, 11.3, 11.0, 11.2, 1200), // swing low 10.9 confirmed here -> bounce
    bar(6, 11.2, 11.4, 11.1, 11.35, 1300), // the peak (lower high candidate)
    bar(7, 11.35, 11.3, 11.0, 11.05, 1000), // lower high 11.4 confirmed here -> armed
    bar(8, 11.05, 11.8, 11.0, 11.6, 6000), // new HOD 11.8 -> invalidated
    bar(9, 11.6, 11.65, 11.3, 11.35, 2000), // crack (again)
    bar(10, 11.35, 11.4, 11.2, 11.25, 1500),
    bar(11, 11.25, 11.5, 11.25, 11.45, 1800), // swing low 11.2 confirmed -> bounce
    bar(12, 11.45, 11.7, 11.4, 11.65, 2000), // the second peak
    bar(13, 11.65, 11.6, 11.3, 11.35, 1800), // lower high 11.7 confirmed -> armed
  ];
}

describe('evaluate — new HOD invalidates, then re-arms (test 4)', () => {
  const params = { ...SIMPLE, swingStrength: 1, vwapLossEntry: false };
  const bars = buildInvalidationBars();

  it('a bar above the old HOD invalidates: state back to spike, levels cleared, no marker (was armed, not short)', () => {
    const through8 = bars.slice(0, 9);
    const nowMs = bars[8]!.ts + 60_000;
    const r = evaluate({ candles: through8, tfMs: 60_000, nowMs, params });
    expect(r.state).toBe('spike');
    expect(r.levels.lowerHigh).toBeNull();
    expect(r.levels.trigger).toBeNull();
    expect(r.levels.stop).toBeNull();
    const inv = r.events.find((e) => e.kind === 'invalidated')!;
    expect(inv.marker).toBeUndefined();
    expect(kinds(r.events)).toEqual(['spike', 'crack', 'lowerHigh', 'invalidated']);
  });

  it('a fresh lower high re-arms against the new HOD — a second lowerHigh event, only one spike total', () => {
    const nowMs = bars[bars.length - 1]!.ts + 60_000;
    const r = evaluate({ candles: bars, tfMs: 60_000, nowMs, params });
    expect(kinds(r.events)).toEqual(['spike', 'crack', 'lowerHigh', 'invalidated', 'crack', 'lowerHigh']);
    expect(r.state).toBe('armed');
    expect(r.levels.lowerHigh).toBeCloseTo(11.7, 6);
  });
});

describe('evaluate — forming bar never confirms structure (test 6)', () => {
  const params = { ...SIMPLE, swingStrength: 1, vwapLossEntry: false };
  const bars = buildInvalidationBars().slice(0, 8); // through bar7 — the lower-high-confirming bar

  it('bar7 forming: the would-be lowerHigh at bar6 is not emitted, state stays bounce', () => {
    const r = evaluate({ candles: bars, tfMs: 60_000, nowMs: bars[7]!.ts + 1_000, params });
    expect(kinds(r.events)).not.toContain('lowerHigh');
    expect(r.state).toBe('bounce');
  });

  it('bar7 closed: the SAME bar now confirms it — lowerHigh present, state armed', () => {
    const r = evaluate({ candles: bars, tfMs: 60_000, nowMs: bars[7]!.ts + 60_000, params });
    expect(kinds(r.events)).toContain('lowerHigh');
    expect(r.state).toBe('armed');
  });
});

describe('evaluate — forming-bar entry + append-only (test 5)', () => {
  // This synthetic case keeps the strict-prefix form (the forming events end
  // on the entry and nothing the close confirms lands ahead of it). The
  // general contract the engine gives — closed history a strict prefix, the
  // forming bar's own events surviving its close as a set — is the one
  // `expectAppendOnlyReplay` (top of this file) states and T14/T25/T30 run.
  const params = { ...SIMPLE, swingStrength: 1, vwapLossEntry: false };
  const bars = [
    ...buildInvalidationBars().slice(0, 8), // armed after bar7 (trigger 10.8455)
    bar(8, 11.05, 11.1, 10.8, 10.85, 2000), // low breaks the trigger
    bar(9, 10.85, 10.9, 10.75, 10.8, 1500),
  ];

  it('bar8 forming, its low already below the trigger -> entry is present', () => {
    const r = evaluate({ candles: bars.slice(0, 9), tfMs: 60_000, nowMs: bars[8]!.ts + 1_000, params });
    expect(kinds(r.events)).toContain('entry');
  });

  it('bar8 closed plus one more bar: the forming-bar events are a strict, byte-identical prefix of the closed-bar events', () => {
    const forming = evaluate({ candles: bars.slice(0, 9), tfMs: 60_000, nowMs: bars[8]!.ts + 1_000, params });
    const closed = evaluate({ candles: bars.slice(0, 10), tfMs: 60_000, nowMs: bars[9]!.ts + 1_000, params });
    expect(closed.events.slice(0, forming.events.length)).toEqual(forming.events);
    expect(closed.events.length).toBeGreaterThan(forming.events.length);
  });
});

describe('evaluate — VWAP-loss entry (test 7)', () => {
  // Iteration 2 (B1/B4): `vwapLossEntry` is now honoured ONLY once a lower
  // high already exists, with the stop measured from THAT lower high, never
  // from the raw HOD — the real-fixture problem this fixes was an
  // `entryVwap` firing minutes into the day with a stop parked above HOD (a
  // 35%-risk short on one real name). The iteration-1 fixture here (a spike
  // straight into a crack with NO swing structure at all) can no longer
  // exercise the "fires" path — a lower high is now a precondition, not an
  // alternative — so it is extended with a confirmed swing low + lower high
  // (armed, lhEntry:false so the engine doesn't short off the lower high
  // itself) before the VWAP-losing bar.
  const bars: Candle[] = [
    bar(0, 5.0, 5.02, 4.98, 5.0, 2000),
    bar(1, 5.0, 6.0, 5.0, 5.95, 9000), // HOD 6.0, arms
    bar(2, 5.95, 5.96, 5.5, 5.55, 4000), // crack, pullback 8.3%
    bar(3, 5.55, 5.6, 5.4, 5.45, 2000), // swing low candidate (low 5.4)
    bar(4, 5.45, 5.7, 5.42, 5.6, 1800), // confirms swing low (k=1, j=3) -> bounce
    bar(5, 5.6, 5.8, 5.55, 5.7, 1800), // the peak (lower high candidate, high 5.8)
    bar(6, 5.7, 5.6, 5.5, 5.55, 1600), // confirms lower high (k=1, j=5) -> armed (lhEntry:false, so no entryLH)
    bar(7, 5.55, 5.56, 5.45, 4.9, 3000), // closes well below VWAP; low 5.45 stays above the 5.373 trigger, so this isn't a breakdown either
  ];
  const nowMs = bars[bars.length - 1]!.ts + 60_000;
  const params = { ...SIMPLE, swingStrength: 1, lhEntry: false };

  it('vwapLossEntry:true, a lower high exists — entryVwap fires, stop = lowerHigh*(1+stopBufferPct%), NEVER hod', () => {
    const r = evaluate({ candles: bars, tfMs: 60_000, nowMs, params: { ...params, vwapLossEntry: true } });
    expect(r.levels.lowerHigh).toBeCloseTo(5.8, 6);
    const ev = r.events.find((e) => e.kind === 'entryVwap')!;
    expect(ev).toBeDefined();
    expect(ev.label).toBe('SHORT (VWAP)');
    expect(ev.marker).toBe('sell');
    expect(r.state).toBe('short');
    expect(r.levels.entryKind).toBe('vwap');
    expect(r.levels.stop).toBeCloseTo(5.8 * 1.01, 6); // the lower high, not hod (6.0) — B1
  });

  it('vwapLossEntry:false — the same VWAP loss, with the same lower high present, produces no entry at all', () => {
    const r = evaluate({ candles: bars, tfMs: 60_000, nowMs, params: { ...params, vwapLossEntry: false } });
    expect(kinds(r.events)).not.toContain('entryVwap');
    expect(r.state).toBe('armed'); // still waiting — a lower high exists (armed) but nothing has fired
  });
});

describe('evaluate — stop (test 8)', () => {
  it('while short, a close above the stop covers: stopped emitted, state back to bounce (a swing low exists), lowerHigh cleared', () => {
    const params = { ...SIMPLE, swingStrength: 1, vwapLossEntry: false };
    const bars: Candle[] = [
      bar(0, 10, 10, 10, 10, 1000),
      // Iteration 2 (B4 hysteresis): bar1's volume is 1500, not the original
      // 9000 — at 9000 shares this single bar so dominates the cumulative
      // VWAP that every post-crack close sits below the 0.5% loss band and
      // NEVER reclaims before the lower high at bar6 confirms, so it is
      // tracked SILENTLY (B2) and this fixture could never reach `armed` —
      // a real behaviour, just not what this test is isolating (see the new
      // T11 real-ATYR test for that exact interaction). 1500 keeps VWAP
      // close enough to the post-crack range that bar5's bounce reclaims it
      // before bar6, restoring the original armed/entry/stop sequence below.
      bar(1, 10, 13, 10, 12.8, 1500), // big HOD (13) well above where the lower high will form
      bar(2, 12.8, 12.9, 11.5, 11.6, 4000), // crack
      bar(3, 11.6, 11.7, 11.3, 11.4, 2000),
      bar(4, 11.4, 11.6, 11.35, 11.5, 1800), // swing low 11.3 confirmed here -> bounce
      bar(5, 11.5, 11.8, 11.45, 11.75, 2200), // the peak
      bar(6, 11.75, 11.7, 11.4, 11.45, 2000), // lower high 11.8 confirmed here -> armed
      bar(7, 11.45, 11.5, 11.2, 11.25, 2500), // low breaks the trigger -> short
      bar(8, 11.25, 12.0, 11.2, 11.95, 3000), // closes above the stop (11.918), high still < HOD(13)
    ];
    const nowMs = bars[bars.length - 1]!.ts + 60_000;
    const r = evaluate({ candles: bars, tfMs: 60_000, nowMs, params });

    expect(kinds(r.events)).toContain('entry');
    const stopped = r.events.find((e) => e.kind === 'stopped')!;
    expect(stopped).toBeDefined();
    expect(stopped.marker).toBe('buy');
    expect(stopped.price).toBeCloseTo(11.8 * 1.01, 6);
    expect(r.state).toBe('bounce');
    expect(r.levels.lowerHigh).toBeNull();
    expect(r.levels.swingLow).toBeCloseTo(11.3, 6);
  });
});

describe('evaluate — two days (test 9)', () => {
  it("today is idle but events still carries yesterday's entry; dayKey is today's", () => {
    const params = { ...SIMPLE, swingStrength: 1, vwapLossEntry: false };
    const DAY_MS = 86_400_000;
    const day1: Candle[] = [
      bar(0, 10, 10, 10, 10, 1000),
      bar(1, 10, 13, 10, 12.8, 1500), // 1500, not 9000 — same VWAP-hysteresis reason as test 8's identical fixture
      bar(2, 12.8, 12.9, 11.5, 11.6, 4000),
      bar(3, 11.6, 11.7, 11.3, 11.4, 2000),
      bar(4, 11.4, 11.6, 11.35, 11.5, 1800),
      bar(5, 11.5, 11.8, 11.45, 11.75, 2200),
      bar(6, 11.75, 11.7, 11.4, 11.45, 2000),
      bar(7, 11.45, 11.5, 11.2, 11.25, 2500),
      bar(8, 11.25, 12.0, 11.2, 11.95, 3000),
    ];
    // Today: a quiet ~4% drift, same shape as test 2 — never arms.
    const day2: Candle[] = [];
    for (let i = 0; i < 15; i++) {
      const px = 10 + (0.4 * i) / 14;
      day2.push(bar(i, px, px + 0.05, px - 0.05, px, 1000, DAY_MS));
    }
    const all = [...day1, ...day2];
    const nowMs = all[all.length - 1]!.ts + 60_000;
    const r = evaluate({ candles: all, tfMs: 60_000, nowMs, params });

    expect(r.state).toBe('idle');
    expect(r.dayKey).toBe(etDayKey(day2[day2.length - 1]!.ts));
    expect(r.dayKey).not.toBe(etDayKey(day1[day1.length - 1]!.ts));
    expect(kinds(r.events)).toContain('entry');
  });
});

describe('evaluate — params respected: swingStrength (test 10)', () => {
  it('swingStrength:1 confirms the same lower high one bar earlier than swingStrength:2', () => {
    const params = { ...SIMPLE };
    const bars: Candle[] = [
      bar(0, 10, 10, 10, 10, 1000),
      bar(1, 10, 12, 10, 11.9, 6000), // HOD 12, arms
      bar(2, 11.9, 12.0, 11.3, 11.4, 3000), // crack
      bar(3, 11.4, 11.5, 11.2, 11.3, 1500),
      bar(4, 11.3, 11.6, 11.25, 11.5, 1600),
      bar(5, 11.5, 11.7, 11.45, 11.65, 1800), // the peak (high 11.7)
      bar(6, 11.65, 11.6, 11.4, 11.45, 1500), // 1 bar past the peak
      bar(7, 11.45, 11.5, 11.3, 11.35, 1400), // 2 bars past the peak
    ];
    // Through bar6 only: k=1's candidate (j=5, checked at i=6) is
    // confirmable here; k=2's candidate (j=5, checked at i=7) is not yet —
    // bar7 hasn't closed in this slice.
    const cut = bars.slice(0, 7);
    const nowMs = bars[6]!.ts + 60_000;

    const k1 = evaluate({ candles: cut, tfMs: 60_000, nowMs, params: { ...params, swingStrength: 1 } });
    const k2 = evaluate({ candles: cut, tfMs: 60_000, nowMs, params: { ...params, swingStrength: 2 } });

    expect(kinds(k1.events)).toContain('lowerHigh');
    expect(kinds(k2.events)).not.toContain('lowerHigh');
  });
});

// ─── Review findings F1-F4 regression tests ──────────────────────────────
// These pin the fixes from the confirmed engine review findings (F1: running
// pullbackLow folded in before stepEntry starves the no-swing-low entry
// path; F2: caller prevClose applied to day 0 instead of the day it
// describes; F3: pre-crack swing structure poisons the post-top trigger;
// F4: stepBaseTarget ran after stepStop, so a forming-bar BASE touch could
// un-happen when the bar closed above the stop). Each was reproduced against
// the pre-fix engine before the fix landed.

describe('evaluate — F1: running pullbackLow must not be folded in before stepEntry (test 11)', () => {
  it('armed with no confirmed swing low: a break of the running pullback low fires once and stays fired', () => {
    const p = { ...SIMPLE, swingStrength: 2, vwapLossEntry: false };
    // Iteration 2 (B4 hysteresis): bars 5-6's closes are nudged up from the
    // originals (1.08, 1.07) to 1.13/1.095 — same VWAP-band reason as test
    // 8/9 above. Only the CLOSES move (opens/highs/lows, so the swing-high
    // at bar4 and the running-pullbackLow math this test is actually about,
    // are untouched); the originals left VWAP lost before bar6's lower high
    // confirms, so it was tracked silently (B2) and never armed at all.
    const A: Candle[] = [
      bar(0, 1, 1, 1, 1, 1000),
      bar(1, 1, 1.2, 1, 1.19, 5000),
      bar(2, 1.19, 1.16, 1.1, 1.11, 2000),
      bar(3, 1.11, 1.13, 1.08, 1.12, 1000),
      bar(4, 1.12, 1.17, 1.11, 1.15, 1000),
      bar(5, 1.15, 1.14, 1.07, 1.13, 1000),
      bar(6, 1.08, 1.1, 1.06, 1.095, 1000),
      bar(7, 1.07, 1.08, 1.05, 1.06, 1000),
    ];
    const forming = evaluate({ candles: A, tfMs: 60_000, nowMs: A[7]!.ts + 1_000, params: p });
    const closed = evaluate({ candles: A, tfMs: 60_000, nowMs: A[7]!.ts + 60_000, params: p });
    expect(kinds(forming.events)).toContain('entry');
    // Before the fix the closed pass re-evaluates rule (d) against a
    // pullbackLow that already includes bar7's own low, so the break test
    // can never be true and the entry vanishes on close.
    expect(closed.events.slice(0, forming.events.length)).toEqual(forming.events);
    expect(closed.state).toBe('short');
  });
});

describe('evaluate — F2: prevClose belongs to the LAST day, not day 0 (test 12)', () => {
  it('prevClose belongs to the last day when candles span yesterday + today', () => {
    const DAY_MS = 86_400_000;
    const yday = [0, 1, 2, 3, 4].map((m) => bar(m, 0.68, 0.685, 0.675, 0.68, 1000, -DAY_MS));
    const today = [
      bar(0, 0.68, 0.7, 0.67, 0.69, 5000),
      bar(1, 0.69, 0.69, 0.66, 0.67, 3000),
      bar(2, 0.67, 0.68, 0.65, 0.66, 3000),
      bar(3, 0.66, 0.67, 0.64, 0.65, 3000),
    ];
    const r = evaluate({ candles: [...yday, ...today], tfMs: 60_000, nowMs: today[3]!.ts + 60_000, prevClose: 0.5, params: DEFAULT_PARAMS });
    // Before the fix, prevClose 0.5 is applied to day 0 (yesterday), and
    // today's gap gate is measured against yesterday's last candle (0.68)
    // instead of the official prior close.
    expect(r.evidence.gapGainPct).toBeCloseTo(40, 0);
    expect(r.events.filter((e) => e.ts < today[0]!.ts)).toEqual([]);
    expect(kinds(r.events)).toContain('spike');
  });
});

describe('evaluate — F3: pre-crack swing structure must not poison the trigger (test 13)', () => {
  // Iteration 2 note: in fixtures C and E below, VWAP now happens to go
  // lost (B4 hysteresis) at/just before the bar that would confirm the
  // post-crack lower high, so that lower high is tracked SILENTLY (B2) and
  // `entry` never fires at all in either fixture any more — a real,
  // sensible consequence of B2 (below VWAP the re-entry signal is
  // `vwapReject`, not another lower high), not a regression: these fixtures
  // are 7-8 bars, hand-built to isolate the F3 trigger/pullbackLow math
  // specifically, with no real-price VWAP behind them, so they're simply
  // too short-and-low-volume to also stay clear of the 0.5% band. The `if
  // (entry)` guards below already made this a "when present" check rather
  // than a "must be present" one, so this loses no real coverage of what F3
  // actually pins (triggerBase's min() never using a stale/undercut swing
  // low) — confirmed separately by T11-T13's real-fixture assertions.
  it('a swing low printed during the spike is not the post-top trigger; the entry level must be inside the bar', () => {
    const p = { ...SIMPLE, minPullbackPct: 8, swingStrength: 2, vwapLossEntry: false };
    const C: Candle[] = [
      bar(0, 1.0, 1.16, 1.0, 1.15, 1000),
      bar(1, 1.15, 1.2, 1.15, 1.19, 5000),
      bar(2, 1.19, 1.19, 1.17, 1.18, 1000),
      bar(3, 1.18, 1.185, 1.14, 1.175, 1000),
      bar(4, 1.175, 1.19, 1.17, 1.185, 1000),
      bar(5, 1.185, 1.195, 1.18, 1.19, 1000),
      bar(6, 1.1, 1.11, 1.05, 1.06, 3000),
      bar(7, 1.06, 1.1, 1.02, 1.03, 2000),
    ];
    const r = evaluate({ candles: C, tfMs: 60_000, nowMs: C[7]!.ts + 60_000, params: p });
    const entry = r.events.find((e) => e.kind === 'entry');
    // Before the fix, the swing low printed at bar5 WHILE STILL SPIKING
    // (before the crack at bar6) becomes the trigger base, so entry fires at
    // 1.14*0.995 = 1.1343 — above bar7's own high of 1.10.
    if (entry) expect(entry.price).toBeLessThanOrEqual(C[7]!.high);
  });

  it('no lowerHigh is announced while the engine still says do not short', () => {
    const p = { ...SIMPLE, minPullbackPct: 8, swingStrength: 2, vwapLossEntry: false };
    const D: Candle[] = [
      bar(0, 1.0, 1.16, 1.0, 1.15, 1000),
      bar(1, 1.15, 1.2, 1.15, 1.19, 5000),
      bar(2, 1.19, 1.17, 1.16, 1.165, 1000),
      bar(3, 1.165, 1.18, 1.165, 1.175, 1000),
      bar(4, 1.175, 1.19, 1.17, 1.185, 1000),
      bar(5, 1.185, 1.185, 1.175, 1.18, 1000),
      bar(6, 1.18, 1.18, 1.172, 1.176, 1000),
    ];
    const r = evaluate({ candles: D, tfMs: 60_000, nowMs: D[6]!.ts + 60_000, params: p });
    expect(r.state).toBe('spike');
    expect(kinds(r.events)).not.toContain('lowerHigh');
  });

  it('post-crack: a confirmed swing low the price has already traded through is not the trigger either (fixture E)', () => {
    const p = { ...SIMPLE, swingStrength: 2, vwapLossEntry: false };
    const E: Candle[] = [
      bar(0, 1.0, 1.16, 1.0, 1.15, 1000),
      bar(1, 1.15, 1.2, 1.15, 1.19, 5000),
      bar(2, 1.19, 1.19, 1.14, 1.15, 1000), // crack
      bar(3, 1.15, 1.17, 1.13, 1.16, 1000),
      bar(4, 1.16, 1.18, 1.15, 1.17, 1000),
      bar(5, 1.17, 1.19, 1.16, 1.18, 1000), // swing low 1.13 confirms here (post-crack)
      bar(6, 1.18, 1.195, 1.17, 1.18, 1000),
      bar(7, 1.18, 1.19, 1.1, 1.11, 1000),
      bar(8, 1.11, 1.115, 1.02, 1.03, 1000),
    ];
    const r = evaluate({ candles: E, tfMs: 60_000, nowMs: E[8]!.ts + 60_000, params: p });
    const entry = r.events.find((e) => e.kind === 'entry');
    // Before the fix, swingLow 1.13 (confirmed post-crack, but already
    // undercut by bar7's low of 1.10) is used verbatim as the trigger base:
    // 1.13*0.995 = 1.12435, above bar8's own high of 1.115.
    if (entry) expect(entry.price).toBeLessThanOrEqual(E[8]!.high);
  });
});

describe('evaluate — F4: a BASE target reached intrabar must survive the bar closing above the stop (test 14)', () => {
  it('a BASE target reached intrabar survives the bar closing above the stop', () => {
    const p = { ...SIMPLE, swingStrength: 2, vwapLossEntry: false };
    const G: Candle[] = [
      bar(0, 1.0, 1.16, 1.0, 1.15, 1000),
      bar(1, 1.15, 1.2, 1.15, 1.19, 5000),
      bar(2, 1.14, 1.14, 1.1, 1.11, 2000),
      bar(3, 1.11, 1.12, 1.08, 1.11, 1000),
      bar(4, 1.11, 1.15, 1.1, 1.14, 1000),
      bar(5, 1.14, 1.14, 1.09, 1.1, 1000),
      bar(6, 1.1, 1.12, 1.085, 1.09, 1000),
      bar(7, 1.09, 1.1, 1.07, 1.075, 1000),
      bar(8, 1.075, 1.19, 0.99, 1.18, 9000),
    ];
    const forming = evaluate({ candles: G, tfMs: 60_000, nowMs: G[8]!.ts + 1_000, params: p });
    const closed = evaluate({ candles: G, tfMs: 60_000, nowMs: G[8]!.ts + 60_000, params: p });
    expect(kinds(forming.events)).toContain('target');
    // Before the fix, stepVwap/stepStop ran before stepBaseTarget in the
    // closed pass, so a bar that closes back above the stop emits `stopped`
    // and skips (g) entirely — the BASE target seen while forming vanishes.
    expect(closed.events.slice(0, forming.events.length)).toEqual(forming.events);
  });
});

// ─── Addendum B8 T11-T17 — real fixtures + the iteration-2 rule changes ──
//
// The fixtures are the real Massive 1-minute bars in src/signals/fixtures/
// (previous trading day + 2026-09-24, `{ sym, date, source, prevClose,
// bars }`), replayed through `toCandle` (chartAgg.ts) exactly as the live
// window would. T11-T13 pin ATYR's actual top; T14 sweeps the four
// gappers for cross-cutting invariants; T15-T17 isolate the three
// synthetic-only rules (stopByClose, vwapLossEntry's lower-high gate, a
// VWAP rejection while not yet short) that the real fixtures don't
// exercise in isolation.

function todayEvents(events: SignalEvent[], dayKey: string): SignalEvent[] {
  return events.filter((e) => etDayKey(e.ts) === dayKey);
}

describe('evaluate — T11 real ATYR top, DEFAULT_PARAMS (B8)', () => {
  // DISAGREEMENT WITH THE ADDENDUM'S HAND-COMPUTED REFERENCE, reported here
  // per the task's own instruction rather than tuned away — TWO layers of
  // it, both load-bearing:
  //
  //  1. (addendum 1, carried forward) minGapPct's default (15, was 20) makes
  //     the GAP gate arm at 06:07 (HOD 0.4398, refClose 0.3751 -> +17.25%),
  //     one bar before the run gate would reach 06:08's +32.1%; the
  //     re-summed VWAP@06:22 (0.5883, not the addendum's hand-computed
  //     0.5782) puts close[06:22] (0.5802) BELOW the 0.5% loss band heading
  //     into the 06:23 bar, not above it — see addendum 3 §8 below.
  //  2. (addendum 3, §8, THIS iteration) a lower high is now silent only
  //     when ITS OWN price sits below VWAP at its confirming bar, not
  //     whenever the transient `vwapLost` flag happens to be true. The
  //     06:21 lower high (0.6670, confirmed on the 06:23 close) sits ~12%
  //     ABOVE VWAP despite the 06:22 dip noted above, and VWAP reclaims on
  //     that SAME 06:23 bar — so under §8 it is NO LONGER silenced: it is
  //     visible, and (lhEntry default true) IS the entry, exactly as
  //     addendum 3 §8's own worked-through consequence predicts. Every
  //     event through the 06:34 target/07:03 vwapReject below matches that
  //     worked-through sequence's OWN kinds/times/prices exactly (entryLH
  //     'SHORT (LH)' 0.6030 at 06:23, stop ending at 0.6868, entryPrice
  //     0.6030) — this test's numbers are not re-derived independently, they
  //     ARE addendum 3 §8's own prediction, confirmed by actually running
  //     the rules.
  //
  //  One thing §8's own worked example does NOT mention, because it only
  //  walks the sequence through the 07:03 vwapReject: the real fixture has
  //  MORE bars after 07:03, and under the SAME rule a fifth swing high
  //  (0.5600, confirmed 07:03) is ALSO above its own bar's VWAP (~0.549 at
  //  07:03, per the vwapReject price just before it) and so is visible too,
  //  firing one more `lowerHigh` — an honest, load-bearing consequence of §8
  //  applied consistently through the end of the fixture, not a rule
  //  disagreement, so it is asserted here rather than trimmed to match the
  //  addendum's own (shorter, "as far as I walked it") worked example.
  //
  //  3. (addendum 5, THIS iteration) the adds are capped, which takes two of
  //  the four `entryLH`s out (the `lowerHigh`s themselves all stay, list-only,
  //  and the stop bookkeeping still runs — the final stop is still 0.6868):
  //    - 06:32 'ADD (LH)' (close 0.5958) is REFUSED by `maxAddRiskPct`: the
  //      06:30 lower high 0.6800 first moves the stop up to 0.6868 (0.68 x
  //      1.01), and (0.6868 - 0.5958) / 0.5958 = 15.3% > 12. Measured against
  //      the pre-move stop 0.6737 it would still be 13.1% > 12, so the
  //      outcome does not hinge on which stop the risk uses.
  //    - 07:05 'ADD (LH)' (close 0.5001) is REFUSED by `!t1Hit` (T1 VWAP
  //      already printed at 06:34); its risk would also have been 37.3%.
  //    - 06:27 'ADD (LH)' (close 0.6324, stop 0.6737 -> 6.5% risk, no T1
  //      yet, first add) survives.
  //  DIFFERENCE FROM ADDENDUM 5's T26 EXPECTATION: it expects "exactly two
  //  ADD-type markers before 06:34 (06:27, 06:32)"; the rules produce ONE
  //  (06:27), because 06:32 is a 15.3%-risk add. The 07:03 VWAP REJECT it
  //  expects "still fires" DOES fire — as the SECOND counted add, which is
  //  only possible because 06:32 was refused (see T26).
  const candles = (atyrFixture.bars as Bar[]).map(toCandle);
  const nowMs = candles[candles.length - 1]!.ts + 10 * 60_000;
  // prevClose omitted: the fixture's own prevClose (0) is the live-scanner
  // lookup bug section A names ("tracked separately, do not fix here") —
  // omitting it is what a caller with a genuinely-missing prevClose does,
  // and B5's refClose/refSource below show the fallback taking over either way.
  const r = evaluate({ candles, tfMs: 60_000, nowMs, params: DEFAULT_PARAMS });
  const today = todayEvents(r.events, '2026-09-24');

  it('emission-order event sequence the rules actually produce for 2026-09-24 (see the disagreement note above)', () => {
    console.log(
      'ATYR today events:',
      today.map((e) => `${fmtEt(e.ts)} ${e.kind} ${e.label} ${fmtPx(e.price)}`).join(' | '),
    );
    // iteration 4 §8: the 06:21 lower high is no longer silenced (it sits
    // above VWAP at its own 06:23 confirming bar), so entryLH/breakdown now
    // fire at 06:23 instead of the pre-§8 sequence's 06:25/06:27/06:34 — and
    // a further lower high appears at 07:03 for the same reason (see the
    // class comment above). Iteration 5 (maxAdds/maxAddRiskPct/!t1Hit): the
    // entryLH that used to follow the 06:30 lower high (06:32, 15.3% risk)
    // and the one after the 07:03 lower high (07:05, after T1) are refused,
    // so those two lowerHighs now stand alone, list-only.
    expect(today.map((e) => e.kind)).toEqual([
      'spike',
      'crack',
      'vwapReclaim',
      'invalidated',
      'crack',
      'lowerHigh',
      'entryLH',
      'breakdown',
      'vwapReclaim',
      'lowerHigh',
      'entryLH',
      'lowerHigh',
      'target',
      'vwapReject',
      'lowerHigh',
    ]);
    expect(today[0]!.label).toBe('SPIKE');
    expect(fmtEt(today[0]!.ts)).toBe('06:07'); // the gap gate, not 06:08's run gate — disagreement note (1)
    expect(today[0]!.price).toBeCloseTo(0.4398, 4);
    const invalidated = today.find((e) => e.kind === 'invalidated')!;
    expect(fmtEt(invalidated.ts)).toBe('06:16');
    expect(invalidated.marker).toBeUndefined(); // was spike/crack, never short — matches the addendum exactly
    const secondCrack = today.filter((e) => e.kind === 'crack')[1]!;
    expect(fmtEt(secondCrack.ts)).toBe('06:18'); // matches the addendum exactly
  });

  it('§8: ALL FOUR post-crack lower highs are visible (none sit below VWAP at their own confirming bar) — the 06:21 one is no longer silenced', () => {
    const lowerHighs = today.filter((e) => e.kind === 'lowerHigh');
    expect(lowerHighs).toHaveLength(4);
    expect(fmtEt(lowerHighs[0]!.ts)).toBe('06:21');
    expect(lowerHighs[0]!.price).toBeCloseTo(0.667, 4);
    expect(fmtEt(lowerHighs[1]!.ts)).toBe('06:25');
    expect(lowerHighs[1]!.price).toBeCloseTo(0.66, 4);
    expect(fmtEt(lowerHighs[2]!.ts)).toBe('06:30');
    expect(lowerHighs[2]!.price).toBeCloseTo(0.68, 4);
    expect(fmtEt(lowerHighs[3]!.ts)).toBe('07:03');
    expect(lowerHighs[3]!.price).toBeCloseTo(0.56, 4);
  });

  it('entryLH shorts at the 06:23 close (0.6030) that confirms the 06:21 lower high — §8, the FIRST lower high is the entry again, not the second; addendum 5 leaves it ONE add (06:27), the 06:32 and 07:05 adds are refused', () => {
    const entries = today.filter((e) => e.kind === 'entryLH');
    expect(entries).toHaveLength(2);
    expect(entries[0]!.label).toBe('SHORT (LH)');
    expect(fmtEt(entries[0]!.ts)).toBe('06:23');
    expect(entries[0]!.price).toBeCloseTo(0.603, 4);
    expect(entries[0]!.marker).toBe('sell');
    expect(entries[1]!.label).toBe('ADD (LH)');
    expect(fmtEt(entries[1]!.ts)).toBe('06:27');
    expect(entries[1]!.price).toBeCloseTo(0.6324, 4);
    // Addendum 5, maxAddRiskPct: the 06:32 add (close 0.5958, which the
    // pre-cap engine printed) is refused at 15.3% risk — but the 06:30 lower
    // high it would have confirmed is still there, list-only, and the stop
    // still moved up to it (final-state test below).
    expect(today.filter((e) => fmtEt(e.ts) === '06:32' && e.marker)).toEqual([]);
    // Addendum 5, !t1Hit: the 07:05 add (close 0.5001) is refused after T1.
    expect(today.filter((e) => fmtEt(e.ts) === '07:05' && e.marker)).toEqual([]);
  });

  it('breakdown confirms the SAME 06:23 bar as entryLH (0.5763, matches the addendum exactly); T1 VWAP fires later, at 06:34 (0.5963)', () => {
    const breakdown = today.find((e) => e.kind === 'breakdown')!;
    expect(fmtEt(breakdown.ts)).toBe('06:23'); // §8: no longer 06:34 — the entry moved up to 06:23, so the breakdown/confirmation is the SAME bar's low breaking the pullback low
    expect(breakdown.price).toBeCloseTo(0.57628, 4);
    expect(breakdown.marker).toBe('sell');
    const target = today.find((e) => e.kind === 'target')!;
    expect(target.label).toBe('T1 VWAP');
    expect(fmtEt(target.ts)).toBe('06:34');
    expect(target.price).toBeCloseTo(0.59632, 4);
    expect(target.marker).toBe('buy');
  });

  it("vwapReject at 07:03 (~0.5494) — the addendum's own \"≈0.5477\" is within 0.3%, supporting a hand-arithmetic slip rather than a rule disagreement", () => {
    const reject = today.find((e) => e.kind === 'vwapReject')!;
    expect(reject.label).toBe('VWAP REJECT'); // already short by 07:03 — the re-entry label, not 'SHORT (VWAP REJECT)'
    expect(fmtEt(reject.ts)).toBe('07:03');
    expect(reject.price).toBeCloseTo(0.54935, 4);
    expect(reject.marker).toBe('sell');
  });

  it('after 06:34 there is the 07:03 vwapReject and a further list-only lowerHigh (§8, same rule as the 06:23 one) and nothing else — no entryLH (addendum 5: !t1Hit), no breakdown/entry/entryVwap/stopped/invalidated/second target/vwapReclaim', () => {
    const after = today.filter((e) => fmtEt(e.ts) > '06:34');
    expect(after.map((e) => e.kind)).toEqual(['vwapReject', 'lowerHigh']);
  });

  it('final state/levels/evidence — HOD, runLow, refClose/refSource and climaxVol all match the addendum exactly; the stop moved up once, to 0.68*1.01; entryPrice is now the 06:23 entry (§8)', () => {
    expect(r.state).toBe('short');
    expect(r.levels.hod).toBeCloseTo(0.732, 4);
    expect(r.levels.runLow).toBeCloseTo(0.3785, 4);
    expect(r.levels.stop).toBeCloseTo(0.68 * 1.01, 4);
    expect(r.levels.entryPrice).toBeCloseTo(0.603, 4); // §8: the FIRST entry is now 06:23 (was 06:27 pre-§8) — later ADDs never touch this
    expect(fmtEt(r.levels.entryTs!)).toBe('06:23');
    expect(r.levels.entryKind).toBe('lh');
    expect(r.evidence.climaxVol).toBe(false); // HOD-bar volume 2.17M < 3x the 847k mean of the earlier bars — matches the addendum exactly
    expect(r.evidence.refSource).toBe('prevDayClose');
    expect(r.evidence.refClose).toBeCloseTo(0.3751, 4);
  });
});

describe('evaluate — T12 real ATYR, lhEntry:false (B8)', () => {
  // Same fixture as T11, lhEntry off — the engine falls back to the
  // iteration-1 armed -> `entry` path entirely. §8 (this iteration) means
  // the 06:21 lower high now ARMS on the 06:23 close (it is no longer
  // silenced — see T11's class comment), and since bar 06:23's own LOW has
  // already broken the pullback-low trigger, stepEntry fires the classic
  // `entry` on the VERY SAME bar (structure and entry both run for bar i in
  // the same pass) — so entry lands at 06:23, not the pre-§8 sequence's
  // 06:34, though the PRICE (0.57628) and final stop (0.6868) are unchanged:
  // both are pullback-low/lower-high math that converges on the same
  // numbers regardless of which bar first reaches them.
  const candles = (atyrFixture.bars as Bar[]).map(toCandle);
  const nowMs = candles[candles.length - 1]!.ts + 10 * 60_000;
  const r = evaluate({ candles, tfMs: 60_000, nowMs, params: { ...DEFAULT_PARAMS, lhEntry: false } });
  const today = todayEvents(r.events, '2026-09-24');

  it('entry (not entryLH/breakdown) fires once, at 06:23, breaking the pullback low on the SAME bar the lower high arms; stop ends at 0.6868', () => {
    expect(today.map((e) => e.kind)).not.toContain('entryLH');
    expect(today.map((e) => e.kind)).not.toContain('breakdown');
    const entry = today.find((e) => e.kind === 'entry')!;
    expect(entry).toBeDefined();
    expect(entry.label).toBe('SHORT');
    expect(fmtEt(entry.ts)).toBe('06:23'); // §8: was 06:34 pre-§8 (see the class comment)
    expect(entry.price).toBeCloseTo(0.57628, 4);
    expect(r.levels.entryKind).toBe('break');
    expect(r.levels.stop).toBeCloseTo(0.68 * 1.01, 4); // matches the addendum's own prediction exactly, unaffected by §8
  });

  it('every post-06:23 lower high (06:25, 06:30, 07:03) still prints list-only, with no ADD (lhEntry is off)', () => {
    const lowerHighs = today.filter((e) => e.kind === 'lowerHigh');
    expect(lowerHighs.map((e) => fmtEt(e.ts))).toEqual(['06:21', '06:25', '06:30', '07:03']);
  });
});

describe('evaluate — T13 real ATYR, stopByClose:false (B8)', () => {
  // Same fixture/§8 shift as T11: entryLH now fires on the 06:21 lower high
  // (confirmed 06:23, stop 0.667*1.01 = 0.6737), a hard stop then trades
  // through it on 06:30's high (0.6800 >= 0.6737), and a FRESH lower high +
  // entryLH re-establishes the short — from 06:30 on, the sequence matches
  // T11/the addendum's own numbers exactly (06:32 entryLH 0.5958 stop
  // 0.6868, 06:34 breakdown 0.5717, plus the same 07:03 lowerHigh tail T11
  // has), because the reconvergence has already happened by that point.
  // Addendum 5: the 06:32 entryLH is the ENTRY of a fresh episode (the first
  // one was hard-stopped at 06:30), so no add gate applies to it even at
  // 15.3% risk — entries are never counted or risk-filtered; the 07:05 ADD
  // (LH) that used to close this sequence is refused by !t1Hit, as in T11.
  const candles = (atyrFixture.bars as Bar[]).map(toCandle);
  const nowMs = candles[candles.length - 1]!.ts + 10 * 60_000;
  const r = evaluate({ candles, tfMs: 60_000, nowMs, params: { ...DEFAULT_PARAMS, stopByClose: false } });
  const today = todayEvents(r.events, '2026-09-24');

  it('entryLH -> stopped (hard, by high) -> fresh lowerHigh+entryLH -> breakdown -> target -> vwapReject -> a further list-only lowerHigh (§8; its ADD refused by addendum 5), in that order', () => {
    expect(today.map((e) => e.kind)).toEqual([
      'spike',
      'crack',
      'vwapReclaim',
      'invalidated',
      'crack',
      'lowerHigh',
      'entryLH',
      'breakdown',
      'vwapReclaim',
      'lowerHigh',
      'entryLH',
      'stopped',
      'lowerHigh',
      'entryLH',
      'breakdown',
      'target',
      'vwapReject',
      'lowerHigh',
    ]);
  });

  it('the first entryLH (06:23, stop 0.6737) is hard-stopped at 06:30 by the HIGH (0.6800), not a close', () => {
    const entries = today.filter((e) => e.kind === 'entryLH');
    expect(fmtEt(entries[0]!.ts)).toBe('06:23'); // §8: was 06:27 pre-§8 (see the class comment)
    expect(entries[0]!.price).toBeCloseTo(0.603, 4);
    const stopped = today.find((e) => e.kind === 'stopped')!;
    expect(fmtEt(stopped.ts)).toBe('06:30');
    expect(stopped.price).toBeCloseTo(0.667 * 1.01, 4); // 0.6737 — the 06:21 lower high's stop, not 06:25's (§8)
  });

  it("the entryLH AFTER the stop (06:32, 0.5958, stop 0.6868) and the 06:34 breakdown (0.5717) match the addendum's own numbers exactly — the post-stop reconvergence. (Index 2, not 1: 06:27 is an ADD (LH) on the FIRST, not-yet-stopped episode — §8 makes the 06:21 lower high visible, so that episode now gets an add of its own before the 06:30 stop.)", () => {
    const entries = today.filter((e) => e.kind === 'entryLH');
    expect(fmtEt(entries[2]!.ts)).toBe('06:32');
    expect(entries[2]!.price).toBeCloseTo(0.5958, 4);
    expect(r.levels.stop).toBeCloseTo(0.68 * 1.01, 4);
    // Two breakdowns now (§8): 06:23 confirms the FIRST (pre-stop) episode,
    // 06:34 confirms this SECOND one — filter+index, not .find(), picks the
    // right one.
    const breakdowns = today.filter((e) => e.kind === 'breakdown');
    expect(breakdowns).toHaveLength(2);
    expect(fmtEt(breakdowns[1]!.ts)).toBe('06:34');
    expect(breakdowns[1]!.price).toBeCloseTo(0.57168, 4);
  });
});

describe('evaluate — T14 the four gappers: GCTK, WETO, SPHL, DCX (B8)', () => {
  const FIXTURES: Record<string, { sym: string; date: string; prevClose: number; bars: Bar[] }> = {
    gctk: gctkFixture as typeof gctkFixture & { bars: Bar[] },
    weto: wetoFixture as typeof wetoFixture & { bars: Bar[] },
    sphl: sphlFixture as typeof sphlFixture & { bars: Bar[] },
    dcx: dcxFixture as typeof dcxFixture & { bars: Bar[] },
  };

  it('evaluate with prevClose omitted never throws for any of the four; today-event keys are unique; refSource is prevDayClose (all four fixtures\' prevClose is 0 — the same live-scanner bug); no entryVwap anywhere (vwapLossEntry is off by default)', () => {
    for (const [sym, fx] of Object.entries(FIXTURES)) {
      const candles = fx.bars.map(toCandle);
      const nowMs = candles[candles.length - 1]!.ts + 10 * 60_000;
      let r: ReturnType<typeof evaluate>;
      expect(() => {
        r = evaluate({ candles, tfMs: 60_000, nowMs, params: DEFAULT_PARAMS });
      }).not.toThrow();
      r = evaluate({ candles, tfMs: 60_000, nowMs, params: DEFAULT_PARAMS });
      const today = todayEvents(r.events, '2026-09-24');
      console.log(`${sym.toUpperCase()} today events:`, today.map((e) => `${fmtEt(e.ts)} ${e.kind} ${e.label} ${fmtPx(e.price)}`).join(' | '));
      const keys = today.map((e) => `${e.ts}:${e.kind}:${e.label}`);
      expect(new Set(keys).size).toBe(keys.length);
      expect(r.evidence.refSource).toBe('prevDayClose');
      expect(today.map((e) => e.kind)).not.toContain('entryVwap');
    }
  });

  it('WETO (+53% on the day) arms off the fallback overnight reference alone — a spike event with a gap-gate note, gapGainPct >= 15, run/ext below their own gates', () => {
    const candles = (wetoFixture.bars as Bar[]).map(toCandle);
    const nowMs = candles[candles.length - 1]!.ts + 10 * 60_000;
    const r = evaluate({ candles, tfMs: 60_000, nowMs, params: DEFAULT_PARAMS });
    const today = todayEvents(r.events, '2026-09-24');
    const spike = today.find((e) => e.kind === 'spike');
    expect(spike).toBeDefined();
    expect(spike!.note).toContain('gap');
    expect(r.evidence.gapGainPct).not.toBeNull();
    expect(r.evidence.gapGainPct!).toBeGreaterThanOrEqual(15);
  });

  it('SPHL: no two target/vwapReclaim events fewer than 3 bars apart between 05:15 and 05:30 ET — the hysteresis-band regression (section A point 2)', () => {
    const candles = (sphlFixture.bars as Bar[]).map(toCandle);
    const nowMs = candles[candles.length - 1]!.ts + 10 * 60_000;
    const r = evaluate({ candles, tfMs: 60_000, nowMs, params: DEFAULT_PARAMS });
    const window = todayEvents(r.events, '2026-09-24')
      .filter((e) => e.kind === 'target' || e.kind === 'vwapReclaim')
      .filter((e) => fmtEt(e.ts) >= '05:15' && fmtEt(e.ts) <= '05:30')
      .sort((a, b) => a.ts - b.ts);
    for (let i = 1; i < window.length; i++) {
      expect(window[i]!.ts - window[i - 1]!.ts).toBeGreaterThanOrEqual(3 * 60_000);
    }
  });

  // Quadratic by construction (every bar is evaluated twice from bar 1), so
  // the two replay tests sit right at vitest's 5 s default on a loaded
  // machine — an explicit budget, like T30's (review window:F7).
  it('append-only under a bar-by-bar replay of today\'s bars (forming then closed, each new bar), for all four symbols — closed history a strict prefix, the forming bar\'s events surviving its close (see expectAppendOnlyReplay)', () => {
    for (const fx of Object.values(FIXTURES)) expectAppendOnlyReplay(fx.bars.map(toCandle), DEFAULT_PARAMS);
  }, 60_000);
});

describe('evaluate — T15 synthetic: stopByClose (B8)', () => {
  // spike -> crack -> swing low -> lower high confirms -> entryLH short at
  // close 11.45, stop = 11.7*1.01 = 11.817. bar7 wicks to 11.90 (ABOVE the
  // stop) but closes back at 11.60 (below it).
  const base: Candle[] = [
    bar(0, 10, 10, 10, 10, 1000),
    bar(1, 10, 12, 10, 11.9, 6000), // HOD 12, arms
    bar(2, 11.9, 12.0, 11.3, 11.4, 3000), // crack
    bar(3, 11.4, 11.5, 11.2, 11.3, 1500),
    bar(4, 11.3, 11.6, 11.25, 11.5, 1600), // swing low 11.2 confirms here (k=1,j=3) -> bounce
    bar(5, 11.5, 11.7, 11.45, 11.65, 1800), // the peak (11.7)
    bar(6, 11.65, 11.6, 11.4, 11.45, 1500), // confirms lower high 11.7 (k=1,j=5) -> entryLH short, stop 11.817
  ];
  const wickBar = bar(7, 11.45, 11.9, 11.4, 11.6, 1400); // high wicks above the stop, close does not
  const params = { ...SIMPLE, swingStrength: 1, lhEntry: true };

  it('stopByClose:true — a wick above the stop that closes back under it is ignored; stays short', () => {
    const bars = [...base, wickBar];
    const r = evaluate({ candles: bars, tfMs: 60_000, nowMs: bars[bars.length - 1]!.ts + 60_000, params: { ...params, stopByClose: true } });
    expect(r.state).toBe('short');
    expect(r.events.map((e) => e.kind)).not.toContain('stopped');
  });

  it('stopByClose:false — the SAME wick stops it out on the close, by the HIGH alone', () => {
    const bars = [...base, wickBar];
    const r = evaluate({ candles: bars, tfMs: 60_000, nowMs: bars[bars.length - 1]!.ts + 60_000, params: { ...params, stopByClose: false } });
    const stopped = r.events.find((e) => e.kind === 'stopped');
    expect(stopped).toBeDefined();
    expect(stopped!.price).toBeCloseTo(11.7 * 1.01, 6);
    expect(r.state).toBe('bounce');
  });

  it('stopByClose:false — also stops on the FORMING bar (the high is monotonic, safe to check early)', () => {
    const bars = [...base, wickBar];
    const forming = evaluate({ candles: bars, tfMs: 60_000, nowMs: wickBar.ts + 1_000, params: { ...params, stopByClose: false } });
    expect(forming.events.map((e) => e.kind)).toContain('stopped');
    const formingTrue = evaluate({ candles: bars, tfMs: 60_000, nowMs: wickBar.ts + 1_000, params: { ...params, stopByClose: true } });
    expect(formingTrue.events.map((e) => e.kind)).not.toContain('stopped');
  });
});

describe('evaluate — T16 synthetic: vwapLossEntry requires a lower high (B8)', () => {
  // Reuses T15's spike/crack/swing-low/lower-high skeleton (lhEntry:false
  // so the engine stays `armed`, not short, off the lower high itself),
  // then a bar that closes well below VWAP.
  const withLowerHigh: Candle[] = [
    bar(0, 5.0, 5.02, 4.98, 5.0, 2000),
    bar(1, 5.0, 6.0, 5.0, 5.95, 9000), // HOD 6.0, arms
    bar(2, 5.95, 5.96, 5.5, 5.55, 4000), // crack
    bar(3, 5.55, 5.6, 5.4, 5.45, 2000), // swing low candidate (low 5.4)
    bar(4, 5.45, 5.7, 5.42, 5.6, 1800), // confirms swing low -> bounce
    bar(5, 5.6, 5.8, 5.55, 5.7, 1800), // the peak (lower high candidate, high 5.8)
    bar(6, 5.7, 5.6, 5.5, 5.55, 1600), // confirms lower high (5.8) -> armed
    bar(7, 5.55, 5.56, 5.45, 4.9, 3000), // closes well below VWAP; low stays above the trigger
  ];
  // Same spike/crack, but NO swing structure at all (monotonic decline —
  // no swing low ever confirms, so no lower high either).
  const noLowerHigh: Candle[] = [
    bar(0, 5.0, 5.02, 4.98, 5.0, 500),
    bar(1, 5.0, 6.0, 5.0, 5.95, 3000),
    bar(2, 5.95, 5.96, 5.5, 5.55, 3000),
    bar(3, 5.55, 5.56, 5.3, 5.32, 8000),
    bar(4, 5.32, 5.33, 5.1, 5.12, 10000),
    bar(5, 5.12, 5.13, 4.9, 4.92, 15000),
    bar(6, 4.92, 4.93, 4.75, 4.8, 15000), // VWAP lost here
  ];
  const params = { ...SIMPLE, swingStrength: 1, lhEntry: false, vwapLossEntry: true };

  it('a lower high exists -> entryVwap fires, using it for the stop (never HOD)', () => {
    const r = evaluate({ candles: withLowerHigh, tfMs: 60_000, nowMs: withLowerHigh[withLowerHigh.length - 1]!.ts + 60_000, params });
    expect(r.levels.lowerHigh).toBeCloseTo(5.8, 6);
    const ev = r.events.find((e) => e.kind === 'entryVwap');
    expect(ev).toBeDefined();
    expect(ev!.marker).toBe('sell');
    expect(r.state).toBe('short');
    expect(r.levels.entryKind).toBe('vwap');
    expect(r.levels.stop).toBeCloseTo(5.8 * 1.01, 6); // the lower high, not HOD (6.0)
  });

  it('no lower high exists -> nothing fires at all, even with the SAME VWAP loss', () => {
    const r = evaluate({ candles: noLowerHigh, tfMs: 60_000, nowMs: noLowerHigh[noLowerHigh.length - 1]!.ts + 60_000, params });
    expect(r.levels.lowerHigh).toBeNull();
    expect(r.events.map((e) => e.kind)).not.toContain('entryVwap');
    expect(r.state).not.toBe('short');
  });
});

describe('evaluate — T17 synthetic: a VWAP rejection while not short becomes an entry (B8)', () => {
  it("'SHORT (VWAP REJECT)', stop above the rejection bar's own high", () => {
    // spike -> crack -> monotonic decline (no swing low, so no lower high —
    // isolates this from the lower-high paths) -> VWAP lost -> a bar pops
    // back up to/through VWAP intrabar but closes back under it, with the
    // PRIOR bar's high still under VWAP (the "untested from below" leg).
    const bars: Candle[] = [
      bar(0, 5.0, 5.02, 4.98, 5.0, 500),
      bar(1, 5.0, 6.0, 5.0, 5.95, 3000), // HOD 6.0, arms
      bar(2, 5.95, 5.96, 5.5, 5.55, 3000), // crack
      bar(3, 5.55, 5.56, 5.3, 5.32, 8000),
      bar(4, 5.32, 5.33, 5.1, 5.12, 10000),
      bar(5, 5.12, 5.13, 4.9, 4.92, 15000),
      bar(6, 4.92, 4.93, 4.75, 4.8, 15000), // VWAP LOST; prior bar's high stays under VWAP
      bar(7, 4.8, 5.15, 4.78, 4.95, 3000), // pops up to/through VWAP intrabar, closes back under it -> REJECT, not short yet
    ];
    const params = { ...SIMPLE, swingStrength: 2 };
    const r = evaluate({ candles: bars, tfMs: 60_000, nowMs: bars[bars.length - 1]!.ts + 60_000, params });
    const reject = r.events.find((e) => e.kind === 'vwapReject')!;
    expect(reject).toBeDefined();
    expect(reject.label).toBe('SHORT (VWAP REJECT)');
    expect(reject.marker).toBe('sell');
    expect(r.state).toBe('short');
    expect(r.levels.entryKind).toBe('vwap');
    expect(r.levels.stop).toBeCloseTo(bars[7]!.high * 1.01, 6); // above the rejection bar's OWN high, not the pullback low
  });
});

// ─── Addendum 3 — T18-T25, the LONG side (iteration 4) ───────────────────
//
// Synthetic ABCD fixture shared by T18-T23: a spike to HOD 12, a crack, a
// pullback low B (10.8), a bounce to a lower high C (11.8, the short's own
// `lowerHigh` — entryLH shorts here), a shallow higher low D (11.10, above
// both B and VWAP) that enters the long (entryHL), a breakout above C that
// confirms/adds the long AND (independently, by its own stop rule) stops
// the short, a new high that hands the long its T2/NEW-HOD targets and
// resets the shared prefix. Base bar 0 carries a deliberately large volume
// so cumulative VWAP stays anchored well below the post-spike price action
// (comfortably under both B and D) — the fixture is about the ABCD
// mechanics, not tuning VWAP timing, so it is built to keep VWAP out of the
// way except where a test is specifically about it (T23).
const ABCD_PARAMS: Partial<SignalParams> = {
  minSpikePct: 10,
  minVwapExtPct: 0,
  minGapPct: 1000,
  minPullbackPct: 3,
  climaxVolMult: 0,
  swingStrength: 1,
};

function buildAbcdBars(): Candle[] {
  return [
    bar(0, 10, 10.05, 9.95, 10, 20000), // huge volume anchors VWAP low
    bar(1, 10, 12, 9.9, 11.9, 3000), // HOD (A) = 12, arms (run +21.2%)
    bar(2, 11.9, 11.95, 11.4, 11.5, 2000), // crack (4.2% pullback)
    bar(3, 11.5, 11.6, 11.0, 11.1, 2000),
    bar(4, 11.1, 11.3, 10.8, 11.2, 2000), // the pullback low (B) candidate
    bar(5, 11.2, 11.6, 11.1, 11.5, 2000), // confirms B=10.8 -> bounce
    bar(6, 11.5, 11.8, 11.45, 11.7, 2000), // the bounce peak (C) candidate
    bar(7, 11.7, 11.75, 11.3, 11.35, 2000), // confirms C=11.8 -> lowerHigh + entryLH 'SHORT (LH)'
    bar(8, 11.35, 11.4, 11.25, 11.3, 2000), // a shallow dip (D leg) — stays well above B and VWAP
    bar(9, 11.3, 11.32, 11.10, 11.15, 2000), // the higher low (D) candidate, low=11.10
    bar(10, 11.15, 11.25, 11.12, 11.22, 2000), // confirms D=11.10 -> higherLow + entryHL 'LONG (HL)'
    bar(11, 11.22, 11.9, 11.2, 11.85, 3000), // high clears C*1.003 (11.8354) -> breakout ('BREAKOUT', long already open)
    bar(12, 11.85, 12.0, 11.8, 11.95, 3000), // close clears C*1.01 (11.918) -> short `stopped`; high==hod -> long T2 HOD
    bar(13, 11.95, 12.3, 11.9, 12.2, 3500), // a genuine NEW HOD -> long 'NEW HOD' target, short `invalidated`, shared prefix resets
  ];
}

describe('evaluate — T18 synthetic ABCD: short and long both read off the same pullback (addendum 3)', () => {
  const bars = buildAbcdBars();
  const nowMs = bars[bars.length - 1]!.ts + 60_000;
  const r = evaluate({ candles: bars, tfMs: 60_000, nowMs, params: { ...DEFAULT_PARAMS, ...ABCD_PARAMS } });

  it('emission-order sequence: short entryLH at C, long higherLow+entryHL at D, breakout, T2 HOD, short stopped, long NEW HOD target, short invalidated', () => {
    expect(r.events.map((e) => `${e.side}:${e.kind}`)).toEqual([
      'short:spike',
      'short:crack',
      'short:lowerHigh',
      'short:entryLH',
      'long:higherLow',
      'long:entryHL',
      'long:breakout',
      'long:longTarget', // T2 HOD
      'short:stopped',
      'short:invalidated',
      'long:longTarget', // NEW HOD
    ]);
    const lowerHigh = r.events.find((e) => e.kind === 'lowerHigh')!;
    expect(lowerHigh.price).toBeCloseTo(11.8, 6);
    const entryLH = r.events.find((e) => e.kind === 'entryLH')!;
    expect(entryLH.label).toBe('SHORT (LH)');
  });

  it("long side: higherLow D=11.10, entryHL 'LONG (HL)' at D's confirming close (11.22), stop = D*0.99", () => {
    const higherLow = r.events.find((e) => e.kind === 'higherLow')!;
    expect(higherLow.price).toBeCloseTo(11.1, 6);
    expect(higherLow.marker).toBeUndefined(); // list-only
    const entryHL = r.events.find((e) => e.kind === 'entryHL')!;
    expect(entryHL.label).toBe('LONG (HL)');
    expect(entryHL.price).toBeCloseTo(11.22, 6);
    expect(entryHL.marker).toBe('buy');
    expect(entryHL.side).toBe('long');
  });

  it('breakout clears C*1.003 (11.8354), labelled BREAKOUT (long already open via HL, so this is confirmation/add, not the entry)', () => {
    const breakout = r.events.find((e) => e.kind === 'breakout')!;
    expect(breakout.label).toBe('BREAKOUT');
    expect(breakout.price).toBeCloseTo(11.8 * 1.003, 6);
    expect(breakout.marker).toBe('buy');
  });

  it('T2 HOD fires when price re-touches the (still-current) HOD of 12, before the new-HOD bar resets everything', () => {
    const targets = r.events.filter((e) => e.kind === 'longTarget');
    expect(targets).toHaveLength(2);
    expect(targets[0]!.label).toBe('T2 HOD');
    expect(targets[0]!.price).toBeCloseTo(12, 6);
    expect(targets[0]!.marker).toBe('sell');
    expect(targets[1]!.label).toBe('NEW HOD');
    expect(targets[1]!.price).toBeCloseTo(12.3, 6); // the bar's own high, not the (about to update) hod
    expect(targets[1]!.marker).toBe('sell');
  });

  it("short's own stop (close above C*1.01 = 11.918) fires independently of the long's breakout — the same pullback, two honest outcomes", () => {
    const stopped = r.events.find((e) => e.kind === 'stopped')!;
    expect(stopped.price).toBeCloseTo(11.8 * 1.01, 6);
    expect(stopped.marker).toBe('buy');
  });

  it('T1 BOUNCE HIGH never fires separately — breaking C IS reaching T1, so the breakout bar latches it instead', () => {
    const t1 = r.events.filter((e) => e.kind === 'longTarget' && e.label === 'T1 BOUNCE HIGH');
    expect(t1).toHaveLength(0);
  });

  it("state sequence: 'short' after C, 'both' after D, 'long' after the short's stop, then the shared prefix ('spike') after the new HOD", () => {
    const through = (cut: number) => {
      const slice = bars.slice(0, cut + 1);
      return evaluate({ candles: slice, tfMs: 60_000, nowMs: slice[slice.length - 1]!.ts + 60_000, params: { ...DEFAULT_PARAMS, ...ABCD_PARAMS } });
    };
    expect(through(7).state).toBe('short'); // C confirmed, entryLH fired
    expect(through(10).state).toBe('both'); // D confirmed, entryHL fired, short still open
    expect(through(12).state).toBe('long'); // short's stop fired this bar, long still open
    expect(through(13).state).toBe('spike'); // new HOD closes the long and re-arms the shared prefix
  });
});

describe('evaluate — T19 same ABCD fixture, hlEntry:false (addendum 3)', () => {
  it("no entryHL; the higher low only ARMS the long — breakout itself is the entry, labelled 'LONG', stop below D", () => {
    const bars = buildAbcdBars();
    const nowMs = bars[bars.length - 1]!.ts + 60_000;
    const r = evaluate({ candles: bars, tfMs: 60_000, nowMs, params: { ...DEFAULT_PARAMS, ...ABCD_PARAMS, hlEntry: false } });
    expect(r.events.map((e) => e.kind)).not.toContain('entryHL');
    const breakout = r.events.find((e) => e.kind === 'breakout')!;
    expect(breakout.label).toBe('LONG');
    expect(breakout.price).toBeCloseTo(11.8 * 1.003, 6);
    expect(breakout.marker).toBe('buy');
    // stop is BELOW D (11.10), not above it
    const cut = evaluate({ candles: bars.slice(0, 12), tfMs: 60_000, nowMs: bars[11]!.ts + 60_000, params: { ...DEFAULT_PARAMS, ...ABCD_PARAMS, hlEntry: false } });
    expect(cut.long.state).toBe('long');
    expect(cut.levels.longStop).toBeCloseTo(11.1 * 0.99, 6);
    expect(cut.levels.longStop!).toBeLessThan(11.1);
  });
});

describe('evaluate — T20 same ABCD shape, D sits below VWAP (addendum 3)', () => {
  // Same price structure as T18 but the base bar's volume is NOT huge, so
  // VWAP stays dragged up by the post-spike bars and sits ABOVE D's own low
  // at D's confirming bar — hlAboveVwap (default true) must silence it.
  function buildBars(): Candle[] {
    const b = buildAbcdBars().slice(0, 11); // through D's confirmation only
    b[0] = bar(0, 10, 10.05, 9.95, 10, 1000); // small base volume -> VWAP tracks the run up, not the base
    return b;
  }

  it('hlAboveVwap:true — D (11.10) is below VWAP at its own bar, so no higherLow/entryHL', () => {
    const bars = buildBars();
    const r = evaluate({ candles: bars, tfMs: 60_000, nowMs: bars[bars.length - 1]!.ts + 60_000, params: { ...DEFAULT_PARAMS, ...ABCD_PARAMS, hlAboveVwap: true } });
    expect(r.events.map((e) => e.kind)).not.toContain('higherLow');
    expect(r.events.map((e) => e.kind)).not.toContain('entryHL');
    expect(r.long.state).toBe('none');
  });

  it('hlAboveVwap:false — the SAME swing low now qualifies: higherLow + entryHL fire', () => {
    const bars = buildBars();
    const r = evaluate({ candles: bars, tfMs: 60_000, nowMs: bars[bars.length - 1]!.ts + 60_000, params: { ...DEFAULT_PARAMS, ...ABCD_PARAMS, hlAboveVwap: false } });
    expect(r.events.map((e) => e.kind)).toContain('higherLow');
    const entryHL = r.events.find((e) => e.kind === 'entryHL')!;
    expect(entryHL.price).toBeCloseTo(11.22, 6);
    expect(r.long.state).toBe('long');
  });
});

describe('evaluate — T21 a swing low at/below B is not a higher low; the short proceeds via breakdown (addendum 3)', () => {
  it('no higherLow ever, while the short breaks the pullback low normally', () => {
    const bars: Candle[] = [
      bar(0, 10, 10.05, 9.95, 10, 20000),
      bar(1, 10, 12, 9.9, 11.9, 3000),
      bar(2, 11.9, 11.95, 11.4, 11.5, 2000),
      bar(3, 11.5, 11.6, 11.0, 11.1, 2000),
      bar(4, 11.1, 11.3, 10.8, 11.2, 2000), // B candidate (10.8)
      bar(5, 11.2, 11.6, 11.1, 11.5, 2000), // confirms B -> bounce
      bar(6, 11.5, 11.8, 11.45, 11.7, 2000), // C candidate
      bar(7, 11.7, 11.75, 11.3, 11.35, 2000), // confirms C -> lowerHigh + entryLH
      bar(8, 11.35, 11.4, 11.0, 11.05, 2000), // turning back down, breaking toward B
      bar(9, 11.05, 11.1, 10.5, 10.6, 2000), // breaks the ~10.77 trigger -> short `breakdown`; also BELOW B (10.8)
      bar(10, 10.6, 10.7, 10.4, 10.5, 2000), // a new low, still below B
      bar(11, 10.5, 10.8, 10.45, 10.7, 2000), // confirms bar10 (10.4) as a swing low — AT/BELOW the running pullback low, so NOT a higher low
    ];
    const r = evaluate({ candles: bars, tfMs: 60_000, nowMs: bars[bars.length - 1]!.ts + 60_000, params: { ...DEFAULT_PARAMS, ...ABCD_PARAMS } });
    expect(r.events.map((e) => e.kind)).not.toContain('higherLow');
    expect(r.levels.higherLow).toBeNull();
    const breakdown = r.events.find((e) => e.kind === 'breakdown')!;
    expect(breakdown).toBeDefined();
    expect(breakdown.marker).toBe('sell');
    expect(r.state).toBe('short');
  });
});

describe('evaluate — T22 long stop (addendum 3)', () => {
  function buildBars(extra: Candle): Candle[] {
    return [
      bar(0, 10, 10.05, 9.95, 10, 20000),
      bar(1, 10, 12, 9.9, 11.9, 3000),
      bar(2, 11.9, 11.95, 11.4, 11.5, 2000),
      bar(3, 11.5, 11.6, 11.0, 11.1, 2000),
      bar(4, 11.1, 11.3, 10.8, 11.2, 2000),
      bar(5, 11.2, 11.6, 11.1, 11.5, 2000),
      bar(6, 11.5, 11.8, 11.45, 11.7, 2000),
      bar(7, 11.7, 11.75, 11.3, 11.35, 2000),
      bar(8, 11.35, 11.4, 11.25, 11.3, 2000),
      bar(9, 11.3, 11.32, 11.10, 11.15, 2000),
      bar(10, 11.15, 11.25, 11.12, 11.22, 2000), // entryHL 'LONG (HL)', D=11.10, stop=D*0.99=10.989
      extra,
    ];
  }

  it("a close below D*0.99 -> longStopped, long.state 'none', higherLow cleared", () => {
    const bars = buildBars(bar(11, 11.22, 11.25, 10.9, 10.95, 2000)); // close 10.95 < 10.989
    const r = evaluate({ candles: bars, tfMs: 60_000, nowMs: bars[bars.length - 1]!.ts + 60_000, params: { ...DEFAULT_PARAMS, ...ABCD_PARAMS } });
    const stopped = r.events.find((e) => e.kind === 'longStopped')!;
    expect(stopped).toBeDefined();
    expect(stopped.label).toBe('STOP (LONG)');
    expect(stopped.price).toBeCloseTo(11.1 * 0.99, 6);
    expect(stopped.marker).toBe('sell');
    expect(r.long.state).toBe('none');
    expect(r.levels.higherLow).toBeNull();
  });

  it('stopByClose:false — a low that touches the stop stops it even though the close does not; stopByClose:true does not', () => {
    const bars = buildBars(bar(11, 11.22, 11.3, 10.85, 11.0, 2000)); // low 10.85 <= 10.989, close 11.0 stays above it
    const withClose = evaluate({ candles: bars, tfMs: 60_000, nowMs: bars[bars.length - 1]!.ts + 60_000, params: { ...DEFAULT_PARAMS, ...ABCD_PARAMS, stopByClose: true } });
    expect(withClose.events.map((e) => e.kind)).not.toContain('longStopped');
    expect(withClose.long.state).toBe('long');
    const withLow = evaluate({ candles: bars, tfMs: 60_000, nowMs: bars[bars.length - 1]!.ts + 60_000, params: { ...DEFAULT_PARAMS, ...ABCD_PARAMS, stopByClose: false } });
    const stopped = withLow.events.find((e) => e.kind === 'longStopped')!;
    expect(stopped).toBeDefined();
    expect(withLow.long.state).toBe('none');
  });

  it('stopByClose:false — also stops on a FORMING bar (the low is monotonic, safe to check early)', () => {
    const extra = bar(11, 11.22, 11.3, 10.85, 11.0, 2000);
    const bars = buildBars(extra);
    const forming = evaluate({ candles: bars, tfMs: 60_000, nowMs: extra.ts + 1_000, params: { ...DEFAULT_PARAMS, ...ABCD_PARAMS, stopByClose: false } });
    expect(forming.events.map((e) => e.kind)).toContain('longStopped');
    const formingClose = evaluate({ candles: bars, tfMs: 60_000, nowMs: extra.ts + 1_000, params: { ...DEFAULT_PARAMS, ...ABCD_PARAMS, stopByClose: true } });
    expect(formingClose.events.map((e) => e.kind)).not.toContain('longStopped');
  });
});

describe('evaluate — T23 VWAP hold while long (addendum 3)', () => {
  it("a dip that touches VWAP and closes back above it -> vwapHold 'VWAP HOLD' once; the immediately following dip-and-close-above bar does NOT repeat (the prior bar's own low was already at/under VWAP, so it is not a 'first touch')", () => {
    // stopBufferPct:3 (wider than the default 1) so the long's stop sits
    // clearly BELOW where VWAP is by this point in the fixture — otherwise
    // a dip that reaches VWAP would trip the stop first, and this test
    // could never isolate the HOLD rule. Base bar 0's volume (9000, not the
    // ABCD fixture's 20000) is tuned so VWAP settles between the stop and D
    // — see the probe this was built from in the task notes.
    const bars: Candle[] = [
      bar(0, 10, 10.05, 9.95, 10, 9000),
      bar(1, 10, 12, 9.9, 11.9, 3000),
      bar(2, 11.9, 11.95, 11.4, 11.5, 2000),
      bar(3, 11.5, 11.6, 11.0, 11.1, 2000),
      bar(4, 11.1, 11.3, 10.8, 11.2, 2000),
      bar(5, 11.2, 11.6, 11.1, 11.5, 2000),
      bar(6, 11.5, 11.8, 11.45, 11.7, 2000),
      bar(7, 11.7, 11.75, 11.3, 11.35, 2000),
      bar(8, 11.35, 11.4, 11.25, 11.3, 2000),
      bar(9, 11.3, 11.32, 11.10, 11.15, 2000),
      bar(10, 11.15, 11.25, 11.12, 11.22, 2000), // entryHL, D=11.10, stop=11.10*0.97=10.767
      bar(11, 11.22, 11.25, 10.85, 10.98, 2000), // dips to touch VWAP (~10.94), closes above -> vwapHold
      bar(12, 10.98, 11.05, 10.9, 11.0, 2000), // dips again, closes above too, but bar11's own low was already <= VWAP -> no repeat
    ];
    const r = evaluate({ candles: bars, tfMs: 60_000, nowMs: bars[bars.length - 1]!.ts + 60_000, params: { ...DEFAULT_PARAMS, ...ABCD_PARAMS, stopBufferPct: 3 } });
    const holds = r.events.filter((e) => e.kind === 'vwapHold');
    expect(holds).toHaveLength(1);
    expect(holds[0]!.label).toBe('VWAP HOLD');
    expect(holds[0]!.marker).toBe('buy');
    expect(fmtEt(holds[0]!.ts)).toBe(fmtEt(bars[11]!.ts));
    expect(r.long.state).toBe('long'); // never stopped
    expect(r.levels.longStop).toBeCloseTo(11.1 * 0.97, 6); // unchanged — a hold never moves the stop
  });
});

describe('evaluate — T24 real ATYR: the long side stays silent all day (addendum 3)', () => {
  it('no higherLow/entryHL/breakout/vwapHold today; every event carries a side', () => {
    const candles = (atyrFixture.bars as Bar[]).map(toCandle);
    const nowMs = candles[candles.length - 1]!.ts + 10 * 60_000;
    const r = evaluate({ candles, tfMs: 60_000, nowMs, params: DEFAULT_PARAMS });
    const today = todayEvents(r.events, '2026-09-24');
    const longToday = today.filter((e) => e.side === 'long');
    console.log('ATYR long-side today events:', longToday.length ? longToday.map((e) => `${fmtEt(e.ts)} ${e.kind} ${e.label} ${fmtPx(e.price)}`).join(' | ') : '(none)');
    expect(longToday).toEqual([]);
    expect(r.long.state).toBe('none');
    for (const e of r.events) {
      expect(e.side === 'short' || e.side === 'long').toBe(true);
    }
  });
});

describe('evaluate — T25 real gappers: the long side never throws, stays append-only (addendum 3)', () => {
  const FIXTURES: Record<string, { bars: Bar[] }> = {
    gctk: gctkFixture as typeof gctkFixture & { bars: Bar[] },
    weto: wetoFixture as typeof wetoFixture & { bars: Bar[] },
    sphl: sphlFixture as typeof sphlFixture & { bars: Bar[] },
    dcx: dcxFixture as typeof dcxFixture & { bars: Bar[] },
  };

  it('no throw; console.log each fixture\'s long-side event list (GCTK/WETO/SPHL are quiet on the long side, DCX has real long-side activity)', () => {
    for (const [sym, fx] of Object.entries(FIXTURES)) {
      const candles = fx.bars.map(toCandle);
      const nowMs = candles[candles.length - 1]!.ts + 10 * 60_000;
      expect(() => evaluate({ candles, tfMs: 60_000, nowMs, params: DEFAULT_PARAMS })).not.toThrow();
      const r = evaluate({ candles, tfMs: 60_000, nowMs, params: DEFAULT_PARAMS });
      const longToday = todayEvents(r.events, '2026-09-24').filter((e) => e.side === 'long');
      console.log(`${sym.toUpperCase()} long-side today events:`, longToday.length ? longToday.map((e) => `${fmtEt(e.ts)} ${e.kind} ${e.label} ${fmtPx(e.price)}`).join(' | ') : '(none)');
    }
  });

  // Same 60 s budget as T14's replay (review window:F7).
  it('append-only under a bar-by-bar replay, BOTH sides, for all four symbols (extends T14\'s harness)', () => {
    for (const fx of Object.values(FIXTURES)) expectAppendOnlyReplay(fx.bars.map(toCandle), DEFAULT_PARAMS);
  }, 60_000);
});


// ─── Addendum 5 — T26-T30, the adds are capped (iteration 5) ─────────────
//
// Three rules under test, all of them about NOT scaling in on every wiggle:
//   1. `maxAdds` — at most this many ADD signals per episode, per side. The
//      short side counts 'ADD (LH)' AND the add form of 'VWAP REJECT'; the
//      long side counts 'ADD (HL)' AND the add form of 'VWAP HOLD'. Entries
//      and the one-per-episode BREAKDOWN/BREAKOUT never count.
//   2. 'ADD (LH)' / 'ADD (HL)' are refused once the first target is behind
//      them (`!t1Hit` / `!t1Long`) and when their own risk to the stop,
//      measured from the confirming close, exceeds `maxAddRiskPct`.
//   3. A VWAP REJECT/HOLD that fired stays spent until a CLOSE 2 x
//      `vwapBandPct` beyond VWAP (under it for a reject, over it for a hold).
// A refused add loses only its marker: the list-only `lowerHigh`/`higherLow`
// still prints and the stop bookkeeping still runs.

const SHORT_ENTRY_LABELS = new Set(['SHORT (LH)', 'SHORT', 'SHORT (VWAP)', 'SHORT (VWAP REJECT)']);
const LONG_ENTRY_LABELS = new Set(['LONG (HL)', 'LONG', 'LONG (VWAP HOLD)']);

interface Episode {
  side: 'short' | 'long';
  entry: SignalEvent;
  adds: SignalEvent[]; // ADD-type markers, in emission order
  addsAfterT1: SignalEvent[]; // the subset of `adds` that are 'ADD (LH)' / 'ADD (HL)' printed after the episode's first target
}

// Splits a day's MARKERS (emission order) into per-side episodes: an entry
// label opens one, its stop / invalidating new HOD closes it. Counted adds
// are 'ADD (LH)' + 'VWAP REJECT' (short) and 'ADD (HL)' + 'VWAP HOLD'
// (long) — the plain entries, BREAKDOWN and BREAKOUT are not adds. The
// first target that gates an 'ADD' is T1 VWAP (short; an episode opened by
// 'SHORT (VWAP REJECT)' starts already past it) and the breakout / T1 BOUNCE
// HIGH (long; a 'LONG' breakout entry starts past it).
function episodesOf(events: SignalEvent[]): Episode[] {
  const out: Episode[] = [];
  let shortEp: Episode | null = null;
  let shortT1 = false;
  let longEp: Episode | null = null;
  let longT1 = false;
  for (const e of events) {
    if (!e.marker) continue;
    if (e.side === 'short') {
      if (SHORT_ENTRY_LABELS.has(e.label)) {
        shortEp = { side: 'short', entry: e, adds: [], addsAfterT1: [] };
        shortT1 = e.label === 'SHORT (VWAP REJECT)';
        out.push(shortEp);
      } else if (e.kind === 'stopped' || e.kind === 'invalidated') {
        shortEp = null;
      } else if (shortEp) {
        if (e.label === 'T1 VWAP') shortT1 = true;
        else if (e.label === 'ADD (LH)') {
          shortEp.adds.push(e);
          if (shortT1) shortEp.addsAfterT1.push(e);
        } else if (e.label === 'VWAP REJECT') shortEp.adds.push(e);
      }
    } else if (LONG_ENTRY_LABELS.has(e.label)) {
      longEp = { side: 'long', entry: e, adds: [], addsAfterT1: [] };
      longT1 = e.kind === 'breakout';
      out.push(longEp);
    } else if (e.kind === 'longStopped' || (e.kind === 'longTarget' && e.label === 'NEW HOD')) {
      longEp = null;
    } else if (longEp) {
      if (e.kind === 'breakout' || e.label === 'T1 BOUNCE HIGH') longT1 = true;
      else if (e.label === 'ADD (HL)') {
        longEp.adds.push(e);
        if (longT1) longEp.addsAfterT1.push(e);
      } else if (e.label === 'VWAP HOLD') longEp.adds.push(e);
    }
  }
  return out;
}

// The same cumulative session VWAP the engine uses, recomputed here from the
// candles so the re-arm scans below don't trust the engine's own arithmetic.
function vwapSeries(candles: Candle[]): number[] {
  const out: number[] = [];
  let pv = 0;
  let v = 0;
  for (const c of candles) {
    pv += ((c.high + c.low + c.close) / 3) * c.volume;
    v += c.volume;
    out.push(v > 0 ? pv / v : NaN);
  }
  return out;
}

function markerLine(e: SignalEvent): string {
  return `${fmtEt(e.ts)} ${e.side[0]}/${e.marker} ${e.label} ${fmtPx(e.price)}`;
}

const lastDetail = (d: string[]): string => d[d.length - 1]!;
const minutesOf = (events: SignalEvent[]): number[] => events.map((e) => (e.ts - ANCHOR) / 60_000);

describe('evaluate — T26 real ATYR: the add caps on the real top (addendum 5)', () => {
  const candles = (atyrFixture.bars as Bar[]).map(toCandle);
  const nowMs = candles[candles.length - 1]!.ts + 10 * 60_000;
  const run = (params: Partial<SignalParams>) => {
    const r = evaluate({ candles, tfMs: 60_000, nowMs, params: { ...DEFAULT_PARAMS, ...params } });
    return { r, today: todayEvents(r.events, '2026-09-24') };
  };
  const { r, today } = run({});
  const shortMarkers = today.filter((e) => e.marker && e.side === 'short');

  it('no ADD (LH) after 06:34 (!t1Hit — T1 VWAP printed at 06:34); the 07:03 VWAP REJECT still fires (the second counted add)', () => {
    console.log('ATYR markers:', shortMarkers.map(markerLine).join(' | '));
    expect(shortMarkers.filter((e) => e.label === 'ADD (LH)' && fmtEt(e.ts) > '06:34')).toEqual([]);
    const reject = shortMarkers.find((e) => e.kind === 'vwapReject')!;
    expect(reject.label).toBe('VWAP REJECT');
    expect(fmtEt(reject.ts)).toBe('07:03');
  });

  it("ADD-type markers before 06:34: the RULES produce ONE (06:27), not the addendum's expected two (06:27, 06:32) — the 06:32 add is a 15.3%-risk add", () => {
    // DIFFERENCE FROM ADDENDUM 5 (reported, not tuned away): the 06:30 lower
    // high (0.6800) moves the stop up to 0.6868 (0.68 x 1.01) and the 06:32
    // confirming close is 0.5958, so the add's risk is (0.6868 - 0.5958) /
    // 0.5958 = 15.3% > maxAddRiskPct 12 (13.1% against the pre-move stop
    // 0.6737 — over the gate either way). 06:27 (close 0.6324, stop 0.6737)
    // is 6.5% and passes.
    const before = shortMarkers.filter((e) => (e.label === 'ADD (LH)' || e.label === 'VWAP REJECT') && fmtEt(e.ts) < '06:34');
    expect(before.map((e) => fmtEt(e.ts))).toEqual(['06:27']);
    expect(before[0]!.price).toBeCloseTo(0.6324, 4);
  });

  it("the refused 06:32 add still leaves its list-only lowerHigh (ts 06:30, 0.6800) and the stop still moved up to it (0.6868) — bookkeeping runs regardless", () => {
    const lh = today.filter((e) => e.kind === 'lowerHigh').find((e) => fmtEt(e.ts) === '06:30')!;
    expect(lh).toBeDefined();
    expect(lh.marker).toBeUndefined();
    expect(lh.price).toBeCloseTo(0.68, 4);
    expect(r.levels.stop).toBeCloseTo(0.68 * 1.01, 4);
  });

  it("with the risk gate lifted (maxAddRiskPct 100, maxAdds 5) the addendum's expected pair returns — 06:27 and 06:32 — while 07:05 stays refused by !t1Hit alone", () => {
    const { today: lax } = run({ maxAdds: 5, maxAddRiskPct: 100 });
    const adds = lax.filter((e) => e.marker && e.label === 'ADD (LH)');
    expect(adds.map((e) => fmtEt(e.ts))).toEqual(['06:27', '06:32']);
    expect(adds[1]!.price).toBeCloseTo(0.5958, 4);
    expect(lax.filter((e) => fmtEt(e.ts) === '07:05' && e.marker)).toEqual([]);
  });

  it('VWAP REJECT counts against the SAME cap as ADD (LH): maxAdds 1 -> the 06:27 add uses it up and the 07:03 reject is refused; maxAdds 0 -> neither', () => {
    const one = run({ maxAdds: 1 });
    expect(one.today.filter((e) => e.marker && (e.label === 'ADD (LH)' || e.label === 'VWAP REJECT')).map((e) => fmtEt(e.ts))).toEqual(['06:27']);
    expect(lastDetail(one.r.short.detail)).toBe('Adds used 1/1 — no more adds this trade (a new trade after a stop can still open on a VWAP rejection)');
    const zero = run({ maxAdds: 0 });
    expect(zero.today.filter((e) => e.marker && (e.label === 'ADD (LH)' || e.label === 'VWAP REJECT'))).toEqual([]);
    // the entry and the one-per-episode confirmations are NOT adds — untouched
    expect(zero.today.filter((e) => e.marker && e.side === 'short').map((e) => `${fmtEt(e.ts)} ${e.label}`)).toEqual(['06:23 SHORT (LH)', '06:23 BREAKDOWN', '06:34 T1 VWAP']);
  });

  it("the short detail's last line says the cap is reached, with the real param values (default: 06:27 add + 07:03 reject = 2/2)", () => {
    expect(lastDetail(r.short.detail)).toBe('Adds used 2/2 — no more adds this trade (a new trade after a stop can still open on a VWAP rejection)');
    expect(r.short.detail).toHaveLength(4);
  });
});

describe('evaluate — T27 real ATYR/GCTK/WETO/SPHL/DCX: the cap holds per episode and per side; VWAP REJECT/HOLD re-arm (addendum 5)', () => {
  const FIXTURES: Record<string, { bars: Bar[] }> = {
    atyr: atyrFixture as typeof atyrFixture & { bars: Bar[] },
    gctk: gctkFixture as typeof gctkFixture & { bars: Bar[] },
    weto: wetoFixture as typeof wetoFixture & { bars: Bar[] },
    sphl: sphlFixture as typeof sphlFixture & { bars: Bar[] },
    dcx: dcxFixture as typeof dcxFixture & { bars: Bar[] },
  };
  const evalToday = (fx: { bars: Bar[] }, params: Partial<SignalParams>) => {
    const all = fx.bars.map(toCandle);
    const nowMs = all[all.length - 1]!.ts + 10 * 60_000;
    const r = evaluate({ candles: all, tfMs: 60_000, nowMs, params: { ...DEFAULT_PARAMS, ...params } });
    return { r, todayEv: todayEvents(r.events, '2026-09-24'), todayBars: all.filter((c) => etDayKey(c.ts) === '2026-09-24') };
  };

  it('per short episode and per long episode, ADD-type markers <= maxAdds (checked at maxAdds 0, 1, 2, 3); no ADD (LH) after T1 VWAP, no ADD (HL) after the breakout / T1 bounce high; marker lists logged at the default', () => {
    for (const [sym, fx] of Object.entries(FIXTURES)) {
      const def = evalToday(fx, {});
      console.log(`${sym.toUpperCase()} today markers (default maxAdds ${DEFAULT_PARAMS.maxAdds}):`, def.todayEv.filter((e) => e.marker).map(markerLine).join(' | '));
      for (const maxAdds of [0, 1, 2, 3]) {
        const { todayEv } = evalToday(fx, { maxAdds });
        const eps = episodesOf(todayEv);
        expect(eps.length).toBeGreaterThan(0);
        for (const ep of eps) {
          expect(ep.adds.length, `${sym} ${ep.side} episode opened ${markerLine(ep.entry)} at maxAdds ${maxAdds}`).toBeLessThanOrEqual(maxAdds);
          expect(ep.addsAfterT1.map(markerLine), `${sym} ${ep.side} episode opened ${markerLine(ep.entry)}: adds after its first target`).toEqual([]);
        }
      }
    }
  });

  it('no two VWAP REJECTs without a close 2 x vwapBandPct UNDER VWAP strictly between them, and no two VWAP HOLDs without one OVER it — at the default AND with the cap lifted (so the re-arm, not the cap, is what is being tested)', () => {
    const band2 = (2 * DEFAULT_PARAMS.vwapBandPct) / 100;
    for (const [sym, fx] of Object.entries(FIXTURES)) {
      for (const params of [{}, { maxAdds: 50, maxAddRiskPct: 1000 }] as Partial<SignalParams>[]) {
        const { todayEv, todayBars } = evalToday(fx, params);
        const vwap = vwapSeries(todayBars);
        const idxOf = (e: SignalEvent) => todayBars.findIndex((c) => c.ts === e.ts);
        const rejects = todayEv.filter((e) => e.kind === 'vwapReject');
        for (let k = 1; k < rejects.length; k++) {
          const a = idxOf(rejects[k - 1]!);
          const b = idxOf(rejects[k]!);
          let deep = false;
          for (let x = a + 1; x < b; x++) if (todayBars[x]!.close < vwap[x]! * (1 - band2)) deep = true;
          expect(deep, `${sym} ${JSON.stringify(params)}: VWAP REJECTs ${fmtEt(rejects[k - 1]!.ts)} and ${fmtEt(rejects[k]!.ts)} with no close ${DEFAULT_PARAMS.vwapBandPct * 2}% under VWAP between`).toBe(true);
        }
        const holds = todayEv.filter((e) => e.kind === 'vwapHold');
        for (let k = 1; k < holds.length; k++) {
          const a = idxOf(holds[k - 1]!);
          const b = idxOf(holds[k]!);
          let far = false;
          for (let x = a + 1; x < b; x++) if (todayBars[x]!.close > vwap[x]! * (1 + band2)) far = true;
          expect(far, `${sym} ${JSON.stringify(params)}: VWAP HOLDs ${fmtEt(holds[k - 1]!.ts)} and ${fmtEt(holds[k]!.ts)} with no close ${DEFAULT_PARAMS.vwapBandPct * 2}% over VWAP between`).toBe(true);
        }
      }
    }
  });

  it("real-data pin, long side: DCX's 04:49 'ADD (HL)' (0.1534, printed by the pre-cap engine) is refused — the 04:43 BREAKOUT already latched T1, so no add follows it", () => {
    const { todayEv } = evalToday(FIXTURES.dcx!, {});
    const long = todayEv.filter((e) => e.side === 'long' && e.marker);
    expect(long.some((e) => e.label === 'BREAKOUT' && fmtEt(e.ts) === '04:43')).toBe(true);
    expect(long.filter((e) => e.label === 'ADD (HL)')).toEqual([]);
    // ...and the 04:49 higher low itself is still on the list, marker-less
    expect(todayEv.some((e) => e.kind === 'higherLow' && fmtEt(e.ts) === '04:47')).toBe(true);
  });

  it("real-data pin, short side: GCTK's 05:00 and 05:09 'ADD (LH)' (after its 04:59 T1 VWAP) are refused; SPHL keeps no ADD (LH) at all after its 04:22 T1 — its two VWAP REJECTs (04:36, 05:31) are the adds", () => {
    const gctk = evalToday(FIXTURES.gctk!, {}).todayEv.filter((e) => e.marker && e.label === 'ADD (LH)');
    expect(gctk).toEqual([]); // the whole day: 04:08 (opened by a VWAP REJECT, already past T1), 05:00/05:09 (after 04:59), 06:54-07:10 (after T1)
    const sphl = evalToday(FIXTURES.sphl!, {}).todayEv.filter((e) => e.marker && e.side === 'short');
    expect(sphl.filter((e) => e.label === 'ADD (LH)')).toEqual([]);
    expect(sphl.filter((e) => e.label === 'VWAP REJECT').map((e) => fmtEt(e.ts))).toEqual(['04:36', '05:31']);
  });
});

// Synthetic fixtures for T28/T29 (1-minute, swingStrength 1, huge bar-0 volume
// so cumulative VWAP stays ~10.3-11.0 — far under every price these bars
// print — so neither the silent-lower-high rule nor a VWAP loss (T1) is ever
// in play; only the add rules are). Peaks/troughs are placed so each cycle
// prints exactly one confirmed swing high and one confirmed swing low.
//
// SHORT_ADDS: spike to HOD 12, crack, pullback low B=10.8, then FIVE lower
// highs: 11.8 (the SHORT (LH) entry, bar 7, stop 11.918), 11.7 (bar 11),
// 11.6 (bar 15), 11.5 (bar 19) and finally 11.9 (bar 23 — a HIGHER lower
// high, still under HOD, which moves the stop up to 12.019). Every add's
// confirming close is 11.25-11.45. `hlEntry:false` keeps the long side from
// entering (it is not under test here); assertions read the short side only.
function buildShortAddsBars(): Candle[] {
  return [
    bar(0, 10, 10.05, 9.95, 10, 20000),
    bar(1, 10, 12, 9.9, 11.9, 3000), // HOD 12
    bar(2, 11.9, 11.95, 11.4, 11.5, 2000), // crack
    bar(3, 11.5, 11.6, 11.0, 11.1, 2000),
    bar(4, 11.1, 11.3, 10.8, 11.2, 2000), // B = 10.8
    bar(5, 11.2, 11.6, 11.1, 11.5, 2000),
    bar(6, 11.5, 11.8, 11.45, 11.7, 2000), // LH1 = 11.8
    bar(7, 11.7, 11.75, 11.3, 11.35, 2000), // confirms LH1 -> SHORT (LH) at 11.35, stop 11.918
    bar(8, 11.35, 11.45, 11.15, 11.25, 2000),
    bar(9, 11.25, 11.55, 11.2, 11.5, 2000),
    bar(10, 11.5, 11.7, 11.45, 11.6, 2000), // LH2 = 11.7
    bar(11, 11.6, 11.62, 11.3, 11.35, 2000), // confirms LH2 -> ADD (LH) #1 at 11.35 (risk 5.0%)
    bar(12, 11.35, 11.45, 11.15, 11.25, 2000),
    bar(13, 11.25, 11.5, 11.2, 11.45, 2000),
    bar(14, 11.45, 11.6, 11.4, 11.5, 2000), // LH3 = 11.6
    bar(15, 11.5, 11.52, 11.2, 11.25, 2000), // confirms LH3 -> ADD (LH) #2 at 11.25 (risk 5.9%)
    bar(16, 11.25, 11.3, 11.1, 11.2, 2000),
    bar(17, 11.2, 11.45, 11.15, 11.4, 2000),
    bar(18, 11.4, 11.5, 11.35, 11.45, 2000), // LH4 = 11.5
    bar(19, 11.45, 11.46, 11.2, 11.25, 2000), // confirms LH4 -> ADD (LH) #3 at 11.25 (5.9%) — past the default cap
    bar(20, 11.25, 11.4, 11.2, 11.35, 2000),
    bar(21, 11.35, 11.75, 11.3, 11.7, 2000),
    bar(22, 11.7, 11.9, 11.6, 11.8, 2000), // LH5 = 11.9 (still < HOD 12)
    bar(23, 11.8, 11.85, 11.4, 11.45, 2000), // confirms LH5 -> ADD (LH) #4 at 11.45; stop moves up to 12.019 first (risk 5.0%)
  ];
}

// LONG_ADDS: the ABCD fixture's first eleven bars (LONG (HL) at bar 10, D1
// = 11.10, stop 10.989 and it NEVER moves), then three more higher lows
// (11.20 / 11.15 / 11.12, confirmed at bars 15 / 19 / 23) between peaks that
// stay under the previous bounce high, so no breakout ever latches T1.
// `lhEntry:false` keeps the short side out; assertions read the long side.
function buildLongAddsBars(): Candle[] {
  return [
    bar(0, 10, 10.05, 9.95, 10, 20000),
    bar(1, 10, 12, 9.9, 11.9, 3000),
    bar(2, 11.9, 11.95, 11.4, 11.5, 2000),
    bar(3, 11.5, 11.6, 11.0, 11.1, 2000),
    bar(4, 11.1, 11.3, 10.8, 11.2, 2000),
    bar(5, 11.2, 11.6, 11.1, 11.5, 2000),
    bar(6, 11.5, 11.8, 11.45, 11.7, 2000), // C = 11.8
    bar(7, 11.7, 11.75, 11.3, 11.35, 2000),
    bar(8, 11.35, 11.4, 11.25, 11.3, 2000),
    bar(9, 11.3, 11.32, 11.1, 11.15, 2000), // D1 = 11.10
    bar(10, 11.15, 11.25, 11.12, 11.22, 2000), // confirms D1 -> LONG (HL) at 11.22
    bar(11, 11.22, 11.5, 11.2, 11.45, 2000),
    bar(12, 11.45, 11.7, 11.4, 11.6, 2000),
    bar(13, 11.6, 11.62, 11.3, 11.35, 2000),
    bar(14, 11.35, 11.45, 11.2, 11.3, 2000), // D2 = 11.20
    bar(15, 11.3, 11.55, 11.25, 11.5, 2000), // confirms D2 -> ADD (HL) #1 at 11.50 (risk 4.4%)
    bar(16, 11.5, 11.65, 11.45, 11.6, 2000),
    bar(17, 11.6, 11.62, 11.3, 11.35, 2000),
    bar(18, 11.35, 11.4, 11.15, 11.25, 2000), // D3 = 11.15
    bar(19, 11.25, 11.5, 11.2, 11.45, 2000), // confirms D3 -> ADD (HL) #2 at 11.45 (4.0%)
    bar(20, 11.45, 11.6, 11.4, 11.5, 2000),
    bar(21, 11.5, 11.52, 11.3, 11.35, 2000),
    bar(22, 11.35, 11.4, 11.12, 11.2, 2000), // D4 = 11.12
    bar(23, 11.2, 11.45, 11.18, 11.4, 2000), // confirms D4 -> ADD (HL) #3 at 11.40 (3.6%) — past the default cap
  ];
}

describe('evaluate — T28 synthetic: maxAdds caps the adds per episode, per side; entries unaffected (addendum 5)', () => {
  const shortBars = buildShortAddsBars();
  const longBars = buildLongAddsBars();
  const runShort = (params: Partial<SignalParams>) =>
    evaluate({ candles: shortBars, tfMs: 60_000, nowMs: shortBars[shortBars.length - 1]!.ts + 60_000, params: { ...DEFAULT_PARAMS, ...ABCD_PARAMS, hlEntry: false, ...params } });
  const runLong = (params: Partial<SignalParams>) =>
    evaluate({ candles: longBars, tfMs: 60_000, nowMs: longBars[longBars.length - 1]!.ts + 60_000, params: { ...DEFAULT_PARAMS, ...ABCD_PARAMS, lhEntry: false, ...params } });
  const shortAdds = (r: ReturnType<typeof evaluate>) => r.events.filter((e) => e.side === 'short' && e.label === 'ADD (LH)');
  const longAdds = (r: ReturnType<typeof evaluate>) => r.events.filter((e) => e.side === 'long' && e.label === 'ADD (HL)');

  it('short, default maxAdds 2: the first two later lower highs add (bars 11, 15); the third and fourth (bars 19, 23) print list-only; the stop still moves up to the bar-22 lower high; the detail says the cap is reached', () => {
    const r = runShort({});
    expect(minutesOf(shortAdds(r))).toEqual([11, 15]);
    expect(r.events.filter((e) => e.kind === 'lowerHigh')).toHaveLength(5); // every lower high is still announced
    expect(r.levels.stop).toBeCloseTo(11.9 * 1.01, 6); // stop bookkeeping runs after the cap
    expect(lastDetail(r.short.detail)).toBe('Adds used 2/2 — no more adds this trade (a new trade after a stop can still open on a VWAP rejection)');
  });

  it('short, maxAdds 0: no adds at all, the SHORT (LH) entry (bar 7, close 11.35) untouched', () => {
    const r = runShort({ maxAdds: 0 });
    expect(shortAdds(r)).toEqual([]);
    const entry = r.events.find((e) => e.kind === 'entryLH')!;
    expect(entry.label).toBe('SHORT (LH)');
    expect(entry.price).toBeCloseTo(11.35, 6);
    expect(minutesOf([entry])).toEqual([7]);
    expect(lastDetail(r.short.detail)).toBe('Adds used 0/0 — no more adds this trade (a new trade after a stop can still open on a VWAP rejection)');
  });

  it('short, maxAdds 5 with maxAddRiskPct 100: all four later lower highs add (bars 11, 15, 19, 23) — the count the pre-cap engine printed for this fixture — and no cap line appears', () => {
    const r = runShort({ maxAdds: 5, maxAddRiskPct: 100 });
    expect(minutesOf(shortAdds(r))).toEqual([11, 15, 19, 23]);
    expect(r.short.detail).toHaveLength(3);
    expect(r.short.detail.join(' ')).not.toContain('Adds used');
  });

  it('long, default maxAdds 2: the first two higher lows add (bars 15, 19); the third (bar 23) prints list-only; the detail says "Adds used 2/2"', () => {
    const r = runLong({});
    expect(minutesOf(longAdds(r))).toEqual([15, 19]);
    expect(r.events.filter((e) => e.kind === 'higherLow')).toHaveLength(4);
    expect(r.levels.longStop).toBeCloseTo(11.1 * 0.99, 6); // the long stop never moves, capped or not
    expect(lastDetail(r.long.detail)).toBe('Adds used 2/2');
  });

  it('long, maxAdds 0: no adds at all, the LONG (HL) entry (bar 10, close 11.22) untouched', () => {
    const r = runLong({ maxAdds: 0 });
    expect(longAdds(r)).toEqual([]);
    const entry = r.events.find((e) => e.kind === 'entryHL')!;
    expect(entry.label).toBe('LONG (HL)');
    expect(entry.price).toBeCloseTo(11.22, 6);
    expect(lastDetail(r.long.detail)).toBe('Adds used 0/0');
  });

  it('long, maxAdds 5 with maxAddRiskPct 100: all three later higher lows add (bars 15, 19, 23) — the pre-cap count', () => {
    const r = runLong({ maxAdds: 5, maxAddRiskPct: 100 });
    expect(minutesOf(longAdds(r))).toEqual([15, 19, 23]);
    expect(r.long.detail).toHaveLength(3);
  });

  it('the allowance is per EPISODE: a stop closes the short episode (bar 16) and the fresh SHORT (LH) it is followed by (bar 20) gets its own adds — cap not carried over (bars 11, 15 | 24)', () => {
    // SHORT_ADDS' first sixteen bars use up the default cap (adds at 11 and
    // 15), then bar 16 closes above the 11.918 stop (11.95, high == HOD so
    // no new HOD) and a fresh cycle prints a new lower high (11.85, bar 19,
    // confirmed 20 -> SHORT (LH)) and a lower one (11.75, confirmed 24).
    const bars = [
      ...shortBars.slice(0, 16),
      bar(16, 11.25, 12.0, 11.2, 11.95, 2000),
      bar(17, 11.95, 11.96, 11.6, 11.65, 2000),
      bar(18, 11.65, 11.7, 11.5, 11.55, 2000),
      bar(19, 11.55, 11.85, 11.5, 11.8, 2000),
      bar(20, 11.8, 11.82, 11.5, 11.55, 2000),
      bar(21, 11.55, 11.6, 11.3, 11.35, 2000),
      bar(22, 11.35, 11.7, 11.3, 11.65, 2000),
      bar(23, 11.65, 11.75, 11.6, 11.7, 2000),
      bar(24, 11.7, 11.72, 11.4, 11.45, 2000),
    ];
    const r = evaluate({ candles: bars, tfMs: 60_000, nowMs: bars[bars.length - 1]!.ts + 60_000, params: { ...DEFAULT_PARAMS, ...ABCD_PARAMS, hlEntry: false } });
    const short = r.events.filter((e) => e.side === 'short' && e.marker);
    expect(short.map((e) => `${(e.ts - ANCHOR) / 60_000} ${e.label}`)).toEqual(['7 SHORT (LH)', '11 ADD (LH)', '15 ADD (LH)', '16 STOP', '20 SHORT (LH)', '24 ADD (LH)']);
    expect(r.levels.stop).toBeCloseTo(11.85 * 1.01, 6); // the second episode's own stop, based on ITS lower high
  });

  it('a new HOD ends the episode too: after SHORT_ADDS uses up the cap, a bar above HOD (bar 16, invalidated) starts a fresh setup whose SHORT (LH) (bar 21) can add again (bars 11, 15 | 25)', () => {
    const bars = [
      ...shortBars.slice(0, 16),
      bar(16, 11.25, 12.3, 11.2, 12.2, 2000), // new HOD 12.3
      bar(17, 12.2, 12.25, 11.7, 11.75, 2000), // crack (4.9% off the new high)
      bar(18, 11.75, 11.8, 11.5, 11.55, 2000),
      bar(19, 11.55, 11.9, 11.55, 11.85, 2000), // confirms the 11.5 swing low -> bounce
      bar(20, 11.85, 12.05, 11.8, 12.0, 2000), // LH = 12.05 (< new HOD)
      bar(21, 12.0, 12.0, 11.6, 11.65, 2000), // confirms it -> SHORT (LH) at 11.65
      bar(22, 11.65, 11.7, 11.4, 11.45, 2000),
      bar(23, 11.45, 11.9, 11.4, 11.85, 2000),
      bar(24, 11.85, 11.95, 11.8, 11.9, 2000), // LH = 11.95
      bar(25, 11.9, 11.92, 11.6, 11.65, 2000), // confirms it -> ADD (LH), a FIRST add of this episode (risk 4.5%); the BREAKDOWN at bar 22 is not an add and uses no allowance
    ];
    const r = evaluate({ candles: bars, tfMs: 60_000, nowMs: bars[bars.length - 1]!.ts + 60_000, params: { ...DEFAULT_PARAMS, ...ABCD_PARAMS, hlEntry: false } });
    const short = r.events.filter((e) => e.side === 'short' && e.marker);
    expect(short.map((e) => `${(e.ts - ANCHOR) / 60_000} ${e.label}`)).toEqual(['7 SHORT (LH)', '11 ADD (LH)', '15 ADD (LH)', '16 NEW HOD', '21 SHORT (LH)', '22 BREAKDOWN', '25 ADD (LH)']);
  });

  it('...and per LONG episode: a long stop (bar 24, close 10.95 < 10.989) closes it and the next LONG (HL) (bar 30) can add again (bars 15, 19 | 34)', () => {
    const bars = [
      ...longBars,
      bar(24, 11.4, 11.42, 10.9, 10.95, 2000),
      bar(25, 10.95, 11.3, 10.95, 11.25, 2000),
      bar(26, 11.25, 11.5, 11.2, 11.45, 2000),
      bar(27, 11.45, 11.6, 11.4, 11.5, 2000),
      bar(28, 11.5, 11.52, 11.3, 11.35, 2000),
      bar(29, 11.35, 11.4, 11.15, 11.2, 2000),
      bar(30, 11.2, 11.45, 11.18, 11.4, 2000),
      bar(31, 11.4, 11.55, 11.35, 11.5, 2000),
      bar(32, 11.5, 11.53, 11.4, 11.45, 2000),
      bar(33, 11.45, 11.5, 11.25, 11.3, 2000),
      bar(34, 11.3, 11.5, 11.28, 11.45, 2000),
    ];
    const r = evaluate({ candles: bars, tfMs: 60_000, nowMs: bars[bars.length - 1]!.ts + 60_000, params: { ...DEFAULT_PARAMS, ...ABCD_PARAMS, lhEntry: false } });
    const long = r.events.filter((e) => e.side === 'long' && e.marker);
    expect(long.map((e) => `${(e.ts - ANCHOR) / 60_000} ${e.label}`)).toEqual(['10 LONG (HL)', '15 ADD (HL)', '19 ADD (HL)', '24 STOP (LONG)', '30 LONG (HL)', '34 ADD (HL)']);
  });
});

describe('evaluate — T28b synthetic: a VWAP REJECT / VWAP HOLD re-arms only after a close 2 x vwapBandPct beyond VWAP (addendum 5)', () => {
  // SHORT: T17's spike -> crack -> monotonic decline -> VWAP lost, then a pop
  // to VWAP that closes under it opens the episode ('SHORT (VWAP REJECT)',
  // bar 7, VWAP ~5.10). Bar 8 then closes either only 0.8% under VWAP
  // (5.06 — not the 1% the re-arm needs) or 6% under it (4.78), and bar 9 is
  // the SAME bar in both variants: it touches VWAP (high 5.11), closes under
  // it (5.07) and follows a bar whose high was under VWAP (the untested-from-
  // below leg) — so the only thing that differs is whether price left the line.
  const head: Candle[] = [
    bar(0, 5.0, 5.02, 4.98, 5.0, 500),
    bar(1, 5.0, 6.0, 5.0, 5.95, 3000), // HOD 6.0, arms
    bar(2, 5.95, 5.96, 5.5, 5.55, 3000), // crack
    bar(3, 5.55, 5.56, 5.3, 5.32, 8000),
    bar(4, 5.32, 5.33, 5.1, 5.12, 10000),
    bar(5, 5.12, 5.13, 4.9, 4.92, 15000),
    bar(6, 4.92, 4.93, 4.75, 4.8, 15000), // VWAP LOST
    bar(7, 4.8, 5.15, 4.78, 4.95, 3000), // 'SHORT (VWAP REJECT)'
  ];
  const retest = bar(9, 5.0, 5.11, 4.96, 5.07, 1500);
  const near = [...head, bar(8, 4.95, 5.09, 5.04, 5.06, 1500), retest]; // bar 8 closes 5.06: within 1% of VWAP 5.1028
  const far = [...head, bar(8, 4.95, 4.96, 4.75, 4.78, 1500), retest]; // bar 8 closes 4.78: ~6% under VWAP 5.0969
  const runShort = (bars: Candle[], params: Partial<SignalParams>) =>
    evaluate({ candles: bars, tfMs: 60_000, nowMs: bars[bars.length - 1]!.ts + 60_000, params: { ...DEFAULT_PARAMS, ...SIMPLE, swingStrength: 2, ...params } });

  it('no close 1% under VWAP since the first reject -> the retest at bar 9 is the same test again: exactly one vwapReject, the entry', () => {
    const rejects = runShort(near, {}).events.filter((e) => e.kind === 'vwapReject');
    expect(rejects.map((e) => e.label)).toEqual(['SHORT (VWAP REJECT)']);
  });

  it('a close well under VWAP between them re-arms it -> the bar-9 retest fires as the add form, label VWAP REJECT', () => {
    const rejects = runShort(far, {}).events.filter((e) => e.kind === 'vwapReject');
    expect(rejects.map((e) => e.label)).toEqual(['SHORT (VWAP REJECT)', 'VWAP REJECT']);
    expect(minutesOf(rejects)).toEqual([7, 9]);
  });

  it('the add form counts against maxAdds (maxAdds 0 refuses it) while the entry form is never counted', () => {
    const rejects = runShort(far, { maxAdds: 0 }).events.filter((e) => e.kind === 'vwapReject');
    expect(rejects.map((e) => e.label)).toEqual(['SHORT (VWAP REJECT)']);
  });

  // LONG: T23's fixture (stopBufferPct 3, base volume 9000): LONG (HL) at bar
  // 10, then bar 11 dips to VWAP (~10.94) and closes above it -> the first
  // 'VWAP HOLD'. Bar 12 keeps its low above VWAP (the "first touch" leg for
  // the next dip) and closes either 0.5% over VWAP (11.00 vs 10.9465 — not
  // the 1% the re-arm needs) or 1.8% over it (11.15); bar 13 is the SAME dip
  // in both variants (low 10.85, close 10.99 back above VWAP).
  const h23: Candle[] = [
    bar(0, 10, 10.05, 9.95, 10, 9000),
    bar(1, 10, 12, 9.9, 11.9, 3000),
    bar(2, 11.9, 11.95, 11.4, 11.5, 2000),
    bar(3, 11.5, 11.6, 11.0, 11.1, 2000),
    bar(4, 11.1, 11.3, 10.8, 11.2, 2000),
    bar(5, 11.2, 11.6, 11.1, 11.5, 2000),
    bar(6, 11.5, 11.8, 11.45, 11.7, 2000),
    bar(7, 11.7, 11.75, 11.3, 11.35, 2000),
    bar(8, 11.35, 11.4, 11.25, 11.3, 2000),
    bar(9, 11.3, 11.32, 11.1, 11.15, 2000),
    bar(10, 11.15, 11.25, 11.12, 11.22, 2000), // LONG (HL), stop 11.10 x 0.97 = 10.767
    bar(11, 11.22, 11.25, 10.85, 10.98, 2000), // first VWAP HOLD
  ];
  const dip = bar(13, 11.05, 11.15, 10.85, 10.99, 2000);
  const holdNear = [...h23, bar(12, 10.98, 11.02, 10.96, 11.0, 2000), dip];
  const holdFar = [...h23, bar(12, 10.98, 11.2, 10.97, 11.15, 2000), dip];
  const runLong = (bars: Candle[], params: Partial<SignalParams>) =>
    evaluate({ candles: bars, tfMs: 60_000, nowMs: bars[bars.length - 1]!.ts + 60_000, params: { ...DEFAULT_PARAMS, ...ABCD_PARAMS, stopBufferPct: 3, ...params } });

  it('no close 1% over VWAP since the first hold -> the bar-13 dip is the same test again: exactly one vwapHold', () => {
    expect(runLong(holdNear, {}).events.filter((e) => e.kind === 'vwapHold').map((e) => e.label)).toEqual(['VWAP HOLD']);
  });

  it('a close well over VWAP between them re-arms it -> the bar-13 dip holds again (bars 11 and 13)', () => {
    const holds = runLong(holdFar, {}).events.filter((e) => e.kind === 'vwapHold');
    expect(holds.map((e) => e.label)).toEqual(['VWAP HOLD', 'VWAP HOLD']);
    expect(minutesOf(holds)).toEqual([11, 13]);
  });

  it('the add form counts against maxAdds: with maxAdds 1 the first hold uses it up and the re-armed second is refused', () => {
    const r = runLong(holdFar, { maxAdds: 1 });
    expect(minutesOf(r.events.filter((e) => e.kind === 'vwapHold'))).toEqual([11]);
    expect(lastDetail(r.long.detail)).toBe('Adds used 1/1');
  });
});

describe('evaluate — T29 synthetic: an add is refused past T1 or above maxAddRiskPct, its list-only structure event still printed (addendum 5)', () => {
  const shortBars = buildShortAddsBars();
  const longBars = buildLongAddsBars();

  it("short, risk gate: at maxAddRiskPct 5.5 the bar-11 add (risk (11.918-11.35)/11.35 = 5.0%) fires, the bar-15/19 adds (5.9%) are refused although the cap (5) allows them, and the bar-23 add fires because the stop has moved UP to 12.019 by then (risk 5.0% vs the moved stop) — every lowerHigh is still announced", () => {
    const r = evaluate({
      candles: shortBars,
      tfMs: 60_000,
      nowMs: shortBars[shortBars.length - 1]!.ts + 60_000,
      params: { ...DEFAULT_PARAMS, ...ABCD_PARAMS, hlEntry: false, maxAdds: 5, maxAddRiskPct: 5.5 },
    });
    expect(minutesOf(r.events.filter((e) => e.side === 'short' && e.label === 'ADD (LH)'))).toEqual([11, 23]);
    expect(r.events.filter((e) => e.kind === 'lowerHigh')).toHaveLength(5);
  });

  it("short, the add's risk is measured against the stop it would actually carry — AFTER the new lower high moved it up: at maxAddRiskPct 4.5 the bar-23 add is refused too (moved stop 12.019 -> 4.97%; against the pre-move 11.918 it would be 4.1% and fire)", () => {
    const r = evaluate({
      candles: shortBars,
      tfMs: 60_000,
      nowMs: shortBars[shortBars.length - 1]!.ts + 60_000,
      params: { ...DEFAULT_PARAMS, ...ABCD_PARAMS, hlEntry: false, maxAdds: 5, maxAddRiskPct: 4.5 },
    });
    expect(r.events.filter((e) => e.side === 'short' && e.label === 'ADD (LH)')).toEqual([]);
    expect(r.levels.stop).toBeCloseTo(11.9 * 1.01, 6);
  });

  it("long, risk gate: at maxAddRiskPct 4.2 the bar-15 add (risk (11.50-10.989)/11.50 = 4.4%) is refused although the cap (5) allows it, the bar-19 (4.0%) and bar-23 (3.6%) adds fire — every higherLow is still announced; the LONG (HL) entry (2.1% risk) is not filtered", () => {
    const r = evaluate({
      candles: longBars,
      tfMs: 60_000,
      nowMs: longBars[longBars.length - 1]!.ts + 60_000,
      params: { ...DEFAULT_PARAMS, ...ABCD_PARAMS, lhEntry: false, maxAdds: 5, maxAddRiskPct: 4.2 },
    });
    expect(minutesOf(r.events.filter((e) => e.side === 'long' && e.label === 'ADD (HL)'))).toEqual([19, 23]);
    expect(r.events.filter((e) => e.kind === 'higherLow')).toHaveLength(4);
    expect(r.events.find((e) => e.kind === 'entryHL' && e.label === 'LONG (HL)')).toBeDefined();
  });

  // T1 HIT vs T1 NOT HIT: identical first seven bars (a short entry at bar
  // 6, close 11.45, stop 11.817), a dip, a bounce to a lower high that
  // confirms at bar 10 above VWAP (~11.2-11.35) and below HOD. In the first
  // variant the dip is an 8000-share bar that closes ~3.5% under VWAP — T1
  // VWAP prints at bar 7; in the control the dip stays within the 0.5% band,
  // so it never prints. Both run with maxAdds 5 and maxAddRiskPct 100 so !t1Hit is
  // the only gate in play.
  const t1head: Candle[] = [
    bar(0, 10, 10, 10, 10, 1000),
    bar(1, 10, 12, 10, 11.9, 6000),
    bar(2, 11.9, 12.0, 11.3, 11.4, 3000),
    bar(3, 11.4, 11.5, 11.2, 11.3, 1500),
    bar(4, 11.3, 11.6, 11.25, 11.5, 1600),
    bar(5, 11.5, 11.7, 11.45, 11.65, 1800), // LH = 11.7
    bar(6, 11.65, 11.6, 11.4, 11.45, 1500), // confirms it -> SHORT (LH) at 11.45
  ];
  const t1Hit = [...t1head, bar(7, 11.45, 11.46, 10.8, 10.85, 8000), bar(8, 10.85, 11.0, 10.8, 10.95, 1500), bar(9, 10.95, 11.5, 10.9, 11.4, 2500), bar(10, 11.4, 11.45, 11.0, 11.05, 2000)];
  const t1Not = [...t1head, bar(7, 11.45, 11.46, 11.3, 11.32, 1500), bar(8, 11.32, 11.4, 11.29, 11.33, 1500), bar(9, 11.33, 11.6, 11.3, 11.5, 2500), bar(10, 11.5, 11.55, 11.32, 11.34, 2000)];
  const runT1 = (bars: Candle[]) =>
    evaluate({
      candles: bars,
      tfMs: 60_000,
      nowMs: bars[bars.length - 1]!.ts + 60_000,
      params: { ...DEFAULT_PARAMS, ...SIMPLE, lhEntry: true, swingStrength: 1, maxAdds: 5, maxAddRiskPct: 100 },
    });

  it('short, !t1Hit: with T1 VWAP already printed (bar 7) the bar-9 lower high (11.50, above VWAP) is announced but does NOT add', () => {
    const r = runT1(t1Hit);
    expect(r.events.some((e) => e.kind === 'target' && e.label === 'T1 VWAP')).toBe(true);
    expect(r.events.filter((e) => e.kind === 'lowerHigh').map((e) => e.price)).toEqual([11.7, 11.5]);
    expect(r.events.filter((e) => e.label === 'ADD (LH)')).toEqual([]);
  });

  it('control: the same bounce with T1 never printed DOES add at bar 10 (so the refusal above is the T1 rule, not risk/cap/VWAP)', () => {
    const r = runT1(t1Not);
    expect(r.events.some((e) => e.label === 'T1 VWAP')).toBe(false);
    expect(minutesOf(r.events.filter((e) => e.label === 'ADD (LH)'))).toEqual([10]);
  });

  it("long, !t1Long: once a BREAKOUT above C has latched T1 (bar 11, high 11.90 > 11.8 x 1.003), no later higher low adds even with the caps lifted — the higherLows (bars 14, 18, 22) are still announced", () => {
    const bars = buildLongAddsBars().map((b, i) => (i === 11 ? bar(11, 11.22, 11.9, 11.2, 11.45, 2000) : b));
    const r = evaluate({
      candles: bars,
      tfMs: 60_000,
      nowMs: bars[bars.length - 1]!.ts + 60_000,
      params: { ...DEFAULT_PARAMS, ...ABCD_PARAMS, lhEntry: false, maxAdds: 5, maxAddRiskPct: 100 },
    });
    expect(r.events.some((e) => e.kind === 'breakout' && e.label === 'BREAKOUT')).toBe(true);
    expect(r.events.filter((e) => e.label === 'ADD (HL)')).toEqual([]);
    expect(minutesOf(r.events.filter((e) => e.kind === 'higherLow')).slice(-3)).toEqual([14, 18, 22]);
  });
});

describe('evaluate — T30 append-only bar-by-bar replay, all five real fixtures, with the caps in play (addendum 5)', () => {
  // T14/T25's harness, over ATYR too and at several param sets: the default,
  // a lax cap (maxAdds 5, maxAddRiskPct 100) that lets many MORE adds
  // through — so a forming bar that briefly showed an add the closed bar then
  // withdrew (or the reverse) would surface here — and, since the review
  // (engine:F5), two NON-default sets that reach paths the defaults never do
  // on these fixtures: swingStrength 1 (a swing confirmed by the very close of
  // a bar whose intrabar breakdown/stop already printed) and stopByClose:false
  // (the hard stop joins the forming pass). Every add/re-arm decision is made
  // on CLOSED bars only, so the counters are a pure function of the closed
  // prefix and the forming pass can only ever append. The contract asserted
  // is the set-based one in `expectAppendOnlyReplay` (top of this file): only
  // the ORDER within the newly closed bar may differ, because B2 puts a
  // structure event the close confirms ahead of that bar's own intrabar
  // breakdown.
  const FIXTURES: Record<string, { bars: Bar[] }> = {
    atyr: atyrFixture as typeof atyrFixture & { bars: Bar[] },
    gctk: gctkFixture as typeof gctkFixture & { bars: Bar[] },
    weto: wetoFixture as typeof wetoFixture & { bars: Bar[] },
    sphl: sphlFixture as typeof sphlFixture & { bars: Bar[] },
    dcx: dcxFixture as typeof dcxFixture & { bars: Bar[] },
  };

  for (const [label, params] of [
    ['default caps', DEFAULT_PARAMS],
    ['lax caps (maxAdds 5, maxAddRiskPct 100)', { ...DEFAULT_PARAMS, maxAdds: 5, maxAddRiskPct: 100 }],
    ['swingStrength 1', { ...DEFAULT_PARAMS, swingStrength: 1 }],
    ['stopByClose false', { ...DEFAULT_PARAMS, stopByClose: false }],
  ] as [string, SignalParams][]) {
    for (const [sym, fx] of Object.entries(FIXTURES)) {
      it(`${sym.toUpperCase()}: closed history is a strict prefix and every forming event survives its close, both sides — ${label}`, () => {
        expectAppendOnlyReplay(fx.bars.map(toCandle), params);
      }, 60_000);
    }
  }
});

// ─── Review findings, engine side (T31-T35) ──────────────────────────────
//
// Each block pins one confirmed defect of the forming-bar pass or of the long
// side's gating; every one FAILED against the pre-review engine.

describe('evaluate — T31 review engine:F1: the intrabar long rules read the bounce high as it stood going INTO the bar', () => {
  // The closed loop runs stepStructure before stepLongTrigger, and a swing
  // high confirmed at j = i-2 is by definition ABOVE high[i]. Reading the
  // freshly raised C made the closed pass withdraw a BREAKOUT (a buy marker
  // that also sounds) the forming pass had already shown against the OLD C.
  // Default swingStrength 2; arming loosened like the ABCD fixture.
  const P: SignalParams = { ...DEFAULT_PARAMS, minSpikePct: 10, minVwapExtPct: 0, minGapPct: 1000, minPullbackPct: 3, climaxVolMult: 0 };
  const bars: Candle[] = [
    bar(0, 10, 10.05, 9.95, 10, 20000),
    bar(1, 10, 12, 9.9, 11.9, 3000),
    bar(2, 11.9, 11.95, 11.4, 11.5, 2000),
    bar(3, 11.5, 11.6, 11.0, 11.1, 2000),
    bar(4, 11.1, 11.3, 10.8, 11.2, 2000),
    bar(5, 11.2, 11.5, 11.1, 11.4, 2000),
    bar(6, 11.4, 11.6, 11.3, 11.5, 2000),
    bar(7, 11.5, 11.8, 11.45, 11.7, 2000),
    bar(8, 11.7, 11.75, 11.4, 11.5, 2000),
    bar(9, 11.5, 11.6, 11.3, 11.35, 2000),
    bar(10, 11.35, 11.4, 11.2, 11.3, 2000),
    bar(11, 11.3, 11.35, 11.15, 11.3, 2000),
    bar(12, 11.3, 11.9, 11.2, 11.5, 2000), // a 11.90 high: confirmed as a swing high two bars later, at bar 14's close
    bar(13, 11.5, 11.8, 11.3, 11.6, 2000),
    bar(14, 11.6, 11.85, 11.5, 11.7, 2000), // high 11.85 clears the OLD C (11.80 x 1.003 = 11.8354) — but not the 11.90 the close confirms
  ];

  it('a BREAKOUT shown while bar 14 forms is still there when bar 14 closes, although the close confirms a higher swing high', () => {
    const f = evaluate({ candles: bars, tfMs: 60_000, nowMs: bars[14]!.ts + 20_000, params: P });
    const c = evaluate({ candles: bars, tfMs: 60_000, nowMs: bars[14]!.ts + 60_000, params: P });
    expect(f.events.some((e) => e.kind === 'breakout')).toBe(true);
    const ck = new Set(c.events.map(evKey));
    expect(f.events.filter((e) => !ck.has(evKey(e)))).toEqual([]);
  });

  it('the trigger is still measured against the old C (11.80 x 1.003), and the close then also announces the new structure it confirmed', () => {
    const c = evaluate({ candles: bars, tfMs: 60_000, nowMs: bars[14]!.ts + 60_000, params: P });
    const breakout = c.events.find((e) => e.kind === 'breakout');
    expect(breakout).toBeDefined();
    expect(breakout!.ts).toBe(bars[14]!.ts);
    expect(breakout!.price).toBeCloseTo(11.8 * 1.003, 6);
    // the swing high the close confirms (11.90 at bar 12) still prints, and it becomes the level for the NEXT bar
    expect(c.events.some((e) => e.kind === 'lowerHigh' && e.ts === bars[12]!.ts)).toBe(true);
    expect(c.levels.bounceHigh).toBeCloseTo(11.9, 6);
  });
});

describe('evaluate — T32 review engine:F2: a forming bar arms SPIKE only on gates that cannot un-happen intrabar', () => {
  // HOD, runLow and the gap gain only ever move one way while a bar prints;
  // vwapExt does not (the live bar's own volume pulls VWAP up under it). A
  // SPIKE (and the "SPIKING — do not short" headline) that the vwapExt gate
  // let through early in a bar vanished when the same bar kept printing.
  const base = [0, 1, 2, 3, 4].map((m) => bar(m, 1, 1.01, 0.99, 1, 1000));

  it('a spike armed through the VWAP-extension gate on a thin early print is not shown until the bar closes with the gate met', () => {
    const early = evaluate({ candles: [...base, bar(5, 1, 1.2, 1, 1.19, 100)], tfMs: 60_000, nowMs: ANCHOR + 5 * 60_000 + 5_000 });
    const closed = evaluate({ candles: [...base, bar(5, 1, 1.2, 1, 1.19, 100000)], tfMs: 60_000, nowMs: ANCHOR + 6 * 60_000 });
    expect(closed.events.slice(0, early.events.length)).toEqual(early.events);
    expect(early.state).toBe('idle');
    expect(kinds(early.events)).toEqual([]);
  });

  it('the same bar closing with the VWAP gate met DOES append the SPIKE (a close re-decides with the full gate)', () => {
    const closed = evaluate({ candles: [...base, bar(5, 1, 1.2, 1, 1.19, 1000)], tfMs: 60_000, nowMs: ANCHOR + 6 * 60_000 });
    expect(kinds(closed.events)).toEqual(['spike']);
    expect(closed.state).toBe('spike');
  });

  it('the monotonic gates still arm early and the SPIKE survives the close: the gap gate, and the run gate when the VWAP gate is disabled', () => {
    const thin = [...base, bar(5, 1, 1.2, 1, 1.19, 100)];
    const heavy = [...base, bar(5, 1, 1.2, 1, 1.19, 100000)];
    const cases: { label: string; prevClose: number | null; params: Partial<SignalParams> }[] = [
      { label: 'gap gate (prevClose 0.9, +33%)', prevClose: 0.9, params: {} },
      { label: 'run gate with minVwapExtPct 0', prevClose: null, params: { minVwapExtPct: 0 } },
    ];
    for (const cs of cases) {
      const f = evaluate({ candles: thin, tfMs: 60_000, nowMs: ANCHOR + 5 * 60_000 + 5_000, prevClose: cs.prevClose, params: cs.params });
      const c = evaluate({ candles: heavy, tfMs: 60_000, nowMs: ANCHOR + 6 * 60_000, prevClose: cs.prevClose, params: cs.params });
      expect(kinds(f.events), cs.label).toEqual(['spike']);
      expect(f.state, cs.label).toBe('spike');
      expect(c.events.map(evKey), cs.label).toEqual(f.events.map(evKey));
    }
  });

  it('inside a running spike a forming new-HOD bar keeps the headline at SPIKING once a CLOSED bar has made it one — it must not flip to "No spike yet" for every bar of the run', () => {
    // 10 flat bars, then a 4%-per-bar run. Each bar is evaluated forming
    // (30 s in) right after the previous bar closed: whenever the closed
    // prefix says 'spike', the forming bar (a new HOD by construction) must
    // still say 'spike'.
    const run: Candle[] = Array.from({ length: 10 }, (_, m) => bar(m, 1, 1.005, 0.995, 1, 1000));
    let px = 1;
    for (let m = 10; m < 25; m++) {
      const c = px * 1.04;
      run.push(bar(m, px, c * 1.005, px * 0.995, c, 5000));
      px = c;
    }
    let spikeBars = 0;
    for (let k = 11; k < run.length; k++) {
      const prefix = run.slice(0, k);
      const prevClosed = evaluate({ candles: prefix, tfMs: 60_000, nowMs: prefix[prefix.length - 1]!.ts + 60_000 });
      if (prevClosed.state !== 'spike') continue;
      spikeBars++;
      const withForming = run.slice(0, k + 1);
      const forming = evaluate({ candles: withForming, tfMs: 60_000, nowMs: withForming[withForming.length - 1]!.ts + 30_000 });
      expect(forming.state, `bar ${k} forming`).toBe('spike');
      expect(forming.headline, `bar ${k} forming`).toBe('SPIKING — do not short');
    }
    expect(spikeBars).toBeGreaterThan(5); // the loop really exercised the case
  });
});

describe('evaluate — T33 review engine:F3: a higher low needs a bounce high C that FOLLOWS the current pullback low B', () => {
  // cHigh is the latest swing high after HOD and was never retired when price
  // later undercut B. After a breakdown to a new low, any swing low above the
  // NEW low qualified as D against the stale C — a long armed on a level the
  // pullback had already left behind (real ATYR at swingStrength 1: a LONG
  // (HL) at 06:26 off the 06:21 high, under the 06:23 low).
  const D = [
    bar(0, 10, 10.05, 9.95, 10, 20000),
    bar(1, 10, 12, 9.9, 11.9, 3000),
    bar(2, 11.9, 11.95, 11.4, 11.5, 2000),
    bar(3, 11.5, 11.6, 11.0, 11.1, 2000),
    bar(4, 11.1, 11.3, 10.8, 11.2, 2000),
    bar(5, 11.2, 11.6, 11.1, 11.5, 2000),
    bar(6, 11.5, 11.8, 11.45, 11.7, 2000),
    bar(7, 11.7, 11.75, 11.3, 11.35, 2000),
    bar(8, 11.35, 11.4, 10.6, 10.7, 2000), // BREAKDOWN through B (10.80)
    bar(9, 10.7, 10.9, 10.5, 10.8, 2000), // the NEW pullback low, 10.50
    bar(10, 10.8, 11.0, 10.7, 10.9, 2000),
    bar(11, 10.9, 11.05, 10.65, 10.7, 2000), // a swing low ABOVE 10.50 — but no swing high has formed since it
    bar(12, 10.7, 11.1, 10.8, 11.0, 2000),
  ];

  it('no higherLow / entryHL against the stale C; the short stays the only open side; the long headline says it needs a new bounce high', () => {
    const r = evaluate({ candles: D, tfMs: 60_000, nowMs: D[12]!.ts + 60_000, params: { ...ABCD_PARAMS } });
    expect(r.events.filter((e) => e.kind === 'higherLow' || e.kind === 'entryHL')).toEqual([]);
    expect(r.state).toBe('short');
    expect(r.long.headline).toBe('Long: no higher low yet');
  });

  it('the stale C is not shown either: no bounce-high / long-trigger level while no long is open', () => {
    const r = evaluate({ candles: D, tfMs: 60_000, nowMs: D[12]!.ts + 60_000, params: { ...ABCD_PARAMS } });
    expect(r.levels.bounceHigh).toBeNull();
    expect(r.levels.longTrigger).toBeNull();
  });

  it('control: the same bars WITHOUT the breakdown (a swing low above B after C) still arm the long — the gate only rejects a C that precedes B', () => {
    const r = evaluate({ candles: buildAbcdBars().slice(0, 11), tfMs: 60_000, nowMs: buildAbcdBars()[10]!.ts + 60_000, params: { ...ABCD_PARAMS } });
    expect(r.events.some((e) => e.kind === 'higherLow')).toBe(true);
    expect(r.events.some((e) => e.kind === 'entryHL')).toBe(true);
  });

  it('an OPEN long keeps showing its entry-time C (its own BREAKOUT / T1 need it) even after a later low undercuts B', () => {
    // ABCD through bar 10 opens the long (higher low D, C = 11.80); then a
    // bar whose low undercuts B (10.8) — the long is still open (its stop is
    // 10.989 by close, so keep the close above it: only the low breaks B).
    const bars = [...buildAbcdBars().slice(0, 11), bar(11, 11.22, 11.3, 10.7, 11.2, 2000)];
    const r = evaluate({ candles: bars, tfMs: 60_000, nowMs: bars[11]!.ts + 60_000, params: { ...ABCD_PARAMS } });
    expect(r.long.state).toBe('long');
    expect(r.levels.bounceHigh).toBeCloseTo(11.8, 6);
    expect(r.levels.longTrigger).toBeCloseTo(11.8 * 1.003, 6);
  });
});

describe('evaluate — T34 review engine:F4: stopByClose:false — a resting stop that already traded is not cancelled by the close', () => {
  // With a hard stop the forming pass checks high >= entry.stop. In the
  // closed loop stepStructure ran first and could confirm a swing high that
  // raises the stop above high[i] — retroactively cancelling a stop that had
  // traded. Every intrabar rule now runs BEFORE structure in hard-stop mode,
  // so a closed bar replays exactly what its forming pass showed and then
  // appends the close-only rules.
  const Pc: SignalParams = { ...DEFAULT_PARAMS, ...ABCD_PARAMS, swingStrength: 2, lhEntry: false, stopByClose: false };
  const C = [
    ...buildAbcdBars().slice(0, 10),
    bar(10, 11.35, 11.95, 11.3, 11.4, 2000), // a 11.95 high — confirmed as a swing high at bar 12's close, which lifts the stop
    bar(11, 11.4, 11.5, 10.7, 10.75, 2000), // breaks the pullback low: SHORT (lhEntry:false -> 'entry')
    bar(12, 10.75, 11.93, 10.7, 11.0, 2000), // high 11.93 trades through the 11.918 stop
  ];

  it('the STOP printed while bar 12 forms is still there — and still in the same order — once bar 12 closes', () => {
    const f = evaluate({ candles: C, tfMs: 60_000, nowMs: C[12]!.ts + 20_000, params: Pc });
    const c = evaluate({ candles: C, tfMs: 60_000, nowMs: C[12]!.ts + 60_000, params: Pc });
    expect(f.events[f.events.length - 1]!.kind).toBe('stopped');
    const k = (e: SignalEvent) => `${e.ts}:${e.kind}:${e.price}`;
    expect(c.events.slice(0, f.events.length).map(k)).toEqual(f.events.map(k));
    expect(c.events.some((e) => e.kind === 'stopped')).toBe(true);
    // the swing high the close confirms is announced AFTER the stop it would have moved
    expect(kinds(c.events.slice(f.events.length))).toContain('lowerHigh');
  });

  it('the stop level in the STOP event is the one the resting order had going into the bar (11.918), not the raised one (11.95 x 1.01)', () => {
    const c = evaluate({ candles: C, tfMs: 60_000, nowMs: C[12]!.ts + 60_000, params: Pc });
    expect(c.events.find((e) => e.kind === 'stopped')!.price).toBeCloseTo(11.8 * 1.01, 6);
  });

  it("real GCTK, hard-stop mode: the 17:01 bar dipped to VWAP and closed near it but its LOW had already traded through the long's 4.08 stop — the STOP (LONG) stands and no close-only VWAP HOLD add prints on that same bar (the pre-review engine printed both)", () => {
    const candles = (gctkFixture.bars as Bar[]).map(toCandle);
    const nowMs = candles[candles.length - 1]!.ts + 10 * 60_000;
    const r = evaluate({ candles, tfMs: 60_000, nowMs, params: { ...DEFAULT_PARAMS, stopByClose: false } });
    const atBar = r.events.filter((e) => e.ts === 1790197260000 && e.side === 'long');
    expect(kinds(atBar)).toContain('longStopped');
    expect(kinds(atBar)).not.toContain('vwapHold');
  });

  it('stopByClose:true is untouched by the reordering: the same wick (close 11.0, under any stop) does NOT stop, and the short stays open', () => {
    const Pt: SignalParams = { ...Pc, stopByClose: true };
    const c = evaluate({ candles: C, tfMs: 60_000, nowMs: C[12]!.ts + 60_000, params: Pt });
    expect(c.events.some((e) => e.kind === 'stopped')).toBe(false);
    expect(c.short.state).toBe('short');
  });
});

describe('evaluate — T35 review engine:F6: evaluate() groups by ET day without an Intl call per candle', () => {
  // evaluate() memoises etDayKey per UTC hour (an ET calendar date can only
  // roll over on a UTC hour boundary: America/New_York's offsets are whole
  // hours and its DST switches happen at 02:00 local). The output cannot
  // change, so what needs pinning is the ASSUMPTION the memo rests on: walk
  // every quarter hour of 2026 — across both DST transitions — and check no
  // ET date ever changes inside a UTC hour.
  it('etDayKey(t) equals etDayKey of the start of t\'s UTC hour, for every 15 minutes of 2026', () => {
    const HOUR = 3_600_000;
    const bad: number[] = [];
    for (let t = Date.UTC(2026, 0, 1); t < Date.UTC(2027, 0, 1); t += 15 * 60_000) {
      if (etDayKey(t) !== etDayKey(Math.floor(t / HOUR) * HOUR)) bad.push(t);
    }
    expect(bad).toEqual([]);
  });

  it('two candles either side of ET midnight (23:59 and 00:00) land on different ET days: the result is dated by the later one', () => {
    // ET midnight in September is 04:00 UTC (ANCHOR is 04:00 ET, 08:00 UTC),
    // itself a UTC hour boundary — the two candles fall in different hour
    // buckets, so the memo re-reads etDayKey between them.
    const midnightEt = ANCHOR - 4 * 3_600_000;
    const mk = (ts: number): Candle => ({ ts, open: 1, high: 1.01, low: 0.99, close: 1, volume: 1000 });
    const candles = [mk(midnightEt - 60_000), mk(midnightEt)];
    expect(etDayKey(candles[0]!.ts)).not.toBe(etDayKey(candles[1]!.ts));
    const r = evaluate({ candles, tfMs: 60_000, nowMs: midnightEt + 600_000 });
    expect(r.dayKey).toBe(etDayKey(midnightEt));
  });
});

describe('fmtEt / fmtPx', () => {
  it('fmtEt renders HH:MM in America/New_York', () => {
    expect(fmtEt(ANCHOR)).toBe('04:00');
  });

  it('fmtPx uses 4dp under $1 and 2dp at or above $1 (MonitorChart\'s own dp rule)', () => {
    expect(fmtPx(0.5982)).toBe('0.5982');
    expect(fmtPx(11.8)).toBe('11.80');
  });
});
