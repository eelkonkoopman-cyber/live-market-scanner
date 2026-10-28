import { useEffect, useRef, useState } from 'react';
import { useStore, GLOBAL_SYM_KEY } from '../store';
import { start } from '../ws';
import { StatusBar } from '../components/StatusBar';
import { TapeReaderView } from '../components/TapeReaderView';

// TapePane is a minimal pop-out window shell showing just the TapeReaderView
// (raw time & sales + Level 2 ladder + order-flow strip — deliberately no
// chart) for one symbol. It's a fresh page load — its
// own store and WebSocket connection — so its tape (order-flow) reading is
// genuinely independent of any other open window's selection by default: the
// backend tracks a SET of watched symbols, one per window, not a single
// shared one.
//
// "Follow" mode (default on) is a purely client-side exception to that: this
// window mirrors whatever symbol was most recently selected in ANY window
// (typically the main one), so a standing tape-reader pop-out tracks your
// clicks instead of staying locked to whatever it was opened with. It's
// implemented via localStorage + the `storage` event rather than any new
// wire protocol — the browser only fires `storage` in OTHER tabs, never the
// one that wrote the value, so following can't create a feedback loop.
// Turning follow off "pins" the window to its current symbol, restoring the
// original independent behavior for side-by-side comparisons.
export function TapePane({ sym: initialSym }: { sym: string }) {
  const feed = useStore((s) => s.feed);
  const chartSym = useStore((s) => s.chartSym);
  const armed = useRef(false);
  const [following, setFollowing] = useState(true);

  useEffect(() => {
    start();
  }, []);

  // (Re-)establish the watch whenever the feed comes up — including after a
  // reconnect, when the server has forgotten this connection's state. When
  // following, start from whatever symbol is currently the freshest anywhere
  // (falling back to the URL's symbol if nothing's been selected yet);
  // otherwise start from the URL's symbol only.
  useEffect(() => {
    if (feed === 'up' && !armed.current) {
      armed.current = true;
      const startSym = following ? readGlobalSym() || initialSym : initialSym;
      if (startSym) useStore.getState().openChart(startSym);
    }
    if (feed === 'down') armed.current = false;
  }, [feed, following, initialSym]);

  // While following, adopt every symbol selected in any other window.
  useEffect(() => {
    if (!following) return;
    function onStorage(e: StorageEvent) {
      if (e.key === GLOBAL_SYM_KEY && e.newValue) useStore.getState().openChart(e.newValue);
    }
    window.addEventListener('storage', onStorage);
    return () => window.removeEventListener('storage', onStorage);
  }, [following]);

  function toggleFollow() {
    setFollowing((was) => {
      const now = !was;
      if (now) {
        const current = readGlobalSym();
        if (current) useStore.getState().openChart(current);
      }
      return now;
    });
  }

  // No symbol in the URL is fine: this is a standing window — in follow mode
  // it simply adopts the next symbol clicked in any window (the ⧉ Tape button
  // opens it this way when nothing has been selected yet).
  const shownSym = chartSym ?? initialSym ?? '';

  return (
    <div className="app pane-window">
      <StatusBar />
      <div className="tape-follow-bar">
        <button
          className={`segbtn ${following ? 'on' : ''}`}
          onClick={toggleFollow}
          title={
            following
              ? 'Following your symbol selection in every window — click to pin this window to the current symbol'
              : 'Pinned to this symbol — click to follow your selection again'
          }
        >
          {following ? `🔗 Following · ${shownSym || '…'}` : `📌 Pinned · ${shownSym || '…'}`}
        </button>
      </div>
      <div className="main workspace" style={{ padding: 12 }}>
        <TapeReaderView style={{ flex: '1 1 auto', minHeight: 0 }} />
      </div>
    </div>
  );
}

function readGlobalSym(): string {
  try {
    return localStorage.getItem(GLOBAL_SYM_KEY) ?? '';
  } catch {
    return '';
  }
}
