import { describe, expect, it, vi, afterEach } from 'vitest';
import { vwapxSizing, vwapxSizingLine, accountBadge, act, availLabel, connLabel, fmtPx, fmtUsd, marginView, parseShares, activePreset, presetBody, shortTile, fmtCompact, statTiles, type OTPreviewSide, breakeven, breakevenBody, dropCancel, dropCancelBody, followText, parseFollowSec, parseRiskChoice, riskFor, riskPctFor, riskView, pctRiskUsd, baseRiskUsd, riskLabel, powerRiskChoice, sizingLine, emaPill, emaAge, emaUsable, fmtMoney, parseStopOn, parseStopPct, actionState, stopText, stopToggleable, parseTpChoice, prepareBody, previewLine, tpPctFor, previewQty, ticketQty, vwapxBody, vwapxButton, vwapxLine, vwapxLevels, type OTVwapx, type OTMargin } from './orderTicket';

describe('order ticket helpers', () => {
  it('formats prices on the US-equity tick', () => {
    expect(fmtPx(0.81234)).toBe('0.8123');
    expect(fmtPx(12.345)).toBe('12.35');
    expect(fmtPx(0)).toBe('-');
  });
  it('badges LIVE vs PAPER from the account id', () => {
    expect(accountBadge('DU5491699')).toBe('PAPER');
    expect(accountBadge('U1234567')).toBe('LIVE');
    expect(accountBadge('')).toBe('');
  });
  it('previews floor(position$ / ema)', () => {
    expect(previewQty(1000, 0.8123)).toBe(1231);
    expect(previewQty(1000, 0)).toBe(0);
    expect(previewQty(-5, 1)).toBe(0);
  });
  it('labels the connection', () => {
    expect(connLabel({ conn: 'connected', connNote: '', addr: '127.0.0.1:7496', clientId: 80 })).toBe('CONNECTED 127.0.0.1:7496 #80');
  });
});

describe('act', () => {
  afterEach(() => vi.unstubAllGlobals());
  it('posts JSON with the ticket header', async () => {
    const f = vi.fn(async () => new Response(JSON.stringify({ ok: true }), { status: 200 }));
    vi.stubGlobal('fetch', f);
    await act('prepare', { side: 'BUY' });
    const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('/api/order/prepare');
    expect(init.method).toBe('POST');
    expect((init.headers as Record<string, string>)['X-Order-Ticket']).toBe('1');
  });
  it('surfaces a server refusal', async () => {
    vi.stubGlobal('fetch', async () => new Response(JSON.stringify({ error: 'symbol mismatch' }), { status: 400 }));
    expect((await act('confirm')).error).toBe('symbol mismatch');
  });
});

describe('shares override', () => {
  it('parses only positive whole numbers', () => {
    expect(parseShares('500')).toBe(500);
    expect(parseShares(' 12 ')).toBe(12);
    expect(parseShares('')).toBe(0);
    expect(parseShares('1.5')).toBe(0);
    expect(parseShares('-3')).toBe(0);
  });
  it('shares win over position $; empty shares sizes from $', () => {
    expect(ticketQty(1000, 500, 1.5)).toBe(500);
    expect(ticketQty(1000, 0, 1.5)).toBe(666);
  });
});

describe('margin display', () => {
  const m = (o: Partial<OTMargin>): OTMargin => ({
    status: 'ok', needed: 600, perShare: 0.6, avail: 3000, haveAvail: true, maxShares: 2500, initChange: 500, maintChange: 600,
    commission: 1, warning: '', note: '', text: 'needs $600 margin (avail $3,000) — max 2,500 sh', ...o,
  });
  it('shows Avail $X only once funds arrive', () => {
    expect(availLabel({ account: 'U1', avail: 25000.5, excess: 0, buyingPower: 0, netLiq: 0, haveAvail: true })).toBe('Avail $25,000');
    expect(availLabel(null)).toBe('');
    expect(fmtUsd(1234567.9)).toBe('$1,234,567');
  });
  it('allows confirm within the margin max', () => {
    expect(marginView({ qty: 1000, margin: m({}) })).toEqual({ text: m({}).text, cls: '', useMax: 0, canConfirm: true });
  });
  it('marks qty above max red with Use max and blocks confirm', () => {
    const v = marginView({ qty: 1000, margin: m({ status: 'over', maxShares: 500, text: 'needs $600 margin (avail $300) — max 500 sh' }) });
    expect(v.cls).toBe('over');
    expect(v.useMax).toBe(500);
    expect(v.canConfirm).toBe(false);
  });
  it('warns but allows confirm when the check is unavailable', () => {
    const v = marginView({ qty: 1000, margin: m({ status: 'unavailable', text: 'margin check unavailable (no answer from TWS within 5s)' }) });
    expect(v).toEqual({ text: 'margin check unavailable (no answer from TWS within 5s)', cls: 'warn', useMax: 0, canConfirm: true });
  });
  it('no margin info: plain confirm', () => {
    expect(marginView({ qty: 5, margin: null }).canConfirm).toBe(true);
  });
});

