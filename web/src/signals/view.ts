// Per-cell "which side of the pullback to show" — pure display helpers for the
// Signal window (addendum 4, "the long side inside the Signal window").
//
// The engine (shortFade.ts) always computes BOTH sides of the pullback — the
// short (lower high) and the long (higher low) — over the same candles, because
// they share the spike/crack/bounce prefix and diverge only at the second swing
// (module comment there, the ABCD section). Each grid cell can nevertheless be
// set to show just `short`, just `long`, or `both`, and that choice is DISPLAY
// ONLY: nothing here (or anywhere) changes what the engine computes, so
// flipping a cell's selector never loses or invents a signal, it only decides
// which of the engine's outputs that cell draws, lists and sounds.
//
// Everything is a pure function of (result, side), no DOM, no store, so it can
// be unit-tested without a renderer — same standing rule as the engine itself.
//
// ─── Iteration 6 — the SETUP selector and the SignalView contract ─────────
//
// Every cell now has a second, bigger selector: its SETUP — `flag` (the 9 EMA
// flag engine, emaFlag.ts: long only, "a stock breaks out, goes sideways, and
// the rising 9 EMA catches up underneath", the default) or `pullback` (the
// shortFade.ts engine above, unchanged). The two engines return different
// result shapes (SignalResult vs FlagResult), but the things AROUND them — the
// chart, the strip, the panel's cards, the sound and the voice — should not
// have to know which one they are looking at. `SignalView` is that neutral
// shape: a state + headline + detail, an event list (every event carries a
// `side` and an optional `marker`), horizontal level lines tagged with a TONE
// (the chart maps tone -> colour, so this module stays colour-free), a plan
// grid, evidence chips and the flag boxes to paint. `flagView` and
// `pullbackView` are the two adapters. The pullback adapter is a pure
// repackaging of what SignalChart used to compute inline from `viewForSide`'s
// output (same lines, same order, same labels, same dash styles, same colours)
// — nothing about the pullback's behaviour changed by moving it here.
//
// What does NOT go through SignalView: the pullback's own detail panel (it
// still reads the raw SignalResult — two side cards, entry-kind wording, the
// T1/T2 grid — see SignalPanel) and the sound/voice diff, which needs the raw
// events plus the engine's own levels. `CellRead` carries both (the raw result
// AND the view) from a cell up to the pane.

import { fmtPx, type SignalEvent, type SignalLevels, type SignalResult, type SignalState } from './shortFade';
import type { FlagBox, FlagEvent, FlagResult } from './emaFlag';
import type { DildoEvent } from './dildoPb';
import type { ClimaxEvent } from './climax';
import type { BbsEvent } from './bouncyShort';
import type { IgnitionEvent } from './ignition';

export type SignalSide = 'short' | 'long' | 'both';
export const SIGNAL_SIDES: readonly SignalSide[] = ['short', 'long', 'both'];

// Which engine a cell runs. `flag` is the default for a new cell AND for a cell
// restored from a layout saved before this selector existed (signalLayout.ts).
export type SetupKind = 'flag' | 'pullback';
export const SIGNAL_SETUPS: readonly SetupKind[] = ['flag', 'pullback'];
export const DEFAULT_SETUP: SetupKind = 'flag';

// Both engines' events share this shape (ts, kind, price, label, note, marker?,
// side) — the flag engine's are side 'long', except its 9 EMA SLAM alert
// ('short'). Anything that only needs the common fields (the chart's markers,
// the events list, the sound gate) takes this. A 1-minute flag cell also merges
// in the GREEN DILDO -> 9 EMA PULLBACK rule's events (dildoPb.ts) and the
// VOLUME CLIMAX -> BOTTOM CONFIRMED rule's (climax.ts): chart markers and list
// rows only, SILENT here (see soundForEvent) — their sound and voice are the
// market scans' alerts, so a symbol open in a cell is never announced twice.
export type ViewEvent = FlagEvent | SignalEvent | DildoEvent | ClimaxEvent | BbsEvent | IgnitionEvent;

// The dildo and climax kinds: drawn and listed, never sounded or spoken by the cell.
const ALERT_RULE_KINDS: ReadonlySet<string> = new Set(['dildo', 'dildoPb', 'dildoCancel', 'climax', 'bottom', 'climaxFail', 'bbs', 'ignition', 'power', 'powerSetup', 'vwapBull', 'vwapBear']);

/**
 * A flag cell's event list with the dildo and climax rules' events merged in:
 * sorted STABLY by ts, the base (flag) events first on a tie, then each extra
 * list in argument order (dildo, then climax). With no extra events it returns
 * `base` ITSELF — the chart's marker effect is keyed on the array's identity,
 * so a copy per render would re-set the markers on every tick.
 */
