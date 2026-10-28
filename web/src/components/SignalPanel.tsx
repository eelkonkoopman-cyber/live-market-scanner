import { Fragment, useEffect, useMemo, useState, useSyncExternalStore } from 'react';
import { DEFAULT_PARAMS, fmtEt, fmtPx, type SignalEvent, type SignalParams, type SignalResult } from '../signals/shortFade';
import { DEFAULT_FLAG_PARAMS, type FlagParams } from '../signals/emaFlag';
import { DEFAULT_DILDO_PARAMS, type DildoParams } from '../signals/dildoPb';
import { DEFAULT_CLIMAX_PARAMS, type ClimaxParams } from '../signals/climax';
import { getSignalRuleParams, setClimaxParams, setDildoParams, subscribeSignalRuleParams } from '../signalLayout';
import { eventTag, sideRead, viewForSide, type CellRead, type SignalSide, type ViewEvent } from '../signals/view';
import { BlacklistPanel } from './BlacklistPanel';

// SignalPanel is the Signal window's aside: the state machine's current
// stage in words (headline + detail, straight from the engine — no text is
// composed here), the plan once armed/open, the evidence behind the call,
// a running event log, and a tuning drawer for the engine's own params. Pure
// props in, callback out — same shape as the engine it renders (SignalPane
// owns the store/localStorage side of params; this component just displays
// and edits the value it's handed).
//
// Two engines, two bodies (iteration 6): the panel reads the ACTIVE cell's
// CellRead (signals/view.ts), and that cell runs either the 9 EMA flag engine
// or the pullback engine. SignalPanel only dispatches:
//   - FlagPanel — the flag cell: state card, plan grid (Trigger / Stop / Risk %
//     / EMA distance / Flag range, or Entry / Stop / Risk % / Open P&L once
//     long), evidence chips, the events log, and the flag engine's Tuning
//     (its own params, its own localStorage key — SignalPane). Everything it
//     shows is the engine-neutral SignalView, so it composes no text either.
//   - PullbackPanel — the pullback cell, unchanged from addendum 4 (below).
//
// Long side (addendum 4), PullbackPanel: the panel reads the ACTIVE cell, and
// that cell has a side selector (short | long | both — signals/view.ts). It
// receives the FULL, unfiltered result plus the side, and applies the side
// itself:
//   - 'both'  — the engine's combined headline/detail on top (omitted when
//     it would only repeat one side's card verbatim — see `showTop`), then a
//     Short card and a Long card, each with its own read and its own plan grid;
//   - a single side — that side's own read on top (never the combined one,
//     which talks about the other side too) and that side's plan grid; a
//     second card would only repeat the top one, so there is none, and the
//     excluded side's card is simply absent;
//   - the events log lists only what the side shows (viewForSide), each row
//     tagged S / L.
//
// Above all of that sits today's symbol blacklist (BlacklistPanel). It belongs
// to the window, not to the active cell; the wrapper below stacks the two, and
// the cell panel is the part that scrolls.

export interface SignalPanelProps {
  read: CellRead | null;
  sym: string | null;
  params: SignalParams; // the pullback engine's tuning
  onParams: (p: SignalParams) => void;
  flagParams: FlagParams; // the flag engine's tuning
  onFlagParams: (p: FlagParams) => void;
}

// ─── Tuning drawer (shared by both engines) ──────────────────────────────
//
// Fields are declared per engine below. Booleans are found by TYPE (any param
// whose value is boolean), numbers are everything else, so a param added to an
// engine later is never silently missing from its drawer: it lands in the
// fallback lists (raw key as the label, step 1) until someone gives it a proper
// entry here.
type BoolKeys<P> = { [K in keyof P & string]: P[K] extends boolean ? K : never }[keyof P & string];
type NumKeys<P> = Exclude<keyof P & string, BoolKeys<P>>;

// `time`: the value is an ET minute-of-day, shown and typed as HH:MM (the climax mute window).
type NumericField<P> = { key: NumKeys<P>; label: string; step: number; min: number; max?: number; int?: boolean; time?: boolean; title?: string };
type BoolField<P> = { key: BoolKeys<P>; label: string; title?: string };
// A string param with a fixed set of values (the dildo's floor).
type ChoiceField<P> = { key: keyof P & string; label: string; options: { value: string; label: string }[]; title?: string };

