import { useEffect, useRef } from 'react';
import { createChart, type ChartApi } from '../vendor/blackthorn-charts';
import type { Bar } from '../types';
import { chartColors, chartFonts } from '../chartTheme';
import { useTheme } from '../useTheme';

// CandleChart renders an inline candlestick chart for a grid row using the
// vendored Blackthorn (Canvas 2D) engine — one lightweight chart per visible
// row. pointer-events are disabled in CSS so it doesn't hijack list scrolling.
export function CandleChart({ bars }: { bars: Bar[] }) {
  const elRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<ChartApi | null>(null);
  const theme = useTheme(); // re-render + re-color on ANY window's theme change

  useEffect(() => {
    if (!elRef.current) return;
    const chart = createChart(elRef.current, {
      chartType: 'candles',
      showVolume: true,
      showGrid: true,
      showLegend: false, // inline: keep it clean; the table already shows OHLC
      showSessionBands: false,
      // A last-price chip is clutter at this size, and the row already shows
      // Last in its own column.
      showLastPrice: false,
      rightOffset: 2,
      fonts: chartFonts(),
      // The row card paints its own background (see styles.css), so this
      // chart's background stays transparent even though chartColors()
      // returns the theme's opaque bg-base for every other field.
      colors: { ...chartColors(), background: 'rgba(0,0,0,0)' },
    });
    chartRef.current = chart;
    return () => {
      chart.remove();
      chartRef.current = null;
    };
  }, []);

  // Re-tint in place on theme change — same applyOptions({ colors }) path as
  // MonitorChart; background stays transparent for the same reason as above.
  useEffect(() => {
    chartRef.current?.applyOptions({ colors: { ...chartColors(), background: 'rgba(0,0,0,0)' } });
  }, [theme]);

  useEffect(() => {
    const chart = chartRef.current;
    if (!chart || !bars || bars.length < 2) return;
    chart.setData(bars.map((b) => ({ ts: b.t, open: b.o, high: b.h, low: b.l, close: b.c, volume: b.v })));
    chart.timeScale().fitContent();
  }, [bars]);

  return <div className="candle" ref={elRef} />;
}