describe('margin preview line', () => {
  const pv = { sym: 'SGRX', qty: 100, longPx: 1.5, shortPx: 1.5, priceSrc: '9EMA' as const,
    buy: { ok: true, maxShares: 2666, perShare: 0.375, note: '' }, sell: { ok: true, maxShares: 2666, perShare: 0.375, note: '' },
    text: 'Max long 2,666 sh · Max short 2,666 sh (margin $/sh 0.38 / 0.38)', at: 1 };
  it('shows the preview for the shown symbol only', () => {
    expect(previewLine({ sym: 'SGRX', preview: pv })).toBe(pv.text);
    expect(previewLine({ sym: 'ABCD', preview: pv })).toBe('');
    expect(previewLine({ sym: 'SGRX', preview: null })).toBe('');
  });
  it('falls back to margin n/a', () => {
    expect(previewLine({ sym: 'SGRX', preview: { ...pv, text: '' } })).toBe('margin n/a');
  });
});

describe('take-profit selector', () => {
  const set = { tpLowPct: 1, tpHighPct: 10 };
  it('defaults to the high preset and remembers low', () => {
    expect(parseTpChoice(null)).toBe('high');
    expect(parseTpChoice('junk')).toBe('high');
    expect(parseTpChoice('low')).toBe('low');
  });
  it('maps the toggle to the configured percent', () => {
    expect(tpPctFor('low', set)).toBe(1);
    expect(tpPctFor('high', set)).toBe(10);
    expect(tpPctFor('low', { tpLowPct: 2, tpHighPct: 8 })).toBe(2);
    expect(tpPctFor('high', { tpLowPct: 0, tpHighPct: 0 })).toBe(10);
  });
  it('sends the chosen percent with prepare', async () => {
    const f = vi.fn(async () => new Response(JSON.stringify({ ok: true }), { status: 200 }));
    vi.stubGlobal('fetch', f);
    await act('prepare', prepareBody('BUY', 1000, 0, 'SGRX', tpPctFor('low', set)));
    const [, init] = f.mock.calls[0] as unknown as [string, RequestInit];
    expect(JSON.parse(init.body as string)).toEqual({ side: 'BUY', positionUSD: 1000, shares: 0, sym: 'SGRX', tpPct: 1, stopOn: true, riskUsd: 0 });
    vi.unstubAllGlobals();
  });
});

describe('soft stop display', () => {
  it('labels the stop with its state', () => {
    expect(stopText({ stopLabel: 'SL 2.41 (soft)', stopState: 'armed', stopOn: true })).toBe('SL 2.41 (soft) armed');
    expect(stopText({ stopLabel: 'SL 2.41 (soft)', stopState: 'exiting', stopOn: true, exitQty: 500, exitFilled: 200 })).toBe(
      'SL 2.41 (soft) exiting 200/500',
    );
    expect(stopText({ stopLabel: 'SL 2.41 (soft)', stopState: 'armed', stopOn: false })).toBe('SL 2.41 (soft) off');
    expect(stopText({ stopLabel: '', stopOn: false })).toBe('SL off');
    expect(stopText({ stopLabel: '', stopOn: true })).toBe('');
  });
  it('allows the toggle only before the stop fires', () => {
    expect(stopToggleable({ active: true, stopState: 'armed' })).toBe(true);
    expect(stopToggleable({ active: true, stopState: 'triggered' })).toBe(false);
    expect(stopToggleable({ active: true, stopState: 'exiting' })).toBe(false);
    expect(stopToggleable({ active: false, stopState: 'done' })).toBe(false);
  });
});

