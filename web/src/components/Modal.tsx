// Shared dialog chrome for the scan editor and the API-key prompt: backdrop,
// panel, title, Escape-to-close, and a focus trap so Tab can't leak focus
// behind the modal onto the grid underneath.
import { useEffect, useRef, type ReactNode } from 'react';

// Focusable elements for the Tab-trap, in DOM order — mirrors the browser's
// own tab order closely enough for a dialog (no positive tabindex anywhere in
// this app, so plain DOM order already matches).
const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

export function Modal({
  title,
  onClose,
  className,
  children,
}: {
  title: string;
  onClose: () => void;
  className?: string;
  children: ReactNode;
}) {
  const panelRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const panel = panelRef.current;
    // Remember what had focus before the dialog opened, so closing it doesn't
    // strand focus at the top of the document.
    const prevFocused = document.activeElement as HTMLElement | null;

    if (panel) {
      const auto = panel.querySelector<HTMLElement>('[autofocus]');
      const first = auto ?? panel.querySelectorAll<HTMLElement>(FOCUSABLE)[0];
      first?.focus();
    }

    function onKeyDown(e: KeyboardEvent) {
      if (e.key === 'Escape') {
        onClose();
        return;
      }
      if (e.key !== 'Tab' || !panel) return;
      const focusable = Array.from(panel.querySelectorAll<HTMLElement>(FOCUSABLE));
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      // Wrap Tab/Shift+Tab at the panel's edges instead of letting focus leak
      // out to whatever is behind the backdrop.
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    }
    window.addEventListener('keydown', onKeyDown);
    return () => {
      window.removeEventListener('keydown', onKeyDown);
      if (prevFocused && document.contains(prevFocused)) prevFocused.focus();
    };
  }, [onClose]);

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div
        className={`modal ${className ?? ''}`}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onClick={(e) => e.stopPropagation()}
        ref={panelRef}
      >
        <h3>{title}</h3>
        {children}
      </div>
    </div>
  );
}
