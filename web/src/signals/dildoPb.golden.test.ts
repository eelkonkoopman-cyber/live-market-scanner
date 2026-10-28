// GOLDEN VECTORS for the GREEN DILDO -> 9 EMA PULLBACK rule: the contract between
// dildoPb.ts (the rule, TypeScript, what the Signal window draws) and
// internal/dildopb (the Go port the market scan runs: the engine's alert-1
// judge Classify and flagscan's alert-2 Evaluate).
//
// Both implementations are pinned to ONE committed file,
// src/signals/fixtures/golden/dildopb-golden.json:
//   - THIS test runs evaluateDildo / classifyDildo over the inputs below and
//     fails if the output differs from the committed file (a rule change in
//     dildoPb.ts that was not followed by a regenerate);
//   - internal/dildopb/golden_test.go replays the SAME inputs through the Go
//     Evaluate / Classify and fails on any difference: events (ts, kind, label,
//     note, reason; price and emaDistPct as exact float64 bits through JSON)
//     and every Result field.
//
// REGENERATE after an intentional change to dildoPb.ts or the fixtures, then
// port the change to Go until `go test ./internal/dildopb/` is green again:
//
//   cd web && UPDATE_GOLDEN=1 npx vitest run src/signals/dildoPb.golden.test.ts
//   (PowerShell:  $env:UPDATE_GOLDEN='1'; npx vitest run src/signals/dildoPb.golden.test.ts)
//
// then run it again WITHOUT the variable (the guard) and commit the JSON with
// the change that caused it. Never hand-edit it.
//
// Inputs: the five real fixtures in fixtures/dildo/ (NOT fixtures/*.json: the
// emaflag golden tests glob that folder and fail on any file missing from
// their own golden) plus ~40 SYNTHETIC series written INTO the golden file
// (`series`), so Go needs no generator: seeded-PRNG random walks with injected
// spikes and follow-ups (hold / dump / run / second spike), thin and gapped
// tapes, priors with a mean body or volume of 0, cross-day priors (the 04:00
// case), a 19:55 dildo whose window crosses 20:00, and EXACT-TIE series built
// from the rule's own numbers (a touch bar whose low IS the touch level, or
// one ulp over it; a low ON the floor; a close ON the hold line, or one ulp
// under it) so a strict-vs-inclusive slip in either language changes the answer.
// Each runs under five parameter sets x two clocks ('closed': last.t + 10 min;
// 'forming': last.t + 30 s). REPLAYS cut each run at, and 1-2 bars after, every
// event's bar (the moments a pullback is pending, touching, or just decided) and
// record the Result. `classify` holds direct classifyDildo vectors, including
// inputs searched to the last ulp of every default threshold.
//
// Non-finite numbers are written as the strings "Infinity" / "-Infinity" /
// "NaN" (JSON has no such numbers, and null means "none" in a Result).
//
// Node-only: reads/writes with node:fs, loaded dynamically with a variable
// specifier so tsc needs no @types/node.
import { describe, expect, it } from 'vitest';
import { DEFAULT_DILDO_PARAMS, classifyDildo, evaluateDildo, type DildoHit, type DildoParams, type DildoResult } from './dildoPb';
import { emaSeries } from './emaFlag';
import { toCandle } from '../chartAgg';
import type { Bar } from '../types';
import type { Candle } from '../vendor/blackthorn-charts';

declare const process: { env: Record<string, string | undefined> };

interface NodeFs {
  readFileSync(path: URL, encoding: 'utf8'): string;
  writeFileSync(path: URL, data: string): void;
  readdirSync(path: URL): string[];
  existsSync(path: URL): boolean;
}
const fsSpec = 'node:fs';
const fs = (await import(/* @vite-ignore */ fsSpec)) as NodeFs;

const FIXTURES_DIR = new URL('./fixtures/dildo/', import.meta.url);
const GOLDEN_FILE = new URL('./fixtures/golden/dildopb-golden.json', import.meta.url);
const GOLDEN_REL = 'web/src/signals/fixtures/golden/dildopb-golden.json';
const TF_MS = 60_000;
const MIN = 60_000;

// ─── numbers ─────────────────────────────────────────────────────────────

function stepUlp(x: number, by: bigint): number {
  const f = new Float64Array([x]);
  const u = new BigInt64Array(f.buffer);
  u[0] = u[0]! + by;
  return f[0]!;
}
const nextUp = (x: number) => (x === 0 ? Number.MIN_VALUE : stepUlp(x, x > 0 ? 1n : -1n));
const nextDown = (x: number) => (x === 0 ? -Number.MIN_VALUE : stepUlp(x, x > 0 ? -1n : 1n));

