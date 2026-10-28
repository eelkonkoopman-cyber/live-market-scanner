import { useEffect, useRef, useState } from 'react';
import { useStore } from '../store';
import { isUnlocked, unlock } from '../audio';
import { etParts, sessionLine } from '../marketClock';
import { fmtClockUS } from '../clockFormat';
import type { SourceHealth } from '../types';
import { THEMES, themeDef, setTheme, type ThemeId } from '../theme';
import { useTheme } from '../useTheme';
import '../theme-picker.css';

// Display names for known StatusFrame.sources keys — a key this map doesn't
// know about (a source added server-side before this list catches up) still
// shows up, keyed by its raw wire id, instead of silently vanishing.
const SOURCE_NAME: Record<string, string> = {
  massive: 'Massive feed',
  alpaca: 'Alpaca feed',
  bzpush: 'Benzinga push',
  rtpr: 'RTPR wire',
  rtprAlerts: 'RTPR impact scores',
  edgar: 'SEC EDGAR',
};

// A source flapping for under a minute reads as a blip, not an outage — this
// keeps one dropped message from flashing the pill red/amber.
const FAILING_GRACE_MS = 60_000;

// Valid tickers only: letters/digits/./- , starting with a letter, up to 10
// chars — matches what the backend actually tracks.
const SYM_RE = /^[A-Z][A-Z0-9.\-]{0,9}$/;

const pad2 = (n: number) => String(n).padStart(2, '0');

function isFormField(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable) return true;
  return target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.tagName === 'SELECT';
}

// Reduce status.sources down to what the pill shows. 'off' sources are
// deliberately disabled, not a health problem, so they never even count
// toward "how many sources are unhealthy".
function sourcesSummary(sources: Record<string, SourceHealth> | undefined) {
  if (!sources) return null;
  const entries = Object.entries(sources).filter(([, h]) => h.state !== 'off');
  if (entries.length === 0) return null;

  const now = Date.now();
  const failing = entries.filter(([, h]) => h.state === 'failing' && now - (h.since ?? 0) > FAILING_GRACE_MS);
  const ok = failing.length === 0;

  const title = entries
    .map(([id, h]) => {
      const since = h.since ? ` since ${fmtClockUS(h.since)}` : '';
      return `${SOURCE_NAME[id] ?? id}: ${h.state}${h.msg ? ' — ' + h.msg : ''}${since}`;
    })
    .join('\n');

  return {
    value: ok ? 'ok' : `${failing.length} failing`,
    cls: ok ? 'ok' : failing.some(([id]) => id === 'massive') ? 'bad' : 'warn',
    title,
  };
}

// swatchGradient previews a theme the same way AfterBell's own
// ThemeQuickToggle does: a diagonal two-tone disc (elevated surface fading
// into the accent corner) built from two of the theme's four swatches
// (index 1 = bg-elevated, index 3 = accent) rather than all four, since a
// small 22px disc can't usefully show more than that.
function swatchGradient(def: { swatches: [string, string, string, string] }): string {
  const [, elevated, , accent] = def.swatches;
  return `linear-gradient(135deg, ${elevated} 0%, ${elevated} 60%, ${accent} 100%)`;
}

