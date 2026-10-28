import { memo } from 'react';

// Sparkline draws a compact inline price line for a grid row. Coloured by net
// direction over the window (green up / red down). Lightweight pure-SVG — no
// charting library, so hundreds of live rows stay smooth.
export const Sparkline = memo(function Sparkline({ data }: { data: number[] }) {
  if (!data || data.length < 2) return <span className="muted">–</span>;

  const w = 88;
  const h = 20;
  const pad = 2;
  let min = data[0];
  let max = data[0];
  for (const v of data) {
    if (v < min) min = v;
    if (v > max) max = v;
  }
  const range = max - min || 1;
  const n = data.length;
  const pts = data
    .map((v, i) => {
      const x = pad + (i / (n - 1)) * (w - 2 * pad);
      const y = pad + (1 - (v - min) / range) * (h - 2 * pad);
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(' ');
  const up = data[n - 1] >= data[0];

  return (
    <svg className="spark" width={w} height={h} viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" aria-hidden="true">
      <polyline points={pts} fill="none" stroke={up ? 'var(--pos)' : 'var(--neg)'} strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
    </svg>
  );
});
