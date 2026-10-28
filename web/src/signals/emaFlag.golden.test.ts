// GOLDEN VECTORS for the 9 EMA flag engine: the contract between emaFlag.ts (the
// rules, TypeScript, what the Signal window runs) and internal/emaflag (the Go
// port the market scan runs server-side, see README "9EMA flag market scan").
//
// Two implementations of one rule set stay identical because both are pinned
// to ONE committed file, src/signals/fixtures/golden/emaflag-golden.json:
//
//   - THIS test runs evaluateFlag over every fixture in
//     src/signals/fixtures/*.json x seven parameter sets x two clocks and fails
//     if the output differs from the committed file (a rule change in emaFlag.ts
//     that was not followed by a regenerate);
//   - internal/emaflag/golden_test.go replays the SAME bars through the Go
//     Evaluate and fails if it differs from the same file (a rule change the Go
//     port has not followed, or the other way round).
//
// REGENERATE after an intentional change to emaFlag.ts or the fixtures (a new
// fixture also needs it: the case list must match), then port the change to Go
// until `go test ./internal/emaflag/` is green again:
//
//   cd web && UPDATE_GOLDEN=1 npx vitest run src/signals/emaFlag.golden.test.ts
//   (PowerShell:  $env:UPDATE_GOLDEN='1'; npx vitest run src/signals/emaFlag.golden.test.ts)
//
// then run it again WITHOUT the variable (that is the guard) and commit the
// regenerated JSON together with the change that caused it. Never hand-edit it.
//
// What a case records: the fixture, the FULL parameter set evaluateFlag ran with
// (so Go never has to guess a default - and the "defaults" cases pin
// DEFAULT_FLAG_PARAMS against Go's DefaultParams()), the clock (nowMs), and the
// call the engine made: the state, EVERY event (ts, kind, label, price) of
// every day in emission order, and the numeric levels the market scan reads off
// the result (null where the engine says null). Event notes, the headline and
// the box geometry are text/drawing the Go port does not carry, so they are not
// in the file. Two clocks per run because the forming-bar pass is a separate
// code path: 'closed' (last.t + 10 min: every bar closed) and 'forming'
// (last.t + 30 s: the last bar is still printing).
//
// REPLAY cases (a second list in the same file). The full-fixture cases all end
// after the run is over (state idle, a few impulse), so on their own they would
// leave the values the market scan actually alerts on - trigger, stop, flag
// bars, the timestamp behind e9Age, the READY / LONG states, a break on a
// FORMING bar - almost never non-null. A replay is the same engine run on a
// fixture TRUNCATED to its first `cut` bars, at both clocks, with the cuts at,
// and two bars after, every event of that fixture x param set's full run: the
// moments the machine is mid-flag or mid-position. A replay records the summary
// only (state, event count, the last event, the levels): the event list of a
// truncated run is a prefix of the full run's, which the full cases pin in full.
//
// EXACT-TIE fixtures (the synthetic zz-tie-* files in the same folder). The real
// fixtures never put a bar exactly ON a rule boundary, so a strict-vs-inclusive
// slip (`>` for `>=`, `<=` for `<`) in either language passes every real case and
// the two engines can drift apart without a test noticing. The zz-tie-* fixtures
// are real BEAT / SAIQ 2026-10-05 bars, unmodified, plus - where a bar has to be
// exact - one or two bars built from the engine's own numbers (its trigger, its
// stop), so a comparison lands on its boundary to the last bit. Every file says
// how in its `source`, and the 'exact ties' tests below fail loudly if a rule
// change moves a fixture off its boundary (rebuild it then):
//
//   zz-tie-trigger-equal / -ulp   a bar whose HIGH is exactly the flag's trigger
//        (no break: strict >) and one whose HIGH is one ulp above it (break)
//   zz-tie-stop-equal / -ulp      a bar whose LOW and CLOSE are exactly the open
//        position's stop (a close under it exits, so no exit; with
//        stopByClose=false the inclusive low <= stop exits) and one ulp under it
//   zz-tie-ema-period-1          BEAT's whole day under the parameter set "ties:
//        emaPeriod=1, zero bands": the EMA IS the close, and impulse extension,
//        the EMA floor, FLAG FAILED and READY all compare equal numbers
//   zz-tie-ready-extend          SAIQ's first 31 bars of 2026-10-05, unmodified,
//        under a readyDistPct for which ema * (1 + readyDistPct/100) equals the
//        04:29 ET close exactly (the 9 EMA NEAR event, a different line from
//        READY at flag open)
//
// The two "ties:" parameter sets run on their fixture only; every fixture runs
// the seven ordinary ones.
//
// 9 EMA SLAM (2026-10-07). Every case and replay also records the latest SLAM
// of its last ET day (slamTs / slamClose / slamPct: the engine's evidence
// lastSlam*, Go's Result.LastSlam*, what the market scan's e9SlamAge /
// e9SlamPct read), and the slam events sit in the event lists like any other
// kind. Under the defaults only a handful of fixtures slam (SPAI 2026-10-07
// 08:15 is the reference), so the seventh ordinary set, `slam loose`, lowers
// every SLAM threshold until about half the corpus fires one - in both engines,
// through every rule's arithmetic, replays included. A SLAM changes no state
// or level, so every other number in the file is what it was before the rule
// existed; `slam: false` is pinned by the unit tests of each engine (the same
// run minus its slam events), not here.
//
// FLAG EXPIRY (maxFlagBars, default 30). Only one real fixture (PTC, captured at
// 13:25 ET: one flag held from 06:34 to 13:24) ever outlives 30 bars, and its
// replays are cut at its events, all before the expiry - so under the defaults
// the stale path would be pinned by that single full case. The sixth ordinary
// set, `maxFlagBars=6`, shrinks the limit until flags on a dozen of the other
// fixtures expire too, which exercises the expiry, the refusal to open a flag
// under an expired top and the READY-bar rule in BOTH engines across the whole
// corpus, replays included. (0 = no expiry is pinned by the unit tests of each
// engine, not here.)
//
// Node-only: reads/writes with node:fs, loaded dynamically with a variable
// specifier so tsc needs no @types/node (the web package does not depend on it).
import { describe, expect, it } from 'vitest';
import { DEFAULT_FLAG_PARAMS, evaluateFlag, type FlagParams, type FlagResult } from './emaFlag';
import { toCandle } from '../chartAgg';
import type { Bar } from '../types';
import type { Candle } from '../vendor/blackthorn-charts';

