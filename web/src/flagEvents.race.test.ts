// RV2-UI-1: a row recorded while flagevents.get is in flight reaches the
// client as a flagevents.add BEFORE the data frame that lacks it. The store
// must keep it: an add before the first data frame is merged when that frame
// lands, and a data frame never drops a row of its day an add already brought.
import { beforeEach, describe, expect, it } from 'vitest';
import { useStore } from './store';
import type { FlagEvent } from './types';

const ev = (sym: string, ts: number, kind: FlagEvent['kind'] = 'dildo'): FlagEvent => ({ ts, at: ts, sym, kind, label: kind, price: 1 });
const keys = () => useStore.getState().flagEvents?.events.map((e) => `${e.sym}|${e.ts}|${e.kind}`);

describe('flagEvents store: add racing the data frame', () => {
  beforeEach(() => useStore.setState({ flagEvents: null, flagEventAdds: null }));

  it('first load: an add before the data frame is kept', () => {
    const s = useStore.getState();
    s.addFlagEvents({ date: '2026-10-08', events: [ev('SBFM', 2)] });
    expect(useStore.getState().flagEvents).toBeNull();
    s.setFlagEvents({ date: '2026-10-08', dates: ['2026-10-08'], events: [ev('AAA', 1)] });
    expect(keys()).toEqual(['AAA|1|dildo', 'SBFM|2|dildo']);
  });

  it('reconnect: the snapshot does not wipe a row an add brought', () => {
    const s = useStore.getState();
    s.setFlagEvents({ date: '2026-10-08', dates: ['2026-10-08'], events: [ev('AAA', 1)] });
    s.addFlagEvents({ date: '2026-10-08', events: [ev('SBFM', 2)] });
    s.setFlagEvents({ date: '2026-10-08', dates: ['2026-10-08'], events: [ev('AAA', 1)] });
    expect(keys()).toEqual(['AAA|1|dildo', 'SBFM|2|dildo']);
  });

  it('stepping back to today keeps an add that came while a past day was shown', () => {
    const s = useStore.getState();
    s.setFlagEvents({ date: '2026-10-07', dates: ['2026-10-08', '2026-10-07'], events: [ev('OLD', 1)] });
    s.addFlagEvents({ date: '2026-10-08', events: [ev('SBFM', 2)] });
    s.setFlagEvents({ date: '2026-10-08', dates: ['2026-10-08', '2026-10-07'], events: [] });
    expect(keys()).toEqual(['SBFM|2|dildo']);
  });

  it('another day is never mixed in, and nothing is duplicated', () => {
    const s = useStore.getState();
    s.addFlagEvents({ date: '2026-10-08', events: [ev('SBFM', 2)] });
    s.setFlagEvents({ date: '2026-10-07', dates: ['2026-10-08', '2026-10-07'], events: [ev('OLD', 1)] });
    expect(keys()).toEqual(['OLD|1|dildo']);
    s.setFlagEvents({ date: '2026-10-08', dates: ['2026-10-08'], events: [ev('SBFM', 2)] });
    expect(keys()).toEqual(['SBFM|2|dildo']);
  });
});
