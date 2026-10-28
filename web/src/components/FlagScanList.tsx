import { useMemo } from 'react';
import { useStore } from '../store';
import { fieldDef } from '../fields';
import { GREEN_DILDO_SCAN_ID, newsBadge } from '../dildoNews';
import { EARLY_BADGE } from '../dildoEarly';
import { FRESH_BADGE } from '../ignitionFresh';
import {
  flagListEmptyText,
  flagListHead,
  flagListMissingText,
  flagListRows,
  flagScanLabel,
  isAlertRuleScanId,
  isE9ScanId,
  orderFlagScans,
} from '../e9';

// FlagScanList: the 9EMA flag MARKET SCAN's results in the Signal window's left
// bar, in the empty space under the watchlist chips (user: "Can you insert the
// results on the left bar so i can see it"). The scan itself runs server-side
// (internal/flagscan) and is already a normal scan — the seeded
// "9EMA Flag — sideways | ready | breakout" — so this list is just that scan's
// rows, in the scan's own order, in the same look and density as the charts
// window's gainers/decliners boxes (it reuses SideBoxes' .sidebox classes):
//
//   TICKER   STAGE (the grid's e9State badge)   EMA (signed % from the 9 EMA)   AGE
//
// The default source is "sideways" (stage >= 2: every stock holding a flag —
// FLAT, READY and LONG — strongest first), because that is the one list that
// never hides a name the trader should be looking at; a picker in the head
// switches it to the "ready" or "breakout" scan or to any scan of the user's own
// (the e9* columns are only on the seeded three, so another scan shows dashes
// for them — the rows are still draggable). The fourth seeded scan, "9EMA Slam"
// (a 9 EMA SLAM in the last 5 minutes — a bearish alert), lists SLAM rows: a red
// SLAM badge, the EMA distance, and the time since the slam in the Age column
// (e9.ts flagListRows). The choice lives in SignalPane
// (it also decides this window's scan view, see there) and is remembered in
// localStorage.
//
// The market-wide 1-minute alert scans (2026-10-07) sit in the picker's own
// "1-minute alerts" group, after the 9EMA ones: "Green Dildo" (DILDO), "Dildo
// 9EMA Pullback" (PB), "Dildo 9EMA Touch" (TOUCH), "Volume Climax" (CLIMAX) and
// "Bottom Confirmed" (BOTTOM). Their rows carry no e9 fields, so the middle
// column shows the dildo's body or the climax's drop instead of the EMA
// distance, and the age is the time since the scan's own event; the headers
// say which (e9.ts flagListHead).
//
// A click on a row puts the symbol on the ACTIVE chart and links it, exactly
// like typing it into the toolbar's input (SignalPane.pickFlagSym); a row also
// drags onto any chart cell, or up into the watchlist, like the gainers rows.
// Rows whose symbol is already on a chart in this window carry a dot and the
// accent colour, so the list doubles as "which of these am I already watching".

// The headers' tooltips are the hints of the fields under them (e9.ts
// flagListHead: the "9EMA Slam" scan's age is the time since the slam, a dildo
// scan's the time since its dildo / pullback / touch, and so on).
const headTitle = (key: string): string => fieldDef(key)?.hint ?? key;

const MUTE_TITLE_ON =
  'This scan’s alert sounds are muted in this window — click to unmute. (Alerts for this scan also play in any other window that shows it.)';
const MUTE_TITLE_OFF =
  'Mute this scan’s alert sounds in this window. Alerts for this scan also play in any other window that shows it — mute one of them to avoid hearing it twice.';
// A seeded signal scan's 🔊 is its switch in the window's "Alerts" menu (the same state).
const SWITCH_TITLE_ON = 'This signal is switched on in this window (its box in the Alerts menu) — click to switch it off: no tone, no voice here.';
const SWITCH_TITLE_OFF = 'This signal is switched off in this window (its box in the Alerts menu) — its rows still list, but it makes no sound here. Click to switch it on.';

