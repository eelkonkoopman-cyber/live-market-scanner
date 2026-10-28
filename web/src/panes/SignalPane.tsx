import { Fragment, useEffect, useMemo, useRef, useState } from 'react';
import { parsePositionSize } from '../positionSize';
import { useStore, GLOBAL_SYM_KEY } from '../store';
import { start, getChart, watchCharts, getWatchlist, watchTape, viewTiles, resyncScan, getFlagEvents, getAlertTws, setAlertTws, getDildoParams } from '../ws';
import { StatusBar } from '../components/StatusBar';
import { applyFollow, subscribeFollowAlerts, type FollowKind } from '../signalFollow';
import { SignalCell } from '../components/SignalCell';
import { SignalPanel } from '../components/SignalPanel';
import { WatchlistSidebar } from '../components/WatchlistSidebar';
import { FlagScanList } from '../components/FlagScanList';
import { FlagEventList } from '../components/FlagEventList';
import { SignalSwitchMenu } from '../components/SignalSwitchMenu';
import {
  cellEventHeard,
  CLIMAX_ALERTS_KEY,
  DILDO_ALERTS_KEY,
  extraScanMutes,
  FLAG_LIST_KEY,
  isSignalSwitchId,
  parseFlagListPrefs,
  parseSignalSwitches,
  serializeSignalSwitches,
  SIGNAL_SWITCHES_KEY,
  signalAlertScanIds,
  signalScansOff,
  type SignalSwitches,
  type SignalSwitchId,
} from '../e9';
import { twsFollowView } from '../twsFollow';
import { todayET } from '../flagEvents';
import {
  loadSignalLayout,
  saveSignalLayout,
  makeSignalLayout,
  layoutSyms,
  loadFlagParams,
  saveFlagParams,
  SIGNAL_PRESETS,
  SIGNAL_PORTRAIT_PRESETS,
  type SignalLayout,
} from '../signalLayout';
import { DEFAULT_PARAMS, type SignalParams } from '../signals/shortFade';
import type { FlagParams } from '../signals/emaFlag';
import { eventOnSide, soundForEvent, voicePhrase, type CellRead, type SetupKind, type SignalSide, type ViewEvent } from '../signals/view';
import { play, playInTurn } from '../audio';
import { speak } from '../voice';
import { isBlacklisted } from '../blacklist';
import type { ChartTf } from '../types';

// SignalPane is the short-fade signal window (?pane=signal[&sym=]) — a GRID
// of signal charts as of addendum 2 ("I want to be able to view multiple
// stocks as well"), each cell (SignalCell) running the deterministic
// short-fade state machine (shortFade.ts) on its own symbol. This file owns:
//   - the grid's layout (signalLayout.ts) — localStorage-only, NOT the
//     server-persisted blob the charts monitor uses; see that module's own
//     comment for why one Signal window is the deliberately supported setup;
//   - all wire traffic for the grid's symbol set, mirroring ChartsPane.tsx's
//     declare-everything/re-assert pattern almost verbatim (one set of
//     symbols instead of one grid's), including the S5 fix (never send a
//     frame while the socket isn't open — getChart arms a 15s watchdog
//     regardless of whether anything was actually transmitted);
//   - the follow/pin symbol-selection mechanism, copied from the original
//     single-chart version of this pane / TapePane (localStorage
//     GLOBAL_SYM_KEY + the `storage` event), now targeting the ACTIVE cell
//     instead of the pane's one symbol;
//   - click-to-activate + click-to-link: unlike the original single-chart
//     pane (which deliberately did NOT drive TWS/depth/borrow — see its own
//     removed comment), the grid explicitly does on a cell click, "exactly
//     what MonitorChart's canvas click does" (addendum 2 §4) — openChart(sym)
//     plus the watchTape effect below, copied from ChartsPane.tsx;
//   - the shared Tuning params (one set for every cell); and
//   - the sound/voice diff across every cell's own result (§11: two new
//     tones — 'short'/'add', synthesised in audio.ts — plus a per-window
//     voice toggle, independent of the tape reader's shared preference;
//     addendum 4 adds the ascending 'long' tone and the long side's lines,
//     and applies each cell's own side selector: a cell showing only 'short'
//     never sounds a long event and vice versa — see signals/view.ts, which
//     also holds the pure label -> tone / event -> phrase tables).
//
// Iteration 6 gives every cell a SETUP (flag | pullback — signalLayout.ts), so
// a cell reports a CellRead (signals/view.ts: the engine's raw result, the
// engine-neutral view, and the setup/side it was built for) instead of a bare
// SignalResult. The diff below is otherwise the same loop: soundForEvent /
// voicePhrase handle both engines' events, a flag cell reports side 'both' (the
// side selector is a pullback concept), and the per-cell basis now includes the
// setup so switching it re-primes silently. The flag engine has its own Tuning
// params (one set for every flag cell, own localStorage key) next to the
// pullback's.
//
// The 9EMA flag MARKET SCAN's results sit in the left bar under the watchlist
// chips (components/FlagScanList.tsx): the scan's own rows, click to put one on
// the active chart (pickFlagSym — the same path as typing it in the toolbar),
// drag to drop one on any cell. THIS file owns which scan feeds it (remembered
// in localStorage) because choosing it also decides this window's scan view —
// see the narrowing effect below.

