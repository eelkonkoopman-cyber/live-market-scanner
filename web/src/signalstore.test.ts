// Regression coverage for the Signal window's store wiring (store.ts §3, as
// updated by addendum 2): it has no SERVER-persisted grid of its own, so
// setSymChartData's guard — originally "the symbol is in a grid cell" only —
// must also accept any symbol currently assigned somewhere in the Signal
// window's own grid (signalSyms), the gridless second consumer of the same
// per-symbol bar cache the charts monitor writes into. signalSyms replaces
// the single-symbol signalSym now that the window holds a grid of cells
// instead of one chart.
import { describe, expect, it } from 'vitest';
import { useStore } from './store';

describe('setSignalSyms', () => {
  it('normalises case and trims whitespace', () => {
    useStore.getState().setSignalSyms(['  atyr  ']);
    expect(useStore.getState().signalSyms).toEqual(['ATYR']);
  });

  it('dedupes, case-insensitively, keeping first-appearance order', () => {
    useStore.getState().setSignalSyms(['gctk', 'ATYR', 'GCTK', ' atyr ']);
    expect(useStore.getState().signalSyms).toEqual(['GCTK', 'ATYR']);
  });

  it('empty array clears it', () => {
    useStore.getState().setSignalSyms(['GBTC']);
    useStore.getState().setSignalSyms([]);
    expect(useStore.getState().signalSyms).toEqual([]);
  });
});

describe('setSymChartData guard', () => {
  const bar = { t: 1, o: 1, h: 1, l: 1, c: 1, v: 1 };

  it('stores chart data for a signal-grid symbol even though it is in no chart-monitor cell', () => {
    useStore.getState().setSignalSyms(['ATYR']);
    useStore.getState().setSymChartData('ATYR', [bar], 0.5);
    expect(useStore.getState().barsBySym['ATYR']).toEqual([bar]);
  });

  it('drops chart data for a symbol that is neither gridded nor in the signal grid', () => {
    useStore.getState().setSignalSyms(['ATYR']);
    useStore.getState().setSymChartData('ZZZZ', [bar], 0.5);
    expect(useStore.getState().barsBySym['ZZZZ']).toBeUndefined();
  });

  it('still stores chart data for a symbol assigned to a grid cell, with an empty signal grid', () => {
    useStore.getState().setSignalSyms([]);
    useStore.getState().assignCell(0, 'NVDA');
    useStore.getState().setSymChartData('NVDA', [bar], 0.5);
    expect(useStore.getState().barsBySym['NVDA']).toEqual([bar]);
  });
});
