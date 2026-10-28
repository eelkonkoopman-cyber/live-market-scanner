# React UI Architecture & Build/Embed Pipeline

Front-end design for the single-binary Go scanner. The UI is a Vite-built
React+TS SPA, embedded into the Go binary via `//go:embed` and fed exclusively
over `ws://localhost:7878/stream`. The UI holds **no durable state** — scans live
in SQLite server-side; the UI is a pure projection of WS frames plus ephemeral
view state.

## 1. Stack & grid choice

**Stack:** Vite 5 + React 18 + TypeScript 5, Zustand for the store, Web Audio for
sound, plain CSS modules (no UI kit — the grid is custom and a kit adds re-render
overhead). No data-fetching lib (no HTTP data layer — everything is WS push).

**Grid: TanStack Virtual (`@tanstack/react-virtual`)** — headless, ~5 KB. Not
AG Grid (300 KB–1 MB; it wants to own sort/filter/row-model, which we do
server-side and would have to fight), not react-window (maintenance mode).

The decisive factor: we must **paint flashes by mutating DOM via refs outside
React's render cycle**, and render rows in a server-supplied `order[]` that
changes ~4×/sec. TanStack Virtual gives us `virtualItems` (index→offset) and
nothing else; we keep total ownership of row DOM. Virtualization also bounds the
flashing-cell DOM mutation count to *visible* rows regardless of `limit` (extended
-hours gapper scans can transiently exceed the viewport).

## 2. Component tree

```
<App>                              // WS provider mount, audio-unlock gate, global keymap
├─ <StatusBar/>                    // session, feed up/down, msgRate, baseline building|ready, dataAge(asOf); ET clock+countdown, sources pill, symbol search ("/", Ctrl+K)
│   └─ <ThemePicker/>              // swatch-disc button + popover menu of the six AfterBell themes (sec.9)
├─ <ScanTabs>                      // tab strip; active tab drives view.set
│   ├─ <Tab .../> *N               // one per enabled scan (from scan.list)
│   └─ <AddScanButton/>            // opens ScanEditor in "new" mode
├─ <MainSplit>
│   ├─ <ScanView scanId=active>
│   │   ├─ <GridToolbar/>          // column toggles, edit-scan, resync button
│   │   └─ <LiveGrid scanId>       // virtualized, sortable-by-server, flashing, hover-freeze (sec.4); labels via fields.ts
│   │       ├─ <GridHeader/>       // shows server sort[] (read-only indicators)
│   │       └─ <GridBody>          // useVirtualizer over order[]
│   │           └─ <GridRow/> *visible
│   │               └─ <Cell/> *cols   // ref-driven flash
│   └─ <NewsPanel/>                // scrolling feed; filters to active scan's tickers
├─ <Modal>                         // shared dialog chrome (Modal.tsx): Escape closes, focus trapped + restored
│   ├─ <ScanEditor/>               // filter tree + sort + alert + sound + columns; fields.ts-driven dropdowns
│   │   ├─ <FilterTreeEditor/>     // recursive AND/OR group -> ScanDef.filter
│   │   ├─ <SortEditor/> <ColumnPicker/> <LimitField/>
│   │   └─ <AlertConfig/>          // enabled,on,trigger,cooldown,requireReentry,<SoundPicker/>
│   └─ <KeyModal/>                 // API-key prompt; same Modal chrome
└─ <ToastLayer/>                   // connection lost/restored, resync, errors
```

`ScanEditor` is a controlled form over a `ScanDef` draft; **Save** issues
`scan.upsert` and closes. It never persists locally. `fields.ts` is the single
registry of field label/name/unit/kind/group/hint that both it and `LiveGrid`
read, so a column header and its filter-dropdown entry can never disagree.

## 3. WS client layer (typed)

A single long-lived connection. The client is a plain class (lives in a
ref/singleton, **not** React state) that owns the socket, dispatches frames into
the store's hot maps, and handles reconnect/backoff and seq-gap → resync.

```ts
// ws/contract.ts — frame types mirror internal/core/wsmsg.go exactly
export type Sym = string;
export type Row = Record<string, number | boolean | string | null>;

export interface TickFrame {
  id: string; seq: number; asOf: number; full: boolean;
  order: Sym[];                 // server-sorted topN (authoritative order)
  rows: Record<Sym, Row>;       // changed-only unless full
}
export interface AlertFrame {
  scanId: string; sym: Sym; kind: 'enter' | 'cross';
  at: number; sound: string; row: Row;
}
export interface StatusFrame {
  session: 'pre'|'regular'|'post'|'closed'; feed: 'live'|'reconnecting'|'down';
  tracked: number; msgRate: number; baseline: 'building'|'ready'; clockMs: number;
}
```