describe('stop-loss row', () => {
  it('defaults SL on; only "off" turns it off', () => {
    expect(parseStopOn(null)).toBe(true);
    expect(parseStopOn('on')).toBe(true);
    expect(parseStopOn('off')).toBe(false);
  });
  it('validates SL% 0.1-20', () => {
    expect(parseStopPct('1.5')).toBe(1.5);
    expect(parseStopPct('0.05')).toBeNull();
    expect(parseStopPct('25')).toBeNull();
    expect(parseStopPct('x')).toBeNull();
  });
  it('sends the SL choice with prepare', () => {
    expect(prepareBody('SELL', 0, 200, 'SGRX', 10, false)).toEqual({ side: 'SELL', positionUSD: 0, shares: 200, sym: 'SGRX', tpPct: 10, stopOn: false, riskUsd: 0 });
    expect(prepareBody('BUY', 500, 0, 'SGRX', 1).stopOn).toBe(true);
  });
});

describe('close / cancel buttons', () => {
  it('enable only with something to act on while connected', () => {
    expect(actionState({ conn: 'connected', closeQty: 500, cancelCount: 0 }, false)).toEqual({ canClose: true, canCancel: false, canBE: false });
    expect(actionState({ conn: 'connected', closeQty: 0, cancelCount: 2 }, false)).toEqual({ canClose: false, canCancel: true, canBE: false });
    expect(actionState({ conn: 'lost', closeQty: 500, cancelCount: 2, beCount: 1 }, false)).toEqual({ canClose: false, canCancel: false, canBE: false });
    expect(actionState({ conn: 'connected', closeQty: 500, cancelCount: 2 }, true)).toEqual({ canClose: false, canCancel: false, canBE: false });
  });
});

describe('pre-market feedback', () => {
  it('shows the 9EMA age: amber > 2m, red > staleMin', () => {
    expect(emaAge({ ema: 0.2637, ageSec: 30 }, 15)).toEqual({ text: '', cls: '' });
    expect(emaAge({ ema: 0.2637, ageSec: 360 }, 15)).toEqual({ text: '6m old', cls: 'amber' });
    expect(emaAge({ ema: 0.2637, ageSec: 16 * 60 }, 15)).toEqual({ text: '16m old', cls: 'red' });
    expect(emaAge({ ema: 0.2637, ageSec: 16 * 60 }, 30).cls).toBe('amber');
  });
  it('gates BUY/SELL on the server emaOk, not the 2-minute stale flag', () => {
    const quote = { sym: 'SAFX', price: 0.27, ema: 0.2637, lastClosed: 1, stale: true, bars: 30, ageSec: 360 };
    expect(emaUsable({ emaOk: true, quote })).toBe(true);
    expect(emaUsable({ emaOk: false, quote })).toBe(false);
  });
  it('labels funds in the account base currency', () => {
    expect(availLabel({ account: 'U1', avail: 2899.5, excess: 0, buyingPower: 0, netLiq: 0, haveAvail: true, currency: 'GBP' })).toBe('Avail £2,899');
    expect(fmtMoney(1500, 'EUR')).toBe('€1,500');
    expect(fmtMoney(1500, undefined)).toBe('$1,500');
  });
  it('CLOSE/CANCEL ignore EMA staleness', () => {
    expect(actionState({ conn: 'connected', closeQty: 500, cancelCount: 1 }, false)).toEqual({ canClose: true, canCancel: true, canBE: false });
  });
});

describe('9EMA pill', () => {
  const quote = { sym: 'IPW', price: 0.6521, ema: 0.6553, lastClosed: 1, stale: false, bars: 338, ageSec: 21 };
  it('shows loading while the bars are incomplete', () => {
    expect(emaPill({ quote, emaNote: 'loading 9EMA' })).toEqual({ value: 'loading 9EMA…', mode: 'closed', loading: true });
  });
  it('shows the value and the mode', () => {
    expect(emaPill({ quote, emaNote: '' })).toEqual({ value: '0.6553', mode: 'closed', loading: false });
    expect(emaPill({ quote: { ...quote, live: true }, emaNote: '' }).mode).toBe('live');
  });
});

