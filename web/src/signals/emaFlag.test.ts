// Pins the 9 EMA FLAG engine (emaFlag.ts) — long only — against (a) the REAL
// 1-minute fixtures in src/signals/fixtures/ (BEAT 2026-10-05 is the trader's
// own screenshot: three flags on one run) and (b) hand-built candles for the
// rules the real data doesn't exercise. Node env, no chart, no store, no WS.
//
// What the real-data tests assert is what the RULES produce, checked by running
// the engine — not the architect's hand trace, which was approximate (it used
// an estimated EMA). Where the two differ the comment next to the assertion
// says how, with the bar values, so a reader can see the rule that made the
// call. Nothing here was tuned to match the trace.
import { describe, expect, it } from 'vitest';
import { DEFAULT_FLAG_PARAMS, emaSeries, evaluateFlag, type FlagEvent, type FlagParams, type FlagResult } from './emaFlag';
import { fmtEt, fmtPx } from './shortFade';
import { etDayKey } from '../marketClock';
import { aggregate, toCandle } from '../chartAgg';
import { COMPUTE } from '../vendor/blackthorn-charts/indicators/registry';
import type { Candle } from '../vendor/blackthorn-charts';
import type { Bar } from '../types';
import beatFixture from './fixtures/beat-2026-10-05.json';
import saiqFixture from './fixtures/saiq-2026-10-05.json';
import miFixture from './fixtures/mi-2026-10-05.json';
import alecFixture from './fixtures/alec-2026-10-05.json';
import veeaFixture from './fixtures/veea-2026-10-05.json';
import apusFixture from './fixtures/apus-2026-10-05.json';
import sdevFixture from './fixtures/sdev-2026-10-05.json';
import qtexFixture from './fixtures/qtex-2026-10-05.json';
import atyrFixture from './fixtures/atyr-2026-09-24.json';
import gctkFixture from './fixtures/gctk-2026-09-24.json';
import wetoFixture from './fixtures/weto-2026-09-24.json';
import sphlFixture from './fixtures/sphl-2026-09-24.json';
import dcxFixture from './fixtures/dcx-2026-09-24.json';
import ptcFixture from './fixtures/ptc-2026-10-05-1325.json';
import mi1325Fixture from './fixtures/mi-2026-10-05-1325.json';
import spaiFixture from './fixtures/spai-2026-10-07.json';

interface Fixture {
  sym: string;
  date: string;
  bars: Bar[];
}

const FIXTURES_1005: Fixture[] = [beatFixture, saiqFixture, miFixture, alecFixture, veeaFixture, apusFixture, sdevFixture, qtexFixture] as Fixture[];
const FIXTURES_0924: Fixture[] = [atyrFixture, gctkFixture, wetoFixture, sphlFixture, dcxFixture] as Fixture[];

const MIN = 60_000;

function keyOf(e: FlagEvent): string {
  return `${e.ts}|${e.kind}|${e.label}|${e.price}|${e.note}|${e.marker ?? ''}`;
}

// 'HH:MM kind LABEL price' — the compact form the event logs and the BEAT
// table below use.
function line(e: FlagEvent): string {
  return `${fmtEt(e.ts)} ${e.kind} ${e.label} ${fmtPx(e.price)}`;
}

function todayEvents(r: FlagResult): FlagEvent[] {
  return r.events.filter((e) => etDayKey(e.ts) === r.dayKey);
}

// ─── The append-only contract ────────────────────────────────────────────
//
// Feed the engine one bar at a time — the last bar FORMING, then the same bar
// CLOSED — and for every step:
//   1. closed history is a strict prefix: every event already emitted for the
//      closed bars is byte-identical in BOTH the forming and the closed run;
//   2. everything the forming bar showed reappears, identically and in the
//      same order, as the FIRST events the closed bar emits (the closed loop
//      runs the forming pass's own step first, then the close-based rules);
//   3. no duplicate (ts, kind, label);
//   4. sanity invariants on the result itself.
function expectAppendOnlyReplay(all: Candle[], dayKey: string, tfMs: number, params?: Partial<FlagParams>): FlagResult {
  const yesterday = all.filter((c) => etDayKey(c.ts) !== dayKey);
  const today = all.filter((c) => etDayKey(c.ts) === dayKey);
  let prevClosed: string[] = [];
  let prevBoxes: FlagResult['boxes'] = [];
  let finalClosed!: FlagResult;
  for (let i = 0; i < today.length; i++) {
    const slice = [...yesterday, ...today.slice(0, i + 1)];
    const lastTs = slice[slice.length - 1]!.ts;
    const forming = evaluateFlag({ candles: slice, tfMs, nowMs: lastTs + 20_000, params });
    const closed = evaluateFlag({ candles: slice, tfMs, nowMs: lastTs + tfMs + 1, params });
    const fKeys = forming.events.map(keyOf);
    const cKeys = closed.events.map(keyOf);
    // (1)
    expect(fKeys.slice(0, prevClosed.length)).toEqual(prevClosed);
    expect(cKeys.slice(0, prevClosed.length)).toEqual(prevClosed);
    // (2)
    const formingNew = fKeys.slice(prevClosed.length);
    expect(cKeys.slice(prevClosed.length, prevClosed.length + formingNew.length)).toEqual(formingNew);
    // (3)
    const ids = closed.events.map((e) => `${e.ts}|${e.kind}|${e.label}`);
    expect(new Set(ids).size).toBe(ids.length);
    // boxes: earlier flags keep their geometry (only the outcome/extent of the latest may move)
    expect(closed.boxes.length).toBeGreaterThanOrEqual(prevBoxes.length);
    for (let b = 0; b < prevBoxes.length; b++) {
      expect(closed.boxes[b]!.startTs).toBe(prevBoxes[b]!.startTs);
      expect(closed.boxes[b]!.high).toBe(prevBoxes[b]!.high);
      expect(closed.boxes[b]!.trigger).toBe(prevBoxes[b]!.trigger);
    }
    // (4)
    for (const r of [forming, closed]) {
      if (r.state === 'long') {
        expect(r.levels.entryPrice).not.toBeNull();
        expect(r.levels.stop).not.toBeNull(); // (a trailed stop may sit above the entry: locked-in profit, not a bug)
      }
      if (r.state === 'flat' || r.state === 'ready') {
        expect(r.levels.trigger).not.toBeNull();
        expect(r.levels.trigger!).toBeGreaterThan(r.levels.flagHigh!);
      }
      expect(r.headline.length).toBeGreaterThan(0);
    }
    prevClosed = cKeys;
    prevBoxes = closed.boxes.map((b) => ({ ...b }));
    finalClosed = closed;
  }
  return finalClosed;
}

// ─── Synthetic candles ───────────────────────────────────────────────────
// ANCHOR is 04:00 ET on 2025-09-24; bar i is i minutes later, so every
// synthetic day below is one ET day (a second day is shifted by DAY).
const ANCHOR = 1758700800000;
const DAY = 86_400_000;
const tsAt = (i: number, dayOffsetMs = 0): number => ANCHOR + dayOffsetMs + i * MIN;

function bar(i: number, o: number, h: number, l: number, c: number, v = 1000, dayOffsetMs = 0): Candle {
  return { ts: tsAt(i, dayOffsetMs), open: o, high: h, low: l, close: c, volume: v };
}

// 20 quiet bars at 1.00: the 9 EMA settles at exactly 1.00.
function baseBars(dayOffsetMs = 0): Candle[] {
  return Array.from({ length: 20 }, (_, i) => bar(i, 1.0, 1.002, 0.998, 1.0, 1000, dayOffsetMs));
}

// Bar 20: the thrust. Close 1.18 is 13.9% over the EMA it creates (1.036) on a
// new high (1.20) -> an impulse; base low 0.998, top 1.20, move 0.202.
const THRUST = bar(20, 1.0, 1.2, 0.999, 1.18, 9000);

// Bars 21..26: a flag that drifts 1% while the EMA catches up from below. High
// stays under 1.20; closes 1.180 -> 1.192.
const FLAT_CLOSES = [1.18, 1.184, 1.188, 1.19, 1.192, 1.192];
function flatBars(from = 0, to = FLAT_CLOSES.length): Candle[] {
  return FLAT_CLOSES.slice(from, to).map((c, k) => bar(21 + from + k, c - 0.001, c + 0.004, c - 0.004, c, 1500));
}
// The flag's own low: the lowest low of the bars AFTER the thrust (bar 21's 1.176). The thrust bar's
// 0.999 is the base the move came from, not part of the flag (see F12).
const FLAG_LOW = Math.min(...flatBars().map((b) => b.low));

// The breakout bar 27 clears the trigger (1.20 * 1.003 = 1.2036) and closes at 1.205.
const BREAK = bar(27, 1.19, 1.21, 1.19, 1.205, 4000);

function run(candles: Candle[], params?: Partial<FlagParams>, nowMs?: number): FlagResult {
  const last = candles[candles.length - 1]!;
  return evaluateFlag({ candles, tfMs: MIN, nowMs: nowMs ?? last.ts + 10 * MIN, params });
}

const closesOf = (cs: Candle[]): number[] => cs.map((c) => c.close);

// ─── F1 ──────────────────────────────────────────────────────────────────

describe('F1 emaSeries is the chart\'s EMA, element by element', () => {
  for (const [name, fx] of [['BEAT', beatFixture], ['SAIQ', saiqFixture]] as [string, Fixture][]) {
    it(`${name}: equals the vendored COMPUTE.ema exactly (nulls, warm-up and every float)`, () => {
      const candles = fx.bars.map(toCandle);
      for (const period of [9, 3, 20]) {
        const mine = emaSeries(closesOf(candles), period);
        const theirs = COMPUTE.ema(candles, { period }).lines.value as (number | null)[];
        expect(mine.length).toBe(theirs.length);
        for (let i = 0; i < mine.length; i++) expect(Object.is(mine[i], theirs[i])).toBe(true);
      }
    });
  }
});

// ─── F2 — real BEAT ──────────────────────────────────────────────────────

