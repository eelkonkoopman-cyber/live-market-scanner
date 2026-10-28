import { useStore } from './store';
import { emitFollowAlert, emitScanAlert } from './signalFollow';
import { play, silence } from './audio';
import { speak, speakRegimeAlert, speakCard, stopSpeaking } from './voice';
import { alertUtterance } from './e9';
import { dildoNewsSuffix } from './dildoNews';
import { dildoSharesSuffix } from './positionSize';
import { earlyDildoUtterance } from './dildoEarly';
import { freshIgnitionUtterance } from './ignitionFresh';
import { powerSetupUtterance } from './powerSetup';
import { volBreakUtterance } from './volBreak';
import { isBlacklisted, normalizeBlacklist } from './blacklist';
import { twsAutoToast } from './twsAuto';
import { parseTwsFollow } from './twsFollow';
import { applyServerDildoParams, registerDildoParamsSender } from './signalLayout';
import type { TickFrame, ReviewSession } from './types';

let ws: WebSocket | null = null;
let backoff = 500;
let started = false;
const alive = true;
const lastSeq: Record<string, number> = {};

// start opens the connection once (guarded against React StrictMode double-mount).
export function start(): void {
  if (started) return;
  started = true;
  stopQueuedSoundOnMute();
  connect();
}

// stopQueuedSoundOnMute: the Signal window's 🔇 and '🗣 Voice' off (mirrored
// into the store by SignalPane) gate every NEW alert frame in dispatch's alert
// case; turning one on also stops what is already queued (rv2-web-1). Tones
// play one after another up to audio.ts's MAX_TONE_BACKLOG_S ahead and up to
// voice.ts's MAX_ALERT_LINES spoken alert lines queue, so a burst at one
// minute's close kept playing and talking for 20-30 s after the click. Only
// on the change to on: both flags stay false in every other window.
function stopQueuedSoundOnMute(): void {
  useStore.subscribe((s, prev) => {
    if (s.signalSoundMuted && !prev.signalSoundMuted) {
      silence();
      stopSpeaking();
    } else if (s.signalVoiceOff && !prev.signalVoiceOff) {
      stopSpeaking();
    }
  });
}

function connect(): void {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  ws = new WebSocket(`${proto}://${location.host}/stream`);
  ws.onopen = () => {
    backoff = 500;
    useStore.getState().setFeed('up');
    send({ t: 'hello' });
    // Re-arm every currently-open tile (not just one) after every (re)connect,
    // so a reconnect restores every tiled scan, not just whichever was last active.
    const tiles = useStore.getState().openTiles;
    if (tiles.length) {
      viewTiles(tiles);
      tiles.forEach(resyncScan);
    }
  };
  ws.onclose = () => {
    useStore.getState().setFeed('down');
    if (alive) setTimeout(connect, backoff);
    backoff = Math.min(backoff * 2, 8000);
  };
  ws.onerror = () => ws?.close();
  ws.onmessage = (e) => dispatch(JSON.parse(e.data as string));
}

export function send(f: unknown): void {
  trySend(f);
}

// trySend is send() that says whether the frame actually went out - for the
// callers that must not pretend a click was heard.
function trySend(f: unknown): boolean {
  if (ws && ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify(f));
    return true;
  }
  return false;
}

// The scans this window last asked the server for (viewTiles / viewActive):
// null = it never narrowed its view, empty = it asked for none. The server
// reads an empty view as "every scan" and sends that window every scan's ticks
// AND alert frames.
let viewed: ReadonlySet<string> | null = null;
let viewedAlertsOnly = false;

// hearOnlyViewedScans is for a window that tiles no scan of its own - the
// tape, charts, review and signal pop-outs (main.tsx). Such a window keeps an
// empty view on purpose (the tape window's symbol panel searches every scan's
// rows) or narrows it to a few scans of its own, and it must not play or speak
// the alerts of scans it never asked for: the tape reader, open all day, said
// every "SYM: green dildo" a second time behind the Signal window, out of
// reach of that window's mute and voice buttons. From the call on, the window
// hears exactly the scans in its view (none while the view is empty). The main
// window and a single-scan pop-out never call it and keep "no view = every
// scan".
export function hearOnlyViewedScans(): void {
  viewedAlertsOnly = true;
}

