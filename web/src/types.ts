// Wire types mirroring internal/core/wsmsg.go.
export type Sym = string;

// One OHLC candle for the inline chart (matches backend core.Bar).
export interface Bar {
  t: number; // bar start, epoch ms
  o: number;
  h: number;
  l: number;
  c: number;
  v: number;
}

// A row's cell values: scalars, or the `bars` candle series for the chart cell.
export type Row = Record<string, number | boolean | string | Bar[] | null>;

// --- charts monitor window (?pane=charts) ---

// Per-cell timeframe of the monitor grid (client-side roll-up of 1-min bars).
export type ChartTf = '1m' | '5m' | '15m';

// One cell of the monitor grid: which symbol it shows (null = empty) and at
// what timeframe.
export interface ChartCell {
  sym: string | null;
  tf: ChartTf;
  // Auto mode (ChartAutoSettings): a pinned cell is never touched by Auto.
  // Set by the cell's pin toggle, or automatically when the user assigns a
  // symbol by hand while Auto is on. Absent = not pinned.
  pinned?: boolean;
  // Follow scan: the cell switches to each new alert / entrant of this scan
  // (chartFollow.ts). Absent = Off. Manual symbol or pin turns it Off.
  followScan?: string;
}

// The whole monitor-grid layout. Persisted server-side as an OPAQUE blob
// (chartgrid.set / chartgrid.data) — the client owns this shape, the server
// only stores and echoes it.
export interface ChartGridLayout {
  rows: number;
  cols: number;
  cells: ChartCell[]; // length rows*cols, row-major
  // The gainers/decliners lists under the watchlist (SideBoxes.tsx). Absent
  // in layouts saved before they existed — the component then shows its
  // defaults until the user edits one.
  boxes?: SideBox[];
  // Auto-fill of the cells with a scan's top gainers (autoFill.ts). Absent in
  // layouts saved before it existed = off.
  auto?: ChartAutoSettings;
}

// Settings for the charts window's Auto mode. scanId null = "use the default"
// (first side box's scan, else the first scan - see resolveAutoScan).
export interface ChartAutoSettings {
  on: boolean;
  scanId: string | null;
  metric: SideBoxMetric;
}

// One gainers/decliners list in the charts window: a scan of the scanner's
// own, shown as its top N rows by the metric, rising (desc) or falling (asc).
export type SideBoxMetric = 'chgDay' | 'chg5m';
export interface SideBox {
  id: string;
  scanId: string | null; // null until the user picks a scan
  dir: 'desc' | 'asc'; // desc = gainers, asc = decliners
  metric: SideBoxMetric;
  limit: number;
}

// --- morning review window (?pane=review) ---

// Wire label for a completed sub-session (matches backend core.Session's
// String()/ParseSession — "pre" | "regular" | "post"; "closed" is a legal
// round-trip value on the backend but never requested from here).
export type ReviewSession = 'pre' | 'regular' | 'post';

// One symbol's RAW stats over a completed sub-session (mirrors backend
// core.SessionSnapshotRow verbatim — same field names, same "raw, never a
// percentage" contract). Every derived number (gain%, day change, ...) is
// computed client-side from these plus the sibling sessions' rows; see
// deriveReviewGainPct in store.ts for the one place that happens.
export interface ReviewRow {
  sym: string;
  hi: number;
  lo: number;
  close: number;
  vwap: number;
  vol: number;
  prevClose: number;
}

export interface Filter {
  op?: string;
  conds?: Filter[];
  field?: string;
  cmp?: string;
  value?: unknown;
}

export interface SortKey {
  field: string;
  dir: string;
}

export interface AlertConfig {
  enabled: boolean;
  on: string;
  trigger?: Filter | null;
  sound: string;
  cooldownSec: number;
  requireReentry: boolean;
  // Say "SYM: scan name" out loud after the tone (ws.ts, e9.ts alertUtterance).
  // Absent on scans saved before it existed = off; the server omits it when false.
  speak?: boolean;
  // Server-side: move the linked TWS window to the symbol when this scan alerts
  // (display-group link only, never an order). Absent = off; omitted when false.
  sendToTws?: boolean;
}

export interface ScanDef {
  id: string;
  name: string;
  enabled: boolean;
  filter: Filter;
  sort: SortKey[];
  limit: number;
  columns: string[];
  alert: AlertConfig;
}

export interface TickFrame {
  id: string;
  seq: number;
  asOf: number;
  full: boolean;
  order: Sym[];
  rows: Record<Sym, Row>;
}