```ts
// ws/client.ts (sketch)
export class ScanWS {
  private ws?: WebSocket;
  private backoff = 500;                        // ms, doubles to cap 8000
  private lastSeq = new Map<string, number>();  // per-scan seq tracking

  connect() {
    const ws = new WebSocket(this.url); this.ws = ws;
    ws.onopen = () => {
      this.backoff = 500;
      this.store.getState().setFeed('live');
      this.send({ t: 'hello' });                // server replies scan.list
      const active = this.store.getState().activeScanId;
      if (active) this.send({ t: 'view.set', d: { scanIds: [active] } });
    };
    ws.onmessage = (e) => this.dispatch(JSON.parse(e.data));
    ws.onclose = () => { this.store.getState().setFeed('down'); this.scheduleReconnect(); };
    ws.onerror = () => ws.close();
  }

  private onTick(d: TickFrame) {
    const prev = this.lastSeq.get(d.id);
    if (!d.full && prev != null && d.seq !== prev + 1) {
      this.send({ t: 'scan.resync', d: { id: d.id } });   // gap -> full snapshot
      return;                                              // drop this partial
    }
    this.lastSeq.set(d.id, d.seq);
    applyTick(this.store, d);                              // sec.4
  }

  setView(scanId: string) {                               // driven by active tab
    this.lastSeq.delete(scanId);
    this.send({ t: 'view.set', d: { scanIds: [scanId] } });
    this.send({ t: 'scan.resync', d: { id: scanId } });   // immediate baseline full tick
  }
}
```

**Per-scan client state** = `{ rows: Map<Sym,Row>; order: Sym[]; asOf; seq }`.
**Full tick** replaces `rows`+`order`; **delta tick** merges `rows` keys and
replaces `order` (order is always the complete topN). `view.set` fires on tab
switch so the backend only computes/pushes the visible scan — **alerts still fire
server-side for all enabled scans regardless of view.**

> Contract note: `news.view` uses `{ mode: 'all' | 'tickers', tickers?: [...] }`.
> "Show news for the active scan" = send `mode:'tickers'` with the active scan's
> current symbol list.

## 4. Row-flash rendering (diff outside React)

Principle: **React renders the row skeleton + column layout once per structural
change (order/columns); cell values and flash classes are painted by mutating DOM
through refs.** The virtualizer re-renders only on scroll or `order[]` change.

```ts
// store/applyTick.ts
const FLASH_FIELDS = ['last','chg1m','chg5m','chg15m','vol'] as const;

export function applyTick(store, d: TickFrame) {
  const st = store.getState();
  const scan = st.hot.get(d.id) ?? newScanState();
  if (d.full) scan.rows = new Map();

  for (const sym in d.rows) {
    const incoming = d.rows[sym], prev = scan.rows.get(sym);
    for (const fld of FLASH_FIELDS) {
      const nv = incoming[fld], ov = prev?.[fld];
      if (typeof nv === 'number' && typeof ov === 'number' && nv !== ov)
        scan.flash.set(flashKey(sym, fld), nv > ov ? 'up' : 'down');
    }
    scan.rows.set(sym, { ...prev, ...incoming });   // merge (delta) / set (full)
  }
  scan.order = d.order; scan.asOf = d.asOf;
  st.hot.set(d.id, scan);
  st.gridSink?.onTick(d.id, d.order, scan);          // imperative — no setState for values
}
```

`GridRow` exposes an imperative `update(row, flashes)` via `useImperativeHandle`
that sets `el.textContent` and toggles `flash-up`/`flash-down` classes (with the
`void el.offsetWidth` reflow trick to restart the CSS animation). Flash is a CSS
`@keyframes` that fades to transparent, so it self-clears with no React state.

```tsx
// grid/LiveGrid.tsx (core of the imperative sink)
useGridSink(scanId, {
  onTick(id, newOrder, scan) {
    if (id !== scanId) return;
    if (!sameOrder(newOrder, order)) setOrder(newOrder);  // React re-render ONLY on reorder
    for (const vi of virt.getVirtualItems()) {            // visible rows only
      const sym = newOrder[vi.index];
      rowHandles.current.get(sym)?.update(scan.rows.get(sym)!, scan.flash);
    }
    scan.flash.clear();
  },
});
```

