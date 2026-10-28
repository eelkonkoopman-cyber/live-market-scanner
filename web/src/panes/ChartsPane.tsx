import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useStore } from '../store';
import {
  start,
  getChart,
  watchCharts,
  getWatchlist,
  getChartGrid,
  persistChartGrid,
  watchTape,
  viewTiles,
  resyncScan,
} from '../ws';
import { StatusBar } from '../components/StatusBar';
import { MonitorChart } from '../components/MonitorChart';
import { DEFAULT_SIDE_BOXES } from '../components/SideBoxes';
import { WatchlistSidebar } from '../components/WatchlistSidebar';
import {
  AUTO_ENTRY_MS,
  AUTO_EXIT_MARGIN,
  AUTO_HOVER_RELEASE_MS,
  AUTO_SWAP_GAP_MS,
  activeAutoScan,
  autoStep,
  emptyMemo,
  rankSyms,
  resolveAutoScan,
  type AutoMemo,
} from '../autoFill';
import { isBlacklisted } from '../blacklist';
import type { SideBoxMetric } from '../types';
import { followedScans, newEntrants } from '../chartFollow';
import { subscribeScanAlerts } from '../signalFollow';

// ChartsPane is the multi-chart monitor window (?pane=charts) — meant to live
// on a spare monitor all day: a rows×cols grid of live MonitorChart cells plus
// a collapsible watchlist sidebar whose chips drag onto cells. Like every
// pop-out it's a fresh page load with its own store and WS connection.
//
// This pane owns ALL wire traffic for the grid: it declares the window's
// live-chart symbol set (chart.watch — the server merges every window's set
// into the ingest tap union), seeds history per symbol (chart.get), and
// persists watchlist/layout edits. Both the watchlist and the layout are
// server-authoritative + persisted, so they survive restarts and stay in sync
// across windows: watchlist.data echoes to everyone (sender included), while
// chartgrid.data only reaches OTHER windows (no sender self-echo).

const PRESETS = [
  { rows: 2, cols: 2, label: '2×2' },
  { rows: 2, cols: 3, label: '3×2' },
  { rows: 3, cols: 3, label: '3×3' },
] as const;

// A portrait monitor has height to spare and width to protect: 2×2/3×2/3×3
// (too few rows for a tall window) squeeze every cell into a tall, narrow
// aspect that's hard to read a candle chart in. Stacking one or two wide
// columns instead keeps each cell landscape-shaped regardless of how tall
// the window is.
const PORTRAIT_PRESETS = [
  { rows: 2, cols: 1, label: '1×2' },
  { rows: 3, cols: 1, label: '1×3' },
  { rows: 4, cols: 1, label: '1×4' },
  { rows: 3, cols: 2, label: '2×3' },
  { rows: 4, cols: 2, label: '2×4' },
] as const;

const PRESET_GROUPS = [
  { title: 'wide-monitor layouts', items: PRESETS },
  { title: 'stacked layouts for a portrait window', items: PORTRAIT_PRESETS },
] as const;

