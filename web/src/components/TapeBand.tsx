import { useStore } from '../store';
import { readTape } from '../ws';
import { fmt } from '../format';
import { fmtClock as hhmmss } from '../clockFormat';
import type { TapePrint } from '../types';
import { TapeControls } from './TapeControls';
import { CvdChart } from './CvdChart';
import { TapeLadder } from './TapeLadder';
import { RegimeTimeline } from './RegimeTimeline';

// TapeBand is the order-flow strip in the symbol panel: a buyers/sellers
// control gauge computed from aggressor-classified prints, the volume delta,
// the notable prints ticker, and Claude's tape read — plus, below that core
// strip, the toolbar (TapeControls: pause, Claude/Local, voice, commentary
// interval), the CVD chart, the Level 2 ladder, and the regime-reads
// timeline (CvdChart / TapeLadder / RegimeTimeline).

function controlLabel(c: number): { text: string; tone: string } {
  if (c >= 0.35) return { text: 'BUYERS IN CONTROL', tone: 'pos' };
  if (c <= -0.35) return { text: 'SELLERS IN CONTROL', tone: 'neg' };
  return { text: 'BALANCED', tone: 'muted' };
}

const signed = (n: number) => (n > 0 ? `+${fmt(n)}` : fmt(n));

// coreOnly (default false — the docked SymbolPanel renders exactly as
// before) keeps just the core strip: verdict gauge, toolbar, metrics and the
// Claude read. The pop-out tape window (TapeReaderView) passes true because
// it mounts the ladder/CVD/timeline itself as main columns, and its full raw
// tape supersedes the notable-print chips.
export function TapeBand({ coreOnly = false }: { coreOnly?: boolean } = {}) {
  const sym = useStore((s) => s.chartSym);
  const tape = useStore((s) => s.tape);
  const read = useStore((s) => s.tapeRead);
  const loading = useStore((s) => s.tapeReadLoading);

  if (!sym) return null;

  // Waiting for the first digest (the engine publishes ~1 Hz once subscribed).
  if (!tape) {
    return (
      <div className="tape">
        <span className="muted fund-note">Tape: connecting…</span>
      </div>
    );
  }
  if (tape.err) {
    return (
      <div className="tape">
        <span className="muted fund-note" title={tape.err}>
          Tape needs real-time trades on your Massive plan
        </span>
      </div>
    );
  }

  const ctl = controlLabel(tape.control);
  // Gauge: -1..+1 mapped to 0..100% so the fill grows from the middle.
  const pct = Math.round(((tape.control + 1) / 2) * 100);
  const thin = tape.totalTrades < 25;

  return (
    <div className="tape">
      <div className="tape-row">
        <span className={`tape-verdict ${ctl.tone}`}>{ctl.text}</span>
        <div className="tape-gauge" title={`control ${tape.control.toFixed(2)} (-1 sellers .. +1 buyers)`}>
          <div className="tape-gauge-mid" />
          <div
            className={`tape-gauge-fill ${tape.control >= 0 ? 'pos' : 'neg'}`}
            style={
              tape.control >= 0
                ? { left: '50%', width: `${pct - 50}%` }
                : { left: `${pct}%`, width: `${50 - pct}%` }
            }
          />
        </div>
        <span className="spacer" />
        {!read?.text && (
          <button className="summarize" disabled={loading} onClick={() => readTape(sym)}>
            {loading ? 'Reading…' : '✨ Read tape'}
          </button>
        )}
      </div>

      <TapeControls sym={sym} claudeEnabled={tape.claudeEnabled} commentarySecs={tape.commentarySecs} />

      <div className="tape-row tape-metrics">
        <span className="sympanel-stat">
          <label>AT ASK / BID (5m)</label>
          <em>
            <span className="pos">{tape.buyPct5m.toFixed(0)}%</span>
            {' / '}
            <span className="neg">{tape.sellPct5m.toFixed(0)}%</span>
          </em>
        </span>
        <span className="sympanel-stat">
          <label>DELTA 1m</label>
          <em className={tape.delta1m >= 0 ? 'pos' : 'neg'}>{signed(tape.delta1m)}</em>
        </span>
        <span className="sympanel-stat">
          <label>DELTA 5m</label>
          <em className={tape.delta5m >= 0 ? 'pos' : 'neg'}>{signed(tape.delta5m)}</em>
        </span>
        <span className="sympanel-stat">
          <label>CUM DELTA</label>
          <em className={tape.deltaTotal >= 0 ? 'pos' : 'neg'}>{signed(tape.deltaTotal)}</em>
        </span>
        <span className="sympanel-stat">
          <label>TRADES/S</label>
          <em>{tape.rate1m.toFixed(1)}</em>
        </span>
        <span className="sympanel-stat">
          <label>AVG SIZE</label>
          <em>{fmt(Math.round(tape.avgSize5m))}</em>
        </span>
        {!tape.quoteRule && (
          <span className="muted fund-note" title="No NBBO quotes on this plan — using the uptick/downtick rule">
            tick-rule
          </span>
        )}
        {thin && <span className="muted fund-note">thin sample</span>}
      </div>

      {!coreOnly && tape.prints && tape.prints.length > 0 && (
        <div className="tape-prints">
          {tape.prints.slice(0, 6).map((p: TapePrint, i) => (
            <span key={`${p.ts}-${i}`} className={`tape-print ${p.side}`}>
              {hhmmss(p.ts)} {fmt(p.size)}@{p.price.toFixed(p.price < 1 ? 4 : 2)}
            </span>
          ))}
        </div>
      )}

      {!coreOnly && (
        <div className="tape-extra">
          <div className="tape-extra-col">
            <div className="tape-subhead">Cumulative volume delta</div>
            <CvdChart data={tape.cvdSeries ?? []} height={64} />
            <div className="tape-subhead">Level 2</div>
            <TapeLadder
              hasDepth={tape.hasDepth}
              bids={tape.bookBids ?? []}
              asks={tape.bookAsks ?? []}
              depthNote={tape.depthNote ?? ''}
            />
          </div>
          <div className="tape-extra-col">
            <RegimeTimeline />
          </div>
        </div>
      )}

      {read?.err && <div className="news-summary err">⚠ {read.err}</div>}
      {read?.text && <div className="news-summary tape-read">{read.text}</div>}
    </div>
  );
}
