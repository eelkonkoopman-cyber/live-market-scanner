import { create } from 'zustand';
import { applyScanFollow, normalizeFollowScan } from './chartFollow';
import { loadPositionSize, savePositionSize } from './positionSize';
import type { ScanDef, StatusFrame, NewsItem, TickFrame, Row, Sym, Bar, SummaryState, ActivityEvent, Fundamentals, TapeStat, TapeCard, TapeReadout, TapePrint, TapePrintsFrame, SetupRead, ShortLive, TrendRef, ChartTf, ChartCell, ChartGridLayout, ChartAutoSettings, SideBox, SideBoxMetric, ReviewRow, ReviewSession, FlagEvent, FlagEventsFrame, DayLogFrame } from './types';
import { flagEventKey as flagKey } from './flagEvents';

export interface GridData {
  order: Sym[];
  rows: Record<Sym, Row>;
  asOf: number;
}

function sameOrder(a: readonly Sym[], b: readonly Sym[]): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
  return true;
}

function isEmptyRows(rows: Record<Sym, Row> | undefined): boolean {
  if (!rows) return true;
  for (const _ in rows) return false;
  return true;
}

export interface Toast {
  id: number;
  msg: string;
  kind?: 'error' | 'info';
}

// --- resizable layout: panel sizes (px) + per-column widths (px), persisted ---

export interface PaneSizes {
  leftW: number; // ws-left flex-basis (grid+activity column)
  gridH: number; // grid flex-basis inside ws-left
  symH: number; // symbol panel flex-basis inside ws-right
  // Details column (symbol panel + news) collapsed: the grid column takes
  // the whole width. It stays mounted while hidden so row clicks still send
  // tape.watch (TWS / depth / borrow) and the follow-mode listener keeps
  // running — see SymbolPanel's effects.
  rightHidden: boolean;
}

const DEFAULT_PANE: PaneSizes = { leftW: 920, gridH: 460, symH: 480, rightHidden: false };
const LAYOUT_KEY = 'mktscanner.layout.v1';

interface PersistedLayout {
  pane?: Partial<PaneSizes>;
  gridCol?: Record<string, number>;
  actCol?: Record<string, number>;
  openTiles?: string[];
}

function loadLayout(): PersistedLayout {
  try {
    const raw = localStorage.getItem(LAYOUT_KEY);
    return raw ? JSON.parse(raw) : {};
  } catch {
    return {}; // private-mode / quota errors -> fall back to defaults
  }
}

function saveLayout(l: PersistedLayout): void {
  try {
    localStorage.setItem(LAYOUT_KEY, JSON.stringify(l));
  } catch {
    /* best-effort */
  }
}

// --- cross-window "last selected symbol" signal, for a tape pop-out's
// "follow" mode. Every openChart() call, in ANY window, writes the symbol
// here; a follow-mode pop-out listens for the `storage` event, which the
// browser only fires in OTHER tabs (never the one that wrote it), so this
// can't feed back into a loop. ---
export const GLOBAL_SYM_KEY = 'mktscanner.globalSym.v1';

function saveGlobalSym(sym: string): void {
  try {
    localStorage.setItem(GLOBAL_SYM_KEY, sym);
  } catch {
    /* best-effort */
  }
}

// --- main window "follow selections made in other windows" toggle ---
// Off by default (the main window has always been the one place that did NOT
// follow — see SymbolPanel's segbtn), persisted like PaneSizes so the choice
// survives a reload once a user turns it on.
const FOLLOW_KEY = 'mktscanner.mainFollow.v1';

function loadFollowOthers(): boolean {
  try {
    const raw = localStorage.getItem(FOLLOW_KEY);
    return raw ? (JSON.parse(raw) as boolean) : false;
  } catch {
    return false; // private-mode / quota errors -> default off
  }
}

function saveFollowOthers(on: boolean): void {
  try {
    localStorage.setItem(FOLLOW_KEY, JSON.stringify(on));
  } catch {
    /* best-effort */
  }
}

const persistedLayout = loadLayout();

// --- charts monitor window (?pane=charts): grid layout helpers ---

const CHART_TFS: readonly ChartTf[] = ['1m', '5m', '15m'];

// makeGrid builds a rows×cols cell array, preserving whatever prior cells
// still fit (flat row-major index) so shrinking/growing the preset doesn't
// wipe assignments that survive the resize.
function makeGrid(rows: number, cols: number, prev?: ChartCell[]): ChartGridLayout {
  const n = rows * cols;
  const cells: ChartCell[] = [];
  for (let i = 0; i < n; i++) {
    const p = prev?.[i];
    cells.push(p ? { ...p } : { sym: null, tf: '1m' });
  }
  return { rows, cols, cells };
}

// normalizeGrid validates a server-echoed layout blob (chartgrid.data). The
// server stores it opaquely, so an old/foreign shape must degrade to "keep
// what we have" (null) rather than crash the grid.
const SIDE_METRICS: readonly string[] = ['chgDay', 'chg5m'];

// normalizeBoxes validates the sidebar lists inside a layout blob the same
// way the cells are: a foreign/old shape degrades to "no boxes" (defaults)
// rather than a crash.
function normalizeBoxes(raw: unknown[]): SideBox[] {
  const out: SideBox[] = [];
  for (const r of raw) {
    if (!r || typeof r !== 'object') continue;
    const b = r as Record<string, unknown>;
    if (typeof b.id !== 'string' || !b.id) continue;
    out.push({
      id: b.id,
      scanId: typeof b.scanId === 'string' && b.scanId ? b.scanId : null,
      dir: b.dir === 'asc' ? 'asc' : 'desc',
      metric: typeof b.metric === 'string' && SIDE_METRICS.includes(b.metric) ? (b.metric as SideBoxMetric) : 'chgDay',
      limit: typeof b.limit === 'number' && b.limit > 0 ? Math.min(100, Math.floor(b.limit)) : 10,
    });
  }
  return out;
}

// normalizeAuto validates the Auto-mode settings the same way: anything
// foreign degrades to the defaults (off, default scan, Day %).
function normalizeAuto(raw: unknown): ChartAutoSettings | undefined {
  if (!raw || typeof raw !== 'object') return undefined;
  const a = raw as Record<string, unknown>;
  return {
    on: a.on === true,
    scanId: typeof a.scanId === 'string' && a.scanId ? a.scanId : null,
    metric: typeof a.metric === 'string' && SIDE_METRICS.includes(a.metric) ? (a.metric as SideBoxMetric) : 'chgDay',
  };
}

