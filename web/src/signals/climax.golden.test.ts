// GOLDEN VECTORS for the VOLUME CLIMAX -> BOTTOM CONFIRMED rule: the contract
// between climax.ts (the rule, TypeScript, what the Signal window draws) and
// internal/climax (the Go port: Classify and the Latch the scanner engine runs
// for BOTH market-wide alerts, and Evaluate).
//
// Both implementations are pinned to ONE committed file,
// src/signals/fixtures/golden/climax-golden.json, in the emaflag-golden shape
// ({note, tfMs, paramSets, cases, classify} plus `series` and `replays`):
//   - THIS test runs evaluateClimax / classifyClimax over the inputs below and
//     fails if the output differs from the committed file;
//   - internal/climax/golden_test.go replays the SAME inputs through the Go
//     Evaluate / Classify and fails on any difference: events (ts, kind, label,
//     reason; price as exact float64 bits through JSON), every Result field,
//     and every Classify vector. Go ports no note text, so notes are not in the
//     file (the TS unit tests pin them).
//
// REGENERATE after an intentional change to climax.ts or the fixtures, then port
// the change to Go until `go test ./internal/climax/` is green again:
//
//   cd web && UPDATE_GOLDEN=1 npx vitest run src/signals/climax.golden.test.ts
//   (PowerShell:  $env:UPDATE_GOLDEN='1'; npx vitest run src/signals/climax.golden.test.ts)
//
// then run it again WITHOUT the variable (the guard). Never hand-edit it.
//
// Inputs: the five real fixtures in fixtures/climax/ (NOT fixtures/*.json, which
// the emaflag golden tests glob) plus ~40 SYNTHETIC series written INTO the
// file (`series`): seeded-PRNG sell-offs with climax bars and random follow-ups
// (confirm / fail / expire / fail-and-re-arm), thin tapes, and hand-built
// shapes for every boundary: drop exactly 6 %, volume exactly 8x and equal to
// the biggest prior, a wick of exactly 25 %, a low EQUAL to the priors' lowest
// (fresh) or one tick over, a close equal to the EMA, the mute edges
// 09:29 / 09:30 / 09:34 / 09:35, the core edges 09:59 / 10:00 / 15:59 / 16:00,
// 9 vs 10 bars today with yesterday's bars in front, a 19:5x climax with no bar
// after 20:00, and a supersede on an equal low. Five parameter sets
// (defaults, noEma, core, k3, loose = N10 D4 V4 W0 $0.20 $25k) x two clocks
// ('closed': last.t + 10 min; 'forming': last.t + 30 s); REPLAYS cut each run
// at, and 1-2 bars after, every event's bar. `classify` holds ~200 direct
// classifyClimax vectors with bit-exact inputs (random, and searched to the
// last ulp of each threshold) and the expected Hit or null.
//
// Non-finite numbers are written as the strings "Infinity" / "-Infinity" /
// "NaN"; null = none.
import { describe, expect, it } from 'vitest';
import { DEFAULT_CLIMAX_PARAMS, classifyClimax, evaluateClimax, type ClimaxHit, type ClimaxParams, type ClimaxResult } from './climax';
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

const FIXTURES_DIR = new URL('./fixtures/climax/', import.meta.url);
const GOLDEN_FILE = new URL('./fixtures/golden/climax-golden.json', import.meta.url);
const GOLDEN_REL = 'web/src/signals/fixtures/golden/climax-golden.json';
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

/** The smallest x (ulp steps from x0, which must be near the boundary) for which the increasing `ok` holds. */
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
const highestPassing = (ok: (x: number) => boolean, x0: number) => -lowestPassing((y) => ok(-y), -x0);

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

const ET_OFF = 4 * 3_600_000; // EDT
const etMs = (y: number, mo: number, d: number, hh: number, mm: number) => Date.UTC(y, mo - 1, d, hh, mm) + ET_OFF;

interface GBar {
  t: number;
  o: number;
  h: number;
  l: number;
  c: number;
  v: number;
}
type Row = [number, number, number, number, number]; // o h l c v
const rows = (t0: number, rs: Row[]): GBar[] => rs.map((r, i) => ({ t: t0 + i * MIN, o: r[0], h: r[1], l: r[2], c: r[3], v: r[4] }));
const fromG = (b: GBar): Candle => ({ ts: b.t, open: b.o, high: b.h, low: b.l, close: b.c, volume: b.v });