export interface AlertFrame {
  scanId: string;
  sym: Sym;
  kind: string;
  at: number;
  sound: string;
  row: Row;
}

// Health of one upstream data source, keyed by source id (e.g. "massive",
// "bzpush") in StatusFrame.sources. The backend is gaining this field in
// parallel with the rest of this UI — an absent map, or an absent entry for
// a given source, is normal and must render fine.
export interface SourceHealth {
  state: string; // ok | failing | off
  msg?: string;
  since?: number; // epoch ms the current state began
  lastOk?: number;
}

export interface StatusFrame {
  session: string;
  feed: string;
  tracked: number;
  msgRate: number;
  baseline: string;
  hasKey: boolean;
  clockMs: number;
  counts?: Record<string, number>; // live match count per scan id, for the tab badges
  twsLink?: string; // off | linked | waiting — IBKR TWS display-group link state
  sources?: Record<string, SourceHealth>; // per-source feed health; absent = not reported yet
}

export interface NewsItem {
  id: string;
  tickers: string[];
  headline: string;
  source: string;
  url: string;
  ts: number;
  sentiment: string;
  teaser?: string;
  hasBody?: boolean; // full article cached server-side (best summaries)

  // RTPR Impact Score (Beta) — arrives ~45s after publication, so it usually
  // attaches to an article already on screen. Absent = not scored (RTPR only
  // scores wire items clearing the account's Connect-tab threshold).
  score?: number;
  scoreTier?: string;
  eventType?: string;
  direction?: string; // bullish | bearish | neutral
}

// Daily-chart trend context for one symbol (nightly classification).
export interface TrendRef {
  dtrend: number; // 1 up / 0 flat / -1 down
  chg20d: number; // % over ~20 trading days
  vsSma: number; // % distance of last close from the SMA
  days: number;
  asOf: string;
}

// Summary lifecycle for one article. Two backends can produce `text` — the
// free, always-on local extractive engine, or an opt-in Claude upgrade —
// `model` names which ("local" | a real Claude model id) so the UI can tag
// the result and knows whether the Claude upgrade is still on offer. Absent
// only before any summary has ever been requested/rendered for this article.
export interface SummaryState {
  loading: boolean;
  text?: string;
  model?: string;
  err?: string;
}

// A scan-entry event (server frame scan.enter) or fired alert, for the log.
export interface ActivityEvent {
  seq: number;
  scanId: string;
  sym: string;
  at: number; // epoch ms
  last: number;
  kind: string; // ENTERED | CROSS
  count: number; // times this sym has appeared today
  hasNews: boolean; // did the row already have a fresh headline at entry?
}

// One reporting period's SEC-filed figures (matches backend core.FinQuarter).
export interface FinQuarter {
  fy: number;
  fq: number;
  periodEnd: string; // YYYY-MM-DD
  filingDate: string;
  revenue?: number | null;
  cash?: number | null;
  operatingCF?: number | null; // negative = burning cash
}

// One notable print on the tape (matches backend core.Print).
export interface TapePrint {
  ts: number;
  price: number;
  size: number;
  side: string; // buy | sell | neutral
  notional: number;
}

// One flushed batch (~4 Hz) of raw time-and-sales prints for a tape-watched
// symbol (matches core.TapePrintsMsg) — the per-trade feed behind the pop-out
// tape window's scrolling tape, distinct from TapeStat.prints (notable prints
// only). Prints are oldest first within the batch; dropped counts prints lost
// to backpressure since the last delivered batch, so the UI can render an
// honest gap marker instead of silently missing tape.
export interface TapePrintsFrame {
  sym: string;
  prints: TapePrint[]; // oldest first
  dropped?: number;
}

// One sample of the cumulative-volume-delta chart (matches core.CVDPoint).
export interface CVDPoint {
  ts: number; // epoch ms
  value: number;
}

// Inside-size dynamics + depth-imbalance bundle for the tape reader (matches
// core.BookStats). bidAbsorb/askAbsorb/Add/Pull/Up/Down are windowed (~30s)
// event counts; Refills/AbsorbedVol are live (unwindowed) and describe the
// CURRENT inside level, resetting whenever that price itself changes.
export interface BookStats {
  bidAbsorb: number; // bid hit by aggressive trades, then refreshed (the classic "refreshing bid")
  askAbsorb: number;
  bidAdd: number; // size added to a quiet quote nobody is hitting — weak signal, not absorption
  askAdd: number;
  bidPull: number; // size vanished with no trade — that side pulling away
  askPull: number;
  bidUp: number; // inside quote stepped to a new price, toward or away
  bidDown: number;
  askUp: number;
  askDown: number;
  bidRefills: number;
  askRefills: number;
  bidAbsorbedVol: number; // volume sold into the current bid while it held
  askAbsorbedVol: number; // volume bought from the current offer while it held
  bidDepth: number; // summed size across the visible L2 bid ladder
  askDepth: number;
  depthImbalance?: number; // -1 (offer heavy) .. +1 (bid heavy); absent when there is no depth data
  insideImbalance: number; // same, but from the L1 bid/ask sizes only
}

