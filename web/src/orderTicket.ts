// Order Ticket pane (?pane=order) - the API client and pure helpers. The server
// side is internal/orderticket (HTTP under /api/order/, not the WebSocket):
// the pane polls GET /api/order/state and posts each action. Every POST carries
// the X-Order-Ticket header the server requires (it forces a CORS preflight, so
// no other web page can drive the ticket).

import { etTime } from './flagEvents';

export interface OTQuote { sym: string; price: number; ema: number; lastClosed: number; stale: boolean; bars: number; ageSec?: number; lastAt?: number; live?: boolean }
export interface OTSettings { host: string; port: number; clientId: number; maxPositionUSD: number; tpLowPct: number; tpHighPct: number;
  stopPct?: number; stopFollow?: boolean; staleMin?: number; emaLive?: boolean;
  riskLow?: number; riskMid?: number; riskHigh?: number; followSec?: number;
  preset?: 'custom' | 'power' | 'newsflag'; trail5m?: boolean; entryTimeoutMin?: number;
  dropCancelOff?: boolean; dropPct?: number; dropSec?: number; nearPct?: number;
  beOff?: boolean; bePct?: number; beOffsetPct?: number;
  riskMode?: '$' | '%'; riskPctLow?: number; riskPctMid?: number; riskPctHigh?: number;
}
export interface OTMargin {
  status: 'ok' | 'over' | 'unavailable'; needed: number; perShare: number; avail: number; haveAvail: boolean; maxShares: number;
  initChange: number; maintChange: number; commission: number; warning: string; note: string; text: string;
}
export interface OTPreviewSide { ok: boolean; maxShares: number; perShare: number; note: string; rejected?: boolean }
export interface OTPreview {
  sym: string; qty: number; longPx: number; shortPx: number; priceSrc: '9EMA' | 'last'; buy: OTPreviewSide; sell: OTPreviewSide; text: string; at: number; currency?: string;
}
export interface OTFunds { account: string; avail: number; excess: number; buyingPower: number; netLiq: number; haveAvail: boolean; currency?: string;
  /** base units per 1 USD from TWS (USD ledger ExchangeRate); netLiqUsd = netLiq / usdRate; haveUsd false = no rate, % risk off */
  usdRate?: number; netLiqUsd?: number; haveUsd?: boolean; usdNote?: string }
export interface OTPending {
  token: string; kind?: '' | 'vwapx'; crossMs?: number; plannedStop?: number; side: 'BUY' | 'SELL' | 'CLOSE'; sym: string; qty: number; limit: number; tp: number; ema: number; text: string; at: number; tpPct?: number; stopOff?: boolean; stopPct?: number; warn?: string;
  margin?: OTMargin | null;
}
export interface OTOrder {
  id: number; childId: number; sym: string; side: 'BUY' | 'SELL'; qty: number; limit: number; filled: number; avg: number;
  tp: number; status: string; childStatus: string; childFilled: number; following: boolean; note: string; active: boolean;
  followLabel?: string; stopLevel?: number; stopState?: '' | 'armed' | 'triggered' | 'exiting' | 'done' | 'disarmed'; stopOn?: boolean; stopLabel?: string; stopBe?: boolean; canBe?: boolean; presetLabel?: string;
  exitId?: number; exitQty?: number; exitFilled?: number;
  /** VWAP X bracket: a real IB stop child (its own status), no soft stop */
  canStop?: boolean; kind?: '' | 'vwapx'; slId?: number; sl?: number; slStatus?: string; slFilled?: number;
}
/** The VWAP X preset for the shown symbol (State.vwapx, internal/orderticket/vwapx.go). */
export interface OTVwapx {
  available: boolean; reason: string; crossMs: number; closesInSec: number;
  entry: number; stop: number; tp: number; ageSec: number; stopPct: number;
  /** SELL = VWAP X down (bear cross, a short); borrow (short only): ok | unknown. No SSR data exists. */
  side?: 'BUY' | 'SELL'; borrow?: '' | 'ok' | 'unknown';
}
export interface OTState {
  conn: 'disconnected' | 'connecting' | 'connected' | 'lost' | 'error';
  connNote: string; addr: string; clientId: number; account: string; live: boolean;
  sym: string; symSource: '' | 'TWS' | 'scanner'; twsSym: string; quote: OTQuote; settings: OTSettings;
  pending: OTPending | null; orders: OTOrder[] | null; note: string; now: number;
  funds?: OTFunds | null;
  preview?: OTPreview | null;
  stopUnwatched?: boolean; stopWarn?: string; stopAlert?: string;
  closeQty?: number; cancelCount?: number; beCount?: number; emaOk?: boolean; emaNote?: string; currency?: string;
  sizing?: OTSizing | null;
  short?: OTShort | null;
  // the loaded symbol's latest Power Setup today (null = none): A+ sizes at mult x risk with the Power Setup preset
  psGrade?: { aplus: boolean; baseRange: number; barMs: number; mult: number } | null;
  vwapx?: OTVwapx | null;
  vwapxBear?: OTVwapx | null;
}

