import { useEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from 'react';
import { useStore } from '../store';
import { getChart } from '../ws';
import { aggregate } from '../chartAgg';
import { etDayKey } from '../marketClock';
import { evaluate, fmtPx, type SignalParams } from '../signals/shortFade';
import { evaluateFlag, type FlagParams } from '../signals/emaFlag';
import { evaluateDildo } from '../signals/dildoPb';
import { evaluateClimax } from '../signals/climax';
import { BBS_NONE, evaluateBbs } from '../signals/bouncyShort';
import { evaluateIgnition } from '../signals/ignition';
import { closedBarMemo } from '../signals/closedMemo';
import { flagView, mergeViewEvents, pullbackView, type CellRead, type SetupKind, type SignalSide } from '../signals/view';
import { FOLLOW_KINDS, FOLLOW_LABELS, normalizeFollow, type FollowKind } from '../signalFollow';
import { getSignalRuleParams, subscribeSignalRuleParams, type SignalCellDef } from '../signalLayout';
import type { ChartTf } from '../types';
import { SignalChart } from './SignalChart';

// SignalCell is one cell of the Signal window's grid (?pane=signal —
// addendum 2): its own header (symbol picker + setup + timeframe), its own
// SignalChart instance (the engine lifecycle lives there, unchanged — see
// SignalChart.tsx's own comment), and a compact state strip summarising
// whatever the engine currently says about this cell's symbol. It owns the
// aggregate()+evaluate() call that used to live directly in SignalPane (one
// chart's worth); now that there can be several, each cell runs its own copy
// and reports the result upward so the pane can drive the shared detail panel
// (the ACTIVE cell's result) and the shared sound diff (every cell's result)
// without either of those needing to know how many cells there are.
//
// The tuning params arrive as props (the pane owns them); the cell reads its
// OWN symbol's bars / prior close / chart error straight from the store, keyed
// on def.sym — the way MonitorChart does. The pane used to subscribe to the
// whole per-symbol maps and thread them down, which re-rendered every cell
// (and the detail panel) on every live bar of every charted symbol. The symbol
// picker's `watchlist` options are read directly for the same reason.
//
// Setup selector (iteration 6): the head leads with a `9EMA flag | Pullback`
// seg choosing which ENGINE this cell runs — flag (emaFlag.ts, long only, the
// default) or pullback (shortFade.ts, unchanged). Exactly one engine runs per
// cell (the other's memo short-circuits to null), and what the cell hands to
// its chart, strip and the pane is the engine-neutral SignalView / CellRead of
// signals/view.ts, so none of those know which engine they are looking at.
//
// Side selector (addendum 4): for a PULLBACK cell the head also carries an
// S | L | S+L seg choosing which side of the pullback it shows (a flag cell is
// long only, so the seg is simply absent there). It is DISPLAY-only — the cell
// always evaluates the engine once and reports that full, unfiltered result
// upward (the pane's sound diff and the detail panel need every event, and
// apply the cell's side themselves); only what THIS cell hands to its
// SignalChart and strip is passed through viewForSide (signals/view.ts, inside
// pullbackView).
//
// Drop ownership (fixes an open issue from the grid iteration): a ticker
// dropped on the chart used to be handled twice — once by SignalChart's own
// drop handler and again as the event bubbled to this cell's <section>. The
// SECTION is the single owner: it is the larger target (it also covers the
// head and the strip, which SignalChart's root never could, and an empty cell
// has no chart at all), and it already owned the drag-over highlight. The
// chart is therefore handed a no-op `onSymbolDrop` (its prop is required and
// the chart is not ours to edit), so its handler still swallows the default
// browser drop but assigns nothing — the assignment happens exactly once, on
// the way up. The chart's own duplicate highlight is hidden in styles.css.

const SYM_RE = /^[A-Z][A-Z0-9.\-]{0,9}$/;
const SIGNAL_TFS: readonly ChartTf[] = ['1m', '5m'];

const SIDE_BUTTONS: { side: SignalSide; label: string }[] = [
  { side: 'short', label: 'S' },
  { side: 'long', label: 'L' },
  { side: 'both', label: 'S+L' },
];

const SETUP_BUTTONS: { setup: SetupKind; label: string }[] = [
  { setup: 'flag', label: '9EMA flag' },
  { setup: 'pullback', label: 'Pullback' },
];

// See the drop-ownership note above: the <section> owns drops, the chart's own
// (required) callback deliberately does nothing.
const noopDrop = () => {};

function validSym(raw: string): string | null {
  const s = raw.trim().toUpperCase();
  return SYM_RE.test(s) ? s : null;
}

// The previous ET day's last close among the candles — the header's day-%
// reference when the scanner supplies no official prior close. The pullback
// engine already computes exactly this (evidence.refClose, B5 of addendum 1);
// the flag engine has no gap gate and no such field, so the flag cell derives
// it the same way: the last candle whose ET day is not the last candle's.
function prevDayClose(candles: { ts: number; close: number }[]): number | null {
  if (candles.length < 2) return null;
  const today = etDayKey(candles[candles.length - 1].ts);
  for (let i = candles.length - 2; i >= 0; i--) {
    if (etDayKey(candles[i].ts) !== today) return candles[i].close > 0 ? candles[i].close : null;
  }
  return null;
}

export interface SignalCellProps {
  idx: number;
  def: SignalCellDef;
  active: boolean;
  params: SignalParams; // the pullback engine's tuning
  flagParams: FlagParams; // the flag engine's tuning
  onAssign: (sym: string | null) => void;
  onTf: (tf: ChartTf) => void;
  onSide: (side: SignalSide) => void;
  onSetup: (setup: SetupKind) => void;
  onFollow: (f: FollowKind) => void;
  onActivate: () => void;
  onResult: (read: CellRead | null) => void;
}

export function SignalCell({
  idx: _idx,
  def,
  active,
  params,
  flagParams,
  onAssign,
  onTf,
  onSide,
  onSetup,
  onFollow,
  onActivate,
  onResult,
}: SignalCellProps) {
  const sym = def.sym;
  const hasSym = !!sym;
  const watchlist = useStore((s) => s.watchlist);
  // Per-symbol selectors: a live bar for another cell's symbol leaves these
  // referentially unchanged, so this cell does not re-render for it.
  const bars = useStore((s) => (sym ? s.barsBySym[sym] : undefined));
  const prevClose = useStore((s) => (sym ? s.prevCloseBySym[sym] : undefined));
  const err = useStore((s) => (sym ? s.chartErrBySym[sym] : undefined));
  const [typed, setTyped] = useState('');
  const [dragOver, setDragOver] = useState(false);
  const dragDepth = useRef(0); // dragenter/dragleave fire per child; count so the highlight doesn't flicker

  const tfMin = def.tf === '5m' ? 5 : 1;
  const candles = useMemo(() => aggregate(bars ?? [], tfMin), [bars, tfMin]);
  const setup = def.setup;
  // A flag cell is long only: the side selector does not apply, and the cell
  // reports 'both' so its (long) events are never gated away by a stale saved
  // side (see CellRead in signals/view.ts).
  const side: SignalSide = setup === 'flag' ? 'both' : def.side;

  // Exactly one engine runs per cell: the memo of the OTHER setup short-circuits
  // to null without evaluating, so a Tuning edit for one engine never recomputes
  // the cells running the other.
  const pullbackResult = useMemo(
    () =>
      setup === 'pullback' && hasSym && candles.length >= 2
        ? evaluate({ candles, tfMs: tfMin * 60_000, nowMs: Date.now(), prevClose: prevClose ?? null, params })
        : null,
    [setup, hasSym, candles, tfMin, prevClose, params],
  );
  const flagResult = useMemo(
    () =>
      setup === 'flag' && hasSym && candles.length >= 2
        ? evaluateFlag({ candles, tfMs: tfMin * 60_000, nowMs: Date.now(), params: flagParams })
        : null,
    [setup, hasSym, candles, tfMin, flagParams],
  );

  // GREEN DILDO -> 9 EMA PULLBACK (signals/dildoPb.ts) and VOLUME CLIMAX ->
  // BOTTOM CONFIRMED (signals/climax.ts), 2026-10-07: on a 1-MINUTE flag cell
  // both rules also run, and their events are merged into the cell's VIEW only
  // (markers + the panel's events list). They are SILENT here: `read.result`
  // stays the flag engine's own, so the pane's sound diff never sees them, and
  // view.ts maps every one of their kinds to no tone and no phrase — their
  // sound and voice are the "Green Dildo" / "Dildo 9EMA Pullback" / "Volume
  // Climax" / "Bottom Confirmed" scans' alerts, so a symbol open in a cell is
  // never announced twice. On a 5-minute cell both are null (1-minute rules).
  // Their params are the window's shared Tuning copy (signalLayout.ts), not a
  // prop: chart markers only, the market scans run the defaults.
  const ruleParams = useSyncExternalStore(subscribeSignalRuleParams, getSignalRuleParams);
  const ruleCell = setup === 'flag' && tfMin === 1 && hasSym && candles.length >= 2;
  const dildoResult = useMemo(
    () => (ruleCell ? evaluateDildo({ candles, tfMs: 60_000, nowMs: Date.now(), params: ruleParams.dildo }) : null),
    [ruleCell, candles, ruleParams.dildo],
  );
  const climaxResult = useMemo(
    () => (ruleCell ? evaluateClimax({ candles, tfMs: 60_000, nowMs: Date.now(), params: ruleParams.climax }) : null),
    [ruleCell, candles, ruleParams.climax],
  );
  // BOUNCY BALL SHORT (signals/bouncyShort.ts, 2026-10-14): same treatment —
  // its BBS ▼ marker at the breaking close and a dotted support line while the
  // base is forming or broke today; silent here (the "Bouncy Ball Short" scan
  // speaks). Default params (no Tuning drawer yet).
  // Both this and IGNITION read CLOSED candles only, so they go through
  // closedBarMemo (signals/closedMemo.ts): a forming-bar update (every live
  // tick) re-runs neither; a new closed bar, a rewritten history, another
  // symbol or timeframe re-evaluates exactly as before. One memo per cell.
  const bbsMemo = useMemo(() => closedBarMemo(evaluateBbs), []);
  const ignMemo = useMemo(() => closedBarMemo(evaluateIgnition), []);
  const bbsResult = useMemo(
    () => (ruleCell ? bbsMemo({ candles, tfMs: 60_000, nowMs: Date.now() }) : null),
    [ruleCell, candles, bbsMemo],
  );
  // IGNITION (signals/ignition.ts, 2026-10-14): its IGN ▲ marker at the close;
  // silent here (the "Ignition" scan speaks). Default params.
  const ignResult = useMemo(
    () => (ruleCell ? ignMemo({ candles, tfMs: 60_000, nowMs: Date.now() }) : null),
    [ruleCell, candles, ignMemo],
  );

  // What this cell DRAWS and READS, and what it REPORTS: the engine's result
  // reduced to a SignalView (signals/view.ts). For the pullback the view is
  // viewForSide's reduction to the cell's side; memoised on (result, side) so
  // the chart's marker and line effects — keyed on view.events / view.lines
  // identity — only re-run when the result or the selector actually changes
  // ('both' hands back the result's own events array). `read` is the pair
  // (raw, unfiltered result + view) the pane's sound diff and detail panel use.
  const read = useMemo<CellRead | null>(() => {
    if (setup === 'flag') {
      if (!flagResult) return null;
      const fv = flagView(flagResult);
      // flag events first on a tie, then the dildo's, then the climax's; `fv` itself (identity kept) when there are none
      const events = mergeViewEvents(fv.events, dildoResult?.events ?? [], climaxResult?.events ?? [], bbsResult?.events ?? [], ignResult ?? []);
      const sup = bbsResult && bbsResult.state !== BBS_NONE ? bbsResult.support : null;
      const lines =
        sup != null
          ? [...fv.lines, { price: sup, label: `${bbsResult!.state === 1 ? 'BBS support' : 'BBS broke'} ${sup < 1 ? sup.toFixed(4) : sup.toFixed(2)}`, style: 'dotted' as const, tone: 'down' as const }]
          : fv.lines;
      return { setup: 'flag', side: 'both', result: flagResult, view: events === fv.events && lines === fv.lines ? fv : { ...fv, events, lines } };
    }
    return pullbackResult ? { setup: 'pullback', side, result: pullbackResult, view: pullbackView(pullbackResult, side) } : null;
  }, [setup, flagResult, pullbackResult, side, dildoResult, climaxResult, bbsResult, ignResult]);
  const view = read?.view ?? null;

  // Report every recompute upward — the pane keys its resultsRef/sound diff
  // off THIS effect firing, not off any store subscription of its own (see
  // SignalPane's onResult comment). Deliberately NOT exhaustive-deps: only
  // `read` changing should ever re-report; onResult is a fresh closure every
  // render (it closes over this cell's own idx/def.sym), and calling the
  // latest one whenever read actually changes is correct.
  //
  // The report is the FULL, unfiltered result — the sound diff applies each
  // cell's side itself and the panel needs both sides' cards. `setup` and
  // `side` ride inside `read`, so a selector change re-reports at once: the
  // pane folds both into its per-cell "basis" and re-primes silently on a
  // change, which must not wait for the next tick to happen (an event landing
  // in that gap would otherwise be swallowed by the late re-prime).
  useEffect(() => {
    onResult(read);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [read]);

  // Header derivation, same shape as SignalPane's original single-chart
  // header: the reference close falls back the same way the engine's own gap
  // gate does (B5 in addendum 1) — a genuinely present, POSITIVE prevClose is
  // the official one; otherwise fall back to the previous ET day's last candle
  // (the pullback engine's own evidence.refClose; the flag cell derives it
  // directly — see prevDayClose).
  const last = bars && bars.length ? bars[bars.length - 1].c : null;
  const officialPrevClose = typeof prevClose === 'number' && prevClose > 0 ? prevClose : null;
  const refClose = officialPrevClose ?? (pullbackResult ? pullbackResult.evidence.refClose : flagResult ? prevDayClose(candles) : null);
  const dayPct = last != null && refClose ? ((last - refClose) / refClose) * 100 : null;
  const dayPctTitle = officialPrevClose != null ? 'vs prev close' : "vs yesterday's AH close";

  // Dropdown options: the watchlist, plus the current symbol if it was
  // assigned outside the list (typed/dropped) — exactly MonitorChart's logic,
  // so the select stays valid either way.
  const options = useMemo(() => {
    if (sym && !watchlist.includes(sym)) return [sym, ...watchlist];
    return watchlist;
  }, [watchlist, sym]);

  const loadingNode: ReactNode | null = !hasSym
    ? null
    : !bars
      ? err
        ? (
            <div className="chartmodal-loading muted" style={{ flexDirection: 'column', gap: 8 }}>
              <span>
                Couldn&rsquo;t load {sym} — {err}
              </span>
              <button
                className="mini"
                onClick={(e) => {
                  e.stopPropagation(); // don't also activate the cell on a Retry click
                  if (!sym) return;
                  useStore.getState().clearSymChartErr(sym);
                  getChart(sym);
                }}
              >
                Retry
              </button>
            </div>
          )
        : <div className="chartmodal-loading muted">Loading {sym}…</div>
      : bars.length < 2
        ? <div className="chartmodal-loading muted">No intraday data for {sym} yet.</div>
        : null;

  return (
    <section
      className={`signal-cell${dragOver ? ' dropzone' : ''}${active ? ' active' : ''}`}
      // The whole cell is a drop target (same shape as MonitorChart's
      // section-level handlers) — a ticker dropped on the head or the strip
      // assigns it too, not just the chart area.
      onDragEnter={(e) => {
        if (!e.dataTransfer.types.includes('text/plain')) return;
        dragDepth.current++;
        setDragOver(true);
      }}
      onDragOver={(e) => {
        if (!e.dataTransfer.types.includes('text/plain')) return;
        e.preventDefault(); // required to make this a valid drop target
        e.dataTransfer.dropEffect = 'copy';
      }}
      onDragLeave={() => {
        dragDepth.current = Math.max(0, dragDepth.current - 1);
        if (dragDepth.current === 0) setDragOver(false);
      }}
      onDrop={(e) => {
        e.preventDefault();
        dragDepth.current = 0;
        setDragOver(false);
        const s = e.dataTransfer.getData('text/plain').trim().toUpperCase();
        if (SYM_RE.test(s)) onAssign(s);
      }}
    >
      <div className="signal-cell-head">
        <select
          className="monitor-sel"
          value={sym ?? ''}
          onChange={(e) => onAssign(e.target.value || null)}
          title="assign a watchlist symbol to this chart ('—' clears it)"
        >
          <option value="">—</option>
          {options.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <div
          className="seg seg-setup"
          title="which signal this chart runs: 9EMA flag (long only — a breakout, then sideways while the 9 EMA catches up) or the Pullback engine (short / long)"
        >
          {SETUP_BUTTONS.map((b) => (
            <button key={b.setup} className={`segbtn ${setup === b.setup ? 'on' : ''}`} onClick={() => onSetup(b.setup)}>
              {b.label}
            </button>
          ))}
        </div>
        <div className="seg">
          {SIGNAL_TFS.map((t) => (
            <button key={t} className={`segbtn ${def.tf === t ? 'on' : ''}`} onClick={() => onTf(t)}>
              {t}
            </button>
          ))}
        </div>
        {setup === 'pullback' && (
          <div className="seg" title="which side of the pullback to show: short (lower high), long (higher low), or both">
            {SIDE_BUTTONS.map((b) => (
              <button key={b.side} className={`segbtn ${side === b.side ? 'on' : ''}`} onClick={() => onSide(b.side)}>
                {b.label}
              </button>
            ))}
          </div>
        )}
        <select
          className="monitor-sel"
          value={def.follow ?? 'off'}
          onChange={(e) => onFollow(normalizeFollow(e.target.value))}
          title="Follow: switch this chart to the symbol of each new Dildo / HOD flag / 9EMA flag sideways alert (the flag options also set the 9EMA flag mode)"
        >
          {FOLLOW_KINDS.map((k) => (
            <option key={k} value={k}>
              {k === 'off' ? 'Follow: Off' : `Follow: ${FOLLOW_LABELS[k]}`}
            </option>
          ))}
        </select>
        <span className="spacer" />
        {last != null && <span className="monitor-last">{fmtPx(last)}</span>}
        {dayPct != null && (
          <span className={`monitor-pct ${dayPct >= 0 ? 'pos' : 'neg'}`} title={dayPctTitle}>
            {dayPct >= 0 ? '+' : ''}
            {dayPct.toFixed(2)}%
          </span>
        )}
      </div>
      {/* Clicking the chart or the strip activates this cell (the detail
          panel then reads it) and links it to TWS/depth/borrow — same
          click-to-link semantics as MonitorChart's canvas click. The head
          (select/tf/price) is deliberately outside this handler so picking a
          symbol or timeframe doesn't also steal activation — and so are the
          controls INSIDE the body: SignalChart's Auto/Manual scale button and
          its » go-to-latest button bubble their clicks up here, and clicking
          » on a non-active cell to see its latest bar would otherwise also
          activate it and move the TWS / depth / borrow link (MonitorChart's
          Auto button sits outside its click-to-link target for the same
          reason). Any click that lands on a button, input or select is theirs
          alone; the canvas and the strip still activate. */}
      <div
        className="signal-cell-body"
        onClick={(e) => {
          if ((e.target as HTMLElement).closest('button, input, select')) return;
          onActivate();
        }}
      >
        {hasSym ? (
          <SignalChart
            sym={sym ?? ''}
            candles={candles}
            tfMin={tfMin}
            prevClose={prevClose ?? null}
            setup={setup}
            emaPeriod={flagParams.emaPeriod}
            view={view}
            loading={loadingNode}
            onSymbolDrop={noopDrop}
          />
        ) : (
          // Same placeholder MonitorChart's empty cell uses (unstyled here —
          // .signal-cell-body reuses .monitor-chart's flex-column shape so
          // .monitor-empty's own flex:1 centering applies exactly as it does
          // there), not nested inside a chart mount point since no engine
          // exists yet to own one.
          <div className="monitor-empty">
            <span className="muted">drop a symbol here</span>
            <input
              value={typed}
              placeholder="or type…"
              onClick={(e) => e.stopPropagation()}
              onChange={(e) => setTyped(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && typed.trim()) {
                  const s = validSym(typed);
                  if (s) onAssign(s);
                  setTyped('');
                }
              }}
            />
          </div>
        )}
        {/* The strip: the state badge + headline on the left, the plan on the
            right. For a pullback cell 'S+L' is the engine's combined headline
            and a single side is that side's own read (view.headline /
            view.state — see sideRead in signals/view.ts), with the plan from
            the FULL result so it never depends on which level fields the view
            happened to null; for a flag cell the plan is 'Long > T · stop S'
            while a flag waits and 'Long E · stop S · +P%' while long. */}
        {view && (
          <div className="signal-strip" data-state={view.state}>
            <span className="signal-strip-headline" title={view.headline}>
              <b className="signal-strip-badge">{view.stateLabel}</b> {view.headline}
            </span>
            <span className="signal-strip-plan">{view.planText}</span>
          </div>
        )}
      </div>
    </section>
  );
}
