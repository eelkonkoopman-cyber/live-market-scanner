import { useCallback, useEffect, useRef, useState } from 'react';
import {
  act,
  accountBadge,
  availLabel,
  emaAge,
  emaPill,
  dropCancel,
  dropCancelBody,
  activePreset,
  presetBody,
  breakeven,
  breakevenBody,
  followText,
  emaUsable,
  connLabel,
  fetchState,
  fmtPx,
  marginView,
  PREVIEW_DEBOUNCE_MS,
  statTiles,
  stopText,
  stopToggleable,
  parseShares,
  parseTpChoice,
  prepareBody,
  vwapxBody,
  vwapxButton,
  vwapxLine,
  vwapxSizing,
  vwapxSizingLine,
  actionState,
  ONLY_TICKET_ORDERS,
  parseStopOn,
  parseStopPct,
  SL_KEY,
  TP_KEY,
  tpPctFor,
  type TPChoice,
  parseRiskChoice,
  RISK_KEY,
  riskFor,
  riskPctFor,
  riskView,
  baseRiskUsd,
  riskLabel,
  powerRiskChoice,
  type RiskChoice,
  SHARES_KEY,
  type OTState,
} from '../orderTicket';
import { psAplusRisk } from '../powerSetup';

// OrderPane is the Order Ticket (?pane=order): a ~320x260 always-on-top window
// (scannerwin pins it) following the ticker shown in TWS. BUY / SELL only
// PREPARE an order; the server sends nothing until the Confirm click.
const POLL_MS = 500;

function load(key: string, def: string): string {
  try {
    return localStorage.getItem(key) ?? def;
  } catch {
    return def;
  }
}

function save(key: string, v: string) {
  try {
    localStorage.setItem(key, v);
  } catch {
    /* per-viewer convenience only */
  }
}