export function mergeViewEvents(base: ViewEvent[], ...extras: readonly ViewEvent[][]): ViewEvent[] {
  if (extras.every((x) => x.length === 0)) return base;
  const all: { e: ViewEvent; g: number; i: number }[] = [];
  base.forEach((e, i) => all.push({ e, g: 0, i }));
  extras.forEach((xs, g) => xs.forEach((e, i) => all.push({ e, g: g + 1, i })));
  all.sort((a, b) => a.e.ts - b.e.ts || a.g - b.g || a.i - b.i);
  return all.map((x) => x.e);
}

// The spike-top story both sides read. The engine tags these events with side
// 'short' (a note on SignalEvent.side says why — the story ends in the short's
// language), but they are shared context, so a long-only cell must still draw
// and list them: a long that appears from nowhere, with no spike or crack in
// front of it, is unreadable.
const SHARED_KINDS: ReadonlySet<string> = new Set(['spike', 'crack', 'invalidated']);

// The engine's shared prefix states — what a side falls back to when it has
// nothing open or armed of its own.
const PREFIX_STATES: ReadonlySet<SignalState> = new Set(['idle', 'spike', 'crack', 'bounce']);

/**
 * STRICT side match, used for sound and voice: a cell showing only 'short'
 * never sounds a long event and vice versa; 'both' sounds everything. Unlike
 * the DISPLAY filter below this does NOT let the shared kinds through — the
 * only shared event that can ever sound is a short's `invalidated` cover, and
 * that belongs to the short side alone.
 */
export function eventOnSide(e: ViewEvent, side: SignalSide): boolean {
  return side === 'both' || e.side === side;
}

/**
 * The S/L tag for an events-list row. Spike/crack (and a list-only
 * `invalidated` setup reset) are shared context, not a side's action, so they
 * get no tag; everything else — including an `invalidated` that carries a
 * marker, which is a short being covered — reads as its engine side.
 */
export function eventTag(e: SignalEvent): 'S' | 'L' | null {
  if (e.kind === 'spike' || e.kind === 'crack') return null;
  if (e.kind === 'invalidated' && !e.marker) return null;
  return e.side === 'long' ? 'L' : 'S';
}

/**
 * The state / headline / detail a cell (or the panel's top card) shows for one
 * side. 'both' is the engine's own combined read, untouched. A single side is
 * that side's own sub-read (`result.short` / `result.long`).
 *
 * The state needs care, because a side's own state is only
 * 'none' | 'armed' | 'short'/'long' — it does not carry the shared prefix
 * (idle/spike/crack/bounce) that tells the trader "do not short into
 * strength". So: an armed or open side reports that; otherwise the shared
 * prefix is reported when the engine's top-level state IS the prefix; and when
 * the top-level state is masked by the OTHER side (it says 'short', 'long',
 * 'both' or 'armed') the prefix is necessarily 'bounce' — the other side can
 * only be armed/open after a confirmed swing low following a bounce high, and
 * the only way back to an earlier prefix state is a new HOD, which closes both
 * sides. 'bounce' (never 'idle') also matters to the chart, which hides the
 * base line while the state is 'idle'.
 */
export function sideRead(result: SignalResult, side: SignalSide): { state: SignalState; headline: string; detail: string[] } {
  if (side === 'both') return { state: result.state, headline: result.headline, detail: result.detail };
  const sub = side === 'short' ? result.short : result.long;
  let state: SignalState;
  if (sub.state === 'short' || sub.state === 'long' || sub.state === 'armed') state = sub.state;
  else state = PREFIX_STATES.has(result.state) ? result.state : 'bounce';
  return { state, headline: sub.headline, detail: sub.detail };
}

/**
 * The result a cell hands to its SignalChart: events and level lines reduced to
 * the cell's side, headline/state to that side's read. `both` returns the very
 * same object (identity matters: SignalChart keys its marker and price-line
 * effects on `result.events` / `result.levels`, so a copy per render would
 * re-set them on every tick).
 *
 * - side 'short': events of side 'short' plus the shared spike/crack/invalidated
 *   (all of which the engine already tags 'short'); the long-only level fields
 *   (higher low, bounce high, long trigger/stop/entry) nulled so no long lines
 *   draw. NEW HOD's long-side target (`longTarget`) is a long event and drops out.
 * - side 'long': the mirror — events of side 'long' plus the shared kinds, but
 *   with any MARKER stripped from the shared ones (a marker on `invalidated`
 *   means "cover the short", a ▲ that would be read as a long entry on a
 *   long-only chart); the short-only level fields (stop, trigger, lower high,
 *   entry) and the short's risk % nulled. HOD, base, VWAP, pullback low and
 *   the swing-low bookkeeping stay: they are the shared frame.
 */
