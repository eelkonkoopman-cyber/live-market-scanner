// Regression coverage for Defect 1 (toasts never dismissed): pushToast must
// collapse an exact repeat of the newest message instead of stacking it and
// cap the list, and removeToast must remove by id — the building blocks
// Toasts.tsx's per-toast auto-dismiss timer and "✕" button rely on. Mirrors
// scanmute.test.ts's style (drives the real store actions via useStore).
import { describe, expect, it } from 'vitest';
import { useStore } from './store';

describe('pushToast / removeToast', () => {
  it('pushing two different messages yields two toasts', () => {
    useStore.setState({ toasts: [] });
    const { pushToast } = useStore.getState();
    pushToast('first problem');
    pushToast('second problem');
    expect(useStore.getState().toasts.map((t) => t.msg)).toEqual(['first problem', 'second problem']);
  });

  it('pushing the same message twice yields one toast (fresh id, so Toasts.tsx\'s timer restarts)', () => {
    useStore.setState({ toasts: [] });
    const { pushToast } = useStore.getState();
    pushToast('duplicate');
    const firstId = useStore.getState().toasts[0].id;
    pushToast('duplicate');
    const toasts = useStore.getState().toasts;
    expect(toasts).toHaveLength(1);
    expect(toasts[0].msg).toBe('duplicate');
    expect(toasts[0].id).not.toBe(firstId);
  });

  it('a non-consecutive repeat does NOT collapse — only a repeat of the newest toast does', () => {
    useStore.setState({ toasts: [] });
    const { pushToast } = useStore.getState();
    pushToast('a');
    pushToast('b');
    pushToast('a');
    expect(useStore.getState().toasts.map((t) => t.msg)).toEqual(['a', 'b', 'a']);
  });

  it('a fifth toast drops the oldest (cap of 4)', () => {
    useStore.setState({ toasts: [] });
    const { pushToast } = useStore.getState();
    pushToast('a');
    pushToast('b');
    pushToast('c');
    pushToast('d');
    pushToast('e');
    expect(useStore.getState().toasts.map((t) => t.msg)).toEqual(['b', 'c', 'd', 'e']);
  });

  it('removeToast removes by id, leaving the others untouched', () => {
    useStore.setState({ toasts: [] });
    const { pushToast, removeToast } = useStore.getState();
    pushToast('one');
    pushToast('two');
    const [first, second] = useStore.getState().toasts;
    removeToast(first.id);
    const toasts = useStore.getState().toasts;
    expect(toasts).toHaveLength(1);
    expect(toasts[0].id).toBe(second.id);
    expect(toasts[0].msg).toBe('two');
  });

  it('removeToast with an unknown id is a harmless no-op', () => {
    useStore.setState({ toasts: [] });
    const { pushToast, removeToast } = useStore.getState();
    pushToast('solo');
    removeToast(999999);
    expect(useStore.getState().toasts).toHaveLength(1);
  });
});
