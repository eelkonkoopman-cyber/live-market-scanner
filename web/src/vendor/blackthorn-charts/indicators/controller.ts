//! Pane-aware indicator controller (BUILD-PLAN §3.6).
//
// Given the live `IndicatorInstance[]` + candles, it computes each indicator
// (with a cache keyed by params+dataVersion so pan/zoom never recompute), turns
// the result into `PaneSeries`, and assigns them to panes: overlays → the price
// pane; each oscillator → its own pane (created on first appearance, its pane
// removed when the instance is gone). This replaces the app's separate-chart
// `OscillatorPane` + `paneSync` workaround.

import { PaneLayout } from '../core/panes';
import { COMPUTE, getIndicator, type ComputeResult, type IndicatorInstance } from './registry';
import type { Candle, ChartColors, PaneSeries, PriceLine } from '../core/types';

type CacheEntry = { key: string; result: ComputeResult };

/** One legend row: a series or level of an indicator at a bar. */
export type IndicatorValue = { key: string; label: string; value: number | null; color: string };

export class IndicatorController {
  private cache = new Map<string, CacheEntry>();

  constructor(private layout: PaneLayout) {}

  /** Reconcile instances → pane series. Returns true if the pane set changed
   *  (a pane was added/removed → caller should recompute layout rects). */
  sync(instances: IndicatorInstance[], candles: Candle[], dataVersion: number, colors: ChartColors): boolean {
    const price = this.layout.pricePane;
    price.series = [];
    price.priceLines = [];

    const wantOsc = new Set<string>();
    const oscOrder: string[] = [];
    let structureChanged = false;

    for (const inst of instances) {
      const def = getIndicator(inst.indicatorId);
      if (!def) continue;
      const result = this.computeCached(inst, candles, dataVersion);
      const { series: seriesList, levels } = this.toSeries(inst, result, colors);

      if (def.category === 'overlay') {
        if (inst.visible) {
          price.series.push(...seriesList);
          price.priceLines.push(...levels);
        }
      } else {
        oscOrder.push(inst.instanceId);
        wantOsc.add(inst.instanceId);
        let pane = this.layout.getPane(inst.instanceId);
        if (!pane) {
          pane = this.layout.addPane(inst.instanceId, {
            ...(def.range ? { range: def.range } : {}),
            guides: def.guideLevels ?? [],
            label: def.name,
          });
          structureChanged = true;
        } else {
          // keep guides/range in sync with the (possibly edited) def
          pane.guides = def.guideLevels ?? [];
          if (def.range) pane.range = def.range;
          else delete pane.range;
        }
        pane.series = inst.visible ? seriesList : [];
      }
    }

    // drop oscillator panes whose instance is gone
    for (const p of this.layout.oscillators) {
      if (!wantOsc.has(p.id)) {
        this.layout.removePane(p.id);
        this.cache.delete(p.id);
        structureChanged = true;
      }
    }
    // panes follow the instance order (the app reorders by moving instances)
    const before = this.layout.oscillators.map((p) => p.id).join('|');
    this.layout.setOrder(oscOrder);
    if (this.layout.oscillators.map((p) => p.id).join('|') !== before) structureChanged = true;
    return structureChanged;
  }

  /** Legend values for an instance at bar `idx` (the last computed data). */
  valuesAt(inst: IndicatorInstance, idx: number): IndicatorValue[] {
    const def = getIndicator(inst.indicatorId);
    const hit = this.cache.get(inst.instanceId);
    if (!def || !hit) return [];
    const out: IndicatorValue[] = [];
    for (const spec of def.render) {
      const style = { ...spec.style, ...(inst.styleOverrides[spec.key] ?? {}) };
      let value: number | null = null;
      if (spec.seriesType === 'level') value = hit.result.levels?.[spec.key] ?? null;
      else {
        const arr = hit.result.lines[spec.key] ?? hit.result.histograms?.[spec.key];
        value = arr && idx >= 0 && idx < arr.length ? (arr[idx] ?? null) : null;
      }
      out.push({ key: spec.key, label: spec.label ?? spec.key, value, color: style.color ?? '#9ca3af' });
    }
    return out;
  }

  private computeCached(inst: IndicatorInstance, candles: Candle[], dataVersion: number): CacheEntry['result'] {
    const key = `${dataVersion}|${inst.indicatorId}|${JSON.stringify(inst.params)}`;
    const hit = this.cache.get(inst.instanceId);
    if (hit && hit.key === key) return hit.result;
    const fn = COMPUTE[getIndicator(inst.indicatorId)!.compute];
    const result = fn(candles, inst.params);
    this.cache.set(inst.instanceId, { key, result });
    return result;
  }

  private toSeries(inst: IndicatorInstance, result: ComputeResult, colors: ChartColors): { series: PaneSeries[]; levels: PriceLine[] } {
    const def = getIndicator(inst.indicatorId)!;
    const out: PaneSeries[] = [];
    const levels: PriceLine[] = [];
    for (const spec of def.render) {
      const style = { ...spec.style, ...(inst.styleOverrides[spec.key] ?? {}) };
      if (spec.seriesType === 'level') {
        const price = result.levels?.[spec.key];
        if (price == null || !Number.isFinite(price)) continue;
        levels.push({
          price,
          label: spec.label ?? spec.key,
          color: style.color ?? colors.axisText,
          lineStyle: style.lineStyle ?? 'dashed',
          lineWidth: style.lineWidth ?? 1,
        });
      } else if (spec.seriesType === 'line') {
        const values = result.lines[spec.key];
        if (!values) continue;
        out.push({
          kind: 'line',
          values,
          color: style.color ?? colors.legendText,
          lineWidth: style.lineWidth ?? 1,
          lineStyle: style.lineStyle ?? 'solid',
          ...(spec.scale === 'volume' ? { scale: 'volume' as const } : {}),
        });
      } else {
        const values = result.histograms?.[spec.key] ?? result.lines[spec.key];
        if (!values) continue;
        // sign-driven coloring, themed (the app hardcoded #22c55e90/#ef444490)
        const up = colors.volumeUp;
        const down = colors.volumeDown;
        const perBar = values.map((v) => (v == null ? null : v >= 0 ? up : down));
        out.push({ kind: 'histogram', values, colors: perBar, color: up });
      }
    }
    return { series: out, levels };
  }

  destroy(): void {
    this.cache.clear();
  }
}
