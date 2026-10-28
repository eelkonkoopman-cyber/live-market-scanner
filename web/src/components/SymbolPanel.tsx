import { useEffect, type CSSProperties } from 'react';
import { useStore, GLOBAL_SYM_KEY } from '../store';
import { getFundamentals, getNewsFor, getTrendFor, getRow, watchTape } from '../ws';
import { fmt, money } from '../format';
import { SetupCard } from './SetupCard';

// SymbolPanel is the docked right-side workspace strip for the selected
// symbol: stats header, daily-trend chip, fundamentals row, and the Claude
// setup card. Selection comes from clicking a grid or activity row
// (store.openChart). The chart and the tape band deliberately do NOT live
// here anymore ("On the right. I dont need the tape or chart. Just the
// fundamentals and news.") — the chart has the ⧉ Charts monitor window and
// the per-cell grid, the tape has its own ⧉ Tape window. Removing them frees
// the right column for what this panel is actually read for: fundamentals on
// top, news below.

// TrendChip is the daily-chart context read: pops in daily up/flat trends
// tended to follow through, pops in daily downtrends tended to fade — so the
// answer sits right next to the symbol, colored like a verdict.
function TrendChip() {
	const trend = useStore((s) => s.trend);
	if (trend === undefined) return null; // not answered yet: render nothing, not a wrong value
	if (trend === null) {
		return (
			<span className="trend-chip unknown" title="Not enough daily history to classify (recent listing?)">
				DAILY ?
			</span>
		);
	}
	const cls = trend.dtrend > 0 ? 'up' : trend.dtrend < 0 ? 'down' : 'flat';
	const arrow = trend.dtrend > 0 ? '↗' : trend.dtrend < 0 ? '↘' : '→';
	const word = trend.dtrend > 0 ? 'UPTREND' : trend.dtrend < 0 ? 'DOWNTREND' : 'FLAT';
	const pct = (x: number) => `${x >= 0 ? '+' : ''}${x.toFixed(1)}%`;
	return (
		<span
			className={`trend-chip ${cls}`}
			title={`Daily chart (${trend.days} sessions to ${trend.asOf}): close ${pct(trend.vsSma)} vs 20-day SMA, ${pct(trend.chg20d)} over ~20 days`}
		>
			DAILY {arrow} {word} · {pct(trend.chg20d)}/20d
		</span>
	);
}

// Stats shown in the header strip, sourced from the symbol's row in the active
// scan grid (falls back to the one-off row.get snapshot when no grid has it).
const STATS: { key: string; label: string }[] = [
  { key: 'last', label: 'LAST' },
  { key: 'chg5m', label: '%5M' },
  { key: 'chgDay', label: '%DAY' },
  { key: 'vol', label: 'VOLUME' },
  { key: 'rvol', label: 'RVOL' },
  { key: 'vwap', label: 'VWAP' },
];

// months of runway rendered as a short qualitative tag next to the number —
// traders scan for "is this about to need to raise" at a glance.
function runwayTone(months: number): string {
  if (months < 6) return 'neg';
  if (months < 12) return 'warn';
  return 'pos';
}

// BorrowStat shows IBKR's live, exact borrow availability for the selected
// symbol (TWS generic tick 236) — distinct from the scanner's shortAvail
// column, which comes from the 15-minute bulk file. On a squeeze the live
// number can go to zero long before the file catches up.
function BorrowStat() {
  const live = useStore((s) => s.shortLive);
  if (!live) {
    return (
      <span className="sympanel-stat">
        <label>BORROW (live)</label>
        <em className="muted">—</em>
      </span>
    );
  }
  if (live.err) {
    return (
      <span className="sympanel-stat" title={live.err}>
        <label>BORROW (live)</label>
        <em className="muted">n/a</em>
      </span>
    );
  }
  // IB's documented bands: >2.5 = at least 1000 shares, >1.5 = locatable.
  const tone = live.indicator > 2.5 ? 'pos' : live.indicator > 1.5 ? 'warn' : live.indicator > 0 ? 'neg' : '';
  const label =
    live.indicator > 2.5 ? 'available' : live.indicator > 1.5 ? 'locatable' : live.indicator > 0 ? 'none' : 'waiting…';
  return (
    <span
      className="sympanel-stat"
      title={`IBKR live borrow — indicator ${live.indicator || '—'} (>2.5 = 1000+ shares, >1.5 = locatable)`}
    >
      <label>BORROW (live)</label>
      <em className={tone}>{live.shares >= 0 ? `${fmt(live.shares)} · ${label}` : label}</em>
    </span>
  );
}

