// The Unsorted tray (§3.3): each board's slide-out capture inbox. Quick
// captures (Ctrl/⌘+Enter anywhere) land here; "Place" files an item onto
// the canvas. GSAP slides the panel in.
//
// The global capture gesture does NOT live here — see captureToUnsorted below
// and the App-level key map that calls it. It used to, which meant the one
// gesture advertised as working "anywhere" only worked while the panel it was
// meant to replace was already open.
import { useEffect, useMemo, useRef, useState } from 'react';
import { createOp, moveOp, useBoard } from '../../store/boardStore';
import { useView } from '../../store/viewStore';
import { isRTL } from '../../store/settingsStore';
import { tweenFromTo } from '../../lib/motion';
import { t as translate, useT } from '../../i18n';
import { prompt } from '../ui/Prompt';
import { toast } from '../ui/Toaster';
import { CloseIcon, EmptyTrayIllustration, InboxIcon, SparkleIcon } from '../Icons';
import { useAgent } from '../../agent/agentStore';
import { useProposedIds } from '../../agent/GhostLayer';

/**
 * Put a line of text into the current board's Unsorted tray.
 *
 * One function, three callers: the tray's own field, the global Ctrl/⌘+Enter
 * gesture, and the agent composer's "just save this as a card" escape hatch —
 * so a capture always lands the same way and is always one undo.
 */
export function captureText(text: string): boolean {
  const trimmed = text.trim();
  if (!trimmed) return false;
  const state = useBoard.getState();
  if (state.readOnly || !state.boardId) return false;
  void state.commitTransaction([
    createOp('CARD', state.boardId, {
      section: 'UNSORTED',
      content: {
        textPreview: trimmed,
        doc: { type: 'doc', content: [{ type: 'paragraph', content: [{ type: 'text', text: trimmed }] }] },
      },
    }),
  ]);
  return true;
}

/**
 * The global quick-capture gesture.
 *
 * `window.prompt` used to stand in for the dialog: an unstyled, unthemed OS
 * box that is left-to-right whatever the board is, on a product whose whole
 * point for this user is that Arabic renders correctly. The in-app host is
 * dir-aware, themed, and dismissable by the same Escape as everything else.
 */
export async function captureToUnsorted(): Promise<void> {
  const text = await prompt({ title: translate('tray.captureTitle'), placeholder: translate('tray.placeholder') });
  if (text && captureText(text)) toast.success(translate('tray.captured'));
}

export function UnsortedTray({ onClose }: { onClose: () => void }) {
  const t = useT();
  const boardId = useBoard((s) => s.boardId);
  const elements = useBoard((s) => s.elements);
  const commitTransaction = useBoard((s) => s.commitTransaction);
  const [capture, setCapture] = useState('');
  const agentEnabled = useAgent((s) => s.capabilities?.enabled ?? false);
  const proposedIds = useProposedIds();
  const panelRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (panelRef.current) {
      tweenFromTo(panelRef.current, { x: isRTL() ? -340 : 340 }, { x: 0, duration: 0.28, ease: 'power3.out' });
    }
  }, []);

  const items = useMemo(
    () =>
      Object.values(elements)
        .filter((el) => el.location.parentId === boardId && el.location.section === 'UNSORTED' && !el.deletedAt)
        .sort((a, b) => a.location.index - b.location.index),
    [elements, boardId],
  );

  const quickCapture = () => {
    if (captureText(capture)) setCapture('');
  };

  const place = (id: string) => {
    const el = elements[id];
    if (!el) return;
    const v = useView.getState();
    const viewport = document.querySelector('.canvas-viewport') as HTMLElement | null;
    const x = ((viewport?.clientWidth ?? 1200) / 2 - v.panX) / v.scale - 130;
    const y = ((viewport?.clientHeight ?? 800) / 2 - v.panY) / v.scale - 60;
    void commitTransaction([moveOp(el, { section: 'CANVAS', position: { x, y } })]);
  };

  return (
    <div ref={panelRef} className="side-panel">
      <div className="panel-head">
        <h3><InboxIcon size={17} /> {t('topbar.unsorted')}</h3>
        <button className="panel-close" onClick={onClose} aria-label={t('common.close')} title={t('common.close')}><CloseIcon size={15} /></button>
      </div>
      <div className="panel-body">
        <input
          className="quick-capture"
          dir="auto"
          aria-label={t('tray.placeholder')}
          placeholder={t('tray.placeholder')}
          value={capture}
          onChange={(e) => setCapture(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && quickCapture()}
        />
        {/* The highest-intent entry point: offered only once there is a real
            pile to sort, so it reads as help rather than as advertising. */}
        {agentEnabled && items.length >= 4 && (
          <button className="tray-organize" onClick={() => useAgent.getState().setOpen(true)}>
            <SparkleIcon size={14} />
            {t('tray.askSort')} {items.length}
          </button>
        )}
        {items.length === 0 && (
          <div className="panel-empty">
            <EmptyTrayIllustration />
            {t('tray.empty')}<br />{t('tray.emptySub')}
          </div>
        )}
        {/* The tray had no proposal awareness at all, and "file my tray" is the
            single most common thing anybody asks the agent to do — so the one
            surface the request is about showed nothing while it was pending. */}
        {items.map((el) => (
          <div key={el.id} className={`panel-item${proposedIds.has(el.id) ? ' agent-proposed' : ''}`}>
            <div>{el.content?.textPreview || el.content?.title || el.content?.filename || el.content?.url || `(${el.type.toLowerCase()})`}</div>
            <div className="pi-actions" style={{ marginTop: 8 }}>
              <button className="pi-btn" onClick={() => place(el.id)}>{t('tray.place')}</button>
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}
