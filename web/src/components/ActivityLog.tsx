import { useStore } from '../store';
import { fmtPrice } from '../format';
import { fmtClock } from '../clockFormat';
import { ColResizer } from './Resize';

// ActivityLog is the Scanz-style event ledger under the grid: every scan entry
// (and cross alert) with time, ticker, price, per-day hit count, and the scan
// it fired from. Clicking a row selects the symbol in the right panel.

const ACT_COLS = [
  { key: 'seq', label: '#', dflt: 44, min: 32, max: 90 },
  { key: 'time', label: '@Time', dflt: 88, min: 60, max: 160 },
  { key: 'sym', label: 'Ticker', dflt: 72, min: 50, max: 160 },
  { key: 'last', label: 'Last Price', dflt: 84, min: 50, max: 160 },
  { key: 'news', label: 'News', dflt: 56, min: 40, max: 100 },
  { key: 'count', label: 'Count', dflt: 60, min: 40, max: 120 },
  { key: 'scan', label: 'Scan', dflt: 170, min: 60, max: 400 },
  { key: 'event', label: 'Event', dflt: 84, min: 50, max: 160 },
] as const;

export function ActivityLog() {
  const activity = useStore((s) => s.activity);
  const openChart = useStore((s) => s.openChart);
  const actColW = useStore((s) => s.actColW);
  const setActColW = useStore((s) => s.setActColW);
  const persistLayout = useStore((s) => s.persistLayout);
  const scans = useStore((s) => s.scans);
  const activeId = useStore((s) => s.activeId);
  const actScanOnly = useStore((s) => s.actScanOnly);
  const toggleActScanOnly = useStore((s) => s.toggleActScanOnly);

  const widthOf = (key: string, dflt: number) => actColW[key] ?? dflt;

  // The log is fed by fanoutAll, so it carries hits from EVERY scan while the
  // grid above shows only the active one. Naming the scan (and dimming the
  // others) is what keeps a quiet scan from looking like a broken one.
  const nameOf = (id: string) => scans.find((x) => x.id === id)?.name ?? id;
  const rows = actScanOnly ? activity.filter((e) => e.scanId === activeId) : activity;
  const others = activity.length - activity.filter((e) => e.scanId === activeId).length;

  return (
    <section className="actlog">
      <div className="actlog-head">
        <span className="actlog-badge">{rows.length} Events</span>
        Activity Log
        <button
          className={`segbtn ${actScanOnly ? 'on' : ''}`}
          onClick={toggleActScanOnly}
          title={
            actScanOnly
              ? 'Showing hits from this scan only — click to show all scans'
              : 'Showing hits from all scans — click to show this scan only'
          }
        >
          This scan only
        </button>
        {!actScanOnly && others > 0 && (
          <span className="actlog-note">{others} from other scans</span>
        )}
      </div>
      <div className="actlog-scroll">
        <table className="actlog-table">
          <colgroup>
            {ACT_COLS.map((c) => (
              <col key={c.key} style={{ width: widthOf(c.key, c.dflt) }} />
            ))}
          </colgroup>
          <thead>
            <tr>
              {ACT_COLS.map((c) => (
                <th key={c.key}>
                  {c.label}
                  <ColResizer
                    width={widthOf(c.key, c.dflt)}
                    min={c.min}
                    max={c.max}
                    onChange={(w) => setActColW(c.key, w)}
                    onCommit={persistLayout}
                  />
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 && (
              <tr>
                <td className="muted" colSpan={ACT_COLS.length}>
                  {actScanOnly && activity.length > 0
                    ? 'No hits for this scan yet — other scans have hits, click to show all.'
                    : 'Scan hits will appear here.'}
                </td>
              </tr>
            )}
            {rows.map((e) => (
              <tr
                key={e.seq}
                className={e.scanId === activeId ? '' : 'actlog-other'}
                onClick={() => openChart(e.sym)}
                title={`Open ${e.sym} — hit on ${nameOf(e.scanId)}`}
              >
                <td className="muted">{e.seq}</td>
                <td>{fmtClock(e.at)}</td>
                <td className="actlog-sym">{e.sym}</td>
                <td>{fmtPrice(e.last)}</td>
                <td>
                  {e.hasNews ? (
                    <span className="flag flag-news-hit" title="Had a fresh headline at entry" />
                  ) : (
                    <span className="muted">–</span>
                  )}
                </td>
                <td>{e.count}</td>
                <td className="muted">{nameOf(e.scanId)}</td>
                <td className={e.kind === 'ENTERED' ? 'pos' : ''}>{e.kind}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
