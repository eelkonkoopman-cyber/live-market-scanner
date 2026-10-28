import { useEffect, type CSSProperties } from 'react';
import { useStore } from '../store';
import { getRow, watchTape } from '../ws';
import { fmt } from '../format';
import { TapeBand } from './TapeBand';
import { TapeLadder } from './TapeLadder';
import { TapeFeed } from './TapeFeed';
import { CvdChart } from './CvdChart';
import { RegimeTimeline } from './RegimeTimeline';

// TapeReaderView is the pop-out tape window's chart-free layout (?pane=tape,
// mounted by TapePane in place of the docked SymbolPanel): the stats header,
// the core order-flow strip (TapeBand coreOnly — verdict gauge, toolbar,
// metrics, Claude read), then the two main columns the window exists for —
// the Level 2 ladder and the raw time & sales — each its own scroller, with
// a slim CVD + tape-reads strip below. Deliberately NO candlestick chart and
// NO fundamentals row, and it never sends chart.get / fundamentals.get /
// news.for / trend.get: selection (store.openChart) only sets state, all
// those requests live in SymbolPanel's effects, which never mount here. The
// only requests this view makes are row.get (header stats) and tape.watch.

// Same stats SymbolPanel's header shows, sourced the same way (row.get
// snapshot under the reserved __standalone__ grid key).
const STATS: { key: string; label: string }[] = [
  { key: 'last', label: 'LAST' },
  { key: 'chg5m', label: '%5M' },
  { key: 'chgDay', label: '%DAY' },
  { key: 'vol', label: 'VOLUME' },
  { key: 'rvol', label: 'RVOL' },
  { key: 'vwap', label: 'VWAP' },
];

export function TapeReaderView({ style }: { style?: CSSProperties } = {}) {
  const sym = useStore((s) => s.chartSym);
  const feed = useStore((s) => s.feed);
  const tape = useStore((s) => s.tape);
  // Mirror SymbolPanel's row lookup: any live grid first (a pop-out normally
  // has none tiled), then the one-off row.get snapshot.
  const row = useStore((s) => {
    if (!s.chartSym) return undefined;
    for (const gid in s.grids) {
      if (gid === '__standalone__') continue;
      const r = s.grids[gid]?.rows[s.chartSym];
      if (r) return r;
    }
    return s.grids.__standalone__?.rows[s.chartSym];
  });
  // True only when `row` above came from a REAL tiled scan's grid (which
  // keeps itself fresh via ticks) rather than the reserved __standalone__
  // row.get snapshot (which does not) — decides whether this view needs to
  // poll for its own refresh below. A separate selector (not bundled into
  // `row` as one object) so this stays a plain boolean compared by value,
  // instead of a new object reference forcing a re-render on every store update.
  const fromLiveGrid = useStore((s) => {
    if (!s.chartSym) return false;
    for (const gid in s.grids) {
      if (gid === '__standalone__') continue;
      if (s.grids[gid]?.rows[s.chartSym]) return true;
    }
    return false;
  });

  // Header stats: the row.get snapshot is this window's only source (no scan
  // is tiled here) — same fallback SymbolPanel uses.
  useEffect(() => {
    if (sym) getRow(sym, ['last', 'chg5m', 'chgDay', 'vol', 'rvol', 'vwap']);
  }, [sym]);
  // This pop-out tiles no scan (empty view.set), so ticks never reach it —
  // and a symbol picked via cross-window follow may not be in ANY tiled
  // scan's grid elsewhere either (fromLiveGrid false). row.get above is this
  // header's only source; poll it so LAST/%5M/%DAY/VOLUME/RVOL/VWAP stay
  // honest on a mover instead of freezing at whatever they were on selection.
  useEffect(() => {
    if (!sym || feed !== 'up' || fromLiveGrid) return;
    const id = setInterval(() => getRow(sym, ['last', 'chg5m', 'chgDay', 'vol', 'rvol', 'vwap']), 2000);
    return () => clearInterval(id);
  }, [sym, feed, fromLiveGrid]);

  // Point the server's tape engine at this symbol, and stop watching (freeing
  // the trade/quote subscription) when the view unmounts — the same lifecycle
  // SymbolPanel runs in the main window. Keyed on `feed` too: tape.watch is
  // per-CONNECTION state (the server's client.tapeSym dies with the socket,
  // and tape.prints batches are routed by it), so a reconnect with the same
  // symbol must re-send the watch or the raw tape stays dead. The gate also
  // stops a watch from being written into a socket that isn't open yet (send
  // drops silently on a CONNECTING socket); the cleanup's watchTape('') after
  // a disconnect is a harmless no-op on the closed socket.
  useEffect(() => {
    if (!sym || feed !== 'up') return;
    watchTape(sym);
    return () => watchTape('');
  }, [sym, feed]);

  if (!sym) {
    return (
      <div className="muted pad">
        No symbol yet — click one in any window and this window will follow.
      </div>
    );
  }

  return (
    <section className="tape-reader" style={style}>
      <div className="sympanel-head">
        <b className="sympanel-sym">{sym}</b>
        {STATS.map((st) => {
          const v = row?.[st.key];
          const tone = st.key.startsWith('chg') && typeof v === 'number' ? (v >= 0 ? 'pos' : 'neg') : '';
          return (
            <span key={st.key} className="sympanel-stat">
              <label>{st.label}</label>
              <em className={tone}>{v == null ? '—' : fmt(v)}</em>
            </span>
          );
        })}
      </div>

      <TapeBand coreOnly />

      <div className="tape-reader-cols">
        <div className="tape-reader-col">
          <div className="tape-subhead">Level 2</div>
          <div className="tape-reader-scroll">
            {tape ? (
              <TapeLadder
                hasDepth={tape.hasDepth}
                bids={tape.bookBids ?? []}
                asks={tape.bookAsks ?? []}
                depthNote={tape.depthNote ?? ''}
              />
            ) : (
              <div className="muted pad">Waiting for tape…</div>
            )}
          </div>
        </div>
        <div className="tape-reader-col">
          <div className="tape-subhead">Time &amp; sales</div>
          <div className="tape-reader-scroll">
            <TapeFeed />
          </div>
        </div>
      </div>

      <div className="tape-reader-bottom">
        <div className="tape-reader-bottom-col">
          <div className="tape-subhead">Cumulative volume delta</div>
          <CvdChart data={tape?.cvdSeries ?? []} height={64} />
        </div>
        <div className="tape-reader-bottom-col">
          <RegimeTimeline />
        </div>
      </div>
    </section>
  );
}