function randomSeries(seed: number): GBar[] {
  const rnd = mulberry32(seed);
  const scale = [0.62, 1.7, 4.4, 12, 48][seed % 5]!;
  const thin = seed % 4 === 1;
  const out: GBar[] = [];
  let px = scale;
  const baseVol = Math.round((90_000 / scale) * (0.5 + rnd()));
  const push = (t: number, o: number, h: number, l: number, c: number, v: number) => {
    o = roundPx(o);
    c = roundPx(c);
    h = roundPx(Math.max(h, o, c));
    l = roundPx(Math.min(l, o, c));
    out.push({ t, o, h, l, c, v: Math.max(0, Math.round(v)) });
    px = c;
  };
  const day = (start: number, minutes: number) => {
    let mode: 'drift' | 'selloff' | 'after' = 'drift';
    let left = 0;
    let follow = 0;
    for (let m = 0; m < minutes; m++) {
      const t = start + m * MIN;
      if (thin && rnd() < 0.4) continue;
      const o = px * (1 + (rnd() - 0.5) * 0.003);
      if (mode === 'drift' && rnd() < 0.06) {
        mode = 'selloff';
        left = 4 + Math.floor(rnd() * 8);
      }
      if (mode === 'selloff') {
        left--;
        if (left <= 0) {
          // the climax candidate: a flush to a fresh low on a volume spike, closing off the low
          const low = o * (1 - 0.03 - rnd() * 0.1);
          const close = low + (o - low) * (0.2 + rnd() * 0.7);
          push(t, o, o * (1 + rnd() * 0.01), low, close, baseVol * (4 + rnd() * 30));
          mode = 'after';
          follow = Math.floor(rnd() * 3); // 0 bounce (confirm), 1 undercut (fail), 2 chop (expire)
          left = 6 + Math.floor(rnd() * 10);
          continue;
        }
        const c = o * (1 - 0.005 - rnd() * 0.02);
        push(t, o, o * (1 + rnd() * 0.004), c * (1 - rnd() * 0.006), c, baseVol * (0.8 + rnd() * 1.5));
        continue;
      }
      if (mode === 'after') {
        left--;
        if (left <= 0) mode = 'drift';
        const d = follow === 0 ? 0.004 + rnd() * 0.02 : follow === 1 ? -0.006 - rnd() * 0.02 : (rnd() - 0.5) * 0.004;
        const c = o * (1 + d);
        push(t, o, Math.max(o, c) * (1 + rnd() * 0.004), Math.min(o, c) * (1 - rnd() * 0.004), c, baseVol * (0.5 + rnd() * 2));
        continue;
      }
      const c = o * (1 + (rnd() - 0.5) * 0.01);
      push(t, o, Math.max(o, c) * (1 + rnd() * 0.004), Math.min(o, c) * (1 - rnd() * 0.004), c, baseVol * (0.3 + rnd() * 1.4));
    }
  };
  if (seed % 3 !== 0) day(etMs(2026, 10, 6, 15, 0), 60 + Math.floor(rnd() * 60));
  const start = [
    [4, 0],
    [9, 0],
    [10, 40],
    [14, 50],
    [19, 0],
  ][seed % 5]!;
  day(etMs(2026, 10, 7, start[0]!, start[1]!), 80 + Math.floor(rnd() * 80));
  return out;
}

// 10 priors: highs 50 (one at exactly 50), lows >= 48, volumes 10 000 each; then the candle.
const PRIORS: Row[] = Array.from({ length: 10 }, (_, i): Row => [49.5, i === 4 ? 50 : 49.8, 48 + (i % 3) * 0.2, 49.2, 10_000]);