export async function fetchState(): Promise<OTState> {
  const r = await fetch('/api/order/state', { cache: 'no-store' });
  if (!r.ok) throw new Error(`state: HTTP ${r.status}`);
  return (await r.json()) as OTState;
}

export interface ActionResult { ok?: boolean; error?: string; result?: unknown; state?: OTState }

export async function act(action: string, body: Record<string, unknown> = {}): Promise<ActionResult> {
  const r = await fetch(`/api/order/${action}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Order-Ticket': '1' },
    body: JSON.stringify(body),
  });
  let j: ActionResult = {};
  try {
    j = (await r.json()) as ActionResult;
  } catch {
    j = { error: `HTTP ${r.status}` };
  }
  if (!r.ok && !j.error) j.error = `HTTP ${r.status}`;
  return j;
}

/** Price with the US-equity tick's decimals: 4 under $1, 2 from $1. */
export function fmtPx(p: number): string {
  if (!(p > 0)) return '-';
  return p < 1 ? p.toFixed(4) : p.toFixed(2);
}

export function connLabel(s: Pick<OTState, 'conn' | 'connNote' | 'addr' | 'clientId'>): string {
  const base = `${s.conn.toUpperCase()} ${s.addr} #${s.clientId}`;
  return s.connNote ? `${base} - ${s.connNote}` : base;
}

/** "LIVE" / "PAPER" from the account id (IBKR paper accounts start with D). */
export function accountBadge(account: string): 'LIVE' | 'PAPER' | '' {
  if (!account) return '';
  return account.toUpperCase().startsWith('D') ? 'PAPER' : 'LIVE';
}

/** Shares the server will size: floor(position$ / ema) - display only. */
export function previewQty(positionUSD: number, ema: number): number {
  if (!(positionUSD > 0) || !(ema > 0)) return 0;
  return Math.floor(positionUSD / ema + 1e-9);
}

export const POS_KEY = 'orderTicket.positionUSD';

export const SHARES_KEY = 'orderTicket.shares';

/** Parsed Shares input: a positive whole number, else 0 (= size from Position $). */
export function parseShares(v: string): number {
  const n = Number(v.trim());
  return Number.isInteger(n) && n > 0 ? n : 0;
}

/** The quantity the ticket will ask for: Shares when set, else floor(position$ / ema). */
export function ticketQty(positionUSD: number, shares: number, ema: number): number {
  return shares > 0 ? shares : previewQty(positionUSD, ema);
}

/** Whole dollars with thousands separators: 25000.5 -> "$25,000". */
export function fmtUsd(v: number): string {
  return fmtMoney(v, 'USD');
}

/** "$", "£", "€", else the ISO code + space. */
export function curSym(cur: string | undefined): string {
  switch ((cur || 'USD').toUpperCase()) {
    case 'USD':
      return '$';
    case 'GBP':
      return '£';
    case 'EUR':
      return '€';
    default:
      return `${(cur || '').toUpperCase()} `;
  }
}

/** Whole units in the given currency: 2899.5 GBP -> "£2,899". */
export function fmtMoney(v: number, cur: string | undefined): string {
  if (!Number.isFinite(v)) return '-';
  return `${v < 0 ? '-' : ''}${curSym(cur)}${Math.floor(Math.abs(v)).toLocaleString('en-US')}`;
}

/** "Avail $X" from the account summary, or '' before it arrives. */
export function availLabel(f: OTFunds | null | undefined): string {
  return f && f.haveAvail ? `Avail ${fmtMoney(f.avail, f.currency)}` : '';
}

export interface MarginView {
  text: string;
  cls: '' | 'over' | 'warn';
  /** Share count for the "Use max (N)" button (0 = no button). */
  useMax: number;
  canConfirm: boolean;
}

/**
 * The confirm line's margin part: "needs $M margin (avail $A) — max N sh",
 * red with Use max when qty > N (Confirm blocked), a warning when the check
 * was unavailable (Confirm still allowed). The server enforces the same rule.
 */
export function marginView(p: Pick<OTPending, 'qty' | 'margin'>): MarginView {
  const m = p.margin;
  if (!m) return { text: '', cls: '', useMax: 0, canConfirm: true };
  if (m.status === 'unavailable') return { text: m.text || 'margin check unavailable', cls: 'warn', useMax: 0, canConfirm: true };
  const over = p.qty > m.maxShares;
  return { text: m.text, cls: over ? 'over' : m.warning ? 'warn' : '', useMax: over && m.maxShares > 0 ? m.maxShares : 0, canConfirm: !over };
}

/** The margin preview line for the symbol shown ('' when none / another symbol's). */
export function previewLine(s: Pick<OTState, 'sym' | 'preview'>): string {
  const p = s.preview;
  if (!p || !s.sym || p.sym !== s.sym) return '';
  return p.text || 'margin n/a';
}

export const PREVIEW_DEBOUNCE_MS = 1500;

export const TP_KEY = 'orderTicket.tpChoice';
export type TPChoice = 'low' | 'high';

/** Stored TP choice; anything else is the default (high, 10%). */
export function parseTpChoice(v: string | null | undefined): TPChoice {
  return v === 'low' ? 'low' : 'high';
}

/** The preset percent for a choice (server defaults 1 / 10 if missing). */
export function tpPctFor(choice: TPChoice, s: Pick<OTSettings, 'tpLowPct' | 'tpHighPct'>): number {
  return choice === 'low' ? s.tpLowPct || 1 : s.tpHighPct || 10;
}

/** The VWAP X prepare payload (BUY = bull cross, SELL = VWAP X down, a short); entry/stop/TP come from the cross candle on the server. */
export function vwapxBody(shares: number, sym: string, riskUsd = 0, side: 'BUY' | 'SELL' = 'BUY') {
  return { side, kind: 'vwapx' as const, positionUSD: 0, shares, sym, riskUsd };
}

/** The VWAP X button: label (countdown while the cross candle forms), enabled, tooltip. */
export function vwapxButton(v: OTVwapx | null | undefined, canSize: boolean, connected: boolean, busy: boolean, side: 'BUY' | 'SELL' = 'BUY'): { label: string; enabled: boolean; title: string } {
  const short = side === 'SELL';
  const base = short ? 'VWAP X↓' : 'VWAP X';
  if (!v) return { label: base, enabled: false, title: `${base}: no state yet` };
  const label = !v.available && v.closesInSec > 0 ? `${base} · ${v.closesInSec}s` : base;
  if (!v.available) return { label, enabled: false, title: v.reason || (short ? 'no VWAP bear cross' : 'no VWAP bull cross') };
  const lv = vwapxLevels(v);
  if (!connected) return { label, enabled: false, title: `not connected to TWS · ${lv}` };
  if (!canSize) return { label, enabled: false, title: `pick a risk or enter shares · ${lv}` };
  if (short) {
    const bw = v.borrow === 'unknown' ? ' · borrow unknown' : '';
    return { label, enabled: !busy, title: `${lv}${bw} · SELL SHORT LMT at the current price + real IB buy stop at the VWAP + 3R TP; Confirm next` };
  }
  return { label, enabled: !busy, title: `${lv} · BUY LMT at the current price + real IB stop at the VWAP + 3R TP; Confirm next` };
}

/** "entry 27.99 · stop 27.80 · TP 28.27 · cross 14:01" (ET). */
export function vwapxLevels(v: Pick<OTVwapx, 'entry' | 'stop' | 'tp' | 'crossMs'>): string {
  return `entry ${fmtPx(v.entry)} · stop ${fmtPx(v.stop)} · TP ${fmtPx(v.tp)} · cross ${etTime(v.crossMs)}`;
}

/** The muted line under the quote pills while VWAP X is available ('' otherwise). */
export function vwapxLine(v: OTVwapx | null | undefined): string {
  if (!v || !v.available) return '';
  const short = v.side === 'SELL';
  const pct = (short && v.stopPct > 0 ? '+' : '') + v.stopPct.toFixed(2).replace('-', '−');
  const bw = short && v.borrow === 'unknown' ? ' · borrow unknown' : '';
  return `${short ? 'VWAP X↓' : 'VWAP X'} ${etTime(v.crossMs)} · entry ${fmtPx(v.entry)} · stop ${fmtPx(v.stop)} (${pct}%) · TP ${fmtPx(v.tp)}${bw}`;
}

export interface VwapxSizing {
  qty: number;
  /** qty x entry. */
  positionUsd: number;
  /** What is actually at stake if the stop fills: qty x per-share risk. */
  riskUsd: number;
  /** Per-share risk |entry - stop|. */
  perShare: number;
  /** Stop distance in percent of entry (unsigned). */
  stopPct: number;
  /** Take-profit distance in percent of entry (unsigned). */
  tpPct: number;
  /** Reward / risk: |tp - entry| / per-share risk (0 when there is no TP). */
  rr: number;
  /** True when the max position trimmed the risk-based size. */
  capped: boolean;
  /** True when typed shares x entry is above the max position (the server refuses it). */
  overMax: boolean;
  /** The typed Shares value is in use (risk is then only what those shares would lose). */
  typed: boolean;
}

/**
 * What a VWAP X press would size to, mirroring the server's riskSize / prepareVwapxPending:
 * typed shares win (refused above max position), else floor(risk / per-share) trimmed to
 * floor(max / entry). Null when the levels are unusable or nothing can size it.
 */
export function vwapxSizing(v: OTVwapx | null | undefined, riskUsd: number, sharesOverride: number, maxPositionUSD: number): VwapxSizing | null {
  if (!v || !v.available) return null;
  const per = Math.abs(v.entry - v.stop);
  if (!(v.entry > 0) || !(per > 0) || !Number.isFinite(per)) return null;
  const typed = sharesOverride > 0;
  let qty = 0;
  let capped = false;
  let overMax = false;
  if (typed) {
    qty = Math.floor(sharesOverride);
    overMax = qty * v.entry > maxPositionUSD + 1e-9;
  } else if (riskUsd > 0) {
    qty = Math.floor(riskUsd / (per + COMMISSION_PER_SHARE) + 1e-9);
    const capQ = Math.floor(maxPositionUSD / v.entry + 1e-9);
    if (qty > capQ) {
      qty = capQ;
      capped = true;
    }
  }
  if (!(qty > 0) || !Number.isFinite(qty)) return null;
  const tpDist = v.tp > 0 ? Math.abs(v.tp - v.entry) : 0;
  return {
    qty,
    positionUsd: qty * v.entry,
    riskUsd: qty * per,
    perShare: per,
    stopPct: (per / v.entry) * 100,
    tpPct: (tpDist / v.entry) * 100,
    rr: tpDist / per,
    capped,
    overMax,
    typed,
  };
}

/** The compact sizing line under VWAP X: text + tone ('' | 'warn' | 'over'); null shows nothing. */
export function vwapxSizingLine(z: VwapxSizing | null, riskWanted: number, maxPositionUSD: number): { text: string; cls: '' | 'warn' | 'over' } | null {
  if (!z) return null;
  const money = (v: number) => fmtMoney(Math.round(v), 'USD'); // round (fmtMoney floors, which shows $49 for 49.9999)
  const base = `${z.qty.toLocaleString('en-US')} sh · ${money(z.positionUsd)}`;
  const tail = `stop ${z.stopPct.toFixed(2)}% · TP ${z.tpPct.toFixed(2)}% = ${z.rr.toFixed(1)}R`;
  if (z.overMax) return { text: `${base} · over max ${money(maxPositionUSD)} - refused`, cls: 'over' };
  if (z.capped) return { text: `${base} · capped by max ${money(maxPositionUSD)} · real risk ${money(z.riskUsd)} of ${money(riskWanted)} · ${tail}`, cls: 'warn' };
  return { text: `${base} · risk ${money(z.riskUsd)} · ${tail}`, cls: z.rr < 1 ? 'warn' : '' };
}

/** The prepare payload: the chosen TP percent rides along with side and size. */
export function prepareBody(side: 'BUY' | 'SELL', positionUSD: number, shares: number, sym: string, tpPct: number, stopOn = true, riskUsd = 0) {
  return { side, positionUSD, shares, sym, tpPct, stopOn, riskUsd };
}

/** The order row's soft-stop text: "SL 2.41 (soft) armed", "... off", exit fill progress. */
export function stopText(o: Pick<OTOrder, 'stopLabel' | 'stopState' | 'stopOn' | 'exitQty' | 'exitFilled'> & Partial<Pick<OTOrder, 'kind' | 'slStatus'>>): string {
  // a VWAP X order's stop is a real IB order: show its own status like the TP child
  if (o.kind === 'vwapx') return o.stopLabel ? `${o.stopLabel}${o.slStatus ? ` ${o.slStatus}` : ''}` : '';
  if (!o.stopLabel) return o.stopOn === false ? 'SL off' : '';
  if (o.stopOn === false) return `${o.stopLabel} off`;
  if (o.stopState === 'exiting') return `${o.stopLabel} exiting ${o.exitFilled ?? 0}/${o.exitQty ?? 0}`;
  return o.stopState ? `${o.stopLabel} ${o.stopState}` : o.stopLabel;
}

/** Whether the per-order stop toggle can be used (not once the stop fired). */
export function stopToggleable(o: Pick<OTOrder, 'stopState' | 'active'> & Partial<Pick<OTOrder, 'kind'>>): boolean {
  return o.kind !== 'vwapx' && o.active && o.stopState !== 'triggered' && o.stopState !== 'exiting';
}

export const SL_KEY = 'orderTicket.stopOn';

/** Stored SL on/off choice; default ON (only an explicit "off" turns it off). */
export function parseStopOn(v: string | null | undefined): boolean {
  return v !== 'off';
}

/** A valid stop % for the SL row (0.1-20), else null. */
export function parseStopPct(v: string): number | null {
  const n = Number(v.trim());
  return Number.isFinite(n) && n >= 0.1 && n <= 20 ? n : null;
}

export const ONLY_TICKET_ORDERS = "Acts only on orders this ticket placed (client 80, its own audit) - never on other TWS orders or positions.";

/** CLOSE / CANCEL button state for the shown symbol. */
export function actionState(s: Pick<OTState, 'conn' | 'closeQty' | 'cancelCount'> & Partial<Pick<OTState, 'beCount'>>, busy: boolean) {
  const up = s.conn === 'connected' && !busy;
  return { canClose: up && (s.closeQty ?? 0) > 0, canCancel: up && (s.cancelCount ?? 0) > 0, canBE: up && (s.beCount ?? 0) > 0 };
}

/**
 * 9EMA age: "6m old" suffix, amber past 2 min, red (BUY/SELL blocked) past
 * staleMin. Thin pre-market names close few 1m bars, hence the wide limit.
 */
export function emaAge(q: Pick<OTQuote, 'ema' | 'ageSec'>, staleMin: number | undefined): { text: string; cls: '' | 'amber' | 'red' } {
  if (!(q.ema > 0) || q.ageSec === undefined) return { text: '', cls: '' };
  const m = Math.max(0, Math.floor(q.ageSec / 60));
  const lim = staleMin || 15;
  if (q.ageSec > lim * 60) return { text: `${m}m old`, cls: 'red' };
  if (q.ageSec > 120) return { text: `${m}m old`, cls: 'amber' };
  return { text: m >= 1 ? `${m}m old` : '', cls: '' };
}

/** BUY/SELL gate on the EMA: the server's emaOk when present. */
export function emaUsable(s: Pick<OTState, 'emaOk' | 'quote'>): boolean {
  return s.emaOk ?? (s.quote.ema > 0 && !s.quote.stale);
}

/** The 9EMA pill: value text, mode tag, and the loading state. */
export function emaPill(s: Pick<OTState, 'quote' | 'emaNote'>): { value: string; mode: 'closed' | 'live'; loading: boolean } {
  const loading = !!s.emaNote;
  return {
    value: loading ? 'loading 9EMA…' : s.quote.ema > 0 ? fmtPx(s.quote.ema) : '-',
    mode: s.quote.live ? 'live' : 'closed',
    loading,
  };
}

/** Server-side sizing for a BUY now (State.sizing). */
export interface OTSizing { qty: number; limit: number; stop: number; position: number; risk: number; bound: string }

export const RISK_KEY = 'orderTicket.riskChoice';
export type RiskChoice = 'low' | 'mid' | 'high';

/** Stored risk choice; default the middle preset. */
export function parseRiskChoice(v: string | null | undefined): RiskChoice {
  return v === 'low' || v === 'high' ? v : 'mid';
}

/** The USD risk for a choice (server defaults 25 / 50 / 100). */
export function riskFor(c: RiskChoice, s: Pick<OTSettings, 'riskLow' | 'riskMid' | 'riskHigh'>): number {
  if (c === 'low') return s.riskLow || 25;
  if (c === 'high') return s.riskHigh || 100;
  return s.riskMid || 50;
}

/** The % risk for a choice (server defaults 1 / 2 / 3). */
export function riskPctFor(c: RiskChoice, s: Pick<OTSettings, 'riskPctLow' | 'riskPctMid' | 'riskPctHigh'>): number {
  if (c === 'low') return s.riskPctLow || 1;
  if (c === 'high') return s.riskPctHigh || 3;
  return s.riskPctMid || 2;
}

/**
 * How the risk buttons size now. mode '%' only when the user chose % AND TWS
 * gave a USD rate (funds.haveUsd); otherwise the $ presets, with a note when %
 * was chosen but is unavailable (a rate is never guessed).
 */
export interface RiskView { mode: '$' | '%'; wantPct: boolean; netLiqUsd: number; note: string }
export function riskView(s: Pick<OTState, 'settings'> & { funds?: OTFunds | null }): RiskView {
  const wantPct = s.settings.riskMode === '%';
  const f = s.funds;
  if (wantPct && f && f.haveUsd && (f.netLiqUsd ?? 0) > 0) return { mode: '%', wantPct, netLiqUsd: f.netLiqUsd!, note: '' };
  const note = wantPct ? `% risk off: ${(f && f.usdNote) || 'no account values from TWS yet'}; sizing with the $ presets` : '';
  return { mode: '$', wantPct, netLiqUsd: 0, note };
}

/** USD risk for pct % of the USD net liquidation, to the cent. */
export function pctRiskUsd(pct: number, netLiqUsd: number): number {
  return pct > 0 && netLiqUsd > 0 ? Math.round(pct * netLiqUsd) / 100 : 0;
}

/** The base (pre-A+) USD risk for a choice in the current view. */
export function baseRiskUsd(c: RiskChoice, s: OTSettings, v: RiskView): number {
  return v.mode === '%' ? pctRiskUsd(riskPctFor(c, s), v.netLiqUsd) : riskFor(c, s);
}

/** "3% · $105" in % mode, "$50" in $ mode (pct null). */
export function riskLabel(pct: number | null, usd: number): string {
  const d = `$${Math.round(usd)}`;
  return pct === null ? d : `${+pct.toFixed(2)}% · ${d}`;
}

/** The choice the Power Setup lands on: the % button set to 3, else high. */
export function powerRiskChoice(s: OTSettings): RiskChoice {
  for (const c of ['low', 'mid', 'high'] as const) if (riskPctFor(c, s) === 3) return c;
  return 'high';
}

/**
 * The sizing line next to the risk row: "= N sh · stop 2.41 · ≈ $X position"
 * (+ the cap that trimmed it); with a Shares override "= N sh · $ risk ≈ X".
 */
export function sizingLine(z: OTSizing | null | undefined, sharesOverride: boolean): string {
  if (!z || !(z.qty > 0)) return '= - sh';
  if (sharesOverride) return `= ${z.qty} sh · $ risk ≈ ${Math.round(z.risk)} · stop ${fmtPx(z.stop)}`;
  const s = `= ${z.qty} sh · stop ${fmtPx(z.stop)} · ≈ ${fmtUsd(z.position)} position`;
  return z.bound ? `${s} (capped: ${z.bound})` : s;
}

/** The working-order row's follow text (server label; legacy "~" fallback). */
export function followText(o: Pick<OTOrder, 'following' | 'followLabel'>): string {
  if (!o.following) return '';
  return o.followLabel || 'follows 9EMA';
}

/** A valid follow interval (2-60 s), else null. */
export function parseFollowSec(v: string): number | null {
  const n = Number(v.trim());
  return Number.isInteger(n) && n >= 2 && n <= 60 ? n : null;
}

/** The fast-drop auto-cancel guard's state from settings (default ON, 3 % / 60 s / 1 %). */
export function dropCancel(s: Pick<OTSettings, 'dropCancelOff' | 'dropPct' | 'dropSec' | 'nearPct'>) {
  return { on: !s.dropCancelOff, pct: s.dropPct || 3, sec: s.dropSec || 60, near: s.nearPct ?? 1 };
}

/** Payload for the dropcancel action (numbers validated server-side). */
export function dropCancelBody(on: boolean, pct: number, sec: number, near: number) {
  return { on, dropPct: pct, dropSec: sec, nearPct: near };
}

/** The breakeven rule from settings (default ON at +5 %, exact entry). */
export function breakeven(s: Pick<OTSettings, 'beOff' | 'bePct' | 'beOffsetPct'>) {
  return { on: !s.beOff, pct: s.bePct || 5, offset: s.beOffsetPct ?? 0 };
}

/** Payload for the breakeven action (pct 0 = keep the thresholds). */
export function breakevenBody(on: boolean, pct: number, offset: number) {
  return { on, bePct: pct, beOffsetPct: offset };
}

/** One big stat tile above BUY/SELL. */
export interface StatTile {
  key: 'shares' | 'stop' | 'position' | 'risk' | 'maxLong' | 'maxShort' | 'shortable';
  cap: string;
  value: string;
  sub: string;
  tag: string; // e.g. "manual"
  cls: '' | 'amber' | 'red' | 'muted';
  title: string;
}

/**
 * The stat-tile view-model (presentation only; numbers come from state.sizing
 * and state.preview): SHARES | STOP | POSITION | RISK | MAX LONG | MAX SHORT.
 */
export function statTiles(
  s: Pick<OTState, 'sym' | 'sizing' | 'preview'> & Partial<Pick<OTState, 'short'>>,
  o: { sharesOverride: boolean; slOn: boolean; riskUsd: number; riskPct?: number | null },
): StatTile[] {
  const z = s.sizing && s.sizing.qty > 0 ? s.sizing : null;
  const p = s.preview && s.sym && s.preview.sym === s.sym ? s.preview : null;
  const t = (key: StatTile['key'], cap: string, value: string, extra: Partial<StatTile> = {}): StatTile => ({
    key, cap, value, sub: '', tag: '', cls: '', title: '', ...extra,
  });
  const shares = t('shares', 'Shares', z ? String(z.qty) : '—', {
    tag: o.sharesOverride ? 'manual' : '',
    ...(z && z.bound ? { cls: 'amber' as const, sub: `capped: ${z.bound}` } : {}),
  });
  const stopPct = z && z.limit > 0 ? (z.stop / z.limit - 1) * 100 : 0;
  const stop = !o.slOn
    ? t('stop', 'Stop', 'no SL', { cls: 'amber', title: 'soft stop off: risk sizing is notional' })
    : t('stop', 'Stop', z ? fmtPx(z.stop) : '—', { sub: z ? `${stopPct < 0 ? '−' : '+'}${Math.abs(stopPct).toFixed(1)}%` : '' });
  const position = t('position', 'Position', z ? fmtUsd(z.position) : '—');
  const notional = o.slOn ? {} : { sub: 'notional', cls: 'amber' as const };
  const risk = o.sharesOverride
    ? t('risk', 'Risk', z ? `≈ $${Math.round(z.risk)}` : '—', notional)
    : o.riskPct != null
      ? t('risk', 'Risk', `$${Math.round(o.riskUsd)}`, { sub: `${riskLabel(o.riskPct, o.riskUsd).split(' · ')[0]} of account`, ...notional })
      : t('risk', 'Risk', `$${o.riskUsd}`, notional);
  const cur = (p && p.currency) || 'USD';
  const max = (key: 'maxLong' | 'maxShort', cap: string, side: OTPreviewSide | undefined): StatTile => {
    if (!p || !side) return t(key, cap, '—', { cls: 'muted', title: 'margin preview pending' });
    if (!side.ok) return t(key, cap, side.rejected ? 'rejected' : 'n/a', { cls: 'muted', title: side.note || 'margin check unavailable' });
    const per = side.perShare > 0 ? `${curSym(cur).trim()}${side.perShare.toFixed(2)}/sh margin` : 'margin/sh —';
    return t(key, cap, String(side.maxShares), {
      cls: z && z.qty > side.maxShares ? 'red' : '',
      title: `${per} (what-if for ${p.qty} sh at ${fmtPx(key === 'maxLong' ? p.longPx : p.shortPx)})`,
    });
  };
  return [shares, stop, position, risk, max('maxLong', 'Max long', p?.buy), max('maxShort', 'Max short', p?.sell), shortTile(s.short, z ? z.qty : 0)];
}

/** IBKR short availability for the shown symbol (State.short). */
export interface OTShort {
  known: boolean; shortable: boolean; avail: number; capped: boolean; fee: number;
  asOf: number; ageMin: number; stale: boolean; source: string; note: string;
  live?: boolean; liveShares?: number; liveIndicator?: number; liveAt?: number; liveErr?: string; fileAvail?: number; fileKnown?: boolean;
}

/** 950 -> "950", 1500 -> "1.5K", 150000 -> "150K", 1250000 -> "1.25M" (the server's fmtCompact). */
export function fmtCompact(n: number): string {
  const trim = (v: string) => v.replace(/\.?0+$/, '');
  if (n >= 1_000_000) return `${trim((n / 1e6).toFixed(2))}M`;
  if (n >= 10_000) return `${Math.round(n / 1000)}K`;
  if (n >= 1000) return `${trim((n / 1000).toFixed(1))}K`;
  return String(n);
}

/**
 * The SHORTABLE tile: compact shares + "fee x%" (+ age when > 15 min); red
 * "none" when IBKR lists none, amber when fewer than the chosen shares, "—"
 * when unknown. Tooltip: exact shares, fee, source and timestamp.
 */
export function shortTile(si: OTShort | null | undefined, qty: number): StatTile {
  const base: StatTile = { key: 'shortable', cap: 'Shortable', value: '—', sub: '', tag: '', cls: 'muted', title: '' };
  if (!si) return { ...base, title: 'short availability not available' };
  const when = si.asOf > 0 ? new Date(si.asOf * 1000).toLocaleString() : 'never';
  const fileLine = si.fileKnown || (!si.live && si.known)
    ? `IBKR file: ${si.capped && !si.live ? '>' : ''}${(si.fileAvail ?? si.avail).toLocaleString('en-US')} sh, fee ${si.fee}%, as of ${when}`
    : `IBKR file: ${si.asOf > 0 ? 'symbol not listed' : 'not loaded'}`;
  const liveLine = si.live
    ? `TWS live: ${si.avail.toLocaleString('en-US')} sh, indicator ${si.liveIndicator ?? '-'}, at ${si.liveAt ? new Date(si.liveAt).toLocaleTimeString() : '-'}`
    : `TWS live: ${si.liveErr ? si.liveErr : 'no tick in the last 60 s'}`;
  const title = `${liveLine}\n${fileLine}`;
  // file fallback: say so, with its age
  const src = si.live ? '' : si.known ? `file${si.ageMin > 0 ? `, ${si.ageMin}m old` : ''}` : '';
  const tag = si.live ? 'live' : '';
  if (!si.known) return { ...base, sub: si.note, title };
  if (!si.shortable) return { ...base, value: 'none', cls: 'red', tag, sub: src, title };
  if (si.live && !(si.avail > 0)) {
    // indicator only (no count tick): shortable but count unknown
    return { ...base, value: 'yes', cls: '', tag, sub: `fee ${si.fee}%`, title };
  }
  return {
    ...base,
    value: `${fmtCompact(si.avail)}${si.capped && !si.live ? '+' : ''}`,
    tag,
    sub: si.live ? `fee ${si.fee}%` : `fee ${si.fee}% · ${src}`,
    cls: !(si.capped && !si.live) && qty > 0 && si.avail < qty ? 'amber' : '',
    title,
  };
}

/** Estimated round-trip commission per share (Go: commissionPerShare). */
export const COMMISSION_PER_SHARE = 0.006;

export type PresetName = 'custom' | 'power' | 'newsflag';

/** The active preset for the next orders (default Custom). */
export function activePreset(s: Pick<OTSettings, 'preset'>): PresetName {
  return s.preset === 'power' || s.preset === 'newsflag' ? s.preset : 'custom';
}

/**
 * Payload for the preset action: the preset plus Custom's 5m-trail and
 * entry-timeout defaults (kept unless overridden).
 */
export function presetBody(
  name: PresetName,
  s: Pick<OTSettings, 'trail5m' | 'entryTimeoutMin'>,
  over: { trail5m?: boolean; entryTimeoutMin?: number } = {},
) {
  return { preset: name, trail5m: over.trail5m ?? !!s.trail5m, entryTimeoutMin: over.entryTimeoutMin ?? (s.entryTimeoutMin || 0) };
}