declare const process: { env: Record<string, string | undefined> };

interface NodeFs {
  readFileSync(path: URL, encoding: 'utf8'): string;
  writeFileSync(path: URL, data: string): void;
  readdirSync(path: URL): string[];
  existsSync(path: URL): boolean;
  mkdirSync(path: URL, opts: { recursive: boolean }): void;
}

const fsSpec = 'node:fs';
const fs = (await import(/* @vite-ignore */ fsSpec)) as NodeFs;

const FIXTURES_DIR = new URL('./fixtures/', import.meta.url);
const GOLDEN_DIR = new URL('./fixtures/golden/', import.meta.url);
const GOLDEN_FILE = new URL('./fixtures/golden/emaflag-golden.json', import.meta.url);
const GOLDEN_REL = 'web/src/signals/fixtures/golden/emaflag-golden.json';

const TF_MS = 60_000;

// The adjacent doubles, to build and to check an exact tie (a bit-pattern step).
function stepUlp(x: number, by: bigint): number {
  const f = new Float64Array([x]);
  const u = new BigInt64Array(f.buffer);
  u[0] = u[0]! + by;
  return f[0]!;
}
const nextUp = (x: number): number => stepUlp(x, 1n);
const nextDown = (x: number): number => stepUlp(x, -1n);

// The tie parameter sets (see the header). TIE_READY_DIST_PCT is the smallest
// band for which ema * (1 + band/100) === the close of the 04:29 ET bar of
// zz-tie-ready-extend (the penultimate one), found by searching the doubles
// around the distance; the guard in 'exact ties' re-checks it against the
// engine's own EMA.
const TIE_EMA1 = 'ties: emaPeriod=1, zero bands';
const TIE_READY = 'ties: readyDistPct on the 04:29 ET close';
const TIE_READY_DIST_PCT = 3.255957527801246;

const PARAM_SETS: { name: string; override: Partial<FlagParams>; only?: string }[] = [
  { name: 'defaults', override: {} },
  { name: 'entryNeedsReady=false', override: { entryNeedsReady: false } },
  { name: 'stopByClose=false', override: { stopByClose: false } },
  { name: 'minFlatBars=4', override: { minFlatBars: 4 } },
  { name: 'readyDistPct=6,maxAdds=0', override: { readyDistPct: 6, maxAdds: 0 } },
  { name: 'maxFlagBars=6', override: { maxFlagBars: 6 } },
  { name: 'slam loose', override: { slamBelowPct: 1.5, slamOpenTolPct: 1, slamBodyAtr: 0.5, slamMinBodyPct: 1, slamCloseInRange: 0.5, slamVolX: 1 } },
  { name: TIE_EMA1, override: { emaPeriod: 1, minExtPct: 0, emaBreakPct: 0, readyDistPct: 0 }, only: 'zz-tie-ema-period-1' },
  { name: TIE_READY, override: { readyDistPct: TIE_READY_DIST_PCT }, only: 'zz-tie-ready-extend' },
];

