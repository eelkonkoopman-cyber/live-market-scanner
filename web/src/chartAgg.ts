import type { Bar } from './types';
import type { Candle } from './vendor/blackthorn-charts';

// Shared timeframe roll-up for Blackthorn charts, extracted verbatim from
// SymbolPanel so the charts monitor window (?pane=charts) renders the exact
// same 1m/5m/15m series as the docked symbol panel.

export function toCandle(b: Bar): Candle {
  return { ts: b.t, open: b.o, high: b.h, low: b.l, close: b.c, volume: b.v };
}

// aggregate rolls 1-minute bars up into tfMin-minute OHLCV buckets aligned to
// the epoch (lines up with ET clock boundaries since ET offsets are whole hours).
export function aggregate(bars: Bar[], tfMin: number): Candle[] {
  if (tfMin <= 1) return bars.map(toCandle);
  const bucketMs = tfMin * 60_000;
  const out: Candle[] = [];
  let cur: Candle | null = null;
  let curBucket = -1;
  for (const b of bars) {
    const bucket = Math.floor(b.t / bucketMs);
    if (bucket !== curBucket) {
      if (cur) out.push(cur);
      cur = { ts: bucket * bucketMs, open: b.o, high: b.h, low: b.l, close: b.c, volume: b.v };
      curBucket = bucket;
    } else if (cur) {
      cur.high = Math.max(cur.high, b.h);
      cur.low = Math.min(cur.low, b.l);
      cur.close = b.c;
      cur.volume += b.v;
    }
  }
  if (cur) out.push(cur);
  return out;
}
