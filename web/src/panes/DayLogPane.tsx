import { useEffect, useMemo, useRef, useState } from 'react';
import { useStore } from '../store';
import { start, getDayLog } from '../ws';
import { StatusBar } from '../components/StatusBar';
import { fmtPct, fmtPrice, fmtCompact, fmtRatio } from '../format';
import {
  NO_FILTER,
  alertsToCsv,
  filterAlerts,
  fmtAge,
  fmtEtTime,
  nextSort,
  scanOptions,
  sortAlerts,
  summarizeByScan,
  todayEt,
  type DayLogFilter,
  type SortDir,
  type SortKey,
} from '../dayLog';
import type { DayAlert, DayMover } from '../types';

// DayLogPane is the Day Log pop-out (?pane=daylog): every scan alert of an ET
// trading day as the server recorded it, with what the price did afterwards, a
// per-scan summary and the day's top gainers / losers. Click a row to select
// its symbol in the other windows (openChart, the same as every other list);
// rows drag onto the charts watchlist like the review rows do.
const REFRESH_MS = 30_000; // today is re-read this often (alerts and outcomes keep arriving)

const COLS: { key: SortKey; label: string; w: number; title: string }[] = [
  { key: 'at', label: 'Time ET', w: 74, title: 'when the alert fired' },
  { key: 'sym', label: 'Symbol', w: 74, title: 'symbol' },
  { key: 'scanName', label: 'Scan', w: 150, title: 'the scan that alerted' },
  { key: 'price', label: 'Price', w: 70, title: 'price at the alert' },
  { key: 'chgDay', label: 'Day %', w: 72, title: 'change on the day at the alert' },
  { key: 'vol', label: 'Volume', w: 74, title: 'session volume at the alert' },
  { key: 'rvol', label: 'RVOL', w: 60, title: 'relative volume at the alert' },
  { key: 'newsAge', label: 'News', w: 56, title: 'age of the latest headline when the alert fired' },
  { key: 'out5', label: '+5m', w: 62, title: 'price change 5 minutes after the alert' },
  { key: 'out15', label: '+15m', w: 62, title: 'price change 15 minutes after the alert' },
  { key: 'out30', label: '+30m', w: 62, title: 'price change 30 minutes after the alert' },
  { key: 'maxUp', label: 'Max up', w: 66, title: 'best move above the alert price inside 30 minutes' },
  { key: 'maxDown', label: 'Max dn', w: 66, title: 'worst move below the alert price inside 30 minutes' },
];

function tone(v: number | null): string {
  return v === null ? '' : v > 0 ? 'pos' : v < 0 ? 'neg' : '';
}

function shiftDay(date: string, delta: number): string {
  const d = new Date(`${date}T12:00:00Z`);
  d.setUTCDate(d.getUTCDate() + delta);
  return d.toISOString().slice(0, 10);
}

