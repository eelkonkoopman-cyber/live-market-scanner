// Auto-mode state in the store: settings, pins, hand-assign auto-pin, Auto's own swaps, persistence shape.
import { beforeEach, describe, expect, it } from 'vitest';
import { useStore } from './store';

const reset = () => {
  useStore.getState().applyGridFromServer({ rows: 2, cols: 2, cells: [] });
  useStore.setState({ gridRev: 0 });
};

describe('charts Auto mode store', () => {
  beforeEach(reset);

  it('setAutoFill / toggleCellPin are local edits that bump gridRev', () => {
    const s = useStore.getState();
    s.setAutoFill({ on: true });
    expect(useStore.getState().chartGrid.auto).toEqual({ on: true, scanId: null, metric: 'chgDay' });
    expect(useStore.getState().gridRev).toBe(1);
    s.toggleCellPin(1);
    expect(useStore.getState().chartGrid.cells[1].pinned).toBe(true);
    s.toggleCellPin(1);
    expect(useStore.getState().chartGrid.cells[1].pinned).toBeUndefined();
    expect(useStore.getState().gridRev).toBe(3);
  });

  it('a hand assignment pins the cell only while Auto is on', () => {
    const s = useStore.getState();
    s.assignCell(0, 'AAA');
    expect(useStore.getState().chartGrid.cells[0].pinned).toBeUndefined();
    s.setAutoFill({ on: true });
    s.assignCell(1, 'bbb');
    expect(useStore.getState().chartGrid.cells[1]).toMatchObject({ sym: 'BBB', pinned: true });
  });

  it("Auto's own swaps keep tf, never pin, skip pinned cells, and bump gridRev", () => {
    const s = useStore.getState();
    s.setAutoFill({ on: true });
    s.setCellTf(0, '5m');
    s.toggleCellPin(2);
    const rev = useStore.getState().gridRev;
    s.applyAutoCells(['X', 'Y', 'Z', null], [0, 2]);
    const cells = useStore.getState().chartGrid.cells;
    expect(cells[0]).toMatchObject({ sym: 'X', tf: '5m' });
    expect(cells[0].pinned).toBeUndefined();
    expect(cells[2].sym).toBeNull(); // pinned: untouched
    expect(useStore.getState().gridRev).toBe(rev + 1);
  });

  it('setGridPreset keeps auto settings and per-cell pins for surviving cells', () => {
    const s = useStore.getState();
    s.setAutoFill({ on: true, metric: 'chg5m' });
    s.toggleCellPin(0);
    s.setGridPreset(2, 3);
    const g = useStore.getState().chartGrid;
    expect(g.auto).toEqual({ on: true, scanId: null, metric: 'chg5m' });
    expect(g.cells).toHaveLength(6);
    expect(g.cells[0].pinned).toBe(true);
    expect(g.cells[5].pinned).toBeUndefined();
  });

  it('normalizes foreign auto/pinned values from the server', () => {
    useStore.getState().applyGridFromServer({
      rows: 1,
      cols: 2,
      cells: [{ sym: 'a', tf: '1m', pinned: 'yes' }, { sym: 'b', tf: '5m', pinned: true }],
      auto: { on: 'true', scanId: 7, metric: 'bogus' },
    });
    const g = useStore.getState().chartGrid;
    expect(g.cells[0].pinned).toBeUndefined();
    expect(g.cells[1].pinned).toBe(true);
    expect(g.auto).toEqual({ on: false, scanId: null, metric: 'chgDay' });
    expect(useStore.getState().gridRev).toBe(0); // adopting a remote layout never bumps
  });
});