type ClockName = 'closed' | 'forming';
const CLOCKS: { name: ClockName; offsetMs: number }[] = [
  { name: 'closed', offsetMs: 10 * 60_000 }, // every bar closed
  { name: 'forming', offsetMs: 30_000 }, // the last bar is still printing
];

// A replay cut sits at each event's bar and this many bars after it.
const REPLAY_AFTER = [0, 2];

interface GoldenEvent {
  ts: number;
  kind: string;
  label: string;
  price: number;
}

interface Levels {
  state: string;
  ema: number | null;
  trigger: number | null;
  stop: number | null;
  flagHigh: number | null;
  flagLow: number | null;
  entryPrice: number | null;
  flagBars: number | null;
  emaDistPct: number | null;
  // the latest 9 EMA SLAM of the last ET day (evidence.lastSlam*), null without one
  slamTs: number | null;
  slamClose: number | null;
  slamPct: number | null;
  // the latest HOD FLAG of the last ET day (evidence.lastHodFlagTs): the FLAT bar of a flag whose run-up made a new high of the day
  hodFlagTs: number | null;
}

interface GoldenCase extends Levels {
  fixture: string;
  paramSet: string;
  params: FlagParams;
  clock: ClockName;
  nowMs: number;
  bars: number;
  events: GoldenEvent[];
}

interface GoldenReplay extends Levels {
  fixture: string;
  paramSet: string;
  clock: ClockName;
  cut: number; // the fixture truncated to its first `cut` bars
  nowMs: number;
  nEvents: number;
  lastEvent: GoldenEvent | null;
}

interface GoldenFile {
  note: string;
  tfMs: number;
  paramSets: Record<string, FlagParams>;
  cases: GoldenCase[];
  replays: GoldenReplay[];
}

const caseId = (c: { fixture: string; paramSet: string; clock: ClockName }): string => `${c.fixture} | ${c.paramSet} | ${c.clock}`;
const replayId = (c: GoldenReplay): string => `${caseId(c)} | cut ${c.cut}`;

function fixtureNames(): string[] {
  return fs
    .readdirSync(FIXTURES_DIR)
    .filter((f) => f.endsWith('.json'))
    .sort();
}

function candlesOf(fixture: string): Candle[] {
  const fx = JSON.parse(fs.readFileSync(new URL(`${fixture}.json`, FIXTURES_DIR), 'utf8')) as { bars: Bar[] };
  return fx.bars.map(toCandle);
}

function levelsOf(r: FlagResult): Levels {
  return {
    state: r.state,
    ema: r.levels.ema,
    trigger: r.levels.trigger,
    stop: r.levels.stop,
    flagHigh: r.levels.flagHigh,
    flagLow: r.levels.flagLow,
    entryPrice: r.levels.entryPrice,
    flagBars: r.evidence.flatBars,
    emaDistPct: r.evidence.emaDistPct,
    slamTs: r.evidence.lastSlamTs,
    slamClose: r.evidence.lastSlamClose,
    slamPct: r.evidence.lastSlamPct,
    hodFlagTs: r.evidence.lastHodFlagTs,
  };
}

const eventsOf = (r: FlagResult): GoldenEvent[] => r.events.map((e) => ({ ts: e.ts, kind: e.kind, label: e.label, price: e.price }));

