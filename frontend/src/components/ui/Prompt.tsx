// Promise-based prompt() / confirm() replacements. Instead of the native
// dialogs (unstyled, focus-stealing, unthemeable), a portal-rendered modal
// resolves a promise. Usage: `const url = await prompt({ title: 'Link' })`.
import { create } from 'zustand';
import { useT } from '../../i18n';

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
import { Modal } from './Modal';
import { CloseIcon } from '../Icons';

export function PromptHost() {
  const { spec, close } = usePrompt();
  const t = useT();
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
    <Modal
      title={spec.title}
      overlayId="prompt"
      onClose={() => close(null)}
      style={{ width: 420 }}
      backdropStyle={{ paddingTop: '18vh' }}
      headExtra={
        <button className="panel-close" aria-label={t('common.close')} title={t('common.close')} onClick={() => close(null)}><CloseIcon size={15} /></button>
      }
    >
      <div className="modal-body">
          {!isConfirm && (
            spec.multiline ? (
              <textarea
                ref={inputRef as React.RefObject<HTMLTextAreaElement>}
                className="search-input"
                dir="auto"
                rows={3}
                aria-label={spec.title}
                placeholder={spec.placeholder}
                value={value}
                onChange={(e) => setValue(e.target.value)}
                // Ctrl/⌘+Enter commits a multiline field; plain Enter is a
                // newline, which is the whole reason it is multiline. Escape is
                // handled on the container, which is why this branch never had
                // one and the board's rules editor could not be closed at all.
                onKeyDown={(e) => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) close(value); }}
              />
            ) : (
              <input
                ref={inputRef as React.RefObject<HTMLInputElement>}
                className="search-input"
                dir="auto"
                aria-label={spec.title}
                placeholder={spec.placeholder}
                value={value}
                onChange={(e) => setValue(e.target.value)}
                onKeyDown={(e) => { if (e.key === 'Enter') close(value); }}
              />
            )
          )}
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
          <button className="topbar-btn" onClick={() => close(null)}>{t('common.cancel')}</button>
          <button className="topbar-btn primary" onClick={() => close(isConfirm ? 'yes' : value)}>
            {spec.confirmLabel ?? t('common.ok')}
          </button>
        </div>
      </div>
    </Modal>
  );
}