const SYM_RE = /^[A-Z][A-Z0-9.\-]{0,9}$/;
const PARAMS_KEY = 'mktscanner.signal.params.v1';
const VOICE_KEY = 'mktscanner.signal.voice.v1';
const SOUND_RECENT_MS = 10 * 60_000; // a reconnect back-fill must not replay the morning

function readGlobalSym(): string {
  try {
    return localStorage.getItem(GLOBAL_SYM_KEY) ?? '';
  } catch {
    return '';
  }
}

function validSym(raw: string): string | null {
  const s = raw.trim().toUpperCase();
  return SYM_RE.test(s) ? s : null;
}

// Merge over DEFAULT_PARAMS, try/catch like voice.ts's loadPrefs — a
// half-written or foreign-shaped blob degrades to the defaults for whatever
// fields it's missing, rather than crashing the pane.
function loadParams(): SignalParams {
  try {
    const raw = localStorage.getItem(PARAMS_KEY);
    return raw ? { ...DEFAULT_PARAMS, ...JSON.parse(raw) } : { ...DEFAULT_PARAMS };
  } catch {
    return { ...DEFAULT_PARAMS };
  }
}

function saveParams(p: SignalParams): void {
  try {
    localStorage.setItem(PARAMS_KEY, JSON.stringify(p));
  } catch {
    /* best-effort */
  }
}

// The per-window voice toggle (§11) — deliberately its OWN localStorage key,
// independent of voice.ts's shared `mktscanner.voice.v1` tape-narration
// preference (that one defaults OFF and is about reading the tape; this one
// defaults ON and is about a stock actually being ready to trade). The
// independence is real: utterances are sent with speak(text, { force: true }),
// which bypasses ONLY voice.ts's shared on/off gate (the chosen voice, the
// shared rate and the outstanding-queue cap still apply), so with default
// settings a signal is spoken even though the tape reader's voice is off, and
// turning the tape reader's voice on or off never affects this window.
function loadVoiceOn(): boolean {
  try {
    const raw = localStorage.getItem(VOICE_KEY);
    return raw === null ? true : raw === '1';
  } catch {
    return true;
  }
}

function saveVoiceOn(on: boolean): void {
  try {
    localStorage.setItem(VOICE_KEY, on ? '1' : '0');
  } catch {
    /* best-effort */
  }
}

// The scan that feeds the left-bar flag list (e9.ts: default 'e9flag-sideways').
// Garbage or nothing in storage reads as the default (parseFlagListPrefs).
function loadFlagScanId(): string {
  try {
    return parseFlagListPrefs(localStorage.getItem(FLAG_LIST_KEY)).scanId;
  } catch {
    return parseFlagListPrefs(null).scanId;
  }
}

function saveFlagScanId(scanId: string): void {
  try {
    localStorage.setItem(FLAG_LIST_KEY, JSON.stringify({ scanId }));
  } catch {
    /* best-effort */
  }
}