function shapeSeries(): Record<string, GBar[]> {
  const out: Record<string, GBar[]> = {};
  const at = (hh: number, mm: number) => etMs(2026, 10, 7, hh, mm);
  // two bars after it: the second closes 49.5, over the candle's midpoint 49 -> BOTTOM CONFIRMED
  const after: Row[] = [
    [48, 48.6, 47.6, 48.5, 30_000],
    [48.5, 49.6, 48.2, 49.5, 20_000],
  ];
  // the candle: low 47 ((50 - 47)/50 = exactly 6 %), 80 000 shares = exactly 8x and the biggest, wick (48 - 47)/(51 - 47) = 25 %
  const edge: Row = [48, 51, 47, 48.5, 80_000];
  out['shape-edges-exact'] = rows(at(11, 0), [...PRIORS, edge, ...after]);
  out['shape-drop-ulp'] = rows(at(11, 0), [...PRIORS, [48, 51, nextUp(47), 48.5, 80_000], ...after]); // just under 6 %
  out['shape-volx-under'] = rows(at(11, 0), [...PRIORS, [48, 51, 47, 48.5, 79_999], ...after]);
  out['shape-wick-ulp'] = rows(at(11, 0), [...PRIORS, [nextDown(48), 51, 47, 48.5, 80_000], ...after]);
  out['shape-low-equal-priors'] = rows(at(11, 0), [...PRIORS.slice(0, 9), [49.5, 49.8, 47, 49.2, 10_000], edge, ...after]); // low == the priors' lowest: fresh
  out['shape-low-tick-over'] = rows(at(11, 0), [...PRIORS.slice(0, 9), [49.5, 49.8, 46.99, 49.2, 10_000], edge, ...after]); // a prior low under it: not fresh
  out['shape-maxvol-tie'] = rows(at(11, 0), [...PRIORS.slice(0, 9), [49.5, 49.8, 48.2, 49.2, 80_000], edge, ...after]); // volume == the biggest prior (8x fails: mean 17 000)
  // the mute and core edges: the same candle starting 09:29 / 09:30 / 09:34 / 09:35 / 09:59 / 10:00 / 15:59 / 16:00
  for (const [hh, mm] of [
    [9, 29],
    [9, 30],
    [9, 34],
    [9, 35],
    [9, 59],
    [10, 0],
    [15, 59],
    [16, 0],
  ] as const) {
    out[`shape-at-${String(hh).padStart(2, '0')}${String(mm).padStart(2, '0')}`] = rows(at(hh, mm) - 10 * MIN, [...PRIORS, edge, ...after]);
  }
  // 9 bars today with yesterday's bars in front: no climax; 10 today: a climax
  const y = rows(etMs(2026, 10, 6, 19, 40), PRIORS);
  out['shape-nine-today'] = [...y, ...rows(at(4, 0), [...PRIORS.slice(1), edge, ...after])];
  out['shape-ten-today'] = [...y, ...rows(at(4, 0), [...PRIORS, edge, ...after])];
  // a 19:5x climax with no bar after 20:00 (the next bars are tomorrow's)
  out['shape-1950-no-after'] = [...rows(etMs(2026, 10, 6, 19, 40), [...PRIORS, edge, [48.5, 48.6, 48.2, 48.3, 5000]]), ...rows(at(4, 0), [[48.3, 49.6, 48.2, 49.5, 9000]])];
  // supersede on an EQUAL low: a second climax whose low equals the first (no fail), still watching
  out['shape-supersede-equal-low'] = rows(at(12, 0), [...PRIORS, edge, [48.4, 48.45, 47, 48.1, 200_000], [48.1, 48.2, 47.5, 47.6, 30_000]]);
  // fail and re-arm on one bar, then a confirm
  out['shape-fail-rearm'] = rows(at(12, 30), [...PRIORS, edge, [48.4, 48.5, 45, 46.5, 400_000], [46.5, 47.5, 46.2, 47.4, 50_000]]);
  // a close EQUAL to the EMA (fails the strict test): flat closes so the EMA is the close
  const flat: Row[] = Array.from({ length: 10 }, (): Row => [50, 50, 50, 50, 10_000]);
  out['shape-close-at-ema'] = rows(at(13, 0), [...flat, ...flat, [50, 50.5, 46, 50, 120_000], ...after]);
  return out;
}

// ─── the golden shapes ───────────────────────────────────────────────────

type Num = number | string | null;
const enc = (v: number | null | undefined): Num => (v == null ? null : Number.isFinite(v) ? v : String(v));