describe('F2 real BEAT 2026-10-05 (1-minute, defaults)', () => {
  const candles = (beatFixture.bars as Bar[]).map(toCandle);
  const r = run(candles);
  const idxAt = (hhmm: string): number => candles.findIndex((c) => etDayKey(c.ts) === '2026-10-05' && fmtEt(c.ts) === hhmm);
  const upTo = (hhmm: string): Candle[] => candles.slice(0, idxAt(hhmm) + 1);

  // THE sequence the rules produce for today. Differences from the architect's
  // hand trace (a hand-estimated EMA), each tied to the rule that decides it:
  //  - 08:31 .. 08:54: ONE impulse (08:31, close 0.4806 = +12.6% over the EMA
  //    0.4267) and nothing else. Its retrace gate rejects the 08:31..08:33
  //    window (the 08:31 bar's own low 0.4161 is under base 0.3999 + half the
  //    0.1413 move = 0.4705; the base is the lowest low of the previous TEN
  //    PRINTS, which on a thin pre-market reach back to 07:34), and the 08:40
  //    close 0.4989 is 1.6% under the EMA 0.5071, which
  //    ENDS the impulse silently (no flag was open, so no FLAG FAILED). The
  //    08:50 bar (+6.2% over its EMA) is not a breakout: its high 0.58 is under
  //    the 0.5899 of 08:39. 08:51 breaks that high but only closes +4.5%.
  //    08:55 (+8.1%, high 0.6252 over 0.5962) is the next impulse.
  //  - flat 08:59 and the entry 09:00 match the trace (flat on the same bar the
  //    EMA is within 4%: close 0.6247 vs EMA 0.6046 = 3.3%).
  //  - flag 2 is FLAT at 09:03, as in the trace (12.5% above its EMA: close
  //    0.7932 vs 0.7051). The window 09:01-09:03 starts on the
  //    09:01 top bar: it counts toward the three bars and its close holds the
  //    EMA floor, but it is the thrust the flag hangs from, so the retrace gate
  //    and the box are measured on 09:02-09:03 only (lows >= 0.7531). Counting
  //    the thrust bar's own wick (low 0.6449, under base 0.4975 + 50% of the
  //    move = 0.6788) used to reject that window and FLAT came a bar late at
  //    09:04, with a box 0.6449-0.8600 that no trader would draw.
  //  - emaNear 09:07 (0.7797 vs 0.7506 = 3.9%) and ADD 09:09 at 0.8626 match.
  //  - flag 3 is FLAT at 09:11 (4.4% above: close 0.8575 vs EMA 0.8211), then
  //    9 EMA NEAR at 09:12 (2.7%: 0.849 vs 0.8267) — not "flat 09:12, ready on
  //    the same bar". Window 09:09-09:11 closes 0.9327/0.8649/0.8575 span 0.0752
  //    = 8.8% of price (cap 10%) and 16.6% of the 0.4525 move (cap 35%).
  //  - 'FLAG FAILED' at 09:15, on the same bar as the EXIT. The flag's running
  //    closes start AFTER the 09:09 top bar (09:10-09:14: 0.8649, 0.8575, 0.849,
  //    0.858, 0.8323 — a span of 0.0326 = 3.9% of price), so flag 3 is still open at
  //    09:15 when the close 0.8016 is 3.0% under the EMA (0.8266) — the trader's
  //    own "flag 3 fails". (Counting the thrust bar's 0.9327 close in the span
  //    made it 12.1% of price, over the 10% cap, and the flag faded silently a
  //    bar early with nothing printed at 09:15.)
  //    The EXIT at 09:15 matches: close 0.8016 under the EMA trail 0.8246
  //    (EMA@09:14 0.8328 * 0.99), +23.8% from 0.6477. The exit prints first
  //    (rule order: the stop, then the fail).
  const EXPECTED = [
    '08:31 impulse BREAKOUT 0.5412',
    '08:55 impulse BREAKOUT 0.6252',
    '08:59 flat FLAT 0.6477',
    '09:00 entry LONG (FLAG) 0.6477',
    '09:03 flat FLAT 0.8626',
    '09:07 emaNear 9 EMA NEAR 0.8626',
    '09:09 add ADD (FLAG) 0.8626',
    '09:11 flat FLAT 0.9528',
    '09:12 emaNear 9 EMA NEAR 0.9528',
    '09:15 exit EXIT 0.8016',
    '09:15 failed FLAG FAILED 0.8016',
  ];

  it('today\'s events are exactly the rules\' sequence', () => {
    expect(todayEvents(r).map(line)).toEqual(EXPECTED);
    // yesterday contributes nothing on this fixture
    expect(r.events.length).toBe(EXPECTED.length);
  });

  it('markers: only the entry/add are buys, only the exit is a sell; everything else is list-only', () => {
    const markers = todayEvents(r).map((e) => `${e.kind}:${e.marker ?? '-'}`);
    expect(markers).toEqual([
      'impulse:-', 'impulse:-', 'flat:-', 'entry:buy', 'flat:-', 'emaNear:-', 'add:buy', 'flat:-', 'emaNear:-', 'exit:sell', 'failed:-',
    ]);
    for (const e of r.events) expect(e.side).toBe('long');
  });

  it('flag 1: flat on 08:59 with the EMA already within 4% (ready on the same bar, no separate 9 EMA NEAR), entry on 09:00 with the stop under the EMA', () => {
    const flat = todayEvents(r).find((e) => e.kind === 'flat' && fmtEt(e.ts) === '08:59')!;
    expect(flat.note).toBe('sideways for 3 bars at 0.6055–0.6458 above the rising 9 EMA (3.3% above) — long above 0.6477');
    expect(todayEvents(r).some((e) => e.kind === 'emaNear' && fmtEt(e.ts) < '09:00')).toBe(false);
    const entry = todayEvents(r).find((e) => e.kind === 'entry')!;
    expect(fmtEt(entry.ts)).toBe('09:00');
    expect(entry.price).toBeCloseTo(0.6458 * 1.003, 10);
    // stop = max(flag low 0.6055, EMA@08:59 0.6046) * 0.99
    expect(entry.note).toBe('broke the flag high 0.6458 after the 9 EMA caught up — stop 0.5994 (7.5% risk)');
    const box = r.boxes[0]!;
    expect(fmtEt(box.startTs)).toBe('08:57');
    expect(fmtEt(box.endTs)).toBe('09:00');
    expect(box.high).toBe(0.6458);
    expect(box.low).toBe(0.6055);
    expect(fmtEt(box.readyTs!)).toBe('08:59');
    expect(box.outcome).toBe('broke');
  });

  it('flag 2 is the ADD and flag 3 fails — box outcomes and geometry', () => {
    expect(r.boxes.map((b) => `${fmtEt(b.startTs)}-${fmtEt(b.endTs)} ${fmtPx(b.low)}-${fmtPx(b.high)} ready ${b.readyTs ? fmtEt(b.readyTs) : '-'} ${b.outcome}`)).toEqual([
      '08:57-09:00 0.6055-0.6458 ready 08:59 broke',
      '09:01-09:09 0.7531-0.8600 ready 09:07 broke',
      '09:09-09:14 0.8230-0.9500 ready 09:12 failed',
    ]);
  });

  it('exit: the EMA trail, closed bar 09:15, +23.8% from the entry; the state ends idle', () => {
    const exit = todayEvents(r).find((e) => e.kind === 'exit')!;
    expect(exit.note).toBe('closed below the 9 EMA trail 0.8246 — exit at 0.8016, +23.8% from 0.6477');
    expect(r.state).toBe('idle');
    expect(r.headline).toBe('No breakout yet');
    expect(r.dayKey).toBe('2026-10-05');
  });

  it('headline/detail at the moments that matter (real bars)', () => {
    const impulse = run(upTo('08:55'));
    expect(impulse.state).toBe('impulse');
    expect(impulse.headline).toBe('BREAKOUT — wait for it to go sideways');
    expect(impulse.detail[0]).toBe('Top 0.6252 at 08:55, +8.9% above the 9 EMA'); // top high vs the EMA at the top bar (0.5743)
    expect(impulse.detail[1]).toBe('A flag needs 3 bars holding within 35% of the move above the EMA');

    const ready = run(upTo('08:59'));
    expect(ready.state).toBe('ready');
    expect(ready.headline).toBe('READY — 9 EMA caught up, long above 0.6477');
    expect(ready.detail).toEqual(['Flag 0.6055–0.6458 (3 bars), EMA 3.3% below', 'Stop would be 0.5994 (7.5% risk)', 'A close >1% under the EMA fails it']);
    expect(ready.levels.trigger).toBeCloseTo(0.6477374, 7);
    expect(ready.levels.stop).toBeCloseTo(0.6055 * 0.99, 10);

    const long = run(upTo('09:00'));
    expect(long.state).toBe('long');
    expect(long.headline).toBe('LONG since 09:00 at 0.6477 (+0.3% open)');
    // the entry note's stop is 0.5994 (max(flag low, EMA@08:59) * 0.99); the 09:00 close then raises the trail
    // to EMA@09:00 0.6136 * 0.99 = 0.6075 — the bar that opened the position raises it like any later bar
    expect(long.detail).toEqual(['Stop 0.6075 (9 EMA trail)', 'Exit on a close below the stop']);
    expect(long.levels.adds).toBe(0);

    const flat = run(upTo('09:04'));
    expect(flat.state).toBe('long'); // the position from 09:00 is still open while flag 2 forms
    const added = run(upTo('09:09'));
    expect(added.state).toBe('long');
    expect(added.levels.adds).toBe(1);
    expect(added.detail[0]).toMatch(/ · adds 1\/2$/);
    expect(flat.levels.trigger).toBeCloseTo(0.86 * 1.003, 10); // the flag the next add would break
  });

  it('evaluates the whole two-day array in one pass (prev day + today); the day boundary does not reset the EMA', () => {
    const closes = closesOf(candles);
    const ema = emaSeries(closes, 9);
    expect(r.levels.ema).toBe(ema[ema.length - 1]);
    expect(r.levels.atr).not.toBeNull();
  });
});

// ─── F3 — synthetic flag: flat -> ready -> break -> trail exit ───────────

describe('F3 synthetic flag: impulse, a drifting flag while the EMA approaches, ready, break, then the EMA trail exits', () => {
  // 20 base bars, the thrust (20), flat bars 21..26, the break (27), two drift
  // bars (28, 29) and a washout (30) closing under the trail. The flag opens at
  // 22 — the thrust bar (20) counts as the first of the three bars, and the
  // flag itself (its low, its closes) is the bars after it: flag low 1.176 (bar
  // 21's low), NOT the thrust bar's 0.999.
  const b28 = bar(28, 1.205, 1.215, 1.2, 1.21);
  const b29 = bar(29, 1.21, 1.225, 1.205, 1.22);
  const b30 = bar(30, 1.215, 1.22, 1.14, 1.15, 6000);
  const all = [...baseBars(), THRUST, ...flatBars(), BREAK, b28, b29, b30];
  const ema = emaSeries(closesOf(all), 9) as number[];

  it('the sequence, bar by bar: impulse 20, flat 22 (not ready), 9 EMA NEAR 26, LONG 27, EXIT 30', () => {
    const r = run(all);
    expect(r.events.map((e) => `${(e.ts - ANCHOR) / MIN} ${e.kind} ${e.label}`)).toEqual([
      '20 impulse BREAKOUT',
      '22 flat FLAT',
      '26 emaNear 9 EMA NEAR',
      '27 entry LONG (FLAG)',
      '30 exit EXIT',
    ]);
    // the EMA really is catching up: not ready at 22-25, ready at 26 (derived, not assumed)
    for (const k of [22, 23, 24, 25]) expect(all[k]!.close).toBeGreaterThan(ema[k]! * 1.04);
    expect(all[26]!.close).toBeLessThanOrEqual(ema[26]! * 1.04);
  });

  it('the states a trader would see, one closed bar at a time', () => {
    const states = [20, 21, 22, 23, 25, 26, 27, 29, 30].map((k) => [k, run(all.slice(0, k + 1)).state]);
    // 21: only two of the three bars exist yet (the thrust bar is the first)
    expect(states).toEqual([[20, 'impulse'], [21, 'impulse'], [22, 'flat'], [23, 'flat'], [25, 'flat'], [26, 'ready'], [27, 'long'], [29, 'long'], [30, 'idle']]);
  });

  it('FLAT headline and detail carry the live distance to the EMA, the flag range, the READY threshold and the trigger', () => {
    const r = run(all.slice(0, 25)); // through bar 24: flat since 22, not ready
    const d = (1.19 / ema[24]! - 1) * 100;
    expect(r.state).toBe('flat');
    expect(r.headline).toBe(`FLAT — sideways above the 9 EMA (${d.toFixed(1)}% above)`);
    expect(r.detail).toEqual([`Flag ${fmtPx(FLAG_LOW)}–1.20 since ${fmtEt(tsAt(20))} (5 bars)`, 'READY when the EMA is within 4%', 'Long above 1.20']);
    expect(r.levels.flagHigh).toBe(1.2);
    expect(r.levels.flagLow).toBeCloseTo(FLAG_LOW, 12); // the lowest low AFTER the thrust bar (0.999)
    expect(r.levels.baseLow).toBe(0.998);
    expect(r.levels.top).toBe(1.2);
    expect(r.levels.topTs).toBe(tsAt(20));
  });

  it('geometry and prices: trigger = high * 1.003, stop = max(flag low, EMA of the last CLOSED bar) * 0.99, exit by the raised trail', () => {
    const r = run(all);
    const byKind = (k: string) => r.events.find((e) => e.kind === k)!;
    expect(byKind('impulse').price).toBe(1.2);
    expect(byKind('flat').price).toBeCloseTo(1.2036, 10);
    const entry = byKind('entry');
    expect(entry.price).toBeCloseTo(1.2036, 10);
    expect(entry.marker).toBe('buy');
    expect(entry.note).toContain('after the 9 EMA caught up');
    const stop0 = Math.max(FLAG_LOW, ema[26]!) * 0.99; // the flag's low is the lowest low after the thrust bar (1.176); the EMA (~1.15) is under it
    expect(stop0).toBeCloseTo(FLAG_LOW * 0.99, 12);
    expect(entry.note).toContain(`stop ${fmtPx(stop0)}`);
    // the entry bar's own close raises the trail like any later bar's: after the closed bar 27 the stop is max(stop0, EMA@27 * 0.99)
    expect(run([...baseBars(), THRUST, ...flatBars(), BREAK]).levels.stop).toBeCloseTo(Math.max(stop0, ema[27]! * 0.99), 12);
    // the trail raises each later closed bar: after 28 and 29 the stop is EMA@29 * 0.99
    const trail = ema[29]! * 0.99;
    expect(run(all.slice(0, 30)).levels.stop).toBeCloseTo(trail, 12);
    const exit = byKind('exit');
    expect(exit.marker).toBe('sell');
    expect(exit.price).toBe(1.15); // by close
    expect(exit.note).toBe(`closed below the 9 EMA trail ${fmtPx(trail)} — exit at ${fmtPx(1.15)}, -4.5% from ${fmtPx(1.2 * 1.003)}`);
    expect(trail).toBeGreaterThan(stop0);
    expect(all[30]!.close).toBeLessThan(trail);
    // the exit is the FIRST close under the trail: 28 and 29 were above it
    expect(all[28]!.close).toBeGreaterThan(ema[27]! * 0.99);
    expect(all[29]!.close).toBeGreaterThan(ema[28]! * 0.99);
  });

  it('the box: from the top bar to the break bar, ready at 26, outcome broke', () => {
    const r = run(all);
    expect(r.boxes).toEqual([
      { startTs: tsAt(20), endTs: tsAt(27), high: 1.2, low: FLAG_LOW, trigger: 1.2 * 1.003, readyTs: tsAt(26), outcome: 'broke' },
    ]);
  });

  it('the break is ALSO called on the forming bar, identically, and the closed bar keeps it', () => {
    const upTo27 = [...baseBars(), THRUST, ...flatBars(), BREAK];
    const forming = run(upTo27, undefined, tsAt(27) + 30_000);
    const closed = run(upTo27);
    expect(forming.events.map(keyOf)).toEqual(closed.events.map(keyOf));
    expect(forming.events[forming.events.length - 1]!.kind).toBe('entry');
    expect(forming.state).toBe('long');
    // but the same bar forming BEFORE it reaches the trigger: nothing yet
    const quiet = run([...baseBars(), THRUST, ...flatBars(), bar(27, 1.19, 1.2, 1.19, 1.195)], undefined, tsAt(27) + 30_000);
    expect(quiet.events.some((e) => e.kind === 'entry')).toBe(false);
    expect(quiet.state).toBe('ready');
  });

  it('a bar that breaks the flag and closes under the stop exits on its own close (the position is judged against the bar that opened it)', () => {
    const crash = bar(27, 1.19, 1.21, 1.1, 1.105, 8000); // breaks 1.2036, closes under the stop (~1.164)
    const r = run([...baseBars(), THRUST, ...flatBars(), crash]);
    // The crash is also a 9 EMA SLAM (F18): out of the READY flag of bar 26, opened above the EMA, closed 3%+ under it
    // on a big body near its low and on volume. It prints LAST on its bar, after the exit, and changes nothing else.
    expect(r.events.map((e) => e.kind)).toEqual(['impulse', 'flat', 'emaNear', 'entry', 'exit', 'slam']);
    expect(r.events[4]!.price).toBe(1.105);
    expect(r.state).toBe('idle'); // 1.105 is also > 1% under the EMA, which ends the impulse
    expect(run([...baseBars(), THRUST, ...flatBars(), crash], { slam: false }).events.map((e) => e.kind)).toEqual(['impulse', 'flat', 'emaNear', 'entry', 'exit']);
  });
});