export function viewForSide(result: SignalResult, side: SignalSide): SignalResult {
  if (side === 'both') return result;
  const read = sideRead(result, side);
  const events: SignalEvent[] = [];
  for (const e of result.events) {
    if (e.side === side) {
      events.push(e);
    } else if (SHARED_KINDS.has(e.kind)) {
      // Shared context always shows; only a long-only view has to drop the
      // short-cover marker (a short-only view IS the side that marker is for).
      if (side === 'long' && e.marker) {
        const { marker: _marker, ...rest } = e;
        events.push(rest);
      } else {
        events.push(e);
      }
    }
  }
  const levels: SignalLevels =
    side === 'short'
      ? {
          ...result.levels,
          higherLow: null,
          higherLowTs: null,
          bounceHigh: null,
          longTrigger: null,
          longStop: null,
          longEntryPrice: null,
          longEntryTs: null,
          longEntryKind: null,
        }
      : {
          ...result.levels,
          lowerHigh: null,
          lowerHighTs: null,
          trigger: null,
          stop: null,
          entryPrice: null,
          entryTs: null,
          entryKind: null,
        };
  return {
    ...result,
    state: read.state,
    headline: read.headline,
    detail: read.detail,
    events,
    levels,
    evidence: side === 'long' ? { ...result.evidence, riskPct: null } : result.evidence,
  };
}

const px = (v: number | null | undefined) => (v == null ? '—' : fmtPx(v));

/**
 * The right-hand half of a cell's strip: the actionable numbers of whatever is
 * armed or open on the cell's side(s), '' when nothing is (an empty strip half
 * is better than a row of dashes while the stock is still spiking). 'both'
 * joins the two sides so a two-sided moment shows both plans.
 */
export function planLine(result: SignalResult, side: SignalSide): string {
  const parts: string[] = [];
  const { levels } = result;
  if (side !== 'long') {
    if (result.short.state === 'short') parts.push(`Short ${px(levels.entryPrice)} · Stop ${px(levels.stop)}`);
    else if (result.short.state === 'armed') parts.push(`Short < ${px(levels.trigger)} · Stop ${px(levels.stop)}`);
  }
  if (side !== 'short') {
    if (result.long.state === 'long') parts.push(`Long ${px(levels.longEntryPrice)} · Stop ${px(levels.longStop)}`);
    else if (result.long.state === 'armed') parts.push(`Long > ${px(levels.longTrigger)} · Stop ${px(levels.longStop)}`);
  }
  return parts.join('  |  ');
}

// ─── SignalView — the setup-agnostic display contract ────────────────────

// What a level line MEANS, not what colour it is: SignalChart maps the tone to
// the live theme's colour on every theme change (chartColors()), so this module
// can stay free of colours and of the DOM.
//   entry — the trigger a signal waits for                (accent)
//   stop  — the protective stop                           (loss colour)
//   level — a reference level such as the prior close     (the warn/gold level colour)
//   ema   — the 9 EMA's own blue                          (the chart styles the indicator itself)
//   up / down — a plain support (gain colour) / resistance (loss colour) level; the
//               pullback engine's HOD/base/lower-high/higher-low lines are these
export type ViewTone = 'entry' | 'stop' | 'level' | 'ema' | 'up' | 'down';

export interface ViewLine {
  price: number;
  label: string;
  style: 'solid' | 'dashed' | 'dotted';
  tone: ViewTone;
}

export interface ViewKV {
  k: string;
  v: string;
}

export interface SignalView {
  setup: SetupKind;
  state: string; // the engine's own state word — drives data-state in the CSS
  stateLabel: string; // the strip badge's text
  headline: string;
  detail: string[];
  events: ViewEvent[]; // emission order, every one with a `side`; `marker` present = draw it
  lines: ViewLine[]; // horizontal levels, in the order they are drawn
  plan: ViewKV[]; // the panel's plan grid (flag only — the pullback panel draws its own)
  planText: string; // the strip's right-hand half ('' = nothing armed/open)
  chips: ViewKV[]; // evidence chips (flag only)
  boxes: FlagBox[]; // flag boxes to paint (flag only)
}

// What a cell hands UP to the pane on every recompute: the engine's own
// UNFILTERED result (the sound diff and the pullback panel need every event and
// both sides), the view the cell itself draws, and the setup/side it was built
// for (the pane folds both into its per-cell "basis" so changing either
// re-primes silently instead of replaying history). A flag cell always reports
// side 'both': the side selector is a pullback concept, and a layout migrated
// from before the setup selector may still carry side 'short' on a cell that is
// now a flag cell — which must not gate its (long) events away.
export type CellRead =
  | { setup: 'pullback'; side: SignalSide; result: SignalResult; view: SignalView }
  | { setup: 'flag'; side: 'both'; result: FlagResult; view: SignalView };

