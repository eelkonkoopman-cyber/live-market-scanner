import { useState, type ReactNode, type CSSProperties } from 'react';
import { useStore } from '../store';
import { sendWatchlist } from '../ws';
import { SideBoxes } from './SideBoxes';

// WatchlistSidebar renders the persisted watchlist as draggable chips: drag
// one onto a chart cell to assign it, drag onto another chip to reorder, ✕ to
// remove, type+Enter to add. Every edit sends the WHOLE list (watchlist.set);
// the store only updates when the server's authoritative watchlist.data echo
// comes back — no local double-apply.
// tickerish gates external drag payloads (a grid row from another window
// carries a clean symbol, but the OS lets ANY dragged text land here).
//
// Extracted verbatim out of ChartsPane.tsx (addendum 2, §7) so the Signal
// window's grid can reuse it: the ONLY behavioural difference between the two
// call sites is `boxes`, which gates the gainers/decliners lists — those
// depend on scan views ChartsPane sets up (viewTiles/resyncScan over
// chartGrid.boxes' scanIds) that the Signal window has no reason to mirror,
// so SignalPane passes boxes={false} and gets the chip list alone — plus, as of
// the 9EMA flag market scan, `children`: whatever the caller wants in the space
// under the chips where the boxes would go (the Signal window puts its
// FlagScanList there). The boxes win when both are given; ChartsPane passes
// no children and leaves `boxes` at its default, so it renders exactly as before.
export function tickerish(raw: string): string | null {
  const s = raw.trim().toUpperCase();
  return /^[A-Z][A-Z0-9.\-]{0,9}$/.test(s) ? s : null;
}

export function WatchlistSidebar({
  boxes = true,
  children,
  style,
}: {
  boxes?: boolean;
  children?: ReactNode;
  style?: CSSProperties; // e.g. the Signal window's dragged sidebar width
}) {
  const watchlist = useStore((s) => s.watchlist);
  const [input, setInput] = useState('');
  const [dragSym, setDragSym] = useState<string | null>(null);
  const [dropSym, setDropSym] = useState<string | null>(null);
  const [extOver, setExtOver] = useState(false);

  const add = () => {
    const s = input.trim().toUpperCase();
    if (!s) return;
    if (!watchlist.includes(s)) sendWatchlist([...watchlist, s]);
    setInput('');
  };
  const remove = (s: string) => sendWatchlist(watchlist.filter((x) => x !== s));
  const reorder = (from: string, to: string) => {
    if (from === to) return;
    const next = watchlist.filter((x) => x !== from);
    const at = next.indexOf(to);
    if (at < 0) return;
    next.splice(at, 0, from);
    sendWatchlist(next);
  };

  return (
    <aside style={style}
      className={`charts-watchlist${extOver ? ' dropzone' : ''}`}
      // The sidebar as a whole accepts tickers dragged in from ANY window's
      // scan grid (dragSym null = the drag didn't start on our own chips).
      onDragOver={(e) => {
        if (dragSym) return; // internal reorders are handled per-chip
        e.preventDefault();
        e.dataTransfer.dropEffect = 'copy';
        setExtOver(true);
      }}
      onDragLeave={() => setExtOver(false)}
      onDrop={(e) => {
        setExtOver(false);
        if (dragSym) return;
        e.preventDefault();
        const sym = tickerish(e.dataTransfer.getData('text/plain'));
        if (sym && !watchlist.includes(sym)) sendWatchlist([...watchlist, sym]);
      }}
    >
      <input
        className="wl-add"
        value={input}
        placeholder="add symbol…"
        onChange={(e) => setInput(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') add();
        }}
      />
      <div className="wl-chips">
        {watchlist.map((s) => (
          <span
            key={s}
            className={`wl-chip${dragSym === s ? ' dragging' : ''}${dropSym === s ? ' dropzone' : ''}`}
            title="drag onto a chart · drag onto another symbol to reorder"
            draggable
            onDragStart={(e) => {
              setDragSym(s);
              e.dataTransfer.effectAllowed = 'copyMove';
              e.dataTransfer.setData('text/plain', s); // Firefox needs a payload
            }}
            onDragOver={(e) => {
              if (!dragSym || dragSym === s) return;
              e.preventDefault(); // required to make this a valid drop target
              e.dataTransfer.dropEffect = 'move';
              setDropSym(s);
            }}
            onDragLeave={() => setDropSym((d) => (d === s ? null : d))}
            onDrop={(e) => {
              e.preventDefault();
              e.stopPropagation(); // keep the aside's external-drop handler out of chip drops
              const from = dragSym ?? tickerish(e.dataTransfer.getData('text/plain'));
              if (from) reorder(from, s); // external syms insert at this spot (filter no-ops, splice adds)
              setDragSym(null);
              setDropSym(null);
            }}
            onDragEnd={() => {
              setDragSym(null);
              setDropSym(null);
            }}
          >
            {s}
            <button className="wl-x" title="remove" onClick={() => remove(s)}>
              ✕
            </button>
          </span>
        ))}
        {watchlist.length === 0 && <span className="wl-hint">Type a symbol above and press Enter, then drag it onto a chart.</span>}
      </div>
      {boxes ? <SideBoxes /> : children}
    </aside>
  );
}