// alertHeard says whether this window plays and speaks an alert of scanId:
// a scan in its view, or - with no narrowed view - any scan, unless the window
// hears only the scans it asked for. Checked on the client too because the
// server's view of this connection is empty again after every reconnect until
// the window re-sends it.
function alertHeard(scanId: string): boolean {
  if (viewed && viewed.size > 0) return viewed.has(scanId);
  return !viewedAlertsOnly;
}

// viewActive tells the server to stream ONLY this scan and sends a baseline
// resync — used by a pop-out single-scan window, which only ever tiles one.
export function viewActive(id: string): void {
  delete lastSeq[id];
  viewed = new Set([id]);
  send({ t: 'view.set', d: { scanIds: [id] } });
  send({ t: 'scan.resync', d: { id } });
}

// viewTiles tells the server the FULL set of scans this window wants ticks
// for (every currently-open tile in the main window) — the server's view.set
// already accepts multiple ids.
export function viewTiles(ids: string[]): void {
  viewed = new Set(ids);
  send({ t: 'view.set', d: { scanIds: ids } });
}

// resyncScan requests a full snapshot for one scan (a newly opened tile, or
// recovering from a seq gap).
export function resyncScan(id: string): void {
  delete lastSeq[id];
  send({ t: 'scan.resync', d: { id } });
}

export function upsertScan(def: unknown): void {
  send({ t: 'scan.upsert', d: def });
}

export function deleteScan(id: string): void {
  send({ t: 'scan.delete', d: { id } });
}

export function setApiKey(key: string): void {
  send({ t: 'settings.setKey', d: { key } });
}

// Per-symbol chart.get watchdog: a grid cell whose reply never arrives
// (dropped frame, backend hang, or an old server sending an error with no
// symbol to blame) would otherwise sit on "Loading SYM…" forever. 15s is
// generous for a same-machine WS round trip; re-arming (a retry, a re-fetch
// on reconnect) simply replaces whatever timer was already running.
const CHART_TIMEOUT_MS = 15_000;
const chartTimers = new Map<string, ReturnType<typeof setTimeout>>();

function armChartTimer(sym: string): void {
  clearTimeout(chartTimers.get(sym));
  chartTimers.set(
    sym,
    setTimeout(() => {
      chartTimers.delete(sym);
      useStore.getState().setSymChartErr(sym, 'no response from the scanner');
    }, CHART_TIMEOUT_MS),
  );
}

function clearChartTimer(sym: string): void {
  clearTimeout(chartTimers.get(sym));
  chartTimers.delete(sym);
}

// getChart requests today's intraday bars for the click-to-expand chart.
export function getChart(sym: string): void {
  armChartTimer(sym);
  send({ t: 'chart.get', d: { sym } });
}

// getFundamentals requests SEC-filed revenue/cash/burn for a symbol.
export function getFundamentals(sym: string): void {
  send({ t: 'fundamentals.get', d: { sym } });
}

// getNewsFor pulls a symbol's archived news history (last 30 days server-side)
// into the panel — the live list only reaches back to page load.
export function getNewsFor(sym: string): void {
  send({ t: 'news.for', d: { sym } });
}

// getTrendFor requests the selected symbol's daily-trend context.
export function getTrendFor(sym: string): void {
  send({ t: 'trend.get', d: { sym } });
}

// watchTape points the server's tape engine at a symbol ('' stops watching,
// which unsubscribes the trade/quote channels).
export function watchTape(sym: string): void {
  send({ t: 'tape.watch', d: { sym } });
}

// selectSymbol is what a click in the charts window does: make `sym` this
// window's selection (other windows following selections pick it up via
// openChart) and make sure the server hears it even when it already WAS the
// selection — TWS may have been moved by another window since, and
// ChartsPane's watchTape effect only fires when chartSym changes.
export function selectSymbol(sym: string): void {
  const st = useStore.getState();
  if (st.chartSym === sym) {
    if (st.feed === 'up') watchTape(sym);
    return;
  }
  st.openChart(sym);
}

// readTape asks Claude to interpret the current order flow.
export function readTape(sym: string): void {
  useStore.getState().tapeReadPending();
  send({ t: 'tape.read', d: { sym } });
}

// setTapeClaude toggles Claude regime reads on/off for a symbol.
export function setTapeClaude(sym: string, on: boolean): void {
  send({ t: 'tape.claudeSet', d: { sym, enabled: on } });
}