const hhmm = (min: number): string => `${String(Math.floor(min / 60)).padStart(2, '0')}:${String(min % 60).padStart(2, '0')}`;
function parseHhmm(raw: string): number | null {
  const m = /^\s*(\d{1,2}):(\d{2})\s*$/.exec(raw);
  if (!m) return null;
  const h = Number(m[1]);
  const mm = Number(m[2]);
  return h < 24 && mm < 60 ? h * 60 + mm : null;
}

// Appends every param of `defaults` the named lists do not cover, shown with its
// raw key so it is at least editable.
function withFallback<P extends object>(defaults: P, numeric: NumericField<P>[], bool: BoolField<P>[]): { numeric: NumericField<P>[]; bool: BoolField<P>[] } {
  const keys = Object.keys(defaults) as (keyof P & string)[];
  const knownNum = new Set<string>(numeric.map((f) => f.key));
  const knownBool = new Set<string>(bool.map((f) => f.key));
  return {
    numeric: [
      ...numeric,
      ...keys.filter((k) => typeof defaults[k] === 'number' && !knownNum.has(k)).map((k) => ({ key: k as NumKeys<P>, label: k, step: 1, min: 0 })),
    ],
    bool: [
      ...bool,
      ...keys.filter((k) => typeof defaults[k] === 'boolean' && !knownBool.has(k)).map((k) => ({ key: k as BoolKeys<P>, label: k })),
    ],
  };
}

const PULLBACK_NUMERIC: NumericField<SignalParams>[] = [
  { key: 'minSpikePct', label: 'Min spike %', step: 1, min: 0 },
  { key: 'minVwapExtPct', label: 'Min VWAP ext % (0 = off)', step: 1, min: 0 },
  { key: 'minGapPct', label: 'Min gap %', step: 1, min: 0 },
  { key: 'runLookbackBars', label: 'Run lookback (bars)', step: 1, min: 1 },
  { key: 'swingStrength', label: 'Swing strength (bars)', step: 1, min: 1 },
  { key: 'minPullbackPct', label: 'Min pullback %', step: 1, min: 0 },
  { key: 'breakBufferPct', label: 'Break buffer %', step: 0.1, min: 0 },
  { key: 'stopBufferPct', label: 'Stop buffer %', step: 0.1, min: 0 },
  { key: 'climaxVolMult', label: 'Climax volume ×', step: 0.5, min: 0 },
  { key: 'vwapBandPct', label: 'VWAP band % (hysteresis)', step: 0.1, min: 0 },
  { key: 'maxAdds', label: 'Max adds per side (0 = none)', step: 1, min: 0, title: 'the most ADD signals per episode on each side (lower-high / VWAP-reject adds for the short, higher-low / VWAP-hold adds for the long); entries and breakdown / breakout confirmations never count' },
  { key: 'maxAddRiskPct', label: 'Max add risk %', step: 1, min: 0, title: 'an ADD (LH) / ADD (HL) is only signalled when its distance to the stop is at most this % of the add price' },
];

const PULLBACK_BOOL: BoolField<SignalParams>[] = [
  { key: 'lhEntry', label: 'Lower-high entry', title: 'short: the close that confirms a lower high is the entry' },
  { key: 'stopByClose', label: 'Stop by close', title: 'stops (both sides) trigger on a bar CLOSE through the stop; off = any intrabar touch' },
  { key: 'vwapLossEntry', label: 'VWAP-loss entry', title: 'short: a close below VWAP is an entry once a lower high exists' },
  { key: 'hlEntry', label: 'Higher-low entry', title: 'long: the close that confirms a higher low is the entry' },
  { key: 'hlAboveVwap', label: 'Higher low above VWAP', title: 'long: a higher low only counts when its low is at/above VWAP' },
];

const PULLBACK_FIELDS = withFallback(DEFAULT_PARAMS, PULLBACK_NUMERIC, PULLBACK_BOOL);

