import { useState } from 'react';
import { useStore } from '../store';
import { blacklistSet } from '../ws';

// BlacklistPanel: today's symbol blacklist, at the head of the Signal window's
// right column. A listed symbol's alerts make no tone or speech (in every
// window) and the Day Log flags them; the list lives in the scanner
// (internal/blacklist) and clears at 00:00 ET. This box only sends add / remove.

export function blacklistSym(raw: string): string | null {
  const s = raw.trim().toUpperCase();
  return /^[A-Z][A-Z0-9.-]{0,11}$/.test(s) ? s : null;
}

function send(sym: string, add: boolean): void {
  if (!blacklistSet(sym, add)) useStore.getState().pushToast(`Blacklist ${sym} not sent: no connection to the scanner`);
}

export function BlacklistPanel() {
  const list = useStore((s) => s.blacklist);
  const [text, setText] = useState('');
  const sym = blacklistSym(text);
  function add() {
    if (!sym) return;
    send(sym, true);
    setText('');
  }
  return (
    <div className="bl-panel">
      <div className="bl-sec" title="Symbols whose alerts are silenced today (the Day Log flags them). Cleared at 00:00 ET.">
        Blacklist (today)
      </div>
      <div className="bl-row">
        <input
          className="bl-input"
          value={text}
          placeholder="Symbol"
          spellCheck={false}
          onChange={(e) => setText(e.target.value.toUpperCase())}
          onKeyDown={(e) => {
            if (e.key === 'Enter') add();
          }}
        />
        <button type="button" className="mini" disabled={!sym} onClick={add}>
          Add
        </button>
        {list.length === 0 && <span className="muted bl-none">None.</span>}
        {list.map((s) => (
          <span key={s} className="bl-chip">
            {s}
            <button type="button" className="bl-x" aria-label={`Remove ${s} from the blacklist`} title={`Remove ${s} from today's blacklist`} onClick={() => send(s, false)}>
              ×
            </button>
          </span>
        ))}
      </div>
    </div>
  );
}
