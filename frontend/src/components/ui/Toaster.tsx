// Toast system: a tiny zustand store + a portal-rendered stack. Every async
// failure surfaces here instead of a silent console.error, and successes get
// a brief confirmation. Auto-dismiss with a manual close.
import { useEffect } from 'react';
import { create } from 'zustand';
// The module-level t(), not useT(): the ErrorBoundary below is a class and
// cannot hold a hook, and by the time either of these renders the language has
// long since been applied at boot.
import { t } from '../../i18n';
import { CheckIcon, CloseIcon } from '../Icons';

type ToastKind = 'success' | 'error' | 'info';
interface Toast { id: number; kind: ToastKind; message: string }

interface ToastState {
  toasts: Toast[];
  push(kind: ToastKind, message: string, sticky?: boolean): number;
  update(id: number, message: string): void;
  dismiss(id: number): void;
}

let nextId = 1;
export const useToasts = create<ToastState>((set) => ({
  toasts: [],
  push: (kind, message, sticky = false) => {
    const id = nextId++;
    set((s) => ({ toasts: [...s.toasts, { id, kind, message }] }));
    // AX22, WCAG 2.2.1. Four seconds is below the threshold for anyone, and it
    // was applied uniformly — including to the failures. This is the universal
    // error channel: "Move failed", every API rejection, every agent start that
    // did not start. A person using a screen reader meets the toast mostly as
    // the report on an action THEY took, and it was gone before the reader
    // reached it. Errors now stay until dismissed; the good news still fades,
    // because a confirmation that will not go away is its own problem.
    //
    // `sticky` is the third case (JN7): a progress counter that outlives four
    // seconds because the work does, and whose owner dismisses it when the work
    // is finished rather than a timer that fires mid-upload.
    if (kind !== 'error' && !sticky) {
      setTimeout(() => set((s) => ({ toasts: s.toasts.filter((t) => t.id !== id) })), 4000);
    }
    return id;
  },
  update: (id, message) => set((s) => ({
    toasts: s.toasts.map((x) => (x.id === id ? { ...x, message } : x)),
  })),
  dismiss: (id) => set((s) => ({ toasts: s.toasts.filter((t) => t.id !== id) })),
}));

// Imperative helpers usable from non-component code (API layer, store).
export const toast = {
  success: (m: string) => useToasts.getState().push('success', m),
  error: (m: string) => useToasts.getState().push('error', m),
  info: (m: string) => useToasts.getState().push('info', m),
  /** A counter that stays put until its owner takes it down. */
  progress: (m: string) => useToasts.getState().push('info', m, true),
  update: (id: number, m: string) => useToasts.getState().update(id, m),
  dismiss: (id: number) => useToasts.getState().dismiss(id),
};

/**
 * AX22. The toast stack was a bare `<div className="toaster">` — no role, no
 * `aria-live` — and it is the universal outcome channel for this product:
 * "Moved 6 items to Unsorted", "Move failed", every API rejection, every agent
 * error. Every one of them was announced to nobody and disappeared in four
 * seconds.
 *
 * TWO containers, because one region cannot vary its politeness and these are
 * two different kinds of news. A success is something to hear when there is a
 * gap; a failure is something to hear now, because the person is about to act
 * on the belief that the thing worked. Both are mounted unconditionally and
 * never unmount — a live region that appears at the same moment its text does
 * is one screen readers do not announce, which is how an aria-live fix ships
 * and does nothing.
 */
export function Toaster() {
  const { toasts, dismiss } = useToasts();
  const row = (item: Toast) => (
    <div key={item.id} className={`toast toast-${item.kind}`}>
      <span className="toast-icon" aria-hidden="true">
        {item.kind === 'success' ? <CheckIcon size={15} /> : item.kind === 'error' ? <CloseIcon size={15} /> : null}
      </span>
      <span className="toast-msg">{item.message}</span>
      <button className="toast-close" aria-label={t('toast.dismiss')} title={t('toast.dismiss')} onClick={() => dismiss(item.id)}><CloseIcon size={13} /></button>
    </div>
  );
  return (
    <div className="toaster">
      <div role="status" aria-live="polite" aria-atomic="false" className="toast-group">
        {toasts.filter((x) => x.kind !== 'error').map(row)}
      </div>
      <div role="alert" aria-live="assertive" aria-atomic="false" className="toast-group">
        {toasts.filter((x) => x.kind === 'error').map(row)}
      </div>
    </div>
  );
}

// ErrorBoundary keeps a single render error from blanking the whole app.
import { Component, type ReactNode } from 'react';

export class ErrorBoundary extends Component<{ children: ReactNode }, { error: Error | null }> {
  constructor(props: { children: ReactNode }) {
    super(props);
    this.state = { error: null };
  }
  static getDerivedStateFromError(error: Error) {
    return { error };
  }
  componentDidCatch(error: Error) {
    console.error('render error', error);
  }
  render() {
    if (this.state.error) {
      return (
        <div className="boot-screen">
          <div className="boot-mark">Q</div>
          <div className="gate-card">
            <div className="gate-title">{t('toast.crashed')}</div>
            <div className="gate-error">{this.state.error.message}</div>
            <button className="topbar-btn primary gate-submit" onClick={() => window.location.reload()}>
              {t('toast.reload')}
            </button>
          </div>
        </div>
      );
    }
    return this.props.children;
  }
}

// useAutoDismiss is a convenience for components that show transient state.
export function useAutoDismiss(active: boolean, fn: () => void, ms = 1500) {
  useEffect(() => {
    if (!active) return;
    const t = setTimeout(fn, ms);
    return () => clearTimeout(t);
  }, [active, fn, ms]);
}
