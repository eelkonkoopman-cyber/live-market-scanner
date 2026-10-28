import { useEffect, useMemo, useRef, useState } from 'react';
import {
  createChart,
  makeInstance,
  US_EQUITIES,
  type ChartApi,
  type PriceLine,
} from '../vendor/blackthorn-charts';
import { useStore } from '../store';
import { getChart, selectSymbol } from '../ws';
import { aggregate } from '../chartAgg';
import type { ChartTf } from '../types';
import { chartColors, chartFonts, chartLevelColor } from '../chartTheme';
import { useTheme } from '../useTheme';

// The trend EMA drawn on every monitor cell (user request, 2026-10-02).
const EMA_PERIOD = 9;

// MonitorChart is one cell of the charts monitor window (?pane=charts): a
// compact Blackthorn chart (candles + volume + VWAP + prev-close line) for
// whichever symbol is assigned to this cell, at the cell's own timeframe.
// Assignment happens three ways: drag a watchlist chip onto the cell, pick
// from the header dropdown, or type a symbol into the empty-cell placeholder.
// The pane (ChartsPane) owns all wire traffic — this component only reads the
// store (barsBySym/prevCloseBySym) and mutates the grid via store actions.

const TFS: readonly ChartTf[] = ['1m', '5m', '15m'];
const TF_MIN: Record<ChartTf, number> = { '1m': 1, '5m': 5, '15m': 15 };

const dp = (v: number) => (Math.abs(v) < 1 ? v.toFixed(4) : v.toFixed(2));

