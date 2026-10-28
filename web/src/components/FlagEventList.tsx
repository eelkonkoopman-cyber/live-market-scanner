import { useVirtualizer } from '@tanstack/react-virtual';
import { isBlacklisted } from '../blacklist';
import { useEffect, useMemo, useRef, useState } from 'react';
import { useStore } from '../store';
import { getFlagEvents } from '../ws';
import { fmtPrice } from '../format';
import {
  dateLabel,
  etTime,
  eventTitle,
  filterEvents,
  flagEventKey,
  loadKindChips,
  allKinds,
  saveKindChips,
  type SignalFilter,
  isLowKey,
  kindBadge,
  eventBadge,
  todayET,
  visibleEvents,
  type FlagEventFilter,
} from '../flagEvents';
import type { FlagEvent } from '../types';
import { newsBadge } from '../dildoNews';
import { fmtShares, sharesFor } from '../positionSize';
import { EARLY_BADGE, splitEarlyNote } from '../dildoEarly';
import { FRESH_BADGE, isSkippedIgnition, splitFreshNote } from '../ignitionFresh';

// FlagEventList: the Signal window's SIGNAL HISTORY, under the live flag scan
// list in the left bar (user: "On the signal page flag scan i want an activity
// list from stocks that made a signal so i can view it back"). FlagScanList
// shows where the flag-scan stocks ARE; this shows what they DID: every signal
// any flag-scan stock made (READY, LONG, ADD, EXIT, FAILED, ...), per ET day,
// newest first. The server records and persists them (flagevents.*, 30 days),
// so the list survives reloads and restarts; the day stepper in the head
// walks back through the stored days. An older server never answers
// flagevents.get, in which case the list just keeps saying it is waiting.
//
// Same look as the flag list (it reuses the .sidebox classes). Rows are
// click-to-load / draggable like FlagScanList's, and a stock already on a
// chart carries the same dot. A new event arriving while today is shown gets a
// brief highlight (no sound — the alerts already speak).
//
// The market-wide 1-minute alerts are in it too (user, after missing SBFM's
// 04:24 green dildo, which the Green Dildo scan lists for 4 minutes only):
// GREEN DILDO, 9 EMA PULLBACK, VOLUME CLIMAX, BOTTOM CONFIRMED — each with its
// own badge and a one-line note under the row (what the rule measured) — and,
// dimmed, the dead ends PB CANCELLED / CLIMAX FAILED. They show under All; the
// Trades filter stays the flag setup's. The window's Dildo / Climax alert
// toggles mute their sound only, never these rows.

const FILTER_KEY = 'mktscanner.signal.flagEventsFilter.v1';

function loadFilter(): FlagEventFilter {
  try {
    return localStorage.getItem(FILTER_KEY) === 'trades' ? 'trades' : 'all';
  } catch {
    return 'all';
  }
}

function saveFilter(f: FlagEventFilter): void {
  try {
    localStorage.setItem(FILTER_KEY, f);
  } catch {
    // storage blocked: the choice just doesn't persist
  }
}

