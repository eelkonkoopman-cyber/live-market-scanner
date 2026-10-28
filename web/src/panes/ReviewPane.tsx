import { useEffect, useMemo, useRef, useState } from 'react';
import { useStore, deriveReviewGainPct } from '../store';
import { start, getReview } from '../ws';
import { StatusBar } from '../components/StatusBar';
import { fmt } from '../format';
import type { ReviewRow, ReviewSession } from '../types';

// ReviewPane is the morning-review pop-out (?pane=review): "what happened in
// yesterday's session" — the after-hours gainers list the morning watchlist
// gets built from. Like every pop-out it's a fresh page load with its own
// store and WS connection, and tiles no scan (empty view.set, same as
// TapePane/ChartsPane).
//
// Rows are draggable (text/plain sym, the GridRow/ChartsPane precedent
// verbatim) onto the charts window's watchlist sidebar, and clickable to
// openChart(), which writes the cross-window GLOBAL_SYM_KEY. That alone does
// NOT make any other window follow along — a window only adopts the symbol if
// it's actually listening for the `storage` event on that key (TapePane
// always does; SymbolPanel does only when its "🔗 Follow" toggle is on). This
// pane has no toggle of its own: clicking a row just broadcasts the
// selection, the same as clicking a grid row anywhere else.
const SESSIONS: { key: ReviewSession; label: string; title: string }[] = [
  { key: 'pre', label: 'PRE', title: 'Pre-market, 04:00–09:30 ET' },
  { key: 'regular', label: 'RTH', title: 'Regular hours, 09:30–16:00 ET' },
  { key: 'post', label: 'AH', title: 'After hours, 16:00–20:00 ET — the default morning-review view' },
];

const SESSION_NOUN: Record<ReviewSession, string> = { pre: 'pre-market', regular: 'regular-hours', post: 'after-hours' };

type SortMode = 'gain' | 'vol';

const MIN_VOL_DEFAULT = 100_000;
const MAX_ROWS = 300;
const SYM_W = 92;
const COL_W = 88;
const VOL_W = 100;

// rv (raw value) renders a session-snapshot field through fmt's existing
// null->"–" path: <=0 means "unknown" for hi/lo/close/vwap (the same
// zero-means-unknown convention core.SessionSnapshotRow documents), so it must
// read as a dash, never a literal 0.
function rv(v: number): string {
  return fmt(v > 0 ? v : null);
}

function fmtPct(v: number | null): string {
  if (v === null) return '–';
  return `${v >= 0 ? '+' : ''}${v.toFixed(1)}%`;
}

