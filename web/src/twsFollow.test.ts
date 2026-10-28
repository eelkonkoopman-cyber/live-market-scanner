import { describe, expect, it } from 'vitest';
import { parseTwsFollow, twsFollowView } from './twsFollow';
import { useStore } from './store';

describe('TWS follow switch', () => {
  it('shows lit when on and dim when off, with a tooltip', () => {
    expect(twsFollowView(true)).toMatchObject({ on: true, label: 'TWS ●' });
    expect(twsFollowView(false)).toMatchObject({ on: false, label: 'TWS ○' });
    expect(twsFollowView(true).title).toContain('ON');
    expect(twsFollowView(false).title).toContain('OFF');
  });
  it('parses the server frame; anything but enabled:false is on', () => {
    expect(parseTwsFollow({ enabled: false })).toBe(false);
    expect(parseTwsFollow({ enabled: true })).toBe(true);
    expect(parseTwsFollow(undefined)).toBe(true);
  });
  it('store defaults to on and holds the server state', () => {
    expect(useStore.getState().twsFollow).toBe(true);
    useStore.getState().setTwsFollow(false);
    expect(useStore.getState().twsFollow).toBe(false);
    useStore.getState().setTwsFollow(true);
  });
});