export function ChartsPane() {
  const feed = useStore((s) => s.feed);
  const chartSym = useStore((s) => s.chartSym);
  const grid = useStore((s) => s.chartGrid);
  const gridRev = useStore((s) => s.gridRev);
  const armed = useRef(false);
  // The symbol set this window last declared to the server — lets a grid
  // change send chart.get only for NEWLY assigned symbols, and makes the
  // sync effect a no-op on unrelated re-renders.
  const declaredRef = useRef<string[] | null>(null);
  const [showWl, setShowWl] = useState(true);

  useEffect(() => {
    start();
  }, []);

  // Clicking a chart cell (MonitorChart) calls openChart(sym) — the same
  // store action a scan-grid row click uses — which sets chartSym here. This
  // effect is what turns that click into the actual tape.watch the server
  // needs: it points the tape engine at the symbol, which (via the server's
  // single "lastSelected" target — see TapeWatchSet in cmd/scanner/main.go)
  // moves the TWS display-group link, the live L2 depth target, and the
  // IBKR live-borrow line to it, exactly like clicking a row anywhere else
  // in the app already does. Being merely ASSIGNED to a cell never does this
  // (chart.watch below only feeds the ingest chart tap) — only an explicit
  // click escalates a symbol to "the one thing the whole app is focused on".
  // Mirrors SymbolPanel/TapeReaderView's own watchTape effect; the cleanup
  // (a symbol change, feed drop, or this window closing) releases the link
  // rather than leaving a stale window as the last-selected target forever.
  useEffect(() => {
    if (!chartSym || feed !== 'up') return;
    watchTape(chartSym);
    return () => watchTape('');
  }, [chartSym, feed]);

  // The gainers/decliners lists under the watchlist (SideBoxes) are fed by
  // scans. Narrow this window's scan view to exactly those — an empty set
  // means "every scan" to the server, which is what a window that never sent
  // view.set was already getting — and ask for a full snapshot of each: a
  // tick is a delta, so a scan bound after the window connected has no
  // baseline until it's resynced. Keyed on feed too, since ws.ts only
  // re-arms the MAIN window's tiles after a reconnect.
  // Auto mode's source scan joins that set (while Auto is on) so its rows
  // stream into this window too.
  const boxScanKey = useStore((s) => {
    const boxes = s.chartGrid.boxes ?? DEFAULT_SIDE_BOXES;
    const ids = new Set(boxes.map((b) => b.scanId).filter((x): x is string => !!x));
    const autoId = activeAutoScan(s.chartGrid, boxes, s.scans);
    if (autoId) ids.add(autoId);
    for (const c of s.chartGrid.cells) if (c.followScan) ids.add(c.followScan); // followed scans stream here too
    return Array.from(ids).sort().join(',');
  });
  useEffect(() => {
    if (feed !== 'up') return;
    const ids = boxScanKey ? boxScanKey.split(',') : [];
    viewTiles(ids);
    ids.forEach(resyncScan);
  }, [boxScanKey, feed]);

  // --- Auto mode: fill the cells with the top gainers of a scan, calmly ---
  // The rules live in autoFill.ts (pure, tested); this is only the driver.
  // Note: the dwell clocks are per window and not persisted. Two charts
  // windows both running Auto could make slightly different swaps - one
  // charts window is the supported setup, so there is no cross-window
  // coordination beyond the usual layout sync.
  const scans = useStore((s) => s.scans);
  const auto = grid.auto;
  const autoOn = !!auto?.on;
  const autoMetric: SideBoxMetric = auto?.metric ?? 'chgDay';
  const autoScan = useMemo(
    () => resolveAutoScan(auto, grid.boxes ?? DEFAULT_SIDE_BOXES, scans),
    [auto, grid.boxes, scans],
  );
  const autoSlots = grid.cells.filter((c) => !c.pinned && !c.followScan).length; // N: the cells Auto may fill
  const autoMemo = useRef<AutoMemo>(emptyMemo());
  const autoInitial = useRef(false); // the next un-held step is the "just switched on" fill
  const autoWasOn = useRef(false);
  const autoLastRun = useRef(0);
  const hover = useRef({ over: false, lastMove: 0 });
  const [newKey, setNewKey] = useState(''); // comma list of cell indexes showing the NEW badge
  const newCells = useMemo(() => new Set(newKey ? newKey.split(',').map(Number) : []), [newKey]);

  const runAuto = useCallback(() => {
    const st = useStore.getState();
    const a = st.chartGrid.auto;
    if (!a?.on) return;
    const scanId = resolveAutoScan(a, st.chartGrid.boxes ?? DEFAULT_SIDE_BOXES, st.scans);
    if (!scanId) return;
    const now = Date.now();
    autoLastRun.current = now;
    const ranking = rankSyms(st.grids[scanId], a.metric, 'desc');
    // Hover-freeze (like gridHold in the scan grids): no changes while the
    // mouse is over the charts, released on leave or after 10 s of stillness.
    const h = hover.current;
    const held = h.over && now - h.lastMove < AUTO_HOVER_RELEASE_MS;
    const cells = st.chartGrid.cells;
    const r = autoStep({
      cells: cells.map((c) => c.sym),
      pinned: cells.map((c) => !!c.pinned || !!c.followScan), // followed cells are off-limits to Auto
      ranking,
      now,
      held,
      initial: autoInitial.current,
      memo: autoMemo.current,
    });
    autoMemo.current = r.memo;
    if (!held && ranking.length) autoInitial.current = false; // the initial fill has had its chance
    if (r.changed.length) st.applyAutoCells(r.cells, r.changed);
    setNewKey(Object.keys(r.memo.newAt).join(','));
  }, []);

  // Switching Auto on (or pointing it at another scan/metric) starts fresh
  // clocks; switching it ON also arms the immediate first fill of empty cells.
  useEffect(() => {
    autoMemo.current = emptyMemo();
    if (autoOn && !autoWasOn.current) autoInitial.current = true;
    autoWasOn.current = autoOn;
    if (!autoOn) setNewKey('');
  }, [autoOn, autoScan, autoMetric]);

  // Run every 2 s, and when the source scan's grid updates (throttled to 1/s;
  // the interval catches anything the throttle skipped). The subscription
  // keeps ChartsPane from re-rendering on every scan tick.
  useEffect(() => {
    if (!autoOn) return;
    const id = setInterval(runAuto, 2000);
    let prev = autoScan ? useStore.getState().grids[autoScan] : undefined;
    const unsub = useStore.subscribe((s) => {
      const g = autoScan ? s.grids[autoScan] : undefined;
      if (g === prev) return;
      prev = g;
      if (Date.now() - autoLastRun.current >= 1000) runAuto();
    });
    runAuto();
    return () => {
      clearInterval(id);
      unsub();
    };
  }, [autoOn, autoScan, autoMetric, runAuto]);

  // --- Follow scan (per cell): alerts of the chosen scan, or - for scans
  // without alerts - the newest row to ENTER the scan's results. ---
  useEffect(() => {
    const offAlerts = subscribeScanAlerts((a) => {
      const st = useStore.getState();
      if (followedScans(st.chartGrid.cells, st.scans).alertIds.includes(a.scanId)) st.followCell(a.scanId, a.sym);
    });
    const prev: Record<string, string[] | undefined> = {};
    const check = () => {
      const st = useStore.getState();
      const { entrantIds } = followedScans(st.chartGrid.cells, st.scans);
      for (const id of Object.keys(prev)) if (!entrantIds.includes(id)) delete prev[id];
      for (const id of entrantIds) {
        const order = st.grids[id]?.order;
        if (!order) continue;
        const fresh = newEntrants(prev[id], order).filter((x) => !isBlacklisted(st.blacklist, st.blacklistAt, x, Date.now()));
        prev[id] = order;
        if (fresh.length) st.followCell(id, fresh[0]);
      }
    };
    const unsub = useStore.subscribe(check);
    check();
    return () => {
      offAlerts();
      unsub();
    };
  }, []);

  // Deduped symbol set across all cells, in first-appearance order.
  const syms = useMemo(() => {
    const out: string[] = [];
    for (const c of grid.cells) if (c.sym && !out.includes(c.sym)) out.push(c.sym);
    return out;
  }, [grid.cells]);
  const symsKey = syms.join(',');

  // (Re-)establish everything whenever the feed comes up — including after a
  // reconnect, when the server has forgotten this connection's chart.watch
  // set and we may have missed other windows' watchlist/layout edits.
  useEffect(() => {
    if (feed === 'up' && !armed.current) {
      armed.current = true;
      getWatchlist();
      getChartGrid();
      const cur: string[] = [];
      for (const c of useStore.getState().chartGrid.cells) {
        if (c.sym && !cur.includes(c.sym)) cur.push(c.sym);
      }
      watchCharts(cur);
      // A reconnect re-arm is a fresh attempt, not a continuation of
      // whatever failed before — clear stale "couldn't load" state first.
      cur.forEach((sym) => useStore.getState().clearSymChartErr(sym));
      cur.forEach(getChart);
      declaredRef.current = cur;
    }
    if (feed === 'down') armed.current = false;
  }, [feed]);

  // Grid/cell changes (local edits AND server-applied layouts): re-declare
  // the whole watch set, seed history for newly assigned symbols only, and
  // drop cached bars for symbols no longer in any cell (bounded memory).
  useEffect(() => {
    const prev = declaredRef.current;
    if (prev && prev.join(',') === symsKey) return;
    watchCharts(syms);
    const had = new Set(prev ?? []);
    for (const s of syms) if (!had.has(s)) getChart(s);
    useStore.getState().pruneChartData(syms);
    declaredRef.current = syms;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [symsKey]);

  // Re-assert the watch set every minute. chart.watch is per-connection
  // server state with no ack: a frame lost in a boot/reconnect race (observed
  // once live: a charts window launched seconds after a scanner restart froze
  // permanently) would otherwise never be retried, because the symsKey effect
  // only fires on grid CHANGES. The frame is an idempotent whole-set replace,
  // so re-sending is free; Chromium's background-window timer throttling
  // (≥1/min) still lets this fire. Worst case after any lost declaration is
  // now a ~60s gap instead of a dead window.
  useEffect(() => {
    const id = setInterval(() => {
      const st = useStore.getState();
      if (st.feed !== 'up') return;
      const cur: string[] = [];
      for (const c of st.chartGrid.cells) if (c.sym && !cur.includes(c.sym)) cur.push(c.sym);
      watchCharts(cur);
    }, 60_000);
    return () => clearInterval(id);
  }, []);

  // Persist layout edits made in THIS window (gridRev only counts local
  // mutations — a layout adopted from another window never re-saves, so two
  // open charts windows can't ping-pong). Debounce lives in persistChartGrid.
  useEffect(() => {
    if (gridRev === 0) return;
    persistChartGrid(useStore.getState().chartGrid);
  }, [gridRev]);

  return (
    <div className="app pane-window">
      <StatusBar />
      <div className="charts-toolbar">
        <button
          className={`segbtn ${showWl ? 'on' : ''}`}
          onClick={() => setShowWl((v) => !v)}
          title={showWl ? 'hide the watchlist sidebar' : 'show the watchlist sidebar'}
        >
          ☰ Watchlist
        </button>
        {PRESET_GROUPS.map((group, gi) => (
          <Fragment key={group.title}>
            {gi > 0 && <span className="charts-toolbar-divider" />}
            <div className="seg" title={group.title}>
              {group.items.map((p) => (
                <button
                  key={p.label}
                  className={`segbtn ${grid.rows === p.rows && grid.cols === p.cols ? 'on' : ''}`}
                  onClick={() => useStore.getState().setGridPreset(p.rows, p.cols)}
                  title={`${p.cols} across × ${p.rows} down`}
                >
                  {p.label}
                </button>
              ))}
            </div>
          </Fragment>
        ))}
        <span className="charts-toolbar-divider" />
        <button
          className={`segbtn ${autoOn ? 'on' : ''}`}
          onClick={() => useStore.getState().setAutoFill({ on: !autoOn })}
          title={`Fill the charts with the top gainers of a scan — calmly: a new name needs ${AUTO_ENTRY_MS / 1000} s in the top ${autoSlots} to get a chart, a charted name is only replaced after it falls out of the top ${autoSlots + AUTO_EXIT_MARGIN} for a minute, at most one swap every ${AUTO_SWAP_GAP_MS / 1000} s, never while your mouse is over the charts. 📌 pins a chart.`}
        >
          ⚡ Auto
        </button>
        {autoOn && (
          <>
            <select
              className="monitor-sel"
              value={autoScan ?? ''}
              onChange={(e) => useStore.getState().setAutoFill({ scanId: e.target.value || null })}
              title="the scan whose top gainers fill the charts"
            >
              {!autoScan && <option value="">no scans yet</option>}
              {scans.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                </option>
              ))}
            </select>
            <div className="seg" title="rank by">
              {(
                [
                  { key: 'chgDay', label: 'Day %' },
                  { key: 'chg5m', label: '5m %' },
                ] as const
              ).map((m) => (
                <button
                  key={m.key}
                  className={`segbtn ${autoMetric === m.key ? 'on' : ''}`}
                  onClick={() => useStore.getState().setAutoFill({ metric: m.key })}
                >
                  {m.label}
                </button>
              ))}
            </div>
            <AutoHint scanId={autoScan} metric={autoMetric} />
          </>
        )}
      </div>
      <div className="charts-body">
        {showWl && <WatchlistSidebar />}
        <div
          className="charts-grid"
          onMouseEnter={() => (hover.current = { over: true, lastMove: Date.now() })}
          onMouseMove={() => (hover.current.lastMove = Date.now())}
          onMouseLeave={() => (hover.current = { over: false, lastMove: 0 })}
          style={{
            gridTemplateColumns: `repeat(${grid.cols}, minmax(0, 1fr))`,
            gridTemplateRows: `repeat(${grid.rows}, minmax(0, 1fr))`,
          }}
        >
          {grid.cells.map((_, i) => (
            <MonitorChart key={i} idx={i} isNew={autoOn && newCells.has(i)} />
          ))}
        </div>
      </div>
    </div>
  );
}

// Small inline status next to the Auto controls - subscribes to the source
// scan itself so ChartsPane doesn't re-render on every scan tick. Like the
// side boxes, it says so when the chosen scan doesn't carry the metric.
function AutoHint({ scanId, metric }: { scanId: string | null; metric: SideBoxMetric }) {
  const grid = useStore((s) => (scanId ? s.grids[scanId] : undefined));
  const label = metric === 'chgDay' ? 'Day %' : '5m %';
  let text = '';
  if (grid && grid.order.length > 0) {
    const carries = grid.order.some((sym) => typeof grid.rows[sym]?.[metric] === 'number');
    if (!carries) text = `this scan has no ${label} column`;
    else if (rankSyms(grid, metric, 'desc').length === 0) text = 'no gainers in this scan right now';
  }
  if (!text) return null;
  return <span className="muted charts-auto-hint">{text}</span>;
}