function normalizeGrid(x: unknown): ChartGridLayout | null {
  if (!x || typeof x !== 'object') return null;
  const o = x as { rows?: unknown; cols?: unknown; cells?: unknown; boxes?: unknown; auto?: unknown };
  const rows = typeof o.rows === 'number' && Number.isFinite(o.rows) ? Math.min(4, Math.max(1, Math.floor(o.rows))) : 0;
  const cols = typeof o.cols === 'number' && Number.isFinite(o.cols) ? Math.min(4, Math.max(1, Math.floor(o.cols))) : 0;
  if (!rows || !cols) return null;
  const raw = Array.isArray(o.cells) ? o.cells : [];
  const cells: ChartCell[] = [];
  for (let i = 0; i < rows * cols; i++) {
    const c = raw[i] as { sym?: unknown; tf?: unknown; pinned?: unknown } | undefined;
    const sym = c && typeof c.sym === 'string' && c.sym.trim() ? c.sym.trim().toUpperCase() : null;
    const tf = c && typeof c.tf === 'string' && (CHART_TFS as readonly string[]).includes(c.tf) ? (c.tf as ChartTf) : '1m';
    const followScan = normalizeFollowScan((c as { followScan?: unknown } | undefined)?.followScan);
    cells.push({ sym, tf, ...(c && c.pinned === true ? { pinned: true } : {}), ...(followScan ? { followScan } : {}) });
  }
  const boxes = Array.isArray(o.boxes) ? normalizeBoxes(o.boxes) : undefined;
  const auto = normalizeAuto(o.auto);
  return { rows, cols, cells, ...(boxes ? { boxes } : {}), ...(auto ? { auto } : {}) };
}

// foldMinute folds one live per-second aggregate into a trailing 1-minute bar
// series (or starts a new minute on a boundary). Returns null for a
// stale/out-of-order second — exactly the semantics applyChartBar has always
// had for the single selected chart, now shared with the monitor grid.
function foldMinute(bars: Bar[], b: { t: number; o: number; h: number; l: number; c: number; v: number }): Bar[] | null {
  const minute = Math.floor(b.t / 60_000) * 60_000;
  const last = bars[bars.length - 1];
  if (last && last.t === minute) {
    const merged: Bar = {
      t: minute,
      o: last.o,
      h: Math.max(last.h, b.h),
      l: Math.min(last.l, b.l),
      c: b.c,
      v: last.v + b.v,
    };
    return [...bars.slice(0, -1), merged];
  }
  if (last && minute < last.t) return null; // stale/out-of-order second
  return [...bars, { t: minute, o: b.o, h: b.h, l: b.l, c: b.c, v: b.v }];
}

// dropChartErr removes one symbol's cached chart-load error, if any. Returns
// the SAME object when there's nothing to drop, so callers can bail out to a
// plain `{}` patch (no needless re-render) exactly like pruneChartData does
// for barsBySym/prevCloseBySym.
function dropChartErr(m: Record<string, string>, sym: string): Record<string, string> {
  if (!(sym in m)) return m;
  const next = { ...m };
  delete next[sym];
  return next;
}

// --- morning review window (?pane=review) ---

// ReviewState is the pop-out's whole picture: dates/session/rows mirror the
// server's review.get / review.data round trip; rthCloseBySym is a client-side
// derivation cache, never sent by the server. It holds ONE date's regular-
// session closes at a time (rthCloseDate names which), refreshed whenever a
// 'regular' reply lands — including the review pane's own auto-fetch of that
// baseline whenever the post session is viewed for a date it doesn't cover yet
// (see ws.ts getReview). Keeping this out of `rows` is what lets the
// after-hours gain be recomputed from RAW values instead of trusting a
// server-sent percentage — see deriveReviewGainPct below.
export interface ReviewState {
  dates: string[]; // full date-picker list, newest first
  date: string | null; // date currently on screen (null before the first reply)
  session: ReviewSession; // session currently on screen; default 'post' (the morning flow)
  rows: ReviewRow[];
  loading: boolean;
  rthCloseBySym: Record<string, number>;
  rthCloseDate: string | null;
}

const DEFAULT_REVIEW: ReviewState = {
  dates: [], date: null, session: 'post', rows: [], loading: false, rthCloseBySym: {}, rthCloseDate: null,
};

// deriveReviewGainPct is the ONE place the review's gain% is computed, from
// RAW persisted values only — this is the feature's named bug class (three
// plausible baselines give three different numbers), so there is exactly one
// derivation and every session routes through it:
//   post    -> (close - SAME DAY's rth close) / rth close   [never prevClose, never the post session's own first print]
//   regular -> (close - prevClose) / prevClose               [the ordinary day change]
//   pre     -> (close - prevClose) / prevClose               [prevClose = the prior day's regular close]
// Returns null (never 0) when the required baseline is missing — a post-session
// row before its date's rth close has loaded, or a first-session prevClose
// that was never observed.
export function deriveReviewGainPct(row: ReviewRow, session: ReviewSession, rthCloseBySym: Record<string, number>): number | null {
  if (session === 'post') {
    const rth = rthCloseBySym[row.sym];
    if (!rth) return null;
    return ((row.close - rth) / rth) * 100;
  }
  if (!row.prevClose) return null;
  return ((row.close - row.prevClose) / row.prevClose) * 100;
}

// --- news summaries: LOCAL (free, always-on) vs CLAUDE (opt-in upgrade) ---

// summaryDisplay is the ONE place the "✨ Summarize" button's label/visibility
// and the summary block's LOCAL/CLAUDE tag are decided, from the raw
// SummaryState alone — mirrors deriveReviewGainPct's role as a single pure
// derivation a test can pin down directly. The button must never vanish just
// because SOME summary exists: a LOCAL auto-summary keeps it, relabeled, so
// the Claude upgrade stays reachable (news.summarize is sent the same way
// either way — main.go's newsSummarizeHandler is the one that tells a real
// cached Claude answer apart from a "local" one and re-summarizes instead of
// silently re-serving the local text). Only a genuine, already-Claude summary
// (model != "local") hides the button — that read is as good as this feature
// gets, so there is nothing left to ask for.
export interface SummaryDisplay {
  buttonLabel: string | null; // null = hide the button (a real Claude summary is already shown)
  tag: 'LOCAL' | 'CLAUDE' | null; // null = no summary text to tag yet
}

export function summaryDisplay(sum: SummaryState | undefined): SummaryDisplay {
  if (!sum?.text) return { buttonLabel: '✨ Summarize', tag: null };
  if (sum.model === 'local') return { buttonLabel: '✨ Ask Claude', tag: 'LOCAL' };
  return { buttonLabel: null, tag: 'CLAUDE' };
}