export function DayLogPane() {
  const feed = useStore((s) => s.feed);
  const dayLog = useStore((s) => s.dayLog);
  const openChart = useStore((s) => s.openChart);
  const [date, setDate] = useState(() => todayEt());
  const [filter, setFilter] = useState<DayLogFilter>(NO_FILTER);
  const [sort, setSort] = useState<{ key: SortKey; dir: SortDir }>({ key: 'at', dir: 'desc' });
  const dateRef = useRef(date);
  dateRef.current = date;

  useEffect(() => {
    start();
  }, []);

  // Load on connect (and again after a reconnect), whenever the date changes, and
  // keep today fresh while the window is open.
  useEffect(() => {
    if (feed !== 'up') return;
    getDayLog(date);
    if (date !== todayEt()) return;
    const id = window.setInterval(() => {
      if (dateRef.current === todayEt()) getDayLog(dateRef.current);
    }, REFRESH_MS);
    return () => window.clearInterval(id);
  }, [feed, date]);

  const frame = dayLog.frame && dayLog.frame.date === date ? dayLog.frame : null;
  const alerts = frame?.alerts ?? [];
  const movers = frame?.movers ?? [];

  const scans = useMemo(() => scanOptions(alerts), [alerts]);
  const summary = useMemo(() => summarizeByScan(filterAlerts(alerts, { ...filter, scan: '' })), [alerts, filter]);
  const rows = useMemo(() => sortAlerts(filterAlerts(alerts, filter), sort.key, sort.dir), [alerts, filter, sort]);
  const gainers = movers.filter((m) => m.side === 'gain');
  const losers = movers.filter((m) => m.side === 'loss');

  function exportCsv() {
    const blob = new Blob(['﻿', alertsToCsv(rows)], { type: 'text/csv;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `daylog-${date}.csv`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 1000);
  }

  const isToday = date === todayEt();
  const csvPath = frame?.csvDir ? `${frame.csvDir}\\${date}.csv` : '';

  return (
    <div className="app pane-window daylog">
      <StatusBar />
      <div className="review-toolbar">
        <button className="segbtn" title="previous day" onClick={() => setDate(shiftDay(date, -1))}>
          ◀
        </button>
        <input
          type="date"
          className="review-date-sel"
          value={date}
          max={todayEt()}
          onChange={(e) => e.target.value && setDate(e.target.value)}
          title="trading day (Eastern time)"
        />
        <button className="segbtn" title="next day" disabled={isToday} onClick={() => setDate(shiftDay(date, 1))}>
          ▶
        </button>
        <button className={`segbtn ${isToday ? 'on' : ''}`} onClick={() => setDate(todayEt())} title="today (Eastern time)">
          Today
        </button>
        <select
          className="review-date-sel"
          value={filter.scan}
          onChange={(e) => setFilter({ ...filter, scan: e.target.value })}
          title="show one scan only"
        >
          <option value="">all scans ({alerts.length})</option>
          {scans.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name} ({s.count})
            </option>
          ))}
        </select>
        <input
          className="daylog-search"
          placeholder="symbol…"
          value={filter.query}
          onChange={(e) => setFilter({ ...filter, query: e.target.value })}
          title="symbol search - starts with, several separated by space or comma"
        />
        <label className="review-minvol" title="hide alerts on names that were blacklisted that day">
          <input
            type="checkbox"
            checked={filter.hideBlacklisted}
            onChange={(e) => setFilter({ ...filter, hideBlacklisted: e.target.checked })}
            style={{ width: 'auto' }}
          />
          hide blacklisted
        </label>
        {dayLog.loading && <span className="review-loading muted">loading…</span>}
        {dayLog.error && <span className="neg daylog-err">{dayLog.error}</span>}
        <span className="spacer" />
        {isToday && (
          <button className="segbtn" onClick={() => getDayLog(date, true)} title="take the top gainers / losers snapshot now (also taken automatically at 20:00 ET and at shutdown)">
            Snapshot movers
          </button>
        )}
        <button className="segbtn" onClick={exportCsv} disabled={rows.length === 0} title={`download the ${rows.length} rows shown as CSV${csvPath ? ` - the server also keeps ${csvPath}` : ''}`}>
          Export CSV
        </button>
      </div>

      <div className="daylog-body">
        <div className="daylog-top">
          <section className="daylog-card">
            <h4>By scan</h4>
            {summary.length === 0 ? (
              <div className="muted daylog-empty">no alerts</div>
            ) : (
              <table className="daylog-table">
                <thead>
                  <tr>
                    <th>Scan</th>
                    <th title="alerts fired">Alerts</th>
                    <th title="average price change 15 minutes after the alert">Avg +15m</th>
                    <th title="share of alerts that were above the alert price 15 minutes later">Up %</th>
                  </tr>
                </thead>
                <tbody>
                  {summary.map((s) => (
                    <tr
                      key={s.scanId}
                      className={`clickable ${filter.scan === s.scanId ? 'sel' : ''}`}
                      onClick={() => setFilter({ ...filter, scan: filter.scan === s.scanId ? '' : s.scanId })}
                      title="click to show this scan only"
                    >
                      <td>{s.scanName}</td>
                      <td>{s.count}</td>
                      <td className={tone(s.avg15)} title={`${s.n15} of ${s.count} have a +15m outcome`}>
                        {fmtPct(s.avg15)}
                      </td>
                      <td>{s.upPct === null ? '–' : `${s.upPct.toFixed(0)}%`}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </section>
          <MoverCard title="Top gainers" rows={gainers} onOpen={openChart} />
          <MoverCard title="Top losers" rows={losers} onOpen={openChart} />
        </div>

        <div className="grid-scroll daylog-scroll">
          <div className="grid-row head daylog-row">
            {COLS.map((c) => (
              <div
                key={c.key}
                className={`cell daylog-h ${sort.key === c.key ? 'sorted' : ''}`}
                style={{ flex: `0 0 ${c.w}px` }}
                title={`${c.title} - click to sort`}
                onClick={() => setSort(nextSort(sort, c.key))}
              >
                {c.label}
                {sort.key === c.key ? (sort.dir === 'asc' ? ' ▲' : ' ▼') : ''}
              </div>
            ))}
            <div className="cell daylog-note-h">News / note</div>
          </div>
          {rows.map((a) => (
            <AlertRow key={a.id} a={a} onOpen={() => openChart(a.sym)} />
          ))}
          {rows.length === 0 && (
            <div className="empty">
              {frame === null
                ? 'Loading…'
                : alerts.length === 0
                  ? isToday
                    ? 'No alerts recorded yet today.'
                    : `No alerts were recorded on ${date} (the scanner may not have been running).`
                  : 'No alerts match the filter.'}
            </div>
          )}
          <div className="review-footer muted">
            {rows.length} of {alerts.length} alerts{csvPath ? ` · file: ${csvPath}` : ''}
          </div>
        </div>
      </div>
    </div>
  );
}

function MoverCard({ title, rows, onOpen }: { title: string; rows: DayMover[]; onOpen: (sym: string) => void }) {
  return (
    <section className="daylog-card daylog-movers">
      <h4>{title}</h4>
      {rows.length === 0 ? (
        <div className="muted daylog-empty">no snapshot yet - taken at 20:00 ET</div>
      ) : (
        <table className="daylog-table">
          <tbody>
            {rows.map((m) => (
              <tr key={m.sym} className="clickable" onClick={() => onOpen(m.sym)} title={m.headline || m.sym}>
                <td>{m.rank}</td>
                <td className="sym">{m.sym}</td>
                <td className={tone(m.chgDay)}>{fmtPct(m.chgDay)}</td>
                <td>{fmtPrice(m.price)}</td>
                <td>{fmtCompact(m.vol)}</td>
                <td className="daylog-hl">{m.headline}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}

function AlertRow({ a, onOpen }: { a: DayAlert; onOpen: () => void }) {
  const text = [a.newsHeadline, a.note].filter(Boolean).join('  ·  ');
  return (
    <div
      className={`grid-row data clickable daylog-row ${a.blacklisted ? 'daylog-bl' : ''}`}
      onClick={onOpen}
      title={`${a.sym} - ${a.scanName}${a.blacklisted ? ' (blacklisted that day)' : ''}\n${text}`}
      draggable
      onDragStart={(e) => {
        e.dataTransfer.setData('text/plain', a.sym);
        e.dataTransfer.effectAllowed = 'copy';
      }}
    >
      {[
        <span key="t">{fmtEtTime(a.at)}</span>,
        <span key="s" className="sym">
          {a.sym}
          {a.blacklisted ? ' ⛔' : ''}
        </span>,
        <span key="sc">{a.scanName || a.scanId}</span>,
        <span key="p">{fmtPrice(a.price)}</span>,
        <span key="c" className={tone(a.chgDay)}>
          {fmtPct(a.chgDay)}
        </span>,
        <span key="v">{fmtCompact(a.vol)}</span>,
        <span key="r">{a.rvol === null ? '–' : fmtRatio(a.rvol)}</span>,
        <span key="n">{a.newsHeadline ? fmtAge(a.newsAge) : '–'}</span>,
        <span key="o5" className={tone(a.out5)}>
          {fmtPct(a.out5)}
        </span>,
        <span key="o15" className={tone(a.out15)}>
          {fmtPct(a.out15)}
        </span>,
        <span key="o30" className={tone(a.out30)}>
          {fmtPct(a.out30)}
        </span>,
        <span key="u" className={tone(a.maxUp)}>
          {fmtPct(a.maxUp)}
        </span>,
        <span key="d" className={tone(a.maxDown)}>
          {fmtPct(a.maxDown)}
        </span>,
      ].map((el, i) => (
        <div key={el.key} className="cell" style={{ flex: `0 0 ${COLS[i].w}px` }}>
          {el}
        </div>
      ))}
      <div className="cell daylog-note">{text}</div>
    </div>
  );
}