// ─── F4 — entryNeedsReady ────────────────────────────────────────────────

describe('F4 entryNeedsReady', () => {
  // flat opens at 23 (D = 7.2%, not ready); bar 24 breaks 1.2036 before the EMA caught up.
  const early = [...baseBars(), THRUST, ...flatBars(0, 3), bar(24, 1.18, 1.21, 1.18, 1.19, 4000)];

  it('default: a break while flat-but-not-ready is an EARLY BREAK — list-only, outcome early, no position', () => {
    const r = run(early);
    expect(r.events.map((e) => `${e.kind}:${e.marker ?? '-'}`)).toEqual(['impulse:-', 'flat:-', 'earlyBreak:-']);
    const eb = r.events[2]!;
    expect(eb.label).toBe('EARLY BREAK');
    expect(eb.price).toBeCloseTo(1.2036, 10);
    expect(eb.note).toBe('broke out before the 9 EMA caught up — chase risk');
    expect(r.boxes.map((b) => b.outcome)).toEqual(['early']);
    expect(r.state).toBe('impulse');
    expect(r.levels.entryPrice).toBeNull();
  });

  it('entryNeedsReady:false: the same break is a LONG (FLAG) entry, and says it came before the EMA caught up', () => {
    const r = run(early, { entryNeedsReady: false });
    expect(r.events.map((e) => `${e.kind}:${e.marker ?? '-'}`)).toEqual(['impulse:-', 'flat:-', 'entry:buy']);
    expect(r.events[2]!.label).toBe('LONG (FLAG)');
    expect(r.events[2]!.note).toContain('before the 9 EMA caught up');
    expect(r.boxes.map((b) => b.outcome)).toEqual(['broke']);
    expect(r.state).toBe('long');
  });
});

// ─── F5 — fail ───────────────────────────────────────────────────────────

describe('F5 fail', () => {
  it('a close 1.5% under the EMA while a flag is active -> FLAG FAILED, outcome failed, state idle', () => {
    // EMA@23 is 1.1085; c24 = 1.0878 puts the close exactly 1.5% under EMA@24.
    const crash = bar(24, 1.18, 1.185, 1.08, 1.0878, 5000);
    const all = [...baseBars(), THRUST, ...flatBars(0, 3), crash];
    const ema = emaSeries(closesOf(all), 9) as number[];
    expect(1 - 1.0878 / ema[24]!).toBeCloseTo(0.015, 4);
    const r = run(all);
    expect(r.events.map((e) => `${e.kind}:${e.label}`)).toEqual(['impulse:BREAKOUT', 'flat:FLAT', 'failed:FLAG FAILED']);
    expect(r.events[2]!.marker).toBeUndefined();
    expect(r.events[2]!.price).toBe(1.0878);
    expect(r.events[2]!.note).toBe('closed 1.5% under the 9 EMA — flag failed');
    expect(r.boxes.map((b) => b.outcome)).toEqual(['failed']);
    expect(r.state).toBe('idle');
    // the impulse is over: a later bar, back above, does not resurrect the flag
    const later = run([...all, bar(25, 1.09, 1.1, 1.08, 1.095)]);
    expect(later.events.length).toBe(3);
  });

  it('a close only 0.5% under the EMA does NOT fail the flag (the gate is more than emaBreakPct)', () => {
    const mild = bar(24, 1.18, 1.185, 1.1, 1.103, 5000); // ema@24 ~1.1055: 0.2% under
    const r = run([...baseBars(), THRUST, ...flatBars(0, 3), mild]);
    expect(r.events.some((e) => e.kind === 'failed')).toBe(false);
  });

  it('a long is NOT closed by FLAG FAILED — only by its own stop', () => {
    // entry at 27, then a bar closing 1.5% under its EMA but still above the stop (hard-coded trail: trailEma off).
    // The flag has a wick at 26 (low 1.14, under the EMA 1.149 — closes still hold it): without one the flag's lows
    // sit ABOVE the EMA, the stop is flagLow * 0.99 ~ 1.164, and no close can be both above it and 1% under the EMA.
    const wicked = flatBars().map((b, k, a) => (k === a.length - 1 ? { ...b, low: 1.14 } : b));
    const all = [...baseBars(), THRUST, ...wicked, BREAK];
    const dip = bar(28, 1.2, 1.2, 1.12, 1.14, 5000);
    const r = run([...all, dip], { trailEma: false });
    const ema = emaSeries(closesOf([...all, dip]), 9) as number[];
    const stop = Math.max(1.14, ema[26]!) * 0.99; // ~1.1375
    expect(1.14).toBeGreaterThan(stop); // above the (untrailed) stop ...
    expect(1.14).toBeLessThan(ema[28]! * 0.99); // ... yet more than 1% under the EMA
    expect(r.events.some((e) => e.kind === 'exit')).toBe(false);
    expect(r.state).toBe('long');
  });
});

// ─── F6 — fade ───────────────────────────────────────────────────────────

describe('F6 fade', () => {
  // the thrust bar plus the closes 1.18/1.184 open the flag at 22 (bar 23's 1.188
  // joins it); the close at 24 (1.115) widens the closes to 0.073 > 35% of the
  // 0.202 move (0.0707) without breaking.
  const fade = [
    ...baseBars(),
    THRUST,
    ...flatBars(0, 3),
    bar(24, 1.14, 1.15, 1.105, 1.115, 2000),
    bar(25, 1.115, 1.12, 1.11, 1.116, 1500),
    bar(26, 1.116, 1.12, 1.11, 1.115, 1500),
  ];

  it('the flag fades silently (no event, outcome faded) and a NEW flat is found from later bars', () => {
    const afterFade = run(fade.slice(0, 25)); // through bar 24
    expect(afterFade.events.map((e) => e.kind)).toEqual(['impulse', 'flat']);
    expect(afterFade.boxes.map((b) => b.outcome)).toEqual(['faded']);
    expect(afterFade.state).toBe('impulse');

    const r = run(fade);
    expect(r.events.map((e) => `${(e.ts - ANCHOR) / MIN} ${e.kind}`)).toEqual(['20 impulse', '22 flat', '26 flat']);
    expect(r.boxes.map((b) => b.outcome)).toEqual(['faded', 'active']);
    // the new flag hangs from the SAME top (the high it is holding under) and is ready at once (1.115 is ~0.3% over its EMA)
    expect(r.boxes[1]!.startTs).toBe(tsAt(20));
    expect(r.boxes[1]!.high).toBe(1.2);
    expect(r.boxes[1]!.readyTs).toBe(tsAt(26));
    expect(r.state).toBe('ready');
  });

  it('a poke through the flag high that stays under the trigger also closes the flag quietly (the top moved)', () => {
    const poke = bar(24, 1.18, 1.2015, 1.18, 1.19, 2000); // 1.2015 > 1.20 but < 1.2036
    const r = run([...baseBars(), THRUST, ...flatBars(0, 3), poke]);
    expect(r.events.map((e) => e.kind)).toEqual(['impulse', 'flat']);
    expect(r.boxes.map((b) => b.outcome)).toEqual(['faded']);
    expect(r.state).toBe('impulse');
    expect(r.levels.top).toBe(1.2015);
  });
});

// ─── F7 — adds cap ───────────────────────────────────────────────────────

describe('F7 adds cap', () => {
  // A staircase: thrust, three flat bars, thrust ... Each thrust breaks the
  // flag before it AND tops a new leg. readyDistPct 50 makes every flag READY
  // the moment it opens, so the test is about the cap, not the EMA race.
  function staircase(thrusts: number): Candle[] {
    const out = baseBars();
    let i = 20;
    let px = 1.0;
    for (let t = 0; t < thrusts; t++) {
      const o = px;
      const c = o * 1.09;
      out.push(bar(i++, o, o * 1.1, o * 0.998, c, 8000));
      for (let k = 0; k < 3; k++) out.push(bar(i++, c, c * 1.003, c * 0.998, c, 1000));
      px = c;
    }
    return out;
  }
  const params: Partial<FlagParams> = { readyDistPct: 50 };

  it('entry + 2 adds, and the third later break adds nothing (the flag is spent: outcome broke, no event)', () => {
    // 5 thrusts: T0 starts the impulse; T1 breaks flag 1 (entry), T2 flag 2 (add), T3 flag 3 (add), T4 flag 4 (at the cap)
    const candles = staircase(5);
    const r = run(candles, params);
    const trades = r.events.filter((e) => e.kind === 'entry' || e.kind === 'add').map((e) => e.label);
    expect(trades).toEqual(['LONG (FLAG)', 'ADD (FLAG)', 'ADD (FLAG)']);
    // five flags: after T0, T1, T2, T3 (each broken by the next thrust) and after T4 (still open)
    expect(r.events.filter((e) => e.kind === 'flat').length).toBe(5);
    expect(r.boxes.map((b) => b.outcome)).toEqual(['broke', 'broke', 'broke', 'broke', 'active']);
    // the 4th break is silent: no event at the T4 bar (index 20 + 4*4)
    const t4 = tsAt(36);
    expect(r.events.some((e) => e.ts === t4)).toBe(false);
    expect(r.levels.adds).toBe(2);
    expect(r.state).toBe('long');
    expect(r.events.some((e) => e.kind === 'exit')).toBe(false);
    // an add never moves the entry: it stays the FIRST break (T0's top 1.10 * 1.003)
    expect(r.levels.entryPrice).toBeCloseTo(1.1 * 1.003, 10);
    expect(r.detail[0]).toMatch(/ · adds 2\/2$/);
  });

  it('maxAdds 0: the entry happens, every later break is silent', () => {
    const r = run(staircase(4), { ...params, maxAdds: 0 });
    expect(r.events.filter((e) => e.kind === 'add').length).toBe(0);
    expect(r.events.filter((e) => e.kind === 'entry').length).toBe(1);
  });

  it('an add never moves the stop below what the trail already raised it to', () => {
    const before = run(staircase(3), params); // entry + 1 add so far
    const after = run(staircase(4), params); // + the second add
    expect(after.levels.stop!).toBeGreaterThanOrEqual(before.levels.stop!);
  });
});

