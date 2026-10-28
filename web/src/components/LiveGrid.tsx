import { memo, useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from 'react';
import { useVirtualizer } from '@tanstack/react-virtual';
import { useStore } from '../store';
import { upsertScan } from '../ws';
import { formatField, fmtDur } from '../format';
import { fieldDef, fieldLabel, fieldTitle } from '../fields';
import { e9Badge } from '../e9';
import { mergeHeldOrder } from '../gridHold';
import { cellFlash } from '../cellFlash';
import { CandleChart } from './CandleChart';
import { ColResizer } from './Resize';
import type { Bar, Filter, Row } from '../types';

const DEFAULT_COLS = ['last', 'chg5m', 'chgDay', 'vol', 'rvol', 'news'];

// describe renders a scan's filter as a one-line criteria summary, shown in the
// empty state so "why is nothing here?" is answered in place instead of
// requiring a trip into the editor.
function describe(f: Filter, nested = false): string {
  if (f.conds?.length) {
    const op = (f.op ?? 'and').toUpperCase();
    const s = f.conds.map((c) => describe(c, true)).filter(Boolean).join(` ${op} `);
    return nested ? `(${s})` : s;
  }
  if (!f.field) return '';
  const label = fieldLabel(f.field);
  // isTrue/isFalse leaves carry no value — don't render "undefined".
  if (f.value === undefined || f.value === null) return `${label} ${f.cmp ?? ''}`.trim();
  // newsAge is stored in SECONDS; rendering it as a duration stops 300 from
  // reading as five hours when it means five minutes. Applies inside `between`
  // ranges too, which arrive as [lo, hi] arrays.
  const one = (x: unknown) => (f.field === 'newsAge' && typeof x === 'number' ? fmtDur(x) : String(x));
  const v = Array.isArray(f.value) ? f.value.map(one).join('–') : one(f.value);
  return `${label} ${f.cmp ?? ''} ${v}`.replace(/\s{2,}/g, ' ');
}

const DEFAULT_SYM_W = 96;
const DEFAULT_COL_W = 92;
const DEFAULT_CHART_W = 400;

// Each stock is a card. A scan with the optional "chart" column enabled gets
// the taller slot (card height 132 + gap 16) to fit the inline candlestick;
// otherwise the compact slot (44 + 8). The card's actual height is set in CSS
// via the .has-chart modifier class — these must stay in sync with it.
const ROW_SLOT_COMPACT = 52;
const ROW_SLOT_CHART = 148;

// How long the cursor can sit idle over a held list before it lets go on its
// own (also released immediately on mouseleave).
const HOLD_IDLE_MS = 10_000;

export function LiveGrid({
  scanId,
  style,
  onHeldChange,
}: {
  scanId: string;
  style?: CSSProperties;
  onHeldChange?: (held: boolean) => void;
}) {
  const grid = useStore((s) => s.grids[scanId]);
  const scan = useStore((s) => s.scans.find((x) => x.id === scanId));
  const gridColW = useStore((s) => s.gridColW);
  const setGridColW = useStore((s) => s.setGridColW);
  const persistLayout = useStore((s) => s.persistLayout);
  const cols = scan?.columns?.length ? scan.columns : DEFAULT_COLS;
  const hasChart = cols.includes('chart');
  const liveOrder = grid?.order ?? [];
  const parentRef = useRef<HTMLDivElement>(null);

  // Hover-freeze: a scan sorted by a fast-moving field (e.g. 5m %) can re-sort
  // several times a second, so a row under the cursor is a moving target.
  // While the mouse sits over the list we hold the ORDER — cell values keep
  // updating live — and only let it re-sort once the user looks away (leaves
  // the list, or stops moving for HOLD_IDLE_MS).
  const [held, setHeld] = useState(false);
  const frozenOrderRef = useRef<string[]>([]);
  const lastRowsRef = useRef<Record<string, Row>>({});
  const holdTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Bounded cache: not held, just point at the live rows object (the store
  // hands us a fresh object every tick, so this is a free reassignment, not a
  // copy) — the cache can never hold more than the current match count. Only
  // a HOLD accumulates, merging in departed symbols for exactly as long as
  // the freeze lasts, and it resets to the live set the instant the hold
  // releases, instead of growing for the life of the component.
  if (held) lastRowsRef.current = { ...lastRowsRef.current, ...(grid?.rows ?? {}) };
  else lastRowsRef.current = grid?.rows ?? {};

  const order = held ? mergeHeldOrder(frozenOrderRef.current, liveOrder) : liveOrder;

  const releaseHold = () => {
    if (holdTimerRef.current) {
      clearTimeout(holdTimerRef.current);
      holdTimerRef.current = null;
    }
    setHeld(false);
  };

  const armHold = () => {
    if (!held) {
      frozenOrderRef.current = liveOrder;
      setHeld(true);
    }
    // Every move restarts the idle clock — only a still cursor lets it go.
    if (holdTimerRef.current) clearTimeout(holdTimerRef.current);
    holdTimerRef.current = setTimeout(releaseHold, HOLD_IDLE_MS);
  };

  // Refs so unmounting doesn't depend on a re-render having happened first.
  useEffect(() => {
    return () => {
      if (holdTimerRef.current) clearTimeout(holdTimerRef.current);
    };
  }, []);

  // The frame around this grid (TileFrame / ScanPane) shows the match count
  // beside the scan name via MatchCount below; it needs to know when the
  // order is frozen so it can say so — there's no title row in here any more.
  useEffect(() => {
    onHeldChange?.(held);
  }, [held, onHeldChange]);

  const virt = useVirtualizer({
    count: order.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => (hasChart ? ROW_SLOT_CHART : ROW_SLOT_COMPACT),
    overscan: 4,
  });

  const sort = scan?.sort?.[0];
  const sort2 = scan?.sort?.[1];

  // Plain click sets the primary sort (toggling direction on repeat). Shift+
  // click sets the SECONDARY key, which only breaks ties on the primary —
  // shift-clicking the current primary is a no-op, since a field can't break
  // its own tie.
  const onSort = (field: string, secondary: boolean) => {
    if (!scan) return;
    if (secondary) {
      if (field === sort?.field) return;
      const dir = sort2?.field === field && sort2.dir === 'desc' ? 'asc' : 'desc';
      upsertScan({ ...scan, sort: [sort ?? { field: 'chg5m', dir: 'desc' }, { field, dir }] });
      return;
    }
    const dir = sort?.field === field && sort.dir === 'desc' ? 'asc' : 'desc';
    // Promoting the existing secondary to primary drops it as the tiebreak.
    const keep = sort2 && sort2.field !== field ? [sort2] : [];
    upsertScan({ ...scan, sort: [{ field, dir }, ...keep] });
  };

  const arrow = (dir?: string) => (dir === 'asc' ? '▲' : '▼');
  const ind = (field: string) => {
    if (sort?.field === field) return ` ${arrow(sort.dir)}`;
    if (sort2?.field === field) return ` ${arrow(sort2.dir)}₂`; // subscript 2
    return '';
  };

  // Column order lives in the scan def (persisted server-side), so a reorder is
  // just an upsert — it survives restarts and is per-scan. Native HTML5 drag is
  // used because it coexists cleanly with the two other header gestures: a
  // click still sorts (drag only begins past the browser's move threshold), and
  // ColResizer's preventDefault on mousedown suppresses dragstart so resizing
  // never turns into a reorder.
  const [dragCol, setDragCol] = useState<string | null>(null);
  const [dropCol, setDropCol] = useState<string | null>(null);

  const moveColumn = (from: string, to: string) => {
    if (!scan || from === to) return;
    const next = cols.filter((c) => c !== from);
    const at = next.indexOf(to);
    if (at < 0) return;
    next.splice(at, 0, from);
    upsertScan({ ...scan, columns: next });
  };

  // Per-column widths (sym + each metric/chart column), draggable via
  // ColResizer and persisted (keyed by field name -> shared across scans).
  // Memoized on its inputs: GridRow is memo()'d and takes `widths` as a prop,
  // so a fresh object per render would re-render every visible row on every
  // tick even when its own row object is unchanged.
  const widths = useMemo(() => {
    const w: Record<string, number> = { sym: gridColW.sym ?? DEFAULT_SYM_W };
    for (const c of cols) w[c] = gridColW[c] ?? (c === 'chart' ? DEFAULT_CHART_W : DEFAULT_COL_W);
    return w;
  }, [cols, gridColW]);
  const resize = (key: string, min: number, max: number) => (
    <ColResizer width={widths[key]} min={min} max={max} onChange={(w) => setGridColW(key, w)} onCommit={persistLayout} />
  );

  // Columns can be dragged wider than the visible area (especially once the
  // surrounding panes are also resized), so the row width is "at least the
  // container, but grow to fit" — this is what makes the header + rows scroll
  // horizontally together via .grid-scroll's own overflow, instead of the
  // header bleeding past its box (rows are individually clipped) or columns
  // becoming unreachable.
  const rowW = widths.sym + cols.reduce((sum, c) => sum + widths[c], 0);
  const rowWidthStyle = `max(${rowW}px, 100%)`;

  return (
    <div className="grid" style={style}>
      <div ref={parentRef} className="grid-scroll" onMouseMove={armHold} onMouseLeave={releaseHold}>
        {/* No rows, nothing to label: the header is also wider than a tiled
            cell, so keeping it put a horizontal scrollbar on an empty scan. */}
        {order.length > 0 && (
          <div className="grid-row head" style={{ width: rowWidthStyle }}>
            <div
              className="cell sym sortable"
              style={{ flex: `0 0 ${widths.sym}px` }}
              title="Click to sort · Shift+click to set the tiebreak"
              onClick={(e) => onSort('sym', e.shiftKey)}
            >
              Symbol{ind('sym')}
              {resize('sym', 70, 220)}
            </div>
            {cols.map((c) => (
              <div
                key={c}
                className={`cell ${c === 'chart' ? '' : 'sortable'} draggable${dragCol === c ? ' dragging' : ''}${dropCol === c ? ' dropzone' : ''}`}
                style={{ flex: `0 0 ${widths[c]}px` }}
                title={c === 'chart' ? 'drag to reorder' : `${fieldTitle(c)}\nClick to sort · Shift+click to set the tiebreak · drag to reorder`}
                onClick={(e) => c !== 'chart' && onSort(c, e.shiftKey)}
                draggable
                onDragStart={(e) => {
                  setDragCol(c);
                  e.dataTransfer.effectAllowed = 'move';
                  e.dataTransfer.setData('text/plain', c); // Firefox needs a payload
                }}
                onDragOver={(e) => {
                  if (!dragCol || dragCol === c) return;
                  e.preventDefault(); // required to make this a valid drop target
                  e.dataTransfer.dropEffect = 'move';
                  setDropCol(c);
                }}
                onDragLeave={() => setDropCol((d) => (d === c ? null : d))}
                onDrop={(e) => {
                  e.preventDefault();
                  const from = dragCol ?? e.dataTransfer.getData('text/plain');
                  if (from) moveColumn(from, c);
                  setDragCol(null);
                  setDropCol(null);
                }}
                onDragEnd={() => {
                  setDragCol(null);
                  setDropCol(null);
                }}
              >
                {fieldLabel(c)}
                {c !== 'chart' && ind(c)}
                {c === 'chart' ? resize(c, 160, 900) : resize(c, 50, 300)}
              </div>
            ))}
          </div>
        )}
        {order.length === 0 && (
          <div className="muted pad empty-scan">
            <div>No matches right now.</div>
            {scan?.filter && (
              <div className="empty-crit" title={describe(scan.filter)}>
                {describe(scan.filter)}
              </div>
            )}
          </div>
        )}
        {/* Only as wide as the rows when there ARE rows: a zero-height spacer
            at the full row width still counts toward scrollWidth and would
            keep a horizontal scrollbar on an empty scan. */}
        <div style={{ height: virt.getTotalSize(), position: 'relative', width: order.length ? rowWidthStyle : '100%' }}>
          {virt.getVirtualItems().map((vi) => {
            const sym = order[vi.index];
            const live = grid?.rows[sym];
            // While held, a symbol that left the live grid still renders from
            // the last row we saw for it (fixed) rather than vanishing.
            const row = live ?? (held ? lastRowsRef.current[sym] : undefined) ?? {};
            const stale = held && !live;
            return <GridRow key={sym} sym={sym} row={row} cols={cols} widths={widths} top={vi.start} stale={stale} />;
          })}
        </div>
      </div>
    </div>
  );
}

const GridRow = memo(function GridRow({
  sym,
  row,
  cols,
  widths,
  top,
  stale,
}: {
  sym: string;
  row: Row;
  cols: string[];
  widths: Record<string, number>;
  top: number;
  stale: boolean;
}) {
  const openChart = useStore((s) => s.openChart);
  const hasChart = cols.includes('chart');
  const prev = useRef<Row>({});
  const cells = cols.map((c) => {
    if (c === 'chart') {
      return (
        <div key={c} className="cell chart" style={{ flex: `0 0 ${widths.chart}px` }}>
          <CandleChart bars={(row.bars as Bar[]) ?? []} />
        </div>
      );
    }
    const v = row[c];
    const p = prev.current[c];
    // up/down flash on a changed number — except the ages that count up every
    // second (e9Age, …): see cellFlash.ts.
    const flash = cellFlash(c, v, p);
    // Boolean columns (news, halted, shortable) render as a real dot element
    // rather than a "●" glyph, so it can be sized and given a glow
    // independently of the numeric text around it.
    let tone: 'pos' | 'neg' | 'muted' | '' = '';
    let body: ReactNode;
    if (typeof v === 'boolean') {
      body = v ? <span className={`flag ${c}`} title={c} /> : null;
    } else if (c === 'dtrend') {
      // Daily-trend verdict: green ↗ / gray → / red ↘; dash = not enough
      // daily history to classify (recent listings).
      body =
        typeof v === 'number' ? (
          <span className={`dtrend ${v > 0 ? 'up' : v < 0 ? 'down' : 'flat'}`}
            title={v > 0 ? 'daily uptrend' : v < 0 ? 'daily downtrend' : 'daily flat'}>
            {v > 0 ? '↗' : v < 0 ? '↘' : '→'}
          </span>
        ) : (
          <span className="muted">–</span>
        );
    } else if (fieldDef(c)?.kind === 'e9State') {
      // 9EMA flag stage (market scan): a badge, like dtrend — the stage word is
      // the signal, so a column of them reads at a glance. A dash is "not a
      // candidate" (pre-market volume under 1x its 10-day average, or not
      // evaluated yet); IDLE is a candidate with no flag forming.
      const b = e9Badge(v);
      body = b ? (
        <span className={`e9-badge ${b.cls}`} title={b.title}>
          {b.label}
        </span>
      ) : (
        <span className="muted">–</span>
      );
    } else if (c === 'hits' && typeof v === 'number') {
      // Recurring (2+) is the signal — it gets a badge; a first appearance
      // stays quiet so the column reads as "what keeps coming back".
      body =
        v >= 2 ? (
          <span className="hits-badge" title={`${v}th entry into this scan today`}>{v}×</span>
        ) : (
          <span className="muted">1×</span>
        );
    } else {
      const f = formatField(c, v, row);
      body = f.text;
      tone = f.tone;
    }
    return (
      <div key={c} className={`cell ${tone} ${flash}`} style={{ flex: `0 0 ${widths[c]}px` }}>
        {body}
      </div>
    );
  });
  prev.current = row;
  return (
    <div
      className={`grid-row data clickable${hasChart ? ' has-chart' : ''}`}
      style={{
        position: 'absolute',
        top: 0,
        transform: `translateY(${top}px)`,
        width: '100%',
        ...(stale ? { opacity: 0.55 } : {}),
      }}
      onClick={() => openChart(sym)}
      title={`Open ${sym} · drag to the charts window's watchlist or a chart cell${stale ? ' · left the scan — releases when you move the mouse away' : ''}`}
      draggable
      onDragStart={(e) => {
        // The payload crosses WINDOWS (main grid -> charts monitor window's
        // watchlist/cells): HTML5 DnD is OS-level, so a plain-text ticker
        // works between two of our windows the same as within one. A click
        // still selects — the browser only begins a drag past its movement
        // threshold (same coexistence the column-reorder headers rely on).
        e.dataTransfer.setData('text/plain', sym); // Firefox requires a payload
        e.dataTransfer.effectAllowed = 'copy';
      }}
    >
      <div className="cell sym" style={{ flex: `0 0 ${widths.sym}px` }}>{sym}</div>
      {cells}
    </div>
  );
});

// MatchCount is the "12 matches · held" readout the frames (TileFrame, the
// ?pane=scan window) show beside the scan name. The grid used to carry it in
// a title row of its own, but that row also repeated the scan's name right
// under the frame's header — everything twice, and ~40px per tile.
export function MatchCount({ scanId, held }: { scanId: string; held: boolean }) {
  const n = useStore((s) => s.grids[scanId]?.order.length ?? 0);
  return (
    <span
      className={`tile-count${n === 0 ? ' zero' : ''}`}
      title={
        held
          ? 'Row order is frozen while the mouse is over the list; it releases when you leave or after 10 s without moving.'
          : undefined
      }
    >
      {n} {n === 1 ? 'match' : 'matches'}
      {held ? ' · held' : ''}
    </span>
  );
}