// setTapeCommentary sets the periodic live-commentary interval for a symbol,
// in seconds (0 = off).
export function setTapeCommentary(sym: string, secs: number): void {
  send({ t: 'tape.commentarySet', d: { sym, seconds: secs } });
}

// getRow requests a one-off row snapshot for a symbol — used by a pop-out
// tape window (no scan tiled) to get header stats for its watched symbol.
export function getRow(sym: string, columns: string[]): void {
  send({ t: 'row.get', d: { sym, columns } });
}

// --- charts monitor window (?pane=charts) ---

// watchCharts replaces this window's whole live-chart symbol SET (the charts
// grid's counterpart of tape.watch). Drives only the ingest chart tap — no
// trade/quote subscriptions, and never moves the TWS/depth target.
export function watchCharts(syms: string[]): void {
  send({ t: 'chart.watch', d: { syms } });
}

// getWatchlist requests the persisted watchlist (it also rides hello, so this
// mainly covers reconnects).
export function getWatchlist(): void {
  send({ t: 'watchlist.get' });
}

// sendWatchlist replaces the persisted watchlist. The server broadcasts the
// authoritative watchlist.data back to EVERY client (sender included) — the
// UI renders from that echo, never from a local optimistic apply.
export function sendWatchlist(syms: string[]): void {
  send({ t: 'watchlist.set', d: { syms } });
}

// getFlagEvents requests the Signal window's flag-scan signal history for one
// ET day ('' = today). The answer is flagevents.data; an older server never
// answers (unknown frame types are ignored), the list then keeps waiting.
export function getFlagEvents(date = ''): void {
  send({ t: 'flagevents.get', d: { date } });
}

// getAlertTws / setAlertTws: the global "TWS follow" switch (server-persisted;
// every window gets alerttws.data on a set).
export function getAlertTws(): void {
  send({ t: 'alerttws.get' });
}
export function setAlertTws(enabled: boolean): void {
  send({ t: 'alerttws.set', d: { enabled } });
}

// getDildoParams / setDildoParams: the market-wide GREEN DILDO tuning
// (server-persisted; every window gets dildo.params.data on a set).
export function getDildoParams(): void {
  send({ t: 'dildo.params.get' });
}
registerDildoParamsSender((d) => send({ t: 'dildo.params.set', d }));

// getChartGrid requests the persisted monitor-grid layout blob.
export function getChartGrid(): void {
  send({ t: 'chartgrid.get' });
}

// persistChartGrid saves the monitor-grid layout, debounced ~500ms so a burst
// of drags/clicks collapses to one chartgrid.set. The server persists it and
// broadcasts chartgrid.data to OTHER windows only (no sender self-echo).
let gridSaveTimer: ReturnType<typeof setTimeout> | undefined;
export function persistChartGrid(layout: unknown): void {
  clearTimeout(gridSaveTimer);
  gridSaveTimer = setTimeout(() => send({ t: 'chartgrid.set', d: { layout } }), 500);
}

// --- Day Log window (?pane=daylog) ---

// getDayLog requests one ET day of the Day Log ('' = today). snapshot asks the
// server to take the top gainers / losers snapshot right now first (today only).
// The answer is daylog.data.
export function getDayLog(date = '', snapshot = false): void {
  useStore.getState().dayLogPending();
  send({ t: 'daylog.get', d: { date, snapshot } });
}

// --- morning review window (?pane=review) ---

// getReview requests one persisted past-session snapshot: empty date = the
// newest date on hand, defaults to session 'post' (the after-hours list the
// morning watchlist is built from). When the requested session is 'post', this
// ALSO fetches that SAME date's 'regular'-session rows — the after-hours
// gain's baseline (this feature's named bug class: it must be THAT DAY's rth
// close, never prevClose, never the post session's own first print) — unless
// that date's baseline is already cached (store.review.rthCloseDate), so
// re-viewing AH after RTH for the same date doesn't re-fetch. An empty date
// always re-fetches the baseline too, since "newest" can resolve differently
// between the two calls only in the rare case new history lands mid-request —
// self-correcting on the next getReview either way.
export function getReview(date?: string, session?: ReviewSession): void {
  const sess = session ?? 'post';
  const d = date ?? '';
  useStore.getState().reviewPending();
  send({ t: 'review.get', d: { date: d, session: sess } });
  if (sess === 'post') {
    const st = useStore.getState();
    if (!d || st.review.rthCloseDate !== d) {
      send({ t: 'review.get', d: { date: d, session: 'regular' } });
    }
  }
}

