// Toast system: a tiny zustand store + a portal-rendered stack. Every async
// failure surfaces here instead of a silent console.error, and successes get
// a brief confirmation. Auto-dismiss with a manual close.
import { useEffect } from 'react';
import { create } from 'zustand';
import { CheckIcon, CloseIcon } from '../Icons';

type ToastKind = 'success' | 'error' | 'info';
interface Toast { id: number; kind: ToastKind; message: string }

interface ToastState {
  toasts: Toast[];
  push(kind: ToastKind, message: string): void;
  dismiss(id: number): void;
}

let nextId = 1;
export const useToasts = create<ToastState>((set) => ({
  toasts: [],
  push: (kind, message) => {
    const id = nextId++;
    set((s) => ({ toasts: [...s.toasts, { id, kind, message }] }));
    setTimeout(() => set((s) => ({ toasts: s.toasts.filter((t) => t.id !== id) })), 4000);
  },
  dismiss: (id) => set((s) => ({ toasts: s.toasts.filter((t) => t.id !== id) })),
}));

// Imperative helpers usable from non-component code (API layer, store).
export const toast = {
  success: (m: string) => useToasts.getState().push('success', m),
  error: (m: string) => useToasts.getState().push('error', m),
  info: (m: string) => useToasts.getState().push('info', m),
};

export function Toaster() {
  const { toasts, dismiss } = useToasts();
  return (
    <div className="toaster">
      {toasts.map((t) => (
        <div key={t.id} className={`toast toast-${t.kind}`}>
          <span className="toast-icon">
            {t.kind === 'success' ? <CheckIcon size={15} /> : t.kind === 'error' ? <CloseIcon size={15} /> : null}
          </span>
          <span className="toast-msg">{t.message}</span>
          <button className="toast-close" onClick={() => dismiss(t.id)}><CloseIcon size={13} /></button>
        </div>
      ))}
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
            <div className="gate-title">Something went wrong</div>
            <div className="gate-error">{this.state.error.message}</div>
            <button className="topbar-btn primary gate-submit" onClick={() => window.location.reload()}>
              Reload QomraNote
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