export function FlagEventList({
  onChart,
  onPick,
}: {
  onChart: ReadonlySet<string>; // symbols on a chart in this window right now
  onPick: (sym: string) => void; // click: put it on the active chart and link it
}) {
  const state = useStore((s) => s.flagEvents);
  const news = useStore((s) => s.news);
  const posSize = useStore((s) => s.positionSize);
  const [filter, setFilterState] = useState<FlagEventFilter>(loadFilter);
  const blacklist = useStore((s) => s.blacklist);
  const blacklistAt = useStore((s) => s.blacklistAt);
  const [kinds, setKinds] = useState<string[]>(loadKindChips);
  const [sym, setSym] = useState('');
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [hideBl, setHideBl] = useState(false);
  const [fresh, setFresh] = useState<ReadonlySet<string>>(() => new Set());
  const seen = useRef<{ date: string; keys: Set<string> } | null>(null);

  const today = todayET(Date.now());
  const date = state?.date ?? '';
  const events = state?.events;
  const isToday = date === today;

  // "New" = an event whose key was not there at the first render of this day's
  // list (or of the last data frame's day): the first sight of a day only seeds
  // the set, so opening the list or stepping to another day highlights nothing.
  useEffect(() => {
    if (!events) return;
    const cur = seen.current;
    if (!cur || cur.date !== date) {
      seen.current = { date, keys: new Set(events.map(flagEventKey)) };
      setFresh(new Set());
      return;
    }
    const added: string[] = [];
    for (const e of events) {
      const k = flagEventKey(e);
      if (!cur.keys.has(k)) {
        cur.keys.add(k);
        added.push(k);
      }
    }
    if (added.length === 0 || !isToday) return;
    setFresh(new Set(added));
    const id = setTimeout(() => setFresh(new Set()), 1600);
    return () => clearTimeout(id);
  }, [events, date, isToday]);

  const base = useMemo(() => (events ? visibleEvents(events, filter) : []), [events, filter]);
  const chips = useMemo(() => allKinds(base), [base]);
  const sf: SignalFilter = useMemo(() => ({ kinds, sym, from, to, hideBlacklisted: hideBl }), [kinds, sym, from, to, hideBl]);
  const rows = useMemo(
    () => filterEvents(base, sf, (s) => isBlacklisted(blacklist, blacklistAt, s, Date.now())),
    [base, sf, blacklist, blacklistAt],
  );
  const toggleKind = (k: string) => {
    const next = kinds.includes(k) ? kinds.filter((x) => x !== k) : [...kinds, k];
    setKinds(next);
    saveKindChips(next);
  };
  const listRef = useRef<HTMLDivElement>(null);
  const VIRTUAL_MIN = 150; // below this a plain list is cheaper (and server-renderable)
  const virtual = rows.length >= VIRTUAL_MIN;
  const virt = useVirtualizer({
    count: virtual ? rows.length : 0,
    getScrollElement: () => listRef.current,
    estimateSize: () => 26,
    overscan: 12,
  });

  const dates = state?.dates ?? [];
  const at = dates.indexOf(date);
  const older = at >= 0 ? dates[at + 1] : undefined; // dates are newest first
  const newer = at > 0 ? dates[at - 1] : undefined;
  const goto = (d: string) => getFlagEvents(d === today ? '' : d);
  const label = state ? dateLabel(date, today) : '';

  function setFilter(f: FlagEventFilter) {
    setFilterState(f);
    saveFilter(f);
  }

  const renderRow = (e: FlagEvent) => {
    const k = flagEventKey(e);
    return <FlagEventRow key={k} e={e} onChart={onChart.has(e.sym)} fresh={fresh.has(k)} onPick={onPick} news={e.kind === 'dildo' ? newsBadge(news, e.sym, e.at || e.ts) : null} posSize={posSize} />;
  };

  let empty: string | null = null;
  if (!state) empty = 'Waiting for the scanner…';
  else if (rows.length === 0 && base.length > 0) empty = 'No signals match the filter.';
  else if (rows.length === 0) {
    // Under Trades a day of only dildo / climax signals still HAS signals: say
    // where they are rather than "none" (the list is where a missed one is found).
    const all = filter === 'trades' && events ? visibleEvents(events, 'all').length : 0;
    const under = `${all} signal${all === 1 ? '' : 's'} under All.`;
    if (all > 0) empty = isToday ? `No trade signals yet today — ${under}` : `No trade signals on ${label} — ${under}`;
    else
      empty = isToday
        ? 'No signals yet today — they appear here as flag-scan stocks make them (and every green dildo / volume climax), and stay for 30 days.'
        : `No signals on ${label}.`;
  }

  return (
    <div className="sideboxes flagev">
      <section className="sidebox">
        <header className="sidebox-head">
          <span
            className="t"
            title="Every signal a flag-scan stock made (READY, LONG, ADD, EXIT, FAILED, SLAM…) and every market-wide GREEN DILDO, 9 EMA PULLBACK, VOLUME CLIMAX and BOTTOM, newest first. Kept for 30 days — step back through the days with the arrows."
          >
            Signals{state ? ` · ${rows.length}` : ''}
          </span>
          {state && (
            <span className="flagev-step">
              <button
                className="iconbtn tile-btn"
                disabled={!older}
                title={older ? `Show ${dateLabel(older, today)}` : 'No older day stored'}
                onClick={() => older && goto(older)}
              >
                ‹
              </button>
              <span className="flagev-date">{label}</span>
              <button
                className="iconbtn tile-btn"
                disabled={!newer}
                title={newer ? `Show ${dateLabel(newer, today)}` : 'Already on the latest day'}
                onClick={() => newer && goto(newer)}
              >
                ›
              </button>
            </span>
          )}
          <span className="seg flagev-seg">
            <button
              className={`segbtn ${filter === 'all' ? 'on' : ''}`}
              title="every signal (READY, LONG, ADD, EXIT, FAILED, SLAM, FLAT, EARLY) plus DILDO, 9EMA PB, CLIMAX, BOTTOM"
              onClick={() => setFilter('all')}
            >
              All
            </button>
            <button
              className={`segbtn ${filter === 'trades' ? 'on' : ''}`}
              title="only the trade signals: LONG, ADD, EXIT, FAILED — and the 9 EMA SLAM alert"
              onClick={() => setFilter('trades')}
            >
              Trades
            </button>
          </span>
        </header>
        {state && (
          <div className="flagev-filter">
            <div className="flagev-chips">
              {chips.map(({ kind: k, n }) => (
                <button
                  key={k}
                  className={`flagev-chip ${kinds.includes(k) ? 'on' : ''} ${n === 0 ? 'empty' : ''}`}
                  title={`Show only ${kindBadge(k as FlagEvent['kind']).text} — ${n} today (click more chips to add; none selected = all)`}
                  onClick={() => toggleKind(k)}
                >
                  {kindBadge(k as FlagEvent['kind']).text}
                  {n > 0 && <span className="flagev-chip-n">{n}</span>}
                </button>
              ))}
            </div>
            <div className="flagev-fields">
              <input className="flagev-q" placeholder="symbol" value={sym} onChange={(ev) => setSym(ev.target.value)} />
              <input className="flagev-t" type="time" title="from (ET)" value={from} onChange={(ev) => setFrom(ev.target.value)} />
              <span>-</span>
              <input className="flagev-t" type="time" title="to (ET)" value={to} onChange={(ev) => setTo(ev.target.value)} />
              <label title="hide today's blacklisted symbols">
                <input type="checkbox" checked={hideBl} onChange={(ev) => setHideBl(ev.target.checked)} /> hide BL
              </label>
              <span className="flagev-count">
                {rows.length} of {base.length}
              </span>
            </div>
          </div>
        )}
        <div className="sidebox-list" ref={listRef}>
          {empty && <div className="sidebox-empty">{empty}</div>}
          {!empty && !virtual && rows.map((e) => renderRow(e))}
          {!empty && virtual && (
            <div style={{ height: virt.getTotalSize(), position: 'relative' }}>
              {virt.getVirtualItems().map((vi) => (
                <div key={vi.key} data-index={vi.index} ref={virt.measureElement} style={{ position: 'absolute', top: 0, left: 0, width: '100%', transform: `translateY(${vi.start}px)` }}>
                  {renderRow(rows[vi.index])}
                </div>
              ))}
            </div>
          )}
        </div>
      </section>
    </div>
  );
}

