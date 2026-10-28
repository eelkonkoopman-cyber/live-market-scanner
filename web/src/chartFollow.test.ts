import { beforeEach, describe, expect, it } from 'vitest';
import { applyScanFollow, followedScans, newEntrants } from './chartFollow';
import { autoStep, emptyMemo } from './autoFill';
import { applyFollow, emitScanAlert, subscribeScanAlerts } from './signalFollow';
import { useStore } from './store';

const cell = (o: Record<string, unknown> = {}) => ({ sym: 'AAA', tf: '1m' as const, ...o });

describe('chart follow scan', () => {
  beforeEach(() => {
    useStore.setState({
      chartGrid: { rows: 1, cols: 3, cells: [cell({ followScan: 'pm' }), cell({ followScan: 'hod' }), cell()] },
      gridRev: 0,
    });
  });

  it('alert switches only cells following that scan', () => {
    useStore.getState().followCell('pm', 'zzz');
    const c = useStore.getState().chartGrid.cells;
    expect(c.map((x) => x.sym)).toEqual(['ZZZ', 'AAA', 'AAA']);
    expect(c[0].followScan).toBe('pm');
  });

  it('scan alert channel is independent of the Signal follow gating', () => {
    const got: string[] = [];
    const off = subscribeScanAlerts((a) => got.push(a.sym));
    emitScanAlert({ scanId: 'pm', sym: 'X' });
    off();
    expect(got).toEqual(['X']);
  });

  it('entrant follow: newest rows only, baseline ignored', () => {
    expect(newEntrants(undefined, ['A', 'B'])).toEqual([]);
    expect(newEntrants(['A', 'B'], ['C', 'A', 'B'])).toEqual(['C']);
    const scans = [{ id: 'pm', alert: { enabled: true } }, { id: 'hod', alert: { enabled: false } }];
    expect(followedScans(useStore.getState().chartGrid.cells, scans)).toEqual({ alertIds: ['pm'], entrantIds: ['hod'] });
  });

  it('applyScanFollow is identity when nothing moves', () => {
    const cells = [cell({ followScan: 'pm', sym: 'ZZZ' })];
    expect(applyScanFollow(cells, 'pm', 'zzz')).toBe(cells);
  });

  it('Auto skips followed cells', () => {
    const cells = useStore.getState().chartGrid.cells;
    const r = autoStep({
      cells: [null, null, null],
      pinned: cells.map((c) => !!c.pinned || !!c.followScan),
      ranking: ['R1', 'R2'],
      now: 1e6,
      held: false,
      initial: true,
      memo: emptyMemo(),
    });
    expect(r.cells[0]).toBeNull();
    expect(r.cells[1]).toBeNull();
    useStore.getState().applyAutoCells(['R1', 'R2', 'R3'], [0, 1, 2]);
    expect(useStore.getState().chartGrid.cells.map((c) => c.sym)).toEqual(['AAA', 'AAA', 'R3']);
  });

  it('manual symbol or pin turns follow off', () => {
    useStore.getState().assignCell(0, 'QQQ');
    expect(useStore.getState().chartGrid.cells[0].followScan).toBeUndefined();
    useStore.getState().toggleCellPin(1);
    const c1 = useStore.getState().chartGrid.cells[1];
    expect(c1.followScan).toBeUndefined();
    expect(c1.pinned).toBe(true);
  });

  it('persists round-trip; old layouts load Off', () => {
    const blob = JSON.parse(JSON.stringify(useStore.getState().chartGrid));
    useStore.getState().applyGridFromServer({ rows: 1, cols: 2, cells: [{ sym: 'A', tf: '1m' }, { sym: 'B', tf: '5m' }] });
    expect(useStore.getState().chartGrid.cells.every((c) => !c.followScan)).toBe(true);
    useStore.getState().applyGridFromServer(blob);
    expect(useStore.getState().chartGrid.cells.map((c) => c.followScan)).toEqual(['pm', 'hod', undefined]);
    useStore.getState().setCellFollow(2, 'x');
    useStore.getState().setCellFollow(2, null);
    expect(useStore.getState().chartGrid.cells[2].followScan).toBeUndefined();
  });

  it('Signal window follow unchanged', () => {
    const r = applyFollow([{ sym: 'AAA', setup: 'pullback' as const, follow: 'dildo' as const }], 'dildo-1m', 'BBB');
    expect(r[0].sym).toBe('BBB');
  });
});