export function ReviewPane() {
  const feed = useStore((s) => s.feed);
  const review = useStore((s) => s.review);
  const openChart = useStore((s) => s.openChart);
  const armed = useRef(false);
  const [sort, setSort] = useState<SortMode>('gain');
  const [minVol, setMinVol] = useState(MIN_VOL_DEFAULT);

  useEffect(() => {
    start();
  }, []);

  // First load once the feed comes up (including a reconnect, when the server
  // has no memory of this connection's prior request) — default date (newest)
  // and default session ('post', the stated morning flow).
  useEffect(() => {
    if (feed === 'up' && !armed.current) {
      armed.current = true;
      getReview();
    }
    if (feed === 'down') armed.current = false;
  }, [feed]);

  function selectDate(date: string) {
    getReview(date, review.session);
  }
  function selectSession(session: ReviewSession) {
    if (session === review.session) return;
    useStore.getState().setReviewSession(session);
    getReview(review.date ?? undefined, session);
  }

  const filtered = useMemo(() => review.rows.filter((r) => r.vol >= minVol), [review.rows, minVol]);

  const sorted = useMemo(() => {
    const withGain = filtered.map((row) => ({ row, gain: deriveReviewGainPct(row, review.session, review.rthCloseBySym) }));
    withGain.sort((a, b) => {
      if (sort === 'vol') return b.row.vol - a.row.vol;
      // Gain sort: missing-baseline rows (null) sink to the bottom instead of
      // sorting as if their gain were 0.
      if (a.gain === null && b.gain === null) return 0;
      if (a.gain === null) return 1;
      if (b.gain === null) return -1;
      return b.gain - a.gain;
    });
    return withGain;
  }, [filtered, sort, review.session, review.rthCloseBySym]);

  const top = sorted.slice(0, MAX_ROWS);
  const noHistory = review.dates.length === 0;
  const noRowsForSelection = !noHistory && review.rows.length === 0;
  const filteredToNothing = !noHistory && !noRowsForSelection && filtered.length === 0;

  return (
    <div className="app pane-window">
      <StatusBar />
      <div className="review-toolbar">
        <select
          className="review-date-sel"
          value={review.date ?? ''}
          disabled={noHistory}
          onChange={(e) => selectDate(e.target.value)}
          title="review date"
        >
          {review.date === null && <option value="">—</option>}
          {review.dates.map((d) => (
            <option key={d} value={d}>
              {d}
            </option>
          ))}
        </select>
        <div className="seg">
          {SESSIONS.map((s) => (
            <button
              key={s.key}
              className={`segbtn ${review.session === s.key ? 'on' : ''}`}
              title={s.title}
              onClick={() => selectSession(s.key)}
            >
              {s.label}
            </button>
          ))}
        </div>
        <div className="seg">
          <button className={`segbtn ${sort === 'gain' ? 'on' : ''}`} onClick={() => setSort('gain')} title="sort by gain%, descending">
            Gain%
          </button>
          <button className={`segbtn ${sort === 'vol' ? 'on' : ''}`} onClick={() => setSort('vol')} title="sort by volume, descending">
            Volume
          </button>
        </div>
        <label className="review-minvol" title="hide rows below this session volume (dead prints clutter AH lists)">
          min vol
          <input
            type="number"
            value={minVol}
            min={0}
            step={10_000}
            onChange={(e) => setMinVol(Math.max(0, Number(e.target.value) || 0))}
          />
        </label>
        {review.loading && <span className="review-loading muted">loading…</span>}
        <span className="spacer" />
        <span className="review-hint muted">click a row to select it in following windows · drag onto the charts watchlist</span>
      </div>

      {noHistory ? (
        <div className="empty">
          No session history yet — stats are recorded at each session boundary from now on.
        </div>
      ) : noRowsForSelection ? (
        <div className="empty">
          No trades recorded for {review.date} ({SESSION_NOUN[review.session]}) — the scanner may not have been running that session.
        </div>
      ) : filteredToNothing ? (
        <div className="empty">
          No rows at or above {minVol.toLocaleString()} volume for {review.date} ({SESSION_NOUN[review.session]}) — lower the min-vol filter.
        </div>
      ) : (
        <div className="grid-scroll review-scroll">
          <div className="grid-row head review-row">
            <div className="cell sym" style={{ flex: `0 0 ${SYM_W}px` }}>
              Symbol
            </div>
            <div className="cell" style={{ flex: `0 0 ${COL_W}px` }}>
              Gain%
            </div>
            <div className="cell" style={{ flex: `0 0 ${COL_W}px` }}>
              Close
            </div>
            <div className="cell" style={{ flex: `0 0 ${COL_W}px` }}>
              High
            </div>
            <div className="cell" style={{ flex: `0 0 ${COL_W}px` }}>
              Low
            </div>
            <div className="cell" style={{ flex: `0 0 ${VOL_W}px` }}>
              Volume
            </div>
            <div className="cell" style={{ flex: `0 0 ${COL_W}px` }}>
              Vwap
            </div>
          </div>
          {top.map(({ row, gain }) => (
            <ReviewDataRow key={row.sym} row={row} gain={gain} onOpen={() => openChart(row.sym)} />
          ))}
          <div className="review-footer muted">
            top {top.length} of {sorted.length} by {sort === 'gain' ? 'Gain%' : 'Volume'}
          </div>
        </div>
      )}
    </div>
  );
}

function ReviewDataRow({ row, gain, onOpen }: { row: ReviewRow; gain: number | null; onOpen: () => void }) {
  const tone = gain === null ? '' : gain >= 0 ? 'pos' : 'neg';
  return (
    <div
      className="grid-row data clickable review-row"
      onClick={onOpen}
      title={`Open ${row.sym} · drag to the charts window's watchlist`}
      draggable
      onDragStart={(e) => {
        // Same cross-window OS-level DnD as LiveGrid's GridRow: a plain-text
        // ticker on the clipboard works between any two of our windows.
        e.dataTransfer.setData('text/plain', row.sym); // Firefox requires a payload
        e.dataTransfer.effectAllowed = 'copy';
      }}
    >
      <div className="cell sym" style={{ flex: `0 0 ${SYM_W}px` }}>
        {row.sym}
      </div>
      <div className={`cell ${tone}`} style={{ flex: `0 0 ${COL_W}px` }}>
        {fmtPct(gain)}
      </div>
      <div className="cell" style={{ flex: `0 0 ${COL_W}px` }}>
        {rv(row.close)}
      </div>
      <div className="cell" style={{ flex: `0 0 ${COL_W}px` }}>
        {rv(row.hi)}
      </div>
      <div className="cell" style={{ flex: `0 0 ${COL_W}px` }}>
        {rv(row.lo)}
      </div>
      <div className="cell" style={{ flex: `0 0 ${VOL_W}px` }}>
        {fmt(row.vol)}
      </div>
      <div className="cell" style={{ flex: `0 0 ${COL_W}px` }}>
        {rv(row.vwap)}
      </div>
    </div>
  );
}
