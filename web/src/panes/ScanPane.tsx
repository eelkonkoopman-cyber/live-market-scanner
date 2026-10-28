import { useEffect, useRef, useState } from 'react';
import { useStore } from '../store';
import { start, viewTiles, resyncScan } from '../ws';
import { LiveGrid, MatchCount } from '../components/LiveGrid';
import { StatusBar } from '../components/StatusBar';

// ScanPane is a minimal pop-out window shell showing just one scan's live
// grid. It's a fresh page load — its own store and WebSocket connection — so
// it streams that scan's ticks independently of whatever window opened it.
//
// (Sound alerts in this pop-out are unlocked by the sound toggle in the
// shared StatusBar — see SoundToggle there for why every window needs its own
// click.)
export function ScanPane({ scanId }: { scanId: string }) {
  const feed = useStore((s) => s.feed);
  const scan = useStore((s) => s.scans.find((x) => x.id === scanId));
  const muted = useStore((s) => !!s.mutedScans[scanId]);
  const toggleScanMute = useStore((s) => s.toggleScanMute);
  const armed = useRef(false);
  const [held, setHeld] = useState(false); // hover-freeze state, reported up by the grid

  useEffect(() => {
    start();
  }, []);

  // Arm on first connect, and re-arm after any reconnect (feed cycles
  // down -> up), so a network blip doesn't leave this window stuck showing
  // stale data.
  useEffect(() => {
    if (feed === 'up' && !armed.current && scanId) {
      armed.current = true;
      viewTiles([scanId]);
      resyncScan(scanId);
    }
    if (feed === 'down') armed.current = false;
  }, [feed, scanId]);

  if (!scanId) {
    return <div className="empty">No scan id given in the URL.</div>;
  }

  return (
    <div className="app pane-window">
      <StatusBar />
      <div className="tile-head">
        <span className="tile-name">{scan?.name ?? scanId}</span>
        <MatchCount scanId={scanId} held={held} />
        <div className="tile-actions">
          <button
            className="iconbtn tile-btn"
            title={muted ? 'sound muted for this scan in this window — click to unmute' : 'mute sound alerts for this scan in this window'}
            onClick={() => toggleScanMute(scanId)}
          >
            {muted ? '🔇' : '🔊'}
          </button>
        </div>
      </div>
      <div className="main workspace" style={{ padding: 12 }}>
        <LiveGrid scanId={scanId} style={{ flex: '1 1 auto', minHeight: 0 }} onHeldChange={setHeld} />
      </div>
    </div>
  );
}