export function SymbolPanel({ style }: { style?: CSSProperties } = {}) {
  const sym = useStore((s) => s.chartSym);
  const feed = useStore((s) => s.feed);
  const followOthers = useStore((s) => s.followOthers);
  // Search every currently-loaded grid for the selected symbol's row — with
  // tiling there's no single "active" grid to assume. Real scan grids take
  // priority over the one-off row.get snapshot (stored under the reserved
  // __standalone__ key), since they're live-updating and it isn't.
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
  // row.get snapshot (which does not) — decides whether the header needs to
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
  const fundamentals = useStore((s) => s.fundamentals);
  const fundamentalsErr = useStore((s) => s.fundamentalsErr);

  // Request fundamentals / archived news / daily trend on selection change.
  // (No chart.get anymore — this panel has no chart to feed; the monitor
  // window fetches its own history per cell.)
  useEffect(() => {
    if (sym) getFundamentals(sym);
  }, [sym]);
  useEffect(() => {
    if (sym) getNewsFor(sym);
  }, [sym]);
  useEffect(() => {
    if (sym) getTrendFor(sym);
  }, [sym]);
  // Fallback header stats: harmless when a live grid already has this row
  // (which takes priority, see the `row` lookup above).
  useEffect(() => {
    if (sym) getRow(sym, ['last', 'chg5m', 'chgDay', 'vol', 'rvol', 'vwap', 'float']);
  }, [sym]);
  // This panel also renders header stats for a symbol NOT in any tiled scan's
  // grid (fromLiveGrid false) — e.g. selected via cross-window follow from
  // Review/Tape/Charts. No scan ticks reach it, so the one-off row.get above
  // goes stale within seconds on a mover; poll while that's true. A live-grid
  // row already gets fresh ticks and needs no polling. Gated on feed too: a
  // down/reconnecting socket must not accumulate an interval firing into
  // nothing (send() itself already no-ops there, but no reason to keep trying).
  useEffect(() => {
    if (!sym || feed !== 'up' || fromLiveGrid) return;
    const id = setInterval(() => getRow(sym, ['last', 'chg5m', 'chgDay', 'vol', 'rvol', 'vwap', 'float']), 2000);
    return () => clearInterval(id);
  }, [sym, feed, fromLiveGrid]);

  // While enabled, adopt every symbol selected in any other window (review,
  // charts, tape) — the same storage-event pattern TapePane.tsx uses (the
  // browser only fires `storage` in OTHER tabs, never this one, so writing
  // GLOBAL_SYM_KEY back via openChart below can't feed back into a loop).
  useEffect(() => {
    if (!followOthers) return;
    function onStorage(e: StorageEvent) {
      if (e.key !== GLOBAL_SYM_KEY || !e.newValue) return;
      if (e.newValue !== useStore.getState().chartSym) useStore.getState().openChart(e.newValue);
    }
    window.addEventListener('storage', onStorage);
    return () => window.removeEventListener('storage', onStorage);
  }, [followOthers]);

  // Keep pointing the server's tape engine at this window's selection even
  // though the tape band no longer renders here: tape.watch's lastSelected is
  // what drives the TWS window link, the live L2 depth target, and the IBKR
  // borrow subscription — clicking a row in the main window must keep moving
  // TWS whether or not a tape pop-out is open. Keyed on `feed` because
  // tape.watch is per-connection server state (a reconnect must re-send it).
  useEffect(() => {
    if (!sym || feed !== 'up') return;
    watchTape(sym);
    return () => watchTape('');
  }, [sym, feed]);

  return (
    <section className="sympanel" style={style}>
      <div className="sympanel-head">
        {sym ? (
          <>
            <b className="sympanel-sym">{sym}</b>
            <TrendChip />
            <button
              className="iconbtn tile-btn"
              title="pop out the tape reader for this symbol"
              onClick={() => window.open(`/?pane=tape&sym=${encodeURIComponent(sym)}`, '_blank', 'width=920,height=820')}
            >
              ⧉
            </button>
            {STATS.map((st) => {
              const v = row?.[st.key];
              const tone =
                st.key.startsWith('chg') && typeof v === 'number' ? (v >= 0 ? 'pos' : 'neg') : '';
              return (
                <span key={st.key} className="sympanel-stat">
                  <label>{st.label}</label>
                  <em className={tone}>{v == null ? '—' : fmt(v)}</em>
                </span>
              );
            })}
          </>
        ) : (
          <span className="muted">Click a row to load its fundamentals & news</span>
        )}
        <button
          className={`segbtn ${followOthers ? 'on' : ''}`}
          style={{ marginLeft: 'auto' }}
          title="Follow symbol selections made in other windows (review, charts, tape)"
          onClick={() => useStore.getState().setFollowOthers(!followOthers)}
        >
          🔗 Follow
        </button>
        <button
          className="iconbtn tile-btn"
          title="Hide this column — the scan grids take the full width (◧ Details in the tab row brings it back)"
          onClick={() => {
            useStore.getState().setPane({ rightHidden: true });
            useStore.getState().persistLayout();
          }}
        >
          ⇥
        </button>
      </div>
      {sym && (
        <div className="sympanel-fund">
          <span className="sympanel-stat">
            <label>FLOAT</label>
            <em>{fmt(row?.float)}</em>
          </span>
          <BorrowStat />
          <span className="sympanel-sep" />
          {fundamentalsErr?.notEntitled ? (
            <span className="muted fund-note" title={fundamentalsErr.msg}>
              Fundamentals need the Massive Financials &amp; Ratios add-on
            </span>
          ) : fundamentalsErr ? (
            <span className="muted fund-note" title={fundamentalsErr.msg}>
              ⚠ Fundamentals unavailable
            </span>
          ) : !fundamentals ? (
            <span className="muted fund-note">Loading fundamentals…</span>
          ) : fundamentals.quarters.length === 0 ? (
            <span className="muted fund-note">No SEC filings found for {sym}.</span>
          ) : (
            <>
              <span className="sympanel-stat">
                <label>REVENUE {fundamentals.quarters[0].periodEnd.slice(0, 7)}</label>
                <em>{money(fundamentals.quarters[0].revenue)}</em>
              </span>
              <span className="sympanel-stat">
                <label>CASH</label>
                <em>{money(fundamentals.quarters[0].cash)}</em>
              </span>
              <span className="sympanel-stat">
                <label>BURN / QTR</label>
                <em className={fundamentals.burnQuarterly ? 'neg' : 'pos'}>
                  {fundamentals.burnQuarterly ? money(fundamentals.burnQuarterly) : 'cash-flow +'}
                </em>
              </span>
              {fundamentals.runwayMonths != null && (
                <span className="sympanel-stat">
                  <label>RUNWAY</label>
                  <em className={runwayTone(fundamentals.runwayMonths)}>
                    {fundamentals.runwayMonths.toFixed(1)}mo
                  </em>
                </span>
              )}
            </>
          )}
        </div>
      )}
      <SetupCard />
    </section>
  );
}