describe('risk sizing row', () => {
  it('defaults to the middle preset and remembers the choice', () => {
    expect(parseRiskChoice(null)).toBe('mid');
    expect(parseRiskChoice('low')).toBe('low');
    expect(parseRiskChoice('x')).toBe('mid');
  });
  it('maps choices to the configured USD presets', () => {
    expect(riskFor('low', {})).toBe(25);
    expect(riskFor('mid', {})).toBe(50);
    expect(riskFor('high', { riskHigh: 200 })).toBe(200);
  });
  it('sends riskUsd with prepare (no position $)', () => {
    expect(prepareBody('BUY', 0, 0, 'SGRX', 10, true, 50)).toEqual({ side: 'BUY', positionUSD: 0, shares: 0, sym: 'SGRX', tpPct: 10, stopOn: true, riskUsd: 50 });
  });
  it('shows the server sizing', () => {
    const z = { qty: 2500, limit: 1.5, stop: 1.48, position: 3750, risk: 50, bound: '' };
    expect(sizingLine(z, false)).toBe('= 2500 sh · stop 1.48 · ≈ $3,750 position');
    expect(sizingLine({ ...z, qty: 3333, position: 4999.5, bound: 'max position $' }, false)).toBe('= 3333 sh · stop 1.48 · ≈ $4,999 position (capped: max position $)');
    expect(sizingLine({ ...z, qty: 300, risk: 6 }, true)).toBe('= 300 sh · $ risk ≈ 6 · stop 1.48');
    expect(sizingLine(null, false)).toBe('= - sh');
  });
});

describe('entry follow', () => {
  it('labels following orders', () => {
    expect(followText({ following: true, followLabel: 'follows 9EMA live · every 5 s' })).toBe('follows 9EMA live · every 5 s');
    expect(followText({ following: false, followLabel: 'x' })).toBe('');
    expect(followText({ following: true })).toBe('follows 9EMA');
  });
  it('validates the interval 2-60 s', () => {
    expect(parseFollowSec('5')).toBe(5);
    expect(parseFollowSec('1')).toBeNull();
    expect(parseFollowSec('61')).toBeNull();
    expect(parseFollowSec('2.5')).toBeNull();
  });
});

describe('drop-cancel guard', () => {
  it('defaults ON with 3% / 60 s / 1%', () => {
    expect(dropCancel({})).toEqual({ on: true, pct: 3, sec: 60, near: 1 });
    expect(dropCancel({ dropCancelOff: true, dropPct: 5, dropSec: 120, nearPct: 0 })).toEqual({ on: false, pct: 5, sec: 120, near: 0 });
  });
  it('sends the toggle and thresholds', () => {
    expect(dropCancelBody(false, 0, 0, 0)).toEqual({ on: false, dropPct: 0, dropSec: 0, nearPct: 0 });
    expect(dropCancelBody(true, 3, 60, 1)).toEqual({ on: true, dropPct: 3, dropSec: 60, nearPct: 1 });
  });
});

describe('breakeven', () => {
  it('defaults ON at +5% exact entry', () => {
    expect(breakeven({})).toEqual({ on: true, pct: 5, offset: 0 });
    expect(breakeven({ beOff: true, bePct: 8, beOffsetPct: 0.2 })).toEqual({ on: false, pct: 8, offset: 0.2 });
  });
  it('sends the toggle and thresholds', () => {
    expect(breakevenBody(false, 0, 0)).toEqual({ on: false, bePct: 0, beOffsetPct: 0 });
  });
  it('shows BE in the stop label', () => {
    expect(stopText({ stopLabel: 'SL 2.41 (soft, BE)', stopState: 'armed', stopOn: true })).toBe('SL 2.41 (soft, BE) armed');
  });
});