// The per-signal switches of the toolbar's "Alerts" menu (e9.ts
// SIGNAL_SWITCHES; default every signal on but the Dildo 9EMA Touch cue). With
// nothing saved yet, the old "Dildo alerts" / "Climax alerts" toggles' values
// are carried over (parseSignalSwitches) — see the scan-view, alert-gating and
// cell-sound code below for what a switch does.
function readKey(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function loadSignalSwitches(): SignalSwitches {
  return parseSignalSwitches(readKey(SIGNAL_SWITCHES_KEY), readKey(DILDO_ALERTS_KEY), readKey(CLIMAX_ALERTS_KEY));
}

function saveSignalSwitches(s: SignalSwitches): void {
  try {
    localStorage.setItem(SIGNAL_SWITCHES_KEY, serializeSignalSwitches(s));
  } catch {
    /* best-effort */
  }
}

const PRESET_GROUPS = [
  { title: 'wide-monitor layouts', items: SIGNAL_PRESETS },
  { title: 'stacked layouts for a portrait window', items: SIGNAL_PORTRAIT_PRESETS },
] as const;

export function SignalPane({ sym: urlSym }: { sym: string }) {
  const feed = useStore((s) => s.feed);
  const chartSym = useStore((s) => s.chartSym);
  // The per-symbol bars / prior close / chart error are read by each
  // SignalCell for its own symbol. Subscribing to the whole maps here made the
  // ENTIRE pane (every cell, chart and the panel) re-render on every live bar
  // of every charted symbol — once a second per cell.

  const [layout, setLayout] = useState<SignalLayout>(loadSignalLayout);
  const [following, setFollowing] = useState(true);
  // The right column (AUTO strip + panel) can slide out of the way; remembered per browser.
  const [sideHidden, setSideHidden] = useState<boolean>(() => {
    try { return localStorage.getItem('signal.sideHidden') === '1'; } catch { return false; }
  });
  const toggleSide = () => setSideHidden((h) => {
    const n = !h;
    try { localStorage.setItem('signal.sideHidden', n ? '1' : '0'); } catch { /* storage unavailable */ }
    return n;
  });
  const [params, setParamsState] = useState<SignalParams>(loadParams);
  const [flagParams, setFlagParamsState] = useState<FlagParams>(loadFlagParams);
  const [typed, setTyped] = useState('');
  const [muted, setMuted] = useState(false);
  const [voiceOn, setVoiceOnState] = useState(loadVoiceOn);
  const [showWl, setShowWl] = useState(true);
  const [activeRead, setActiveRead] = useState<CellRead | null>(null);
  const [flagScanId, setFlagScanIdState] = useState<string>(loadFlagScanId);
  const [switches, setSwitches] = useState<SignalSwitches>(loadSignalSwitches);
  const scans = useStore((s) => s.scans);
  const posSize = useStore((s) => s.positionSize);
  const twsFollow = useStore((s) => s.twsFollow);
  const twsView = twsFollowView(twsFollow);

  // Mirror the toolbar's mute and voice toggles into the store so ws.ts's alert
  // case honours them for the flag SCAN's alert frames too (see store.ts
  // signalSoundMuted). An effect, not the click handlers: the persisted voice
  // preference must apply at mount, not only after the first click. Reset on
  // unmount so a closed Signal pane cannot leave the window silenced. The
  // Alerts menu's switched-off signals go the same way (store.ts
  // signalScansOff): ws.ts silences their alerts in this window.
  useEffect(() => {
    useStore.getState().setSignalSoundMuted(muted);
  }, [muted]);
  useEffect(() => {
    useStore.getState().setSignalVoiceOff(!voiceOn);
  }, [voiceOn]);
  useEffect(() => {
    useStore.getState().setSignalScansOff(signalScansOff(switches));
  }, [switches]);
  useEffect(
    () => () => {
      useStore.getState().setSignalSoundMuted(false);
      useStore.getState().setSignalVoiceOff(false);
      useStore.getState().setSignalScansOff([]);
    },
    [],
  );

  useEffect(() => {
    start();
  }, []);

  // --- sound / voice bookkeeping refs (§11) ---
  // cellResultsRef holds the latest read per CELL INDEX, so activating a cell
  // (or the panel following a clamped `active` after a preset shrink) shows
  // THAT cell's own result immediately instead of waiting for its next
  // recompute. Per cell, not per symbol: two cells can show one symbol on
  // different timeframes (or sides), and a per-symbol map handed back
  // whichever of them reported last — the 5m result under the 1m cell.
  // cellBasisRef tracks each CELL's own (sym, tf, setup, side) so a symbol
  // reassignment, a timeframe switch, a setup switch or a side-selector change
  // re-primes silently instead of replaying its whole history as fresh alerts.
  // seenRef/primedSymsRef are keyed `${sym}:${ts}:${kind}:${label}` / by sym —
  // see store.ts's signalSyms and this file's own top comment for why the grid
  // is symbol-, not cell-, centric for sound purposes. (The label is part of
  // the key because two events of one kind can share a bar: the long's 'T1
  // BOUNCE HIGH' and 'T2 HOD' are both `longTarget`.)
  const cellResultsRef = useRef<Map<number, CellRead | null>>(new Map());
  const cellBasisRef = useRef<Map<number, string>>(new Map());
  const seenRef = useRef<Set<string>>(new Set());
  const primedSymsRef = useRef<Set<string>>(new Set());

  // A Tuning edit changes the evaluation of the cells running THAT engine only
  // (the pullback params feed pullback cells, the flag params feed flag cells —
  // SignalCell's result memos depend on one set each), so only those cells are
  // re-primed silently, same as the original single-chart pane's "basis change"
  // reset (S1): their per-cell basis is dropped and their immediate re-report
  // adds every event the new params produce to the seen-set without a sound.
  // The other engine's cells do NOT re-report (their memos and `read` keep their
  // identity), so wiping THEIR basis — or the shared seen/primed sets — would
  // make their next tick prime silently and swallow whatever genuinely new
  // signal it carried.
  // The reset has to happen HERE, in the handler, before the re-render — NOT in
  // a `useEffect(..., [params])`: React runs a commit's CHILD passive effects
  // before the parent's, so each SignalCell's onResult effect (fed by the
  // new-params result) would call handleCellResult FIRST, with the basis still
  // intact, and every event the new params create inside the 10-minute window
  // would sound and be spoken as a fresh alert; the parent effect would then
  // clear the state, so the NEXT tick would prime silently and swallow a
  // genuinely new signal. Dropping the basis before setParamsState means the
  // re-render's reports find none and prime silently, and the following tick
  // diffs normally. (Reset-to-defaults goes through onParams, i.e. through here.)
  function reprimeSetup(setup: SetupKind) {
    layout.cells.forEach((c, i) => {
      if (c.setup === setup) cellBasisRef.current.delete(i);
    });
  }

  function setParams(p: SignalParams) {
    reprimeSetup('pullback');
    setParamsState(p);
    saveParams(p);
  }

  // The flag engine's Tuning edit: same reasoning, for the flag cells only.
  function setFlagParams(p: FlagParams) {
    reprimeSetup('flag');
    setFlagParamsState(p);
    saveFlagParams(p);
  }

  function setFlagScanId(id: string) {
    setFlagScanIdState(id);
    saveFlagScanId(id);
  }

  function toggleVoice() {
    setVoiceOnState((was) => {
      saveVoiceOn(!was);
      return !was;
    });
  }

  // One signal on/off (the Alerts menu, or the left-bar list's 🔊 while it shows
  // that signal). Saved at once; the effects pick the rest up (scan view, the
  // store's signalScansOff). The cells' seen-set is left alone: an event that
  // arrived while its signal was off was marked seen, so switching it back on
  // never replays it.
  function toggleSwitch(id: SignalSwitchId) {
    setSwitches((was) => {
      const now = { ...was, [id]: !was[id] };
      saveSignalSwitches(now);
      return now;
    });
  }

  // layoutRef mirrors `layout` for effects/callbacks that need the freshest
  // value without re-subscribing on every keystroke-level change (the wire
  // effects, the follow-mode storage listener).
  const layoutRef = useRef(layout);
  useEffect(() => {
    layoutRef.current = layout;
  }, [layout]);

  // Debounced persist (300ms) — every layout mutation (preset switch, cell
  // assign, cell tf, activate) lands here.
  const saveTimer = useRef<ReturnType<typeof setTimeout>>();
  useEffect(() => {
    clearTimeout(saveTimer.current);
    saveTimer.current = setTimeout(() => saveSignalLayout(layout), 300);
    return () => clearTimeout(saveTimer.current);
  }, [layout]);

  function assignCellSym(i: number, raw: string | null, opts?: { pin?: boolean }) {
    const s = raw ? validSym(raw) : null;
    if (raw && !s) return; // invalid ticker typed/dropped — ignore, same gate every cell uses
    setLayout((l) => {
      if (i < 0 || i >= l.cells.length || l.cells[i].sym === s) return l;
      const cells = l.cells.slice();
      cells[i] = { ...cells[i], sym: s };
      return { ...l, cells };
    });
    // A manual assignment (typed, dropped, or picked from a cell's dropdown)
    // wins over following — clearing a cell (s === null) is not a choice of
    // symbol and must not flip follow mode off.
    if (opts?.pin && s) setFollowing(false);
  }

  // Follow: a new alert of the kind a cell follows moves that cell to its symbol.
  useEffect(
    () =>
      subscribeFollowAlerts((a) => {
        const s = validSym(a.sym);
        if (s) setLayout((l) => {
          const cells = applyFollow(l.cells, a.scanId, s);
          return cells === l.cells ? l : { ...l, cells };
        });
      }),
    [],
  );

  function setCellFollow(i: number, follow: FollowKind) {
    setLayout((l) => {
      if (i < 0 || i >= l.cells.length || (l.cells[i].follow ?? 'off') === follow) return l;
      const cells = l.cells.slice();
      cells[i] = { ...cells[i], follow };
      return { ...l, cells };
    });
  }

  function setCellTf(i: number, tf: ChartTf) {
    setLayout((l) => {
      if (i < 0 || i >= l.cells.length) return l;
      const cells = l.cells.slice();
      cells[i] = { ...cells[i], tf };
      return { ...l, cells };
    });
  }

  // The cell's S | L | S+L selector — display-only (signals/view.ts), persisted
  // with the layout like the timeframe.
  function setCellSide(i: number, side: SignalSide) {
    setLayout((l) => {
      if (i < 0 || i >= l.cells.length || l.cells[i].side === side) return l;
      const cells = l.cells.slice();
      cells[i] = { ...cells[i], side };
      return { ...l, cells };
    });
  }

  // The cell's SETUP — which engine it runs (9EMA flag | Pullback). Persisted
  // with the layout; the cell re-evaluates and re-reports at once, and the
  // basis change in handleCellResult re-primes it silently.
  function setCellSetup(i: number, setup: SetupKind) {
    setLayout((l) => {
      if (i < 0 || i >= l.cells.length || l.cells[i].setup === setup) return l;
      const cells = l.cells.slice();
      cells[i] = { ...cells[i], setup };
      return { ...l, cells };
    });
  }

  // Clicking a cell's chart or strip activates it (the detail panel reads
  // it) and links it to TWS/depth/borrow — see this file's top comment.
  // cellResultsRef holds THIS cell's latest result from its last report, so
  // the panel updates immediately instead of waiting for the cell's next
  // onResult.
  function activateCell(i: number) {
    const sym = layout.cells[i]?.sym ?? null;
    setLayout((l) => (l.active === i ? l : { ...l, active: i }));
    if (sym) linkChart(sym);
    setActiveRead(cellResultsRef.current.get(i) ?? null);
  }

  // Links a symbol to TWS / depth / borrow ("the one thing the app is focused
  // on") — shared by a cell click (activateCell) and a flag-list click
  // (pickFlagSym).
  function linkChart(sym: string) {
    const st = useStore.getState();
    const already = st.chartSym === sym;
    st.openChart(sym); // also re-writes GLOBAL_SYM_KEY, so windows in follow mode pick the symbol up again
    // openChart sets chartSym, and the watchTape effect below is keyed on
    // [chartSym, feed] — when the clicked symbol is ALREADY this window's
    // chartSym nothing changes, the effect never re-fires and no tape.watch
    // goes out. If another window moved TWS in the meantime (click ATYR here,
    // click XYZ in the main window, click the ATYR cell again) TWS, L2 depth
    // and borrow would stay on XYZ. Re-send the watch by hand in that case
    // (idempotent server-side: hub.go's TTapeWatch handler just re-targets
    // the link). MonitorChart solves the same thing with ws.ts's
    // selectSymbol, but that returns early when the symbol is unchanged and
    // so would also skip openChart's global-selection write.
    if (already && st.feed === 'up') watchTape(sym);
  }

  // A click on a flag-list row: the symbol goes to the ACTIVE cell exactly as if
  // it had been typed into the toolbar input (assignCellSym with pin — a manual
  // choice wins over following) and is linked like a cell click.
  function pickFlagSym(sym: string) {
    assignCellSym(layout.active, sym, { pin: true });
    linkChart(sym);
  }

  function toggleFollow() {
    setFollowing((was) => {
      const now = !was;
      if (now) {
        const g = validSym(readGlobalSym());
        if (g) assignCellSym(layoutRef.current.active, g);
      }
      return now;
    });
  }

  // Seed the active cell from the URL's ?sym= (if given) once on mount —
  // same "explicit URL wins" behaviour the original single-chart pane had.
  // Without a URL sym, fall back to the currently-stored global selection
  // ONLY when the active cell doesn't already hold a symbol from a saved
  // layout: a plain re-open of the window must not clobber a persisted
  // multi-symbol grid just because some other window's selection differs.
  useEffect(() => {
    const urlS = validSym(urlSym);
    if (urlS) {
      assignCellSym(layoutRef.current.active, urlS);
    } else if (!layoutRef.current.cells[layoutRef.current.active]?.sym) {
      const g = validSym(readGlobalSym());
      if (g) assignCellSym(layoutRef.current.active, g);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // While following, adopt every symbol selected in any OTHER window into
  // the ACTIVE cell — the browser only fires `storage` in other tabs, never
  // the one that wrote the value, so this can't feed back into a loop (see
  // GLOBAL_SYM_KEY's comment in store.ts).
  useEffect(() => {
    if (!following) return;
    function onStorage(e: StorageEvent) {
      if (e.key !== GLOBAL_SYM_KEY || !e.newValue) return;
      const s = validSym(e.newValue);
      if (s) assignCellSym(layoutRef.current.active, s);
    }
    window.addEventListener('storage', onStorage);
    return () => window.removeEventListener('storage', onStorage);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [following]);

  // Mirror the grid's deduped symbol set into the store — setSymChartData's
  // guard needs this (see store.ts's signalSyms comment).
  const syms = useMemo(() => layoutSyms(layout), [layout]);
  const symsKey = syms.join(',');
  useEffect(() => {
    useStore.getState().setSignalSyms(syms);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [symsKey]);

  // Click-to-link: mirrors ChartsPane.tsx's identical effect verbatim — the
  // store update from activateCell's openChart() call is what actually needs
  // to MOVE the TWS display-group link, live L2 depth target, and IBKR live-
  // borrow line; being merely assigned to a cell never does this, only an
  // explicit click escalates a symbol to "the one thing the app is focused
  // on".
  useEffect(() => {
    if (!chartSym || feed !== 'up') return;
    watchTape(chartSym);
    return () => watchTape('');
  }, [chartSym, feed]);

  // Wire — mirrors ChartsPane.tsx's effects, a grid's worth of symbols
  // instead of a single one. `armed`/`declaredRef` play ChartsPane's own two
  // roles: armed guards the one-time declare-everything on each feed-up
  // (including a reconnect, when the server has forgotten this connection's
  // chart.watch set), while declaredRef lets the grid's symbol SET change
  // re-declare without waiting for the next reconnect.
  const armed = useRef(false);
  const declaredRef = useRef<string[] | null>(null);
  useEffect(() => {
    if (feed === 'up' && !armed.current) {
      armed.current = true;
      getWatchlist();
      getAlertTws();
      getDildoParams();
      const cur = layoutSyms(layoutRef.current);
      watchCharts(cur);
      cur.forEach((s) => useStore.getState().clearSymChartErr(s));
      cur.forEach(getChart);
      declaredRef.current = cur;
    }
    if (feed === 'down') armed.current = false;
  }, [feed]);

  useEffect(() => {
    const prev = declaredRef.current;
    if (prev && prev.join(',') === symsKey) return;
    // S5: only send wire frames (and arm getChart's 15s watchdog) when the
    // feed is actually up — see the original single-chart pane's identical
    // comment. When the feed is down, `armed` is already false, so the
    // feed-up effect above declares the full current set the moment the
    // socket connects.
    if (useStore.getState().feed === 'up') {
      watchCharts(syms);
      const had = new Set(prev ?? []);
      for (const s of syms) {
        if (!had.has(s)) {
          useStore.getState().clearSymChartErr(s);
          getChart(s);
        }
      }
    }
    useStore.getState().pruneChartData(syms);
    declaredRef.current = syms;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [symsKey]);

  // Re-assert the watch every minute — same reasoning as ChartsPane's own
  // identical timer: chart.watch is per-connection server state with no ack,
  // so a frame lost in a boot/reconnect race is otherwise never retried.
  useEffect(() => {
    const id = setInterval(() => {
      if (useStore.getState().feed !== 'up') return;
      watchCharts(layoutSyms(layoutRef.current));
    }, 60_000);
    return () => clearInterval(id);
  }, []);

  // The left-bar flag list (FlagScanList) is fed by one of the scanner's own
  // scans, so — like ChartsPane's gainers/decliners boxes (its boxScanKey
  // effect, which this mirrors) — this window narrows its scan view to exactly
  // that scan and asks for a full snapshot of it: a tick is a delta, so a scan
  // bound after the window connected has no baseline until it is resynced.
  // Keyed on feed too (ws.ts re-arms only the MAIN window's tiles after a
  // reconnect; the server has forgotten this connection's view by then), and
  // it runs whether or not the sidebar is showing, so hiding the list does not
  // quietly change what this window hears.
  //
  // THIS CHANGES WHAT THE WINDOW RECEIVES. Until now the Signal window sent no
  // view.set at all, and an empty view means "every scan" to the server — so it
  // was sent EVERY scan's ticks and alert frames and, once its sound is
  // unlocked, played all their alert sounds and spoke their names. Now it
  // receives only the chosen scan's: the 9EMA flag scan's alerts are the point
  // (the list's 🔊/🔇 is that scan's per-window mute), the other scans' alerts
  // stay in the windows that show them.
  //
  // The green dildo and the volume climax (2026-10-07) are each TWO alerts in
  // sequence on one symbol — "SYM: green dildo" then "SYM: nine E M A
  // pullback", "SYM: volume climax" then "SYM: bottom confirmed" — from two
  // separate scans, so a view of the one listed scan would hear at most half of
  // either. So the view also takes every signal scan switched ON in the
  // toolbar's "Alerts" menu (2026-10-08: one switch per signal — the three 9EMA
  // flag stages, the slam, the dildo's three and the climax's two; e9.ts
  // signalAlertScanIds: the list's scan first, then the switched-on ones that
  // exist on the scanner). A switched-OFF signal is not subscribed, unless the
  // list shows it (its rows need it) — then ws.ts keeps its alerts silent
  // (store.ts signalScansOff). The list still shows only the picked scan's rows;
  // the extra scans' alerts play and speak through ws.ts like the list's own,
  // under this window's 🔊 / 🗣 (their switch, not a per-scan mute, is their
  // control here: see the effect below). Keyed on the id list's VALUE: a scan
  // list that arrives (or is edited) without changing which of them are viewed
  // does not re-send the view or re-request their snapshots.
  const viewIds = useMemo(
    () => signalAlertScanIds(flagScanId, scans.map((x) => x.id), switches),
    [flagScanId, scans, switches],
  );
  const viewKey = JSON.stringify(viewIds);
  useEffect(() => {
    if (feed !== 'up') return;
    viewTiles(viewIds);
    viewIds.forEach(resyncScan);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [viewKey, feed]);

  // A scan the switches add to the view is governed by its switch. A per-scan
  // mute set from the list while the list showed that scan would otherwise
  // outlive the list moving on - invisible (the list's 🔊 now reflects the
  // other scan, the switch reads ON) and swallowing that scan's tone and voice
  // for the rest of the window's life. So it is released (e9.ts
  // extraScanMutes); the list's own scan keeps its mute.
  const mutedScans = useStore((s) => s.mutedScans);
  useEffect(() => {
    const release = extraScanMutes(viewIds, flagScanId, mutedScans);
    if (release.length === 0) return;
    const { toggleScanMute } = useStore.getState();
    for (const id of release) toggleScanMute(id); // muted -> unmuted
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [viewKey, flagScanId, mutedScans]);

  // The signal history under the flag list (FlagEventList): ask for the day
  // being viewed whenever the feed comes up — a reconnect re-requests it (the
  // viewed date is the store's flagEvents.date; '' = today, which is also what
  // we send when that date IS today so a session left open past midnight lands
  // on the new day). An older server never answers; the list keeps waiting.
  useEffect(() => {
    if (feed !== 'up') return;
    const d = useStore.getState().flagEvents?.date ?? '';
    getFlagEvents(d === todayET(Date.now()) ? '' : d);
  }, [feed]);

  // The symbols on a chart in this window, for the list's "already on a chart"
  // marker. Keyed on the joined string so a layout edit that does not change the
  // symbol set (a timeframe, the active cell) does not rebuild the list's rows.
  const onChartSet = useMemo(() => new Set(syms), [symsKey]); // eslint-disable-line react-hooks/exhaustive-deps

  // --- sound / voice diff, every cell (§11) --- (the refs it uses are
  // declared next to setParams, above: a Tuning edit re-primes its engine's cells there)

  // Cells that no longer exist (a preset shrink unmounts them without any
  // final report) must not leave results behind for a later regrow to show —
  // a regrown cell mounts fresh and re-reports, rebuilding its own entry.
  // Declared BEFORE the active-result sync below: one component's effects run
  // in declaration order.
  const cellCount = layout.cells.length;
  useEffect(() => {
    for (const k of Array.from(cellResultsRef.current.keys())) {
      if (k >= cellCount) cellResultsRef.current.delete(k);
    }
  }, [cellCount]);

  // The panel reads the ACTIVE cell's read. handleCellResult keeps it
  // current while the cell recomputes, but the active INDEX can change with no
  // cell recomputing at all — a preset shrink clamps `active` to a cell whose
  // bars (and so result memo) did not change, so its onResult effect never
  // fires and the panel kept the vanished cell's headline, plan and events
  // under the new grid (nothing on screen said so: the panel ignores its `sym`
  // prop). Re-derive it from the per-cell map whenever the active index or the
  // cell count changes; the cells' child effects have already filled the map
  // by the time this parent effect runs.
  useEffect(() => {
    setActiveRead(cellResultsRef.current.get(layout.active) ?? null);
  }, [layout.active, cellCount]);

  // A symbol that leaves the grid entirely (every cell it occupied got
  // cleared or reassigned) loses its seen-set/primed state — re-adding it
  // later must not either replay its whole history OR silently skip its
  // very first real signal.
  useEffect(() => {
    const symSet = new Set(syms);
    for (const s of Array.from(primedSymsRef.current)) {
      if (symSet.has(s)) continue;
      primedSymsRef.current.delete(s);
      for (const k of Array.from(seenRef.current)) {
        if (k.startsWith(`${s}:`)) seenRef.current.delete(k);
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [symsKey]);

  const eventKey = (sym: string, e: ViewEvent) => `${sym}:${e.ts}:${e.kind}:${e.label}`;

  function handleCellResult(idx: number, sym: string | null, tfMin: number, read: CellRead | null) {
    cellResultsRef.current.set(idx, read); // null included: a cleared cell must not keep showing its last result
    if (idx === layout.active) setActiveRead(read);
    if (!sym || !read) return;
    // The raw, UNFILTERED events of whichever engine this cell runs (a union of
    // two array types, widened to the shared event shape for the loop), and the
    // side the cell applies — always 'both' for a flag cell (see CellRead).
    const { setup, side, result } = read;
    const events: ViewEvent[] = result.events;

    // Priming: the first result for this SYMBOL (however it arrived — new to
    // the grid, or moved cells) and any (sym, tf, setup, side) basis change both add
    // every current event to the seen-set silently, same as the original
    // pane's per-symbol primedRef/tf-switch handling, now scoped per symbol
    // instead of the whole pane. Every event is primed regardless of side —
    // history never sounds — so a side switch cannot replay the morning.
    const basisKey = `${sym}:${tfMin}:${setup}:${side}`;
    const prevBasis = cellBasisRef.current.get(idx);
    cellBasisRef.current.set(idx, basisKey);
    if (!primedSymsRef.current.has(sym) || prevBasis !== basisKey) {
      for (const e of events) seenRef.current.add(eventKey(sym, e));
      primedSymsRef.current.add(sym);
      return;
    }

    // A lowerHigh event is stamped with the SWING bar's ts (swingStrength
    // bars before the confirming close), so at the moment it first exists it
    // is already (swingStrength + 1) bars old — widen the cutoff on a slow
    // timeframe so a structure event confirmed by the latest close still
    // counts as "now" (see the original pane's identical comment, S4). The
    // same holds for a higherLow. (The flag engine's events are stamped with
    // their own bar, never an earlier swing bar, so the plain recency window
    // is enough there.)
    const recentCutoff =
      Date.now() - (setup === 'flag' ? SOUND_RECENT_MS : Math.max(SOUND_RECENT_MS, (params.swingStrength + 3) * tfMin * 60_000));
    // This result's new tones sound IN TURN (playInTurn), never on top of each
    // other: a long slammed out of its position brings its EXIT bell and the
    // SLAM's falling glide in one result, and started together the two beat.
    const tones: string[] = [];
    // A blacklisted symbol (today's blacklist) is silent here too.
    const st = useStore.getState();
    const blocked = isBlacklisted(st.blacklist, st.blacklistAt, sym, Date.now());
    for (const e of events) {
      // The cell's side selector gates sound and voice STRICTLY (a 'short'
      // cell never sounds a long event and vice versa). An event that is not
      // this cell's business is left UNSEEN on purpose: the symbol may also
      // sit in another cell on the other side, and that cell must still get
      // to sound it. (An event no cell can hear just stays unseen; the loop
      // skips it in O(1).)
      if (!eventOnSide(e, side)) continue;
      const key = eventKey(sym, e);
      if (seenRef.current.has(key)) continue;
      seenRef.current.add(key);
      if (e.ts < recentCutoff) continue;
      // A signal switched off in the Alerts menu is silent in the cells too
      // (e9.ts cellEventHeard: FLAT = sideways, 9 EMA NEAR = ready, LONG / ADD /
      // EXIT = breakout, SLAM = slam). Marked seen above, so switching it back
      // on does not replay it.
      if (!cellEventHeard(switches, setup, e.kind)) continue;
      if (blocked) continue; // marked seen above: unblocking does not replay it
      const snd = soundForEvent(e);
      if (snd && !muted) tones.push(snd);
      if (voiceOn && !muted) {
        // The flag engine's phrases read its whole result (levels, evidence and
        // the entry an exit's P&L is measured from); the pullback's, its levels.
        const phrase = voicePhrase(sym, e, read.setup === 'flag' ? read.result : read.result.levels);
        // force: this window's own toggle decides, not the tape reader's
        // shared voice preference (see the note above loadVoiceOn).
        if (phrase) speak(phrase, { force: true });
      }
    }
    if (tones.length) playInTurn(tones);
  }

  const activeCell = layout.cells[layout.active] ?? null;
  const activeSym = activeCell?.sym ?? null;

  return (
    <div className="app pane-window">
      <StatusBar />
      <div className="signal-toolbar">
        <button
          className={`segbtn ${showWl ? 'on' : ''}`}
          onClick={() => setShowWl((v) => !v)}
          title={showWl ? 'hide the watchlist sidebar' : 'show the watchlist sidebar'}
        >
          ☰ Watchlist
        </button>
        <button
          className={`segbtn ${following ? 'on' : ''}`}
          onClick={toggleFollow}
          title={
            following
              ? 'Following your symbol selection in every window — click to pin the active chart to its current symbol'
              : 'Pinned — click to follow your selection again'
          }
        >
          {following ? `🔗 Following · ${activeSym || '…'}` : '📌 Pinned'}
        </button>
        <input
          className="wl-add"
          value={typed}
          placeholder="assign to the active chart"
          onChange={(e) => setTyped(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && typed.trim()) {
              assignCellSym(layout.active, typed, { pin: true });
              setTyped('');
            }
          }}
        />
        {PRESET_GROUPS.map((group, gi) => (
          <Fragment key={group.title}>
            {gi > 0 && <span className="charts-toolbar-divider" />}
            <div className="seg" title={group.title}>
              {group.items.map((p) => (
                <button
                  key={p.label}
                  className={`segbtn ${layout.rows === p.rows && layout.cols === p.cols ? 'on' : ''}`}
                  onClick={() => setLayout((l) => makeSignalLayout(p.rows, p.cols, l.cells, l.active))}
                  title={`${p.cols} across × ${p.rows} down`}
                >
                  {p.label}
                </button>
              ))}
            </div>
          </Fragment>
        ))}
        <span className="spacer" />
        <button className={`segbtn ${twsView.on ? 'on' : ''}`} onClick={() => setAlertTws(!twsFollow)} title={twsView.title}>
          {twsView.label}
        </button>
        <SignalSwitchMenu switches={switches} onToggle={toggleSwitch} />
        <label className="segbtn" title="Position size in $: each Green Dildo alert also says (and the SIGNALS row shows) how many whole shares this buys at the dildo's close. 0 or empty = off.">
          Size $
          <input
            type="number"
            min={0}
            step={100}
            style={{ width: '6em', marginLeft: 4 }}
            value={posSize || ''}
            placeholder="off"
            onChange={(ev) => useStore.getState().setPositionSize(parsePositionSize(ev.target.value === '' ? '' : ev.target.value))}
          />
        </label>
        <button
          className="segbtn"
          onClick={() => setMuted((m) => !m)}
          title={
            muted
              ? "sound muted in this window (trade signals and every signal alert, tone and voice) — click to unmute"
              : "mute sound in this window — trade signals and every signal alert (tone and voice); the Alerts menu switches signals one by one"
          }
        >
          {muted ? '🔇' : '🔊'}
        </button>
        <button
          className={`segbtn ${voiceOn ? 'on' : ''}`}
          onClick={toggleVoice}
          title={
            voiceOn
              ? "trade signals and the switched-on signal alerts are spoken (symbol first) — independent of the tape reader's voice setting; click to turn off"
              : "voice off in this window — click to have trade signals and the switched-on signal alerts spoken"
          }
        >
          🗣 Voice
        </button>
      </div>
      <div className="signal-body">
        {showWl && (
          <WatchlistSidebar boxes={false}>
            <div className="flag-stack">
              <FlagScanList
                scanId={flagScanId}
                onScanId={setFlagScanId}
                onChart={onChartSet}
                onPick={pickFlagSym}
                signalOn={isSignalSwitchId(flagScanId) ? switches[flagScanId] : undefined}
                onToggleSignal={isSignalSwitchId(flagScanId) ? () => toggleSwitch(flagScanId) : undefined}
              />
              <FlagEventList onChart={onChartSet} onPick={pickFlagSym} />
            </div>
          </WatchlistSidebar>
        )}
        <div
          className="signal-grid"
          style={{
            gridTemplateColumns: `repeat(${layout.cols}, minmax(0, 1fr))`,
            gridTemplateRows: `repeat(${layout.rows}, minmax(0, 1fr))`,
          }}
        >
          {layout.cells.map((def, idx) => (
            <SignalCell
              key={idx}
              idx={idx}
              def={def}
              active={layout.active === idx}
              params={params}
              flagParams={flagParams}
              onAssign={(s) => assignCellSym(idx, s, { pin: true })}
              onTf={(tf) => setCellTf(idx, tf)}
              onSide={(side) => setCellSide(idx, side)}
              onSetup={(setup) => setCellSetup(idx, setup)}
              onFollow={(f) => setCellFollow(idx, f)}
              onActivate={() => activateCell(idx)}
              onResult={(r) => handleCellResult(idx, def.sym, def.tf === '5m' ? 5 : 1, r)}
            />
          ))}
        </div>
        <button
          type="button"
          className="signal-side-toggle"
          onClick={toggleSide}
          title={sideHidden ? 'Show the AUTO / signal panel' : 'Hide the AUTO / signal panel'}
          aria-label={sideHidden ? 'Show side panel' : 'Hide side panel'}
        >
          {sideHidden ? '‹' : '›'}
        </button>
        {!sideHidden && (
          <SignalPanel
            read={activeRead}
            sym={activeSym}
            params={params}
            onParams={setParams}
            flagParams={flagParams}
            onFlagParams={setFlagParams}
          />
        )}
      </div>
    </div>
  );
}