interface GEvent {
  ts: number;
  kind: string;
  label: string;
  price: Num;
  reason: string | null;
}
interface GResult {
  lastClimaxTs: Num;
  climaxLow: Num;
  climaxMid: Num;
  dropPct: Num;
  volX: Num;
  wickPct: Num;
  dollarVol: Num;
  state: number;
  bars: number;
  confTs: Num;
  confPx: Num;
  failTs: Num;
  endTs: Num;
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
  pr: { n: number; hi: Num; lo: Num; sumVol: number; maxVol: number };
  ema: Num;
  etMin: number;
  paramSet: string;
  hit: Record<keyof ClimaxHit, Num> | null;
}
interface GoldenFile {
  note: string;
  tfMs: number;
  paramSets: Record<string, ClimaxParams>;
  series: Record<string, GBar[]>;
  cases: GCase[];
  replays: GReplay[];
  classify: GClassify[];
}

const PARAM_SETS: { name: string; override: Partial<ClimaxParams> }[] = [
  { name: 'defaults', override: {} },
  { name: 'noEma', override: { requireBelowEma: false } },
  { name: 'core', override: { coreHoursOnly: true } },
  { name: 'k3', override: { confirmBars: 3 } },
  { name: 'loose', override: { lookback: 10, minDropPct: 4, minVolX: 4, minWickPct: 0, minPrice: 0.2, minDollarVol: 25_000 } },
];
const CLOCKS = [
  { name: 'closed' as const, offsetMs: 10 * MIN },
  { name: 'forming' as const, offsetMs: 30_000 },
];
const REPLAY_AFTER = [0, 1, 2];

const encEvent = (e: ClimaxResult['events'][number]): GEvent => ({ ts: e.ts, kind: e.kind, label: e.label, price: enc(e.price), reason: e.reason ?? null });
const encResult = (r: ClimaxResult): GResult => ({
  lastClimaxTs: enc(r.lastClimaxTs),
  climaxLow: enc(r.climaxLow),
  climaxMid: enc(r.climaxMid),
  dropPct: enc(r.dropPct),
  volX: enc(r.volX),
  wickPct: enc(r.wickPct),
  dollarVol: enc(r.dollarVol),
  state: r.state,
  bars: r.bars,
  confTs: enc(r.confTs),
  confPx: enc(r.confPx),
  failTs: enc(r.failTs),
  endTs: enc(r.endTs),
});

function fixtureNames(): string[] {
  return fs
    .readdirSync(FIXTURES_DIR)
    .filter((f) => f.endsWith('.json'))
    .map((f) => f.replace(/\.json$/, ''))
    .sort();
}
const fixtureCandles = (name: string): Candle[] => (JSON.parse(fs.readFileSync(new URL(`${name}.json`, FIXTURES_DIR), 'utf8')) as { bars: Bar[] }).bars.map(toCandle);

function buildSeries(): Record<string, GBar[]> {
  const out: Record<string, GBar[]> = {};
  for (let s = 1; s <= 24; s++) out[`syn-${String(s).padStart(2, '0')}`] = randomSeries(5000 + s * 104_729);
  Object.assign(out, shapeSeries());
  return out;
}

