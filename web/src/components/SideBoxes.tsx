import { ExchangeClocks } from './ExchangeClocks';
import { useMemo, useState } from 'react';
import { useStore } from '../store';
import { formatField } from '../format';
import { fieldDef } from '../fields';
import { selectSymbol } from '../ws';
import { rankSyms } from '../autoFill';
import type { SideBox, SideBoxMetric } from '../types';

// SideBoxes: the "simplistic scanner" under the charts window's watchlist
// (user: "make a simple gainers/decliners list. Like a simplistic scanner…
// I want at least two boxes there. Only ticker, price, volume and gain is
// required"). Each box is fed by one of the scanner's own scans — the server
// already evaluates those over every tracked ticker and streams their rows —
// and shows that scan's top N by Day % or 5m %, rising (gainers) or falling
// (decliners). Columns: ticker, price, volume, PM× (pre-market volume vs the
// 10-day average daily volume, user: "add the Pm Vol vs 10x day average") and
// the gain. The server streams those fields with every scan's rows
// (scan.sideBoxFields), so any scan can feed a box whatever its own columns.
// Rows drag onto a chart cell (or up into the
// watchlist) exactly like watchlist chips, and a click sends the symbol to
// TWS like a chart click does. The box list lives in the persisted
// charts-window layout blob (chartGrid.boxes) so it survives restarts and
// follows to other charts windows; ChartsPane narrows this window's scan
// view to the scans the boxes use and resyncs them.

export const DEFAULT_SIDE_BOXES: SideBox[] = [
  { id: 'gainers', scanId: null, dir: 'desc', metric: 'chgDay', limit: 10 },
  { id: 'decliners', scanId: null, dir: 'asc', metric: 'chgDay', limit: 10 },
];

const METRICS: { key: SideBoxMetric; label: string }[] = [
  { key: 'chgDay', label: 'Day %' },
  { key: 'chg5m', label: '5m %' },
];
const LIMITS = [10, 20, 50] as const;
const PMX_TITLE = fieldDef('pmVolX')?.hint ?? 'pre-market volume vs the 10-day average daily volume';

// The box's PM× column is narrow: drop decimals as the multiple grows so a
// "142.60x" news runner still fits (143x, 31.1x, 3.05x).
export function fmtPmX(v: unknown): string {
  if (typeof v !== 'number' || !isFinite(v)) return '–';
  if (v >= 99.5) return `${Math.round(v)}x`;
  if (v >= 9.995) return `${v.toFixed(1)}x`;
  return `${v.toFixed(2)}x`;
}

export function SideBoxes() {
  const boxes = useStore((s) => s.chartGrid.boxes ?? DEFAULT_SIDE_BOXES);
  const setSideBoxes = useStore((s) => s.setSideBoxes);
  const update = (id: string, patch: Partial<SideBox>) =>
    setSideBoxes(boxes.map((b) => (b.id === id ? { ...b, ...patch } : b)));
  return (
    <div className="sideboxes">
      {boxes.map((b) => (
        <SideBoxView
          key={b.id}
          box={b}
          onChange={(p) => update(b.id, p)}
          onRemove={() => setSideBoxes(boxes.filter((x) => x.id !== b.id))}
        />
      ))}
      <ExchangeClocks />
      <button
        className="mini sidebox-add"
        title="add another gainers/decliners list"
        onClick={() =>
          setSideBoxes([
            ...boxes,
            { id: `box-${Date.now().toString(36)}`, scanId: null, dir: 'desc', metric: 'chgDay', limit: 10 },
          ])
        }
      >
        + add list
      </button>
    </div>
  );
}