interface State {
  feed: 'up' | 'down';
  status: StatusFrame | null;
  scans: ScanDef[];
  activeId: string | null;
  openTiles: string[]; // ordered scanIds tiled open in the main window; activeId is always a member (or both empty)
  // mutedScans: per-scan sound-alert mute, scoped to THIS WINDOW only — plain
  // in-memory state, deliberately NOT persisted to localStorage. This app's
  // native shell runs every window (main + every pop-out) as a separate
  // process, but they all share ONE WebView2 user-data folder, so
  // localStorage is already deliberately shared across windows (see
  // GLOBAL_SYM_KEY's follow-mode). A mute preference must NOT leak the same
  // way — "mute scan X" in one window must never silence scan X in another —
  // so it lives only in this window's own store instance, which is airtight
  // by construction (a separate JS runtime per window) rather than relying on
  // browser-storage isolation semantics in an unusual shared-profile setup.
  // Absent/false = audible, so nobody's alerts change unless muted here.
  mutedScans: Record<string, boolean>;
  // signalSoundMuted / signalVoiceOff: the Signal window's toolbar 🔊/🔇 and
  // '🗣 Voice' toggles, mirrored here by SignalPane so ws.ts's alert case can
  // honour them. They used to gate only the trade signals the window's own cells
  // print; the flag SCAN's alert frames (the window narrows its scan view to that
  // scan, seeded with "speak the symbol") never saw them, so the buttons promised
  // more than they did. Per-window and in-memory like mutedScans: every other
  // window leaves them false (audible, spoken) because nothing there sets them.
  // The voice flag starts from SignalPane's persisted value (an effect on mount),
  // so a saved "voice off" holds for the scan's speech after a reload too.
  signalSoundMuted: boolean;
  signalVoiceOff: boolean;
  twsFollow: boolean; // global alert-driven TWS follow switch (server-owned, alerttws.data); default on
  // signalScansOff: the signal scans switched OFF in the Signal window's
  // "Alerts" menu (e9.ts SIGNAL_SWITCHES), mirrored here by SignalPane so
  // ws.ts's alert case neither plays nor speaks their alerts. An array (checked
  // with includes), never a keyed object: a scan id is user data. Per-window and
  // in-memory like the two above; empty everywhere else.
  signalScansOff: string[];
  // positionSize: the Signal window's "Position size $" (positionSize.ts); 0 = off.
  positionSize: number;
  grids: Record<string, GridData>;
  news: NewsItem[];
  toasts: Toast[];
  editorOpen: boolean;
  editing: ScanDef | null;
  keyModalOpen: boolean;
  chartSym: string | null;
  chartBars: Bar[] | null;
  chartPrevClose: number | null;
  fundamentals: Fundamentals | null;
  fundamentalsErr: { msg: string; notEntitled: boolean } | null;
  // Daily-trend context for the SELECTED symbol. undefined = not yet answered,
  // null = server answered "no history" — the chip renders differently.
  trend: TrendRef | null | undefined;
  tape: TapeStat | null;
  // Raw time-and-sales ring for the SELECTED symbol (pop-out tape window's
  // scrolling tape), newest first, capped at TAPE_FEED_CAP. tapeFeedDropped
  // accumulates prints lost to backpressure (the engine never blocks on a
  // slow window), so the feed can render an honest gap marker.
  tapeFeed: TapePrint[];
  tapeFeedDropped: number;
  tapeRead: TapeReadout | null;
  tapeReadLoading: boolean;
  tapeCards: TapeCard[]; // completed tape-reading cards for the selected symbol, newest first
  tapePaused: boolean; // client-only "freeze the reads feed" toggle; never sent to the server
  shortLive: ShortLive | null;
  setupRead: SetupRead | null; // Claude's bull-flag read for the selected symbol (arrives unprompted on scan entry)
  summaries: Record<string, SummaryState>;
  newsQuery: string; // ticker/text filter for the news panel
  newsScanOnly: boolean; // show only news for symbols in the active scan
  activity: ActivityEvent[]; // scan entries + alerts, newest first
  actScanOnly: boolean; // activity log: hide hits belonging to other scans
  newsFollowSym: boolean; // news panel follows the selected symbol
  // Main window "follow symbol selections made in other windows" toggle (the
  // review/charts/tape pop-outs' clicks) — see SymbolPanel's "🔗 Follow"
  // segbtn. Persisted (FOLLOW_KEY), unlike mutedScans: this is a genuine user
  // preference, not per-window state that must never leak.
  followOthers: boolean;
  pane: PaneSizes;
  gridColW: Record<string, number>; // LiveGrid column widths, keyed by field ('sym' or metric name)
  actColW: Record<string, number>; // ActivityLog column widths, keyed by column key

  // --- charts monitor window (?pane=charts) ---
  watchlist: string[]; // server-authoritative (watchlist.data); edits go out via ws.sendWatchlist
  chartGrid: ChartGridLayout;
  // The Signal window's flag-scan signal history (flagevents.*): null until the
  // server answers (an older server never does). `date` is the day being viewed
  // ('YYYY-MM-DD' ET, as the frame names it), `dates` the days that have events
  // (newest first), `events` that day's, oldest first. The viewed date lives
  // here so a reconnect can re-request it.
  flagEvents: { date: string; dates: string[]; events: FlagEvent[] } | null;
  // The Day Log window (daylog.*): the day on screen as the server sent it, whether a request is in flight and the last error.
  dayLog: { frame: DayLogFrame | null; loading: boolean; error: string | null };
  // Every flagevents.add of the latest day added (deduped, capped): a row
  // recorded while a flagevents.get was in flight arrives as an add BEFORE the
  // data frame that lacks it, so setFlagEvents merges these back in.
  flagEventAdds: { date: string; events: FlagEvent[] } | null;
  // gridRev counts LOCAL (user-made) grid edits only — the pane's debounced
  // chartgrid.set persist keys off it, so a server-echoed layout from another
  // window (applyGridFromServer) can never trigger a re-save ping-pong loop.
  gridRev: number;
  barsBySym: Record<string, Bar[]>; // 1-min bars per grid-assigned symbol
  prevCloseBySym: Record<string, number | null>;
  // sym -> user-facing message when a grid cell's chart.get failed, timed
  // out, or was lost — lets MonitorChart show "Couldn't load" + Retry instead
  // of sitting on "Loading" forever (see ws.ts's per-symbol chart timers).
  chartErrBySym: Record<string, string>;

  // --- morning review window (?pane=review) ---
  review: ReviewState;

  // --- signal window (?pane=signal) ---
  // The deduped symbol SET currently assigned across the Signal window's own
  // grid (addendum 2 — the window shows multiple stocks now, each cell
  // running the engine on its own symbol) — a second, gridless consumer of
  // the same per-symbol bar cache the charts monitor writes into (see
  // setSymChartData's guard below). Lives here (rather than only in
  // SignalPane's own React state / signalLayout.ts) purely so that guard has
  // something store-side to check; the pane owns everything else about its
  // layout (signalLayout.ts, localStorage) the same way TapePane owns its
  // own single symbol.
  signalSyms: string[];

