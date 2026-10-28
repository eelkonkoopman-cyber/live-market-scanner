import { Fragment, useEffect, useRef, useState } from 'react';
import { SIGNAL_SWITCHES, type SignalSwitchId, type SignalSwitches } from '../e9';

// SignalSwitchMenu: the Signal window's "Alerts" button and its popover, one
// checkbox per signal (e9.ts SIGNAL_SWITCHES: the three 9EMA flag stages, the
// slam, the green dildo, its 9 EMA pullback and touch, the volume climax and its
// confirmed bottom). Unticked = that signal makes no sound and says nothing in
// THIS window — its scan alert and the matching chart-cell cue alike — and the
// window stops subscribing to its scan (SignalPane owns the state, the
// persistence and the effects; this is only the control). Replaces the old
// "Dildo alerts" / "Climax alerts" toolbar toggles. Same popover look and
// dismissal (outside mousedown, Escape) as the main window's More menu.
export function SignalSwitchMenu({
  switches,
  onToggle,
  defaultOpen = false,
}: {
  switches: SignalSwitches;
  onToggle: (id: SignalSwitchId) => void;
  defaultOpen?: boolean; // tests render the open menu (no DOM to click in)
}) {
  const [open, setOpen] = useState(defaultOpen);
  const wrapRef = useRef<HTMLDivElement>(null);
  const btnRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!open) return;
    function onMouseDown(e: MouseEvent) {
      if (wrapRef.current?.contains(e.target as Node)) return;
      setOpen(false);
    }
    function onKeyDown(e: KeyboardEvent) {
      if (e.key !== 'Escape') return;
      setOpen(false);
      btnRef.current?.focus();
    }
    window.addEventListener('mousedown', onMouseDown);
    window.addEventListener('keydown', onKeyDown);
    return () => {
      window.removeEventListener('mousedown', onMouseDown);
      window.removeEventListener('keydown', onKeyDown);
    };
  }, [open]);

  const onCount = SIGNAL_SWITCHES.filter((s) => switches[s.id]).length;
  const total = SIGNAL_SWITCHES.length;

  return (
    <div className="tabmore-wrap signal-switches" ref={wrapRef}>
      <button
        ref={btnRef}
        className={`segbtn ${onCount > 0 ? 'on' : ''}`}
        aria-haspopup="true"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        title="Choose which signals this window plays and speaks — each one on or off (scan alerts and the matching chart-cell sounds). Other windows are not affected."
      >
        🔔 Alerts {onCount}/{total}
      </button>
      {open && (
        <div className="tabmenu signal-switch-menu" role="menu">
          {SIGNAL_SWITCHES.map((s, i) => (
            <Fragment key={s.id}>
              {(i === 0 || SIGNAL_SWITCHES[i - 1].group !== s.group) && <div className="signal-switch-group">{s.group}</div>}
              <label className={`tabmenu-item signal-switch${switches[s.id] ? ' on' : ''}`} title={s.title}>
                <input type="checkbox" role="menuitemcheckbox" checked={switches[s.id]} onChange={() => onToggle(s.id)} />
                <span className="tabmenu-name">{s.label}</span>
              </label>
            </Fragment>
          ))}
          <div className="signal-switch-note">Off = silent in this window only (tone and voice). The 🔊 and 🗣 buttons still cover everything.</div>
        </div>
      )}
    </div>
  );
}