export function FlagScanList({
  scanId,
  onScanId,
  onChart,
  onPick,
  signalOn,
  onToggleSignal,
}: {
  scanId: string; // the scan whose rows are listed (SignalPane owns it: it also narrows the window's scan view to it)
  onScanId: (id: string) => void;
  onChart: ReadonlySet<string>; // symbols on a chart in this window right now
  onPick: (sym: string) => void; // click: put it on the active chart and link it
  // When the listed scan is one of the Signal window's switchable signals: its
  // switch, which the head's 🔊 / 🔇 then shows and flips (instead of a separate
  // per-scan mute that would disagree with the Alerts menu).
  signalOn?: boolean;
  onToggleSignal?: () => void;
}) {
  const feed = useStore((s) => s.feed);
  const scans = useStore((s) => s.scans);
  const grid = useStore((s) => s.grids[scanId]);
  const scanMuted = useStore((s) => s.mutedScans[scanId] === true);
  const muted = onToggleSignal ? signalOn === false : scanMuted;
  const toggleScanMute = useStore((s) => s.toggleScanMute);

  const scan = scans.find((x) => x.id === scanId);
  const ordered = useMemo(() => orderFlagScans(scans), [scans]);
  const seeded = ordered.filter((x) => isE9ScanId(x.id));
  const alertScans = ordered.filter((x) => isAlertRuleScanId(x.id));
  const others = ordered.filter((x) => !isE9ScanId(x.id) && !isAlertRuleScanId(x.id));
  const rows = useMemo(() => flagListRows(grid?.order ?? [], grid?.rows ?? {}, onChart, scanId), [grid, onChart, scanId]);
  const head = flagListHead(scanId);
  // Green Dildo rows: 'NEWS 4m' when the stock had a headline in the hour before
  // its dildo (live age, headline as tooltip; dildoNews.ts).
  const news = useStore((s) => (scanId === GREEN_DILDO_SCAN_ID ? s.news : null));
  const now = Date.now();
  const rowNews = (sym: string) => {
    if (!news) return null;
    const age = grid?.rows?.[sym]?.dildoAge;
    return newsBadge(news, sym, typeof age === 'number' ? now - age * 1000 : now, now);
  };

  // Which state the body is in. The scan list is empty until the server's
  // scan.list arrives, so "scans.length === 0" is still "waiting", not "this
  // scan is missing" — otherwise every connect would flash the seeding hint.
  let empty: string | null = null;
  if (feed !== 'up' || scans.length === 0) {
    empty = 'Waiting for the scanner…';
  } else if (!scan) {
    empty = flagListMissingText(scanId);
  } else if (!scan.enabled) {
    empty = `${scan.name} is switched off — enable it in the scan editor.`;
  } else if (!grid) {
    empty = 'Waiting for the scanner…';
  } else if (rows.length === 0) {
    empty = flagListEmptyText(scanId, scan.name);
  }

  return (
    <div className="sideboxes flaglist">
      <section className="sidebox">
        <header className="sidebox-head">
          <span
            className="t"
            title="The 9EMA flag market scan: stocks with pre-market volume of at least 1x their 10-day average, run through the Signal window's flag rules — or, picked below, one of the whole-market 1-minute alert scans (green dildo, 9 EMA pullback, volume climax, bottom confirmed: “look now” prompts, not systems). Click a row to put it on the active chart, or drag it onto any chart (or up into the watchlist)."
          >
            Flag scan{scan && grid ? ` · ${rows.length}` : ''}
          </span>
          {scans.length > 0 && (
            <select
              className="flag-scan-sel"
              value={scan ? scanId : ''}
              title="the scan that feeds this list (the 9EMA stage, EMA distance and age columns are on the seeded 9EMA flag scans only; the slam scan lists 9 EMA slams of the last 5 minutes; the 1-minute alert scans list their latest green dildo, 9 EMA pullback, volume climax or confirmed bottom)"
              onChange={(e) => {
                if (e.target.value) onScanId(e.target.value);
              }}
            >
              {!scan && <option value="">pick a scan…</option>}
              {seeded.length > 0 && (
                <optgroup label="9EMA flag scans">
                  {seeded.map((s) => (
                    <option key={s.id} value={s.id}>
                      {flagScanLabel(s.name)}
                    </option>
                  ))}
                </optgroup>
              )}
              {alertScans.length > 0 && (
                <optgroup label="1-minute alerts">
                  {alertScans.map((s) => (
                    <option key={s.id} value={s.id}>
                      {flagScanLabel(s.name)}
                    </option>
                  ))}
                </optgroup>
              )}
              {others.length > 0 && (
                <optgroup label="your scans">
                  {others.map((s) => (
                    <option key={s.id} value={s.id}>
                      {s.name}
                    </option>
                  ))}
                </optgroup>
              )}
            </select>
          )}
          <button
            className={`iconbtn tile-btn flag-mute${muted ? ' off' : ''}`}
            title={onToggleSignal ? (muted ? SWITCH_TITLE_OFF : SWITCH_TITLE_ON) : muted ? MUTE_TITLE_ON : MUTE_TITLE_OFF}
            onClick={() => (onToggleSignal ? onToggleSignal() : toggleScanMute(scanId))}
          >
            {muted ? '🔇' : '🔊'}
          </button>
        </header>
        <div className="sidebox-list">
          {empty && <div className="sidebox-empty">{empty}</div>}
          {!empty && (
            <div className="sidebox-cols e9row">
              <span>Ticker</span>
              <span>Stage</span>
              <span title={headTitle(head.midKey)}>{head.mid}</span>
              <span title={headTitle(head.ageKey)}>Age</span>
            </div>
          )}
          {!empty &&
            rows.map((r) => (
              <div
                key={r.sym}
                className={`sidebox-row e9row${r.onChart ? ' on-chart' : ''}`}
                draggable
                title={r.title}
                onDragStart={(e) => {
                  e.dataTransfer.effectAllowed = 'copy';
                  e.dataTransfer.setData('text/plain', r.sym);
                }}
                onClick={() => onPick(r.sym)}
              >
                <span className="sb-sym">{r.sym}</span>
                <span className="sb-stage">
                  {r.badge ? (
                    <span className={`e9-badge ${r.badge.cls}`} title={r.badge.title}>
                      {r.badge.label}
                    </span>
                  ) : (
                    <span className="muted">–</span>
                  )}
                  {r.early && (
                    <span className="flagev-badge early-badge" title={EARLY_BADGE.title}>
                      {EARLY_BADGE.label}
                    </span>
                  )}
                  {r.psTag && (
                    <span className="flagev-badge early-badge" title="Power setup filters: close under 21% over the day open, 9EMA up 2.1%+ over 3 bars (a check passed, a cross failed, a ? unknown)">
                      {r.psTag}
                    </span>
                  )}
                  {r.ignFresh && (
                    <span className="flagev-badge early-badge" title={FRESH_BADGE.title}>
                      {FRESH_BADGE.label}
                    </span>
                  )}
                  {(() => {
                    const nb = rowNews(r.sym);
                    return nb ? (
                      <span className="flagev-badge news-badge" title={nb.title}>
                        {nb.label}
                      </span>
                    ) : null;
                  })()}
                </span>
                <span className={`sb-num ${r.dist.startsWith('-') ? 'neg' : r.dist.startsWith('+') ? 'pos' : ''}`}>{r.dist}</span>
                <span className="sb-num">{r.age}</span>
              </div>
            ))}
        </div>
      </section>
    </div>
  );
}
