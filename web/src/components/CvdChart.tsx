import { memo } from 'react';
import type { CVDPoint } from '../types';

// CvdChart draws the rolling cumulative-volume-delta line for the tape
// reader (buy volume minus sell volume, over time). Same pure-SVG, no-canvas
// convention as Sparkline — viewBox scaled to the series' own min/max,
// colour driven by CSS variables, stroke width kept crisp via
// vectorEffect="non-scaling-stroke" — just wider/shorter and stretched to
// its container (width 100%) since this is a standalone panel rather than an
// inline grid cell. Unlike Sparkline's up/down-over-the-window colouring,
// CVD's sign is the meaningful thing (mirrors app.js's drawChart: `last >= 0
// ? buy : sell`, same convention TapeBand already uses for delta figures),
// so colour tracks the sign of the latest value, and a zero baseline plus a
// light fill between the line and zero are drawn to make that sign obvious
// at a glance.
export const CvdChart = memo(function CvdChart({ data, height = 56 }: { data: CVDPoint[]; height?: number }) {
  if (!data || data.length < 2) return <span className="muted">–</span>;

  const w = 100;
  const h = height;
  const pad = 3;
  const n = data.length;

  let min = 0;
  let max = 0;
  for (const p of data) {
    if (p.value < min) min = p.value;
    if (p.value > max) max = p.value;
  }
  if (min === max) {
    min -= 1;
    max += 1;
  }
  const slack = (max - min) * 0.1 || 1;
  min -= slack;
  max += slack;
  const range = max - min;

  const t0 = data[0].ts;
  const t1 = data[n - 1].ts || t0 + 1;
  const px = (ts: number) => pad + ((ts - t0) / (t1 - t0 || 1)) * (w - 2 * pad);
  const py = (v: number) => pad + (1 - (v - min) / range) * (h - 2 * pad);

  const last = data[n - 1].value;
  const color = last >= 0 ? 'var(--pos)' : 'var(--neg)';
  const zeroY = py(0);

  const linePts = data.map((p) => `${px(p.ts).toFixed(1)},${py(p.value).toFixed(1)}`).join(' ');
  const areaPts = `${px(t0).toFixed(1)},${zeroY.toFixed(1)} ${linePts} ${px(t1).toFixed(1)},${zeroY.toFixed(1)}`;

  return (
    <svg className="cvd-chart" width="100%" height={h} viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" aria-hidden="true">
      <line x1={pad} y1={zeroY} x2={w - pad} y2={zeroY} stroke="var(--line2)" strokeWidth="1" vectorEffect="non-scaling-stroke" />
      <polygon points={areaPts} fill={color} fillOpacity="0.12" stroke="none" />
      <polyline points={linePts} fill="none" stroke={color} strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
    </svg>
  );
});