function buildClassify(): GClassify[] {
  const out: GClassify[] = [];
  const rnd = mulberry32(777);
  const add = (o: number, h: number, l: number, c: number, v: number, pr: { n: number; hi: number; lo: number; sumVol: number; maxVol: number }, ema: number | null, etMin: number, paramSet = 'defaults') => {
    const ps = PARAM_SETS.find((x) => x.name === paramSet)!;
    const hit = classifyClimax({ ts: 0, open: o, high: h, low: l, close: c, volume: v }, pr, ema, etMin, { ...DEFAULT_CLIMAX_PARAMS, ...ps.override });
    out.push({
      o,
      h,
      l,
      c,
      v,
      pr: { n: pr.n, hi: enc(pr.hi), lo: enc(pr.lo), sumVol: pr.sumVol, maxVol: pr.maxVol },
      ema: enc(ema),
      etMin,
      paramSet,
      hit: hit && { dropPct: enc(hit.dropPct), volX: enc(hit.volX), wickPct: enc(hit.wickPct), dollarVol: enc(hit.dollarVol), emaDistPct: enc(hit.emaDistPct), mid: enc(hit.mid) },
    });
  };
  // random candidates around the thresholds
  const sets = ['defaults', 'noEma', 'core', 'loose'];
  for (let i = 0; i < 160; i++) {
    const s = [0.55, 1.3, 3.8, 9.5, 40][i % 5]!;
    const hi = roundPx(s * (1 + rnd() * 0.05));
    const lo = roundPx(s * (0.93 + rnd() * 0.05));
    const l = roundPx(lo * (0.9 + rnd() * 0.12));
    const h = roundPx(Math.max(l * (1 + rnd() * 0.08), l + tick(l)));
    const o = roundPx(l + (h - l) * rnd());
    const c = roundPx(l + (h - l) * rnd());
    const n = i % 9 === 0 ? 9 : 10;
    const meanV = 5000 + rnd() * 80_000;
    const sumVol = rnd() < 0.05 ? 0 : Math.round(meanV * n);
    const maxVol = sumVol === 0 ? 0 : Math.round(meanV * (1.2 + rnd() * 3));
    const v = Math.round(meanV * (rnd() * 16));
    const ema = rnd() < 0.1 ? null : roundPx(c * (0.97 + rnd() * 0.08));
    const etMin = [240, 569, 570, 574, 575, 599, 600, 700, 959, 960, 1190][i % 11]!;
    add(o, h, l, c, v, { n, hi, lo, sumVol, maxVol }, ema, etMin, sets[i % sets.length]);
  }
  // every threshold to the last ulp: the passing input and its failing neighbour
  const P = DEFAULT_CLIMAX_PARAMS;
  const pr = { n: 10, hi: 50, lo: 48, sumVol: 100_000, maxVol: 10_000 };
  const o = 48;
  const h = 51;
  const c = 48.5;
  const v = 80_000;
  const ET = 700;
  // drop falls as the low rises: the highest passing low
  const lD = highestPassing((l) => ((pr.hi - l) / pr.hi) * 100 >= P.minDropPct, pr.hi * (1 - P.minDropPct / 100));
  add(o, h, lD, c, v, pr, 49, ET);
  add(o, h, nextUp(lD), c, v, pr, 49, ET);
  // volX falls as the priors' volume grows: the largest passing sum
  const sV = highestPassing((s) => v / (s / pr.n) >= P.minVolX, (v * pr.n) / P.minVolX);
  add(o, h, 47, c, v, { ...pr, sumVol: sV }, 49, ET);
  add(o, h, 47, c, v, { ...pr, sumVol: nextUp(sV) }, 49, ET);
  // wick grows with min(open, close): the lowest passing open (close above it)
  const oW = lowestPassing((x) => ((Math.min(x, c) - 47) / (h - 47)) * 100 >= P.minWickPct, 47 + ((h - 47) * P.minWickPct) / 100);
  add(oW, h, 47, c, v, pr, 49, ET);
  add(nextDown(oW), h, 47, c, v, pr, 49, ET);
  // volume == the biggest prior, and one share under
  add(o, h, 47, c, v, { ...pr, maxVol: v }, 49, ET);
  add(o, h, 47, c, v, { ...pr, maxVol: v + 1 }, 49, ET);
  // fresh low: equal, and one ulp over
  add(o, h, 47, c, v, { ...pr, lo: 47 }, 49, ET);
  add(o, h, 47, c, v, { ...pr, lo: nextDown(47) }, 49, ET);
  // close == EMA, one ulp over it, unknown EMA (with and without the EMA test)
  add(o, h, 47, c, v, pr, c, ET);
  add(o, h, 47, c, v, pr, nextUp(c), ET);
  add(o, h, 47, c, v, pr, null, ET);
  add(o, h, 47, c, v, pr, null, ET, 'noEma');
  // price 0.50 and $vol 50 000 exactly, and their neighbours
  const cheap = { n: 10, hi: 0.6, lo: 0.49, sumVol: 100_000, maxVol: 10_000 };
  add(0.52, 0.53, 0.48, 0.5, 100_000, cheap, 0.55, ET);
  add(0.52, 0.53, 0.48, nextDown(0.5), 100_000, cheap, 0.55, ET, 'loose');
  add(0.52, 0.53, 0.48, 0.5, 99_999, cheap, 0.55, ET);
  add(0.52, 0.53, 0.48, nextDown(0.5), 1_000_000, cheap, 0.55, ET);
  // the mute and core edges, a prior mean of 0, h == l, 9 priors, priors' high 0
  for (const m of [569, 570, 574, 575]) add(o, h, 47, c, v, pr, 49, m);
  for (const m of [599, 600, 959, 960]) add(o, h, 47, c, v, pr, 49, m, 'core');
  add(o, h, 47, c, v, { ...pr, sumVol: 0, maxVol: 0 }, 49, ET);
  add(47, 47, 47, 47, v, pr, 49, ET, 'loose');
  add(o, h, 47, c, v, { ...pr, n: 9 }, 49, ET);
  add(o, h, 47, c, v, { n: 10, hi: 0, lo: 48, sumVol: 100_000, maxVol: 10_000 }, 49, ET, 'loose');
  return out;
}