function SideBoxView({
  box,
  onChange,
  onRemove,
}: {
  box: SideBox;
  onChange: (p: Partial<SideBox>) => void;
  onRemove: () => void;
}) {
  const scans = useStore((s) => s.scans);
  const grid = useStore((s) => (box.scanId ? s.grids[box.scanId] : undefined));
  const scan = scans.find((x) => x.id === box.scanId);
  // Settings are open while no scan is chosen and closed once one is, until
  // the user toggles them (⚙). Derived, not initialised once: the persisted
  // layout arrives from the server after mount, so a mount-time default
  // would leave every already-configured box open on each fresh load.
  const [cfgOverride, setCfgOverride] = useState<boolean | null>(null);
  const cfg = cfgOverride ?? !box.scanId;
  const setCfg = (v: boolean | ((prev: boolean) => boolean)) =>
    setCfgOverride(typeof v === 'function' ? v(cfg) : v);

  // Gainers are rows that are actually up, decliners rows actually down (see
  // rankSyms, shared with the charts window's Auto mode).
  const syms = useMemo(() => rankSyms(grid, box.metric, box.dir).slice(0, box.limit), [grid, box.metric, box.dir, box.limit]);

  const kind = box.dir === 'desc' ? 'gainers' : 'decliners';
  const title = `${box.dir === 'desc' ? 'Gainers' : 'Decliners'}${scan ? ` · ${scan.name}` : ''}`;

  return (
    <section className="sidebox">
      <header className="sidebox-head">
        <span className="t" title={title}>
          {title}
        </span>
        <button className="wl-x" title={cfg ? 'hide settings' : 'settings: scan, gainers/decliners, metric, rows'} onClick={() => setCfg((v) => !v)}>
          ⚙
        </button>
        <button className="wl-x" title="remove this list" onClick={onRemove}>
          ✕
        </button>
      </header>
      {cfg && (
        <div className="sidebox-cfg">
          <select
            value={box.scanId ?? ''}
            title="the scan that feeds this list"
            onChange={(e) => {
              onChange({ scanId: e.target.value || null });
              setCfgOverride(e.target.value ? false : null); // picked: close; cleared: back to "open while unbound"
            }}
          >
            <option value="">pick a scan…</option>
            {scans.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
          </select>
          <div className="seg" title="which end of the scan to show">
            <button className={`segbtn ${box.dir === 'desc' ? 'on' : ''}`} onClick={() => onChange({ dir: 'desc' })}>
              Gainers
            </button>
            <button className={`segbtn ${box.dir === 'asc' ? 'on' : ''}`} onClick={() => onChange({ dir: 'asc' })}>
              Decliners
            </button>
          </div>
          <div className="seg" title="rank by">
            {METRICS.map((m) => (
              <button key={m.key} className={`segbtn ${box.metric === m.key ? 'on' : ''}`} onClick={() => onChange({ metric: m.key })}>
                {m.label}
              </button>
            ))}
          </div>
          <div className="seg" title="how many rows">
            {LIMITS.map((n) => (
              <button key={n} className={`segbtn ${box.limit === n ? 'on' : ''}`} onClick={() => onChange({ limit: n })}>
                {n}
              </button>
            ))}
          </div>
        </div>
      )}
      <div className="sidebox-list">
        {!scan && <div className="sidebox-empty">Pick a scan above — its rows show here as {kind}.</div>}
        {scan && syms.length === 0 && (
          <div className="sidebox-empty">
            No {kind} in {scan.name} right now.
          </div>
        )}
        {syms.length > 0 && (
          <div className="sidebox-cols">
            <span>Ticker</span>
            <span>Price</span>
            <span>Vol</span>
            <span title={PMX_TITLE}>PM×</span>
            <span>{box.metric === 'chgDay' ? 'Day' : '5m'}</span>
          </div>
        )}
        {syms.map((sym) => {
          const row = grid?.rows[sym] ?? {};
          const gain = formatField(box.metric, row[box.metric]);
          return (
            <div
              key={sym}
              className="sidebox-row"
              draggable
              title="drag onto a chart (or up into the watchlist) · click to open in TWS"
              onDragStart={(e) => {
                e.dataTransfer.effectAllowed = 'copy';
                e.dataTransfer.setData('text/plain', sym);
              }}
              onClick={() => selectSymbol(sym)}
            >
              <span className="sb-sym">{sym}</span>
              <span className="sb-num">{formatField('last', row.last).text}</span>
              <span className="sb-num">{formatField('vol', row.vol).text}</span>
              <span className="sb-num">{fmtPmX(row.pmVolX)}</span>
              <span className={`sb-num ${gain.tone}`}>{gain.text}</span>
            </div>
          );
        })}
      </div>
    </section>
  );
}
