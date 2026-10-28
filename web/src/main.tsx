import React from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';
import { ScanPane } from './panes/ScanPane';
import { TapePane } from './panes/TapePane';
import { ChartsPane } from './panes/ChartsPane';
import { ReviewPane } from './panes/ReviewPane';
import { SignalPane } from './panes/SignalPane';
import { DayLogPane } from './panes/DayLogPane';
import { OrderPane } from './panes/OrderPane';
import { useStore, suppressTileSeeding } from './store';
import { hearOnlyViewedScans } from './ws';
import { createChart } from './vendor/blackthorn-charts';
import { initTheme } from './theme';
import '@fontsource-variable/plus-jakarta-sans';
import './styles.css';

// Re-apply the persisted theme (index.html's inline script already painted
// <html> before this module even loaded, but this call also seeds this
// module's OWN `current` variable and installs the cross-window `storage`
// listener) and start listening for other windows changing it. Must run
// before createRoot below so every component's first render — including
// useTheme() — sees the right value from the start, not a flash of warm
// followed by a re-render.
initTheme();

// Expose the store + chart factory for local debugging/inspection (single-user
// local app).
(window as unknown as { __store: typeof useStore }).__store = useStore;
(window as unknown as { __createChart: typeof createChart }).__createChart = createChart;

// Pop-out windows are dispatched client-side by URL: the Go server has no
// real routing, it serves this same bundle for every path (SPA fallback), so
// which tree renders is decided here, before mounting. Each pop-out is a
// fresh page load — its own store and WebSocket connection — so independent
// per-window scan/symbol selection falls out for free with zero cross-window
// messaging.
const params = new URLSearchParams(location.search);
const pane = params.get('pane');

let tree: React.ReactElement;
if (pane === 'scan') {
  const id = params.get('id') ?? '';
  // Force this window's tile set to exactly this one scan, overriding
  // whatever openTiles a DIFFERENT window (main or another pop-out) last
  // persisted to the SAME localStorage key. suppressTileSeeding stops this
  // tab's OWN first scan.list from auto-seeding a different default tile —
  // each tab runs a fresh module instance, so that seeding guard doesn't
  // otherwise know a pop-out already made its choice.
  suppressTileSeeding();
  useStore.setState({ openTiles: id ? [id] : [], activeId: id || null });
  tree = <ScanPane scanId={id} />;
} else if (pane === 'tape') {
  const sym = params.get('sym') ?? '';
  // Deliberately empty: this window tiles no scan, so the server's view.set
  // default (empty = "wants all") passively feeds every scan's ticks into
  // this window's own grids cache, which SymbolPanel's row lookup searches.
  suppressTileSeeding();
  useStore.setState({ openTiles: [], activeId: null });
  // ...but it plays and speaks NO scan alert: the tape reader stays open all
  // day, and an every-scan alert feed made it say every alert a second time
  // behind the window that shows the scan (ws.ts hearOnlyViewedScans).
  hearOnlyViewedScans();
  tree = <TapePane sym={sym} />;
} else if (pane === 'charts') {
  // The multi-chart monitor window (third-monitor layout). Tiles no scan;
  // ChartsPane narrows view.set to whatever scans its gainers/decliners
  // lists use (none picked = the server's every-scan default, like the tape
  // pop-out). Its watchlist and grid layout come from the server
  // (persisted, shared across windows).
  suppressTileSeeding();
  useStore.setState({ openTiles: [], activeId: null });
  hearOnlyViewedScans(); // the alerts of its side boxes' scans only (none picked = none)
  tree = <ChartsPane />;
} else if (pane === 'review') {
  // The morning-review pop-out: past-session snapshots, not live ticks — same
  // empty view.set default as tape/charts (tiles no scan).
  suppressTileSeeding();
  useStore.setState({ openTiles: [], activeId: null });
  hearOnlyViewedScans(); // past sessions: no live scan alert
  tree = <ReviewPane />;
} else if (pane === 'daylog') {
  // The Day Log: every alert of an ET day with its outcome - recorded history,
  // not live ticks, so it tiles no scan and plays no scan alert.
  suppressTileSeeding();
  useStore.setState({ openTiles: [], activeId: null });
  hearOnlyViewedScans();
  tree = <DayLogPane />;
} else if (pane === 'order') {
  // The Order Ticket (internal/orderticket over HTTP): tiles no scan, plays no alert.
  suppressTileSeeding();
  useStore.setState({ openTiles: [], activeId: null });
  hearOnlyViewedScans();
  tree = <OrderPane />;
} else if (pane === 'signal') {
  // The short-fade signal window (one dedicated chart on a spare monitor,
  // running shortFade.ts's state machine) — same gridless "tiles no scan"
  // shape as tape/charts/review. ?sym is optional: absent, the pane follows
  // whatever symbol was last selected anywhere (GLOBAL_SYM_KEY), exactly
  // like TapePane's own follow mode.
  suppressTileSeeding();
  useStore.setState({ openTiles: [], activeId: null });
  hearOnlyViewedScans(); // its list's scan and the dildo / climax scans it views (SignalPane)
  tree = <SignalPane sym={params.get('sym') ?? ''} />;
} else {
  tree = <App />;
}

createRoot(document.getElementById('root')!).render(<React.StrictMode>{tree}</React.StrictMode>);