function build(): { paramSets: Record<string, FlagParams>; cases: GoldenCase[]; replays: GoldenReplay[] } {
  const paramSets: Record<string, FlagParams> = {};
  for (const ps of PARAM_SETS) paramSets[ps.name] = { ...DEFAULT_FLAG_PARAMS, ...ps.override };
  const cases: GoldenCase[] = [];
  const replays: GoldenReplay[] = [];
  for (const file of fixtureNames()) {
    const fixture = file.replace(/\.json$/, '');
    const candles = candlesOf(fixture);
    const idxOfTs = new Map(candles.map((c, i) => [c.ts, i]));
    for (const ps of PARAM_SETS) {
      if (ps.only !== undefined && ps.only !== fixture) continue;
      let fullEvents: GoldenEvent[] = [];
      for (const clock of CLOCKS) {
        const nowMs = candles[candles.length - 1]!.ts + clock.offsetMs;
        const r = evaluateFlag({ candles, tfMs: TF_MS, nowMs, params: ps.override });
        const events = eventsOf(r);
        if (clock.name === 'closed') fullEvents = events;
        cases.push({ fixture, paramSet: ps.name, params: paramSets[ps.name]!, clock: clock.name, nowMs, bars: candles.length, events, ...levelsOf(r) });
      }
      // Replays: cut at each event's bar and REPLAY_AFTER bars later.
      const cuts = new Set<number>();
      for (const e of fullEvents) {
        const j = idxOfTs.get(e.ts);
        if (j === undefined) continue;
        for (const d of REPLAY_AFTER) if (j + d < candles.length) cuts.add(j + d + 1);
      }
      for (const cut of [...cuts].sort((a, b) => a - b)) {
        const slice = candles.slice(0, cut);
        for (const clock of CLOCKS) {
          const nowMs = slice[slice.length - 1]!.ts + clock.offsetMs;
          const r = evaluateFlag({ candles: slice, tfMs: TF_MS, nowMs, params: ps.override });
          const events = eventsOf(r);
          replays.push({ fixture, paramSet: ps.name, clock: clock.name, cut, nowMs, nEvents: events.length, lastEvent: events.length ? events[events.length - 1]! : null, ...levelsOf(r) });
        }
      }
    }
  }
  return { paramSets, cases, replays };
}

// One case per line: a regenerate shows up as a readable line diff.
function serialise(b: ReturnType<typeof build>): string {
  const note =
    'GENERATED by web/src/signals/emaFlag.golden.test.ts (UPDATE_GOLDEN=1) from emaFlag.ts evaluateFlag over src/signals/fixtures/*.json. ' +
    'Guarded by that test (TS) and internal/emaflag/golden_test.go (Go). Do not edit by hand.';
  const lines = (xs: unknown[]): string => `[\n${xs.map((x) => JSON.stringify(x)).join(',\n')}\n]`;
  return `{"note":${JSON.stringify(note)},"tfMs":${TF_MS},"paramSets":${JSON.stringify(b.paramSets)},"cases":${lines(b.cases)},"replays":${lines(b.replays)}}\n`;
}

function close(actual: number | null, expected: number | null, what: string): void {
  if (expected === null) {
    expect(actual, what).toBeNull();
  } else {
    expect(actual, what).not.toBeNull();
    expect(actual as number, what).toBeCloseTo(expected, 9);
  }
}

// The levels compared within a tolerance (close()); the replay fast path reads the same list.
const CLOSE_LEVELS = ['ema', 'trigger', 'stop', 'flagHigh', 'flagLow', 'entryPrice', 'emaDistPct', 'slamClose', 'slamPct'] as const;

function expectLevels(actual: Levels, golden: Levels, id: string): void {
  expect(actual.state, `${id} state`).toBe(golden.state);
  for (const k of CLOSE_LEVELS) close(actual[k], golden[k], `${id} ${k}`);
  expect(actual.flagBars, `${id} flagBars`).toBe(golden.flagBars);
  expect(actual.slamTs, `${id} slamTs`).toBe(golden.slamTs);
}

function expectEvent(a: GoldenEvent, g: GoldenEvent, id: string): void {
  expect(`${a.ts} ${a.kind} ${a.label}`, id).toBe(`${g.ts} ${g.kind} ${g.label}`);
  close(a.price, g.price, `${id} price`);
}

function expectSameCase(actual: GoldenCase, golden: GoldenCase): void {
  const id = caseId(golden);
  expect(actual.params, `${id} params`).toEqual(golden.params);
  expect(actual.nowMs, `${id} nowMs`).toBe(golden.nowMs);
  expect(actual.bars, `${id} bar count`).toBe(golden.bars);
  expect(actual.events.length, `${id} event count`).toBe(golden.events.length);
  golden.events.forEach((g, i) => expectEvent(actual.events[i]!, g, `${id} event ${i}`));
  expectLevels(actual, golden, id);
}

function expectSameReplay(actual: GoldenReplay, golden: GoldenReplay): void {
  const id = replayId(golden);
  expect(actual.nowMs, `${id} nowMs`).toBe(golden.nowMs);
  expect(actual.nEvents, `${id} event count`).toBe(golden.nEvents);
  if (golden.lastEvent === null) expect(actual.lastEvent, `${id} last event`).toBeNull();
  else expectEvent(actual.lastEvent!, golden.lastEvent, `${id} last event`);
  expectLevels(actual, golden, id);
}