/** The smallest x (in ulp steps from x0, which must be near the boundary) for which the increasing `ok` holds. */
function lowestPassing(ok: (x: number) => boolean, x0: number): number {
  let x = x0;
  let i = 0;
  const guard = () => {
    if (++i > 100_000) throw new Error(`lowestPassing: no boundary within 100 000 ulps of ${x0}`);
  };
  if (ok(x)) {
    while (ok(nextDown(x))) (x = nextDown(x)), guard();
    return x;
  }
  while (!ok(x)) (x = nextUp(x)), guard();
  return x;
}

/** The largest x (in ulp steps from x0, near the boundary) for which the decreasing `ok` holds. */
function highestPassing(ok: (x: number) => boolean, x0: number): number {
  return -lowestPassing((y) => ok(-y), -x0);
}

// Seeded PRNG (mulberry32): the synthetic series are part of the golden file, but a regenerate must
// still reproduce them byte for byte, so nothing here may read Math.random.
function mulberry32(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const tick = (px: number) => (px >= 1 ? 0.01 : 0.0001);
const roundPx = (px: number) => Math.max(tick(px), Math.round(px / tick(px)) * tick(px));

// ─── synthetic series ────────────────────────────────────────────────────

const ET_OFF = 4 * 3_600_000; // the series sit in EDT (2026-10-05/06/07)
const etMs = (y: number, mo: number, d: number, hh: number, mm: number) => Date.UTC(y, mo - 1, d, hh, mm) + ET_OFF;

interface GBar {
  t: number;
  o: number;
  h: number;
  l: number;
  c: number;
  v: number;
}
const toG = (c: Candle): GBar => ({ t: c.ts, o: c.open, h: c.high, l: c.low, c: c.close, v: c.volume });

type Regime = 'hold' | 'dump' | 'run' | 'second' | 'drift';

function randomSeries(seed: number): GBar[] {
  const rnd = mulberry32(seed);
  const scale = [0.32, 0.85, 2.4, 7.5, 31][seed % 5]!;
  const twoDays = seed % 3 !== 0;
  const thin = seed % 4 === 1;
  const out: GBar[] = [];
  let px = scale;
  const baseVol = Math.round((150_000 / scale) * (0.4 + rnd()));
  const push = (t: number, o: number, h: number, l: number, c: number, v: number) => {
    o = roundPx(o);
    c = roundPx(c);
    h = roundPx(Math.max(h, o, c));
    l = roundPx(Math.min(l, o, c));
    out.push({ t, o, h, l, c, v: Math.max(0, Math.round(v)) });
    px = c;
  };
  const day = (start: number, minutes: number) => {
    let regime: Regime = 'drift';
    let left = 0;
    for (let m = 0; m < minutes; m++) {
      const t = start + m * MIN;
      if (thin && rnd() < 0.45) continue; // a thin tape: many empty minutes
      if (left <= 0) regime = 'drift';
      left--;
      const o = px * (1 + (rnd() - 0.5) * 0.004);
      if (regime === 'drift' && rnd() < 0.045) {
        // a spike candle (usually a dildo), then a follow-up regime
        const body = 0.05 + rnd() * 0.3;
        const c = o * (1 + body);
        push(t, o, c * (1 + rnd() * 0.04), o * (1 - rnd() * 0.01), c, baseVol * (2 + rnd() * 30));
        const r = rnd();
        regime = r < 0.4 ? 'hold' : r < 0.6 ? 'dump' : r < 0.75 ? 'run' : r < 0.9 ? 'second' : 'drift';
        left = 5 + Math.floor(rnd() * 22);
        continue;
      }
      let drift = (rnd() - 0.5) * 0.012;
      if (regime === 'hold') drift = (rnd() - 0.6) * 0.012; // sideways, slightly down: the EMA catches up
      if (regime === 'dump') drift = -0.01 - rnd() * 0.04;
      if (regime === 'run') drift = 0.004 + rnd() * 0.02;
      if (regime === 'second' && rnd() < 0.25) {
        const c = o * (1 + 0.08 + rnd() * 0.25);
        push(t, o, c * (1 + rnd() * 0.02), o * (1 - rnd() * 0.005), c, baseVol * (5 + rnd() * 40));
        regime = 'hold';
        continue;
      }
      const c = o * (1 + drift);
      const wig = Math.abs(drift) + 0.002 + rnd() * 0.01;
      push(t, o, Math.max(o, c) * (1 + rnd() * wig), Math.min(o, c) * (1 - rnd() * wig), c, baseVol * (0.2 + rnd() * 1.6) * (regime === 'drift' ? 1 : 1.8));
    }
  };
  if (twoDays) day(etMs(2026, 10, 6, 16, 0), 60 + Math.floor(rnd() * 60)); // yesterday's post-market
  const startHm = [
    [4, 0],
    [9, 30],
    [11, 15],
    [19, 20],
  ][seed % 4]!;
  day(etMs(2026, 10, 7, startHm[0]!, startHm[1]!), 70 + Math.floor(rnd() * 70));
  return out;
}

// The unit test's base: 12 quiet bars at 1.00, a dildo 1.00 -> 1.20 at bar 12 (floor 1.10), three bars holding ~1.20.
function tieBase(t0: number): Candle[] {
  const b = (i: number, o: number, h: number, l: number, c: number, v: number): Candle => ({ ts: t0 + i * MIN, open: o, high: h, low: l, close: c, volume: v });
  const cs: Candle[] = [];
  for (let i = 0; i < 12; i++) cs.push(b(i, 1, 1.005, 0.995, 1, 1000));
  cs.push(b(12, 1, 1.205, 0.995, 1.2, 100_000));
  cs.push(b(13, 1.2, 1.22, 1.19, 1.21, 5000));
  cs.push(b(14, 1.21, 1.22, 1.19, 1.2, 5000));
  cs.push(b(15, 1.2, 1.22, 1.19, 1.2, 5000));
  return cs;
}

// EXACT-TIE series: the touch bar (bar 16) put ON a rule boundary with the rule's own arithmetic.
function tieSeries(): Record<string, GBar[]> {
  const p = DEFAULT_DILDO_PARAMS;
  const t0 = etMs(2026, 10, 7, 9, 40);
  const base = tieBase(t0);
  const ema = emaSeries(base.map((c) => c.close), p.emaPeriod);
  const k = 2 / (p.emaPeriod + 1);
  const e15 = ema[15]!;
  const level = e15 * (1 + p.touchPct / 100);
  const floor = (base[12]!.open + base[12]!.close) / 2;
  const holdK = 1 - p.holdPct / 100;
  const ts16 = t0 + 16 * MIN;
  const touch = (low: number, close: number): Candle => ({ ts: ts16, open: 1.2, high: 1.2, low, close, volume: 8000 });
  // the close that sits exactly on the hold line: c >= (c·k + e15·(1 − k))·holdK, searched to the ulp
  const holds = (c: number) => !(c < (c * k + e15 * (1 - k)) * holdK);
  const cHold = lowestPassing(holds, (e15 * (1 - k) * holdK) / (1 - k * holdK));
  const lowHold = floor + (cHold - floor) / 2;
  const after = (c: number): Candle => ({ ts: ts16 + MIN, open: c, high: c + 0.01, low: c - 0.005, close: c + 0.005, volume: 4000 });
  const mk = (bar16: Candle) => [...base, bar16, after(bar16.close)].map(toG);
  return {
    'tie-touch-equal': mk(touch(level, 1.18)), // low == level: a touch (inclusive) -> 9 EMA PULLBACK
    'tie-touch-ulp': mk(touch(nextUp(level), 1.18)), // one ulp over: no touch
    'tie-floor-equal': mk(touch(floor, 1.18)), // low == floor: cancelled 'floor' (inclusive)
    'tie-floor-ulp': mk(touch(nextUp(floor), 1.18)), // one ulp over the floor: a valid touch
    'tie-hold-equal': mk(touch(lowHold, cHold)), // close exactly on the hold line: holds
    'tie-hold-ulp': mk(touch(lowHold, nextDown(cHold))), // one ulp under: cancelled 'ema'
  };
}

// Hand-built shapes the PRNG would rarely hit.
function shapeSeries(): Record<string, GBar[]> {
  const out: Record<string, GBar[]> = {};
  const mk = (t0: number, rows: [number, number, number, number, number][]): GBar[] => rows.map((r, i) => ({ t: t0 + i * MIN, o: r[0], h: r[1], l: r[2], c: r[3], v: r[4] }));
  // cross-day priors: yesterday's 19:50-19:59, then a 04:00 dildo and a hold + touch
  const y: [number, number, number, number, number][] = Array.from({ length: 10 }, () => [2, 2.01, 1.99, 2, 3000]);
  const today: [number, number, number, number, number][] = [
    [2, 2.6, 1.99, 2.58, 90_000],
    [2.58, 2.62, 2.5, 2.55, 20_000],
    [2.55, 2.6, 2.5, 2.56, 15_000],
    [2.56, 2.58, 2.45, 2.5, 12_000],
    [2.5, 2.52, 2.3, 2.48, 15_000],
    [2.48, 2.5, 2.42, 2.49, 8000],
  ];
  out['shape-cross-day-priors'] = [...mk(etMs(2026, 10, 6, 19, 50), y), ...mk(etMs(2026, 10, 7, 4, 0), today)];
  // a 19:55 dildo whose 20-minute window crosses 20:00: the next bars are tomorrow's 04:00
  const eve: [number, number, number, number, number][] = [
    ...Array.from({ length: 12 }, (): [number, number, number, number, number] => [3, 3.01, 2.99, 3, 4000]),
    [3, 3.45, 2.99, 3.42, 60_000],
    [3.42, 3.46, 3.38, 3.4, 9000],
    [3.4, 3.44, 3.36, 3.41, 9000],
    [3.41, 3.43, 3.37, 3.4, 9000],
    [3.4, 3.42, 3.35, 3.38, 9000],
  ];
  out['shape-window-crosses-20'] = [...mk(etMs(2026, 10, 6, 19, 43), eve), ...mk(etMs(2026, 10, 7, 4, 0), [[3.38, 3.4, 3.2, 3.3, 5000], [3.3, 3.32, 3.25, 3.3, 3000]])];
  // priors with a mean body of 0 and a mean volume of 0 (both multiples infinite)
  out['shape-mean-zero'] = mk(etMs(2026, 10, 7, 7, 0), [
    [1.5, 1.5, 1.5, 1.5, 0],
    [1.5, 1.5, 1.5, 1.5, 0],
    [1.5, 1.5, 1.5, 1.5, 0],
    [1.5, 1.5, 1.5, 1.5, 0],
    [1.5, 1.72, 1.49, 1.7, 80_000],
    [1.7, 1.71, 1.6, 1.65, 3000],
  ]);
  // thin priors: exactly 2, then 3 bars before a spike
  out['shape-thin-priors'] = mk(etMs(2026, 10, 7, 4, 0), [
    [5, 5.02, 4.98, 5, 2000],
    [5, 5.02, 4.98, 5.01, 2000],
    [5.01, 5.9, 5, 5.85, 40_000], // 2 priors: not judged
    [5.85, 5.9, 5.8, 5.86, 3000],
    [5.86, 6.9, 5.85, 6.85, 90_000], // 4 priors: a dildo
    [6.85, 6.9, 6.5, 6.6, 9000],
  ]);
  return out;
}

// ─── the golden shapes ───────────────────────────────────────────────────

type Num = number | string | null; // a non-finite number is a string ("Infinity" / "NaN")
const enc = (v: number | null | undefined): Num => (v == null ? null : Number.isFinite(v) ? v : String(v));

interface GEvent {
  ts: number;
  kind: string;
  label: string;
  price: Num;
  note: string;
  reason: string | null;
  emaDistPct: Num;
}
interface GResult {
  lastDildoTs: Num;
  lastDildoBodyPct: Num;
  lastDildoBodyX: Num;
  lastDildoVolX: Num;
  lastDildoDvol: Num;
  pbState: number;
  touchTs: Num;
  pbTs: Num;
  pbPx: Num;
  floor: Num;
  deadlineTs: Num;
}
interface GCase {
  source: 'fixture' | 'series';
  name: string;
  paramSet: string;
  clock: 'closed' | 'forming';
  nowMs: number;
  bars: number;
  events: GEvent[];
  result: GResult;
}
interface GReplay {
  source: 'fixture' | 'series';
  name: string;
  paramSet: string;
  clock: 'closed' | 'forming';
  cut: number;
  nowMs: number;
  nEvents: number;
  lastEvent: GEvent | null;
  result: GResult;
}
interface GClassify {
  o: number;
  h: number;
  l: number;
  c: number;
  v: number;
  sumAbsBody: number;
  sumVol: number;
  n: number;
  paramSet: string;
  hit: Record<keyof DildoHit, Num> | null;
}
interface GoldenFile {
  note: string;
  tfMs: number;
  paramSets: Record<string, DildoParams>;
  series: Record<string, GBar[]>;
  cases: GCase[];
  replays: GReplay[];
  classify: GClassify[];
}

const PARAM_SETS: { name: string; override: Partial<DildoParams> }[] = [
  { name: 'defaults', override: {} },
  { name: 'open floor', override: { floor: 'open' } },
  { name: 'loose', override: { minBodyPct: 3, minBodyX: 2, maxTopPct: 30, minVolX: 2, minPrice: 0.2, minDollarVol: 25_000 } },
  { name: 'window 10, touch 0, hold 0', override: { pbWindowMin: 10, touchPct: 0, holdPct: 0 } },
  { name: 'priors 5, min 1, ema 5', override: { priorBars: 5, minPriorBars: 1, emaPeriod: 5, touchPct: 2, holdPct: 2 } },
];
const CLOCKS = [
  { name: 'closed' as const, offsetMs: 10 * MIN },
  { name: 'forming' as const, offsetMs: 30_000 },
];
const REPLAY_AFTER = [0, 1, 2];

const encEvent = (e: DildoResult['events'][number]): GEvent => ({ ts: e.ts, kind: e.kind, label: e.label, price: enc(e.price), note: e.note, reason: e.reason ?? null, emaDistPct: enc(e.emaDistPct ?? Number.NaN) });
const encResult = (r: DildoResult): GResult => ({
  lastDildoTs: enc(r.lastDildoTs),
  lastDildoBodyPct: enc(r.lastDildoBodyPct),
  lastDildoBodyX: enc(r.lastDildoBodyX),
  lastDildoVolX: enc(r.lastDildoVolX),
  lastDildoDvol: enc(r.lastDildoDvol),
  pbState: r.pbState,
  touchTs: enc(r.touchTs),
  pbTs: enc(r.pbTs),
  pbPx: enc(r.pbPx),
  floor: enc(r.floor),
  deadlineTs: enc(r.deadlineTs),
});

function fixtureNames(): string[] {
  return fs
    .readdirSync(FIXTURES_DIR)
    .filter((f) => f.endsWith('.json'))
    .map((f) => f.replace(/\.json$/, ''))
    .sort();
}
function fixtureCandles(name: string): Candle[] {
  return (JSON.parse(fs.readFileSync(new URL(`${name}.json`, FIXTURES_DIR), 'utf8')) as { bars: Bar[] }).bars.map(toCandle);
}
const fromG = (b: GBar): Candle => ({ ts: b.t, open: b.o, high: b.h, low: b.l, close: b.c, volume: b.v });

function buildSeries(): Record<string, GBar[]> {
  const out: Record<string, GBar[]> = {};
  for (let s = 1; s <= 32; s++) out[`syn-${String(s).padStart(2, '0')}`] = randomSeries(1000 + s * 7919);
  Object.assign(out, tieSeries(), shapeSeries());
  return out;
}

function buildClassify(): GClassify[] {
  const out: GClassify[] = [];
  const rnd = mulberry32(424242);
  const P = DEFAULT_DILDO_PARAMS;
  const add = (o: number, h: number, l: number, c: number, v: number, sumAbsBody: number, sumVol: number, n: number, paramSet = 'defaults') => {
    const ps = PARAM_SETS.find((x) => x.name === paramSet)!;
    const hit = classifyDildo({ ts: 0, open: o, high: h, low: l, close: c, volume: v }, { n, sumAbsBody, sumVol }, { ...P, ...ps.override });
    out.push({ o, h, l, c, v, sumAbsBody, sumVol, n, paramSet, hit: hit && { bodyPct: enc(hit.bodyPct), bodyX: enc(hit.bodyX), volX: enc(hit.volX), topPct: enc(hit.topPct), dollarVol: enc(hit.dollarVol) } });
  };
  // random candles around the thresholds, under the defaults and the loose set
  for (let i = 0; i < 120; i++) {
    const o = roundPx([0.21, 0.6, 1.4, 4.2, 18][i % 5]! * (0.9 + rnd() * 0.2));
    const body = (rnd() - 0.15) * 0.25;
    const c = roundPx(o * (1 + body));
    const h = roundPx(Math.max(o, c) * (1 + rnd() * 0.05));
    const l = roundPx(Math.min(o, c) * (1 - rnd() * 0.03));
    const n = Math.floor(rnd() * 11);
    const v = Math.round((50_000 + rnd() * 900_000) / Math.max(0.2, c));
    const sumAbsBody = rnd() < 0.08 ? 0 : Math.abs(c - o) * n * rnd() * 0.5;
    const sumVol = rnd() < 0.08 ? 0 : Math.round(v * n * rnd() * 0.4);
    add(o, h, l, c, v, sumAbsBody, sumVol, n, i % 3 === 0 ? 'loose' : 'defaults');
  }
  // every default threshold searched to the last ulp: the passing input and its failing neighbour
  const o = 2.5;
  const n = 10;
  const passBody = (c: number) => ((c - o) / o) * 100 >= P.minBodyPct;
  const cB = lowestPassing(passBody, o * 1.08);
  add(o, cB * 1.01, o * 0.99, cB, 200_000, 0.1, 100_000, n);
  add(o, cB * 1.01, o * 0.99, nextDown(cB), 200_000, 0.1, 100_000, n);
  const c = 2.9;
  const l = o * 0.99;
  // topPct grows with the high: the largest high that still closes in the top 20 % (analytic start, then ulps)
  const passTop = (h: number) => ((h - c) / (h - l)) * 100 <= P.maxTopPct;
  const hT = highestPassing(passTop, (c - (P.maxTopPct / 100) * l) / (1 - P.maxTopPct / 100));
  add(o, hT, l, c, 200_000, 0.1, 100_000, n);
  add(o, nextUp(hT), l, c, 200_000, 0.1, 100_000, n);
  // bodyX / volX fall as the priors' sums grow: the largest passing sum
  const sB = highestPassing((s) => (c - o) / (s / n) >= P.minBodyX, ((c - o) * n) / P.minBodyX);
  add(o, 2.92, l, c, 200_000, sB, 100_000, n);
  add(o, 2.92, l, c, 200_000, nextUp(sB), 100_000, n);
  const sV = highestPassing((s) => 200_000 / (s / n) >= P.minVolX, (200_000 * n) / P.minVolX);
  add(o, 2.92, l, c, 200_000, 0.1, sV, n);
  add(o, 2.92, l, c, 200_000, 0.1, nextUp(sV), n);
  // price exactly 0.20 and $vol exactly 100 000 (0.25 x 400 000), and their neighbours
  add(0.16, 0.2, 0.16, 0.2, 600_000, 0, 0, 3);
  add(0.16, 0.2, 0.16, nextDown(0.2), 600_000, 0, 0, 3);
  add(0.2, 0.25, 0.2, 0.25, 400_000, 0, 0, 3);
  add(0.2, 0.25, 0.2, 0.25, 399_999, 0, 0, 3);
  // red, doji, h == l, open 0, 2 vs 3 priors
  add(1, 1.1, 0.9, 0.95, 500_000, 0, 0, 10);
  add(1, 1.1, 0.9, 1, 500_000, 0, 0, 10);
  add(1.2, 1.2, 1.2, 1.2, 500_000, 0, 0, 10);
  add(0, 1.2, 0, 1.2, 500_000, 0, 0, 10);
  add(1, 1.3, 0.99, 1.29, 500_000, 0.01, 1000, 2);
  add(1, 1.3, 0.99, 1.29, 500_000, 0.015, 1500, 3);
  return out;
}

function build(): Omit<GoldenFile, 'note' | 'tfMs'> {
  const paramSets: Record<string, DildoParams> = {};
  for (const ps of PARAM_SETS) paramSets[ps.name] = { ...DEFAULT_DILDO_PARAMS, ...ps.override };
  const series = buildSeries();
  const inputs: { source: 'fixture' | 'series'; name: string; candles: Candle[] }[] = [
    ...fixtureNames().map((name) => ({ source: 'fixture' as const, name, candles: fixtureCandles(name) })),
    ...Object.keys(series)
      .sort()
      .map((name) => ({ source: 'series' as const, name, candles: series[name]!.map(fromG) })),
  ];
  const cases: GCase[] = [];
  const replays: GReplay[] = [];
  for (const inp of inputs) {
    const cs = inp.candles;
    const idxOfTs = new Map(cs.map((c, i) => [c.ts, i]));
    for (const ps of PARAM_SETS) {
      let fullEvents: GEvent[] = [];
      for (const clock of CLOCKS) {
        const nowMs = cs[cs.length - 1]!.ts + clock.offsetMs;
        const r = evaluateDildo({ candles: cs, tfMs: TF_MS, nowMs, params: ps.override });
        const events = r.events.map(encEvent);
        if (clock.name === 'closed') fullEvents = events;
        cases.push({ source: inp.source, name: inp.name, paramSet: ps.name, clock: clock.name, nowMs, bars: cs.length, events, result: encResult(r) });
      }
      // replays only under two param sets (the file stays a reasonable size)
      if (ps.name !== 'defaults' && ps.name !== 'window 10, touch 0, hold 0') continue;
      const cuts = new Set<number>();
      for (const e of fullEvents) {
        const j = idxOfTs.get(e.ts);
        if (j === undefined) continue;
        for (const d of REPLAY_AFTER) if (j + d < cs.length) cuts.add(j + d + 1);
      }
      for (const cut of [...cuts].sort((a, b) => a - b)) {
        const slice = cs.slice(0, cut);
        for (const clock of CLOCKS) {
          const nowMs = slice[slice.length - 1]!.ts + clock.offsetMs;
          const r = evaluateDildo({ candles: slice, tfMs: TF_MS, nowMs, params: ps.override });
          const ev = r.events.map(encEvent);
          replays.push({ source: inp.source, name: inp.name, paramSet: ps.name, clock: clock.name, cut, nowMs, nEvents: ev.length, lastEvent: ev.length ? ev[ev.length - 1]! : null, result: encResult(r) });
        }
      }
    }
  }
  return { paramSets, series, cases, replays, classify: buildClassify() };
}

function serialise(b: ReturnType<typeof build>): string {
  const note =
    'GENERATED by web/src/signals/dildoPb.golden.test.ts (UPDATE_GOLDEN=1) from dildoPb.ts over fixtures/dildo/*.json and the synthetic `series` below. ' +
    'Guarded by that test (TS) and internal/dildopb/golden_test.go (Go). Non-finite numbers are the strings "Infinity" / "NaN"; null = none. Do not edit by hand.';
  const lines = (xs: unknown[]): string => `[\n${xs.map((x) => JSON.stringify(x)).join(',\n')}\n]`;
  const series = `{\n${Object.keys(b.series)
    .sort()
    .map((k) => `${JSON.stringify(k)}:${JSON.stringify(b.series[k])}`)
    .join(',\n')}\n}`;
  return `{"note":${JSON.stringify(note)},"tfMs":${TF_MS},"paramSets":${JSON.stringify(b.paramSets)},"series":${series},"cases":${lines(b.cases)},"replays":${lines(b.replays)},"classify":${lines(b.classify)}}\n`;
}

const caseId = (c: { source: string; name: string; paramSet: string; clock: string }) => `${c.name} | ${c.paramSet} | ${c.clock}`;
const replayId = (r: GReplay) => `${caseId(r)} | cut ${r.cut}`;

const current = build();

describe('dildoPb golden vectors', () => {
  if (process.env.UPDATE_GOLDEN === '1') {
    it('regenerates the golden file', () => {
      fs.writeFileSync(GOLDEN_FILE, serialise(current));
      console.log(`UPDATE_GOLDEN=1: wrote ${current.cases.length} cases + ${current.replays.length} replays + ${current.classify.length} classify vectors to ${GOLDEN_REL}`);
      expect(current.cases.length).toBeGreaterThan(0);
    });
    return;
  }

  const regenerate = 'regenerate with: cd web && UPDATE_GOLDEN=1 npx vitest run src/signals/dildoPb.golden.test.ts (then port the change to internal/dildopb)';
  const golden: GoldenFile | null = fs.existsSync(GOLDEN_FILE) ? (JSON.parse(fs.readFileSync(GOLDEN_FILE, 'utf8')) as GoldenFile) : null;

  it(`has the committed golden file (${GOLDEN_REL})`, () => {
    expect(golden, `${GOLDEN_REL} is missing - ${regenerate}`).not.toBeNull();
  });
  if (!golden) return;

  it('the defaults, the param sets and the synthetic series are the ones in the file', () => {
    expect(golden.paramSets.defaults, `DEFAULT_DILDO_PARAMS changed - ${regenerate}`).toEqual(DEFAULT_DILDO_PARAMS);
    expect(golden.paramSets).toEqual(current.paramSets);
    const canon = (x: Record<string, GBar[]>) => JSON.stringify(Object.keys(x).sort().map((k) => [k, x[k]]));
    const same = canon(golden.series) === canon(current.series);
    expect(same, `the synthetic series changed - ${regenerate}`).toBe(true);
  });

  it('covers exactly the fixtures x param sets x clocks (and replay cuts) of today', () => {
    expect(golden.cases.map(caseId), `the case list differs - ${regenerate}`).toEqual(current.cases.map(caseId));
    expect(golden.replays.map(replayId), `the replay list differs - ${regenerate}`).toEqual(current.replays.map(replayId));
  });

  it(`cases: ${current.cases.length} runs match`, () => {
    const byId = new Map(golden.cases.map((c) => [caseId(c), c]));
    for (const c of current.cases) expect(JSON.stringify(c), caseId(c)).toBe(JSON.stringify(byId.get(caseId(c))));
  });

  it(`replays: ${current.replays.length} truncated runs match`, () => {
    const byId = new Map(golden.replays.map((r) => [replayId(r), r]));
    for (const r of current.replays) {
      const g = byId.get(replayId(r));
      if (JSON.stringify(r) !== JSON.stringify(g)) expect(JSON.stringify(r), replayId(r)).toBe(JSON.stringify(g));
    }
  });

  it(`classify: ${current.classify.length} vectors match`, () => {
    expect(JSON.stringify(current.classify), regenerate).toBe(JSON.stringify(golden.classify));
  });

  // The vacuity guard: parity over runs that never fire proves nothing.
  it('exercises every event kind, every cancel reason and every pbState', () => {
    const evs = current.cases.flatMap((c) => c.events);
    for (const k of ['dildo', 'dildoPb', 'dildoCancel']) expect(evs.some((e) => e.kind === k), `no '${k}' event`).toBe(true);
    for (const r of ['floor', 'ema', 'faded']) expect(evs.some((e) => e.reason === r), `no '${r}' cancel`).toBe(true);
    const states = new Set([...current.cases, ...current.replays].map((c) => c.result.pbState));
    for (const s of [0, 1, 2, 3, 4, 5]) expect(states.has(s), `no run ends in pbState ${s}`).toBe(true);
    const inf = current.cases.some((c) => c.events.some((e) => e.kind === 'dildo' && e.note.includes('∞')));
    expect(inf, 'no dildo with an infinite multiple (a prior mean of 0)').toBe(true);
    expect(current.classify.filter((v) => v.hit).length, 'classify vectors that hit').toBeGreaterThan(20);
    expect(current.classify.filter((v) => !v.hit).length, 'classify vectors that miss').toBeGreaterThan(20);
    // the synthetic corpus must supersede and cross days, not just the hand-built shapes
    const supersede = current.cases.filter((c) => c.paramSet === 'defaults' && c.clock === 'closed' && c.events.some((e, i, a) => e.kind === 'dildo' && i > 0 && a[i - 1]!.kind === 'dildo'));
    expect(supersede.length, 'no back-to-back dildos (latest supersedes)').toBeGreaterThan(2);
  });

  it('the exact-tie series land on their boundary and decide as the rule says', () => {
    const at = (name: string) => current.cases.find((c) => c.name === name && c.paramSet === 'defaults' && c.clock === 'closed')!;
    const kinds = (name: string) => at(name).events.map((e) => `${e.kind}${e.reason ? `/${e.reason}` : ''}`);
    expect(kinds('tie-touch-equal')).toEqual(['dildo', 'dildoPb']);
    expect(kinds('tie-touch-ulp')).toEqual(['dildo']);
    expect(kinds('tie-floor-equal')).toEqual(['dildo', 'dildoCancel/floor']);
    expect(kinds('tie-floor-ulp')).toEqual(['dildo', 'dildoPb']);
    expect(kinds('tie-hold-equal')).toEqual(['dildo', 'dildoPb']);
    expect(kinds('tie-hold-ulp')).toEqual(['dildo', 'dildoCancel/ema']);
    // ... and the bars really are ON the boundary (a rule change that moved it must rebuild them)
    const P = DEFAULT_DILDO_PARAMS;
    const touchK = 1 + P.touchPct / 100;
    const holdK = 1 - P.holdPct / 100;
    const k = 2 / (P.emaPeriod + 1);
    const s = current.series['tie-touch-equal']!.map(fromG);
    const ema = emaSeries(s.map((c) => c.close), P.emaPeriod);
    expect(s[16]!.low, 'the touch bar low IS the touch level').toBe(ema[15]! * touchK);
    const h = current.series['tie-hold-equal']!.map(fromG);
    const emaH = emaSeries(h.map((c) => c.close), P.emaPeriod);
    const c = h[16]!.close;
    const below = nextDown(c);
    expect(c >= emaH[16]! * holdK, 'the hold close passes').toBe(true);
    expect(below < (below * k + emaH[15]! * (1 - k)) * holdK, 'one ulp under it fails').toBe(true);
  });
});