describe('stat tiles', () => {
  const sizing = { qty: 52, limit: 37.77, stop: 35.88, position: 1964, risk: 98, bound: '' };
  const side = (o: Partial<OTPreviewSide>): OTPreviewSide => ({ ok: true, maxShares: 264, perShare: 0, note: '', ...o });
  const preview = {
    sym: 'AMKR', qty: 52, longPx: 37.77, shortPx: 37.78, priceSrc: '9EMA' as const,
    buy: side({}), sell: side({ maxShares: 63, perShare: 17.22 }), text: '', at: 1, currency: 'GBP',
  };
  const base = { sym: 'AMKR', sizing, preview };
  const opt = { sharesOverride: false, slOn: true, riskUsd: 100 };
  const byKey = (s: Parameters<typeof statTiles>[0], o = opt) => Object.fromEntries(statTiles(s, o).map((t) => [t.key, t]));
  it('shows the six numbers', () => {
    const t = byKey(base);
    expect([t.shares.value, t.stop.value, t.position.value, t.risk.value, t.maxLong.value, t.maxShort.value]).toEqual([
      '52', '35.88', '$1,964', '$100', '264', '63',
    ]);
    expect(t.stop.sub).toBe('−5.0%');
    expect(t.maxLong.title).toContain('margin/sh —'); // never "0.00"
    expect(t.maxShort.title).toContain('£17.22/sh margin');
  });
  it('flags caps amber and over-max red', () => {
    const t = byKey({ ...base, sizing: { ...sizing, qty: 300, bound: 'max position $' } });
    expect(t.shares.cls).toBe('amber');
    expect(t.shares.sub).toBe('capped: max position $');
    expect(t.maxLong.cls).toBe('red');
    expect(t.maxShort.cls).toBe('red');
    expect(byKey(base).maxShort.cls).toBe('');
  });
  it('n/a, rejected and pending previews are muted', () => {
    const t = byKey({ ...base, preview: { ...preview, buy: side({ ok: false, note: 'IB 201: x', rejected: true }), sell: side({ ok: false }) } });
    expect([t.maxLong.value, t.maxLong.cls, t.maxShort.value]).toEqual(['rejected', 'muted', 'n/a']);
    expect(byKey({ ...base, preview: null }).maxLong.value).toBe('—');
    expect(byKey({ ...base, sym: 'IPW' }).maxLong.value).toBe('—'); // another symbol's preview
  });
  it('manual shares and SL off', () => {
    const t = byKey(base, { sharesOverride: true, slOn: false, riskUsd: 100 });
    expect(t.shares.tag).toBe('manual');
    expect(t.risk.value).toBe('≈ $98');
    expect([t.stop.value, t.stop.cls]).toEqual(['no SL', 'amber']);
    expect(t.risk.sub).toBe('notional');
  });
});

describe('shortable tile', () => {
  const si = { known: true, shortable: true, avail: 150000, capped: false, fee: 12.5, asOf: 1793000000, ageMin: 5, stale: false, source: 'IBKR short-stock file', note: '' };
  it('shows compact shares and the fee', () => {
    const t = shortTile(si, 52);
    expect([t.value, t.sub, t.cls, t.tag]).toEqual(['150K', 'fee 12.5% · file, 5m old', '', '']);
    expect(t.title).toContain('IBKR file: 150,000 sh, fee 12.5%');
    expect(fmtCompact(1_250_000)).toBe('1.25M');
    expect(fmtCompact(1500)).toBe('1.5K');
    expect(fmtCompact(10_000_000)).toBe('10M');
  });
  it('amber when fewer than the chosen shares, red when none, age when > 15 min', () => {
    expect(shortTile({ ...si, avail: 40 }, 52).cls).toBe('amber');
    expect(shortTile({ ...si, avail: 10_000_000, capped: true }, 1e9).cls).toBe('');
    const none = shortTile({ ...si, avail: 0, shortable: false }, 52);
    expect([none.value, none.cls]).toEqual(['none', 'red']);
    expect(shortTile({ ...si, ageMin: 40 }, 52).sub).toBe('fee 12.5% · file, 40m old');
  });
  it('unknown or missing is "—"', () => {
    expect(shortTile(null, 52).value).toBe('—');
    const u = shortTile({ ...si, known: false, note: 'not in IBKR\'s short list' }, 52);
    expect([u.value, u.cls, u.sub]).toEqual(['—', 'muted', "not in IBKR's short list"]);
  });
  it('is the 7th stat tile', () => {
    const tiles = statTiles({ sym: 'X', sizing: null, preview: null, short: si }, { sharesOverride: false, slOn: true, riskUsd: 50 });
    expect(tiles.map((t) => t.key)).toEqual(['shares', 'stop', 'position', 'risk', 'maxLong', 'maxShort', 'shortable']);
  });
});

