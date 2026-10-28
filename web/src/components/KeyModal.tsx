import { useState } from 'react';
import { useStore } from '../store';
import { setApiKey } from '../ws';
import { Modal } from './Modal';

export function KeyModal() {
  const close = useStore((s) => s.closeKeyModal);
  const hasKey = useStore((s) => !!s.status?.hasKey);
  const [key, setKey] = useState('');

  function save() {
    const k = key.trim();
    if (!k) return;
    setApiKey(k);
    close();
  }

  return (
    <Modal title="Massive API key" onClose={close}>
      <p className="muted">
        Paste your Massive <b>real-time</b> API key. It's saved locally on this
        machine (~/.marketscanner) and used to connect the live feed — the feed
        starts within a few seconds of saving.
      </p>
      <div className="row">
        <input
          type="password"
          autoFocus
          placeholder="paste key here"
          value={key}
          onChange={(e) => setKey(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') save();
          }}
        />
      </div>
      <div className="actions">
        <span className="muted">{hasKey ? 'A key is already set.' : 'No key set yet.'}</span>
        <span className="spacer" />
        <button onClick={close}>Cancel</button>
        <button className="primary" onClick={save}>
          Save
        </button>
      </div>
    </Modal>
  );
}
