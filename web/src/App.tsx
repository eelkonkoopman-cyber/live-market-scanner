import { useEffect } from 'react';
import { useStore } from './store';
import { start } from './ws';
import { useSize } from './useSize';
import { Splitter } from './components/Resize';
import { StatusBar } from './components/StatusBar';
import { ScanTabs } from './components/ScanTabs';
import { TileFrame } from './components/TileFrame';
import { NewsPanel } from './components/NewsPanel';
import { ScanEditor } from './components/ScanEditor';
import { KeyModal } from './components/KeyModal';
import { SymbolPanel } from './components/SymbolPanel';
import { ActivityLog } from './components/ActivityLog';
import { Toasts } from './components/Toasts';

const MIN_PANE = { leftW: 380, rightW: 420, gridH: 160, actH: 120, symH: 220, newsH: 140 };
const SPLIT_W = 8; // splitter hit-area, subtracted from the available-space clamp

export function App() {
  const openTiles = useStore((s) => s.openTiles);
  const editorOpen = useStore((s) => s.editorOpen);
  const keyModalOpen = useStore((s) => s.keyModalOpen);
  const openKeyModal = useStore((s) => s.openKeyModal);

  const pane = useStore((s) => s.pane);
  const setPane = useStore((s) => s.setPane);
  const persistLayout = useStore((s) => s.persistLayout);

  const [workspaceRef, wsSize] = useSize<HTMLDivElement>();
  const [leftColRef, leftColSize] = useSize<HTMLDivElement>();
  const [rightColRef, rightColSize] = useSize<HTMLDivElement>();

  useEffect(() => {
    start();
  }, []);

  // A boolean, not the status frame: the frame is replaced 1x/s (also when
  // nothing in it changed), and selecting it re-rendered the whole window —
  // news rows, activity log, tiles — every second.
  const needsKey = useStore((s) => s.status != null && !s.status.hasKey);

  const leftMax = wsSize.w > 0 ? Math.max(MIN_PANE.leftW, wsSize.w - MIN_PANE.rightW - SPLIT_W) : 2000;
  const gridMax = leftColSize.h > 0 ? Math.max(MIN_PANE.gridH, leftColSize.h - MIN_PANE.actH - SPLIT_W) : 2000;
  const symMax = rightColSize.h > 0 ? Math.max(MIN_PANE.symH, rightColSize.h - MIN_PANE.newsH - SPLIT_W) : 2000;

  // Re-clamp on every container resize (not just during a drag) — otherwise a
  // layout saved on a larger window (or a shrunk OS window) can push panes
  // into overflow. Only writes when a clamp actually changes something.
  useEffect(() => {
    if (wsSize.w === 0 || leftColSize.h === 0 || rightColSize.h === 0) return;
    const clamped: Partial<typeof pane> = {};
    const cLeft = Math.min(Math.max(pane.leftW, MIN_PANE.leftW), leftMax);
    const cGrid = Math.min(Math.max(pane.gridH, MIN_PANE.gridH), gridMax);
    const cSym = Math.min(Math.max(pane.symH, MIN_PANE.symH), symMax);
    if (cLeft !== pane.leftW) clamped.leftW = cLeft;
    if (cGrid !== pane.gridH) clamped.gridH = cGrid;
    if (cSym !== pane.symH) clamped.symH = cSym;
    if (Object.keys(clamped).length > 0) {
      setPane(clamped);
      persistLayout();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wsSize.w, leftColSize.h, rightColSize.h, leftMax, gridMax, symMax]);

  return (
    <div className="app">
      <StatusBar />
      {needsKey && (
        <button className="unlock keybanner" onClick={openKeyModal}>
          🔑 No API key set — click to paste your Massive key
        </button>
      )}
      <ScanTabs />
      <div className="main workspace" ref={workspaceRef}>
        <div
          className="ws-left"
          ref={leftColRef}
          style={pane.rightHidden ? { flex: '1 1 auto', minWidth: 0 } : { flex: `0 0 ${pane.leftW}px`, minWidth: 0 }}
        >
          {openTiles.length > 0 ? (
            <div className="tile-grid" style={{ flex: `0 0 ${pane.gridH}px`, minHeight: 0 }}>
              {openTiles.map((id) => (
                <TileFrame key={id} scanId={id} />
              ))}
            </div>
          ) : (
            <div className="empty" style={{ flex: `0 0 ${pane.gridH}px`, minHeight: 0 }}>
              No scan tiled — click a tab to open one.
            </div>
          )}
          <Splitter
            axis="y"
            value={pane.gridH}
            min={MIN_PANE.gridH}
            max={gridMax}
            onChange={(gridH) => setPane({ gridH })}
            onCommit={persistLayout}
          />
          <ActivityLog />
        </div>
        {!pane.rightHidden && (
          <Splitter
            axis="x"
            value={pane.leftW}
            min={MIN_PANE.leftW}
            max={leftMax}
            onChange={(leftW) => setPane({ leftW })}
            onCommit={persistLayout}
          />
        )}
        {/* Hidden with display:none rather than unmounted: SymbolPanel's
            effects (tape.watch for the TWS link, the cross-window follow
            listener) must keep running while the column is collapsed. */}
        <div className={`ws-right${pane.rightHidden ? ' collapsed' : ''}`} ref={rightColRef}>
          {/* The symbol panel is content-sized now that the chart and tape
              band live in their own windows — no splitter needed; the news
              panel takes every remaining pixel. */}
          <SymbolPanel style={{ flex: '0 0 auto', minHeight: 0 }} />
          <NewsPanel />
        </div>
      </div>
      {editorOpen && <ScanEditor />}
      {keyModalOpen && <KeyModal />}
      <Toasts />
    </div>
  );
}
