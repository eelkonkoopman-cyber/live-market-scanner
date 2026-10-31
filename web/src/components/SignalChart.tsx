import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import {
  createChart,
  makeInstance,
  makeDrawing,
  US_EQUITIES,
  type ChartApi,
  type ChartColors,
  type Candle,
  type Drawing,
  type DrawingsEvent,
  type PriceLine,
} from '../vendor/blackthorn-charts';
import { chartColors, chartFonts, chartLevelColor } from '../chartTheme';
import { useTheme } from '../useTheme';
import { useStore } from '../store';
import { fmtPx } from '../signals/shortFade';
import type { FlagBox } from '../signals/emaFlag';
import { chartMarkers, type SetupKind, type SignalView, type ViewTone } from '../signals/view';

// SignalChart is the single big chart in the Signal window (?pane=signal) —
// the engine lifecycle (create/teardown, fonts.ready, sessions, the indicator,
// price-scale auto/manual, theme re-tint, the renderedRef
// update-vs-setData heuristic, live-edge following) is copied VERBATIM from
// MonitorChart.tsx, one cell's worth, because a standing one-chart monitor
// window has exactly the same lifecycle needs as a grid cell — only the
// surrounding chrome (levels, markers and flag boxes drawn from the signal
// engine's own output instead of just a prev-close line) differs.
//
// Setup-agnostic (iteration 6): the chart no longer knows which engine produced
// what it draws. It is handed a SignalView (signals/view.ts) — events (the ones
// with a `marker` become engine markers), level lines tagged with a TONE (mapped
// to the live theme's colours here, on every theme change) and, for the 9 EMA
// flag setup, the flag boxes — plus the cell's SETUP, which picks the
// indicator: a 9 EMA (a light blue like the TWS line the trader watches) for
// `flag`, VWAP for `pullback`, the engine's own pairing for each.
//
// Same drag/drop ticker gate MonitorChart's cell uses
// (^[A-Z][A-Z0-9.\-]{0,9}$). Unlike MonitorChart (which mutates the grid
// store directly on drop), this component has no store of its own to write
// to — the Signal window's assigned symbol is SignalPane's local React
// state, following/pin toggle and all — so a validated drop is handed back
// up via `onSymbolDrop` instead. This callback isn't part of the engine
// lifecycle being mirrored; it's the one piece of chrome this chart owns
// that MonitorChart's cell doesn't (a monitor cell has no follow/pin
// concept), so it lives here rather than being bolted onto SignalPane's own
// wrapper — SignalChart's root IS the flex-1 chart pane (`.signal-chart`),
// exactly like MonitorChart's `<section>` is the drop target for its cell.

const SYM_DROP_RE = /^[A-Z][A-Z0-9.\-]{0,9}$/;

// The flag setup's 9 EMA line: a light blue, like the user's own TWS chart.
const FLAG_EMA_COLOR = '#4aa3ff';

// A view line's tone -> this theme's colour (see ViewTone in signals/view.ts).
function toneColor(tone: ViewTone, colors: ChartColors): string {
  switch (tone) {
    case 'entry':
      return colors.accent;
    case 'stop':
    case 'down':
      return colors.downBody;
    case 'up':
      return colors.upBody;
    case 'ema':
      return FLAG_EMA_COLOR;
    case 'level':
      return chartLevelColor();
  }
}

// A flag box's outline/fill colour by outcome: a live flag in the accent, one
// that broke (a taken or capped break) in the gain colour, an EARLY break, a
// faded flag or a STALE one (outlived maxFlagBars: a range, not a flag) muted,
// a failed one (closed under the EMA) in the loss colour.
// The engine fills a rect at 8% alpha of this colour.
function boxColor(outcome: FlagBox['outcome'], colors: ChartColors): string {
  switch (outcome) {
    case 'active':
      return colors.accent;
    case 'broke':
      return colors.upBody;
    case 'failed':
      return colors.downBody;
    case 'early':
    case 'faded':
    case 'stale':
      return colors.axisText;
  }
}