// ─── F8 — stopByClose:false ──────────────────────────────────────────────

describe('F8 stopByClose:false (a hard stop order)', () => {
  const touch = bar(28, 1.205, 1.215, 1.13, 1.2, 5000); // low 1.13 touches the stop, close 1.20 holds above it
  const upTo27 = [...baseBars(), THRUST, ...flatBars(), BREAK];
  const ema = emaSeries(closesOf([...upTo27, touch]), 9) as number[];
  // flag low 1.176 (after the thrust) * 0.99 ~ 1.164: a wick to 1.13 trades through it; the entry bar's
  // own trail raise (EMA@27 * 0.99 ~ 1.149) does not lift it
  const stop = Math.max(FLAG_LOW, ema[26]!, ema[27]!) * 0.99;

  it('default (stop by close): the wick does nothing — the bar closed above the stop', () => {
    const r = run([...upTo27, touch]);
    expect(r.events.some((e) => e.kind === 'exit')).toBe(false);
    expect(r.state).toBe('long');
  });

  it('exit on a LOW at/under the stop, at the stop price, on the closed bar', () => {
    expect(touch.low).toBeLessThanOrEqual(stop);
    const r = run([...upTo27, touch], { stopByClose: false });
    const exit = r.events.find((e) => e.kind === 'exit')!;
    expect(exit.ts).toBe(tsAt(28));
    expect(exit.price).toBeCloseTo(stop, 12);
    expect(exit.marker).toBe('sell');
    expect(exit.note).toBe(`traded down to the stop ${fmtPx(stop)} — exit at ${fmtPx(stop)}, ${((stop / (1.2 * 1.003) - 1) * 100).toFixed(1)}% from ${fmtPx(1.2 * 1.003)}`);
    // the close (1.20) is well above the EMA, so the impulse is still live
    expect(r.state).toBe('impulse');
  });

  it('...and also on the FORMING bar, identically, with nothing else invented', () => {
    const forming = run([...upTo27, touch], { stopByClose: false }, tsAt(28) + 20_000);
    const closed = run([...upTo27, touch], { stopByClose: false });
    const fExit = forming.events.filter((e) => e.kind === 'exit');
    expect(fExit.length).toBe(1);
    expect(forming.events.map(keyOf)).toEqual(closed.events.slice(0, forming.events.length).map(keyOf));
    expect(forming.state).not.toBe('long');
    // a forming bar that has not touched the stop yet exits nothing
    const calm = bar(28, 1.205, 1.215, 1.2, 1.21);
    const calmForming = run([...upTo27, calm], { stopByClose: false }, tsAt(28) + 20_000);
    expect(calmForming.events.some((e) => e.kind === 'exit')).toBe(false);
    expect(calmForming.state).toBe('long');
  });

  it('a bar that breaks the flag AND trades down to the stop shows entry then exit on the forming bar and on the close alike', () => {
    const wick = bar(27, 1.19, 1.21, 1.1, 1.2, 4000); // breaks 1.2036 and wicks under the ~1.164 stop
    const base = [...baseBars(), THRUST, ...flatBars(), wick];
    const forming = run(base, { stopByClose: false }, tsAt(27) + 20_000);
    const closed = run(base, { stopByClose: false });
    const tail = (r: FlagResult) => r.events.slice(-2).map((e) => e.kind);
    expect(tail(forming)).toEqual(['entry', 'exit']);
    expect(tail(closed)).toEqual(['entry', 'exit']);
  });
});

// ─── Other rules ─────────────────────────────────────────────────────────

describe('days and warm-up', () => {
  it('no candles / too few to have an EMA: idle, no events, no throw', () => {
    const empty = evaluateFlag({ candles: [], tfMs: MIN, nowMs: 0 });
    expect(empty.state).toBe('idle');
    expect(empty.dayKey).toBe('');
    expect(empty.events).toEqual([]);
    expect(empty.boxes).toEqual([]);
    const few = run(baseBars().slice(0, 5));
    expect(few.state).toBe('idle');
    expect(few.levels.ema).toBeNull();
    expect(few.evidence.emaDistPct).toBeNull();
  });

  it('an impulse needs >= 5 earlier bars the SAME ET day: the same thrust is ignored as bar 3 of a new day and armed as bar 5', () => {
    const day1 = baseBars();
    const mk = (thrustIdx: number): Candle[] => {
      const d2: Candle[] = [];
      for (let k = 0; k < thrustIdx; k++) d2.push(bar(k, 1.0, 1.002, 0.998, 1.0, 1000, DAY));
      d2.push(bar(thrustIdx, 1.0, 1.2, 0.999, 1.18, 9000, DAY));
      return [...day1, ...d2];
    };
    expect(run(mk(3)).events).toEqual([]);
    expect(run(mk(4)).events).toEqual([]);
    const r = run(mk(5));
    expect(r.events.map((e) => e.kind)).toEqual(['impulse']);
    expect(r.dayKey).toBe(etDayKey(tsAt(0, DAY)));
  });

  it('the state machine is per ET day: a position open at the end of day 1 does not leak into day 2, yet its events are returned and the EMA stays continuous', () => {
    const day1 = [...baseBars(), THRUST, ...flatBars(), BREAK]; // ends long
    const day2 = Array.from({ length: 12 }, (_, i) => bar(i, 1.19, 1.195, 1.185, 1.19, 1000, DAY));
    const r = run([...day1, ...day2]);
    expect(r.state).toBe('idle'); // day 2 has its own (empty) machine
    expect(r.events.map((e) => e.kind)).toEqual(['impulse', 'flat', 'emaNear', 'entry']); // all from day 1
    expect(etDayKey(r.events[0]!.ts)).not.toBe(r.dayKey);
    const ema = emaSeries(closesOf([...day1, ...day2]), 9);
    expect(r.levels.ema).toBe(ema[ema.length - 1]);
    expect(r.levels.entryPrice).toBeNull();
    // and day 1 alone is still long (the open position is real, just not carried)
    expect(run(day1).state).toBe('long');
  });
});

// ─── F9 — append-only on every real fixture ──────────────────────────────

describe('F9 append-only: bar-by-bar replay of every fixture (last bar forming, then closed)', () => {
  const cases: [Fixture, string][] = [...FIXTURES_1005.map((f): [Fixture, string] => [f, '2026-10-05']), ...FIXTURES_0924.map((f): [Fixture, string] => [f, '2026-09-24'])];
  for (const [fx, dayKey] of cases) {
    it(`${fx.sym} ${dayKey}: closed history is a strict prefix, forming events reappear identically, no duplicates`, () => {
      const candles = fx.bars.map(toCandle);
      const final = expectAppendOnlyReplay(candles, dayKey, MIN);
      // the replay's last closed step IS the full evaluation
      const full = run(candles);
      expect(final.events.map(keyOf)).toEqual(full.events.map(keyOf));
    }, 60_000);
  }
  it('prints each 2026-10-05 fixture\'s events once (the orchestrator reads them)', () => {
    const blocks = FIXTURES_1005.map((fx) => {
      const full = run(fx.bars.map(toCandle));
      const rows = todayEvents(full).map((e) => `  ${line(e)}`);
      return `[F9] ${fx.sym} ${fx.date} (${rows.length} events today, state ${full.state})\n${rows.join('\n') || '  (none)'}`;
    });
    console.log(`\n${blocks.join('\n')}\n`);
    expect(blocks.length).toBe(FIXTURES_1005.length);
  });
});

// The same contract with the rules that change WHEN things fire: a hard stop
// (checked on the forming bar by low), entries without waiting for READY, and
// loose gates that make the engine trade far more often than the defaults.
describe('F9b append-only under non-default params (hard stop, no READY gate, loose gates)', () => {
  const variants: [string, Partial<FlagParams>][] = [
    ['stopByClose:false', { stopByClose: false }],
    ['entryNeedsReady:false', { entryNeedsReady: false }],
    ['hard stop + no READY gate + 4 adds + no trail', { stopByClose: false, entryNeedsReady: false, maxAdds: 4, trailEma: false }],
    ['loose: ext 3%, flat 2 bars, ready 8%', { minExtPct: 3, minFlatBars: 2, readyDistPct: 8, maxFlatPct: 15 }],
    ['minFlatBars 1 (a flag can open on the top bar itself)', { minFlatBars: 1 }],
  ];
  for (const [name, params] of variants) {
    for (const fx of [beatFixture, alecFixture, apusFixture, veeaFixture] as Fixture[]) {
      it(`${fx.sym} with ${name}`, () => {
        const candles = fx.bars.map(toCandle);
        const final = expectAppendOnlyReplay(candles, '2026-10-05', MIN, params);
        expect(final.events.map(keyOf)).toEqual(run(candles, params).events.map(keyOf));
      }, 60_000);
    }
  }
});

// ─── F10 — 5-minute candles ──────────────────────────────────────────────

describe('F10 5-minute candles (aggregated through chartAgg) on BEAT', () => {
  const candles = aggregate(beatFixture.bars as Bar[], 5);
  const TF5 = 5 * MIN;

  it('runs and is append-only bar by bar', () => {
    const r = run5(candles);
    expect(r.dayKey).toBe('2026-10-05');
    // only the first leg arms anything: later 5m bars keep setting new tops, the 08:35-08:45 window's low (0.47) is
    // under the 50% retrace floor and the 09:05-09:15 closes span 16.6% of price (cap 10%) — no flag ever opens
    expect(todayEvents(r).map(line)).toEqual(['08:30 impulse BREAKOUT 0.5412']);
    const final = expectAppendOnlyReplay(candles, '2026-10-05', TF5);
    expect(final.events.map(keyOf)).toEqual(r.events.map(keyOf));
  });

  it('prints the 5-minute events', () => {
    const r = run5(candles);
    const rows = todayEvents(r).map((e) => `  ${line(e)}`);
    console.log(`\n[F10] BEAT 5-minute 2026-10-05 (${rows.length} events today, state ${r.state})\n${rows.join('\n') || '  (none)'}\n`);
    for (const e of r.events) expect(e.side).toBe('long');
  });

  function run5(cs: Candle[]): FlagResult {
    return evaluateFlag({ candles: cs, tfMs: TF5, nowMs: cs[cs.length - 1]!.ts + TF5 + 1 });
  }
});

// ─── F11 — timing ────────────────────────────────────────────────────────

