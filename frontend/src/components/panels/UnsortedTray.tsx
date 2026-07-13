// The Unsorted tray (§3.3): each board's slide-out capture inbox. Quick
// captures (Ctrl/⌘+Enter anywhere) land here; "Place" files an item onto
// the canvas. GSAP slides the panel in.
import { useEffect, useMemo, useRef, useState } from 'react';
import gsap from 'gsap';
import { createOp, moveOp, useBoard } from '../../store/boardStore';
import { useView } from '../../store/viewStore';
import { CloseIcon, EmptyTrayIllustration, InboxIcon } from '../Icons';

export function UnsortedTray({ onClose }: { onClose: () => void }) {
  const { boardId, elements, commitTransaction } = useBoard();
  const [capture, setCapture] = useState('');
  const panelRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (panelRef.current) {
      gsap.fromTo(panelRef.current, { x: 340 }, { x: 0, duration: 0.28, ease: 'power3.out' });
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
    const text = capture.trim();
    if (!text) return;
    void commitTransaction([
      createOp('CARD', boardId, {
        section: 'UNSORTED',
        content: {
          textPreview: text,
          doc: { type: 'doc', content: [{ type: 'paragraph', content: [{ type: 'text', text }] }] },
        },
      }),
    ]);
    setCapture('');
  };

  // Ctrl/⌘+Enter captures from anywhere while the tray is open (§3.3).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
        const text = window.prompt('Quick note → Unsorted');
        if (text?.trim()) {
          void useBoard.getState().commitTransaction([
            createOp('CARD', useBoard.getState().boardId, {
              section: 'UNSORTED',
              content: {
                textPreview: text.trim(),
                doc: { type: 'doc', content: [{ type: 'paragraph', content: [{ type: 'text', text: text.trim() }] }] },
              },
            }),
          ]);
        }
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

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
        <h3><InboxIcon size={17} /> Unsorted</h3>
        <button className="panel-close" onClick={onClose} title="Close"><CloseIcon size={15} /></button>
      </div>
      <div className="panel-body">
        <input
          className="quick-capture"
          dir="auto"
          placeholder="Quick note… (Enter to capture)"
          value={capture}
          onChange={(e) => setCapture(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && quickCapture()}
        />
        {items.length === 0 && (
          <div className="panel-empty">
            <EmptyTrayIllustration />
            Captures land here first.<br />File them onto the board when you're ready.
          </div>
        )}
        {items.map((el) => (
          <div key={el.id} className="panel-item">
            <div>{el.content?.textPreview || el.content?.title || el.content?.filename || el.content?.url || `(${el.type.toLowerCase()})`}</div>
            <div className="pi-actions" style={{ marginTop: 8 }}>
              <button className="pi-btn" onClick={() => place(el.id)}>Place on board</button>
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}