/**
 * The pullback cell's view: `viewForSide`'s reduced result repackaged as a
 * SignalView. The lines are EXACTLY what SignalChart used to build inline from
 * the same object — same order (HOD, stop, short trigger, base, lower high,
 * higher low, long trigger + long stop), same labels, same dash styles — with
 * each old colour expressed as a tone (downBody = 'down' / 'stop', upBody =
 * 'up', accent = 'entry'). `events` is the view's own array (for 'both' that is
 * `result.events` itself, so identity-keyed effects keep their cadence).
 */
export function pullbackView(result: SignalResult, side: SignalSide): SignalView {
  const vr = viewForSide(result, side);
  const lv = vr.levels;
  const lines: ViewLine[] = [];
  if (lv.hod != null) lines.push({ price: lv.hod, label: `HOD ${fmtPx(lv.hod)}`, style: 'solid', tone: 'down' });
  if (lv.stop != null) lines.push({ price: lv.stop, label: `Stop ${fmtPx(lv.stop)}`, style: 'dashed', tone: 'stop' });
  if (lv.trigger != null) lines.push({ price: lv.trigger, label: `Short < ${fmtPx(lv.trigger)}`, style: 'solid', tone: 'entry' });
  // The base is hidden while the engine is idle (the view's state, which for a
  // masked side is 'bounce' rather than 'idle' — see sideRead).
  if (lv.runLow != null && vr.state !== 'idle') lines.push({ price: lv.runLow, label: `Base ${fmtPx(lv.runLow)}`, style: 'dotted', tone: 'up' });
  // The lower high on its own only while there is no stop yet (not armed/short).
  if (lv.lowerHigh != null && lv.stop == null) lines.push({ price: lv.lowerHigh, label: `LH ${fmtPx(lv.lowerHigh)}`, style: 'dotted', tone: 'down' });
  if (lv.higherLow != null) lines.push({ price: lv.higherLow, label: `HL ${fmtPx(lv.higherLow)}`, style: 'dotted', tone: 'up' });
  if (lv.longStop != null) {
    if (lv.longTrigger != null) lines.push({ price: lv.longTrigger, label: `Long > ${fmtPx(lv.longTrigger)}`, style: 'solid', tone: 'entry' });
    lines.push({ price: lv.longStop, label: `Long stop ${fmtPx(lv.longStop)}`, style: 'dashed', tone: 'up' });
  }
  return {
    setup: 'pullback',
    state: vr.state,
    stateLabel: vr.state.toUpperCase(),
    headline: vr.headline,
    detail: vr.detail,
    events: vr.events,
    lines,
    plan: [],
    planText: planLine(result, side),
    chips: [],
    boxes: [],
  };
}

const FLAG_STATE_LABEL: Record<string, string> = {
  idle: 'IDLE',
  impulse: 'BREAKOUT',
  flat: 'FLAT',
  ready: 'READY',
  long: 'LONG',
};

const pxOr = (v: number | null | undefined) => (v == null ? '—' : fmtPx(v));
const pct1 = (v: number | null | undefined) => (v == null ? '—' : `${v.toFixed(1)}%`);

/**
 * The flag cell's view of an `evaluateFlag` result. Everything quoted comes
 * from the engine's own levels/evidence (`levels.stop` is the open position's
 * stop, else the stop a break of the active flag WOULD get; `evidence.riskPct`
 * the matching risk) — nothing is re-derived here. Lines: 'Long > T' while a
 * flag is waiting (flat/ready), 'Stop S' while long. The plan grid and the
 * strip's plan text follow the same two phases.
 */