// summarizeNews asks the server for the Claude summary of one article.
export function summarizeNews(id: string): void {
  useStore.getState().summaryPending(id);
  send({ t: 'news.summarize', d: { id } });
}

// --- Today's symbol blacklist (the Signal window's Blacklist section) ---
// Returns whether the frame was sent. A rejected request comes back as an
// `error` frame with code 'blacklist' (a toast); a successful one as the next
// 'blacklist' frame.
export function blacklistSet(sym: string, add: boolean): boolean {
  return trySend({ t: add ? 'blacklist.add' : 'blacklist.remove', d: { sym } });
}

/* eslint-disable @typescript-eslint/no-explicit-any */
function dispatch(f: any): void {
  const s = useStore.getState();
  switch (f.t) {
    case 'scan.list':
      s.setScans(f.d?.scans ?? []);
      break;
    case 'status':
      s.setStatus(f.d);
      break;
    case 'scan.tick':
      onTick(f.d as TickFrame);
      break;
    case 'alert':
      // Per-window, per-scan mute (mutedScans lives only in this window's own
      // store instance — see store.ts's doc comment on why). This gates AUDIO
      // ONLY: every other effect below (activity log, etc.) fires regardless
      // of mute state — muting silences sound, never the visual/log record.
      //
      // The Signal window's own toolbar (signalSoundMuted / signalVoiceOff,
      // mirrored into the store by SignalPane) gates the same path: that window
      // narrows its view to the flag scan, whose alerts reach it through here, so
      // its 🔊/🔇 and '🗣 Voice' buttons have to cover them. Both stay false in
      // every other window. So does its "Alerts" menu: a signal switched off
      // there (signalScansOff) is silent in that window, even while its left-bar
      // list keeps the scan in view. A window that tiles no scan hears only the
      // scans it asked for (alertHeard, hearOnlyViewedScans).
      // A symbol on today's blacklist is never heard, in any scan
      // (the user "just doesn't want to hear" it); its log record still lands.
      // Charts-grid cells that follow this scan hear it regardless of mute.
      if (!isBlacklisted(s.blacklist, s.blacklistAt, f.d?.sym ?? '', Date.now())) {
        emitScanAlert({ scanId: f.d?.scanId ?? '', sym: f.d?.sym ?? '' });
      }
      if (
        alertHeard(f.d?.scanId ?? '') &&
        !isBlacklisted(s.blacklist, s.blacklistAt, f.d?.sym ?? '', Date.now()) &&
        !s.mutedScans[f.d?.scanId ?? ''] &&
        !s.signalSoundMuted &&
        !s.signalScansOff.includes(f.d?.scanId ?? '')
      ) {
        emitFollowAlert({ scanId: f.d?.scanId ?? '', sym: f.d?.sym ?? '' });
        play(f.d?.sound ?? 'ping');
        // A scan with "Speak the symbol" on names the stock right after the
        // tone ("BEAT: nine E M A flag, ready") — the scan windows sit on spare
        // monitors, so the tone says "something fired" and the voice says
        // which. Same mute gate as the tone. `force` because this is the
        // scan's own opt-in, not the tape reader's shared narration pref
        // (default off), which would otherwise swallow it (voice.ts speak).
        if (!s.signalVoiceOff) {
          const plain = alertUtterance(s.scans, f.d?.scanId ?? '', f.d?.sym ?? '');
          // Green Dildo only: "ABCD: early green dildo" when the server judged
          // the dildo early (dildoEarly.ts), and ". News headline 10 seconds
          // ago" when the stock had a headline in the last hour (dildoNews.ts).
          // Ignition: "ABCD: fresh ignition" when it was fresh (ignitionFresh.ts).
          // Power Setup: "ABCD: power setup, entry 3.45, stop 3.24" (powerSetup.ts).
          // Vol Break: "ABCD: volume break 3.43 (4.2x vol), nine E M A 3.31" (volBreak.ts).
          const say = plain && volBreakUtterance(powerSetupUtterance(freshIgnitionUtterance(earlyDildoUtterance(plain, f.d?.scanId ?? '', f.d?.sym ?? '', f.d?.row), f.d?.scanId ?? '', f.d?.sym ?? '', f.d?.row), f.d?.scanId ?? '', f.d?.row), f.d?.scanId ?? '', f.d?.row);
          // Then the share count the window's position size buys at the
          // dildo's close: ". 1,250 shares." (positionSize.ts; 0 = off).
          const shares = dildoSharesSuffix(f.d?.scanId ?? '', f.d?.row, s.positionSize);
          if (say) speak(say + dildoNewsSuffix(f.d?.scanId ?? '', f.d?.sym ?? '', s.news, Date.now()) + (shares ? shares + '.' : ''), { force: true });
        }
      }
      if (f.d?.kind === 'cross') {
        // cross alerts aren't scan entries; log them separately. The row
        // snapshot only has a `news` field if the scan's columns include it.
        s.pushActivity({
          scanId: f.d?.scanId ?? '', sym: f.d?.sym ?? '', at: f.d?.at ?? Date.now(),
          last: typeof f.d?.row?.last === 'number' ? f.d.row.last : 0, kind: 'CROSS',
          hasNews: f.d?.row?.news === true,
        });
      }
      break;
    case 'tws.auto': {
      // The server's outcome for an alert of a scan with "Send to TWS when it
      // alerts" on. Broadcast to every window; only the main one (no ?pane=)
      // says so, once, instead of every pop-out repeating it.
      if (new URLSearchParams(location.search).get('pane')) break;
      const msg = twsAutoToast(f.d);
      if (msg) s.pushToast(msg);
      break;
    }
    case 'scan.enter':
      s.pushActivity({
        scanId: f.d?.scanId ?? '', sym: f.d?.sym ?? '', at: f.d?.at ?? Date.now(),
        last: f.d?.last ?? 0, kind: 'ENTERED', hasNews: f.d?.hasNews === true,
      });
      break;
    case 'news':
      s.pushNews(f.d?.items ?? []);
      break;
    case 'chart.data':
      // Both routes can apply to the same frame: the selected chart and a
      // monitor-grid cell may show the same symbol.
      clearChartTimer(f.d?.sym ?? '');
      s.setChartData(f.d?.sym ?? '', f.d?.bars ?? [], f.d?.prevClose ?? 0);
      s.setSymChartData(f.d?.sym ?? '', f.d?.bars ?? [], f.d?.prevClose ?? 0);
      break;
    case 'watchlist.data':
      s.setWatchlist(f.d?.syms ?? []);
      break;
    case 'daylog.data':
      if (f.d?.date) s.setDayLog(f.d);
      break;
    case 'flagevents.data':
      if (f.d?.date) s.setFlagEvents(f.d);
      break;
    case 'flagevents.add':
      if (f.d?.date) s.addFlagEvents(f.d);
      break;
    case 'alerttws.data':
      s.setTwsFollow(parseTwsFollow(f.d));
      break;
    case 'dildo.params.data':
      applyServerDildoParams(f.d);
      break;
    case 'chartgrid.data':
      s.applyGridFromServer(f.d?.layout ?? null);
      break;
    case 'fundamentals.data':
      s.setFundamentals(f.d);
      break;
    case 'trend.data':
      s.setTrend(f.d?.sym ?? '', f.d?.ref ?? null);
      break;
    case 'news.score':
      s.setNewsScore(f.d?.articleId ?? '', f.d?.score ?? 0, f.d?.tier ?? '', f.d?.eventType ?? '', f.d?.direction ?? '');
      break;
    case 'chart.bar':
      s.applyChartBar(f.d);
      break;
    case 'tape.stat':
      s.setTape(f.d);
      break;
    case 'tape.prints':
      // Raw time & sales batch for the pop-out tape window. The store applies
      // the same current-symbol stale-guard tape.stat uses.
      s.pushTapePrints(f.d);
      break;
    case 'short.live':
      s.setShortLive(f.d);
      break;
    case 'tape.readout':
      s.setTapeRead(f.d);
      break;
    case 'tape.regime':
      // Voice-only: the instant, free local-verdict alert. No store slice —
      // the (possibly upgraded) visual card follows via tape.card. Paused
      // mutes voice only (never ingestion) — nothing to skip here besides
      // the speak call itself.
      if (!s.tapePaused) speakRegimeAlert(f.d);
      break;
    case 'tape.card':
      // Always rendered (the reads feed keeps recording through a pause);
      // only the voice/blip narration is muted while paused.
      s.pushTapeCard(f.d);
      if (!s.tapePaused) speakCard(f.d, false, s.chartSym);
      break;
    case 'setup.read':
      // Claude's bull-flag read, pushed on scan entry (never requested from
      // here). The store drops it unless it's for the selected symbol.
      s.setSetupRead(f.d);
      break;
    case 'row.data':
      if (f.d?.row) s.setStandaloneRow(f.d?.sym ?? '', f.d.row);
      break;
    case 'review.data': {
      // Session label is server-controlled ("pre"|"regular"|"post"|"closed" —
      // core.Session.String()); anything unrecognized falls back to 'post'
      // rather than corrupting the store with a value the UI's segbtns don't
      // know how to render.
      const sessRaw = f.d?.session;
      const session: ReviewSession = sessRaw === 'pre' || sessRaw === 'regular' ? sessRaw : 'post';
      s.setReviewData(f.d?.date ?? '', session, f.d?.dates ?? [], f.d?.rows ?? []);
      break;
    }
    case 'blacklist':
      // Sent on hello and after every change.
      s.setBlacklist(normalizeBlacklist(f.d));
      break;
    case 'news.summary':
      s.setSummary(f.d?.id ?? '', f.d?.summary ?? '', f.d?.model ?? '', f.d?.err ?? '');
      break;
    case 'error':
      // A failed chart fetch resolves the modal's loading state (the toast below
      // explains why) instead of leaving it spinning forever.
      if (f.d?.code === 'chart' && s.chartSym) s.setChartData(s.chartSym, [], 0);
      // Same idea for the charts-monitor grid: the server tags the frame with
      // the symbol (d.sym) when it can, so only that cell is blamed. An older
      // backend won't send d.sym at all — with no symbol to pin the error on,
      // blame every cell still waiting on a chart.get rather than leaving some
      // of them stuck on "Loading" forever.
      if (f.d?.code === 'chart') {
        const sym: string | undefined = f.d?.sym;
        if (sym) {
          clearChartTimer(sym);
          s.setSymChartErr(sym, f.d?.msg ?? '');
        } else {
          for (const pending of [...chartTimers.keys()]) {
            clearChartTimer(pending);
            s.setSymChartErr(pending, f.d?.msg ?? '');
          }
        }
      }
      if ((f.d?.code === 'fundamentals' || f.d?.code === 'fundamentals-not-entitled') && s.chartSym) {
        s.setFundamentalsErr(s.chartSym, f.d?.msg ?? '', f.d?.code === 'fundamentals-not-entitled');
      }
      // A failed review.get resolves the pane's loading spinner instead of
      // leaving it stuck forever (the toast below explains why).
      if (f.d?.code === 'review') s.reviewFailed();
      if (f.d?.code === 'daylog') s.dayLogFailed(f.d?.msg ?? 'the Day Log request failed');
      // Not-entitled is expected and shown inline in the panel every time you
      // click a symbol — a toast on every click would be spam, not signal.
      if (f.d?.code !== 'fundamentals-not-entitled') {
        s.pushToast(`error: ${f.d?.msg ?? ''}`);
      }
      break;
  }
}