describe('shortable tile: TWS live', () => {
  const si = {
    known: true, shortable: true, avail: 2300, capped: false, fee: 12.5, asOf: 1793000000, ageMin: 12, stale: false,
    source: 'TWS live', note: '', live: true, liveShares: 2300, liveIndicator: 3, liveAt: 1793000600000, fileAvail: 150000, fileKnown: true,
  };
  it('shows the live count with a live tag and both sources in the tooltip', () => {
    const t = shortTile(si, 52);
    expect([t.value, t.tag, t.sub, t.cls]).toEqual(['2.3K', 'live', 'fee 12.5%', '']);
    expect(t.title).toContain('TWS live: 2,300 sh, indicator 3');
    expect(t.title).toContain('IBKR file: 150,000 sh');
    expect(shortTile(si, 5000).cls).toBe('amber');
    expect(shortTile({ ...si, avail: 0, shortable: false }, 52).value).toBe('none');
  });
  it('falls back to the file with the reason', () => {
    const t = shortTile({ ...si, live: false, source: 'IBKR short-stock file', avail: 150000, liveErr: 'IB 10089: needs subscription' }, 52);
    expect([t.value, t.tag, t.sub]).toEqual(['150K', '', 'fee 12.5% · file, 12m old']);
    expect(t.title).toContain('TWS live: IB 10089');
  });
});

describe('SL to entry button', () => {
  it('enables with a movable stop while connected', () => {
    expect(actionState({ conn: 'connected', closeQty: 0, cancelCount: 0, beCount: 1 }, false).canBE).toBe(true);
    expect(actionState({ conn: 'connected', closeQty: 500, cancelCount: 0, beCount: 0 }, false).canBE).toBe(false);
    expect(actionState({ conn: 'connected', closeQty: 500, cancelCount: 0, beCount: 1 }, true).canBE).toBe(false);
  });
});

describe('presets', () => {
  it('defaults to Custom', () => {
    expect(activePreset({})).toBe('custom');
    expect(activePreset({ preset: 'power' })).toBe('power');
  });
  it('switching keeps the Custom 5m-trail / timeout defaults', () => {
    expect(presetBody('power', { trail5m: true, entryTimeoutMin: 10 })).toEqual({ preset: 'power', trail5m: true, entryTimeoutMin: 10 });
    expect(presetBody('custom', {}, { trail5m: true })).toEqual({ preset: 'custom', trail5m: true, entryTimeoutMin: 0 });
  });
});

describe('percent risk', () => {
  const funds = { account: 'U1', avail: 0, excess: 0, buyingPower: 0, netLiq: 2800, haveAvail: true, currency: 'GBP' };
  it('defaults 1 / 2 / 3 %', () => {
    expect(riskPctFor('low', {})).toBe(1);
    expect(riskPctFor('mid', {})).toBe(2);
    expect(riskPctFor('high', { riskPctHigh: 4 })).toBe(4);
  });
  it('sizes from the USD net liquidation only with a TWS rate', () => {
    const s = { riskMode: '%' as const };
    const off = riskView({ settings: s as never, funds: { ...funds, haveUsd: false, usdNote: 'no USD→GBP rate from TWS yet; % risk off' } });
    expect(off.mode).toBe('$');
    expect(off.note).toContain('rate');
    const on = riskView({ settings: s as never, funds: { ...funds, haveUsd: true, usdRate: 0.8, netLiqUsd: 3500 } });
    expect(on.mode).toBe('%');
    expect(pctRiskUsd(3, 3500)).toBe(105);
    expect(baseRiskUsd('high', {} as never, on)).toBe(105);
    expect(riskLabel(3, 105)).toBe('3% · $105');
    expect(riskLabel(3 * 1.5, 157.5)).toBe('4.5% · $158');
    expect(riskLabel(null, 50)).toBe('$50');
  });
  it('$ mode is unchanged', () => {
    const v = riskView({ settings: {} as never, funds: { ...funds, haveUsd: true, netLiqUsd: 3500 } });
    expect(v.mode).toBe('$');
    expect(v.note).toBe('');
    expect(baseRiskUsd('mid', {} as never, v)).toBe(50);
  });
  it('power lands on the 3 % button', () => {
    expect(powerRiskChoice({} as never)).toBe('high');
    expect(powerRiskChoice({ riskPctMid: 3 } as never)).toBe('mid');
  });
  it('risk tile shows % and $', () => {
    const tiles = statTiles({ sym: 'X', sizing: null, preview: null } as never, { sharesOverride: false, slOn: true, riskUsd: 105, riskPct: 3 });
    expect(tiles.find((t) => t.key === 'risk')!.value).toBe('$105');
    const d = statTiles({ sym: 'X', sizing: null, preview: null } as never, { sharesOverride: false, slOn: true, riskUsd: 50 });
    expect(d.find((t) => t.key === 'risk')!.value).toBe('$50');
  });
});