export function flagView(result: FlagResult): SignalView {
  const { state, levels, evidence } = result;
  const waiting = state === 'flat' || state === 'ready';
  const open = state === 'long';
  const pnl = evidence.openPnlPct;
  const pnlText = pnl == null ? '—' : `${pnl >= 0 ? '+' : ''}${pnl.toFixed(1)}%`;

  const lines: ViewLine[] = [];
  if (waiting && levels.trigger != null) lines.push({ price: levels.trigger, label: `Long > ${fmtPx(levels.trigger)}`, style: 'solid', tone: 'entry' });
  if (open && levels.stop != null) lines.push({ price: levels.stop, label: `Stop ${fmtPx(levels.stop)}`, style: 'dashed', tone: 'stop' });

  const plan: ViewKV[] = [];
  if (waiting) {
    plan.push({ k: 'Trigger', v: pxOr(levels.trigger) });
    plan.push({ k: 'Stop', v: pxOr(levels.stop) });
    plan.push({ k: 'Risk %', v: pct1(evidence.riskPct) });
    plan.push({ k: 'EMA distance', v: pct1(evidence.emaDistPct) });
    plan.push({ k: 'Flag range', v: levels.flagLow != null && levels.flagHigh != null ? `${fmtPx(levels.flagLow)}–${fmtPx(levels.flagHigh)}` : '—' });
  } else if (open) {
    plan.push({ k: 'Entry', v: pxOr(levels.entryPrice) });
    plan.push({ k: 'Stop', v: pxOr(levels.stop) });
    // Once the 9 EMA trail has carried the stop ABOVE the entry the engine's
    // risk % (entry − stop, over entry) turns negative: that is profit locked
    // in, and a row reading "Risk % −22.1%" would not say so.
    const risk = evidence.riskPct;
    plan.push(risk != null && risk < 0 ? { k: 'Locked in', v: `+${(-risk).toFixed(1)}%` } : { k: 'Risk %', v: pct1(risk) });
    plan.push({ k: 'Open P&L', v: pnlText });
    plan.push({ k: 'EMA distance', v: pct1(evidence.emaDistPct) });
  }

  // The strip's plan: 'Long > T · stop S' while a flag waits, 'Long E · stop S
  // · +P%' while long, nothing otherwise.
  const stopPart = levels.stop != null ? ` · stop ${fmtPx(levels.stop)}` : '';
  let planText = '';
  if (waiting && levels.trigger != null) planText = `Long > ${fmtPx(levels.trigger)}${stopPart}`;
  else if (open && levels.entryPrice != null) planText = `Long ${fmtPx(levels.entryPrice)}${stopPart}${pnl != null ? ` · ${pnlText}` : ''}`;

  const chips: ViewKV[] = [
    { k: 'ext', v: evidence.extPct == null ? '—' : `+${evidence.extPct.toFixed(1)}%` },
    { k: 'flag bars', v: evidence.flatBars == null ? '—' : String(evidence.flatBars) },
    { k: 'span', v: evidence.flatSpanPctOfMove == null ? '—' : `${evidence.flatSpanPctOfMove.toFixed(0)}% of move` },
    { k: 'EMA dist', v: pct1(evidence.emaDistPct) },
    { k: 'retrace', v: pct1(evidence.retracePct) },
    // volDry = the flag's mean volume / the leg's mean volume (under 1 = drying up).
    { k: 'vol dry', v: evidence.volDry == null ? '—' : `${(evidence.volDry * 100).toFixed(0)}% of leg` },
  ];

  return {
    setup: 'flag',
    state,
    // An impulse whose top has outlived `maxFlagBars` is the engine's RANGE read (emaFlag.ts summarise): the strip prints
    // `${badge} ${headline}`, so the badge says RANGE too — BREAKOUT in front of 'RANGE — no flag…' contradicted it for
    // six hours on PTC. `state` itself stays 'impulse' (data-state, the scan's e9State 1).
    stateLabel: state === 'impulse' && result.topExpired ? 'RANGE' : (FLAG_STATE_LABEL[state] ?? state.toUpperCase()),
    headline: result.headline,
    detail: result.detail,
    events: withSlamMarkers(result.events),
    lines,
    plan,
    planText,
    chips,
    boxes: result.boxes,
  };
}

// The 9 EMA SLAM leaves the engine WITHOUT a marker (emaFlag.ts: it is an alert,
// never a trade, and the window draws its own). The view's copy of it gets a
// 'sell' marker — the red ▼ the SignalView contract draws for `marker`, and the
// red dot in the panel's events list — while every other event, and the engine's
// own array (the sound diff's input), is left untouched. No slam: the engine's
// array itself, so identity-keyed effects keep their cadence.
function withSlamMarkers(events: FlagEvent[]): FlagEvent[] {
  if (!events.some((e) => e.kind === 'slam' && !e.marker)) return events;
  return events.map((e) => (e.kind === 'slam' && !e.marker ? { ...e, marker: 'sell' as const } : e));
}

// ─── Chart markers ───────────────────────────────────────────────────────

// One engine Execution marker (vendor/blackthorn-charts: buy = ▲ under the
// price, sell = ▼ over it, each with a label pill). Structurally that type, so
// this module stays free of the chart engine.
export interface ViewMarker {
  ts: number;
  price: number;
  side: 'buy' | 'sell';
  label: string;
}

// The short pill labels of the dildo / climax markers (their events carry the long labels for the list).
const ALERT_RULE_MARKER: Readonly<Record<string, string>> = { dildo: 'DILDO', dildoPb: '9 EMA PB', climax: 'CLIMAX', bottom: 'BOTTOM', bbs: 'BBS', ignition: 'IGN' };

