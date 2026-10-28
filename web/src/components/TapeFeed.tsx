import { useStore } from '../store';
import { fmt } from '../format';
import { fmtClock as hhmmss } from '../clockFormat';

// TapeFeed is the scrolling raw time & sales for the pop-out tape window:
// one row per print — time / size / price — colored by the same Lee-Ready
// aggressor side the CVD and verdict pipeline computed for it (buy = pos,
// sell = neg, neutral = muted). Newest prints render at the TOP (the store's
// ring is already newest-first), so there is no autoscroll to manage: the
// natural resting scroll position (top) always shows the live edge.
//
// Prints at/above the engine's rolling block threshold (tape.blockThreshold,
// the same signal the notable-prints/regime pipeline uses) render heavier so
// blocks jump out of the stream.
//
// Perf: this re-renders ~4x/s on a busy tape (one flushed batch per
// tape.prints frame), so rows are deliberately dumb — a single map, no
// per-row hooks or handlers. Only the first MAX_ROWS of the store's ring are
// rendered; the ring's extra depth just absorbs bursts.
const MAX_ROWS = 150;

const priceStr = (p: number) => p.toFixed(p < 1 ? 4 : 2);

export function TapeFeed() {
  const prints = useStore((s) => s.tapeFeed);
  const dropped = useStore((s) => s.tapeFeedDropped);
  // A number selector, so the 1 Hz tape.stat refresh only re-renders this
  // component when the threshold itself moves.
  const blockThreshold = useStore((s) => s.tape?.blockThreshold ?? 0);

  if (prints.length === 0 && dropped === 0) {
    return <div className="tape-feed-empty muted">No prints yet — waiting for trades…</div>;
  }

  const rows = prints.length > MAX_ROWS ? prints.slice(0, MAX_ROWS) : prints;

  return (
    <div className="tape-feed">
      {dropped > 0 && (
        <div
          className="tape-feed-dropped"
          title="Prints lost to backpressure since this symbol was opened — the engine never blocks on a slow window, so bursts can outrun the stream"
        >
          … {fmt(dropped)} prints dropped
        </div>
      )}
      {rows.map((p, i) => {
        const block = blockThreshold > 0 && p.size >= blockThreshold;
        return (
          <div key={`${p.ts}-${i}`} className={`tape-feed-row ${p.side}${block ? ' block' : ''}`}>
            <span className="tape-feed-time">{hhmmss(p.ts)}</span>
            <span className="tape-feed-size">{fmt(p.size)}</span>
            <span className="tape-feed-price">{priceStr(p.price)}</span>
          </div>
        );
      })}
    </div>
  );
}