// The "who is in control" read — rules-based (source "local"), or Claude's
// structured read normalized into the same shape (source "api") (matches
// core.TapeVerdict).
export interface TapeVerdict {
  control: string; // buyers | sellers | balanced
  confidence: number;
  text: string; // short, speakable summary
  evidence?: string[]; // supporting detail, shown on screen only
  source: string; // local | api
}

// One price/size rung of an L2 book ladder, best level first (matches
// core.DepthLevel).
export interface DepthLevel {
  price: number;
  size: number;
}

// ~1 Hz order-flow digest for the watched symbol (matches core.TapeStat).
export interface TapeStat {
  sym: string;
  asOf: number;
  watching: boolean;
  watchSecs: number;
  quoteRule: boolean; // false = tick-rule fallback (no NBBO on this plan)
  bid: number;
  ask: number;
  delta1m: number;
  delta5m: number;
  deltaTotal: number;
  buyPct5m: number;
  sellPct5m: number;
  avgSize5m: number;
  rate1m: number;
  totalTrades: number;
  prints?: TapePrint[];
  control: number; // -1 sellers .. +1 buyers
  err?: string;

  // --- tape-reader bundle: regime detection, verdicts, depth (ported from
  // the standalone Python app's engine.py) ---
  delta10s: number; // buy vol - sell vol, last 10s
  ratio1s: number; // delta as share of that window's volume, -1..+1
  ratio10s: number;
  ratio60s: number;
  printsPerSec: number; // trades/sec, last 1s
  printsPerSecBaseline: number; // trades/sec, trailing 60s
  blockThreshold: number; // size at/above which a print is flagged as a block
  medianSize: number; // median print size, trailing prints
  cvdSeries?: CVDPoint[];
  book: BookStats;
  verdict: TapeVerdict; // rules-based (or Claude) "who is in control" read
  claudeEnabled: boolean; // Claude regime reads on for this symbol
  commentarySecs: number; // periodic Claude commentary interval; 0 = off
  hasDepth: boolean;
  bookBids?: DepthLevel[]; // top-of-book L2 ladder, best first
  bookAsks?: DepthLevel[];
  depthNote?: string; // why depth is unavailable, straight from IBKR
}

// Instant, free local-verdict alert fired the moment a regime shift is
// detected, before any Claude read completes — see TapeCard for the (possibly
// upgraded) follow-up (matches core.TapeRegimeMsg).
export interface TapeRegimeFrame {
  sym: string;
  ts: number;
  trigger: string;
  headline: string;
  verdict: TapeVerdict;
}

// A completed tape-reading card for a regime event — either the instant local
// verdict standing in alone, or Claude's structured read once it returns
// (model distinguishes which). control is the same buyers/sellers/balanced
// verdict TapeVerdict.control carries. err carries a failed Claude call, so
// the local verdict already sent via TapeRegimeFrame remains the
// authoritative read (matches core.TapeCardMsg).
export interface TapeCard {
  sym: string;
  ts: number;
  trigger: string;
  headline: string;
  model: string; // "local" | the Claude model id
  control: string; // buyers | sellers | balanced
  confidence: number;
  whatsHappening: string;
  evidence?: string[];
  watchFor?: string;
  err?: string;
}

// Real-time IBKR borrow availability for the selected symbol (TWS generic
// tick 236). Costs one market-data line, so only one symbol at a time.
export interface ShortLive {
  sym: string;
  indicator: number; // >2.5 = 1000+ shares, >1.5 = locatable, <=1.5 = no; 0 = pending
  shares: number; // exact count, -1 = not reported
  at: number;
  err?: string;
}

// Claude's interpretation of the current tape.
export interface TapeReadout {
  sym: string;
  text?: string;
  err?: string;
  at: number;
}