// The flag engine's fields (emaFlag.ts FlagParams). Bar counts and periods are
// whole numbers (`int`): a fractional lookback or EMA length means nothing.
const FLAG_NUMERIC: NumericField<FlagParams>[] = [
  { key: 'emaPeriod', label: 'EMA length', step: 1, min: 1, int: true, title: 'the moving average the flag waits for (the blue line on the chart)' },
  { key: 'atrPeriod', label: 'ATR length', step: 1, min: 1, int: true, title: 'Wilder ATR — evidence, and the SLAM body test' },
  { key: 'minExtPct', label: 'Min breakout % above EMA', step: 0.5, min: 0, title: 'impulse: a bar must CLOSE at least this % above the EMA …' },
  { key: 'breakoutLookback', label: 'Breakout lookback (bars)', step: 1, min: 1, int: true, title: '… on a bar whose high beats the highest high of this many prior bars of the same day' },
  { key: 'impulseBaseBars', label: 'Impulse base (bars)', step: 1, min: 1, int: true, title: "the move's base low = the lowest low of this many bars before the impulse bar" },
  { key: 'minFlatBars', label: 'Min flat bars', step: 1, min: 1, int: true, title: 'closed bars, at/after the top, that must qualify as sideways' },
  { key: 'maxFlatOfMovePct', label: 'Max flat span % of move', step: 1, min: 0, title: 'flat = the window’s closes span at most this % of the move (top − base low) …' },
  { key: 'maxFlatPct', label: 'Max flat span % of price', step: 1, min: 0, title: '… and at most this % of price (a sanity cap for enormous moves)' },
  { key: 'maxRetracePct', label: 'Max retrace %', step: 1, min: 0, title: 'the flag’s lowest low must hold above this much of the move' },
  { key: 'emaBreakPct', label: 'EMA break % (flag fails)', step: 0.1, min: 0, title: 'a CLOSE more than this % under the EMA ends the setup' },
  { key: 'readyDistPct', label: 'READY within % of EMA', step: 0.5, min: 0, title: 'READY once the flag’s latest close is within this % above the EMA' },
  { key: 'breakBufferPct', label: 'Break buffer %', step: 0.1, min: 0, title: 'trigger = flag high × (1 + this %)' },
  { key: 'stopBufferPct', label: 'Stop buffer %', step: 0.1, min: 0, title: 'stop = max(flag low, EMA) × (1 − this %); the trail uses the same buffer' },
  { key: 'maxAdds', label: 'Max adds (0 = none)', step: 1, min: 0, int: true, title: 'later flag breakouts while long are ADDs, at most this many per position' },
  // 9 EMA SLAM (emaFlag.ts): a bearish ALERT out of a flat / ready / long flag — one closed candle from above the EMA to well under it
  { key: 'slamBelowPct', label: 'SLAM: close % under EMA', step: 0.5, min: 0, title: 'SLAM: the candle CLOSES at least this % under its 9 EMA' },
  { key: 'slamOpenTolPct', label: 'SLAM: open tolerance %', step: 0.1, min: 0, title: 'SLAM: … having opened at most this % under the previous bar’s EMA (whose close held it)' },
  { key: 'slamBodyAtr', label: 'SLAM: body in ATRs (0 = off)', step: 0.25, min: 0, title: 'SLAM: the body (open − close) is at least this many ATRs of the bar before' },
  { key: 'slamMinBodyPct', label: 'SLAM: body % of open', step: 0.5, min: 0, title: 'SLAM: … and at least this % of the open' },
  { key: 'slamCloseInRange', label: 'SLAM: close in lowest … of range', step: 0.05, min: 0, max: 1, title: 'SLAM: it closes in the lowest this fraction of its high–low range (0.35 = bottom 35%)' },
  { key: 'slamVolX', label: 'SLAM: volume × (0 = off)', step: 0.1, min: 0, title: 'SLAM: volume at least this × the mean of the 10 bars before it the same day (no test before the 3rd bar)' },
];

const FLAG_BOOL: BoolField<FlagParams>[] = [
  { key: 'entryNeedsReady', label: 'Entry needs READY', title: 'a flag-high break only counts as an entry once the EMA has caught up; before that it is an EARLY BREAK (list only)' },
  { key: 'trailEma', label: 'Trail stop with the EMA', title: 'while long, each closed bar raises the stop to the EMA − buffer (never lowers it)' },
  { key: 'stopByClose', label: 'Stop by close', title: 'exit on a bar CLOSE below the stop; off = any intrabar touch (also on the forming bar)' },
  { key: 'slam', label: '9 EMA SLAM alert', title: 'a candle that slams from above the 9 EMA to well under it out of a sideways flag (or a long) — an alert only, never a trade' },
];

const FLAG_FIELDS = withFallback(DEFAULT_FLAG_PARAMS, FLAG_NUMERIC, FLAG_BOOL);

