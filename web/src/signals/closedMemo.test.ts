// closedBarMemo (closedMemo.ts) must be invisible: for evaluateBbs and
// evaluateIgnition, which read closed candles only, the memoized answer is
// deepEqual to a fresh evaluation after EVERY update of a simulated live feed
// (forming-bar ticks, bar closes), and any change to a closed candle, the
// symbol, the timeframe or the params is a miss.
import { describe, expect, it } from 'vitest';
import { closedBarMemo, closedCount, type ClosedBarInput } from './closedMemo';
import { evaluateBbs, type BbsInput } from './bouncyShort';
import { evaluateIgnition } from './ignition';
import { aaoiBars, randomBars } from './bouncyShort.fixtures';
import { ncplBars, randomIgnitionBars } from './ignition.fixtures';
import type { Candle } from '../vendor/blackthorn-charts';

const MIN = 60_000;

/** The forming candle `bar` is `f` of the way through its minute (f = 1: its final values). */
function partial(bar: Candle, f: number): Candle {
  const close = bar.open + (bar.close - bar.open) * f;
  return {
    ts: bar.ts,
    open: bar.open,
    high: Math.max(bar.open, close, bar.open + (bar.high - bar.open) * f),
    low: Math.min(bar.open, close, bar.open + (bar.low - bar.open) * f),
    close,
    volume: Math.floor(bar.volume * f),
  };
}

// Four updates per minute of bar i: three forming ticks, then the close (now = ts + 60 s).
const UPDATES: [number, number][] = [
  [0.25, 10_000],
  [0.5, 30_000],
  [0.75, 50_000],
  [1, MIN],
];

type In = ClosedBarInput<undefined>;
const input = (candles: Candle[], nowMs: number): In => ({ candles, tfMs: 60_000, nowMs });

/**
 * Replays `bars` as a live feed from bar `from` on, as aggregate() would hand it
 * to the cell (fresh candle objects every update). After every update the memo
 * must equal a fresh evaluation. Returns how often the memo had to evaluate.
 */
function replay<R>(evaluate: (input: In) => R, bars: Candle[], from: number): { evals: number; calls: number; last: R } {
  let evals = 0;
  const memo = closedBarMemo((i: In) => {
    evals++;
    return evaluate(i);
  });
  let calls = 0;
  let last: R | undefined;
  let prev: R | undefined;
  for (let i = from; i < bars.length; i++) {
    const closed = bars.slice(0, i).map((c) => ({ ...c }));
    for (const [f, dt] of UPDATES) {
      const candles = [...closed, partial(bars[i]!, f)];
      const got = memo(input(candles, bars[i]!.ts + dt));
      calls++;
      expect(got).toEqual(evaluate(input(candles.map((c) => ({ ...c })), bars[i]!.ts + dt)));
      // a forming tick (closed set unchanged since the previous call) hands back the SAME result object
      if (f !== 1 && prev !== undefined) expect(got).toBe(prev);
      prev = got;
      last = got;
    }
  }
  return { evals, calls, last: last as R };
}

const bbsInput = (candles: Candle[], nowMs: number): BbsInput => ({ candles, tfMs: 60_000, nowMs });

describe('closedCount', () => {
  it('is the evaluators\' last + 1: the newest candle counts once nowMs reaches its close', () => {
    const cs = randomBars(1, 5);
    const t = cs[4]!.ts;
    expect(closedCount(cs, MIN, t)).toBe(4);
    expect(closedCount(cs, MIN, t + MIN - 1)).toBe(4);
    expect(closedCount(cs, MIN, t + MIN)).toBe(5);
    expect(closedCount(cs, MIN, t + 10 * MIN)).toBe(5);
    expect(closedCount([], MIN, t)).toBe(0);
    expect(closedCount(cs.slice(0, 1), MIN, cs[0]!.ts + 5)).toBe(0);
  });
});

describe('memoized bbs / ignition == fresh over a live feed', () => {
  const feeds: [string, Candle[], number][] = [
    ['aaoi (bouncy-ball break at 10:40)', aaoiBars(), 30],
    ['ncpl (ignition at 09:06)', ncplBars(), 12],
    ['random walk (bbs)', randomBars(4, 160), 20],
    ['random spikes (ignition)', randomIgnitionBars(9, 160), 20],
  ];
  let bbsEvents = 0;
  let ignEvents = 0;

  for (const [name, bars, from] of feeds) {
    it(`${name}: deepEqual after every update, one evaluation per closed bar`, () => {
      const minutes = bars.length - from;
      const b = replay(evaluateBbs, bars, from);
      const g = replay(evaluateIgnition, bars, from);
      // 4 updates a minute; only the first forming tick and each close change the closed set
      expect(b.calls).toBe(4 * minutes);
      expect(b.evals).toBe(minutes + 1);
      expect(g.evals).toBe(minutes + 1);
      bbsEvents += b.last.events.length;
      ignEvents += g.last.length;
    }, 60_000);
  }

  it('the feeds were not vacuous', () => {
    expect(bbsEvents).toBeGreaterThan(0);
    expect(ignEvents).toBeGreaterThan(0);
  });
});

