import { memo, useEffect, useMemo, useReducer } from 'react';
import { useStore, summaryDisplay } from '../store';
import { summarizeNews } from '../ws';
import { fmtClock, fmtDayMonth } from '../clockFormat';
import type { NewsItem } from '../types';

// An article counts as FRESH while it's at most this old — fresh cards get a
// brighter box so breaking news is separable from the backlog at a glance.
const FRESH_MS = 5 * 60_000;

function ago(ts: number, now: number): string {
  const s = Math.max(0, Math.floor((now - ts) / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  return `${Math.floor(s / 3600)}h`;
}

// The actual publish time, e.g. "10:34:59" — plus the date when the article
// isn't from today (the archive keeps 30 days, so older items are reachable).
function fmtAbs(ts: number, today: boolean): string {
  const time = fmtClock(ts);
  if (today) return time;
  return `${fmtDayMonth(ts)} ${time}`;
}

// The symbols in the active scan's grid, as a Set whose IDENTITY only changes
// when the membership does. The grid's `order` array is a fresh one on every
// tick of the scan (and on every re-rank), so selecting the grid — or its
// order — re-rendered the panel and all its rows several times a second
// although the panel only asks "is this ticker in the scan". One selector per
// panel instance, because it remembers the last set it handed out.
export function createScanSymsSelector() {
  let prevOrder: readonly string[] | undefined;
  let prevSet: ReadonlySet<string> = new Set();
  return (s: { activeId: string | null; grids: Record<string, { order: readonly string[] }> }): ReadonlySet<string> => {
    const order = s.activeId ? s.grids[s.activeId]?.order : undefined;
    if (order === prevOrder) return prevSet;
    prevOrder = order;
    const next = new Set(order ?? []);
    if (next.size !== prevSet.size || !Array.from(next).every((sym) => prevSet.has(sym))) prevSet = next;
    return prevSet;
  };
}

export function NewsPanel() {
  const news = useStore((s) => s.news);
  const query = useStore((s) => s.newsQuery);
  const scanOnly = useStore((s) => s.newsScanOnly);
  const setQuery = useStore((s) => s.setNewsQuery);
  const toggleScanOnly = useStore((s) => s.toggleNewsScanOnly);
  const activeId = useStore((s) => s.activeId);
  const activeScanName = useStore((s) => s.scans.find((x) => x.id === s.activeId)?.name);
  const scanSymsSelector = useMemo(createScanSymsSelector, []);
  const scanSyms = useStore(scanSymsSelector);
  const selectedSym = useStore((s) => s.chartSym);
  const followSym = useStore((s) => s.newsFollowSym);
  const toggleFollow = useStore((s) => s.toggleNewsFollowSym);

  // Freshness decays with no event to announce it: a card highlighted at
  // 13:00 must dim at 13:05 even if nothing new arrives to trigger a render,
  // and the relative ages count in seconds under a minute. The panel used to
  // be dragged along by App's 1 Hz status re-render and the scan ticks; now
  // it owns the 1 s clock itself. A tick is cheap: the rows are memoized and
  // only the ones whose age text, fresh box or "today" flag moved re-render.
  const [, tick] = useReducer((x: number) => x + 1, 0);
  useEffect(() => {
    const id = setInterval(tick, 1000);
    return () => clearInterval(id);
  }, []);
  const now = Date.now();
  const todayStr = new Date(now).toDateString();

  // scanSyms: symbols currently matched by the active scan (for the "Scan" filter).
  const symLock = followSym && selectedSym ? selectedSym : null;

  const shown = useMemo(() => {
    const q = query.trim().toUpperCase();
    return news.filter((n) => {
      if (symLock && !(n.tickers ?? []).includes(symLock)) return false;
      if (scanOnly && !(n.tickers ?? []).some((t) => scanSyms.has(t))) return false;
      if (q) {
        const inTickers = (n.tickers ?? []).some((t) => t.toUpperCase().includes(q));
        const inTitle = n.headline.toUpperCase().includes(q);
        if (!inTickers && !inTitle) return false;
      }
      return true;
    });
  }, [news, query, scanOnly, scanSyms, symLock]);

  // An empty list has several genuinely different causes — "no news at all"
  // vs. "filtered down to zero" look identical unless named. The trap this
  // closes: "Scan" stays on, the active scan goes quiet (normal after hours —
  // a 1-minute-momentum scan has nothing to say once volume dries up), and
  // the panel reads as "no news" even while headlines are still arriving for
  // every OTHER symbol.
  const emptyReason = (): string => {
    if (news.length === 0) return 'No headlines yet.';
    if (symLock) return `No news yet for ${symLock}.`;
    if (scanOnly) {
      const name = activeScanName ?? 'this scan';
      return scanSyms.size === 0
        ? `"${name}" has no matches right now, so Scan-only shows nothing — turn off "Scan" to see all news.`
        : `No news yet for the ${scanSyms.size} symbol${scanSyms.size === 1 ? '' : 's'} in "${name}".`;
    }
    if (query.trim()) return `Nothing matches "${query.trim()}".`;
    return 'Nothing matches the filter.';
  };

  return (
    <aside className="news">
      <div className="news-head">
        News
        {symLock && (
          <button className="symlock" title="Showing this symbol's news — click to show all" onClick={toggleFollow}>
            {symLock} ✕
          </button>
        )}
        {!symLock && selectedSym && (
          <button className="segbtn" title="Follow the selected symbol again" onClick={toggleFollow}>
            follow {selectedSym}
          </button>
        )}
        <input
          className="news-filter"
          placeholder="filter ticker…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <button
          className={`segbtn ${scanOnly ? 'on' : ''}`}
          title={activeId ? 'Only symbols currently in the active scan' : 'No active scan'}
          onClick={toggleScanOnly}
        >
          Scan
        </button>
      </div>
      <div className="news-list">
        {shown.length === 0 && <div className="muted pad">{emptyReason()}</div>}
        {shown.map((n) => (
          <NewsRow
            key={`${n.id}-${n.ts}`}
            n={n}
            inScan={(n.tickers ?? []).some((t) => scanSyms.has(t))}
            age={ago(n.ts, now)}
            fresh={now - n.ts < FRESH_MS}
            today={new Date(n.ts).toDateString() === todayStr}
          />
        ))}
      </div>
    </aside>
  );
}

// ImpactBadge shows RTPR's Impact Score (Beta) once it arrives (~45s after
// publication — an enrichment of an article already on screen, not a speed
// signal). Colored by direction so a glance separates "worth reading" from
// "skip" without parsing the number.
function ImpactBadge({ n }: { n: NewsItem }) {
  const dir = n.direction === 'bullish' ? 'bullish' : n.direction === 'bearish' ? 'bearish' : 'neutral';
  const event = (n.eventType ?? '').replace(/_/g, ' ').toUpperCase();
  return (
    <span
      className={`impact-badge ${dir}`}
      title={`RTPR Impact Score (Beta): ${n.score}/100${n.scoreTier ? ` · ${n.scoreTier} tier` : ''}${event ? ` · ${event}` : ''}`}
    >
      {n.score}
      {event && <span className="impact-event"> · {event}</span>}
    </span>
  );
}

// Memoized: the panel re-renders on every clock tick, news push and filter
// keystroke, and 500 rows are the expensive part. Everything that moves with
// the clock (age text, fresh box, the "today" date prefix) comes in as a prop
// so the row re-renders exactly when what it prints changes.
const NewsRow = memo(function NewsRow({
  n,
  inScan,
  age,
  fresh,
  today,
}: {
  n: NewsItem;
  inScan: boolean;
  age: string;
  fresh: boolean;
  today: boolean;
}) {
  const sum = useStore((s) => s.summaries[n.id]);
  // The free local engine auto-summarizes in the background, so a summary
  // showing up is not by itself proof the user got the Claude read they might
  // want — see summaryDisplay's doc comment (store.ts) for why the button
  // stays reachable (relabeled) rather than disappearing on a LOCAL summary.
  const { buttonLabel, tag } = summaryDisplay(sum);
  const summarizeTitle =
    tag === 'LOCAL'
      ? 'Ask Claude for a deeper read of this article'
      : n.hasBody
        ? 'Summarize full article with Claude'
        : 'Summarize with Claude (headline/teaser or fetched page)';
  return (
    <div className={`news-item ${n.sentiment || 'neutral'} ${inScan ? 'inscan' : ''} ${fresh ? 'fresh' : ''}`}>
      <div className="news-meta">
        <span className="news-tickers">{(n.tickers ?? []).slice(0, 5).join(' ')}</span>
        <span className="news-time" title={new Date(n.ts).toLocaleString()}>
          <span className="news-ago">{age}</span>
          <span className="news-abs">{fmtAbs(n.ts, today)}</span>
        </span>
      </div>
      <a className="news-title" href={n.url} target="_blank" rel="noreferrer">
        {n.headline}
      </a>
      <div className="news-foot">
        <span className="news-src">{n.source}</span>
        {n.score !== undefined && <ImpactBadge n={n} />}
        <span className="spacer" />
        {buttonLabel && (
          <button className="summarize" disabled={sum?.loading} onClick={() => summarizeNews(n.id)} title={summarizeTitle}>
            {sum?.loading ? 'Summarizing…' : buttonLabel}
          </button>
        )}
      </div>
      {sum?.err && <div className="news-summary err">⚠ {sum.err}</div>}
      {sum?.text && (
        <div className={`news-summary ${tag === 'LOCAL' ? 'local' : ''}`}>
          {tag && <span className={`summary-tag ${tag === 'CLAUDE' ? 'claude' : ''}`}>{tag}</span>}
          {sum.text}
        </div>
      )}
    </div>
  );
});