// The flag boxes as LOCKED rect drawings (locked: rendered, never draggable or
// resizable). Ids are deterministic (index + start) so a re-apply on every tick
// keeps the same objects instead of churning hover state; `makeDrawing`'s own
// ids are time-based and would differ every call.
function boxDrawings(boxes: FlagBox[], colors: ChartColors): Drawing[] {
  return boxes.map((b, i) => ({
    ...makeDrawing(
      'rect',
      [
        { time: b.startTs, price: b.high },
        { time: b.endTs, price: b.low },
      ],
      { color: boxColor(b.outcome, colors), lineWidth: 1, lineStyle: b.outcome === 'faded' || b.outcome === 'stale' ? 'dashed' : 'solid' },
    ),
    id: `flag-${i}-${b.startTs}`,
    locked: true,
  }));
}

export interface SignalChartProps {
  sym: string;
  candles: Candle[];
  tfMin: number;
  prevClose: number | null;
  setup: SetupKind;
  emaPeriod: number; // the flag setup's EMA length (FlagParams.emaPeriod); ignored for pullback
  view: SignalView | null;
  loading: ReactNode | null;
  onSymbolDrop: (sym: string) => void;
}

export function SignalChart({ sym, candles, tfMin, prevClose, setup, emaPeriod, view, loading, onSymbolDrop }: SignalChartProps) {
  const theme = useTheme(); // re-render + re-color on ANY window's theme change
  const elRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<ChartApi | null>(null);
  const [dragOver, setDragOver] = useState(false);
  const dragDepth = useRef(0); // dragenter/dragleave fire per child; count so the highlight doesn't flicker
  const hasSym = !!sym;

  // Price (Y) axis auto/manual state, mirrored from the engine via its own
  // subscribePriceScaleChange event — see MonitorChart's identical comment.
  const [autoScale, setAutoScaleState] = useState(true);

  // Create the chart when the pane gains a symbol, tear down when it empties.
  // Keyed on hasSym (not sym) so switching symbols reuses the instance — the
  // renderedRef heuristic below turns that into a setData, not a rebuild.
  useEffect(() => {
    if (!hasSym || !elRef.current) return;
    const chart = createChart(elRef.current, {
      chartType: 'candles',
      showVolume: true,
      showGrid: true,
      showLegend: false,
      showSessionBands: true,
      timezone: 'America/New_York',
      colors: chartColors(),
      fonts: chartFonts(),
    });
    // Re-measure once real web fonts are ready — see MonitorChart's comment
    // (the axis gutter/crosshair chip must not reflow under the trader).
    void document.fonts?.ready.then(() => {
      if (chartRef.current === chart) chart.applyOptions({ fonts: chartFonts() });
    });
    try {
      chart.sessions.apply(US_EQUITIES);
    } catch {
      /* session shading is best-effort */
    }
    chartRef.current = chart;
    setAutoScaleState(chart.priceScale().isAutoScale());
    const onPriceScaleChange = () => setAutoScaleState(chart.priceScale().isAutoScale());
    chart.subscribePriceScaleChange(onPriceScaleChange);
    // The flag boxes are drawings, and the engine selects a drawing the moment
    // it is pressed (showing handles and anchor chips) even when it is locked.
    // These are read-only annotations, never something to pick up, so drop the
    // selection straight away. (A press INSIDE a box is still swallowed by the
    // engine's hit test — it does not start a pan — which is its own behaviour
    // for any drawing, locked or not.)
    const onDrawingsEvent = (e: DrawingsEvent) => {
      if (e.type === 'select' && e.id) chart.drawings.select(null);
    };
    chart.drawings.subscribe(onDrawingsEvent);
    return () => {
      chart.drawings.unsubscribe(onDrawingsEvent);
      chart.unsubscribePriceScaleChange(onPriceScaleChange);
      chart.remove();
      chartRef.current = null;
    };
  }, [hasSym]);

  // The indicator follows the cell's setup: the 9 EMA for the flag engine
  // (period from its params, a light blue like the user's TWS line — the
  // instance's `value` series style takes a colour override), VWAP for the
  // pullback engine. Declared AFTER the create effect so, on the commit that
  // creates the chart, the chart already exists when this runs (effects of one
  // component run in declaration order); re-runs on a setup or period change,
  // and replaces the previous set outright.
  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;
    if (setup === 'flag') {
      const ema = makeInstance('ema', { period: Math.max(1, Math.floor(emaPeriod) || 9) });
      if (ema) {
        ema.styleOverrides = { value: { color: FLAG_EMA_COLOR, lineWidth: 2 } };
        // VWAP alongside the 9 EMA (user, 2026-10-29: "add VWAP to the chart").
        const vw = makeInstance('vwap', {});
        chart.setIndicators(vw ? [ema, vw] : [ema]);
        return;
      }
    }
    const vwap = makeInstance('vwap', {});
    chart.setIndicators(vwap ? [vwap] : []);
  }, [hasSym, setup, emaPeriod]);

  // Live-edge following — see MonitorChart's identical comment: any pan/zoom
  // turns auto-scroll off until the view is reset, so this re-anchors the
  // view when a new bar would push the live edge off screen, and drives the
  // "go to latest" button while it's out of view.
  const [atLatest, setAtLatest] = useState(true);
  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;
    const onViewChange = () => setAtLatest(chart.timeScale().isAtLatest());
    onViewChange();
    chart.subscribeVisibleRangeChange(onViewChange);
    return () => chart.unsubscribeVisibleRangeChange(onViewChange);
  }, [hasSym]);

  // Re-tint an EXISTING chart in place when the theme changes (this window's
  // own picker, or another window's) — see MonitorChart's identical comment.
  useEffect(() => {
    chartRef.current?.applyOptions({ colors: chartColors() });
  }, [theme]);

  // Render data — MonitorChart's update-vs-setData heuristic, copied
  // verbatim (same field names, same reconnect-backfill branch, same
  // seriesChanged auto-scale reset): a full setData resets the user's
  // pan/zoom, so it's reserved for a genuinely new series (symbol switch,
  // timeframe switch, or the snapshot landing); live ticks that only
  // rewrite/append the trailing candle go through update().
  const renderedRef = useRef<{ count: number; firstTs: number; tf: number; sym: string }>({
    count: 0,
    firstTs: 0,
    tf: 0,
    sym: '',
  });
  useEffect(() => {
    const chart = chartRef.current;
    if (!chart || candles.length < 2) {
      // Reset the growth-tracking fields only — NOT sym/tf. See MonitorChart's
      // identical comment: a cell/pane can sit here a while (freshly assigned,
      // still loading) with the price axis already toggled to Manual against a
      // still-empty chart; leaving sym/tf at their last REAL-render values
      // guarantees the first real setData for a newly-loading symbol is always
      // seen as a series change below.
      renderedRef.current = { ...renderedRef.current, count: 0, firstTs: 0 };
      return;
    }
    const prev = renderedRef.current;
    const isSameSeries =
      prev.count > 0 &&
      prev.sym === sym &&
      prev.tf === tfMin &&
      prev.firstTs === candles[0].ts &&
      candles.length >= prev.count &&
      candles.length - prev.count <= 1;

    if (isSameSeries) {
      const view = chart.timeScale();
      const followLive = view.isAtLatest();
      chart.update(candles[candles.length - 1]);
      if (followLive && !view.isAtLatest()) view.scrollToLatest();
    } else {
      // A stale manual price pin from an unrelated series must never carry
      // over — see MonitorChart's identical comment for why this reset has
      // to live HERE, gated on the series actually changing (not every
      // setData path, so a same-symbol reconnect backfill leaves a manual
      // pin alone).
      const seriesChanged = prev.sym !== sym || prev.tf !== tfMin;
      if (seriesChanged) {
        chart.priceScale().setAutoScale(true);
        chart.setData(candles);
        chart.timeScale().resetView();
      } else {
        const view = chart.timeScale();
        const followLive = view.isAtLatest();
        view.scrollBy(0);
        chart.setData(candles);
        if (followLive) view.scrollToLatest();
      }
    }
    renderedRef.current = { count: candles.length, firstTs: candles[0].ts, tf: tfMin, sym };
  }, [candles, tfMin, sym]);

  // Markers — every event that carries one (the pullback's entry/entryVwap/
  // vwapReject/target/stopped/invalidated, the flag's LONG (FLAG) / ADD (FLAG) /
  // EXIT, and the flag's 9 EMA SLAM: a red ▼ at the slam bar's close, which
  // joins an EXIT ▼ on the same bar — see chartMarkers in signals/view.ts),
  // re-set after any setData.
  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;
    chart.setMarkers(chartMarkers(view?.events ?? []));
  }, [view?.events, candles, theme]);

  // Price lines — the prior close (when there is a real one) plus whatever
  // levels the view carries, each toned to this theme's colours. For the
  // pullback setup those are the lines this chart used to build inline: HOD,
  // the short stop and trigger, the base, the lone lower high (only while there
  // is no stop yet), and the long side's higher low / trigger / stop (see
  // pullbackView in signals/view.ts — which side's levels exist is its filter,
  // not this component's). For the flag setup: 'Long > T' while a flag waits,
  // 'Stop S' while long. Keyed on candles too (mirrors MonitorChart) so the
  // lines survive a setData.
  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;
    const colors = chartColors();
    const lines: PriceLine[] = [];
    if (prevClose && Number.isFinite(prevClose) && prevClose > 0) {
      lines.push({ price: prevClose, color: chartLevelColor(), label: `Prev Close ${fmtPx(prevClose)}`, lineStyle: 'dashed' });
    }
    for (const l of view?.lines ?? []) {
      lines.push({ price: l.price, color: toneColor(l.tone, colors), label: l.label, lineStyle: l.style });
    }
    chart.setPriceLines(lines);
  }, [view?.lines, prevClose, candles, theme]);

  // Flag boxes — locked 'rect' drawings (the engine's own drawings layer),
  // one per flag the view carries; empty for a pullback cell, which clears any
  // left behind by a setup switch. `drawings.set` hydrates without repainting
  // (it is the persistence path, no event), so setVisible(true) — which does
  // invalidate the drawings layer, and is a no-op otherwise — forces the
  // repaint. Re-applied after any setData and on a theme change (keyed on
  // candles and theme like the markers), so the boxes survive a series swap
  // and re-tint with the theme.
  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;
    chart.drawings.set(setup === 'flag' && view ? boxDrawings(view.boxes, chartColors()) : []);
    chart.drawings.setVisible(true);
  }, [view?.boxes, setup, candles, theme]);

  return (
    <div
      className={`signal-chart${dragOver ? ' dropzone' : ''}`}
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
        if (SYM_DROP_RE.test(s)) onSymbolDrop(s);
      }}
    >
      {hasSym && (
        <button
          type="button"
          className="signal-scale segbtn"
          onClick={() => chartRef.current?.priceScale().setAutoScale(!autoScale)}
          title={autoScale ? 'price axis: auto-fit (click to pin the current range)' : 'price axis: manually pinned (click to resume auto-fit)'}
        >
          {autoScale ? 'Auto' : 'Manual'}
        </button>
      )}
      <div className="signal-canvas" ref={elRef}>
        {loading}
      </div>
      {/* A sibling of the engine container, not a child — see MonitorChart's
          identical comment: the engine captures the pointer on pointerdown
          inside its container, which would swallow this button's click. */}
      {hasSym && candles.length >= 2 && !atLatest && (
        <button
          type="button"
          className="signal-latest"
          onClick={() => chartRef.current?.timeScale().scrollToLatest()}
          title="Go to latest bar (the chart follows new bars again)"
          aria-label="Go to latest bar"
        >
          »
        </button>
      )}
    </div>
  );
}
