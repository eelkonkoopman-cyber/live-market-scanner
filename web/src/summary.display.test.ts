// Regression coverage for the news-summary button/tag decision (the frontend
// half of "keep Claude as opt-in for news"): a LOCAL auto-summary must never
// make the Claude upgrade unreachable, and a genuine Claude summary must
// never be mistaken for a local one (or vice versa). summaryDisplay (store.ts)
// is the ONE place this is decided, from the raw SummaryState alone — this
// file pins down all three cases named in the design.
import { describe, expect, it } from 'vitest';
import { summaryDisplay } from './store';
import type { SummaryState } from './types';

describe('summaryDisplay', () => {
  it('no summary yet: shows the plain Summarize button, no tag', () => {
    expect(summaryDisplay(undefined)).toEqual({ buttonLabel: '✨ Summarize', tag: null });
  });

  it('local summary present: relabels to Ask Claude and tags LOCAL — the button must stay reachable', () => {
    const sum: SummaryState = { loading: false, text: 'Company X raised $5M in a registered direct offering.', model: 'local' };
    expect(summaryDisplay(sum)).toEqual({ buttonLabel: '✨ Ask Claude', tag: 'LOCAL' });
  });

  it('real Claude summary present: hides the button and tags CLAUDE', () => {
    const sum: SummaryState = { loading: false, text: 'Claude says the raise funds an already-announced expansion.', model: 'claude-3-5-sonnet-20241022' };
    expect(summaryDisplay(sum)).toEqual({ buttonLabel: null, tag: 'CLAUDE' });
  });

  it('a pending/errored request with no text yet is treated as "no summary" (button stays, no tag)', () => {
    const sum: SummaryState = { loading: false, err: 'timed out' };
    expect(summaryDisplay(sum)).toEqual({ buttonLabel: '✨ Summarize', tag: null });
  });
});