  // --- today's symbol blacklist (blacklist.ts) ---
  // blacklist: the last 'blacklist' frame (sorted, never null); blacklistAt:
  // when it arrived (ms) - the list only holds for that ET day.
  blacklist: string[];
  blacklistAt: number;

  setFeed: (f: 'up' | 'down') => void;
  setStatus: (s: StatusFrame) => void;
  setScans: (s: ScanDef[]) => void;
  setActive: (id: string) => void;
  openTile: (id: string) => void; // add-if-missing + focus
  closeTile: (id: string) => void; // remove + refocus to a remaining tile or null
  toggleScanMute: (scanId: string) => void; // flip this window's sound-alert mute for one scan
  setSignalSoundMuted: (muted: boolean) => void; // the Signal window toolbar's mute, for scan alerts too
  setTwsFollow: (on: boolean) => void;
  setSignalVoiceOff: (off: boolean) => void; // the Signal window toolbar's voice toggle (inverted), for scan alerts too
  setPositionSize: (n: number) => void;
  setSignalScansOff: (ids: string[]) => void; // the Signal window's switched-off signal scans
  applyTick: (d: TickFrame) => void;
  // applyTicks folds a batch of tick frames (ws.ts coalesces what arrives
  // within one short window) into ONE store update, so a snapshot that moves
  // six tiled scans costs one render pass, not six.
  applyTicks: (list: TickFrame[]) => void;
  // setStandaloneRow feeds a one-off row.get response into the grids cache
  // under a reserved key, so SymbolPanel's "search every open grid" row
  // lookup finds it for free — used by a pop-out tape window with no scan
  // tiled, which has no other source for header stats.
  setStandaloneRow: (sym: Sym, row: Row) => void;
  pushNews: (items: NewsItem[]) => void;
  setNewsScore: (id: string, score: number, tier: string, eventType: string, direction: string) => void;
  pushToast: (msg: string) => void;
  removeToast: (id: number) => void;
  openEditor: (def: ScanDef | null) => void;
  closeEditor: () => void;
  openKeyModal: () => void;
  closeKeyModal: () => void;
  openChart: (sym: string) => void;
  closeChart: () => void;
  setChartData: (sym: string, bars: Bar[], prevClose: number) => void;
  applyChartBar: (b: { sym: string; t: number; o: number; h: number; l: number; c: number; v: number }) => void;
  setFundamentals: (f: Fundamentals) => void;
  setTrend: (sym: string, ref: TrendRef | null) => void;
  setFundamentalsErr: (sym: string, msg: string, notEntitled: boolean) => void;
  setTape: (t: TapeStat) => void;
  pushTapePrints: (m: TapePrintsFrame) => void;
  setShortLive: (s: ShortLive) => void;
  setTapeRead: (r: TapeReadout) => void;
  tapeReadPending: () => void;
  pushTapeCard: (c: TapeCard) => void;
  setTapePaused: (on: boolean) => void;
  setSetupRead: (r: SetupRead) => void;
  summaryPending: (id: string) => void;
  setSummary: (id: string, text: string, model: string, err: string) => void;
  setNewsQuery: (q: string) => void;
  toggleNewsScanOnly: () => void;
  toggleActScanOnly: () => void;
  pushActivity: (e: { scanId: string; sym: string; at: number; last: number; kind: string; hasNews: boolean }) => void;
  toggleNewsFollowSym: () => void;
  setFollowOthers: (on: boolean) => void; // toggle + persist "follow other windows' symbol selection"
  setPane: (patch: Partial<PaneSizes>) => void;
  setGridColW: (col: string, w: number) => void;
  setActColW: (col: string, w: number) => void;
  persistLayout: () => void; // call once on drag-end (mouseup), not per mousemove

  // --- charts monitor window (?pane=charts) ---
  setWatchlist: (syms: string[]) => void;
  setFlagEvents: (frame: FlagEventsFrame) => void; // flagevents.data: replace
  dayLogPending: () => void; // a daylog.get went out
  setDayLog: (frame: DayLogFrame) => void; // daylog.data
  dayLogFailed: (msg: string) => void; // the "daylog" error frame
  addFlagEvents: (frame: FlagEventsFrame) => void; // flagevents.add: merge today's new events
  setGridPreset: (rows: number, cols: number) => void; // preserves cells that still fit
  assignCell: (i: number, sym: string | null) => void;
  setCellTf: (i: number, tf: ChartTf) => void;
  applyGridFromServer: (layout: unknown) => void; // chartgrid.data — never bumps gridRev
  setAutoFill: (patch: Partial<ChartAutoSettings>) => void; // Auto mode on/off, scan, metric - a local edit, bumps gridRev
  toggleCellPin: (i: number) => void; // Auto mode: pin/unpin a cell - a local edit, bumps gridRev
  setCellFollow: (i: number, scanId: string | null) => void; // Follow scan per cell (null = Off) - bumps gridRev
  followCell: (scanId: string, sym: string) => void; // switch every cell following scanId to sym - bumps gridRev
  applyAutoCells: (syms: (string | null)[], changed: number[]) => void; // Auto's own swaps into un-pinned cells - bumps gridRev (so the layout persists), does NOT pin
  setSideBoxes: (boxes: SideBox[]) => void; // the sidebar gainers/decliners lists — a local edit, bumps gridRev
  setSymChartData: (sym: string, bars: Bar[], prevClose: number) => void; // chart.data routed to a grid cell
  setSymChartErr: (sym: string, msg: string) => void; // a grid cell's chart.get failed, timed out, or was lost
  clearSymChartErr: (sym: string) => void; // retry, reassignment, or a fresh chart.data superseded it
  pruneChartData: (keep: string[]) => void; // drop cached bars for syms no longer in any cell

  // --- morning review window (?pane=review) ---
  setReviewSession: (session: ReviewSession) => void; // client-selected, like openChart's chartSym — highlights immediately, ws.ts follows up with the request
  reviewPending: () => void;
  reviewFailed: () => void; // clears loading on a failed review.get (the "review" TError code) instead of spinning forever
  // setReviewData applies one review.get reply (ws.ts dispatch). A 'regular'
  // reply ALWAYS refreshes the after-hours baseline for its date, regardless
  // of which session is on screen; it only replaces the DISPLAYED
  // date/rows/loading when the echoed session matches the currently selected
  // one — same "ignore replies for a selection we've since navigated away
  // from" guard setTape/setTrend use, keyed on session instead of sym.
  setReviewData: (date: string, session: ReviewSession, dates: string[], rows: ReviewRow[]) => void;