/**
 * The markers SignalChart draws for a view: every event that carries one, in
 * emission order, as before — with two merges, both the same rule: a marker
 * that lands on the bar of a marker of its own side joins it instead of
 * stacking a second arrow and label pill on the same price.
 *   - a 9 EMA SLAM on the bar of a long slammed out of its position (the EXIT ▼
 *     at the same close) reads 'EXIT · SLAM'; the slam is emitted after its
 *     bar's EXIT, so the EXIT is already there;
 *   - the dildo / climax rules' ▲ markers — 'DILDO' and '9 EMA PB' (the dildo's
 *     close and the pullback bar's close), 'CLIMAX' (at the climax LOW: the level
 *     that must hold) and 'BOTTOM' (the confirming close) — join a ▲ already on
 *     their bar: 'LONG (FLAG) · PB', 'LONG (FLAG) · BOTTOM', 'DILDO · CLIMAX'
 *     (mergeViewEvents puts the flag events first on a tie, then the dildo's,
 *     then the climax's).
 * Nothing else merges (the pullback can put two markers on one bar, and both stay).
 */
export function chartMarkers(events: readonly ViewEvent[]): ViewMarker[] {
  const out: ViewMarker[] = [];
  for (const e of events) {
    if (!e.marker) continue;
    const ruleLabel = ALERT_RULE_MARKER[e.kind];
    if (e.kind === 'slam' || ruleLabel) {
      const host = out.find((m) => m.ts === e.ts && m.side === e.marker);
      if (host) {
        host.label = `${host.label} · ${ruleLabel ?? 'SLAM'}`;
        continue;
      }
    }
    out.push({ ts: e.ts, price: e.price, side: e.marker, label: ruleLabel ?? e.label });
  }
  return out;
}

// ─── Sound and voice cues ────────────────────────────────────────────────
//
// Which tone and which spoken line each event earns, kept beside the side
// filter (eventOnSide above is the gate that decides WHETHER a cell may sound
// an event at all; these decide WHAT it sounds) so the whole table is one pure,
// testable unit instead of living inside a React component. Tones are audio.ts
// names ('short' descending, 'long' ascending, 'add' a single note, plus the
// scan tones ping/bell). The window sits on a spare monitor the trader isn't
// looking at: every spoken line starts with the symbol.

/**
 * The tone for one event, chosen by LABEL, not kind — several kinds share a
 * meaning (entryLH covers both 'SHORT (LH)' and 'ADD (LH)'; 'NEW HOD' is an
 * `invalidated` on the short side and a `longTarget` on the long side).
 *
 * Direction is in the contour: a short entry falls (880→587 Hz), a long entry
 * rises (587→880 Hz); an add is the single 587 Hz note on either side; exits
 * ring the bell; targets and a lower high are a soft ping. A short's NEW HOD
 * only sounds when it carries a marker (a live short being covered) — an idle
 * setup reset is list-only — while the long's NEW HOD is its take-profit.
 *
 * The 9 EMA flag engine's events (iteration 6) are mapped here too, by label:
 * FLAT (the stock has gone sideways above the rising EMA) is its own soft
 * double note ('flat', audio.ts — the alert the trader asked for), 9 EMA NEAR
 * a ping, LONG (FLAG) the ascending 'long' phrase, ADD (FLAG) the single 'add'
 * note, EXIT the bell. FLAG FAILED has no tone (the voice says it, and only
 * when its bar is not also an exit or a slam), and the flag engine's BREAKOUT (the impulse
 * itself) and EARLY BREAK are silent — they are context, not actions. That
 * BREAKOUT shares its LABEL with the pullback long side's breakout
 * confirmation (an 'add' note), so the flag's pair is told apart by KIND
 * first. SLAM (9 EMA), the flag engine's one bearish alert, is the falling
 * 'slam' glide — the same tone the "9EMA Slam" scan's alert plays.
 */