// GREEN DILDO -> 9 EMA PULLBACK (signals/dildoPb.ts) and VOLUME CLIMAX -> BOTTOM
// CONFIRMED (signals/climax.ts), 2026-10-07: their markers draw on 1-minute flag
// cells. One shared copy for the window (signalLayout.ts), edited here.
const RULE_HINT = 'Changes the chart markers only; the market scan uses the defaults';
const DILDO_HINT = 'Applies to alerts market-wide (saved on the server, shared by every window)';

const DILDO_NUMERIC: NumericField<DildoParams>[] = [
  { key: 'minBodyPct', label: 'Min body %', step: 0.5, min: 0, title: 'the green body (close - open) is at least this % of the open' },
  { key: 'minBodyX', label: 'Min body x prior bars', step: 0.5, min: 0, title: 'the body is at least this x the mean body of the prior bars' },
  { key: 'maxTopPct', label: 'Max close-off-high %', step: 1, min: 0, max: 100, title: 'it closes in the top this % of its range' },
  { key: 'minVolX', label: 'Min volume x prior bars', step: 0.5, min: 0, title: "its volume is at least this x the prior bars' mean" },
  { key: 'priorBars', label: 'Prior bars', step: 1, min: 1, int: true, title: 'the yardstick: up to this many traded bars before the candle (may reach into the previous evening)' },
  { key: 'minPriorBars', label: 'Min prior bars', step: 1, min: 0, int: true, title: 'no dildo is judged with fewer prior bars' },
  { key: 'minPrice', label: 'Min price $', step: 0.05, min: 0 },
  { key: 'minDollarVol', label: 'Min candle $ volume', step: 10_000, min: 0, title: 'close x volume of the candle' },
  { key: 'pbWindowMin', label: 'Pullback window (min)', step: 1, min: 1, int: true, title: 'the 9 EMA pullback is looked for this many minutes after the dildo' },
  { key: 'touchPct', label: 'Touch within % of EMA', step: 0.1, min: 0, title: "a bar whose low comes within this % above the previous bar's EMA touches it" },
  { key: 'holdPct', label: 'Hold: close >= EMA - %', step: 0.1, min: 0, title: 'the touch bar must close no more than this % under its EMA' },
  { key: 'emaPeriod', label: 'EMA length', step: 1, min: 1, int: true },
];
const DILDO_BOOL: BoolField<DildoParams>[] = [{ key: 'enabled', label: 'Green dildo markers' }];
const DILDO_FIELDS = withFallback(DEFAULT_DILDO_PARAMS, DILDO_NUMERIC, DILDO_BOOL);
const DILDO_CHOICES: ChoiceField<DildoParams>[] = [
  {
    key: 'floor',
    label: 'Pullback floor',
    title: 'a pullback whose low breaks this level is cancelled',
    options: [
      { value: 'mid', label: 'dildo body midpoint' },
      { value: 'open', label: 'dildo open' },
    ],
  },
];

const CLIMAX_NUMERIC: NumericField<ClimaxParams>[] = [
  { key: 'lookback', label: 'Prior bars (same day)', step: 1, min: 1, int: true, title: 'the last N traded bars of today; with fewer, no climax is judged' },
  { key: 'minDropPct', label: 'Min drop % from the high', step: 0.5, min: 0, title: "the low is at least this % under the prior bars' highest high" },
  { key: 'minVolX', label: 'Min volume x prior bars', step: 0.5, min: 0 },
  { key: 'minWickPct', label: 'Min lower wick %', step: 1, min: 0, max: 100, title: '(min(open, close) - low) as % of the range' },
  { key: 'emaPeriod', label: 'EMA length', step: 1, min: 1, int: true },
  { key: 'minPrice', label: 'Min price $', step: 0.05, min: 0 },
  { key: 'minDollarVol', label: 'Min candle $ volume', step: 10_000, min: 0 },
  { key: 'muteStartMin', label: 'Mute from (ET)', step: 1, min: 0, max: 1440, int: true, time: true, title: 'no climax on bars starting from this ET time ...' },
  { key: 'muteEndMin', label: 'Mute until (ET, excl.)', step: 1, min: 0, max: 1440, int: true, time: true, title: '... up to (not including) this one; from >= until switches the mute off' },
  { key: 'confirmBars', label: 'Confirm within (bars)', step: 1, min: 1, int: true, title: 'BOTTOM CONFIRMED needs a close above the climax midpoint within this many bars, before any bar undercuts its low' },
];
const CLIMAX_BOOL: BoolField<ClimaxParams>[] = [
  { key: 'enabled', label: 'Volume climax markers' },
  { key: 'requireMaxVol', label: 'Biggest volume of the prior bars' },
  { key: 'requireBelowEma', label: 'Close under the 9 EMA' },
  { key: 'coreHoursOnly', label: '10:00-15:59 ET only' },
];
const CLIMAX_FIELDS = withFallback(DEFAULT_CLIMAX_PARAMS, CLIMAX_NUMERIC, CLIMAX_BOOL);