// One history row: ET time, ticker, badge, price, and - for the dildo / climax
// signals - the note on a second line. Click loads the ticker on the active
// chart (the parent's onPick), drag drops it on any chart. A plain function of
// its props (no hooks), so a test can call it and fire its handlers.
export function FlagEventRow({
  e,
  onChart,
  fresh,
  onPick,
  news,
  posSize = 0,
}: {
  e: FlagEvent;
  posSize?: number; // the window's Position size $ (positionSize.ts): a dildo row shows '1,250 sh'
  news?: { label: string; title: string } | null; // GREEN DILDO with a headline in the hour before: 'NEWS 10s' (age at the alert)
  onChart: boolean; // the ticker is on a chart in this window
  fresh: boolean; // arrived just now: flash
  onPick: (sym: string) => void;
}) {
  const b = eventBadge(e);
  const shares = e.kind === 'dildo' ? sharesFor(posSize, e.price) : 0;
  const { early, note: n0 } = splitEarlyNote(e.kind, e.note);
  const { fresh: ignFresh, note } = splitFreshNote(e.kind, n0);
  return (
    <div
      className={`sidebox-row flagev-row${isLowKey(e.kind) || isSkippedIgnition(e.kind, e.note) ? ' lowkey' : ''}${onChart ? ' on-chart' : ''}${fresh ? ' new' : ''}`}
      draggable
      title={eventTitle(e)}
      onDragStart={(ev) => {
        ev.dataTransfer.effectAllowed = 'copy';
        ev.dataTransfer.setData('text/plain', e.sym);
      }}
      onClick={() => onPick(e.sym)}
    >
      <span className="flagev-time">{etTime(e.ts)}</span>
      <span className="sb-sym">{e.sym}</span>
      <span className="flagev-kind">
        <span className={`flagev-badge ${b.tone}`}>{b.text}</span>
        {early && (
          <span className="flagev-badge early-badge" title={EARLY_BADGE.title}>
            {EARLY_BADGE.label}
          </span>
        )}
        {ignFresh && (
          <span className="flagev-badge early-badge" title={FRESH_BADGE.title}>
            {FRESH_BADGE.label}
          </span>
        )}
        {news && (
          <span className="flagev-badge news-badge" title={news.title}>
            {news.label}
          </span>
        )}
        {shares > 0 && (
          <span className="flagev-badge" title={`${fmtShares(shares)} shares at $${e.price} for the Position size $${posSize}`}>
            {fmtShares(shares)} sh
          </span>
        )}
      </span>
      <span className="sb-num">{fmtPrice(e.price)}</span>
      {note && <span className="flagev-note">{note}</span>}
    </div>
  );
}