describe('F11 timing', () => {
  it('evaluateFlag on the largest fixture is under 5 ms (median)', () => {
    const all = [...FIXTURES_1005, ...FIXTURES_0924];
    const biggest = all.reduce((a, b) => (b.bars.length > a.bars.length ? b : a));
    const candles = biggest.bars.map(toCandle);
    const last = candles[candles.length - 1]!;
    const nowMs = last.ts + 20_000; // forming, the live path
    for (let k = 0; k < 5; k++) evaluateFlag({ candles, tfMs: MIN, nowMs });
    const times: number[] = [];
    for (let k = 0; k < 41; k++) {
      const t0 = performance.now();
      evaluateFlag({ candles, tfMs: MIN, nowMs });
      times.push(performance.now() - t0);
    }
    times.sort((a, b) => a - b);
    const median = times[Math.floor(times.length / 2)]!;
    console.log(`\n[F11] evaluateFlag on ${biggest.sym} (${candles.length} bars): median ${median.toFixed(3)} ms, p90 ${times[Math.floor(times.length * 0.9)]!.toFixed(3)} ms, max ${times[times.length - 1]!.toFixed(3)} ms\n`);
    expect(median).toBeLessThan(5);
  });
});

// ─── F12 — the flag is measured AFTER its top bar (review F-1) ───────────

describe('F12 the flag is measured after the top bar (the impulse bar is not part of its own flag)', () => {
  const beat = (beatFixture.bars as Bar[]).map(toCandle);

  it('BEAT: flag 2 is FLAT at 09:03 with its own lows, flag 3 fails at 09:15 instead of fading a bar early', () => {
    const r = run(beat);
    const t = todayEvents(r).map(line);
    expect(t).toContain('09:03 flat FLAT 0.8626');
    expect(t).toContain('09:15 failed FLAG FAILED 0.8016');
    // the 09:01 thrust bar's low (0.6449) is not the flag's low: the flag's lows are >= 0.7531
    expect(r.boxes[1]!.low).toBe(0.7531);
    expect(r.boxes[2]!.outcome).toBe('failed');
  });

  it('QTEX: one top and one trigger give one FLAT (no repeat after a fade on the top-bar close)', () => {
    const q = run((qtexFixture.bars as Bar[]).map(toCandle));
    const same = todayEvents(q).filter((e) => e.kind === 'flat' && Math.abs(e.price - 1.63 * 1.003) < 1e-9);
    expect(same.length).toBe(1);
  });

  it('minFlatBars 1 can open the flag on the top bar itself, and nothing it prints is Infinity or NaN', () => {
    for (const fx of [...FIXTURES_1005, ...FIXTURES_0924]) {
      const rr = run(fx.bars.map(toCandle), { minFlatBars: 1 });
      for (const b of rr.boxes) {
        expect(Number.isFinite(b.low), `${fx.sym} box low`).toBe(true);
        expect(b.low).toBeLessThanOrEqual(b.high);
      }
      for (const e of rr.events) expect(`${e.note}|${e.price}`, `${fx.sym} ${e.label}`).not.toMatch(/Infinity|NaN/);
      expect(JSON.stringify(rr.levels) + JSON.stringify(rr.evidence)).not.toMatch(/Infinity|NaN/);
    }
  });
});

// ─── F13 — the entry bar's own close raises the trail (review F-2) ───────

describe('F13 the bar that opened the position raises the EMA trail like any later bar', () => {
  it('BEAT through the closed 09:00 bar: the stop is already max(initial stop, EMA@09:00 * 0.99)', () => {
    const c = (beatFixture.bars as Bar[]).map(toCandle);
    const j = c.findIndex((x) => etDayKey(x.ts) === '2026-10-05' && fmtEt(x.ts) === '09:00') + 1;
    const r = run(c.slice(0, j));
    expect(r.state).toBe('long');
    expect(r.levels.stop!).toBeCloseTo(0.60747, 4); // without the entry-bar raise: 0.599445
  });
});

// ─── F14 — which side of the EMA the price is on (review F-3) ────────────

