import { useState } from 'react';
import { useStore } from '../store';
import { viewTiles } from '../ws';
import { LiveGrid, MatchCount } from './LiveGrid';

// TileFrame wraps one scan's LiveGrid with a small header (name, focus ring,
// pop-out, close) so several can be tiled side by side in the main window.
export function TileFrame({ scanId }: { scanId: string }) {
  const scan = useStore((s) => s.scans.find((x) => x.id === scanId));
  const activeId = useStore((s) => s.activeId);
  const openTiles = useStore((s) => s.openTiles);
  const setActive = useStore((s) => s.setActive);
  const closeTile = useStore((s) => s.closeTile);
  const muted = useStore((s) => !!s.mutedScans[scanId]);
  const toggleScanMute = useStore((s) => s.toggleScanMute);
  const [held, setHeld] = useState(false); // hover-freeze state, reported up by the grid

  if (!scan) return null;
  const focused = scanId === activeId;

  return (
    <div className={`tile ${focused ? 'focused' : ''}`} onMouseDown={() => setActive(scanId)}>
      <div className="tile-head">
        <span className="tile-name">{scan.name}</span>
        <MatchCount scanId={scanId} held={held} />
        <div className="tile-actions">
          <button
            className="iconbtn tile-btn"
            title={muted ? 'sound muted for this scan in this window — click to unmute' : 'mute sound alerts for this scan in this window'}
            onClick={(e) => {
              e.stopPropagation();
              toggleScanMute(scanId);
            }}
          >
            {muted ? '🔇' : '🔊'}
          </button>
          <button
            className="iconbtn tile-btn"
            title="pop out to a new window"
            onClick={(e) => {
              e.stopPropagation();
              window.open(`/?pane=scan&id=${encodeURIComponent(scanId)}`, '_blank', 'width=560,height=780');
            }}
          >
            ⧉
          </button>
          <button
            className="iconbtn tile-btn"
            title="close tile"
            onClick={(e) => {
              e.stopPropagation();
              const rest = openTiles.filter((x) => x !== scanId);
              closeTile(scanId);
              viewTiles(rest);
            }}
          >
            ✕
          </button>
        </div>
      </div>
      <LiveGrid scanId={scanId} style={{ flex: '1 1 auto', minHeight: 0 }} onHeldChange={setHeld} />
    </div>
  );
}