Net: a tick changing 30 cells does **zero** React re-renders unless `order[]`
changed; it does 30 `textContent`/`classList` writes on visible cells only. The
client never sorts — order is always the server's `order[]`.

**Hover-freeze** (`gridHold.ts`): while the mouse is over the grid, `LiveGrid`
renders `mergeHeldOrder(frozenOrder, order)` instead of the server's live
`order[]` directly — the frozen order keeps every symbol's slot (a departed
one stays, dimmed, from a small last-seen cache) and only appends genuine new
arrivals at the bottom. The `onTick` cell-painting above is untouched, so
values keep updating live inside the held slots; only the *positions* stop
moving. Released on `mouseleave` or 10s of no mouse movement.

## 5. Sound alerts (Web Audio)

- **Unlock gate:** browsers block audio until a user gesture. `<App>` renders a
  one-time "Enable sound" overlay; on click we `resume()` the `AudioContext` and
  decode all sound buffers. Until then, alerts queue as toasts and play once
  unlocked.
- **Decoded buffer cache:** sounds are static assets (`/sounds/*.wav`) decoded
  once into `AudioBuffer`s keyed by name. `SoundPicker` lists these names; the
  choice is stored in `ScanDef.alert.sound` (server-side).
- **Playback:** the `alert` frame carries `sound`; the client plays it
  unconditionally. **Cooldown / requireReentry / enter-vs-cross are entirely
  server-side**, so the client never dedupes — the two sides can't disagree.

## 6. State management — Zustand + refs for hot data

**Two tiers:**

1. **Hot tier (NOT reactive):** `hot: Map<scanId, ScanState>` where
   `ScanState = { rows: Map<Sym,Row>; order: Sym[]; flash: Map<string,'up'|'down'>; asOf; seq }`.
   Mutated in place by `applyTick`; read imperatively by the grid via `gridSink`.
   No React subscriber ever reads `hot` reactively.
2. **Cold/reactive tier (Zustand):** scan list, active tab, columns per scan,
   status, news (capped ring ~500), connection state, toasts, per-symbol chart
   errors (`chartErrBySym`). Human-cadence changes that drive normal
   re-renders.

Why Zustand: narrow slice subscriptions + the ability to keep mutable Maps
*outside* the subscribe graph. Redux's per-tick immutable copies and context's
all-consumers-re-render are exactly what we must avoid at 4 Hz × N scans.

Throttling: the backend already throttles to `tickHz`. The grid's imperative
`onTick` is cheap (visible cells only) and schedules React work only on reorder,
so no `requestAnimationFrame` coalescing layer is required up front (add a
rAF-batched `setOrder` if reorder churn shows up in profiling). The status bar's
`dataAge` is a 1 Hz `setInterval` reading the latest `asOf` from `hot`.

**Error/toast lifecycle:** `chartErrBySym` is keyed by symbol so the charts-
monitor grid blames exactly the cell whose `chart.get` failed — populated by
an `error` frame that now carries `Sym` (`internal/server/hub.go`) or by a 15s
client watchdog for a reply that never arrives, cleared by the next successful
`chart.data` or a manual Retry click. `toasts` is capped at 4; `pushToast`
collapses a repeat of the newest message into a fresh id so its 6s
auto-dismiss timer restarts instead of firing early, and a click dismisses
immediately.

## 7. Build / embed pipeline

```
/web                      # React app (Vite root)
  index.html
  src/...
  public/sounds/*.wav
  vite.config.ts
/internal/server
  embed.go                # //go:embed all:web/dist
  spa.go                  # SPA fallback handler
  ws.go                   # /stream upgrader
```

```ts
// web/vite.config.ts
export default defineConfig({
  plugins: [react()],
  base: './',                       // relative asset URLs for embed
  build: { outDir: 'dist', emptyOutDir: true },
  server: { port: 5173, proxy: {
    '/stream': { target: 'ws://localhost:7878', ws: true },
    '/sounds': { target: 'http://localhost:7878' },
  }},
});
```

```go
// internal/server/embed.go
//go:embed all:web/dist
var distFS embed.FS
func DistFS() fs.FS { sub, _ := fs.Sub(distFS, "web/dist"); return sub }
```

SPA fallback: serve a real file if `fs.Stat` finds it (hashed assets, `/sounds`),
else return `index.html`. WS URL adapts: `import.meta.env.DEV` → `ws://localhost:7878/stream`,
else `ws://${location.host}/stream` (same origin as the served binary).