function build(): Omit<GoldenFile, 'note' | 'tfMs'> {
  const paramSets: Record<string, ClimaxParams> = {};
  for (const ps of PARAM_SETS) paramSets[ps.name] = { ...DEFAULT_CLIMAX_PARAMS, ...ps.override };
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
        const r = evaluateClimax({ candles: cs, tfMs: TF_MS, nowMs, params: ps.override });
        const events = r.events.map(encEvent);
        if (clock.name === 'closed') fullEvents = events;
        cases.push({ source: inp.source, name: inp.name, paramSet: ps.name, clock: clock.name, nowMs, bars: cs.length, events, result: encResult(r) });
      }
      if (ps.name !== 'defaults' && ps.name !== 'loose') continue; // replays under two sets keep the file a sane size
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
          const r = evaluateClimax({ candles: slice, tfMs: TF_MS, nowMs, params: ps.override });
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
    'GENERATED by web/src/signals/climax.golden.test.ts (UPDATE_GOLDEN=1) from climax.ts over fixtures/climax/*.json and the synthetic `series` below. ' +
    'Guarded by that test (TS) and internal/climax/golden_test.go (Go). Non-finite numbers are the strings "Infinity" / "NaN"; null = none. Do not edit by hand.';
  const lines = (xs: unknown[]): string => `[\n${xs.map((x) => JSON.stringify(x)).join(',\n')}\n]`;
  const series = `{\n${Object.keys(b.series)
    .sort()
    .map((k) => `${JSON.stringify(k)}:${JSON.stringify(b.series[k])}`)
    .join(',\n')}\n}`;
  return `{"note":${JSON.stringify(note)},"tfMs":${TF_MS},"paramSets":${JSON.stringify(b.paramSets)},"series":${series},"cases":${lines(b.cases)},"replays":${lines(b.replays)},"classify":${lines(b.classify)}}\n`;
}

const caseId = (c: { name: string; paramSet: string; clock: string }) => `${c.name} | ${c.paramSet} | ${c.clock}`;
const replayId = (r: GReplay) => `${caseId(r)} | cut ${r.cut}`;

const current = build();

