//! Crosshair (spans all panes) + hovered-bar OHLC legend (BUILD-PLAN §1.8/§3.2),
//! its own layer so a mouse move never repaints the series.

import type { DataModel } from '../core/datamodel';
import type { TimeScale } from '../core/timescale';
import type { PaneLayout } from '../core/panes';
import type { ChartColors } from '../core/types';
import { align } from './layers';
import { font } from './fonts';

const _f = new Map<string, Intl.DateTimeFormat>();
/** "Apr 29, 09:35" for intraday bars; "Apr 29, 2026" for daily and up. */
function dtf(tz: string, daily: boolean): Intl.DateTimeFormat {
  const key = `${tz}|${daily ? 'd' : 'i'}`;
  let f = _f.get(key);
  if (!f) {
    f = daily
      ? new Intl.DateTimeFormat('en-US', { timeZone: tz, month: 'short', day: 'numeric', year: 'numeric' })
      : new Intl.DateTimeFormat('en-US', {
          timeZone: tz, month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false,
        });
    _f.set(key, f);
  }
  return f;
}

function fmtVol(v: number): string {
  if (v >= 1e9) return (v / 1e9).toFixed(2) + 'B';
  if (v >= 1e6) return (v / 1e6).toFixed(2) + 'M';
  if (v >= 1e3) return (v / 1e3).toFixed(1) + 'K';
  return String(Math.round(v));
}

export function drawCrosshair(
  ctx: CanvasRenderingContext2D,
  layout: PaneLayout,
  mouse: { x: number; y: number } | null,
  data: DataModel,
  ts: TimeScale,
  colors: ChartColors,
  tz: string,
  showLegend = true,
  legendTitle = '',
): void {
  const n = data.length;
  if (n === 0) return;
  const plotW = layout.plotW;
  const contentH = layout.contentH;

  const inPlot = !!mouse && mouse.x >= 0 && mouse.x <= plotW && mouse.y >= 0 && mouse.y <= contentH;
  let idx = -1;
  if (inPlot && mouse) idx = Math.max(0, Math.min(n - 1, Math.round(ts.xToIndex(mouse.x))));
  const legendIdx = idx >= 0 ? idx : n - 1;

  if (idx >= 0 && mouse) {
    const snapX = ts.indexToX(idx);
    const pane = layout.paneAtY(mouse.y);

    ctx.strokeStyle = colors.crosshair;
    ctx.lineWidth = 1;
    ctx.setLineDash([4, 4]);
    ctx.beginPath();
    // vertical: full stacked height
    ctx.moveTo(align(snapX), 0);
    ctx.lineTo(align(snapX), contentH);
    // horizontal: only within the hovered pane
    ctx.moveTo(0, align(mouse.y));
    ctx.lineTo(plotW, align(mouse.y));
    ctx.stroke();
    ctx.setLineDash([]);

    // price label (right gutter) — uses the hovered pane's scale
    const price = pane.scale.yToPrice(mouse.y);
    const pLabel = price.toFixed(Math.abs(price) < 1 ? 4 : 2);
    ctx.font = font(11);
    const pw = ctx.measureText(pLabel).width + 10;
    ctx.fillStyle = colors.crosshairLabelBg;
    ctx.fillRect(plotW, mouse.y - 8, pw, 16);
    ctx.fillStyle = colors.legendText;
    ctx.textAlign = 'left';
    ctx.textBaseline = 'middle';
    ctx.fillText(pLabel, plotW + 5, mouse.y + 0.5);

    // time label (bottom axis)
    const tLabel = dtf(tz, data.interval() >= 86_400_000).format(new Date(data.time(idx)));
    const tw = ctx.measureText(tLabel).width + 12;
    ctx.fillStyle = colors.crosshairLabelBg;
    ctx.fillRect(snapX - tw / 2, contentH + 1, tw, 16);
    ctx.fillStyle = colors.legendText;
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    ctx.fillText(tLabel, snapX, contentH + 9);
  }

  // OHLC legend (top-left of the price pane)
  if (!showLegend) return;
  const o = data.open(legendIdx);
  const c = data.close(legendIdx);
  const up = c >= o;
  // change vs the previous bar's close (TradingView convention)
  const prev = legendIdx > 0 ? data.close(legendIdx - 1) : o;
  const chg = c - prev;
  const pct = prev !== 0 ? (chg / prev) * 100 : 0;
  const sign = chg >= 0 ? '+' : '';
  const seg: Array<[string, string]> = [
    ['O', o.toFixed(2)],
    ['H', data.high(legendIdx).toFixed(2)],
    ['L', data.low(legendIdx).toFixed(2)],
    ['C', c.toFixed(2)],
    ['', `${sign}${chg.toFixed(2)} (${sign}${pct.toFixed(2)}%)`],
    ['V', fmtVol(data.volume(legendIdx))],
  ];
  ctx.textAlign = 'left';
  ctx.textBaseline = 'alphabetic';
  let lx = 8;
  const ly = 14;
  if (legendTitle) {
    // symbol · timeframe in the accent, spaced like the app's small-caps labels
    ctx.font = font(11, 'ui', '600');
    ctx.fillStyle = colors.accent;
    const spaced = ctx as CanvasRenderingContext2D & { letterSpacing?: string };
    const prev = spaced.letterSpacing;
    if (prev !== undefined) spaced.letterSpacing = '0.08em';
    ctx.fillText(legendTitle.toUpperCase(), lx, ly);
    lx += ctx.measureText(legendTitle.toUpperCase()).width + 12;
    if (prev !== undefined) spaced.letterSpacing = prev;
  }
  ctx.font = font(11);
  const valColor = up ? colors.upBody : colors.downBody;
  const chgColor = chg >= 0 ? colors.upBody : colors.downBody;
  for (const [k, v] of seg) {
    if (k) {
      ctx.fillStyle = colors.axisText;
      ctx.fillText(k, lx, ly);
      lx += ctx.measureText(k).width + 3;
    }
    ctx.fillStyle = k === 'V' ? colors.axisText : k === '' ? chgColor : valColor;
    ctx.fillText(v, lx, ly);
    lx += ctx.measureText(v).width + 10;
  }
}