describe('VWAP X', () => {
  // 2026-10-29 14:01 ET = 18:01 UTC
  const crossMs = Date.UTC(2026, 9, 29, 18, 1);
  const ok: OTVwapx = { available: true, reason: '', crossMs, closesInSec: 0, entry: 27.99, stop: 27.8, tp: 28.27, ageSec: 150, stopPct: -0.678813 };
  it('builds the prepare body: BUY only, kind vwapx, size from shares or risk', () => {
    expect(vwapxBody(120, 'MGM')).toEqual({ side: 'BUY', kind: 'vwapx', positionUSD: 0, shares: 120, sym: 'MGM', riskUsd: 0 });
    expect(vwapxBody(0, 'MGM', 50)).toMatchObject({ shares: 0, riskUsd: 50, kind: 'vwapx' });
  });
  it('titles the levels in ET and shows the muted line', () => {
    expect(vwapxLevels(ok)).toBe('entry 27.99 · stop 27.80 · TP 28.27 · cross 14:01');
    expect(vwapxLine(ok)).toBe('VWAP X 14:01 · entry 27.99 · stop 27.80 (−0.68%) · TP 28.27');
    expect(vwapxLine({ ...ok, available: false })).toBe('');
    expect(vwapxLine(null)).toBe('');
  });
  it('enables only when available, connected and sized', () => {
    expect(vwapxButton(ok, true, true, false)).toMatchObject({ label: 'VWAP X', enabled: true });
    expect(vwapxButton(ok, true, true, false).title).toContain('entry 27.99 · stop 27.80 · TP 28.27 · cross 14:01');
    expect(vwapxButton(ok, false, true, false)).toMatchObject({ enabled: false });
    expect(vwapxButton(ok, false, true, false).title).toContain('pick a risk or enter shares');
    expect(vwapxButton(ok, true, false, false).enabled).toBe(false);
    expect(vwapxButton(ok, true, true, true).enabled).toBe(false);
    expect(vwapxButton(undefined, true, true, false).enabled).toBe(false);
  });
  it('counts down while the cross candle forms, else shows the reason', () => {
    const forming: OTVwapx = { ...ok, available: false, reason: 'cross candle closes in 23s', closesInSec: 23, entry: 0, stop: 0, tp: 0 };
    expect(vwapxButton(forming, true, true, false)).toEqual({ label: 'VWAP X · 23s', enabled: false, title: 'cross candle closes in 23s' });
    const old: OTVwapx = { ...ok, available: false, reason: 'cross is 15m old', closesInSec: 0 };
    expect(vwapxButton(old, true, true, false)).toEqual({ label: 'VWAP X', enabled: false, title: 'cross is 15m old' });
  });
  it('VWAP X down: SELL body, short levels line with the borrow note, ↓ button label', () => {
    const bear: OTVwapx = { available: true, reason: '', crossMs, closesInSec: 0, entry: 27.99, stop: 28.21, tp: 27.71, ageSec: 150, stopPct: 0.785995, side: 'SELL', borrow: 'unknown' };
    expect(vwapxBody(120, 'MGM', 0, 'SELL')).toEqual({ side: 'SELL', kind: 'vwapx', positionUSD: 0, shares: 120, sym: 'MGM', riskUsd: 0 });
    expect(vwapxBody(120, 'MGM')).toMatchObject({ side: 'BUY' });
    expect(vwapxLine(bear)).toBe('VWAP X↓ 14:01 · entry 27.99 · stop 28.21 (+0.79%) · TP 27.71 · borrow unknown');
    expect(vwapxLine({ ...bear, borrow: 'ok' })).toBe('VWAP X↓ 14:01 · entry 27.99 · stop 28.21 (+0.79%) · TP 27.71');
    expect(vwapxButton(bear, true, true, false, 'SELL')).toMatchObject({ label: 'VWAP X↓', enabled: true });
    expect(vwapxButton(bear, true, true, false, 'SELL').title).toContain('borrow unknown');
    expect(vwapxButton(bear, true, true, false, 'SELL').title).toContain('SELL SHORT');
    expect(vwapxButton(bear, false, true, false, 'SELL').enabled).toBe(false);
    expect(vwapxButton(undefined, true, true, false, 'SELL')).toMatchObject({ label: 'VWAP X↓', enabled: false });
    const forming: OTVwapx = { ...bear, available: false, reason: 'cross candle closes in 9s', closesInSec: 9 };
    expect(vwapxButton(forming, true, true, false, 'SELL')).toEqual({ label: 'VWAP X↓ · 9s', enabled: false, title: 'cross candle closes in 9s' });
    const nb: OTVwapx = { ...bear, available: false, reason: 'not shortable (no borrow)' };
    expect(vwapxButton(nb, true, true, false, 'SELL')).toEqual({ label: 'VWAP X↓', enabled: false, title: 'not shortable (no borrow)' });
    expect(vwapxLine(nb)).toBe('');
  });
  it('shows a VWAP X order stop as the IB stop with its own status, not a soft stop toggle', () => {
    const o = { stopLabel: 'SL 27.80 (IB stop)', stopState: '' as const, stopOn: false, exitQty: 0, exitFilled: 0, kind: 'vwapx' as const, slStatus: 'PreSubmitted', active: true };
    expect(stopText(o)).toBe('SL 27.80 (IB stop) PreSubmitted');
    expect(stopToggleable(o)).toBe(false);
    expect(stopToggleable({ stopState: 'armed', active: true })).toBe(true);
  });
});