describe('anything that changes what the evaluators can see is a miss', () => {
  const bars = aaoiBars();
  const now = bars[bars.length - 1]!.ts + 10 * MIN;
  const copy = (): Candle[] => bars.map((c) => ({ ...c }));

  function counted() {
    let evals = 0;
    const memo = closedBarMemo((i: BbsInput) => {
      evals++;
      return evaluateBbs(i);
    });
    return { memo, evals: () => evals };
  }

  it('same values in new candle objects hit', () => {
    const { memo, evals } = counted();
    const a = memo(bbsInput(copy(), now));
    expect(memo(bbsInput(copy(), now))).toBe(a);
    expect(evals()).toBe(1);
  });

  it('a rewritten closed candle misses and the new answer is the fresh one (here: the break is erased)', () => {
    const { memo, evals } = counted();
    const a = memo(bbsInput(copy(), now));
    expect(a.events.length).toBe(1);
    const rewritten = copy();
    const brk = rewritten.findIndex((c) => c.ts === a.events[0]!.ts);
    rewritten[brk] = { ...rewritten[brk]!, close: rewritten[brk]!.close + 3 }; // no longer closes under the support
    const b = memo(bbsInput(rewritten, now));
    expect(evals()).toBe(2);
    expect(b).toEqual(evaluateBbs(bbsInput(rewritten, now)));
    expect(b.events.length).toBe(0);
  });

  it('each of the six fields of a closed candle matters, the first and the last included', () => {
    const { memo, evals } = counted();
    memo(bbsInput(copy(), now));
    let expected = 1;
    for (const idx of [0, 7, bars.length - 2]) {
      for (const field of ['ts', 'open', 'high', 'low', 'close', 'volume'] as const) {
        const cs = copy();
        cs[idx] = { ...cs[idx]!, [field]: cs[idx]![field] + 0.01 };
        const got = memo(bbsInput(cs, now));
        expect(got).toEqual(evaluateBbs(bbsInput(cs, now)));
        expected++;
        expect(evals()).toBe(expected);
        memo(bbsInput(copy(), now)); // back to the original: a miss again (one entry only)
        expected++;
      }
    }
    expect(evals()).toBe(expected);
  });

  it('another symbol with the same bar count and last timestamp misses', () => {
    const { memo, evals } = counted();
    memo(bbsInput(copy(), now));
    const other = copy().map((c) => ({ ...c, open: c.open * 1.5, high: c.high * 1.5, low: c.low * 1.5, close: c.close * 1.5 }));
    expect(memo(bbsInput(other, now))).toEqual(evaluateBbs(bbsInput(other, now)));
    expect(evals()).toBe(2);
  });

  it('a different timeframe or params object misses', () => {
    const { memo, evals } = counted();
    memo(bbsInput(copy(), now));
    expect(memo({ candles: copy(), tfMs: 300_000, nowMs: now })).toEqual(evaluateBbs({ candles: copy(), tfMs: 300_000, nowMs: now }));
    expect(evals()).toBe(2);
    const params = { breakPct: 5 };
    expect(memo({ ...bbsInput(copy(), now), params })).toEqual(evaluateBbs({ ...bbsInput(copy(), now), params }));
    expect(evals()).toBe(3);
    memo({ ...bbsInput(copy(), now), params }); // same params object: hit
    expect(evals()).toBe(3);
    memo({ ...bbsInput(copy(), now), params: { ...params } }); // equal but new object: miss (safe side)
    expect(evals()).toBe(4);
  });

  it('empty input, a single forming candle, and a throwing evaluation behave like the bare evaluator', () => {
    const { memo, evals } = counted();
    expect(memo(bbsInput([], now))).toEqual(evaluateBbs(bbsInput([], now)));
    const one = [bars[0]!];
    expect(memo(bbsInput(one, one[0]!.ts + 5))).toEqual(evaluateBbs(bbsInput(one, one[0]!.ts + 5)));
    const good = memo(bbsInput(copy(), now));
    const bad = copy();
    bad[3] = { ...bad[3]!, ts: Number.NaN }; // etDayKey throws a RangeError
    expect(() => evaluateBbs(bbsInput(bad, now))).toThrow();
    expect(() => memo(bbsInput(bad, now))).toThrow();
    expect(memo(bbsInput(copy(), now))).toBe(good); // the failed call did not disturb the cached entry
    expect(evals()).toBe(4);
  });
});