  // --- signal window (?pane=signal) ---
  setSignalSyms: (syms: string[]) => void; // normalises: trim + uppercase each, dedupes

  // --- today's blacklist ---
  setBlacklist: (syms: string[]) => void;
}

// Raw time-and-sales ring size: ~a couple minutes of busy tape, and safely
// more than the ~150 rows the feed component actually renders.
const TAPE_FEED_CAP = 400;

let toastSeq = 1;
let activitySeq = 0;
// Only auto-pick a default tile on the very first scan.list of the session —
// module-level (not persisted) so a user who explicitly closes every tile
// doesn't have one re-appear on the next unrelated scan.list broadcast (e.g.
// after editing some other scan).
let tilesSeeded = false;

// suppressTileSeeding lets a pop-out pane (which deliberately wants zero or
// exactly one specific tile, not the "seed a default" behavior) opt out
// before its own first scan.list arrives. Each browser tab/window runs its
// own fresh module instance, so `tilesSeeded` alone can't tell "a pop-out
// already decided its tiles" apart from "a genuinely fresh main-window load".
export function suppressTileSeeding(): void {
  tilesSeeded = true;
}

export const useStore = create<State>()((set, get) => ({
  feed: 'down',
  status: null,
  scans: [],
  activeId: null,
  openTiles: persistedLayout.openTiles ?? [],
  mutedScans: {},
  signalSoundMuted: false,
  signalVoiceOff: false,
  twsFollow: true,
  signalScansOff: [],
  positionSize: loadPositionSize(),
  grids: {},
  news: [],
  toasts: [],
  editorOpen: false,
  editing: null,
  keyModalOpen: false,
  chartSym: null,
  chartBars: null,
  chartPrevClose: null,
  fundamentals: null,
  fundamentalsErr: null,
  trend: undefined,
  tape: null,
  tapeFeed: [],
  tapeFeedDropped: 0,
  tapeRead: null,
  tapeReadLoading: false,
  tapeCards: [],
  tapePaused: false,
  shortLive: null,
  setupRead: null,
  summaries: {},
  newsQuery: '',
  newsScanOnly: false,
  activity: [],
  actScanOnly: false,
  newsFollowSym: true,
  followOthers: loadFollowOthers(),
  pane: { ...DEFAULT_PANE, ...persistedLayout.pane },
  gridColW: persistedLayout.gridCol ?? {},
  actColW: persistedLayout.actCol ?? {},
  watchlist: [],
  flagEvents: null,
  dayLog: { frame: null, loading: false, error: null },
  flagEventAdds: null,
  chartGrid: makeGrid(2, 2),
  gridRev: 0,
  barsBySym: {},
  prevCloseBySym: {},
  chartErrBySym: {},
  review: DEFAULT_REVIEW,
  signalSyms: [],
  blacklist: [],
  blacklistAt: 0,

  // A disconnect clears the raw tape ring: after a reconnect the server has
  // no memory of what it already sent, so whatever is on screen is stale —
  // and stale tape is worse than empty tape.
  setFeed: (feed) => set(feed === 'down' ? { feed, tapeFeed: [], tapeFeedDropped: 0 } : { feed }),
  setStatus: (status) => set({ status }),
  setScans: (scans) =>
    set((st) => {
      const validIds = new Set(scans.map((x) => x.id));
      let openTiles = st.openTiles.filter((id) => validIds.has(id));
      // Prune mute flags for scans that no longer exist — dangling keys are
      // harmless (a deleted scan's tile is already gone), but this keeps the
      // record from growing unbounded across a long session of edits.
      const mutedScans = Object.fromEntries(
        Object.entries(st.mutedScans).filter(([id]) => validIds.has(id)),
      );
      let activeId = st.activeId && openTiles.includes(st.activeId) ? st.activeId : (openTiles[openTiles.length - 1] ?? null);
      // First-ever scan.list with nothing tiled (fresh install, no persisted
      // layout): seed one tile so the app isn't blank, matching the old
      // single-active-tab default. Never re-seeds after a deliberate close.
      if (!tilesSeeded) {
        tilesSeeded = true;
        if (openTiles.length === 0 && scans.length > 0) {
          const seed = scans.find((x) => x.enabled)?.id ?? scans[0].id;
          openTiles = [seed];
          activeId = seed;
        }
      }
      return { scans, openTiles, activeId, mutedScans };
    }),
  setActive: (activeId) => set({ activeId }),
  toggleScanMute: (scanId) =>
    set((st) => ({ mutedScans: { ...st.mutedScans, [scanId]: !st.mutedScans[scanId] } })),
  setSignalSoundMuted: (signalSoundMuted) => set({ signalSoundMuted }),
  setSignalVoiceOff: (signalVoiceOff) => set({ signalVoiceOff }),
  setTwsFollow: (twsFollow) => set({ twsFollow }),
  setSignalScansOff: (signalScansOff) => set({ signalScansOff }),
  setPositionSize: (positionSize) => {
    savePositionSize(positionSize);
    set({ positionSize });
  },
  openTile: (id) => {
    set((st) => (st.openTiles.includes(id) ? { activeId: id } : { openTiles: [...st.openTiles, id], activeId: id }));
    get().persistLayout();
  },
  closeTile: (id) => {
    set((st) => {
      const openTiles = st.openTiles.filter((x) => x !== id);
      const activeId = st.activeId === id ? (openTiles[openTiles.length - 1] ?? null) : st.activeId;
      return { openTiles, activeId };
    });
    get().persistLayout();
  },
  applyTick: (d) => get().applyTicks([d]),
  applyTicks: (list) => {
    let grids = get().grids;
    let changed = false;
    for (const d of list) {
      const g = grids[d.id] ?? { order: [], rows: {}, asOf: 0 };
      // A delta with no row bodies and an unchanged order is a no-op: keep
      // the grid object as it is so nothing subscribed to this scan
      // re-renders. (The scanner no longer sends such ticks, but an older
      // binary still does, ten times a second per scan.)
      if (!d.full && isEmptyRows(d.rows) && sameOrder(g.order, d.order)) continue;
      const merged: Record<Sym, Row> = d.full ? {} : { ...g.rows };
      for (const sym in d.rows) merged[sym] = { ...(merged[sym] ?? {}), ...d.rows[sym] };
      const rows: Record<Sym, Row> = {};
      for (const sym of d.order) if (merged[sym]) rows[sym] = merged[sym];
      if (!changed) {
        grids = { ...grids };
        changed = true;
      }
      grids[d.id] = { order: d.order, rows, asOf: d.asOf };
    }
    // No set() at all when nothing changed: zustand notifies every subscriber
    // on any set, even an empty patch.
    if (changed) set({ grids });
  },
  setStandaloneRow: (sym, row) =>
    set((st) => ({
      grids: { ...st.grids, __standalone__: { order: [sym], rows: { [sym]: row }, asOf: Date.now() } },
    })),
  pushNews: (items) =>
    set((st) => {
      const seen = new Set(st.news.map((n) => n.id));
      const fresh = items.filter((n) => !seen.has(n.id));
      if (fresh.length === 0) return {};
      // Sort by publish time, not arrival: archive history (news.for) lands
      // AFTER live items despite being older, and would otherwise sit on top.
      // Cap 500 (headline refs only, no bodies) — enough that a selected
      // symbol's same-day history survives a busy tape.
      const merged = [...fresh, ...st.news].sort((a, b) => b.ts - a.ts);
      return { news: merged.slice(0, 500) };
    }),
  // A score can arrive for an article not currently in the list (evicted by
  // the 500 cap, or one this client never saw) — a no-op patch, not an error.
  setNewsScore: (id, score, tier, eventType, direction) =>
    set((st) => ({
      news: st.news.map((n) => (n.id === id ? { ...n, score, scoreTier: tier, eventType, direction } : n)),
    })),
  // A repeat of the newest toast REPLACES it (fresh id) instead of stacking a
  // duplicate — the id change is what makes Toasts.tsx's per-toast auto-
  // dismiss timer restart rather than the old one firing early.
  pushToast: (msg) =>
    set((st) => {
      const last = st.toasts[st.toasts.length - 1];
      const base = last && last.msg === msg ? st.toasts.slice(0, -1) : st.toasts;
      return { toasts: [...base, { id: toastSeq++, msg }].slice(-4) };
    }),
  removeToast: (id) => set((st) => ({ toasts: st.toasts.filter((t) => t.id !== id) })),
  openEditor: (editing) => set({ editorOpen: true, editing }),
  closeEditor: () => set({ editorOpen: false, editing: null }),
  openKeyModal: () => set({ keyModalOpen: true }),
  closeKeyModal: () => set({ keyModalOpen: false }),
  openChart: (sym) => {
    saveGlobalSym(sym);
    set({
      chartSym: sym, chartBars: null, chartPrevClose: null,
      fundamentals: null, fundamentalsErr: null, trend: undefined,
      tape: null, tapeFeed: [], tapeFeedDropped: 0, tapeRead: null, tapeReadLoading: false,
      tapeCards: [], tapePaused: false, shortLive: null, setupRead: null,
    });
  },
  closeChart: () =>
    set({
      chartSym: null, chartBars: null, chartPrevClose: null,
      fundamentals: null, fundamentalsErr: null,
      tape: null, tapeFeed: [], tapeFeedDropped: 0, tapeRead: null, tapeReadLoading: false,
      tapeCards: [], tapePaused: false, shortLive: null, setupRead: null,
    }),
  setChartData: (sym, bars, prevClose) =>
    set((st) => (st.chartSym === sym ? { chartBars: bars, chartPrevClose: prevClose || null } : {})),
  // Fold a live per-second aggregate into the trailing 1-minute candle (or
  // start a new one on a minute boundary). The panel renders 1-min base bars,
  // so seconds accumulate into the forming minute rather than appending.
  // Feeds TWO independent targets with identical folding: the single selected
  // chart (chartSym/chartBars, unchanged semantics) and the monitor grid's
  // per-symbol cache (barsBySym) — the same symbol can be both.
  applyChartBar: (b) =>
    set((st) => {
      const patch: { chartBars?: Bar[]; barsBySym?: Record<string, Bar[]> } = {};
      if (st.chartSym === b.sym && st.chartBars) {
        const next = foldMinute(st.chartBars, b);
        if (next) patch.chartBars = next;
      }
      const cellBars = st.barsBySym[b.sym];
      if (cellBars) {
        const next = foldMinute(cellBars, b);
        if (next) patch.barsBySym = { ...st.barsBySym, [b.sym]: next };
      }
      return patch;
    }),
  setFundamentals: (f) =>
    set((st) => (st.chartSym === f.sym ? { fundamentals: f, fundamentalsErr: null } : {})),
  // Same stale-guard as fundamentals: drop answers for a deselected symbol.
  setTrend: (sym, ref) => set((st) => (st.chartSym === sym ? { trend: ref } : {})),
  setFundamentalsErr: (sym, msg, notEntitled) =>
    set((st) => (st.chartSym === sym ? { fundamentals: null, fundamentalsErr: { msg, notEntitled } } : {})),
  // Tape frames are broadcast to all clients, so drop any that arrive for a
  // symbol we've since navigated away from.
  setTape: (t) => set((st) => (st.chartSym === t.sym ? { tape: t } : {})),
  // Same stale-guard as setTape: tape.prints is routed per watching window
  // server-side, but a batch can still race a symbol switch — drop any batch
  // for a symbol we've since navigated away from. The batch arrives oldest
  // first; the ring is newest first, so the batch is reversed and prepended.
  pushTapePrints: (m) =>
    set((st) => {
      if (st.chartSym !== m.sym) return {};
      const batch = m.prints ?? [];
      const dropped = st.tapeFeedDropped + (m.dropped ?? 0);
      if (batch.length === 0) return { tapeFeedDropped: dropped };
      const reversed = [...batch].reverse();
      return { tapeFeed: [...reversed, ...st.tapeFeed].slice(0, TAPE_FEED_CAP), tapeFeedDropped: dropped };
    }),
  setShortLive: (s) => set((st) => (st.chartSym === s.sym ? { shortLive: s } : {})),
  setTapeRead: (r) =>
    set((st) => (st.chartSym === r.sym ? { tapeRead: r, tapeReadLoading: false } : {})),
  tapeReadPending: () => set({ tapeReadLoading: true }),
  // Same stale-guard as setTape: drop cards for a symbol we've navigated away
  // from. Newest first, capped at 30 (mirrors the Python UI's reads feed).
  pushTapeCard: (c) =>
    set((st) => (st.chartSym === c.sym ? { tapeCards: [c, ...st.tapeCards].slice(0, 30) } : {})),
  setTapePaused: (on) => set({ tapePaused: on }),
  // Setup reads are fired by scan entry and fanned out to every client (there
  // is no per-client request), so the same stale-guard as setTape/setTapeRead
  // applies: a read for a symbol we've since navigated away from is dropped,
  // never shown under the wrong ticker.
  setSetupRead: (r) => set((st) => (st.chartSym === r.sym ? { setupRead: r } : {})),
  summaryPending: (id) =>
    set((st) => ({ summaries: { ...st.summaries, [id]: { loading: true } } })),
  setSummary: (id, text, model, err) =>
    set((st) => ({
      summaries: { ...st.summaries, [id]: { loading: false, text: text || undefined, model: model || undefined, err: err || undefined } },
    })),
  setNewsQuery: (newsQuery) => set({ newsQuery }),
  toggleNewsScanOnly: () => set((st) => ({ newsScanOnly: !st.newsScanOnly })),
  toggleActScanOnly: () => set((st) => ({ actScanOnly: !st.actScanOnly })),
  pushActivity: (e) =>
    set((st) => {
      const count = st.activity.filter((a) => a.sym === e.sym).length + 1;
      const ev = { ...e, seq: ++activitySeq, count };
      return { activity: [ev, ...st.activity].slice(0, 200) };
    }),
  toggleNewsFollowSym: () => set((st) => ({ newsFollowSym: !st.newsFollowSym })),
  setFollowOthers: (on) => {
    saveFollowOthers(on);
    set({ followOthers: on });
  },

  // Drag handlers call setPane/setGridColW/setActColW on every mousemove (cheap,
  // in-memory) and persistLayout() once on mouseup (writes localStorage).
  setPane: (patch) => set((st) => ({ pane: { ...st.pane, ...patch } })),
  setGridColW: (col, w) => set((st) => ({ gridColW: { ...st.gridColW, [col]: w } })),
  setActColW: (col, w) => set((st) => ({ actColW: { ...st.actColW, [col]: w } })),
  persistLayout: () => {
    const st = get();
    saveLayout({ pane: st.pane, gridCol: st.gridColW, actCol: st.actColW, openTiles: st.openTiles });
  },

  // --- charts monitor window (?pane=charts) ---
  // The watchlist is server-authoritative: edits are SENT via ws.sendWatchlist
  // (watchlist.set) and only rendered when the server's watchlist.data echo
  // lands here — no local double-apply.
  setWatchlist: (watchlist) => set({ watchlist }),
  // The frame is the whole day as of the server's snapshot - but a day only
  // grows, and an add can overtake the snapshot (RV2-UI-1), so the rows of the
  // same day already shown or added live that the frame lacks are kept.
  dayLogPending: () => set((st) => ({ dayLog: { ...st.dayLog, loading: true, error: null } })),
  setDayLog: (frame) => set({ dayLog: { frame, loading: false, error: null } }),
  dayLogFailed: (msg) => set((st) => ({ dayLog: { ...st.dayLog, loading: false, error: msg } })),
  setFlagEvents: (frame) =>
    set((st) => {
      let events = frame.events ?? [];
      const extra = [
        ...(st.flagEvents?.date === frame.date ? st.flagEvents.events : []),
        ...(st.flagEventAdds?.date === frame.date ? st.flagEventAdds.events : []),
      ];
      if (extra.length) {
        const seen = new Set(events.map(flagKey));
        const keep = extra.filter((e) => {
          const k = flagKey(e);
          if (seen.has(k)) return false;
          seen.add(k);
          return true;
        });
        if (keep.length) events = [...events, ...keep];
      }
      return { flagEvents: { date: frame.date, dates: frame.dates ?? [frame.date], events } };
    }),
  // A live add concerns TODAY only: it is merged into the day list always
  // (a new day appears in the stepper) but appended to the events only while
  // that day is the one being viewed. Before the first data frame it is only
  // remembered (flagEventAdds), and merged when that frame lands.
  addFlagEvents: (frame) =>
    set((st) => {
      let adds = st.flagEventAdds;
      if (frame.events?.length && (!adds || frame.date >= adds.date)) {
        const prev = adds && adds.date === frame.date ? adds.events : [];
        const seen = new Set(prev.map(flagKey));
        const fresh = frame.events.filter((e) => !seen.has(flagKey(e)));
        adds = { date: frame.date, events: [...prev, ...fresh].slice(-2000) };
      }
      const cur = st.flagEvents;
      if (!cur) return { flagEventAdds: adds };
      const dates = [...new Set([...(frame.dates ?? []), frame.date, ...cur.dates])].sort().reverse().slice(0, 30);
      let events = cur.events;
      if (cur.date === frame.date && frame.events?.length) {
        const seen = new Set(events.map((e) => `${e.sym}|${e.ts}|${e.kind}`));
        const fresh = frame.events.filter((e) => !seen.has(`${e.sym}|${e.ts}|${e.kind}`));
        if (fresh.length) events = [...events, ...fresh];
      }
      return { flagEvents: { date: cur.date, dates, events }, flagEventAdds: adds };
    }),
  setGridPreset: (rows, cols) =>
    set((st) => ({ chartGrid: { ...st.chartGrid, ...makeGrid(rows, cols, st.chartGrid.cells) }, gridRev: st.gridRev + 1 })),
  assignCell: (i, sym) =>
    set((st) => {
      if (i < 0 || i >= st.chartGrid.cells.length) return {};
      const s = sym ? sym.trim().toUpperCase() : null;
      const normalized = s || null;
      // Re-assigning the SAME symbol the cell already holds (both null counts
      // as equal) is a genuine no-op: nothing about the grid actually
      // changes, so this must not bump gridRev or touch chartErrBySym either.
      // ChartsPane only sends a fresh chart.get when the pane's DEDUPED
      // symbol set changes — an unchanged symbol means no new request ever
      // follows, so clearing an existing error here would drop the cell back
      // to "Loading…" forever with no timer running to resolve it.
      if ((st.chartGrid.cells[i]?.sym ?? null) === normalized) return {};
      const cells = st.chartGrid.cells.slice();
      // With Auto on, a hand-made assignment (drag, dropdown, typing, clearing)
      // is the user taking that chart over: pin it so Auto leaves it alone.
      const { followScan: _f, ...cur } = cells[i]; // a hand-made assignment turns Follow scan Off
      cells[i] = { ...cur, sym: normalized, ...(st.chartGrid.auto?.on ? { pinned: true } : {}) };
      return {
        chartGrid: { ...st.chartGrid, cells },
        gridRev: st.gridRev + 1,
        // A fresh assignment deserves a fresh attempt — don't carry over
        // whatever error the symbol's PREVIOUS occupancy of some cell left behind.
        ...(normalized ? { chartErrBySym: dropChartErr(st.chartErrBySym, normalized) } : {}),
      };
    }),
  setCellTf: (i, tf) =>
    set((st) => {
      if (i < 0 || i >= st.chartGrid.cells.length) return {};
      const cells = st.chartGrid.cells.slice();
      cells[i] = { ...cells[i], tf };
      return { chartGrid: { ...st.chartGrid, cells }, gridRev: st.gridRev + 1 };
    }),
  // Server-echoed layout (chartgrid.get answer, or another window's edit
  // broadcast via fanoutExcept — the sender never hears its own set back).
  // Deliberately does NOT bump gridRev: adopting a remote layout must not
  // re-save it, or two open charts windows would ping-pong forever.
  applyGridFromServer: (layout) =>
    set(() => {
      const g = normalizeGrid(layout);
      return g ? { chartGrid: g } : {};
    }),
  setSideBoxes: (boxes) => set((st) => ({ chartGrid: { ...st.chartGrid, boxes }, gridRev: st.gridRev + 1 })),
  setAutoFill: (patch) =>
    set((st) => {
      const cur: ChartAutoSettings = st.chartGrid.auto ?? { on: false, scanId: null, metric: 'chgDay' };
      return { chartGrid: { ...st.chartGrid, auto: { ...cur, ...patch } }, gridRev: st.gridRev + 1 };
    }),
  toggleCellPin: (i) =>
    set((st) => {
      const c = st.chartGrid.cells[i];
      if (!c) return {};
      const cells = st.chartGrid.cells.slice();
      const { pinned: was, followScan: _f, ...rest } = c; // pinning turns Follow scan Off
      cells[i] = was ? { ...rest, ...(c.followScan ? { followScan: c.followScan } : {}) } : { ...rest, pinned: true };
      return { chartGrid: { ...st.chartGrid, cells }, gridRev: st.gridRev + 1 };
    }),
  setCellFollow: (i, scanId) =>
    set((st) => {
      const c = st.chartGrid.cells[i];
      if (!c) return {};
      const cells = st.chartGrid.cells.slice();
      const { followScan: _f, ...rest } = c;
      cells[i] = scanId ? { ...rest, followScan: scanId } : rest;
      return { chartGrid: { ...st.chartGrid, cells }, gridRev: st.gridRev + 1 };
    }),
  followCell: (scanId, sym) =>
    set((st) => {
      const cells = applyScanFollow(st.chartGrid.cells, scanId, sym);
      if (cells === st.chartGrid.cells) return {};
      return {
        chartGrid: { ...st.chartGrid, cells },
        gridRev: st.gridRev + 1,
        chartErrBySym: dropChartErr(st.chartErrBySym, sym.trim().toUpperCase()),
      };
    }),
  applyAutoCells: (syms, changed) =>
    set((st) => {
      const cells = st.chartGrid.cells.slice();
      let dirty = false;
      let errs = st.chartErrBySym;
      for (const i of changed) {
        const c = cells[i];
        const sym = syms[i] ?? null;
        // Re-check at apply time: a hand edit (pin) between the step and now wins.
        if (!c || c.pinned || c.followScan || c.sym === sym) continue;
        cells[i] = { ...c, sym }; // keeps the cell's own timeframe
        if (sym) errs = dropChartErr(errs, sym);
        dirty = true;
      }
      if (!dirty) return {};
      return { chartGrid: { ...st.chartGrid, cells }, gridRev: st.gridRev + 1, chartErrBySym: errs };
    }),
  // chart.data routed into the grid cache — for symbols actually in a cell,
  // OR one of the Signal window's own assigned symbols (that pane has no
  // server-persisted grid of its own — see `signalSyms`'s comment above) —
  // so the main window (whose grid is empty and whose signalSyms is empty)
  // stores nothing. applyChartBar already folds live ticks into any
  // barsBySym key that exists, and pruneChartData(keep) is what a pane calls
  // on a symbol switch to drop the old one. A fresh reply always supersedes
  // any earlier failure for the same symbol.
  setSymChartData: (sym, bars, prevClose) =>
    set((st) => {
      if (!sym || (!st.chartGrid.cells.some((c) => c.sym === sym) && !st.signalSyms.includes(sym))) return {};
      return {
        barsBySym: { ...st.barsBySym, [sym]: bars },
        prevCloseBySym: { ...st.prevCloseBySym, [sym]: prevClose || null },
        chartErrBySym: dropChartErr(st.chartErrBySym, sym),
      };
    }),
  setSymChartErr: (sym, msg) =>
    set((st) => (st.chartErrBySym[sym] === msg ? {} : { chartErrBySym: { ...st.chartErrBySym, [sym]: msg } })),
  clearSymChartErr: (sym) =>
    set((st) => {
      const chartErrBySym = dropChartErr(st.chartErrBySym, sym);
      return chartErrBySym === st.chartErrBySym ? {} : { chartErrBySym };
    }),
  pruneChartData: (keep) =>
    set((st) => {
      const keepSet = new Set(keep);
      let changed = false;
      const barsBySym: Record<string, Bar[]> = {};
      for (const k in st.barsBySym) {
        if (keepSet.has(k)) barsBySym[k] = st.barsBySym[k];
        else changed = true;
      }
      const prevCloseBySym: Record<string, number | null> = {};
      for (const k in st.prevCloseBySym) {
        if (keepSet.has(k)) prevCloseBySym[k] = st.prevCloseBySym[k];
        else changed = true;
      }
      const chartErrBySym: Record<string, string> = {};
      for (const k in st.chartErrBySym) {
        if (keepSet.has(k)) chartErrBySym[k] = st.chartErrBySym[k];
        else changed = true;
      }
      return changed ? { barsBySym, prevCloseBySym, chartErrBySym } : {};
    }),

  // --- morning review window (?pane=review) ---
  setReviewSession: (session) => set((st) => ({ review: { ...st.review, session } })),
  reviewPending: () => set((st) => ({ review: { ...st.review, loading: true } })),
  reviewFailed: () => set((st) => ({ review: { ...st.review, loading: false } })),
  setReviewData: (date, session, dates, rows) =>
    set((st) => {
      const review: ReviewState = { ...st.review, dates };
      if (session === 'regular') {
        const rthCloseBySym: Record<string, number> = {};
        for (const r of rows) if (r.close > 0) rthCloseBySym[r.sym] = r.close;
        review.rthCloseBySym = rthCloseBySym;
        review.rthCloseDate = date || null;
      }
      if (session === st.review.session) {
        review.date = date || null;
        review.rows = rows;
        review.loading = false;
      }
      return { review };
    }),

  // --- signal window (?pane=signal) ---
  setSignalSyms: (syms) =>
    set(() => {
      const out: string[] = [];
      for (const raw of syms) {
        const s = (raw ?? '').trim().toUpperCase();
        if (s && !out.includes(s)) out.push(s);
      }
      return { signalSyms: out };
    }),

  // --- today's blacklist ---
  setBlacklist: (blacklist) => set({ blacklist, blacklistAt: Date.now() }),
}));
