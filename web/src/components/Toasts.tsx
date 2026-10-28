import { useEffect } from 'react';
import { useStore, type Toast } from '../store';

// A toast auto-dismisses after this long, or sooner on click. Per-toast (not
// one shared timer) so a fresh duplicate — store.pushToast's replace-newest
// path hands it a brand new id — gets its own full countdown instead of
// inheriting one that's already half-elapsed.
const TOAST_MS = 6000;

export function Toasts() {
  const toasts = useStore((s) => s.toasts);
  return (
    <div className="toasts" role="status">
      {toasts.map((t) => (
        <ToastItem key={t.id} toast={t} />
      ))}
    </div>
  );
}

function ToastItem({ toast }: { toast: Toast }) {
  useEffect(() => {
    const timer = setTimeout(() => useStore.getState().removeToast(toast.id), TOAST_MS);
    return () => clearTimeout(timer);
  }, [toast.id]);

  return (
    <div className="toast" onClick={() => useStore.getState().removeToast(toast.id)}>
      {toast.msg}
      <button
        style={{ background: 'none', border: 'none', color: 'inherit', cursor: 'pointer', marginLeft: 10, font: 'inherit' }}
        aria-label="dismiss"
        onClick={(e) => {
          e.stopPropagation(); // don't also trigger the toast's own click-to-dismiss
          useStore.getState().removeToast(toast.id);
        }}
      >
        ✕
      </button>
    </div>
  );
}
