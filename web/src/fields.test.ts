// The dildo / climax field hints (the column tooltips and the scan editor's
// help) must state the rules as the engine applies them (rv2-web-3):
//   - the climax volume test is INCLUSIVE: volume >= the biggest single prior
//     bar (climax.ts requireMaxVol, internal/climax), so a candle that ties it
//     counts - "more than any one of them" said the opposite;
//   - a dildo is judged against the UP TO priorBars (10) traded bars before it,
//     with at least minPriorBars (3): an early dildo with 3-9 priors is valid.
import { describe, expect, it } from 'vitest';
import { FIELD_BY_KEY } from './fields';
import { DEFAULT_CLIMAX_PARAMS } from './signals/climax';
import { DEFAULT_DILDO_PARAMS } from './signals/dildoPb';

describe('dildo / climax field hints state the rules as the engine applies them', () => {
  it('the climax volume test is inclusive: at least as much as any one prior bar, never "more than"', () => {
    expect(DEFAULT_CLIMAX_PARAMS.requireMaxVol).toBe(true);
    for (const key of ['climaxAge', 'climaxVolX']) {
      const hint = FIELD_BY_KEY[key]!.hint!;
      expect(hint, key).not.toMatch(/more than any one of them/);
      expect(hint, key).toMatch(/at least as much as any one of them/);
    }
  });

  it('the dildo priors are the up to 10 (at least 3) traded bars before the candle, in every hint that counts them', () => {
    const { priorBars, minPriorBars } = DEFAULT_DILDO_PARAMS;
    for (const key of ['dildoAge', 'dildoVolX']) {
      const hint = FIELD_BY_KEY[key]!.hint!;
      expect(hint, key).toContain(`up to ${priorBars}`);
      expect(hint, key).not.toMatch(new RegExp(`of the ${priorBars} traded`));
    }
    expect(FIELD_BY_KEY.dildoAge!.hint).toContain(`at least ${minPriorBars}`);
  });
});
