import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { DEFAULT_DILDO_PARAMS } from './signals/dildoPb';
import {
  DILDO_PARAMS_KEY,
  DILDO_SEND_DEBOUNCE_MS,
  applyServerDildoParams,
  getSignalRuleParams,
  registerDildoParamsSender,
  resetDildoParams,
  setDildoParams,
} from './signalLayout';

// The green dildo tuning is server-owned: load from dildo.params.data, edits
// are sent debounced, Reset sends the defaults, old localStorage is ignored.
describe('server-owned green dildo params', () => {
  const sent: unknown[] = [];
  beforeEach(() => {
    vi.useFakeTimers();
    sent.length = 0;
    registerDildoParamsSender((p) => sent.push(p));
  });
  afterEach(() => {
    vi.runAllTimers();
    applyServerDildoParams(DEFAULT_DILDO_PARAMS);
    registerDildoParamsSender(null);
    vi.useRealTimers();
  });

  it('loads (normalised) from the server broadcast and ignores the old localStorage value', () => {
    try { localStorage.setItem(DILDO_PARAMS_KEY, JSON.stringify({ minBodyPct: 99 })); } catch { /* none */ }
    expect(getSignalRuleParams().dildo).toEqual(DEFAULT_DILDO_PARAMS);
    applyServerDildoParams({ minBodyPct: 12, floor: 'open', minVolX: -1, junk: 1 });
    expect(getSignalRuleParams().dildo).toEqual({ ...DEFAULT_DILDO_PARAMS, minBodyPct: 12, floor: 'open' });
  });

  it('edits apply at once and send ONE set after the debounce', () => {
    setDildoParams({ ...DEFAULT_DILDO_PARAMS, minBodyPct: 9 });
    setDildoParams({ ...DEFAULT_DILDO_PARAMS, minBodyPct: 10 });
    expect(getSignalRuleParams().dildo.minBodyPct).toBe(10);
    expect(sent).toHaveLength(0);
    vi.advanceTimersByTime(DILDO_SEND_DEBOUNCE_MS - 1);
    expect(sent).toHaveLength(0);
    vi.advanceTimersByTime(1);
    expect(sent).toEqual([{ ...DEFAULT_DILDO_PARAMS, minBodyPct: 10 }]);
  });

  it('a broadcast does not clobber an edit still waiting to be sent', () => {
    setDildoParams({ ...DEFAULT_DILDO_PARAMS, minBodyPct: 10 });
    applyServerDildoParams(DEFAULT_DILDO_PARAMS);
    expect(getSignalRuleParams().dildo.minBodyPct).toBe(10);
    vi.advanceTimersByTime(DILDO_SEND_DEBOUNCE_MS);
    applyServerDildoParams({ ...DEFAULT_DILDO_PARAMS, minBodyPct: 10 }); // the echo
    expect(getSignalRuleParams().dildo.minBodyPct).toBe(10);
  });

  it('Reset to defaults sends the defaults', () => {
    applyServerDildoParams({ minBodyPct: 12 });
    resetDildoParams();
    expect(getSignalRuleParams().dildo).toEqual(DEFAULT_DILDO_PARAMS);
    vi.advanceTimersByTime(DILDO_SEND_DEBOUNCE_MS);
    expect(sent).toEqual([DEFAULT_DILDO_PARAMS]);
  });
});
