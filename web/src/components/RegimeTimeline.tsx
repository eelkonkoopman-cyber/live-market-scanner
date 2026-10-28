import { useEffect, useRef } from 'react';
import { useStore } from '../store';
import type { TapeCard } from '../types';
import { fmtClock as hhmmss } from '../clockFormat';

// RegimeTimeline is the "Claude reads" feed for the selected symbol: every
// completed tape-reading card (store.tapeCards), newest first — the Go/React
// port of the reference Python app's `#reads` panel (static/app.js
// renderRead()). Each card is either the instant, free local verdict standing
// in alone, or Claude's structured read once it lands (card.model tells
// which); the footer names the regime trigger that fired it so a heartbeat's
// periodic narration reads differently from an instant sweep/absorption call.

// controlLabel/controlTone reuse TapeBand's exact three-way wording and
// pos/neg/muted color convention for "who's in control" — TapeBand derives it
// from a -1..+1 score, this from the card's own already-classified
// buyers/sellers/balanced string.
function controlLabel(control: string): string {
  if (control === 'buyers') return 'BUYERS IN CONTROL';
  if (control === 'sellers') return 'SELLERS IN CONTROL';
  return 'BALANCED';
}
function controlTone(control: string): string {
  if (control === 'buyers') return 'pos';
  if (control === 'sellers') return 'neg';
  return 'muted';
}

// triggerLabel turns a regime.go trigger id into readable words. "heartbeat"
// gets its own wording (it's periodic commentary, not a one-off regime
// shift) — everything else is just its snake_case name with spaces, so an
// unrecognized future trigger still renders sensibly instead of disappearing.
function triggerLabel(trigger: string): string {
  if (trigger === 'heartbeat') return 'periodic update';
  return trigger.replace(/_/g, ' ');
}

function RegimeCard({ card }: { card: TapeCard }) {
  const tone = card.err ? 'err' : controlTone(card.control);
  const modelLabel = card.model === 'local' ? 'local verdict' : card.model;
  return (
    <div className={`regime-card ${tone}`}>
      <div className="regime-card-head">
        <span className={`regime-verdict ${tone}`}>
          {card.err ? card.headline || 'Regime shift' : controlLabel(card.control)}
        </span>
        <span className="regime-when">{hhmmss(card.ts)}</span>
      </div>
      {card.err ? (
        <div className="regime-say err">⚠ {card.err}</div>
      ) : (
        <>
          <div className="regime-conf">
            confidence {Math.round(card.confidence * 100)}%{card.headline ? ` · ${card.headline}` : ''}
          </div>
          <div className="regime-say">{card.whatsHappening}</div>
          {card.evidence && card.evidence.length > 0 && (
            <ul className="regime-evidence">
              {card.evidence.map((e, i) => (
                <li key={i}>{e}</li>
              ))}
            </ul>
          )}
          {card.watchFor && <div className="regime-watch">Watch: {card.watchFor}</div>}
        </>
      )}
      <div className="regime-foot">
        {triggerLabel(card.trigger)} · {modelLabel}
      </div>
    </div>
  );
}

export function RegimeTimeline() {
  const sym = useStore((s) => s.chartSym);
  const cards = useStore((s) => s.tapeCards);
  const paused = useStore((s) => s.tapePaused);

  // Pausing (the toolbar's Pause/Resume button, see TapeControls.tsx) freezes
  // this feed at whatever it showed the instant the user paused — cards keep
  // arriving into the store underneath (so nothing is lost, and voice/other
  // consumers are unaffected), this component just stops re-rendering them
  // until resumed. frozen tracks the live list right up until paused flips
  // true, then stops updating.
  const frozen = useRef<TapeCard[]>(cards);
  useEffect(() => {
    if (!paused) frozen.current = cards;
  }, [cards, paused]);
  const shown = paused ? frozen.current : cards;

  if (!sym) return null;

  return (
    <div className="regime">
      <div className="regime-head">
        Tape reads
        <span className="regime-count">{shown.length}</span>
        {paused && (
          <span className="muted fund-note" title="New reads keep arriving underneath; resume from the toolbar to see them">
            frozen — paused
          </span>
        )}
      </div>
      <div className="regime-list">
        {shown.length === 0 && <div className="muted pad">No tape reads yet — watching for a regime shift…</div>}
        {shown.map((c, i) => (
          <RegimeCard key={`${c.ts}-${i}`} card={c} />
        ))}
      </div>
    </div>
  );
}