export function MonitorChart({ idx, isNew = false }: { idx: number; isNew?: boolean }) {
  const cell = useStore((s) => s.chartGrid.cells[idx]);
  const sym = cell?.sym ?? null;
  const tf: ChartTf = cell?.tf ?? '1m';
  const autoOn = useStore((s) => !!s.chartGrid.auto?.on); // charts-window Auto mode (autoFill.ts)
  const pinned = !!cell?.pinned;
  const followScan = cell?.followScan ?? '';
  const scans = useStore((s) => s.scans);
  const followName = scans.find((x) => x.id === followScan)?.name;
  const bars = useStore((s) => (sym ? s.barsBySym[sym] : undefined));
  const prevClose = useStore((s) => (sym ? s.prevCloseBySym[sym] : undefined));
  const err = useStore((s) => (sym ? s.chartErrBySym[sym] : undefined));
  const watchlist = useStore((s) => s.watchlist);
  const chartSym = useStore((s) => s.chartSym); // set by clicking any cell's chart, see isLinked below
  const theme = useTheme(); // re-render + re-color on ANY window's theme change

  const elRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<ChartApi | null>(null);
  const [dragOver, setDragOver] = useState(false);
  const dragDepth = useRef(0); // dragenter/dragleave fire per child; count so the highlight doesn't flicker
  const [typed, setTyped] = useState('');
  // Price (Y) axis auto/manual state, mirrored from the engine via its own
  // subscribePriceScaleChange event — NOT polled. The engine has no signal
  // for time-scale mode today either, but price needs one because dragging
  // or wheel-zooming the price axis (inside the canvas, invisible to React)
  // can flip auto->manual on its own.
  const [autoScale, setAutoScaleState] = useState(true);

  const hasSym = sym != null;
  // True while THIS cell's symbol is the one a chart click most recently
  // selected in this window — drives the TWS/depth/borrow link (see the
  // watchTape effect in ChartsPane.tsx) and its own header badge/border below.
  const isLinked = hasSym && chartSym === sym;
  const tfMin = TF_MIN[tf];
  const candles = useMemo(() => aggregate(bars ?? [], tfMin), [bars, tfMin]);

  // Cheap header derivation straight from the live 1-min bars — no row.get,
  // no grid lookup (this window tiles no scan).
  const last = bars && bars.length ? bars[bars.length - 1].c : null;
  const dayPct = last != null && prevClose ? ((last - prevClose) / prevClose) * 100 : null;

  // Create the chart when the cell gains a symbol, tear down when it empties.
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
      // showLastPrice left at the engine default (on): the dashed last-price
      // line + axis chip is wanted here.
    });
    // The chart is built with whatever --mono/--sans resolve to right now,
    // which can still be the browser's pre-webfont fallback metrics on a
    // cold load. Re-measure once the real faces are ready so the axis gutter
    // and crosshair chip settle at their final width instead of reflowing
    // under the trader mid-session.
    void document.fonts?.ready.then(() => {
      if (chartRef.current === chart) chart.applyOptions({ fonts: chartFonts() });
    });
    try {
      chart.sessions.apply(US_EQUITIES);
    } catch {
      /* session shading is best-effort */
    }
    // VWAP plus the 9 EMA on every cell. Indicators compute on the candles the
    // cell is showing, so the EMA follows the 1m / 5m / 15m switch.
    const indicators = [makeInstance('vwap', {}), makeInstance('ema', { period: EMA_PERIOD })].filter(
      (x): x is NonNullable<typeof x> => x !== null,
    );
    if (indicators.length) chart.setIndicators(indicators);
    chartRef.current = chart;
    setAutoScaleState(chart.priceScale().isAutoScale());
    const onPriceScaleChange = () => setAutoScaleState(chart.priceScale().isAutoScale());
    chart.subscribePriceScaleChange(onPriceScaleChange);
    return () => {
      chart.unsubscribePriceScaleChange(onPriceScaleChange);
      chart.remove();
      chartRef.current = null;
    };
  }, [hasSym]);

  // Live-edge following. The engine auto-scrolls on update() only until the
  // chart is first panned or zoomed: any drag, wheel zoom or pinch, even a
  // click with a pixel of mouse wobble, turns it off until the view is reset.
  // A monitor cell is watched all day and nudged constantly, so the render
  // effect below re-anchors the view whenever a new bar would push the live
  // edge off screen. This flag drives the "go to latest" button shown while
  // the latest bar is out of view, the same control AfterBell's chart offers.
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
  // own picker, or another window's — useTheme() re-renders either way).
  // No need to recreate the instance: applyOptions({ colors }) Object.assigns
  // the new palette, repaints the container background, and invalidates the
  // canvas (see vendor/blackthorn-charts/chart.ts's applyOptions). Skipped
  // when there's no chart yet (empty cell) — nothing to re-paint.
  useEffect(() => {
    chartRef.current?.applyOptions({ colors: chartColors() });
  }, [theme]);

  // Render data — SymbolPanel's update-vs-setData heuristic, per cell: a full
  // setData resets the user's pan/zoom, so it's reserved for a genuinely new
  // series (symbol switch, timeframe switch, or the snapshot landing); live
  // ticks that only rewrite/append the trailing candle go through update().
  const renderedRef = useRef<{ count: number; firstTs: number; tf: number; sym: string }>({
    count: 0,
    firstTs: 0,
    tf: 0,
    sym: '',
  });
  useEffect(() => {
    const chart = chartRef.current;
    if (!chart || candles.length < 2) {
      // Reset the growth-tracking fields only — NOT sym/tf. A cell can sit
      // in this branch for a while (freshly assigned, still loading: bars is
      // undefined or under 2 candles) with the price axis already toggled to
      // Manual against a still-empty chart (engine default range {0,1}); if
      // sym/tf were stamped here, the seriesChanged check below would see
      // prev.sym already equal to the new symbol once real data lands and
      // skip the auto-scale reset, leaving that bogus {0,1} pin applied to
      // real prices. Leaving sym/tf at whatever they were after the last
      // REAL render (or '' / 0 from this ref's initial value, pre-mount)
      // guarantees the first real setData for a newly-loading symbol is
      // always seen as a series change.
      renderedRef.current = { ...renderedRef.current, count: 0, firstTs: 0 };
      return;
    }
    const prev = renderedRef.current;
    // "Same series" means live ticks only ever rewrote or appended the
    // TRAILING candle (growth of at most one). A bigger jump with the same
    // firstTs is a reconnect re-fetch that back-filled gap minutes (the pane
    // re-sends chart.get on every re-arm) — update(last) would silently skip
    // the recovered candles, so that case must take the full setData path.
    const isSameSeries =
      prev.count > 0 &&
      prev.sym === sym &&
      prev.tf === tfMin &&
      prev.firstTs === candles[0].ts &&
      candles.length >= prev.count &&
      candles.length - prev.count <= 1;

    if (isSameSeries) {
      // Follow the live edge whenever the latest bar was in view before this
      // tick. Once the chart has been panned or zoomed the engine leaves the
      // view alone, so a new bar would slide the live edge off screen;
      // scrollToLatest() brings it back at the current zoom and hands
      // following back to the engine. Someone reading history, with the
      // latest bar out of view, is left where they are.
      const view = chart.timeScale();
      const followLive = view.isAtLatest();
      chart.update(candles[candles.length - 1]);
      if (followLive && !view.isAtLatest()) view.scrollToLatest();
    } else {
      // A stale manual price pin from an unrelated series must never carry
      // over: chart.setData() itself preserves whatever price-scale pin was
      // in effect (by design — a reconnect backfill of the SAME symbol/tf
      // must not silently clear the user's pin, see
      // vendor/blackthorn-charts/chart.ts's "silent auto-scale reset"
      // guard). So the reset has to happen HERE, and only when the series
      // identity actually changed (symbol or timeframe), not on every
      // setData path — a same-symbol backfill reload (bigger jump, same
      // firstTs is impossible, but same sym/tf) must leave a manual pin
      // alone.
      const seriesChanged = prev.sym !== (sym ?? '') || prev.tf !== tfMin;
      if (seriesChanged) {
        chart.priceScale().setAutoScale(true);
        chart.setData(candles);
        // A genuinely new series opens at the engine's default view (default
        // zoom, latest bar right-anchored) via resetView() — NOT the old
        // fitContent(), which squeezed a whole two sessions' worth of bars
        // into one small monitor cell.
        chart.timeScale().resetView();
      } else {
        // Same series re-fetched after a reconnect: keep the trader's zoom,
        // and keep following the live edge if they were watching it. The
        // engine's setData keeps the current view only for a chart that has
        // been panned or zoomed since its last reset; a chart that is merely
        // following would snap back to the default zoom. scrollBy(0) marks
        // the view as user-set without moving it.
        const view = chart.timeScale();
        const followLive = view.isAtLatest();
        view.scrollBy(0);
        chart.setData(candles);
        if (followLive) view.scrollToLatest();
      }
    }
    renderedRef.current = { count: candles.length, firstTs: candles[0].ts, tf: tfMin, sym: sym ?? '' };
  }, [candles, tfMin, sym]);

  // Prev-close level line, when known. Keyed on candles too (mirrors
  // SymbolPanel) so the line survives a setData.
  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;
    const lines: PriceLine[] =
      prevClose && Number.isFinite(prevClose) && prevClose > 0
        ? [{ price: prevClose, color: chartLevelColor(), label: `Prev Close ${dp(prevClose)}`, lineStyle: 'dashed' }]
        : [];
    chart.setPriceLines(lines);
  }, [prevClose, hasSym, candles, theme]);

  // Dropdown options: the watchlist, plus the current symbol if it was
  // assigned outside the list (typed/dropped), so the select stays valid.
  const options = useMemo(() => {
    if (sym && !watchlist.includes(sym)) return [sym, ...watchlist];
    return watchlist;
  }, [watchlist, sym]);

  const assign = (s: string | null) => useStore.getState().assignCell(idx, s);

  return (
    <section
      className={`monitor-chart${dragOver ? ' dropzone' : ''}${isLinked ? ' linked' : ''}`}
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
        // Drops can come from the sidebar chips OR a grid row in another
        // window; gate the payload so arbitrary dragged text can't become a
        // phantom "symbol" that chart.gets forever.
        const s = e.dataTransfer.getData('text/plain').trim().toUpperCase();
        if (/^[A-Z][A-Z0-9.\-]{0,9}$/.test(s)) assign(s);
      }}
    >
      <div className="monitor-head">
        <select
          className="monitor-sel"
          value={sym ?? ''}
          onChange={(e) => assign(e.target.value || null)}
          title="assign a watchlist symbol to this chart ('—' clears it)"
        >
          <option value="">—</option>
          {options.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <select
          className="monitor-sel"
          value={followName ? followScan : ''}
          onChange={(e) => useStore.getState().setCellFollow(idx, e.target.value || null)}
          title="Follow scan: this chart switches to each new alert / entrant of the chosen scan. Picking a symbol by hand or pinning turns it Off. Auto skips followed charts."
        >
          <option value="">Follow: Off</option>
          {scans.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
            </option>
          ))}
        </select>
        {followName && (
          <span className="monitor-new-badge" title={`Following ${followName}`}>
            {followName}
          </span>
        )}
        {autoOn && (
          <button
            className={`iconbtn tile-btn monitor-pin${pinned ? ' on' : ''}`}
            onClick={() => useStore.getState().toggleCellPin(idx)}
            title={pinned ? 'Pinned: Auto will never replace this chart (click to unpin)' : 'Pin: Auto will never replace this chart'}
            aria-pressed={pinned}
          >
            📌
          </button>
        )}
        {autoOn && isNew && !pinned && hasSym && (
          <span className="monitor-new-badge" title="Auto just put this name on the chart">
            NEW
          </span>
        )}
        <div className="seg">
          {TFS.map((t) => (
            <button
              key={t}
              className={`segbtn ${tf === t ? 'on' : ''}`}
              onClick={() => useStore.getState().setCellTf(idx, t)}
            >
              {t}
            </button>
          ))}
        </div>
        <span className="spacer" />
        {hasSym && (
          <button
            className={`segbtn ${!autoScale ? 'on' : ''}`}
            onClick={() => chartRef.current?.priceScale().setAutoScale(!autoScale)}
            title={autoScale ? 'price axis: auto-fit (click to pin the current range)' : 'price axis: manually pinned (click to resume auto-fit)'}
          >
            {autoScale ? 'Auto' : 'Manual'}
          </button>
        )}
        {last != null && <span className="monitor-last">{dp(last)}</span>}
        {dayPct != null && (
          <span className={`monitor-pct ${dayPct >= 0 ? 'pos' : 'neg'}`}>
            {dayPct >= 0 ? '+' : ''}
            {dayPct.toFixed(2)}%
          </span>
        )}
        {isLinked && (
          <span
            className="monitor-tws-badge"
            title="Linked: this chart is driving TWS, the live Level 2 depth target, and the live borrow line"
          >
            TWS
          </span>
        )}
      </div>
      {hasSym ? (
        <div className="monitor-body">
          <div
            className="monitor-canvas"
            ref={elRef}
            onClick={() => {
              // Click-to-link (user: "If i click on the chart i want the
              // ticker of that chart open in TWS"): the same openChart(sym)
              // a scan-grid row click already uses, so every window that
              // follows selections (main panel's Follow toggle, a tape
              // pop-out) also picks this symbol up — the ChartsPane-level
              // effect on chartSym is what actually sends tape.watch.
              // selectSymbol also re-sends tape.watch when this chart was
              // already the selection, since another window may have moved
              // TWS elsewhere since.
              if (sym) selectSymbol(sym);
            }}
            title={`Click to open ${sym} in TWS (Level 2 depth and the live borrow line follow it too)`}
          >
            {!bars &&
              (err ? (
                <div className="chartmodal-loading muted" style={{ flexDirection: 'column', gap: 8 }}>
                  <span>Couldn&rsquo;t load {sym} — {err}</span>
                  <button
                    className="mini"
                    onClick={() => {
                      if (!sym) return;
                      useStore.getState().clearSymChartErr(sym);
                      getChart(sym);
                    }}
                  >
                    Retry
                  </button>
                </div>
              ) : (
                <div className="chartmodal-loading muted">Loading {sym}…</div>
              ))}
            {bars && bars.length < 2 && (
              <div className="chartmodal-loading muted">No intraday data for {sym} yet.</div>
            )}
          </div>
          {/* A sibling of the engine's container, not a child: the engine
              captures the pointer on pointerdown inside its container, which
              would swallow this button's click. */}
          {bars && bars.length >= 2 && !atLatest && (
            <button
              type="button"
              className="monitor-latest"
              onClick={() => chartRef.current?.timeScale().scrollToLatest()}
              title="Go to latest bar (the chart follows new bars again)"
              aria-label="Go to latest bar"
            >
              »
            </button>
          )}
        </div>
      ) : (
        <div className="monitor-empty">
          <span className="muted">drop a symbol here</span>
          <input
            value={typed}
            placeholder="or type…"
            onChange={(e) => setTyped(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && typed.trim()) {
                assign(typed);
                setTyped('');
              }
            }}
          />
        </div>
      )}
    </section>
  );
}