describe('climax golden vectors', () => {
  if (process.env.UPDATE_GOLDEN === '1') {
    it('regenerates the golden file', () => {
      fs.writeFileSync(GOLDEN_FILE, serialise(current));
      console.log(`UPDATE_GOLDEN=1: wrote ${current.cases.length} cases + ${current.replays.length} replays + ${current.classify.length} classify vectors to ${GOLDEN_REL}`);
      expect(current.cases.length).toBeGreaterThan(0);
    });
    return;
  }

  const regenerate = 'regenerate with: cd web && UPDATE_GOLDEN=1 npx vitest run src/signals/climax.golden.test.ts (then port the change to internal/climax)';
  const golden: GoldenFile | null = fs.existsSync(GOLDEN_FILE) ? (JSON.parse(fs.readFileSync(GOLDEN_FILE, 'utf8')) as GoldenFile) : null;

  it(`has the committed golden file (${GOLDEN_REL})`, () => {
    expect(golden, `${GOLDEN_REL} is missing - ${regenerate}`).not.toBeNull();
  });
  if (!golden) return;

  it('the defaults, the param sets and the synthetic series are the ones in the file', () => {
    expect(golden.paramSets.defaults, `DEFAULT_CLIMAX_PARAMS changed - ${regenerate}`).toEqual(DEFAULT_CLIMAX_PARAMS);
    expect(golden.paramSets).toEqual(current.paramSets);
    const canon = (x: Record<string, GBar[]>) => JSON.stringify(Object.keys(x).sort().map((k) => [k, x[k]]));
    expect(canon(golden.series) === canon(current.series), `the synthetic series changed - ${regenerate}`).toBe(true);
  });

  it('covers exactly the fixtures x param sets x clocks (and replay cuts) of today', () => {
    expect(golden.cases.map(caseId), `the case list differs - ${regenerate}`).toEqual(current.cases.map(caseId));
    expect(golden.replays.map(replayId), `the replay list differs - ${regenerate}`).toEqual(current.replays.map(replayId));
  });

  it(`cases: ${current.cases.length} runs match`, () => {
    const byId = new Map(golden.cases.map((c) => [caseId(c), c]));
    for (const c of current.cases) {
      const g = byId.get(caseId(c));
      if (JSON.stringify(c) !== JSON.stringify(g)) expect(JSON.stringify(c), caseId(c)).toBe(JSON.stringify(g));
    }
  });

  it(`replays: ${current.replays.length} truncated runs match`, () => {
    const byId = new Map(golden.replays.map((r) => [replayId(r), r]));
    for (const r of current.replays) {
      const g = byId.get(replayId(r));
      if (JSON.stringify(r) !== JSON.stringify(g)) expect(JSON.stringify(r), replayId(r)).toBe(JSON.stringify(g));
    }
  });

  it(`classify: ${current.classify.length} vectors match`, () => {
    expect(JSON.stringify(current.classify) === JSON.stringify(golden.classify), regenerate).toBe(true);
  });

  it('exercises every event kind and every state, the forming fail and the supersede', () => {
    const evs = current.cases.flatMap((c) => c.events);
    for (const k of ['climax', 'bottom', 'climaxFail']) expect(evs.some((e) => e.kind === k), `no '${k}' event`).toBe(true);
    const states = new Set([...current.cases, ...current.replays].map((c) => c.result.state));
    for (const s of [0, 1, 2, 3, 4]) expect(states.has(s), `no run ends in state ${s}`).toBe(true);
    // a forming-bar undercut: state 3 with failTs on the (forming) last bar and no fail event at it
    expect(
      current.replays.some((r) => r.clock === 'forming' && r.result.state === 3 && r.result.failTs === r.nowMs - 30_000 && r.lastEvent?.kind !== 'climaxFail'),
      'no forming-bar fail',
    ).toBe(true);
    const at = (name: string, ps = 'defaults') => current.cases.find((c) => c.name === name && c.paramSet === ps && c.clock === 'closed')!;
    const kinds = (name: string, ps = 'defaults') => at(name, ps).events.map((e) => e.kind).join(',');
    expect(kinds('shape-edges-exact')).toBe('climax,bottom');
    expect(kinds('shape-drop-ulp')).toBe('');
    expect(kinds('shape-volx-under')).toBe('');
    expect(kinds('shape-wick-ulp')).toBe('');
    expect(kinds('shape-low-equal-priors')).toBe('climax,bottom');
    expect(kinds('shape-low-tick-over')).toBe('');
    expect(kinds('shape-at-0929')).toBe('climax,bottom');
    expect(kinds('shape-at-0930')).toBe('');
    expect(kinds('shape-at-0934')).toBe('');
    expect(kinds('shape-at-0935')).toBe('climax,bottom');
    expect(kinds('shape-at-0959', 'core')).toBe('');
    expect(kinds('shape-at-1000', 'core')).toBe('climax,bottom');
    expect(kinds('shape-at-1559', 'core')).toBe('climax,bottom');
    expect(kinds('shape-at-1600', 'core')).toBe('');
    expect(kinds('shape-nine-today')).not.toContain('climax');
    expect(kinds('shape-ten-today')).toContain('climax');
    expect(kinds('shape-supersede-equal-low')).toBe('climax,climax');
    expect(kinds('shape-fail-rearm')).toBe('climax,climaxFail,climax,bottom');
    expect(kinds('shape-close-at-ema')).toBe('');
    expect(kinds('shape-close-at-ema', 'noEma')).toContain('climax');
    expect(at('shape-1950-no-after').result.state, 'the 19:5x climax is yesterday: none today').toBe(0);
    expect(current.classify.filter((v) => v.hit).length).toBeGreaterThan(30);
    expect(current.classify.filter((v) => !v.hit).length).toBeGreaterThan(30);
    const synFiring = new Set(current.cases.filter((c) => c.name.startsWith('syn-') && c.events.some((e) => e.kind === 'climax')).map((c) => c.name));
    expect(synFiring.size, 'the random series rarely climax').toBeGreaterThanOrEqual(10);
  });
});
