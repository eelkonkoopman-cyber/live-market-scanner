import type { DepthLevel } from '../types';
import { fmt } from '../format';

// TapeLadder is the Level 2 bid/ask ladder for the tape reader — ported from
// the standalone Python app's renderDepth()/#dom. Most venues/data plans
// carry no L2 entitlement (IBKR sends inside quotes only), so the common
// case is the fallback message below, not the ladder itself; the inside-size
// dynamics (absorption/pull counts on BookStats) still work without it and
// are rendered elsewhere.
const priceStr = (p: number) => p.toFixed(p < 1 ? 4 : 2);

export function TapeLadder({
  hasDepth,
  bids,
  asks,
  depthNote,
}: {
  hasDepth: boolean;
  bids: DepthLevel[];
  asks: DepthLevel[];
  depthNote: string;
}) {
  if (!hasDepth) {
    // Straight from IBKR when present (e.g. a specific reason for this
    // venue/contract); otherwise the same generic reason app.js falls back to.
    const why = depthNote || 'needs a Level 2 subscription for this venue';
    return (
      <div className="tape-ladder-empty">
        No depth data — {why}.
        <br />
        Inside-size dynamics above still work.
      </div>
    );
  }

  const rows = Math.max(bids.length, asks.length);
  const max = Math.max(1, ...bids.map((l) => l.size), ...asks.map((l) => l.size));

  return (
    <div className="tape-ladder">
      {Array.from({ length: rows }, (_, i) => {
        const b = bids[i];
        const a = asks[i];
        return (
          <div key={i} className={`tape-ladder-row${i === 0 ? ' inside' : ''}`}>
            <div className="tape-ladder-bid">
              <div className="tape-ladder-bar" style={{ width: `${b ? (b.size / max) * 100 : 0}%` }} />
              <span className="tape-ladder-size">{b ? fmt(b.size) : ''}</span>
              <span className="tape-ladder-price">{b ? priceStr(b.price) : ''}</span>
            </div>
            <div className="tape-ladder-ask">
              <span className="tape-ladder-price">{a ? priceStr(a.price) : ''}</span>
              <span className="tape-ladder-size">{a ? fmt(a.size) : ''}</span>
              <div className="tape-ladder-bar" style={{ width: `${a ? (a.size / max) * 100 : 0}%` }} />
            </div>
          </div>
        );
      })}
    </div>
  );
}