// Tick coalescing. One engine snapshot makes the scanner evaluate every scan
// in turn, so the ticks of all this window's viewed scans arrive back to back
// within a few milliseconds; applying each one as its own store update made
// the window render once per scan per snapshot. Ticks are queued for a short
// window and folded in ONE applyTicks call — at most ~25 render passes a second
// however many scans are tiled, at ≤ TICK_BATCH_MS of added latency. The gap
// check still runs on arrival so a resync request goes out immediately; the
// resulting full frame simply joins the queue in order.
const TICK_BATCH_MS = 40;
const pendingTicks: TickFrame[] = [];
let tickFlushTimer: ReturnType<typeof setTimeout> | null = null;

function flushTicks(): void {
  tickFlushTimer = null;
  if (pendingTicks.length === 0) return;
  const batch = pendingTicks.splice(0, pendingTicks.length);
  useStore.getState().applyTicks(batch);
}

function onTick(d: TickFrame): void {
  const prev = lastSeq[d.id];
  if (!d.full && prev !== undefined && d.seq !== prev + 1) {
    // gap detected -> request a full snapshot, drop this partial
    send({ t: 'scan.resync', d: { id: d.id } });
    return;
  }
  lastSeq[d.id] = d.seq;
  pendingTicks.push(d);
  if (tickFlushTimer === null) tickFlushTimer = setTimeout(flushTicks, TICK_BATCH_MS);
}