// The replay FAST PATH. The replays are one `it`, and expectSameReplay spends ~18 expect() calls (each with its
// message string built up front) on every one of ~4900 of them: ~90 000 assertions that took 1.2-3.5 s alone
// and ran past vitest's 5 s per-test limit when the full suite shared a loaded machine (a synchronous test over
// its limit fails as "timed out" once it returns). A replay IDENTICAL to its golden one passes expectSameReplay
// by construction - every toBe field Object.is-equal, every close() field both null or the SAME finite number
// (distance 0, under any tolerance) - so it skips the expect() calls. Anything else - a missing golden replay,
// a one-ulp difference, a NaN, a missing field - still goes through expect() and expectSameReplay, tolerance
// and messages unchanged. The golden file is the JSON of this code's own output (JSON round-trips a double
// exactly), so on a green run every replay takes the fast path; the guard test below fails if a recorded field
// is ever left out of it.
const sameNumber = (a: number | null, g: number | null): boolean => (g === null ? a === null : Number.isFinite(g) && a === g);

function identicalReplay(a: GoldenReplay, g: GoldenReplay): boolean {
  if (!Object.is(a.nowMs, g.nowMs) || !Object.is(a.nEvents, g.nEvents) || !Object.is(a.state, g.state) || !Object.is(a.flagBars, g.flagBars) || !Object.is(a.slamTs, g.slamTs) || !Object.is(a.hodFlagTs, g.hodFlagTs)) return false;
  for (const k of CLOSE_LEVELS) if (!sameNumber(a[k], g[k])) return false;
  const ae = a.lastEvent;
  const ge = g.lastEvent;
  if (ae === null || ge === null) return ae === null && ge === null;
  return Object.is(ae.ts, ge.ts) && Object.is(ae.kind, ge.kind) && Object.is(ae.label, ge.label) && sameNumber(ae.price, ge.price);
}

const current = build();

