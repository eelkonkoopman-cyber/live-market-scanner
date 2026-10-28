// The flag-event history slice of the store: replace on data, merge on add.
import { beforeEach, describe, expect, it } from 'vitest';
import { useStore } from './store';
import type { FlagEvent } from './types';

const ev = (sym: string, ts: number, kind: FlagEvent['kind'] = 'entry'): FlagEvent => ({ ts, at: ts, sym, kind, label: kind, price: 2 });

describe('flagEvents store', () => {
  beforeEach(() => useStore.setState({ flagEvents: null }));

  it('starts null and ignores adds until the first data frame', () => {
    expect(useStore.getState().flagEvents).toBeNull();
    useStore.getState().addFlagEvents({ date: '2026-10-06', events: [ev('AAA', 1)] });
    expect(useStore.getState().flagEvents).toBeNull();
  });

  it('setFlagEvents replaces everything', () => {
    const s = useStore.getState();
    s.setFlagEvents({ date: '2026-10-06', dates: ['2026-10-06', '2026-10-05'], events: [ev('AAA', 1)] });
    s.setFlagEvents({ date: '2026-10-05', dates: ['2026-10-06', '2026-10-05'], events: [ev('BBB', 2)] });
    expect(useStore.getState().flagEvents).toEqual({ date: '2026-10-05', dates: ['2026-10-06', '2026-10-05'], events: [ev('BBB', 2)] });
  });

  it('addFlagEvents appends new events of the viewed day, deduped by sym|ts|kind', () => {
    const s = useStore.getState();
    s.setFlagEvents({ date: '2026-10-06', dates: ['2026-10-06'], events: [ev('AAA', 1)] });
    s.addFlagEvents({ date: '2026-10-06', events: [ev('AAA', 1), ev('AAA', 1, 'exit'), ev('BBB', 2)] });
    expect(useStore.getState().flagEvents?.events.map((e) => `${e.sym}${e.ts}${e.kind}`)).toEqual(['AAA1entry', 'AAA1exit', 'BBB2entry']);
  });

  it('merges the date but not the events when another day is viewed', () => {
    const s = useStore.getState();
    s.setFlagEvents({ date: '2026-10-05', dates: ['2026-10-05'], events: [ev('OLD', 1)] });
    s.addFlagEvents({ date: '2026-10-06', events: [ev('NEW', 2)], dates: ['2026-10-06', '2026-10-05'] });
    const f = useStore.getState().flagEvents;
    expect(f?.date).toBe('2026-10-05');
    expect(f?.dates).toEqual(['2026-10-06', '2026-10-05']);
    expect(f?.events.map((e) => e.sym)).toEqual(['OLD']);
  });

  it('keeps dates newest first and deduped', () => {
    const s = useStore.getState();
    s.setFlagEvents({ date: '2026-10-06', dates: ['2026-10-06', '2026-10-05'], events: [] });
    s.addFlagEvents({ date: '2026-10-07', events: [] });
    s.addFlagEvents({ date: '2026-10-07', events: [] });
    expect(useStore.getState().flagEvents?.dates).toEqual(['2026-10-07', '2026-10-06', '2026-10-05']);
  });
});
