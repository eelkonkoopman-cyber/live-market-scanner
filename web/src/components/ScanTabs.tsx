import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { useStore, GLOBAL_SYM_KEY } from '../store';
import { viewTiles, resyncScan } from '../ws';
import { fitCount, orderTabs } from '../tabFit';
import type { ScanDef } from '../types';

// Tab bar = ONE line: [scan pills that fit][More ▾ N][+] | [window buttons].
// How many pills fit is MEASURED (a hidden twin row renders every pill at its
// natural width) and depends only on widths and the available space — never on
// which scans are matching — so tabs keep their fixed scan order and nothing
// slides in or out of view just because a match count changed. Whatever does
// not fit lives in the More menu, in the same order.
export function ScanTabs() {
  const scans = useStore((s) => s.scans);
  const activeId = useStore((s) => s.activeId);
  const openTiles = useStore((s) => s.openTiles);
  const openTile = useStore((s) => s.openTile);
  const openEditor = useStore((s) => s.openEditor);
  // Shallow-compared: every status frame carries a fresh `counts` object, even
  // when no scan's count moved, and the pill row should not re-render for that.
  const counts = useStore(useShallow((s) => s.status?.counts));
  const chartSym = useStore((s) => s.chartSym);
  const rightHidden = useStore((s) => s.pane.rightHidden);

  const areaRef = useRef<HTMLDivElement>(null);
  const measureRef = useRef<HTMLDivElement>(null);
  // Starts at "everything fits"; the layout effect below corrects it before
  // the first paint.
  const [fit, setFit] = useState(Number.MAX_SAFE_INTEGER);
  const [menuOpen, setMenuOpen] = useState(false);
  const moreRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const closeTimer = useRef<number | undefined>(undefined);

  // Row order: tiled scans first (in tile order), then the rest in scan order,
  // so the scans in use are never the ones pushed into More. It only changes
  // when the USER tiles or closes a scan — never because a match count moved —
  // so the bar still never rearranges itself as data changes. The measuring
  // twin below uses the same order, so its widths line up with fitCount.
  const byId = new Map(scans.map((sc) => [sc.id, sc]));
  const ordered = orderTabs(scans.map((sc) => sc.id), openTiles).map((id) => byId.get(id)!);
  const visibleCount = Math.min(fit, ordered.length);
  const visible = ordered.slice(0, visibleCount);
  const visibleIds = new Set(visible.map((sc) => sc.id));
  // The More menu lists what is hidden in normal scan order (predictable).
  const hidden = scans.filter((sc) => !visibleIds.has(sc.id));
  const hiddenActive = hidden.filter((sc) => sc.id === activeId || openTiles.includes(sc.id));

  // Click on a scan (pill or menu row): tile it (add-if-missing + focus) and,
  // if it was not tiled yet, subscribe the server to it and resync.
  function activate(sc: ScanDef) {
    const wasOpen = openTiles.includes(sc.id);
    openTile(sc.id);
    if (!wasOpen) {
      viewTiles([...openTiles, sc.id]);
      resyncScan(sc.id);
    }
  }

  const measure = useCallback(() => {
    const area = areaRef.current;
    const m = measureRef.current;
    if (!area || !m) return;
    const kids = Array.from(m.children) as HTMLElement[];
    if (kids.length < 2) return;
    const plus = kids[kids.length - 1].getBoundingClientRect().width;
    const more = kids[kids.length - 2].getBoundingClientRect().width;
    const pills = kids.slice(0, kids.length - 2).map((k) => k.getBoundingClientRect().width);
    const g = parseFloat(getComputedStyle(m).columnGap);
    const gap = Number.isFinite(g) ? g : 5;
    setFit(fitCount(pills, area.clientWidth, gap, more + gap + plus, plus));
  }, []);

  // Re-measure whenever something that changes a pill's width changes (names,
  // order, classes). Live counts are deliberately NOT a dependency: the twin
  // row measures every pill with a fixed worst-case badge (see renderPill), so
  // the cut-off cannot move when matches appear or vanish. The trade is a few
  // px of slack at the end of the row when badges are absent — a stable
  // cut-off is worth more to this user than the slack.
  useLayoutEffect(() => {
    measure();
  }, [measure, scans, openTiles, activeId]);

  // … and whenever the available width (window resize) or the natural widths
  // (fonts finishing loading) change.
  useEffect(() => {
    const ro = new ResizeObserver(() => measure());
    if (areaRef.current) ro.observe(areaRef.current);
    if (measureRef.current) ro.observe(measureRef.current);
    return () => ro.disconnect();
  }, [measure]);

  // Dismiss the More menu on outside mousedown or Escape.
  useEffect(() => {
    if (!menuOpen) return;
    function onMouseDown(e: MouseEvent) {
      const t = e.target as Node;
      if (menuRef.current?.contains(t) || moreRef.current?.contains(t)) return;
      setMenuOpen(false);
    }
    function onKeyDown(e: KeyboardEvent) {
      if (e.key === 'Escape') {
        setMenuOpen(false);
        moreRef.current?.focus();
      }
    }
    window.addEventListener('mousedown', onMouseDown);
    window.addEventListener('keydown', onKeyDown);
    return () => {
      window.removeEventListener('mousedown', onMouseDown);
      window.removeEventListener('keydown', onKeyDown);
    };
  }, [menuOpen]);

  useEffect(() => () => window.clearTimeout(closeTimer.current), []);

  const menuShown = menuOpen && hidden.length > 0;

  // One scan pill. `live` false = the inert twin used only for measuring.
  const renderPill = (sc: ScanDef, live: boolean) => {
    // Every tab carries its live match count, so an empty grid is obviously
    // "this scan matches nothing right now" rather than "the feed is broken" —
    // the Activity Log below shows hits from ALL scans, which otherwise
    // makes an idle scan look like it is dropping rows.
    // The measuring twin always carries a worst-case "000" badge on enabled
    // pills (counts never reach 4 digits — scan limits are a few hundred), so
    // its widths do not depend on live data. (.tab.idle only changes colours,
    // not width.) Disabled pills never show a badge, live or measured.
    const n = live ? counts?.[sc.id] : 0;
    const showBadge = live ? sc.enabled && n !== undefined && n > 0 : sc.enabled;
    const isOpen = openTiles.includes(sc.id);
    // A scan matching nothing right now gets no badge and a fainter pill
    // (user: "Make this layout more user friendly" — with 16 scans, ten
    // "0" badges were most of the tab bar's ink). Only live counts stay
    // loud, so the eye lands on the scans that are actually firing.
    const idle = sc.enabled && (n ?? 0) === 0;
    return (
      <button
        key={sc.id}
        className={`tab ${sc.id === activeId ? 'active' : ''} ${isOpen ? 'open' : ''} ${idle ? 'idle' : ''}`}
        onClick={live ? () => activate(sc) : undefined}
        onDoubleClick={live ? () => openEditor(sc) : undefined}
        title={live ? 'click to tile, double-click to edit' : undefined}
        tabIndex={live ? undefined : -1}
      >
        {sc.name}
        {!sc.enabled && ' (off)'}
        {showBadge && <span className="tab-count">{live ? n : '000'}</span>}
      </button>
    );
  };

  const moreTitle =
    'More scans' + (hiddenActive.length > 0 ? ` — Includes tiled: ${hiddenActive.map((sc) => sc.name).join(', ')}` : '');

  return (
    <div className="tabs">
      <div className="tabs-scans" ref={areaRef}>
        <div className="tabs-row">
          {visible.map((sc) => renderPill(sc, true))}
          {hidden.length > 0 && (
            <div className="tabmore-wrap">
              <button
                ref={moreRef}
                type="button"
                className={`tab more ${hiddenActive.length > 0 ? 'open' : ''}`}
                title={moreTitle}
                aria-haspopup="menu"
                aria-expanded={menuShown}
                onClick={() => setMenuOpen((o) => !o)}
              >
                More ▾<span className="tab-count">{hidden.length}</span>
              </button>
              {menuShown && (
                <div className="tabmenu" role="menu" ref={menuRef}>
                  {hidden.map((sc) => {
                    const n = counts?.[sc.id];
                    const isOpen = openTiles.includes(sc.id);
                    const idle = sc.enabled && (n ?? 0) === 0;
                    return (
                      <button
                        key={sc.id}
                        type="button"
                        role="menuitem"
                        className={`tabmenu-item ${sc.id === activeId ? 'active' : ''} ${isOpen ? 'open' : ''} ${idle ? 'idle' : ''}`}
                        title="click to tile, double-click to edit"
                        onClick={() => {
                          activate(sc);
                          // Close shortly AFTER the click, not at once: a
                          // double-click's second click must still land on this
                          // row (to open the editor), not on whatever is behind
                          // a menu that already vanished.
                          window.clearTimeout(closeTimer.current);
                          closeTimer.current = window.setTimeout(() => setMenuOpen(false), 280);
                        }}
                        onDoubleClick={() => {
                          window.clearTimeout(closeTimer.current);
                          setMenuOpen(false);
                          openEditor(sc);
                        }}
                      >
                        <span className="tabmenu-name">
                          {sc.name}
                          {!sc.enabled && ' (off)'}
                        </span>
                        {sc.enabled && n !== undefined && n > 0 && <span className="tab-count">{n}</span>}
                      </button>
                    );
                  })}
                  <button
                    type="button"
                    role="menuitem"
                    className="tabmenu-item tabmenu-add"
                    onClick={() => {
                      setMenuOpen(false);
                      openEditor(null);
                    }}
                  >
                    <span className="tabmenu-name">+ New scan</span>
                  </button>
                </div>
              )}
            </div>
          )}
          <button className="tab add" title="New scan" onClick={() => openEditor(null)}>
            +
          </button>
        </div>
        {/* Inert, invisible twin of the row above at NATURAL widths (every
            pill, plus the More and + buttons) — only ever read by measure().
            Clipped to a zero-height box so it can neither be seen nor widen the
            page. */}
        <div className="tabs-measure" aria-hidden="true" ref={measureRef}>
          {ordered.map((sc) => renderPill(sc, false))}
          <button className="tab more" tabIndex={-1}>
            More ▾<span className="tab-count">{String(scans.length)}</span>
          </button>
          <button className="tab add" tabIndex={-1}>
            +
          </button>
        </div>
      </div>
      <div className="tabs-windows">
        <button
          className={`tab window ${rightHidden ? '' : 'open'}`}
          title={
            rightHidden
              ? 'Show the details column (fundamentals & news) again'
              : 'Hide the details column (fundamentals & news) — the scan grids take the full width; row clicks still drive TWS and the other windows'
          }
          onClick={() => {
            useStore.getState().setPane({ rightHidden: !rightHidden });
            useStore.getState().persistLayout();
          }}
        >
          ◧ Details
        </button>
        <button
          className="tab window"
          title="Open the tape reader in its own window (follows your symbol clicks)"
          onClick={() => {
            // Prefer this window's selection, else whatever was selected last in
            // ANY window (the follow-mode key) — the tape window needs a symbol
            // in its URL to arm itself before follow mode takes over.
            let sym = chartSym ?? '';
            if (!sym) {
              try {
                sym = localStorage.getItem(GLOBAL_SYM_KEY) ?? '';
              } catch {
                /* ignore */
              }
            }
            window.open(`/?pane=tape&sym=${encodeURIComponent(sym)}`, '_blank', 'width=920,height=820');
          }}
        >
          ⧉ Tape
        </button>
        <button
          className="tab window"
          title="Open the multi-chart monitor window (watchlist + chart grid)"
          onClick={() => window.open('/?pane=charts', '_blank', 'width=1600,height=900')}
        >
          ⧉ Charts
        </button>
        <button
          className="tab window"
          title="Open the morning review window (yesterday's session, sorted by gain — drag rows onto the charts watchlist)"
          onClick={() => window.open('/?pane=review', '_blank', 'width=1100,height=800')}
        >
          ⧉ Review
        </button>
        <button
          className="tab window"
          title="Open the pullback signal window (a grid of charts, each reading short and long setups off the spike-and-pullback engine)"
          onClick={() => window.open('/?pane=signal', '_blank', 'width=1600,height=900')}
        >
          ⧉ Signal
        </button>
        <button
          className="tab window"
          title="Open the Day Log (every alert of a trading day with what the price did afterwards, plus the day's top gainers and losers)"
          onClick={() => window.open('/?pane=daylog', '_blank', 'width=1300,height=850')}
        >
          ⧉ Day Log
        </button>
        <button
          className="tab window"
          title="Open the Order Ticket (small always-on-top window: follows the TWS ticker; BUY/SELL a limit at the 1m 9EMA with a 10% take profit, confirm before sending)"
          onClick={() => window.open('/?pane=order', '_blank', 'width=320,height=260')}
        >
          ⧉ Order
        </button>
      </div>
    </div>
  );
}
