import { useStore } from '../store';
import { fmtClock as hhmmss } from '../clockFormat';

// SetupCard is Claude's read of the selected symbol's bull-flag / tight-
// consolidation setup, mounted under the fundamentals row in SymbolPanel. It
// is push-only: the server fires a read when a symbol enters a scan that
// filters on the nightly flag fields, so unlike the tape read there is no
// button here and nothing to request — most symbols never produce one, and
// this block renders nothing at all until one arrives for the symbol on
// screen (store.setupRead is already scoped to chartSym).
//
// The card body deliberately reuses RegimeTimeline's .regime-card look: it is
// the same shape of content (a short verdict line, a paragraph, a bulleted
// list, a footer), so only the quality pill is new CSS. A failed read gets
// TapeBand's .news-summary.err treatment, for the same reason it does there —
// "Claude couldn't read this" is information, not a reason to show nothing.

// qualityTone maps the 1-5 score onto the app-wide pos/neg/muted tones (the
// same three the tape verdict uses) rather than a fourth color scale: 4-5 is
// constructive, 1-2 is broken, 3 sits neutral.
function qualityTone(q: number): string {
  if (q >= 4) return 'pos';
  if (q <= 2) return 'neg';
  return 'muted';
}

export function SetupCard() {
  const sym = useStore((s) => s.chartSym);
  const read = useStore((s) => s.setupRead);

  if (!sym || !read) return null;

  // A failed read still arrives fully populated (quality 0 + err set), so it
  // can be rendered as a failure instead of vanishing.
  if (read.err) {
    return <div className="setup-read news-summary err">⚠ Setup read unavailable — {read.err}</div>;
  }

  const tone = qualityTone(read.quality);
  return (
    <div className={`setup-read regime-card ${tone}`}>
      <div className="regime-card-head">
        <span className={`regime-verdict ${tone}`}>
          <span
            className={`setup-quality ${tone}`}
            title="Claude's read of the nightly flag geometry: 1 broken .. 5 textbook"
          >
            SETUP {read.quality}/5
          </span>
          {read.verdict}
        </span>
        <span className="regime-when">{hhmmss(read.ts)}</span>
      </div>
      <div className="regime-say">{read.whatsHappening}</div>
      {read.risks && read.risks.length > 0 && (
        <ul className="regime-evidence">
          {read.risks.map((r, i) => (
            <li key={i}>{r}</li>
          ))}
        </ul>
      )}
      {read.invalidation && <div className="regime-watch">Invalidated if: {read.invalidation}</div>}
      {/* asOf is the flag geometry's date, not when the read ran (that's the
          timestamp in the head) — worth showing, since a read describes the
          nightly shape and stays valid exactly as long as that shape does. */}
      <div className="regime-foot">
        flag as of {read.asOf} · {read.model}
      </div>
    </div>
  );
}