describe('emaFlag golden vectors', () => {
  if (process.env.UPDATE_GOLDEN === '1') {
    it('regenerates the golden file', () => {
      fs.mkdirSync(GOLDEN_DIR, { recursive: true });
      fs.writeFileSync(GOLDEN_FILE, serialise(current));
      console.log(`UPDATE_GOLDEN=1: wrote ${current.cases.length} cases + ${current.replays.length} replays to ${GOLDEN_REL}`);
      expect(current.cases.length).toBeGreaterThan(0);
    });
    return;
  }

  const regenerate = `regenerate with: cd web && UPDATE_GOLDEN=1 npx vitest run src/signals/emaFlag.golden.test.ts (then port the change to internal/emaflag)`;
  const golden: GoldenFile | null = fs.existsSync(GOLDEN_FILE) ? (JSON.parse(fs.readFileSync(GOLDEN_FILE, 'utf8')) as GoldenFile) : null;

  it(`has the committed golden file (${GOLDEN_REL})`, () => {
    expect(golden, `${GOLDEN_REL} is missing - ${regenerate}`).not.toBeNull();
  });

  it('covers exactly the fixtures x param sets x clocks (and replay cuts) of today', () => {
    if (!golden) return;
    expect(golden.cases.map(caseId), `the case list differs (a fixture was added/removed or a param set changed) - ${regenerate}`).toEqual(current.cases.map(caseId));
    expect(golden.replays.map(replayId), `the replay list differs - ${regenerate}`).toEqual(current.replays.map(replayId));
  });

  // The vacuity guard: parity over cases that never leave 'idle' proves
  // nothing. Between them the cases and replays must reach every state and emit
  // every event kind the Go port has to reproduce.
  it('exercises every state and every event kind', () => {
    const states = new Set([...current.cases, ...current.replays].map((c) => c.state));
    const kinds = new Set(current.cases.flatMap((c) => c.events.map((e) => e.kind)));
    for (const s of ['idle', 'impulse', 'flat', 'ready', 'long']) expect(states.has(s), `no case or replay ends in state ${s}`).toBe(true);
    for (const k of ['impulse', 'flat', 'emaNear', 'entry', 'add', 'earlyBreak', 'failed', 'exit', 'slam']) expect(kinds.has(k), `no case emits a '${k}' event`).toBe(true);
    // ... and the scan's numbers must be present in a live state, not just null.
    const withStop = current.replays.filter((r) => r.state === 'ready' && r.stop !== null && r.trigger !== null && r.flagBars !== null);
    expect(withStop.length, 'no READY replay carries trigger/stop/flagBars').toBeGreaterThan(0);
    expect(current.replays.some((r) => r.state === 'long' && r.entryPrice !== null && r.stop !== null), 'no LONG replay carries entryPrice/stop').toBe(true);
  });

  // The SLAM must be in the file, not just in the code: SPAI's reference slam under the defaults, the scan's
  // slam fields filled in a replay of it, and the loose set firing across the corpus (a no-op rule would leave both
  // engines agreeing on nothing).
  it('exercises the 9 EMA SLAM (SPAI 08:15 under the defaults, the loose set across the corpus)', () => {
    for (const clock of ['closed', 'forming'] as const) {
      const spai = current.cases.find((x) => x.fixture === 'spai-2026-10-07' && x.paramSet === 'defaults' && x.clock === clock)!;
      const slams = spai.events.filter((e) => e.kind === 'slam');
      expect(slams.map((e) => `${e.ts} ${e.label} ${e.price}`), `SPAI ${clock}`).toEqual([`${spai.slamTs} SLAM (9 EMA) 4.566`]);
      expect(spai.slamClose, `SPAI ${clock}`).toBe(4.566);
      expect(spai.slamPct!, `SPAI ${clock}`).toBeLessThan(-3);
    }
    expect(current.replays.some((r) => r.fixture === 'spai-2026-10-07' && r.paramSet === 'defaults' && r.lastEvent?.kind === 'slam' && r.slamTs === r.lastEvent.ts)).toBe(true);
    const firing = new Set(current.cases.filter((c) => c.paramSet === 'slam loose' && c.events.some((e) => e.kind === 'slam')).map((c) => c.fixture));
    expect(firing.size, 'the slam loose set no longer fires on enough fixtures').toBeGreaterThanOrEqual(8);
    // a SLAM changes no state or level: the loose set's cases differ from the defaults only in slam events / fields
    for (const c of current.cases.filter((x) => x.paramSet === 'slam loose')) {
      const d = current.cases.find((x) => x.fixture === c.fixture && x.paramSet === 'defaults' && x.clock === c.clock)!;
      const strip = (x: GoldenCase) => ({ ...x, paramSet: '', params: null, slamTs: null, slamClose: null, slamPct: null, events: x.events.filter((e) => e.kind !== 'slam') });
      expect(strip(c), `${caseId(c)} vs defaults`).toEqual(strip(d));
    }
  });

  // The expiry must actually be in the file, not just in the code: PTC under the defaults ends IMPULSE (before the
  // expiry it ended READY on a 373-bar flag), and the tight-limit set changes the call on a dozen fixtures. If a
  // later edit made maxFlagBars a no-op, both Go and TS would still agree - on nothing.
  it('exercises the flag expiry (PTC under the defaults, the maxFlagBars=6 set across the corpus)', () => {
    const find = (fixture: string, paramSet: string, clock: ClockName): GoldenCase => current.cases.find((x) => x.fixture === fixture && x.paramSet === paramSet && x.clock === clock)!;
    for (const clock of ['closed', 'forming'] as const) {
      const ptc = find('ptc-2026-10-05-1325', 'defaults', clock);
      expect(ptc.state, `PTC ${clock}: a flag older than 30 bars must have expired`).toBe('impulse');
      expect(ptc.flagBars, `PTC ${clock}`).toBeNull();
    }
    let differing = 0;
    for (const c of current.cases.filter((x) => x.paramSet === 'maxFlagBars=6' && x.clock === 'closed')) {
      const d = find(c.fixture, 'defaults', 'closed');
      if (d.state !== c.state || JSON.stringify(d.events) !== JSON.stringify(c.events)) differing++;
    }
    expect(differing, 'the maxFlagBars=6 set no longer changes enough fixtures to exercise the expiry').toBeGreaterThanOrEqual(8);
  });

  // The tie fixtures sit exactly on a rule boundary, and say so here: if a rule
  // or a default changes, the boundary moves, the fixture quietly becomes an
  // ordinary case and the golden file would still match - these fail instead
  // (rebuild the fixture from its `source` note, then regenerate).
  describe('exact ties', () => {
    const closedAt = (cs: Candle[]): number => cs[cs.length - 1]!.ts + 10 * 60_000;
    const run = (cs: Candle[], params: Partial<FlagParams> = {}): FlagResult => evaluateFlag({ candles: cs, tfMs: TF_MS, nowMs: closedAt(cs), params });
    const tieCase = (fixture: string, paramSet: string, clock: ClockName): GoldenCase => {
      const c = current.cases.find((x) => x.fixture === fixture && x.paramSet === paramSet && x.clock === clock);
      if (!c) throw new Error(`no case ${fixture} | ${paramSet} | ${clock}`);
      return c;
    };
    const eventAt = (c: GoldenCase, kind: string, ts: number): GoldenEvent | undefined => c.events.find((e) => e.kind === kind && e.ts === ts);
    const stale = (what: string): string => `${what} - a rule or default moved the boundary: rebuild the fixture (see its "source") and regenerate`;

    it('a HIGH exactly on the trigger is not a break; one ulp above it is', () => {
      for (const [fixture, ulp] of [['zz-tie-trigger-equal', false], ['zz-tie-trigger-ulp', true]] as const) {
        const cs = candlesOf(fixture);
        const tieBar = cs[cs.length - 1]!;
        const armed = run(cs.slice(0, -1));
        expect(armed.state, stale(`${fixture}: the prefix is no longer READY`)).toBe('ready');
        const trigger = armed.levels.trigger!;
        expect(tieBar.high, stale(`${fixture}: the last bar's high is not the trigger${ulp ? ' + 1 ulp' : ''}`)).toBe(ulp ? nextUp(trigger) : trigger);
        for (const clock of ['forming', 'closed'] as const) {
          const c = tieCase(fixture, 'defaults', clock);
          const entry = eventAt(c, 'entry', tieBar.ts);
          if (ulp) {
            expect(entry, `${fixture} ${clock}: one ulp over the trigger must enter`).toBeDefined();
            expect(entry!.price, `${fixture} ${clock} entry price`).toBe(trigger);
            expect(c.state, `${fixture} ${clock} state`).toBe('long');
          } else {
            expect(entry, `${fixture} ${clock}: a high EQUAL to the trigger is not a break`).toBeUndefined();
            if (clock === 'forming') expect(c.state, `${fixture} forming: the flag is still armed`).toBe('ready');
          }
        }
      }
    });

    it('a bar whose LOW and CLOSE are exactly the stop does not exit by close; the low <= stop rule exits', () => {
      for (const [fixture, ulp] of [['zz-tie-stop-equal', false], ['zz-tie-stop-ulp', true]] as const) {
        const cs = candlesOf(fixture);
        const tieBar = cs[cs.length - 2]!; // one more bar follows, so the tie bar is CLOSED on both clocks
        const inPos = run(cs.slice(0, -2));
        expect(inPos.state, stale(`${fixture}: the prefix is no longer LONG`)).toBe('long');
        const stop = inPos.levels.stop!;
        const edge = ulp ? nextDown(stop) : stop;
        expect(tieBar.close, stale(`${fixture}: the tie bar's close is not the stop${ulp ? ' - 1 ulp' : ''}`)).toBe(edge);
        expect(tieBar.low, stale(`${fixture}: the tie bar's low is not the stop${ulp ? ' - 1 ulp' : ''}`)).toBe(edge);
        for (const clock of ['forming', 'closed'] as const) {
          const byClose = eventAt(tieCase(fixture, 'defaults', clock), 'exit', tieBar.ts);
          const byLow = eventAt(tieCase(fixture, 'stopByClose=false', clock), 'exit', tieBar.ts);
          if (ulp) expect(byClose?.price, `${fixture} ${clock}: a close one ulp under the stop exits at the close`).toBe(edge);
          else expect(byClose, `${fixture} ${clock}: a close EQUAL to the stop does not exit`).toBeUndefined();
          expect(byLow?.price, `${fixture} ${clock} stopByClose=false: low <= stop exits at the stop, ties included`).toBe(stop);
        }
      }
    });

    it('emaPeriod 1: the EMA is the close, so READY, the floor and the extension are all ties - and the setup still runs', () => {
      const cs = candlesOf('zz-tie-ema-period-1');
      const r = evaluateFlag({ candles: cs, tfMs: TF_MS, nowMs: closedAt(cs), params: PARAM_SETS.find((p) => p.name === TIE_EMA1)!.override });
      expect(r.levels.ema, stale('the EMA of period 1 is not the last close')).toBe(cs[cs.length - 1]!.close);
      const c = tieCase('zz-tie-ema-period-1', TIE_EMA1, 'closed');
      const kinds = new Set(c.events.map((e) => e.kind));
      // Every flag opens READY (a close can only be EQUAL to its own EMA), so there is
      // never a 9 EMA NEAR event, yet entries exist: they need READY, which a strict
      // < would withhold, as would a strict > on the floor or the impulse test.
      for (const k of ['impulse', 'flat', 'entry', 'exit']) expect(kinds.has(k), `no '${k}' event under ${TIE_EMA1}`).toBe(true);
      expect(kinds.has('emaNear'), 'a flag that opens READY has no 9 EMA NEAR event').toBe(false);
      expect(current.replays.some((x) => x.fixture === 'zz-tie-ema-period-1' && x.paramSet === TIE_EMA1 && x.state === 'ready'), 'no replay is READY').toBe(true);
    });

    it('9 EMA NEAR fires on a close exactly at ema * (1 + readyDistPct/100), and not one ulp of band under it', () => {
      const cs = candlesOf('zz-tie-ready-extend');
      const tieIdx = cs.length - 2; // one more bar follows, so the tie bar is CLOSED on both clocks
      const tieBar = cs[tieIdx]!;
      const atTie = run(cs.slice(0, tieIdx + 1), { readyDistPct: TIE_READY_DIST_PCT });
      expect(tieBar.close, stale('the 04:29 ET bar is not exactly on the READY band')).toBe(atTie.levels.ema! * (1 + TIE_READY_DIST_PCT / 100));
      for (const clock of ['forming', 'closed'] as const) {
        expect(eventAt(tieCase('zz-tie-ready-extend', TIE_READY, clock), 'emaNear', tieBar.ts), `${clock}: 9 EMA NEAR on the tie bar`).toBeDefined();
      }
      const under = run(cs, { readyDistPct: nextDown(TIE_READY_DIST_PCT) });
      expect(under.events.find((e) => e.kind === 'emaNear' && e.ts === tieBar.ts), 'a band one ulp under must not fire on the tie bar').toBeUndefined();
      expect(tieCase('zz-tie-ready-extend', TIE_READY, 'closed').state, 'the flag is READY after the tie bar').toBe('ready');
    });
  });

  if (!golden) return;
  const caseById = new Map(golden.cases.map((c) => [caseId(c), c]));
  for (const c of current.cases) {
    it(caseId(c), () => {
      const g = caseById.get(caseId(c));
      expect(g, `no golden case for ${caseId(c)} - ${regenerate}`).toBeDefined();
      expectSameCase(c, g!);
    });
  }
  const replayById = new Map(golden.replays.map((r) => [replayId(r), r]));
  it(`replays: ${current.replays.length} truncated runs match`, () => {
    for (const r of current.replays) {
      const g = replayById.get(replayId(r));
      if (g !== undefined && identicalReplay(r, g)) continue; // passes expectSameReplay by construction (see identicalReplay)
      expect(g, `no golden replay for ${replayId(r)} - ${regenerate}`).toBeDefined();
      expectSameReplay(r, g!);
    }
  });

  // The fast path must never be the weaker check: a change to ANY recorded field of a replay or of its last event
  // (the id fields aside: a different id is a different replay), on either side, must send it down the expect()
  // path. A field added to GoldenReplay / Levels / GoldenEvent and left out of identicalReplay fails here.
  it('the replay fast path notices a change to any recorded field', () => {
    const ID_KEYS = new Set(['fixture', 'paramSet', 'clock', 'cut']);
    const nudge = (v: unknown): unknown => (v === null ? 0 : typeof v === 'number' ? nextUp(v) : typeof v === 'string' ? `${v}?` : null);
    const copy = (r: GoldenReplay): GoldenReplay => ({ ...r, lastEvent: r.lastEvent && { ...r.lastEvent } });
    const picks = (['idle', 'impulse', 'flat', 'ready', 'long'] as const).map((s) => current.replays.find((r) => r.state === s && r.lastEvent !== null));
    for (const r of picks) {
      expect(r, 'no replay in one of the five states with a last event').toBeDefined();
      const id = replayId(r!);
      expect(identicalReplay(copy(r!), r!), `${id}: an unchanged copy is identical`).toBe(true);
      expectSameReplay(copy(r!), r!); // ... and passes the full check, as identity promises
      const changed: [string, GoldenReplay][] = [['lastEvent = null', { ...r!, lastEvent: null }]];
      for (const k of Object.keys(r!) as (keyof GoldenReplay)[]) {
        if (!ID_KEYS.has(k) && k !== 'lastEvent') changed.push([k, { ...r!, [k]: nudge(r![k]) } as GoldenReplay]);
      }
      for (const k of Object.keys(r!.lastEvent!) as (keyof GoldenEvent)[]) {
        changed.push([`lastEvent.${k}`, { ...r!, lastEvent: { ...r!.lastEvent!, [k]: nudge(r!.lastEvent![k]) } as GoldenEvent }]);
      }
      expect(changed.length, `${id}: fields probed`).toBeGreaterThanOrEqual(1 + 14 + 4);
      for (const [what, x] of changed) {
        expect(identicalReplay(x, r!), `${id}: ${what} changed but the fast path calls it identical`).toBe(false);
        expect(identicalReplay(r!, x), `${id}: golden ${what} changed but the fast path calls it identical`).toBe(false);
      }
    }
  });
});
