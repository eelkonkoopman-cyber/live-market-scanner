// Splitter (drag a divider between two boxes) and ColResizer (drag a column's
// right edge) — the two drag primitives behind "everything is resizable".
// Both compute the new size from the drag-START value + cumulative delta (not
// the latest prop value), so they stay correct regardless of React's render
// timing during a fast drag.

import type { MouseEvent as ReactMouseEvent } from 'react';

export function Splitter({
  axis,
  value,
  onChange,
  onCommit,
  min,
  max,
}: {
  axis: 'x' | 'y'; // 'x' = drag left/right (resizes a width); 'y' = drag up/down (resizes a height)
  value: number;
  onChange: (next: number) => void;
  onCommit: () => void;
  min: number;
  max: number;
}) {
  const onDown = (e: ReactMouseEvent) => {
    e.preventDefault();
    const startPos = axis === 'x' ? e.clientX : e.clientY;
    const startVal = value;
    const cls = axis === 'x' ? 'resizing-x' : 'resizing-y';
    document.body.classList.add(cls);
    const onMove = (ev: MouseEvent) => {
      const pos = axis === 'x' ? ev.clientX : ev.clientY;
      onChange(Math.min(max, Math.max(min, startVal + (pos - startPos))));
    };
    const onUp = () => {
      document.body.classList.remove(cls);
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
      onCommit();
    };
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
  };
  return <div className={`splitter splitter-${axis}`} onMouseDown={onDown} />;
}

export function ColResizer({
  width,
  onChange,
  onCommit,
  min = 40,
  max = 900,
}: {
  width: number;
  onChange: (next: number) => void;
  onCommit: () => void;
  min?: number;
  max?: number;
}) {
  const onDown = (e: ReactMouseEvent) => {
    e.preventDefault();
    e.stopPropagation(); // don't trigger the header's onClick (sort)
    const startX = e.clientX;
    const startW = width;
    document.body.classList.add('resizing-x');
    const onMove = (ev: MouseEvent) => {
      onChange(Math.min(max, Math.max(min, startW + (ev.clientX - startX))));
    };
    const onUp = () => {
      document.body.classList.remove('resizing-x');
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
      onCommit();
    };
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
  };
  return <span className="colresizer" onMouseDown={onDown} onClick={(e) => e.stopPropagation()} />;
}