describe('vwapxSizing', () => {
  const long = { available: true, reason: '', crossMs: 0, closesInSec: 0, entry: 28, stop: 27.8, tp: 28.28, ageSec: 0, stopPct: -0.71 };
  const short = { ...long, side: 'SELL' as const, entry: 28, stop: 28.2, tp: 27.72 };
  it('sizes a long from risk like the server', () => {
    const z = vwapxSizing(long, 50, 0, 100000)!;
    expect(z.qty).toBe(242);
    expect(z.positionUsd).toBe(6776);
    expect(z.riskUsd).toBeCloseTo(48.4, 6);
    expect(z.stopPct).toBeCloseTo(0.714, 2);
    expect(z.rr).toBeCloseTo(1.4, 6);
    expect(z.capped).toBe(false);
  });
  it('sizes a short on the same distance', () => {
    const z = vwapxSizing(short, 50, 0, 100000)!;
    expect(z.qty).toBe(242);
    expect(z.rr).toBeCloseTo(1.4, 6);
  });
  it('trims to the max position and says so', () => {
    const z = vwapxSizing(long, 50, 0, 5000)!;
    expect(z.qty).toBe(178);
    expect(z.capped).toBe(true);
    expect(z.riskUsd).toBeCloseTo(35.6, 6);
    const l = vwapxSizingLine(z, 50, 5000)!;
    expect(l.cls).toBe('warn');
    expect(l.text).toContain('capped by max $5,000');
    expect(l.text).toContain('real risk $36 of $50');
  });
  it('typed shares override the risk', () => {
    const z = vwapxSizing(long, 50, 100, 100000)!;
    expect(z.qty).toBe(100);
    expect(z.typed).toBe(true);
    expect(z.riskUsd).toBeCloseTo(20, 6);
    expect(z.overMax).toBe(false);
  });
  it('flags typed shares above the max position', () => {
    const z = vwapxSizing(long, 50, 1000, 5000)!;
    expect(z.overMax).toBe(true);
    expect(vwapxSizingLine(z, 50, 5000)!.cls).toBe('over');
  });
  it('is null without a usable stop distance, size or levels', () => {
    expect(vwapxSizing({ ...long, stop: 28 }, 50, 0, 5000)).toBeNull();
    expect(vwapxSizing({ ...long, stop: NaN }, 50, 0, 5000)).toBeNull();
    expect(vwapxSizing({ ...long, available: false }, 50, 0, 5000)).toBeNull();
    expect(vwapxSizing(null, 50, 0, 5000)).toBeNull();
    expect(vwapxSizing(long, 0, 0, 5000)).toBeNull();
    expect(vwapxSizing(long, 0.1, 0, 5000)).toBeNull();
    expect(vwapxSizingLine(null, 50, 5000)).toBeNull();
  });
  it('tints rr < 1 amber and formats the line', () => {
    const z = vwapxSizing({ ...long, tp: 28.1 }, 50, 0, 100000)!;
    expect(z.rr).toBeCloseTo(0.5, 6);
    expect(vwapxSizingLine(z, 50, 100000)!.cls).toBe('warn');
    expect(vwapxSizingLine(vwapxSizing(long, 50, 0, 100000), 50, 100000)!.text).toBe('242 sh · $6,776 · risk $48 · stop 0.71% · TP 1.00% = 1.4R');
  });
});