// The two alert rules' drawers, bound to the window's shared copy.
function AlertRuleTuning() {
  const rp = useSyncExternalStore(subscribeSignalRuleParams, getSignalRuleParams);
  return (
    <>
      <TuningDrawer title="Green dildo" hint={DILDO_HINT} params={rp.dildo} defaults={DEFAULT_DILDO_PARAMS} fields={DILDO_FIELDS} choices={DILDO_CHOICES} onParams={setDildoParams} />
      <TuningDrawer title="Volume climax" hint={RULE_HINT} params={rp.climax} defaults={DEFAULT_CLIMAX_PARAMS} fields={CLIMAX_FIELDS} onParams={setClimaxParams} />
    </>
  );
}

function TuningDrawer<P extends object>({
  params,
  defaults,
  fields,
  onParams,
  title = 'Tuning',
  hint,
  choices = [],
}: {
  params: P;
  defaults: P;
  fields: { numeric: NumericField<P>[]; bool: BoolField<P>[] };
  onParams: (p: P) => void;
  title?: string;
  hint?: string;
  choices?: ChoiceField<P>[];
}) {
  // Per-field draft strings for the Tuning number boxes, committed on
  // blur/Enter rather than on every keystroke. A plain controlled
  // `value={params[f.key]}` input can never be cleared: `Number('')` is `0`
  // and `Number.isFinite(0)` is true, so the ordinary select-all/backspace
  // edit gesture used to commit `{ [f.key]: 0 }` straight into the engine
  // (and into localStorage) the instant the box went empty — e.g. swingStrength
  // 0 turns every post-HOD bar into a vacuous swing, runLookbackBars 0
  // silently disarms the whole setup. Holding the in-progress text here
  // (uncommitted) means React never fights the user's typing, and only a
  // parseable, in-range value ever reaches onParams (S2).
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  // Drop stale drafts when params change from outside this component (the
  // Reset button, or a future edit in another window) so a leftover draft
  // doesn't shadow the new value.
  useEffect(() => {
    setDrafts({});
  }, [params]);

  const current = params as Record<string, unknown>;

  function commitDraft(f: NumericField<P>) {
    const raw = drafts[f.key];
    if (raw === undefined) return;
    const v = f.time ? (parseHhmm(raw) ?? Number.NaN) : Number(raw);
    if (raw.trim() !== '' && Number.isFinite(v) && v >= f.min && (f.max === undefined || v <= f.max) && (!f.int || Number.isInteger(v)) && v !== current[f.key]) {
      onParams({ ...params, [f.key]: v });
    }
    setDrafts((d) => {
      const next = { ...d };
      delete next[f.key];
      return next;
    });
  }

  return (
    <details className="signal-tuning">
      <summary>{title}</summary>
      <div className="signal-tuning-body">
        {hint && (
          <div className="muted" style={{ fontSize: '0.85em' }}>
            {hint}
          </div>
        )}
        {fields.numeric.map((f) => (
          <label key={f.key} className="signal-tuning-field" title={f.title}>
            <span>{f.label}</span>
            <input
              type={f.time ? 'text' : 'number'}
              inputMode={f.time ? 'numeric' : undefined}
              placeholder={f.time ? 'HH:MM' : undefined}
              step={f.time ? undefined : f.step}
              min={f.time ? undefined : f.min}
              max={f.time ? undefined : f.max}
              value={drafts[f.key] ?? (f.time ? hhmm(Number(current[f.key])) : String(current[f.key]))}
              onChange={(e) => setDrafts((d) => ({ ...d, [f.key]: e.target.value }))}
              onBlur={() => commitDraft(f)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') (e.target as HTMLInputElement).blur();
              }}
            />
          </label>
        ))}
        {choices.map((f) => (
          <label key={f.key} className="signal-tuning-field" title={f.title}>
            <span>{f.label}</span>
            <select value={String(current[f.key])} onChange={(e) => onParams({ ...params, [f.key]: e.target.value })}>
              {f.options.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </select>
          </label>
        ))}
        {fields.bool.map((f) => (
          <label key={f.key} className="signal-tuning-field signal-tuning-checkbox" title={f.title}>
            <span>{f.label}</span>
            <input type="checkbox" checked={Boolean(current[f.key])} onChange={(e) => onParams({ ...params, [f.key]: e.target.checked })} />
          </label>
        ))}
        <button type="button" className="mini" onClick={() => onParams({ ...defaults })}>
          Reset to defaults
        </button>
      </div>
    </details>
  );
}