// Claude's structured read of ONE bull-flag / tight-consolidation setup
// (matches core.SetupRead, the setup.read message). Pushed unprompted when a
// symbol enters a scan that filters on the nightly flag fields — the
// deterministic pattern pass has already done the whole-market filtering, so
// only a shortlist ever gets read.
//
// quality is 1 (broken) .. 5 (textbook) on a successful read and 0 ONLY on a
// failed one, which sets err instead: every failure path still sends a
// populated read, so the panel can say why rather than showing nothing.
//
// asOf is the flag geometry's date this read describes (and the date half of
// its cache key), NOT when the read ran — that is ts, unix millis.
export interface SetupRead {
  sym: string;
  ts: number;
  quality: number;
  verdict: string;
  whatsHappening: string;
  risks?: string[];
  invalidation?: string;
  model: string;
  err?: string;
  asOf: string;
}

// SEC-filed fundamentals for one symbol, fetched on selection (matches
// backend core.Fundamentals).
export interface Fundamentals {
  sym: string;
  quarters: FinQuarter[]; // newest first, up to 4
  burnQuarterly?: number | null; // $ burned last quarter; absent if cash-flow positive
  runwayMonths?: number | null;
  asOf: number;
  source?: string;
}

// --- Day Log window (?pane=daylog): daylog.get / daylog.data ---
// Every scan alert of an ET day as the server recorded it (internal/daylog),
// oldest first. null = not known (no row, no bars yet). out5/out15/out30 are the
// % price change that many minutes after the alert, maxUp / maxDown the best /
// worst % inside 30 minutes (>= 0 / <= 0); done = the outcome pass finished.
export interface DayAlert {
  id: number;
  day: string; // 'YYYY-MM-DD' (ET)
  at: number; // ms
  sym: string;
  scanId: string;
  scanName: string;
  kind: string; // enter | cross
  price: number | null;
  chgDay: number | null;
  vol: number | null;
  rvol: number | null;
  newsHeadline: string;
  newsAge: number | null; // seconds between the headline and the alert
  note: string;
  blacklisted: boolean;
  out5: number | null;
  out15: number | null;
  out30: number | null;
  maxUp: number | null;
  maxDown: number | null;
  done: boolean;
}
// One line of the end-of-day top gainers (side 'gain') / losers (side 'loss').
export interface DayMover {
  day: string;
  side: 'gain' | 'loss';
  rank: number;
  sym: string;
  price: number | null;
  chgDay: number | null;
  vol: number | null;
  headline: string;
  takenAt: number;
}
export interface DayLogFrame {
  date: string;
  dates: string[]; // newest first, always includes today
  alerts: DayAlert[];
  movers: DayMover[];
  csvDir: string; // where the server writes <date>.csv
}

// --- Signal window: flag-scan signal history (flagevents.*) ---
// Every signal a flag-scan stock made, per ET day, persisted by the server.
// 'impulse' is every candidate's breakout (noise in a history list: the UI
// drops it, see flagEvents.ts). 'slam' is the 9 EMA SLAM (a bearish alert out
// of a flag or a long, emitted after the FLAG FAILED / EXIT of its bar).
// The market-wide 1-minute alerts share the list (the chart rules' own kinds):
// 'dildo' GREEN DILDO, 'dildoPb' 9 EMA PULLBACK, 'dildoCancel' PB CANCELLED,
// 'climax' VOLUME CLIMAX, 'bottom' BOTTOM CONFIRMED, 'climaxFail' CLIMAX FAILED,
// 'bbs' BOUNCY BALL SHORT. 'ignition' IGNITION.
export type FlagEventKind = 'impulse' | 'flat' | 'emaNear' | 'entry' | 'add' | 'earlyBreak' | 'failed' | 'exit' | 'slam' | 'dildo' | 'dildoPb' | 'dildoCancel' | 'climax' | 'bottom' | 'climaxFail' | 'bbs' | 'ignition' | 'power' | 'powerSetup' | 'volBreak' | 'volBreakEntry' | 'vwapBull' | 'vwapBear' | 'vwapEarlyUp' | 'vwapEarlyDn';
export interface FlagEvent {
  ts: number; // bar start, ms
  at: number; // server ms when recorded
  sym: string;
  kind: FlagEventKind;
  label: string;
  price: number | null;
  note?: string; // the dildo / climax rows: one line of what the rule measured
}
// flagevents.data (the answer to flagevents.get: the whole viewed day, oldest
// first, plus the list of days) and flagevents.add (live: only the NEW events
// of today; dates present when the day list changed).
export interface FlagEventsFrame {
  date: string; // 'YYYY-MM-DD' (ET)
  dates?: string[]; // newest first, <= 30, always includes today
  events: FlagEvent[];
}
