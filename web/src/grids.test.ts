// applyTicks folds a batch of scan.tick frames into ONE store update and skips
// no-op deltas (no row bodies, same order) so a window viewing six tiled scans
// renders once per engine snapshot — and not at all when nothing moved.
import { beforeEach, describe, expect, it } from 'vitest';
import { useStore } from './store';
import type { TickFrame } from './types';

function frame(id: string, seq: number, order: string[], rows: TickFrame['rows'] = {}, full = false): TickFrame {
  return { id, seq, asOf: seq * 100, full, order, rows };
}

describe('applyTicks', () => {
  beforeEach(() => {
    useStore.setState({ grids: {} });
  });

  it('a full frame seeds the grid', () => {
    useStore.getState().applyTicks([frame('s1', 1, ['A', 'B'], { A: { last: 1 }, B: { last: 2 } }, true)]);
    const g = useStore.getState().grids.s1;
    expect(g.order).toEqual(['A', 'B']);
    expect(g.rows.A).toEqual({ last: 1 });
  });

  it('a delta with no rows and the same order leaves the grid object untouched', () => {
    useStore.getState().applyTicks([frame('s1', 1, ['A', 'B'], { A: { last: 1 }, B: { last: 2 } }, true)]);
    const before = useStore.getState().grids;
    let notified = 0;
    const unsub = useStore.subscribe(() => notified++);
    useStore.getState().applyTicks([frame('s1', 2, ['A', 'B'])]);
    unsub();
    expect(useStore.getState().grids).toBe(before);
    expect(useStore.getState().grids.s1).toBe(before.s1);
    expect(notified).toBe(0);
  });

  it('a reorder with no row bodies is a change', () => {
    useStore.getState().applyTicks([frame('s1', 1, ['A', 'B'], { A: { last: 1 }, B: { last: 2 } }, true)]);
    useStore.getState().applyTicks([frame('s1', 2, ['B', 'A'])]);
    expect(useStore.getState().grids.s1.order).toEqual(['B', 'A']);
  });

  it('a batch across scans is one notification, and deltas merge per row', () => {
    useStore.getState().applyTicks([
      frame('s1', 1, ['A'], { A: { last: 1, vol: 10 } }, true),
      frame('s2', 1, ['Z'], { Z: { last: 9 } }, true),
    ]);
    const rowAbefore = useStore.getState().grids.s1.rows.A;
    let notified = 0;
    const unsub = useStore.subscribe(() => notified++);
    useStore.getState().applyTicks([
      frame('s1', 2, ['A'], { A: { last: 2 } }),
      frame('s2', 2, ['Z'], { Z: { last: 8 } }),
      frame('s2', 3, ['Z', 'Y'], { Y: { last: 1 } }),
    ]);
    unsub();
    expect(notified).toBe(1);
    const st = useStore.getState();
    expect(st.grids.s1.rows.A).toEqual({ last: 2, vol: 10 }); // merged, not replaced
    expect(st.grids.s1.rows.A).not.toBe(rowAbefore);
    expect(st.grids.s2.order).toEqual(['Z', 'Y']);
    expect(st.grids.s2.rows.Z).toEqual({ last: 8 });
    expect(st.grids.s2.rows.Y).toEqual({ last: 1 });
  });

  it('a symbol that left the order is dropped; a later full frame replaces everything', () => {
    useStore.getState().applyTicks([frame('s1', 1, ['A', 'B'], { A: { last: 1 }, B: { last: 2 } }, true)]);
    useStore.getState().applyTicks([frame('s1', 2, ['A'])]);
    expect(useStore.getState().grids.s1.rows.B).toBeUndefined();
    useStore.getState().applyTicks([frame('s1', 3, ['C'], { C: { last: 3 } }, true)]);
    expect(useStore.getState().grids.s1.rows).toEqual({ C: { last: 3 } });
  });

  it('applyTick is the single-frame form of applyTicks', () => {
    useStore.getState().applyTick(frame('s1', 1, ['A'], { A: { last: 1 } }, true));
    expect(useStore.getState().grids.s1.rows.A).toEqual({ last: 1 });
  });
});