// ThemePicker is the disc button + popover menu that lets a person switch
// palettes from ANY window. useTheme() (not a local useState) is what makes
// the disc itself update when some OTHER window changes the theme — the
// same cross-window reactivity the "Following" bar gets from the `storage`
// event in TapePane, just routed through React's external-store hook
// instead of a manual listener.
function ThemePicker() {
  const theme = useTheme();
  const active = themeDef(theme);
  const [open, setOpen] = useState(false);
  const btnRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const itemRefs = useRef<Array<HTMLButtonElement | null>>([]);

  // Dismiss on outside mousedown or Escape — the same two paths Modal.tsx
  // uses for the scan editor / API-key dialogs — plus Up/Down to move focus
  // between rows, since this is a real (if tiny) menu.
  useEffect(() => {
    if (!open) return;
    function onMouseDown(e: MouseEvent) {
      const t = e.target as Node;
      if (menuRef.current?.contains(t) || btnRef.current?.contains(t)) return;
      setOpen(false);
    }
    function onKeyDown(e: KeyboardEvent) {
      if (e.key === 'Escape') {
        setOpen(false);
        btnRef.current?.focus();
        return;
      }
      if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
      const items = itemRefs.current.filter((el): el is HTMLButtonElement => el != null);
      if (items.length === 0) return;
      e.preventDefault();
      const from = items.findIndex((el) => el === document.activeElement);
      const next = items[(from + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length];
      next.focus();
    }
    window.addEventListener('mousedown', onMouseDown);
    window.addEventListener('keydown', onKeyDown);
    return () => {
      window.removeEventListener('mousedown', onMouseDown);
      window.removeEventListener('keydown', onKeyDown);
    };
  }, [open]);

  function choose(id: ThemeId) {
    setTheme(id);
    setOpen(false);
    btnRef.current?.focus();
  }

  return (
    <div className="thememenu-wrap">
      <button
        ref={btnRef}
        type="button"
        className="themebtn"
        style={{ background: swatchGradient(active) }}
        aria-label={`Theme: ${active.name}`}
        title={`Theme: ${active.name}`}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      />
      {open && (
        <div className="thememenu" role="menu" ref={menuRef}>
          {THEMES.map((t, i) => (
            <button
              key={t.id}
              type="button"
              ref={(el) => {
                itemRefs.current[i] = el;
              }}
              role="menuitemradio"
              aria-checked={t.id === theme}
              className="thememenu-item"
              title={t.description}
              onClick={() => choose(t.id)}
            >
              <span className="thememenu-swatches">
                {t.swatches.map((c, j) => (
                  <span key={j} className="thememenu-swatch" style={{ background: c }} />
                ))}
              </span>
              <span className="thememenu-name">{t.name}</span>
              <span className="thememenu-check">{t.id === theme ? '✓' : ''}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

// SoundToggle replaces the old full-width "Enable sound alerts" banners (main
// window, scan pop-out, signal pop-out). Browsers block ANY audio until a user
// gesture in THAT browsing context, and audio.ts's `unlocked` flag is a
// per-window module singleton — so every window (main AND each pop-out) needs
// its own one-time click, and since StatusBar renders in all of them, this one
// control covers them all. Locked it is amber (so it is noticed); once
// unlocked it goes back to a normal quiet icon.
function SoundToggle() {
  const [soundOn, setSoundOn] = useState(isUnlocked());
  return (
    <button
      type="button"
      className={`iconbtn soundbtn${soundOn ? '' : ' warn'}`}
      title={
        soundOn
          ? 'Sound alerts are on'
          : 'Sound alerts are off — click to enable (browsers need one click before a page may play sound)'
      }
      aria-label={soundOn ? 'Sound alerts are on' : 'Enable sound alerts'}
      onClick={() => {
        if (soundOn) return;
        unlock();
        setSoundOn(true);
      }}
    >
      {soundOn ? '🔊' : '🔇'}
    </button>
  );
}

export function StatusBar() {
  const status = useStore((s) => s.status);
  const feed = useStore((s) => s.feed);
  const openKeyModal = useStore((s) => s.openKeyModal);

  // Ticks the ET clock pill once a second; its "next boundary" countdown and
  // the sources pill's failing-grace check ride along on the same timer.
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, []);

  // The charts/review pop-outs have their own symbol inputs (watchlist add,
  // review table) — the global search box would be redundant chrome there.
  // Each pop-out is a fresh page load (see main.tsx), so this never changes
  // for the life of the window — no need to track it as state.
  const pane = new URLSearchParams(location.search).get('pane');
  const showSymSearch = pane !== 'charts' && pane !== 'review';

  const [symValue, setSymValue] = useState('');
  const [symInvalid, setSymInvalid] = useState(false);
  const symRef = useRef<HTMLInputElement>(null);
  const symShiftRef = useRef(false); // shiftKey on the Enter keydown that is about to submit

  // "/" or Ctrl+K focuses the box from anywhere in the window (unless some
  // other field is already being typed into); Escape blurs it back out.
  useEffect(() => {
    if (!showSymSearch) return;
    function onKeyDown(e: KeyboardEvent) {
      if (e.key === 'Escape') {
        if (document.activeElement === symRef.current) symRef.current?.blur();
        return;
      }
      const isSlash = e.key === '/';
      const isCtrlK = (e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k';
      if (!isSlash && !isCtrlK) return;
      if (isFormField(e.target)) return; // don't hijack typing in some other field
      e.preventDefault();
      symRef.current?.focus();
    }
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [showSymSearch]);

  function trySubmitSym() {
    const sym = symValue.trim().toUpperCase();
    if (!SYM_RE.test(sym)) {
      setSymInvalid(true); // keep the text, just flag it — do nothing else
      return;
    }
    setSymInvalid(false);
    useStore.getState().openChart(sym);
    // Same launch params as every other "⧉ Tape"-style opener in the app.
    if (symShiftRef.current) {
      window.open(`/?pane=tape&sym=${encodeURIComponent(sym)}`, '_blank', 'width=920,height=820');
    }
    setSymValue('');
    symRef.current?.blur();
  }

  const stat = (label: string, val: string, cls = '', prio?: 'low') => (
    <span className="stat" data-prio={prio}>
      <span className="lbl">{label}</span> <b className={cls}>{val}</b>
    </span>
  );

  const et = etParts(now);
  const clockStr = `${pad2(et.hh)}:${pad2(et.mm)}:${pad2(et.ss)}`;
  const clockSub = sessionLine(now, status?.session ?? 'closed');
  const sessionShort =
    status?.session === 'pre' ? 'PRE' : status?.session === 'regular' ? 'REG' : status?.session === 'post' ? 'POST' : 'CLOSED';

  const twsVal = status?.twsLink ?? 'off';
  const twsCls = twsVal === 'linked' ? 'ok' : twsVal === 'waiting' ? 'warn' : 'off';

  const sources = sourcesSummary(status?.sources);

  return (
    <header className="statusbar">
      <span
        className="brand"
        title={feed === 'up' ? 'Connected to the scanner' : 'Disconnected — reconnecting…'}
      >
        <span className="dot" data-up={feed === 'up'} />
        Live Market Scanner
      </span>
      <span className="stat-group">
        {feed === 'up'
          ? stat('feed', status?.feed ?? '–', status?.feed === 'live' ? 'ok' : 'bad', 'low')
          : stat('feed', 'offline', 'bad', 'low')}
        <span
          className="stat clock"
          title={`US market time (America/New_York), session ${sessionShort}; the countdown is to the next session boundary (no holiday calendar)`}
        >
          <span className="lbl">{sessionShort}</span> <b>{clockStr} ET</b>
          <span className="sub">· {clockSub}</span>
        </span>
        <span className="stat" title="Interactive Brokers TWS display-group link — moves TWS to the symbol you click">
          <span className="lbl">tws</span> <b className={twsCls}>{twsVal}</b>
        </span>
        {sources && (
          <span className="stat" title={sources.title}>
            <span className="lbl">sources</span> <b className={sources.cls}>{sources.value}</b>
          </span>
        )}
        {stat('tracked', `${status?.tracked ?? 0} · ${status?.msgRate ?? 0}/s`, '', 'low')}
        {status?.baseline !== 'ready' && stat('rvol', status?.baseline ?? '–', 'warn')}
      </span>
      <span className="spacer" />
      {showSymSearch && (
        <form
          className="symsearch"
          onSubmit={(e) => {
            e.preventDefault();
            trySubmitSym();
          }}
        >
          <input
            ref={symRef}
            value={symValue}
            onChange={(e) => {
              setSymValue(e.target.value);
              if (symInvalid) setSymInvalid(false);
            }}
            onKeyDown={(e) => {
              // Some key-event delivery paths (e.g. synthesized keydown
              // during verification/automation) fire keydown without the
              // keypress that drives a form's IMPLICIT submission, so Enter
              // would silently do nothing. Submit explicitly instead; the
              // form's onSubmit stays too as harmless belt-and-braces for
              // the normal browser path.
              if (e.key !== 'Enter') return;
              symShiftRef.current = e.shiftKey;
              e.preventDefault();
              trySubmitSym();
            }}
            placeholder="symbol  /"
            spellCheck={false}
            autoComplete="off"
            aria-label="symbol search"
            title={symInvalid ? 'Not a valid ticker (letters/digits, starting with a letter)' : undefined}
          />
        </form>
      )}
      <SoundToggle />
      <ThemePicker />
      <button className="iconbtn gearbtn" title="API key settings" aria-label="API key settings" onClick={openKeyModal}>
        ⚙
      </button>
    </header>
  );
}