export function soundForEvent(e: ViewEvent): string | null {
  if (e.kind === 'impulse' || e.kind === 'earlyBreak') return null;
  // GREEN DILDO / 9 EMA PULLBACK / VOLUME CLIMAX / BOTTOM CONFIRMED (and their
  // cancel / fail rows) are silent in a cell: the "Green Dildo", "Dildo 9EMA
  // Pullback", "Volume Climax" and "Bottom Confirmed" scans' alerts are their
  // only sound, so a symbol open in a cell is never announced twice.
  if (ALERT_RULE_KINDS.has(e.kind)) return null;
  switch (e.label) {
    // 9 EMA flag (long only)
    case 'FLAT':
      return 'flat';
    case '9 EMA NEAR':
      return 'ping';
    case 'LONG (FLAG)':
      return 'long';
    case 'ADD (FLAG)':
      return 'add';
    case 'EXIT':
      return 'bell';
    case 'FLAG FAILED':
      return null;
    case 'SLAM (9 EMA)':
      return 'slam';
    // short side
    case 'SHORT (LH)':
    case 'SHORT':
    case 'SHORT (VWAP)':
    case 'SHORT (VWAP REJECT)':
      return 'short';
    case 'ADD (LH)':
    case 'BREAKDOWN':
    case 'VWAP REJECT':
      return 'add';
    case 'LOWER HIGH':
      return 'ping';
    case 'STOP':
      return 'bell';
    // long side
    case 'LONG (HL)':
    case 'LONG':
    case 'LONG (VWAP HOLD)':
      return 'long';
    case 'ADD (HL)':
    case 'BREAKOUT':
    case 'VWAP HOLD':
      return 'add';
    case 'STOP (LONG)':
      return 'bell';
    case 'T1 BOUNCE HIGH':
    case 'T2 HOD':
      return 'ping';
    case 'NEW HOD':
      return e.side === 'long' ? 'ping' : e.marker ? 'bell' : null;
    default:
      return null;
  }
}

/**
 * The spoken text for one event, symbol first, or null when the event is not
 * spoken. Lower highs, higher lows, targets and warnings are chatter and are
 * never spoken (a tone at most) — except the long's NEW HOD take-profit, which
 * is spoken because it asks for an action (take profit). The plain 'entry'/'entryVwap' short entries
 * (reachable only with lhEntry off, or a VWAP loss with a lower high) and the
 * long's armed-breakout 'LONG' and 'ADD' forms of VWAP hold get phrases in the
 * same voice as the specified ones. Prices via fmtPx; `levels` supplies the
 * stop to read out on an entry (the short's `stop`, the long's `longStop`).
 *
 * `ctx` is the pullback cell's SignalLevels (as ever) or, for a flag cell, its
 * FlagResult — see flagVoicePhrase below for the flag lines.
 */
export function voicePhrase(sym: string, e: ViewEvent, ctx: SignalLevels | FlagResult): string | null {
  // The dildo / climax rules' events are never spoken by a cell (see soundForEvent).
  if (ALERT_RULE_KINDS.has(e.kind)) return null;
  // A flag cell's context is its whole FlagResult (the phrases need the levels,
  // the evidence AND the entry event an exit's P&L is measured from); a
  // pullback cell's is its SignalLevels, as ever. `levels` exists only on the
  // flag result, so this is a real discriminator, not a cast.
  if ('levels' in ctx) return flagVoicePhrase(sym, e, ctx);
  const levels = ctx;
  const shortStop = levels.stop != null ? ` Stop ${fmtPx(levels.stop)}.` : '';
  const longStop = levels.longStop != null ? ` Stop ${fmtPx(levels.longStop)}.` : '';
  const p = fmtPx(e.price);
  switch (e.kind) {
    // short side
    case 'entryLH':
      return e.label === 'ADD (LH)' ? `${sym}: add, lower high, ${p}.` : `${sym}: short. Lower high. ${p}.${shortStop}`;
    case 'breakdown':
      return `${sym}: breakdown, ${p}.`;
    case 'entry':
      return `${sym}: short. ${p}.${shortStop}`;
    case 'entryVwap':
      return `${sym}: short. VWAP loss. ${p}.${shortStop}`;
    case 'vwapReject':
      return e.label === 'SHORT (VWAP REJECT)' ? `${sym}: VWAP rejection, short, ${p}.${shortStop}` : `${sym}: add, VWAP rejection, ${p}.`;
    case 'stopped':
      return `${sym}: stop, ${p}.`;
    case 'invalidated':
      return e.marker ? `${sym}: new high, cover.` : null;
    // long side
    case 'entryHL':
      return e.label === 'ADD (HL)' ? `${sym}: add, higher low, ${p}.` : `${sym}: long. Higher low. ${p}.${longStop}`;
    case 'breakout':
      return e.label === 'LONG' ? `${sym}: long. Breakout. ${p}.${longStop}` : `${sym}: breakout, ${p}.`;
    case 'vwapHold':
      return e.label === 'LONG (VWAP HOLD)' ? `${sym}: VWAP hold, long, ${p}.${longStop}` : `${sym}: add, VWAP hold, ${p}.`;
    case 'longStopped':
      return `${sym}: long stop, ${p}.`;
    case 'longTarget':
      return e.label === 'NEW HOD' ? `${sym}: new high, take profit.` : null;
    default:
      return null;
  }
}