export function OrderPane() {
  const [stopEdit, setStopEdit] = useState<{ id: number; val: string } | null>(null);
  const [st, setSt] = useState<OTState | null>(null);
  const [err, setErr] = useState('');
  const [riskChoice, setRiskChoice] = useState<RiskChoice>(() => parseRiskChoice(load(RISK_KEY, 'mid')));
  const chooseRisk = (c: RiskChoice) => {
    setRiskChoice(c);
    save(RISK_KEY, c);
  };
  const [riskIn, setRiskIn] = useState<[string, string, string]>(['', '', '']);
  const [riskPctIn, setRiskPctIn] = useState<[string, string, string]>(['', '', '']);
  const [riskModeIn, setRiskModeIn] = useState<'$' | '%'>('$');
  const riskUsdRef = useRef(0);
  // the A+ setup ('SYM@barMs') whose 1.5x risk the user switched off ('' = none): an override, never silent
  const [aplusOff, setAplusOff] = useState('');
  const [sharesIn, setSharesIn] = useState(() => load(SHARES_KEY, ''));
  const [busy, setBusy] = useState(false);
  const [showSet, setShowSet] = useState(false);
  const [setSaved, setSetSaved] = useState(false);
  // Esc closes the settings panel
  useEffect(() => {
    if (!showSet) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setShowSet(false);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [showSet]);
  const [port, setPort] = useState('');
  const [cid, setCid] = useState('');
  const [maxUsd, setMaxUsd] = useState('');
  const [tpLow, setTpLow] = useState('');
  const [tpHigh, setTpHigh] = useState('');
  const [stopPct, setStopPct] = useState('');
  const [staleIn, setStaleIn] = useState('');
  const [followIn, setFollowIn] = useState('');
  const [timeoutIn, setTimeoutIn] = useState('');
  const [dropIn, setDropIn] = useState<[string, string, string]>(['', '', '']);
  const [beIn, setBeIn] = useState<[string, string]>(['', '']);
  const [stopFollow, setStopFollow] = useState(false);
  const [tpChoice, setTpChoice] = useState<TPChoice>(() => parseTpChoice(load(TP_KEY, 'high')));
  const [slOn, setSlOn] = useState<boolean>(() => parseStopOn(load(SL_KEY, 'on')));
  const chooseSl = (on: boolean) => {
    setSlOn(on);
    save(SL_KEY, on ? 'on' : 'off');
  };
  const [slPctIn, setSlPctIn] = useState<string | null>(null); // null = not edited: show the server's value
  const chooseTp = (c: TPChoice) => {
    setTpChoice(c);
    save(TP_KEY, c);
  };

  useEffect(() => {
    document.title = 'Order Ticket';
    let alive = true;
    const tick = () =>
      fetchState()
        .then((s) => {
          if (!alive) return;
          setSt(s);
          setErr((e) => (e === 'server unreachable' ? '' : e));
        })
        .catch(() => {
          if (alive) setErr('server unreachable');
        });
    tick();
    const id = window.setInterval(tick, POLL_MS);
    return () => {
      alive = false;
      window.clearInterval(id);
    };
  }, []);

  // Tell the server the size to preview margin for, debounced.
  useEffect(() => {
    const id = window.setTimeout(() => {
      void act('previewsize', { positionUSD: 0, shares: parseShares(sharesIn), riskUsd: riskUsdRef.current }).catch(() => undefined);
    }, PREVIEW_DEBOUNCE_MS);
    return () => window.clearTimeout(id);
  }, [riskChoice, sharesIn, st?.settings.riskLow, st?.settings.riskMid, st?.settings.riskHigh, st?.settings.riskMode, st?.settings.riskPctLow, st?.settings.riskPctMid, st?.settings.riskPctHigh, st?.funds?.netLiqUsd, st?.funds?.haveUsd, st?.settings.preset, st?.sym, st?.psGrade?.aplus, st?.psGrade?.barMs, aplusOff]);

  // SL% edited in the main row: save through stopcfg, debounced.
  useEffect(() => {
    if (slPctIn === null) return;
    const v = parseStopPct(slPctIn);
    if (v === null) return;
    const id = window.setTimeout(() => {
      void act('stopcfg', { stopPct: v, stopFollow: !!st?.settings.stopFollow })
        .then((r) => {
          if (r.state) setSt(r.state);
          setErr(r.error ?? '');
          setSlPctIn(null);
        })
        .catch(() => undefined);
    }, PREVIEW_DEBOUNCE_MS);
    return () => window.clearTimeout(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [slPctIn]);

  const run = useCallback(async (action: string, body: Record<string, unknown> = {}) => {
    setBusy(true);
    try {
      const r = await act(action, body);
      if (r.state) setSt(r.state);
      setErr(r.error ?? '');
    } catch (e) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  }, []);

  const saveShares = (v: string) => {
    setSharesIn(v);
    save(SHARES_KEY, v);
  };

  if (!st) {
    return (
      <div className="oticket">
        <div className="ot-status">{err || 'loading...'}</div>
      </div>
    );
  }

  const connected = st.conn === 'connected';
  const badge = accountBadge(st.account);
  const q = st.quote;
  const rv = riskView(st);
  const baseRisk = baseRiskUsd(riskChoice, st.settings, rv);
  const basePct = rv.mode === '%' ? riskPctFor(riskChoice, st.settings) : null;
  const grade = st.psGrade;
  const aplusKey = grade?.aplus ? `${st.sym}@${grade.barMs}` : '';
  const aplus = psAplusRisk(baseRisk, grade, activePreset(st.settings) === 'power', aplusKey !== '' && aplusOff === aplusKey);
  const riskUsd = aplus.risk;
  riskUsdRef.current = riskUsd;
  const riskPct = basePct === null ? null : aplus.applied && grade ? basePct * grade.mult : basePct;
  const orders = st.orders ?? [];
  const active = orders.filter((o) => o.active);
  const shares = parseShares(sharesIn);
  const age = emaAge(q, st.settings.staleMin);
  const pill = emaPill(st);
  const canTrade = connected && !!st.sym && emaUsable(st) && (shares > 0 || riskUsd > 0) && !busy;
  const pending = st.pending;
  const vx = vwapxButton(st.vwapx, shares > 0 || riskUsd > 0, connected && !!st.sym, busy);
  const vxLine = vwapxLine(st.vwapx);
  const vxb = vwapxButton(st.vwapxBear, shares > 0 || riskUsd > 0, connected && !!st.sym, busy, 'SELL');
  const vxbLine = vwapxLine(st.vwapxBear);
  const vxSize = vwapxSizingLine(vwapxSizing(st.vwapx, riskUsd, shares, st.settings.maxPositionUSD), riskUsd, st.settings.maxPositionUSD);
  const vxbSize = vwapxSizingLine(vwapxSizing(st.vwapxBear, riskUsd, shares, st.settings.maxPositionUSD), riskUsd, st.settings.maxPositionUSD);
  const mv = pending ? marginView(pending) : null;
  const acts = actionState(st, busy);
  const avail = availLabel(st.funds);

  const openSettings = () => {
    setPort(String(st.settings.port));
    setCid(String(st.settings.clientId));
    setMaxUsd(String(st.settings.maxPositionUSD));
    setTpLow(String(st.settings.tpLowPct || 1));
    setRiskIn([String(riskFor('low', st.settings)), String(riskFor('mid', st.settings)), String(riskFor('high', st.settings))]);
    setRiskPctIn([String(riskPctFor('low', st.settings)), String(riskPctFor('mid', st.settings)), String(riskPctFor('high', st.settings))]);
    setRiskModeIn(st.settings.riskMode === '%' ? '%' : '$');
    setTpHigh(String(st.settings.tpHighPct || 10));
    setStopPct(String(st.settings.stopPct || 1));
    setStaleIn(String(st.settings.staleMin || 15));
    setFollowIn(String(st.settings.followSec || 5));
    setTimeoutIn(String(st.settings.entryTimeoutMin || 0));
    {
      const d = dropCancel(st.settings);
      setDropIn([String(d.pct), String(d.sec), String(d.near)]);
      const b = breakeven(st.settings);
      setBeIn([String(b.pct), String(b.offset)]);
    }
    setStopFollow(!!st.settings.stopFollow);
    setSetSaved(false);
    setShowSet((v) => !v);
  };

  const saveSettings = async () => {
    if (connected) {
      await run('max', { max: Number(maxUsd) });
      await run('tp', { tpLowPct: Number(tpLow), tpHighPct: Number(tpHigh) });
      await run('risk', { riskLow: Number(riskIn[0]), riskMid: Number(riskIn[1]), riskHigh: Number(riskIn[2]) });
      await run('riskpct', { riskPctLow: Number(riskPctIn[0]), riskPctMid: Number(riskPctIn[1]), riskPctHigh: Number(riskPctIn[2]) });
      await run('riskmode', { riskMode: riskModeIn });
      await run('stopcfg', { stopPct: Number(stopPct), stopFollow });
      await run('stalemin', { staleMin: Number(staleIn) });
      await run('followsec', { followSec: Number(followIn) });
      await run('preset', presetBody(activePreset(st.settings), st.settings, { entryTimeoutMin: Number(timeoutIn) }));
      await run('dropcancel', dropCancelBody(dc.on, Number(dropIn[0]), Number(dropIn[1]), Number(dropIn[2])));
      await run('breakeven', breakevenBody(be.on, Number(beIn[0]), Number(beIn[1])));
      return;
    }
    await run('settings', {
      settings: {
        host: st.settings.host,
        port: Number(port),
        clientId: Number(cid),
        maxPositionUSD: Number(maxUsd),
        tpLowPct: Number(tpLow),
        tpHighPct: Number(tpHigh),
        riskLow: Number(riskIn[0]),
        riskMid: Number(riskIn[1]),
        riskHigh: Number(riskIn[2]),
        riskMode: riskModeIn,
        riskPctLow: Number(riskPctIn[0]),
        riskPctMid: Number(riskPctIn[1]),
        riskPctHigh: Number(riskPctIn[2]),
        stopPct: Number(stopPct),
        stopFollow,
        staleMin: Number(staleIn),
        followSec: Number(followIn),
        preset: activePreset(st.settings),
        trail5m: !!st.settings.trail5m,
        entryTimeoutMin: Number(timeoutIn),
        dropCancelOff: !dc.on,
        dropPct: Number(dropIn[0]),
        dropSec: Number(dropIn[1]),
        nearPct: Number(dropIn[2]),
        beOff: !be.on,
        bePct: Number(beIn[0]),
        beOffsetPct: Number(beIn[1]),
      },
    });
  };
  const tpPct = tpPctFor(tpChoice, st.settings);
  const dc = dropCancel(st.settings);
  const be = breakeven(st.settings);

  return (
    <div className="oticket">
      <div className="ot-head">
        <span className={`ot-sym${st.sym ? '' : ' none'}`} title={st.symSource ? `symbol from ${st.symSource}` : 'no symbol'}>
          {st.sym || '----'}
        </span>
        <span className="ot-src">{st.symSource}</span>
        {badge && (
          <span className={`ot-badge ${badge === 'LIVE' ? 'live' : 'paper'}`} title={st.account}>
            {badge}
          </span>
        )}
        {activePreset(st.settings) === 'power' && (
          <span className="ot-badge ot-ps" title="Power Setup preset: 9EMA entry (cancel if unfilled 15m), SL 6% under 9EMA, TP 3R, BE +7.5%, 5m trail exit">
            Power Setup
          </span>
        )}
        {activePreset(st.settings) === 'newsflag' && (
          <span className="ot-badge ot-ps" title="News Flag preset: 9EMA entry, usual stop and risk, no breakeven, exit on the 5m trail (previous 5m bar's low)">
            News Flag
          </span>
        )}
        <span className="ot-spacer" />
        <button className="ot-mini iconbtn tile-btn" onClick={openSettings} title="Connection settings and max position $" aria-label="Settings">
          ⚙
        </button>
        {connected || st.conn === 'connecting' ? (
          <button className="ot-mini iconbtn tile-btn" onClick={() => run('disconnect')}>
            Disconnect
          </button>
        ) : (
          <button className="ot-mini iconbtn tile-btn" onClick={() => run('connect')}>
            Connect
          </button>
        )}
      </div>
      <div className="ot-quote">
        <span className="ot-qi">
          <span className="ot-lbl">Last</span> <b>{fmtPx(q.price)}</b>
        </span>
        <span
          className={`ot-qi${age.cls === 'red' ? ' ot-stale ot-age-red' : age.cls === 'amber' ? ' ot-stale' : ''}`}
          title={
            pill.loading
              ? `${st.emaNote}: BUY/SELL wait until the bars are complete`
              : age.cls === 'red'
                ? `newest closed 1m bar older than ${st.settings.staleMin || 15} min: BUY/SELL blocked (CLOSE/CANCEL still work)`
                : pill.mode === 'live'
                  ? '9 EMA incl. the live (forming) 1m candle'
                  : '9 EMA over closed 1m candles'
          }
        >
          <span className="ot-lbl">9EMA {pill.mode === 'live' ? 'live' : 'closed'}</span> <b className={pill.loading ? 'ot-loading' : ''}>{pill.value}</b>
          {!pill.loading && age.text ? <span className="ot-stale-sfx"> · {age.text}</span> : ''}
        </span>
        {avail && (
          <span className="ot-qi ot-avail" title="IB AvailableFunds (account summary)">
            {avail}
          </span>
        )}
      </div>
      {vxLine && (
        <div className="ot-vxline" title="9EMA VWAP bull cross: BUY LMT at the cross candle close, real IB stop under its low, TP +1% (times ET)">
          {vxLine}
        </div>
      )}
      {vxbLine && (
        <div className="ot-vxline" title="9EMA VWAP bear cross: SELL SHORT LMT at the cross candle close, real IB buy stop over its high, TP -1% (times ET)">
          {vxbLine}
        </div>
      )}
      {showSet && (
        <div className="ot-setwrap" role="dialog" aria-label="Order ticket settings" onClick={(e) => e.target === e.currentTarget && setShowSet(false)}>
          <div className="ot-setpanel">
            <div className="ot-sethead">
              <span className="ot-settitle">Settings</span>
              <button className="ot-setclose" aria-label="close settings (Esc)" title="close (Esc)" onClick={() => setShowSet(false)}>
                ✕
              </button>
            </div>
            <div className="ot-setgrid">
              <section className="ot-setsec">
                <h3>Connection</h3>
                <div className="ot-setrow">
                  <span className="ot-setlbl">Port</span>
                  <span className="ot-setin">
                    <input value={port} onChange={(e) => setPort(e.target.value)} disabled={connected} size={5} />
                  </span>
                </div>
                <div className="ot-setrow">
                  <span className="ot-setlbl">Client id</span>
                  <span className="ot-setin">
                    <input value={cid} onChange={(e) => setCid(e.target.value)} disabled={connected} size={3} />
                  </span>
                </div>
                {connected && <div className="ot-sethint">Disconnect to change port / client id.</div>}
                <div className="ot-setrow">
                  <span className="ot-setlbl">Max position</span>
                  <span className="ot-setin">
                    <span className="ot-unit">$</span>
                    <input value={maxUsd} onChange={(e) => setMaxUsd(e.target.value)} size={6} />
                  </span>
                </div>
              </section>

              <section className="ot-setsec">
                <h3>Sizing</h3>
                <div className="ot-setrow" title="risk presets in USD (1-5000)">
                  <span className="ot-setlbl">Risk low / mid / high</span>
                  <span className="ot-setin">
                    {[0, 1, 2].map((i) => (
                      <span key={i} className="ot-setin">
                        <span className="ot-unit">$</span>
                        <input
                          value={riskIn[i]}
                          onChange={(e) => setRiskIn((r) => r.map((x, j) => (j === i ? e.target.value : x)) as [string, string, string])}
                          size={4}
                        />
                      </span>
                    ))}
                  </span>
                </div>
                <div className="ot-setrow" title="risk % of the account's NetLiquidation (0.1-10), converted to USD with TWS's USD rate">
                  <span className="ot-setlbl">Risk % low / mid / high</span>
                  <span className="ot-setin">
                    {[0, 1, 2].map((i) => (
                      <span key={i} className="ot-setin">
                        <input
                          value={riskPctIn[i]}
                          onChange={(e) => setRiskPctIn((r) => r.map((x, j) => (j === i ? e.target.value : x)) as [string, string, string])}
                          size={3}
                        />
                        <span className="ot-unit">%</span>
                      </span>
                    ))}
                  </span>
                </div>
                <div className="ot-setrow" title="risk buttons in USD or in % of the account (the Power Setup preset switches to %)">
                  <span className="ot-setlbl">Risk mode</span>
                  <div className="seg" role="group" aria-label="risk mode">
                    {(['$', '%'] as const).map((m) => (
                      <button key={m} className={`segbtn${riskModeIn === m ? ' on' : ''}`} aria-pressed={riskModeIn === m} onClick={() => setRiskModeIn(m)}>
                        {m}
                      </button>
                    ))}
                  </div>
                </div>
                {riskModeIn === '%' && !st.funds?.haveUsd && (
                  <div className="ot-sethint">{st.funds?.usdNote || 'no account values from TWS yet'}: % mode stays off (the $ presets size) until TWS reports the USD rate.</div>
                )}
                <div className="ot-sethint">Shares = risk ÷ (limit − planned soft stop).</div>
              </section>

              <section className="ot-setsec">
                <h3>Entry &amp; 9EMA</h3>
                <div className="ot-setrow">
                  <span className="ot-setlbl">9EMA</span>
                  <div className="seg" role="group" aria-label="9EMA mode" title="closed candles (default) or incl. the live candle, like TWS's chart EMA; saved at once">
                    <button className={`segbtn${st.settings.emaLive ? '' : ' on'}`} aria-pressed={!st.settings.emaLive} onClick={() => run('emalive', { on: false })}>
                      closed candles
                    </button>
                    <button className={`segbtn${st.settings.emaLive ? ' on' : ''}`} aria-pressed={!!st.settings.emaLive} onClick={() => run('emalive', { on: true })}>
                      incl. live candle
                    </button>
                  </div>
                </div>
                <div className="ot-setrow" title="Custom preset: cancel an entry not filled within this many minutes (0 = off; Power Setup uses 15)">
                  <span className="ot-setlbl">Entry timeout</span>
                  <span className="ot-setin">
                    <input value={timeoutIn} onChange={(e) => setTimeoutIn(e.target.value)} size={3} />
                    <span className="ot-unit">min</span>
                  </span>
                </div>
                <div className="ot-setrow" title="exit when price breaks the previous completed 5-minute bar's low (high for a short); Custom preset, saved at once">
                  <span className="ot-setlbl">5m trail exit</span>
                  <div className="seg" role="group" aria-label="5m trail exit">
                    <button className={`segbtn${st.settings.trail5m ? ' on' : ''}`} aria-pressed={!!st.settings.trail5m} onClick={() => run('preset', presetBody(activePreset(st.settings), st.settings, { trail5m: true }))}>
                      on
                    </button>
                    <button className={`segbtn${st.settings.trail5m ? '' : ' on'}`} aria-pressed={!st.settings.trail5m} onClick={() => run('preset', presetBody(activePreset(st.settings), st.settings, { trail5m: false }))}>
                      off
                    </button>
                  </div>
                </div>
                <div className="ot-setrow" title="a working entry is re-priced to the 9EMA this often (2-60 s; 60 ~ once per bar)">
                  <span className="ot-setlbl">Follow every</span>
                  <span className="ot-setin">
                    <input value={followIn} onChange={(e) => setFollowIn(e.target.value)} size={3} />
                    <span className="ot-unit">s</span>
                  </span>
                </div>
                <div className="ot-setrow" title="BUY/SELL allowed while the 9EMA's newest closed 1m bar is at most this old (1-120 min)">
                  <span className="ot-setlbl">9EMA max age</span>
                  <span className="ot-setin">
                    <input value={staleIn} onChange={(e) => setStaleIn(e.target.value)} size={3} />
                    <span className="ot-unit">min</span>
                  </span>
                </div>
                <div className="ot-sethint">Older closed bar than this blocks BUY/SELL (CLOSE/CANCEL still work).</div>
              </section>

              <section className="ot-setsec">
                <h3>Take profit</h3>
                <div className="ot-setrow">
                  <span className="ot-setlbl">TP low / high</span>
                  <span className="ot-setin">
                    <input value={tpLow} onChange={(e) => setTpLow(e.target.value)} size={3} title="take-profit preset 1 (0.1-50)" />
                    <span className="ot-unit">%</span>
                    <input value={tpHigh} onChange={(e) => setTpHigh(e.target.value)} size={3} title="take-profit preset 2 (0.1-50)" />
                    <span className="ot-unit">%</span>
                  </span>
                </div>
              </section>

              <section className="ot-setsec">
                <h3>Stop loss</h3>
                <div className="ot-setrow">
                  <span className="ot-setlbl">SL distance</span>
                  <span className="ot-setin">
                    <input value={stopPct} onChange={(e) => setStopPct(e.target.value)} size={3} title="soft stop below/above the 9EMA (0.1-20)" />
                    <span className="ot-unit">%</span>
                  </span>
                </div>
                <div className="ot-setrow">
                  <span className="ot-setlbl">SL mode</span>
                  <div className="seg" role="group" aria-label="stop follows 9EMA" title="after each closed bar the stop follows the 9EMA (raise-only long / lower-only short)">
                    <button className={`segbtn${stopFollow ? '' : ' on'}`} aria-pressed={!stopFollow} onClick={() => setStopFollow(false)}>
                      fixed
                    </button>
                    <button className={`segbtn${stopFollow ? ' on' : ''}`} aria-pressed={stopFollow} onClick={() => setStopFollow(true)}>
                      follows 9EMA
                    </button>
                  </div>
                </div>
                <div className="ot-setrow" title="move the soft stop to entry (+ offset %, 0-5) once the price is this % in your favour (0.5-50)">
                  <span className="ot-setlbl">
                    Breakeven <span className={`ot-setstate${be.on ? ' on' : ''}`}>{be.on ? 'on' : 'off'}</span>
                  </span>
                  <span className="ot-setin">
                    <input value={beIn[0]} onChange={(e) => setBeIn((r) => [e.target.value, r[1]])} size={3} />
                    <span className="ot-unit">%</span>
                    <span className="ot-unit">+</span>
                    <input value={beIn[1]} onChange={(e) => setBeIn((r) => [r[0], e.target.value])} size={3} />
                    <span className="ot-unit">%</span>
                  </span>
                </div>
                <div className="ot-sethint">Stop to entry (+ offset) once price is trigger % in your favour. On/off in the main view.</div>
              </section>

              <section className="ot-setsec">
                <h3>
                  Drop-cancel <span className={`ot-setstate${dc.on ? ' on' : ''}`}>{dc.on ? 'on' : 'off'}</span>
                </h3>
                <div
                  className="ot-setrow"
                  title="cancel an unfilled entry when the 9EMA falls (rises for a short) and price moves fast into it: move % (0.5-20) within s (10-300), within % of the 9EMA (0-5)"
                >
                  <span className="ot-setlbl">Drop / window / near</span>
                  <span className="ot-setin">
                    {[0, 1, 2].map((i) => (
                      <span key={i} className="ot-setin">
                        <input
                          value={dropIn[i]}
                          onChange={(e) => setDropIn((r) => r.map((x, j) => (j === i ? e.target.value : x)) as [string, string, string])}
                          size={3}
                        />
                        <span className="ot-unit">{['%', 's', '%'][i]}</span>
                      </span>
                    ))}
                  </span>
                </div>
                <div className="ot-sethint">Cancel an unfilled entry when price moves this fast to within near % of a turning 9EMA.</div>
              </section>
            </div>
            <div className="ot-setfoot">
              {setSaved && !err && <span className="ot-setsaved">saved</span>}
              <button
                className="ot-mini ot-setsave"
                disabled={busy}
                onClick={() => {
                  setSetSaved(false);
                  void saveSettings().then(() => setSetSaved(true));
                }}
              >
                Save
              </button>
            </div>
          </div>
        </div>
      )}
      <div className="ot-entry">
        <span className="ot-tp-cap">Risk</span>
        <div className={`seg ot-risk${shares > 0 ? ' ot-derived' : ''}`} role="group" aria-label="risk" title="shares = risk / (limit - planned soft stop); presets in Set">
          {(['low', 'mid', 'high'] as const).map((c) => (
            <button key={c} className={`segbtn${riskChoice === c ? ' on' : ''}`} aria-pressed={riskChoice === c} onClick={() => chooseRisk(c)}>
              {rv.mode === '%' ? riskLabel(riskPctFor(c, st.settings), baseRiskUsd(c, st.settings, rv)) : `$${riskFor(c, st.settings)}`}
            </button>
          ))}
        </div>
        {aplus.eligible && grade && (
          <button
            className={`segbtn ot-aplus${aplus.applied ? ' on' : ''}`}
            aria-pressed={aplus.applied}
            onClick={() => setAplusOff(aplus.applied ? aplusKey : '')}
            title={`latest Power Setup is A+ (tight base ${grade.baseRange.toFixed(1)}%): risk x${grade.mult} while the Power Setup preset is on. Click to ${aplus.applied ? 'size at the plain' : 'restore the A+'} risk`}
          >
            {aplus.applied ? `A+ ×${grade.mult} → ${riskPct !== null ? riskLabel(riskPct, aplus.risk) : `$${aplus.risk}`}` : `A+ ×${grade.mult} off`}
          </button>
        )}
        {rv.note && (
          <span className="ot-risknote" style={{ color: 'var(--amber, #d29922)' }} title={rv.note}>
            % off
          </span>
        )}
        <label>
          Shares <input value={sharesIn} onChange={(e) => saveShares(e.target.value)} inputMode="numeric" size={5} placeholder="auto" />
        </label>
      </div>
      <div className="ot-tiles" title="sizing for a BUY now; MAX from the what-if margin preview (no order is placed)">
        {statTiles(st, { sharesOverride: shares > 0, slOn, riskUsd, riskPct }).map((tl) => (
          <div key={tl.key} className={`ot-tile${tl.cls ? ` ${tl.cls}` : ''}`} title={tl.title || undefined}>
            <span className="ot-tile-cap">
              {tl.cap}
              {tl.tag && <span className="ot-tile-tag">{tl.tag}</span>}
            </span>
            <span className="ot-tile-val">{tl.value}</span>
            {tl.sub && <span className="ot-tile-sub">{tl.sub}</span>}
          </div>
        ))}
      </div>
      {pending ? (
        <div className="ot-confirm">
          <div className="ot-confirm-text">{pending.text}</div>
          {pending.warn && <div className="ot-margin warn">{pending.warn}</div>}
          {mv && mv.text && <div className={`ot-margin ${mv.cls}`}>{mv.text}</div>}
          {mv && mv.useMax > 0 && (
            <button
              className="ot-mini"
              disabled={busy}
              onClick={() =>
                run('prepare', pending.kind === 'vwapx' ? vwapxBody(mv.useMax, pending.sym, 0, pending.side === 'SELL' ? 'SELL' : 'BUY') : prepareBody(pending.side as 'BUY' | 'SELL', 0, mv.useMax, pending.sym, pending.tpPct ?? tpPct, !pending.stopOff))
              }
            >
              Use max ({mv.useMax})
            </button>
          )}
          <button
            className={`ot-btn ${pending.side === 'BUY' ? 'buy' : 'sell'}`}
            disabled={busy || (mv ? !mv.canConfirm : false)}
            onClick={() => run('confirm', { token: pending.token, sym: pending.sym })}
          >
            Confirm
          </button>
          <button className="ot-btn" disabled={busy} onClick={() => run('abort')}>
            Cancel
          </button>
        </div>
      ) : (
        <div className="ot-sides">
          <div className="ot-tprow">
            <span className="ot-tp-cap">Preset</span>
            <div className="seg" role="group" aria-label="preset" title="Power Setup: 9EMA entry (cancel if unfilled 15m), SL 6% under the 9EMA, TP 3R, BE at +7.5%, 5m trail exit, no drop-cancel">
              {(['custom', 'power', 'newsflag'] as const).map((n) => (
                <button key={n} className={`segbtn${activePreset(st.settings) === n ? ' on' : ''}`} aria-pressed={activePreset(st.settings) === n} onClick={() => {
                  if (n === 'power') chooseRisk(powerRiskChoice(st.settings));
                  void run('preset', presetBody(n, st.settings));
                }}>
                  {n === 'power' ? 'Power Setup' : n === 'newsflag' ? 'News Flag' : 'Custom'}
                </button>
              ))}
            </div>
          </div>
          <div className="ot-tprow">
            <span className="ot-tp-cap">Take profit</span>
          {activePreset(st.settings) === 'power' ? (
            <span className="ot-tp-fixed" title="Power Setup: TP = entry + 3 x (entry - stop), re-priced with the entry">
              {(() => {
                const z = st.sizing && st.sizing.limit > 0 && st.sizing.stop > 0 ? st.sizing : null;
                if (!z) return 'TP 3R';
                const tp = z.limit + 3 * (z.limit - z.stop);
                return `TP 3R · +${((tp / z.limit - 1) * 100).toFixed(1)}% · ${fmtPx(tp)}`;
              })()}
            </span>
          ) : activePreset(st.settings) === 'newsflag' ? (
            <span className="ot-tp-fixed" title="News Flag: exit when price breaks the previous completed 5-minute bar's low (high for a short). Far +50% limit stays as the bracket's TP">
              5m trail · prev bar low
            </span>
          ) : (
          <div className="seg ot-tp" role="group" aria-label="take profit">
            {(['low', 'high'] as const).map((c) => (
              <button key={c} className={`segbtn ot-tp-btn${tpChoice === c ? ' on' : ''}`} aria-pressed={tpChoice === c} onClick={() => chooseTp(c)}>
                TP {tpPctFor(c, st.settings)}%
              </button>
            ))}
          </div>
          )}
          </div>
          <div className="ot-tprow ot-slrow">
            <span className="ot-tp-cap">Stop loss</span>
            <div className="seg" role="group" aria-label="stop loss">
              <button className={`segbtn${slOn ? '' : ' on'}`} aria-pressed={!slOn} onClick={() => chooseSl(false)}>
                SL off
              </button>
              <button className={`segbtn${slOn ? ' on' : ''}`} aria-pressed={slOn} onClick={() => chooseSl(true)}>
                SL on
              </button>
            </div>
            <label className="ot-slpct" title="soft stop distance from the 9EMA (0.1-20 %); follow option in Set">
              <span className="ot-lbl">SL%</span>
              <input
                value={slPctIn ?? String(st.settings.stopPct || 1)}
                onChange={(e) => setSlPctIn(e.target.value)}
                inputMode="decimal"
                size={3}
                disabled={!slOn}
              />
            </label>
            <div
              className="seg"
              role="group"
              aria-label="drop-cancel"
              title={`Cancel an unfilled entry when the 9EMA turns against it and price moves ${dc.pct}% in ${dc.sec} s to within ${dc.near}% of it (thresholds in Set)`}
            >
              <button className={`segbtn${dc.on ? ' on' : ''}`} aria-pressed={dc.on} onClick={() => run('dropcancel', dropCancelBody(true, 0, 0, 0))}>
                Drop-cancel on
              </button>
              <button className={`segbtn${dc.on ? '' : ' on'}`} aria-pressed={!dc.on} onClick={() => run('dropcancel', dropCancelBody(false, 0, 0, 0))}>
                off
              </button>
            </div>
            <div
              className="seg"
              role="group"
              aria-label="breakeven"
              title={`Move the soft stop to entry${be.offset ? ` +${be.offset}%` : ''} once the price is ${be.pct}% in your favour (never loosens; settings in Set)`}
            >
              <button className={`segbtn${be.on ? ' on' : ''}`} aria-pressed={be.on} onClick={() => run('breakeven', breakevenBody(true, 0, 0))}>
                BE on
              </button>
              <button className={`segbtn${be.on ? '' : ' on'}`} aria-pressed={!be.on} onClick={() => run('breakeven', breakevenBody(false, 0, 0))}>
                off
              </button>
            </div>
          </div>
          <button className="ot-btn buy" disabled={!canTrade} onClick={() => run('prepare', prepareBody('BUY', 0, shares, st.sym, tpPct, slOn, riskUsd))}>
            BUY
          </button>
          <button className="ot-btn sell" disabled={!canTrade} onClick={() => run('prepare', prepareBody('SELL', 0, shares, st.sym, tpPct, slOn, riskUsd))}>
            SELL
          </button>
          <button className="ot-btn buy ot-vwapx" disabled={!vx.enabled} title={vx.title} onClick={() => run('prepare', vwapxBody(shares, st.sym, riskUsd))}>
            {vx.label}
          </button>
          <button className="ot-btn sell ot-vwapx" disabled={!vxb.enabled} title={vxb.title} onClick={() => run('prepare', vwapxBody(shares, st.sym, riskUsd, 'SELL'))}>
            {vxb.label}
          </button>
          {vxSize && (
            <div className={`ot-vxsize ${vxSize.cls}`} title="What VWAP X would size to with the current Risk / Shares and max position">
              VWAP X {vxSize.text}
            </div>
          )}
          {vxbSize && (
            <div className={`ot-vxsize ${vxbSize.cls}`} title="What VWAP X↓ would size to with the current Risk / Shares and max position">
              VWAP X↓ {vxbSize.text}
            </div>
          )}
          <div className="ot-actrow">
            <button
              className="ot-btn close"
              disabled={!acts.canClose}
              title={`Flatten the ticket's ${st.sym} position (cancel entry + TP, then LMT exit). ${ONLY_TICKET_ORDERS}`}
              onClick={() => run('close', { sym: st.sym })}
            >
              CLOSE{st.closeQty ? ` ${st.closeQty}` : ''}
            </button>
            <button
              className="ot-btn cancelsym"
              disabled={!acts.canCancel}
              title={`Cancel the ticket's unfilled ${st.sym} entries (filled shares untouched). ${ONLY_TICKET_ORDERS}`}
              onClick={() => run('cancelsym', { sym: st.sym })}
            >
              CANCEL
            </button>
            <button
              className="ot-btn tobe"
              disabled={!acts.canBE}
              title={`Move the soft stop of the ticket's ${st.sym} position to entry (only tightens; refused if price is already through entry). ${ONLY_TICKET_ORDERS}`}
              onClick={() => run('stopbesym', { sym: st.sym })}
            >
              SL → ENTRY
            </button>
          </div>
        </div>
      )}
      <div className="ot-orders">
        {orders.map((o) => (
          <div
            key={o.id}
            className={`ot-order${o.active ? '' : ' done'}`}
            title={`${o.status}${o.childStatus ? ` / TP ${o.childStatus}` : ''}${o.slStatus ? ` / SL ${o.slStatus}` : ''}${o.note ? ` - ${o.note}` : ''}`}
          >
            <span className={o.side === 'BUY' ? 'pos' : 'neg'}>{o.side === 'BUY' ? 'B' : 'S'}</span>
            <span>
              {o.sym} {o.qty}@{fmtPx(o.limit)} f{o.filled} TP {fmtPx(o.tp)}
            </span>
            {o.presetLabel && <span className="ot-pslabel">{o.presetLabel}</span>}
            {followText(o) && <span className="ot-follow">{followText(o)}</span>}
            {stopText(o) && <span className={`ot-sl ${o.stopOn === false ? 'off' : o.stopState ?? ''}`}>{stopText(o)}</span>}
            {stopToggleable(o) && (
              <button
                className="ot-x ot-sl-x"
                disabled={busy}
                title={o.stopOn === false ? 'enable the soft stop' : 'disable the soft stop'}
                onClick={() => run('stop', { id: o.id, on: o.stopOn === false })}
              >
                {o.stopOn === false ? 'SL on' : 'SL off'}
              </button>
            )}
            {o.canStop && (stopEdit?.id === o.id ? (
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  void run('movestop', { id: o.id, px: Number(stopEdit.val) });
                  setStopEdit(null);
                }}
              >
                <input value={stopEdit.val} autoFocus size={6} aria-label="stop price" onChange={(e) => setStopEdit({ id: o.id, val: e.target.value })} onKeyDown={(e) => { if (e.key === 'Escape') setStopEdit(null); }} />
              </form>
            ) : (
              <button className="ot-x ot-sl-x" disabled={busy} title="type a new stop price, Enter sends it, Esc cancels" onClick={() => setStopEdit({ id: o.id, val: String((o.kind === 'vwapx' ? o.sl : o.stopLevel) || '') })}>
                SL
              </button>
            ))}
            {o.canBe && (
              <button className="ot-x ot-sl-x" disabled={busy} title="move this order's soft stop to entry" onClick={() => run('stopbe', { id: o.id })}>
                → BE
              </button>
            )}
            {o.active && (
              <button className="ot-x" disabled={busy} onClick={() => run('cancel', { id: o.id })}>
                CANCEL
              </button>
            )}
          </div>
        ))}
        {active.length > 1 && (
          <button className="ot-mini" disabled={busy} onClick={() => run('cancelall')}>
            CANCEL ALL
          </button>
        )}
      </div>
      {st.stopAlert && <div className="ot-status has-err" title={st.stopAlert}>{st.stopAlert}</div>}
      {st.stopUnwatched && <div className="ot-status has-err">STOP NOT WATCHED - reconnect to TWS</div>}
      {st.stopWarn && !st.stopUnwatched && <div className="ot-status">{st.stopWarn}</div>}
      <div className={`ot-status ${st.conn}${err || /\b(rejected|error|cancelled by|not accepted)\b|^IB \d+/i.test(st.note || '') ? ' has-err' : ''}`} title={busy && !pending ? 'checking margin...' : err || st.note || connLabel(st)}>{busy && !pending ? 'checking margin...' : err || st.note || connLabel(st)}</div>
    </div>
  );
}