// ─── Shared bits ─────────────────────────────────────────────────────────

const px = (v: number | null) => (v == null ? '—' : fmtPx(v));
const pct = (v: number | null) => (v == null ? '—' : `${v.toFixed(1)}%`);
const pctPlus = (v: number | null) => (v == null ? '—' : `+${v.toFixed(1)}%`);

function ReadLines({ detail }: { detail: string[] }) {
  return (
    <>
      {detail.map((line, i) => (
        <div key={i} className="signal-detail-line">
          {line}
        </div>
      ))}
    </>
  );
}

// Events list: ts DESCENDING for display, ties broken by reverse EMISSION
// order (later-emitted first) — `events` itself stays in emission order
// (SignalResult.events' own doc comment, B7), since a `lowerHigh` carries
// its swing bar's ts but is only emitted once a later bar confirms it, so
// ts order and emission order are not the same thing. (The flag engine's events
// are emitted in ts order, so there the rule is just newest first.)
function newestFirst<E extends ViewEvent>(events: E[]): E[] {
  return events
    .map((e, i) => ({ e, i }))
    .sort((a, b) => b.e.ts - a.e.ts || b.i - a.i)
    .map((x) => x.e);
}

// One row per event. Dot colour follows the marker, which is already right for
// every engine: a buy is gain-coloured, a sell loss-coloured (pullback: entries
// /adds are 'sell' for the short and 'buy' for the long, exits/targets the
// mirror; flag: entries/adds 'buy', the exit 'sell'), a list-only event is
// muted. `tagFor` is the pullback's S / L side tag (a flag log is long only, so
// it passes none).
function EventRows<E extends ViewEvent>({ events, tagFor }: { events: E[]; tagFor?: (e: E) => 'S' | 'L' | null }) {
  return (
    <>
      {events.map((e, i) => {
        const tag = tagFor ? tagFor(e) : null;
        return (
          <div key={`${e.ts}:${e.kind}:${i}`} className="signal-event-row">
            <span className={`signal-event-dot ${e.marker === 'sell' ? 'neg' : e.marker === 'buy' ? 'pos' : 'muted'}`} />
            <div className="signal-event-body">
              <div className="signal-event-head">
                {tag && (
                  <span className="signal-event-tag" data-side={e.side} title={e.side === 'long' ? 'long side' : 'short side'}>
                    {tag}
                  </span>
                )}
                {fmtEt(e.ts)} · {e.label} · {fmtPx(e.price)}
              </div>
              <div className="signal-event-note muted">{e.note}</div>
            </div>
          </div>
        );
      })}
    </>
  );
}

// ─── The 9 EMA flag panel ────────────────────────────────────────────────

function FlagPanel({ read, flagParams, onFlagParams }: { read: Extract<CellRead, { setup: 'flag' }>; flagParams: FlagParams; onFlagParams: (p: FlagParams) => void }) {
  const { view } = read;
  const displayEvents = newestFirst(view.events);
  return (
    <aside className="signal-panel">
      <div className="signal-state" data-state={view.state}>
        <div className="signal-headline">{view.headline}</div>
        <ReadLines detail={view.detail} />
      </div>

      {view.plan.length > 0 && (
        <div className="signal-plan" data-setup="flag">
          {view.plan.map((row) => (
            <Fragment key={row.k}>
              <span>{row.k}</span>
              <b>{row.v}</b>
            </Fragment>
          ))}
        </div>
      )}

      <div className="signal-evidence">
        {view.chips.map((c) => (
          <span key={c.k} className="signal-chip">
            {c.k} {c.v}
          </span>
        ))}
      </div>

      <div className="signal-events">
        {displayEvents.length === 0 && <div className="muted signal-events-empty">No events yet.</div>}
        <EventRows events={displayEvents} />
      </div>

      <TuningDrawer params={flagParams} defaults={DEFAULT_FLAG_PARAMS} fields={FLAG_FIELDS} onParams={onFlagParams} />
      <AlertRuleTuning />
    </aside>
  );
}

