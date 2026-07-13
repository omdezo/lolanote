// Promise-based prompt() / confirm() replacements. Instead of the native
// dialogs (unstyled, focus-stealing, unthemeable), a portal-rendered modal
// resolves a promise. Usage: `const url = await prompt({ title: 'Link' })`.
import { create } from 'zustand';

interface PromptSpec {
  title: string;
  placeholder?: string;
  defaultValue?: string;
  confirmLabel?: string;
  kind?: 'text' | 'confirm';
  multiline?: boolean;
}

interface PromptState {
  spec: (PromptSpec & { resolve: (v: string | null) => void }) | null;
  open(spec: PromptSpec): Promise<string | null>;
  close(value: string | null): void;
}

export const usePrompt = create<PromptState>((set, get) => ({
  spec: null,
  open: (spec) =>
    new Promise((resolve) => set({ spec: { ...spec, resolve } })),
  close: (value) => {
    get().spec?.resolve(value);
    set({ spec: null });
  },
}));

// Imperative helpers.
export function prompt(spec: PromptSpec): Promise<string | null> {
  return usePrompt.getState().open({ kind: 'text', ...spec });
}
export function confirm(title: string, confirmLabel = 'Confirm'): Promise<boolean> {
  return usePrompt.getState().open({ title, kind: 'confirm', confirmLabel }).then((v) => v !== null);
}

import { useEffect, useRef, useState } from 'react';
import { CloseIcon } from '../Icons';

export function PromptHost() {
  const { spec, close } = usePrompt();
  const [value, setValue] = useState('');
  const inputRef = useRef<HTMLInputElement | HTMLTextAreaElement>(null);

  useEffect(() => {
    if (spec) {
      setValue(spec.defaultValue ?? '');
      setTimeout(() => inputRef.current?.focus(), 20);
    }
  }, [spec]);

  if (!spec) return null;
  const isConfirm = spec.kind === 'confirm';

  return (
    <div className="modal-backdrop" onClick={() => close(null)} style={{ paddingTop: '18vh' }}>
      <div className="modal" style={{ width: 420 }} onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <h3>{spec.title}</h3>
          <button className="panel-close" onClick={() => close(null)}><CloseIcon size={15} /></button>
        </div>
        <div className="modal-body">
          {!isConfirm && (
            spec.multiline ? (
              <textarea
                ref={inputRef as React.RefObject<HTMLTextAreaElement>}
                className="search-input"
                dir="auto"
                rows={3}
                placeholder={spec.placeholder}
                value={value}
                onChange={(e) => setValue(e.target.value)}
              />
            ) : (
              <input
                ref={inputRef as React.RefObject<HTMLInputElement>}
                className="search-input"
                dir="auto"
                placeholder={spec.placeholder}
                value={value}
                onChange={(e) => setValue(e.target.value)}
                onKeyDown={(e) => { if (e.key === 'Enter') close(value); if (e.key === 'Escape') close(null); }}
              />
            )
          )}
          <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
            <button className="topbar-btn" onClick={() => close(null)}>Cancel</button>
            <button className="topbar-btn primary" onClick={() => close(isConfirm ? 'yes' : value)}>
              {spec.confirmLabel ?? 'OK'}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