// The flag engine's spoken lines. Numbers come from the engine's typed data —
// never parsed out of the `note` text — read from the result the event was
// first seen in, which is the live one (the voice fires on the tick the closed
// bar produced the event): the flag range for FLAT, the trigger for 9 EMA NEAR,
// the stop for the entry, and for an exit the P&L against the matching entry
// event's price (the FIRST entry of the position — adds never move it; an
// exit's own `levels` are already cleared). The EMA distance for FLAT / 9 EMA
// NEAR is the EVENT bar's own (the engine stamps `emaDistPct` on those two
// events): the result's live distance belongs to the bar printing NOW, and a
// closed-bar event is first seen on the first tick of the NEXT bar, where the
// forming EMA has already moved ~20% of the way to the new price — the spoken
// number would disagree with the event's note and the brief.
// FLAG FAILED is spoken only when no position was open: while long, the exit
// that fires on the same bar is the thing to hear — and on the bar of a 9 EMA
// SLAM, the slam is. BREAKOUT and EARLY BREAK are not spoken at all. SLAM (9 EMA)
// says '<SYM>: nine E M A slam', word for word what the "9EMA Slam" scan's alert
// says (e9.ts spokenScanName), so the trader learns one line for the signal.
function flagVoicePhrase(sym: string, e: ViewEvent, r: FlagResult): string | null {
  const p = fmtPx(e.price);
  const dist = 'emaDistPct' in e && e.emaDistPct != null ? e.emaDistPct : r.evidence.emaDistPct;
  switch (e.label) {
    case 'FLAT': {
      // The flag's range: the engine's live flag levels, else the box this
      // event's bar belongs to.
      let low = r.levels.flagLow;
      let high = r.levels.flagHigh;
      if (low == null || high == null) {
        const box = r.boxes.find((b) => b.startTs <= e.ts && e.ts <= b.endTs);
        low = box?.low ?? null;
        high = box?.high ?? null;
      }
      const range = low != null && high != null ? `, ${fmtPx(low)} to ${fmtPx(high)}` : '';
      const ema = dist == null ? '' : dist >= 0 ? `, EMA ${dist.toFixed(1)}% below` : `, EMA ${(-dist).toFixed(1)}% above`;
      return `${sym}: going sideways above the 9 EMA${range}${ema}.`;
    }
    case '9 EMA NEAR': {
      // The event's own price IS the flag's trigger (the engine stamps FLAT /
      // 9 EMA NEAR / the entry with it), so "long above" survives the flag
      // having already broken by the time this is read out.
      const d = dist == null ? '' : dist >= 0 ? `, ${dist.toFixed(1)}% below` : `, ${(-dist).toFixed(1)}% above`;
      return `${sym}: 9 EMA caught up${d}. Long above ${p}.`;
    }
    case 'LONG (FLAG)': {
      const stop = r.levels.stop != null ? ` Stop ${fmtPx(r.levels.stop)}.` : '';
      return `${sym}: long, flag breakout, ${p}.${stop}`;
    }
    case 'ADD (FLAG)':
      return `${sym}: add, flag breakout, ${p}.`;
    case 'EXIT': {
      let entry: number | null = null;
      for (let i = r.events.length - 1; i >= 0; i--) {
        const x = r.events[i];
        if (x.kind === 'entry' && x.ts <= e.ts) {
          entry = x.price;
          break;
        }
      }
      const pnl = entry != null && entry > 0 ? ((e.price - entry) / entry) * 100 : null;
      // The wording follows the engine's note: 'closed below the N EMA trail S' is a
      // close under the TRAILED stop, 'closed below the stop S' one under the
      // initial stop (the bar can still be above the EMA — never say it lost it),
      // and 'traded down to ...' a by-low touch (stopByClose off): a stop hit.
      const trail = /^closed below the (\d+) EMA trail/.exec(e.note);
      const why = trail ? `closed below the ${trail[1]} EMA` : /^closed below/.test(e.note) ? 'closed below the stop' : 'stop hit';
      const tail = pnl == null ? '' : `, ${pnl >= 0 ? 'plus' : 'minus'} ${Math.abs(pnl).toFixed(1)} percent`;
      return `${sym}: exit, ${why}, ${p}${tail}.`;
    }
    case 'FLAG FAILED': {
      const wasLong = r.state === 'long' || r.events.some((x) => x.kind === 'exit' && x.ts === e.ts);
      const slammed = r.events.some((x) => x.kind === 'slam' && x.ts === e.ts);
      return wasLong || slammed ? null : `${sym}: flag failed, lost the 9 EMA.`;
    }
    case 'SLAM (9 EMA)':
      return `${sym}: nine E M A slam`;
    default:
      return null;
  }
}