describe('F14 a ready flag whose close is under the EMA says so (the floor lets it sit up to emaBreakPct under)', () => {
  const dcx = (dcxFixture.bars as Bar[]).map(toCandle);
  const gctk = (gctkFixture.bars as Bar[]).map(toCandle);
  const through = (cs: Candle[], hhmm: string): Candle[] => cs.slice(0, cs.findIndex((x) => etDayKey(x.ts) === '2026-09-24' && fmtEt(x.ts) === hhmm) + 1);

  it('DCX 05:30: close 0.1376 is UNDER the 9 EMA 0.1387 — the READY detail does not claim the EMA is below', () => {
    const r = run(through(dcx, '05:30'));
    expect(r.state).toBe('ready');
    expect(r.evidence.emaDistPct!).toBeLessThan(0);
    expect(r.detail[0]).not.toMatch(/EMA [\d.]+% below/);
    expect(r.detail[0]).toContain('EMA 0.8% above (price under it)');
  });

  it('GCTK 04:17: the FLAT event note names the under-EMA close instead of printing "(-0.2% above)"', () => {
    const r = run(through(gctk, '04:17'));
    const flat = r.events.find((e) => e.kind === 'flat' && fmtEt(e.ts) === '04:17')!;
    expect(flat.note).not.toMatch(/\(-/);
    expect(flat.note).toContain('(0.2% under it)');
  });
});

// ─── F15 — flag expiry (maxFlagBars) ─────────────────────────────────────

// A flag whose box spans more than `maxFlagBars` (30) closed bars, counted from the top bar inclusive, without
// breaking ends 'stale': silent, the state falls back to impulse, and no flag may open under that top again.
describe('F15 flag expiry: a flag that has not resolved in 30 bars is a range', () => {
  // The thrust (bar 20, top 1.20, base low 0.998, move 0.202) and then 40 bars of sideways drift: the closes cycle
  // 1.180 / 1.184 / 1.188 / 1.192 (a 0.012 span, 6% of the move), every high 0.004 over its close (never above the
  // 1.20 top, so the top never moves and the trigger 1.2036 is never touched), every low 0.004 under (far above
  // the retrace gate 1.099).
  const driftBars = (from: number, to: number): Candle[] =>
    Array.from({ length: to - from + 1 }, (_, k) => {
      const i = from + k;
      const c = 1.18 + (i % 4) * 0.004;
      return bar(i, c - 0.001, c + 0.004, c - 0.004, c, 1200);
    });
  const range = [...baseBars(), THRUST, ...driftBars(21, 60)];
  const through = (n: number): Candle[] => range.slice(0, n + 1); // bars 0..n (the array index is the bar number)
  const stamps = (r: FlagResult): string[] => r.events.map((e) => `${(e.ts - ANCHOR) / MIN} ${e.kind}`);

  it('the flag is open at 30 bars from the top and expires on the 31st: silent, impulse, box stale', () => {
    // bar 20 (the top) is bar 1, so bar 49 is the 30th: still a flag, READY since the EMA caught up at 26.
    const at49 = run(through(49));
    expect(at49.state).toBe('ready');
    expect(at49.evidence.flatBars).toBe(30);
    expect(at49.boxes.map((b) => b.outcome)).toEqual(['active']);
    expect(stamps(at49)).toEqual(['20 impulse', '22 flat', '26 emaNear']);

    // bar 50 is the 31st: the flag ends, and nothing is printed for it.
    const at50 = run(through(50));
    expect(at50.state).toBe('impulse');
    expect(at50.evidence.flatBars).toBeNull();
    expect(at50.levels.trigger).toBeNull();
    expect(at50.levels.flagHigh).toBeNull();
    expect(at50.levels.top).toBe(1.2); // the impulse itself is alive: the EMA is still under the price
    expect(stamps(at50)).toEqual(stamps(at49)); // silent, like 'faded'
    expect(at50.boxes.map((b) => b.outcome)).toEqual(['stale']);
    expect(at50.boxes[0]!.startTs).toBe(tsAt(20));
    expect(at50.boxes[0]!.endTs).toBe(tsAt(50)); // 31 bars: the box keeps the bar that expired it
    expect(at50.boxes[0]!.readyTs).toBe(tsAt(26));
    expect(at50.headline).toBe('RANGE — no flag: the top is over 30 bars old');
    expect(at50.detail[1]).toBe(`A new flag needs a new high above ${fmtPx(1.2)}`);
  });

  it('after the expiry no FLAT is re-emitted for the same top, for as long as the drift lasts', () => {
    const r = run(range); // through bar 60: 11 bars past the expiry
    expect(stamps(r)).toEqual(['20 impulse', '22 flat', '26 emaNear']);
    expect(r.boxes.length).toBe(1);
    expect(r.state).toBe('impulse');
    expect(r.levels.stop).toBeNull();
    // the same bars with the expiry off: ONE flag, 41 bars old, READY — what PTC showed for six hours
    const off = run(range, { maxFlagBars: 0 });
    expect(off.state).toBe('ready');
    expect(off.evidence.flatBars).toBe(41);
    expect(off.boxes.map((b) => b.outcome)).toEqual(['active']);
    expect(stamps(off)).toEqual(stamps(r));
    // and the forming clock on the last bar tells the same story (the expiry is a closed-bar rule)
    expect(run(range, undefined, tsAt(60) + 30_000).state).toBe('impulse');
  });

  it('a NEW high is a new top: it clears the RANGE call and a fresh flag from it opens, goes READY and is a real flag', () => {
    // bar 61 prints 1.215 — through the old trigger 1.2036, but there is no flag any more, so it is not an entry, not an
    // EARLY BREAK: just a higher top. Bars 62-63 park under it, tight: FLAT on 63 (the top bar counts as one of the three).
    const newTop = [bar(61, 1.2, 1.215, 1.19, 1.205, 5000), bar(62, 1.205, 1.212, 1.2, 1.206, 1500), bar(63, 1.206, 1.213, 1.2, 1.207, 1500)];
    const at61 = run([...range, newTop[0]!]);
    expect(at61.state).toBe('impulse');
    expect(at61.levels.top).toBe(1.215);
    expect(at61.headline).toBe('BREAKOUT — wait for it to go sideways'); // no longer the expired top
    expect(stamps(at61)).toEqual(['20 impulse', '22 flat', '26 emaNear']);
    const r = run([...range, ...newTop]);
    expect(stamps(r)).toEqual(['20 impulse', '22 flat', '26 emaNear', '63 flat']);
    expect(r.events.some((e) => e.kind === 'entry' || e.kind === 'earlyBreak')).toBe(false);
    expect(r.state).toBe('ready'); // 1.207 is ~1.5% over its EMA: READY on the FLAT bar
    expect(r.levels.trigger).toBeCloseTo(1.215 * 1.003, 10);
    expect(r.boxes.map((b) => b.outcome)).toEqual(['stale', 'active']);
    expect(r.boxes[1]!.startTs).toBe(tsAt(61));
    expect(r.evidence.flatBars).toBe(3);
    // ...and it can be traded: the break of the new trigger is a LONG
    const brk = run([...range, ...newTop, bar(64, 1.207, 1.22, 1.205, 1.219, 4000)]);
    expect(stamps(brk).slice(-2)).toEqual(['63 flat', '64 entry']);
    expect(brk.boxes.map((b) => b.outcome)).toEqual(['stale', 'broke']);
  });

  it('a break on the 31st bar is still a break: the intrabar rule runs before the expiry', () => {
    const r = run([...through(49), bar(50, 1.19, 1.21, 1.19, 1.205, 4000)]);
    expect(stamps(r).slice(-1)).toEqual(['50 entry']);
    expect(r.boxes.map((b) => b.outcome)).toEqual(['broke']);
    expect(r.state).toBe('long');
  });

  it('the bar that expires a flag prints no 9 EMA NEAR (the expiry runs before the READY test)', () => {
    // Without a limit the EMA catches up on bar 26 (flag bars: 26 - 20 + 1 = 7). Limit 6 expires the flag on that very bar.
    const off = run(through(30), { maxFlagBars: 0 });
    expect(stamps(off)).toEqual(['20 impulse', '22 flat', '26 emaNear']);
    const six = run(through(30), { maxFlagBars: 6 });
    expect(stamps(six)).toEqual(['20 impulse', '22 flat']);
    expect(six.boxes.map((b) => `${b.outcome} ${(b.endTs - ANCHOR) / MIN} ${b.readyTs}`)).toEqual(['stale 26 null']);
    expect(six.state).toBe('impulse');
    // limit 7: the 7th bar is still a flag, READY on it, and it expires the bar after
    const seven = run(through(30), { maxFlagBars: 7 });
    expect(stamps(seven)).toEqual(['20 impulse', '22 flat', '26 emaNear']);
    expect(seven.boxes.map((b) => `${b.outcome} ${(b.endTs - ANCHOR) / MIN}`)).toEqual(['stale 27']);
  });

  it('a top already older than the limit refuses a flag (born stale): a faded flag cannot re-open from it, a young top still can (F6)', () => {
    // F6's sequence: the flag opens at 22 (5 bars from the top at 24 when the 1.115 close fades it) and a NEW flat opens
    // at 26 — 7 bars from the top. Limit 6: that flat would be born stale, so it never opens and prints no FLAT.
    const fade = [...baseBars(), THRUST, ...flatBars(0, 3), bar(24, 1.14, 1.15, 1.105, 1.115, 2000), bar(25, 1.115, 1.12, 1.11, 1.116, 1500), bar(26, 1.116, 1.12, 1.11, 1.115, 1500)];
    const six = run(fade, { maxFlagBars: 6 });
    expect(stamps(six)).toEqual(['20 impulse', '22 flat']);
    expect(six.boxes.map((b) => b.outcome)).toEqual(['faded']);
    expect(six.state).toBe('impulse');
    expect(six.headline).toBe('RANGE — no flag: the top is over 6 bars old');
    expect(stamps(run(fade, { maxFlagBars: 7 }))).toEqual(['20 impulse', '22 flat', '26 flat']);
    expect(stamps(run(fade))).toEqual(['20 impulse', '22 flat', '26 flat']);
  });

  it('a position is not touched by an expiring flag: the long keeps its entry, nothing is exited or added', () => {
    // F3's flag breaks on bar 27 (flag bars 8, inside a limit of 12) and opens the long; bars 28-45 park under the new
    // top 1.21 (bar 27's high), a second flag opens on 29 and expires on 39 (13 bars from its top) while the position
    // is open: no exit, no add, the position and its entry survive.
    const park = Array.from({ length: 18 }, (_, k) => bar(28 + k, 1.205, 1.209, 1.2, 1.204 + (k % 2) * 0.002, 1500));
    const all = [...baseBars(), THRUST, ...flatBars(), BREAK, ...park];
    const r = run(all, { maxFlagBars: 12 });
    expect(stamps(r).filter((x) => /entry|exit|add/.test(x))).toEqual(['27 entry']);
    expect(r.boxes.map((x) => x.outcome)).toEqual(['broke', 'stale']);
    expect(r.boxes[1]!.startTs).toBe(tsAt(27));
    expect(r.boxes[1]!.endTs).toBe(tsAt(39));
    expect(r.state).toBe('long');
    expect(r.levels.entryPrice).toBeCloseTo(1.2 * 1.003, 10);
    expect(r.levels.trigger).toBeNull(); // the second flag is gone; the long has no prospective flag
  });
});

// PTC (a $192 stock, gapped +34% from ~144 at 04:13, drifted sideways all day), captured at 13:25 ET: before the
// expiry the engine held ONE 'active' flag from 06:34 to 13:24 — 373 bars, READY the whole time, trigger 199.27.
describe('F16 real PTC 2026-10-05 13:25: nine hours sideways is a range, not a flag', () => {
  const candles = (ptcFixture.bars as Bar[]).map(toCandle);
  const r = run(candles);
  const old = run(candles, { maxFlagBars: 0 });
  const box = (b: FlagResult['boxes'][number]): string => `${fmtEt(b.startTs)}-${fmtEt(b.endTs)} ${fmtPx(b.low)}-${fmtPx(b.high)} ready ${b.readyTs ? fmtEt(b.readyTs) : '-'} ${b.outcome}`;
  const idxOf = (ts: number): number => candles.findIndex((c) => c.ts === ts);

  it('without the expiry (the old behaviour): seven boxes, the last one active for 373 bars and the state READY at 13:24', () => {
    expect(old.boxes.map(box)).toEqual([
      '04:13-04:37 193.16-194.99 ready 04:17 faded',
      '04:38-05:37 193.51-195.07 ready 04:43 faded',
      '05:53-06:05 194.65-195.69 ready 06:00 faded',
      '06:13-06:19 196.31-197.19 ready 06:16 faded',
      '06:20-06:24 197.01-197.50 ready 06:23 faded',
      '06:28-06:32 197.75-198.12 ready 06:31 faded',
      '06:34-13:24 192.47-198.67 ready 06:40 active',
    ]);
    expect(old.state).toBe('ready');
    expect(old.evidence.flatBars).toBe(373);
    expect(old.levels.trigger).toBeCloseTo(198.67 * 1.003, 6); // 199.27
  });

  it('with it: the same six faded boxes, the 06:34 flag ends STALE after 31 bars, and the state is impulse, not ready', () => {
    expect(r.boxes.map(box)).toEqual([
      '04:13-04:37 193.16-194.99 ready 04:17 faded',
      '04:38-05:37 193.51-195.07 ready 04:43 faded',
      '05:53-06:05 194.65-195.69 ready 06:00 faded',
      '06:13-06:19 196.31-197.19 ready 06:16 faded',
      '06:20-06:24 197.01-197.50 ready 06:23 faded',
      '06:28-06:32 197.75-198.12 ready 06:31 faded',
      '06:34-07:16 195.67-198.67 ready 06:40 stale',
    ]);
    // 'bars' are bars: PTC prints no bar in a minute without a trade, so 31 bars from 06:34 end at 07:16
    const last = r.boxes[r.boxes.length - 1]!;
    expect(idxOf(last.endTs) - idxOf(last.startTs) + 1).toBe(31);
    expect(r.state).toBe('impulse');
    expect(r.evidence.flatBars).toBeNull();
    expect(r.levels.trigger).toBeNull();
    expect(r.levels.top).toBe(198.67);
    expect(r.headline).toBe('RANGE — no flag: the top is over 30 bars old');
  });

  it('no active flag is older than 30 bars at ANY point of the day, and the events are the old ones: the expiry is silent', () => {
    expect(r.boxes.filter((b) => b.outcome === 'active')).toEqual([]);
    for (let n = 1; n <= candles.length; n += 7) {
      const slice = run(candles.slice(0, n));
      if (slice.state === 'flat' || slice.state === 'ready') expect(slice.evidence.flatBars!).toBeLessThanOrEqual(30);
    }
    expect(r.events.map(keyOf)).toEqual(old.events.map(keyOf));
    expect(todayEvents(r).map(line)).toEqual([
      '04:13 impulse BREAKOUT 194.99',
      '04:17 flat FLAT 195.57',
      '04:43 flat FLAT 195.66',
      '06:00 flat FLAT 196.28',
      '06:16 flat FLAT 197.78',
      '06:23 flat FLAT 198.09',
      '06:31 flat FLAT 198.71',
      '06:40 flat FLAT 199.27',
    ]);
  });

  it('the forming clock on the last bar agrees (the expiry is decided on closed bars)', () => {
    const forming = run(candles, undefined, candles[candles.length - 1]!.ts + 30_000);
    expect(forming.state).toBe('impulse');
    expect(forming.boxes.map(box)).toEqual(r.boxes.map(box));
  });

  it('append-only replay of the whole day (last bar forming, then closed): history is a strict prefix, no duplicates', () => {
    const final = expectAppendOnlyReplay(candles, '2026-10-05', MIN);
    expect(final.events.map(keyOf)).toEqual(r.events.map(keyOf));
    expect(final.boxes.map(box)).toEqual(r.boxes.map(box));
  }, 120_000);
});

// MI, captured at the same moment: no flag of the day outlives 30 bars, so the expiry changes nothing at all.
describe('F17 real MI 2026-10-05 13:25: no flag exceeds 30 bars, so the expiry changes nothing', () => {
  const candles = (mi1325Fixture.bars as Bar[]).map(toCandle);
  const r = run(candles);
  const old = run(candles, { maxFlagBars: 0 });

  it('events, boxes, state and levels are identical with and without the expiry; no stale box', () => {
    expect(r.events.map(keyOf)).toEqual(old.events.map(keyOf));
    expect(r.boxes).toEqual(old.boxes);
    expect(r.state).toBe(old.state);
    expect(r.levels).toEqual(old.levels);
    expect(r.evidence).toEqual(old.evidence);
    expect(r.headline).toBe(old.headline);
    expect(r.boxes.some((b) => b.outcome === 'stale')).toBe(false);
  });

  it('the day ends READY on a 4-bar flag (13:21 top, trigger 6.42)', () => {
    expect(r.state).toBe('ready');
    expect(r.evidence.flatBars).toBe(4);
    expect(r.boxes.map((b) => `${fmtEt(b.startTs)}-${fmtEt(b.endTs)} ${b.outcome}`).slice(-1)).toEqual(['13:21-13:24 active']);
    expect(r.boxes.map((b) => b.outcome)).toEqual(['failed', 'failed', 'failed', 'early', 'failed', 'broke', 'failed', 'failed', 'failed', 'active']);
  });

  it('append-only replay of the whole day', () => {
    const final = expectAppendOnlyReplay(candles, '2026-10-05', MIN);
    expect(final.events.map(keyOf)).toEqual(r.events.map(keyOf));
  }, 120_000);
});

// ─── Params ──────────────────────────────────────────────────────────────

describe('params', () => {
  it('DEFAULT_FLAG_PARAMS are the spec\'s values', () => {
    expect(DEFAULT_FLAG_PARAMS).toEqual({
      emaPeriod: 9,
      atrPeriod: 14,
      minExtPct: 5,
      breakoutLookback: 30,
      impulseBaseBars: 10,
      minFlatBars: 3,
      maxFlatOfMovePct: 35,
      maxFlatPct: 10,
      maxRetracePct: 50,
      emaBreakPct: 1,
      readyDistPct: 4,
      entryNeedsReady: true,
      breakBufferPct: 0.3,
      stopBufferPct: 1.0,
      trailEma: true,
      stopByClose: true,
      maxAdds: 2,
      maxFlagBars: 30,
      // 9 EMA SLAM (2026-10-07): the brief's starting values except slamBelowPct 2 -> 3 (the 60-day calibration)
      slam: true,
      slamBelowPct: 3,
      slamOpenTolPct: 0.5,
      slamBodyAtr: 1,
      slamMinBodyPct: 2,
      slamCloseInRange: 0.35,
      slamVolX: 1.3,
    });
  });

  it('a partial override only changes what it names; headline text carries the ACTUAL values', () => {
    const idle = run(baseBars(), { minExtPct: 7, breakoutLookback: 12, emaPeriod: 9 });
    expect(idle.headline).toBe('No breakout yet');
    expect(idle.detail).toEqual(['Waiting for a close ≥7% above the 9 EMA on a new 12-bar high']);
    const imp = run([...baseBars(), THRUST], { minFlatBars: 4, maxFlatOfMovePct: 20 });
    expect(imp.detail[1]).toBe('A flag needs 4 bars holding within 20% of the move above the EMA');
  });

  it('the evidence the Signal panel shows is filled from the live flag', () => {
    const r = run([...baseBars(), THRUST, ...flatBars(0, 4)]); // flat at 23, one more bar
    expect(r.state).toBe('flat');
    expect(r.evidence.flatBars).toBe(5); // top bar 20 .. bar 24
    expect(r.evidence.flatSpanPctOfMove!).toBeCloseTo(((1.19 - 1.18) / (1.2 - 0.998)) * 100, 6);
    expect(r.evidence.flatSpanPct!).toBeCloseTo(((1.19 - 1.18) / 1.19) * 100, 6);
    expect(r.evidence.extPct!).toBeCloseTo((1.2 / (1.0 + 0.2 * 0.18) - 1) * 100, 6);
    // the retrace is the flag's own: the lows AFTER the top bar (1.176), not the thrust bar's 0.999
    expect(r.evidence.retracePct!).toBeCloseTo(((1.2 - FLAG_LOW) / (1.2 - 0.998)) * 100, 6);
    expect(r.evidence.emaDistPct!).toBeGreaterThan(4);
    expect(r.evidence.riskPct!).toBeGreaterThan(0);
    expect(r.evidence.openPnlPct).toBeNull();
    // volume: bars after the top (1500 each) over the leg from the base-low bar (10) through the top bar (20)
    expect(r.evidence.volDry!).toBeCloseTo(1500 / mean([...Array(10).fill(1000), 9000]), 10);
    expect(r.levels.atr).not.toBeNull();
  });
});

function mean(v: number[]): number {
  return v.reduce((a, b) => a + b, 0) / v.length;
}

// ─── F18 — 9 EMA SLAM ────────────────────────────────────────────────────
//
// "A candle after a stock has been up and trades more or less sideways SLAMS
// through the 9EMA" (2026-10-07, SPAI 1-minute). An alert out of a flat /
// ready / long episode; it changes no state and no long-side event. Each rule
// is flipped on its own below on ONE synthetic bar 27 out of the READY flag of
// F3 (bars 0..26), then the real SPAI reference, the forming-bar rule, the
// one-per-episode guard and append-only. internal/emaflag/emaflag_test.go
// pins the same bars on the Go side.

describe('F18 9 EMA SLAM', () => {
  const READY = [...baseBars(), THRUST, ...flatBars()]; // state 'ready' after bar 26 (F3)
  // Bar 27: opens 1.19 (above EMA@26 1.149), closes 1.08 = 4.9% under EMA@27 1.135, body 0.11 = 7.6 ATR(14)@26 and
  // 9.2% of the open, close 8% up its range, volume 6000 = 2.9x the mean (2100) of bars 17..26.
  const SLAM = bar(27, 1.19, 1.192, 1.07, 1.08, 6000);
  const slams = (r: FlagResult): FlagEvent[] => r.events.filter((e) => e.kind === 'slam');
  const ema = emaSeries(closesOf([...READY, SLAM]), 9) as number[];

  it("the synthetic slam: one SLAM (9 EMA) at the close, side short, no marker, after the same bar's FLAG FAILED", () => {
    expect(run(READY).state).toBe('ready');
    const r = run([...READY, SLAM]);
    expect(r.events.map((e) => `${(e.ts - ANCHOR) / MIN} ${e.kind}`)).toEqual(['20 impulse', '22 flat', '26 emaNear', '27 failed', '27 slam']);
    const s = slams(r)[0]!;
    expect(s.label).toBe('SLAM (9 EMA)');
    expect(s.price).toBe(1.08);
    expect(s.side).toBe('short');
    expect(s.marker).toBeUndefined();
    expect(s.emaDistPct!).toBeCloseTo((1.08 / ema[27]! - 1) * 100, 12);
    expect(s.emaDistPct!).toBeLessThan(-3);
    expect(s.note).toBe(`${fmtEt(tsAt(27))} slammed through the 9 EMA from above: closed 1.08, 4.9% under the EMA ${fmtPx(ema[27]!)} — a 7.6 ATR body on 2.9× volume`);
    expect(r.evidence.lastSlamTs).toBe(tsAt(27));
    expect(r.evidence.lastSlamClose).toBe(1.08);
    expect(r.evidence.lastSlamPct).toBe(s.emaDistPct);
    // a SLAM changes nothing else: the same run without the rule is the same minus the event
    const off = run([...READY, SLAM], { slam: false });
    expect(off.events.map(keyOf)).toEqual(r.events.filter((e) => e.kind !== 'slam').map(keyOf));
    const blank = (x: FlagResult) => ({ ...x, events: [], evidence: { ...x.evidence, lastSlamTs: null, lastSlamClose: null, lastSlamPct: null } });
    expect(blank(off)).toEqual(blank(r));
    expect(off.evidence.lastSlamTs).toBeNull();
  });

  it('rule 1 (setup): no slam without a flat / ready / long episode at the end of the previous bar', () => {
    // minFlatBars 10: the same bars open no flag (state impulse) -> the identical bar 27 is no slam
    expect(run(READY, { minFlatBars: 10 }).state).toBe('impulse');
    expect(slams(run([...READY, SLAM], { minFlatBars: 10 }))).toEqual([]);
    // idle: a slam-shaped bar straight after the quiet base
    expect(slams(run([...baseBars(), bar(20, 1.0, 1.002, 0.9, 0.91, 9000)]))).toEqual([]);
  });

  it('rule 2a (from above): the previous close must hold the EMA', () => {
    // bar 26 closes 1.13 (UNDER its EMA, inside the 1% floor: the flag stays READY) vs 1.14 (just above it)
    const with26 = (c26: number) => [...baseBars(), THRUST, ...flatBars(0, 5), bar(26, c26 - 0.001, c26 + 0.004, c26 - 0.004, c26, 1500)];
    const s27 = bar(27, 1.135, 1.137, 1.05, 1.06, 6000);
    for (const [c26, fires] of [[1.13, false], [1.14, true]] as const) {
      const pre = with26(c26);
      const e = emaSeries(closesOf([...pre, s27]), 9) as number[];
      expect(run(pre).state, `c26 ${c26}`).toBe('ready');
      expect(c26 >= e[26]!, `c26 ${c26} vs EMA@26 ${e[26]}`).toBe(fires);
      expect(1.135 >= e[26]! * 0.995).toBe(true); // rule 2b holds either way
      expect(slams(run([...pre, s27])).length, `c26 ${c26}`).toBe(fires ? 1 : 0);
    }
  });

  it('rule 2b (from above): the slam bar must open at/above the previous EMA, less slamOpenTolPct', () => {
    // opens 1.14: 0.8% under EMA@26 (1.149), over the 0.5% tolerance; every other rule still holds
    const low = bar(27, 1.14, 1.142, 1.07, 1.08, 6000);
    expect(1.14 < ema[26]! * 0.995 && 1.14 >= ema[26]! * 0.99).toBe(true);
    expect(slams(run([...READY, low]))).toEqual([]);
    expect(slams(run([...READY, low], { slamOpenTolPct: 1 })).length).toBe(1);
  });

  it("rule 3 (through): the close must be slamBelowPct under the bar's own EMA", () => {
    // closes 1.11: 2.7% under EMA@27 (1.141) — through the 1% fail gate, not the 3% slam line
    const shallow = bar(27, 1.19, 1.192, 1.1, 1.11, 6000);
    const e = emaSeries(closesOf([...READY, shallow]), 9) as number[];
    expect(1.11 > e[27]! * 0.97 && 1.11 <= e[27]! * 0.98).toBe(true);
    const r = run([...READY, shallow]);
    expect(r.events.map((x) => x.kind)).toContain('failed');
    expect(slams(r)).toEqual([]);
    expect(slams(run([...READY, shallow], { slamBelowPct: 2 })).length).toBe(1);
  });

  it('rule 4a (hard): the body must be slamBodyAtr ATRs (of the previous bar); 0 switches the ATR test off', () => {
    expect(slams(run([...READY, SLAM], { slamBodyAtr: 8 }))).toEqual([]); // 7.6 ATR
    expect(slams(run([...READY, SLAM], { slamBodyAtr: 7.5 })).length).toBe(1);
    expect(slams(run([...READY, SLAM], { slamBodyAtr: 0 })).length).toBe(1);
  });

  it('rule 4b (hard): the body must be slamMinBodyPct of the open', () => {
    expect(slams(run([...READY, SLAM], { slamMinBodyPct: 10 }))).toEqual([]); // 9.2%
    expect(slams(run([...READY, SLAM], { slamMinBodyPct: 9 })).length).toBe(1);
  });

  it("rule 5 (weak close): the close must sit in the lowest slamCloseInRange of the bar's range", () => {
    // the same open/close with the low at 1.00: the close is 42% up its range
    const wick = bar(27, 1.19, 1.192, 1.0, 1.08, 6000);
    expect(slams(run([...READY, wick]))).toEqual([]);
    expect(slams(run([...READY, wick], { slamCloseInRange: 0.45 })).length).toBe(1);
  });

  it('rule 6 (volume): slamVolX x the mean of the up-to-10 earlier bars of the day; skipped with fewer than 3', () => {
    const thin = { ...SLAM, volume: 2700 }; // 1.29x the 2100 mean
    expect(slams(run([...READY, thin]))).toEqual([]);
    expect(slams(run([...READY, { ...SLAM, volume: 2730 }])).length).toBe(1); // exactly 1.3x: inclusive
    expect(slams(run([...READY, thin], { slamVolX: 1.2 })).length).toBe(1);
  });

  it('the switch: slam false never emits one', () => {
    expect(slams(run([...READY, SLAM], { slam: false }))).toEqual([]);
  });

  it("a slam out of a LONG: after the same bar's EXIT (the position is the episode)", () => {
    const b28 = bar(28, 1.205, 1.215, 1.2, 1.21);
    const s29 = bar(29, 1.21, 1.212, 1.1, 1.11, 9000);
    expect(run([...READY, BREAK, b28]).state).toBe('long');
    const r = run([...READY, BREAK, b28, s29]);
    expect(r.events.slice(-2).map((e) => `${(e.ts - ANCHOR) / MIN} ${e.kind}`)).toEqual(['29 exit', '29 slam']);
    expect(r.state).toBe('idle');
  });

  it('a slam that first breaks a not-yet-READY flag follows its EARLY BREAK alone (no FLAG FAILED, no EXIT)', () => {
    // 15 quiet bars at 10, a thrust to 11.1 / 11.0, two flag bars: FLAT (trigger 11.1333) but not READY (the close is
    // more than readyDistPct over the EMA). Bar 18 pokes 11.2 (EARLY BREAK: the flag closes) and closes 9.8, far under
    // the EMA: the fail gate finds no flag, there is no position, and the slam is the bar's only other event.
    const bars = Array.from({ length: 15 }, (_, i) => bar(i, 10, 10.02, 9.98, 10 + (i % 2 ? 0.01 : -0.01)));
    bars.push(bar(15, 10, 11.1, 9.99, 11, 3000), bar(16, 11, 11.05, 10.9, 10.95, 1200), bar(17, 10.95, 11.05, 10.9, 11, 1100));
    expect(run(bars).state).toBe('flat');
    const r = run([...bars, bar(18, 11, 11.2, 9.78, 9.8, 6000)]);
    expect(r.events.map((e) => `${(e.ts - ANCHOR) / MIN} ${e.kind}`)).toEqual(['15 impulse', '17 flat', '18 earlyBreak', '18 slam']);
    expect(r.boxes.map((b) => b.outcome)).toEqual(['early']);
  });

  it('…but while LONG, that EARLY BREAK is followed by the EXIT too: EARLY BREAK, EXIT, SLAM on one bar', () => {
    // LONG on 27 (F3), a second thrust to 1.374 (28..30) and a tight ADD flag far above the EMA (31..33): FLAT
    // (trigger 1.378122) but not READY, so its break is an EARLY BREAK, not an ADD. Bar 34 pokes the trigger and
    // closes 8.8% under the EMA: the EARLY BREAK is emitted with the position open, the stop-by-close exits it on
    // the same bar, and the slam comes last.
    const thrust2 = [1.25, 1.31, 1.37].map((c, k) => bar(28 + k, c - 0.04, c + 0.004, c - 0.045, c, 6000));
    const flag2 = [1.365, 1.368, 1.366].map((c, k) => bar(31 + k, c - 0.001, c + 0.003, c - 0.003, c, 2500));
    const pre = [...READY, BREAK, ...thrust2, ...flag2];
    const r33 = run(pre);
    expect(r33.state).toBe('long');
    expect(r33.events.slice(-2).map((e) => `${(e.ts - ANCHOR) / MIN} ${e.kind}`)).toEqual(['27 entry', '32 flat']);
    expect(r33.levels.trigger!).toBeCloseTo(1.378122, 9);
    const r = run([...pre, bar(34, 1.366, r33.levels.trigger! + 0.01, 1.15, 1.16, 20000)]);
    expect(r.events.filter((e) => e.ts === tsAt(34)).map((e) => e.kind)).toEqual(['earlyBreak', 'exit', 'slam']);
    expect(r.state).toBe('idle');
  });

  it('never on a forming bar: the same bar forming says nothing, closed it slams (append-only)', () => {
    const forming = run([...READY, SLAM], undefined, tsAt(27) + 30_000);
    expect(slams(forming)).toEqual([]);
    expect(forming.evidence.lastSlamTs).toBeNull();
    const closed = run([...READY, SLAM]);
    expect(closed.events.slice(0, forming.events.length).map(keyOf)).toEqual(forming.events.map(keyOf));
    expect(slams(closed).length).toBe(1);
    expectAppendOnlyReplay([...READY, SLAM, bar(28, 1.08, 1.09, 1.06, 1.07)], etDayKey(tsAt(0)), MIN);
  });

  it('at most one per episode: a flag that survives its slam (loose fail / span gates) does not slam again', () => {
    const loose: Partial<FlagParams> = { emaBreakPct: 20, maxFlatOfMovePct: 100, maxFlatPct: 100 };
    const b28 = bar(28, 1.08, 1.19, 1.08, 1.19, 2000);
    const b29 = bar(29, 1.19, 1.192, 1.07, 1.08, 9000); // a second slam candidate out of the same flag
    const r = run([...READY, SLAM, b28, b29], loose);
    expect(r.state).toBe('ready'); // the flag is still open: a SLAM changes no state
    expect(slams(r).map((e) => (e.ts - ANCHOR) / MIN)).toEqual([27]);
    // without the first slam (bar 27 on thin volume) the same bar 29 IS the episode's slam
    const r2 = run([...READY, { ...SLAM, volume: 1000 }, b28, b29], loose);
    expect(slams(r2).map((e) => (e.ts - ANCHOR) / MIN)).toEqual([29]);
    expect(r2.evidence.lastSlamTs).toBe(tsAt(29));
  });

  it('a later flag is its own episode: a position that survived its slam does not swallow the new flag\'s slam', () => {
    // trailEma off + an 8% stop (both editable in the Signal panel): the slam of bar 29 leaves the LONG of bar 27 open
    const wide: Partial<FlagParams> = { trailEma: false, stopBufferPct: 8 };
    const b28 = bar(28, 1.205, 1.215, 1.2, 1.21);
    const s29 = (v: number) => bar(29, 1.21, 1.212, 1.1, 1.11, v);
    // the stock recovers to a new top (1.315) and goes sideways again: a NEW flag next to the old position
    const rally = [1.13, 1.17, 1.22, 1.27, 1.31].map((c, k) => bar(30 + k, c - 0.03, c + 0.005, c - 0.035, c, 6000));
    const flag2 = [1.3, 1.305, 1.3, 1.302].map((c, k) => bar(35 + k, c - 0.002, c + 0.004, c - 0.004, c, 2500));
    const s39 = bar(39, 1.3, 1.302, 1.17, 1.18, 12000); // from above the EMA to 5.5% under it, out of the new flag
    const pre = [...READY, BREAK, b28, s29(9000), ...rally, ...flag2];
    const at = (r: FlagResult, i: number) => r.events.filter((e) => e.ts === tsAt(i)).map((e) => e.kind);
    const r38 = run(pre, wide);
    expect(at(r38, 29)).toEqual(['slam']); // the first slam, no EXIT: the position stays open (and slammed)
    expect(r38.state).toBe('long');
    expect(r38.levels.flagHigh).toBe(1.315); // ... with the new flag open under the new top
    const r = run([...pre, s39], wide);
    expect(at(r, 39)).toEqual(['failed', 'slam']);
    expect(slams(r).map((e) => (e.ts - ANCHOR) / MIN)).toEqual([29, 39]);
    expect(r.evidence.lastSlamTs).toBe(tsAt(39));
    // the same bar 39 without the first slam (bar 29 on thin volume) slams too: bar 29 changes nothing about bar 39
    const thin = [...READY, BREAK, b28, s29(1000), ...rally, ...flag2, s39];
    expect(slams(run(thin, wide)).map((e) => (e.ts - ANCHOR) / MIN)).toEqual([39]);
    // and the defaults (the trail closes the position on its slam) agree: 29 and 39
    expect(slams(run([...pre, s39])).map((e) => (e.ts - ANCHOR) / MIN)).toEqual([29, 39]);
  });

  it('one slam marks every episode it went through: a position and a flag open together slam once, then never again', () => {
    // the same run: bar 39 slammed out of the old position AND the new flag; the flag failed, the position (stop
    // 1.08) is still open. A recovery bar and a second slam-shaped bar: the position already slammed, no flag is open.
    const wide: Partial<FlagParams> = { trailEma: false, stopBufferPct: 8 };
    const rally = [1.13, 1.17, 1.22, 1.27, 1.31].map((c, k) => bar(30 + k, c - 0.03, c + 0.005, c - 0.035, c, 6000));
    const flag2 = [1.3, 1.305, 1.3, 1.302].map((c, k) => bar(35 + k, c - 0.002, c + 0.004, c - 0.004, c, 2500));
    const bars = [...READY, BREAK, bar(28, 1.205, 1.215, 1.2, 1.21), bar(29, 1.21, 1.212, 1.1, 1.11, 1000), ...rally, ...flag2, bar(39, 1.3, 1.302, 1.17, 1.18, 12000)];
    const r39 = run(bars, wide);
    expect(r39.state).toBe('long');
    expect(slams(r39).map((e) => (e.ts - ANCHOR) / MIN)).toEqual([39]);
    const again = [...bars, bar(40, 1.18, 1.3, 1.18, 1.29, 3000), bar(41, 1.29, 1.292, 1.16, 1.17, 20000)];
    const r = run(again, wide);
    expect(r.state).toBe('long');
    expect(slams(r).map((e) => (e.ts - ANCHOR) / MIN)).toEqual([39]);
    // the guard is what holds bar 41 back: with bar 39 on thin volume (no slam there) bar 41 IS the position's slam
    again[39] = { ...again[39]!, volume: 1000 };
    expect(slams(run(again, wide)).map((e) => (e.ts - ANCHOR) / MIN)).toEqual([41]);
  });

  it("the day's latest slam only: a slam on the previous ET day is in the events, not in the evidence", () => {
    // WETO 2026-09-24: a post-market slam on 09-23 17:30 ET, none on the 24th
    const r = run((wetoFixture as Fixture).bars.map(toCandle));
    expect(slams(r).map((e) => `${etDayKey(e.ts)} ${fmtEt(e.ts)}`)).toEqual(['2026-09-23 17:30']);
    expect(r.dayKey).toBe('2026-09-24');
    expect(r.evidence.lastSlamTs).toBeNull();
    expect(r.evidence.lastSlamPct).toBeNull();
  });

  describe("real SPAI 2026-10-07 (the trader's chart)", () => {
    const candles = (spaiFixture as Fixture).bars.map(toCandle);
    const e = emaSeries(closesOf(candles), 9) as number[];
    const at = (hhmm: string): number => candles.findIndex((c) => etDayKey(c.ts) === '2026-10-07' && fmtEt(c.ts) === hhmm);
    const i15 = at('08:15');
    const i16 = at('08:16');

    it('fires exactly once, on the 08:15 bar, at its close 4.566, after the FLAG FAILED of the same bar', () => {
      const r = run(candles);
      expect(todayEvents(r).map(line)).toEqual([
        '08:07 impulse BREAKOUT 4.97',
        '08:11 flat FLAT 5.07',
        '08:12 earlyBreak EARLY BREAK 5.07',
        '08:14 flat FLAT 5.12',
        '08:15 failed FLAG FAILED 4.57',
        '08:15 slam SLAM (9 EMA) 4.57',
      ]);
      const s = slams(r);
      expect(s.length).toBe(1);
      expect(s[0]!.ts).toBe(candles[i15]!.ts);
      expect(s[0]!.price).toBe(4.566);
      expect(s[0]!.side).toBe('short');
      // the documented numbers: EMA@08:15 4.7724, 4.3% under it; 1.7 ATR body; 1.6x the mean volume of 08:05..08:14
      expect(e[i15]!).toBeCloseTo(4.7724, 4);
      expect(s[0]!.emaDistPct!).toBeCloseTo(-4.3252, 4);
      expect(s[0]!.note).toBe('08:15 slammed through the 9 EMA from above: closed 4.57, 4.3% under the EMA 4.77 — a 1.7 ATR body on 1.6× volume');
      expect(r.evidence.lastSlamTs).toBe(candles[i15]!.ts);
      expect(r.evidence.lastSlamClose).toBe(4.566);
      expect(r.evidence.lastSlamPct).toBe(s[0]!.emaDistPct);
    });

    it('08:16 kept falling but is not a slam: it OPENED under the EMA', () => {
      expect(candles[i16]!.open).toBeLessThan(e[i15]! * 0.995);
      expect(candles[i16]!.close).toBeLessThan(e[i16]! * 0.97); // it did close far under the EMA
      // the brief's starting slamBelowPct 2 and the calibrated 3 agree: 08:15 only
      for (const slamBelowPct of [2, 3]) expect(slams(run(candles, { slamBelowPct })).map((x) => fmtEt(x.ts))).toEqual(['08:15']);
    });

    it('not on the forming 08:15 bar; closed, it slams; append-only over the whole day', () => {
      const upTo15 = candles.slice(0, i15 + 1);
      expect(slams(run(upTo15, undefined, candles[i15]!.ts + 30_000))).toEqual([]);
      expect(slams(run(upTo15)).length).toBe(1);
      const final = expectAppendOnlyReplay(candles, '2026-10-07', MIN);
      expect(final.events.map(keyOf)).toEqual(run(candles).events.map(keyOf));
    });
  });

  it('slam false reproduces the engine without the rule on every fixture: the same run minus its slam events', () => {
    const all = [...FIXTURES_1005, ...FIXTURES_0924, ptcFixture, mi1325Fixture, spaiFixture] as Fixture[];
    const blank = (r: FlagResult) => ({ ...r, events: [], evidence: { ...r.evidence, lastSlamTs: null, lastSlamClose: null, lastSlamPct: null } });
    let fired = 0;
    for (const fx of all) {
      const cs = fx.bars.map(toCandle);
      for (const nowMs of [cs[cs.length - 1]!.ts + 10 * MIN, cs[cs.length - 1]!.ts + 30_000]) {
        const on = evaluateFlag({ candles: cs, tfMs: MIN, nowMs });
        const off = evaluateFlag({ candles: cs, tfMs: MIN, nowMs, params: { slam: false } });
        fired += slams(on).length;
        expect(off.events.map(keyOf), fx.sym).toEqual(on.events.filter((x) => x.kind !== 'slam').map(keyOf));
        expect(blank(off), fx.sym).toEqual(blank(on));
        expect(off.evidence.lastSlamTs).toBeNull();
      }
    }
    expect(fired).toBeGreaterThanOrEqual(8); // SPAI, MI 13:25, QTEX, WETO — on both clocks
  });
});

// ─── HOD FLAG ────────────────────────────────────────────────────────────
// evidence.lastHodFlagTs: the FLAT bar of a flag whose run-up top is a new high of the ET day. Go twin: hodflag_test.go.
describe('HOD FLAG', () => {
  const hodRun = (n: number, spike: number, more: number): Candle[] => {
    const out: Candle[] = [];
    for (let i = 0; i < n; i++) out.push(bar(i, 1.0, i === 0 ? spike : 1.002, 0.998, 1.0, 1000));
    out.push(bar(n, 1.0, 1.2, 0.999, 1.18, 9000));
    const closes = [1.18, 1.184, 1.188, 1.19, 1.192, 1.192];
    for (let k = 0; k < closes.length + more; k++) {
      const c = closes[Math.min(k, closes.length - 1)]!;
      out.push(bar(n + 1 + k, c - 0.001, c + 0.004, c - 0.004, c, 1500));
    }
    return out;
  };
  const flatTs = (r: FlagResult): number | undefined => r.events.find((e) => e.kind === 'flat')?.ts;

  it('a leg that makes a new high of the day: set on the FLAT bar', () => {
    const r = run(hodRun(40, 1.002, 0));
    expect(flatTs(r)).toBeDefined();
    expect(r.evidence.lastHodFlagTs).toBe(flatTs(r));
  });

  it('a leg under an earlier high of the day: no HOD flag', () => {
    const r = run(hodRun(40, 1.5, 0));
    expect(flatTs(r)).toBeDefined();
    expect(r.evidence.lastHodFlagTs).toBeNull();
  });

  it('once per flag: later bars of the same flag do not move it', () => {
    const first = run(hodRun(40, 1.002, 0)).evidence.lastHodFlagTs;
    expect(first).not.toBeNull();
    for (const more of [1, 3, 8]) expect(run(hodRun(40, 1.002, more)).evidence.lastHodFlagTs).toBe(first);
  });
});