**Workflows:**
- *Dev:* Go backend on :7878 + `vite` on :5173; the proxy forwards `/stream` (WS).
  Hot-reload the UI without rebuilding the binary.
- *Prod:* `cd web && npm run build` → `web/dist`; then `go build` embeds it. A
  `go:generate`/Makefile step runs the Vite build before `go build` so the embed
  is never stale.

## 8. Saved-scans persistence (UI is stateless)

1. On WS connect → `hello`. Server replies `scan.list` from SQLite `scan_defs`.
2. UI hydrates tabs/columns from that frame. Active tab is ephemeral (first
   enabled, or last-used in `sessionStorage` as a pure UX nicety).
3. Create/edit → `scan.upsert <ScanDef>`; server validates, writes SQLite, echoes
   the authoritative def (or re-broadcasts `scan.list`). UI updates from the
   server response, not its local draft.
4. Delete → `scan.delete {id}`; server removes + confirms; UI drops the tab.
5. On reconnect/refresh, re-run `hello` → `scan.list` and fully rebuild — nothing
   is lost. No localStorage scan cache, no client-side merge logic. SQLite is the
   single source of truth; the UI is a stateless projection.

## 9. Theming

`data-theme` on `<html>` selects one of six AfterBell palettes (`warm`
default, `paper`, `slate`, `midnight`, `forest`, `plum`). Three layers:

1. **`afterbell-themes.css`** — the six `[data-theme]` blocks of `--ab-*`
   tokens, copied from AfterBell's own `app.css` and re-prefixed so they
   can't collide with the scanner's tokens below.
2. **`styles.css`** — semantic tokens (`--bg`, `--fg`, `--accent`, `--pos`,
   `--neg`, …) all derive from the active theme's `--ab-*` triplets, so one
   attribute flip re-colours every component.
3. **Components** — style rules use the semantic tokens, or
   `rgb(var(--ab-…) / alpha)` where they need a tint of a theme colour
   (`theme-picker.css` also borrows `--ab-glass-shadow-strong`). Two readers
   sit outside CSS: canvas charts read `--ab-*` through `chartTheme.ts`
   (below), and `cmd/scannerwin/overlay.go`'s native drag pill reads them
   with literal RGB fallbacks for pages that never define them.

Runtime (`theme.ts` + `useTheme.ts`): a plain module-level store, not
Zustand — `setTheme()` stamps `document.documentElement.dataset.theme`,
persists to `localStorage` (`mktscanner.theme.v1`), and every open window
(main + pop-outs) picks up the change via the `storage` event; `useTheme()`
is the `useSyncExternalStore` binding components use to re-render on either
this window's own change or another window's. `index.html` inline-scripts
the same lookup before React boots, so first paint already has the right
theme.

Canvas charts can't read CSS tokens at draw time, so `chartTheme.ts`
snapshots the active `--ab-*` values into a `ChartColors` object on demand;
`MonitorChart`/`CandleChart` re-run it and call `applyOptions({ colors })` on
theme change instead of rebuilding the chart instance.

## 10. Charts

`web/src/vendor/blackthorn-charts/` is AfterBell's chart engine TypeScript
source, mirrored in by `npm run sync:charts`
(`web/scripts/sync-chart-engine.mjs`) — never hand-edited here; the
directory's own `PROVENANCE.md` records the AfterBell commit and sync time.
Two consumers:

- **`MonitorChart`** (charts monitor window, one cell per symbol): `setData`
  on a symbol/timeframe switch, `update` for live ticks (preserves
  pan/zoom), `timeScale().resetView()` on a genuine series change (default
  zoom, latest bar right-anchored), and `priceScale()` for the manual
  Y-axis pin.
- **`CandleChart`** (inline per-row chart): `setData` +
  `timeScale().fitContent()` only — no live ticks, no manual scale,
  `showLastPrice: false`.

Both call `applyOptions({ colors })` to re-theme in place on a theme
change, rather than rebuilding the chart. `chartTheme.ts` bridges the
scanner's `--ab-*` CSS tokens to the engine's own `ChartColors`/`ChartFonts`
types (imported type-only): `chartColors()` maps
background/grid/axes/candles/volume/crosshair/legend/accent, `chartFonts()`
maps the app's monospace/UI font stacks (numbers vs. words), which MonitorChart
re-applies once web fonts finish loading (row charts read them at mount).