// ─── The pullback panel (addendum 4, unchanged) ──────────────────────────

// Which of the four entry routes opened the CURRENT short episode, in
// words, for the plan grid's Entry row. `levels.entryKind` only carries the
// 3-value 'lh'|'break'|'vwap' the engine's public SignalLevels type defines
// — the finer 'VWAP loss' vs 'VWAP rejection' split for 'vwap' lives inside
// shortFade.ts as an internal-only flag (`entryIsReject`, deliberately not
// exposed — see that file's SignalLevels.entryKind comment), so it's
// reconstructed here from the event log instead: the entry event carries
// the same ts as `levels.entryTs` (an 'ADD (LH)'/'VWAP REJECT' add never
// touches entryTs, only the original entry does), and a `vwapReject` there
// means the episode opened via the rejection ('SHORT (VWAP REJECT)') rather
// than a plain VWAP-loss close (`entryVwap`, 'SHORT (VWAP)').
function entryKindLabel(result: SignalResult): string | null {
  const { entryKind, entryTs } = result.levels;
  if (entryKind === 'lh') return 'lower high';
  if (entryKind === 'break') return 'breakdown';
  if (entryKind === 'vwap') {
    const opener = result.events.find((e) => e.ts === entryTs && (e.kind === 'entryVwap' || e.kind === 'vwapReject'));
    return opener?.kind === 'vwapReject' ? 'VWAP rejection' : 'VWAP loss';
  }
  return null;
}

// The long side's entry route in words — the long's three routes are all
// distinguishable from the public `longEntryKind`, no event lookup needed.
function longEntryKindLabel(kind: SignalResult['levels']['longEntryKind']): string | null {
  if (kind === 'hl') return 'higher low';
  if (kind === 'break') return 'breakout';
  if (kind === 'vwap') return 'VWAP hold';
  return null;
}

// The plan grid of ONE side (entry, stop, risk %, T1, T2), shown once that
// side is armed or open. Once open, the headline row is the entry that
// actually fired — not the trigger level, which only means something before an
// entry exists ('armed', reachable at all only with lhEntry / hlEntry off).
function ShortPlan({ result }: { result: SignalResult }) {
  if (result.short.state === 'none') return null;
  const { levels, evidence } = result;
  const entryWord = result.short.state === 'short' ? entryKindLabel(result) : null;
  return (
    <div className="signal-plan" data-side="short">
      {entryWord ? (
        <>
          <span>Entry ({entryWord})</span>
          <b>{px(levels.entryPrice)}</b>
        </>
      ) : (
        <>
          <span>Trigger</span>
          <b>{px(levels.trigger)}</b>
        </>
      )}
      <span>Stop</span>
      <b>{px(levels.stop)}</b>
      <span>Risk %</span>
      <b>{pct(evidence.riskPct)}</b>
      <span>T1 VWAP</span>
      <b>{px(levels.vwap)}</b>
      <span>T2 Base</span>
      <b>{px(levels.runLow)}</b>
    </div>
  );
}

function LongPlan({ result }: { result: SignalResult }) {
  if (result.long.state === 'none') return null;
  const { levels } = result;
  const open = result.long.state === 'long';
  const entryWord = open ? longEntryKindLabel(levels.longEntryKind) : null;
  // The engine reports no risk % for the long (evidence.riskPct is the
  // short's), so it is derived here from the same two prices the long's own
  // detail line quotes: (entry or trigger − stop) / that price.
  const ref = open ? levels.longEntryPrice : levels.longTrigger;
  const risk = ref != null && levels.longStop != null && ref > 0 ? ((ref - levels.longStop) / ref) * 100 : null;
  return (
    <div className="signal-plan" data-side="long">
      {entryWord ? (
        <>
          <span>Entry ({entryWord})</span>
          <b>{px(levels.longEntryPrice)}</b>
        </>
      ) : (
        <>
          <span>Trigger</span>
          <b>{px(levels.longTrigger)}</b>
        </>
      )}
      <span>Stop</span>
      <b>{px(levels.longStop)}</b>
      <span>Risk %</span>
      <b>{pct(risk)}</b>
      <span>T1 Bounce high</span>
      <b>{px(levels.bounceHigh)}</b>
      <span>T2 HOD</span>
      <b>{px(levels.hod)}</b>
    </div>
  );
}

