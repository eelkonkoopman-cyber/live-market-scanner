// Regression coverage for the morning-review feature's named bug class:
// "wrong-baseline gain". The after-hours gain must be derived vs the SAME
// DAY's regular-session close — never the prior day's official close
// (prevClose, that's the day change) and never the post session's own first
// print. deriveReviewGainPct (store.ts) is the ONE place this is computed;
// this file pins it down with numbers where all three candidate baselines
// disagree, so a regression that swaps in the wrong baseline fails loudly.
import { describe, expect, it } from 'vitest';
import { deriveReviewGainPct, useStore } from './store';
import type { ReviewRow } from './types';

const row = (patch: Partial<ReviewRow>): ReviewRow => ({
  sym: 'AAA',
  hi: 0,
  lo: 0,
  close: 0,
  vwap: 0,
  vol: 0,
  prevClose: 0,
  ...patch,
});

describe('deriveReviewGainPct', () => {
  it('post session: gain is vs the SAME DAY rth close, not prevClose or the post session first print', () => {
    // prevClose 10 (day change baseline), rth close 12 (the CORRECT AH
    // baseline), post's own first print 12.5 (a wrong-but-plausible
    // baseline), post close 13.
    const r = row({ close: 13, prevClose: 10 });
    const rthCloseBySym = { AAA: 12 };
    const got = deriveReviewGainPct(r, 'post', rthCloseBySym);
    expect(got).not.toBeNull();
    // Correct: (13 - 12) / 12 * 100
    expect(got!).toBeCloseTo(((13 - 12) / 12) * 100, 6);
    // Provably NOT the other two baselines a regression could swap in.
    expect(got!).not.toBeCloseTo(((13 - 10) / 10) * 100, 3); // vs prevClose: +30%
    expect(got!).not.toBeCloseTo(((13 - 12.5) / 12.5) * 100, 3); // vs post's own first print: +4%
  });

  it('post session: missing rth baseline renders null, never 0 or Infinity', () => {
    const r = row({ close: 13, prevClose: 10 });
    expect(deriveReviewGainPct(r, 'post', {})).toBeNull();
    expect(deriveReviewGainPct(r, 'post', { AAA: 0 })).toBeNull(); // 0 = "unknown" per the raw-row convention, not a real close
  });

  it('regular session: gain is vs prevClose (the ordinary day change)', () => {
    const r = row({ close: 12, prevClose: 10 });
    const got = deriveReviewGainPct(r, 'regular', {});
    expect(got).toBeCloseTo(20, 6); // (12-10)/10 * 100
  });

  it('pre session: gain is vs prevClose (the prior day\'s regular close)', () => {
    const r = row({ close: 11, prevClose: 10 });
    const got = deriveReviewGainPct(r, 'pre', {});
    expect(got).toBeCloseTo(10, 6);
  });

  it('regular/pre: missing prevClose renders null, never 0 or Infinity', () => {
    const r = row({ close: 12, prevClose: 0 });
    expect(deriveReviewGainPct(r, 'regular', {})).toBeNull();
    expect(deriveReviewGainPct(r, 'pre', {})).toBeNull();
  });
});

// Drives the SAME bug-class-1 scenario through the real store action
// (setReviewData), the way the review pane actually populates
// rthCloseBySym: a 'regular' reply caches every symbol's rth close, then a
// 'post' reply's rows are read against that cache — not a value handed to
// the derivation directly.
describe('setReviewData -> deriveReviewGainPct (store action, not just the pure fn)', () => {
  it('caches the rth close from a regular reply and a later post reply\'s gain uses it', () => {
    useStore.setState({
      review: { dates: [], date: null, session: 'post', rows: [], loading: false, rthCloseBySym: {}, rthCloseDate: null },
    });

    // The regular-session snapshot for the day (rth close 12), landing
    // BEFORE the post reply — exactly ws.ts's getReview fan-out order.
    useStore.getState().setReviewData('2026-08-24', 'regular', ['2026-08-24'], [row({ sym: 'AAA', close: 12, prevClose: 10 })]);

    const cached = useStore.getState().review.rthCloseBySym;
    expect(cached.AAA).toBe(12);

    // The post-session snapshot itself; session is already 'post' by default.
    useStore.getState().setReviewData('2026-08-24', 'post', ['2026-08-24'], [row({ sym: 'AAA', close: 13, prevClose: 10 })]);

    const st = useStore.getState().review;
    expect(st.rows).toHaveLength(1);
    const gain = deriveReviewGainPct(st.rows[0], st.session, st.rthCloseBySym);
    expect(gain).toBeCloseTo(((13 - 12) / 12) * 100, 6); // +8.33%, NOT +30% (vs prevClose 10)
  });
});