function PullbackPanel({
  result,
  side,
  params,
  onParams,
}: {
  result: SignalResult;
  side: SignalSide;
  params: SignalParams;
  onParams: (p: SignalParams) => void;
}) {
  // The events list follows the cell's side (a short-only cell's log does not
  // list long events). Memoised on (result, side): viewForSide is cheap, but
  // the panel re-renders on every tick of the active cell.
  const view = useMemo(() => viewForSide(result, side), [result, side]);

  const { evidence, events } = result;
  const top = sideRead(result, side);
  const shortRead = sideRead(result, 'short');
  const longRead = sideRead(result, 'long');
  // In 'S+L' the combined read on top is the engine's own — and while only one
  // side is open (the common live case: "SHORT signalled …") it is that side's
  // read VERBATIM, so the top card and the Short/Long card underneath it would
  // print the same four lines twice. Drop the top card exactly when it
  // duplicates a side card; it stays whenever it adds something (the
  // 'TWO-SIDED' summary, or the shared spike-top story with the long's first
  // detail line appended). A single-side cell always keeps it — there it IS the
  // side's card.
  const sameRead = (a: { headline: string; detail: string[] }, b: { headline: string; detail: string[] }) =>
    a.headline === b.headline && a.detail.length === b.detail.length && a.detail.every((l, i) => l === b.detail[i]);
  const showTop = side !== 'both' || !(sameRead(top, shortRead) || sameRead(top, longRead));

  const displayEvents = newestFirst<SignalEvent>(view.events);

  return (
    <aside className="signal-panel">
      {showTop && (
        <div className="signal-state" data-state={top.state}>
          <div className="signal-headline">{top.headline}</div>
          <ReadLines detail={top.detail} />
        </div>
      )}

      {side === 'both' ? (
        <>
          {(['short', 'long'] as const).map((s) => {
            const read = s === 'short' ? shortRead : longRead;
            return (
              <div key={s} className="signal-side-card" data-side={s}>
                <div className="signal-side-title">
                  <span>{s === 'short' ? 'Short' : 'Long'}</span>
                  <b className="signal-side-badge" data-state={read.state}>
                    {read.state.toUpperCase()}
                  </b>
                </div>
                <div className="signal-side-headline">{read.headline}</div>
                <ReadLines detail={read.detail} />
                {s === 'short' ? <ShortPlan result={result} /> : <LongPlan result={result} />}
              </div>
            );
          })}
        </>
      ) : side === 'short' ? (
        <ShortPlan result={result} />
      ) : (
        <LongPlan result={result} />
      )}

      <div className="signal-evidence">
        <span className="signal-chip">run {pctPlus(evidence.runGainPct)}</span>
        <span className="signal-chip">gap {pctPlus(evidence.gapGainPct)}</span>
        <span className="signal-chip">VWAP ext {pct(evidence.vwapExtPct)}</span>
        <span className="signal-chip">climax vol {evidence.climaxVol ? '✓' : '–'}</span>
        <span className="signal-chip">pullback {pct(evidence.pullbackPct)}</span>
        <span className="signal-chip">retrace {pct(evidence.retracePct)}</span>
      </div>

      <div className="signal-events">
        {events.length === 0 && <div className="muted signal-events-empty">No events yet.</div>}
        {events.length > 0 && displayEvents.length === 0 && <div className="muted signal-events-empty">No events on this side yet.</div>}
        <EventRows events={displayEvents} tagFor={eventTag} />
      </div>

      <TuningDrawer params={params} defaults={DEFAULT_PARAMS} fields={PULLBACK_FIELDS} onParams={onParams} />
    </aside>
  );
}

function SignalPanelBody({ read, sym: _sym, params, onParams, flagParams, onFlagParams }: SignalPanelProps) {
  if (!read) {
    // The panel always shows the ACTIVE cell of the Signal window's grid
    // (addendum 2) rather than the pane's one symbol, so the empty state is
    // about which chart is being read, not about assigning one.
    return (
      <aside className="signal-panel">
        <div className="signal-empty muted">Click a chart to read it.</div>
      </aside>
    );
  }
  if (read.setup === 'flag') return <FlagPanel read={read} flagParams={flagParams} onFlagParams={onFlagParams} />;
  return <PullbackPanel result={read.result} side={read.side} params={params} onParams={onParams} />;
}

export function SignalPanel(props: SignalPanelProps) {
  return (
    <div className="signal-side">
      <BlacklistPanel />
      <SignalPanelBody {...props} />
    </div>
  );
}
